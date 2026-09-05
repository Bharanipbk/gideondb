# Network partition behavior

VectorDB metadata uses majority Raft quorum. In a three-voter cluster, the
two-node side of a partition can elect a leader and commit metadata changes.
An isolated voter cannot commit placement, membership, or capacity changes and
steps down when it cannot renew quorum.

Data writes additionally require the configured replica acknowledgement level.
A request fails closed when its leader cannot reach the required replicas. Do
not retry an `unknown` distributed write outcome blindly; read the record or
reconcile by application idempotency key first.

After connectivity returns, the current leader repairs conflicting uncommitted
metadata suffixes and lagging replicas are reconciled by sequence comparison
and snapshot installation. Operators should wait for `/v1/cluster/readiness`
to report `ready: true` before starting membership or capacity changes.

The deterministic chaos gate repeatedly isolates each current leader, verifies
minority proposal rejection, commits on the majority, heals the links, and
checks that every voter converges without metadata epoch regression.
