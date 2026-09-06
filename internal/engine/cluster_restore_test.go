package engine

import (
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
)

func TestClusterRestorePlanSelectsNewestReplicaAndRemapsTopology(t *testing.T) {
	oldA := "11111111111111111111111111111111"
	oldB := "22222222222222222222222222222222"
	newA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	points := []NodeRecoveryPoint{
		{MetadataEpoch: 7, NodeID: oldA, PlacementDigest: recoveryDigest('a'), Shards: []ShardRecoveryPoint{{Collection: "docs", ShardID: 0, NodeID: oldA, Sequence: 4}, {Collection: "docs", ShardID: 1, NodeID: oldA, Sequence: 9}}},
		{MetadataEpoch: 7, NodeID: oldB, PlacementDigest: recoveryDigest('a'), Shards: []ShardRecoveryPoint{{Collection: "docs", ShardID: 0, NodeID: oldB, Sequence: 6}, {Collection: "docs", ShardID: 1, NodeID: oldB, Sequence: 9}}},
	}
	manifest, err := MergeRecoveryPoints(points)
	if err != nil {
		t.Fatal(err)
	}
	target := cluster.ReplicaPlacementTable{Epoch: 1, ReplicationFactor: 2, Shards: []cluster.ShardReplicaPlacement{
		{Collection: "docs", ShardID: 1, LeaderID: newB, Replicas: []string{newB, newA}},
		{Collection: "docs", ShardID: 0, LeaderID: newA, Replicas: []string{newA, newB}},
	}}
	plan, err := PlanClusterRestore(manifest, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Assignments) != 2 || plan.Assignments[0].SourceNodeID != oldB || plan.Assignments[0].SourceSequence != 6 || plan.Assignments[0].TargetLeaderID != newA {
		t.Fatalf("unexpected shard-zero assignment: %#v", plan.Assignments)
	}
	if plan.Assignments[1].SourceNodeID != oldA || plan.Assignments[1].SourceSequence != 9 || plan.Assignments[1].TargetLeaderID != newB {
		t.Fatalf("tie-break or remap failed: %#v", plan.Assignments[1])
	}
	missing := target
	missing.Shards = append(missing.Shards, cluster.ShardReplicaPlacement{Collection: "docs", ShardID: 2, LeaderID: newA, Replicas: []string{newA, newB}})
	if _, err := PlanClusterRestore(manifest, missing); err == nil {
		t.Fatal("accepted target shard without a backup replica")
	}
}

func recoveryDigest(character byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = character
	}
	return string(value)
}
