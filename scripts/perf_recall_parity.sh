#!/usr/bin/env bash
# perf_recall_parity.sh — prove a change to the past-recall path returns
# byte-identical results to a baseline commit on the same 15k-turn store.
#
# It builds the same dump tool (go-hive/e2e/paritydump/main.go, a build-ignored
# file) twice: once against the baseline commit exported by `git archive`, once
# against the current working tree. Both dumps run the same query file against
# a copy of the same home and the JSONL outputs are compared byte-for-byte
# (then record-by-record, to report where a difference starts).
#
# Usage:
#   go_hive/scripts/perf_recall_parity.sh --home "$TMPDIR/perf-recall-15k/home-15000"
#                                         [--base-ref d9aab9cf]
#                                         [--queries FILE]
#                                         [--out DIR]
#                                         [--owners queen_alpha,queen_beta,queen_gamma]
#                                         [--no-exclude]
#
# Prerequisite: the home from go_hive/scripts/perf_recall_15k.sh (the script
# generates and caches the 120-query file when --queries is absent). Offline:
# CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod.
#
# Exit codes: 0 = identical; 1 = results differ (report says where); 2 = usage
# or build problem.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODULE="$ROOT/go-hive"
REPO_ROOT="$(git -C "$ROOT" rev-parse --show-toplevel)"
OUT="$ROOT/docs/e2e-assets/perf-opt-2026-11-22/parity"
BASE_REF="d9aab9cf"
HOME_DIR=""
QUERIES=""
OWNERS="queen_alpha,queen_beta,queen_gamma"
EXCLUDE=1

while [ $# -gt 0 ]; do
  case "$1" in
    --home) HOME_DIR="$2"; shift 2 ;;
    --base-ref) BASE_REF="$2"; shift 2 ;;
    --queries) QUERIES="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --owners) OWNERS="$2"; shift 2 ;;
    --no-exclude) EXCLUDE=0; shift ;;
    -h|--help) sed -n '2,30p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "perf_recall_parity: unknown argument: $1" >&2; exit 2 ;;
  esac
done
if [ -z "$HOME_DIR" ]; then
  echo "perf_recall_parity: --home is required (the store from perf_recall_15k.sh)" >&2
  exit 2
fi
if [ ! -d "$HOME_DIR/queens" ]; then
  echo "perf_recall_parity: $HOME_DIR has no queens/ store" >&2
  exit 2
fi

export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod
WORK="${PERF_PARITY_WORK:-${TMPDIR:-/tmp}/perf-recall-parity}"
mkdir -p "$OUT" "$WORK/bin" "$WORK/home-base" "$WORK/home-new"

BASE_SRC="$WORK/base-src"
if [ ! -f "$BASE_SRC/go_hive/go-hive/go.mod" ]; then
  echo "== export baseline $BASE_REF =="
  rm -rf "$BASE_SRC"
  mkdir -p "$BASE_SRC"
  git -C "$REPO_ROOT" archive "$BASE_REF" -- go_hive/go-hive go_hive/scripts | tar -x -C "$BASE_SRC"
fi
# Build the same dump source against both trees: only internal/memory differs.
mkdir -p "$BASE_SRC/go_hive/go-hive/e2e/paritydump"
cp "$MODULE/e2e/paritydump/main.go" "$BASE_SRC/go_hive/go-hive/e2e/paritydump/main.go"

echo "== build dump binaries (base=$BASE_REF, new=working tree) =="
( cd "$BASE_SRC/go_hive/go-hive" && go build -o "$WORK/bin/paritydump-base" e2e/paritydump/main.go )
( cd "$MODULE" && go build -o "$WORK/bin/paritydump-new" e2e/paritydump/main.go )

if [ -z "$QUERIES" ]; then
  QUERIES="$OUT/queries.txt"
  uv run --no-project python "$ROOT/scripts/perf_recall_queries.py" "$HOME_DIR" "$QUERIES"
