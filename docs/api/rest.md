# REST API

**Maturity:** Experimental  
**Base path:** `/v1`

The machine-readable contract is [openapi.yaml](openapi.yaml). Request bodies
are limited to 16 MiB and unknown JSON fields are rejected. Errors contain a
stable-intent `code` and a human-readable `message`; documented codes are stable
within a pre-1.0 minor line.

Collection, peer, and shard-placement lists accept `limit` from 1 through 200
and an opaque `cursor`, returning `next_cursor` when another page exists. Public
API requests are protected by a configurable per-credential token bucket (or
direct client address without credentials). A rejected request returns `429
rate_limit_exceeded` and a `Retry-After` header. Health, readiness, and internal
cluster transport are excluded so overload protection cannot break liveness or
replication.

| Method | Path | Behavior |
|---|---|---|
| GET | `/health` | Process health |
| GET | `/logs` | Newest-first bounded and sanitized in-memory HTTP events |
| GET | `/node` | Stable node/cluster identity and advertised address |
| GET | `/cluster/peers` | Bootstrap metadata epoch and local static-peer health view |
| GET | `/cluster/placement` | Deterministic shard placement and activation status |
| GET | `/cluster/rebalance/plan` | Non-mutating join/leave/capacity movement preview |
| POST | `/cluster/rebalance/prepare` | Leader-coordinated, durable snapshot and final catch-up preparation |
| POST | `/cluster/rebalance/apply` | Leader-only, compare-and-change Raft membership transition |
| POST | `/cluster/rebalance/abort` | Leader-only release of an uncommitted prepared transition |
| POST | `/cluster/backup/recovery-point` | Leader-coordinated cluster freeze and canonical recovery manifest |
| GET | `/cluster/readiness` | Peer health and control-plane fingerprint convergence |
| GET | `/collections` | List collection configurations |
| POST | `/collections` | Create a collection |
| GET | `/collections/{name}` | Describe configuration and vector count |
| DELETE | `/collections/{name}` | Delete a collection catalog and shard WALs |
| POST | `/collections/{name}/vectors` | Insert or replace a record |
| GET | `/collections/{name}/vectors` | Cursor-page records on this node, redacting vectors by default |
| POST | `/collections/{name}/vectors/batch` | Insert or replace 1–10,000 records |
| GET | `/collections/{name}/vectors/{id}` | Read a record |
| DELETE | `/collections/{name}/vectors/{id}` | Delete a record |
| POST | `/collections/{name}/search` | Dense, sparse, or hybrid search |
| POST | `/internal/shards/{collection}/{shard}/search` | Fenced single-shard internal search |
| POST | `/cluster/collections/{name}/search` | Experimental placement-aware distributed search |
| DELETE | `/cluster/collections/{name}/vectors/{id}` | Placement-aware, quorum-replicated record deletion |
| POST | `/internal/shards/{collection}/{shard}/vectors/batch` | Fenced single-shard WAL-backed batch write |
| POST | `/cluster/collections/{name}/vectors/batch` | Experimental distributed batch-write coordinator |

Operational metrics are exposed outside the versioned API at `GET /metrics`
using the Prometheus text exposition format. Metrics include bounded-label HTTP
request counts and latency histograms, in-flight requests, collection count,
live vectors per collection, configured-peer health, thresholded peer state,
and cluster-view readiness. HTTP labels use route templates such as
`/v1/collections/{name}` rather than user-supplied path values. Collection names
appear only on the vector gauge and should therefore remain operationally
bounded.

The server accepts W3C `traceparent` headers. Valid incoming trace IDs are
propagated, each request receives a new server span ID, and the response returns
the resulting `traceparent`. Structured completion logs include trace/span IDs,
method, route template, status, and duration. Raw URL paths, query strings,
request bodies, collection names, and vector IDs are not logged by this tracer.

Node information reports only non-secret effective posture: whether API
authentication, internal mutual TLS, and static routing are enabled, plus the
replication factor, placement capacity, and supported cluster protocol range.
It never returns credential values, certificate material, or data paths.

The authenticated node log feed returns at most 200 events. Production servers
recover and retain the configured number of sanitized events (4,096 by default,
256–1,000,000 via `-audit-retention`) in
`operational-events.jsonl` under the data directory; embedded/test servers
without a persistence path retain 256 in memory. The file is mode `0600`, uses
append-only JSON lines, and is atomically compacted after reaching twice its
retention bound. The response reports `durable` and any `persistence_error`.
Entries contain a timestamp,
severity, method, bounded route template, status, duration, trace ID, and span
ID. Raw paths, query values, request/response bodies, collection and record
identifiers, vectors, metadata, payloads, and credentials are never retained.

