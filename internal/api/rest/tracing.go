package rest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

type traceContext struct{ traceID, spanID, flags string }
type traceContextKey struct{}

func parseTraceparent(value string) (traceContext, bool) {
	parts := strings.Split(value, "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return traceContext{}, false
	}
	for _, part := range parts[1:] {
		if _, err := hex.DecodeString(part); err != nil {
			return traceContext{}, false
		}
	}
	if parts[1] == strings.Repeat("0", 32) || parts[2] == strings.Repeat("0", 16) {
		return traceContext{}, false
	}
	return traceContext{traceID: strings.ToLower(parts[1]), flags: strings.ToLower(parts[3])}, true
}

func randomHex(bytes int) string {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		panic("cryptographic randomness unavailable: " + err.Error())
	}
	return hex.EncodeToString(value)
}

func newServerTrace(r *http.Request) traceContext {
	trace, ok := parseTraceparent(r.Header.Get("traceparent"))
	if !ok {
		trace = traceContext{traceID: randomHex(16), flags: "01"}
	}
	trace.spanID = randomHex(8)
	return trace
}

func (t traceContext) header() string { return "00-" + t.traceID + "-" + t.spanID + "-" + t.flags }

func TraceFromContext(ctx context.Context) (traceID, spanID string, ok bool) {
	trace, ok := ctx.Value(traceContextKey{}).(traceContext)
	return trace.traceID, trace.spanID, ok
}

func (s *Server) traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trace := newServerTrace(r)
		w.Header().Set("traceparent", trace.header())
		tracked := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		*r = *r.WithContext(context.WithValue(r.Context(), traceContextKey{}, trace))
		next.ServeHTTP(tracked, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		s.logger.Info("http.server.request",
			"trace_id", trace.traceID, "span_id", trace.spanID,
			"method", r.Method, "route", route, "status", tracked.status,
			"duration_ms", float64(time.Since(started).Microseconds())/1000,
		)
		s.events.add(httpEvent{Timestamp: time.Now().UTC(), Method: r.Method, Route: route, Status: tracked.status, DurationMS: float64(time.Since(started).Microseconds()) / 1000, TraceID: trace.traceID, SpanID: trace.spanID})
	})
}
