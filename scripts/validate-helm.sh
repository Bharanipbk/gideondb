#!/usr/bin/env bash
# Lint and render the Helm chart for every supported values file, and check
# that invalid combinations fail at template time. Needs only the helm CLI;
# no cluster is contacted.
set -euo pipefail

helm="${HELM:-helm}"
chart="deployments/helm/gideondb"
out="$(mktemp -d -t gideondb-helm.XXXXXX)"
trap 'rm -r "$out"' EXIT

for values in "" values-single-node.yaml values-cluster.yaml; do
  args=()
  [[ -n "$values" ]] && args=(-f "$chart/$values")
  "$helm" lint --strict "$chart" ${args[@]+"${args[@]}"}
  "$helm" template gideondb "$chart" --namespace gideondb ${args[@]+"${args[@]}"} > "$out/rendered.yaml"
  grep -q '^kind: StatefulSet$' "$out/rendered.yaml"
  grep -q 'path: /v1/ready' "$out/rendered.yaml"
done

grep -q '^kind: PodDisruptionBudget$' "$out/rendered.yaml"
grep -q 'GIDEONDB_PEERS' "$out/rendered.yaml"

expect_failure() {
  if "$helm" template gideondb "$chart" "$@" > /dev/null 2> "$out/error.txt"; then
    echo "expected helm template to fail for: $*" >&2
    exit 1
  fi
}
expect_failure --set replicaCount=3
expect_failure --set tls.mode=none
expect_failure --set auth.apiKey=short
expect_failure --set cluster.id=not-hex
echo "helm chart validation passed"
