package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
)

type rebalanceSnapshotRequest struct {
	Change   rebalanceRequest `json:"change"`
	SourceID string           `json:"source_id"`
	Sequence uint64           `json:"sequence"`
	Records  []core.Record    `json:"records"`
}

type rebalanceActionRequest struct {
	Change   rebalanceRequest `json:"change"`
	ActionID string           `json:"action_id"`
}

type rebalanceAbortRequest struct {
	PlanDigest string `json:"plan_digest"`
}

type distributedRebalanceDataMover struct {
	server *Server
	change rebalanceRequest
	digest string
	term   uint64
	peers  map[string]cluster.Peer
}

func (s *Server) distributedRebalanceMover(change rebalanceRequest, _ rebalanceTransition, digest string, term uint64) *distributedRebalanceDataMover {
	peers := map[string]cluster.Peer{}
	if s.peerProvider != nil {
		for _, peer := range s.peerProvider.Peers() {
			peers[peer.NodeID] = peer
		}
	}
	return &distributedRebalanceDataMover{server: s, change: change, digest: digest, term: term, peers: peers}
}

func (m *distributedRebalanceDataMover) CopySnapshot(ctx context.Context, movement cluster.ShardMovement, target string) error {
	return m.execute(ctx, movement, target, "copy_snapshot")
}

func (m *distributedRebalanceDataMover) CatchUpWAL(ctx context.Context, movement cluster.ShardMovement, target string) error {
	return m.execute(ctx, movement, target, "catch_up_wal")
}

func (m *distributedRebalanceDataMover) execute(ctx context.Context, movement cluster.ShardMovement, target, actionType string) error {
	actionID := ""
	for _, action := range movement.Actions {
		if action.Type == actionType && action.TargetID == target {
			actionID = action.ID
			break
		}
	}
	if actionID == "" {
		return fmt.Errorf("planned rebalance action not found")
	}
	if movement.SourceLeaderID == m.server.nodeID {
		local := &localRebalanceDataMover{server: m.server, change: m.change, peers: m.peers, term: m.term, planDigest: m.digest}
		if actionType == "copy_snapshot" {
			return local.CopySnapshot(ctx, movement, target)
		}
		return local.CatchUpWAL(ctx, movement, target)
	}
	peer, exists := m.peers[movement.SourceLeaderID]
	if !exists {
		return fmt.Errorf("source leader %s is not discoverable", movement.SourceLeaderID)
	}
	payload, err := json.Marshal(rebalanceActionRequest{Change: m.change, ActionID: actionID})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, peer.SeedURL+"/v1/internal/rebalance/action", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	m.server.setRebalanceHeaders(request, peer.NodeID, m.term)
	response, err := m.server.internalClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("source returned HTTP %d: %s", response.StatusCode, body)
	}
	return nil
}

func (s *Server) setRebalanceHeaders(request *http.Request, target string, term uint64) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GideonDB-Cluster-ID", s.clusterID)
	request.Header.Set("X-GideonDB-Target-Node-ID", target)
	request.Header.Set("X-GideonDB-Metadata-Epoch", strconv.FormatUint(s.currentMetadataEpoch(), 10))
	request.Header.Set("X-GideonDB-Coordinator-Node-ID", s.nodeID)
	request.Header.Set("X-GideonDB-Leader-Term", strconv.FormatUint(term, 10))
	if s.peerAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+s.peerAPIKey)
	}
}

