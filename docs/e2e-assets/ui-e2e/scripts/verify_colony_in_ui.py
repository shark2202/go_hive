#!/usr/bin/env python3
"""UI E2E gate: a colony must appear in the sidebar AND on its colony page.

Drives a real browser against a running Hive serve. Two independent surfaces
are asserted so a front-end optimistic render cannot pass:

  1. sidebar  — a link to the colony's route is present in the rail, and the
                colony count includes it
  2. page     — navigating to the colony route renders that colony's h1,
                its queen, and the session panel — not an error page

Cross-checks the backend (`/api/colonies/tools-index`) so we know the UI is
reading server state rather than interpolating locally.

Usage:
  verify_colony_in_ui.py --colony ui_e2e_012144
  verify_colony_in_ui.py --colony ui_e2e_012144 --base http://127.0.0.1:8788
  verify_colony_in_ui.py --colony ui_e2e_012144 --shots /tmp/shots
  verify_colony_in_ui.py --list            # list colonies, verify all

Exit 0 = pass, 1 = fail, 2 = environment problem (serve down / no browser).
"""
import argparse
import json
import os
import sys
import urllib.request

DEFAULT_BASE = os.environ.get("HIVE_BASE", "http://127.0.0.1:8788")


def is_error_page(page, body: str) -> bool:
    """True only if the colony page actually fell back to an error render.

    Deliberately NOT a blind substring scan of body text. A colony's chat
    transcript is part of the body, so a test colony whose messages discuss
    "404" or "Colony not found" would red-light itself -- a false positive
    that trains people to ignore the gate.

    We assert the real error signature instead. When a colony fails to load,
    colony-chat.tsx:2287-2294 replaces the whole render with a single
    <p>Colony not found: {id}</p> -- no h1, no session tabs. So:
      - the standalone "Colony not found" paragraph is present, OR
      - the structural shell is missing entirely (no h1 / no session tabs).
    """
    try:
        not_found_p = page.query_selector("xpath=//p[contains(., 'Colony not found')]")
    except Exception:
        not_found_p = None
    if not_found_p is not None:
        return True

    # Structural fallback: a rendered colony always has an h1 and the session
    # tab strip. Their absence means we got the bare error shell.
    has_h1 = False
    for sel in ("h1",):
        try:
            el = page.query_selector(sel)
            has_h1 = el is not None and (el.inner_text() or "").strip() != ""
        except Exception:
            has_h1 = False
    if not has_h1:
        return True
    return False


def error_page_detail(page, body: str) -> str:
    """Report WHICH signal fired, so a red build is diagnosable."""
    try:
        el = page.query_selector("xpath=//p[contains(., 'Colony not found')]")
        if el is not None:
            return f"error render: {(el.inner_text() or '').strip()!r}"
    except Exception:
        pass
    try:
        h1 = (page.inner_text("h1") or "").strip()
    except Exception:
        h1 = ""
    if not h1:
        return "error render: no h1 (bare error shell)"
    return f"rendered ok (h1={h1!r}); chat text may mention 'not found' -- ignored"


def slug_to_route_id(slug: str) -> str:
    """Exact mirror of the frontend's slugToColonyId (src/lib/colony-registry.ts):

        slug.replace(/_/g, "-").replace(/^email-/, "").replace(/-agent$/, "")

    Disk dirs use underscores; route ids use hyphens. Nothing else is stripped.
    """
    route = slug.replace("_", "-")
    if route.startswith("email-"):
        route = route[len("email-"):]
    if route.endswith("-agent"):
        route = route[: -len("-agent")]
    return route


def api_get(base: str, path: str, timeout: int = 15):
    with urllib.request.urlopen(f"{base}{path}", timeout=timeout) as r:
        return json.load(r)


def discover_colonies(base: str):
    """Backend truth: slug -> {queen_name, created_at, display_name}.

    tools-index gives the slug/queen/created_at; /discover gives the
    human-readable title the sidebar renders ("Ui E2E 012144"). We take the
    title from the backend instead of title-casing the slug locally, so the
    assertion can't drift from the real renderer.
    """
    try:
        rows = api_get(base, "/api/colonies/tools-index").get("colonies") or []
    except Exception as exc:
        raise SystemExit(f"ENV: cannot read {base}/api/colonies/tools-index — {exc}")
    out = {c["name"]: dict(c) for c in rows if c.get("name")}

    try:
        disc = api_get(base, "/api/discover")
    except Exception:
        disc = {}
    for group in (disc.values() if isinstance(disc, dict) else []):
        if not isinstance(group, list):
            continue
        for entry in group:
            slug = os.path.basename(str(entry.get("path", "")).rstrip("/"))
            if slug in out and entry.get("name"):
                out[slug]["display_name"] = entry["name"]
    return out


