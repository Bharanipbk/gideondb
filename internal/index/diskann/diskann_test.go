package diskann

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
	"github.com/Bharanipbk/gideondb/internal/index/flat"
)

// clusteredVectors mirrors the gideondb-bench synthetic generator.
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

var testParams = Params{MaxDegree: 32, BuildList: 64, Alpha: 1.2}

func build(t testing.TB, metric core.Metric, vectors [][]float32, tag uint64) (string, *Index) {
	t.Helper()
	graph, err := BuildGraph(metric, vectors, testParams, 0)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "segment.diskann")
	if err := WriteFile(path, metric, vectors, graph, testParams.MaxDegree, tag); err != nil {
		t.Fatal(err)
	}
	x, err := Open(path, metric, len(vectors[0]), len(vectors), tag, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = x.Close() })
	return path, x
}

func exactIndex(t testing.TB, metric core.Metric, vectors [][]float32) *flat.Index {
	exact, err := flat.New(index.Config{Dimension: len(vectors[0]), Metric: metric})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vectors {
		if err := exact.Upsert(uint64(i+1), v); err != nil {
			t.Fatal(err)
		}
	}
	return exact
}

func recall(t testing.TB, x *Index, exact *flat.Index, queries [][]float32, k, ef int, allowed func(uint64) bool, allowedCount int) float64 {
	var matches, total int
	for _, query := range queries {
		want, _ := exact.SearchFiltered(query, k, allowed)
		got, err := x.SearchWithOptions(query, k, index.SearchOptions{EFSearch: ef, Allowed: allowed, AllowedCount: allowedCount})
		if err != nil {
			t.Fatal(err)
		}
		expected := map[uint64]bool{}
		for _, c := range want {
			expected[c.ID] = true
		}
		for _, c := range got {
			if allowed != nil && !allowed(c.ID) {
				t.Fatalf("result %d violates filter", c.ID)
			}
			if expected[c.ID] {
				matches++
			}
		}
		total += len(want)
	}
	return float64(matches) / float64(total)
}

func TestClusteredRecall(t *testing.T) {
	for _, metric := range []core.Metric{core.MetricCosine, core.MetricL2, core.MetricDot} {
		t.Run(string(metric), func(t *testing.T) {
			vectors := clusteredVectors(7, 10000, 64, 50, 16)
			queries := clusteredVectors(8, 100, 64, 50, 16)
			_, x := build(t, metric, vectors, 1)
			exact := exactIndex(t, metric, vectors)
			// Inner-product search is harder to navigate on a graph built
			// for Euclidean neighbourhoods, so dot gets a wider list.
			required := 128
			if metric == core.MetricDot {
				required = 256
			}
			for _, ef := range []int{32, 64, 128, 256} {
				r := recall(t, x, exact, queries, 10, ef, nil, -1)
				t.Logf("%s ef=%d recall@10=%.3f", metric, ef, r)
				if ef == required && r < 0.95 {
					t.Fatalf("recall@10 = %.3f at ef=%d, want >= 0.95", r, ef)
				}
			}
		})
	}
}

