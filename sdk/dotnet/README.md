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

## Query syntax, highlighting, facets, aggregations and sorting

Collections accept a `TextAnalyzer` (for example `"english"` or `"cjk"`) and
`Synonyms`. `SearchOptions` adds `Syntax = "simple"` (phrases, `+must`,
`-must_not`, `fuzzy~`, `prefix*`, `^boost`), `Highlight`, `Facets`,
`Aggregations`, `Sort` and `SortCandidates`; only the fields you set are sent.
`SearchWithResponseAsync`, `SearchFacetsAsync` and `SearchAggregateAsync`
return a `SearchResponse` with `Facets` and `Aggregations`;
`DistributedSearchResponse` exposes the same. Each `SearchResult` carries
`Highlights` when requested.

```csharp
await client.CreateCollectionAsync(new CollectionConfig
{
    Name = "articles", Dimension = 384, Metric = "cosine", ShardCount = 1,
    TextAnalyzer = "english", Synonyms = new[] { new[] { "car", "auto" } }
});

var response = await client.SearchWithResponseAsync("articles", new SearchOptions
{
    Text = "\"vector database\" -draft", Syntax = "simple", TopK = 10,
    Highlight = new HighlightOptions { PreTag = "<b>", PostTag = "</b>" },
    Facets = new[] { new FacetRequest { Field = "lang", Size = 5 } },
    Aggregations = new Dictionary<string, Aggregation>
    {
        ["price"] = new Aggregation { Type = "stats", Field = "price" }
    },
    Sort = new[] { new SortField { Field = "price", Order = "asc" } }
});
var langs = response.Facets!["lang"];
var avg = response.Aggregations!["price"].Avg;
```

## Autocomplete and document ingestion

```csharp
var suggestions = await client.SuggestAsync("articles", "vectr datab", size: 5);
Console.WriteLine(suggestions.DidYouMean);

var document = await client.IngestDocumentAsync("articles", "guide.pdf",
    await File.ReadAllBytesAsync("guide.pdf"),
    new DocumentOptions { Id = "guide", ChunkSize = 800, Metadata = new Dictionary<string, object?> { ["lang"] = "en" } });
Console.WriteLine($"{document.DocumentId}: {document.Chunks} chunks");
```

A `Stream` overload of `IngestDocumentAsync` leaves the caller's stream open.

Run the dependency-free contract tests from the repository root:

```sh
make test-dotnet-sdk
```
