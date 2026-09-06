// Command gideondb-loadtest runs a reproducible single-node validation workload.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
	"github.com/Bharanipbk/gideondb/internal/wal"
)

type report struct {
	Vectors                int     `json:"vectors"`
	Dimension              int     `json:"dimension"`
	Shards                 int     `json:"shards"`
	Concurrency            int     `json:"concurrency"`
	Queries                int     `json:"queries"`
	WALSync                string  `json:"wal_sync"`
	GoVersion              string  `json:"go_version"`
	OS                     string  `json:"os"`
	Architecture           string  `json:"architecture"`
	CPUs                   int     `json:"cpus"`
	IngestSeconds          float64 `json:"ingest_seconds"`
	IngestVectorsPerSecond float64 `json:"ingest_vectors_per_second"`
	CheckpointSeconds      float64 `json:"checkpoint_seconds"`
	RecoverySeconds        float64 `json:"recovery_seconds"`
	QuerySeconds           float64 `json:"query_seconds"`
	QueriesPerSecond       float64 `json:"queries_per_second"`
	LatencyP50Millis       float64 `json:"latency_p50_ms"`
	LatencyP95Millis       float64 `json:"latency_p95_ms"`
	LatencyP99Millis       float64 `json:"latency_p99_ms"`
	HeapAllocBytes         uint64  `json:"heap_alloc_bytes"`
	HeapSysBytes           uint64  `json:"heap_sys_bytes"`
	Verified               bool    `json:"verified"`
}

func main() {
	vectors := flag.Int("vectors", 1_000_000, "number of vectors")
	dimension := flag.Int("dimension", 128, "vector dimension")
	shards := flag.Int("shards", 8, "logical shards")
	queries := flag.Int("queries", 1000, "queries after recovery")
	concurrency := flag.Int("concurrency", runtime.GOMAXPROCS(0), "concurrent query workers")
	batchSize := flag.Int("batch-size", 1000, "ingest batch size")
	dataPath := flag.String("data-path", "", "data directory; empty uses a temporary directory")
	preserve := flag.Bool("preserve", false, "preserve an automatically created data directory")
	walSync := flag.String("wal-sync", "always", "WAL durability: always or async")
	flag.Parse()
	if *vectors <= 0 || *dimension <= 0 || *shards <= 0 || *queries <= 0 || *concurrency <= 0 || *batchSize <= 0 {
		fail(fmt.Errorf("all numeric arguments must be positive"))
	}
	path := *dataPath
	if path == "" {
		var err error
		path, err = os.MkdirTemp("", "gideondb-loadtest-*")
		if err != nil {
			fail(err)
		}
		if !*preserve {
			defer os.RemoveAll(path)
		} else {
			fmt.Fprintln(os.Stderr, "preserving data path:", path)
		}
	}
	db, err := engine.OpenWithOptions(path, engine.Options{WALSyncMode: wal.SyncMode(*walSync), CheckpointEvery: 0})
	if err != nil {
		fail(err)
	}
	config := core.CollectionConfig{Name: "loadtest", Dimension: *dimension, Metric: core.MetricCosine, ShardCount: *shards, Index: core.IndexConfig{Type: core.IndexFlat}}
	if err := db.CreateCollection(config); err != nil {
		fail(err)
	}
	started := time.Now()
	for first := 0; first < *vectors; first += *batchSize {
		count := min(*batchSize, *vectors-first)
		batch := make([]core.Record, count)
		for offset := range batch {
			id := first + offset + 1
			batch[offset] = core.Record{ID: fmt.Sprintf("v-%09d", id), Vector: deterministicVector(uint64(id), *dimension)}
		}
		if _, err := db.BatchUpsert(config.Name, batch); err != nil {
			fail(err)
		}
	}
	ingestDuration := time.Since(started)
	started = time.Now()
	if err := db.Checkpoint(config.Name); err != nil {
		fail(err)
	}
	checkpointDuration := time.Since(started)
	if err := db.Close(); err != nil {
		fail(err)
	}
	started = time.Now()
	db, err = engine.Open(path)
	if err != nil {
		fail(err)
	}
	recoveryDuration := time.Since(started)
	defer db.Close()
	_, count, err := db.DescribeCollection(config.Name)
	if err != nil || count != *vectors {
		fail(fmt.Errorf("recovered count=%d want=%d err=%v", count, *vectors, err))
	}
	for sample := 0; sample < min(100, *vectors); sample++ {
		id := sample*(*vectors/min(100, *vectors)) + 1
		record, err := db.Get(config.Name, "", fmt.Sprintf("v-%09d", id))
		if err != nil || len(record.Vector) != *dimension {
			fail(fmt.Errorf("recovery sample %d failed: %v", id, err))
		}
	}
	latencies := make([]time.Duration, *queries)
	var next atomic.Int64
	var wait sync.WaitGroup
	var errorMu sync.Mutex
	var firstError error
	started = time.Now()
	for range *concurrency {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				position := int(next.Add(1) - 1)
				if position >= *queries {
					return
				}
				id := position%*vectors + 1
				queryStarted := time.Now()
				results, err := db.Search(config.Name, "", deterministicVector(uint64(id), *dimension), 10)
				latencies[position] = time.Since(queryStarted)
				found := false
				wantID := fmt.Sprintf("v-%09d", id)
				for _, result := range results {
					if result.ID == wantID {
						found = true
						break
					}
				}
				if err != nil || !found {
					errorMu.Lock()
					if firstError == nil {
						firstError = fmt.Errorf("query %d did not return %s: results=%d err=%v", position, wantID, len(results), err)
					}
					errorMu.Unlock()
					return
				}
			}
		}()
	}
	wait.Wait()
	queryDuration := time.Since(started)
	if firstError != nil {
		fail(firstError)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	runtime.GC()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	result := report{
		Vectors: *vectors, Dimension: *dimension, Shards: *shards, Concurrency: *concurrency, Queries: *queries, WALSync: *walSync,
		GoVersion: runtime.Version(), OS: runtime.GOOS, Architecture: runtime.GOARCH, CPUs: runtime.NumCPU(),
		IngestSeconds: ingestDuration.Seconds(), IngestVectorsPerSecond: float64(*vectors) / ingestDuration.Seconds(),
		CheckpointSeconds: checkpointDuration.Seconds(), RecoverySeconds: recoveryDuration.Seconds(), QuerySeconds: queryDuration.Seconds(), QueriesPerSecond: float64(*queries) / queryDuration.Seconds(),
		LatencyP50Millis: millis(percentile(latencies, 0.50)), LatencyP95Millis: millis(percentile(latencies, 0.95)), LatencyP99Millis: millis(percentile(latencies, 0.99)),
		HeapAllocBytes: memory.HeapAlloc, HeapSysBytes: memory.HeapSys, Verified: true,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fail(err)
	}
}

func deterministicVector(id uint64, dimension int) []float32 {
	vector := make([]float32, dimension)
	state := id + 0x9e3779b97f4a7c15
	for position := range vector {
		state ^= state >> 12
		state ^= state << 25
		state ^= state >> 27
		value := state * 0x2545f4914f6cdd1d
		vector[position] = float32(int32(value>>32)) / float32(1<<31)
	}
	return vector
}

func percentile(values []time.Duration, quantile float64) time.Duration {
	position := int(quantile*float64(len(values)-1) + 0.5)
	return values[position]
}
func millis(value time.Duration) float64 { return float64(value.Microseconds()) / 1000 }
func fail(err error)                     { fmt.Fprintln(os.Stderr, "gideondb-loadtest:", err); os.Exit(1) }
