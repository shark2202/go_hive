#!/usr/bin/env bash
# probe_qg_staleness.sh — does the sidebar pick up a colony created OUT OF BAND?
#
# fetchColonies() runs only on mount / visibilitychange / in-app create-delete.
# fetchStatus() polls every 30s but only maps status onto EXISTING entries.
# So a colony created by another process should NOT appear until a reload.
#
# This probe: opens a tab at root, snapshots the sidebar, creates a throwaway
# colony via the API, waits past the 30s poll, snapshots again, then reloads
# and snapshots a third time. Cleans up the throwaway colony at the end.
set -uo pipefail
BASE="http://127.0.0.1:8788"
ORCA="/usr/local/bin/orca"
STAMP="$(date +%H%M%S)"
SLUG="qg_probe_${STAMP}"
ROUTE="${SLUG//_/-}"

js() { "$ORCA" eval --page "$PAGE" --expression "$1" --json 2>&1 \
  | /usr/bin/python3 -c 'import sys,json
try: print(json.load(sys.stdin).get("result",{}).get("result") or "")
except Exception: print("")'; }

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

echo "--- T0: sidebar BEFORE out-of-band create ---"
js "JSON.stringify({count:(document.body.innerText.match(/蜂群\s*(\d+)/)||[])[1], has:Array.from(document.querySelectorAll(\"a[href*='/colony/']\")).map(e=>e.getAttribute('href')).filter(h=>h.includes('$ROUTE')).length})"

echo "--- creating $SLUG out of band (API) ---"
curl -s -X POST "$BASE/api/sessions" -H 'Content-Type: application/json' \
  -d "{\"colony_id\":\"$SLUG\",\"queen_name\":\"queen_growth\",\"initial_prompt\":\"staleness probe\"}" \
  -o /tmp/qg_probe_create.json -w "HTTP:%{http_code}\n"
head -c 300 /tmp/qg_probe_create.json; echo

echo "--- T+40s: sidebar WITHOUT reload (past the 30s poll) ---"
sleep 40
js "JSON.stringify({count:(document.body.innerText.match(/蜂群\s*(\d+)/)||[])[1], has:Array.from(document.querySelectorAll(\"a[href*='/colony/']\")).map(e=>e.getAttribute('href')).filter(h=>h.includes('$ROUTE')).length})"

echo "--- T+reload: sidebar AFTER reload ---"
"$ORCA" reload --page "$PAGE" --json >/dev/null 2>&1 || true
sleep 5
js "JSON.stringify({count:(document.body.innerText.match(/蜂群\s*(\d+)/)||[])[1], has:Array.from(document.querySelectorAll(\"a[href*='/colony/']\")).map(e=>e.getAttribute('href')).filter(h=>h.includes('$ROUTE')).length})"

echo "--- cleanup: delete $SLUG ---"
curl -s -X DELETE "$BASE/api/colonies/$SLUG" -o /tmp/qg_probe_del.json -w "HTTP:%{http_code}\n"
head -c 200 /tmp/qg_probe_del.json; echo
"$ORCA" tab close --page "$PAGE" --json >/dev/null 2>&1 || true
echo "DONE"
