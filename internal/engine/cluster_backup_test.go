package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vectordb/vectordb/internal/core"
)

func TestClusterBackupPackageRestoresManifestBoundNodeArchives(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("a", 64)
	nodes := []string{"11111111111111111111111111111111", "22222222222222222222222222222222"}
	points := make([]NodeRecoveryPoint, 0, 2)
	archives := map[string]string{}
	for index, nodeID := range nodes {
		db, err := Open(filepath.Join(root, "source-"+nodeID))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CreateCollection(core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 2}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Upsert("docs", core.Record{ID: string(rune('a' + index)), Vector: []float32{1, 2}}); err != nil {
			t.Fatal(err)
		}
		point, err := db.CaptureRecoveryPoint(5, nodeID, digest, "")
		if err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(root, nodeID+".tar.gz")
		if err := db.BackupAtRecoveryPoint(archive, point); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		points = append(points, point)
		archives[nodeID] = archive
	}
	manifest, err := MergeRecoveryPoints(points)
	if err != nil {
		t.Fatal(err)
	}
	packagePath := filepath.Join(root, "cluster.tar.gz")
	if err := CreateClusterBackup(packagePath, manifest, archives); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err := RestoreClusterBackup(packagePath, restored); err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range nodes {
		if _, err := os.Stat(filepath.Join(restored, "nodes", nodeID, RestoredRecoveryPointFile)); err != nil {
			t.Fatalf("node %s provenance: %v", nodeID, err)
		}
	}
	if err := CreateClusterBackup(filepath.Join(root, "incomplete.tar.gz"), manifest, map[string]string{nodes[0]: archives[nodes[0]]}); err == nil {
		t.Fatal("accepted incomplete node archive set")
	}
}
