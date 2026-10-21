// Package pq implements a product-quantized exact-rescoring index.
//
// The dimension is split into m equal subspaces and each subspace is
// quantized to one of 256 k-means centroids, so a vector is stored as m
// bytes. Search computes a per-query lookup table of m*256 partial scores
// (asymmetric distance computation: the query stays at full precision), ranks
// every allowed code by summing m table entries, keeps Oversampling*k
// candidates, and rescores only those at full precision through a
// caller-supplied scorer, following the same pattern as the binary index.
//
// Codebooks must be trained before codes can be produced. Training is
// deterministic for a given input order, sample size and seed, so a segment
// rebuilt from the same records produces identical codes.
package pq

import (
	"container/heap"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"math/rand"
	"runtime"
	"sort"
	"sync"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
)

const (
	// Centroids is the number of centroids per subspace; codes are one byte.
	Centroids = 256
	// MinTrainingVectors is the smallest segment that is product-quantized.
	// Smaller segments keep exact flat search: with fewer points than this
	// the 256-centroid codebooks are underdetermined and exact search is
	// cheap anyway.
	MinTrainingVectors = 1024
	// MaxTrainingSample bounds k-means cost; larger segments train on a
	// deterministic sample and then encode every vector.
	MaxTrainingSample = 16384
	// Iterations is the number of Lloyd iterations per subspace.
	Iterations = 12
	// Seed makes training reproducible.
	Seed = 0x6769646f6e // "gideon"
)

// Scorer returns the full-precision similarity between query and vector id
// using the larger-is-better convention of distance.Score.
type Scorer func(query []float32, id uint64) (float32, error)

// Codebook holds m*256 centroids of dimension/m components each.
type Codebook struct {
	Dimension int
	Subspaces int
	Metric    core.Metric
	// Centroids is laid out subspace-major: subspace s, centroid c starts at
	// (s*256+c)*subDimension.
	Centroids []float32
}

func (c *Codebook) subDimension() int { return c.Dimension / c.Subspaces }

// Train learns a codebook from vectors. Cosine vectors are normalized first so
// inner products of reconstructions approximate cosine similarity.
func Train(vectors [][]float32, dimension, subspaces int, metric core.Metric) (*Codebook, error) {
	if dimension <= 0 || subspaces <= 0 || dimension%subspaces != 0 || !metric.Valid() {
		return nil, fmt.Errorf("%w: invalid product quantization configuration", core.ErrInvalidArgument)
	}
	if len(vectors) == 0 {
		return nil, fmt.Errorf("%w: product quantization needs training vectors", core.ErrInvalidArgument)
	}
	sample := vectors
	if len(sample) > MaxTrainingSample {
		random := rand.New(rand.NewSource(Seed))
		picked := random.Perm(len(vectors))[:MaxTrainingSample]
		sort.Ints(picked)
		sample = make([][]float32, len(picked))
		for i, position := range picked {
			sample[i] = vectors[position]
		}
	}
	prepared := make([][]float32, len(sample))
	for i, vector := range sample {
		if len(vector) != dimension {
			return nil, core.ErrDimensionMismatch
		}
		prepared[i] = prepare(metric, vector)
	}
	codebook := &Codebook{Dimension: dimension, Subspaces: subspaces, Metric: metric, Centroids: make([]float32, subspaces*Centroids*(dimension/subspaces))}
	parallel(subspaces, func(subspace int) {
		trainSubspace(codebook, prepared, subspace)
	})
	return codebook, nil
}

func trainSubspace(codebook *Codebook, vectors [][]float32, subspace int) {
	sub := codebook.subDimension()
	offset := subspace * sub
	centroids := codebook.Centroids[subspace*Centroids*sub : (subspace+1)*Centroids*sub]
	random := rand.New(rand.NewSource(Seed + int64(subspace)))
	order := random.Perm(len(vectors))
	for c := 0; c < Centroids; c++ {
		copy(centroids[c*sub:(c+1)*sub], vectors[order[c%len(order)]][offset:offset+sub])
	}
	assignment := make([]int, len(vectors))
	sums := make([]float64, Centroids*sub)
	counts := make([]int, Centroids)
	for iteration := 0; iteration < Iterations; iteration++ {
		for i, vector := range vectors {
			assignment[i] = nearest(centroids, vector[offset:offset+sub], sub)
		}
		for i := range sums {
			sums[i] = 0
		}
		for i := range counts {
			counts[i] = 0
		}
		for i, vector := range vectors {
			c := assignment[i]
			counts[c]++
			for j, value := range vector[offset : offset+sub] {
				sums[c*sub+j] += float64(value)
			}
		}
		for c := 0; c < Centroids; c++ {
			if counts[c] == 0 {
				// Reseed an empty cluster from a random training point.
				copy(centroids[c*sub:(c+1)*sub], vectors[random.Intn(len(vectors))][offset:offset+sub])
				continue
			}
			for j := 0; j < sub; j++ {
				centroids[c*sub+j] = float32(sums[c*sub+j] / float64(counts[c]))
			}
		}
	}
}

