package rest

import "net/http"

// AdminRoute is a REST administrative endpoint that the gRPC transport
// exposes as a typed RPC. The handler is the unwrapped REST handler; the gRPC
// layer authenticates and authorizes before calling it.
type AdminRoute struct {
	Method  string
	Path    string
	Handler http.Handler
}

// AdminRoutes returns the administrative and cluster endpoints keyed by a
// stable name shared with the gRPC transport.
func (s *Server) AdminRoutes() map[string]AdminRoute {
	route := func(method, path string, handler http.HandlerFunc) AdminRoute {
		return AdminRoute{Method: method, Path: path, Handler: handler}
	}
	return map[string]AdminRoute{
		"ready":             route(http.MethodGet, "/v1/ready", s.ready),
		"cluster_readiness": route(http.MethodGet, "/v1/cluster/readiness", s.clusterReadiness),
		"cluster_peers":     route(http.MethodGet, "/v1/cluster/peers", s.clusterPeers),
		"cluster_placement": route(http.MethodGet, "/v1/cluster/placement", s.clusterPlacement),
		"cluster_replicas":  route(http.MethodGet, "/v1/cluster/replicas", s.clusterReplicaPlacement),
		"rebalance_plan":    route(http.MethodGet, "/v1/cluster/rebalance/plan", s.clusterRebalancePlan),
		"rebalance_prepare": route(http.MethodPost, "/v1/cluster/rebalance/prepare", s.clusterRebalancePrepare),
		"rebalance_apply":   route(http.MethodPost, "/v1/cluster/rebalance/apply", s.clusterRebalanceApply),
		"rebalance_abort":   route(http.MethodPost, "/v1/cluster/rebalance/abort", s.clusterRebalanceAbort),
		"metadata_epoch":    route(http.MethodPost, "/v1/cluster/metadata/epoch", s.advanceMetadataEpoch),
		"recovery_point":    route(http.MethodPost, "/v1/cluster/backup/recovery-point", s.clusterBackupRecoveryPoint),
		"logs":              route(http.MethodGet, "/v1/logs", s.recentLogs),
		"cluster_logs":      route(http.MethodGet, "/v1/cluster/logs", s.clusterLogs),
		"embedders":         route(http.MethodGet, "/v1/embedders", s.embedderStats),
		"rerankers":         route(http.MethodGet, "/v1/rerankers", s.rerankerList),
	}
}
