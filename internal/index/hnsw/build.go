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
	h.squaredNorms = make([]float32, 0, count)
	h.nodes = make([]node, 0, count)
	h.ordinals = make(map[uint64]int, count)
	h.construction = constructionScratch{}
	h.entry, h.maxLevel, h.live = -1, -1, 0
	for i, id := range ids {
		h.appendNodeLocked(id, vectors[i])
	}
	h.connectParallelLocked(0, workers)
}

// appendNodeLocked stores a vector and its level without linking it.
func (h *Index) appendNodeLocked(id uint64, vector []float32) int {
	level := deterministicLevel(id, h.m)
	ordinal := len(h.nodes)
	h.vectors = append(h.vectors, vector...)
	h.squaredNorms = append(h.squaredNorms, h.querySquaredNorm(vector))
	h.nodes = append(h.nodes, node{id: id, level: level, neighbors: make([][]int, level+1)})
	h.construction.visited = append(h.construction.visited, 0)
	h.ordinals[id] = ordinal
	h.live++
	return ordinal
}

// connectParallelLocked links the stored but unlinked nodes from first
// onwards into the graph using workers goroutines and per-node locks. The
// caller holds mu for writing, so searches wait until the batch is linked.
func (h *Index) connectParallelLocked(first, workers int) {
	count := len(h.nodes)
	if first >= count {
		return
	}
	if h.entry < 0 {
		h.entry, h.maxLevel = first, h.nodes[first].level
		first++
	}
	h.nodeLocks = make([]sync.Mutex, count)
	defer func() { h.nodeLocks = nil }()

	var entryMu sync.Mutex
	var next atomic.Int64
	next.Store(int64(first))
	var wg sync.WaitGroup
	for range min(workers, count-first) {
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

// UpsertBatch inserts or updates many vectors under one write lock. New
// vectors in a batch of at least parallelBuildThreshold are linked into the
// existing graph concurrently with per-node locks, the same scheme as a
// parallel Build, so bulk writes to a live segment use every core instead of
// one. Smaller batches, or workers == 1, insert serially and produce exactly
// the graph that the same sequence of Upsert calls would. As with Upsert,
// replacing an existing vector rebuilds the graph, once per batch. The graph
// layout and persisted format are unchanged. workers <= 0 uses GOMAXPROCS.
func (h *Index) UpsertBatch(ids []uint64, vectors [][]float32, workers int) error {
	if len(ids) != len(vectors) {
		return fmt.Errorf("%w: ids and vectors differ in length", core.ErrInvalidArgument)
	}
	for _, vector := range vectors {
		if len(vector) != h.dimension {
			return core.ErrDimensionMismatch
		}
	}
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.immutableGraph {
		return fmt.Errorf("%w: persisted hnsw graph is immutable", core.ErrInvalidArgument)
	}
	if workers <= 1 || len(ids) < parallelBuildThreshold {
		updated := false
		for i, id := range ids {
			if ordinal, ok := h.ordinals[id]; ok {
				copy(h.vectorAt(ordinal), vectors[i])
				updated = true
				continue
			}
			_ = h.insertLocked(id, vectors[i])
		}
		if updated {
			h.rebuildLocked()
		}
		return nil
	}
	first := len(h.nodes)
	updated := false
	for i, id := range ids {
		if ordinal, ok := h.ordinals[id]; ok {
			copy(h.vectorAt(ordinal), vectors[i])
			updated = true
			continue
		}
		h.appendNodeLocked(id, vectors[i])
	}
	if updated {
		// rebuildLocked recomputes every norm and relinks every node.
		h.rebuildLocked()
		return nil
	}
	h.connectParallelLocked(first, workers)
	return nil
}
