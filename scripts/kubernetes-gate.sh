#!/usr/bin/env bash
set -euo pipefail

cluster_name="${KIND_CLUSTER_NAME:-gideondb-gate}"
keep_cluster="${KEEP_CLUSTER:-false}"
gate_tmp="$(mktemp -d -t gideondb-kubernetes-gate.XXXXXX)"

cleanup() {
  if [[ "$keep_cluster" != "true" ]]; then
    kind delete cluster --name "$cluster_name" >/dev/null 2>&1 || true
  fi
  rm -r "$gate_tmp"
}
trap cleanup EXIT

for command_name in kind docker kubectl openssl jq; do
  command -v "$command_name" >/dev/null || {
    echo "required command not found: $command_name" >&2
    exit 2
  }
done

kind_config="$gate_tmp/kind.yaml"
cat >"$kind_config" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
  - role: worker
  - role: worker
YAML

kind create cluster --name "$cluster_name" --config "$kind_config" --wait 120s
docker build -t gideondb:dev .
kind load docker-image gideondb:dev --name "$cluster_name"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj '/CN=GideonDB Kubernetes Gate CA' \
  -keyout "$gate_tmp/ca.key" -out "$gate_tmp/ca.crt" >/dev/null 2>&1
cat >"$gate_tmp/cert.conf" <<'EOF'
[req]
distinguished_name = dn
prompt = no
req_extensions = extensions
[dn]
CN = gideondb
[extensions]
subjectAltName = @alt_names
[alt_names]
DNS.1 = gideondb
DNS.2 = gideondb.gideondb.svc
DNS.3 = gideondb.gideondb.svc.cluster.local
DNS.4 = gideondb-headless
DNS.5 = gideondb-headless.gideondb.svc
DNS.6 = gideondb-headless.gideondb.svc.cluster.local
DNS.7 = *.gideondb-headless.gideondb.svc.cluster.local
EOF
openssl req -new -newkey rsa:2048 -nodes -config "$gate_tmp/cert.conf" \
  -keyout "$gate_tmp/tls.key" -out "$gate_tmp/tls.csr" >/dev/null 2>&1
openssl x509 -req -days 1 -sha256 -in "$gate_tmp/tls.csr" \
  -CA "$gate_tmp/ca.crt" -CAkey "$gate_tmp/ca.key" -CAcreateserial \
  -extfile "$gate_tmp/cert.conf" -extensions extensions \
  -out "$gate_tmp/tls.crt" >/dev/null 2>&1

kubectl apply -f deployments/kubernetes/namespace.yaml
api_key="kubernetes-gate-credential-0123456789"
cluster_id="0123456789abcdef0123456789abcdef"
kubectl -n gideondb create secret generic gideondb-credentials \
  --from-literal="cluster-id=$cluster_id" --from-literal="api-key=$api_key"
kubectl -n gideondb create secret generic gideondb-tls \
  --from-file="ca.crt=$gate_tmp/ca.crt" \
  --from-file="tls.crt=$gate_tmp/tls.crt" \
  --from-file="tls.key=$gate_tmp/tls.key"
kubectl apply -k deployments/kubernetes
kubectl -n gideondb rollout status statefulset/gideondb --timeout=10m

cat >"$gate_tmp/client.yaml" <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: gideondb-gate-client
  namespace: gideondb
spec:
  restartPolicy: Never
  containers:
    - name: curl
      image: curlimages/curl:8.12.1
      command: ["sleep", "3600"]
      volumeMounts:
        - name: tls
          mountPath: /run/tls
          readOnly: true
  volumes:
    - name: tls
      secret:
        secretName: gideondb-tls
YAML
kubectl apply -f "$gate_tmp/client.yaml"
kubectl -n gideondb wait --for=condition=Ready pod/gideondb-gate-client --timeout=3m

request_node() {
  local ordinal="$1"
  kubectl -n gideondb exec gideondb-gate-client -- \
    curl --silent --show-error --fail --cacert /run/tls/ca.crt \
    -H "Authorization: Bearer $api_key" \
    "https://gideondb-${ordinal}.gideondb-headless.gideondb.svc.cluster.local:6333/v1/node"
}

readiness="$(kubectl -n gideondb exec gideondb-gate-client -- \
  curl --silent --show-error --fail --cacert /run/tls/ca.crt \
  -H "Authorization: Bearer $api_key" \
  https://gideondb.gideondb.svc.cluster.local:6333/v1/cluster/readiness)"
[[ "$(jq -r '.ready' <<<"$readiness")" == "true" ]]
[[ "$(kubectl -n gideondb get pods -l app.kubernetes.io/name=gideondb -o json | jq '[.items[] | select(.status.conditions[]? | select(.type == "Ready" and .status == "True"))] | length')" == "3" ]]

for attempt in $(seq 1 60); do
  leader_ordinal=""
  for ordinal in 0 1 2; do
    node="$(request_node "$ordinal")"
    if [[ "$(jq -r '.raft_role' <<<"$node")" == "leader" ]]; then
      leader_ordinal="$ordinal"
      old_leader_id="$(jq -r '.node_id' <<<"$node")"
      break
    fi
  done
  [[ -n "$leader_ordinal" ]] && break
  sleep 1
done
[[ -n "${leader_ordinal:-}" ]]

disruptions="$(kubectl -n gideondb get pdb/gideondb -o json | jq -r '.status.disruptionsAllowed')"
(( disruptions >= 1 ))
kubectl -n gideondb delete pod "gideondb-$leader_ordinal" --wait=true

for attempt in $(seq 1 90); do
  replacement_id=""
  for ordinal in 0 1 2; do
    [[ "$ordinal" == "$leader_ordinal" ]] && continue
    if node="$(request_node "$ordinal" 2>/dev/null)" && [[ "$(jq -r '.raft_role' <<<"$node")" == "leader" ]]; then
      replacement_id="$(jq -r '.node_id' <<<"$node")"
      break
    fi
  done
  [[ -n "$replacement_id" ]] && break
  sleep 1
done
[[ -n "${replacement_id:-}" && "$replacement_id" != "$old_leader_id" ]]

kubectl -n gideondb rollout status statefulset/gideondb --timeout=10m
[[ "$(kubectl -n gideondb get pods -l app.kubernetes.io/name=gideondb -o json | jq '[.items[] | select(.status.conditions[]? | select(.type == "Ready" and .status == "True"))] | length')" == "3" ]]
echo "Kubernetes gate passed: leader $old_leader_id transferred to $replacement_id and the cluster returned to three ready pods."

