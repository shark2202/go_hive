#!/usr/bin/env bash
# 时间治理仪表：每次检查点输出一行指标并追加到 ledger.csv
# 用法：bash go_hive/scripts/checkpoint.sh [备注]
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
GO="$ROOT/go_hive/go-hive"
PY="$ROOT/core/framework"
TOOLS="$ROOT/tools/src/aden_tools/tools"

routes_go=$(grep -rhoE '"(GET|POST|PUT|DELETE|PATCH) /[^"]*"' "$GO/internal/server" --include=*.go 2>/dev/null | sed -E 's/"(GET|POST|PUT|DELETE|PATCH) //; s/"$//' | sort -u | wc -l | tr -d ' ')
routes_py=$(grep -rhoE '\.router\.add_[a-z]+\("[^"]*"' "$PY" --include=*.py 2>/dev/null | sed -E 's/.*\("//; s/"$//' | sort -u | wc -l | tr -d ' ')
tests=$(grep -rhoE '^func (Test|Fuzz|Benchmark)[A-Za-z0-9_]+' "$GO" --include=*_test.go 2>/dev/null | wc -l | tr -d ' ')
tool_dirs=$(ls -d "$TOOLS"/*/ 2>/dev/null | wc -l | tr -d ' ')
tool_go=$(ls "$GO/internal/tools/integrations"/*.go 2>/dev/null | wc -l | tr -d ' ')
parity_missing=$(bash "$ROOT/go_hive/scripts/check_parity.sh" 2>&1 | grep -oE 'missing [0-9]+|MISSING [0-9]+' | grep -oE '[0-9]+' | head -1)
parity_missing=${parity_missing:-0}
note="${1:-}"

line="$(date '+%Y-%m-%d %H:%M'),${routes_go},${routes_py},${tests},${tool_go},${tool_dirs},${parity_missing},${note}"
echo "$line"
echo "$line" >> "$ROOT/go_hive/ledger.csv"
