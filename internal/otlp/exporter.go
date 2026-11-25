// Package otlp exports server spans to an OpenTelemetry collector with the
// OTLP/HTTP JSON protocol (POST {endpoint}/v1/traces). It has no
// dependencies beyond the standard library.
//
// Spans are queued in a bounded buffer and sent in batches by one
// goroutine. When the collector is slow or down, new spans are dropped and
// counted rather than blocking requests.
package otlp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Span is one finished server span.
type Span struct {
	TraceID, SpanID, ParentSpanID string // lowercase hex
	Name                          string
	Start, End                    time.Time
	Attributes                    map[string]any // string, bool, int, int64, float64
	Error                         bool
}

// Config configures an Exporter.
type Config struct {
	// Endpoint is the collector base URL, e.g. http://otel-collector:4318.
	Endpoint string
	// Headers are sent with every export, e.g. an authorization token.
	Headers map[string]string
	// ServiceName and InstanceID become resource attributes.
	ServiceName, InstanceID, Version string
	// QueueSize bounds buffered spans (default 4096); BatchSize bounds one
	// export (default 512); Interval is the flush period (default 5s).
	QueueSize, BatchSize int
	Interval             time.Duration
	Client               *http.Client
	Logger               *slog.Logger
}

// Exporter batches spans to an OTLP/HTTP collector.
type Exporter struct {
	config   Config
	target   string
	queue    chan Span
	flush    chan chan struct{}
	done     chan struct{}
	stopped  chan struct{}
	once     sync.Once
	exported atomic.Uint64
	dropped  atomic.Uint64
	failed   atomic.Uint64
}

