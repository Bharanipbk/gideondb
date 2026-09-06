# LlamaIndex integration

`create_llamaindex_vector_store` creates a LlamaIndex-compatible vector store
backed by an existing VectorDB collection. The optional package is imported
lazily, so the base SDK remains dependency-free.

```sh
pip install llama-index-core
```

```python
from llama_index.core import StorageContext, VectorStoreIndex
from vectordb import Client
from vectordb_integrations import create_llamaindex_vector_store

db = Client("http://127.0.0.1:6333", api_key="...")
store = create_llamaindex_vector_store(db, "documents", namespace="tenant-a")
storage = StorageContext.from_defaults(vector_store=store)
index = VectorStoreIndex.from_documents(documents, storage_context=storage)
```

The bridge stores node text, metadata, embeddings, node IDs, and reference
document IDs; it returns nodes, similarities, and IDs from dense queries. Exact
match metadata filters, namespaces, sync/async calls, and reference-document
deletion within the current adapter session are supported. After a process
restart, deletion falls back to treating the reference-document ID as a node
ID; durable reference-document bulk deletion requires a future server API.
Hybrid, sparse, and MMR modes are rejected explicitly.

Collection creation remains explicit so dimensions and distance metrics cannot
silently disagree with the embedding model. The implementation follows
LlamaIndex's current
[`VectorStore` protocol](https://github.com/run-llama/llama_index/blob/main/llama-index-core/llama_index/core/vector_stores/types.py).
