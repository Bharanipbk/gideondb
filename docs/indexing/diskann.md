# DiskANN index

The `diskann` index serves checkpointed segments from a disk-resident
[Vamana](https://proceedings.neurips.cc/paper/2019/hash/09853c7fb1d3f8ee67a61b6bf4a7f8e6-Abstract.html)
graph. Full-precision vectors and graph adjacency stay in a memory-mapped file;
only one byte per dimension of navigation codes is kept on the Go heap. Use it
when a node must hold more vectors than fit in RAM as float32 or as an HNSW
graph.

```json
{
  "name": "docs",
  "dimension": 768,
  "metric": "cosine",
  "shard_count": 4,
  "index": {"type": "diskann", "max_degree": 64, "alpha": 1.2, "ef_construction": 128, "ef_search": 100}
}
```

| Field | Meaning | Values | Default |
|---|---|---|---|
| `max_degree` | Vamana *R*: most neighbours per node | 4–256 | `64` |
| `alpha` | Robust-prune factor; above 1 keeps long-range edges | 1–2 | `1.2` |
| `ef_construction` | Build search list *L* | `max_degree`–10000 | `128` |
| `ef_search` | Default query search list (beam) | 1–10000 | `100` |

`m` and `quantization` are rejected for `diskann`; `max_degree` and `alpha`
are rejected for other index types. A search request's `ef_search` overrides
the collection default for that query, as with HNSW.

## Lifecycle

- **Writes** go to the shard's mutable segment, which uses the exact flat
  index. New records are searchable at once.
- **Checkpoint** builds a Vamana graph over the shard's complete live view and
  writes `segment-<lsn>[-view].diskann` next to the column files. The manifest
  references it in `graph_file`; the collection's index type says how to read
  it. The shard then serves the sealed segment from that file and drops the
  in-memory vectors.
- **Startup** maps the file, verifies its checksum and the record order, and
  derives the navigation codes. Record columns are read without their vectors.
- **Empty shards** have no DiskANN file and are served exactly.

Construction is in memory: it needs the shard's vectors plus the graph
(about `4 × max_degree` bytes per vector) while it runs. Serving does not.

The build is deterministic: nodes are inserted in a fixed order in
prefix-doubling batches (searched in parallel, linked serially), so the same
vectors give the same graph on any number of cores. A final pass makes every
node reachable from the medoid; without it, robust pruning can strip all
in-edges from a far outlier and search would never return it.

## Search

1. The query is compared with 8-bit scalar codes on the heap. A best-first
   beam of `ef_search` candidates starts at the medoid.
2. Each expanded node's record is read from the mapped file: its exact
   score is computed from the stored vector and its adjacency list extends the
   beam. The vector shares a record with the adjacency, so one page read
   serves both.
3. Results are the top `k` expanded nodes by exact score.

Filters, namespaces and tombstones are applied to results, not traversal, so
the graph stays connected. When the metadata filter allows at most
`4 × ef_search` records or 5% of the segment, or the beam finds fewer than `k`
allowed records, the allowed records are scored exactly from the file instead.

Graphs for `dot` collections are built on Euclidean distance and searched by
dot product; they need a wider beam than cosine or L2 for the same recall.

## On-disk format

`.diskann` files are version 1, little-endian and CRC-32C checksummed:

| Part | Contents |
|---|---|
| Header (64 B) | `GDAN`, version, dimension, max degree, count, medoid, metric, record-order tag, payload length, checksum |
| Code ranges | Per-dimension minimum and step for the 8-bit codes |
| Node records | Per node: vector (`4 × dimension` B), degree, `max_degree` neighbour ordinals |

Records are fixed size, so node *i* is at a computed offset. Unknown versions,
reserved bits, checksum failures and files whose record-order tag does not
match the segment's records fail startup.

## Memory and measurements

Per vector, the heap holds `dimension` bytes of codes plus the record map; a
flat or HNSW segment holds `4 × dimension` bytes of vectors (plus the graph
for HNSW). The page cache holds whichever records queries touch.

`gideondb-bench` includes `diskann` and `diskann-ef200`:

```bash
go run ./cmd/gideondb-bench -n 50000 -dimension 128 -configs flat,hnsw,hnsw-ef128,diskann,diskann-ef200
```

Synthetic clustered data, 50,000 × 128, cosine, 200 queries, k = 10, Apple
Silicon (darwin/arm64). The run shared the machine with the test suite, so
treat QPS and build time as rough; heap and recall are stable.

| Config | Recall@10 | QPS | p50 ms | p99 ms | Build s | Segment heap MiB |
|---|---:|---:|---:|---:|---:|---:|
| flat | 1.000 | 9 | 73.770 | 475.053 | 0.09 | 61.4 |
| hnsw | 0.995 | 637 | 0.578 | 17.137 | 73.88 | 82.1 |
| hnsw-ef128 | 1.000 | 2756 | 0.346 | 0.737 | 41.62 | 82.1 |
| diskann | 1.000 | 1462 | 0.581 | 1.869 | 29.02 | 17.0 |
| diskann-ef200 | 1.000 | 1959 | 0.499 | 0.732 | 35.40 | 16.9 |

DiskANN heap is about a fifth of HNSW's; the 25 MiB file is mapped, not heap.
Unit tests on 10,000 × 64 clustered data with `max_degree` 32 reach recall@10
0.99 (cosine) and 0.985 (L2) at `ef_search` 128, and 0.99 (dot) at 256.

## Choosing codes

The task brief proposed reusing the 1-bit codes of
[binary quantization](binary-quantization.md) for navigation. On clustered
data they ranked neighbours too coarsely: recall@10 was 0.37–0.42 at
`ef_search` 128, against 0.98–0.99 with 8-bit codes. The 8-bit codes cost 8×
more heap than 1-bit codes and 4× less than float32.

Product quantization would cut code memory further (for example 32–96 bytes
per vector). To switch once a PQ package exists: train codebooks in
`segment.BuildDiskANN`, store them in place of the code ranges with a new file
version, and replace `navigator`/`navigate` in `internal/index/diskann` with
asymmetric PQ lookups. Version 1 files would still open with 8-bit codes.

## Limits

- Construction is in memory and single-node per shard; there is no streaming
  or incremental graph update. Each checkpoint rebuilds the shard's graph.
- Reads use the OS page cache through mmap, not direct I/O or io_uring.
- The gRPC API has no `diskann` index type yet; create these collections
  over REST.
