#!/usr/bin/env python3
"""UI E2E gate: a colony must appear in the sidebar AND on its colony page.

Drives the real Hive UI through Orca's embedded browser. Every assertion reads
live DOM state; nothing is inferred from the API or the disk (the API is used
only to learn the name the runtime gives the colony).

Usage:
    ./verify_colony_ui.py ui_e2e_012144
    ./verify_colony_ui.py ui_e2e_012144 --expect-fail   # negative control

Exit 0 = PASS, 1 = FAIL, 2 = harness error (browser/frontend unreachable —
kept distinct so a broken rig never reads as a broken feature).

Two things this harness has to absorb, both learned the hard way:
  1. Hive is an SPA holding open SSE streams, so a page never reaches
     "load finished" and goto reports browser_navigation_failed. Non-fatal.
  2. On a first-run profile the tutorial overlay auto-plays behind a
     `role=dialog[aria-modal=true]` scrim that swallows clicks on the sidebar.
     A gate that ignores this fails for a reason that has nothing to do with
     colonies.
"""
import argparse
import json
import subprocess
import sys
import time

ORCA = "/usr/local/bin/orca"
BASE = "http://127.0.0.1:8788"

TUTORIAL_SEEN = "tutorial-seen-v1"


def slug_to_colony_id(slug: str) -> str:
    """Mirror of frontend src/lib/colony-registry.ts::slugToColonyId."""
    cid = slug.replace("_", "-")
    if cid.startswith("email-"):
        cid = cid[len("email-"):]
    if cid.endswith("-agent"):
        cid = cid[: -len("-agent")]
    return cid


def _curl(url: str) -> str:
    return subprocess.run(["curl", "-s", url], capture_output=True, text=True, timeout=30).stdout


def expected_display_name(slug: str) -> str:
    """The renderer title-cases with its own rule (it produced "Ui E2e" while
    the runtime says "Ui E2E"), so don't re-implement it — ask the runtime what
    this colony is called and assert the UI agrees with *that*."""
    for _cat, arr in json.loads(_curl(f"{BASE}/api/discover")).items():
        for c in arr:
            if (c.get("path") or "").endswith("/" + slug) and c.get("name"):
                return c["name"]
    raise RuntimeError(f"{slug} not in /api/discover — does the colony exist?")


def _orca_once(*args):
    p = subprocess.run([ORCA, *args, "--json"], capture_output=True, text=True, timeout=120)
    if not p.stdout.strip():
        raise RuntimeError(f"orca {' '.join(args)}: no output ({p.stderr.strip()[:200]})")
    try:
        payload = json.loads(p.stdout)
    except json.JSONDecodeError:
        raise RuntimeError(f"orca {' '.join(args)}: non-JSON {p.stdout[:200]}")
    if not payload.get("ok"):
        raise RuntimeError(f"orca {' '.join(args)}: {json.dumps(payload.get('error'))[:300]}")
    return payload["result"]


def orca(*args, retries=4):
    """Orca returns runtime_unavailable when momentarily busy; that is a
    transport blip, not a broken UI, so retry before failing the harness."""
    last = None
    for attempt in range(retries):
        if attempt:
            time.sleep(3)
        try:
            return _orca_once(*args)
        except RuntimeError as e:
            last = e
            if "runtime_unavailable" not in str(e):
                raise
    raise last


def ev(page: str, expr: str):
    return json.loads(orca("eval", "--page", page, "--expression", expr)["result"])


def goto(page: str, url: str):
    """SSE keeps the load event from ever settling; treat as non-fatal."""
    try:
        orca("goto", "--page", page, "--url", url)
    except RuntimeError as e:
        if "browser_navigation_failed" not in str(e):
            raise


def dismiss_tutorial(page: str) -> bool:
    """Press Escape until the tutorial scrim is gone. Escape → finish() →
    sets the per-user seen flag, so this only ever happens once per profile."""
    for _ in range(6):
        if not ev(page, "JSON.stringify(!!document.querySelector('[role=dialog][aria-modal=\"true\"]'))"):
            return True
        try:
            orca("keypress", "--page", page, "--key", "Escape")
        except RuntimeError:
            ev(page, "JSON.stringify((dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true})),true))")
        time.sleep(1.2)
    return not ev(page, "JSON.stringify(!!document.querySelector('[role=dialog][aria-modal=\"true\"]'))")


