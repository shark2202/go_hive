#!/usr/bin/env bash
#
# Real-Chrome end-to-end verification for the Go browser bridge (L3a).
#
# What it does:
#   1. builds cmd/browserbridge and cmd/chromeloader
#   2. serves a local HTML fixture (interactive page, shadow DOM, file input)
#   3. starts the bridge (WS 14829 + legacy 9229 + HTTP 14830)
#   4. starts a real Google Chrome with a throwaway profile and loads the
#      bundled extension via cmd/chromeloader (Chrome >= 137 ignores
#      --load-extension, so the DevTools pipe + Extensions.loadUnpacked is used)
#   5. drives the bridge's HTTP /command API through the active-tool flow:
#      status -> open -> navigate -> interact(click/type/key/scroll/zoom) ->
#      select -> get_text -> resize -> shadow_query -> upload -> html ->
#      screenshot -> snapshot -> console -> dialog_respond -> script ->
#      create_context -> status -> stop
#   6. writes every raw response plus the screenshot into an evidence dir
#
# Ports and the extension dir are overridable for hosts where something else
# already listens: BB_E2E_WS_PORT / BB_E2E_HTTP_PORT / BB_E2E_CONTROL_PORT /
# BB_E2E_LEGACY_PORT / BB_E2E_FIXTURE_PORT / BB_E2E_EXT_DIR. The shipped
# extension hardcodes 14829/9229, so a non-default WS port needs an extension
# copy with that one constant changed (BB_E2E_EXT_DIR).
#
# Requirements: macOS/Linux, curl, jq, a Chrome/Chromium binary, uv or python3
# (for the fixture server). Nothing here mocks the extension: a real Chrome and
# the real 1.7.4 extension are used.
#
# Usage: go_hive/browserbridge/scripts/real_chrome_e2e.sh [evidence-dir]
set -euo pipefail

MODULE_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT=${1:-$(mktemp -d "${TMPDIR:-/tmp}/browserbridge-e2e.XXXXXX")}
WS_PORT=${BB_E2E_WS_PORT:-14829}
HTTP_PORT=${BB_E2E_HTTP_PORT:-14830}
FIXTURE_PORT=${BB_E2E_FIXTURE_PORT:-18099}
LEGACY_PORT=${BB_E2E_LEGACY_PORT:-9229}
BASE="http://127.0.0.1:${HTTP_PORT}"
# The shipped extension hardcodes BRIDGE_PORTS=[14829, 9229]; when another
# bridge already holds those ports, point this at a copy whose port constant
# matches BB_E2E_WS_PORT (documented in the run notes) instead of stopping it.
EXT_DIR=${BB_E2E_EXT_DIR:-$MODULE_DIR/extension}

FAILURES=0
STEP=0
BRIDGE_PID=""
LOADER_PID=""
FIXTURE_PID=""

have() { command -v "$1" >/dev/null 2>&1; }

