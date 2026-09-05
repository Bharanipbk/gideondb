# Java SDK

The experimental Java SDK requires Java 17 and has no third-party runtime
dependencies. It uses the JDK HTTP client and exposes collection lifecycle,
namespaced vector CRUD, filtered search, and placement-aware distributed
operations.

```java
import io.vectordb.client.VectorDBClient;
import java.time.Duration;
import java.util.List;
import java.util.Map;

var client = new VectorDBClient(
    "https://vectors.example.com",
    System.getenv("VECTORDB_API_KEY"),
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

Run strict compilation and behavioral tests from the repository root:

```sh
make test-java-sdk
```
