# Python SDK

**Maturity:** Experimental

The synchronous Python 3.10+ client under `sdk/python` uses only the standard
library. It supports health/readiness, collection lifecycle, namespaced vector
CRUD and batches, filtered search, and placement-aware distributed search and
writes.

```python
import os
from gideondb import Client

db = Client(
    "https://gideondb.example.com",
    api_key=os.environ["GIDEONDB_API_KEY"],
    timeout=5,
)

db.create_collection({
    "name": "docs",
    "dimension": 768,
    "metric": "cosine",
    "shard_count": 16,
})

db.upsert("docs", {"id": "doc-1", "vector": embedding})
results = db.search("docs", embedding, 10)
```

For private CAs, build an `ssl.SSLContext` with
`ssl.create_default_context(cafile=...)` and pass it as `ssl_context`. The
default request timeout is 30 seconds and every response is limited to 16 MiB.
An injectable `opener` is available for transport integration and tests.

Non-2xx responses raise `APIError` with `status_code`, `code`, and `message`.
Network, TLS, oversized-response, and invalid-JSON failures raise
`TransportError`. The client performs no automatic retries because timed-out
and cross-shard writes may already be durable.

For static-routing clusters, use `distributed_search` and
`distributed_batch_upsert`. A decoded HTTP 207 is returned normally; callers
must inspect `partial` and every shard outcome. Partial search remains opt-in
with `allow_partial=True`.

Node-local record browsing returns an opaque continuation cursor and omits
vectors unless explicitly requested:

```python
page = db.scroll("documents", limit=50)
next_page = db.scroll(
    "documents", limit=50, cursor=page["next_cursor"], include_vector=True
)
```

`distributed_scroll` accepts the same arguments and traverses one authoritative
owner per shard. Its response also includes `metadata_epoch` and
`authoritative_placement`:

```python
page = db.distributed_scroll("documents", limit=50)
```

Run the SDK tests from the repository root:

```bash
make test-python-sdk
```

## Hybrid search, quantization and server-side embedding

```python
# Flat index with binary quantization and a server-side vectorizer.
db.create_collection({
    "name": "articles", "dimension": 384, "metric": "cosine", "shard_count": 1,
    "vectorizer": "minilm",
    "index": {"type": "flat", "quantization": "binary", "oversampling": 4},
})

# The vector is optional when text is given: the server embeds it and indexes it for BM25.
db.upsert("articles", {"id": "a1", "text": "vector databases", "sparse_vector": {"vector": 1.2}})

# Text-only search (embedded server-side, fused with BM25).
db.search("articles", None, 10, text="vector databases")

# Hybrid dense + lexical with reciprocal rank fusion; alpha is the dense weight (0..1, default 0.5).
db.search("articles", embedding, 10, text="vector databases", alpha=0.7, fusion="rrf")

# Sparse-only search.
db.search("articles", None, 10, sparse_vector={"vector": 1.0})
```

`fusion` is `"weighted"` (default) or `"rrf"`. `quantization: "binary"` is only
valid on flat indexes; `oversampling` (1-100, default 4) controls how many
candidates are rescored at full precision.
