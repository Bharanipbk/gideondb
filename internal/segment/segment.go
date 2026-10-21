// Package segment owns the mutable record and local-index unit within a shard.
package segment

import (
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"sync"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
	"github.com/Bharanipbk/gideondb/internal/index"
	"github.com/Bharanipbk/gideondb/internal/index/binary"
	"github.com/Bharanipbk/gideondb/internal/index/diskann"
	"github.com/Bharanipbk/gideondb/internal/index/flat"
	"github.com/Bharanipbk/gideondb/internal/index/hnsw"
	"github.com/Bharanipbk/gideondb/internal/metadata"
)

// Segment is the Phase 1 mutable segment. Immutable segments and tombstone
// sidecars are introduced with the production storage phase.
type Segment struct {
	mu       sync.RWMutex
	index    index.VectorIndex
	nextID   uint64
	records  map[uint64]core.Record
	ordinals map[string]uint64
	metadata *metadata.Index
	readOnly bool
	vectorAt func(ordinal int) ([]float32, error)
	closer   interface{ Close() error }
	metric   core.Metric
	lexical  *lexicalIndex
}

func NewMapped(config core.CollectionConfig, records []core.Record, source flat.MappedSource, closer interface{ Close() error }) (*Segment, error) {
	return NewMappedWithMetadata(config, records, source, closer, nil)
}

func NewMappedWithMetadata(config core.CollectionConfig, records []core.Record, source flat.MappedSource, closer interface{ Close() error }, metadataIndex *metadata.Index) (*Segment, error) {
	if source == nil || source.Dimension() != config.Dimension {
		return nil, fmt.Errorf("%w: mapped source dimension mismatch", core.ErrInvalidArgument)
	}
	ids := make([]uint64, len(records))
	for position := range ids {
		ids[position] = uint64(position + 1)
	}
	var idx index.VectorIndex
	var err error
	if config.Index.Quantization == core.QuantizationBinary {
		// Codes live on the heap; full-precision vectors stay memory-mapped and
		// are touched only for rescoring.
		quantized, buildErr := binary.New(config.Dimension, config.Index.Oversampling, func(query []float32, id uint64) (float32, error) {
			return source.Score(config.Metric, query, int(id-1))
		})
		if buildErr != nil {
			return nil, buildErr
		}
		for position, id := range ids {
			vector, copyErr := source.VectorCopy(position)
			if copyErr != nil {
				return nil, copyErr
			}
			if err := quantized.Upsert(id, vector); err != nil {
				return nil, err
			}
		}
		idx = quantized
	} else if idx, err = flat.NewMapped(source, config.Metric, ids); err != nil {
		return nil, err
	}
	loadedMetadata := metadataIndex != nil
	if metadataIndex == nil {
		metadataIndex = metadata.NewIndex()
	}
	s := &Segment{
		index: idx, nextID: uint64(len(records) + 1), records: make(map[uint64]core.Record, len(records)),
		ordinals: make(map[string]uint64, len(records)), metadata: metadataIndex, readOnly: true,
		vectorAt: source.VectorCopy, closer: closer, metric: config.Metric, lexical: newLexicalIndex(),
	}
	for position, record := range records {
		ordinal := uint64(position + 1)
		record.Vector = nil
		s.records[ordinal] = cloneRecord(record)
		s.lexical.add(ordinal, record)
		s.ordinals[key(record.Namespace, record.ID)] = ordinal
		if !loadedMetadata {
			s.metadata.Upsert(ordinal, record.Metadata)
		}
	}
	return s, nil
}

func New(config core.CollectionConfig) (*Segment, error) {
	indexConfig := index.Config{
		Dimension: config.Dimension, Metric: config.Metric, M: config.Index.M,
		EFConstruction: config.Index.EFConstruction, EFSearch: config.Index.EFSearch,
	}
	var idx index.VectorIndex
	var err error
	s := &Segment{
		nextID: 1, records: make(map[uint64]core.Record),
		ordinals: make(map[string]uint64), metadata: metadata.NewIndex(), metric: config.Metric, lexical: newLexicalIndex(),
	}
	switch {
	case config.Index.Type == core.IndexFlat && config.Index.Quantization == core.QuantizationBinary:
		// Records already hold full-precision vectors, so the index keeps only
		// codes and rescores from the record map under the segment lock.
		idx, err = binary.New(config.Dimension, config.Index.Oversampling, func(query []float32, id uint64) (float32, error) {
			return distance.Score(config.Metric, query, s.records[id].Vector)
		})
	case config.Index.Type == core.IndexFlat || config.Index.Type == core.IndexDiskANN:
		// DiskANN graphs are built only when a segment is sealed.
		idx, err = flat.New(indexConfig)
	case config.Index.Type == core.IndexHNSW:
		idx, err = hnsw.New(indexConfig)
	default:
		err = fmt.Errorf("%w: unsupported index type %q", core.ErrInvalidArgument, config.Index.Type)
	}
	if err != nil {
		return nil, err
	}
	s.index = idx
	return s, nil
}

