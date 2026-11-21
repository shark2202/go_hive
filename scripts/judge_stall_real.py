#!/usr/bin/env python3
"""Cross-implementation stall-check run against the real configured judge.

Boots the Python `hive serve` and the Go `hive serve` from this worktree with
the same temporary HIVE_HOME shape: the queen's chat model is the deterministic
gateway (go_hive/go-hive/e2e/cmd/stallgw), the `judge` slot is the real
TypeSafe endpoint (default the local startlux-decide service) behind a
recording proxy that forwards every /systemone question verbatim and logs the
exact request body, response body and latency.

What is real and what is scripted:

  * judge: REAL — every judged turn is answered by the configured endpoint
    (startlux-decide `POST /v1/systemone`, model `0.8B-Instant-MLX-4bit`).
  * queen model: scripted on purpose — the gateway replays a fixed announcement
    or a tool call so both implementations receive the same turn. This is a
    pipeline parity run, not a claim about any real model's stall rate.
  * server + events + transcript + `/api/config/features`: REAL — both `hive
    serve` binaries are driven over HTTP, and the reminder is read back from
    the session transcript, the SSE stream and the queen's next model request.

Cases: stall at threshold 0.5/0.7, a reply whose real-judge p lands between
the two thresholds, a turn that called a tool, the option switched off, an
unreachable judge, and a queen turn with no client input (external event).

Usage:
  uv run --offline python go_hive/scripts/judge_stall_real.py \
      --out go_hive/docs/e2e-assets/judge-stall-real
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

STALL_MODEL = "stall-e2e-model"
STALL_ANNOUNCEMENT = "好的，我这就去整理，整理完马上告诉你。"
STALL_LATER = "整理好了。"
BORDERLINE_REPLY = "收到，我先看看。"
TOOL_NAME = "get_current_time"
TOOL_REPLY = "现在是下午 4 点 05 分。"
TITLE = "待办整理"


def log(message: str) -> None:
    print(f"[judge-stall-real] {message}", flush=True)


# --------------------------------------------------------------------------- #
# recording judge proxy
# --------------------------------------------------------------------------- #


class JudgeProxy:
    """Recording reverse proxy in front of the real judge endpoint."""

    def __init__(self, upstream: str, log_path: Path):
        self.upstream = upstream.rstrip("/")
        self.log_path = log_path
        self.records: list[dict] = []
        self.tag = ""
        self.lock = threading.Lock()
        proxy = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def _record(self, entry: dict):
                with proxy.lock:
                    proxy.records.append(entry)
                    with open(proxy.log_path, "a", encoding="utf-8") as handle:
                        handle.write(json.dumps(entry, ensure_ascii=False) + "\n")

            def do_GET(self):
                if self.path in ("/__records", "/__records/"):
                    with proxy.lock:
                        body = json.dumps(proxy.records, ensure_ascii=False).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                    return
                self.send_response(404)
                self.send_header("Content-Length", "0")
                self.end_headers()

            def do_POST(self):
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b""
                headers = {
                    key: value
                    for key, value in self.headers.items()
                    if key.lower() not in ("host", "content-length", "connection", "accept-encoding")
                }
                started = time.time()
                entry = {
                    "tag": proxy.tag,
                    "ts": started,
                    "method": self.command,
                    "path": self.path,
                    "request": raw.decode("utf-8", "replace"),
                    "status": None,
                    "response": None,
                    "ms": None,
                }
                try:
                    request = urllib.request.Request(
                        proxy.upstream + self.path, data=raw, headers=headers, method=self.command
                    )
                    with urllib.request.urlopen(request, timeout=60) as upstream:
                        payload = upstream.read()
                        status = upstream.status
                        out_headers = list(upstream.headers.items())
                    entry["status"] = status
                    entry["response"] = payload.decode("utf-8", "replace")
                    entry["ms"] = round((time.time() - started) * 1000)
                    proxy._append(entry)
                    self.send_response(status)
                    for key, value in out_headers:
                        if key.lower() in (
                            "content-length",
                            "transfer-encoding",
                            "connection",
                            "content-encoding",
                        ):
                            continue
                        self.send_header(key, value)
                    self.send_header("Content-Length", str(len(payload)))
                    self.end_headers()
                    self.wfile.write(payload)
                except urllib.error.HTTPError as exc:
                    payload = exc.read()
                    entry["status"] = exc.code
                    entry["response"] = payload.decode("utf-8", "replace")
                    entry["ms"] = round((time.time() - started) * 1000)
                    proxy._append(entry)
                    self.send_response(exc.code)
                    self.send_header("Content-Length", str(len(payload)))
                    self.end_headers()
                    self.wfile.write(payload)
                except Exception as exc:  # noqa: BLE001
                    entry["status"] = None
                    entry["response"] = f"proxy error: {exc}"
                    entry["ms"] = round((time.time() - started) * 1000)
                    proxy._append(entry)
                    body = json.dumps({"proxy_error": str(exc)}).encode()
                    self.send_response(502)
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def _append(self, entry: dict):
        with self.lock:
            self.records.append(entry)
            with open(self.log_path, "a", encoding="utf-8") as handle:
                handle.write(json.dumps(entry, ensure_ascii=False) + "\n")

    @property
    def base(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def mark(self, tag: str) -> int:
        with self.lock:
            self.tag = tag
            return len(self.records)

    def since(self, index: int) -> list[dict]:
        with self.lock:
            return list(self.records[index:])


# --------------------------------------------------------------------------- #
# http helpers
# --------------------------------------------------------------------------- #


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def http_json(method: str, url: str, payload=None, timeout: float = 30.0, headers: dict | None = None):
    data = None
    merged = dict(headers or {})
    if payload is not None:
        data = json.dumps(payload).encode()
        merged["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=merged, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read()
            status = response.status
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        status = exc.code
    body = None
    if raw:
        try:
            body = json.loads(raw)
        except ValueError:
            body = {"raw": raw.decode("utf-8", "replace")[:800]}
    return status, body


class SSEWatcher:
    """Reads /api/sessions/{id}/events in a thread, writing raw lines to disk."""

    def __init__(self, url: str, path: Path):
        self.url = url
        self.path = path
        self.events: list[dict] = []
        self.stop = False
        self.thread = threading.Thread(target=self._run, daemon=True)
        self.thread.start()

    def _run(self):
        try:
            request = urllib.request.Request(self.url, headers={"Accept": "text/event-stream"})
            with urllib.request.urlopen(request, timeout=600) as response:
                with open(self.path, "w", encoding="utf-8") as handle:
                    for raw in response:
                        if self.stop:
                            break
                        text = raw.decode("utf-8", "replace").rstrip("\n")
                        handle.write(text + "\n")
                        if text.startswith("data:"):
                            try:
                                self.events.append(json.loads(text[5:].strip()))
                            except ValueError:
                                pass
        except Exception as exc:  # noqa: BLE001
            if not self.stop:
                log(f"sse watcher stopped: {exc}")

    def close(self):
        self.stop = True


# --------------------------------------------------------------------------- #
# process management
# --------------------------------------------------------------------------- #


def wait_health(base: str, proc: subprocess.Popen, log_path: Path, timeout: float = 90.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            tail = log_path.read_text(encoding="utf-8", errors="replace")[-3000:]
            raise RuntimeError(f"serve exited before healthy:\n{tail}")
        try:
            status, _ = http_json("GET", base + "/api/health", timeout=3)
            if status == 200:
                return
        except Exception:
            pass
        time.sleep(0.3)
    raise RuntimeError("serve did not become healthy")


def stop_proc(proc: subprocess.Popen):
    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        proc.wait(timeout=20)
    except subprocess.TimeoutExpired:
        proc.kill()


def start_serve(
    side: str,
    repo_root: Path,
    home: Path,
    port: int,
    log_path: Path,
    go_binary: Path,
    env_extra: dict,
):
    env = {
        key: value
        for key, value in os.environ.items()
        if key
        not in (
            "HIVE_STALL_CHECK",
            "HIVE_HOME",
            "HIVE_LLM_BASE_URL",
            "OPENAI_API_KEY",
            "OPENAI_BASE_URL",
            "OPENAI_API_BASE",
            "HIVE_DESKTOP_TOKEN",
        )
    }
    env.update({"HIVE_HOME": str(home), **env_extra})
    if side == "py":
        command = [
            "uv",
            "run",
            "--offline",
            "hive",
            "serve",
            "--host",
            "127.0.0.1",
            "--port",
            str(port),
        ]
        cwd = repo_root
    else:
        command = [str(go_binary), "serve", "--host", "127.0.0.1", "--port", str(port)]
        cwd = home
    proc = subprocess.Popen(
        command,
        cwd=str(cwd),
        env=env,
        stdout=log_path.open("w"),
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    return proc


def start_gateway(binary: Path, options: dict, log_path: Path) -> tuple[subprocess.Popen, str]:
    command = [str(binary), "-addr", "127.0.0.1:0", "-log", str(log_path)]
    for key, value in options.items():
        command += [f"-{key}", str(value)]
    proc = subprocess.Popen(
        command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, start_new_session=True
    )
    line = proc.stdout.readline().strip()
    match = re.search(r"(http://[^\s]+)/v1", line)
    if not match:
        proc.kill()
        raise RuntimeError(f"stallgw did not report its URL: {line!r}")
    return proc, match.group(1)


# --------------------------------------------------------------------------- #
# session helpers
# --------------------------------------------------------------------------- #


def session_dir(home: Path, session_id: str) -> Path:
    matches = list((home / "queens").glob(f"*/sessions/{session_id}"))
    if not matches:
        raise RuntimeError(f"session dir not found for {session_id}")
    return matches[0]


def read_parts(session: Path) -> list[dict]:
    parts = []
    for path in sorted((session / "conversations" / "parts").glob("*.json")):
        try:
            parts.append(json.loads(path.read_text(encoding="utf-8")))
        except ValueError:
            continue
    return parts


def part_digest(part: dict) -> dict:
    return {
        "seq": part.get("seq"),
        "role": part.get("role"),
        "tool_use_id": part.get("tool_use_id"),
        "tool_calls": part.get("tool_calls"),
        "is_client_input": part.get("is_client_input", False),
        "is_system_reminder": part.get("is_system_reminder", False),
        "is_trigger": part.get("is_trigger", False),
        "content": str(part.get("content") or "")[:4000],
    }


def trim_gateway_requests(log_path: Path) -> list[dict]:
    """Summarize the gateway log: the full bodies carry the queen's 20k-char
    system prompt, and the parsed reminder flag plus the last user message are
    the evidence this run needs. Returns the trimmed records."""
    trimmed = []
    for record in gateway_requests(log_path):
        try:
            body = json.loads(record.get("body") or "{}")
        except ValueError:
            continue
        messages = body.get("messages") or []
        last_user = ""
        for message in messages:
            content = message.get("content")
            if message.get("role") == "user" and isinstance(content, str) and content:
                last_user = content
        trimmed.append(
            {
                "seq": record.get("seq"),
                "kind": record.get("kind"),
                "path": record.get("path"),
                "model": body.get("model"),
                "messages": len(messages),
                "tools": len(body.get("tools") or []),
                "stream": body.get("stream"),
                "has_reminder": any(
                    all(marker in str(m.get("content") or "") for marker in REMINDER_MARKERS) for m in messages
                ),
                "last_user_tail": last_user[-600:],
            }
        )
    return trimmed


def gateway_requests(log_path: Path) -> list[dict]:
    if not log_path.exists():
        return []
    out = []
    for line in log_path.read_text(encoding="utf-8").splitlines():
        try:
            out.append(json.loads(line))
        except ValueError:
            continue
    return out



# --------------------------------------------------------------------------- #
# cases
# --------------------------------------------------------------------------- #

USER_MESSAGE = "帮我把今天的待办事项整理一下，整理完告诉我结果。"
CONTEXT_MESSAGE = "[external] 系统时钟同步完成，请回复一句确认。"

CASES = [
    {
        "id": "stall-0.5",
        "gateway": {"announcement": STALL_ANNOUNCEMENT, "later": STALL_LATER},
        "threshold": 0.5,
        "expect": "remind",
        "expect_judge": 1,
        "expect_reminder": True,
    },
    {
        "id": "stall-0.7",
        "gateway": {"announcement": STALL_ANNOUNCEMENT, "later": STALL_LATER},
        "threshold": 0.7,
        "expect": "remind",
        "expect_judge": 1,
        "expect_reminder": True,
    },
    {
        "id": "borderline-0.5",
        "gateway": {"announcement": BORDERLINE_REPLY, "later": STALL_LATER},
        "threshold": 0.5,
        "expect": "remind",
        "expect_judge": 1,
        "expect_reminder": True,
    },
    {
        "id": "borderline-0.7",
        "gateway": {"announcement": BORDERLINE_REPLY, "later": STALL_LATER},
        "threshold": 0.7,
        "expect": "quiet",
        "expect_judge": 1,
        "expect_reminder": False,
    },
    {
        "id": "tool-0.5",
        "gateway": {"tool-call": TOOL_NAME, "tool-reply": TOOL_REPLY, "later": STALL_LATER},
        "threshold": 0.5,
        "expect": "quiet",
        "expect_judge": 1,
        "expect_reminder": False,
    },
    {
        "id": "switch-off",
        "gateway": {"announcement": STALL_ANNOUNCEMENT, "later": STALL_LATER},
        "threshold": 0.5,
        "stall_check": False,
        "expect": "no-judge",
        "expect_judge": 0,
        "expect_reminder": False,
    },
    {
        "id": "judge-dead",
        "gateway": {"announcement": STALL_ANNOUNCEMENT, "later": STALL_LATER},
        "threshold": 0.5,
        "judge_base": "http://127.0.0.1:9/v1",
        "expect": "quiet",
        "expect_judge": 0,
        "expect_reminder": False,
    },
    {
        "id": "no-user",
        "gateway": {"announcement": "收到，时钟同步完成。", "later": STALL_LATER},
        "threshold": 0.5,
        "context_inject": CONTEXT_MESSAGE,
        "expect": "no-judge",
        "expect_judge": 0,
        "expect_reminder": False,
    },
]

REMINDER_MARKERS = ("<system-reminder>", "Don't mention this reminder")


def write_case_config(home: Path, gateway_base: str, judge_base: str, threshold: float, enabled: bool):
    configuration = {
        "llm": {
            "provider": "openai",
            "model": STALL_MODEL,
            "api_base": gateway_base + "/v1",
            "api_key_env_var": "STALLGW_KEY",
        },
        "stall_check": enabled,
        "judge": {
            "protocol": "typesafe",
            "model": "0.8B-Instant-MLX-4bit",
            "api_base": judge_base,
            "api_key": "local-judge-key",
            "threshold": threshold,
        },
    }
    (home / "configuration.json").write_text(
        json.dumps(configuration, ensure_ascii=False, indent=2), encoding="utf-8"
    )


def write_case_config_go(home: Path, gateway_base: str, judge_base: str, threshold: float, enabled: bool):
    configuration = {
        "llm": {
            "provider": "custom",
            "model": "ollama/" + STALL_MODEL,
            "api_base": gateway_base + "/v1",
        },
        "stall_check": enabled,
        "judge": {
            "protocol": "typesafe",
            "model": "0.8B-Instant-MLX-4bit",
            "api_base": judge_base,
            "api_key": "local-judge-key",
            "threshold": threshold,
        },
    }
    (home / "configuration.json").write_text(
        json.dumps(configuration, ensure_ascii=False, indent=2), encoding="utf-8"
    )


def transcript_has_reminder(parts: list[dict]) -> bool:
    for part in parts:
        content = str(part.get("content") or "")
        if all(marker in content for marker in REMINDER_MARKERS):
            return True
    return False


def gateway_saw_reminder(requests: list[dict]) -> bool:
    for record in requests:
        try:
            body = json.loads(record.get("body") or "{}")
        except ValueError:
            continue
        for message in body.get("messages") or []:
            content = str(message.get("content") or "")
            if all(marker in content for marker in REMINDER_MARKERS):
                return True
    return False


def run_case(side: str, case: dict, context: dict) -> dict:
    case_id = case["id"]
    run_dir = context["out"] / "cases" / f"{side}-{case_id}"
    run_dir.mkdir(parents=True, exist_ok=True)
    gateway_log = Path(tempfile.mkdtemp(prefix="jsr-gateway-")) / "queen-requests.jsonl"
    home = Path(tempfile.mkdtemp(prefix=f"jsr-{side}-{case_id}-"))
    (home / "configuration.json").parent.mkdir(parents=True, exist_ok=True)

    gateway_options = {
        "model": STALL_MODEL,
        "title": TITLE,
        "later": case["gateway"].get("later", STALL_LATER),
        "reply-when": USER_MESSAGE,
    }
    if "announcement" in case["gateway"]:
        gateway_options["announcement"] = case["gateway"]["announcement"]
    if "tool-call" in case["gateway"]:
        gateway_options["tool-call"] = case["gateway"]["tool-call"]
        gateway_options["tool-reply"] = case["gateway"].get("tool-reply", "")
    gateway_proc, gateway_base = start_gateway(context["stallgw"], gateway_options, gateway_log)

    enabled = case.get("stall_check", True)
    judge_base = case.get("judge_base", context["judge_proxy"].base + "/v1")
    if side == "py":
        write_case_config(home, gateway_base, judge_base, case["threshold"], enabled)
    else:
        write_case_config_go(home, gateway_base, judge_base, case["threshold"], enabled)

    judge_index = context["judge_proxy"].mark(f"{side}-{case_id}")
    port = free_port()
    base = f"http://127.0.0.1:{port}"
    serve_log = run_dir / "server.log"
    serve = start_serve(
        side,
        context["repo_root"],
        home,
        port,
        serve_log,
        context["go_binary"],
        {"STALLGW_KEY": "stallgw-key"},
    )
    result: dict = {
        "side": side,
        "case": case_id,
        "home": str(home),
        "threshold": case["threshold"],
        "expect_judge": case.get("expect_judge"),
        "expect_reminder": case.get("expect_reminder"),
    }
    try:
        wait_health(base, serve, serve_log)
        status, body = http_json("POST", base + "/api/sessions", {})
        session_id = (body or {}).get("session_id") or ""
        result["session_id"] = session_id
        result["create_status"] = status
        time.sleep(1.0)
        status, features = http_json("GET", base + "/api/config/features")
        result["features"] = features

        watcher = SSEWatcher(f"{base}/api/sessions/{session_id}/events", run_dir / "sse-events.jsonl")
        deadline = time.time() + 20
        while time.time() < deadline and not watcher.events:
            time.sleep(0.2)

        if case.get("context_inject"):
            status, body = http_json(
                "POST",
                f"{base}/api/sessions/{session_id}/queen-context",
                {"message": case["context_inject"]},
            )
            result["context_inject_status"] = status
        else:
            status, body = http_json(
                "POST", f"{base}/api/sessions/{session_id}/chat", {"message": USER_MESSAGE}
            )
            result["chat_status"] = status

        session = session_dir(home, session_id)
        deadline = time.time() + (25 if case.get("context_inject") else 90)
        while time.time() < deadline:
            parts = read_parts(session)
            assistant = [p for p in parts if p.get("role") == "assistant"]
            judge_records = context["judge_proxy"].since(judge_index)
            gateway_hits = gateway_requests(gateway_log)
            queen_calls = [r for r in gateway_hits if r.get("kind") == "queen"]
            if case["expect"] == "remind":
                if judge_records and len(assistant) >= 2:
                    break
            elif case["expect"] == "quiet":
                if len(assistant) >= 1 and gateway_saw_reminder(gateway_hits):
                    break
                if len(assistant) >= 1:
                    # no reminder expected: settle a moment, then leave
                    time.sleep(2.5)
                    break
            else:  # no-judge
                if len(assistant) >= 1 or (case.get("context_inject") and queen_calls):
                    time.sleep(2.5)
                    break
            time.sleep(0.4)

        # settle: catch a late reminder or a second round
        time.sleep(2.0)
        watcher.close()

        parts = read_parts(session)
        judge_records = context["judge_proxy"].since(judge_index)
        gateway_hits = gateway_requests(gateway_log)

        result.update(
            {
                "parts": [part_digest(part) for part in parts],
                "judge_requests": judge_records,
                "queen_calls": [
                    {
                        "seq": record.get("seq"),
                        "kind": record.get("kind"),
                        "auth": record.get("auth"),
                        "messages": len((json.loads(record.get("body") or "{}").get("messages") or [])),
                        "has_reminder": gateway_saw_reminder([record]),
                    }
                    for record in gateway_hits
                ],
                "reminder_in_transcript": transcript_has_reminder(parts),
                "reminder_reached_model": gateway_saw_reminder(gateway_hits),
                "sse_event_types": [event.get("type") for event in watcher.events],
                "sse_carrying_reminder": [
                    event
                    for event in watcher.events
                    if "system-reminder" in json.dumps(event, ensure_ascii=False)
                ],
                "events_jsonl": _read_jsonl(session / "events.jsonl"),
            }
        )
        if side == "py":
            shutil.copyfile(session / "events.jsonl", run_dir / "session-events.jsonl")
        else:
            shutil.copyfile(session / "events.jsonl", run_dir / "session-events.jsonl")
        (run_dir / "transcript.json").write_text(
            json.dumps(result["parts"], ensure_ascii=False, indent=2), encoding="utf-8"
        )
        result["server_log_tail"] = serve_log.read_text(encoding="utf-8", errors="replace")[-4000:]
        result["gateway_log"] = str(gateway_log)
        return result
    finally:
        stop_proc(serve)
        stop_proc(gateway_proc)


def _read_jsonl(path: Path) -> list[dict]:
    if not path.exists():
        return []
    out = []
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            out.append(json.loads(line))
        except ValueError:
            continue
    return out


def case_summary(result: dict) -> str:
    lines = [f"case {result['side']}-{result['case']}", f"session {result.get('session_id')}", ""]
    lines.append(f"chat_status={result.get('chat_status')} judge_requests={len(result['judge_requests'])}")
    for record in result["judge_requests"]:
        lines.append(f"  judge[{record['tag']}] {record['status']} {record['ms']}ms")
        lines.append("  request: " + record["request"][:1500])
        lines.append("  response: " + (record["response"] or "")[:400])
    lines.append(f"reminder_in_transcript={result['reminder_in_transcript']}")
    lines.append(f"reminder_reached_model={result['reminder_reached_model']}")
    lines.append("queen calls:")
    for call in result["queen_calls"]:
        lines.append(f"  seq={call['seq']} kind={call['kind']} messages={call['messages']} reminder={call['has_reminder']}")
    lines.append("transcript:")
    for part in result["parts"]:
        lines.append(f"  {part['seq']:>3} {part['role']:<9} client_input={part['is_client_input']} content={part['content'][:160]!r}")
    lines.append("sse event types: " + ", ".join(result["sse_event_types"]))
    return "\n".join(lines) + "\n"



# --------------------------------------------------------------------------- #
# main
# --------------------------------------------------------------------------- #


def judge_endpoint(judge_url: str) -> str:
    return judge_url.rstrip("/") + "/v1/systemone"


def judge_health(judge_url: str) -> dict:
    for path in ("/v1/health", "/health"):
        try:
            status, body = http_json("GET", judge_url.rstrip("/") + path, timeout=5)
            if status == 200 and isinstance(body, dict):
                return {"path": path, "body": body}
        except Exception:
            continue
    return {}


def stability_probe(judge_url: str, request_body: str, repeats: int = 3) -> list[dict]:
    body = json.loads(request_body)
    out = []
    for index in range(repeats):
        started = time.time()
        status, payload = http_json(
            "POST", judge_endpoint(judge_url), body, timeout=60, headers={"Authorization": "Bearer stability-probe"}
        )
        out.append(
            {
                "repeat": index + 1,
                "status": status,
                "noul": (payload or {}).get("answers", {}).get("answer", {}).get("noul"),
                "ms": round((time.time() - started) * 1000),
            }
        )
    return out


def compare_bodies(results: list[dict]) -> list[dict]:
    """Deep-compare the judge request bodies Python and Go sent per case."""
    by_case: dict[str, dict[str, dict]] = {}
    for result in results:
        if result["judge_requests"]:
            by_case.setdefault(result["case"], {})[result["side"]] = result["judge_requests"][0]
    comparisons = []
    for case_id, sides in sorted(by_case.items()):
        py, go = sides.get("py"), sides.get("go")
        if not py or not go:
            comparisons.append({"case": case_id, "equal": None, "note": "one side missing"})
            continue
        py_body = json.loads(py["request"])
        go_body = json.loads(go["request"])
        comparisons.append(
            {
                "case": case_id,
                "equal": py_body == go_body,
                "python": py_body,
                "go": go_body,
                "python_p": json.loads(py["response"])["answers"]["answer"]["noul"],
                "go_p": json.loads(go["response"])["answers"]["answer"]["noul"],
            }
        )
    return comparisons


def main() -> int:
    parser = argparse.ArgumentParser()
    script_repo = Path(__file__).resolve().parents[2]
    parser.add_argument("--repo-root", default=str(script_repo))
    parser.add_argument("--go-module", default="")
    parser.add_argument("--out", default="")
    parser.add_argument("--judge-url", default="http://127.0.0.1:18099")
    parser.add_argument("--only", default="", help="comma-separated case ids")
    parser.add_argument("--sides", default="py,go")
    args = parser.parse_args()

    repo_root = Path(args.repo_root).resolve()
    go_module = Path(args.go_module).resolve() if args.go_module else repo_root / "go_hive/go-hive"
    out = Path(args.out).resolve() if args.out else repo_root / "go_hive/docs/e2e-assets/judge-stall-real"
    out.mkdir(parents=True, exist_ok=True)

    health = judge_health(args.judge_url)
    if not health:
        raise SystemExit(f"real judge not reachable at {args.judge_url}")
    log(f"judge healthy: {json.dumps(health, ensure_ascii=False)[:300]}")

    build_dir = Path(tempfile.mkdtemp(prefix="jsr-build-"))
    stallgw_bin = build_dir / "stallgw"
    hive_go_bin = build_dir / "hive-go"
    env = {**os.environ, "CGO_ENABLED": "0", "GOPROXY": "off"}
    subprocess.run(["go", "build", "-o", str(stallgw_bin), "./e2e/cmd/stallgw"], cwd=go_module, env=env, check=True)
    subprocess.run(["go", "build", "-o", str(hive_go_bin), "./cmd/hive"], cwd=go_module, env=env, check=True)

    judge_proxy = JudgeProxy(args.judge_url, out / "judge-requests.jsonl")
    (out / "judge-requests.jsonl").unlink(missing_ok=True)
    context = {
        "out": out,
        "repo_root": repo_root,
        "stallgw": stallgw_bin,
        "go_binary": hive_go_bin,
        "judge_proxy": judge_proxy,
    }

    selected = set(filter(None, args.only.split(","))) if args.only else None
    sides = [side for side in args.sides.split(",") if side]
    results = []
    try:
        for side in sides:
            for case in CASES:
                if selected and case["id"] not in selected:
                    continue
                log(f"run {side}-{case['id']}")
                result = run_case(side, case, context)
                results.append(result)
                case_dir = out / "cases" / f"{side}-{case['id']}"
                (case_dir / "summary.txt").write_text(case_summary(result), encoding="utf-8")
                (case_dir / "judge-requests.jsonl").write_text(
                    "".join(json.dumps(record, ensure_ascii=False) + "\n" for record in result["judge_requests"]),
                    encoding="utf-8",
                )
                (case_dir / "queen-calls.jsonl").write_text(
                    "".join(json.dumps(call, ensure_ascii=False) + "\n" for call in result["queen_calls"]),
                    encoding="utf-8",
                )
                if result.get("gateway_log"):
                    trimmed = trim_gateway_requests(Path(result["gateway_log"]))
                    (case_dir / "queen-requests.jsonl").write_text(
                        "".join(json.dumps(call, ensure_ascii=False) + "\n" for call in trimmed),
                        encoding="utf-8",
                    )
                (case_dir / "sse-reminder-events.jsonl").write_text(
                    "".join(json.dumps(event, ensure_ascii=False) + "\n" for event in result["sse_carrying_reminder"]),
                    encoding="utf-8",
                )


        # Stability: replay each side's first trigger request against the real
        # endpoint, three times, from the captured bytes.
        stability = {}
        for result in results:
            if result["case"] in ("stall-0.5", "borderline-0.5") and result["judge_requests"]:
                stability[f"{result['side']}-{result['case']}"] = stability_probe(
                    args.judge_url, result["judge_requests"][0]["request"]
                )
        comparisons = compare_bodies(results)
        (out / "judge-stability.json").write_text(
            json.dumps(stability, ensure_ascii=False, indent=2), encoding="utf-8"
        )
        (out / "body-comparison.json").write_text(
            json.dumps(comparisons, ensure_ascii=False, indent=2), encoding="utf-8"
        )

        summary = build_summary(results, comparisons, stability, health, args.judge_url)
        (out / "summary.json").write_text(
            json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8"
        )
        (out / "summary.md").write_text(render_summary_md(summary), encoding="utf-8")
        log("evidence written to " + str(out))
    finally:
        judge_proxy.server.shutdown()
    return 0



# --------------------------------------------------------------------------- #
# summary
# --------------------------------------------------------------------------- #


def build_summary(results, comparisons, stability, health, judge_url) -> dict:
    cases = []
    for result in results:
        p = None
        if result["judge_requests"]:
            try:
                p = json.loads(result["judge_requests"][0]["response"])["answers"]["answer"]["noul"]
            except (ValueError, KeyError, TypeError):
                p = None
        reminded = bool(result["reminder_in_transcript"]) and bool(result["reminder_reached_model"])
        expected_judge = result.get("expect_judge")
        expected_reminder = result.get("expect_reminder")
        verdict = {
            "judge_requests_ok": expected_judge is None or len(result["judge_requests"]) == expected_judge,
            "reminder_ok": expected_reminder is None or reminded == expected_reminder,
        }
        verdict["pass"] = all(verdict.values())
        cases.append(
            {
                "side": result["side"],
                "case": result["case"],
                "threshold": result.get("threshold"),
                "verdict": verdict,
                "session": result.get("session_id"),
                "judge_requests": len(result["judge_requests"]),
                "p": p,
                "reminder_in_transcript": result["reminder_in_transcript"],
                "reminder_reached_model": result["reminder_reached_model"],
                "reminded": reminded,
                "assistant_parts": len([part for part in result["parts"] if part["role"] == "assistant"]),
                "tool_parts": len([part for part in result["parts"] if part["role"] == "tool"]),
                "queen_calls": len(result["queen_calls"]),
                "features": (result.get("features") or {}).get("features") or result.get("features"),
                "server_log_tail": result.get("server_log_tail", ""),
            }
        )
    return {
        "generated": time.strftime("%Y-%m-%d %H:%M:%S %Z"),
        "judge_url": judge_url,
        "judge_health": health,
        "cases": cases,
        "body_comparisons": comparisons,
        "stability": stability,
    }


def render_summary_md(summary: dict) -> str:
    lines = [
        "# judge-stall-real 实测汇总",
        "",
        f"- 生成：{summary['generated']}",
        f"- judge：`{summary['judge_url']}` · {json.dumps(summary['judge_health'].get('body', {}), ensure_ascii=False)[:220]}",
        "",
        "| side | case | threshold | judge 请求 | p | 阈值比较 | 提醒进入转录 | 提醒到模型 | assistant/tool | 判定 |",
        "| --- | --- | ---: | ---: | ---: | --- | --- | --- | --- | --- |",
    ]
    for case in summary["cases"]:
        p = "—" if case["p"] is None else f"{case['p']:.4f}"
        threshold = case.get("threshold")
        if case["p"] is not None and threshold is not None:
            compared = "p≥阈值→提醒" if case["p"] >= threshold else "p<阈值→不提醒"
        else:
            compared = "—"
        lines.append(
            f"| {case['side']} | {case['case']} | {threshold} | {case['judge_requests']} | {p} | {compared} | "
            f"{'是' if case['reminder_in_transcript'] else '否'} | "
            f"{'是' if case['reminder_reached_model'] else '否'} | "
            f"{case['assistant_parts']}/{case['tool_parts']} | "
            f"{'PASS' if case['verdict']['pass'] else 'CHECK ' + json.dumps(case['verdict'])} |"
        )
    lines += ["", "## judge 请求体两侧比对", ""]
    for comparison in summary["body_comparisons"]:
        if comparison["equal"] is None:
            lines.append(f"- `{comparison['case']}`: {comparison.get('note')}")
            continue
        lines.append(
            f"- `{comparison['case']}`: 请求体{'一致' if comparison['equal'] else '不一致'}；"
            f"p(py)={comparison['python_p']:.6f} p(go)={comparison['go_p']:.6f}"
        )
    lines += ["", "## 同一请求 3 次 p 稳定性", ""]
    for name, probes in summary["stability"].items():
        values = ", ".join(f"{probe['noul']:.6f}" for probe in probes if probe["noul"] is not None)
        lines.append(f"- `{name}`: {values}")
    lines.append("")
    return "\n".join(lines)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt:
        raise SystemExit(130)
