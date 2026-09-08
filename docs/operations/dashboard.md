# Administration dashboard

The console uses a responsive operations shell with grouped workspace and
operations navigation, a mobile drawer, visible connection and refresh state,
route context, keyboard focus indicators, reduced-motion support, and a skip
link. Its self-contained presentation follows AdminLTE 4 application-shell
conventions: a compact top navbar, graphite sidebar, Bootstrap-like type scale,
status widgets, and restrained card containers without requiring CDN assets or
an additional browser runtime. Health summary cards distinguish catalog, record, membership, and
placement-epoch signals while keeping the existing bounded API read model.

The Resilience workspace derives active signals from the already bounded
readiness, membership, replication-lag, request-error, and authentication
posture responses. Its backup panel is a preflight and runbook—not a backup
success indicator. It reports whether cluster snapshot prerequisites appear
ready and directs administrators to the coordinated gRPC snapshot and offline
restore workflow.

Browser regressions run against an isolated live GideonDB process in pinned
desktop and mobile Chromium projects. The suite covers route context,
responsive drawer state, resilience signals, tab-scoped API-key behavior, and
typed confirmation that prevents accidental collection deletion. Run it with
`make test-dashboard` after installing the locked dashboard dependencies.

GideonDB embeds an experimental administration dashboard in the server binary.
Open `/dashboard/` on a running node; for the default development address this
is `http://127.0.0.1:6333/dashboard/`.

The dashboard has its own administrator login, separate from bearer credentials
used by API clients. For an explicit local-only bootstrap, run
`./run-gideondb.sh` on macOS/Linux or `run-gideondb.bat` on Windows; they
provision `admin` / `admin123` outside the
compiled server and requires that password to be replaced before any
administrative API can be used. The replacement must contain at least 12
characters. New data directories otherwise require both
`GIDEONDB_DASHBOARD_USERNAME` and `GIDEONDB_DASHBOARD_PASSWORD` when the data
directory has no existing dashboard credential.

Custom local bootstrap credentials can set the same username/password variables
plus `GIDEONDB_DASHBOARD_BOOTSTRAP=true`. Bootstrap mode is rejected on a
non-loopback listener. Production binaries contain no default dashboard
credential and never silently create one.

For container and secret-manager deployments, set
`GIDEONDB_DASHBOARD_PASSWORD_FILE` to an owner-readable (`0600`) mounted secret
instead of placing the password directly in the environment. Configure only one
password source. The mounted secret is consumed only while creating the
persistent verifier; subsequent starts use that verifier.

Only a salted PBKDF2-SHA-256 verifier is persisted in
`dashboard-credentials.json`; the file is owner-only and is deliberately
excluded from data backup archives. Password replacement invalidates all old
sessions and issues a new opaque session and CSRF token. Dashboard sessions use
HTTP-only strict same-site cookies, and TLS listeners also mark them secure.
An active console renews and rotates its opaque session cookie and CSRF token
every 15 minutes without extending beyond the server's normal authentication
checks.

The current dashboard provides:

- a persistent dark/light appearance switch shared by login and console views;
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
