#!/usr/bin/env python3
"""UI E2E gate: a colony must appear in the sidebar AND on its colony page.

Headless twin of verify_colony_sidebar.sh. Drives a private headless Chrome
(channel="chrome" -> the installed Google Chrome; no browser download) against
a live `hive-embed serve`. Every assertion reads the RENDERED DOM, so it proves
the UI reflects backend state rather than that an API returns a row.

Why headless as the gate of record: the Orca-browser path shares ONE browser
with every other agent, and its runtime restarts drop tabs mid-run (observed
live: `browser_tab_not_found` on a tab created seconds earlier). That is fine
for interactive poking and useless as a gate -- a flaky harness reads as a UI
regression. This script owns its browser and tears it down.

Two DOM details this script exists to get right (both cost a debugging round):
  * The sidebar count is a <RollingNumber> odometer: the visible strip contains
    EVERY digit 0-9, so textContent yields "0123456789". The real value is in
    the sibling .sr-only span. Read that.
  * The colony page is a route-level lazy chunk, so it renders AFTER the click.
    Worse, the previous page's <h1> is still mounted while the chunk loads, so
    polling for "an h1 exists" captures the HOME heading. Capture the home h1
    first, then wait for one that differs.

Usage:
  ./verify_colony_sidebar_headless.py <colony_slug> [--base URL]
                                      [--shots DIR] [--expect-fail]

  colony_slug   on-disk colony dir name, e.g. ui_e2e_012144
                (route id derived "_" -> "-", matching slugToColonyId)
  --expect-fail assert the gate FAILS (proves the assertions have teeth)

Exit: 0 = all assertions pass, 1 = a UI assertion failed, 2 = harness problem.
"""
import argparse
import json
import re
import sys
import urllib.request
from pathlib import Path

PANEL_LABELS = {"数据", "计划", "自动化", "工作节点",
                "Data", "Plan", "Automation", "Workers"}

# Read the .sr-only sibling, never textContent -- see module docstring.
SIDEBAR_JS = r"""() => {
  const links = [...document.querySelectorAll('a[href*="/colony/"]')]
    .map(a => a.getAttribute('href'));
  let counter = '', counterNum = '';
  for (const b of document.querySelectorAll('button')) {
    const label = (b.textContent || '').trim();
    if (!/蜂群|Colon(y|ies)/i.test(label)) continue;
    const sr = b.querySelector('.sr-only');
    const num = sr ? (sr.textContent || '').trim() : '';
    if (/^\d+$/.test(num)) { counter = label; counterNum = num; break; }
  }
  if (!links.length || !counterNum) return '';
  return JSON.stringify({ links, counter, counterNum });
}"""

COLONY_JS = r"""() => {
  const h1 = (document.querySelector('h1')?.textContent || '').trim();
  return JSON.stringify({
    url: location.href,
    h1,
    errorPage: /not found|404|Something went wrong|出错了|加载失败/i
                 .test(document.body.innerText || ''),
    panels: [...document.querySelectorAll('button,[role=tab]')]
      .map(e => (e.textContent || '').trim())
      .filter(t => PANEL_LABELS.includes(t)),
  });
}""".replace("PANEL_LABELS", json.dumps(sorted(PANEL_LABELS)))

HOME_H1_JS = r"""() => (document.querySelector('h1')?.textContent || '').trim()"""

CLICK_JS = r"""(cid) => {
  const a = [...document.querySelectorAll('a[href*="/colony/"]')]
    .find(x => (x.getAttribute('href') || '').endsWith('/' + cid));
  if (!a) return false;
  a.click();
  return true;
}"""


