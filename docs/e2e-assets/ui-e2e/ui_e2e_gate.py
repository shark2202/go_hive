#!/usr/bin/env python3
"""UI E2E gate: a colony must appear in the sidebar AND on its colony page.

Drives a real browser (Orca's embedded one) against the live Hive serve and
asserts, in order:

  1. serve is reachable
  2. the colony's row is present in the sidebar        <- "appears in sidebar"
  3. the sidebar counter parses
  4. the colony page renders that colony               <- "appears on colony page"
  5. the page is not an error/empty state
  6. no hard console errors (SSE reconnect noise tolerated)

Exit 0 = PASS, 1 = FAIL. One JSON line on stdout.

Usage: ui_e2e_gate.py <colony_disk_slug> [base_url]

Why Orca: the Hive Browser Bridge Chrome extension is not installed on this
machine, so `hive-browser` cannot attach to the user's Chrome. Orca's embedded
browser needs no extension.

Why the sidebar budget is generous: the sidebar is hydrated by a client-side
fetchColonies() fan-out, measured here at ~85s on a cold tab with 24 colonies
and ~300 sessions while the backend answers in single-digit milliseconds. That
latency is itself reported (sidebar_ms) rather than hidden behind a fast fail.
"""
import json
import os
import subprocess
import sys
import time

ORCA = os.environ.get("ORCA_BIN", "/usr/local/bin/orca")
SIDEBAR_BUDGET_MS = int(os.environ.get("SIDEBAR_BUDGET_MS", "150000"))
POLL_MS = 3000


def orca(*args, timeout=60):
    try:
        r = subprocess.run([ORCA, *args, "--json"], capture_output=True,
                           text=True, timeout=timeout)
        return json.loads(r.stdout) if r.stdout.strip() else {}
    except Exception as e:
        return {"_error": str(e)}


def ev(page, js):
    """Evaluate JS in the page; return the result value ('' on failure)."""
    d = orca("eval", "--page", page, "--expression", js)
    v = d.get("result", {}).get("result")
    return "" if v is None else v


def as_bool(v):
    """Orca's eval returns JS booleans as the strings 'true'/'false'."""
    if isinstance(v, bool):
        return v
    if isinstance(v, str):
        return v.strip().lower() == "true"
    return False


def result(ok, **kw):
    print(json.dumps({"ok": ok, **kw}, ensure_ascii=False))
    return 0 if ok else 1


def main():
    if len(sys.argv) < 2:
        return result(False, stage="usage",
                      reason="ui_e2e_gate.py <colony_disk_slug> [base_url]")
    colony = sys.argv[1]
    base = sys.argv[2] if len(sys.argv) > 2 else "http://127.0.0.1:8788"

    # Frontend slugToColonyId parity: disk underscores become route hyphens.
    route_id = colony.replace("_", "-")
    page = ""

    def log(msg):
        print(msg, file=sys.stderr)

    def index_of(pg):
        tl = orca("tab", "list")
        for t in tl.get("result", {}).get("tabs", []):
            if t.get("browserPageId") == pg:
                return t.get("index")
        return None

    def cleanup():
        if not page:
            return
        idx = index_of(page)
        if idx is not None:
            orca("tab", "close", "--index", str(idx))

    try:
        # 1. serve reachable
        r = subprocess.run(
            ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
             "--max-time", "5", base + "/"],
            capture_output=True, text=True)
        code = r.stdout.strip()
        if code != "200":
            return result(False, colony=colony, stage="serve",
                          reason=f"GET {base}/ returned '{code}'")
        log(f"  PASS serve reachable ({base})")

        # 2. our own tab
        tab = orca("tab", "create", "--url", base + "/")
        page = tab.get("result", {}).get("browserPageId", "")
        if not page:
            return result(False, colony=colony, stage="tab_create",
                          reason=f"no browserPageId: {str(tab)[:200]}")
        log(f"  PASS opened tab {page[:8]}")

        # 3. wait for the sidebar row to hydrate (the slow part)
        selector_js = (
            "!!document.querySelector(\"a[href*='/colony/%s']\")" % route_id
        )
        started = time.time()
        found = False
        while (time.time() - started) * 1000 < SIDEBAR_BUDGET_MS:
            if as_bool(ev(page, selector_js)):
                found = True
                break
            time.sleep(POLL_MS / 1000.0)
        sidebar_ms = int((time.time() - started) * 1000)

        if not found:
            links = ev(page, "JSON.stringify([...document.querySelectorAll('a')]"
                             ".map(a=>a.getAttribute('href')).filter(h=>h&&h.includes('colony')))")
            return result(False, colony=colony, route_id=route_id,
                          stage="sidebar_present", sidebar_ms=sidebar_ms,
                          reason=f"colony '{route_id}' not in sidebar after "
                                 f"{sidebar_ms}ms. colony links={str(links)[:300]}")
        log(f"  PASS sidebar row present after {sidebar_ms}ms")

        # counter (reported, not asserted on a specific value)
        count = ""
        m = ev(page, "(()=>{const m=document.body.innerText.match(/蜂群\\s*(\\d+)/);"
                     "return m?m[1]:''})()")
        if m:
            count = str(m)
        log(f"     sidebar counter: {count or '<unreadable>'}")

        # 4. colony page
        orca("goto", "--page", page, "--url", f"{base}/#/colony/{route_id}")
        time.sleep(3)

        url_now = str(ev(page, "location.href"))
        if f"/colony/{route_id}" not in url_now:
            return result(False, colony=colony, route_id=route_id,
                          stage="colony_route", url=url_now,
                          reason=f"not on colony route: '{url_now}'")
        log(f"  PASS route -> {url_now}")

        h1 = str(ev(page, "(()=>{const h=document.querySelector('h1');"
                           "return h?h.textContent.trim():''})()")).strip()
        if not h1:
            return result(False, colony=colony, route_id=route_id,
                          stage="colony_h1", url=url_now,
                          reason="colony page rendered no <h1>")

        def norm(s):
            return "".join(ch for ch in s if ch.isalnum()).lower()

        if norm(colony) not in norm(h1) and norm(colony.split("_")[-1]) not in norm(h1):
            return result(False, colony=colony, route_id=route_id,
                          stage="colony_h1", url=url_now, h1=h1,
                          reason=f"h1 '{h1}' does not name colony '{colony}'")
        log(f'  PASS colony page h1: "{h1}"')

        bad = ev(page, "String(/not found|404|something went wrong|出错了|无法找到/i"
                       ".test(document.body.innerText))")
        if as_bool(bad):
            return result(False, colony=colony, route_id=route_id,
                          stage="error_page", url=url_now,
                          reason="colony page shows an error/empty state")
        log("  PASS colony page is not an error state")

        # 5. console
        con = orca("console", "--page", page, "--limit", "80")
        raw = json.dumps(con)
        errs = raw.count('"level": "error"') + raw.count('"level":"error"')
        if errs:
            return result(False, colony=colony, route_id=route_id,
                          stage="console", url=url_now, console_errors=errs,
                          reason=f"{errs} console error(s)")
        log("  PASS console clean")

        return result(True, colony=colony, route_id=route_id,
                      sidebar_ms=sidebar_ms, sidebar_count=count, h1=h1,
                      url=url_now, console_errors=0)
    finally:
        cleanup()


if __name__ == "__main__":
    sys.exit(main())
