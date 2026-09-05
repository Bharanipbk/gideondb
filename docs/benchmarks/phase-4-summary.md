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

## Completion boundary

This completes the planned Phase 4 implementation and evidence pass. Remaining
performance work is continuous engineering rather than a blocker for Phase 5.
Product-scale claims still require the technical design's full dataset,
concurrency, cold/warm cache, RSS, tail-latency, and hardware matrix.
