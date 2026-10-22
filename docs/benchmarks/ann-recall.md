# ANN recall and latency benchmark

`cmd/gideondb-bench` compares index configurations on the same dataset:
recall@k against exact search, single-thread QPS, p50/p99 latency, build time
and segment heap. It drives a single in-process segment, so the results isolate
index behaviour from HTTP, WAL and sharding overhead.

```bash
# Synthetic clustered data (default: 100 clusters, intrinsic dimension 32)
make bench-ann VECTORS=50000 DIMENSION=384

# Standard datasets in .fvecs format; see standard-datasets.md
go run ./cmd/gideondb-bench -base sift_base.fvecs -queries sift_query.fvecs \
  -metric l2 -query-count 1000 -configs flat,hnsw,hnsw-ef256 -json sift.json
```

Configurations: `flat`, `hnsw`, `hnsw-ef128`, `hnsw-ef256`, `binary`
(oversampling 4), `binary-x10`, `binary-x20`, `pq` (oversampling 8), `pq-x10`,
`pq-x20`. `-intrinsic 0` gives full-rank
noise, and `-clusters 0` removes topic clusters.

## Results — 2026-10-16

Apple M3 (8 cores), Go 1.26, darwin/arm64, single query thread, k = 10,
cosine.

### 50,000 × 384, 100 clusters, intrinsic dimension 32

| Config | Recall@10 | QPS | p50 ms | p99 ms | Build s | Segment heap MiB |
|---|---:|---:|---:|---:|---:|---:|
| flat | 1.000 | 68 | 14.555 | 16.541 | 0.09 | 166.5 |
| hnsw (M=16, ef=64) | 1.000 | 2510 | 0.364 | 0.661 | 44.21 | 185.9 |
| hnsw-ef256 | 1.000 | 1324 | 0.716 | 1.205 | 44.25 | 185.9 |
| binary (×4) | 0.878 | 215 | 4.606 | 5.361 | 0.18 | 88.9 |
| binary-x10 | 0.989 | 211 | 4.710 | 5.865 | 0.18 | 88.9 |
| binary-x20 | 1.000 | 203 | 4.886 | 5.879 | 0.18 | 88.9 |

### 20,000 × 128, 100 clusters, intrinsic dimension 32

| Config | Recall@10 | QPS | p50 ms | p99 ms | Build s | Segment heap MiB |
|---|---:|---:|---:|---:|---:|---:|
| flat | 1.000 | 347 | 2.889 | 3.230 | 0.02 | 25.9 |
| hnsw | 1.000 | 6931 | 0.141 | 0.223 | 6.42 | 33.6 |
| hnsw-ef128 | 1.000 | 4663 | 0.212 | 0.310 | 6.43 | 33.6 |
| hnsw-ef256 | 1.000 | 1969 | 0.502 | 0.653 | 6.40 | 33.6 |
| binary-x10 | 0.983 | 575 | 1.717 | 1.874 | 0.03 | 16.6 |

### 20,000 × 128, no clusters, intrinsic dimension 32

| Config | Recall@10 | QPS | p50 ms | p99 ms | Build s |
|---|---:|---:|---:|---:|---:|
| hnsw | 0.976 | 3235 | 0.299 | 0.392 | 8.39 |
| hnsw-ef256 | 1.000 | 1065 | 0.928 | 1.115 | 8.39 |

### Product quantization — 2026-10-21, 50,000 × 128, 100 clusters, intrinsic dimension 32

Apple M3, Go 1.26.5. Default `pq_subspaces` = 16 (16 bytes per vector). The
machine was heavily loaded by other jobs during this run, so only recall is
reported; QPS and latency were not reproducible.

| Config | Recall@10 (cosine) | Recall@10 (l2) |
|---|---:|---:|
| binary-x10 | 0.893 | — |
| pq (×8) | 0.847 | 0.928 |
| pq-x10 | 0.891 | 0.959 |
| pq-x20 | 0.977 | 0.997 |

## Findings

- **Binary quantization** reaches recall@10 0.99 at oversampling 10, about 3×
  faster than exact flat search with roughly half the segment heap. In the
  index microbenchmark (`go test -bench . ./internal/index/binary`), it is
  about 12× faster than exact scoring on 50,000 × 384 vectors.
- **HNSW recall on clustered data is fixed (gap 18).** Before the fix,
  raising `ef_search` from 64 to 256 left recall@10 flat (0.567 → 0.569 on
  50,000 × 384; 0.697 → 0.697 on 20,000 × 128) because neighbour selection kept
  only the closest M links, so the graph had almost no cross-cluster edges.
  HNSW now uses the paper's neighbour-selection heuristic with
  keepPrunedConnections, 2×M links on the base layer and mL = 1/ln(M) levels.
  Recall@10 is 1.000 on all clustered runs; without clusters it rises with
  `ef_search` (0.976 → 1.000). QPS at equal `ef_search` is lower because the
  search now explores more of the graph.
- **HNSW build is parallel for batch builds (gap 19).** Checkpoint graph
  builds and full rebuilds now insert nodes concurrently with per-node locks,
  and cosine scoring uses cached vector norms. 50,000 × 384 builds in 11.1 s
  on 8 cores instead of 40.0 s on one, with the same recall (1.000 at
  `ef_search` 64 and 256). The `Build s` column above still measures live
  one-at-a-time inserts into a mutable segment, which stay serial.

These are synthetic results. For GloVe (SIFT1M pending), including an end-to-end
run through the HTTP server with concurrent and filtered queries, see
[standard datasets](standard-datasets.md). For kernel and insert throughput,
see [distance kernels](distance-kernels.md). Comparisons with other systems
wait for VectorDBBench.
