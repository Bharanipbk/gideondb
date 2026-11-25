package otlp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExporterSendsOTLPJSON(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	var auth string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("bad request %s %s", r.URL.Path, r.Header.Get("Content-Type"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		auth = r.Header.Get("Authorization")
		mu.Unlock()
	}))
	defer collector.Close()
	exporter, err := New(Config{Endpoint: collector.URL, Headers: map[string]string{"Authorization": "Bearer t"}, InstanceID: "node-1", BatchSize: 2, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(100, 5)
	for i := 0; i < 3; i++ {
		exporter.Export(Span{TraceID: strings.Repeat("a", 32), SpanID: strings.Repeat("b", 16), ParentSpanID: strings.Repeat("c", 16),
			Name: "POST /v1/collections/{name}/search", Start: start, End: start.Add(time.Millisecond),
			Attributes: map[string]any{"http.response.status_code": 500, "http.route": "/x"}, Error: i == 0})
	}
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || auth != "Bearer t" {
		t.Fatalf("batches = %d, auth = %q", len(bodies), auth)
	}
	encoded, _ := json.Marshal(bodies[0])
	for _, want := range []string{`"service.name"`, `"stringValue":"gideondb"`, `"service.instance.id"`, `"parentSpanId":"cccccccccccccccc"`,
		`"startTimeUnixNano":"100000000005"`, `"kind":2`, `"intValue":"500"`, `"status":{"code":2}`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("payload missing %s: %s", want, encoded)
		}
	}
	if exported, dropped, failed := exporter.Stats(); exported != 3 || dropped != 0 || failed != 0 {
		t.Fatalf("stats = %d %d %d", exported, dropped, failed)
	}
	exporter.Export(Span{})
	if _, dropped, _ := exporter.Stats(); dropped != 1 {
		t.Fatal("export after close must be dropped")
	}
}

func TestExporterDropsWhenQueueFullAndCountsFailures(t *testing.T) {
	block := make(chan struct{})
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer collector.Close()
	exporter, err := New(Config{Endpoint: collector.URL + "/v1/traces", QueueSize: 2, BatchSize: 1, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		exporter.Export(Span{TraceID: strings.Repeat("a", 32), SpanID: strings.Repeat("b", 16)})
	}
	if _, dropped, _ := exporter.Stats(); dropped == 0 {
		t.Fatal("full queue must drop spans instead of blocking")
	}
	close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exporter.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if exported, _, failed := exporter.Stats(); exported != 0 || failed == 0 {
		t.Fatalf("503 must count as failure: exported=%d failed=%d", exported, failed)
	}
}

func TestSampledIsDeterministicAndProportional(t *testing.T) {
	if !Sampled(strings.Repeat("f", 32), 1) || Sampled(strings.Repeat("0", 31)+"1", 0) {
		t.Fatal("ratio bounds")
	}
	kept := 0
	for i := 0; i < 20000; i++ {
		id := randomTraceID(i)
		if Sampled(id, 0.25) != Sampled(id, 0.25) {
			t.Fatal("not deterministic")
		}
		if Sampled(id, 0.25) {
			kept++
		}
	}
	if kept < 4500 || kept > 5500 {
		t.Fatalf("kept %d of 20000 at 0.25", kept)
	}
}

func randomTraceID(i int) string {
	// A cheap deterministic spread over the low 8 bytes.
	x := uint64(i)*0x9E3779B97F4A7C15 + 0x632BE59BD9B4E019
	const hexdigits = "0123456789abcdef"
	b := []byte(strings.Repeat("0", 16))
	for j := 15; j >= 0; j-- {
		b[j] = hexdigits[x&15]
		x >>= 4
	}
	return strings.Repeat("1", 16) + string(b)
}

func TestNewRejectsBadEndpointAndParsesHeaders(t *testing.T) {
	if _, err := New(Config{Endpoint: "otel:4318"}); err == nil {
		t.Fatal("endpoint without scheme accepted")
	}
	headers, err := ParseHeaders("authorization=Bearer%20x, x-tenant = a ")
	if err != nil || headers["authorization"] != "Bearer x" || headers["x-tenant"] != "a" {
		t.Fatalf("headers = %v, %v", headers, err)
	}
	if _, err := ParseHeaders("novalue"); err == nil {
		t.Fatal("malformed header accepted")
	}
}
