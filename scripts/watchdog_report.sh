#!/bin/bash
# 看门狗（每 5 分钟采样 + 异常主动告警）：
#   - 采样健康、HEAD、脏工作树、并行 lane 数、最近门禁结论
#   - 异常时打印 ⚠️ ALERT 并触发 macOS 通知（同一告警 15 分钟内只响一次）
#   - 单行摘要打到 stdout（Hermes heartbeat 会送进会话），详细块追加到 WATCHDOG_REPORT.md
#
# 可注入开关（默认值 = 生产口径，故障注入/测试用）：
#   WATCHDOG_REPO / WATCHDOG_REPORT / WATCHDOG_STATE   换成夹具仓库与隔离状态文件
#   WATCHDOG_PORT（默认 8788） / WATCHDOG_SERVE_PATTERN（默认 hive-embed serve）
#   WATCHDOG_ONCE=1   跑完一轮就退出（测试用）
set -u

REPO="${WATCHDOG_REPO:-/Users/mac/hive}"
REPORT="${WATCHDOG_REPORT:-$REPO/go_hive/WATCHDOG_REPORT.md}"
STATE="${WATCHDOG_STATE:-$REPO/go_hive/.watchdog_report_state}"
INTERVAL="${1:-300}"
ALERT_COOLDOWN="${ALERT_COOLDOWN:-900}"
PORT="${WATCHDOG_PORT:-8788}"
SERVE_PATTERN="${WATCHDOG_SERVE_PATTERN:-hive-embed serve}"
ONCE="${WATCHDOG_ONCE:-0}"

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
  # curl 在连接失败时仍会打印 %{http_code}=000 且退出非 0；不能再用 `|| echo 000`
  # （会产生 "000000"）。只保留一个兜底。
  health=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$PORT/api/health" 2>/dev/null || true)
  [ -n "$health" ] || health="000"
  head=$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo "?")
  dirty_total=$(git -C "$REPO" status --short --untracked-files=no 2>/dev/null | wc -l | tr -d ' ')
  # 只统计我们改动区（go_hive/），并排除遥测文件，避免台账/报告自身触发告警
  dirty=$(git -C "$REPO" status --short --untracked-files=no 2>/dev/null \
    | grep -E '^ M go_hive/' \
    | grep -vE 'go_hive/(TIME_EVENTS\.csv|WATCHDOG_REPORT\.md|QC_REPORT\.md|\.watchdog_state)' \
    | wc -l | tr -d ' ')
  worktrees=$(git -C "$REPO" worktree list 2>/dev/null | wc -l | tr -d ' ')
  servers=$(pgrep -f "$SERVE_PATTERN" | wc -l | tr -d ' ')
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

  # 2 小时 slot 版节奏：cadence.json 由 release_cadence_loop.sh / release_slot.sh 维护。
  # 08:00–24:00 内，最近一次成功 slot 超过 2h20m 未更新 → 告警。
  release_line="none"
  release_stale=0
  cadence="$REPO/go_hive/dist/slots/cadence.json"
  if [ -f "$cadence" ]; then
    release_line=$(python3 - "$cadence" <<'PY'
import json,sys,time
try:
    d=json.load(open(sys.argv[1])); age=int(time.time())-int(d.get("last_epoch",0))
    print(f"slot={d.get('last_slot','?')} status={d.get('last_status','?')} age={age}s")
except Exception:
    print("none")
PY
)
    age=$(printf '%s' "$release_line" | sed -n 's/.*age=\([0-9]*\)s.*/\1/p')
    [ -n "$age" ] && [ "$age" -gt 8400 ] && release_stale=1
  else
    hour_now=$((10#$(date +%H)))
    [ "$hour_now" -ge 10 ] && release_stale=1
  fi

  # 每 6 小时治理重算：governance.json 由 governance_recheck_loop.sh 维护。
  gov_age="?"
  gov_state="$REPO/go_hive/dist/slots/governance.json"
  if [ -f "$gov_state" ]; then
    gov_age=$(( $(date +%s) - $(stat -f %m "$gov_state") ))
  fi

  summary="[watchdog $ts] health=$health HEAD=$head dirty_hive=${dirty} total=${dirty_total} worktrees=$worktrees serve=$servers ledger_age=${ledger_age}s release=\"$release_line\" gov_age=${gov_age}s gate=\"$gate_line\""
  echo "$summary"

  {
    echo "## $ts"
    echo "- health: $health"
    echo "- HEAD: $head"
    echo "- dirty(tracked): hive=${dirty} total=${dirty_total}"
    echo "- worktrees: $worktrees"
    echo "- hive serve 进程: $servers"
    echo "- 最近门禁: $gate_line"
    echo
  } >> "$REPORT"

  # ---- 休眠感知：机器 sleep 会让本轮与上轮间隔远超 INTERVAL，此时
  # timegov/HEAD 的“陈旧”是休眠造成，不是故障；跳过本轮 stall/ledger 告警，
  # 并把 HEAD 计时重置到唤醒时刻。短睡眠循环保证唤醒后尽快补采。
  cycle_now=$(date +%s)
  prev_cycle=$(state_get last_cycle_at)
  sleep_gap=0
  if [ -n "$prev_cycle" ] && [ $((cycle_now - prev_cycle)) -gt $((INTERVAL * 2)) ]; then
    sleep_gap=1
    echo "[watchdog $ts] wake gap $(( (cycle_now - prev_cycle) / 60 )) min — stall/ledger alerts skipped once"
    state_set last_head_at "$cycle_now"
  fi
  state_set last_cycle_at "$cycle_now"

  # ---- 异常检测（每条告警 15 分钟内只响一次）----
  [ "$health" != "200" ] && alert health "health=${health}（期望 200，端口 ${PORT}）"
  [ "$servers" -eq 0 ] 2>/dev/null && alert serve "serve 进程（${SERVE_PATTERN}）为 0"
  case "$gate_line" in *FAIL*|*MISSING*) alert gate "最近门禁异常：$gate_line" ;; esac
  [ "$release_stale" = "1" ] && alert release "2 小时 slot 版未按时更新（${release_line}）；检查 release_cadence_loop.sh / dist/slots/cadence.log"
  # 升级兼容门禁失败（release_slot.sh 写入 compat_failed）：slot 版本不得视为可交付。
  case "$release_line" in *status=compat_failed*) alert compat "slot 升级兼容门禁失败（${release_line}）；见 go_hive/dist/slots/<slot>/compat.json" ;; esac
  if [ "$gov_age" != "?" ] && [ "$gov_age" -gt 24000 ] 2>/dev/null; then
    alert governance "治理重算超过 6h40m 未更新（${gov_age}s）；检查 governance_recheck_loop.sh / dist/slots/governance.log"
  fi
  [ "$dirty" -ge 3 ] 2>/dev/null && alert dirty "go_hive 区有 ${dirty} 个未提交改动（total=${dirty_total}），检查是否有 lane 写进主线"
  if [ "$sleep_gap" != 1 ] && [ "$ledger_age" != "?" ] && [ "$ledger_age" -gt 900 ] 2>/dev/null; then
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
    alert stall "HEAD 已 $(( (now - last_head_at) / 60 )) 分钟未变（worktrees=${worktrees}），可能停滞"
  fi

  # 故障注入/测试：跑完一轮即退出（生产实例不设该变量，保持常驻）。
  [ "$ONCE" = "1" ] && break

  # 固定节拍：本轮工作耗时从睡眠里扣掉，避免每轮漂移。
  # 15 秒粒度累加：系统休眠会冻结一次 300s 的 sleep，唤醒后要等整周期；
  # 短睡眠让下一轮采样在唤醒后尽快发生。
  now=$(date +%s)
  elapsed=$((now - cycle_start))
  remaining=$((INTERVAL - elapsed))
  [ "$remaining" -lt 1 ] && remaining=1
  while [ "$remaining" -gt 0 ]; do
    step=15
    [ "$remaining" -lt "$step" ] && step="$remaining"
    sleep "$step"
    remaining=$((remaining - step))
  done
done
