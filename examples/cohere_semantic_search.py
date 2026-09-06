"""Ingest and search text using Cohere retrieval embeddings."""

import os

from gideondb import Client
from gideondb_integrations import CohereEmbedder, Document, SemanticStore

dimension = os.getenv("COHERE_EMBED_DIMENSION", "")
db = Client(os.getenv("GIDEONDB_URL", "http://127.0.0.1:6333"), api_key=os.getenv("GIDEONDB_API_KEY", ""))
embedder = CohereEmbedder(
    os.getenv("COHERE_EMBED_MODEL", "embed-v4.0"),
    os.environ["COHERE_API_KEY"],
    output_dimension=int(dimension) if dimension else None,
)
store = SemanticStore(db, "documents", embedder)

store.upsert_documents([
    Document("quickstart", "GideonDB stores and searches dense vectors.", {"kind": "guide"}),
    Document("durability", "The write-ahead log provides durable recovery.", {"kind": "architecture"}),
])

for result in store.search("How are writes recovered?", 3):
    print(result["id"], result["score"], result.get("payload", {}).get("text", ""))
