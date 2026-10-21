package engine

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/wal"
)

func namedConfig(index core.IndexType) core.CollectionConfig {
	return core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricCosine, ShardCount: 2, Index: core.IndexConfig{Type: index}, Vectors: map[string]core.VectorField{
		"title":   {Dimension: 3, Metric: core.MetricDot, Datatype: core.DatatypeFloat16, Index: core.IndexConfig{Type: core.IndexHNSW}},
		"body":    {Dimension: 2, Metric: core.MetricL2, Datatype: core.DatatypeBFloat16},
		"colbert": {Dimension: 2, Metric: core.MetricDot, MultiVector: true},
	}}
}

func namedRecords() []core.Record {
	return []core.Record{
		{ID: "a", Vector: []float32{1, 0}, Vectors: map[string][]float32{"title": {1, 0, 0}, "body": {0, 0}}, MultiVectors: map[string][][]float32{"colbert": {{1, 0}, {0, 0.1}}}},
		{ID: "b", Vector: []float32{0, 1}, Vectors: map[string][]float32{"title": {0, 1, 0}, "body": {5, 5}}, MultiVectors: map[string][][]float32{"colbert": {{0.6, 0}, {0, 0.6}}}},
		{ID: "c", Vector: []float32{1, 1}, Vectors: map[string][]float32{"title": {0.1, 0.1, 0.3}}},
	}
}

func checkNamedSearch(t *testing.T, db *Engine) {
	t.Helper()
	results, err := db.SearchNamed("docs", "", core.NamedQuery{Field: "title", Vector: []float32{0, 1, 0}, TopK: 2}, nil)
	if err != nil || len(results) != 2 || results[0].ID != "b" {
		t.Fatalf("title search = %+v, %v", results, err)
	}
	results, err = db.SearchNamed("docs", "", core.NamedQuery{Field: "body", Vector: []float32{4, 4}, TopK: 5}, nil)
	if err != nil || len(results) != 2 || results[0].ID != "b" {
		t.Fatalf("body search must cover only records with the field: %+v, %v", results, err)
	}
	// MaxSim for the token query {1,0}: a scores 1, b scores 0.6.
	results, err = db.SearchNamed("docs", "", core.NamedQuery{Field: "colbert", MultiVector: [][]float32{{1, 0}}, TopK: 2}, nil)
	if err != nil || len(results) != 2 || results[0].ID != "a" || results[0].Score != 1 {
		t.Fatalf("maxsim search = %+v, %v", results, err)
	}
	record, err := db.Get("docs", "", "a")
	if err != nil || len(record.MultiVectors["colbert"]) != 2 || record.Vectors["title"][0] != 1 || record.Vectors["body"] == nil {
		t.Fatalf("Get must return named vectors: %+v, %v", record, err)
	}
	if _, err := db.Get("docs", "", "c"); err != nil {
		t.Fatal(err)
	}
	dense, err := db.Search("docs", "", []float32{1, 0}, 1)
	if err != nil || dense[0].ID != "a" {
		t.Fatalf("default vector search = %+v, %v", dense, err)
	}
	first, err := db.Search("docs", "", []float32{0, 1}, 3)
	if err != nil {
		t.Fatal(err)
	}
	reranked, err := db.RerankMaxSim("docs", first, "colbert", [][]float32{{1, 0}})
	if err != nil || len(reranked) != 2 || reranked[0].ID != "a" {
		t.Fatalf("maxsim rerank must reorder and drop records without the field: %+v, %v", reranked, err)
	}
}

