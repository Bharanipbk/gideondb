"""LangGraph long-term memory backed by GideonDB.

    pip install langgraph
    gideondb -lite &                       # or any GideonDB URL
    python examples/agents/langgraph_memory.py
"""

import os

from gideondb import Client
from gideondb_integrations import OllamaEmbedder, create_langgraph_store

db = Client(os.getenv("GIDEONDB_URL", "http://127.0.0.1:6333"), api_key=os.getenv("GIDEONDB_API_KEY", ""))
embedder = OllamaEmbedder(os.getenv("OLLAMA_EMBED_MODEL", "nomic-embed-text"))
dimension = len(embedder.embed(["probe"])[0])
try:
    db.create_collection({"name": "langgraph_store", "dimension": dimension, "metric": "cosine", "shard_count": 1, "index": {"type": "hnsw"}})
except Exception as error:  # already exists
    if getattr(error, "status_code", None) != 409:
        raise

store = create_langgraph_store(db, "langgraph_store", embedder, index_fields=["text"])
store.put(("users", "alice", "memories"), "coffee", {"text": "Alice takes her coffee black."})
store.put(("users", "alice", "memories"), "city", {"text": "Alice lives in Lisbon."})

for item in store.search(("users", "alice"), query="How does Alice like her drinks?", limit=1):
    print(item.key, round(item.score, 3), item.value["text"])

# Pass the store to a graph so nodes can read and write memories:
#   graph = builder.compile(store=store)
