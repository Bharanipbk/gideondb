package grpcapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	v1 "github.com/Bharanipbk/gideondb/gen/gideondb/v1"
	"github.com/Bharanipbk/gideondb/internal/core"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type responseCapture struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (r *responseCapture) Header() http.Header  { return r.header }
func (r *responseCapture) WriteHeader(code int) { r.status = code }
func (r *responseCapture) Write(value []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(value)
}

func (s *Server) distributedScroll(ctx context.Context, request *v1.ScrollRequest) (*v1.ScrollResponse, error) {
	if s.options.DistributedScroll == nil {
		return nil, status.Error(codes.Unimplemented, "distributed gRPC scroll is not configured")
	}
	limit := request.GetLimit()
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return nil, status.Error(codes.InvalidArgument, "limit must be between 1 and 200")
	}
	query := url.Values{
		"namespace":      {request.GetNamespace()},
		"limit":          {strconv.FormatUint(uint64(limit), 10)},
		"cursor":         {request.GetCursor()},
		"include_vector": {strconv.FormatBool(request.GetIncludeVector())},
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, "/v1/cluster/collections/"+url.PathEscape(request.GetCollection())+"/vectors?"+query.Encode(), nil)
	if err != nil {
		return nil, status.Error(codes.Internal, "construct distributed scroll request")
	}
	httpRequest.SetPathValue("name", request.GetCollection())
	capture := &responseCapture{header: make(http.Header)}
	s.options.DistributedScroll.ServeHTTP(capture, httpRequest)
	if capture.status != http.StatusOK {
		return nil, distributedHTTPError(capture.status)
	}
	var body struct {
		Records                []core.Record `json:"records"`
		NextCursor             string        `json:"next_cursor"`
		VectorsIncluded        bool          `json:"vectors_included"`
		MetadataEpoch          uint64        `json:"metadata_epoch"`
		AuthoritativePlacement bool          `json:"authoritative_placement"`
	}
	decoder := json.NewDecoder(io.LimitReader(&capture.body, maxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return nil, status.Error(codes.Internal, "decode distributed scroll response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, status.Error(codes.Internal, "distributed scroll response contains trailing data")
	}
	records := make([]*v1.Record, len(body.Records))
	for index, record := range body.Records {
		converted, err := toProtoRecord(record)
		if err != nil {
			return nil, status.Error(codes.Internal, "convert distributed scroll response")
		}
		records[index] = converted
	}
	return &v1.ScrollResponse{Records: records, NextCursor: body.NextCursor, VectorsIncluded: body.VectorsIncluded, MetadataEpoch: body.MetadataEpoch, AuthoritativePlacement: body.AuthoritativePlacement}, nil
}

func (s *Server) distributedSearch(ctx context.Context, request *v1.SearchRequest) (*v1.SearchResponse, error) {
	filter := map[string]any(nil)
	if request.GetFilter() != nil {
		filter = request.GetFilter().AsMap()
	}
	payload, err := json.Marshal(struct {
		Vector       []float32      `json:"vector"`
		TopK         uint32         `json:"top_k"`
		Namespace    string         `json:"namespace,omitempty"`
		Filter       map[string]any `json:"filter,omitempty"`
		AllowPartial bool           `json:"allow_partial,omitempty"`
	}{Vector: request.GetVector(), TopK: request.GetTopK(), Namespace: request.GetNamespace(), Filter: filter, AllowPartial: request.GetAllowPartial()})
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "search filter cannot be encoded")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/cluster/collections/"+url.PathEscape(request.GetCollection())+"/search", bytes.NewReader(payload))
	if err != nil {
		return nil, status.Error(codes.Internal, "construct distributed search request")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.SetPathValue("name", request.GetCollection())
	capture := &responseCapture{header: make(http.Header)}
	s.options.DistributedSearch.ServeHTTP(capture, httpRequest)
	if capture.status != http.StatusOK {
		return nil, distributedSearchHTTPError(capture.status)
	}
	var body struct {
		Results  []core.SearchResult `json:"results"`
		Failures []struct {
			ShardID uint32 `json:"shard_id"`
			NodeID  string `json:"node_id"`
			Error   string `json:"error"`
		} `json:"failures"`
		Partial                bool   `json:"partial"`
		MetadataEpoch          uint64 `json:"metadata_epoch"`
		AuthoritativePlacement bool   `json:"authoritative_placement"`
	}
	decoder := json.NewDecoder(io.LimitReader(&capture.body, maxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return nil, status.Error(codes.Internal, "decode distributed search response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, status.Error(codes.Internal, "distributed search response contains trailing data")
	}
	results, err := toProtoResults(body.Results)
	if err != nil {
		return nil, status.Error(codes.Internal, "convert distributed search response")
	}
	failures := make([]*v1.ShardFailure, len(body.Failures))
	for index, failure := range body.Failures {
		failures[index] = &v1.ShardFailure{ShardId: failure.ShardID, NodeId: failure.NodeID, Error: failure.Error}
	}
	return &v1.SearchResponse{Results: results, Failures: failures, Partial: body.Partial, MetadataEpoch: body.MetadataEpoch, AuthoritativePlacement: body.AuthoritativePlacement}, nil
}

func distributedSearchHTTPError(code int) error {
	switch code {
	case http.StatusBadRequest:
		return status.Error(codes.InvalidArgument, "distributed search request is invalid")
	case http.StatusUnauthorized:
		return status.Error(codes.Unauthenticated, "distributed search authentication failed")
	case http.StatusForbidden:
		return status.Error(codes.PermissionDenied, "distributed search is forbidden")
	case http.StatusNotFound:
		return status.Error(codes.NotFound, "collection not found")
	case http.StatusConflict:
		return status.Error(codes.FailedPrecondition, "distributed search placement is stale")
	case http.StatusTooManyRequests:
		return status.Error(codes.ResourceExhausted, "distributed search rate limit exceeded")
	default:
		return status.Error(codes.Unavailable, "distributed search is unavailable")
	}
}

func (s *Server) distributedDelete(ctx context.Context, request *v1.DeleteRequest) (*v1.DeleteResponse, error) {
	query := url.Values{}
	if request.GetNamespace() != "" {
		query.Set("namespace", request.GetNamespace())
	}
	path := "/v1/cluster/collections/" + url.PathEscape(request.GetCollection()) + "/vectors/" + url.PathEscape(request.GetId())
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return nil, status.Error(codes.Internal, "construct distributed delete request")
	}
	httpRequest.SetPathValue("name", request.GetCollection())
	httpRequest.SetPathValue("id", request.GetId())
	capture := &responseCapture{header: make(http.Header)}
	s.options.DistributedDelete.ServeHTTP(capture, httpRequest)
	if capture.status != http.StatusOK {
		return nil, distributedWriteHTTPError(capture.status)
	}
	return &v1.DeleteResponse{}, nil
}

func (s *Server) distributedBatchUpsert(ctx context.Context, collection string, records []core.Record, acknowledgement v1.Acknowledgement) (*v1.BatchUpsertResponse, error) {
	level := map[v1.Acknowledgement]string{
		v1.Acknowledgement_ACKNOWLEDGEMENT_UNSPECIFIED: "quorum",
		v1.Acknowledgement_ACKNOWLEDGEMENT_LEADER:      "leader",
		v1.Acknowledgement_ACKNOWLEDGEMENT_QUORUM:      "quorum",
		v1.Acknowledgement_ACKNOWLEDGEMENT_ALL:         "all",
	}[acknowledgement]
	if level == "" {
		return nil, status.Error(codes.InvalidArgument, "acknowledgement is invalid")
	}
	payload, err := json.Marshal(struct {
		Records         []core.Record `json:"records"`
		Acknowledgement string        `json:"acknowledgement"`
	}{Records: records, Acknowledgement: level})
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "records cannot be encoded")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/cluster/collections/"+url.PathEscape(collection)+"/vectors/batch", bytes.NewReader(payload))
	if err != nil {
		return nil, status.Error(codes.Internal, "construct distributed upsert request")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.SetPathValue("name", collection)
	capture := &responseCapture{header: make(http.Header)}
	s.options.DistributedUpsert.ServeHTTP(capture, httpRequest)
	if capture.status != http.StatusOK && capture.status != http.StatusMultiStatus {
		return nil, distributedWriteHTTPError(capture.status)
	}
	var body struct {
		Outcomes []struct {
			ShardID              uint32        `json:"shard_id"`
			NodeID               string        `json:"node_id"`
			Status               string        `json:"status"`
			Records              []core.Record `json:"records"`
			ReplicasAcknowledged uint32        `json:"replicas_acknowledged"`
			ReplicationFactor    uint32        `json:"replication_factor"`
			Error                string        `json:"error"`
		} `json:"outcomes"`
		Partial       bool   `json:"partial"`
		MetadataEpoch uint64 `json:"metadata_epoch"`
		Authoritative bool   `json:"authoritative_placement"`
	}
	decoder := json.NewDecoder(io.LimitReader(&capture.body, maxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return nil, status.Error(codes.Internal, "decode distributed upsert response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, status.Error(codes.Internal, "distributed upsert response contains trailing data")
	}
	response := &v1.BatchUpsertResponse{Partial: body.Partial, MetadataEpoch: body.MetadataEpoch}
	for _, outcome := range body.Outcomes {
		response.Outcomes = append(response.Outcomes, &v1.ShardWriteOutcome{ShardId: outcome.ShardID, NodeId: outcome.NodeID, Status: outcome.Status, Error: outcome.Error, ReplicasAcknowledged: outcome.ReplicasAcknowledged, ReplicationFactor: outcome.ReplicationFactor})
		if outcome.Status != "committed" {
			continue
		}
		for _, record := range outcome.Records {
			converted, err := toProtoRecord(record)
			if err != nil {
				return nil, status.Error(codes.Internal, "convert distributed upsert response")
			}
			response.Records = append(response.Records, converted)
		}
	}
	return response, nil
}

func distributedWriteHTTPError(code int) error {
	switch code {
	case http.StatusBadRequest:
		return status.Error(codes.InvalidArgument, "distributed upsert request is invalid")
	case http.StatusUnauthorized:
		return status.Error(codes.Unauthenticated, "distributed upsert authentication failed")
	case http.StatusForbidden:
		return status.Error(codes.PermissionDenied, "distributed upsert is forbidden")
	case http.StatusNotFound:
		return status.Error(codes.NotFound, "collection not found")
	case http.StatusConflict:
		return status.Error(codes.FailedPrecondition, "distributed upsert placement is stale")
	case http.StatusTooManyRequests:
		return status.Error(codes.ResourceExhausted, "distributed upsert rate limit exceeded")
	default:
		return status.Error(codes.Unavailable, "distributed upsert is unavailable")
	}
}

func distributedHTTPError(code int) error {
	switch code {
	case http.StatusBadRequest:
		return status.Error(codes.InvalidArgument, "distributed scroll request is invalid")
	case http.StatusUnauthorized:
		return status.Error(codes.Unauthenticated, "distributed scroll authentication failed")
	case http.StatusForbidden:
		return status.Error(codes.PermissionDenied, "distributed scroll is forbidden")
	case http.StatusNotFound:
		return status.Error(codes.NotFound, "collection not found")
	case http.StatusConflict:
		return status.Error(codes.FailedPrecondition, "distributed scroll cursor or placement is stale")
	case http.StatusTooManyRequests:
		return status.Error(codes.ResourceExhausted, "distributed scroll rate limit exceeded")
	default:
		return status.Error(codes.Unavailable, "distributed scroll is unavailable")
	}
}
