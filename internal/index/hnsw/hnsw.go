// Package hnsw implements an experimental hierarchical navigable small-world
// graph. Flat search remains the correctness oracle.
package hnsw

import (
	"fmt"
	"math/bits"
	"sort"
	"sync"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
	"github.com/Bharanipbk/gideondb/internal/index"
)

type node struct {
	id        uint64
	level     int
	deleted   bool
	neighbors [][]int
}

type Index struct {
	mu                     sync.RWMutex
	dimension              int
	metric                 core.Metric
	m                      int
	efConstruction         int
	efSearch               int
	vectors                []float32
	nodes                  []node
	ordinals               map[uint64]int
	entry                  int
	maxLevel               int
	live                   int
	constructionVisited    []uint32
	constructionGeneration uint32
	visitedPool            sync.Pool
}

const maxPooledVisitedEntries = 4096

func New(cfg index.Config) (*Index, error) {
	if cfg.Dimension <= 0 || !cfg.Metric.Valid() || cfg.M < 2 || cfg.EFConstruction < cfg.M || cfg.EFSearch < 1 {
		return nil, fmt.Errorf("%w: invalid hnsw configuration", core.ErrInvalidArgument)
	}
	h := &Index{
		dimension: cfg.Dimension, metric: cfg.Metric, m: cfg.M,
		efConstruction: cfg.EFConstruction, efSearch: cfg.EFSearch,
		ordinals: make(map[uint64]int), entry: -1, maxLevel: -1,
	}
	h.visitedPool.New = func() any { return make(map[int]struct{}, cfg.EFSearch*2) }
	return h, nil
}

func (h *Index) Upsert(id uint64, vector []float32) error {
	if len(vector) != h.dimension {
		return core.ErrDimensionMismatch
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if ordinal, ok := h.ordinals[id]; ok {
		copy(h.vectorAt(ordinal), vector)
		// Updating a vector invalidates its graph geometry. Rebuild deterministically
		// to preserve search quality; immutable segments avoid this cost later.
		h.rebuildLocked()
		return nil
	}
	return h.insertLocked(id, vector)
}

func (h *Index) insertLocked(id uint64, vector []float32) error {
	querySquaredNorm := h.querySquaredNorm(vector)
	level := deterministicLevel(id)
	ordinal := len(h.nodes)
	h.vectors = append(h.vectors, vector...)
	h.nodes = append(h.nodes, node{id: id, level: level, neighbors: make([][]int, level+1)})
	h.constructionVisited = append(h.constructionVisited, 0)
	h.ordinals[id] = ordinal
	h.live++
	if h.entry < 0 {
		h.entry, h.maxLevel = ordinal, level
		return nil
	}

	entry := h.entry
	for layer := h.maxLevel; layer > level; layer-- {
		entry = h.greedyLocked(vector, querySquaredNorm, entry, layer)
	}
	for layer := min(level, h.maxLevel); layer >= 0; layer-- {
		generation := h.nextConstructionGenerationLocked()
		candidates := h.searchLayerLocked(vector, querySquaredNorm, []int{entry}, h.efConstruction, layer, -1, nil, h.constructionVisited, generation, nil)
		selected := h.selectBestLocked(vector, candidates, h.m)
		h.nodes[ordinal].neighbors[layer] = append(h.nodes[ordinal].neighbors[layer], selected...)
		for _, neighbor := range selected {
			h.nodes[neighbor].neighbors[layer] = append(h.nodes[neighbor].neighbors[layer], ordinal)
			h.pruneLocked(neighbor, layer)
		}
		if len(candidates) > 0 {
			entry = candidates[0].ordinal
		}
	}
	if level > h.maxLevel {
		h.entry, h.maxLevel = ordinal, level
	}
	return nil
}

func (h *Index) Delete(id uint64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	ordinal, ok := h.ordinals[id]
	if !ok {
		return core.ErrNotFound
	}
	h.nodes[ordinal].deleted = true
	delete(h.ordinals, id)
	h.live--
	return nil
}

func (h *Index) Search(query []float32, k int) ([]index.Candidate, error) {
	return h.SearchFiltered(query, k, nil)
}

func (h *Index) SearchFiltered(query []float32, k int, allowed func(id uint64) bool) ([]index.Candidate, error) {
	if len(query) != h.dimension {
		return nil, core.ErrDimensionMismatch
	}
	if k <= 0 {
		return nil, fmt.Errorf("%w: k must be positive", core.ErrInvalidArgument)
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.live == 0 || h.entry < 0 {
		return nil, nil
	}
	entry := h.entry
	querySquaredNorm := h.querySquaredNorm(query)
	for layer := h.maxLevel; layer > 0; layer-- {
		entry = h.greedyLocked(query, querySquaredNorm, entry, layer)
	}
	ef := max(k, h.efSearch)
	visited := h.visitedPool.Get().(map[int]struct{})
	defer func() {
		if len(visited) > maxPooledVisitedEntries {
			return
		}
		clear(visited)
		h.visitedPool.Put(visited)
	}()
	accept := func(ordinal int) bool {
		n := h.nodes[ordinal]
		return !n.deleted && (allowed == nil || allowed(n.id))
	}
	found := h.searchLayerLocked(query, querySquaredNorm, []int{entry}, ef, 0, -1, accept, nil, 0, visited)
	result := make([]index.Candidate, 0, min(k, len(found)))
	for _, candidate := range found {
		n := h.nodes[candidate.ordinal]
		if n.deleted {
			continue
		}
		result = append(result, index.Candidate{ID: n.id, Score: candidate.score})
		if len(result) == k {
			break
		}
	}
	return result, nil
}

func (h *Index) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.live
}

func (h *Index) Stats() index.Stats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var edges uint64
	for _, n := range h.nodes {
		for _, neighbors := range n.neighbors {
			edges += uint64(len(neighbors))
		}
	}
	return index.Stats{
		Type: core.IndexHNSW, Vectors: h.live,
		VectorBytes: uint64(len(h.vectors)) * 4,
		GraphBytes:  edges * 8, GraphEdges: edges,
		Deleted: len(h.nodes) - h.live, MaximumLevel: h.maxLevel,
	}
}

