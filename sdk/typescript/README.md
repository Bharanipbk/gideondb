# TypeScript SDK

The experimental `@gideondb/client` package is dependency-free and uses the
standard Fetch API available in Node.js 20+ and modern browsers. It supports
collection lifecycle operations, namespaced vector CRUD, filtered search, and
placement-aware distributed search and batch writes.

```js
import { GideonDBClient } from "@gideondb/client";

const client = new GideonDBClient("https://vectors.example.com", {
  apiKey: process.env.GIDEONDB_API_KEY,
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
MiB, and exposes `GideonDBAPIError` and `GideonDBTransportError`. It deliberately
does not retry requests: callers must decide whether a write is safe to replay,
especially when a distributed result reports partial or unknown outcomes.

`scroll` browses records physically present on one node. Its cursor is opaque,
and vectors require an explicit opt-in:

```js
const page = await client.scroll("documents", { limit: 50 });
const next = await client.scroll("documents", {
  limit: 50, cursor: page.next_cursor, includeVector: true,
});
```

Use `distributedScroll` for a placement-aware page across the cluster:

```js
const clusterPage = await client.distributedScroll("documents", { limit: 50 });
```

Run its tests from the repository root:

```sh
make test-typescript-sdk
```

## Hybrid search, quantization and server-side embedding

```js
// Flat index with binary quantization and a server-side vectorizer.
await client.createCollection({
  name: "articles", dimension: 384, metric: "cosine", shard_count: 1,
  vectorizer: "minilm",
  index: { type: "flat", quantization: "binary", oversampling: 4 },
});

// The vector is optional when text is given: the server embeds it and indexes it for BM25.
await client.upsert("articles", { id: "a1", text: "vector databases", sparse_vector: { vector: 1.2 } });

// Text-only search (embedded server-side, fused with BM25).
await client.search("articles", { text: "vector databases", topK: 10 });

// Hybrid dense + lexical with reciprocal rank fusion; alpha is the dense weight (0..1, default 0.5).
await client.search("articles", { vector: embedding, text: "vector databases", alpha: 0.7, fusion: "rrf", topK: 10 });

// Sparse-only search.
await client.search("articles", { sparseVector: { vector: 1.0 }, topK: 10 });
```

`fusion` is `"weighted"` (default) or `"rrf"`. `quantization: "binary"` is only
valid on flat indexes; `oversampling` (1-100, default 4) controls how many
candidates are rescored at full precision.
