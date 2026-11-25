package main

// compare mode drives GideonDB, Qdrant and OpenSearch with the same data,
// the same HNSW parameters and the same queries, and reports recall, QPS,
// latency, build time and container memory for each engine at every
// ef_search value in a sweep. Ground truth is exact search computed locally.

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
)

type searchEngine interface {
	name() string
	setup(dimension int, metric core.Metric, m, efConstruction int) error
	ingest(first int, vectors [][]float32) error
	// finish waits until the index is built and searchable.
	finish() error
	search(query []float32, k, ef int) ([]int, error)
	teardown()
}

type compareRow struct {
	Engine        string  `json:"engine"`
	EFSearch      int     `json:"ef_search"`
	RecallAtK     float64 `json:"recall_at_k"`
	QPS           float64 `json:"qps"`
	P50Millis     float64 `json:"p50_ms"`
	P99Millis     float64 `json:"p99_ms"`
	IngestSeconds float64 `json:"ingest_seconds"`
	BuildSeconds  float64 `json:"build_seconds"`
	MemoryMiB     float64 `json:"memory_mib,omitempty"`
}

type compareReport struct {
	Dataset        string       `json:"dataset"`
	Vectors        int          `json:"vectors"`
	Dimension      int          `json:"dimension"`
	Metric         string       `json:"metric"`
	K              int          `json:"k"`
	M              int          `json:"m"`
	EFConstruction int          `json:"ef_construction"`
	Concurrency    int          `json:"concurrency"`
	Queries        int          `json:"queries"`
	Host           string       `json:"host"`
	Rows           []compareRow `json:"rows"`
}

