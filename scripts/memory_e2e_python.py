#!/usr/bin/env python3
"""Python half of the memory end-to-end double run.

Drives a real `hive serve` (Python implementation) through the same steps as
`go test -tags e2e ./e2e -run TestMemoryEndToEndGoRuntime`:

  * live timeline extraction (timeline.jsonl + timeline_meta.json)
  * relative-date resolution and item validation (via the deterministic gateway)
  * stop flush, startup backfill
  * search_timeline / search_messages tool calls, including CJK
  * past-conversation excerpts (dated) and their de-duplication
  * scoped memory recall: query-only selector, pinned profile backfill,
    no repeat injection, selector failure must not block the turn

The model is `e2e/memgw` in the Go module: a deterministic OpenAI-compatible
gateway that records every prompt. Its log is the cross-implementation prompt
diff evidence.

Usage:
  uv run --no-project python go_hive/scripts/memory_e2e_python.py \
      --go-module <worktree>/go_hive/go-hive --out /tmp/memory-e2e-py
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

S1_MESSAGES = [
    "昨天我去参加了同事的婚礼，随礼 800 元。",
    "我打算下个月去云南旅行，顺便拍点照片。",
    "我家养了一只橘猫，叫团子。",
]
PAST_ASK = "帮我回忆一下我在婚礼上随礼的事情，还有北京的安排。"


def log(message: str) -> None:
    print(f"[py-e2e] {message}", flush=True)


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def http_json(method: str, url: str, payload: dict | None = None, timeout: float = 30) -> tuple[int, dict]:
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
    body = {}
    if raw:
        try:
            body = json.loads(raw)
        except ValueError:
            body = {"raw": raw.decode("utf-8", "replace")[:400]}
    return status, body


def wait_health(base: str, proc: subprocess.Popen, log_path: Path, timeout: float = 40) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            tail = log_path.read_text(errors="replace")[-4000:]
            raise SystemExit(f"python hive serve exited before /api/health:\n{tail}")
        try:
            status, _ = http_json("GET", base + "/api/health", timeout=2)
            if status == 200:
                return
        except OSError:
            pass
        time.sleep(0.1)
    raise SystemExit("python hive serve did not become healthy")


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


def wait_assistant(session_dir: Path, count: int, timeout: float = 25) -> list[dict]:
    deadline = time.time() + timeout
    while time.time() < deadline:
        parts = parts_of(session_dir)
        if len(contents(parts, "assistant")) >= count:
            return parts
        time.sleep(0.05)
    raise SystemExit(f"session {session_dir.name}: assistant replies never reached {count}")


def wait_contains(session_dir: Path, needles: list[str], timeout: float = 25) -> list[dict]:
    deadline = time.time() + timeout
    while time.time() < deadline:
        parts = parts_of(session_dir)
        for content in contents(parts, "assistant"):
            if all(needle in content for needle in needles):
                return parts
        time.sleep(0.05)
    raise SystemExit(f"session {session_dir.name}: no assistant reply containing {needles}")


def wait_timeline(session_dir: Path, want_turns: int, timeout: float = 20) -> dict:
    deadline = time.time() + timeout
    timeline = session_dir / "timeline.jsonl"
    meta_path = session_dir / "timeline_meta.json"
    while time.time() < deadline:
        if timeline.is_file() and meta_path.is_file():
            try:
                meta = json.loads(meta_path.read_text(encoding="utf-8"))
            except ValueError:
                meta = {}
            if int(meta.get("user_turns") or 0) >= want_turns:
                return meta
        time.sleep(0.05)
    raise SystemExit(f"timeline {session_dir} did not reach user_turns={want_turns}")


def read_items(session_dir: Path) -> list[dict]:
    return [
        json.loads(line)
        for line in (session_dir / "timeline.jsonl").read_text(encoding="utf-8").splitlines()
        if line.strip()
    ]


def find_item(items: list[dict], needle: str) -> dict | None:
    return next((item for item in items if needle in str(item.get("text", ""))), None)


def wait_user_block(session_dir: Path, needle: str, timeout: float = 10) -> str:
    """Poll the transcript for a user part containing *needle* (injections land
    asynchronously upstream: recall is cached and refreshed in the background)."""
    deadline = time.time() + timeout
    last = ""
    while time.time() < deadline:
        for content in contents(parts_of(session_dir), "user"):
            if needle in content:
                last = content
        if last:
            return last
        time.sleep(0.05)
    return last


def session_started(session_id: str) -> datetime:
    match = re.match(r"^session_(\d{8})_(\d{6})_", session_id)
    if not match:
        raise SystemExit(f"session id {session_id} has no start stamp")
    return datetime.strptime(match.group(1) + match.group(2), "%Y%m%d%H%M%S")


def find_session_dir(home: Path, session_id: str) -> Path:
    matches = list((home / "queens").glob(f"*/sessions/{session_id}"))
    if not matches:
        raise SystemExit(f"session directory for {session_id} not found under {home}")
    return matches[0]


def copy_evidence(out: Path, name: str, source: Path) -> None:
    if source.is_file():
        shutil.copyfile(source, out / name)
    else:
        log(f"evidence {name} skipped: {source} missing")


class Run:
    def __init__(self, home: Path, base: str):
        self.home = home
        self.base = base

    def create(self) -> str:
        status, body = http_json("POST", self.base + "/api/sessions", {})
        session_id = str(body.get("session_id") or body.get("id") or "")
        if status not in (200, 201) or not session_id:
            raise SystemExit(f"POST /api/sessions -> {status} {body}")
        time.sleep(0.05)
        return session_id

    def chat(self, session_id: str, message: str) -> tuple[Path, list[dict]]:
        session_dir = find_session_dir(self.home, session_id)
        before = len(contents(parts_of(session_dir), "assistant"))
        status, body = http_json("POST", f"{self.base}/api/sessions/{session_id}/chat", {"message": message})
        if status != 200:
            raise SystemExit(f"POST chat -> {status} {body}")
        return session_dir, wait_assistant(session_dir, before + 1)

    def stop(self, session_id: str) -> None:
        status, body = http_json("DELETE", f"{self.base}/api/sessions/{session_id}")
        if status != 200:
            raise SystemExit(f"DELETE session -> {status} {body}")


def start_serve(repo_root: str, home: Path, log_path: Path, extra_env: dict[str, str]) -> tuple[subprocess.Popen, str]:
    port = free_port()
    base = f"http://127.0.0.1:{port}"
    env = dict(os.environ)
    for name in ("HIVE_MEMORY_TIMELINE", "HIVE_TIMELINE_BATCH", "HIVE_TIMELINE_COOLDOWN", "OPENAI_API_KEY"):
        env.pop(name, None)
    env.update({"HIVE_HOME": str(home), "MEMGW_KEY": "memory-e2e-key", **extra_env})
    proc = subprocess.Popen(
        ["uv", "run", "--offline", "hive", "serve", "--host", "127.0.0.1", "--port", str(port)],
        cwd=repo_root,
        env=env,
        stdout=log_path.open("w"),
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    wait_health(base, proc, log_path)
    return proc, base


def stop_serve(proc: subprocess.Popen) -> None:
    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        proc.wait(timeout=15)
    except subprocess.TimeoutExpired:
        proc.kill()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--go-module", default="/Users/mac/hive/go_hive/go-hive")
    parser.add_argument("--repo-root", default="/Users/mac/hive")
    parser.add_argument("--out", default="/tmp/memory-e2e-py")
    parser.add_argument("--keep-home", default="")
    args = parser.parse_args()

    go_module = Path(args.go_module).resolve()
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    gateway_log = out / "py-gateway-requests.jsonl"
    gateway_log.unlink(missing_ok=True)

    memgw_bin = out / "memgw"
    subprocess.run(
        ["go", "build", "-o", str(memgw_bin), "./e2e/cmd/memgw"],
        cwd=go_module,
        env={**os.environ, "CGO_ENABLED": "0", "GOPROXY": "off"},
        check=True,
    )
    gateway = subprocess.Popen(
        [str(memgw_bin), "-addr", "127.0.0.1:0", "-log", str(gateway_log)],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    line = gateway.stdout.readline().strip()
    match = re.search(r"(http://[^\s]+)/v1", line)
    if not match:
        gateway.kill()
        raise SystemExit(f"memgw did not report its URL: {line!r}")
    gateway_url = match.group(1)
    log(f"gateway {gateway_url}")

    home = Path(args.keep_home).resolve() if args.keep_home else Path(tempfile.mkdtemp(prefix="hive-py-memory-"))
    home.mkdir(parents=True, exist_ok=True)
    (home / "configuration.json").write_text(
        json.dumps(
            {
                "llm": {
                    "provider": "openai",
                    "model": "memory-e2e",
                    "api_base": gateway_url + "/v1",
                    "api_key_env_var": "MEMGW_KEY",
                },
                "memory_timeline": True,
            }
        ),
        encoding="utf-8",
    )
    log(f"HIVE_HOME {home}")

    try:
        # ── Phase A: BATCH=1 live timeline, tools, backfill, past, recall ─
        serve, base = start_serve(args.repo_root, home, out / "py-serve-main.log", {"HIVE_TIMELINE_BATCH": "1"})
        try:
            run = Run(home, base)

            s1 = run.create()
            log(f"S1 {s1}")
            for message in S1_MESSAGES:
                run.chat(s1, message)
            s1_dir = find_session_dir(home, s1)
            meta = wait_timeline(s1_dir, 3)
            assert int(meta.get("version")) == 1, meta
            assert meta.get("model"), meta
            assert meta.get("updated_at"), meta
            items = read_items(s1_dir)
            started = session_started(s1)
            yesterday = (started - timedelta(days=1)).strftime("%Y-%m-%d")
            said_on = started.strftime("%Y-%m-%d")
            wedding = find_item(items, "婚礼")
            assert wedding, items
            assert wedding["start"] == yesterday, (wedding, yesterday)
            assert wedding["end"] == yesterday, wedding
            assert wedding["kind"] == "event", wedding
            assert len(wedding.get("aliases") or []) == 4, wedding
            dentist = find_item(items, "牙医")
            assert dentist and dentist["start"] < dentist["end"], (dentist,)
            assert dentist["start"] == (started - timedelta(days=15)).strftime("%Y-%m-%d"), dentist
            cat = find_item(items, "团子")
            assert cat and cat["kind"] == "event" and cat["start"] == said_on, cat
            assert not find_item(items, "不应写入"), items
            copy_evidence(out, "py-timeline-s1.jsonl", s1_dir / "timeline.jsonl")
            copy_evidence(out, "py-timeline-meta-s1.json", s1_dir / "timeline_meta.json")
            log(f"timeline live OK: {len(items)} items, meta={meta}")

            s5 = run.create()
            log(f"S5 {s5}")
            s5_dir, _ = run.chat(s5, 'CALL search_timeline {"pattern":"婚礼"}')
            wait_contains(s5_dir, ["TOOL_RESULT_ECHO", "婚礼", s1])
            reply_token = re.search(r"ASSISTANT_REPLY_\d+", contents(parts_of(s1_dir), "assistant")[-1]).group(0)
            run.chat(s5, f'CALL search_messages {{"pattern":"{reply_token}","role":"assistant"}}')
            wait_contains(s5_dir, ["TOOL_RESULT_ECHO", reply_token, s1])
            run.chat(s5, 'CALL search_messages {"pattern":"婚礼"}')
            wait_contains(s5_dir, ["TOOL_RESULT_ECHO", "婚礼", s1])
            log("search_timeline / search_messages OK")

            old_id = "session_20250101_093000_meme2e01"  # same fixture id as the Go run: the prompts must match byte for byte
            queen = s1_dir.parent.parent.name  # queens/<queen>/sessions/<id>
            log(f"queen {queen}")
            old_dir = home / "queens" / queen / "sessions" / old_id
            old_dir.mkdir(parents=True, exist_ok=True)
            events = [
                {"type": "client_input_received", "timestamp": "2025-01-01T09:30:01", "data": {"content": "去年我在杭州买了一套房子。"}},
                {"type": "client_input_received", "timestamp": "2025-01-01T09:31:02", "data": {"content": "前年我换了一辆黑色的车。"}},
            ]
            (old_dir / "events.jsonl").write_text(
                "\n".join(json.dumps(event, ensure_ascii=False) for event in events) + "\n", encoding="utf-8"
            )
            s3 = run.create()
            log(f"S3 {s3}")
            wait_timeline(old_dir, 2)
            old_wedding = find_item(read_items(old_dir), "婚礼")
            assert old_wedding and old_wedding["start"] == "2024-12-31", old_wedding
            copy_evidence(out, "py-timeline-backfill-2025.jsonl", old_dir / "timeline.jsonl")
            log("backfill OK")

            global_dir = home / "memories" / "global"
            queen_dir = home / "memories" / "agents" / "queens" / queen
            global_dir.mkdir(parents=True, exist_ok=True)
            queen_dir.mkdir(parents=True, exist_ok=True)
            (global_dir / "profile_user.md").write_text(
                "---\nname: user profile\ndescription: stable facts about the user\ntype: profile\n---\n\n用户偏好简洁、直接的回复。\n",
                encoding="utf-8",
            )
            (global_dir / "project_atlas.md").write_text(
                "---\nname: Atlas project\ndescription: deployment notes for project Atlas\ntype: preference\n---\n\nAtlas 部署在 8080 端口。\n",
                encoding="utf-8",
            )
            (queen_dir / "queen_notes.md").write_text(
                "---\nname: queen notes\ndescription: working notes for this queen\ntype: preference\n---\n\n这个 queen 负责记忆验证。\n",
                encoding="utf-8",
            )
            s4 = run.create()
            log(f"S4 {s4}")
            s4_dir, first_turn = run.chat(s4, PAST_ASK)
            joined = "\n".join(contents(first_turn, "user"))
            for needle in ("Excerpts from earlier conversations", s1):
                assert needle in joined, f"first turn missing {needle!r}"
            assert joined.count("Excerpts from earlier conversations") == 1, joined
            excerpt_lines = [line for line in joined.splitlines() if line.startswith("- ") and s1 in line]
            assert 1 <= len(excerpt_lines) <= 10, excerpt_lines

            # Scoped recall is cached and refreshed in the background upstream:
            # poll for the injected block instead of assuming it beat the turn.
            recall_text = wait_user_block(s4_dir, "Recalled memories relevant")
            assert recall_text, "no recall block was injected within 10s"
            for needle in ("project_atlas.md", "profile_user.md", "queen_notes.md", f"--- Queen Memories: {queen} ---"):
                assert needle in recall_text, (needle, recall_text[:600])

            requests = [
                json.loads(line) for line in gateway_log.read_text(encoding="utf-8").splitlines() if line.strip()
            ]
            recall_requests = [
                request
                for request in requests
                if request.get("kind") == "recall" and PAST_ASK in request.get("body", "")
            ]
            assert len(recall_requests) >= 2, len(recall_requests)
            for request in recall_requests:
                body = json.loads(request["body"])
                user_messages = [message for message in body.get("messages", []) if message.get("role") == "user"]
                assert len(user_messages) == 1, user_messages
                text = user_messages[0].get("content") or ""
                assert "## User query" in text and PAST_ASK in text, text[:200]
                for stale in ("云南", "团子", "相机"):
                    assert stale not in text, (stale, text[:200])

            _, second_turn = run.chat(s4, PAST_ASK)
            joined_second = "\n".join(contents(second_turn, "user"))
            # The excerpt reminder re-renders on every user prompt upstream.
            assert joined_second.count("Excerpts from earlier conversations") == 2, joined_second
            # The cached recall block did not change, so it is not re-injected.
            assert joined_second.count("Recalled memories relevant") == 1, joined_second

            _, fail_turn = run.chat(s4, "FAILRECALL 今天天气怎么样？")
            assert any("ASSISTANT_REPLY_" in content for content in contents(fail_turn, "assistant")), fail_turn
            # Upstream's refresh leaves the cache untouched when the selector
            # fails, so no new block is guaranteed. Record what happened; when
            # one does appear it may only be the pinned profile fallback.
            deadline = time.time() + 8
            new_blocks: list[str] = []
            while time.time() < deadline:
                blocks = [content for content in contents(parts_of(s4_dir), "user") if "Recalled memories relevant" in content]
                if len(blocks) > 1:
                    new_blocks = blocks[1:]
                    break
                time.sleep(0.1)
            if new_blocks:
                for block in new_blocks:
                    assert "project_atlas.md" not in block, block
                log(f"recall failure: injected pinned fallback ({'profile_user.md' in new_blocks[-1]})")
            else:
                log("recall failure: no new block (upstream keeps the cached block)")
            (out / "py-conversation-s4.json").write_text(
                json.dumps(parts_of(s4_dir), ensure_ascii=False, indent=2), encoding="utf-8"
            )
            log("past conversations + recall OK")

            # ── TopK cap: eight matching earlier turns yield six excerpts ─
            for index in range(1, 9):
                crafted_id = f"session_202502{index:02d}_090000_topk{index:04d}"
                crafted = home / "queens" / queen / "sessions" / crafted_id
                crafted.mkdir(parents=True, exist_ok=True)
                event = {
                    "type": "client_input_received",
                    "timestamp": f"2025-02-{index:02d}T09:00:01",
                    "data": {"content": f"我在第 {index} 次帆船训练中完成了绕标。"},
                }
                (crafted / "events.jsonl").write_text(json.dumps(event, ensure_ascii=False) + "\n", encoding="utf-8")
            s6 = run.create()
            log(f"S6 {s6}")
            s6_dir = find_session_dir(home, s6)
            run.chat(s6, "帆船绕标训练记录")
            top_block = wait_user_block(s6_dir, "Excerpts from earlier conversations")
            dated_lines = [
                line
                for line in top_block.splitlines()
                if re.match(r"^- \d{4}-\d{2}-\d{2} \([A-Z][a-z]{2}\), session_", line)
            ]
            assert len(dated_lines) == 6, (len(dated_lines), top_block)
            assert top_block.count("帆船训练") == 6, top_block
            (out / "py-conversation-s6-topk.json").write_text(
                json.dumps(parts_of(s6_dir), ensure_ascii=False, indent=2), encoding="utf-8"
            )
            log("excerpt TopK=6 OK")
        finally:
            stop_serve(serve)

        # ── Phase B: default knobs, first-turn extraction + stop flush ──
        serve, base = start_serve(args.repo_root, home, out / "py-serve-stop.log", {})
        try:
            run = Run(home, base)
            first = run.create()
            log(f"first-turn S {first}")
            run.chat(first, "昨天我去参加了同事的婚礼，随礼 800 元。")
            first_dir = find_session_dir(home, first)
            wait_timeline(first_dir, 1, timeout=25)
            copy_evidence(out, "py-timeline-first-turn.jsonl", first_dir / "timeline.jsonl")

            stop = run.create()
            log(f"stop-flush S {stop}")
            stop_dir = find_session_dir(home, stop)
            event = {
                "type": "client_input_received",
                "timestamp": "2026-11-20T18:00:00",
                "data": {"content": "我上周买了一台相机，花了 5000 元。"},
            }
            with (stop_dir / "events.jsonl").open("a", encoding="utf-8") as handle:
                handle.write(json.dumps(event, ensure_ascii=False) + "\n")
            assert not (stop_dir / "timeline.jsonl").is_file(), "timeline existed before stop"
            run.stop(stop)
            wait_timeline(stop_dir, 1, timeout=25)
            copy_evidence(out, "py-timeline-s2-stop-flush.jsonl", stop_dir / "timeline.jsonl")
            copy_evidence(out, "py-timeline-s2-stop-meta.json", stop_dir / "timeline_meta.json")
            log("first-turn extraction + stop flush OK")
        finally:
            stop_serve(serve)

        log("PYTHON RUN PASS")
        return 0
    finally:
        gateway.terminate()
        try:
            gateway.wait(timeout=5)
        except subprocess.TimeoutExpired:
            gateway.kill()


if __name__ == "__main__":
    sys.exit(main())
