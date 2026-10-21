# MCP server for agents

**Maturity:** Experimental

`gideondb-mcp` is a [Model Context Protocol](https://modelcontextprotocol.io)
server. MCP clients such as Claude Desktop, Claude Code, Cursor and agent SDKs
launch it as a subprocess and talk to it over stdio. It reaches GideonDB
through the public REST API, so it works with any deployment the REST client
can reach.

```bash
go install github.com/Bharanipbk/gideondb/cmd/gideondb-mcp@latest
```

## Tools

Tools are read-only unless you opt in, so an agent cannot change data you did
not mean it to.

| Tool | Needs | What it does |
|---|---|---|
| `list_collections` | — | List collections |
| `describe_collection` | — | Configuration and vector count |
| `search` | — | Search by `vector`, or by `text` when an embedding model is configured; supports `namespace`, `filter`, `ef_search` |
| `get_record` | — | Read one record (vector omitted) |
| `upsert_records` | `-allow-writes` | Up to 1,000 records by `vector` or `text`; text is stored in `payload.text` |
| `delete_record` | `-allow-writes` | Delete one record |
| `create_collection` | `-allow-admin` | Create a collection (cosine, HNSW by default) |
| `delete_collection` | `-allow-admin` | Delete a collection; `confirm` must repeat its name |
| `recall` | embedding model | A user's relevant memories, ranked by similarity blended with recency |
| `remember` | embedding model, `-allow-writes` | Store a memory, optionally with `ttl_seconds` |
| `forget` | embedding model, `-allow-writes` | Delete one memory |
| `prune_memories` | embedding model, `-allow-writes` | Delete a user's expired memories |

Tool failures (bad arguments, missing collections, HTTP errors) come back as
tool results with `isError: true`, so the model can read and correct them.

## Memory API

Memories live in one collection (`agent_memory` by default, created on first
use with the embedding model's dimension). Each `user` is a namespace, so one
user's memories are never returned to another. Every memory stores
`created_at` and `expires_at` (Unix seconds) in metadata and its text in
`payload.text`.

`recall` filters out expired memories, fetches four times `top_k` candidates
and re-ranks them:

```text
score = (1 - recency_weight) × similarity + recency_weight × 0.5^(age_hours / half_life_hours)
```

`recency_weight` defaults to 0.2 and `half_life_hours` to 168 (one week).
Expired memories are hidden immediately; `prune_memories` deletes them.

## Embedding models

`search` and `upsert_records` accept `text` in two ways. If the collection
has a [server-side vectorizer](../indexing/server-side-embedding.md), the MCP
server passes the text through and GideonDB embeds it. Otherwise, configure a
model on `gideondb-mcp` itself; it calls any OpenAI-compatible `/embeddings`
endpoint on the agent's behalf. The memory tools always need this local model:

| Provider | `-embed-url` | Example `-embed-model` |
|---|---|---|
| OpenAI | `https://api.openai.com/v1` | `text-embedding-3-small` |
| Ollama | `http://localhost:11434/v1` | `nomic-embed-text` |
| vLLM, LM Studio, TEI | the server's `/v1` URL | the served model |

Without a local model, text works only on collections with a vectorizer, and
the memory tools are hidden.

## Configuration

| Flag | Environment | Default |
|---|---|---|
| `-url` | `GIDEONDB_URL` | `http://localhost:6333` |
| — | `GIDEONDB_API_KEY` | none |
| `-embed-url` | `EMBEDDING_URL` | none |
| `-embed-model` | `EMBEDDING_MODEL` | none |
| — | `EMBEDDING_API_KEY` | none |
| `-memory-collection` | — | `agent_memory` |
| `-allow-writes`, `-allow-admin` | — | off |

Keys are read only from the environment so they never appear in process
listings.

### Claude Desktop

Add to `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "gideondb": {
      "command": "gideondb-mcp",
      "args": ["-allow-writes", "-embed-url", "http://localhost:11434/v1", "-embed-model", "nomic-embed-text"],
      "env": { "GIDEONDB_URL": "http://localhost:6333", "GIDEONDB_API_KEY": "<your key>" }
    }
  }
}
```

### Claude Code

```bash
claude mcp add gideondb --env GIDEONDB_API_KEY=<your key> -- gideondb-mcp -allow-writes
```

## Limits

- Stdio transport only; Streamable HTTP is not implemented yet.
- Single-node REST endpoints; distributed search and writes are not exposed.
- `prune_memories` scans the user's namespace on the node it talks to.

For framework-native adapters that share this memory layout (LangGraph,
OpenAI Agents, Claude Agent SDK, CrewAI), see [agent frameworks](agents.md).