func runCompare(arguments []string) {
	flags := flag.NewFlagSet("compare", flag.ExitOnError)
	engines := flags.String("engines", "gideondb,qdrant,opensearch", "engines to run, comma separated")
	gideonURL := flags.String("gideondb-url", "https://127.0.0.1:16333", "GideonDB REST URL")
	gideonInsecure := flags.Bool("gideondb-insecure", true, "accept GideonDB's self-signed benchmark certificate")
	gideonKeyFile := flags.String("gideondb-api-key-file", "", "file holding the GideonDB API key")
	qdrantURL := flags.String("qdrant-url", "http://127.0.0.1:16334", "Qdrant REST URL")
	openSearchURL := flags.String("opensearch-url", "http://127.0.0.1:19201", "OpenSearch URL")
	containers := flags.String("containers", "gideondb=bench-gideondb,qdrant=bench-qdrant,opensearch=bench-opensearch", "engine=container pairs for memory readings (docker stats)")
	basePath := flags.String("base", "", ".fvecs base vectors (required)")
	queriesPath := flags.String("queries", "", ".fvecs query vectors (required)")
	limit := flags.Int("limit", 0, "load at most this many base vectors; 0 loads all")
	queryCount := flags.Int("query-count", 1000, "queries per ef_search value")
	metricName := flags.String("metric", "l2", "cosine, dot or l2")
	k := flags.Int("k", 10, "neighbours per query")
	m := flags.Int("m", 16, "HNSW M for every engine")
	efConstruction := flags.Int("ef-construction", 100, "HNSW ef_construction for every engine")
	sweep := flags.String("ef-sweep", "16,32,64,128,256", "ef_search values")
	concurrency := flags.Int("concurrency", 16, "concurrent query clients")
	ingestConcurrency := flags.Int("ingest-concurrency", 4, "concurrent batch clients")
	batchSize := flags.Int("batch-size", 500, "vectors per batch")
	markdown := flags.Bool("markdown", true, "also print a Markdown table to stderr")
	_ = flags.Parse(arguments)
	if *basePath == "" || *queriesPath == "" {
		fail(fmt.Errorf("-base and -queries are required in compare mode"))
	}
	metric := core.Metric(*metricName)
	if !metric.Valid() {
		fail(fmt.Errorf("unsupported metric %q", *metricName))
	}
	base, err := readFvecs(*basePath, *limit)
	if err != nil {
		fail(err)
	}
	queries, err := readFvecs(*queriesPath, *queryCount)
	if err != nil {
		fail(err)
	}
	if len(base) == 0 || len(queries) == 0 {
		fail(fmt.Errorf("dataset is empty"))
	}
	var efs []int
	for _, field := range strings.Split(*sweep, ",") {
		value, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || value <= 0 {
			fail(fmt.Errorf("bad -ef-sweep value %q", field))
		}
		efs = append(efs, value)
	}
	containerOf := map[string]string{}
	for _, pair := range strings.Split(*containers, ",") {
		if name, container, ok := strings.Cut(pair, "="); ok {
			containerOf[name] = container
		}
	}

	fmt.Fprintln(os.Stderr, "computing exact ground truth…")
	truth := make([][]int, len(queries))
	parallel(len(queries), runtime.GOMAXPROCS(0), func(i int) error {
		truth[i] = exactNeighbours(metric, base, queries[i], *k, func(int) bool { return true })
		return nil
	})

	httpClient := &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{MaxIdleConnsPerHost: *concurrency * 2}}
	report := compareReport{
		Dataset: *basePath, Vectors: len(base), Dimension: len(base[0]), Metric: string(metric), K: *k, M: *m,
		EFConstruction: *efConstruction, Concurrency: *concurrency, Queries: len(queries),
		Host: fmt.Sprintf("%s/%s, %d CPUs", runtime.GOOS, runtime.GOARCH, runtime.NumCPU()),
	}
	for _, name := range strings.Split(*engines, ",") {
		var e searchEngine
		switch strings.TrimSpace(name) {
		case "gideondb":
			gideonHTTP := httpClient
			if *gideonInsecure {
				gideonHTTP = &http.Client{Timeout: httpClient.Timeout, Transport: &http.Transport{MaxIdleConnsPerHost: *concurrency * 2, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			}
			client := &apiClient{base: strings.TrimRight(*gideonURL, "/"), http: gideonHTTP}
			if *gideonKeyFile != "" {
				key, err := os.ReadFile(*gideonKeyFile)
				if err != nil {
					fail(err)
				}
				client.key = strings.TrimSpace(string(key))
			}
			e = &gideonEngine{client: client}
		case "qdrant":
			e = &qdrantEngine{client: &apiClient{base: strings.TrimRight(*qdrantURL, "/"), http: httpClient}}
		case "opensearch":
			e = &openSearchEngine{client: &apiClient{base: strings.TrimRight(*openSearchURL, "/"), http: httpClient}}
		default:
			fail(fmt.Errorf("unknown engine %q", name))
		}
		rows, err := benchmarkEngine(e, base, queries, truth, metric, *k, *m, *efConstruction, efs, *concurrency, *ingestConcurrency, *batchSize)
		if err != nil {
			fail(fmt.Errorf("%s: %w", e.name(), err))
		}
		memory := containerMemoryMiB(containerOf[e.name()])
		for i := range rows {
			rows[i].MemoryMiB = memory
		}
		report.Rows = append(report.Rows, rows...)
		e.teardown()
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fail(err)
	}
	if *markdown {
		fmt.Fprintln(os.Stderr, "\n| Engine | ef_search | Recall@k | QPS | p50 ms | p99 ms | Ingest s | Build s | Memory MiB |")
		fmt.Fprintln(os.Stderr, "|---|---:|---:|---:|---:|---:|---:|---:|---:|")
		for _, r := range report.Rows {
			fmt.Fprintf(os.Stderr, "| %s | %d | %.3f | %.0f | %.2f | %.2f | %.1f | %.1f | %.0f |\n",
				r.Engine, r.EFSearch, r.RecallAtK, r.QPS, r.P50Millis, r.P99Millis, r.IngestSeconds, r.BuildSeconds, r.MemoryMiB)
		}
	}
}

func benchmarkEngine(e searchEngine, base, queries [][]float32, truth [][]int, metric core.Metric, k, m, efConstruction int, efs []int, concurrency, ingestConcurrency, batchSize int) ([]compareRow, error) {
	fmt.Fprintf(os.Stderr, "[%s] creating index…\n", e.name())
	e.teardown()
	if err := e.setup(len(base[0]), metric, m, efConstruction); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "[%s] ingesting %d vectors…\n", e.name(), len(base))
	started := time.Now()
	var ingestErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				ingestErr = fmt.Errorf("%v", r)
			}
		}()
		ingestErr = parallelErr((len(base)+batchSize-1)/batchSize, ingestConcurrency, func(batch int) error {
			first := batch * batchSize
			// Upserts are idempotent, so transient transport errors (Docker
			// Desktop's port proxy drops connections under load) are retried.
			var err error
			for attempt := range 4 {
				if err = e.ingest(first, base[first:min(first+batchSize, len(base))]); err == nil {
					return nil
				}
				time.Sleep(time.Duration(attempt+1) * time.Second)
			}
			return err
		})
	}()
	if ingestErr != nil {
		return nil, ingestErr
	}
	ingest := time.Since(started)
	fmt.Fprintf(os.Stderr, "[%s] waiting for the index…\n", e.name())
	built := time.Now()
	if err := e.finish(); err != nil {
		return nil, err
	}
	build := time.Since(built)

	// Warm up: one pass at the largest ef, unmeasured.
	if err := parallelErr(min(len(queries), 200), concurrency, func(i int) error {
		_, err := e.search(queries[i], k, efs[len(efs)-1])
		return err
	}); err != nil {
		return nil, err
	}

	var rows []compareRow
	for _, ef := range efs {
		fmt.Fprintf(os.Stderr, "[%s] querying ef_search=%d…\n", e.name(), ef)
		latencies := make([]time.Duration, len(queries))
		var hits atomic.Int64
		begin := time.Now()
		err := parallelErr(len(queries), concurrency, func(i int) error {
			start := time.Now()
			ids, err := e.search(queries[i], k, ef)
			if err != nil {
				return err
			}
			latencies[i] = time.Since(start)
			// Tie-aware recall: a result counts when it scores at least as
			// well as the k-th true neighbour, so engines are not penalized
			// for returning an equally close duplicate (SIFT has many).
			threshold, _ := distance.Score(metric, queries[i], base[truth[i][len(truth[i])-1]])
			threshold -= 1e-4 * float32(math.Max(1, math.Abs(float64(threshold))))
			seen := make(map[int]struct{}, len(ids))
			for _, id := range ids {
				if _, dup := seen[id]; dup || id < 0 || id >= len(base) {
					continue
				}
				seen[id] = struct{}{}
				if score, _ := distance.Score(metric, queries[i], base[id]); score >= threshold {
					hits.Add(1)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		elapsed := time.Since(begin)
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		rows = append(rows, compareRow{
			Engine: e.name(), EFSearch: ef, RecallAtK: float64(hits.Load()) / float64(len(queries)*k),
			QPS: float64(len(queries)) / elapsed.Seconds(), P50Millis: millis(percentile(latencies, 0.50)),
			P99Millis: millis(percentile(latencies, 0.99)), IngestSeconds: ingest.Seconds(), BuildSeconds: build.Seconds(),
		})
	}
	return rows, nil
}

// parallelErr is parallel that returns the first error instead of exiting.
func parallelErr(count, workers int, work func(int) error) error {
	var first atomic.Pointer[error]
	parallel(count, workers, func(i int) error {
		if first.Load() != nil {
			return nil
		}
		if err := work(i); err != nil {
			first.CompareAndSwap(nil, &err)
		}
		return nil
	})
	if p := first.Load(); p != nil {
		return *p
	}
	return nil
}

func containerMemoryMiB(container string) float64 {
	if container == "" {
		return 0
	}
	output, err := exec.Command("docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", container).Output()
	if err != nil {
		return 0
	}
	used, _, _ := strings.Cut(strings.TrimSpace(string(output)), "/")
	used = strings.TrimSpace(used)
	units := []struct {
		suffix string
		scale  float64
	}{{"GiB", 1024}, {"MiB", 1}, {"KiB", 1.0 / 1024}, {"GB", 1000 * 1000 * 1000 / (1 << 20)}, {"MB", 1000 * 1000 / (1 << 20)}, {"kB", 1000.0 / (1 << 20)}, {"B", 1.0 / (1 << 20)}}
	for _, unit := range units {
		if strings.HasSuffix(used, unit.suffix) {
			value, err := strconv.ParseFloat(strings.TrimSuffix(used, unit.suffix), 64)
			if err != nil {
				return 0
			}
			return value * unit.scale
		}
	}
	return 0
}

const benchCollection = "compare"

// --- GideonDB ---

type gideonEngine struct{ client *apiClient }

func (g *gideonEngine) name() string { return "gideondb" }
func (g *gideonEngine) setup(dimension int, metric core.Metric, m, efConstruction int) error {
	return g.client.do(http.MethodPost, "/v1/collections", map[string]any{
		"name": benchCollection, "dimension": dimension, "metric": metric, "shard_count": 1,
		"index": map[string]any{"type": "hnsw", "m": m, "ef_construction": efConstruction},
	}, nil)
}
func (g *gideonEngine) ingest(first int, vectors [][]float32) error {
	records := make([]map[string]any, len(vectors))
	for i, vector := range vectors {
		records[i] = map[string]any{"id": recordID(first + i), "vector": vector}
	}
	return g.client.do(http.MethodPost, "/v1/collections/"+benchCollection+"/vectors/batch", map[string]any{"records": records}, nil)
}
func (g *gideonEngine) finish() error { return nil } // HNSW is built on insert
func (g *gideonEngine) search(query []float32, k, ef int) ([]int, error) {
	var response struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := g.client.do(http.MethodPost, "/v1/collections/"+benchCollection+"/search", map[string]any{"vector": query, "top_k": k, "ef_search": ef}, &response); err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(response.Results))
	for _, result := range response.Results {
		if ordinal, err := strconv.Atoi(strings.TrimPrefix(result.ID, "v")); err == nil {
			ids = append(ids, ordinal)
		}
	}
	return ids, nil
}
func (g *gideonEngine) teardown() {
	_ = g.client.do(http.MethodDelete, "/v1/collections/"+benchCollection, nil, nil)
}

// --- Qdrant ---

type qdrantEngine struct{ client *apiClient }

func (q *qdrantEngine) name() string { return "qdrant" }
func (q *qdrantEngine) setup(dimension int, metric core.Metric, m, efConstruction int) error {
	distance := map[core.Metric]string{core.Metric("l2"): "Euclid", core.Metric("cosine"): "Cosine", core.Metric("dot"): "Dot"}[metric]
	return q.client.do(http.MethodPut, "/collections/"+benchCollection, map[string]any{
		"vectors":     map[string]any{"size": dimension, "distance": distance},
		"hnsw_config": map[string]any{"m": m, "ef_construct": efConstruction, "full_scan_threshold": 10},
		// Build HNSW for any segment size; the default threshold (10 MB)
		// would leave small datasets on brute force.
		"optimizers_config": map[string]any{"indexing_threshold": 1},
	}, nil)
}
func (q *qdrantEngine) ingest(first int, vectors [][]float32) error {
	points := make([]map[string]any, len(vectors))
	for i, vector := range vectors {
		points[i] = map[string]any{"id": first + i, "vector": vector}
	}
	return q.client.do(http.MethodPut, "/collections/"+benchCollection+"/points?wait=true", map[string]any{"points": points}, nil)
}
func (q *qdrantEngine) finish() error {
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		var info struct {
			Result struct {
				Status string `json:"status"`
			} `json:"result"`
		}
		if err := q.client.do(http.MethodGet, "/collections/"+benchCollection, nil, &info); err != nil {
			return err
		}
		if info.Result.Status == "green" {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("index not green after 30 minutes")
}
func (q *qdrantEngine) search(query []float32, k, ef int) ([]int, error) {
	var response struct {
		Result struct {
			Points []struct {
				ID int `json:"id"`
			} `json:"points"`
		} `json:"result"`
	}
	if err := q.client.do(http.MethodPost, "/collections/"+benchCollection+"/points/query", map[string]any{
		"query": query, "limit": k, "params": map[string]any{"hnsw_ef": ef},
	}, &response); err != nil {
		return nil, err
	}
	ids := make([]int, len(response.Result.Points))
	for i, point := range response.Result.Points {
		ids[i] = point.ID
	}
	return ids, nil
}
func (q *qdrantEngine) teardown() {
	_ = q.client.do(http.MethodDelete, "/collections/"+benchCollection, nil, nil)
}

// --- OpenSearch (k-NN plugin, faiss HNSW) ---

type openSearchEngine struct {
	client    *apiClient
	normalize bool // cosine is run as inner product over unit vectors
}

func (o *openSearchEngine) name() string { return "opensearch" }
func (o *openSearchEngine) setup(dimension int, metric core.Metric, m, efConstruction int) error {
	space := "l2"
	switch metric {
	case core.Metric("cosine"):
		space, o.normalize = "innerproduct", true
	case core.Metric("dot"):
		space = "innerproduct"
	}
	return o.client.do(http.MethodPut, "/"+benchCollection, map[string]any{
		"settings": map[string]any{"index": map[string]any{
			"knn": true, "number_of_shards": 1, "number_of_replicas": 0, "refresh_interval": "-1",
		}},
		"mappings": map[string]any{"properties": map[string]any{"v": map[string]any{
			"type": "knn_vector", "dimension": dimension,
			"method": map[string]any{"name": "hnsw", "engine": "faiss", "space_type": space,
				"parameters": map[string]any{"m": m, "ef_construction": efConstruction}},
		}}},
	}, nil)
}
func (o *openSearchEngine) ingest(first int, vectors [][]float32) error {
	var body bytes.Buffer
	for i, vector := range vectors {
		fmt.Fprintf(&body, "{\"index\":{\"_index\":%q,\"_id\":\"%d\"}}\n", benchCollection, first+i)
		line, _ := json.Marshal(map[string]any{"v": o.prepare(vector)})
		body.Write(line)
		body.WriteByte('\n')
	}
	request, err := http.NewRequest(http.MethodPost, o.client.base+"/_bulk", &body)
	if err != nil {
		return err
	}
	request.Header.Set("content-type", "application/x-ndjson")
	response, err := o.client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var result struct {
		Errors bool `json:"errors"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	if response.StatusCode >= 300 || result.Errors {
		return fmt.Errorf("bulk failed: %s", response.Status)
	}
	return nil
}
func (o *openSearchEngine) finish() error {
	// Refresh, merge to one segment (one graph, like the other engines),
	// then load the graph into native memory.
	if err := o.client.do(http.MethodPost, "/"+benchCollection+"/_refresh", nil, nil); err != nil {
		return err
	}
	if err := o.client.do(http.MethodPost, "/"+benchCollection+"/_forcemerge?max_num_segments=1", nil, nil); err != nil {
		return err
	}
	if err := o.client.do(http.MethodPost, "/"+benchCollection+"/_refresh", nil, nil); err != nil {
		return err
	}
	return o.client.do(http.MethodGet, "/_plugins/_knn/warmup/"+benchCollection, nil, nil)
}
func (o *openSearchEngine) search(query []float32, k, ef int) ([]int, error) {
	var response struct {
		Hits struct {
			Hits []struct {
				ID string `json:"_id"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := o.client.do(http.MethodPost, "/"+benchCollection+"/_search", map[string]any{
		"size": k, "_source": false,
		"query": map[string]any{"knn": map[string]any{"v": map[string]any{
			"vector": o.prepare(query), "k": k, "method_parameters": map[string]any{"ef_search": ef},
		}}},
	}, &response); err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		if ordinal, err := strconv.Atoi(hit.ID); err == nil {
			ids = append(ids, ordinal)
		}
	}
	return ids, nil
}
func (o *openSearchEngine) prepare(vector []float32) []float32 {
	if !o.normalize {
		return vector
	}
	var norm float64
	for _, value := range vector {
		norm += float64(value) * float64(value)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return vector
	}
	out := make([]float32, len(vector))
	for i, value := range vector {
		out[i] = float32(float64(value) / norm)
	}
	return out
}
func (o *openSearchEngine) teardown() {
	_ = o.client.do(http.MethodDelete, "/"+benchCollection, nil, nil)
}
