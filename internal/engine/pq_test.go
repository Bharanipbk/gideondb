package engine

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index/pq"
	"github.com/Bharanipbk/gideondb/internal/storage/segmentfile"
	"github.com/Bharanipbk/gideondb/internal/wal"
)

func TestPQCollectionPersistsCodebooksAcrossCheckpointAndReopen(t *testing.T) {
	path := t.TempDir()
	db, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways, CheckpointEvery: 0})
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "pq", Dimension: 16, Metric: core.MetricL2, ShardCount: 1, Index: core.IndexConfig{Type: core.IndexFlat, Quantization: core.QuantizationPQ, PQSubspaces: 4}}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	described, _, err := db.DescribeCollection("pq")
	if err != nil || described.Index.Oversampling != 8 || described.Index.PQSubspaces != 4 {
		t.Fatalf("defaults not applied: %+v %v", described.Index, err)
	}
	random := rand.New(rand.NewSource(7))
	vectors := make([][]float32, pq.MinTrainingVectors+200)
	for i := range vectors {
		vectors[i] = make([]float32, 16)
		for j := range vectors[i] {
			vectors[i][j] = float32(random.NormFloat64())
		}
	}
	upsert := func(db *Engine, from, to int) {
		for i := from; i < to; i++ {
			if _, err := db.Upsert("pq", core.Record{ID: fmt.Sprintf("v%05d", i), Vector: vectors[i]}); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(db *Engine, label string, count int) {
		t.Helper()
		for _, target := range []int{0, count / 2, count - 1} {
			results, err := db.Search("pq", "", vectors[target], 1)
			if err != nil || len(results) != 1 || results[0].ID != fmt.Sprintf("v%05d", target) {
				t.Fatalf("%s: target %d results=%#v err=%v", label, target, results, err)
			}
		}
	}
	manifestPath := filepath.Join(path, "segments", "pq", "shard-000000", "MANIFEST.json")
	upsert(db, 0, pq.MinTrainingVectors+100)
	check(db, "mutable", pq.MinTrainingVectors+100)
	if err := db.Checkpoint("pq"); err != nil {
		t.Fatal(err)
	}
	check(db, "checkpointed", pq.MinTrainingVectors+100)
	manifest, err := segmentfile.LoadManifest(manifestPath)
	if err != nil || manifest.PQFile == "" {
		t.Fatalf("first checkpoint manifest=%+v err=%v", manifest, err)
	}
	upsert(db, pq.MinTrainingVectors+100, len(vectors))
	if err := db.Checkpoint("pq"); err != nil {
		t.Fatal(err)
	}
	manifest, err = segmentfile.LoadManifest(manifestPath)
	if err != nil || manifest.PQFile == "" {
		t.Fatalf("second checkpoint manifest=%+v err=%v", manifest, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	check(reopened, "reopened", len(vectors))
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	// A checkpoint written before PQ files existed loads by retraining.
	directory := filepath.Dir(manifestPath)
	if err := os.Remove(filepath.Join(directory, manifest.PQFile)); err != nil {
		t.Fatal(err)
	}
	manifest.PQFile = ""
	if err := segmentfile.SaveManifest(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	legacy, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	check(legacy, "legacy", len(vectors))
}
