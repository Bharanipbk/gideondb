# Named vectors and half-precision storage

**Maturity:** Experimental

A collection always has one default, unnamed vector (`dimension`, `metric`,
`index`). It may also declare up to 16 **named vector fields**, each with its
own dimension, metric, index and storage precision. Records then carry
`vectors: {"title": [...], "body": [...]}` beside `vector`, and a search picks
a field with `using`.

Collections and records that do not use named vectors are unchanged: the API,
WAL, checkpoints, backups and replication behave exactly as before.

## Declare fields

```bash
curl -X POST localhost:6333/v1/collections -H 'Content-Type: application/json' -d '{
  "name": "articles", "dimension": 384, "metric": "cosine", "shard_count": 2,
  "vectors": {
    "title": {"dimension": 384, "metric": "cosine", "index": {"type": "hnsw"}, "datatype": "float16"},
    "image": {"dimension": 512, "metric": "dot", "datatype": "bfloat16"}
  }
}'
```

| Field | Meaning |
| --- | --- |
| `dimension` | 1–65536 |
| `metric` | `cosine`, `dot` or `l2` |
| `index` | `{"type": "flat"}` (default, exact scan) or `{"type": "hnsw", "m", "ef_construction", "ef_search"}`. Quantization is only available on the default vector. |
| `datatype` | `float32` (default), `float16` or `bfloat16` |
| `multi_vector` | `true` for late-interaction fields; see [multi-vector search](multi-vector.md) |

Field names match `^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`. Fields are fixed when the
collection is created, like the rest of the collection config.

## Write records

```json
{"id": "a1", "vector": [...384 values...],
 "vectors": {"title": [...384...], "image": [...512...]},
 "metadata": {"lang": "en"}}
```

The default `vector` remains required. Named fields are optional per record: a
record without `image` is simply not returned by searches over `image`.
Unknown field names, wrong dimensions and non-finite values are rejected with
`400 invalid_argument`. `GET` and scroll return the stored named vectors.

## Search a field

```json
{"using": "title", "vector": [...384...], "top_k": 10,
 "filter": {"lang": "en"}, "ef_search": 128}
```

`using` works on `POST /v1/collections/{name}/search` and on the cluster
coordinator `POST /v1/cluster/collections/{name}/search`, with filters,
namespaces, `offset`, `group_by` and [`rerank`](reranking.md). It cannot be
combined with `sparse_vector` or `text`; hybrid retrieval uses the default
vector. Scores follow the field's metric (larger is better).

## float16 and bfloat16

A half-precision field stores each value in 2 bytes instead of 4:

- **Conversion at write time.** Values are rounded (round-to-nearest-even)
  when the record is prepared, before the WAL append. The WAL, checkpoints,
  replicas and `GET` therefore all hold exactly the value that is searched.
  `float16` keeps ~3 decimal digits with a range of ±65504 (larger values are
  rejected); `bfloat16` keeps float32's range with ~2–3 digits.
- **float32 scoring.** Stored codes are widened to float32 for every
  distance computation; queries are never rounded.
- **Memory.** The per-segment vector store of the field uses half the bytes.
  With `"index": {"type": "flat"}` that store is the only copy, so the field's
  vector memory is halved. An HNSW index on a named field keeps its own
  float32 copy for graph traversal, so it saves only the stored copy.

## How it is stored

Named vectors live in the record column of a checkpoint. A record column that
contains named or multi-vectors is written as **record column version 2**;
columns without them stay version 1, so existing data and collections that do
not use the feature remain readable by older binaries. Older binaries refuse
version 2 instead of silently dropping vectors (see
[compatibility](../operations/compatibility.md)).

On open, per-field indexes are rebuilt from the record column (named-field
HNSW graphs are not yet persisted), so opening a large checkpoint with HNSW
named fields takes longer than with the default index alone.

## Limits and remaining work

- gRPC and the SDKs do not expose `vectors`, `multi_vectors`, `using` or
  `rerank` yet; use REST.
- Half precision applies to named fields only; the default vector stays
  float32 (use binary quantization to shrink it).
- Named-field HNSW graphs are rebuilt on open rather than persisted.
