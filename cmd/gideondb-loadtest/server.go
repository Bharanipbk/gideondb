package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
)

// serverReport is the result of an end-to-end run against a running server
// over its REST API.
type serverReport struct {
	Server                 string  `json:"server"`
	Dataset                string  `json:"dataset"`
	Vectors                int     `json:"vectors"`
	Dimension              int     `json:"dimension"`
	Metric                 string  `json:"metric"`
	Index                  string  `json:"index"`
	EFSearch               int     `json:"ef_search,omitempty"`
	Shards                 int     `json:"shards"`
	Buckets                int     `json:"filter_buckets"`
	Concurrency            int     `json:"concurrency"`
	IngestConcurrency      int     `json:"ingest_concurrency"`
	K                      int     `json:"k"`
	GoVersion              string  `json:"go_version"`
	Architecture           string  `json:"architecture"`
	CPUs                   int     `json:"cpus"`
	IngestSeconds          float64 `json:"ingest_seconds"`
	IngestVectorsPerSecond float64 `json:"ingest_vectors_per_second"`
	Phases                 []phase `json:"phases"`
}

type phase struct {
	Name             string  `json:"name"`
	Queries          int     `json:"queries"`
	QueriesPerSecond float64 `json:"queries_per_second"`
	LatencyP50Millis float64 `json:"latency_p50_ms"`
	LatencyP95Millis float64 `json:"latency_p95_ms"`
	LatencyP99Millis float64 `json:"latency_p99_ms"`
	RecallAtK        float64 `json:"recall_at_k"`
}

// runServer loads a dataset into a running server, then measures concurrent
// unfiltered and filtered search. Each record carries "bucket" = i % buckets,
// so a filtered query on one bucket keeps 1/buckets of the collection. Recall
// is checked against exact search done locally for every measured query.
func runServer(arguments []string) {
	flags := flag.NewFlagSet("server", flag.ExitOnError)
	server := flags.String("server", "http://127.0.0.1:6333", "server base URL")
	apiKeyFile := flags.String("api-key-file", "", "file containing the bearer API key")
	basePath := flags.String("base", "", ".fvecs base vectors (required)")
	queriesPath := flags.String("queries", "", ".fvecs query vectors (required)")
	limit := flags.Int("limit", 0, "load at most this many base vectors; 0 loads all")
	queryCount := flags.Int("query-count", 1000, "queries per phase")
	metricName := flags.String("metric", "l2", "cosine, dot or l2")
	indexType := flags.String("index", "hnsw", "flat or hnsw")
	efSearch := flags.Int("ef-search", 0, "per-query ef_search; 0 uses the collection default")
	shards := flags.Int("shards", 1, "collection shard count")
	buckets := flags.Int("buckets", 10, "metadata buckets; a filtered query keeps 1/buckets of the data")
	concurrency := flags.Int("concurrency", 16, "concurrent query clients")
	ingestConcurrency := flags.Int("ingest-concurrency", 4, "concurrent batch-upsert clients")
	batchSize := flags.Int("batch-size", 500, "records per batch upsert")
	k := flags.Int("k", 10, "neighbours per query")
	collection := flags.String("collection", "loadtest", "collection name; created, and replaced if it exists")
	keep := flags.Bool("keep", false, "keep the collection after the run")
	_ = flags.Parse(arguments)
	if *basePath == "" || *queriesPath == "" {
		fail(fmt.Errorf("-base and -queries are required in server mode"))
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
	client := &apiClient{base: strings.TrimRight(*server, "/"), http: &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{MaxIdleConnsPerHost: *concurrency * 2}}}
	if *apiKeyFile != "" {
		key, err := os.ReadFile(*apiKeyFile)
		if err != nil {
			fail(err)
		}
		client.key = strings.TrimSpace(string(key))
	}

	_ = client.do(http.MethodDelete, "/v1/collections/"+*collection, nil, nil)
	config := map[string]any{"name": *collection, "dimension": len(base[0]), "metric": metric, "shard_count": *shards, "index": map[string]any{"type": *indexType}}
	if err := client.do(http.MethodPost, "/v1/collections", config, nil); err != nil {
		fail(err)
	}
	if !*keep {
		defer client.do(http.MethodDelete, "/v1/collections/"+*collection, nil, nil)
	}

	fmt.Fprintf(os.Stderr, "ingesting %d vectors…\n", len(base))
	started := time.Now()
	parallel(len(base) / *batchSize + 1, *concurrency, func(batch int) error {
		first := batch * *batchSize
		if first >= len(base) {
			return nil
		}
		records := make([]map[string]any, 0, *batchSize)
		for i := first; i < min(first+*batchSize, len(base)); i++ {
			records = append(records, map[string]any{"id": recordID(i), "vector": base[i], "metadata": map[string]any{"bucket": i % *buckets}})
		}
		return client.do(http.MethodPost, "/v1/collections/"+*collection+"/vectors/batch", map[string]any{"records": records}, nil)
	})
	ingest := time.Since(started)

	fmt.Fprintln(os.Stderr, "computing exact ground truth…")
	truth := make([][]int, len(queries))
	filteredTruth := make([][]int, len(queries))
	parallel(len(queries), runtime.GOMAXPROCS(0), func(i int) error {
		truth[i] = exactNeighbours(metric, base, queries[i], *k, func(int) bool { return true })
		bucket := i % *buckets
		filteredTruth[i] = exactNeighbours(metric, base, queries[i], *k, func(j int) bool { return j%*buckets == bucket })
		return nil
	})

	report := serverReport{
		Server: client.base, Dataset: *basePath, Vectors: len(base), Dimension: len(base[0]), Metric: string(metric), Index: *indexType,
		EFSearch: *efSearch, Shards: *shards, Buckets: *buckets, Concurrency: *concurrency, IngestConcurrency: *ingestConcurrency, K: *k,
		GoVersion: runtime.Version(), Architecture: runtime.GOOS + "/" + runtime.GOARCH, CPUs: runtime.NumCPU(),
		IngestSeconds: ingest.Seconds(), IngestVectorsPerSecond: float64(len(base)) / ingest.Seconds(),
	}
	for _, filtered := range []bool{false, true} {
		name, expected := "unfiltered", truth
		if filtered {
			name, expected = fmt.Sprintf("filtered(bucket, 1/%d)", *buckets), filteredTruth
		}
		fmt.Fprintf(os.Stderr, "querying %s…\n", name)
		latencies := make([]time.Duration, len(queries))
		var hits atomic.Int64
		started := time.Now()
		parallel(len(queries), *concurrency, func(i int) error {
			request := map[string]any{"vector": queries[i], "top_k": *k}
			if *efSearch > 0 {
				request["ef_search"] = *efSearch
			}
			if filtered {
				request["filter"] = map[string]any{"bucket": i % *buckets}
			}
			var response struct {
				Results []struct {
					ID string `json:"id"`
				} `json:"results"`
			}
			begin := time.Now()
			if err := client.do(http.MethodPost, "/v1/collections/"+*collection+"/search", request, &response); err != nil {
				return err
			}
			latencies[i] = time.Since(begin)
			want := make(map[string]struct{}, *k)
			for _, ordinal := range expected[i] {
				want[recordID(ordinal)] = struct{}{}
			}
			for _, result := range response.Results {
				if _, ok := want[result.ID]; ok {
					hits.Add(1)
				}
			}
			return nil
		})
		elapsed := time.Since(started)
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		report.Phases = append(report.Phases, phase{
			Name: name, Queries: len(queries), QueriesPerSecond: float64(len(queries)) / elapsed.Seconds(),
			LatencyP50Millis: millis(percentile(latencies, 0.50)), LatencyP95Millis: millis(percentile(latencies, 0.95)), LatencyP99Millis: millis(percentile(latencies, 0.99)),
			RecallAtK: float64(hits.Load()) / float64(len(queries)**k),
		})
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fail(err)
	}
}

