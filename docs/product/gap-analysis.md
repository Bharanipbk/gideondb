# GideonDB gap analysis

Status: in progress · Last reviewed: 2026-10-16

This document lists the capability gaps between GideonDB today and what users
expect from a competitive open-source vector database (Qdrant, Weaviate,
Milvus, pgvector) and a commercial managed service (Pinecone, Zilliz Cloud,
Qdrant Cloud, Turbopuffer). Each gap records the current state in the code,
why it matters, and a proposed direction. Release-engineering items already
tracked in [PENDING_DEVELOPMENT.md](../../PENDING_DEVELOPMENT.md) are not
repeated here.

Priority key: **P0** blocks adoption or launch · **P1** expected by most users ·
**P2** differentiator or later-stage.

Status key: ✅ done · 🟡 partly done · ⬜ not started.

## Summary

| # | Gap | Track | Priority | Status |
|---|-----|-------|----------|--------|
| 1 | Indexed sparse / hybrid search and BM25 | Open source | P0 | ✅ engine + REST; 🟡 gRPC/SDK/dashboard |
| 2 | Binary and product quantization | Open source | P0 | ✅ binary; ⬜ PQ |
| 3 | Public benchmarks | Open source | P0 | 🟡 harness + synthetic results; ⬜ standard datasets |
| 4 | Disk-resident ANN index | Open source / Cloud | P1 | ⬜ |
| 5 | Server-side embedding (text in, results out) | Open source | P1 | ✅ REST + Go client; ⬜ gRPC, other SDKs |
| 6 | Reranking and multi-vector (late interaction) | Open source | P1 | ⬜ |
| 7 | Agent integrations (MCP server, memory API) | Open source | P1 | ✅ MCP + memory; ⬜ agent SDK adapters |
| 8 | Embedded / single-binary "lite" mode | Open source | P2 | ⬜ |
| 9 | Richer data model (multiple named vectors, payload indexes, geo, full-text filters) | Open source | P1 | 🟡 geo, text filters, group-by, offset; ⬜ named vectors, float16 |
| 10 | Multi-tenancy, scoped API keys, RBAC | Cloud | P0 | ⬜ |
| 11 | Quotas, rate limits, usage metering, billing | Cloud | P0 | ⬜ |
| 12 | Managed control plane (provision, autoscale, upgrade) | Cloud | P0 | ⬜ |
| 13 | Tiered storage on object storage | Cloud | P1 | ⬜ |
| 14 | Serverless / scale-to-zero tier | Cloud | P2 | ⬜ |
| 15 | Enterprise security and compliance | Cloud | P1 | ⬜ |
| 16 | Query analytics and observability for customers | Cloud | P2 | ⬜ |
| 17 | Community and launch readiness | Open source | P0 | ✅ quickstart, Docker one-liner, docs search, release pipeline; ⬜ Helm |
| 18 | HNSW graph connectivity on clustered data (found by gap 3) | Open source | P0 | ✅ |
| 19 | HNSW build throughput | Open source | P1 | 🟡 parallel batch build; ⬜ live inserts, SIMD |

---

## Open-source engine gaps

### 1. Indexed sparse / hybrid search and BM25 — P0 · ✅ engine, 🟡 clients

- **Done (2026-10-16):**
  - Per-segment inverted indexes for `sparse_vector` (term-at-a-time cosine)
    and for a new record `text` field (Okapi BM25, `k1 = 1.2`, `b = 0.75`,
    Unicode tokenizer with English stopwords).
  - Records accept `text` (≤ 1 MiB), persisted through the WAL, checkpoints,
    backups and replication.
  - Hybrid queries combine any of dense, sparse and text signals, with
    `"fusion": "weighted"` (default) or `"rrf"`. Each signal contributes its
    top `max(4k, 100)` candidates. Weighted fusion then rescores every
    candidate exactly on every signal.
  - Wired through local, distributed and internal-shard REST routes. See
    [sparse, keyword and hybrid retrieval](../indexing/sparse-hybrid.md).
  - A text + sparse query over 20,000 documents takes about 0.09 ms
    (`go test -bench SparseInverted ./internal/segment`).
