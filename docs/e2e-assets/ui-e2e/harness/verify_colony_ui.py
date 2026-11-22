#!/usr/bin/env python3
"""
UI E2E: a colony must appear in the sidebar AND on its colony page.

Reproducible, headless, assertion-based. Replaces the hand-driven Orca click-through
in REPORT.md with something a gate can run unattended.

Drives the REAL Hive SPA (no mocks, no fixtures) in the installed Google Chrome
via Playwright's channel="chrome" -- no browser download required. Uses a
throwaway profile, so it never touches the user's logged-in Chrome.

Not used: `hive-browser`. That path needs the Hive Browser Bridge extension
(jkpcegnbfimimjodblcemoheedidnppm) installed in the user's Chrome; it is absent
on this machine, so a hive-browser gate would fail for reasons unrelated to the
code under test. See "Known gap" in REPORT.md.

Usage:
    verify_colony_ui.py <colony_disk_name> [--base http://127.0.0.1:8788] [--out DIR]

Exit codes: 0 = all assertions pass, 1 = an assertion failed, 2 = harness error.
"""

import argparse
import json
import os
import re
import sys
import time

try:
    from playwright.sync_api import sync_playwright
except ImportError:
    print("FATAL: playwright not importable. Install it for the python you run, "
          "e.g. pip3 install playwright (no `playwright install` needed -- we "
          "drive the system Chrome via channel='chrome').", file=sys.stderr)
    sys.exit(2)

# Page text that means we landed on an error surface, not a colony.
ERROR_PAGE_RE = re.compile(r"not found|404|something went wrong|出错了|加载失败", re.I)


def slug_to_colony_id(slug):
    """Mirror of frontend src/lib/colony-registry.ts::slugToColonyId.

    It only rewrites '_' -> '-'; it does NOT title-case or strip words.
    Kept in lockstep with the TS on purpose: the route id is the contract this
    test asserts on, so if that mapping drifts, this test should fail loudly.
    """
    return slug.replace("_", "-")


def run(colony, base, out_dir, keep_open_sec):
    colony_id = slug_to_colony_id(colony)
    result = {
        "colony_disk": colony,
        "colony_id": colony_id,
        "base": base,
        "assertions": [],
        "evidence": {},
    }
    failures = []

    def check(name, ok, detail=""):
        result["assertions"].append({"name": name, "ok": bool(ok), "detail": detail})
        if not ok:
            failures.append("%s: %s" % (name, detail))

    os.makedirs(out_dir, exist_ok=True)

    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome", headless=True)
        page = browser.new_page(viewport={"width": 1440, "height": 900})

        console = []
        page.on("console", lambda m: console.append({"level": m.type, "text": m.text}))
        page.on("pageerror", lambda e: console.append({"level": "pageerror", "text": str(e)}))

        # ---- 1. Sidebar -----------------------------------------------------
        page.goto(base + "/", wait_until="load", timeout=45000)
        # The colony list is fetched after mount (/discover + /sessions); wait on
        # the link itself rather than a bare sleep.
        try:
            page.wait_for_selector("a[href*='%s']" % colony_id, timeout=20000)
        except Exception:
            pass
        time.sleep(1.5)

        check("sidebar: loaded the Hive SPA",
              page.title() == "Hive" and "root" in page.content(),
              "title=%r" % page.title())

        links = page.eval_on_selector_all(
            "a[href*='/colony/']", "els => els.map(e => e.getAttribute('href'))")
        sidebar_target = sorted({h for h in links if colony_id in h or colony in h})
        check("sidebar: colony link present",
              len(sidebar_target) > 0,
              "links=%s" % sorted(set(links)))

        # The sidebar renders the human-readable display name; match on the
        # timestamp suffix so we don't depend on title-casing rules.
        suffix = colony.rsplit("_", 1)[-1] if "_" in colony else colony
        sidebar_text = page.inner_text("aside, nav, body")
        check("sidebar: colony name visible in list",
              suffix in sidebar_text,
              "looked for %r" % suffix)

        page.screenshot(path=os.path.join(out_dir, "sidebar.png"), full_page=False)
        result["evidence"]["sidebar_png"] = os.path.join(out_dir, "sidebar.png")

        # ---- 2. Colony page -------------------------------------------------
        page.goto("%s/#/colony/%s" % (base, colony_id), wait_until="load", timeout=45000)
        try:
            page.wait_for_function("() => !!document.querySelector('h1')", timeout=20000)
        except Exception:
            pass
        time.sleep(2.0)

        h1s = [h.strip() for h in page.eval_on_selector_all(
            "h1", "els => els.map(e => e.textContent)") if h.strip()]
        body_text = page.inner_text("body")

        check("colony page: route is the colony URL",
              colony_id in page.url,
              "url=%s" % page.url)

        check("colony page: heading names the colony",
              any(suffix in h for h in h1s),
              "h1=%s" % h1s)

        check("colony page: not an error surface",
              not ERROR_PAGE_RE.search(body_text),
              "matched an error pattern in body text")

        # A colony page renders its panel chrome. If the colony failed to
        # resolve, this collapses and the requirement is not actually met.
        panel_markers = ["数据", "计划", "自动化", "工作节点",
                         "Data", "Plan", "Automations", "Workers"]
        check("colony page: colony panel rendered",
              any(m in body_text for m in panel_markers),
              "no panel markers found")

        page.screenshot(path=os.path.join(out_dir, "colony-page.png"), full_page=True)
        result["evidence"]["colony_page_png"] = os.path.join(out_dir, "colony-page.png")

        result["evidence"]["console"] = console[-40:]
        errors = [c for c in console if c["level"] in ("error", "pageerror")]
        check("no console errors",
              len(errors) == 0,
              json.dumps(errors[:5]))

        result["evidence"]["final_url"] = page.url
        if keep_open_sec:
            time.sleep(keep_open_sec)
        browser.close()

    result["pass"] = not failures
    result["failures"] = failures
    return result


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("colony", help="colony disk name, e.g. ui_e2e_012144")
    ap.add_argument("--base", default=os.environ.get("HIVE_BASE", "http://127.0.0.1:8788"))
    ap.add_argument("--out", default=os.environ.get("HIVE_E2E_OUT", "./ui-e2e-artifacts"))
    ap.add_argument("--keep-open", type=float, default=0.0,
                    help="seconds to hold the page open before closing (debugging)")
    args = ap.parse_args()

    try:
        result = run(args.colony, args.base, args.out, args.keep_open)
    except Exception as e:
        print(json.dumps({"pass": False, "harness_error": str(e)}, indent=2), file=sys.stderr)
        return 2

    print(json.dumps(result, indent=2, ensure_ascii=False))
    print("\n=== UI E2E: %s ===" % ("PASS" if result["pass"] else "FAIL"), file=sys.stderr)
    return 0 if result["pass"] else 1


if __name__ == "__main__":
    sys.exit(main())
