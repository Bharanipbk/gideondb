# Backup and restore

## Create a backup

Backups exclude node and cluster identity plus the node-local
`SHARD_OWNERSHIP.json` manifest. A restored copy therefore starts without a
physical ownership constraint and must pass the destination cluster's normal
convergence and ownership activation checks.

```bash
vectordb -data-path ./data -backup-to /backups/vectordb-2026-09-03.tar.gz
```

Backup opens the engine, takes its write lock, checkpoints every logical shard,
and writes a versioned gzip/tar archive. Every regular file carries a SHA-256
checksum. The archive is written to a temporary file, synchronized, renamed
atomically to the requested destination, and never overwrites an existing file.
The destination must be outside the live data directory.

Writes are unavailable while checkpointing and archive traversal run. This is
a consistency-first single-node implementation; large deployments will need
snapshot or reflink integration to shorten the pause.

## Cluster recovery points

The distributed backup safety core captures the committed metadata epoch,
placement digest, canonical capacity manifest, node identity, and durable WAL
sequence of every physically owned shard. Node reports can be merged only when
they share the exact committed view. The merged manifest is sorted
canonically, rejects duplicate node or node/shard reports, and retains replica
sequences independently so restore tooling can select and verify a valid copy.

Each node now provides authenticated internal freeze, recovery-point capture,
and release operations. A freeze is bound to a bounded operation ID and the
normal cluster/node/epoch fence. It drains in-flight data mutations, rejects
new mutations with `backup_in_progress`, permits capture only for the matching
operation, and permits only that operation to release the barrier. This forms
the two-phase safety boundary required by the coordinator.

The Raft leader can coordinate these primitives through
`POST /v1/cluster/backup/recovery-point`. The request supplies one operation ID.
The coordinator requires an authoritative converged view, freezes local and
remote voters, captures every node report, validates and merges the manifest,
and releases all successfully frozen barriers through deferred cleanup on both
success and failure.

While its matching barrier is held, a coordinator can send the captured node
recovery point to `POST /v1/internal/backup/archive`. The node rechecks every
owned shard sequence under the engine lock, creates the archive in a private
temporary path, and streams it as `application/gzip`; callers cannot select a
server filesystem destination. Any sequence drift rejects creation.

The node archive header contains the checksummed recovery point. Restore validates
that metadata and writes `BACKUP_RECOVERY_POINT.json` into the restored data
directory so cluster restore orchestration can verify provenance.

`CreateClusterBackup` packages the canonical cluster recovery manifest and
exactly one checksummed archive for every reported node. `RestoreClusterBackup`
rejects missing, extra, duplicate, corrupt, or unknown-node archives; restores
all nodes into a sibling staging directory; compares every embedded node point
with the outer manifest; and atomically publishes only the completely verified
cluster directory.

Restore planning now selects the highest durable sequence among every backed-up
replica, with source node ID as a deterministic tie-breaker, and maps that
source onto a caller-supplied target replica placement. Old node identities are
never copied into the target. Planning rejects duplicate or malformed target
replica sets and any target shard without a recoverable source.

`MaterializeClusterRestore` verifies that every selected source still matches
the backup epoch, placement digest, capacity manifest, shard, and exact durable
sequence. It then creates fresh target node data paths, activates only the
planned shard ownership, installs each selected snapshot on every new replica,
and atomically publishes the remapped topology only after all targets close
successfully. Empty sequence-zero shards remain empty without synthesizing WAL
history. A failed or forged plan leaves no destination behind.

## Restore

Stop any server using the target path, then run:

```bash
vectordb -data-path ./restored-data \
  -restore-from /backups/vectordb-2026-09-03.tar.gz
```

Restore requires a nonexistent destination. It validates the format header,
entry types, path containment, file count, duplicate filesystem creation, and
every checksum while extracting into a sibling staging directory. Only a fully
validated restore is renamed into place. Existing data is never merged,
deleted, or overwritten.

After restoration, normal engine startup performs its usual manifest, segment,
and WAL validation. Backups remain experimental and carry no cross-version
compatibility promise before the first stable format release.
