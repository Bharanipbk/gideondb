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

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
)

type internalShardDeleteRequest struct {
	Namespace       string `json:"namespace,omitempty"`
	Acknowledgement string `json:"acknowledgement"`
}

// DistributedDeleteHandler exposes placement-aware deletion to other
// in-process transports without reapplying public HTTP middleware.
func (s *Server) DistributedDeleteHandler() http.Handler {
	return http.HandlerFunc(s.distributedDelete)
}

func (s *Server) distributedDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuthoritativePlacement(w) {
		return
	}
	name, id := r.PathValue("name"), r.PathValue("id")
	namespace := r.URL.Query().Get("namespace")
	shardID, err := s.engine.RouteShard(name, namespace, id)
	if err != nil {
		writeError(w, err)
		return
	}
	owner, peer, local, err := s.authoritativeShardOwner(name, shardID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "placement_unavailable", Message: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	acknowledged := 0
	if local {
		acknowledged, _, err = s.commitLeaderShardDelete(ctx, name, shardID, namespace, id, "quorum", r.Header.Get("traceparent"))
	} else {
		acknowledged, err = s.remoteShardDelete(ctx, peer, name, shardID, namespace, id, r.Header.Get("traceparent"))
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "committed", "shard_id": shardID, "node_id": owner, "metadata_epoch": s.currentMetadataEpoch(), "replicas_acknowledged": acknowledged, "replication_factor": s.replicationFactor})
}

func (s *Server) internalShardDelete(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	value, err := strconv.ParseUint(r.PathValue("shard"), 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_shard", Message: "shard must be an unsigned 32-bit integer"})
		return
	}
	shardID := uint32(value)
	if !s.validateShardOwner(w, r.PathValue("collection"), shardID) {
		return
	}
	var request internalShardDeleteRequest
	if !decode(w, r, &request) {
		return
	}
	acknowledgement, err := parseAcknowledgement(request.Acknowledgement)
	if err != nil {
		writeError(w, err)
		return
	}
	acknowledged, ambiguous, err := s.commitLeaderShardDelete(r.Context(), r.PathValue("collection"), shardID, request.Namespace, r.PathValue("id"), acknowledgement, r.Header.Get("traceparent"))
	if err != nil {
		if ambiguous {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "replication_acknowledgement_unavailable", Message: err.Error()})
			return
		}
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "committed", "shard_id": shardID, "metadata_epoch": s.currentMetadataEpoch(), "replicas_acknowledged": acknowledged, "replication_factor": s.replicationFactor})
}

func (s *Server) authoritativeShardOwner(name string, shardID uint32) (string, cluster.Peer, bool, error) {
	config, _, err := s.engine.DescribeCollection(name)
	if err != nil {
		return "", cluster.Peer{}, false, err
	}
	peers, err := s.membershipPeers()
	if err != nil {
		return "", cluster.Peer{}, false, err
	}
	table, err := cluster.PlanPlacementWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, []core.CollectionConfig{config})
	if err != nil {
		return "", cluster.Peer{}, false, err
	}
	for _, assignment := range table.Shards {
		if assignment.ShardID != shardID {
			continue
		}
		if assignment.NodeID == s.nodeID {
			return assignment.NodeID, cluster.Peer{}, true, nil
		}
		for _, peer := range peers {
			if peer.NodeID == assignment.NodeID {
				return assignment.NodeID, peer, false, nil
			}
		}
		return assignment.NodeID, cluster.Peer{}, false, fmt.Errorf("placement node is not discoverable")
	}
	return "", cluster.Peer{}, false, fmt.Errorf("shard has no authoritative placement")
}

func (s *Server) remoteShardDelete(ctx context.Context, peer cluster.Peer, collection string, shardID uint32, namespace, id, traceparent string) (int, error) {
	payload, err := json.Marshal(internalShardDeleteRequest{Namespace: namespace, Acknowledgement: "quorum"})
	if err != nil {
		return 0, err
	}
	target := peer.SeedURL + "/v1/internal/shards/" + url.PathEscape(collection) + "/" + strconv.FormatUint(uint64(shardID), 10) + "/vectors/" + url.PathEscape(id)
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GideonDB-Cluster-ID", s.clusterID)
	request.Header.Set("X-GideonDB-Target-Node-ID", peer.NodeID)
	request.Header.Set("X-GideonDB-Metadata-Epoch", strconv.FormatUint(s.currentMetadataEpoch(), 10))
	if s.peerAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+s.peerAPIKey)
	}
	if traceparent != "" {
		request.Header.Set("traceparent", traceparent)
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
		ShardID              uint32 `json:"shard_id"`
		MetadataEpoch        uint64 `json:"metadata_epoch"`
		ReplicasAcknowledged int    `json:"replicas_acknowledged"`
		ReplicationFactor    int    `json:"replication_factor"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return 0, fmt.Errorf("decode peer response: %w", err)
	}
	if body.ShardID != shardID || body.MetadataEpoch != s.currentMetadataEpoch() || body.ReplicationFactor != s.replicationFactor || body.ReplicasAcknowledged < requiredAcknowledgements("quorum", s.replicationFactor) {
		return body.ReplicasAcknowledged, fmt.Errorf("peer response fence or quorum mismatch")
	}
	return body.ReplicasAcknowledged, nil
}
