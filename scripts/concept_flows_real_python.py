#!/usr/bin/env python3
"""Python half of the concept-flow real-model dual run.

Drives a real `uv run hive serve` (the reference implementation) through the
same scenarios as `go test -tags e2e ./e2e -run TestRealLLMConceptFlows`:

  worker_dispatch     queen -> run_worker -> worker report -> lead summary
  worker_llm_probe    configuration.worker_llm must reach the worker's wire call
  escalation_chain    worker doom-loop -> inbox -> list/reply -> worker reports
  ask_user_park       queen asks -> session parks -> answer resumes the turn
  queen_llm_probe     a new queen session must run on her queen_llm slot
  resume_after_restart fork a colony, restart `serve --colony`, resume live

Every scenario points configuration.json at the recording proxy
(e2e/cmd/recordproxy, built from the Go module) and writes evidence.json in
the same shape as the Go half, so
go_hive/scripts/concept_flows_real_compare.py can diff the two.

The test models are the real upstream model (no scripting). Usage:

  uv run --no-project python go_hive/scripts/concept_flows_real_python.py \
      --go-module <worktree>/go_hive/go-hive \
      --repo-root <worktree>/core \
      --out <evidence root> \
      --upstream https://token.tokenbroad.com/v1
"""

from __future__ import annotations

import argparse
import glob
import json
import os
import re
import signal
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

COLONY_ID = "cf_worker"
FORK_COLONY_ID = "cf_fork"
PROBE_WORKER_MODEL = "cf-probe-worker-model"
PROBE_QUEEN_MODEL = "cf-probe-queen-model"
PROXY_KEY_ENV = "CF_PROXY_KEY"
ASK_QUESTION = "Which colour should I use?"


def log(message: str) -> None:
    print(f"[py-concept] {message}", flush=True)


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def http_json(method: str, url: str, payload: dict | None = None, timeout: float = 120) -> tuple[int, dict]:
    data = None
    headers = {}
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
            body = json.loads(raw)
        except ValueError:
            body = {"raw": raw.decode("utf-8", "replace")[:400]}
    return status, body


def wait_until(what: str, timeout: float, fn) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if fn():
            return True
        time.sleep(0.2)
    log(f"timed out waiting for {what}")
    return False


# ---- filesystem readers -----------------------------------------------------


def find_session_dir(home: Path, session_id: str) -> Path | None:
    for pattern in (
        str(home / "queens" / "*" / "sessions" / session_id),
        str(home / "colonies" / "*" / "queens" / "*" / "sessions" / session_id),
    ):
        matches = glob.glob(pattern)
        if matches:
            return Path(matches[0])
    return None


def read_parts(session_dir: Path | None) -> list[dict]:
    if session_dir is None:
        return []
    out = []
    for path in sorted((session_dir / "conversations" / "parts").glob("*.json")):
        try:
            out.append(json.loads(path.read_text(encoding="utf-8")))
        except ValueError:
            continue
    return out


def read_events(session_dir: Path | None) -> list[dict]:
    if session_dir is None:
        return []
    path = session_dir / "events.jsonl"
    if not path.is_file():
        return []
    events = []
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        if not line.strip():
            continue
        try:
            events.append(json.loads(line))
        except ValueError:
            continue
    return events


def trim_value(value, depth: int = 0):
    if isinstance(value, str):
        return value if len(value) <= 1200 else value[:1200] + "…"
    if isinstance(value, dict):
        if depth >= 2:
            return {"keys": sorted(value.keys())}
        return {key: trim_value(item, depth + 1) for key, item in value.items()}
    if isinstance(value, list):
        return [trim_value(item, depth + 1) for item in value[:8]]
    return value


def trim_event(event: dict) -> dict:
    out = {key: event[key] for key in ("type", "stream_id", "node_id", "execution_id", "seq") if key in event}
    if isinstance(event.get("data"), dict):
        out["data"] = trim_value(event["data"])
    return out


def part_view(part: dict) -> dict:
    out: dict = {"role": part.get("role")}
    content = part.get("content")
    if isinstance(content, str) and content:
        out["content"] = content if len(content) <= 4000 else content[:4000] + "…"
    if "is_client_input" in part:
        out["is_client_input"] = part["is_client_input"]
    calls = part.get("tool_calls")
    if isinstance(calls, list):
        names = []
        for call in calls:
            function = call.get("function") or {}
            arguments = function.get("arguments") or ""
            names.append(f"{function.get('name', '')} {arguments}".strip())
        out["tool_calls"] = names
    return out


