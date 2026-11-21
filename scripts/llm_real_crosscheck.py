#!/usr/bin/env python3
"""Real-LLM crosscheck: Python Hive ↔ Go Hive against one real upstream model.

The probe (stdlib only) runs each side's *real* provider path against a local
pass-through recording proxy, which forwards every request to the real
upstream in the configured ``~/.hive-go/configuration.json`` ``llm`` slot and
records the exact JSON request/response payloads (the ``Authorization`` header
value is never recorded or printed). It then diffs, per scenario, the wire
request bodies field by field, the client-side stream event sequences, the
final text / finish reason / usage, the tool-selection shapes and the failure
paths, and writes the evidence set under ``--out``.

Scenarios (same prompts, temperature=0 on both sides):

  chat_minimal   3 × stream "Reply with exactly: pong" (minimal system)
  frozen_system  1 × stream with the frozen queen system prompt (sha recheck)
  tool_time      1 × stream "What time is it? Use get_current_time" with the
                 real tool definition, then the follow-up turn after the
                 tool result is backfilled
  unauthorized   1 × stream with a wrong key → real upstream 401

Everything is opt-in (``HIVE_REAL_LLM_E2E=1``) and never runs in ``make ci``.

Usage:

    HIVE_REAL_LLM_E2E=1 uv run --offline python go_hive/scripts/llm_real_crosscheck.py \
        --out docs/e2e-assets/llm-real [--skip-go] [--skip-python]

Exit code 0 means the probe completed; per-scenario verdicts live in
``compare.json`` (differences are evidence, not failures).
"""

from __future__ import annotations

import argparse
import hashlib
import http.client
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_REAL_HOME = Path.home() / ".hive-go"
DEFAULT_OUT = REPO_ROOT / "docs" / "e2e-assets" / "llm-real"
DEFAULT_SCENARIOS = "chat_minimal,frozen_system,tool_time,unauthorized"
UPSTREAM_TIMEOUT_S = 600.0

# Headers never forwarded verbatim and never recorded by value.
SKIP_FORWARD_HEADERS = {"host", "content-length", "connection", "accept-encoding", "keep-alive", "proxy-connection", "transfer-encoding"}
REDACT_HEADERS = {"authorization", "x-api-key", "api-key", "cookie"}


def log(message: str) -> None:
    print(f"[llm-crosscheck] {message}", flush=True)


# ---------------------------------------------------------------------------
# key resolution (values never logged or recorded)
# ---------------------------------------------------------------------------


def resolve_key_env_var(real_home: Path) -> str:
    config = json.loads((real_home / "configuration.json").read_text(encoding="utf-8"))
    llm = config.get("llm") or {}
    env_name = str(llm.get("api_key_env_var") or "").strip()
    if not env_name:
        raise SystemExit(f"configuration.json llm.api_key_env_var is not set in {real_home}")
    if os.environ.get(env_name):
        return env_name
    env_file = real_home / "env"
    if env_file.is_file():
        for raw_line in env_file.read_text(encoding="utf-8").splitlines():
            line = raw_line.strip()
            if not line or line.startswith("#"):
                continue
            if line.startswith("export "):
                line = line[len("export ") :]
            name, _, value = line.partition("=")
            if name.strip() == env_name and value.strip():
                os.environ[env_name] = value.strip().strip('"').strip("'")
                log(f"loaded {env_name} from {env_file} (value never printed)")
                return env_name
    raise SystemExit(
        f"env var {env_name} is not set and {env_file} has no value for it; "
        "export it (or fill the env file) before running the crosscheck"
    )


def upstream_from_config(real_home: Path) -> str:
    config = json.loads((real_home / "configuration.json").read_text(encoding="utf-8"))
    llm = config.get("llm") or {}
    api_base = str(llm.get("api_base") or "").strip()
    if not api_base.startswith("http"):
        raise SystemExit(f"configuration.json llm.api_base is not an http(s) URL: {api_base!r}")
    return api_base


def build_fixture_home(real_home: Path, proxy_base: str, dest: Path) -> Path:
    """Copy the real configuration.json into a fixture home with api_base→proxy."""
    dest.mkdir(parents=True, exist_ok=True)
    config = json.loads((real_home / "configuration.json").read_text(encoding="utf-8"))
    llm = config.setdefault("llm", {})
    llm["api_base"] = proxy_base
    (dest / "configuration.json").write_text(json.dumps(config, indent=1) + "\n", encoding="utf-8")
    return dest


# ---------------------------------------------------------------------------
# recording pass-through proxy
# ---------------------------------------------------------------------------


