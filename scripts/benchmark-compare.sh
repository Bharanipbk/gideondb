#!/usr/bin/env bash
# Compare GideonDB with Qdrant and OpenSearch on one .fvecs dataset.
#
#   scripts/benchmark-compare.sh BASE.fvecs QUERIES.fvecs METRIC OUT.json [extra compare flags]
#
# Starts the three engines with deployments/benchmark/compose.yaml (same CPU
# and memory limits), waits for them, runs `gideondb-loadtest compare`, and
# stops them again. Set KEEP=1 to leave the containers running.
set -euo pipefail
[[ $# -ge 4 ]] || { sed -n 2,8p "$0"; exit 2; }
base=$1 queries=$2 metric=$3 out=$4; shift 4
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
(umask 077; openssl rand -hex 24 > "$work/api-key")
(umask 077; openssl rand -hex 16 > "$work/dashboard-password")
# Docker Desktop bind mounts stay readable with 0600; GideonDB rejects looser modes.
export BENCH_API_KEY_FILE="$work/api-key" BENCH_PASSWORD_FILE="$work/dashboard-password"
compose=(docker compose -f "$root/deployments/benchmark/compose.yaml")
"${compose[@]}" up -d --build --force-recreate
[[ ${KEEP:-0} == 1 ]] || trap '"${compose[@]}" down -v; rm -rf "$work"' EXIT

wait_for() { for _ in $(seq 300); do curl -fsS -o /dev/null "$@" && return 0; sleep 1; done; echo "timeout: $*" >&2; return 1; }
wait_for -k -H "Authorization: Bearer $(cat "$work/api-key")" https://127.0.0.1:16333/v1/ready
wait_for http://127.0.0.1:16334/readyz
wait_for "http://127.0.0.1:19201/_cluster/health?wait_for_status=yellow&timeout=1s"

go build -o "$work/" "$root/cmd/gideondb-loadtest"
"$work/gideondb-loadtest" compare -gideondb-api-key-file "$work/api-key" \
  -base "$base" -queries "$queries" -metric "$metric" "$@" > "$out"
echo "wrote $out" >&2
