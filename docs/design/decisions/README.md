# Architecture decision records

ADRs are numbered, reviewed design contracts. Proposed records can change;
accepted records are superseded by a new ADR rather than rewritten.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-use-go.md) | Go for the core server | Proposed |
| [0002](0002-segment-based-storage.md) | Immutable segment-based storage | Proposed |
| [0003](0003-hnsw-first-ann-index.md) | Flat oracle and HNSW first ANN | Proposed |
| [0004](0004-logical-sharding.md) | Logical shards from the first release | Proposed |
| [0005](0005-cluster-metadata-consensus.md) | Raft for cluster metadata only | Proposed |
| [0006](0006-replication-model.md) | Leader-based per-shard replication | Proposed |
| [0007](0007-memory-layout.md) | Ordinal-based contiguous layouts | Proposed |
| [0008](0008-wal-durability.md) | Per-shard WAL and explicit sync modes | Proposed |

Every ADR must cover alternatives, trade-offs, performance consequences,
failure consequences, and future consequences.