func TestReopenRoundTrip(t *testing.T) {
	vectors := clusteredVectors(3, 2000, 32, 20, 8)
	path, x := build(t, core.MetricL2, vectors, 42)
	query := vectors[17]
	before, err := x.Search(query, 5)
	if err != nil {
		t.Fatal(err)
	}
	if before[0].ID != 18 || before[0].Score != 0 {
		t.Fatalf("self query top = %+v", before[0])
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Search(query, 5); err == nil {
		t.Fatal("search after close succeeded")
	}
	reopened, err := Open(path, core.MetricL2, 32, 2000, 42, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.Search(query, 5)
	if err != nil {
		t.Fatal(err)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("result %d changed after reopen: %+v != %+v", i, before[i], after[i])
		}
	}
	copied, err := reopened.VectorCopy(17)
	if err != nil {
		t.Fatal(err)
	}
	for i := range copied {
		if copied[i] != vectors[17][i] {
			t.Fatal("vector copy differs from the written vector")
		}
	}
	if stats := reopened.Stats(); stats.Type != core.IndexDiskANN || stats.Vectors != 2000 || stats.QuantizedBytes != 2000*32 || stats.MappedBytes == 0 {
		t.Fatalf("stats = %+v", stats)
	}
	for _, mismatch := range []struct {
		metric           core.Metric
		dimension, count int
		tag              uint64
	}{{core.MetricDot, 32, 2000, 42}, {core.MetricL2, 16, 2000, 42}, {core.MetricL2, 32, 1999, 42}, {core.MetricL2, 32, 2000, 7}} {
		if other, err := Open(path, mismatch.metric, mismatch.dimension, mismatch.count, mismatch.tag, 64); err == nil {
			other.Close()
			t.Fatalf("open with %+v succeeded", mismatch)
		}
	}
}

func TestRejectsCorruptFiles(t *testing.T) {
	vectors := clusteredVectors(4, 300, 16, 4, 4)
	path, x := build(t, core.MetricCosine, vectors, 1)
	x.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func([]byte) []byte{
		"payload bit flip": func(b []byte) []byte { b[len(b)-3] ^= 1; return b },
		"future version":   func(b []byte) []byte { b[4] = 2; return b },
		"truncated":        func(b []byte) []byte { return b[:len(b)-4] },
		"bad magic":        func(b []byte) []byte { b[0] = 'X'; return b },
	}
	for name, corrupt := range cases {
		corrupted := filepath.Join(t.TempDir(), "corrupt.diskann")
		if err := os.WriteFile(corrupted, corrupt(append([]byte(nil), data...)), 0o600); err != nil {
			t.Fatal(err)
		}
		if other, err := Open(corrupted, core.MetricCosine, 16, 300, 1, 64); err == nil {
			other.Close()
			t.Fatalf("%s: open succeeded", name)
		}
	}
}

func TestFilteredSearch(t *testing.T) {
	vectors := clusteredVectors(5, 5000, 32, 25, 8)
	queries := clusteredVectors(6, 50, 32, 25, 8)
	_, x := build(t, core.MetricCosine, vectors, 1)
	exact := exactIndex(t, core.MetricCosine, vectors)
	for _, modulus := range []uint64{2, 10, 1000} {
		allowed := func(id uint64) bool { return id%modulus == 0 }
		count := int(uint64(len(vectors)) / modulus)
		for _, allowedCount := range []int{count, -1} {
			r := recall(t, x, exact, queries, 10, 128, allowed, allowedCount)
			t.Logf("1/%d allowed (count %d) recall@10=%.3f", modulus, allowedCount, r)
			if r < 0.9 {
				t.Fatalf("filtered recall@10 = %.3f for 1/%d allowed", r, modulus)
			}
		}
	}
	none, err := x.SearchFiltered(queries[0], 10, func(uint64) bool { return false })
	if err != nil || len(none) != 0 {
		t.Fatalf("empty filter returned %v, %v", none, err)
	}
}

func TestConcurrentSearchAndClose(t *testing.T) {
	vectors := clusteredVectors(9, 2000, 16, 10, 4)
	_, x := build(t, core.MetricL2, vectors, 1)
	var wait sync.WaitGroup
	start := make(chan struct{})
	for worker := range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for i := range 50 {
				query := vectors[(worker*50+i)%len(vectors)]
				if _, err := x.SearchWithOptions(query, 10, index.SearchOptions{EFSearch: 32 + i, AllowedCount: -1}); err != nil {
					return // closed underneath us
				}
				if _, err := x.VectorCopy(i); err != nil {
					return
				}
			}
		}()
	}
	close(start)
	if err := x.Close(); err != nil { // races the searches; they must fail cleanly
		t.Fatal(err)
	}
	wait.Wait()
}

func TestBuildValidation(t *testing.T) {
	if _, err := BuildGraph(core.MetricL2, nil, testParams, 1); err == nil {
		t.Fatal("empty build succeeded")
	}
	if _, err := BuildGraph(core.MetricL2, [][]float32{{1, 2}, {1}}, testParams, 1); err == nil {
		t.Fatal("ragged build succeeded")
	}
	graph, err := BuildGraph(core.MetricL2, [][]float32{{1, 2}}, testParams, 1)
	if err != nil || len(graph.Neighbors) != 1 || len(graph.Neighbors[0]) != 0 {
		t.Fatalf("single vector graph = %+v, %v", graph, err)
	}
	for node, neighbors := range mustGraph(t).Neighbors {
		if len(neighbors) > testParams.MaxDegree {
			t.Fatalf("node %d degree %d exceeds R", node, len(neighbors))
		}
		for _, neighbor := range neighbors {
			if int(neighbor) == node {
				t.Fatalf("node %d links to itself", node)
			}
		}
	}
}

func mustGraph(t *testing.T) *Graph {
	graph, err := BuildGraph(core.MetricCosine, clusteredVectors(1, 1000, 16, 5, 4), testParams, 4)
	if err != nil {
		t.Fatal(err)
	}
	return graph
}