class RecordingProxy:
    """Forward POST/GET to the real upstream, record exact JSON payloads.

    The Authorization header value is replaced by ``Bearer <redacted>`` in the
    recording; everything else is recorded verbatim. Accept-Encoding is
    stripped on the way out so the recorded SSE body is always plain text.
    """

    def __init__(self, upstream_base: str, record_path: Path) -> None:
        parsed = urllib.parse.urlsplit(upstream_base.rstrip("/"))
        self.upstream_scheme = parsed.scheme
        self.upstream_host = parsed.hostname or ""
        self.upstream_port = parsed.port or (443 if parsed.scheme == "https" else 80)
        self.upstream_path_prefix = parsed.path.rstrip("/")
        self.record_path = record_path
        self._lock = threading.Lock()
        self._seq = 0
        self.current_label = "unlabeled"
        self.records: list[dict[str, Any]] = []

        outer = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, fmt: str, *args: Any) -> None:  # noqa: A002
                return

            def _handle(self, method: str) -> None:
                length = int(self.headers.get("Content-Length") or 0)
                raw_body = self.rfile.read(length) if length else b""
                # Client may gzip/deflate a request body; both runtimes send
                # plain JSON, so pass through untouched and record raw bytes.
                body_json: Any = None
                if raw_body:
                    try:
                        body_json = json.loads(raw_body.decode("utf-8"))
                    except (UnicodeDecodeError, json.JSONDecodeError):
                        body_json = None
                request_headers = {k.lower(): v for k, v in self.headers.items()}
                forward_headers = {
                    k: v for k, v in request_headers.items() if k not in SKIP_FORWARD_HEADERS
                }
                forward_headers["Accept-Encoding"] = "identity"
                forward_headers["Content-Length"] = str(len(raw_body))

                started = time.time()
                status, resp_headers, resp_body, resp_error = outer.forward(
                    method, self.path, forward_headers, raw_body
                )
                duration_ms = round((time.time() - started) * 1000, 1)

                outer.record(
                    {
                        "label": outer.current_label,
                        "method": method,
                        "path": self.path,
                        "request_headers": {
                            k: ("Bearer <redacted>" if k in REDACT_HEADERS else v)
                            for k, v in request_headers.items()
                        },
                        "request_body": body_json,
                        "request_body_sha256": hashlib.sha256(raw_body).hexdigest() if raw_body else None,
                        "response_status": status,
                        "response_headers": resp_headers,
                        "response_body_text": resp_body.decode("utf-8", "replace") if resp_body else "",
                        "response_error": resp_error,
                        "duration_ms": duration_ms,
                    }
                )

                if resp_error:
                    payload = json.dumps({"error": {"message": f"crosscheck proxy: {resp_error}"}}).encode()
                    self.send_response(502)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(payload)))
                    self.send_header("Connection", "close")
                    self.end_headers()
                    self.wfile.write(payload)
                    self.close_connection = True
                    return

                self.send_response(status)
                for key, value in (resp_headers or {}).items():
                    if key.lower() in ("transfer-encoding", "content-length", "connection", "content-encoding"):
                        continue
                    self.send_header(key, value)
                self.send_header("Connection", "close")
                self.end_headers()
                if resp_body:
                    self.wfile.write(resp_body)
                self.wfile.flush()
                self.close_connection = True

            def do_POST(self) -> None:  # noqa: N802
                self._handle("POST")

            def do_GET(self) -> None:  # noqa: N802
                self._handle("GET")

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}"

    def start(self) -> "RecordingProxy":
        self.thread.start()
        return self

    def stop(self) -> None:
        self.server.shutdown()
        self.server.server_close()

    def record(self, record: dict[str, Any]) -> None:
        with self._lock:
            self._seq += 1
            record["seq"] = self._seq
            self.records.append(record)
            with self.record_path.open("a", encoding="utf-8") as handle:
                handle.write(json.dumps(record, ensure_ascii=False) + "\n")

    def forward(
        self, method: str, path: str, headers: dict[str, str], body: bytes
    ) -> tuple[int, dict[str, str], bytes, str | None]:
        target = path
        if not target.startswith("/"):
            target = "/" + target
        try:
            if self.upstream_scheme == "https":
                conn: http.client.HTTPConnection = http.client.HTTPSConnection(
                    self.upstream_host, self.upstream_port, timeout=UPSTREAM_TIMEOUT_S
                )
            else:
                conn = http.client.HTTPConnection(
                    self.upstream_host, self.upstream_port, timeout=UPSTREAM_TIMEOUT_S
                )
            conn.request(method, target, body=body, headers=headers)
            response = conn.getresponse()
            payload = response.read()
            resp_headers = {k.lower(): v for k, v in response.getheaders()}
            conn.close()
            return response.status, resp_headers, payload, None
        except Exception as exc:  # noqa: BLE001 - surfaced to the client and recorded
            return 0, {}, b"", f"{type(exc).__name__}: {exc}"


# ---------------------------------------------------------------------------
# harness drivers
# ---------------------------------------------------------------------------


def run_command(command: list[str], *, cwd: Path, env: dict[str, str], timeout: float) -> subprocess.CompletedProcess:
    return subprocess.run(
        command,
        cwd=str(cwd),
        env=env,
        text=True,
        capture_output=True,
        timeout=timeout,
        check=False,
    )


def run_python_side(
    fixture_home: Path, out_json: Path, scenarios: str, reps: int, key_env: str
) -> None:
    env = dict(os.environ)
    env.update(
        {
            "HIVE_HOME": str(fixture_home),
            "HIVE_REAL_LLM_E2E": "1",
        }
    )
    if not env.get(key_env):
        raise SystemExit(f"{key_env} is not set in the harness environment")
    command = [
        "uv",
        "run",
        "--offline",
        "python",
        "go_hive/scripts/llm_real_crosscheck_python.py",
        "--out",
        str(out_json),
        "--scenarios",
        scenarios,
        "--reps",
        str(reps),
    ]
    log(f"python harness: {' '.join(command)}")
    result = run_command(command, cwd=REPO_ROOT, env=env, timeout=1800)
    sys.stdout.write(result.stdout)
    sys.stderr.write(result.stderr)
    if result.returncode != 0:
        raise SystemExit(f"python harness failed with exit {result.returncode}")


def run_go_side(
    fixture_home: Path, out_json: Path, scenarios: str, reps: int, key_env: str, go_module: Path
) -> None:
    env = dict(os.environ)
    env.update(
        {
            "HIVE_HOME": str(fixture_home),
            "HIVE_REAL_LLM_E2E": "1",
            "HIVE_LLM_CROSSCHECK_OUT": str(out_json),
            "HIVE_LLM_CROSSCHECK_SCENARIOS": scenarios,
            "HIVE_LLM_CROSSCHECK_REPS": str(reps),
        }
    )
    if not env.get(key_env):
        raise SystemExit(f"{key_env} is not set in the harness environment")
    command = [
        "go",
        "test",
        "./internal/cli",
        "-run",
        "TestLLMRealCrosscheckE2E",
        "-count=1",
        "-v",
        "-timeout",
        "30m",
    ]
    log(f"go harness: {' '.join(command)}")
    result = run_command(command, cwd=go_module, env=env, timeout=2400)
    sys.stdout.write(result.stdout)
    sys.stderr.write(result.stderr)
    if result.returncode != 0:
        raise SystemExit(f"go harness failed with exit {result.returncode}")


