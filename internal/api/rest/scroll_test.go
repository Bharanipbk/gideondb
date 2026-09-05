package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/vectordb/vectordb/internal/engine"
)

func TestScrollIsStableBoundedAndRedactedByDefault(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler := New(db, nil).Handler()
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "docs", "dimension": 2, "metric": "dot", "shard_count": 2}, http.StatusCreated)
	for _, record := range []map[string]any{{"id": "b", "vector": []float32{1, 0}, "metadata": map[string]any{"n": 2}}, {"id": "a", "vector": []float32{0, 1}, "metadata": map[string]any{"n": 1}}, {"id": "c", "namespace": "tenant", "vector": []float32{1, 1}}} {
		requestJSON(t, handler, http.MethodPost, "/v1/collections/docs/vectors", record, http.StatusOK)
	}
	first := scrollRequest(t, handler, "/v1/collections/docs/vectors?limit=2")
	if len(first.Records) != 2 || first.Records[0].ID != "a" || first.Records[1].ID != "b" || first.Records[0].Vector != nil || first.Next == "" {
		t.Fatalf("unexpected first page: %+v", first)
	}
	second := scrollRequest(t, handler, "/v1/collections/docs/vectors?cursor="+url.QueryEscape(first.Next))
	if len(second.Records) != 1 || second.Records[0].ID != "c" {
		t.Fatalf("unexpected second page: %+v", second)
	}
	visible := scrollRequest(t, handler, "/v1/collections/docs/vectors?namespace=tenant&include_vector=true")
	if len(visible.Records) != 1 || len(visible.Records[0].Vector) != 2 || !visible.VectorsIncluded {
		t.Fatalf("unexpected visible page: %+v", visible)
	}
	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/v1/collections/docs/vectors?cursor=bad", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor status=%d", bad.Code)
	}
}

type scrollResponse struct {
	Records []struct {
		ID     string    `json:"id"`
		Vector []float32 `json:"vector"`
	} `json:"records"`
	Next            string `json:"next_cursor"`
	VectorsIncluded bool   `json:"vectors_included"`
}

func scrollRequest(t *testing.T, handler http.Handler, target string) scrollResponse {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("scroll status=%d body=%s", response.Code, response.Body.String())
	}
	var body scrollResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}
