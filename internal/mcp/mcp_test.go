package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bharanipbk/gideondb/internal/api/rest"
	"github.com/Bharanipbk/gideondb/internal/embedding"
	"github.com/Bharanipbk/gideondb/internal/engine"
	"github.com/Bharanipbk/gideondb/pkg/client"
)

// wordEmbedder hashes words into a small bag-of-words vector, so texts that
// share words are similar. Good enough to test ranking deterministically.
type wordEmbedder struct{}

func (wordEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		vector := make([]float32, 16)
		for _, word := range strings.Fields(strings.ToLower(text)) {
			hash := fnv.New32a()
			_, _ = hash.Write([]byte(word))
			vector[hash.Sum32()%16]++
		}
		vectors[i] = vector
	}
	return vectors, nil
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type harness struct {
	t      *testing.T
	client *client.Client
	server *Server
	now    time.Time
	nextID int
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	return newHarnessWithServerEmbedders(t, cfg, nil)
}

func newHarnessWithServerEmbedders(t *testing.T, cfg Config, embedders *embedding.Registry) *harness {
	t.Helper()
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler := rest.NewWithOptions(db, slog.New(slog.NewTextHandler(io.Discard, nil)), rest.Options{Embedders: embedders}).Handler()
	httpClient := &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
	cfg.Client, err = client.New("http://gideondb.test", client.Options{HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, client: cfg.Client, now: time.Unix(1_700_000_000, 0)}
	cfg.Now = func() time.Time { return h.now }
	h.server = NewServer("gideondb", "test", Tools(cfg))
	return h
}

// rpc sends one request through Serve and returns the decoded response.
func (h *harness) rpc(method string, params any) map[string]any {
	h.t.Helper()
	h.nextID++
	message := map[string]any{"jsonrpc": "2.0", "id": h.nextID, "method": method}
	if params != nil {
		message["params"] = params
	}
	line, _ := json.Marshal(message)
	var out bytes.Buffer
	if err := h.server.Serve(context.Background(), bytes.NewReader(append(line, '\n')), &out); err != nil {
		h.t.Fatal(err)
	}
	var reply map[string]any
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		h.t.Fatalf("decode %q: %v", out.String(), err)
	}
	return reply
}

// call invokes a tool and returns its structured result, failing the test if
// the tool reported an error unless wantError is set.
func (h *harness) call(name string, arguments map[string]any, wantError bool) map[string]any {
	h.t.Helper()
	reply := h.rpc("tools/call", map[string]any{"name": name, "arguments": arguments})
	result, ok := reply["result"].(map[string]any)
	if !ok {
		h.t.Fatalf("%s: no result: %v", name, reply)
	}
	isError, _ := result["isError"].(bool)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if isError != wantError {
		h.t.Fatalf("%s: isError=%v, want %v: %s", name, isError, wantError, text)
	}
	var value map[string]any
	_ = json.Unmarshal([]byte(text), &value)
	return value
}

