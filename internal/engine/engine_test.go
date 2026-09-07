package engine

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
	"github.com/Bharanipbk/gideondb/internal/storage/segmentfile"
	"github.com/Bharanipbk/gideondb/internal/wal"
)

func TestPersistenceAndSearch(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "docs", Dimension: 3, Metric: core.MetricCosine, ShardCount: 4}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	for _, record := range []core.Record{
		{ID: "a", Vector: []float32{1, 0, 0}, Metadata: map[string]any{"kind": "a"}},
		{ID: "b", Vector: []float32{0, 1, 0}},
		{ID: "c", Namespace: "other", Vector: []float32{1, 0, 0}},
	} {
		if _, err := db.Upsert("docs", record); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get("docs", "", "a")
	if err != nil || got.Metadata["kind"] != "a" {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	results, err := reopened.Search("docs", "", []float32{1, 0, 0}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].ID != "a" || results[1].ID != "b" {
		t.Fatalf("unexpected namespace-filtered results: %#v", results)
	}
	if err := reopened.Delete("docs", "", "a"); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Get("docs", "", "a"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("expected persisted deletion, got %v", err)
	}
	_ = reopened.Close()
}

func TestSparseAndHybridSearchSurviveCheckpoint(t *testing.T) {
	path := t.TempDir()
	db, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways, CheckpointEvery: 0})
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "hybrid", Dimension: 2, Metric: core.MetricCosine, ShardCount: 2}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	for _, record := range []core.Record{
		{ID: "dense", Vector: []float32{1, 0}, SparseVector: map[string]float32{"other": 1}},
		{ID: "sparse", Vector: []float32{0, 1}, SparseVector: map[string]float32{"database": 1}},
	} {
		if _, err := db.Upsert(config.Name, record); err != nil {
			t.Fatal(err)
		}
	}
	sparse, err := db.SearchSparseHybrid(config.Name, "", nil, map[string]float32{"database": 1}, 0.5, 2, nil)
	if err != nil || len(sparse) != 2 || sparse[0].ID != "sparse" {
		t.Fatalf("sparse=%#v err=%v", sparse, err)
	}
	denseWeighted, err := db.SearchSparseHybrid(config.Name, "", []float32{1, 0}, map[string]float32{"database": 1}, 0.8, 2, nil)
	if err != nil || denseWeighted[0].ID != "dense" {
		t.Fatalf("dense weighted=%#v err=%v", denseWeighted, err)
	}
	sparseWeighted, err := db.SearchSparseHybrid(config.Name, "", []float32{1, 0}, map[string]float32{"database": 1}, 0.2, 2, nil)
	if err != nil || sparseWeighted[0].ID != "sparse" {
		t.Fatalf("sparse weighted=%#v err=%v", sparseWeighted, err)
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(config.Name, "", "sparse")
	if err != nil || got.SparseVector["database"] != 1 {
		t.Fatalf("record=%#v err=%v", got, err)
	}
	results, err := reopened.SearchSparseHybrid(config.Name, "", nil, map[string]float32{"database": 1}, 0.5, 1, nil)
	if err != nil || len(results) != 1 || results[0].ID != "sparse" {
		t.Fatalf("recovered sparse=%#v err=%v", results, err)
	}
}

