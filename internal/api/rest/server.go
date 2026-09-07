// Package rest exposes the versioned HTTP API.
package rest

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
	"github.com/Bharanipbk/gideondb/internal/metadata"
)

const maxBodyBytes = 16 << 20

type Server struct {
	engine              *engine.Engine
	logger              *slog.Logger
	mux                 *http.ServeMux
	metrics             *metricsRegistry
	events              *eventLog
	apiKeyHash          []byte
	nodeID              string
	clusterID           string
	advertiseAddress    string
	startedAt           time.Time
	metadataEpoch       uint64
	peerProvider        PeerProvider
	peerAPIKey          string
	internalClient      *http.Client
	staticRouting       bool
	replicationFactor   int
	placementCapacity   uint32
	raftStore           *cluster.RaftStore
	raftProtocol        RaftProtocol
	rebalanceBarriers   *cluster.RebalanceBarrierStore
	rebalanceExecutor   *cluster.RebalanceExecutor
	ownershipMu         sync.Mutex
	ownershipApplied    bool
	replicationMu       sync.Mutex
	repairMu            sync.Mutex
	repairRetries       map[string]replicaRepairRetry
	backupGate          sync.RWMutex
	backupStateMu       sync.Mutex
	backupOperation     string
	requireInternalMTLS bool
	draining            atomic.Bool
	rateLimiter         *requestRateLimiter
}

func New(e *engine.Engine, logger *slog.Logger) *Server {
	return NewWithOptions(e, logger, Options{})
}

func NewWithOptions(e *engine.Engine, logger *slog.Logger, options Options) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	events := newEventLog(256)
	if options.EventLogPath != "" {
		events = newPersistentEventLog(options.EventLogPath, 4096)
	}
	s := &Server{engine: e, logger: logger, mux: http.NewServeMux(), metrics: newMetricsRegistry(), events: events, nodeID: options.NodeID, clusterID: options.ClusterID, advertiseAddress: options.AdvertiseAddress, startedAt: time.Now().UTC(), metadataEpoch: options.MetadataEpoch, peerProvider: options.PeerProvider, peerAPIKey: options.APIKey, internalClient: options.InternalHTTPClient, staticRouting: options.EnableStaticRouting, replicationFactor: options.ReplicationFactor, placementCapacity: options.PlacementCapacity, raftStore: options.RaftStore, raftProtocol: options.RaftProtocol, rebalanceBarriers: options.RebalanceBarriers, rebalanceExecutor: options.RebalanceExecutor, requireInternalMTLS: options.RequireInternalMTLS}
	if options.RateLimitPerSecond > 0 && options.RateLimitBurst > 0 {
		s.rateLimiter = newRequestRateLimiter(options.RateLimitPerSecond, options.RateLimitBurst)
	}
	if s.replicationFactor == 0 {
		s.replicationFactor = 1
	}
	if s.placementCapacity == 0 {
		s.placementCapacity = 1
	}
	if s.raftStore != nil {
		if committed, exists := s.raftStore.CommittedView(); exists {
			if capacities, err := cluster.ParseCapacityManifest(committed.CapacityManifest); err == nil {
				for _, capacity := range capacities {
					if capacity.NodeID == s.nodeID {
						s.placementCapacity = capacity.Capacity
						break
					}
				}
			}
		}
	}
	s.repairRetries = make(map[string]replicaRepairRetry)
	if s.raftProtocol == nil {
		s.raftProtocol = s.raftStore
	}
	if s.metadataEpoch == 0 {
		s.metadataEpoch = 1
	}
	if s.internalClient == nil {
		s.internalClient = &http.Client{Timeout: 5 * time.Second}
	}
	if options.APIKey != "" {
		digest := sha256.Sum256([]byte(options.APIKey))
		s.apiKeyHash = append([]byte(nil), digest[:]...)
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return securityHeaders(s.metrics.middleware(s.traceMiddleware(s.rateLimitMiddleware(s.drainMiddleware(s.backupBarrierMiddleware(requireInternalMTLS(s.requireInternalMTLS, s.mux)))))))
}

func (s *Server) BeginDrain() { s.draining.Store(true) }

