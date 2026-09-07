// Command gideondb-memory-calibration measures the retained Go heap of an
// isolated in-memory index build and optionally writes an in-use heap profile.
// It is a development tool, not a production capacity estimator.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
	"github.com/Bharanipbk/gideondb/internal/index/flat"
	"github.com/Bharanipbk/gideondb/internal/index/hnsw"
)

type result struct {
	Index                    string      `json:"index"`
	Vectors                  int         `json:"vectors"`
	Dimension                int         `json:"dimension"`
	GoVersion                string      `json:"go_version"`
	GOOS                     string      `json:"goos"`
	GOARCH                   string      `json:"goarch"`
	StructuralStats          index.Stats `json:"structural_stats"`
	StructuralBytes          uint64      `json:"structural_bytes"`
	StructuralBytesPerVector float64     `json:"structural_bytes_per_vector"`
	RetainedHeapBytes        int64       `json:"retained_heap_bytes"`
	RetainedBytesPerVector   float64     `json:"retained_bytes_per_vector"`
	RetainedHeapObjects      int64       `json:"retained_heap_objects"`
	HeapProfile              string      `json:"heap_profile,omitempty"`
}

func main() {
	indexType := flag.String("index", "flat", "index implementation: flat or hnsw")
	vectors := flag.Int("vectors", 50000, "number of vectors to insert")
	dimension := flag.Int("dimension", 128, "vector dimension")
	profilePath := flag.String("heap-profile", "", "optional output path for an in-use heap profile")
	flag.Parse()

	if *vectors <= 0 || *dimension <= 0 {
		fail("vectors and dimension must be positive")
	}

	// Settle runtime initialization before taking the baseline. Each index type
	// should be measured in a separate process so its delta remains isolated.
	debug.FreeOSMemory()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	idx, err := newIndex(*indexType, *dimension)
	if err != nil {
		fail(err.Error())
	}
	vector := make([]float32, *dimension)
	for id := 1; id <= *vectors; id++ {
		fillVector(vector, uint64(id))
		if err := idx.Upsert(uint64(id), vector); err != nil {
			fail(fmt.Sprintf("insert vector %d: %v", id, err))
		}
	}
	vector = nil
	debug.FreeOSMemory()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	stats := idx.Stats()
	structural := stats.VectorBytes + stats.GraphBytes
	measurement := result{
		Index:                    *indexType,
		Vectors:                  *vectors,
		Dimension:                *dimension,
		GoVersion:                runtime.Version(),
		GOOS:                     runtime.GOOS,
		GOARCH:                   runtime.GOARCH,
		StructuralStats:          stats,
		StructuralBytes:          structural,
		StructuralBytesPerVector: float64(structural) / float64(*vectors),
		RetainedHeapBytes:        signedDelta(after.HeapAlloc, before.HeapAlloc),
		RetainedHeapObjects:      signedDelta(after.HeapObjects, before.HeapObjects),
		HeapProfile:              *profilePath,
	}
	measurement.RetainedBytesPerVector = float64(measurement.RetainedHeapBytes) / float64(*vectors)

	if *profilePath != "" {
		profile, err := os.Create(*profilePath)
		if err != nil {
			fail(fmt.Sprintf("create heap profile: %v", err))
		}
		if err := pprof.WriteHeapProfile(profile); err != nil {
			_ = profile.Close()
			fail(fmt.Sprintf("write heap profile: %v", err))
		}
		if err := profile.Close(); err != nil {
			fail(fmt.Sprintf("close heap profile: %v", err))
		}
	}

	if err := json.NewEncoder(os.Stdout).Encode(measurement); err != nil {
		fail(fmt.Sprintf("encode result: %v", err))
	}
	runtime.KeepAlive(idx)
}

func newIndex(indexType string, dimension int) (index.VectorIndex, error) {
	cfg := index.Config{Dimension: dimension, Metric: core.MetricCosine}
	switch indexType {
	case "flat":
		return flat.New(cfg)
	case "hnsw":
		cfg.M = 16
		cfg.EFConstruction = 100
		cfg.EFSearch = 64
		return hnsw.New(cfg)
	default:
		return nil, fmt.Errorf("index must be flat or hnsw, got %q", indexType)
	}
}

func fillVector(vector []float32, id uint64) {
	state := id*0x9e3779b97f4a7c15 + 0x6a09e667f3bcc909
	for i := range vector {
		state ^= state >> 12
		state ^= state << 25
		state ^= state >> 27
		vector[i] = float32(int32(state>>32)) / float32(1<<31)
	}
}

func signedDelta(after, before uint64) int64 {
	if after >= before {
		return int64(after - before)
	}
	return -int64(before - after)
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(2)
}
