# Reranking

**Maturity:** Experimental

A search may add a second stage with `rerank`. The first stage (dense, named,
multi-vector, sparse, BM25 or hybrid search, with filters) returns
`candidates` results; the reranker rescores them and the response returns the
best `top_k` (after `offset` and `group_by`) in reranked order.

Two rerankers are built in:

| `type` | Scores with | Runs |
| --- | --- | --- |
| `maxsim` | MaxSim over a [multi-vector field](multi-vector.md) | In process, on the node that stores each record |
| `cross_encoder` | An external rerank model over HTTP | Once, on the node that answers the search |

## Candidate count

`candidates` defaults to 50 (or the reranker's `max_documents`, if smaller) and
is never less than `offset + top_k`. It is capped at 1000, and a cross-encoder
request is also capped by that reranker's `max_documents`.

## MaxSim

```json
{"vector": [...], "top_k": 10,
 "rerank": {"type": "maxsim", "field": "colbert", "multi_vector": [[...], [...]], "candidates": 100}}
```

Result scores become MaxSim scores. Candidates without the multi-vector field
have no comparable score and are dropped. On the cluster endpoint each shard
reranks its own candidates and the coordinator merges by MaxSim score, so no
token vectors cross the network.

## Cross-encoder

Configure rerank endpoints with `-rerankers-file` (or
`GIDEONDB_RERANKERS_FILE`), in the same style as
[server-side embedding](server-side-embedding.md):

```json
{
  "rerankers": [
    {"name": "cohere", "url": "https://api.cohere.com/v2", "model": "rerank-v3.5",
     "api_key_file": "/run/secrets/cohere-key", "timeout_seconds": 3},
    {"name": "jina", "url": "https://api.jina.ai/v1", "model": "jina-reranker-v2-base-multilingual",
     "api_key_file": "/run/secrets/jina-key"},
    {"name": "local", "url": "http://localhost:8080", "model": "BAAI/bge-reranker-v2-m3",
     "max_documents": 100, "max_document_bytes": 4096}
  ]
}
```

GideonDB sends `POST {url}/rerank` with
`{"model", "query", "documents": [...], "top_n"}` and reads
`{"results": [{"index", "relevance_score"}]}` (or `data`), the shape shared by
Cohere, Jina, Voyage, vLLM, Hugging Face TEI and Infinity. Keys are read from
`api_key_file` and never stored in the catalog or returned.

| Setting | Default |
| --- | --- |
| `timeout_seconds` | 5 |
| `max_documents` | 200 candidates per call |
| `max_document_bytes` | 8192 (longer documents are truncated at a UTF-8 boundary) |

```json
{"text": "how do I rotate keys", "top_k": 5,
 "rerank": {"type": "cross_encoder", "model": "cohere", "query": "how do I rotate keys",
            "document": "text", "candidates": 50}}
```

`document` selects the text sent for each candidate: `text` (record text,
the default), `payload.<key>` or `metadata.<key>` (non-string values are sent
as JSON). On the cluster endpoint, shards attach record text to their
candidates and the coordinator calls the model once; text is removed before
results are returned.

## Response and failure behaviour

```json
{"results": [...], "rerank": {"applied": true, "type": "cross_encoder", "model": "cohere", "candidates": 50}}
```

If the cross-encoder fails, times out, or returns a malformed reply, the
search still succeeds: results keep their first-stage order and scores, and
`rerank.applied` is `false` with the reason in `rerank.error`.
`GET /v1/rerankers` lists configured rerankers and counts failed calls. An
unknown reranker name or an over-limit candidate count is a `400` before any
search runs. MaxSim errors (which only arise from invalid input, rejected up
front) fail the request.

## Remaining work

- `rerank` is not exposed over gRPC or in the SDKs yet.
- No in-process cross-encoder (ONNX) runtime.