func (s *Server) drainMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.draining.Load() && r.URL.Path != "/v1/health" && r.URL.Path != "/v1/ready" && !strings.HasPrefix(r.URL.Path, "/v1/internal/raft/") {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "node_draining", Message: "node is terminating"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusTemporaryRedirect)
	})
	s.mux.HandleFunc("GET /docs", s.docsWeb)
	s.mux.HandleFunc("GET /docs/", s.docsWeb)
	s.mux.HandleFunc("GET /docs/_index", s.docsIndex)
	s.mux.HandleFunc("GET /docs/_content/{path...}", s.docsContent)
	s.mux.HandleFunc("GET /dashboard", s.dashboard)
	s.mux.HandleFunc("GET /dashboard/", s.dashboard)
	s.mux.HandleFunc("GET /v1/health", s.health)
	s.mux.HandleFunc("GET /v1/ready", s.ready)
	s.mux.HandleFunc("GET /metrics", s.protected(s.serveMetrics))
	s.mux.HandleFunc("GET /v1/logs", s.protected(s.recentLogs))
	s.mux.HandleFunc("GET /v1/cluster/logs", s.protected(s.clusterLogs))
	s.mux.HandleFunc("GET /v1/internal/logs", s.protected(s.internalLogs))
	s.mux.HandleFunc("GET /v1/collections", s.protected(s.listCollections))
	s.mux.HandleFunc("GET /v1/node", s.protected(s.nodeInfo))
	s.mux.HandleFunc("GET /v1/cluster/peers", s.protected(s.clusterPeers))
	s.mux.HandleFunc("GET /v1/cluster/placement", s.protected(s.clusterPlacement))
	s.mux.HandleFunc("GET /v1/cluster/replicas", s.protected(s.clusterReplicaPlacement))
	s.mux.HandleFunc("GET /v1/cluster/rebalance/plan", s.protected(s.clusterRebalancePlan))
	s.mux.HandleFunc("POST /v1/cluster/rebalance/prepare", s.protected(s.clusterRebalancePrepare))
	s.mux.HandleFunc("POST /v1/cluster/rebalance/apply", s.protected(s.clusterRebalanceApply))
	s.mux.HandleFunc("POST /v1/cluster/rebalance/abort", s.protected(s.clusterRebalanceAbort))
	s.mux.HandleFunc("GET /v1/cluster/readiness", s.protected(s.clusterReadiness))
	s.mux.HandleFunc("POST /v1/cluster/backup/recovery-point", s.protected(s.clusterBackupRecoveryPoint))
	s.mux.HandleFunc("POST /v1/cluster/metadata/epoch", s.protected(s.advanceMetadataEpoch))
	s.mux.HandleFunc("POST /v1/internal/shards/{collection}/{shard}/search", s.protected(s.internalShardSearch))
	s.mux.HandleFunc("GET /v1/internal/shards/{collection}/{shard}/vectors", s.protected(s.internalShardScroll))
	s.mux.HandleFunc("POST /v1/internal/shards/{collection}/{shard}/vectors/batch", s.protected(s.internalShardBatchUpsert))
	s.mux.HandleFunc("POST /v1/internal/replicas/{collection}/{shard}/append", s.protected(s.internalReplicaAppend))
	s.mux.HandleFunc("POST /v1/internal/replicas/{collection}/{shard}/snapshot", s.protected(s.internalReplicaSnapshot))
	s.mux.HandleFunc("GET /v1/internal/replicas/{collection}/{shard}/status", s.protected(s.internalReplicaStatus))
	s.mux.HandleFunc("POST /v1/internal/rebalance/{collection}/{shard}/snapshot", s.protected(s.internalRebalanceSnapshot))
	s.mux.HandleFunc("POST /v1/internal/rebalance/action", s.protected(s.internalRebalanceAction))
	s.mux.HandleFunc("POST /v1/internal/rebalance/release", s.protected(s.internalRebalanceRelease))
	s.mux.HandleFunc("POST /v1/internal/rebalance/abort", s.protected(s.internalRebalanceAbort))
	s.mux.HandleFunc("POST /v1/internal/raft/request-vote", s.protected(s.raftRequestVote))
	s.mux.HandleFunc("POST /v1/internal/raft/append-entries", s.protected(s.raftAppendEntries))
	s.mux.HandleFunc("POST /v1/internal/raft/install-snapshot", s.protected(s.raftInstallSnapshot))
	s.mux.HandleFunc("POST /v1/internal/raft/timeout-now", s.protected(s.raftTimeoutNow))
	s.mux.HandleFunc("POST /v1/internal/backup/freeze", s.protected(s.internalBackupFreeze))
	s.mux.HandleFunc("GET /v1/internal/backup/recovery-point", s.protected(s.internalBackupRecoveryPoint))
	s.mux.HandleFunc("POST /v1/internal/backup/archive", s.protected(s.internalBackupArchive))
	s.mux.HandleFunc("POST /v1/internal/backup/release", s.protected(s.internalBackupRelease))
	s.mux.HandleFunc("POST /v1/cluster/collections/{name}/search", s.protected(s.distributedSearch))
	s.mux.HandleFunc("GET /v1/cluster/collections/{name}/vectors", s.protected(s.distributedScroll))
	s.mux.HandleFunc("POST /v1/cluster/collections/{name}/vectors/batch", s.protected(s.distributedBatchUpsert))
	s.mux.HandleFunc("POST /v1/collections", s.protected(s.createCollection))
	s.mux.HandleFunc("GET /v1/collections/{name}", s.protected(s.describeCollection))
	s.mux.HandleFunc("DELETE /v1/collections/{name}", s.protected(s.deleteCollection))
	s.mux.HandleFunc("POST /v1/collections/{name}/vectors", s.protected(s.upsert))
	s.mux.HandleFunc("GET /v1/collections/{name}/vectors", s.protected(s.scroll))
	s.mux.HandleFunc("POST /v1/collections/{name}/vectors/batch", s.protected(s.batchUpsert))
	s.mux.HandleFunc("GET /v1/collections/{name}/vectors/{id}", s.protected(s.get))
	s.mux.HandleFunc("DELETE /v1/collections/{name}/vectors/{id}", s.protected(s.delete))
	s.mux.HandleFunc("POST /v1/collections/{name}/search", s.protected(s.search))
}

