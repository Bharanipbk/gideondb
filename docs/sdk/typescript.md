# TypeScript SDK

The experimental `@vectordb/client` package is dependency-free and uses the
standard Fetch API available in Node.js 20+ and modern browsers. It supports
collection lifecycle operations, namespaced vector CRUD, filtered search, and
placement-aware distributed search and batch writes.

```js
import { VectorDBClient } from "@vectordb/client";

const client = new VectorDBClient("https://vectors.example.com", {
  apiKey: process.env.VECTORDB_API_KEY,
});

await client.createCollection({
  name: "documents",
  dimension: 3,
  metric: "cosine",
  shard_count: 4,
});

await client.upsert("documents", {
  id: "doc-1",
  vector: [0.1, 0.2, 0.3],
  metadata: { category: "guide" },
});

const results = await client.search("documents", {
  vector: [0.1, 0.2, 0.3],
  topK: 10,
  filter: { category: "guide" },
});
```

Every call accepts an optional `AbortSignal`. The client also applies a
configurable timeout (30 seconds by default), rejects responses larger than 16
MiB, and exposes `VectorDBAPIError` and `VectorDBTransportError`. It deliberately
does not retry requests: callers must decide whether a write is safe to replay,
especially when a distributed result reports partial or unknown outcomes.

Run its tests from the repository root:

```sh
make test-typescript-sdk
```
