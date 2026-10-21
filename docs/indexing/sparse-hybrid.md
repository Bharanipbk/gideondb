# Sparse, keyword and hybrid retrieval

GideonDB combines up to three retrieval signals in one query:

| Signal | Record field | Query field | Index |
|---|---|---|---|
| Dense | `vector` (required) | `vector` | flat, HNSW or binary-quantized flat |
| Sparse | `sparse_vector` (optional) | `sparse_vector` | per-segment inverted index, cosine |
| Keyword | `text` (optional) | `text` | per-segment BM25 inverted index |

A search that includes `sparse_vector` or `text` uses the hybrid path. A
search with only `vector` uses the dense path unchanged.

## Record fields

`sparse_vector` is a JSON object of terms or feature IDs to finite float32
weights, with 1–10,000 non-empty terms. Use it for learned sparse models such as
SPLADE, or your own term weights.

`text` is free text of up to 1 MiB, indexed for BM25. GideonDB tokenizes it
for you, so clients do not have to compute keyword weights. It is persisted in
the WAL, checkpoints, backups and replication, and returned on `GET`.

```json
{"id":"doc-7","vector":[0.12,0.34],"text":"Binary quantization cuts vector memory by 32x"}
```

Records still require a dense vector, so existing collections and persisted
formats stay compatible. Older checkpoints simply have no `text`.

## Query forms

Keyword only (BM25). Scores are raw BM25 values:

```json
{"text":"binary quantization","top_k":10}
```

Sparse only. Scores are raw sparse cosine values:

```json
{"sparse_vector":{"database":1.0,"vector":0.7},"top_k":10}
```

Hybrid with weighted fusion (the default). `alpha` is the dense weight from 0
to 1 and defaults to `0.5`:

```json
{"vector":[0.12,0.34],"text":"binary quantization","alpha":0.7,"top_k":10}
```

Hybrid with Reciprocal Rank Fusion:

```json
{"vector":[0.12,0.34],"text":"binary quantization","sparse_vector":{"bq":1},"fusion":"rrf","top_k":10}
```

All forms support namespaces and metadata filters. They work on the local
route, on `/v1/cluster/collections/{name}/search`, and on internal shard
fan-out.

## How retrieval works

Each segment keeps two inverted indexes in memory, built when the segment is
loaded and maintained on every upsert and delete:

- **Sparse:** term → (record, weight) postings, plus each record's sparse
  norm. A query visits only the postings of its own terms (term-at-a-time
  accumulation), not every record.
- **Text:** token → (record, term frequency) postings, plus document lengths.
  Scoring is Okapi BM25 with `k1 = 1.2` and `b = 0.75`.

For a query, each requested signal contributes its top
`max(4 × top_k, 100)` candidates from the segment. Candidates must pass the
namespace, metadata filter and tombstone checks. The union is then fused:

- **`weighted`:** every candidate is scored exactly on every requested signal.
  A candidate found only by the dense index still gets its true sparse and BM25
  scores, and the reverse. Normalization:
  - dense: the collection metric mapped to `[0,1]`;
  - sparse: `(cosine + 1) / 2`;
  - BM25: `score / (score + 2)`. This keeps scores comparable across segments
    and shards without per-segment maxima.

  Lexical signals are averaged, then `alpha·dense + (1 − alpha)·lexical`.
  Dense + sparse scores are identical to earlier releases.
- **`rrf`:** each signal's ranked list adds `1 / (60 + rank)`, with rank
  starting at 1. RRF ignores score scales, so it is the safer choice when
  signals are hard to calibrate.

Shards and segments are merged by fused score.

## Tokenizer

Text is lowercased and split on every character that is not a Unicode letter
or digit. A short list of English stopwords (`the`, `and`, `of`, …) is
removed. There is no stemming, so `index` and `indexes` are different tokens.
The same tokenizer applies to records and queries.

## Behaviour changes and limits

- Sparse-only and text-only searches return only records that share at least
  one query term. Earlier releases scanned every record and also returned
  non-matching records with score 0.
- BM25 statistics (document count, average length, document frequency) are
  per segment. Scores for the same record can therefore differ slightly
  between segments and shards. This is the same trade-off Lucene makes per
  shard.
- A per-request `ef_search` is ignored on the hybrid path. Dense candidates
  use the collection's configured `ef_search`.
- The inverted indexes are rebuilt from record columns at load time, not
  persisted separately. Load time grows with total text volume.
- gRPC (`Record.text`, `Record.sparse_vector`, `SearchRequest.text`,
  `sparse_vector`, `alpha`, `fusion`), all six SDKs and the dashboard search
  playground expose the same fields.
