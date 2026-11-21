#!/usr/bin/env bash
# 每 6 小时治理重算调度器：00/06/12/18 点后触发 governance_recheck.py。
# 短睡眠避免宿主机 idle sleep 冻结整段 sleep。
set -u
REPO=/Users/mac/hive
STATE="$REPO/go_hive/dist/slots/governance.json"
LOG="$REPO/go_hive/dist/slots/governance.log"
mkdir -p "$(dirname "$STATE")"

bucket() { printf '%s-%02d' "$(date +%Y%m%d)" "$(( (10#$(date +%H)) / 6 * 6 ))"; }

last_bucket() {
  [ -f "$STATE" ] || { echo ""; return; }
  python3 - "$STATE" <<'PY'
import json,sys
try:
    at=json.load(open(sys.argv[1])).get("at","")
    print(at[:10].replace("-","") + "-" + f"{int(at[11:13])//6*6:02d}" if at else "")
except Exception:
    print("")
PY
}

while true; do
  h=$((10#$(date +%H))); m=$((10#$(date +%M)))
  if [ $((h % 6)) -eq 0 ] && [ "$m" -ge 5 ]; then
    b=$(bucket); last=$(last_bucket)
    if [ "$b" != "$last" ]; then
      echo "[gov $(date '+%F %T')] recheck $b (last=$last)" >>"$LOG"
      python3 "$REPO/go_hive/scripts/governance_recheck.py" >>"$LOG" 2>&1
      echo "[gov $(date '+%F %T')] rc=$?" >>"$LOG"
    fi
  fi
  sleep 15
done
