#!/bin/sh
set -eu

export GIDEONDB_DASHBOARD_USERNAME=admin
export GIDEONDB_DASHBOARD_PASSWORD=admin123
export GIDEONDB_DASHBOARD_BOOTSTRAP=true

exec go run ./cmd/gideondb "$@"
