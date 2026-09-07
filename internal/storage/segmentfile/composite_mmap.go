package segmentfile

import (
	"fmt"
	"path/filepath"

	"github.com/Bharanipbk/gideondb/internal/core"
)

type VectorLocation struct {
	Segment int
	Ordinal int
}

// CompositeMappedVectors presents selected rows from multiple immutable vector
// columns as one logical source without copying vector payloads into Go memory.
type CompositeMappedVectors struct {
	sources   []*MappedVectors
	locations []VectorLocation
	dimension int
}

func OpenCompositeMappedVectors(directory string, refs []SegmentRef, locations []VectorLocation) (*CompositeMappedVectors, error) {
	if len(refs) == 0 {
		return nil, fmt.Errorf("composite mapping requires at least one segment")
	}
	result := &CompositeMappedVectors{sources: make([]*MappedVectors, 0, len(refs)), locations: append([]VectorLocation(nil), locations...), dimension: int(refs[0].Dimension)}
	for _, ref := range refs {
		if int(ref.Dimension) != result.dimension {
			_ = result.Close()
			return nil, fmt.Errorf("composite vector dimension mismatch")
		}
		source, err := OpenMappedVectors(filepath.Join(directory, ref.VectorsFile), ref.Dimension, ref.RecordCount, ref.MaxLSN)
		if err != nil {
			_ = result.Close()
			return nil, err
		}
		result.sources = append(result.sources, source)
	}
	for _, location := range result.locations {
		if location.Segment < 0 || location.Segment >= len(result.sources) || location.Ordinal < 0 || location.Ordinal >= result.sources[location.Segment].Len() {
			_ = result.Close()
			return nil, fmt.Errorf("invalid composite vector location")
		}
	}
	return result, nil
}

func (m *CompositeMappedVectors) Len() int       { return len(m.locations) }
func (m *CompositeMappedVectors) Dimension() int { return m.dimension }
func (m *CompositeMappedVectors) MappedBytes() uint64 {
	var total uint64
	for _, source := range m.sources {
		total += source.MappedBytes()
	}
	return total
}
func (m *CompositeMappedVectors) BeginRead() error {
	for position, source := range m.sources {
		if err := source.BeginRead(); err != nil {
			for acquired := position - 1; acquired >= 0; acquired-- {
				m.sources[acquired].EndRead()
			}
			return err
		}
	}
	return nil
}
func (m *CompositeMappedVectors) EndRead() {
	for position := len(m.sources) - 1; position >= 0; position-- {
		m.sources[position].EndRead()
	}
}
func (m *CompositeMappedVectors) Score(metric core.Metric, query []float32, ordinal int) (float32, error) {
	if err := m.BeginRead(); err != nil {
		return 0, err
	}
	defer m.EndRead()
	return m.ScoreAcquired(metric, query, ordinal)
}
func (m *CompositeMappedVectors) ScoreAcquired(metric core.Metric, query []float32, ordinal int) (float32, error) {
	if ordinal < 0 || ordinal >= len(m.locations) {
		return 0, fmt.Errorf("%w: composite vector ordinal out of range", core.ErrInvalidArgument)
	}
	location := m.locations[ordinal]
	return m.sources[location.Segment].ScoreAcquired(metric, query, location.Ordinal)
}
func (m *CompositeMappedVectors) ScoreAcquiredPrepared(metric core.Metric, query []float32, querySquaredNorm float32, ordinal int) (float32, error) {
	if ordinal < 0 || ordinal >= len(m.locations) {
		return 0, fmt.Errorf("%w: composite vector ordinal out of range", core.ErrInvalidArgument)
	}
	location := m.locations[ordinal]
	return m.sources[location.Segment].ScoreAcquiredPrepared(metric, query, querySquaredNorm, location.Ordinal)
}
func (m *CompositeMappedVectors) VectorCopy(ordinal int) ([]float32, error) {
	if ordinal < 0 || ordinal >= len(m.locations) {
		return nil, fmt.Errorf("%w: composite vector ordinal out of range", core.ErrInvalidArgument)
	}
	location := m.locations[ordinal]
	return m.sources[location.Segment].VectorCopy(location.Ordinal)
}
func (m *CompositeMappedVectors) Close() error {
	var first error
	for _, source := range m.sources {
		if err := source.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
