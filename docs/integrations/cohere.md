# Cohere semantic-search integration

`CohereEmbedder` connects `SemanticStore` to Cohere's v2 Embed API without an
additional runtime dependency. It uses `search_document` for stored records and
`search_query` for queries, as recommended by Cohere's
[semantic-search guide](https://docs.cohere.com/v2/docs/sem-search-quickstart).

The adapter sends at most 96 texts per request, requests float embeddings,
supports the documented v4 output dimensions, limits responses to 16 MiB, and
rejects mismatched batches, inconsistent dimensions, and invalid numeric data.
Custom servers require HTTPS, except for loopback development endpoints.

Create a GideonDB collection whose dimension matches the model, then run:

```sh
export COHERE_API_KEY=...
export COHERE_EMBED_MODEL=embed-v4.0
# Optional: export COHERE_EMBED_DIMENSION=1536

PYTHONPATH=sdk/python/src:integrations/python \
  python3 examples/cohere_semantic_search.py
```

Provider inference and GideonDB writes are separate failure boundaries. Use
stable record IDs and retry failed operations deliberately.