# ---------------------------------------------------------------------------
# comparison
# ---------------------------------------------------------------------------


def canon(value: Any) -> Any:
    """JSON-semantic canonical form (dict key order, 0 vs 0.0, 1 vs 1.0)."""
    if isinstance(value, dict):
        return {k: canon(v) for k, v in value.items()}
    if isinstance(value, list):
        return [canon(v) for v in value]
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return round(float(value), 9)
    return value


def parse_arguments(raw: Any) -> Any:
    if isinstance(raw, str):
        try:
            return json.loads(raw)
        except json.JSONDecodeError:
            return raw
    return raw


def diff_message_lists(py_msgs: list[Any], go_msgs: list[Any], *, dynamic_id: bool = True) -> list[dict[str, Any]]:
    """Structural diff of the messages array that tolerates dynamic fields."""
    diffs: list[dict[str, Any]] = []
    if len(py_msgs) != len(go_msgs):
        diffs.append({"field": "messages.length", "python": len(py_msgs), "go": len(go_msgs)})
    for index, (py_msg, go_msg) in enumerate(zip(py_msgs, go_msgs)):
        path = f"messages[{index}]"
        if not isinstance(py_msg, dict) or not isinstance(go_msg, dict):
            diffs.append({"field": path, "python": py_msg, "go": go_msg})
            continue
        py_keys, go_keys = set(py_msg), set(go_msg)
        if py_keys != go_keys:
            diffs.append({"field": f"{path}.keys", "python": sorted(py_keys), "go": sorted(go_keys)})
        for key in sorted(py_keys & go_keys):
            py_val, go_val = py_msg[key], go_msg[key]
            if key == "content":
                if isinstance(py_val, list) or isinstance(go_val, list):
                    if canon(py_val) != canon(go_val):
                        diffs.append({"field": f"{path}.content", "python": py_val, "go": go_val})
                elif (py_val is None) != (go_val is None):
                    diffs.append({"field": f"{path}.content_null", "python": py_val, "go": go_val})
                elif py_val != go_val:
                    diffs.append(
                        {
                            "field": f"{path}.content",
                            "python": {"sha256": hashlib.sha256(str(py_val).encode()).hexdigest(), "len": len(str(py_val))},
                            "go": {"sha256": hashlib.sha256(str(go_val).encode()).hexdigest(), "len": len(str(go_val))},
                        }
                    )
            elif key == "tool_calls":
                py_calls = py_val if isinstance(py_val, list) else []
                go_calls = go_val if isinstance(go_val, list) else []
                if len(py_calls) != len(go_calls):
                    diffs.append({"field": f"{path}.tool_calls.length", "python": len(py_calls), "go": len(go_calls)})
                for call_index, (py_call, go_call) in enumerate(zip(py_calls, go_calls)):
                    call_path = f"{path}.tool_calls[{call_index}]"
                    if set(py_call) != set(go_call):
                        diffs.append({"field": f"{call_path}.keys", "python": sorted(py_call), "go": sorted(go_call)})
                    if call_path and py_call.get("type") != go_call.get("type"):
                        diffs.append({"field": f"{call_path}.type", "python": py_call.get("type"), "go": go_call.get("type")})
                    py_fn, go_fn = py_call.get("function") or {}, go_call.get("function") or {}
                    if py_fn.get("name") != go_fn.get("name"):
                        diffs.append({"field": f"{call_path}.function.name", "python": py_fn.get("name"), "go": go_fn.get("name")})
                    py_args, go_args = parse_arguments(py_fn.get("arguments")), parse_arguments(go_fn.get("arguments"))
                    if canon(py_args) != canon(go_args):
                        diffs.append({"field": f"{call_path}.function.arguments", "python": py_args, "go": go_args})
                    elif py_fn.get("arguments") != go_fn.get("arguments"):
                        diffs.append(
                            {
                                "field": f"{call_path}.function.arguments.bytes",
                                "python": py_fn.get("arguments"),
                                "go": go_fn.get("arguments"),
                                "note": "same JSON value, different byte serialization",
                            }
                        )
                    if dynamic_id and (bool(py_call.get("id")) != bool(go_call.get("id"))):
                        diffs.append({"field": f"{call_path}.id_present", "python": bool(py_call.get("id")), "go": bool(go_call.get("id"))})
            elif key == "tool_call_id":
                if bool(py_val) != bool(go_val):
                    diffs.append({"field": f"{path}.tool_call_id_present", "python": bool(py_val), "go": bool(go_val)})
            elif canon(py_val) != canon(go_val):
                diffs.append({"field": f"{path}.{key}", "python": py_val, "go": go_val})
    return diffs


def diff_tool_lists(py_tools: list[Any], go_tools: list[Any]) -> list[dict[str, Any]]:
    diffs: list[dict[str, Any]] = []
    if len(py_tools) != len(go_tools):
        diffs.append({"field": "tools.length", "python": len(py_tools), "go": len(go_tools)})
    for index, (py_tool, go_tool) in enumerate(zip(py_tools, go_tools)):
        path = f"tools[{index}]"
        if canon(py_tool) != canon(go_tool):
            py_fn = (py_tool or {}).get("function") or {}
            go_fn = (go_tool or {}).get("function") or {}
            if py_fn.get("name") != go_fn.get("name"):
                diffs.append({"field": f"{path}.function.name", "python": py_fn.get("name"), "go": go_fn.get("name")})
            if py_fn.get("description") != go_fn.get("description"):
                diffs.append(
                    {
                        "field": f"{path}.function.description",
                        "python": {"sha256": hashlib.sha256(str(py_fn.get("description")).encode()).hexdigest()},
                        "go": {"sha256": hashlib.sha256(str(go_fn.get("description")).encode()).hexdigest()},
                    }
                )
            if canon(py_fn.get("parameters")) != canon(go_fn.get("parameters")):
                diffs.append(
                    {
                        "field": f"{path}.function.parameters",
                        "python": py_fn.get("parameters"),
                        "go": go_fn.get("parameters"),
                    }
                )
            if not any(d["field"].startswith(path) for d in diffs):
                diffs.append({"field": path, "python": py_tool, "go": go_tool})
    return diffs


