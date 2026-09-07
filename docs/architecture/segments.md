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

The checkpoint enumerates only the shard's current live records. Replaced
versions and deletions are therefore physically absent from the new segment.
This is full-shard compaction, not the final multi-segment size-tiered policy.
It bounds WAL replay but has `O(live shard bytes)` write amplification at every
threshold and temporarily holds serialized payload bytes in memory.

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

- Flat shards search one mmap-backed checkpoint base plus one mutable WAL delta;
  active keys and tombstones suppress older immutable values.
- Record metadata/payload remains JSON inside its integrity envelope.
- Vector data is a separate fixed-width column with a validated mmap reader and
  direct flat index used by the flat recovery path.
- HNSW checkpoints load a versioned, checksummed graph; legacy checkpoints
  without one rebuild deterministically from records.
- After a manifest and its referenced columns validate, startup and checkpoint
  publication remove recognized obsolete segment columns, legacy segments,
  graph/filter files, and interrupted atomic-write temporaries. Unknown files are
  preserved, and cleanup errors are reported rather than silently ignored.
