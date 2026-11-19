#!/bin/bash
# 看门狗（每 5 分钟主动汇报）：健康、HEAD、脏工作树、并行 lane 数、最近门禁结论。
# 单行摘要打到 stdout（Hermes 的 heartbeat 会按 5 分钟把它送进会话），
# 详细块追加到 go_hive/WATCHDOG_REPORT.md。
set -u

REPO=/Users/mac/hive
REPORT="$REPO/go_hive/WATCHDOG_REPORT.md"
INTERVAL="${1:-300}"

[ -f "$REPORT" ] || printf '# Watchdog 汇报（每 5 分钟）\n\n' > "$REPORT"

while true; do
  ts=$(date '+%Y-%m-%d %H:%M:%S')
  health=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:8788/api/health 2>/dev/null || echo "000")
  head=$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo "?")
  dirty=$(git -C "$REPO" status --short 2>/dev/null | grep -v '^??' | wc -l | tr -d ' ')
  worktrees=$(git -C "$REPO" worktree list 2>/dev/null | wc -l | tr -d ' ')
  servers=$(pgrep -f "hive-embed serve" | wc -l | tr -d ' ')
  lanes=$(pgrep -f "delegate_task\|subagent" >/dev/null 2>&1 && echo "?" || echo "-")
  last_gate=$(ls -t /tmp/[knm]_targets.log 2>/dev/null | head -1)
  gate_line="no-gate-log"
  if [ -n "$last_gate" ]; then
    gate_line=$(tail -1 "$last_gate" 2>/dev/null | cut -c1-60)
  fi

  summary="[watchdog $ts] health=$health HEAD=$head dirty=$dirty worktrees=$worktrees serve=$servers gate=\"$gate_line\""
  echo "$summary"

  {
    echo "## $ts"
    echo "- health: $health"
    echo "- HEAD: $head"
    echo "- dirty(tracked): $dirty"
    echo "- worktrees: $worktrees"
    echo "- hive serve 进程: $servers"
    echo "- 最近门禁: $gate_line"
    echo
  } >> "$REPORT"

  sleep "$INTERVAL"
done
