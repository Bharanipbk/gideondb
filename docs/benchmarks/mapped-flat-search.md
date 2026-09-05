# Mapped flat-search baseline

**Maturity:** Local development benchmark, not a product claim  
**Date:** 2026-09-03

Environment: Apple M3, arm64 macOS, Go 1.26.5. Dataset: 10,000 synthetic
128-dimensional float32 vectors, dot-product search, top-k 10, warm filesystem
cache. Both implementations use the same top-k heap and query.

```bash
go test -run '^$' -bench 'Benchmark(Search10Kx128|MappedSearch10Kx128)' \
  -benchtime=300ms -benchmem ./internal/index/flat
```

| Storage | Time | Heap allocation |
|---|---:|---:|
| Heap-contiguous flat | 812,077 ns/op | 432 B/op, 15 allocs/op |
| Read-only mmap flat | 772,040 ns/op | 432 B/op, 15 allocs/op |

The mapped run uses a guarded zero-copy float32 view. Earlier byte-by-byte
little-endian decoding measured about 1.28 ms/op, which justified the optimized
path. These results exclude cold-page faults, mapping/open/CRC validation,
record decoding, metadata filtering, WAL replay and concurrent load. Larger
datasets and cold/warm page-cache matrices are required before product claims.

## Portable kernel optimization

Four independent accumulation lanes reduce the dependency chain in dot, L2,
and cosine kernels. On the same Apple M3, a 768-dimensional dot product fell
from a stable 1,184–1,188 ns/op baseline to 414–438 ns/op, with zero
allocations. This is instruction-level parallelism in portable Go, not an
architecture-specific SIMD claim.

After the kernel change, repeated 10K-by-128 searches measured:

| Storage | Time range | Heap allocation |
|---|---:|---:|
| Heap-contiguous flat | 735,150–737,678 ns/op | 432 B/op, 15 allocs/op |
| Read-only mmap flat | 749,655–751,047 ns/op | 432 B/op, 15 allocs/op |

The smaller end-to-end improvement shows that candidate filtering, metadata
lookups, heap maintenance, and memory bandwidth now account for more of query
latency. Architecture-specific SIMD remains future work and must retain the
portable implementation as its correctness oracle.

## Prepared cosine query

Heap and mmap flat indexes now calculate the query's squared norm once before
scanning candidates. Five 1-second runs of the 768-dimensional kernel measured
420–433 ns/op for the standalone cosine path and 414–419 ns/op for the prepared
path, both with zero allocations. A 10K-by-128 heap cosine search measured
757–781 microseconds/op. The modest improvement is reported as measured: the
four-lane kernel already overlaps much of the arithmetic on this CPU.

## Typed top-K selection

Replacing generic `container/heap` and reflection-based final sorting with
typed heap and insertion-ordering primitives reduced 10K-by-128 top-10 search
from 15 allocations and 432 B/op to one result-slice allocation and 160 B/op.
Five 1-second runs measured 748–760 microseconds/op for heap dot search (apart
from one 808-microsecond outlier), 760–764 microseconds/op for heap cosine, and
761–768 microseconds/op for mmap dot search. This is a 93% allocation-count and
63% allocated-byte reduction with broadly comparable latency.