func NewImmutable(config core.CollectionConfig, records []core.Record, metadataIndex *metadata.Index) (*Segment, error) {
	s, err := New(config)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if err := s.Upsert(record); err != nil {
			return nil, err
		}
	}
	if metadataIndex != nil {
		s.metadata = metadataIndex
	}
	s.readOnly = true
	return s, nil
}

func BuildHNSWGraph(config core.CollectionConfig, records []core.Record) ([]byte, error) {
	ids := make([]uint64, len(records))
	vectors := make([][]float32, len(records))
	for position, record := range records {
		ids[position], vectors[position] = uint64(position+1), record.Vector
	}
	idx, err := hnsw.Build(indexConfig(config), ids, vectors, 0)
	if err != nil {
		return nil, err
	}
	return idx.MarshalGraph()
}

func NewHNSWFromGraph(config core.CollectionConfig, records []core.Record, graph []byte) (*Segment, error) {
	return NewHNSWFromGraphWithMetadata(config, records, graph, nil)
}

func NewHNSWFromGraphWithMetadata(config core.CollectionConfig, records []core.Record, graph []byte, metadataIndex *metadata.Index) (*Segment, error) {
	ids := make([]uint64, len(records))
	vectors := make([][]float32, len(records))
	for position, record := range records {
		ids[position] = uint64(position + 1)
		vectors[position] = record.Vector
	}
	idx, err := hnsw.LoadGraph(indexConfig(config), ids, vectors, graph)
	if err != nil {
		return nil, err
	}
	loadedMetadata := metadataIndex != nil
	if metadataIndex == nil {
		metadataIndex = metadata.NewIndex()
	}
	s := &Segment{index: idx, nextID: uint64(len(records) + 1), records: make(map[uint64]core.Record, len(records)), ordinals: make(map[string]uint64, len(records)), metadata: metadataIndex, readOnly: true, metric: config.Metric, lexical: newLexicalIndex()}
	for position, record := range records {
		ordinal := uint64(position + 1)
		s.records[ordinal] = cloneRecord(record)
		s.lexical.add(ordinal, record)
		s.ordinals[key(record.Namespace, record.ID)] = ordinal
		if !loadedMetadata {
			s.metadata.Upsert(ordinal, record.Metadata)
		}
	}
	return s, nil
}

// DiskANNTag digests the record keys in order so a DiskANN file is only
// ever served with the records whose ordinals it was built from.
func DiskANNTag(records []core.Record) uint64 {
	digest := fnv.New64a()
	for _, record := range records {
		_, _ = digest.Write([]byte(key(record.Namespace, record.ID)))
		_, _ = digest.Write([]byte{0xff})
	}
	return digest.Sum64()
}

// BuildDiskANN builds a Vamana graph over records, which must carry their
// vectors, and writes it with the vectors to path. Record order defines the
// ordinals that NewDiskANNWithMetadata expects.
func BuildDiskANN(config core.CollectionConfig, records []core.Record, path string) error {
	vectors := make([][]float32, len(records))
	for position, record := range records {
		vectors[position] = record.Vector
	}
	params := diskann.Params{MaxDegree: config.Index.MaxDegree, BuildList: config.Index.EFConstruction, Alpha: config.Index.Alpha}
	graph, err := diskann.BuildGraph(config.Metric, vectors, params, 0)
	if err != nil {
		return err
	}
	return diskann.WriteFile(path, config.Metric, vectors, graph, params.MaxDegree, DiskANNTag(records))
}

