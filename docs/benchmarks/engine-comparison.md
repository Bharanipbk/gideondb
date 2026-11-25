# GideonDB vs Qdrant vs OpenSearch

First results, 2026-11-25. These are **preliminary**: one small dataset, run
on a heavily loaded laptop. Treat the memory and ingest figures as indicative.
Treat QPS as noise until it is re-run on an idle host (see
[caveats](#caveats)).

## Setup

| | |
|---|---|
| Harness | `gideondb-loadtest compare`, run by [`scripts/benchmark-compare.sh`](../../scripts/benchmark-compare.sh) |
| Engines | GideonDB `main` (dd24c1b), Qdrant v1.12.4, OpenSearch 2.18.0 (k-NN plugin, faiss HNSW) |
| Containers | [`deployments/benchmark/compose.yaml`](../../deployments/benchmark/compose.yaml): each engine 4 CPUs and 4 GiB; OpenSearch heap 1 GiB |
| Host | Apple M3, 8 cores, 16 GiB, Docker Desktop. Load average 25–105 from unrelated containers and builds |
| Index | HNSW, M = 16, ef_construction = 100, one shard/segment per engine |
| Clients | 4 ingest clients, batches of 500; 16 query clients |
| Recall | Tie-aware recall@10 against exact search computed by the harness. A hit counts when it scores at least as well as the 10th true neighbour, so an equally close duplicate (SIFT has many) is not a miss |

Engine-specific settings that keep the comparison fair:

- **Qdrant:** `indexing_threshold` and `full_scan_threshold` are lowered so
  that it searches an HNSW graph. By default it brute-forces small collections.
- **OpenSearch:** the index is force-merged to one segment and warmed up.
  `ef_search` is passed per query with `method_parameters`.
- **GideonDB:** `-wal-sync async` and `-checkpoint-every 0`, as in the
  [standard-dataset runs](standard-datasets.md).

## SIFT-small (10,000 × 128, L2, 100 queries, k = 10)

| Engine | ef_search | Recall@10 | QPS | p50 ms | p99 ms | Ingest s | Build s | Memory MiB |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| GideonDB | 16 | 0.972 | 2151 | 6.11 | 11.89 | 5.4 | 0.0 | **52** |
| GideonDB | 64 | **1.000** | 1876 | 8.00 | 10.76 | 5.4 | 0.0 | 52 |
| GideonDB | 256 | 1.000 | 1105 | 13.22 | 27.26 | 5.4 | 0.0 | 52 |
| Qdrant | 16 | 0.988 | 851 | 15.46 | 34.39 | **0.7** | 2.0 | 189 |
| Qdrant | 64 | 0.999 | 1207 | 11.64 | 28.41 | 0.7 | 2.0 | 189 |
| Qdrant | 256 | 1.000 | 959 | 13.54 | 31.73 | 0.7 | 2.0 | 189 |
| OpenSearch | 16 | 1.000 | 61 | 214.77 | 686.15 | 3.4 | 1.5 | 1496 |
| OpenSearch | 64 | 1.000 | 59 | 212.35 | 734.10 | 3.4 | 1.5 | 1496 |
| OpenSearch | 256 | 1.000 | 57 | 223.88 | 604.36 | 3.4 | 1.5 | 1496 |

The harness also measures ef 32 and 128. Its JSON output has every row.
Memory is the container's resident memory after the run (`docker stats`).

## Findings

- **Memory:** GideonDB used about 3.6× less than Qdrant and about 29× less
  than OpenSearch. OpenSearch's figure is mostly the JVM, which it uses even
  with a 1 GiB heap.
- **Recall:** all three reach 1.000. GideonDB needs ef ≥ 64, and Qdrant is
  slightly ahead at ef 16. OpenSearch is at 1.000 for every ef, and its QPS
  stays flat, which suggests faiss expands small ef values. Check this on a
  larger set.
- **Ingest is GideonDB's weak point.** Qdrant ingests about 8× faster, and
  OpenSearch about 1.6× faster. GideonDB inserted each record into a live HNSW
  graph inside the write path, and a single shard used only about 1.1 of its
  4 CPUs. On GloVe (390,000 × 100), batches slowed from about 3 s to over 5 min
  as the graph grew, so the run did not finish (see below).
  **Update:** batch writes now link each shard's records in parallel, which
  makes ingest about 3× faster (556–654 → 1,943–2,008 vectors/s on 100,000
  GloVe vectors, with recall unchanged; see the [changelog](../../CHANGELOG.md)).
  The figures above are from the earlier build. The engine-wide write lock
  still serializes concurrent batches, so that is the next step.
- **Per-query latency:** latency through REST was lowest for GideonDB at
  every ef on this run. With the host this loaded, that ranking is not
  reliable yet.

## GloVe-100 (390,000 × 100, cosine): not completed

Two attempts failed during GideonDB's ingest:

1. A TLS `bad record MAC` from Docker Desktop's port proxy. The harness now
   retries batches.
2. A batch exceeded the 5-minute client timeout while the host load average was 105.

Neither engine got as far as querying. Re-run on an idle machine:

```bash
scripts/benchmark-compare.sh glove-100-base.fvecs glove-100-query.fvecs cosine glove.json -query-count 1000
```

Dataset preparation is in [standard datasets](standard-datasets.md#reproduce).
`nlp.stanford.edu` refused connections during this run. The same
`glove.6B.zip` is mirrored at `huggingface.co/stanfordnlp/glove`. Its
SHA-256, `6471382cdd837544bf3ac72497a38715e845897d265b2b424b4761832009c837`,
differs from the Stanford archive's because the zip was packaged separately.
The extracted `glove.6B.100d.txt` was used unchanged.

## Caveats

- QPS changes from ef to ef in ways that track host load, not the index (for
  example, Qdrant's ef 32 is slower than its ef 64). Re-run with
  `BENCH_CPUS` pinned on an idle host before quoting throughput.
- Everything goes over each engine's REST API through Docker's port proxy,
  so client and proxy overhead is included, the same for all engines.
- 10,000 vectors fit in cache for every engine. Larger datasets (GloVe,
  SIFT1M) are needed before drawing conclusions about recall/QPS curves.

## Reproduce

```bash
scripts/benchmark-compare.sh siftsmall_base.fvecs siftsmall_query.fvecs l2 sift.json -query-count 100
```

`siftsmall.tar.gz` comes from `ftp://ftp.irisa.fr/local/texmex/corpus/`.
Extra flags go to `gideondb-loadtest compare` (`-engines`, `-ef-sweep`,
`-m`, `-ef-construction`, `-concurrency`, ...). `KEEP=1` leaves the
containers running.
