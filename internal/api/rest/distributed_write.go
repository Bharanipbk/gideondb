package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/vectordb/vectordb/internal/cluster"
	"github.com/vectordb/vectordb/internal/core"
)

type distributedBatchRequest struct {
	Records         []core.Record `json:"records"`
	Acknowledgement string        `json:"acknowledgement,omitempty"`
}

type internalShardBatchRequest struct {
	Records         []core.Record `json:"records"`
	Acknowledgement string        `json:"acknowledgement"`
}

type shardWriteOutcome struct {
	ShardID              uint32        `json:"shard_id"`
	NodeID               string        `json:"node_id"`
	Status               string        `json:"status"`
	Records              []core.Record `json:"records,omitempty"`
	ReplicasAcknowledged int           `json:"replicas_acknowledged,omitempty"`
	ReplicationFactor    int           `json:"replication_factor,omitempty"`
	Acknowledgement      string        `json:"acknowledgement,omitempty"`
	Error                string        `json:"error,omitempty"`
}

func (s *Server) internalShardBatchUpsert(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	shardID, err := strconv.ParseUint(r.PathValue("shard"), 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_shard", Message: "shard must be an unsigned 32-bit integer"})
		return
	}
	if !s.validateShardOwner(w, r.PathValue("collection"), uint32(shardID)) {
		return
	}
	var request internalShardBatchRequest
	if !decode(w, r, &request) {
		return
	}
	acknowledgement, err := parseAcknowledgement(request.Acknowledgement)
	if err != nil {
		writeError(w, err)
		return
	}
	records, acknowledged, ambiguous, err := s.commitLeaderShardBatch(r.Context(), r.PathValue("collection"), uint32(shardID), request.Records, acknowledgement, r.Header.Get("traceparent"))
	if err != nil {
		if ambiguous {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "replication_acknowledgement_unavailable", Message: err.Error()})
			return
		}
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records, "shard_id": shardID, "metadata_epoch": s.currentMetadataEpoch(), "replicas_acknowledged": acknowledged, "replication_factor": s.replicationFactor, "acknowledgement": acknowledgement})
}

func (s *Server) distributedBatchUpsert(w http.ResponseWriter, r *http.Request) {
	if !s.requireStaticPlacement(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var request distributedBatchRequest
	if !decode(w, r, &request) {
		return
	}
	if len(request.Records) == 0 || len(request.Records) > 10_000 {
		writeError(w, fmt.Errorf("%w: batch size must be between 1 and 10000", core.ErrInvalidArgument))
		return
	}
	acknowledgement, err := parseAcknowledgement(request.Acknowledgement)
	if err != nil {
		writeError(w, err)
		return
	}
	config, _, err := s.engine.DescribeCollection(r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	groups := make(map[uint32][]core.Record)
	for position, record := range request.Records {
		if record.ID == "" {
			writeError(w, fmt.Errorf("%w: record %d id is required", core.ErrInvalidArgument, position))
			return
		}
		if err := core.ValidateVector(record.Vector, config.Dimension); err != nil {
			writeError(w, fmt.Errorf("record %d: %w", position, err))
			return
		}
		shardID, err := s.engine.RouteShard(config.Name, record.Namespace, record.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		groups[shardID] = append(groups[shardID], record)
	}
	peers, peerErr := s.membershipPeers()
	if peerErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: peerErr.Error()})
		return
	}
	epoch := s.currentMetadataEpoch()
	table, err := cluster.PlanPlacementWithCapacity(epoch, s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, []core.CollectionConfig{config})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "placement_unavailable", Message: err.Error()})
		return
	}
	ownerByShard := make(map[uint32]string, len(table.Shards))
	for _, assignment := range table.Shards {
		ownerByShard[assignment.ShardID] = assignment.NodeID
	}
	peerByID := make(map[string]cluster.Peer, len(peers))
	for _, peer := range peers {
		if peer.NodeID != "" {
			peerByID[peer.NodeID] = peer
		}
	}
	shardIDs := make([]int, 0, len(groups))
	for shardID := range groups {
		shardIDs = append(shardIDs, int(shardID))
	}
	sort.Ints(shardIDs)
	outcomes := make([]shardWriteOutcome, len(shardIDs))
	workers := min(len(outcomes), min(32, max(1, runtime.GOMAXPROCS(0)*2)))
	jobs := make(chan int)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for position := range jobs {
				shardID := uint32(shardIDs[position])
				owner := ownerByShard[shardID]
				outcome := shardWriteOutcome{ShardID: shardID, NodeID: owner}
				var records []core.Record
				var acknowledged int
				var writeErr error
				ambiguous := false
				if owner == s.nodeID {
					records, acknowledged, ambiguous, writeErr = s.commitLeaderShardBatch(ctx, config.Name, shardID, groups[shardID], acknowledgement, r.Header.Get("traceparent"))
				} else if peer, exists := peerByID[owner]; !exists {
					writeErr = fmt.Errorf("placement node is not discoverable")
				} else {
					records, acknowledged, ambiguous, writeErr = s.remoteShardBatchUpsert(ctx, peer, config.Name, shardID, groups[shardID], acknowledgement, r.Header.Get("traceparent"))
				}
				if writeErr == nil {
					outcome.Status, outcome.Records, outcome.ReplicasAcknowledged, outcome.ReplicationFactor, outcome.Acknowledgement = "committed", records, acknowledged, s.replicationFactor, acknowledgement
				} else if ambiguous {
					outcome.Status, outcome.Error = "unknown", writeErr.Error()
				} else {
					outcome.Status, outcome.Error = "failed", writeErr.Error()
				}
				outcomes[position] = outcome
			}
		}()
	}
	for position := range outcomes {
		jobs <- position
	}
	close(jobs)
	wait.Wait()
	partial := false
	for _, outcome := range outcomes {
		if outcome.Status != "committed" {
			partial = true
			break
		}
	}
	status := http.StatusOK
	if partial {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, map[string]any{"outcomes": outcomes, "partial": partial, "metadata_epoch": epoch, "authoritative_placement": s.staticPlacementReady()})
}

