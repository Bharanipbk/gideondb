# gRPC API

Maturity: **contract implemented; server transport planned**.

The versioned public protobuf contract is
[`vectordb.v1.VectorDBService`](../../api/proto/vectordb/v1/vectordb.proto).
It covers the 18 operations required by the technical design:

- collection creation, deletion, listing, and description;
- record upsert, bounded batch upsert, deletion, and exact lookup;
- search, ordered batch search, and opaque-cursor scrolling;
- cluster status plus node and logical-shard placement listing;
- health and bounded operational statistics; and
- server-streamed snapshots plus client-streamed restores.

The current Go process does not yet listen for gRPC traffic. Publishing the
schema before wiring a server makes field numbering, error mapping, streaming
bounds, and public versus internal protocol ownership independently reviewable.

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
Go message and service generation. Until Buf is installed, the normal repository
suite still runs a dependency-free structural gate that verifies the package,
service, complete RPC set, and declaration comments.

```sh
make test-proto-contract
buf lint
buf format --diff --exit-code
```

Generated Go transport code and the actual server are deliberately deferred to
the next implementation increment; the source schema remains authoritative.
