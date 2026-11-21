#!/usr/bin/env bash
# refactor_freeze.sh — behavior-freeze proof for structural refactors.
#
# A structural change (package split, file move, rename) must not move any
# frozen surface. This script captures sha256 digests of the canonical
# artifacts and can compare a later tree against a captured baseline.
#
#   scripts/refactor_freeze.sh capture <label>   # write docs/refactor-freeze/<label>.json
#   scripts/refactor_freeze.sh compare <label>   # recompute and diff; exit 1 on drift
#   scripts/refactor_freeze.sh list <label>      # print the baseline table
#
# Drift is allowed only when the path is listed in
# docs/refactor-freeze/allowlist.json with a reason (intentional change).
# Everything else fails: the proof is that the refactor moved nothing.
#
# Evidence is repo-relative and reproducible: no /tmp paths, no clock reads.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
FREEZE_DIR="$REPO_ROOT/go_hive/docs/refactor-freeze"

# Canonical frozen artifacts, repo-relative (REPO_ROOT = /Users/mac/hive).
ARTIFACTS=(
  "go_hive/contracts/tool-definitions.json"
  "go_hive/contracts/cli-surface.json"
  "go_hive/contracts/api-routes.json"
  "go_hive/contracts/storage-fields.json"
  "contracts/module-inventory.json"
  "go_hive/docs/tool-contract-recheck.json"
  "go_hive/docs/appearance-scope-registry.json"
  "go_hive/docs/cli-surface-alignment.json"
  "go_hive/docs/skills-parity-report.json"
  "go_hive/docs/tool-contract-diff.md"
)

usage() {
  echo "usage: refactor_freeze.sh {capture|compare|list} <label>" >&2
  exit 2
}

sha_of() {
  # sha256 of a file, or "missing" when it is absent (absence is signal, not a pass).
  local rel="$1"
  if [ -f "$REPO_ROOT/$rel" ]; then
    shasum -a 256 "$REPO_ROOT/$rel" | awk '{print $1}'
  else
    echo "missing"
  fi
}

count_of() {
  # Best-effort readable counts for a few known artifacts (0 when absent/unparsable).
  local rel="$1" out=0
  case "$rel" in
    */tool-definitions.json)
      out=$(python3 - "$REPO_ROOT/$rel" <<'PY' 2>/dev/null || echo 0
import json,sys
d=json.load(open(sys.argv[1]))
print(len(d.get("tools") or d.get("definitions") or []))
PY
      ) ;;
    */api-routes.json)
      out=$(python3 - "$REPO_ROOT/$rel" <<'PY' 2>/dev/null || echo 0
import json,sys
d=json.load(open(sys.argv[1]))
print(len(d.get("routes") or []))
PY
      ) ;;
    */module-inventory.json)
      out=$(python3 - "$REPO_ROOT/$rel" <<'PY' 2>/dev/null || echo 0
import json,sys
d=json.load(open(sys.argv[1]))
print(len(d.get("files") or []))
PY
      ) ;;
  esac
  echo "$out"
}

capture() {
  local label="$1" rev
  rev="$(git -C "$REPO_ROOT" rev-parse HEAD)"
  mkdir -p "$FREEZE_DIR"
  local out="$FREEZE_DIR/$label.json"
  {
    echo "{"
    echo "  \"label\": \"$label\","
    echo "  \"git_rev\": \"$rev\","
    echo "  \"artifacts\": {"
    local first=1
    for rel in "${ARTIFACTS[@]}"; do
      [ $first -eq 1 ] && first=0 || echo ","
      printf '    "%s": {"sha256": "%s", "count": %s}' "$rel" "$(sha_of "$rel")" "$(count_of "$rel")"
    done
    echo ""
    echo "  }"
    echo "}"
  } > "$out"
  echo "captured $label @ $rev -> $out"
  if git -C "$REPO_ROOT" ls-files --error-unmatch "go_hive/docs/refactor-freeze/$label.json" >/dev/null 2>&1; then
    echo "tracked: yes"
  else
    echo "tracked: no (add it to git so a clone can rebuild the verdict)"
  fi
}

compare() {
  local label="$1"
  local base="$FREEZE_DIR/$label.json"
  [ -f "$base" ] || { echo "no baseline: $base" >&2; exit 1; }
  local allow="$FREEZE_DIR/allowlist.json"
  python3 - "$REPO_ROOT" "$base" "$allow" <<'PY'
import hashlib, json, os, subprocess, sys

repo, base_path, allow_path = sys.argv[1], sys.argv[2], sys.argv[3]
base = json.load(open(base_path))
allow = {}
if os.path.exists(allow_path):
    allow = {e["path"]: e.get("reason", "") for e in json.load(open(allow_path)).get("allowed", [])}

def sha(rel):
    p = os.path.join(repo, rel)
    if not os.path.isfile(p):
        return "missing"
    h = hashlib.sha256()
    with open(p, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()

drift, allowed, missing = [], [], []
for rel, meta in base["artifacts"].items():
    now = sha(rel)
    if now == meta["sha256"]:
        continue
    if now == "missing" or meta["sha256"] == "missing":
        missing.append(rel)
    elif rel in allow:
        allowed.append((rel, allow[rel]))
    else:
        drift.append(rel)

print(f"baseline {base['label']} @ {base['git_rev']}")
print(f"artifacts checked: {len(base['artifacts'])}")
for rel, reason in allowed:
    print(f"  ALLOWED  {rel} — {reason}")
for rel in missing:
    print(f"  MISSING  {rel}")
for rel in drift:
    print(f"  DRIFT    {rel}")

if drift or missing:
    print("refactor-freeze: FAIL — frozen surface moved")
    sys.exit(1)
print("refactor-freeze: OK — frozen surface unchanged" + (" (with allowlisted changes)" if allowed else ""))
PY
}

list_baseline() {
  local label="$1" base="$FREEZE_DIR/$label.json"
  [ -f "$base" ] || { echo "no baseline: $base" >&2; exit 1; }
  python3 - "$base" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
print(f"{d['label']} @ {d['git_rev']}")
for rel, meta in d["artifacts"].items():
    print(f"  {meta['sha256'][:16]}  count={meta['count']:<6} {rel}")
PY
}

[ $# -eq 2 ] || usage
case "$1" in
  capture) capture "$2" ;;
  compare) compare "$2" ;;
  list)    list_baseline "$2" ;;
  *)       usage ;;
esac
