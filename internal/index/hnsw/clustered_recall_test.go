package hnsw

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
	"github.com/Bharanipbk/gideondb/internal/index/flat"
)

// clusteredVectors mirrors the gideondb-bench synthetic generator: points lie
// near one of several well-separated topic centroids on a shared
// low-dimensional subspace. Without cross-cluster links in the graph, search
// cannot leave the cluster it enters and recall plateaus regardless of ef.
func clusteredVectors(seed int64, count, dimension, clusters, intrinsic int) [][]float32 {
	random := rand.New(rand.NewSource(seed))
	gaussian := func() []float32 {
		v := make([]float32, dimension)
		for i := range v {
			v[i] = float32(random.NormFloat64())
		}
		return v
	}
	centers := make([][]float32, clusters)
	for i := range centers {
		centers[i] = gaussian()
	}
	projection := make([][]float32, intrinsic)
	for i := range projection {
		projection[i] = gaussian()
	}
	scale := float32(0.7 / math.Sqrt(float64(intrinsic)))
	vectors := make([][]float32, count)
	for n := range vectors {
		v := append([]float32(nil), centers[random.Intn(clusters)]...)
		for _, row := range projection {
			weight := float32(random.NormFloat64()) * scale
			for i, value := range row {
				v[i] += weight * value
			}
		}
		for i, noise := range gaussian() {
			v[i] += 0.05 * noise
		}
		vectors[n] = v
	}
	return vectors
}

func clusteredRecall(t *testing.T, approx *Index, exact *flat.Index, queries [][]float32, k, ef int) float64 {
	t.Helper()
	var matches, total int
	for _, query := range queries {
		want, _ := exact.Search(query, k)
		got, err := approx.SearchWithOptions(query, k, index.SearchOptions{EFSearch: ef, AllowedCount: -1})
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
	return float64(matches) / float64(total)
}

func TestClusteredRecallImprovesWithEF(t *testing.T) {
	const dimension, count, queryCount, k = 64, 6000, 100, 10
	cfg := index.Config{Dimension: dimension, Metric: core.MetricCosine, M: 16, EFConstruction: 100, EFSearch: 64}
	approx, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	exact, _ := flat.New(cfg)
	all := clusteredVectors(7, count+queryCount, dimension, 60, 16)
	started := time.Now()
	for i, vector := range all[:count] {
		if err := approx.Upsert(uint64(i+1), vector); err != nil {
			t.Fatal(err)
		}
		_ = exact.Upsert(uint64(i+1), vector)
	}
	t.Logf("build %v", time.Since(started))
	queries := all[count:]
	low := clusteredRecall(t, approx, exact, queries, k, 64)
	high := clusteredRecall(t, approx, exact, queries, k, 256)
	t.Logf("recall@%d ef=64 %.3f ef=256 %.3f", k, low, high)
	if high < 0.95 {
		t.Fatalf("recall@%d with ef_search 256 = %.3f, want >= 0.95", k, high)
	}
	if high < low {
		t.Fatalf("recall fell when ef_search increased: %.3f -> %.3f", low, high)
	}
}

func TestLoadGraphAcceptsVersionOne(t *testing.T) {
	cfg := index.Config{Dimension: 4, Metric: core.MetricL2, M: 4, EFConstruction: 8, EFSearch: 8}
	original, _ := New(cfg)
	ids := []uint64{1, 2, 3}
	vectors := [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}}
	for i, id := range ids {
		_ = original.Upsert(id, vectors[i])
	}
	data, err := original.MarshalGraph()
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint16(data[4:6], 1)
	binary.LittleEndian.PutUint32(data[56:60], graphChecksum(data))
	loaded, err := LoadGraph(cfg, ids, vectors, data)
	if err != nil {
		t.Fatalf("version 1 graph: %v", err)
	}
	if loaded.Len() != 3 {
		t.Fatalf("len = %d", loaded.Len())
	}
}