func (s *Server) internalRebalanceAction(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	if s.rebalanceBarriers == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "rebalance_unavailable", Message: "rebalance barriers are not configured"})
		return
	}
	var request rebalanceActionRequest
	if !decode(w, r, &request) {
		return
	}
	role, leaderID, term := cluster.RaftFollower, "", uint64(0)
	if provider, ok := s.raftProtocol.(interface {
		Status() (cluster.RaftRole, string, uint64)
	}); ok {
		role, leaderID, term = provider.Status()
	}
	coordinator := r.Header.Get("X-GideonDB-Coordinator-Node-ID")
	requestTerm, err := strconv.ParseUint(r.Header.Get("X-GideonDB-Leader-Term"), 10, 64)
	if err != nil || requestTerm != term || coordinator == "" || (role != cluster.RaftLeader && leaderID != coordinator) || (role == cluster.RaftLeader && coordinator != s.nodeID) {
		writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_coordinator_fence", Message: "request is not from the current Raft leader and term"})
		return
	}
	plan, status, apiErr := s.observerRebalancePlan(request.Change)
	if apiErr != nil {
		writeJSON(w, status, *apiErr)
		return
	}
	digest, err := cluster.RebalancePlanDigest(plan)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "rebalance_plan_invalid", Message: err.Error()})
		return
	}
	peers := map[string]cluster.Peer{}
	if s.peerProvider != nil {
		for _, peer := range s.peerProvider.Peers() {
			peers[peer.NodeID] = peer
		}
	}
	mover := &localRebalanceDataMover{server: s, change: request.Change, peers: peers, term: term, planDigest: digest}
	for _, movement := range plan.Movements {
		if movement.SourceLeaderID != s.nodeID {
			continue
		}
		for _, action := range movement.Actions {
			if action.ID != request.ActionID {
				continue
			}
			if action.Type == "copy_snapshot" {
				err = mover.CopySnapshot(r.Context(), movement, action.TargetID)
			} else if action.Type == "catch_up_wal" {
				err = mover.CatchUpWAL(r.Context(), movement, action.TargetID)
			} else {
				err = fmt.Errorf("action is not a data preparation prerequisite")
			}
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "rebalance_action_failed", Message: err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "action_id": action.ID})
			return
		}
	}
	writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_action_mismatch", Message: "action is not assigned to this source leader"})
}

func (s *Server) observerRebalancePlan(change rebalanceRequest) (cluster.RebalancePlan, int, *apiError) {
	if change.LeaveNodeID != s.nodeID {
		transition, status, apiErr := s.prepareRebalance(change, true)
		return transition.plan, status, apiErr
	}
	discovered := []cluster.Peer{}
	if s.peerProvider != nil {
		discovered = s.peerProvider.Peers()
	}
	if len(discovered) == 0 {
		return cluster.RebalancePlan{}, http.StatusServiceUnavailable, &apiError{Code: "membership_unavailable", Message: "leaving source cannot discover a surviving planning anchor"}
	}
	anchor := discovered[0]
	currentPeers := make([]cluster.Peer, 0, len(discovered))
	currentPeers = append(currentPeers, cluster.Peer{NodeID: s.nodeID, AdvertiseAddress: s.advertiseAddress, PlacementCapacity: s.currentPlacementCapacity(), Healthy: true})
	for _, peer := range discovered[1:] {
		currentPeers = append(currentPeers, peer)
	}
	current, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch(), anchor.NodeID, anchor.AdvertiseAddress, peerPlacementCapacity(anchor), currentPeers, s.engine.ListCollections(), s.replicationFactor)
	if err != nil {
		return cluster.RebalancePlan{}, http.StatusConflict, &apiError{Code: "rebalance_plan_invalid", Message: err.Error()}
	}
	targetPeers := make([]cluster.Peer, 0, len(currentPeers))
	for _, peer := range currentPeers {
		if peer.NodeID != change.LeaveNodeID {
			targetPeers = append(targetPeers, peer)
		}
	}
	target, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch()+1, anchor.NodeID, anchor.AdvertiseAddress, peerPlacementCapacity(anchor), targetPeers, s.engine.ListCollections(), s.replicationFactor)
	if err != nil {
		return cluster.RebalancePlan{}, http.StatusConflict, &apiError{Code: "rebalance_plan_invalid", Message: err.Error()}
	}
	plan, err := cluster.PlanRebalance(current, target)
	if err != nil {
		return cluster.RebalancePlan{}, http.StatusConflict, &apiError{Code: "rebalance_plan_invalid", Message: err.Error()}
	}
	return plan, http.StatusOK, nil
}

func peerPlacementCapacity(peer cluster.Peer) uint32 {
	if peer.PlacementCapacity == 0 {
		return 1
	}
	return peer.PlacementCapacity
}

