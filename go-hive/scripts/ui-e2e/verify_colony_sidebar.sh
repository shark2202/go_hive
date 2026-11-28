#!/usr/bin/env bash
# UI E2E gate: a colony must appear in the sidebar AND on its colony page.
#
# Drives Orca's embedded browser against a live `hive-embed serve`. Every
# assertion reads the rendered DOM — the point is to prove the UI reflects
# backend state, not that an API returns a row.
#
# Concurrency notes (learned the hard way):
#   * The Orca browser is SHARED. Other agents leave tabs mid-navigation, so a
#     fixed `sleep` after goto is not a readiness signal. This script polls for
#     a ready DOM instead, and opens its OWN tab so it never fights over one.
#   * Never use a global scratch path for logs; use --log with a private path.
#
# Usage:
#   ./verify_colony_sidebar.sh <colony_slug> [--base URL] [--page ID]
#                              [--shots DIR] [--log FILE] [--keep-tab]
#
#   colony_slug   on-disk colony dir name, e.g. ui_e2e_012144
#                 (route id derived "_" -> "-", matching slugToColonyId)
#
# Exit: 0 = all assertions pass, 1 = a UI assertion failed, 2 = harness problem.
set -uo pipefail

ORCA="${ORCA_CLI_COMMAND:-orca}"
BASE="http://127.0.0.1:8788"
PAGE=""
KEEP_TAB=0
SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SHOTS="$(cd "$SELF/../../.." && pwd)/docs/e2e-assets/ui-e2e"
LOG="/tmp/queen_growth_ui_e2e/gate.log"
SLUG=""
READY_TIMEOUT=25

while [ $# -gt 0 ]; do
  case "$1" in
    --base)  BASE="$2";  shift 2 ;;
    --page)  PAGE="$2";  shift 2 ;;
    --shots) SHOTS="$2"; shift 2 ;;
    --log)   LOG="$2";   shift 2 ;;
    --timeout) READY_TIMEOUT="$2"; shift 2 ;;
    --keep-tab) KEEP_TAB=1; shift ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) SLUG="$1"; shift ;;
  esac
done

[ -n "$SLUG" ] || { echo "FATAL: colony slug required" >&2; exit 2; }
mkdir -p "$SHOTS" "$(dirname "$LOG")"

# slugToColonyId: only "_" -> "-" (no title-casing).
CID="${SLUG//_/-}"
fail=0
OWN_TAB=""
cleanup() {
  if [ "$KEEP_TAB" -eq 0 ] && [ -n "$OWN_TAB" ]; then
    "$ORCA" tab close --page "$OWN_TAB" >/dev/null 2>&1
  fi
}
trap cleanup EXIT

note() { printf '  %-44s %s\n' "$1" "$2"; }
pass() { note "$1" "PASS  $2"; }
bad()  { note "$1" "FAIL  $2"; fail=1; }

