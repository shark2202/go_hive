#!/usr/bin/env bash
# UI E2E gate — a colony MUST appear in the sidebar AND on its colony page.
#
# Usage: verify-colony-ui.sh <colony_disk_name> [base_url]
# Exit:  0 = PASS ; non-zero = FAIL (failing assertion on stderr)
#
# Drives Orca's embedded browser. That is the working path on this box: the
# hive-browser bridge extension is NOT installed in the user's Chrome, so
# `hive-browser` reports connected:false. Never launch/kill Chrome to "fix" it.
#
# Two hardening rules learned the hard way:
#  1. Every Orca call is wrapped in a hard timeout. When Orca is busy it
#     answers "could not connect to Orca" and an unguarded call hangs forever.
#  2. The fresh tab is closed on EXIT — including SIGINT/SIGTERM. An earlier
#     version trapped only after the tab was created, so a timeout mid-run
#     leaked orphan tabs.
#
# The gate opens its OWN tab, so a stale tab left on an old colony (which
# throws one `[SSE] error ... network error` after a backend restart) cannot
# pollute the run.
set -uo pipefail

COLONY="${1:?usage: verify-colony-ui.sh <colony_disk_name> [base_url]}"
BASE="${2:-http://127.0.0.1:8788}"
ROUTE_ID="${COLONY//_/-}"          # slugToColonyId: only "_" -> "-"
ORCA="${ORCA:-/usr/local/bin/orca}"
PY=/usr/bin/python3
PAGE=""
CALL_TIMEOUT="${CALL_TIMEOUT:-25}"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok  $*"; }

# hard timeout (no coreutils `timeout` on this box; perl alarm works)
tmo() { local s="$1"; shift; perl -e 'alarm shift; exec @ARGV' "$s" "$@"; }

# orca with timeout + retries. Orca is intermittently busy: when it is, it
# either answers `runtime_unavailable` ("could not connect to Orca") OR the
# call runs long enough that the timeout wrapper kills it and we get EMPTY
# output. Both mean "retry", so retry on either. Empty is never a legitimate
# answer from the subcommands used below.
orca() {
  local attempt out
  for attempt in 1 2 3 4; do
    out=$(tmo "$CALL_TIMEOUT" "$ORCA" "$@" 2>&1)
    case "$out" in
      "") : ;;                                   # timed out -- retry
      *"could not connect to Orca"*) : ;;        # runtime_unavailable -- retry
      *) printf '%s' "$out"; return 0 ;;
    esac
    sleep $((attempt * 4))
  done
  printf '%s' "$out"                            # last attempt; let caller fail
}

cleanup() {
  if [ -n "$PAGE" ]; then
    tmo 15 "$ORCA" tab close --page "$PAGE" --json >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

[ -x "$ORCA" ] || fail "orca CLI not found at $ORCA"
STATUS=$(orca status --json) || true
case "$STATUS" in
  *'"reachable": true'*) ok_pending=1 ;;
  *) fail "Orca is not reachable (run: orca status --json) -- rc=$? out=${STATUS:0:120}" ;;
esac

