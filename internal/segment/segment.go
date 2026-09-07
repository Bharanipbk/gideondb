// Package segment owns the mutable record and local-index unit within a shard.
package segment

import (
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
	"github.com/Bharanipbk/gideondb/internal/index"
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
	idx, err := flat.NewMapped(source, config.Metric, ids)
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
		vectorAt: source.VectorCopy, closer: closer, metric: config.Metric,
	}
	for position, record := range records {
		ordinal := uint64(position + 1)
		record.Vector = nil
		s.records[ordinal] = cloneRecord(record)
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
	switch config.Index.Type {
	case core.IndexFlat:
		idx, err = flat.New(indexConfig)
	case core.IndexHNSW:
		idx, err = hnsw.New(indexConfig)
	default:
		err = fmt.Errorf("%w: unsupported index type %q", core.ErrInvalidArgument, config.Index.Type)
	}
	if err != nil {
		return nil, err
	}
	return &Segment{
		index: idx, nextID: 1, records: make(map[uint64]core.Record),
		ordinals: make(map[string]uint64), metadata: metadata.NewIndex(), metric: config.Metric,
	}, nil
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
	idx, err := hnsw.New(indexConfig(config))
	if err != nil {
		return nil, err
	}
	for position, record := range records {
		if err := idx.Upsert(uint64(position+1), record.Vector); err != nil {
			return nil, err
		}
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
	s := &Segment{index: idx, nextID: uint64(len(records) + 1), records: make(map[uint64]core.Record, len(records)), ordinals: make(map[string]uint64, len(records)), metadata: metadataIndex, readOnly: true, metric: config.Metric}
	for position, record := range records {
		ordinal := uint64(position + 1)
		s.records[ordinal] = cloneRecord(record)
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
	s.ordinals[k] = ordinal
	s.records[ordinal] = cloneRecord(record)
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

// SearchSparseHybrid performs exact sparse-only or dense+sparse scoring. Alpha
// is the dense weight and must be in [0,1].
func (s *Segment) SearchSparseHybrid(namespace string, dense []float32, sparse map[string]float32, alpha float32, k int, filter *metadata.Expr, excluded map[string]struct{}) ([]core.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var allowed *metadata.Set
	if filter != nil {
		allowed = s.metadata.Evaluate(filter)
	}
	queryNorm := sparseNorm(sparse)
	results := make([]core.SearchResult, 0, len(s.records))
	for ordinal, record := range s.records {
		if record.Namespace != namespace || (allowed != nil && !allowed.Contains(ordinal)) {
			continue
		}
		if _, blocked := excluded[key(record.Namespace, record.ID)]; blocked {
			continue
		}
		sparseScore := sparseCosine(sparse, queryNorm, record.SparseVector)
		score := sparseScore
		if dense != nil {
			vector := record.Vector
			if vector == nil && s.vectorAt != nil {
				var err error
				vector, err = s.vectorAt(int(ordinal - 1))
				if err != nil {
					return nil, err
				}
			}
			denseScore, err := distance.Score(s.metric, dense, vector)
			if err != nil {
				return nil, err
			}
			score = alpha*normalizeDense(s.metric, denseScore) + (1-alpha)*((sparseScore+1)/2)
		}
		results = append(results, core.SearchResult{ID: record.ID, Score: score, Metadata: cloneMap(record.Metadata), Payload: cloneMap(record.Payload), Namespace: record.Namespace})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score || (results[i].Score == results[j].Score && results[i].ID < results[j].ID)
	})
	if len(results) > k {
		results = results[:k]
	}
	return results, nil
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