func (s *Server) currentMetadataEpoch() uint64 {
	if s.raftStore != nil {
		_, _, epoch := s.raftStore.State()
		return epoch
	}
	return s.metadataEpoch
}

func (s *Server) currentPlacementCapacity() uint32 {
	if capacity, exists := s.committedPlacementCapacities()[s.nodeID]; exists {
		return capacity
	}
	return s.placementCapacity
}

func (s *Server) committedPlacementCapacities() map[string]uint32 {
	result := map[string]uint32{}
	if s.raftStore == nil {
		return result
	}
	committed, exists := s.raftStore.CommittedView()
	if !exists {
		return result
	}
	capacities, err := cluster.ParseCapacityManifest(committed.CapacityManifest)
	if err != nil {
		return result
	}
	for _, capacity := range capacities {
		result[capacity.NodeID] = capacity.Capacity
	}
	return result
}

func (s *Server) applyCommittedPeerCapacities(peers []cluster.Peer) []cluster.Peer {
	capacities := s.committedPlacementCapacities()
	for index := range peers {
		if capacity, exists := capacities[peers[index].NodeID]; exists {
			peers[index].PlacementCapacity = capacity
		}
	}
	return peers
}

func (s *Server) nodeInfo(w http.ResponseWriter, _ *http.Request) {
	mode := "standalone"
	if s.peerProvider != nil && len(s.peerProvider.Peers()) > 0 {
		mode = "static-discovery"
	}
	if s.staticRouting {
		mode = "static-routing"
	}
	digests, err := s.viewDigests()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "cluster_view_invalid", Message: err.Error()})
		return
	}
	response := map[string]any{"node_id": s.nodeID, "cluster_id": s.clusterID, "advertise_address": s.advertiseAddress, "started_at": s.startedAt, "mode": mode, "metadata_epoch": s.currentMetadataEpoch(), "replication_factor": s.replicationFactor, "placement_capacity": s.currentPlacementCapacity(), "min_protocol_version": cluster.MinClusterProtocolVersion, "protocol_version": cluster.ClusterProtocolVersion, "authentication_required": s.apiKeyHash != nil, "internal_mtls_required": s.requireInternalMTLS, "static_routing": s.staticRouting, "membership_digest": digests.Membership, "catalog_digest": digests.Catalog, "placement_digest": digests.Placement}
	if status, ok := s.raftProtocol.(interface {
		Status() (cluster.RaftRole, string, uint64)
	}); ok {
		role, leaderID, term := status.Status()
		response["raft_role"], response["raft_leader_id"], response["raft_term"] = role, leaderID, term
	}
	if s.raftStore != nil {
		stableVoters, jointOldVoters, jointNewVoters := s.raftStore.VoterConfiguration()
		response["raft_voters"] = stableVoters
		if len(jointOldVoters) != 0 {
			response["raft_joint_old_voters"], response["raft_joint_new_voters"] = jointOldVoters, jointNewVoters
		}
		if committed, exists := s.raftStore.CommittedView(); exists {
			response["committed_membership_digest"], response["committed_catalog_digest"], response["committed_placement_digest"] = committed.Membership, committed.Catalog, committed.Placement
			if committed.CapacityManifest != "" {
				response["committed_capacity_manifest"] = committed.CapacityManifest
			}
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) clusterPeers(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := parsePageQuery(w, r)
	if !ok {
		return
	}
	peers := []cluster.Peer{}
	if s.peerProvider != nil {
		peers = s.peerProvider.Peers()
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].NodeID < peers[j].NodeID })
	start := sort.Search(len(peers), func(i int) bool { return peers[i].NodeID > cursor })
	end := min(len(peers), start+limit)
	next := ""
	if end < len(peers) {
		next = pageCursor(peers[end-1].NodeID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"cluster_id": s.clusterID, "local_node_id": s.nodeID, "metadata_epoch": s.currentMetadataEpoch(), "peers": peers[start:end], "next_cursor": next})
}

