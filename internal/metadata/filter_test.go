package metadata

import (
	"errors"
	"fmt"
	"math"
	"strings"
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

func TestPersistedIndexRoundTripAndValidation(t *testing.T) {
	index := NewIndex()
	index.Upsert(1, map[string]any{"kind": "guide", "year": 2024})
	index.Upsert(2, map[string]any{"kind": "api", "year": 2026})
	index.Upsert(3, nil)
	payload, err := index.MarshalBinary(3)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := LoadBinary(payload, 3)
	if err != nil {
		t.Fatal(err)
	}
	expr, _ := Parse(map[string]any{"year": map[string]any{"$gte": 2025}})
	matched := restored.Evaluate(expr)
	if matched.Len() != 1 || !matched.Contains(2) {
		t.Fatalf("restored numeric posting = %#v", setIDs(matched))
	}
	if _, err := LoadBinary(payload, 4); err == nil {
		t.Fatal("accepted persisted metadata index with wrong record count")
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

func evaluateIDs(t *testing.T, index *Index, raw map[string]any) []uint64 {
	t.Helper()
	expr, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse %v: %v", raw, err)
	}
	set := index.Evaluate(expr)
	var ids []uint64
	for id := uint64(0); id < uint64(len(set.words)*64); id++ {
		if set.Contains(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestTextOperator(t *testing.T) {
	index := NewIndex()
	index.Upsert(1, map[string]any{"title": "Vector Databases, explained!"})
	index.Upsert(2, map[string]any{"title": "Relational databases"})
	index.Upsert(3, map[string]any{"title": 42})
	index.Upsert(4, map[string]any{"body": "vector"})
	cases := map[string][]uint64{
		"databases":        {1, 2},
		"VECTOR databases": {1},
		"vector graph":     nil,
		"explained":        {1},
		"data":             nil, // whole words only
	}
	for query, want := range cases {
		got := evaluateIDs(t, index, map[string]any{"title": map[string]any{"$text": query}})
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("$text %q = %v, want %v", query, got, want)
		}
	}
	if got := evaluateIDs(t, index, map[string]any{"$not": map[string]any{"title": map[string]any{"$text": "vector"}}}); fmt.Sprint(got) != "[2 3 4]" {
		t.Fatalf("$not $text = %v", got)
	}
	for _, bad := range []any{"", "  ,, ", 7, strings.Repeat("a", maxTextQueryBytes+1)} {
		if _, err := Parse(map[string]any{"title": map[string]any{"$text": bad}}); err == nil {
			t.Fatalf("$text %v should be rejected", bad)
		}
	}
}

func TestGeoRadiusOperator(t *testing.T) {
	index := NewIndex()
	points := map[uint64][2]float64{
		1: {48.8566, 2.3522},  // Paris
		2: {48.8049, 2.1204},  // Versailles, ~19 km from Paris
		3: {51.5074, -0.1278}, // London, ~344 km
		4: {-16.5, 179.9},     // Fiji side of the antimeridian
		5: {-16.5, -179.9},    // ~21 km away across it
		6: {89.99, 10},        // near the north pole
		7: {89.99, -170},      // other side of the pole, ~2.2 km away
	}
	for id, point := range points {
		index.Upsert(id, map[string]any{"lat": point[0], "lon": point[1]})
	}
	index.Upsert(8, map[string]any{"lat": 48.8566}) // missing lon
	index.Upsert(9, map[string]any{"y": 48.8566, "x": 2.3522})
	geo := func(lat, lon, meters float64, extra ...string) map[string]any {
		spec := map[string]any{"lat": lat, "lon": lon, "meters": meters}
		if len(extra) == 2 {
			spec["lat_field"], spec["lon_field"] = extra[0], extra[1]
		}
		return map[string]any{"$geo_radius": spec}
	}
	cases := []struct {
		filter map[string]any
		want   string
	}{
		{geo(48.8566, 2.3522, 1000), "[1]"},
		{geo(48.8566, 2.3522, 25_000), "[1 2]"},
		{geo(48.8566, 2.3522, 400_000), "[1 2 3]"},
		{geo(-16.5, 179.95, 30_000), "[4 5]"},
		{geo(89.99, 10, 5_000), "[6 7]"},
		{geo(48.8566, 2.3522, 1000, "y", "x"), "[9]"},
		{map[string]any{"$and": []any{geo(48.8566, 2.3522, 25_000), map[string]any{"lat": map[string]any{"$gt": 48.83}}}}, "[1]"},
	}
	for _, c := range cases {
		if got := fmt.Sprint(evaluateIDs(t, index, c.filter)); got != c.want {
			t.Fatalf("%v = %s, want %s", c.filter, got, c.want)
		}
	}
	for _, bad := range []any{
		"x",
		map[string]any{"lat": 1, "lon": 2},
		map[string]any{"lat": 91, "lon": 0, "meters": 1},
		map[string]any{"lat": 0, "lon": 0, "meters": -1},
		map[string]any{"lat": 0, "lon": 0, "meters": 1, "radius": 2},
		map[string]any{"lat": 0, "lon": 0, "meters": 1, "lat_field": ""},
	} {
		if _, err := Parse(map[string]any{"$geo_radius": bad}); err == nil {
			t.Fatalf("$geo_radius %v should be rejected", bad)
		}
	}
}
