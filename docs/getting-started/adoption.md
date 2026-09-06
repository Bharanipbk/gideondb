# Adoption guide

VectorDB is pre-alpha. Use this guide to evaluate it without turning successful
local tests into unsupported production claims. Each stage has an exit gate;
do not advance until the gate is repeatable with your own data and failure
model.

## 1. Confirm workload fit

Record the vector dimension, metric, expected vector count, write rate, query
concurrency, top K, metadata selectivity, payload size, tenant/namespace model,
latency objective, recall objective, durability target, and recovery-time
objective. VectorDB currently stores dense float32 vectors and supports cosine,
dot-product and L2 distance. Sparse/hybrid retrieval and quantized storage are
not implemented.

Choose flat search when exact results or a smaller dataset matter more than
latency. Treat HNSW as experimental and measure recall against flat search with
your data before selecting its construction and search parameters. Follow the
[HNSW guide](../indexing/hnsw.md) and checked-in [benchmark methodology](../benchmarks/phase-4-summary.md).

**Exit gate:** a written workload manifest and explicit acceptance thresholds.

## 2. Run a disposable local evaluation

Complete the [quickstart](quickstart.md) in a disposable data directory. Test
collection creation, batch ingestion, filtered search, namespace isolation,
record deletion, restart recovery, and checkpoint behavior. Use the SDK closest
to the intended application: [Go](../sdk/go.md), [Python](../sdk/python.md),
[TypeScript](../sdk/typescript.md), or [Java](../sdk/java.md).

Generate embeddings outside the database. Provider-neutral Python helpers
support [Ollama](../integrations/ollama.md), [Hugging Face](../integrations/huggingface.md),
[OpenAI-compatible APIs](../integrations/openai-compatible.md), and
[Cohere](../integrations/cohere.md); optional bridges support
[LangChain](../integrations/langchain.md) and [LlamaIndex](../integrations/llamaindex.md).
Use one embedding model and dimension consistently for ingestion and queries.

**Exit gate:** deterministic correctness after restart and measured search
quality against a representative labelled query set.

## 3. Establish a single-node baseline

Use a dedicated volume and an explicit [configuration](../operations/configuration.md).
Select WAL sync and checkpoint settings from the documented durability
trade-offs, then run the [single-node validation](../benchmarks/single-node-validation.md)
and workload-specific soak tests. Measure p50/p95/p99 latency, throughput, RSS,
CPU, disk growth, recovery time, and HNSW recall where applicable. Do not reuse
benchmark numbers from different hardware, dimensions, durability settings, or
recall targets.

Exercise [backup and restore](../operations/backup-restore.md) into a new data
path and verify record counts plus representative queries. A backup that has
not been restored successfully is not a recovery plan.

**Exit gate:** reproducible performance and recovery evidence on target-class
hardware, with capacity headroom and alert thresholds.

## 4. Harden the service boundary

Follow the [security guide](../operations/security.md). Use a bearer token from
a mode-`0600` file, TLS for non-loopback traffic, private-CA mutual TLS between
cluster nodes, restricted network reachability, non-root execution, and secret
rotation procedures. Never place API credentials in dashboard URLs, examples,
images, or committed configuration.

Scrape the bounded metrics and connect structured logs/traces as described in
[observability](../operations/observability.md). Alert on readiness loss, error
rate, tail latency, capacity, replica lag, repair/rebalance failures, and backup
failures. The embedded dashboard is an operational aid, not the metrics system
of record.

**Exit gate:** security review, credential-rotation drill, alerts tested with
injected faults, and documented on-call ownership.

## 5. Validate distributed operation

Use at least three failure-domain-separated voters and follow the
[Kubernetes deployment guide](../operations/kubernetes.md). Verify placement
convergence and readiness before load. Test minority and majority partitions
using the [network-partition runbook](../operations/network-partitions.md), then
test leader termination, follower recovery, replica repair, and capacity-aware
movement while traffic continues.

The checked-in kind gate is disposable validation, not evidence that a specific
production cluster, storage class, ingress, or disruption policy is safe. The
external kind CI execution remains an outstanding Phase 9 release gate.

**Exit gate:** repeated fault drills meet acknowledged-write safety, recovery,
availability, and replica-lag objectives on the target platform.

## 6. Rehearse lifecycle operations

Follow the [rolling-upgrade runbook](../operations/rolling-upgrades.md), moving
one voter at a time and checking protocol compatibility and readiness between
nodes. Rehearse backup, full-cluster restore onto fresh identities, capacity
changes, node replacement, certificate rotation, and rollback. Preserve the
exact binary version and configuration used for every backup and benchmark.

**Exit gate:** an operator who did not author the system can execute the
runbooks during a timed game day without undocumented steps.

## 7. Make an explicit rollout decision

Before serving production traffic, document accepted pre-alpha risks and a
fallback. Start with non-critical or shadow traffic, cap dataset and tenant
growth, and define stop conditions for correctness, latency, memory, disk,
recovery, or operational load. Pin the tested build rather than tracking a
moving branch.

The current blockers to a general production recommendation are listed in the
[roadmap](../roadmap.md). Completing this guide reduces deployment risk but does
not change the project's maturity label or replace an independent review.
