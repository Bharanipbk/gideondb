package rest

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/vectordb/vectordb/internal/cluster"
)

type rebalanceRequest struct {
	ExpectedEpoch     uint64 `json:"expected_epoch"`
	JoinNodeID        string `json:"join_node_id,omitempty"`
	JoinAddress       string `json:"join_address,omitempty"`
	LeaveNodeID       string `json:"leave_node_id,omitempty"`
	CapacityNodeID    string `json:"capacity_node_id,omitempty"`
	PlacementCapacity uint32 `json:"placement_capacity,omitempty"`
}

type rebalanceTransition struct {
	change              map[string]string
	plan                cluster.RebalancePlan
	target              cluster.ReplicaPlacementTable
	targetPeers         []cluster.Peer
	voters              []string
	targetLocalCapacity uint32
}

type raftVoterChanger interface {
	ChangeVoters(context.Context, []string, cluster.ViewDigests) (uint64, error)
}

type raftViewAdvancer interface {
	AdvanceView(context.Context, cluster.ViewDigests) (uint64, error)
}

func (s *Server) clusterRebalancePlan(w http.ResponseWriter, r *http.Request) {
	request := rebalanceRequest{JoinNodeID: r.URL.Query().Get("join_node_id"), JoinAddress: r.URL.Query().Get("join_address"), LeaveNodeID: r.URL.Query().Get("leave_node_id"), CapacityNodeID: r.URL.Query().Get("capacity_node_id")}
	if raw := r.URL.Query().Get("placement_capacity"); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_placement_capacity", Message: "placement_capacity must be an unsigned integer"})
			return
		}
		request.PlacementCapacity = uint32(value)
	}
	transition, status, apiErr := s.prepareRebalance(request, false)
	if apiErr != nil {
		writeJSON(w, status, *apiErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"change": transition.change, "plan": transition.plan, "target_placement": transition.target})
}

func (s *Server) clusterRebalancePrepare(w http.ResponseWriter, r *http.Request) {
	if s.rebalanceExecutor == nil || s.rebalanceBarriers == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "rebalance_unavailable", Message: "durable rebalance execution is not configured"})
		return
	}
	var request rebalanceRequest
	if !decode(w, r, &request) {
		return
	}
	if request.ExpectedEpoch != s.currentMetadataEpoch() {
		writeJSON(w, http.StatusConflict, apiError{Code: "epoch_precondition_failed", Message: "expected_epoch does not match the committed metadata epoch"})
		return
	}
	role, leaderID, term := cluster.RaftFollower, "", uint64(0)
	if provider, ok := s.raftProtocol.(interface {
		Status() (cluster.RaftRole, string, uint64)
	}); ok {
		role, leaderID, term = provider.Status()
	}
	if role != cluster.RaftLeader {
		writeJSON(w, http.StatusConflict, map[string]any{"code": "not_raft_leader", "message": "rebalance preparation requires the Raft leader", "raft_role": role, "leader_id": leaderID, "term": term})
		return
	}
	transition, status, apiErr := s.prepareRebalance(request, true)
	if apiErr != nil {
		writeJSON(w, status, *apiErr)
		return
	}
	digest, err := cluster.RebalancePlanDigest(transition.plan)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "rebalance_plan_invalid", Message: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	preparation, err := s.rebalanceExecutor.Prepare(ctx, transition.plan, s.distributedRebalanceMover(request, transition, digest, term))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "rebalance_preparation_failed", "message": err.Error(), "preparation": preparation})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": transition.plan, "preparation": preparation})
}

