# Rebalancing architecture

## Status

Phase 8 is in progress. Deterministic movement planning, the durable joint
consensus safety core, runtime membership replication, and authenticated
operator application are implemented. Data preparation now has a durable,
plan-bound execution journal.

## Transition planner

The planner compares replica placement at the committed current epoch with a
candidate next-epoch placement after exactly one node join or leave. It emits
only changed shards in canonical collection/shard order and is independent of
peer discovery order.

Every movement is an explicit dependency graph:

1. Copy a leader snapshot to each new replica.
2. Catch the replica up through the ordered WAL.
3. Transfer leadership when the target leader changes.
4. Commit the new placement metadata.
5. Remove replicas absent from the committed target.

Removal can never precede metadata cutover, and leadership cannot move to a new
replica before its catch-up action. Plans reject non-increasing epochs,
replication-factor changes, and mismatched shard sets.

`GET /v1/cluster/rebalance/plan` is inspection-only. It supports one join or
leave and always reports `authoritative: false`. A leave that would reduce the
cluster below the configured replication factor is rejected.

`POST /v1/cluster/rebalance/apply` accepts the same one-node change plus an
`expected_epoch` precondition. The Raft leader recomputes the target placement
and all target-view digests rather than trusting client-supplied state. A
joining node must already be discoverable at the supplied identity and address
as a non-voting learner. Discovered learners and removed nodes are excluded
from routing until their voter transition commits.

## Next boundary

The Raft state machine now persists stable and joint old/new voter sets. Entry
into joint mode commits under the old majority; finalization requires voter
identity acknowledgements satisfying majorities of both sets. Joint state
survives restart, authorizes the union for protocol messages, blocks compaction,
and prevents a removed node from campaigning after finalization. Membership
changes are restricted to one join or leave per transition.

The runtime now treats discovered non-voters as learners, derives election and
heartbeat peers from persisted active membership, and counts acknowledgements
by voter identity. A join catches the learner up to the joint entry before the
old configuration may commit it. Finalization then uses dual majorities and
propagates the committed result to the target set. Removed leaders step down.

Snapshot-copy and final catch-up images can now be sent through an authenticated
internal staging endpoint. The receiver fences cluster ID, target node,
metadata epoch, source identity, and Raft term, then independently recomputes
the join placement before accepting data on a learner. The executor itself runs only
`copy_snapshot` and `catch_up_wal`, records every successful action
atomically in `cluster-rebalance.json`, resumes after restart, and rejects a
different plan while work is active. It cannot execute leadership transfer,
metadata cutover, or replica removal. Its journal may be cleared only after the
target epoch is known to be committed. Membership commit alone does not yet
move shard data.

Distributed source-action dispatch and post-commit barrier release fanout are
now wired through the leader-only preparation and apply workflows.

The write-barrier durability core is now implemented. A source serializes
against shard replication, persists a plan-digest-bound barrier before its
final catch-up export, and rejects subsequent writes to that shard. Barriers
survive restart, cannot be replaced by another plan, and are released only
when the target epoch is committed. Sources that miss release fanout clear the
barrier automatically before their next write after observing that epoch.

Transitions that never enter joint consensus can now be aborted explicitly.
Abort is leader/term/epoch fenced, releases only exact-plan source barriers,
and retains the journal when any source is unreachable so recovery is
retryable. Abort is prohibited once joint consensus begins.

The next slice validates complete join and leave workflows under injected
source, target, coordinator, and restart failures.

The multi-node gates now cover join preparation across a remote
source leader: learner transport failure, coordinator journal restart/resume,
staged record verification, durable remote-source barrier verification, and
distributed abort cleanup. A separate real-Raft gate drives the authenticated
prepare/apply workflow through joint consensus and verifies every old voter and
the learner converge on the committed target epoch and voter set. The leave
control-plane gate now symmetrically verifies prepare/apply through real joint
consensus, convergence on the surviving voter set, journal cleanup, and
campaign rejection by the removed voter. The leave data-plane gate additionally
migrates a real record from a shard led by the departing node, verifies it on
the surviving target before cutover, checks the source barrier, and confirms
barrier release after commit. A leaving source recomputes its actions using a
surviving voter as the observer anchor. The gate now injects a target outage
mid-transfer, reconstructs the coordinator executor from disk, resumes the
same plan, and completes the data-preserving cutover.

Replica leaders now run a bounded periodic reconciliation pass after the
cluster view is authoritative. A fenced internal status probe compares durable
WAL sequences; lagging followers receive a consistent complete shard snapshot.
Healthy replicas avoid record materialization, and repair success/failure plus
sequence lag feed the existing bounded-label metrics.

Automatic repair now tracks failures independently per collection, shard, and
follower. Retries use exponential delays beginning at one second and capped at
five minutes; a successful probe or snapshot install clears the delay
immediately. This prevents an unavailable follower from being probed for every
locally led shard on every repair pass while allowing recovered replicas to
rejoin promptly.

Placement planning now accepts bounded integer capacity weights from 1 through
256. Capacity is implemented as deterministic virtual rendezvous tickets, so a
node with more resources receives proportionally more primary and replica
assignments without platform-dependent floating-point scoring. The legacy
planner defaults every node to capacity 1 and therefore preserves existing
ownership decisions.

Configured capacity is now propagated through the node identity endpoint and
peer discovery, incorporated into cluster-view placement digests, and applied
consistently by routing, replication, repair, and join/leave rebalance planning.
Older peers that omit the field are treated as unit capacity for compatibility;
values above the supported bound are rejected by discovery.

Capacity changes now have a dedicated deterministic planning contract. It
validates the existing member and old/new bounded weights, preserves membership,
advances exactly one epoch, and emits the same ordered copy, catch-up, leader
transfer, commit, and removal actions used by durable join/leave migration.
No-op and unknown-node transitions are rejected.

Raft now exposes a dedicated same-voter view transition. It validates the
target convergence fingerprints, advances exactly one metadata epoch through
the existing quorum replication path, persists the committed view, and avoids
joint consensus because membership is unchanged. Multi-node tests verify that
the new epoch and view converge on every voter.

Committed Raft views now include a canonical, sorted capacity manifest. The
manifest is validated on append and snapshot installation, replicated with the
placement digest, persisted through compaction, and restored on reopen. Server
startup prefers the committed local weight over process configuration, so a
completed transition cannot silently revert after restart. Legacy metadata
without a manifest remains readable as unit-capacity state.

The rebalance plan, prepare, apply, and abort APIs now accept a mutually
exclusive capacity change. Data preparation uses the existing durable journal
and source barriers, while apply selects the same-voter Raft view transition.
Committed manifests override both local configuration and stale peer discovery
weights immediately, so routing activates the new table as soon as the Raft
entry is applied.

Capacity-change recovery is covered by injected target catch-up failure followed
by coordinator journal reopen and exact-plan resume. Already durable snapshot
copies are not repeated, cleanup is rejected before the target epoch, and the
journal is cleared after commit. A separate server/Raft reopen gate verifies
that committed capacity overrides stale process configuration and remains
visible through node introspection.

These gates complete Phase 8. The next development phase focuses on production
distributed operation: partition and chaos coverage, rolling upgrades,
coordinated backups, and Kubernetes operations.
