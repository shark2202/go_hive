#!/usr/bin/env bash
# e2e_browser_residuals.sh — R09/R10/R11 end-to-end probe.
#
# Builds the real browserbridge binary, starts it on throwaway ports with a
# fake extension speaking the real WebSocket protocol, then drives it through
# the control RPC (14831) to assert:
#
#   R09  two concurrent `_cdp` calls on one tab are serialized: the second
#        command frame only reaches the extension after the first reply is
#        released (the extension logs each command the moment it is read, and
#        handles frames concurrently so a held command cannot mask the next).
#   R10  a 17 MiB extension frame (above the old 16 MiB cap, below upstream's
#        50 MiB) is accepted and relayed, and the connection survives it.
#   R11  GET /status carries bridge_version (the binary's version),
#        runtime_version (null without an embedder value), bind_errors (empty
#        on a healthy bridge) and the five-key listening block.
#
# Ports are overridable: BRIDGE_WS_PORT / BRIDGE_HTTP_PORT / BRIDGE_CONTROL_PORT.
# Zero network access; everything is local and torn down on exit.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
go_hive=$(cd "$here/.." && pwd)
root=$(cd "$go_hive/.." && pwd)

WS_PORT=${BRIDGE_WS_PORT:-18929}
HTTP_PORT=${BRIDGE_HTTP_PORT:-18930}
CONTROL_PORT=${BRIDGE_CONTROL_PORT:-18931}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/browser-residuals-e2e.XXXXXX")
pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  rm -rf "$tmp"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  if [ -f "$tmp/fakeext.log" ]; then echo "--- fake extension log ---" >&2; cat "$tmp/fakeext.log" >&2; fi
  if [ -f "$tmp/bridge.log" ]; then echo "--- bridge log (tail) ---" >&2; tail -20 "$tmp/bridge.log" >&2; fi
  exit 1
}
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
version=$("$tmp/browserbridge" -version)

echo "== real bridge + fake extension =="
HIVE_HOME="$tmp/home" "$tmp/browserbridge" \
  -ws "127.0.0.1:$WS_PORT" -http "127.0.0.1:$HTTP_PORT" -control "127.0.0.1:$CONTROL_PORT" \
  -no-legacy -no-sweep -no-idle-exit >"$tmp/bridge.log" 2>&1 &
pids+=($!)
wait_http "http://127.0.0.1:$HTTP_PORT/health" "bridge"

uv run --offline --project "$root/tools" python "$here/e2e_browser_residuals_fakeext.py" \
  --serve --port "$WS_PORT" --events "$tmp/events.jsonl" --release "$tmp/release" \
  >"$tmp/fakeext.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 100); do
  if curl -fsS -m 1 "http://127.0.0.1:$HTTP_PORT/status" 2>/dev/null | grep -q '"connected":true'; then break; fi
  sleep 0.1
done
curl -fsS -m 1 "http://127.0.0.1:$HTTP_PORT/status" | grep -q '"connected":true' || fail "fake extension never attached"

echo "== R09/R10/R11 driver =="
uv run --offline --project "$root/tools" python "$here/e2e_browser_residuals_fakeext.py" \
  --drive --control-port "$CONTROL_PORT" --http-port "$HTTP_PORT" \
  --events "$tmp/events.jsonl" --release "$tmp/release" --expect-version "$version" \
  || fail "residual driver"

pass "all residual probes against the real bridge binary (version $version)"