class Gate:
    def __init__(self, slug, base, shots):
        self.slug = slug
        self.cid = slug.replace("_", "-")  # slugToColonyId: only "_" -> "-"
        self.base = base.rstrip("/")
        self.shots = Path(shots)
        self.results = []

    def check(self, label, ok, detail=""):
        self.results.append((label, bool(ok), detail))

    @property
    def failed(self):
        return [r for r in self.results if not r[1]]

    def api_has_colony(self):
        try:
            with urllib.request.urlopen(
                    f"{self.base}/api/colonies/tools-index", timeout=15) as r:
                data = json.load(r)
        except Exception as e:  # noqa: BLE001
            return False, f"api unreachable: {e}"
        for c in data.get("colonies", []) or []:
            if c.get("name") == self.slug:
                return True, json.dumps(
                    {k: c.get(k) for k in ("name", "queen_name", "created_at")})
        return False, f"{self.slug} absent from tools-index"

    def poll(self, page, js, ready, tries=30, arg=None):
        """Poll `js` until `ready(obj)` is true. A fixed sleep is not a
        readiness signal on a page that fetches and lazy-loads."""
        for _ in range(tries):
            try:
                raw = page.evaluate(js, arg) if arg is not None else page.evaluate(js)
            except Exception:  # noqa: BLE001
                raw = None
            if raw:
                try:
                    obj = json.loads(raw)
                except Exception:  # noqa: BLE001
                    obj = raw
                if ready(obj):
                    return obj
            page.wait_for_timeout(1000)
        return None

    def run(self, page):
        page.goto(self.base + "/", wait_until="domcontentloaded", timeout=45000)

        # 0. sidebar rendered
        sidebar = self.poll(page, SIDEBAR_JS, lambda o: o.get("counterNum"))
        if not sidebar:
            self.check("sidebar rendered", False, "no links+counter within 30s")
            return
        self.check("sidebar rendered", True,
                   f"{len(sidebar['links'])} links, counter={sidebar['counterNum']}")

        # 1. the colony link is present
        hits = [h for h in sidebar["links"]
                if (h or "").endswith("/" + self.cid)]
        self.check("sidebar: colony link present", bool(hits),
                   f"colony/{self.cid}")

        # 2. the counter is honest (not hardcoded/optimistic)
        rendered = len({h for h in sidebar["links"] if h})
        shown = int(sidebar["counterNum"] or 0)
        self.check("sidebar: counter matches rendered links",
                   shown != 0 and shown == rendered,
                   f"counter={shown} links={rendered}")

        # 3. capture the HOME h1 before navigating, so we can tell when the
        #    lazy colony chunk has actually replaced it.
        home_h1 = (page.evaluate(HOME_H1_JS) or "").strip()

        clicked = page.evaluate(CLICK_JS, self.cid)
        self.check("sidebar: link is clickable", clicked)

        # 4. wait for the colony page: right URL, and a *different* h1.
        def colony_ready(o):
            return (f"colony/{self.cid}" in (o.get("url") or "")
                    and o.get("h1")
                    and (not home_h1 or o["h1"] != home_h1))

        colony = self.poll(page, COLONY_JS, colony_ready)
        if not colony:
            self.check("colony page rendered", False, "colony h1 never appeared")
            return
        self.check("colony page rendered", True, colony["h1"])

        # 5. URL routed
        self.check("colony page: URL routed",
                   f"colony/{self.cid}" in (colony.get("url") or ""),
                   colony.get("url", ""))

        # 6. heading identifies the colony
        numpart = re.search(r"[0-9]{4,}", self.slug)
        self.check("colony page: heading renders",
                   bool(numpart) and numpart.group(0) in colony.get("h1", ""),
                   f"h1={colony.get('h1')!r}")

        # 7. not an error state
        self.check("colony page: not an error state",
                   not colony.get("errorPage"))

        # 8. panels rendered
        self.check("colony page: panels rendered", bool(colony.get("panels")),
                   ",".join(colony.get("panels") or []))

        # 9. UI agrees with the API
        ok, detail = self.api_has_colony()
        self.check("api: agrees with UI", ok, detail)

    def shoot(self, page, name):
        try:
            self.shots.mkdir(parents=True, exist_ok=True)
            page.screenshot(path=str(self.shots / name), full_page=True)
            return str(self.shots / name)
        except Exception:  # noqa: BLE001
            return ""

    def report(self):
        print("UI E2E — colony in sidebar + colony page")
        print(f"  slug   : {self.slug}")
        print(f"  colony : {self.cid}")
        print(f"  base   : {self.base}")
        print()
        for label, ok, detail in self.results:
            print(f"  {label:<44} {'PASS' if ok else 'FAIL'}  {detail}")
        print()
        print("RESULT: " + ("PASS" if not self.failed else "FAIL"))
        return 1 if self.failed else 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("slug")
    ap.add_argument("--base", default="http://127.0.0.1:8788")
    ap.add_argument("--shots", default=None)
    ap.add_argument("--expect-fail", action="store_true")
    args = ap.parse_args()

    here = Path(__file__).resolve()
    shots = args.shots or str(here.parents[3] / "docs" / "e2e-assets" / "ui-e2e")

    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("FATAL: playwright not importable; run with /Users/mac/hive/.venv/bin/python",
              file=sys.stderr)
        return 2

    g = Gate(args.slug, args.base, shots)
    try:
        with sync_playwright() as p:
            browser = p.chromium.launch(channel="chrome", headless=True)
            page = browser.new_page(viewport={"width": 1440, "height": 900})
            g.run(page)
            g.shoot(page, f"gate-{args.slug}-colony-full.png")
            browser.close()
    except Exception as e:  # noqa: BLE001
        print(f"FATAL: harness error: {e}", file=sys.stderr)
        return 2

    code = g.report()
    if args.expect_fail:
        print("( --expect-fail: gate MUST fail )")
        return 0 if code == 1 else 1
    return code


if __name__ == "__main__":
    sys.exit(main())
