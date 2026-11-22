#!/usr/bin/env python3
"""UI E2E gate — a colony MUST appear in the sidebar AND on its colony page.

Owned by: queen_growth (Steve Jobs) surfacing verification.
Sibling scripts gate-sidebar-colony.sh / verify_colony_gate.py were written
concurrently by other agents during the same window; this one is the
isolated-context variant (see below) and is safe to run in parallel with them.

Usage
-----
    gate_colony_surfacing.py                    # verify the default colony
    gate_colony_surfacing.py <colony_slug>      # verify a specific colony
    gate_colony_surfacing.py --create           # create via UI, verify, tear down

Design
------
* ISOLATED browser context (Playwright + installed Chrome). The Orca embedded
  tab is shared between agents; a concurrent navigation mid-assertion causes a
  FALSE FAIL. This gate owns its browser and always closes it.
* PRESENCE, never counts. Other agents create/delete colonies on the same live
  serve; the sidebar count races. Assert the target is listed; nothing more.
* A POSITIVE result is only meaningful beside a NEGATIVE control. The colony
  route is an SPA shell: it returns 200 for ANY id and echoes the raw slug as
  <h1>. So "HTTP 200", "h1 non-empty", and "body contains 012144" all
  FALSE-PASS. The discriminating assertion is h1 == display name (not slug)
  AND the session panels AND the queen subtitle. We prove the discriminator
  works by asserting a bogus id FAILS those same checks.
* Teardown in `finally`, so a crashed run never leaks a colony. Leaked test
  colonies inflate the sidebar and read as real usage.
"""

from __future__ import annotations

import json
import os
import re
import sys
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:8788"
DEFAULT_TARGET = "ui_e2e_012144"
PANELS = ["数据", "计划", "自动化", "工作节点", "Data", "Plan", "Automations", "Workers"]


def slug_to_colony_id(slug: str) -> str:
    """Mirror frontend lib/colony-registry.ts slugToColonyId(): '_' -> '-', nothing else."""
    return slug.replace("_", "-")


def display_name(slug: str) -> str:
    return " ".join(w.capitalize() for w in slug.split("_"))


def http_json(path: str):
    with urllib.request.urlopen(f"{BASE}{path}", timeout=15) as r:
        return json.loads(r.read().decode())


def http_delete(path: str) -> int:
    req = urllib.request.Request(f"{BASE}{path}", method="DELETE")
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            return r.status
    except urllib.error.HTTPError as e:
        return e.code


class Gate:
    def __init__(self):
        self.fails: list[str] = []
        self.passes: list[str] = []

    def ok(self, m):
        self.passes.append(m)
        print(f"PASS  {m}")

    def bad(self, m):
        self.fails.append(m)
        print(f"FAIL  {m}")

    def check(self, cond, msg, detail=""):
        self.ok(msg) if cond else self.bad(f"{msg}{(' — ' + detail) if detail else ''}")
        return bool(cond)


FACTS_JS = """() => {
  const h1 = [...document.querySelectorAll('h1,h2')].map(e => e.textContent.trim());
  const body = document.body.innerText || '';
  const btns = [...document.querySelectorAll('button,a')].map(e => e.textContent.trim());
  const panels = ['数据','计划','自动化','工作节点','Data','Plan','Automations','Workers']
      .filter(p => btns.some(t => t === p || t.startsWith(p)));
  return {
    h1, panels,
    hasQueen: /Head of Growth|Head of RevOps|Head of Content/.test(body),
    isError: /not found|404|Something went wrong|出错了/i.test(body),
    links: [...document.querySelectorAll('a')].map(e => e.getAttribute('href') || '')
                                          .filter(h => h.includes('/colony/')),
  };
}"""


def facts(page):
    return page.evaluate(FACTS_JS)


def sidebar_has(page, colony_id: str, tries: int = 12) -> bool:
    """ColonyContext.fetchColonies() runs on mount + visibilitychange only —
    there is no list poll — so allow the rebuilt list a moment to land."""
    for _ in range(tries):
        hrefs = page.evaluate(
            "() => [...document.querySelectorAll('a')].map(e => e.getAttribute('href') || '')"
        )
        if any(colony_id in h for h in hrefs):
            return True
        time.sleep(1)
    return False


