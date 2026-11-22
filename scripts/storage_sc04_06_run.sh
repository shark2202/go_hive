#!/usr/bin/env bash
# SC-04 (permission bits) + SC-06 (session meta / tasks / colony sidecar
# write-read-rewrite cycle) + SC-08 (single-side filename verification).
#
# Evidence lands in docs/e2e-assets/storage-sc04-06/:
#   sc04-perms-{py,go}.json   raw mode maps for a home written by each side
#   sc04-perms.json           grouped mode diff (the SC-04 table source)
#   sc06-py-write.json        Python writes meta/tasks/colony sidecars
#   sc06-go-read.json         Go reads the Python-written home
#   sc06-go-write.json        Go rewrites the same artifacts
#   sc06-py-read.json         Python reads the Go-rewritten home
#   sc06-cycle-diff.json      field-level diff + API checks + hash transitions
#   sc06-home-artifacts/      the actual files after the cycle
#   sc08-file-names.json      per-name hit sites + verdicts
#   summary.json              verdict roll-up
#   commands.md               every command + stdout
#
# Usage:
#   bash go_hive/scripts/storage_sc04_06_run.sh [--work <scratch>] [--out <dir>]
#        [--suite all|sc04,sc06,sc08] [--python-bin <python>]
#
# Defaults: work=<mktemp -d>, out=<worktree>/docs/e2e-assets/storage-sc04-06
# The Python halves need the project venv (uv run python by default, or
# --python-bin / PYTHON_BIN; PYTHONPATH is pinned to this tree's core/).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
GO_MODULE="$ROOT/go_hive/go-hive"

WORK=""
OUT="$ROOT/docs/e2e-assets/storage-sc04-06"
SUITE="all"
PYTHON_BIN="${PYTHON_BIN:-}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --work) WORK="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --suite) SUITE="$2"; shift 2 ;;
    --python-bin) PYTHON_BIN="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
if [[ -z "$WORK" ]]; then
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/storage-sc04-06.XXXXXX")"
fi
mkdir -p "$WORK" "$OUT"
WORK="$(cd "$WORK" && pwd -P)"
OUT="$(cd "$OUT" && pwd -P)"
LOG="$OUT/commands.md"
: > "$LOG"

log() { printf '[storage-sc04-06] %s\n' "$*"; }

record() { # record <description> <command...>
  local desc="$1"; shift
  printf '\n### %s\n\n```bash\n%s\n```\n\n' "$desc" "$*" >> "$LOG"
  "$@" >> "$LOG" 2>&1
}

record_to() { # record_to <description> <out-file> <command...>
  local desc="$1" file="$2"; shift 2
  printf '\n### %s\n\n```bash\n%s\n```\n\n' "$desc" "$*" >> "$LOG"
  "$@" > "$file" 2>> "$LOG"
}

python_bin() {
  if [[ -n "$PYTHON_BIN" ]]; then printf '%s' "$PYTHON_BIN"; return; fi
  if [[ -x "$ROOT/.venv/bin/python" ]]; then printf '%s' "$ROOT/.venv/bin/python"; return; fi
  printf 'uv run python'
}

py() { # run the Python probe with the repo framework on PYTHONPATH
  ( cd "$ROOT/core" && PYTHONPATH="$ROOT/core" $(python_bin) "$SCRIPT_DIR/storage_compat_probes.py" "$@" )
}

have_suite() { [[ ",$SUITE," == *",$1,"* || "$SUITE" == "all" ]]; }

# ---------------------------------------------------------------------------
# build the Go harness once
# ---------------------------------------------------------------------------

log "building the Go storage-compat harness"
record "build the Go storage-compat harness" \
  bash -c "cd '$GO_MODULE' && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build -o '$WORK/storagecompat' ./e2e/cmd/storagecompat"
GO_HALF="$WORK/storagecompat"

printf '\n### environment\n\n```\nROOT=%s\nWORK=%s\nOUT=%s\nSUITE=%s\npython=%s\n```\n\n' \
  "$ROOT" "$WORK" "$OUT" "$SUITE" "$(python_bin)" >> "$LOG"

