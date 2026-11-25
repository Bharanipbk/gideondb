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
    ..Default::default()
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
    ..Default::default()
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

## Query syntax, highlighting, facets, aggregations and sorting

`CollectionConfig` accepts `text_analyzer` and `synonyms`. `SearchOptions`
adds `syntax` (`"simple"`), `highlight`, `facets`, `aggregations`, `sort` and
`sort_candidates`; only fields you set are sent. `search_response` (and
`distributed_search`) return `facets` and `aggregations` alongside results, and
each `SearchResult` carries `highlights`.

```rust
use gideondb_client::{Aggregation, FacetRequest, HighlightOptions, SearchOptions, SortField};
use std::collections::BTreeMap;

let response = client.search_response("articles", &SearchOptions {
    text: Some("\"vector database\" -draft".into()),
    syntax: Some("simple".into()),
    highlight: Some(HighlightOptions { fragments: Some(2), ..Default::default() }),
    facets: vec![FacetRequest { field: "category".into(), size: Some(5) }],
    aggregations: BTreeMap::from([("price".into(), Aggregation {
        kind: "stats".into(), field: "price".into(), ..Default::default()
    })]),
    sort: vec![SortField { field: "published".into(), order: Some("desc".into()) }],
    top_k: 10,
    ..Default::default()
})?;
println!("{:?} {:?}", response.facets["category"], response.aggregations["price"].avg);
```

## Autocomplete and document ingestion

```rust
use gideondb_client::DocumentOptions;

let suggestions = client.suggest("articles", "vectr datab", 5)?;
println!("{:?} {:?}", suggestions.completions, suggestions.did_you_mean);

let bytes = std::fs::read("guide.pdf").unwrap();
let doc = client.ingest_document("articles", "guide.pdf", &bytes, &DocumentOptions {
    namespace: Some("handbook".into()),
    chunk_size: Some(800),
    content_type: Some("application/pdf".into()),
    ..Default::default()
})?;
println!("{} chunks for {}", doc.chunks, doc.document_id);
```

Run the SDK tests from the repository root:

```sh
make test-rust-sdk
```
