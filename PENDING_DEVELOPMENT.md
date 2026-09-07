# Pending development

This document is the central checklist for work that is designed or documented
but not yet complete. Keep an item checked only when its implementation,
automated tests, and user-facing documentation are all finished.

## High priority: release blockers

- [ ] **Run the Kubernetes gate before release.** The local gate exists at
  `scripts/kubernetes-gate.sh`, but automatic GitHub execution is intentionally
  deferred. Run `make validate-kubernetes` manually when Kubernetes validation
  resumes. It must deploy the three-node StatefulSet, verify readiness and TLS,
  replace the elected leader, and return the cluster to three ready members.
- [ ] **Implement the gRPC server transport.** Generate the Go bindings from
  `api/proto/gideondb/v1/gideondb.proto`, expose the documented RPCs, map REST
  error semantics to gRPC status codes, enforce authentication/deadlines/stream
  limits, and add interoperability and compatibility tests. REST is currently
  the only active public transport. See [the gRPC contract](docs/api/grpc.md).
- [ ] **Complete open-source release files and policy.** Add the final
  `LICENSE`, contribution guide, code of conduct, security policy, issue and PR
  templates, and select/document DCO or CLA handling. The README and package
  metadata declare Apache-2.0; add the canonical repository license text and
  maintainer attribution before the first public release.
- [ ] **Establish the full CI baseline.** Add appropriately scoped formatting,
  vet/lint, unit, race, integration, fuzz-smoke, documentation/link,
  OpenAPI/protobuf compatibility, and reproducible-build checks before a public
  release. Kubernetes CI may be restored as a separate, manually triggered or
  path-filtered job when it becomes useful.

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

- [x] Support API-key rotation without restarting and multiple principals with
  role-based authorization and tenant isolation. A secure reloadable principals
  file provides reader, writer, and admin roles; non-admin collection-prefix
  scopes filter listings and deny cross-tenant access, invalid rotations fail
  closed, and the legacy process/cluster key remains backward compatible.
- [ ] Add audit retention, configurable rate limits, certificate reload, and
  distinct client/server node identities.
- [ ] Document and automate certificate lifecycle and secret rotation for
  supported deployment targets.
- [ ] Validate Kubernetes behavior on the intended production storage class,
  ingress/load balancer, CNI NetworkPolicy implementation, and failure domains.
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
