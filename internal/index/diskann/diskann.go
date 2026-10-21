package diskann

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"sync"
	"unsafe"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
	"github.com/Bharanipbk/gideondb/internal/index"
)

// Index serves a DiskANN file. IDs are ordinal+1, matching segment record
// ordinals. It is immutable; Close must follow the last search.
type Index struct {
	mu         sync.RWMutex
	mapped     []byte
	unmap      func([]byte) error
	records    []byte
	floats     []float32
	header     fileHeader
	recordSize int
	codes      []uint8
	minimums   []float32
	steps      []float32
	searchList int
	visits     sync.Pool
}

// Open maps path and validates it against the caller's expectations. The
// whole file is read once to verify its checksum and derive the in-memory
// navigation codes; afterwards only touched records are paged in.
func Open(path string, metric core.Metric, dimension, count int, tag uint64, searchList int) (*Index, error) {
	if searchList < 1 {
		return nil, fmt.Errorf("%w: diskann search list must be positive", core.ErrInvalidArgument)
	}
	mapped, unmap, err := mapFile(path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Index, error) { _ = unmap(mapped); return nil, err }
	header, err := parseHeader(mapped)
	if err != nil {
		return fail(err)
	}
	if header.metric != metric || header.dimension != dimension || header.count != count || header.tag != tag {
		return fail(fmt.Errorf("diskann file does not match its segment"))
	}
	x := &Index{
		mapped: mapped, unmap: unmap, header: header, recordSize: recordSize(header.dimension, header.maxDegree),
		searchList: searchList,
	}
	ranges := make([]float32, 2*dimension)
	for i := range ranges {
		ranges[i] = math.Float32frombits(binary.LittleEndian.Uint32(mapped[headerSize+4*i:]))
	}
	x.minimums, x.steps = ranges[:dimension], ranges[dimension:]
	x.records = mapped[headerSize+8*dimension:]
	if nativeLittleEndian() && uintptr(unsafe.Pointer(&x.records[0]))%unsafe.Alignof(float32(0)) == 0 {
		x.floats = unsafe.Slice((*float32)(unsafe.Pointer(&x.records[0])), len(x.records)/4)
	}
	x.codes = make([]uint8, count*dimension)
	vector, scratch := make([]float32, dimension), make([]float32, dimension)
	for ordinal := range count {
		x.readVector(ordinal, vector)
		degree := binary.LittleEndian.Uint32(x.records[ordinal*x.recordSize+4*dimension:])
		if int(degree) > header.maxDegree {
			return fail(fmt.Errorf("diskann node degree out of range"))
		}
		for _, neighbor := range x.neighbors(ordinal) {
			if int(neighbor) >= count {
				return fail(fmt.Errorf("diskann neighbour out of range"))
			}
		}
		x.encode(codeInput(metric, vector, scratch), x.codes[ordinal*dimension:(ordinal+1)*dimension])
	}
	x.visits.New = func() any { return newVisitSet(count) }
	return x, nil
}

func (x *Index) readVector(ordinal int, into []float32) {
	if x.floats != nil {
		start := ordinal * x.recordSize / 4
		copy(into, x.floats[start:start+x.header.dimension])
		return
	}
	start := ordinal * x.recordSize
	for i := range into {
		into[i] = math.Float32frombits(binary.LittleEndian.Uint32(x.records[start+4*i:]))
	}
}

func (x *Index) neighbors(ordinal int) []uint32 {
	start := ordinal*x.recordSize + 4*x.header.dimension
	degree := int(binary.LittleEndian.Uint32(x.records[start:]))
	if x.floats != nil {
		words := x.floats[(start+4)/4 : (start+4)/4+degree]
		return unsafe.Slice((*uint32)(unsafe.Pointer(unsafe.SliceData(words))), degree)
	}
	result := make([]uint32, degree)
	for i := range result {
		result[i] = binary.LittleEndian.Uint32(x.records[start+4+4*i:])
	}
	return result
}

func (x *Index) encode(vector []float32, code []uint8) {
	for i, value := range vector {
		if x.steps[i] > 0 {
			code[i] = uint8(min(255, max(0, math.Round(float64((value-x.minimums[i])/x.steps[i])))))
		}
	}
}

// navigator scores codes against one query without decoding them: for L2
// it holds q-min and the steps, for dot and cosine q*step and the constant
// q·min. Smaller distances are better.
type navigator struct {
	metric   core.Metric
	shifted  []float32
	steps    []float32
	constant float32
}

func (x *Index) navigator(query []float32) navigator {
	n := navigator{metric: x.header.metric, shifted: make([]float32, len(query)), steps: x.steps}
	if n.metric == core.MetricCosine {
		query = codeInput(core.MetricCosine, query, make([]float32, len(query)))
	}
	for i, value := range query {
		if n.metric == core.MetricL2 {
			n.shifted[i] = value - x.minimums[i]
		} else {
			n.shifted[i] = value * x.steps[i]
			n.constant += value * x.minimums[i]
		}
	}
	return n
}

func (x *Index) navigate(n *navigator, ordinal uint32) float32 {
	dimension := x.header.dimension
	code := x.codes[int(ordinal)*dimension : (int(ordinal)+1)*dimension]
	var total float32
	if n.metric == core.MetricL2 {
		for i, c := range code {
			difference := n.shifted[i] - float32(c)*n.steps[i]
			total += difference * difference
		}
		return total
	}
	for i, c := range code {
		total += n.shifted[i] * float32(c)
	}
	return -(total + n.constant)
}

