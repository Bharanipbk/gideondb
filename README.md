# VectorDB

VectorDB is an experimental open-source, shard-aware vector database written
primarily in Go. Phase 1 now contains a working exact-search vertical slice.
No performance, durability, or scalability claims are made before the later
storage phases and reproducible benchmarking.

The foundational data model is:

```text
Collection -> Logical Shard -> Segment -> Vector Index
```

Start with the [technical design](docs/technical-design.md), then review the
[architecture decisions](docs/design/decisions/README.md) and [release
roadmap](docs/roadmap.md).

## Project status

Pre-alpha. Implemented today: float32 vectors, cosine/dot/L2 scoring, exact
flat and experimental HNSW indexes, collection/logical-shard/mutable-segment
ownership, atomic JSON collection catalogs, per-shard checksummed WALs, a basic
REST API, immutable shard checkpoints, atomic manifests, tests and
microbenchmarks. Indexed metadata filters and shard-grouped batch upserts are
available. New checkpoints separate record and float32 vector columns. Flat
collections install validated read-only mmap checkpoint bases and merge them
with WAL-backed mutable deltas and tombstones. Multi-shard queries use bounded
parallel fan-out and deterministic global top-K merging. HNSW indexes rebuild
in memory.

## Run the development server

```bash
go run ./cmd/vectordb -data-path ./data -http-address 127.0.0.1:6333 \
  -wal-sync always -checkpoint-every 1000
```

See the [quickstart](docs/getting-started/quickstart.md) for an end-to-end
search. Run `make test` and `make benchmark` for validation.

Prometheus-compatible operational metrics are available at `GET /metrics` on
the configured HTTP listener. HTTP requests propagate W3C Trace Context and
emit structured completion logs containing trace and server-span IDs.

For authenticated service, store a bearer token of at least 16 characters in a
`0600` file and start with `-api-key-file`. Non-loopback authenticated listeners
require `-tls-cert-file` and `-tls-key-file` unless cleartext is explicitly
acknowledged with `-allow-insecure-http`; unauthenticated non-loopback binds are
refused unless `-allow-unauthenticated` is explicitly set.

Create a consistent archive with `-backup-to`, and restore it into a nonexistent
data path with `-restore-from`. See the [backup and restore guide](docs/operations/backup-restore.md).

Runtime settings can be layered through a strict JSON `-config` file,
`VECTORDB_*` environment variables, and explicit CLI overrides. See
[configuration management](docs/operations/configuration.md).

The project includes a non-root multi-stage [Docker image](docs/operations/docker.md)
and one-shot [operational CLI modes](docs/operations/cli.md) for validation,
health checks, backup, restore, and build-version reporting.

## License

Apache-2.0 is the proposed license; the license file is pending maintainer
identity and copyright confirmation.
