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
	"time"

	"github.com/vectordb/vectordb/internal/cluster"
	"github.com/vectordb/vectordb/internal/core"
)

// commitLeaderShardBatch serializes leader commits through follower fanout so
// concurrent requests cannot deliver later WAL sequences first.
func (s *Server) commitLeaderShardBatch(ctx context.Context, collection string, shardID uint32, records []core.Record, acknowledgement string, traceparent string) ([]core.Record, int, bool, error) {
	s.replicationMu.Lock()
	unlockHere := true
	defer func() {
		if unlockHere {
			s.replicationMu.Unlock()
		}
	}()
	if s.rebalanceBarriers != nil {
		if err := s.rebalanceBarriers.ReleaseCommitted(s.currentMetadataEpoch()); err != nil {
			return nil, 0, false, fmt.Errorf("release committed rebalance barriers: %w", err)
		}
		if barrier, frozen := s.rebalanceBarriers.IsFrozen(collection, shardID); frozen {
			return nil, 0, false, fmt.Errorf("shard writes frozen for rebalance target epoch %d", barrier.TargetEpoch)
		}
	}

	var term uint64
	if s.replicationFactor > 1 {
		status, ok := s.raftProtocol.(interface {
			Status() (cluster.RaftRole, string, uint64)
		})
		if !ok {
			return nil, 0, false, fmt.Errorf("metadata Raft status is required for replicated writes")
		}
		_, _, term = status.Status()
		if term == 0 {
			return nil, 0, false, fmt.Errorf("metadata Raft term is not established")
		}
	}

	config, _, err := s.engine.DescribeCollection(collection)
	if err != nil {
		return nil, 0, false, err
	}
	peers, err := s.membershipPeers()
	if err != nil {
		return nil, 0, false, err
	}
	table, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, []core.CollectionConfig{config}, s.replicationFactor)
	if err != nil {
		return nil, 0, false, err
	}
	var placement *cluster.ShardReplicaPlacement
	for index := range table.Shards {
		if table.Shards[index].ShardID == shardID {
			placement = &table.Shards[index]
			break
		}
	}
	if placement == nil || placement.LeaderID != s.nodeID {
		return nil, 0, false, fmt.Errorf("local node is not the planned shard leader")
	}

	prepared, sequence, err := s.engine.BatchUpsertShardWithSequence(collection, shardID, records)
	if err != nil {
		return nil, 0, false, err
	}
	if s.replicationFactor == 1 {
		return prepared, 1, false, nil
	}

	peerByID := make(map[string]cluster.Peer, len(peers))
	for _, peer := range peers {
		peerByID[peer.NodeID] = peer
	}
	type replicaResult struct {
		nodeID string
		err    error
	}
	results := make(chan replicaResult, len(placement.Replicas)-1)
	fanoutCtx, cancelFanout := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	for _, replicaID := range placement.Replicas[1:] {
		s.metrics.setReplicaLeaderSequence(collection, shardID, replicaID, sequence)
		peer, exists := peerByID[replicaID]
		if !exists {
			s.metrics.observeReplication("append", "failure", collection, shardID, replicaID, sequence)
			results <- replicaResult{nodeID: replicaID, err: fmt.Errorf("not discoverable")}
			continue
		}
		go func() {
			results <- replicaResult{nodeID: replicaID, err: s.remoteReplicaAppend(fanoutCtx, peer, collection, shardID, sequence, prepared, term, traceparent)}
		}()
	}
	required := requiredAcknowledgements(acknowledgement, s.replicationFactor)
	acknowledged := 1
	remaining := len(placement.Replicas) - 1
	var failures []string
	if acknowledged >= required {
		unlockHere = false
		go func() {
			defer cancelFanout()
			for range remaining {
				<-results
			}
			s.replicationMu.Unlock()
		}()
		return prepared, acknowledged, false, nil
	}
	for remaining > 0 {
		result := <-results
		remaining--
		if result.err == nil {
			acknowledged++
		} else {
			failures = append(failures, result.nodeID+": "+result.err.Error())
		}
		if acknowledged >= required {
			if remaining > 0 {
				unlockHere = false
				go func(pending int) {
					defer cancelFanout()
					for range pending {
						<-results
					}
					s.replicationMu.Unlock()
				}(remaining)
			} else {
				cancelFanout()
			}
			return prepared, acknowledged, false, nil
		}
	}
	cancelFanout()
	if acknowledged < required {
		return prepared, acknowledged, true, fmt.Errorf("replication acknowledgement level %s unavailable: acknowledged %d of %d replicas, require %d (%v)", acknowledgement, acknowledged, s.replicationFactor, required, failures)
	}
	return prepared, acknowledged, false, nil
}

func parseAcknowledgement(value string) (string, error) {
	if value == "" {
		return "quorum", nil
	}
	if value != "leader" && value != "quorum" && value != "all" {
		return "", fmt.Errorf("%w: acknowledgement must be leader, quorum, or all", core.ErrInvalidArgument)
	}
	return value, nil
}

func requiredAcknowledgements(level string, replicationFactor int) int {
	switch level {
	case "leader":
		return 1
	case "all":
		return replicationFactor
	default:
		return replicationFactor/2 + 1
	}
}

