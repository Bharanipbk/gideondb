package rest

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

func TestRESTLifecycle(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, nil).Handler()

	requestJSON(t, handler, http.MethodPost, "/v1/collections",
		map[string]any{"name": "docs", "dimension": 2, "metric": "dot", "shard_count": 2}, http.StatusCreated)
	requestJSON(t, handler, http.MethodPost, "/v1/collections/docs/vectors",
		map[string]any{"id": "one", "vector": []float32{1, 0}, "metadata": map[string]any{"type": "test"}}, http.StatusOK)
	response := requestJSON(t, handler, http.MethodPost, "/v1/collections/docs/search",
		map[string]any{"vector": []float32{1, 0}, "top_k": 1, "ef_search": 8}, http.StatusOK)
	var body struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(response, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Results) != 1 || body.Results[0].ID != "one" {
		t.Fatalf("unexpected search response: %s", response)
	}
	response = requestJSON(t, handler, http.MethodPost, "/v1/collections/docs/search",
		map[string]any{"vector": []float32{1, 0}, "top_k": 1, "filter": map[string]any{"type": "missing"}}, http.StatusOK)
	if err := json.Unmarshal(response, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Results) != 0 {
		t.Fatalf("filter should exclude result: %s", response)
	}
	requestJSON(t, handler, http.MethodPost, "/v1/collections/docs/search",
		map[string]any{"vector": []float32{1, 0}, "top_k": 1, "ef_search": 10001}, http.StatusBadRequest)
	requestJSON(t, handler, http.MethodPost, "/v1/collections/docs/vectors/batch",
		map[string]any{"records": []any{
			map[string]any{"id": "two", "vector": []float32{0, 1}},
			map[string]any{"id": "three", "vector": []float32{0.5, 0.5}},
		}}, http.StatusOK)
	_, count, err := db.DescribeCollection("docs")
	if err != nil || count != 3 {
		t.Fatalf("batch count = %d, %v", count, err)
	}
}

func TestRESTSparseAndHybridSearch(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, nil).Handler()
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "hybrid", "dimension": 2, "metric": "cosine", "shard_count": 1}, http.StatusCreated)
	requestJSON(t, handler, http.MethodPost, "/v1/collections/hybrid/vectors", map[string]any{"id": "one", "vector": []float32{1, 0}, "sparse_vector": map[string]float32{"gideondb": 2}}, http.StatusOK)
	response := requestJSON(t, handler, http.MethodPost, "/v1/collections/hybrid/search", map[string]any{"sparse_vector": map[string]float32{"gideondb": 1}, "top_k": 1}, http.StatusOK)
	if !strings.Contains(string(response), `"id":"one"`) {
		t.Fatalf("unexpected sparse response: %s", response)
	}
	requestJSON(t, handler, http.MethodPost, "/v1/collections/hybrid/search", map[string]any{"vector": []float32{1, 0}, "sparse_vector": map[string]float32{"gideondb": 1}, "alpha": 1.1, "top_k": 1}, http.StatusBadRequest)
	requestJSON(t, handler, http.MethodPost, "/v1/collections/hybrid/vectors", map[string]any{"id": "bad", "vector": []float32{1, 0}, "sparse_vector": map[string]float32{"": 1}}, http.StatusBadRequest)
}

func TestRESTTextSearchAndRRFFusion(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, nil).Handler()
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "docs", "dimension": 2, "metric": "cosine", "shard_count": 2}, http.StatusCreated)
	requestJSON(t, handler, http.MethodPost, "/v1/collections/docs/vectors/batch", map[string]any{"records": []map[string]any{
		{"id": "bm25", "vector": []float32{0, 1}, "text": "BM25 keyword retrieval for vector databases"},
		{"id": "dense", "vector": []float32{1, 0}, "text": "cooking recipes"},
	}}, http.StatusOK)
	response := requestJSON(t, handler, http.MethodPost, "/v1/collections/docs/search", map[string]any{"text": "keyword retrieval", "top_k": 5}, http.StatusOK)
	if !strings.Contains(string(response), `"id":"bm25"`) || strings.Contains(string(response), `"id":"dense"`) {
		t.Fatalf("unexpected text response: %s", response)
	}
	response = requestJSON(t, handler, http.MethodPost, "/v1/collections/docs/search", map[string]any{"vector": []float32{1, 0}, "text": "keyword", "fusion": "rrf", "top_k": 5}, http.StatusOK)
	if !strings.Contains(string(response), `"id":"bm25"`) || !strings.Contains(string(response), `"id":"dense"`) {
		t.Fatalf("rrf should return candidates from both signals: %s", response)
	}
	record := requestJSON(t, handler, http.MethodGet, "/v1/collections/docs/vectors/bm25", nil, http.StatusOK)
	if !strings.Contains(string(record), `"text":"BM25 keyword retrieval for vector databases"`) {
		t.Fatalf("text not returned on get: %s", record)
	}
	requestJSON(t, handler, http.MethodPost, "/v1/collections/docs/search", map[string]any{"text": "x", "fusion": "max", "top_k": 1}, http.StatusBadRequest)
}

