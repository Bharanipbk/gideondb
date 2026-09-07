# Compatibility and data migration

GideonDB follows semantic versioning, but releases remain pre-1.0. This page is
the compatibility contract for public APIs, cluster protocols, and files in the
data directory.

## Pre-1.0 release policy

- Patch releases within the same minor line preserve the documented `/v1` REST
  contract and all persistent formats written by that minor release.
- A minor release may make a breaking API or configuration change. Its release
  notes must identify the change, replacement, and required operator action.
- Fields and endpoints are deprecated for at least one minor release when safe
  dual behavior is practical. Security, corruption, or data-loss fixes may
  remove unsafe behavior immediately and will be called out prominently.
- Documented error `code` values are stable within a minor line. Human-readable
  error messages are not an integration contract.
- Internal `/v1/internal/*` routes are negotiated cluster transport, not a
  public client API.

## Current compatibility matrix

| Surface | Current writer | Current reader | Guarantee |
| --- | --- | --- | --- |
| REST API | `/v1` | `/v1` | Patch-compatible within a minor line |
| Cluster protocol | v2 | v1–v2 | Rolling overlap; incompatible peers fail readiness |
| WAL | version 1 | version 1 | Unknown versions fail closed |
| Checkpoint manifest | format 3 | formats 1–3 | Formats 1 and 2 migrate through normal checkpoint publication |
| Record/vector/tombstone columns | version 1 | version 1 | Checksummed little-endian envelope |
| HNSW graph | version 1 | version 1 or absent | A missing legacy graph is rebuilt |
| Metadata filter index | version 1 | version 1 or absent | A missing legacy index is rebuilt |
| Catalog | format 1 | format 1 | Unknown format fails startup |
| Idempotency ledger | format 1 | format 1 | Unknown or corrupt entries fail startup |
| Node/cluster/Raft/ownership state | format 1 | format 1 | Node-local, not portable backup data |
| Backup archive | format 1 | format 1 | Same minor line unless release notes say otherwise |

Readers validate magic, versions, reserved fields, sizes, path safety,
checksums, shard routing, dimensions, LSN ordering, and record counts. GideonDB
does not silently reinterpret an unknown format.

## Offline migration

Stop the node and take a verified backup with the currently running binary.
Then run:

```bash
gideondb -data-path ./data -verify-data
gideondb -data-path ./data -migrate-data
gideondb -data-path ./data -verify-data
```

`-migrate-data` is offline and mutually exclusive with server, backup, restore,
and verification modes. It validates the complete data path and rewrites owned
format-1 or format-2 checkpoints through the atomic format-3 publisher. Current
format-3 checkpoints are unchanged. The operation is restart-safe: the old
manifest remains authoritative until its replacement is durably renamed.

For a cluster, migrate one compatible voter at a time using the rolling-upgrade
procedure. Do not migrate every replica simultaneously or combine a binary
upgrade with membership, capacity, or replication-factor changes.

## Downgrades

Do not start an older binary after a newer binary has written a format it does
not advertise as readable. Restore the pre-upgrade backup with the older binary
instead. Copying selected files, editing manifests, or lowering format numbers
is unsupported and can lose data.
