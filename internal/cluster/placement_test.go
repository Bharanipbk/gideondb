package cluster

import (
	"reflect"
	"testing"

	"github.com/vectordb/vectordb/internal/core"
)

func TestPlacementIsDeterministicAndKeepsUnhealthyKnownNodes(t *testing.T) {
	collections := []core.CollectionConfig{{Name: "docs", Dimension: 3, Metric: core.MetricCosine, ShardCount: 64}}
	peers := []Peer{
		{NodeID: remoteTestNode, AdvertiseAddress: "node-b:6333", Healthy: true},
		{NodeID: clusterTestID, AdvertiseAddress: "node-c:6333", Healthy: true},
	}
	first, err := PlanPlacement(7, localTestNode, "node-a:6333", peers, collections)
	if err != nil {
		t.Fatal(err)
	}
	reversed := []Peer{peers[1], peers[0]}
	second, err := PlanPlacement(7, localTestNode, "node-a:6333", reversed, collections)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("placement depends on peer input order")
	}
	owners := make(map[string]int)
	for _, shard := range first.Shards {
		owners[shard.NodeID]++
	}
	if len(owners) != 3 {
		t.Fatalf("expected shards distributed across three nodes: %#v", owners)
	}

	peers[0].Healthy = false
	unhealthy, err := PlanPlacement(7, localTestNode, "node-a:6333", peers, collections)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Shards, unhealthy.Shards) {
		t.Fatal("temporary peer health changed ownership")
	}
}

func TestPlacementAdditionHasRendezvousMinimalMovement(t *testing.T) {
	collection := []core.CollectionConfig{{Name: "docs", Dimension: 1, Metric: core.MetricDot, ShardCount: 128}}
	before, err := PlanPlacement(1, localTestNode, "node-a:6333", []Peer{{NodeID: remoteTestNode, AdvertiseAddress: "node-b:6333"}}, collection)
	if err != nil {
		t.Fatal(err)
	}
	after, err := PlanPlacement(2, localTestNode, "node-a:6333", []Peer{{NodeID: remoteTestNode, AdvertiseAddress: "node-b:6333"}, {NodeID: clusterTestID, AdvertiseAddress: "node-c:6333"}}, collection)
	if err != nil {
		t.Fatal(err)
	}
	moved := 0
	for i := range before.Shards {
		if before.Shards[i].NodeID != after.Shards[i].NodeID {
			moved++
			if after.Shards[i].NodeID != clusterTestID {
				t.Fatal("existing owners moved between old nodes")
			}
		}
	}
	if moved == 0 || moved == len(before.Shards) {
		t.Fatalf("unexpected movement count %d", moved)
	}
}

func TestPlacementRejectsInvalidOrDuplicateNodes(t *testing.T) {
	collection := []core.CollectionConfig{{Name: "docs", Dimension: 1, Metric: core.MetricDot, ShardCount: 1}}
	if _, err := PlanPlacement(0, localTestNode, "node-a:6333", nil, collection); err == nil {
		t.Fatal("expected zero epoch rejection")
	}
	if _, err := PlanPlacement(1, localTestNode, "node-a:6333", []Peer{{NodeID: localTestNode, AdvertiseAddress: "duplicate:6333"}}, collection); err == nil {
		t.Fatal("expected duplicate rejection")
	}
}
