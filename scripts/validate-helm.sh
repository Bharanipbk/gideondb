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

# Observability integrations render only when enabled.
"$helm" template gideondb "$chart" --namespace gideondb \
  --set observability.serviceMonitor.enabled=true --set observability.prometheusRule.enabled=true \
  --set observability.tracing.otlpEndpoint=http://otel:4318 --set observability.tracing.headersSecret.name=otlp > "$out/observability.yaml"
grep -q '^kind: ServiceMonitor$' "$out/observability.yaml"
grep -q 'type: Bearer' "$out/observability.yaml"
grep -q '^kind: PrometheusRule$' "$out/observability.yaml"
grep -q 'alert: GideonDBReplicationLag' "$out/observability.yaml"
grep -q 'OTEL_EXPORTER_OTLP_ENDPOINT' "$out/observability.yaml"
grep -q 'OTEL_EXPORTER_OTLP_HEADERS' "$out/observability.yaml"
if grep -q 'OTEL_EXPORTER_OTLP' "$out/rendered.yaml"; then
  echo "tracing env must be absent by default" >&2
  exit 1
fi
cmp -s "$chart/files/prometheus-rules.yaml" deployments/observability/prometheus-rules.yaml || {
  echo "deployments/observability/prometheus-rules.yaml and the chart copy differ" >&2
  exit 1
}
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
