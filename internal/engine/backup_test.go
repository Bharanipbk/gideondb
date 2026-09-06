package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	archive := filepath.Join(root, "backup.tar.gz")
	restoredPath := filepath.Join(root, "restored")
	db, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	originalIdentity, err := cluster.LoadOrCreate(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cluster.LoadOrCreateMetadata(source, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := cluster.OpenRaftStore(source, originalIdentity.ID, 1); err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "documents", Dimension: 2, Metric: core.MetricDot, ShardCount: 2, Index: core.IndexConfig{Type: core.IndexFlat}}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	for _, record := range []core.Record{{ID: "one", Vector: []float32{1, 0}}, {ID: "two", Vector: []float32{0, 1}}} {
		if _, err := db.Upsert(config.Name, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ApplyShardOwnership(map[string][]uint32{"documents": {0, 1}}); err != nil {
		t.Fatal(err)
	}
	recoveryPoint, err := db.CaptureRecoveryPoint(7, originalIdentity.ID, strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BackupAtRecoveryPoint(archive, recoveryPoint); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBackup(archive, restoredPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(restoredPath, cluster.IdentityFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup cloned node identity %s: %v", originalIdentity.ID, err)
	}
	if _, err := os.Stat(filepath.Join(restoredPath, cluster.MetadataFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup cloned cluster metadata: %v", err)
	}
	if _, err := os.Stat(filepath.Join(restoredPath, OwnershipFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup cloned shard ownership: %v", err)
	}
	if _, err := os.Stat(filepath.Join(restoredPath, cluster.RaftStateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup cloned metadata Raft identity: %v", err)
	}
	restoredPoint, err := os.ReadFile(filepath.Join(restoredPath, RestoredRecoveryPointFile))
	if err != nil || !strings.Contains(string(restoredPoint), `"metadata_epoch":7`) || !strings.Contains(string(restoredPoint), originalIdentity.ID) {
		t.Fatalf("restored recovery point=%q err=%v", restoredPoint, err)
	}
	restored, err := Open(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for _, id := range []string{"one", "two"} {
		record, err := restored.Get(config.Name, "", id)
		if err != nil || record.ID != id {
			t.Fatalf("restored %s = %#v, %v", id, record, err)
		}
	}
	if err := RestoreBackup(archive, restoredPath); err == nil {
		t.Fatal("expected existing destination rejection")
	}
}

func TestBackupRejectsStaleRecoveryPoint(t *testing.T) {
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	point, err := db.CaptureRecoveryPoint(3, "11111111111111111111111111111111", strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert("docs", core.Record{ID: "later", Vector: []float32{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := db.BackupAtRecoveryPoint(filepath.Join(root, "stale.tar.gz"), point); err == nil {
		t.Fatal("backup accepted a stale shard sequence fence")
	}
}

func TestRestoreRejectsCorruptArchive(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "corrupt.tar.gz")
	if err := os.WriteFile(archive, []byte("not a backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := RestoreBackup(archive, filepath.Join(root, "restore"))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected corruption error, got %v", err)
	}
}

func TestBackupRejectsDestinationInsideDataPath(t *testing.T) {
	root := t.TempDir()
	db, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Backup(filepath.Join(root, "nested", "backup.tar.gz")); err == nil {
		t.Fatal("expected in-data backup rejection")
	}
}
