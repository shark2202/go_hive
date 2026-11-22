#!/usr/bin/env bash
# probe_qg_stale3.sh — staleness probe with DRIFT DETECTION.
# The shared Orca browser has other agents retargeting tabs, so every probe
# re-asserts the page URL and reports drift instead of silently returning junk.
set -uo pipefail
BASE="http://127.0.0.1:8788"; ORCA="/usr/local/bin/orca"
SLUG="qg_stale_$(date +%H%M%S)"; ROUTE="${SLUG//_/-}"
DIR="/Users/mac/.hive-go/colonies/$SLUG"

js() { "$ORCA" eval --page "$PAGE" --expression "$1" --json 2>&1 \
  | /usr/bin/python3 -c 'import sys,json
try:
    v=json.load(sys.stdin).get("result",{}).get("result")
    print(v if v is not None else "<null>")
except Exception: print("<err>")'; }

probe() {
  local u; u="$(js 'location.href')"
  if [ "$u" != "$BASE/" ]; then echo "{\"drift\":\"$u\"}"; return; fi
  js "JSON.stringify({has:Array.from(document.querySelectorAll(\"a[href*='/colony/']\")).some(e=>(e.getAttribute('href')||'').includes('$ROUTE')),count:(document.body.innerText.match(/蜂群\s*(\d+)/)||[])[1]})"
}
cleanup() { rm -rf "$DIR"; [ -n "${PAGE:-}" ] && "$ORCA" tab close --page "$PAGE" --json >/dev/null 2>&1; }
trap cleanup EXIT

PAGE=""
for i in 1 2 3 4 5 6; do
  O="$("$ORCA" tab create --url "$BASE/" --json 2>&1)"
  PAGE="$(printf '%s' "$O" | /usr/bin/python3 -c 'import sys,json
try: print(json.load(sys.stdin).get("result",{}).get("browserPageId") or "")
except Exception: print("")' 2>/dev/null)"
  [ -n "$PAGE" ] && break; sleep $((i*4))
done
[ -n "$PAGE" ] || { echo '{"ok":false,"stage":"tab"}'; exit 1; }
echo "PAGE=$PAGE SLUG=$SLUG"
sleep 3
echo "T0   baseline (root)     : $(probe)"
mkdir -p "$DIR"
printf '{"created_at":%s,"name":"%s","queen_name":"queen_growth"}\n' "$(date +%s)" "$SLUG" > "$DIR/metadata.json"
printf '{"name":"%s","queen_name":"queen_growth","description":"stale probe","goal":{"description":"probe"},"tools":[]}\n' "$SLUG" > "$DIR/worker.json"
echo "[]" > "$DIR/triggers.json"
echo "     wrote $DIR"
echo "     /api/discover sees it: $(curl -s "$BASE/api/discover" | grep -c "$SLUG")"
echo "T+8s  no reload          : $(sleep 8; probe)"
echo "T+45s no reload          : $(sleep 37; probe)"
"$ORCA" reload --page "$PAGE" --json >/dev/null 2>&1 || true
echo "T+rl  after reload       : $(sleep 6; probe)"