func (s *Server) internalRebalanceRelease(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	if s.rebalanceBarriers == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "rebalance_unavailable", Message: "rebalance barriers are not configured"})
		return
	}
	if err := s.rebalanceBarriers.ReleaseCommitted(s.currentMetadataEpoch()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "barrier_release_failed", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "released", "metadata_epoch": s.currentMetadataEpoch()})
}

func (s *Server) internalRebalanceAbort(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	if s.rebalanceBarriers == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "rebalance_unavailable", Message: "rebalance barriers are not configured"})
		return
	}
	var request rebalanceAbortRequest
	if !decode(w, r, &request) {
		return
	}
	role, leaderID, term := cluster.RaftFollower, "", uint64(0)
	if provider, ok := s.raftProtocol.(interface {
		Status() (cluster.RaftRole, string, uint64)
	}); ok {
		role, leaderID, term = provider.Status()
	}
	coordinator := r.Header.Get("X-GideonDB-Coordinator-Node-ID")
	requestTerm, err := strconv.ParseUint(r.Header.Get("X-GideonDB-Leader-Term"), 10, 64)
	if err != nil || requestTerm != term || coordinator == "" || (role != cluster.RaftLeader && leaderID != coordinator) || (role == cluster.RaftLeader && coordinator != s.nodeID) {
		writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_coordinator_fence", Message: "abort is not from the current Raft leader and term"})
		return
	}
	if err := s.rebalanceBarriers.ReleasePlan(request.PlanDigest); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "rebalance_abort_failed", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "aborted", "plan_digest": request.PlanDigest})
}

func (s *Server) abortRebalanceBarriers(ctx context.Context, plan cluster.RebalancePlan, digest string, term uint64) []string {
	failures := []string{}
	sources := map[string]struct{}{}
	for _, movement := range plan.Movements {
		for _, action := range movement.Actions {
			if action.Type == "catch_up_wal" {
				sources[movement.SourceLeaderID] = struct{}{}
			}
		}
	}
	peers := map[string]cluster.Peer{}
	if s.peerProvider != nil {
		for _, peer := range s.peerProvider.Peers() {
			peers[peer.NodeID] = peer
		}
	}
	payload, err := json.Marshal(rebalanceAbortRequest{PlanDigest: digest})
	if err != nil {
		return []string{err.Error()}
	}
	for source := range sources {
		if source == s.nodeID {
			if err := s.rebalanceBarriers.ReleasePlan(digest); err != nil {
				failures = append(failures, source+": "+err.Error())
			}
			continue
		}
		peer, exists := peers[source]
		if !exists {
			failures = append(failures, source+": not discoverable")
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, peer.SeedURL+"/v1/internal/rebalance/abort", bytes.NewReader(payload))
		if err != nil {
			failures = append(failures, source+": "+err.Error())
			continue
		}
		s.setRebalanceHeaders(request, source, term)
		response, err := s.internalClient.Do(request)
		if err != nil {
			failures = append(failures, source+": "+err.Error())
			continue
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			failures = append(failures, fmt.Sprintf("%s: HTTP %d", source, response.StatusCode))
		}
	}
	return failures
}

func (s *Server) releaseRebalanceBarriers(ctx context.Context, plan cluster.RebalancePlan, epoch uint64) []string {
	failures := []string{}
	sources := map[string]struct{}{}
	for _, movement := range plan.Movements {
		for _, action := range movement.Actions {
			if action.Type == "catch_up_wal" {
				sources[movement.SourceLeaderID] = struct{}{}
			}
		}
	}
	peers := map[string]cluster.Peer{}
	if s.peerProvider != nil {
		for _, peer := range s.peerProvider.Peers() {
			peers[peer.NodeID] = peer
		}
	}
	term := uint64(0)
	if provider, ok := s.raftProtocol.(interface {
		Status() (cluster.RaftRole, string, uint64)
	}); ok {
		_, _, term = provider.Status()
	}
	for source := range sources {
		if source == s.nodeID {
			if s.rebalanceBarriers != nil {
				if err := s.rebalanceBarriers.ReleaseCommitted(epoch); err != nil {
					failures = append(failures, source+": "+err.Error())
				}
			}
			continue
		}
		peer, exists := peers[source]
		if !exists {
			failures = append(failures, source+": not discoverable")
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, peer.SeedURL+"/v1/internal/rebalance/release", bytes.NewReader([]byte("{}")))
		if err != nil {
			failures = append(failures, source+": "+err.Error())
			continue
		}
		s.setRebalanceHeaders(request, source, term)
		request.Header.Set("X-GideonDB-Metadata-Epoch", strconv.FormatUint(epoch, 10))
		response, err := s.internalClient.Do(request)
		if err != nil {
			failures = append(failures, source+": "+err.Error())
			continue
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			failures = append(failures, fmt.Sprintf("%s: HTTP %d", source, response.StatusCode))
		}
	}
	return failures
}

