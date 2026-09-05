# Glossary

- **Collection**: Schema and policy boundary for vectors of one dimension and
  metric.
- **Logical shard**: Stable partition of a collection, independent of physical
  node placement.
- **Replica**: A physical copy of a logical shard.
- **Segment**: Bounded storage and search unit within a shard. Immutable after
  sealing, except for sidecar tombstone state.
- **Active segment**: Mutable in-memory segment receiving acknowledged writes.
- **WAL**: Per-shard write-ahead log used for durability and recovery.
- **LSN**: Monotonically increasing per-shard log sequence number.
- **Manifest**: Checksummed description of the segments constituting a shard.
- **Tombstone**: Logical deletion retained until compaction.
- **Coordinator**: Request-scoped role that routes work and merges results; it
  does not own user data.
- **Placement epoch**: Monotonic cluster-metadata version used to reject stale
  routing and leadership decisions.
