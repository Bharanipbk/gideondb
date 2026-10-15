package hnsw

import (
	"errors"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
	"github.com/Bharanipbk/gideondb/internal/index/flat"
)

func TestParallelBuildRecallAndRoundTrip(t *testing.T) {
	const dimension, count, queryCount, k = 64, 6000, 100, 10
	cfg := index.Config{Dimension: dimension, Metric: core.MetricCosine, M: 16, EFConstruction: 100, EFSearch: 64}
	all := clusteredVectors(11, count+queryCount, dimension, 60, 16)
	ids := make([]uint64, count)
	exact, _ := flat.New(cfg)
	for i := range ids {
		ids[i] = uint64(i + 1)
		_ = exact.Upsert(ids[i], all[i])
	}
	built, err := Build(cfg, ids, all[:count], 8)
	if err != nil {
		t.Fatal(err)
	}
	if built.Len() != count {
		t.Fatalf("len = %d", built.Len())
	}
	recall := clusteredRecall(t, built, exact, all[count:], k, 128)
	t.Logf("parallel build recall@%d ef=128 %.3f", k, recall)
	if recall < 0.95 {
		t.Fatalf("recall@%d = %.3f, want >= 0.95", k, recall)
	}
	data, err := built.MarshalGraph()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadGraph(cfg, ids, all[:count], data)
	if err != nil {
		t.Fatal(err)
	}
	if got := clusteredRecall(t, loaded, exact, all[count:], k, 128); got != recall {
		t.Fatalf("loaded recall %.3f, built recall %.3f", got, recall)
	}
}

func TestParallelRebuildOnUpdate(t *testing.T) {
	const dimension, count = 32, parallelBuildThreshold + 100
	cfg := index.Config{Dimension: dimension, Metric: core.MetricL2, M: 8, EFConstruction: 50, EFSearch: 50}
	h, _ := New(cfg)
	vectors := clusteredVectors(3, count, dimension, 20, 8)
	for i, vector := range vectors {
		_ = h.Upsert(uint64(i+1), vector)
	}
	// Move vector 5 next to vector 1001; search there must now return both.
	moved := append([]float32(nil), vectors[1000]...)
	moved[0] += 0.001
	if err := h.Upsert(5, moved); err != nil {
		t.Fatal(err)
	}
	got, err := h.Search(moved, 2)
	if err != nil || len(got) != 2 || got[0].ID != 5 || got[1].ID != 1001 {
		t.Fatalf("search after rebuild = %v, %v", got, err)
	}
	if h.Len() != count {
		t.Fatalf("len = %d", h.Len())
	}
}

func TestBuildRejectsInvalidInput(t *testing.T) {
	cfg := index.Config{Dimension: 2, Metric: core.MetricL2, M: 4, EFConstruction: 8, EFSearch: 8}
	if _, err := Build(cfg, []uint64{1, 1}, [][]float32{{1, 0}, {0, 1}}, 2); !errors.Is(err, core.ErrInvalidArgument) {
		t.Fatalf("duplicate ids: %v", err)
	}
	if _, err := Build(cfg, []uint64{1}, [][]float32{{1, 0, 0}}, 2); !errors.Is(err, core.ErrDimensionMismatch) {
		t.Fatalf("dimension: %v", err)
	}
	if _, err := Build(cfg, []uint64{1}, nil, 2); !errors.Is(err, core.ErrInvalidArgument) {
		t.Fatalf("length mismatch: %v", err)
	}
	empty, err := Build(cfg, nil, nil, 2)
	if err != nil || empty.Len() != 0 {
		t.Fatalf("empty build: %v", err)
	}
}