func nearest(centroids, part []float32, sub int) int {
	best, bestDistance := 0, float32(math.MaxFloat32)
	for c := 0; c < Centroids; c++ {
		centroid := centroids[c*sub : (c+1)*sub]
		var distance float32
		for j, value := range part {
			delta := value - centroid[j]
			distance += delta * delta
		}
		if distance < bestDistance {
			best, bestDistance = c, distance
		}
	}
	return best
}

// Encode writes the m-byte code of vector into code.
func (c *Codebook) Encode(vector []float32, code []byte) {
	prepared := prepare(c.Metric, vector)
	sub := c.subDimension()
	for subspace := 0; subspace < c.Subspaces; subspace++ {
		centroids := c.Centroids[subspace*Centroids*sub : (subspace+1)*Centroids*sub]
		code[subspace] = byte(nearest(centroids, prepared[subspace*sub:(subspace+1)*sub], sub))
	}
}

// EncodeAll encodes vectors in parallel into one contiguous code array.
func (c *Codebook) EncodeAll(count int, vectorAt func(position int) ([]float32, error)) ([]byte, error) {
	codes := make([]byte, count*c.Subspaces)
	workers := runtime.GOMAXPROCS(0)
	chunk := (count + workers - 1) / workers
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		start, end := worker*chunk, min((worker+1)*chunk, count)
		if start >= end {
			break
		}
		wg.Add(1)
		go func(worker, start, end int) {
			defer wg.Done()
			for position := start; position < end; position++ {
				vector, err := vectorAt(position)
				if err != nil {
					errs[worker] = err
					return
				}
				if len(vector) != c.Dimension {
					errs[worker] = core.ErrDimensionMismatch
					return
				}
				c.Encode(vector, codes[position*c.Subspaces:(position+1)*c.Subspaces])
			}
		}(worker, start, end)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return codes, nil
}

// table returns the m*256 partial scores of query (larger is better).
func (c *Codebook) table(query []float32) []float32 {
	prepared := prepare(c.Metric, query)
	sub := c.subDimension()
	table := make([]float32, c.Subspaces*Centroids)
	for subspace := 0; subspace < c.Subspaces; subspace++ {
		part := prepared[subspace*sub : (subspace+1)*sub]
		for centroid := 0; centroid < Centroids; centroid++ {
			values := c.Centroids[(subspace*Centroids+centroid)*sub : (subspace*Centroids+centroid+1)*sub]
			var score float32
			if c.Metric == core.MetricL2 {
				for j, value := range part {
					delta := value - values[j]
					score -= delta * delta
				}
			} else {
				for j, value := range part {
					score += value * values[j]
				}
			}
			table[subspace*Centroids+centroid] = score
		}
	}
	return table
}

func prepare(metric core.Metric, vector []float32) []float32 {
	if metric != core.MetricCosine {
		return vector
	}
	var norm float64
	for _, value := range vector {
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		return vector
	}
	scale := float32(1 / math.Sqrt(norm))
	normalized := make([]float32, len(vector))
	for i, value := range vector {
		normalized[i] = value * scale
	}
	return normalized
}

func parallel(n int, work func(int)) {
	workers := min(runtime.GOMAXPROCS(0), n)
	next := make(chan int, n)
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				work(i)
			}
		}()
	}
	wg.Wait()
}

// Index is a product-quantized candidate generator with full-precision
// rescoring. Codes are stored densely; deletes swap the last code into place.
type Index struct {
	mu           sync.RWMutex
	codebook     *Codebook
	oversampling int
	ids          []uint64
	codes        []byte
	positions    map[uint64]int
	score        Scorer
}

