// Package binary implements a 1-bit (sign) quantized exact-rescoring index.
//
// Each vector is reduced to one bit per dimension (value > 0). Search ranks
// all allowed codes by Hamming distance, keeps Oversampling*k candidates, and
// rescores only those at full precision through a caller-supplied scorer.
// Codes use dimension/8 bytes per vector, 32x smaller than float32. Sign codes
// approximate angular similarity, so the index suits cosine and dot-product
// embeddings that are roughly zero-centered; recall should be measured with
// gideondb-bench before enabling it for a workload.
package binary

import (
	"container/heap"
	"fmt"
	"math/bits"
	"sort"
	"sync"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
)

// Scorer returns the full-precision similarity between query and vector id
// using the larger-is-better convention of distance.Score.
type Scorer func(query []float32, id uint64) (float32, error)

type Index struct {
	mu           sync.RWMutex
	dimension    int
	words        int
	oversampling int
	codes        map[uint64][]uint64
	score        Scorer
	vectorBytes  uint64
}

func New(dimension, oversampling int, score Scorer) (*Index, error) {
	if dimension <= 0 || oversampling < 1 || score == nil {
		return nil, fmt.Errorf("%w: invalid binary index configuration", core.ErrInvalidArgument)
	}
	return &Index{dimension: dimension, words: (dimension + 63) / 64, oversampling: oversampling, codes: map[uint64][]uint64{}, score: score}, nil
}

// Encode packs the sign bit of each component, least-significant bit first.
func Encode(vector []float32, words int) []uint64 {
	code := make([]uint64, words)
	for position, value := range vector {
		if value > 0 {
			code[position/64] |= 1 << (position % 64)
		}
	}
	return code
}

func (b *Index) Upsert(id uint64, vector []float32) error {
	if len(vector) != b.dimension {
		return core.ErrDimensionMismatch
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.codes[id]; !exists {
		b.vectorBytes += uint64(b.dimension) * 4
	}
	b.codes[id] = Encode(vector, b.words)
	return nil
}

func (b *Index) Delete(id uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.codes[id]; exists {
		delete(b.codes, id)
		b.vectorBytes -= uint64(b.dimension) * 4
	}
	return nil
}

func (b *Index) Search(query []float32, k int) ([]index.Candidate, error) {
	return b.SearchFiltered(query, k, nil)
}

func (b *Index) SearchFiltered(query []float32, k int, allowed func(uint64) bool) ([]index.Candidate, error) {
	if len(query) != b.dimension {
		return nil, core.ErrDimensionMismatch
	}
	if k <= 0 {
		return nil, fmt.Errorf("%w: k must be positive", core.ErrInvalidArgument)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	queryCode := Encode(query, b.words)
	limit := k * b.oversampling
	nearest := &hammingHeap{}
	for id, code := range b.codes {
		if allowed != nil && !allowed(id) {
			continue
		}
		distance := 0
		for word, bitsValue := range code {
			distance += bits.OnesCount64(bitsValue ^ queryCode[word])
		}
		if nearest.Len() < limit {
			heap.Push(nearest, hammingCandidate{id, distance})
		} else if top := (*nearest)[0]; distance < top.distance || (distance == top.distance && id < top.id) {
			(*nearest)[0] = hammingCandidate{id, distance}
			heap.Fix(nearest, 0)
		}
	}
	candidates := make([]index.Candidate, 0, nearest.Len())
	for _, candidate := range *nearest {
		score, err := b.score(query, candidate.id)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, index.Candidate{ID: candidate.id, Score: score})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score || (candidates[i].Score == candidates[j].Score && candidates[i].ID < candidates[j].ID)
	})
	if len(candidates) > k {
		candidates = candidates[:k]
	}
	return candidates, nil
}

func (b *Index) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.codes)
}

func (b *Index) Stats() index.Stats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return index.Stats{Type: core.IndexFlat, Vectors: len(b.codes), VectorBytes: b.vectorBytes, QuantizedBytes: uint64(len(b.codes)) * uint64(b.words) * 8}
}

type hammingCandidate struct {
	id       uint64
	distance int
}

// hammingHeap is a max-heap on distance so the worst kept candidate is at
// the root and can be replaced in O(log n).
type hammingHeap []hammingCandidate

func (h hammingHeap) Len() int { return len(h) }
func (h hammingHeap) Less(i, j int) bool {
	return h[i].distance > h[j].distance || (h[i].distance == h[j].distance && h[i].id > h[j].id)
}
func (h hammingHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *hammingHeap) Push(x any)   { *h = append(*h, x.(hammingCandidate)) }
func (h *hammingHeap) Pop() any {
	old := *h
	item := old[len(old)-1]
	*h = old[:len(old)-1]
	return item
}
