package flat

import (
	"math/rand"
	"sort"
	"testing"

	"github.com/vectordb/vectordb/internal/core"
	"github.com/vectordb/vectordb/internal/index"
)

func TestTypedCandidateHeapSelectsDeterministicTopK(t *testing.T) {
	random := rand.New(rand.NewSource(11))
	all := make([]index.Candidate, 1000)
	for i := range all {
		all[i] = index.Candidate{ID: uint64(i + 1), Score: float32(random.Intn(25))}
	}
	want := append([]index.Candidate(nil), all...)
	sort.Slice(want, func(i, j int) bool { return better(want[i], want[j]) })
	want = want[:17]
	top := make(candidateHeap, 0, 17)
	for _, candidate := range all {
		if len(top) < 17 {
			pushCandidate(&top, candidate)
		} else if better(candidate, top[0]) {
			top[0] = candidate
			fixCandidateRoot(top)
		}
	}
	sortCandidates(top)
	for i := range want {
		if top[i] != want[i] {
			t.Fatalf("candidate[%d] = %#v, want %#v", i, top[i], want[i])
		}
	}
}

func TestIndexLifecycle(t *testing.T) {
	idx, err := New(index.Config{Dimension: 2, Metric: core.MetricDot})
	if err != nil {
		t.Fatal(err)
	}
	for id, vector := range map[uint64][]float32{
		1: {1, 0}, 2: {0, 1}, 3: {0.5, 0.5},
	} {
		if err := idx.Upsert(id, vector); err != nil {
			t.Fatal(err)
		}
	}
	results, err := idx.Search([]float32{1, 0}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].ID != 1 || results[1].ID != 3 {
		t.Fatalf("unexpected results: %#v", results)
	}
	if err := idx.Upsert(2, []float32{2, 0}); err != nil {
		t.Fatal(err)
	}
	results, _ = idx.Search([]float32{1, 0}, 1)
	if results[0].ID != 2 {
		t.Fatalf("upsert did not replace vector: %#v", results)
	}
	if err := idx.Delete(2); err != nil {
		t.Fatal(err)
	}
	results, _ = idx.Search([]float32{1, 0}, 1)
	if results[0].ID != 1 || idx.Len() != 2 {
		t.Fatalf("delete failed: %#v len=%d", results, idx.Len())
	}
}

func BenchmarkSearch10Kx128(b *testing.B) {
	benchmarkSearch10Kx128(b, core.MetricDot)
}

func BenchmarkCosineSearch10Kx128(b *testing.B) {
	benchmarkSearch10Kx128(b, core.MetricCosine)
}

func benchmarkSearch10Kx128(b *testing.B, metric core.Metric) {
	idx, _ := New(index.Config{Dimension: 128, Metric: metric})
	vector := make([]float32, 128)
	for id := uint64(1); id <= 10_000; id++ {
		for j := range vector {
			vector[j] = float32((int(id) + j) % 17)
		}
		_ = idx.Upsert(id, vector)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = idx.Search(vector, 10)
	}
}
