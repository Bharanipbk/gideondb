# Roadmap

Each phase has a correctness gate. Dates and throughput targets are omitted
until maintainers have measured the implementation.

1. **Core foundations — complete**: types, distance kernels, flat index,
   collection/shard/segment ownership, REST, tests and format documentation.
2. **HNSW — complete**: deterministic construction, concurrent reads,
   inserts/deletes, recovery, accounting and recall benchmarks.
3. **Production storage — complete**: WAL, recovery, immutable segments,
   compaction, snapshots, filters, batches and checksums.
4. **Performance — complete**: mmap, parallel shard search, query merging,
   portable kernels, GC reduction and construction optimization.
5. **Single-node production readiness — complete**: metrics, tracing, security,
   backup/restore, layered configuration, Docker, operational CLI and a passing
   2M-by-128 durable checkpoint/recovery/correctness gate.
6. **Basic distributed cluster — complete**: identity, static discovery,
   cluster identity/bootstrap metadata, thresholded observational health, and deterministic
   placement planning plus fenced internal shard reads are complete;
   experimental distributed search and write coordination are complete.
   Deterministic cluster-view convergence checks are also complete.
   Opt-in immutable static placement activation and physical WAL/segment
   ownership pruning are complete. The durable metadata-Raft term, vote, log,
   quorum-commit, and monotonic-epoch state machine is complete; authenticated
   Raft RPCs, live committed-epoch reads, autonomous elections, heartbeats,
   follower catch-up, higher-term step-down, leader-only epoch proposals,
   incremental replication, quorum commit propagation, and fail-closed voter
   discovery, immutable persisted voter membership, and authoritative
   committed-view validation are complete. Quorum-contact leader demotion and
   deterministic majority/minority partition validation are also complete.
   Durable absolute-index snapshots, thresholded automatic compaction, and
   authenticated lagging-follower snapshot installation are complete.
   Joint-consensus membership changes are deferred to Phase 8 node join/leave.
   A deterministic three-node data-plane gate passes partitioned writes,
   distributed search, physical ownership checks, and owner recovery.
7. **Replication — complete**: deterministic distinct replica-set placement
   and initial leader selection are complete. Ordered leader-prepared follower
   WAL append, leader/epoch fencing, duplicate conflict detection, gap handling,
   restart recovery, replica ownership, leader fanout, majority acknowledgement,
   automatic snapshot recovery, lag/recovery
   metrics, concurrent replica consistency validation, and configurable
   leader/quorum/all acknowledgement levels are complete.
8. **Rebalancing — complete**: deterministic join/leave movement planning,
   safe cutover dependency graphs, and the durable joint-consensus state machine
   with dual-majority validation, learner catch-up, identity-aware runtime
   elections/heartbeats, replicated membership proposals, and authenticated
   compare-and-change operator apply workflow are complete. Durable,
   plan-digest-bound prerequisite execution and restart resume are complete.
   Authenticated, target-validated learner snapshot staging is complete.
   Crash-safe, plan-bound source write barriers are complete and fail writes
   closed until the target epoch commits. Leader-coordinated distributed source
   dispatch, apply-time exact-plan gating, and post-commit release fanout are
   complete. Exact-plan, retryable abort recovery before joint consensus is
   complete. The remote-source join preparation gate now covers injected
   learner failure, coordinator restart/resume, staged-data verification,
   durable source fencing, and distributed abort. A real-Raft join gate also
   verifies authenticated prepare/apply, joint-consensus finalization, and
   target voter/epoch convergence. A real-Raft leave gate verifies surviving
   voter convergence, removed-node campaign fencing, and journal cleanup.
   Leave migration now verifies real record preservation, departing-source
   fencing, and post-commit release. Injected target failure plus coordinator
   restart/resume is covered for leave migration. Automatic sequence-based
   follower detection, snapshot repair, and bounded per-follower repair
   backoff are complete. Deterministic resource-aware placement inputs are now
   available and propagated through configuration, discovery, convergence,
   routing, replication, repair, and join/leave rebalancing. Explicit live
   capacity-change planning and the same-voter Raft view commit primitive are
   complete. Canonical capacity manifests now persist through Raft replication,
   snapshots, and restart. Capacity plan/prepare/apply/abort API integration,
   injected target failure, coordinator journal reopen/resume, committed-epoch
   cleanup, and stale-configuration restart recovery are complete.
