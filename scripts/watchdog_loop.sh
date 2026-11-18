#!/usr/bin/env bash
# 独立看门狗：只读巡检，不改代码、不跑门禁重活。
# 每 INTERVAL 秒记录一次状态；发现异常追加到 ALERTS.md 并在日志打 ALERT。
# 用法：bash go_hive/scripts/watchdog_loop.sh [interval_seconds]
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
MOD="$ROOT/go_hive/go-hive"
LOG="$ROOT/go_hive/watchdog.log"
ALERTS="$ROOT/go_hive/ALERTS.md"
INTERVAL="${1:-240}"
STATE="$ROOT/go_hive/.watchdog_state"
PREV_ROUTES=0; PREV_TESTS=0; PREV_HEAD=""

[ -f "$STATE" ] && . "$STATE" 2>/dev/null || true
[ -f "$ALERTS" ] || echo "# 看门狗告警" > "$ALERTS"

alert() {
  local kind="$1" msg="$2"
  printf -- "- %s [%s] %s\n" "$(date '+%m-%d %H:%M')" "$kind" "$msg" >> "$ALERTS"
  echo "ALERT [$kind] $msg"
}

while true; do
  ts="$(date '+%Y-%m-%d %H:%M')"
  head="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null)"
  routes=$(grep -rhoE '"(GET|POST|PUT|DELETE|PATCH) /[^"]*"' "$MOD/internal/server" --include=*.go --exclude='*_test.go' 2>/dev/null | sort -u | wc -l | tr -d ' ')
  tests=$(grep -rhoE '^func Test[A-Za-z0-9_]+' "$MOD" --include=*_test.go 2>/dev/null | wc -l | tr -d ' ')
  dirty=$(git -C "$ROOT" worktree list --porcelain 2>/dev/null | grep -c '^worktree ' )
  dirtywt=0
  for w in "$ROOT"/.wt/*/; do
    [ -d "$w/.git" ] || [ -f "$w/.git" ] || continue
    n=$(git -C "$w" status --short 2>/dev/null | wc -l | tr -d ' ')
    [ "$n" -gt 0 ] && dirtywt=$((dirtywt+1))
  done
  cgo=$(grep -rl 'import "C"' "$MOD" --include=*.go 2>/dev/null | wc -l | tr -d ' ')
  dist=no; [ -f "$MOD/frontend/dist/index.html" ] && dist=yes

  echo "$ts head=$head routes=$routes tests=$tests dirty_wt=$dirtywt cgo=$cgo dist=$dist" >> "$LOG"

  [ "$routes" -lt "$PREV_ROUTES" ] && alert "REGRESS" "路由数下降 $PREV_ROUTES → $routes"
  [ "$tests" -lt "$PREV_TESTS" ] && alert "REGRESS" "测试数下降 $PREV_TESTS → $tests"
  [ "$cgo" -gt 0 ] && alert "CGO" "出现 import \"C\" 的文件 $cgo 个"
  [ "$dirtywt" -ge 4 ] && alert "STALL" "$dirtywt 个 worktree 有未提交改动，可能卡住"

  PREV_ROUTES=$routes; PREV_TESTS=$tests; PREV_HEAD=$head
  printf 'PREV_ROUTES=%s\nPREV_TESTS=%s\nPREV_HEAD=%s\n' "$PREV_ROUTES" "$PREV_TESTS" "$PREV_HEAD" > "$STATE"
  sleep "$INTERVAL"
done
