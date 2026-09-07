package engine

import (
	"fmt"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/storage/segmentfile"
)

func BenchmarkMaterializeFourSegments1Kx128(b *testing.B) {
	const segments, count, dimension = 4, 1000, 128
	directory := b.TempDir()
	refs := make([]segmentfile.SegmentRef, 0, segments)
	for segmentID := range segments {
		records := make([]core.Record, count)
		for position := range records {
			vector := make([]float32, dimension)
			for offset := range vector {
				vector[offset] = float32((position + offset + segmentID) % 17)
			}
			records[position] = core.Record{ID: fmt.Sprintf("%d-%d", segmentID, position), Vector: vector, Version: uint64(segmentID + 1)}
		}
		ref, err := writeSegmentRef(directory, fmt.Sprintf("segment-%d", segmentID), records, nil, dimension, uint64(segmentID+1))
		if err != nil {
			b.Fatal(err)
		}
		refs = append(refs, ref)
	}
	b.ReportAllocs()
	b.SetBytes(segments * count * dimension * 4)
	b.ResetTimer()
	for b.Loop() {
		records, _, err := materializeSegments(directory, refs)
		if err != nil || len(records) != segments*count {
			b.Fatalf("records=%d err=%v", len(records), err)
		}
	}
}