type apiClient struct {
	base string
	key  string
	http *http.Client
}

func (c *apiClient) do(method, path string, body, into any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("content-type", "application/json")
	if c.key != "" {
		request.Header.Set("Authorization", "Bearer "+c.key)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("%s %s: %s: %s", method, path, response.Status, bytes.TrimSpace(message))
	}
	if into == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	return json.NewDecoder(response.Body).Decode(into)
}

// parallel runs work(0..count-1) on workers goroutines and fails the run on
// the first error.
func parallel(count, workers int, work func(int) error) {
	var next atomic.Int64
	var wait sync.WaitGroup
	var once sync.Once
	var firstError error
	for range max(1, min(workers, count)) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= count {
					return
				}
				if err := work(i); err != nil {
					once.Do(func() { firstError = err })
					next.Store(int64(count))
					return
				}
			}
		}()
	}
	wait.Wait()
	if firstError != nil {
		fail(firstError)
	}
}

// exactNeighbours returns the ordinals of the k best allowed base vectors,
// ties broken by ordinal.
func exactNeighbours(metric core.Metric, base [][]float32, query []float32, k int, allowed func(int) bool) []int {
	type scored struct {
		ordinal int
		score   float32
	}
	best := make([]scored, 0, k+1)
	for ordinal, vector := range base {
		if !allowed(ordinal) {
			continue
		}
		score, _ := distance.Score(metric, query, vector)
		if len(best) == k && score <= best[k-1].score {
			continue
		}
		position := sort.Search(len(best), func(i int) bool { return best[i].score < score })
		best = append(best, scored{})
		copy(best[position+1:], best[position:])
		best[position] = scored{ordinal, score}
		if len(best) > k {
			best = best[:k]
		}
	}
	result := make([]int, len(best))
	for i, item := range best {
		result[i] = item.ordinal
	}
	return result
}

// readFvecs reads little-endian records of [int32 dimension][dimension float32].
func readFvecs(path string, limit int) ([][]float32, error) {
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

func recordID(ordinal int) string { return fmt.Sprintf("v%d", ordinal) }
