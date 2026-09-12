# Pending development

This document is the central checklist for work that is designed or documented
but not yet complete. Keep an item checked only when its implementation,
automated tests, and user-facing documentation are all finished.

## High priority: release blockers

- [x] **Establish a local black-box application E2E suite.** The tagged Go
  suite builds and starts the deployable binary, exercises authenticated REST,
  dashboard login, collection and vector lifecycle, filtered search, bounded
  redacted scrolling, metrics, embedded documentation, graceful restart,
  durable recovery, and deletion. `make test-e2e` runs it without Docker, AWS,
  or external services, and CI executes it as a dedicated job.

- [ ] **Run the Kubernetes gate before release.** The local gate exists at
  `scripts/kubernetes-gate.sh`, but automatic GitHub execution is intentionally
  deferred. Run `make validate-kubernetes` manually when Kubernetes validation
  resumes. It must deploy the three-node StatefulSet, verify readiness and TLS,
  replace the elected leader, and return the cluster to three ready members.
- [x] **Complete the gRPC server transport.** Generated Go bindings and an
  opt-in listener now expose collection/record CRUD, local search, batch search,
  local scroll, cluster/node/shard inspection, health, stats, and checksummed
  local snapshot streaming with bearer authentication, required
  deadlines, 16 MiB message limits, canonical status mapping, TLS support, and
  an interoperability test. Distributed scroll now delegates to the existing
  authoritative placement/fencing coordinator. Placement-aware delete now
  routes to the authoritative leader, persists and replicates an ordered WAL
  tombstone, enforces metadata/leader fencing, and requires quorum
  acknowledgement. The admin-only restore stream validates operation identity,
  offsets, bounded chunks and archive size, SHA-256, and backup structure before
  atomically publishing to an explicitly configured empty destination.
  Cluster-wide snapshots now hold every voter barrier through recovery-point
  capture and node-archive collection, then stream one checksummed package.
  Search and batch
  search now delegate to authoritative shard owners when static routing is
  active and preserve partial failure and epoch metadata. Upsert and batch
  upsert now use the authoritative distributed coordinator and retain
  acknowledgement, per-shard committed/failed/unknown outcomes, and epoch
  metadata. Bounded per-credential gRPC token buckets share the configured rate
  and burst limits and return retry metadata. Reloadable principals enforce
  reader, writer, and admin roles plus collection-prefix isolation. Published v1 field numbers,
  wire types, enum values, RPC types, and streaming shapes are now guarded by
  an additive descriptor compatibility test. Sanitized gRPC outcomes now share
  the bounded durable operational audit log with REST. See
  [the gRPC contract](docs/api/grpc.md).
- [x] **Complete open-source release files and policy.** The repository now
  includes the canonical Apache-2.0 `LICENSE`, contribution and governance
  guides, code of conduct, private security-reporting policy, structured issue
  and pull-request templates, maintainer attribution, and a documented DCO 1.1
  sign-off policy instead of a CLA.
- [x] **Establish the full CI baseline.** The read-only GitHub Actions workflow
  now enforces formatting, vet, pinned Staticcheck, unit, race, uncached
  integration, bounded fuzz-smoke, documentation links, OpenAPI/protobuf and
  generated-binding compatibility, all SDK/integration suites, dashboard
  syntax, and byte-identical trimmed Linux builds. Kubernetes remains an
  explicit manual gate because it requires real Docker, kind, CNI, and storage
  behavior. See [continuous integration](docs/development/continuous-integration.md).

## Storage and indexing

- [x] Persist versioned, checksummed HNSW graph files so startup does not need
  to rebuild graph edges from every stored record. Graph topology is validated
  against the manifest records, collection index settings, entry point, layer
  bounds, neighbor ordinals, and CRC32C before publication.
- [x] Store recovered immutable HNSW adjacency in a contiguous packed neighbor
  array with per-node layer offsets and measured backing-array accounting.
  Mutable graph construction retains editable slices; checkpoint recovery
  publishes the packed representation and rejects mutation.
- [x] Add a per-query HNSW `ef_search` override across REST, distributed shard
  transport, and the Go client, plus exact allowed-set search for selective
  metadata bitmaps.