def parts_view(parts: list[dict]) -> list[dict]:
    return [part_view(part) for part in parts]


def part_roles(parts: list[dict]) -> list[str]:
    return [str(part.get("role")) for part in parts]


def worker_actions(parts: list[dict]) -> list[str]:
    actions = []
    for part in parts:
        for call in part.get("tool_calls") or []:
            function = call.get("function") or {}
            if function.get("name"):
                actions.append(str(function["name"]))
    return actions


def worker_report(parts: list[dict]) -> dict | None:
    report = None
    for part in parts:
        for call in part.get("tool_calls") or []:
            function = call.get("function") or {}
            if function.get("name") != "report_to_parent":
                continue
            try:
                report = json.loads(function.get("arguments") or "{}")
            except ValueError:
                continue
    return report


def worker_dirs(colony_path: Path) -> list[Path]:
    """Every worker directory under <colony>/workers.

    Python names workers with a session-like id (session_YYYYMMDD_...), Go with
    worker_N; both are plain directories, so the reader is name-agnostic.
    """
    root = colony_path / "workers"
    if not root.is_dir():
        return []
    return sorted(path for path in root.iterdir() if path.is_dir())


def colony_files(colony_path: Path) -> list[str]:
    out = []
    for root, dirs, files in os.walk(colony_path):
        relative_root = os.path.relpath(root, colony_path)
        for name in dirs:
            relative = os.path.normpath(os.path.join(relative_root, name)) if relative_root != "." else name
            out.append(relative + "/")
        for name in files:
            relative = os.path.normpath(os.path.join(relative_root, name)) if relative_root != "." else name
            out.append(relative)
    return sorted(out)


