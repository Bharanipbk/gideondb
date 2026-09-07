# Immutable shard checkpoints and compaction

**Maturity:** Experimental

Each shard owns at most one manifest-selected immutable checkpoint segment in
the current implementation:

```text
<data-path>/segments/<collection>/shard-000000/
  MANIFEST.json
  segment-00000000000000001234.records
  segment-00000000000000001234.vectors
  segment-00000000000000001234.filter
  segment-00000000000000001234.graph  # HNSW collections only
```

The default checkpoint threshold is 1,000 mutations per shard. Set
`-checkpoint-every 0` to disable automatic checkpoints. The engine also exposes
an internal `Checkpoint(collection)` operation used by recovery tests; an admin
API and CLI will be added with snapshot operations.

## Commit order

```mermaid
sequenceDiagram
  participant Engine
  participant WAL
  participant Segment
  participant Manifest
  Engine->>WAL: sync and capture max LSN
  Engine->>Segment: write record/vector/filter/graph temps, fsync, rename
  Engine->>Manifest: write temp, fsync, rename, fsync directory
  Engine->>WAL: truncate, seek, fsync; next LSN=max+1
  Engine->>Segment: remove obsolete segment after grace point
```

Publishing the manifest before resetting the WAL is the critical invariant. A
crash before manifest publication leaves an unreferenced segment and the full
WAL. A crash after publication but before reset loads the new segment and skips
WAL records at or below its checkpoint LSN. A crash after reset loads the same
segment and begins replay at the next LSN.

## Recovery

For each shard, startup loads and validates `MANIFEST.json`, reads its record and
vector columns and its filter index, verifies headers and CRC32C values, requires
matching dimension/count/checkpoint LSN, reconstructs records, and recomputes
routing. Filter postings and numeric entries are validated before direct
installation. It then opens the WAL with the manifest LSN as its replay floor.
Missing referenced files, corrupt columns, mismatches, non-finite vectors, or
wrong-shard records fail startup. Legacy checkpoints without a filter index and
the combined-segment format 1 remain readable.

## Compaction semantics

Each checkpoint flushes only records changed since the prior generation and
tombstones for deletions from older bundles. Replaced versions coexist until a
selected compaction merges them using newest-operation semantics. This bounds
ordinary flush write amplification while retaining atomic manifest publication
and WAL replay boundaries.

### Size-tiered policy

Manifest format 3 can reference up to 16 immutable bundles
ordered by strictly increasing checkpoint LSN, including record, vector, graph,
filter, and tombstone files. Format-1 and format-2 manifests remain readable.

The checked-in size-tiered planner uses four-segment fan-in, groups inputs whose
sizes are within a 4× range, caps one compaction at 64 MiB, and applies pressure
at eight segments. It returns an explicit error instead of exceeding the byte
bound when pressure cannot produce a valid plan. In a deterministic simulation
of 64 equal unit flushes, the planner wrote 778 units including flushes and
compactions, versus 2,080 units for rewriting the complete live shard after
every flush—a ratio of 0.374. This is algorithmic evidence, not disk or latency
benchmark data.

The production checkpoint writer now emits format-3 delta bundles containing
only active records and persisted base-deletion tombstones. Recovery applies
segments from oldest to newest, so later records replace earlier values and
later tombstones remove them. The writer promotes an existing format-2 bundle
into the first format-3 manifest without rewriting it, executes one selected
bounded plan per flush, and atomically publishes the resulting segment set.

A four-segment benchmark with 1,000 records and 128 dimensions per segment
materialized 2.05 MB of raw vectors in 6.53–6.66 ms at 307–314 MB/s, allocating
about 7.46 MB in 12,178 allocations. Because decoded records and merge maps use
roughly 3.6× the raw vector bytes in this small trial, the initial compaction
input cap was reduced from 256 MiB to 64 MiB. This is local Apple M3 evidence,
not a production capacity claim.

## Pinned readers and generations

`Engine.PinSnapshot(collection)` pins the manifest-backed immutable generation
currently installed for every shard. The returned read-only snapshot exposes
generation IDs, record lookup, ordered record enumeration, and filtered vector
search. A collection must have completed at least one checkpoint before it can
be pinned. Call `Close` when the reader is finished; release is idempotent.

Checkpoint publication installs a new generation without invalidating existing
readers. Retired mmap-backed segments remain open until their last pin is
released. Obsolete files are retained whenever any generation of that shard is
pinned and are reclaimed by a later checkpoint or startup cleanup after all
pins have been released. Collection deletion is rejected while snapshot readers
are pinned. This API provides process-local consistency; it does not establish
a transactionally consistent timestamp across separate cluster nodes.

## Current limitations

- Flat shards use a composite mmap source for format-3 generations. Recovery
  resolves each live key to its newest `(segment, ordinal)` from record columns,
  applies tombstones, and leaves surviving vector payloads in their original
  mapped files.
- Record metadata/payload remains JSON inside its integrity envelope.
- Vector data is a separate fixed-width column with a validated mmap reader and
  direct flat index used by the flat recovery path.
- HNSW checkpoints load a versioned, checksummed full-view graph; legacy
  checkpoints without one rebuild deterministically from records.
- After a manifest and its referenced columns validate, startup and checkpoint
  publication remove recognized obsolete segment columns, legacy segments,
  graph/filter files, and interrupted atomic-write temporaries. Unknown files are
  preserved, and cleanup errors are reported rather than silently ignored.
