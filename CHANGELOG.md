# Changelog

This project follows semantic versioning once releases begin. Current entries
describe unreleased experimental work.

## Unreleased

### Added

- A versioned, fully documented `gideondb.v1` public protobuf contract covering
  18 collection, record, search, cluster, health, statistics, snapshot, and
  restore RPCs; Buf v2 lint/generation policy and a dependency-free structural
  contract gate are included ahead of Go server wiring.

- An embedded, responsive administration dashboard with node and cluster
  overview, collection creation and confirmation-gated deletion, a redacted
  vector-search playground, membership and placement views, session-scoped API
  credentials, safe DOM rendering, a strict same-origin content policy, and
  Prometheus-backed HTTP traffic, latency, error, in-flight, replica lag, and
  replication-operation visibility. The Data Explorer adds bounded node-local
  cursor browsing, exact-ID lookup, insertion, confirmation-gated deletion,
  namespace selection, and explicit vector reveal. A read-only Configuration
  section displays non-secret security, routing, replication, capacity,
  protocol, and collection/index settings. A severity-filtered Logs view shows
  recent sanitized node-local HTTP events with trace correlation.
- An authenticated bounded in-memory operational-event API retaining only
  route templates, status, duration, severity, timestamps, and trace/span IDs.
- A deterministic node-local record-scroll API with opaque cursors, namespace
  filtering, a hard 200-record page limit, and vectors redacted by default.
- Node-local cursor scrolling across the Go, Python, TypeScript, Java, Rust, and .NET SDKs,
  including page-limit validation and explicit vector inclusion.
- A provider-neutral Python semantic ingestion/search helper, dependency-free
  Ollama batch embedding adapter, and runnable local semantic-search example
  with bounded and strictly validated embedding responses.
- A dependency-free Hugging Face feature-extraction adapter and runnable hosted
  semantic-search example with HTTPS-only endpoints, Bearer authentication,
  and strict sentence-level embedding validation.
- A dependency-free OpenAI-compatible embeddings adapter and runnable semantic-
  search example with authenticated custom endpoints, response index ordering,
  and strict batch and dimension validation.
- A dependency-free Cohere v2 embedding adapter with distinct document/query
  retrieval modes, documented batch limits, and a runnable semantic-search
  example; the provider-neutral store now honors specialized embedding modes.
- An optional, lazily loaded LangChain `VectorStore` bridge with text ingestion,
  similarity search, scores, metadata filters, namespaces, and ID deletion.
- An optional LlamaIndex vector-store bridge with embedded-node ingestion,
  dense queries, exact-match filters, namespaces, sync/async operations, and
  session-aware reference-document deletion.
- A staged adoption guide with measurable gates from workload qualification
  through local evaluation, production hardening, distributed fault drills,
  lifecycle rehearsal, and risk-controlled rollout; the root project status
  now reflects the implemented system and remaining pre-alpha gaps.
- Placement-aware cluster-wide record scrolling with deterministic global
  ordering, one owner per shard, bounded pages, vector redaction, fail-closed
  shard errors, and namespace- plus metadata-epoch-fenced opaque cursors.
- Cluster-wide scrolling support in the Go, Python, TypeScript, Java, Rust, and .NET SDKs,
  reusing each SDK's bounded scroll options and exposing cluster epoch and
  authoritative-placement response metadata.
- Bounded dashboard time-series charts for request rate, interval mean latency,
  and server-error rate, with five-second visible-page sampling, pause/resume,
  counter-reset handling, and a 120-sample memory ceiling.
- Bounded durable sanitized operational events using mode-`0600` append-only
  JSON lines with restart recovery and atomic compaction, plus authenticated,
  epoch-fenced cluster log aggregation with explicit per-node failures.
- An experimental dependency-free Go SDK with context-aware authenticated
  requests, bounded responses, typed API errors, collection and vector CRUD,
  batches, filtered search, and placement-aware distributed search/writes.
- An experimental standard-library Python SDK with configurable TLS context and
  timeout, bounded responses, typed API/transport errors, namespaced CRUD,
  filtered search, batches, and placement-aware distributed operations.
- An experimental dependency-free TypeScript SDK for Node.js 20+ and modern
  browsers with abort-aware timeouts, bounded streaming responses, typed
  API/transport errors, namespaced CRUD, search, batches, and distributed
  operations.