func TestOperationalPaginationAndRateLimit(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if err := db.CreateCollection(core.CollectionConfig{Name: name, Dimension: 1, Metric: core.MetricDot, ShardCount: 1}); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewWithOptions(db, nil, Options{RateLimitPerSecond: 1, RateLimitBurst: 1}).Handler()
	first := httptest.NewRequest(http.MethodGet, "/v1/collections?limit=2", nil)
	first.Header.Set("Authorization", "Bearer caller-a")
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusOK || !strings.Contains(firstResponse.Body.String(), `"name":"alpha"`) || !strings.Contains(firstResponse.Body.String(), `"name":"beta"`) || !strings.Contains(firstResponse.Body.String(), `"next_cursor":"`) {
		t.Fatalf("first page status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}

	limited := httptest.NewRequest(http.MethodGet, "/v1/collections", nil)
	limited.Header.Set("Authorization", "Bearer caller-a")
	limitedResponse := httptest.NewRecorder()
	handler.ServeHTTP(limitedResponse, limited)
	if limitedResponse.Code != http.StatusTooManyRequests || limitedResponse.Header().Get("Retry-After") == "" || !strings.Contains(limitedResponse.Body.String(), "rate_limit_exceeded") {
		t.Fatalf("limited status=%d retry=%q body=%s", limitedResponse.Code, limitedResponse.Header().Get("Retry-After"), limitedResponse.Body.String())
	}

	independent := httptest.NewRequest(http.MethodGet, "/v1/collections?limit=2", nil)
	independent.Header.Set("Authorization", "Bearer caller-b")
	independentResponse := httptest.NewRecorder()
	handler.ServeHTTP(independentResponse, independent)
	if independentResponse.Code != http.StatusOK {
		t.Fatalf("independent identity status=%d body=%s", independentResponse.Code, independentResponse.Body.String())
	}
}

func TestMetricsUseRoutePatternsAndExposeEngineGauges(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, nil).Handler()
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "metrics", "dimension": 2, "metric": "dot", "shard_count": 1}, http.StatusCreated)
	requestJSON(t, handler, http.MethodPost, "/v1/collections/metrics/vectors", map[string]any{"id": "secret-record-id", "vector": []float32{1, 0}}, http.StatusOK)
	request := httptest.NewRequest(http.MethodGet, "/v1/collections/metrics/vectors/secret-record-id", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("get status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	for _, want := range []string{
		`route="GET /v1/collections/{name}/vectors/{id}"`,
		`gideondb_collections 1`,
		`gideondb_vectors{collection="metrics"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "secret-record-id") {
		t.Fatalf("metrics leaked path value into labels:\n%s", body)
	}
}

func TestTraceContextPropagationAndBoundedLogging(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := New(db, logger).Handler()
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "trace", "dimension": 1, "metric": "dot", "shard_count": 1}, http.StatusCreated)
	requestJSON(t, handler, http.MethodPost, "/v1/collections/trace/vectors", map[string]any{"id": "private-vector-id", "vector": []float32{1}}, http.StatusOK)
	request := httptest.NewRequest(http.MethodGet, "/v1/collections/trace/vectors/private-vector-id", nil)
	request.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	traceparent := response.Header().Get("traceparent")
	if !strings.HasPrefix(traceparent, "00-0123456789abcdef0123456789abcdef-") || len(traceparent) != 55 {
		t.Fatalf("response traceparent = %q", traceparent)
	}
	output := logs.String()
	if !strings.Contains(output, `"route":"GET /v1/collections/{name}/vectors/{id}"`) {
		t.Fatalf("trace log missing route template: %s", output)
	}
	if strings.Contains(output, "private-vector-id") {
		t.Fatalf("trace log leaked raw vector ID: %s", output)
	}
}

func requestJSON(t *testing.T, handler http.Handler, method, target string, value any, wantStatus int) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d: %s", response.Code, wantStatus, response.Body.String())
	}
	return response.Body.Bytes()
}

func TestRESTAnalyzerFacetsAndHighlight(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, nil).Handler()
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "bad", "dimension": 2, "metric": "cosine", "shard_count": 1, "text_analyzer": "klingon"}, http.StatusBadRequest)
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "shop", "dimension": 2, "metric": "cosine", "shard_count": 3, "text_analyzer": "english"}, http.StatusCreated)
	requestJSON(t, handler, http.MethodPost, "/v1/collections/shop/vectors/batch", map[string]any{"records": []map[string]any{
		{"id": "1", "vector": []float32{1, 0}, "text": "Lightweight running shoes for trails", "metadata": map[string]any{"brand": "acme", "size": 42}},
		{"id": "2", "vector": []float32{0, 1}, "text": "Runners love these road shoes", "metadata": map[string]any{"brand": "acme", "size": 44}},
		{"id": "3", "vector": []float32{1, 1}, "text": "A warm winter jacket", "metadata": map[string]any{"brand": "zeta", "size": 42}},
	}}, http.StatusOK)
	response := requestJSON(t, handler, http.MethodPost, "/v1/collections/shop/search", map[string]any{
		"text": "shoe", "top_k": 5,
		"facets":    []map[string]any{{"field": "brand"}, {"field": "size", "size": 1}},
		"highlight": map[string]any{},
	}, http.StatusOK)
	body := string(response)
	for _, want := range []string{`"id":"1"`, `"id":"2"`, `running \u003cem\u003eshoes\u003c/em\u003e`, `"facets":`, `"brand":[{"value":"acme","count":2}]`, `"size":[{"value":42,"count":1}]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, `"id":"3"`) || strings.Contains(body, `"text":`) {
		t.Fatalf("unexpected hit or leaked text: %s", body)
	}
	// A vector search can highlight with an explicit query and facet over everything.
	response = requestJSON(t, handler, http.MethodPost, "/v1/collections/shop/search", map[string]any{
		"vector": []float32{1, 1}, "top_k": 1, "facets": []map[string]any{{"field": "brand"}},
		"highlight": map[string]any{"query": "jackets", "pre_tag": "[", "post_tag": "]"},
	}, http.StatusOK)
	if !strings.Contains(string(response), `winter [jacket]`) || !strings.Contains(string(response), `{"value":"acme","count":2},{"value":"zeta","count":1}`) {
		t.Fatalf("vector highlight/facets: %s", response)
	}
	requestJSON(t, handler, http.MethodPost, "/v1/collections/shop/search", map[string]any{"vector": []float32{1, 1}, "top_k": 1, "highlight": map[string]any{}}, http.StatusBadRequest)
	requestJSON(t, handler, http.MethodPost, "/v1/collections/shop/search", map[string]any{"vector": []float32{1, 1}, "top_k": 1, "facets": []map[string]any{{"field": ""}}}, http.StatusBadRequest)
}

