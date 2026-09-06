// Package flat implements exact brute-force vector search.
package flat

import (
	"fmt"
	"sync"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
	"github.com/Bharanipbk/gideondb/internal/index"
)

// Index stores vectors contiguously and maps stable caller IDs to ordinals.
// Deleted ordinals are reused on later inserts.
type Index struct {
	mu        sync.RWMutex
	dimension int
	metric    core.Metric
	vectors   []float32
	ids       []uint64
	ordinals  map[uint64]int
	free      []int
}

func New(cfg index.Config) (*Index, error) {
	if cfg.Dimension <= 0 || !cfg.Metric.Valid() {
		return nil, fmt.Errorf("%w: invalid flat index configuration", core.ErrInvalidArgument)
	}
	return &Index{dimension: cfg.Dimension, metric: cfg.Metric, ordinals: make(map[uint64]int)}, nil
}

func (f *Index) Upsert(id uint64, vector []float32) error {
	if len(vector) != f.dimension {
		return core.ErrDimensionMismatch
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if ordinal, ok := f.ordinals[id]; ok {
		copy(f.vectorAt(ordinal), vector)
		return nil
	}
	var ordinal int
	if n := len(f.free); n > 0 {
		ordinal = f.free[n-1]
		f.free = f.free[:n-1]
		copy(f.vectorAt(ordinal), vector)
		f.ids[ordinal] = id
	} else {
		ordinal = len(f.ids)
		f.vectors = append(f.vectors, vector...)
		f.ids = append(f.ids, id)
	}
	f.ordinals[id] = ordinal
	return nil
}

func (f *Index) Delete(id uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ordinal, ok := f.ordinals[id]
	if !ok {
		return core.ErrNotFound
	}
	delete(f.ordinals, id)
	f.ids[ordinal] = 0
	clear(f.vectorAt(ordinal))
	f.free = append(f.free, ordinal)
	return nil
}

func (f *Index) Search(query []float32, k int) ([]index.Candidate, error) {
	return f.SearchFiltered(query, k, nil)
}

func (f *Index) SearchFiltered(query []float32, k int, allowed func(id uint64) bool) ([]index.Candidate, error) {
	if len(query) != f.dimension {
		return nil, core.ErrDimensionMismatch
	}
	if k <= 0 {
		return nil, fmt.Errorf("%w: k must be positive", core.ErrInvalidArgument)
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	querySquaredNorm := float32(0)
	if f.metric == core.MetricCosine {
		querySquaredNorm = distance.SquaredNorm(query)
	}
	h := make(candidateHeap, 0, min(k, len(f.ordinals)))
	for ordinal, id := range f.ids {
		if id == 0 || (allowed != nil && !allowed(id)) {
			continue
		}
		score, err := distance.ScoreWithQuerySquaredNorm(f.metric, query, querySquaredNorm, f.vectorAt(ordinal))
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

func (f *Index) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.ordinals)
}

func (f *Index) Stats() index.Stats {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return index.Stats{
		Type: core.IndexFlat, Vectors: len(f.ordinals),
		VectorBytes: uint64(len(f.vectors)) * 4,
	}
}

func (f *Index) vectorAt(ordinal int) []float32 {
	start := ordinal * f.dimension
	return f.vectors[start : start+f.dimension]
}

func better(a, b index.Candidate) bool {
	return a.Score > b.Score || (a.Score == b.Score && a.ID < b.ID)
}

type candidateHeap []index.Candidate

func worse(a, b index.Candidate) bool { return better(b, a) }

func pushCandidate(h *candidateHeap, candidate index.Candidate) {
	*h = append(*h, candidate)
	position := len(*h) - 1
	for position > 0 {
		parent := (position - 1) / 2
		if !worse((*h)[position], (*h)[parent]) {
			break
		}
		(*h)[position], (*h)[parent] = (*h)[parent], (*h)[position]
		position = parent
	}
}

func fixCandidateRoot(h candidateHeap) {
	position := 0
	for {
		left := position*2 + 1
		if left >= len(h) {
			return
		}
		worst := left
		right := left + 1
		if right < len(h) && worse(h[right], h[left]) {
			worst = right
		}
		if !worse(h[worst], h[position]) {
			return
		}
		h[position], h[worst] = h[worst], h[position]
		position = worst
	}
}

func sortCandidates(candidates []index.Candidate) {
	for position := 1; position < len(candidates); position++ {
		candidate := candidates[position]
		insert := position
		for insert > 0 && better(candidate, candidates[insert-1]) {
			candidates[insert] = candidates[insert-1]
			insert--
		}
		candidates[insert] = candidate
	}
}
