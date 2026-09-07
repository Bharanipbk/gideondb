# Configuration management

Configuration is resolved in this order, with later layers winning:

1. Built-in defaults
2. Strict JSON file selected by `-config`
3. `GIDEONDB_*` environment variables
4. Explicitly supplied CLI flags

Omitted CLI flags do not overwrite file or environment values. Unknown JSON
fields, trailing JSON values, malformed booleans or integers, invalid WAL sync
modes, and incomplete TLS certificate/key pairs fail startup.

## Example file

```json
{
  "http_address": "127.0.0.1:6333",
  "advertise_address": "node-a.internal:6333",
  "cluster_id": "1234567890abcdef1234567890abcdef",
  "peers": ["https://node-b.internal:6333", "https://node-c.internal:6333"],
  "data_path": "/var/lib/gideondb",
  "wal_sync": "always",
  "checkpoint_every": 1000,
  "replication_factor": 2,
  "placement_capacity": 1,
  "rate_limit_per_second": 100,
  "rate_limit_burst": 200,
  "audit_retention": 4096,
  "api_key_file": "/run/secrets/gideondb-api-key",
  "principals_file": "/run/secrets/gideondb-principals.json",
  "allow_unauthenticated": false,
  "allow_insecure_http": false,
  "enable_static_routing": false,
  "tls_cert_file": "/run/tls/tls.crt",
  "tls_key_file": "/run/tls/tls.key",
  "tls_ca_file": "/run/tls/ca.crt",
  "node_tls_cert_file": "/run/tls/node-client.crt",
  "node_tls_key_file": "/run/tls/node-client.key"
}
```

Start with:

```bash
gideondb -config /etc/gideondb/config.json
```

## Environment variables

- `GIDEONDB_HTTP_ADDRESS`
- `GIDEONDB_ADVERTISE_ADDRESS`
- `GIDEONDB_CLUSTER_ID`
- `GIDEONDB_PEERS` (comma-separated HTTP(S) base URLs)
- `GIDEONDB_DATA_PATH`
- `GIDEONDB_WAL_SYNC`
- `GIDEONDB_CHECKPOINT_EVERY`
- `GIDEONDB_REPLICATION_FACTOR`
- `GIDEONDB_PLACEMENT_CAPACITY`
- `GIDEONDB_RATE_LIMIT_PER_SECOND`
- `GIDEONDB_RATE_LIMIT_BURST`
- `GIDEONDB_API_KEY_FILE`
- `GIDEONDB_ALLOW_UNAUTHENTICATED`
- `GIDEONDB_ALLOW_INSECURE_HTTP`
- `GIDEONDB_ENABLE_STATIC_ROUTING`
- `GIDEONDB_TLS_CERT_FILE`
- `GIDEONDB_TLS_KEY_FILE`
- `GIDEONDB_TLS_CA_FILE`

Environment booleans use Go boolean syntax such as `true` or `false`.

Public `/v1` API requests use a per-credential token bucket, falling back to
the direct client address when no authorization header is present. Defaults are
100 requests per second with a burst of 200. Exceeded requests return `429
rate_limit_exceeded` with `Retry-After`; health, readiness, and authenticated
internal node traffic are excluded. The limiter retains at most 4,096 active
identities to keep its own memory bounded.

`tls_ca_file` requires the server certificate and key. It enables private-CA
verification for outbound node traffic and verified client-certificate
enforcement on all internal API routes.

The cluster ID is a shared, non-zero 128-bit value encoded as 32 lowercase
hexadecimal characters. The first startup generates and persists one when it is
omitted. To form a cluster, configure the same ID on every node before its first
startup. A configured value that differs from persisted metadata fails startup.

Static routing is disabled by default. Enabling it activates deterministic shard
ownership only while every configured peer is healthy and reports matching
epoch, membership, catalog, and placement fingerprints. Multi-node public
distributed data routes additionally require those exact fingerprints in a
committed metadata-Raft view; static convergence alone is not authoritative.
Create identical collection catalogs on every node before enabling it; schema
mutation and legacy single-node data routes are blocked while it is active.
The replication factor defaults to one, must be positive, and must not exceed
the converged cluster size. Every node must configure the same value. Static
activation persists immutable ownership for both the deterministic leader and
all follower replicas; changing the factor requires a new ownership workflow.
A single-node deployment with replication factor one is inherently
authoritative because it has no remote placement or quorum dependency.

Placement capacity defaults to one and accepts relative integer weights from 1
through 256. Configure larger values on nodes with proportionally greater disk,
memory, and I/O capacity. Nodes advertise the value during discovery, include
it in placement convergence, and use it for routing, replication, repair, and
join/leave rebalance planning. Configure it consistently before cluster
formation or node join. Changing an existing member's capacity is fail-closed
because it alters the placement digest; an explicit committed capacity-change
workflow is not yet available.

## Secret handling

Configuration stores only the API-key file path, never the credential itself.
Configuration and secret changes currently require a process restart; dynamic
reload and secret rotation are not yet implemented.