# ---------------------------------------------------------------------------
# SC-04: permission bits on sessions/colonies dirs + files
# ---------------------------------------------------------------------------

if have_suite sc04; then
  log "SC-04: writing one home per implementation"
  PY_HOME="$WORK/sc04-py"
  GO_HOME="$WORK/sc04-go"
  rm -rf "$PY_HOME" "$GO_HOME"
  mkdir -p "$PY_HOME" "$GO_HOME"

  record_to "SC-04 Python seed: storage writer probes (state/conversations/checkpoints/tracker/credentials)" \
    "$WORK/sc04-py-probes.json" py probes --home "$PY_HOME"
  record_to "SC-04 Python seed: session meta / tasks / colony sidecars (production writers)" \
    "$WORK/sc04-py-sidecars.json" py sidecar-write --home "$PY_HOME"

  record_to "SC-04 Go seed: storage writer probes (state/conversations/checkpoints/tracker/credentials)" \
    "$WORK/sc04-go-probes.json" "$GO_HALF" write-probes --home "$GO_HOME"
  record_to "SC-04 Go seed: session meta / tasks / colony sidecars (production writers)" \
    "$WORK/sc04-go-sidecars.json" "$GO_HALF" sidecar-write --mode seed --home "$GO_HOME"

  record "SC-04 permission probe (Python-written home)" \
    python3 "$SCRIPT_DIR/storage_sc04_perms.py" probe --home "$PY_HOME" --out "$OUT/sc04-perms-py.json"
  record "SC-04 permission probe (Go-written home)" \
    python3 "$SCRIPT_DIR/storage_sc04_perms.py" probe --home "$GO_HOME" --out "$OUT/sc04-perms-go.json"

  record "SC-04 permission diff (normalise the per-side probe session dir name)" \
    python3 "$SCRIPT_DIR/storage_sc04_perms.py" diff \
      --py "$OUT/sc04-perms-py.json" --go "$OUT/sc04-perms-go.json" \
      --out "$OUT/sc04-perms.json" \
      --rename "queens/queen_growth/sessions/session_compat_probe_py=>queens/queen_growth/sessions/session_compat_probe" \
      --rename "queens/queen_growth/sessions/session_compat_probe_go=>queens/queen_growth/sessions/session_compat_probe"
fi

# ---------------------------------------------------------------------------
# SC-06: one home, Python write -> Go read -> Go write -> Python read
# ---------------------------------------------------------------------------

if have_suite sc06; then
  log "SC-06: Python write -> Go read -> Go write -> Python read"
  SC06_HOME="$WORK/sc06-home"
  rm -rf "$SC06_HOME"
  mkdir -p "$SC06_HOME"

  record_to "SC-06 step 1: Python writes session meta / tasks / colony sidecars" \
    "$OUT/sc06-py-write.json" py sidecar-write --home "$SC06_HOME"
  record_to "SC-06 step 2: Go reads the same home through the production readers" \
    "$OUT/sc06-go-read.json" "$GO_HALF" sidecar-read --phase go-read --home "$SC06_HOME"
  record_to "SC-06 step 3: Go rewrites title/phase/tasks/tools/triggers through the production writers" \
    "$OUT/sc06-go-write.json" "$GO_HALF" sidecar-write --home "$SC06_HOME"
  record_to "SC-06 step 4: Python reads the Go-rewritten home" \
    "$OUT/sc06-py-read.json" py sidecar-read --phase python-read --home "$SC06_HOME"

  record "SC-06 field-level diff (go-read vs python-write, python-read vs go-write)" \
    python3 "$SCRIPT_DIR/storage_sidecar_cycle_compare.py" \
      --py-write "$OUT/sc06-py-write.json" \
      --go-read "$OUT/sc06-go-read.json" \
      --go-write "$OUT/sc06-go-write.json" \
      --py-read "$OUT/sc06-py-read.json" \
      --out "$OUT/sc06-cycle-diff.json" --label sc06

  mkdir -p "$OUT/sc06-home-artifacts/session" "$OUT/sc06-home-artifacts/colonies"
  cp "$SC06_HOME/queens/queen_growth/sessions/session_sidecar_cycle/meta.json" "$OUT/sc06-home-artifacts/session/" 2>/dev/null || true
  cp "$SC06_HOME/queens/queen_growth/sessions/session_sidecar_cycle/tasks.json" "$OUT/sc06-home-artifacts/session/" 2>/dev/null || true
  for colony in cycle_colony cycle_colony_go; do
    mkdir -p "$OUT/sc06-home-artifacts/colonies/$colony"
    for name in metadata.json worker.json tools.json triggers.json; do
      cp "$SC06_HOME/colonies/$colony/$name" "$OUT/sc06-home-artifacts/colonies/$colony/" 2>/dev/null || true
    done
  done
