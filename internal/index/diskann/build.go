// Package diskann implements a disk-resident Vamana graph index.
//
// The graph is built in memory when a segment is sealed and written once to
// a file of fixed-size per-node records holding the full-precision vector
// and its adjacency list. Serving maps that file read-only and keeps only
// 8-bit scalar codes (one byte per dimension) on the heap for navigation, so
// resident memory grows with the codes rather than the vectors. Every node
// a query expands is scored exactly from its on-disk record, which shares
// the page with the adjacency list that the expansion has to read anyway.
package diskann

import (
	"fmt"
	"math/rand"
	"runtime"
	"slices"
	"sort"
	"sync"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
)

// Params tune graph construction. MaxDegree is Vamana's R, BuildList its L,
// and Alpha the robust-prune distance factor (1.2 is the paper's default).
type Params struct {
	MaxDegree int
	BuildList int
	Alpha     float32
}

// Graph is a built Vamana graph over vectors addressed by position.
type Graph struct {
	Neighbors [][]uint32
	Medoid    uint32
}

type scored struct {
	node     uint32
	distance float32
}

// maxBatch caps an insertion batch so late inserts see recent edges.
const maxBatch = 1024

type builder struct {
	metric  core.Metric
	vectors [][]float32
	params  Params
	graph   [][]uint32
	medoid  uint32
}

// BuildGraph runs two Vamana passes (alpha 1, then params.Alpha) over a
// fixed random insertion order, then repairs reachability from the
// medoid. The result is deterministic for a given input; workers <= 0 uses
// GOMAXPROCS.
func BuildGraph(metric core.Metric, vectors [][]float32, params Params, workers int) (*Graph, error) {
	if !metric.Valid() || len(vectors) == 0 || len(vectors) > 1<<32-1 || params.MaxDegree < 1 || params.BuildList < 1 || params.Alpha < 1 {
		return nil, fmt.Errorf("%w: invalid diskann build configuration", core.ErrInvalidArgument)
	}
	dimension := len(vectors[0])
	for _, vector := range vectors {
		if len(vector) != dimension {
			return nil, core.ErrDimensionMismatch
		}
	}
	b := &builder{metric: metric, vectors: vectors, params: params, graph: make([][]uint32, len(vectors))}
	b.medoid = b.findMedoid()
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	order := rand.New(rand.NewSource(int64(len(vectors)))).Perm(len(vectors))
	for _, alpha := range []float32{1, params.Alpha} {
		// Insert in prefix-doubling batches: each batch searches and prunes
		// in parallel against the graph as it stood before the batch, then
		// its edges are applied serially in order, so the graph depends
		// only on the input and never on scheduling or the worker count.
		for start := 0; start < len(order); {
			batch := order[start:min(len(order), start+min(max(start, 1), maxBatch))]
			pruned := make([][]uint32, len(batch))
			next := make(chan int, len(batch))
			for i := range batch {
				next <- i
			}
			close(next)
			var wait sync.WaitGroup
			for range min(workers, len(batch)) {
				wait.Add(1)
				go func() {
					defer wait.Done()
					visited := newVisitSet(len(vectors))
					for i := range next {
						pruned[i] = b.prunedNeighbors(uint32(batch[i]), alpha, visited)
					}
				}()
			}
			wait.Wait()
			for i, node := range batch {
				b.link(uint32(node), pruned[i], alpha)
			}
			start += len(batch)
		}
	}
	b.connect()
	return &Graph{Neighbors: b.graph, Medoid: b.medoid}, nil
}

// distance is the non-negative construction distance. Dot-product graphs
// are built on Euclidean distance, since inner products violate the
// triangle-like assumption behind robust pruning; search still ranks by dot.
func (b *builder) distance(left, right []float32) float32 {
	if b.metric == core.MetricCosine {
		score, _ := distance.Score(core.MetricCosine, left, right)
		return 1 - score
	}
	score, _ := distance.Score(core.MetricL2, left, right)
	return -score
}

func (b *builder) findMedoid() uint32 {
	centroid := make([]float32, len(b.vectors[0]))
	for _, vector := range b.vectors {
		for i, value := range vector {
			centroid[i] += value
		}
	}
	for i := range centroid {
		centroid[i] /= float32(len(b.vectors))
	}
	best, bestDistance := uint32(0), float32(0)
	for position, vector := range b.vectors {
		if d := b.distance(centroid, vector); position == 0 || d < bestDistance {
			best, bestDistance = uint32(position), d
		}
	}
	return best
}

// greedySearch returns every node expanded while searching for target with
// a list of size BuildList, which is the candidate set Vamana prunes.
func (b *builder) greedySearch(target []float32, visited *visitSet) []scored {
	visited.reset()
	pool := []poolEntry{{node: b.medoid, distance: b.distance(target, b.vectors[b.medoid])}}
	visited.add(b.medoid)
	var expanded []scored
	for {
		next := -1
		for i := range pool {
			if !pool[i].expanded {
				next = i
				break
			}
		}
		if next < 0 {
			return expanded
		}
		pool[next].expanded = true
		node := pool[next].node
		expanded = append(expanded, scored{node, pool[next].distance})
		for _, neighbor := range b.graph[node] {
			if !visited.add(neighbor) {
				continue
			}
			pool = insertPool(pool, poolEntry{node: neighbor, distance: b.distance(target, b.vectors[neighbor])}, b.params.BuildList)
		}
	}
}

