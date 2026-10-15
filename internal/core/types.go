// Package core contains shared domain types without storage or transport
// dependencies.
package core

import (
	"errors"
	"fmt"
	"math"
	"regexp"
)

var (
	ErrNotFound             = errors.New("not found")
	ErrAlreadyExists        = errors.New("already exists")
	ErrDimensionMismatch    = errors.New("vector dimension mismatch")
	ErrInvalidArgument      = errors.New("invalid argument")
	ErrShardNotOwned        = errors.New("shard not owned by this node")
	ErrReplicationGap       = errors.New("replication sequence gap")
	ErrReplicationConflict  = errors.New("replication sequence conflict")
	ErrReplicationCompacted = errors.New("replication sequence compacted")
	ErrIdempotencyConflict  = errors.New("idempotency key conflict")
)

// Metric identifies how vector similarity is computed.
type Metric string

const (
	MetricCosine Metric = "cosine"
	MetricDot    Metric = "dot"
	MetricL2     Metric = "l2"
)

func (m Metric) Valid() bool {
	switch m {
	case MetricCosine, MetricDot, MetricL2:
		return true
	default:
		return false
	}
}

func ValidateVector(vector []float32, dimension int) error {
	if len(vector) != dimension {
		return ErrDimensionMismatch
	}
	for position, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("%w: vector value at position %d must be finite", ErrInvalidArgument, position)
		}
	}
	return nil
}

func ValidateSparseVector(vector map[string]float32) error {
	if len(vector) == 0 {
		return fmt.Errorf("%w: sparse_vector must contain at least one term", ErrInvalidArgument)
	}
	if len(vector) > 10000 {
		return fmt.Errorf("%w: sparse_vector cannot exceed 10000 terms", ErrInvalidArgument)
	}
	for term, value := range vector {
		if term == "" {
			return fmt.Errorf("%w: sparse_vector terms must not be empty", ErrInvalidArgument)
		}
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("%w: sparse_vector value for %q must be finite", ErrInvalidArgument, term)
		}
	}
	return nil
}

// CollectionConfig is immutable for the lifetime of a collection in v0.1.
type CollectionConfig struct {
	Name       string      `json:"name"`
	Dimension  int         `json:"dimension"`
	Metric     Metric      `json:"metric"`
	ShardCount int         `json:"shard_count"`
	Index      IndexConfig `json:"index"`
	// Vectorizer names a server-configured embedding model. When set, records
	// and searches may send text instead of a vector. Optional.
	Vectorizer string `json:"vectorizer,omitempty"`
}

var vectorizerName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type IndexType string

const (
	IndexFlat IndexType = "flat"
	IndexHNSW IndexType = "hnsw"
)

// IndexConfig selects the collection-local vector index. Zero values select
// the flat index; HNSW defaults are applied by Normalized.
type IndexConfig struct {
	Type           IndexType `json:"type"`
	M              int       `json:"m,omitempty"`
	EFConstruction int       `json:"ef_construction,omitempty"`
	EFSearch       int       `json:"ef_search,omitempty"`
	// Quantization "binary" keeps 1-bit sign codes for candidate generation
	// and rescores Oversampling*k candidates at full precision. Flat only.
	Quantization Quantization `json:"quantization,omitempty"`
	Oversampling int          `json:"oversampling,omitempty"`
}

type Quantization string

const (
	QuantizationNone   Quantization = ""
	QuantizationBinary Quantization = "binary"
)

func (c CollectionConfig) Normalized() CollectionConfig {
	if c.Index.Type == "" {
		c.Index.Type = IndexFlat
	}
	if c.Index.Type == IndexHNSW {
		if c.Index.M == 0 {
			c.Index.M = 16
		}
		if c.Index.EFConstruction == 0 {
			c.Index.EFConstruction = 100
		}
		if c.Index.EFSearch == 0 {
			c.Index.EFSearch = 64
		}
	}
	if c.Index.Quantization == QuantizationBinary && c.Index.Oversampling == 0 {
		c.Index.Oversampling = 4
	}
	return c
}

