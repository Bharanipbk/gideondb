package cluster

import (
	"strings"
	"testing"
)

func TestRebalanceWriteBarrierSurvivesRestartAndReleasesAfterCommit(t *testing.T) {
	path := t.TempDir()
	store, err := OpenRebalanceBarrierStore(path)
	if err != nil {
		t.Fatal(err)
	}
	barrier := RebalanceWriteBarrier{PlanDigest: strings.Repeat("a", 64), Collection: "docs", ShardID: 7, TargetEpoch: 9}
	if err := store.Freeze(barrier); err != nil {
		t.Fatal(err)
	}
	if err := store.Freeze(barrier); err != nil {
		t.Fatalf("idempotent freeze: %v", err)
	}
	restarted, err := OpenRebalanceBarrierStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, frozen := restarted.IsFrozen("docs", 7); !frozen || got != barrier {
		t.Fatalf("barrier=%#v frozen=%v", got, frozen)
	}
	if err := restarted.ReleaseCommitted(8); err != nil {
		t.Fatal(err)
	}
	if _, frozen := restarted.IsFrozen("docs", 7); !frozen {
		t.Fatal("barrier released before target epoch")
	}
	if err := restarted.ReleaseCommitted(9); err != nil {
		t.Fatal(err)
	}
	if _, frozen := restarted.IsFrozen("docs", 7); frozen {
		t.Fatal("barrier remained after committed target epoch")
	}
}

func TestRebalanceWriteBarrierRejectsConflictingPlan(t *testing.T) {
	store, err := OpenRebalanceBarrierStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := RebalanceWriteBarrier{PlanDigest: strings.Repeat("a", 64), Collection: "docs", ShardID: 1, TargetEpoch: 2}
	if err := store.Freeze(first); err != nil {
		t.Fatal(err)
	}
	first.PlanDigest = strings.Repeat("b", 64)
	if err := store.Freeze(first); err == nil {
		t.Fatal("conflicting plan acquired frozen shard")
	}
	if err := store.ReleasePlan(strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if _, frozen := store.IsFrozen("docs", 1); !frozen {
		t.Fatal("unrelated plan released barrier")
	}
	if err := store.ReleasePlan(strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, frozen := store.IsFrozen("docs", 1); frozen {
		t.Fatal("exact plan did not release barrier")
	}
}
