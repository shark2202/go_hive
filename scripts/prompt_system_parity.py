#!/usr/bin/env python3
"""First-turn system prompt parity: Python reference vs Go target.

Runs both implementations against their own local, recording OpenAI-compatible
stub (no vendor, no external network), creates the same queen session over each
HTTP API, and captures the exact request bodies the runtime sends on the opening
turn and on the first user message:

  * the full system prompt text,
  * the conversation messages (system excluded),
  * the <system-reminder> block the queen's reminder hub injects, split into its
    source bodies so a segment-level diff is readable.

Both homes carry the same fixture skills (profile ``edge`` from
internal/skills/testdata/prompt-catalog/fixture.json), so the skills catalog a
queen session announces is comparable too.

Outputs (default go_hive/docs/e2e-assets/prompt-catalog):

  system-prompt.python.txt / .go.txt
  system-prompt.diff
  first-turn-messages.python.txt / .go.txt
  first-turn-messages.diff
  reminder.python.txt / .go.txt
  live-diff.json
  live-summary.txt

Opt-in; stdlib only. Usage:

    uv run --offline python go_hive/scripts/prompt_system_parity.py
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
GO_MODULE = REPO_ROOT / "go_hive" / "go-hive"
GO_BINARY = GO_MODULE / "dist" / "hive-prompt-parity"
FIXTURE = GO_MODULE / "internal" / "skills" / "testdata" / "prompt-catalog" / "fixture.json"
DEFAULT_OUT = REPO_ROOT / "go_hive" / "docs" / "e2e-assets" / "prompt-catalog"

QUEEN_ID = "queen_growth"
LLM_MODEL = "prompt-parity-probe-model"
LLM_KEY_ENV = "HIVE_PROMPT_PARITY_KEY"
TEXT_REPLY = "PROMPT_PARITY_OK"
TURN_CONTEXT_NAME = "probe"
TURN_CONTEXT_BODY = "Turn-context probe body for the parity probe."
HEALTH_WAIT_S = 180.0
REQUEST_WAIT_S = 120.0


def log(message: str) -> None:
    print(f"[prompt-parity] {message}", flush=True)


# ---------------------------------------------------------------------------
# recording stub LLM
# ---------------------------------------------------------------------------


class RecordingStub:
    """OpenAI-compatible stub that records every request body to JSONL."""

    def __init__(self, log_path: Path) -> None:
        self.log_path = log_path
        self.records = 0
        self.lock = threading.Lock()
        outer = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
                return

            def do_GET(self) -> None:  # noqa: N802
                data = b'{"data": []}'
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_POST(self) -> None:  # noqa: N802
                length = int(self.headers.get("Content-Length", "0"))
                raw = self.rfile.read(length) if length else b"{}"
                try:
                    body = json.loads(raw.decode("utf-8"))
                except json.JSONDecodeError:
                    body = {"_raw": raw.decode("utf-8", "replace")}
                with outer.lock:
                    outer.records += 1
                    record = {"seq": outer.records, "body": body}
                    with outer.log_path.open("a", encoding="utf-8") as handle:
                        handle.write(json.dumps(record, ensure_ascii=False) + "\n")
                        handle.flush()
                        os.fsync(handle.fileno())
                if body.get("stream"):
                    self._stream(body.get("model", LLM_MODEL))
                else:
                    self._json(body.get("model", LLM_MODEL))

            def _chunk(self, model: str, delta: dict[str, Any], finish: Any = None) -> bytes:
                payload = {
                    "id": "chatcmpl-prompt-parity",
                    "object": "chat.completion.chunk",
                    "created": 1700000000,
                    "model": model,
                    "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
                }
                return f"data: {json.dumps(payload)}\n\n".encode()

            def _stream(self, model: str) -> None:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.end_headers()
                self.wfile.write(self._chunk(model, {"role": "assistant", "content": TEXT_REPLY}))
                self.wfile.write(self._chunk(model, {}, "stop"))
                self.wfile.write(b"data: [DONE]\n\n")
                self.wfile.flush()

            def _json(self, model: str) -> None:
                payload = {
                    "id": "chatcmpl-prompt-parity",
                    "object": "chat.completion",
                    "created": 1700000000,
                    "model": model,
                    "choices": [
                        {
                            "index": 0,
                            "message": {"role": "assistant", "content": TEXT_REPLY},
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
                }
                data = json.dumps(payload).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}/v1"

    def start(self) -> "RecordingStub":
        self.thread.start()
        return self

    def stop(self) -> None:
        self.server.shutdown()
        self.server.server_close()


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------


def http_json(method: str, url: str, payload: dict | None = None, timeout: float = 90.0) -> tuple[int, dict]:
    data = None
    headers: dict[str, str] = {}
    if payload is not None:
        data = json.dumps(payload).encode()
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read()
            status = response.status
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        status = exc.code
    body: dict = {}
    if raw:
        try:
            decoded = json.loads(raw)
            body = decoded if isinstance(decoded, dict) else {"value": decoded}
        except ValueError:
            body = {"raw": raw.decode("utf-8", "replace")[:400]}
    return status, body


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_health(base: str, proc: subprocess.Popen, log_path: Path, timeout: float = HEALTH_WAIT_S) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            tail = log_path.read_text(errors="replace")[-4000:]
            raise SystemExit(f"server exited before /api/health (log {log_path}):\n{tail}")
        try:
            status, _ = http_json("GET", base + "/api/health", timeout=3)
            if status == 200:
                return
        except OSError:
            pass
        time.sleep(0.2)
    raise SystemExit(f"server did not become healthy: {log_path}")


# ---------------------------------------------------------------------------
# fixture
# ---------------------------------------------------------------------------


def write_skill_md(path: Path, dir_name: str, spec: dict) -> None:
    path.mkdir(parents=True, exist_ok=True)
    if "raw_frontmatter" in spec:
        frontmatter = spec["raw_frontmatter"]
    else:
        name = spec.get("name", dir_name)
        lines = [f"name: {name}", f"description: {json.dumps(spec['description'], ensure_ascii=False)}"]
        metadata = spec.get("metadata")
        if metadata:
            lines.append("metadata:")
            for key, value in metadata.items():
                lines.append(f"  {key}: {json.dumps(value, ensure_ascii=False)}")
        frontmatter = "\n".join(lines)
    body = f"Body for {dir_name}.\n"
    (path / "SKILL.md").write_text(f"---\n{frontmatter}\n---\n\n{body}", encoding="utf-8")


def materialize_fixture(fixture: dict, profile: str, home: Path, user_home: Path) -> None:
    """Write one fixture profile under a HIVE_HOME; ``symlink_to`` entries
    become symlinks created after every real skill exists."""
    entry = fixture["profiles"][profile]
    links: list[tuple[Path, str]] = []

    def emit(base: Path, skills: dict) -> None:
        for dir_name, spec in skills.items():
            if spec.get("symlink_to"):
                links.append((base / dir_name, spec["symlink_to"]))
                continue
            write_skill_md(base / dir_name, dir_name, spec)

    emit(home / "skills", entry.get("hive_skills", {}))
    emit(home / "queens" / QUEEN_ID / "skills", entry.get("queen_skills", {}))
    emit(user_home / ".agents" / "skills", entry.get("agents_skills", {}))
    for link_path, target in links:
        link_path.parent.mkdir(parents=True, exist_ok=True)
        if link_path.is_symlink() or link_path.exists():
            link_path.unlink()
        os.symlink(target, link_path)


def make_home(root: Path, name: str, api_base: str, user_home: Path) -> Path:
    home = root / name
    (home / "secrets").mkdir(parents=True, exist_ok=True)
    # Same deterministic credential key on both sides: it seals the credential
    # store, not the prompt.
    (home / "secrets" / "credential_key").write_text("cHJvbXB0LXBhcml0eS1wcm9iZS1rZXktMDAwMDAwMDA=", encoding="utf-8")
    os.chmod(home / "secrets" / "credential_key", 0o600)
    config = {
        "llm": {
            "provider": "openai",
            "model": LLM_MODEL,
            "api_base": api_base,
            "api_key_env_var": LLM_KEY_ENV,
        },
        # Both sides get the same turn_context section. On the Python side a
        # fake installed package below answers it; the Go build links the same
        # package name as a built-in provider (internal/server/
        # turn_context_probe.go), so the configuration gate starts one package
        # per side and the injected text is comparable.
        "turn_context": {TURN_CONTEXT_NAME: {"enabled": True}},
    }
    (home / "configuration.json").write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
    user_home.mkdir(parents=True, exist_ok=True)
    fixture = json.loads(FIXTURE.read_text(encoding="utf-8"))
    materialize_fixture(fixture, "edge", home, user_home)
    return home


def write_python_turn_context_plugin(root: Path) -> Path:
    """Create a fake installed `hive.turn_context` package (module + dist-info).

    Python discovers turn-context packages through importlib.metadata entry
    points; a dist-info directory on PYTHONPATH is enough for the server to see
    an installed package named ``probe``. The Go analogue is the built-in
    ``probe`` package linked into the binary (internal/server/
    turn_context_probe.go), so the same configuration starts a package on both
    sides.
    """
    plugin_dir = root / "python-turn-context-plugins"
    plugin_dir.mkdir(parents=True, exist_ok=True)
    (plugin_dir / "turn_context_probe.py").write_text(
        "class Probe:\n"
        "    def __init__(self, host):\n"
        "        self.host = host\n"
        "\n"
        "    async def start(self):\n"
        "        return None\n"
        "\n"
        "    async def stop(self):\n"
        "        return None\n"
        "\n"
        "    async def context(self, queen_id, message):\n"
        f"        return {TURN_CONTEXT_BODY!r}\n",
        encoding="utf-8",
    )
    dist_info = plugin_dir / "turn_context_probe-0.1.dist-info"
    dist_info.mkdir(exist_ok=True)
    (dist_info / "METADATA").write_text(
        "Metadata-Version: 2.1\nName: turn-context-probe\nVersion: 0.1\n", encoding="utf-8"
    )
    (dist_info / "entry_points.txt").write_text(
        f"[hive.turn_context]\n{TURN_CONTEXT_NAME} = turn_context_probe:Probe\n", encoding="utf-8"
    )
    return plugin_dir


# ---------------------------------------------------------------------------
# capture
# ---------------------------------------------------------------------------


def read_records(path: Path) -> list[dict]:
    if not path.is_file():
        return []
    out = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return out


def message_text(message: dict) -> str:
    content = message.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(
            part.get("text", "")
            for part in content
            if isinstance(part, dict) and isinstance(part.get("text"), str)
        )
    return ""


def system_text(body: dict) -> str:
    messages = body.get("messages") or []
    if not isinstance(messages, list):
        return ""
    for message in messages:
        if isinstance(message, dict) and message.get("role") == "system":
            return message_text(message)
    return ""


def conversation_text(body: dict) -> str:
    """Serialize every non-system message as role + content, one block each."""
    blocks = []
    messages = body.get("messages") or []
    if not isinstance(messages, list):
        return ""
    for message in messages:
        if not isinstance(message, dict) or message.get("role") == "system":
            continue
        role = message.get("role", "?")
        text = message_text(message)
        tool_calls = message.get("tool_calls")
        suffix = ""
        if tool_calls:
            suffix = "\n[tool_calls] " + json.dumps(tool_calls, ensure_ascii=False)
        blocks.append(f"--- {role} ---\n{text}{suffix}")
    return "\n\n".join(blocks)


def reminder_text(conversation: str) -> str:
    match = re.search(r"<system-reminder>\n(.*?)\n</system-reminder>", conversation, re.DOTALL)
    return match.group(1) if match else ""


def canonicalize(text: str, hive_home: Path, user_home: Path, session_id: str, port: int) -> str:
    # macOS resolves a probe root under /var/folders to /private/var/folders, and
    # the runtime may carry either spelling; paste that spelling away first so
    # the captures do not depend on which TMPDIR the probe was handed.
    text = text.replace("/private" + str(hive_home), "<HIVE_HOME>")
    text = text.replace("/private" + str(user_home), "<HOME>")
    text = text.replace(str(hive_home), "<HIVE_HOME>")
    text = text.replace(str(user_home), "<HOME>")
    # Bundled skills: Python renders the checkout path, Go a temp extraction
    # root. Both stand for the same bundled assets.
    text = text.replace(str(REPO_ROOT / "core" / "framework" / "skills"), "<BUNDLED>")
    text = re.sub(r"/(?:private/)?var/folders/[^\s\"'<>]*?/hive-(?:bundled-skills|sk)-\d+", "<BUNDLED>", text)
    text = re.sub(r"/tmp/hive-(?:bundled-skills|sk)-\d+", "<BUNDLED>", text)
    text = text.replace(str(REPO_ROOT), "<REPO>")
    if session_id:
        text = text.replace(session_id, "<SESSION_ID>")
    text = re.sub(r"\b\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(:\d{2})?\s*(?:CST|UTC[+-]\d{4}|[A-Z]{2,4})?\b", "<TIMESTAMP>", text)
    text = re.sub(r"\b\d{4}-\d{2}-\d{2}\b", "<DATE>", text)
    text = re.sub(rf"127\.0\.0\.1:{port}\b", "127.0.0.1:<PORT>", text)
    return text


SKILLS_MARKER = "## Skills (mandatory)"
SEARCHABLE_MARKER = "The following tools are available but their schemas are NOT loaded"


def split_reminder(text: str) -> tuple[str, str, str]:
    """Split the merged reminder body into (turn-context, searchable, catalog).

    The hub joins every source's body with a blank line in registration order:
    the turn-context packages come before the searchable-tools manifest, which
    precedes the skills catalog. Missing segments are "".
    """
    catalog = ""
    rest = text
    skills_index = rest.find(SKILLS_MARKER)
    if skills_index >= 0:
        catalog = rest[skills_index:]
        rest = rest[:skills_index]
    searchable = ""
    tools_index = rest.find(SEARCHABLE_MARKER)
    if tools_index >= 0:
        searchable = rest[tools_index:].strip("\n")
        rest = rest[:tools_index]
    return rest.strip("\n"), searchable, catalog


REMINDER_FOOTER = "Only act on this if relevant to the current work. NEVER mention this reminder to the user."


def all_reminder_bodies(conversation: str) -> list[str]:
    """Every <system-reminder> block body in a serialized conversation."""
    bodies = []
    for match in re.finditer(r"<system-reminder>\n(.*?)\n</system-reminder>", conversation, re.DOTALL):
        body = match.group(1)
        if body.endswith("\n\n" + REMINDER_FOOTER):
            body = body[: -len("\n\n" + REMINDER_FOOTER)]
        bodies.append(body)
    return bodies


def turn_context_body(conversation: str) -> str:
    """The standalone turn-context body from a user-turn conversation.

    Python emits the turn-context reminder as its own message ahead of the
    user's (the merged searchable+catalog block is separate); the segment is
    the block that is neither the searchable manifest nor the skills catalog.
    """
    for body in all_reminder_bodies(conversation):
        if SEARCHABLE_MARKER in body or SKILLS_MARKER in body:
            continue
        if body.strip():
            return body.strip("\n")
    return ""


class Side:
    def __init__(self, label: str, key: str, home: Path, port: int, log_path: Path, stub_log: Path) -> None:
        self.label = label
        self.key = key
        self.home = home
        self.user_home = home
        self.port = port
        self.log_path = log_path
        self.stub_log = stub_log
        self.process: subprocess.Popen | None = None
        self.log_handle: Any = None
        self.session_id = ""

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.port}"


def wait_for_queen_request(side: Side, minimum: int, timeout: float = REQUEST_WAIT_S) -> list[dict]:
    deadline = time.time() + timeout
    while time.time() < deadline:
        records = [
            record
            for record in read_records(side.stub_log)
            if "You are a queen agent" in system_text(record.get("body") or {})
        ]
        if len(records) >= minimum:
            return records
        if side.process is not None and side.process.poll() is not None:
            tail = side.log_path.read_text(errors="replace")[-4000:]
            raise SystemExit(f"{side.label} exited while waiting for a queen request:\n{tail}")
        time.sleep(0.2)
    raise SystemExit(f"{side.label}: no queen request after {timeout:.0f}s (stub log {side.stub_log})")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", default=str(DEFAULT_OUT))
    parser.add_argument("--workdir", default="")
    parser.add_argument("--no-build", action="store_true")
    parser.add_argument("--keep", action="store_true")
    args = parser.parse_args()

    out = Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    root = Path(args.workdir).resolve() if args.workdir else Path(
        os.environ.get("TMPDIR", "/tmp")
    ) / f"hive-prompt-parity-{os.getpid()}"
    root.mkdir(parents=True, exist_ok=True)
    logs = root / "logs"
    logs.mkdir(exist_ok=True)

    if not args.no_build:
        log("building Go target")
        build = subprocess.run(
            ["go", "build", "-o", str(GO_BINARY), "./cmd/hive"],
            cwd=str(GO_MODULE),
            capture_output=True,
            text=True,
            timeout=900,
            env={**os.environ, "GOPROXY": os.environ.get("GOPROXY", "off")},
        )
        if build.returncode != 0:
            raise SystemExit(f"go build failed:\n{build.stdout}\n{build.stderr}")
    if not GO_BINARY.is_file():
        raise SystemExit(f"Go binary missing: {GO_BINARY} (run without --no-build)")

    uv = shutil.which("uv")
    if uv is None:
        raise SystemExit("uv is not on PATH")

    plugin_dir = write_python_turn_context_plugin(root)
    log(f"fake turn-context package: {plugin_dir}")

    reference_stub = RecordingStub(root / "reference-requests.jsonl").start()
    target_stub = RecordingStub(root / "target-requests.jsonl").start()
    log(f"stub LLMs: reference={reference_stub.url} target={target_stub.url}")

    reference_home = make_home(root, "reference-home", reference_stub.url, root / "reference-user-home")
    target_home = make_home(root, "target-home", target_stub.url, root / "target-user-home")
    reference = Side("Python reference", "python", reference_home, free_port(), logs / "python.log", reference_stub.log_path)
    reference.user_home = root / "reference-user-home"
    target = Side("Go target", "go", target_home, free_port(), logs / "go.log", target_stub.log_path)
    target.user_home = root / "target-user-home"
    while target.port == reference.port:
        target.port = free_port()

    base_env = {
        key: value
        for key, value in os.environ.items()
        if key not in {"HIVE_HOME", "HIVE_CREDENTIAL_KEY", "HIVE_RUNTIME_PORT", "HIVE_DESKTOP_TOKEN", "HOME"}
    }
    base_env.update({"PYTHONUNBUFFERED": "1", LLM_KEY_ENV: "prompt-parity-dummy"})
    reference_env = dict(base_env)
    reference_env["HIVE_HOME"] = str(reference_home)
    reference_env["HOME"] = str(reference.user_home)
    reference_env["PYTHONPATH"] = str(plugin_dir)
    reference_env["HIVE_CREDENTIAL_KEY"] = (reference_home / "secrets" / "credential_key").read_text(encoding="utf-8")
    target_env = dict(base_env)
    target_env["HIVE_HOME"] = str(target_home)
    target_env["HOME"] = str(target.user_home)
    target_env["HIVE_CREDENTIAL_KEY"] = (target_home / "secrets" / "credential_key").read_text(encoding="utf-8")

    argv: dict[str, list[str]] = {
        "python": [uv, "run", "--offline", "hive", "serve", "--host", "127.0.0.1", "--port", str(reference.port)],
        "go": [str(GO_BINARY), "serve", "--host", "127.0.0.1", "--port", str(target.port)],
    }
    cwd = {"python": str(REPO_ROOT), "go": str(GO_MODULE)}
    envs = {"python": reference_env, "go": target_env}

    captures: dict[str, dict] = {}
    try:
        for side in (reference, target):
            handle = open(side.log_path, "w", encoding="utf-8")  # noqa: SIM115
            side.log_handle = handle
            side.process = subprocess.Popen(
                argv[side.key],
                cwd=cwd[side.key],
                env=envs[side.key],
                stdout=handle,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
        for side in (reference, target):
            wait_health(side.base_url, side.process, side.log_path)
            log(f"{side.label} healthy on {side.base_url}")
        fresh_logs = {"python": reference.stub_log, "go": target.stub_log}
        for side in (reference, target):
            fresh_logs[side.key].unlink(missing_ok=True)
            status, body = http_json("POST", side.base_url + "/api/sessions", {"queen_name": QUEEN_ID})
            if status not in (200, 201):
                raise SystemExit(f"{side.label}: create session failed: {status} {body}")
            side.session_id = str(body.get("session_id") or body.get("id") or "")
            if not side.session_id:
                raise SystemExit(f"{side.label}: create session returned no id: {body}")
            log(f"{side.label} session {side.session_id}")

        for side in (reference, target):
            records = wait_for_queen_request(side, 1)
            log(f"{side.label}: opening queen request captured ({len(records)})")

        for side in (reference, target):
            status, body = http_json(
                "POST",
                side.base_url + f"/api/sessions/{side.session_id}/chat",
                {"message": "PARITY_PROBE_MESSAGE"},
            )
            if status not in (200, 202):
                raise SystemExit(f"{side.label}: chat failed: {status} {body}")

        for side in (reference, target):
            records = wait_for_queen_request(side, 2)
            log(f"{side.label}: user-turn queen request captured ({len(records)})")

        for side in (reference, target):
            records = wait_for_queen_request(side, 2)
            opening = records[0]["body"]
            user_turn = records[-1]["body"]
            system = system_text(opening)
            conversation = conversation_text(opening)
            reminder = reminder_text(conversation)
            _, searchable, catalog = split_reminder(reminder)
            user_conversation = conversation_text(user_turn)
            captures[side.key] = {
                "system": canonicalize(system, side.home, side.user_home, side.session_id, side.port),
                "opening_conversation": canonicalize(conversation, side.home, side.user_home, side.session_id, side.port),
                "turn_context_body": canonicalize(
                    turn_context_body(user_conversation), side.home, side.user_home, side.session_id, side.port
                ),
                "reminder_catalog": canonicalize(catalog, side.home, side.user_home, side.session_id, side.port),
                "reminder_searchable": canonicalize(searchable, side.home, side.user_home, side.session_id, side.port),
                "user_turn_conversation": canonicalize(user_conversation, side.home, side.user_home, side.session_id, side.port),
                "meta": {
                    "system_chars": len(system),
                    "opening_messages": len(opening.get("messages") or []),
                    "opening_tools": len(opening.get("tools") or []),
                    "queen_requests": len(records),
                    "turn_context_injected": bool(turn_context_body(user_conversation).strip()),
                    "turn_context_keys": sorted(
                        (json.loads((side.home / "configuration.json").read_text(encoding="utf-8")).get("turn_context") or {}).keys()
                    ),
                },
            }
    finally:
        for side in (reference, target):
            if side.process is not None and side.process.poll() is None:
                try:
                    os.killpg(os.getpgid(side.process.pid), 15)
                except (ProcessLookupError, PermissionError):
                    side.process.terminate()
                try:
                    side.process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    side.process.kill()
            if side.log_handle is not None:
                side.log_handle.close()
        reference_stub.stop()
        target_stub.stop()

    python_capture = captures.get("python")
    go_capture = captures.get("go")
    if python_capture is None or go_capture is None:
        raise SystemExit("missing capture for one side")

    def write_pair(name: str, python_text: str, go_text: str) -> dict:
        (out / f"{name}.python.txt").write_text(python_text, encoding="utf-8")
        (out / f"{name}.go.txt").write_text(go_text, encoding="utf-8")
        return {"identical": python_text == go_text, "python_chars": len(python_text), "go_chars": len(go_text)}

    result: dict[str, Any] = {
        "system_prompt": write_pair("system-prompt", python_capture["system"], go_capture["system"]),
        "opening_messages": write_pair("first-turn-messages", python_capture["opening_conversation"], go_capture["opening_conversation"]),
        "user_turn_messages": write_pair(
            "user-turn-messages", python_capture["user_turn_conversation"], go_capture["user_turn_conversation"]
        ),
        "turn_context": write_pair("turn-context-live", python_capture["turn_context_body"], go_capture["turn_context_body"]),
        "reminder_catalog": write_pair("reminder-catalog", python_capture["reminder_catalog"], go_capture["reminder_catalog"]),
        "reminder_searchable": write_pair(
            "reminder-searchable", python_capture["reminder_searchable"], go_capture["reminder_searchable"]
        ),
        "meta": {"python": python_capture["meta"], "go": go_capture["meta"]},
    }

    import difflib

    def unified(python_text: str, go_text: str) -> str:
        return "".join(
            difflib.unified_diff(
                python_text.splitlines(keepends=True),
                go_text.splitlines(keepends=True),
                fromfile="python",
                tofile="go",
                n=2,
            )
        )

    (out / "system-prompt.diff").write_text(unified(python_capture["system"], go_capture["system"]), encoding="utf-8")
    (out / "first-turn-messages.diff").write_text(
        unified(python_capture["opening_conversation"], go_capture["opening_conversation"]), encoding="utf-8"
    )
    (out / "reminder-catalog.diff").write_text(
        unified(python_capture["reminder_catalog"], go_capture["reminder_catalog"]), encoding="utf-8"
    )
    (out / "reminder-searchable.diff").write_text(
        unified(python_capture["reminder_searchable"], go_capture["reminder_searchable"]), encoding="utf-8"
    )
    (out / "user-turn-messages.diff").write_text(
        unified(python_capture["user_turn_conversation"], go_capture["user_turn_conversation"]), encoding="utf-8"
    )
    (out / "turn-context-live.diff").write_text(
        unified(python_capture["turn_context_body"], go_capture["turn_context_body"]), encoding="utf-8"
    )
    (out / "live-diff.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")

    summary = [
        "first-turn prompt parity (canonicalized)",
        f"  system prompt:        {'IDENTICAL' if result['system_prompt']['identical'] else 'DIFF'} "
        f"(python={result['system_prompt']['python_chars']} chars, go={result['system_prompt']['go_chars']} chars)",
        f"  opening messages:     {'IDENTICAL' if result['opening_messages']['identical'] else 'DIFF'} "
        f"(python={result['opening_messages']['python_chars']} chars, go={result['opening_messages']['go_chars']} chars)",
        f"  skills catalog block: {'IDENTICAL' if result['reminder_catalog']['identical'] else 'DIFF'} "
        f"(python={result['reminder_catalog']['python_chars']} chars, go={result['reminder_catalog']['go_chars']} chars)",
        f"  searchable manifest:  {'IDENTICAL' if result['reminder_searchable']['identical'] else 'DIFF'} "
        f"(python={result['reminder_searchable']['python_chars']} chars, go={result['reminder_searchable']['go_chars']} chars)",
        f"  turn_context:         python_injected={python_capture['meta']['turn_context_injected']} "
        f"go_injected={go_capture['meta']['turn_context_injected']} "
        f"body={'IDENTICAL' if result['turn_context']['identical'] else 'DIFF'} "
        f"(keys python={python_capture['meta']['turn_context_keys']} go={go_capture['meta']['turn_context_keys']})",
    ]
    (out / "live-summary.txt").write_text("\n".join(summary) + "\n", encoding="utf-8")
    print("\n".join(summary))
    if not args.keep and not args.workdir:
        shutil.rmtree(root, ignore_errors=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
