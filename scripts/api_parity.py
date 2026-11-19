#!/usr/bin/env python3
"""Go hive vs Python hive — API and chat-flow parity harness.

Starts both servers against throwaway HIVE_HOME directories, probes the same
core endpoints on each, creates a session, sends one chat turn, subscribes to
the session SSE stream for ten seconds, and compares:

  * HTTP status codes
  * JSON key sets and value types, recursively (values are never compared, so
    ids, ports and timestamps are ignored by construction)
  * a small set of endpoint-specific semantic fields
  * the assistant turn persisted under ``conversations/parts/``
  * the SSE event-type sequence published while the turn runs

It writes ``go_hive/API_PARITY_REPORT.md`` and exits 0 when every probe passes,
1 otherwise.  Standard library only (urllib), no third-party imports.

The restart scenario really creates sessions (that is the behaviour under
test), and on the Python side every creation starts a queen turn, so a run
costs LLM calls; ``--stale-post-cap`` bounds them and ``--skip-restart`` skips
the scenario entirely.  A full run takes roughly two to six minutes.

Usage:
    uv run python go_hive/scripts/api_parity.py
    uv run python go_hive/scripts/api_parity.py --skip-flow
    uv run python go_hive/scripts/api_parity.py --keep --sse-seconds 15
    uv run python go_hive/scripts/api_parity.py --stale-post-cap 10
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
import statistics
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_GO_DIR = REPO_ROOT / "go_hive" / "go-hive"
DEFAULT_GO_BINARY = DEFAULT_GO_DIR / "dist" / "hive-embed"
DEFAULT_REPORT = REPO_ROOT / "go_hive" / "API_PARITY_REPORT.md"
DEFAULT_ENV_FILE = Path.home() / ".hive-go" / "env"

LLM_PROVIDER = "openai"
LLM_MODEL = "deepseek-flash-1017"
LLM_API_BASE = "https://token.tokenbroad.com/v1"
LLM_KEY_ENV = "BROADTOKEN_API_KEY"

# Values that legitimately differ between two independent processes.  They are
# listed for documentation only: the comparison works on key names and types,
# so these values never enter the diff.
DYNAMIC_VALUE_KEYS = {
    "session_id",
    "colony_id",
    "spawned_colony_id",
    "id",
    "name",
    "loaded_at",
    "uptime_seconds",
    "seq",
    "timestamp",
    "last_event_at",
    "last_queen_event_at",
    "last_health_check_at",
    "snapshot_seq",
    "last_event_seq",
    "event_seq",
    "port",
    "pid",
    "path",
    "agent_path",
}

CHAT_PROMPT = "Reply with exactly: PARITY_OK"

MAX_HEALTH_WAIT_S = 180.0
CHAT_REPLY_TIMEOUT_S = 180.0


# ---------------------------------------------------------------------------
# small utilities
# ---------------------------------------------------------------------------


def now_iso() -> str:
    return datetime.now().astimezone().isoformat(timespec="seconds")


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def load_env_file(path: Path) -> dict[str, str]:
    """Read KEY=VALUE / export KEY=VALUE lines from a shell-style env file."""
    values: dict[str, str] = {}
    if not path.is_file():
        return values
    for raw in path.read_text(encoding="utf-8", errors="replace").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[len("export ") :].strip()
        if "=" not in line:
            continue
        key, _, value = line.partition("=")
        key = key.strip()
        value = value.strip().strip("'").strip('"')
        if key:
            values[key] = value
    return values


def log_tail(path: Path, lines: int = 25) -> str:
    if not path.is_file():
        return f"<no log at {path}>"
    try:
        content = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError as exc:
        return f"<cannot read log: {exc}>"
    return "\n".join(content[-lines:])


@dataclass
class HttpResult:
    status: int
    payload: object
    raw: str
    error: str = ""

    @property
    def json_ok(self) -> bool:
        return self.error == "" and self.payload is not None


def http_request(
    method: str,
    url: str,
    body: object | None = None,
    timeout: float = 60.0,
) -> HttpResult:
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
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", errors="replace")
        status = exc.code
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        return HttpResult(status=0, payload=None, raw="", error=str(exc))

    try:
        payload = json.loads(raw) if raw.strip() else None
    except json.JSONDecodeError:
        return HttpResult(status=status, payload=None, raw=raw, error="non-JSON body")
    return HttpResult(status=status, payload=payload, raw=raw)


def typename(value: object) -> str:
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "bool"
    if isinstance(value, (int, float)):
        return "number"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    return type(value).__name__


def shape_of(value: object, prefix: str = "") -> dict[str, str]:
    """Map ``dotted.path -> type``.  Arrays contribute their element shape."""
    shape: dict[str, str] = {}
    if isinstance(value, dict):
        for key in sorted(value):
            child = f"{prefix}.{key}" if prefix else key
            shape[child] = typename(value[key])
            if isinstance(value[key], (dict, list)):
                shape.update(shape_of(value[key], child))
    elif isinstance(value, list):
        # Union the first few elements so heterogeneous lists (queens, specs)
        # still yield a stable comparison surface.
        for item in value[:3]:
            if isinstance(item, (dict, list)):
                shape.update(shape_of(item, f"{prefix}[]"))
            else:
                shape[f"{prefix}[]"] = typename(item)
                break
    return shape


@dataclass
class ShapeDiff:
    missing_in_go: list[str] = field(default_factory=list)
    extra_in_go: list[str] = field(default_factory=list)
    type_mismatch: list[str] = field(default_factory=list)

    def __bool__(self) -> bool:
        return bool(self.missing_in_go or self.extra_in_go or self.type_mismatch)

    @property
    def count(self) -> int:
        return len(self.missing_in_go) + len(self.extra_in_go) + len(self.type_mismatch)


def array_at(payload: object, dotted: str) -> list | None:
    """Resolve a dotted path whose segments may end in ``[]``.

    ``specs[].tools`` means: value["specs"][0]["tools"].  Returns the list at
    that path, or None when the path cannot be resolved.
    """
    current: object = payload
    for raw in dotted.split("."):
        is_array = raw.endswith("[]")
        name = raw[:-2] if is_array else raw
        if isinstance(current, dict):
            current = current.get(name)
        else:
            return None
        if is_array:
            if not isinstance(current, list):
                return None
            if not current:
                return []
            current = current[0]
    return current if isinstance(current, list) else None


def array_container(path: str) -> str:
    """Array path behind a shape key.

    ``events[].data`` -> ``events``; ``specs[].tools[]`` -> ``specs[].tools``.
    Empty string when the key does not describe an array element.
    """
    index = path.rfind("[]")
    return path[:index] if index >= 0 else ""


def compare_shapes(py: object, go: object, ignore_prefixes: set[str] | None = None) -> ShapeDiff:
    ignored = ignore_prefixes or set()
    py_shape = shape_of(py)
    go_shape = shape_of(go)
    if ignored:
        py_shape = {k: v for k, v in py_shape.items() if not any(k.startswith(p) for p in ignored)}
        go_shape = {k: v for k, v in go_shape.items() if not any(k.startswith(p) for p in ignored)}
    diff = ShapeDiff()
    for key in sorted(set(py_shape) - set(go_shape)):
        # An empty array carries no element shape; that is missing data, not a
        # contract difference (e.g. Go returns events: [] after a turn).
        container = array_container(key)
        if container and array_at(go, container) == []:
            continue
        diff.missing_in_go.append(f"{key} ({py_shape[key]})")
    for key in sorted(set(go_shape) - set(py_shape)):
        container = array_container(key)
        if container and array_at(py, container) == []:
            continue
        diff.extra_in_go.append(f"{key} ({go_shape[key]})")
    for key in sorted(set(py_shape) & set(go_shape)):
        if py_shape[key] != go_shape[key]:
            diff.type_mismatch.append(f"{key}: python={py_shape[key]} go={go_shape[key]}")
    return diff


def format_key_list(keys: list[str], limit: int = 12) -> str:
    shown = ", ".join(f"`{k}`" for k in keys[:limit])
    if len(keys) > limit:
        shown += f" … 共 {len(keys)} 个"
    return shown


def values_at(payload: object, dotted: str) -> object:
    current = payload
    for part in dotted.split("."):
        if isinstance(current, dict):
            current = current.get(part)
        else:
            return None
    return current


# ---------------------------------------------------------------------------
# SSE collection
# ---------------------------------------------------------------------------


@dataclass
class SseFrame:
    event: str | None
    payload_type: str | None
    seq: int | None


class SseCollector:
    """Consume an SSE endpoint in a thread until stopped."""

    def __init__(self, url: str, seconds: float) -> None:
        self.url = url
        self.seconds = seconds
        self.frames: list[SseFrame] = []
        self.error = ""
        self.started_at = 0.0
        self.ended_at = 0.0
        self._stop = threading.Event()
        self._response = None
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self) -> "SseCollector":
        self.started_at = time.time()
        self._thread.start()
        return self

    def stop(self) -> None:
        self._stop.set()
        response = self._response
        if response is not None:
            try:
                response.close()
            except Exception:
                pass
        self._thread.join(timeout=5)
        self.ended_at = time.time()

    def _run(self) -> None:
        request = urllib.request.Request(self.url, headers={"Accept": "text/event-stream"})
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                self._response = response
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
                        continue
                    if line.startswith("event:"):
                        event_name = line[len("event:") :].strip()
                    elif line.startswith("data:"):
                        data_lines.append(line[len("data:") :].lstrip())
        except Exception as exc:  # noqa: BLE001 - report, do not abort the run
            if not self._stop.is_set():
                self.error = str(exc)

    def _record(self, event_name: str | None, data: str) -> None:
        payload_type = None
        seq = None
        try:
            decoded = json.loads(data)
            if isinstance(decoded, dict):
                payload_type = decoded.get("type")
                raw_seq = decoded.get("seq")
                seq = raw_seq if isinstance(raw_seq, int) else None
        except json.JSONDecodeError:
            pass
        self.frames.append(SseFrame(event=event_name, payload_type=payload_type, seq=seq))

    @property
    def payload_types(self) -> list[str]:
        return [f.payload_type for f in self.frames if f.payload_type]

    @property
    def event_names(self) -> list[str]:
        return [f.event for f in self.frames if f.event]


# ---------------------------------------------------------------------------
# process management
# ---------------------------------------------------------------------------


@dataclass
class Side:
    key: str  # "python" | "go"
    label: str
    port: int
    home: Path
    log_path: Path
    version: str = ""
    start_seconds: float = 0.0
    started_at: float = 0.0
    restart_seconds: float = 0.0
    process: subprocess.Popen | None = None
    log_handle: Any = None
    argv: list[str] = field(default_factory=list)
    cwd: Path | None = None
    env: dict[str, str] = field(default_factory=dict)

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.port}"


class ParityError(RuntimeError):
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
        raise ParityError(f"go build failed:\n{proc.stdout}\n{proc.stderr}")


def make_home(root: Path, name: str, llm_env_var: str) -> tuple[Path, str]:
    home = root / name
    (home / "secrets").mkdir(parents=True, exist_ok=True)
    # Fernet key: 32 random bytes, urlsafe-base64 encoded (what
    # framework.credentials.key_storage.generate_and_save_credential_key writes).
    credential_key = base64.urlsafe_b64encode(secrets.token_bytes(32)).decode("ascii")
    (home / "secrets" / "credential_key").write_text(credential_key, encoding="utf-8")
    os.chmod(home / "secrets" / "credential_key", 0o600)
    config = {
        "llm": {
            "provider": LLM_PROVIDER,
            "model": LLM_MODEL,
            "api_base": LLM_API_BASE,
            "api_key_env_var": llm_env_var,
        }
    }
    (home / "configuration.json").write_text(
        json.dumps(config, indent=2) + "\n", encoding="utf-8"
    )
    return home, credential_key


def spawn(side: Side) -> None:
    side.log_path.parent.mkdir(parents=True, exist_ok=True)
    log_handle = open(side.log_path, "w", encoding="utf-8")  # noqa: SIM115 - owned by the child
    side.process = subprocess.Popen(
        side.argv,
        cwd=str(side.cwd),
        env=side.env,
        stdout=log_handle,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    side.log_handle = log_handle


def restart_side(side: Side, timeout: float = MAX_HEALTH_WAIT_S) -> None:
    """Stop the server and start it again on the same port and HIVE_HOME."""
    terminate(side)
    initial_start_seconds = side.start_seconds
    side.log_path = side.log_path.with_name(f"{side.log_path.stem}-restart.log")
    side.started_at = time.time()
    spawn(side)
    wait_health(side, timeout)
    side.restart_seconds = round(time.time() - side.started_at, 2)
    # wait_health rewrites start_seconds; keep the cold-boot number for the header.
    side.start_seconds = initial_start_seconds


def wait_health(side: Side, timeout: float = MAX_HEALTH_WAIT_S) -> dict:
    deadline = time.time() + timeout
    last_error = ""
    while time.time() < deadline:
        if side.process is not None and side.process.poll() is not None:
            raise ParityError(
                f"{side.label} exited with code {side.process.returncode} before /api/health.\n"
                f"log tail:\n{log_tail(side.log_path)}"
            )
        result = http_request("GET", f"{side.base_url}/api/health", timeout=5)
        if result.status == 200 and isinstance(result.payload, dict):
            side.start_seconds = round(time.time() - side.started_at, 2)
            return result.payload
        last_error = result.error or f"HTTP {result.status}"
        time.sleep(0.25)
    raise ParityError(
        f"{side.label} did not become healthy within {timeout:.0f}s (last: {last_error}).\n"
        f"log tail:\n{log_tail(side.log_path)}"
    )


def terminate(side: Side) -> None:
    proc = side.process
    if proc is None or proc.poll() is not None:
        return
    try:
        proc.terminate()
    except OSError:
        return
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass
    if side.log_handle is not None:
        try:
            side.log_handle.close()
        except Exception:
            pass


# ---------------------------------------------------------------------------
# endpoint evaluation
# ---------------------------------------------------------------------------


@dataclass
class SemanticCheck:
    label: str
    ok: bool
    detail: str = ""
    severity: int = 70


@dataclass
class EndpointResult:
    name: str
    method: str
    path: str
    py_status: int
    go_status: int
    status_note: str = ""
    shape_diff: ShapeDiff = field(default_factory=ShapeDiff)
    semantic: list[SemanticCheck] = field(default_factory=list)
    notes: list[str] = field(default_factory=list)
    py_preview: str = ""
    go_preview: str = ""
    requires_json: bool = True

    @property
    def ok(self) -> bool:
        # Parity is equality, not success: two 404s are a match.
        if self.py_status != self.go_status:
            return False
        if self.shape_diff:
            return False
        return all(check.ok for check in self.semantic)

    @property
    def diff_count(self) -> int:
        status_diff = 0 if self.py_status == self.go_status else 1
        return status_diff + self.shape_diff.count + sum(1 for c in self.semantic if not c.ok)


def summarize_payload(payload: object, limit: int = 220) -> str:
    if payload is None:
        return "<non-JSON or empty>"
    text = json.dumps(payload, ensure_ascii=False, sort_keys=True)
    return text if len(text) <= limit else text[:limit] + "…"


def evaluate(
    name: str,
    method: str,
    path: str,
    py: HttpResult,
    go: HttpResult,
    semantic: list[SemanticCheck] | None = None,
    requires_json: bool = True,
    notes: list[str] | None = None,
    ignore_prefixes: set[str] | None = None,
) -> EndpointResult:
    result = EndpointResult(
        name=name,
        method=method,
        path=path,
        py_status=py.status,
        go_status=go.status,
        semantic=semantic or [],
        notes=notes or [],
        py_preview=summarize_payload(py.payload),
        go_preview=summarize_payload(go.payload),
        requires_json=requires_json,
    )
    if requires_json and py.json_ok and go.json_ok:
        result.shape_diff = compare_shapes(py.payload, go.payload, ignore_prefixes)
    elif requires_json:
        if not py.json_ok:
            result.notes.append(f"python body: {py.error or 'unparsable'} — {py.raw[:120]!r}")
        if not go.json_ok:
            result.notes.append(f"go body: {go.error or 'unparsable'} — {go.raw[:120]!r}")
    return result


def _ids(items: object, key: str) -> list[str]:
    if not isinstance(items, list):
        return []
    out = []
    for item in items:
        if isinstance(item, dict) and isinstance(item.get(key), str):
            out.append(item[key])
    return out


def _len(payload: object, dotted: str) -> int | None:
    value = values_at(payload, dotted)
    return len(value) if isinstance(value, list) else None


# ---------------------------------------------------------------------------
# parts on disk
# ---------------------------------------------------------------------------


def find_parts(home: Path, session_id: str) -> list[dict]:
    candidates = sorted(home.glob(f"**/sessions/{session_id}/conversations/parts/*.json"))
    if not candidates:
        candidates = sorted(home.glob(f"**/{session_id}/**/conversations/parts/*.json"))
    parts = []
    for path in candidates:
        try:
            decoded = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue
        if isinstance(decoded, dict):
            decoded["_file"] = path.name
            parts.append(decoded)
    parts.sort(key=lambda p: (p.get("seq") if isinstance(p.get("seq"), int) else 0, p["_file"]))
    return parts


def wait_for_reply(
    home: Path,
    session_id: str,
    token: str,
    deadline: float,
) -> list[dict]:
    """Poll the parts directory until an assistant part carries ``token``.

    Python writes a kickoff greeting part right after the session is created,
    so "an assistant part appeared" is not enough to identify the answer to
    this turn; the token pins it to the reply.
    """
    needle = token.lower()
    while time.time() < deadline:
        parts = find_parts(home, session_id)
        for part in parts:
            if part.get("role") != "assistant":
                continue
            if needle in str(part.get("content", "")).lower():
                return parts
        time.sleep(0.4)
    return find_parts(home, session_id)


def pick_reply(parts: list[dict], token: str) -> str:
    needle = token.lower()
    for part in reversed(parts):
        if part.get("role") == "assistant" and needle in str(part.get("content", "")).lower():
            return str(part.get("content", ""))
    assistant = [p for p in parts if p.get("role") == "assistant"]
    return str(assistant[-1].get("content", "")) if assistant else ""


# ---------------------------------------------------------------------------
# main harness
# ---------------------------------------------------------------------------


class Harness:
    def __init__(self, args: argparse.Namespace) -> None:
        self.args = args
        self.root = Path(args.workdir).expanduser() if args.workdir else Path(
            os.environ.get("TMPDIR", "/tmp")
        ) / f"hive-parity-{os.getpid()}"
        self.results: list[EndpointResult] = []
        self.flow: dict[str, Any] = {}
        self.findings: list[tuple[int, str, str]] = []
        self.notes: list[str] = []
        self.py: Side | None = None
        self.go: Side | None = None
        self.env_file = Path(args.env_file).expanduser()

    # -- setup -------------------------------------------------------------

    def prepare(self) -> None:
        if not (REPO_ROOT / "core" / "framework" / "cli.py").is_file():
            raise ParityError(f"cannot find the Python hive checkout under {REPO_ROOT}")
        go_dir = Path(self.args.go_dir).expanduser()
        if not (go_dir / "cmd" / "hive").is_dir():
            raise ParityError(f"cannot find the Go hive checkout under {go_dir}")
        binary = Path(self.args.go_binary).expanduser()
        if not self.args.no_build:
            build_go_binary(go_dir, binary)
        if not binary.is_file():
            raise ParityError(f"Go binary missing: {binary} (run without --no-build)")

        file_env = load_env_file(self.env_file)
        if LLM_KEY_ENV not in os.environ and LLM_KEY_ENV in file_env:
            os.environ[LLM_KEY_ENV] = file_env[LLM_KEY_ENV]
        if not os.environ.get(LLM_KEY_ENV):
            raise ParityError(
                f"{LLM_KEY_ENV} is not set and not present in {self.env_file}. "
                "The chat-flow half of this harness needs a real key; use --skip-flow for endpoints only."
            )

        self.root.mkdir(parents=True, exist_ok=True)
        py_home, py_key = make_home(self.root, "python-home", LLM_KEY_ENV)
        go_home, go_key = make_home(self.root, "go-home", LLM_KEY_ENV)

        py_port = self.args.port_python or free_port()
        go_port = self.args.port_go or free_port()
        while go_port == py_port:
            go_port = free_port()

        self.py = Side("python", "Python hive", py_port, py_home, self.root / "logs" / "python.log")
        self.go = Side("go", "Go hive", go_port, go_home, self.root / "logs" / "go.log")

        base_env = {
            k: v
            for k, v in os.environ.items()
            if k
            not in {
                "HIVE_HOME",
                "HIVE_CREDENTIAL_KEY",
                "HIVE_RUNTIME_PORT",
                "HIVE_DESKTOP_TOKEN",
            }
        }
        base_env["PYTHONUNBUFFERED"] = "1"

        py_env = dict(base_env)
        py_env["HIVE_HOME"] = str(py_home)
        py_env["HIVE_CREDENTIAL_KEY"] = py_key

        go_env = dict(base_env)
        go_env["HIVE_HOME"] = str(go_home)
        go_env["HIVE_CREDENTIAL_KEY"] = go_key

        uv = shutil.which("uv")
        if uv is None:
            raise ParityError("uv is not on PATH; the Python side is started with `uv run --offline hive`")

        py_argv = [uv, "run", "--offline", "hive", "serve", "--host", "127.0.0.1", "--port", str(py_port)]
        go_argv = [str(binary), "serve", "--host", "127.0.0.1", "--port", str(go_port)]

        print(f"[parity] python home: {py_home}")
        print(f"[parity] go home:     {go_home}")
        print(f"[parity] ports: python={py_port} go={go_port}")

        self.py.argv, self.py.cwd, self.py.env = py_argv, REPO_ROOT, py_env
        self.go.argv, self.go.cwd, self.go.env = go_argv, go_dir, go_env
        self.py.started_at = time.time()
        spawn(self.py)
        self.go.started_at = time.time()
        spawn(self.go)

        self.wait_both_healthy()

        self.py.version = self.read_version([uv, "run", "--offline", "hive", "--version"], REPO_ROOT, py_env)
        self.go.version = self.read_version([str(binary), "--version"], go_dir, go_env)

    def wait_both_healthy(self, timeout: float = MAX_HEALTH_WAIT_S) -> None:
        """Poll both servers in one loop so each records its own boot latency."""
        sides = [side for side in (self.py, self.go) if side is not None]
        pending = {side.key: side for side in sides}
        deadline = time.time() + timeout
        last_error: dict[str, str] = {}
        while pending and time.time() < deadline:
            for key, side in list(pending.items()):
                if side.process is not None and side.process.poll() is not None:
                    raise ParityError(
                        f"{side.label} exited with code {side.process.returncode} before /api/health.\n"
                        f"log tail:\n{log_tail(side.log_path)}"
                    )
                result = http_request("GET", f"{side.base_url}/api/health", timeout=5)
                if result.status == 200 and isinstance(result.payload, dict):
                    side.start_seconds = round(time.time() - side.started_at, 2)
                    pending.pop(key)
                else:
                    last_error[key] = result.error or f"HTTP {result.status}"
            if pending:
                time.sleep(0.25)
        if pending:
            details = ", ".join(f"{side.label}: {last_error.get(side.key, 'timeout')}" for side in pending.values())
            raise ParityError(f"servers did not become healthy within {timeout:.0f}s ({details})")

    def read_version(self, argv: list[str], cwd: Path, env: dict[str, str]) -> str:
        try:
            proc = subprocess.run(
                argv, cwd=str(cwd), env=env, capture_output=True, text=True, timeout=120
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            return f"<version failed: {exc}>"
        text = (proc.stdout + proc.stderr).strip().splitlines()
        # --version also drags LiteLLM warnings onto stderr; pick the real line.
        for line in text:
            stripped = line.strip()
            if re.match(r"^hive\s+\d+\.\d+", stripped):
                return stripped
        return text[-1] if text else f"<empty, rc={proc.returncode}>"

    # -- probing -----------------------------------------------------------

    def get(self, side: Side, path: str, timeout: float = 60.0) -> HttpResult:
        return http_request("GET", side.base_url + path, timeout=timeout)

    def probe_static(self) -> None:
        assert self.py and self.go
        py, go = self.py, self.go

        # 1. /api/health
        p, g = self.get(py, "/api/health"), self.get(go, "/api/health")
        self.results.append(
            evaluate(
                "健康检查", "GET", "/api/health", p, g,
                [
                    SemanticCheck("status == 'ok'", values_at(p.payload, "status") == "ok" and values_at(g.payload, "status") == "ok",
                                  f"python={values_at(p.payload, 'status')!r} go={values_at(g.payload, 'status')!r}"),
                ],
                # resources.avail_mb / renderers appear only under memory pressure;
                # they are environment readings, not an API contract.
                ignore_prefixes={"resources."},
            )
        )

        # 2. /api/config/llm
        p, g = self.get(py, "/api/config/llm"), self.get(go, "/api/config/llm")
        checks = []
        for field_name in ("provider", "model"):
            pv, gv = values_at(p.payload, field_name), values_at(g.payload, field_name)
            checks.append(SemanticCheck(f"{field_name} 相同", pv == gv, f"python={pv!r} go={gv!r}"))
        checks.append(
            SemanticCheck(
                "has_api_key 两边都为 true",
                values_at(p.payload, "has_api_key") is True and values_at(g.payload, "has_api_key") is True,
                f"python={values_at(p.payload, 'has_api_key')!r} go={values_at(g.payload, 'has_api_key')!r}",
            )
        )
        self.results.append(evaluate("LLM 配置", "GET", "/api/config/llm", p, g, checks))

        # 3. /api/credentials
        p, g = self.get(py, "/api/credentials"), self.get(go, "/api/credentials")
        self.results.append(
            evaluate(
                "凭据列表", "GET", "/api/credentials", p, g,
                [
                    SemanticCheck("credentials 是数组", isinstance(values_at(p.payload, "credentials"), list) and isinstance(values_at(g.payload, "credentials"), list)),
                    SemanticCheck("credential 数量相同", _len(p.payload, "credentials") == _len(g.payload, "credentials"),
                                  f"python={_len(p.payload, 'credentials')} go={_len(g.payload, 'credentials')}"),
                ],
            )
        )

        # 4. /api/credentials/specs
        p, g = self.get(py, "/api/credentials/specs"), self.get(go, "/api/credentials/specs")
        p_ids, g_ids = _ids(values_at(p.payload, "specs"), "credential_id"), _ids(values_at(g.payload, "specs"), "credential_id")
        missing = sorted(set(p_ids) - set(g_ids))
        checks = [
            SemanticCheck(
                "specs 数量相同",
                len(p_ids) == len(g_ids),
                f"python={len(p_ids)} go={len(g_ids)}",
            ),
            SemanticCheck(
                "credential_id 集合相同",
                set(p_ids) == set(g_ids),
                f"python 独有 {len(missing)} 个" + (f"：{', '.join(missing[:8])}" if missing else ""),
            ),
        ]
        self.results.append(
            evaluate("凭据规格目录", "GET", "/api/credentials/specs", p, g, checks)
        )

        # 5. /api/queen/profiles
        p, g = self.get(py, "/api/queen/profiles"), self.get(go, "/api/queen/profiles")
        p_q, g_q = _ids(values_at(p.payload, "queens"), "id"), _ids(values_at(g.payload, "queens"), "id")
        self.results.append(
            evaluate(
                "Queen 档案", "GET", "/api/queen/profiles", p, g,
                [
                    SemanticCheck("queen 数量相同", len(p_q) == len(g_q), f"python={len(p_q)} go={len(g_q)}"),
                    SemanticCheck("queen id 集合相同", set(p_q) == set(g_q),
                                  "python 独有 " + ", ".join(sorted(set(p_q) - set(g_q))[:6]) if set(p_q) != set(g_q) else ""),
                ],
            )
        )

        # 6. /api/models  (documented as absent on both sides today)
        p, g = self.get(py, "/api/models"), self.get(go, "/api/models")
        self.results.append(
            evaluate(
                "模型列表（占位）", "GET", "/api/models", p, g,
                [SemanticCheck("两边都未实现 /api/models", p.status == g.status, f"python={p.status} go={g.status}")],
                requires_json=False,
                notes=["两边都没有 /api/models，真实端点名为 /api/models/overview"],
            )
        )

        # 7. /api/models/overview
        p, g = self.get(py, "/api/models/overview"), self.get(go, "/api/models/overview")
        checks = [
            SemanticCheck(
                "llm 槽位 provider/model 相同",
                values_at(p.payload, "slots.llm.provider") == values_at(g.payload, "slots.llm.provider")
                and values_at(p.payload, "slots.llm.model") == values_at(g.payload, "slots.llm.model"),
                f"python={values_at(p.payload, 'slots.llm')!r} go={values_at(g.payload, 'slots.llm')!r}",
            ),
        ]
        self.results.append(evaluate("模型总览", "GET", "/api/models/overview", p, g, checks))

        # 8. /api/org-chart (documented as absent on both sides today)
        p, g = self.get(py, "/api/org-chart"), self.get(go, "/api/org-chart")
        self.results.append(
            evaluate(
                "组织架构图（占位）", "GET", "/api/org-chart", p, g,
                [SemanticCheck("两边都未实现 /api/org-chart", p.status == g.status, f"python={p.status} go={g.status}")],
                requires_json=False,
            )
        )

        # 9. /api/skills
        p, g = self.get(py, "/api/skills"), self.get(go, "/api/skills")
        p_skills, g_skills = _ids(values_at(p.payload, "skills"), "name"), _ids(values_at(g.payload, "skills"), "name")
        self.results.append(
            evaluate(
                "技能目录", "GET", "/api/skills", p, g,
                [
                    SemanticCheck("skill 数量相同", len(p_skills) == len(g_skills), f"python={len(p_skills)} go={len(g_skills)}"),
                ],
            )
        )

        # 10. /api/mcp/servers
        p, g = self.get(py, "/api/mcp/servers"), self.get(go, "/api/mcp/servers")
        p_srv, g_srv = _ids(values_at(p.payload, "servers"), "name"), _ids(values_at(g.payload, "servers"), "name")
        self.results.append(
            evaluate(
                "MCP 服务器", "GET", "/api/mcp/servers", p, g,
                [
                    SemanticCheck("server 名称集合相同", set(p_srv) == set(g_srv),
                                  f"python={len(p_srv)} go={len(g_srv)}"),
                ],
            )
        )

    def create_sessions(self) -> None:
        assert self.py and self.go
        p = http_request("POST", self.py.base_url + "/api/sessions", body={}, timeout=180)
        g = http_request("POST", self.go.base_url + "/api/sessions", body={}, timeout=180)
        self.flow["create"] = {"py": p, "go": g}
        py_sid = p.payload.get("session_id") if isinstance(p.payload, dict) else None
        go_sid = g.payload.get("session_id") if isinstance(g.payload, dict) else None
        self.flow["py_sid"] = py_sid
        self.flow["go_sid"] = go_sid

        checks = [
            SemanticCheck("两边都创建成功 (201)", p.status == 201 and g.status == 201, f"python={p.status} go={g.status}"),
            SemanticCheck("响应都带 session_id", isinstance(py_sid, str) and isinstance(go_sid, str),
                          f"python={py_sid!r} go={go_sid!r}"),
        ]
        if isinstance(p.payload, dict) and isinstance(g.payload, dict):
            checks.append(
                SemanticCheck(
                    "queen 身份已绑定 (queen_id 非空)",
                    p.payload.get("queen_id") is not None and g.payload.get("queen_id") is not None,
                    f"python={p.payload.get('queen_id')!r} go={g.payload.get('queen_id')!r}",
                )
            )
            checks.append(
                SemanticCheck(
                    "queen_phase 相同",
                    p.payload.get("queen_phase") == g.payload.get("queen_phase"),
                    f"python={p.payload.get('queen_phase')!r} go={g.payload.get('queen_phase')!r}",
                )
            )
        self.results.append(evaluate("创建会话", "POST", "/api/sessions", p, g, checks))
        if not (isinstance(py_sid, str) and isinstance(go_sid, str)):
            raise ParityError("session creation did not return a session_id on both sides")

    def run_flow(self) -> None:
        assert self.py and self.go
        py_sid = str(self.flow["py_sid"])
        go_sid = str(self.flow["go_sid"])

        py_sse = SseCollector(
            f"{self.py.base_url}/api/sessions/{py_sid}/events", self.args.sse_seconds
        ).start()
        go_sse = SseCollector(
            f"{self.go.base_url}/api/sessions/{go_sid}/events", self.args.sse_seconds
        ).start()
        time.sleep(1.0)

        chat_started = time.time()
        py_chat = http_request(
            "POST", f"{self.py.base_url}/api/sessions/{py_sid}/chat", body={"message": CHAT_PROMPT}, timeout=120
        )
        go_chat = http_request(
            "POST", f"{self.go.base_url}/api/sessions/{go_sid}/chat", body={"message": CHAT_PROMPT}, timeout=120
        )

        deadline = chat_started + self.args.reply_timeout
        wait_for_reply(self.py.home, py_sid, "PARITY_OK", deadline)
        wait_for_reply(self.go.home, go_sid, "PARITY_OK", deadline)

        # Let the SSE window finish, then close both streams.
        remaining = self.args.sse_seconds - (time.time() - py_sse.started_at)
        if remaining > 0:
            time.sleep(remaining)
        py_sse.stop()
        go_sse.stop()

        # Re-read once the window closed: the turn may have finished while the
        # stream was still open.
        py_parts = find_parts(self.py.home, py_sid)
        go_parts = find_parts(self.go.home, go_sid)
        py_text = pick_reply(py_parts, "PARITY_OK")
        go_text = pick_reply(go_parts, "PARITY_OK")

        self.flow.update(
            {
                "chat": {"py": py_chat, "go": go_chat},
                "sse": {"py": py_sse, "go": go_sse},
                "parts": {"py": py_parts, "go": go_parts},
                "assistant": {"py": py_text, "go": go_text},
                "timing": {"chat_seconds": round(time.time() - chat_started, 1)},
            }
        )

        checks = [
            SemanticCheck("chat 都返回 200", py_chat.status == 200 and go_chat.status == 200,
                          f"python={py_chat.status} go={go_chat.status}"),
            SemanticCheck("两边都落盘了 assistant part", bool(py_text.strip()) and bool(go_text.strip()),
                          f"python={bool(py_text.strip())} go={bool(go_text.strip())}"),
            SemanticCheck("assistant 回复包含 PARITY_OK", "PARITY_OK" in py_text.upper() and "PARITY_OK" in go_text.upper(),
                          f"python={py_text[:60]!r} go={go_text[:60]!r}"),
            SemanticCheck(
                "SSE 事件类型集合相同",
                set(py_sse.payload_types) == set(go_sse.payload_types),
                f"python={sorted(set(py_sse.payload_types))} go={sorted(set(go_sse.payload_types))}",
            ),
            SemanticCheck(
                "SSE 帧都带 event: 名称（或都不带）",
                (len(py_sse.event_names) > 0) == (len(go_sse.event_names) > 0),
                f"python event 名称 {len(py_sse.event_names)} 个，go {len(go_sse.event_names)} 个",
            ),
        ]
        self.flow["checks"] = checks

    def probe_session_surface(self) -> None:
        assert self.py and self.go
        py, go = self.py, self.go
        py_sid = str(self.flow["py_sid"])
        go_sid = str(self.flow["go_sid"])

        def pair(path_tpl: str) -> tuple[HttpResult, HttpResult, str]:
            pp = path_tpl.format(session=py_sid)
            gp = path_tpl.format(session=go_sid)
            return self.get(py, pp), self.get(go, gp), path_tpl

        # list
        p, g, _ = pair("/api/sessions")
        checks = [
            SemanticCheck(
                "两边列表都包含刚建的会话",
                any(s.get("session_id") == py_sid for s in values_at(p.payload, "sessions") or [] if isinstance(s, dict))
                and any(s.get("session_id") == go_sid for s in values_at(g.payload, "sessions") or [] if isinstance(s, dict)),
                f"python={_len(p.payload, 'sessions')} go={_len(g.payload, 'sessions')}",
            )
        ]
        self.results.append(evaluate("会话列表", "GET", "/api/sessions", p, g, checks))

        # detail
        p, g, _ = pair("/api/sessions/{session}")
        checks = [
            SemanticCheck("session_id 回显正确",
                          values_at(p.payload, "session_id") == py_sid and values_at(g.payload, "session_id") == go_sid,
                          f"python={values_at(p.payload, 'session_id')!r} go={values_at(g.payload, 'session_id')!r}"),
            SemanticCheck("queen_phase 相同",
                          values_at(p.payload, "queen_phase") == values_at(g.payload, "queen_phase"),
                          f"python={values_at(p.payload, 'queen_phase')!r} go={values_at(g.payload, 'queen_phase')!r}"),
            SemanticCheck("queen_id 相同",
                          values_at(p.payload, "queen_id") == values_at(g.payload, "queen_id"),
                          f"python={values_at(p.payload, 'queen_id')!r} go={values_at(g.payload, 'queen_id')!r}"),
        ]
        self.results.append(evaluate("会话详情", "GET", "/api/sessions/{id}", p, g, checks))

        # events/history — what the UI replays after a page reload
        p, g, _ = pair("/api/sessions/{session}/events/history")
        py_total = values_at(p.payload, "total")
        go_total = values_at(g.payload, "total")
        checks = [
            SemanticCheck(
                "历史事件非空（turn 之后）",
                isinstance(py_total, int) and py_total > 0 and isinstance(go_total, int) and go_total > 0,
                f"python total={py_total} go total={go_total}",
                severity=75,
            ),
            SemanticCheck(
                "events 都是数组",
                isinstance(values_at(p.payload, "events"), list) and isinstance(values_at(g.payload, "events"), list),
            ),
        ]
        self.results.append(
            evaluate("事件历史", "GET", "/api/sessions/{id}/events/history", p, g, checks)
        )

        # snapshot
        p, g, _ = pair("/api/sessions/{session}/snapshot")
        checks = [
            SemanticCheck("activity 相同",
                          values_at(p.payload, "activity") == values_at(g.payload, "activity"),
                          f"python={values_at(p.payload, 'activity')!r} go={values_at(g.payload, 'activity')!r}"),
            SemanticCheck("awaiting_input 相同",
                          values_at(p.payload, "awaiting_input") == values_at(g.payload, "awaiting_input"),
                          f"python={values_at(p.payload, 'awaiting_input')!r} go={values_at(g.payload, 'awaiting_input')!r}"),
        ]
        self.results.append(evaluate("会话快照", "GET", "/api/sessions/{id}/snapshot", p, g, checks))

        # stats
        p, g, _ = pair("/api/sessions/{session}/stats")
        checks = [
            SemanticCheck("stats 都非空",
                          isinstance(p.payload, dict) and bool(p.payload) and isinstance(g.payload, dict) and bool(g.payload),
                          f"python keys={sorted(p.payload)[:6] if isinstance(p.payload, dict) else None} go keys={sorted(g.payload)[:6] if isinstance(g.payload, dict) else None}",
                          severity=80),
        ]
        self.results.append(evaluate("会话统计", "GET", "/api/sessions/{id}/stats", p, g, checks))

        # tasks
        p, g, _ = pair("/api/sessions/{session}/tasks")
        checks = [
            SemanticCheck("tasks 都是数组",
                          isinstance(values_at(p.payload, "tasks"), list) and isinstance(values_at(g.payload, "tasks"), list)),
        ]
        notes = ["两边都返回 404（该会话尚无任务列表），属预期一致"]
        self.results.append(
            evaluate("会话任务", "GET", "/api/sessions/{id}/tasks", p, g, checks, notes=notes)
        )

    # -- restart / stale-session scenario ----------------------------------

    def probe_events_status(self, side: Side, session_id: str) -> tuple[int, str]:
        """Open the SSE route and report only its status, then hang up."""
        url = f"{side.base_url}/api/sessions/{session_id}/events"
        request = urllib.request.Request(url, headers={"Accept": "text/event-stream"})
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                return response.status, ""
        except urllib.error.HTTPError as exc:
            return exc.code, exc.read().decode("utf-8", errors="replace")[:200]
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            return 0, str(exc)

    def probe_restart_scenario(self) -> None:
        """Create → restart → reopen the old session id.

        Reproduces the frontend loop seen against Go: a page whose URL carries a
        session id that is on disk but no longer in memory gets 404 from
        ``/events`` and answers by POSTing ``/api/sessions`` again.  Both sides
        run the same loop for the same ten seconds.
        """
        assert self.py and self.go
        scenario: dict[str, Any] = {"sides": {}}
        for side, sid_key in ((self.py, "py_sid"), (self.go, "go_sid")):
            sid = str(self.flow[sid_key])
            restart_side(side)

            detail = self.get(side, f"/api/sessions/{sid}")
            listing = self.get(side, "/api/sessions")
            live_ids = _ids(values_at(listing.payload, "sessions"), "session_id")

            events_statuses: list[int] = []
            post_statuses: list[int] = []
            post_latencies: list[float] = []
            first_status, first_body = self.probe_events_status(side, sid)
            events_statuses.append(first_status)

            window = self.args.stale_seconds
            cap = self.args.stale_post_cap
            deadline = time.time() + window
            while time.time() < deadline and len(post_statuses) < cap:
                if first_status == 200:
                    # A live stream needs no recovery; do not spin on it.
                    time.sleep(0.25)
                    first_status, _ = self.probe_events_status(side, sid)
                    events_statuses.append(first_status)
                    continue
                posted_at = time.time()
                created = http_request("POST", side.base_url + "/api/sessions", body={}, timeout=180)
                post_latencies.append((time.time() - posted_at) * 1000.0)
                post_statuses.append(created.status)
                status, _ = self.probe_events_status(side, sid)
                events_statuses.append(status)
                time.sleep(0.01)

            scenario["sides"][side.key] = {
                "restart_seconds": side.restart_seconds,
                "detail_status": detail.status,
                "in_list": sid in live_ids,
                "list_size": len(live_ids),
                "events_first": events_statuses[0] if events_statuses else 0,
                "events_statuses": sorted(set(events_statuses)),
                "post_count": len(post_statuses),
                "post_statuses": sorted(set(post_statuses)),
                "post_min_ms": round(min(post_latencies), 1) if post_latencies else None,
                "post_median_ms": round(statistics.median(post_latencies), 1) if post_latencies else None,
                "post_max_ms": round(max(post_latencies), 1) if post_latencies else None,
                "window": window,
                "cap": cap,
            }

        py_s = scenario["sides"]["python"]
        go_s = scenario["sides"]["go"]
        py_same = py_s["events_first"] == go_s["events_first"]
        scenario["consistent"] = py_same
        if py_same and py_s["events_first"] != 200:
            slow = py_s["post_median_ms"] or 0
            fast = go_s["post_median_ms"] or 0
            ratio = (slow / fast) if fast else 0
            capped = go_s["post_count"] >= go_s["cap"]
            scenario["conclusion"] = (
                "两边一致：重启后旧 session id 的 `/events` 都返回 "
                f"{py_s['events_first']}（会话详情 `GET /api/sessions/{{id}}` 两边都能从磁盘回退返回 "
                f"{py_s['detail_status']}/{go_s['detail_status']}），因此前端重试风暴不是 Go 独有，上游同样触发。"
                "差别在放大器：POST /api/sessions 中位耗时 Python "
                f"{py_s['post_median_ms']}ms（{py_s['post_min_ms']}–{py_s['post_max_ms']}ms）、Go "
                f"{go_s['post_median_ms']}ms（{go_s['post_min_ms']}–{go_s['post_max_ms']}ms）"
                + (f"，约 {ratio:.0f}×" if ratio else "")
                + f"，同样的重试循环在 Go 上 {go_s['window']:g} 秒内"
                + (f"至少产生 {go_s['post_count']} 次创建请求（达到测量上限）" if capped else f"产生 {go_s['post_count']} 次创建请求")
                + "。"
                + (
                    f" Python 侧创建会话会拉起 queen 首轮，耗时波动到 {py_s['post_max_ms'] / 1000:.1f}s，"
                    "同一循环在 Python 上自然被限速。"
                    if (py_s["post_max_ms"] or 0) > 5000
                    else ""
                )
            )
        elif py_s["events_first"] == 200 and go_s["events_first"] != 200:
            scenario["conclusion"] = (
                f"Go 独有回归：Python 的 `/events` 对磁盘上存在的会话返回 200，Go 返回 "
                f"{go_s['events_first']}。正是这个 404 触发了前端的 POST /api/sessions 重试循环。"
            )
        elif py_s["events_first"] != 200 and go_s["events_first"] == 200:
            scenario["conclusion"] = (
                f"Python 独有：Python `/events` 返回 {py_s['events_first']}，Go 返回 200。"
            )
        else:
            scenario["conclusion"] = "两边对陈旧 session id 都返回 200，未复现 404 触发的重试。"
        self.flow["restart"] = scenario

    # -- findings ----------------------------------------------------------

    def collect_findings(self) -> None:
        assert self.py and self.go
        for result in self.results:
            if result.ok:
                continue
            group = f"{result.method} {result.path}"
            if result.py_status != result.go_status:
                self.findings.append(
                    (95, f"{result.method} {result.path} 状态码不一致：Python {result.py_status} / Go {result.go_status}", group)
                )
            diff = result.shape_diff
            if diff.missing_in_go:
                preview = ", ".join(diff.missing_in_go[:5])
                self.findings.append(
                    (60, f"{result.method} {result.path} Go 缺少 {len(diff.missing_in_go)} 个字段：{preview}", group)
                )
            if diff.type_mismatch:
                preview = "; ".join(diff.type_mismatch[:4])
                self.findings.append(
                    (45, f"{result.method} {result.path} 字段类型不一致：{preview}", group)
                )
            if diff.extra_in_go:
                preview = ", ".join(diff.extra_in_go[:5])
                self.findings.append(
                    (12, f"{result.method} {result.path} Go 多出 {len(diff.extra_in_go)} 个字段：{preview}", group)
                )
            for check in result.semantic:
                if not check.ok:
                    self.findings.append(
                        (check.severity, f"{result.method} {result.path} — {check.label}：{check.detail}", group)
                    )

        sse = self.flow.get("sse") if self.flow else None
        if sse:
            py_types = set(sse["py"].payload_types)  # type: ignore[index]
            go_types = set(sse["go"].payload_types)  # type: ignore[index]
            missing = sorted(py_types - go_types)
            if missing:
                self.findings.append(
                    (85, "SSE 事件序列：Go 少发 " + ", ".join(missing[:8]) + f" 等 {len(missing)} 种事件类型", "flow:sse")
                )
        chat = self.flow.get("chat") if self.flow else None
        if chat and chat["py"].status != chat["go"].status:  # type: ignore[index]
            self.findings.append((95, "POST /chat 状态码不一致", "flow:chat"))

        restart = self.flow.get("restart") if self.flow else None
        if restart and not restart.get("consistent"):
            py_s = restart["sides"]["python"]
            go_s = restart["sides"]["go"]
            self.findings.append(
                (
                    95,
                    "重启后陈旧会话：/events Python "
                    f"{py_s['events_first']} / Go {go_s['events_first']}（前端重试风暴的触发点）",
                    "flow:restart",
                )
            )

    def top_findings(self, limit: int = 5) -> list[str]:
        ordered = sorted(self.findings, key=lambda item: -item[0])
        seen_groups: set[str] = set()
        out: list[str] = []
        for _, text, group in ordered:
            if group in seen_groups:
                continue
            seen_groups.add(group)
            out.append(text)
            if len(out) >= limit:
                break
        return out

    # -- report ------------------------------------------------------------

    def report_text(self) -> str:
        assert self.py and self.go
        passed = sum(1 for r in self.results if r.ok)
        total = len(self.results)
        flow_checks = self.flow.get("checks", []) if self.flow else []
        flow_ok = bool(flow_checks) and all(c.ok for c in flow_checks)

        lines: list[str] = []
        lines.append("# Go hive vs Python hive — API 行为对比报告")
        lines.append("")
        lines.append(f"- 生成时间：{now_iso()}")
        lines.append(f"- 仓库：`{REPO_ROOT}`")
        lines.append(f"- 分支 / 提交：`{self.git_branch()}` / `{self.git_commit()}`")
        lines.append(f"- Python hive：`{self.py.version}`，端口 `{self.py.port}`，HIVE_HOME `{self.py.home}`")
        lines.append(f"- Go hive：`{self.go.version}`，端口 `{self.go.port}`，HIVE_HOME `{self.go.home}`")
        lines.append(
            f"- LLM：provider=`{LLM_PROVIDER}` model=`{LLM_MODEL}` api_base=`{LLM_API_BASE}` key_env=`{LLM_KEY_ENV}`"
        )
        lines.append(
            f"- 启动耗时：Python {self.py.start_seconds}s / Go {self.go.start_seconds}s"
        )
        lines.append("")
        lines.append(f"**端点对比：{passed}/{total} PASS，{total - passed} FAIL。**")
        if flow_checks:
            lines.append(
                f"**流程对比：{'PASS' if flow_ok else 'FAIL'}**（"
                f"{sum(1 for c in flow_checks if c.ok)}/{len(flow_checks)} 项通过）"
            )
        lines.append("")
        lines.append("> 对比规则：状态码必须相同；JSON 键集合与类型（递归，数组取前 3 个元素）必须相同；")
        lines.append("> 动态值（id、时间戳、端口等）不参与比较。`额外字段` 记入差异但仍判 FAIL，")
        lines.append("> 因为本报告服务于后续修复，需要看到完整缺口。")
        lines.append("")

        lines.append("## 汇总")
        lines.append("")
        lines.append("| # | 端点 | 方法 | 状态码 (Py/Go) | 结果 | 差异数 |")
        lines.append("|---|------|------|----------------|------|--------|")
        for index, result in enumerate(self.results, 1):
            verdict = "PASS" if result.ok else "FAIL"
            lines.append(
                f"| {index} | `{result.path}` | {result.method} | {result.py_status}/{result.go_status} | **{verdict}** | {result.diff_count} |"
            )
        lines.append("")

        lines.append("## 端点明细")
        lines.append("")
        for index, result in enumerate(self.results, 1):
            verdict = "PASS" if result.ok else "FAIL"
            lines.append(f"### {index}. {result.method} `{result.path}` — {verdict}")
            lines.append("")
            lines.append(f"- 状态码：Python `{result.py_status}` / Go `{result.go_status}`")
            for note in result.notes:
                lines.append(f"- 备注：{note}")
            if result.shape_diff:
                if result.shape_diff.missing_in_go:
                    lines.append(
                        f"- 仅 Python 有的字段（{len(result.shape_diff.missing_in_go)}）："
                        + format_key_list(result.shape_diff.missing_in_go)
                    )
                if result.shape_diff.extra_in_go:
                    lines.append(
                        f"- 仅 Go 有的字段（{len(result.shape_diff.extra_in_go)}）："
                        + format_key_list(result.shape_diff.extra_in_go)
                    )
                if result.shape_diff.type_mismatch:
                    lines.append(
                        f"- 类型不一致（{len(result.shape_diff.type_mismatch)}）："
                        + "; ".join(result.shape_diff.type_mismatch[:15])
                    )
            for check in result.semantic:
                mark = "✓" if check.ok else "✗"
                detail = f" — {check.detail}" if check.detail else ""
                lines.append(f"- {mark} {check.label}{detail}")
            if not result.ok:
                lines.append("")
                lines.append("<details><summary>响应摘要</summary>")
                lines.append("")
                lines.append(f"Python：`{result.py_preview}`")
                lines.append("")
                lines.append(f"Go：`{result.go_preview}`")
                lines.append("")
                lines.append("</details>")
            lines.append("")

        lines.append("## 流程对比（创建会话 → /chat → 落盘 → SSE）")
        lines.append("")
        if not self.flow.get("chat"):
            lines.append("本次运行使用 `--skip-flow`，未执行对话流程。")
            lines.append("")
        else:
            chat = self.flow["chat"]
            parts = self.flow["parts"]
            assistant = self.flow["assistant"]
            sse = self.flow["sse"]
            lines.append(f"- 提示词：`{CHAT_PROMPT}`")
            lines.append(
                f"- POST `/api/sessions/{{id}}/chat`：Python `{chat['py'].status}` / Go `{chat['go'].status}`"
            )
            lines.append(
                f"- assistant 落盘 part 数：Python {sum(1 for p in parts['py'] if p.get('role') == 'assistant')} / "
                f"Go {sum(1 for p in parts['go'] if p.get('role') == 'assistant')}"
            )
            lines.append(
                f"- 回复文本：Python `{str(assistant['py'])[:120]}` / Go `{str(assistant['go'])[:120]}`"
            )
            py_keys = sorted({k for p in parts["py"] if p.get("role") == "assistant" for k in p if not k.startswith("_")})
            go_keys = sorted({k for p in parts["go"] if p.get("role") == "assistant" for k in p if not k.startswith("_")})
            lines.append(f"- assistant part 字段：Python `{py_keys}` / Go `{go_keys}`")
            lines.append("")
            for check in self.flow.get("checks", []):
                mark = "✓" if check.ok else "✗"
                detail = f" — {check.detail}" if check.detail else ""
                lines.append(f"- {mark} {check.label}{detail}")
            lines.append("")
            lines.append(f"### SSE（各订阅 {self.args.sse_seconds:g} 秒）")
            lines.append("")
            lines.append(
                f"**Python 事件 type 序列（去重保序）：** `{self.unique_sequence(sse['py'].payload_types)}`"
            )
            lines.append("")
            lines.append(
                f"**Go 事件 type 序列（去重保序）：** `{self.unique_sequence(sse['go'].payload_types)}`"
            )
            lines.append("")
            lines.append(
                f"- 帧数：Python {len(sse['py'].frames)} / Go {len(sse['go'].frames)}"
            )
            lines.append(
                f"- SSE `event:` 名称：Python `{sorted(set(sse['py'].event_names))}` / Go `{sorted(set(sse['go'].event_names))}`"
            )
            lines.append(
                f"- 仅 Python 有的事件类型：`{sorted(set(sse['py'].payload_types) - set(sse['go'].payload_types))}`"
            )
            lines.append(
                f"- 仅 Go 有的事件类型：`{sorted(set(sse['go'].payload_types) - set(sse['py'].payload_types))}`"
            )
            lines.append("")

        restart = self.flow.get("restart") if self.flow else None
        if restart:
            lines.append("## 场景对比：重启后打开已不在内存的会话")
            lines.append("")
            lines.append(
                "模拟前端：先用返回的 session id 打开会话页，然后重启 serve（同端口、同 HIVE_HOME），"
                f"再用旧 id 订阅 `/events`；若 `/events` 非 200，就像页面那样再 POST 一次 `/api/sessions`，"
                f"持续 {self.args.stale_seconds:g} 秒（POST 上限 {self.args.stale_post_cap} 次）。"
            )
            lines.append("")
            lines.append("| 指标 | Python | Go |")
            lines.append("|------|--------|----|")
            py_s = restart["sides"]["python"]
            go_s = restart["sides"]["go"]
            lines.append(f"| 重启耗时 | {py_s['restart_seconds']}s | {go_s['restart_seconds']}s |")
            lines.append(f"| GET `/api/sessions/{{旧 id}}` | {py_s['detail_status']} | {go_s['detail_status']} |")
            lines.append(f"| 旧会话出现在 `/api/sessions` 列表 | {py_s['in_list']} | {go_s['in_list']} |")
            lines.append(f"| `/events` 首次状态码 | {py_s['events_first']} | {go_s['events_first']} |")
            lines.append(
                f"| `/events` 状态码集合 | {py_s['events_statuses']} | {go_s['events_statuses']} |"
            )
            def count_label(side_stats: dict[str, Any]) -> str:
                if side_stats["post_count"] >= side_stats["cap"]:
                    return f"≥{side_stats['post_count']}（达上限）"
                return str(side_stats["post_count"])

            lines.append(
                f"| {self.args.stale_seconds:g} 秒内 POST `/api/sessions` 次数 | {count_label(py_s)} | {count_label(go_s)} |"
            )
            lines.append(f"| POST 状态码集合 | {py_s['post_statuses']} | {go_s['post_statuses']} |")
            lines.append(
                f"| POST 耗时 min/中位/max | {py_s['post_min_ms']}/{py_s['post_median_ms']}/{py_s['post_max_ms']}ms "
                f"| {go_s['post_min_ms']}/{go_s['post_median_ms']}/{go_s['post_max_ms']}ms |"
            )

            def rate(value: float | None) -> str:
                if not value:
                    return "n/a"
                if value >= 1000:
                    return "<1/s"
                return f"≈{1000.0 / value:.0f}/s"

            lines.append(
                f"| 按中位耗时折算的创建速率 | {rate(py_s['post_median_ms'])} | {rate(go_s['post_median_ms'])} |"
            )
            if go_s["post_count"] >= go_s["cap"] or py_s["post_count"] >= py_s["cap"]:
                lines.append("")
                lines.append(
                    f"> POST 计数上限 {go_s['cap']} 次：达到上限的一侧记 `≥{go_s['cap']}`，真实速率看折算值。"
                )
            lines.append("")
            lines.append(f"**结论：** {restart['conclusion']}")
            lines.append("")

        top = self.top_findings(5)
        lines.append("## 最重要的 5 个差异")
        lines.append("")
        if top:
            for index, text in enumerate(top, 1):
                lines.append(f"{index}. {text}")
        else:
            lines.append("无：所有探针完全一致。")
        lines.append("")

        lines.append("## 复现")
        lines.append("")
        lines.append("```bash")
        lines.append("uv run python go_hive/scripts/api_parity.py")
        lines.append("```")
        lines.append("")
        lines.append(
            "脚本每次自行挑选空闲端口、创建临时 HIVE_HOME、拉起两侧 serve，结束后清理（`--keep` 可保留）。"
        )
        lines.append("")
        return "\n".join(lines)

    @staticmethod
    def unique_sequence(values: list[str]) -> str:
        seen: set[str] = set()
        out: list[str] = []
        for value in values:
            if value in seen:
                continue
            seen.add(value)
            out.append(value)
        return " → ".join(out) if out else "<none>"

    def git_branch(self) -> str:
        try:
            return subprocess.run(
                ["git", "rev-parse", "--abbrev-ref", "HEAD"],
                cwd=str(REPO_ROOT), capture_output=True, text=True, timeout=20,
            ).stdout.strip() or "<unknown>"
        except (OSError, subprocess.TimeoutExpired):
            return "<unknown>"

    def git_commit(self) -> str:
        try:
            return subprocess.run(
                ["git", "rev-parse", "--short", "HEAD"],
                cwd=str(REPO_ROOT), capture_output=True, text=True, timeout=20,
            ).stdout.strip() or "<unknown>"
        except (OSError, subprocess.TimeoutExpired):
            return "<unknown>"

    # -- lifecycle ---------------------------------------------------------

    def run(self) -> int:
        exit_code = 1
        try:
            self.prepare()
            self.probe_static()
            self.create_sessions()
            if not self.args.skip_flow:
                self.run_flow()
            self.probe_session_surface()
            if not self.args.skip_flow and not self.args.skip_restart:
                self.probe_restart_scenario()
            self.collect_findings()
            report = self.report_text()
            report_path = Path(self.args.report).expanduser()
            report_path.parent.mkdir(parents=True, exist_ok=True)
            report_path.write_text(report, encoding="utf-8")
            passed = sum(1 for r in self.results if r.ok)
            flow_ok = all(c.ok for c in self.flow.get("checks", [])) if self.flow.get("checks") else None
            print("")
            print(f"[parity] report: {report_path}")
            print(f"[parity] endpoints: {passed}/{len(self.results)} PASS")
            if flow_ok is not None:
                print(f"[parity] flow: {'PASS' if flow_ok else 'FAIL'}")
            for index, text in enumerate(self.top_findings(5), 1):
                print(f"[parity] diff {index}: {text}")
            exit_code = 0 if passed == len(self.results) and flow_ok is not False else 1
        finally:
            if self.py:
                terminate(self.py)
            if self.go:
                terminate(self.go)
            if not self.args.keep and self.root.is_dir():
                shutil.rmtree(self.root, ignore_errors=True)
        return exit_code


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Go vs Python hive API parity harness")
    parser.add_argument("--go-dir", default=str(DEFAULT_GO_DIR), help="Go module directory")
    parser.add_argument("--go-binary", default=str(DEFAULT_GO_BINARY), help="Go binary (built if missing)")
    parser.add_argument("--no-build", action="store_true", help="Fail instead of building the Go binary")
    parser.add_argument("--report", default=str(DEFAULT_REPORT), help="Report path")
    parser.add_argument("--env-file", default=str(DEFAULT_ENV_FILE), help="Shell env file with the LLM key")
    parser.add_argument("--workdir", default="", help="Directory for temp HIVE_HOMEs (default: TMPDIR)")
    parser.add_argument("--port-python", type=int, default=0, help="Pin the Python port")
    parser.add_argument("--port-go", type=int, default=0, help="Pin the Go port")
    parser.add_argument("--sse-seconds", type=float, default=10.0, help="SSE window per side")
    parser.add_argument("--reply-timeout", type=float, default=CHAT_REPLY_TIMEOUT_S, help="Seconds to wait for the assistant turn")
    parser.add_argument("--skip-flow", action="store_true", help="Endpoint probes only; no LLM call")
    parser.add_argument("--skip-restart", action="store_true", help="Skip the restart / stale-session scenario")
    parser.add_argument("--stale-seconds", type=float, default=10.0, help="Restart-scenario measurement window")
    parser.add_argument("--stale-post-cap", type=int, default=30, help="Max POST /api/sessions during that window (bounds LLM spend)")
    parser.add_argument("--keep", action="store_true", help="Keep the temp HIVE_HOMEs and logs")
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    try:
        return Harness(args).run()
    except ParityError as exc:
        print(f"[parity] error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
