#!/usr/bin/env bash
# LongMemEval n=60 — Go side (hive-longmemeval run): real queen, real judge.
# Evidence package for go_hive/docs/e2e-assets/longmemeval/2026-11-22-60q-real-run.md.
#
#   pass 1:  bash run-go-60.sh
#   retry:   RETRY_IDS=id1,id2 bash run-go-60.sh    (prune those rows from results.jsonl first)
#
# Answering model and judge both come from the main LLM slot (~/.hive-go/env,
# BROADTOKEN_API_KEY); the key is never printed.
set -euo pipefail
set +x
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_HIVE="$(cd "$HERE/../../../.." && pwd)"
WT="$(cd "$GO_HIVE/.." && pwd)"
DATA="${HIVE_LME_DATA:-$HOME/.hive-go/longmemeval-data}"
BIN="${BIN:-/tmp/hive-longmemeval-lm60}"
RUN_DIR="${RUN_DIR:-$DATA/runs/go-60}"

source "$HOME/.hive-go/env"
export HIVE_LME_API_BASE="${HIVE_LME_API_BASE:-https://token.tokenbroad.com/v1}"
export HIVE_LME_MODEL="${HIVE_LME_MODEL:-deepseek-flash-1017}"
export HIVE_LME_API_KEY="$BROADTOKEN_API_KEY"
export HIVE_LME_JUDGE_API_BASE="${HIVE_LME_JUDGE_API_BASE:-https://token.tokenbroad.com/v1}"
export HIVE_LME_JUDGE_MODEL="${HIVE_LME_JUDGE_MODEL:-deepseek-v4-pro}"
export HIVE_LME_JUDGE_API_KEY="$BROADTOKEN_API_KEY"

if [ -n "${RETRY_IDS:-}" ]; then
  IDS="$RETRY_IDS"
else
  IDS="$(cd "$WT" && uv run --offline python -c "import json;print(','.join(json.load(open('go_hive/docs/e2e-assets/longmemeval/60q/sample-n60.json'))['question_ids']))")"
fi

echo "go-60: $(printf '%s' "$IDS" | tr ',' '\n' | wc -l | tr -d ' ') question(s) -> $RUN_DIR"
exec "$BIN" run --data "$DATA/s_cleaned" --questions "$IDS" --run-dir "$RUN_DIR" --timeout 420 --verbose
