#!/usr/bin/env bash
#
# Real-Chrome end-to-end verification for the Go browser host as shipped:
#
#   `hive serve`      starts and supervises `browserbridge`   (task: serve 生命周期)
#   `hive-browser`    drives it over POST /command            (task: CLI)
#   real Chrome       loads the bundled 1.7.4 extension       (task: 真机联调)
#
# What it does:
#   1. builds hive, hive-browser, browserbridge and chromeloader
#   2. uses a throwaway HIVE_HOME whose configuration.json turns the browser
#      feature on ({"gcu_enabled": true})
#   3. starts `hive serve` and asserts the bridge child is up, is a child of
#      serve, and answers /health
#   4. drives `hive-browser` with NO Chrome running and asserts the explicit
#      not-connected contract (exit 2, connected=false)
#   5. loads the bundled extension into real Chrome via chromeloader
#   6. drives the real page through `hive-browser` (open / page text /
#      evaluate / interact / snapshot / screenshot / tab list / tab close)
#   7. stops serve and asserts the bridge child is gone
#
# Requirements: macOS/Linux, curl, jq, go, a Chrome/Chromium binary, uv or
# python3 (fixture server). Nothing is mocked: a real Chrome, the real
# extension and the real bridge binary are used.
#
# Usage: go_hive/scripts/e2e_browser_host.sh [evidence-dir]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_HIVE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MODULE_DIR="$GO_HIVE_ROOT/go-hive"
BRIDGE_MODULE_DIR="$GO_HIVE_ROOT/browserbridge"
OUT="${1:-$(mktemp -d "${TMPDIR:-/tmp}/browser-host-e2e.XXXXXX")}"
EXT_DIR="$BRIDGE_MODULE_DIR/extension"

WS_PORT="${BH_E2E_WS_PORT:-14829}"
HTTP_PORT="${BH_E2E_HTTP_PORT:-14830}"
CONTROL_PORT="${BH_E2E_CONTROL_PORT:-14831}"
LEGACY_PORT="${BH_E2E_LEGACY_PORT:-9229}"
SERVE_PORT="${BH_E2E_SERVE_PORT:-8891}"
FIXTURE_PORT="${BH_E2E_FIXTURE_PORT:-18099}"
IDLE_WS_PORT="${BH_E2E_IDLE_WS_PORT:-14859}"
IDLE_HTTP_PORT="${BH_E2E_IDLE_HTTP_PORT:-14860}"
IDLE_CONTROL_PORT="${BH_E2E_IDLE_CONTROL_PORT:-14861}"
BASE="http://127.0.0.1:$HTTP_PORT"

HIVE_HOME="$OUT/hive-home"
FAILURES=0
STEP=0
SERVE_PID=""
BRIDGE_PID=""
LOADER_PID=""
FIXTURE_PID=""
PROFILE_DIR=""

