# Configuration management

Configuration is resolved in this order, with later layers winning:

1. Built-in defaults
2. Strict JSON file selected by `-config`
3. `VECTORDB_*` environment variables
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
  "data_path": "/var/lib/vectordb",
  "wal_sync": "always",
  "checkpoint_every": 1000,
  "replication_factor": 2,
  "placement_capacity": 1,
  "api_key_file": "/run/secrets/vectordb-api-key",
  "allow_unauthenticated": false,
  "allow_insecure_http": false,
  "enable_static_routing": false,
  "tls_cert_file": "/run/tls/tls.crt",
  "tls_key_file": "/run/tls/tls.key",
  "tls_ca_file": "/run/tls/ca.crt"
}
```

Start with:

```bash
vectordb -config /etc/vectordb/config.json
```

## Environment variables

- `VECTORDB_HTTP_ADDRESS`
- `VECTORDB_ADVERTISE_ADDRESS`
- `VECTORDB_CLUSTER_ID`
- `VECTORDB_PEERS` (comma-separated HTTP(S) base URLs)
- `VECTORDB_DATA_PATH`
- `VECTORDB_WAL_SYNC`
- `VECTORDB_CHECKPOINT_EVERY`
- `VECTORDB_REPLICATION_FACTOR`
- `VECTORDB_PLACEMENT_CAPACITY`
- `VECTORDB_API_KEY_FILE`
- `VECTORDB_ALLOW_UNAUTHENTICATED`
- `VECTORDB_ALLOW_INSECURE_HTTP`
- `VECTORDB_ENABLE_STATIC_ROUTING`
- `VECTORDB_TLS_CERT_FILE`
- `VECTORDB_TLS_KEY_FILE`
- `VECTORDB_TLS_CA_FILE`

Environment booleans use Go boolean syntax such as `true` or `false`.

`tls_ca_file` requires the server certificate and key. It enables private-CA
verification for outbound node traffic and verified client-certificate
enforcement on all internal API routes.

The cluster ID is a shared, non-zero 128-bit value encoded as 32 lowercase
hexadecimal characters. The first startup generates and persists one when it is
omitted. To form a cluster, configure the same ID on every node before its first
startup. A configured value that differs from persisted metadata fails startup.

Static routing is disabled by default. Enabling it activates deterministic shard
ownership only while every configured peer is healthy and reports matching
epoch, membership, catalog, and placement fingerprints. It is intended for
fixed-topology functional clusters and is not a replacement for consensus.
Create identical collection catalogs on every node before enabling it; schema
mutation and legacy single-node data routes are blocked while it is active.
The replication factor defaults to one, must be positive, and must not exceed
the converged cluster size. Every node must configure the same value. Static
activation persists immutable ownership for both the deterministic leader and
all follower replicas; changing the factor requires a new ownership workflow.

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