- **Behaviour change:** sparse-only search returns only records sharing a
  query term (see CHANGELOG).
- **Remaining:**
  - Expose `text`, `fusion` and `sparse_vector` in the gRPC contract and all
    six SDKs.
  - Add a text query box and fusion selector to the dashboard search
    playground.
  - Use collection-level BM25 statistics instead of per-segment ones.
  - Persist the inverted indexes in checkpoints instead of rebuilding them at
    load.
  - Optional stemming or language analyzers.

### 2. Binary and product quantization — P0 · ✅ binary, ⬜ PQ

- **Done (2026-10-16):** `"index": {"type": "flat", "quantization":
  "binary", "oversampling": N}` (default N = 4).
  - Sign codes are compared by Hamming distance (XOR + popcount), then the top
    `N × k` are rescored at full precision.
  - In memory-mapped checkpoints, only codes are on the heap; floats are read
    from disk for rescoring.
  - Results on 50,000 × 384 clustered data: recall@10 0.878 at ×4, 0.989 at
    ×10 and 1.000 at ×20, at about 3× the QPS of exact flat search and roughly
    half the segment heap. See
    [binary quantization](../indexing/binary-quantization.md).
- **Remaining:**
  - Product quantization (PQ/OPQ) trained per segment at checkpoint.
  - Binary or PQ codes inside HNSW, which needs gap 18 first.
  - Expose `quantization` in gRPC `CreateCollection` and the dashboard create
    dialog.

### 3. Public benchmarks — P0 · 🟡 harness done

- **Done (2026-10-16):** `cmd/gideondb-bench` and `make bench-ann` report
  recall@k against exact ground truth, QPS, p50/p99, build time and segment
  heap for `flat`, `hnsw`, `hnsw-ef128/256` and `binary(-x10/-x20)`. Input is
  synthetic clustered data or `.fvecs` files (SIFT1M, GIST1M). First results
  are in [ANN recall and latency](../benchmarks/ann-recall.md).
- **Remaining:**
  - Run SIFT1M, GIST1M, GloVe and Cohere 1M.
  - Run VectorDBBench end to end through the server API, with concurrency and
    filtered queries.
  - Publish comparisons with Qdrant, Milvus and pgvector, but only after gap
    19 is fixed.

### 4. Disk-resident ANN index — P1

- **Current state:** HNSW graphs live in memory; segments are mmap-oriented
  but the graph is not designed for SSD access patterns.
- **Why it matters:** RAM-bound indexes cap dataset size per node and make
  cloud pricing uncompetitive at 100M+ vectors.
- **Direction:** DiskANN/Vamana-style graph with compressed vectors in RAM and
  full vectors on SSD; select per collection.

### 5. Server-side embedding — P1 · ✅ REST

- **Done (2026-10-16):** collections can name a `vectorizer` from the
  server's `-embedders-file` (OpenAI-compatible APIs: OpenAI, Azure, Ollama,
  vLLM, LM Studio, TEI). Records and searches accept `text` on single-node
  and cluster REST endpoints; text is kept in `payload.text`. Batching,
  timeouts, key files and a per-node LRU cache keyed by content hash;
  `GET /v1/embedders` reports cache counters. Guide:
  [server-side embedding](../indexing/server-side-embedding.md).
- **Remaining:** `text` on gRPC and in the non-Go SDKs; native Cohere and
  local ONNX providers.

- **Current state:** Embeddings are generated client-side via the Python
  integrations and examples.
- **Why it matters:** "Insert text, search text" removes the biggest
  first-run friction and makes SDKs thinner.
- **Direction:** Optional per-collection vectorizer config (OpenAI-compatible,
  Ollama, Cohere, local ONNX); `text` field accepted on upsert and search;
  cache embeddings by content hash.

### 6. Reranking and multi-vector — P1

- **Current state:** Single dense vector per record; no rerank stage.
- **Why it matters:** Cross-encoder reranking and late-interaction models
  (ColBERT, ColPali) materially improve RAG quality.
