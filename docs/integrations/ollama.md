# Ollama semantic-search integration

The optional Python integration keeps embedding generation outside VectorDB's
storage core. `SemanticStore` accepts any object implementing the small
`Embedder` protocol, while `OllamaEmbedder` provides a dependency-free adapter
for Ollama's native `POST /api/embed` endpoint.

Ollama documents that the endpoint accepts either one string or an array in
`input` and returns a corresponding `embeddings` array. The adapter uses batch
inputs, verifies response count and vector dimensions, rejects non-finite
values, and limits responses to 16 MiB. See the official [Ollama embedding API](https://docs.ollama.com/api/embed).

Start VectorDB and Ollama, pull an embedding model, create a matching
collection, and run the example:

```sh
ollama pull embeddinggemma

curl -X POST http://127.0.0.1:6333/v1/collections \
  -H 'Content-Type: application/json' \
  -d '{"name":"documents","dimension":768,"metric":"cosine","shard_count":1}'

PYTHONPATH=sdk/python/src:integrations/python \
  python3 examples/ollama_semantic_search.py
```

Confirm the model's actual embedding dimension and use that value when
creating the collection. Indexing and querying must use the same model.

The generic integration surface is deliberately small:

```python
store = SemanticStore(vectordb_client, "documents", my_embedder)
store.upsert_documents([Document("id", "text", {"kind": "guide"})])
results = store.search("recovery behavior", 5, filter={"kind": "guide"})
```

Document text is stored in payload under `text`; metadata remains available
for filters. Provider calls and VectorDB writes are separate operations, so
applications should use stable IDs and retry each boundary deliberately.
