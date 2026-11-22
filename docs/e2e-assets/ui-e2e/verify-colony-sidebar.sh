#!/usr/bin/env bash
# UI E2E gate: a colony must appear in the sidebar AND on its colony page.
# Drives the real browser (Orca embedded). Usage: verify-colony-sidebar.sh [colony_slug]
set -uo pipefail

ORCA=/usr/local/bin/orca
BASE="${HIVE_BASE:-http://127.0.0.1:8788}"
SLUG="${1:-ui_e2e_012144}"
ROUTE_ID="$(printf '%s' "$SLUG" | sed 's/_/-/g')"   # slugToColonyId: only _ -> -

fail() { echo "FAIL: $*" >&2; exit 1; }

command -v "$ORCA" >/dev/null || fail "orca CLI not found"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/")" = "200" ] || fail "serve not up at $BASE"

# Backend must know the colony before the UI can show it.
API="$(curl -s "$BASE/api/colonies/tools-index")"
printf '%s' "$API" | grep -q "\"name\": *\"$SLUG\"" || fail "colony '$SLUG' absent from /api/colonies/tools-index"

# Fresh tab per run: a tab left on a dead SSE stream logs a spurious
# `[SSE] network error` that would false-fail the console assertion below.
P="$("$ORCA" tab create --url "$BASE/" --json | /usr/bin/python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["browserPageId"])')" || fail "could not open tab"
[ -n "$P" ] || fail "empty browserPageId"
cleanup() { "$ORCA" tab close --page "$P" --json >/dev/null 2>&1 || true; }
trap cleanup EXIT

"$ORCA" wait --page "$P" --url "$BASE/#/" --json >/dev/null 2>&1

# --- assertion 1: sidebar ---
SIDEBAR="$("$ORCA" eval --page "$P" --expression "(() => {
  const a=[...document.querySelectorAll(\"a[href*='/colony/']\")]
    .filter(el=>el.getAttribute('href').replace(/\$/,'').endsWith('/colony/$ROUTE_ID'));
  return JSON.stringify({found:a.length>0,label:a[0]?a[0].textContent.trim():null,total:document.querySelectorAll(\"a[href*='/colony/']\").length});
})()" --json | /usr/bin/python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["result"])')"

echo "sidebar: $SIDEBAR"
printf '%s' "$SIDEBAR" | grep -q '"found": *true' || fail "colony '$SLUG' NOT in sidebar (route $ROUTE_ID)"

# --- assertion 2: colony page (navigate by clicking the sidebar link) ---
"$ORCA" goto --page "$P" --url "$BASE/#/colony/$ROUTE_ID" --json >/dev/null 2>&1
PAGE="$("$ORCA" eval --page "$P" --expression "JSON.stringify({
  url:location.href,
  h1:(document.querySelector('h1')||{}).textContent?.trim()||null,
  errorPage:/not found|404|出错了|Something went wrong/i.test(document.body.innerText),
  panels:['数据','计划','自动化','工作节点'].filter(t=>document.body.innerText.includes(t)).length
})" --json | /usr/bin/python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["result"])')"

echo "page:    $PAGE"
printf '%s' "$PAGE" | grep -q "\"url\": *\"$BASE/#/colony/$ROUTE_ID\"" || fail "wrong route; got $PAGE"
printf '%s' "$PAGE" | grep -q '"errorPage": *false' || fail "colony page rendered an error state"
printf '%s' "$PAGE" | grep -q '"h1": *null' && fail "colony page has no h1"

# --- assertion 3: console clean (allowlist the known stale-SSE noise) ---
ERRS="$("$ORCA" console --page "$P" --limit 50 --json \
  | grep -o '"level": *"error"' | wc -l | tr -d ' ')"
[ "$ERRS" = "0" ] || fail "$ERRS console error(s); inspect 'orca console --page $P'"

echo "PASS: '$SLUG' present in sidebar and on colony page ($BASE/#/colony/$ROUTE_ID)"