jq_get() { /usr/bin/python3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: print(''); sys.exit(0)
cur=d
for k in sys.argv[1].split('.'):
    if isinstance(cur,list):
        try: cur=cur[int(k)]
        except Exception: print(''); sys.exit(0)
    if not isinstance(cur,dict) or k not in cur: print(''); sys.exit(0)
    cur=cur[k]
print('' if cur is None else cur)
" "$1" 2>/dev/null; }

# orca_eval <js> -> result.result, retrying transient transport hiccups.
# A hung/slow page can make one eval take ~30s; keep the retry budget small.
orca_eval() {
  local js="$1" out=""
  for _ in 1 2 3; do
    out=$("$ORCA" eval --page "$PAGE" --expression "$js" --json 2>/dev/null | jq_get 'result.result')
    [ -n "$out" ] && { printf '%s' "$out"; return 0; }
    sleep 2
  done
}

# wait_ready <js-returns-truthy-string> <label> — poll instead of sleeping.
wait_ready() {
  local js="$1" deadline=$((SECONDS + READY_TIMEOUT))
  while [ "$SECONDS" -lt "$deadline" ]; do
    local v; v=$(orca_eval "$js")
    [ -n "$v" ] && { printf '%s' "$v"; return 0; }
    sleep 1
  done
  return 1
}

find_page() {
  "$ORCA" tab list --json 2>/dev/null | /usr/bin/python3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(0)
for t in (d.get('result') or {}).get('tabs') or []:
    u=t.get('url') or ''
    if '127.0.0.1:8788' in u or 'localhost:8788' in u:
        print(t.get('browserPageId') or ''); break
" 2>/dev/null
}

# --- resolve a browser page ---------------------------------------------------
# `orca tab list` is intermittently slow/failing right after the runtime wakes.
# Retry before giving up — a transport hiccup must not read as a UI failure.
if [ -z "$PAGE" ]; then
  for a in 1 2 3 4 5; do
    PAGE=$(find_page); [ -n "$PAGE" ] && break; sleep "$a"
  done
fi
# No reusable page (or the caller wants isolation): open a dedicated tab so we
# don't navigate a tab another agent is mid-way through.
if [ -z "$PAGE" ]; then
  for a in 1 2 3; do
    PAGE=$("$ORCA" tab create --url "$BASE/" --json 2>/dev/null | jq_get 'result.browserPageId')
    [ -n "$PAGE" ] && break; sleep $((a+1))
  done
  OWN_TAB="$PAGE"
fi
[ -n "$PAGE" ] || { echo "FATAL: no browser page — Orca unreachable after retries" >&2; exit 2; }

{
echo "UI E2E — colony in sidebar + colony page"
echo "  slug   : $SLUG"
echo "  colony : $CID"
echo "  base   : $BASE"
echo "  page   : $PAGE"
echo
} | tee -a "$LOG"

# --- 0. cold load from root ---------------------------------------------------
# Start at / so the sidebar runs a full discover+sessions fetch rather than
# reusing a cached colony list. Then WAIT for a rendered sidebar — a stale or
# still-navigating page is the #1 false-positive/negative source here.
"$ORCA" goto --page "$PAGE" --url "$BASE/" --json >/dev/null 2>&1

SIDE=$(wait_ready '(() => {
  const links = [...document.querySelectorAll("a[href*=\"/colony/\"]")];
  const counter = [...document.querySelectorAll("button")]
    .map(b => (b.textContent || "").trim())
    .find(t => /^蜂群\s*\d+$/.test(t) || /^Colon(y|ies)\s*\d+$/i.test(t)) || "";
  if (!links.length || !counter) return "";
  return JSON.stringify({ links: links.map(a => a.getAttribute("href")),
                          counter, counterNum: (counter.match(/(\d+)\s*$/) || [, ""])[1] });
})()' "sidebar ready")

if [ -z "$SIDE" ]; then
  bad "sidebar: rendered" "no colony links+counter within ${READY_TIMEOUT}s"
  echo; echo "RESULT: FAIL"; exit 1
fi

# --- 1. sidebar: colony link present -----------------------------------------
if printf '%s' "$SIDE" | grep -q "colony/$CID"; then
  pass "sidebar: colony link present" "$CID"
else
  bad "sidebar: colony link present" "$CID not in sidebar"
fi

# --- 2. sidebar: counter is honest -------------------------------------------
CNT=$(printf '%s' "$SIDE" | /usr/bin/python3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: print('0 0'); sys.exit(0)
links={a for a in d.get('links',[]) if a}
print(len(links), d.get('counterNum','') or 0)" 2>/dev/null)
EXPECT_N=$(printf '%s' "$CNT" | cut -d' ' -f1)
GOT_N=$(printf '%s' "$CNT" | cut -d' ' -f2)
if [ -n "$GOT_N" ] && [ "$EXPECT_N" = "$GOT_N" ] && [ "$GOT_N" != "0" ]; then
  pass "sidebar: counter matches rendered links" "$GOT_N"
elif [ "$GOT_N" = "0" ]; then
  bad "sidebar: counter matches rendered links" "no counter button found"
else
  bad "sidebar: counter matches rendered links" "counter=$GOT_N links=$EXPECT_N"
fi

# --- 3. click through to the colony page -------------------------------------
CLICKED=$(orca_eval "(() => { const a=[...document.querySelectorAll('a[href*=\"/colony/\"]')]
  .find(x => (x.getAttribute('href')||'').endsWith('/$CID')); if(a){a.click(); return 'clicked';} return ''; })()")