- An experimental dependency-free Java 17 SDK using the JDK HTTP client with
  strict origin and timeout handling, bounded responses, typed API/transport
  errors, namespaced CRUD, search, batches, and distributed operations.
- An experimental synchronous Rust SDK using Rustls-backed HTTP, whole-call
  timeouts, bounded responses, typed failures, an injectable transport, and the
  same lifecycle, vector, search, scroll, and distributed-operation coverage.
- An experimental dependency-free .NET 8 SDK with cancellation-aware
  asynchronous operations, bounded streaming responses, typed failures,
  injectable HTTP transport, and equivalent lifecycle, vector, search, scroll,
  and distributed-operation coverage.
- A hardened three-node Kubernetes StatefulSet base with stable per-node PVCs,
  headless and readiness-gated Services, credential and private-CA TLS Secrets,
  quorum-preserving disruption policy, host anti-affinity, restricted ingress,
  non-root execution, bounded startup recovery, and an operator runbook.
- Private-CA mutual TLS for node discovery and every internal Raft, replication,
  repair, rebalance, and backup route, while retaining detail-free HTTPS probes.
- Graceful distributed shutdown with immediate readiness removal, new-traffic
  rejection, deterministic caught-up-voter selection, term-fenced Raft
  leadership transfer, and bounded in-flight HTTP draining.
- A disposable kind-based Kubernetes deployment gate that builds the current
  image, generates short-lived private-CA credentials, verifies three-node
  convergence and quorum disruption state, terminates the elected leader, and
  requires replacement leadership plus full pod readiness recovery.
- A least-privilege Kubernetes gate workflow with pinned kind tooling,
  path-scoped triggers, per-ref concurrency, a bounded runtime, automatic
  cleanup, and retained cluster diagnostics for failed runs.
- A detail-free unauthenticated readiness probe that verifies converged static
  placement and activates assigned shard ownership before admitting traffic,
  plus shared-seed self-pruning for stable StatefulSet DNS lists.
- A repeated Raft partition/heal chaos gate covering isolated-leader rejection,
  majority progress, conflicting uncommitted-log repair, and monotonic epoch
  convergence, plus an operator partition-behavior runbook.
- Cluster protocol-range advertisement and negotiation with legacy-v1 fallback,
  incompatible-peer readiness fencing, negotiated-version introspection, and a
  one-voter-at-a-time rolling-upgrade runbook.
- Canonical cluster recovery-point manifests with committed epoch, placement,
  capacity, node, and per-owned-shard WAL sequence fences, plus strict
  cross-node view and duplicate-report validation.
- Authenticated, epoch-fenced coordinated-backup freeze/capture/release
  primitives that drain in-flight mutations, reject new writes, and enforce
  exact operation ownership for capture and release.
- Leader-coordinated backup barrier fanout with readiness gating, authenticated
  recovery-point collection, canonical cluster manifest assembly, and deferred
  release of every successfully frozen node.
- Recovery-point-bound node archives with exact pre-write sequence validation,
  authenticated temporary-file streaming, checksummed embedded metadata, stale
  fence rejection, and restored provenance preservation.
- Canonical multi-node backup packages with exact node-set enforcement, nested
  checksums, staged per-node restore, embedded/outer manifest comparison, and
  atomic publication only after complete verification.
- Deterministic cluster restore planning that chooses the highest-sequence
  replica with stable tie-breaking, validates complete shard coverage, and maps
  sources onto new leader/replica identities without cloning old node IDs.
- Atomic physical cluster-restore materialization that revalidates backup view
  provenance and exact shard sequences, installs selected snapshots into fresh
  remapped replica paths, activates target-only ownership, supports empty
  sequence-zero shards, and leaves no partial destination after failure.
- Deterministic capacity-weighted rendezvous placement primitives with bounded
  integer resource weights and backward-compatible unit-capacity defaults.
- End-to-end placement-capacity configuration and advertisement, including
  convergence fencing and weighted routing, replication, repair, and rebalance
  planning.
- Deterministic same-membership capacity-change plans that reuse the durable
  rebalance action graph and reject invalid, unknown-node, and no-op changes.
- Quorum-backed same-voter Raft view transitions for metadata-only placement
  epoch changes without unnecessary joint consensus.
