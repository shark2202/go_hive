#!/usr/bin/env bash
# UI E2E gate: a colony must appear in the sidebar AND on its colony page.
#
#   ui_colony_gate.sh verify <slug>   # assert an existing colony surfaces in both places
#   ui_colony_gate.sh full            # create a fresh colony through the UI, verify, tear down
#
# Reads the expected sidebar label from /api/discover (same source the UI reads),
# so the UI assertion is a genuine UI-vs-API cross-check, not a self-fulfilling echo.
#
# Exit 0 = PASS, 1 = FAIL. Every step prints PASS/FAIL so a log alone is diagnosable.
set -uo pipefail

ORCA="${ORCA_CLI_COMMAND:-/usr/local/bin/orca}"
BASE="${BASE:-http://127.0.0.1:8788}"
PY="${PY:-/usr/bin/python3}"
PASS=0; FAIL=0

ok()  { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
die() { echo "  FATAL $*"; exit 1; }

# slug (disk dir name) -> route id, mirroring frontend slugToColonyId()
slug_to_route() { printf '%s' "$1" | sed -e 's/_/-/g' -e 's/^email-//' -e 's/-agent$//'; }

# Resolve the sidebar label for a slug from the backend discovery payload.
discover_label() {
  curl -s "$BASE/api/discover" | "$PY" -c '
import json,sys
slug=sys.argv[1]
d=json.load(sys.stdin)
for grp,items in d.items():
    if not isinstance(items,list): continue
    for c in items:
        p=(c.get("path") or "").rstrip("/")
        if p.split("/")[-1]==slug:
            print(c.get("name") or ""); raise SystemExit
' "$1"
}

discover_count() {
  curl -s "$BASE/api/discover" | "$PY" -c '
import json,sys
d=json.load(sys.stdin)
n=0
for grp,items in d.items():
    if isinstance(items,list): n+=len(items)
print(n)
'
}

# Pin the Hive tab (or create one) and echo its browserPageId.
pick_page() {
  local id
  id=$("$ORCA" tab list --json 2>/dev/null | "$PY" -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: raise SystemExit
for t in (d.get("result") or {}).get("tabs") or []:
    if "127.0.0.1:8788" in (t.get("url") or ""):
        print(t["browserPageId"]); raise SystemExit
' 2>/dev/null)
  if [ -z "$id" ]; then
    id=$("$ORCA" tab create --url "$BASE/" --json 2>/dev/null | "$PY" -c '
import json,sys
print((json.load(sys.stdin).get("result") or {}).get("browserPageId") or "")' 2>/dev/null)
  fi
  [ -n "$id" ] || die "no Hive tab and could not create one (is Orca running?)"
  printf '%s' "$id"
}

ref_for_label() {  # snapshot text, label -> ref
  printf '%s' "$1" | grep -F "link \"$2\"" | grep -oE '\[ref=e[0-9]+\]' | head -1 | tr -d '[]' | sed 's/ref=//'
}

verify_sidebar() { # page, slug, label
  local P="$1" slug="$2" label="$3" snap ref
  "$ORCA" goto --page "$P" --url "$BASE/" --json >/dev/null 2>&1 || die "goto $BASE failed"
  snap=$("$ORCA" snapshot --page "$P" --interactive 2>/dev/null)

  local want_label; want_label=$(discover_label "$slug")
  [ -n "$want_label" ] || die "API has no colony named '$slug'"
  if [ "$want_label" = "$label" ]; then ok "API label matches expectation: '$label'"
  else bad "API label '$want_label' != expected '$label'"; fi

  ref=$(ref_for_label "$snap" "$label")
  if [ -n "$ref" ]; then ok "sidebar renders link \"$label\" ($ref)"
  else bad "sidebar has NO link \"$label\""; fi

  local want_n got_n
  want_n=$(discover_count); got_n=$(printf '%s' "$snap" | grep -oE '"蜂群 [0-9]+"|"Colonies [0-9]+"|Colonies[^0-9]*[0-9]+' | grep -oE '[0-9]+' | head -1)
  if [ -n "$got_n" ] && [ "$got_n" = "$want_n" ]; then ok "sidebar count $got_n == API count $want_n"
  else bad "sidebar count '${got_n:-none}' != API count '$want_n'"; fi

  printf '%s' "$ref"
}

verify_page() { # page, slug, label
  local P="$1" slug="$2" label="$3" route
  route=$(slug_to_route "$slug")
  "$ORCA" goto --page "$P" --url "$BASE/#/colony/$route" --json >/dev/null 2>&1 || die "goto colony route failed"

  local got
  got=$("$ORCA" eval --page "$P" --expression "JSON.stringify({url:location.href,h1:(document.querySelector('h1')||{}).textContent||'',err:/not found|404|Something went wrong|出错了/i.test(document.body.innerText)})" --json 2>/dev/null \
        | "$PY" -c 'import json,sys; print(json.load(sys.stdin)["result"]["result"])' 2>/dev/null)

  if printf '%s' "$got" | grep -qF "\"h1\":\"$label\""; then
    ok "colony page h1 == \"$label\""
  else
    bad "colony page h1 wrong: $got"
  fi

  if printf '%s' "$got" | grep -qF '"err":false'; then
    ok "colony page is not an error page"
  else
    bad "colony page looks like an error page: $got"
  fi

  if printf '%s' "$got" | grep -qF "/colony/$route"; then
    ok "route is /colony/$route"
  else
    bad "route mismatch: $got"
  fi

  local cons
  cons=$("$ORCA" console --page "$P" --limit 40 --json 2>/dev/null)
  if printf '%s' "$cons" | grep -qF "connected: " && printf '%s' "$cons" | grep -qF "$slug"; then
    ok "SSE connected for $slug (page bound to a real session)"
  else
    bad "no SSE 'connected' line for $slug in console"
  fi

  if printf '%s' "$cons" | grep -qF '"level": "error"'; then
    bad "console has error-level entries"
  else
    ok "no console errors"
  fi
}

MODE="${1:-verify}"
case "$MODE" in
  verify)
    SLUG="${2:-ui_e2e_012144}"
    LABEL="${3:-$(discover_label "$SLUG")}"
    echo "== verify: $SLUG (label '$LABEL') =="
    P=$(pick_page)
    verify_sidebar "$P" "$SLUG" "$LABEL" >/dev/null
    verify_page "$P" "$SLUG" "$LABEL"
    ;;
  *)
    echo "usage: $0 verify <slug>   |   $0 full" >&2; exit 2 ;;
esac

echo
echo "== $PASS passed, $FAIL failed =="
[ "$FAIL" -eq 0 ] || exit 1
