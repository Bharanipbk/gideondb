# VectorDB

VectorDB is an experimental open-source, shard-aware vector database written
primarily in Go. It includes single-node storage and recovery, flat and HNSW
search, metadata filtering, static distributed placement, Raft metadata
coordination, replication and repair, capacity-aware rebalancing, operational
hardening, multi-language SDKs, and an embedded administration dashboard.
It remains pre-alpha: APIs and persistent formats may change, and deployment at
scale requires workload-specific validation.

The foundational data model is:

```text
Collection -> Logical Shard -> Segment -> Vector Index
```

Start with the [technical design](docs/technical-design.md), then review the
[architecture decisions](docs/design/decisions/README.md) and [release
roadmap](docs/roadmap.md).

For evaluation or rollout, follow the staged [adoption guide](docs/getting-started/adoption.md).

## Project status

Pre-alpha. The core supports float32 vectors; cosine, dot-product and L2
scoring; exact flat and experimental HNSW indexes; logical shards; checksummed
WAL recovery; immutable mmap-oriented checkpoints; tombstones; indexed
metadata filters; bounded parallel search; replication; repair; Raft-backed
cluster metadata; deterministic placement and rebalancing; backup/restore;
Prometheus metrics; structured tracing; REST APIs; Go, Python, TypeScript, Java,
Rust, and .NET SDKs; Python embedding/framework integrations; and a web dashboard.

Known release gaps include the external Kubernetes gate, gRPC server transport, and final
open-source governance/release artifacts. See the [roadmap](docs/roadmap.md)
for the authoritative phase status.

## Run locally

### Prerequisites

- [Go 1.26 or newer](https://go.dev/doc/install)
- `make` (optional; the equivalent Go commands are shown below)
- `curl` to run the health check and quickstart requests

From the repository root, start the development server with:

```bash
make run
```

The equivalent command, including the default durability settings, is:

```bash
go run ./cmd/vectordb \
  -data-path ./data \
  -http-address 127.0.0.1:6333 \
  -wal-sync always \
  -checkpoint-every 1000
```

VectorDB listens at `http://127.0.0.1:6333` and persists its data in `./data`.
The directory is created automatically. Check that the server is ready from a
second terminal:

```bash
curl -fsS http://127.0.0.1:6333/v1/health
curl -fsS http://127.0.0.1:6333/v1/ready
```

Open the administration dashboard at
[http://127.0.0.1:6333/dashboard/](http://127.0.0.1:6333/dashboard/). Follow the
[quickstart](docs/getting-started/quickstart.md) to create a collection, insert
a vector, and run a search. Stop the server with `Ctrl+C`; the contents of
`./data` remain available for the next run.

To build and run a standalone binary instead:

```bash
make build
./vectordb -data-path ./data -http-address 127.0.0.1:6333
```

Run the Go and SDK test suites with `make test`, or the benchmarks with
`make benchmark`.

## Run with Docker

### Prerequisites

- Docker Engine or Docker Desktop with the Docker daemon running
- `make` (optional)

Build the non-root, multi-stage image from the repository root:

```bash
make docker IMAGE=vectordb:dev
```

Without `make`, use:

```bash
docker build -t vectordb:dev .
```

Create and start a container with port `6333` published and data stored in a
named Docker volume:

```bash
docker run --name vectordb --detach \
  --publish 6333:6333 \
  --volume vectordb-data:/var/lib/vectordb \
  vectordb:dev \
  -data-path /var/lib/vectordb \
  -http-address 0.0.0.0:6333 \
  -allow-unauthenticated
```

The explicit `-allow-unauthenticated` flag is required because the server
otherwise refuses an unauthenticated non-loopback listener. This setup is for
local development only.

Check the container and API:

```bash
docker ps --filter name=vectordb
docker logs vectordb
curl -fsS http://127.0.0.1:6333/v1/health
curl -fsS http://127.0.0.1:6333/v1/ready
```

The dashboard is available at
[http://127.0.0.1:6333/dashboard/](http://127.0.0.1:6333/dashboard/), and the
same [quickstart API requests](docs/getting-started/quickstart.md) work against
the container.

Stop and remove the container with:

```bash
docker stop vectordb
docker rm vectordb
```

The `vectordb-data` volume is not removed, so a new container using the same
volume resumes with the existing data. To run the container in the foreground
and remove it automatically when stopped, omit `--name vectordb --detach` and
add `--rm`.

For an authenticated/TLS-enabled container, host bind mounts, and production
considerations, see the [Docker operations guide](docs/operations/docker.md)
and [security guide](docs/operations/security.md).

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
