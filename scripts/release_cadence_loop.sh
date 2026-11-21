#!/usr/bin/env bash
# 2 小时 slot 版调度器：在 08/10/.../22 点的 slot 窗口内各触发一次
# scripts/release_slot.sh；已做过的 slot 不重复。短睡眠循环避免宿主机
# idle sleep 冻结一整个长 sleep。
set -u
REPO=/Users/mac/hive
SLOTS="$REPO/go_hive/dist/slots"
CADENCE="$SLOTS/cadence.json"
LOG="$SLOTS/cadence.log"

mkdir -p "$SLOTS"

slot_now() {
  local h; h=$(date +%H); h=$((10#$h / 2 * 2))
  printf '%s-%02d' "$(date +%Y%m%d)" "$h"
}

current_last() {
  [ -f "$CADENCE" ] || { echo ""; return; }
  python3 - "$CADENCE" <<'PY'
import json,sys
try: print(json.load(open(sys.argv[1])).get("last_slot",""))
except Exception: print("")
PY
}

while true; do
  hour=$((10#$(date +%H)))
  if [ "$hour" -ge 8 ] && [ "$hour" -lt 24 ]; then
    slot=$(slot_now)
    last=$(current_last)
    # 每个 slot 在整点后 2 分钟起可构建；整点前不抢跑
    minute=$((10#$(date +%M)))
    if [ "$slot" != "$last" ] && [ "$minute" -ge 2 ]; then
      echo "[cadence $(date '+%F %T')] trigger $slot (last=$last)" >>"$LOG"
      bash "$REPO/go_hive/scripts/release_slot.sh" "$slot" >>"$LOG" 2>&1
      echo "[cadence $(date '+%F %T')] rc=$? slot=$slot" >>"$LOG"
    fi
  fi
  # 15s 粒度，最多睡 30s；休眠唤醒后尽快补采
  sleep 15
done
