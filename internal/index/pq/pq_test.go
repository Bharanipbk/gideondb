package pq

import (
	"math/rand"
	"sync"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
	idx "github.com/Bharanipbk/gideondb/internal/index"
	"github.com/Bharanipbk/gideondb/internal/index/flat"
)

func clusteredVectors(seed int64, n, clusters, dimension int, spread float64) [][]float32 {
	random := rand.New(rand.NewSource(seed))
	centers := make([][]float32, clusters)
	for i := range centers {
		centers[i] = make([]float32, dimension)
		for j := range centers[i] {
			centers[i][j] = float32(random.NormFloat64())
		}
	}
	vectors := make([][]float32, n)
	for i := range vectors {
		center := centers[random.Intn(clusters)]
		vectors[i] = make([]float32, dimension)
		for j := range vectors[i] {
			vectors[i][j] = center[j] + float32(random.NormFloat64()*spread)
		}
	}
	return vectors
}

func build(t testing.TB, vectors [][]float32, metric core.Metric, subspaces, oversampling int) *Index {
	return buildPrefix(t, vectors, len(vectors), metric, subspaces, oversampling)
}

// buildPrefix trains and loads the first n vectors; the scorer sees them all.
func buildPrefix(t testing.TB, all [][]float32, n int, metric core.Metric, subspaces, oversampling int) *Index {
	t.Helper()
	vectors := all[:n]
	codebook, err := Train(vectors, len(vectors[0]), subspaces, metric)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(codebook, oversampling, func(query []float32, id uint64) (float32, error) {
		return distance.Score(metric, query, all[id-1])
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, vector := range vectors {
		if err := b.Upsert(uint64(i+1), vector); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func recall(t *testing.T, vectors [][]float32, metric core.Metric, quantized *Index, k int) float64 {
	t.Helper()
	dimension := len(vectors[0])
	exact, _ := flat.New(idx.Config{Dimension: dimension, Metric: metric})
	for i, vector := range vectors {
		_ = exact.Upsert(uint64(i+1), vector)
	}
	random := rand.New(rand.NewSource(99))
	hits, total := 0, 0
	for q := 0; q < 50; q++ {
		query := make([]float32, dimension)
		for j, value := range vectors[q*37] {
			query[j] = value + float32(random.NormFloat64()*0.3)
		}
		want, _ := exact.Search(query, k)
		got, err := quantized.Search(query, k)
		if err != nil {
			t.Fatal(err)
		}
		expected := map[uint64]bool{}
		for _, candidate := range want {
			expected[candidate.ID] = true
		}
		for _, candidate := range got {
			if expected[candidate.ID] {
				hits++
			}
		}
		total += len(want)
	}
	return float64(hits) / float64(total)
}

func TestRescoringRecallOnClusteredData(t *testing.T) {
	vectors := clusteredVectors(1, 4000, 40, 64, 0.6)
	for _, metric := range []core.Metric{core.MetricCosine, core.MetricDot, core.MetricL2} {
		got := recall(t, vectors, metric, build(t, vectors, metric, 16, 10), 10)
		if got < 0.95 {
			t.Fatalf("%s recall@10=%.3f, want >= 0.95", metric, got)
		}
	}
}

func TestTrainingIsDeterministic(t *testing.T) {
	vectors := clusteredVectors(2, 1500, 10, 16, 0.5)
	first, _ := Train(vectors, 16, 4, core.MetricL2)
	second, _ := Train(vectors, 16, 4, core.MetricL2)
	for i := range first.Centroids {
		if first.Centroids[i] != second.Centroids[i] {
			t.Fatalf("centroid %d differs", i)
		}
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	vectors := clusteredVectors(3, 1200, 8, 32, 0.5)
	codebook, _ := Train(vectors, 32, 8, core.MetricCosine)
	codes, err := codebook.EncodeAll(len(vectors), func(position int) ([]float32, error) { return vectors[position], nil })
	if err != nil {
		t.Fatal(err)
	}
	data, err := Marshal(codebook, codes, 42)
	if err != nil {
		t.Fatal(err)
	}
	decoded, decodedCodes, fingerprint, err := Unmarshal(data)
	if err != nil || fingerprint != 42 || decoded.Metric != core.MetricCosine || decoded.Subspaces != 8 || string(decodedCodes) != string(codes) {
		t.Fatalf("decoded=%+v fingerprint=%d err=%v", decoded.Subspaces, fingerprint, err)
	}
	for i := range codebook.Centroids {
		if codebook.Centroids[i] != decoded.Centroids[i] {
			t.Fatalf("centroid %d differs", i)
		}
	}
	data[len(data)/2] ^= 1
	if _, _, _, err := Unmarshal(data); err == nil {
		t.Fatal("corrupted file should be rejected")
	}
}

func TestDeleteAndFilter(t *testing.T) {
	vectors := clusteredVectors(4, 1100, 8, 16, 0.5)
	b := build(t, vectors, core.MetricL2, 4, 4)
	_ = b.Delete(1)
	_ = b.Delete(1100)
	if b.Len() != 1098 {
		t.Fatalf("len=%d", b.Len())
	}
	results, err := b.SearchFiltered(vectors[0], 5, func(id uint64) bool { return id%2 == 0 })
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.ID%2 != 0 || result.ID == 1100 {
			t.Fatalf("unexpected id %d", result.ID)
		}
	}
	if stats := b.Stats(); stats.QuantizedBytes == 0 || stats.Vectors != 1098 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	if _, err := Train([][]float32{{1, 2, 3}}, 3, 2, core.MetricL2); err == nil {
		t.Fatal("subspaces must divide dimension")
	}
	if _, err := New(nil, 1, nil); err == nil {
		t.Fatal("nil codebook accepted")
	}
}

func TestConcurrentUpsertDeleteSearch(t *testing.T) {
	vectors := clusteredVectors(5, 1500, 10, 16, 0.5)
	b := buildPrefix(t, vectors, 1024, core.MetricCosine, 4, 4)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 1024 + worker; i < len(vectors); i += 4 {
				_ = b.Upsert(uint64(i+1), vectors[i])
				_ = b.Delete(uint64(i - 1000))
				if _, err := b.Search(vectors[i], 5); err != nil {
					t.Error(err)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
}