def diff_request_bodies(py_body: dict[str, Any] | None, go_body: dict[str, Any] | None) -> dict[str, Any]:
    if py_body is None or go_body is None:
        return {"status": "diff", "diffs": [{"field": "<body>", "python": py_body is not None, "go": go_body is not None}]}
    diffs: list[dict[str, Any]] = []
    py_keys, go_keys = set(py_body), set(go_body)
    if py_keys != go_keys:
        diffs.append({"field": "keys", "python": sorted(py_keys), "go": sorted(go_keys)})
    for key in sorted(py_keys & go_keys):
        if key == "messages":
            diffs.extend(diff_message_lists(py_body[key], go_body[key]))
        elif key == "tools":
            diffs.extend(diff_tool_lists(py_body[key], go_body[key]))
        elif canon(py_body[key]) != canon(go_body[key]):
            diffs.append({"field": key, "python": py_body[key], "go": go_body[key]})
    return {"status": "pass" if not diffs else "diff", "diffs": diffs}


def project_events(events: list[dict[str, Any]]) -> list[str]:
    return [str(event.get("type")) for event in events]


def compare_side_results(py_scenario: dict[str, Any], go_scenario: dict[str, Any], scenario: str) -> dict[str, Any]:
    """Response-behavior comparison from the two harness result files.

    Every dimension reports a per-field root cause, not just pass/diff:
    a missing field (Python has it, Go does not — or the other way round) is a
    structural gap; a differing value is either a numeric/accounting
    difference or a behavior difference to attribute in the report.
    """
    out: dict[str, Any] = {}
    py_reps, go_reps = py_scenario.get("reps", []), go_scenario.get("reps", [])
    out["rep_count"] = {"python": len(py_reps), "go": len(go_reps)}
    behaviors = []
    for index, (py_rep, go_rep) in enumerate(zip(py_reps, go_reps)):
        py_r1 = py_rep.get("round1", py_rep)
        go_r1 = go_rep.get("round1", go_rep)
        entry: dict[str, Any] = {"rep": index}
        entry["final_text"] = {"python": py_r1.get("final_text"), "go": go_r1.get("final_text")}
        entry["finish_reason"] = {"python": py_r1.get("finish_reason"), "go": go_r1.get("finish_reason")}
        entry["usage"] = {
            "python": py_r1.get("usage"),
            "go": go_r1.get("usage"),
            "field_diffs": diff_usage(py_r1.get("usage") or {}, go_r1.get("usage") or {}),
        }
        py_types, go_types = project_events(py_r1.get("events", [])), project_events(go_r1.get("events", []))
        entry["event_types"] = {"python": py_types, "go": go_types}
        entry["event_diff"] = diff_event_sequences(py_r1.get("events", []), go_r1.get("events", []))
        entry["tool_calls"] = {
            "python": [{"name": c.get("name"), "arguments": c.get("arguments_parsed")} for c in py_r1.get("tool_calls", [])],
            "go": [{"name": c.get("name"), "arguments": parse_arguments(c.get("arguments_raw", c.get("arguments_parsed")))} for c in go_r1.get("tool_calls", [])],
        }
        entry["error"] = {"python": py_r1.get("error"), "go": go_r1.get("error")}
        if scenario == "tool_time":
            py_r2, go_r2 = py_rep.get("round2") or {}, go_rep.get("round2") or {}
            entry["round2_final_text"] = {"python": py_r2.get("final_text"), "go": go_r2.get("final_text")}
            entry["round2_final_text_normalized_equal"] = normalize_clock(str(py_r2.get("final_text") or "")) == normalize_clock(
                str(go_r2.get("final_text") or "")
            )
            entry["round2_finish_reason"] = {"python": py_r2.get("finish_reason"), "go": go_r2.get("finish_reason")}
            entry["round2_event_types"] = {
                "python": project_events(py_r2.get("events", [])),
                "go": project_events(go_r2.get("events", [])),
            }
            entry["round2_event_diff"] = diff_event_sequences(py_r2.get("events", []), go_r2.get("events", []))
            entry["round2_usage"] = {
                "python": py_r2.get("usage"),
                "go": go_r2.get("usage"),
                "field_diffs": diff_usage(py_r2.get("usage") or {}, go_r2.get("usage") or {}),
            }
        behaviors.append(entry)
    out["behaviors"] = behaviors

    # Verdicts: text/finish must match; usage and event sequences may diverge
    # but only with a recorded root cause.
    text_ok = all(b["final_text"]["python"] == b["final_text"]["go"] for b in behaviors)
    finish_ok = all(b["finish_reason"]["python"] == b["finish_reason"]["go"] for b in behaviors)
    usage_ok = all(not b["usage"]["field_diffs"] for b in behaviors)
    events_ok = all(
        b["event_diff"]["semantic_equal"] and not b["event_diff"]["missing"] and not b["event_diff"]["extra"]
        for b in behaviors
    )
    tools_ok = all(canon(b["tool_calls"]["python"]) == canon(b["tool_calls"]["go"]) for b in behaviors)
    out["verdict"] = {
        "final_text": "pass" if text_ok else "diff",
        "finish_reason": "pass" if finish_ok else "diff",
        "usage": "pass" if usage_ok else "diff",
        "event_sequence": "pass" if events_ok else "diff",
        "tool_calls": "pass" if tools_ok else "diff",
    }
    return out


