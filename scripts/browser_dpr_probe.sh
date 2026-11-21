#!/usr/bin/env bash
# browser_dpr_probe.sh — fixed-input, two-sided dpr probe for the annotated
# screenshot contract (upstream tools/src/gcu/browser/tools/inspection.py
# `_resize_and_annotate` vs the Go bridge's page-overlay annotation).
#
# For each dpr in {1,2,3} the probe:
#   1. starts the Go bridge (isolated ports) and real Chrome with
#      --force-device-scale-factor=<dpr> (a wrapper the chromeloader execs),
#      loading the bundled extension into a throwaway profile;
#   2. opens a fixed fixture: 1280x720 CSS viewport, #probe-target fixed at
#      CSS (240,300) 640x120;
#   3. clicks #probe-target (records the last-interaction highlight), then
#      captures one raw PNG and one annotated 800px JPEG through the same
#      /command API the tool layer uses;
#   4. writes the live getBoundingClientRect + bridge metadata into a manifest.
#
# The analysis half (browser_dpr_probe_analyze.py) feeds the same raw PNG
# through the upstream PIL implementation and measures both annotated JPEGs
# pixel-wise (rect edges via scanlines, region means, label band).
#
# Ports default to 19859-19862/19869 because a live Hive session usually owns
# 14829/9229. Override with BB_DPR_*.
#
# Usage: go_hive/scripts/browser_dpr_probe.sh [evidence-dir]
set -euo pipefail
export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)
MODULE_DIR="$REPO/go_hive/browserbridge"
OUT=${1:-$(mktemp -d "${TMPDIR:-/tmp}/browser-dpr-probe.XXXXXX")}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
WS_PORT=${BB_DPR_WS_PORT:-19859}
HTTP_PORT=${BB_DPR_HTTP_PORT:-19860}
CONTROL_PORT=${BB_DPR_CONTROL_PORT:-19861}
LEGACY_PORT=${BB_DPR_LEGACY_PORT:-19862}
FIXTURE_PORT=${BB_DPR_FIXTURE_PORT:-19869}
DPRS=${BB_DPR_PROBE_DPRS:-"1 2 3"}
CSS_W=1280
CSS_H=720
BASE="http://127.0.0.1:${HTTP_PORT}"
EXT_SRC="$MODULE_DIR/extension"
EXT_COPY="$OUT/extension"

FAILURES=0
BRIDGE_PID=""
LOADER_PID=""
FIXTURE_PID=""