`GET /v1/cluster/logs` performs authenticated, metadata-epoch-fenced reads from
discovered peers and deterministically merges the newest events. Each event is
tagged with its node ID. Unavailable nodes are returned in `failures` with
`partial=true`; unlike data pagination, partial operational visibility cannot
skip or mutate database records.

The optional `namespace` query parameter on get/delete and body field on search
selects an exact namespace. Omitting it selects only the default empty
namespace; it never searches every namespace.

The record-list endpoint orders records by namespace and ID, accepts a bounded
`limit` from 1 through 200, and returns an opaque `next_cursor`. Without a
namespace it browses every namespace physically present on the current node.
Vectors are omitted unless `include_vector=true` is explicitly set.
In distributed mode, `GET /v1/cluster/collections/{name}/vectors` queries one
placement owner per shard, merges records by namespace and ID, and returns an
epoch-fenced opaque cursor. Any shard failure fails the page because advancing
a partial cursor could permanently skip records. A metadata-epoch change
invalidates the cursor, and changing its namespace is rejected. This provides
deterministic bounded traversal, not snapshot isolation from concurrent writes.

Vectors and search queries must contain exactly the collection dimension and
only finite float32 values; NaN and positive/negative infinity are rejected.
Records may additionally contain a `sparse_vector` with 1–10,000 non-empty
term keys and finite weights. A search with only `sparse_vector` uses sparse
cosine similarity; supplying both vectors enables hybrid scoring, with `alpha`
from 0 through 1 selecting the dense weight (default `0.5`). See [Sparse and
hybrid retrieval](../indexing/sparse-hybrid.md).

Batch requests validate every record before writing. Records are grouped by
logical shard and encoded as one WAL record per shard. Each shard group is
atomic during replay, but the overall cross-shard batch is not atomic: an I/O
failure after an earlier shard commits can produce a partial batch. Clients
must use stable IDs and inspect shard outcomes.

Internal shard searches require exact cluster, target-node, and metadata-epoch
headers. A stale epoch, wrong target, or cluster mismatch returns `409`; a
request from a future metadata epoch returns `503`. These routes are transport
primitives and are not a stable public client API.

Metadata Raft peers use authenticated `request-vote` and `append-entries`
internal endpoints. Both require `X-GideonDB-Cluster-ID`; a mismatched cluster
is rejected before the body reaches the Raft state machine. Append responses
publish the node's live committed epoch in `X-GideonDB-Metadata-Epoch`.
Lagging followers use the similarly protected `install-snapshot` endpoint when
their next required index has already been compacted by the leader.

`POST /v1/cluster/metadata/epoch` is a leader-only compare-and-advance
operation. The caller supplies only `expected_epoch`; the leader derives the
next membership, catalog, and placement digests from its canonical local view,
replicates the command, and responds only after a voter majority commits it.
Followers return `409 not_raft_leader`, and absent quorum returns `503`.
After commit, authoritative readiness requires the locally recomputed
membership, catalog, and placement digests to match the committed tuple exactly.

`GET /v1/cluster/replicas?replication_factor=N` previews deterministic Phase 7
replica sets and their initial leaders. It remains non-authoritative until
replica catch-up and placement transitions are consensus-backed.

`GET /v1/cluster/rebalance/plan` previews one Phase 8 placement change. Supply
either `join_node_id` plus `join_address`, `leave_node_id`, or
`capacity_node_id` plus `placement_capacity`. Capacity must be from 1 through
256 and must differ from the member's committed value. The response
contains the next-epoch target placement and only changed shards. Each shard's
dependency graph orders snapshot copy, WAL catch-up, optional leadership
transfer, metadata cutover, and final replica removal. The plan is deliberately
non-authoritative and does not mutate membership or move data.

`POST /v1/cluster/rebalance/apply` accepts `expected_epoch` and the same change
fields. It requires normal bearer authentication when an API key is
configured, rejects stale epochs, and is accepted only by the current Raft
leader. Joins require the node to be present in discovery as a learner with an
exact matching advertised address. The server recomputes the movement plan,
canonical voter set, and next-epoch view digests, then commits the one-node
change through joint consensus. A successful response means membership and
target metadata committed. Apply rejects plans that have not completed the
exact durable preparation journal. After commit the coordinator releases
source barriers; any temporarily unreachable source also self-releases its
barrier once it observes the committed target epoch.

Capacity changes retain the voter set and commit through a same-voter Raft view
advance after durable preparation. The canonical capacity manifest becomes
authoritative immediately on every voter, overriding stale discovery values
until peers advertise the committed weight.

