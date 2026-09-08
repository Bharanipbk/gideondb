#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$script_dir"

export GIDEONDB_DASHBOARD_USERNAME="${GIDEONDB_DASHBOARD_USERNAME:-admin}"
export GIDEONDB_DASHBOARD_PASSWORD="${GIDEONDB_DASHBOARD_PASSWORD:-admin123}"
export GIDEONDB_DASHBOARD_BOOTSTRAP="${GIDEONDB_DASHBOARD_BOOTSTRAP:-true}"

exec go run ./cmd/gideondb "$@"
