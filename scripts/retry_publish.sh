#!/bin/bash
# Retry publishing the go_hive mirror until it carries the current source HEAD.
# GitHub from this host intermittently times out; each attempt is bounded by
# publish_go_hive.sh and this loop just paces retries.
set -u
SRC=/Users/mac/hive
PUB=/Users/mac/.publish-go-hive/repo
LOG=/tmp/publish-retry.log
for attempt in $(seq 1 24); do
  want=$(git -C "$SRC" rev-parse --short HEAD)
  echo "[$(date '+%F %T')] attempt $attempt want=$want" >> "$LOG"
  bash "$SRC/go_hive/scripts/publish_go_hive.sh" "$want" >> "$LOG" 2>&1
  got=$(git -C "$PUB" log -1 --format=%s 2>/dev/null || true)
  case "$got" in
    *"$want"*) echo "[$(date '+%F %T')] published: $got" >> "$LOG"; exit 0 ;;
  esac
  sleep 300
done
echo "[$(date '+%F %T')] gave up after 24 attempts" >> "$LOG"
exit 1
