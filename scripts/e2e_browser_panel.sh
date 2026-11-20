#!/usr/bin/env bash
# e2e_browser_panel.sh — R03/R04/R05 end-to-end probe.
#
# Builds the real browserbridge + hive + hive-browser binaries, starts a bridge
# on throwaway ports with a fake extension speaking the real WebSocket protocol,
# starts a real `hive serve` pointed at that bridge, and asserts:
#
#   R03  GET /api/browser/status reports the running bridge (connected/healthy
#        with an extension attached) instead of a hardcoded offline.
#   R04  GET /api/browser/contexts and /api/browser/tabs return the upstream
#        Python bodies ({available,count,…} / isHiveGroup enrichment / 200
#        available:false when no bridge), and POST /contexts/close keeps the
#        upstream {closed:[…]} / 400 contract.
#   R05  CLI exit codes: screenshot without --intent reaches the bridge (0 when
#        connected, 2 when there is no bridge — never 6), `help nosuchcmd` is 1,
#        and a leading common flag (`--json status`) parses.
#
# Ports are overridable: BRIDGE_WS_PORT / BRIDGE_HTTP_PORT / BRIDGE_CONTROL_PORT
# / SERVER_PORT. Zero network access; everything is local and torn down on exit.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
go_hive=$(cd "$here/.." && pwd)
root=$(cd "$go_hive/.." && pwd)

WS_PORT=${BRIDGE_WS_PORT:-18829}
HTTP_PORT=${BRIDGE_HTTP_PORT:-18830}
CONTROL_PORT=${BRIDGE_CONTROL_PORT:-18831}
SERVER_PORT=${SERVER_PORT:-18788}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/browser-panel-e2e.XXXXXX")
pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  rm -rf "$tmp"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }

wait_http() {
  local url=$1 what=$2 tries=${3:-100}
  for _ in $(seq 1 "$tries"); do
    if curl -fsS -m 1 "$url" >/dev/null 2>&1; then return 0; fi
    sleep 0.1
  done
  fail "$what did not answer at $url"
}

echo "== build =="
(cd "$go_hive/browserbridge" && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build -o "$tmp/browserbridge" ./cmd/browserbridge) || fail "browserbridge build"
(cd "$go_hive/go-hive" && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build -o "$tmp/hive" ./cmd/hive \
  && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build -o "$tmp/hive-browser" ./cmd/hive-browser) || fail "hive build"

echo "== real bridge + fake extension =="
mkdir -p "$tmp/bridge-home" "$tmp/serve-home" "$tmp/cli-home"
HIVE_HOME="$tmp/bridge-home" "$tmp/browserbridge" \
  -ws "127.0.0.1:$WS_PORT" -http "127.0.0.1:$HTTP_PORT" -control "127.0.0.1:$CONTROL_PORT" \
  -no-legacy -no-sweep -no-idle-exit >"$tmp/bridge.log" 2>&1 &
pids+=($!)
wait_http "http://127.0.0.1:$HTTP_PORT/health" "bridge"

uv run --offline --project "$root/tools" python "$here/e2e_browser_panel_fakeext.py" --port "$WS_PORT" \
  >"$tmp/fakeext.log" 2>&1 &
pids+=($!)

for _ in $(seq 1 100); do
  if curl -fsS -m 1 "http://127.0.0.1:$HTTP_PORT/status" | grep -q '"connected":true'; then break; fi
  sleep 0.1
done
curl -fsS -m 1 "http://127.0.0.1:$HTTP_PORT/status" | grep -q '"connected":true' || fail "fake extension never attached"

created=$(uv run --offline --project "$root/tools" python "$here/e2e_browser_panel_fakeext.py" \
  --control-port "$CONTROL_PORT" --create-context e2e-profile)
echo "$created" | grep -q '"ok": true' || fail "create_context failed: $created"

echo "== real server pointed at the test bridge =="
HIVE_HOME="$tmp/serve-home" HIVE_BROWSERBRIDGE_HTTP="127.0.0.1:$HTTP_PORT" HIVE_BROWSER_BRIDGE=0 \
  "$tmp/hive" serve --host 127.0.0.1 --port "$SERVER_PORT" >"$tmp/serve.log" 2>&1 &
pids+=($!)
wait_http "http://127.0.0.1:$SERVER_PORT/api/health" "hive serve"
base="http://127.0.0.1:$SERVER_PORT"

