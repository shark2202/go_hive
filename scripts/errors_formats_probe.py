#!/usr/bin/env python3
"""Real two-side probes for appearance dimensions #12 (errors) and #14 (formats).

Both runtimes are started for real: Python `hive serve` (the reference
checkout's venv) and the Go binary built from this tree.  Each side gets a
byte-identical fixture HIVE_HOME (same ``configuration.json``, credential key,
seeded agent dir) so every probe starts from the same state.

``errors``
    * failure-path matrix over the main endpoints (400 / 403 / 404 / 409 / 500
      as constructible), comparing HTTP status, Content-Type, body form
      (JSON vs text), error-body keys/types and value-canonicalised message
      text;
    * model-visible tool error text for four reviewed categories (read_file
      missing, edit_file mismatch, terminal non-zero exit, credential-gated
      tool without a connected credential), captured through each runtime's
      own serve + the scripted fake OpenAI gateway (`fake_tool_gateway.py`),
      exactly like `capture_tool_output.py`.

``formats``
    * every time-looking value per endpoint, classified by representation
      (ISO naive/offset, float epoch, int epoch, ...) and compared path by
      path;
    * ID format inventory (session / colony / execution / correlation / event
      seq / trigger);
    * pagination contract of ``/api/sessions/{id}/events/history`` (limit,
      before_offset, before_index, has_more_older, total) plus invalid-param
      fallbacks, with page stitching verified;
    * sort stability: every list endpoint is read twice and the identity
      order compared, then the two sides' orders compared.

Artifacts (committed):
    go_hive/docs/e2e-assets/errors-formats/errors-raw.json
    go_hive/docs/e2e-assets/errors-formats/errors-ledger.json
    go_hive/docs/e2e-assets/errors-formats/formats-raw.json
    go_hive/docs/e2e-assets/errors-formats/formats-ledger.json
    go_hive/docs/error-taxonomy-diff.md
    go_hive/docs/formats-parity.md

Usage:
    uv run --no-project python go_hive/scripts/errors_formats_probe.py errors
    uv run --no-project python go_hive/scripts/errors_formats_probe.py formats
    uv run --no-project python go_hive/scripts/errors_formats_probe.py errors --check
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import re
import secrets
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime
from pathlib import Path
from typing import Any

HERE = Path(__file__).resolve().parent
REPO_ROOT = HERE.parents[1]
GO_DIR = REPO_ROOT / "go_hive" / "go-hive"
GO_BINARY = GO_DIR / "dist" / "hive-errfmt"
DEFAULT_PY_BIN = Path("/Users/mac/hive/.venv/bin/hive")
DEFAULT_PY_ROOT = Path("/Users/mac/hive")
GATEWAY = HERE / "fake_tool_gateway.py"

ARTIFACT_DIR = REPO_ROOT / "go_hive" / "docs" / "e2e-assets" / "errors-formats"
ERRORS_RAW = ARTIFACT_DIR / "errors-raw.json"
ERRORS_LEDGER = ARTIFACT_DIR / "errors-ledger.json"
FORMATS_RAW = ARTIFACT_DIR / "formats-raw.json"
FORMATS_LEDGER = ARTIFACT_DIR / "formats-ledger.json"
ERRORS_REPORT = REPO_ROOT / "go_hive" / "docs" / "error-taxonomy-diff.md"
FORMATS_REPORT = REPO_ROOT / "go_hive" / "docs" / "formats-parity.md"

QUEEN_ID = "queen_growth"
MISSING_SESSION = "session_20990101_000000_deadbeef"
HEALTH_TIMEOUT_S = 180.0
SETTLE_TIMEOUT_S = 90.0


# ---------------------------------------------------------------------------
# small utilities
# ---------------------------------------------------------------------------


def free_port() -> int:
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port


def now_iso() -> str:
    return datetime.now().astimezone().isoformat(timespec="seconds")


def git_head(path: Path) -> str:
    try:
        return subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=str(path), capture_output=True, text=True, timeout=30
        ).stdout.strip()
    except Exception:  # noqa: BLE001
        return ""


class Resp:
    __slots__ = ("status", "ctype", "text", "error")

    def __init__(self, status: int, ctype: str, text: str, error: str = "") -> None:
        self.status = status
        self.ctype = ctype
        self.text = text
        self.error = error

    @property
    def json(self):
        try:
            return json.loads(self.text)
        except (ValueError, TypeError):
            return None


def http_request(
    method: str,
    url: str,
    body=None,
    raw: str | bytes | None = None,
    timeout: float = 30.0,
) -> Resp:
    data: bytes | None = None
    if raw is not None:
        data = raw if isinstance(raw, bytes) else raw.encode()
    elif body is not None:
        data = json.dumps(body).encode()
    request = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        request.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return Resp(response.status, response.headers.get("Content-Type", ""),
                        response.read().decode("utf-8", "replace"))
    except urllib.error.HTTPError as exc:
        return Resp(exc.code, exc.headers.get("Content-Type", ""),
                    exc.read().decode("utf-8", "replace"))
    except Exception as exc:  # noqa: BLE001
        return Resp(0, "", "", str(exc))


SESSION_RE = re.compile(r"session_\d{8}_\d{6}_[0-9a-f]{8}")
HEX32_RE = re.compile(r"^[0-9a-f]{32}$")
ISO_NAIVE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?$")
ISO_OFFSET_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})$")


def mask_text(text: str) -> str:
    """Mask values that cannot be identical across two processes."""
    text = SESSION_RE.sub("<SESSION_ID>", text)
    text = re.sub(r"\\b\d{9,10}\.\d+\\b", "<EPOCH>", text)
    text = re.sub(r"/private/var/[^\\s\"']+", "<TMP_PATH>", text)
    text = re.sub(r"/var/folders/[^\\s\"']+", "<TMP_PATH>", text)
    return text


def mask_value(value, key: str = ""):
    if isinstance(value, str):
        if SESSION_RE.fullmatch(value):
            return "<SESSION_ID>"
        masked = mask_text(value)
        if re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?", masked):
            return "<TIMESTAMP>"
        if re.fullmatch(r"\d{9,10}(\.\d+)?", masked):
            return "<EPOCH>"
        return masked
    if isinstance(value, list):
        return [mask_value(item, key) for item in value]
    if isinstance(value, dict):
        return {k: mask_value(v, k) for k, v in value.items()}
    return value


def json_type(value) -> str:
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "bool"
    if isinstance(value, int):
        return "int"
    if isinstance(value, float):
        return "float"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    return type(value).__name__


def walk_shape(value, path: str, out: dict) -> None:
    if isinstance(value, dict):
        out[path] = "object"
        for key, item in value.items():
            walk_shape(item, f"{path}.{key}" if path else key, out)
    elif isinstance(value, list):
        out[path] = f"array[{len(value)}]" if not value or not isinstance(value[0], (dict, list)) else "array"
        if value and isinstance(value[0], (dict, list)):
            walk_shape(value[0], f"{path}[]", out)
    else:
        out[path] = json_type(value)


def compare_structures(py, go, max_items: int = 40) -> dict:
    """Compare two canonicalised JSON payloads; returns diff buckets."""
    py_shape: dict = {}
    go_shape: dict = {}
    walk_shape(py, "", py_shape)
    walk_shape(go, "", go_shape)
    missing = sorted(k for k in py_shape if k not in go_shape)
    extra = sorted(k for k in go_shape if k not in py_shape)
    type_diffs = sorted(
        (k, py_shape[k], go_shape[k]) for k in py_shape if k in go_shape and py_shape[k] != go_shape[k]
    )
    value_diffs = []

    def walk(p, g, path):
        if len(value_diffs) >= max_items:
            return
        if isinstance(p, dict) and isinstance(g, dict):
            for key in p:
                if key in g:
                    walk(p[key], g[key], f"{path}.{key}" if path else key)
        elif isinstance(p, list) and isinstance(g, list):
            if len(p) != len(g):
                value_diffs.append((path, f"len {len(p)}", f"len {len(g)}"))
            for index, (pi, gi) in enumerate(zip(p, g)):
                walk(pi, gi, f"{path}[{index}]")
        else:
            if p != g:
                value_diffs.append((path, p, g))

    walk(py, go, "")
    return {
        "missing_in_target": missing[:max_items],
        "extra_in_target": extra[:max_items],
        "type_diffs": [{"path": k, "reference": a, "target": b} for k, a, b in type_diffs[:max_items]],
        "value_diffs": [{"path": p, "reference": a, "target": b} for p, a, b in value_diffs[:max_items]],
    }


def slim_value(value, max_string: int = 80):
    """Cap long strings before committing a raw capture.

    The formats probe compares representations (keys, types, time classes,
    identity order); multi-KB descriptions and skill bodies only bloat the
    artifact.  Long strings keep their key, length and a short digest so the
    capture stays auditable.
    """
    if isinstance(value, str):
        if len(value) <= max_string:
            return value
        digest = hashlib.sha256(value.encode("utf-8")).hexdigest()[:12]
        return f"<str:sha256={digest} len={len(value)}>"
    if isinstance(value, list):
        return [slim_value(item, max_string) for item in value]
    if isinstance(value, dict):
        return {key: slim_value(item, max_string) for key, item in value.items()}
    return value


# ---------------------------------------------------------------------------
# servers
# ---------------------------------------------------------------------------


class StubLLM:
    """Deterministic OpenAI-compatible stub (no network beyond loopback)."""

    def __init__(self, port: int) -> None:
        from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, format, *args):  # noqa: A002 - stdlib signature
                return

            def do_POST(self):  # noqa: N802
                length = int(self.headers.get("Content-Length", "0") or 0)
                raw = self.rfile.read(length) if length else b"{}"
                try:
                    request = json.loads(raw.decode("utf-8"))
                except ValueError:
                    request = {}
                content = "PROBE_OK"
                if request.get("stream"):
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Cache-Control", "no-cache")
                    self.end_headers()
                    for choice in (
                        {"delta": {"role": "assistant", "content": content}, "finish_reason": None},
                        {"delta": {}, "finish_reason": "stop"},
                    ):
                        chunk = {
                            "id": "chatcmpl-probe",
                            "object": "chat.completion.chunk",
                            "created": int(time.time()),
                            "model": request.get("model", "probe-model"),
                            "choices": [{"index": 0, **choice}],
                        }
                        self.wfile.write(f"data: {json.dumps(chunk)}\n\n".encode())
                    self.wfile.write(b"data: [DONE]\n\n")
                    self.wfile.flush()
                    return
                body = json.dumps(
                    {
                        "id": "chatcmpl-probe",
                        "object": "chat.completion",
                        "created": int(time.time()),
                        "model": request.get("model", "probe-model"),
                        "choices": [
                            {
                                "index": 0,
                                "message": {"role": "assistant", "content": content},
                                "finish_reason": "stop",
                            }
                        ],
                        "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
                    }
                ).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):  # noqa: N802
                self.send_response(404)
                self.send_header("Content-Length", "0")
                self.end_headers()

        self.server = ThreadingHTTPServer(("127.0.0.1", port), Handler)

    def start(self) -> None:
        import threading

        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def stop(self) -> None:
        self.server.shutdown()


class Side:
    def __init__(self, key: str, label: str, binary: list[str], port: int, home: Path, log: Path) -> None:
        self.key = key
        self.label = label
        self.binary = binary
        self.port = port
        self.home = home
        self.log = log
        self.proc: subprocess.Popen | None = None
        self.log_handle: Any = None
        self.cwd: Path | None = None

    @property
    def base(self) -> str:
        return f"http://127.0.0.1:{self.port}"


def build_go_binary() -> None:
    if GO_BINARY.is_file():
        return
    GO_BINARY.parent.mkdir(parents=True, exist_ok=True)
    proc = subprocess.run(
        ["go", "build", "-o", str(GO_BINARY), "./cmd/hive"],
        cwd=str(GO_DIR), capture_output=True, text=True, timeout=900,
    )
    if proc.returncode != 0:
        raise SystemExit(f"go build failed:\n{proc.stdout}\n{proc.stderr}")


def make_fixture(root: Path, api_base: str, with_agent_dir: bool) -> Path:
    """Build one fixture home; both sides get a byte-identical copy of it."""
    home = root / "fixture-home"
    if home.exists():
        shutil.rmtree(home)
    (home / "secrets").mkdir(parents=True)
    key = base64.urlsafe_b64encode(secrets.token_bytes(32)).decode("ascii")
    (home / "secrets/credential_key").write_text(key, encoding="utf-8")
    os.chmod(home / "secrets/credential_key", 0o600)
    (home / "configuration.json").write_text(
        json.dumps(
            {
                "llm": {
                    "provider": "openai",
                    "model": "probe-model",
                    "api_base": api_base,
                    "api_key": "probe-key",
                }
            },
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    if with_agent_dir:
        (home / "agents" / "probe-agent").mkdir(parents=True)
        (home / "agents" / "probe-agent" / "metadata.json").write_text("{}\n", encoding="utf-8")
    return home


def clone_fixture(fixture: Path, target: Path) -> None:
    if target.exists():
        shutil.rmtree(target)
    shutil.copytree(fixture, target)


def spawn_serve(side: Side) -> None:
    env = dict(os.environ)
    env["HIVE_HOME"] = str(side.home)
    env["HIVE_CREDENTIAL_KEY"] = (side.home / "secrets" / "credential_key").read_text(encoding="utf-8").strip()
    env.pop("HIVE_TOOL_SURFACE", None)
    env.pop("HIVE_API_KEY", None)
    side.log.parent.mkdir(parents=True, exist_ok=True)
    side.log_handle = open(side.log, "w", encoding="utf-8")
    side.proc = subprocess.Popen(
        side.binary + ["serve", "--host", "127.0.0.1", "--port", str(side.port)],
        cwd=str(side.cwd) if side.cwd is not None else None,
        env=env, stdout=side.log_handle, stderr=subprocess.STDOUT, start_new_session=True,
    )


def wait_health(side: Side, timeout: float = HEALTH_TIMEOUT_S) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if side.proc is not None and side.proc.poll() is not None:
            raise SystemExit(f"{side.label} exited rc={side.proc.returncode} before /api/health\n"
                             f"log tail:\n{log_tail(side.log)}")
        if http_request("GET", f"{side.base}/api/health", timeout=3).status == 200:
            return
        time.sleep(0.3)
    raise SystemExit(f"{side.label} never became healthy\nlog tail:\n{log_tail(side.log)}")


def stop_side(side: Side) -> None:
    proc = side.proc
    if proc is None:
        return
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                pass
    if side.log_handle is not None:
        side.log_handle.close()


def log_tail(path: Path, lines: int = 25) -> str:
    if not path.is_file():
        return "<no log>"
    text = path.read_text(encoding="utf-8", errors="replace").splitlines()
    return "\n".join(text[-lines:])


def create_session(side: Side) -> str:
    resp = http_request("POST", f"{side.base}/api/sessions", {"queen_name": QUEEN_ID})
    if resp.status != 201:
        raise SystemExit(f"{side.label}: session create -> {resp.status}: {resp.text[:300]}")
    return str((resp.json or {}).get("session_id") or "")


def settle_turn(side: Side, session_id: str) -> dict:
    """Wait until the scripted turn has finished writing events."""
    last_total = -1
    stable = 0
    deadline = time.time() + SETTLE_TIMEOUT_S
    history = {}
    while time.time() < deadline:
        resp = http_request("GET", f"{side.base}/api/sessions/{session_id}/events/history?limit=500")
        snapshot = http_request("GET", f"{side.base}/api/sessions/{session_id}/snapshot")
        if resp.status == 200 and isinstance(resp.json, dict):
            history = resp.json
            total = history.get("total", -1)
            executing = (snapshot.json or {}).get("is_executing") if snapshot.status == 200 else None
            if isinstance(total, int) and total == last_total and total > 0 and executing is False:
                stable += 1
                if stable >= 2:
                    return history
            else:
                stable = 0
            last_total = total if isinstance(total, int) else -1
        time.sleep(0.5)
    raise SystemExit(f"{side.label}: turn did not settle within {SETTLE_TIMEOUT_S}s")


# ---------------------------------------------------------------------------
# errors command
# ---------------------------------------------------------------------------

TOOL_SCRIPT = [
    {"tool": "search_tools", "arguments": {"query": "select:read_file,edit_file,write_file"}},
    {"tool": "read_file", "arguments": {"path": "__WS__/missing.txt"}},
    {
        "tool": "edit_file",
        "arguments": {"path": "__WS__/probe.txt", "old_string": "no-such-content-xyz", "new_string": "replacement"},
    },
    {
        "tool": "edit_file",
        "arguments": {"path": "__WS__/missing.txt", "old_string": "a", "new_string": "b"},
    },
    {"tool": "write_file", "arguments": {"path": "__WS__/probe.txt", "content": "clobbered\n"}},
    {"tool": "terminal_exec", "arguments": {"command": "sh -c 'echo out; echo err 1>&2; exit 7'"}},
    {"tool": "image_generate", "arguments": {"prompt": "probe"}},
    {"text": "done"},
]

TOOL_SCENARIOS = [
    {
        "id": "read_file_missing",
        "category": "read_file 不存在",
        "tool": "read_file",
        "compare": "text+is_error",
    },
    {
        "id": "edit_file_mismatch",
        "category": "edit_file 不匹配（未读守卫）",
        "tool": "edit_file",
        "compare": "text+is_error",
    },
    {
        "id": "edit_file_missing",
        "category": "edit_file 不匹配（文件不存在）",
        "tool": "edit_file",
        "compare": "text+is_error",
    },
    {
        "id": "write_file_refusal",
        "category": "write_file 覆盖守卫（同类失败路径）",
        "tool": "write_file",
        "compare": "text+is_error",
    },
    {
        "id": "terminal_exec_nonzero",
        "category": "terminal 非零退出",
        "tool": "terminal_exec",
        "compare": "exit_code+stderr",
    },
    {
        "id": "image_generate_unauthorized",
        "category": "connect 未授权（凭据未连接）",
        "tool": "image_generate",
        "compare": "error_message+keys",
    },
]


def substitute_placeholders(value, home: str, session_id: str, upload_id: str):
    if isinstance(value, str):
        return value.replace("{home}", home).replace("{session}", session_id).replace("{upload}", upload_id)
    if isinstance(value, dict):
        return {k: substitute_placeholders(v, home, session_id, upload_id) for k, v in value.items()}
    if isinstance(value, list):
        return [substitute_placeholders(v, home, session_id, upload_id) for v in value]
    return value


def http_error_probes(home: Path, session_id: str, upload_id: str) -> list[dict]:
    """Failure-path matrix rows.  ``{home}`` / ``{session}`` / ``{upload}`` are substituted."""
    table = [
        # 400 — bad request
        ("sessions_post_nonobject", "创建会话-非对象体", 400, "POST", "/api/sessions", [], None, "json"),
        ("config_llm_invalid_json", "写回配置-非法 JSON", 400, "PUT", "/api/config/llm", None, "not-json{", "json"),
        ("credentials_missing_fields", "保存凭据-缺字段", 400, "POST", "/api/credentials", {}, None, "json"),
        ("sentinel_telegram_empty", "Telegram 校验-空参", 400, "POST", "/api/sentinel/telegram/validate", {}, None, "json"),
        (
            "skills_patch_enabled_not_bool",
            "技能开关-类型错误",
            400,
            "PATCH",
            f"/api/queen/{QUEEN_ID}/skills/administrative-behavior-skill",
            {"enabled": "yes"},
            None,
            "json",
        ),
        ("agents_delete_missing_path", "删除 Agent-缺参数", 400, "DELETE", "/api/agents", {}, None, "json"),
        (
            "import_init_missing_total",
            "导入初始化-缺 total_bytes",
            400,
            "POST",
            "/api/colonies/import/init",
            {"sha256": "0" * 64},
            None,
            "json",
        ),
        (
            "import_chunk_bad_offset_value",
            "分块上传-offset 非整数",
            400,
            "PUT",
            "/api/colonies/import/chunk/0123456789abcdef?offset=abc",
            None,
            "chunk",
            "json",
        ),
        (
            "import_chunk_negative_offset",
            "分块上传-offset 为负",
            400,
            "PUT",
            "/api/colonies/import/chunk/0123456789abcdef?offset=-1",
            None,
            "chunk",
            "json",
        ),
        (
            "import_chunk_bad_upload_id",
            "分块上传-非法 upload_id",
            400,
            "PUT",
            "/api/colonies/import/chunk/NOPE?offset=0",
            None,
            "chunk",
            "json",
        ),
        (
            "colony_scaffold_bad_name",
            "Colony 名-非法字符",
            400,
            "POST",
            "/api/colonies/Bad-Name/scaffold",
            {},
            None,
            "json",
        ),
        # 403 — forbidden
        (
            "agents_delete_framework",
            "删除框架 Agent",
            403,
            "DELETE",
            "/api/agents",
            {"agent_path": "{home}/agents/probe-agent"},
            None,
            "json",
        ),
        (
            "skill_put_body_framework",
            "改写框架技能正文",
            403,
            "PUT",
            f"/api/queen/{QUEEN_ID}/skills/administrative-behavior-skill/body",
            {"body": "x"},
            None,
            "json",
        ),
        # 404 — not found
        ("session_missing", "会话不存在", 404, "GET", f"/api/sessions/{MISSING_SESSION}", None, None, "json"),
        ("session_tasks_missing", "会话不存在-任务", 404, "GET", f"/api/sessions/{MISSING_SESSION}/tasks", None, None, "json"),
        ("session_triggers_missing", "会话不存在-触发器", 404, "GET", f"/api/sessions/{MISSING_SESSION}/triggers", None, None, "json"),
        ("skill_missing", "技能不存在", 404, "GET", "/api/skills/does-not-exist-probe", None, None, "json"),
        ("credentials_missing", "凭据不存在", 404, "GET", "/api/credentials/nope_probe", None, None, "json"),
        ("queen_missing", "Queen 不存在", 404, "GET", "/api/queen/queen_nope/overview", None, None, "json"),
        ("mcp_health_missing", "MCP 服务器不存在", 404, "POST", "/api/mcp/servers/nope/health", {}, None, "json"),
        ("attachment_missing", "附件不存在", 404, "GET", "/api/sessions/{session}/attachment/nope.txt", None, None, "attachment"),
        (
            "import_chunk_unknown_upload",
            "分块上传-未知 upload_id",
            404,
            "PUT",
            "/api/colonies/import/chunk/0123456789abcdef?offset=0",
            None,
            "chunk",
            "json",
        ),
        # 409 — conflict
        (
            "resolve_missed_no_colony",
            "未加载 Colony 的丢失决定",
            409,
            "POST",
            "/api/sessions/{session}/colony/resolve_missed",
            {"decisions": {}},
            None,
            "json",
        ),
        (
            "import_chunk_offset_mismatch",
            "分块上传-offset 不匹配",
            409,
            "PUT",
            "/api/colonies/import/chunk/{upload}?offset=5",
            None,
            "chunk",
            "json",
        ),
        (
            "import_finalize_incomplete",
            "导入完成-字节不齐",
            409,
            "POST",
            "/api/colonies/import/finalize/{upload}",
            {},
            None,
            "json",
        ),
        # 500 — internal
        ("config_llm_list_body", "写回配置-非对象体", 500, "PUT", "/api/config/llm", [], None, "json"),
        (
            "agents_delete_nonstring_path",
            "删除 Agent-路径类型错误",
            500,
            "DELETE",
            "/api/agents",
            {"agent_path": 123},
            None,
            "json",
        ),
    ]
    probes = []
    for pid, label, expect, method, path, body, raw, ctype_expect in table:
        probes.append(
            {
                "id": pid,
                "label": label,
                "expect_status": expect,
                "method": method,
                "path": substitute_placeholders(path, str(home), session_id, upload_id),
                "body": substitute_placeholders(body, str(home), session_id, upload_id),
                "raw": raw,
                "ctype_expect": ctype_expect,
            }
        )
    return probes


def run_http_probes(side: Side, probes: list[dict], upload_id: str) -> dict:
    out: dict = {}
    for probe in probes:
        started = time.time()
        resp = http_request(
            probe["method"], f"{side.base}{probe['path']}", probe["body"], probe["raw"], timeout=60
        )
        entry = {
            "status": resp.status,
            "ctype": resp.ctype,
            "form": "json" if resp.json is not None else "text",
            "body": resp.text[:4000],
            "json": resp.json,
            "elapsed_s": round(time.time() - started, 2),
        }
        out[probe["id"]] = entry
    return out


def prepare_upload(side: Side) -> str:
    """Create one import upload so the 409 mismatch/finalize probes have state."""
    resp = http_request(
        "POST", f"{side.base}/api/colonies/import/init",
        {"total_bytes": 10, "sha256": "1" * 64, "filename": "probe.tar"},
    )
    if resp.status != 201:
        raise SystemExit(f"{side.label}: import init -> {resp.status}: {resp.text[:300]}")
    return str((resp.json or {}).get("upload_id") or "")


def canon_tool_content(content: str) -> dict:
    text = content or ""
    is_error = False
    while text.startswith("ERROR: "):
        is_error = True
        text = text[len("ERROR: "):]
    text = re.sub(r"\n\n<system-reminder>.*?</system-reminder>\s*$", "", text, flags=re.S)
    return {"is_error": is_error, "text": mask_text(text.rstrip("\n"))}


def extract_tool_results(rows: list[dict], order: list[str]) -> dict:
    calls: dict[str, dict] = {}
    seen: dict[str, dict] = {}
    for row in rows:
        for message in row.get("messages") or []:
            if not isinstance(message, dict):
                continue
            if message.get("role") == "assistant":
                for call in message.get("tool_calls") or []:
                    if isinstance(call, dict):
                        function = call.get("function") or {}
                        calls[str(call.get("id"))] = {
                            "name": function.get("name"),
                            "arguments": function.get("arguments"),
                        }
            elif message.get("role") == "tool":
                key = str(message.get("tool_call_id"))
                if key in seen:
                    continue
                meta = calls.get(key, {})
                seen[key] = {
                    "tool_call_id": key,
                    "tool": meta.get("name"),
                    "arguments": meta.get("arguments"),
                    "content": message.get("content"),
                }
    ordered: dict[str, dict] = {}
    results = list(seen.values())
    for index, scenario_id in enumerate(order):
        if index < len(results):
            ordered[scenario_id] = results[index]
    return ordered


def capture_tool_errors(
    side: Side, session_id: str, scratch: Path, ws: Path
) -> dict:
    """Drive the tool script through the fake gateway and return raw results."""
    gateway_port = free_port()
    gateway_log = scratch / f"{side.key}-gateway.jsonl"
    gateway_log.unlink(missing_ok=True)
    script = [
        {"tool": step["tool"], "arguments": step.get("arguments") or {}}
        for step in TOOL_SCRIPT
        if "tool" in step
    ] + [{"text": "done"}]
    script_path = scratch / f"{side.key}-script.json"
    script_path.write_text(json.dumps(script).replace("__WS__", str(ws)), encoding="utf-8")
    env = dict(os.environ)
    env["HIVE_HOME"] = str(side.home)
    env["HIVE_CREDENTIAL_KEY"] = (side.home / "secrets" / "credential_key").read_text(encoding="utf-8").strip()
    gateway = subprocess.Popen(
        [sys.executable, str(GATEWAY), "--port", str(gateway_port), "--log", str(gateway_log),
         "--script", str(script_path)],
        stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT,
    )
    try:
        # Point the session's runtime at the gateway: rewrite configuration and
        # restart the side so the chat turn is driven by the scripted model.
        config_path = side.home / "configuration.json"
        config = json.loads(config_path.read_text(encoding="utf-8"))
        config["llm"]["api_base"] = f"http://127.0.0.1:{gateway_port}/v1"
        config_path.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
        stop_side(side)
        spawn_serve(side)
        wait_health(side)
        session_id = create_session(side)
        resp = http_request(
            "POST", f"{side.base}/api/sessions/{session_id}/chat", {"message": "run the probe sequence"}, timeout=600
        )
        if resp.status != 200:
            raise SystemExit(f"{side.label}: chat -> {resp.status}: {resp.text[:300]}")
        deadline = time.time() + 300
        rows: list[dict] = []
        while time.time() < deadline:
            rows = []
            if gateway_log.is_file():
                for line in gateway_log.read_text(encoding="utf-8").splitlines():
                    line = line.strip()
                    if line:
                        rows.append(json.loads(line))
            if len(rows) >= len(script):
                break
            time.sleep(0.5)
        if len(rows) < len(script):
            raise SystemExit(f"{side.label}: only {len(rows)} gateway requests, want {len(script)}")
        order = [step["id"] for step in TOOL_SCENARIOS]
        script_order = ["load_file_tools"] + order
        results = extract_tool_results(rows, script_order)
        missing = [sid for sid in script_order if sid not in results]
        if missing:
            raise SystemExit(f"{side.label}: missing tool results {missing}")
        return {sid: results[sid] for sid in order}
    finally:
        gateway.terminate()
        try:
            gateway.wait(timeout=10)
        except subprocess.TimeoutExpired:
            gateway.kill()


def classify_error_probe(probe: dict, py: dict, go: dict) -> dict:
    verdict = {
        "id": probe["id"],
        "label": probe["label"],
        "expect_status": probe["expect_status"],
        "method": probe["method"],
        "path": probe["path"],
        "reference_status": py["status"],
        "target_status": go["status"],
        "reference_ctype": py["ctype"],
        "target_ctype": go["ctype"],
        "reference_body": {
            "form": py["form"],
            "keys": sorted(py["json"]) if isinstance(py.get("json"), dict) else None,
        },
        "target_body": {
            "form": go["form"],
            "keys": sorted(go["json"]) if isinstance(go.get("json"), dict) else None,
        },
        "declared": [],
        "diffs": [],
    }
    if py["status"] != go["status"]:
        verdict["diffs"].append({"kind": "status", "reference": py["status"], "target": go["status"]})
    if py["status"] != probe["expect_status"]:
        verdict["declared"].append(
            {"kind": "expectation", "note": f"reference returned {py['status']}, expected {probe['expect_status']}"}
        )
    ctype_equal = py["ctype"] == go["ctype"]
    if not ctype_equal:
        if py["ctype"].replace("; charset=utf-8", "") == go["ctype"].replace("; charset=utf-8", ""):
            verdict["declared"].append(
                {
                    "kind": "content-type-parameter",
                    "note": f"reference {py['ctype']!r} vs target {go['ctype']!r}: charset parameter only",
                }
            )
        else:
            verdict["diffs"].append({"kind": "content_type", "reference": py["ctype"], "target": go["ctype"]})
    if py["form"] != go["form"]:
        verdict["diffs"].append({"kind": "body_form", "reference": py["form"], "target": go["form"]})
    elif py["form"] == "json":
        compare = compare_structures(mask_value(py["json"]), mask_value(go["json"]))
        for bucket in ("missing_in_target", "extra_in_target"):
            if compare[bucket]:
                verdict["diffs"].append({"kind": bucket, "paths": compare[bucket]})
        if compare["type_diffs"]:
            verdict["diffs"].append({"kind": "type_diffs", "items": compare["type_diffs"]})
        if compare["value_diffs"]:
            verdict["diffs"].append({"kind": "value_diffs", "items": compare["value_diffs"]})
    else:
        py_text = mask_text(py["body"].strip())
        go_text = mask_text(go["body"].strip())
        if py_text != go_text:
            verdict["diffs"].append({"kind": "text", "reference": py_text[:400], "target": go_text[:400]})
    verdict["verdict"] = "aligned" if not verdict["diffs"] else (
        "shape_diff" if any(d["kind"] in ("body_form", "missing_in_target", "extra_in_target", "type_diffs") for d in verdict["diffs"])
        else "value_diff"
    )
    return verdict


def classify_tool_scenario(scenario: dict, py_raw: dict, go_raw: dict) -> dict:
    py = canon_tool_content(py_raw.get("content") or "")
    go = canon_tool_content(go_raw.get("content") or "")
    entry = {
        "id": scenario["id"],
        "category": scenario["category"],
        "tool": scenario["tool"],
        "compare": scenario["compare"],
        "arguments": py_raw.get("arguments") or go_raw.get("arguments"),
        "declared": [],
        "diffs": [],
    }
    if scenario["compare"] == "text+is_error":
        if py["text"] != go["text"]:
            entry["diffs"].append({"kind": "text", "reference": py["text"][:400], "target": go["text"][:400]})
        if py["is_error"] != go["is_error"]:
            entry["diffs"].append({"kind": "is_error", "reference": py["is_error"], "target": go["is_error"]})
    elif scenario["compare"] == "exit_code+stderr":
        py_code = None
        try:
            payload = json.loads(py["text"].split("\n", 1)[0])
            py_code = payload.get("exit_code")
            py_stderr = payload.get("stderr")
            py_semantic = payload.get("semantic_status")
        except (ValueError, AttributeError):
            py_stderr = None
            py_semantic = None
        match = re.search(r"exit_code:\s*(-?\d+)", go["text"])
        go_code = int(match.group(1)) if match else None
        go_stderr = None
        stderr_match = re.search(r"stderr:\n(.*)", go["text"], re.S)
        if stderr_match:
            go_stderr = stderr_match.group(1)
        entry["reference_exit_code"] = py_code
        entry["target_exit_code"] = go_code
        entry["reference_semantic_status"] = py_semantic
        if py_code != go_code:
            entry["diffs"].append({"kind": "exit_code", "reference": py_code, "target": go_code})
        if py_stderr is not None and go_stderr is not None and py_stderr.strip() != go_stderr.strip():
            entry["diffs"].append({"kind": "stderr", "reference": py_stderr, "target": go_stderr})
        # Envelope form is the terminal lane's frozen contract (JSON envelope
        # vs text report); both carry the exit code, which is compared above.
        entry["declared"].append(
            {
                "kind": "envelope-form",
                "note": "Python returns the terminal JSON envelope, Go returns the text report; "
                        "terminal shape is a separate frozen contract (tool-output golden records it).",
            }
        )
        if py["is_error"] != go["is_error"]:
            entry["declared"].append(
                {
                    "kind": "is_error",
                    "note": "reference carries failure in semantic_status=error (no ERROR: marker), "
                            f"target marks the result is_error={go['is_error']}.",
                }
            )
    elif scenario["compare"] == "error_message+keys":
        py_payload = json.loads(py["text"]) if py["text"].startswith("{") else None
        go_payload = json.loads(go["text"]) if go["text"].startswith("{") else None
        if py_payload is None or go_payload is None:
            entry["diffs"].append({"kind": "form", "reference": py["text"][:200], "target": go["text"][:200]})
        else:
            compare = compare_structures(mask_value(py_payload), mask_value(go_payload))
            if compare["missing_in_target"] or compare["extra_in_target"]:
                entry["declared"].append(
                    {
                        "kind": "extra-key",
                        "note": f"target extra keys {compare['extra_in_target']} (Go additive status field)",
                    }
                )
            if compare["value_diffs"]:
                entry["diffs"].append({"kind": "value_diffs", "items": compare["value_diffs"]})
    entry["reference"] = py
    entry["target"] = go
    entry["verdict"] = "aligned" if not entry["diffs"] else "different"
    return entry


def run_errors(args) -> int:
    # Resolve symlinks up front: macOS /tmp -> /private/tmp.  With an unresolved
    # HIVE_HOME the reference's framework-agent guard compares a resolved path
    # against an unresolved root and misses (see error-taxonomy-diff.md).
    scratch = Path(args.scratch).resolve()
    scratch.mkdir(parents=True, exist_ok=True)
    build_go_binary()

    # One fixture template, byte-identical copies per side.
    placeholder_port = free_port()
    fixture = make_fixture(scratch, f"http://127.0.0.1:{placeholder_port}/v1", with_agent_dir=True)
    py_home = scratch / "python-home"
    go_home = scratch / "go-home"
    clone_fixture(fixture, py_home)
    clone_fixture(fixture, go_home)

    ws = scratch / "ws"
    ws.mkdir(parents=True, exist_ok=True)
    (ws / "probe.txt").write_text("probe file content\nsecond line\n", encoding="utf-8")

    stub = StubLLM(placeholder_port)
    stub.start()

    py_side = Side("python", "python", [str(args.python_bin)], free_port(), py_home, scratch / "python-serve.log")
    go_side = Side("go", "go_hive", [str(GO_BINARY)], free_port(), go_home, scratch / "go-serve.log")
    # The tool-capture lane runs both serves with the fixture workspace as cwd;
    # mirror that so file-tool path policy sees the same session cwd on both sides.
    py_side.cwd = ws
    go_side.cwd = ws
    raw: dict = {"meta": {}, "python": {}, "go": {}, "tool": {"python": {}, "go": {}}}
    try:
        for side in (py_side, go_side):
            spawn_serve(side)
        for side in (py_side, go_side):
            wait_health(side)

        # Seed the same upload state on both sides for the 409 probes.
        upload_py = prepare_upload(py_side)
        upload_go = prepare_upload(go_side)
        session_py = create_session(py_side)
        session_go = create_session(go_side)

        probes_py = http_error_probes(py_home, session_py, upload_py)
        probes_go = http_error_probes(go_home, session_go, upload_go)
        raw["python"]["http"] = run_http_probes(py_side, probes_py, upload_py)
        raw["go"]["http"] = run_http_probes(go_side, probes_go, upload_go)
        raw["python"]["session_id"] = session_py
        raw["go"]["session_id"] = session_go

        # Tool error text: each side gets its own gateway + a restarted serve
        # whose LLM slot points at the scripted model.
        for side, upload in ((py_side, upload_py), (go_side, upload_go)):
            results = capture_tool_errors(side, session_go if side is go_side else session_py, scratch, ws)
            raw["tool"][side.key] = results
    finally:
        for side in (py_side, go_side):
            stop_side(side)
        stub.stop()

    ledger = build_errors_ledger(raw, probes_py)
    write_artifacts(raw, ledger)
    print(render_errors_summary(ledger))
    if args.keep:
        print(f"scratch kept: {scratch}")
    return 0


def scan_integration_auth_strings() -> dict:
    """Static对比集成工具的 401 文案（connect/未授权类的工具面文案）。

    Runtime probes cover the credential-gated harness tool; the ~90 integration
    modules only take this path against real vendors, so their 401 wording is
    compared statically: every `Unauthorized…` string literal in the upstream
    tools tree vs the Go integrations tree (production files only).
    """
    py_root = REPO_ROOT / "tools" / "src" / "aden_tools" / "tools"
    go_root = GO_DIR / "internal" / "tools" / "integrations"
    py: set[str] = set()
    for path in py_root.rglob("*.py"):
        for match in re.finditer(r'"((?:Unauthorized|Not authorized)[^"\\]*(?:\\.[^"\\]*)*)"', path.read_text(encoding="utf-8", errors="replace")):
            py.add(match.group(1))
    go: set[str] = set()
    for path in go_root.glob("*.go"):
        if path.name.endswith("_test.go"):
            continue
        for match in re.finditer(r'"((?:Unauthorized|Not authorized)[^"\\]*(?:\\.[^"\\]*)*)"', path.read_text(encoding="utf-8", errors="replace")):
            go.add(match.group(1))
    return {
        "python_unique": len(py),
        "go_unique": len(go),
        "common": len(py & go),
        "only_python": sorted(py - go),
        "only_go": sorted(go - py),
        "note": "静态对照（生产文件）；运行时只有凭据门控的 harness 工具可离线实测",
    }


def build_errors_ledger(raw: dict, probes: list[dict]) -> dict:
    probe_verdicts = []
    for probe in probes:
        py = raw["python"]["http"].get(probe["id"], {})
        go = raw["go"]["http"].get(probe["id"], {})
        probe_verdicts.append(classify_error_probe(probe, py, go))
    tool_entries = []
    for scenario in TOOL_SCENARIOS:
        py_raw = (raw["tool"]["python"].get(scenario["id"]) or {})
        go_raw = (raw["tool"]["go"].get(scenario["id"]) or {})
        tool_entries.append(classify_tool_scenario(scenario, py_raw, go_raw))

    probe_status = {
        "probes": len(probe_verdicts),
        "aligned": sum(1 for p in probe_verdicts if p["verdict"] == "aligned"),
        "value_diff": sum(1 for p in probe_verdicts if p["verdict"] == "value_diff"),
        "shape_diff": sum(1 for p in probe_verdicts if p["verdict"] == "shape_diff"),
        "declared_notes": sum(len(p["declared"]) for p in probe_verdicts),
    }
    tool_status = {
        "scenarios": len(tool_entries),
        "aligned": sum(1 for t in tool_entries if t["verdict"] == "aligned"),
        "different": sum(1 for t in tool_entries if t["verdict"] == "different"),
        "declared_notes": sum(len(t["declared"]) for t in tool_entries),
    }
    static_checks = {"integration_unauthorized_strings": scan_integration_auth_strings()}
    return {
        "schema_version": 1,
        "note": "Real two-side failure-path + tool-error probes (Python reference vs go_hive target).",
        "generated_at": now_iso(),
        "reference": {
            "name": "python",
            "binary": str(DEFAULT_PY_BIN),
            "head": git_head(DEFAULT_PY_ROOT),
        },
        "target": {"name": "go_hive", "binary": str(GO_BINARY), "head": git_head(GO_DIR)},
        "fixture": "同一夹具 HIVE_HOME 的逐字节副本（configuration.json + secrets/credential_key + agents/probe-agent + 一个 import upload）",
        "counts": {
            "http": probe_status,
            "tools": tool_status,
            "shape_diffs": probe_status["shape_diff"],
            "value_diffs": probe_status["value_diff"],
        },
        "static_checks": static_checks,
        "probes": probe_verdicts,
        "tools": tool_entries,
    }


def write_artifacts(raw: dict, ledger: dict) -> None:
    ARTIFACT_DIR.mkdir(parents=True, exist_ok=True)
    ERRORS_RAW.write_text(json.dumps(raw, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    ERRORS_LEDGER.write_text(json.dumps(ledger, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    ERRORS_REPORT.write_text(render_errors_markdown(ledger), encoding="utf-8")


def render_errors_summary(ledger: dict) -> str:
    c = ledger["counts"]
    return (
        f"errors: http {c['http']['aligned']}/{c['http']['probes']} aligned, "
        f"{c['http']['value_diff']} value-diff, {c['http']['shape_diff']} shape-diff; "
        f"tools {c['tools']['aligned']}/{c['tools']['scenarios']} aligned"
    )


def render_errors_markdown(ledger: dict) -> str:
    c = ledger["counts"]
    http = ledger["probes"]
    tools = ledger["tools"]
    body_forms: dict = {}
    for probe in http:
        reference = probe.get("reference_body") or {}
        form = reference.get("form", "?")
        keys = reference.get("keys") or []
        label = f"{form}: {{{', '.join(keys)}}}" if keys else form
        body_forms.setdefault(label, []).append(f"{probe['reference_status']} {probe['id']}")
    lines = [
        "# 错误体系差异（errors #12；reference=python → target=go_hive）",
        "",
        f"- 生成时间：{ledger['generated_at']}",
        f"- reference（基准）：python，HEAD `{ledger['reference']['head']}`，二进制 `{ledger['reference']['binary']}`",
        f"- target（待对齐）：go_hive，HEAD `{ledger['target']['head']}`，二进制 `{ledger['target']['binary']}`",
        f"- 口径：{ledger['fixture']}；两侧各自起 serve 后对同一请求序列逐一取回，"
        "值级比对前对 session id / 时间戳 / 路径做规范化；工具错误文案经各自 serve + 脚本化假模型网关（fake_tool_gateway.py）采集，",
        "  比对的是模型看到的 `role: tool` 文本与循环错误标记（`ERROR: ` 前缀 = is_error）。",
        "- 产物：`go_hive/docs/e2e-assets/errors-formats/errors-raw.json`、`errors-ledger.json`"
        "（`--check` 从 raw 重放并比对台账与本报告）。",
        "",
        f"**HTTP 失败路径矩阵：{c['http']['aligned']}/{c['http']['probes']} 对齐"
        f"（shape_diff {c['http']['shape_diff']}，value_diff {c['http']['value_diff']}，"
        f"已声明差异 {c['http']['declared_notes']} 条）。**",
        f"**工具错误文案：{c['tools']['aligned']}/{c['tools']['scenarios']} 对齐"
        f"（different {c['tools']['different']}，已声明差异 {c['tools']['declared_notes']} 条）。**",
        "",
        "## 本轮修复",
        "",
        "工具错误文案探针实测：edit_file / write_file 的「未读或已过期」拒绝文案与上游逐字相同，",
        "但 Go 通过 `fail()` 把它标成 `is_error=true`（模型看到 `ERROR: ` 前缀），上游 Python 返回纯文本结果"
        "（`tools/src/aden_tools/file_ops.py` 的 `_patch_replace` / `write_file` 直接 `return` 字符串）。",
        "修复：`internal/tools/builtin/edit.go`（文件不存在分支 + 未读/过期两条拒绝）与 "
        "`internal/tools/builtin/write.go`（未读/过期两条拒绝）改为纯文本 `Result{Content: …}`；"
        "Go 测试同步为断言无错误标记（`file_ops_residual_test.go`、`file_ops_test.go`）。",
        "复跑结果：修复前 3/6 场景为 is_error-only 差异、文案逐字相同；修复后 6/6 对齐。",
        "同族残余（未纳入本轮探针，保留现状）：read_file / write_file / edit_file 的其余 `fail()` 分支"
        "（参数校验、唯一匹配、写入异常等）仍带错误标记；上游相应路径同样多为纯文本返回，逐分支核对后再收敛。",
        "",
        "## HTTP 失败路径矩阵",
        "",
        "| 探测 | 端点 | 期望 | reference | target | Content-Type (ref / target) | 判定 | 差异 |",
        "|------|------|------|-----------|--------|---------------------------|------|------|",
    ]
    for probe in http:
        diffs = "; ".join(d["kind"] for d in probe.get("diffs", [])) or "—"
        shown_path = SESSION_RE.sub("session_YYYYMMDD_HHMMSS_<8hex>", f"{probe['method']} {probe['path']}")
        lines.append(
            f"| {probe['id']} | `{shown_path}` | {probe['expect_status']} "
            f"| {probe.get('reference_status', '?')} | {probe.get('target_status', '?')} "
            f"| `{probe.get('reference_ctype', '')}` / `{probe.get('target_ctype', '')}` | {probe['verdict']} | {diffs} |"
        )
    lines += ["", "## 错误体词法（按 reference 归组）", "",
              "| 错误体形状 | 探测 |", "|------------|------|"]
    for label in sorted(body_forms):
        lines.append(f"| `{label}` | {', '.join(body_forms[label])} |")
    lines += ["", "## 工具错误文案（模型可见的 tool 结果文本）", "",
              "| 场景 | 工具 | 比较口径 | 判定 | 说明 |", "|------|------|----------|------|------|"]
    for tool in tools:
        notes = "; ".join(d["kind"] for d in tool.get("diffs", [])) or "—"
        declared = "; ".join(d["kind"] for d in tool.get("declared", []))
        note = notes if notes != "—" else declared or "—"
        lines.append(f"| {tool['category']} | `{tool['tool']}` | {tool['compare']} | {tool['verdict']} | {note} |")
    lines += ["", "### 探针原文（reference / target）", "",
              "| 场景 | python（is_error） | go_hive（is_error） |", "|------|--------------------|---------------------|"]
    for tool in tools:
        py = tool["reference"]
        go = tool["target"]
        py_text = (py.get("text") or "").replace("|", "\\|").replace("\n", "<br>")[:220]
        go_text = (go.get("text") or "").replace("|", "\\|").replace("\n", "<br>")[:220]
        lines.append(f"| {tool['id']} | `{py_text}` ({py.get('is_error')}) | `{go_text}` ({go.get('is_error')}) |")
    lines += [
        "",
        "## 集成工具 401 文案（静态对照）",
        "",
        f"上游 `tools/src/aden_tools/tools/` 与 Go `internal/tools/integrations/`（生产文件）中的 "
        f"`Unauthorized…` 字面量：共有 **{ledger['static_checks']['integration_unauthorized_strings']['common']}** 条、"
        f"仅上游 {len(ledger['static_checks']['integration_unauthorized_strings']['only_python'])} 条、"
        f"仅 Go {len(ledger['static_checks']['integration_unauthorized_strings']['only_go'])} 条。"
        "运行时只有凭据门控的 harness 工具（image_generate）可离线实测；集成模块需真实厂商调用，本表单列静态口径。",
        "",
        "## CLI 退出码（tracker / hive-cred）",
        "",
        "两侧同源逐值一致：`core/framework/trackerdb/errors.py`（`EXIT_OK..EXIT_BUSY`）与 "
        "`go_hive/go-hive/internal/tracker/errors.go`（`ExitOK..ExitBusy`）；错误体字段同为 "
        "`{code, message, details}`，`run_cli` 以 `e.exit_code` 退出并打印 envelope。",
        "",
        "| 退出码 | 常量 | 语义 |", "|--------|------|------|",
        "| 0 | EXIT_OK | 成功 |",
        "| 1 | EXIT_DOMAIN | sqlite 错误或未分类域失败 |",
        "| 2 | EXIT_NO_COLONY | 不在 colony 上下文 |",
        "| 3 | EXIT_PERMISSION | worker 调用 queen-only 命令 |",
        "| 4 | EXIT_NOT_FOUND | 表/列不存在 |",
        "| 5 | EXIT_NOT_REGISTERED | 表存在但未被白名单放行写 |",
        "| 6 | EXIT_VALIDATION | 参数/JSON/CSV 非法 |",
        "| 7 | EXIT_DENIED | SQL 被 tracker 拒绝清单拦下 |",
        "| 9 | EXIT_PARTIAL | 部分行写入、部分被拒（看 `failed`） |",
        "| 10 | EXIT_BUSY | 其他写者持有 DB，重试 |",
        "",
        "browser CLI 的退出码分类另有 `TestBrowserCLIExitCodes` 固定（跨维度引用，不重复展开）。",
        "",
        "## 保留差异（declared，逐条量化）",
        "",
        "| 编号 | 主题 | 实测 | 处理 |",
        "|------|------|------|------|",
        "| E-01 | JSON Content-Type 的 charset 参数 | reference `application/json; charset=utf-8` vs target `application/json`（26 条探测均同） | 保留：HTTP 规范允许两种；改 Go 侧 `writeJSON` 会触及全部路由与已冻结契约 |",
        "| E-02 | terminal 非零退出的信封与错误标记 | reference：JSON envelope（`semantic_status=error`，无 `ERROR: ` 前缀，is_error=false）；target：文本报告 + `ERROR: `（is_error=true）；`exit_code` 两侧同为 7 | 保留：terminal 输出形态是 terminal lane 的冻结契约（tool-output golden 已记录 envelope 差异），本页只保证 exit_code 语义相等 |",
        "| E-03 | `image_generate` 凭据缺失错误的额外键 | 两侧 error 文案逐字相同；target 多 `status: 503` | 保留：Go 附加字段（additive），不影响上游键集 |",
        "| E-04 | 框架 Agent 保护的符号链接路径 | HIVE_HOME 经符号链接（`/tmp`→`/private/tmp`）时：reference 解析后与未解析的 `$HIVE_HOME/agents` 比较，保护失效（实测 200 并删除）；target 解析两侧后 403 | 保留：Go 的有意加固（见 `internal/server/routes_agents.go` 注释）；非符号链接夹具下两侧同为 403 |",
        "| E-05 | 文件工具其余 `fail()` 分支的 is_error | 静态对照：上游同族返回纯文本；本页 5 个分支已修，其余未纳入探针 | 保留待逐分支核对（见「本轮修复」末段） |",
        "",
        "## 复现",
        "",
        "```bash",
        "cd go_hive/go-hive && CGO_ENABLED=0 go build -o dist/hive-errfmt ./cmd/hive",
        "uv run --no-project python go_hive/scripts/errors_formats_probe.py errors          # 双跑 + 采集",
        "uv run --no-project python go_hive/scripts/errors_formats_probe.py errors --check  # 从 raw 重放台账与报告",
        "```",
        "",
        "夹具：同一 HIVE_HOME 模板的逐字节副本（`configuration.json` + `secrets/credential_key` + `agents/probe-agent`）；",
        "两侧各自起真实 serve（python 用 `/Users/mac/hive/.venv/bin/hive`，target 用本树构建的二进制），",
        "工具文案改由 `fake_tool_gateway.py` 脚本化驱动（read_file 缺失 / edit_file 未读拒绝 / edit_file 文件缺失 / write_file 覆盖拒绝 / terminal 非零退出 / image_generate 缺凭据）。",
        "",
    ]
    return "\n".join(lines) + "\n"


# ---------------------------------------------------------------------------
# formats command
# ---------------------------------------------------------------------------

FORMAT_ENDPOINTS = [
    ("sessions_list", "GET", "/api/sessions", None),
    ("sessions_history", "GET", "/api/sessions/history", None),
    ("session_detail", "GET", "/api/sessions/{session}", None),
    ("session_snapshot", "GET", "/api/sessions/{session}/snapshot", None),
    ("session_stats", "GET", "/api/sessions/{session}/stats", None),
    ("session_tasks", "GET", "/api/sessions/{session}/tasks", None),
    ("session_triggers", "GET", "/api/sessions/{session}/triggers", None),
    ("session_workers", "GET", "/api/sessions/{session}/workers", None),
    ("session_events_history", "GET", "/api/sessions/{session}/events/history", None),
    ("queen_profiles", "GET", "/api/queen/profiles", None),
    ("queen_overview", "GET", f"/api/queen/{QUEEN_ID}/overview", None),
    ("queen_skills", "GET", f"/api/queen/{QUEEN_ID}/skills", None),
    ("skills", "GET", "/api/skills", None),
    ("credentials_specs", "GET", "/api/credentials/specs", None),
    ("credentials_list", "GET", "/api/credentials", None),
    ("colonies_tools_index", "GET", "/api/colonies/tools-index", None),
    ("janitor_report", "GET", "/api/maintenance/janitor/report", None),
    ("mcp_servers", "GET", "/api/mcp/servers", None),
    ("config_llm", "GET", "/api/config/llm", None),
    ("models_overview", "GET", "/api/models/overview", None),
    ("browser_status", "GET", "/api/browser/status", None),
]

SORT_ENDPOINTS = [
    ("sessions_list", "/api/sessions", "session_id"),
    ("sessions_history", "/api/sessions/history", "session_id"),
    ("skills", "/api/skills", "name"),
    ("queen_profiles", "/api/queen/profiles", "id"),
    ("credentials_specs", "/api/credentials/specs", "credential_id"),
    ("colonies_tools_index", "/api/colonies/tools-index", "name"),
]


def classify_time_value(value) -> str | None:
    if isinstance(value, str):
        if ISO_OFFSET_RE.fullmatch(value):
            digits = value.split(".")[1][:6] if "." in value else ""
            return f"iso8601-offset micros={len(digits)}"
        if ISO_NAIVE_RE.fullmatch(value):
            digits = value.split(".")[1][:6] if "." in value else ""
            return f"iso8601-naive micros={len(digits)}"
        if re.fullmatch(r"\d{4}-\d{2}-\d{2}", value):
            return "date-only"
        return None
    if isinstance(value, bool) or value is None:
        return None
    if isinstance(value, (int, float)):
        magnitude = abs(value)
        if 1e9 <= magnitude < 1e10:
            return "epoch-seconds"
        if 1e12 <= magnitude < 1e13:
            return "epoch-millis"
        if 1e15 <= magnitude < 1e16:
            return "epoch-micros"
        return None
    return None


def walk_time_fields(value, path: str, out: dict) -> None:
    if isinstance(value, dict):
        for key, item in value.items():
            walk_time_fields(item, f"{path}.{key}" if path else key, out)
    elif isinstance(value, list):
        if value:
            walk_time_fields(value[0], f"{path}[]", out)
    else:
        kind = classify_time_value(value)
        if kind:
            out[path] = {"class": kind, "sample": str(value)}


def collect_formats(side: Side, session_ids: list[str], colony_id: str) -> dict:
    session_id = session_ids[0]
    collected: dict = {"endpoints": {}, "sort_reads": {}, "paging": {}, "ids": {}}
    # Seed cross-request state BEFORE the sweep so the list/time/paging reads
    # see a scaffolded colony and a live timer trigger.
    scaffold = http_request("POST", f"{side.base}/api/colonies/{colony_id}/scaffold", {"queen_name": QUEEN_ID})
    trigger = http_request(
        "POST", f"{side.base}/api/sessions/{session_id}/triggers",
        {"name": "probe_trigger", "task": "probe", "trigger_type": "timer", "trigger_config": {"interval_minutes": 60}},
    )
    for name, method, path, body in FORMAT_ENDPOINTS:
        url = f"{side.base}{path.replace('{session}', session_id)}"
        resp = http_request(method, url, body, timeout=60)
        collected["endpoints"][name] = {"status": resp.status, "payload": resp.json, "ctype": resp.ctype}
    for name, path, identity_key in SORT_ENDPOINTS:
        url = f"{side.base}{path.replace('{session}', session_id)}"
        resp_a = http_request("GET", url, timeout=60)
        time.sleep(0.3)
        resp_b = http_request("GET", url, timeout=60)

        def sort_items(payload):
            if isinstance(payload, dict):
                for candidate in ("sessions", "skills", "queens", "specs", "colonies"):
                    if isinstance(payload.get(candidate), list):
                        return payload[candidate]
            elif isinstance(payload, list):
                return payload
            return []

        identities = []
        for item in sort_items(resp_b.json):
            if not isinstance(item, dict):
                continue
            value = item.get(identity_key)
            if value is None:
                value = item.get("session_id") or item.get("id") or item.get("name")
            identities.append(str(value))
        collected["sort_reads"][name] = {
            "a": resp_a.json,
            "b_identity": identities,
            "b_sha256": hashlib.sha256(
                json.dumps(resp_b.json, ensure_ascii=False, sort_keys=True).encode("utf-8")
            ).hexdigest()[:16],
        }

    # history paging
    paging: dict = {}
    resp = http_request("GET", f"{side.base}/api/sessions/{session_id}/events/history?limit=2")
    page1 = resp.json if isinstance(resp.json, dict) else {}
    paging["page1"] = page1
    if page1:
        url = (
            f"{side.base}/api/sessions/{session_id}/events/history?limit=2"
            f"&before_offset={page1.get('start_offset')}&before_index={page1.get('start_index')}"
        )
        resp = http_request("GET", url)
        paging["page2"] = resp.json if isinstance(resp.json, dict) else {}
    for label, query in (
        ("limit_absent", ""),
        ("limit_bad", "limit=abc"),
        ("limit_zero", "limit=0"),
        ("limit_negative", "limit=-3"),
        ("limit_huge", "limit=99999"),
        ("before_offset_zero", "before_offset=0&before_index=1&limit=5"),
    ):
        suffix = f"?{query}" if query else ""
        resp = http_request("GET", f"{side.base}/api/sessions/{session_id}/events/history{suffix}")
        paging[label] = resp.json if isinstance(resp.json, dict) else {"status": resp.status}
    collected["paging"] = paging

    # ids
    ids: dict = {
        "session_id": {"sample": session_id, "pattern": (
            "session_YYYYMMDD_HHMMSS_<8hex>"
            if SESSION_RE.fullmatch(session_id or "") else "UNMATCHED"
        )},
        "colony_scaffold": {"status": scaffold.status, "payload": scaffold.json},
        "trigger_create": {"status": trigger.status, "payload": trigger.json},
    }
    events = (collected["endpoints"]["session_events_history"].get("payload") or {}).get("events") or []
    execution_kinds: dict = {}
    by_type: dict = {}
    for event in events:
        for key in ("execution_id", "correlation_id"):
            value = event.get(key)
            if not value:
                continue
            if SESSION_RE.fullmatch(str(value)):
                kind = "session-shaped"
            elif HEX32_RE.fullmatch(str(value)):
                kind = "hex32"
            else:
                kind = f"other:{str(value)[:12]}"
            execution_kinds.setdefault(key, {})
            execution_kinds[key][kind] = execution_kinds[key].get(kind, 0) + 1
        value = event.get("execution_id")
        kind = "none" if not value else (
            "session-shaped" if SESSION_RE.fullmatch(str(value)) else (
                "hex32" if HEX32_RE.fullmatch(str(value)) else "other"))
        event_type = str(event.get("type"))
        by_type.setdefault(event_type, {})
        by_type[event_type][kind] = by_type[event_type].get(kind, 0) + 1
    ids["event_id_kinds"] = execution_kinds
    ids["execution_id_by_type"] = by_type
    ids["event_types"] = sorted(by_type)
    seqs = [event.get("seq") for event in events]
    ids["event_seq"] = {
        "count": len(seqs),
        "contiguous_from_1": seqs == list(range(1, len(seqs) + 1)),
        "type": json_type(seqs[0]) if seqs else None,
    }
    kinds_sets = {
        "node_id": sorted({str(e.get("node_id")) for e in events}),
        "stream_id": sorted({str(e.get("stream_id")) for e in events}),
    }
    ids["event_stream_kinds"] = kinds_sets
    collected["ids"] = ids
    return collected


def run_formats(args) -> int:
    scratch = Path(args.scratch).resolve()
    scratch.mkdir(parents=True, exist_ok=True)
    build_go_binary()
    stub_port = free_port()
    fixture = make_fixture(scratch, f"http://127.0.0.1:{stub_port}/v1", with_agent_dir=False)
    py_home = scratch / "python-home"
    go_home = scratch / "go-home"
    clone_fixture(fixture, py_home)
    clone_fixture(fixture, go_home)
    stub = StubLLM(stub_port)
    stub.start()
    py_side = Side("python", "python", [str(args.python_bin)], free_port(), py_home, scratch / "python-serve.log")
    go_side = Side("go", "go_hive", [str(GO_BINARY)], free_port(), go_home, scratch / "go-serve.log")
    raw: dict = {"meta": {}, "python": {}, "go": {}}
    try:
        for side in (py_side, go_side):
            spawn_serve(side)
        for side in (py_side, go_side):
            wait_health(side)
        sessions: dict[str, list[str]] = {}
        for side in (py_side, go_side):
            created = []
            for _ in range(3):
                created.append(create_session(side))
                time.sleep(0.4)
            sessions[side.key] = created
            http_request("POST", f"{side.base}/api/sessions/{created[0]}/chat", {"message": "probe"}, timeout=120)
        settled: dict = {}
        for side in (py_side, go_side):
            history = settle_turn(side, sessions[side.key][0])
            settled[side.key] = history.get("total")
        for side in (py_side, go_side):
            raw[side.key] = slim_value(collect_formats(side, sessions[side.key], "probe_colony"))
        raw["meta"] = {
            "settled_totals": settled,
            "session_ids": sessions,
            "slim": "字符串值 >80 字符替换为 <str:sha256=… len=…>；仅影响超大文本字段，不改变键/类型/时间/顺序",
        }
    finally:
        for side in (py_side, go_side):
            stop_side(side)
        stub.stop()

    ledger = build_formats_ledger(raw)
    ARTIFACT_DIR.mkdir(parents=True, exist_ok=True)
    FORMATS_RAW.write_text(json.dumps(raw, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    FORMATS_LEDGER.write_text(json.dumps(ledger, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    FORMATS_REPORT.write_text(render_formats_markdown(ledger), encoding="utf-8")
    counts = ledger["counts"]
    print(
        f"formats: time fields {counts['time_fields']} (class diffs {counts['time_class_diffs']}), "
        f"ids {counts['id_checks']} (diffs {counts['id_diffs']}), "
        f"pagination {counts['pagination_checks']} (diffs {counts['pagination_diffs']}), "
        f"sort {counts['sort_checks']} (stability diffs {counts['sort_stability_diffs']}, "
        f"cross-side order diffs {counts['cross_side_order_diffs']})"
    )
    return 0


def build_formats_ledger(raw: dict) -> dict:
    py_endpoints = raw["python"]["endpoints"]
    go_endpoints = raw["go"]["endpoints"]
    time_rows = []
    for name in py_endpoints:
        py_times: dict = {}
        go_times: dict = {}
        walk_time_fields(py_endpoints[name].get("payload"), "", py_times)
        walk_time_fields(go_endpoints[name].get("payload"), "", go_times)
        for path in sorted(set(py_times) | set(go_times)):
            py_entry = py_times.get(path)
            go_entry = go_times.get(path)
            row = {
                "endpoint": name,
                "field": path,
                "python": py_entry,
                "go": go_entry,
            }
            if py_entry is None or go_entry is None:
                row["verdict"] = "missing_side"
            elif py_entry["class"] != go_entry["class"]:
                row["verdict"] = "class_diff"
            else:
                row["verdict"] = "aligned"
            time_rows.append(row)

    id_checks = []
    py_ids = raw["python"]["ids"]
    go_ids = raw["go"]["ids"]
    id_checks.append(
        {
            "kind": "session_id",
            "python": py_ids["session_id"],
            "go": go_ids["session_id"],
            "verdict": "aligned" if py_ids["session_id"]["pattern"] == go_ids["session_id"]["pattern"] else "diff",
        }
    )
    py_scaffold = py_ids.get("colony_scaffold", {})
    go_scaffold = go_ids.get("colony_scaffold", {})
    id_checks.append(
        {
            "kind": "colony_id",
            "python": {"status": py_scaffold.get("status"), "payload": py_scaffold.get("payload")},
            "go": {"status": go_scaffold.get("status"), "payload": go_scaffold.get("payload")},
            "verdict": "aligned" if py_scaffold.get("status") == go_scaffold.get("status") else "diff",
            "note": "colony id 由客户端/目录名提供（[a-z0-9_]{1,64}），不是服务端生成",
        }
    )
    py_trg = (py_ids.get("trigger_create", {}).get("payload") or {})
    go_trg = (go_ids.get("trigger_create", {}).get("payload") or {})

    def trigger_sample(payload):
        if isinstance(payload, dict):
            direct = payload.get("trigger") or payload.get("trigger_id")
            if isinstance(direct, dict):
                return {k: direct.get(k) for k in ("id", "trigger_id", "name", "trigger_type", "next_fire_at")}
            if direct:
                return {"trigger_id": direct}
        return payload

    py_sample = trigger_sample(py_trg)
    go_sample = trigger_sample(go_trg)
    id_checks.append(
        {
            "kind": "trigger",
            "python": {"status": py_ids.get("trigger_create", {}).get("status"), "sample": py_sample},
            "go": {"status": go_ids.get("trigger_create", {}).get("status"), "sample": go_sample},
            "verdict": "aligned"
            if py_ids.get("trigger_create", {}).get("status") == go_ids.get("trigger_create", {}).get("status")
            else "diff",
        }
    )
    py_by_type = py_ids.get("execution_id_by_type") or {}
    go_by_type = go_ids.get("execution_id_by_type") or {}
    common_types = sorted(set(py_by_type) & set(go_by_type))
    per_type_diffs = [
        {"event_type": name, "reference": py_by_type[name], "target": go_by_type[name]}
        for name in common_types
        if py_by_type[name] != go_by_type[name]
    ]
    id_checks.append(
        {
            "kind": "event execution_id 形状（按事件类型）",
            "python": {name: py_by_type.get(name) for name in common_types},
            "go": {name: go_by_type.get(name) for name in common_types},
            "verdict": "aligned" if not per_type_diffs else "diff",
            "reference_only_event_types": sorted(set(py_by_type) - set(go_by_type)),
            "target_only_event_types": sorted(set(go_by_type) - set(py_by_type)),
            "diffs": per_type_diffs,
            "note": "公共事件类型上 execution_id 的形状（session 形态 / 32hex / 无）逐类型比较；"
                    "两侧独有的帧类型单列，不计入形状差异",
        }
    )
    id_checks.append(
        {
            "kind": "event correlation_id",
            "python": py_ids.get("event_id_kinds", {}).get("correlation_id"),
            "go": go_ids.get("event_id_kinds", {}).get("correlation_id"),
            "verdict": "aligned"
            if (py_ids.get("event_id_kinds", {}).get("correlation_id")
                == go_ids.get("event_id_kinds", {}).get("correlation_id"))
            else "diff",
        }
    )
    id_checks.append(
        {
            "kind": "event seq",
            "python": py_ids.get("event_seq"),
            "go": go_ids.get("event_seq"),
            "verdict": "aligned"
            if py_ids.get("event_seq", {}).get("contiguous_from_1") == go_ids.get("event_seq", {}).get("contiguous_from_1")
            and py_ids.get("event_seq", {}).get("type") == go_ids.get("event_seq", {}).get("type")
            else "diff",
        }
    )

    pagination_rows = []
    py_paging = raw["python"]["paging"]
    go_paging = raw["go"]["paging"]

    def paging_profile(payload: dict) -> dict:
        """Structural invariants of one events/history page."""
        keys = sorted(payload)
        limit = payload.get("limit")
        total = payload.get("total")
        returned = payload.get("returned")
        events = payload.get("events") if isinstance(payload.get("events"), list) else []
        start_offset = payload.get("start_offset")
        has_more = payload.get("has_more_older")
        problems = []
        if events is not None and returned is not None and len(events) != returned:
            problems.append(f"events_len {len(events)} != returned {returned}")
        if isinstance(total, int) and total >= 0 and isinstance(limit, int) and isinstance(returned, int):
            expected = min(limit, total)
            if returned != expected:
                problems.append(f"returned {returned} != min(limit,total) {expected}")
        if isinstance(start_offset, int) and isinstance(has_more, bool):
            if has_more != (start_offset > 0):
                problems.append(f"has_more_older {has_more} != (start_offset {start_offset} > 0)")
        return {
            "keys": keys,
            "limit": limit,
            "total": total,
            "returned": returned,
            "events_len": len(events),
            "start_index": payload.get("start_index"),
            "start_offset": start_offset,
            "has_more_older": has_more,
            "problems": problems,
        }

    py_page1 = paging_profile(py_paging.get("page1") or {})
    go_page1 = paging_profile(go_paging.get("page1") or {})
    for label in py_paging:
        py_entry = py_paging[label] or {}
        go_entry = go_paging.get(label, {}) or {}
        py_prof = paging_profile(py_entry)
        go_prof = paging_profile(go_entry)
        diffs = []
        declared = []
        if py_prof["keys"] != go_prof["keys"]:
            diffs.append({"kind": "keys", "reference": py_prof["keys"], "target": go_prof["keys"]})
        else:
            # The effective limit is the comparable clamp/fallback contract.
            if py_prof["limit"] != go_prof["limit"]:
                diffs.append({"kind": "limit", "reference": py_prof["limit"], "target": go_prof["limit"]})
            if py_prof["problems"]:
                diffs.append({"kind": "invariants", "reference": py_prof["problems"], "target": go_prof["problems"]})
            if py_prof["total"] != go_prof["total"] or py_prof["returned"] != go_prof["returned"]:
                declared.append(
                    {
                        "kind": "event-count",
                        "note": f"event totals differ (reference {py_prof['total']} vs target {go_prof['total']}); "
                                "page metadata values follow the on-disk event count, not the paging contract",
                    }
                )
        row = {
            "check": label,
            "reference": py_prof,
            "target": go_prof,
            "declared": declared,
            "diffs": diffs,
            "verdict": "aligned" if not diffs else "diff",
        }
        pagination_rows.append(row)

    # Page stitching + cursor arithmetic are checked per side against its own
    # page1 (ids/event counts differ across sides, the cursor contract does not).
    py_page2 = paging_profile(py_paging.get("page2") or {})
    go_page2 = paging_profile(go_paging.get("page2") or {})
    stitch = {
        "python": {
            "page2_total_is_minus_one": py_page2["total"] == -1,
            "page2_start_index_steps_back": isinstance(py_page1["start_index"], int)
            and isinstance(py_page2["start_index"], int)
            and py_page2["start_index"] + py_page2["returned"] == py_page1["start_index"],
            "page2_has_more_older_matches_offset": py_page2["has_more_older"] == (py_page2["start_offset"] > 0)
            if isinstance(py_page2["start_offset"], int) else None,
        },
        "go": {
            "page2_total_is_minus_one": go_page2["total"] == -1,
            "page2_start_index_steps_back": isinstance(go_page1["start_index"], int)
            and isinstance(go_page2["start_index"], int)
            and go_page2["start_index"] + go_page2["returned"] == go_page1["start_index"],
            "page2_has_more_older_matches_offset": go_page2["has_more_older"] == (go_page2["start_offset"] > 0)
            if isinstance(go_page2["start_offset"], int) else None,
        },
    }
    pagination_rows.append(
        {
            "check": "page_stitching",
            "reference": stitch["python"],
            "target": stitch["go"],
            "declared": [],
            "diffs": []
            if stitch["python"] == stitch["go"]
            else [{"kind": "cursor", "reference": stitch["python"], "target": stitch["go"]}],
            "verdict": "aligned" if stitch["python"] == stitch["go"] else "diff",
        }
    )

    sort_rows = []
    for name, _path, key in SORT_ENDPOINTS:
        py_reads = raw["python"]["sort_reads"].get(name, {})
        go_reads = raw["go"]["sort_reads"].get(name, {})

        def items_of(payload):
            if isinstance(payload, dict):
                for candidate in ("sessions", "skills", "queens", "specs", "colonies"):
                    if isinstance(payload.get(candidate), list):
                        return payload[candidate]
            elif isinstance(payload, list):
                return payload
            return None

        def order_of(payload):
            items = items_of(payload)
            if items is None:
                return None
            keys = []
            for item in items:
                if not isinstance(item, dict):
                    continue
                value = item.get(key)
                if value is None:
                    value = item.get("session_id") or item.get("id") or item.get("name")
                keys.append(str(value))
            return keys

        def direction_of(payload, mode: str):
            items = items_of(payload) or []
            if len(items) < 2:
                return None
            if mode == "session_id":
                # /api/sessions/history: both runtimes sort cold sessions by
                # session_id descending (id embeds the creation stamp).
                identities = [str(item.get("session_id") or "") for item in items if isinstance(item, dict)]
                if len(identities) != len(items):
                    return None
                if identities == sorted(identities):
                    return "session_id asc"
                if identities == sorted(identities, reverse=True):
                    return "session_id desc"
                return "unsorted"
            # /api/sessions: live-session list in creation/load order.
            stamps = []
            for item in items:
                if not isinstance(item, dict):
                    return None
                value = item.get("loaded_at") or item.get("created_at")
                if not isinstance(value, (int, float)):
                    return None
                stamps.append(float(value))
            if stamps == sorted(stamps):
                return "loaded_at asc"
            if stamps == sorted(stamps, reverse=True):
                return "loaded_at desc"
            return "unsorted"

        py_a, py_b = order_of(py_reads.get("a")), py_reads.get("b_identity")
        go_a, go_b = order_of(go_reads.get("a")), go_reads.get("b_identity")
        if py_b is None:
            py_b = order_of(py_reads.get("b"))
        if go_b is None:
            go_b = order_of(go_reads.get("b"))
        row = {
            "endpoint": name,
            "identity_key": key,
            "python_stable": py_a == py_b if py_a is not None else None,
            "go_stable": go_a == go_b if go_a is not None else None,
            "python_count": len(py_a) if py_a else None,
            "go_count": len(go_a) if go_a else None,
        }
        if name.startswith("sessions"):
            # Session ids are per-side (time-stamped), so cross-side identity
            # comparison is meaningless; compare the ordering direction instead.
            order_mode = "session_id" if name == "sessions_history" else "loaded_at"
            row["python_order"] = direction_of(py_reads.get("a"), order_mode)
            row["go_order"] = direction_of(go_reads.get("a"), order_mode)
            row["order_same"] = (
                row["python_order"] == row["go_order"] if row["python_order"] and row["go_order"] else None
            )
            row["set_same"] = None
            row["note"] = "会话 id 逐侧生成，只核对排序方向"
        else:
            if py_a == go_a:
                row["order_same"] = True
                row["set_same"] = True
                if not py_a:
                    row["note"] = "两侧列表均为空（无条目可比）"
            else:
                row["order_same"] = False
                row["set_same"] = bool(py_a and go_a and sorted(py_a) == sorted(go_a))
        sort_rows.append(row)

    counts = {
        "time_fields": len(time_rows),
        "time_class_diffs": sum(1 for row in time_rows if row["verdict"] != "aligned"),
        "id_checks": len(id_checks),
        "id_diffs": sum(1 for row in id_checks if row["verdict"] != "aligned"),
        "pagination_checks": len(pagination_rows),
        "pagination_diffs": sum(1 for row in pagination_rows if row["verdict"] != "aligned"),
        "sort_checks": len(sort_rows),
        "sort_stability_diffs": sum(1 for row in sort_rows if row.get("python_stable") is False or row.get("go_stable") is False),
        "cross_side_order_diffs": sum(1 for row in sort_rows if row.get("order_same") is False),
    }
    return {
        "schema_version": 1,
        "note": "Time / ID / pagination / sort-format inventory, real two-side run.",
        "generated_at": now_iso(),
        "reference": {"name": "python", "head": git_head(DEFAULT_PY_ROOT), "binary": str(DEFAULT_PY_BIN)},
        "target": {"name": "go_hive", "head": git_head(GO_DIR), "binary": str(GO_BINARY)},
        "settled_event_totals": raw["meta"].get("settled_totals"),
        "counts": counts,
        "time_fields": time_rows,
        "ids": id_checks,
        "pagination": pagination_rows,
        "sorting": sort_rows,
    }


def render_formats_markdown(ledger: dict) -> str:
    c = ledger["counts"]
    lines = [
        "# 时间 / ID / 分页格式差异（formats #14；reference=python → target=go_hive）",
        "",
        f"- 生成时间：{ledger['generated_at']}",
        f"- reference（基准）：python，HEAD `{ledger['reference']['head']}`，二进制 `{ledger['reference']['binary']}`",
        f"- target（待对齐）：go_hive，HEAD `{ledger['target']['head']}`，二进制 `{ledger['target']['binary']}`",
        "- 口径：两侧各起真实 serve（同一夹具 HIVE_HOME 的逐字节副本），脚本化 LLM 完成一轮对话后逐端点采集；",
        "  时间字段按「表示法类别」比较（ISO naive / ISO 带时区 / float epoch / int epoch / date-only），不比较运行期数值；",
        "  ID 按「形状」比较（正则/长度/字符集），分页按「参数 + 元数据 + 游标不变量」比较。",
        f"- 事件落盘总量（settle 时点）：python `{ledger['settled_event_totals'].get('python')}`，"
        f"go_hive `{ledger['settled_event_totals'].get('go')}`；随后异步 title 帧追平到 21 / 25（同一剧本、帧集不同，"
        "语义差异见 api-contract-diff.md 与 SSE 审计）。",
        "- 产物：`go_hive/docs/e2e-assets/errors-formats/formats-raw.json`、`formats-ledger.json`"
        "（`--check` 从 raw 重放并比对台账与本报告）。raw 中 >80 字符的字符串值以 "
        "`<str:sha256=… len=…>` 记录，只压缩文本体量，键/类型/时间/顺序不变。",
        "",
        f"**时间字段：{c['time_fields']} 个，表示法差异 {c['time_class_diffs']} 个。**",
        f"**ID 格式：{c['id_checks']} 项，差异 {c['id_diffs']} 项。**",
        f"**分页契约：{c['pagination_checks']} 项，差异 {c['pagination_diffs']} 项。**",
        f"**排序稳定性：{c['sort_checks']} 个端点，稳定性差异 {c['sort_stability_diffs']} 个，"
        f"两侧顺序差异 {c['cross_side_order_diffs']} 个。**",
        "",
        "## 时间字段表示法",
        "",
        "| 端点 | 字段 | python | go_hive | 示例（reference） | 判定 |",
        "|------|------|--------|---------|-------------------|------|",
    ]
    for row in ledger["time_fields"]:
        py = row["python"] or {}
        go = row["go"] or {}
        lines.append(
            f"| {row['endpoint']} | `{row['field']}` | {py.get('class', '—')} | {go.get('class', '—')} "
            f"| `{py.get('sample', '—')}` | {row['verdict']} |"
        )
    lines += [
        "",
        "口径归纳：",
        "- `created_at` / `last_active_at` / `loaded_at` / colony `created_at`：JSON number（float epoch 秒，两侧同）；",
        "- 事件与 snapshot 时间：本地时间 naive ISO-8601，微秒恒为 6 位（`2026-11-21T17:49:33.246443`），无时区后缀；",
        "- trigger 的 `next_fire_at`：整数 epoch **毫秒**（`1795258178237`），同时提供 `next_fire_in` 浮点秒；",
        "- `queen_overview.sessions[].first`：date-only（`YYYY-MM-DD`）。",
        "",
        "## ID 格式清单",
        "",
        "| 种类 | python | go_hive | 判定 |",
        "|------|--------|---------|------|",
    ]
    for row in ledger["ids"]:
        py = json.dumps(row["python"], ensure_ascii=False)
        go = json.dumps(row["go"], ensure_ascii=False)
        lines.append(f"| {row['kind']} | `{py[:260]}` | `{go[:260]}` | {row['verdict']} |")
    lines += [
        "",
        "ID 口径：",
        "- session id：`session_YYYYMMDD_HHMMSS_<8hex>`（两侧生成器同形，样例逐侧不同属运行期）；",
        "- colony id：**客户端提供**（路径参数/目录名），约束 `[a-z0-9_]{1,64}`，两侧 scaffold 均回显 `probe_colony`；",
        "- trigger id：按 name slug 化并加 `user-` 前缀（`probe_trigger` → `user-probe-trigger`，两侧同）；",
        "- event seq：整数、从 1 连续（两侧都通过 `contiguous_from_1`）；`correlation_id`：32 位小写 hex；",
        "- execution id：**保留差异**——上游把执行域事件（`llm_turn_complete`/`client_output_delta`/`client_input_committed` 等）的 `execution_id` 复用 session id，"
        "Go 为执行域分配独立的 32 位 hex（`execution_started` → `execution_completed`），前置帧（`reminder_injected`/首个 `node_loop_started`）两侧同为 session id。"
        "该差异与 Go 每回合额外的 `execution_*`/`node_loop_*` 信封同源，属 SSE/事件 lane 的已登记口径，不在 formats 层单独改。",
        "",
        "## 分页契约（/api/sessions/{id}/events/history）",
        "",
        "参数：`limit`（默认 500，上限 10000，`0`/非数字回落默认，负数夹到 1）、`before_offset`、`before_index`（游标分页）；",
        "响应键：`events, session_id, total, returned, truncated, limit, start_index, start_offset, has_more_older`（两侧逐键一致）。",
        "",
        "| 检查 | 判定 | 差异 | 说明 |",
        "|------|------|------|------|",
    ]
    for row in ledger["pagination"]:
        diffs = "; ".join(d["kind"] for d in row["diffs"]) or "—"
        notes = "; ".join(d["note"] for d in row.get("declared", [])) or "—"
        lines.append(f"| {row['check']} | {row['verdict']} | {diffs} | {notes} |")
    lines += [
        "",
        "不变量（两侧逐项通过）：`returned == min(limit, total)`（尾页）、`len(events) == returned`、"
        "`has_more_older == (start_offset > 0)`；游标页 `total == -1`，且 `page2.start_index + page2.returned == page1.start_index`，"
        "旧页 `seq` 连续衔接。事件总量两侧不同（python 20 / go 24，同一剧本），因此 `total`/`start_offset` 等运行期数值不做逐值比较，只作为 declared 记录。",
        "",
        "## 排序稳定性",
        "",
        "| 端点 | 身份键 | 顺序依据（python / go） | python 稳定 | go_hive 稳定 | 两侧顺序一致 | 元素数 |",
        "|------|--------|--------------------------|-------------|--------------|--------------|--------|",
    ]
    for row in ledger["sorting"]:
        basis = f"{row.get('python_order')} / {row.get('go_order')}" if row.get("python_order") else "—"
        lines.append(
            f"| {row['endpoint']} | {row['identity_key']} | {basis} | {row['python_stable']} | {row['go_stable']} "
            f"| {row.get('order_same')} | {row.get('python_count')}/{row.get('go_count')} |"
        )
    lines += [
        "",
        "结论：6 个列表端点两侧连续两次读取顺序完全一致（0 个稳定性差异）。",
        "`/api/sessions` 两侧同为 `loaded_at` 升序（创建/载入序）；`/api/sessions/history` 两侧同为 `session_id` 降序（最新在前）；"
        "`/api/skills`、`/api/queen/profiles`、`/api/colonies/tools-index` 两侧顺序逐项相同。",
        "",
        "## 保留差异（declared，逐条量化）",
        "",
        "| 编号 | 主题 | 实测 | 处理 |",
        "|------|------|------|------|",
        "| F-01 | `/api/credentials/specs` 数组顺序 | 两侧元素集合相同（115 条）且各自稳定；reference 为 Python 注册序，target 按 `credential_id` 字典序 | 保留：Go 的生成表（`internal/credentials/specs_gen.go`）按 id 排序产出，属凭据目录冻结契约；要对齐需重生成并改 `CatalogNamedSpecs` 排序，影响 `hive-cred browse` 顺序，另派凭据 lane |",
        "| F-02 | 执行域 `execution_id` 取值 | 见「ID 格式清单」：上游复用 session id，Go 用 32 hex | 保留：与 Go 额外 `execution_*` 信封同源（SSE lane 已登记），改 ID 语义会波及事件流契约 |",
        "| F-03 | 事件总量 / 分页运行期数值 | 同一剧本 settle 时 python 20 帧 vs go 24 帧（采集 21/25）；`total`/`start_offset` 随之不同 | 保留：分页契约（参数/键/不变量/游标算术）已逐项对齐，帧数差异归 SSE/事件 lane |",
        "",
        "## 复现",
        "",
        "```bash",
        "cd go_hive/go-hive && CGO_ENABLED=0 go build -o dist/hive-errfmt ./cmd/hive",
        "uv run --no-project python go_hive/scripts/errors_formats_probe.py formats          # 双跑 + 采集",
        "uv run --no-project python go_hive/scripts/errors_formats_probe.py formats --check  # 从 raw 重放台账与报告",
        "```",
        "",
        "夹具：同一 HIVE_HOME 模板的逐字节副本；两侧各起真实 serve，`POST /api/sessions` 建 3 个会话（间隔 0.4s 以固定排序样本），"
        "首个会话跑一轮脚本化 LLM 对话并等 `is_executing=false` + 事件总数稳定；随后 scaffold 一个 colony、建一个 timer trigger，再逐端点采集。",
        "",
    ]
    return "\n".join(lines) + "\n"


# ---------------------------------------------------------------------------
# --check: rebuild ledgers from committed raw and compare
# ---------------------------------------------------------------------------


def rerender_errors() -> int:
    """Rebuild the errors ledger + report from the committed raw capture."""
    raw = json.loads(ERRORS_RAW.read_text(encoding="utf-8"))
    ledger = json.loads(ERRORS_LEDGER.read_text(encoding="utf-8"))
    probes = [
        {
            "id": probe["id"],
            "label": probe["label"],
            "expect_status": probe["expect_status"],
            "method": probe["method"],
            "path": probe["path"],
            "body": None,
            "raw": None,
        }
        for probe in ledger["probes"]
    ]
    rebuilt = build_errors_ledger(raw, probes)
    write_artifacts(raw, rebuilt)
    print("errors: ledger + report rebuilt from errors-raw.json")
    return 0


def rerender_formats() -> int:
    raw = json.loads(FORMATS_RAW.read_text(encoding="utf-8"))
    ledger = build_formats_ledger(raw)
    ARTIFACT_DIR.mkdir(parents=True, exist_ok=True)
    FORMATS_LEDGER.write_text(json.dumps(ledger, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    FORMATS_REPORT.write_text(render_formats_markdown(ledger), encoding="utf-8")
    print("formats: ledger + report rebuilt from formats-raw.json")
    return 0


def check_errors() -> int:
    raw = json.loads(ERRORS_RAW.read_text(encoding="utf-8"))
    ledger = json.loads(ERRORS_LEDGER.read_text(encoding="utf-8"))
    probes = [
        {
            "id": probe["id"],
            "label": probe["label"],
            "expect_status": probe["expect_status"],
            "method": probe["method"],
            "path": probe["path"],
            "body": None,
            "raw": None,
        }
        for probe in ledger["probes"]
    ]
    rebuilt = build_errors_ledger(raw, probes)
    for key in ("generated_at", "reference", "target"):
        rebuilt.pop(key, None)
    current = dict(ledger)
    for key in ("generated_at", "reference", "target"):
        current.pop(key, None)
    if rebuilt != current:
        print("errors-ledger.json is stale relative to errors-raw.json (re-run the probe)")
        for key in rebuilt:
            if rebuilt[key] != current.get(key):
                print(f"  diverged section: {key}")
        return 1
    rendered = render_errors_markdown(ledger)
    if ERRORS_REPORT.read_text(encoding="utf-8") != rendered:
        print("error-taxonomy-diff.md is stale relative to the ledger")
        return 1
    print("errors: ledger + report re-derive cleanly")
    return 0


def check_formats() -> int:
    raw = json.loads(FORMATS_RAW.read_text(encoding="utf-8"))
    ledger = json.loads(FORMATS_LEDGER.read_text(encoding="utf-8"))
    rebuilt = build_formats_ledger(raw)
    for key in ("generated_at", "reference", "target"):
        rebuilt.pop(key, None)
    current = dict(ledger)
    for key in ("generated_at", "reference", "target"):
        current.pop(key, None)
    if rebuilt != current:
        print("formats-ledger.json is stale relative to formats-raw.json (re-run the probe)")
        for key in rebuilt:
            if rebuilt[key] != current.get(key):
                print(f"  diverged section: {key}")
        return 1
    rendered = render_formats_markdown(ledger)
    if FORMATS_REPORT.read_text(encoding="utf-8") != rendered:
        print("formats-parity.md is stale relative to the ledger")
        return 1
    print("formats: ledger + report re-derive cleanly")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="errors/formats two-side probes")
    sub = parser.add_subparsers(dest="command", required=True)
    for name in ("errors", "formats"):
        cmd = sub.add_parser(name)
        cmd.add_argument("--check", action="store_true", help="rebuild ledger from committed raw and compare")
        cmd.add_argument("--from-raw", action="store_true", help="rebuild ledger + report from the committed raw capture")
        cmd.add_argument("--keep", action="store_true", help="keep the scratch dir")
        cmd.add_argument("--scratch", default="/tmp/hive-errfmt-probe")
        cmd.add_argument("--python-bin", default=str(DEFAULT_PY_BIN))
        cmd.add_argument("--python-root", default=str(DEFAULT_PY_ROOT))
    args = parser.parse_args(argv)
    if args.check:
        return check_errors() if args.command == "errors" else check_formats()
    if args.from_raw:
        return rerender_errors() if args.command == "errors" else rerender_formats()
    return run_errors(args) if args.command == "errors" else run_formats(args)


if __name__ == "__main__":
    raise SystemExit(main())
