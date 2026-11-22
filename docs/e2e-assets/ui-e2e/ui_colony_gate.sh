#!/usr/bin/env bash
# UI E2E gate: a colony must appear in the sidebar AND on its colony page.
#
#   ui_colony_gate.sh verify <slug>    # assert an existing colony surfaces in both places
#
# Exit 0 = PASS, 1 = FAIL. Every assertion prints PASS/FAIL so a log alone is diagnosable.
#
# Notes learned the hard way:
#  * Hive is an SPA holding an open SSE stream, so `orca goto` never sees a load
#    event and returns browser_navigation_failed ~30s in, while the page is in
#    fact fine. Navigation is therefore "goto, tolerate the error, then wait on
#    the URL" — the URL is the source of truth, not the load event.
#  * The colony page is reached by CLICKING the sidebar link, not by goto. That
#    is the user path, and it is what the sidebar assertion actually claims.
#  * Labels come from GET /api/discover, the same payload the UI renders, so the
#    check is UI-vs-API, not an echo of its own expectation.
set -uo pipefail

ORCA="${ORCA_CLI_COMMAND:-/usr/local/bin/orca}"
BASE="${BASE:-http://127.0.0.1:8788}"
PY="${PY:-/usr/bin/python3}"
PASS=0; FAIL=0

