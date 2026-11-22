#!/usr/bin/env bash
# perf_recall_15k.sh — reproducible recall/search performance evidence on a
# synthetic 15,000-turn store (multiple queen scopes).
#
# What it does (all offline, no LLM, no network):
#   1. builds the harness (go-hive/e2e/perfrecall15k/main.go, a build-ignored
#      file — not part of the product build) and the hive binary;
#   2. generates a deterministic 15,000-turn store (3 queen scopes) into a
#      scratch HIVE_HOME and records its size/lines;
#   3. benches the recall index cold build, the first-query-after-upgrade path,
#      120 warm recall queries, index load+parse cost, search_messages /
#      search_timeline latency, peak RSS, index file sizes, and a CPU profile
#      for bottleneck attribution;
#   4. measures `hive serve` time-to-health on an empty home and on a copy of
#      the 15k home (no derived index), plus their RSS;
#   5. renders summary.txt from the JSON evidence.
#
# Usage:
#   go_hive/scripts/perf_recall_15k.sh [--turns N] [--seed S] [--home DIR]
#                                      [--out DIR] [--runs N] [--reuse-home]
#
# Re-run with --reuse-home to skip regeneration and re-measure the same store.
# Outputs (raw evidence) land in go_hive/docs/e2e-assets/perf-recall-2026-11-22/
# by default; method and interpretation: go_hive/docs/perf-recall-15k.md.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODULE="$ROOT/go-hive"
OUT="$ROOT/docs/e2e-assets/perf-recall-2026-11-22"
TURNS=15000
SEED=20261122
HOME_DIR=""
REUSE_HOME=0
RUNS=3

while [ $# -gt 0 ]; do
  case "$1" in
    --turns) TURNS="$2"; shift 2 ;;
    --seed) SEED="$2"; shift 2 ;;
    --home) HOME_DIR="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --runs) RUNS="$2"; shift 2 ;;
    --reuse-home) REUSE_HOME=1; shift ;;
    -h|--help)
      sed -n '2,30p' "${BASH_SOURCE[0]}"
      exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod
SCRATCH="${TMPDIR:-/tmp}/perf-recall-15k"
BIN="$SCRATCH/bin"
mkdir -p "$OUT" "$BIN" "$SCRATCH"
if [ -z "$HOME_DIR" ]; then
  HOME_DIR="$SCRATCH/home-${TURNS}"
fi

# Capture the full transcript into the evidence directory (tee keeps the
# console output). Written as .txt because the repo gitignores *.log.
exec > >(tee "$OUT/run.txt") 2>&1

{
  echo "== environment =="
  echo "date:    $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "commit:  $(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)"
  echo "branch:  $(git -C "$ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
  echo "host:    $(uname -srm)"
  echo "cpu:     $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
  echo "mem:     $(sysctl -n hw.memsize 2>/dev/null || echo unknown) bytes"
  echo "go:      $(go version)"
  echo "load:    $(sysctl -n vm.loadavg 2>/dev/null || echo unknown)"
  echo "module:  $MODULE"
  echo "turns:   $TURNS (seed $SEED), runs/serve=$RUNS"
  echo "home:    $HOME_DIR"
} | tee "$OUT/env.txt"

echo "== build harness + hive binary =="
(
  cd "$MODULE"
  go build -o "$BIN/perfrecall" e2e/perfrecall15k/main.go
  go build -o "$BIN/hive" ./cmd/hive
)

if [ "$REUSE_HOME" -eq 1 ] && [ -d "$HOME_DIR/queens" ]; then
  echo "== reusing generated home $HOME_DIR ==" | tee "$OUT/gen.txt"
else
  rm -rf "$HOME_DIR"
  echo "== generate $TURNS turns =="
  "$BIN/perfrecall" gen -home "$HOME_DIR" -turns "$TURNS" -seed "$SEED" -json "$OUT/gen.json" | tee "$OUT/gen.txt"
fi

