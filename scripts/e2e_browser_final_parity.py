#!/usr/bin/env python3
"""Final browser-parity double-run: upstream Python vs the Go port on one fixture.

Proves two residual closings with live evidence:

  1. Panel value parity (isHiveGroup / hive_group_count). Runs the REAL
     upstream handlers (framework.server.routes_workers.handle_browser_tabs /
     handle_browser_contexts) in-process against the same logical state the
     fake extension feeds the Go e2e (profile e2e-profile -> group 777; tabs
     101 in that group and 102 ungrouped), then runs the Go side for real via
     go_hive/scripts/e2e_browser_panel.sh (E2E_DUMP=1) and compares the two
     JSON bodies field for field. Upstream's hive-group set is always empty
     (it reads ctx.get("groupId") from list_active_contexts() rows keyed
     "group_id"), so both sides must emit isHiveGroup=false and
     hive_group_count=0.

  2. CLI validation parity (evaluate/script exit-6 conditions). Runs the REAL
     upstream cmd_evaluate / cmd_script handlers in-process and the real
     `hive-browser` binary (built from go_hive/go-hive) over the same case
     list, both with no connected extension, then compares exit codes. A
     connected-profile fake bridge is used for the --browser-profile cases
     (upstream validate_browser_profile vs the Go route's /status read).

Usage (from the repo root, with `go` on PATH):
    uv run --offline --project core python go_hive/scripts/e2e_browser_final_parity.py

Writes docs/fixtures/browser-final-parity/dual-run.txt and exits non-zero on
any mismatch. --skip-e2e reuses the last captured Go panel bodies instead of
re-running the e2e.
"""

from __future__ import annotations

import argparse
import asyncio
import io
import json
import os
import re
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CORE = ROOT / "core"
TOOLS = ROOT / "tools"
GO_HIVE = ROOT / "go_hive"
FIXTURE_DIR = GO_HIVE / "docs" / "fixtures" / "browser-final-parity"

# The fake-extension fixture the Go e2e drives (e2e_browser_panel_fakeext.py).
GROUP_ID = 777
PROFILE = "e2e-profile"
TABS = [
    {"id": 101, "url": "https://example.com/", "title": "Example", "groupId": GROUP_ID},
    {"id": 102, "url": "https://other.example/", "title": "Other", "groupId": -1},
]

sys.path.insert(0, str(CORE))
sys.path.insert(0, str(TOOLS / "src"))


class FakeBridge:
    """Upstream bridge stand-in for handle_browser_tabs/contexts."""

    is_connected = True

    def __init__(self, labels=None):
        self.labels = labels or []
        self.evaluate_calls = 0

    def connected_profiles(self):
        return [{"label": label} for label in self.labels]

    async def list_tabs(self, group_id=None):
        return {"tabs": [dict(tab) for tab in TABS]}

    async def evaluate(self, tab_id, js):
        self.evaluate_calls += 1
        return {"ok": True, "result": "42"}


def upstream_panel_bodies():
    """Run the real upstream panel handlers against the fixture state."""
    import gcu.browser.bridge as gbridge
    import gcu.browser.tools.lifecycle as lifecycle
    from framework.server.routes_workers import (
        handle_browser_contexts,
        handle_browser_tabs,
    )

    gbridge.get_bridge = lambda: FakeBridge()
    lifecycle._contexts.clear()
    lifecycle._contexts[PROFILE] = {
        "groupId": GROUP_ID,
        "name": PROFILE,
        "tabs": {101},
        "activeTabId": 101,
    }
    contexts = json.loads(asyncio.run(handle_browser_contexts(None)).body.decode())
    tabs = json.loads(asyncio.run(handle_browser_tabs(None)).body.decode())
    return contexts, tabs