func (s *Server) clusterRebalanceAbort(w http.ResponseWriter, r *http.Request) {
	if s.rebalanceExecutor == nil || s.rebalanceBarriers == nil || s.raftStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "rebalance_unavailable", Message: "durable rebalance execution is not configured"})
		return
	}
	var request rebalanceRequest
	if !decode(w, r, &request) {
		return
	}
	if request.ExpectedEpoch != s.currentMetadataEpoch() {
		writeJSON(w, http.StatusConflict, apiError{Code: "epoch_precondition_failed", Message: "expected_epoch does not match the committed metadata epoch"})
		return
	}
	role, leaderID, term := cluster.RaftFollower, "", uint64(0)
	if provider, ok := s.raftProtocol.(interface {
		Status() (cluster.RaftRole, string, uint64)
	}); ok {
		role, leaderID, term = provider.Status()
	}
	if role != cluster.RaftLeader {
		writeJSON(w, http.StatusConflict, map[string]any{"code": "not_raft_leader", "message": "rebalance abort requires the Raft leader", "raft_role": role, "leader_id": leaderID, "term": term})
		return
	}
	_, jointOld, jointNew := s.raftStore.VoterConfiguration()
	if len(jointOld) != 0 || len(jointNew) != 0 {
		writeJSON(w, http.StatusConflict, apiError{Code: "membership_change_active", Message: "cannot abort while joint consensus is active"})
		return
	}
	transition, status, apiErr := s.prepareRebalance(request, false)
	if apiErr != nil {
		writeJSON(w, status, *apiErr)
		return
	}
	digest, err := cluster.RebalancePlanDigest(transition.plan)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "rebalance_plan_invalid", Message: err.Error()})
		return
	}
	failures := s.abortRebalanceBarriers(r.Context(), transition.plan, digest, term)
	if len(failures) != 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "rebalance_abort_incomplete", "message": "one or more source barriers could not be released", "failures": failures})
		return
	}
	if err := s.rebalanceExecutor.Abort(transition.plan); err != nil {
		writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_abort_failed", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "aborted", "plan_digest": digest, "metadata_epoch": s.currentMetadataEpoch()})
}

func (s *Server) clusterRebalanceApply(w http.ResponseWriter, r *http.Request) {
	if s.raftProtocol == nil || s.raftStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "raft_unavailable", Message: "Raft metadata change service is not configured"})
		return
	}
	var request rebalanceRequest
	if !decode(w, r, &request) {
		return
	}
	current := s.currentMetadataEpoch()
	if request.ExpectedEpoch != current {
		writeJSON(w, http.StatusConflict, apiError{Code: "epoch_precondition_failed", Message: "expected_epoch does not match the committed metadata epoch"})
		return
	}
	_, jointOld, jointNew := s.raftStore.VoterConfiguration()
	if len(jointOld) != 0 || len(jointNew) != 0 {
		writeJSON(w, http.StatusConflict, apiError{Code: "membership_change_active", Message: "a joint-consensus membership transition is already active"})
		return
	}
	transition, status, apiErr := s.prepareRebalance(request, true)
	if apiErr != nil {
		writeJSON(w, status, *apiErr)
		return
	}
	if s.rebalanceExecutor == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "rebalance_unavailable", Message: "durable rebalance execution is not configured"})
		return
	}
	ready, err := s.rebalanceExecutor.Ready(transition.plan)
	if err != nil || !ready {
		writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_not_prepared", Message: "the exact rebalance plan must complete data preparation before membership cutover"})
		return
	}
	digests, err := cluster.ComputeViewDigestsWithCapacity(current+1, s.nodeID, s.advertiseAddress, transition.targetLocalCapacity, transition.targetPeers, s.engine.ListCollections())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "cluster_view_invalid", Message: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var epoch uint64
	if request.CapacityNodeID != "" {
		advancer, ok := s.raftProtocol.(raftViewAdvancer)
		if !ok {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "raft_unavailable", Message: "Raft view change service is not configured"})
			return
		}
		epoch, err = advancer.AdvanceView(ctx, digests)
	} else {
		changer, ok := s.raftProtocol.(raftVoterChanger)
		if !ok {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "raft_unavailable", Message: "Raft membership change service is not configured"})
			return
		}
		epoch, err = changer.ChangeVoters(ctx, transition.voters, digests)
	}
	if errors.Is(err, cluster.ErrNotRaftLeader) {
		role, leaderID, term := cluster.RaftFollower, "", uint64(0)
		if provider, available := s.raftProtocol.(interface {
			Status() (cluster.RaftRole, string, uint64)
		}); available {
			role, leaderID, term = provider.Status()
		}
		writeJSON(w, http.StatusConflict, map[string]any{"code": "not_raft_leader", "message": err.Error(), "raft_role": role, "leader_id": leaderID, "term": term})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_change_failed", Message: err.Error()})
		return
	}
	releaseFailures := s.releaseRebalanceBarriers(r.Context(), transition.plan, epoch)
	if err := s.rebalanceExecutor.ResetCommitted(epoch); err != nil {
		releaseFailures = append(releaseFailures, "journal: "+err.Error())
	}
	writeJSON(w, http.StatusOK, map[string]any{"metadata_epoch": epoch, "change": transition.change, "plan": transition.plan, "target_placement": transition.target, "voters": transition.voters, "membership_digest": digests.Membership, "catalog_digest": digests.Catalog, "placement_digest": digests.Placement, "barrier_release_failures": releaseFailures})
}

