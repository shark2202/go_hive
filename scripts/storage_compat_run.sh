#!/usr/bin/env bash
# Storage-compat double run: generate legacy HIVE_HOME fixtures, migrate one
# copy with the Python implementation and one with the Go implementation,
# then diff trees / JSON fields / SQLite schemas and cross-read credentials.
#
# Usage:
#   bash go_hive/scripts/storage_compat_run.sh [--work <scratch>] [--out <dir>]
#
# Defaults: work=<mktemp -d>, out=<worktree>/docs/storage-compat
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PY_SCRIPTS="$ROOT/go_hive/scripts"
GO_MODULE="$ROOT/go_hive/go-hive"

WORK=""
OUT="$ROOT/docs/storage-compat"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --work) WORK="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
if [[ -z "$WORK" ]]; then
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/storage-compat.XXXXXX")"
fi
mkdir -p "$WORK" "$OUT"

log() { echo "[storage-compat] $*"; }

# A fixed Fernet key so both halves of the probes write with the same key
# (the cross-read test also exercises each side reading the other's files).
FIXED_KEY="MDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDA="

py() { (cd "$ROOT/core" && PYTHONPATH="$ROOT/core" uv run python "$PY_SCRIPTS/storage_compat_probes.py" "$@"); }
go_half() { (cd "$GO_MODULE" && GOFLAGS=-mod=mod GOPROXY=off go run ./e2e/cmd/storagecompat "$@"); }
diffy() { python3 "$PY_SCRIPTS/storage_compat_diff.py" "$@"; }

# ---------------------------------------------------------------------------
# 1. migration fixtures, one double run per legacy layout
# ---------------------------------------------------------------------------

for label in v1 v2 colony-fork; do
  template="$WORK/fx-$label-template"
  if [[ ! -d "$template" ]]; then
    python3 "$PY_SCRIPTS/storage_compat_gen_fixture.py" --dest "$template" --version "$label" >/dev/null
  fi
  py_home="$WORK/$label-py"
  go_home="$WORK/$label-go"
  rm -rf "$py_home" "$go_home"
  cp -R "$template" "$py_home"
  cp -R "$template" "$go_home"
  python3 "$PY_SCRIPTS/storage_compat_gen_fixture.py" --fix-paths "$py_home" >/dev/null
  python3 "$PY_SCRIPTS/storage_compat_gen_fixture.py" --fix-paths "$go_home" >/dev/null

  log "migrate $label (python)"
  py migrate --home "$py_home" > "$WORK/$label-py-migrate.json"
  log "migrate $label (go)"
  go_half migrate --home "$go_home" > "$WORK/$label-go-migrate.json"

  log "diff $label"
  diffy run --py "$py_home" --go "$go_home" --label "$label" --out "$OUT"
done

# ---------------------------------------------------------------------------
# 2. writer probes (state.json / conversations / checkpoints / tracker / creds)
# ---------------------------------------------------------------------------

py_probe="$WORK/probes-py"
go_probe="$WORK/probes-go"
rm -rf "$py_probe" "$go_probe"
mkdir -p "$py_probe/secrets" "$go_probe/secrets"
printf '%s' "$FIXED_KEY" > "$py_probe/secrets/credential_key"
printf '%s' "$FIXED_KEY" > "$go_probe/secrets/credential_key"

log "probes (python)"
py probes --home "$py_probe" > "$WORK/probes-py.json"
log "probes (go)"
go_half write-probes --home "$go_probe" > "$WORK/probes-go.json"

log "diff probes"
diffy run --py "$py_probe" --go "$go_probe" --label probes --out "$OUT" \
  --rename "queens/queen_growth/sessions/session_compat_probe_py=>queens/queen_growth/sessions/session_compat_probe" \
  --rename "queens/queen_growth/sessions/session_compat_probe_go=>queens/queen_growth/sessions/session_compat_probe"

# ---------------------------------------------------------------------------
# 3. credential cross-read
# ---------------------------------------------------------------------------

log "credential cross-read: go reads python-written store"
go_half read-credential --dir "$py_probe/credentials" --key "$py_probe/secrets/credential_key" --id demo_oauth \
  > "$WORK/credentials-go-reads-py.json"
go_half read-credential --dir "$py_probe/credentials" --key "$py_probe/secrets/credential_key" --id demo_api_key \
  >> "$WORK/credentials-go-reads-py.json"

log "credential cross-read: python reads go-written store"
py creds-load --home "$go_probe" > "$WORK/credentials-py-reads-go.json"

python3 - "$WORK" "$OUT" <<'PYEOF'
import json
import sys
from pathlib import Path

work, out = Path(sys.argv[1]), Path(sys.argv[2])


def read_lines(path: Path):
    raw = path.read_text()
    stripped = raw.strip()
    if not stripped:
        return []
    # The go half appends two JSON documents; split on object boundaries.
    docs, depth, start = [], 0, 0
    for i, ch in enumerate(raw):
        if ch == "{":
            if depth == 0:
                start = i
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                docs.append(json.loads(raw[start:i + 1]))
    return docs


summary = {
    "fixed_key_comment": "Both probe homes used the same Fernet key file; each side read the other side's store.",
    "go_reads_python": read_lines(work / "credentials-go-reads-py.json"),
    "python_reads_go": json.loads((work / "credentials-py-reads-go.json").read_text()),
}
(out / "credentials-crossread.json").write_text(json.dumps(summary, indent=2, sort_keys=True))
print(json.dumps({
    "go_reads_python_ids": [d.get("id") for d in summary["go_reads_python"]],
    "python_reads_go_ids": summary["python_reads_go"].get("ids"),
}, indent=2))
PYEOF

# ---------------------------------------------------------------------------
# 4. summary
# ---------------------------------------------------------------------------

diffy summary --dir "$OUT"

log "field contract -> contracts/storage-fields.json"
python3 "$PY_SCRIPTS/gen_storage_contract.py" --work "$WORK" --out "$ROOT/contracts/storage-fields.json" > "$WORK/contract-build.json"

log "done; artifacts in $OUT (work dir: $WORK)"
