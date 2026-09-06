# OpenAI-compatible semantic-search integration

`OpenAICompatibleEmbedder` connects `SemanticStore` to OpenAI's embeddings API
or a server implementing the same JSON contract, without adding a runtime
dependency. It sends batched string input with float encoding and restores
results to input order using each response item's `index`. See the official
[OpenAI embeddings API](https://developers.openai.com/api/reference/resources/embeddings/methods/create).

The default base URL is `https://api.openai.com/v1`. Custom servers must use
HTTPS, except loopback development endpoints may use HTTP. The adapter requires
Bearer authentication, limits responses to 16 MiB, and rejects missing or
duplicate indices, mismatched batches, inconsistent dimensions, non-numeric
values, and non-finite values.

Create a VectorDB collection whose dimension matches the selected model, then
run:

```sh
export OPENAI_API_KEY=...
export OPENAI_EMBED_MODEL=text-embedding-3-small
# Optional for supported models: export OPENAI_EMBED_DIMENSIONS=1536
# Optional compatible endpoint: export OPENAI_BASE_URL=https://example.com/v1

PYTHONPATH=sdk/python/src:integrations/python \
  python3 examples/openai_compatible_semantic_search.py
```

Use the same model and dimensions for ingestion and queries. Provider inference
and VectorDB writes are separate failure boundaries; use stable record IDs and
retry them deliberately.
