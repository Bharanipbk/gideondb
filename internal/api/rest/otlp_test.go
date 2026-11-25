package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/engine"
	"github.com/Bharanipbk/gideondb/internal/otlp"
)

type spanRecorder struct {
	mu    sync.Mutex
	spans []otlp.Span
}

func (r *spanRecorder) Export(span otlp.Span) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spans = append(r.spans, span)
}

func TestServerSpansFollowTraceparentAndSampling(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder := &spanRecorder{}
	handler := NewWithOptions(db, nil, Options{SpanExporter: recorder, TraceSampleRatio: 0}).Handler()

	parent := "00-" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 16) + "-01"
	request := httptest.NewRequest(http.MethodGet, "/v1/collections", nil)
	request.Header.Set("traceparent", parent)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if len(recorder.spans) != 1 {
		t.Fatalf("sampled parent: spans = %d", len(recorder.spans))
	}
	span := recorder.spans[0]
	if span.TraceID != strings.Repeat("a", 32) || span.ParentSpanID != strings.Repeat("b", 16) || span.Name != "GET /v1/collections" || span.Attributes["http.response.status_code"] != http.StatusOK {
		t.Fatalf("span = %+v", span)
	}

	// Ratio 0 drops new roots and tells downstream services so.
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/collections", nil))
	if len(recorder.spans) != 1 || !strings.HasSuffix(response.Header().Get("traceparent"), "-00") {
		t.Fatalf("unsampled root exported or flagged wrong: %d %s", len(recorder.spans), response.Header().Get("traceparent"))
	}

	// An explicitly unsampled parent is not exported either.
	request = httptest.NewRequest(http.MethodGet, "/v1/collections", nil)
	request.Header.Set("traceparent", strings.TrimSuffix(parent, "01")+"00")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if len(recorder.spans) != 1 {
		t.Fatal("unsampled parent was exported")
	}

	// Without an exporter, roots stay sampled as before.
	plain := New(db, nil).Handler()
	response = httptest.NewRecorder()
	plain.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/collections", nil))
	if !strings.HasSuffix(response.Header().Get("traceparent"), "-01") {
		t.Fatalf("default traceparent = %s", response.Header().Get("traceparent"))
	}
}
