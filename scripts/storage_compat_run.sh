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
# 4. real `hive serve` startup migration check (SC-01)
# ---------------------------------------------------------------------------
# The real binary must run the SessionManager.__init__ storage prep chain
# (migrate_v2 -> migrate_v3 -> migrate_colony_sessions -> ensure all tracker
# DBs) on startup. This drives the built binary against the same fixture
# templates section 1 uses and compares the post-start tree with the
# double-run reference (the Python-equivalent migrated tree). Only the files
# `serve` always writes at runtime are allowed on top of that reference.

log "serve startup: building the real hive binary"
SERVE_BIN="$WORK/hive-serve"
(cd "$GO_MODULE" && CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off go build -o "$SERVE_BIN" ./cmd/hive)

serve_home_run() { # $1 = label, $2 = home
  local label="$1" home="$2" port pid ready
  port="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
  HIVE_HOME="$home" "$SERVE_BIN" serve --host 127.0.0.1 --port "$port" > "$WORK/serve-$label.log" 2>&1 &
  pid=$!
  ready=0
  for _ in $(seq 1 150); do
    if curl -fsS "http://127.0.0.1:$port/api/health" > /dev/null 2>&1; then
      ready=1
      break
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      break
    fi
    sleep 0.2
  done
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 1 50); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.1
  done
  kill -9 "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  if [[ "$ready" != "1" ]]; then
    echo "serve startup check ($label): health never came up" >&2
    tail -20 "$WORK/serve-$label.log" >&2 || true
    exit 1
  fi
}

for label in v1 v2; do
  serve_home="$WORK/serve-$label"
  rm -rf "$serve_home"
  cp -R "$WORK/fx-$label-template" "$serve_home"
  python3 "$PY_SCRIPTS/storage_compat_gen_fixture.py" --fix-paths "$serve_home" >/dev/null
  python3 "$PY_SCRIPTS/storage_compat_gen_fixture.py" --manifest "$serve_home" > "$WORK/serve-$label-before.json"

  log "serve startup: $label (go binary)"
  serve_home_run "$label" "$serve_home"
  python3 "$PY_SCRIPTS/storage_compat_gen_fixture.py" --manifest "$serve_home" > "$WORK/serve-$label-after.json"

  log "serve startup: $label second start (idempotency)"
  serve_home_run "$label" "$serve_home"
  python3 "$PY_SCRIPTS/storage_compat_gen_fixture.py" --manifest "$serve_home" > "$WORK/serve-$label-second.json"
done

# Failure injection: a read-only home makes migrate_v2 fail at its marker
# write. serve must still come up (non-fatal warning only).
log "serve startup: failure injection (read-only home)"
fail_home="$WORK/serve-fail"
rm -rf "$fail_home"
cp -R "$WORK/fx-v1-template" "$fail_home"
python3 "$PY_SCRIPTS/storage_compat_gen_fixture.py" --fix-paths "$fail_home" >/dev/null
chmod 0555 "$fail_home"
serve_home_run "fail" "$fail_home"
chmod 0755 "$fail_home"

python3 - "$WORK" "$OUT" <<'PY'
import json
import re
import sys
from pathlib import Path

work, out = Path(sys.argv[1]), Path(sys.argv[2])

# Files a started `serve` writes regardless of migration state (MCP registry
# bootstrap); anything else on top of the reference is a failure.
ALLOWED_RUNTIME = {"mcp_registry/config.json", "mcp_registry/installed.json"}
EXPECTED_ENSURED = {"v1": 2, "v2": 3}  # colony(s) + the colonies/.migrations pseudo-colony

