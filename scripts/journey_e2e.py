#!/usr/bin/env python3
"""Hive API journey release gate (Q1 of docs/qa-postmortem-20261123.md).

Boots the prebuilt Hive server binary (`dist/hive-embed serve`) against a
throwaway HIVE_HOME on a free port, then walks the user journey a release must
survive:

  a. create a queen DM session                  POST /api/sessions {}
  b. list queen profiles (UI prerequisite)      GET  /api/queen/profiles
  c. create a fresh colony from the popup       POST /api/sessions {colony_id,...}
  d. colony exists on disk and in the lists     files + /api/colonies/tools-index
                                                + /api/sessions
  e. the SPA answers its pages                  GET /, GET /colony/<id>
  f. timer triggers can be created and stopped  POST .../triggers + .../deactivate
  g. cleanup                                    DELETE sessions, stop serve,
                                                delete the temp home

Every step asserts the HTTP status plus the business fields the UI reads, and
the run fails (exit 1) on the first measurement that disagrees. The journey
never calls a model: configuration.json points the main LLM slot at an
unreachable loopback port, so an accidental inference fails loudly instead of
hiding behind a real provider.

Exit codes:
  0 = every step passed and cleanup succeeded
  1 = journey failed (a scored step or cleanup returned the wrong result)
  2 = environment error (binary missing, server never became healthy, ...)

Usage:
  uv run --no-project python go_hive/scripts/journey_e2e.py
  uv run --no-project python go_hive/scripts/journey_e2e.py \\
      --binary /path/to/dist/hive-embed \\
      --scratch-root "$TMPDIR" \\
      --save-server-log /tmp/journey-serve.log \\
      --json --json-out /tmp/journey-result.json
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import platform
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone
from pathlib import Path

EXIT_PASS = 0
EXIT_JOURNEY_FAILED = 1
EXIT_ENVIRONMENT_ERROR = 2

DEFAULT_COLONY_ID = "journey-colony"
SCHEMA = "hive.journey-e2e/v1"

# The journey is about the API path, not the browser extension. A fixed-port
# browserbridge sidecar would make concurrent lanes fight over 14830 without
# telling us anything about the product, so disable it explicitly.
DISABLE_BROWSER_BRIDGE_ENV = {"HIVE_BROWSER_BRIDGE": "0"}


def now_iso() -> str:
    return datetime.now(timezone.utc).astimezone().isoformat(timespec="seconds")


class HttpResponse:
    """One HTTP exchange; HTTP status codes are data, not exceptions."""

    __slots__ = ("status", "content_type", "body_text", "json", "error", "duration_ms")

    def __init__(self, status=None, content_type="", body_text="", body_json=None,
                 error="", duration_ms=0.0):
        self.status = status
        self.content_type = content_type
        self.body_text = body_text
        self.json = body_json
        self.error = error
        self.duration_ms = duration_ms

    def body_summary(self, limit: int = 1500) -> str:
        if self.error:
            return self.error
        text = self.body_text
        if len(text) > limit:
            return text[:limit] + f"... ({len(text)} bytes total)"
        return text

    def transport_failed(self) -> bool:
        return self.status is None


def http_call(method: str, url: str, payload=None, timeout: float = 30.0) -> HttpResponse:
    data = None
    headers = {"Accept": "application/json, text/html;q=0.9, */*;q=0.5"}
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, method=method, headers=headers)
    started = time.monotonic()
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read()
            result = HttpResponse(
                status=response.status,
                content_type=response.headers.get("Content-Type", ""),
                body_text=raw.decode("utf-8", "replace"),
            )
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        result = HttpResponse(
            status=exc.code,
            content_type=(exc.headers.get("Content-Type", "") if exc.headers else ""),
            body_text=raw.decode("utf-8", "replace"),
        )
    except OSError as exc:
        result = HttpResponse(error=f"{type(exc).__name__}: {exc}")
    result.duration_ms = round((time.monotonic() - started) * 1000, 1)
    if result.body_text:
        try:
            result.json = json.loads(result.body_text)
        except ValueError:
            result.json = None
    return result


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Recorder:
    """Collects step records in the shape both the table and --json emit."""

    def __init__(self):
        self.steps: list[dict] = []
        self.cleanup: list[dict] = []
        self.notes: list[str] = []

    def step(self, step_id, title, expect, status, response: HttpResponse | None = None,
             duration_ms=None, actual="", checks=None, fields=None,
             request=None) -> dict:
        record = {
            "id": step_id,
            "title": title,
            "expect": expect,
            "status": status,
            "http_status": response.status if response is not None else None,
            "duration_ms": duration_ms if duration_ms is not None
            else (response.duration_ms if response is not None else None),
            "actual": actual,
            "checks": checks or [],
            "fields": fields or {},
        }
        if request is not None:
            record["request"] = request
        if response is not None:
            record["response"] = {
                "status": response.status,
                "content_type": response.content_type,
                "body": response.body_summary(),
            }
        self.steps.append(record)
        return record

    def cleanup_action(self, action, ok, detail) -> None:
        self.cleanup.append({"action": action, "ok": bool(ok), "detail": detail})

    def failed_steps(self) -> list[str]:
        return [s["id"] for s in self.steps if s["status"] == "fail"]

    def cleanup_ok(self) -> bool:
        return all(item["ok"] for item in self.cleanup)


def check(name: str, ok: bool, actual: str) -> dict:
    return {"name": name, "ok": bool(ok), "actual": actual}


def response_actual(response: HttpResponse, *field_parts: str) -> str:
    """One-line actual: HTTP status plus the business fields a step reads."""
    if response.transport_failed():
        return f"transport error: {response.error}"
    parts = [f"HTTP {response.status}"]
    if field_parts:
        parts.append(" · ".join(field_parts))
    return " · ".join(parts)


def wait_health(proc: subprocess.Popen, port: int, timeout: float) -> tuple[bool, str]:
    """Poll /api/health until it answers 200.

    macOS has no `timeout(1)`; the README's way to bound a boot is a poll loop.
    Returns (healthy, reason).
    """
    base = f"http://127.0.0.1:{port}"
    deadline = time.monotonic() + timeout
    last_error = "no response yet"
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            return False, f"serve exited while booting (rc={proc.returncode})"
        response = http_call("GET", base + "/api/health", timeout=2.0)
        if response.status == 200:
            return True, ""
        last_error = response.error or f"HTTP {response.status}"
        time.sleep(0.15)
    return False, f"/api/health never answered 200 within {timeout:.0f}s (last: {last_error})"


def stop_process(proc: subprocess.Popen | None, grace: float = 10.0) -> tuple[bool, str]:
    if proc is None:
        return True, "no serve process was started"
    if proc.poll() is not None:
        return True, f"serve already exited (rc={proc.returncode})"
    proc.send_signal(signal.SIGTERM)
    try:
        proc.wait(timeout=grace)
        return True, f"serve stopped on SIGTERM (rc={proc.returncode})"
    except subprocess.TimeoutExpired:
        proc.kill()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            return False, "serve ignored SIGTERM and SIGKILL"
        return False, f"serve required SIGKILL after SIGTERM timeout (rc={proc.returncode})"


def prepare_home(home: Path) -> str:
    """Throwaway HIVE_HOME: secrets/credential_key + a minimal configuration.

    The main LLM slot points at 127.0.0.1:9 (discard): unreachable on purpose,
    because this journey never sends a model request.
    """
    home.mkdir(parents=True, exist_ok=False)
    (home / "secrets").mkdir()
    credential_key = base64.urlsafe_b64encode(secrets.token_bytes(32)).decode("ascii")
    (home / "secrets" / "credential_key").write_text(credential_key + "\n", encoding="utf-8")
    configuration = {
        "llm": {
            "provider": "openai",
            "model": "gpt-4o-mini",
            "api_base": "http://127.0.0.1:9/v1",
            "api_key": "journey-e2e-no-llm",
        }
    }
    (home / "configuration.json").write_text(
        json.dumps(configuration, indent=2) + "\n", encoding="utf-8"
    )
    return credential_key


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def binary_version(binary: Path) -> str:
    try:
        completed = subprocess.run(
            [str(binary), "--version"], capture_output=True, text=True, timeout=20
        )
        lines = (completed.stdout or completed.stderr or "").strip().splitlines()
        return lines[0] if lines else "unknown"
    except (OSError, subprocess.SubprocessError) as exc:
        return f"unavailable ({exc})"


def git_info(repo_root: Path) -> dict:
    def run(*args):
        try:
            completed = subprocess.run(
                ["git", "-C", str(repo_root), *args],
                capture_output=True, text=True, timeout=10,
            )
            return completed.stdout.strip()
        except (OSError, subprocess.SubprocessError):
            return ""

    return {"git_head": run("rev-parse", "HEAD"),
            "git_branch": run("rev-parse", "--abbrev-ref", "HEAD")}


def default_binary(repo_root: Path) -> Path:
    return repo_root / "go_hive" / "go-hive" / "dist" / "hive-embed"


def resolve_scratch_root(args) -> Path:
    if args.scratch_root:
        root = Path(args.scratch_root).expanduser()
    elif os.environ.get("JOURNEY_E2E_SCRATCH"):
        root = Path(os.environ["JOURNEY_E2E_SCRATCH"]).expanduser()
    else:
        root = Path(tempfile.gettempdir())
    root.mkdir(parents=True, exist_ok=True)
    return root


def run_journey(recorder: Recorder, base: str, home: Path, colony_id: str,
                query_timeout: float, queen_override: str | None,
                context: dict) -> None:
    """Execute steps a-f, filling `context` with the ids later steps need.

    Independent steps keep running when a dependency failed; dependent steps
    are recorded as skipped so the result still maps the whole journey.
    """

    # ---------------------------------------------------------------- step a
    request = {"method": "POST", "path": "/api/sessions", "body": {}}
    response = http_call("POST", base + "/api/sessions", {}, timeout=query_timeout)
    body = response.json if isinstance(response.json, dict) else {}
    session_id = str(body.get("session_id") or "")
    no_colony = "colony_id" not in body or body.get("colony_id") is None
    checks = [
        check("HTTP status is 201", response.status == 201, f"HTTP {response.status}"),
        check("body carries session_id", bool(session_id), f"session_id={session_id!r}"),
        check("session is a queen DM (no colony_id)", no_colony,
              f"colony_id={body.get('colony_id')!r}"),
    ]
    ok = all(item["ok"] for item in checks)
    recorder.step(
        "a", "create queen DM session", "HTTP 201 + session_id + colony_id null",
        "pass" if ok else "fail", response=response,
        actual=response_actual(response, f"session_id={session_id or 'none'}",
                               f"colony_id={body.get('colony_id')!r}"),
        checks=checks, fields={"session_id": session_id}, request=request,
    )
    if ok:
        context["dm_session_id"] = session_id

    # ---------------------------------------------------------------- step b
    request = {"method": "GET", "path": "/api/queen/profiles"}
    response = http_call("GET", base + "/api/queen/profiles", timeout=query_timeout)
    body = response.json if isinstance(response.json, dict) else {}
    queens = body.get("queens") if isinstance(body.get("queens"), list) else []
    first = queens[0] if queens and isinstance(queens[0], dict) else {}
    queen_id = queen_override or str(first.get("id") or "")
    checks = [
        check("HTTP status is 200", response.status == 200, f"HTTP {response.status}"),
        check("queens is a non-empty list", len(queens) > 0, f"{len(queens)} queen(s)"),
        check("first profile carries an id", bool(queen_id), f"id={queen_id!r}"),
    ]
    ok = all(item["ok"] for item in checks)
    recorder.step(
        "b", "list queen profiles (UI prerequisite)",
        "HTTP 200 + non-empty queens[] + id", "pass" if ok else "fail",
        response=response,
        actual=response_actual(response, f"{len(queens)} queen(s)",
                               f"first id={queen_id or 'none'}"),
        checks=checks, fields={"queen_count": len(queens), "queen_id": queen_id},
        request=request,
    )
    if ok:
        context["queen_id"] = queen_id

    # ---------------------------------------------------------------- step c
    if context["queen_id"]:
        payload = {"colony_id": colony_id, "queen_name": context["queen_id"],
                   "initial_phase": "colony"}
        request = {"method": "POST", "path": "/api/sessions", "body": payload}
        response = http_call("POST", base + "/api/sessions", payload, timeout=query_timeout)
        body = response.json if isinstance(response.json, dict) else {}
        new_session_id = str(body.get("session_id") or "")
        checks = [
            check("HTTP status is 201", response.status == 201, f"HTTP {response.status}"),
            check("body carries session_id", bool(new_session_id),
                  f"session_id={new_session_id!r}"),
            check("session is bound to the new colony",
                  body.get("colony_id") == colony_id,
                  f"colony_id={body.get('colony_id')!r}, want {colony_id!r}"),
            check("has_worker is true", body.get("has_worker") is True,
                  f"has_worker={body.get('has_worker')!r}"),
        ]
        ok = all(item["ok"] for item in checks)
        recorder.step(
            "c", f"create fresh colony {colony_id!r} (popup path)",
            "HTTP 201 + session_id + colony_id + has_worker",
            "pass" if ok else "fail", response=response,
            actual=response_actual(response,
                                   (f"session_id={new_session_id}" if new_session_id else
                                    f"error={body.get('error')!r}")),
            checks=checks, fields={"session_id": new_session_id}, request=request,
        )
        if ok:
            context["colony_session_id"] = new_session_id
        elif response.status == 404 and "Colony not found" in str(body.get("error", "")):
            recorder.notes.append(
                "step c returned the known pre-fix 404 ('Colony not found') recorded in "
                "docs/qa-postmortem-20261123.md — this gate exists to catch exactly that."
            )
    else:
        recorder.step(
            "c", f"create fresh colony {colony_id!r} (popup path)",
            "HTTP 201 + session_id + colony_id + has_worker", "skipped",
            actual="skipped: step b did not produce a queen id",
        )

    # ---------------------------------------------------------------- step d
    if context["colony_session_id"]:
        colony_dir = home / "colonies" / colony_id
        metadata_path = colony_dir / "metadata.json"
        worker_path = colony_dir / "worker.json"
        metadata, worker = {}, {}
        read_errors = []
        for path in (metadata_path, worker_path):
            if not path.is_file():
                read_errors.append(f"{path.name} missing")
                continue
            try:
                parsed = json.loads(path.read_text(encoding="utf-8"))
            except (OSError, ValueError) as exc:
                read_errors.append(f"{path.name} unreadable: {exc}")
                continue
            if not isinstance(parsed, dict):
                read_errors.append(f"{path.name} is not a JSON object")
                continue
            if path is metadata_path:
                metadata = parsed
            else:
                worker = parsed

        index_response = http_call("GET", base + "/api/colonies/tools-index",
                                   timeout=query_timeout)
        index_body = index_response.json if isinstance(index_response.json, dict) else {}
        index_colonies = index_body.get("colonies") if isinstance(index_body.get("colonies"), list) else []
        index_hit = next((c for c in index_colonies
                          if isinstance(c, dict) and c.get("name") == colony_id), None)

        sessions_response = http_call("GET", base + "/api/sessions", timeout=query_timeout)
        sessions_body = sessions_response.json if isinstance(sessions_response.json, dict) else {}
        sessions = sessions_body.get("sessions") if isinstance(sessions_body.get("sessions"), list) else []
        listed = next((s for s in sessions
                       if isinstance(s, dict)
                       and s.get("session_id") == context["colony_session_id"]), None)

        checks = [
            check("metadata.json exists", metadata_path.is_file(),
                  f"{metadata_path} {'exists' if metadata_path.is_file() else 'missing'}"),
            check("metadata.json name == colony_id", metadata.get("name") == colony_id,
                  f"name={metadata.get('name')!r}"),
            check("metadata.json has created_at", bool(metadata.get("created_at")),
                  f"created_at={metadata.get('created_at')!r}"),
            check("metadata.json queen_name == bound queen",
                  metadata.get("queen_name") == context["queen_id"],
                  f"queen_name={metadata.get('queen_name')!r}"),
            check("worker.json exists", worker_path.is_file(),
                  f"{worker_path} {'exists' if worker_path.is_file() else 'missing'}"),
            check("worker.json has a name", bool(worker.get("name")),
                  f"name={worker.get('name')!r}"),
            check("GET /api/colonies/tools-index lists the colony", index_hit is not None,
                  f"tools-index HTTP {index_response.status}, "
                  f"{'listed' if index_hit else 'not listed'}"),
            check("GET /api/sessions lists the colony session", listed is not None,
                  f"sessions HTTP {sessions_response.status}, "
                  f"{'listed' if listed else 'not listed'}"),
            check("listed session carries colony_id",
                  listed is not None and listed.get("colony_id") == colony_id,
                  f"colony_id={(listed or {}).get('colony_id')!r}"),
        ]
        ok = all(item["ok"] for item in checks)
        recorder.step(
            "d", "colony on disk + in tools-index + in sessions",
            "metadata.json{name,created_at,queen_name} + worker.json{name} + both lists",
            "pass" if ok else "fail", response=index_response,
            actual=(f"metadata={'ok' if metadata else 'missing'} · "
                    f"worker={'ok' if worker else 'missing'} · "
                    f"tools-index={'listed' if index_hit else 'absent'} · "
                    f"sessions={'listed' if listed else 'absent'}"),
            checks=checks,
            fields={
                "metadata": metadata,
                "worker": worker,
                "read_errors": read_errors,
                "tools_index_status": index_response.status,
                "sessions_status": sessions_response.status,
            },
        )
    else:
        recorder.step(
            "d", "colony on disk + in tools-index + in sessions",
            "metadata.json{name,created_at,queen_name} + worker.json{name} + both lists",
            "skipped", actual="skipped: step c did not create the colony",
        )

    # ---------------------------------------------------------------- step e
    root_response = http_call("GET", base + "/", timeout=query_timeout)
    colony_page_response = http_call("GET", base + f"/colony/{colony_id}", timeout=query_timeout)

    def html_flags(response: HttpResponse) -> tuple[bool, bool]:
        text = (response.body_text or "").lower()
        return ("text/html" in response.content_type.lower(),
                "<!doctype html" in text or "<html" in text)

    root_type_ok, root_body_ok = html_flags(root_response)
    page_type_ok, page_body_ok = html_flags(colony_page_response)
    checks = [
        check("GET / is 200", root_response.status == 200, f"HTTP {root_response.status}"),
        check("GET / is text/html", root_type_ok, f"content-type={root_response.content_type!r}"),
        check("GET / serves the SPA document", root_body_ok, "doctype/'<html' check"),
        check("GET /colony/<id> is 200", colony_page_response.status == 200,
              f"HTTP {colony_page_response.status}"),
        check("GET /colony/<id> is text/html", page_type_ok,
              f"content-type={colony_page_response.content_type!r}"),
        check("GET /colony/<id> serves the SPA document", page_body_ok, "doctype/'<html' check"),
    ]
    ok = all(item["ok"] for item in checks)
    recorder.step(
        "e", "SPA pages answer (root + colony route)",
        "HTTP 200 text/html for / and /colony/<id>",
        "pass" if ok else "fail", response=root_response,
        actual=(f"/ → HTTP {root_response.status} "
                f"{root_response.content_type or 'no content-type'} · "
                f"/colony/{colony_id} → HTTP {colony_page_response.status} "
                f"{colony_page_response.content_type or 'no content-type'}"),
        checks=checks,
        fields={"root_status": root_response.status,
                "colony_page_status": colony_page_response.status},
        request={"method": "GET", "paths": ["/", f"/colony/{colony_id}"]},
    )

    # ---------------------------------------------------------------- step f
    if context["colony_session_id"]:
        session_id = context["colony_session_id"]
        trigger_payload = {"name": "probe", "task": "noop",
                           "trigger_config": {"interval_minutes": 60}}
        create_response = http_call(
            "POST", base + f"/api/sessions/{session_id}/triggers",
            trigger_payload, timeout=query_timeout,
        )
        create_body = create_response.json if isinstance(create_response.json, dict) else {}
        trigger_id = str(create_body.get("trigger_id") or "")
        deactivate_response = None
        deactivate_body = {}
        if trigger_id:
            deactivate_response = http_call(
                "POST",
                base + f"/api/sessions/{session_id}/triggers/{trigger_id}/deactivate",
                None, timeout=query_timeout,
            )
            deactivate_body = (deactivate_response.json
                               if isinstance(deactivate_response.json, dict) else {})
        checks = [
            check("create trigger is 200/201", create_response.status in (200, 201),
                  f"HTTP {create_response.status}"),
            check("create trigger returns trigger_id", bool(trigger_id),
                  f"trigger_id={trigger_id!r}"),
            check("deactivate trigger is 200",
                  deactivate_response is not None and deactivate_response.status == 200,
                  f"HTTP {deactivate_response.status if deactivate_response else 'not called'}"),
            check("deactivate echoes the trigger_id",
                  bool(trigger_id) and deactivate_body.get("trigger_id") == trigger_id,
                  f"trigger_id={deactivate_body.get('trigger_id')!r}"),
        ]
        ok = all(item["ok"] for item in checks)
        recorder.step(
            "f", "create + deactivate a timer trigger on the colony session",
            "HTTP 200/201 with trigger_id, then HTTP 200 on deactivate",
            "pass" if ok else "fail", response=create_response,
            actual=(f"create HTTP {create_response.status} trigger_id={trigger_id or 'none'} · "
                    f"deactivate HTTP "
                    f"{deactivate_response.status if deactivate_response else 'n/a'}"),
            checks=checks,
            fields={"trigger_id": trigger_id,
                    "deactivate_status":
                        deactivate_response.status if deactivate_response else None,
                    "deactivate_body": deactivate_body},
            request={"method": "POST",
                     "paths": [f"/api/sessions/{session_id}/triggers",
                               f"/api/sessions/{session_id}/triggers/<id>/deactivate"]},
        )
    else:
        recorder.step(
            "f", "create + deactivate a timer trigger on the colony session",
            "HTTP 200/201 with trigger_id, then HTTP 200 on deactivate",
            "skipped", actual="skipped: step c did not create the colony",
        )


def run_cleanup(recorder: Recorder, base: str, context: dict, query_timeout: float) -> None:
    """Step g, part 1: stop the sessions this run started (while serve is still
    up), so a temp home never leaks live runtimes into the assertion artifacts."""
    for label, session_id in (("colony session", context.get("colony_session_id")),
                              ("queen DM session", context.get("dm_session_id"))):
        if not session_id:
            continue
        response = http_call("DELETE", base + f"/api/sessions/{session_id}",
                             timeout=query_timeout)
        ok = response.status == 200
        detail = (f"HTTP {response.status} · {response.body_text.strip()[:200]}"
                  if not response.transport_failed() else response.error)
        recorder.cleanup_action(f"DELETE {label} {session_id}", ok, detail)


def render_table(result: dict) -> str:
    environment = result["environment"]
    lines = []
    lines.append("Hive API journey gate — real hive-embed serve on a throwaway HIVE_HOME")
    lines.append(f"  binary     : {environment['binary']} ({environment['binary_version']})")
    lines.append(f"  binary sha : {environment['binary_sha256'][:16]}…")
    lines.append(f"  repo HEAD  : {environment.get('git_head', '?')} "
                 f"({environment.get('git_branch', '?')})")
    kept = "kept (--keep)" if environment.get("home_kept") else "deleted after the run"
    lines.append(f"  HIVE_HOME  : {environment['hive_home']} [{kept}]")
    lines.append(f"  base URL   : {environment.get('base_url', 'not reached')}")
    lines.append(f"  started at : {result['generated_at']}")
    lines.append("")
    for step in result["steps"]:
        label = {"pass": "PASS", "fail": "FAIL", "skipped": "SKIP"}.get(step["status"], "?")
        http = step.get("http_status")
        http_text = "-" if http is None else str(http)
        duration = step.get("duration_ms")
        duration_text = "-" if duration is None else f"{duration:.1f}ms"
        lines.append(f"[{step['id']}] {label} http={http_text:<3} {duration_text:>8}  "
                     f"{step['title']}")
        if step["status"] == "pass":
            lines.append(f"      {step['actual']}")
        elif step["status"] == "skipped":
            lines.append(f"      skipped — {step['actual']}")
        else:
            lines.append(f"      expect : {step['expect']}")
            lines.append(f"      actual : {step['actual']}")
            for item in step.get("checks", []):
                if not item["ok"]:
                    lines.append(f"      failed : {item['name']} — {item['actual']}")
    lines.append("")
    lines.append("cleanup (step g):")
    if result["cleanup"]:
        for item in result["cleanup"]:
            lines.append(f"  {'ok ' if item['ok'] else 'BAD'} {item['action']} — {item['detail']}")
    else:
        lines.append("  (nothing to clean up)")
    lines.append("")
    if result["ok"]:
        lines.append(f"result: PASS — {len(result['steps'])} steps, cleanup ok")
    elif result["exit_code"] == EXIT_ENVIRONMENT_ERROR:
        lines.append("result: ENVIRONMENT ERROR — see note above / serve log")
    else:
        failed = result["failed"]
        failed_text = ", ".join(failed) if failed else "cleanup"
        lines.append(f"result: FAIL — failed: {failed_text}"
                     f"{'' if result['cleanup_ok'] else ' + cleanup'}")
    for note in result.get("notes", []):
        lines.append(f"note: {note}")
    lines.append(f"exit code: {result['exit_code']}")
    return "\n".join(lines)


def parse_args(argv):
    parser = argparse.ArgumentParser(
        description="Boot a real hive-embed serve and run the release-gate API journey.",
    )
    parser.add_argument("--binary",
                        help="path to hive-embed (default: <repo>/go_hive/go-hive/dist/hive-embed)")
    parser.add_argument("--scratch-root",
                        help="directory for the throwaway HIVE_HOME "
                             "(default: $JOURNEY_E2E_SCRATCH or $TMPDIR)")
    parser.add_argument("--colony-id", default=DEFAULT_COLONY_ID,
                        help=f"colony id the journey creates (default: {DEFAULT_COLONY_ID})")
    parser.add_argument("--queen-name",
                        help="queen id to bind (default: first id from /api/queen/profiles)")
    parser.add_argument("--health-timeout", type=float, default=90.0,
                        help="seconds to wait for /api/health to answer 200 (default: 90)")
    parser.add_argument("--request-timeout", type=float, default=30.0,
                        help="per-request HTTP timeout in seconds (default: 30)")
    parser.add_argument("--keep", action="store_true",
                        help="keep the throwaway HIVE_HOME (and its serve.log) for inspection")
    parser.add_argument("--json", action="store_true",
                        help="print the machine-readable result on stdout "
                             "(human table goes to stderr)")
    parser.add_argument("--json-out", help="also write the machine-readable result to this path")
    parser.add_argument("--save-server-log",
                        help="copy the serve log here before the home is deleted")
    return parser.parse_args(argv)


def main(argv=None) -> int:
    args = parse_args(argv)
    repo_root = Path(__file__).resolve().parents[2]
    binary = Path(args.binary).expanduser() if args.binary else default_binary(repo_root)
    if not binary.is_file():
        print(f"environment error: no server binary at {binary}", file=sys.stderr)
        print("build one first:  make -C go_hive/go-hive build && make -C go_hive/go-hive embed-ui  "
              "(dist/hive-embed; or pass --binary <path>)", file=sys.stderr)
        return EXIT_ENVIRONMENT_ERROR

    try:
        scratch_root = resolve_scratch_root(args)
    except OSError as exc:
        print(f"environment error: cannot prepare scratch root: {exc}", file=sys.stderr)
        return EXIT_ENVIRONMENT_ERROR
    home = scratch_root / f"journey-{uuid.uuid4()}"
    if str(home.resolve()).startswith(str(repo_root.resolve()) + os.sep):
        print(f"environment error: scratch dir {home} is inside the repo; "
              f"pick --scratch-root outside it", file=sys.stderr)
        return EXIT_ENVIRONMENT_ERROR

    started_wall = time.monotonic()
    recorder = Recorder()
    context = {"dm_session_id": "", "queen_id": "", "colony_session_id": ""}
    proc = None
    log_handle = None
    log_path = home / "serve.log"
    saved_log = ""
    fatal = ""
    base = ""
    exit_code = EXIT_ENVIRONMENT_ERROR
    environment = {
        "python": platform.python_version(),
        "platform": platform.platform(),
        "repo_root": str(repo_root),
        "binary": str(binary),
        "binary_version": binary_version(binary),
        "binary_sha256": sha256_file(binary),
        "scratch_root": str(scratch_root),
        "hive_home": str(home),
        "home_kept": False,
        "serve_browser_bridge": "disabled (HIVE_BROWSER_BRIDGE=0)",
    }
    environment.update(git_info(repo_root))

    try:
        credential_key = prepare_home(home)
        log_handle = log_path.open("w", encoding="utf-8")
        child_env = dict(os.environ)
        child_env.update({"HIVE_HOME": str(home), "HIVE_CREDENTIAL_KEY": credential_key})
        child_env.update(DISABLE_BROWSER_BRIDGE_ENV)
        child_env.pop("HIVE_DESKTOP_TOKEN", None)

        healthy = False
        boot_reason = ""
        for _ in range(3):
            port = free_port()
            base = f"http://127.0.0.1:{port}"
            environment["port"] = port
            environment["base_url"] = base
            proc = subprocess.Popen(
                [str(binary), "serve", "--host", "127.0.0.1", "--port", str(port)],
                env=child_env, cwd=str(home),
                stdout=log_handle, stderr=subprocess.STDOUT,
            )
            healthy, boot_reason = wait_health(proc, port, args.health_timeout)
            if healthy:
                break
            log_handle.flush()
            stopped, stop_detail = stop_process(proc)
            proc = None
            tail = (log_path.read_text(encoding="utf-8", errors="replace")[-3000:]
                    if log_path.is_file() else "")
            if "address already in use" in tail:
                boot_reason = f"port {port} was taken, retrying on a fresh port"
                continue
            boot_reason = f"{boot_reason} / {stop_detail}\n--- serve log tail ---\n{tail}"
            break

        if not healthy:
            fatal = f"serve never became healthy: {boot_reason}"
            exit_code = EXIT_ENVIRONMENT_ERROR
        else:
            try:
                run_journey(recorder, base, home, args.colony_id,
                            args.request_timeout, args.queen_name, context)
            finally:
                if proc is not None and proc.poll() is None and base:
                    run_cleanup(recorder, base, context, args.request_timeout)
            if recorder.failed_steps() or not recorder.cleanup_ok():
                exit_code = EXIT_JOURNEY_FAILED
            else:
                exit_code = EXIT_PASS
    except Exception as exc:  # noqa: BLE001 - an unexpected failure is an env error here
        import traceback

        traceback.print_exc()
        fatal = f"unexpected error: {type(exc).__name__}: {exc}"
        exit_code = EXIT_ENVIRONMENT_ERROR
    finally:
        stopped, stop_detail = stop_process(proc)
        recorder.cleanup_action("stop serve process", stopped, stop_detail)
        if log_handle is not None:
            try:
                log_handle.close()
            except OSError:
                pass
        if args.save_server_log and log_path.is_file():
            try:
                saved_log = str(Path(args.save_server_log).expanduser())
                Path(saved_log).parent.mkdir(parents=True, exist_ok=True)
                Path(saved_log).write_text(
                    log_path.read_text(encoding="utf-8", errors="replace"), encoding="utf-8")
            except OSError as exc:
                saved_log = ""
                recorder.cleanup_action("copy serve log", False, f"{exc}")
        if args.keep:
            environment["home_kept"] = True
        else:
            try:
                shutil.rmtree(home)
                ok = not home.exists()
            except OSError as exc:
                ok = False
                recorder.cleanup_action("delete temp HIVE_HOME", False, str(exc))
            else:
                recorder.cleanup_action("delete temp HIVE_HOME", ok,
                                        str(home) if ok else f"{home} still exists")

    environment["serve_log_saved"] = saved_log
    failed = recorder.failed_steps()
    ok = exit_code == EXIT_PASS and not failed and recorder.cleanup_ok()
    result = {
        "schema": SCHEMA,
        "ok": ok,
        "exit_code": exit_code,
        "generated_at": now_iso(),
        "duration_ms": round((time.monotonic() - started_wall) * 1000, 1),
        "environment": environment,
        "steps": recorder.steps,
        "cleanup": recorder.cleanup,
        "cleanup_ok": recorder.cleanup_ok(),
        "failed": failed,
        "notes": recorder.notes,
    }
    if fatal:
        result["notes"].insert(0, f"environment error: {fatal}")

    if args.json_out:
        out_path = Path(args.json_out).expanduser()
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_text(json.dumps(result, indent=2, ensure_ascii=False) + "\n",
                            encoding="utf-8")
    table = render_table(result)
    if args.json:
        print(json.dumps(result, indent=2, ensure_ascii=False))
        print(table, file=sys.stderr)
    else:
        print(table)
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
