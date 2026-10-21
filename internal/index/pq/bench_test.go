package pq

import (
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func BenchmarkSearch50k(b *testing.B) {
	vectors := clusteredVectors(1, 50000, 100, 128, 0.6)
	index := build(b, vectors, core.MetricCosine, 16, 10)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = index.Search(vectors[i%1000], 10)
	}
}
