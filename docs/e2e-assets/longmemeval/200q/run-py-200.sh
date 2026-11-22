#!/usr/bin/env bash
# LongMemEval n=200 — Python side, no notes (pytest tests/e2e/test_longmemeval.py):
# same sample, real queen, real judge.
# Evidence package for go_hive/docs/e2e-assets/longmemeval/2026-11-22-200q-real-run.md.
#
#   pass 1:  bash run-py-200.sh
#   retry:   RETRY_IDS=id1,id2 bash run-py-200.sh   (run 200q/prune_failed.py first)
#   workers: PYTEST_WORKERS=6 bash run-py-200.sh
set -euo pipefail
set +x
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_HIVE="$(cd "$HERE/../../../.." && pwd)"
WT="$(cd "$GO_HIVE/.." && pwd)"
DATA="${HIVE_LME_DATA:-$HOME/.hive-go/longmemeval-data}"
RUN_DIR="${RUN_DIR:-$DATA/runs/py-200}"

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

if [ -n "${RETRY_IDS:-}" ]; then
  IDS="$RETRY_IDS"
else
  IDS="$(cd "$WT" && uv run --offline python -c "import json;print(','.join(json.load(open('go_hive/docs/e2e-assets/longmemeval/200q/sample-n200.json'))['question_ids']))")"
fi
export HIVE_LME_QUESTIONS="$IDS"

echo "py-200: $(printf '%s' "$IDS" | tr ',' '\n' | wc -l | tr -d ' ') question(s) -> $RUN_DIR"
cd "$WT/core"
exec uv run --offline pytest tests/e2e/test_longmemeval.py -m live -n "${PYTEST_WORKERS:-4}"
