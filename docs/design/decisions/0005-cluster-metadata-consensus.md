# ADR-0005: Use Raft for cluster metadata, not user vector writes

## Status

Accepted

## Context

Nodes need one durable view of schemas, membership, placement, leaders and
epochs without a global bottleneck for data traffic.

## Decision

Use a small Raft group for control-plane metadata. Route vector writes to
independent shard replication groups.

## Alternatives

A global Raft log gives simple ordering but caps write throughput and enlarges
the failure domain. Eventually consistent metadata risks conflicting placement
and split brain.

## Advantages

Strong control-plane ordering with horizontally partitioned data traffic.

## Disadvantages

Two protocols and careful epoch fencing are required; metadata majority loss
blocks topology changes.

## Consequences

Minority partitions cannot elect leaders or alter placement. Existing shards
may serve operations only under documented epoch/lease rules. Per-shard
consensus remains an option if replication testing demands it.

## Implementation status

The fixed-voter metadata group now includes durable terms, votes, logs,
snapshots and committed views; authenticated vote, append and snapshot RPCs;
autonomous elections and quorum-loss demotion; incremental replication;
leader-only proposals; and live epoch activation. Joint-consensus voter changes
are deferred to Phase 8 because membership changes require coordinated shard
movement.
