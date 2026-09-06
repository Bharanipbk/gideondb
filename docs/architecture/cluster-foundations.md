# Cluster foundations

## Node identity

On server startup, each data directory loads or creates `NODE_ID.json`. The ID
is 128 random bits encoded as 32 lowercase hexadecimal characters. Creation
writes and synchronizes a private temporary file, then uses a no-replace hard
link so concurrent first starts converge on one winner without overwriting an
existing identity. Corrupt or unknown-format identity files fail closed.

The identity is deliberately excluded from backup archives. A restored data set
therefore receives a new identity on first server start, preventing accidental
duplicate node IDs when a backup seeds another machine.

## Node information

Authenticated clients can call `GET /v1/node` to retrieve the stable node ID,
configured advertised address, process start time, and current `standalone` or `static-discovery`
mode. `-advertise-address` defaults to the HTTP listen address and can also be
set through `advertise_address` or `GIDEONDB_ADVERTISE_ADDRESS`.

## Static peer discovery

Operators may configure up to 256 HTTP(S) peer base URLs through `peers`,
`GIDEONDB_PEERS`, or `-peers`. Every five seconds the node concurrently calls
each peer's authenticated `GET /v1/node` endpoint with a three-second client
timeout and a 1 MiB strict response limit. The view records node ID, advertised
address, health, last check, last successful observation, and the latest error.
Failed checks retain the last known identity and last-seen timestamp. Seeds
resolving to the local node, malformed identities, and duplicate IDs are unhealthy.

## Cluster metadata envelope

Each data directory also stores a private `CLUSTER_META.json` containing a
random 128-bit cluster ID, format version, bootstrap metadata epoch, and creation
time. Operators joining independently initialized nodes must configure the same
cluster ID before first startup. Peer handshakes reject nodes with a different
cluster ID. Like node identity, this envelope is excluded from data backups so a
restore cannot accidentally inherit control-plane membership.

Epoch 1 is explicitly a local bootstrap epoch. Later epochs are committed by
the metadata Raft group and exposed through protocol fencing.

Authenticated clients can inspect `GET /v1/cluster/peers`; Prometheus output
includes `gideondb_cluster_peer_healthy`. All nodes currently use the same bearer
key, and HTTPS peers must use certificates trusted by the system trust store.

## Thresholded node health

Peer health follows `unknown → healthy → suspected → unhealthy → recovering →
healthy`. A known healthy peer becomes suspected after one failed poll and
unhealthy after three consecutive failures. Recovery requires two consecutive
successful identity checks; the first moves it to recovering. Counters saturate
at their thresholds. A seed that has never returned a valid identity remains
unknown regardless of repeated transport failures. Identity, cluster, self, and
duplicate-ID violations transition directly to unhealthy because they are not
ordinary liveness failures.

`last_seen` advances only on a valid response, while `last_checked` advances on
every attempt and `last_transition` changes only when state changes. The REST
membership view exposes the state and counters. Metrics retain the binary
`gideondb_cluster_peer_healthy` gauge and add
`gideondb_cluster_peer_state{state=...}`.

This view is observational only. It does not provide membership epochs, quorum
failure detection, placement, consensus, replication, or distributed routing.

## Placement preview

`GET /v1/cluster/placement` produces a deterministic, epoch-tagged assignment
of every collection's logical shards across the local node and all peers whose
identities have been observed. It uses SHA-256 rendezvous scoring over
collection, shard ID, and stable node ID. Inputs and output are sorted, so every
node with the same metadata view produces the same table. Adding a node moves
only shards won by that node.

Health does not alter ownership: a known but currently unhealthy node remains a
candidate. This prevents transient failure detection from causing implicit data
movement. The table reports `authoritative: false` by default and becomes
authoritative only under the opt-in static activation described below. Logical
record-to-shard routing remains the existing
fixed modulo hash and is independent of physical membership.

## Epoch fencing and internal shard reads

The authenticated internal endpoint
`POST /v1/internal/shards/{collection}/{shard}/search` searches exactly one
logical shard. Requests must carry `X-GideonDB-Cluster-ID`,
`X-GideonDB-Target-Node-ID`, and `X-GideonDB-Metadata-Epoch`. Validation occurs
before request-body decoding or shard access. Cluster and target mismatches and
stale epochs return typed `409` errors; a future epoch returns `503`, indicating
that the receiver cannot safely serve the caller's newer metadata view. Every
response publishes the receiver's current epoch header.

