package collection

import (
	"fmt"
	"math/rand"
	"runtime"
	"testing"

	"github.com/vectordb/vectordb/internal/core"
)

func TestMergeTopKDeterministic(t *testing.T) {
	shards := [][]core.SearchResult{
		{{ID: "delta", Score: 4}, {ID: "charlie", Score: 2}},
		{{ID: "alpha", Score: 2}, {ID: "echo", Score: 1}},
		{{ID: "bravo", Score: 2}, {ID: "foxtrot", Score: 0}},
	}
	got := mergeTopK(shards, 4)
	want := []string{"delta", "alpha", "bravo", "charlie"}
	if len(got) != len(want) {
		t.Fatalf("result count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("result[%d] = %q, want %q; all=%#v", i, got[i].ID, want[i], got)
		}
	}
}

func TestParallelShardSearchReturnsGlobalTopK(t *testing.T) {
	c, err := New(core.CollectionConfig{Name: "parallel", Dimension: 2, Metric: core.MetricDot, ShardCount: 8, Index: core.IndexConfig{Type: core.IndexFlat}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		record := core.Record{ID: fmt.Sprintf("id-%03d", i), Vector: []float32{float32(i), 1}}
		if _, err := c.Upsert(record); err != nil {
			t.Fatal(err)
		}
	}
	results, err := c.Search("", []float32{1, 0}, 7)
	if err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		want := fmt.Sprintf("id-%03d", 99-i)
		if result.ID != want {
			t.Fatalf("result[%d] = %q, want %q", i, result.ID, want)
		}
	}
}

func BenchmarkParallelShardSearch8x8Kx32(b *testing.B) {
	c, err := New(core.CollectionConfig{Name: "bench", Dimension: 32, Metric: core.MetricDot, ShardCount: 8, Index: core.IndexConfig{Type: core.IndexFlat}})
	if err != nil {
		b.Fatal(err)
	}
	random := rand.New(rand.NewSource(1))
	for i := 0; i < 65536; i++ {
		vector := make([]float32, 32)
		for j := range vector {
			vector[j] = random.Float32()
		}
		if _, err := c.Upsert(core.Record{ID: fmt.Sprintf("id-%05d", i), Vector: vector}); err != nil {
			b.Fatal(err)
		}
	}
	query := make([]float32, 32)
	for i := range query {
		query[i] = random.Float32()
	}
	previous := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(previous)
	for _, workers := range []int{1, 8} {
		runtime.GOMAXPROCS(workers)
		b.Run(fmt.Sprintf("workers-%d", workers), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := c.Search("", query, 10); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMergeTopK8x10(b *testing.B) {
	perShard := make([][]core.SearchResult, 8)
	for shardID := range perShard {
		perShard[shardID] = make([]core.SearchResult, 10)
		for position := range perShard[shardID] {
			perShard[shardID][position] = core.SearchResult{ID: fmt.Sprintf("%d-%d", shardID, position), Score: float32(100 - position*8 - shardID)}
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = mergeTopK(perShard, 10)
	}
}
