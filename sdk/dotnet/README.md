# .NET SDK

The experimental `GideonDB.Client` library targets .NET 8 and provides an
asynchronous, dependency-free client for GideonDB's versioned REST API. It
supports collection lifecycle operations, namespaced vector CRUD, batches,
filtered search, node-local and cluster-wide scrolling, distributed search,
and placement-aware distributed writes.

```csharp
using GideonDB.Client;

using var client = new GideonDBClient(
    "https://vectors.example.com",
    Environment.GetEnvironmentVariable("GIDEONDB_API_KEY"));

await client.CreateCollectionAsync(new CollectionConfig
{
    Name = "documents",
    Dimension = 3,
    Metric = "cosine",
    ShardCount = 4
});

await client.UpsertAsync("documents", new VectorRecord
{
    Id = "doc-1",
    Vector = new[] { 0.1f, 0.2f, 0.3f }
});

var results = await client.SearchAsync("documents", new SearchOptions
{
    Vector = new[] { 0.1f, 0.2f, 0.3f },
    TopK = 10
});
```

Every operation accepts a `CancellationToken`. The client combines caller
cancellation with a configurable 30-second default timeout, requests response
headers before streaming the body, and rejects responses larger than 16 MiB.
It exposes separate API, transport, malformed-response, and response-size
exceptions. API exceptions retain the HTTP status and stable server error code.

The client deliberately performs no automatic retries because a write can
commit before the caller observes a transport failure. A caller-owned
`HttpClient` can be injected for private certificate authorities, proxies,
instrumentation, testing, or application-wide connection pooling.

## Text, sparse, and hybrid search

`VectorRecord.Text` is indexed for BM25 and embedded server-side when the
collection has a `Vectorizer` and no vector is given; `SparseVector` holds
positive term weights. `SearchOptions` accepts `Text`, `SparseVector`, `Alpha`
(dense weight 0..1, default 0.5) and `Fusion` (`"weighted"` default or `"rrf"`);
text-only and sparse-only searches omit the vector. Binary quantization is a
flat-index option.

```csharp
await client.CreateCollectionAsync(new CollectionConfig
{
    Name = "articles", Dimension = 384, Metric = "cosine", ShardCount = 1,
    Vectorizer = "all-minilm-l6-v2",
    Index = new IndexConfig { Type = "flat", Quantization = "binary", Oversampling = 4 }
});

await client.UpsertAsync("articles", new VectorRecord
{
    Id = "a1", Text = "vector databases explained",
    SparseVector = new Dictionary<string, float> { ["vector"] = 1.2f, ["database"] = 0.8f }
});

var hits = await client.SearchAsync("articles", new SearchOptions
{
    Text = "vector database", Alpha = 0.7, Fusion = "rrf", TopK = 5
});
```

Run the dependency-free contract tests from the repository root:

```sh
make test-dotnet-sdk
```
