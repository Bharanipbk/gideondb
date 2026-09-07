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

The JSON collection catalog persists HNSW configuration, per-shard WALs persist
mutations, and every checkpoint publishes a versioned CRC32C-protected graph
file alongside its record and vector columns. Recovery validates the graph's
configuration, record order, entry point, levels, and neighbor ordinals before
installing it, then applies only WAL mutations newer than the checkpoint. Older
checkpoints without a graph remain readable through deterministic rebuilding.

## Memory accounting

The index reports live/deleted nodes, vector bytes, directed graph edges,
structural edge bytes, and maximum level. This is a deterministic structural
lower bound, not Go heap usage or process RSS. For recovered immutable graphs,
graph bytes include the packed neighbor and per-layer offset backing arrays.
Slice headers, map buckets, spare capacity, and allocator overhead are not
included. A reproducible heap calibration measured the expected gap, but its
result is environment- and workload-specific; run the calibration command with
representative dimensions and HNSW settings before capacity planning.

## Quality validation

Flat search is the oracle. The test suite currently requires at least 0.90
recall@10 for a deterministic 1,000-vector, 32-dimensional cosine dataset using
`M=16`, `efConstruction=100`, and `efSearch=100`. This small synthetic test is a
regression guard, not a production recall or scale claim.

## Known limitations

- Mutable graph construction uses editable per-node slices. Checkpoint recovery
  loads adjacency into a contiguous packed neighbor array with per-node layer
  offsets and rejects mutation of that immutable graph.
- Construction/search are serialized against mutation by one graph lock.
- REST and the Go client accept a per-query `ef_search` from 1 through 10,000;
  omission uses the collection default.
- Selective metadata bitmaps use an exact allowed-set scan. New checkpoints
  restore versioned, checksummed filter indexes directly; legacy checkpoints
  rebuild them from record metadata.
- Graph persistence, checkpoint rebuild, and packed immutable recovery are
  implemented. Architecture-specific SIMD remains deferred. Symmetric int8 was
  evaluated but is not a storage or search option because its portable scoring
  latency regressed despite strong synthetic recall and lower payload size.