func (c CollectionConfig) Validate() error {
	c = c.Normalized()
	if c.Name == "" {
		return fmt.Errorf("%w: collection name is required", ErrInvalidArgument)
	}
	if c.Dimension <= 0 {
		return fmt.Errorf("%w: dimension must be positive", ErrInvalidArgument)
	}
	if !c.Metric.Valid() {
		return fmt.Errorf("%w: unsupported metric %q", ErrInvalidArgument, c.Metric)
	}
	if c.ShardCount <= 0 {
		return fmt.Errorf("%w: shard_count must be positive", ErrInvalidArgument)
	}
	if c.Vectorizer != "" && !vectorizerName.MatchString(c.Vectorizer) {
		return fmt.Errorf("%w: vectorizer must match %s", ErrInvalidArgument, vectorizerName)
	}
	switch c.Index.Type {
	case IndexFlat:
	case IndexHNSW:
		if c.Index.M < 2 || c.Index.M > 128 {
			return fmt.Errorf("%w: hnsw m must be between 2 and 128", ErrInvalidArgument)
		}
		if c.Index.EFConstruction < c.Index.M || c.Index.EFConstruction > 10000 {
			return fmt.Errorf("%w: hnsw ef_construction must be between m and 10000", ErrInvalidArgument)
		}
		if c.Index.EFSearch < 1 || c.Index.EFSearch > 10000 {
			return fmt.Errorf("%w: hnsw ef_search must be between 1 and 10000", ErrInvalidArgument)
		}
	default:
		return fmt.Errorf("%w: unsupported index type %q", ErrInvalidArgument, c.Index.Type)
	}
	switch c.Index.Quantization {
	case QuantizationNone:
		if c.Index.Oversampling != 0 {
			return fmt.Errorf("%w: oversampling requires quantization", ErrInvalidArgument)
		}
	case QuantizationBinary:
		if c.Index.Type != IndexFlat {
			return fmt.Errorf("%w: binary quantization requires the flat index", ErrInvalidArgument)
		}
		if c.Index.Oversampling < 1 || c.Index.Oversampling > 100 {
			return fmt.Errorf("%w: oversampling must be between 1 and 100", ErrInvalidArgument)
		}
	default:
		return fmt.Errorf("%w: unsupported quantization %q", ErrInvalidArgument, c.Index.Quantization)
	}
	return nil
}

// Record is the externally visible vector record. Metadata and payload use
// JSON-compatible values at the API boundary.
type Record struct {
	ID           string             `json:"id"`
	Vector       []float32          `json:"vector,omitempty"`
	SparseVector map[string]float32 `json:"sparse_vector,omitempty"`
	Metadata     map[string]any     `json:"metadata,omitempty"`
	Payload      map[string]any     `json:"payload,omitempty"`
	Timestamp    int64              `json:"timestamp"`
	Version      uint64             `json:"version"`
	Namespace    string             `json:"namespace,omitempty"`
	// Text is optional document text indexed for BM25 keyword retrieval.
	Text string `json:"text,omitempty"`
}

// MaxTextBytes bounds per-record text so a single record cannot dominate a
// segment's lexical index.
const MaxTextBytes = 1 << 20

func ValidateText(text string) error {
	if len(text) > MaxTextBytes {
		return fmt.Errorf("%w: text cannot exceed %d bytes", ErrInvalidArgument, MaxTextBytes)
	}
	return nil
}

// FusionMode selects how dense, sparse and text rankings are combined.
type FusionMode string

const (
	// FusionWeighted blends normalized scores with Alpha as the dense weight.
	FusionWeighted FusionMode = "weighted"
	// FusionRRF sums reciprocal ranks, 1/(RRFConstant+rank), per signal.
	FusionRRF FusionMode = "rrf"
)

const RRFConstant = 60

// HybridQuery combines any of dense, sparse and text retrieval. At least one
// of Sparse or Text is required; Dense alone uses the vector search path.
type HybridQuery struct {
	Dense  []float32
	Sparse map[string]float32
	Text   string
	Alpha  float32
	Fusion FusionMode
	TopK   int
}

// Validate checks query shape against the collection dimension.
func (q HybridQuery) Validate(dimension int) error {
	if q.Dense != nil {
		if err := ValidateVector(q.Dense, dimension); err != nil {
			return err
		}
	}
	if q.Sparse == nil && q.Text == "" {
		return fmt.Errorf("%w: hybrid search requires sparse_vector or text", ErrInvalidArgument)
	}
	if q.Sparse != nil {
		if err := ValidateSparseVector(q.Sparse); err != nil {
			return err
		}
	}
	if err := ValidateText(q.Text); err != nil {
		return err
	}
	if q.Alpha < 0 || q.Alpha > 1 {
		return fmt.Errorf("%w: alpha must be between 0 and 1", ErrInvalidArgument)
	}
	if q.TopK <= 0 {
		return fmt.Errorf("%w: top_k must be positive", ErrInvalidArgument)
	}
	switch q.Fusion {
	case "", FusionWeighted, FusionRRF:
	default:
		return fmt.Errorf("%w: unsupported fusion %q", ErrInvalidArgument, q.Fusion)
	}
	return nil
}

// SearchResult is ordered by descending Score. Score is normalized so larger
// always means a better match for every metric.
type SearchResult struct {
	ID        string         `json:"id"`
	Score     float32        `json:"score"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
}