func New(codebook *Codebook, oversampling int, score Scorer) (*Index, error) {
	if codebook == nil || oversampling < 1 || score == nil {
		return nil, fmt.Errorf("%w: invalid pq index configuration", core.ErrInvalidArgument)
	}
	return &Index{codebook: codebook, oversampling: oversampling, positions: map[uint64]int{}, score: score}, nil
}

// NewWithCodes builds an index from precomputed codes in ids order.
func NewWithCodes(codebook *Codebook, oversampling int, score Scorer, ids []uint64, codes []byte) (*Index, error) {
	b, err := New(codebook, oversampling, score)
	if err != nil {
		return nil, err
	}
	if len(codes) != len(ids)*codebook.Subspaces {
		return nil, fmt.Errorf("%w: pq code count mismatch", core.ErrInvalidArgument)
	}
	b.ids, b.codes = append([]uint64(nil), ids...), codes
	for position, id := range ids {
		if _, exists := b.positions[id]; exists {
			return nil, fmt.Errorf("%w: duplicate pq id", core.ErrInvalidArgument)
		}
		b.positions[id] = position
	}
	return b, nil
}

func (b *Index) Codebook() *Codebook { return b.codebook }

func (b *Index) Upsert(id uint64, vector []float32) error {
	if len(vector) != b.codebook.Dimension {
		return core.ErrDimensionMismatch
	}
	code := make([]byte, b.codebook.Subspaces)
	b.codebook.Encode(vector, code)
	b.mu.Lock()
	defer b.mu.Unlock()
	if position, exists := b.positions[id]; exists {
		copy(b.codes[position*len(code):], code)
		return nil
	}
	b.positions[id] = len(b.ids)
	b.ids = append(b.ids, id)
	b.codes = append(b.codes, code...)
	return nil
}

func (b *Index) Delete(id uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	position, exists := b.positions[id]
	if !exists {
		return nil
	}
	m := b.codebook.Subspaces
	last := len(b.ids) - 1
	if position != last {
		b.ids[position] = b.ids[last]
		copy(b.codes[position*m:(position+1)*m], b.codes[last*m:])
		b.positions[b.ids[position]] = position
	}
	b.ids = b.ids[:last]
	b.codes = b.codes[:last*m]
	delete(b.positions, id)
	return nil
}

func (b *Index) Search(query []float32, k int) ([]index.Candidate, error) {
	return b.SearchFiltered(query, k, nil)
}