func (s *Server) clusterPlacement(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := parsePageQuery(w, r)
	if !ok {
		return
	}
	peers, peerErr := s.membershipPeers()
	if peerErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: peerErr.Error()})
		return
	}
	table, err := cluster.PlanPlacementWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, s.engine.ListCollections())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "placement_unavailable", Message: err.Error()})
		return
	}
	table.Authoritative = s.authoritativePlacementReady()
	key := func(item cluster.ShardPlacement) string {
		return fmt.Sprintf("%s\x00%010d", item.Collection, item.ShardID)
	}
	start := sort.Search(len(table.Shards), func(i int) bool { return key(table.Shards[i]) > cursor })
	end := min(len(table.Shards), start+limit)
	if end < len(table.Shards) {
		table.NextCursor = pageCursor(key(table.Shards[end-1]))
	}
	table.Shards = table.Shards[start:end]
	writeJSON(w, http.StatusOK, table)
}

func (s *Server) clusterReplicaPlacement(w http.ResponseWriter, r *http.Request) {
	factor, err := strconv.Atoi(r.URL.Query().Get("replication_factor"))
	if err != nil || factor < 1 {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_replication_factor", Message: "replication_factor must be a positive integer"})
		return
	}
	peers, peerErr := s.membershipPeers()
	if peerErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: peerErr.Error()})
		return
	}
	table, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, s.engine.ListCollections(), factor)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "replica_placement_unavailable", Message: err.Error()})
		return
	}
	// Replica placement remains advisory until replica WAL catch-up is enforced.
	table.Authoritative = false
	writeJSON(w, http.StatusOK, table)
}

func (s *Server) viewDigests() (cluster.ViewDigests, error) {
	peers, err := s.membershipPeers()
	if err != nil {
		return cluster.ViewDigests{}, err
	}
	return cluster.ComputeViewDigestsWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, s.engine.ListCollections())
}

// membershipPeers excludes discovered learners and removed nodes once Raft has
// a persisted voter configuration. Before Raft membership is configured it
// preserves the static-discovery behavior.
func (s *Server) membershipPeers() ([]cluster.Peer, error) {
	peers := []cluster.Peer{}
	if s.peerProvider != nil {
		peers = append(peers, s.peerProvider.Peers()...)
	}
	if s.raftStore == nil || len(s.raftStore.Voters()) == 0 {
		return s.applyCommittedPeerCapacities(peers), nil
	}
	voters := s.raftStore.Voters()
	wanted := make(map[string]struct{}, len(voters))
	for _, voter := range voters {
		if voter != s.nodeID {
			wanted[voter] = struct{}{}
		}
	}
	result := make([]cluster.Peer, 0, len(wanted))
	for _, peer := range peers {
		if _, exists := wanted[peer.NodeID]; exists {
			result = append(result, peer)
			delete(wanted, peer.NodeID)
		}
	}
	if len(wanted) != 0 {
		return nil, fmt.Errorf("one or more configured Raft voters are not discoverable")
	}
	return s.applyCommittedPeerCapacities(result), nil
}

