# Parallel logical-shard search

The collection search path assigns logical shards to at most `GOMAXPROCS`
workers. Each shard produces a local top-K result, then a bounded min-heap
selects the global top-K in `O(shards * K * log K)` time. Final ordering is
deterministic: descending score, then ascending record ID.

## Reproduce

```bash
go test -run '^$' -bench BenchmarkParallelShardSearch8x8Kx32 \
  -benchtime=500ms -benchmem ./internal/collection
```

Apple M3, Darwin arm64, Go benchmark results from 2026-09-03:

```text
workers-1   1,088,916 ns/op   21,692 B/op   203 allocs/op
workers-8   1,079,830 ns/op   21,499 B/op   203 allocs/op
```

This 65,536-vector, 32-dimension flat scan is memory-bandwidth dominated on the
measured machine, so eight workers improved latency by less than one percent.
The benchmark is retained to prevent unsupported speedup claims and to guide
later memory-layout, SIMD, and worker-scheduling work. Single-shard collections
take a direct path without worker or channel overhead.