func TestRESTQuerySyntaxAggregationsAndSort(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, nil).Handler()
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "shop", "dimension": 2, "metric": "cosine", "shard_count": 3,
		"text_analyzer": "english", "synonyms": [][]string{{"sneaker", "shoe"}}}, http.StatusCreated)
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "bad", "dimension": 2, "metric": "cosine", "shard_count": 1, "synonyms": [][]string{{"lonely"}}}, http.StatusBadRequest)
	records := []map[string]any{
		{"id": "1", "vector": []float32{1, 0}, "text": "red running shoes", "metadata": map[string]any{"price": 120, "brand": "acme"}},
		{"id": "2", "vector": []float32{1, 0.1}, "text": "blue running shoes", "metadata": map[string]any{"price": 80, "brand": "zeta"}},
		{"id": "3", "vector": []float32{0.9, 0.2}, "text": "red trail shoes", "metadata": map[string]any{"price": 95, "brand": "acme"}},
		{"id": "4", "vector": []float32{0, 1}, "text": "red winter jacket", "metadata": map[string]any{"price": 200, "brand": "acme"}},
		{"id": "5", "vector": []float32{0.5, 0.5}, "text": "running socks", "metadata": map[string]any{"brand": "zeta"}},
	}
	requestJSON(t, handler, http.MethodPost, "/v1/collections/shop/vectors/batch", map[string]any{"records": records}, http.StatusOK)

	// Syntax + synonyms: +sneakers finds shoes, -blue excludes 2, +red requires red.
	body := string(requestJSON(t, handler, http.MethodPost, "/v1/collections/shop/search", map[string]any{
		"text": "+red +sneakers -blue", "syntax": "simple", "top_k": 10,
		"aggregations": map[string]any{
			"price_stats": map[string]any{"type": "stats", "field": "price"},
			"price_hist":  map[string]any{"type": "histogram", "field": "price", "interval": 50},
			"price_bands": map[string]any{"type": "range", "field": "price", "ranges": []map[string]any{{"to": 100}, {"from": 100, "key": "premium"}}},
			"brands":      map[string]any{"type": "terms", "field": "brand"},
		},
		"sort": []map[string]any{{"field": "price"}},
	}, http.StatusOK))
	for _, want := range []string{
		`"results":[{"id":"3"`, `{"id":"1"`,
		`"price_stats":{"count":2,"min":95,"max":120,"sum":215,"avg":107.5}`,
		`"price_hist":{"buckets":[{"key":50,"count":1},{"key":100,"count":1}]}`,
		`"price_bands":{"buckets":[{"key":"*-100","to":100,"count":1},{"key":"premium","from":100,"count":1}]}`,
		`"brands":{"buckets":[{"key":"acme","count":2}]}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	for _, unwanted := range []string{`"id":"2"`, `"id":"4"`, `"id":"5"`} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("unexpected %s in %s", unwanted, body)
		}
	}
	// Vector search sorted by price descending; record 5 has no price and sorts last.
	body = string(requestJSON(t, handler, http.MethodPost, "/v1/collections/shop/search", map[string]any{
		"vector": []float32{1, 0}, "top_k": 5, "sort": []map[string]any{{"field": "price", "order": "desc"}},
	}, http.StatusOK))
	order := []string{`"id":"4"`, `"id":"1"`, `"id":"3"`, `"id":"2"`, `"id":"5"`}
	last := -1
	for _, id := range order {
		position := strings.Index(body, id)
		if position < last {
			t.Fatalf("sort order wrong: %s", body)
		}
		last = position
	}
	// Phrase with highlight terms from the parsed query.
	body = string(requestJSON(t, handler, http.MethodPost, "/v1/collections/shop/search", map[string]any{
		"text": `"running shoes"`, "syntax": "simple", "top_k": 5, "highlight": map[string]any{"pre_tag": "[", "post_tag": "]"},
	}, http.StatusOK))
	if !strings.Contains(body, `[running] [shoes]`) || strings.Contains(body, `"id":"3"`) {
		t.Fatalf("phrase/highlight: %s", body)
	}
	for _, bad := range []map[string]any{
		{"text": `"open`, "syntax": "simple", "top_k": 1},
		{"text": "x", "syntax": "lucene", "top_k": 1},
		{"vector": []float32{1, 0}, "top_k": 1, "sort": []map[string]any{{"field": "price", "order": "up"}}},
		{"vector": []float32{1, 0}, "top_k": 1, "aggregations": map[string]any{"h": map[string]any{"type": "histogram", "field": "price"}}},
	} {
		requestJSON(t, handler, http.MethodPost, "/v1/collections/shop/search", bad, http.StatusBadRequest)
	}
}

