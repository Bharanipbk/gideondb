package hnsw

import (
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
	"github.com/Bharanipbk/gideondb/internal/index/flat"
)

func TestSearchRecallAgainstFlat(t *testing.T) {
	const dimension, count, k = 32, 1000, 10
	cfg := index.Config{Dimension: dimension, Metric: core.MetricCosine, M: 16, EFConstruction: 100, EFSearch: 100}
	approx, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	exact, _ := flat.New(cfg)
	vectors := deterministicVectors(count, dimension)
	for i, vector := range vectors {
		id := uint64(i + 1)
		if err := approx.Upsert(id, vector); err != nil {
			t.Fatal(err)
		}
		_ = exact.Upsert(id, vector)
	}
	var matches, total int
	for queryIndex := 0; queryIndex < 50; queryIndex++ {
		query := vectors[(queryIndex*19)%count]
		want, _ := exact.Search(query, k)
		got, err := approx.Search(query, k)
		if err != nil {
			t.Fatal(err)
		}
		set := make(map[uint64]struct{}, len(want))
		for _, candidate := range want {
			set[candidate.ID] = struct{}{}
		}
		for _, candidate := range got {
			if _, ok := set[candidate.ID]; ok {
				matches++
			}
		}
		total += len(want)
	}
	recall := float64(matches) / float64(total)
	t.Logf("recall@%d = %.3f", k, recall)
	if recall < 0.90 {
		t.Fatalf("recall@%d = %.3f, want >= 0.90", k, recall)
	}
}

func TestGraphRoundTripAndCorruption(t *testing.T) {
	cfg := index.Config{Dimension: 3, Metric: core.MetricCosine, M: 4, EFConstruction: 16, EFSearch: 12}
	original, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	vectors := [][]float32{{1, 0, 0}, {0.9, 0.1, 0}, {0, 1, 0}, {0, 0, 1}}
	ids := []uint64{1, 2, 3, 4}
	for position := range ids {
		if err := original.Upsert(ids[position], vectors[position]); err != nil {
			t.Fatal(err)
		}
	}
	graph, err := original.MarshalGraph()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := LoadGraph(cfg, ids, vectors, graph)
	if err != nil {
		t.Fatal(err)
	}
	if restored.packedOffsets == nil || len(restored.packedNeighbors) == 0 || restored.nodes[0].neighbors != nil {
		t.Fatal("restored graph did not use packed immutable adjacency")
	}
	if restored.Stats().GraphBytes == 0 || restored.Stats().GraphEdges != original.Stats().GraphEdges {
		t.Fatalf("packed graph stats=%#v original=%#v", restored.Stats(), original.Stats())
	}
	if err := restored.Upsert(99, []float32{1, 0, 0}); !errors.Is(err, core.ErrInvalidArgument) {
		t.Fatalf("packed graph upsert error = %v, want invalid argument", err)
	}
	if err := restored.Delete(1); !errors.Is(err, core.ErrInvalidArgument) {
		t.Fatalf("packed graph delete error = %v, want invalid argument", err)
	}
	reencoded, err := restored.MarshalGraph()
	if err != nil {
		t.Fatalf("marshal packed graph: %v", err)
	}
	if _, err := LoadGraph(cfg, ids, vectors, reencoded); err != nil {
		t.Fatalf("reload packed graph: %v", err)
	}
	want, _ := original.Search([]float32{1, 0, 0}, 3)
	got, err := restored.Search([]float32{1, 0, 0}, 3)
	if err != nil || len(got) != len(want) {
		t.Fatalf("restored search=%v err=%v", got, err)
	}
	for position := range want {
		if got[position] != want[position] {
			t.Fatalf("candidate %d=%v want %v", position, got[position], want[position])
		}
	}
	corrupt := append([]byte(nil), graph...)
	corrupt[len(corrupt)-1] ^= 0xff
	if _, err := LoadGraph(cfg, ids, vectors, corrupt); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum error, got %v", err)
	}
	wrong := cfg
	wrong.EFSearch++
	if _, err := LoadGraph(wrong, ids, vectors, graph); err == nil || !strings.Contains(err.Error(), "configuration") {
		t.Fatalf("expected configuration error, got %v", err)
	}
}

