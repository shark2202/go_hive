#!/usr/bin/env python3
"""Hive UI E2E gate: a colony must surface in the sidebar AND on its colony page.

Forgery-resistant by construction. The naive assertion `h1 contains the colony
name` is NOT evidence: a deep-link to a nonexistent colony renders h1 = the raw
route slug, derived from the URL alone. The signals that actually require a
backend read are:
  * h1 equals the DISPLAY name from worker.json ("Ui E2E 012144") which is
    title-cased and appears nowhere in the route slug ("ui-e2e-012144");
  * the main region resolves a queen attribution ("Steve Jobs / Head of Growth");
  * the main region leaves the `正在连接智能体…` placeholder behind.
A negative control (ghost colony) is run in the same process and MUST behave
differently -- if the ghost ever satisfies the real assertions, the gate is
measuring the URL, not the system, and the whole run is void.
"""
import json, os, time, pathlib, sys
from playwright.sync_api import sync_playwright

BASE      = os.environ.get("HIVE_BASE", "http://127.0.0.1:8788")
DISK_NAME = os.environ.get("COLONY_DISK_NAME", "ui_e2e_012144")
COLONY_ID = DISK_NAME.replace("_", "-")       # slugToColonyId: only _ -> -, no title-casing

# Expected values are DERIVED FROM DISK, never hardcoded -- otherwise the gate
# asserts against a stale literal and silently rots when the colony is renamed.
COL_DIR = pathlib.Path.home() / ".hive-go" / "colonies" / DISK_NAME
if not COL_DIR.is_dir():
    sys.exit(f"GATE ERROR: colony not on disk: {COL_DIR}")

_worker = json.loads((COL_DIR / "worker.json").read_text())
_meta   = json.loads((COL_DIR / "metadata.json").read_text())
DISPLAY  = _worker["name"]                    # "Ui E2E 012144"
QUEEN    = _meta.get("queen_name")            # "queen_growth"
QUEEN_ROLE = {"queen_growth": "Head of Growth", "queen_brand_design": "Brand Design",
              "queen_content_creation": "Content Creation", "queen_lead_gen": "Lead Gen",
              "queen_outbound": "Outbound", "queen_sales": "Sales"}.get(QUEEN)
QUEEN_HINT = QUEEN_ROLE                       # None => fall back to per-assertion skip
GHOST_ID   = "ui-e2e-999999"
CONNECTING = "正在连接智能体"

OUT = pathlib.Path(__file__).resolve().parent.parent
RES = OUT / "gate-results.json"
results = []

def rec(name, ok, detail):
    results.append({"id": name.split(" ")[0], "assertion": name, "ok": bool(ok), "detail": str(detail)[:500]})
    print(("  PASS  " if ok else "  FAIL  ") + name + " :: " + str(detail)[:260].replace("\n", " "), flush=True)
    RES.write_text(json.dumps(results, indent=2))

def settle(fn, pred, tries=60, gap=0.5):
    """Poll until pred(value) or exhaustion. Returns (final_value, matched, history)."""
    hist, val = [], None
    for _ in range(tries):
        val = fn()
        key = str(val)[:80]
        if key not in hist:
            hist.append(key)
        if pred(val):
            return val, True, hist
        time.sleep(gap)
    return val, False, hist

def close_dialogs(page):
    try:
        page.keyboard.press("Escape"); time.sleep(0.25)
        page.eval_on_selector_all(
            '[role="dialog"] button',
            """els => els.forEach(e => { const t = (e.textContent||'') + ' ' + (e.getAttribute('aria-label')||'');
                 if (/skip|got it|close|dismiss|稍后|关闭/i.test(t)) e.click(); })""")
        time.sleep(0.4)
        page.keyboard.press("Escape")
        return page.eval_on_selector_all('[role="dialog"]', "e => e.filter(x => x.offsetParent !== null).length")
    except Exception as e:
        return f"err:{e}"

def h1s(page):
    return page.eval_on_selector_all("h1", "e => e.map(x => (x.textContent||'').trim())")

