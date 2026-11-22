#!/usr/bin/env python3
"""
Probe: does a colony created ON DISK outside the UI appear in the sidebar
without a page reload?

Claim under test (mine): it does NOT -- fetchColonies only runs on mount,
visibilitychange, and after create/delete; the 30s poll is fetchStatus
(/sessions only, .map over existing rows) and cannot add a row.

Counter-claim in REPORT.md: "运行期间侧栏还实时冒出了别的 agent 新建的
disk-only-test-0127、ui-e2e-refresh-probe -- 说明列表是实时读后端".

Sequence, all in ONE page instance (a reload would invalidate the test):
  1. load /, settle, record baseline sidebar hrefs
  2. create ~/.hive-go/colonies/<probe>/ on disk (out of band, no browser)
  3. poll the DOM for 60s (> the 30s fetchStatus interval) WITHOUT touching
     the page -- no goto, no reload, no resize
  4. record whether the probe link appeared
  5. THEN reload and confirm it appears, proving the disk state is valid
"""
import json, os, shutil, subprocess, sys, time

from playwright.sync_api import sync_playwright

BASE = os.environ.get("HIVE_BASE", "http://127.0.0.1:8788")
PROBE = "oob_probe_zz"
HIVE = os.path.expanduser("~/.hive-go")
PROBE_DIR = os.path.join(HIVE, "colonies", PROBE)


def make_probe():
    """Match the layout the create flow writes (metadata + worker)."""
    os.makedirs(os.path.join(PROBE_DIR, "conversations", "parts"), exist_ok=True)
    os.makedirs(os.path.join(PROBE_DIR, "runs"), exist_ok=True)
    os.makedirs(os.path.join(PROBE_DIR, "skills"), exist_ok=True)
    os.makedirs(os.path.join(PROBE_DIR, "tracker"), exist_ok=True)
    with open(os.path.join(PROBE_DIR, "metadata.json"), "w") as f:
        json.dump({"created_at": time.time(), "name": PROBE,
                   "queen_name": "queen_growth"}, f)
    with open(os.path.join(PROBE_DIR, "worker.json"), "w") as f:
        json.dump({"description": "Colony " + PROBE,
                   "goal": {"description": "Work on tasks for colony " + PROBE},
                   "name": "Oob Probe Zz", "queen_name": "queen_growth",
                   "tools": []}, f)
    with open(os.path.join(PROBE_DIR, "triggers.json"), "w") as f:
        f.write("[]")
    with open(os.path.join(PROBE_DIR, "conversations", "cursor.json"), "w") as f:
        f.write('{"seq": 0}')


def cleanup():
    shutil.rmtree(PROBE_DIR, ignore_errors=True)


def backend_has_probe():
    try:
        out = subprocess.run(
            ["curl", "-s", "--max-time", "8", BASE + "/api/colonies/tools-index"],
            capture_output=True, text=True, timeout=20).stdout
        return PROBE in out
    except Exception as e:
        return "curl-error: %s" % e


def links(page):
    return sorted(set(page.eval_on_selector_all(
        "a[href*='/colony/']", "els => els.map(e => e.getAttribute('href'))")))


def main():
    out = {"probe": PROBE, "base": BASE}
    cleanup()
    assert not os.path.exists(PROBE_DIR)

    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome", headless=True)
        page = browser.new_page(viewport={"width": 1440, "height": 900})
        errs = []
        page.on("pageerror", lambda e: errs.append(str(e)))

        # 1. baseline
        page.goto(BASE + "/", wait_until="load", timeout=45000)
        time.sleep(4)
        base_links = links(page)
        out["baseline_links"] = base_links
        out["baseline_has_probe"] = any(PROBE.replace("_", "-") in h for h in base_links)
        print("baseline: %d links, probe present=%s"
              % (len(base_links), out["baseline_has_probe"]), flush=True)

        # 2. create on disk, out of band
        make_probe()
        out["disk_created"] = os.path.isdir(PROBE_DIR)
        time.sleep(1)
        out["backend_sees_probe"] = backend_has_probe()
        print("probe on disk; backend sees it = %s" % out["backend_sees_probe"], flush=True)

        # 3. watch WITHOUT touching the page, past the 30s poll
        target = PROBE.replace("_", "-")
        appeared_at = None
        samples = []
        for i in range(24):  # 24 * 2.5s = 60s
            time.sleep(2.5)
            ls = links(page)
            seen = any(target in h for h in ls)
            samples.append({"t": round(2.5 * (i + 1), 1), "n": len(ls), "seen": seen})
            if seen and appeared_at is None:
                appeared_at = round(2.5 * (i + 1), 1)
                break
        out["poll_samples"] = samples
        out["appeared_without_reload"] = appeared_at is not None
        out["appeared_after_sec"] = appeared_at
        print("after 60s no-touch watch: appeared=%s at=%s"
              % (out["appeared_without_reload"], appeared_at), flush=True)

        # 4. reload -- proves the disk layout is valid
        page.reload(wait_until="load", timeout=45000)
        time.sleep(4)
        after = links(page)
        out["after_reload_links"] = after
        out["present_after_reload"] = any(target in h for h in after)
        print("after reload: probe present = %s" % out["present_after_reload"], flush=True)

        out["pageerrors"] = errs
        browser.close()

    cleanup()
    out["cleaned_up"] = not os.path.exists(PROBE_DIR)

    if out["appeared_without_reload"]:
        verdict = "REFUTED: a disk-only colony DID appear live"
    elif out["present_after_reload"]:
        verdict = "CONFIRMED: disk-only colony does NOT appear without a reload"
    else:
        verdict = "INCONCLUSIVE: probe never rendered even after reload"
    out["verdict"] = verdict
    print(json.dumps(out, indent=2))
    print("\nVERDICT:", verdict)
    return 0 if "CONFIRMED" in verdict or "REFUTED" in verdict else 1


if __name__ == "__main__":
    sys.exit(main())
