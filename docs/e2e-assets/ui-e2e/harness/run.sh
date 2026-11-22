#!/usr/bin/env bash
# UI E2E gate: "a colony must appear in the sidebar AND on its colony page".
#
# Usage: run.sh <colony_disk_name> [base_url]
#   run.sh ui_e2e_012144
#   run.sh ui_e2e_012144 http://127.0.0.1:8788
#
# Exit: 0 pass, 1 assertion failed, 2 preflight/harness error.
set -uo pipefail

COLONY=${1:-}
BASE=${2:-${HIVE_BASE:-http://127.0.0.1:8788}}
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
OUT=${HIVE_E2E_OUT:-$HERE/ui-e2e-artifacts}

if [ -z "$COLONY" ]; then
  echo "usage: run.sh <colony_disk_name> [base_url]" >&2
  exit 2
fi

PY=${HIVE_E2E_PYTHON:-python3}

# Preflight: the gate is meaningless if nothing is serving the SPA. Fail with a
# plain message instead of a browser timeout buried in a stack trace.
if ! curl -sf -o /dev/null --max-time 5 "$BASE/"; then
  echo "PREFLIGHT FAIL: nothing answering at $BASE/ — start the Hive server first." >&2
  echo "  e.g. cd <repo> && ./dist/hive-embed serve --host 127.0.0.1 --port 8788" >&2
  exit 2
fi

# Preflight: the colony must exist on disk, or the run is a guaranteed false red.
if [ ! -d "$HOME/.hive-go/colonies/$COLONY" ]; then
  echo "PREFLIGHT FAIL: no colony at ~/.hive-go/colonies/$COLONY" >&2
  exit 2
fi

"$PY" "$HERE/verify_colony_ui.py" "$COLONY" --base "$BASE" --out "$OUT"
rc=$?
echo "artifacts: $OUT" >&2
exit $rc