def wait_for_colony_links(page: str, timeout: float = 60) -> int:
    """Poll until the sidebar has rendered colony links. Dismisses the tutorial
    if it turns up mid-wait. Returns the final link count (0 on timeout)."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            n = ev(page, "JSON.stringify(document.querySelectorAll(\"a[href*='/colony/']\").length)")
            if isinstance(n, int) and n > 0:
                return n
            dismiss_tutorial(page)
        except RuntimeError:
            pass
        time.sleep(2)
    return 0


def check(results, name, ok, detail=""):
    print(f"  {'PASS' if ok else 'FAIL'}  {name}" + (f" — {detail}" if detail else ""), flush=True)
    results.append(bool(ok))
    return ok


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("colony", help="colony directory name, e.g. ui_e2e_012144")
    ap.add_argument("--expect-fail", action="store_true",
                    help="negative control: assert the gate FAILS for this name")
    args = ap.parse_args()

    colony_id = slug_to_colony_id(args.colony)
    try:
        display = expected_display_name(args.colony)
    except Exception as e:
        print(f"HARNESS ERROR: {e}")
        return 2
    print(f"== colony gate: {args.colony} -> label {display!r}, route /colony/{colony_id} ==\n")

    page = None
    results = []
    try:
        page = orca("tab", "create", "--url", BASE)["browserPageId"]
        wait_for_colony_links(page)

        # ---------- SIDEBAR ----------
        goto(page, BASE)
        n = wait_for_colony_links(page)
        side = ev(page, r"""JSON.stringify((() => {
          const links = [...document.querySelectorAll("a[href*='/colony/']")];
          const badge = [...document.querySelectorAll('button')]
            .map(b => b.textContent.trim())
            .find(t => /^(蜂群|Colonies?)\s*\d+$/i.test(t)) || null;
          return { hrefs: links.map(a => a.getAttribute('href')),
                   labels: links.map(a => a.textContent.trim()),
                   badge,
                   bodyLen: (document.body.innerText || '').length };
        })())""")

        print("sidebar:")
        check(results, "sidebar rendered", n > 0, f"{n} colony link(s)")
        check(results, f"link for {colony_id}",
              any(colony_id in h for h in side["hrefs"]), ", ".join(side["hrefs"]))
        check(results, f"label {display!r}", display in side["labels"], ", ".join(side["labels"]))
        check(results, "colony count badge", side["badge"] is not None, str(side["badge"]))

        # ---------- COLONY PAGE (reached by clicking the sidebar entry) ----------
        print("\ncolony page:")
        target = next((h for h in side["hrefs"] if colony_id in h), None)
        check(results, "sidebar entry is clickable target", target is not None, str(target))
        if target:
            ev(page, f"""JSON.stringify((() => {{
              const a = [...document.querySelectorAll("a[href*='/colony/']")]
                .find(x => x.getAttribute('href').includes({json.dumps(colony_id)}));
              a.click(); return true; }})())""")
            time.sleep(3)
            dismiss_tutorial(page)

        st = ev(page, r"""JSON.stringify((() => ({
          url: location.href,
          h1: [...document.querySelectorAll('h1')].map(e => e.textContent.trim()),
          errorPage: /not found|404|something went wrong|出错了|加载失败/i.test(document.body.innerText || ''),
          text: (document.body.innerText || '').slice(0, 6000),
        }))())""")
        check(results, "route is the colony page", colony_id in st["url"], st["url"])
        check(results, "h1 names the colony", display in st["h1"], f"h1={st['h1']}")
        check(results, "colony name in page body",
              args.colony in st["text"] or display in st["text"])
        check(results, "not an error page", not st["errorPage"])

        cons = orca("console", "--page", page, "--limit", "40").get("messages", [])
        errs = [m for m in cons if m.get("level") == "error"]
        check(results, "no console errors", not errs,
              (errs[0].get("text", "")[:120] if errs else ""))
    except Exception as e:
        print(f"\nHARNESS ERROR: {e}")
        return 2
    finally:
        if page:
            try:
                orca("tab", "close", "--page", page)
            except Exception:
                pass

    passed = all(results)
    if args.expect_fail:
        print(f"\nNEGATIVE CONTROL: gate {'correctly FAILED' if not passed else 'WRONGLY PASSED'}")
        return 0 if not passed else 1
    print(f"\n{'PASS' if passed else 'FAIL'}  ({sum(results)}/{len(results)} assertions)")
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
