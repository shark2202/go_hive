#!/usr/bin/env bash
# emoh — exports-migration baseline probe (Python vs Go).
#
# Question (blocked G2 / P0-G2-1): which `exports/` directory does the v1→v2
# startup migration consume, and where do the migrated colonies land?
# Upstream Python resolves `Path("exports")` relative to the process CWD
# (core/framework/storage/migrate_v2.py:37); the Go port anchors the lookup
# to $HIVE_HOME/exports (internal/server/storage_startup.go:63).
#
# This probe seeds *the same shaped* directory trees on both sides and runs
# each real CLI through its startup storage sequence under four launch modes:
#
#   m1 cwd=repo     CWD is a checkout-shaped dir that holds exports/
#   m2 cwd=$HOME    CWD is a sandboxed stand-in for the user's home (holds exports/)
#   m3 cwd=tmp      CWD is an unrelated scratch dir (holds exports/)
#   m4 cwd=neutral  CWD holds no exports/; the CLI is launched by absolute path
#
# Every scenario seeds BOTH candidates with a marker colony:
#   $CWD/exports/probe_from_cwd   and   $HIVE_HOME/exports/probe_from_home
# The startup migration runs before the HTTP listener binds, so `serve
# --port 1` performs the full v2→v3→colony_sessions→tracker sequence and then
# exits with a bind failure — no server is left running. Whichever marker
# appears under $HIVE_HOME/colonies identifies the resolved baseline.
#
# It also probes the implementation APIs with an explicit absolute exports
# dir (and Go's empty-string default) and diffs `hive colony list` text/JSON
# on a home containing colonies/.migrations.
#
# Usage:
#   EMOH_GO_BIN=<go-hive> EMOH_PY_HIVE=<abs path to python hive entry> \
#     bash go_hive/scripts/emoh_exports_baseline.sh
#
# Env:
#   EMOH_GO_BIN       Go hive binary (built from go_hive/go-hive). Required.
#   EMOH_PY_HIVE      Absolute path to the Python hive entry (console script).
#                     Required for the Python half (skip with EMOH_SKIP_PY=1).
#   EMOH_PY_CORE      core/ dir prepended to PYTHONPATH to pin the probed tree.
#   EMOH_PY_PYTHON    Python interpreter for the API probe
#                     (default: <dir of EMOH_PY_HIVE>/python).
#   EMOH_REPO_WRAPPER optional repo `hive` bash wrapper; adds the "absolute
#                     launch of the v1 wrapper" row.
#   EMOH_GO_MODULE    go module dir (go_hive/go-hive); enables the Go API rows.
#   EMOH_WORK         scratch dir (default: mktemp -d).
#   EMOH_OUT          evidence dir
#                     (default: <repo>/go_hive/docs/e2e-assets/exports-baseline).
#   EMOH_ALARM        seconds before a run is killed (default 90).
#   EMOH_SKIP_PY      set to 1 to run the Go half only.
#
# Evidence: matrix.tsv / matrix.json / matrix.md, per-run serve logs, and the
# colony-list diff artifacts, all under $EMOH_OUT. The probe never writes to
# the repo outside $EMOH_OUT.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

GO_BIN="${EMOH_GO_BIN:-}"
PY_HIVE="${EMOH_PY_HIVE:-}"
PY_CORE="${EMOH_PY_CORE:-}"
PY_PYTHON="${EMOH_PY_PYTHON:-}"
REPO_WRAPPER="${EMOH_REPO_WRAPPER:-}"
GO_MODULE="${EMOH_GO_MODULE:-}"
OUT="${EMOH_OUT:-$ROOT/go_hive/docs/e2e-assets/exports-baseline}"
WORK="${EMOH_WORK:-}"
ALARM="${EMOH_ALARM:-90}"
SKIP_PY="${EMOH_SKIP_PY:-0}"

if [[ -z "$GO_BIN" ]]; then
  echo "emoh: EMOH_GO_BIN is required" >&2
  exit 2
fi
if [[ "$SKIP_PY" != "1" && -z "$PY_HIVE" ]]; then
  echo "emoh: EMOH_PY_HIVE is required (or set EMOH_SKIP_PY=1)" >&2
  exit 2
fi
if [[ "$SKIP_PY" != "1" && -z "$PY_PYTHON" ]]; then
  PY_PYTHON="$(dirname "$PY_HIVE")/python"
fi

