# ADR-0008: Use per-shard WALs with explicit durability modes

## Status

Proposed

## Context

Acknowledged writes need recoverability without forcing unrelated shards
through one log or hiding latency/durability trade-offs.

## Decision

Use checksummed, versioned, segmented WALs per shard with monotonically
increasing LSNs. Expose `always`, bounded group-commit `batch`, and explicitly
lossy `async` modes.

## Alternatives

A global WAL simplifies total ordering but bottlenecks and enlarges recovery.
Relying only on immutable flushes risks large acknowledged-data loss.

## Advantages

Parallel shards, bounded recovery and precise durability selection.

## Disadvantages

No global order, many log files and more group-commit workers/state.

## Consequences

Cross-shard atomicity is unsupported. Corruption before a truncated final tail
fails recovery closed. WAL is discarded only after a durable manifest covers
its LSN. Benchmarks must state the sync mode and storage device.
