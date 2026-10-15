// Command gideondb-bench measures recall, latency, throughput, build time and
// memory for GideonDB index configurations on the same dataset.
//
// Data is either synthetic clustered vectors (default) or the standard .fvecs
// format used by SIFT1M/GIST1M (-base and -queries). Ground truth is computed
// with the exact flat index, so recall@k is relative to exact search.
//
//	go run ./cmd/gideondb-bench -n 100000 -dimension 384
//	go run ./cmd/gideondb-bench -base sift_base.fvecs -queries sift_query.fvecs -metric l2
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/segment"
)

type result struct {
	Config         string  `json:"config"`
	RecallAtK      float64 `json:"recall_at_k"`
	QPS            float64 `json:"qps"`
	P50Millis      float64 `json:"p50_ms"`
	P99Millis      float64 `json:"p99_ms"`
	BuildSeconds   float64 `json:"build_seconds"`
	SegmentHeapMiB float64 `json:"segment_heap_mib"`
	Vectors        int     `json:"vectors"`
	Dimension      int     `json:"dimension"`
	Queries        int     `json:"queries"`
	K              int     `json:"k"`
	Metric         string  `json:"metric"`
	Dataset        string  `json:"dataset"`
	GoVersion      string  `json:"go_version"`
	Architecture   string  `json:"architecture"`
	CPUs           int     `json:"cpus"`
}

type benchConfig struct {
	name  string
	index core.IndexConfig
}

func main() {
	n := flag.Int("n", 50_000, "synthetic vectors (ignored with -base)")
	dimension := flag.Int("dimension", 128, "synthetic dimension (ignored with -base)")
	clusters := flag.Int("clusters", 100, "synthetic topic clusters; 0 for uniform Gaussian (worst case)")
	intrinsic := flag.Int("intrinsic", 32, "synthetic intrinsic dimension; 0 for full-rank noise (hard case)")
	queryCount := flag.Int("query-count", 200, "synthetic queries, or a cap on -queries")
	k := flag.Int("k", 10, "neighbours per query")
	metricName := flag.String("metric", "cosine", "cosine, dot or l2")
	basePath := flag.String("base", "", ".fvecs base vectors")
	queriesPath := flag.String("queries", "", ".fvecs query vectors")
	configsFlag := flag.String("configs", "flat,hnsw,hnsw-ef128,binary,binary-x10", "comma-separated configurations")
	jsonPath := flag.String("json", "", "write results as JSON to this path")
	seed := flag.Int64("seed", 42, "synthetic data seed")
	flag.Parse()

	metric := core.Metric(*metricName)
	if !metric.Valid() {
		fail(fmt.Errorf("unsupported metric %q", *metricName))
	}
	var base, queries [][]float32
	dataset := "synthetic"
	if *basePath != "" {
		var err error
		if base, err = readFvecs(*basePath, 0); err != nil {
			fail(err)
		}
		if queries, err = readFvecs(*queriesPath, *queryCount); err != nil {
			fail(err)
		}
		dataset = *basePath
	} else {
		base, queries = synthetic(*seed, *n, *queryCount, *dimension, *clusters, *intrinsic)
		dataset = fmt.Sprintf("synthetic(clusters=%d,intrinsic=%d)", *clusters, *intrinsic)
	}
	if len(base) == 0 || len(queries) == 0 {
		fail(fmt.Errorf("dataset is empty"))
	}
	dim := len(base[0])
	fmt.Fprintf(os.Stderr, "dataset=%s vectors=%d queries=%d dimension=%d metric=%s k=%d\n", dataset, len(base), len(queries), dim, metric, *k)

	available := map[string]core.IndexConfig{
		"flat":       {Type: core.IndexFlat},
		"hnsw":       {Type: core.IndexHNSW},
		"hnsw-ef128": {Type: core.IndexHNSW, EFSearch: 128},
		"hnsw-ef256": {Type: core.IndexHNSW, EFSearch: 256},
		"binary":     {Type: core.IndexFlat, Quantization: core.QuantizationBinary},
		"binary-x10": {Type: core.IndexFlat, Quantization: core.QuantizationBinary, Oversampling: 10},
		"binary-x20": {Type: core.IndexFlat, Quantization: core.QuantizationBinary, Oversampling: 20},
	}
	var configs []benchConfig
	for _, name := range strings.Split(*configsFlag, ",") {
		index, ok := available[strings.TrimSpace(name)]
		if !ok {
			fail(fmt.Errorf("unknown config %q", name))
		}
		configs = append(configs, benchConfig{strings.TrimSpace(name), index})
	}

	fmt.Fprintln(os.Stderr, "computing exact ground truth…")
	truth := groundTruth(base, queries, metric, *k)
	var results []result
	for _, config := range configs {
		fmt.Fprintf(os.Stderr, "running %s…\n", config.name)
		r := run(config, base, queries, truth, metric, *k)
		r.Dataset = dataset
		results = append(results, r)
	}
	printMarkdown(results)
	if *jsonPath != "" {
		data, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(*jsonPath, append(data, '\n'), 0o644); err != nil {
			fail(err)
		}
	}
}

