"""Ingest and search text using an OpenAI-compatible embedding endpoint."""

import os

from gideondb import Client
from gideondb_integrations import Document, OpenAICompatibleEmbedder, SemanticStore

dimensions = os.getenv("OPENAI_EMBED_DIMENSIONS", "")
db = Client(os.getenv("GIDEONDB_URL", "http://127.0.0.1:6333"), api_key=os.getenv("GIDEONDB_API_KEY", ""))
embedder = OpenAICompatibleEmbedder(
    os.getenv("OPENAI_EMBED_MODEL", "text-embedding-3-small"),
    os.environ["OPENAI_API_KEY"],
    base_url=os.getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
    dimensions=int(dimensions) if dimensions else None,
)
store = SemanticStore(db, "documents", embedder)

store.upsert_documents([
    Document("quickstart", "GideonDB stores and searches dense vectors.", {"kind": "guide"}),
    Document("durability", "The write-ahead log provides durable recovery.", {"kind": "architecture"}),
])

for result in store.search("How are writes recovered?", 3):
    print(result["id"], result["score"], result.get("payload", {}).get("text", ""))
