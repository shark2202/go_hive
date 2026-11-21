#!/usr/bin/env bash
# G2 lossless-migration double run on *non-empty* legacy HIVE_HOMEs.
#
# For each rich fixture (v1-rich = pre-v2 layout, v2-rich = pre-v3 layout):
#   1. build the fixture once (sessions / colonies / tracker / memories /
#      senders / skills + real Fernet credentials with a fixed key)
#   2. copy it twice, migrate one copy with the Python implementation and one
#      with the Go implementation
#   3. diff trees / JSON fields / SQLite schema+rows
#   4. read-probe both copies: session lists, histories, colony discovery,
#      tracker query output, sidecar summaries, senders, skills, memories,
#      credentials — and diff what the two implementations see
#   5. cross-read credentials in both directions
#   6. start the real Python and Go `serve` binaries on fresh fixture copies
#      (startup migration runs), query the same API endpoints, diff payloads
#   7. write every command + stdout to commands.md and a machine summary
#
# Usage:
#   bash go_hive/scripts/g2_home_e2e_run.sh [--work <scratch>] [--out <dir>]
#
# Defaults: work=<mktemp -d>, out=<worktree>/docs/e2e-assets/g2-home-e2e
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PY_SCRIPTS="$ROOT/go_hive/scripts"
GO_MODULE="$ROOT/go_hive/go-hive"

WORK=""
OUT="$ROOT/docs/e2e-assets/g2-home-e2e"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --work) WORK="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
if [[ -z "$WORK" ]]; then
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/g2-home-e2e.XXXXXX")"
fi
mkdir -p "$WORK" "$OUT"
# Resolve symlinks (/tmp -> /private/tmp on macOS) so both homes are named the
# same way the Python half names its home; otherwise path literals inside
# generated files would differ for a reason unrelated to migration.
WORK="$(cd "$WORK" && pwd -P)"
OUT="$(cd "$OUT" && pwd -P)"
LOG="$OUT/commands.md"
: > "$LOG"

FIXED_KEY="MDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDA="

log() { printf '[g2-home-e2e] %s\n' "$*"; }

record() { # record <description> <command...>
  local desc="$1"; shift
  printf '\n### %s\n\n```bash\n%s\n```\n\n' "$desc" "$*" >> "$LOG"
  "$@" >> "$LOG" 2>&1
}

record_to() { # record_to <description> <output-file> <command...>
  local desc="$1" file="$2"; shift 2
  printf '\n### %s\n\n```bash\n%s\n```\n\n' "$desc" "$*" >> "$LOG"
  "$@" > "$file" 2>> "$LOG"
}

py() { (cd "$ROOT/core" && PYTHONPATH="$ROOT/core" uv run python "$PY_SCRIPTS/storage_compat_probes.py" "$@"); }
go_half() { (cd "$GO_MODULE" && GOFLAGS=-mod=mod GOPROXY=off go run ./e2e/cmd/storagecompat "$@"); }
diffy() { python3 "$PY_SCRIPTS/storage_compat_diff.py" "$@"; }

free_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

wait_health() { # wait_health <port> <pid> <log>
  local port="$1" pid="$2" logfile="$3"
  for _ in $(seq 1 400); do
    if curl -fsS "http://127.0.0.1:$port/api/health" > /dev/null 2>&1; then
      return 0
    fi
    if ! kill -0 "$pid" 2>/dev/null; then break; fi
    sleep 0.25
  done
  echo "server on port $port never became healthy" >&2
  tail -30 "$logfile" >&2 || true
  return 1
}

stop_server() { # stop_server <pid>
  local pid="$1"
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 1 60); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.1
  done
  kill -9 "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