func run(config benchConfig, base, queries [][]float32, truth [][]string, metric core.Metric, k int) result {
	collection := core.CollectionConfig{Name: "bench", Dimension: len(base[0]), Metric: metric, ShardCount: 1, Index: config.index}.Normalized()
	if err := collection.Validate(); err != nil {
		fail(err)
	}
	settle()
	before := heapAlloc()
	started := time.Now()
	seg, err := segment.New(collection)
	if err != nil {
		fail(err)
	}
	for i, vector := range base {
		if err := seg.Upsert(core.Record{ID: id(i), Vector: vector}); err != nil {
			fail(err)
		}
	}
	build := time.Since(started)
	settle()
	after := heapAlloc()
	heap := float64(after-min(before, after)) / (1 << 20)

	latencies := make([]time.Duration, len(queries))
	hits := 0
	queryStarted := time.Now()
	for i, query := range queries {
		begin := time.Now()
		found, err := seg.SearchFilteredWithOptions("", query, k, nil, nil, config.index.EFSearch)
		latencies[i] = time.Since(begin)
		if err != nil {
			fail(err)
		}
		expected := map[string]struct{}{}
		for _, want := range truth[i] {
			expected[want] = struct{}{}
		}
		for _, got := range found {
			if _, ok := expected[got.ID]; ok {
				hits++
			}
		}
	}
	elapsed := time.Since(queryStarted)
	runtime.KeepAlive(seg)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return result{
		Config: config.name, RecallAtK: float64(hits) / float64(len(queries)*k), QPS: float64(len(queries)) / elapsed.Seconds(),
		P50Millis: millis(percentile(latencies, 0.50)), P99Millis: millis(percentile(latencies, 0.99)),
		BuildSeconds: build.Seconds(), SegmentHeapMiB: heap, Vectors: len(base), Dimension: len(base[0]),
		Queries: len(queries), K: k, Metric: string(metric), GoVersion: runtime.Version(),
		Architecture: runtime.GOOS + "/" + runtime.GOARCH, CPUs: runtime.NumCPU(),
	}
}

func groundTruth(base, queries [][]float32, metric core.Metric, k int) [][]string {
	exact, err := segment.New(core.CollectionConfig{Name: "truth", Dimension: len(base[0]), Metric: metric, ShardCount: 1}.Normalized())
	if err != nil {
		fail(err)
	}
	for i, vector := range base {
		if err := exact.Upsert(core.Record{ID: id(i), Vector: vector}); err != nil {
			fail(err)
		}
	}
	truth := make([][]string, len(queries))
	for i, query := range queries {
		found, err := exact.Search("", query, k)
		if err != nil {
			fail(err)
		}
		for _, item := range found {
			truth[i] = append(truth[i], item.ID)
		}
	}
	return truth
}

// synthetic draws points around topic centroids. With intrinsic > 0 the
// within-topic variation lies on a random low-dimensional subspace plus small
// isotropic noise, which resembles real embedding distributions. intrinsic=0
// gives full-rank noise, a worst case for every ANN method.
func synthetic(seed int64, n, queryCount, dimension, clusters, intrinsic int) ([][]float32, [][]float32) {
	random := rand.New(rand.NewSource(seed))
	gaussian := func(size int) []float32 {
		v := make([]float32, size)
		for i := range v {
			v[i] = float32(random.NormFloat64())
		}
		return v
	}
	centers := make([][]float32, max(clusters, 1))
	for i := range centers {
		centers[i] = gaussian(dimension)
	}
	var projection [][]float32
	if intrinsic > 0 {
		projection = make([][]float32, intrinsic)
		for i := range projection {
			projection[i] = gaussian(dimension)
		}
	}
	point := func() []float32 {
		v := make([]float32, dimension)
		if clusters > 0 {
			copy(v, centers[random.Intn(clusters)])
		}
		if projection == nil {
			for i, noise := range gaussian(dimension) {
				v[i] += 0.7 * noise
			}
			return v
		}
		scale := float32(0.7 / math.Sqrt(float64(intrinsic)))
		for _, row := range projection {
			weight := float32(random.NormFloat64()) * scale
			for i, value := range row {
				v[i] += weight * value
			}
		}
		for i, noise := range gaussian(dimension) {
			v[i] += 0.05 * noise
		}
		return v
	}
	base := make([][]float32, n)
	for i := range base {
		base[i] = point()
	}
	queries := make([][]float32, queryCount)
	for i := range queries {
		queries[i] = point()
	}
	return base, queries
}

// readFvecs reads little-endian records of [int32 dimension][dimension float32].
func readFvecs(path string, limit int) ([][]float32, error) {
	if path == "" {
		return nil, fmt.Errorf("-queries is required with -base")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var vectors [][]float32
	header := make([]byte, 4)
	for limit == 0 || len(vectors) < limit {
		if _, err := io.ReadFull(file, header); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		dimension := int(binary.LittleEndian.Uint32(header))
		if dimension <= 0 || dimension > 65536 {
			return nil, fmt.Errorf("%s: invalid dimension %d", path, dimension)
		}
		raw := make([]byte, dimension*4)
		if _, err := io.ReadFull(file, raw); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		vector := make([]float32, dimension)
		for i := range vector {
			vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
		}
		vectors = append(vectors, vector)
	}
	return vectors, nil
}

func printMarkdown(results []result) {
	fmt.Println("| Config | Recall@k | QPS | p50 ms | p99 ms | Build s | Segment heap MiB |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|")
	for _, r := range results {
		fmt.Printf("| %s | %.3f | %.0f | %.3f | %.3f | %.2f | %.1f |\n", r.Config, r.RecallAtK, r.QPS, r.P50Millis, r.P99Millis, r.BuildSeconds, r.SegmentHeapMiB)
	}
}

// settle forces collection of earlier runs so heap deltas reflect only the
// segment being measured.
func settle() {
	runtime.GC()
	debug.FreeOSMemory()
}

func heapAlloc() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

func percentile(values []time.Duration, p float64) time.Duration {
	return values[min(len(values)-1, int(math.Ceil(p*float64(len(values))))-1)]
}

func millis(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
func id(i int) string                { return fmt.Sprintf("v%d", i) }

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gideondb-bench:", err)
	os.Exit(1)
}
