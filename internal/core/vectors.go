package core

import (
	"fmt"
	"math"
	"regexp"
	"sort"
)

// Datatype selects how a named vector field stores its values. Scoring is
// always float32; half-precision types are converted at write time.
type Datatype string

const (
	DatatypeFloat32  Datatype = "float32"
	DatatypeFloat16  Datatype = "float16"
	DatatypeBFloat16 Datatype = "bfloat16"
)

// VectorField declares one named dense vector field. A multi-vector field
// stores a list of vectors per record (late interaction, e.g. ColBERT) and is
// searched by MaxSim over candidates from an index of mean-pooled vectors.
type VectorField struct {
	Dimension   int         `json:"dimension"`
	Metric      Metric      `json:"metric"`
	Index       IndexConfig `json:"index"`
	Datatype    Datatype    `json:"datatype,omitempty"`
	MultiVector bool        `json:"multi_vector,omitempty"`
}

const (
	MaxNamedVectorFields     = 16
	MaxMultiVectorLength     = 1024
	maxVectorFieldsDimension = 65536
)

var vectorFieldName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

// Normalized applies index defaults and the float32 datatype.
func (f VectorField) Normalized() VectorField {
	if f.Datatype == "" {
		f.Datatype = DatatypeFloat32
	}
	f.Index = CollectionConfig{Index: f.Index}.Normalized().Index
	return f
}

func (f VectorField) validate(name string) error {
	f = f.Normalized()
	if !vectorFieldName.MatchString(name) {
		return fmt.Errorf("%w: vector field name %q must match %s", ErrInvalidArgument, name, vectorFieldName)
	}
	if f.Dimension <= 0 || f.Dimension > maxVectorFieldsDimension {
		return fmt.Errorf("%w: vector field %q dimension must be between 1 and %d", ErrInvalidArgument, name, maxVectorFieldsDimension)
	}
	if !f.Metric.Valid() {
		return fmt.Errorf("%w: vector field %q has unsupported metric %q", ErrInvalidArgument, name, f.Metric)
	}
	switch f.Datatype {
	case DatatypeFloat32, DatatypeFloat16, DatatypeBFloat16:
	default:
		return fmt.Errorf("%w: vector field %q has unsupported datatype %q", ErrInvalidArgument, name, f.Datatype)
	}
	if f.Index.Quantization != QuantizationNone {
		return fmt.Errorf("%w: vector field %q: quantization is only supported on the default vector", ErrInvalidArgument, name)
	}
	probe := CollectionConfig{Name: "field", Dimension: f.Dimension, Metric: f.Metric, ShardCount: 1, Index: f.Index}
	if err := probe.Validate(); err != nil {
		return fmt.Errorf("vector field %q: %w", name, err)
	}
	return nil
}

func (c CollectionConfig) validateVectorFields() error {
	if len(c.Vectors) > MaxNamedVectorFields {
		return fmt.Errorf("%w: at most %d named vector fields are supported", ErrInvalidArgument, MaxNamedVectorFields)
	}
	for name, field := range c.Vectors {
		if err := field.validate(name); err != nil {
			return err
		}
	}
	return nil
}

// VectorField returns the normalized declaration of a named field.
func (c CollectionConfig) VectorField(name string) (VectorField, bool) {
	field, ok := c.Vectors[name]
	if !ok {
		return VectorField{}, false
	}
	return field.Normalized(), true
}