collect_endpoints() { # collect_endpoints <base-url> <home> <out-file> <name=path>...
  local base="$1" home="$2" file="$3"; shift 3
  python3 - "$base" "$home" "$file" "$@" <<'PY'
import json
import sys
import urllib.request
from pathlib import Path

base, home, out = sys.argv[1], sys.argv[2], Path(sys.argv[3])
specs = sys.argv[4:]


def get(path):
    with urllib.request.urlopen(base + path, timeout=10) as response:
        return json.loads(response.read().decode())


def markers(root):
    found = []
    for rel in (".migrated-v2", ".migrated-v3", "colonies/.migrations/colony_sessions_v1"):
        if (Path(root) / rel).exists():
            found.append(rel)
    return found


doc = {"home": home, "markers": markers(home)}
# Only the liveness contract of /api/health is compared: the `resources`
# block reports live process/GPU state (verdict, renderer counts), not
# stored data.
health = get("/api/health")
doc["health"] = {"status": health.get("status"), "http": 200}
for spec in specs:
    name, _, path = spec.partition("=")
    doc[name] = get(path)
doc["migrated_file_count"] = sum(
    1
    for top in ("queens", "colonies", "memories", "agents", "exports")
    for p in (Path(home) / top).rglob("*")
    if p.is_file()
) if (Path(home) / "colonies").exists() or (Path(home) / "queens").exists() else 0
out.write_text(json.dumps(doc, indent=2, sort_keys=True), encoding="utf-8")
print(json.dumps({"endpoints": len(specs) + 1, "markers": doc["markers"]}), file=sys.stderr)
PY
}

# ---------------------------------------------------------------------------
# 1. fixtures
# ---------------------------------------------------------------------------

log "building rich fixtures"
mkdir -p "$WORK/templates"
for label in v1-rich v2-rich; do
  if [[ -d "$WORK/templates/$label" ]]; then
    log "reusing existing $label fixture at $WORK/templates/$label"
  else
    record "build $label fixture" python3 "$PY_SCRIPTS/g2_home_fixture.py" --dest "$WORK/templates/$label" --version "$label"
    mkdir -p "$WORK/templates/$label/secrets"
    printf '%s' "$FIXED_KEY" > "$WORK/templates/$label/secrets/credential_key"
    record "seed $label credentials (fixed Fernet key)" py creds-save --home "$WORK/templates/$label"
  fi
done

# ---------------------------------------------------------------------------
# 2. migrate + diff + read probes, one double run per fixture
# ---------------------------------------------------------------------------

for label in v1-rich v2-rich; do
  py_home="$WORK/$label-py"
  go_home="$WORK/$label-go"
  rm -rf "$py_home" "$go_home"
  cp -R "$WORK/templates/$label" "$py_home"
  cp -R "$WORK/templates/$label" "$go_home"
  record "fix $label absolute paths (python copy)" python3 "$PY_SCRIPTS/g2_home_fixture.py" --fix-paths "$py_home"
  record "fix $label absolute paths (go copy)" python3 "$PY_SCRIPTS/g2_home_fixture.py" --fix-paths "$go_home"
  record "freeze $label summary/session mtimes (python copy)" python3 "$PY_SCRIPTS/g2_home_fixture.py" --fresh-summaries "$py_home"
  record "freeze $label summary/session mtimes (go copy)" python3 "$PY_SCRIPTS/g2_home_fixture.py" --fresh-summaries "$go_home"

  log "migrate $label (python)"
  record_to "migrate $label with the Python implementation" "$OUT/$label-py-migrate.json" py migrate --home "$py_home"
  log "migrate $label (go)"
  record_to "migrate $label with the Go implementation" "$OUT/$label-go-migrate.json" go_half migrate --home "$go_home"

  log "read probes $label (python)"
  record_to "read probes $label (python): session lists/histories/colonies/tracker/sidecars/senders/skills/memories/credentials" \
    "$OUT/$label-py-read.json" py read-probes --home "$py_home"
  log "read probes $label (go)"
  record_to "read probes $label (go): same view through the Go APIs" \
    "$OUT/$label-go-read.json" go_half read-probes --home "$go_home"

  log "diff $label: tree / JSON fields / SQLite"
  diffy run --py "$py_home" --go "$go_home" --label "$label" --out "$OUT"

  log "diff $label: read probes"
  record "diff $label read-probe output" \
    python3 "$PY_SCRIPTS/g2_home_read_diff.py" --py "$OUT/$label-py-read.json" --go "$OUT/$label-go-read.json" --out "$OUT/$label-read-diff.json" --label "$label"

  log "credentials cross-read $label"
  printf '\n### credential cross-read %s\n\n```bash\ngo_half read-credential --all --dir %s/credentials --key %s/secrets/credential_key\npy creds-load --home %s\n```\n\n' \
    "$label" "$py_home" "$py_home" "$go_home" >> "$LOG"
  record_to "credential cross-read $label (go reads the python-written store)" "$OUT/$label-go-reads-py-credentials.json" \
    go_half read-credential --all --dir "$py_home/credentials" --key "$py_home/secrets/credential_key"
  record_to "credential cross-read $label (python reads the go copy's store)" "$OUT/$label-py-reads-go-credentials.json" \
    py creds-load --home "$go_home"

  # Verify all four views agree: each side's own read probe, each side reading
  # the other's store.
  python3 - "$OUT" "$label" <<'PY' >> "$LOG" 2>&1