def diff_usage(py_usage: dict[str, Any], go_usage: dict[str, Any]) -> list[dict[str, Any]]:
    """Per-field usage diff: missing field vs differing value."""
    diffs: list[dict[str, Any]] = []
    for field in sorted(set(py_usage) | set(go_usage)):
        py_val, go_val = py_usage.get(field), go_usage.get(field)
        if field not in py_usage:
            diffs.append({"field": field, "kind": "missing_python", "python": None, "go": go_val})
        elif field not in go_usage:
            diffs.append({"field": field, "kind": "missing_go", "python": py_val, "go": None})
        elif canon(py_val) != canon(go_val):
            diffs.append({"field": field, "kind": "value", "python": py_val, "go": go_val})
    return diffs


def diff_event_sequences(py_events: list[dict[str, Any]], go_events: list[dict[str, Any]]) -> dict[str, Any]:
    """Alignment diff of the two client-side stream event sequences.

    Comparison is at the provider StreamEvent layer; the known Go-only
    per-turn envelopes (execution_* / node_loop_* / llm_turn_complete) are a
    server-SSE-layer artifact and do not appear here (see
    go_hive/docs/sse-payload-parity.md §4.1).

    Raw sequences are kept for evidence, but the verdict uses a semantic
    projection: consecutive text_delta / reasoning_delta runs collapse to one
    entry, because upstream chunk boundaries differ between two independent
    calls and that is transport framing, not client behavior.
    """
    py_types = project_events(py_events)
    go_types = project_events(go_events)
    py_counts: dict[str, int] = {}
    go_counts: dict[str, int] = {}
    for etype in py_types:
        py_counts[etype] = py_counts.get(etype, 0) + 1
    for etype in go_types:
        go_counts[etype] = go_counts.get(etype, 0) + 1
    missing = sorted(set(py_counts) - set(go_counts))
    extra = sorted(set(go_counts) - set(py_counts))
    semantic_python = semantic_event_types(py_types)
    semantic_go = semantic_event_types(go_types)
    first_divergence = None
    for index, (py_type, go_type) in enumerate(zip(semantic_python, semantic_go)):
        if py_type != go_type:
            first_divergence = {"index": index, "python": py_type, "go": go_type}
            break
    if first_divergence is None and len(semantic_python) != len(semantic_go):
        first_divergence = {
            "index": min(len(semantic_python), len(semantic_go)),
            "python": semantic_python[-1] if semantic_python else None,
            "go": semantic_go[-1] if semantic_go else None,
            "note": "semantic sequences share a prefix; length differs",
        }

    classification = "pass"
    if "reasoning_delta" in py_counts and "reasoning_delta" not in go_counts:
        classification = "reasoning_delta_missing_go"
    elif "reasoning_delta" in go_counts and "reasoning_delta" not in py_counts:
        classification = "reasoning_delta_missing_python"
    elif first_divergence is not None or missing or extra:
        classification = "event_sequence_divergence"
    elif py_counts != go_counts:
        classification = "chunk_boundary_variance"
    return {
        "python_count": len(py_types),
        "go_count": len(go_types),
        "missing": missing,
        "extra": extra,
        "semantic_python": semantic_python,
        "semantic_go": semantic_go,
        "semantic_equal": semantic_python == semantic_go,
        "classification": classification,
        "count_delta": {etype: {"python": py_counts.get(etype, 0), "go": go_counts.get(etype, 0)} for etype in sorted(set(py_counts) | set(go_counts)) if py_counts.get(etype, 0) != go_counts.get(etype, 0)},
        "first_divergence": first_divergence,
    }


def semantic_event_types(types: list[str]) -> list[str]:
    """Collapse adjacent delta runs to one entry (framing-free projection)."""
    out: list[str] = []
    for etype in types:
        if out and out[-1] == etype and etype in ("text_delta", "reasoning_delta"):
            continue
        out.append(etype)
    return out


def compare_tools_to_golden(py_request: dict[str, Any] | None, go_request: dict[str, Any] | None) -> dict[str, Any]:
    """Compare both sides' tools[] against the frozen tool-contract golden.

    The golden is go_hive/go-hive/internal/cli/tool_contract_parity.json —
    the frozen Python model-visible contract the Go catalogue applies.
    """
    golden_path = REPO_ROOT / "go_hive" / "go-hive" / "internal" / "cli" / "tool_contract_parity.json"
    golden_doc = json.loads(golden_path.read_text(encoding="utf-8"))
    out: dict[str, Any] = {"golden": "go_hive/go-hive/internal/cli/tool_contract_parity.json", "tools": {}}
    for side, request in (("python", py_request), ("go", go_request)):
        entry: dict[str, Any] = {"present": False}
        tools = (request or {}).get("request_body", {}).get("tools") if request else None
        if isinstance(tools, list) and tools:
            wire = tools[0] if isinstance(tools[0], dict) else {}
            function = wire.get("function") or {}
            name = function.get("name")
            golden = golden_doc.get("tools", {}).get(name)
            entry["present"] = True
            entry["name"] = name
            entry["type"] = wire.get("type")
            if golden is None:
                entry["golden_entry"] = False
            else:
                entry["golden_entry"] = True
                entry["description_match"] = function.get("description") == golden.get("description")
                entry["parameters_match"] = canon(function.get("parameters")) == canon(golden.get("parameters"))
                if not entry["parameters_match"]:
                    entry["parameters"] = function.get("parameters")
                    entry["golden_parameters"] = golden.get("parameters")
        out["tools"][side] = entry
    out["match"] = all(
        out["tools"].get(side, {}).get("present")
        and out["tools"][side].get("golden_entry")
        and out["tools"][side].get("description_match")
        and out["tools"][side].get("parameters_match")
        for side in ("python", "go")
    )
    return out


