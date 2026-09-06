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
  `api/proto/vectordb/v1/vectordb.proto`, expose the documented RPCs, map REST
  error semantics to gRPC status codes, enforce authentication/deadlines/stream
  limits, and add interoperability and compatibility tests. REST is currently
  the only active public transport. See [the gRPC contract](docs/api/grpc.md).
- [ ] **Complete open-source release files and policy.** Add the final
  `LICENSE`, contribution guide, code of conduct, security policy, issue and PR
  templates, and select/document DCO or CLA handling. The license currently
  awaits maintainer identity and copyright confirmation.
- [ ] **Establish the full CI baseline.** Add appropriately scoped formatting,
  vet/lint, unit, race, integration, fuzz-smoke, documentation/link,
  OpenAPI/protobuf compatibility, and reproducible-build checks before a public
  release. Kubernetes CI may be restored as a separate, manually triggered or
  path-filtered job when it becomes useful.

## Storage and indexing

- [ ] Persist versioned, checksummed HNSW graph files so startup does not need
  to rebuild graph edges from every stored record.
- [ ] Rebuild persisted HNSW graphs during compaction and replace slice-based
  adjacency with a measured packed immutable representation.
- [ ] Add a per-query HNSW `efSearch` override and selectivity-aware filtered
  traversal backed by persisted filter indexes.
- [ ] Evaluate SIMD distance kernels and vector quantization while retaining
  flat search as the correctness oracle.
- [ ] Add general snapshot pinning and a multi-generation reader API.
- [ ] Add reliable orphan/obsolete segment discovery and cleanup.
- [ ] Replace full-shard checkpoint compaction with a bounded multi-segment,
  size-tiered policy after measuring write amplification and memory use.
- [ ] Calibrate index memory accounting against heap profiles before publishing
  bytes-per-vector claims.

## API and distributed behavior

- [ ] Add durable idempotency keys for writes so ambiguous distributed outcomes
  can be retried safely.
- [ ] Finish consensus-backed authoritative placement and fully separated
  physical shard ownership for every public distributed route.
- [ ] Add any still-missing pagination and server-side rate limiting to public
  operational APIs.
- [ ] Finalize API and persistent-format compatibility guarantees, migrations,
  and pre-1.0 breaking-change policy.
- [ ] Add sparse and hybrid retrieval if adopted by the release scope; the
  current engine supports dense float32 vectors only.

## Security and operations

- [ ] Support API-key rotation without restarting and multiple principals with
  role-based authorization and tenant isolation.
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
