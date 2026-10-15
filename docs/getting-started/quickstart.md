# Five-minute quickstart

**Maturity:** Experimental

Requirements: [Go 1.26 or newer](https://go.dev/doc/install), `git`, `curl`
and `openssl`. Every command below was run as written on macOS and Linux.

## 1. Start the server

```bash
git clone https://github.com/Bharanipbk/gideondb.git
cd gideondb
mkdir -p ~/.gideondb && openssl rand -hex 24 > ~/.gideondb/api-key && chmod 600 ~/.gideondb/api-key
./run-gideondb.sh -data-path ./data -http-address 127.0.0.1:6333 -api-key-file ~/.gideondb/api-key
```

The REST API always needs a bearer key, even on localhost, because the
administration dashboard is enabled. The launcher creates the dashboard login
`admin` / `admin123` for a new data directory; the dashboard makes you change
it on first sign-in. On Windows, use `run-gideondb.bat` with the same flags.

## 2. Create a collection, insert, search

In a second terminal:

```bash
export GIDEONDB_API_KEY="$(cat ~/.gideondb/api-key)"
curl -fsS http://127.0.0.1:6333/v1/ready

curl -fsS -X POST http://127.0.0.1:6333/v1/collections \
  -H "Authorization: Bearer $GIDEONDB_API_KEY" -H 'content-type: application/json' \
  -d '{"name":"documents","dimension":3,"metric":"cosine","shard_count":4}'

curl -fsS -X POST http://127.0.0.1:6333/v1/collections/documents/vectors/batch \
  -H "Authorization: Bearer $GIDEONDB_API_KEY" -H 'content-type: application/json' \
  -d '{"records":[
        {"id":"doc-1","vector":[1,0,0],"metadata":{"language":"go"}},
        {"id":"doc-2","vector":[0.8,0.2,0],"metadata":{"language":"python"}},
        {"id":"doc-3","vector":[0,0,1],"metadata":{"language":"go"}}]}'

curl -fsS -X POST http://127.0.0.1:6333/v1/collections/documents/search \
  -H "Authorization: Bearer $GIDEONDB_API_KEY" -H 'content-type: application/json' \
  -d '{"vector":[1,0.1,0],"top_k":2}'
```

The search returns `doc-1`, then `doc-2`. Larger scores are always better; L2
scores are negative squared distances.

## 3. Filter by metadata

```bash
curl -fsS -X POST http://127.0.0.1:6333/v1/collections/documents/search \
  -H "Authorization: Bearer $GIDEONDB_API_KEY" -H 'content-type: application/json' \
  -d '{"vector":[1,0.1,0],"top_k":2,"filter":{"language":"go"}}'
```

Only `go` records come back: `doc-1`, then `doc-3`. Filters support `$eq`,
`$ne`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`, `$nin`, `$exists`, `$and`, `$or`
and `$not`; see [metadata filtering](../indexing/filtering.md).

## 4. Look around

- Dashboard: [http://127.0.0.1:6333/dashboard/](http://127.0.0.1:6333/dashboard/)
- Searchable docs: [http://127.0.0.1:6333/docs/](http://127.0.0.1:6333/docs/)
- Metrics: `curl -fsS -H "Authorization: Bearer $GIDEONDB_API_KEY" http://127.0.0.1:6333/metrics`

Stop the server with `Ctrl+C`. Data stays in `./data` for the next run.

## Next steps

- Use real embeddings: [Ollama](../integrations/ollama.md),
  [OpenAI-compatible APIs](../integrations/openai-compatible.md) or
  [Hugging Face](../integrations/huggingface.md).
- Use an SDK: [Go](../sdk/go.md), [Python](../sdk/python.md),
  [TypeScript](../sdk/typescript.md), [Java](../sdk/java.md),
  [Rust](../sdk/rust.md) or [.NET](../sdk/dotnet.md).
- Give an AI agent search and long-term memory with the
  [MCP server](../integrations/mcp.md).
- Tune [HNSW](../indexing/hnsw.md) and plan a rollout with the
  [adoption guide](adoption.md).