// NewDiskANNWithMetadata opens a sealed segment served from the DiskANN file
// at path. Record vectors are not retained; reads come from the file.
func NewDiskANNWithMetadata(config core.CollectionConfig, records []core.Record, path string, metadataIndex *metadata.Index) (*Segment, error) {
	idx, err := diskann.Open(path, config.Metric, config.Dimension, len(records), DiskANNTag(records), config.Index.EFSearch)
	if err != nil {
		return nil, err
	}
	loadedMetadata := metadataIndex != nil
	if metadataIndex == nil {
		metadataIndex = metadata.NewIndex()
	}
	s := &Segment{
		index: idx, nextID: uint64(len(records) + 1), records: make(map[uint64]core.Record, len(records)),
		ordinals: make(map[string]uint64, len(records)), metadata: metadataIndex, readOnly: true,
		vectorAt: idx.VectorCopy, closer: idx, metric: config.Metric, lexical: newLexicalIndex(),
	}
	for position, record := range records {
		ordinal := uint64(position + 1)
		record.Vector = nil
		s.records[ordinal] = cloneRecord(record)
		s.lexical.add(ordinal, record)
		s.ordinals[key(record.Namespace, record.ID)] = ordinal
		if !loadedMetadata {
			s.metadata.Upsert(ordinal, record.Metadata)
		}
	}
	return s, nil
}

func indexConfig(config core.CollectionConfig) index.Config {
	return index.Config{Dimension: config.Dimension, Metric: config.Metric, M: config.Index.M, EFConstruction: config.Index.EFConstruction, EFSearch: config.Index.EFSearch}
}

func key(namespace, id string) string { return namespace + "\x00" + id }

