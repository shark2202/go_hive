#!/usr/bin/env bash
# UI E2E gate — a colony MUST appear in the sidebar AND on its colony page.
#
# Usage: verify-colony-ui.sh <colony_disk_name> [base_url]
# Exit:  0 = PASS ; non-zero = FAIL (failing assertion printed to stderr)
#
# Drives Orca's embedded browser. That is the working path on this box: the
# hive-browser bridge extension is NOT installed in the user's Chrome, so
# `hive-browser` reports connected:false. Never launch/kill Chrome to "fix" it.
#
# The gate opens its OWN fresh tab and closes it on exit. A stale tab left on
# an old colony throws one `[SSE] error ... network error` when the backend
# restarts; a fresh tab keeps that noise out of the gate.
set -uo pipefail

COLONY="${1:?usage: verify-colony-ui.sh <colony_disk_name> [base_url]}"
BASE="${2:-http://127.0.0.1:8788}"
ROUTE_ID="${COLONY//_/-}"          # slugToColonyId: only "_" -> "-"
ORCA="${ORCA:-/usr/local/bin/orca}"
PY=/usr/bin/python3

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok  $*"; }

[ -x "$ORCA" ] || fail "orca CLI not found at $ORCA"

# --- backend truth: the name the sidebar should actually render ------------
DISCOVER=$(curl -sf "$BASE/api/discover") || fail "cannot reach $BASE/api/discover"
EXPECTED_NAME=$(printf '%s' "$DISCOVER" | "$PY" -c "
import json,sys
d=json.load(sys.stdin)
for grp in d.values():
    for c in grp:
        if c.get('path','').rstrip('/').endswith('/colonies/$COLONY'):
            print(c.get('name','')); raise SystemExit
") || true
[ -n "${EXPECTED_NAME:-}" ] || fail "colony '$COLONY' absent from /api/discover"
echo "colony=$COLONY  route_id=$ROUTE_ID  name='$EXPECTED_NAME'  base=$BASE"

# --- fresh tab -------------------------------------------------------------
TAB_JSON=$("$ORCA" tab create --url "$BASE/" --json) || fail "orca tab create failed"
PAGE=$(printf '%s' "$TAB_JSON" | "$PY" -c "import json,sys;print(json.load(sys.stdin)['result']['browserPageId'])" 2>/dev/null)
[ -n "${PAGE:-}" ] || fail "no browserPageId from orca tab create"
cleanup() { "$ORCA" tab close --page "$PAGE" --json >/dev/null 2>&1 || true; }
trap cleanup EXIT
echo "tab=$PAGE"

# eval_js: run JS, print the returned string
eval_js() {
  "$ORCA" eval --page "$PAGE" --expression "$1" --json \
    | "$PY" -c "import json,sys;print(json.load(sys.stdin)['result']['result'])"
}

# --- ASSERT 1: sidebar lists the colony ------------------------------------
"$ORCA" wait --page "$PAGE" --selector "a[href*='/colony/']" >/dev/null 2>&1
sleep 1
LINKS=$(eval_js "JSON.stringify([...document.querySelectorAll(\"a[href*='/colony/']\")].map(a=>a.getAttribute('href')))") || fail "eval failed (sidebar)"
case "$LINKS" in
  *"/colony/$ROUTE_ID"*) ok "sidebar has link /colony/$ROUTE_ID" ;;
  *) fail "sidebar missing /colony/$ROUTE_ID ; links=$LINKS" ;;
esac
SIDE_TEXT=$(eval_js "document.body.innerText")
case "$SIDE_TEXT" in
  *"$EXPECTED_NAME"*) ok "sidebar renders name '$EXPECTED_NAME'" ;;
  *) fail "sidebar does not render '$EXPECTED_NAME'" ;;
esac

# --- ASSERT 2: the colony page renders it ----------------------------------
"$ORCA" goto --page "$PAGE" --url "$BASE/#/colony/$ROUTE_ID" --json >/dev/null || fail "goto colony page failed"
"$ORCA" wait --page "$PAGE" --selector "h1" >/dev/null 2>&1
sleep 1
H1=$(eval_js "document.querySelector('h1')?.textContent.trim()||''")
[ "$H1" = "$EXPECTED_NAME" ] && ok "colony page h1 = '$H1'" || fail "colony page h1 '$H1' != '$EXPECTED_NAME'"

ERRPAGE=$(eval_js "/not found|404|Something went wrong|出错了/i.test(document.body.innerText)?'1':'0'")
[ "$ERRPAGE" = "0" ] && ok "colony page is not an error page" || fail "colony page rendered an error state"

# --- ASSERT 3: page is reading backend state, not frontend optimism --------
API_ROW=$(curl -sf "$BASE/api/colonies/tools-index" | "$PY" -c "
import json,sys
d=json.load(sys.stdin)
for c in d.get('colonies',[]):
    if c.get('name')=='$COLONY':
        print(repr(c.get('created_at'))); raise SystemExit
" 2>/dev/null) || true
DISK_TS=$("$PY" -c "import json;print(repr(json.load(open('$HOME/.hive-go/colonies/$COLONY/metadata.json'))['created_at']))" 2>/dev/null) || true
[ -n "$API_ROW" ] && ok "tools-index has '$COLONY' (created_at=$API_ROW)" || fail "'$COLONY' absent from /api/colonies/tools-index"
[ "$API_ROW" = "$DISK_TS" ] && ok "created_at matches disk metadata ($DISK_TS)" || fail "created_at drift: api=$API_ROW disk=$DISK_TS"

echo "PASS: $COLONY appears in the sidebar and on its colony page."
</SCRIPT_EOF
chmod +x /Users/mac/hive/go_hive/docs/e2e-assets/ui-e2e/verify-colony-ui.sh
echo "--- syntax check ---"; bash -n /Users/mac/hive/go_hive/docs/e2e-assets/ui-e2e/verify-colony-ui.sh && echo "syntax OK"
