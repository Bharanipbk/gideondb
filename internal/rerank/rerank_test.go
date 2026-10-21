package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRerankRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" || r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var body struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "rr" || body.Query != "q" || len(body.Documents) != 2 || body.Documents[1] != "abc" {
			http.Error(w, "unexpected body", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]}`))
	}))
	defer server.Close()
	model := NewModel(Config{Name: "rr", URL: server.URL + "/v1/", Model: "rr", MaxDocumentBytes: 3}, "secret", nil)
	scores, err := model.Rerank(context.Background(), "q", []string{"x", "abcdef"})
	if err != nil || scores[0] != 0.1 || scores[1] != 0.9 {
		t.Fatalf("Rerank = %v, %v", scores, err)
	}
}

func TestRerankFailures(t *testing.T) {
	var reply atomic.Value
	reply.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch reply.Load().(string) {
		case "slow":
			time.Sleep(300 * time.Millisecond)
		case "error":
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		case "short":
			_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":1}]}`))
			return
		case "duplicate":
			_, _ = w.Write([]byte(`{"data":[{"index":0,"relevance_score":1},{"index":0,"relevance_score":2}]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	model := NewModel(Config{Name: "rr", URL: server.URL, Model: "rr", MaxDocuments: 2}, "", &http.Client{Timeout: 100 * time.Millisecond})
	for _, mode := range []string{"slow", "error", "short", "duplicate"} {
		reply.Store(mode)
		if _, err := model.Rerank(context.Background(), "q", []string{"a", "b"}); err == nil {
			t.Fatalf("%s: expected error", mode)
		}
	}
	if _, err := model.Rerank(context.Background(), "q", []string{"a", "b", "c"}); err == nil || !strings.Contains(err.Error(), "at most 2") {
		t.Fatalf("max documents error = %v", err)
	}
}

func TestLoadFile(t *testing.T) {
	directory := t.TempDir()
	key := filepath.Join(directory, "key")
	_ = os.WriteFile(key, []byte("k\n"), 0o600)
	path := filepath.Join(directory, "rerankers.json")
	_ = os.WriteFile(path, []byte(`{"rerankers":[{"name":"cohere","url":"https://api.cohere.com/v2","model":"rerank-v3.5","api_key_file":"`+key+`","timeout_seconds":2}]}`), 0o600)
	registry, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	model, ok := registry.Get("cohere")
	if !ok || model.apiKey != "k" || model.Timeout() != 2*time.Second || model.MaxDocuments() != defaultMaxDocuments {
		t.Fatalf("loaded %+v", model)
	}
	for _, bad := range []string{
		`{"rerankers":[{"name":"Bad","url":"https://x","model":"m"}]}`,
		`{"rerankers":[{"name":"a","url":"ftp://x","model":"m"}]}`,
		`{"rerankers":[{"name":"a","url":"https://x"}]}`,
		`{"rerankers":[{"name":"a","url":"https://x","model":"m"},{"name":"a","url":"https://x","model":"m"}]}`,
		`{"rerankers":[{"name":"a","url":"https://x","model":"m","extra":1}]}`,
	} {
		_ = os.WriteFile(path, []byte(bad), 0o600)
		if _, err := LoadFile(path); err == nil {
			t.Fatalf("LoadFile(%s) succeeded", bad)
		}
	}
	var none *Registry
	if _, ok := none.Get("x"); ok || none.Names() != nil {
		t.Fatal("nil registry must be empty")
	}
}
