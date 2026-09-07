package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

func committedTestRaftStore(t *testing.T, nodeID string, voters []string, epoch uint64, view cluster.ViewDigests) *cluster.RaftStore {
	t.Helper()
	if epoch < 2 {
		t.Fatal("committed test epoch must be at least 2")
	}
	store, err := cluster.OpenRaftStore(t.TempDir(), nodeID, epoch-1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ConfigureVoters(voters); err != nil {
		t.Fatal(err)
	}
	vote, err := store.BeginElection()
	if err != nil {
		t.Fatal(err)
	}
	index, err := store.AppendCommand(vote.Term, cluster.MetadataCommand{Type: "advance_epoch", Epoch: epoch, MembershipDigest: view.Membership, CatalogDigest: view.Catalog, PlacementDigest: view.Placement, CapacityManifest: view.CapacityManifest})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitWithQuorum(index, vote.Term, len(voters), len(voters)); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestDistributedDataRoutesRequireCommittedPlacement(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	config := core.CollectionConfig{Name: "authority", Dimension: 2, Metric: core.MetricDot, ShardCount: 4}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	const localNode = "11111111111111111111111111111111"
	const remoteNode = "22222222222222222222222222222222"
	peer := cluster.Peer{SeedURL: "http://node-b:6333", NodeID: remoteNode, AdvertiseAddress: "node-b:6333", Healthy: true, State: cluster.PeerHealthy}
	view, err := cluster.ComputeViewDigests(3, localNode, "node-a:6333", []cluster.Peer{peer}, []core.CollectionConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	peer.MetadataEpoch = 3
	peer.MembershipDigest, peer.CatalogDigest, peer.PlacementDigest = view.Membership, view.Catalog, view.Placement
	handler := NewWithOptions(db, nil, Options{NodeID: localNode, ClusterID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AdvertiseAddress: "node-a:6333", MetadataEpoch: 3, PeerProvider: staticPeerProvider{peer}, EnableStaticRouting: true}).Handler()

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/v1/cluster/collections/authority/search", `{"vector":[1,0],"top_k":1}`},
		{http.MethodGet, "/v1/cluster/collections/authority/vectors", ""},
		{http.MethodPost, "/v1/cluster/collections/authority/vectors/batch", `{"records":[{"id":"one","vector":[1,0]}]}`},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "authoritative_placement_required") {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}
