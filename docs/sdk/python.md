# Python SDK

**Maturity:** Experimental

The synchronous Python 3.10+ client under `sdk/python` uses only the standard
library. It supports health/readiness, collection lifecycle, namespaced vector
CRUD and batches, filtered search, and placement-aware distributed search and
writes.

```python
from vectordb import Client

db = Client(
    "https://vectordb.example.com",
    api_key=os.environ["VECTORDB_API_KEY"],
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

Run the SDK tests from the repository root:

```bash
make test-python-sdk
```

