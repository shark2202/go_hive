#!/usr/bin/env bash
# e2e_browser_panel.sh — R01/R02/R03/R04/R05/R07 end-to-end probe.
#
# Builds the real browserbridge + hive + hive-browser binaries, starts a bridge
# on throwaway ports with a fake extension speaking the real WebSocket protocol,
# starts a real `hive serve` pointed at that bridge, and asserts:
#
#   R01  `hive-browser screenshot` spills the real 800px JPEG: saved_to on
#        disk (JPEG magic, Pillow-decodable 800x450) + _image marker + the
#        upstream metadata, with no inline base64.
#   R02  `hive-browser page snapshot` spills the AX tree to saved_to, returns
#        length, the tree carries [ref=eN] and [href="…"], and `--head` reads
#        real content back.
#   R03  GET /api/browser/status reports the running bridge (connected/healthy
#        with an extension attached) instead of a hardcoded offline.
#   R04  GET /api/browser/contexts and /api/browser/tabs return the upstream
#        Python bodies ({available,count,…} / isHiveGroup enrichment / 200
#        available:false when no bridge), and POST /contexts/close keeps the
#        upstream {closed:[…]} / 400 contract.
#   R05  CLI exit codes: screenshot without --intent reaches the bridge (0 when
#        connected, 2 when there is no bridge — never 6), `help nosuchcmd` is 1,
#        and a leading common flag (`--json status`) parses.
#   R07  `hive-browser upload` with a missing file exits 4 (the bridge's
#        "File not found", never the old local 6).
#   R15  coordinate click/hover/key carry the annotated screenshot: the CLI
#        result keeps the interaction dict and adds only saved_to + _image
#        (800x450 JPEG, decoded), with no inline base64 or screenshot metadata.
#
# Ports are overridable: BRIDGE_WS_PORT / BRIDGE_HTTP_PORT / BRIDGE_CONTROL_PORT
# / SERVER_PORT. Zero network access; everything is local and torn down on exit.
# Set E2E_EVIDENCE_DIR to copy the R15 raw JSON outputs out of the temp dir.
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

echo "== R01: screenshot spills the real 800px JPEG =="
set +e
cli screenshot --tab 101 --json --bridge "http://127.0.0.1:$HTTP_PORT" >"$tmp/shot.json" 2>"$tmp/shot.err"
code=$?
set -e
[ "$code" -eq 0 ] || fail "screenshot exited $code (stderr: $(cat "$tmp/shot.err"))"
python3 - "$tmp/shot.json" <<'PY' || fail "R01 screenshot contract"
import json, pathlib, sys
body = json.loads(pathlib.Path(sys.argv[1]).read_text())
assert "data" not in body, "inline base64 must be spilled"
assert body["ok"] is True and body["imageType"] == "jpeg", body
assert body["imageWidth"] == 800, body
saved = pathlib.Path(body["saved_to"])
assert saved.is_file(), saved
raw = saved.read_bytes()
assert raw[:3] == b"\xff\xd8\xff", raw[:3]
assert body["size"] == len(raw), (body["size"], len(raw))
assert saved.name.startswith("browser_screenshot_") and saved.name.endswith("_tab101.jpg"), saved.name
image = body["_image"]
assert image["path"] == str(saved) and image["mime"] == "image/jpeg", image
assert "intent" in image and image["intent"] is None, image
assert body["annotated"] is False, body
assert "fractions 0..1" in body["scaleHint"], body
PY
uv run --offline --project "$root/tools" python - "$tmp/shot.json" <<'PY' || fail "R01 JPEG decode"
import json, pathlib, sys
from PIL import Image
body = json.loads(pathlib.Path(sys.argv[1]).read_text())
with Image.open(body["saved_to"]) as img:
    assert img.format == "JPEG", img.format
    assert img.size == (800, 450), img.size
    assert body["imageWidth"] == img.width, (body["imageWidth"], img.size)
PY
pass "hive-browser screenshot -> 800x450 JPEG at saved_to + _image (no inline base64)"

