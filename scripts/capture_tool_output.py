#!/usr/bin/env python3
"""Capture model-visible tool *results* from both runtimes.

Method (same as the tool-flat-surface / tool-contract lanes): a scripted fake
OpenAI-compatible gateway in front of each runtime's `serve`; one default queen
session; one chat whose scripted model calls the scenario tool sequence in
order. Each following gateway request carries the previous results as
`role: tool` messages — that serialization is the tool-output contract.

Both runtimes run against a disposable HIVE_HOME and the same fixture
workspace, with no external network use beyond the fake gateway.

Usage:
    python3 go_hive/scripts/capture_tool_output.py --side both \
        --out go_hive/docs/e2e-assets/tool-output/raw \
        --scratch /tmp/tool-output-capture

    # python reference (its own checkout + venv)
    --python-root /Users/mac/hive --python-bin /Users/mac/hive/.venv/bin/hive
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import secrets
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
GATEWAY = HERE / "fake_tool_gateway.py"
# The reference branch in local development; override with --python-root.
DEFAULT_PYTHON_ROOT = Path("/Users/mac/hive")

def free_port() -> int:
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port


def http(method: str, url: str, body: dict | None = None, timeout: float = 30.0) -> tuple[int, str]:
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        request.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status, response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read().decode("utf-8", "replace")


def wait_health(port: int, proc: subprocess.Popen, timeout: float = 180.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            raise SystemExit(f"server exited early rc={proc.returncode}")
        try:
            code, _ = http("GET", f"http://127.0.0.1:{port}/api/health", timeout=2)
            if code in (200, 204):
                return
        except Exception:
            pass
        time.sleep(0.5)
    raise SystemExit(f"health never came up on {port}")


def read_log(path: Path) -> list[dict]:
    if not path.is_file():
        return []
    rows = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line:
            rows.append(json.loads(line))
    return rows


def wait_requests(path: Path, proc: subprocess.Popen, count: int, timeout: float = 300.0) -> list[dict]:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            raise SystemExit(f"server exited early rc={proc.returncode}")
        rows = read_log(path)
        if len(rows) >= count:
            return rows
        time.sleep(0.5)
    raise SystemExit(f"only {len(read_log(path))} gateway request(s), want {count}")


def prepare_home(home: Path, api_base: str) -> None:
    if home.exists():
        shutil.rmtree(home)
    (home / "secrets").mkdir(parents=True)
    key = base64.urlsafe_b64encode(secrets.token_bytes(32)).decode("ascii")
    (home / "secrets/credential_key").write_text(key, encoding="utf-8")
    (home / "configuration.json").write_text(
        json.dumps(
            {
                "llm": {
                    "provider": "openai",
                    "model": "gpt-4o-mini",
                    "api_base": api_base,
                    "api_key": "fake-tool-golden",
                }
            },
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )


def extract_results(rows: list[dict], order: list[str]) -> dict:
    """Pull every `role: tool` message out of the newest requests, joined with
    the assistant tool_call that produced it. Results are matched to scenarios
    by position: the script issues exactly one tool call per scenario, in order.
    """
    calls: dict[str, dict] = {}
    seen: dict[str, dict] = {}
    for row in rows:
        for message in row.get("messages") or []:
            if not isinstance(message, dict):
                continue
            if message.get("role") == "assistant":
                for call in message.get("tool_calls") or []:
                    if not isinstance(call, dict):
                        continue
                    function = call.get("function") or {}
                    calls[str(call.get("id"))] = {
                        "name": function.get("name"),
                        "arguments": function.get("arguments"),
                    }
            elif message.get("role") == "tool":
                key = str(message.get("tool_call_id"))
                meta = calls.get(key, {})
                seen[key] = {
                    "tool_call_id": key,
                    "tool": meta.get("name"),
                    "arguments": meta.get("arguments"),
                    "content": message.get("content"),
                    "message_keys": sorted(message),
                }
    ordered: dict[str, dict] = {}
    results = list(seen.values())
    for index, scenario_id in enumerate(order):
        if index < len(results):
            ordered[scenario_id] = results[index]
    return ordered


def capture(side: str, args, scenarios_doc: dict) -> dict:
    scenarios = scenarios_doc["scenarios"]
    order = [scenario["id"] for scenario in scenarios]
    scratch_root = Path(args.scratch)
    # One shared fixture workspace for both sides: the scenarios read the same
    # files, so the captured paths differ only by the (masked) HIVE_HOME.
    work_dir = scratch_root / "ws"
    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    work_dir.mkdir(parents=True, exist_ok=True)
    for name, content in (scenarios_doc.get("fixtures") or {}).items():
        (work_dir / name).write_text(content, encoding="utf-8")

    home = scratch_root / f"{side}-home"
    gateway_port = free_port()
    server_port = free_port()
    # Full request transcripts stay in the scratch dir: they carry the whole
    # session prompt and would swamp the repo. What ships is the extracted
    # tool-result JSON below plus the server log.
    gateway_log = scratch_root / f"{side}-gateway.jsonl"
    gateway_log.unlink(missing_ok=True)

    script = [{"tool": scenario["tool"], "arguments": scenario.get("arguments") or {}} for scenario in scenarios]
    script.append({"text": "done"})
    script_path = out_dir / f"{side}-script.json"
    script_path.write_text(json.dumps(script).replace("__WS__", str(work_dir)), encoding="utf-8")

    prepare_home(home, f"http://127.0.0.1:{gateway_port}/v1")
    env = dict(os.environ)
    env["HIVE_HOME"] = str(home)
    env["HIVE_CREDENTIAL_KEY"] = (home / "secrets/credential_key").read_text(encoding="utf-8").strip()
    env["OPENAI_API_KEY"] = "fake-tool-golden"
    env.pop("HIVE_TOOL_SURFACE", None)

    if side == "python":
        binary = Path(args.python_bin)
        root = Path(args.python_root)
    else:
        root = Path(args.go_root)
        binary = root / "dist/hive"

    gateway = subprocess.Popen(
        [sys.executable, str(GATEWAY), "--port", str(gateway_port), "--log", str(gateway_log),
         "--script", str(script_path)],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.STDOUT,
    )
    serve_log = open(out_dir / f"{side}-serve.log", "w", encoding="utf-8")
    server = subprocess.Popen(
        [str(binary), "serve", "--host", "127.0.0.1", "--port", str(server_port)],
        cwd=str(work_dir),
        env=env,
        stdout=serve_log,
        stderr=subprocess.STDOUT,
    )
    try:
        wait_health(server_port, server)
        base = f"http://127.0.0.1:{server_port}"
        code, text = http("POST", f"{base}/api/queen/queen_growth/session", {})
        if code != 200:
            raise SystemExit(f"{side}: session create failed {code}: {text[:400]}")
        session_id = json.loads(text).get("session_id") or ""
        code, text = http("POST", f"{base}/api/sessions/{session_id}/chat",
                          {"message": "run the capture sequence"}, timeout=900)
        if code != 200:
            raise SystemExit(f"{side}: chat failed {code}: {text[:400]}")
        rows = wait_requests(gateway_log, server, len(scenarios) + 1)
        results = extract_results(rows, order)
        (out_dir / f"{side}-results.json").write_text(
            json.dumps(results, ensure_ascii=False, indent=1) + "\n", encoding="utf-8"
        )
        missing = [scenario_id for scenario_id in order if scenario_id not in results]
        print(f"{side}: captured {len(results)}/{len(order)}"
              + (f" missing={missing}" if missing else ""))
        if missing:
            raise SystemExit(f"{side}: incomplete capture")
        return results
    finally:
        server.terminate()
        try:
            server.wait(timeout=15)
        except subprocess.TimeoutExpired:
            server.kill()
        gateway.terminate()
        try:
            gateway.wait(timeout=10)
        except subprocess.TimeoutExpired:
            gateway.kill()
        serve_log.close()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--side", choices=["python", "go", "both"], default="go")
    parser.add_argument("--scenarios", default=str(HERE.parent / "docs/e2e-assets/tool-output/scenarios.json"))
    parser.add_argument("--out", default=str(HERE.parent / "docs/e2e-assets/tool-output/raw"))
    parser.add_argument("--scratch", default="/tmp/tool-output-capture")
    parser.add_argument("--python-root", default=str(DEFAULT_PYTHON_ROOT))
    parser.add_argument("--python-bin", default=str(DEFAULT_PYTHON_ROOT / ".venv/bin/hive"))
    parser.add_argument("--go-root", default=str(HERE.parent / "go-hive"))
    args = parser.parse_args()

    scenarios_doc = json.loads(Path(args.scenarios).read_text(encoding="utf-8"))
    sides = ["python", "go"] if args.side == "both" else [args.side]
    for side in sides:
        capture(side, args, scenarios_doc)
    return 0


if __name__ == "__main__":
    sys.exit(main())