report = {}
problems = []
for label in ("v1", "v2"):
    before = json.loads((work / f"serve-{label}-before.json").read_text())["files"]
    after = json.loads((work / f"serve-{label}-after.json").read_text())["files"]
    second = json.loads((work / f"serve-{label}-second.json").read_text())["files"]
    golden = json.loads((out / f"{label}-tree-diff.json").read_text())
    expected = set(golden["equal"]) | set(golden["changed_opaque"])
    added = sorted(set(after) - set(before))
    removed = sorted(set(before) - set(after))
    extra = sorted(set(after) - expected)
    missing = sorted(expected - set(after))
    unexpected_extra = [p for p in extra if p not in ALLOWED_RUNTIME]
    markers = sorted(p for p in after if Path(p).name.startswith(".migrated-") or "colony_sessions_v1" in p)
    for marker in (".migrated-v3", "colonies/.migrations/colony_sessions_v1"):
        if marker not in after:
            problems.append(f"{label}: marker {marker} missing after serve")
    if any(p.startswith("agents/") for p in after):
        problems.append(f"{label}: pre-v3 agents/ files still present after serve")
    if any(p.endswith("/data/tracker.db") and not p.startswith("exports/") for p in after):
        problems.append(f"{label}: tracker still under data/ after serve")
    if missing:
        problems.append(f"{label}: migrated files missing vs reference: {missing}")
    if unexpected_extra:
        problems.append(f"{label}: unexpected files vs reference: {unexpected_extra}")
    if set(second) != set(after):
        problems.append(f"{label}: second serve changed the file inventory: {sorted(set(second) ^ set(after))}")
    log = (work / f"serve-{label}.log").read_text(errors="replace")
    match = re.search(r"tracker_db: ensured (\d+) colony tracker DB\(s\) at startup", log)
    ensured = int(match.group(1)) if match else None
    if ensured != EXPECTED_ENSURED[label]:
        problems.append(f"{label}: tracker ensure log = {ensured}, want {EXPECTED_ENSURED[label]}")
    report[label] = {
        "health": 200,
        "file_count_before": len(before),
        "file_count_after": len(after),
        "markers_after": markers,
        "tracker_ensure_log": match.group(0) if match else None,
        "migration_reference": f"{label}-tree-diff.json",
        "added": added,
        "removed": removed,
        "extra_vs_reference": extra,
        "missing_vs_reference": missing,
        "second_start_unchanged": set(second) == set(after),
    }

fail_log = (work / "serve-fail.log").read_text(errors="replace")
fail_marker = (work / "serve-fail" / ".migrated-v2").exists()
if "v2 migration failed (non-fatal)" not in fail_log:
    problems.append("failure injection: serve did not report the non-fatal v2 migration warning")
if fail_marker:
    problems.append("failure injection: .migrated-v2 was written despite the injected failure")
failure_injection = {
    "health": 200,
    "warning_logged": "v2 migration failed (non-fatal)" in fail_log,
    "marker_written": fail_marker,
    "note": "Read-only home: migrate_v2 fails at its marker write; serve still reaches /api/health 200.",
}

(out / "serve-startup-go.json").write_text(json.dumps({
    "description": "Real `hive serve` (go_hive binary) started on generated legacy fixtures. The post-start tree is compared with the storage-compat double-run reference (the Python-equivalent migrated tree); serve runtime files are listed in extra_vs_reference and limited to mcp_registry bootstrap files. Each fixture is served twice to confirm the second start changes nothing; a read-only home proves a failed migration is non-fatal.",
    "fixtures": report,
    "failure_injection": failure_injection,
}, indent=2, sort_keys=True))
if problems:
    raise SystemExit("serve startup check: FAIL\n" + "\n".join(problems))
print(json.dumps({label: {
    "files": f"{r['file_count_before']}->{r['file_count_after']}",
    "markers": len(r["markers_after"]),
    "tracker_ensure": r["tracker_ensure_log"],
    "second_start_unchanged": r["second_start_unchanged"],
} for label, r in report.items()}, indent=2))
print(json.dumps({"failure_injection": failure_injection}, indent=2))
PY

# ---------------------------------------------------------------------------
# 5. summary
# ---------------------------------------------------------------------------

diffy summary --dir "$OUT"

log "field contract -> contracts/storage-fields.json"
python3 "$PY_SCRIPTS/gen_storage_contract.py" --work "$WORK" --out "$ROOT/contracts/storage-fields.json" > "$WORK/contract-build.json"

log "done; artifacts in $OUT (work dir: $WORK)"