if [[ -z "$WORK" ]]; then
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/emoh.XXXXXX")"
fi
mkdir -p "$WORK" "$OUT"
WORK="$(cd "$WORK" && pwd -P)"   # physical path (macOS /tmp -> /private/tmp)
OUT="$(cd "$OUT" && pwd -P)"

TSV="$OUT/matrix.tsv"
LOG="$OUT/commands.md"
: > "$TSV"
: > "$OUT/notes.tsv"

log() { printf '[emoh] %s\n' "$*" >&2; }

{
  printf '# emoh evidence commands\n\n'
  printf '```bash\nEMOH_GO_BIN=%s \\\nEMOH_PY_HIVE=%s \\\nEMOH_PY_CORE=%s \\\nEMOH_REPO_WRAPPER=%s \\\nEMOH_GO_MODULE=%s \\\nEMOH_WORK=%s \\\nEMOH_OUT=%s \\\nbash go_hive/scripts/emoh_exports_baseline.sh\n```\n\n' \
    "$GO_BIN" "$PY_HIVE" "$PY_CORE" "$REPO_WRAPPER" "$GO_MODULE" "$WORK" "$OUT"
} >> "$LOG"

# perl alarm shim (macOS has no `timeout`): SIGALRM kills the exec'd process.
alarm() { perl -e 'alarm shift; exec @ARGV' "$1" "${@:2}"; }

seed_exports() { # seed_exports <exports-dir> <colony-name>
  local dir="$1" name="$2"
  mkdir -p "$dir/$name"
  printf '{"queen_name":"probe","created_at":"2026-01-01T00:00:00"}\n' > "$dir/$name/metadata.json"
  printf '{"name":"probe_worker","description":"emoh marker"}\n' > "$dir/$name/probe_worker.json"
}

colonies_of() { # colonies_of <home> -> sorted non-.migrations colony dirs, one per line
  if [[ -d "$1/colonies" ]]; then
    (cd "$1/colonies" && find . -mindepth 1 -maxdepth 1 -type d -print \
      | sed 's|^\./||' | grep -v '^\.migrations$' | LC_ALL=C sort)
  fi
}

record_row() { # record_row side mode cwd cwd_exports home_exports launch exit marker migrated dot_migrations
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$1" "$2" "$3" "$4" "$5" "$6" "$7" "$8" "$9" "${10}" >> "$TSV"
}

# ---------------------------------------------------------------------------
# Scenario runner: one side × one mode
# ---------------------------------------------------------------------------
run_scenario() { # run_scenario <side> <mode> <cwd-subdir> <cwd-has-exports:0|1> <launch:abs|wrapper>
  local side="$1" mode="$2" cwd_name="$3" cwd_exports="$4" launch="$5"
  local sc="$WORK/$side-$mode"
  local home="$sc/home"
  rm -rf "$sc"
  mkdir -p "$home"
  local runner="$GO_BIN"
  [[ "$side" == "py" ]] && runner="$PY_HIVE"
  [[ "$launch" == "wrapper" ]] && runner="$REPO_WRAPPER"

  local cwd="$sc/$cwd_name"
  mkdir -p "$cwd"
  seed_exports "$home/exports" "probe_from_home"
  if [[ "$cwd_exports" == "1" ]]; then
    seed_exports "$cwd/exports" "probe_from_cwd"
  fi

  printf '\n### %s/%s — cwd=%s, cwd has exports/: %s, launch=%s\n\n```bash\nHIVE_HOME=%s perl -e '"'"'alarm shift; exec @ARGV'"'"' %s %s serve --port 1\n```\n' \
    "$side" "$mode" "$cwd" "$cwd_exports" "$launch" "$home" "$ALARM" "$runner" >> "$LOG"

  # Startup migrations happen before the listener binds; --port 1 then fails
  # with a permission error and the process exits — no server left running.
  (
    cd "$cwd"
    export HIVE_HOME="$home"
    if [[ "$side" == "py" ]]; then
      export HIVE_NO_FILE_LOG=1
      [[ -n "$PY_CORE" ]] && export PYTHONPATH="$PY_CORE"
    fi
    alarm "$ALARM" "$runner" serve --port 1
  ) >>"$sc/serve.log" 2>&1
  local code=$?

  local marker="no"; [[ -f "$home/.migrated-v2" ]] && marker="yes"
  local migrated; migrated="$(colonies_of "$home" | paste -sd';' -)"
  [[ -z "$migrated" ]] && migrated="-"
  local dotmig="no"; [[ -d "$home/colonies/.migrations" ]] && dotmig="yes"
  local note=""
  if [[ "$launch" == "wrapper" ]]; then
    note="$(grep -m1 'must be run from the project directory' "$sc/serve.log" 2>/dev/null | head -c 100)"
  fi
  [[ -z "$note" ]] && note="$(grep -m1 -E 'PermissionError|bind: permission denied' "$sc/serve.log" 2>/dev/null | head -c 100)"
  mkdir -p "$OUT/runs/$side-$mode"
  cp "$sc/serve.log" "$OUT/runs/$side-$mode/serve.log"
  record_row "$side" "$mode" "$cwd_name" "$cwd_exports" \
    "$( [[ -d "$home/exports" ]] && echo 1 || echo 0 )" "$launch" "$code" "$marker" "$migrated" "$dotmig"
  printf '%s\t%s\t%s\n' "$side" "$mode" "$note" >> "$OUT/notes.tsv"
}

