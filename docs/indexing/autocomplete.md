# Autocomplete and "did you mean"

`GET /v1/collections/{name}/suggest?text=...&size=5` helps users while they
type:

```bash
curl -fsS -H "Authorization: Bearer $KEY" "http://127.0.0.1:6333/v1/collections/docs/suggest?text=fast+runn"
```

```json
{"completions": [{"text": "fast running", "word": "running", "count": 42},
                 {"text": "fast runner", "word": "runner", "count": 7}]}
```

```bash
curl -fsS -H "Authorization: Bearer $KEY" "http://127.0.0.1:6333/v1/collections/docs/suggest?text=runing+shoez+"
```

```json
{"completions": [], "did_you_mean": "running shoes "}
```

- **Completions:** the last word is completed unless the text ends in a space.
  Completions come from words in record text, ordered by how many documents
  contain them. `text` is the input with the last word completed.
- **Corrections:** `did_you_mean` replaces each word that isn't indexed with the
  most common indexed word within 1 edit (2 edits for 6+ letters). It is
  omitted when nothing needs correcting. A partial last word that has
  completions is not treated as a typo.
- **Word normalization:** words are lowercased and accent-folded like the
  collection analyzer, but they aren't stemmed, so suggestions are real
  words. Stopwords and tokens longer than 40 bytes are not suggested.
- **Limits:** `size` is 1–50 (default 5). `text` is at most 1,024 bytes. The
  endpoint is a GET, so reader principals can call it.
- **Scope:** counts are per node and include all namespaces. Records that were
  overwritten but are not yet checkpointed may still add to counts. Treat
  suggestions as hints.

This is also available over [gRPC](../api/grpc.md#search-features).
