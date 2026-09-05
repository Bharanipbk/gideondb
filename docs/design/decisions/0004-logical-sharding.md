# ADR-0004: Use logical shards from the first release

## Status

Proposed

## Context

Retrofitting shard ownership after building collection-wide storage would
rewrite durability, concurrency, routing and APIs.

## Decision

Every collection has a fixed count of logical shards. Hash canonical tenant,
namespace and ID to a shard. Physical node placement is a separate table.

## Alternatives

Collection-wide storage is simpler initially but blocks incremental placement.
Physical-node hashing causes ownership changes whenever membership changes.

## Advantages

Isolation of WALs and compaction, parallel recovery/search, and stable units for
replication and migration.

## Disadvantages

More files, fanout and coordination on one node; fixed shard counts can become
poorly sized.

## Consequences

Cross-shard batches are not atomic. Shard count is immutable initially; future
online resharding needs a versioned routing-map protocol. Loss of one shard can
produce partial search only when the client explicitly opts in.
