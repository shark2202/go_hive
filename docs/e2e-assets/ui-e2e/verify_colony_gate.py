#!/usr/bin/env python3
"""UI E2E gate — a colony MUST appear in the sidebar AND on its colony page.

This is the consolidated gate. It replaces four overlapping scripts that were
written independently and disagreed with each other; two of them failed or
crashed on a green system, and one printed PASS while its own output showed the
home page. Read docs/e2e-assets/ui-e2e/REPORT.md for that history.

    ./verify_colony_gate.py ui_e2e_012144                # assert it surfaces
    ./verify_colony_gate.py ui_e2e_012144 --expect-fail  # proves the gate can fail

Exit codes are deliberately three-valued so a broken rig never reads as a
broken feature:
    0 = PASS      1 = FAIL (product)      2 = HARNESS (browser/serve unreachable)

The loading window matters. On a COLD deep-link the colony page paints the raw
route id as <h1> ("ui-e2e-012144") and the sidebar has 0 colony links until
fetchColonies() resolves; it settles a moment later. Assertions therefore wait
for a predicate with a deadline instead of sampling once — sampling once is what
made an earlier gate fail on a healthy system.
"""
import argparse
import json
import os
import subprocess
import sys
import time

ORCA = os.environ.get("ORCA_CLI", "/usr/local/bin/orca")
BASE = os.environ.get("HIVE_BASE", "http://127.0.0.1:8788")
SETTLE_S = float(os.environ.get("HIVE_GATE_SETTLE", "20"))

PASS, FAIL, HARNESS = 0, 1, 2


class Harness(Exception):
    """The rig is broken (browser/serve/orca), not the feature."""


def slug_to_colony_id(slug: str) -> str:
    """Mirror of frontend src/lib/colony-registry.ts::slugToColonyId."""
    cid = slug.replace("_", "-")
    if cid.startswith("email-"):
        cid = cid[len("email-"):]
    if cid.endswith("-agent"):
        cid = cid[: -len("-agent")]
    return cid


def slug_to_display_name(slug: str) -> str:
    """Mirror of slugToDisplayName (no COLONY_DISPLAY_NAME_OVERRIDES apply here)."""
    return " ".join(w[:1].upper() + w[1:] for w in slug.split("_") if w)


def orca(*args, timeout=120):
    cmd = [ORCA, *args, "--json"]
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    except FileNotFoundError:
        raise Harness(f"orca CLI not found at {ORCA}")
    except subprocess.TimeoutExpired:
        raise Harness(f"orca {' '.join(args)} timed out after {timeout}s")
    if not p.stdout.strip():
        raise Harness(f"orca {' '.join(args)}: no output (stderr: {p.stderr.strip()[:200]})")
    try:
        payload = json.loads(p.stdout)
    except json.JSONDecodeError:
        raise Harness(f"orca {' '.join(args)}: non-JSON output {p.stdout[:200]}")
    if not payload.get("ok"):
        raise Harness(f"orca {' '.join(args)}: {json.dumps(payload.get('error'))[:300]}")
    return payload["result"]


def ev(page, expr):
    raw = orca("eval", "--page", page, "--expression", expr)["result"]
    return json.loads(raw)


def wait_for(page, expr, pred, want, timeout=SETTLE_S, interval=0.5):
    """Poll a predicate until it holds or the deadline passes.

    Returns (ok, last_value). This exists because the colony page has a real
    loading window; a single sample races it and reports a false failure.
    """
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            last = ev(page, expr)
        except Harness:
            time.sleep(interval)
            continue
        if pred(last):
            return True, last
        time.sleep(interval)
    return False, last


def discover_name(slug):
    """The label the sidebar should render, from the same API the UI reads."""
    import urllib.request
    try:
        with urllib.request.urlopen(f"{BASE}/api/discover", timeout=10) as r:
            d = json.load(r)
    except Exception as e:
        raise Harness(f"cannot reach {BASE}/api/discover: {e}")
    for items in d.values():
        if not isinstance(items, list):
            continue
        for c in items:
            if (c.get("path") or "").rstrip("/").endswith("/colonies/" + slug):
                return c.get("name") or ""
    return ""


def disk_created_at(slug):
    p = os.path.expanduser(f"~/.hive-go/colonies/{slug}/metadata.json")
    try:
        with open(p) as f:
            return json.load(f).get("created_at")
    except Exception:
        return None


def api_created_at(slug):
    import urllib.request
    try:
        with urllib.request.urlopen(f"{BASE}/api/colonies/tools-index", timeout=10) as r:
            d = json.load(r)
    except Exception as e:
        raise Harness(f"cannot reach tools-index: {e}")
    for c in d.get("colonies", []):
        if c.get("name") == slug:
            return c.get("created_at")
    return None


