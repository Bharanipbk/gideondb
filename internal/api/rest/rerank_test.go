package rest

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
	"github.com/Bharanipbk/gideondb/internal/rerank"
)

const namedCollection = `{"name":"docs","dimension":2,"metric":"dot","shard_count":2,"vectors":{
	"title":{"dimension":2,"metric":"dot","datatype":"float16","index":{"type":"hnsw"}},
	"colbert":{"dimension":2,"metric":"dot","multi_vector":true}}}`

const namedBatch = `{"records":[
	{"id":"a","vector":[1,0],"text":"apples","payload":{"doc":"alpha"},"vectors":{"title":[1,0]},"multi_vectors":{"colbert":[[1,0],[0,0.1]]}},
	{"id":"b","vector":[0.9,0],"text":"bananas","payload":{"doc":"beta"},"vectors":{"title":[0,1]},"multi_vectors":{"colbert":[[0.5,0],[0,1]]}},
	{"id":"c","vector":[0.8,0],"text":"cherries","payload":{"doc":"gamma"}}]}`

// scoreByLength is a fake cross-encoder that ranks longer documents higher.
func rerankServer(t *testing.T, delay time.Duration, fail *atomic.Bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		if fail != nil && fail.Load() {
			http.Error(w, "overloaded", http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		documents := strings.Split(strings.SplitN(string(body), `"documents":[`, 2)[1], `]`)[0]
		var results []string
		for i, document := range strings.Split(documents, ",") {
			results = append(results, fmt.Sprintf(`{"index":%d,"relevance_score":%d}`, i, len(document)))
		}
		_, _ = fmt.Fprintf(w, `{"results":[%s]}`, strings.Join(results, ","))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestNamedVectorAndRerankSearch(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var fail atomic.Bool
	fast := rerankServer(t, 0, &fail)
	slow := rerankServer(t, 2*time.Second, nil)
	registry := rerank.NewRegistryWithModels(
		rerank.NewModel(rerank.Config{Name: "length", URL: fast.URL, Model: "len", MaxDocuments: 10}, "", nil),
		rerank.NewModel(rerank.Config{Name: "slow", URL: slow.URL, Model: "len", TimeoutSeconds: 1}, "", nil),
	)
	h := NewWithOptions(db, nil, Options{Rerankers: registry}).Handler()
	if code, body := send(t, h, http.MethodPost, "/v1/collections", namedCollection); code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, body)
	}
	if code, body := send(t, h, http.MethodPost, "/v1/collections/docs/vectors/batch", namedBatch); code != http.StatusOK {
		t.Fatalf("batch = %d %v", code, body)
	}
	ids := func(body map[string]any) string {
		var out []string
		for _, item := range body["results"].([]any) {
			out = append(out, item.(map[string]any)["id"].(string))
		}
		return strings.Join(out, ",")
	}
	if code, body := send(t, h, http.MethodGet, "/v1/collections/docs/vectors/b", ""); code != http.StatusOK || body["multi_vectors"] == nil || body["vectors"] == nil {
		t.Fatalf("get = %d %v", code, body)
	}
	if code, body := send(t, h, http.MethodPost, "/v1/collections/docs/search", `{"using":"title","vector":[0,1],"top_k":1}`); code != http.StatusOK || ids(body) != "b" {
		t.Fatalf("named search = %d %v", code, body)
	}
	if code, body := send(t, h, http.MethodPost, "/v1/collections/docs/search", `{"using":"colbert","multi_vector":[[0,1]],"top_k":2,"candidates":10}`); code != http.StatusOK || ids(body) != "b,a" {
		t.Fatalf("multi-vector search = %d %v", code, body)
	}
	// MaxSim rerank over default-vector candidates; c has no tokens.
	code, body := send(t, h, http.MethodPost, "/v1/collections/docs/search", `{"vector":[1,0],"top_k":3,"rerank":{"type":"maxsim","field":"colbert","multi_vector":[[0,1]]}}`)
	if code != http.StatusOK || ids(body) != "b,a" || body["rerank"].(map[string]any)["applied"] != true {
		t.Fatalf("maxsim rerank = %d %v", code, body)
	}
	// Cross-encoder over record text: "cherries" > "bananas" > "apples".
	code, body = send(t, h, http.MethodPost, "/v1/collections/docs/search", `{"vector":[1,0],"top_k":2,"rerank":{"type":"cross_encoder","model":"length","query":"fruit"}}`)
	if code != http.StatusOK || ids(body) != "c,b" || body["rerank"].(map[string]any)["applied"] != true || strings.Contains(fmt.Sprint(body), "cherries") {
		t.Fatalf("cross-encoder rerank = %d %v", code, body)
	}
	// Payload documents: "gamma" and "alpha" tie on length, beta is shorter.
	code, body = send(t, h, http.MethodPost, "/v1/collections/docs/search", `{"vector":[1,0],"top_k":1,"rerank":{"type":"cross_encoder","model":"length","query":"q","document":"payload.doc","candidates":3}}`)
	if code != http.StatusOK || ids(body) != "a" {
		t.Fatalf("payload rerank = %d %v", code, body)
	}
	// Failures and timeouts keep the first-stage order and say so.
	fail.Store(true)
	for _, model := range []string{"length", "slow"} {
		code, body = send(t, h, http.MethodPost, "/v1/collections/docs/search", `{"vector":[1,0],"top_k":2,"rerank":{"type":"cross_encoder","model":"`+model+`","query":"fruit"}}`)
		status := body["rerank"].(map[string]any)
		if code != http.StatusOK || ids(body) != "a,b" || status["applied"] != false || status["error"] == "" {
			t.Fatalf("%s fallback = %d %v", model, code, body)
		}
	}
	for _, bad := range []string{
		`{"using":"title","vector":[1,0],"text":"x","top_k":1}`,
		`{"multi_vector":[[1,0]],"top_k":1}`,
		`{"vector":[1,0],"top_k":1,"candidates":5}`,
		`{"vector":[1,0],"top_k":1,"rerank":{"type":"cross_encoder","model":"missing","query":"q"}}`,
		`{"vector":[1,0],"top_k":1,"rerank":{"type":"cross_encoder","model":"length","query":"q","candidates":11}}`,
		`{"vector":[1,0],"top_k":1,"rerank":{"type":"maxsim","field":"title","multi_vector":[[1,0]]}}`,
		`{"vector":[1,0],"top_k":1,"rerank":{"type":"maxsim","field":"colbert","multi_vector":[[1,0]],"candidates":1001}}`,
	} {
		if code, body := send(t, h, http.MethodPost, "/v1/collections/docs/search", bad); code != http.StatusBadRequest {
			t.Fatalf("%s = %d %v", bad, code, body)
		}
	}
	if code, body := send(t, h, http.MethodGet, "/v1/rerankers", ""); code != http.StatusOK || len(body["rerankers"].([]any)) != 2 {
		t.Fatalf("rerankers = %d %v", code, body)
	}
}

func TestDistributedNamedSearchAndRerank(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 8, Vectors: map[string]core.VectorField{
		"colbert": {Dimension: 2, Metric: core.MetricDot, MultiVector: true},
	}}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	const localNode = "11111111111111111111111111111111"
	const remoteNode = "22222222222222222222222222222222"
	const clusterID = "33333333333333333333333333333333"
	peer := cluster.Peer{SeedURL: "http://peer-b:6333", NodeID: remoteNode, ClusterID: clusterID, AdvertiseAddress: "peer-b:6333", Healthy: true, State: cluster.PeerHealthy}
	digests, err := cluster.ComputeViewDigests(7, localNode, "node-a:6333", []cluster.Peer{peer}, []core.CollectionConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	peer.MetadataEpoch = 7
	peer.MembershipDigest, peer.CatalogDigest, peer.PlacementDigest = digests.Membership, digests.Catalog, digests.Placement
	plan, err := cluster.PlanPlacement(7, localNode, "node-a:6333", []cluster.Peer{peer}, []core.CollectionConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	// Store one record in a locally owned shard.
	localID := ""
	for i := 0; localID == "" && i < 1000; i++ {
		id := "local-" + strconv.Itoa(i)
		shardID, _ := db.RouteShard("docs", "", id)
		for _, assignment := range plan.Shards {
			if assignment.ShardID == shardID && assignment.NodeID == localNode {
				localID = id
			}
		}
	}
	if localID == "" {
		t.Fatal("no locally owned shard")
	}
	if _, err := db.Upsert("docs", core.Record{ID: localID, Vector: []float32{1, 0}, Text: "a long local document", MultiVectors: map[string][][]float32{"colbert": {{0, 2}}}}); err != nil {
		t.Fatal(err)
	}
	var bodies atomic.Value
	client := &http.Client{Transport: restRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		payload, _ := io.ReadAll(request.Body)
		bodies.Store(string(payload))
		parts := strings.Split(request.URL.Path, "/")
		shardID, _ := strconv.ParseUint(parts[len(parts)-2], 10, 32)
		body := fmt.Sprintf(`{"results":[{"id":"remote-%d","score":1,"text":"short"}],"shard_id":%d,"metadata_epoch":7}`, shardID, shardID)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	server := rerankServer(t, 0, nil)
	registry := rerank.NewRegistryWithModels(rerank.NewModel(rerank.Config{Name: "length", URL: server.URL, Model: "len"}, "", nil))
	store := committedTestRaftStore(t, localNode, []string{localNode, remoteNode}, 7, digests)
	handler := NewWithOptions(db, nil, Options{APIKey: "0123456789abcdef", NodeID: localNode, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 7, PeerProvider: staticPeerProvider{peer}, InternalHTTPClient: client, EnableStaticRouting: true, RaftStore: store, Rerankers: registry}).Handler()
	call := func(body string) (int, string) {
		request := httptest.NewRequest(http.MethodPost, "/v1/cluster/collections/docs/search", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer 0123456789abcdef")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code, response.Body.String()
	}
	code, body := call(`{"using":"colbert","multi_vector":[[0,1]],"top_k":1}`)
	if code != http.StatusOK || !strings.Contains(body, `"id":"`+localID+`","score":2`) || !strings.Contains(bodies.Load().(string), `"using":"colbert"`) {
		t.Fatalf("named = %d %s / %v", code, body, bodies.Load())
	}
	code, body = call(`{"vector":[1,0],"top_k":1,"rerank":{"type":"maxsim","field":"colbert","multi_vector":[[0,1]]}}`)
	if code != http.StatusOK || !strings.Contains(body, `"id":"`+localID+`","score":2`) || !strings.Contains(body, `"applied":true`) || !strings.Contains(bodies.Load().(string), `"top_k":50`) {
		t.Fatalf("maxsim = %d %s / %v", code, body, bodies.Load())
	}
	// The cross-encoder runs on the coordinator over local text (attached
	// here) and remote text (attached by peers); text is not returned.
	code, body = call(`{"vector":[1,0],"top_k":1,"rerank":{"type":"cross_encoder","model":"length","query":"q"}}`)
	if code != http.StatusOK || !strings.Contains(body, `"id":"`+localID+`"`) || !strings.Contains(body, `"applied":true`) || strings.Contains(body, `"text"`) {
		t.Fatalf("cross-encoder = %d %s", code, body)
	}
}
