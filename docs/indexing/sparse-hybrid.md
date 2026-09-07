# Sparse and hybrid retrieval

Records may store an optional `sparse_vector`: a JSON object whose keys are
terms or feature IDs and whose values are finite float32 weights. A sparse
vector contains 1–10,000 non-empty terms. Records still require their normal
dense vector so existing collections, WAL records, checkpoints, backups, and
replication remain compatible.

Send only `sparse_vector` for sparse cosine retrieval:

```json
{"sparse_vector":{"database":1.0,"vector":0.7},"top_k":10}
```

Send both vectors for hybrid retrieval. `alpha` is the dense contribution from
0 through 1 and defaults to `0.5`:

```json
{
  "vector":[0.12,0.34],
  "sparse_vector":{"database":1.0},
  "alpha":0.7,
  "top_k":10
}
```

Hybrid scoring normalizes the collection's dense metric to `[0,1]`, normalizes
sparse cosine to `[0,1]`, and computes `alpha*dense + (1-alpha)*sparse`.
Sparse-only responses expose raw sparse cosine scores. Both modes support exact
namespace and metadata filtering and work through local and placement-aware
distributed search routes.

Sparse and hybrid search currently performs an exact scan. `ef_search` applies
only to dense-only HNSW queries and is ignored when `sparse_vector` is present.
This favors predictable correctness while a persistent sparse inverted index
remains outside the current release scope.
