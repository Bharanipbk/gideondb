# Metadata filtering optimization baseline

**Maturity:** Local development benchmark, not a product claim  
**Date:** 2026-09-03  
**Commit:** Uncommitted workspace state

## Environment

- CPU: Apple M3
- Architecture: arm64
- OS: macOS (`darwin` reported by Go)
- Go: 1.26.5
- Dataset: 100,000 synthetic records in one in-memory metadata index
- Benchmark time: 200 ms minimum per case
- Command:

```bash
go test -run '^$' -bench 'Benchmark(Equality|Numeric)' \
  -benchtime=200ms -benchmem ./internal/metadata
```

## Results

| Operation | Map sets / field scan | Dense bitmaps / sorted numeric | Allocation change |
|---|---:|---:|---:|
| Equality posting | 2,005,726 ns/op | 1,761 ns/op | 2,660,209 → 13,616 B/op |
| Numeric `>= 2020` | 4,463,902 ns/op | 57,452 ns/op | 4,729,581 → 13,600 B/op |

An additional one-iteration build benchmark inserts 100,000 unique numeric
values. Moving from dense-per-value postings to adaptive singleton/sparse/dense
postings reduced the measured build from 305,403,375 ns and 706,432,144
allocated bytes to 240,936,791 ns and 44,322,344 allocated bytes. The current
run made 401,456 allocations; further dictionary and merge work is needed.

The equality dataset has ten categories. The numeric dataset has values spread
over 30 years and the predicate matches roughly one third of records. Setup and
index construction occur before benchmark timing.

These results isolate filter-set evaluation. They do not include vector search,
REST, WAL, checkpointing, concurrent mutation, process RSS, or disk I/O. Dense
bitmap cost scales with the highest segment ordinal, while range cost also
scales with match cardinality and the pending delta (bounded at 4,095 entries).
Build measurements use one iteration and are especially sensitive to runtime
noise. More datasets and repeated statistical runs are required before release
claims.