# --- backend truth: the name the sidebar should actually render ------------
DISCOVER=$(tmo 20 curl -sf "$BASE/api/discover") || fail "cannot reach $BASE/api/discover"
EXPECTED_NAME=$(printf '%s' "$DISCOVER" | "$PY" -c "
import json,sys
d=json.load(sys.stdin)
for grp in d.values():
    if not isinstance(grp,list): continue
    for c in grp:
        if isinstance(c,dict) and c.get('path','').rstrip('/').endswith('/colonies/$COLONY'):
            print(c.get('name','')); raise SystemExit
" 2>/dev/null) || true
[ -n "${EXPECTED_NAME:-}" ] || fail "colony '$COLONY' absent from /api/discover"
echo "colony=$COLONY  route_id=$ROUTE_ID  name='$EXPECTED_NAME'  base=$BASE"

# --- fresh tab -------------------------------------------------------------
TAB_JSON=$(orca tab create --url "$BASE/" --json) || fail "orca tab create failed"
PAGE=$(printf '%s' "$TAB_JSON" | "$PY" -c "
import json,sys,re
m=re.search(r'\{.*\}', sys.stdin.read(), re.S)
d=json.loads(m.group(0)) if m else {}
print(d.get('result',{}).get('browserPageId',''))
" 2>/dev/null)
[ -n "${PAGE:-}" ] || fail "no browserPageId from orca tab create: $TAB_JSON"
echo "tab=$PAGE"

# eval_js: run JS on the page, print the returned value
eval_js() {
  local r; r=$(orca eval --page "$PAGE" --expression "$1" --json)
  printf '%s' "$r" | "$PY" -c "
import json,sys,re
s=sys.stdin.read()
m=re.search(r'\{.*\}', s, re.S)
d=json.loads(m.group(0)) if m else {}
print(d.get('result',{}).get('result',''))
" 2>/dev/null
}

# --- ASSERT 1: sidebar lists the colony ------------------------------------
# Colonies arrive from an async fetch (/discover + /sessions). Block on the
# exact href rather than sleeping a fixed beat: the SPA fills the rail after
# mount, and a fixed sleep raced it on a cold tab.
if tmo 40 "$ORCA" wait --page "$PAGE" --selector "a[href='/colony/$ROUTE_ID']" >/dev/null 2>&1; then
  ok "sidebar has link /colony/$ROUTE_ID"
else
  LINKS=$(eval_js "JSON.stringify([...document.querySelectorAll(\"a[href*='/colony/']\")].map(a=>a.getAttribute('href')))")
  fail "sidebar missing /colony/$ROUTE_ID ; links=$LINKS"
fi
SIDE_TEXT=$(eval_js "document.body.innerText")
case "$SIDE_TEXT" in
  *"$EXPECTED_NAME"*) ok "sidebar renders name '$EXPECTED_NAME'" ;;
  *) fail "sidebar does not render '$EXPECTED_NAME'" ;;
esac

# --- ASSERT 2: the colony page renders it ----------------------------------
orca goto --page "$PAGE" --url "$BASE/#/colony/$ROUTE_ID" --json >/dev/null || fail "goto colony page failed"
tmo 25 tmo 40 "$ORCA" wait --page "$PAGE" --selector "h1" >/dev/null 2>&1 || true
H1=$(eval_js "document.querySelector('h1')?.textContent.trim()||''")
[ "$H1" = "$EXPECTED_NAME" ] && ok "colony page h1 = '$H1'" || fail "colony page h1 '$H1' != '$EXPECTED_NAME'"

ERRPAGE=$(eval_js "/not found|404|Something went wrong|出错了/i.test(document.body.innerText)?'1':'0'")
[ "$ERRPAGE" = "0" ] && ok "colony page is not an error page" || fail "colony page rendered an error state"

# --- ASSERT 3: page reads backend state, not frontend optimism -------------
API_TS=$(tmo 20 curl -sf "$BASE/api/colonies/tools-index" | "$PY" -c "
import json,sys
d=json.load(sys.stdin)
for c in d.get('colonies',[]):
    if c.get('name')=='$COLONY':
        print(repr(c.get('created_at'))); raise SystemExit
" 2>/dev/null) || true
DISK_TS=$("$PY" -c "import json;print(repr(json.load(open('$HOME/.hive-go/colonies/$COLONY/metadata.json'))['created_at']))" 2>/dev/null) || true
[ -n "$API_TS" ] && ok "tools-index has '$COLONY' (created_at=$API_TS)" || fail "'$COLONY' absent from /api/colonies/tools-index"
[ "$API_TS" = "$DISK_TS" ] && ok "created_at matches disk metadata ($DISK_TS)" || fail "created_at drift: api=$API_TS disk=$DISK_TS"

echo "PASS: $COLONY appears in the sidebar and on its colony page."