have() { command -v "$1" >/dev/null 2>&1; }
log() { printf '%s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "$LOADER_PID" ] && kill "$LOADER_PID" 2>/dev/null || true
  [ -n "$BRIDGE_PID" ] && kill "$BRIDGE_PID" 2>/dev/null || true
  [ -n "$FIXTURE_PID" ] && kill "$FIXTURE_PID" 2>/dev/null || true
  sleep 1
  [ -n "$LOADER_PID" ] && kill -9 "$LOADER_PID" 2>/dev/null || true
  [ -n "$BRIDGE_PID" ] && kill -9 "$BRIDGE_PID" 2>/dev/null || true
  [ -n "$FIXTURE_PID" ] && kill -9 "$FIXTURE_PID" 2>/dev/null || true
  for port in "$WS_PORT" "$HTTP_PORT" "$CONTROL_PORT" "$LEGACY_PORT" "$FIXTURE_PORT"; do
    lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | xargs -r kill -9 2>/dev/null || true
  done
  if [ -d "$OUT" ]; then
    pkill -9 -f "user-data-dir=$OUT/chrome-profile" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# ── preflight ───────────────────────────────────────────────────────────────
have curl || fail "curl is required"
have jq || fail "jq is required"
have uv || fail "uv is required"
[ -f "$EXT_SRC/manifest.json" ] || fail "no extension manifest at $EXT_SRC"
CHROME_BIN=${BB_DPR_CHROME:-}
if [ -z "$CHROME_BIN" ]; then
  for candidate in \
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
    "/Applications/Chromium.app/Contents/MacOS/Chromium" \
    "$(command -v google-chrome 2>/dev/null || true)" \
    "$(command -v chromium 2>/dev/null || true)"; do
    if [ -n "$candidate" ] && [ -x "$candidate" ]; then CHROME_BIN=$candidate; break; fi
  done
fi
[ -n "$CHROME_BIN" ] || fail "no Chrome/Chromium binary found (set BB_DPR_CHROME)"
PY_RUNNER=(uv run --offline --project "$REPO/tools" python)

mkdir -p "$OUT"
log "evidence dir : $OUT"
log "chrome       : $CHROME_BIN ($("$CHROME_BIN" --version 2>/dev/null || echo unknown))"
log "dprs         : $DPRS"

for port in "$WS_PORT" "$HTTP_PORT" "$CONTROL_PORT" "$LEGACY_PORT" "$FIXTURE_PORT"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    fail "port $port is already in use"
  fi
done

# ── build ───────────────────────────────────────────────────────────────────
BRIDGE_BIN=${BB_DPR_BROWSERBRIDGE_BIN:-$OUT/browserbridge}
log "== build browserbridge + chromeloader =="
(cd "$MODULE_DIR" && go build -o "$OUT/chromeloader" ./cmd/chromeloader)
if [ -n "${BB_DPR_BROWSERBRIDGE_BIN:-}" ]; then
  log "bridge binary : $BRIDGE_BIN (prebuilt override)"
else
  (cd "$MODULE_DIR" && go build -o "$BRIDGE_BIN" ./cmd/browserbridge)
fi

# ── fixture + extension copy ────────────────────────────────────────────────
mkdir -p "$OUT/web" "$OUT/artifacts"
cat > "$OUT/web/index.html" <<'HTML'
<!doctype html>
<html><head><meta charset="utf-8"><title>DPR probe fixture</title>
<style>
  html, body { margin: 0; padding: 0; height: 100%; background: #ffffff; overflow: hidden; }
  #probe-target { position: fixed; left: 240px; top: 300px; width: 640px; height: 120px; background: #334155; }
</style></head>
<body><div id="probe-target"></div></body></html>
HTML

rm -rf "$EXT_COPY"
cp -R "$EXT_SRC" "$EXT_COPY"
perl -0pi -e "s/const BRIDGE_PORTS = \[14829, 9229\];/const BRIDGE_PORTS = [$WS_PORT];/" "$EXT_COPY/offscreen.js"
grep -q "BRIDGE_PORTS = \[$WS_PORT\]" "$EXT_COPY/offscreen.js" || fail "could not repoint the extension copy at port $WS_PORT"

log "== fixture server on 127.0.0.1:$FIXTURE_PORT =="
(cd "$OUT/web" && "${PY_RUNNER[@]}" -m http.server "$FIXTURE_PORT" --bind 127.0.0.1) > "$OUT/fixture.log" 2>&1 &
FIXTURE_PID=$!
disown "$FIXTURE_PID" 2>/dev/null || true
for _ in $(seq 1 40); do
  curl -sf "http://127.0.0.1:$FIXTURE_PORT/index.html" >/dev/null && break
  sleep 0.25
done
curl -sf "http://127.0.0.1:$FIXTURE_PORT/index.html" >/dev/null || fail "fixture server did not start"

command_post() { # <type> <params-json> > response file
  curl -s --max-time 120 -X POST "$BASE/command" \
    -H 'Content-Type: application/json' \
    -d "$(jq -cn --arg t "$1" --argjson p "$2" '{type: $t, params: $p}')"
}

command_post_ok() { # <type> <params-json> <file> <attempts> — retries until result.ok
  local attempts=${4:-3} i=1
  while [ "$i" -le "$attempts" ]; do
    command_post "$1" "$2" > "$3" || true
    if jq -e '.ok == true and (.result.ok == true or (.result | type) == "array")' "$3" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
    i=$((i + 1))
  done
  return 1
}

start_bridge() {
  "$BRIDGE_BIN" \
    -ws "127.0.0.1:$WS_PORT" \
    -http "127.0.0.1:$HTTP_PORT" \
    -control "127.0.0.1:$CONTROL_PORT" \
    -legacy "127.0.0.1:$LEGACY_PORT" \
    -no-idle-exit \
    -artifacts "$OUT/artifacts" \
    -extension "$EXT_COPY" \
    > "$OUT/bridge.log" 2>&1 &
  BRIDGE_PID=$!
  disown "$BRIDGE_PID" 2>/dev/null || true
  for _ in $(seq 1 60); do
    curl -sf "$BASE/health" >/dev/null && return 0
    sleep 0.25
  done
  fail "bridge never became healthy"
}

stop_bridge() {
  [ -n "$BRIDGE_PID" ] || return 0
  kill "$BRIDGE_PID" 2>/dev/null || true
  for _ in $(seq 1 40); do
    kill -0 "$BRIDGE_PID" 2>/dev/null || break
    sleep 0.25
  done
  kill -9 "$BRIDGE_PID" 2>/dev/null || true
  BRIDGE_PID=""
  for _ in $(seq 1 40); do
    lsof -nP -iTCP:"$WS_PORT" -sTCP:LISTEN >/dev/null 2>&1 || break
    sleep 0.25
  done
}

stop_chrome() {
  [ -n "$LOADER_PID" ] || return 0
  kill "$LOADER_PID" 2>/dev/null || true
  for _ in $(seq 1 60); do
    kill -0 "$LOADER_PID" 2>/dev/null || break
    sleep 0.25
  done
  kill -9 "$LOADER_PID" 2>/dev/null || true
  pkill -9 -f "user-data-dir=$OUT/chrome-profile-$1" 2>/dev/null || true
  LOADER_PID=""
}

mkdir -p "$OUT/evidence"
for dpr in $DPRS; do
  STEP_DIR="$OUT/evidence/dpr$dpr"
  mkdir -p "$STEP_DIR"
  log ""
  log "== dpr=$dpr =="

  start_bridge

  cat > "$OUT/chrome-dpr$dpr.sh" <<EOF
#!/bin/bash
exec "$CHROME_BIN" --force-device-scale-factor=$dpr "\$@"
EOF
  chmod +x "$OUT/chrome-dpr$dpr.sh"

  PROFILE_DIR="$OUT/chrome-profile-$dpr"
  rm -rf "$PROFILE_DIR"
  "$OUT/chromeloader" \
    -ext "$EXT_COPY" \
    -profile "$PROFILE_DIR" \
    -chrome "$OUT/chrome-dpr$dpr.sh" \
    -ready-file "$OUT/chrome-ready-$dpr.json" \
    -timeout 60s \
    -open about:blank \
    > "$OUT/chromeloader-$dpr.log" 2>&1 &
  LOADER_PID=$!
  disown "$LOADER_PID" 2>/dev/null || true
  for _ in $(seq 1 160); do
    [ -s "$OUT/chrome-ready-$dpr.json" ] && break
    kill -0 "$LOADER_PID" 2>/dev/null || break
    sleep 0.5
  done
  [ -s "$OUT/chrome-ready-$dpr.json" ] || { log "  FAIL: chrome never became ready (dpr=$dpr)"; FAILURES=$((FAILURES+1)); stop_chrome "$dpr"; stop_bridge; continue; }
  for _ in $(seq 1 120); do
    if curl -sf "$BASE/status" | jq -e '.connected == true' >/dev/null 2>&1; then break; fi
    sleep 0.5
  done
  curl -sf "$BASE/status" > "$STEP_DIR/status-connected.json" || true
  jq -e '.connected == true' "$STEP_DIR/status-connected.json" >/dev/null 2>&1 || { log "  FAIL: extension never connected (dpr=$dpr)"; FAILURES=$((FAILURES+1)); stop_chrome "$dpr"; stop_bridge; continue; }

  # Open the fixture. The CSS viewport is pinned by the Chrome window size; ask
  # for an explicit metrics override only if the live viewport differs (the
  # Emulation call stalls for its full CDP budget on some extension builds).
  command_post browser_open "{\"url\":\"http://127.0.0.1:$FIXTURE_PORT/index.html\"}" > "$STEP_DIR/open.json"
  TAB_ID=$(jq -r '.result.tabId' "$STEP_DIR/open.json")
  [ -n "$TAB_ID" ] && [ "$TAB_ID" != "null" ] || { log "  FAIL: no tab id (dpr=$dpr)"; FAILURES=$((FAILURES+1)); stop_chrome "$dpr"; stop_bridge; continue; }

  read_viewport() {
    command_post browser_evaluate "{\"tab_id\":$TAB_ID,\"script\":\"window.innerWidth+'x'+window.innerHeight+'@'+window.devicePixelRatio\"}" > "$STEP_DIR/viewport.json"
    jq -r '.result.result // ""' "$STEP_DIR/viewport.json"
  }
  VIEWPORT=$(read_viewport)
  VIEW_W=${VIEWPORT%%x*}
  VIEW_REST=${VIEWPORT#*x}
  VIEW_H=${VIEW_REST%%@*}
  VIEW_DPR=${VIEW_REST#*@}
  if [ "$VIEW_W" != "$CSS_W" ] || [ "$VIEW_H" != "$CSS_H" ]; then
    command_post browser_resize "{\"width\":$CSS_W,\"height\":$CSS_H,\"tab_id\":$TAB_ID}" > "$STEP_DIR/resize.json"
    VIEWPORT=$(read_viewport)
    VIEW_W=${VIEWPORT%%x*}; VIEW_REST=${VIEWPORT#*x}; VIEW_H=${VIEW_REST%%@*}; VIEW_DPR=${VIEW_REST#*@}
  else
    echo '{"skipped": "viewport already fixed by --window-size"}' > "$STEP_DIR/resize.json"
  fi
  log "  viewport: $VIEWPORT"
  if [ "$VIEW_W" != "$CSS_W" ] || [ "$VIEW_H" != "$CSS_H" ]; then
    log "  FAIL: viewport $VIEWPORT != ${CSS_W}x${CSS_H} (dpr=$dpr)"
    FAILURES=$((FAILURES+1))
  fi
  if [ "$VIEW_DPR" != "$dpr" ]; then
    log "  FAIL: window.devicePixelRatio=$VIEW_DPR, wanted $dpr"
    FAILURES=$((FAILURES+1))
  fi

  # Click the target: records the last-interaction highlight (CSS rect).
  command_post_ok browser_interact "{\"action\":\"left_click\",\"selector\":\"#probe-target\",\"tab_id\":$TAB_ID,\"auto_snapshot_mode\":\"off\"}" "$STEP_DIR/click.json" 3 \
    || { log "  FAIL: click failed (dpr=$dpr): $(head -c 200 "$STEP_DIR/click.json")"; FAILURES=$((FAILURES+1)); }

  # The authoritative CSS rect of the annotated target (same box the bridge
  # recorded as the highlight). The outer (function(){…})() keeps the script a
  # single expression — the bridge wraps bare multi-statement scripts without a
  # return.
  command_post browser_evaluate "{\"tab_id\":$TAB_ID,\"script\":\"(function(){var r=document.querySelector('#probe-target').getBoundingClientRect();return JSON.stringify({left:r.left,top:r.top,width:r.width,height:r.height});})()\"}" > "$STEP_DIR/rect.json"
  RECT_JSON=$(jq -r '.result.result // empty' "$STEP_DIR/rect.json")
  jq -n --argjson rect "${RECT_JSON:-null}" '$rect' >/dev/null 2>&1 || { log "  FAIL: rect evaluate returned no JSON (dpr=$dpr): $(head -c 200 "$STEP_DIR/rect.json")"; FAILURES=$((FAILURES+1)); }
  # The probe's fixed input: layout must put #probe-target at CSS (240,300) 640x120.
  jq -e '(.left|round)==240 and (.top|round)==300 and (.width|round)==640 and (.height|round)==120' \
    <<<"${RECT_JSON:-null}" >/dev/null 2>&1 \
    || { log "  FAIL: target rect ${RECT_JSON:-null} != expected (240,300,640,120) (dpr=$dpr)"; FAILURES=$((FAILURES+1)); }
  log "  target rect: $RECT_JSON"

  # Raw viewport capture (no clip, no resize, no annotation): the shared input
  # for the upstream PIL half.
  command_post_ok browser_screenshot "{\"tab_id\":$TAB_ID,\"format\":\"png\"}" "$STEP_DIR/raw.json" 3 \
    || { log "  FAIL: raw capture failed (dpr=$dpr)"; FAILURES=$((FAILURES+1)); }
  jq -r '.result.data' "$STEP_DIR/raw.json" | base64 -D > "$STEP_DIR/raw.png" 2>/dev/null \
    || jq -r '.result.data' "$STEP_DIR/raw.json" | base64 -d > "$STEP_DIR/raw.png"
  [ -s "$STEP_DIR/raw.png" ] || { log "  FAIL: raw capture decode failed (dpr=$dpr)"; FAILURES=$((FAILURES+1)); }

  # Annotated 800px JPEG through the same path the tool layer uses.
  command_post_ok browser_screenshot "{\"tab_id\":$TAB_ID,\"format\":\"jpeg\",\"quality\":75,\"max_width\":800,\"annotate\":true}" "$STEP_DIR/go.json" 3 \
    || { log "  FAIL: annotated capture failed (dpr=$dpr)"; FAILURES=$((FAILURES+1)); }
  jq -r '.result.data' "$STEP_DIR/go.json" | base64 -D > "$STEP_DIR/go.jpg" 2>/dev/null \
    || jq -r '.result.data' "$STEP_DIR/go.json" | base64 -d > "$STEP_DIR/go.jpg"
  [ -s "$STEP_DIR/go.jpg" ] || { log "  FAIL: annotated capture decode failed (dpr=$dpr)"; FAILURES=$((FAILURES+1)); }

  jq -n \
    --argjson dpr "$dpr" \
    --argjson cssW "$VIEW_W" --argjson cssH "$VIEW_H" \
    --arg viewport "$VIEWPORT" \
    --argjson rect "$RECT_JSON" \
    --slurpfile rawmeta "$STEP_DIR/raw.json" \
    --slurpfile gometa "$STEP_DIR/go.json" \
    '{dpr: $dpr, cssWidth: $cssW, cssHeight: $cssH, viewport: $viewport,
      highlight: {kind: "rect", label: "#probe-target", x: $rect.left, y: $rect.top, w: $rect.width, h: $rect.height},
      raw: "raw.png", go: "go.jpg",
      rawMeta: ($rawmeta[0].result | {imageWidth, imageHeight, cssWidth, cssHeight, devicePixelRatio, capturedWidth, capturedHeight, physicalScale, annotated}),
      goMeta: ($gometa[0].result | {imageWidth, imageHeight, cssWidth, cssHeight, devicePixelRatio, capturedWidth, capturedHeight, physicalScale, annotated})}' \
    > "$STEP_DIR/manifest.json"

  log "  go annotated: $(jq -c '.goMeta | {imageWidth, imageHeight, devicePixelRatio, physicalScale, annotated}' "$STEP_DIR/manifest.json")"

  stop_chrome "$dpr"
  stop_bridge
done

log ""
log "== upstream PIL half + pixel comparison =="
"${PY_RUNNER[@]}" "$HERE/browser_dpr_probe_analyze.py" "$OUT" | tee "$OUT/analysis.txt"
[ "${PIPESTATUS[0]}" -eq 0 ] || FAILURES=$((FAILURES+1))

if [ "$FAILURES" -ne 0 ]; then
  fail "$FAILURES step(s) failed — evidence in $OUT"
fi
log ""
log "PASS: probe completed — evidence in $OUT"