def normalize_clock(text: str) -> str:
    """Blank wall-clock values so two independent tool answers compare by shape."""
    text = re.sub(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?", "<timestamp>", text)
    text = re.sub(r"\d{4}-\d{2}-\d{2}", "<date>", text)
    text = re.sub(r"\d{2}:\d{2}:\d{2}", "<time>", text)
    text = re.sub(r"\b\d{10}\b", "<epoch>", text)
    return text


def count_reasoning_chunks(record: dict[str, Any] | None) -> int:
    """Non-empty upstream `reasoning_content` chunks in a recorded response."""
    if not record:
        return 0
    body = record.get("response_body_text") or ""
    return len(re.findall(r'"reasoning_content":\s*"(?:[^"\\]|\\.)+"', body))


def normalize_tool_result(content: Any) -> Any:
    """Parse a tool-result string and blank the wall-clock values."""
    value: Any = content
    for _ in range(2):  # tolerate one legacy double-encoded layer
        if not isinstance(value, str):
            break
        try:
            value = json.loads(value)
        except (json.JSONDecodeError, TypeError):
            break
    if isinstance(value, dict):
        dynamic = {"datetime", "date", "time", "unix_timestamp", "timestamp"}
        return {key: ("<time>" if key in dynamic else normalize_tool_result(item)) for key, item in value.items()}
    if isinstance(value, list):
        return [normalize_tool_result(item) for item in value]
    return value


def compare_tool_results(py_request: dict[str, Any] | None, go_request: dict[str, Any] | None) -> dict[str, Any]:
    """Compare the second turn's tool message shape (values are wall-clock)."""
    def tool_message(request: dict[str, Any] | None) -> dict[str, Any] | None:
        messages = (request or {}).get("request_body", {}).get("messages") or []
        for message in messages:
            if isinstance(message, dict) and message.get("role") == "tool":
                return message
        return None

    py_message = tool_message(py_request)
    go_message = tool_message(go_request)
    out: dict[str, Any] = {
        "python": {"present": py_message is not None, "content_len": len(str((py_message or {}).get("content") or ""))},
        "go": {"present": go_message is not None, "content_len": len(str((go_message or {}).get("content") or ""))},
    }
    if py_message and go_message:
        py_norm = normalize_tool_result(py_message.get("content"))
        go_norm = normalize_tool_result(go_message.get("content"))
        out["shape_equal_after_normalization"] = canon(py_norm) == canon(go_norm)
        if not out["shape_equal_after_normalization"]:
            out["python_normalized"] = py_norm
            out["go_normalized"] = go_norm
        out["python_keys"] = sorted(py_norm) if isinstance(py_norm, dict) else None
        out["go_keys"] = sorted(go_norm) if isinstance(go_norm, dict) else None
    return out


def compare_failure(
    py_record: dict[str, Any] | None,
    go_record: dict[str, Any] | None,
    py_scenario: dict[str, Any],
    go_scenario: dict[str, Any],
) -> dict[str, Any]:
    """Failure-path comparison: status, upstream body shape, retries, surfaces."""

    def body_shape(record: dict[str, Any] | None) -> Any:
        raw = (record or {}).get("response_body_text") or ""
        try:
            decoded = json.loads(raw)
        except json.JSONDecodeError:
            return None
        if isinstance(decoded, dict) and isinstance(decoded.get("error"), dict):
            return sorted(decoded["error"])
        return sorted(decoded) if isinstance(decoded, dict) else None

    def rep_call(scenario: dict[str, Any]) -> dict[str, Any]:
        reps = scenario.get("reps") or [{}]
        return reps[0] if isinstance(reps[0], dict) else {}

    py_apart = rep_call(py_scenario)
    go_apart = rep_call(go_scenario)
    return {
        "http_status": {
            "python": (py_record or {}).get("response_status"),
            "go": (go_record or {}).get("response_status"),
        },
        "request_count": {"python": 1 if py_record else 0, "go": 1 if go_record else 0},
        "upstream_body_keys": {"python": body_shape(py_record), "go": body_shape(go_record)},
        "upstream_request_id_differs": (py_record or {}).get("response_body_text") != (go_record or {}).get("response_body_text"),
        "python_surface": py_apart.get("error"),
        "go_surface": go_apart.get("error"),
    }


def group_records(records: list[dict[str, Any]], label: str) -> list[dict[str, Any]]:
    return [r for r in records if r.get("label") == label]


def assign_wire_requests(
    records: list[dict[str, Any]], scenarios: list[str], reps: int, side_results: dict[str, Any]
) -> dict[str, list[dict[str, Any]]]:
    """Pair recordings with scenarios in harness execution order."""
    indexed = [r for r in records if str(r.get("path", "")).endswith("/chat/completions")]
    cursor = 0
    assignment: dict[str, list[dict[str, Any]]] = {}

    def take(count: int) -> list[dict[str, Any]]:
        nonlocal cursor
        chunk = indexed[cursor : cursor + count]
        cursor += count
        return chunk

    for scenario in scenarios:
        scenario_results = side_results.get("scenarios", {}).get(scenario, {})
        if scenario == "chat_minimal":
            assignment[scenario] = take(len(scenario_results.get("reps", [])))
        elif scenario == "frozen_system":
            assignment[scenario] = take(1)
        elif scenario == "tool_time":
            reps_list = scenario_results.get("reps", [])
            taken = []
            for rep in reps_list:
                taken.append(take(1))  # round 1
                if rep.get("round2") is not None:
                    taken.append(take(1))  # round 2
            assignment[scenario] = [r for group in taken for r in group]
        elif scenario == "unauthorized":
            assignment[scenario] = take(len(scenario_results.get("reps", [])))
    assignment["_extra"] = indexed[cursor:]
    return assignment


def body_summary(body: dict[str, Any] | None) -> dict[str, Any]:
    """A compact, dynamic-field-free projection of a request body."""
    if body is None:
        return {}
    summary: dict[str, Any] = {}
    for key, value in body.items():
        if key == "messages":
            messages = []
            for msg in value if isinstance(value, list) else []:
                if not isinstance(msg, dict):
                    continue
                projection: dict[str, Any] = {"role": msg.get("role")}
                content = msg.get("content")
                if isinstance(content, str):
                    projection["content"] = {"len": len(content), "sha256": hashlib.sha256(content.encode()).hexdigest()}
                else:
                    projection["content"] = content
                if "tool_calls" in msg:
                    projection["tool_calls"] = [
                        {
                            "type": call.get("type"),
                            "function": {
                                "name": (call.get("function") or {}).get("name"),
                                "arguments": (call.get("function") or {}).get("arguments"),
                            },
                        }
                        for call in msg.get("tool_calls") or []
                    ]
                for extra in ("tool_call_id", "reasoning_content", "thinking_blocks"):
                    if extra in msg:
                        projection[extra] = msg[extra]
                projection["keys"] = sorted(msg)
                messages.append(projection)
            summary["messages"] = messages
        elif key == "tools":
            summary["tools"] = value
        else:
            summary[key] = value
    return summary


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", default=str(DEFAULT_OUT), help="evidence directory")
    parser.add_argument("--real-home", default=str(DEFAULT_REAL_HOME))
    parser.add_argument("--scenarios", default=DEFAULT_SCENARIOS)
    parser.add_argument("--reps", type=int, default=3, help="chat_minimal reps")
    parser.add_argument("--skip-go", action="store_true")
    parser.add_argument("--skip-python", action="store_true")
    parser.add_argument(
        "--compare-only",
        action="store_true",
        help="skip both harnesses; re-compare the existing recordings.jsonl + results files",
    )
    parser.add_argument("--scratch", default=os.environ.get("TMPDIR", "/tmp"), help="fixture home parent")
    args = parser.parse_args()

    if os.environ.get("HIVE_REAL_LLM_E2E") != "1":
        raise SystemExit("opt-in required: set HIVE_REAL_LLM_E2E=1")

    out_dir = Path(args.out).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    real_home = Path(args.real_home).expanduser().resolve()
    key_env = resolve_key_env_var(real_home)
    upstream_base = upstream_from_config(real_home)
    scenarios = [s for s in args.scenarios.split(",") if s]

    record_path = out_dir / "recordings.jsonl"
    py_results_path = out_dir / "python-results.json"
    go_results_path = out_dir / "go-results.json"
    records: list[dict[str, Any]] = []

    if args.compare_only:
        if not record_path.exists() or not py_results_path.exists() or not go_results_path.exists():
            raise SystemExit(
                "--compare-only needs recordings.jsonl, python-results.json and go-results.json in the evidence dir"
            )
        records = [json.loads(line) for line in record_path.read_text(encoding="utf-8").splitlines() if line.strip()]
        log(f"compare-only: {len(records)} recorded requests")
    else:
        if record_path.exists():
            record_path.unlink()
        # Clear stale side results up front: a previous run's file must never be
        # compared against this run's recordings.
        if not args.skip_python and py_results_path.exists():
            py_results_path.unlink()
        if not args.skip_go and go_results_path.exists():
            go_results_path.unlink()

        proxy = RecordingProxy(upstream_base, record_path).start()
        log(f"recording proxy on {proxy.base} → {upstream_base} (authorization redacted)")
        fixture_home = build_fixture_home(
            real_home, f"{proxy.base}{urllib.parse.urlsplit(upstream_base).path.rstrip('/')}",
            Path(args.scratch) / "llm-real-crosscheck-home",
        )
        log(f"fixture home: {fixture_home}")
        try:
            if not args.skip_python:
                proxy.current_label = "python"
                run_python_side(fixture_home, py_results_path, ",".join(scenarios), args.reps, key_env)
            if not args.skip_go:
                proxy.current_label = "go"
                run_go_side(fixture_home, go_results_path, ",".join(scenarios), args.reps, key_env, REPO_ROOT / "go_hive" / "go-hive")
        finally:
            proxy.stop()
        records = proxy.records

    # ------------------------------------------------------------------
    # compare
    # ------------------------------------------------------------------
    compare: dict[str, Any] = {
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "upstream": upstream_base,
        "scenarios": {},
        "verdict": {},
    }
    py_results = json.loads(py_results_path.read_text()) if py_results_path.exists() else None
    go_results = json.loads(go_results_path.read_text()) if go_results_path.exists() else None
    compare["sides"] = {
        "python": None if py_results is None else py_results.get("tenant"),
        "go": None if go_results is None else go_results.get("tenant"),
    }
    if py_results is None or go_results is None:
        log("both sides required for the comparison; writing partial evidence only")
        (out_dir / "compare.json").write_text(json.dumps(compare, indent=1, ensure_ascii=False) + "\n")
        return 0

    py_records = group_records(records, "python")
    go_records = group_records(records, "go")
    if (not args.skip_python and not py_records) or (not args.skip_go and not go_records):
        compare["verdict"] = {
            "status": "incomplete",
            "reason": (
                f"one side produced no recorded requests (python={len(py_records)} "
                f"go={len(go_records)}); a single-sided run is not a crosscheck"
            ),
        }
        (out_dir / "compare.json").write_text(
            json.dumps(compare, indent=1, ensure_ascii=False) + "\n", encoding="utf-8"
        )
        log("incomplete: " + compare["verdict"]["reason"])
        return 1
    py_wire = assign_wire_requests(py_records, scenarios, args.reps, py_results)
    go_wire = assign_wire_requests(go_records, scenarios, args.reps, go_results)

    def expected_requests(results: dict[str, Any], scenario: str) -> int:
        reps_list = results.get("scenarios", {}).get(scenario, {}).get("reps", [])
        if scenario == "tool_time":
            return sum(1 + (1 if rep.get("round2") is not None else 0) for rep in reps_list)
        return len(reps_list)

    normalized_dir = out_dir / "normalized"
    normalized_dir.mkdir(exist_ok=True)

    for scenario in scenarios:
        py_reqs = py_wire.get(scenario, [])
        go_reqs = go_wire.get(scenario, [])
        py_expected = expected_requests(py_results, scenario)
        go_expected = expected_requests(go_results, scenario)
        wire_complete = len(py_reqs) == py_expected and len(go_reqs) == go_expected
        entry: dict[str, Any] = {
            "request_count": {"python": len(py_reqs), "go": len(go_reqs), "expected": {"python": py_expected, "go": go_expected}},
            "wire_complete": wire_complete,
            "body_diffs": [],
        }
        for index, (py_req, go_req) in enumerate(zip(py_reqs, go_reqs)):
            entry["body_diffs"].append(
                {
                    "index": index,
                    "diff": diff_request_bodies(py_req.get("request_body"), go_req.get("request_body")),
                }
            )
        if not wire_complete:
            entry["request_shape"] = "incomplete"
        else:
            entry["request_shape"] = (
                "pass" if all(item["diff"]["status"] == "pass" for item in entry["body_diffs"]) else "diff"
            )
        entry["response"] = compare_side_results(
            py_results["scenarios"].get(scenario, {}), go_results["scenarios"].get(scenario, {}), scenario
        )
        # Attribute event-sequence diffs with the upstream evidence: a side
        # whose upstream stream carried no reasoning_content cannot emit
        # reasoning_delta events, so a difference there is gateway routing
        # variance, not client behavior.
        for behavior in entry["response"]["behaviors"]:
            rep_index = int(behavior.get("rep", 0))
            request_index = 2 * rep_index if scenario == "tool_time" else rep_index
            py_upstream = count_reasoning_chunks(py_reqs[request_index] if request_index < len(py_reqs) else None)
            go_upstream = count_reasoning_chunks(go_reqs[request_index] if request_index < len(go_reqs) else None)
            behavior["upstream_reasoning_chunks"] = {"python": py_upstream, "go": go_upstream}
            classification = behavior["event_diff"]["classification"]
            if classification == "reasoning_delta_missing_go" and go_upstream == 0:
                behavior["event_diff"]["classification"] = "upstream_reasoning_presence_variance"
            elif classification == "reasoning_delta_missing_python" and py_upstream == 0:
                behavior["event_diff"]["classification"] = "upstream_reasoning_presence_variance"
        if scenario == "unauthorized":
            for behavior in entry["response"]["behaviors"]:
                if behavior["event_diff"]["missing"] == ["error"] and not behavior["event_diff"]["extra"]:
                    behavior["event_diff"]["classification"] = "error_event_vs_returned_error"
        if all(
            behavior["event_diff"]["classification"] in ("pass", "chunk_boundary_variance", "upstream_reasoning_presence_variance")
            for behavior in entry["response"]["behaviors"]
        ):
            entry["response"]["verdict"]["event_sequence"] = "pass"
        if scenario == "tool_time":
            entry["tools_golden"] = compare_tools_to_golden(
                py_reqs[0] if py_reqs else None, go_reqs[0] if go_reqs else None
            )
            entry["tool_result"] = compare_tool_results(
                py_reqs[1] if len(py_reqs) > 1 else None,
                go_reqs[1] if len(go_reqs) > 1 else None,
            )
        if scenario == "unauthorized":
            entry["failure"] = compare_failure(
                py_reqs[0] if py_reqs else None,
                go_reqs[0] if go_reqs else None,
                py_results["scenarios"].get(scenario, {}),
                go_results["scenarios"].get(scenario, {}),
            )
        compare["scenarios"][scenario] = entry

        for side, requests in (("python", py_reqs), ("go", go_reqs)):
            for index, request in enumerate(requests):
                dump = {
                    "scenario": scenario,
                    "index": index,
                    "request_body": body_summary(request.get("request_body")),
                    "response_status": request.get("response_status"),
                }
                (normalized_dir / f"{scenario}.{side}.{index}.json").write_text(
                    json.dumps(dump, indent=1, ensure_ascii=False) + "\n", encoding="utf-8"
                )

    # Top-level verdict: which dimensions diverge anywhere (complete scenarios
    # only; an incomplete scenario blocks the verdict instead of guessing).
    dimension_status: dict[str, str] = {}
    incomplete = []
    for scenario, entry in compare["scenarios"].items():
        if not entry["wire_complete"]:
            incomplete.append(scenario)
            continue
        for dimension, status in entry["response"]["verdict"].items():
            if status != "pass":
                dimension_status[dimension] = "diff"
        if entry["request_shape"] != "pass":
            dimension_status["request_shape"] = "diff"
    compare["verdict"] = {
        "status": "incomplete" if incomplete else ("pass" if not dimension_status else "partial"),
        "dimensions_with_diffs": sorted(dimension_status),
        "incomplete_scenarios": incomplete,
        "extra_records": len(py_wire.get("_extra", [])) + len(go_wire.get("_extra", [])),
    }

    (out_dir / "compare.json").write_text(
        json.dumps(compare, indent=1, ensure_ascii=False) + "\n", encoding="utf-8"
    )
    log(f"wrote {out_dir / 'compare.json'}")

    # Human summary.
    lines = [f"records: python={len(py_records)} go={len(go_records)}"]
    for scenario in scenarios:
        entry = compare["scenarios"][scenario]
        verdicts = entry["response"]["verdict"]
        lines.append(
            f"{scenario}: request_shape={entry['request_shape']} "
            + " ".join(f"{k}={v}" for k, v in verdicts.items())
            + f" (requests: python={entry['request_count']['python']} go={entry['request_count']['go']})"
        )
    summary = "\n".join(lines) + "\n"
    (out_dir / "summary.txt").write_text(summary, encoding="utf-8")
    log("summary:\n" + summary)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