# ---------------------------------------------------------------------------
# Part 1 — launch-mode matrix
# ---------------------------------------------------------------------------
if [[ "$SKIP_PY" != "1" ]]; then
  run_scenario py m1_cwd_repo    repo     1 abs
  run_scenario py m2_cwd_home    homecwd  1 abs
  run_scenario py m3_cwd_tmp     tmpcwd   1 abs
  run_scenario py m4_cwd_neutral neutral  0 abs
  if [[ -n "$REPO_WRAPPER" && -x "$REPO_WRAPPER" ]]; then
    run_scenario py m4w_repo_wrapper neutral 0 wrapper
  fi
fi
run_scenario go m1_cwd_repo    repo     1 abs
run_scenario go m2_cwd_home    homecwd  1 abs
run_scenario go m3_cwd_tmp     tmpcwd   1 abs
run_scenario go m4_cwd_neutral neutral  0 abs

# ---------------------------------------------------------------------------
# Part 2 — implementation API probes (explicit absolute exports dir)
# ---------------------------------------------------------------------------
api_row() { # api_row <side> <probe> <home> <exit> <migrated>
  local marker="no"; [[ -f "$3/.migrated-v2" ]] && marker="yes"
  record_row "$1" "$2" "(api)" "explicit-abs" "n/a" "direct-call" "$4" "$marker" "$5" "-"
}

if [[ "$SKIP_PY" != "1" ]]; then
  sc="$WORK/py-api-abs"; rm -rf "$sc"; mkdir -p "$sc/home" "$sc/abs_exports"
  seed_exports "$sc/abs_exports" "probe_from_abs"
  printf '\n### py api_explicit_abs\n\n```bash\nHIVE_HOME=%s/python -c "run_migration(exports_dir=Path(%s))"\n```\n' "$sc/home" "$sc/abs_exports" >> "$LOG"
  (
    cd "$sc"
    export HIVE_HOME="$sc/home"
    [[ -n "$PY_CORE" ]] && export PYTHONPATH="$PY_CORE"
    "$PY_PYTHON" -c 'import sys; from pathlib import Path; from framework.storage.migrate_v2 import run_migration; run_migration(exports_dir=Path(sys.argv[1]))' "$sc/abs_exports"
  ) >"$sc/run.log" 2>&1
  code=$?
  mkdir -p "$OUT/runs/py-api_explicit_abs"; cp "$sc/run.log" "$OUT/runs/py-api_explicit_abs/run.log"
  api_row py api_explicit_abs "$sc/home" "$code" "$(colonies_of "$sc/home" | paste -sd';' -)"
fi

if [[ -n "$GO_MODULE" ]]; then
  probe_dir="$GO_MODULE/.emoh-probe"
  rm -rf "$probe_dir"; mkdir -p "$probe_dir"
  cat > "$probe_dir/main.go" <<'GO'
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/aden-hive/hive-go/internal/agents"
	"github.com/aden-hive/hive-go/internal/storage"
)

