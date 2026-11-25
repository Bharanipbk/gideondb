package shard

import (
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
	"github.com/Bharanipbk/gideondb/internal/segment"
)

func TestFacetsCountLiveRecordsAcrossSegments(t *testing.T) {
	config := core.CollectionConfig{Name: "f", Dimension: 2, Metric: core.Metric("l2"), ShardCount: 1, TextAnalyzer: "english"}.Normalized()
	s, err := New(0, config)
	if err != nil {
		t.Fatal(err)
	}
	base := []core.Record{
		{ID: "a", Vector: []float32{1, 0}, Metadata: map[string]any{"color": "red", "tags": []any{"x", "y", "x"}}, Text: "running shoes"},
		{ID: "b", Vector: []float32{0, 1}, Metadata: map[string]any{"color": "red"}, Text: "blue jacket"},
		{ID: "c", Vector: []float32{1, 1}, Metadata: map[string]any{"color": "blue", "tags": []any{"y"}}, Text: "trail runner"},
	}
	immutable, err := segment.NewImmutable(config, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InstallImmutable(immutable); err != nil {
		t.Fatal(err)
	}
	// Overwrite b in the active segment and delete c.
	if err := s.Upsert(core.Record{ID: "b", Vector: []float32{0, 1}, Metadata: map[string]any{"color": "green"}, Text: "green jacket"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("", "c"); err != nil {
		t.Fatal(err)
	}
	requests := []core.FacetRequest{{Field: "color"}, {Field: "tags"}}
	top := s.Facets("", nil, "", []string{"color", "tags"}).Top(requests)
	assertBuckets(t, top["color"], map[any]int{"red": 1, "green": 1})
	assertBuckets(t, top["tags"], map[any]int{"x": 1, "y": 1})

	filter, err := metadata.Parse(map[string]any{"color": "green"})
	if err != nil {
		t.Fatal(err)
	}
	assertBuckets(t, s.Facets("", filter, "", []string{"color"}).Top(requests)["color"], map[any]int{"green": 1})
	// "runs" stems to "run", matching a's "running" but not deleted c.
	assertBuckets(t, s.Facets("", nil, "runs", []string{"color"}).Top(requests)["color"], map[any]int{"red": 1})
}

func assertBuckets(t *testing.T, got []core.FacetBucket, want map[any]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("buckets = %+v, want %v", got, want)
	}
	for _, bucket := range got {
		if want[bucket.Value] != bucket.Count {
			t.Fatalf("buckets = %+v, want %v", got, want)
		}
	}
}

func TestUpsertBatchCountsAndTombstones(t *testing.T) {
	config := core.CollectionConfig{Name: "f", Dimension: 2, Metric: core.Metric("l2"), ShardCount: 1}.Normalized()
	s, _ := New(0, config)
	base, _ := segment.NewImmutable(config, []core.Record{{ID: "old", Vector: []float32{1, 0}}, {ID: "gone", Vector: []float32{0, 1}}}, nil)
	if err := s.InstallImmutable(base); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("", "gone"); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 {
		t.Fatalf("len after delete = %d", s.Len())
	}
	// "old" exists in the base; "gone" is resurrected; "new" appears twice.
	err := s.UpsertBatch([]core.Record{{ID: "old", Vector: []float32{1, 1}}, {ID: "gone", Vector: []float32{0, 2}}, {ID: "new", Vector: []float32{2, 2}}, {ID: "new", Vector: []float32{3, 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 3 {
		t.Fatalf("len = %d, want 3", s.Len())
	}
	if record, err := s.Get("", "gone"); err != nil || record.Vector[1] != 2 {
		t.Fatalf("tombstone not cleared: %v %v", record, err)
	}
	if record, _ := s.Get("", "new"); record.Vector[0] != 3 {
		t.Fatalf("last write did not win: %v", record)
	}
}