// New validates config and starts the export loop.
func New(config Config) (*Exporter, error) {
	parsed, err := url.Parse(strings.TrimRight(config.Endpoint, "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("otlp endpoint must be an http(s) URL, got %q", config.Endpoint)
	}
	if config.ServiceName == "" {
		config.ServiceName = "gideondb"
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 4096
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 512
	}
	if config.Interval <= 0 {
		config.Interval = 5 * time.Second
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if config.Logger == nil {
		config.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	target := parsed.String()
	if !strings.HasSuffix(target, "/v1/traces") {
		target += "/v1/traces"
	}
	e := &Exporter{config: config, target: target, queue: make(chan Span, config.QueueSize),
		flush: make(chan chan struct{}), done: make(chan struct{}), stopped: make(chan struct{})}
	go e.loop()
	return e, nil
}

// Export queues a span without blocking; it is dropped when the queue is full
// or the exporter is closed.
func (e *Exporter) Export(span Span) {
	select {
	case <-e.done:
		e.dropped.Add(1)
		return
	default:
	}
	select {
	case e.queue <- span:
	default:
		e.dropped.Add(1)
	}
}

// Stats reports exported, dropped and failed span counts.
func (e *Exporter) Stats() (exported, dropped, failed uint64) {
	return e.exported.Load(), e.dropped.Load(), e.failed.Load()
}

// Flush sends every queued span and waits for the result.
func (e *Exporter) Flush(ctx context.Context) error {
	reply := make(chan struct{})
	select {
	case e.flush <- reply:
	case <-e.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-reply:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close flushes queued spans (bounded by ctx) and stops the exporter.
func (e *Exporter) Close(ctx context.Context) error {
	err := e.Flush(ctx)
	e.once.Do(func() { close(e.done) })
	select {
	case <-e.stopped:
	case <-ctx.Done():
		return ctx.Err()
	}
	return err
}

func (e *Exporter) loop() {
	defer close(e.stopped)
	ticker := time.NewTicker(e.config.Interval)
	defer ticker.Stop()
	batch := make([]Span, 0, e.config.BatchSize)
	send := func() {
		if len(batch) > 0 {
			e.send(batch)
			batch = batch[:0]
		}
	}
	drain := func() {
		for {
			select {
			case span := <-e.queue:
				batch = append(batch, span)
				if len(batch) == e.config.BatchSize {
					send()
				}
			default:
				send()
				return
			}
		}
	}
	for {
		select {
		case span := <-e.queue:
			batch = append(batch, span)
			if len(batch) == e.config.BatchSize {
				send()
			}
		case <-ticker.C:
			send()
		case reply := <-e.flush:
			drain()
			close(reply)
		case <-e.done:
			drain()
			return
		}
	}
}

func (e *Exporter) send(spans []Span) {
	payload, err := json.Marshal(e.encode(spans))
	if err != nil {
		e.failed.Add(uint64(len(spans)))
		return
	}
	request, err := http.NewRequest(http.MethodPost, e.target, bytes.NewReader(payload))
	if err != nil {
		e.failed.Add(uint64(len(spans)))
		return
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range e.config.Headers {
		request.Header.Set(key, value)
	}
	response, err := e.config.Client.Do(request)
	if err != nil {
		e.failed.Add(uint64(len(spans)))
		e.config.Logger.Warn("otlp.export.failed", "error", err.Error(), "spans", len(spans))
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
	_ = response.Body.Close()
	if response.StatusCode/100 != 2 {
		e.failed.Add(uint64(len(spans)))
		e.config.Logger.Warn("otlp.export.failed", "status", response.StatusCode, "spans", len(spans))
		return
	}
	e.exported.Add(uint64(len(spans)))
}

// OTLP/JSON encodes trace and span IDs as hex strings and 64-bit integers
// as decimal strings.
type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type anyValue struct {
	String *string  `json:"stringValue,omitempty"`
	Bool   *bool    `json:"boolValue,omitempty"`
	Int    *string  `json:"intValue,omitempty"`
	Double *float64 `json:"doubleValue,omitempty"`
}

func attribute(key string, value any) keyValue {
	var v anyValue
	switch typed := value.(type) {
	case string:
		v.String = &typed
	case bool:
		v.Bool = &typed
	case int:
		s := strconv.Itoa(typed)
		v.Int = &s
	case int64:
		s := strconv.FormatInt(typed, 10)
		v.Int = &s
	case float64:
		v.Double = &typed
	default:
		s := fmt.Sprint(typed)
		v.String = &s
	}
	return keyValue{Key: key, Value: v}
}

func (e *Exporter) encode(spans []Span) map[string]any {
	resource := []keyValue{attribute("service.name", e.config.ServiceName)}
	if e.config.InstanceID != "" {
		resource = append(resource, attribute("service.instance.id", e.config.InstanceID))
	}
	if e.config.Version != "" {
		resource = append(resource, attribute("service.version", e.config.Version))
	}
	encoded := make([]map[string]any, len(spans))
	for i, span := range spans {
		attributes := make([]keyValue, 0, len(span.Attributes))
		for _, key := range sortedKeys(span.Attributes) {
			attributes = append(attributes, attribute(key, span.Attributes[key]))
		}
		item := map[string]any{
			"traceId": span.TraceID, "spanId": span.SpanID, "name": span.Name,
			"kind":              2, // SPAN_KIND_SERVER
			"startTimeUnixNano": strconv.FormatInt(span.Start.UnixNano(), 10),
			"endTimeUnixNano":   strconv.FormatInt(span.End.UnixNano(), 10),
			"attributes":        attributes,
		}
		if span.ParentSpanID != "" {
			item["parentSpanId"] = span.ParentSpanID
		}
		if span.Error {
			item["status"] = map[string]any{"code": 2} // STATUS_CODE_ERROR
		}
		encoded[i] = item
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": resource},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "github.com/Bharanipbk/gideondb"}, "spans": encoded}},
	}}}
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Sampled reports whether a new root trace is kept at ratio, decided from
// the trace ID so every node agrees (the OpenTelemetry TraceIdRatioBased
// rule over the low 8 bytes).
func Sampled(traceID string, ratio float64) bool {
	if ratio >= 1 {
		return true
	}
	if ratio <= 0 {
		return false
	}
	raw, err := hex.DecodeString(traceID)
	if err != nil || len(raw) != 16 {
		return false
	}
	return binary.BigEndian.Uint64(raw[8:]) < uint64(ratio*math.MaxUint64)
}

// ParseHeaders parses "key=value,key2=value2" (OTEL_EXPORTER_OTLP_HEADERS).
func ParseHeaders(raw string) (map[string]string, error) {
	headers := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("otlp header %q must be key=value", pair)
		}
		decoded, err := url.QueryUnescape(strings.TrimSpace(value))
		if err != nil {
			return nil, err
		}
		headers[strings.TrimSpace(key)] = decoded
	}
	return headers, nil
}
