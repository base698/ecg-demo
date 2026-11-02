#!/usr/bin/env bash
# Retire an instance through the controller (same as the dashboard button):
#   ./retire.sh ecg-tasks-v1
set -euo pipefail
curl -sf -X POST "http://localhost:9000/api/retire?id=${1:?instance id}" && echo "retired $1"