fi
echo "== queries: $QUERIES ($(wc -l < "$QUERIES" | tr -d ' ') lines) =="

EXCLUDE_FLAG=(-exclude=true)
if [ "$EXCLUDE" -eq 0 ]; then EXCLUDE_FLAG=(-exclude=false); fi

# Both dumps run on byte-identical copies of the same home (derived index
# included), so neither run can change the input the other sees.
rm -rf "$WORK/home-base" "$WORK/home-new"
cp -R "$HOME_DIR" "$WORK/home-base"
cp -R "$HOME_DIR" "$WORK/home-new"

echo "== dump base =="
"$WORK/bin/paritydump-base" -home "$WORK/home-base" -queries "$QUERIES" -out "$OUT/results-base.jsonl" -owners "$OWNERS" "${EXCLUDE_FLAG[@]}"
echo "== dump new =="
"$WORK/bin/paritydump-new" -home "$WORK/home-new" -queries "$QUERIES" -out "$OUT/results-new.jsonl" -owners "$OWNERS" "${EXCLUDE_FLAG[@]}"

REPORT="$OUT/parity-report.txt"
{
  echo "perf_recall_parity: base=$BASE_REF ($(git -C "$REPO_ROOT" rev-parse --short "$BASE_REF"))"
  echo "perf_recall_parity: queries=$(wc -l < "$QUERIES" | tr -d ' ') owners=$OWNERS exclude_variant=$EXCLUDE"
  echo "perf_recall_parity: base_results=$(wc -l < "$OUT/results-base.jsonl" | tr -d ' ') lines new_results=$(wc -l < "$OUT/results-new.jsonl" | tr -d ' ') lines"
  echo "sha256 base: $(shasum -a 256 "$OUT/results-base.jsonl" | cut -d' ' -f1)"
  echo "sha256 new:  $(shasum -a 256 "$OUT/results-new.jsonl" | cut -d' ' -f1)"
} | tee "$REPORT"

uv run --no-project python - "$OUT/results-base.jsonl" "$OUT/results-new.jsonl" "$REPORT" <<'PY' | tee -a "$REPORT"
import json
import sys

base_path, new_path, report = sys.argv[1:4]


def stats(path):
    records = turns = 0
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            record = json.loads(line)
            if record.get("kind") != "query":
                continue
            records += 1
            turns += len(record.get("turns") or [])
    return records, turns


base_records, base_turns = stats(base_path)
new_records, new_turns = stats(new_path)
print(f"perf_recall_parity: records base={base_records} new={new_records}; turns base={base_turns} new={new_turns}")
PY

if cmp -s "$OUT/results-base.jsonl" "$OUT/results-new.jsonl"; then
  printf '%s\n' "PARITY: PASS — every dumped turn and score is byte-identical" | tee -a "$REPORT"
  exit 0
fi

echo "PARITY: FAIL — first differing record:" | tee -a "$REPORT"
uv run --no-project python - "$OUT/results-base.jsonl" "$OUT/results-new.jsonl" <<'PY' | tee -a "$REPORT"
import json, sys
base_path, new_path = sys.argv[1], sys.argv[2]
base = [json.loads(line) for line in open(base_path, encoding="utf-8")]
new = [json.loads(line) for line in open(new_path, encoding="utf-8")]
print(f"records base={len(base)} new={len(new)}")
for i, (a, b) in enumerate(zip(base, new)):
    if a != b:
        print(f"record {i}: owner={a.get('owner')} query={a.get('query')!r} exclude={a.get('exclude')!r}")
        print(f"  base turns={len(a.get('turns') or [])} new turns={len(b.get('turns') or [])}")
        for j, (ta, tb) in enumerate(zip(a.get("turns") or [], b.get("turns") or [])):
            if ta != tb:
                print(f"  first differing turn {j}: base={ta} new={tb}")
                break
        break
else:
    print("common prefix identical; length differs")
PY
exit 1