func TestUpdateDeleteAndConcurrentSearch(t *testing.T) {
	idx, _ := New(index.Config{Dimension: 2, Metric: core.MetricDot, M: 4, EFConstruction: 16, EFSearch: 16})
	for id := uint64(1); id <= 100; id++ {
		_ = idx.Upsert(id, []float32{float32(id), 1})
	}
	if err := idx.Upsert(1, []float32{1000, 0}); err != nil {
		t.Fatal(err)
	}
	results, _ := idx.Search([]float32{1, 0}, 1)
	if len(results) != 1 || results[0].ID != 1 {
		t.Fatalf("updated vector not found: %#v", results)
	}
	if err := idx.Delete(1); err != nil {
		t.Fatal(err)
	}
	results, _ = idx.Search([]float32{1, 0}, 10)
	for _, result := range results {
		if result.ID == 1 {
			t.Fatal("deleted ID returned by search")
		}
	}

	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				if _, err := idx.Search([]float32{1, 0}, 10); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wait.Wait()
	stats := idx.Stats()
	if stats.Type != core.IndexHNSW || stats.Vectors != 99 || stats.GraphEdges == 0 || stats.Deleted != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestQueryEFSearchAndSelectiveExactPath(t *testing.T) {
	idx, err := New(index.Config{Dimension: 2, Metric: core.MetricDot, M: 4, EFConstruction: 16, EFSearch: 8})
	if err != nil {
		t.Fatal(err)
	}
	for id, vector := range [][]float32{{1, 0}, {0.8, 0.2}, {0, 1}, {-1, 0}} {
		if err := idx.Upsert(uint64(id+1), vector); err != nil {
			t.Fatal(err)
		}
	}
	allowed := func(id uint64) bool { return id == 2 || id == 3 }
	results, err := idx.SearchWithOptions([]float32{1, 0}, 2, index.SearchOptions{EFSearch: 1, Allowed: allowed, AllowedCount: 2})
	if err != nil || len(results) != 2 || results[0].ID != 2 || results[1].ID != 3 {
		t.Fatalf("selective search = %#v, %v", results, err)
	}
	if _, err := idx.SearchWithOptions([]float32{1, 0}, 1, index.SearchOptions{EFSearch: 10001, AllowedCount: -1}); !errors.Is(err, core.ErrInvalidArgument) {
		t.Fatalf("ef_search validation error = %v", err)
	}
}

func BenchmarkSearch5Kx128(b *testing.B) {
	const dimension = 128
	idx, _ := New(index.Config{Dimension: dimension, Metric: core.MetricCosine, M: 16, EFConstruction: 100, EFSearch: 64})
	vectors := deterministicVectors(5000, dimension)
	for i, vector := range vectors {
		_ = idx.Upsert(uint64(i+1), vector)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = idx.Search(vectors[b.N%len(vectors)], 10)
	}
}

func BenchmarkBuild2Kx128(b *testing.B) {
	const dimension = 128
	vectors := deterministicVectors(2000, dimension)
	cfg := index.Config{Dimension: dimension, Metric: core.MetricCosine, M: 16, EFConstruction: 100, EFSearch: 64}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		idx, err := New(cfg)
		if err != nil {
			b.Fatal(err)
		}
		for i, vector := range vectors {
			if err := idx.Upsert(uint64(i+1), vector); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func deterministicVectors(count, dimension int) [][]float32 {
	rng := rand.New(rand.NewPCG(42, 7))
	vectors := make([][]float32, count)
	for i := range vectors {
		vectors[i] = make([]float32, dimension)
		for j := range vectors[i] {
			vectors[i][j] = rng.Float32()*2 - 1
		}
	}
	return vectors
}
