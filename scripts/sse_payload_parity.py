#!/usr/bin/env python3
"""Capture and diff the session SSE payload contract: Python reference vs Go target.

This is the SSE companion to ``freeze_api_responses.py``. Where that probe
compares one response body per endpoint, this one drives the *same scenario*
through both implementations and compares the SSE frames the client sees:

    create session -> opening turn settles -> subscribe events ->
    send a chat message -> send an ask_user message -> answer it ->
    reconnect (replay) -> stop session

The stub LLM answers every request deterministically from localhost (no
vendor, no external network): plain messages get a fixed streamed text
reply, messages containing ``ASK_ME`` get one streamed ``ask_user`` tool
call, and the tool-result call gets text again. Both servers receive the
same configuration, so frame payloads are directly comparable after
volatile values (seq / timestamps / ids) are normalised.

Outputs (default ``go_hive/docs/e2e-assets/sse-payload-parity``):

  capture-reference.json   live / replay / history frames, normalised
  capture-target.json      same, Go target
  diff.json                per-type field matrix + sequence diff
  summary.txt              one-screen human summary

The probe is opt-in: it is not part of ``make ci`` and never touches the
network beyond 127.0.0.1. Run it by hand:

    uv run python go_hive/scripts/sse_payload_parity.py
    uv run python go_hive/scripts/sse_payload_parity.py --no-build
    uv run python go_hive/scripts/sse_payload_parity.py --keep --workdir /tmp/sse-parity

Standard library only; no third-party imports.
"""

from __future__ import annotations

import argparse
import base64
import difflib
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
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
GO_DIR = REPO_ROOT / "go_hive" / "go-hive"
GO_BINARY = GO_DIR / "dist" / "hive-sse-parity"
EVIDENCE_DIR = REPO_ROOT / "go_hive" / "docs" / "e2e-assets" / "sse-payload-parity"

QUEEN_ID = "queen_growth"
LLM_PROVIDER = "openai"
LLM_MODEL = "sse-parity-probe-model"
LLM_KEY_ENV = "HIVE_SSE_PARITY_KEY"

TEXT_REPLY = "PAYLOAD_PARITY_OK"
ASK_TOOL_CALL_ID = "call_sse_parity_ask"
ASK_QUESTIONS = [
    {
        "id": "color",
        "prompt": "Which color should the probe use?",
        "options": ["blue", "green"],
    }
]

HEALTH_WAIT_S = 180.0
SETTLE_TIMEOUT_S = 60.0
SETTLE_STABLE_S = 1.5
MIN_EVENTS = 5


# ---------------------------------------------------------------------------
# stub LLM
# ---------------------------------------------------------------------------


