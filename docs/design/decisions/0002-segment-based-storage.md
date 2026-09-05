# ADR-0002: Use immutable segment-based storage

## Status

Proposed

## Context

Reads, writes, snapshots and compaction must coexist without a global lock, and
recovery needs bounded, inspectable units.

## Decision

Each logical shard owns one active segment and a manifest-selected set of
largely immutable segments. Tombstones and replacement segments are committed
through atomic manifest changes.

## Alternatives

In-place page updates reduce compaction but complicate crash atomicity and
concurrent index maintenance. One monolithic append log simplifies writing but
makes search and reclamation expensive.

## Advantages

Stable reader snapshots, safe file transfer, incremental recovery, independent
index rebuild and bounded compaction inputs.

## Disadvantages

Write and space amplification, duplicate versions until compaction, and more
manifest logic.

## Consequences

Compaction must be resource-bounded. A corrupt required segment fails the shard
closed. Future replication and migration can transfer immutable files and catch
up from WAL without changing segment ownership.
