package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
)

type replicaRepairRetry struct {
	Failures    uint32
	NextAttempt time.Time
}

func (s *Server) internalReplicaStatus(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	shardValue, err := strconv.ParseUint(r.PathValue("shard"), 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_shard", Message: "shard must be an unsigned 32-bit integer"})
		return
	}
	config, _, err := s.engine.DescribeCollection(r.PathValue("collection"))
	if err != nil {
		writeError(w, err)
		return
	}
	peers, err := s.membershipPeers()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: err.Error()})
		return
	}
	table, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, []core.CollectionConfig{config}, s.replicationFactor)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "placement_unavailable", Message: err.Error()})
		return
	}
	shardID := uint32(shardValue)
	assigned := false
	for _, placement := range table.Shards {
		if placement.ShardID == shardID {
			for _, replica := range placement.Replicas {
				assigned = assigned || replica == s.nodeID
			}
		}
	}
	if !assigned {
		writeJSON(w, http.StatusConflict, apiError{Code: "not_a_replica", Message: "local node is not assigned to this replica set"})
		return
	}
	sequence, err := s.engine.ReplicaSequence(config.Name, shardID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sequence": sequence, "shard_id": shardID, "metadata_epoch": s.currentMetadataEpoch()})
}

func (s *Server) RunReplicaRepair(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.RepairReplicasOnce(ctx); err != nil {
				s.logger.Warn("replica repair pass failed", "error", err)
			}
		}
	}
}

func (s *Server) RepairReplicasOnce(ctx context.Context) error {
	if s.replicationFactor <= 1 || !s.staticPlacementReady() {
		return nil
	}
	status, ok := s.raftProtocol.(interface {
		Status() (cluster.RaftRole, string, uint64)
	})
	if !ok {
		return fmt.Errorf("metadata Raft status is required for replica repair")
	}
	_, _, term := status.Status()
	if term == 0 {
		return fmt.Errorf("metadata Raft term is not established")
	}
	peers, err := s.membershipPeers()
	if err != nil {
		return err
	}
	peerByID := map[string]cluster.Peer{}
	for _, peer := range peers {
		peerByID[peer.NodeID] = peer
	}
	var failures []string
	for _, config := range s.engine.ListCollections() {
		table, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, []core.CollectionConfig{config}, s.replicationFactor)
		if err != nil {
			return err
		}
		for _, placement := range table.Shards {
			if placement.LeaderID != s.nodeID {
				continue
			}
			leaderSequence, err := s.engine.ReplicaSequence(config.Name, placement.ShardID)
			if err != nil {
				failures = append(failures, err.Error())
				continue
			}
			for _, followerID := range placement.Replicas[1:] {
				retryKey := replicaMetricKey(config.Name, placement.ShardID, followerID)
				if !s.repairAttemptAllowed(retryKey, time.Now()) {
					continue
				}
				peer, exists := peerByID[followerID]
				if !exists {
					failures = append(failures, followerID+": not discoverable")
					s.recordRepairFailure(retryKey, time.Now())
					continue
				}
				followerSequence, err := s.remoteReplicaSequence(ctx, peer, config.Name, placement.ShardID)
				s.metrics.setReplicaLeaderSequence(config.Name, placement.ShardID, followerID, leaderSequence)
				if err != nil {
					failures = append(failures, followerID+": "+err.Error())
					s.recordRepairFailure(retryKey, time.Now())
					continue
				}
				s.metrics.setReplicaFollowerSequence(config.Name, placement.ShardID, followerID, followerSequence)
				if followerSequence >= leaderSequence || leaderSequence == 0 {
					s.clearRepairFailure(retryKey)
					continue
				}
				snapshotSequence, records, err := s.engine.ExportReplicaSnapshot(config.Name, placement.ShardID)
				if err != nil {
					failures = append(failures, followerID+": "+err.Error())
					s.recordRepairFailure(retryKey, time.Now())
					continue
				}
				s.metrics.setReplicaLeaderSequence(config.Name, placement.ShardID, followerID, snapshotSequence)
				if err := s.remoteReplicaSnapshot(ctx, peer, config.Name, placement.ShardID, snapshotSequence, records, term, ""); err != nil {
					s.metrics.observeReplication("repair", "failure", config.Name, placement.ShardID, followerID, snapshotSequence)
					failures = append(failures, followerID+": "+err.Error())
					s.recordRepairFailure(retryKey, time.Now())
					continue
				}
				s.metrics.observeReplication("repair", "success", config.Name, placement.ShardID, followerID, snapshotSequence)
				s.metrics.setReplicaFollowerSequence(config.Name, placement.ShardID, followerID, snapshotSequence)
				s.clearRepairFailure(retryKey)
			}
		}
	}
	if len(failures) != 0 {
		return fmt.Errorf("replica repair failures: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (s *Server) repairAttemptAllowed(key string, now time.Time) bool {
	s.repairMu.Lock()
	defer s.repairMu.Unlock()
	retry, exists := s.repairRetries[key]
	return !exists || !now.Before(retry.NextAttempt)
}

func (s *Server) recordRepairFailure(key string, now time.Time) {
	s.repairMu.Lock()
	retry := s.repairRetries[key]
	if retry.Failures < 31 {
		retry.Failures++
	}
	delay := time.Second
	for step := uint32(1); step < retry.Failures && delay < 5*time.Minute; step++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	retry.NextAttempt = now.Add(delay)
	s.repairRetries[key] = retry
	s.repairMu.Unlock()
}

func (s *Server) clearRepairFailure(key string) {
	s.repairMu.Lock()
	delete(s.repairRetries, key)
	s.repairMu.Unlock()
}

func (s *Server) remoteReplicaSequence(ctx context.Context, peer cluster.Peer, collection string, shardID uint32) (uint64, error) {
	target := peer.SeedURL + "/v1/internal/replicas/" + url.PathEscape(collection) + "/" + strconv.FormatUint(uint64(shardID), 10) + "/status"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("X-GideonDB-Cluster-ID", s.clusterID)
	request.Header.Set("X-GideonDB-Target-Node-ID", peer.NodeID)
	request.Header.Set("X-GideonDB-Metadata-Epoch", strconv.FormatUint(s.currentMetadataEpoch(), 10))
	if s.peerAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+s.peerAPIKey)
	}
	response, err := s.internalClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("peer returned HTTP %d", response.StatusCode)
	}
	var body struct {
		Sequence uint64 `json:"sequence"`
		ShardID  uint32 `json:"shard_id"`
		Epoch    uint64 `json:"metadata_epoch"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return 0, err
	}
	if body.ShardID != shardID || body.Epoch != s.currentMetadataEpoch() {
		return 0, fmt.Errorf("replica status fence mismatch")
	}
	return body.Sequence, nil
}
