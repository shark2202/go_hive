#!/usr/bin/env bash
# probe_qg_stale2.sh — lean staleness probe.
# Creates a MINIMAL colony on disk (out of band; /discover reads disk), then asks
# whether the sidebar shows it WITHOUT a reload. Disk-only avoids the heavy
# queen-bootstrap path that a POST /api/sessions triggers.
set -uo pipefail
BASE="http://127.0.0.1:8788"; ORCA="/usr/local/bin/orca"
SLUG="qg_stale_$(date +%H%M%S)"; ROUTE="${SLUG//_/-}"
DIR="/Users/mac/.hive-go/colonies/$SLUG"

js() { "$ORCA" eval --page "$PAGE" --expression "$1" --json 2>&1 \
  | /usr/bin/python3 -c 'import sys,json
try: print(json.load(sys.stdin).get("result",{}).get("result") or "")
except Exception: print("<no-result>")'; }
probe() { js "JSON.stringify({has:Array.from(document.querySelectorAll(\"a[href*='/colony/']\")).some(e=>(e.getAttribute('href')||'').includes('$ROUTE')),count:(document.body.innerText.match(/蜂群\s*(\d+)/)||[])[1]})"; }

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
sleep 3
echo "SLUG=$SLUG"
echo "T0  baseline            : $(probe)"

mkdir -p "$DIR"
printf '{"created_at":%s,"name":"%s","queen_name":"queen_growth"}\n' "$(date +%s)" "$SLUG" > "$DIR/metadata.json"
printf '{"name":"%s","queen_name":"queen_growth","description":"stale probe","goal":{"description":"probe"},"tools":[]}\n' "$SLUG" > "$DIR/worker.json"
printf '[]\n' > "$DIR/triggers.json"
echo "    created on disk     : $DIR"
echo "T+0s  api discover sees : $(curl -s "$BASE/api/discover" | grep -c "$SLUG")"

echo "T+40s NO-reload         : $(sleep 40; probe)"
echo "      (expect has:false if the list is mount-only)"
"$ORCA" reload --page "$PAGE" --json >/dev/null 2>&1 || true
echo "T+rl  AFTER reload      : $(sleep 5; probe)"
echo "      (expect has:true)"
