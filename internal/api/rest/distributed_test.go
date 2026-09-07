package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

type restRoundTripFunc func(*http.Request) (*http.Response, error)

func (function restRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestDistributedSearchFanoutFencingAndPartialFailures(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 32}
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
	remoteShards := 0
	for _, assignment := range plan.Shards {
		if assignment.NodeID == remoteNode {
			remoteShards++
		}
	}
	if remoteShards == 0 {
		t.Fatal("test placement has no remote shards")
	}

	var fail atomic.Bool
	var calls atomic.Int64
	client := &http.Client{Transport: restRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		for name, want := range map[string]string{"Authorization": "Bearer 0123456789abcdef", "X-GideonDB-Cluster-ID": clusterID, "X-GideonDB-Target-Node-ID": remoteNode, "X-GideonDB-Metadata-Epoch": "7"} {
			if got := request.Header.Get(name); got != want {
				return nil, fmt.Errorf("%s=%q, want %q", name, got, want)
			}
		}
		if fail.Load() {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header)}, nil
		}
		parts := strings.Split(request.URL.Path, "/")
		shardID, err := strconv.ParseUint(parts[len(parts)-2], 10, 32)
		if err != nil {
			return nil, err
		}
		body := fmt.Sprintf(`{"results":[{"id":"remote-%d","score":10}],"shard_id":%d,"metadata_epoch":7}`, shardID, shardID)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	store := committedTestRaftStore(t, localNode, []string{localNode, remoteNode}, 7, digests)
	handler := NewWithOptions(db, nil, Options{APIKey: "0123456789abcdef", NodeID: localNode, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 7, PeerProvider: staticPeerProvider{peer}, InternalHTTPClient: client, EnableStaticRouting: true, RaftStore: store}).Handler()
	call := func(allowPartial bool) *httptest.ResponseRecorder {
		body := `{"vector":[1,0],"top_k":5}`
		if allowPartial {
			body = `{"vector":[1,0],"top_k":5,"allow_partial":true}`
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/cluster/collections/docs/search", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer 0123456789abcdef")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	response := call(false)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"partial":false`) || !strings.Contains(response.Body.String(), `"id":"remote-`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if calls.Load() != int64(remoteShards) {
		t.Fatalf("remote calls=%d, want %d", calls.Load(), remoteShards)
	}

	fail.Store(true)
	response = call(false)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "distributed_search_failed") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	response = call(true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"partial":true`) || !strings.Contains(response.Body.String(), `"failures":[{`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMergeDistributedTopKIsBoundedAndDeterministic(t *testing.T) {
	results := mergeDistributedTopK([][]core.SearchResult{{{ID: "b", Score: 2}, {ID: "c", Score: 1}}, {{ID: "a", Score: 2}, {ID: "d", Score: 3}}}, 3)
	if len(results) != 3 || results[0].ID != "d" || results[1].ID != "a" || results[2].ID != "b" {
		t.Fatalf("unexpected merge: %#v", results)
	}
}

func TestDistributedBatchWriteOutcomesAndValidation(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	config := core.CollectionConfig{Name: "writes", Dimension: 2, Metric: core.MetricDot, ShardCount: 32}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	const localNode = "11111111111111111111111111111111"
	const remoteNode = "22222222222222222222222222222222"
	const clusterID = "33333333333333333333333333333333"
	peer := cluster.Peer{SeedURL: "http://peer-b:6333", NodeID: remoteNode, ClusterID: clusterID, AdvertiseAddress: "peer-b:6333", Healthy: true, State: cluster.PeerHealthy}
	digests, err := cluster.ComputeViewDigests(9, localNode, "node-a:6333", []cluster.Peer{peer}, []core.CollectionConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	peer.MetadataEpoch = 9
	peer.MembershipDigest, peer.CatalogDigest, peer.PlacementDigest = digests.Membership, digests.Catalog, digests.Placement
	plan, err := cluster.PlanPlacement(9, localNode, "node-a:6333", []cluster.Peer{peer}, []core.CollectionConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	ownerByShard := make(map[uint32]string)
	for _, assignment := range plan.Shards {
		ownerByShard[assignment.ShardID] = assignment.NodeID
	}
	var localRecord, remoteRecord core.Record
	for candidate := 0; localRecord.ID == "" || remoteRecord.ID == ""; candidate++ {
		record := core.Record{ID: fmt.Sprintf("record-%d", candidate), Vector: []float32{1, 0}}
		shardID, routeErr := db.RouteShard("writes", "", record.ID)
		if routeErr != nil {
			t.Fatal(routeErr)
		}
		if ownerByShard[shardID] == localNode && localRecord.ID == "" {
			localRecord = record
		}
		if ownerByShard[shardID] == remoteNode && remoteRecord.ID == "" {
			remoteRecord = record
		}
		if candidate > 10_000 {
			t.Fatal("could not find records for local and remote owners")
		}
	}
	var ambiguous atomic.Bool
	var calls atomic.Int64
	client := &http.Client{Transport: restRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if ambiguous.Load() {
			return nil, errors.New("response lost")
		}
		if request.Header.Get("X-GideonDB-Metadata-Epoch") != "9" || request.Header.Get("X-GideonDB-Target-Node-ID") != remoteNode {
			return nil, errors.New("missing write fence")
		}
		if request.Header.Get("Idempotency-Key") != "batch:001" {
			return nil, errors.New("missing idempotency key")
		}
		var incoming internalShardBatchRequest
		if err := json.NewDecoder(request.Body).Decode(&incoming); err != nil {
			return nil, err
		}
		parts := strings.Split(request.URL.Path, "/")
		shardID, err := strconv.ParseUint(parts[len(parts)-3], 10, 32)
		if err != nil {
			return nil, err
		}
		for position := range incoming.Records {
			incoming.Records[position].Version = 10
			incoming.Records[position].Timestamp = 1
		}
		payload, err := json.Marshal(map[string]any{"records": incoming.Records, "shard_id": shardID, "metadata_epoch": 9, "replicas_acknowledged": 1, "replication_factor": 1, "acknowledgement": incoming.Acknowledgement})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(payload))), Header: make(http.Header)}, nil
	})}
	store := committedTestRaftStore(t, localNode, []string{localNode, remoteNode}, 9, digests)
	handler := NewWithOptions(db, nil, Options{NodeID: localNode, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 9, PeerProvider: staticPeerProvider{peer}, InternalHTTPClient: client, EnableStaticRouting: true, RaftStore: store}).Handler()
	call := func(records []core.Record, keys ...string) *httptest.ResponseRecorder {
		payload, marshalErr := json.Marshal(map[string]any{"records": records})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/cluster/collections/writes/vectors/batch", strings.NewReader(string(payload)))
		request.Header.Set("Content-Type", "application/json")
		if len(keys) != 0 {
			request.Header.Set("Idempotency-Key", keys[0])
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	response := call([]core.Record{localRecord, remoteRecord}, "batch:001")
	if response.Code != http.StatusOK || strings.Count(response.Body.String(), `"status":"committed"`) != 2 || !strings.Contains(response.Body.String(), `"partial":false`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := db.Get("writes", "", localRecord.ID); err != nil {
		t.Fatalf("local record not committed: %v", err)
	}
	firstLocal, _ := db.Get("writes", "", localRecord.ID)
	response = call([]core.Record{localRecord, remoteRecord}, "batch:001")
	retriedLocal, _ := db.Get("writes", "", localRecord.ID)
	if response.Code != http.StatusOK || retriedLocal.Version != firstLocal.Version || retriedLocal.Timestamp != firstLocal.Timestamp {
		t.Fatalf("idempotent retry changed local record: first=%#v retry=%#v status=%d", firstLocal, retriedLocal, response.Code)
	}
	before := calls.Load()
	response = call([]core.Record{{ID: "invalid", Vector: []float32{1}}})
	if response.Code != http.StatusBadRequest || calls.Load() != before {
		t.Fatalf("validation status=%d calls=%d", response.Code, calls.Load())
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/cluster/collections/writes/vectors/batch", strings.NewReader(`{"records":[{"id":"invalid-ack","vector":[1,0]}],"acknowledgement":"eventual"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || calls.Load() != before || !strings.Contains(response.Body.String(), "acknowledgement") {
		t.Fatalf("ack validation status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	ambiguous.Store(true)
	response = call([]core.Record{remoteRecord})
	if response.Code != http.StatusMultiStatus || !strings.Contains(response.Body.String(), `"status":"unknown"`) || !strings.Contains(response.Body.String(), `"partial":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
