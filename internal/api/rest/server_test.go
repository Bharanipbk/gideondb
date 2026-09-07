package rest

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
