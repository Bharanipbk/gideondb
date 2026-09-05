package rest

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vectordb/vectordb/internal/cluster"
	"github.com/vectordb/vectordb/internal/core"
	"github.com/vectordb/vectordb/internal/engine"
)

type staticPeerProvider []cluster.Peer

func (peers staticPeerProvider) Peers() []cluster.Peer { return peers }

func TestBearerAuthenticationProtectsDataAndMetricsButNotHealth(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := NewWithOptions(db, nil, Options{APIKey: "0123456789abcdef"}).Handler()
	for _, target := range []string{"/v1/collections", "/metrics"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("%s status=%d headers=%v", target, response.Code, response.Header())
		}
		request = httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer 0123456789abcdef")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("authorized %s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	internalRequest := httptest.NewRequest(http.MethodPost, "/v1/internal/shards/docs/0/search", strings.NewReader(`{"vector":[1],"top_k":1}`))
	internalResponse := httptest.NewRecorder()
	handler.ServeHTTP(internalResponse, internalRequest)
	if internalResponse.Code != http.StatusUnauthorized {
		t.Fatalf("internal search status=%d", internalResponse.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health status=%d", response.Code)
	}
	for name, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store"} {
		if got := response.Header().Get(name); got != want {
			t.Fatalf("%s=%q, want %q", name, got, want)
		}
	}
}