- Canonical capacity manifests in committed Raft views, including validation,
  snapshot/restart durability, node introspection, and committed-value recovery
  at server startup.
- Authenticated capacity-change plan, prepare, apply, and abort handling using
  durable migration journals, same-voter Raft commits, and immediate committed
  capacity overrides for local and discovered nodes.
- Capacity-transition recovery gates covering injected target failure, durable
  coordinator resume without duplicate copying, committed-epoch cleanup, and
  stale-configuration server restart.
- Bounded per-replica exponential backoff for automatic replica repair, with
  immediate reset after successful health checks or snapshot recovery.
- Bounded static peer discovery with authenticated identity polling, duplicate
  node-ID reconciliation, last-seen health state, membership inspection, and a
  Prometheus peer-health gauge.
- Thresholded peer health states with transient-failure suspicion, three-check
  unhealthy detection, two-check recovery, transition timestamps, saturated
  counters, and state metrics.
- Durable cluster identity and bootstrap metadata epoch, explicit shared-ID
  configuration, peer cluster-boundary enforcement, and backup exclusion.
- Deterministic epoch-tagged logical-shard placement previews using rendezvous
  hashing, stable membership during transient health failures, and explicit
  non-authoritative routing semantics.
- Strict cluster/node/epoch fencing for authenticated single-shard search
  transport, with typed stale/future epoch failures and current-epoch response
  headers.
- Experimental distributed search coordination with bounded local/remote
  fanout, propagated fencing and tracing, strict peer-response validation,
  deterministic bounded-memory global top-K, and explicit partial-failure mode.
- Experimental distributed batch writes with preflight validation, per-shard
  WAL commits, bounded fenced fanout, and explicit committed/failed/unknown
  outcomes without unsafe automatic retries.
- Canonical membership, catalog, and placement fingerprints with peer-view
  convergence diagnostics and a fail-closed cluster-readiness gauge.
- Opt-in immutable static routing with convergence-gated coordinators,
  authoritative placement status, transport-level shard-owner enforcement,
  immutable schema, and blocked single-node routing bypasses.
- Fail-safe node-local shard ownership manifests that remove empty unowned WAL
  and segment structures, reject stray unowned data, and survive restart.
- Durable metadata-Raft safety core with persisted terms and votes, log
  consistency checks, committed-entry protection, majority-gated current-term
  commits, and strictly monotonic epoch application.
- Authenticated, cluster-fenced metadata-Raft vote and append RPCs with bounded
  batches, three-node transport validation, and live committed-epoch serving.
- Autonomous metadata-Raft runtime with randomized elections, majority leader
  selection, heartbeats, bounded follower catch-up, higher-term step-down, and
  node-info role diagnostics.
- Leader-only compare-and-advance metadata proposals with canonical server-side
  digests, bounded incremental replication, conflict backtracking, quorum
  commit propagation, and fail-closed configured-voter discovery.
- Immutable, sorted, durable Raft voter membership with exact discovery-view
  matching, non-voter RPC rejection, restart validation, and node diagnostics.
- Durable committed-view digest tuples bound to each applied metadata epoch,
  with conflict rejection and fail-closed authoritative placement readiness.
- Quorum-contact tracking that demotes isolated metadata leaders, with a
  deterministic majority/minority partition gate proving only the majority can
  commit a new epoch.
- Absolute-index Raft logs with durable committed-prefix snapshot boundaries,
  restart validation, and correct post-compaction index continuation.
- Thresholded automatic metadata-log compaction plus authenticated snapshot
  installation for lagging followers, including membership, digest, term,
  freshness, restart, and suffix-retention safety checks.
- Completed the fixed-topology Phase 6 cluster gate; joint voter changes move
  to the Phase 8 node join/leave workflow.
- Began Phase 7 with deterministic distinct replica-set placement, stable
  initial leaders, replication-factor validation, and a non-authoritative
  inspection endpoint.
- Added ordered follower shard-WAL append for leader-prepared batches with
  version preservation, exact sequence enforcement, retained-payload duplicate
  detection, gap/compaction errors, restart reconstruction, and fenced internal
  replica transport.
- Activated configurable static replica ownership for leaders and followers,
  with factor-one compatibility, node advertisement, and readiness fencing for
  cross-node replication-factor mismatches.
