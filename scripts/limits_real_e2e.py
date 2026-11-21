#!/usr/bin/env python3
"""Real-model limits e2e: set a limit, observe the effect, on both sides.

The appearance dimension ``limits`` (concurrency / budget / turns) needs
"set a limit -> observe the truncation / rejection" evidence, not just reader
grep. This driver runs both implementations against the *real* provider
through ``real_llm_proxy.py`` (a pass-through recorder; nothing answers for
the model) and records:

  A. loop.max_tool_result_chars=200 + a long terminal_exec output
     -> both sides spill the result and show the same truncation marker.
  B. llm.aux_max_tokens=17 -> the session-title call (the aux utility path)
     must carry max_tokens=17 on both sides.
  C. HIVE_MAX_ITER=1 -> the queen loop's iteration cap (and whether the env
     read the reference documents is effective).

Opt-in: HIVE_REAL_LLM_E2E=1 and BROADTOKEN_API_KEY in the environment.

Usage:
  HIVE_REAL_LLM_E2E=1 source ~/.hive-go/env   # or export BROADTOKEN_API_KEY
  uv run --no-project python go_hive/scripts/limits_real_e2e.py \
      --out go_hive/docs/e2e-assets/limits
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
from pathlib import Path

WORKTREE = Path(__file__).resolve().parents[1]          # go_hive/
REPO_ROOT = WORKTREE.parent                              # repository root
GO_MODULE = WORKTREE / "go-hive"
GO_BINARY = GO_MODULE / "dist" / "hive"

MODEL = os.environ.get("HIVE_REAL_LLM_MODEL", "deepseek-flash-1017")
API_BASE = os.environ.get("HIVE_REAL_LLM_API_BASE", "https://token.tokenbroad.com/v1")
KEY_ENV = os.environ.get("HIVE_REAL_LLM_KEY_ENV", "BROADTOKEN_API_KEY")

TOOL_RESULT_LIMIT = 200
AUX_MAX_TOKENS = 512
LONG_PROMPT = (
    "Use the terminal_exec tool to run exactly this shell command: "
    "python3 -c \"print('Z'*12000)\". "
    "Then reply with only the number of characters you can see in that tool result."
)
ITER_PROMPT = (
    "Please run two shell commands with terminal_exec, one after the other: "
    "first `echo one`, then `echo two`. When both have run, reply with both outputs."
)


def log(message: str) -> None:
    print(f"[limits-e2e] {message}", flush=True)


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


def wait_health(base: str, proc: subprocess.Popen, log_path: Path, timeout: float = 120) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            tail = log_path.read_text(errors="replace")[-4000:]
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


def find_session_dir(home: Path, session_id: str) -> Path:
    matches = list((home / "queens").glob(f"*/sessions/{session_id}"))
    if not matches:
        raise SystemExit(f"session dir for {session_id} not found under {home}")
    return matches[0]


def tool_parts(session_dir: Path) -> list[dict]:
    return [p for p in parts_of(session_dir) if p.get("role") == "tool"]


def assistant_texts(session_dir: Path) -> list[str]:
    out = []
    for part in parts_of(session_dir):
        if part.get("role") != "assistant":
            continue
        content = part.get("content")
        if isinstance(content, str) and content.strip():
            out.append(content)
    return out


def wait_for(predicate, timeout: float, interval: float = 0.25):
    deadline = time.time() + timeout
    while time.time() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(interval)
    return None


def wait_quiescent(session_dir: Path, stable_s: float, timeout: float) -> int:
    """Return the final part count once the transcript stops growing."""
    deadline = time.time() + timeout
    last_count = -1
    last_change = time.time()
    while time.time() < deadline:
        count = len(parts_of(session_dir))
        if count != last_count:
            last_count = count
            last_change = time.time()
        elif time.time() - last_change >= stable_s:
            return count
        time.sleep(0.25)
    return last_count


def event_types(session_dir: Path) -> list[str]:
    types = []
    events = session_dir / "events.jsonl"
    if not events.is_file():
        return types
    for line in events.read_text(encoding="utf-8").splitlines():
        try:
            types.append(json.loads(line).get("type", ""))
        except ValueError:
            continue
    return types


def start_proxy(scratch: Path, side: str) -> tuple[subprocess.Popen, str, Path]:
    log_path = scratch / f"proxy-{side}.jsonl"
    proc = subprocess.Popen(
        [
            sys.executable,
            str(WORKTREE / "scripts" / "real_llm_proxy.py"),
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
    line = (proc.stdout.readline() if proc.stdout else "").strip()
    match = re.search(r"(http://[^\s]+)", line)
    if not match:
        proc.kill()
        raise SystemExit(f"proxy did not report its URL: {line!r}")
    base = match.group(1)
    log(f"[{side}] proxy {base} -> {API_BASE}")
    return proc, base, log_path


def prepare_home(home: Path, proxy_url: str, max_iter: int | None) -> None:
    home.mkdir(parents=True, exist_ok=True)
    config = {
        "llm": {
            "provider": "openai",
            "model": MODEL,
            "api_base": proxy_url,
            "api_key_env_var": KEY_ENV,
            "aux_max_tokens": AUX_MAX_TOKENS,
        },
        "loop": {"max_tool_result_chars": TOOL_RESULT_LIMIT},
    }
    (home / "configuration.json").write_text(
        json.dumps(config, ensure_ascii=False, indent=1), encoding="utf-8"
    )
    for queen in ("default", "queen_dm"):
        profile = home / "queens" / queen / "profile.yaml"
        profile.parent.mkdir(parents=True, exist_ok=True)
        profile.write_text(f"name: {queen.replace('_', ' ').title()}\n", encoding="utf-8")


def start_serve(side: str, home: Path, log_path: Path, env_extra: dict[str, str]) -> tuple[subprocess.Popen, str]:
    port = free_port()
    base = f"http://127.0.0.1:{port}"
    env = dict(os.environ)
    env["HIVE_HOME"] = str(home)
    env.update(env_extra)
    if side == "go":
        command = [str(GO_BINARY), "serve", "--host", "127.0.0.1", "--port", str(port)]
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
    log(f"[{side}] serve {base} (HIVE_HOME={home}, extra={env_extra})")
    return proc, base


def create_session(base: str) -> str:
    status, body = http_json("POST", base + "/api/sessions", {})
    session_id = str(body.get("session_id") or body.get("id") or "")
    if status not in (200, 201) or not session_id:
        raise SystemExit(f"POST /api/sessions -> {status} {body}")
    return session_id


def chat(base: str, session_id: str, message: str) -> int:
    status, body = http_json("POST", f"{base}/api/sessions/{session_id}/chat", {"message": message}, timeout=600)
    if status != 200:
        raise SystemExit(f"POST chat -> {status} {body}")
    return status


SPILL_RE = re.compile(r"Full result at: (\S+)")


def truncation_evidence(session_dir: Path, out: Path, side: str) -> dict:
    """Grab the long terminal_exec result and describe the truncation marker."""
    part = wait_for(
        lambda: next((p for p in tool_parts(session_dir) if "Preview (truncated):" in str(p.get("content", ""))), None),
        timeout=300,
    )
    evidence: dict = {"found": bool(part)}
    if not part:
        return evidence
    content = str(part.get("content", ""))
    match = SPILL_RE.search(content)
    spill_path = match.group(1) if match else ""
    spill_size = None
    if spill_path and Path(spill_path).is_file():
        spill_size = Path(spill_path).stat().st_size
    lines = content.splitlines()
    (out / f"{side}-truncated-tool-result.txt").write_text(content, encoding="utf-8")
    evidence.update(
        {
            "visible_chars": len(content),
            "first_line": lines[0] if lines else "",
            "has_preview_marker": "Preview (truncated):" in content,
            "has_grep_hint": "For targeted lookup, run `grep -nE" in content,
            "has_sample_warning": "do NOT draw counts, totals, or conclusions from it" in content,
            "spill_path": spill_path,
            "spill_bytes": spill_size,
        }
    )
    return evidence


def title_evidence(home: Path, session_dir: Path, proxy_log: Path, timeout: float = 90) -> dict:
    deadline = time.time() + timeout
    meta: dict = {}
    title_requests: list[dict] = []
    while time.time() < deadline:
        meta = read_json(session_dir / "meta.json", {}) or {}
        title_requests = []
        if proxy_log.is_file():
            for line in proxy_log.read_text(encoding="utf-8").splitlines():
                try:
                    entry = json.loads(line)
                except ValueError:
                    continue
                request = entry.get("request")
                if not isinstance(request, dict):
                    continue
                # Both wires put the namer instruction in a system message:
                # Python passes ``system=``, Go's completer folds it into
                # messages[0]. Search the whole recorded request.
                body = json.dumps(request, ensure_ascii=False)
                if "You name chat conversations" in body:
                    response = entry.get("response") or {}
                    try:
                        named = response["choices"][0]["message"].get("content")
                    except (KeyError, IndexError, TypeError):
                        named = None
                    title_requests.append(
                        {
                            "max_tokens": request.get("max_tokens"),
                            "status": entry.get("status"),
                            "model": request.get("model"),
                            "response_content": named,
                        }
                    )
        if title_requests:
            break
        time.sleep(1.0)
    return {
        "title": meta.get("title"),
        "title_source": meta.get("title_source"),
        "title_requests": title_requests,
    }


def iteration_evidence(session_dir: Path) -> dict:
    types = event_types(session_dir)
    counts = {t: types.count(t) for t in sorted(set(types)) if t}
    texts = assistant_texts(session_dir)
    events_file = session_dir / "events.jsonl"
    error_lines = []
    if events_file.is_file():
        for line in events_file.read_text(encoding="utf-8").splitlines():
            low = line.lower()
            if "max iterations" in low or "inner turns" in low or "max_iterations" in low:
                error_lines.append(line[:400])
    tool_names = []
    for part in tool_parts(session_dir):
        tool_names.append({"chars": len(str(part.get("content", ""))), "preview": str(part.get("content", ""))[:120]})
    return {
        "event_types": counts,
        "tool_parts": tool_names,
        "assistant_texts": [t[:200] for t in texts],
        "max_iteration_error_lines": error_lines[:5],
    }


def run_side(side: str, scratch: Path, out: Path) -> dict:
    result: dict = {"side": side}
    proxy, proxy_url, proxy_log = start_proxy(scratch, side)
    home = scratch / f"home-{side}"
    if home.exists():
        shutil.rmtree(home)
    prepare_home(home, proxy_url, None)
    serve_log = scratch / f"serve-{side}.log"

    # ---- A. loop.max_tool_result_chars=200 + long tool output -------------
    serve, base = start_serve(side, home, serve_log, {})
    try:
        session_id = create_session(base)
        log(f"[{side}] truncation session {session_id}")
        chat(base, session_id, LONG_PROMPT)
        session_dir = find_session_dir(home, session_id)
        result["session"] = session_id
        result["truncation"] = truncation_evidence(session_dir, out, side)
        if not result["truncation"].get("found"):
            log(f"[{side}] tool result not seen; retrying once")
            chat(base, session_id, LONG_PROMPT)
            result["truncation"] = truncation_evidence(session_dir, out, side)
        # Let the turn finish (the title namer fires on turn end) before the
        # title evidence is read.
        wait_for(lambda: len(assistant_texts(session_dir)) >= 2, timeout=240)
        result["title"] = title_evidence(home, session_dir, proxy_log)
    finally:
        stop_process(serve)

    # ---- C. HIVE_MAX_ITER=1 ----------------------------------------------
    serve, base = start_serve(side, home, scratch / f"serve-{side}-iter.log", {"HIVE_MAX_ITER": "1"})
    try:
        session_id = create_session(base)
        log(f"[{side}] iteration-cap session {session_id}")
        chat(base, session_id, ITER_PROMPT)
        session_dir = find_session_dir(home, session_id)
        wait_for(lambda: tool_parts(session_dir), timeout=300)
        wait_quiescent(session_dir, 12.0, timeout=180)
        result["max_iter"] = iteration_evidence(session_dir)
        result["max_iter"]["session"] = session_id
    finally:
        stop_process(serve)
        stop_process(proxy)
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", default=str(WORKTREE / "docs" / "e2e-assets" / "limits"))
    parser.add_argument("--sides", default="go,python")
    args = parser.parse_args()

    if os.environ.get("HIVE_REAL_LLM_E2E") != "1":
        raise SystemExit("opt-in required: HIVE_REAL_LLM_E2E=1 (real provider calls)")
    if not os.environ.get(KEY_ENV):
        raise SystemExit(f"{KEY_ENV} must be set (e.g. source ~/.hive-go/env)")
    if not GO_BINARY.is_file():
        raise SystemExit(f"build the Go binary first: {GO_BINARY}")

    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    scratch = Path(tempfile.mkdtemp(prefix="limits-e2e-"))
    log(f"scratch {scratch}")

    results = []
    for side in [s.strip() for s in args.sides.split(",") if s.strip()]:
        results.append(run_side(side, scratch, out))

    # Merge with whatever earlier side-runs already wrote, so `--sides go` and
    # `--sides python` build one complete two-sided record.
    record_path = out / "limits-real-e2e.json"
    previous = read_json(record_path, {}) or {}
    merged = {entry.get("side"): entry for entry in (previous.get("results") or []) if entry.get("side")}
    for entry in results:
        merged[entry["side"]] = entry

    summary = {
        "model": MODEL,
        "api_base": API_BASE,
        "limit": TOOL_RESULT_LIMIT,
        "aux_max_tokens": AUX_MAX_TOKENS,
        "results": [merged[key] for key in sorted(merged)],
    }
    record_path.write_text(
        json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
