# Java SDK

The experimental Java SDK requires Java 17 and has no third-party runtime
dependencies. It uses the JDK HTTP client and exposes collection lifecycle,
namespaced vector CRUD, filtered search, and placement-aware distributed
operations.

```java
import io.gideondb.client.GideonDBClient;
import java.time.Duration;
import java.util.List;
import java.util.Map;

var client = new GideonDBClient(
    "https://vectors.example.com",
    System.getenv("GIDEONDB_API_KEY"),
    Duration.ofSeconds(30));

client.createCollection(Map.of(
    "name", "documents",
    "dimension", 3,
    "metric", "cosine",
    "shard_count", 4));

client.upsert("documents", Map.of(
    "id", "doc-1",
    "vector", List.of(0.1, 0.2, 0.3),
    "metadata", Map.of("category", "guide")));

var results = client.search(
    "documents", List.of(0.1, 0.2, 0.3), 10,
    Map.of("category", "guide"), "");
```

Responses are limited to 16 MiB. HTTP failures raise `ApiException`, while
network, interruption, malformed-response, and response-limit failures raise
`TransportException`. The client never retries implicitly because distributed
writes can return partial or unknown outcomes.

Node-local browsing accepts an opaque cursor and keeps vectors redacted unless
the final argument is `true`:

```java
var page = client.scroll("documents", "", 50, "", false);
var next = client.scroll(
    "documents", "", 50, (String) page.get("next_cursor"), true);
```

Use `distributedScroll` with the same arguments for placement-aware traversal:

```java
var clusterPage = client.distributedScroll("documents", "", 50, "", false);
```

## Text, sparse, and hybrid search

Records may carry `text` (indexed for BM25 and embedded server-side when the
collection has a `vectorizer` and no vector is given) and a `sparse_vector` of
positive term weights. `SearchOptions` builds dense, text-only, sparse-only, or
hybrid queries; `alpha` (0..1, default 0.5) weights the dense side and `fusion`
is `"weighted"` (default) or `"rrf"`. Binary quantization is a flat-index option.

```java
client.createCollection(Map.of(
    "name", "articles", "dimension", 384, "metric", "cosine",
    "vectorizer", "all-minilm-l6-v2",
    "index", Map.of("type", "flat", "quantization", "binary", "oversampling", 4)));

client.upsert("articles", Map.of(
    "id", "a1", "text", "vector databases explained",
    "sparse_vector", Map.of("vector", 1.2, "database", 0.8)));

var hits = client.search("articles", new GideonDBClient.SearchOptions()
    .text("vector database").alpha(0.7).fusion("rrf").topK(5));
```

## Query syntax, highlighting, facets, aggregations and sorting

`createCollection` passes `text_analyzer` and `synonyms` straight through.
`searchResponse` returns the whole body (`results`, `facets`, `aggregations`);
`distributedSearch(collection, options, allowPartial)` accepts the same options.
Each result carries `highlights` when `highlight` is set.

```java
client.createCollection(Map.of("name", "shop", "dimension", 384,
    "text_analyzer", "english", "synonyms", List.of(List.of("tv", "television"))));

var response = client.searchResponse("shop", new GideonDBClient.SearchOptions()
    .text("\"smart tv\" -refurbished").syntax("simple").topK(10)
    .highlight(new GideonDBClient.HighlightOptions().preTag("<b>").postTag("</b>"))
    .facet("brand", 5)
    .aggregation("price", GideonDBClient.Aggregation.stats("price"))
    .aggregation("bands", GideonDBClient.Aggregation.range("price")
        .addRange("cheap", null, 100.0).addRange("premium", 100.0, null))
    .sort("price", "asc").sortCandidates(500));
var brands = GideonDBClient.facets(response).get("brand");
var stats = GideonDBClient.aggregations(response).get("price");
```

## Autocomplete and document ingestion

```java
var s = client.suggest("shop", "smart telev", 5);
var completions = GideonDBClient.completions(s);   // [{text, word, count}]
var fix = s.get("did_you_mean");                   // may be null

var doc = client.ingestDocument("articles", "guide.pdf",
    java.nio.file.Files.readAllBytes(java.nio.file.Path.of("guide.pdf")),
    new GideonDBClient.DocumentOptions().namespace("kb")
        .metadata(Map.of("team", "docs")).chunkSize(800).chunkOverlap(100)
        .contentType("application/pdf"));
// doc: document_id, title, format, chunks, characters, replaced_chunks_removed
```

Run strict compilation and behavioral tests from the repository root:

```sh
make test-java-sdk
```
