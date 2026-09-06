package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

func TestReplicaPlacementPreviewEndpoint(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateCollection(core.CollectionConfig{Name: "vectors", Dimension: 2, Metric: core.MetricDot, ShardCount: 8}); err != nil {
		t.Fatal(err)
	}
	provider := staticPeerProvider{
		{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", Healthy: true},
		{NodeID: "33333333333333333333333333333333", AdvertiseAddress: "node-c:6333", Healthy: true},
	}
	handler := NewWithOptions(db, nil, Options{NodeID: "11111111111111111111111111111111", ClusterID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AdvertiseAddress: "node-a:6333", MetadataEpoch: 1, PeerProvider: provider}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/v1/cluster/replicas?replication_factor=2", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var table cluster.ReplicaPlacementTable
	if err := json.Unmarshal(response.Body.Bytes(), &table); err != nil {
		t.Fatal(err)
	}
	if table.Authoritative || table.ReplicationFactor != 2 || len(table.Shards) != 8 {
		t.Fatalf("table=%#v", table)
	}
	for _, shard := range table.Shards {
		if len(shard.Replicas) != 2 || shard.LeaderID != shard.Replicas[0] || shard.Replicas[0] == shard.Replicas[1] {
			t.Fatalf("invalid shard=%#v", shard)
		}
	}
	bad := httptest.NewRequest(http.MethodGet, "/v1/cluster/replicas?replication_factor=4", nil)
	badResponse := httptest.NewRecorder()
	handler.ServeHTTP(badResponse, bad)
	if badResponse.Code != http.StatusBadRequest {
		t.Fatalf("bad factor status=%d body=%s", badResponse.Code, badResponse.Body.String())
	}
}