echo "== R02: snapshot refs/href + artifact + --head =="
set +e
cli page snapshot --tab 101 --json --bridge "http://127.0.0.1:$HTTP_PORT" >"$tmp/snap.json" 2>"$tmp/snap.err"
code=$?
set -e
[ "$code" -eq 0 ] || fail "page snapshot exited $code (stderr: $(cat "$tmp/snap.err"))"
python3 - "$tmp/snap.json" <<'PY' || fail "R02 snapshot contract"
import json, pathlib, sys
body = json.loads(pathlib.Path(sys.argv[1]).read_text())
assert "tree" not in body and "nodes" not in body, body
saved = pathlib.Path(body["saved_to"])
assert saved.is_file(), saved
text = saved.read_text()
assert body["ok"] is True and body["length"] == len(text), (body["length"], len(text))
assert saved.name.startswith("browser_snapshot_") and saved.name.endswith("_tab101.txt"), saved.name
assert "[ref=e1]" in text, text
assert "- button [ref=e2] \"Go\"" in text, text
assert "[href=\"https://example.com/docs\"]" in text, text
PY
set +e
cli page snapshot --tab 101 --head 40 --json --bridge "http://127.0.0.1:$HTTP_PORT" >"$tmp/snap-head.json" 2>/dev/null
code=$?
set -e
[ "$code" -eq 0 ] || fail "page snapshot --head exited $code"
python3 - "$tmp/snap.json" "$tmp/snap-head.json" <<'PY' || fail "R02 --head no-op"
import json, pathlib, sys
full = json.loads(pathlib.Path(sys.argv[1]).read_text())
head = json.loads(pathlib.Path(sys.argv[2]).read_text())
text = pathlib.Path(full["saved_to"]).read_text()
assert head.get("head") == text[:40], (head.get("head"), text[:40])
PY
pass "hive-browser page snapshot -> saved_to + [ref=eN]/[href] + working --head"

echo "== R15: coordinate click/hover/key carry the annotated screenshot =="
r15() { # name, args...
  local name=$1
  shift
  set +e
  cli interact "$@" --tab 101 --json --bridge "http://127.0.0.1:$HTTP_PORT" >"$tmp/r15-$name.json" 2>"$tmp/r15-$name.err"
  local code=$?
  set -e
  [ "$code" -eq 0 ] || fail "R15 $name exited $code (stderr: $(cat "$tmp/r15-$name.err"))"
}
r15 click --action left_click --coordinate 0.5,0.5
r15 hover --action hover --coordinate 0.5,0.5
r15 key --action key --text Enter --coordinate 0.5,0.5
python3 - "$tmp" <<'PY' || fail "R15 coordinate action image contract"
import json, pathlib, sys
tmp = pathlib.Path(sys.argv[1])
cases = {
    "click": ("click", {"ok", "action", "tabId", "x", "y", "button", "clickCount", "method", "saved_to", "_image"}),
    "hover": ("hover_coordinate", {"ok", "action", "tabId", "x", "y", "saved_to", "_image"}),
    "key": ("press_at", {"ok", "action", "tabId", "x", "y", "key", "saved_to", "_image"}),
}
for name, (action, want_keys) in cases.items():
    body = json.loads((tmp / f"r15-{name}.json").read_text())
    assert body["ok"] is True, body
    assert body["action"] == action, body
    assert "data" not in body and "mimeType" not in body, "inline image must be spilled"
    assert set(body) == want_keys, (sorted(body), sorted(want_keys))
    saved = pathlib.Path(body["saved_to"])
    assert saved.is_file(), saved
    assert saved.name.startswith("browser_interact_") and saved.name.endswith("_tab101.jpg"), saved.name
    raw = saved.read_bytes()
    assert raw[:3] == b"\xff\xd8\xff", raw[:3]
    image = body["_image"]
    assert image["path"] == str(saved) and image["mime"] == "image/jpeg", image
    assert "intent" in image and image["intent"] is None, image
print("R15 key sets + pointers ok: click/hover/key")
PY
uv run --offline --project "$root/tools" python - "$tmp" <<'PY' || fail "R15 annotated JPEG decode"
import json, pathlib, sys
from PIL import Image
tmp = pathlib.Path(sys.argv[1])
for name in ("click", "hover", "key"):
    body = json.loads((tmp / f"r15-{name}.json").read_text())
    with Image.open(body["saved_to"]) as img:
        assert img.format == "JPEG", (name, img.format)
        assert img.size == (800, 450), (name, img.size)
print("R15 image decode ok: 800x450 JPEG x3")
PY
if [ "${E2E_EVIDENCE_DIR:-}" != "" ]; then
  mkdir -p "$E2E_EVIDENCE_DIR"
  cp "$tmp"/r15-*.json "$E2E_EVIDENCE_DIR"/
fi
pass "hive-browser interact coordinate click/hover/key -> interaction dict + saved_to/_image (800x450 JPEG)"

echo "== R07: upload missing file -> 4 (bridge-side, not local 6) =="
set +e
cli upload '#f' --file "$tmp/nope.txt" --tab 101 --json --bridge "http://127.0.0.1:$HTTP_PORT" >"$tmp/upload.json" 2>/dev/null
code=$?
set -e
[ "$code" -eq 4 ] || fail "upload missing file exited $code, want 4 (not_found)"
grep -q 'File not found' "$tmp/upload.json" || fail "upload body: $(cat "$tmp/upload.json")"
pass "hive-browser upload missing file -> 4 File not found (never 6)"

echo "ALL PASS"
if [ "${E2E_DUMP:-}" = "1" ]; then
  echo "--- status: $status_body"
  echo "--- contexts: $contexts_body"
  echo "--- tabs: $tabs_body"
fi