- [x] Persist versioned, checksummed immutable metadata filter indexes.
  Recovery validates the file's manifest count/LSN, ordinal/value/posting
  consistency, and numeric entries before directly installing the index.
- [x] Evaluate SIMD distance kernels and symmetric int8 vector quantization
  while retaining float32 flat scoring as the correctness oracle. Portable
  four-lane float32 kernels remain the production path; int8 storage is deferred
  because its portable cosine kernel regressed latency despite 0.996 recall@10
  and an approximately 75% vector-payload reduction in the synthetic trial.
- [x] Add reference-counted snapshot pinning and a multi-generation reader API.
  Engine snapshots pin one immutable generation per shard, expose consistent
  get/search/record reads, defer retired mmap closure until the last release,
  and prevent collection deletion while readers remain pinned.
- [x] Add reliable orphan/obsolete segment discovery and cleanup. Startup and
  checkpoint publication now preserve only manifest-referenced segment files,
  remove recognized stale/temp files, preserve unknown operator/future-format
  files, and report cleanup failures.
- [x] Add backward-compatible format-3 manifests for up to 16 ordered immutable
  segment bundles, with safe path, dimension, and increasing-LSN validation.
- [x] Add a deterministic size-tiered compaction planner with four-segment
  fan-in, an eight-segment pressure limit, a 64 MiB input bound, and explicit
  backpressure when no bounded plan exists. A 64-flush unit simulation wrote
  778 units versus 2,080 for full-shard rewrites (0.374 ratio).
- [x] Migrate checkpoint flush/recovery to format-3 delta segments, persist
  tombstones, merge newest versions across segment readers, execute bounded
  plans, and retain format-2 promotion compatibility. A four-by-1,000-by-128
  materialization benchmark measured 6.53–6.66 ms and about 7.46 MB allocated
  for 2.05 MB of raw vectors; that evidence reduced the input cap to 64 MiB.
- [x] Add a direct composite mmap reader for format-3 flat segments. Recovery
  resolves newest record locations and tombstones from record columns, then
  searches surviving vectors in their original mapped segment files without
  copying vector payloads into the Go heap.
- [x] Add a reproducible retained-heap calibration command with optional in-use
  heap profiles. Three isolated Apple M3/Go 1.26.5 runs measured 549.03–549.13
  retained B/vector for flat 50,000 x 128 (512 structural) and 908.07–908.14
  for mutable HNSW 5,000 x 64 (508.63 structural); these remain workload-local
  measurements rather than publishable universal capacity claims.

## API and distributed behavior

- [x] Add durable `Idempotency-Key` handling for placement-aware writes. Each
  shard persists the request digest and prepared record versions before WAL
  append, matching retries return the original result across checkpoint and
  restart, conflicting reuse returns `409`, and coordinator/client propagation
  is covered by recovery and transport tests.
- [x] Require authoritative placement for every public distributed search,
  scroll, and batch-write route. Multi-node authority now requires converged
  fingerprints matching a committed metadata-Raft view; standalone ownership
  remains valid, and the three-node gate verifies owner-only WAL, segment, and
  idempotency state across restart.
- [x] Add opaque cursor pagination with 1–200 item bounds to collection, peer,
  and shard-placement listings. Public APIs now use configurable per-credential
  or direct-address token buckets, return `429` with `Retry-After`, exempt
  health/readiness and internal transport, and cap retained limiter identities
  at 4,096.
- [x] Finalize API and persistent-format compatibility guarantees, migrations,
  and pre-1.0 breaking-change policy. The published compatibility matrix now
  covers REST, cluster transport, WAL, checkpoints, index sidecars, metadata,
  and backups; `gideondb -migrate-data` performs an offline format-1/2 to
  format-3 checkpoint migration, with regression tests for both legacy paths.
- [x] Add sparse and hybrid retrieval. Records persist optional validated sparse
  term weights through WAL, replication, checkpoints, backup, and recovery;
  local and distributed APIs support exact sparse cosine or normalized weighted
  hybrid scoring with metadata/namespace filters. A persistent sparse inverted
  index remains a future optimization rather than a correctness dependency.

## Security and operations

