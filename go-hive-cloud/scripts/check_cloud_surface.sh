#!/usr/bin/env bash
# go-hive-cloud API/SSE/WS surface gate.
#
# Fails when:
#   - contracts/cloud-surface.json or docs/cloud-surface.html drifted from the
#     generator output;
#   - registered routes != endpoints marked implemented;
#   - an observed-contract endpoint is unclassified;
#   - an SSE event written by the code is not registered (or vice versa);
#   - WebSocket/Upgrade code appears while websockets[] is empty.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
PYTHON="${PYTHON:-python3}"

exec "$PYTHON" "$ROOT/scripts/gen_cloud_surface.py" --check
