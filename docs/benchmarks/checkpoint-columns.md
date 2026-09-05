# Checkpoint column read baseline

**Maturity:** Local development benchmark, not a product claim  
**Date:** 2026-09-03

Environment: Apple M3, arm64 macOS, Go 1.26.5. The fixture contains 1,000
records with 128-dimensional float32 vectors, IDs, and otherwise empty record
fields. Both files are in the local filesystem cache. The benchmark validates
both CRCs, decodes record JSON, decodes little-endian vectors, and reconstructs
records.

```bash
go test -run '^$' -bench BenchmarkReadBundle1Kx128 \
  -benchtime=200ms -benchmem ./internal/storage/segmentfile
```

```text
794,814 ns/op
1,330,716 B/op
2,032 allocs/op
```

This measures column decoding only—not WAL replay, metadata/HNSW rebuild,
search, cold disk I/O, mmap, or process startup. The allocation result confirms
that record reconstruction remains expensive; mmap-backed vector ownership and
persisted indexes are future work.
