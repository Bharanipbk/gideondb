# Observability

## Metrics

`GET /metrics` exposes Prometheus text-format counters, latency histograms, an
in-flight request gauge, collection count, and live vectors by collection.
Request labels use bounded HTTP route templates. The endpoint shares the REST
listener and needs the same bearer credential as the REST API (any role), so
scrape it with `authorization: {type: Bearer, credentials_file: ...}`.

Replication adds `gideondb_replication_operations_total`, labeled only by the
bounded `append|snapshot` operation and `success|failure` result, plus
`gideondb_replication_lag_sequences`. The lag gauge reports the difference
between the leader WAL sequence and the last acknowledged sequence for each
configured collection, shard, and follower node. A zero value means the
follower has acknowledged the current leader sequence; it does not independently
prove semantic equality, which is covered by consistency validation.

## Tracing foundation

The HTTP boundary implements W3C Trace Context propagation. A valid incoming
`traceparent` contributes its trace ID and sampling flags; the server generates
a fresh span ID and returns the resulting context in the response. Without a
valid parent, the server creates a new sampled trace.

Each completed request emits a structured `http.server.request` log with:

- `trace_id` and `span_id`
- HTTP method and route template
- response status
- elapsed milliseconds

The log deliberately excludes raw paths, query strings, bodies, collection
names, namespaces, vector IDs, vectors, metadata, and payloads.

Production nodes also append this same sanitized event shape to a mode-`0600`,
bounded JSON-lines file in the data directory. The latest configured entries
(4,096 by default; 256–1,000,000 via `-audit-retention`) survive
restart; compaction is atomic. `/v1/cluster/logs` uses authenticated and
metadata-epoch-fenced peer requests to merge recent entries and reports
unavailable nodes explicitly.

## OpenTelemetry traces

GideonDB can export one server span per sampled HTTP request to any
OpenTelemetry Collector or OTLP-compatible backend (Tempo, Jaeger, Honeycomb,
Datadog, ...) with OTLP/HTTP JSON. The exporter is built in and has no
dependencies.

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `-otlp-endpoint` | `OTEL_EXPORTER_OTLP_ENDPOINT` | empty (off) | Collector base URL; spans are POSTed to `{endpoint}/v1/traces` |
| `-otlp-headers` | `OTEL_EXPORTER_OTLP_HEADERS` | empty | `key=value,...` headers, URL-escaped values (for example an API token) |
| `-trace-sample-ratio` | `GIDEONDB_TRACE_SAMPLE_RATIO` | `1` | Fraction of new root traces kept |

```bash
gideondb -data-path ./data -otlp-endpoint http://127.0.0.1:4318 -trace-sample-ratio 0.1
```

- **Spans:** each span is named `METHOD route-template` (for example
  `POST /v1/collections/{name}/search`) and is a `SERVER` span. It carries
  `http.request.method`, `http.route` and `http.response.status_code`.
  A 5xx response sets the span's status to error. The resource carries
  `service.name=gideondb`, `service.instance.id` (the node ID) and
  `service.version`.
- **Parenting:** an incoming `traceparent` becomes the span's parent. The
  coordinator passes `traceparent` on peer shard search, write, delete and
  replication requests, so a distributed search appears as one trace with a
  span per node.
- **Sampling:** a request with a `traceparent` follows its sampled flag. A new
  root trace is kept when its trace ID falls under the ratio. This is
  OpenTelemetry's TraceIdRatioBased rule, so every node makes the same
  decision. Unsampled requests return `traceparent` flags `00`.
- **Back-pressure:** spans go into a bounded in-memory queue (4,096), are sent in
  batches of 512 every 5 s, and are flushed on shutdown. When the collector
  is slow or down, new spans are dropped and a warning is logged. Requests
  never wait for the exporter.
- **Privacy:** spans hold no collection names, IDs, vectors, metadata or
  bodies, the same as the request log.

[`deployments/observability/otel-collector.yaml`](../../deployments/observability/otel-collector.yaml)
is a minimal Collector pipeline to start from.

## Centralized logs

GideonDB writes one JSON object per line to stdout (`log/slog`). Request
logs include `trace_id` and `span_id`, so a log backend can link straight to
the trace. Ship stdout with your platform's collector.
[`deployments/observability/fluent-bit.conf`](../../deployments/observability/fluent-bit.conf)
is a Fluent Bit example for Kubernetes and Loki. The durable
`operational-events.jsonl` file and `/v1/cluster/logs` remain a bounded
on-node record for the dashboard. They do not replace a log pipeline.

## Dashboards and alerts

[`deployments/observability/`](../../deployments/observability/) contains:

- `grafana-dashboard.json`: an importable Grafana dashboard with panels for
  request rate, 5xx ratio, live vectors, cluster view, latency by route,
  in-flight requests, replication lag and operations, and peer health. It has
  `datasource` and `job` variables.
- `prometheus-rules.yaml`: recording rules plus alerts. The alerts are
  `GideonDBDown`, `GideonDBHighErrorRate` (>5% 5xx),
  `GideonDBSlowSearch` (search p99 > 500 ms), `GideonDBReplicationLag`
  (>1,000 sequences), `GideonDBReplicationFailures`, `GideonDBPeerUnhealthy`
  and `GideonDBClusterViewNotReady`. Tune the thresholds to your workload.
  It is checked with `promtool check rules`.

## Helm

The chart wires all of this with values. The Prometheus Operator CRDs are
needed for the first two:

```yaml
observability:
  serviceMonitor: {enabled: true}      # scrapes /metrics with the chart's API key
  prometheusRule: {enabled: true}      # installs prometheus-rules.yaml
  tracing:
    otlpEndpoint: http://otel-collector.observability:4318
    sampleRatio: 0.1
    headersSecret: {name: otlp-auth, key: headers}
```

## Current boundary

Spans are emitted per HTTP request. There are no child spans yet for WAL,
index or replication work, no gRPC server spans, no baggage, and no OTLP
metrics or logs (metrics are Prometheus pull). gRPC requests do appear in the
audit log and Prometheus metrics.
