package hnsw

import (
	"math/rand/v2"
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