class StubLLMHandler(BaseHTTPRequestHandler):
    """Deterministic OpenAI-compatible gateway (streaming and JSON).

    Chat messages whose last user turn contains ``ASK_ME`` receive one
    streamed ``ask_user`` tool call; everything else — including the call
    that carries the tool result back — receives ``PAYLOAD_PARITY_OK``.
    """

    protocol_version = "HTTP/1.1"

    def log_message(self, format: str, *args: Any) -> None:  # noqa: A002 - stdlib signature
        return

    # -- request classification ------------------------------------------

    def _wants_ask_user(self, body: dict[str, Any]) -> bool:
        messages = body.get("messages") or []
        if not isinstance(messages, list) or not messages:
            return False
        # A tool result already came back: answer with text so the scenario
        # terminates instead of asking again.
        for message in messages:
            if isinstance(message, dict) and (message.get("role") == "tool" or message.get("tool_call_id")):
                return False
        last_user = ""
        for message in reversed(messages):
            if isinstance(message, dict) and message.get("role") == "user":
                content = message.get("content")
                if isinstance(content, str):
                    last_user = content
                elif isinstance(content, list):
                    last_user = "".join(
                        part.get("text", "")
                        for part in content
                        if isinstance(part, dict) and isinstance(part.get("text"), str)
                    )
                break
        return "ASK_ME" in last_user

    # -- wire formats -----------------------------------------------------

    def _chunk(self, model: str, delta: dict[str, Any], finish_reason: Any = None) -> bytes:
        payload = {
            "id": "chatcmpl-sse-parity",
            "object": "chat.completion.chunk",
            "created": 1700000000,
            "model": model,
            "choices": [{"index": 0, "delta": delta, "finish_reason": finish_reason}],
        }
        return f"data: {json.dumps(payload)}\n\n".encode()

    def _stream_text(self, model: str) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        self.wfile.write(self._chunk(model, {"role": "assistant", "content": TEXT_REPLY}))
        self.wfile.write(self._chunk(model, {}, "stop"))
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def _stream_ask_user(self, model: str) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        first = {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "index": 0,
                    "id": ASK_TOOL_CALL_ID,
                    "type": "function",
                    "function": {"name": "ask_user", "arguments": ""},
                }
            ],
        }
        self.wfile.write(self._chunk(model, first))
        arguments = json.dumps({"questions": ASK_QUESTIONS})
        # Two argument fragments mirror a real provider's chunking.
        split = len(arguments) // 2
        for fragment in (arguments[:split], arguments[split:]):
            delta = {"tool_calls": [{"index": 0, "function": {"arguments": fragment}}]}
            self.wfile.write(self._chunk(model, delta))
        self.wfile.write(self._chunk(model, {}, "tool_calls"))
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def _json_reply(self, model: str, ask: bool) -> None:
        if ask:
            message = {
                "role": "assistant",
                "content": None,
                "tool_calls": [
                    {
                        "id": ASK_TOOL_CALL_ID,
                        "type": "function",
                        "function": {"name": "ask_user", "arguments": json.dumps({"questions": ASK_QUESTIONS})},
                    }
                ],
            }
            finish = "tool_calls"
        else:
            message = {"role": "assistant", "content": TEXT_REPLY}
            finish = "stop"
        payload = {
            "id": "chatcmpl-sse-parity",
            "object": "chat.completion",
            "created": 1700000000,
            "model": model,
            "choices": [{"index": 0, "message": message, "finish_reason": finish}],
            "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
        }
        data = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self) -> None:  # noqa: N802 - stdlib signature
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length) if length else b"{}"
        try:
            body = json.loads(raw.decode("utf-8"))
        except json.JSONDecodeError:
            body = {}
        model = body.get("model", LLM_MODEL)
        ask = self._wants_ask_user(body)
        if body.get("stream"):
            if ask:
                self._stream_ask_user(model)
            else:
                self._stream_text(model)
            return
        self._json_reply(model, ask)

    def do_GET(self) -> None:  # noqa: N802 - stdlib signature
        data = b'{"object": "list", "data": []}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def start_stub_llm() -> tuple[ThreadingHTTPServer, str]:
    server = ThreadingHTTPServer(("127.0.0.1", 0), StubLLMHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, f"http://127.0.0.1:{server.server_address[1]}/v1"


# ---------------------------------------------------------------------------
# small utilities
# ---------------------------------------------------------------------------


def now_iso() -> str:
    return datetime.now().astimezone().isoformat(timespec="seconds")


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def log_tail(path: Path, lines: int = 30) -> str:
    if not path.is_file():
        return f"<no log at {path}>"
    try:
        return "\n".join(path.read_text(encoding="utf-8", errors="replace").splitlines()[-lines:])
    except OSError as exc:
        return f"<cannot read log: {exc}>"


@dataclass
class HttpResult:
    status: int
    payload: Any
    raw: str
    content_type: str = ""
    error: str = ""

    @property
    def json_ok(self) -> bool:
        return self.error == "" and self.payload is not None


def http_request(method: str, url: str, body: Any | None = None, timeout: float = 90.0) -> HttpResult:
    data = None
    headers = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read().decode("utf-8", errors="replace")
            status = response.status
            content_type = response.headers.get("Content-Type", "")
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", errors="replace")
        status = exc.code
        content_type = exc.headers.get("Content-Type", "") if exc.headers else ""
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        return HttpResult(status=0, payload=None, raw="", error=str(exc))
    try:
        payload = json.loads(raw) if raw.strip() else None
    except json.JSONDecodeError:
        return HttpResult(status=status, payload=None, raw=raw, content_type=content_type, error="non-JSON body")
    return HttpResult(status=status, payload=payload, raw=raw, content_type=content_type)


# ---------------------------------------------------------------------------
# SSE capture
# ---------------------------------------------------------------------------


@dataclass
class SseCapture:
    status: int = 0
    content_type: str = ""
    frames: list[dict[str, Any]] = field(default_factory=list)
    comments: list[str] = field(default_factory=list)
    error: str = ""

    def snapshot(self) -> list[dict[str, Any]]:
        return [dict(frame) for frame in self.frames]


class SseCollector:
    """Connect to one SSE endpoint and record normalised frames.

    Volatile values are replaced while key presence is preserved, matching
    the api-responses contract convention: ``seq``, ``timestamp``,
    ``execution_id`` and generated ids become placeholders. Chat content is
    kept verbatim because both sides answer from the same stub.
    """

    def __init__(self, url: str, label: str) -> None:
        self.url = url
        self.label = label
        self.capture = SseCapture()
        self._stop = threading.Event()
        self._lock = threading.Lock()
        self._response = None
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self) -> "SseCollector":
        self._thread.start()
        return self

    def stop(self) -> SseCapture:
        self._stop.set()
        response = self._response
        if response is not None:
            try:
                response.close()
            except Exception:
                pass
        self._thread.join(timeout=5)
        return self.capture

    def frame_count(self) -> int:
        with self._lock:
            return len(self.capture.frames)

    def wait_for_frames(self, count: int, timeout: float) -> bool:
        deadline = time.time() + timeout
        while time.time() < deadline:
            if self.frame_count() >= count:
                return True
            time.sleep(0.05)
        return self.frame_count() >= count

    def wait_settle(self, quiet_s: float, timeout: float) -> bool:
        """Wait until the frame count stops growing for ``quiet_s``."""
        deadline = time.time() + timeout
        last = self.frame_count()
        stable_since = time.time()
        while time.time() < deadline:
            time.sleep(0.1)
            current = self.frame_count()
            if current != last:
                last = current
                stable_since = time.time()
            elif time.time() - stable_since >= quiet_s:
                return True
        return False

    def _run(self) -> None:
        request = urllib.request.Request(self.url, headers={"Accept": "text/event-stream"})
        try:
            with urllib.request.urlopen(request, timeout=SETTLE_TIMEOUT_S + 15) as response:
                self._response = response
                self.capture.status = response.status
                self.capture.content_type = response.headers.get("Content-Type", "")
                event_name: str | None = None
                data_lines: list[str] = []
                while not self._stop.is_set():
                    raw = response.readline()
                    if not raw:
                        break
                    line = raw.decode("utf-8", errors="replace").rstrip("\n").rstrip("\r")
                    if line == "":
                        if data_lines:
                            self._record(event_name, "\n".join(data_lines))
                        event_name, data_lines = None, []
                        continue
                    if line.startswith(":"):
                        with self._lock:
                            self.capture.comments.append(line)
                        continue
                    if line.startswith("event:"):
                        event_name = line[len("event:") :].strip()
                    elif line.startswith("data:"):
                        data_lines.append(line[len("data:") :].lstrip())
        except Exception as exc:  # noqa: BLE001 - report, never abort the probe
            if not self._stop.is_set():
                self.capture.error = str(exc)

    def _record(self, event_name: str | None, data: str) -> None:
        try:
            decoded = json.loads(data)
        except json.JSONDecodeError:
            with self._lock:
                self.capture.frames.append({"event": event_name, "raw_data": data[:500]})
            return
        if not isinstance(decoded, dict):
            with self._lock:
                self.capture.frames.append({"event": event_name, "raw_data": json.dumps(decoded)[:500]})
            return
        frame = normalise_envelope(decoded, event_name)
        with self._lock:
            self.capture.frames.append(frame)


_VOLATILE_KEYS = {
    "seq",
    "tool_use_id",
    "call_id",
    "timestamp",
    "started_at",
    "id",
    "session_id",
    "execution_id",
    "colony_id",
    "correlation_id",
    "run_id",
}


def normalise_value(value: Any, key: str = "") -> Any:
    """Recursively replace volatile values, keeping structure and types."""
    if isinstance(value, dict):
        return {k: normalise_value(v, k) for k, v in value.items()}
    if isinstance(value, list):
        return [normalise_value(item, key) for item in value]
    if key in _VOLATILE_KEYS and value is not None:
        return f"<{key}>"
    return value


