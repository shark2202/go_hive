#!/usr/bin/env bash
# Optional storage-contract sweep: fixtures + expected trees + permission bits
# + the SC-06 sidecar write-read-rewrite cycle + the single-side filename
# audit. This is the scripted entry behind `make storage-sweep`; it is
# deliberately NOT part of `make ci` (it needs the Python project env, real
# `hive serve` starts and several minutes).
#
# Usage:
#   bash go_hive/scripts/storage_sweep.sh [--work <scratch>] [--suite all|compat|sc04-06]
#        [--out <e2e dir>] [--python-bin <python>]
#
#   compat   storage_compat_run.sh (legacy-home fixtures, migration double run,
#            writer probes, credentials cross-read, real serve startup)
#   sc04-06  storage_sc04_06_run.sh (SC-04 permissions, SC-06 sidecar cycle,
#            SC-08 filename audit)
#
# Evidence: compat -> docs/storage-compat/, sc04-06 ->
# docs/e2e-assets/storage-sc04-06/.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

WORK=""
SUITE="all"
OUT=""
PYTHON_ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --work) WORK="$2"; shift 2 ;;
    --suite) SUITE="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --python-bin) PYTHON_ARGS=(--python-bin "$2"); shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
if [[ -z "$WORK" ]]; then
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/storage-sweep.XXXXXX")"
fi
mkdir -p "$WORK"
WORK="$(cd "$WORK" && pwd -P)"

log() { printf '[storage-sweep] %s\n' "$*"; }

run_compat() {
  log "compat: storage_compat_run.sh (fixtures -> double run -> serve startup)"
  bash "$SCRIPT_DIR/storage_compat_run.sh" --work "$WORK/compat"
}

run_sc04_06() {
  log "sc04-06: permissions + sidecar cycle + filename audit"
  local args=(--work "$WORK/sc04-06")
  if [[ -n "$OUT" ]]; then args+=(--out "$OUT"); fi
  if [[ ${#PYTHON_ARGS[@]} -gt 0 ]]; then args+=("${PYTHON_ARGS[@]}"); fi
  bash "$SCRIPT_DIR/storage_sc04_06_run.sh" "${args[@]}"
}

case "$SUITE" in
  all)
    run_compat
    run_sc04_06
    ;;
  compat) run_compat ;;
  sc04-06) run_sc04_06 ;;
  *) echo "unknown suite: $SUITE (want all|compat|sc04-06)" >&2; exit 2 ;;
esac

log "done; work dir: $WORK"
