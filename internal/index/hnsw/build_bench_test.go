package hnsw

import (
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
)

func benchmarkBuild(b *testing.B, workers int) {
	const dimension, count = 384, 10000
	vectors := clusteredVectors(42, count, dimension, 100, 32)
	ids := make([]uint64, count)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	cfg := index.Config{Dimension: dimension, Metric: core.MetricCosine, M: 16, EFConstruction: 100, EFSearch: 64}
	b.ResetTimer()
	for range b.N {
		if _, err := Build(cfg, ids, vectors, workers); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBuildSerial(b *testing.B)   { benchmarkBuild(b, 1) }
func BenchmarkBuildParallel(b *testing.B) { benchmarkBuild(b, 0) }
