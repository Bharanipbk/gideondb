package segment

import (
	"fmt"
	"sort"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
	"github.com/Bharanipbk/gideondb/internal/index"
	"github.com/Bharanipbk/gideondb/internal/index/hnsw"
	"github.com/Bharanipbk/gideondb/internal/metadata"
)

// storedVector holds one vector at its field's storage precision: float32
// values, or 16-bit codes for float16/bfloat16 fields.
type storedVector struct {
	f32 []float32
	u16 []uint16
}

func encodeVector(datatype core.Datatype, vector []float32) storedVector {
	switch datatype {
	case core.DatatypeFloat16:
		codes := make([]uint16, len(vector))
		for i, value := range vector {
			codes[i] = core.Float32ToFloat16(value)
		}
		return storedVector{u16: codes}
	case core.DatatypeBFloat16:
		codes := make([]uint16, len(vector))
		for i, value := range vector {
			codes[i] = core.Float32ToBFloat16(value)
		}
		return storedVector{u16: codes}
	}
	return storedVector{f32: append([]float32(nil), vector...)}
}

func (v storedVector) decode(datatype core.Datatype) []float32 {
	if v.u16 == nil {
		return append([]float32(nil), v.f32...)
	}
	out := make([]float32, len(v.u16))
	if datatype == core.DatatypeBFloat16 {
		for i, code := range v.u16 {
			out[i] = core.BFloat16ToFloat32(code)
		}
	} else {
		for i, code := range v.u16 {
			out[i] = core.Float16ToFloat32(code)
		}
	}
	return out
}

// namedField is one declared vector field inside a segment. Dense fields
// index their vector; multi-vector fields index the mean-pooled vector for
// candidate generation and keep every token vector for MaxSim rescoring.
// A nil index means exact scan over the stored (or pooled) vectors.
type namedField struct {
	config core.VectorField
	index  index.VectorIndex
	dense  map[uint64]storedVector
	multi  map[uint64][]storedVector
	pooled map[uint64]storedVector
}

func newNamedField(config core.VectorField) (*namedField, error) {
	field := &namedField{config: config, dense: map[uint64]storedVector{}, multi: map[uint64][]storedVector{}, pooled: map[uint64]storedVector{}}
	if config.Index.Type == core.IndexHNSW {
		idx, err := hnsw.New(index.Config{Dimension: config.Dimension, Metric: config.Metric, M: config.Index.M, EFConstruction: config.Index.EFConstruction, EFSearch: config.Index.EFSearch})
		if err != nil {
			return nil, err
		}
		field.index = idx
	}
	return field, nil
}

func (f *namedField) put(ordinal uint64, vector []float32, list [][]float32) error {
	f.remove(ordinal)
	if f.config.MultiVector {
		if list == nil {
			return nil
		}
		stored := make([]storedVector, len(list))
		for i, item := range list {
			stored[i] = encodeVector(f.config.Datatype, item)
		}
		pooled := core.MeanPool(list)
		f.multi[ordinal] = stored
		f.pooled[ordinal] = storedVector{f32: pooled}
		vector = pooled
	} else {
		if vector == nil {
			return nil
		}
		f.dense[ordinal] = encodeVector(f.config.Datatype, vector)
	}
	if f.index != nil {
		return f.index.Upsert(ordinal, vector)
	}
	return nil
}

func (f *namedField) remove(ordinal uint64) {
	_, dense := f.dense[ordinal]
	_, multi := f.multi[ordinal]
	if !dense && !multi {
		return
	}
	delete(f.dense, ordinal)
	delete(f.multi, ordinal)
	delete(f.pooled, ordinal)
	if f.index != nil {
		_ = f.index.Delete(ordinal)
	}
}

func (f *namedField) multiVector(ordinal uint64) [][]float32 {
	stored, ok := f.multi[ordinal]
	if !ok {
		return nil
	}
	out := make([][]float32, len(stored))
	for i, item := range stored {
		out[i] = item.decode(f.config.Datatype)
	}
	return out
}

// candidates returns the best k ordinals for vector from the field index, or
// by exact scan over dense (or pooled) vectors.
func (f *namedField) candidates(vector []float32, k, efSearch int, allow func(uint64) bool) ([]index.Candidate, error) {
	if f.index != nil {
		if tunable, ok := f.index.(index.TunableVectorIndex); ok {
			return tunable.SearchWithOptions(vector, k, index.SearchOptions{EFSearch: efSearch, Allowed: allow, AllowedCount: -1})
		}
		return f.index.SearchFiltered(vector, k, allow)
	}
	source := f.dense
	if f.config.MultiVector {
		source = f.pooled
	}
	scored := make([]index.Candidate, 0, len(source))
	for ordinal, stored := range source {
		if !allow(ordinal) {
			continue
		}
		score, err := distance.Score(f.config.Metric, vector, stored.decode(f.config.Datatype))
		if err != nil {
			return nil, err
		}
		scored = append(scored, index.Candidate{ID: ordinal, Score: score})
	}
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score || (scored[i].Score == scored[j].Score && scored[i].ID < scored[j].ID)
	})
	if len(scored) > k {
		scored = scored[:k]
	}
	return scored, nil
}

