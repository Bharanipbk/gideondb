# Server-side embedding

**Maturity:** Experimental

A collection can name a **vectorizer**: an embedding model configured on the
server. Clients then send `text` instead of `vector` when they insert records
and when they search, and GideonDB calls the model for them.

## Configure models

Start the server with `-embedders-file` (or `GIDEONDB_EMBEDDERS_FILE`):

```json
{
  "embedders": [
    {
      "name": "local",
      "url": "http://localhost:11434/v1",
      "model": "nomic-embed-text",
      "dimension": 768
    },
    {
      "name": "openai-small",
      "url": "https://api.openai.com/v1",
      "model": "text-embedding-3-small",
      "api_key_file": "/run/secrets/openai-key",
      "dimension": 1536,
      "batch_size": 128,
      "timeout_seconds": 30,
      "cache_entries": 50000
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `name` | Identifier collections use; lowercase letters, digits, `-`, `_` |
| `url` | Base URL of an OpenAI-compatible `/embeddings` API: OpenAI, Azure OpenAI, Ollama, vLLM, LM Studio, Text Embeddings Inference |
| `model` | Model name sent to the API |
| `api_key_file` | Optional file holding the bearer key; keys are never stored in the catalog, WAL or responses |
| `dimension` | Optional; when set, collections must match it and every returned vector is checked |
| `batch_size` | Texts per provider request (default 64) |
| `timeout_seconds` | Per-request timeout (default 30) |
| `cache_entries` | In-memory LRU entries keyed by SHA-256 of model and text (default 10,000) |

Every node that serves a collection must have the same embedder configured.

## Create a collection with a vectorizer

```bash
curl -fsS -X POST http://127.0.0.1:6333/v1/collections \
  -H "Authorization: Bearer $GIDEONDB_API_KEY" -H 'content-type: application/json' \
  -d '{"name":"articles","dimension":768,"metric":"cosine","shard_count":2,"vectorizer":"local"}'
```

Creation fails if the vectorizer is not configured on the node or its
declared dimension differs from the collection's.

## Insert and search by text

```bash
curl -fsS -X POST http://127.0.0.1:6333/v1/collections/articles/vectors/batch \
  -H "Authorization: Bearer $GIDEONDB_API_KEY" -H 'content-type: application/json' \
  -d '{"records":[
        {"id":"a1","text":"HNSW builds a layered proximity graph","metadata":{"topic":"ann"}},
        {"id":"a2","text":"Write-ahead logs make recovery deterministic"}]}'

curl -fsS -X POST http://127.0.0.1:6333/v1/collections/articles/search \
  -H "Authorization: Bearer $GIDEONDB_API_KEY" -H 'content-type: application/json' \
  -d '{"text":"how does graph search work","top_k":3}'
```

- A record with `text` and no `vector` is embedded. A record with both keeps
  its vector; nothing is embedded. Records in one batch may mix the forms.
- A search with `text` and no `vector` embeds the query. With both, the
  given vector is used.
- `text` is also the field that [lexical search](sparse-hybrid.md) indexes
  and queries with BM25 where that is available, so the same text drives
  dense and keyword retrieval.
- The text is stored in `payload.text` unless the record's payload already
  sets `text`.
- Text that is embedded is limited to 64 KiB per record or query.
- `text` also works on `POST /v1/cluster/collections/{name}/search` and
  `/vectors/batch`; the coordinator embeds once before fanning out.
- On a collection without a vectorizer, `text` is passed through untouched;
  a record or dense search without a vector then fails validation as before.

## Errors

| Status | Code | Cause |
|---|---|---|
| 400 | `invalid_argument` | Text to embed exceeds 64 KiB, or no vector and no vectorizer |
| 502 | `embedding_failed` | The model server returned an error or a wrong dimension |
| 503 | `vectorizer_unavailable` | The collection names a model this node does not have |
| 504 | `embedding_timeout` | The model server did not answer in time |

## Cache statistics

`GET /v1/embedders` lists each configured model with its cache hits and
misses. It never returns URLs or keys.

## Clients

The Go client sends text with `client.Record{ID: "a1", Text: "..."}` and
`client.SearchOptions{Text: "...", TopK: 5}`. The
[MCP server](../integrations/mcp.md) passes text through to the server when
it has no embedding model of its own.

## Limits

- Only OpenAI-compatible APIs are built in; Cohere's native API and local
  ONNX models are not.
- The cache is per node and in memory; it is empty after a restart.
- gRPC requests do not accept `text` yet.
