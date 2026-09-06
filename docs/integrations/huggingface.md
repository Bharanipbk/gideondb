# Hugging Face semantic-search integration

`HuggingFaceEmbedder` connects the provider-neutral `SemanticStore` helper to
Hugging Face's feature-extraction inference task without adding a runtime
dependency. Hugging Face documents batch string `inputs`, optional
normalization and truncation, and an array-of-arrays response for this task.
See the official [feature extraction API](https://huggingface.co/docs/inference-providers/tasks/feature-extraction).

The adapter uses the current HF Inference router by default and accepts a
custom HTTPS `endpoint_url` for dedicated Inference Endpoints. It requires a
Bearer token with inference permission, limits responses to 16 MiB, and rejects
mismatched batches, token-level tensors, inconsistent dimensions, non-numeric
values, and non-finite values.

Create a VectorDB collection whose dimension matches the selected model, then
run:

```sh
export HF_TOKEN=hf_...
export HF_EMBED_MODEL=sentence-transformers/all-MiniLM-L6-v2

PYTHONPATH=sdk/python/src:integrations/python \
  python3 examples/huggingface_semantic_search.py
```

The model used to ingest documents must also be used for queries. Provider
inference and VectorDB writes are separate failure boundaries; use stable
record IDs and retry them deliberately.
