# Rust SDK

The experimental `gideondb-client` crate provides a synchronous Rust client
for GideonDB's versioned REST API. It supports collection lifecycle operations,
namespaced vector CRUD, filtered search, node-local and cluster-wide cursor
scrolling, and placement-aware distributed search and batch writes.

```rust
use gideondb_client::{Client, CollectionConfig, Record};

let client = Client::new("https://vectors.example.com")?;
client.create_collection(&CollectionConfig {
    name: "documents".into(),
    dimension: 3,
    metric: "cosine".into(),
    shard_count: 4,
    vectorizer: None,
    index: None,
})?;

client.upsert("documents", &Record {
    id: "doc-1".into(),
    vector: vec![0.1, 0.2, 0.3],
    ..Default::default()
})?;
# Ok::<(), gideondb_client::Error>(())
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

## Text, sparse, and hybrid search

`Record::text` is indexed for BM25 and embedded server-side when the collection
has a `vectorizer` and no vector is given; `sparse_vector` holds positive term
weights. `SearchOptions` accepts `text`, `sparse_vector`, `alpha` (dense weight
0..1, default 0.5) and `fusion` (`"weighted"` default or `"rrf"`); text-only and
sparse-only searches omit the vector. Binary quantization is a flat-index
option. All option structs implement `Default`.

```rust
use gideondb_client::{CollectionConfig, IndexConfig, Record, SearchOptions};
use std::collections::BTreeMap;

client.create_collection(&CollectionConfig {
    name: "articles".into(),
    dimension: 384,
    metric: "cosine".into(),
    shard_count: 1,
    vectorizer: Some("all-minilm-l6-v2".into()),
    index: Some(IndexConfig {
        kind: "flat".into(),
        quantization: Some("binary".into()),
        oversampling: Some(4),
        ..Default::default()
    }),
})?;
client.upsert("articles", &Record {
    id: "a1".into(),
    text: Some("vector databases explained".into()),
    sparse_vector: BTreeMap::from([("vector".into(), 1.2), ("database".into(), 0.8)]),
    ..Default::default()
})?;
let hits = client.search("articles", &SearchOptions {
    text: Some("vector database".into()),
    alpha: Some(0.7),
    fusion: Some("rrf".into()),
    top_k: 5,
    ..Default::default()
})?;
```

Run the SDK tests from the repository root:

```sh
make test-rust-sdk
```
