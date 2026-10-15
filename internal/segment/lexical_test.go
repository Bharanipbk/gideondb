package segment

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
)

func newTestSegment(t *testing.T, records ...core.Record) *Segment {
	t.Helper()
	s, err := New(core.CollectionConfig{Name: "c", Dimension: 2, Metric: core.MetricCosine, ShardCount: 1}.Normalized())
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err := s.Upsert(record); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func ids(results []core.SearchResult) []string {
	out := make([]string, len(results))
	for i, result := range results {
		out[i] = result.ID
	}
	return out
}

func TestTokenizeLowercasesSplitsAndDropsStopwords(t *testing.T) {
	got := Tokenize("The Vector-DB, and HNSW_index v2!")
	want := []string{"vector", "db", "hnsw", "index", "v2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestBM25RanksByTermRarityAndFrequency(t *testing.T) {
	s := newTestSegment(t,
		core.Record{ID: "common", Vector: []float32{1, 0}, Text: "database database database storage"},
		core.Record{ID: "rare", Vector: []float32{1, 0}, Text: "quantization database"},
		core.Record{ID: "other", Vector: []float32{1, 0}, Text: "storage engine database"},
		core.Record{ID: "none", Vector: []float32{1, 0}, Text: "unrelated words only"},
	)
	results, err := s.SearchHybrid("", core.HybridQuery{Text: "quantization database", TopK: 10}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := ids(results)
	if len(got) != 3 || got[0] != "rare" {
		t.Fatalf("rare term should rank first and non-matching records be excluded: %v", got)
	}
	for _, result := range results {
		if result.Score <= 0 {
			t.Fatalf("text-only scores are raw positive BM25 values: %+v", result)
		}
	}
}

func TestLexicalIndexTracksUpdatesAndDeletes(t *testing.T) {
	s := newTestSegment(t, core.Record{ID: "a", Vector: []float32{1, 0}, Text: "alpha", SparseVector: map[string]float32{"x": 1}})
	if err := s.Upsert(core.Record{ID: "a", Vector: []float32{1, 0}, Text: "beta", SparseVector: map[string]float32{"y": 1}}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []core.HybridQuery{{Text: "alpha", TopK: 5}, {Sparse: map[string]float32{"x": 1}, TopK: 5}} {
		if results, _ := s.SearchHybrid("", query, nil, nil); len(results) != 0 {
			t.Fatalf("stale postings after update for %+v: %v", query, ids(results))
		}
	}
	if results, _ := s.SearchHybrid("", core.HybridQuery{Text: "beta", TopK: 5}, nil, nil); len(results) != 1 {
		t.Fatalf("updated text not indexed: %v", ids(results))
	}
	if err := s.Delete("", "a"); err != nil {
		t.Fatal(err)
	}
	if len(s.lexical.text) != 0 || len(s.lexical.sparse) != 0 || s.lexical.totalTerms != 0 || len(s.lexical.docLength) != 0 {
		t.Fatalf("lexical index not empty after delete: %+v", s.lexical)
	}
}

func TestHybridRespectsNamespaceFilterAndExclusions(t *testing.T) {
	s := newTestSegment(t,
		core.Record{ID: "keep", Vector: []float32{1, 0}, Text: "vector search", Metadata: map[string]any{"lang": "en"}},
		core.Record{ID: "filtered", Vector: []float32{1, 0}, Text: "vector search", Metadata: map[string]any{"lang": "fr"}},
		core.Record{ID: "excluded", Vector: []float32{1, 0}, Text: "vector search", Metadata: map[string]any{"lang": "en"}},
		core.Record{ID: "elsewhere", Vector: []float32{1, 0}, Text: "vector search", Namespace: "tenant-b", Metadata: map[string]any{"lang": "en"}},
	)
	filter, err := metadata.Parse(map[string]any{"lang": "en"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := s.SearchHybrid("", core.HybridQuery{Text: "vector", Dense: []float32{1, 0}, Alpha: 0.5, TopK: 10}, filter, map[string]struct{}{key("", "excluded"): {}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(results); !reflect.DeepEqual(got, []string{"keep"}) {
		t.Fatalf("got %v", got)
	}
}

func TestWeightedFusionBlendsDenseSparseAndText(t *testing.T) {
	s := newTestSegment(t,
		core.Record{ID: "dense", Vector: []float32{1, 0}, Text: "cooking recipes"},
		core.Record{ID: "lexical", Vector: []float32{0, 1}, Text: "gideondb vector database", SparseVector: map[string]float32{"gideondb": 1}},
	)
	query := core.HybridQuery{Dense: []float32{1, 0}, Sparse: map[string]float32{"gideondb": 1}, Text: "gideondb database", TopK: 2}
	query.Alpha = 0.9
	if results, _ := s.SearchHybrid("", query, nil, nil); results[0].ID != "dense" {
		t.Fatalf("high alpha should favour dense: %v", ids(results))
	}
	query.Alpha = 0.1
	results, _ := s.SearchHybrid("", query, nil, nil)
	if results[0].ID != "lexical" || len(results) != 2 {
		t.Fatalf("low alpha should favour lexical and include dense-only candidates: %v", ids(results))
	}
	for _, result := range results {
		if result.Score < 0 || result.Score > 1 {
			t.Fatalf("weighted scores must be normalized: %+v", result)
		}
	}
}

func TestRRFFusionSumsReciprocalRanks(t *testing.T) {
	s := newTestSegment(t,
		core.Record{ID: "both", Vector: []float32{0.9, 0.1}, Text: "hybrid retrieval"},
		core.Record{ID: "dense-only", Vector: []float32{1, 0}, Text: "unrelated"},
		core.Record{ID: "text-only", Vector: []float32{0, 1}, Text: "hybrid"},
	)
	results, err := s.SearchHybrid("", core.HybridQuery{Dense: []float32{1, 0}, Text: "hybrid retrieval", Fusion: core.FusionRRF, TopK: 3}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].ID != "both" {
		t.Fatalf("record ranked by both signals should win under RRF: %v", ids(results))
	}
	want := float32(1)/61 + float32(1)/62
	if diff := results[0].Score - want; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("rrf score=%v want %v", results[0].Score, want)
	}
}

func TestHybridQueryValidation(t *testing.T) {
	for _, query := range []core.HybridQuery{
		{TopK: 1},
		{Text: "x", TopK: 0},
		{Text: "x", TopK: 1, Alpha: 2},
		{Text: "x", TopK: 1, Fusion: "max"},
		{Text: "x", TopK: 1, Dense: []float32{1}},
	} {
		if err := query.Validate(2); err == nil {
			t.Fatalf("expected validation error for %+v", query)
		}
	}
}

func BenchmarkSparseInvertedIndex(b *testing.B) {
	s, _ := New(core.CollectionConfig{Name: "c", Dimension: 2, Metric: core.MetricCosine, ShardCount: 1}.Normalized())
	for i := range 20000 {
		_ = s.Upsert(core.Record{ID: fmt.Sprint(i), Vector: []float32{1, 0}, SparseVector: map[string]float32{fmt.Sprint("t", i%500): 1, "shared": 0.1}, Text: fmt.Sprintf("document %d topic%d", i, i%200)})
	}
	b.ResetTimer()
	for range b.N {
		_, _ = s.SearchHybrid("", core.HybridQuery{Sparse: map[string]float32{"t7": 1}, Text: "topic7", TopK: 10}, nil, nil)
	}
}
