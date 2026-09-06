# Phase 1 quickstart

**Maturity:** Experimental

Requirements: Go 1.26 or newer.

Start the server:

```bash
go run ./cmd/gideondb -data-path ./data -http-address 127.0.0.1:6333 \
  -wal-sync always -checkpoint-every 1000
```

Create a collection with four logical shards:

```bash
curl -sS -X POST http://127.0.0.1:6333/v1/collections \
  -H 'content-type: application/json' \
  -d '{"name":"documents","dimension":3,"metric":"cosine","shard_count":4}'
```

Insert a vector:

```bash
curl -sS -X POST http://127.0.0.1:6333/v1/collections/documents/vectors \
  -H 'content-type: application/json' \
  -d '{"id":"doc-1","vector":[1,0,0],"metadata":{"language":"go"}}'
```

Search it:

```bash
curl -sS -X POST http://127.0.0.1:6333/v1/collections/documents/search \
  -H 'content-type: application/json' \
  -d '{"vector":[1,0,0],"top_k":10}'
```

The first result has the highest score. L2 scores are negative squared
distance, making a larger score consistently better for all metrics.

## Current durability warning

Collection configuration is stored in an atomic JSON catalog snapshot. Vector
mutations use checksummed per-shard WALs. After 1,000 mutations to a shard by
default, live records are compacted into an immutable checksummed segment, an
atomic manifest publishes its checkpoint LSN, and the covered WAL is reset.
Group commit, multi-segment size-tiered compaction, mmap, index files, snapshots,
and stable format compatibility are not implemented. Do not use this phase for
production data.