- **Direction:** Multi-vector field with MaxSim scoring used as a rescoring
  stage; pluggable rerank step (local model or external API) in the query
  pipeline.

### 7. Agent integrations — P1 · ✅ MCP server and memory

- **Done (2026-10-16):** `cmd/gideondb-mcp`, a dependency-free MCP server over
  stdio. Read tools by default; write and admin tools behind `-allow-writes`
  and `-allow-admin`. Memory tools (`remember`, `recall`, `forget`,
  `prune_memories`) store per-user memories as namespaces with TTL and rank
  recall by similarity blended with recency. Text is embedded through any
  OpenAI-compatible endpoint until gap 5 lands. Guide:
  [MCP server](../integrations/mcp.md).
- **Remaining:** Streamable HTTP transport; adapters for agent SDKs
  (OpenAI Agents, LangGraph, CrewAI).

- **Current state:** LangChain and LlamaIndex adapters only.
- **Why it matters:** Agent frameworks are the fastest-growing source of new
  vector DB users.
- **Direction:** First-party MCP server (search, upsert, collection admin
  tools); simple "memory" API with TTL, recency weighting and per-user
  namespaces; adapters for popular agent SDKs.

### 8. Embedded / lite mode — P2

- **Current state:** Runs as a server; no documented in-process use.
- **Why it matters:** SQLite-style embedding drives adoption in notebooks,
  desktop apps and tests.
- **Direction:** Stable Go library API in `pkg/`, a Python wheel wrapping a
  local engine, and a zero-config single-binary mode.

### 9. Richer data model — P1 · 🟡

- **Done (2026-10-16):** `$text` (whole-word) and `$geo_radius` filters,
  `offset` score pagination and `group_by`/`group_size` grouping on single-node
  and cluster search, with Go client support. No persisted format changed.
- **Remaining (each changes the record or segment format and needs a design
  note first):** named vectors per record; float16/bfloat16 storage; explicit
  payload index types with a token index for `$text`.

- **Current state:** One float32 vector per record, metadata filters,
  namespaces.
- **Why it matters:** Multimodal and multi-field apps need several vectors per
  record and fast filtered search on many payload fields.
- **Direction:** Named vectors per record (different dimensions/metrics);
  explicit payload index types (keyword, integer range, geo, full-text);
  float16/bfloat16 storage; group-by and pagination by score.

### 17. Community and launch readiness — P0 · ✅ (Helm pending)

- **Done (2026-10-16):** a verified five-minute quickstart (the old one
  failed with 401 because the API now always needs a key); full-text search
  with snippets in the embedded docs reader; a fixed Dockerfile (it could not
  build `main`); a tag-triggered release workflow for binaries, checksums and
  a multi-arch GHCR image; a releasing guide with cadence and changelog
  rules; label and Discussions guidance in CONTRIBUTING.
- **Remaining:**
  - ~~Docker one-liner~~ done: the image serves HTTPS with a self-signed
    certificate (`-tls-self-signed`) and reads `GIDEONDB_API_KEY`.
  - Helm chart (Kustomize manifests exist in `deployments/kubernetes`).
  - Enable GitHub Discussions, create the labels, make the GHCR package
    public after the first release.

- **Current state:** Governance, contributing and code-of-conduct files exist;
  SDK publishing is in progress.
- **Gaps:** Five-minute quickstart, Docker/Helm one-liners on the README,
  public discussion channel, "good first issue" labels, release cadence and
  changelog discipline, a docs site with search.

---

### 18. HNSW graph connectivity on clustered data — P0 · ✅ done

- **Done (2026-10-16):** neighbour selection now uses the HNSW paper heuristic
  with keepPrunedConnections, both at insert and when a neighbour's list
  overflows; the base layer holds 2×M links and levels follow mL = 1/ln(M).
  Clustered recall@10 rose from 0.567 to 1.000 (50,000 × 384). The graph
  format is now version 2; version 1 graphs still load. Regression test:
  `internal/index/hnsw/clustered_recall_test.go`.

