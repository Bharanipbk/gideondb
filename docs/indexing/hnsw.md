# Experimental HNSW index

**Maturity:** Experimental

Create an HNSW-backed collection by supplying an index configuration:

```json
{
  "name": "documents",
  "dimension": 768,
  "metric": "cosine",
  "shard_count": 4,
  "index": {
    "type": "hnsw",
    "m": 16,
    "ef_construction": 100,
    "ef_search": 64
  }
}
```

Defaults are `M=16`, `efConstruction=100`, and `efSearch=64`. `M` is bounded
to 2–128 and both breadth parameters are bounded to protect memory and CPU.

## Current implementation

Each mutable segment owns one graph. Node vectors are row-major contiguous
float32 values. Nodes have deterministic geometrically distributed levels,
which makes snapshot rebuilds and recall tests reproducible. Neighbor lists are
bidirectional and pruned to `M` nearest candidates at each layer. Search greedily
descends upper layers, then performs a bounded best-first layer-zero search.

Construction uses typed min/max heaps and a reusable dense generation-mark
array for visited ordinals. The generation array is protected by the index
write lock and avoids allocating a hash map for every inserted layer;
concurrent searches retain request-local visited state.
Those request-local maps are recycled through a concurrency-safe pool when
small, while maps exceeding 4,096 visited ordinals are discarded to cap
retained scratch capacity.
Cosine traversal also prepares the query norm once and reuses it for every
distance comparison in greedy descent and bounded layer search.

Searches share a read lock and may run concurrently. Inserts and deletes take a
write lock. Deletion uses a tombstone: deleted nodes remain traversable but are
not returned. Replacing a vector rebuilds the mutable graph because its old
edges no longer describe the new geometry. Immutable segments and compaction
will make replacement rebuilds less expensive in the production-storage phase.

## Persistence

The JSON collection catalog persists HNSW configuration and per-shard WALs
persist records, but neither persists graph edges. Startup replays records and
loads immutable checkpoint records before WAL replay, then deterministically
rebuilds the graph. A versioned, checksummed
graph file is required before v0.1.0 and will be specified alongside immutable
segments. Consequently, current startup time scales with graph construction.

## Memory accounting

The index reports live/deleted nodes, vector bytes, directed graph edges,
estimated edge bytes, and maximum level. This is owned-allocation accounting,
not process RSS: slice headers, map buckets and allocator overhead are not yet
included. Heap-profile calibration is required before publishing bytes/vector.

## Quality validation

Flat search is the oracle. The test suite currently requires at least 0.90
recall@10 for a deterministic 1,000-vector, 32-dimensional cosine dataset using
`M=16`, `efConstruction=100`, and `efSearch=100`. This small synthetic test is a
regression guard, not a production recall or scale claim.

## Known limitations

- Graph adjacency currently uses Go slices rather than the planned packed
  immutable representation.
- Construction/search are serialized against mutation by one graph lock.
- No per-query `efSearch` override is exposed through REST.
- Filter-aware traversal is implemented for mutable-segment scalar metadata;
  selectivity-aware planning and persisted filter indexes are not.
- Graph files, compaction rebuild, SIMD and quantization are not implemented.