def run_go_e2e() -> dict:
    """Run the real Go e2e and return the dumped panel bodies."""
    env = dict(os.environ, E2E_DUMP="1")
    result = subprocess.run(
        ["bash", str(GO_HIVE / "scripts" / "e2e_browser_panel.sh")],
        cwd=str(ROOT), env=env, capture_output=True, text=True, timeout=900,
    )
    output = result.stdout + result.stderr
    if result.returncode != 0 or "ALL PASS" not in output:
        raise RuntimeError(f"go e2e failed (rc={result.returncode}):\n{output[-4000:]}")
    bodies = {}
    for line in result.stdout.splitlines():
        match = re.match(r"^--- (status|contexts|tabs): (\{.*\})$", line.strip())
        if match:
            bodies[match.group(1)] = json.loads(match.group(2))
    missing = {"contexts", "tabs"} - bodies.keys()
    if missing:
        raise RuntimeError(f"e2e dump missing bodies: {missing}\n{output[-2000:]}")
    return bodies


def compare_panel(upstream_contexts, upstream_tabs, go_bodies) -> list[str]:
    """Return a list of mismatches (empty means byte-equal after JSON decode)."""
    mismatches = []
    if upstream_contexts != go_bodies["contexts"]:
        mismatches.append(
            "contexts body differs:\n  upstream: %s\n  go:       %s"
            % (json.dumps(upstream_contexts, sort_keys=True), json.dumps(go_bodies["contexts"], sort_keys=True))
        )
    if upstream_tabs != go_bodies["tabs"]:
        mismatches.append(
            "tabs body differs:\n  upstream: %s\n  go:       %s"
            % (json.dumps(upstream_tabs, sort_keys=True), json.dumps(go_bodies["tabs"], sort_keys=True))
        )
    return mismatches


# ── CLI validation twin runs ────────────────────────────────────────────────

