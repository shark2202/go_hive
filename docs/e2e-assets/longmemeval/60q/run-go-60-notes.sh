#!/usr/bin/env bash
# LongMemEval n=60 — Go side WITH memory notes (the Go equivalent of upstream
# HIVE_LME_NOTES=1: hive-longmemeval --notes).
# Evidence package for go_hive/docs/e2e-assets/longmemeval/2026-11-22-60q-notes-compare.md.
#
#   pass 1:      bash run-go-60-notes.sh
#   prebuilt:    SKIP_PREBUILD=1 bash run-go-60-notes.sh   (reuse notes_cache)
#   concurrency: HIVE_LME_NOTES_CONCURRENCY=8 bash run-go-60-notes.sh
#
# Step 1 (`notes`) reflects on each seeded session, oldest first, and caches
# per question (<data>/notes_cache/<model>/<prompt version>/<variant>_<id>),
# in parallel across questions. Step 2 (`run --notes`) copies each cache into
# the question's isolated HIVE_HOME and asks the queen there — so the measured
# run has the notes production would have left. Answering model and judge come
# from the main LLM slot (~/.hive-go/env, BROADTOKEN_API_KEY); the key is never
# printed.
set -euo pipefail
set +x
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_HIVE="$(cd "$HERE/../../../.." && pwd)"
WT="$(cd "$GO_HIVE/.." && pwd)"
DATA="${HIVE_LME_DATA:-$HOME/.hive-go/longmemeval-data}"
BIN="${BIN:-/tmp/hive-longmemeval-lm60notes}"
RUN_DIR="${RUN_DIR:-$DATA/runs/go-60-notes}"
CACHE="${HIVE_LME_NOTES_CACHE_DIR:-$DATA/notes_cache}"

source "$HOME/.hive-go/env"
export HIVE_LME_DATA="$DATA"
export HIVE_LME_API_BASE="${HIVE_LME_API_BASE:-https://token.tokenbroad.com/v1}"
export HIVE_LME_MODEL="${HIVE_LME_MODEL:-deepseek-flash-1017}"
export HIVE_LME_API_KEY="$BROADTOKEN_API_KEY"
export HIVE_LME_NOTES_MODEL="${HIVE_LME_NOTES_MODEL:-$HIVE_LME_MODEL}"
export HIVE_LME_JUDGE_API_BASE="${HIVE_LME_JUDGE_API_BASE:-https://token.tokenbroad.com/v1}"
export HIVE_LME_JUDGE_MODEL="${HIVE_LME_JUDGE_MODEL:-deepseek-v4-pro}"
export HIVE_LME_JUDGE_API_KEY="$BROADTOKEN_API_KEY"

if [ -n "${RETRY_IDS:-}" ]; then
  IDS="$RETRY_IDS"
else
  IDS="$(cd "$WT" && uv run --offline python -c "import json;print(','.join(json.load(open('go_hive/docs/e2e-assets/longmemeval/60q/sample-n60.json'))['question_ids']))")"
fi

echo "go-60-notes: $(printf '%s' "$IDS" | tr ',' '\n' | wc -l | tr -d ' ') question(s) -> $RUN_DIR (notes on)"
if [ "${SKIP_PREBUILD:-0}" != "1" ] && [ -z "${RETRY_IDS:-}" ]; then
  "$BIN" notes \
    --data "$DATA/s_cleaned" \
    --questions "$IDS" \
    --notes-model "$HIVE_LME_NOTES_MODEL" \
    --notes-api-base "$HIVE_LME_API_BASE" \
    --notes-cache-dir "$CACHE" \
    --concurrency "${HIVE_LME_NOTES_CONCURRENCY:-8}" \
    --verbose
fi
exec "$BIN" run \
  --data "$DATA/s_cleaned" \
  --questions "$IDS" \
  --run-dir "$RUN_DIR" \
  --notes \
  --notes-model "$HIVE_LME_NOTES_MODEL" \
  --notes-api-base "$HIVE_LME_API_BASE" \
  --notes-cache-dir "$CACHE" \
  --timeout 420 \
  --verbose
