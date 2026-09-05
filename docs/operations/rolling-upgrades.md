# Rolling upgrades

Each node advertises `min_protocol_version` and `protocol_version` from
`GET /v1/node`. Discovery selects the newest version shared by both ranges and
reports it as `negotiated_protocol` in the peer view.

The current release speaks protocol v2 and remains compatible with v1. Peers
from releases that predate protocol advertisement are treated as v1. A peer
whose range does not overlap is marked unhealthy and cannot satisfy cluster
readiness, preventing routing or metadata changes on a mixed incompatible
cluster.

Upgrade one voter at a time:

1. Confirm `/v1/cluster/readiness` is ready before beginning.
2. Stop one non-leader voter, replace the binary, and restart it with the same
   data path and configuration.
3. Wait until its peer record is healthy and exposes a non-zero
   `negotiated_protocol`.
4. Confirm cluster readiness again before proceeding to the next voter.
5. Upgrade the leader last, allowing leadership to move before stopping it.

Do not combine a rolling binary upgrade with a join, leave, capacity change,
or replication-factor change. If a peer reports an incompatible protocol,
restore the previous compatible binary rather than forcing readiness.
