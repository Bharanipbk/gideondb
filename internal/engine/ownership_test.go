package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestShardOwnershipPrunesEmptyWALsAndSurvivesRestart(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "owned", Dimension: 2, Metric: core.MetricDot, ShardCount: 4}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	assignments := map[string][]uint32{"owned": {0, 2}}
	if err := db.ApplyShardOwnership(assignments); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyShardOwnership(assignments); err != nil {
		t.Fatalf("idempotent activation failed: %v", err)
	}
	if err := db.ApplyShardOwnership(map[string][]uint32{"owned": {1, 3}}); err == nil {
		t.Fatal("expected immutable ownership rejection")
	}
	info, err := os.Stat(filepath.Join(path, OwnershipFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("ownership mode=%o", info.Mode().Perm())
	}
	for shardID := 0; shardID < config.ShardCount; shardID++ {
		_, err := os.Stat(filepath.Join(path, "wal", config.Name, shardWALName(shardID)))
		if shardID == 0 || shardID == 2 {
			if err != nil {
				t.Fatalf("owned shard %d WAL: %v", shardID, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unowned shard %d WAL still exists: %v", shardID, err)
		}
	}
	unownedID := idForShard(t, db, config.Name, 1)
	if _, err := db.Get(config.Name, "", unownedID); !errors.Is(err, core.ErrShardNotOwned) {
		t.Fatalf("unowned read error=%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Get(config.Name, "", unownedID); !errors.Is(err, core.ErrShardNotOwned) {
		t.Fatalf("reopened unowned read error=%v", err)
	}
}

func TestShardOwnershipRefusesToPruneData(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	config := core.CollectionConfig{Name: "guarded", Dimension: 2, Metric: core.MetricDot, ShardCount: 2}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	id := idForShard(t, db, config.Name, 1)
	if _, err := db.Upsert(config.Name, core.Record{ID: id, Vector: []float32{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyShardOwnership(map[string][]uint32{"guarded": {0}}); err == nil {
		t.Fatal("expected non-empty shard rejection")
	}
	if _, err := os.Stat(filepath.Join(path, OwnershipFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest should not exist after rejection: %v", err)
	}
}

func idForShard(t *testing.T, db *Engine, collection string, wanted uint32) string {
	t.Helper()
	for candidate := 0; candidate < 10_000; candidate++ {
		id := fmt.Sprintf("record-%d", candidate)
		shardID, err := db.RouteShard(collection, "", id)
		if err != nil {
			t.Fatal(err)
		}
		if shardID == wanted {
			return id
		}
	}
	t.Fatalf("could not find ID for shard %d", wanted)
	return ""
}

func shardWALName(shardID int) string {
	return fmt.Sprintf("shard-%06d.wal", shardID)
}