import json
import sys
from pathlib import Path

out, label = Path(sys.argv[1]), sys.argv[2]


def flatten(creds):
    view = {}
    for cred in creds:
        keys = {name: data.get("value") for name, data in cred.get("keys", {}).items()}
        view[cred["id"]] = {"type": cred.get("credential_type"), "keys": keys}
    return view


py_probe = flatten(json.loads((out / f"{label}-py-read.json").read_text())["credentials"])
go_probe = flatten(json.loads((out / f"{label}-go-read.json").read_text())["credentials"])
go_reads_py = flatten(json.loads((out / f"{label}-go-reads-py-credentials.json").read_text())["credentials"])
py_reads_go = flatten(json.loads((out / f"{label}-py-reads-go-credentials.json").read_text())["credentials"])
report = {
    "ids": sorted(py_probe),
    "views": {
        "python_own": py_probe,
        "go_own": go_probe,
        "go_reads_python": go_reads_py,
        "python_reads_go": py_reads_go,
    },
    "equal": py_probe == go_probe == go_reads_py == py_reads_go,
}
(out / f"{label}-credentials-verify.json").write_text(json.dumps(report, indent=2, sort_keys=True), encoding="utf-8")
print(json.dumps({"label": label, "credentials_equal": report["equal"], "ids": report["ids"]}))
if not report["equal"]:
    raise SystemExit("credential cross-read mismatch")
PY
done

# ---------------------------------------------------------------------------
# 3. cross-engine consumption: each implementation on the other's migrated home
# ---------------------------------------------------------------------------
# Replacement direction 1: the Python-migrated home is handed to the Go
# runtime (in place, as a user switching runtimes would). Its idempotent
# migration pass must not rewrite anything, and the Go read view must equal
# the Python one.
# Replacement direction 2: the Go-migrated home is read by the Python
# implementation.

for label in v1-rich v2-rich; do
  py_home="$WORK/$label-py"
  go_home="$WORK/$label-go"

  record_to "manifest: $label python home before go consumes it" "$OUT/$label-cross-before.json" \
    python3 "$PY_SCRIPTS/g2_home_fixture.py" --manifest "$py_home"
  record_to "go migrate pass on the python-migrated $label home" "$OUT/$label-cross-migrate.json" \
    go_half migrate --home "$py_home"
  record_to "go read probes on the python-migrated $label home" "$OUT/$label-cross-go-on-py.json" \
    go_half read-probes --home "$py_home"
  record_to "manifest: $label python home after go consumed it" "$OUT/$label-cross-after.json" \
    python3 "$PY_SCRIPTS/g2_home_fixture.py" --manifest "$py_home"
  record "diff $label (go-on-python reads vs python-own reads)" \
    python3 "$PY_SCRIPTS/g2_home_read_diff.py" --py "$OUT/$label-py-read.json" --go "$OUT/$label-cross-go-on-py.json" \
    --out "$OUT/$label-cross-read-diff.json" --label "$label-cross"

  record_to "python read probes on the go-migrated $label home" "$OUT/$label-cross-py-on-go.json" \
    py read-probes --home "$go_home"
  record "diff $label (python-on-go reads vs go-own reads)" \
    python3 "$PY_SCRIPTS/g2_home_read_diff.py" --py "$OUT/$label-cross-py-on-go.json" --go "$OUT/$label-go-read.json" \
    --out "$OUT/$label-cross-read-diff-reverse.json" --label "$label-cross-reverse"

  python3 - "$OUT" "$label" <<'PY' >> "$LOG" 2>&1
import json
import sys
from pathlib import Path

