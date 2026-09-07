# Phase 4 performance summary

**Maturity:** Local development evidence, not product-scale claims  
**Date:** 2026-09-03  
**Reference machine:** Apple M3, Darwin arm64

Phase 4 established contiguous vector storage, mmap-backed flat checkpoint
bases, bounded parallel shard fan-out, deterministic heap-based query merging,
portable four-lane distance kernels, prepared cosine scoring, and allocation-
reduced flat and HNSW paths. Individual reproducible commands and raw ranges
are recorded in the benchmark documents beside this file.

## Scheduling conclusion

Shard workers are bounded by `GOMAXPROCS`; single-shard queries bypass workers.
The 8-shard flat benchmark is memory-bandwidth and metadata-lookup dominated,
and did not show a material eight-worker speedup. Parallel fan-out remains
useful for independent HNSW shards and as the execution boundary for future
distributed search, but no broad QPS multiplier is claimed. Typed merge
primitives and avoiding a cloned universe bitmap for unfiltered queries reduced
the 8-shard benchmark from 203 to 61 allocations and roughly 21.5 KB to 9.6 KB
per operation; wall-time samples remained noisy.

## SIMD decision

The portable four-lane kernels reduced the 768-dimensional dot benchmark by
about 64–65% and preserve one implementation across supported platforms.
Architecture-specific ARM64 and AMD64 assembly is deferred: it requires two
maintained kernels, feature detection, numerical-equivalence testing, and a
measured end-to-end gain beyond the current memory/metadata bottlenecks. The
portable kernel is the correctness oracle for any later SIMD implementation.

## Int8 quantization evaluation

On 2026-09-07, a symmetric per-vector int8 prototype was tested against exact
float32 scoring. It uses one byte per dimension plus a four-byte scale, reducing
a 768-dimensional vector from 3,072 to 772 bytes (about 75%). On 1,000
deterministic random 32-dimensional vectors and 25 queries, approximate cosine
top-10 achieved 0.996 recall@10 against exact flat scores.

The portable int8 cosine kernel measured 1,075–1,079 ns/op with zero allocations
on the same Apple M3, versus 414–415 ns/op for prepared float32 cosine. Encoding
a 768-dimensional vector measured 5,768–5,772 ns/op, 768 B/op, and one
allocation. Commands:

```bash
go test -run TestInt8RecallAgainstExactFlatScores -v ./internal/quantization
go test -run '^$' -bench 'Benchmark(Dot768|CosinePrepared768)$' \
  -benchmem -count=3 ./internal/distance
go test -run '^$' -bench 'Benchmark(Int8Cosine768|QuantizeInt8_768)$' \
  -benchmem -count=3 ./internal/quantization
```

Decision: retain the prototype and its regression tests as evaluation evidence,
but do not add int8 to the persisted segment format or public configuration yet.
An accelerated scoring implementation, representative embedding datasets, all
metrics, end-to-end HNSW reranking, and a versioned format ADR are required
before adoption. Exact float32 flat search remains the oracle.

## Memory calibration

The reproducible [index memory calibration](index-memory-calibration.md)
compares structural index counters with retained Go heap deltas and can emit
in-use heap profiles. On the reference machine, three isolated runs measured
549.03–549.13 retained B/vector for a 50,000 x 128 flat index, versus 512
structural B/vector. A 5,000 x 64 mutable HNSW index measured 908.07–908.14
retained B/vector, versus 508.63 structural B/vector. These are point
measurements that expose uncounted runtime overhead, not capacity claims.

## Completion boundary

This completes the planned Phase 4 implementation and evidence pass. Remaining
performance work is continuous engineering rather than a blocker for Phase 5.
Product-scale claims still require the technical design's full dataset,
concurrency, cold/warm cache, RSS, tail-latency, and hardware matrix.