// maxSim scores ordinal's token vectors against query.
func (f *namedField) maxSim(ordinal uint64, query [][]float32) (float32, bool, error) {
	document := f.multiVector(ordinal)
	if document == nil {
		return 0, false, nil
	}
	score, err := core.MaxSim(f.config.Metric, query, document, distance.Score)
	return score, true, err
}

// initNamed creates the declared fields and moves named vectors out of
// records already placed in s.records, as the mapped and graph-loading
// constructors do.
func (s *Segment) initNamed(config core.CollectionConfig) error {
	if len(config.Vectors) == 0 {
		return nil
	}
	s.named = make(map[string]*namedField, len(config.Vectors))
	for _, name := range config.VectorFieldNames() {
		declared, _ := config.VectorField(name)
		field, err := newNamedField(declared)
		if err != nil {
			return fmt.Errorf("vector field %q: %w", name, err)
		}
		s.named[name] = field
	}
	for ordinal, record := range s.records {
		if err := s.putNamedLocked(ordinal, record); err != nil {
			return err
		}
		record.Vectors, record.MultiVectors = nil, nil
		s.records[ordinal] = record
	}
	return nil
}

func (s *Segment) putNamedLocked(ordinal uint64, record core.Record) error {
	for name, field := range s.named {
		if err := field.put(ordinal, record.Vectors[name], record.MultiVectors[name]); err != nil {
			return fmt.Errorf("vector field %q: %w", name, err)
		}
	}
	return nil
}

func (s *Segment) removeNamedLocked(ordinal uint64) {
	for _, field := range s.named {
		field.remove(ordinal)
	}
}

// attachNamedLocked restores named vectors on a record copy for reads.
func (s *Segment) attachNamedLocked(ordinal uint64, record *core.Record) {
	for name, field := range s.named {
		if stored, ok := field.dense[ordinal]; ok {
			if record.Vectors == nil {
				record.Vectors = map[string][]float32{}
			}
			record.Vectors[name] = stored.decode(field.config.Datatype)
		}
		if list := field.multiVector(ordinal); list != nil {
			if record.MultiVectors == nil {
				record.MultiVectors = map[string][][]float32{}
			}
			record.MultiVectors[name] = list
		}
	}
}

// MultiVector returns a record's token vectors for a multi-vector field.
func (s *Segment) MultiVector(namespace, id, fieldName string) ([][]float32, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ordinal, ok := s.ordinals[key(namespace, id)]
	field := s.named[fieldName]
	if !ok || field == nil {
		return nil, false
	}
	list := field.multiVector(ordinal)
	return list, list != nil
}

// SearchNamed searches one named field. A multi-vector query takes
// query.CandidateCount() candidates from the pooled index (or exact pooled
// scan) and rescores them with MaxSim; a single vector against a
// multi-vector field searches the pooled representation directly.
func (s *Segment) SearchNamed(namespace string, query core.NamedQuery, filter *metadata.Expr, excluded map[string]struct{}) ([]core.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	field := s.named[query.Field]
	if field == nil {
		return nil, fmt.Errorf("%w: unknown vector field %q", core.ErrInvalidArgument, query.Field)
	}
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
	probe, limit := query.Vector, query.TopK
	if query.MultiVector != nil {
		probe, limit = core.MeanPool(query.MultiVector), query.CandidateCount()
	}
	candidates, err := field.candidates(probe, limit, query.EFSearch, allow)
	if err != nil {
		return nil, err
	}
	if query.MultiVector != nil {
		for position, candidate := range candidates {
			score, _, err := field.maxSim(candidate.ID, query.MultiVector)
			if err != nil {
				return nil, err
			}
			candidates[position].Score = score
		}
	}
	results := make([]core.SearchResult, 0, len(candidates))
	for _, candidate := range candidates {
		record := s.records[candidate.ID]
		results = append(results, core.SearchResult{ID: record.ID, Score: candidate.Score, Metadata: cloneMap(record.Metadata), Payload: cloneMap(record.Payload), Namespace: record.Namespace})
	}
	sortResults(results)
	if len(results) > query.TopK {
		results = results[:query.TopK]
	}
	return results, nil
}

func sortResults(results []core.SearchResult) {
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score || (results[i].Score == results[j].Score && results[i].ID < results[j].ID)
	})
}