- Added ordered leader-to-follower batch fanout with control-plane term fences,
  majority acknowledgement, replica counts in write outcomes, and explicit
  ambiguous results when the leader is durable but quorum is unavailable.
- Added automatic follower snapshot catch-up for missing or compacted WAL
  sequences, including durable shard replacement, stale-snapshot rejection,
  sequence continuation, and recovery across restart.
- Added replication operation counters and per-follower WAL sequence-lag
  gauges, with concurrent same-shard validation proving post-recovery record
  and version convergence.
- Completed Phase 7 with propagated `leader`, `quorum`, and `all` write
  acknowledgement policies, quorum-by-default validation, and ordered
  background fanout after an acknowledgement threshold is reached.
- Began Phase 8 with deterministic next-epoch join/leave movement planning,
  dependency-ordered snapshot, catch-up, leadership, cutover, and removal
  actions, plus a non-mutating REST inspection endpoint.
- Added the durable joint-consensus membership state machine with old/new
  majority enforcement, one-node transition validation, restart recovery,
  compaction fencing, and removed-voter campaign prevention.
- Wired joint consensus into the live Raft runtime with persisted active-peer
  selection, identity-based election and heartbeat quorums, learner catch-up,
  two-stage membership replication, and removed-leader step-down.
- Added an authenticated, leader-only rebalance apply endpoint with an epoch
  precondition, server-derived target placement and view digests, discovered
  learner admission, and routing isolation for non-voters.
- Added a durable rebalance prerequisite executor that runs only snapshot-copy
  and WAL-catch-up actions, checkpoints each success, resumes without repeating
  completed work, rejects plan replacement, and fences journal cleanup on the
  committed target epoch.
- Added authenticated learner snapshot staging with cluster, node, epoch,
  source, and Raft-term fences. Receivers independently recompute the proposed
  join placement before accepting a shard image; catch-up can idempotently
  resend the latest complete image.
- Added durable per-shard rebalance write barriers. Final catch-up serializes
  with replication, persists the plan and target epoch before export, survives
  restart, rejects conflicting transitions, blocks further shard writes, and
  releases only after the target epoch commits.
- Wired the durable preparation journal to a leader-only operator endpoint,
  dispatching copy and catch-up work to each actual source leader. Membership
  apply now requires the exact plan to be prepared, releases source barriers
  after commit, and lets lagging sources self-release after observing the
  committed target epoch.
- Added leader-only rebalance abort with epoch, term, and joint-consensus
  fences. Abort releases only exact-plan barriers across source nodes and
  retains the durable journal after partial failure for safe retry.
- Added a multi-node rebalance failure gate covering remote-source dispatch,
  injected learner outage, durable coordinator resume, staged shard-data
  verification, persisted source barriers, and distributed abort cleanup.
- Added a real-Raft operator join gate that elects from the old voter set,
  prepares through REST, applies through joint consensus, verifies target epoch
  and voter convergence on every node, and confirms journal cleanup.
- Added the symmetric real-Raft leave gate, verifying authenticated
  prepare/apply, joint-consensus convergence on surviving voters, finalized
  state delivery to the removed node, campaign rejection, and journal cleanup.
- Extended the leave gate through the data plane with a real departing-node
  shard record, surviving-target staging verification, durable source fencing,
  post-commit release, and observer planning for actions executed by the node
  being removed.
- Added injected leave-transfer recovery coverage: target outage interrupts the
  plan, the coordinator executor is reopened from its durable journal, retry
  resumes safely, and the subsequent real-Raft cutover preserves data and
  releases the departing source barrier.
- Added automatic replica reconciliation with fenced follower-sequence probes,
  snapshot repair for lagging assigned replicas, production background
  scheduling, and repair/lag metrics. Healthy replicas avoid snapshot record
  materialization.
- Deterministic three-node functional validation covering partitioned record
  ownership, physical WAL pruning, fenced distributed writes, global search,
  and restart recovery.
- Persistent cryptographic node identities with atomic first-writer creation,
  authenticated node-info discovery, advertised addresses, and backup exclusion
  to prevent cloned identities after restore.
- Prometheus-compatible HTTP request counters, latency histograms, in-flight
  tracking, and collection/vector gauges at `GET /metrics`, using bounded route
  template labels.
- W3C Trace Context propagation with fresh server span IDs, response
  `traceparent` headers, and structured request-completion span logs.
