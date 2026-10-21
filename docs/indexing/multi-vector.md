# Multi-vector (late interaction) search

**Maturity:** Experimental

Late-interaction models such as ColBERT and ColPali embed a document as one
vector per token (or image patch) and score a query with **MaxSim**: for every
query vector take the best similarity against any document vector, then sum.

```text
MaxSim(Q, D) = Σ_{q ∈ Q} max_{d ∈ D} sim(q, d)
```

`sim` is the field's metric score (`dot`, `cosine`, or negative squared L2).

## Declare and write

A multi-vector field is a [named vector field](named-vectors.md) with
`"multi_vector": true`:

```json
"vectors": {
  "colbert": {"dimension": 128, "metric": "dot", "multi_vector": true,
              "datatype": "float16", "index": {"type": "hnsw"}}
}
```

Records send token vectors in `multi_vectors` (1–1024 vectors per record):

```json
{"id": "d1", "vector": [...], "multi_vectors": {"colbert": [[...128...], [...128...]]}}
```

`float16`/`bfloat16` halves the memory of the token vectors, which dominate a
late-interaction collection.

## Search

```json
{"using": "colbert", "multi_vector": [[...q1...], [...q2...]], "top_k": 10, "candidates": 200}
```

This first version is a two-stage pipeline:

1. **Candidates.** Each record's token vectors are mean-pooled into one
   vector, which the field's index (flat or HNSW) stores. The query tokens
   are mean-pooled the same way and the index returns `candidates` records
   per segment (default `max(4 × top_k, 100)`, at most 1000).
2. **Rescoring.** Each candidate is rescored exactly with MaxSim against its
   stored token vectors and the best `top_k` are returned.

Mean pooling is a cheap, model-agnostic candidate generator; recall depends
on `candidates`. Raise it when the exact top results differ from what pooling
retrieves, or generate candidates with another signal and use MaxSim as a
[rerank stage](reranking.md) instead:

```json
{"vector": [...dense query...], "top_k": 10,
 "rerank": {"type": "maxsim", "field": "colbert", "multi_vector": [[...], [...]], "candidates": 100}}
```

A plain `vector` query with `using` set to a multi-vector field searches the
pooled vectors directly, without MaxSim.

## Cost

MaxSim costs `|Q| × |D| × dimension` multiply-adds per candidate, so 32 query
tokens × 180 document tokens × 128 dimensions ≈ 0.7 M per candidate. Keep
`candidates` in the low hundreds. Token-level indexes (PLAID-style centroid
pruning) are not implemented.
