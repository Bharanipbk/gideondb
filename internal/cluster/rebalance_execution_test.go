package cluster

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vectordb/vectordb/internal/core"
)

type recordingDataMover struct {
	copies      []string
	catchups    []string
	failCatchUp bool
}

func (m *recordingDataMover) CopySnapshot(_ context.Context, movement ShardMovement, target string) error {
	m.copies = append(m.copies, fmt.Sprintf("%s/%d/%s", movement.Collection, movement.ShardID, target))
	return nil
}

func (m *recordingDataMover) CatchUpWAL(_ context.Context, movement ShardMovement, target string) error {
	m.catchups = append(m.catchups, fmt.Sprintf("%s/%d/%s", movement.Collection, movement.ShardID, target))
	if m.failCatchUp {
		m.failCatchUp = false
		return errors.New("injected catch-up failure")
	}
	return nil
}

func TestRebalanceExecutorPersistsAndResumesDataPreparation(t *testing.T) {
	const local = "11111111111111111111111111111111"
	peerB := Peer{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", Healthy: true}
	peerC := Peer{NodeID: "33333333333333333333333333333333", AdvertiseAddress: "node-c:6333", Healthy: true}
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 32}
	current, err := PlanReplicaPlacement(7, local, "node-a:6333", []Peer{peerB}, []core.CollectionConfig{config}, 2)
	if err != nil {
		t.Fatal(err)
	}
	target, err := PlanReplicaPlacement(8, local, "node-a:6333", []Peer{peerB, peerC}, []core.CollectionConfig{config}, 2)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanRebalance(current, target)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	executor, err := OpenRebalanceExecutor(path)
	if err != nil {
		t.Fatal(err)
	}
	mover := &recordingDataMover{failCatchUp: true}
	partial, err := executor.Prepare(context.Background(), plan, mover)
	if err == nil || partial.ReadyForCutover || len(partial.Completed) != 1 || len(mover.copies) != 1 {
		t.Fatalf("partial=%#v copies=%v catchups=%v err=%v", partial, mover.copies, mover.catchups, err)
	}
	restarted, err := OpenRebalanceExecutor(path)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := restarted.Prepare(context.Background(), plan, mover)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.ReadyForCutover || len(mover.copies) < 1 {
		t.Fatalf("prepared=%#v copies=%v", prepared, mover.copies)
	}
	// The already durable first copy is not repeated after restart.
	first := mover.copies[0]
	count := 0
	for _, copied := range mover.copies {
		if copied == first {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("durable copy repeated %d times: %v", count, mover.copies)
	}
	if err := restarted.ResetCommitted(7); err == nil {
		t.Fatal("reset before target epoch commit succeeded")
	}
	if err := restarted.ResetCommitted(8); err != nil {
		t.Fatal(err)
	}
	if _, exists := restarted.Status(); exists {
		t.Fatal("completed preparation was not cleared")
	}
}

func TestRebalanceExecutorRejectsDifferentActivePlan(t *testing.T) {
	const a = "11111111111111111111111111111111"
	const b = "22222222222222222222222222222222"
	plan := RebalancePlan{FromEpoch: 1, ToEpoch: 2, Movements: []ShardMovement{{
		Collection: "docs", ShardID: 0, SourceLeaderID: a, TargetLeaderID: b,
		Actions: []RebalanceAction{{ID: "docs/0/copy/" + b, Type: "copy_snapshot", SourceID: a, TargetID: b}},
	}}}
	executor, err := OpenRebalanceExecutor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Prepare(context.Background(), plan, &recordingDataMover{}); err != nil {
		t.Fatal(err)
	}
	changed := plan
	changed.ToEpoch = 3
	if _, err := executor.Prepare(context.Background(), changed, &recordingDataMover{}); err == nil {
		t.Fatal("different plan replaced active durable preparation")
	}
	if err := executor.Abort(changed); err == nil {
		t.Fatal("different plan aborted active preparation")
	}
	if err := executor.Abort(plan); err != nil {
		t.Fatal(err)
	}
	if _, exists := executor.Status(); exists {
		t.Fatal("aborted preparation remains active")
	}
}

func TestCapacityRebalanceResumesAfterInjectedTargetFailure(t *testing.T) {
	const local = "11111111111111111111111111111111"
	peer := Peer{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", PlacementCapacity: 1, Healthy: true}
	config := core.CollectionConfig{Name: "weighted", Dimension: 2, Metric: core.MetricDot, ShardCount: 128}
	current, target, plan, err := PlanCapacityRebalance(11, local, "node-a:6333", 1, []Peer{peer}, []core.CollectionConfig{config}, 1, peer.NodeID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Nodes) != len(target.Nodes) || len(plan.Movements) == 0 {
		t.Fatalf("invalid capacity plan: current=%#v target=%#v plan=%#v", current, target, plan)
	}
	path := t.TempDir()
	executor, err := OpenRebalanceExecutor(path)
	if err != nil {
		t.Fatal(err)
	}
	mover := &recordingDataMover{failCatchUp: true}
	partial, err := executor.Prepare(context.Background(), plan, mover)
	if err == nil || partial.ReadyForCutover {
		t.Fatalf("injected target failure did not interrupt preparation: partial=%#v err=%v", partial, err)
	}
	restarted, err := OpenRebalanceExecutor(path)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := restarted.Prepare(context.Background(), plan, mover)
	if err != nil || !prepared.ReadyForCutover {
		t.Fatalf("capacity preparation did not resume: prepared=%#v err=%v", prepared, err)
	}
	for _, copied := range mover.copies {
		count := 0
		for _, candidate := range mover.copies {
			if candidate == copied {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("durable capacity copy repeated after restart: %v", mover.copies)
		}
	}
	if err := restarted.ResetCommitted(12); err != nil {
		t.Fatal(err)
	}
	if _, exists := restarted.Status(); exists {
		t.Fatal("capacity preparation journal remains after committed epoch")
	}
}