func TestNamedVectorsSurviveWALReplayAndCheckpoints(t *testing.T) {
	for _, index := range []core.IndexType{core.IndexFlat, core.IndexHNSW} {
		t.Run(string(index), func(t *testing.T) {
			path := t.TempDir()
			db, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.CreateCollection(namedConfig(index)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.BatchUpsert("docs", namedRecords()); err != nil {
				t.Fatal(err)
			}
			checkNamedSearch(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			// WAL replay only.
			if db, err = OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways}); err != nil {
				t.Fatal(err)
			}
			checkNamedSearch(t, db)
			if err := db.Checkpoint("docs"); err != nil {
				t.Fatal(err)
			}
			// A post-checkpoint delta overrides the base's named vectors.
			if _, err := db.Upsert("docs", core.Record{ID: "c", Vector: []float32{1, 1}, Vectors: map[string][]float32{"title": {0, 0, 1}}}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if db, err = OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways}); err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			checkNamedSearch(t, db)
			results, err := db.SearchNamed("docs", "", core.NamedQuery{Field: "title", Vector: []float32{0, 0, 1}, TopK: 1}, nil)
			if err != nil || results[0].ID != "c" || results[0].Score != 1 {
				t.Fatalf("delta must shadow base: %+v, %v", results, err)
			}
			if err := db.Delete("docs", "", "a"); err != nil {
				t.Fatal(err)
			}
			results, err = db.SearchNamed("docs", "", core.NamedQuery{Field: "colbert", MultiVector: [][]float32{{1, 0}}, TopK: 5}, nil)
			if err != nil || len(results) != 1 || results[0].ID != "b" {
				t.Fatalf("deleted record must leave named search: %+v, %v", results, err)
			}
		})
	}
}

func TestNamedVectorCheckpointUsesRecordColumnV2(t *testing.T) {
	path := t.TempDir()
	db, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateCollection(core.CollectionConfig{Name: "plain", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}); err != nil {
		t.Fatal(err)
	}
	config := namedConfig(core.IndexFlat)
	config.ShardCount = 1
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert("plain", core.Record{ID: "x", Vector: []float32{1, 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BatchUpsert("docs", namedRecords()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]uint16{"plain": 1, "docs": 2} {
		if err := db.Checkpoint(name); err != nil {
			t.Fatal(err)
		}
		files, _ := filepath.Glob(filepath.Join(db.shardSegmentDir(name, 0), "*.records"))
		if len(files) == 0 {
			t.Fatalf("%s: no record column written", name)
		}
		data, err := os.ReadFile(files[len(files)-1])
		if err != nil {
			t.Fatal(err)
		}
		if got := binary.LittleEndian.Uint16(data[4:6]); got != want {
			t.Fatalf("%s record column version = %d, want %d", name, got, want)
		}
	}
}

func TestNamedVectorValidation(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateCollection(namedConfig(core.IndexFlat)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert("docs", core.Record{ID: "x", Vector: []float32{1, 0}, Vectors: map[string][]float32{"nope": {1}}}); err == nil {
		t.Fatal("unknown field accepted")
	}
	for _, query := range []core.NamedQuery{
		{Field: "nope", Vector: []float32{1}, TopK: 1},
		{Field: "title", Vector: []float32{1}, TopK: 1},
		{Field: "title", MultiVector: [][]float32{{1, 0, 0}}, TopK: 1},
		{Field: "colbert", TopK: 1},
	} {
		if _, err := db.SearchNamed("docs", "", query, nil); err == nil {
			t.Fatalf("SearchNamed(%+v) succeeded", query)
		}
	}
}

func TestNamedVectorsReplicateAndBackUp(t *testing.T) {
	config := namedConfig(core.IndexFlat)
	config.ShardCount = 1
	leader, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	if err := leader.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	prepared, err := leader.BatchUpsertShard(config.Name, 0, namedRecords())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	follower, err := Open(filepath.Join(root, "follower"))
	if err != nil {
		t.Fatal(err)
	}
	if err := follower.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	if err := follower.ApplyReplicaBatch(config.Name, 0, 1, prepared); err != nil {
		t.Fatal(err)
	}
	checkNamedSearch(t, follower)
	sequence, snapshot, err := leader.ExportReplicaSnapshot(config.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(filepath.Join(root, "fresh"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := fresh.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	if err := fresh.InstallReplicaSnapshot(config.Name, 0, sequence, snapshot); err != nil {
		t.Fatal(err)
	}
	checkNamedSearch(t, fresh)

	archive := filepath.Join(root, "backup.tar.gz")
	if err := follower.Backup(archive); err != nil {
		t.Fatal(err)
	}
	if err := follower.Close(); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err := RestoreBackup(archive, restored); err != nil {
		t.Fatal(err)
	}
	db, err := Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	checkNamedSearch(t, db)
}