func (h *Index) greedyLocked(query []float32, querySquaredNorm float32, entry, layer int) int {
	current := entry
	currentScore := h.scoreLocked(query, querySquaredNorm, current)
	for {
		changed := false
		for _, neighbor := range h.neighborsAt(current, layer) {
			score := h.scoreLocked(query, querySquaredNorm, neighbor)
			if score > currentScore || (score == currentScore && h.nodes[neighbor].id < h.nodes[current].id) {
				current, currentScore, changed = neighbor, score, true
			}
		}
		if !changed {
			return current
		}
	}
}

func (h *Index) searchLayerLocked(query []float32, querySquaredNorm float32, entries []int, ef, layer, excluded int, accept func(int) bool, visitedMarks []uint32, generation uint32, visited map[int]struct{}) []scoredOrdinal {
	if visitedMarks == nil && visited == nil {
		visited = make(map[int]struct{}, ef*2)
	}
	frontier := make(maxHeap, 0, ef)
	best := make(minHeap, 0, ef+1)
	for _, entry := range entries {
		if entry < 0 || entry == excluded {
			continue
		}
		scored := scoredOrdinal{ordinal: entry, score: h.scoreLocked(query, querySquaredNorm, entry)}
		maxPush(&frontier, scored)
		if accept == nil || accept(entry) {
			minPush(&best, scored)
		}
		if visitedMarks != nil {
			visitedMarks[entry] = generation
		} else {
			visited[entry] = struct{}{}
		}
	}
	for len(frontier) > 0 {
		candidate := maxPop(&frontier)
		if len(best) >= ef && candidate.score < best[0].score {
			break
		}
		for _, neighbor := range h.neighborsAt(candidate.ordinal, layer) {
			if neighbor == excluded {
				continue
			}
			if visitedMarks != nil {
				if visitedMarks[neighbor] == generation {
					continue
				}
				visitedMarks[neighbor] = generation
			} else {
				if _, ok := visited[neighbor]; ok {
					continue
				}
				visited[neighbor] = struct{}{}
			}
			scored := scoredOrdinal{ordinal: neighbor, score: h.scoreLocked(query, querySquaredNorm, neighbor)}
			if len(best) < ef || scored.score > best[0].score {
				maxPush(&frontier, scored)
				if accept == nil || accept(neighbor) {
					minPush(&best, scored)
					if len(best) > ef {
						minPop(&best)
					}
				}
			}
		}
	}
	result := append([]scoredOrdinal(nil), best...)
	sort.Slice(result, func(i, j int) bool {
		return result[i].score > result[j].score || (result[i].score == result[j].score && h.nodes[result[i].ordinal].id < h.nodes[result[j].ordinal].id)
	})
	return result
}

func (h *Index) selectBestLocked(query []float32, candidates []scoredOrdinal, limit int) []int {
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	result := make([]int, 0, len(candidates))
	for _, candidate := range candidates {
		if !h.nodes[candidate.ordinal].deleted {
			result = append(result, candidate.ordinal)
		}
	}
	return result
}

