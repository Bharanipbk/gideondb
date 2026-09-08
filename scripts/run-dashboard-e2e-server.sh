#!/bin/sh
set -eu

data_path=$(mktemp -d "${TMPDIR:-/tmp}/gideondb-dashboard-e2e.XXXXXX")
cleanup() {
  if [ -n "${server_pid:-}" ]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf "$data_path"
}
trap cleanup EXIT INT TERM

cd ../../../..
GIDEONDB_DASHBOARD_USERNAME=admin GIDEONDB_DASHBOARD_PASSWORD=browser-test-password GOCACHE="${TMPDIR:-/tmp}/gideondb-go-cache" go run ./cmd/gideondb \
  -http-address 127.0.0.1:16334 \
  -grpc-address '' \
  -data-path "$data_path" &
server_pid=$!
wait "$server_pid"