func (s *Server) clusterReadiness(w http.ResponseWriter, _ *http.Request) {
	local, peers, reasons := s.clusterReadinessState()
	writeJSON(w, http.StatusOK, map[string]any{"ready": len(reasons) == 0, "authoritative": s.authoritativePlacementReady(), "static_routing_enabled": s.staticRouting, "metadata_epoch": s.currentMetadataEpoch(), "digests": local, "peers_checked": len(peers), "reasons": reasons})
}

func (s *Server) staticPlacementReady() bool {
	if !s.staticRouting {
		return false
	}
	_, _, reasons := s.clusterReadinessState()
	return len(reasons) == 0
}

// authoritativePlacementReady distinguishes a converged development view from
// a placement committed by the metadata Raft group. A one-node deployment is
// authoritative without distributed consensus because it has no remote owner.
func (s *Server) authoritativePlacementReady() bool {
	peers, err := s.membershipPeers()
	if err != nil {
		return false
	}
	if len(peers) == 0 && s.replicationFactor == 1 {
		return true
	}
	if !s.staticPlacementReady() || s.raftStore == nil || len(s.raftStore.Voters()) == 0 {
		return false
	}
	local, err := s.viewDigests()
	if err != nil {
		return false
	}
	committed, exists := s.raftStore.CommittedView()
	return exists && committed.Membership == local.Membership && committed.Catalog == local.Catalog && committed.Placement == local.Placement
}

func (s *Server) requireAuthoritativePlacement(w http.ResponseWriter) bool {
	if !s.requireStaticPlacement(w) {
		return false
	}
	if s.authoritativePlacementReady() {
		return true
	}
	writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "authoritative_placement_required", Message: "distributed data routes require placement committed by the metadata Raft group"})
	return false
}

func (s *Server) requireStaticPlacement(w http.ResponseWriter) bool {
	if !s.staticRouting {
		return true
	}
	if s.staticPlacementReady() {
		if err := s.activateLocalOwnership(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "ownership_activation_failed", Message: err.Error()})
			return false
		}
		return true
	}
	writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "cluster_view_not_ready", Message: "static routing requires a converged healthy cluster view"})
	return false
}

func (s *Server) activateLocalOwnership() error {
	s.ownershipMu.Lock()
	defer s.ownershipMu.Unlock()
	if s.ownershipApplied {
		return nil
	}
	collections := s.engine.ListCollections()
	peers, peerErr := s.membershipPeers()
	if peerErr != nil {
		return peerErr
	}
	table, err := cluster.PlanReplicaPlacementWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, collections, s.replicationFactor)
	if err != nil {
		return err
	}
	assignments := make(map[string][]uint32, len(collections))
	for _, config := range collections {
		assignments[config.Name] = []uint32{}
	}
	for _, shard := range table.Shards {
		for _, replicaID := range shard.Replicas {
			if replicaID == s.nodeID {
				assignments[shard.Collection] = append(assignments[shard.Collection], shard.ShardID)
				break
			}
		}
	}
	if err := s.engine.ApplyShardOwnership(assignments); err != nil {
		return err
	}
	s.ownershipApplied = true
	return nil
}

func (s *Server) validateShardOwner(w http.ResponseWriter, collectionName string, shardID uint32) bool {
	if !s.staticRouting {
		return true
	}
	if !s.requireStaticPlacement(w) {
		return false
	}
	config, _, err := s.engine.DescribeCollection(collectionName)
	if err != nil {
		writeError(w, err)
		return false
	}
	peers, peerErr := s.membershipPeers()
	if peerErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: peerErr.Error()})
		return false
	}
	table, err := cluster.PlanPlacementWithCapacity(s.currentMetadataEpoch(), s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, []core.CollectionConfig{config})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "placement_unavailable", Message: err.Error()})
		return false
	}
	for _, assignment := range table.Shards {
		if assignment.ShardID == shardID {
			if assignment.NodeID == s.nodeID {
				return true
			}
			writeJSON(w, http.StatusConflict, apiError{Code: "wrong_owner", Message: fmt.Sprintf("shard %d is assigned to node %s", shardID, assignment.NodeID)})
			return false
		}
	}
	writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_shard", Message: "shard ID out of range"})
	return false
}

