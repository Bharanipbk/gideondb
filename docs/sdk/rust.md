# Rust SDK

The experimental `vectordb-client` crate provides a synchronous Rust client
for VectorDB's versioned REST API. It supports collection lifecycle operations,
namespaced vector CRUD, filtered search, node-local and cluster-wide cursor
scrolling, and placement-aware distributed search and batch writes.

```rust
use vectordb_client::{Client, CollectionConfig, Record};

let client = Client::new("https://vectors.example.com")?;
client.create_collection(&CollectionConfig {
    name: "documents".into(),
    dimension: 3,
    metric: "cosine".into(),
    shard_count: 4,
    index: None,
})?;

client.upsert("documents", &Record {
    id: "doc-1".into(),
    vector: vec![0.1, 0.2, 0.3],
    metadata: serde_json::Map::new(),
    payload: serde_json::Map::new(),
    timestamp: None,
    version: None,
    namespace: None,
})?;
# Ok::<(), vectordb_client::Error>(())
```

`Client::with_options` accepts a bearer API key and a positive whole-request
timeout. HTTPS uses Rustls and public web PKI roots. Responses are capped at 16
MiB; API failures produce `Error::Api` with status, code, and message, while
transport, validation, and malformed-response failures have distinct variants.
No operation is retried automatically because a failed write can have committed
before the client observes the failure.

The client is blocking. Applications needing cancellation should execute calls
in their runtime's blocking pool and abort the surrounding task; the configured
whole-call timeout remains the hard network bound. Advanced users can inject a
custom `Transport` for private PKI policy, instrumentation, or testing.

Run the SDK tests from the repository root:

```sh
make test-rust-sdk
```