ok()  { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
die() { echo "  FATAL $*"; exit 1; }

slug_to_route() { printf '%s' "$1" | sed -e 's/_/-/g' -e 's/^email-//' -e 's/-agent$//'; }

discover_json() { curl -s --max-time 15 "$BASE/api/discover" || die "GET /api/discover failed"; }

discover_label() {
  discover_json | "$PY" -c '
import json,sys
slug=sys.argv[1]
d=json.load(sys.stdin)
for _,items in d.items():
    if not isinstance(items,list): continue
    for c in items:
        if (c.get("path") or "").rstrip("/").split("/")[-1]==slug:
            print(c.get("name") or ""); raise SystemExit
' "$1"
}

discover_count() {
  discover_json | "$PY" -c '
import json,sys
d=json.load(sys.stdin)
print(sum(len(v) for v in d.values() if isinstance(v,list)))'
}

fresh_page() {
  "$ORCA" tab create --url "$BASE/" --json 2>/dev/null \
    | "$PY" -c 'import json,sys
try: print((json.load(sys.stdin).get("result") or {}).get("browserPageId") or "")
except Exception: print("")'
}

current_url() {
  "$ORCA" tab current --json 2>/dev/null | "$PY" -c '
import json,sys
try: print(((json.load(sys.stdin).get("result") or {}).get("tab") or {}).get("url") or "")
except Exception: print("")'
}

# goto that tolerates the SPA's missing load event, then confirms via URL.
wait_text() { # page, text, timeout_s
  "$ORCA" wait --page "$1" --text "$2" --json >/dev/null 2>&1
}

ref_for_label() { printf '%s' "$1" | grep -F "link \"$2\"" | grep -oE '\[ref=e[0-9]+\]' | head -1 | tr -d '[]' | sed 's/^ref=//'; }

MODE="${1:-verify}"
[ "$MODE" = "verify" ] || { echo "usage: $0 verify <slug>" >&2; exit 2; }

SLUG="${2:-ui_e2e_012144}"
LABEL=$(discover_label "$SLUG")
[ -n "$LABEL" ] || die "API has no colony named '$SLUG'"
ROUTE=$(slug_to_route "$SLUG")
P=$(fresh_page)
[ -n "$P" ] || die "could not open a Hive tab (is Orca running?)"

echo "== UI colony gate: $SLUG =="
echo "   label '$LABEL'   route /colony/$ROUTE"
echo "   fresh tab $P (mount-time fetchColonies)"
echo
echo "-- sidebar --"

# A fresh tab mounts the app and calls fetchColonies(); waiting for the label
# text proves the sidebar entry came from a real backend fetch, not stale state.
if wait_text "$P" "$LABEL" 45; then ok "sidebar shows \"$LABEL\" after a fresh fetch"
else bad "sidebar never showed \"$LABEL\" within 45s of a fresh load"; fi

SNAP=$("$ORCA" snapshot --page "$P" --interactive 2>/dev/null)
REF=$(ref_for_label "$SNAP" "$LABEL")
if [ -n "$REF" ]; then ok "sidebar link \"$LABEL\" is present ($REF)"
else bad "sidebar has NO link \"$LABEL\""; fi

GOT_N=$(printf '%s' "$SNAP" | grep -oE '蜂群 [0-9]+|Colonies [0-9]+' | grep -oE '[0-9]+' | head -1)
echo "  INFO  sidebar heading count=${GOT_N:-none}; /api/discover total=$(discover_count)"
echo "  INFO  (counts are not asserted: the sidebar renders a deduped, filtered subset)"
echo

echo "-- colony page (via sidebar click) --"

if [ -z "$REF" ]; then
  bad "skipping click/h1 assertions: no sidebar link to click"
else
  "$ORCA" click --page "$P" --element "@$REF" --json >/dev/null 2>&1
  wait_text "$P" "$LABEL" 20

  CUR=$(current_url)
  case "$CUR" in
    *"/colony/$ROUTE"*) ok "click navigated to /colony/$ROUTE" ;;
    *) bad "click did not reach /colony/$ROUTE (url '$CUR')" ;;
  esac

  GOT=$("$ORCA" eval --page "$P" --expression "JSON.stringify({h1:(document.querySelector('h1')||{}).textContent||'',err:/not found|404|Something went wrong|出错了/i.test(document.body.innerText),panels:[...document.querySelectorAll('button')].map(b=>b.textContent.trim()).filter(t=>/^(数据|计划|自动化|工作节点|Data|Plan|Automation|Workers)$/.test(t))})" --json 2>/dev/null \
        | "$PY" -c 'import json,sys; print(json.load(sys.stdin)["result"]["result"])' 2>/dev/null)

  if printf '%s' "$GOT" | grep -qF "\"h1\":\"$LABEL\""; then ok "colony page h1 == \"$LABEL\""
  else bad "colony page h1 wrong: ${GOT:-<no eval result>}"; fi

  if printf '%s' "$GOT" | grep -qF '"err":false'; then ok "colony page is not an error page"
  else bad "colony page looks like an error page"; fi

  if printf '%s' "$GOT" | grep -qE '"panels":\[[^]]+\]'; then ok "colony panel tabs rendered"
  else bad "colony panel tabs missing"; fi

  CONS=$("$ORCA" console --page "$P" --limit 40 --json 2>/dev/null)
  if printf '%s' "$CONS" | grep -qF "connected: " && printf '%s' "$CONS" | grep -qF "$SLUG"; then
    ok "SSE connected for $SLUG (page bound to a real session)"
  else bad "no SSE 'connected' line for $SLUG in console"; fi
fi

echo
echo "-- cross-check --"
API_TS=$(discover_json | "$PY" -c '
import json,sys
slug=sys.argv[1]
d=json.load(sys.stdin)
for _,items in d.items():
    if not isinstance(items,list): continue
    for c in items:
        if (c.get("path") or "").rstrip("/").split("/")[-1]==slug: print(c.get("created_at")); raise SystemExit
' "$SLUG")
DISK_TS=$( [ -f "$HOME/.hive-go/colonies/$SLUG/metadata.json" ] && "$PY" -c 'import json,sys; print(json.load(open(sys.argv[1]))["created_at"])' "$HOME/.hive-go/colonies/$SLUG/metadata.json" || echo "")
if [ -n "$DISK_TS" ] && [ "$API_TS" = "$DISK_TS" ]; then ok "API created_at == disk metadata ($API_TS)"
else bad "created_at mismatch: API '${API_TS:-none}' vs disk '${DISK_TS:-none}'"; fi

echo
echo "== $PASS passed, $FAIL failed =="
[ "$FAIL" -eq 0 ] || exit 1
