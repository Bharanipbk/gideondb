#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: rotate-kubernetes-tls.sh --ca-bundle PATH --cert PATH --key PATH [options]

Atomically updates the GideonDB Kubernetes TLS Secret after validating that the
certificate, private key, and CA bundle agree.

Options:
  --namespace NAME   Kubernetes namespace (default: gideondb)
  --secret NAME      TLS Secret name (default: gideondb-tls)
  --ca-bundle PATH   PEM trust bundle to publish as ca.crt
  --cert PATH        PEM server/node certificate chain to publish as tls.crt
  --key PATH         PEM private key to publish as tls.key
  --dry-run          Validate inputs and print the generated Secret; do not apply
  -h, --help         Show this help
EOF
}

namespace="gideondb"
secret_name="gideondb-tls"
ca_bundle=""
certificate=""
private_key=""
dry_run="false"

while (($#)); do
  case "$1" in
    --namespace) namespace="${2:?missing namespace}"; shift 2 ;;
    --secret) secret_name="${2:?missing secret name}"; shift 2 ;;
    --ca-bundle) ca_bundle="${2:?missing CA bundle path}"; shift 2 ;;
    --cert) certificate="${2:?missing certificate path}"; shift 2 ;;
    --key) private_key="${2:?missing private-key path}"; shift 2 ;;
    --dry-run) dry_run="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

for command_name in openssl kubectl; do
  command -v "$command_name" >/dev/null || { echo "required command not found: $command_name" >&2; exit 2; }
done
for required in "$ca_bundle" "$certificate" "$private_key"; do
  [[ -n "$required" && -r "$required" ]] || { echo "required input is not readable: ${required:-<unset>}" >&2; exit 2; }
done

openssl x509 -in "$certificate" -noout -checkend 86400 >/dev/null || {
  echo "certificate is invalid or expires within 24 hours" >&2
  exit 1
}
openssl verify -CAfile "$ca_bundle" "$certificate" >/dev/null

cert_public="$(openssl x509 -in "$certificate" -pubkey -noout | openssl pkey -pubin -outform DER | openssl sha256)"
key_public="$(openssl pkey -in "$private_key" -pubout -outform DER | openssl sha256)"
[[ "$cert_public" == "$key_public" ]] || { echo "certificate and private key do not match" >&2; exit 1; }

secret_yaml="$(kubectl -n "$namespace" create secret generic "$secret_name" \
  --from-file="ca.crt=$ca_bundle" \
  --from-file="tls.crt=$certificate" \
  --from-file="tls.key=$private_key" \
  --dry-run=client -o yaml)"

if [[ "$dry_run" == "true" ]]; then
  printf '%s\n' "$secret_yaml"
  exit 0
fi

printf '%s\n' "$secret_yaml" | kubectl apply -f -
echo "Updated Secret $namespace/$secret_name. New GideonDB TLS handshakes reload the projected files without restart."