func (h *Index) pruneLocked(ordinal, layer int) {
	neighbors := h.nodes[ordinal].neighbors[layer]
	if len(neighbors) <= h.m {
		return
	}
	query := h.vectorAt(ordinal)
	querySquaredNorm := h.querySquaredNorm(query)
	sort.Slice(neighbors, func(i, j int) bool {
		a, b := h.scoreLocked(query, querySquaredNorm, neighbors[i]), h.scoreLocked(query, querySquaredNorm, neighbors[j])
		return a > b || (a == b && h.nodes[neighbors[i]].id < h.nodes[neighbors[j]].id)
	})
	h.nodes[ordinal].neighbors[layer] = neighbors[:h.m]
}

func (h *Index) rebuildLocked() {
	type item struct {
		id     uint64
		vector []float32
	}
	items := make([]item, 0, h.live)
	for ordinal, n := range h.nodes {
		if !n.deleted {
			items = append(items, item{id: n.id, vector: append([]float32(nil), h.vectorAt(ordinal)...)})
		}
	}
	h.vectors, h.nodes = nil, nil
	h.constructionVisited = h.constructionVisited[:0]
	h.ordinals = make(map[uint64]int, len(items))
	h.entry, h.maxLevel, h.live = -1, -1, 0
	for _, item := range items {
		_ = h.insertLocked(item.id, item.vector)
	}
}

func (h *Index) nextConstructionGenerationLocked() uint32 {
	h.constructionGeneration++
	if h.constructionGeneration == 0 {
		clear(h.constructionVisited)
		h.constructionGeneration = 1
	}
	return h.constructionGeneration
}

func (h *Index) querySquaredNorm(query []float32) float32 {
	if h.metric == core.MetricCosine {
		return distance.SquaredNorm(query)
	}
	return 0
}

func (h *Index) scoreLocked(query []float32, querySquaredNorm float32, ordinal int) float32 {
	score, _ := distance.ScoreWithQuerySquaredNorm(h.metric, query, querySquaredNorm, h.vectorAt(ordinal))
	return score
}

func (h *Index) vectorAt(ordinal int) []float32 {
	start := ordinal * h.dimension
	return h.vectors[start : start+h.dimension]
}

func (h *Index) neighborsAt(ordinal, layer int) []int {
	if layer > h.nodes[ordinal].level {
		return nil
	}
	return h.nodes[ordinal].neighbors[layer]
}

// deterministicLevel approximates a geometric distribution with p=1/2 and
// makes graph construction reproducible for tests and persisted rebuilds.
func deterministicLevel(id uint64) int {
	x := id + 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	x ^= x >> 31
	return min(bits.TrailingZeros64(x), 32)
}

type scoredOrdinal struct {
	ordinal int
	score   float32
}

type maxHeap []scoredOrdinal

func maxPush(h *maxHeap, value scoredOrdinal) {
	*h = append(*h, value)
	position := len(*h) - 1
	for position > 0 {
		parent := (position - 1) / 2
		if (*h)[parent].score >= (*h)[position].score {
			break
		}
		(*h)[parent], (*h)[position] = (*h)[position], (*h)[parent]
		position = parent
	}
}

func maxPop(h *maxHeap) scoredOrdinal {
	result := (*h)[0]
	last := len(*h) - 1
	(*h)[0] = (*h)[last]
	*h = (*h)[:last]
	position := 0
	for {
		left := position*2 + 1
		if left >= len(*h) {
			break
		}
		largest := left
		if right := left + 1; right < len(*h) && (*h)[right].score > (*h)[left].score {
			largest = right
		}
		if (*h)[position].score >= (*h)[largest].score {
			break
		}
		(*h)[position], (*h)[largest] = (*h)[largest], (*h)[position]
		position = largest
	}
	return result
}

type minHeap []scoredOrdinal

func minPush(h *minHeap, value scoredOrdinal) {
	*h = append(*h, value)
	position := len(*h) - 1
	for position > 0 {
		parent := (position - 1) / 2
		if (*h)[parent].score <= (*h)[position].score {
			break
		}
		(*h)[parent], (*h)[position] = (*h)[position], (*h)[parent]
		position = parent
	}
}

func minPop(h *minHeap) scoredOrdinal {
	result := (*h)[0]
	last := len(*h) - 1
	(*h)[0] = (*h)[last]
	*h = (*h)[:last]
	position := 0
	for {
		left := position*2 + 1
		if left >= len(*h) {
			break
		}
		smallest := left
		if right := left + 1; right < len(*h) && (*h)[right].score < (*h)[left].score {
			smallest = right
		}
		if (*h)[position].score <= (*h)[smallest].score {
			break
		}
		(*h)[position], (*h)[smallest] = (*h)[smallest], (*h)[position]
		position = smallest
	}
	return result
}
