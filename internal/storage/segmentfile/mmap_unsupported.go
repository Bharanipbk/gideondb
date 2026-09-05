//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly)

package segmentfile

import (
	"fmt"

	"github.com/vectordb/vectordb/internal/core"
)

// MappedVectors keeps the public internal contract buildable on platforms
// where the standard library does not expose a supported mmap implementation.
type MappedVectors struct{}

func OpenMappedVectors(string, uint32, uint64, uint64) (*MappedVectors, error) {
	return nil, fmt.Errorf("mmap vector columns are unsupported on this platform")
}
func (*MappedVectors) Len() int            { return 0 }
func (*MappedVectors) Dimension() int      { return 0 }
func (*MappedVectors) MappedBytes() uint64 { return 0 }
func (*MappedVectors) Score(core.Metric, []float32, int) (float32, error) {
	return 0, fmt.Errorf("mmap unsupported")
}
func (*MappedVectors) BeginRead() error { return fmt.Errorf("mmap unsupported") }
func (*MappedVectors) EndRead()         {}
func (*MappedVectors) ScoreAcquired(core.Metric, []float32, int) (float32, error) {
	return 0, fmt.Errorf("mmap unsupported")
}
func (*MappedVectors) ScoreAcquiredPrepared(core.Metric, []float32, float32, int) (float32, error) {
	return 0, fmt.Errorf("mmap unsupported")
}
func (*MappedVectors) VectorCopy(int) ([]float32, error) { return nil, fmt.Errorf("mmap unsupported") }
func (*MappedVectors) Close() error                      { return nil }
