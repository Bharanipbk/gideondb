// Package core contains shared domain types without storage or transport
// dependencies.
package core

import (
	"errors"
	"fmt"
	"math"
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

// CollectionConfig is immutable for the lifetime of a collection in v0.1.
type CollectionConfig struct {
	Name       string      `json:"name"`
	Dimension  int         `json:"dimension"`
	Metric     Metric      `json:"metric"`
	ShardCount int         `json:"shard_count"`
	Index      IndexConfig `json:"index"`
}

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
}

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
	return nil
}

// Record is the externally visible vector record. Metadata and payload use
// JSON-compatible values at the API boundary.
type Record struct {
	ID        string         `json:"id"`
	Vector    []float32      `json:"vector,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	Timestamp int64          `json:"timestamp"`
	Version   uint64         `json:"version"`
	Namespace string         `json:"namespace,omitempty"`
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
