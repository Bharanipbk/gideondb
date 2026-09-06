package rest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

func TestStaticOwnershipMaterializesLeaderAndFollowerReplicas(t *testing.T) {
	const localNode = "11111111111111111111111111111111"
	peers := staticPeerProvider{
		{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", Healthy: true},
		{NodeID: "33333333333333333333333333333333", AdvertiseAddress: "node-c:6333", Healthy: true},
	}
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 12}
	dataPath := t.TempDir()
	db, err := engine.Open(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	server := NewWithOptions(db, nil, Options{NodeID: localNode, ClusterID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AdvertiseAddress: "node-a:6333", MetadataEpoch: 1, ReplicationFactor: 2, PeerProvider: peers, EnableStaticRouting: true})
	if err := server.activateLocalOwnership(); err != nil {
		t.Fatal(err)
	}
	table, err := cluster.PlanReplicaPlacement(1, localNode, "node-a:6333", []cluster.Peer(peers), []core.CollectionConfig{config}, 2)
	if err != nil {
		t.Fatal(err)
	}
	var expected []uint32
	for _, shard := range table.Shards {
		for _, replica := range shard.Replicas {
			if replica == localNode {
				expected = append(expected, shard.ShardID)
			}
		}
	}
	var manifest struct {
		Collections map[string][]uint32 `json:"collections"`
	}
	payload, err := os.ReadFile(filepath.Join(dataPath, engine.OwnershipFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	sort.Slice(expected, func(i, j int) bool { return expected[i] < expected[j] })
	if !reflect.DeepEqual(manifest.Collections[config.Name], expected) {
		t.Fatalf("owned shards=%v want=%v", manifest.Collections[config.Name], expected)
	}
}