COL=$(wait_ready '(() => {
  const h1 = (document.querySelector("h1")?.textContent || "").trim();
  if (!h1) return "";
  return JSON.stringify({
    url: location.href, h1,
    errorPage: /not found|404|Something went wrong|出错了|加载失败/i.test(document.body.innerText || ""),
    tabs: [...document.querySelectorAll("button,[role=tab]")]
            .map(e => (e.textContent||"").trim())
            .filter(t => ["数据","计划","自动化","工作节点","Data","Plan","Automation","Workers"].includes(t))
  });
})()' "colony page ready")

# --- 4. colony page: URL ------------------------------------------------------
if printf '%s' "$COL" | grep -q "colony/$CID"; then
  pass "colony page: URL routed" "/colony/$CID"
else
  bad "colony page: URL routed" "$(printf '%s' "$COL" | jq_get url)"
fi

# --- 5. colony page: heading is the colony ------------------------------------
H1=$(printf '%s' "$COL" | jq_get h1)
NUMPART=$(printf '%s' "$SLUG" | grep -oE '[0-9]{4,}')
if [ -n "$H1" ] && printf '%s' "$H1" | grep -qi "$NUMPART"; then
  pass "colony page: heading renders" "$H1"
else
  bad "colony page: heading renders" "h1='$H1'"
fi

# --- 6. colony page: not an error page ---------------------------------------
if printf '%s' "$COL" | grep -q '"errorPage":false'; then
  pass "colony page: not an error state" "clean"
else
  bad "colony page: not an error state" "error text present"
fi

# --- 7. colony page: panels rendered ------------------------------------------
if printf '%s' "$COL" | grep -qE '"(数据|Data)"'; then
  pass "colony page: panels rendered" "$(printf '%s' "$COL" | jq_get tabs | tr -d '[]\"')"
else
  bad "colony page: panels rendered" "expected panel tabs missing"
fi

# --- 8. console: no errors ----------------------------------------------------
CONS=$("$ORCA" console --page "$PAGE" --limit 60 --json 2>/dev/null)
ERRS=$(printf '%s' "$CONS" | grep -c '"level": *"error"')
SSE_OK=$(printf '%s' "$CONS" | grep -c "SSE] connected:.*$SLUG")
if [ "$ERRS" = "0" ]; then
  pass "console: no errors" "0 error entries"
else
  bad "console: no errors" "$ERRS error entries"
fi
if [ "$SSE_OK" -ge 1 ]; then
  pass "console: SSE bound to colony" "connected .../$SLUG"
else
  note "console: SSE bound to colony" "WARN  no SSE line for $SLUG"
fi

# --- evidence -----------------------------------------------------------------
# `orca screenshot` prints the path it just wrote — use that, never a glob.
# The screenshots dir is shared with every other agent on this machine, so
# `ls -t | head` would happily copy someone else's page into our evidence.
SHOT=$("$ORCA" screenshot --page "$PAGE" --json 2>/dev/null | jq_get 'result.path')
[ -n "$SHOT" ] || SHOT=$("$ORCA" screenshot --page "$PAGE" 2>/dev/null | tail -1)
if [ -n "$SHOT" ] && [ -f "$SHOT" ]; then
  cp "$SHOT" "$SHOTS/gate-$SLUG.png" && note "evidence: screenshot" "$SHOTS/gate-$SLUG.png"
else
  note "evidence: screenshot" "WARN  capture failed"
fi

{
echo
if [ "$fail" -eq 0 ]; then echo "RESULT: PASS"; else echo "RESULT: FAIL"; fi
} | tee -a "$LOG"
[ "$fail" -eq 0 ] && exit 0 || exit 1