def main_txt(page, n=3000):
    try:
        return page.inner_text("main")[:n]
    except Exception:
        return ""

def links(page):
    return page.eval_on_selector_all('a[href*="/colony/"]',
        "e => e.map(x => ({href: x.getAttribute('href'), text: (x.textContent||'').trim()}))")

def colony_link_ok(l):
    return any(COLONY_ID in (h.get("href") or "") for h in l)

def page_ok(page_id, label):
    """Assertions that require backend truth. Returns (all_ok, detail)."""
    want_display = DISPLAY                       # "Ui E2E 012144" -- title-cased, from worker.json
    url_slug     = COLONY_ID                     # "ui-e2e-012144" -- appears nowhere in the display name
    h1, h1_ok, h1_hist = settle(lambda: h1s(page_id),
        lambda v: any((t or "").strip() == want_display for t in v))
    rec(f"{label}1 h1 EQUALS the backend display name {want_display!r}", h1_ok,
        {"h1": h1, "history": h1_hist})
    # A URL-derived h1 would read the raw slug. The display name differs in case
    # ("Ui" vs "ui") and spacing ("E2E" vs "e2e"), so an exact-equality check
    # against the slug is a genuine forgery test, not a restatement of 1.
    rec(f"{label}2 h1 is not merely the URL slug {url_slug!r}",
        not any((t or "").strip().lower() == url_slug for t in h1), h1)

    if QUEEN_HINT:
        mt, q_ok, q_hist = settle(lambda: main_txt(page_id), lambda v: QUEEN_HINT in v)
        rec(f"{label}3 main region resolves queen {QUEEN!r} -> {QUEEN_HINT!r}", q_ok,
            mt[:220].replace("\n", " | "))
    else:
        mt = main_txt(page_id); q_ok = True
        rec(f"{label}3 queen role unknown for {QUEEN!r} -- skipped", True, "no role mapping")
    mt = main_txt(page_id)
    rec(f"{label}4 main region left the {CONNECTING!r} placeholder", CONNECTING not in mt,
        {"saw_connecting": CONNECTING in mt, "states": q_hist[:3]})
    rec(f"{label}5 no error surface in main", ("Not Found" not in mt) and ("Something went wrong" not in mt), "clean")
    return h1_ok and q_ok