func (s *Server) clusterReadinessState() (cluster.ViewDigests, []cluster.Peer, []string) {
	local, err := s.viewDigests()
	reasons := make([]string, 0)
	if err != nil {
		reasons = append(reasons, err.Error())
	}
	if s.raftStore != nil && s.currentMetadataEpoch() > 1 {
		committed, exists := s.raftStore.CommittedView()
		if !exists {
			reasons = append(reasons, "current metadata epoch has no committed view digests")
		} else {
			if committed.Membership != local.Membership {
				reasons = append(reasons, "local membership differs from committed metadata")
			}
			if committed.Catalog != local.Catalog {
				reasons = append(reasons, "local catalog differs from committed metadata")
			}
			if committed.Placement != local.Placement {
				reasons = append(reasons, "local placement differs from committed metadata")
			}
		}
	}
	peers, peerErr := s.membershipPeers()
	if peerErr != nil {
		reasons = append(reasons, peerErr.Error())
	}
	for _, peer := range peers {
		if peer.State != cluster.PeerHealthy {
			reasons = append(reasons, fmt.Sprintf("peer %s is %s", peer.SeedURL, peer.State))
			continue
		}
		if peer.MetadataEpoch != s.currentMetadataEpoch() {
			reasons = append(reasons, fmt.Sprintf("peer %s metadata epoch differs", peer.SeedURL))
		}
		peerFactor := peer.ReplicationFactor
		if peerFactor == 0 {
			peerFactor = 1
		}
		if peerFactor != s.replicationFactor {
			reasons = append(reasons, fmt.Sprintf("peer %s replication factor differs", peer.SeedURL))
		}
		if peer.MembershipDigest != local.Membership {
			reasons = append(reasons, fmt.Sprintf("peer %s membership differs", peer.SeedURL))
		}
		if peer.CatalogDigest != local.Catalog {
			reasons = append(reasons, fmt.Sprintf("peer %s catalog differs", peer.SeedURL))
		}
		if peer.PlacementDigest != local.Placement {
			reasons = append(reasons, fmt.Sprintf("peer %s placement differs", peer.SeedURL))
		}
	}
	return local, peers, reasons
}

