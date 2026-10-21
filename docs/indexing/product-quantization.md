# Product quantization

Product quantization (PQ) compresses each vector to `pq_subspaces` bytes for
candidate generation, then rescores the best candidates at full precision. It
is an option of the flat index, like
[binary quantization](binary-quantization.md):

```json
{
  "name": "docs",
  "dimension": 384,
  "metric": "cosine",
  "shard_count": 4,
  "index": {"type": "flat", "quantization": "pq", "pq_subspaces": 48, "oversampling": 20}
}
```

| Field | Values | Default |
|---|---|---|
| `quantization` | `""` (none), `"binary"` or `"pq"` | none |
| `pq_subspaces` | 1 to `dimension`, and it must divide `dimension`. Requires `"pq"`. | `dimension / 8`; if 8 does not divide the dimension, `dimension / 4`, then `/ 2`, then `/ 1` |
| `oversampling` | 1–100. Requires `quantization`. | `8` when pq |

PQ requires `"type": "flat"`. HNSW collections reject it.

## How it works

1. **Training.** The dimension is split into `m = pq_subspaces` equal
   subspaces. In each subspace, k-means with 256 centroids runs for 12 Lloyd
   iterations on up to 16,384 vectors sampled with a fixed seed, so the same
   records always produce the same codebooks. Subspaces train in parallel.
   Cosine collections normalize vectors first.
2. **Encoding.** Each vector is stored as `m` bytes, one centroid index per
   subspace.
3. **Search (asymmetric distance computation).** The query is not quantized.
   For each query, GideonDB builds an `m × 256` lookup table of partial scores
   (inner product for cosine and dot, negative squared distance for L2). Every
   allowed code is scored with `m` table lookups, keeping the
   `oversampling × k` best.
4. **Rescoring.** Those candidates are rescored with the collection's exact
   metric. The top `k` are returned with exact scores.

Namespace, metadata filters and tombstones are applied before the code scan,
so filtered queries never rescore disallowed records.

## When codebooks are trained

Codebooks are trained when a segment is sealed, not on every write:

- **Mutable segment (recent writes):** always exact flat search. New writes
  are visible immediately, with no training cost on the write path.
- **Sealed segments** (after a checkpoint, and memory-mapped segments loaded at
  startup): product-quantized when they hold at least **1,024 vectors**
  (`pq.MinTrainingVectors`). Smaller segments keep exact flat search: 256
  centroids per subspace are underdetermined with fewer points, and exact
  search on so few vectors is already cheap.

Each checkpoint writes the trained codebooks and codes next to the segment
files (`segment-<lsn>.pq` for a single-bundle checkpoint, `view-<lsn>.pq` for a
multi-segment view) and records the file as `pq_file` in `MANIFEST.json`.
Reopening a shard loads that file instead of retraining.

The file is versioned and checksummed. It also stores a fingerprint of the
record order its codes were written in. If the file is missing (for example a
checkpoint written before PQ existed, or a collection whose segment was too
small at the time), corrupt, or written for a different layout or
configuration, the segment retrains from its own vectors on load and stays
searchable. See the [compatibility contract](../operations/compatibility.md).

## Memory

Each vector costs `m` bytes, plus `dimension × 256 × 4` bytes of codebook per
sealed segment (128 KiB at 128 dimensions). With the default `m = dimension / 8`
that is 32× smaller than float32, the same as binary quantization, but with
finer distances.

- **Memory-mapped checkpoints:** codes and codebooks are on the heap.
  Full-precision vectors stay mapped from disk and are read only for rescored
  candidates.
- **In-memory sealed segments:** records still hold their float vectors for
  rescoring, so PQ speeds up the scan but does not reduce heap use.

## Choosing `pq_subspaces` and `oversampling`

More subspaces give more accurate approximate scores and larger codes. More
oversampling raises recall at the cost of more full-precision rescoring.
Training cost grows with `dimension × 256 × sample size` and runs once per
checkpoint.

Measured with [`gideondb-bench`](../benchmarks/ann-recall.md) on 50,000 ×
128-dimension clustered synthetic vectors (100 clusters, intrinsic dimension
32), 200 queries, k = 10, default `pq_subspaces` = 16 (16 bytes vs 512 bytes of
float32 per vector):

| Config | Metric | recall@10 |
|---|---|---:|
| `binary-x10` | cosine | 0.893 |
| `pq` (×8) | cosine | 0.847 |
| `pq-x10` | cosine | 0.891 |
| `pq-x20` | cosine | 0.977 |
| `pq` (×8) | l2 | 0.928 |
| `pq-x10` | l2 | 0.959 |
| `pq-x20` | l2 | 0.997 |

The bench run was on a shared, heavily loaded 8-core machine, so QPS and
latency were not reproducible enough to publish. In isolation, a PQ scan of
50,000 codes with ×10 rescoring takes about 0.7 ms
(`go test ./internal/index/pq -bench Search50k`). Unlike sign codes, PQ suits L2
on non-centered data, such as SIFT descriptors. Benchmark your own embeddings:

```bash
go run ./cmd/gideondb-bench -base your_base.fvecs -queries your_queries.fvecs -configs flat,pq,pq-x10,pq-x20
```

The bench builds PQ configurations as sealed segments, so `Build s` includes
training and encoding.

## Limits

- Flat index only. PQ codes inside HNSW are future work.
- OPQ (a learned rotation before quantization) is not implemented. It was not
  added because rescoring at ×20 already recovers recall above 0.97 on the
  benchmark data.
- Search is still a linear scan over compact codes. For very large segments,
  HNSW remains the lower-latency option.
- The gRPC `CreateCollection` request does not yet expose `quantization`,
  `oversampling` or `pq_subspaces`. Create quantized collections over REST.
