# ADR-0006: Use leader-based per-shard replication

## Status

Proposed

## Context

Replicas need ordered updates, explicit acknowledgement levels and a repair
path without global serialization.

## Decision

One epoch-fenced leader assigns per-shard LSNs and ships ordered WAL entries to
followers. Support ONE, QUORUM and ALL durable acknowledgement semantics.

## Alternatives

Leaderless quorums improve write availability but require conflict resolution
for vectors, metadata and deletes. Chain replication complicates topology
changes. Global consensus is rejected by ADR-0005.

## Advantages

Simple per-shard order, efficient batching and clear lag/snapshot repair.

## Disadvantages

Leader failover pauses writes and hot leaders can bottleneck. Precise safety
depends on election and commit rules.

## Consequences

The project must not claim strong consistency until partition and history tests
validate the implementation. Stale epochs are rejected. Lost quorum makes
QUORUM/ALL writes unavailable rather than weakening acknowledgement silently.
