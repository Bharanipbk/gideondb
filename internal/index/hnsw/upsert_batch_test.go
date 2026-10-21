package hnsw

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
	"github.com/Bharanipbk/gideondb/internal/index/flat"
)

func TestSerialUpsertBatchMatchesUpsertLoop(t *testing.T) {
	const dimension, count = 16, 600
	cfg := index.Config{Dimension: dimension, Metric: core.MetricCosine, M: 8, EFConstruction: 40, EFSearch: 40}
	vectors := clusteredVectors(5, count, dimension, 10, 6)
	ids := make([]uint64, count)
	looped, _ := New(cfg)
	for i, vector := range vectors {
		ids[i] = uint64(i + 1)
		_ = looped.Upsert(ids[i], vector)
	}
	batched, _ := New(cfg)
	// Two batches, so the second links into an existing graph.
	if err := batched.UpsertBatch(ids[:200], vectors[:200], 1); err != nil {
		t.Fatal(err)
	}
	if err := batched.UpsertBatch(ids[200:], vectors[200:], 8); err != nil {
		t.Fatal(err)
	}
	want, _ := looped.MarshalGraph()
	got, _ := batched.MarshalGraph()
	if !bytes.Equal(got, want) {
		t.Fatal("serial UpsertBatch built a different graph than Upsert")
	}
}

func TestParallelUpsertBatchIntoLiveGraph(t *testing.T) {
	const dimension, initial, added, queryCount, k = 48, 1500, 4500, 100, 10
	cfg := index.Config{Dimension: dimension, Metric: core.MetricL2, M: 16, EFConstruction: 100, EFSearch: 64}
	all := clusteredVectors(23, initial+added+queryCount, dimension, 50, 12)
	exact, _ := flat.New(cfg)
	live, _ := New(cfg)
	for i := range initial {
		_ = live.Upsert(uint64(i+1), all[i])
		_ = exact.Upsert(uint64(i+1), all[i])
	}
	ids := make([]uint64, added)
	for i := range ids {
		ids[i] = uint64(initial + i + 1)
		_ = exact.Upsert(ids[i], all[initial+i])
	}
	// Searches running alongside the batch must see either the old or the
	// new graph, never a partial one; the race detector checks the rest.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := live.Search(all[0], k); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	err := live.UpsertBatch(ids, all[initial:initial+added], 8)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if live.Len() != initial+added {
		t.Fatalf("len = %d", live.Len())
	}
	recall := clusteredRecall(t, live, exact, all[initial+added:], k, 128)
	t.Logf("live parallel batch recall@%d ef=128 %.3f", k, recall)
	if recall < 0.95 {
		t.Fatalf("recall@%d = %.3f, want >= 0.95", k, recall)
	}
	data, err := live.MarshalGraph()
	if err != nil {
		t.Fatal(err)
	}
	allIDs := make([]uint64, initial+added)
	for i := range allIDs {
		allIDs[i] = uint64(i + 1)
	}
	if _, err := LoadGraph(cfg, allIDs, all[:initial+added], data); err != nil {
		t.Fatalf("graph from a live batch does not load: %v", err)
	}
}

func TestUpsertBatchUpdatesAndValidates(t *testing.T) {
	const dimension, count = 8, parallelBuildThreshold + 10
	cfg := index.Config{Dimension: dimension, Metric: core.MetricL2, M: 8, EFConstruction: 32, EFSearch: 32}
	h, _ := New(cfg)
	vectors := clusteredVectors(9, count, dimension, 8, 4)
	ids := make([]uint64, count)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	// Id 7 appears twice; the later vector wins, as with two Upserts.
	ids = append(ids, 7)
	moved := append([]float32(nil), vectors[100]...)
	moved[0] += 0.001
	vectors = append(vectors, moved)
	if err := h.UpsertBatch(ids, vectors, 4); err != nil {
		t.Fatal(err)
	}
	if h.Len() != count {
		t.Fatalf("len = %d", h.Len())
	}
	got, err := h.Search(moved, 2)
	if err != nil || len(got) != 2 || got[0].ID != 7 || got[1].ID != 101 {
		t.Fatalf("search after batch update = %v, %v", got, err)
	}
	if err := h.UpsertBatch([]uint64{1}, nil, 1); !errors.Is(err, core.ErrInvalidArgument) {
		t.Fatalf("length mismatch: %v", err)
	}
	if err := h.UpsertBatch([]uint64{1}, [][]float32{{1}}, 1); !errors.Is(err, core.ErrDimensionMismatch) {
		t.Fatalf("dimension: %v", err)
	}
}

func benchmarkLiveBatch(b *testing.B, workers int) {
	const dimension, initial, added = 128, 2000, 8000
	vectors := clusteredVectors(42, initial+added, dimension, 100, 32)
	ids := make([]uint64, added)
	for i := range ids {
		ids[i] = uint64(initial + i + 1)
	}
	cfg := index.Config{Dimension: dimension, Metric: core.MetricL2, M: 16, EFConstruction: 100, EFSearch: 64}
	for b.Loop() {
		b.StopTimer()
		h, _ := New(cfg)
		for i := range initial {
			_ = h.Upsert(uint64(i+1), vectors[i])
		}
		b.StartTimer()
		if err := h.UpsertBatch(ids, vectors[initial:], workers); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLiveInsertSerial(b *testing.B)   { benchmarkLiveBatch(b, 1) }
func BenchmarkLiveInsertParallel(b *testing.B) { benchmarkLiveBatch(b, 0) }

// A node can gain reverse links from other workers before it appends its own
// selected neighbours, so its list must be pruned afterwards too. Otherwise
// it overflows the layer limit and LoadGraph rejects the persisted graph.
func TestParallelBuildRespectsNeighbourLimits(t *testing.T) {
	const dimension, count = 16, 20000
	cfg := index.Config{Dimension: dimension, Metric: core.MetricCosine, M: 4, EFConstruction: 16, EFSearch: 16}
	vectors := clusteredVectors(31, count, dimension, 5, 4)
	ids := make([]uint64, count)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	for round := range 3 {
		built, err := Build(cfg, ids, vectors, 16)
		if err != nil {
			t.Fatal(err)
		}
		for ordinal, n := range built.nodes {
			for layer, neighbors := range n.neighbors {
				if len(neighbors) > built.maxNeighbors(layer) {
					t.Fatalf("round %d: node %d layer %d has %d links, limit %d", round, ordinal, layer, len(neighbors), built.maxNeighbors(layer))
				}
			}
		}
		data, _ := built.MarshalGraph()
		if _, err := LoadGraph(cfg, ids, vectors, data); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}
