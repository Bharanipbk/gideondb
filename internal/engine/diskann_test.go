package engine

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
	"github.com/Bharanipbk/gideondb/internal/storage/segmentfile"
)

func TestDiskANNCollectionCheckpointsAndRestarts(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "disk", Dimension: 8, Metric: core.MetricL2, ShardCount: 2, Index: core.IndexConfig{Type: core.IndexDiskANN, MaxDegree: 8, EFConstruction: 32}}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewSource(1))
	vectors := map[string][]float32{}
	for i := range 400 {
		vector := make([]float32, 8)
		for j := range vector {
			vector[j] = float32(random.NormFloat64())
		}
		id := fmt.Sprintf("r%03d", i)
		vectors[id] = vector
		if _, err := db.Upsert("disk", core.Record{ID: id, Vector: vector, Metadata: map[string]any{"even": i%2 == 0}}); err != nil {
			t.Fatal(err)
		}
	}
	selfSearch := func(db *Engine, id string) {
		t.Helper()
		got, err := db.Search("disk", "", vectors[id], 3)
		if err != nil || len(got) == 0 || got[0].ID != id {
			t.Fatalf("Search(%s) = %#v, %v", id, got, err)
		}
	}
	selfSearch(db, "r007") // mutable flat segment before any checkpoint
	if err := db.Checkpoint("disk"); err != nil {
		t.Fatal(err)
	}
	for shardID := range config.ShardCount {
		manifest, err := segmentfile.LoadManifest(filepath.Join(path, "segments", "disk", fmt.Sprintf("shard-%06d", shardID), "MANIFEST.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(manifest.GraphFile, ".diskann") {
			t.Fatalf("shard %d manifest graph file = %q, want a .diskann file", shardID, manifest.GraphFile)
		}
	}
	selfSearch(db, "r123")
	// Writes after the checkpoint land in the mutable segment and merge in.
	if _, err := db.Upsert("disk", core.Record{ID: "late", Vector: []float32{9, 9, 9, 9, 9, 9, 9, 9}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete("disk", "", "r123"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Search("disk", "", vectors["r123"], 1); len(got) == 1 && got[0].ID == "r123" {
		t.Fatal("deleted record returned from diskann segment")
	}
	vectors["late"] = []float32{9, 9, 9, 9, 9, 9, 9, 9}
	selfSearch(db, "late")
	if err := db.Checkpoint("disk"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	selfSearch(reopened, "late")
	selfSearch(reopened, "r250")
	if got, err := reopened.Get("disk", "", "r250"); err != nil || len(got.Vector) != 8 || got.Vector[3] != vectors["r250"][3] {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	if _, err := reopened.Get("disk", "", "r123"); err == nil {
		t.Fatal("deleted record survived restart")
	}
	filter, err := metadata.Parse(map[string]any{"even": true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.SearchFiltered("disk", "", vectors["r251"], 5, filter)
	if err != nil || len(got) != 5 {
		t.Fatalf("SearchFiltered() = %#v, %v", got, err)
	}
	for _, result := range got {
		if result.Metadata["even"] != true {
			t.Fatalf("filtered result %#v violates filter", result)
		}
	}
	var wait sync.WaitGroup
	for worker := range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := range 25 {
				id := fmt.Sprintf("r%03d", (worker*25+i)*3%400)
				if id == "r123" {
					continue
				}
				if _, err := reopened.Search("disk", "", vectors[id], 3); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	if _, err := reopened.Upsert("disk", core.Record{ID: "concurrent", Vector: vectors["r001"]}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Checkpoint("disk"); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(path, "segments", "disk", "shard-000000", "MANIFEST.json")
	manifest, err := segmentfile.LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(manifestPath))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".diskann") && entry.Name() != manifest.GraphFile {
			t.Fatalf("obsolete diskann file %s was not cleaned up", entry.Name())
		}
	}
	graphPath := filepath.Join(filepath.Dir(manifestPath), manifest.GraphFile)
	data, err := os.ReadFile(graphPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(graphPath, data, 0o640); err != nil {
		t.Fatal(err)
	}
	if corrupt, err := Open(path); err == nil {
		_ = corrupt.Close()
		t.Fatal("expected corrupt diskann file to fail engine startup")
	}
}
