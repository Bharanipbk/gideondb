package binary

import (
	"math/rand"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
	idx "github.com/Bharanipbk/gideondb/internal/index"
	"github.com/Bharanipbk/gideondb/internal/index/flat"
)

func randomVectors(seed int64, n, dimension int) [][]float32 {
	random := rand.New(rand.NewSource(seed))
	vectors := make([][]float32, n)
	for i := range vectors {
		vectors[i] = make([]float32, dimension)
		for j := range vectors[i] {
			vectors[i][j] = float32(random.NormFloat64())
		}
	}
	return vectors
}

// clusteredVectors mimics embedding structure: points scattered around a set
// of topic centroids. Pure random Gaussian data is a worst case for sign codes
// because nearest neighbours are barely closer than random points.
func clusteredVectors(seed int64, n, clusters, dimension int, spread float64) [][]float32 {
	random := rand.New(rand.NewSource(seed))
	centers := randomVectors(seed+1000, clusters, dimension)
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

func build(t testing.TB, vectors [][]float32, oversampling int) *Index {
	t.Helper()
	b, err := New(len(vectors[0]), oversampling, func(query []float32, id uint64) (float32, error) {
		return distance.Score(core.MetricCosine, query, vectors[id-1])
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

func TestEncodePacksSignBits(t *testing.T) {
	code := Encode([]float32{1, -1, 0, 2, 0.5}, 1)
	if code[0] != 0b11001 {
		t.Fatalf("code=%b", code[0])
	}
	if words := len(Encode(make([]float32, 130), 3)); words != 3 {
		t.Fatalf("words=%d", words)
	}
}

func TestRescoringRecoversExactTopK(t *testing.T) {
	const dimension, n, k = 128, 3000, 10
	vectors := clusteredVectors(1, n, 40, dimension, 0.6)
	exact, _ := flat.New(idx.Config{Dimension: dimension, Metric: core.MetricCosine})
	for i, vector := range vectors {
		_ = exact.Upsert(uint64(i+1), vector)
	}
	quantized := build(t, vectors, 10)
	queries := make([][]float32, 50)
	noise := randomVectors(2, len(queries), dimension)
	for i := range queries {
		queries[i] = make([]float32, dimension)
		for j, value := range vectors[i*37] {
			queries[i][j] = value + 0.3*noise[i][j]
		}
	}
	var hits, total int
	for _, query := range queries {
		want, _ := exact.Search(query, k)
		got, err := quantized.Search(query, k)
		if err != nil {
			t.Fatal(err)
		}
		truth := map[uint64]struct{}{}
		for _, c := range want {
			truth[c.ID] = struct{}{}
		}
		for i, c := range got {
			if _, ok := truth[c.ID]; ok {
				hits++
			}
			if i > 0 && got[i-1].Score < c.Score {
				t.Fatal("results must be sorted by rescored similarity")
			}
		}
		total += k
	}
	recall := float64(hits) / float64(total)
	t.Logf("recall@10=%.3f with oversampling 10", recall)
	if recall < 0.9 {
		t.Fatalf("recall@10=%.3f", recall)
	}
}

func TestFilterDeleteAndStats(t *testing.T) {
	vectors := randomVectors(3, 100, 64)
	b := build(t, vectors, 4)
	got, err := b.SearchFiltered(vectors[10], 5, func(id uint64) bool { return id%2 == 0 })
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if c.ID%2 != 0 {
			t.Fatalf("filter violated: %+v", c)
		}
	}
	_ = b.Delete(11)
	if got, _ := b.Search(vectors[10], 1); got[0].ID == 11 {
		t.Fatal("deleted vector returned")
	}
	stats := b.Stats()
	if stats.Vectors != 99 || stats.QuantizedBytes != 99*8 || b.Len() != 99 {
		t.Fatalf("stats=%+v", stats)
	}
	if _, err := b.Search(make([]float32, 3), 1); err == nil {
		t.Fatal("dimension mismatch should fail")
	}
}

func BenchmarkBinarySearch(b *testing.B) {
	vectors := randomVectors(4, 50000, 384)
	index := build(b, vectors, 4)
	query := vectors[7]
	b.ResetTimer()
	for range b.N {
		_, _ = index.Search(query, 10)
	}
}

func BenchmarkFlatSearch(b *testing.B) {
	vectors := randomVectors(4, 50000, 384)
	exact, _ := flat.New(idx.Config{Dimension: 384, Metric: core.MetricCosine})
	for i, vector := range vectors {
		_ = exact.Upsert(uint64(i+1), vector)
	}
	query := vectors[7]
	b.ResetTimer()
	for range b.N {
		_, _ = exact.Search(query, 10)
	}
}