class Report:
    def __init__(self):
        self.rows = []

    def check(self, name, ok, detail=""):
        self.rows.append((ok, name, detail))
        print(f"  {'PASS' if ok else 'FAIL'}  {name}" + (f" — {detail}" if detail else ""))
        return ok


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("colony", help="colony directory name, e.g. ui_e2e_012144")
    ap.add_argument("--expect-fail", action="store_true",
                    help="negative control: require that the gate FAILS for this colony")
    args = ap.parse_args()

    slug = args.colony
    cid = slug_to_colony_id(slug)
    display = slug_to_display_name(slug)
    print(f"colony={slug}  route_id={cid}  display={display!r}  base={BASE}\n")

    # Serve must be up before anything else, so a dead server reads as HARNESS.
    try:
        import urllib.request
        urllib.request.urlopen(BASE + "/", timeout=5).read(1)
    except Exception as e:
        print(f"HARNESS ERROR: serve not reachable at {BASE}: {e}")
        return HARNESS

    try:
        page = orca("tab", "create", "--url", BASE + "/")["browserPageId"]
    except Harness as e:
        print(f"HARNESS ERROR: {e}")
        return HARNESS

    rep = Report()
    try:
        # ---------- 1. SIDEBAR ----------
        print("sidebar:")
        side_expr = r"""JSON.stringify((() => {
          const links = [...document.querySelectorAll("a[href*='/colony/']")];
          const badge = [...document.querySelectorAll('button')]
            .map(b => b.textContent.trim())
            .find(t => /^蜂群\s*\d+$|^Colon(y|ies)\s*\d+$/i.test(t)) || null;
          return {
            hrefs: links.map(a => a.getAttribute('href')),
            labels: links.map(a => a.textContent.trim()),
            badge, bodyLen: (document.body.innerText || '').length,
          };
        })())"""

        ok, side = wait_for(page, side_expr,
                            lambda v: v and any(cid in h for h in v.get("hrefs", [])),
                            True)
        side = side or {"hrefs": [], "labels": [], "badge": None, "bodyLen": 0}

        rep.check("frontend rendered", side.get("bodyLen", 0) > 200,
                  f"body={side.get('bodyLen')} chars")
        rep.check(f"sidebar has link for {cid!r}",
                  any(cid in h for h in side.get("hrefs", [])),
                  f"{len(side.get('hrefs', []))} colony links found")

        # The rendered label must match what the API says, not a guess.
        want = discover_name(slug)
        if want:
            rep.check(f"sidebar renders label {want!r}", want in side.get("labels", []),
                      ", ".join(side.get("labels", [])[:6]))
        else:
            rep.check(f"sidebar renders label {display!r}", display in side.get("labels", []),
                      ", ".join(side.get("labels", [])[:6]))

        rep.check("colony count badge present", side.get("badge") is not None, str(side.get("badge")))

        # ---------- 2. COLONY PAGE ----------
        print("\ncolony page:")
        page_expr = r"""JSON.stringify((() => {
          const h1 = [...document.querySelectorAll('h1')].map(e => e.textContent.trim());
          return {
            url: location.href, h1,
            textLen: (document.body.innerText || '').length,
            errorPage: /not found|404|something went wrong|出错了|加载失败/i.test(document.body.innerText || ''),
          };
        })())"""
        labels = {display, want} - {""}

        # Navigate the way a user would: click the sidebar entry.
        try:
            ev(page, f"""JSON.stringify((() => {{
              const a = [...document.querySelectorAll("a[href*='/colony/']")]
                .find(x => x.getAttribute('href').includes({json.dumps(cid)}));
              if (a) {{ a.click(); return true; }} return false;
            }})())""")
        except Harness:
            pass

        # Then wait for the page to settle on the colony identity. On a cold
        # deep-link h1 is briefly the raw route id; that is the loading window,
        # not a failure — hence the predicate instead of a single sample.
        ok, st = wait_for(page, page_expr,
                          lambda v: v and cid in v.get("url", "")
                          and not v.get("errorPage")
                          and any(l in v.get("h1", []) for l in labels),
                          True)
        st = st or {"url": "", "h1": [], "textLen": 0, "errorPage": False}

        rep.check("route is the colony page", cid in st.get("url", ""), st.get("url", ""))
        rep.check("h1 names the colony (after settle)",
                  any(l in st.get("h1", []) for l in labels),
                  f"h1={st.get('h1')}")
        rep.check("not an error page", not st.get("errorPage"))

        # ---------- 3. CONSOLE ----------
        try:
            cons = orca("console", "--page", page, "--limit", "40")
            msgs = cons.get("messages") or cons.get("console") or []
            errs = [m for m in msgs if isinstance(m, dict) and m.get("level") == "error"]
            rep.check("no console errors", not errs,
                      f"{len(errs)} error(s)" + (f": {errs[0].get('text','')[:100]}" if errs else ""))
        except Harness as e:
            rep.check("console readable", False, str(e)[:120])

        # ---------- 4. UI vs BACKEND vs DISK ----------
        print("\nconsistency:")
        api_ts = api_created_at(slug)
        disk_ts = disk_created_at(slug)
        rep.check("colony present in /api/colonies/tools-index", api_ts is not None, str(api_ts))
        rep.check("created_at agrees between API and disk",
                  api_ts is not None and api_ts == disk_ts,
                  f"api={api_ts} disk={disk_ts}")

    except Harness as e:
        print(f"\nHARNESS ERROR: {e}")
        return HARNESS
    finally:
        subprocess.run([ORCA, "tab", "close", "--page", page, "--json"],
                       capture_output=True, text=True, timeout=60)

    passed = all(ok for ok, _, _ in rep.rows)
    if args.expect_fail:
        print(f"\nNEGATIVE CONTROL: gate {'correctly FAILED' if not passed else 'WRONGLY PASSED'}")
        return PASS if not passed else FAIL

    print(f"\n{'PASS' if passed else 'FAIL'}  ({sum(1 for ok,_,_ in rep.rows if ok)}/{len(rep.rows)} assertions)")
    return PASS if passed else FAIL


if __name__ == "__main__":
    sys.exit(main())
