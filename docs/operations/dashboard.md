# Administration dashboard

VectorDB embeds an experimental administration dashboard in the server binary.
Open `/dashboard/` on a running node; for the default development address this
is `http://127.0.0.1:6333/dashboard/`.

The current dashboard provides:

- node health, runtime identity, leadership, and catalog totals;
- collection creation, inspection, and confirmation-gated deletion;
- cursor-based, node-local record browsing, exact-ID lookup, record insertion,
  and confirmation-gated deletion, with optional explicit vector reveal;
- manual vector search with namespaces and metadata filters;
- cluster membership, readiness, and shard-placement views.
- cumulative HTTP request volume, average latency, in-flight work, and server
  errors by bounded route template;
- a bounded live time series for request rate, interval mean latency, and
  server-error rate, sampled every five seconds while the page is visible;
- follower WAL-sequence lag and replication transport outcomes.
- read-only effective security, routing, replication, capacity, protocol, and
  per-collection index configuration.
- severity-filtered recent HTTP operational events with status, latency, and
  trace correlation identifiers.

If API-key authentication is enabled, select **API key** and enter the same
Bearer credential used by API clients. The credential is retained only in the
current browser tab through `sessionStorage`. It is sent only to the dashboard's
same origin and is never included in URLs.

Search results display IDs, scores, metadata, and payloads. Stored and query
vectors remain redacted. Dynamic values are inserted with text-only DOM APIs,
and the server applies a content security policy that permits scripts, styles,
and API connections only from the node's own origin. The dashboard calls only
public, versioned `/v1` APIs and therefore cannot exceed the permissions of its
supplied API identity.

The Data Explorer is intentionally node-local in distributed mode. Its browse
view reflects records physically owned by the connected node, not a
cluster-wide merged scroll. Operators must explicitly select **Reveal vectors**
before stored vector values are requested or rendered.

The performance view parses the node's authenticated Prometheus exposition in
the browser. It retains at most 120 samples—about ten minutes at the five-second
interval—in page memory and can pause/resume sampling. Counter resets are
treated as a new baseline. This history disappears on reload, covers only the
connected node, and reports interval mean rather than percentile latency.
Prometheus remains the metrics system of record for durable history, alerting,
percentiles, and cross-node aggregation.

The Logs view is a bounded troubleshooting aid and currently reads the
connected node's feed. Production nodes persist the latest 4,096 sanitized HTTP
completion events under their data directory, and `/v1/cluster/logs` provides a
bounded cross-node merge with explicit peer failures. Centralized structured
log collection remains appropriate for longer retention, indexing, alerting,
and analysis outside the active cluster.