func (b *builder) prunedNeighbors(node uint32, alpha float32, visited *visitSet) []uint32 {
	candidates := b.greedySearch(b.vectors[node], visited)
	for _, neighbor := range b.graph[node] {
		candidates = append(candidates, scored{neighbor, b.distance(b.vectors[node], b.vectors[neighbor])})
	}
	return b.robustPrune(node, candidates, alpha)
}

// link installs node's pruned out-edges and the matching back edges.
func (b *builder) link(node uint32, pruned []uint32, alpha float32) {
	b.graph[node] = pruned
	for _, neighbor := range pruned {
		if slices.Contains(b.graph[neighbor], node) {
			continue
		}
		b.addEdge(neighbor, node, alpha)
	}
}

func (b *builder) addEdge(from, to uint32, alpha float32) {
	current := b.graph[from]
	if len(current) < b.params.MaxDegree {
		b.graph[from] = append(current, to)
		return
	}
	back := make([]scored, 0, len(current)+1)
	for _, existing := range append(current, to) {
		back = append(back, scored{existing, b.distance(b.vectors[from], b.vectors[existing])})
	}
	b.graph[from] = b.robustPrune(from, back, alpha)
}

// connect makes every node reachable from the medoid. Robust pruning can
// strip all in-edges from a far outlier (its neighbours keep it only as a
// back edge, then prune it as their farthest candidate), and search could
// then never return it. Each unreachable node gets an edge from its
// nearest reachable node, replacing that node's farthest neighbour when it
// is full; the loop repeats until a traversal reaches everything.
func (b *builder) connect() {
	for range len(b.vectors) {
		reached := b.reachable()
		missing := -1
		for node, ok := range reached {
			if !ok {
				missing = node
				break
			}
		}
		if missing < 0 {
			return
		}
		from, best := -1, float32(0)
		for node, ok := range reached {
			if d := b.distance(b.vectors[missing], b.vectors[node]); ok && (from < 0 || d < best) {
				from, best = node, d
			}
		}
		if neighbors := b.graph[from]; len(neighbors) < b.params.MaxDegree {
			b.graph[from] = append(neighbors, uint32(missing))
		} else {
			farthest := 0
			for i, neighbor := range neighbors {
				if b.distance(b.vectors[from], b.vectors[neighbor]) > b.distance(b.vectors[from], b.vectors[neighbors[farthest]]) {
					farthest = i
				}
			}
			neighbors[farthest] = uint32(missing)
		}
	}
}

func (b *builder) reachable() []bool {
	reached := make([]bool, len(b.vectors))
	reached[b.medoid] = true
	queue := []uint32{b.medoid}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, neighbor := range b.graph[node] {
			if !reached[neighbor] {
				reached[neighbor] = true
				queue = append(queue, neighbor)
			}
		}
	}
	return reached
}

// robustPrune keeps the closest candidate and drops every remaining one
// that it alpha-dominates, repeating until MaxDegree neighbours are kept.
func (b *builder) robustPrune(node uint32, candidates []scored, alpha float32) []uint32 {
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].distance < candidates[j].distance || (candidates[i].distance == candidates[j].distance && candidates[i].node < candidates[j].node)
	})
	unique := candidates[:0]
	for i, candidate := range candidates {
		if candidate.node == node || (i > 0 && candidate.node == candidates[i-1].node) {
			continue
		}
		unique = append(unique, candidate)
	}
	removed := make([]bool, len(unique))
	kept := make([]uint32, 0, b.params.MaxDegree)
	for i, candidate := range unique {
		if removed[i] {
			continue
		}
		kept = append(kept, candidate.node)
		if len(kept) == b.params.MaxDegree {
			break
		}
		for j := i + 1; j < len(unique); j++ {
			if !removed[j] && alpha*b.distance(b.vectors[candidate.node], b.vectors[unique[j].node]) <= unique[j].distance {
				removed[j] = true
			}
		}
	}
	return kept
}

type poolEntry struct {
	node     uint32
	distance float32
	expanded bool
}

// insertPool keeps pool sorted by ascending distance and at most limit long.
func insertPool(pool []poolEntry, entry poolEntry, limit int) []poolEntry {
	if len(pool) == limit && entry.distance >= pool[len(pool)-1].distance {
		return pool
	}
	at := sort.Search(len(pool), func(i int) bool { return pool[i].distance > entry.distance })
	if len(pool) < limit {
		pool = append(pool, poolEntry{})
	}
	copy(pool[at+1:], pool[at:len(pool)-1])
	pool[at] = entry
	return pool
}

// visitSet is a reusable bitset that clears only the words it touched.
type visitSet struct {
	words   []uint64
	touched []uint32
}

func newVisitSet(size int) *visitSet { return &visitSet{words: make([]uint64, (size+63)/64)} }

func (v *visitSet) add(node uint32) bool {
	word, bit := node/64, uint64(1)<<(node%64)
	if v.words[word]&bit != 0 {
		return false
	}
	if v.words[word] == 0 {
		v.touched = append(v.touched, word)
	}
	v.words[word] |= bit
	return true
}

func (v *visitSet) reset() {
	for _, word := range v.touched {
		v.words[word] = 0
	}
	v.touched = v.touched[:0]
}
