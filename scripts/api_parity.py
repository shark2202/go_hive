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
  * the frontend's stale-session recovery loop after a restart, once with a
    blank create and once with ``queen_resume_from`` — a side that heals the
    old id stops the loop, which is what separates a real recovery gap from a
    mere speed difference

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
    body_error: str = ""

    @property
    def ok(self) -> bool:
        # Parity is equality, not success: two 404s are a match.
        if self.py_status != self.go_status:
            return False
        if self.body_error:
            return False
        if self.shape_diff:
            return False
        return all(check.ok for check in self.semantic)

    @property
    def diff_count(self) -> int:
        status_diff = 0 if self.py_status == self.go_status else 1
        body_diff = 1 if self.body_error else 0
        return status_diff + body_diff + self.shape_diff.count + sum(1 for c in self.semantic if not c.ok)


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
        problems = []
        if not py.json_ok:
            result.notes.append(f"python body: {py.error or 'unparsable'} — {py.raw[:120]!r}")
            problems.append(f"python={py.error or 'unparsable'}")
        if not go.json_ok:
            result.notes.append(f"go body: {go.error or 'unparsable'} — {go.raw[:120]!r}")
            problems.append(f"go={go.error or 'unparsable'}")
        result.body_error = "; ".join(problems)
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
        self.crashes: list[dict[str, Any]] = []
        self.phases: list[dict[str, Any]] = []
        self.evidence: dict[str, Any] = {}
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

    def record_crashes(self) -> bool:
        """Record a side that exited mid-run; returns True when one is found.

        Without this, a crashed server surfaces only as a wall of status 0 —
        the interesting evidence (the process exit code and the log tail that
        explains it) is lost when the temp HIVE_HOMEs are cleaned up.
        """
        seen = {crash["side"] for crash in self.crashes}
        found = False
        for side in (self.py, self.go):
            if side is None or side.key in seen:
                continue
            if side.process is None or side.process.poll() is None:
                continue
            self.crashes.append(
                {
                    "side": side.key,
                    "label": side.label,
                    "returncode": side.process.returncode,
                    "log": log_tail(side.log_path, 40),
                }
            )
            found = True
        return found

    def get(self, side: Side, path: str, timeout: float = 60.0) -> HttpResult:
        return http_request("GET", side.base_url + path, timeout=timeout)

    def wait_for_opening_block(self, side: Side, session_id: str, want: int = 5, timeout: float = 30.0) -> None:
        """Wait (bounded) until the session's history carries its opening block.

        A freshly created session writes five opening events (queen identity,
        session-start reminder, loop start, first iteration, context usage).
        The Python queen loop writes them just after the create response, so a
        history read that races it sees a one-event prefix and reports a false
        shape diff against the Go side, which writes the block synchronously.
        Waiting for the settled block removes the race without weakening the
        comparison: a side that never reaches it still diffs below.
        """
        deadline = time.time() + timeout
        while time.time() < deadline:
            result = self.get(side, f"/api/sessions/{session_id}/events/history")
            total = values_at(result.payload, "total")
            if isinstance(total, int) and total >= want:
                return
            time.sleep(0.25)

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
                    # Same count is not enough: a missing skill replaced by a
                    # different one must fail here, not hide behind the total.
                    SemanticCheck(
                        "skill 名称集合相同",
                        set(p_skills) == set(g_skills),
                        "python 独有 " + ", ".join(sorted(set(p_skills) - set(g_skills))[:6])
                        + "｜go 独有 " + ", ".join(sorted(set(g_skills) - set(p_skills))[:6]),
                    ),
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
                    severity=90,
                )
            )
            checks.append(
                SemanticCheck(
                    "queen_phase 相同",
                    p.payload.get("queen_phase") == g.payload.get("queen_phase"),
                    f"python={p.payload.get('queen_phase')!r} go={g.payload.get('queen_phase')!r}",
                    severity=80,
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

        # events — the SSE endpoint itself (the stream content is compared in the flow section)
        py_ev_status, py_ev_meta = self.probe_events_status(py, py_sid)
        go_ev_status, go_ev_meta = self.probe_events_status(go, go_sid)
        self.results.append(
            evaluate(
                "事件流订阅", "GET", "/api/sessions/{id}/events",
                HttpResult(status=py_ev_status, payload={}, raw=""),
                HttpResult(status=go_ev_status, payload={}, raw=""),
                [
                    SemanticCheck(
                        "都返回 200",
                        py_ev_status == 200 and go_ev_status == 200,
                        f"python={py_ev_status} go={go_ev_status}",
                    ),
                    SemanticCheck(
                        "Content-Type 都声明 text/event-stream",
                        "text/event-stream" in py_ev_meta.lower() and "text/event-stream" in go_ev_meta.lower(),
                        f"python={py_ev_meta!r} go={go_ev_meta!r}",
                    ),
                ],
                requires_json=False,
            )
        )

        # events/history — what the UI replays after a page reload. Wait for
        # each side's opening block first: Python's is written asynchronously
        # by the queen loop, so an immediate read can catch a prefix.
        self.wait_for_opening_block(py, py_sid)
        self.wait_for_opening_block(go, go_sid)
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
        notes = [
            f"状态码 Python {p.status} / Go {g.status}"
            + ("（该会话尚无任务列表，两边同为 404，属预期一致）" if p.status == g.status == 404 else "")
        ]
        self.results.append(
            evaluate("会话任务", "GET", "/api/sessions/{id}/tasks", p, g, checks, notes=notes)
        )

    # -- restart / stale-session scenario ----------------------------------

    def probe_events_status(self, side: Side, session_id: str) -> tuple[int, str]:
        """Open the SSE route and report only its status and Content-Type, then hang up."""
        url = f"{side.base_url}/api/sessions/{session_id}/events"
        request = urllib.request.Request(url, headers={"Accept": "text/event-stream"})
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                return response.status, response.headers.get("Content-Type", "")
        except urllib.error.HTTPError as exc:
            return exc.code, exc.read().decode("utf-8", errors="replace")[:200]
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            return 0, str(exc)

    def storm_loop(
        self, side: Side, session_id: str, body: dict[str, Any], window: float, cap: int
    ) -> dict[str, Any]:
        """Simulate one client recovery loop and measure what it costs.

        The loop mirrors the frontend: subscribe to ``/events``; on a non-200,
        POST ``/api/sessions`` and try the same id again.  Two bodies are
        interesting — ``{}`` (a blank create, what a naive client does) and
        ``{"queen_resume_from": id}`` (what ``pages/queen-dm.tsx`` does in its
        ``onSessionGone`` handler).
        """
        post_statuses: list[int] = []
        post_latencies: list[float] = []
        created_ids: list[str] = []
        events_statuses: list[int] = []
        first_status, _ = self.probe_events_status(side, session_id)
        events_statuses.append(first_status)
        healed = False
        deadline = time.time() + window
        while time.time() < deadline and len(post_statuses) < cap:
            if first_status == 200:
                # A live stream needs no recovery; do not spin on it.
                time.sleep(0.25)
                first_status, _ = self.probe_events_status(side, session_id)
                events_statuses.append(first_status)
                continue
            posted_at = time.time()
            created = http_request("POST", side.base_url + "/api/sessions", body=body, timeout=180)
            post_latencies.append((time.time() - posted_at) * 1000.0)
            post_statuses.append(created.status)
            if isinstance(created.payload, dict):
                new_id = created.payload.get("session_id")
                if isinstance(new_id, str):
                    created_ids.append(new_id)
            status, _ = self.probe_events_status(side, session_id)
            events_statuses.append(status)
            if status == 200:
                healed = True
                break
            time.sleep(0.01)
        return {
            "post_count": len(post_statuses),
            "post_statuses": sorted(set(post_statuses)),
            "post_min_ms": round(min(post_latencies), 1) if post_latencies else None,
            "post_median_ms": round(statistics.median(post_latencies), 1) if post_latencies else None,
            "post_max_ms": round(max(post_latencies), 1) if post_latencies else None,
            "events_first": events_statuses[0] if events_statuses else 0,
            "events_statuses": sorted(set(events_statuses)),
            "events_after": events_statuses[-1] if events_statuses else 0,
            "healed": healed,
            "created_id_reused": session_id in created_ids,
            "distinct_created_ids": len(set(created_ids)),
            "cap": cap,
            "window": window,
        }

    def probe_restart_scenario(self) -> None:
        """Create → restart → reopen the old session id.

        A page whose URL carries a session id that is on disk but no longer in
        memory gets 404 from ``/events``.  Two recovery strategies are measured
        on both sides for the same window:

        * ``blank``  — POST ``{}``: a brand-new session, old id stays dead.
        * ``resume`` — POST ``{"queen_resume_from": id}``: what the DM page's
          ``onSessionGone`` handler actually sends to rebind the queen.
        """
        assert self.py and self.go
        scenario: dict[str, Any] = {"sides": {}}
        for side, sid_key in ((self.py, "py_sid"), (self.go, "go_sid")):
            sid = str(self.flow[sid_key])
            restart_side(side)

            detail = self.get(side, f"/api/sessions/{sid}")
            listing = self.get(side, "/api/sessions")
            live_ids = _ids(values_at(listing.payload, "sessions"), "session_id")

            window = self.args.stale_seconds
            cap = self.args.stale_post_cap
            blank = self.storm_loop(side, sid, {}, window, cap)
            resume = self.storm_loop(side, sid, {"queen_resume_from": sid}, window, min(cap, 10))

            scenario["sides"][side.key] = {
                "restart_seconds": side.restart_seconds,
                "detail_status": detail.status,
                "in_list": sid in live_ids,
                "list_size": len(live_ids),
                "blank": blank,
                "resume": resume,
            }

        py_s = scenario["sides"]["python"]
        go_s = scenario["sides"]["go"]
        scenario["consistent"] = py_s["blank"]["events_first"] == go_s["blank"]["events_first"]
        scenario["conclusion"] = self.restart_conclusion(py_s, go_s)
        self.flow["restart"] = scenario

    @staticmethod
    def restart_conclusion(py_s: dict[str, Any], go_s: dict[str, Any]) -> str:
        py_blank, go_blank = py_s["blank"], go_s["blank"]
        py_resume, go_resume = py_s["resume"], go_s["resume"]
        if py_blank["events_first"] != go_blank["events_first"]:
            if py_blank["events_first"] == 200:
                return (
                    f"Go 独有回归：重启后 Python 对旧 id 的 `/events` 返回 200，Go 返回 "
                    f"{go_blank['events_first']}。这个 404 就是前端 POST /api/sessions 重试循环的触发点。"
                )
            return (
                f"Python 独有：重启后 Python 的 `/events` 返回 {py_blank['events_first']}，Go 返回 200。"
            )
        if py_blank["events_first"] == 200:
            return "两边对陈旧 session id 的 `/events` 都返回 200，未复现 404 触发的重试。"

        slow = py_blank["post_median_ms"] or 0
        fast = go_blank["post_median_ms"] or 0
        ratio = (slow / fast) if fast else 0
        blank_line = (
            f"空创建（POST `{{}}`）两边都陷入循环：Python {py_blank['post_count']} 次 / Go "
            f"{go_blank['post_count']} 次（{go_blank['window']:g}s 窗口，上限 {go_blank['cap']}），"
            f"中位耗时 Python {py_blank['post_median_ms']}ms / Go {go_blank['post_median_ms']}ms"
            + (f"，约 {ratio:.0f}×" if ratio else "")
            + "。"
        )
        if py_resume["healed"] and not go_resume["healed"]:
            return (
                "前端真实的自愈路径（POST `queen_resume_from=<旧 id>`）只有 Python 能恢复："
                f"Python 第 {py_resume['post_count']} 次 POST 后旧 id 的 `/events` 回到 200，循环自行停止；"
                f"Go 在 {go_resume['window']:g}s 窗口内试了 {go_resume['post_count']} 次"
                f"（上限 {go_resume['cap']}），旧 id 始终 {go_resume['events_after']}，"
                "因此同一个页面在 Go 上会持续重试。"
                + blank_line
                + "结论：404 本身是上游同构行为，但「恢复后能否继续流」是 Go 独有缺口，"
                "这才是重试风暴在 Go 上放大的根因；Go 每次 POST 又快 "
                + (f"{ratio:.0f}×" if ratio else "得多")
                + "，把同一个循环推得更高频。"
            )
        if py_resume["healed"] and go_resume["healed"]:
            return (
                "两边都会自愈：POST `queen_resume_from=<旧 id>` 之后旧 id 的 `/events` 都回到 200，"
                "前端循环会停止。差别只在速率放大器：" + blank_line
            )
        if not py_resume["healed"] and not go_resume["healed"]:
            return (
                "两边都不自愈：POST `queen_resume_from=<旧 id>` 之后旧 id 的 `/events` 仍非 200"
                f"（Python {py_resume['events_after']} / Go {go_resume['events_after']}），"
                "因此重试风暴不是 Go 独有，上游同构触发。" + blank_line
            )
        return "两边行为相反：Python 不自愈而 Go 自愈。" + blank_line

    # -- evidence ----------------------------------------------------------

    def collect_artifacts(self) -> None:
        """Record on-disk facts that explain the empty responses.

        The parity table says *what* differs; these numbers say why — e.g. a
        Go home with zero ``events.jsonl`` files cannot serve history from
        disk no matter how many turns ran.
        """
        assert self.py and self.go
        evidence: dict[str, Any] = {}
        for side in (self.py, self.go):
            if side is None:
                continue
            events = sorted(side.home.glob("**/events.jsonl"))
            queens = sorted(
                path.name
                for path in (side.home / "queens").glob("*")
                if path.is_dir()
            ) if (side.home / "queens").is_dir() else []
            sessions = sorted(side.home.glob("**/sessions/session_*"))
            evidence[side.key] = {
                "events_jsonl": len(events),
                "events_lines": sum(len(p.read_text(errors="replace").splitlines()) for p in events[:50]),
                "queen_dirs": queens,
                "session_dirs": len(sessions),
            }
        self.evidence = evidence

    # -- findings ----------------------------------------------------------

    def collect_findings(self) -> None:
        assert self.py and self.go
        for crash in self.crashes:
            self.findings.append(
                (
                    100,
                    f"{crash['label']} 进程在对比过程中退出（exit={crash['returncode']}），其后的端点只会返回连接失败",
                    "process:crash",
                )
            )
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
        if restart:
            py_s = restart["sides"]["python"]
            go_s = restart["sides"]["go"]
            if py_s["blank"]["events_first"] != go_s["blank"]["events_first"]:
                self.findings.append(
                    (
                        95,
                        "重启后陈旧会话：/events Python "
                        f"{py_s['blank']['events_first']} / Go {go_s['blank']['events_first']}"
                        "（前端重试风暴的触发点）",
                        "flow:restart",
                    )
                )
            elif py_s["resume"]["healed"] != go_s["resume"]["healed"]:
                self.findings.append(
                    (
                        95,
                        "重启后 `queen_resume_from` 自愈：Python "
                        f"{'恢复' if py_s['resume']['healed'] else '未恢复'} / Go "
                        f"{'恢复' if go_s['resume']['healed'] else '未恢复'}"
                        f"（旧 id 的 /events 最终 Python {py_s['resume']['events_after']} / Go {go_s['resume']['events_after']}）",
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
        if self.phases:
            lines.append(
                "- 阶段耗时：" + " → ".join(f"{p['label']} {p['seconds']}s" for p in self.phases)
            )
        lines.append(
            "- 进程存活：Python "
            + ("正常" if self.py.process and self.py.process.poll() is None else "已退出")
            + " / Go "
            + ("正常" if self.go.process and self.go.process.poll() is None else "已退出")
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
        lines.append("## 结论")
        lines.append("")
        failed = [r for r in self.results if not r.ok]
        if failed:
            lines.append(
                f"- 端点契约 {passed}/{total} 通过；{len(failed)} 条 FAIL，逐条见下方端点明细。"
            )
        else:
            lines.append(
                f"- 端点契约 {passed}/{total} 全部通过：状态码相同，JSON 键集合与类型一致，"
                "端点级语义检查全部成立。"
            )
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

        if self.crashes:
            lines.append("## 服务进程异常退出")
            lines.append("")
            for crash in self.crashes:
                lines.append(
                    f"### {crash['label']} — exit code `{crash['returncode']}`"
                )
                lines.append("")
                lines.append("```text")
                lines.append(crash["log"])
                lines.append("```")
                lines.append("")
            lines.append(
                "该侧进程退出后，后续端点只会返回连接失败（状态码 0），因此它们的 FAIL 记录的是"
                "“服务不在了”，不是“协议不一致”；上面的日志尾部才是根因证据。"
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
            lines.append(
                "> 注：Python 额外落盘的 assistant part 是会话创建时的开场白；"
                "`event:` 名称差异属于线格式（Python 只发 `data:`，Go 额外带 `event:` 名），"
                "当前前端 `api/client.ts::subscribeSse` 会解析但 `onEvent` 不消费事件名，所以不影响现有 UI。"
            )
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
                f"- 完整事件 type 序列（按到达顺序，最多 80 帧）：Python `{self.compact_sequence(sse['py'].payload_types)}`"
            )
            lines.append(
                f"- 完整事件 type 序列（按到达顺序，最多 80 帧）：Go `{self.compact_sequence(sse['go'].payload_types)}`"
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
                "再用旧 id 订阅 `/events`；若 `/events` 非 200，就像页面那样再 POST 一次 `/api/sessions`。"
                f"两种请求体各测 {self.args.stale_seconds:g} 秒："
                "`blank` = `{}`（新建一个无关会话），`resume` = `{\"queen_resume_from\": 旧 id}`"
                "（`pages/queen-dm.tsx` 的 `onSessionGone` 实际发的恢复请求）。"
            )
            lines.append("")
            lines.append("| 指标 | Python | Go |")
            lines.append("|------|--------|----|")
            py_s = restart["sides"]["python"]
            go_s = restart["sides"]["go"]
            py_blank, go_blank = py_s["blank"], go_s["blank"]
            py_resume, go_resume = py_s["resume"], go_s["resume"]
            lines.append(f"| 重启耗时 | {py_s['restart_seconds']}s | {go_s['restart_seconds']}s |")
            lines.append(f"| GET `/api/sessions/{{旧 id}}` | {py_s['detail_status']} | {go_s['detail_status']} |")
            lines.append(f"| 旧会话出现在 `/api/sessions` 列表 | {py_s['in_list']} | {go_s['in_list']} |")
            lines.append(f"| `/events` 首次状态码 | {py_blank['events_first']} | {go_blank['events_first']} |")
            lines.append(
                f"| `/events` 状态码集合（blank 循环） | {py_blank['events_statuses']} | {go_blank['events_statuses']} |"
            )

            def count_label(side_stats: dict[str, Any]) -> str:
                if side_stats["post_count"] >= side_stats["cap"]:
                    return f"≥{side_stats['post_count']}（达上限）"
                return str(side_stats["post_count"])

            lines.append(
                f"| blank：POST 次数 | {count_label(py_blank)} | {count_label(go_blank)} |"
            )
            lines.append(f"| blank：POST 状态码集合 | {py_blank['post_statuses']} | {go_blank['post_statuses']} |")
            lines.append(
                f"| blank：POST 耗时 min/中位/max | {py_blank['post_min_ms']}/{py_blank['post_median_ms']}/{py_blank['post_max_ms']}ms "
                f"| {go_blank['post_min_ms']}/{go_blank['post_median_ms']}/{go_blank['post_max_ms']}ms |"
            )

            def rate(value: float | None) -> str:
                if not value:
                    return "n/a"
                if value >= 1000:
                    return "<1/s"
                return f"≈{1000.0 / value:.0f}/s"

            lines.append(
                f"| blank：按中位耗时折算的创建速率 | {rate(py_blank['post_median_ms'])} | {rate(go_blank['post_median_ms'])} |"
            )
            lines.append(f"| resume：POST 次数 | {count_label(py_resume)} | {count_label(go_resume)} |")
            lines.append(
                f"| resume：POST 后旧 id 的 `/events` | {py_resume['events_after']} | {go_resume['events_after']} |"
            )
            lines.append(
                f"| resume：旧 id 是否恢复可订阅 | {py_resume['healed']} | {go_resume['healed']} |"
            )
            lines.append(
                f"| resume：POST 返回的 session id 是否复用旧 id | {py_resume['created_id_reused']} | {go_resume['created_id_reused']} |"
            )
            lines.append(
                f"| resume：产生的不同 session id 数 | {py_resume['distinct_created_ids']} | {go_resume['distinct_created_ids']} |"
            )
            lines.append(
                f"| resume：POST 耗时 min/中位/max | {py_resume['post_min_ms']}/{py_resume['post_median_ms']}/{py_resume['post_max_ms']}ms "
                f"| {go_resume['post_min_ms']}/{go_resume['post_median_ms']}/{go_resume['post_max_ms']}ms |"
            )
            if go_blank["post_count"] >= go_blank["cap"] or py_blank["post_count"] >= py_blank["cap"]:
                lines.append("")
                lines.append(
                    f"> blank 循环 POST 计数上限 {go_blank['cap']} 次：达到上限的一侧记 `≥{go_blank['cap']}`，真实速率看折算值。"
                )
            lines.append("")
            lines.append(f"**结论：** {restart['conclusion']}")
            lines.append("")

        top = self.top_findings(5)
        if self.evidence:
            py_e = self.evidence.get("python", {})
            go_e = self.evidence.get("go", {})
            lines.append("## 磁盘证据")
            lines.append("")
            lines.append("| 证据 | Python | Go |")
            lines.append("|------|--------|----|")
            lines.append(
                f"| `events.jsonl` 文件数 | {py_e.get('events_jsonl')} | {go_e.get('events_jsonl')} |"
            )
            lines.append(
                f"| `events.jsonl` 总行数（前 50 个文件） | {py_e.get('events_lines')} | {go_e.get('events_lines')} |"
            )
            lines.append(
                f"| `queens/` 下的身份目录 | `{py_e.get('queen_dirs')}` | `{go_e.get('queen_dirs')}` |"
            )
            lines.append(
                f"| 会话目录数 | {py_e.get('session_dirs')} | {go_e.get('session_dirs')} |"
            )
            lines.append("")
            empty_sides = [
                side
                for side, stats in (("Python", py_e), ("Go", go_e))
                if not stats.get("events_jsonl")
            ]
            if empty_sides:
                lines.append(
                    "".join(empty_sides)
                    + " 的 HIVE_HOME 里没有 `events.jsonl`：该侧的 history / snapshot / stats 只能为空，"
                    "不是序列化差异，是这些数据从未被写入。"
                )
            else:
                lines.append(
                    "两侧的 `events.jsonl` 与会话树都随会话创建落盘；上表用于核对同一组文件的规模。"
                )
            lines.append("")

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

    @staticmethod
    def compact_sequence(values: list[str], limit: int = 80) -> str:
        if not values:
            return "<none>"
        head = " → ".join(values[:limit])
        if len(values) > limit:
            head += f" → …（共 {len(values)} 帧）"
        return head

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
            steps = [
                ("静态端点", self.probe_static),
                ("创建会话", self.create_sessions),
            ]
            if not self.args.skip_flow:
                steps.append(("对话流程", self.run_flow))
            steps.append(("会话端点", self.probe_session_surface))
            if not self.args.skip_flow and not self.args.skip_restart:
                steps.append(("重启场景", self.probe_restart_scenario))

            for label, step in steps:
                started = time.time()
                step()
                self.phases.append({"label": label, "seconds": round(time.time() - started, 2)})
                if self.record_crashes():
                    print(f"[parity] aborting after «{label}»: a server exited mid-run")
                    break
            self.collect_artifacts()
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
            exit_code = 0 if passed == len(self.results) and flow_ok is not False and not self.crashes else 1
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