// VectorFieldNames lists named fields in a stable order.
func (c CollectionConfig) VectorFieldNames() []string {
	names := make([]string, 0, len(c.Vectors))
	for name := range c.Vectors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PrepareNamedVectors validates a record's named and multi-vectors against
// the collection declaration and rounds half-precision fields so the WAL,
// checkpoints and replicas hold exactly the stored values.
func (c CollectionConfig) PrepareNamedVectors(record Record) (Record, error) {
	if len(record.Vectors) == 0 && len(record.MultiVectors) == 0 {
		return record, nil
	}
	vectors := make(map[string][]float32, len(record.Vectors))
	for name, vector := range record.Vectors {
		field, ok := c.VectorField(name)
		if !ok {
			return Record{}, fmt.Errorf("%w: unknown vector field %q", ErrInvalidArgument, name)
		}
		if field.MultiVector {
			return Record{}, fmt.Errorf("%w: vector field %q is multi-vector; send it in multi_vectors", ErrInvalidArgument, name)
		}
		if err := ValidateVector(vector, field.Dimension); err != nil {
			return Record{}, fmt.Errorf("vector field %q: %w", name, err)
		}
		rounded := RoundToDatatype(field.Datatype, vector)
		if err := ValidateVector(rounded, field.Dimension); err != nil {
			return Record{}, fmt.Errorf("vector field %q: value out of %s range: %w", name, field.Datatype, err)
		}
		vectors[name] = rounded
	}
	multi := make(map[string][][]float32, len(record.MultiVectors))
	for name, list := range record.MultiVectors {
		field, ok := c.VectorField(name)
		if !ok {
			return Record{}, fmt.Errorf("%w: unknown vector field %q", ErrInvalidArgument, name)
		}
		if !field.MultiVector {
			return Record{}, fmt.Errorf("%w: vector field %q is single-vector; send it in vectors", ErrInvalidArgument, name)
		}
		if err := ValidateMultiVector(list, field.Dimension); err != nil {
			return Record{}, fmt.Errorf("vector field %q: %w", name, err)
		}
		rounded := make([][]float32, len(list))
		for position, vector := range list {
			rounded[position] = RoundToDatatype(field.Datatype, vector)
			if err := ValidateVector(rounded[position], field.Dimension); err != nil {
				return Record{}, fmt.Errorf("vector field %q: value out of %s range: %w", name, field.Datatype, err)
			}
		}
		multi[name] = rounded
	}
	record.Vectors, record.MultiVectors = nil, nil
	if len(vectors) > 0 {
		record.Vectors = vectors
	}
	if len(multi) > 0 {
		record.MultiVectors = multi
	}
	return record, nil
}

// ValidateMultiVector checks a late-interaction vector list.
func ValidateMultiVector(list [][]float32, dimension int) error {
	if len(list) == 0 {
		return fmt.Errorf("%w: multi-vector must contain at least one vector", ErrInvalidArgument)
	}
	if len(list) > MaxMultiVectorLength {
		return fmt.Errorf("%w: multi-vector cannot exceed %d vectors", ErrInvalidArgument, MaxMultiVectorLength)
	}
	for _, vector := range list {
		if err := ValidateVector(vector, dimension); err != nil {
			return err
		}
	}
	return nil
}

// RoundToDatatype returns vector rounded to the field's storage precision.
func RoundToDatatype(datatype Datatype, vector []float32) []float32 {
	out := make([]float32, len(vector))
	switch datatype {
	case DatatypeFloat16:
		for i, value := range vector {
			out[i] = Float16ToFloat32(Float32ToFloat16(value))
		}
	case DatatypeBFloat16:
		for i, value := range vector {
			out[i] = BFloat16ToFloat32(Float32ToBFloat16(value))
		}
	default:
		copy(out, vector)
	}
	return out
}

// Float32ToBFloat16 rounds to nearest even; NaN is preserved as a quiet NaN.
func Float32ToBFloat16(value float32) uint16 {
	bits := math.Float32bits(value)
	if bits&0x7fffffff > 0x7f800000 {
		return uint16(bits>>16) | 0x40
	}
	bits += 0x7fff + (bits>>16)&1
	return uint16(bits >> 16)
}

func BFloat16ToFloat32(value uint16) float32 { return math.Float32frombits(uint32(value) << 16) }

// Float32ToFloat16 converts to IEEE 754 binary16 with round-to-nearest-even.
// Values beyond the float16 range become ±Inf; callers validate finiteness
// before conversion, so PrepareNamedVectors rejects such values instead.
func Float32ToFloat16(value float32) uint16 {
	bits := math.Float32bits(value)
	sign := uint16(bits>>16) & 0x8000
	exponent := int32(bits>>23&0xff) - 127 + 15
	mantissa := bits & 0x7fffff
	switch {
	case bits&0x7fffffff > 0x7f800000:
		return sign | 0x7e00
	case exponent >= 0x1f:
		return sign | 0x7c00
	case exponent <= 0:
		if exponent < -10 {
			return sign
		}
		mantissa |= 0x800000
		shift := uint32(14 - exponent)
		half := mantissa >> shift
		remainder := mantissa & (1<<shift - 1)
		midpoint := uint32(1) << (shift - 1)
		if remainder > midpoint || (remainder == midpoint && half&1 == 1) {
			half++
		}
		return sign | uint16(half)
	}
	half := uint32(exponent)<<10 | mantissa>>13
	remainder := mantissa & 0x1fff
	if remainder > 0x1000 || (remainder == 0x1000 && half&1 == 1) {
		half++
	}
	return sign | uint16(half)
}

func Float16ToFloat32(value uint16) float32 {
	sign := uint32(value&0x8000) << 16
	exponent := uint32(value>>10) & 0x1f
	mantissa := uint32(value & 0x3ff)
	switch {
	case exponent == 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | mantissa<<13)
	case exponent == 0:
		if mantissa == 0 {
			return math.Float32frombits(sign)
		}
		result := float32(mantissa) / (1 << 24)
		if sign != 0 {
			return -result
		}
		return result
	}
	return math.Float32frombits(sign | (exponent+127-15)<<23 | mantissa<<13)
}