fi

# ---------------------------------------------------------------------------
# SC-08: single-side filename lists, name by name
# ---------------------------------------------------------------------------

if have_suite sc08; then
  log "SC-08: verifying the single-side filename lists"
  record "SC-08 per-name scan + curated verdicts" \
    python3 "$SCRIPT_DIR/storage_filenames_audit.py" \
      --root "$ROOT" --contract contracts/storage-fields.json --out "$OUT/sc08-file-names.json"
fi

# ---------------------------------------------------------------------------
# roll-up
# ---------------------------------------------------------------------------

log "writing summary"
python3 - "$OUT" "$SUITE" <<'PY' >> "$LOG" 2>&1
import json
import sys
from pathlib import Path

out, suite = Path(sys.argv[1]), sys.argv[2]
summary = {"suite": suite, "evidence_dir": str(out), "sections": {}}

perms_path = out / "sc04-perms.json"
if perms_path.exists():
    perms = json.loads(perms_path.read_text())
    summary["sections"]["sc04_permissions"] = {
        "evidence": "sc04-perms.json",
        "counts": perms["counts"],
        "groups": [{"py_mode": g["py_mode"], "go_mode": g["go_mode"], "count": g["count"]}
                   for g in perms["groups"]],
        "areas": {area: data["mismatch_count"] for area, data in sorted(perms["area_summary"].items())},
    }

cycle_path = out / "sc06-cycle-diff.json"
if cycle_path.exists():
    cycle = json.loads(cycle_path.read_text())
    summary["sections"]["sc06_sidecar_cycle"] = {
        "evidence": "sc06-cycle-diff.json",
        "verdict": cycle["verdict"],
        "failures": cycle["failures"],
        "go_read_vs_python_write_equal": cycle["view_diffs"]["go_read_vs_python_write"]["equal"],
        "python_read_vs_go_write_equal": cycle["view_diffs"]["python_read_vs_go_write"]["equal"],
        "api_checks_passed": cycle["api_checks"]["passed"],
        "api_checks_run": cycle["api_checks"]["checks_run"],
        "changed_on_go_write": cycle["file_transitions"]["changed"],
        "created_by_go_write": cycle["file_transitions"]["created_by_go_write"],
    }

files_path = out / "sc08-file-names.json"
if files_path.exists():
    audit = json.loads(files_path.read_text())
    summary["sections"]["sc08_single_side_files"] = {
        "evidence": "sc08-file-names.json",
        "verdicts": audit["summary"]["verdicts"],
        "names_also_used_by_the_other_side": audit["summary"]["names_also_used_by_the_other_side"],
        "names_also_used_by_the_other_side_list": audit["summary"]["names_also_used_by_the_other_side_list"],
        "exceptions": [item["name"] for item in audit["names"] if item["verdict"] != "兼容"],
    }

failures = []
if "sc06_sidecar_cycle" in summary["sections"] and summary["sections"]["sc06_sidecar_cycle"]["verdict"] != "PASS":
    failures.append("sc06 cycle FAIL")
if "sc08_single_side_files" in summary["sections"]:
    bad = [item["name"] for item in json.loads(files_path.read_text())["names"] if item["verdict"] == "UNREVIEWED"]
    if bad:
        failures.append(f"sc08 unreviewed names: {bad}")
summary["failures"] = failures
summary["verdict"] = "PASS" if not failures else "FAIL"
(out / "summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8")
print(json.dumps(summary, indent=2, ensure_ascii=False))
raise SystemExit(0 if summary["verdict"] == "PASS" else 1)
PY

log "done; evidence in $OUT (work dir: $WORK)"
