#!/bin/bash
# 看门狗（每 5 分钟采样 + 异常主动告警）：
#   - 采样健康、HEAD、脏工作树、并行 lane 数、最近门禁结论
#   - 异常时打印 ⚠️ ALERT 并触发 macOS 通知（同一告警 15 分钟内只响一次）
#   - 单行摘要打到 stdout（Hermes heartbeat 会送进会话），详细块追加到 WATCHDOG_REPORT.md
set -u

REPO=/Users/mac/hive
REPORT="$REPO/go_hive/WATCHDOG_REPORT.md"
STATE="$REPO/go_hive/.watchdog_state"
INTERVAL="${1:-300}"
ALERT_COOLDOWN="${ALERT_COOLDOWN:-900}"

[ -f "$REPORT" ] || printf '# Watchdog 汇报（每 5 分钟）\n\n' > "$REPORT"
[ -f "$STATE" ] || : > "$STATE"

state_get() { grep -m1 "^$1=" "$STATE" 2>/dev/null | cut -d= -f2-; }
state_set() {
  local key="$1" val="$2" tmp
  tmp=$(mktemp)
  grep -v "^$key=" "$STATE" > "$tmp" 2>/dev/null || true
  echo "$key=$val" >> "$tmp"
  mv "$tmp" "$STATE"
}

notify() {
  local title="$1" message="$2"
  if command -v osascript >/dev/null 2>&1; then
    osascript -e "display notification \"${message//\"/\\\"}\" with title \"${title//\"/\\\"}\"" >/dev/null 2>&1 || true
  fi
}

alert() {
  local key="$1" message="$2" now last
  now=$(date +%s)
  last=$(state_get "alert_$key")
  last=${last:-0}
  if [ $((now - last)) -lt "$ALERT_COOLDOWN" ]; then
    return 0
  fi
  state_set "alert_$key" "$now"
  echo "⚠️ ALERT [$key] $message"
  notify "Hive 看门狗" "$message"
  echo "- ⚠️ ALERT $message" >> "$REPORT"
}

while true; do
  cycle_start=$(date +%s)
  ts=$(date '+%Y-%m-%d %H:%M:%S')
  health=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:8788/api/health 2>/dev/null || echo "000")
  head=$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo "?")
  dirty=$(git -C "$REPO" status --short --untracked-files=no 2>/dev/null | wc -l | tr -d ' ')
  worktrees=$(git -C "$REPO" worktree list 2>/dev/null | wc -l | tr -d ' ')
  servers=$(pgrep -f "hive-embed serve" | wc -l | tr -d ' ')
  last_gate=$(ls -t /tmp/[knmprwstx]*_targets.log 2>/dev/null | head -1)
  gate_line="no-gate-log"
  if [ -n "$last_gate" ]; then
    gate_line=$(tail -1 "$last_gate" 2>/dev/null | cut -c1-60)
  fi
  ledger_age="?"
  if [ -f "$REPO/go_hive/TIME_EVENTS.csv" ]; then
    last_row=$(tail -1 "$REPO/go_hive/TIME_EVENTS.csv" | cut -d, -f1)
    if [ -n "$last_row" ]; then
      last_epoch=$(date -j -f "%Y-%m-%d %H:%M:%S" "$last_row" +%s 2>/dev/null || echo "")
      [ -n "$last_epoch" ] && ledger_age=$(( $(date +%s) - last_epoch ))
    fi
  fi

  summary="[watchdog $ts] health=$health HEAD=$head dirty=$dirty worktrees=$worktrees serve=$servers ledger_age=${ledger_age}s gate=\"$gate_line\""
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

  # ---- 异常检测（每条告警 15 分钟内只响一次）----
  [ "$health" != "200" ] && alert health "health=$health（期望 200，端口 8788）"
  [ "$servers" -eq 0 ] 2>/dev/null && alert serve "hive-embed serve 进程为 0"
  case "$gate_line" in *FAIL*|*MISSING*) alert gate "最近门禁异常：$gate_line" ;; esac
  [ "$dirty" -ge 8 ] 2>/dev/null && alert dirty "主线 tracked 改动 $dirty 个，可能混入未预期变更"
  if [ "$ledger_age" != "?" ] && [ "$ledger_age" -gt 900 ] 2>/dev/null; then
    alert ledger "时间台账超过 $((ledger_age / 60)) 分钟未更新（timegov 可能挂了）"
  fi
  # HEAD 超过 45 分钟未动且还有工作树 → 可能整队停滞
  last_head=$(state_get last_head)
  last_head_at=$(state_get last_head_at)
  now=$(date +%s)
  if [ "$head" != "$last_head" ]; then
    state_set last_head "$head"
    state_set last_head_at "$now"
  elif [ -n "$last_head_at" ] && [ $((now - last_head_at)) -gt 2700 ] && [ "$worktrees" -gt 0 ]; then
    alert stall "HEAD 已 $(( (now - last_head_at) / 60 )) 分钟未变（worktrees=$worktrees），可能停滞"
  fi

  # 固定节拍：本轮工作耗时从睡眠里扣掉，避免每轮漂移。
  now=$(date +%s)
  elapsed=$((now - cycle_start))
  remaining=$((INTERVAL - elapsed))
  [ "$remaining" -lt 1 ] && remaining=1
  sleep "$remaining"
done