def tracker_dump(colony_path: Path) -> dict:
    db_path = colony_path / "tracker" / "tracker.db"
    out: dict = {"exists": db_path.is_file()}
    if not out["exists"]:
        return out
    try:
        connection = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
        try:
            names = [row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")]
            tables = {}
            for name in names:
                tables[name] = connection.execute(f"SELECT COUNT(*) FROM {name}").fetchone()[0]
            out["tables"] = tables
            try:
                cursor = connection.execute("SELECT * FROM _tracker_registry ORDER BY table_name")
                columns = [description[0] for description in cursor.description]
                out["registry"] = [dict(zip(columns, row)) for row in cursor.fetchall()]
            except sqlite3.Error:
                out["registry"] = None
        finally:
            connection.close()
    except sqlite3.Error as exc:
        out["error"] = str(exc)
    return out


# ---- proxy records ----------------------------------------------------------

KIND_QUEEN = "queen"
KIND_WORKER = "worker"


def load_records(path: Path) -> list[dict]:
    if not path.is_file():
        return []
    out = []
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        if not line.strip():
            continue
        try:
            out.append(json.loads(line))
        except ValueError:
            continue
    return out


def classify(record: dict) -> str:
    tools = record.get("tools") or []
    if not tools:
        return "no-tools"
    if "run_worker" in tools and "report_to_parent" in tools:
        return "colony-lead"
    if "run_worker" in tools:
        return "queen"
    if "report_to_parent" in tools:
        return "worker"
    return "other"


def model_requests(records: list[dict]) -> list[dict]:
    out = []
    for record in records:
        content = record.get("content") or ""
        out.append(
            {
                "seq": record.get("seq"),
                "kind": classify(record),
                "model": record.get("model"),
                "path": record.get("path"),
                "status": record.get("status"),
                "tools": record.get("tools") or [],
                "message_count": record.get("message_count"),
                "tool_calls": record.get("tool_calls") or [],
                "finish_reason": record.get("finish_reason"),
                "content": content if len(content) <= 1200 else content[:1200] + "…",
                "error": record.get("error"),
            }
        )
    return out


# ---- serve lifecycle --------------------------------------------------------


class Serve:
    def __init__(self, repo_root: str, home: Path, preload: str | None, log_path: Path):
        self.repo_root = repo_root
        self.home = home
        self.port = free_port()
        self.base = f"http://127.0.0.1:{self.port}"
        args = ["uv", "run", "--offline", "hive", "serve", "--host", "127.0.0.1", "--port", str(self.port)]
        if preload:
            args += ["--colony", preload]
        env = dict(os.environ)
        env["HIVE_HOME"] = str(home)
        env[PROXY_KEY_ENV] = "placeholder"
        env.pop("BROADTOKEN_API_KEY", None)
        env.pop("OPENAI_API_KEY", None)
        self.log = log_path.open("w")
        self.proc = subprocess.Popen(args, cwd=repo_root, env=env, stdout=self.log, stderr=subprocess.STDOUT, start_new_session=True)
        deadline = time.time() + 60
        while time.time() < deadline:
            if self.proc.poll() is not None:
                raise SystemExit(f"python hive serve exited before /api/health:\n{log_path.read_text(errors='replace')[-4000:]}")
            try:
                status, _ = http_json("GET", self.base + "/api/health", timeout=2)
                if status == 200:
                    return
            except OSError:
                pass
            time.sleep(0.1)
        raise SystemExit("python hive serve did not become healthy")

    def stop(self):
        try:
            os.killpg(os.getpgid(self.proc.pid), signal.SIGTERM)
        except ProcessLookupError:
            return
        try:
            self.proc.wait(timeout=20)
        except subprocess.TimeoutExpired:
            self.proc.kill()
        self.log.close()

    def create(self, payload: dict) -> tuple[str, dict]:
        status, body = http_json("POST", self.base + "/api/sessions", payload)
        session_id = str(body.get("session_id") or "")
        if status not in (200, 201) or not session_id:
            raise SystemExit(f"POST /api/sessions -> {status} {body}")
        return session_id, body

    def chat(self, session_id: str, message: str) -> tuple[int, dict]:
        return http_json("POST", f"{self.base}/api/sessions/{session_id}/chat", {"message": message})

    def session(self, session_id: str) -> tuple[int, dict]:
        return http_json("GET", f"{self.base}/api/sessions/{session_id}")

    def snapshot(self, session_id: str) -> dict:
        status, body = http_json("GET", f"{self.base}/api/sessions/{session_id}/snapshot")
        return body if status == 200 else {"http_status": status}


# ---- shared evidence pieces -------------------------------------------------


def session_evidence(serve: Serve | None, home: Path, session_id: str) -> dict:
    directory = find_session_dir(home, session_id)
    evidence: dict = {"dir_found": directory is not None}
    if directory is None:
        return evidence
    evidence["parts"] = parts_view(read_parts(directory))
    events = [trim_event(event) for event in read_events(directory)]
    evidence["events"] = events
    counts: dict = {}
    for event in events:
        counts[event.get("type", "")] = counts.get(event.get("type", ""), 0) + 1
    evidence["event_types"] = counts
    if serve is not None:
        evidence["snapshot"] = snapshot_view(serve.snapshot(session_id))
    return evidence


def snapshot_view(snapshot: dict) -> dict:
    out = {}
    for key in ("activity", "interrupt_cause", "park_reason", "pending_questions", "is_streaming", "current_execution_id", "last_event_at", "seq"):
        if key in snapshot:
            out[key] = snapshot[key]
    return out


def live_view(body: dict) -> dict:
    return {key: body[key] for key in ("queen_id", "queen_name", "colony_id", "queen_phase", "has_worker") if key in body}


def worker_evidence(colony_path: Path) -> list[dict]:
    out = []
    for directory in worker_dirs(colony_path):
        parts = read_parts(directory)
        entry: dict = {
            "dir": directory.name,
            "actions": worker_actions(parts),
            "part_roles": part_roles(parts),
        }
        report = worker_report(parts)
        if report is not None:
            entry["report"] = report
        for name in ("meta", "state", "result"):
            path = directory / f"{name}.json"
            if path.is_file():
                try:
                    entry[name] = trim_value(json.loads(path.read_text(encoding="utf-8")))
                except ValueError:
                    pass
        out.append(entry)
    return out


def base_evidence(scenario: str, variant: str) -> dict:
    return {"side": "python", "scenario": scenario, "variant": variant}


def write_evidence(root: Path, scenario: str, variant: str, evidence: dict) -> Path:
    directory = root / "python" / f"{scenario}-{variant}"
    directory.mkdir(parents=True, exist_ok=True)
    evidence["generated_at"] = time.strftime("%Y-%m-%dT%H:%M:%S")
    (directory / "evidence.json").write_text(json.dumps(evidence, ensure_ascii=False, indent=2), encoding="utf-8")
    return directory


def wait_idle(serve: Serve, session_id: str, timeout: float) -> None:
    def idle() -> bool:
        snapshot = serve.snapshot(session_id)
        activity = snapshot.get("activity") or ""
        return activity in ("", "awaiting_user", "idle", "parked")

    wait_until("session idle", timeout, idle)


def wait_worker_report(colony_path: Path, timeout: float) -> bool:
    def done() -> bool:
        for directory in worker_dirs(colony_path):
            if worker_report(read_parts(directory)) is not None:
                return True
        return False

    return wait_until("worker report", timeout, done)


def write_config(home: Path, proxy_url: str, model: str, extra: dict | None) -> None:
    config = {
        "llm": {
            "provider": "openai",
            "model": model,
            "api_base": proxy_url,
            "api_key_env_var": PROXY_KEY_ENV,
        }
    }
    if extra:
        config.update(extra)
    (home / "configuration.json").write_text(json.dumps(config, indent=2), encoding="utf-8")


def write_colony(home: Path, colony_id: str, worker: dict, queen: str = "queen_growth") -> Path:
    colony_path = home / "colonies" / colony_id
    colony_path.mkdir(parents=True, exist_ok=True)
    (colony_path / "metadata.json").write_text(json.dumps({"name": colony_id, "queen_name": queen, "created_at": 0}), encoding="utf-8")
    (colony_path / "worker.json").write_text(json.dumps(worker, indent=2), encoding="utf-8")
    return colony_path


def worker_definition(prompt: str) -> dict:
    return {
        "name": "cf_worker",
        "description": "Concept-flow E2E worker",
        "system_prompt": prompt,
        "tools": [],
        "goal": {"description": "Concept-flow E2E work"},
    }


# ---- scenarios --------------------------------------------------------------


def scenario_worker_dispatch(context: dict) -> None:
    home = context["home"]
    proxy_log: Path = context["proxy_log"]
    write_config(home, context["proxy_url"], context["model"], None)
    colony_path = write_colony(home, COLONY_ID, worker_definition("You are a worker agent. Call the get_current_time tool exactly once, then call report_to_parent with status='success' and a summary containing the returned time. If get_current_time is not in your toolset, get the current time another way (for example the terminal) and report that. Do not call any other tool."))
    serve = Serve(context["repo_root"], home, None, context["out"] / "worker_dispatch-serve.log")
    try:
        session_id, _ = serve.create({"colony_id": COLONY_ID})
        status, body = serve.chat(session_id, "Use the run_worker tool to dispatch exactly one worker with the task: get the current time (call get_current_time if you have it) and then report it back with report_to_parent, status success. After the worker reports, reply with a one-line summary of its report.")
        if status != 200:
            raise SystemExit(f"worker_dispatch chat -> {status} {body}")
        wait_until("worker dir", 300, lambda: len(worker_dirs(colony_path)) > 0)
        wait_worker_report(colony_path, 300)
        wait_until("worker report in lead transcript", 120, lambda: lead_has_worker_report(home, session_id, colony_path))
        wait_idle(serve, session_id, 120)

        records = load_records(proxy_log)
        evidence = base_evidence("worker_dispatch", "main")
        evidence["session_id"] = session_id
        evidence["session_live"] = live_view(serve.session(session_id)[1])
        evidence["session"] = session_evidence(serve, home, session_id)
        evidence["colony_queen"] = {"parts": parts_view(read_parts(colony_path))}
        evidence["workers"] = worker_evidence(colony_path)
        evidence["colony_files"] = colony_files(colony_path)
        evidence["tracker"] = tracker_dump(colony_path)
        evidence["model_requests"] = model_requests(records)

        checks: dict = {
            "worker_spawned": len(worker_dirs(colony_path)) > 0,
            "worker_reported": False,
            "worker_report_status": "",
            "queen_dispatched": False,
            "all_models_main": True,
        }
        worker_tools = None
        for record in records:
            if record.get("model") != context["model"]:
                checks["all_models_main"] = False
            tools = record.get("tools") or []
            if "run_worker" in tools and any(str(call).startswith("run_worker") for call in record.get("tool_calls") or []):
                checks["queen_dispatched"] = True
            if "report_to_parent" in tools and "run_worker" not in tools and worker_tools is None:
                worker_tools = tools
        for worker in evidence["workers"]:
            if worker.get("report") is not None:
                checks["worker_reported"] = True
                checks["worker_report_status"] = worker["report"].get("status", "")
        if worker_tools is not None:
            checks["worker_tools"] = worker_tools
        evidence["checks"] = checks
        write_evidence(context["out"], "worker_dispatch", "main", evidence)
        log(f"worker_dispatch checks={checks}")
    finally:
        serve.stop()


def lead_has_worker_report(home: Path, session_id: str, colony_path: Path) -> bool:
    for directory in (find_session_dir(home, session_id), colony_path):
        if directory is None:
            continue
        for part in read_parts(directory):
            if "[WORKER_REPORT]" in str(part.get("content") or ""):
                return True
    return False


def scenario_worker_llm_probe(context: dict) -> None:
    home = context["home"]
    proxy_log: Path = context["proxy_log"]
    extra = {
        "worker_llm": {
            "provider": "openai",
            "model": PROBE_WORKER_MODEL,
            "api_base": context["proxy_url"],
            "api_key_env_var": PROXY_KEY_ENV,
        }
    }
    write_config(home, context["proxy_url"], context["model"], extra)
    colony_path = write_colony(home, COLONY_ID, worker_definition("You are a worker agent. Call the get_current_time tool exactly once, then call report_to_parent with status='success' and a summary containing the returned time."))
    serve = Serve(context["repo_root"], home, None, context["out"] / "worker_llm_probe-serve.log")
    try:
        session_id, _ = serve.create({"colony_id": COLONY_ID})
        serve.chat(session_id, "Use the run_worker tool to dispatch exactly one worker with the task: call get_current_time once and report with report_to_parent.")
        probe = {}
        wait_until("worker probe model request", 240, lambda: probe_record(proxy_log, PROBE_WORKER_MODEL, lambda record: "report_to_parent" in (record.get("tools") or []), probe))
        time.sleep(8)
        records = load_records(proxy_log)
        evidence = base_evidence("worker_llm_probe", "probe")
        evidence["session_id"] = session_id
        evidence["session_live"] = live_view(serve.session(session_id)[1])
        evidence["session"] = session_evidence(serve, home, session_id)
        evidence["workers"] = worker_evidence(colony_path)
        evidence["tracker"] = tracker_dump(colony_path)
        evidence["model_requests"] = model_requests(records)
        checks: dict = {
            "worker_llm_consumed": bool(probe),
            "probe_request_status": probe.get("status"),
            "probe_request_advertised": probe.get("tools"),
            "main_slot_still_used": any(record.get("model") == context["model"] for record in records),
        }
        for worker in evidence["workers"]:
            state = worker.get("state") or {}
            if isinstance(state, dict) and state.get("status"):
                checks["worker_state_status"] = state["status"]
        evidence["checks"] = checks
        write_evidence(context["out"], "worker_llm_probe", "probe", evidence)
        log(f"worker_llm_probe checks={checks}")
    finally:
        serve.stop()


def probe_record(proxy_log: Path, model: str, want, sink: dict) -> bool:
    for record in load_records(proxy_log):
        if record.get("model") == model and want(record):
            sink.update(record)
            return True
    return False


def scenario_queen_llm_probe(context: dict) -> None:
    home = context["home"]
    proxy_log: Path = context["proxy_log"]
    extra = {
        "queen_llm": {
            "queen_growth": {
                "provider": "openai",
                "model": PROBE_QUEEN_MODEL,
                "api_base": context["proxy_url"],
                "api_key_env_var": PROXY_KEY_ENV,
            }
        }
    }
    write_config(home, context["proxy_url"], context["model"], extra)
    serve = Serve(context["repo_root"], home, None, context["out"] / "queen_llm_probe-serve.log")
    try:
        session_id, created = serve.create({"queen_name": "queen_growth"})
        serve.chat(session_id, "Reply with the single word: hello.")
        probe = {}
        wait_until("queen probe model request", 180, lambda: probe_record(proxy_log, PROBE_QUEEN_MODEL, lambda record: True, probe))
        time.sleep(8)
        evidence = base_evidence("queen_llm_probe", "probe")
        evidence["session_id"] = session_id
        evidence["session_live"] = live_view(serve.session(session_id)[1])
        evidence["create_response_queen_id"] = created.get("queen_id")
        evidence["session"] = session_evidence(serve, home, session_id)
        evidence["model_requests"] = model_requests(load_records(proxy_log))
        evidence["checks"] = {
            "queen_llm_consumed": bool(probe),
            "probe_request_status": probe.get("status"),
            "queen_id": created.get("queen_id"),
        }
        write_evidence(context["out"], "queen_llm_probe", "probe", evidence)
        log(f"queen_llm_probe checks={evidence['checks']}")
    finally:
        serve.stop()


def scenario_escalation_chain(context: dict) -> None:
    home = context["home"]
    proxy_log: Path = context["proxy_log"]
    write_config(home, context["proxy_url"], context["model"], None)
    colony_path = write_colony(home, COLONY_ID, worker_definition("You are a worker agent under test. The test harness REQUIRES this exact sequence: (1) call the task_list tool with an empty object three times in a row, with identical (empty) arguments; (2) then stop and wait — do NOT call report_to_parent yet; (3) only after a [QUEEN_REPLY] message arrives, call report_to_parent with status='success' and summary='recovered after escalation'. Do not call any other tool."))
    serve = Serve(context["repo_root"], home, None, context["out"] / "escalation_chain-serve.log")
    try:
        session_id, _ = serve.create({"colony_id": COLONY_ID})
        serve.chat(session_id, "Use run_worker to dispatch exactly one worker with the task: call task_list three times in a row with empty arguments, then wait for guidance. When you receive a [WORKER_ESCALATION] handoff from that worker, you MUST first call list_worker_questions, then call reply_to_worker with guidance telling it to stop repeating and to call report_to_parent now with status success. Then summarize the outcome for me.")
        wait_worker_report(colony_path, 540)
        wait_idle(serve, session_id, 120)

        records = load_records(proxy_log)
        marks: dict = {
            "worker_repeated_identical": False,
            "escalation_handoff_to_lead": False,
            "list_worker_questions": False,
            "reply_to_worker": False,
            "queen_reply_consumed": False,
            "worker_resumed_reported": False,
        }
        repeats = 0
        for record in records:
            for call in record.get("tool_calls") or []:
                if str(call).startswith("task_list "):
                    repeats += 1
                if str(call).startswith("list_worker_questions"):
                    marks["list_worker_questions"] = True
                if str(call).startswith("reply_to_worker"):
                    marks["reply_to_worker"] = True
                if str(call).startswith("report_to_parent"):
                    marks["worker_resumed_reported"] = True
            if "[QUEEN_REPLY]" in (record.get("body") or ""):
                marks["queen_reply_consumed"] = True
        # The handoff must be a real transcript message, not the queen's own
        # plan text echoed inside a tool call: look at the parts, not the body.
        for directory in (find_session_dir(home, session_id), colony_path):
            for part in read_parts(directory):
                if "[External event] [WORKER_ESCALATION]" in str(part.get("content") or ""):
                    marks["escalation_handoff_to_lead"] = True
        marks["identical_task_list_calls"] = repeats
        if repeats >= 3:
            marks["worker_repeated_identical"] = True

        evidence = base_evidence("escalation_chain", "main")
        evidence["session_id"] = session_id
        evidence["session_live"] = live_view(serve.session(session_id)[1])
        evidence["session"] = session_evidence(serve, home, session_id)
        evidence["colony_queen"] = {"parts": parts_view(read_parts(colony_path))}
        evidence["workers"] = worker_evidence(colony_path)
        evidence["tracker"] = tracker_dump(colony_path)
        evidence["model_requests"] = model_requests(records)
        evidence["checks"] = marks
        write_evidence(context["out"], "escalation_chain", "main", evidence)
        log(f"escalation_chain checks={marks}")
    finally:
        serve.stop()


def scenario_ask_user_park(context: dict) -> None:
    home = context["home"]
    proxy_log: Path = context["proxy_log"]
    write_config(home, context["proxy_url"], context["model"], None)
    serve = Serve(context["repo_root"], home, None, context["out"] / "ask_user_park-serve.log")
    try:
        session_id, _ = serve.create({})
        serve.chat(session_id, f"Call the ask_user tool right now with exactly one question: '{ASK_QUESTION}' Then wait for my answer. After I answer, reply with exactly: COLOUR=<my answer>")

        def parked() -> bool:
            directory = find_session_dir(home, session_id)
            for event in read_events(directory):
                if event.get("type") != "client_input_requested":
                    continue
                data = event.get("data") or {}
                if data.get("park_reason") == "ask_user" or data.get("questions"):
                    return True
            return False

        wait_until("ask_user park", 180, parked)
        park_snapshot = serve.snapshot(session_id)
        serve.chat(session_id, "blue")

        def answered() -> bool:
            directory = find_session_dir(home, session_id)
            for part in read_parts(directory):
                if part.get("role") == "assistant" and "blue" in str(part.get("content") or "").lower():
                    return True
            return False

        wait_until("reply after answer", 180, answered)
        wait_idle(serve, session_id, 60)

        evidence = base_evidence("ask_user_park", "main")
        evidence["session_id"] = session_id
        evidence["session_live"] = live_view(serve.session(session_id)[1])
        evidence["session"] = session_evidence(serve, home, session_id)
        evidence["park_snapshot"] = snapshot_view(park_snapshot)
        evidence["snapshot_after_answer"] = snapshot_view(serve.snapshot(session_id))
        evidence["model_requests"] = model_requests(load_records(proxy_log))
        checks = {"park_event": False, "ask_user_tool_call": False, "resumed_after_answer": False}
        for event in evidence["session"]["events"]:
            if event.get("type") == "client_input_requested":
                checks["park_event"] = True
        for record in load_records(proxy_log):
            for call in record.get("tool_calls") or []:
                if str(call).startswith("ask_user"):
                    checks["ask_user_tool_call"] = True
        for part in evidence["session"]["parts"]:
            if part.get("role") == "assistant" and "blue" in str(part.get("content") or "").lower():
                checks["resumed_after_answer"] = True
        evidence["checks"] = checks
        write_evidence(context["out"], "ask_user_park", "main", evidence)
        log(f"ask_user_park checks={checks}")
    finally:
        serve.stop()


def scenario_resume_after_restart(context: dict) -> None:
    home = context["home"]
    proxy_log: Path = context["proxy_log"]
    write_config(home, context["proxy_url"], context["model"], None)
    serve = Serve(context["repo_root"], home, None, context["out"] / "resume_after_restart-serve.log")
    try:
        dm_id, _ = serve.create({"queen_name": "queen_growth"})
        serve.chat(dm_id, "We are about to fork this conversation into a colony. Reply with OK.")
        wait_idle(serve, dm_id, 120)

        status, body = http_json(
            "POST",
            serve.base + "/api/sessions",
            {
                "colony_id": FORK_COLONY_ID,
                "source_session_id": dm_id,
                "initial_prompt": "Use run_worker to dispatch exactly one worker with the task: get the current time (terminal date is fine) and report it back with report_to_parent. After the worker reports, reply with a one-line summary.",
            },
        )
        forked_id = str(body.get("session_id") or "")
        if status not in (200, 201) or not forked_id:
            raise SystemExit(f"fork create -> {status} {body}")
        colony_path = home / "colonies" / FORK_COLONY_ID
        wait_worker_report(colony_path, 300)
        wait_idle(serve, forked_id, 120)

        metadata = {}
        meta_path = colony_path / "metadata.json"
        if meta_path.is_file():
            try:
                metadata = json.loads(meta_path.read_text(encoding="utf-8"))
            except ValueError:
                metadata = {}
        before = {
            "forked_session_id": forked_id,
            "metadata_queen_session_id": metadata.get("queen_session_id"),
            "session_parts": len(read_parts(find_session_dir(home, forked_id))),
            "worker_dirs": len(worker_dirs(colony_path)),
            "tracker": tracker_dump(colony_path),
            "model_requests_before_restart_n": len(load_records(proxy_log)),
        }
        serve.stop()

        restarted = Serve(context["repo_root"], home, str(colony_path), context["out"] / "resume_after_restart-restarted.log")
        try:
            time.sleep(2)
            list_status, list_body = http_json("GET", restarted.base + "/api/sessions")
            live_ids = [str(entry.get("session_id")) for entry in list_body.get("sessions") or [] if entry.get("session_id")]
            get_status, get_body = restarted.session(forked_id)
            chat_status, chat_body = restarted.chat(forked_id, "In one line: what did the worker report?")

            def replied() -> bool:
                for part in read_parts(find_session_dir(home, forked_id)):
                    if part.get("role") == "assistant" and str(part.get("content") or "").strip():
                        return True
                return False

            resumed_reply = False
            if chat_status == 200:
                resumed_reply = wait_until("resumed fork reply", 180, replied)
            wait_idle(restarted, forked_id, 60)

            evidence = base_evidence("resume_after_restart", "main")
            evidence["session_id"] = forked_id
            evidence["dm_session_id"] = dm_id
            evidence["before_restart"] = before
            evidence["after_restart"] = {
                "list_status": list_status,
                "live_session_ids": live_ids,
                "get_status": get_status,
                "get_body": str(get_body)[:1200],
                "chat_status": chat_status,
                "chat_body": str(chat_body)[:600],
                "session_parts": len(read_parts(find_session_dir(home, forked_id))),
                "worker_dirs": len(worker_dirs(colony_path)),
                "tracker": tracker_dump(colony_path),
            }
            checks = {
                "metadata_records_session": metadata.get("queen_session_id") == forked_id,
                "session_live_after_restart": forked_id in live_ids,
                "session_readable_after_restart": get_status == 200,
                "resumed_reply": resumed_reply,
                "worker_dirs_intact": len(worker_dirs(colony_path)) > 0,
                "tracker_intact": before["tracker"].get("exists") is True,
            }
            evidence["checks"] = checks
            write_evidence(context["out"], "resume_after_restart", "main", evidence)
            log(f"resume_after_restart checks={checks}")
        finally:
            restarted.stop()
    finally:
        serve.stop()


SCENARIOS = {
    "worker_dispatch": scenario_worker_dispatch,
    "worker_llm_probe": scenario_worker_llm_probe,
    "queen_llm_probe": scenario_queen_llm_probe,
    "escalation_chain": scenario_escalation_chain,
    "ask_user_park": scenario_ask_user_park,
    "resume_after_restart": scenario_resume_after_restart,
}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--go-module", required=True)
    parser.add_argument("--repo-root", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--upstream", required=True)
    parser.add_argument("--model", default="deepseek-flash-1017")
    parser.add_argument("--only", default="", help="comma-separated scenario names")
    args = parser.parse_args()

    if os.environ.get("HIVE_REAL_LLM_E2E") != "1":
        print("real-model concept-flow dual run is opt-in: set HIVE_REAL_LLM_E2E=1", file=sys.stderr)
        return 2
    key_env = os.environ.get("HIVE_REAL_LLM_KEY_ENV", "BROADTOKEN_API_KEY")
    api_key = os.environ.get(key_env, "").strip()
    if not api_key:
        print(f"{key_env} is empty; the recording proxy needs the real upstream key", file=sys.stderr)
        return 2

    out = Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    go_module = Path(args.go_module).resolve()

    proxy_binary = out / "recordproxy"
    build = subprocess.run(
        ["go", "build", "-o", str(proxy_binary), "./e2e/cmd/recordproxy"],
        cwd=go_module,
        env={**os.environ, "CGO_ENABLED": "0", "GOPROXY": "off"},
    )
    if build.returncode != 0:
        return build.returncode

    scenarios = list(SCENARIOS)
    if args.only:
        scenarios = [name.strip() for name in args.only.split(",") if name.strip()]

    failures = 0
    for name in scenarios:
        scenario_out = out / name
        scenario_out.mkdir(parents=True, exist_ok=True)
        proxy_log = scenario_out / "model-requests.jsonl"
        proxy = subprocess.Popen(
            [str(proxy_binary), "-upstream", args.upstream, "-log", str(proxy_log), "-key-env", key_env],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        try:
            line = proxy.stdout.readline().strip()
            match = re.search(r"(http://[^\s]+)/v1", line)
            if not match:
                log(f"proxy failed to report its URL: {line!r}")
                failures += 1
                continue
            proxy_url = match.group(1) + "/v1"
            home = Path(tempfile.mkdtemp(prefix=f"cf-py-{name}-"))
            context = {
                "repo_root": args.repo_root,
                "home": home,
                "proxy_url": proxy_url,
                "proxy_log": proxy_log,
                "model": args.model,
                "out": out,
            }
            log(f"scenario {name}: home={home} proxy={proxy_url}")
            try:
                SCENARIOS[name](context)
            except SystemExit as exc:
                log(f"scenario {name} aborted: {exc}")
                failures += 1
            except Exception as exc:  # noqa: BLE001
                log(f"scenario {name} crashed: {exc!r}")
                failures += 1
        finally:
            proxy.terminate()
            try:
                proxy.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proxy.kill()

    log(f"python run complete: failures={failures}")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
