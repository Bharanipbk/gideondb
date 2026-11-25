package grpcapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	v1 "github.com/Bharanipbk/gideondb/gen/gideondb/v1"
)

// AdminRoute is a REST administrative endpoint bridged to a gRPC RPC. The
// handler must be the unwrapped REST handler: gRPC authenticates and
// authorizes before calling it.
type AdminRoute struct {
	Method  string
	Path    string
	Handler http.Handler
}

// adminRPC binds a gRPC method to a route name in Options.AdminRoutes.
type adminRPC struct {
	route     string
	adminOnly bool
}

var adminRPCs = map[string]adminRPC{
	v1.GideonDBService_Ready_FullMethodName:                {route: "ready"},
	v1.GideonDBService_ClusterReadiness_FullMethodName:     {route: "cluster_readiness"},
	v1.GideonDBService_ListPeers_FullMethodName:            {route: "cluster_peers"},
	v1.GideonDBService_GetPlacement_FullMethodName:         {route: "cluster_placement"},
	v1.GideonDBService_ListReplicas_FullMethodName:         {route: "cluster_replicas"},
	v1.GideonDBService_PlanRebalance_FullMethodName:        {route: "rebalance_plan", adminOnly: true},
	v1.GideonDBService_PrepareRebalance_FullMethodName:     {route: "rebalance_prepare", adminOnly: true},
	v1.GideonDBService_ApplyRebalance_FullMethodName:       {route: "rebalance_apply", adminOnly: true},
	v1.GideonDBService_AbortRebalance_FullMethodName:       {route: "rebalance_abort", adminOnly: true},
	v1.GideonDBService_AdvanceMetadataEpoch_FullMethodName: {route: "metadata_epoch", adminOnly: true},
	v1.GideonDBService_CreateRecoveryPoint_FullMethodName:  {route: "recovery_point", adminOnly: true},
	v1.GideonDBService_RecentLogs_FullMethodName:           {route: "logs"},
	v1.GideonDBService_ClusterLogs_FullMethodName:          {route: "cluster_logs"},
	v1.GideonDBService_ListEmbedders_FullMethodName:        {route: "embedders"},
	v1.GideonDBService_ListRerankers_FullMethodName:        {route: "rerankers"},
}

func (s *Server) admin(ctx context.Context, method string, request *v1.AdminRequest) (*v1.AdminResponse, error) {
	rpc := adminRPCs[method]
	route, ok := s.options.AdminRoutes[rpc.route]
	if !ok || route.Handler == nil {
		return nil, status.Error(codes.Unimplemented, "this administrative RPC is not configured on this node")
	}
	var body io.Reader = http.NoBody
	if request.GetBody() != nil {
		payload, err := json.Marshal(request.GetBody().AsMap())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "request body cannot be encoded")
		}
		body = bytes.NewReader(payload)
	} else if route.Method == http.MethodPost {
		body = bytes.NewReader([]byte("{}"))
	}
	target := route.Path
	if len(request.GetQuery()) > 0 {
		values := url.Values{}
		for key, value := range request.GetQuery() {
			values.Set(key, value)
		}
		target += "?" + values.Encode()
	}
	httpRequest, err := http.NewRequestWithContext(ctx, route.Method, target, body)
	if err != nil {
		return nil, status.Error(codes.Internal, "construct administrative request")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	capture := &responseCapture{header: make(http.Header)}
	route.Handler.ServeHTTP(capture, httpRequest)
	if capture.status == 0 {
		capture.status = http.StatusOK
	}
	var decoded any
	if capture.body.Len() > 0 {
		if err := json.Unmarshal(capture.body.Bytes(), &decoded); err != nil {
			return nil, status.Error(codes.Internal, "decode administrative response")
		}
	}
	if capture.status >= 300 {
		message := http.StatusText(capture.status)
		if object, ok := decoded.(map[string]any); ok {
			if text, ok := object["message"].(string); ok && text != "" {
				message = text
			}
		}
		return nil, status.Error(httpStatusCode(capture.status), message)
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		object = map[string]any{"items": decoded}
	}
	result, err := structpb.NewStruct(object)
	if err != nil {
		return nil, status.Error(codes.Internal, "convert administrative response")
	}
	return &v1.AdminResponse{Result: result}, nil
}

func httpStatusCode(code int) codes.Code {
	switch code {
	case http.StatusBadRequest:
		return codes.InvalidArgument
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusNotFound:
		return codes.NotFound
	case http.StatusConflict, http.StatusPreconditionFailed:
		return codes.FailedPrecondition
	case http.StatusTooManyRequests:
		return codes.ResourceExhausted
	case http.StatusNotImplemented:
		return codes.Unimplemented
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return codes.Unavailable
	}
	return codes.Internal
}

func (s *Server) Ready(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_Ready_FullMethodName, r)
}
func (s *Server) ClusterReadiness(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_ClusterReadiness_FullMethodName, r)
}
func (s *Server) ListPeers(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_ListPeers_FullMethodName, r)
}
func (s *Server) GetPlacement(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_GetPlacement_FullMethodName, r)
}
func (s *Server) ListReplicas(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_ListReplicas_FullMethodName, r)
}
func (s *Server) PlanRebalance(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_PlanRebalance_FullMethodName, r)
}
func (s *Server) PrepareRebalance(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_PrepareRebalance_FullMethodName, r)
}
func (s *Server) ApplyRebalance(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_ApplyRebalance_FullMethodName, r)
}
func (s *Server) AbortRebalance(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_AbortRebalance_FullMethodName, r)
}
func (s *Server) AdvanceMetadataEpoch(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_AdvanceMetadataEpoch_FullMethodName, r)
}
func (s *Server) CreateRecoveryPoint(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_CreateRecoveryPoint_FullMethodName, r)
}
func (s *Server) RecentLogs(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_RecentLogs_FullMethodName, r)
}
func (s *Server) ClusterLogs(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_ClusterLogs_FullMethodName, r)
}
func (s *Server) ListEmbedders(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_ListEmbedders_FullMethodName, r)
}
func (s *Server) ListRerankers(ctx context.Context, r *v1.AdminRequest) (*v1.AdminResponse, error) {
	return s.admin(ctx, v1.GideonDBService_ListRerankers_FullMethodName, r)
}