def upstream_cli_cases(tmp: Path, fake_url: str | None) -> list[dict]:
    """Run the real upstream cmd_evaluate / cmd_script handlers, case by case."""
    import argparse

    import gcu.browser.bridge as gbridge
    import gcu.browser.tools.lifecycle as lifecycle  # noqa: F401  (import side effects)
    import gcu.browser.tools.script as script_tool
    import gcu.cli_commands.evaluate as ev
    import gcu.cli_commands.script as sc
    from gcu.errors import BrowserError, classify_result

    empty_js = tmp / "empty.js"
    empty_json = tmp / "empty.json"
    empty_js.write_text("")
    empty_json.write_text("")
    missing = tmp / "missing.js"

    recorded: dict = {}

    async def recorder(**kwargs):
        recorded.clear()
        recorded.update(kwargs)
        return {"ok": True}

    real_run = script_tool.run_browser_script

    def finish(name, fn, stub=False):
        recorded.clear()
        script_tool.run_browser_script = recorder if stub else real_run
        try:
            result = fn()
            return {"case": name, "exit": classify_result(result), "kind": "result",
                    "sent": dict(recorded)}
        except BrowserError as exc:
            return {"case": name, "exit": exc.exit_code, "kind": "BrowserError",
                    "message": exc.message}

    def evaluate_case(name, js, bridge):
        ns = argparse.Namespace(js=js, profile=None, tab=None)
        ev.get_bridge = lambda: bridge
        return finish(name, lambda: asyncio.run(ev.cmd_evaluate(ns)))

    def evaluate_stdin_case(name, stdin_text):
        ns = argparse.Namespace(js="-", profile=None, tab=None)
        ev.get_bridge = lambda: None
        saved = sys.stdin
        sys.stdin = io.StringIO(stdin_text)
        try:
            return finish(name, lambda: asyncio.run(ev.cmd_evaluate(ns)))
        finally:
            sys.stdin = saved

    def script_case(name, *, skill="x", script="y", args_json=None,
                    browser_profile=None, bridge=None, stub=False):
        ns = argparse.Namespace(skill=skill, script=script, browser_profile=browser_profile,
                                args_json=args_json, timeout_s=60.0, tab=None,
                                profile=None, _display_name=None)
        gbridge.get_bridge = lambda: bridge
        return finish(name, lambda: asyncio.run(sc.cmd_script(ns)), stub=stub)

    def script_stdin_case(name, stdin_text):
        ns = argparse.Namespace(skill="x", script="y", browser_profile=None,
                                args_json="-", timeout_s=60.0, tab=None, profile=None,
                                _display_name=None)
        gbridge.get_bridge = lambda: None
        saved = sys.stdin
        sys.stdin = io.StringIO(stdin_text)
        try:
            return finish(name, lambda: asyncio.run(sc.cmd_script(ns)))
        finally:
            sys.stdin = saved

    def argparse_case(name, argv):
        """The real upstream parser: argparse converts/validates before dispatch."""
        import contextlib

        from gcu.cli import _build_parser

        stderr = io.StringIO()
        try:
            with contextlib.redirect_stderr(stderr):
                _build_parser().parse_args(argv)
            return {"case": name, "exit": 0, "kind": "argparse", "message": ""}
        except SystemExit as exc:
            return {"case": name, "exit": int(exc.code or 0), "kind": "argparse",
                    "message": stderr.getvalue().strip()}

    no_bridge = None
    live = FakeBridge(labels=["Work"]) if fake_url else None
    cases = [
        evaluate_case("evaluate (no --js)", None, no_bridge),
        evaluate_case("evaluate --js ''", "", no_bridge),
        evaluate_case("evaluate --js '   '", "   ", no_bridge),
        evaluate_case("evaluate --js @missing", "@" + str(missing), no_bridge),
        evaluate_case("evaluate --js @empty", "@" + str(empty_js), no_bridge),
        evaluate_stdin_case("evaluate --js - (empty stdin)", ""),
        evaluate_case("evaluate inline (no bridge)", "document.title", no_bridge),
        evaluate_case("evaluate ' @missing' (leading space)", " @" + str(missing), no_bridge),
        script_case("script (no --skill)", skill=None, bridge=no_bridge),
        script_case("script --skill ''", skill="", bridge=no_bridge),
        script_case("script (no --script)", script=None, bridge=no_bridge),
        script_case("script --skill '   '", skill="   ", bridge=no_bridge),
        script_case("script --args not-json", args_json="not-json", bridge=no_bridge),
        script_case("script --args '   '", args_json="   ", bridge=no_bridge),
        script_case("script --args '[1]'", args_json="[1]", bridge=no_bridge),
        script_case("script --args null", args_json="null", bridge=no_bridge),
        script_case("script --args ' - '", args_json=" - ", bridge=no_bridge),
        script_case("script --args @empty", args_json="@" + str(empty_json), bridge=no_bridge),
        script_stdin_case("script --args - (empty stdin)", ""),
        argparse_case("script --timeout-s soon (no --skill)", ["script", "--timeout-s", "soon"]),
        script_case("script --browser-profile Nope", browser_profile="Nope", bridge=live),
        script_case("script --browser-profile '   '", browser_profile="   ", bridge=live),
        script_case("script --browser-profile Work", browser_profile="Work", bridge=live, stub=True),
        script_case("script --browser-profile default", browser_profile="default", bridge=live, stub=True),
    ]
    script_tool.run_browser_script = real_run
    return cases


def build_hive_browser(tmp: Path) -> Path:
    binary = tmp / "hive-browser"
    env = dict(os.environ, CGO_ENABLED="0", GOPROXY="off", GOSUMDB="off", GOFLAGS="-mod=mod")
    result = subprocess.run(
        ["go", "build", "-o", str(binary), "./cmd/hive-browser"],
        cwd=str(GO_HIVE / "go-hive"), env=env, capture_output=True, text=True, timeout=600,
    )
    if result.returncode != 0:
        raise RuntimeError(f"go build failed:\n{result.stderr[-3000:]}")
    return binary