func TestCheckpointLoadsSegmentThenReplaysWAL(t *testing.T) {
	path := t.TempDir()
	db, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways})
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "checkpointed", Dimension: 2, Metric: core.MetricDot, ShardCount: 2}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	for _, record := range []core.Record{
		{ID: "keep", Vector: []float32{2, 0}},
		{ID: "delete", Vector: []float32{1, 0}},
		{ID: "other", Vector: []float32{0, 1}},
	} {
		if _, err := db.Upsert("checkpointed", record); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Delete("checkpointed", "", "delete"); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint("checkpointed"); err != nil {
		t.Fatal(err)
	}
	for shardID := 0; shardID < 2; shardID++ {
		walPath := filepath.Join(path, "wal", "checkpointed", fmt.Sprintf("shard-%06d.wal", shardID))
		info, err := os.Stat(walPath)
		if err != nil || info.Size() != 0 {
			t.Fatalf("WAL %s not reset: size=%v err=%v", walPath, info.Size(), err)
		}
	}
	if _, err := db.Upsert("checkpointed", core.Record{ID: "after", Vector: []float32{3, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Get("checkpointed", "", "delete"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("deleted record resurrected: %v", err)
	}
	for _, id := range []string{"keep", "other", "after"} {
		if _, err := reopened.Get("checkpointed", "", id); err != nil {
			t.Fatalf("record %s missing after recovery: %v", id, err)
		}
	}
}

func TestMappedCheckpointMergesDeltaAndTombstones(t *testing.T) {
	path := t.TempDir()
	db, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways})
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "mapped-live", Dimension: 2, Metric: core.MetricDot, ShardCount: 1, Index: core.IndexConfig{Type: core.IndexFlat}}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	for _, record := range []core.Record{
		{ID: "replace", Vector: []float32{1, 0}, Metadata: map[string]any{"generation": float64(1)}},
		{ID: "remove", Vector: []float32{2, 0}},
		{ID: "keep", Vector: []float32{0, 1}},
	} {
		if _, err := db.Upsert(config.Name, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert(config.Name, core.Record{ID: "replace", Vector: []float32{4, 0}, Metadata: map[string]any{"generation": float64(2)}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(config.Name, "", "remove"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert(config.Name, core.Record{ID: "new", Vector: []float32{3, 0}}); err != nil {
		t.Fatal(err)
	}
	results, err := db.Search(config.Name, "", []float32{1, 0}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].ID != "replace" || results[1].ID != "new" || results[2].ID != "keep" {
		t.Fatalf("unexpected merged mapped/delta results: %#v", results)
	}
	got, err := db.Get(config.Name, "", "replace")
	if err != nil || got.Vector[0] != 4 || got.Metadata["generation"] != float64(2) {
		t.Fatalf("replacement not visible: %#v, %v", got, err)
	}
	if _, err := db.Get(config.Name, "", "remove"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("tombstone not visible: %v", err)
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"replace", "new", "keep"} {
		if _, err := db.Get(config.Name, "", id); err != nil {
			t.Fatalf("%s missing after mapped restart: %v", id, err)
		}
	}
	if _, err := db.Get(config.Name, "", "remove"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("deleted record resurrected after restart: %v", err)
	}
}

func TestAutomaticCheckpoint(t *testing.T) {
	path := t.TempDir()
	db, _ := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways, CheckpointEvery: 2})
	defer db.Close()
	_ = db.CreateCollection(core.CollectionConfig{Name: "auto", Dimension: 1, Metric: core.MetricDot, ShardCount: 1})
	_, _ = db.Upsert("auto", core.Record{ID: "one", Vector: []float32{1}})
	_, err := db.Upsert("auto", core.Record{ID: "two", Vector: []float32{2}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(path, "segments", "auto", "shard-000000", "MANIFEST.json")
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("automatic checkpoint missing: %v", err)
	}
}

func TestCorruptCheckpointFailsEngineStartup(t *testing.T) {
	path := t.TempDir()
	db, _ := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways})
	_ = db.CreateCollection(core.CollectionConfig{Name: "corrupt", Dimension: 1, Metric: core.MetricDot, ShardCount: 1})
	_, _ = db.Upsert("corrupt", core.Record{ID: "one", Vector: []float32{1}})
	if err := db.Checkpoint("corrupt"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(path, "segments", "corrupt", "shard-000000")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var segmentPath string
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".vectors" {
			segmentPath = filepath.Join(directory, entry.Name())
			break
		}
	}
	if segmentPath == "" {
		t.Fatal("segment file not found")
	}
	file, _ := os.OpenFile(segmentPath, os.O_RDWR, 0)
	_, _ = file.WriteAt([]byte{'X'}, 42)
	_ = file.Close()
	if reopened, err := Open(path); err == nil {
		_ = reopened.Close()
		t.Fatal("expected corrupt segment to fail engine startup")
	}
}

func TestFilteredSearchWithFlatAndHNSW(t *testing.T) {
	for _, indexConfig := range []core.IndexConfig{
		{Type: core.IndexFlat},
		{Type: core.IndexHNSW, M: 8, EFConstruction: 32, EFSearch: 32},
	} {
		t.Run(string(indexConfig.Type), func(t *testing.T) {
			db, _ := Open(t.TempDir())
			defer db.Close()
			config := core.CollectionConfig{Name: "filtered", Dimension: 2, Metric: core.MetricDot, ShardCount: 2, Index: indexConfig}
			if err := db.CreateCollection(config); err != nil {
				t.Fatal(err)
			}
			for _, record := range []core.Record{
				{ID: "best-wrong-language", Vector: []float32{10, 0}, Metadata: map[string]any{"language": "python", "year": 2026}},
				{ID: "best-match", Vector: []float32{8, 0}, Metadata: map[string]any{"language": "go", "year": 2025}},
				{ID: "older", Vector: []float32{7, 0}, Metadata: map[string]any{"language": "go", "year": 2022}},
			} {
				if _, err := db.Upsert("filtered", record); err != nil {
					t.Fatal(err)
				}
			}
			filter, err := metadata.Parse(map[string]any{
				"language": "go", "year": map[string]any{"$gte": 2024},
			})
			if err != nil {
				t.Fatal(err)
			}
			results, err := db.SearchFiltered("filtered", "", []float32{1, 0}, 10, filter)
			if err != nil || len(results) != 1 || results[0].ID != "best-match" {
				t.Fatalf("filtered search = %#v, %v", results, err)
			}
		})
	}
}

func TestBatchUpsertReplaysSingleShardWALRecord(t *testing.T) {
	path := t.TempDir()
	db, _ := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways})
	if err := db.CreateCollection(core.CollectionConfig{Name: "batch", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}); err != nil {
		t.Fatal(err)
	}
	stored, err := db.BatchUpsert("batch", []core.Record{
		{ID: "one", Vector: []float32{1, 0}},
		{ID: "two", Vector: []float32{2, 0}},
		{ID: "three", Vector: []float32{3, 0}},
	})
	if err != nil || len(stored) != 3 {
		t.Fatalf("BatchUpsert() = %#v, %v", stored, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, id := range []string{"one", "two", "three"} {
		if _, err := reopened.Get("batch", "", id); err != nil {
			t.Fatalf("batch record %s not recovered: %v", id, err)
		}
	}
}

func TestBatchUpsertValidatesBeforeWriting(t *testing.T) {
	db, _ := Open(t.TempDir())
	defer db.Close()
	_ = db.CreateCollection(core.CollectionConfig{Name: "batch", Dimension: 2, Metric: core.MetricDot, ShardCount: 1})
	_, err := db.BatchUpsert("batch", []core.Record{
		{ID: "valid", Vector: []float32{1, 0}},
		{ID: "invalid", Vector: []float32{1}},
	})
	if !errors.Is(err, core.ErrDimensionMismatch) {
		t.Fatalf("expected validation error, got %v", err)
	}
	if _, err := db.Get("batch", "", "valid"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("valid prefix was written before validation completed: %v", err)
	}
}

func TestRejectsNonFiniteVectors(t *testing.T) {
	db, _ := Open(t.TempDir())
	defer db.Close()
	_ = db.CreateCollection(core.CollectionConfig{Name: "finite", Dimension: 1, Metric: core.MetricDot, ShardCount: 1})
	if _, err := db.Upsert("finite", core.Record{ID: "nan", Vector: []float32{float32(math.NaN())}}); !errors.Is(err, core.ErrInvalidArgument) {
		t.Fatalf("expected non-finite vector rejection, got %v", err)
	}
	if _, err := db.Search("finite", "", []float32{float32(math.Inf(1))}, 1); !errors.Is(err, core.ErrInvalidArgument) {
		t.Fatalf("expected non-finite query rejection, got %v", err)
	}
}

func TestRejectsUnsafeCollectionName(t *testing.T) {
	db, _ := Open(t.TempDir())
	err := db.CreateCollection(core.CollectionConfig{Name: "../escape", Dimension: 2, Metric: core.MetricDot, ShardCount: 1})
	if !errors.Is(err, core.ErrInvalidArgument) {
		t.Fatalf("expected invalid argument, got %v", err)
	}
}

func TestHNSWCollectionSurvivesRestart(t *testing.T) {
	path := t.TempDir()
	db, _ := Open(path)
	config := core.CollectionConfig{
		Name: "approx", Dimension: 2, Metric: core.MetricDot, ShardCount: 2,
		Index: core.IndexConfig{Type: core.IndexHNSW, M: 4, EFConstruction: 16, EFSearch: 16},
	}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert("approx", core.Record{ID: "winner", Vector: []float32{2, 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert("approx", core.Record{ID: "other", Vector: []float32{0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint("approx"); err != nil {
		t.Fatal(err)
	}
	for shardID := 0; shardID < config.ShardCount; shardID++ {
		manifest, err := segmentfile.LoadManifest(filepath.Join(path, "segments", config.Name, fmt.Sprintf("shard-%06d", shardID), "MANIFEST.json"))
		if err != nil {
			t.Fatal(err)
		}
		if manifest.GraphFile == "" {
			t.Fatalf("shard %d manifest does not reference an hnsw graph", shardID)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Search("approx", "", []float32{1, 0}, 1)
	if err != nil || len(got) != 1 || got[0].ID != "winner" {
		t.Fatalf("Search() = %#v, %v", got, err)
	}
	described, _, _ := reopened.DescribeCollection("approx")
	if described.Index.Type != core.IndexHNSW {
		t.Fatalf("index config not restored: %#v", described.Index)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(path, "segments", config.Name, "shard-000000", "MANIFEST.json")
	manifest, err := segmentfile.LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(filepath.Dir(manifestPath), manifest.GraphFile)
	graph, err := os.ReadFile(graphPath)
	if err != nil {
		t.Fatal(err)
	}
	graph[len(graph)-1] ^= 0xff
	if err := os.WriteFile(graphPath, graph, 0o640); err != nil {
		t.Fatal(err)
	}
	if corrupt, err := Open(path); err == nil {
		_ = corrupt.Close()
		t.Fatal("expected corrupt hnsw graph to fail engine startup")
	}
}

func TestFilterIndexSurvivesRestartAndCorruption(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "filters", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert("filters", core.Record{ID: "guide", Vector: []float32{1, 0}, Metadata: map[string]any{"kind": "guide", "year": 2026}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint("filters"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	filter, _ := metadata.Parse(map[string]any{"kind": "guide"})
	results, err := reopened.SearchFiltered("filters", "", []float32{1, 0}, 1, filter)
	if err != nil || len(results) != 1 || results[0].ID != "guide" {
		t.Fatalf("persisted filter search = %#v, %v", results, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(path, "segments", "filters", "shard-000000", "MANIFEST.json")
	manifest, err := segmentfile.LoadManifest(manifestPath)
	if err != nil || manifest.FilterFile == "" {
		t.Fatalf("filter manifest = %#v, %v", manifest, err)
	}
	filterPath := filepath.Join(filepath.Dir(manifestPath), manifest.FilterFile)
	data, err := os.ReadFile(filterPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(filterPath, data, 0o640); err != nil {
		t.Fatal(err)
	}
	if corrupt, err := Open(path); err == nil {
		_ = corrupt.Close()
		t.Fatal("expected corrupt filter index to fail engine startup")
	}
}

func TestPinnedSnapshotRetainsOlderGeneration(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "pinned", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert(config.Name, core.Record{ID: "item", Vector: []float32{1, 0}, Metadata: map[string]any{"generation": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	firstManifestPath := filepath.Join(path, "segments", config.Name, "shard-000000", "MANIFEST.json")
	firstManifest, err := segmentfile.LoadManifest(firstManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	firstVectorFile := firstManifest.VectorsFile
	if firstManifest.Format == 3 {
		firstVectorFile = firstManifest.Segments[0].VectorsFile
	}
	first, err := db.PinSnapshot(config.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert(config.Name, core.Record{ID: "item", Vector: []float32{2, 0}, Metadata: map[string]any{"generation": 2}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	second, err := db.PinSnapshot(config.Name)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generations()[0] == second.Generations()[0] {
		t.Fatal("checkpoint did not publish a new generation")
	}
	oldRecord, err := first.Get("", "item")
	if err != nil || oldRecord.Vector[0] != 1 {
		t.Fatalf("first generation record = %#v, %v", oldRecord, err)
	}
	newRecord, err := second.Get("", "item")
	if err != nil || newRecord.Vector[0] != 2 {
		t.Fatalf("second generation record = %#v, %v", newRecord, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(firstManifestPath), firstVectorFile)); err != nil {
		t.Fatalf("pinned generation vector file removed: %v", err)
	}
	if err := db.DeleteCollection(config.Name); !errors.Is(err, core.ErrInvalidArgument) {
		t.Fatalf("delete with pinned readers error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert(config.Name, core.Record{ID: "item", Vector: []float32{3, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert(config.Name, core.Record{ID: "item", Vector: []float32{4, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(firstManifestPath), firstVectorFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released obsolete generation still exists: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMultiSegmentDeltaRecoveryAppliesNewestVersionsAndTombstones(t *testing.T) {
	path := t.TempDir()
	db, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways, CheckpointEvery: 0})
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "multi", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	for _, record := range []core.Record{{ID: "a", Vector: []float32{1, 0}}, {ID: "b", Vector: []float32{0, 1}}} {
		if _, err := db.Upsert(config.Name, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert(config.Name, core.Record{ID: "a", Vector: []float32{2, 0}, Metadata: map[string]any{"kind": "new"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(config.Name, "", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert(config.Name, core.Record{ID: "c", Vector: []float32{0, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert(config.Name, core.Record{ID: "d", Vector: []float32{1, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(path, "segments", config.Name, "shard-000000", "MANIFEST.json")
	manifest, err := segmentfile.LoadManifest(manifestPath)
	if err != nil || manifest.Format != 3 || len(manifest.Segments) != 3 || manifest.RecordCount != 3 || manifest.Segments[1].TombstonesFile == "" {
		t.Fatalf("multi-segment manifest = %#v, %v", manifest, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	a, err := reopened.Get(config.Name, "", "a")
	if err != nil || a.Vector[0] != 2 || a.Metadata["kind"] != "new" {
		t.Fatalf("newest a = %#v, %v", a, err)
	}
	if _, err := reopened.Get(config.Name, "", "b"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("deleted b error = %v", err)
	}
	for _, id := range []string{"c", "d"} {
		if _, err := reopened.Get(config.Name, "", id); err != nil {
			t.Fatalf("missing %s after recovery: %v", id, err)
		}
	}
}

func TestMigratePersistentFormatsRewritesLegacyCombinedCheckpoint(t *testing.T) {
	path := t.TempDir()
	db, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways, CheckpointEvery: 0})
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "legacy", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	stored, err := db.Upsert(config.Name, core.Record{ID: "one", Vector: []float32{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(path, "segments", config.Name, "shard-000000")
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := segmentfile.Write(filepath.Join(directory, "legacy.vseg"), []core.Record{stored}, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := segmentfile.SaveManifest(filepath.Join(directory, "MANIFEST.json"), segmentfile.Manifest{Format: 1, SegmentFile: "legacy.vseg", MaxLSN: 1, RecordCount: 1}); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways, CheckpointEvery: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.MigratePersistentFormats(); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := segmentfile.LoadManifest(filepath.Join(directory, "MANIFEST.json"))
	if err != nil || manifest.Format != 3 || manifest.RecordCount != 1 || len(manifest.Segments) != 1 {
		t.Fatalf("migrated manifest=%#v err=%v", manifest, err)
	}
	verified, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	if record, err := verified.Get(config.Name, "", stored.ID); err != nil || record.Version != stored.Version {
		t.Fatalf("migrated record=%#v err=%v", record, err)
	}
}

func TestMigratePersistentFormatsPromotesFormatTwoByReference(t *testing.T) {
	path := t.TempDir()
	db, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways, CheckpointEvery: 0})
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "legacy-columns", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	stored, err := db.Upsert(config.Name, core.Record{ID: "one", Vector: []float32{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint(config.Name); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(path, "segments", config.Name, "shard-000000")
	manifestPath := filepath.Join(directory, "MANIFEST.json")
	current, err := segmentfile.LoadManifest(manifestPath)
	if err != nil || len(current.Segments) != 1 {
		t.Fatalf("current manifest=%#v err=%v", current, err)
	}
	ref := current.Segments[0]
	legacy := segmentfile.Manifest{
		Format:      2,
		RecordsFile: ref.RecordsFile,
		VectorsFile: ref.VectorsFile,
		GraphFile:   ref.GraphFile,
		FilterFile:  ref.FilterFile,
		Dimension:   ref.Dimension,
		MaxLSN:      ref.MaxLSN,
		RecordCount: ref.RecordCount,
	}
	if err := segmentfile.SaveManifest(manifestPath, legacy); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAlways, CheckpointEvery: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.MigratePersistentFormats(); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := segmentfile.LoadManifest(manifestPath)
	if err != nil || migrated.Format != 3 || len(migrated.Segments) != 1 {
		t.Fatalf("migrated manifest=%#v err=%v", migrated, err)
	}
	if migrated.Segments[0].RecordsFile != ref.RecordsFile || migrated.Segments[0].VectorsFile != ref.VectorsFile {
		t.Fatalf("format-2 files were not promoted by reference: before=%#v after=%#v", ref, migrated.Segments[0])
	}
	verified, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	if record, err := verified.Get(config.Name, "", stored.ID); err != nil || record.Version != stored.Version {
		t.Fatalf("migrated record=%#v err=%v", record, err)
	}
}

func TestBatchUpsertShardValidatesRoutingAndRecovers(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "distributed", Dimension: 2, Metric: core.MetricDot, ShardCount: 4}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	record := core.Record{ID: "record", Vector: []float32{1, 0}}
	shardID, err := db.RouteShard(config.Name, "", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BatchUpsertShard(config.Name, (shardID+1)%4, []core.Record{record}); err == nil {
		t.Fatal("expected wrong-shard rejection")
	}
	stored, err := db.BatchUpsertShard(config.Name, shardID, []core.Record{record})
	if err != nil || len(stored) != 1 || stored[0].Version == 0 {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(config.Name, "", record.ID)
	if err != nil || got.Version != stored[0].Version {
		t.Fatalf("got=%#v err=%v", got, err)
	}
}

func TestShardBatchIdempotencySurvivesCheckpointAndRestart(t *testing.T) {
	dataPath := t.TempDir()
	db, err := OpenWithOptions(dataPath, Options{WALSyncMode: wal.SyncAlways, CheckpointEvery: 1})
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "idempotent", Dimension: 2, Metric: core.MetricDot, ShardCount: 1}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	request := []core.Record{{ID: "one", Vector: []float32{1, 2}, Metadata: map[string]any{"source": "test"}}}
	first, firstSequence, err := db.BatchUpsertShardIdempotent(config.Name, 0, request, "import:batch-001")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenWithOptions(dataPath, Options{WALSyncMode: wal.SyncAlways, CheckpointEvery: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	retried, retrySequence, err := reopened.BatchUpsertShardIdempotent(config.Name, 0, request, "import:batch-001")
	if err != nil {
		t.Fatal(err)
	}
	if retrySequence != firstSequence || len(retried) != 1 || retried[0].Version != first[0].Version || retried[0].Timestamp != first[0].Timestamp {
		t.Fatalf("retry changed committed result: first=%#v/%d retry=%#v/%d", first, firstSequence, retried, retrySequence)
	}
	_, _, err = reopened.BatchUpsertShardIdempotent(config.Name, 0, []core.Record{{ID: "one", Vector: []float32{2, 1}}}, "import:batch-001")
	if !errors.Is(err, core.ErrIdempotencyConflict) {
		t.Fatalf("reused key error = %v, want idempotency conflict", err)
	}
}
