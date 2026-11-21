#!/usr/bin/env bash
# Three-level runtime-log parity run (lane tool-logs-runtime).
#
# Replays the same deterministic scenario through the Python production logger
# (framework/tracker/runtime_logger.py + runtime_log_store.py) and the Go port
# (internal/runtimelogs), then diffs every JSON document field by field.
#
# Usage:
#   bash go_hive/scripts/runtime_logs_dual_run.sh [--work <scratch>] [--out <dir>]
#
# Defaults: work=<mktemp -d>, out=<worktree>/docs/runtime-logs-compat
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PY_SCRIPTS="$ROOT/go_hive/scripts"
GO_MODULE="$ROOT/go_hive/go-hive"

WORK=""
OUT="$ROOT/docs/runtime-logs-compat"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --work) WORK="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
if [[ -z "$WORK" ]]; then
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/runtime-logs.XXXXXX")"
fi
mkdir -p "$WORK" "$OUT"

log() { echo "[runtime-logs] $*"; }

py() { (cd "$ROOT/core" && PYTHONPATH="$ROOT/core" uv run python "$PY_SCRIPTS/runtime_logs_python_probe.py" "$@"); }
go_half() { (cd "$GO_MODULE" && GOFLAGS=-mod=mod GOPROXY=off CGO_ENABLED=0 go run ./e2e/cmd/runtimelogs "$@"); }

log "python half -> $WORK/py"
py --out "$WORK/py" > "$WORK/py-manifest.json"
log "go half -> $WORK/go"
go_half --out "$WORK/go" > "$WORK/go-manifest.json"

log "diff"
python3 "$PY_SCRIPTS/runtime_logs_diff.py" --py "$WORK/py" --go "$WORK/go" --out "$OUT"

log "cross-read: Go loader over the Python tree"
go_half --read "$WORK/py" > "$WORK/go-reads-py.json"
log "cross-read: Python loader over the Go tree"
py --read "$WORK/go" > "$WORK/py-reads-go.json"
python3 - "$WORK/go-reads-py.json" "$WORK/py-reads-go.json" "$OUT" <<'PY'
import json
import sys

go_read = json.load(open(sys.argv[1], encoding="utf-8"))
py_read = json.load(open(sys.argv[2], encoding="utf-8"))
report = {"ok": go_read == py_read, "go_reads_python": go_read, "python_reads_go": py_read}
with open(sys.argv[3] + "/cross-read.json", "w", encoding="utf-8") as fh:
    json.dump(report, fh, indent=2, ensure_ascii=False)
    fh.write("\n")
if go_read != py_read:
    print("cross-read: FAIL — the two loaders disagree", file=sys.stderr)
    raise SystemExit(1)
print("cross-read: OK — both loaders report the same runs/steps/verdicts")
PY

log "artifacts: $OUT"