func TestRESTSuggestCompletesAndCorrects(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := New(db, nil).Handler()
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "docs", "dimension": 2, "metric": "cosine", "shard_count": 2, "text_analyzer": "english"}, http.StatusCreated)
	requestJSON(t, handler, http.MethodPost, "/v1/collections/docs/vectors/batch", map[string]any{"records": []map[string]any{
		{"id": "1", "vector": []float32{1, 0}, "text": "Running shoes for marathon running"},
		{"id": "2", "vector": []float32{1, 0}, "text": "Running jackets"},
		{"id": "3", "vector": []float32{1, 0}, "text": "Runner's guide to recovery"},
		{"id": "4", "vector": []float32{1, 0}, "text": "Café reviews"},
	}}, http.StatusOK)
	get := func(query string) string {
		request := httptest.NewRequest(http.MethodGet, "/v1/collections/docs/suggest?"+query, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, response.Code, response.Body)
		}
		return response.Body.String()
	}
	body := get("text=fast+runn&size=3")
	if !strings.Contains(body, `{"text":"fast running","word":"running","count":2}`) || !strings.Contains(body, `"word":"runner"`) {
		t.Fatalf("completions: %s", body)
	}
	if body := get("text=runing+shoez+"); !strings.Contains(body, `"did_you_mean":"running shoes "`) {
		t.Fatalf("did you mean: %s", body)
	}
	if body := get("text=cafe+rev"); !strings.Contains(body, `"word":"reviews"`) || strings.Contains(body, "did_you_mean") {
		t.Fatalf("folded words count as indexed: %s", body)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/collections/docs/suggest?text=x&size=99", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("size 99 = %d", response.Code)
	}
}
