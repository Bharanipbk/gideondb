# Index memory calibration

**Maturity:** Local development evidence, not a capacity guarantee  
**Date:** 2026-09-07  
**Reference machine:** Apple M3, Darwin arm64, Go 1.26.5

This calibration compares the structural bytes returned by `Index.Stats()` with
the change in Go's retained heap after a forced garbage collection. Each index
is built in a fresh process. The command also writes an in-use heap profile so
allocation attribution can be inspected on a Go installation that includes the
`pprof` tool.

## Reproduce

```bash
go run ./cmd/gideondb-memory-calibration \
  -index flat -vectors 50000 -dimension 128 \
  -heap-profile /tmp/gideondb-flat.pprof

go run ./cmd/gideondb-memory-calibration \
  -index hnsw -vectors 5000 -dimension 64 \
  -heap-profile /tmp/gideondb-hnsw.pprof

go tool pprof -top -sample_index=inuse_space /tmp/gideondb-flat.pprof
go tool pprof -top -sample_index=inuse_space /tmp/gideondb-hnsw.pprof
```

HNSW uses cosine distance, `M=16`, `efConstruction=100`, and `efSearch=64`.
Input vectors are generated deterministically. The calibration calls
`debug.FreeOSMemory` before its baseline and after construction, reads
`runtime.MemStats`, and keeps the completed index live through measurement.

## Results

Three fresh-process runs produced:

| Index | Dataset | Structural B/vector | Retained heap B/vector |
| --- | ---: | ---: | ---: |
| Flat | 50,000 x 128 | 512.00 | 549.03–549.13 |
| Mutable HNSW | 5,000 x 64 | 508.63 | 908.07–908.14 |

Flat's structural count is its float32 vector payload. Its observed retained
gap was about 37 B/vector and includes IDs, ordinal lookup storage, spare slice
capacity, and allocator effects. Mutable HNSW structurally counted the 256-byte
vector payload plus 252.63 B/vector of live directed edge payload. Its observed
gap was about 399 B/vector and additionally includes nodes, per-level slice
headers and capacities, ordinal lookup storage, construction visit marks, and
allocator effects.

## Interpretation limits

- `Stats()` is a deterministic structural lower bound, not Go heap usage or
  process RSS. It intentionally does not estimate map buckets, slice headers,
  spare capacity, or allocator metadata.
- `MappedBytes` describes mapped file extent. It must not be added to retained
  Go heap and does not imply that every mapped page is resident.
- HNSW bytes depend on dimension, `M`, level distribution, mutation/deletion
  history, and whether the recovered graph uses packed immutable adjacency.
- Go version, architecture, allocator behavior, dataset size, and collection
  lifecycle can change the retained gap. Run this command with representative
  settings before capacity planning.
- The generated profile is a whole-process in-use profile, while the reported
  retained value is a before/after heap delta. Small runtime allocations may
  therefore appear in the profile without belonging to the index.

These measurements close the implementation calibration task but are not a
basis for publishing a universal bytes-per-vector claim.
