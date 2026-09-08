# gRPC API

Maturity: **local data-plane transport implemented; distributed and administrative RPCs planned**.

The versioned public protobuf contract is
[`gideondb.v1.GideonDBService`](../../api/proto/gideondb/v1/gideondb.proto).
It covers the 18 operations required by the technical design:

- collection creation, deletion, listing, and description;
- record upsert, bounded batch upsert, deletion, and exact lookup;
- search, ordered batch search, and opaque-cursor scrolling;
- cluster status plus node and logical-shard placement listing;
- health and bounded operational statistics; and
- server-streamed snapshots plus client-streamed restores.

The Go process can expose an opt-in gRPC listener with `-grpc-address`, the
`GIDEONDB_GRPC_ADDRESS` environment variable, or the `grpc_address` JSON field.
It implements collection CRUD, record CRUD, local search and batch search,
local and distributed scroll, cluster status, node/shard listings, health,
statistics, and local snapshot streaming. Distributed scroll reuses the REST
coordinator's authoritative placement, peer fencing, bounded merge, and
epoch-bound cursor semantics. With static routing active, search and batch
search use the same authoritative shard coordinator and retain partial failure
details and the serving metadata epoch. Upsert and batch upsert likewise use the
distributed coordinator, including leader/quorum/all acknowledgement policies
and explicit per-shard committed, failed, or unknown outcomes. Placement-aware
With `cluster_wide=true`, snapshot requires the metadata-Raft leader and a
converged authoritative placement. It freezes every committed voter, captures
one canonical recovery point, collects a recovery-point-bound archive from
each node while all barriers remain held, packages the checksummed node
archives, and streams that package through the same offset and SHA-256
contract. Barriers are released on success and failure. Delete is
placement-aware when static routing is enabled: the authoritative shard leader
commits an ordered WAL tombstone and waits for quorum replication before gRPC
reports success.

Restore is an admin-only client stream into the explicitly configured
`-grpc-restore-path`. It validates the operation ID, contiguous offsets, 1 MiB
chunk bound, 64 GiB archive bound, terminal SHA-256 digest, and backup format
before atomically publishing a new directory. It never replaces the running
engine data path.

```sh
gideondb -http-address 127.0.0.1:6333 -grpc-address 127.0.0.1:6334
```

The listener uses the configured bearer API key, reloadable principals file,
and TLS identity. The legacy API key has administrator authority. Principals
enforce reader, writer, and administrator roles plus collection-prefix tenant
isolation; collection listings are filtered to the caller's allowed prefixes.
Every RPC must carry a client deadline, and inbound and outbound messages are
capped at 16 MiB. Non-loopback startup applies the same explicit authentication
and cleartext opt-in rules as the REST listener. Per-credential token buckets
use `rate_limit_per_second` and `rate_limit_burst`; exhausted calls return
`RESOURCE_EXHAUSTED` with `retry-after-ms` response metadata. The limiter keeps
at most 4,096 credential identities and stores hashes rather than bearer tokens.
Every completed or rejected RPC records only its stable method name, mapped
outcome status, and duration in the same bounded durable audit stream as REST;
bearer tokens, request bodies, collection names, and record identifiers are not
recorded.

## Authentication, deadlines, and errors

Authenticated clients send `authorization: Bearer <token>` in request metadata.
Every call should have a client deadline. Servers must enforce their own bounded
work limits even when a caller omits a deadline. The public service never exposes
the internal Raft, replication, repair, rebalance, or backup-control transports.

Implementations map stable REST error semantics to standard gRPC status codes:

| Condition | gRPC status |
| --- | --- |
| Invalid vector, filter, cursor, or limit | `INVALID_ARGUMENT` |
| Missing or invalid bearer credential | `UNAUTHENTICATED` |
| Existing collection or version conflict | `ALREADY_EXISTS` or `ABORTED` |
| Missing collection or record | `NOT_FOUND` |
| Stale metadata epoch or placement fence | `FAILED_PRECONDITION` |
| Unavailable quorum, owner, or readiness | `UNAVAILABLE` |
| Caller deadline expires | `DEADLINE_EXCEEDED` |
| Caller cancels | `CANCELLED` |
| Unexpected internal failure | `INTERNAL` |

Writes are not automatically retry-safe after an ambiguous transport failure.
Batch and distributed responses retain explicit per-shard committed, failed, or
unknown outcomes.

## Pagination and streaming bounds

`Scroll` accepts a page size from 1 through 200. Its cursor is opaque and may be
bound to namespace, collection, placement epoch, and vector-inclusion settings.
Clients must return it unchanged. Vectors remain excluded unless
`include_vector` is true.

Snapshot and restore chunks carry absolute offsets, operation IDs, a final
SHA-256 digest, and an end-of-stream marker. Each `data` field is limited by the
contract to 1 MiB. Implementations must reject gaps, overlaps, checksum
mismatches, stale epochs, reused operation identifiers, and publication into a
non-empty restore destination.

## Validation and generation

The root [`buf.yaml`](../../buf.yaml) enables the Buf `STANDARD` lint category
and `FILE` breaking-change policy. [`buf.gen.yaml`](../../buf.gen.yaml) defines
Go message and service generation. Generated Go bindings are checked in so
ordinary `go test ./...` validates the transport without requiring Buf. The
normal repository suite also runs a dependency-free structural gate that
verifies the package, service, complete RPC set, and declaration comments.

```sh
make test-proto-contract
make test-proto-compatibility
buf lint
buf format --diff --exit-code
buf generate
go test ./internal/api/grpcapi
```