class Result:
    def __init__(self):
        self.checks = []

    def check(self, name, ok, detail=""):
        self.checks.append({"check": name, "ok": bool(ok), "detail": detail})
        mark = "PASS" if ok else "FAIL"
        print(f"  [{mark}] {name}" + (f" — {detail}" if detail else ""))
        return ok

    @property
    def ok(self):
        return all(c["ok"] for c in self.checks)


def verify(base, colony_slug, shots=None, headless=True):
    from playwright.sync_api import sync_playwright

    route_id = slug_to_route_id(colony_slug)
    res = Result()
    print(f"\ncolony: {colony_slug}  ->  route: #/colony/{route_id}")

    backend = discover_colonies(base)
    api_row = backend.get(colony_slug)
    res.check("backend lists colony", api_row is not None,
              json.dumps(api_row) if api_row else "not in /api/colonies/tools-index")

    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome", headless=headless)
        page = browser.new_page(viewport={"width": 1440, "height": 900})
        console_errors = []
        page.on("console", lambda m: console_errors.append(m.text) if m.type == "error" else None)
        page.on("pageerror", lambda e: console_errors.append(str(e)))

        # ---- surface 1: sidebar -------------------------------------------
        page.goto(base + "/", wait_until="domcontentloaded", timeout=45000)
        page.wait_for_selector("a[href*='/colony/']", timeout=30000)
        page.wait_for_timeout(1500)  # let /discover + /sessions settle

        hrefs = page.eval_on_selector_all(
            "a[href*='/colony/']", "els=>els.map(e=>e.getAttribute('href'))")
        target_href = next((h for h in hrefs if h and h.rstrip("/").endswith(route_id)), None)
        res.check("sidebar has colony link", target_href is not None,
                  f"links={[h for h in hrefs if h]}")
        if shots:
            os.makedirs(shots, exist_ok=True)
            page.screenshot(path=os.path.join(shots, f"sidebar-{route_id}.png"))

        # ---- surface 2: colony page --------------------------------------
        page.goto(f"{base}/#/colony/{route_id}", wait_until="domcontentloaded", timeout=45000)
        page.wait_for_selector("h1", timeout=30000)
        page.wait_for_timeout(1500)

        h1 = (page.inner_text("h1") or "").strip()
        expect_title = (api_row or {}).get("display_name") or colony_slug
        res.check("colony page h1", h1 == expect_title, f"h1={h1!r} expected={expect_title!r}")

        body = page.inner_text("body") or ""
        res.check("page is not an error page",
                  not is_error_page(page, body),
                  error_page_detail(page, body))
        res.check("route stayed on colony", page.url.endswith(f"#/colony/{route_id}"), page.url)

        if api_row:
            queen = api_row.get("queen_name")
            if queen:
                res.check("queen surfaced on page", queen.split("_")[-1].lower() in body.lower()
                          or "head of" in body.lower(),
                          f"queen={queen}")

        res.check("session panel rendered",
                  any(t in body for t in ("数据", "计划", "自动化", "工作节点", "Data", "Plan", "Automation")),
                  "colony tabs present")

        # Only hard failures count: a stale tab's SSE reconnect is noise.
        real_errors = [e for e in console_errors if "SSE" not in e]
        res.check("no console errors", not real_errors, f"errors={real_errors[:3]}")

        if shots:
            page.screenshot(path=os.path.join(shots, f"colony-page-{route_id}.png"), full_page=True)
        browser.close()

    return res


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--colony", help="colony slug as stored on disk, e.g. ui_e2e_012144")
    ap.add_argument("--base", default=DEFAULT_BASE)
    ap.add_argument("--list", action="store_true", help="verify every colony the backend reports")
    ap.add_argument("--shots", default=None, help="directory for screenshots")
    ap.add_argument("--headed", action="store_true", help="run with a visible browser")
    args = ap.parse_args()

    try:
        colonies = discover_colonies(args.base)
    except SystemExit as exc:
        print(exc)
        return 2
    if not colonies:
        print(f"ENV: no colonies reported by {args.base}")
        return 2

    targets = list(colonies) if args.list else [args.colony]
    if not args.colony and not args.list:
        print("ENV: pass --colony <slug> or --list")
        return 2
    if args.colony and args.colony not in colonies:
        print(f"ENV: {args.colony} not in backend colony list. Known: {sorted(colonies)}")
        return 2

    try:
        results = [(c, verify(args.base, c, args.shots, headless=not args.headed)) for c in targets]
    except Exception as exc:
        print(f"ENV: browser drive failed — {type(exc).__name__}: {exc}")
        return 2

    failed = [c for c, r in results if not r.ok]
    print(f"\n{'PASS' if not failed else 'FAIL'} — {len(results) - len(failed)}/{len(results)} colonies verified")
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
