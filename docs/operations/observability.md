# Observability

## Metrics

`GET /metrics` exposes Prometheus text-format counters, latency histograms, an
in-flight request gauge, collection count, and live vectors by collection.
Request labels use bounded HTTP route templates. The endpoint shares the REST
listener and currently has no independent authentication.

Replication adds `vectordb_replication_operations_total`, labeled only by the
bounded `append|snapshot` operation and `success|failure` result, plus
`vectordb_replication_lag_sequences`. The lag gauge reports the difference
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
bounded JSON-lines file in the data directory. The latest 4,096 entries survive
restart; compaction is atomic. `/v1/cluster/logs` uses authenticated and
metadata-epoch-fenced peer requests to merge recent entries and reports
unavailable nodes explicitly.

## Current boundary

This is a dependency-free tracing foundation, not a complete OpenTelemetry
exporter. There is no sampling configuration, OTLP export, child spans for WAL
or index work, baggage propagation, or cross-node propagation yet. Those can be
added behind the stable request context without changing the REST contract.
