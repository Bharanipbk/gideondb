package hnsw

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
)

// parallelBuildThreshold keeps small graphs on the serial, fully
// deterministic path; below it, goroutine and lock overhead outweighs the gain.
const parallelBuildThreshold = 2048

// Build constructs an index from a batch of vectors. With workers > 1 and
// enough vectors, nodes are inserted concurrently with per-node locks, as in
// hnswlib. Node levels stay deterministic, but neighbour lists then depend on
// scheduling, so two parallel builds of the same data may differ slightly.
// workers <= 0 uses GOMAXPROCS.
func Build(cfg index.Config, ids []uint64, vectors [][]float32, workers int) (*Index, error) {
	if len(ids) != len(vectors) {
		return nil, fmt.Errorf("%w: ids and vectors differ in length", core.ErrInvalidArgument)
	}
	h, err := New(cfg)
	if err != nil {
		return nil, err
	}
	seen := make(map[uint64]struct{}, len(ids))
	for i, vector := range vectors {
		if len(vector) != h.dimension {
			return nil, core.ErrDimensionMismatch
		}
		if _, ok := seen[ids[i]]; ok {
			return nil, fmt.Errorf("%w: duplicate id %d", core.ErrInvalidArgument, ids[i])
		}
		seen[ids[i]] = struct{}{}
	}
	h.buildLocked(ids, vectors, workers)
	return h, nil
}

// buildLocked replaces the graph with one built from ids and vectors, which
// must be unique and of the index dimension.
func (h *Index) buildLocked(ids []uint64, vectors [][]float32, workers int) {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	count := len(ids)
	if workers <= 1 || count < parallelBuildThreshold {
		h.vectors, h.squaredNorms, h.nodes = nil, nil, nil
		h.construction = constructionScratch{}
		h.ordinals = make(map[uint64]int, count)
		h.entry, h.maxLevel, h.live = -1, -1, 0
		for i, id := range ids {
			_ = h.insertLocked(id, vectors[i])
		}
		return
	}

	// Store every vector and level first so workers only ever write
	// neighbour lists, which nodeLocks guard.
	h.vectors = make([]float32, 0, count*h.dimension)
	h.squaredNorms = make([]float32, count)
	h.nodes = make([]node, count)
	h.ordinals = make(map[uint64]int, count)
	for i, id := range ids {
		level := deterministicLevel(id, h.m)
		h.vectors = append(h.vectors, vectors[i]...)
		h.squaredNorms[i] = h.querySquaredNorm(vectors[i])
		h.nodes[i] = node{id: id, level: level, neighbors: make([][]int, level+1)}
		h.ordinals[id] = i
	}
	h.live = count
	h.construction = constructionScratch{visited: make([]uint32, count)}
	h.entry, h.maxLevel = 0, h.nodes[0].level
	h.nodeLocks = make([]sync.Mutex, count)
	defer func() { h.nodeLocks = nil }()

	var entryMu sync.Mutex
	var next atomic.Int64
	next.Store(1)
	var wg sync.WaitGroup
	for range min(workers, count-1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scratch := constructionScratch{visited: make([]uint32, count)}
			for {
				ordinal := int(next.Add(1) - 1)
				if ordinal >= count {
					return
				}
				level := h.nodes[ordinal].level
				entryMu.Lock()
				entry, maxLevel := h.entry, h.maxLevel
				// A node that raises the top level keeps the entry lock while it
				// links, so no other insert starts from a half-built top layer.
				// This happens about log_M(count) times per build.
				raises := level > maxLevel
				if !raises {
					entryMu.Unlock()
				}
				h.connectLocked(ordinal, entry, maxLevel, &scratch)
				if raises {
					h.entry, h.maxLevel = ordinal, level
					entryMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
}
