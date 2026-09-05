# Immutable shard checkpoints and compaction

**Maturity:** Experimental

Each shard owns at most one manifest-selected immutable checkpoint segment in
the current implementation:

```text
<data-path>/segments/<collection>/shard-000000/
  MANIFEST.json
  segment-00000000000000001234.records
  segment-00000000000000001234.vectors
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
  Engine->>Segment: write record/vector temps, fsync, rename, fsync directory
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
vector columns, verifies both headers and CRC32C values, requires matching
dimension/count/checkpoint LSN, reconstructs records, and recomputes routing. It
then opens the WAL with the manifest LSN as its replay floor. Missing referenced
files, corrupt columns, mismatches, non-finite vectors, or wrong-shard records
fail startup. Legacy manifest/combined-segment format 1 remains readable.

## Compaction semantics

The checkpoint enumerates only the shard's current live records. Replaced
versions and deletions are therefore physically absent from the new segment.
This is full-shard compaction, not the final multi-segment size-tiered policy.
It bounds WAL replay but has `O(live shard bytes)` write amplification at every
threshold and temporarily holds serialized payload bytes in memory.

## Current limitations

- Flat shards search one mmap-backed checkpoint base plus one mutable WAL delta;
  active keys and tombstones suppress older immutable values.
- Record metadata/payload remains JSON inside its integrity envelope.
- Vector data is a separate fixed-width column with a validated mmap reader and
  direct flat index used by the flat recovery path.
- HNSW indexes rebuild from records during startup.
- No general snapshot pinning or multi-generation reader API.
- Obsolete-file cleanup is best-effort; orphan discovery is not implemented.
