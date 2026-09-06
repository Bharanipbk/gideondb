# Single-node validation

The `gideondb-loadtest` command creates deterministic vectors, ingests them in
shard-grouped batches, checkpoints, closes and reopens the engine, verifies the
recovered count and sampled records, then runs concurrent exact searches that
must return their source IDs. It emits one JSON report with environment,
throughput, p50/p95/p99 latency, checkpoint/recovery time, and Go heap values.

```bash
make validate-single-node VECTORS=100000 DIMENSION=128 SHARDS=8 \
  QUERIES=200 CONCURRENCY=8 WAL_SYNC=always
```

## Local baseline

Apple M3, 8 CPUs, Darwin arm64, Go 1.26.5, flat cosine, 100,000 vectors,
128 dimensions, 8 shards, 8 query workers, 200 queries. The initial run used
`wal_sync=async` and therefore is a performance smoke result, not a durable-
acknowledgement claim:

| Measurement | Result |
|---|---:|
| Ingest | 72,326 vectors/s |
| Checkpoint | 0.561 s |
| Recovery | 0.130 s |
| Queries | 349.7/s |
| p50 / p95 / p99 | 21.863 / 29.675 / 33.803 ms |
| Heap allocated / reserved | 21.6 MB / 276.2 MB |
| Correctness checks | passed |

A second run with `wal_sync=always` measured 25,887 vectors/s ingest, 0.534 s
checkpoint, 0.128 s recovery, 91.4 queries/s, and p50/p95/p99 of
88.217/171.748/211.360 ms. Heap allocated/reserved was 21.6/276.0 MB and all
recovery/query correctness checks passed. Query timing differed substantially
between the two short local runs even though WAL mode no longer affects the
reopened query phase, demonstrating why sustained repetitions and environmental
controls are required before latency claims.

## Million-vector durable runs

The same Apple M3 host completed two `wal_sync=always` scale gates:

| Vectors | Ingest | Checkpoint | Recovery | Query p50 / p95 / p99 | Heap allocated / reserved | Verified |
|---:|---:|---:|---:|---:|---:|---:|
| 1,000,000 | 40,276/s | 5.567 s | 2.104 s | 1.233 / 1.538 / 1.924 s | 314.7 MB / 3.154 GB | yes |
| 2,000,000 | 36,854/s | 15.094 s | 3.454 s | 2.785 / 3.200 / 3.482 s | 629.0 MB / 5.872 GB | yes |

Both runs used 128 dimensions, 8 shards, flat cosine, 8 concurrent query
workers, and shard-grouped batches of 2,000. The 1M run executed 100 queries;
the 2M run executed 50. Recovered counts, 100 sampled records, and every exact-
query source-ID check passed.

The 2M gate demonstrates functional multi-million-vector operation on this
hardware and closes the Phase 5 scale gate. It is not a low-latency claim:
seconds-level exact-scan tail latency and 5.87 GB of reserved Go heap at 2M are
explicit capacity constraints. Broader dimensions, sustained load, RSS/disk
telemetry, ANN scale, and injected process/storage faults remain continuous
release qualification work.
