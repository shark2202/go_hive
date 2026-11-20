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
  # Verify against the REMOTE, not the local mirror: a local commit alone does
  # not mean the push landed (GitHub times out intermittently here).
  remote_sha=$(GIT_TERMINAL_PROMPT=0 git -C "$PUB" -c http.version=HTTP/1.1 ls-remote origin main 2>/dev/null | awk '{print $1}')
  local_sha=$(git -C "$PUB" rev-parse HEAD 2>/dev/null || true)
  if [ -n "$remote_sha" ] && [ "$remote_sha" = "$local_sha" ]; then
    echo "[$(date '+%F %T')] published to remote: $local_sha" >> "$LOG"
    exit 0
  fi
  echo "[$(date '+%F %T')] push not visible on remote (local=$local_sha remote=$remote_sha)" >> "$LOG"
  sleep 300
done
echo "[$(date '+%F %T')] gave up after 24 attempts" >> "$LOG"
exit 1