func (s *Server) serveMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.metrics.writeTo(w, s)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ready exposes only a probe-safe status. Detailed convergence reasons remain
// on the authenticated cluster readiness endpoint.
func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	if s.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	if s.staticRouting {
		_, _, reasons := s.clusterReadinessState()
		if len(reasons) != 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		if err := s.activateLocalOwnership(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) listCollections(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := parsePageQuery(w, r)
	if !ok {
		return
	}
	collections := s.engine.ListCollections()
	start := sort.Search(len(collections), func(i int) bool { return collections[i].Name > cursor })
	end := min(len(collections), start+limit)
	next := ""
	if end < len(collections) {
		next = pageCursor(collections[end-1].Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": collections[start:end], "next_cursor": next})
}

func (s *Server) createCollection(w http.ResponseWriter, r *http.Request) {
	if s.staticRouting {
		writeJSON(w, http.StatusConflict, apiError{Code: "static_schema_immutable", Message: "create collections before enabling static routing"})
		return
	}
	var config core.CollectionConfig
	if !decode(w, r, &config) {
		return
	}
	if err := s.engine.CreateCollection(config); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, config)
}

func (s *Server) describeCollection(w http.ResponseWriter, r *http.Request) {
	config, count, err := s.engine.DescribeCollection(r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": config, "vector_count": count})
}

func (s *Server) deleteCollection(w http.ResponseWriter, r *http.Request) {
	if s.staticRouting {
		writeJSON(w, http.StatusConflict, apiError{Code: "static_schema_immutable", Message: "collection deletion requires consensus-backed metadata"})
		return
	}
	if err := s.engine.DeleteCollection(r.PathValue("name")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) upsert(w http.ResponseWriter, r *http.Request) {
	if s.rejectStandaloneDataPath(w) {
		return
	}
	var record core.Record
	if !decode(w, r, &record) {
		return
	}
	stored, err := s.engine.Upsert(r.PathValue("name"), record)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stored)
}

type batchUpsertRequest struct {
	Records []core.Record `json:"records"`
}

func (s *Server) batchUpsert(w http.ResponseWriter, r *http.Request) {
	if s.rejectStandaloneDataPath(w) {
		return
	}
	var request batchUpsertRequest
	if !decode(w, r, &request) {
		return
	}
	records, err := s.engine.BatchUpsert(r.PathValue("name"), request.Records)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	if s.rejectStandaloneDataPath(w) {
		return
	}
	record, err := s.engine.Get(r.PathValue("name"), r.URL.Query().Get("namespace"), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if s.rejectStandaloneDataPath(w) {
		return
	}
	if err := s.engine.Delete(r.PathValue("name"), r.URL.Query().Get("namespace"), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type searchRequest struct {
	Vector    []float32      `json:"vector"`
	TopK      int            `json:"top_k"`
	EFSearch  int            `json:"ef_search,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
	Filter    map[string]any `json:"filter,omitempty"`
}

func (s *Server) internalShardSearch(w http.ResponseWriter, r *http.Request) {
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
	var request searchRequest
	if !decode(w, r, &request) {
		return
	}
	filter, err := metadata.Parse(request.Filter)
	if err != nil {
		writeError(w, err)
		return
	}
	results, err := s.engine.SearchShardFilteredWithEF(r.PathValue("collection"), uint32(shardID), request.Namespace, request.Vector, request.TopK, filter, request.EFSearch)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "shard_id": shardID, "metadata_epoch": s.currentMetadataEpoch()})
}

func (s *Server) validateInternalFence(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("X-GideonDB-Metadata-Epoch", strconv.FormatUint(s.currentMetadataEpoch(), 10))
	epoch, err := strconv.ParseUint(r.Header.Get("X-GideonDB-Metadata-Epoch"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_epoch", Message: "X-GideonDB-Metadata-Epoch must be an unsigned integer"})
		return false
	}
	if err := cluster.ValidateFence(s.clusterID, s.nodeID, s.currentMetadataEpoch(), r.Header.Get("X-GideonDB-Cluster-ID"), r.Header.Get("X-GideonDB-Target-Node-ID"), epoch); err != nil {
		fence := err.(*cluster.FenceError)
		status := http.StatusConflict
		if fence.Kind == cluster.FenceFutureEpoch {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, apiError{Code: string(fence.Kind), Message: fence.Error()})
		return false
	}
	return true
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	if s.rejectStandaloneDataPath(w) {
		return
	}
	var request searchRequest
	if !decode(w, r, &request) {
		return
	}
	filter, err := metadata.Parse(request.Filter)
	if err != nil {
		writeError(w, err)
		return
	}
	results, err := s.engine.SearchFilteredWithEF(r.PathValue("name"), request.Namespace, request.Vector, request.TopK, filter, request.EFSearch)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) rejectStandaloneDataPath(w http.ResponseWriter) bool {
	if !s.staticRouting {
		return false
	}
	writeJSON(w, http.StatusConflict, apiError{Code: "static_routing_required", Message: "use the placement-aware cluster endpoint while static routing is enabled"})
	return true
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_json", Message: err.Error()})
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_json", Message: "request must contain one JSON value"})
		return false
	}
	return true
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, core.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, core.ErrAlreadyExists):
		status, code = http.StatusConflict, "already_exists"
	case errors.Is(err, core.ErrShardNotOwned):
		status, code = http.StatusConflict, "shard_not_owned"
	case errors.Is(err, core.ErrReplicationGap):
		status, code = http.StatusConflict, "replication_gap"
	case errors.Is(err, core.ErrReplicationConflict):
		status, code = http.StatusConflict, "replication_conflict"
	case errors.Is(err, core.ErrReplicationCompacted):
		status, code = http.StatusGone, "replication_sequence_compacted"
	case errors.Is(err, core.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "idempotency_conflict"
	case errors.Is(err, core.ErrInvalidArgument), errors.Is(err, core.ErrDimensionMismatch):
		status, code = http.StatusBadRequest, "invalid_argument"
	}
	message := err.Error()
	if status == http.StatusInternalServerError {
		message = "internal server error"
	}
	writeJSON(w, status, apiError{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func formatUint(value uint64) string { return strconv.FormatUint(value, 10) }

// ParseAddress validates an address supplied by configuration.
func ParseAddress(value string) (string, error) {
	parts := strings.Split(value, ":")
	if len(parts) < 2 {
		return "", fmt.Errorf("address must include a port")
	}
	port, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid port")
	}
	return value, nil
}
