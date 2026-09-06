// Package index defines the local vector index contract.
package index

import "github.com/Bharanipbk/gideondb/internal/core"

type Candidate struct {
	ID    uint64
	Score float32
}

type VectorIndex interface {
	Upsert(id uint64, vector []float32) error
	Delete(id uint64) error
	Search(vector []float32, k int) ([]Candidate, error)
	SearchFiltered(vector []float32, k int, allowed func(id uint64) bool) ([]Candidate, error)
	Len() int
	Stats() Stats
}

type Stats struct {
	Type         core.IndexType
	Vectors      int
	VectorBytes  uint64
	MappedBytes  uint64
	GraphBytes   uint64
	GraphEdges   uint64
	Deleted      int
	MaximumLevel int
}

type Config struct {
	Dimension      int
	Metric         core.Metric
	M              int
	EFConstruction int
	EFSearch       int
}
