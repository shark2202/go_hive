#!/usr/bin/env python3
"""Freeze the Hive HTTP response contract: Python reference vs Go target.

Starts a throwaway Python hive (the contract reference) and a throwaway Go
hive (the target being aligned) against separate HIVE_HOME directories and
free ports, probes one fixed endpoint set with identical input, canonicalises
the JSON, and writes:

  go_hive/contracts/api-responses/index.json
  go_hive/contracts/api-responses/reference/<endpoint>.json
  go_hive/contracts/api-responses/target/<endpoint>.json
  go_hive/contracts/api-responses/shapes/reference/<endpoint>.shape.json
  go_hive/contracts/api-responses/shapes/target/<endpoint>.shape.json
  go_hive/contracts/api-responses/diff.json
  go_hive/docs/api-contract-diff.md

The reference payload is the contract; every diff is reported in the
direction reference -> target.  Dynamic values (ids, timestamps, ports, temp
paths, gauges) are replaced by placeholders while key presence is preserved.

Standard library only; no third-party imports, no LLM calls (both servers run
without a usable model on purpose so the probe stays offline and
deterministic).

Usage:
    uv run python go_hive/scripts/freeze_api_responses.py
    uv run python go_hive/scripts/freeze_api_responses.py --keep --sse-seconds 5
    uv run python go_hive/scripts/freeze_api_responses.py --no-build
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
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

sys.path.insert(0, str(Path(__file__).resolve().parent))
import api_contract_lib as lib  # noqa: E402

REPO_ROOT = Path(__file__).resolve().parents[2]
GO_DIR = REPO_ROOT / "go_hive" / "go-hive"
GO_BINARY = GO_DIR / "dist" / "hive-contract"
RESPONSES_DIR = REPO_ROOT / "go_hive" / "contracts" / "api-responses"
REPORT_PATH = REPO_ROOT / "go_hive" / "docs" / "api-contract-diff.md"
ROUTE_MANIFEST = REPO_ROOT / "go_hive" / "contracts" / "api-routes.json"

QUEEN_ID = "queen_growth"
LLM_PROVIDER = "openai"
LLM_MODEL = "contract-probe-model"
LLM_KEY_ENV = "HIVE_CONTRACT_PROBE_KEY"

HEALTH_WAIT_S = 180.0
SETTLE_TIMEOUT_S = 25.0
SETTLE_STABLE_S = 1.5
MIN_EVENTS = 5


class StubLLMHandler(BaseHTTPRequestHandler):
    """OpenAI-compatible local stub so the kickoff turn is deterministic.

    Both servers receive the same placeholder configuration; this stub answers
    ``/v1/chat/completions`` (streaming or not) with a fixed assistant message,
    so the freeze never depends on a real vendor or network.
    """

    protocol_version = "HTTP/1.1"

    def log_message(self, format: str, *args: Any) -> None:  # noqa: A002 - stdlib signature
        return

    def do_POST(self) -> None:  # noqa: N802 - stdlib signature
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length) if length else b"{}"
        try:
            body = json.loads(raw.decode("utf-8"))
        except json.JSONDecodeError:
            body = {}
        model = body.get("model", LLM_MODEL)
        content = "CONTRACT_OK"
        if body.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.end_headers()
            for choice in (
                {"delta": {"role": "assistant", "content": content}, "finish_reason": None},
                {"delta": {}, "finish_reason": "stop"},
            ):
                chunk = {
                    "id": "chatcmpl-contract",
                    "object": "chat.completion.chunk",
                    "created": int(time.time()),
                    "model": model,
                    "choices": [{"index": 0, **choice}],
                }
                self.wfile.write(f"data: {json.dumps(chunk)}\n\n".encode())
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
            return
        payload = {
            "id": "chatcmpl-contract",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": model,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
        }
        data = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

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
    port = int(server.server_address[1])
    return server, f"http://127.0.0.1:{port}/v1"

# Probes are executed in this order.  ``session`` paths substitute the id the
# side returned from POST /api/sessions.
PROBES: list[dict[str, Any]] = [
    {"id": "health", "label": "健康检查", "method": "GET", "path": "/api/health", "ignore_prefixes": ["resources."]},
    {"id": "queen_profiles", "label": "Queen 档案", "method": "GET", "path": "/api/queen/profiles"},
    {"id": "queen_tools", "label": "Queen 工具库", "method": "GET", "path": f"/api/queen/{QUEEN_ID}/tools"},
    {"id": "skills", "label": "技能目录", "method": "GET", "path": "/api/skills"},
    {"id": "config_llm", "label": "LLM 配置", "method": "GET", "path": "/api/config/llm"},
    {"id": "models_overview", "label": "模型总览", "method": "GET", "path": "/api/models/overview"},
    {"id": "credentials_specs", "label": "凭据规格目录", "method": "GET", "path": "/api/credentials/specs"},
    {
        "id": "session_create",
        "label": "创建会话",
        "method": "POST",
        "path": "/api/sessions",
        "body": {"queen_name": QUEEN_ID},
        "creates_session": True,
    },
    {"id": "sessions_list", "label": "会话列表", "method": "GET", "path": "/api/sessions"},
    {"id": "session_detail", "label": "会话详情", "method": "GET", "path": "/api/sessions/{session}"},
    {"id": "session_events", "label": "会话事件流", "method": "GET", "path": "/api/sessions/{session}/events", "sse": True},
    {"id": "session_events_history", "label": "事件历史", "method": "GET", "path": "/api/sessions/{session}/events/history"},
    {"id": "session_snapshot", "label": "会话快照", "method": "GET", "path": "/api/sessions/{session}/snapshot"},
    {"id": "session_stats", "label": "会话统计", "method": "GET", "path": "/api/sessions/{session}/stats"},
]

# Applied after the probe table: older Go routes may be absent.
EXPECTED_ROUTE_PATHS = {
    "health": "GET /api/health",
    "queen_profiles": "GET /api/queen/profiles",
    "queen_tools": f"GET /api/queen/{{{QUEEN_ID}}}/tools",
}


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

    @property
    def is_json_content_type(self) -> bool:
        return "application/json" in self.content_type.lower()


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


@dataclass
class SseCapture:
    status: int = 0
    content_type: str = ""
    event_names: list[str] = field(default_factory=list)
    payload_types: list[str] = field(default_factory=list)
    frames: list[dict[str, Any]] = field(default_factory=list)
    error: str = ""


class SseCollector:
    def __init__(self, url: str, seconds: float, canonicalize: bool = True) -> None:
        self.url = url
        self.seconds = seconds
        self.capture = SseCapture()
        self.canonicalize = canonicalize
        self._stop = threading.Event()
        self._response = None
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self) -> "SseCollector":
        self._thread.start()
        return self

    def run(self) -> SseCapture:
        self._thread.start()
        self._thread.join(timeout=self.seconds + 15)
        self.stop()
        return self.capture

    def stop(self) -> None:
        self._stop.set()
        response = self._response
        if response is not None:
            try:
                response.close()
            except Exception:
                pass
        self._thread.join(timeout=5)

    def _run(self) -> None:
        request = urllib.request.Request(self.url, headers={"Accept": "text/event-stream"})
        try:
            with urllib.request.urlopen(request, timeout=self.seconds + 10) as response:
                self._response = response
                self.capture.status = response.status
                self.capture.content_type = response.headers.get("Content-Type", "")
                deadline = time.time() + self.seconds
                event_name: str | None = None
                data_lines: list[str] = []
                while not self._stop.is_set() and time.time() < deadline:
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
                        continue
                    if line.startswith("event:"):
                        event_name = line[len("event:") :].strip()
                    elif line.startswith("data:"):
                        data_lines.append(line[len("data:") :].lstrip())
        except Exception as exc:  # noqa: BLE001 - report, do not abort capture
            if not self._stop.is_set():
                self.capture.error = str(exc)

    def _record(self, event_name: str | None, data: str) -> None:
        if event_name:
            self.capture.event_names.append(event_name)
        frame: dict[str, Any] = {"event": event_name}
        try:
            decoded = json.loads(data)
        except json.JSONDecodeError:
            frame["raw_data"] = data[:500]
            self.capture.frames.append(frame)
            return
        if isinstance(decoded, dict):
            payload_type = decoded.get("type")
            if isinstance(payload_type, str):
                self.capture.payload_types.append(payload_type)
            frame["type"] = payload_type
            frame["seq"] = lib.canonicalize(decoded.get("seq"), "seq")
            frame["envelope"] = lib.canonicalize(decoded) if self.canonicalize else decoded
        else:
            frame["raw_data"] = json.dumps(decoded, ensure_ascii=False)[:500]
        self.capture.frames.append(frame)


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
    results: dict[str, Any] = field(default_factory=dict)

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.port}"


class FreezeError(RuntimeError):
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
        raise FreezeError(f"go build failed:\n{proc.stdout}\n{proc.stderr}")


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
            raise FreezeError(
                f"{side.label} exited with code {side.process.returncode} before /api/health.\n"
                f"log tail:\n{log_tail(side.log_path)}"
            )
        result = http_request("GET", f"{side.base_url}/api/health", timeout=5)
        if result.status == 200:
            return
        last_error = result.error or f"HTTP {result.status}"
        time.sleep(0.25)
    raise FreezeError(
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
        if re.match(r"^hive\s+\d+\.\d+", stripped):
            return stripped
    return lines[-1] if lines else f"<empty, rc={proc.returncode}>"


def git_capture(argv: list[str]) -> str:
    try:
        return subprocess.run(
            ["git", *argv], cwd=str(REPO_ROOT), capture_output=True, text=True, timeout=20
        ).stdout.strip() or "unknown"
    except (OSError, subprocess.TimeoutExpired):
        return "unknown"


# ---------------------------------------------------------------------------
# capture
# ---------------------------------------------------------------------------


class Freezer:
    def __init__(self, args: argparse.Namespace) -> None:
        self.args = args
        self.root = Path(args.workdir).expanduser() if args.workdir else Path(
            os.environ.get("TMPDIR", "/tmp")
        ) / f"hive-contract-{os.getpid()}"
        self.reference: Side | None = None
        self.target: Side | None = None
        self.captures: dict[str, dict[str, HttpResult | SseCapture]] = {}
        self.route_manifest: dict[str, Any] = {}
        self.proto = lib.load_proto()
        self.stub_server: ThreadingHTTPServer | None = None
        self.llm_api_base = ""

    # -- setup -------------------------------------------------------------

    def prepare(self) -> None:
        if not (REPO_ROOT / "core" / "framework" / "cli.py").is_file():
            raise FreezeError(f"cannot find the Python hive checkout under {REPO_ROOT}")
        if not self.args.no_build:
            build_go_binary(GO_DIR, GO_BINARY)
        if not GO_BINARY.is_file():
            raise FreezeError(f"Go binary missing: {GO_BINARY} (run without --no-build)")

        uv = shutil.which("uv")
        if uv is None:
            raise FreezeError("uv is not on PATH; the Python side starts with `uv run --offline hive`")

        self.root.mkdir(parents=True, exist_ok=True)
        self.stub_server, self.llm_api_base = start_stub_llm()
        print(f"[freeze] stub LLM: {self.llm_api_base}")
        ref_home = make_home(self.root, "reference-home", self.llm_api_base)
        go_home = make_home(self.root, "target-home", self.llm_api_base)
        ref_port = self.args.port_reference or free_port()
        go_port = self.args.port_target or free_port()
        while go_port == ref_port:
            go_port = free_port()

        self.reference = Side("reference", "Python reference", "python", ref_port, ref_home, self.root / "logs" / "reference.log")
        self.target = Side("target", "Go target", "go_hive", go_port, go_home, self.root / "logs" / "target.log")

        base_env = {
            k: v
            for k, v in os.environ.items()
            if k not in {"HIVE_HOME", "HIVE_CREDENTIAL_KEY", "HIVE_RUNTIME_PORT", "HIVE_DESKTOP_TOKEN"}
        }
        base_env["PYTHONUNBUFFERED"] = "1"
        base_env[LLM_KEY_ENV] = "contract-probe-dummy"

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

        print(f"[freeze] reference home: {ref_home}")
        print(f"[freeze] target home:    {go_home}")
        print(f"[freeze] ports: reference={ref_port} target={go_port}")
        for side in (self.reference, self.target):
            spawn(side)
        for side in (self.reference, self.target):
            wait_health(side)
            side.version = read_version(
                [uv, "run", "--offline", "hive", "--version"] if side.key == "reference" else [str(GO_BINARY), "--version"],
                side.cwd or REPO_ROOT,
                side.env,
            )
            print(f"[freeze] {side.label}: {side.version}")

        if ROUTE_MANIFEST.is_file():
            self.route_manifest = json.loads(ROUTE_MANIFEST.read_text(encoding="utf-8"))

    # -- probing -----------------------------------------------------------

    def _url(self, side: Side, probe: dict[str, Any]) -> str:
        path = probe["path"]
        if "{session}" in path:
            if not side.session_id:
                raise FreezeError(f"{side.label} has no session id for {probe['id']}")
            path = path.replace("{session}", side.session_id)
        return side.base_url + path

    def probe_one(self, side: Side, probe: dict[str, Any]) -> Any:
        url = self._url(side, probe)
        if probe.get("sse"):
            return SseCollector(url, self.args.sse_seconds).run()
        return http_request(probe["method"], url, body=probe.get("body"), timeout=120)

    def wait_for_events(self) -> None:
        """Wait until both history logs stop growing (kickoff turns settle).

        Both servers run the same opening turn that calls the stub model:
        the session starts its queen loop immediately, answers from the stub
        and parks on turn_done. Stability, not a fixed count, is the
        completion condition so the probe does not bake one implementation's
        event count into the harness.
        """
        assert self.reference and self.target
        deadline = time.time() + SETTLE_TIMEOUT_S
        last: dict[str, int] = {"reference": -1, "target": -1}
        stable_since: float | None = None
        while time.time() < deadline:
            totals: dict[str, int] = {}
            for side in (self.reference, self.target):
                result = http_request("GET", f"{side.base_url}/api/sessions/{side.session_id}/events/history", timeout=15)
                total = result.payload.get("total") if isinstance(result.payload, dict) else None
                totals[side.key] = total if isinstance(total, int) else -1
            if totals == last and all(value >= MIN_EVENTS for value in totals.values()):
                if stable_since is None:
                    stable_since = time.time()
                elif time.time() - stable_since >= SETTLE_STABLE_S:
                    return
            else:
                stable_since = None
                last = totals
            time.sleep(0.25)
        print(f"[freeze] warning: event logs did not settle within {SETTLE_TIMEOUT_S:.0f}s (last totals={last})")

    def capture(self) -> None:
        assert self.reference and self.target
        sides = (self.reference, self.target)
        for probe in PROBES:
            if probe["id"] == "session_create":
                for side in sides:
                    result = self.probe_one(side, probe)
                    side.results[probe["id"]] = result
                    if isinstance(result, HttpResult) and isinstance(result.payload, dict):
                        session_id = result.payload.get("session_id")
                        if isinstance(session_id, str):
                            side.session_id = session_id
                    if not side.session_id:
                        raise FreezeError(f"{side.label}: POST /api/sessions did not return session_id")
                self.wait_for_events()
                continue
            for side in sides:
                side.results[probe["id"]] = self.probe_one(side, probe)
            print(f"[freeze] captured {probe['id']}")

    # -- outputs -----------------------------------------------------------

    def write_capture(self, side: Side, probe: dict[str, Any], result: Any) -> dict[str, Any]:
        side_dir = RESPONSES_DIR / side.key
        shape_dir = RESPONSES_DIR / "shapes" / side.key
        side_dir.mkdir(parents=True, exist_ok=True)
        shape_dir.mkdir(parents=True, exist_ok=True)

        payload: Any
        status = 0
        content_type = ""
        notes: list[str] = []
        if isinstance(result, SseCapture):
            status = result.status
            content_type = result.content_type
            payload = {
                "content_type": result.content_type,
                "frames": result.frames,
                "frame_count": len(result.frames),
                "payload_types": result.payload_types,
                "event_names": result.event_names,
            }
            if result.error:
                notes.append(result.error)
        else:
            status = result.status
            content_type = result.content_type
            if result.error:
                notes.append(result.error)
            payload = result.payload

        canonical = lib.canonicalize(payload)
        shape = lib.shape_of(canonical) if canonical is not None else {}
        counts = lib.count_arrays(canonical) if canonical is not None else {}
        record = {
            "endpoint": probe["id"],
            "label": probe["label"],
            "method": probe["method"],
            "path": probe["path"],
            "side": side.key,
            "implementation": side.implementation,
            "version": side.version,
            "status": status,
            "content_type": content_type,
            "captured_at": now_iso(),
            "canonical": canonical,
            "shape": shape,
            "counts": counts,
            "notes": notes,
        }
        (side_dir / f"{probe['id']}.json").write_text(json.dumps(record, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        (shape_dir / f"{probe['id']}.shape.json").write_text(
            json.dumps({"endpoint": probe["id"], "shape": shape, "counts": counts}, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        return record

    def records(self) -> dict[str, dict[str, Any]]:
        assert self.reference and self.target
        out: dict[str, dict[str, Any]] = {}
        for probe in PROBES:
            out[probe["id"]] = {
                "reference": self.write_capture(self.reference, probe, self.reference.results[probe["id"]]),
                "target": self.write_capture(self.target, probe, self.target.results[probe["id"]]),
                "probe": probe,
            }
        return out

    # -- diff --------------------------------------------------------------

    def diff(self, records: dict[str, dict[str, Any]]) -> dict[str, Any]:
        endpoints: list[dict[str, Any]] = []
        for probe in PROBES:
            entry = diff_endpoint(
                probe,
                records[probe["id"]]["reference"],
                records[probe["id"]]["target"],
                self.proto,
            )
            endpoints.append(entry)

        totals = {
            "endpoints": len(endpoints),
            "pass": sum(1 for e in endpoints if e["verdict"] == "PASS"),
            "fail": sum(1 for e in endpoints if e["verdict"] != "PASS"),
            "missing_in_target": sum(len(e["missing_in_target"]) for e in endpoints),
            "extra_in_target": sum(len(e["extra_in_target"]) for e in endpoints),
            "type_mismatch": sum(len(e["type_mismatch"]) for e in endpoints),
            "count_mismatch": sum(len(e["count_mismatch"]) for e in endpoints),
            "semantic_failures": sum(1 for e in endpoints for c in e["semantic"] if not c["ok"]),
        }
        return {
            "schema": "hive-api-contract-diff/1",
            "generated_at": now_iso(),
            "direction": "reference(python) -> target(go_hive)",
            "reference_id": f"python@{git_capture(['rev-parse', 'HEAD'])}",
            "target_id": f"go_hive@{git_capture(['rev-parse', 'HEAD'])}",
            "reference": {
                "implementation": "python",
                "version": self.reference.version if self.reference else "",
                "commit": git_capture(["rev-parse", "HEAD"]),
                "last_core_commit": git_capture(["log", "-1", "--format=%H", "--", "core"]),
                "last_tools_commit": git_capture(["log", "-1", "--format=%H", "--", "tools"]),
            },
            "target": {
                "implementation": "go_hive",
                "version": self.target.version if self.target else "",
                "commit": git_capture(["rev-parse", "HEAD"]),
            },
            "probe": {
                "queen_id": QUEEN_ID,
                "llm": {
                    "provider": LLM_PROVIDER,
                    "model": LLM_MODEL,
                    "api_base": self.llm_api_base,
                    "key_env": LLM_KEY_ENV,
                    "note": "local OpenAI-compatible stub; deterministic kickoff turn, no vendor call, no network",
                },
                "sse_seconds": self.args.sse_seconds,
            },
            "totals": totals,
            "endpoints": endpoints,
            "non_verbatim": NON_VERBATIM,
        }

    def write_index(self, records: dict[str, dict[str, Any]], diff: dict[str, Any]) -> None:
        index = {
            "schema": "hive-api-responses/1",
            "generated_at": now_iso(),
            "direction": diff["direction"],
            "reference": diff["reference"],
            "target": diff["target"],
            "probe": diff["probe"],
            "endpoints": [
                {
                    "endpoint": probe["id"],
                    "label": probe["label"],
                    "method": probe["method"],
                    "path": probe["path"],
                    "reference_status": records[probe["id"]]["reference"]["status"],
                    "target_status": records[probe["id"]]["target"]["status"],
                    "reference_file": f"reference/{probe['id']}.json",
                    "target_file": f"target/{probe['id']}.json",
                    "reference_shape": f"shapes/reference/{probe['id']}.shape.json",
                    "target_shape": f"shapes/target/{probe['id']}.shape.json",
                }
                for probe in PROBES
            ],
            "totals": diff["totals"],
        }
        (RESPONSES_DIR / "index.json").write_text(json.dumps(index, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        (RESPONSES_DIR / "diff.json").write_text(json.dumps(diff, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    def report(self, diff: dict[str, Any], index: dict[str, Any]) -> str:
        return render_markdown(diff, index, self.route_manifest)

    # -- lifecycle ---------------------------------------------------------

    def run(self) -> int:
        try:
            self.prepare()
            self.capture()
            records = self.records()
            diff = self.diff(records)
            self.write_index(records, diff)
            index = json.loads((RESPONSES_DIR / "index.json").read_text(encoding="utf-8"))
            report_path = Path(self.args.report).expanduser()
            report_path.parent.mkdir(parents=True, exist_ok=True)
            report_path.write_text(self.report(diff, index), encoding="utf-8")
            totals = diff["totals"]
            print("")
            print(f"[freeze] responses: {RESPONSES_DIR}")
            print(f"[freeze] report:    {report_path}")
            print(
                f"[freeze] endpoints: {totals['pass']}/{totals['endpoints']} PASS; "
                f"missing_in_target={totals['missing_in_target']} extra_in_target={totals['extra_in_target']} "
                f"type_mismatch={totals['type_mismatch']} count_mismatch={totals['count_mismatch']} "
                f"semantic_failures={totals['semantic_failures']}"
            )
            for entry in diff["endpoints"]:
                if entry["verdict"] != "PASS":
                    print(f"[freeze]   FAIL {entry['method']} {entry['path']} ({entry['diff_count']} diffs)")
            if self.args.fail_on_diff and totals["fail"]:
                return 1
            return 0
        finally:
            for side in (self.reference, self.target):
                if side is not None:
                    terminate(side)
            if self.stub_server is not None:
                self.stub_server.shutdown()
                self.stub_server.server_close()
            if not self.args.keep and self.root.is_dir():
                shutil.rmtree(self.root, ignore_errors=True)


# ---------------------------------------------------------------------------
# diff engine
# ---------------------------------------------------------------------------


def hardening_kind(py_type: str | None, go_type: str | None) -> str:
    if py_type is None:
        return "extra"
    if go_type is None:
        return "missing"
    if py_type == go_type:
        return "equal"
    return "type"


def proto_label(spec: lib.ProtoSpec, endpoint: str, field_path: str, fallback_type: str | None) -> str:
    resolved = lib.resolve_field(spec, endpoint, field_path)
    if resolved.field is not None:
        if resolved.struct_key:
            return "google.protobuf.Struct (free-form key)"
        return resolved.field.rendered_type()
    if resolved.note:
        return f"undeclared ({resolved.note})"
    return lib.render_json_kind(fallback_type or "object")


GO_EXTENSION_FIELDS = {
    "specs[].accounts",
    "specs[].tool_modules",
}


def is_freeform_data(field: str) -> bool:
    """Field paths inside a Struct-typed payload are data keys, not contract fields.

    ``events[].data.*`` and ``frames[].envelope.data.*`` are free-form JSON
    blobs (google.protobuf.Struct): their keys vary per event type, so they are
    compared per event type in the semantic checks instead of field by field.
    """
    return "[].data." in field or ".envelope.data." in field


def normalizability(field: str, py_type: str | None, go_type: str | None) -> tuple[str, str]:
    if field in GO_EXTENSION_FIELDS:
        return "yes", "target extension: additive field; drop it when projecting onto the reference contract"
    if py_type is None:
        return "yes", "target extension: additive field; not part of the reference contract"
    if go_type is None:
        return "yes (presence)", "reference emits a field the target omits; proto optional captures absence, not a value"
    if py_type == "null" or go_type == "null":
        return "no", "null vs value: proto3 JSON folds null and unset together; the distinction is lost"
    if py_type.startswith("number") and go_type.startswith("number"):
        return "yes (numeric canonicalization)", "both are JSON numbers; proto double accepts 3 and 3.0"
    return "no", "different JSON types cannot be projected onto one proto field"


def diff_endpoint(
    probe: dict[str, Any],
    reference: dict[str, Any],
    target: dict[str, Any],
    spec: lib.ProtoSpec,
) -> dict[str, Any]:
    endpoint = probe["id"]
    ignore_prefixes = probe.get("ignore_prefixes", [])
    ref_shape = {k: v for k, v in reference["shape"].items() if not any(k.startswith(p) for p in ignore_prefixes)}
    go_shape = {k: v for k, v in target["shape"].items() if not any(k.startswith(p) for p in ignore_prefixes)}

    def array_empty(side_payload: Any, key: str) -> bool:
        container = lib.array_container(key)
        if not container:
            return False
        resolved = lib.resolve_array(side_payload, container)
        return resolved == []

    missing: list[dict[str, Any]] = []
    extra: list[dict[str, Any]] = []
    type_mismatch: list[dict[str, Any]] = []

    for key in sorted(set(ref_shape) - set(go_shape)):
        if array_empty(target["canonical"], key) or is_freeform_data(key):
            continue
        missing.append(
            {
                "field": key,
                "proto": proto_label(spec, endpoint, key, ref_shape[key]),
                "reference_type": ref_shape[key],
                "target_type": "absent",
                "json_normalizable": normalizability(key, ref_shape[key], None)[0],
                "note": normalizability(key, ref_shape[key], None)[1],
            }
        )
    for key in sorted(set(go_shape) - set(ref_shape)):
        if array_empty(reference["canonical"], key) or is_freeform_data(key):
            continue
        extra.append(
            {
                "field": key,
                "proto": proto_label(spec, endpoint, key, go_shape[key]),
                "reference_type": "absent",
                "target_type": go_shape[key],
                "json_normalizable": normalizability(key, None, go_shape[key])[0],
                "note": normalizability(key, None, go_shape[key])[1],
            }
        )
    for key in sorted(set(ref_shape) & set(go_shape)):
        if is_freeform_data(key):
            continue
        if ref_shape[key] != go_shape[key]:
            normal, note = normalizability(key, ref_shape[key], go_shape[key])
            type_mismatch.append(
                {
                    "field": key,
                    "proto": proto_label(spec, endpoint, key, ref_shape[key]),
                    "reference_type": ref_shape[key],
                    "target_type": go_shape[key],
                    "json_normalizable": normal,
                    "note": note,
                }
            )

    count_mismatch: list[dict[str, Any]] = []
    ref_counts = reference.get("counts", {})
    go_counts = target.get("counts", {})
    for key in sorted(set(ref_counts) | set(go_counts)):
        rv, gv = ref_counts.get(key), go_counts.get(key)
        if rv == gv:
            continue
        count_mismatch.append(
            {
                "field": key,
                "proto": proto_label(spec, endpoint, key, "array"),
                "reference_type": f"array[{rv}]",
                "target_type": f"array[{gv}]",
                "json_normalizable": "no",
                "note": "element count differs; only the element type is proto-verifiable",
            }
        )

    semantic = semantic_checks(endpoint, reference["canonical"], target["canonical"])

    notes: list[str] = []
    if ignore_prefixes:
        notes.append(
            "比较时忽略 " + "、".join(ignore_prefixes) + "：这些是进程环境读数（内存采样/渲染器数），"
            "不是 API 契约字段；reference 触发采样时才会出现 avail_mb/renderers。"
        )
    if endpoint in {"session_events", "session_events_history"}:
        notes.append(
            "events[].data 与 frames[].envelope.data 是 google.protobuf.Struct 自由字段："
            "按事件类型比较 data 键集合（见语义检查），不逐字段列出。"
        )
    if endpoint == "session_stats" and any(row["field"].startswith("event_bus.events_by_type") for row in missing + extra + type_mismatch):
        notes.append(
            "event_bus.events_by_type 是 map<string, int64>：键名即事件类型，缺失的键等价于 target 少发对应事件类型。"
        )

    diff_count = len(missing) + len(extra) + len(type_mismatch) + len(count_mismatch) + sum(
        1 for check in semantic if not check["ok"]
    )
    return {
        "endpoint": endpoint,
        "label": probe["label"],
        "method": probe["method"],
        "path": probe["path"],
        "reference_status": reference["status"],
        "target_status": target["status"],
        "reference_content_type": reference["content_type"],
        "target_content_type": target["content_type"],
        "verdict": "PASS" if diff_count == 0 else "FAIL",
        "diff_count": diff_count,
        "missing_in_target": missing,
        "extra_in_target": extra,
        "type_mismatch": type_mismatch,
        "count_mismatch": count_mismatch,
        "semantic": semantic,
        "notes": notes,
    }


def _values_at(payload: Any, dotted: str) -> Any:
    current = payload
    for part in dotted.split("."):
        if isinstance(current, dict):
            current = current.get(part)
        else:
            return None
    return current


def _names(items: Any, key: str = "name") -> list[str]:
    if not isinstance(items, list):
        return []
    return sorted(item[key] for item in items if isinstance(item, dict) and isinstance(item.get(key), str))


def _event_types(history: Any) -> list[Any]:
    events = history.get("events") if isinstance(history, dict) else None
    if not isinstance(events, list):
        return []
    return [event.get("type") for event in events if isinstance(event, dict) and isinstance(event.get("type"), str)]


def _data_keys_by_type(envelopes: Any) -> dict[str, list[str]]:
    """Union of ``data`` keys per event type (free-form payload contract)."""
    out: dict[str, list[str]] = {}
    if not isinstance(envelopes, list):
        return out
    for envelope in envelopes:
        if not isinstance(envelope, dict):
            continue
        event_type = envelope.get("type")
        data = envelope.get("data")
        if not isinstance(event_type, str) or not isinstance(data, dict):
            continue
        out.setdefault(event_type, [])
        for key in data:
            if key not in out[event_type]:
                out[event_type].append(key)
    return {event_type: sorted(keys) for event_type, keys in out.items()}


def semantic_checks(endpoint: str, reference: Any, target: Any) -> list[dict[str, Any]]:
    checks: list[dict[str, Any]] = []

    def add(label: str, ok: bool, detail: str = "") -> None:
        checks.append({"check": label, "ok": bool(ok), "detail": detail})

    if endpoint == "health":
        add("status == ok", _values_at(reference, "status") == "ok" and _values_at(target, "status") == "ok",
            f"reference={_values_at(reference, 'status')!r} target={_values_at(target, 'status')!r}")
    elif endpoint == "queen_profiles":
        ref_ids, go_ids = _names(_values_at(reference, "queens"), "id"), _names(_values_at(target, "queens"), "id")
        add("queen id set equal", ref_ids == go_ids,
            f"reference={len(ref_ids)} target={len(go_ids)} missing={sorted(set(ref_ids) - set(go_ids))[:6]}")
    elif endpoint == "queen_tools":
        add("queen_id equal", _values_at(reference, "queen_id") == _values_at(target, "queen_id"),
            f"reference={_values_at(reference, 'queen_id')!r} target={_values_at(target, 'queen_id')!r}")
        add("mcp server set equal", _names(_values_at(reference, "mcp_servers")) == _names(_values_at(target, "mcp_servers")),
            f"reference={_names(_values_at(reference, 'mcp_servers'))} target={_names(_values_at(target, 'mcp_servers'))}")
    elif endpoint == "skills":
        ref_names, go_names = _names(_values_at(reference, "skills")), _names(_values_at(target, "skills"))
        missing = sorted(set(ref_names) - set(go_names))
        extra = sorted(set(go_names) - set(ref_names))
        add("skill name set equal", ref_names == go_names,
            f"reference={len(ref_names)} target={len(go_names)} missing={missing[:6]} extra={extra[:6]}")
    elif endpoint == "config_llm":
        for field_name in ("provider", "model"):
            add(f"{field_name} equal", _values_at(reference, field_name) == _values_at(target, field_name),
                f"reference={_values_at(reference, field_name)!r} target={_values_at(target, field_name)!r}")
        add("has_api_key equal", _values_at(reference, "has_api_key") == _values_at(target, "has_api_key"),
            f"reference={_values_at(reference, 'has_api_key')!r} target={_values_at(target, 'has_api_key')!r}")
    elif endpoint == "models_overview":
        for field_name in ("provider", "model"):
            path = f"slots.llm.{field_name}"
            add(f"slots.llm.{field_name} equal", _values_at(reference, path) == _values_at(target, path),
                f"reference={_values_at(reference, path)!r} target={_values_at(target, path)!r}")
    elif endpoint == "credentials_specs":
        ref_ids = _names(_values_at(reference, "specs"), "credential_id")
        go_ids = _names(_values_at(target, "specs"), "credential_id")
        add("credential_id set equal", ref_ids == go_ids,
            f"reference={len(ref_ids)} target={len(go_ids)} missing={sorted(set(ref_ids) - set(go_ids))[:6]} extra={sorted(set(go_ids) - set(ref_ids))[:6]}")
    elif endpoint == "session_create":
        for field_name in ("queen_id", "queen_name", "queen_phase"):
            add(f"{field_name} equal", _values_at(reference, field_name) == _values_at(target, field_name),
                f"reference={_values_at(reference, field_name)!r} target={_values_at(target, field_name)!r}")
    elif endpoint == "sessions_list":
        ref_list, go_list = _values_at(reference, "sessions") or [], _values_at(target, "sessions") or []
        add("session count equal", len(ref_list) == len(go_list), f"reference={len(ref_list)} target={len(go_list)}")
    elif endpoint == "session_detail":
        add("queen_id equal", _values_at(reference, "queen_id") == _values_at(target, "queen_id"),
            f"reference={_values_at(reference, 'queen_id')!r} target={_values_at(target, 'queen_id')!r}")
    elif endpoint == "session_events":
        add("content type is event-stream",
            "text/event-stream" in str(reference.get("content_type", "")).lower()
            and "text/event-stream" in str(target.get("content_type", "")).lower(),
            f"reference={reference.get('content_type')!r} target={target.get('content_type')!r}")
        ref_types = reference.get("payload_types") or []
        go_types = target.get("payload_types") or []
        add("payload type set equal", set(ref_types) == set(go_types),
            f"reference={sorted(set(ref_types))} target={sorted(set(go_types))}")
        ref_names = [name for name in (reference.get("event_names") or []) if name]
        go_names = [name for name in (target.get("event_names") or []) if name]
        add("SSE event: name presence equal", bool(ref_names) == bool(go_names),
            f"reference={ref_names} target={go_names}")
        ref_data = _data_keys_by_type([frame.get("envelope") for frame in (reference.get("frames") or [])])
        go_data = _data_keys_by_type([frame.get("envelope") for frame in (target.get("frames") or [])])
        common = sorted(set(ref_data) & set(go_data))
        data_diffs = [event_type for event_type in common if ref_data[event_type] != go_data[event_type]]
        add("data keys equal for common frame types", not data_diffs,
            f"differences={data_diffs[:6]}")
    elif endpoint == "session_events_history":
        add("event type sequence equal", _event_types(reference) == _event_types(target),
            f"reference={_event_types(reference)} target={_event_types(target)}")
        add("total equal", _values_at(reference, "total") == _values_at(target, "total"),
            f"reference={_values_at(reference, 'total')!r} target={_values_at(target, 'total')!r}")
        ref_data = _data_keys_by_type(_values_at(reference, "events"))
        go_data = _data_keys_by_type(_values_at(target, "events"))
        common = sorted(set(ref_data) & set(go_data))
        data_diffs = [event_type for event_type in common if ref_data[event_type] != go_data[event_type]]
        add("data keys equal for common event types", not data_diffs,
            f"differences={data_diffs[:6]}")
    elif endpoint == "session_snapshot":
        add("snapshot keys equal", set(reference or {}) == set(target or {}),
            f"reference={sorted(reference or {})} target={sorted(target or {})}")
    elif endpoint == "session_stats":
        add("stats keys equal", set(reference or {}) == set(target or {}),
            f"reference={sorted(reference or {})} target={sorted(target or {})}")
        add("running equal", _values_at(reference, "running") == _values_at(target, "running"),
            f"reference={_values_at(reference, 'running')!r} target={_values_at(target, 'running')!r}")
        add("event_bus.subscriptions is runtime-internal (not compared)",
            True, f"reference={_values_at(reference, 'event_bus.subscriptions')!r} target={_values_at(target, 'event_bus.subscriptions')!r}")

    return checks


# ---------------------------------------------------------------------------
# markdown report
# ---------------------------------------------------------------------------

NON_VERBATIM = [
    {
        "id": "NV-01",
        "topic": "int64/uint64 与 2^53",
        "detail": (
            "proto3 JSON 把 64 位整数编码为字符串；Hive 两端都发 JSON number。"
            "> 2^53 的整数在 JSON number 中不可无损往返，需按 string 传或在契约中声明上限。"
        ),
    },
    {
        "id": "NV-02",
        "topic": "JSON number 整数性",
        "detail": (
            "Python json 区分 3（int）与 3.0（float），Go encoding/json 统一成 float64，"
            "proto3 double 两种字面量都接受。`uptime_seconds` 的 int/float 差异无法逐字，"
            "只能按 double 规范化。"
        ),
    },
    {
        "id": "NV-03",
        "topic": "null vs 缺失",
        "detail": (
            "proto3 JSON 中 null 表示字段未设置；它不能与“字段存在且值为 null”区分。"
            "契约冻结保留 null 与缺失的差异（shape 里 null 与 absent 是两种类型），"
            "但任何 proto 化投影都会丢掉这一层。"
        ),
    },
    {
        "id": "NV-04",
        "topic": "map 顺序",
        "detail": "map<string, T> 与 JSON object 的键序都不属于契约；逐字节比较必须排序后比较。",
    },
    {
        "id": "NV-05",
        "topic": "浮点打印精度",
        "detail": (
            "Python repr 与 Go encoding/json 对 double 的最短表示不同（如 loaded_at 的末位），"
            "值级不能逐字比较，只能按 double 语义比较或屏蔽。"
        ),
    },
    {
        "id": "NV-06",
        "topic": "Timestamp 文本格式",
        "detail": (
            "google.protobuf.Timestamp 的规范 JSON 是带时区的 RFC 3339；Hive 两端都发本地 naive 时间"
            "（YYYY-MM-DDTHH:MM:SS.ffffff）。冻结按不透明 string 处理，转 Timestamp 时必须先约定时区。"
        ),
    },
    {
        "id": "NV-07",
        "topic": "NaN / Infinity",
        "detail": "proto3 JSON 用 \"NaN\"/\"Infinity\" 字符串；Python json 默认发 NaN/Infinity 裸字面量（非标准 JSON）。当前冻结载荷未出现。",
    },
    {
        "id": "NV-08",
        "topic": "bytes/base64",
        "detail": "proto3 bytes 线上是 base64；当前冻结端点不含二进制字段，附件接口未纳入本批冻结。",
    },
]


def render_markdown(diff: dict[str, Any], index: dict[str, Any], route_manifest: dict[str, Any]) -> str:
    totals = diff["totals"]
    lines: list[str] = []
    lines.append("# API 契约差异（reference=python → target=go_hive）")
    lines.append("")
    lines.append(f"- 生成时间：{diff['generated_at']}")
    lines.append(f"- 基准标识：`reference={diff['reference_id']}`，`target={diff['target_id']}`")
    lines.append(f"- reference（契约基准）：`{diff['reference']['implementation']}` {diff['reference']['version']}，")
    lines.append(f"  HEAD `{diff['reference']['commit']}`，`core` 最后改动 `{diff['reference']['last_core_commit']}`，`tools` 最后改动 `{diff['reference']['last_tools_commit']}`")
    lines.append(f"- target（待对齐）：`{diff['target']['implementation']}` {diff['target']['version']}，HEAD `{diff['target']['commit']}`")
    lines.append(f"- 探针输入：queen=`{diff['probe']['queen_id']}`；LLM 槽位指向本地 OpenAI 兼容 stub"
                 f"（`{diff['probe']['llm']['api_base']}`，确定性开场回合，不访问外网），SSE 窗口 {diff['probe']['sse_seconds']}s")
    lines.append("- 产物：`go_hive/contracts/api-responses/`（canonical JSON + shape + diff.json）")
    lines.append("")
    lines.append("> 口径：契约基准是 Python 当前 checkout 的抓取结果，所有字段名/类型/数量以 reference 为准；")
    lines.append("> Go 侧只作为 target 量化差异。易变值（session id、时间戳、端口、临时路径、资源读数）已替换为占位符，")
    lines.append("> 但键的存在性（null vs 缺失）保留。")
    lines.append("")
    lines.append(f"**端点：{totals['pass']}/{totals['endpoints']} PASS，{totals['fail']} FAIL。**")
    lines.append(
        f"**差异计数：missing_in_target={totals['missing_in_target']}，extra_in_target={totals['extra_in_target']}，"
        f"type_mismatch={totals['type_mismatch']}，count_mismatch={totals['count_mismatch']}，"
        f"semantic_failures={totals['semantic_failures']}。**"
    )
    lines.append("")

    if route_manifest:
        summary = route_manifest.get("summary", {})
        lines.append("## 路由面（静态提取）")
        lines.append("")
        lines.append(
            f"- reference={summary.get('reference_routes')} 条，target={summary.get('target_routes')} 条；"
            f"missing_in_target={summary.get('missing_in_target')}（effective={summary.get('missing_in_target_effective')}），"
            f"extra_in_target={summary.get('extra_in_target')}（api={summary.get('extra_in_target_api')}），"
            f"method_mismatch={summary.get('method_mismatch')}。"
        )
        lines.append("- 明细：`go_hive/contracts/api-routes.json`（每条 Go 多余路由带 `annotation` 标注是否为扩展/别名）。")
        lines.append("")

    lines.append("## 端点汇总")
    lines.append("")
    lines.append("| # | 端点 | 方法 | 状态码 (reference/target) | 判定 | 差异数 |")
    lines.append("|---|------|------|---------------------------|------|--------|")
    for idx, entry in enumerate(diff["endpoints"], 1):
        verdict = "**PASS**" if entry["verdict"] == "PASS" else "**FAIL**"
        lines.append(
            f"| {idx} | `{entry['path']}` | {entry['method']} | "
            f"{entry['reference_status']}/{entry['target_status']} | {verdict} | {entry['diff_count']} |"
        )
    lines.append("")

    lines.append("## 逐端点明细")
    lines.append("")
    for idx, entry in enumerate(diff["endpoints"], 1):
        lines.append(f"### {idx}. {entry['method']} `{entry['path']}` — {entry['verdict']}")
        lines.append("")
        lines.append(f"- 状态码：reference `{entry['reference_status']}` / target `{entry['target_status']}`")
        lines.append(f"- Content-Type：reference `{entry['reference_content_type']}` / target `{entry['target_content_type']}`")
        for note in entry.get("notes", []):
            lines.append(f"- 口径备注：{note}")
        rows = (
            entry["missing_in_target"]
            + entry["extra_in_target"]
            + entry["type_mismatch"]
            + entry["count_mismatch"]
        )
        if rows:
            lines.append("")
            lines.append("| 字段 | proto 类型 | reference 实际 | target 实际 | JSON 可规范化 | 说明 |")
            lines.append("|------|-----------|----------------|-------------|----------------|------|")
            for row in rows:
                lines.append(
                    f"| `{row['field']}` | `{row['proto']}` | `{row['reference_type']}` | `{row['target_type']}` | "
                    f"{row['json_normalizable']} | {row['note']} |"
                )
        else:
            lines.append("- 字段形状：一致（按 canonical shape 比较）")
        semantic_failures = [check for check in entry["semantic"] if not check["ok"]]
        if entry["semantic"]:
            lines.append("")
            lines.append("语义检查：")
            for check in entry["semantic"]:
                mark = "✓" if check["ok"] else "✗"
                detail = f" — {check['detail']}" if check["detail"] else ""
                lines.append(f"- {mark} {check['check']}{detail}")
        elif semantic_failures:
            lines.append("")
        lines.append("")

    lines.append("## 不可逐字清单")
    lines.append("")
    lines.append("| 编号 | 主题 | 说明 |")
    lines.append("|------|------|------|")
    for item in NON_VERBATIM:
        lines.append(f"| {item['id']} | {item['topic']} | {item['detail']} |")
    lines.append("")

    lines.append("## 类型映射")
    lines.append("")
    lines.append("Python → proto3 → JSON 线格式规则见 `go_hive/contracts/type-mapping.md`；")
    lines.append("proto 定义见 `go_hive/contracts/api.proto`（规范文件，不引入 protobuf 运行时）。")
    lines.append("")
    lines.append("## 复现")
    lines.append("")
    lines.append("```bash")
    lines.append("uv run python go_hive/scripts/export_api_routes.py")
    lines.append("uv run python go_hive/scripts/freeze_api_responses.py")
    lines.append("uv run python go_hive/scripts/check_api_proto.py")
    lines.append("```")
    lines.append("")
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# entry point
# ---------------------------------------------------------------------------


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Freeze the Hive API response contract")
    parser.add_argument("--workdir", default="", help="scratch directory (default: TMPDIR/hive-contract-<pid>)")
    parser.add_argument("--report", default=str(REPORT_PATH), help="markdown report path")
    parser.add_argument("--no-build", action="store_true", help="fail instead of building the Go binary")
    parser.add_argument("--keep", action="store_true", help="keep the temp HIVE_HOME dirs and logs")
    parser.add_argument("--port-reference", type=int, default=0, help="fixed Python port (default: free)")
    parser.add_argument("--port-target", type=int, default=0, help="fixed Go port (default: free)")
    parser.add_argument("--sse-seconds", type=float, default=3.0, help="SSE observation window")
    parser.add_argument("--fail-on-diff", action="store_true", help="exit 1 when any endpoint differs")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    freezer = Freezer(args)
    return freezer.run()


if __name__ == "__main__":
    raise SystemExit(main())