// NamedQuery searches one named vector field. Exactly one of Vector or
// MultiVector is set; MultiVector requires a multi-vector field.
type NamedQuery struct {
	Field       string
	Vector      []float32
	MultiVector [][]float32
	TopK        int
	EFSearch    int
	// Candidates bounds how many pooled-index candidates MaxSim rescores per
	// segment. Zero selects max(4*TopK, 100), capped at MaxRerankCandidates.
	Candidates int
}

// Validate checks the query against the collection's field declarations.
func (q NamedQuery) Validate(config CollectionConfig) error {
	field, ok := config.VectorField(q.Field)
	if !ok {
		return fmt.Errorf("%w: unknown vector field %q", ErrInvalidArgument, q.Field)
	}
	if q.TopK <= 0 {
		return fmt.Errorf("%w: top_k must be positive", ErrInvalidArgument)
	}
	if q.EFSearch < 0 || q.EFSearch > 10000 {
		return fmt.Errorf("%w: ef_search must be between 1 and 10000 when set", ErrInvalidArgument)
	}
	if q.Candidates < 0 || q.Candidates > MaxRerankCandidates {
		return fmt.Errorf("%w: candidates must be between 1 and %d when set", ErrInvalidArgument, MaxRerankCandidates)
	}
	switch {
	case q.Vector != nil && q.MultiVector != nil:
		return fmt.Errorf("%w: send either vector or multi_vector, not both", ErrInvalidArgument)
	case q.MultiVector != nil:
		if !field.MultiVector {
			return fmt.Errorf("%w: multi_vector queries require a multi-vector field", ErrInvalidArgument)
		}
		return ValidateMultiVector(q.MultiVector, field.Dimension)
	case q.Vector != nil:
		return ValidateVector(q.Vector, field.Dimension)
	default:
		return fmt.Errorf("%w: named vector search requires vector or multi_vector", ErrInvalidArgument)
	}
}

// CandidateCount is the per-segment pool size MaxSim rescoring uses.
func (q NamedQuery) CandidateCount() int {
	if q.Candidates > 0 {
		return max(q.Candidates, q.TopK)
	}
	return min(max(4*q.TopK, 100), max(MaxRerankCandidates, q.TopK))
}

