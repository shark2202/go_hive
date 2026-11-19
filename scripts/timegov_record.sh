#!/usr/bin/env bash
# 独立时间记录器：只采样事实，不解释、不修改任何代码。
# 每 INTERVAL 秒向 TIME_EVENTS.csv 追加一行（时间、HEAD、路由、测试、worktree 脏数）。
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
MOD="$ROOT/go_hive/go-hive"
OUT="$ROOT/go_hive/TIME_EVENTS.csv"
INTERVAL="${1:-300}"

[ -f "$OUT" ] || echo "ts,head,routes,tests,dirty_worktrees,open_lanes" > "$OUT"

while true; do
  cycle_start="$(date +%s)"
  ts="$(date '+%Y-%m-%d %H:%M:%S')"
  head="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null)"
  routes=$(grep -rhoE '"(GET|POST|PUT|DELETE|PATCH) /[^"]*"' "$MOD/internal/server" --include=*.go --exclude='*_test.go' 2>/dev/null | sort -u | wc -l | tr -d ' ')
  tests=$(grep -rhoE '^func Test[A-Za-z0-9_]+' "$MOD" --include=*_test.go 2>/dev/null | wc -l | tr -d ' ')
  dirty=0
  for w in "$ROOT"/.wt/*/; do
    [ -d "$w" ] || continue
    n=$(git -C "$w" status --short --untracked-files=no 2>/dev/null | wc -l | tr -d ' ')
    [ "$n" -gt 0 ] && dirty=$((dirty+1))
  done
  echo "$ts,$head,$routes,$tests,$dirty," >> "$OUT"
  # 固定节拍：采样耗时从睡眠里扣掉，否则 73 个 worktree 的扫描会让“5 分钟”漂成十几分钟。
  now="$(date +%s)"
  elapsed=$((now - cycle_start))
  remaining=$((INTERVAL - elapsed))
  [ "$remaining" -lt 1 ] && remaining=1
  sleep "$remaining"
done