- Permission-checked bearer-key files, constant-time authentication, TLS server
  support, secure non-loopback startup guards, and conservative HTTP headers.
- Crash-consistent versioned backup archives with per-file SHA-256 checksums,
  atomic publication, staged restore validation, and backup/restore CLI modes.
- Layered configuration with strict JSON decoding, validated environment
  variables, explicit CLI precedence, and secret-file references.
- Non-root multi-stage Docker image plus version, healthcheck, configuration
  validation, data verification, backup, and restore CLI modes.
- Reproducible single-node ingest/checkpoint/recovery/concurrent-query validation
  harness with JSON throughput, latency, heap, platform, and correctness output.
- Passing durable 1M and 2M vector single-node scale gates with recorded ingest,
  checkpoint, recovery, query-latency, heap, and correctness results.

- Comprehensive technical design and eight proposed architecture decisions.
- Go server with collection, logical-shard and mutable-segment ownership.
- Portable cosine, dot-product and squared-L2 scoring.
- Exact flat index with contiguous row-major vector storage.
- Experimental HNSW index with configurable `M`, `efConstruction` and
  `efSearch`, deterministic levels, tombstone deletion and index statistics.
- Atomic JSON collection catalogs plus per-shard checksummed WALs with `always`
  and explicitly lossy `async` sync modes.
- WAL recovery with torn-tail truncation, LSN validation, CRC32C corruption
  detection, routed replay, and fail-closed startup.
- Immutable per-shard checkpoint segments, atomic manifests, manifest-LSN WAL
  replay floors, automatic mutation thresholds, WAL reset after manifest
  durability, and full-shard live-record compaction.
- Indexed top-level scalar metadata filtering with boolean set algebra,
  equality/membership postings, numeric/string ranges, existence predicates,
  and filter-aware flat/HNSW search.
- Validated batch upsert of up to 10,000 records, grouped into one WAL record
  per logical shard with explicitly documented cross-shard partial semantics.
- Dense ordinal bitmaps for filter set algebra and equality postings, plus
  sorted numeric range indexes with benchmarked allocation reductions.
- Adaptive singleton/sparse/dense equality postings and numeric immutable-base/
  mutable-delta merges to avoid high-cardinality quadratic memory behavior.
- Manifest format 2 with independently checksummed record and fixed-width
  little-endian float32 vector columns, plus backward-compatible format-1
  recovery and finite-vector validation.
- Read-only validated mmap vector sources, immutable mapped flat search,
  separate mapped-memory accounting, guarded zero-copy little-endian float32
  views, portable decode fallback, and unsupported-platform build fallback.
- Live mmap checkpoint bases for flat collections, merged with WAL-backed
  mutable deltas and tombstone/shadow sets across reads, search and compaction.
- Bounded parallel logical-shard search with deterministic heap-based global
  top-K merging and reproducible single-worker/parallel benchmarks.
- Four-lane portable dot-product, squared-L2 and cosine accumulation kernels,
  improving instruction-level parallelism while retaining scalar tail handling
  and zero hot-path allocations.
- Prepared cosine queries that compute the query norm once per heap or mmap
  scan and reuse it across all candidate vectors.
- Typed flat-search heap and final-ordering primitives that reduce top-10 query
  allocations from 15 to 1 and allocated bytes from 432 to 160.
- Typed HNSW construction heaps and generation-marked visited ordinals, cutting
  repeated hash-map and interface allocation during graph builds.
- Concurrently safe, size-capped HNSW visited-set reuse for lower search
  latency and GC pressure without retaining unusually large query scratch maps.
- Prepared cosine state propagated through HNSW greedy descent, layer search,
  graph construction, and neighbor pruning.
- Typed cross-shard top-K merging and allocation-free unfiltered metadata
  admission, completing the Phase 4 query-scheduling and GC-pressure pass.
- Versioned experimental REST API and OpenAPI description.
- Correctness, persistence, REST, recall, concurrency, race and benchmark
  coverage.

### Known limitations

- Catalog, WAL and segment formats remain experimental; the checkpoint payload
  is JSON and indexes rebuild during startup.
- HNSW graph adjacency is not yet packed or persisted directly.
- gRPC and distributed execution are not implemented; HNSW checkpoints still
  rebuild their graph in memory during recovery.