func (s *Server) remoteReplicaAppend(ctx context.Context, peer cluster.Peer, collection string, shardID uint32, sequence uint64, records []core.Record, term uint64, traceparent string) error {
	code, err := s.remoteReplicaAppendOnce(ctx, peer, collection, shardID, sequence, records, term, traceparent)
	if err == nil {
		s.metrics.observeReplication("append", "success", collection, shardID, peer.NodeID, sequence)
		return nil
	}
	s.metrics.observeReplication("append", "failure", collection, shardID, peer.NodeID, sequence)
	if code != "replication_gap" && code != "replication_sequence_compacted" {
		return err
	}
	snapshotSequence, snapshotRecords, snapshotErr := s.engine.ExportReplicaSnapshot(collection, shardID)
	if snapshotErr != nil {
		return fmt.Errorf("export recovery snapshot: %w", snapshotErr)
	}
	if snapshotSequence < sequence {
		return fmt.Errorf("recovery snapshot sequence %d is behind append %d", snapshotSequence, sequence)
	}
	if snapshotErr := s.remoteReplicaSnapshot(ctx, peer, collection, shardID, snapshotSequence, snapshotRecords, term, traceparent); snapshotErr != nil {
		s.metrics.observeReplication("snapshot", "failure", collection, shardID, peer.NodeID, snapshotSequence)
		return fmt.Errorf("install recovery snapshot after %s: %w", code, snapshotErr)
	}
	s.metrics.observeReplication("snapshot", "success", collection, shardID, peer.NodeID, snapshotSequence)
	return nil
}

func (s *Server) remoteReplicaAppendOnce(ctx context.Context, peer cluster.Peer, collection string, shardID uint32, sequence uint64, records []core.Record, term uint64, traceparent string) (string, error) {
	payload, err := json.Marshal(replicaAppendRequest{Sequence: sequence, ReplicationFactor: s.replicationFactor, Records: records})
	if err != nil {
		return "", err
	}
	target := peer.SeedURL + "/v1/internal/replicas/" + url.PathEscape(collection) + "/" + strconv.FormatUint(uint64(shardID), 10) + "/append"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-VectorDB-Cluster-ID", s.clusterID)
	request.Header.Set("X-VectorDB-Target-Node-ID", peer.NodeID)
	request.Header.Set("X-VectorDB-Metadata-Epoch", strconv.FormatUint(s.currentMetadataEpoch(), 10))
	request.Header.Set("X-VectorDB-Leader-Node-ID", s.nodeID)
	request.Header.Set("X-VectorDB-Leader-Term", strconv.FormatUint(term, 10))
	if s.peerAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+s.peerAPIKey)
	}
	if traceparent != "" {
		request.Header.Set("traceparent", traceparent)
	}
	response, err := s.internalClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var body apiError
		if decodeErr := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes)).Decode(&body); decodeErr != nil {
			return "", fmt.Errorf("peer returned HTTP %d", response.StatusCode)
		}
		return body.Code, fmt.Errorf("peer returned HTTP %d: %s", response.StatusCode, body.Code)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	var body struct {
		Sequence      uint64 `json:"sequence"`
		ShardID       uint32 `json:"shard_id"`
		MetadataEpoch uint64 `json:"metadata_epoch"`
		Status        string `json:"status"`
	}
	if err := decoder.Decode(&body); err != nil {
		return "", fmt.Errorf("decode replica response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", fmt.Errorf("replica response must contain one JSON value")
	}
	if body.Sequence != sequence || body.ShardID != shardID || body.MetadataEpoch != s.currentMetadataEpoch() || body.Status != "appended" {
		return "", fmt.Errorf("replica response fence mismatch")
	}
	return "", nil
}

func (s *Server) remoteReplicaSnapshot(ctx context.Context, peer cluster.Peer, collection string, shardID uint32, sequence uint64, records []core.Record, term uint64, traceparent string) error {
	payload, err := json.Marshal(replicaSnapshotRequest{Sequence: sequence, ReplicationFactor: s.replicationFactor, Records: records})
	if err != nil {
		return err
	}
	target := peer.SeedURL + "/v1/internal/replicas/" + url.PathEscape(collection) + "/" + strconv.FormatUint(uint64(shardID), 10) + "/snapshot"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-VectorDB-Cluster-ID", s.clusterID)
	request.Header.Set("X-VectorDB-Target-Node-ID", peer.NodeID)
	request.Header.Set("X-VectorDB-Metadata-Epoch", strconv.FormatUint(s.currentMetadataEpoch(), 10))
	request.Header.Set("X-VectorDB-Leader-Node-ID", s.nodeID)
	request.Header.Set("X-VectorDB-Leader-Term", strconv.FormatUint(term, 10))
	if s.peerAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+s.peerAPIKey)
	}
	if traceparent != "" {
		request.Header.Set("traceparent", traceparent)
	}
	response, err := s.internalClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("peer returned HTTP %d", response.StatusCode)
	}
	var body struct {
		Sequence      uint64 `json:"sequence"`
		ShardID       uint32 `json:"shard_id"`
		MetadataEpoch uint64 `json:"metadata_epoch"`
		Status        string `json:"status"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return fmt.Errorf("decode snapshot response: %w", err)
	}
	if body.Sequence != sequence || body.ShardID != shardID || body.MetadataEpoch != s.currentMetadataEpoch() || body.Status != "installed" {
		return fmt.Errorf("snapshot response fence mismatch")
	}
	return nil
}
