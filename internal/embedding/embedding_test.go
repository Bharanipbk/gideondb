package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type countingEmbedder struct {
	calls  atomic.Int64
	inputs atomic.Int64
	size   int
}

func (c *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	c.calls.Add(1)
	c.inputs.Add(int64(len(texts)))
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		vectors[i] = make([]float32, c.size)
		vectors[i][0] = float32(len(text))
	}
	return vectors, nil
}

func TestModelBatchesAndCaches(t *testing.T) {
	inner := &countingEmbedder{size: 3}
	model := NewModel(Config{Name: "m", Model: "m", BatchSize: 2, CacheEntries: 3}, inner)
	vectors, err := model.Embed(context.Background(), []string{"a", "bb", "ccc", "a"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 4 || vectors[1][0] != 2 || vectors[3][0] != 1 {
		t.Fatalf("vectors = %v", vectors)
	}
	// "a" appears twice in one call; both are misses, sent in 2 batches of 2.
	if inner.calls.Load() != 2 || inner.inputs.Load() != 4 {
		t.Fatalf("calls=%d inputs=%d", inner.calls.Load(), inner.inputs.Load())
	}
	if _, err := model.Embed(context.Background(), []string{"bb", "ccc"}, 3); err != nil {
		t.Fatal(err)
	}
	if inner.inputs.Load() != 4 || model.hits.Load() != 2 {
		t.Fatalf("cached texts were re-embedded: inputs=%d hits=%d", inner.inputs.Load(), model.hits.Load())
	}
	// Capacity 3: adding two new texts evicts the least recently used ones.
	_, _ = model.Embed(context.Background(), []string{"dddd", "eeeee"}, 3)
	_, _ = model.Embed(context.Background(), []string{"a"}, 3)
	if inner.inputs.Load() != 7 {
		t.Fatalf("evicted entry should be re-embedded: inputs=%d", inner.inputs.Load())
	}
	if _, err := model.Embed(context.Background(), []string{"zz"}, 5); !errors.Is(err, ErrDimension) {
		t.Fatalf("dimension check: %v", err)
	}
}

func TestOpenAIEmbedder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Reply out of order to check that index ordering is honoured.
		data := []map[string]any{}
		for i := len(body.Input) - 1; i >= 0; i-- {
			data = append(data, map[string]any{"index": i, "embedding": []float32{float32(len(body.Input[i])), 1}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	embedder := &OpenAIEmbedder{BaseURL: server.URL + "/v1/", Model: "x", APIKey: "secret"}
	vectors, err := embedder.Embed(context.Background(), []string{"one", "three"})
	if err != nil {
		t.Fatal(err)
	}
	if vectors[0][0] != 3 || vectors[1][0] != 5 {
		t.Fatalf("vectors = %v", vectors)
	}
	embedder.APIKey = "wrong"
	if _, err := embedder.Embed(context.Background(), []string{"x"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected HTTP 401 error, got %v", err)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	_ = os.WriteFile(key, []byte("secret\n"), 0o600)
	good := filepath.Join(dir, "good.json")
	_ = os.WriteFile(good, []byte(`{"embedders":[{"name":"local","url":"http://localhost:11434/v1","model":"nomic-embed-text","dimension":768,"api_key_file":"`+key+`"}]}`), 0o600)
	registry, err := LoadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	model, ok := registry.Get("local")
	if !ok || model.Dimension() != 768 || model.embedder.(*OpenAIEmbedder).APIKey != "secret" {
		t.Fatalf("model = %+v", model)
	}
	if _, ok := registry.Get("missing"); ok {
		t.Fatal("unknown embedder found")
	}
	for name, body := range map[string]string{
		"bad name":   `{"embedders":[{"name":"Bad Name","url":"http://x","model":"m"}]}`,
		"bad url":    `{"embedders":[{"name":"a","url":"ftp://x","model":"m"}]}`,
		"no model":   `{"embedders":[{"name":"a","url":"http://x"}]}`,
		"duplicate":  `{"embedders":[{"name":"a","url":"http://x","model":"m"},{"name":"a","url":"http://x","model":"m"}]}`,
		"unknown":    `{"embedders":[{"name":"a","url":"http://x","model":"m","api_key":"inline"}]}`,
		"no keyfile": `{"embedders":[{"name":"a","url":"http://x","model":"m","api_key_file":"/nonexistent"}]}`,
	} {
		path := filepath.Join(dir, "bad.json")
		_ = os.WriteFile(path, []byte(body), 0o600)
		if _, err := LoadFile(path); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}