`POST /v1/cluster/rebalance/prepare` accepts the same request body and runs on
the Raft leader. It dispatches every copy and catch-up action to the placement
source leader. Final catch-up persists a source write barrier before exporting
the latest image, closing the write/cutover race. Repeating the request resumes
from the durable action journal.

`POST /v1/cluster/rebalance/abort` uses the same body. It is accepted only by
the current Raft leader at the expected epoch and is rejected while joint
consensus is active. The coordinator releases only barriers bearing the exact
plan digest. It clears the preparation journal only after every source
acknowledges, so an incomplete abort is safe to retry.

The internal follower endpoint
`POST /v1/internal/replicas/{collection}/{shard}/append` accepts an exact WAL
sequence, replication factor, and leader-prepared records. It requires the
normal cluster/target/epoch headers plus `X-GideonDB-Leader-Node-ID` and
`X-GideonDB-Leader-Term`. Gaps, conflicting retries, stale terms, wrong leaders,
and non-replica targets fail explicitly.
An append response with `replication_gap` or
`replication_sequence_compacted` causes the leader to install a complete shard
snapshot through the separately fenced internal snapshot endpoint. Snapshot
installation rejects stale sequences and resumes ordered append at the next
leader WAL sequence.

`POST /v1/internal/rebalance/{collection}/{shard}/snapshot` stages a complete
shard image on a joining learner. It is an authenticated transport primitive,
not a public client API. In addition to the normal internal request fences it
requires the source leader identity and current Raft term; the receiver
recomputes the proposed join and rejects snapshots for shards it would not own.

`GET /v1/internal/replicas/{collection}/{shard}/status` returns the durable WAL
sequence of an assigned replica under the normal cluster/node/epoch fences.
Shard leaders use it during periodic reconciliation and install a complete
snapshot when a follower is behind.

Distributed search fails with `503` if any shard fails. Setting
`allow_partial: true` explicitly permits a `200` response containing incomplete
results and per-shard failures. Responses include the metadata epoch and
`authoritative_placement`. Multi-node search, scroll, and batch-write routes now
fail with `503 authoritative_placement_required` unless the converged placement
exactly matches metadata committed by the configured Raft voters. A standalone
replication-factor-one node is authoritative without a remote consensus group.

Distributed batch writes validate all records before fanout, but shard commits
are independent. HTTP `207` indicates at least one `failed` or `unknown` shard
outcome. An unknown outcome may already be durable and is never retried
automatically. Clients may send `Idempotency-Key` using 1–128 letters, digits,
periods, underscores, colons, or hyphens. The coordinator propagates the key to
each shard leader. The leader durably reserves the prepared record versions
before WAL append, returns the original result for matching retries (including
after checkpoint and restart), and rejects reuse with different shard content
as `idempotency_conflict`. Keys are retained until the collection is deleted;
operators should therefore use one key per logical request and monitor storage
growth for high-cardinality ingestion workloads.
With replication factor greater than one, the shard leader sends its exact
prepared WAL batch to all followers and reports `committed` only after a
majority acknowledges durable append. Each committed outcome includes
`replicas_acknowledged` and `replication_factor`. A missed quorum after the
leader append is reported as `unknown`.

The optional `acknowledgement` field selects the required durability level:
`leader` requires only the leader WAL, `quorum` (the default) requires a
majority, and `all` requires every configured replica. Leader and quorum
responses may return once their threshold is reached; outstanding follower
work retains per-shard ordering in the background. The selected level is
propagated to remote shard leaders and returned in committed outcomes.

When static routing is enabled, collection create/delete returns
`409 static_schema_immutable`, and legacy single-node record/search routes return
`409 static_routing_required`. Build identical catalogs before activation and
use cluster coordinator endpoints afterward.

The internal coordinated-backup protocol uses
`POST /v1/internal/backup/freeze`,
`GET /v1/internal/backup/recovery-point`, and
`POST /v1/internal/backup/archive`, and
`POST /v1/internal/backup/release`. All four require normal internal
cluster/node/epoch headers plus `X-GideonDB-Backup-Operation`. Freeze and
release are idempotent only for the matching operation; a different operation
receives `409`.

`POST /v1/cluster/backup/recovery-point` accepts `{"operation":"..."}` on the
current Raft leader. It requires authoritative cluster readiness, freezes every
committed peer, collects all fenced node recovery points, returns their
canonical merged manifest, and releases every successfully frozen barrier on
both success and failure.

The metrics endpoint shares the REST listener; when bearer authentication is
configured it protects metrics along with data routes. REST list endpoints are
bounded and cursor-paginated, public routes are rate-limited, and gRPC remains
a contract without a server transport implementation.