out, label = Path(sys.argv[1]), sys.argv[2]
before = json.loads((out / f"{label}-cross-before.json").read_text())["files"]
after = json.loads((out / f"{label}-cross-after.json").read_text())["files"]
changed = sorted(rel for rel in set(before) & set(after) if before[rel] != after[rel])
report = {
    "label": label,
    "files_before": len(before),
    "files_after": len(after),
    "only_before": sorted(set(before) - set(after)),
    "only_after": sorted(set(after) - set(before)),
    "changed": changed,
    "explanation": "A Go migrate pass over a Python-migrated home must not rewrite any file.",
}
report["clean"] = not report["only_before"] and not report["only_after"] and not changed
(out / f"{label}-cross-consume.json").write_text(json.dumps(report, indent=2, sort_keys=True), encoding="utf-8")
print(json.dumps({"label": label, "cross_consume_clean": report["clean"], "changed": changed}))
if not report["clean"]:
    raise SystemExit("cross-engine consumption rewrote files")
PY
done

log "building the Go hive binary"
# ---------------------------------------------------------------------------
# 4. real serve binaries: startup migration + API-visible data
# ---------------------------------------------------------------------------
SERVE_BIN="$WORK/hive-serve"
(cd "$GO_MODULE" && CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off go build -o "$SERVE_BIN" ./cmd/hive)

log "ensuring the Python environment"
record "materialise the workspace venv (uv)" bash -c "cd '$ROOT/core' && uv run python -V"
PY_HIVE="$ROOT/.venv/bin/hive"
if [[ ! -x "$PY_HIVE" ]]; then
  echo "python console script missing at $PY_HIVE" >&2
  exit 1
fi

for label in v1-rich v2-rich; do
  # The two fixtures carry different colonies; query the same data surface on
  # both sides (discover + tracker tables/rows/changes per colony).
  if [[ "$label" == "v1-rich" ]]; then
    api_specs=(
      "discover=/api/discover"
      "legacy_tables=/api/colonies/legacy_colony/data/tables"
      "legacy_deals_rows=/api/colonies/legacy_colony/data/tables/deals/rows"
      "legacy_changes=/api/colonies/legacy_colony/data/changes"
      "ops_tables=/api/colonies/ops_colony/data/tables"
    )
  else
    api_specs=(
      "discover=/api/discover"
      "alpha_tables=/api/colonies/alpha/data/tables"
      "alpha_leads_rows=/api/colonies/alpha/data/tables/leads/rows"
      "alpha_invoices_rows=/api/colonies/alpha/data/tables/invoices/rows"
      "alpha_changes=/api/colonies/alpha/data/changes"
      "beta_tables=/api/colonies/beta/data/tables"
      "beta_contacts_rows=/api/colonies/beta/data/tables/contacts/rows"
    )
  fi

  for side in py go; do
    api_home="$WORK/$label-api-$side"
    rm -rf "$api_home"
    cp -R "$WORK/templates/$label" "$api_home"
    python3 "$PY_SCRIPTS/g2_home_fixture.py" --fix-paths "$api_home" >> "$LOG" 2>&1
    python3 "$PY_SCRIPTS/g2_home_fixture.py" --fresh-summaries "$api_home" >> "$LOG" 2>&1
  done

  port="$(free_port)"
  py_log="$OUT/$label-serve-py.log.txt"
  log "serve $label (python) on port $port"
  printf '\n### serve %s (python) on an unmigrated fixture copy\n\n```bash\ncd %s && HIVE_HOME=%s PYTHONPATH=%s/core %s serve --host 127.0.0.1 --port %s\n```\n\n' \
    "$label" "$WORK/$label-api-py" "$WORK/$label-api-py" "$ROOT" "$PY_HIVE" "$port" >> "$LOG"
  # CWD is the home: the Python v2 migration resolves its v1 exports/ source
  # relative to the process CWD, while Go anchors it to $HIVE_HOME. Giving
  # both the same effective input keeps the API check apples-to-apples; the
  # residual difference is documented in docs/g2-lossless-migration.md.
  (
    cd "$WORK/$label-api-py"
    HIVE_HOME="$WORK/$label-api-py" PYTHONPATH="$ROOT/core" exec "$PY_HIVE" serve --host 127.0.0.1 --port "$port"
  ) > "$py_log" 2>&1 &
  py_pid=$!
  wait_health "$port" "$py_pid" "$py_log"
  record_to "collect API payloads $label (python serve, rich $label home)" "$OUT/$label-api-py.json" \
    collect_endpoints "http://127.0.0.1:$port" "$WORK/$label-api-py" "$OUT/$label-api-py.json" "${api_specs[@]}"
  stop_server "$py_pid"

  port="$(free_port)"
  go_log="$OUT/$label-serve-go.log.txt"
  log "serve $label (go) on port $port"
  printf '\n### serve %s (go) on an unmigrated fixture copy\n\n```bash\nHIVE_HOME=%s %s serve --host 127.0.0.1 --port %s\n```\n\n' \
    "$label" "$WORK/$label-api-go" "$SERVE_BIN" "$port" >> "$LOG"
  (HIVE_HOME="$WORK/$label-api-go" "$SERVE_BIN" serve --host 127.0.0.1 --port "$port") > "$go_log" 2>&1 &
  go_pid=$!
  wait_health "$port" "$go_pid" "$go_log"
  record_to "collect API payloads $label (go serve, rich $label home)" "$OUT/$label-api-go.json" \
    collect_endpoints "http://127.0.0.1:$port" "$WORK/$label-api-go" "$OUT/$label-api-go.json" "${api_specs[@]}"
  stop_server "$go_pid"

  record "diff $label API-visible data (python serve vs go serve)" \
    python3 "$PY_SCRIPTS/g2_home_read_diff.py" --py "$OUT/$label-api-py.json" --go "$OUT/$label-api-go.json" --out "$OUT/$label-api-diff.json" --label "$label-api"

  # Startup migration evidence: both serve processes must have produced the
  # same migrated tree (runtime-only files excluded).
  python3 - "$WORK/$label-api-py" "$WORK/$label-api-go" "$OUT/$label-api-tree.json" <<'PY' >> "$LOG" 2>&1
