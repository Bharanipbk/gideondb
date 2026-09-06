package metadata

import (
	"errors"
	"math"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestFilterOperators(t *testing.T) {
	index := NewIndex()
	index.Upsert(1, map[string]any{"language": "go", "year": 2024, "active": true})
	index.Upsert(2, map[string]any{"language": "python", "year": 2022, "active": false})
	index.Upsert(3, map[string]any{"language": "go", "year": 2026})
	index.Upsert(4, nil)

	tests := []struct {
		name string
		raw  map[string]any
		want []uint64
	}{
		{"implicit equality", map[string]any{"language": "go"}, []uint64{1, 3}},
		{"not equal requires field", map[string]any{"language": map[string]any{"$ne": "go"}}, []uint64{2}},
		{"range", map[string]any{"year": map[string]any{"$gte": 2024, "$lt": 2026}}, []uint64{1}},
		{"in", map[string]any{"language": map[string]any{"$in": []any{"python", "rust"}}}, []uint64{2}},
		{"not in", map[string]any{"language": map[string]any{"$nin": []any{"python"}}}, []uint64{1, 3}},
		{"exists", map[string]any{"active": map[string]any{"$exists": false}}, []uint64{3, 4}},
		{"and", map[string]any{"$and": []any{map[string]any{"language": "go"}, map[string]any{"year": map[string]any{"$gt": 2024}}}}, []uint64{3}},
		{"or", map[string]any{"$or": []any{map[string]any{"year": map[string]any{"$lte": 2022}}, map[string]any{"active": true}}}, []uint64{1, 2}},
		{"not", map[string]any{"$not": map[string]any{"language": "go"}}, []uint64{2, 4}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expr, err := Parse(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			got := index.Evaluate(expr)
			if got.Len() != len(test.want) {
				t.Fatalf("got %d matches, want IDs %v", got.Len(), test.want)
			}
			for _, id := range test.want {
				if !got.Contains(id) {
					t.Fatalf("ID %d missing", id)
				}
			}
		})
	}
}

func TestIndexUpdateAndDelete(t *testing.T) {
	index := NewIndex()
	index.Upsert(1, map[string]any{"kind": "old"})
	index.Upsert(1, map[string]any{"kind": "new"})
	old, _ := Parse(map[string]any{"kind": "old"})
	newValue, _ := Parse(map[string]any{"kind": "new"})
	if index.Evaluate(old).Len() != 0 || index.Evaluate(newValue).Len() != 1 {
		t.Fatal("upsert left stale posting")
	}
	index.Delete(1)
	if index.Evaluate(newValue).Len() != 0 {
		t.Fatal("delete left stale posting")
	}
}

func TestParseRejectsInvalidFilters(t *testing.T) {
	for _, raw := range []map[string]any{
		{"$unknown": true},
		{"year": map[string]any{"$gte": []any{1}}},
		{"kind": map[string]any{"$in": []any{}}},
		{"$and": "not-an-array"},
		{"score": math.Inf(1)},
	} {
		if _, err := Parse(raw); !errors.Is(err, core.ErrInvalidArgument) {
			t.Fatalf("expected invalid argument for %#v, got %v", raw, err)
		}
	}
}

func TestDenseBitmapAcrossWords(t *testing.T) {
	index := NewIndex()
	for _, id := range []uint64{1, 63, 64, 65, 4096} {
		index.Upsert(id, map[string]any{"selected": id%2 == 0})
	}
	expr, _ := Parse(map[string]any{"selected": true})
	got := index.Evaluate(expr)
	for _, id := range []uint64{64, 4096} {
		if !got.Contains(id) {
			t.Fatalf("bitmap lost ID %d", id)
		}
	}
	if got.Len() != 2 {
		t.Fatalf("bitmap count = %d, want 2", got.Len())
	}
}

func TestAdaptivePostingPromotesAndRemoves(t *testing.T) {
	index := NewIndex()
	for id := uint64(1); id <= 100; id++ {
		index.Upsert(id, map[string]any{"shared": "value"})
	}
	posting := index.postings["shared"][canonical("value")]
	if posting.dense == nil || posting.count != 100 {
		t.Fatalf("posting did not promote: %#v", posting)
	}
	for id := uint64(1); id <= 25; id++ {
		index.Delete(id)
	}
	expr, _ := Parse(map[string]any{"shared": "value"})
	if got := index.Evaluate(expr).Len(); got != 75 {
		t.Fatalf("posting count after delete = %d, want 75", got)
	}
	index.Upsert(10_000, map[string]any{"unique": "only"})
	unique := index.postings["unique"][canonical("only")]
	if unique.dense != nil || unique.sparse != nil || unique.count != 1 {
		t.Fatalf("singleton posting allocated dense storage: %#v", unique)
	}
}

func TestNumericRangeAfterUpdateAndDelete(t *testing.T) {
	index := NewIndex()
	index.Upsert(1, map[string]any{"score": 10})
	index.Upsert(2, map[string]any{"score": 20})
	index.Upsert(1, map[string]any{"score": 30})
	index.Delete(2)
	expr, _ := Parse(map[string]any{"score": map[string]any{"$gte": 15}})
	got := index.Evaluate(expr)
	if got.Len() != 1 || !got.Contains(1) {
		t.Fatalf("stale numeric entries remain: count=%d", got.Len())
	}
}

func TestNumericDeltaMergesAndRemainsCorrect(t *testing.T) {
	index := NewIndex()
	for id := uint64(1); id <= 5000; id++ {
		index.Upsert(id, map[string]any{"score": int(id)})
	}
	field := index.numeric["score"]
	if field == nil || len(field.base) == 0 || len(field.delta) >= 4096 {
		t.Fatalf("unexpected base/delta sizes: base=%d delta=%d", len(field.base), len(field.delta))
	}
	for id := uint64(1); id <= 1000; id++ {
		index.Upsert(id, map[string]any{"score": int(id + 10_000)})
	}
	expr, _ := Parse(map[string]any{"score": map[string]any{"$gte": 10_001}})
	if got := index.Evaluate(expr).Len(); got != 1000 {
		t.Fatalf("updated numeric range count = %d, want 1000", got)
	}
}

func BenchmarkEqualityFilter100K(b *testing.B) {
	index := NewIndex()
	for id := uint64(1); id <= 100_000; id++ {
		index.Upsert(id, map[string]any{"category": "category-" + string(rune('a'+id%10))})
	}
	expr, _ := Parse(map[string]any{"category": "category-a"})
	b.ReportAllocs()
	for b.Loop() {
		_ = index.Evaluate(expr)
	}
}

func BenchmarkNumericRange100K(b *testing.B) {
	index := NewIndex()
	for id := uint64(1); id <= 100_000; id++ {
		index.Upsert(id, map[string]any{"year": 2000 + int(id%30)})
	}
	expr, _ := Parse(map[string]any{"year": map[string]any{"$gte": 2020}})
	b.ReportAllocs()
	for b.Loop() {
		_ = index.Evaluate(expr)
	}
}

func BenchmarkNumericIndexBuild100K(b *testing.B) {
	for b.Loop() {
		index := NewIndex()
		for id := uint64(1); id <= 100_000; id++ {
			index.Upsert(id, map[string]any{"score": int(id)})
		}
	}
}
