package rest

import (
	"net/http"
	"strconv"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
)

type replicaAppendRequest struct {
	Sequence          uint64        `json:"sequence"`
	ReplicationFactor int           `json:"replication_factor"`
	Records           []core.Record `json:"records"`
	Operation         string        `json:"operation,omitempty"`
	Namespace         string        `json:"namespace,omitempty"`
	ID                string        `json:"id,omitempty"`
}

type replicaSnapshotRequest struct {
	Sequence          uint64        `json:"sequence"`
	ReplicationFactor int           `json:"replication_factor"`
	Records           []core.Record `json:"records"`
}

func (s *Server) internalReplicaAppend(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	if !s.requireStaticPlacement(w) {
		return
	}
	shardValue, err := strconv.ParseUint(r.PathValue("shard"), 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_shard", Message: "shard must be an unsigned 32-bit integer"})
		return
	}
	leaderID := r.Header.Get("X-GideonDB-Leader-Node-ID")
	leaderTerm, err := strconv.ParseUint(r.Header.Get("X-GideonDB-Leader-Term"), 10, 64)
	if !cluster.ValidNodeID(leaderID) || err != nil || leaderTerm == 0 {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_replication_leader", Message: "valid leader node ID and term are required"})
		return
	}
	status, ok := s.raftProtocol.(interface {
		Status() (cluster.RaftRole, string, uint64)
	})
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "raft_unavailable", Message: "metadata Raft status is required for replica fencing"})
		return
	}
	_, _, currentTerm := status.Status()
	if leaderTerm != currentTerm {
		writeJSON(w, http.StatusConflict, apiError{Code: "replication_leader_fence", Message: "replication leader term does not match the control-plane term"})
		return
	}
	var request replicaAppendRequest
	if !decode(w, r, &request) {
		return
	}
	if request.ReplicationFactor != s.replicationFactor {
		writeJSON(w, http.StatusConflict, apiError{Code: "replication_factor_mismatch", Message: "request replication factor does not match local configuration"})
		return
	}
	config, _, err := s.engine.DescribeCollection(r.PathValue("collection"))
	if err != nil {
		writeError(w, err)
		return
	}
	peers, peerErr := s.membershipPeers()
	if peerErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: peerErr.Error()})
		return
	}
	table, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, []core.CollectionConfig{config}, s.replicationFactor)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "replica_placement_unavailable", Message: err.Error()})
		return
	}
	shardID := uint32(shardValue)
	assigned := false
	for _, placement := range table.Shards {
		if placement.ShardID != shardID {
			continue
		}
		if placement.LeaderID != leaderID {
			writeJSON(w, http.StatusConflict, apiError{Code: "wrong_replication_leader", Message: "sender is not the planned shard leader"})
			return
		}
		for _, replica := range placement.Replicas {
			assigned = assigned || replica == s.nodeID
		}
		break
	}
	if !assigned {
		writeJSON(w, http.StatusConflict, apiError{Code: "not_a_replica", Message: "local node is not assigned to this replica set"})
		return
	}
	var applyErr error
	switch request.Operation {
	case "", "upsert":
		applyErr = s.engine.ApplyReplicaBatch(config.Name, shardID, request.Sequence, request.Records)
	case "delete":
		if len(request.Records) != 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_replica_delete", Message: "delete append must not contain records"})
			return
		}
		applyErr = s.engine.ApplyReplicaDelete(config.Name, shardID, request.Sequence, request.Namespace, request.ID)
	default:
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_replica_operation", Message: "replica operation must be upsert or delete"})
		return
	}
	if applyErr != nil {
		writeError(w, applyErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sequence": request.Sequence, "shard_id": shardID, "metadata_epoch": s.currentMetadataEpoch(), "status": "appended"})
}

func (s *Server) internalReplicaSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) || !s.requireStaticPlacement(w) {
		return
	}
	shardValue, err := strconv.ParseUint(r.PathValue("shard"), 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_shard", Message: "shard must be an unsigned 32-bit integer"})
		return
	}
	leaderID := r.Header.Get("X-GideonDB-Leader-Node-ID")
	leaderTerm, err := strconv.ParseUint(r.Header.Get("X-GideonDB-Leader-Term"), 10, 64)
	if !cluster.ValidNodeID(leaderID) || err != nil || leaderTerm == 0 {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_replication_leader", Message: "valid leader node ID and term are required"})
		return
	}
	status, ok := s.raftProtocol.(interface {
		Status() (cluster.RaftRole, string, uint64)
	})
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "raft_unavailable", Message: "metadata Raft status is required for replica fencing"})
		return
	}
	_, _, currentTerm := status.Status()
	if leaderTerm != currentTerm {
		writeJSON(w, http.StatusConflict, apiError{Code: "replication_leader_fence", Message: "replication leader term does not match the control-plane term"})
		return
	}
	var request replicaSnapshotRequest
	if !decode(w, r, &request) {
		return
	}
	if request.ReplicationFactor != s.replicationFactor {
		writeJSON(w, http.StatusConflict, apiError{Code: "replication_factor_mismatch", Message: "request replication factor does not match local configuration"})
		return
	}
	config, _, err := s.engine.DescribeCollection(r.PathValue("collection"))
	if err != nil {
		writeError(w, err)
		return
	}
	peers, peerErr := s.membershipPeers()
	if peerErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: peerErr.Error()})
		return
	}
	table, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, []core.CollectionConfig{config}, s.replicationFactor)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "replica_placement_unavailable", Message: err.Error()})
		return
	}
	shardID := uint32(shardValue)
	assigned := false
	for _, placement := range table.Shards {
		if placement.ShardID != shardID {
			continue
		}
		if placement.LeaderID != leaderID {
			writeJSON(w, http.StatusConflict, apiError{Code: "wrong_replication_leader", Message: "sender is not the planned shard leader"})
			return
		}
		for _, replica := range placement.Replicas {
			assigned = assigned || replica == s.nodeID
		}
		break
	}
	if !assigned {
		writeJSON(w, http.StatusConflict, apiError{Code: "not_a_replica", Message: "local node is not assigned to this replica set"})
		return
	}
	if err := s.engine.InstallReplicaSnapshot(config.Name, shardID, request.Sequence, request.Records); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sequence": request.Sequence, "shard_id": shardID, "metadata_epoch": s.currentMetadataEpoch(), "status": "installed"})
}
