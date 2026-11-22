#!/usr/bin/env bash
# UI E2E gate: a colony must appear in the sidebar AND on its colony page.
#
# Design notes (why it is shaped this way):
#  * Never asserts an absolute sidebar count. Other agents create/delete
#    colonies on the same live instance; counts race. Presence only.
#  * Colony name is unique per run, so concurrent runs cannot collide and a
#    leftover from a crashed run never turns into a false PASS.
#  * Refuses to run if a colony with this name already exists.
#  * Teardown is a trap, so a failure mid-run does not leak a test colony
#    (leaked colonies inflate the sidebar and look like real usage).
set -uo pipefail

BASE="${BASE:-http://127.0.0.1:8788}"
ORCA="${ORCA:-/usr/local/bin/orca}"
STAMP="$(date +%H%M%S)"
SLUG="ui_gate_${STAMP}"                     # must stay [a-z0-9_] for DELETE
DISPLAY="Ui Gate ${STAMP}"                  # backend title-cases the slug
PAGE=""
TAB_CREATED=0
FAILS=0

say()  { printf '%s\n' "$*"; }
pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; FAILS=$((FAILS+1)); }

cleanup() {
  local code=$?
  if [[ -n "${SLUG}" ]]; then
    local status
    status=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$BASE/api/colonies/$SLUG" 2>/dev/null)
    if [[ "$status" == "200" ]]; then
      say "teardown: deleted $SLUG"
    else
      say "teardown: DELETE $SLUG -> HTTP $status (may not have been created)"
    fi
  fi
  if [[ "$TAB_CREATED" == "1" && -n "$PAGE" ]]; then
    "$ORCA" tab close --page "$PAGE" --json >/dev/null 2>&1
  fi
  exit $code
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------- preflight
if ! curl -sf -o /dev/null "$BASE/"; then
  fail "serve not reachable at $BASE"; exit 1
fi
pass "serve reachable"

if ! "$ORCA" status --json 2>/dev/null | jq -e '.result.app.running == true' >/dev/null; then
  fail "Orca not running (browser backend unavailable)"; exit 1
fi
pass "Orca runtime up"

if curl -s "$BASE/api/colonies/tools-index" | jq -e --arg n "$SLUG" '.colonies[]? | select(.name==$n)' >/dev/null 2>&1; then
  fail "colony $SLUG already exists — refusing to run (would fake a pass)"; exit 1
fi
pass "name $SLUG is unused"

# ------------------------------------------------------------ ref helpers
# Snapshot is the only source of refs; refs are invalidated by navigation,
# so every interaction re-snapshots and re-resolves by accessible name.
snap() {
  "$ORCA" snapshot --page "$PAGE" --interactive 2>/dev/null
}
# ref <grep-pattern>  -> prints ref id or empty
ref() {
  snap | grep -m1 -E "$1" | sed -n 's/.*\[ref=\(e[0-9]*\)\].*/\1/p'
}
with_retry() { # with_retry <tries> <cmd...>
  local n=$1; shift
  local i
  for ((i=1; i<=n; i++)); do
    if "$@"; then return 0; fi
    sleep 2
  done
  return 1
}

# --------------------------------------------------------------- open page
RES=$("$ORCA" tab create --url "$BASE/" --json 2>/dev/null)
PAGE=$(printf '%s' "$RES" | jq -r '.result.browserPageId // empty')
if [[ -z "$PAGE" ]]; then
  fail "could not open a browser tab"; exit 1
fi
TAB_CREATED=1
pass "tab open ($PAGE)"

with_retry 3 "$ORCA" wait --page "$PAGE" --text "蜂群" --json >/dev/null 2>&1 \
  || with_retry 3 "$ORCA" wait --page "$PAGE" --selector "a" --json >/dev/null 2>&1

# ------------------------------------------------------- create via the UI
CREATE_BTN=$(with_retry 3 bash -c "$(declare -f snap ref); PAGE='$PAGE' ORCA='$ORCA' \
  ref 'button \"(创建蜂群|Create [Cc]olony)\"'")
[[ -n "$CREATE_BTN" ]] || { fail "create-colony button not found (sidebar not rendered?)"; exit 1; }
"$ORCA" click --page "$PAGE" --element "@$CREATE_BTN" --json >/dev/null 2>&1
pass "opened create dialog"

NAME_INPUT=$(snap | grep -m1 -E 'textbox' | sed -n 's/.*\[ref=\(e[0-9]*\)\].*/\1/p')
[[ -n "$NAME_INPUT" ]] || { fail "colony name input not found"; exit 1; }
"$ORCA" fill --page "$PAGE" --element "@$NAME_INPUT" --value "$SLUG" --json >/dev/null 2>&1
pass "filled name $SLUG"

QUEEN=$(snap | grep -m1 -E 'button "Steve Jobs' | sed -n 's/.*\[ref=\(e[0-9]*\)\].*/\1/p')
[[ -n "$QUEEN" ]] || { fail "queen picker entry (Steve Jobs) not found"; exit 1; }
"$ORCA" click --page "$PAGE" --element "@$QUEEN" --json >/dev/null 2>&1
pass "selected queen Steve Jobs"

SUBMIT=$(snap | grep -m1 -E 'button "(创建|Create)"' | sed -n 's/.*\[ref=\(e[0-9]*\)\].*/\1/p')
[[ -n "$SUBMIT" ]] || { fail "submit button not found"; exit 1; }
"$ORCA" click --page "$PAGE" --element "@$SUBMIT" --json >/dev/null 2>&1

if with_retry 6 "$ORCA" wait --page "$PAGE" --url "/colony/" --json >/dev/null 2>&1; then
  pass "create navigated to colony route"
else
  fail "create did not navigate to a colony route"
fi

# ------------------------------------------------------- ASSERT 1: colony page
URL=$("$ORCA" tab current --json 2>/dev/null | jq -r '.result.tab.url // .result.url // empty')
case "$URL" in
  *"/colony/ui-gate-${STAMP}"*) pass "route id is ui-gate-${STAMP}" ;;
  *) fail "unexpected route: $URL" ;;
