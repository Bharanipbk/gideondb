package rest

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
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

func TestDistributedScrollMergesShardsAndFencesCursor(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 8}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	for _, record := range []core.Record{{ID: "c", Vector: []float32{1, 0}}, {ID: "a", Vector: []float32{1, 0}}, {ID: "b", Vector: []float32{1, 0}}, {ID: "d", Namespace: "tenant", Vector: []float32{1, 0}}} {
		if _, err := db.Upsert("docs", record); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewWithOptions(db, nil, Options{NodeID: "11111111111111111111111111111111", ClusterID: "22222222222222222222222222222222", AdvertiseAddress: "node-a:6333", MetadataEpoch: 7}).Handler()
	first := scrollRequest(t, handler, "/v1/cluster/collections/docs/vectors?limit=2")
	if len(first.Records) != 2 || first.Records[0].ID != "a" || first.Records[1].ID != "b" || first.Next == "" || first.Records[0].Vector != nil {
		t.Fatalf("unexpected first page: %+v", first)
	}
	second := scrollRequest(t, handler, "/v1/cluster/collections/docs/vectors?limit=2&include_vector=true&cursor="+url.QueryEscape(first.Next))
	if len(second.Records) != 2 || second.Records[0].ID != "c" || second.Records[1].ID != "d" || len(second.Records[0].Vector) != 2 {
		t.Fatalf("unexpected second page: %+v", second)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(first.Next)
	if err != nil {
		t.Fatal(err)
	}
	var cursor distributedScrollCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		t.Fatal(err)
	}
	cursor.Epoch = 6
	staleJSON, _ := json.Marshal(cursor)
	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, httptest.NewRequest(http.MethodGet, "/v1/cluster/collections/docs/vectors?cursor="+url.QueryEscape(base64.RawURLEncoding.EncodeToString(staleJSON)), nil))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
	mismatch := httptest.NewRecorder()
	handler.ServeHTTP(mismatch, httptest.NewRequest(http.MethodGet, "/v1/cluster/collections/docs/vectors?namespace=tenant&cursor="+url.QueryEscape(first.Next), nil))
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
}

type scrollResponse struct {
	Records []struct {
		ID        string    `json:"id"`
		Namespace string    `json:"namespace"`
		Vector    []float32 `json:"vector"`
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