{
  echo "== data sizes =="
  echo "home: $HOME_DIR"
  du -sh "$HOME_DIR"
  for d in "$HOME_DIR"/queens/*/sessions; do
    q="$(basename "$(dirname "$d")")"
    sessions="$(find "$d" -maxdepth 1 -type d -name 'session_*' | wc -l | tr -d ' ')"
    lines="$(find "$d" -name events.jsonl -print0 | xargs -0 cat | wc -l | tr -d ' ')"
    kib="$(du -sk "$d" | cut -f1)"
    echo "$q sessions=$sessions event_lines=$lines size=${kib}KiB"
  done
  echo "events.jsonl lines (all):   $(find "$HOME_DIR/queens" -name events.jsonl -print0 | xargs -0 cat | wc -l | tr -d ' ')"
  echo "timeline.jsonl lines (all): $(find "$HOME_DIR/queens" -name timeline.jsonl -print0 | xargs -0 cat | wc -l | tr -d ' ')"
  echo "session dirs (all):         $(find "$HOME_DIR/queens" -maxdepth 3 -type d -name 'session_*' | wc -l | tr -d ' ')"
} | tee "$OUT/sizes.txt"

# Serve measurement home: the same turns, copied before any derived index
# exists, so the boot path is the upgrade-like one.
SERVE_HOME="$SCRATCH/serve-home-${TURNS}"
rm -rf "$SERVE_HOME"
mkdir -p "$SERVE_HOME"
cp -R "$HOME_DIR/queens" "$SERVE_HOME/queens"

echo "== bench (recall index, warm queries, search tools, RSS) =="
"$BIN/perfrecall" bench -home "$HOME_DIR" -json "$OUT/bench.json" -cpuprofile "$OUT/cpu.pprof" | tee "$OUT/bench.txt"

# Index files after the bench rebuilt them (sizes, turn counts).
{
  echo "== recall index files =="
  du -sk "$HOME_DIR/.message_index" || true
  find "$HOME_DIR/.message_index" -type f -name '*.json' -exec ls -l {} +
} > "$OUT/index-files.txt"

echo "== CPU profile attribution (optional extra 20 warm queries) =="
if [ -f "$OUT/cpu.pprof" ]; then
  ( cd "$MODULE" && go tool pprof -top -nodecount=30 "$BIN/perfrecall" "$OUT/cpu.pprof" ) > "$OUT/pprof-top.txt" 2>&1 || true
  ( cd "$MODULE" && go tool pprof -top -cum -nodecount=30 "$BIN/perfrecall" "$OUT/cpu.pprof" ) > "$OUT/pprof-cum.txt" 2>&1 || true
fi

echo "== serve cold start: empty home =="
EMPTY_HOME="$SCRATCH/empty-home"
rm -rf "$EMPTY_HOME"
mkdir -p "$EMPTY_HOME"
"$BIN/perfrecall" servebench -binary "$BIN/hive" -home "$EMPTY_HOME" -label empty -runs "$RUNS" -port0 8810 -logdir "$OUT" -json "$OUT/serve-empty.json" | tee "$OUT/serve-empty.txt"

echo "== serve cold start: ${TURNS}-turn home =="
"$BIN/perfrecall" servebench -binary "$BIN/hive" -home "$SERVE_HOME" -label 15k -runs "$RUNS" -port0 8820 -logdir "$OUT" -json "$OUT/serve-15k.json" | tee "$OUT/serve-15k.txt"

echo "== summary =="
REPORT_ARGS=(-bench "$OUT/bench.json" -serve-empty "$OUT/serve-empty.json" -serve-15k "$OUT/serve-15k.json" -out "$OUT/summary.txt")
if [ -f "$OUT/gen.json" ]; then
  REPORT_ARGS+=(-gen "$OUT/gen.json")
fi
"$BIN/perfrecall" report "${REPORT_ARGS[@]}" | tee "$OUT/summary.stdout.txt"

echo
echo "raw evidence: $OUT"