func (s *Segment) Upsert(record core.Record) error {
	if s.readOnly {
		return fmt.Errorf("immutable segment cannot be mutated")
	}
	if record.ID == "" {
		return fmt.Errorf("%w: record id is required", core.ErrInvalidArgument)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(record.Namespace, record.ID)
	ordinal, ok := s.ordinals[k]
	if !ok {
		ordinal = s.nextID
		s.nextID++
	}
	if err := s.index.Upsert(ordinal, record.Vector); err != nil {
		return err
	}
	if previous, exists := s.records[ordinal]; exists {
		s.lexical.remove(ordinal, previous)
	}
	s.ordinals[k] = ordinal
	s.records[ordinal] = cloneRecord(record)
	s.lexical.add(ordinal, record)
	s.metadata.Upsert(ordinal, record.Metadata)
	return nil
}

func (s *Segment) Get(namespace, id string) (core.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ordinal, ok := s.ordinals[key(namespace, id)]
	if !ok {
		return core.Record{}, core.ErrNotFound
	}
	record := cloneRecord(s.records[ordinal])
	if record.Vector == nil && s.vectorAt != nil {
		vector, err := s.vectorAt(int(ordinal - 1))
		if err != nil {
			return core.Record{}, err
		}
		record.Vector = vector
	}
	return record, nil
}

func (s *Segment) Delete(namespace, id string) error {
	if s.readOnly {
		return fmt.Errorf("immutable segment cannot be mutated")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(namespace, id)
	ordinal, ok := s.ordinals[k]
	if !ok {
		return core.ErrNotFound
	}
	if err := s.index.Delete(ordinal); err != nil {
		return err
	}
	s.lexical.remove(ordinal, s.records[ordinal])
	delete(s.ordinals, k)
	delete(s.records, ordinal)
	s.metadata.Delete(ordinal)
	return nil
}

func (s *Segment) Search(namespace string, query []float32, k int) ([]core.SearchResult, error) {
	return s.SearchFiltered(namespace, query, k, nil)
}

func (s *Segment) SearchFiltered(namespace string, query []float32, k int, filter *metadata.Expr) ([]core.SearchResult, error) {
	return s.SearchFilteredWithOptions(namespace, query, k, filter, nil, 0)
}

func (s *Segment) SearchFilteredExcluding(namespace string, query []float32, k int, filter *metadata.Expr, excluded map[string]struct{}) ([]core.SearchResult, error) {
	return s.SearchFilteredWithOptions(namespace, query, k, filter, excluded, 0)
}

func (s *Segment) SearchFilteredWithOptions(namespace string, query []float32, k int, filter *metadata.Expr, excluded map[string]struct{}, efSearch int) ([]core.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var allowed *metadata.Set
	if filter != nil {
		allowed = s.metadata.Evaluate(filter)
	}
	allow := func(id uint64) bool {
		if allowed != nil && !allowed.Contains(id) {
			return false
		}
		record, ok := s.records[id]
		if !ok || record.Namespace != namespace {
			return false
		}
		_, blocked := excluded[key(record.Namespace, record.ID)]
		return !blocked
	}
	var candidates []index.Candidate
	var err error
	if tunable, ok := s.index.(index.TunableVectorIndex); ok {
		allowedCount := -1
		if allowed != nil {
			allowedCount = allowed.Len()
		}
		candidates, err = tunable.SearchWithOptions(query, k, index.SearchOptions{EFSearch: efSearch, Allowed: allow, AllowedCount: allowedCount})
	} else {
		candidates, err = s.index.SearchFiltered(query, k, allow)
	}
	if err != nil {
		return nil, err
	}
	results := make([]core.SearchResult, 0, min(k, len(candidates)))
	for _, candidate := range candidates {
		record := s.records[candidate.ID]
		results = append(results, core.SearchResult{
			ID: record.ID, Score: candidate.Score, Metadata: cloneMap(record.Metadata),
			Payload: cloneMap(record.Payload), Namespace: record.Namespace,
		})
		if len(results) == k {
			break
		}
	}
	return results, nil
}

// SearchSparseHybrid is the sparse-vector form of SearchHybrid with weighted
// fusion. Alpha is the dense weight and must be in [0,1].
func (s *Segment) SearchSparseHybrid(namespace string, dense []float32, sparse map[string]float32, alpha float32, k int, filter *metadata.Expr, excluded map[string]struct{}) ([]core.SearchResult, error) {
	return s.SearchHybrid(namespace, core.HybridQuery{Dense: dense, Sparse: sparse, Alpha: alpha, TopK: k}, filter, excluded)
}

// hybridCandidateFloor bounds how many candidates each signal contributes
// before fusion. Small segments are therefore scored exhaustively.
const (
	hybridCandidateFloor      = 100
	hybridCandidateMultiplier = 4
)

// SearchHybrid retrieves candidates from each requested signal (dense ANN,
// sparse inverted index, BM25 text index), then fuses them. Weighted fusion
// rescores every candidate exactly on every signal; RRF sums 1/(60+rank).
func (s *Segment) SearchHybrid(namespace string, query core.HybridQuery, filter *metadata.Expr, excluded map[string]struct{}) ([]core.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var allowed *metadata.Set
	if filter != nil {
		allowed = s.metadata.Evaluate(filter)
	}
	allow := func(ordinal uint64) bool {
		if allowed != nil && !allowed.Contains(ordinal) {
			return false
		}
		record, ok := s.records[ordinal]
		if !ok || record.Namespace != namespace {
			return false
		}
		_, blocked := excluded[key(record.Namespace, record.ID)]
		return !blocked
	}
	limit := max(query.TopK*hybridCandidateMultiplier, hybridCandidateFloor)
	var denseList []index.Candidate
	if query.Dense != nil {
		var err error
		if denseList, err = s.index.SearchFiltered(query.Dense, limit, allow); err != nil {
			return nil, err
		}
	}
	var sparseList, textList []scoredOrdinal
	if query.Sparse != nil {
		sparseList = s.lexical.searchSparse(query.Sparse, allow, limit)
	}
	if query.Text != "" {
		textList = s.lexical.searchText(query.Text, allow, limit)
	}

	scores := map[uint64]float32{}
	if query.Fusion == core.FusionRRF {
		for rank, candidate := range denseList {
			scores[candidate.ID] += rrf(rank)
		}
		for rank, candidate := range sparseList {
			scores[candidate.ordinal] += rrf(rank)
		}
		for rank, candidate := range textList {
			scores[candidate.ordinal] += rrf(rank)
		}
	} else {
		denseScores := make(map[uint64]float32, len(denseList))
		for _, candidate := range denseList {
			denseScores[candidate.ID] = candidate.Score
			scores[candidate.ID] = 0
		}
		for _, candidate := range sparseList {
			scores[candidate.ordinal] = 0
		}
		for _, candidate := range textList {
			scores[candidate.ordinal] = 0
		}
		queryNorm := sparseNorm(query.Sparse)
		textScore := func(uint64) float32 { return 0 }
		if query.Text != "" {
			textScore = s.lexical.bm25Scorer(query.Text)
		}
		lexicalSignals := 0
		if query.Sparse != nil {
			lexicalSignals++
		}
		if query.Text != "" {
			lexicalSignals++
		}
		raw := query.Dense == nil && lexicalSignals == 1
		for ordinal := range scores {
			record := s.records[ordinal]
			var lexical float32
			if query.Sparse != nil {
				cosine := sparseCosine(query.Sparse, queryNorm, record.SparseVector)
				if raw {
					lexical = cosine
				} else {
					lexical += (cosine + 1) / 2
				}
			}
			if query.Text != "" {
				bm25 := textScore(ordinal)
				if raw {
					lexical = bm25
				} else {
					lexical += normalizeBM25(bm25)
				}
			}
			if !raw {
				lexical /= float32(lexicalSignals)
			}
			score := lexical
			if query.Dense != nil {
				denseScore, found := denseScores[ordinal]
				if !found {
					vector, err := s.vectorFor(ordinal, record)
					if err != nil {
						return nil, err
					}
					if denseScore, err = distance.Score(s.metric, query.Dense, vector); err != nil {
						return nil, err
					}
				}
				score = query.Alpha*normalizeDense(s.metric, denseScore) + (1-query.Alpha)*lexical
			}
			scores[ordinal] = score
		}
	}
	results := make([]core.SearchResult, 0, len(scores))
	for ordinal, score := range scores {
		record := s.records[ordinal]
		results = append(results, core.SearchResult{ID: record.ID, Score: score, Metadata: cloneMap(record.Metadata), Payload: cloneMap(record.Payload), Namespace: record.Namespace})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score || (results[i].Score == results[j].Score && results[i].ID < results[j].ID)
	})
	if len(results) > query.TopK {
		results = results[:query.TopK]
	}
	return results, nil
}

func rrf(rank int) float32 { return 1 / float32(core.RRFConstant+rank+1) }

func (s *Segment) vectorFor(ordinal uint64, record core.Record) ([]float32, error) {
	if record.Vector != nil || s.vectorAt == nil {
		return record.Vector, nil
	}
	return s.vectorAt(int(ordinal - 1))
}

func sparseNorm(vector map[string]float32) float64 {
	var sum float64
	for _, value := range vector {
		sum += float64(value) * float64(value)
	}
	return math.Sqrt(sum)
}

func sparseCosine(query map[string]float32, queryNorm float64, candidate map[string]float32) float32 {
	if queryNorm == 0 || len(candidate) == 0 {
		return 0
	}
	var product, squared float64
	for term, value := range candidate {
		squared += float64(value) * float64(value)
		product += float64(value) * float64(query[term])
	}
	if squared == 0 {
		return 0
	}
	return float32(product / (queryNorm * math.Sqrt(squared)))
}

func normalizeDense(metric core.Metric, score float32) float32 {
	switch metric {
	case core.MetricCosine:
		return (score + 1) / 2
	case core.MetricL2:
		return 1 / (1 - score)
	default:
		return float32(1 / (1 + math.Exp(-float64(score))))
	}
}

func (s *Segment) Records() []core.Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]core.Record, 0, len(s.records))
	for _, record := range s.records {
		cloned := cloneRecord(record)
		if cloned.Vector == nil && s.vectorAt != nil {
			cloned.Vector, _ = s.vectorAt(int(s.ordinals[key(record.Namespace, record.ID)] - 1))
		}
		result = append(result, cloned)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Namespace == result[j].Namespace {
			return result[i].ID < result[j].ID
		}
		return result[i].Namespace < result[j].Namespace
	})
	return result
}

func (s *Segment) Len() int { return s.index.Len() }

func (s *Segment) Has(namespace, id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.ordinals[key(namespace, id)]
	return ok
}

func (s *Segment) Keys() map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]struct{}, len(s.ordinals))
	for value := range s.ordinals {
		result[value] = struct{}{}
	}
	return result
}

func (s *Segment) Close() error {
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}

func cloneRecord(in core.Record) core.Record {
	out := in
	out.Vector = append([]float32(nil), in.Vector...)
	if in.SparseVector != nil {
		out.SparseVector = make(map[string]float32, len(in.SparseVector))
		for term, value := range in.SparseVector {
			out.SparseVector[term] = value
		}
	}
	out.Metadata = cloneMap(in.Metadata)
	out.Payload = cloneMap(in.Payload)
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
