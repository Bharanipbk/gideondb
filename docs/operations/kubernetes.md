# Kubernetes deployment

The manifests in `deployments/kubernetes` run a three-node experimental
GideonDB cluster. They use one StatefulSet PVC per durable node identity, a
headless Service for peer discovery, a readiness-gated client Service,
credential and private-CA TLS Secrets, mandatory host anti-affinity, a quorum-preserving
PodDisruptionBudget, and restricted ingress.

## Prerequisites

- Kubernetes 1.27 or newer
- three schedulable worker nodes (required by host anti-affinity)
- a default StorageClass providing `ReadWriteOnce` volumes
- a GideonDB image available to every node
- a private CA and node certificate whose SANs cover all three stable pod DNS
  names under `gideondb-headless.gideondb.svc.cluster.local`

The checked-in image is `gideondb:dev`. Change it with a Kustomize image
override or load that local tag into every development-cluster node.

## Create required Secrets

The manifests deliberately do not contain credentials. Create the namespace,
then supply one shared non-zero 32-character lowercase hexadecimal cluster ID,
a random bearer token of at least 16 characters, and the CA-signed identity:

```bash
kubectl apply -f deployments/kubernetes/namespace.yaml
kubectl -n gideondb create secret generic gideondb-credentials \
  --from-literal=cluster-id=0123456789abcdef0123456789abcdef \
  --from-literal=api-key='replace-with-a-random-production-token'
kubectl -n gideondb create secret generic gideondb-tls \
  --from-file=ca.crt=./ca.crt --from-file=tls.crt=./tls.crt \
  --from-file=tls.key=./tls.key
kubectl apply -k deployments/kubernetes
```

Never reuse the example cluster ID or token in production. Secret volumes are
projected read-only and made readable only by the pod's non-root process group.
Internal discovery and fanout present the node certificate and validate peers
against `ca.crt`. Every `/v1/internal/*` request additionally requires a
verified client certificate. Probe and public API routes use TLS but do not
require a client certificate; public APIs remain protected by the bearer key.

Rotate mounted TLS material with `scripts/rotate-kubernetes-tls.sh`. It checks
expiry, issuer trust, and certificate/key agreement before atomically updating
the Secret. Leaf rotation and the required three-stage overlap for changing a
CA are documented in the [security runbook](security.md#certificate-lifecycle-and-rotation).
Because the StatefulSet uses a projected Secret volume rather than `subPath`,
new handshakes adopt the updated files without restarting the pods.

## Probe semantics

- Startup calls `/v1/health` for up to ten minutes, allowing WAL and segment
  recovery to complete before liveness begins.
- Liveness calls `/v1/health` and detects a nonresponsive server; it does not
  restart a node merely because peers are unavailable.
- Readiness calls the detail-free `/v1/ready`. With static routing enabled it
  requires a converged cluster view and successful local ownership activation.
  Only ready pods enter the client Service.

The headless Service publishes unready addresses to avoid a discovery/readiness
cycle. Every pod receives the same stable ordinal seed list; discovery removes
the seed that resolves to its own durable identity.

## Bootstrap and operation

The manifests start three discoverable processes but intentionally do not
invent Raft membership. Bootstrap the first PVC as the initial voter, then use
the authenticated join prepare/apply workflow documented in
[rebalancing](../architecture/rebalancing.md) to add the other two durable node
IDs. Do not independently initialize three unrelated voter sets.

Before maintenance, verify authenticated `/v1/cluster/readiness`, then update
one pod at a time. The disruption budget permits only one voluntary outage, and
required anti-affinity prevents two database replicas sharing a worker failure
domain. Follow the [rolling-upgrade runbook](rolling-upgrades.md).

On `SIGTERM`, a pod immediately fails readiness and rejects new application
traffic while retaining Raft RPC availability. If it is the metadata leader,
it catches up the first healthy voter in canonical node-ID order and sends a
term-fenced `timeout-now` request. After the replacement election completes—or
after the bounded transfer attempt fails—it stops background loops and drains
in-flight HTTP requests. The StatefulSet's 60-second grace period exceeds the
10-second transfer and 20-second HTTP shutdown budgets.

Scale-out is not a plain `kubectl scale`: create the new PVC/pod, perform the
durable join and shard-migration workflow, and only then increase serving
capacity. Scale-in requires the symmetric leave workflow before deleting a pod
or PVC. Kubernetes does not delete StatefulSet PVCs by default; retain them for
rollback until the leave and backup checks pass.

## Network access

The NetworkPolicy allows port 6333 between GideonDB pods and from namespaces
labelled for client access:

```bash
kubectl label namespace my-application gideondb-client-access=true
```

Your CNI must enforce NetworkPolicy. Egress remains unrestricted so cluster DNS
continues to work. Expose the client Service externally only through a
TLS-terminating load balancer or ingress whose threat model preserves bearer
credentials.

## Customization

Review image digest, StorageClass, volume size, CPU/memory requests, topology
keys, replication factor, placement capacity, and backup policy before
production use. Keep the StatefulSet replica count, static seed list,
replication factor, and quorum policy mutually consistent. Resource limits in
the base are safe examples, not universal sizing recommendations.

## Automated deployment gate

Run `make validate-kubernetes` on a host with Docker, kind, kubectl, OpenSSL,
and jq. The gate creates a disposable four-node kind environment, builds and
loads the current GideonDB image, generates a short-lived private CA and node
identity, deploys the exact Kustomize base, and verifies:

- all three StatefulSet members converge and become ready;
- authenticated cluster readiness is true over the generated CA;
- the disruption budget permits at most the quorum-safe voluntary outage;
- terminating the current metadata leader elects a different leader; and
- the terminated ordinal rejoins and the cluster returns to three ready pods.

The cluster is deleted on exit. Set `KEEP_CLUSTER=true` to retain a failed
environment for investigation and `KIND_CLUSTER_NAME` to choose its name.

Automatic GitHub execution is currently deferred. Run the gate manually before
Kubernetes-related releases or after changes to the server, Docker image,
manifests, or gate script. Restoring an appropriately scoped CI job is tracked
in [pending development](../../PENDING_DEVELOPMENT.md).
