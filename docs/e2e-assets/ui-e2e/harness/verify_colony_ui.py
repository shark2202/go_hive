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

# Strings that mean we landed on an error surface, not a colony.
# Deliberately narrow: the colony page renders a CHAT TRANSCRIPT, and a
# transcript legitimately contains prose like "Colony not found" (that is
# exactly what this gate's own not-found test writes). Substring-matching the
# whole body over a loose /not found/i turned that into a false FAIL on a
# perfectly healthy colony. Match only the app's own surface markers.
ERROR_PAGE_RE = re.compile(
    r"Page not found|Colony not found|未找到蜂群|页面不存在|something went wrong",
    re.I)


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

    # The display name is owned by the BACKEND (worker.json -> /api/discover),
    # not by any client-side title-casing. Asserting on it (instead of a
    # lowercased suffix substring) is what makes these checks exact: a suffix
    # match passed "012144" only because it is all digits, failed "ghost"
    # ("ghost" != "Ghost"), and would false-PASS "demo" against "Demo2".
    def backend_display_name():
        try:
            import urllib.request
            with urllib.request.urlopen(base + "/api/discover", timeout=10) as r:
                disc = json.loads(r.read().decode("utf-8"))
        except Exception as e:
            return None, "discover failed: %r" % (e,)
        for v in disc.values():
            if not isinstance(v, list):
                continue
            for item in v:
                if not isinstance(item, dict):
                    continue
                if os.path.basename(item.get("path", "")) == colony:
                    return item.get("name"), None
        return None, "colony %r not in /api/discover" % colony

    display_name, dn_err = backend_display_name()
    result["evidence"]["backend_display_name"] = display_name

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

        # Exact href, not substring: '#/colony/demo' must not be satisfied by
        # '#/colony/demo2'.
        want_href = "#/colony/%s" % colony_id
        links = page.eval_on_selector_all(
            "a[href*='/colony/']", "els => els.map(e => e.getAttribute('href'))")
        check("sidebar: colony link present",
              want_href in links,
              "want=%s got=%s" % (want_href, sorted(set(links))))

        # Compare against the backend's exact display name, on the entry's own
        # text -- so "Demo" cannot be satisfied by the neighbouring "Demo2" row.
        label = display_name
        if label is None:
            check("sidebar: colony name visible in list", False,
                  "no backend display name (%s)" % dn_err)
        else:
            entry_text = page.eval_on_selector_all(
                "a[href*='/colony/']",
                "els => els.filter(e => e.getAttribute('href') === %r)"
                "        .map(e => (e.textContent || '').trim())" % want_href)
            check("sidebar: colony name visible in list",
                  any(t == label for t in entry_text),
                  "want=%r got=%s" % (label, entry_text))

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

        if display_name is None:
            check("colony page: heading names the colony", False,
                  "no backend display name (%s)" % dn_err)
        else:
            check("colony page: heading names the colony",
                  display_name in h1s,
                  "want=%r h1=%s" % (display_name, h1s))

        # Only inspect the app's own chrome, not the chat transcript body.
        err_match = ERROR_PAGE_RE.search(" ".join(h1s)) or \
                    ERROR_PAGE_RE.search(page.inner_text("nav") if page.query_selector("nav") else "")
        check("colony page: not an error surface",
              not err_match,
              "matched %r in page chrome" % (err_match.group(0) if err_match else ""))

        # A colony page renders its panel chrome. If the colony failed to
        # resolve, this collapses and the requirement is not actually met.
        panel_markers = ["数据", "计划", "自动化", "工作节点",
                         "Data", "Plan", "Automations", "Workers"]
        found_markers = [m for m in panel_markers if m in body_text]
        # The page is localised, so a run yields one language's four tabs.
        full_panel = len(found_markers) >= 4
        check("colony page: colony panel rendered",
              full_panel,
              "markers=%s (%d/4)" % (found_markers, len(found_markers)))

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