func TestServerRestartRestoresCommittedPlacementCapacity(t *testing.T) {
	const nodeID = "11111111111111111111111111111111"
	path := t.TempDir()
	store, err := cluster.OpenRaftStore(path, nodeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ConfigureVoters([]string{nodeID}); err != nil {
		t.Fatal(err)
	}
	vote, err := store.RequestVote(cluster.RequestVoteRequest{Term: 1, CandidateID: nodeID})
	if err != nil || !vote.VoteGranted {
		t.Fatalf("vote=%#v err=%v", vote, err)
	}
	manifest := `[{"node_id":"11111111111111111111111111111111","capacity":4}]`
	command := cluster.MetadataCommand{Type: "advance_epoch", Epoch: 2, MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64), CapacityManifest: manifest}
	index, err := store.AppendCommand(1, command)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitWithVoterAcks(index, 1, []string{nodeID}); err != nil {
		t.Fatal(err)
	}
	reopened, err := cluster.OpenRaftStore(path, nodeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := NewWithOptions(db, nil, Options{NodeID: nodeID, ClusterID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AdvertiseAddress: "node-a:6333", PlacementCapacity: 1, RaftStore: reopened})
	if capacity := server.currentPlacementCapacity(); capacity != 4 {
		t.Fatalf("restored capacity=%d, want 4", capacity)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/node", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"placement_capacity":4`) || !strings.Contains(response.Body.String(), "committed_capacity_manifest") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLoadAPIKeyFilePermissionsAndLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte("0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := LoadAPIKeyFile(path)
	if err != nil || key != "0123456789abcdef" {
		t.Fatalf("key=%q err=%v", key, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAPIKeyFile(path); err == nil {
		t.Fatal("expected permissive file mode rejection")
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAPIKeyFile(path); err != nil {
		t.Fatalf("process-group-readable secret rejected: %v", err)
	}
}

func TestLoopbackAddressDetection(t *testing.T) {
	for _, address := range []string{"127.0.0.1:6333", "[::1]:6333", "localhost:6333"} {
		if !IsLoopbackAddress(address) {
			t.Fatalf("%q should be loopback", address)
		}
	}
	for _, address := range []string{"0.0.0.0:6333", "[::]:6333", "10.0.0.1:6333", "invalid"} {
		if IsLoopbackAddress(address) {
			t.Fatalf("%q should not be loopback", address)
		}
	}
}

func TestNodeInfoRequiresAuthentication(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := NewWithOptions(db, nil, Options{APIKey: "0123456789abcdef", NodeID: "00112233445566778899aabbccddeeff", AdvertiseAddress: "node-a:6333", PlacementCapacity: 4}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/v1/node", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated node info status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/v1/node", nil)
	request.Header.Set("Authorization", "Bearer 0123456789abcdef")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "00112233445566778899aabbccddeeff") || !strings.Contains(response.Body.String(), `"placement_capacity":4`) || !strings.Contains(response.Body.String(), `"min_protocol_version":1`) || !strings.Contains(response.Body.String(), `"protocol_version":2`) {
		t.Fatalf("node info status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProbeEndpointsRemainUnauthenticated(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := NewWithOptions(db, nil, Options{APIKey: "0123456789abcdef"}).Handler()
	for _, target := range []string{"/v1/health", "/v1/ready"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
}

func TestDrainRemovesReadinessAndRejectsNewTraffic(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := New(db, nil)
	server.BeginDrain()
	for target, status := range map[string]int{"/v1/health": http.StatusOK, "/v1/ready": http.StatusServiceUnavailable, "/v1/collections": http.StatusServiceUnavailable} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != status {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
}

func TestInternalRoutesRequireVerifiedClientCertificateWhenEnabled(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := NewWithOptions(db, nil, Options{RequireInternalMTLS: true}).Handler()
	request := httptest.NewRequest(http.MethodPost, "/v1/internal/unknown", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "mutual_tls_required") {
		t.Fatalf("unverified internal request status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/internal/unknown", strings.NewReader(`{}`))
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code == http.StatusUnauthorized && strings.Contains(response.Body.String(), "mutual_tls_required") {
		t.Fatalf("verified internal request rejected: %s", response.Body.String())
	}
}

func TestClusterPeersAndMetricsExposeMembershipHealth(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := staticPeerProvider{{SeedURL: "http://peer-a:6333", NodeID: "11223344556677889900aabbccddeeff", AdvertiseAddress: "node-b:6333", Healthy: true}}
	if err := db.CreateCollection(core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 4}); err != nil {
		t.Fatal(err)
	}
	handler := NewWithOptions(db, nil, Options{NodeID: "00112233445566778899aabbccddeeff", AdvertiseAddress: "node-a:6333", MetadataEpoch: 3, PeerProvider: provider}).Handler()
	for target, fragment := range map[string]string{
		"/v1/cluster/peers":     `"healthy":true`,
		"/v1/cluster/placement": `"authoritative":false`,
		"/metrics":              `vectordb_cluster_peer_healthy{seed="http://peer-a:6333",node_id="11223344556677889900aabbccddeeff"} 1`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
}

func TestClusterReadinessRequiresConvergedHealthyViews(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 4}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	const localNode = "00112233445566778899aabbccddeeff"
	const remoteNode = "11223344556677889900aabbccddeeff"
	const clusterID = "22112233445566778899aabbccddeeff"
	provider := staticPeerProvider{{SeedURL: "http://peer-b:6333", NodeID: remoteNode, AdvertiseAddress: "node-b:6333", Healthy: true, State: cluster.PeerHealthy}}
	digests, err := cluster.ComputeViewDigests(3, localNode, "node-a:6333", []cluster.Peer(provider), []core.CollectionConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	provider[0].MetadataEpoch = 3
	provider[0].MembershipDigest, provider[0].CatalogDigest, provider[0].PlacementDigest = digests.Membership, digests.Catalog, digests.Placement
	handler := NewWithOptions(db, nil, Options{NodeID: localNode, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 3, PeerProvider: provider, EnableStaticRouting: true}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/cluster/readiness", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":true`) || !strings.Contains(response.Body.String(), `"authoritative":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/cluster/placement", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"authoritative":true`) {
		t.Fatalf("placement status=%d body=%s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/collections/docs/vectors", strings.NewReader(`{"id":"bypass","vector":[1,0]}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "static_routing_required") {
		t.Fatalf("bypass status=%d body=%s", response.Code, response.Body.String())
	}
	plan, err := cluster.PlanPlacement(3, localNode, "node-a:6333", []cluster.Peer(provider), []core.CollectionConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	remoteShard := uint32(config.ShardCount)
	for _, assignment := range plan.Shards {
		if assignment.NodeID == remoteNode {
			remoteShard = assignment.ShardID
			break
		}
	}
	if remoteShard == uint32(config.ShardCount) {
		t.Fatal("test placement has no remote-owned shard")
	}
	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/internal/shards/docs/%d/search", remoteShard), strings.NewReader(`{"vector":[1,0],"top_k":1}`))
	request.Header.Set("X-VectorDB-Cluster-ID", clusterID)
	request.Header.Set("X-VectorDB-Target-Node-ID", localNode)
	request.Header.Set("X-VectorDB-Metadata-Epoch", "3")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "wrong_owner") {
		t.Fatalf("wrong-owner status=%d body=%s", response.Code, response.Body.String())
	}
	provider[0].CatalogDigest = "different"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/cluster/readiness", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":false`) || !strings.Contains(response.Body.String(), "catalog differs") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/cluster/collections/docs/search", strings.NewReader(`{"vector":[1,0],"top_k":1}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "cluster_view_not_ready") {
		t.Fatalf("unready search status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestClusterReadinessRejectsReplicationFactorMismatch(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const localNode = "00112233445566778899aabbccddeeff"
	provider := staticPeerProvider{{SeedURL: "http://peer-b:6333", NodeID: "11223344556677889900aabbccddeeff", AdvertiseAddress: "node-b:6333", Healthy: true, State: cluster.PeerHealthy, MetadataEpoch: 1, ReplicationFactor: 1}}
	digests, err := cluster.ComputeViewDigests(1, localNode, "node-a:6333", []cluster.Peer(provider), nil)
	if err != nil {
		t.Fatal(err)
	}
	provider[0].MembershipDigest, provider[0].CatalogDigest, provider[0].PlacementDigest = digests.Membership, digests.Catalog, digests.Placement
	handler := NewWithOptions(db, nil, Options{NodeID: localNode, ClusterID: "22112233445566778899aabbccddeeff", AdvertiseAddress: "node-a:6333", MetadataEpoch: 1, ReplicationFactor: 2, PeerProvider: provider, EnableStaticRouting: true}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/cluster/readiness", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":false`) || !strings.Contains(response.Body.String(), "replication factor differs") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestInternalShardSearchRequiresExactFence(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 4}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert("docs", core.Record{ID: "one", Vector: []float32{1, 0}}); err != nil {
		t.Fatal(err)
	}
	const nodeID = "00112233445566778899aabbccddeeff"
	const clusterID = "11223344556677889900aabbccddeeff"
	handler := NewWithOptions(db, nil, Options{NodeID: nodeID, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 7}).Handler()
	call := func(shard int, requestCluster, target, epoch string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/internal/shards/docs/%d/search", shard), strings.NewReader(`{"vector":[1,0],"top_k":10}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-VectorDB-Cluster-ID", requestCluster)
		request.Header.Set("X-VectorDB-Target-Node-ID", target)
		request.Header.Set("X-VectorDB-Metadata-Epoch", epoch)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for _, test := range []struct {
		cluster, node, epoch string
		status               int
		code                 string
	}{
		{clusterID, nodeID, "6", http.StatusConflict, "stale_epoch"},
		{clusterID, nodeID, "8", http.StatusServiceUnavailable, "future_epoch"},
		{"22112233445566778899aabbccddeeff", nodeID, "7", http.StatusConflict, "cluster_mismatch"},
		{clusterID, "22112233445566778899aabbccddeeff", "7", http.StatusConflict, "wrong_node"},
	} {
		response := call(0, test.cluster, test.node, test.epoch)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	found := false
	for shardID := 0; shardID < config.ShardCount; shardID++ {
		response := call(shardID, clusterID, nodeID, "7")
		if response.Code != http.StatusOK {
			t.Fatalf("shard %d status=%d body=%s", shardID, response.Code, response.Body.String())
		}
		found = found || strings.Contains(response.Body.String(), `"id":"one"`)
	}
	if !found {
		t.Fatal("record not found through fenced shard searches")
	}
	writeID := ""
	for candidate := 0; writeID == ""; candidate++ {
		value := fmt.Sprintf("internal-%d", candidate)
		shardID, routeErr := db.RouteShard("docs", "", value)
		if routeErr != nil {
			t.Fatal(routeErr)
		}
		if shardID == 0 {
			writeID = value
		}
	}
	body := fmt.Sprintf(`{"records":[{"id":%q,"vector":[0,1]}]}`, writeID)
	writeRequest := httptest.NewRequest(http.MethodPost, "/v1/internal/shards/docs/0/vectors/batch", strings.NewReader(body))
	writeRequest.Header.Set("Content-Type", "application/json")
	writeRequest.Header.Set("X-VectorDB-Cluster-ID", clusterID)
	writeRequest.Header.Set("X-VectorDB-Target-Node-ID", nodeID)
	writeRequest.Header.Set("X-VectorDB-Metadata-Epoch", "7")
	writeResponse := httptest.NewRecorder()
	handler.ServeHTTP(writeResponse, writeRequest)
	if writeResponse.Code != http.StatusOK {
		t.Fatalf("internal write status=%d body=%s", writeResponse.Code, writeResponse.Body.String())
	}
	if _, err := db.Get("docs", "", writeID); err != nil {
		t.Fatalf("internal write missing: %v", err)
	}
}