esac

H1=$("$ORCA" eval --page "$PAGE" --expression \
  "JSON.stringify({h1:[...document.querySelectorAll('h1,h2')].map(e=>e.textContent.trim()),err:/not found|404|Something went wrong|出错了/i.test(document.body.innerText)})" \
  --json 2>/dev/null | jq -r '.result.result // empty')
if [[ -n "$H1" ]]; then
  echo "$H1" | grep -q "$DISPLAY" && pass "colony page h1 = '$DISPLAY'" \
    || fail "colony page h1 missing '$DISPLAY' (got: $H1)"
  echo "$H1" | grep -q '"err":false' && pass "colony page is not an error page" \
    || fail "colony page rendered an error"
else
  fail "could not read colony page (eval timeout)"
fi

# ------------------------------------------------------- ASSERT 2: sidebar
"$ORCA" goto --page "$PAGE" --url "$BASE/" --json >/dev/null 2>&1
FOUND=0
for _ in $(seq 1 10); do
  if snap | grep -q "$DISPLAY"; then FOUND=1; break; fi
  sleep 2
done
if [[ "$FOUND" == "1" ]]; then
  pass "sidebar lists '$DISPLAY'"
else
  fail "sidebar does not list '$DISPLAY'"
fi

# --------------------------------------------------- cross-check + disk
if curl -s "$BASE/api/colonies/tools-index" | jq -e --arg n "$SLUG" '.colonies[]? | select(.name==$n)' >/dev/null 2>&1; then
  pass "API tools-index reports $SLUG"
else
  fail "API tools-index does not report $SLUG"
fi
if [[ -d "$HOME/.hive-go/colonies/$SLUG" ]]; then
  pass "disk directory present"
else
  fail "disk directory missing"
fi

say ""
if [[ "$FAILS" -eq 0 ]]; then
  say "RESULT: PASS (colony $SLUG in sidebar and on colony page)"
  exit 0
else
  say "RESULT: FAIL ($FAILS assertion(s) failed)"
  exit 1
fi
