package lite_test

import (
	"errors"
	"testing"

	"github.com/Bharanipbk/gideondb/pkg/lite"
)

func TestLifecycle(t *testing.T) {
	dir := t.TempDir()
	db, err := lite.Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCollection(lite.Collection{Name: "docs", Dimension: 3, Metric: lite.Cosine}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCollection(lite.Collection{Name: "docs", Dimension: 3, Metric: lite.Cosine}); !errors.Is(err, lite.ErrAlreadyExists) {
		t.Fatalf("duplicate create: %v", err)
	}
	err = db.Upsert("docs",
		lite.Record{ID: "a", Vector: []float32{1, 0, 0}, Text: "go vector database", Metadata: map[string]any{"lang": "go"}},
		lite.Record{ID: "b", Vector: []float32{0.8, 0.2, 0}, Text: "python notebooks", Metadata: map[string]any{"lang": "python"}},
		lite.Record{ID: "c", Vector: []float32{0, 0, 1}, Text: "go concurrency", Metadata: map[string]any{"lang": "go"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := db.Search("docs", lite.Query{Vector: []float32{1, 0.1, 0}, TopK: 2})
	if err != nil || len(hits) != 2 || hits[0].ID != "a" || hits[1].ID != "b" {
		t.Fatalf("dense search = %+v, %v", hits, err)
	}
	hits, err = db.Search("docs", lite.Query{Vector: []float32{1, 0.1, 0}, TopK: 2, Filter: map[string]any{"lang": "go"}})
	if err != nil || len(hits) != 2 || hits[0].ID != "a" || hits[1].ID != "c" {
		t.Fatalf("filtered search = %+v, %v", hits, err)
	}
	hits, err = db.Search("docs", lite.Query{Text: "notebooks", TopK: 3})
	if err != nil || len(hits) == 0 || hits[0].ID != "b" {
		t.Fatalf("text search = %+v, %v", hits, err)
	}
	hits, err = db.Search("docs", lite.Query{Vector: []float32{0, 0, 1}, Text: "concurrency", TopK: 1, Fusion: lite.FusionRRF})
	if err != nil || len(hits) != 1 || hits[0].ID != "c" {
		t.Fatalf("hybrid search = %+v, %v", hits, err)
	}
	if _, err := db.Search("docs", lite.Query{Vector: []float32{1, 0}}); !errors.Is(err, lite.ErrDimensionMismatch) {
		t.Fatalf("bad dimension: %v", err)
	}
	if err := db.Delete("docs", "", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get("docs", "", "b"); !errors.Is(err, lite.ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Search("docs", lite.Query{Vector: []float32{1, 0, 0}}); !errors.Is(err, lite.ErrClosed) {
		t.Fatalf("search after close: %v", err)
	}

	reopened, err := lite.Open(dir, &lite.Options{AsyncWAL: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	count, err := reopened.Count("docs")
	if err != nil || count != 2 {
		t.Fatalf("count after reopen = %d, %v", count, err)
	}
	record, err := reopened.Get("docs", "", "a")
	if err != nil || record.Text != "go vector database" || record.Metadata["lang"] != "go" {
		t.Fatalf("get after reopen = %+v, %v", record, err)
	}
	collections, err := reopened.Collections()
	if err != nil || len(collections) != 1 || collections[0].Index.Type != lite.IndexFlat || collections[0].Shards != 1 {
		t.Fatalf("collections = %+v, %v", collections, err)
	}
}
