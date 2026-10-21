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

Run strict compilation and behavioral tests from the repository root:

```sh
make test-java-sdk
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