import json
import sys
from pathlib import Path

py_home, go_home, out = Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3])
RUNTIME_PREFIXES = ("logs/", "maintenance/", "mcp_registry/")


def inventory(root):
    return {
        str(p.relative_to(root)): p.stat().st_size
        for p in root.rglob("*")
        if p.is_file() and not str(p.relative_to(root)).startswith(RUNTIME_PREFIXES)
    }


py, go = inventory(py_home), inventory(go_home)
report = {
    "py_home": str(py_home),
    "go_home": str(go_home),
    "py_files": len(py),
    "go_files": len(go),
    "only_py": sorted(set(py) - set(go)),
    "only_go": sorted(set(go) - set(py)),
    "excluded_runtime_prefixes": list(RUNTIME_PREFIXES),
}
report["equal"] = not report["only_py"] and not report["only_go"]
out.write_text(json.dumps(report, indent=2, sort_keys=True), encoding="utf-8")
print(json.dumps({"api_tree_equal": report["equal"], "py_files": len(py), "go_files": len(go)}))
PY
done

# ---------------------------------------------------------------------------
# 4b. behavior-difference evidence: Python serve resolves the v1 `exports/`
# source relative to the process CWD; Go anchors it to $HIVE_HOME.
# ---------------------------------------------------------------------------

cwd_home="$WORK/v1-rich-serve-cwd"
rm -rf "$cwd_home"
cp -R "$WORK/templates/v1-rich" "$cwd_home"
python3 "$PY_SCRIPTS/g2_home_fixture.py" --fix-paths "$cwd_home" >> "$LOG" 2>&1
port="$(free_port)"
cwd_log="$OUT/v1-rich-serve-py-cwd.log.txt"
log "serve v1-rich (python, cwd=$ROOT/core) to record the exports lookup"
printf '\n### serve v1-rich (python, CWD=core) to record the exports lookup\n\n```bash\ncd %s/core && HIVE_HOME=%s PYTHONPATH=%s/core %s serve --host 127.0.0.1 --port %s\n```\n\n' \
  "$ROOT" "$cwd_home" "$ROOT" "$PY_HIVE" "$port" >> "$LOG"
(
  cd "$ROOT/core"
  HIVE_HOME="$cwd_home" PYTHONPATH="$ROOT/core" exec "$PY_HIVE" serve --host 127.0.0.1 --port "$port"
) > "$cwd_log" 2>&1 &
cwd_pid=$!
wait_health "$port" "$cwd_pid" "$cwd_log"
stop_server "$cwd_pid"
python3 - "$cwd_home" "$OUT/v1-rich-serve-cwd.json" <<'PY' >> "$LOG" 2>&1
import json
import sys
from pathlib import Path

