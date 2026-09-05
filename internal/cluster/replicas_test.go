package cluster

import (
	"reflect"
	"testing"

	"github.com/vectordb/vectordb/internal/core"
)

func TestReplicaPlacementIsDeterministicDistinctAndBalanced(t *testing.T) {
	local := "11111111111111111111111111111111"
	peers := []Peer{
		{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", Healthy: true},
		{NodeID: "33333333333333333333333333333333", AdvertiseAddress: "node-c:6333", Healthy: true},
	}
	collections := []core.CollectionConfig{{Name: "vectors", Dimension: 2, Metric: core.MetricDot, ShardCount: 48}}
	first, err := PlanReplicaPlacement(7, local, "node-a:6333", peers, collections, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanReplicaPlacement(7, local, "node-a:6333", []Peer{peers[1], peers[0]}, collections, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("replica placement depends on discovery order")
	}
	leaders := make(map[string]int)
	replicas := make(map[string]int)
	for _, shard := range first.Shards {
		if len(shard.Replicas) != 2 || shard.LeaderID != shard.Replicas[0] || shard.Replicas[0] == shard.Replicas[1] {
			t.Fatalf("invalid shard placement: %#v", shard)
		}
		leaders[shard.LeaderID]++
		for _, nodeID := range shard.Replicas {
			replicas[nodeID]++
		}
	}
	if len(leaders) != 3 || len(replicas) != 3 {
		t.Fatalf("leaders=%v replicas=%v", leaders, replicas)
	}
}

func TestReplicaPlacementRejectsImpossibleFactor(t *testing.T) {
	config := []core.CollectionConfig{{Name: "vectors", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}}
	if _, err := PlanReplicaPlacement(1, "11111111111111111111111111111111", "node-a:6333", nil, config, 2); err == nil {
		t.Fatal("accepted replication factor larger than node count")
	}
	if _, err := PlanReplicaPlacement(1, "11111111111111111111111111111111", "node-a:6333", nil, config, 0); err == nil {
		t.Fatal("accepted zero replication factor")
	}
}

func TestReplicaPlacementHonorsDeterministicCapacityWeights(t *testing.T) {
	local := "11111111111111111111111111111111"
	remote := Peer{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", PlacementCapacity: 3}
	collections := []core.CollectionConfig{{Name: "weighted", Dimension: 2, Metric: core.MetricDot, ShardCount: 4096}}

	first, err := PlanReplicaPlacementWithCapacity(7, local, "node-a:6333", 1, []Peer{remote}, collections, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanReplicaPlacementWithCapacity(7, local, "node-a:6333", 1, []Peer{remote}, collections, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("capacity-aware placement is not deterministic")
	}
	owners := map[string]int{}
	for _, shard := range first.Shards {
		owners[shard.LeaderID]++
	}
	ratio := float64(owners[remote.NodeID]) / float64(owners[local])
	if ratio < 2.5 || ratio > 3.5 {
		t.Fatalf("ownership ratio=%f counts=%v, want approximately 3:1", ratio, owners)
	}
}

func TestPlacementRejectsCapacityOutsideBound(t *testing.T) {
	config := []core.CollectionConfig{{Name: "vectors", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}}
	local := "11111111111111111111111111111111"
	if _, err := PlanReplicaPlacementWithCapacity(1, local, "node-a:6333", 0, nil, config, 1); err == nil {
		t.Fatal("accepted zero local placement capacity")
	}
	peer := Peer{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", PlacementCapacity: MaxPlacementCapacity + 1}
	if _, err := PlanReplicaPlacementWithCapacity(1, local, "node-a:6333", 1, []Peer{peer}, config, 1); err == nil {
		t.Fatal("accepted excessive peer placement capacity")
	}
}
