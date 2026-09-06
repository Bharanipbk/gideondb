package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/Bharanipbk/gideondb/internal/cluster"
)

type clusterHTTPEvent struct {
	httpEvent
	NodeID string `json:"node_id"`
}
type logNodeFailure struct {
	NodeID string `json:"node_id"`
	Error  string `json:"error"`
}

func (s *Server) internalLogs(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	limit, level, ok := parseLogQuery(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": s.events.recent(limit, level), "node_id": s.nodeID, "metadata_epoch": s.currentMetadataEpoch()})
}

func (s *Server) clusterLogs(w http.ResponseWriter, r *http.Request) {
	limit, level, ok := parseLogQuery(w, r)
	if !ok {
		return
	}
	epoch := s.currentMetadataEpoch()
	events := make([]clusterHTTPEvent, 0, limit)
	for _, event := range s.events.recent(limit, level) {
		events = append(events, clusterHTTPEvent{httpEvent: event, NodeID: s.nodeID})
	}
	peers, err := s.membershipPeers()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	failures := make([]logNodeFailure, 0)
	for _, peer := range peers {
		remote, fetchErr := s.remoteLogs(ctx, peer, limit, level)
		if fetchErr != nil {
			failures = append(failures, logNodeFailure{NodeID: peer.NodeID, Error: fetchErr.Error()})
			continue
		}
		for _, event := range remote {
			events = append(events, clusterHTTPEvent{httpEvent: event, NodeID: peer.NodeID})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if !events[i].Timestamp.Equal(events[j].Timestamp) {
			return events[i].Timestamp.After(events[j].Timestamp)
		}
		if events[i].NodeID != events[j].NodeID {
			return events[i].NodeID < events[j].NodeID
		}
		return events[i].Sequence > events[j].Sequence
	})
	if len(events) > limit {
		events = events[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "failures": failures, "partial": len(failures) > 0, "metadata_epoch": epoch, "per_node_retention": s.events.capacity})
}

func (s *Server) remoteLogs(ctx context.Context, peer cluster.Peer, limit int, level string) ([]httpEvent, error) {
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if level != "" {
		query.Set("level", level)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, peer.SeedURL+"/v1/internal/logs?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-GideonDB-Cluster-ID", s.clusterID)
	request.Header.Set("X-GideonDB-Target-Node-ID", peer.NodeID)
	request.Header.Set("X-GideonDB-Metadata-Epoch", strconv.FormatUint(s.currentMetadataEpoch(), 10))
	if s.peerAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+s.peerAPIKey)
	}
	response, err := s.internalClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peer returned HTTP %d", response.StatusCode)
	}
	var body struct {
		Events        []httpEvent `json:"events"`
		NodeID        string      `json:"node_id"`
		MetadataEpoch uint64      `json:"metadata_epoch"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return nil, fmt.Errorf("decode peer response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("peer response must contain one JSON value")
	}
	if body.NodeID != peer.NodeID || body.MetadataEpoch != s.currentMetadataEpoch() {
		return nil, fmt.Errorf("peer response fence mismatch")
	}
	for _, event := range body.Events {
		if !validHTTPEvent(event) {
			return nil, fmt.Errorf("peer returned invalid event")
		}
	}
	return body.Events, nil
}

func validHTTPEvent(event httpEvent) bool {
	return event.Sequence > 0 && !event.Timestamp.IsZero() && len(event.Level) <= 5 && len(event.Event) <= 64 && len(event.Method) <= 16 && len(event.Route) > 0 && len(event.Route) <= 256 && event.Status >= 100 && event.Status <= 599 && len(event.TraceID) <= 64 && len(event.SpanID) <= 32
}
