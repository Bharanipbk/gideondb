#!/usr/bin/env bash
set -euo pipefail

test_tmp="$(mktemp -d -t gideondb-tls-rotation-test.XXXXXX)"
cleanup() { rm -r "$test_tmp"; }
trap cleanup EXIT

openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj '/CN=rotation-test-ca' \
  -keyout "$test_tmp/ca.key" -out "$test_tmp/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=gideondb' \
  -keyout "$test_tmp/tls.key" -out "$test_tmp/tls.csr" >/dev/null 2>&1
openssl x509 -req -days 2 -in "$test_tmp/tls.csr" -CA "$test_tmp/ca.crt" \
  -CAkey "$test_tmp/ca.key" -CAcreateserial -out "$test_tmp/tls.crt" >/dev/null 2>&1
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
  -out "$test_tmp/wrong.key" >/dev/null 2>&1

mkdir "$test_tmp/bin"
cat >"$test_tmp/bin/kubectl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' 'apiVersion: v1' 'kind: Secret' 'metadata:' '  name: gideondb-tls'
EOF
chmod +x "$test_tmp/bin/kubectl"

PATH="$test_tmp/bin:$PATH" ./scripts/rotate-kubernetes-tls.sh \
  --ca-bundle "$test_tmp/ca.crt" --cert "$test_tmp/tls.crt" \
  --key "$test_tmp/tls.key" --dry-run | grep -q 'kind: Secret'

if PATH="$test_tmp/bin:$PATH" ./scripts/rotate-kubernetes-tls.sh \
  --ca-bundle "$test_tmp/ca.crt" --cert "$test_tmp/tls.crt" \
  --key "$test_tmp/wrong.key" --dry-run >/dev/null 2>&1; then
  echo "mismatched private key was accepted" >&2
  exit 1
fi

echo "Kubernetes TLS rotation helper tests passed."
