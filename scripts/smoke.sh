#!/usr/bin/env bash
# G3 集成冒烟：真实二进制 + 真实 HTTP，不 mock。
# 用法：bash go_hive/scripts/smoke.sh [port]
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
MOD="$ROOT/go_hive/go-hive"
PORT="${1:-8793}"
HOME_DIR="$(mktemp -d /tmp/hive_smoke.XXXXXX)"
BIN="$HOME_DIR/hive"
LOG="$HOME_DIR/server.log"
FAIL=0

cleanup() { [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null; wait "${PID:-}" 2>/dev/null; }
trap cleanup EXIT

cd "$MOD"
if ! CGO_ENABLED=0 go build -o "$BIN" ./cmd/hive; then
  echo "BUILD FAILED"; exit 1
fi

HIVE_HOME="$HOME_DIR/home" "$BIN" serve --port "$PORT" >"$LOG" 2>&1 &
PID=$!

for _ in $(seq 1 50); do
  curl -fsS "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1 && break
  sleep 0.2
done

probe() { # name expected code
  local name="$1" expect="$2" code="$3"
  if [ "$code" = "$expect" ]; then printf "  PASS  %-28s %s\n" "$name" "$code"
  else printf "  FAIL  %-28s got %s want %s\n" "$name" "$code" "$expect"; FAIL=$((FAIL+1)); fi
}

code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

probe "health" 200 "$(code "http://127.0.0.1:$PORT/api/health")"
probe "dashboard /" 200 "$(code "http://127.0.0.1:$PORT/")"
probe "spa deep route" 200 "$(code "http://127.0.0.1:$PORT/colonies/demo")"
probe "credentials specs" 200 "$(code "http://127.0.0.1:$PORT/api/credentials/specs")"
probe "queen profiles" 200 "$(code "http://127.0.0.1:$PORT/api/queen/profiles")"
probe "prompts" 200 "$(code "http://127.0.0.1:$PORT/api/prompts")"
probe "mcp servers" 200 "$(code "http://127.0.0.1:$PORT/api/mcp/servers")"
probe "models overview" 200 "$(code "http://127.0.0.1:$PORT/api/models/overview")"

SID=$(curl -s -X POST "http://127.0.0.1:$PORT/api/sessions" -H 'Content-Type: application/json' -d '{}' \
  | sed -n 's/.*"session_id":"\([^"]*\)".*/\1/p')
if [ -n "$SID" ]; then printf "  PASS  %-28s 201 (sid=%s)\n" "create session" "${SID:0:18}…"
else printf "  FAIL  %-28s no session_id\n" "create session"; FAIL=$((FAIL+1)); fi

probe "chat without key" 503 "$(code -X POST "http://127.0.0.1:$PORT/api/sessions/$SID/chat" \
  -H 'Content-Type: application/json' -d '{"message":"hi"}')"
probe "unknown session" 404 "$(code "http://127.0.0.1:$PORT/api/sessions/session_absent/stats")"
probe "workers list" 200 "$(code "http://127.0.0.1:$PORT/api/sessions/$SID/workers")"
probe "events SSE" 200 "$(curl -s -o /dev/null -w '%{http_code}' --max-time 1 \
  "http://127.0.0.1:$PORT/api/sessions/$SID/events")"

echo
if [ "$FAIL" -eq 0 ]; then echo "SMOKE: PASS ($PORT)"; else echo "SMOKE: $FAIL FAILED"; fi
echo "server log: $LOG"
exit "$FAIL"
