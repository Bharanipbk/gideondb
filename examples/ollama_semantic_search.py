"""Ingest and search text with local Ollama embeddings and VectorDB."""

import os

from vectordb import Client
from vectordb_integrations import Document, OllamaEmbedder, SemanticStore

db = Client(os.getenv("VECTORDB_URL", "http://127.0.0.1:6333"), api_key=os.getenv("VECTORDB_API_KEY", ""))
embedder = OllamaEmbedder(os.getenv("OLLAMA_EMBED_MODEL", "embeddinggemma"))
store = SemanticStore(db, "documents", embedder)

store.upsert_documents([
    Document("quickstart", "VectorDB stores and searches dense vectors.", {"kind": "guide"}),
    Document("durability", "The write-ahead log provides durable recovery.", {"kind": "architecture"}),
])

for result in store.search("How are writes recovered?", 3):
    print(result["id"], result["score"], result.get("payload", {}).get("text", ""))
