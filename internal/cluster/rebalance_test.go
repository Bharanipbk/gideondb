package cluster

import (
	"reflect"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestPlanRebalanceIsDeterministicAndOrdersSafeCutover(t *testing.T) {
	const localID = "11111111111111111111111111111111"
	peers := []Peer{
		{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", Healthy: true},
		{NodeID: "33333333333333333333333333333333", AdvertiseAddress: "node-c:6333", Healthy: true},
	}
	collections := []core.CollectionConfig{{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 32}}
	current, err := PlanReplicaPlacement(7, localID, "node-a:6333", peers, collections, 2)
	if err != nil {
		t.Fatal(err)
	}
	joined := append(append([]Peer(nil), peers...), Peer{NodeID: "44444444444444444444444444444444", AdvertiseAddress: "node-d:6333", Healthy: true})
	target, err := PlanReplicaPlacement(8, localID, "node-a:6333", joined, collections, 2)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanRebalance(current, target)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Authoritative || len(plan.Movements) == 0 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	for _, movement := range plan.Movements {
		positions := make(map[string]int, len(movement.Actions))
		for position, action := range movement.Actions {
			positions[action.ID] = position
		}
		for position, action := range movement.Actions {
			for _, requirement := range action.Requires {
				if requiredPosition, exists := positions[requirement]; !exists || requiredPosition >= position {
					t.Fatalf("action %s has unsafe prerequisite %s", action.ID, requirement)
				}
			}
		}
	}
	joined[0], joined[2] = joined[2], joined[0]
	reorderedTarget, err := PlanReplicaPlacement(8, localID, "node-a:6333", joined, collections, 2)
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := PlanRebalance(current, reorderedTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, reordered) {
		t.Fatal("rebalance plan changed with peer discovery order")
	}
}

func TestPlanRebalanceRejectsUnsafeShapes(t *testing.T) {
	base := ReplicaPlacementTable{Epoch: 1, ReplicationFactor: 1, Shards: []ShardReplicaPlacement{{Collection: "docs", ShardID: 0, LeaderID: "a", Replicas: []string{"a"}}}}
	for name, target := range map[string]ReplicaPlacementTable{
		"same epoch":       {Epoch: 1, ReplicationFactor: 1, Shards: base.Shards},
		"factor change":    {Epoch: 2, ReplicationFactor: 2, Shards: base.Shards},
		"missing shard":    {Epoch: 2, ReplicationFactor: 1},
		"unexpected shard": {Epoch: 2, ReplicationFactor: 1, Shards: []ShardReplicaPlacement{{Collection: "docs", ShardID: 1, LeaderID: "a", Replicas: []string{"a"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PlanRebalance(base, target); err == nil {
				t.Fatal("expected invalid plan rejection")
			}
		})
	}
}

func TestCapacityRebalanceIsDeterministicAndKeepsMembership(t *testing.T) {
	localID := "11111111111111111111111111111111"
	peer := Peer{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", PlacementCapacity: 1}
	collections := []core.CollectionConfig{{Name: "vectors", Dimension: 2, Metric: core.MetricDot, ShardCount: 256}}
	current, target, plan, err := PlanCapacityRebalance(7, localID, "node-a:6333", 1, []Peer{peer}, collections, 1, peer.NodeID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if current.Epoch != 7 || target.Epoch != 8 || plan.FromEpoch != 7 || plan.ToEpoch != 8 || len(plan.Movements) == 0 {
		t.Fatalf("unexpected capacity transition: current=%#v target=%#v plan=%#v", current, target, plan)
	}
	if len(current.Nodes) != len(target.Nodes) || target.Nodes[1].Capacity != 3 {
		t.Fatalf("capacity transition changed membership or omitted target weight: %#v", target.Nodes)
	}
	_, repeatedTarget, repeatedPlan, err := PlanCapacityRebalance(7, localID, "node-a:6333", 1, []Peer{peer}, collections, 1, peer.NodeID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(target, repeatedTarget) || !reflect.DeepEqual(plan, repeatedPlan) {
		t.Fatal("capacity transition depends on invocation state")
	}
}

func TestCapacityRebalanceRejectsNoopAndUnknownNode(t *testing.T) {
	localID := "11111111111111111111111111111111"
	peer := Peer{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", PlacementCapacity: 1}
	collections := []core.CollectionConfig{{Name: "vectors", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}}
	if _, _, _, err := PlanCapacityRebalance(7, localID, "node-a:6333", 1, []Peer{peer}, collections, 1, peer.NodeID, 1); err == nil {
		t.Fatal("accepted no-op capacity transition")
	}
	if _, _, _, err := PlanCapacityRebalance(7, localID, "node-a:6333", 1, []Peer{peer}, collections, 1, "33333333333333333333333333333333", 2); err == nil {
		t.Fatal("accepted unknown capacity target")
	}
}