func (s *Server) prepareRebalance(request rebalanceRequest, requireDiscoveredJoin bool) (rebalanceTransition, int, *apiError) {
	joining := request.JoinNodeID != "" || request.JoinAddress != ""
	leaving := request.LeaveNodeID != ""
	changingCapacity := request.CapacityNodeID != "" || request.PlacementCapacity != 0
	changeCount := 0
	for _, selected := range []bool{joining, leaving, changingCapacity} {
		if selected {
			changeCount++
		}
	}
	if changeCount != 1 {
		return rebalanceTransition{}, http.StatusBadRequest, &apiError{Code: "invalid_rebalance_change", Message: "specify exactly one node join, leave, or capacity change"}
	}
	if joining && (!cluster.ValidNodeID(request.JoinNodeID) || request.JoinAddress == "") {
		return rebalanceTransition{}, http.StatusBadRequest, &apiError{Code: "invalid_join_node", Message: "join_node_id and join_address are required"}
	}
	if joining {
		if request.JoinNodeID == s.nodeID {
			return rebalanceTransition{}, http.StatusConflict, &apiError{Code: "node_already_member", Message: "join node is already in placement"}
		}
		if _, err := ParseAddress(request.JoinAddress); err != nil {
			return rebalanceTransition{}, http.StatusBadRequest, &apiError{Code: "invalid_join_address", Message: err.Error()}
		}
	}
	if leaving && (!cluster.ValidNodeID(request.LeaveNodeID) || request.LeaveNodeID == s.nodeID) {
		return rebalanceTransition{}, http.StatusBadRequest, &apiError{Code: "invalid_leave_node", Message: "leave_node_id must identify a configured remote node"}
	}
	if changingCapacity && (!cluster.ValidNodeID(request.CapacityNodeID) || request.PlacementCapacity < 1 || request.PlacementCapacity > cluster.MaxPlacementCapacity) {
		return rebalanceTransition{}, http.StatusBadRequest, &apiError{Code: "invalid_capacity_change", Message: "capacity_node_id and placement_capacity from 1 to 256 are required"}
	}
	currentPeers, err := s.membershipPeers()
	if err != nil {
		return rebalanceTransition{}, http.StatusServiceUnavailable, &apiError{Code: "membership_unavailable", Message: err.Error()}
	}
	localCapacity := s.currentPlacementCapacity()
	current, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, localCapacity, currentPeers, s.engine.ListCollections(), s.replicationFactor)
	if err != nil {
		return rebalanceTransition{}, http.StatusServiceUnavailable, &apiError{Code: "placement_unavailable", Message: err.Error()}
	}
	targetPeers := append([]cluster.Peer(nil), currentPeers...)
	targetLocalCapacity := localCapacity
	if changingCapacity {
		_, target, plan, err := cluster.PlanCapacityRebalance(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, localCapacity, currentPeers, s.engine.ListCollections(), s.replicationFactor, request.CapacityNodeID, request.PlacementCapacity)
		if err != nil {
			return rebalanceTransition{}, http.StatusBadRequest, &apiError{Code: "capacity_change_invalid", Message: err.Error()}
		}
		if request.CapacityNodeID == s.nodeID {
			targetLocalCapacity = request.PlacementCapacity
		} else {
			for index := range targetPeers {
				if targetPeers[index].NodeID == request.CapacityNodeID {
					targetPeers[index].PlacementCapacity = request.PlacementCapacity
				}
			}
		}
		voters := make([]string, 0, len(target.Nodes))
		for _, node := range target.Nodes {
			voters = append(voters, node.NodeID)
		}
		sort.Strings(voters)
		change := map[string]string{"capacity_node_id": request.CapacityNodeID, "placement_capacity": strconv.FormatUint(uint64(request.PlacementCapacity), 10)}
		return rebalanceTransition{change: change, plan: plan, target: target, targetPeers: targetPeers, voters: voters, targetLocalCapacity: targetLocalCapacity}, http.StatusOK, nil
	} else if joining {
		for _, peer := range currentPeers {
			if peer.NodeID == request.JoinNodeID {
				return rebalanceTransition{}, http.StatusConflict, &apiError{Code: "node_already_member", Message: "join node is already in placement"}
			}
		}
		joinPeer := cluster.Peer{NodeID: request.JoinNodeID, AdvertiseAddress: request.JoinAddress, Healthy: true}
		found := false
		if s.peerProvider != nil {
			for _, peer := range s.peerProvider.Peers() {
				if peer.NodeID == request.JoinNodeID {
					found = true
					if peer.AdvertiseAddress != request.JoinAddress {
						return rebalanceTransition{}, http.StatusConflict, &apiError{Code: "join_identity_mismatch", Message: "join address does not match the discovered learner"}
					}
					joinPeer = peer
					break
				}
			}
		}
		if requireDiscoveredJoin && !found {
			return rebalanceTransition{}, http.StatusConflict, &apiError{Code: "join_node_not_discovered", Message: "join node must be discovered as a learner before apply"}
		}
		targetPeers = append(targetPeers, joinPeer)
	} else {
		filtered := make([]cluster.Peer, 0, len(targetPeers))
		found := false
		for _, peer := range targetPeers {
			if peer.NodeID == request.LeaveNodeID {
				found = true
				continue
			}
			filtered = append(filtered, peer)
		}
		if !found {
			return rebalanceTransition{}, http.StatusNotFound, &apiError{Code: "node_not_member", Message: "leave node is not in placement"}
		}
		targetPeers = filtered
	}
	target, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch()+1, s.nodeID, s.advertiseAddress, targetLocalCapacity, targetPeers, s.engine.ListCollections(), s.replicationFactor)
	if err != nil {
		return rebalanceTransition{}, http.StatusBadRequest, &apiError{Code: "target_placement_invalid", Message: err.Error()}
	}
	plan, err := cluster.PlanRebalance(current, target)
	if err != nil {
		return rebalanceTransition{}, http.StatusBadRequest, &apiError{Code: "rebalance_plan_invalid", Message: err.Error()}
	}
	voters := make([]string, 0, len(target.Nodes))
	for _, node := range target.Nodes {
		voters = append(voters, node.NodeID)
	}
	sort.Strings(voters)
	change := map[string]string{"join_node_id": request.JoinNodeID, "join_address": request.JoinAddress, "leave_node_id": request.LeaveNodeID}
	return rebalanceTransition{change: change, plan: plan, target: target, targetPeers: targetPeers, voters: voters, targetLocalCapacity: targetLocalCapacity}, http.StatusOK, nil
}