class ProfileBridgeHandler(BaseHTTPRequestHandler):
    """Minimal live bridge: /status reports profile Work, /command succeeds."""

    def log_message(self, *args):  # keep the evidence log clean
        pass

    def _send(self, payload: dict):
        body = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/status":
            self._send({"bridge": "running", "connected": True,
                        "profiles": ["Work"], "connections": [{"label": "Work"}]})
        elif self.path == "/health":
            self._send({"ok": True, "status": "ok", "connected": True, "connections": 1})
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(length)
        self._send({"ok": True, "result": {"ok": True}})


def start_profile_bridge():
    server = HTTPServer(("127.0.0.1", 0), ProfileBridgeHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, f"http://127.0.0.1:{server.server_port}"


def closed_bridge_url() -> str:
    import socket

    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return f"http://127.0.0.1:{port}"


def go_cli_cases(binary: Path, tmp: Path, profile_url: str) -> list[dict]:
    """Run the real hive-browser binary over the same case list."""
    empty_js = tmp / "empty.js"
    empty_json = tmp / "empty.json"
    missing = tmp / "missing.js"
    dead = closed_bridge_url()

    cases = []
    plan = [
        ("evaluate (no --js)", ["evaluate"], None, ""),
        ("evaluate --js ''", ["evaluate", "--js", ""], None, ""),
        ("evaluate --js '   '", ["evaluate", "--js", "   "], None, ""),
        ("evaluate --js @missing", ["evaluate", "--js", "@" + str(missing)], None, ""),
        ("evaluate --js @empty", ["evaluate", "--js", "@" + str(empty_js)], None, ""),
        ("evaluate --js - (empty stdin)", ["evaluate", "--js", "-"], None, ""),
        ("evaluate inline (no bridge)", ["evaluate", "--js", "document.title"], None, ""),
        ("evaluate ' @missing' (leading space)", ["evaluate", "--js", " @" + str(missing)], None, ""),
        ("script (no --skill)", ["script"], None, ""),
        ("script --skill ''", ["script", "--skill", ""], None, ""),
        ("script (no --script)", ["script", "--skill", "x"], None, ""),
        ("script --skill '   '", ["script", "--skill", "   ", "--script", "y"], None, ""),
        ("script --args not-json", ["script", "--skill", "x", "--script", "y", "--args", "not-json"], None, ""),
        ("script --args '   '", ["script", "--skill", "x", "--script", "y", "--args", "   "], None, ""),
        ("script --args '[1]'", ["script", "--skill", "x", "--script", "y", "--args", "[1]"], None, ""),
        ("script --args null", ["script", "--skill", "x", "--script", "y", "--args", "null"], None, ""),
        ("script --args ' - '", ["script", "--skill", "x", "--script", "y", "--args", " - "], None, ""),
        ("script --args @empty", ["script", "--skill", "x", "--script", "y", "--args", "@" + str(empty_json)], None, ""),
        ("script --args - (empty stdin)", ["script", "--skill", "x", "--script", "y", "--args", "-"], None, ""),
        ("script --timeout-s soon (no --skill)", ["script", "--timeout-s", "soon"], None, ""),
        ("script --browser-profile Nope", ["script", "--skill", "x", "--script", "y", "--browser-profile", "Nope"], profile_url, ""),
        ("script --browser-profile '   '", ["script", "--skill", "x", "--script", "y", "--browser-profile", "   "], profile_url, ""),
        ("script --browser-profile Work", ["script", "--skill", "x", "--script", "y", "--browser-profile", "Work"], profile_url, ""),
        ("script --browser-profile default", ["script", "--skill", "x", "--script", "y", "--browser-profile", "default"], profile_url, ""),
    ]
    for name, argv, bridge, stdin_text in plan:
        target = bridge or dead
        env = dict(os.environ)
        result = subprocess.run(
            [str(binary), *argv, "--bridge", target],
            input=stdin_text, capture_output=True, text=True, env=env, timeout=180,
        )
        cases.append({
            "case": name,
            "exit": result.returncode,
            "kind": "process",
            "stdout": result.stdout.strip(),
            "stderr": result.stderr.strip(),
        })
    return cases


def compare_cli(upstream_cases, go_cases) -> list[str]:
    mismatches = []
    by_name = {row["case"]: row for row in go_cases}
    for row in upstream_cases:
        go = by_name.get(row["case"])
        if go is None:
            mismatches.append(f"{row['case']}: missing Go case")
            continue
        if row["exit"] != go["exit"]:
            mismatches.append(
                "%s: exit upstream=%s go=%s\n    upstream: %s\n    go:       %s"
                % (row["case"], row["exit"], go["exit"],
                   row.get("message", row.get("sent", "")), go.get("stderr", ""))
            )
    return mismatches


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--skip-e2e", action="store_true",
                        help="reuse docs/fixtures/browser-final-parity/e2e-dump.json instead of re-running the bash e2e")
    args = parser.parse_args()

    FIXTURE_DIR.mkdir(parents=True, exist_ok=True)
    dump_path = FIXTURE_DIR / "e2e-dump.json"

    lines = ["# browser-final-parity dual-run", ""]

    # ── 1. panel value parity ────────────────────────────────────────────
    upstream_contexts, upstream_tabs = upstream_panel_bodies()
    lines.append("## Panel routes (upstream handler in-process vs Go e2e dump)")
    lines.append("")
    lines.append("upstream_tabs = " + json.dumps(upstream_tabs, sort_keys=True))
    if args.skip_e2e and dump_path.exists():
        go_bodies = json.loads(dump_path.read_text())
        lines.append("go_bodies (reused e2e dump) = " + json.dumps(go_bodies, sort_keys=True))
    else:
        go_bodies = run_go_e2e()
        dump_path.write_text(json.dumps(go_bodies, sort_keys=True, indent=2) + "\n")
        lines.append("go_contexts = " + json.dumps(go_bodies["contexts"], sort_keys=True))
        lines.append("go_tabs = " + json.dumps(go_bodies["tabs"], sort_keys=True))
    panel_mismatches = compare_panel(upstream_contexts, upstream_tabs, go_bodies)
    lines.append("")
    lines.append("PANEL: " + ("MATCH" if not panel_mismatches else "MISMATCH"))
    for problem in panel_mismatches:
        lines.append("  - " + problem)

    # ── 2. CLI validation parity ─────────────────────────────────────────
    with tempfile.TemporaryDirectory(prefix="browser-final-parity-") as tmp_name:
        tmp = Path(tmp_name)
        server, profile_url = start_profile_bridge()
        try:
            upstream_cases = upstream_cli_cases(tmp, profile_url)
            binary = build_hive_browser(tmp)
            go_cases = go_cli_cases(binary, tmp, profile_url)
        finally:
            server.shutdown()
    cli_mismatches = compare_cli(upstream_cases, go_cases)

    lines.append("")
    lines.append("## CLI validation conditions (upstream handlers in-process vs hive-browser binary, no extension)")
    lines.append("")
    for row in upstream_cases:
        go = next(entry for entry in go_cases if entry["case"] == row["case"])
        detail = row.get("message") or json.dumps(row.get("sent", {}), ensure_ascii=False)
        lines.append(f"- {row['case']}")
        lines.append(f"    upstream exit={row['exit']} [{row['kind']}] {detail}")
        lines.append(f"    go       exit={go['exit']} stderr={go['stderr']!r}")
    lines.append("")
    lines.append("CLI: " + ("MATCH" if not cli_mismatches else "MISMATCH"))
    for problem in cli_mismatches:
        lines.append("  - " + problem)

    report = "\n".join(lines) + "\n"
    (FIXTURE_DIR / "dual-run.txt").write_text(report)
    print(report)
    print(f"evidence: {FIXTURE_DIR / 'dual-run.txt'}")
    if panel_mismatches or cli_mismatches:
        print("RESULT: MISMATCH", file=sys.stderr)
        return 1
    print("RESULT: ALL MATCH")
    return 0


if __name__ == "__main__":
    sys.exit(main())
