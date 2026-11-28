#!/usr/bin/env bash
# Serve the local go_hive UI on the LAN.
#
# The running service lives in /Users/mac/hive/go_hive with HIVE_HOME=.hive_go.
# This script does not stop a running service unless `restart` is explicitly
# requested; `start` refuses to take a port that is already in use.
#
# Usage:
#   scripts/serve_8787.sh status
#   scripts/serve_8787.sh start       # foreground, suitable for a supervisor
#   scripts/serve_8787.sh restart     # explicit stop + start
#
# Stable binary: /Users/mac/go_hive/hive-current/hive. Override with HIVE_BIN.
set -euo pipefail

ROOT="/Users/mac/hive/go_hive"
HIVE_BIN="${HIVE_BIN:-/Users/mac/go_hive/hive-current/hive}"
HOST="${HIVE_HOST:-0.0.0.0}"
PORT="${HIVE_PORT:-8787}"

cd "$ROOT"

listener_pid() {
  lsof -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null | head -n 1 || true
}

health() {
  curl -fsS -m 5 "http://127.0.0.1:${PORT}/api/health" >/dev/null 2>&1
}

start() {
  if [ ! -x "$HIVE_BIN" ]; then
    echo "serve_8787: binary not executable: $HIVE_BIN" >&2
    exit 1
  fi
  if [ -n "$(listener_pid)" ]; then
    echo "serve_8787: port ${PORT} is already in use (refusing to stop it)" >&2
    exit 1
  fi
  if [ -f "$ROOT/.hive_go/env" ]; then
    set +x
    # shellcheck disable=SC1091
    . "$ROOT/.hive_go/env"
  fi
  export HIVE_HOME="./.hive_go"
  exec "$HIVE_BIN" serve --host "$HOST" --port "$PORT"
}

case "${1:-status}" in
  status)
    pid="$(listener_pid)"
    if [ -z "$pid" ]; then
      echo "serve_8787: not listening on ${PORT}"
      exit 1
    fi
    if health; then
      echo "serve_8787: ok pid=${pid} host=${HOST} port=${PORT}"
      exit 0
    fi
    echo "serve_8787: listener pid=${pid} but /api/health is not ready" >&2
    exit 1
    ;;
  start)
    start
    ;;
  restart)
    pid="$(listener_pid)"
    if [ -n "$pid" ]; then
      echo "serve_8787: stopping pid=${pid} on ${PORT}"
      kill "$pid"
      for _ in $(seq 1 40); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.25
      done
      if kill -0 "$pid" 2>/dev/null; then
        echo "serve_8787: pid=${pid} did not exit" >&2
        exit 1
      fi
    fi
    start
    ;;
  *)
    echo "usage: $0 {status|start|restart}" >&2
    exit 2
    ;;
esac
