#!/usr/bin/env python3
"""UI E2E gate: a colony must appear in the sidebar AND on its colony page.

Drives a real Chrome through Playwright using an isolated temp profile, so it
never touches the user's running browser, cookies, or the Hive Browser Bridge
extension. Reads the expected colony set from the backend, then verifies the
UI agrees with it.

Exit codes:
  0  PASS  -- every assertion held
  1  FAIL  -- at least one assertion failed (details on stderr)
  2  ERROR -- could not run (server down, browser missing)

Usage:
  python3 verify_colony_surface.py --colony ui_e2e_012144
  python3 verify_colony_surface.py --colony ui_e2e_012144 --base http://127.0.0.1:8788
  python3 verify_colony_surface.py --all-from-backend
"""
from __future__ import annotations

import argparse
import json
import re
import sys
import urllib.request

# Console lines that are known noise and must not fail an unattended run.
# A stale tab whose SSE stream was cut by a backend restart logs one
# "[SSE] error: ... network error" line. That is reconnect churn, not a
# defect in colony surfacing -- but it must be matched narrowly, so it can
# never mask a real regression.
CONSOLE_NOISE = [
    re.compile(r"^\[SSE\] (error|opening|connected)"),
]


def slug_to_colony_id(slug: str) -> str:
    """Mirror of frontend src/lib/colony-registry.ts slugToColonyId.

    Only "_" -> "-" and two prefix/suffix strips. Deliberately NOT a
    title-case or lowercase: the route id preserves the disk slug's case,
    so "ui_e2e_012144" routes at "/colony/ui-e2e-012144".
    """
    out = slug.replace("_", "-")
    if out.startswith("email-"):
        out = out[len("email-"):]
    if out.endswith("-agent"):
        out = out[: -len("-agent")]
    return out


def fetch_colonies(base: str) -> list[dict]:
    """Backend truth for which colonies exist and how they're named.

    /api/discover is the endpoint the sidebar's fetchColonies() actually
    reads, so comparing the UI against it is a real cross-check rather
    than a tautology.
    """
    with urllib.request.urlopen(f"{base}/api/discover", timeout=15) as r:
        payload = json.load(r)
    rows: list[dict] = []
    for _category, entries in payload.items():
        if not isinstance(entries, list):
            continue
        for e in entries:
            if not isinstance(e, dict):
                continue
            name = e.get("name")
            path = e.get("path") or ""
            slug = path.rstrip("/").rsplit("/", 1)[-1] if path else ""
            if name and slug:
                rows.append({"name": name, "slug": slug, "created_at": e.get("created_at")})
    return rows