def normalise_envelope(envelope: dict[str, Any], event_name: str | None = None) -> dict[str, Any]:
    """Normalise one SSE frame / history event into the comparison shape."""
    return {
        "event": event_name,
        "type": envelope.get("type"),
        "stream_id": envelope.get("stream_id"),
        "node_id": envelope.get("node_id"),
        "execution_id": "<execution_id>" if envelope.get("execution_id") is not None else None,
        "correlation_id": "<correlation_id>" if envelope.get("correlation_id") is not None else None,
        "colony_id": envelope.get("colony_id"),
        "run_id": "<run_id>" if "run_id" in envelope else "<absent>",
        "seq": "<seq>" if envelope.get("seq") is not None else None,
        "timestamp": "<timestamp>",
        "data": normalise_value(envelope.get("data") or {}),
    }


def normalise_history_event(event: dict[str, Any]) -> dict[str, Any]:
    """Apply the frame normalisation to one REST history event."""
    if not isinstance(event, dict):
        return {"raw_data": event}
    return normalise_envelope(event)


# ---------------------------------------------------------------------------
# sides
# ---------------------------------------------------------------------------


@dataclass
class Side:
    key: str  # "reference" | "target"
    label: str
    implementation: str
    port: int
    home: Path
    log_path: Path
    argv: list[str] = field(default_factory=list)
    cwd: Path | None = None
    env: dict[str, str] = field(default_factory=dict)
    process: subprocess.Popen | None = None
    log_handle: Any = None
    version: str = ""
    session_id: str = ""
    captures: dict[str, Any] = field(default_factory=dict)
    responses: dict[str, Any] = field(default_factory=dict)

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.port}"


class ProbeError(RuntimeError):
    pass


def build_go_binary(go_dir: Path, binary: Path) -> None:
    if binary.is_file():
        return
    binary.parent.mkdir(parents=True, exist_ok=True)
    proc = subprocess.run(
        ["go", "build", "-o", str(binary), "./cmd/hive"],
        cwd=str(go_dir),
        capture_output=True,
        text=True,
        timeout=900,
    )
    if proc.returncode != 0:
        raise ProbeError(f"go build failed:\n{proc.stdout}\n{proc.stderr}")


def make_home(root: Path, name: str, api_base: str) -> Path:
    home = root / name
    (home / "secrets").mkdir(parents=True, exist_ok=True)
    credential_key = base64.urlsafe_b64encode(secrets.token_bytes(32)).decode("ascii")
    (home / "secrets" / "credential_key").write_text(credential_key, encoding="utf-8")
    os.chmod(home / "secrets" / "credential_key", 0o600)
    config = {
        "llm": {
            "provider": LLM_PROVIDER,
            "model": LLM_MODEL,
            "api_base": api_base,
            "api_key_env_var": LLM_KEY_ENV,
        }
    }
    (home / "configuration.json").write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
    return home


