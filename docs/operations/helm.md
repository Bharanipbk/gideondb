# Helm chart

**Maturity:** Experimental

`deployments/helm/gideondb` packages the same topology as the
[Kubernetes manifests](kubernetes.md): a StatefulSet with one PVC per node, a
headless Service for peer discovery, a readiness-gated client Service, TLS
and credential Secrets, startup/readiness/liveness probes on `/v1/health` and
`/v1/ready`, a quorum PodDisruptionBudget, host anti-affinity and an optional
NetworkPolicy.

## Single node

```bash
helm install gideondb deployments/helm/gideondb \
  -f deployments/helm/gideondb/values-single-node.yaml
```

This starts one pod with a self-signed certificate kept in its data volume and
generates the API key, cluster ID and dashboard password into a Secret named
after the release. `helm status gideondb` prints the commands to read them and
to port-forward:

```bash
kubectl port-forward svc/gideondb 6333
export GIDEONDB_API_KEY="$(kubectl get secret gideondb -o jsonpath='{.data.api-key}' | base64 -d)"
curl -k -H "Authorization: Bearer $GIDEONDB_API_KEY" https://127.0.0.1:6333/v1/collections
```

## Three-node cluster

Clusters require a private CA so nodes can verify each other (mutual TLS on
`/v1/internal/*`). Issue a node certificate whose SANs cover every pod's
stable name, `<fullname>-<ordinal>.<fullname>-headless.<namespace>.svc.cluster.local`.
With release name `gideondb` in namespace `gideondb`, `<fullname>` is
`gideondb`; otherwise it is `<release>-gideondb`.

```bash
kubectl create namespace gideondb
kubectl -n gideondb create secret generic gideondb-tls \
  --from-file=ca.crt=./ca.crt --from-file=tls.crt=./tls.crt --from-file=tls.key=./tls.key
helm install gideondb deployments/helm/gideondb -n gideondb \
  -f deployments/helm/gideondb/values-cluster.yaml
```

`values-cluster.yaml` sets three replicas, replication factor 3, hard
anti-affinity (three schedulable nodes needed), a `minAvailable: 2`
disruption budget, 100 GiB volumes and the NetworkPolicy. The chart fills in
`GIDEONDB_PEERS` with every ordinal's URL and enables static routing.

### Cluster bootstrap

As with the plain manifests, the chart starts discoverable processes but does
not invent Raft membership. Bootstrap the first PVC as the initial voter, then
add the other node IDs with the join workflow in
[rebalancing](../architecture/rebalancing.md). Scale-out and scale-in are not
plain `helm upgrade --set replicaCount=`: follow the join/leave and shard
migration steps first, as described in [Kubernetes](kubernetes.md#bootstrap-and-operation).

## Values

| Key | Default | Notes |
|---|---|---|
| `replicaCount` | `1` | More than 1 requires `tls.mode=secret` |
| `image.repository` / `tag` / `digest` | `ghcr.io/bharanipbk/gideondb`, appVersion | Pin `digest` in production |
| `cluster.id` | generated | 32 lowercase hex characters, shared by all nodes |
| `cluster.replicationFactor` | `min(replicaCount, 3)` | Cannot exceed `replicaCount` |
| `auth.apiKey` | generated | At least 16 characters |
| `auth.existingSecret` | — | Secret with `cluster-id`, `api-key`, `dashboard-password` keys; the chart then creates none |
| `dashboard.username` / `password` | `admin` / generated | Password applies only to a new data directory |
| `tls.mode` | `selfSigned` | `selfSigned` or `secret` |
| `tls.secretName` | — | Secret with `tls.crt`, `tls.key`, `ca.crt` |
| `config.walSync`, `config.checkpointEvery` | `always`, `1000` | See [configuration](configuration.md) |
| `config.extraArgs`, `config.extraEnv` | `[]` | e.g. `-grpc-address=0.0.0.0:6334`, `GIDEONDB_EMBEDDERS_FILE` |
| `persistence.size` / `storageClass` | `20Gi` / cluster default | PVCs are kept on uninstall |
| `resources` | 500m / 1Gi requests | Size for your data; see [index memory calibration](../benchmarks/index-memory-calibration.md) |
| `podDisruptionBudget.minAvailable` | quorum | Created only when `replicaCount > 1` |
| `podAntiAffinity` | `hard` | `hard`, `soft` or `none` |
| `networkPolicy.enabled` | `false` | Allows pod-to-pod and labelled client namespaces |
| `probes.*` | as in the manifests | Startup allows ten minutes for recovery |

The chart refuses, at template time, combinations the server would reject at
startup: plaintext HTTP (the dashboard needs HTTPS off loopback), several
nodes without a CA, a short API key or a malformed cluster ID.

Generated credentials are looked up on upgrade, so they stay stable, and the
Secret carries `helm.sh/resource-policy: keep` so a reinstall can reopen the
retained volumes. `helm template` cannot look them up and generates new
values each run; use `auth.existingSecret` with GitOps tools that render
offline.

The dashboard password reaches the pod as an environment variable from the
Secret rather than a file, because the server rejects group-readable password
files and `fsGroup` makes Secret files group-readable.

## Validation

```bash
make validate-helm
```

runs `helm lint --strict` and `helm template` for the default, single-node and
cluster values and checks that invalid values fail. CI runs it with a pinned,
checksum-verified Helm release. It does not contact a cluster; the
[Kubernetes gate](kubernetes.md#automated-deployment-gate) covers live
behaviour for the plain manifests.