// emoh API probe:
//
//	abs <home> <exports>      RunMigrationV2 with an explicit exports dir
//	default <home> <exports>  RunMigrationV2 with the "" (CWD-relative) default
//	discover <coloniesDir>    agents.DiscoverAgents (GET /api/discover core)
func main() {
	mode := os.Args[1]
	if mode == "discover" {
		groups := agents.DiscoverAgents(os.Args[2])
		out := map[string][]string{}
		for key, entries := range groups {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name)
			}
			sort.Strings(names)
			out[key] = names
		}
		encoded, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(string(encoded))
		return
	}
	home, exports := os.Args[2], os.Args[3]
	dir := ""
	if mode == "abs" {
		dir = exports
	}
	if err := storage.RunMigrationV2(home, dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
GO
  probe_bin="$WORK/emoh-go-probe"
  (
    cd "$GO_MODULE"
    export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod
    go build -o "$probe_bin" ./.emoh-probe
  ) > "$WORK/go-probe-build.log" 2>&1
  build_code=$?
  cp "$WORK/go-probe-build.log" "$OUT/runs/go-probe-build.log"

  sc="$WORK/go-api-abs"; rm -rf "$sc"; mkdir -p "$sc/home" "$sc/abs_exports"
  seed_exports "$sc/abs_exports" "probe_from_abs"
  printf '\n### go api_explicit_abs\n\n```bash\nHIVE_HOME=%s %s abs %s %s\n```\n' "$sc/home" "$probe_bin" "$sc/home" "$sc/abs_exports" >> "$LOG"
  if [[ "$build_code" == "0" ]]; then
    ( cd "$sc" && export HIVE_HOME="$sc/home" && alarm 300 "$probe_bin" abs "$sc/home" "$sc/abs_exports" ) >"$sc/run.log" 2>&1
    code=$?
  else
    echo "emoh-go-probe build failed (see $WORK/go-probe-build.log)" > "$sc/run.log"
    code=99
  fi
  mkdir -p "$OUT/runs/go-api_explicit_abs"; cp "$sc/run.log" "$OUT/runs/go-api_explicit_abs/run.log"
  api_row go api_explicit_abs "$sc/home" "$code" "$(colonies_of "$sc/home" | paste -sd';' -)"

  sc="$WORK/go-api-default"; rm -rf "$sc"; mkdir -p "$sc/home" "$sc/cwd"
  seed_exports "$sc/cwd/exports" "probe_from_cwd"
  printf '\n### go api_default_empty_cwd_relative\n\n```bash\ncd %s && HIVE_HOME=%s %s default %s ""\n```\n' "$sc/cwd" "$sc/home" "$probe_bin" "$sc/home" >> "$LOG"
  if [[ "$build_code" == "0" ]]; then
    ( cd "$sc/cwd" && export HIVE_HOME="$sc/home" && alarm 300 "$probe_bin" default "$sc/home" "" ) >"$sc/run.log" 2>&1
    code=$?
  else
    echo "emoh-go-probe build failed (see $WORK/go-probe-build.log)" > "$sc/run.log"
    code=99
  fi
  mkdir -p "$OUT/runs/go-api_default_empty_cwd_relative"; cp "$sc/run.log" "$OUT/runs/go-api_default_empty_cwd_relative/run.log"
  api_row go api_default_empty_cwd_relative "$sc/home" "$code" "$(colonies_of "$sc/home" | paste -sd';' -)"
  rm -rf "$probe_dir"
fi

# ---------------------------------------------------------------------------
# Part 3 — `hive colony list` on a home containing colonies/.migrations
# ---------------------------------------------------------------------------
LIST="$OUT/colony-list"
mkdir -p "$LIST"
lh="$WORK/list-home"
rm -rf "$lh"
mkdir -p "$lh/colonies/.migrations" "$lh/colonies/alpha" "$lh/colonies/beta" "$lh/colonies/.stray_dotdir"
printf '1\n' > "$lh/colonies/.migrations/colony_sessions_v1"
printf '{"queen_name":"queenie","queen_session_id":"sess-1","created_at":"2026-01-02T03:04:05.123456"}\n' > "$lh/colonies/alpha/metadata.json"
printf '{"name":"w1"}\n' > "$lh/colonies/alpha/w1.json"
printf '{"name":"w2"}\n' > "$lh/colonies/alpha/w2.json"
printf '{"queen_name":"beta-q","created_at":"2026-02-03T00:00:00"}\n' > "$lh/colonies/beta/metadata.json"

printf '\n### colony list fixture home: %s\n' "$lh" >> "$LOG"
{
  printf '\n```bash\n'
  printf 'HIVE_HOME=%s %s colony list\n' "$lh" "$PY_HIVE"
  printf 'HIVE_HOME=%s %s colony list --json\n' "$lh" "$PY_HIVE"
  printf 'HIVE_HOME=%s %s colony list\n' "$lh" "$GO_BIN"
  printf 'HIVE_HOME=%s %s colony list --json\n' "$lh" "$GO_BIN"
  printf '```\n'
} >> "$LOG"
if [[ "$SKIP_PY" != "1" ]]; then
  (
    cd "$WORK"; export HIVE_HOME="$lh"
    if [[ -n "$PY_CORE" ]]; then export PYTHONPATH="$PY_CORE"; fi
    "$PY_HIVE" colony list
  ) > "$LIST/py.txt" 2> "$LIST/py.txt.err"; echo $? > "$LIST/py.exit"
  (
    cd "$WORK"; export HIVE_HOME="$lh"
    if [[ -n "$PY_CORE" ]]; then export PYTHONPATH="$PY_CORE"; fi
    "$PY_HIVE" colony list --json
  ) > "$LIST/py.json" 2> "$LIST/py.json.err"; echo $? > "$LIST/py.exit.json"
  eh="$WORK/list-empty"; rm -rf "$eh"; mkdir -p "$eh"
  (
    cd "$WORK"; export HIVE_HOME="$eh"
    if [[ -n "$PY_CORE" ]]; then export PYTHONPATH="$PY_CORE"; fi
    "$PY_HIVE" colony list
  ) > "$LIST/py-empty.txt" 2>&1
  (
    cd "$WORK"; export HIVE_HOME="$eh"
    if [[ -n "$PY_CORE" ]]; then export PYTHONPATH="$PY_CORE"; fi
    "$PY_HIVE" colony list --json
  ) > "$LIST/py-empty.json" 2>&1
fi
( cd "$WORK" && export HIVE_HOME="$lh" && "$GO_BIN" colony list )        > "$LIST/go.txt"  2> "$LIST/go.txt.err"; echo $? > "$LIST/go.exit"
( cd "$WORK" && export HIVE_HOME="$lh" && "$GO_BIN" colony list --json )  > "$LIST/go.json" 2> "$LIST/go.json.err"; echo $? > "$LIST/go.exit.json"
eh="$WORK/list-empty-go"; rm -rf "$eh"; mkdir -p "$eh"
( cd "$WORK" && export HIVE_HOME="$eh" && "$GO_BIN" colony list )        > "$LIST/go-empty.txt" 2>&1
( cd "$WORK" && export HIVE_HOME="$eh" && "$GO_BIN" colony list --json )  > "$LIST/go-empty.json" 2>&1

# API-layer surfaces on the same fixture (executed where callable).
if [[ "$SKIP_PY" != "1" ]]; then
  (
    cd "$WORK"; export HIVE_HOME="$lh"
    if [[ -n "$PY_CORE" ]]; then export PYTHONPATH="$PY_CORE"; fi
    "$PY_PYTHON" -c 'import json; from framework.host.colony_metadata import list_colony_ids; from framework.agents.discovery import discover_agents; groups = discover_agents(); print(json.dumps({"tools_index_ids": list_colony_ids(), "discover": {k: sorted(e.name for e in v) for k, v in groups.items()}}, indent=2))'
  ) > "$LIST/api-surfaces-py.json" 2> "$LIST/api-surfaces-py.json.err"
fi
if [[ -n "${probe_bin:-}" && -x "${probe_bin:-/nonexistent}" ]]; then
  "$probe_bin" discover "$lh/colonies" > "$LIST/api-discover-go.json" 2> "$LIST/api-discover-go.json.err"
fi

# ---------------------------------------------------------------------------
# Part 4 — derive matrix.md / matrix.json / colony-list compare
# ---------------------------------------------------------------------------
"${EMOH_PYTHON:-python3}" - "$OUT" <<'PY'
import hashlib, json, sys
from pathlib import Path

out = Path(sys.argv[1])
rows = []
for line in (out / "matrix.tsv").read_text().splitlines():
    if not line.strip():
        continue
    f = line.split("\t")
    rows.append(dict(zip(
        ["side","mode","cwd","cwd_exports","home_exports","launch","exit","marker","migrated","dot_migrations"],
        f + [""] * (10 - len(f)))))

notes = {}
nf = out / "notes.tsv"
if nf.exists():
    for line in nf.read_text().splitlines():
        if line.strip():
            s, m, note = (line.split("\t", 2) + [""])[:3]
            notes[(s, m)] = note

matrix = {"generated_by": "go_hive/scripts/emoh_exports_baseline.sh", "rows": []}
for r in rows:
    r["note"] = notes.get((r["side"], r["mode"]), "")
    matrix["rows"].append(r)
(out / "matrix.json").write_text(json.dumps(matrix, indent=2, ensure_ascii=False) + "\n")

def md_escape(s):
    return s.replace("|", "\\|")

lines = [
    "# emoh matrix — exports baseline per launch mode (measured)",
    "",
    "Marker colonies: `probe_from_cwd` seeded at `<CWD>/exports/`, `probe_from_home` at `$HIVE_HOME/exports/`.",
    "`migrated` = colony dirs (excluding `.migrations`) that appeared under `$HIVE_HOME/colonies` after the run.",
    "",
    "| side | mode | CWD | CWD has exports/ | $HIVE_HOME has exports/ | launch | exit | .migrated-v2 | migrated | .migrations dir |",
    "|---|---|---|---|---|---|---|---|---|---|",
]
for r in rows:
    lines.append("| {} | {} | {} | {} | {} | {} | {} | {} | {} | {} |".format(
        r["side"], r["mode"], r["cwd"], r["cwd_exports"], r["home_exports"],
        r["launch"], r["exit"], r["marker"], md_escape(r["migrated"]), r["dot_migrations"]))
(out / "matrix.md").write_text("\n".join(lines) + "\n")

lst = out / "colony-list"
summary = {}

def sha(p):
    return hashlib.sha256(Path(p).read_bytes()).hexdigest()

if (lst / "py.txt").exists() and (lst / "go.txt").exists():
    summary["text"] = {
        "py_sha256": sha(lst / "py.txt"), "go_sha256": sha(lst / "go.txt"),
        "equal": (lst / "py.txt").read_bytes() == (lst / "go.txt").read_bytes(),
        "py_exit": (lst / "py.exit").read_text().strip(),
        "go_exit": (lst / "go.exit").read_text().strip(),
    }
    pyj = json.loads((lst / "py.json").read_text())
    goj = json.loads((lst / "go.json").read_text())
    canon = lambda v: json.dumps(v, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    import difflib
    diff_lines = difflib.unified_diff(
        (lst / "py.json").read_text().splitlines(),
        (lst / "go.json").read_text().splitlines(),
        fromfile="python colony list --json", tofile="go colony list --json", lineterm="")
    (lst / "json.diff").write_text("\n".join(diff_lines) + "\n")

    def row_of(entries, name=".migrations"):
        for entry in entries:
            if entry.get("name") == name:
                return entry
        return None

    summary["json"] = {
        "py_sha256": sha(lst / "py.json"), "go_sha256": sha(lst / "go.json"),
        "literal_equal": (lst / "py.json").read_bytes() == (lst / "go.json").read_bytes(),
        "semantic_equal": canon(pyj) == canon(goj),
        "py_key_order_rows": [list(d.keys()) for d in pyj],
        "go_key_order_rows": [list(d.keys()) for d in goj],
        "names_py": [d.get("name") for d in pyj],
        "names_go": [d.get("name") for d in goj],
        "migrations_row_py": row_of(pyj),
        "migrations_row_go": row_of(goj),
        "empty_variants_equal": (
            (lst / "py-empty.txt").read_bytes() == (lst / "go-empty.txt").read_bytes()
            and (lst / "py-empty.json").read_bytes() == (lst / "go-empty.json").read_bytes()
        ),
        "empty_py": (lst / "py-empty.txt").read_text().strip() + " / " + (lst / "py-empty.json").read_text().strip(),
        "empty_go": (lst / "go-empty.txt").read_text().strip() + " / " + (lst / "go-empty.json").read_text().strip(),
    }

    api = {}
    py_api = lst / "api-surfaces-py.json"
    go_discover = lst / "api-discover-go.json"
    if py_api.exists():
        py_d = json.loads(py_api.read_text())
        api["python_tools_index_ids"] = py_d.get("tools_index_ids")
        api["python_discover"] = py_d.get("discover")
    if go_discover.exists():
        go_d = json.loads(go_discover.read_text())
        api["go_discover"] = go_d
        if py_api.exists():
            api["discover_equal"] = py_d.get("discover") == go_d
    if api:
        summary["api_surfaces"] = api
else:
    summary = {"skipped": "python half not run"}

(lst / "compare.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False) + "\n")
print("emoh: wrote matrix.md / matrix.json / colony-list/compare.json under", out)
PY

log "done. evidence: $OUT"
