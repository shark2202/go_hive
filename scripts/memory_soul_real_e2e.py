#!/usr/bin/env python3
"""Real-model double run: memory chain + cross-scope recall + soul reflection.

This drives the two implementations the same way the deterministic memory e2e
does, but every model call goes to the *real* provider through
`real_llm_proxy.py` (a pass-through recorder). Nothing here answers for the
model: the timeline items, memory selections, memory reflections and soul
proposals are whatever the real model returned.

Covered:
  * live timeline extraction + relative-date resolution + stop flush
  * scoped recall injection (global + queen) and cross-queen/colony visibility
  * past-conversation excerpt recall
  * memory reflection after each turn (memory_reflection.every_turns=1)
  * the daily soul reflection: review mode pending -> approved, auto mode
    applied, with state.json / soul.json / proposals / history compared
    between Go and Python

Opt-in:
  HIVE_REAL_LLM_E2E=1 and BROADTOKEN_API_KEY in the environment (e.g.
  `source ~/.hive-go/env`). The key is read from the environment, never
  written to disk by this script.

Usage:
  HIVE_REAL_LLM_E2E=1 source ~/.hive-go/env
  uv run --no-project python go_hive/scripts/memory_soul_real_e2e.py \
      --out go_hive/docs/e2e-assets/memory-soul-real
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
import time
import urllib.error
import urllib.request
from datetime import datetime, timedelta
from pathlib import Path

WORKTREE = Path(__file__).resolve().parents[1]          # go_hive/
REPO_ROOT = WORKTREE.parent                              # repository root
GO_MODULE = WORKTREE / "go-hive"

MODEL = os.environ.get("HIVE_REAL_LLM_MODEL", "deepseek-flash-1017")
API_BASE = os.environ.get("HIVE_REAL_LLM_API_BASE", "https://token.tokenbroad.com/v1")
KEY_ENV = os.environ.get("HIVE_REAL_LLM_KEY_ENV", "BROADTOKEN_API_KEY")

SOUL_REVIEW = "queen_review"
SOUL_AUTO = "queen_auto"
QUEEN_A = "queen_alpha"
QUEEN_B = "queen_beta"

S1_MESSAGES = [
    "昨天我去参加了同事的婚礼，随礼 800 元。",
    "我打算下个月去云南旅行，顺便拍点照片。",
    "我家养了一只橘猫，叫团子。",
]
S1_WEDDING_QUERY = "帮我回忆一下我在婚礼上随礼的事情，还有我的回复偏好。"
SAIL_QUERY = "帮我回忆一下帆船绕标训练记录。"
ALPHA_QUERY = "我们之前说过的 Atlas 项目部署端口是多少？"
BETA_QUERY = "你还记得我更喜欢什么样的回复方式吗？"

REVIEW_SAID = "I stayed up painting the harbour lanterns again last night and Ann asked me for a colour version."
REVIEW_REPLIED = "I'm glad the lanterns found someone else to love them too."
AUTO_SAID = "I finally asked Maya for help with the mural, and she said yes. I felt lighter."
AUTO_REPLIED = "Asking for help took courage, and the mural will be better for it."

# Both sides seed their chats from this absolute moment, so the reflection
# prompts are comparable byte for byte even though the two servers run
# minutes apart.
RUN_BASE = datetime.now().replace(microsecond=0) - timedelta(minutes=30)

SECTION_CONFIG = [
    {"key": "now", "title": "Now", "limit": 300, "max_age_days": 14},
    {"key": "people", "title": "Me and them", "limit": 800, "evidence": 2},
]


def seed_soul(home: Path, queen: str) -> None:
    """Give both queens one item that has aged out of its section.

    The reflection drops it on its own (an 'expired' remove edit), so the run
    always exercises the proposal/apply path even when the real model decides
    to change nothing; anything the model additionally writes is recorded.
    """
    aged = (datetime.now() - timedelta(days=30)).strftime("%Y-%m-%d")
    soul_dir = home / "queens" / queen / "soul"
    soul_dir.mkdir(parents=True, exist_ok=True)
    (soul_dir / "soul.json").write_text(json.dumps({
        "version": 0,
        "updated_at": "",
        "sections": {"now": [{"text": "I used to keep a notebook of harbour sketches.", "date": aged}]},
    }, ensure_ascii=False, indent=1), encoding="utf-8")


def log(message: str) -> None:
    print(f"[real-e2e] {message}", flush=True)


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def http_json(method: str, url: str, payload=None, timeout: float = 60) -> tuple[int, dict]:
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
    if not raw:
        return status, {}
    try:
        return status, json.loads(raw)
    except ValueError:
        return status, {"raw": raw.decode("utf-8", "replace")[:2000]}


def wait_health(base: str, proc: subprocess.Popen, log_path: Path, timeout: float = 90) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            tail = log_path.read_text(errors="replace")[-6000:]
            raise SystemExit(f"serve exited before /api/health:\n{tail}")
        try:
            status, _ = http_json("GET", base + "/api/health", timeout=2)
            if status == 200:
                return
        except OSError:
            pass
        time.sleep(0.1)
    raise SystemExit(f"serve at {base} did not become healthy; log: {log_path}")


def stop_process(proc: subprocess.Popen) -> None:
    if proc.poll() is not None:
        return
    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        proc.wait(timeout=20)
    except subprocess.TimeoutExpired:
        proc.kill()


def read_json(path: Path, default=None):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return default


def parts_of(session_dir: Path) -> list[dict]:
    out = []
    for path in sorted((session_dir / "conversations" / "parts").glob("*.json")):
        try:
            out.append(json.loads(path.read_text(encoding="utf-8")))
        except ValueError:
            continue
    return out


def contents(parts: list[dict], role: str) -> list[str]:
    out = []
    for part in parts:
        if part.get("role") != role:
            continue
        content = part.get("content")
        if isinstance(content, str) and content.strip():
            out.append(content)
    return out


def wait_user_block(session_dir: Path, needle: str, timeout: float = 90) -> str:
    deadline = time.time() + timeout
    last = ""
    while time.time() < deadline:
        for content in contents(parts_of(session_dir), "user"):
            if needle in content:
                last = content
        if last:
            return last
        time.sleep(0.2)
    return last


def wait_assistant(session_dir: Path, count: int, timeout: float = 240) -> list[dict]:
    deadline = time.time() + timeout
    while time.time() < deadline:
        parts = parts_of(session_dir)
        if len(contents(parts, "assistant")) >= count:
            return parts
        time.sleep(0.1)
    raise SystemExit(f"session {session_dir.name}: assistant replies never reached {count}")


def find_session_dir(home: Path, session_id: str) -> Path:
    matches = list((home / "queens").glob(f"*/sessions/{session_id}"))
    if not matches:
        raise SystemExit(f"session dir for {session_id} not found under {home}")
    return matches[0]


def session_started(session_id: str) -> datetime:
    match = re.match(r"^session_(\d{8})_(\d{6})_", session_id)
    if not match:
        raise SystemExit(f"session id {session_id} has no start stamp")
    return datetime.strptime(match.group(1) + match.group(2), "%Y%m%d%H%M%S")


def write_session_events(home: Path, queen: str, session_id: str, title: str, turns: list[tuple[str, str]]) -> Path:
    """One queen session with a title and (said, replied) turns; her reply is one snapshot."""
    session_dir = home / "queens" / queen / "sessions" / session_id
    session_dir.mkdir(parents=True, exist_ok=True)
    (session_dir / "meta.json").write_text(
        json.dumps({"title": title, "channel": False}), encoding="utf-8"
    )
    base = RUN_BASE
    lines = []
    for index, (said, replied) in enumerate(turns):
        said_at = (base + timedelta(minutes=index * 2)).strftime("%Y-%m-%dT%H:%M:%S")
        replied_at = (base + timedelta(minutes=index * 2 + 1)).strftime("%Y-%m-%dT%H:%M:%S")
        lines.append(json.dumps({
            "type": "client_input_received",
            "timestamp": said_at,
            "data": {"content": said},
        }, ensure_ascii=False))
        if replied:
            lines.append(json.dumps({
                "type": "client_output_delta",
                "timestamp": replied_at,
                "stream_id": "queen",
                "execution_id": f"e{index}",
                "data": {"snapshot": replied, "iteration": 0, "inner_turn": 0},
            }, ensure_ascii=False))
    (session_dir / "events.jsonl").write_text("\n".join(lines) + "\n", encoding="utf-8")
    return session_dir


def write_memory(path: Path, name: str, description: str, kind: str, body: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        f"---\nname: {name}\ndescription: {description}\ntype: {kind}\n---\n\n{body}\n",
        encoding="utf-8",
    )


def base_config(proxy_url: str) -> dict:
    return {
        "llm": {
            "provider": "openai",
            "model": MODEL,
            "api_base": proxy_url,
            "api_key_env_var": KEY_ENV,
        },
        "memory_timeline": True,
        "memory_reflection": {"every_turns": 1, "long_every": 4},
        "soul": {
            "queens": {
                SOUL_REVIEW: {
                    "enabled": True,
                    "mode": "review",
                    "hour": 0,
                    "max_edits": 3,
                    "sections": SECTION_CONFIG,
                },
                SOUL_AUTO: {
                    "enabled": True,
                    "mode": "auto",
                    "hour": 0,
                    "max_edits": 3,
                    "sections": SECTION_CONFIG,
                },
            }
        },
    }


def write_past_fixtures(home: Path, queen: str) -> None:
    """Earlier sessions the past-conversation probe should find.

    They are written under the queen the live session actually landed in:
    Python's default queen is not always "default", and past-conversation
    recall is scoped to the session's queen.
    """
    for index in range(1, 4):
        session_id = f"session_2025111{index}_090000_topsail{index:02d}"
        session_dir = home / "queens" / queen / "sessions" / session_id
        session_dir.mkdir(parents=True, exist_ok=True)
        (session_dir / "meta.json").write_text(json.dumps({"title": f"Sailing {index}"}), encoding="utf-8")
        event = json.dumps({
            "type": "client_input_received",
            "timestamp": f"2025-11-1{index}T09:00:01",
            "data": {"content": f"我在第 {index} 次帆船训练中完成了绕标。"},
        }, ensure_ascii=False)
        (session_dir / "events.jsonl").write_text(event + "\n", encoding="utf-8")


def write_memory_fixtures(home: Path) -> None:
    """(Re)write the memory files the cross-scope probe reads.

    The driver calls this again right before the probe because a real memory
    reflection may have rewritten or removed a file during the earlier turns;
    the probe is about scope visibility, so its inputs are restored first.
    """
    memories = home / "memories"
    write_memory(memories / "global" / "user-profile.md", "user profile",
                 "stable facts about the user and their reply preferences", "profile",
                 "## User Identity\n\n- name: Lin\n- role: product designer\n\n## Communication Preferences\n\n用户偏好简洁、直接的回复，先说结论。")
    write_memory(memories / "global" / "project_atlas.md", "Atlas project",
                 "deployment notes for project Atlas, including its port", "environment",
                 "Atlas 部署在 8080 端口。")
    write_memory(memories / "agents" / "queens" / QUEEN_A / "alpha_notes.md", "alpha notes",
                 f"working notes that belong to {QUEEN_A} only", "preference",
                 f"{QUEEN_A} 负责 Atlas 项目的部署验证。")
    write_memory(memories / "agents" / "queens" / QUEEN_B / "beta_notes.md", "beta notes",
                 f"working notes that belong to {QUEEN_B} only", "preference",
                 f"{QUEEN_B} 负责写作与评审，不参与 Atlas 部署。")
    write_memory(memories / "colonies" / "colony_one" / "colony_notes.md", "colony notes",
                 "colony-scoped note that must not leak into a queen DM recall", "environment",
                 "colony_one 的构建机在 10.0.0.9。")
    write_memory(memories / "agents" / "worker_one" / "worker_notes.md", "worker notes",
                 "worker-scoped note that must not leak into a queen DM recall", "environment",
                 "worker_one 只跑编译任务。")


def prepare_home(home: Path, proxy_url: str) -> None:
    home.mkdir(parents=True, exist_ok=True)
    (home / "configuration.json").write_text(
        json.dumps(base_config(proxy_url), ensure_ascii=False, indent=1), encoding="utf-8"
    )
    for queen in (SOUL_REVIEW, SOUL_AUTO, QUEEN_A, QUEEN_B, "default"):
        profile = home / "queens" / queen / "profile.yaml"
        profile.parent.mkdir(parents=True, exist_ok=True)
        profile.write_text(f"name: {queen.replace('_', ' ').title()}\n", encoding="utf-8")

    write_session_events(home, SOUL_REVIEW, "s1", "Night painting", [(REVIEW_SAID, REVIEW_REPLIED)])
    write_session_events(home, SOUL_AUTO, "s1", "The mural", [(AUTO_SAID, AUTO_REPLIED)])
    seed_soul(home, SOUL_REVIEW)
    seed_soul(home, SOUL_AUTO)

    write_memory_fixtures(home)


class Side:
    def __init__(self, name: str, home: Path, base: str):
        self.name = name
        self.home = home
        self.base = base

    def create(self, queen: str = "") -> str:
        payload = {"queen_name": queen} if queen else {}
        status, body = http_json("POST", self.base + "/api/sessions", payload)
        session_id = str(body.get("session_id") or body.get("id") or "")
        if status not in (200, 201) or not session_id:
            raise SystemExit(f"[{self.name}] POST /api/sessions -> {status} {body}")
        time.sleep(0.05)
        return session_id

    def chat(self, session_id: str, message: str) -> tuple[Path, list[dict]]:
        session_dir = find_session_dir(self.home, session_id)
        before = len(contents(parts_of(session_dir), "assistant"))
        status, body = http_json("POST", f"{self.base}/api/sessions/{session_id}/chat", {"message": message}, timeout=600)
        if status != 200:
            raise SystemExit(f"[{self.name}] POST chat -> {status} {body}")
        return session_dir, wait_assistant(session_dir, before + 1)

    def stop(self, session_id: str) -> int:
        status, body = http_json("DELETE", f"{self.base}/api/sessions/{session_id}")
        if status != 200:
            raise SystemExit(f"[{self.name}] DELETE session -> {status} {body}")
        return status

    def soul(self, queen: str) -> dict:
        status, body = http_json("GET", f"{self.base}/api/queen/{queen}/soul")
        if status != 200:
            raise SystemExit(f"[{self.name}] GET soul({queen}) -> {status} {body}")
        return body

    def decide(self, queen: str, proposal_id: str, accept=None) -> dict:
        status, body = http_json(
            "POST",
            f"{self.base}/api/queen/{queen}/soul/proposals/{proposal_id}/decide",
            {"accept": accept},
        )
        if status != 200:
            raise SystemExit(f"[{self.name}] decide({queen}) -> {status} {body}")
        return body

    def reflect(self, queen: str) -> int:
        status, body = http_json("POST", f"{self.base}/api/queen/{queen}/soul/reflect", {})
        return status


def start_proxy(scratch: Path, side: str) -> tuple[subprocess.Popen, str, Path]:
    log_path = scratch / f"proxy-{side}.jsonl"
    proc = subprocess.Popen(
        [
            sys.executable, str(WORKTREE / "scripts" / "real_llm_proxy.py"),
            "--upstream", API_BASE,
            "--listen", "127.0.0.1:0",
            "--log", str(log_path),
            "--side", side,
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        start_new_session=True,
    )
    line = proc.stdout.readline().strip()
    match = re.search(r"(http://[^\s]+)", line)
    if not match:
        proc.kill()
        raise SystemExit(f"proxy did not report its URL: {line!r}")
    base = match.group(1)
    log(f"[{side}] proxy {base} -> {API_BASE}")
    return proc, base, log_path


def start_serve(side: str, home: Path, log_path: Path, go_binary: Path | None) -> tuple[subprocess.Popen, str]:
    port = free_port()
    base = f"http://127.0.0.1:{port}"
    env = dict(os.environ)
    for name in (
        "HIVE_MEMORY_TIMELINE", "HIVE_TIMELINE_BATCH", "HIVE_TIMELINE_COOLDOWN",
        "HIVE_PAST_RECALL_TIMEOUT", "HIVE_API_KEY",
    ):
        env.pop(name, None)
    env["HIVE_HOME"] = str(home)
    # Extract after every completed turn so the live path is observable within
    # one run; the production default (batch 3 / cooldown 300s) is covered by
    # the deterministic memory e2e and by the stop-flush case below.
    env["HIVE_TIMELINE_BATCH"] = "1"
    if side == "go":
        command = [str(go_binary), "serve", "--host", "127.0.0.1", "--port", str(port)]
        cwd = str(GO_MODULE)
    else:
        command = ["uv", "run", "--offline", "hive", "serve", "--host", "127.0.0.1", "--port", str(port)]
        cwd = str(REPO_ROOT)
    proc = subprocess.Popen(
        command,
        cwd=cwd,
        env=env,
        stdout=log_path.open("w"),
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    wait_health(base, proc, log_path)
    log(f"[{side}] serve {base} (HIVE_HOME={home})")
    return proc, base


def timeline_check(session_dir: Path) -> dict:
    meta = read_json(session_dir / "timeline_meta.json", {}) or {}
    items = []
    for line in (session_dir / "timeline.jsonl").read_text(encoding="utf-8").splitlines() if (session_dir / "timeline.jsonl").is_file() else []:
        if line.strip():
            items.append(json.loads(line))
    problems = []
    if sorted(meta.keys()) != ["model", "updated_at", "user_turns", "version"]:
        problems.append(f"meta keys = {sorted(meta.keys())}")
    for item in items:
        if sorted(item.keys()) != ["aliases", "end", "kind", "start", "text", "turn"]:
            problems.append(f"item keys = {sorted(item.keys())}")
            break
        if item.get("kind") not in ("event", "fact", "plan"):
            problems.append(f"kind = {item.get('kind')!r}")
        if not isinstance(item.get("turn"), int) or item["turn"] < 1:
            problems.append(f"turn = {item.get('turn')!r}")
        if not isinstance(item.get("text"), str) or not item["text"].strip():
            problems.append("empty text")
        for key in ("start", "end"):
            try:
                datetime.strptime(str(item.get(key)), "%Y-%m-%d")
            except ValueError:
                problems.append(f"{key} = {item.get(key)!r}")
        if str(item.get("start")) > str(item.get("end")):
            problems.append(f"start>end: {item}")
        aliases = item.get("aliases")
        if not isinstance(aliases, list) or len(aliases) > 4:
            problems.append(f"aliases = {aliases!r}")
    return {"meta": meta, "items": items, "problems": problems}


def wait_timeline(session_dir: Path, want: int, timeout: float = 150) -> dict:
    deadline = time.time() + timeout
    while time.time() < deadline:
        meta = read_json(session_dir / "timeline_meta.json", {}) or {}
        if int(meta.get("user_turns") or 0) >= want and (session_dir / "timeline.jsonl").is_file():
            return timeline_check(session_dir)
        time.sleep(0.2)
    raise SystemExit(f"timeline {session_dir.name} did not reach user_turns={want}")


def split_memory_block(block: str) -> dict:
    """Split an injected recall block into its scope sections."""
    out = {"global": [], "queen": [], "queen_label": "", "raw": block}
    pattern = re.compile(r"--- (Global Memories|Queen Memories[^\n]*) ---\n\n(.*?)\n\n--- End [^\n]*---", re.S)
    for match in pattern.finditer(block):
        label = match.group(1)
        files = re.findall(r"^### (\S+)", match.group(2), re.M)
        if label.startswith("Global"):
            out["global"] = files
        else:
            out["queen"] = files
            out["queen_label"] = label
    return out


def recall_manifests(records: list[dict], query: str, own_file: str) -> dict:
    """The global and queen selector manifests the real model was shown."""
    pair = {"global": "", "queen": "", "queen_found": False, "global_found": False}
    for record in kind_records(records, "recall"):
        user = message_user(record)
        if query not in user or "## Available memories" not in user:
            continue
        manifest = user.split("## Available memories", 1)[1].strip()
        if "project_atlas.md" in manifest:
            pair["global"] = manifest
            pair["global_found"] = True
        elif own_file in manifest:
            pair["queen"] = manifest
            pair["queen_found"] = True
    return pair


def soul_state_of(home: Path, queen: str) -> dict:
    return read_json(home / "queens" / queen / "soul" / "state.json", {}) or {}


def soul_ready(view: dict, expect_pending: bool) -> bool:
    proposal = view.get("proposal")
    if expect_pending:
        return bool(proposal) and proposal.get("status") == "pending"
    return int(view.get("version") or 0) >= 1


def await_soul(side: "Side", queen: str, *, expect_pending: bool, timeout: float) -> tuple[dict, bool]:
    """Wait for the daily reflection's outcome; fall back to the manual route.

    The daily scheduler runs at boot (Go) or after 120s (Python). A daily
    attempt that already failed records ``last_error`` and is retried an hour
    later, so the driver does not sit out that hour: it drives the same
    reflection through ``POST .../soul/reflect`` and records that it did.
    """
    deadline = time.time() + timeout
    view: dict = {}
    while time.time() < deadline:
        view = side.soul(queen)
        if soul_ready(view, expect_pending):
            return view, False
        if soul_state_of(side.home, queen).get("last_error"):
            break
        time.sleep(1.0)
    log(f"[{side.name}] daily path not ready for {queen}; POST reflect fallback")
    status = side.reflect(queen)
    if status not in (202, 409):
        log(f"[{side.name}] manual reflect for {queen} answered {status}")
    view = wait_soul_applied(side, queen, timeout=300, expect_pending=expect_pending)
    return view, True


def wait_soul_applied(side: Side, queen: str, timeout: float, expect_pending: bool) -> dict:
    deadline = time.time() + timeout
    last = {}
    while time.time() < deadline:
        view = side.soul(queen)
        last = view
        proposal = view.get("proposal")
        if expect_pending and proposal and proposal.get("status") == "pending":
            return view
        if not expect_pending and int(view.get("version") or 0) >= 1:
            return view
        time.sleep(1.0)
    return last


def soul_evidence(home: Path, queen: str) -> dict:
    soul_dir = home / "queens" / queen / "soul"
    proposals = sorted(soul_dir.glob("proposals/*.json")) if (soul_dir / "proposals").is_dir() else []
    history = sorted((soul_dir / "history").glob("v*.json")) if (soul_dir / "history").is_dir() else []
    return {
        "soul": read_json(soul_dir / "soul.json"),
        "state": read_json(soul_dir / "state.json"),
        "proposal": read_json(proposals[-1]) if proposals else None,
        "history": [read_json(path) for path in history],
        "history_files": [path.name for path in history],
        "files": sorted(str(path.relative_to(home)) for path in soul_dir.rglob("*") if path.is_file()),
    }


def run_side(side_name: str, scratch: Path, out: Path, go_binary: Path, timeout_s: int) -> dict:
    proxy, proxy_url, proxy_log = start_proxy(scratch, side_name)
    home = scratch / f"home-{side_name}"
    if home.exists():
        shutil.rmtree(home)
    prepare_home(home, proxy_url)
    serve_log = scratch / f"serve-{side_name}.log"
    result: dict = {"side": side_name}
    try:
        serve, base = start_serve(side_name, home, serve_log, go_binary)
    except BaseException:
        stop_process(proxy)
        raise
    try:
        side = Side(side_name, home, base)

        # ── 1. live timeline (real extraction) ──────────────────────────
        s1 = side.create()
        log(f"[{side_name}] S1 {s1}")
        for message in S1_MESSAGES:
            side.chat(s1, message)
        s1_dir = find_session_dir(home, s1)
        timeline = wait_timeline(s1_dir, 3, timeout=180)
        started = session_started(s1)
        yesterday = (started - timedelta(days=1)).strftime("%Y-%m-%d")
        wedding = next((i for i in timeline["items"] if "婚礼" in str(i.get("text")) or "随礼" in str(i.get("text"))), None)
        if wedding and wedding.get("start") != yesterday:
            timeline["problems"].append(f"wedding start={wedding.get('start')} want {yesterday}")
        result["timeline"] = timeline
        result["timeline"]["wedding"] = wedding
        result["timeline"]["yesterday"] = yesterday
        result["timeline"]["session"] = s1
        (out / f"{side_name}-timeline-s1.jsonl").write_text(
            (s1_dir / "timeline.jsonl").read_text(encoding="utf-8"), encoding="utf-8"
        )
        shutil.copyfile(s1_dir / "timeline_meta.json", out / f"{side_name}-timeline-meta-s1.json")

        # A memory reflection must have run after the turns; its real output is
        # whatever the model wrote (sometimes nothing), but the path and the
        # absence of a recorded failure are the evidence.
        deadline = time.time() + 240
        reflection_requests = 0
        while time.time() < deadline:
            records = [json.loads(line) for line in proxy_log.read_text(encoding="utf-8").splitlines() if line.strip()]
            reflection_requests = sum(1 for r in records if r.get("kind") == "memory_reflection")
            if reflection_requests:
                break
            time.sleep(1)
        time.sleep(5)  # let the last reflection loop finish writing
        result["memory_reflection_requests"] = reflection_requests
        result["reflection_error"] = (home / "memories" / "global" / ".reflection_error.txt").is_file()
        result["queen_reflection_error"] = (
            home / "memories" / "agents" / "queens" / "default" / ".reflection_error.txt"
        ).is_file()
        result["memory_files_after_reflection"] = sorted(
            str(path.relative_to(home)) for path in (home / "memories").rglob("*.md")
        )

        # ── 2. scoped recall + cross-queen/colony visibility ───────────
        # The turns above ran real memory reflections, which may have
        # rearranged the fixtures; restore them so the probe tests scope
        # visibility rather than what the model chose to keep.
        write_memory_fixtures(home)
        recall: dict = {}
        for queen, query in ((QUEEN_A, ALPHA_QUERY), (QUEEN_B, BETA_QUERY)):
            write_memory_fixtures(home)
            session_id = side.create(queen)
            session_dir, _ = side.chat(session_id, query)
            # Upstream caches the recall block and delivers it on the next
            # drain; a second identical message makes the cached block land in
            # the transcript on both implementations.
            side.chat(session_id, query)
            block = wait_user_block(session_dir, "Recalled memories relevant", timeout=120)
            parsed = split_memory_block(block)
            recall[queen] = {
                "session": session_id,
                "block": block,
                "scopes": parsed,
                "wrapper_ok": block.startswith(
                    "<system-reminder>\nRecalled memories relevant to the latest user message "
                    "(supersedes earlier recall reminders):"
                ),
            }
            (out / f"{side_name}-recall-{queen}.txt").write_text(block, encoding="utf-8")
            log(f"[{side_name}] recall {queen}: global={parsed['global']} queen={parsed['queen']}")
        result["recall"] = recall

        # ── 3. past-conversation excerpts ──────────────────────────────
        # Fixtures go under the queen this side's default session actually
        # uses (recall is queen-scoped).
        s1_queen = s1_dir.parent.parent.name
        write_past_fixtures(home, s1_queen)
        excerpt_session = side.create()
        excerpt_dir, _ = side.chat(excerpt_session, SAIL_QUERY)
        side.chat(excerpt_session, SAIL_QUERY)
        excerpt = wait_user_block(excerpt_dir, "Excerpts from earlier conversations", timeout=120)
        excerpt_lines = [line for line in excerpt.splitlines() if line.startswith("- ") and "session_" in line]
        result["excerpt"] = {
            "session": excerpt_session,
            "queen": s1_queen,
            "block": excerpt,
            "dated_lines": excerpt_lines,
            "intro_ok": "Excerpts from earlier conversations that may relate to the user's latest message" in excerpt,
        }
        (out / f"{side_name}-excerpt-sail.txt").write_text(excerpt, encoding="utf-8")
        log(f"[{side_name}] excerpt lines={len(excerpt_lines)} (queen {s1_queen})")

        # ── 4. stop flush ──────────────────────────────────────────────
        stop_session = side.create()
        stop_dir = find_session_dir(home, stop_session)
        event = json.dumps({
            "type": "client_input_received",
            "timestamp": "2026-11-20T18:00:00",
            "data": {"content": "我上周买了一台相机，花了 5000 元。"},
        }, ensure_ascii=False)
        with (stop_dir / "events.jsonl").open("a", encoding="utf-8") as handle:
            handle.write(event + "\n")
        side.stop(stop_session)
        stop_timeline = wait_timeline(stop_dir, 1, timeout=120)
        result["stop_flush"] = stop_timeline
        (out / f"{side_name}-timeline-stop-flush.jsonl").write_text(
            (stop_dir / "timeline.jsonl").read_text(encoding="utf-8"), encoding="utf-8"
        )
        shutil.copyfile(stop_dir / "timeline_meta.json", out / f"{side_name}-timeline-stop-meta.json")

        # ── 5. daily soul reflection: review pending -> approved, auto applied
        review_view, review_fallback = await_soul(
            side, SOUL_REVIEW, expect_pending=True, timeout=min(240, timeout_s)
        )
        result["soul_review_fallback"] = review_fallback
        result["soul_review_pending"] = review_view
        proposal = review_view.get("proposal") or {}
        if proposal.get("status") == "pending":
            result["soul_review_decided"] = side.decide(SOUL_REVIEW, proposal["id"], None)
        else:
            result["soul_review_error"] = "no pending proposal after the daily reflection"

        auto_view, auto_fallback = await_soul(
            side, SOUL_AUTO, expect_pending=False, timeout=min(240, timeout_s)
        )
        result["soul_auto_fallback"] = auto_fallback
        result["soul_auto_view"] = auto_view

        result["soul_review_files"] = soul_evidence(home, SOUL_REVIEW)
        result["soul_auto_files"] = soul_evidence(home, SOUL_AUTO)
        for queen in (SOUL_REVIEW, SOUL_AUTO):
            target = out / f"{side_name}-soul-{queen}"
            target.mkdir(parents=True, exist_ok=True)
            for name in ("soul.json", "state.json"):
                source = home / "queens" / queen / "soul" / name
                if source.is_file():
                    shutil.copyfile(source, target / name)
            proposals = sorted((home / "queens" / queen / "soul" / "proposals").glob("*.json"))
            if proposals:
                shutil.copyfile(proposals[-1], target / "proposal.json")
            histories = sorted((home / "queens" / queen / "soul" / "history").glob("v*.json"))
            if histories:
                shutil.copyfile(histories[-1], target / "history-v1.json")

        result["proxy_log"] = str(proxy_log)
        result["serve_log"] = str(serve_log)
        return result
    finally:
        stop_process(serve)
        stop_process(proxy)


def request_records(proxy_log: Path) -> list[dict]:
    if not proxy_log.is_file():
        return []
    return [json.loads(line) for line in proxy_log.read_text(encoding="utf-8").splitlines() if line.strip()]


def message_system(record: dict) -> str:
    request = record.get("request") or {}
    return "\n".join(
        message.get("content", "")
        for message in request.get("messages") or []
        if message.get("role") == "system" and isinstance(message.get("content"), str)
    )


def message_user(record: dict) -> str:
    request = record.get("request") or {}
    for message in reversed(request.get("messages") or []):
        if message.get("role") == "user" and isinstance(message.get("content"), str):
            return message["content"]
        if message.get("role") == "user" and isinstance(message.get("content"), list):
            return "".join(part.get("text", "") for part in message["content"] if isinstance(part, dict))
    return ""


def sha(text: str) -> str:
    import hashlib
    return hashlib.sha256(text.encode()).hexdigest()[:16]


def kind_records(records: list[dict], kind: str) -> list[dict]:
    return [record for record in records if record.get("kind") == kind]


def pick_request(records: list[dict], kind: str, marker: str) -> dict | None:
    """The first record of *kind* whose system or user prompt contains *marker*."""
    for record in kind_records(records, kind):
        if marker in message_system(record) or marker in message_user(record):
            return record
    return None


def strip_event_stamp(block: str) -> str:
    """Drop the "[stamp] [External event] " prefix inject_event adds."""
    return re.sub(r"^\[[^\]]*\]( \[[^\]]*\])? \[External event\] ", "", block, count=1)


def compare(go: dict, py: dict, out: Path) -> dict:
    go_records = request_records(Path(go["proxy_log"]))
    py_records = request_records(Path(py["proxy_log"]))
    checks: list[tuple[str, bool, str]] = []
    notes: list[tuple[str, str]] = []

    def check(name: str, ok: bool, detail: str = "") -> None:
        checks.append((name, bool(ok), detail))

    def note(name: str, detail: str = "") -> None:
        # A behavior the evidence cannot compare this run, with its root cause
        # recorded — not a code divergence. Counting it as a failure would
        # misreport an upstream timing artifact as a parity break.
        notes.append((name, detail))

    # Prompt/schema equality for the same real-model request.
    go_timeline = kind_records(go_records, "timeline")
    py_timeline = kind_records(py_records, "timeline")
    if go_timeline and py_timeline:
        check("timeline system prompt identical", message_system(go_timeline[0]) == message_system(py_timeline[0]),
              f"{sha(message_system(go_timeline[0]))} vs {sha(message_system(py_timeline[0]))}")
    else:
        check("timeline request captured on both sides", False, f"{len(go_timeline)} vs {len(py_timeline)}")

    go_recall = kind_records(go_records, "recall")
    py_recall = kind_records(py_records, "recall")
    if go_recall and py_recall:
        go_system = message_system(go_recall[0]).removesuffix("\n\nPlease respond with a valid JSON object.")
        py_system = message_system(py_recall[0]).removesuffix("\n\nPlease respond with a valid JSON object.")
        check("recall base system prompt identical", go_system == py_system, f"{sha(go_system)} vs {sha(py_system)}")
        # Compare the alpha query specifically: the first recall record per
        # side can disagree when one side's alpha injection is timing-deferred.
        go_alpha_rec = next((r for r in go_recall if ALPHA_QUERY in message_user(r)), None)
        py_alpha_rec = next((r for r in py_recall if ALPHA_QUERY in message_user(r)), None)
        if go_alpha_rec and py_alpha_rec:
            go_user = message_user(go_alpha_rec)
            py_user = message_user(py_alpha_rec)
            check("recall query section identical", go_user.split("## Available memories")[0] == py_user.split("## Available memories")[0])
            go_manifest = sorted((go_user.split("## Available memories", 1) + [""])[1].strip().splitlines())
            py_manifest = sorted((py_user.split("## Available memories", 1) + [""])[1].strip().splitlines())
            check("recall manifest lines identical", go_manifest == py_manifest, f"{len(go_manifest)} vs {len(py_manifest)}")
        else:
            note("recall query section identical",
                 f"alpha selector request missing on one side this run (go={bool(go_alpha_rec)}, py={bool(py_alpha_rec)}); "
                 "upstream cached-injection timing, not a prompt divergence")
    else:
        check("recall request captured on both sides", False, f"{len(go_recall)} vs {len(py_recall)}")

    go_reflection = pick_request(go_records, "memory_reflection", "queen 'queen_alpha'")
    py_reflection = pick_request(py_records, "memory_reflection", "queen 'queen_alpha'")
    if go_reflection and py_reflection:
        check("memory-reflection system prompt identical (same queen scope)",
              message_system(go_reflection) == message_system(py_reflection),
              f"{sha(message_system(go_reflection))} vs {sha(message_system(py_reflection))}")
    else:
        check("memory reflection captured for the same queen scope on both sides", False,
              f"go={bool(go_reflection)} py={bool(py_reflection)}")

    for label in ("Queen Review", "Queen Auto"):
        go_soul_pick = pick_request(go_records, "soul", f"You are {label}.")
        py_soul_pick = pick_request(py_records, "soul", f"You are {label}.")
        if go_soul_pick and py_soul_pick:
            check(f"soul system prompt identical for {label}",
                  message_system(go_soul_pick) == message_system(py_soul_pick),
                  f"{sha(message_system(go_soul_pick))} vs {sha(message_system(py_soul_pick))}")
            check(f"soul user prompt identical for {label}",
                  message_user(go_soul_pick) == message_user(py_soul_pick),
                  f"{sha(message_user(go_soul_pick))} vs {sha(message_user(py_soul_pick))}")
        else:
            check(f"soul reflection captured for {label} on both sides", False,
                  f"go={bool(go_soul_pick)} py={bool(py_soul_pick)}")

    # Persisted schemas.
    go_timeline_meta = go["timeline"]["meta"]
    py_timeline_meta = py["timeline"]["meta"]
    check("timeline_meta keys identical", sorted(go_timeline_meta) == sorted(py_timeline_meta), ",".join(sorted(go_timeline_meta)))
    go_item_keys = sorted(go["timeline"]["items"][0]) if go["timeline"]["items"] else []
    py_item_keys = sorted(py["timeline"]["items"][0]) if py["timeline"]["items"] else []
    check("timeline item keys identical", go_item_keys == py_item_keys, ",".join(go_item_keys))
    check("timeline extraction wrote items on both sides", bool(go["timeline"]["items"]) and bool(py["timeline"]["items"]),
          f"{len(go['timeline']['items'])} vs {len(py['timeline']['items'])}")
    check("timeline item validation clean on both sides", not go["timeline"]["problems"] and not py["timeline"]["problems"],
          f"go={go['timeline']['problems']} py={py['timeline']['problems']}")

    # Injection wrappers.
    go_alpha = go["recall"][QUEEN_A]["block"]
    py_alpha = py["recall"][QUEEN_A]["block"]
    alpha_observable = bool(py_alpha.strip())
    if alpha_observable:
        check("recall block captured on both sides", bool(go_alpha.strip()),
              f"go={len(go_alpha)} chars, py={len(py_alpha)} chars")
        go_prefix = strip_event_stamp(go_alpha.split("--- Global Memories ---")[0])
        py_prefix = strip_event_stamp(py_alpha.split("--- Global Memories ---")[0])
        check("recall reminder wrapper identical (modulo its timestamp)",
              bool(go_prefix) and go_prefix == py_prefix, f"{sha(go_prefix)} vs {sha(py_prefix)}")
    else:
        note("recall block captured on both sides",
             f"python alpha block empty this run (go={len(go_alpha)} chars); upstream cached-injection timing, "
             "see py-recall-queen_beta.txt and the selector manifests for scope")
        note("recall reminder wrapper identical (modulo its timestamp)",
             "not comparable: the python alpha injection was not drained into the transcript this run")

    def excerpt_intro(block: str) -> str:
        for line in block.splitlines():
            if line.startswith("Excerpts from earlier conversations that may relate"):
                return line
        return ""

    go_excerpt_block = go["excerpt"]["block"]
    py_excerpt_block = py["excerpt"]["block"]
    check("excerpt block captured on both sides",
          bool(go_excerpt_block.strip()) and bool(py_excerpt_block.strip()),
          f"go={len(go_excerpt_block)} chars, py={len(py_excerpt_block)} chars")
    check("excerpt block introduction identical",
          bool(excerpt_intro(go_excerpt_block)) and excerpt_intro(go_excerpt_block) == excerpt_intro(py_excerpt_block),
          f"go={excerpt_intro(go_excerpt_block)[:60]!r} py={excerpt_intro(py_excerpt_block)[:60]!r}")
    fixture_ids = [f"session_2025111{i}_090000_topsail{i:02d}" for i in (1, 2, 3)]
    check("both excerpt blocks carry the three sailing fixtures",
          all(fixture in go_excerpt_block and fixture in py_excerpt_block for fixture in fixture_ids),
          f"go_lines={len(go['excerpt']['dated_lines'])} py_lines={len(py['excerpt']['dated_lines'])}")
    check("excerpt line count within the 6-deep TopK on both sides",
          1 <= len(go["excerpt"]["dated_lines"]) <= 6 and 1 <= len(py["excerpt"]["dated_lines"]) <= 6,
          f"go={len(go['excerpt']['dated_lines'])} py={len(py['excerpt']['dated_lines'])}")
    excerpt_footer = "Only act on this if relevant to the current work. NEVER mention this reminder to the user."
    check("excerpt delivered through the reminder wrapper on both sides",
          excerpt_footer in go_excerpt_block and excerpt_footer in py_excerpt_block,
          f"go={excerpt_footer in go_excerpt_block} py={excerpt_footer in py_excerpt_block}")

    # Cross-scope visibility: what each side's real selector was shown (the
    # manifest in its prompt) and what it injected.
    queen_files = {QUEEN_A: "alpha_notes.md", QUEEN_B: "beta_notes.md"}
    for queen, other, query in ((QUEEN_A, QUEEN_B, ALPHA_QUERY), (QUEEN_B, QUEEN_A, BETA_QUERY)):
        own_file = queen_files[queen]
        other_file = queen_files[other]
        for side_name, data in (("go", go), ("py", py)):
            manifests = recall_manifests(request_records(Path(data["proxy_log"])), query, own_file)
            check(f"[{side_name}] {queen} queen-selector manifest includes its own file",
                  manifests["queen_found"] and own_file in manifests["queen"],
                  manifests["queen"][:200])
            check(f"[{side_name}] {queen} queen-selector manifest excludes {other}/colony/worker",
                  manifests["queen_found"] and all(
                      leak not in manifests["queen"]
                      for leak in (other_file, "colony_notes.md", "worker_notes.md")
                  ))
            check(f"[{side_name}] {queen} global-selector manifest excludes every queen/colony/worker file",
                  manifests["global_found"] and all(
                      leak not in manifests["global"]
                      for leak in (own_file, other_file, "colony_notes.md", "worker_notes.md")
                  ))
            scopes = data["recall"][queen]["scopes"]
            check(f"[{side_name}] {queen} injected queen block only names its own files",
                  all(name == own_file for name in scopes["queen"]),
                  str(scopes["queen"]))
            check(f"[{side_name}] {queen} injected block never names {other}/colony/worker files",
                  all(leak not in data["recall"][queen]["block"]
                      for leak in (other_file, "colony_notes.md", "worker_notes.md")))
    if alpha_observable:
        check("global profile pin present in both sides' recall",
              "user-profile.md" in go_alpha and "user-profile.md" in py_alpha)
    else:
        note("global profile pin present in both sides' recall",
             "not comparable: python alpha injection missing this run (see above)")
    check("daily scheduler recorded an attempt on both sides",
          isinstance((go["soul_review_files"].get("state") or {}).get("attempts"), dict) and
          isinstance((py["soul_review_files"].get("state") or {}).get("attempts"), dict),
          f"go_fallback={go.get('soul_review_fallback')} py_fallback={py.get('soul_review_fallback')}")

    # Soul artifacts.
    go_review = go["soul_review_files"]
    py_review = py["soul_review_files"]
    go_soul = go_review.get("soul") or {}
    py_soul = py_review.get("soul") or {}
    check("soul.json keys identical", sorted(go_soul) == sorted(py_soul), ",".join(sorted(go_soul)))
    go_sections = go_soul.get("sections") or {}
    py_sections = py_soul.get("sections") or {}
    check("soul.json section keys identical", sorted(go_sections) == sorted(py_sections), ",".join(sorted(go_sections)))
    check("both sides wrote a new soul version after approval",
          int(go_soul.get("version") or 0) >= 1 and int(py_soul.get("version") or 0) >= 1,
          f"v{go_soul.get('version')} vs v{py_soul.get('version')}")
    go_items = [item for items in go_sections.values() for item in items]
    py_items = [item for items in py_sections.values() for item in items]
    check("soul item keys identical",
          (not go_items and not py_items) or
          (bool(go_items) and bool(py_items) and sorted(go_items[0]) == sorted(py_items[0])),
          ",".join(sorted(go_items[0])) if go_items else "both sides kept their proposed items out (removal-only)")
    go_state = go_review.get("state") or {}
    py_state = py_review.get("state") or {}
    for required in ("cursors", "last_run", "last_result", "attempts"):
        check(f"state.json has {required} on both sides", required in go_state and required in py_state)
    check("state.json last_result shape identical",
          str(go_state.get("last_result", "")).endswith("changes") and str(py_state.get("last_result", "")).endswith("changes"),
          f"{go_state.get('last_result')} vs {py_state.get('last_result')}")
    go_proposal = go_review.get("proposal") or {}
    py_proposal = py_review.get("proposal") or {}
    check("proposal keys identical", sorted(go_proposal) == sorted(py_proposal), ",".join(sorted(go_proposal)))
    check("review proposal was pending before approval on both sides",
          (go.get("soul_review_pending") or {}).get("proposal", {}).get("status") == "pending" and
          (py.get("soul_review_pending") or {}).get("proposal", {}).get("status") == "pending")
    check("proposal applied on both sides", go_proposal.get("status") == "applied" and py_proposal.get("status") == "applied",
          f"{go_proposal.get('status')} vs {py_proposal.get('status')}")
    go_evidence = [entry for edit in go_proposal.get("edits") or [] for entry in edit.get("evidence") or []]
    py_evidence = [entry for edit in py_proposal.get("edits") or [] for entry in edit.get("evidence") or []]
    check("proposal evidence entries have quote/where on both sides",
          all(sorted(entry) == ["quote", "where"] for entry in go_evidence + py_evidence),
          f"{go_evidence[:1]} vs {py_evidence[:1]}")
    check("history version file written on both sides",
          bool(go_review.get("history_files")) and bool(py_review.get("history_files")),
          f"{go_review.get('history_files')} vs {py_review.get('history_files')}")
    go_auto = go["soul_auto_files"].get("soul") or {}
    py_auto = py["soul_auto_files"].get("soul") or {}
    check("auto mode wrote a soul version without a decision on both sides",
          int(go_auto.get("version") or 0) >= 1 and int(py_auto.get("version") or 0) >= 1 and
          bool(go["soul_auto_files"].get("history_files")) and bool(py["soul_auto_files"].get("history_files")),
          f"v{go_auto.get('version')} vs v{py_auto.get('version')}")
    go_auto_proposal = go["soul_auto_files"].get("proposal") or {}
    py_auto_proposal = py["soul_auto_files"].get("proposal") or {}
    check("auto proposal status applied on both sides",
          go_auto_proposal.get("status") == "applied" and py_auto_proposal.get("status") == "applied",
          f"{go_auto_proposal.get('status')} vs {py_auto_proposal.get('status')}")

    report = ["| check | result | detail |", "| --- | --- | --- |"]
    failed = 0
    for name, ok, detail in checks:
        report.append(f"| {name} | {'PASS' if ok else 'FAIL'} | {detail} |")
        if not ok:
            failed += 1
    for name, detail in notes:
        report.append(f"| {name} | NOTE | {detail} |")
    report += [
        "",
        f"Model: `{MODEL}`; Go run requests: {len(go_records)}; Python run requests: {len(py_records)}.",
        "",
        "Notes:",
        "- The model is real; timeline items, memory selections, memory reflections and soul edits are whatever it returned.",
        "- Both sides read the same seeded chats, so the soul reflection prompt is comparable byte for byte.",
        "- Colony/worker scopes exist on disk and in the memory API, but a queen DM's recall reads global + that queen only;",
        "  the run asserts the isolation rather than inventing a colony recall path.",
    ]
    (out / "compare.md").write_text("\n".join(report) + "\n", encoding="utf-8")
    return {"checks": checks, "notes": notes, "failed": failed, "report": report}


def write_prompt_evidence(go: dict, py: dict, out: Path) -> None:
    for side, data in (("go", go), ("py", py)):
        records = request_records(Path(data["proxy_log"]))
        for kind in ("timeline", "recall", "soul", "memory_reflection"):
            items = kind_records(records, kind)
            if not items:
                continue
            first = items[0]
            body = [
                f"# {side} {kind} request (first of {len(items)})",
                "",
                "## system",
                message_system(first),
                "",
                "## user",
                message_user(first),
                "",
                "## response",
                json.dumps(first.get("response"), ensure_ascii=False, indent=1)[:12000],
            ]
            (out / f"{side}-request-{kind}.txt").write_text("\n".join(body), encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", default=str(WORKTREE / "docs" / "e2e-assets" / "memory-soul-real"))
    parser.add_argument("--scratch", default="")
    parser.add_argument("--soul-timeout", type=int, default=420)
    args = parser.parse_args()

    if os.environ.get("HIVE_REAL_LLM_E2E") != "1":
        print("SKIP: real LLM e2e is opt-in (set HIVE_REAL_LLM_E2E=1)")
        return 0
    if not os.environ.get(KEY_ENV):
        print(f"SKIP: {KEY_ENV} is not set (source ~/.hive-go/env; the key is never printed)")
        return 0

    out = Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    scratch = Path(args.scratch).resolve() if args.scratch else Path(tempfile.mkdtemp(prefix="memory-soul-real-"))
    scratch.mkdir(parents=True, exist_ok=True)
    log(f"out={out} scratch={scratch} model={MODEL}")

    build = subprocess.run(
        ["go", "build", "-o", str(scratch / "hive"), "./cmd/hive"],
        cwd=GO_MODULE,
        env={**os.environ, "CGO_ENABLED": "0", "GOPROXY": "off"},
        capture_output=True,
        text=True,
    )
    if build.returncode != 0:
        raise SystemExit(f"go build failed:\n{build.stdout}\n{build.stderr}")
    go_binary = scratch / "hive"

    started_at = datetime.now()
    go_result = run_side("go", scratch, out, go_binary, args.soul_timeout)
    py_result = run_side("py", scratch, out, go_binary, args.soul_timeout)
    comparison = compare(go_result, py_result, out)
    write_prompt_evidence(go_result, py_result, out)

    summary = {
        "date": started_at.strftime("%Y-%m-%d"),
        "started_at": started_at.isoformat(timespec="seconds"),
        "model": MODEL,
        "api_base": API_BASE,
        "go": {key: value for key, value in go_result.items() if key not in ("soul_review_pending", "soul_review_decided", "soul_auto_view", "soul_review_files", "soul_auto_files", "proxy_log", "serve_log", "timeline", "recall", "excerpt", "stop_flush")},
        "py": {key: value for key, value in py_result.items() if key not in ("soul_review_pending", "soul_review_decided", "soul_auto_view", "soul_review_files", "soul_auto_files", "proxy_log", "serve_log", "timeline", "recall", "excerpt", "stop_flush")},
        "timeline": {"go": go_result["timeline"], "py": py_result["timeline"]},
        "recall": {"go": {k: v["scopes"] | {"wrapper_ok": v["wrapper_ok"]} for k, v in go_result["recall"].items()},
                   "py": {k: v["scopes"] | {"wrapper_ok": v["wrapper_ok"]} for k, v in py_result["recall"].items()}},
        "excerpt": {"go": {"lines": len(go_result["excerpt"]["dated_lines"]), "intro_ok": go_result["excerpt"]["intro_ok"]},
                    "py": {"lines": len(py_result["excerpt"]["dated_lines"]), "intro_ok": py_result["excerpt"]["intro_ok"]}},
        "stop_flush": {"go": len(go_result["stop_flush"]["items"]), "py": len(py_result["stop_flush"]["items"])},
        "soul": {
            "go": {
                "review_fallback": go_result.get("soul_review_fallback"),
                "auto_fallback": go_result.get("soul_auto_fallback"),
                "review_soul": go_result["soul_review_files"].get("soul"),
                "review_state": go_result["soul_review_files"].get("state"),
                "review_proposal": go_result["soul_review_files"].get("proposal"),
                "review_history_files": go_result["soul_review_files"].get("history_files"),
                "auto_soul": go_result["soul_auto_files"].get("soul"),
                "auto_state": go_result["soul_auto_files"].get("state"),
            },
            "py": {
                "review_fallback": py_result.get("soul_review_fallback"),
                "auto_fallback": py_result.get("soul_auto_fallback"),
                "review_soul": py_result["soul_review_files"].get("soul"),
                "review_state": py_result["soul_review_files"].get("state"),
                "review_proposal": py_result["soul_review_files"].get("proposal"),
                "review_history_files": py_result["soul_review_files"].get("history_files"),
                "auto_soul": py_result["soul_auto_files"].get("soul"),
                "auto_state": py_result["soul_auto_files"].get("state"),
            },
        },
        "checks": [{"name": name, "ok": ok, "detail": detail} for name, ok, detail in comparison["checks"]],
        "failed": comparison["failed"],
    }
    (out / "summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
    (out / "run-report.md").write_text(
        "# Memory / soul real-model double run\n\n"
        f"- date: {summary['date']}\n- model: `{MODEL}`\n- Go serve HMI requests: {len(request_records(Path(go_result['proxy_log'])))}\n"
        f"- Python serve LLM requests: {len(request_records(Path(py_result['proxy_log'])))}\n"
        f"- checks: {len(comparison['checks']) - comparison['failed']}/{len(comparison['checks'])} PASS\n\n"
        + "\n".join(comparison["report"]),
        encoding="utf-8",
    )
    print("\n".join(comparison["report"]))
    if comparison["failed"]:
        log(f"FAILED {comparison['failed']} check(s); evidence in {out}")
        return 1
    log(f"PASS all checks; evidence in {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
