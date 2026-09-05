# HNSW construction optimization

**Maturity:** Local development benchmark, not a product claim  
**Date:** 2026-09-03

Environment: Apple M3, Darwin arm64. The workload builds 2,000 deterministic
128-dimensional cosine vectors with `M=16` and `efConstruction=100`.

```bash
go test -run '^$' -bench BenchmarkBuild2Kx128 \
  -benchtime=1x -count=5 -benchmem ./internal/index/hnsw
```

| Implementation | Time | Allocated bytes | Allocations |
|---|---:|---:|---:|
| Generic heaps + per-layer visited maps | 1.134–1.144 s | ~207.1 MB | ~2.658 M |
| Typed heaps | 1.031–1.038 s | ~156.5 MB | ~190.1 K |
| Typed heaps + generation marks | 0.887–0.896 s | ~47.9 MB | ~165.1 K |

Against the original baseline, the final implementation reduced construction
time by about 21%, allocated bytes by about 77%, and allocation count by about
94%. Recall tests remain the correctness oracle. These small-build results do
not establish million-vector construction throughput or memory requirements.

## Search scratch reuse

The search path checks out a request-exclusive visited map from a concurrency-
safe pool and clears it before reuse. Maps that visit more than 4,096 ordinals
are discarded, preventing unusually broad queries from becoming retained pool
capacity.

Five 1-second runs of the 5K-by-128 cosine search benchmark measured
135.7–136.7 microseconds/op, about 5.5 KB/op, and 8 allocations/op. Before
scratch reuse, the same run varied from 151–187 microseconds/op, allocated
38.4 KB/op, and made 15 allocations/op. Allocated bytes fell about 86% and
allocation count about 47%; latency improved despite variance in the baseline.

## Prepared cosine traversal

The query squared norm is now calculated once and carried through greedy
descent, layer search, construction, and neighbor pruning. On top of the typed
heap and scratch-reuse work, five warmed search runs improved from
135.7–136.7 to 134.6–135.0 microseconds/op. Construction improved from
887–896 to 877–885 milliseconds for the same 2K-by-128 workload. Allocation
counts were unchanged. This roughly one-percent incremental gain is retained
because it removes provably repeated work without adding mutable query state.
