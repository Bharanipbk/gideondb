# Agent frameworks

**Maturity:** Experimental

GideonDB gives agents search and long-term memory in two ways:

- **MCP** — run [`gideondb-mcp`](mcp.md) and point any MCP-capable agent at
  it. Nothing to install in the agent's language.
- **Python adapters** in `gideondb_integrations` for frameworks that want a
  native memory object: a LangGraph store, a framework-neutral `MemoryStore`,
  and plain function tools.

All of them use the same memory layout as the MCP server (one collection, a
namespace per user, `created_at`/`expires_at` metadata, text in
`payload.text`), so memories written through one path are recalled through
any other.

Any of these can target a local [`gideondb -lite`](../getting-started/lite.md)
server during development.

## Claude Agent SDK (MCP)

```python
from claude_agent_sdk import ClaudeAgentOptions, query

options = ClaudeAgentOptions(
    mcp_servers={"gideondb": {
        "type": "stdio", "command": "gideondb-mcp",
        "args": ["-allow-writes", "-embed-url", "http://localhost:11434/v1", "-embed-model", "nomic-embed-text"],
        "env": {"GIDEONDB_URL": "http://127.0.0.1:6333"},
    }},
    allowed_tools=["mcp__gideondb__remember", "mcp__gideondb__recall", "mcp__gideondb__search"],
)
async for message in query(prompt="Remember that I live in Lisbon.", options=options):
    print(message)
```

Full example: [`examples/agents/claude_agent_sdk_mcp.py`](../../examples/agents/claude_agent_sdk_mcp.py).
Claude Desktop and Claude Code setup is in the [MCP guide](mcp.md#configuration).

## OpenAI Agents SDK (MCP)

```python
from agents import Agent, Runner
from agents.mcp import MCPServerStdio

async with MCPServerStdio(name="gideondb", params={
    "command": "gideondb-mcp",
    "args": ["-allow-writes", "-embed-url", "https://api.openai.com/v1", "-embed-model", "text-embedding-3-small"],
    "env": {"GIDEONDB_URL": "http://127.0.0.1:6333", "EMBEDDING_API_KEY": os.environ["OPENAI_API_KEY"]},
}) as gideondb:
    agent = Agent(name="Assistant", instructions="...", mcp_servers=[gideondb])
    print((await Runner.run(agent, "How do I take my coffee?")).final_output)
```

Full example: [`examples/agents/openai_agents_mcp.py`](../../examples/agents/openai_agents_mcp.py).

## LangGraph store

`create_langgraph_store` returns a `langgraph.store.base.BaseStore`, so graph
nodes get `store.put`, `store.get`, `store.search` (semantic, with
namespace-prefix and value filters), `store.list_namespaces` and TTLs.

```bash
pip install langgraph
```

```python
from gideondb import Client
from gideondb_integrations import OllamaEmbedder, create_langgraph_store

db = Client("http://127.0.0.1:6333")
db.create_collection({"name": "langgraph_store", "dimension": 768, "metric": "cosine",
                      "shard_count": 1, "index": {"type": "hnsw"}})
store = create_langgraph_store(db, "langgraph_store", OllamaEmbedder("nomic-embed-text"), index_fields=["text"])

store.put(("users", "alice", "memories"), "coffee", {"text": "Alice takes her coffee black."})
store.search(("users", "alice"), query="drink preferences", limit=3)
graph = builder.compile(store=store)
```

- The embedder can be any `gideondb_integrations` embedder or a LangChain
  `Embeddings` object.
- Every item is embedded, because GideonDB records always carry a vector.
  `PutOp.index` (a field list) or `index_fields` picks the text; `"$"` means
  the whole value as JSON. `index=False` is treated like the default.
- Namespaces are stored as metadata, so namespace-prefix search runs in the
  index. Value filters are applied after retrieval (with 4x oversampling);
  they support equality and `$eq`, `$ne`, `$gt`, `$gte`, `$lt`, `$lte`.
- Searches without a query and `list_namespaces` scan the collection. Keep
  one collection per application rather than one shared by many.
- `ttl` (minutes) hides expired items immediately; they are not deleted.
- Tested against LangGraph 1.2.

Example: [`examples/agents/langgraph_memory.py`](../../examples/agents/langgraph_memory.py).

## MemoryStore and function tools (CrewAI, OpenAI Agents, LangChain, others)

`MemoryStore` is the MCP memory API in Python: `remember`, `recall`
(similarity blended with recency), `forget` and `prune`, with the same
defaults (`recency_weight` 0.2, `half_life_hours` 168). It creates the
collection (`agent_memory`, HNSW, cosine) on first use.

`memory_tools(memory, user)` returns typed, documented `remember`, `recall`
and `forget` functions bound to one user, which any framework that builds tools
from Python functions accepts:

```python
from gideondb_integrations import MemoryStore, OllamaEmbedder, memory_tools

memory = MemoryStore(db, OllamaEmbedder("nomic-embed-text"))
remember, recall, forget = memory_tools(memory, user="alice")

# OpenAI Agents:  from agents import function_tool;  tools = [function_tool(f) for f in (remember, recall)]
# CrewAI:         from crewai.tools import tool;       tools = [tool(f.__name__)(f) for f in (remember, recall)]
# LangChain:      StructuredTool.from_function(remember)
```

Example: [`examples/agents/function_tools_memory.py`](../../examples/agents/function_tools_memory.py).
LlamaIndex agents can use the existing [LlamaIndex vector store](llamaindex.md)
or these function tools.

## Tests

`make test-python-integrations` covers the adapters with in-memory fakes and
stub framework modules, so CI needs no framework packages or network. Set
`GIDEONDB_BINARY` to a built `gideondb` to also run the `LiteServer` test
against a real process.