// localRebalanceDataMover streams a complete shard image from a local source
// leader. Re-sending the latest image is the WAL catch-up barrier for this
// transport slice and remains safe and idempotent at the target.
type localRebalanceDataMover struct {
	server     *Server
	change     rebalanceRequest
	peers      map[string]cluster.Peer
	term       uint64
	planDigest string
}

func (m *localRebalanceDataMover) CopySnapshot(ctx context.Context, movement cluster.ShardMovement, target string) error {
	return m.streamLatest(ctx, movement, target, false)
}

func (m *localRebalanceDataMover) CatchUpWAL(ctx context.Context, movement cluster.ShardMovement, target string) error {
	return m.streamLatest(ctx, movement, target, true)
}

func (m *localRebalanceDataMover) streamLatest(ctx context.Context, movement cluster.ShardMovement, target string, freeze bool) error {
	if movement.SourceLeaderID != m.server.nodeID {
		return fmt.Errorf("source leader %s must execute this movement", movement.SourceLeaderID)
	}
	peer, exists := m.peers[target]
	if !exists {
		return fmt.Errorf("target learner %s is not discoverable", target)
	}
	m.server.replicationMu.Lock()
	defer m.server.replicationMu.Unlock()
	if freeze {
		if m.server.rebalanceBarriers == nil {
			return fmt.Errorf("durable rebalance write barriers are not configured")
		}
		if err := m.server.rebalanceBarriers.Freeze(cluster.RebalanceWriteBarrier{PlanDigest: m.planDigest, Collection: movement.Collection, ShardID: movement.ShardID, TargetEpoch: m.change.ExpectedEpoch + 1}); err != nil {
			return err
		}
	}
	sequence, records, err := m.server.engine.ExportReplicaSnapshot(movement.Collection, movement.ShardID)
	if err != nil {
		return err
	}
	if sequence == 0 {
		return nil
	}
	return m.server.remoteRebalanceSnapshot(ctx, peer, movement, m.change, sequence, records, m.term)
}

func (s *Server) remoteRebalanceSnapshot(ctx context.Context, peer cluster.Peer, movement cluster.ShardMovement, change rebalanceRequest, sequence uint64, records []core.Record, term uint64) error {
	payload, err := json.Marshal(rebalanceSnapshotRequest{Change: change, SourceID: movement.SourceLeaderID, Sequence: sequence, Records: records})
	if err != nil {
		return err
	}
	target := peer.SeedURL + "/v1/internal/rebalance/" + url.PathEscape(movement.Collection) + "/" + strconv.FormatUint(uint64(movement.ShardID), 10) + "/snapshot"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GideonDB-Cluster-ID", s.clusterID)
	request.Header.Set("X-GideonDB-Target-Node-ID", peer.NodeID)
	request.Header.Set("X-GideonDB-Metadata-Epoch", strconv.FormatUint(s.currentMetadataEpoch(), 10))
	request.Header.Set("X-GideonDB-Leader-Node-ID", movement.SourceLeaderID)
	request.Header.Set("X-GideonDB-Leader-Term", strconv.FormatUint(term, 10))
	if s.peerAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+s.peerAPIKey)
	}
	response, err := s.internalClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("rebalance target returned HTTP %d: %s", response.StatusCode, body)
	}
	return nil
}