echo "== R03: status reports the running bridge =="
status_body=$(curl -fsS -m 5 "$base/api/browser/status")
python3 - "$status_body" <<'PY' || fail "R03 status shape"
import json, sys
body = json.loads(sys.argv[1])
assert body["bridge"] is True, body
assert body["connected"] is True, body
assert body["health"] == "healthy", body
assert body["last_pong_age_ms"] is not None, body
PY
pass "GET /api/browser/status -> bridge:true connected:true health:healthy"

echo "== R04: contexts/tabs upstream bodies =="
contexts_body=$(curl -fsS -m 5 "$base/api/browser/contexts")
python3 - "$contexts_body" <<'PY' || fail "R04 contexts shape"
import json, sys
body = json.loads(sys.argv[1])
assert body["available"] is True, body
assert body["count"] == 1, body
row = body["contexts"][0]
assert row["profile"] == "e2e-profile", row
assert row["group_id"] == 777, row
assert row["name"] == "e2e-profile", row
assert row["tab_count"] == 1, row
assert row["active_tab_id"] == 101, row
PY
pass "GET /api/browser/contexts -> {available,count,contexts[{profile,group_id,name,tab_count,active_tab_id}]}"

tabs_body=$(curl -fsS -m 5 "$base/api/browser/tabs")
python3 - "$tabs_body" <<'PY' || fail "R04 tabs shape"
import json, sys
body = json.loads(sys.argv[1])
assert body["available"] is True, body
assert body["count"] == 2 and body["live_count"] == 2 and body["discarded_count"] == 0, body
assert body["hive_group_count"] == 1, body
rows = {row["id"]: row for row in body["tabs"]}
hive = rows[101]
assert hive["isHiveGroup"] is True and hive["groupId"] == 777, hive
assert hive["windowId"] is None and hive["discarded"] is False, hive
loose = rows[102]
assert loose["isHiveGroup"] is False and loose["groupId"] is None, loose
for key in ("id", "windowId", "groupId", "isHiveGroup", "url", "title", "discarded", "active", "audible", "pinned"):
    assert key in loose, (key, loose)
PY
pass "GET /api/browser/tabs -> {available,count,live_count,discarded_count,hive_group_count,tabs[…isHiveGroup]}"

close_status=$(curl -sS -m 5 -o "$tmp/close.json" -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{}' "$base/api/browser/contexts/close")
[ "$close_status" = "400" ] || fail "close without profile returned $close_status"
grep -q 'profile (or all=true) is required' "$tmp/close.json" || fail "close 400 body: $(cat "$tmp/close.json")"
pass "POST /api/browser/contexts/close {} -> 400 {\"error\": \"profile (or all=true) is required\"}"

echo "== R05: CLI exit codes against the live test bridge =="
cli() { HIVE_HOME="$tmp/cli-home" "$tmp/hive-browser" "$@"; }

cli screenshot --tab 101 --json --bridge "http://127.0.0.1:$HTTP_PORT" >"$tmp/screenshot.json" || fail "screenshot without --intent exited $?"
pass "hive-browser screenshot (no --intent) -> 0"

set +e
cli help nosuchcmd >/dev/null 2>&1
code=$?
set -e
[ "$code" -eq 1 ] || fail "help nosuchcmd exited $code, want 1"
pass "hive-browser help nosuchcmd -> 1"

set +e
cli --json status --bridge "http://127.0.0.1:$HTTP_PORT" >"$tmp/leading.json" 2>"$tmp/leading.err"
code=$?
set -e
[ "$code" -eq 0 ] || fail "--json status exited $code, want 0 (stderr: $(cat "$tmp/leading.err"))"
grep -q 'unrecognized arguments' "$tmp/leading.err" && fail "--json status was rejected: $(cat "$tmp/leading.err")"
pass "hive-browser --json status -> 0 (leading common flag accepted)"

dead=$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)
set +e
cli screenshot --tab 101 --json --bridge "http://127.0.0.1:$dead" >/dev/null 2>&1
code=$?
set -e
[ "$code" -eq 2 ] || fail "screenshot without a bridge exited $code, want 2 (not_connected)"
pass "hive-browser screenshot with no bridge -> 2 (never 6)"

echo "ALL PASS"
if [ "${E2E_DUMP:-}" = "1" ]; then
  echo "--- status: $status_body"
  echo "--- contexts: $contexts_body"
  echo "--- tabs: $tabs_body"
fi
