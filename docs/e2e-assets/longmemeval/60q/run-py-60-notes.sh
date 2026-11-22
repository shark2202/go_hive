#!/usr/bin/env bash
# LongMemEval n=60 — Python side WITH memory notes (upstream HIVE_LME_NOTES=1).
# Evidence package for go_hive/docs/e2e-assets/longmemeval/2026-11-22-60q-notes-compare.md.
#
#   pass 1:  bash run-py-60-notes.sh
#   workers: PYTEST_WORKERS=8 bash run-py-60-notes.sh
#   retry:   RETRY_IDS=id1,id2 bash run-py-60-notes.sh (prune those rows first)
#
# The first run over each question reflects on every session of its history
# (cached under $HIVE_LME_DATA/notes_cache/<model>/<prompt hash>); later runs
# copy the cache. Answering model and judge come from the main LLM slot
# (~/.hive-go/env, BROADTOKEN_API_KEY); the key is never printed.
set -euo pipefail
set +x
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_HIVE="$(cd "$HERE/../../../.." && pwd)"
WT="$(cd "$GO_HIVE/.." && pwd)"
DATA="${HIVE_LME_DATA:-$HOME/.hive-go/longmemeval-data}"
RUN_DIR="${RUN_DIR:-$DATA/runs/py-60-notes}"

source "$HOME/.hive-go/env"
export HIVE_LIVE_API_BASE="${HIVE_LIVE_API_BASE:-https://token.tokenbroad.com/v1}"
export HIVE_LIVE_MODEL="${HIVE_LIVE_MODEL:-deepseek-flash-1017}"
export HIVE_LIVE_API_KEY="$BROADTOKEN_API_KEY"
export HIVE_LME_JUDGE_MODEL="${HIVE_LME_JUDGE_MODEL:-deepseek-v4-pro}"
export HIVE_LME_JUDGE_API_BASE="${HIVE_LME_JUDGE_API_BASE:-https://token.tokenbroad.com/v1}"
export HIVE_LME_JUDGE_API_KEY="$BROADTOKEN_API_KEY"
export HIVE_LME_DATA="$DATA"
export HIVE_LME_RUN_DIR="$RUN_DIR"
export HIVE_LME_RESUME="${HIVE_LME_RESUME:-1}"
# The notes treatment: build each question's memory notes from its seeded
# history before asking (upstream switch; cached after the first run).
export HIVE_LME_NOTES=1

if [ -n "${RETRY_IDS:-}" ]; then
  IDS="$RETRY_IDS"
else
  IDS="$(cd "$WT" && uv run --offline python -c "import json;print(','.join(json.load(open('go_hive/docs/e2e-assets/longmemeval/60q/sample-n60.json'))['question_ids']))")"
fi
export HIVE_LME_QUESTIONS="$IDS"

echo "py-60-notes: $(printf '%s' "$IDS" | tr ',' '\n' | wc -l | tr -d ' ') question(s) -> $RUN_DIR (notes on)"
cd "$WT/core"
exec uv run --offline pytest tests/e2e/test_longmemeval.py -m live -n "${PYTEST_WORKERS:-8}"
