package rest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vectordb/vectordb/internal/cluster"
	"github.com/vectordb/vectordb/internal/engine"
)

func TestRecentLogsAreAuthenticatedBoundedAndSanitized(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler := NewWithOptions(db, nil, Options{APIKey: "0123456789abcdef"}).Handler()
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/logs", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	missing := httptest.NewRequest(http.MethodGet, "/v1/collections/private-name/vectors/secret-record", nil)
	missing.Header.Set("Authorization", "Bearer 0123456789abcdef")
	handler.ServeHTTP(httptest.NewRecorder(), missing)
	request := httptest.NewRequest(http.MethodGet, "/v1/logs?limit=10&level=warn", nil)
	request.Header.Set("Authorization", "Bearer 0123456789abcdef")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private-name") || strings.Contains(response.Body.String(), "secret-record") || strings.Contains(response.Body.String(), "0123456789abcdef") {
		t.Fatalf("event feed leaked request data: %s", response.Body.String())
	}
	var body struct {
		Events    []httpEvent `json:"events"`
		Retention int         `json:"retention"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Retention != 256 || len(body.Events) == 0 || body.Events[0].Route != "GET /v1/collections/{name}/vectors/{id}" || body.Events[0].Status != http.StatusNotFound {
		t.Fatalf("unexpected feed: %+v", body)
	}
	bad := httptest.NewRequest(http.MethodGet, "/v1/logs?limit=201", nil)
	bad.Header.Set("Authorization", "Bearer 0123456789abcdef")
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, bad)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("bad limit status=%d", rejected.Code)
	}
}

func TestClusterLogsMergePeersAndReportPartialFailures(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const localID = "11111111111111111111111111111111"
	const remoteID = "22222222222222222222222222222222"
	const clusterID = "33333333333333333333333333333333"
	fail := false
	peer := cluster.Peer{SeedURL: "http://peer-b:6333", NodeID: remoteID, AdvertiseAddress: "peer-b:6333", Healthy: true}
	client := &http.Client{Transport: restRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-VectorDB-Target-Node-ID") != remoteID || request.Header.Get("Authorization") != "Bearer 0123456789abcdef" {
			t.Errorf("missing peer authentication headers")
		}
		if fail {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header)}, nil
		}
		body := `{"events":[{"sequence":4,"timestamp":"2026-09-05T12:00:00Z","level":"warn","event":"http.server.request","method":"GET","route":"GET /remote","status":404,"duration_ms":1,"trace_id":"trace","span_id":"span"}],"node_id":"` + remoteID + `","metadata_epoch":3}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	handler := NewWithOptions(db, nil, Options{APIKey: "0123456789abcdef", NodeID: localID, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 3, PeerProvider: staticPeerProvider{peer}, InternalHTTPClient: client}).Handler()
	call := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/v1/cluster/logs?limit=10&level=warn", nil)
		request.Header.Set("Authorization", "Bearer 0123456789abcdef")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	response := call()
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"node_id":"`+remoteID+`"`) || !strings.Contains(response.Body.String(), `"partial":false`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	fail = true
	response = call()
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"partial":true`) || !strings.Contains(response.Body.String(), remoteID) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPersistentEventLogRecoversBoundsAndCompacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log := newPersistentEventLog(path, 2)
	for status := 200; status < 205; status++ {
		log.add(httpEvent{Timestamp: time.Now().UTC(), Method: "GET", Route: "GET /safe/{id}", Status: status, TraceID: "trace", SpanID: "span"})
	}
	durable, persistError := log.persistence()
	if !durable || persistError != "" {
		t.Fatalf("durable=%v error=%q", durable, persistError)
	}
	recovered := newPersistentEventLog(path, 2)
	events := recovered.recent(10, "")
	if len(events) != 2 || events[0].Sequence != 5 || events[1].Sequence != 4 {
		t.Fatalf("events=%+v", events)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	recovered.add(httpEvent{Timestamp: time.Now().UTC(), Method: "POST", Route: "POST /safe", Status: 500})
	if got := recovered.recent(1, "")[0]; got.Sequence != 6 || got.Level != "error" {
		t.Fatalf("event=%+v", got)
	}
}

func TestEventLogEvictsOldestEntry(t *testing.T) {
	log := newEventLog(2)
	for status := 200; status < 203; status++ {
		log.add(httpEvent{Status: status, Route: "GET /test"})
	}
	events := log.recent(10, "")
	if len(events) != 2 || events[0].Status != 202 || events[1].Status != 201 || events[0].Sequence != 3 {
		t.Fatalf("events=%+v", events)
	}
}