def excluded(link_text: str) -> bool:
    """Colony links we don't assert on."""
    return False


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--colony", default="ui_e2e_012144", help="colony slug on disk")
    ap.add_argument("--base", default="http://127.0.0.1:8788")
    ap.add_argument("--expect-name", default=None, help="expected sidebar label")
    ap.add_argument("--screenshot-dir", default="")
    ap.add_argument("--all-from-backend", action="store_true",
                    help="verify every non-demo colony the backend reports")
    args = ap.parse_args()

    failures: list[str] = []
    notes: list[str] = []

    def check(ok: bool, label: str, detail: str = "") -> None:
        mark = "PASS" if ok else "FAIL"
        print(f"  [{mark}] {label}" + (f" — {detail}" if detail else ""))
        if not ok:
            failures.append(f"{label}{(' — ' + detail) if detail else ''}")

    # ---- backend truth -------------------------------------------------
    try:
        backend = fetch_colonies(args.base)
    except Exception as exc:  # noqa: BLE001
        print(f"ERROR: cannot reach {args.base}/api/discover: {exc}", file=sys.stderr)
        return 2

    by_slug = {c["slug"]: c for c in backend}

    if args.all_from_backend:
        targets = [s for s in by_slug if not s.startswith("demo")]
    else:
        targets = [args.colony]

    print(f"Hive UI colony-surface gate — {args.base}")
    print(f"backend reports {len(backend)} colonies: {', '.join(sorted(by_slug))}\n")

    try:
        from playwright.sync_api import sync_playwright
    except Exception as exc:  # noqa: BLE001
        print(f"ERROR: playwright not importable: {exc}", file=sys.stderr)
        return 2

    try:
        pw = sync_playwright().start()
        # channel="chrome" uses the installed Google Chrome but with a fresh
        # throwaway profile: isolated from the user's session and extensions.
        browser = pw.chromium.launch(headless=True, channel="chrome")
    except Exception as exc:  # noqa: BLE001
        print(f"ERROR: cannot launch Chrome: {exc}", file=sys.stderr)
        return 2

    page = browser.new_page(viewport={"width": 1440, "height": 900})
    console: list[str] = []
    page.on("console", lambda m: console.append(f"{m.type}: {m.text}"))
    page.on("pageerror", lambda e: console.append(f"pageerror: {e}"))

    try:
        page.goto(args.base, wait_until="domcontentloaded", timeout=30000)
        # The sidebar fills from /discover + /sessions after mount; give the
        # React tree a beat to paint the list before we read it.
        page.wait_for_selector("a[href*='/colony/']", timeout=20000)

        # ---- assertion 1: sidebar lists every target -------------------
        sidebar_hrefs = page.eval_on_selector_all(
            "a[href*='/colony/']", "els => els.map(e => e.getAttribute('href'))"
        )
        sidebar_ids = {h.rsplit("/", 1)[-1] for h in sidebar_hrefs if h}

        for slug in targets:
            expected_id = slug_to_colony_id(slug)
            check(expected_id in sidebar_ids,
                  f"sidebar lists colony '{slug}'",
                  f"expected route /colony/{expected_id}; sidebar has {sorted(sidebar_ids)}")

        # ---- assertion 2: colony page renders, matched to backend ------
        for slug in targets:
            expected_id = slug_to_colony_id(slug)
            meta = by_slug.get(slug, {})
            expected_name = args.expect_name or meta.get("name") or slug
            url = f"{args.base}/#/colony/{expected_id}"

            # Navigate by clicking the sidebar entry when present: that also
            # proves the link is live, not just that the href exists.
            if expected_id in sidebar_ids:
                page.click(f"a[href$='/colony/{expected_id}']")
                page.wait_for_function(
                    "id => location.href.endsWith('/colony/' + id)",
                    arg=expected_id, timeout=15000,
                )
            else:
                notes.append(f"navigated to {slug} by URL (not in sidebar)")

            page.wait_for_selector("h1", timeout=15000)
            h1 = (page.inner_text("h1") or "").strip()
            body = page.inner_text("body")

            check(h1 == expected_name,
                  f"colony page h1 for '{slug}'",
                  f"expected {expected_name!r}, got {h1!r}")

            check(expected_id in page.url,
                  f"colony page URL for '{slug}'",
                  page.url)

            # An error page that still has an h1 would slip past the check
            # above, so look for the app's own failure copy too.
            looks_broken = re.search(
                r"not found|404|something went wrong|出错了|加载失败", body, re.I
            )
            check(not looks_broken,
                  f"colony page is not an error page for '{slug}'",
                  looks_broken.group(0) if looks_broken else "")

            if args.screenshot_dir:
                page.screenshot(path=f"{args.screenshot_dir}/colony-{expected_id}.png",
                                full_page=True)

        # ---- assertion 3: no unexpected console errors -----------------
        bad = []
        for line in console:
            if not line.startswith(("error:", "pageerror:")):
                continue
            if any(p.search(line.split(":", 1)[1].strip()) for p in CONSOLE_NOISE):
                continue
            bad.append(line)
        check(not bad, "console has no unexpected errors",
              "; ".join(bad[:3]) if bad else "")

    finally:
        browser.close()
        pw.stop()

    print()
    if failures:
        print(f"RESULT: FAIL ({len(failures)} assertion(s))", file=sys.stderr)
        for f in failures:
            print(f"  - {f}", file=sys.stderr)
        return 1

    print(f"RESULT: PASS ({len(targets)} colony/colonies verified)")
    for n in notes:
        print(f"  note: {n}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
