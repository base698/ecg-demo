#!/usr/bin/env bash
# Deploy a new version of a service as a candidate instance.
#
#   ./deploy.sh tasks 2                      # tasks v2, identical behaviour
#   ./deploy.sh tasks 3 FAIL_RATE=0.3        # tasks v3 with a 30% error regression
#   ./deploy.sh api 2 LATENCY_MS=120         # api v2, slower
#   ./deploy.sh tasks 4 CONTRACT=v2          # different contract → different node key
#
# The instance registers with the controller on startup, becomes a candidate,
# and inherits obligations derived from the current baseline's evidence.
set -euo pipefail
svc=${1:?service (api|tasks|store)}; ver=${2:?version}; shift 2
case "$svc" in api|tasks|store) ;; *) echo "unknown service: $svc" >&2; exit 1 ;; esac
name="ecg-${svc}-v${ver}"
args=()
for kv in "$@"; do args+=(-e "$kv"); done
# Reusing a live ID can destroy the baseline before the replacement is checked.
if docker container inspect "$name" >/dev/null 2>&1; then
  echo "$name already exists; choose a new version or retire it first" >&2
  exit 1
fi
docker compose run --no-deps -d --rm --name "$name" -e VERSION="$ver" -e INSTANCE_ID="$name" ${args[@]+"${args[@]}"} "svc-$svc" >/dev/null
echo "deployed $name — watch it on http://localhost:9000"