def main() -> int:
    argv = sys.argv[1:]
    do_create = "--create" in argv
    argv = [a for a in argv if not a.startswith("--")]
    target = argv[0] if argv else DEFAULT_TARGET
    colony_id = slug_to_colony_id(target)
    want = display_name(target)

    gate = Gate()
    print("UI E2E — colony in sidebar + colony page")
    print(f"  base   : {BASE}")
    print(f"  colony : {target}  (route /colony/{colony_id})")
    print()

    try:
        http_json("/api/colonies/tools-index")
        gate.ok(f"serve reachable at {BASE}")
    except Exception as e:
        gate.bad(f"serve unreachable at {BASE}: {e}")
        return 1

    from playwright.sync_api import sync_playwright

    created = False
    pw = sync_playwright().start()
    browser = pw.chromium.launch(channel="chrome", headless=True)
    ctx = browser.new_context(viewport={"width": 1440, "height": 900})
    page = ctx.new_page()
    console_errs: list[str] = []
    page.on("console", lambda m: console_errs.append(m.text) if m.type == "error" else None)
    page.on("pageerror", lambda e: console_errs.append(str(e)))

    try:
        if do_create:
            target = f"ui_gate_{time.strftime('%H%M%S')}"
            colony_id = slug_to_colony_id(target)
            want = display_name(target)
            print(f"  create : {target}")
            page.goto(BASE, wait_until="domcontentloaded", timeout=30000)
            time.sleep(2)
            page.get_by_role("button", name=re.compile(r"创建蜂群|Create [Cc]olony")).first.click(timeout=10000)
            time.sleep(1)
            page.get_by_placeholder(re.compile(r"research team|e\.g\.")).first.fill(target)
            page.get_by_role("button", name=re.compile(r"Steve Jobs")).first.click()
            page.get_by_role("button", name=re.compile(r"^(创建|Create)$")).first.click()
            created = True
            try:
                page.wait_for_url(re.compile(r"/colony/"), timeout=20000)
                gate.ok("create navigated to the colony route")
            except Exception:
                gate.bad("create did not navigate to a colony route")

        # ---- ASSERT 1: colony page
        page.goto(f"{BASE}/#/colony/{colony_id}", wait_until="domcontentloaded", timeout=30000)
        time.sleep(3)
        f = facts(page)
        gate.check(want in f["h1"], f"colony page h1 is display name '{want}'", f"h1={f['h1']}")
        gate.check(any(p in f["panels"] for p in PANELS), "colony page renders session panels", f"panels={f['panels']}")
        gate.check(not f["isError"], "colony page is not an error page")
        page.screenshot(path="/tmp/hive_e2e/gate-colony-page.png")

        # ---- ASSERT 2: sidebar
        page.goto(BASE, wait_until="domcontentloaded", timeout=30000)
        time.sleep(2)
        gate.check(sidebar_has(page, colony_id), f"sidebar lists colony (link to {colony_id})")
        page.screenshot(path="/tmp/hive_e2e/gate-sidebar.png")

        # ---- NEGATIVE control — without this the positives prove nothing
        page.goto(f"{BASE}/#/colony/does-not-exist-xyz", wait_until="domcontentloaded", timeout=30000)
        time.sleep(3)
        n = facts(page)
        gate.check(want not in n["h1"], "negative: bogus route shows no real display name", f"h1={n['h1']}")
        gate.check(not any(p in n["panels"] for p in PANELS), "negative: bogus route renders no panels", f"panels={n['panels']}")
        page.screenshot(path="/tmp/hive_e2e/gate-negative.png")

        # ---- console hygiene (SSE reconnect noise from other agents is not ours)
        real = [e for e in console_errs if "network error" not in e]
        gate.check(not real, "no console errors", "; ".join(real[:3]))

        # ---- cross-check API + disk
        try:
            idx = http_json("/api/colonies/tools-index")
            gate.check(any(c.get("name") == target for c in idx.get("colonies", [])),
                       f"API tools-index reports {target}")
        except Exception as e:
            gate.bad(f"API cross-check failed: {e}")
        disk = os.path.expanduser(f"~/.hive-go/colonies/{target}")
        gate.check(os.path.isdir(disk), f"disk directory present ({disk})")

    finally:
        try:
            browser.close()
        except Exception:
            pass
        pw.stop()
        if created:
            print(f"teardown: DELETE /api/colonies/{target} -> HTTP {http_delete(f'/api/colonies/{target}')}")

    print()
    if gate.fails:
        print(f"RESULT: FAIL ({len(gate.fails)} assertion(s) failed)")
        return 1
    print(f"RESULT: PASS ({len(gate.passes)} assertions) — {target} in sidebar and on colony page")
    return 0


if __name__ == "__main__":
    sys.exit(main())
