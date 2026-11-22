#!/usr/bin/env bash
# gate_queen_growth_v2.sh — UI E2E gate: colony must appear in sidebar AND colony page.
# Owned by queen_growth. Uniquely named to avoid collisions with other agents
# writing into this shared directory.
#
# Usage: gate_queen_growth_v2.sh <colony_disk_slug> [base_url]
# Exit 0 = PASS. One JSON line on stdout.

set -uo pipefail
SLUG="${1:?usage: gate_queen_growth_v2.sh <colony_disk_slug> [base_url]}"
BASE="${2:-http://127.0.0.1:8788}"
ORCA="${ORCA_BIN:-/usr/local/bin/orca}"
ROUTE="${SLUG//_/-}"            # slugToColonyId parity: "_" -> "-"
TOK="${SLUG##*_}"               # trailing digits, for the h1 identity check

fail() { echo "{\"ok\":false,\"stage\":\"$1\",\"reason\":\"$2\"}" >&2; exit 1; }

curl -sf -o /dev/null "$BASE/" || fail serve "serve unreachable at $BASE"

# Orca's runtime is shared; retry past transient runtime_unavailable.
PAGE=""
for i in 1 2 3 4 5 6; do
  OUT="$("$ORCA" tab create --url "$BASE/" --json 2>&1)"
  PAGE="$(printf '%s' "$OUT" | /usr/bin/python3 -c \
    'import sys,json
try: print(json.load(sys.stdin).get("result",{}).get("browserPageId") or "")
except Exception: print("")' 2>/dev/null)"
  [ -n "$PAGE" ] && break
  sleep $((i*4))
done
[ -n "$PAGE" ] || fail tab_create "no browserPageId after retries"
trap '"$ORCA" tab close --page "$PAGE" --json >/dev/null 2>&1 || true' EXIT

js() { "$ORCA" eval --page "$PAGE" --expression "$1" --json 2>&1 \
  | /usr/bin/python3 -c 'import sys,json
try: print(json.load(sys.stdin).get("result",{}).get("result") or "")
except Exception: print("")'; }

"$ORCA" wait --page "$PAGE" --load load --json >/dev/null 2>&1 || true
sleep 3

LINKS="$(js "JSON.stringify(Array.from(document.querySelectorAll(\"a[href*='/colony/']\")).map(e=>e.getAttribute('href')))")"
COUNT="$(js "(()=>{const m=document.body.innerText.match(/蜂群\s*(\d+)/);return m?m[1]:''})()")"
[ -n "$LINKS" ] || fail sidebar "sidebar links empty"
printf '%s' "$LINKS" | grep -q "$ROUTE" || fail sidebar_present "$ROUTE absent from sidebar: $LINKS"

"$ORCA" goto --page "$PAGE" --url "$BASE/#/colony/$ROUTE" --json >/dev/null 2>&1 || true
sleep 3
URL="$(js "location.href")"; H1="$(js "(()=>{const h=document.querySelector('h1,h2');return h?h.textContent.trim():''})()")"
ERR="$(js "String(/not found|404|Something went wrong|出错了/i.test(document.body.innerText))")"
case "$URL" in *"/colony/$ROUTE"*) : ;; *) fail route "not colony route: $URL" ;; esac
[ "$ERR" = "false" ] || fail error_page "colony page in error state"
printf '%s' "$H1" | grep -qi "$TOK" || fail h1 "h1 does not name colony: $H1"

ERRC="$("$ORCA" console --page "$PAGE" --limit 60 --json 2>&1 | grep -ciE '"level": *"error"' || true)"
[ "${ERRC:-0}" -eq 0 ] || fail console "$ERRC hard console errors"

echo "{\"ok\":true,\"colony\":\"$SLUG\",\"route\":\"$ROUTE\",\"sidebar_count\":\"$COUNT\",\"h1\":\"$H1\",\"url\":\"$URL\",\"hard_console_errors\":0}"
exit 0
