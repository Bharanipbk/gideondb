# Three-node static-cluster functional gate

**Maturity:** Implemented correctness validation; not a performance benchmark.

The automated `TestThreeNodeStaticClusterPartitionsWritesSearchesAndRecovers`
gate constructs three independent engines with separate temporary data
directories, stable node IDs, a shared cluster ID, identical collection
catalogs, converged view fingerprints, and static routing enabled. Requests
travel through the real REST handlers using an in-process HTTP transport so the
test is deterministic and does not require host networking.

The workload creates one record for each of 24 logical shards and submits all
records to node A's distributed batch coordinator. It verifies:

- every shard reports a committed outcome;
- rendezvous placement gives all three nodes at least one shard;
- each record is readable from exactly its assigned owner while both non-owners
  reject the shard;
- every node persists its ownership manifest and has no WAL file for any
  unowned shard;
- a distributed top-3 search coordinated by node C fans out to all owners and
  returns the correct global score order with authoritative static placement;
- after closing and reopening all three engines, every owner recovers its
  assigned records, non-owners still reject access, and unowned WALs remain
  absent.

This gate validates partitioned durable shard materialization and coordination.
It does not exercise real sockets, process crashes, network partitions, TLS, or
consensus. Those remain separate gates before a production distributed claim is
appropriate.

Run it with:

```bash
go test ./internal/api/rest \
  -run TestThreeNodeStaticClusterPartitionsWritesSearchesAndRecovers -count=1
```
