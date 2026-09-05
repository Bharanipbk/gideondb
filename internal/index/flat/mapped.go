package flat

import (
	"fmt"

	"github.com/vectordb/vectordb/internal/core"
	"github.com/vectordb/vectordb/internal/distance"
	"github.com/vectordb/vectordb/internal/index"
)

type MappedSource interface {
	Len() int
	Dimension() int
	MappedBytes() uint64
	Score(metric core.Metric, query []float32, ordinal int) (float32, error)
	BeginRead() error
	EndRead()
	ScoreAcquired(metric core.Metric, query []float32, ordinal int) (float32, error)
	ScoreAcquiredPrepared(metric core.Metric, query []float32, querySquaredNorm float32, ordinal int) (float32, error)
	VectorCopy(ordinal int) ([]float32, error)
}

// MappedIndex is an immutable exact index whose vectors remain outside the Go
// heap. IDs are supplied in record-column order.
type MappedIndex struct {
	source MappedSource
	metric core.Metric
	ids    []uint64
}

func NewMapped(source MappedSource, metric core.Metric, ids []uint64) (*MappedIndex, error) {
	if source == nil || !metric.Valid() || source.Dimension() <= 0 || source.Len() != len(ids) {
		return nil, fmt.Errorf("%w: invalid mapped index configuration", core.ErrInvalidArgument)
	}
	seen := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, fmt.Errorf("%w: mapped index IDs must be nonzero", core.ErrInvalidArgument)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("%w: duplicate mapped index ID %d", core.ErrInvalidArgument, id)
		}
		seen[id] = struct{}{}
	}
	return &MappedIndex{source: source, metric: metric, ids: append([]uint64(nil), ids...)}, nil
}

func (m *MappedIndex) Upsert(uint64, []float32) error { return fmt.Errorf("mapped index is immutable") }
func (m *MappedIndex) Delete(uint64) error            { return fmt.Errorf("mapped index is immutable") }
func (m *MappedIndex) Search(query []float32, k int) ([]index.Candidate, error) {
	return m.SearchFiltered(query, k, nil)
}
func (m *MappedIndex) SearchFiltered(query []float32, k int, allowed func(uint64) bool) ([]index.Candidate, error) {
	if len(query) != m.source.Dimension() {
		return nil, core.ErrDimensionMismatch
	}
	if k <= 0 {
		return nil, fmt.Errorf("%w: k must be positive", core.ErrInvalidArgument)
	}
	if err := m.source.BeginRead(); err != nil {
		return nil, err
	}
	defer m.source.EndRead()
	querySquaredNorm := float32(0)
	if m.metric == core.MetricCosine {
		querySquaredNorm = distance.SquaredNorm(query)
	}
	h := make(candidateHeap, 0, min(k, len(m.ids)))
	for ordinal, id := range m.ids {
		if allowed != nil && !allowed(id) {
			continue
		}
		score, err := m.source.ScoreAcquiredPrepared(m.metric, query, querySquaredNorm, ordinal)
		if err != nil {
			return nil, err
		}
		candidate := index.Candidate{ID: id, Score: score}
		if len(h) < k {
			pushCandidate(&h, candidate)
		} else if better(candidate, h[0]) {
			h[0] = candidate
			fixCandidateRoot(h)
		}
	}
	sortCandidates(h)
	return h, nil
}
func (m *MappedIndex) Len() int { return len(m.ids) }
func (m *MappedIndex) Stats() index.Stats {
	return index.Stats{Type: core.IndexFlat, Vectors: len(m.ids), MappedBytes: m.source.MappedBytes()}
}