- **Found by:** the gap 3 benchmark. On clustered data, raising `ef_search`
  from 64 to 256 leaves recall@10 unchanged (0.567 → 0.569 on 50,000 × 384;
  0.697 → 0.697 on 20,000 × 128). On the same data without clusters it rises
  from 0.883 to 0.986.
- **Why it matters:** a plateau that does not respond to `ef_search` means the
  graph is not navigable between clusters. Real corpora are clustered by topic,
  so HNSW recall in production will be worse than tests suggest.
- **Direction:**
  - Check heuristic neighbour selection: keep pruned connections up to M, and
    use extended candidates.
  - Check bidirectional link repair and entry-point selection.
  - Add a clustered-data recall test to `internal/index/hnsw` that must pass
    before closing this gap.

### 19. HNSW build throughput — P1 · 🟡 batch build done

- **Done (2026-10-16):** `hnsw.Build` inserts nodes in parallel with per-node
  locks (hnswlib style) and is used for checkpoint graph builds and for full
  rebuilds after a vector update. Cosine scoring uses cached vector norms.
  50,000 × 384 builds in 11.1 s on 8 cores, down from about 42 s on one
  thread, with unchanged recall. Graphs under 2,048 vectors still build
  serially and deterministically.
- **Remaining:**
  - Live inserts into a mutable segment are still one at a time under the
    index lock (about 40 s for 50,000 × 384).
  - SIMD distance kernels (Go has no auto-vectorisation; needs assembly or
    a Go SIMD package).

## Cloud (paid) gaps

The engine stays Apache 2.0. These gaps describe the closed control plane and
service layer around it.

### 10. Multi-tenancy, scoped API keys, RBAC — P0

- **Current state:** Single admin dashboard credential plus bearer token;
  namespaces exist but carry no ownership or isolation.
- **Direction:** Organizations → projects → clusters; API keys scoped to
  project, collection and action (read/write/admin); roles for members;
  tenant isolation enforced at the API layer and in storage paths.

### 11. Quotas, rate limits, usage metering, billing — P0

- **Current state:** Prometheus metrics are node-local and not attributed to
  a tenant.
- **Direction:** Per-tenant counters (vectors stored, storage bytes, read/write
  units, egress); token-bucket rate limits; usage pipeline into a billing
  provider; plan limits enforced server-side.

### 12. Managed control plane — P0

- **Current state:** Raft metadata, rebalancing and backup/restore exist
  inside a cluster; there is no service that creates or operates clusters.
- **Direction:** Provisioning API and console; Kubernetes operator for
  lifecycle (create, scale, rolling upgrade, failover); scheduled backups with
  point-in-time restore; regional deployment on AWS/GCP/Azure.

### 13. Tiered storage — P1

- **Current state:** Data lives on local disk.
- **Direction:** Immutable segments uploaded to S3-compatible storage; hot
  cache on local NVMe; lazy loading of cold collections. Builds on the existing
  immutable checkpoint format.

### 14. Serverless tier — P2

- **Direction:** Separate compute from storage so idle collections cost only
  object storage; pay-per-query pricing; fast cold start from cached segment
  headers.

### 15. Enterprise security and compliance — P1

- **Current state:** TLS with reload, audited dashboard sessions, CSRF
  protection.
- **Gaps:** SSO/SAML/OIDC, SCIM, audit-log export, private networking
  (PrivateLink/VPC peering), customer-managed encryption keys, encryption at
  rest, SOC 2 Type II and GDPR data-processing terms.

### 16. Customer-facing analytics — P2

- **Direction:** Extend the dashboard with per-collection QPS/latency, slow
  query log, recall drift checks against exact search, index health, and
  usage/billing views.

---

## Suggested sequencing

1. **Open-source launch:** gaps 1 ✅, 2 ✅ (binary), 3 🟡, 18 ✅, 19 🟡 (batch build)
   before publishing benchmarks, then 17 🟡.
2. **Adoption growth:** gaps 5 ✅, 7 ✅, 9 🟡, then 6 and 8.
3. **Cloud beta:** gaps 10, 11, 12.
4. **Cloud scale and enterprise:** gaps 4, 13, 15, then 14 and 16.