func (s *Server) internalRebalanceSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	shardValue, err := strconv.ParseUint(r.PathValue("shard"), 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_shard", Message: "shard must be an unsigned 32-bit integer"})
		return
	}
	var request rebalanceSnapshotRequest
	if !decode(w, r, &request) {
		return
	}
	if request.Change.ExpectedEpoch != s.currentMetadataEpoch() || request.SourceID != r.Header.Get("X-GideonDB-Leader-Node-ID") {
		writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_fence_mismatch", Message: "rebalance epoch or source identity differs"})
		return
	}
	leaderTerm, termErr := strconv.ParseUint(r.Header.Get("X-GideonDB-Leader-Term"), 10, 64)
	statusProvider, statusAvailable := s.raftProtocol.(interface {
		Status() (cluster.RaftRole, string, uint64)
	})
	if termErr != nil || leaderTerm == 0 || !statusAvailable {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_replication_leader", Message: "valid control-plane leader term is required"})
		return
	}
	_, _, currentTerm := statusProvider.Status()
	if leaderTerm != currentTerm {
		writeJSON(w, http.StatusConflict, apiError{Code: "replication_leader_fence", Message: "source term does not match the control-plane term"})
		return
	}
	var plan cluster.RebalancePlan
	if request.Change.LeaveNodeID != "" || request.Change.CapacityNodeID != "" {
		transition, status, apiErr := s.prepareRebalance(request.Change, false)
		if apiErr != nil {
			writeJSON(w, status, *apiErr)
			return
		}
		plan = transition.plan
	} else {
		if request.Change.JoinNodeID != s.nodeID || request.Change.JoinAddress != s.advertiseAddress {
			writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_target_mismatch", Message: "join staging target identity differs"})
			return
		}
		discovered := []cluster.Peer{}
		if s.peerProvider != nil {
			discovered = s.peerProvider.Peers()
		}
		var source cluster.Peer
		currentPeers := make([]cluster.Peer, 0, len(discovered))
		for _, peer := range discovered {
			if peer.NodeID == request.SourceID {
				source = peer
				continue
			}
			if peer.NodeID != s.nodeID {
				currentPeers = append(currentPeers, peer)
			}
		}
		if source.NodeID == "" {
			writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_source_unknown", Message: "source leader is not discoverable"})
			return
		}
		current, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch(), source.NodeID, source.AdvertiseAddress, peerPlacementCapacity(source), currentPeers, s.engine.ListCollections(), s.replicationFactor)
		if err != nil {
			writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_plan_invalid", Message: err.Error()})
			return
		}
		targetPeers := append(currentPeers, cluster.Peer{NodeID: s.nodeID, AdvertiseAddress: s.advertiseAddress, PlacementCapacity: s.currentPlacementCapacity(), Healthy: true})
		target, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch()+1, source.NodeID, source.AdvertiseAddress, peerPlacementCapacity(source), targetPeers, s.engine.ListCollections(), s.replicationFactor)
		if err != nil {
			writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_plan_invalid", Message: err.Error()})
			return
		}
		plan, err = cluster.PlanRebalance(current, target)
		if err != nil {
			writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_plan_invalid", Message: err.Error()})
			return
		}
	}
	collection, shardID := r.PathValue("collection"), uint32(shardValue)
	authorized := false
	for _, movement := range plan.Movements {
		if movement.Collection != collection || movement.ShardID != shardID || movement.SourceLeaderID != request.SourceID {
			continue
		}
		for _, action := range movement.Actions {
			if action.Type == "copy_snapshot" && action.TargetID == s.nodeID {
				authorized = true
			}
		}
	}
	if !authorized {
		writeJSON(w, http.StatusConflict, apiError{Code: "rebalance_target_mismatch", Message: "local node is not the planned new replica"})
		return
	}
	if err := s.engine.InstallReplicaSnapshot(collection, shardID, request.Sequence, request.Records); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "staged", "sequence": request.Sequence, "shard_id": shardID, "metadata_epoch": s.currentMetadataEpoch()})
}
