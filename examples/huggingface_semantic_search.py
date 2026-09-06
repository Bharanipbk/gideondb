"""Ingest and search text using Hugging Face feature extraction."""

import os

from gideondb import Client
from gideondb_integrations import Document, HuggingFaceEmbedder, SemanticStore

db = Client(os.getenv("GIDEONDB_URL", "http://127.0.0.1:6333"), api_key=os.getenv("GIDEONDB_API_KEY", ""))
embedder = HuggingFaceEmbedder(
    os.getenv("HF_EMBED_MODEL", "sentence-transformers/all-MiniLM-L6-v2"),
    os.environ["HF_TOKEN"],
)
store = SemanticStore(db, "documents", embedder)

store.upsert_documents([
    Document("quickstart", "GideonDB stores and searches dense vectors.", {"kind": "guide"}),
    Document("durability", "The write-ahead log provides durable recovery.", {"kind": "architecture"}),
])

for result in store.search("How are writes recovered?", 3):
    print(result["id"], result["score"], result.get("payload", {}).get("text", ""))