with sync_playwright() as p:
    b = p.chromium.launch(channel="chrome", headless=True)

    # ---------- A: COLD LOAD of root in a brand-new context ----------
    try:
        print("== A. COLD LOAD root (fresh context => fetchColonies runs from mount) ==", flush=True)
        ctx = b.new_context(viewport={"width": 1440, "height": 950})
        api = {}
        ctx.on("response", lambda r: api.__setitem__(r.url.split("8788")[-1], r.status) if "/api/" in r.url else None)
        pg = ctx.new_page()
        errs = []
        pg.on("console", lambda m: errs.append((m.type, m.text[:200])) if m.type == "error" else None)
        pg.on("pageerror", lambda e: errs.append(("pageerror", str(e)[:200])))
        pg.goto(BASE + "/", wait_until="domcontentloaded", timeout=60000)
        close_dialogs(pg)

        l, l_ok, _ = settle(lambda: links(pg), lambda v: colony_link_ok(v), tries=40, gap=1.0)
        rec("A1 sidebar lists >=1 colony on cold load", len(l) > 0, f"{len(l)} colony links")
        rec(f"A2 sidebar contains {COLONY_ID} on cold load", l_ok,
            [h for h in l if COLONY_ID in (h.get("href") or "")])
        rec("A3 /api/discover hit on this cold load", any("/api/discover" in u for u in api),
            sorted(k for k in api if "discover" in k or "sessions" in k))
        pg.screenshot(path=str(OUT / "gate-a-sidebar-cold.png"))
        rec("A4 sidebar screenshot", (OUT / "gate-a-sidebar-cold.png").exists(), "gate-a-sidebar-cold.png")
        rec("A5 zero console errors on cold root load", len(errs) == 0, errs[:4])
    except Exception as e:
        rec("A* cold root section", False, repr(e)[:250])

    # ---------- B: CLICK-THROUGH ----------
    try:
        print("== B. CLICK-THROUGH sidebar -> colony page ==", flush=True)
        left = close_dialogs(pg)
        rec("B0 modals cleared before click", isinstance(left, int) and left == 0, f"visible dialogs={left}")
        pg.click(f'a[href*="{COLONY_ID}"]', timeout=20000)
        pg.wait_for_url(f"**/#/colony/{COLONY_ID}", timeout=30000)
        rec("B1 click lands on colony route", f"/#/colony/{COLONY_ID}" in pg.url, pg.url)
        page_ok(pg, "B")
        pg.screenshot(path=str(OUT / "gate-b-colony-page.png"), full_page=True)
    except Exception as e:
        rec("B* click-through section", False, repr(e)[:250])

    # ---------- C: COLD DEEP-LINK in its own fresh context ----------
    try:
        print("== C. COLD deep-link (separate fresh context) ==", flush=True)
        ctx2 = b.new_context(viewport={"width": 1440, "height": 950})
        p2 = ctx2.new_page()
        errs2 = []
        p2.on("console", lambda m: errs2.append((m.type, m.text[:200])) if m.type == "error" else None)
        p2.on("pageerror", lambda e: errs2.append(("pageerror", str(e)[:200])))
        p2.goto(f"{BASE}/#/colony/{COLONY_ID}", wait_until="domcontentloaded", timeout=60000)
        close_dialogs(p2)
        page_ok(p2, "C")
        p2.screenshot(path=str(OUT / "gate-c-cold-deeplink.png"), full_page=True)
        rec("C6 zero console errors on cold deep-link", len(errs2) == 0, errs2[:4])
    except Exception as e:
        rec("C* cold deep-link section", False, repr(e)[:250])

    # ---------- N: NEGATIVE CONTROL ----------
    try:
        print("== N. NEGATIVE CONTROL (ghost colony) ==", flush=True)
        ctx3 = b.new_context(viewport={"width": 1440, "height": 950})
        p3 = ctx3.new_page()
        p3.goto(BASE + "/", wait_until="domcontentloaded", timeout=60000)
        close_dialogs(p3)
        lg, _, _ = settle(lambda: links(p3), lambda v: len(v) > 0, tries=40, gap=1.0)
        rec("N1 ghost colony absent from sidebar", len(lg) > 0 and not any(GHOST_ID in (h.get("href") or "") for h in lg),
            f"{len(lg)} links, ghost hits={[h for h in lg if GHOST_ID in (h.get('href') or '')]}")

        p3.goto(f"{BASE}/#/colony/{GHOST_ID}", wait_until="domcontentloaded", timeout=60000)
        close_dialogs(p3)
        gh1, _, _ = settle(lambda: h1s(p3), lambda v: any(v), tries=20)
        gmt, got_queen, _ = settle(lambda: main_txt(p3), lambda v: QUEEN_HINT in v, tries=16)
        rec("N2 ghost deep-link does NOT resolve a queen (gate is backend-sensitive)",
            not got_queen, {"h1": gh1, "main": gmt[:120].replace("\n", " | ")})
        rec("N3 ghost deep-link h1 is the raw slug, not a display name",
            any(GHOST_ID in (t or "") for t in gh1), gh1)
        p3.screenshot(path=str(OUT / "gate-n-ghost-control.png"))
    except Exception as e:
        rec("N* negative control section", False, repr(e)[:250])

    b.close()

ok = sum(1 for r in results if r["ok"])
total = len(results)
verdict = "PASS" if results and ok == total else "FAIL"
print(json.dumps({"colony": DISK_NAME, "display": DISPLAY, "queen": QUEEN,
                  "passed": ok, "total": total, "verdict": verdict}, indent=1), flush=True)
RES.write_text(json.dumps(results, indent=2))
sys.exit(0 if verdict == "PASS" else 1)
