# Binary quantization

Binary quantization keeps a 1-bit code per dimension (the sign of each value)
for candidate generation, then rescores the best candidates at full precision.
It is an option of the flat index:

```json
{
  "name": "docs",
  "dimension": 384,
  "metric": "cosine",
  "shard_count": 4,
  "index": {"type": "flat", "quantization": "binary", "oversampling": 10}
}
```

| Field | Values | Default |
|---|---|---|
| `quantization` | `""` (none) or `"binary"` | none |
| `oversampling` | 1–100. Requires `quantization`. | `4` when binary |

Binary quantization requires `"type": "flat"`. HNSW collections reject it.

## How it works

1. Each vector becomes `ceil(dimension / 64)` 64-bit words. Bit *i* is set when
   component *i* is greater than 0.
2. A query is encoded the same way. Every allowed code is compared by Hamming
   distance (XOR + popcount), keeping the `oversampling × k` closest.
3. Those candidates are rescored with the collection's exact metric. The top
   `k` are returned with exact scores.

Namespace, metadata filters and tombstones are applied before the Hamming scan,
so filtered queries never rescore disallowed records.

Codes take `dimension / 8` bytes per vector, 32× less than float32:

- **In-memory segments:** codes replace the flat index's private float copy.
  Rescoring reads vectors from the record store.
- **Checkpointed, memory-mapped segments:** codes are built on the heap at load
  time. Full-precision vectors stay mapped from disk and are read only for the
  few rescored candidates. Heap use is then about 32× lower than loading the
  floats.

## Choosing `oversampling`

Sign codes preserve angle, so recall depends on the embedding distribution.
Measured with [`gideondb-bench`](../benchmarks/ann-recall.md) on 50,000 ×
384-dimension clustered vectors with low intrinsic dimension:

| oversampling | recall@10 | QPS vs exact flat |
|---|---:|---:|
| 4 (default) | 0.878 | 3.2× |
| 10 | 0.989 | 3.1× |
| 20 | 1.000 | 3.0× |

On full-rank random Gaussian data, the worst case for sign codes, recall@10 is
only about 0.55 even at oversampling 10. Always benchmark your own embeddings
before enabling binary quantization:

```bash
go run ./cmd/gideondb-bench -base your_base.fvecs -queries your_queries.fvecs -configs flat,binary,binary-x10,binary-x20
```

Binary quantization works best for cosine or dot-product embeddings that are
roughly zero-centered, which covers most modern text-embedding models. It is
usually a poor fit for L2 on non-centered data, such as raw SIFT descriptors,
whose components are all non-negative.

## Limits

- Flat index only. A quantized HNSW graph is future work.
- Search is still a linear scan, now over compact codes: about 12× faster per
  vector than exact float scoring in the microbenchmark. For very large
  segments, HNSW remains the lower-latency option.
- The gRPC `CreateCollection` request does not yet expose `quantization` or
  `oversampling`. Create quantized collections over REST.