9. **Production distributed operation — in progress**: deterministic repeated
   leader-isolation chaos coverage now verifies minority rejection, majority
   progress, conflicting-log repair, and monotonic epoch convergence. Protocol
   range negotiation, legacy-v1 fallback, incompatible-peer fencing, and the
   rolling-upgrade runbook are complete. Canonical, epoch/view-fenced per-node
   shard recovery points and deterministic cluster-manifest merging are
   complete. Authenticated operation-bound node freeze/capture/release barriers
   now drain in-flight mutations and fail new writes closed. Leader-only
   freeze/capture/release fanout and canonical manifest assembly are complete.
   Manifest-bound node archive creation, private temporary streaming transport,
   stale-sequence rejection, and restore provenance preservation are complete.
   Canonical multi-node packaging and atomic manifest-bound cluster restore are
   complete. Deterministic newest-replica selection and identity-independent
   target-topology restore planning are complete. Provenance-fenced physical
   shard materialization into fresh remapped target identities, exact sequence
   installation, target-only ownership activation, and atomic topology
   publication are complete. A three-node Kubernetes StatefulSet baseline with
   stable storage, shared-seed discovery, traffic probes, quorum disruption
   protection, failure-domain spreading, restricted ingress, and documented
   bootstrap/scaling workflows is complete. Private-CA mutual TLS for discovery
   and all internal control/data-plane traffic is complete. Graceful shutdown
   now removes readiness, rejects new application work, catches up a canonical
   voter, transfers metadata leadership through a term-fenced immediate
   election, and drains in-flight HTTP work within bounded budgets. A
   reproducible kind gate now builds and deploys the real image, checks
   three-node convergence and PDB state, terminates the elected leader, verifies
   replacement leadership, and waits for full readiness recovery. Execution in
   a CI environment with a container runtime remains before Phase 9 is closed.
   A dedicated, bounded GitHub Actions job now runs that gate on relevant
   changes and retains cluster diagnostics on failure.
10. **Ecosystem — in progress**: the dependency-free, context-aware Go SDK now
    covers authenticated health/readiness, collection lifecycle, namespaced
    vector CRUD and batches, filtered search, placement-aware distributed
    search/writes, bounded responses, and typed API errors without unsafe
    automatic retries. A standard-library Python 3.10+ SDK now provides the
    equivalent synchronous lifecycle, vector, search, distributed-operation,
    bounded-response, TLS-context, and typed-error surface. A dependency-free
    TypeScript SDK for Node.js 20+ and modern browsers now adds abort-aware
    timeouts, bounded streaming responses, typed transport/API failures, and
    the same lifecycle, vector, search, and distributed-operation coverage.
    A dependency-free Java 17 SDK now provides the same core synchronous API,
    strict origin/timeouts, bounded responses, typed errors, and an injectable
    transport. The first embedded administration dashboard slice now provides
    authenticated overview, collection management, redacted search, cluster
    membership, shard-placement, cumulative HTTP performance, and replication
    lag views under a strict same-origin content policy. A node-local Data
    Explorer adds bounded cursor browsing, exact lookup, insertion, deletion,
    namespace selection, and explicit vector reveal. A read-only Configuration
    section exposes non-secret runtime posture and collection/index settings.
    Cluster-wide scrolling, time-series charts, log views, Rust and .NET SDKs,
    integrations, and adoption documentation remain.

The release scope and gates are defined in the [technical
design](technical-design.md#50-first-usable-release-v010).