def spawn(side: Side) -> None:
    side.log_path.parent.mkdir(parents=True, exist_ok=True)
    handle = open(side.log_path, "w", encoding="utf-8")  # noqa: SIM115 - owned by the child
    side.process = subprocess.Popen(
        side.argv,
        cwd=str(side.cwd),
        env=side.env,
        stdout=handle,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    side.log_handle = handle


def terminate(side: Side) -> None:
    proc = side.process
    if proc is not None and proc.poll() is None:
        try:
            proc.terminate()
            proc.wait(timeout=10)
        except (OSError, subprocess.TimeoutExpired):
            try:
                proc.kill()
                proc.wait(timeout=5)
            except (OSError, subprocess.TimeoutExpired):
                pass
    if side.log_handle is not None:
        try:
            side.log_handle.close()
        except Exception:
            pass


def wait_health(side: Side, timeout: float = HEALTH_WAIT_S) -> None:
    deadline = time.time() + timeout
    last_error = ""
    while time.time() < deadline:
        if side.process is not None and side.process.poll() is not None:
            raise ProbeError(
                f"{side.label} exited with code {side.process.returncode} before /api/health.\n"
                f"log tail:\n{log_tail(side.log_path)}"
            )
        result = http_request("GET", f"{side.base_url}/api/health", timeout=5)
        if result.status == 200:
            return
        last_error = result.error or f"HTTP {result.status}"
        time.sleep(0.25)
    raise ProbeError(
        f"{side.label} did not become healthy within {timeout:.0f}s (last: {last_error}).\n"
        f"log tail:\n{log_tail(side.log_path)}"
    )


def read_version(argv: list[str], cwd: Path, env: dict[str, str]) -> str:
    try:
        proc = subprocess.run(argv, cwd=str(cwd), env=env, capture_output=True, text=True, timeout=120)
    except (OSError, subprocess.TimeoutExpired) as exc:
        return f"<version failed: {exc}>"
    lines = (proc.stdout + proc.stderr).strip().splitlines()
    for line in lines:
        stripped = line.strip()
        if stripped.lower().startswith("hive "):
            return stripped
    return lines[-1] if lines else f"<empty, rc={proc.returncode}>"


def git_capture(argv: list[str]) -> str:
    try:
        return (
            subprocess.run(["git", *argv], cwd=str(REPO_ROOT), capture_output=True, text=True, timeout=20).stdout.strip()
            or "unknown"
        )
    except (OSError, subprocess.TimeoutExpired):
        return "unknown"


# ---------------------------------------------------------------------------
# probe
# ---------------------------------------------------------------------------


class Probe:
    def __init__(self, args: argparse.Namespace) -> None:
        self.args = args
        self.root = Path(args.workdir).expanduser() if args.workdir else Path(
            os.environ.get("TMPDIR", "/tmp")
        ) / f"hive-sse-parity-{os.getpid()}"
        self.reference: Side | None = None
        self.target: Side | None = None
        self.stub_server: ThreadingHTTPServer | None = None
        self.llm_api_base = ""
        self.scenario: dict[str, Any] = {}

    # -- setup -------------------------------------------------------------

    def prepare(self) -> None:
        if not (REPO_ROOT / "core" / "framework" / "cli.py").is_file():
            raise ProbeError(f"cannot find the Python hive checkout under {REPO_ROOT}")
        if not self.args.no_build:
            build_go_binary(GO_DIR, GO_BINARY)
        if not GO_BINARY.is_file():
            raise ProbeError(f"Go binary missing: {GO_BINARY} (run without --no-build)")

        uv = shutil.which("uv")
        if uv is None:
            raise ProbeError("uv is not on PATH; the Python side starts with `uv run --offline hive`")

        self.root.mkdir(parents=True, exist_ok=True)
        self.stub_server, self.llm_api_base = start_stub_llm()
        print(f"[sse-parity] stub LLM: {self.llm_api_base}")
        ref_home = make_home(self.root, "reference-home", self.llm_api_base)
        go_home = make_home(self.root, "target-home", self.llm_api_base)
        ref_port = self.args.port_reference or free_port()
        go_port = self.args.port_target or free_port()
        while go_port == ref_port:
            go_port = free_port()

        self.reference = Side(
            "reference", "Python reference", "python", ref_port, ref_home, self.root / "logs" / "reference.log"
        )
        self.target = Side("target", "Go target", "go_hive", go_port, go_home, self.root / "logs" / "target.log")

        base_env = {
            k: v
            for k, v in os.environ.items()
            if k not in {"HIVE_HOME", "HIVE_CREDENTIAL_KEY", "HIVE_RUNTIME_PORT", "HIVE_DESKTOP_TOKEN"}
        }
        base_env["PYTHONUNBUFFERED"] = "1"
        base_env[LLM_KEY_ENV] = "sse-parity-dummy"

        ref_env = dict(base_env)
        ref_env["HIVE_HOME"] = str(ref_home)
        ref_env["HIVE_CREDENTIAL_KEY"] = (ref_home / "secrets" / "credential_key").read_text(encoding="utf-8")
        go_env = dict(base_env)
        go_env["HIVE_HOME"] = str(go_home)
        go_env["HIVE_CREDENTIAL_KEY"] = (go_home / "secrets" / "credential_key").read_text(encoding="utf-8")

        self.reference.argv = [uv, "run", "--offline", "hive", "serve", "--host", "127.0.0.1", "--port", str(ref_port)]
        self.reference.cwd = REPO_ROOT
        self.reference.env = ref_env
        self.target.argv = [str(GO_BINARY), "serve", "--host", "127.0.0.1", "--port", str(go_port)]
        self.target.cwd = GO_DIR
        self.target.env = go_env

        print(f"[sse-parity] reference home: {ref_home}")
        print(f"[sse-parity] target home:    {go_home}")
        print(f"[sse-parity] ports: reference={ref_port} target={go_port}")
        for side in (self.reference, self.target):
            spawn(side)
        for side in (self.reference, self.target):
            wait_health(side)
            side.version = read_version(
                [uv, "run", "--offline", "hive", "--version"] if side.key == "reference" else [str(GO_BINARY), "--version"],
                side.cwd or REPO_ROOT,
                side.env,
            )
            print(f"[sse-parity] {side.label}: {side.version}")

    # -- driving -----------------------------------------------------------

    def _post(self, side: Side, key: str, path: str, body: Any | None = None, timeout: float = 120) -> HttpResult:
        result = http_request("POST", side.base_url + path, body=body, timeout=timeout)
        side.responses[key] = {"status": result.status, "payload": result.payload, "error": result.error}
        return result

    def _delete(self, side: Side, key: str, path: str, timeout: float = 60) -> HttpResult:
        result = http_request("DELETE", side.base_url + path, timeout=timeout)
        side.responses[key] = {"status": result.status, "payload": result.payload, "error": result.error}
        return result

    def create_sessions(self) -> None:
        assert self.reference and self.target
        for side in (self.reference, self.target):
            result = self._post(side, "session_create", "/api/sessions", {"queen_name": QUEEN_ID})
            if not result.json_ok or not isinstance(result.payload, dict):
                raise ProbeError(f"{side.label}: POST /api/sessions failed: {result.status} {result.error} {result.raw[:400]}")
            session_id = result.payload.get("session_id")
            if not isinstance(session_id, str) or not session_id:
                raise ProbeError(f"{side.label}: POST /api/sessions returned no session_id: {result.payload}")
            side.session_id = session_id
            print(f"[sse-parity] {side.label}: session {session_id}")
        self.wait_for_events("opening turn")

    def history_events(self, side: Side) -> list[dict[str, Any]] | None:
        result = http_request("GET", f"{side.base_url}/api/sessions/{side.session_id}/events/history", timeout=15)
        if result.status != 200 or not isinstance(result.payload, dict):
            return None
        events = result.payload.get("events")
        return events if isinstance(events, list) else None

    def wait_for_events(self, label: str) -> None:
        """Wait until both history logs stop growing (turns settle)."""
        assert self.reference and self.target
        deadline = time.time() + SETTLE_TIMEOUT_S
        last: dict[str, int] = {"reference": -1, "target": -1}
        stable_since: float | None = None
        while time.time() < deadline:
            totals: dict[str, int] = {}
            for side in (self.reference, self.target):
                events = self.history_events(side)
                totals[side.key] = len(events) if events is not None else -1
            if totals == last and all(value >= MIN_EVENTS for value in totals.values()):
                if stable_since is None:
                    stable_since = time.time()
                elif time.time() - stable_since >= SETTLE_STABLE_S:
                    print(f"[sse-parity] {label}: settled at {totals}")
                    return
            else:
                stable_since = None
                last = totals
            time.sleep(0.25)
        print(f"[sse-parity] warning: {label} did not settle within {SETTLE_TIMEOUT_S:.0f}s (last totals={last})")

    def wait_for_question(self, label: str) -> None:
        """Wait until both histories carry a client_input_requested with questions."""
        assert self.reference and self.target
        deadline = time.time() + SETTLE_TIMEOUT_S
        while time.time() < deadline:
            pending: dict[str, bool] = {}
            for side in (self.reference, self.target):
                events = self.history_events(side) or []
                pending[side.key] = any(
                    event.get("type") == "client_input_requested"
                    and isinstance((event.get("data") or {}).get("questions"), list)
                    and (event.get("data") or {}).get("questions")
                    for event in events
                )
            if all(pending.values()):
                print(f"[sse-parity] {label}: question surfaces on both sides")
                time.sleep(0.5)
                return
            time.sleep(0.25)
        print(f"[sse-parity] warning: {label} did not surface a question in time; continuing")

    def run_scenario(self) -> None:
        assert self.reference and self.target
        sides = (self.reference, self.target)

        # 1. live subscription, opened before the first user turn so every
        #    campaign frame is captured as it streams.
        live: dict[str, SseCollector] = {}
        for side in sides:
            collector = SseCollector(
                f"{side.base_url}/api/sessions/{side.session_id}/events", f"{side.key}:live"
            ).start()
            if not collector.wait_for_frames(1, timeout=15):
                raise ProbeError(f"{side.label}: session SSE produced no snapshot frame; {collector.capture.error}")
            live[side.key] = collector
        print("[sse-parity] live subscriptions attached")

        # 2. plain message -> text turn.
        for side in sides:
            self._post(side, "chat_text", f"/api/sessions/{side.session_id}/chat", {"message": "payload parity probe"})
        self.wait_for_events("text turn")

        # 3. ask_user message -> question park.
        for side in sides:
            self._post(
                side,
                "chat_ask",
                f"/api/sessions/{side.session_id}/chat",
                {"message": "ASK_ME payload parity question"},
            )
        self.wait_for_question("ask turn")

        # 4. answer the question -> text turn resumes and parks on turn_done.
        for side in sides:
            self._post(side, "chat_answer", f"/api/sessions/{side.session_id}/chat", {"message": "blue"})
        self.wait_for_events("answer turn")
        for side in sides:
            collector = live[side.key]
            collector.wait_settle(quiet_s=2.0, timeout=15)
            side.captures["live"] = collector.stop()
            print(f"[sse-parity] {side.label}: live frames={len(side.captures['live'].frames)}")

        # 5. replay capture: a fresh subscriber on the settled session must
        #    see snapshot first, then the replayable subset, in order.
        replay: dict[str, SseCollector] = {}
        for side in sides:
            collector = SseCollector(
                f"{side.base_url}/api/sessions/{side.session_id}/events", f"{side.key}:replay"
            ).start()
            collector.wait_for_frames(1, timeout=15)
            replay[side.key] = collector
        time.sleep(2.0)
        for side in sides:
            side.captures["replay"] = replay[side.key].stop()
            print(f"[sse-parity] {side.label}: replay frames={len(side.captures['replay'].frames)}")

        # 6. replay capture stays closed; stop the session (its live tail is
        #    recorded from a second short capture so shutdown noise does not
        #    contaminate the settled replay sequence).
        tail: dict[str, SseCollector] = {}
        for side in sides:
            tail[side.key] = SseCollector(
                f"{side.base_url}/api/sessions/{side.session_id}/events", f"{side.key}:stop"
            ).start()
            tail[side.key].wait_for_frames(1, timeout=15)
        for side in sides:
            self._delete(side, "session_stop", f"/api/sessions/{side.session_id}")
        time.sleep(1.5)
        for side in sides:
            side.captures["stop"] = tail[side.key].stop()
            print(f"[sse-parity] {side.label}: stop-tail frames={len(side.captures['stop'].frames)}")

        # 7. full history for the payload matrix (includes the opening block
        #    that is never replayed to subscribers). History events are
        #    normalised exactly like streamed frames.
        for side in sides:
            events = self.history_events(side)
            side.captures["history"] = [normalise_history_event(event) for event in (events or [])]
            print(f"[sse-parity] {side.label}: history events={len(side.captures['history'])}")

    # -- outputs -----------------------------------------------------------

    def write_capture(self, side: Side) -> dict[str, Any]:
        record = {
            "side": side.key,
            "implementation": side.implementation,
            "version": side.version,
            "session_id": "<session_id>",
            "captured_at": now_iso(),
            "responses": side.responses,
            "captures": {
                name: {
                    "status": capture.status,
                    "content_type": capture.content_type,
                    "frame_count": len(capture.frames),
                    "frames": capture.frames,
                    "comments": capture.comments,
                    "error": capture.error,
                }
                for name, capture in side.captures.items()
                if isinstance(capture, SseCapture)
            },
            "history": side.captures.get("history", []),
        }
        path = self.out_dir / f"capture-{side.key}.json"
        path.write_text(json.dumps(record, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        return record

    @property
    def out_dir(self) -> Path:
        return Path(self.args.out) if self.args.out else EVIDENCE_DIR


# ---------------------------------------------------------------------------
# diff
# ---------------------------------------------------------------------------


def collect_type_paths(value: Any, prefix: str, out: dict[str, set[str]]) -> None:
    """Record the scalar/container types observed at every data path."""
    if isinstance(value, dict):
        if prefix:
            out.setdefault(prefix, set()).add("object")
        for key, item in value.items():
            collect_type_paths(item, f"{prefix}.{key}" if prefix else key, out)
        return
    if isinstance(value, list):
        out.setdefault(prefix, set()).add("array")
        for item in value:
            collect_type_paths(item, f"{prefix}[]", out)
        return
    out.setdefault(prefix, set()).add(scalar_type(value))


def scalar_type(value: Any) -> str:
    if isinstance(value, bool):
        return "bool"
    if isinstance(value, int):
        return "int"
    if isinstance(value, float):
        return "float"
    if isinstance(value, str):
        return "str"
    if value is None:
        return "null"
    return type(value).__name__


def type_paths_for_frames(frames: list[dict[str, Any]]) -> dict[str, list[str]]:
    out: dict[str, set[str]] = {}
    for frame in frames:
        data = frame.get("data")
        if isinstance(data, dict):
            collect_type_paths(data, "", out)
    out.pop("", None)
    return {path: sorted(types) for path, types in out.items()}


def frames_by_type(frames: list[dict[str, Any]]) -> dict[str, list[dict[str, Any]]]:
    grouped: dict[str, list[dict[str, Any]]] = {}
    for frame in frames:
        event_type = frame.get("type")
        if not isinstance(event_type, str):
            continue
        grouped.setdefault(event_type, []).append(frame)
    return grouped


def union_keys(entries: list[dict[str, Any]]) -> set[str]:
    keys: set[str] = set()
    for entry in entries:
        data = entry.get("data")
        if isinstance(data, dict):
            keys.update(data.keys())
    return keys


def scalar_values_by_path(frames: list[dict[str, Any]]) -> dict[str, list[str]]:
    """Collect the distinct scalar values observed at each data path.

    Structural and volatile values are excluded: containers are walked, and
    ids/seq/timestamps were already normalised to placeholders.
    """
    values: dict[str, set[str]] = {}

    def walk(value: Any, path: str) -> None:
        if isinstance(value, dict):
            for key, item in value.items():
                walk(item, f"{path}.{key}" if path else key)
            return
        if isinstance(value, list):
            for item in value:
                walk(item, f"{path}[]")
            return
        values.setdefault(path, set()).add(json.dumps(value, ensure_ascii=False))

    for frame in frames:
        data = frame.get("data")
        if isinstance(data, dict):
            walk(data, "")
    return {path: sorted(items) for path, items in values.items()}


def value_diffs(reference: list[dict[str, Any]], target: list[dict[str, Any]]) -> dict[str, Any]:
    """Report data paths whose observed scalar value sets differ."""
    ref_values = scalar_values_by_path(reference)
    tgt_values = scalar_values_by_path(target)
    out: dict[str, Any] = {}
    for path in sorted(set(ref_values) | set(tgt_values)):
        ref = ref_values.get(path, [])
        tgt = tgt_values.get(path, [])
        if ref != tgt:
            out[path] = {"reference": ref, "target": tgt}
    return out


def frame_signature(frame: dict[str, Any]) -> str:
    """Order-comparison signature: the event type only."""
    return str(frame.get("type"))


def sequence_diff(reference: list[dict[str, Any]], target: list[dict[str, Any]], label: str) -> dict[str, Any]:
    ref_seq = [frame_signature(frame) for frame in reference]
    tgt_seq = [frame_signature(frame) for frame in target]
    ops = [
        {"op": op, "reference": ref_seq[i1:i2], "target": tgt_seq[j1:j2]}
        for op, i1, i2, j1, j2 in difflib.SequenceMatcher(a=ref_seq, b=tgt_seq).get_opcodes()
        if op != "equal"
    ]
    return {
        "capture": label,
        "reference_count": len(reference),
        "target_count": len(target),
        "reference_types": ref_seq,
        "target_types": tgt_seq,
        "equal": not ops,
        "ops": ops,
    }


def is_subsequence_of_history(frames: list[dict[str, Any]], history: list[dict[str, Any]]) -> bool:
    """True when every frame appears in history, in order (gaps allowed).

    Gaps cover the documented replay exclusions: lifecycle events are never
    replayed, and an already-resolved client_input_requested is suppressed.
    """
    cursor = 0
    for frame in frames:
        found = False
        while cursor < len(history):
            candidate = history[cursor]
            cursor += 1
            if candidate.get("type") == frame.get("type") and candidate.get("data") == frame.get("data"):
                found = True
                break
        if not found:
            return False
    return True


def replay_count_delta(reference_types: list[str], target_types: list[str]) -> dict[str, dict[str, int]]:
    """Per-type count difference for two replay type sequences."""
    from collections import Counter

    ref_counts = Counter(reference_types)
    tgt_counts = Counter(target_types)
    out: dict[str, dict[str, int]] = {}
    for event_type in sorted(set(ref_counts) | set(tgt_counts)):
        if ref_counts[event_type] != tgt_counts[event_type]:
            out[event_type] = {"reference": ref_counts[event_type], "target": tgt_counts[event_type]}
    return out


def compare_captures(reference: dict[str, Any], target: dict[str, Any]) -> dict[str, Any]:
    ref_caps = reference["captures"]
    tgt_caps = target["captures"]

    # -- per-type field matrix over every capture (live + replay + history)
    per_type: dict[str, Any] = {}
    history_ref = reference.get("history") or []
    history_tgt = target.get("history") or []
    streams: dict[str, tuple[list[dict[str, Any]], list[dict[str, Any]]]] = {
        "live": (ref_caps.get("live", {}).get("frames", []), tgt_caps.get("live", {}).get("frames", [])),
        "replay": (ref_caps.get("replay", {}).get("frames", []), tgt_caps.get("replay", {}).get("frames", [])),
        "history": (history_ref, history_tgt),
    }
    all_types: set[str] = set()
    for ref_frames, tgt_frames in streams.values():
        all_types.update(frames_by_type(ref_frames))
        all_types.update(frames_by_type(tgt_frames))

    for event_type in sorted(all_types):
        entry: dict[str, Any] = {}
        for stream_name, (ref_frames, tgt_frames) in streams.items():
            ref_group = frames_by_type(ref_frames).get(event_type, [])
            tgt_group = frames_by_type(tgt_frames).get(event_type, [])
            if not ref_group and not tgt_group:
                continue
            ref_keys = union_keys(ref_group)
            tgt_keys = union_keys(tgt_group)
            ref_shapes = type_paths_for_frames(ref_group) if ref_group else {}
            tgt_shapes = type_paths_for_frames(tgt_group) if tgt_group else {}
            entry[stream_name] = {
                "reference_count": len(ref_group),
                "target_count": len(tgt_group),
                "reference_present": bool(ref_group),
                "target_present": bool(tgt_group),
                "missing_in_target": sorted(ref_keys - tgt_keys),
                "extra_in_target": sorted(tgt_keys - ref_keys),
                "type_mismatch": sorted(
                    path
                    for path in set(ref_shapes) & set(tgt_shapes)
                    if ref_shapes[path] != tgt_shapes[path]
                ),
                "type_details": {
                    path: {"reference": ref_shapes[path], "target": tgt_shapes[path]}
                    for path in sorted(set(ref_shapes) & set(tgt_shapes))
                    if ref_shapes[path] != tgt_shapes[path]
                },
                "reference_keys": sorted(ref_keys),
                "target_keys": sorted(tgt_keys),
                "reference_shapes": ref_shapes,
                "target_shapes": tgt_shapes,
            }
        # envelope-level comparison (same fields for every event type)
        env_rows: dict[str, Any] = {}
        ref_all = [frame for ref_frames, _ in streams.values() for frame in ref_frames if frame.get("type") == event_type]
        tgt_all = [frame for _, tgt_frames in streams.values() for frame in tgt_frames if frame.get("type") == event_type]
        for field_name in ("event", "stream_id", "node_id", "execution_id", "colony_id", "correlation_id", "run_id"):
            ref_values = {json.dumps(frame.get(field_name), ensure_ascii=False) for frame in ref_all}
            tgt_values = {json.dumps(frame.get(field_name), ensure_ascii=False) for frame in tgt_all}
            env_rows[field_name] = {
                "reference": sorted(ref_values),
                "target": sorted(tgt_values),
                "equal": ref_values == tgt_values,
            }
        entry["envelope"] = env_rows
        # value-level comparison over the full history log: which scalar data
        # values actually differ (content, model, numbers, ...).
        entry["history_value_diff"] = value_diffs(
            frames_by_type(history_ref).get(event_type, []), frames_by_type(history_tgt).get(event_type, [])
        )
        per_type[event_type] = entry

    # -- sequence comparisons
    sequences = {
        "live": sequence_diff(
            ref_caps.get("live", {}).get("frames", []), tgt_caps.get("live", {}).get("frames", []), "live"
        ),
        "replay": sequence_diff(
            ref_caps.get("replay", {}).get("frames", []), tgt_caps.get("replay", {}).get("frames", []), "replay"
        ),
        "history": sequence_diff(history_ref, history_tgt, "history"),
    }
    # The documented intentional difference is Go's per-turn execution /
    # node-loop envelope, which the persistent Python queen loop does not emit
    # for the same turn. Drop those four types from the target and re-compare,
    # so every remaining order gap is a real one.
    go_envelope_types = {
        "execution_started",
        "execution_completed",
        "node_loop_started",
        "node_loop_completed",
    }
    target_live_without_envelope = [
        frame for frame in tgt_caps.get("live", {}).get("frames", []) if frame.get("type") not in go_envelope_types
    ]
    sequences["live_without_go_envelope"] = sequence_diff(
        ref_caps.get("live", {}).get("frames", []),
        target_live_without_envelope,
        "live_without_go_envelope",
    )

    # -- replay semantics: declared sets + observed sets + order guarantees
    ref_replay_types = [frame.get("type") for frame in ref_caps.get("replay", {}).get("frames", [])][1:]
    tgt_replay_types = [frame.get("type") for frame in tgt_caps.get("replay", {}).get("frames", [])][1:]
    # Go additionally replays the per-turn execution envelope it alone emits;
    # beyond that the replayed visible-chat subset must be the same multiset.
    envelope_replay = {"execution_started", "execution_completed", "execution_failed"}
    ref_replay_core = [event_type for event_type in ref_replay_types if event_type not in envelope_replay]
    tgt_replay_core = [event_type for event_type in tgt_replay_types if event_type not in envelope_replay]
    replay = {
        "reference_replayed_types": ref_replay_types,
        "target_replayed_types": tgt_replay_types,
        "same_replayed_multiset": sorted(ref_replay_types) == sorted(tgt_replay_types),
        "same_replayed_multiset_without_go_envelope": sorted(ref_replay_core) == sorted(tgt_replay_core),
        "multiset_delta_without_envelope": replay_count_delta(ref_replay_core, tgt_replay_core),
        "reference_replay_order_matches_history": is_subsequence_of_history(
            [frame for frame in ref_caps.get("replay", {}).get("frames", []) if frame.get("type") != "session_snapshot"],
            history_ref,
        ),
        "target_replay_order_matches_history": is_subsequence_of_history(
            [frame for frame in tgt_caps.get("replay", {}).get("frames", []) if frame.get("type") != "session_snapshot"],
            history_tgt,
        ),
        "reference_first_frame": (ref_caps.get("replay", {}).get("frames") or [{}])[0].get("type"),
        "target_first_frame": (tgt_caps.get("replay", {}).get("frames") or [{}])[0].get("type"),
    }

    # -- judge behaviour: the implicit verdict stream is history-only (it is
    #    in neither the default live subscription nor the replay subset), so
    #    it is compared from the full event log. Both sides must publish the
    #    same verdict per settled turn: a queen turn is judged when its park
    #    unblocks, and the skip_judge short-circuit still publishes the verdict.
    def _judge_rows(history: list[dict[str, Any]]) -> list[dict[str, Any]]:
        rows = []
        for event in frames_by_type(history).get("judge_verdict", []):
            data = event.get("data") or {}
            rows.append(
                {
                    "action": data.get("action"),
                    "feedback": data.get("feedback"),
                    "judge_type": data.get("judge_type"),
                    "iteration": data.get("iteration"),
                }
            )
        return rows

    ref_judge = _judge_rows(history_ref)
    tgt_judge = _judge_rows(history_tgt)
    judge = {
        "reference": ref_judge,
        "target": tgt_judge,
        "reference_count": len(ref_judge),
        "target_count": len(tgt_judge),
        "same_verdicts": ref_judge == tgt_judge,
        "live_visible": {
            "reference": any(frame.get("type") == "judge_verdict" for frame in ref_caps.get("live", {}).get("frames", [])),
            "target": any(frame.get("type") == "judge_verdict" for frame in tgt_caps.get("live", {}).get("frames", [])),
        },
        "replay_visible": {
            "reference": any(frame.get("type") == "judge_verdict" for frame in ref_caps.get("replay", {}).get("frames", [])),
            "target": any(frame.get("type") == "judge_verdict" for frame in tgt_caps.get("replay", {}).get("frames", [])),
        },
    }

    # -- summary counters
    type_gaps = {
        event_type: {
            stream_name: {
                "missing_in_target": stream["missing_in_target"],
                "extra_in_target": stream["extra_in_target"],
                "type_mismatch": stream["type_mismatch"],
                "type_details": stream.get("type_details", {}),
            }
            for stream_name, stream in entry.items()
            if isinstance(stream, dict) and stream_name not in {"envelope", "history_value_diff"}
        }
        for event_type, entry in per_type.items()
    }
    failing = {
        event_type: gap
        for event_type, gap in type_gaps.items()
        if any(stream["missing_in_target"] or stream["extra_in_target"] or stream["type_mismatch"] for stream in gap.values())
    }
    live_type_sets = {
        "reference": sorted(frames_by_type(ref_caps.get("live", {}).get("frames", []))),
        "target": sorted(frames_by_type(tgt_caps.get("live", {}).get("frames", []))),
    }
    live_extra = sorted(set(live_type_sets["target"]) - set(live_type_sets["reference"]))
    live_missing = sorted(set(live_type_sets["reference"]) - set(live_type_sets["target"]))

    return {
        "generated_at": now_iso(),
        "reference": {"version": reference.get("version"), "session_id": "<session_id>"},
        "target": {"version": target.get("version"), "session_id": "<session_id>"},
        "per_type": per_type,
        "sequences": sequences,
        "replay": replay,
        "judge": judge,
        "live_type_sets": live_type_sets,
        "live_missing_in_target": live_missing,
        "live_extra_in_target": live_extra,
        "types_with_field_gaps": failing,
    }


def write_summary(diff: dict[str, Any], path: Path) -> None:
    lines: list[str] = []
    lines.append("SSE payload parity — Python reference vs Go target")
    lines.append(f"generated: {diff['generated_at']}")
    lines.append(f"reference: {diff['reference']['version']}    target: {diff['target']['version']}")
    lines.append("")
    lines.append("== live type sets ==")
    lines.append(f"reference ({len(diff['live_type_sets']['reference'])}): " + ", ".join(diff["live_type_sets"]["reference"]))
    lines.append(f"target    ({len(diff['live_type_sets']['target'])}): " + ", ".join(diff["live_type_sets"]["target"]))
    if diff["live_missing_in_target"]:
        lines.append("missing in target: " + ", ".join(diff["live_missing_in_target"]))
    if diff["live_extra_in_target"]:
        lines.append("extra in target: " + ", ".join(diff["live_extra_in_target"]))
    lines.append("")
    for stream_name, seq in diff["sequences"].items():
        lines.append(f"== {stream_name} sequence ==")
        lines.append(
            f"{'EQUAL' if seq['equal'] else 'DIFF'} (reference={seq['reference_count']} target={seq['target_count']})"
        )
        for op in seq["ops"]:
            lines.append(
                f"  {op['op']}: reference={op['reference']} target={op['target']}"
            )
        lines.append("")
    lines.append("== per-type field gaps ==")
    if not diff["types_with_field_gaps"]:
        lines.append("none: every compared event type carried the same data keys and types in both implementations.")
    for event_type, streams in sorted(diff["types_with_field_gaps"].items()):
        lines.append(f"- {event_type}")
        for stream_name, gap in sorted(streams.items()):
            parts = []
            if gap["missing_in_target"]:
                parts.append("missing_in_target=" + ",".join(gap["missing_in_target"]))
            if gap["extra_in_target"]:
                parts.append("extra_in_target=" + ",".join(gap["extra_in_target"]))
            if gap["type_mismatch"]:
                parts.append("type_mismatch=" + ",".join(gap["type_mismatch"]))
            lines.append(f"    {stream_name}: " + "; ".join(parts))
            for data_path, detail in sorted(gap.get("type_details", {}).items()):
                lines.append(f"      {data_path}: reference={detail['reference']} target={detail['target']}")
    lines.append("")
    lines.append("== history value differences ==")
    any_values = False
    for event_type, entry in sorted(diff["per_type"].items()):
        value_diff = entry.get("history_value_diff") or {}
        if not value_diff:
            continue
        any_values = True
        lines.append(f"- {event_type}")
        for data_path, values in sorted(value_diff.items()):
            lines.append(f"    {data_path}: reference={values['reference']} target={values['target']}")
    if not any_values:
        lines.append("none: every compared data value matched.")
    lines.append("")
    lines.append("== judge verdicts (history-only) ==")
    lines.append(
        f"reference ({diff['judge']['reference_count']}): "
        + json.dumps(diff["judge"]["reference"], ensure_ascii=False)
    )
    lines.append(
        f"target    ({diff['judge']['target_count']}): "
        + json.dumps(diff["judge"]["target"], ensure_ascii=False)
    )
    lines.append("same verdicts: " + ("yes" if diff["judge"]["same_verdicts"] else "NO"))
    lines.append(
        "visible in live/replay: reference="
        + f"{diff['judge']['live_visible']['reference']}/{diff['judge']['replay_visible']['reference']}"
        + " target="
        + f"{diff['judge']['live_visible']['target']}/{diff['judge']['replay_visible']['target']}"
    )
    lines.append("")
    lines.append("== replay ==")
    lines.append(f"reference first frame: {diff['replay']['reference_first_frame']}")
    lines.append(f"target first frame:    {diff['replay']['target_first_frame']}")
    lines.append(
        "same replayed multiset: " + ("yes" if diff["replay"]["same_replayed_multiset"] else "NO")
    )
    lines.append(
        "same replayed multiset (excluding Go-only execution envelope): "
        + ("yes" if diff["replay"]["same_replayed_multiset_without_go_envelope"] else "NO")
    )
    if diff["replay"].get("multiset_delta_without_envelope"):
        lines.append("  per-type count delta: " + json.dumps(diff["replay"]["multiset_delta_without_envelope"]))
    lines.append(
        "reference replay order follows history: "
        + ("yes" if diff["replay"]["reference_replay_order_matches_history"] else "NO")
    )
    lines.append(
        "target replay order follows history: "
        + ("yes" if diff["replay"]["target_replay_order_matches_history"] else "NO")
    )
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--workdir", help="scratch root (default: $TMPDIR/hive-sse-parity-<pid>)")
    parser.add_argument("--out", help=f"evidence directory (default: {EVIDENCE_DIR})")
    parser.add_argument("--no-build", action="store_true", help="reuse the existing Go binary")
    parser.add_argument("--keep", action="store_true", help="keep HOME/logs after the run")
    parser.add_argument("--port-reference", type=int, default=0)
    parser.add_argument("--port-target", type=int, default=0)
    args = parser.parse_args()

    probe = Probe(args)
    try:
        probe.prepare()
        assert probe.reference and probe.target
        probe.create_sessions()
        probe.run_scenario()
        probe.out_dir.mkdir(parents=True, exist_ok=True)
        reference_record = probe.write_capture(probe.reference)
        target_record = probe.write_capture(probe.target)
        diff = compare_captures(reference_record, target_record)
        (probe.out_dir / "diff.json").write_text(json.dumps(diff, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        write_summary(diff, probe.out_dir / "summary.txt")
        print(f"[sse-parity] evidence written to {probe.out_dir}")
        print((probe.out_dir / "summary.txt").read_text(encoding="utf-8"))
        return 0
    finally:
        for side in (probe.reference, probe.target):
            if side is not None:
                terminate(side)
        if probe.stub_server is not None:
            probe.stub_server.shutdown()
        if not args.keep and probe.root.exists():
            shutil.rmtree(probe.root, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