func (b *Index) SearchFiltered(query []float32, k int, allowed func(uint64) bool) ([]index.Candidate, error) {
	if len(query) != b.codebook.Dimension {
		return nil, core.ErrDimensionMismatch
	}
	if k <= 0 {
		return nil, fmt.Errorf("%w: k must be positive", core.ErrInvalidArgument)
	}
	table := b.codebook.table(query)
	m := b.codebook.Subspaces
	b.mu.RLock()
	defer b.mu.RUnlock()
	limit := k * b.oversampling
	nearest := &approximateHeap{}
	for position, id := range b.ids {
		if allowed != nil && !allowed(id) {
			continue
		}
		code := b.codes[position*m : (position+1)*m]
		var score float32
		for subspace, centroid := range code {
			score += table[subspace*Centroids+int(centroid)]
		}
		if nearest.Len() < limit {
			heap.Push(nearest, approximateCandidate{id, score})
		} else if top := (*nearest)[0]; score > top.score || (score == top.score && id < top.id) {
			(*nearest)[0] = approximateCandidate{id, score}
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
	return len(b.ids)
}

func (b *Index) Stats() index.Stats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return index.Stats{
		Type: core.IndexFlat, Vectors: len(b.ids), VectorBytes: uint64(len(b.ids)) * uint64(b.codebook.Dimension) * 4,
		QuantizedBytes: uint64(len(b.codes)) + uint64(len(b.codebook.Centroids))*4,
	}
}

type approximateCandidate struct {
	id    uint64
	score float32
}

// approximateHeap is a min-heap on score so the worst kept candidate is at
// the root and can be replaced in O(log n).
type approximateHeap []approximateCandidate

func (h approximateHeap) Len() int { return len(h) }
func (h approximateHeap) Less(i, j int) bool {
	return h[i].score < h[j].score || (h[i].score == h[j].score && h[i].id > h[j].id)
}
func (h approximateHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *approximateHeap) Push(x any)   { *h = append(*h, x.(approximateCandidate)) }
func (h *approximateHeap) Pop() any {
	old := *h
	item := old[len(old)-1]
	*h = old[:len(old)-1]
	return item
}

// File layout (little endian), version 1:
//
//	magic "GPQ1" | version u16 | metric u8 | reserved u8 | dimension u32 |
//	subspaces u32 | count u64 | fingerprint u64 | centroids f32[...] |
//	codes u8[count*subspaces] | crc32c u32 (over everything before it)
//
// The fingerprint identifies the record order the codes were written in, so a
// loader can detect a mismatch and retrain instead of using misaligned codes.
const fileHeaderSize = 32

var fileMagic = [4]byte{'G', 'P', 'Q', '1'}
var crcTable = crc32.MakeTable(crc32.Castagnoli)
var metricCodes = []core.Metric{core.MetricCosine, core.MetricDot, core.MetricL2}

// Marshal serializes a codebook with codes for count vectors.
func Marshal(codebook *Codebook, codes []byte, fingerprint uint64) ([]byte, error) {
	if len(codes)%codebook.Subspaces != 0 {
		return nil, fmt.Errorf("%w: pq code length mismatch", core.ErrInvalidArgument)
	}
	metric := -1
	for i, candidate := range metricCodes {
		if candidate == codebook.Metric {
			metric = i
		}
	}
	if metric < 0 {
		return nil, fmt.Errorf("%w: unsupported pq metric", core.ErrInvalidArgument)
	}
	data := make([]byte, fileHeaderSize, fileHeaderSize+len(codebook.Centroids)*4+len(codes)+4)
	copy(data[:4], fileMagic[:])
	binary.LittleEndian.PutUint16(data[4:6], 1)
	data[6] = byte(metric)
	binary.LittleEndian.PutUint32(data[8:12], uint32(codebook.Dimension))
	binary.LittleEndian.PutUint32(data[12:16], uint32(codebook.Subspaces))
	binary.LittleEndian.PutUint64(data[16:24], uint64(len(codes)/codebook.Subspaces))
	binary.LittleEndian.PutUint64(data[24:32], fingerprint)
	for _, value := range codebook.Centroids {
		data = binary.LittleEndian.AppendUint32(data, math.Float32bits(value))
	}
	data = append(data, codes...)
	return binary.LittleEndian.AppendUint32(data, crc32.Checksum(data, crcTable)), nil
}

// Unmarshal decodes Marshal output. Codes alias data.
func Unmarshal(data []byte) (codebook *Codebook, codes []byte, fingerprint uint64, err error) {
	if len(data) < fileHeaderSize+4 || string(data[:4]) != string(fileMagic[:]) {
		return nil, nil, 0, fmt.Errorf("invalid pq file")
	}
	body := data[:len(data)-4]
	if crc32.Checksum(body, crcTable) != binary.LittleEndian.Uint32(data[len(data)-4:]) {
		return nil, nil, 0, fmt.Errorf("pq file checksum mismatch")
	}
	if binary.LittleEndian.Uint16(data[4:6]) != 1 || int(data[6]) >= len(metricCodes) || data[7] != 0 {
		return nil, nil, 0, fmt.Errorf("unsupported pq file version")
	}
	dimension := int(binary.LittleEndian.Uint32(data[8:12]))
	subspaces := int(binary.LittleEndian.Uint32(data[12:16]))
	count := binary.LittleEndian.Uint64(data[16:24])
	if dimension <= 0 || subspaces <= 0 || dimension%subspaces != 0 {
		return nil, nil, 0, fmt.Errorf("invalid pq file shape")
	}
	centroidCount := uint64(subspaces) * Centroids * uint64(dimension/subspaces)
	if uint64(len(body)) != fileHeaderSize+centroidCount*4+count*uint64(subspaces) {
		return nil, nil, 0, fmt.Errorf("pq file length mismatch")
	}
	codebook = &Codebook{Dimension: dimension, Subspaces: subspaces, Metric: metricCodes[data[6]], Centroids: make([]float32, centroidCount)}
	for i := range codebook.Centroids {
		codebook.Centroids[i] = math.Float32frombits(binary.LittleEndian.Uint32(body[fileHeaderSize+i*4:]))
	}
	return codebook, body[fileHeaderSize+int(centroidCount)*4:], binary.LittleEndian.Uint64(data[24:32]), nil
}