func (s *Server) remoteShardBatchUpsert(ctx context.Context, peer cluster.Peer, collection string, shardID uint32, records []core.Record, acknowledgement string, traceparent string) ([]core.Record, int, bool, error) {
	payload, err := json.Marshal(internalShardBatchRequest{Records: records, Acknowledgement: acknowledgement})
	if err != nil {
		return nil, 0, false, err
	}
	target := peer.SeedURL + "/v1/internal/shards/" + url.PathEscape(collection) + "/" + strconv.FormatUint(uint64(shardID), 10) + "/vectors/batch"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, false, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-VectorDB-Cluster-ID", s.clusterID)
	request.Header.Set("X-VectorDB-Target-Node-ID", peer.NodeID)
	request.Header.Set("X-VectorDB-Metadata-Epoch", strconv.FormatUint(s.currentMetadataEpoch(), 10))
	if s.peerAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+s.peerAPIKey)
	}
	if traceparent != "" {
		request.Header.Set("traceparent", traceparent)
	}
	response, err := s.internalClient.Do(request)
	if err != nil {
		return nil, 0, true, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, 0, response.StatusCode >= 500, fmt.Errorf("peer returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	var body struct {
		Records              []core.Record `json:"records"`
		ShardID              uint32        `json:"shard_id"`
		MetadataEpoch        uint64        `json:"metadata_epoch"`
		ReplicasAcknowledged int           `json:"replicas_acknowledged"`
		ReplicationFactor    int           `json:"replication_factor"`
		Acknowledgement      string        `json:"acknowledgement"`
	}
	if err := decoder.Decode(&body); err != nil {
		return nil, 0, true, fmt.Errorf("decode peer response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, 0, true, fmt.Errorf("peer response must contain one JSON value")
	}
	if body.ShardID != shardID || body.MetadataEpoch != s.currentMetadataEpoch() {
		return nil, 0, true, fmt.Errorf("peer response fence mismatch")
	}
	required := requiredAcknowledgements(acknowledgement, s.replicationFactor)
	if body.ReplicationFactor != s.replicationFactor || body.Acknowledgement != acknowledgement || body.ReplicasAcknowledged < required {
		return nil, body.ReplicasAcknowledged, true, fmt.Errorf("peer response lacks replication quorum")
	}
	return body.Records, body.ReplicasAcknowledged, false, nil
}