have() { command -v "$1" >/dev/null 2>&1; }
log() { printf '%s\n' "$*"; }
step() { STEP=$((STEP + 1)); printf '\n### step %02d: %s\n' "$STEP" "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
check() { # description, then command already evaluated by the caller
  if [ "$1" = "0" ]; then printf '  ok  : %s\n' "$2"; else printf '  FAIL: %s\n' "$2"; FAILURES=$((FAILURES + 1)); fi
}

cleanup() {
  [ -n "$LOADER_PID" ] && kill "$LOADER_PID" 2>/dev/null || true
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null || true
  [ -n "$FIXTURE_PID" ] && kill "$FIXTURE_PID" 2>/dev/null || true
  sleep 1
  for pid in "$LOADER_PID" "$SERVE_PID" "$FIXTURE_PID"; do
    [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null || true
  done
  # Children can outlive their wrapper (chromeloader -> Chrome, serve -> bridge).
  for port in "$WS_PORT" "$HTTP_PORT" "$SERVE_PORT" "$FIXTURE_PORT"; do
    lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | xargs -r kill -9 2>/dev/null || true
  done
  if [ -n "$PROFILE_DIR" ]; then
    pkill -9 -f "user-data-dir=$PROFILE_DIR" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# ── preflight ───────────────────────────────────────────────────────────────
step "preflight"
have curl || fail "curl is required"
have jq || fail "jq is required"
have go || fail "go is required"
[ -f "$EXT_DIR/manifest.json" ] || fail "no extension manifest at $EXT_DIR"

CHROME_BIN="${BH_E2E_CHROME:-}"
if [ -z "$CHROME_BIN" ]; then
  for candidate in \
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
    "/Applications/Chromium.app/Contents/MacOS/Chromium" \
    "$(command -v google-chrome 2>/dev/null || true)" \
    "$(command -v chromium 2>/dev/null || true)"; do
    if [ -n "$candidate" ] && [ -x "$candidate" ]; then CHROME_BIN="$candidate"; break; fi
  done
fi
if [ -z "$CHROME_BIN" ]; then
  log "no Chrome/Chromium binary found: skipping the real-browser steps (BH_E2E_CHROME to override)"
  CHROME_BIN=""
fi

if have uv; then PY_RUNNER="uv run --no-project python"; else PY_RUNNER="python3"; fi

for port in "$WS_PORT" "$HTTP_PORT" "$CONTROL_PORT" "$LEGACY_PORT" "$SERVE_PORT" "$FIXTURE_PORT" \
            "$IDLE_WS_PORT" "$IDLE_HTTP_PORT" "$IDLE_CONTROL_PORT"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    fail "port $port is already in use (stop the other bridge/serve first)"
  fi
done
log "evidence dir: $OUT"

# ── build ───────────────────────────────────────────────────────────────────
step "build hive, hive-browser, browserbridge, chromeloader"
(cd "$MODULE_DIR" && go build -o "$OUT/hive" ./cmd/hive && go build -o "$OUT/hive-browser" ./cmd/hive-browser)
(cd "$BRIDGE_MODULE_DIR" && go build -o "$OUT/browserbridge" ./cmd/browserbridge && go build -o "$OUT/chromeloader" ./cmd/chromeloader)
log "hive:            $OUT/hive"
log "hive-browser:    $OUT/hive-browser"
log "browserbridge:   $OUT/browserbridge"

# ── fixtures ────────────────────────────────────────────────────────────────
step "write the fixture page and the throwaway HIVE_HOME"
mkdir -p "$OUT/web" "$HIVE_HOME"
cat > "$HIVE_HOME/configuration.json" <<'JSON'
{
  "gcu_enabled": true,
  "created_at": "2026-11-20T00:00:00+00:00"
}
JSON
cat > "$OUT/web/index.html" <<'HTML'
<!doctype html>
<html><head><meta charset="utf-8"><title>Hive Browser Host E2E</title></head>
<body>
  <h1 id="title">hive browser host</h1>
  <p id="para">hello from the fixture</p>
  <input id="name" type="text" placeholder="name">
  <button id="btn" onclick="document.getElementById('out').textContent='clicked'">Click me</button>
  <div id="out"></div>
</body></html>
HTML

( cd "$OUT/web" && $PY_RUNNER -m http.server "$FIXTURE_PORT" --bind 127.0.0.1 ) > "$OUT/fixture.log" 2>&1 &
FIXTURE_PID=$!
disown "$FIXTURE_PID" 2>/dev/null || true
for _ in $(seq 1 40); do
  curl -sf "http://127.0.0.1:$FIXTURE_PORT/index.html" > /dev/null && break
  sleep 0.25
done
curl -sf "http://127.0.0.1:$FIXTURE_PORT/index.html" > /dev/null || fail "fixture server did not start"

# ── hive serve starts the bridge ────────────────────────────────────────────
step "start \`hive serve\` and assert it brings the bridge up"
export HIVE_HOME
export HIVE_BROWSERBRIDGE_BIN="$OUT/browserbridge"
export HIVE_BROWSERBRIDGE_CONTROL="127.0.0.1:$CONTROL_PORT"
"$OUT/hive" serve --port "$SERVE_PORT" > "$OUT/serve.log" 2>&1 &
SERVE_PID=$!
disown "$SERVE_PID" 2>/dev/null || true

for _ in $(seq 1 60); do
  grep -q "Browser bridge: ready on 127.0.0.1:$HTTP_PORT" "$OUT/serve.log" && break
  kill -0 "$SERVE_PID" 2>/dev/null || break
  sleep 0.5
done
if grep -q "Browser bridge: ready on 127.0.0.1:$HTTP_PORT" "$OUT/serve.log"; then
  BRIDGE_PID="$(sed -n "s/.*Browser bridge: ready on 127.0.0.1:$HTTP_PORT (pid \([0-9]*\)).*/\1/p" "$OUT/serve.log" | tail -1)"
  check 0 "serve announced the bridge: $(grep 'Browser bridge: ready' "$OUT/serve.log" | tail -1)"
else
  check 1 "serve never announced a ready bridge (see $OUT/serve.log)"
fi
curl -sf --max-time 5 "$BASE/health" > "$OUT/bridge-health.json" \
  && check 0 "bridge /health: $(cat "$OUT/bridge-health.json")" \
  || check 1 "bridge /health unreachable"

# Control RPC 14831: serve holds one control client for its whole lifetime, so
# the bridge can distinguish "no browser work" from "runtime gone". The
# keepalive dials with a 1s backoff, so poll instead of racing the bind.
control_ok=1
for _ in $(seq 1 48); do
  if curl -sf --max-time 5 "$BASE/status" > "$OUT/bridge-status.json" \
     && jq -e ".control_rpc_clients >= 1" "$OUT/bridge-status.json" > /dev/null; then
    control_ok=0
    break
  fi
  sleep 0.25
done
if [ "$control_ok" = "0" ]; then
  check 0 "serve holds a control client (control_rpc_clients=$(jq -r '.control_rpc_clients' "$OUT/bridge-status.json"))"
else
  check 1 "no control client from serve after 12s: $(jq -c '.control_rpc_clients' "$OUT/bridge-status.json" 2>/dev/null)"
fi
jq -e ".listening.control_rpc == true" "$OUT/bridge-status.json" > /dev/null \
  && check 0 "control RPC is listening ($(jq -c '.listening' "$OUT/bridge-status.json"))" \
  || check 1 "control RPC is not listening: $(jq -c '.listening' "$OUT/bridge-status.json")"
jq -e ".runtime_alive == true" "$OUT/bridge-status.json" > /dev/null \
  && check 0 "bridge sees the runtime as alive (client_hello owner pid)" \
  || check 1 "runtime_alive is not true: $(jq -c '.runtime_alive' "$OUT/bridge-status.json")"
if [ -n "$BRIDGE_PID" ] && ps -p "$BRIDGE_PID" -o command= >/dev/null 2>&1; then
  command_line="$(ps -p "$BRIDGE_PID" -o command= | tr -d '\n')"
  parent_pid="$(ps -p "$BRIDGE_PID" -o ppid= | tr -d ' ')"
  log "  bridge child pid $BRIDGE_PID (parent $parent_pid): $command_line"
  [ "$parent_pid" = "$SERVE_PID" ] && check 0 "the bridge is a child of serve (ppid=$SERVE_PID)" \
    || check 1 "bridge ppid=$parent_pid, expected serve pid $SERVE_PID"
  case "$command_line" in
    *browserbridge*) check 0 "the child process is browserbridge" ;;
    *) check 1 "unexpected child command: $command_line" ;;
  esac
else
  check 1 "no live bridge child process"
fi
grep -q "Hive API server running" "$OUT/serve.log" || check 1 "serve did not report its own API"
curl -sf --max-time 5 "http://127.0.0.1:$SERVE_PORT/api/health" > "$OUT/serve-health.json" \
  && check 0 "serve /api/health: $(head -c 120 "$OUT/serve-health.json")" \
  || check 1 "serve /api/health unreachable"
log "  browser bridge child log: $HIVE_HOME/browserbridge.log"

# ── CLI: no Chrome yet ──────────────────────────────────────────────────────
step "hive-browser without Chrome: explicit not-connected contract (exit 2)"
set +e
"$OUT/hive-browser" status --json > "$OUT/cli-status-nochrome.json" 2> "$OUT/cli-status-nochrome.err"
status_code=$?
set -e
[ "$status_code" = "2" ] && check 0 "status exit code = 2 (not_connected)" \
  || check 1 "status exit code = $status_code, want 2"
jq -e '.connected == false' "$OUT/cli-status-nochrome.json" > /dev/null \
  && check 0 "status --json reports connected=false" \
  || check 1 "status --json did not report connected=false: $(cat "$OUT/cli-status-nochrome.json")"

set +e
"$OUT/hive-browser" setup --json > "$OUT/cli-setup-nochrome.json" 2>&1
setup_code=$?
set -e
[ "$setup_code" = "2" ] && check 0 "setup exit code = 2 (extension not connected)" \
  || check 1 "setup exit code = $setup_code, want 2"
jq -e '.install_url' "$OUT/cli-setup-nochrome.json" > /dev/null \
  && check 0 "setup --json carries the install steps" \
  || check 1 "setup --json missing install_url"

# ── real Chrome ─────────────────────────────────────────────────────────────
if [ -n "$CHROME_BIN" ]; then
  step "load the bundled extension into real Chrome ($CHROME_BIN)"
  PROFILE_DIR="$OUT/chrome-profile"
  rm -rf "$PROFILE_DIR"
  "$OUT/chromeloader" \
    -ext "$EXT_DIR" \
    -profile "$PROFILE_DIR" \
    -chrome "$CHROME_BIN" \
    -ready-file "$OUT/chrome-ready.json" \
    -timeout 30s \
    > "$OUT/chromeloader.log" 2>&1 &
  LOADER_PID=$!
  disown "$LOADER_PID" 2>/dev/null || true
  for _ in $(seq 1 120); do
    [ -s "$OUT/chrome-ready.json" ] && break
    kill -0 "$LOADER_PID" 2>/dev/null || break
    sleep 0.5
  done
  if [ -s "$OUT/chrome-ready.json" ]; then
    check 0 "chrome + extension ready: $(cat "$OUT/chrome-ready.json")"
  else
    check 1 "chrome/extension never became ready (see $OUT/chromeloader.log)"
  fi

  for _ in $(seq 1 60); do
    curl -sf "$BASE/status" | jq -e '.connected == true' > /dev/null 2>&1 && break
    sleep 0.5
  done
  curl -sf "$BASE/status" > "$OUT/bridge-status-connected.json" || true
  jq -e '.connected == true' "$OUT/bridge-status-connected.json" > /dev/null 2>&1 \
    && check 0 "the extension connected to the supervised bridge" \
    || check 1 "the extension never connected: $(head -c 200 "$OUT/bridge-status-connected.json" 2>/dev/null)"

  step "hive-browser against real Chrome"
  set +e
  "$OUT/hive-browser" status --json > "$OUT/cli-status-connected.json" 2> "$OUT/cli-status-connected.err"
  status_code=$?
  set -e
  [ "$status_code" = "0" ] && check 0 "status exit code = 0" || check 1 "status exit code = $status_code, want 0"
  jq -e '.connected == true' "$OUT/cli-status-connected.json" > /dev/null \
    && check 0 "status: $(jq -c '{connected, profile, connected_profiles: [.connected_profiles[].label]}' "$OUT/cli-status-connected.json")" \
    || check 1 "status did not report a connection"

  set +e
  "$OUT/hive-browser" open "http://127.0.0.1:$FIXTURE_PORT/index.html" --json > "$OUT/cli-open.json" 2> "$OUT/cli-open.err"
  open_code=$?
  set -e
  [ "$open_code" = "0" ] && check 0 "open exit code = 0" || check 1 "open exit code = $open_code ($(cat "$OUT/cli-open.err"))"
  TAB_ID="$(jq -r '.tabId // empty' "$OUT/cli-open.json" 2>/dev/null)"
  if [ -n "$TAB_ID" ]; then check 0 "open returned tabId=$TAB_ID url=$(jq -r '.url' "$OUT/cli-open.json")"
  else check 1 "open returned no tabId: $(cat "$OUT/cli-open.json")"; TAB_ID=""; fi

  # Synthetic input needs a visible renderer: a minimized or fully occluded
  # Chrome window makes the browser drop CDP mouse/key events, so the click
  # below would "succeed" without reaching the page. That is exactly what
  # POST /tabs/{id}/reveal exists for (activate + raise the window) — so this
  # doubles as real-Chrome coverage for the new route.
  if [ -n "$TAB_ID" ]; then
    if curl -sf --max-time 5 -X POST "$BASE/tabs/$TAB_ID/reveal" > "$OUT/reveal.json"; then
      check 0 "reveal raised the Chrome window: $(jq -c '{ok, revealed, prevState}' "$OUT/reveal.json")"
    else
      check 1 "reveal failed for tab $TAB_ID"
    fi
    sleep 0.5
  fi

  set +e
  "$OUT/hive-browser" page text '#title' --json > "$OUT/cli-text.json" 2> "$OUT/cli-text.err"
  text_code=$?
  set -e
  [ "$text_code" = "0" ] && check 0 "page text exit code = 0" || check 1 "page text exit code = $text_code ($(cat "$OUT/cli-text.err"))"
  jq -e '.text == "hive browser host"' "$OUT/cli-text.json" > /dev/null \
    && check 0 "page text read the real DOM: $(jq -c . "$OUT/cli-text.json")" \
    || check 1 "page text returned $(cat "$OUT/cli-text.json")"

  set +e
  "$OUT/hive-browser" evaluate --js 'document.title' --json > "$OUT/cli-title.json" 2>&1
  title_code=$?
  set -e
  [ "$title_code" = "0" ] && check 0 "evaluate exit code = 0" || check 1 "evaluate exit code = $title_code ($(cat "$OUT/cli-title.json"))"
  jq -e '.result == "Hive Browser Host E2E"' "$OUT/cli-title.json" > /dev/null \
    && check 0 "evaluate saw the real page title" \
    || check 1 "evaluate returned $(jq -c . "$OUT/cli-title.json")"

  set +e
  # A first CDP attach makes Chrome drop its "debugging this browser" infobar,
  # which shrinks the layout viewport ~36px mid-flight; a synthetic click that
  # raced that shift lands below the button. Retry the click (bounded) instead
  # of failing a real-browser run on a layout race — the assertion below still
  # requires the handler to have fired.
  click_code=1
  for attempt in 1 2 3; do
    "$OUT/hive-browser" interact --action left_click --selector '#btn' --json > "$OUT/cli-click.json" 2>&1
    click_code=$?
    [ "$click_code" = "0" ] || continue
    "$OUT/hive-browser" evaluate --js "document.getElementById('out').textContent" --json > "$OUT/cli-clicked.json" 2>&1
    jq -e '.result == "clicked"' "$OUT/cli-clicked.json" > /dev/null && break
    sleep 0.5
  done
  set -e
  [ -f "$OUT/cli-clicked.json" ] || printf '{"result":null}\n' > "$OUT/cli-clicked.json"
  [ "$click_code" = "0" ] && check 0 "interact --action left_click exit code = 0" || check 1 "click exit code = $click_code"
  jq -e '.result == "clicked"' "$OUT/cli-clicked.json" > /dev/null \
    && check 0 "the click ran the page handler (out=clicked)" \
    || check 1 "click had no effect: $(jq -c . "$OUT/cli-clicked.json")"

  set +e
  "$OUT/hive-browser" page snapshot --json > "$OUT/cli-snapshot.json" 2>&1
  snapshot_code=$?
  "$OUT/hive-browser" screenshot --intent "verify the fixture renders" --json > "$OUT/cli-screenshot.json" 2>&1
  screenshot_code=$?
  "$OUT/hive-browser" tab list --json > "$OUT/cli-tabs.json" 2>&1
  tabs_code=$?
  set -e
  [ "$snapshot_code" = "0" ] && check 0 "page snapshot exit code = 0 ($(jq -c '{nodes}' "$OUT/cli-snapshot.json"))" \
    || check 1 "page snapshot exit code = $snapshot_code"
  [ "$screenshot_code" = "0" ] && check 0 "screenshot exit code = 0 ($(jq -c '{saved_to, mimeType}' "$OUT/cli-screenshot.json"))" \
    || check 1 "screenshot exit code = $screenshot_code"
  [ "$tabs_code" = "0" ] && check 0 "tab list exit code = 0 ($(jq -c '{total}' "$OUT/cli-tabs.json"))" \
    || check 1 "tab list exit code = $tabs_code"

  if [ -n "$TAB_ID" ]; then
    "$OUT/hive-browser" tab close "$TAB_ID" --json > "$OUT/cli-tab-close.json" 2>&1 || true
    set +e
    "$OUT/hive-browser" page text '#title' --json > "$OUT/cli-after-close.json" 2> "$OUT/cli-after-close.err"
    after_code=$?
    set -e
    [ "$after_code" = "3" ] && check 0 "after tab close the CLI says 'Browser not started' (exit 3)" \
      || check 1 "after tab close exit code = $after_code, want 3"
  fi
  log "browser sessions state: $(cat "$HIVE_HOME/browser_cli_state.json" 2>/dev/null || echo '(none)')"
else
  step "real Chrome steps skipped (no Chrome binary)"
fi

# ── shutdown ────────────────────────────────────────────────────────────────
step "stop serve and assert the bridge child is gone"
kill -TERM "$SERVE_PID" 2>/dev/null || true
for _ in $(seq 1 40); do
  kill -0 "$SERVE_PID" 2>/dev/null || break
  sleep 0.25
done
if kill -0 "$SERVE_PID" 2>/dev/null; then check 1 "serve did not exit on SIGTERM"; else check 0 "serve exited on SIGTERM"; fi
if [ -n "$BRIDGE_PID" ]; then
  for _ in $(seq 1 40); do
    ps -p "$BRIDGE_PID" -o pid= >/dev/null 2>&1 || break
    sleep 0.25
  done
  if ps -p "$BRIDGE_PID" -o pid= >/dev/null 2>&1; then
    check 1 "bridge child $BRIDGE_PID survived serve shutdown"
  else
    check 0 "bridge child $BRIDGE_PID was stopped with serve"
  fi
fi
if curl -sf --max-time 3 "$BASE/health" > /dev/null 2>&1; then
  check 1 "something still answers $BASE/health"
else
  check 0 "nothing answers $BASE/health after shutdown"
fi

# ── idle exit ───────────────────────────────────────────────────────────────
step "a bare bridge with no control client and no desktop parent exits on idle"
# The idle watchdog is the only path (besides SIGTERM) that takes the bridge
# down: zero live control clients AND no HIVE_DESKTOP_PARENT_PID holder.
env -u HIVE_DESKTOP_PARENT_PID "$OUT/browserbridge" \
  -ws "127.0.0.1:$IDLE_WS_PORT" -http "127.0.0.1:$IDLE_HTTP_PORT" \
  -control "127.0.0.1:$IDLE_CONTROL_PORT" -no-legacy -no-sweep \
  -idle-grace 2s -idle-check 200ms > "$OUT/idle-bridge.log" 2>&1 &
IDLE_PID=$!
disown "$IDLE_PID" 2>/dev/null || true
for _ in $(seq 1 40); do
  curl -sf --max-time 1 "http://127.0.0.1:$IDLE_HTTP_PORT/health" > /dev/null && break
  sleep 0.25
done
curl -sf --max-time 2 "http://127.0.0.1:$IDLE_HTTP_PORT/status" > "$OUT/idle-status.json" 2>/dev/null \
  && jq -e ".control_rpc_clients == 0" "$OUT/idle-status.json" > /dev/null \
  && check 0 "bare bridge has zero control clients before the grace" \
  || check 1 "bare bridge /status did not report zero control clients"
set +e
IDLE_EXITED=0
for _ in $(seq 1 80); do
  if ! ps -p "$IDLE_PID" -o pid= >/dev/null 2>&1; then IDLE_EXITED=1; break; fi
  sleep 0.25
done
if [ "$IDLE_EXITED" = "1" ]; then
  wait "$IDLE_PID"
  idle_code=$?
else
  kill -TERM "$IDLE_PID" 2>/dev/null || true
  wait "$IDLE_PID" 2>/dev/null
  idle_code="alive"
fi
set -e
[ "$IDLE_EXITED" = "1" ] && check 0 "bare bridge exited within $(awk 'BEGIN{print 80*0.25}')s of the idle grace" \
  || check 1 "bare bridge did not idle-exit"
[ "$idle_code" = "0" ] && check 0 "idle exit code was 0 (intentional: the supervisor will not respawn)" \
  || check 1 "idle exit code = $idle_code, want 0"
grep -q "no control RPC clients and no desktop" "$OUT/idle-bridge.log" \
  && check 0 "idle watchdog logged its reason" \
  || check 1 "idle watchdog never logged; see $OUT/idle-bridge.log"

step "summary"
log "evidence: $OUT"
if [ "$FAILURES" != "0" ]; then
  log "FAILED: $FAILURES check(s) failed"
  exit 1
fi
log "PASS: browser host end-to-end"