// MeanPool averages a multi-vector; it is the representation indexed for
// multi-vector candidate generation.
func MeanPool(list [][]float32) []float32 {
	if len(list) == 0 {
		return nil
	}
	pooled := make([]float32, len(list[0]))
	for _, vector := range list {
		for i, value := range vector {
			pooled[i] += value
		}
	}
	for i := range pooled {
		pooled[i] /= float32(len(list))
	}
	return pooled
}

// MaxRerankCandidates bounds every rerank and MaxSim candidate pool.
const MaxRerankCandidates = 1000

// RerankType selects the second-stage scorer.
type RerankType string

const (
	RerankMaxSim       RerankType = "maxsim"
	RerankCrossEncoder RerankType = "cross_encoder"
)

// RerankSpec is an optional second stage applied to the first-stage top
// candidates. MaxSim rescoring uses a multi-vector field; cross-encoder
// reranking calls a server-configured rerank endpoint.
type RerankSpec struct {
	Type RerankType `json:"type"`
	// Candidates is how many first-stage results are reranked.
	Candidates int `json:"candidates,omitempty"`
	// MaxSim: the multi-vector field and the query token vectors.
	Field       string      `json:"field,omitempty"`
	MultiVector [][]float32 `json:"multi_vector,omitempty"`
	// Cross-encoder: the configured reranker, the query text, and where
	// document text comes from: "text" (record text, the default) or
	// "payload.<key>" / "metadata.<key>".
	Model    string `json:"model,omitempty"`
	Query    string `json:"query,omitempty"`
	Document string `json:"document,omitempty"`
}

const DefaultRerankCandidates = 50

// CandidateCount is the number of first-stage results to rerank.
func (r RerankSpec) CandidateCount(window int) int {
	if r.Candidates > 0 {
		return max(r.Candidates, window)
	}
	return max(DefaultRerankCandidates, window)
}

func (r RerankSpec) Validate(config CollectionConfig, window int) error {
	if r.Candidates < 0 || r.Candidates > MaxRerankCandidates || window > MaxRerankCandidates {
		return fmt.Errorf("%w: rerank candidates and offset + top_k must not exceed %d", ErrInvalidArgument, MaxRerankCandidates)
	}
	switch r.Type {
	case RerankMaxSim:
		field, ok := config.VectorField(r.Field)
		if !ok || !field.MultiVector {
			return fmt.Errorf("%w: maxsim rerank requires a multi-vector field", ErrInvalidArgument)
		}
		return ValidateMultiVector(r.MultiVector, field.Dimension)
	case RerankCrossEncoder:
		if r.Model == "" || r.Query == "" {
			return fmt.Errorf("%w: cross_encoder rerank requires model and query", ErrInvalidArgument)
		}
		if len(r.Query) > MaxTextBytes {
			return fmt.Errorf("%w: rerank query is too long", ErrInvalidArgument)
		}
		if _, _, err := r.DocumentSource(); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported rerank type %q", ErrInvalidArgument, r.Type)
	}
}

// DocumentSource parses Document into ("text", "") or (scope, key).
func (r RerankSpec) DocumentSource() (string, string, error) {
	switch document := r.Document; {
	case document == "" || document == "text":
		return "text", "", nil
	case len(document) > len("payload.") && document[:len("payload.")] == "payload.":
		return "payload", document[len("payload."):], nil
	case len(document) > len("metadata.") && document[:len("metadata.")] == "metadata.":
		return "metadata", document[len("metadata."):], nil
	}
	return "", "", fmt.Errorf("%w: rerank document must be text, payload.<key> or metadata.<key>", ErrInvalidArgument)
}

// MaxSim is the late-interaction score: for each query vector, the best
// similarity against any document vector, summed over query vectors.
func MaxSim(metric Metric, query, document [][]float32, score func(Metric, []float32, []float32) (float32, error)) (float32, error) {
	var total float32
	for _, q := range query {
		best := float32(math.Inf(-1))
		for _, d := range document {
			value, err := score(metric, q, d)
			if err != nil {
				return 0, err
			}
			best = max(best, value)
		}
		if len(document) > 0 {
			total += best
		}
	}
	return total, nil
}