func (x *Index) Upsert(uint64, []float32) error { return fmt.Errorf("diskann index is immutable") }
func (x *Index) Delete(uint64) error            { return fmt.Errorf("diskann index is immutable") }
func (x *Index) Len() int                       { return x.header.count }

func (x *Index) Search(query []float32, k int) ([]index.Candidate, error) {
	return x.SearchWithOptions(query, k, index.SearchOptions{AllowedCount: -1})
}

func (x *Index) SearchFiltered(query []float32, k int, allowed func(uint64) bool) ([]index.Candidate, error) {
	return x.SearchWithOptions(query, k, index.SearchOptions{Allowed: allowed, AllowedCount: -1})
}

// SearchWithOptions runs a best-first beam search ordered by the in-memory
// 8-bit code distances, scoring each expanded node exactly from disk.
// EFSearch overrides the configured search list. Filters are applied to the
// results, not to traversal; when a filter is selective or leaves fewer than
// k matches, the allowed records are scanned exactly instead.
func (x *Index) SearchWithOptions(query []float32, k int, options index.SearchOptions) ([]index.Candidate, error) {
	if len(query) != x.header.dimension {
		return nil, core.ErrDimensionMismatch
	}
	if k <= 0 {
		return nil, fmt.Errorf("%w: k must be positive", core.ErrInvalidArgument)
	}
	searchList := x.searchList
	if options.EFSearch > 0 {
		searchList = options.EFSearch
	}
	searchList = max(searchList, k)
	x.mu.RLock()
	defer x.mu.RUnlock()
	if x.mapped == nil {
		return nil, fmt.Errorf("diskann index is closed")
	}
	allowed := options.Allowed
	if allowed != nil && options.AllowedCount >= 0 && (options.AllowedCount <= 4*searchList || options.AllowedCount*20 <= x.header.count) {
		return x.exact(query, k, allowed)
	}
	querySquaredNorm := float32(0)
	if x.header.metric == core.MetricCosine {
		querySquaredNorm = distance.SquaredNorm(query)
	}
	nav := x.navigator(query)
	visited := x.visits.Get().(*visitSet)
	defer func() { visited.reset(); x.visits.Put(visited) }()
	medoid := x.header.medoid
	visited.add(medoid)
	pool := make([]poolEntry, 1, searchList+1)
	pool[0] = poolEntry{node: medoid, distance: x.navigate(&nav, medoid)}
	results := make([]index.Candidate, 0, 2*searchList)
	for {
		next := -1
		for i := range pool {
			if !pool[i].expanded {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		pool[next].expanded = true
		node := int(pool[next].node)
		if id := uint64(node + 1); allowed == nil || allowed(id) {
			score, err := x.score(query, querySquaredNorm, node)
			if err != nil {
				return nil, err
			}
			results = append(results, index.Candidate{ID: id, Score: score})
		}
		for _, neighbor := range x.neighbors(node) {
			if visited.add(neighbor) {
				pool = insertPool(pool, poolEntry{node: neighbor, distance: x.navigate(&nav, neighbor)}, searchList)
			}
		}
	}
	if allowed != nil && len(results) < k {
		return x.exact(query, k, allowed)
	}
	return topK(results, k), nil
}

func (x *Index) score(query []float32, querySquaredNorm float32, ordinal int) (float32, error) {
	if x.floats != nil {
		start := ordinal * x.recordSize / 4
		return distance.ScoreWithQuerySquaredNorm(x.header.metric, query, querySquaredNorm, x.floats[start:start+x.header.dimension])
	}
	vector := make([]float32, x.header.dimension)
	x.readVector(ordinal, vector)
	return distance.ScoreWithQuerySquaredNorm(x.header.metric, query, querySquaredNorm, vector)
}

func (x *Index) exact(query []float32, k int, allowed func(uint64) bool) ([]index.Candidate, error) {
	querySquaredNorm := float32(0)
	if x.header.metric == core.MetricCosine {
		querySquaredNorm = distance.SquaredNorm(query)
	}
	var results []index.Candidate
	for ordinal := range x.header.count {
		id := uint64(ordinal + 1)
		if allowed != nil && !allowed(id) {
			continue
		}
		score, err := x.score(query, querySquaredNorm, ordinal)
		if err != nil {
			return nil, err
		}
		results = append(results, index.Candidate{ID: id, Score: score})
	}
	return topK(results, k), nil
}

func topK(results []index.Candidate, k int) []index.Candidate {
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score || (results[i].Score == results[j].Score && results[i].ID < results[j].ID)
	})
	if len(results) > k {
		results = results[:k]
	}
	return results
}

// VectorCopy returns the full-precision vector at ordinal from disk.
func (x *Index) VectorCopy(ordinal int) ([]float32, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	if x.mapped == nil {
		return nil, fmt.Errorf("diskann index is closed")
	}
	if ordinal < 0 || ordinal >= x.header.count {
		return nil, fmt.Errorf("%w: vector ordinal out of range", core.ErrInvalidArgument)
	}
	vector := make([]float32, x.header.dimension)
	x.readVector(ordinal, vector)
	return vector, nil
}

func (x *Index) Stats() index.Stats {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return index.Stats{
		Type: core.IndexDiskANN, Vectors: x.header.count, MappedBytes: uint64(len(x.mapped)),
		QuantizedBytes: uint64(len(x.codes)),
	}
}

func (x *Index) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.mapped == nil {
		return nil
	}
	err := x.unmap(x.mapped)
	x.mapped, x.records, x.floats, x.codes = nil, nil, nil, nil
	return err
}

func nativeLittleEndian() bool {
	value := uint16(1)
	return *(*byte)(unsafe.Pointer(&value)) == 1
}