This is the data-plane primitive for distributed fanout, not public ownership
activation. It currently uses the authenticated REST listener and searches the
local copy because physical shard materialization has not yet been separated.
The placement view cannot bypass the fence or owner validation.

## Distributed search preview

`POST /v1/cluster/collections/{name}/search` coordinates a read over the current
placement preview. It sends one fenced internal request per logical shard,
searches locally owned shards without a network hop, and limits remote fanout to
at most 32 workers. The internal HTTP client has a five-second timeout and
propagates authentication, trace context, cluster ID, target node ID, and epoch.
Peer responses are strictly decoded with a 16 MiB limit and must echo the shard
and metadata epoch.

The coordinator merges shard results with an O(top_k) heap and deterministic
score/ID tie ordering. Any shard failure returns `503` by default. Callers must
set `allow_partial: true` to receive incomplete results; those responses include
`partial: true` and bounded per-shard failure records.

The response reports whether static placement is authoritative. By default it validates
the search transport and coordination behavior while every node still stores
all shards; it is not yet a correctness claim for partitioned storage. Public
`/collections/{name}/search` remains the single-node path.

## Distributed write preview

`POST /v1/cluster/collections/{name}/vectors/batch` validates the complete
request before any mutation, routes records by the immutable logical-shard
hash, groups them by shard, and sends each group to its preview-placement owner.
Local and remote work uses at most 32 workers under a five-second coordination
deadline. The internal
`POST /v1/internal/shards/{collection}/{shard}/vectors/batch` endpoint applies
the cluster/node/epoch fence before committing the group as one WAL record and
rejects records that do not route to the addressed shard.

Cross-shard writes are not atomic. Responses list each shard as `committed`,
`failed`, or `unknown`; partial responses use HTTP `207`. Transport errors,
peer 5xx responses, malformed successful responses, and response-fence
mismatches are `unknown` because the remote WAL may already contain the write.
The coordinator never retries these automatically. Clients should retain stable
record IDs and reconcile unknown outcomes before retrying; durable idempotency
keys are not implemented yet.

Responses state whether static placement is authoritative. This validates routing and
failure semantics but does not activate physical ownership or provide
partition-safe writes before metadata consensus.

## Cluster-view convergence

Every node publishes its metadata epoch and SHA-256 fingerprints of the stable
node-ID set, normalized collection catalog, and resulting shard assignments in
`GET /v1/node`. Health is excluded from these fingerprints so transient probes
cannot change them. Canonical sorting makes the values independent of which
node computes them or the order in which peers and collections were discovered.

`GET /v1/cluster/readiness` reports ready only when every configured peer is in
the healthy state and reports the exact local epoch and all three fingerprints.
It returns specific mismatch reasons. `authoritative` becomes true only when
static routing is enabled and the view is ready.
`gideondb_cluster_view_ready` exposes the same decision as a gauge. Readiness is
a necessary activation precondition, not consensus: matching views do not grant
leadership or make a minority partition safe for writes.

## Opt-in static placement activation

`enable_static_routing`, `GIDEONDB_ENABLE_STATIC_ROUTING`, or
`-enable-static-routing` activates ownership for the immutable bootstrap epoch.
Every distributed coordinator request then requires a ready cluster view;
otherwise it returns `503 cluster_view_not_ready`. Internal shard reads and
writes additionally recompute placement and return `409 wrong_owner` unless the
target node owns that shard. Placement, readiness, distributed-search, and
distributed-write responses report authoritative placement while these checks
hold. Node mode becomes `static-routing`.

This mode supports functional fixed-topology clusters, not safe topology
changes. There is no membership log, election, quorum commit, or lease, and
failure detection has a bounded delay. Operators must not change peers,
collections, or node identities independently. Collection create/delete is
blocked after activation, so every node's catalog must be prepared consistently
before startup. Legacy single-node get, search, upsert, batch, and delete routes
also return `409 static_routing_required`; cluster coordinators and fenced
internal routes are the only enabled data paths. Production partition safety and
epoch advancement still require metadata consensus. Physical storage also
persists an immutable node-local `SHARD_OWNERSHIP.json` manifest on the first
convergence-gated data request. Activation fails closed if an unowned shard
already contains records, WAL history, or segment files. Once validated, the
engine closes and removes empty unowned WALs and segment directories; restart
loads only owned durable shard structures. The manifest is excluded from backup
archives so restored data can be assigned deliberately in its destination
cluster.

