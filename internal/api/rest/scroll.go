package rest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
)

type distributedScrollCursor struct {
	Version        int    `json:"v"`
	Epoch          uint64 `json:"e"`
	Namespace      string `json:"q,omitempty"`
	AfterNamespace string `json:"n"`
	AfterID        string `json:"i"`
}

type shardScrollOutcome struct {
	records []core.Record
	more    bool
	err     error
}

func (s *Server) scroll(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_limit", Message: "limit must be between 1 and 200"})
			return
		}
		limit = value
	}
	afterNamespace, afterID := "", ""
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		parts := strings.SplitN(string(decoded), "\x00", 2)
		if err != nil || len(parts) != 2 || parts[1] == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_cursor", Message: "cursor is invalid"})
			return
		}
		afterNamespace, afterID = parts[0], parts[1]
	}
	records, more, err := s.engine.Scroll(r.PathValue("name"), r.URL.Query().Get("namespace"), afterNamespace, afterID, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	includeVector := r.URL.Query().Get("include_vector") == "true"
	if !includeVector {
		for index := range records {
			records[index].Vector = nil
		}
	}
	next := ""
	if more && len(records) != 0 {
		last := records[len(records)-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.Namespace + "\x00" + last.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records, "next_cursor": next, "vectors_included": includeVector})
}

func (s *Server) internalShardScroll(w http.ResponseWriter, r *http.Request) {
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
	limit, afterNamespace, afterID, ok := parseInternalScrollQuery(w, r)
	if !ok {
		return
	}
	records, more, err := s.engine.ScrollShard(r.PathValue("collection"), uint32(shardID), r.URL.Query().Get("namespace"), afterNamespace, afterID, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records, "more": more, "shard_id": uint32(shardID), "metadata_epoch": s.currentMetadataEpoch()})
}

func parseInternalScrollQuery(w http.ResponseWriter, r *http.Request) (int, string, string, bool) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 || limit > 200 {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_limit", Message: "limit must be between 1 and 200"})
		return 0, "", "", false
	}
	return limit, r.URL.Query().Get("after_namespace"), r.URL.Query().Get("after_id"), true
}

func (s *Server) distributedScroll(w http.ResponseWriter, r *http.Request) {
	if !s.requireStaticPlacement(w) {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_limit", Message: "limit must be between 1 and 200"})
			return
		}
		limit = value
	}
	namespace, afterNamespace, afterID := r.URL.Query().Get("namespace"), "", ""
	epoch := s.currentMetadataEpoch()
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		var cursor distributedScrollCursor
		if err != nil || len(decoded) > 4096 || json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 1 || cursor.AfterID == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_cursor", Message: "cursor is invalid"})
			return
		}
		if cursor.Epoch != epoch {
			writeJSON(w, http.StatusConflict, apiError{Code: "stale_cursor", Message: "cursor metadata epoch is stale"})
			return
		}
		if cursor.Namespace != namespace {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "cursor_namespace_mismatch", Message: "cursor namespace does not match request"})
			return
		}
		afterNamespace, afterID = cursor.AfterNamespace, cursor.AfterID
	}
	config, _, err := s.engine.DescribeCollection(r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	peers, err := s.membershipPeers()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: err.Error()})
		return
	}
	table, err := cluster.PlanPlacementWithCapacity(epoch, s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, []core.CollectionConfig{config})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "placement_unavailable", Message: err.Error()})
		return
	}
	peerByID := make(map[string]cluster.Peer, len(peers))
	for _, peer := range peers {
		peerByID[peer.NodeID] = peer
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	outcomes := make([]shardScrollOutcome, len(table.Shards))
	for position, assignment := range table.Shards {
		if assignment.NodeID == s.nodeID {
			outcomes[position].records, outcomes[position].more, outcomes[position].err = s.engine.ScrollShard(config.Name, assignment.ShardID, namespace, afterNamespace, afterID, limit)
			continue
		}
		peer, exists := peerByID[assignment.NodeID]
		if !exists {
			outcomes[position].err = fmt.Errorf("placement node %s is not discoverable", assignment.NodeID)
			continue
		}
		outcomes[position].records, outcomes[position].more, outcomes[position].err = s.remoteShardScroll(ctx, peer, config.Name, assignment.ShardID, namespace, afterNamespace, afterID, limit, r.Header.Get("traceparent"))
	}
	all := make([]core.Record, 0, len(outcomes)*limit)
	more := false
	for position, outcome := range outcomes {
		if outcome.err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "distributed_scroll_failed", Message: fmt.Sprintf("shard %d: %v", table.Shards[position].ShardID, outcome.err)})
			return
		}
		all = append(all, outcome.records...)
		more = more || outcome.more
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Namespace < all[j].Namespace || (all[i].Namespace == all[j].Namespace && all[i].ID < all[j].ID)
	})
	if len(all) > limit {
		all = all[:limit]
		more = true
	}
	includeVector := r.URL.Query().Get("include_vector") == "true"
	if !includeVector {
		for index := range all {
			all[index].Vector = nil
		}
	}
	next := ""
	if more && len(all) > 0 {
		cursor, _ := json.Marshal(distributedScrollCursor{Version: 1, Epoch: epoch, Namespace: namespace, AfterNamespace: all[len(all)-1].Namespace, AfterID: all[len(all)-1].ID})
		next = base64.RawURLEncoding.EncodeToString(cursor)
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": all, "next_cursor": next, "vectors_included": includeVector, "metadata_epoch": epoch, "authoritative_placement": s.staticPlacementReady()})
}

func (s *Server) remoteShardScroll(ctx context.Context, peer cluster.Peer, collection string, shardID uint32, namespace, afterNamespace, afterID string, limit int, traceparent string) ([]core.Record, bool, error) {
	query := url.Values{"limit": {strconv.Itoa(limit)}, "namespace": {namespace}, "after_namespace": {afterNamespace}, "after_id": {afterID}}
	target := peer.SeedURL + "/v1/internal/shards/" + url.PathEscape(collection) + "/" + strconv.FormatUint(uint64(shardID), 10) + "/vectors?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, false, err
	}
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
		return nil, false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("peer returned HTTP %d", response.StatusCode)
	}
	var body struct {
		Records       []core.Record `json:"records"`
		More          bool          `json:"more"`
		ShardID       uint32        `json:"shard_id"`
		MetadataEpoch uint64        `json:"metadata_epoch"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return nil, false, fmt.Errorf("decode peer response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false, fmt.Errorf("peer response must contain one JSON value")
	}
	if body.ShardID != shardID || body.MetadataEpoch != s.currentMetadataEpoch() {
		return nil, false, fmt.Errorf("peer response fence mismatch")
	}
	return body.Records, body.More, nil
}