home, out = Path(sys.argv[1]), Path(sys.argv[2])
colonies = sorted(p.name for p in (home / "colonies").iterdir() if p.is_dir()) if (home / "colonies").is_dir() else []
data_colonies = [name for name in colonies if name != ".migrations"]
exports = sorted(p.name for p in (home / "exports").iterdir() if p.is_dir()) if (home / "exports").is_dir() else []
markers = sorted(p.name for p in home.iterdir() if p.name.startswith(".migrated-")) if home.is_dir() else []
report = {
    "home": str(home),
    "python_cwd": "core/ (repository module dir)",
    "colonies": colonies,
    "data_colonies_migrated": data_colonies,
    "exports_still_present": exports,
    "markers": markers,
    "exports_migrated": bool(data_colonies),
    "note": (
        "Python `serve` ran its v2 migration but resolved the legacy `exports/` "
        "source relative to the process CWD (core/), so the home's exports/ were "
        "NOT copied into colonies/ while .migrated-v2 was still written. Go's "
        "RunMigrationV2 anchors the same lookup to $HIVE_HOME/exports. In the API "
        "section above, the Python server was started with CWD=home so both "
        "implementations received the same effective input."
    ),
}
out.write_text(json.dumps(report, indent=2, sort_keys=True), encoding="utf-8")
print(json.dumps({"exports_migrated": report["exports_migrated"], "markers": markers}))
PY

# ---------------------------------------------------------------------------
# 5. machine summary
# ---------------------------------------------------------------------------

log "writing summary"
python3 - "$OUT" "$PY_SCRIPTS" <<'PY'
import json
import sys
from pathlib import Path

sys.path.insert(0, sys.argv[2])
from g2_home_read_diff import normalise, walk  # noqa: E402

out = Path(sys.argv[1])
labels = ["v1-rich", "v2-rich"]
summary = {"runs": {}}


