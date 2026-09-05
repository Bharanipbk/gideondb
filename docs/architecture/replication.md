# Replication architecture

## Status

Phase 7 is in progress. Replica placement, ownership, ordered follower append,
leader fanout, quorum acknowledgement, and snapshot catch-up are implemented.

## Replica-set placement

`PlanReplicaPlacement` ranks every eligible node independently for each logical
shard using the same deterministic rendezvous score as primary placement. The
highest-scoring distinct nodes form the replica set, and the first is the
initial leader. Canonical node and collection ordering makes the result
independent of discovery response order.

The requested replication factor must be at least one and cannot exceed the
number of known nodes. Health does not silently remove a replica because doing
so would change durability without a committed metadata transition.

`GET /v1/cluster/replicas?replication_factor=N` exposes the plan for inspection.
It reports `authoritative: false` until replica snapshot catch-up and placement
transitions are consensus-backed.

## Replicated write path

The engine now accepts leader-prepared replica batches at an exact per-shard WAL
sequence. It validates shard routing, vectors, versions, and timestamps; appends
the canonical batch to the existing shard WAL before visibility; and restores
the leader's original version metadata. The WAL sequence is the replication
sequence.

Retries within the retained WAL compare a SHA-256 payload fingerprint. An
identical retry succeeds without another write, a different payload at the same
sequence returns `replication_conflict`, a future sequence returns
`replication_gap`, and a retry older than the checkpoint boundary returns
`replication_sequence_compacted` so the leader can initiate snapshot recovery.
Recovery rebuilds retained fingerprints while replaying the normal WAL.

`POST /v1/internal/replicas/{collection}/{shard}/append` additionally fences the
cluster, target node, metadata epoch, leader node, and leader term. It verifies
that the sender is the deterministic shard leader and that the receiver belongs
to the requested replica set before reaching storage.

Static activation now materializes every local primary or follower shard from
the configured replica table. Nodes advertise their replication factor and
fail readiness when a healthy peer reports a different value. Factor one keeps
the previous single-primary ownership behavior.

All local and remotely coordinated shard writes enter the same leader path. The
leader durably appends and prepares the batch, then concurrently sends that
exact WAL sequence and prepared record metadata to every follower. Leader
operations are serialized through fanout so followers cannot observe sequence
N+1 before N.

A shard outcome is `committed` only after `floor(replication_factor/2)+1`
replicas acknowledge durable append. Responses expose `replicas_acknowledged`
and `replication_factor`. If quorum is missed after the leader append, the
outcome is `unknown` because the mutation remains durable on the leader.

## Next boundary

Followers reporting a sequence gap or compacted history trigger automatic
snapshot catch-up. The leader exports a consistent materialized shard at its
current WAL sequence. The follower validates the same cluster, target, epoch,
leader, term, replica set, and replication factor fences, then durably replaces
its checkpoint and resumes its WAL at the next sequence. Older snapshots are
rejected to prevent rollback. Because the snapshot includes the triggering
mutation, successful installation counts as that follower's acknowledgement.

Replication observability exposes bounded append/snapshot result counters and a
per-collection, shard, and follower sequence-lag gauge. Consistency validation
also exercises concurrent same-shard writes and compares every follower record
version against the leader after recovery.

Distributed batch writes support `leader`, `quorum`, and `all`
acknowledgement levels. Quorum is the default. Leader and quorum requests may
return when their threshold is met while remaining fanout drains in the
background; the replication serialization gate remains held until every
attempt completes, preventing a later WAL sequence from overtaking it.

This completes the Phase 7 replication implementation boundary. Phase 8 begins
with explicit movement planning and consensus-backed replica-set transitions.