func toolNames(reply map[string]any) map[string]bool {
	names := map[string]bool{}
	for _, tool := range reply["result"].(map[string]any)["tools"].([]any) {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	return names
}

func TestProtocolHandshakeAndToolGating(t *testing.T) {
	readOnly := newHarness(t, Config{})
	init := readOnly.rpc("initialize", map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if got := init["result"].(map[string]any)["protocolVersion"]; got != ProtocolVersion {
		t.Fatalf("protocolVersion = %v", got)
	}
	names := toolNames(readOnly.rpc("tools/list", nil))
	if !names["search"] || names["upsert_records"] || names["delete_collection"] || names["remember"] {
		t.Fatalf("read-only tools = %v", names)
	}
	full := newHarness(t, Config{AllowWrites: true, AllowAdmin: true, Embedder: wordEmbedder{}})
	names = toolNames(full.rpc("tools/list", nil))
	for _, name := range []string{"create_collection", "delete_collection", "upsert_records", "delete_record", "remember", "recall", "forget", "prune_memories"} {
		if !names[name] {
			t.Fatalf("missing tool %s in %v", name, names)
		}
	}
	if reply := full.rpc("no/such/method", nil); reply["error"].(map[string]any)["code"].(float64) != codeMethodNotFound {
		t.Fatalf("unknown method reply = %v", reply)
	}
	if reply := full.rpc("tools/call", map[string]any{"name": "nope"}); reply["error"] == nil {
		t.Fatalf("unknown tool reply = %v", reply)
	}
}

func TestNotificationsGetNoResponse(t *testing.T) {
	h := newHarness(t, Config{})
	var out bytes.Buffer
	input := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" + `{"jsonrpc":"2.0","id":7,"method":"ping"}` + "\n"
	if err := h.server.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"id":7`) {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCollectionToolsWithText(t *testing.T) {
	h := newHarness(t, Config{AllowWrites: true, AllowAdmin: true, Embedder: wordEmbedder{}})
	h.call("create_collection", map[string]any{"name": "docs", "dimension": 16}, false)
	h.call("upsert_records", map[string]any{"collection": "docs", "records": []any{
		map[string]any{"id": "a", "text": "go vector database engine", "metadata": map[string]any{"lang": "go"}},
		map[string]any{"id": "b", "text": "chocolate cake recipe", "metadata": map[string]any{"lang": "food"}},
	}}, false)
	found := h.call("search", map[string]any{"collection": "docs", "text": "vector database", "top_k": 1}, false)
	results := found["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["id"] != "a" {
		t.Fatalf("search = %v", found)
	}
	if text := results[0].(map[string]any)["payload"].(map[string]any)["text"]; text != "go vector database engine" {
		t.Fatalf("payload text = %v", text)
	}
	filtered := h.call("search", map[string]any{"collection": "docs", "text": "vector database", "filter": map[string]any{"lang": "food"}}, false)
	if got := filtered["results"].([]any); len(got) != 1 || got[0].(map[string]any)["id"] != "b" {
		t.Fatalf("filtered search = %v", filtered)
	}
	record := h.call("get_record", map[string]any{"collection": "docs", "id": "b"}, false)
	if record["vector"] != nil {
		t.Fatalf("get_record leaked the vector: %v", record)
	}
	h.call("search", map[string]any{"collection": "docs", "text": "x", "vector": []float32{1}}, true)
	h.call("upsert_records", map[string]any{"collection": "docs", "records": []any{map[string]any{"id": "c"}}}, true)
	h.call("delete_collection", map[string]any{"name": "docs", "confirm": "wrong"}, true)
	h.call("delete_record", map[string]any{"collection": "docs", "id": "a"}, false)
	h.call("delete_collection", map[string]any{"name": "docs", "confirm": "docs"}, false)
	h.call("describe_collection", map[string]any{"name": "docs"}, true)
}

func TestMemoryLifecycle(t *testing.T) {
	h := newHarness(t, Config{AllowWrites: true, Embedder: wordEmbedder{}})
	// Recall before any memory exists returns nothing rather than failing.
	if got := h.call("recall", map[string]any{"user": "alice", "query": "coffee"}, false)["memories"].([]any); len(got) != 0 {
		t.Fatalf("empty recall = %v", got)
	}
	old := h.call("remember", map[string]any{"user": "alice", "text": "alice likes black coffee"}, false)["id"].(string)
	h.now = h.now.Add(30 * 24 * time.Hour)
	recent := h.call("remember", map[string]any{"user": "alice", "text": "alice now likes green tea not coffee", "metadata": map[string]any{"topic": "drinks"}}, false)["id"].(string)
	h.call("remember", map[string]any{"user": "alice", "text": "alice coffee order code is 1234", "ttl_seconds": 60}, false)
	h.call("remember", map[string]any{"user": "bob", "text": "bob likes coffee too"}, false)
	h.call("remember", map[string]any{"user": "alice", "text": "x", "metadata": map[string]any{"expires_at": 1}}, true)

	recall := func(weight float64) []any {
		return h.call("recall", map[string]any{"user": "alice", "query": "what coffee does alice like", "top_k": 5, "recency_weight": weight}, false)["memories"].([]any)
	}
	memories := recall(0)
	if len(memories) != 3 {
		t.Fatalf("alice should see her 3 memories, got %v", memories)
	}
	for _, item := range memories {
		if strings.Contains(item.(map[string]any)["text"].(string), "bob") {
			t.Fatalf("memory leaked across users: %v", item)
		}
	}
	if first := recall(1)[0].(map[string]any)["id"]; first == old {
		t.Fatalf("recency weight 1 should not rank the 30-day-old memory first")
	}
	// Once the TTL passes, the code memory disappears from recall and is pruned.
	h.now = h.now.Add(2 * time.Minute)
	if got := recall(0); len(got) != 2 {
		t.Fatalf("expired memory still recalled: %v", got)
	}
	if pruned := h.call("prune_memories", map[string]any{"user": "alice"}, false)["pruned"]; pruned != float64(1) {
		t.Fatalf("pruned = %v", pruned)
	}
	filtered := h.call("recall", map[string]any{"user": "alice", "query": "tea", "filter": map[string]any{"topic": "drinks"}}, false)["memories"].([]any)
	if len(filtered) != 1 || filtered[0].(map[string]any)["id"] != recent {
		t.Fatalf("filtered recall = %v", filtered)
	}
	h.call("forget", map[string]any{"user": "alice", "id": old}, false)
	if got := recall(0); len(got) != 1 {
		t.Fatalf("after forget = %v", got)
	}
}

func TestTextFallsBackToServerVectorizer(t *testing.T) {
	registry := embedding.NewRegistryWithModels(embedding.NewModel(embedding.Config{Name: "words", Model: "words", Dimension: 16}, wordEmbedder{}))
	h := newHarnessWithServerEmbedders(t, Config{AllowWrites: true}, registry)
	if _, err := h.server.byName["search"].handler(context.Background(), json.RawMessage(`{"collection":"x","text":"q"}`)); err == nil {
		t.Fatal("search on a missing collection should fail")
	}
	h.call("upsert_records", map[string]any{"collection": "missing", "records": []any{map[string]any{"id": "a", "text": "go"}}}, true)
	// Create a collection that uses the server-side vectorizer, then use text
	// without any embedding model in the MCP server.
	ctx := context.Background()
	if _, err := h.client.CreateCollection(ctx, client.CollectionConfig{Name: "docs", Dimension: 16, Metric: "cosine", ShardCount: 1, Vectorizer: "words"}); err != nil {
		t.Fatal(err)
	}
	h.call("upsert_records", map[string]any{"collection": "docs", "records": []any{
		map[string]any{"id": "a", "text": "go vector database"},
		map[string]any{"id": "b", "text": "chocolate cake"},
	}}, false)
	found := h.call("search", map[string]any{"collection": "docs", "text": "vector database", "top_k": 1}, false)
	result := found["results"].([]any)[0].(map[string]any)
	if result["id"] != "a" {
		t.Fatalf("search = %v", found)
	}
}
