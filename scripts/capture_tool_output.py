#!/usr/bin/env python3
"""Capture model-visible tool *results* from both runtimes.

Method (same as the tool-flat-surface / tool-contract lanes): a scripted fake
OpenAI-compatible gateway in front of each runtime's `serve`; one chat per
batch against the queen session, whose scripted model calls the scenario tool
sequence in order. Each following gateway request carries the previous results
as `role: tool` messages — that serialization is the tool-output contract.

Both runtimes run against a disposable HIVE_HOME and the same fixture
workspace. The only network traffic is the fake gateway and a local fixture
HTTP server (127.0.0.1) that the web/network scenarios point at.

Batches: scenarios carry an optional `batch` field. A batch is one chat; the
queen session is shared across a side's batches (the conversation accumulates,
so this batch's results are the newest ones). The split keeps a bad batch from
sinking the side and bounds the per-chat payload. A failed batch is retried
(fresh workspace + chat); a dead `serve` is restarted on a new port from the
dedicated 189xx range, and gateway call ids are unique per process so results
never collide across batches.

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
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

HERE = Path(__file__).resolve().parent
GATEWAY = HERE / "fake_tool_gateway.py"
# The reference branch in local development; override with --python-root.
DEFAULT_PYTHON_ROOT = Path("/Users/mac/hive")

# Several lanes run `hive serve` concurrently on this machine. Stay off the
# ephemeral range (bind(0) can hand another lane the same port between our
# bind-test and the child's bind) and use one dedicated, verified window.
PORT_RANGE_START = 18900
PORT_RANGE_END = 19000
_PORT_STATE = {"next": PORT_RANGE_START, "used": set()}


def next_port() -> int:
    """Reserve the next free port in the dedicated range."""
    for port in range(_PORT_STATE["next"], PORT_RANGE_END):
        if port in _PORT_STATE["used"]:
            continue
        sock = socket.socket()
        try:
            sock.bind(("127.0.0.1", port))
        except OSError:
            continue
        finally:
            sock.close()
        _PORT_STATE["used"].add(port)
        _PORT_STATE["next"] = port + 1
        return port
    raise SystemExit(f"no free port in {PORT_RANGE_START}-{PORT_RANGE_END - 1}")


class FixtureHandler(SimpleHTTPRequestHandler):
    """Static fixture files plus the network-tool endpoints.

    Both runtimes request the same URLs; the handler pins every header the
    http_headers_scan checks so the comparison is about the tools, not the
    server. Unhandled paths fall through to SimpleHTTPRequestHandler so
    /fixture-page.html serves the workspace fixture.
    """

    protocol_version = "HTTP/1.1"

    def version_string(self) -> str:  # BaseHTTPRequestHandler adds Server: this
        return "fixture/1.0"

    def log_message(self, format, *args):  # type: ignore[override]  # noqa: A002
        return

    def _respond(self, status: int, body: bytes, headers: list[tuple[str, str]]) -> None:
        self.send_response(status)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        for name, value in headers:
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):  # noqa: N802
        path = urllib.parse.urlparse(self.path).path
        if path == "/headers-all":
            headers = [
                ("Content-Security-Policy", "default-src 'self'"),
                ("X-Frame-Options", "DENY"),
                ("X-Content-Type-Options", "nosniff"),
                ("Referrer-Policy", "strict-origin-when-cross-origin"),
                ("Permissions-Policy", "camera=(), microphone=(), geolocation=()"),
                ("X-XSS-Protection", "1; mode=block"),
                ("X-Powered-By", "fixture"),
            ]
            self._respond(200, b"<html><body><h1>headers</h1></body></html>", headers)
            return
        if path == "/headers-missing":
            self._respond(200, b"<html><body><h1>bare</h1></body></html>", [])
            return
        if path == "/redirect-headers":
            self.send_response(302)
            self.send_header("Location", "/headers-all")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if path == "/server-error":
            self._respond(500, b"<html><body><h1>error</h1></body></html>", [])
            return
        super().do_GET()


class QuietHTTPServer(ThreadingHTTPServer):
    """The runtimes drop keep-alive sockets between chats; that is not an error."""

    def handle_error(self, request, client_address):
        exc = sys.exc_info()[1]
        if isinstance(exc, (ConnectionResetError, BrokenPipeError, ConnectionAbortedError)):
            return
        super().handle_error(request, client_address)


def start_fixture_server(work_dir: Path) -> tuple[ThreadingHTTPServer, str]:
    handler = partial(FixtureHandler, directory=str(work_dir))
    server = QuietHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, f"http://127.0.0.1:{server.server_address[1]}"


class CaptureError(RuntimeError):
    """One batch (or server start) failed; the caller may retry it."""


RETRYABLE_ERRORS = (ConnectionResetError, ConnectionAbortedError, TimeoutError, urllib.error.URLError)


def http(method: str, url: str, body: dict | None = None, timeout: float = 30.0, attempts: int = 3) -> tuple[int, str]:
    """HTTP helper with a short retry for connection resets.

    A reset here usually means a runtime server was restarted or another lane
    raced us for a port; a fresh request on a healthy server succeeds.
    """
    data = json.dumps(body).encode() if body is not None else None
    last: BaseException | None = None
    for attempt in range(attempts):
        request = urllib.request.Request(url, data=data, method=method)
        if data is not None:
            request.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                return response.status, response.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as exc:
            return exc.code, exc.read().decode("utf-8", "replace")
        except RETRYABLE_ERRORS as exc:  # noqa: PERF203 — retry is the point
            last = exc
            if attempt + 1 < attempts:
                time.sleep(0.5 * (attempt + 1))
    raise CaptureError(f"{method} {url} failed after {attempts} attempts: {last}")


def wait_health(port: int, proc: subprocess.Popen, timeout: float = 180.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            raise CaptureError(f"server exited early rc={proc.returncode}")
        try:
            code, _ = http("GET", f"http://127.0.0.1:{port}/api/health", timeout=2, attempts=1)
            if code in (200, 204):
                return
        except (CaptureError, OSError, urllib.error.URLError):
            pass
        time.sleep(0.5)
    raise CaptureError(f"health never came up on {port}")


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
            raise CaptureError(f"server exited early rc={proc.returncode}")
        rows = read_log(path)
        if len(rows) >= count:
            return rows
        time.sleep(0.5)
    raise CaptureError(f"only {len(read_log(path))} gateway request(s), want {count}")


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
    # The queen session is shared by every batch's chat, so the conversation
    # (and every row's message list) accumulates across batches. Each chat
    # issues exactly len(order) calls, and a retried batch appends its own
    # fresh call ids, so this batch's results are the newest len(order) ones.
    results = list(seen.values())[-len(order):] if order else []
    for index, scenario_id in enumerate(order):
        if index < len(results):
            ordered[scenario_id] = results[index]
    return ordered


def contains_ref(value) -> bool:
    if isinstance(value, dict):
        if set(value) == {"$from"}:
            return True
        return any(contains_ref(item) for item in value.values())
    if isinstance(value, list):
        return any(contains_ref(item) for item in value)
    return False


def split_batches(scenarios: list[dict]) -> list[list[dict]]:
    """Group scenarios by their `batch` field (contiguous, order preserved).

    A batch is one chat/session. Values minted by an earlier call (`$from`)
    must stay inside the same batch, or the placeholder cannot resolve.
    """
    batches: list[list[dict]] = []
    current: list[dict] = []
    current_batch = None
    for scenario in scenarios:
        batch = scenario.get("batch", 1)
        if current and batch != current_batch:
            batches.append(current)
            current = []
        current.append(scenario)
        current_batch = batch
    if current:
        batches.append(current)
    for batch in batches:
        for index, scenario in enumerate(batch):
            if index == 0 and contains_ref(scenario.get("arguments")):
                raise SystemExit(f"scenario {scenario['id']} starts a batch but needs a $from value")
    return batches


def reset_workspace(work_dir: Path, fixtures: dict[str, str]) -> None:
    if work_dir.exists():
        shutil.rmtree(work_dir)
    work_dir.mkdir(parents=True, exist_ok=True)
    for name, content in fixtures.items():
        target = work_dir / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content, encoding="utf-8")


def start_serve(binary: Path, work_dir: Path, env: dict, log_path: Path) -> tuple[subprocess.Popen, int, object]:
    """Start `hive serve`, retrying on a new port when it dies or never warms."""
    last: BaseException | None = None
    for _ in range(5):
        port = next_port()
        log = open(log_path, "w", encoding="utf-8")
        proc = subprocess.Popen(
            [str(binary), "serve", "--host", "127.0.0.1", "--port", str(port)],
            cwd=str(work_dir),
            env=env,
            stdout=log,
            stderr=subprocess.STDOUT,
        )
        try:
            wait_health(port, proc)
            return proc, port, log
        except CaptureError as exc:
            last = exc
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
            log.close()
    raise CaptureError(f"serve never became healthy: {last}")


def stop_process(proc: subprocess.Popen | None, log) -> None:
    if proc is not None:
        proc.terminate()
        try:
            proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            proc.kill()
    if log is not None:
        log.close()


def run_batch(
    side: str,
    batch: list[dict],
    *,
    server,
    server_port: int,
    gateway_port: int,
    fixture_url: str,
    work_dir: Path,
    scratch_root: Path,
    batch_index: int,
) -> dict:
    """Run one chat: script the batch, collect its tool results."""
    order = [scenario["id"] for scenario in batch]
    gateway_log = scratch_root / f"{side}-gateway-batch{batch_index}.jsonl"
    gateway_log.unlink(missing_ok=True)
    script_path = scratch_root / f"{side}-script-batch{batch_index}.json"
    steps = [{"tool": scenario["tool"], "arguments": scenario.get("arguments") or {}} for scenario in batch]
    steps.append({"text": "done"})
    script_path.write_text(
        json.dumps(steps).replace("__WS__", str(work_dir)).replace("__FIXTURE_URL__", fixture_url),
        encoding="utf-8",
    )

    gateway = subprocess.Popen(
        [sys.executable, str(GATEWAY), "--port", str(gateway_port), "--log", str(gateway_log), "--script", str(script_path)],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.STDOUT,
    )
    try:
        base = f"http://127.0.0.1:{server_port}"
        code, text = http("POST", f"{base}/api/queen/queen_growth/session", {}, timeout=60)
        if code != 200:
            raise CaptureError(f"{side}: session create failed {code}: {text[:400]}")
        session_id = json.loads(text).get("session_id") or ""
        code, text = http(
            "POST",
            f"{base}/api/sessions/{session_id}/chat",
            {"message": "run the capture sequence"},
            timeout=900,
            attempts=1,
        )
        if code != 200:
            raise CaptureError(f"{side}: chat failed {code}: {text[:400]}")
        rows = wait_requests(gateway_log, server, len(batch) + 1)
        results = extract_results(rows, order)
        missing = [scenario_id for scenario_id in order if scenario_id not in results]
        if missing:
            raise CaptureError(f"{side}: batch {batch_index} missing {missing}")
        print(f"{side}: batch {batch_index} captured {len(results)}/{len(order)}")
        return results
    finally:
        stop_process(gateway, None)


def capture_side(side: str, args, scenarios_doc: dict) -> dict:
    scenarios = scenarios_doc["scenarios"]
    scratch_root = Path(args.scratch)
    work_dir = scratch_root / "ws"
    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    scratch_root.mkdir(parents=True, exist_ok=True)

    home = scratch_root / f"{side}-home"
    gateway_port = next_port()
    prepare_home(home, f"http://127.0.0.1:{gateway_port}/v1")
    env = dict(os.environ)
    env["HIVE_HOME"] = str(home)
    env["HIVE_CREDENTIAL_KEY"] = (home / "secrets/credential_key").read_text(encoding="utf-8").strip()
    env["OPENAI_API_KEY"] = "fake-tool-golden"
    # The golden observes the envelope as delivered. The Python loop's
    # proactive microcompaction rewrites old terminal/web results into
    # "Old tool result …" pointers once a conversation holds >6 of them. The
    # batch split keeps conversations short; raise the knob as belt-and-braces
    # so a future long batch cannot capture the context-management layer
    # instead of the tool contract. Go's equivalent does not fire here.
    env["HIVE_MICROCOMPACT_KEEP_RECENT"] = "1000"
    env.pop("HIVE_TOOL_SURFACE", None)

    binary = Path(args.python_bin) if side == "python" else Path(args.go_root) / "dist/hive"
    if not binary.is_file():
        raise SystemExit(f"{side}: binary not found at {binary}")

    # The shared full script ships as an artifact (placeholders left for the
    # reader); the batches below get their own resolved scripts under scratch.
    script = [{"tool": scenario["tool"], "arguments": scenario.get("arguments") or {}} for scenario in scenarios]
    script.append({"text": "done"})
    (out_dir / f"{side}-script.json").write_text(
        json.dumps(script).replace("__WS__", str(work_dir)).replace("__FIXTURE_URL__", "<FIXTURE_URL>"),
        encoding="utf-8",
    )

    fixtures = scenarios_doc.get("fixtures") or {}
    batches = split_batches(scenarios)
    results: dict[str, dict] = {}
    server = None
    server_log = None
    server_port = 0
    fixture_server = None
    fixture_url = ""

    def reset_side_state() -> None:
        nonlocal server, server_log, fixture_server, fixture_url
        stop_process(server, server_log)
        server = None
        server_log = None
        if fixture_server is not None:
            fixture_server.shutdown()
            fixture_server.server_close()
            fixture_server = None

    try:
        for batch_index, batch in enumerate(batches, start=1):
            for attempt in range(1, args.attempts + 1):
                try:
                    if server is None or server.poll() is not None:
                        # serve runs with the fixture workspace as cwd, so it
                        # must exist before the child starts; a retry starts
                        # from a clean copy of the fixtures.
                        reset_workspace(work_dir, fixtures)
                        server, server_port, server_log = start_serve(
                            binary, work_dir, env, out_dir / f"{side}-serve.log"
                        )
                    if fixture_server is None:
                        fixture_server, fixture_url = start_fixture_server(work_dir)
                    batch_results = run_batch(
                        side,
                        batch,
                        server=server,
                        server_port=server_port,
                        gateway_port=gateway_port,
                        fixture_url=fixture_url,
                        work_dir=work_dir,
                        scratch_root=scratch_root,
                        batch_index=batch_index,
                    )
                    results.update(batch_results)
                    break
                except Exception as exc:  # noqa: BLE001 — one bad batch must not sink the side
                    print(f"{side}: batch {batch_index} attempt {attempt} failed: {exc}")
                    reset_side_state()
                    if attempt == args.attempts:
                        raise
                    time.sleep(1.0)
        return results
    finally:
        reset_side_state()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--side", choices=["python", "go", "both"], default="go")
    parser.add_argument("--scenarios", default=str(HERE.parent / "docs/e2e-assets/tool-output/scenarios.json"))
    parser.add_argument("--out", default=str(HERE.parent / "docs/e2e-assets/tool-output/raw"))
    parser.add_argument("--scratch", default="/tmp/tool-output-capture")
    parser.add_argument("--python-root", default=str(DEFAULT_PYTHON_ROOT))
    parser.add_argument("--python-bin", default=str(DEFAULT_PYTHON_ROOT / ".venv/bin/hive"))
    parser.add_argument("--go-root", default=str(HERE.parent / "go-hive"))
    parser.add_argument("--attempts", type=int, default=3, help="attempts per batch (fresh workspace + session)")
    args = parser.parse_args()

    scenarios_doc = json.loads(Path(args.scenarios).read_text(encoding="utf-8"))
    sides = ["python", "go"] if args.side == "both" else [args.side]
    for side in sides:
        results = capture_side(side, args, scenarios_doc)
        out_dir = Path(args.out)
        (out_dir / f"{side}-results.json").write_text(
            json.dumps(results, ensure_ascii=False, indent=1) + "\n", encoding="utf-8"
        )
        print(f"{side}: captured {len(results)}/{len(scenarios_doc['scenarios'])}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