- [x] Improve the administration dashboard UI for production operations,
  including responsive navigation, clearer cluster and shard health, accessible
  loading/error/empty states, safer destructive-action confirmations, and
  operator-focused backup, restore, replication, and alert views. The responsive
  operations shell, mobile navigation drawer, route context, live refresh state,
  accessible landmarks/focus states, and clearer health summary are complete.
  The resilience view now adds derived readiness, peer-health, replication-lag,
  server-error, and authentication alerts plus a truthful cluster-backup
  readiness checklist and recovery runbook. Pinned Playwright desktop and mobile
  projects now cover navigation, responsive drawer behavior, resilience signals,
  tab-scoped API credentials, and exact destructive confirmation against an
  isolated live server. The dashboard preserves its bounded read model.
- [x] Add dashboard login and session authentication. Local development may
  bootstrap with username `admin` and password `admin123`, but these credentials
  must never be compiled into or silently enabled in a production build.
  Production startup must require an operator-supplied secret, store only a
  strong password hash, use secure HTTP-only same-site cookies, enforce CSRF and
  login throttling, rotate sessions, record sanitized audit events, and require
  the bootstrap password to be changed before administrative access is granted.
  The first implementation slice is complete: the dashboard now has a dedicated
  sign-in screen, opaque eight-hour server-side sessions, HTTP-only strict
  same-site cookies, CSRF checks on every cookie-authenticated mutation, logout
  invalidation, per-client login throttling, and sanitized request auditing.
  The `admin` / `admin123` bootstrap is now supplied only by the explicit
  `scripts/run-local-dev.sh` workflow and is absent from the compiled server;
  new data directories require explicit dashboard credentials. Persistent owner-only credentials now
  contain only a 600,000-iteration salted PBKDF2-SHA-256 verifier and are
  excluded from backup archives. Bootstrap sessions cannot use administrative
  APIs until a new 12-character-or-longer password is persisted; replacement
  invalidates every prior session and rotates the active cookie and CSRF token.
  Production bootstrap now also accepts an owner-only mounted secret through
  `GIDEONDB_DASHBOARD_PASSWORD_FILE`, suitable for container secret-manager
  integrations without exposing the password in process arguments. Active
  dashboard sessions renew every 15 minutes by atomically rotating both the
  opaque cookie and CSRF token. Dedicated Go and browser regressions now cover
  bootstrap replacement, stale-session rejection, and periodic renewal.
- [x] Support API-key rotation without restarting and multiple principals with
  role-based authorization and tenant isolation. A secure reloadable principals
  file provides reader, writer, and admin roles; non-admin collection-prefix
  scopes filter listings and deny cross-tenant access, invalid rotations fail
  closed, and the legacy process/cluster key remains backward compatible.
- [x] Add configurable durable audit retention and per-credential/client rate
  limits. Sanitized audit events retain 256–1,000,000 entries (4,096 by
  default), recover across restart, and compact atomically; bounded token
  buckets expose configurable rate and burst controls.
- [x] Add live certificate/CA reload and distinct inbound server versus
  outbound node-client TLS identities. New handshakes reload atomically replaced
  server certificates, client certificates, keys, and CA roots; separate node
  client flags fall back to the server pair for backward compatibility, and
  cryptographic regression tests exercise certificate and issuer rotation.
- [x] Document and automate certificate lifecycle and secret rotation for
  supported deployment targets. The Kubernetes helper validates certificate
  expiry, issuer trust, and key matching before atomically updating the
  projected TLS Secret. The security runbook documents leaf rotation,
  three-stage CA overlap, per-pod verification, rollback, container-mounted
  equivalents, and the separate bearer-principal rotation path.
- [ ] Validate Kubernetes behavior on the intended production storage class,
  ingress/load balancer, CNI NetworkPolicy implementation, and failure domains.
  Execute and retain evidence through the staged
  [production deployment plan](docs/operations/production-deployment-plan.md).
- [ ] Run repeatable workload-specific soak, backup/restore, rolling-upgrade,
  partition, repair, rebalance, and disaster-recovery drills before recommending
  production use.
- [ ] Add durable external observability guidance or integrations for metrics,
  traces, and centralized logs; the embedded dashboard is only a bounded
  operational aid.

## Maintenance rule

When a new limitation or deferred feature is documented elsewhere, add it here
in the same change. When completing an item, link the implementation and tests
in the commit or pull request, update the relevant design/operations document,
and then mark or remove the checklist entry.
