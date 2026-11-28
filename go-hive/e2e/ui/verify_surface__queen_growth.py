#!/usr/bin/env python3
"""UI E2E gate (queen_growth): colony must appear in sidebar AND colony page.

Isolated: launches installed Chrome via Playwright with a throwaway profile,
so it never touches the user's Chrome session or the Hive Browser Bridge.
Uniquely named to avoid the shared-file collisions in this directory.
"""
import argparse, json, re, sys, urllib.request


def slug_to_colony_id(slug):
    out = slug.replace("_", "-")
    if out.startswith("email-"):
        out = out[len("email-"):]
    if out.endswith("-agent"):
        out = out[: -len("-agent")]
    return out


def fetch_colonies(base):
    with urllib.request.urlopen(f"{base}/api/discover", timeout=15) as r:
        payload = json.load(r)
    rows = []
    for _c, entries in payload.items():
        if not isinstance(entries, list):
            continue
        for e in entries:
            if not isinstance(e, dict):
                continue
            path = e.get("path") or ""
            slug = path.rstrip("/").rsplit("/", 1)[-1] if path else ""
            if e.get("name") and slug:
                rows.append({"name": e["name"], "slug": slug})
    return rows


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--colony", default="ui_e2e_012144")
    ap.add_argument("--base", default="http://127.0.0.1:8788")
    ap.add_argument("--shot", default="")
    a = ap.parse_args()

    fails = []
    def check(ok, label, detail=""):
        print(f"  [{'PASS' if ok else 'FAIL'}] {label}" + (f" — {detail}" if detail else ""))
        if not ok:
            fails.append(label + (f" — {detail}" if detail else ""))

    try:
        backend = fetch_colonies(a.base)
    except Exception as e:
        print(f"ERROR contacting {a.base}: {e}", file=sys.stderr); return 2
    by_slug = {c["slug"]: c for c in backend}
    target_id = slug_to_colony_id(a.colony)
    expected_name = by_slug.get(a.colony, {}).get("name", a.colony)

    print(f"Hive UI colony-surface gate (independent)")
    print(f"  slug {a.colony} -> route {target_id}; backend has {len(backend)} colonies")
    check(a.colony in by_slug, "backend reports the colony", f"{a.colony}")

    from playwright.sync_api import sync_playwright
    with sync_playwright() as p:
        b = p.chromium.launch(headless=True, channel="chrome")
        ctx = b.new_context(viewport={"width": 1440, "height": 1200})
        # A fresh profile has no "tutorial seen" flag, so the first-run tour
        # auto-plays and its modal overlay swallows clicks in the sidebar.
        # Real users dismiss it once; the gate seeds that flag so it tests
        # colony surfacing rather than the onboarding flow.
        ctx.add_init_script("""
            (() => {
              try {
                const seen = ["tutorial-seen-v1"];
                const keys = new Set(["_"]);
                for (let i = 0; i < localStorage.length; i++) {
                  const m = /^hive:u:([^:]+):/.exec(localStorage.key(i) || "");
                  if (m) keys.add(m[1]);
                }
                for (const k of keys) {
                  for (const s of seen) localStorage.setItem(`hive:u:${k}:${s}`, "true");
                }
              } catch (e) {}
            })();
        """)
        page = ctx.new_page()
        logs = []
        page.on("console", lambda m: logs.append(f"{m.type}: {m.text}"))
        page.on("pageerror", lambda e: logs.append(f"pageerror: {e}"))
        try:
            page.goto(a.base, wait_until="domcontentloaded", timeout=30000)
            page.wait_for_selector("a[href*='/colony/']", timeout=25000)

            hrefs = page.eval_on_selector_all(
                "a[href*='/colony/']", "els => els.map(e => e.getAttribute('href'))")
            ids = {h.rsplit('/', 1)[-1] for h in hrefs if h}
            check(target_id in ids, "sidebar renders the colony link",
                  f"want /colony/{target_id}; got {len(ids)} links")

            # Only attempt the click-through if the link actually exists.
            # Otherwise the driver would sit on a 30s "waiting for locator"
            # timeout and the gate would hang instead of failing fast.
            if target_id not in ids:
                print("  [SKIP] click-through — no sidebar link to click")
                page.goto(f"{a.base}/#/colony/{target_id}",
                          wait_until="domcontentloaded", timeout=20000)
            else:
                page.click(f"a[href$='/colony/{target_id}']")
                page.wait_for_function("id => location.href.endsWith('/colony/' + id)",
                                       arg=target_id, timeout=15000)
            page.wait_for_selector("h1", timeout=15000)
            h1 = (page.inner_text("h1") or "").strip()
            body = page.inner_text("body")

            check(h1 == expected_name, "colony page h1 matches backend name",
                  f"want {expected_name!r} got {h1!r}")
            check("not found" not in body.lower() and "出错了" not in body,
                  "colony page is not an error page")
            if a.shot:
                page.screenshot(path=a.shot, full_page=True)
                print(f"  shot: {a.shot}")

            real = [l for l in logs
                    if l.startswith(("error:", "pageerror:"))
                    and "[SSE]" not in l]
            check(not real, "no unexpected console errors", "; ".join(real[:3]))
        finally:
            b.close()

    print()
    if fails:
        print(f"RESULT: FAIL ({len(fails)})", file=sys.stderr)
        for f in fails:
            print("  - " + f, file=sys.stderr)
        return 1
    print("RESULT: PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
