"""Give any function-calling agent framework GideonDB memory without MCP.

memory_tools() returns plain typed Python functions. Wrap them with your
framework's decorator, for example:

    OpenAI Agents:  from agents import function_tool;  tools = [function_tool(f) for f in fns]
    CrewAI:         from crewai.tools import tool;       tools = [tool(f.__name__)(f) for f in fns]
    LangChain:      from langchain_core.tools import StructuredTool
                    tools = [StructuredTool.from_function(f) for f in fns]

The memories use the same layout as gideondb-mcp, so both see the same data.
"""

import os

from gideondb import Client
from gideondb_integrations import MemoryStore, OllamaEmbedder, memory_tools

db = Client(os.getenv("GIDEONDB_URL", "http://127.0.0.1:6333"), api_key=os.getenv("GIDEONDB_API_KEY", ""))
memory = MemoryStore(db, OllamaEmbedder(os.getenv("OLLAMA_EMBED_MODEL", "nomic-embed-text")))
remember, recall, forget = memory_tools(memory, user="alice")

remember("Alice is allergic to peanuts.")
remember("Alice is visiting Kyoto next week.", ttl_seconds=14 * 24 * 3600)
print(recall("Any food restrictions?", top_k=1))