def classify_changed(tree, out_dir, label, py_home, go_home):
    """Split byte-changed files into format-only (same parsed data) vs semantic."""
    format_only, semantic = [], []
    for item in tree["changed"]:
        rel = item["path"]
        if not rel.endswith(".json"):
            semantic.append(rel)
            continue
        try:
            py_doc = json.loads((Path(py_home) / rel).read_text(encoding="utf-8"))
            go_doc = json.loads((Path(go_home) / rel).read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            semantic.append(rel)
            continue
        result = {"only_py": [], "only_go": [], "value_mismatches": []}
        walk(
            normalise(py_doc, str(Path(py_home).resolve())),
            normalise(go_doc, str(Path(go_home).resolve())),
            "$",
            result,
        )
        if result["only_py"] or result["only_go"] or result["value_mismatches"]:
            semantic.append(rel)
        else:
            format_only.append(rel)
    return format_only, semantic


for label in labels:
    tree = json.loads((out / f"{label}-tree-diff.json").read_text())
    field = json.loads((out / f"{label}-field-diff.json").read_text())
    sqlite = json.loads((out / f"{label}-sqlite-diff.json").read_text())
    read = json.loads((out / f"{label}-read-diff.json").read_text())
    api = json.loads((out / f"{label}-api-diff.json").read_text())
    api_tree = json.loads((out / f"{label}-api-tree.json").read_text())
    py_creds = json.loads((out / f"{label}-py-reads-go-credentials.json").read_text())
    creds_check = json.loads((out / f"{label}-credentials-verify.json").read_text())
    cross = json.loads((out / f"{label}-cross-consume.json").read_text())
    cross_read = json.loads((out / f"{label}-cross-read-diff.json").read_text())
    cross_read_rev = json.loads((out / f"{label}-cross-read-diff-reverse.json").read_text())
    format_only, semantic = classify_changed(tree, out, label, tree["py_root"], tree["go_root"])
    summary["runs"][label] = {
        "files_py": tree["counts"]["py"],
        "files_go": tree["counts"]["go"],
        "identical_files": len(tree["equal"]),
        "changed_files": [c["path"] for c in tree["changed"]],
        "changed_format_only": format_only,
        "changed_semantic": semantic,
        "opaque_files": tree["changed_opaque"],
        "only_py": tree["only_py"],
        "only_go": tree["only_go"],
        "json_field_diffs": len(field),
        "sqlite_files": len(sqlite),
        "sqlite_mismatches": sorted(
            path for path, value in sqlite.items()
            if not value.get("schema_equal", False) or not value.get("rows_equal", False)
        ),
        "read_probe_mismatches": read["counts"] if "counts" in read else None,
        "api_probe_mismatches": api["counts"] if "counts" in api else None,
        "api_startup_tree_equal": api_tree["equal"],
        "credentials_verify_equal": creds_check["equal"],
        "cross_consume_clean": cross["clean"],
        "cross_read_mismatches": cross_read["counts"],
        "cross_read_reverse_mismatches": cross_read_rev["counts"],
        "credentials_python_reads_go": sorted(py_creds.get("ids", [])),
    }

summary["totals"] = {
    "fixtures": {label: summary["runs"][label]["files_py"] for label in labels},
    "tree_changed": sum(len(summary["runs"][label]["changed_files"]) for label in labels),
    "tree_changed_semantic": sum(len(summary["runs"][label]["changed_semantic"]) for label in labels),
    "tree_unique": sum(len(summary["runs"][label]["only_py"]) + len(summary["runs"][label]["only_go"]) for label in labels),
    "json_field_diffs": sum(summary["runs"][label]["json_field_diffs"] for label in labels),
    "sqlite_mismatches": sum(len(summary["runs"][label]["sqlite_mismatches"]) for label in labels),
    "read_probe_mismatches": sum(
        sum(summary["runs"][label]["read_probe_mismatches"].values())
        for label in labels if summary["runs"][label]["read_probe_mismatches"]
    ),
    "api_probe_mismatches": sum(
        sum(summary["runs"][label]["api_probe_mismatches"].values())
        for label in labels if summary["runs"][label]["api_probe_mismatches"]
    ),
}
summary["green"] = all(
    not run["changed_semantic"]
    and not run["only_py"]
    and not run["only_go"]
    and run["json_field_diffs"] == 0
    and not run["sqlite_mismatches"]
    and (run["read_probe_mismatches"] or {}).get("value_mismatches", 0) == 0
    and (run["api_probe_mismatches"] or {}).get("value_mismatches", 0) == 0
    and run["api_startup_tree_equal"]
    and run["credentials_verify_equal"]
    and run["cross_consume_clean"]
    and sum(run["cross_read_mismatches"].values()) == 0
    and sum(run["cross_read_reverse_mismatches"].values()) == 0
    for run in summary["runs"].values()
)
(out / "summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8")

lines = ["# G2 lossless migration — rich legacy-home double run", ""]
lines.append("Generated by `go_hive/scripts/g2_home_e2e_run.sh`. See `commands.md` for every command + stdout.")
lines.append("")
lines.append("| fixture | files py/go | identical | changed | format-only | semantic | opaque | unique | JSON field diffs | SQLite mismatches | read probes | API probes | credentials | cross-consume | cross reads py↔go | startup tree |")
lines.append("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- | ---: | --- |")
for label in labels:
    run = summary["runs"][label]
    read = run["read_probe_mismatches"] or {}
    api = run["api_probe_mismatches"] or {}
    lines.append(
        f"| {label} | {run['files_py']}/{run['files_go']} | {run['identical_files']} | {len(run['changed_files'])} | "
        f"{len(run['changed_format_only'])} | {len(run['changed_semantic'])} | "
        f"{len(run['opaque_files'])} | {len(run['only_py']) + len(run['only_go'])} | {run['json_field_diffs']} | "
        f"{len(run['sqlite_mismatches'])} | {sum(read.values())} | {sum(api.values())} | "
        f"{'ok' if run['credentials_verify_equal'] else 'DIFF'} | {'ok' if run['cross_consume_clean'] else 'DIFF'} | "
        f"{sum(run['cross_read_mismatches'].values())}/{sum(run['cross_read_reverse_mismatches'].values())} | "
        f"{'ok' if run['api_startup_tree_equal'] else 'DIFF'} |"
    )
lines.append("")
lines.append(f"Verdict: {'GREEN' if summary['green'] else 'RED'}")
(out / "summary.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
print(json.dumps({"green": summary["green"], "totals": summary["totals"]}, indent=2))
PY

log "g2 emblem: $OUT/summary.md"
if grep -q "Verdict: GREEN" "$OUT/summary.md"; then
  log "GREEN"
else
  log "RED - inspect $OUT/summary.json"
  exit 1
fi