## Three-node functional gate

The automated three-node gate uses separate durable engine directories and real
REST handlers for three converged static-routing nodes. A coordinator writes one
record per logical shard, verifies that only the assigned owner materializes
each record, coordinates global top-K search from another node, and then
restarts every engine to verify owner recovery. The in-process HTTP transport
keeps the test deterministic while exercising the same authentication, fencing,
ownership, WAL, response validation, and merge code used by the server.

The gate also verifies a manifest on each node, absence of every unowned WAL,
and continued ownership rejection after restart. Empty in-memory logical shard
objects remain inexpensive routing metadata, while durable WAL and segment
materialization is owner-only. Real socket, multi-process, partition, and
consensus tests remain.

## Metadata Raft safety core

Each server now opens a node-local `CLUSTER_RAFT.json` state file and derives
its serving epoch from the committed state. The implementation durably records
the current term, vote, metadata log, commit index, and applied epoch using an
atomic replace plus directory sync. Its protocol core implements Raft's
up-to-date-log vote rule, one vote per term, previous-entry matching,
conflicting uncommitted suffix replacement, committed-entry protection, and
majority-gated current-term commit. Applied `advance_epoch` commands must be a
strictly monotonic sequence and carry membership, catalog, and placement
digests.

The Raft file is node-specific and excluded from backups. Authenticated
`request-vote` and `append-entries` HTTP endpoints enforce the cluster ID before
invoking the protocol core. Servers read the applied epoch from Raft state, so
follower commits become visible without restart. A runtime now uses randomized
election timeouts, majority voting, periodic leader heartbeats, full-log
follower catch-up for the bounded bootstrap log, and higher-term step-down.
Node info exposes the current Raft role, leader ID, and term.

The leader-only epoch proposal API uses an expected-epoch precondition and
derives all view digests server-side. Per-follower `nextIndex` state drives
bounded incremental replication and conflict backtracking; the leader commits
only current-term entries acknowledged by a majority, then propagates the new
commit index. Elections and proposals fail closed while any configured voter
identity remains undiscovered, preventing quorum shrink during startup.

On the first fully discovered view, every node sorts and persists the complete
fixed voter-ID set in `CLUSTER_RAFT.json`. Subsequent discovery views must match
that exact set; health changes and address changes cannot alter quorum size or
composition. Vote and append requests from identities outside the persisted set
are rejected. Node info exposes the durable voter list for diagnosis.

Every applied epoch also persists its committed membership, catalog, and
placement digest tuple. Static readiness recomputes the local view at the
current epoch and compares all three values with that committed tuple before it
can become authoritative. A missing tuple, local catalog drift, membership
drift, or placement drift fails readiness and therefore blocks routed reads and
writes. Duplicate commands for the same epoch are idempotent only when their
digest tuples are identical. Node info exposes the committed tuple for audit.

Leaders also track successful heartbeat contact with a voter majority. Losing
majority contact for an election-timeout interval demotes the leader locally
and clears its replication progress. A deterministic partition gate isolates
the current leader, verifies its proposal path closes, elects a replacement in
the remaining majority, and commits there while the minority cannot advance
metadata.

The durable store now uses absolute log indices and can compact its fully
committed prefix into a snapshot boundary containing the last included index
and term plus the applied epoch and committed view. Restart validation checks
that boundary, and new entries continue at the next absolute index.

After 256 newly committed entries, leaders automatically compact the committed
prefix. A follower whose `nextIndex` is behind the retained boundary receives
an authenticated `InstallSnapshot` RPC containing the voter set, applied epoch,
and committed view. Installation validates leader membership, term, sorted
voters, digest formats, and snapshot freshness, then retains a matching local
suffix when safe. Replication resumes at the next absolute index.

Joint-consensus membership changes and production retry/backoff remain for
later phases.