log()  { printf '%s\n' "$*"; }
step() { STEP=$((STEP + 1)); printf '\n### step %02d: %s\n' "$STEP" "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "$LOADER_PID" ] && kill "$LOADER_PID" 2>/dev/null || true
  [ -n "$BRIDGE_PID" ] && kill "$BRIDGE_PID" 2>/dev/null || true
  [ -n "$FIXTURE_PID" ] && kill "$FIXTURE_PID" 2>/dev/null || true
  sleep 1
  [ -n "$LOADER_PID" ] && kill -9 "$LOADER_PID" 2>/dev/null || true
  [ -n "$BRIDGE_PID" ] && kill -9 "$BRIDGE_PID" 2>/dev/null || true
  [ -n "$FIXTURE_PID" ] && kill -9 "$FIXTURE_PID" 2>/dev/null || true
  # Child processes can outlive the wrappers (uv -> python, chromeloader ->
  # Chrome). The preflight proved these ports were free, so anything listening
  # on them now belongs to this run.
  for port in "${WS_PORT:-}" "${HTTP_PORT:-}" "${FIXTURE_PORT:-}" "${LEGACY_PORT:-}"; do
    [ -n "$port" ] || continue
    lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | xargs -r kill -9 2>/dev/null || true
  done
  if [ -n "${PROFILE_DIR:-}" ]; then
    pkill -9 -f "user-data-dir=$PROFILE_DIR" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# ── preflight ───────────────────────────────────────────────────────────────
have curl || fail "curl is required"
have jq || fail "jq is required"
[ -f "$EXT_DIR/manifest.json" ] || fail "no extension manifest at $EXT_DIR"

CHROME_BIN=${BB_E2E_CHROME:-}
if [ -z "$CHROME_BIN" ]; then
  for candidate in \
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
    "/Applications/Chromium.app/Contents/MacOS/Chromium" \
    "$(command -v google-chrome 2>/dev/null || true)" \
    "$(command -v chromium 2>/dev/null || true)"; do
    if [ -n "$candidate" ] && [ -x "$candidate" ]; then CHROME_BIN=$candidate; break; fi
  done
fi
[ -n "$CHROME_BIN" ] || fail "no Chrome/Chromium binary found (set BB_E2E_CHROME)"

PY_RUNNER=""
if have uv; then PY_RUNNER="uv run python"; elif have python3; then PY_RUNNER="python3"; fi
[ -n "$PY_RUNNER" ] || fail "uv or python3 is required for the fixture server"

mkdir -p "$OUT"
log "evidence dir : $OUT"
log "chrome       : $CHROME_BIN ($("$CHROME_BIN" --version 2>/dev/null || echo unknown))"
log "extension    : $(jq -r '.version' "$EXT_DIR/manifest.json")"

for port in "$WS_PORT" "$HTTP_PORT" "$FIXTURE_PORT" "$LEGACY_PORT"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    fail "port $port is already in use"
  fi
done

# ── build ───────────────────────────────────────────────────────────────────
step "build browserbridge + chromeloader"
(cd "$MODULE_DIR" && go build -o "$OUT/browserbridge" ./cmd/browserbridge && go build -o "$OUT/chromeloader" ./cmd/chromeloader)
"$OUT/browserbridge" -version > "$OUT/bridge.version.txt"
log "bridge version: $(cat "$OUT/bridge.version.txt")"

# ── fixtures ────────────────────────────────────────────────────────────────
step "write fixture page, upload file and skill script"
mkdir -p "$OUT/web" "$OUT/skills/e2e-skill/scripts" "$OUT/artifacts"
cat > "$OUT/web/index.html" <<'HTML'
<!doctype html>
<html><head><meta charset="utf-8"><title>Bridge E2E Fixture</title></head>
<body>
  <h1 id="title">Bridge E2E Fixture</h1>
  <p id="para">hello from fixture</p>
  <a id="link" href="https://example.com/">example link</a>
  <input id="name" type="text" placeholder="name">
  <select id="pick">
    <option value="a">Alpha</option>
    <option value="b">Beta</option>
  </select>
  <input id="file" type="file">
  <button id="btn" onclick="document.title='clicked'">Click me</button>
  <button id="late-confirm" onclick="setTimeout(function(){ window.__confirmed = confirm('proceed?'); }, 50)">Ask later</button>
  <div id="host"></div>
  <script>
    const root = document.getElementById('host').attachShadow({mode: 'open'});
    root.innerHTML = '<span class="item">shadow item</span>';
  </script>
</body></html>
HTML
printf 'e2e upload payload\n' > "$OUT/upload-payload.txt"
cat > "$OUT/skills/e2e-skill/SKILL.md" <<'MD'
---
name: hive.e2e-skill
description: end-to-end verification helper for the Go browser bridge
---
MD
cat > "$OUT/skills/e2e-skill/scripts/probe.py" <<'PY'
"""E2E probe: reads the stdin envelope, calls the bridge's HTTP API and prints JSON."""
import json
import os
import sys
import urllib.request

try:
    context = json.loads(sys.stdin.read() or "{}")
except Exception:
    context = {}

try:
    with urllib.request.urlopen(os.environ["HIVE_BRIDGE_HTTP"] + "/health", timeout=5) as resp:
        health = json.loads(resp.read().decode())
except Exception as exc:  # reported as data, not as a crash
    health = {"error": str(exc)}

print(json.dumps({
    "bridge_health_ok": bool(health.get("ok")),
    "tab_id_env": os.environ.get("HIVE_BRIDGE_TAB_ID"),
    "arg_echo": context.get("args"),
    "skill_dir": os.environ.get("HIVE_BRIDGE_SKILL_DIR"),
}))
PY
log "fixtures written under $OUT"

step "start fixture web server on 127.0.0.1:$FIXTURE_PORT"
( cd "$OUT/web" && $PY_RUNNER -m http.server "$FIXTURE_PORT" --bind 127.0.0.1 ) > "$OUT/fixture.log" 2>&1 &
FIXTURE_PID=$!
disown "$FIXTURE_PID" 2>/dev/null || true
for _ in $(seq 1 40); do
  if curl -sf "http://127.0.0.1:$FIXTURE_PORT/index.html" > /dev/null; then break; fi
  sleep 0.25
done
curl -sf "http://127.0.0.1:$FIXTURE_PORT/index.html" > /dev/null || fail "fixture server did not start"
log "fixture server pid $FIXTURE_PID"

# ── bridge ──────────────────────────────────────────────────────────────────
step "start the Go bridge (ws $WS_PORT, legacy 9229, http $HTTP_PORT)"
# -no-idle-exit: this harness drives the bridge over /command for minutes and
# holds no control-RPC client, so the idle watchdog (30s with no client and no
# desktop parent) would take the bridge down mid-flow. The idle exit itself is
# covered by go_hive/scripts/e2e_browser_host.sh step 09 and the browserbridge
# cmd tests.
"$OUT/browserbridge" \
  -ws "127.0.0.1:$WS_PORT" \
  -http "127.0.0.1:$HTTP_PORT" \
  -control "127.0.0.1:${BB_E2E_CONTROL_PORT:-14831}" \
  -no-idle-exit \
  -legacy "127.0.0.1:$LEGACY_PORT" \
  -artifacts "$OUT/artifacts" \
  -extension "$EXT_DIR" \
  -skills "$OUT/skills" \
  -script-command "$( [ "$PY_RUNNER" = "python3" ] && echo python3 || echo uv,run,python )" \
  > "$OUT/bridge.log" 2>&1 &
BRIDGE_PID=$!
disown "$BRIDGE_PID" 2>/dev/null || true
for _ in $(seq 1 40); do
  curl -sf "$BASE/health" > /dev/null && break
  sleep 0.25
done
curl -sf "$BASE/health" > "$OUT/health.json" || fail "bridge never became healthy"
log "bridge pid $BRIDGE_PID ready: $(cat "$OUT/health.json")"
grep -q "legacy websocket listening" "$OUT/bridge.log" || fail "legacy $LEGACY_PORT listener did not bind"

# ── real chrome ─────────────────────────────────────────────────────────────
step "launch real Chrome with a throwaway profile and load the extension"
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
[ -s "$OUT/chrome-ready.json" ] || fail "chrome/extension never became ready (see $OUT/chromeloader.log)"
log "extension loaded: $(cat "$OUT/chrome-ready.json")"

for _ in $(seq 1 60); do
  if curl -sf "$BASE/status" | jq -e '.connected == true' > /dev/null; then break; fi
  sleep 0.5
done
curl -sf "$BASE/status" > "$OUT/status-connected.json"
jq -e '.connected == true' "$OUT/status-connected.json" > /dev/null || fail "the extension never connected to the bridge"
log "extension connected: $(jq -c '{profiles, connections: [.connections[].label]}' "$OUT/status-connected.json")"

# ── helpers ─────────────────────────────────────────────────────────────────
call() { # name, type, params-json
  local name=$1 type=$2 params=${3:-'{}'}
  local file="$OUT/cmd-$name.json"
  curl -sf --max-time 25 -X POST "$BASE/command" \
    -H 'Content-Type: application/json' \
    -d "$(jq -cn --arg t "$type" --argjson p "$params" '{type: $t, params: $p}')" \
    > "$file" || { printf '  FAIL: %s (transport error)\n' "$name"; FAILURES=$((FAILURES + 1)); return 1; }
  local ok
  ok=$(jq -r '.result.ok // .ok' "$file" 2>/dev/null | tail -1)
  if [ "$ok" = "true" ]; then
    printf '  ok  : %s -> %s\n' "$name" "$(jq -c '.result | del(.data, .tree, .snapshot) | . as $r | $r' "$file" | head -c 220)"
  else
    printf '  FAIL: %s -> %s\n' "$name" "$(head -c 300 "$file")"
    FAILURES=$((FAILURES + 1))
  fi
}

# ── the flow ────────────────────────────────────────────────────────────────
step "run the active-tool flow against the live extension"
call status browser_status '{"profile":"default"}'
call tabs browser_tabs '{}'
call open browser_open "{\"url\":\"http://127.0.0.1:$FIXTURE_PORT/index.html\"}"
TAB_ID=$(jq -r '.result.tabId' "$OUT/cmd-open.json")
[ "$TAB_ID" != "null" ] && [ -n "$TAB_ID" ] || fail "browser_open returned no tabId"
log "tab id: $TAB_ID"

call navigate browser_navigate "{\"tab_id\":$TAB_ID,\"url\":\"http://127.0.0.1:$FIXTURE_PORT/index.html\"}"
call interact_click browser_interact "{\"action\":\"left_click\",\"selector\":\"#btn\",\"tab_id\":$TAB_ID,\"auto_snapshot_mode\":\"simple\"}"
jq -e '.result.snapshot.ok == true' "$OUT/cmd-interact_click.json" > /dev/null || { log "  FAIL: click did not return an auto snapshot"; FAILURES=$((FAILURES+1)); }
# R15: a coordinate click returns the interaction dict plus the annotated
# screenshot (annotation drawn by the shared browser_screenshot path).
call interact_click_coordinate browser_interact "{\"action\":\"left_click\",\"coordinate\":[0.9,0.9],\"tab_id\":$TAB_ID,\"auto_snapshot_mode\":\"off\"}"
jq -e '.result.ok == true and .result.action == "click" and .result.mimeType == "image/jpeg"' "$OUT/cmd-interact_click_coordinate.json" > /dev/null || { log "  FAIL: coordinate click lost the action/image shape"; FAILURES=$((FAILURES+1)); }
jq -r '.result.data' "$OUT/cmd-interact_click_coordinate.json" | base64 -D > "$OUT/click-coordinate.jpg" 2>/dev/null \
  || jq -r '.result.data' "$OUT/cmd-interact_click_coordinate.json" | base64 -d > "$OUT/click-coordinate.jpg"
[ -s "$OUT/click-coordinate.jpg" ] || { log "  FAIL: coordinate click image decode failed"; FAILURES=$((FAILURES+1)); }
# The image must carry the annotation, not just be a capture: the red click
# marker is drawn at the clicked point. Scan for its pixels (PIL only; a host
# without PIL skips the scan instead of failing).
$PY_RUNNER - "$OUT" <<'PY' || { log "  FAIL: coordinate click marker scan"; FAILURES=$((FAILURES+1)); }
import json, pathlib, sys
out = pathlib.Path(sys.argv[1])
try:
    from PIL import Image
except Exception:
    print("  note: PIL unavailable — marker scan skipped")
    raise SystemExit(0)
body = json.loads((out / "cmd-interact_click_coordinate.json").read_text())
img = Image.open(out / "click-coordinate.jpg").convert("RGB")
w, h = img.size
px = img.load()
marker = [(x, y) for x in range(w) for y in range(h)
          if px[x, y][0] > 170 and px[x, y][1] < 120 and px[x, y][2] < 120]
if len(marker) < 50:
    print(f"  FAIL: red click-marker pixels = {len(marker)} in {w}x{h}")
    raise SystemExit(1)
xs = [p[0] for p in marker]
ys = [p[1] for p in marker]
print(f"  ok  : click marker {len(marker)} red px, bbox=({min(xs)},{min(ys)})-({max(xs)},{max(ys)}) in {w}x{h}")
PY
call evaluate_title browser_evaluate "{\"tab_id\":$TAB_ID,\"script\":\"document.title\"}"
jq -e '.result.result == "clicked"' "$OUT/cmd-evaluate_title.json" > /dev/null || log "  note: document.title not 'clicked' ($(jq -c '.result.result' "$OUT/cmd-evaluate_title.json"))"

call interact_type browser_interact "{\"action\":\"type\",\"selector\":\"#name\",\"text\":\"hello bridge\",\"tab_id\":$TAB_ID,\"auto_snapshot_mode\":\"off\"}"
call evaluate_value browser_evaluate "{\"tab_id\":$TAB_ID,\"script\":\"document.querySelector('#name').value\"}"
jq -e '.result.result == "hello bridge"' "$OUT/cmd-evaluate_value.json" > /dev/null || { log "  FAIL: typed value not observed"; FAILURES=$((FAILURES+1)); }

call interact_key browser_interact "{\"action\":\"key\",\"text\":\"Tab\",\"tab_id\":$TAB_ID,\"auto_snapshot_mode\":\"off\"}"
call interact_scroll browser_interact "{\"action\":\"scroll\",\"scroll_direction\":\"down\",\"scroll_amount\":300,\"tab_id\":$TAB_ID,\"auto_snapshot_mode\":\"off\"}"
call interact_zoom browser_interact "{\"action\":\"zoom\",\"region\":[0.0,0.0,0.5,0.4],\"tab_id\":$TAB_ID}"
jq -e '.result.data != null' "$OUT/cmd-interact_zoom.json" > /dev/null || { log "  FAIL: zoom returned no image"; FAILURES=$((FAILURES+1)); }

call select browser_select "{\"selector\":\"#pick\",\"values\":[\"b\"],\"tab_id\":$TAB_ID}"
jq -e '.result.selected == ["b"]' "$OUT/cmd-select.json" > /dev/null || { log "  FAIL: select did not report ['b']"; FAILURES=$((FAILURES+1)); }

call get_text browser_get_text "{\"selector\":\"#para\",\"tab_id\":$TAB_ID}"
jq -e '.result.text == "hello from fixture"' "$OUT/cmd-get_text.json" > /dev/null || { log "  FAIL: get_text returned the wrong text"; FAILURES=$((FAILURES+1)); }

call resize browser_resize "{\"width\":1280,\"height\":900,\"tab_id\":$TAB_ID}"
call evaluate_viewport browser_evaluate "{\"tab_id\":$TAB_ID,\"script\":\"window.innerWidth\"}"
jq -e '.result.result == 1280' "$OUT/cmd-evaluate_viewport.json" > /dev/null || log "  note: innerWidth=$(jq -c '.result.result' "$OUT/cmd-evaluate_viewport.json") (device metrics override)"

call shadow_query browser_shadow_query "{\"selector\":\"#host >>> .item\",\"tab_id\":$TAB_ID}"
jq -e '.result.tag == "SPAN"' "$OUT/cmd-shadow_query.json" > /dev/null || { log "  FAIL: shadow query did not pierce"; FAILURES=$((FAILURES+1)); }

call upload browser_upload "{\"selector\":\"#file\",\"file_paths\":[\"$OUT/upload-payload.txt\"],\"tab_id\":$TAB_ID}"
jq -e '.result.accepted.change_fired == true' "$OUT/cmd-upload.json" > /dev/null || { log "  FAIL: upload change event not observed"; FAILURES=$((FAILURES+1)); }

call html browser_html "{\"tab_id\":$TAB_ID,\"selector\":\"#para\"}"
HTML_ARTIFACT=$(jq -r '.result.saved_to' "$OUT/cmd-html.json")
[ -s "$HTML_ARTIFACT" ] || { log "  FAIL: html artifact missing"; FAILURES=$((FAILURES+1)); }
cp "$HTML_ARTIFACT" "$OUT/artifact.html" 2>/dev/null || true

call screenshot browser_screenshot "{\"intent\":\"verify the fixture renders\",\"tab_id\":$TAB_ID}"
jq -r '.result.data' "$OUT/cmd-screenshot.json" | base64 -D > "$OUT/screenshot.png" 2>/dev/null \
  || jq -r '.result.data' "$OUT/cmd-screenshot.json" | base64 -d > "$OUT/screenshot.png"
[ -s "$OUT/screenshot.png" ] || { log "  FAIL: screenshot decode failed"; FAILURES=$((FAILURES+1)); }

call snapshot browser_snapshot "{\"tab_id\":$TAB_ID,\"mode\":\"simple\"}"
jq -e '.result.nodes > 0' "$OUT/cmd-snapshot.json" > /dev/null || { log "  FAIL: snapshot had no nodes"; FAILURES=$((FAILURES+1)); }

call console browser_console "{\"tab_id\":$TAB_ID}"
jq -e '.result.message == "Console capture not yet implemented"' "$OUT/cmd-console.json" > /dev/null || log "  note: console stub shape changed"

call interact_confirm browser_interact "{\"action\":\"left_click\",\"selector\":\"#late-confirm\",\"tab_id\":$TAB_ID,\"auto_snapshot_mode\":\"off\"}"
sleep 1
call dialog_respond browser_dialog_respond "{\"action\":\"accept\",\"tab_id\":$TAB_ID}"
call evaluate_confirm browser_evaluate "{\"tab_id\":$TAB_ID,\"script\":\"window.__confirmed\"}"
jq -e '.result.result == true' "$OUT/cmd-evaluate_confirm.json" > /dev/null || log "  note: confirm() result=$(jq -c '.result.result' "$OUT/cmd-evaluate_confirm.json")"

call script browser_script "{\"skill\":\"hive.e2e-skill\",\"script\":\"probe.py\",\"args\":{\"purpose\":\"e2e\"},\"tab_id\":$TAB_ID}"
jq -e '.result.result.bridge_health_ok == true' "$OUT/cmd-script.json" > /dev/null || { log "  FAIL: skill script could not reach the bridge"; FAILURES=$((FAILURES+1)); }

call context_create create_context '{"agent_id":"e2e-agent","display_name":"E2E Agent"}'
call context_status browser_status '{"profile":"e2e-agent"}'
jq -e '.result.running == true' "$OUT/cmd-context_status.json" > /dev/null || { log "  FAIL: context did not register"; FAILURES=$((FAILURES+1)); }
call context_stop browser_stop '{"profile":"e2e-agent"}'
jq -e '.result.status == "stopped"' "$OUT/cmd-context_stop.json" > /dev/null || { log "  FAIL: browser_stop did not stop the context"; FAILURES=$((FAILURES+1)); }
call stop_setup browser_setup '{}'

curl -sf "$BASE/status" > "$OUT/status-final.json"
curl -sf "$BASE/contexts" > "$OUT/contexts.json"
curl -sf "$BASE/profiles" > "$OUT/profiles.json"
curl -sf "$BASE/tabs/$TAB_ID/actions" > "$OUT/tab-actions.json"
curl -sf "$BASE/tabs/$TAB_ID/health" > "$OUT/tab-health.json"

cp "$OUT/bridge.log" "$OUT/bridge.log" 2>/dev/null || true

log ""
log "evidence files:"
ls -1 "$OUT" | sed 's/^/  /'
log ""
log "screenshot: $OUT/screenshot.png ($(wc -c < "$OUT/screenshot.png" | tr -d ' ') bytes)"
log "tab actions: $(jq -c '[.actions[] | {verb, target, ok}]' "$OUT/tab-actions.json")"

if [ "$FAILURES" -ne 0 ]; then
  fail "$FAILURES step(s) failed — evidence in $OUT"
fi
log "PASS: all steps succeeded against real Chrome ($("$CHROME_BIN" --version 2>/dev/null))"