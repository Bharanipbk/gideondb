package flat

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
	"github.com/Bharanipbk/gideondb/internal/storage/segmentfile"
)

func TestMappedIndexMatchesHeapFlat(t *testing.T) {
	directory := t.TempDir()
	records := make([]core.Record, 200)
	ids := make([]uint64, len(records))
	heapIndex, _ := New(index.Config{Dimension: 8, Metric: core.MetricCosine})
	for position := range records {
		vector := make([]float32, 8)
		for offset := range vector {
			vector[offset] = float32((position + 1) * (offset + 3) % 17)
		}
		records[position] = core.Record{ID: fmt.Sprintf("record-%d", position), Vector: vector}
		ids[position] = uint64(position + 1)
		_ = heapIndex.Upsert(ids[position], vector)
	}
	manifest, err := segmentfile.WriteBundle(directory, "mapped", records, 8, 200)
	if err != nil {
		t.Fatal(err)
	}
	source, err := segmentfile.OpenMappedVectors(filepath.Join(directory, manifest.VectorsFile), 8, 200, 200)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	mappedIndex, err := NewMapped(source, core.MetricCosine, ids)
	if err != nil {
		t.Fatal(err)
	}
	stats := mappedIndex.Stats()
	if stats.MappedBytes == 0 || stats.VectorBytes != 0 {
		t.Fatalf("unexpected mapped accounting: %#v", stats)
	}
	query := records[73].Vector
	want, _ := heapIndex.Search(query, 20)
	got, err := mappedIndex.Search(query, 20)
	if err != nil {
		t.Fatal(err)
	}
	for position := range want {
		if got[position].ID != want[position].ID || got[position].Score != want[position].Score {
			t.Fatalf("candidate %d: mapped=%#v heap=%#v", position, got[position], want[position])
		}
	}
	filtered, err := mappedIndex.SearchFiltered(query, 10, func(id uint64) bool { return id%2 == 0 })
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range filtered {
		if candidate.ID%2 != 0 {
			t.Fatalf("filter admitted odd ID %d", candidate.ID)
		}
	}
	if err := mappedIndex.Upsert(999, query); err == nil {
		t.Fatal("mapped mutation should fail")
	}
}

func BenchmarkMappedSearch10Kx128(b *testing.B) {
	directory := b.TempDir()
	records := make([]core.Record, 10_000)
	ids := make([]uint64, len(records))
	for position := range records {
		vector := make([]float32, 128)
		for offset := range vector {
			vector[offset] = float32((position + offset) % 31)
		}
		records[position] = core.Record{ID: fmt.Sprintf("r-%d", position), Vector: vector}
		ids[position] = uint64(position + 1)
	}
	manifest, err := segmentfile.WriteBundle(directory, "benchmark", records, 128, 10_000)
	if err != nil {
		b.Fatal(err)
	}
	source, err := segmentfile.OpenMappedVectors(filepath.Join(directory, manifest.VectorsFile), 128, 10_000, 10_000)
	if err != nil {
		b.Fatal(err)
	}
	defer source.Close()
	idx, _ := NewMapped(source, core.MetricDot, ids)
	query := records[0].Vector
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := idx.Search(query, 10); err != nil {
			b.Fatal(err)
		}
	}
}
