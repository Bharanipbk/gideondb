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
- follower WAL-sequence lag and replication transport outcomes.
- read-only effective security, routing, replication, capacity, protocol, and
  per-collection index configuration.

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
the browser. Values are cumulative snapshots rather than a retained time
series; Prometheus remains the metrics system of record for alerting, history,
and cross-node aggregation.
