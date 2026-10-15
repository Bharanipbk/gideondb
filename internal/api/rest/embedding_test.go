package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/embedding"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

// keywordEmbedder maps texts to 4-d vectors by keyword so ranking is
// predictable: "go" → x, "python" → y, "rust" → z, anything else → w.
type keywordEmbedder struct{ fail bool }

func (k keywordEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if k.fail {
		return nil, errors.New("model server returned HTTP 500")
	}
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		vector := []float32{0, 0, 0, 0.1}
		for position, word := range []string{"go", "python", "rust"} {
			if strings.Contains(strings.ToLower(text), word) {
				vector[position] = 1
			}
		}
		vectors[i] = vector
	}
	return vectors, nil
}

func embeddingServer(t *testing.T) http.Handler {
	t.Helper()
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	registry := embedding.NewRegistryWithModels(
		embedding.NewModel(embedding.Config{Name: "keywords", Model: "keywords", Dimension: 4}, keywordEmbedder{}),
		embedding.NewModel(embedding.Config{Name: "broken", Model: "broken"}, keywordEmbedder{fail: true}),
	)
	return NewWithOptions(db, nil, Options{Embedders: registry}).Handler()
}

func send(t *testing.T, handler http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
	var decoded map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &decoded)
	return response.Code, decoded
}

func TestTextUpsertAndSearch(t *testing.T) {
	h := embeddingServer(t)
	if code, body := send(t, h, http.MethodPost, "/v1/collections", `{"name":"docs","dimension":4,"metric":"cosine","shard_count":2,"vectorizer":"keywords"}`); code != http.StatusCreated || body["vectorizer"] != "keywords" {
		t.Fatalf("create = %d %v", code, body)
	}
	if code, body := send(t, h, http.MethodPost, "/v1/collections/docs/vectors", `{"id":"g","text":"Go is fast","metadata":{"kind":"lang"}}`); code != http.StatusOK || body["payload"].(map[string]any)["text"] != "Go is fast" {
		t.Fatalf("upsert = %d %v", code, body)
	}
	code, body := send(t, h, http.MethodPost, "/v1/collections/docs/vectors/batch", `{"records":[
		{"id":"p","text":"python notebooks"},
		{"id":"r","text":"rust borrow checker","payload":{"text":"custom"}},
		{"id":"v","vector":[0,0,0,1]}]}`)
	if code != http.StatusOK {
		t.Fatalf("batch = %d %v", code, body)
	}
	if code, body := send(t, h, http.MethodGet, "/v1/collections/docs/vectors/r", ""); code != http.StatusOK || body["payload"].(map[string]any)["text"] != "custom" {
		t.Fatalf("existing payload.text must be kept: %d %v", code, body)
	}
	code, body = send(t, h, http.MethodPost, "/v1/collections/docs/search", `{"text":"learning rust","top_k":1}`)
	results, _ := body["results"].([]any)
	if code != http.StatusOK || len(results) != 1 || results[0].(map[string]any)["id"] != "r" {
		t.Fatalf("text search = %d %v", code, body)
	}
	if code, _ := send(t, h, http.MethodGet, "/v1/embedders", ""); code != http.StatusOK {
		t.Fatalf("embedders = %d", code)
	}
	_, stats := send(t, h, http.MethodGet, "/v1/embedders", "")
	for _, item := range stats["embedders"].([]any) {
		if entry := item.(map[string]any); entry["name"] == "keywords" && entry["cache_misses"].(float64) != 4 {
			t.Fatalf("stats = %v", entry)
		}
	}
}

func TestTextErrors(t *testing.T) {
	h := embeddingServer(t)
	for name, body := range map[string]string{
		"unknown vectorizer": `{"name":"a","dimension":4,"metric":"cosine","shard_count":1,"vectorizer":"nope"}`,
		"dimension mismatch": `{"name":"a","dimension":8,"metric":"cosine","shard_count":1,"vectorizer":"keywords"}`,
		"invalid name":       `{"name":"a","dimension":4,"metric":"cosine","shard_count":1,"vectorizer":"Bad Name"}`,
	} {
		if code, _ := send(t, h, http.MethodPost, "/v1/collections", body); code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", name, code)
		}
	}
	send(t, h, http.MethodPost, "/v1/collections", `{"name":"plain","dimension":4,"metric":"cosine","shard_count":1}`)
	send(t, h, http.MethodPost, "/v1/collections", `{"name":"docs","dimension":4,"metric":"cosine","shard_count":1,"vectorizer":"keywords"}`)
	send(t, h, http.MethodPost, "/v1/collections", `{"name":"down","dimension":4,"metric":"cosine","shard_count":1,"vectorizer":"broken"}`)
	cases := []struct {
		path, body string
		status     int
	}{
		{"/v1/collections/plain/vectors", `{"id":"a","text":"go"}`, http.StatusBadRequest},
		{"/v1/collections/plain/search", `{"text":"go","top_k":1}`, http.StatusBadRequest},
		{"/v1/collections/docs/vectors", `{"id":"a","text":"go","vector":[1,0,0,0]}`, http.StatusOK},
		{"/v1/collections/docs/search", `{"text":"go","vector":[1,0,0,0],"top_k":1}`, http.StatusOK},
		{"/v1/collections/plain/vectors", `{"id":"b","text":"go","vector":[1,0,0,0]}`, http.StatusOK},
		{"/v1/collections/docs/vectors", `{"id":"a","text":"` + strings.Repeat("x", maxEmbedTextBytes+1) + `"}`, http.StatusBadRequest},
		{"/v1/collections/down/vectors", `{"id":"a","text":"go"}`, http.StatusBadGateway},
		{"/v1/collections/missing/search", `{"text":"go","top_k":1}`, http.StatusNotFound},
	}
	for _, c := range cases {
		if code, body := send(t, h, http.MethodPost, c.path, c.body); code != c.status {
			t.Fatalf("%s %.60s: status %d, want %d (%v)", c.path, c.body, code, c.status, body)
		}
	}
}
