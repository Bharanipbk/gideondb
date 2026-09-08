package engine

import (
	"errors"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestApplyReplicaBatchOrdersDeduplicatesAndRecovers(t *testing.T) {
	config := core.CollectionConfig{Name: "replicated", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}
	leader, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	if err := leader.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	prepared, err := leader.BatchUpsertShard(config.Name, 0, []core.Record{{ID: "one", Vector: []float32{1, 2}}})
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	follower, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := follower.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	if err := follower.ApplyReplicaBatch(config.Name, 0, 1, prepared); err != nil {
		t.Fatal(err)
	}
	if err := follower.ApplyReplicaBatch(config.Name, 0, 1, prepared); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	conflict := append([]core.Record(nil), prepared...)
	conflict[0].Vector = []float32{9, 9}
	if err := follower.ApplyReplicaBatch(config.Name, 0, 1, conflict); !errors.Is(err, core.ErrReplicationConflict) {
		t.Fatalf("conflicting retry error=%v", err)
	}
	if err := follower.ApplyReplicaBatch(config.Name, 0, 3, prepared); !errors.Is(err, core.ErrReplicationGap) {
		t.Fatalf("gap error=%v", err)
	}
	if err := follower.Close(); err != nil {
		t.Fatal(err)
	}
	follower, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()
	if err := follower.ApplyReplicaBatch(config.Name, 0, 1, prepared); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	got, err := follower.Get(config.Name, "", "one")
	if err != nil || got.Version != prepared[0].Version || got.Timestamp != prepared[0].Timestamp {
		t.Fatalf("recovered record=%#v err=%v", got, err)
	}
	if err := follower.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	if err := follower.ApplyReplicaBatch(config.Name, 0, 1, prepared); !errors.Is(err, core.ErrReplicationCompacted) {
		t.Fatalf("compacted retry error=%v", err)
	}
}

func TestApplyReplicaDeleteOrdersDeduplicatesAndRecovers(t *testing.T) {
	path := t.TempDir()
	config := core.CollectionConfig{Name: "replicated-delete", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}
	follower, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := follower.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	record := core.Record{ID: "one", Vector: []float32{1, 2}, Version: 1, Timestamp: 1}
	if err := follower.ApplyReplicaBatch(config.Name, 0, 1, []core.Record{record}); err != nil {
		t.Fatal(err)
	}
	if err := follower.ApplyReplicaDelete(config.Name, 0, 2, "", record.ID); err != nil {
		t.Fatal(err)
	}
	if err := follower.ApplyReplicaDelete(config.Name, 0, 2, "", record.ID); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	if err := follower.ApplyReplicaDelete(config.Name, 0, 2, "", "different"); !errors.Is(err, core.ErrReplicationConflict) {
		t.Fatalf("conflicting retry error=%v", err)
	}
	if _, err := follower.Get(config.Name, "", record.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("record survived delete: %v", err)
	}
	if err := follower.Close(); err != nil {
		t.Fatal(err)
	}
	follower, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()
	if _, err := follower.Get(config.Name, "", record.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("delete did not survive recovery: %v", err)
	}
}

func TestInstallReplicaSnapshotPersistsSequenceAndReplacement(t *testing.T) {
	path := t.TempDir()
	config := core.CollectionConfig{Name: "snapshot", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}
	follower, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := follower.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	old := core.Record{ID: "old", Vector: []float32{1, 0}, Version: 1, Timestamp: 1}
	if err := follower.ApplyReplicaBatch(config.Name, 0, 1, []core.Record{old}); err != nil {
		t.Fatal(err)
	}
	snapshot := core.Record{ID: "current", Vector: []float32{0, 1}, Version: 5, Timestamp: 5}
	if err := follower.InstallReplicaSnapshot(config.Name, 0, 5, []core.Record{snapshot}); err != nil {
		t.Fatal(err)
	}
	if _, err := follower.Get(config.Name, "", old.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("old record survived snapshot: %v", err)
	}
	if err := follower.Close(); err != nil {
		t.Fatal(err)
	}
	follower, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()
	if _, err := follower.Get(config.Name, "", snapshot.ID); err != nil {
		t.Fatalf("snapshot did not survive restart: %v", err)
	}
	next := core.Record{ID: "next", Vector: []float32{1, 1}, Version: 6, Timestamp: 6}
	if err := follower.ApplyReplicaBatch(config.Name, 0, 6, []core.Record{next}); err != nil {
		t.Fatalf("WAL did not continue after snapshot: %v", err)
	}
	if err := follower.InstallReplicaSnapshot(config.Name, 0, 4, []core.Record{snapshot}); !errors.Is(err, core.ErrReplicationConflict) {
		t.Fatalf("stale snapshot error=%v", err)
	}
}
