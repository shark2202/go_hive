#!/usr/bin/env python3
"""Real vision-fallback caption E2E.

Drives one implementation (the Go port or the Python reference) against the
real upstream, with a text-only main model and an image-bearing tool result:

  user turn  ->  main model calls attach_file(<png>)
             ->  the loop sees image content and a text-only catalog model
             ->  vision_fallback captions the image (real vision model)
             ->  the caption is spliced into the tool result, images dropped
             ->  main model answers from the caption

Both model calls are real: the main model is a catalogued text-only model
(deepseek-v4-pro) and the fallback is a real multimodal model
(deepseek-flash-1017). Every request/response passes through the recording
proxy (e2e/cmd/recordproxy), so the evidence file carries the wire bodies.

Usage (one implementation per call, then compare):

  python3 go_hive/scripts/e2e_vision_caption.py --impl go \
      --go-module <worktree>/go_hive/go-hive --out docs/e2e-assets/vision-caption

  python3 go_hive/scripts/e2e_vision_caption.py --impl python \
      --repo-root <worktree>/core --out docs/e2e-assets/vision-caption

  python3 go_hive/scripts/e2e_vision_caption.py --compare \
      --out docs/e2e-assets/vision-caption

The real upstream and key come from VC_UPSTREAM / BROADTOKEN_API_KEY.
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
import time
import urllib.error
import urllib.request
from pathlib import Path

MAIN_PROVIDER = "deepseek"
MAIN_MODEL = "deepseek-v4-pro"          # catalogued supports_vision=false
VISION_PROVIDER = "openai"
VISION_MODEL = "deepseek-flash-1017"    # real multimodal upstream
PROXY_KEY = "sk-vc-proxy-placeholder"   # the proxy injects the real key
CAPTION_MARKER = "[vision-fallback caption]"
EXHAUSTED_MARKER = "[image stripped — vision fallback exhausted]"
COORD_RE = re.compile(r"\(\s*(\d?\.\d+|[01])\s*,\s*(\d?\.\d+|[01])\s*\)")


def log(message: str) -> None:
    print(f"[vision-caption] {message}", flush=True)


def fail(message: str) -> None:
    print(f"[vision-caption] FAIL: {message}", flush=True)


def require(condition: bool, message: str) -> bool:
    if condition:
        print(f"  ok   {message}", flush=True)
    else:
        fail(message)
    return condition


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def http_json(method: str, url: str, payload: dict | None = None, timeout: float = 120.0) -> tuple[int, dict]:
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
            body = {"raw": raw.decode("utf-8", "replace")[:4000]}
    return status, body


def wait_until(label: str, timeout: float, predicate) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            if predicate():
                return True
        except Exception:  # noqa: BLE001 - polling probe
            pass
        time.sleep(0.25)
    log(f"timed out waiting for {label}")
    return False


def wait_http(url: str, timeout: float = 60.0) -> bool:
    def healthy() -> bool:
        try:
            status, _ = http_json("GET", url, timeout=2)
            return status == 200
        except OSError:
            return False

    return wait_until(url, timeout, healthy)


# ---- fixture ----------------------------------------------------------------


def fixture_paths(out: Path) -> tuple[Path, Path]:
    fixture_dir = out / "fixture"
    return fixture_dir / "screenshot.png", fixture_dir / "screenshot-ground-truth.json"


def ensure_fixture(out: Path) -> tuple[Path, dict]:
    png, truth = fixture_paths(out)
    if png.exists() and truth.exists():
        return png, json.loads(truth.read_text())
    generator = Path(__file__).resolve().parent / "vision_caption_fixture.py"
    subprocess.run([sys.executable, str(generator), "--out", str(png.parent)], check=True)
    return png, json.loads(truth.read_text())


# ---- processes --------------------------------------------------------------


class Proc:
    def __init__(self, args: list[str], env: dict, cwd: str, log_path: Path):
        self.log_path = log_path
        self.log = log_path.open("w")
        log(f"start: {' '.join(args)}")
        self.proc = subprocess.Popen(
            args, cwd=cwd, env=env, stdout=self.log, stderr=subprocess.STDOUT, start_new_session=True
        )

    def stop(self) -> None:
        if self.proc.poll() is not None:
            self.log.close()
            return
        try:
            os.killpg(os.getpgid(self.proc.pid), signal.SIGTERM)
        except ProcessLookupError:
            return
        try:
            self.proc.wait(timeout=20)
        except subprocess.TimeoutExpired:
            self.proc.kill()
        self.log.close()

    def tail(self, limit: int = 4000) -> str:
        try:
            return self.log_path.read_text(errors="replace")[-limit:]
        except OSError:
            return ""


# ---- serve (both implementations expose the same REST surface) --------------


class Serve:
    def __init__(self, impl: str, home: Path, cwd: str, binary: str | None, log_path: Path):
        self.impl = impl
        self.home = home
        self.port = free_port()
        self.base = f"http://127.0.0.1:{self.port}"
        if impl == "go":
            args = [binary or "hive", "serve", "--host", "127.0.0.1", "--port", str(self.port)]
        else:
            args = ["uv", "run", "--offline", "hive", "serve", "--host", "127.0.0.1", "--port", str(self.port)]
        env = dict(os.environ)
        env["HIVE_HOME"] = str(home)
        env["HIVE_CREDENTIAL_KEY"] = "vc-e2e-credential-key"
        # Both providers resolve their key from the section's literal api_key;
        # keep provider env vars out of the picture so nothing else is used.
        env.pop("OPENAI_API_KEY", None)
        env.pop("DEEPSEEK_API_KEY", None)
        env.pop("HIVE_API_KEY", None)
        self.proc = Proc(args, env, cwd, log_path)

    def create(self) -> str:
        status, body = http_json("POST", self.base + "/api/sessions", {"queen_name": "qa"})
        session_id = str(body.get("session_id") or body.get("id") or "")
        if status not in (200, 201) or not session_id:
            raise SystemExit(f"POST /api/sessions -> {status} {body}")
        return session_id

    def chat(self, session_id: str, message: str) -> tuple[int, dict]:
        return http_json("POST", f"{self.base}/api/sessions/{session_id}/chat", {"message": message}, timeout=300)

    def snapshot(self, session_id: str) -> dict:
        _, body = http_json("GET", f"{self.base}/api/sessions/{session_id}/snapshot", timeout=30)
        return body

    def stop(self) -> None:
        self.proc.stop()


# ---- evidence ---------------------------------------------------------------


def read_proxy_log(path: Path) -> list[dict]:
    records = []
    if not path.exists():
        return records
    for line in path.read_text(errors="replace").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            records.append(json.loads(line))
        except ValueError:
            continue
    return records


def body_json(record: dict) -> dict:
    try:
        return json.loads(record.get("body") or "")
    except ValueError:
        return {}


def record_has_image(record: dict) -> bool:
    return "data:image/" in (record.get("body") or "")


def messages_of(record: dict) -> list[dict]:
    return list(body_json(record).get("messages") or [])


def message_text(message: dict) -> str:
    content = message.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "\n".join(
            str(part.get("text") or "") for part in content if isinstance(part, dict) and part.get("type") == "text"
        )
    return ""


def message_has_image(message: dict) -> bool:
    content = message.get("content")
    if not isinstance(content, list):
        return False
    return any(isinstance(part, dict) and part.get("type") in ("image_url", "image") for part in content)


def find_session_dir(home: Path, session_id: str) -> Path | None:
    import glob

    patterns = [
        str(home / "queens" / "*" / "sessions" / session_id),
        str(home / "colonies" / "*" / "queens" / "*" / "sessions" / session_id),
    ]
    for pattern in patterns:
        for candidate in glob.glob(pattern):
            return Path(candidate)
    return None


def read_stored_messages(session_dir: Path | None) -> list[dict]:
    """Read the persisted conversation parts, newest format first."""
    if session_dir is None:
        return []
    parts_dir = session_dir / "conversations" / "parts"
    messages = []
    if parts_dir.exists():
        for path in sorted(parts_dir.glob("*.json")):
            try:
                messages.append(json.loads(path.read_text()))
            except ValueError:
                continue
        if messages:
            return messages
    for name in ("conversation.json", "messages.json"):
        candidate = session_dir / "conversations" / name
        if candidate.exists():
            try:
                payload = json.loads(candidate.read_text())
            except ValueError:
                continue
            if isinstance(payload, dict):
                return list(payload.get("messages") or [])
            if isinstance(payload, list):
                return payload
    return []


def message_role(message: dict) -> str:
    return str(message.get("role") or "")


def elide_images(messages: list[dict]) -> list[dict]:
    """Shrink image payloads so the evidence file stays readable."""
    out = []
    for message in messages:
        copy = json.loads(json.dumps(message))
        if isinstance(copy.get("image_content"), list):
            for block in copy["image_content"]:
                if isinstance(block, dict) and block.get("type") == "image_url":
                    block["image_url"] = {"url": "<elided " + str(len(str(block.get("image_url")))) + " chars>"}
        out.append(copy)
    return out


def system_prompt_from_python(repo_root: Path) -> str:
    """Extract _VISION_SUBAGENT_SYSTEM verbatim from the reference source."""
    import ast

    source = repo_root / "framework" / "agent_loop" / "internals" / "vision_fallback.py"
    tree = ast.parse(source.read_text(encoding="utf-8"))
    for node in tree.body:
        if isinstance(node, ast.Assign) and getattr(node.targets[0], "id", "") == "_VISION_SUBAGENT_SYSTEM":
            return ast.literal_eval(node.value)
    raise SystemExit(f"_VISION_SUBAGENT_SYSTEM not found in {source}")


def coords_in(text: str) -> list[tuple[float, float]]:
    return [(float(m.group(1)), float(m.group(2))) for m in COORD_RE.finditer(text or "")]


def submit_coordinate(caption: str) -> tuple[float, float] | None:
    """The coordinate the caption attaches to the Submit element.

    Captions put the coordinate right after the element label — `"Submit"
    button (0.76, 0.54)` — so each "submit" mention is paired with the nearest
    following coordinate and the tightest pair wins. That keeps a later
    sentence ("Submit is disabled until you type into the search field
    (0.44, 0.42)") from stealing the input's coordinate.
    """
    best: tuple[int, tuple[float, float]] | None = None
    for label in re.finditer("submit", (caption or "").lower()):
        tail = caption[label.end(): label.end() + 120]
        match = COORD_RE.search(tail)
        if not match:
            continue
        candidate = (match.start(), (float(match.group(1)), float(match.group(2))))
        if best is None or candidate[0] <= best[0]:
            best = candidate
    return best[1] if best else None


def caption_from_last_main_request(records: list[dict]) -> str:
    """The caption text the last main-model request carried, if any."""
    for record in reversed(records):
        if CAPTION_MARKER not in (record.get("body") or ""):
            continue
        for message in messages_of(record):
            text = message_text(message)
            index = text.find(CAPTION_MARKER)
            if index >= 0:
                return text[index:]
    return ""


def build_evidence(impl: str, records: list[dict], messages: list[dict], ground: dict, expected_system: str) -> dict:
    vision_records = [r for r in records if r.get("model") == VISION_MODEL]
    main_records = [r for r in records if r.get("model") != VISION_MODEL]
    caption_record = next((r for r in vision_records if record_has_image(r)), None)
    caption_request = body_json(caption_record) if caption_record else {}
    request_messages = list(caption_request.get("messages") or [])
    request_system = next((m for m in request_messages if m.get("role") == "system"), {})
    request_user = next((m for m in request_messages if m.get("role") == "user"), {})
    caption_text = str(caption_record.get("content") or "") if caption_record else ""
    injected = caption_from_last_main_request(records)

    tool_message = next((m for m in messages if message_role(m) == "tool"), None)
    stored_text = ""
    if tool_message is not None:
        stored_text = message_text(tool_message)

    coords = coords_in(caption_text)
    submit = submit_coordinate(caption_text)
    truth = (ground.get("measured") or {}).get("submit")

    checks = {
        # (a) the loop detected the image in the tool result and handed it to
        # the captioner (the vision request carries the PNG data URI).
        "image_detected": bool(caption_record and record_has_image(caption_record)),
        # (b) the caption came from the configured vision_fallback slot.
        "caption_slot_called": bool(
            caption_record
            and caption_request.get("model") == VISION_MODEL
            and str(request_system.get("content") or "") == expected_system
        ),
        "caption_has_coordinates": bool(coords),
        # (c) the caption is spliced into the tool result the next main-model
        # request carries, in the upstream marker format.
        "caption_injected": CAPTION_MARKER in injected,
        "images_dropped_from_main_requests": all(not message_has_image(m) for r in main_records for m in messages_of(r)),
        "stored_tool_result_has_caption": CAPTION_MARKER in stored_text,
        "stored_tool_result_has_no_image": "data:image/" not in json.dumps(tool_message or {}),
        "exhausted_marker_absent": EXHAUSTED_MARKER not in stored_text,
    }
    # Sanity bound, not a precision claim: the subagent eyeballs coordinates to
    # two decimals, so the caption lands near the element, not on it (observed
    # error on this fixture: <=0.06). The bound only has to be tight enough
    # that a caption not grounded in this screenshot could not pass it.
    if submit and truth:
        delta = (round(abs(submit[0] - truth[0]), 4), round(abs(submit[1] - truth[1]), 4))
        checks["coordinate_matches_ground_truth"] = delta[0] <= 0.10 and delta[1] <= 0.10

    return {
        "impl": impl,
        "main_model": MAIN_MODEL,
        "vision_model": VISION_MODEL,
        "checks": checks,
        "passed": all(checks.values()),
        "caption": {
            "text": caption_text,
            "coordinates": [list(pair) for pair in coords],
            "submit_coordinate": list(submit) if submit else None,
            "ground_truth_submit": truth,
            "submit_delta": list(delta) if submit and truth else None,
            "request_system_prompt_sha256": _sha(request_system.get("content") or ""),
            "intent": message_text(request_user),
            "request_image_blocks": sum(1 for part in (request_user.get("content") or []) if isinstance(part, dict) and part.get("type") == "image_url"),
        },
        "injected_tool_result": injected,
        "stored_tool_result": stored_text[-4000:],
        "requests": [
            {
                "seq": record.get("seq"),
                "model": record.get("model"),
                "has_image": record_has_image(record),
                "tool_names": record.get("tools") or [],
                "tool_calls": record.get("tool_calls") or [],
                "finish_reason": record.get("finish_reason"),
                "status": record.get("status"),
                "elapsed_hint": record.get("time"),
            }
            for record in records
        ],
    }


def _sha(text: str) -> str:
    import hashlib

    return hashlib.sha256(text.encode()).hexdigest()


# ---- run one implementation -------------------------------------------------


def run_impl(args) -> int:
    out = Path(args.out).resolve()
    impl = args.impl
    workspace = out / impl
    if workspace.exists():
        shutil.rmtree(workspace)
    (workspace / "home" / "queens" / "qa").mkdir(parents=True)
    (workspace / "home" / "queens" / "qa" / "profile.yaml").write_text("name: QA\ntitle: Head of QA\n")

    png, ground = ensure_fixture(out)
    fixture_copy = workspace / png.name
    shutil.copy2(png, fixture_copy)

    upstream = args.upstream or os.environ.get("VC_UPSTREAM", "https://token.tokenbroad.com/v1")
    if not os.environ.get("BROADTOKEN_API_KEY"):
        raise SystemExit("BROADTOKEN_API_KEY is not set; the real upstream needs it")

    # 1. recording proxy in front of the real upstream
    proxy_port = free_port()
    proxy_log = workspace / "proxy.jsonl"
    proxy = Proc(
        [
            args.recordproxy_binary,
            "-addr", f"127.0.0.1:{proxy_port}",
            "-upstream", upstream,
            "-log", str(proxy_log),
            "-key-env", "BROADTOKEN_API_KEY",
            "-body-head", "65536",
            "-body-tail", "262144",
        ],
        dict(os.environ),
        str(workspace),
        workspace / "recordproxy.log",
    )
    proxy_url = f"http://127.0.0.1:{proxy_port}/v1"
    if not wait_until("recordproxy listening", 30, lambda: "LISTENING" in proxy.tail()):
        print(proxy.tail())
        proxy.stop()
        return 1

    # 2. configuration.json: text-only main model + real vision fallback
    config = {
        "llm": {"provider": MAIN_PROVIDER, "model": MAIN_MODEL, "api_base": proxy_url, "api_key": PROXY_KEY},
        "vision_fallback": {"provider": VISION_PROVIDER, "model": VISION_MODEL, "api_base": proxy_url, "api_key": PROXY_KEY},
    }
    (workspace / "home" / "configuration.json").write_text(json.dumps(config, indent=2) + "\n")

    # 3. serve
    cwd = str(Path(args.go_module).resolve()) if impl == "go" else str(Path(args.repo_root).resolve())
    serve = Serve(impl, workspace / "home", cwd, args.hive_binary, workspace / "serve.log")
    session_id = ""
    evidence: dict = {"impl": impl}
    try:
        if not wait_http(serve.base + "/api/health", timeout=90):
            print(serve.proc.tail(6000))
            raise SystemExit("serve did not become healthy")
        session_id = serve.create()
        log(f"session {session_id}")

        message = (
            f"Call the attach_file tool with paths ['{fixture_copy}'] to attach that screenshot. "
            "Then tell me, in one sentence, where the Submit button is on that screen."
        )
        status, body = serve.chat(session_id, message)
        if status != 200:
            raise SystemExit(f"chat -> {status} {body}")

        def captured() -> bool:
            records = read_proxy_log(proxy_log)
            has_caption_call = any(r.get("model") == VISION_MODEL and record_has_image(r) for r in records)
            has_followup = any(CAPTION_MARKER in (r.get("body") or "") for r in records if r.get("model") != VISION_MODEL)
            return has_caption_call and has_followup

        captured_ok = wait_until("caption request + follow-up main request", args.timeout, captured)
        # Give the turn a moment to persist its final assistant message.
        wait_until("session idle", 30, lambda: (serve.snapshot(session_id).get("activity") or "idle") in ("", "idle", "awaiting_user"))

        records = read_proxy_log(proxy_log)
        session_dir = find_session_dir(workspace / "home", session_id)
        messages = read_stored_messages(session_dir)
        expected_system = system_prompt_from_python(Path(args.reference_root).resolve())
        evidence = build_evidence(impl, records, messages, ground, expected_system)
        evidence["session_id"] = session_id
        evidence["session_dir"] = str(session_dir) if session_dir else None
        evidence["captured_both_requests"] = captured_ok
        evidence["proxy_url"] = proxy_url
        evidence["config"] = config
        evidence["fixture"] = {
            "path": str(png),
            "sha256": _sha_bytes(png.read_bytes()),
            "ground_truth": ground,
        }
        evidence["stored_messages"] = elide_images(messages)
        evidence["passed"] = evidence["passed"] and captured_ok
    finally:
        (workspace / "evidence.json").write_text(json.dumps(evidence, indent=2) + "\n")
        serve.stop()
        proxy.stop()

    print(json.dumps({k: evidence.get(k) for k in ("impl", "checks", "passed")}, indent=2))
    if evidence.get("caption", {}).get("text"):
        print("--- caption ---")
        print(evidence["caption"]["text"][:1500])
    return 0 if evidence.get("passed") else 1


def _sha_bytes(data: bytes) -> str:
    import hashlib

    return hashlib.sha256(data).hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--impl", choices=("go", "python"))
    parser.add_argument("--out", default="docs/e2e-assets/vision-caption")
    parser.add_argument("--go-module", default="")
    parser.add_argument("--repo-root", default="core")
    parser.add_argument("--reference-root", default="core", help="Python reference tree (for the prompt hash)")
    parser.add_argument("--upstream", default="")
    parser.add_argument("--hive-binary", default="")
    parser.add_argument("--recordproxy-binary", default="/tmp/vc-recordproxy")
    parser.add_argument("--timeout", type=float, default=300)
    parser.add_argument("--compare", action="store_true")
    parser.add_argument("--vectors", action="store_true", help="byte-compare both implementations on fixed vectors")
    args = parser.parse_args()

    if args.compare:
        return compare(Path(args.out).resolve())
    if args.vectors:
        return run_vectors(args)
    if not args.impl:
        parser.error("--impl, --compare or --vectors is required")
    return run_impl(args)


def run_vectors(args) -> int:
    """Byte-compare both implementations' real functions on fixed vectors."""
    out = Path(args.out).resolve()
    vectors_in = out / "vectors.json"
    if not vectors_in.exists():
        fail(f"missing {vectors_in}")
        return 1
    go_out = out / "go-vectors.json"
    py_out = out / "python-vectors.json"
    repo = Path(args.go_module).resolve().parent.parent  # go_hive/go-hive -> repo root
    core = Path(args.repo_root).resolve()
    if not core.exists():
        core = repo / "core"

    env = dict(os.environ)
    env["VC_VECTORS_IN"] = str(vectors_in)
    env["VC_VECTORS_OUT"] = str(go_out)
    go = subprocess.run(
        ["go", "test", "-tags", "e2e", "-count=1", "-run", "TestVisionCaptionParityVectors", "./internal/agentloop"],
        cwd=str(Path(args.go_module).resolve()),
        env=env,
        capture_output=True,
        text=True,
        timeout=600,
    )
    log(go.stdout.strip()[-1200:] or "(go vectors produced no output)")
    if go.returncode != 0:
        print(go.stderr[-4000:])
        return 1

    python_script = Path(__file__).resolve().parent / "vision_caption_python_vectors.py"
    py = subprocess.run(
        ["uv", "run", "--offline", "python", str(python_script), "--vectors", str(vectors_in), "--out", str(py_out),
         "--agent-loop", str(core / "framework" / "agent_loop" / "agent_loop.py")],
        cwd=str(core),
        capture_output=True,
        text=True,
        timeout=600,
    )
    log(py.stdout.strip()[-1200:] or "(python vectors produced no output)")
    if py.returncode != 0:
        print(py.stderr[-4000:])
        return 1

    go_vectors = {item["id"]: item for item in json.loads(go_out.read_text())["vectors"]}
    py_vectors = {item["id"]: item for item in json.loads(py_out.read_text())["vectors"]}
    report: dict = {"vectors": [], "passed": True}
    for vector_id in go_vectors:
        go_item, py_item = go_vectors[vector_id], py_vectors.get(vector_id, {})
        checks = {
            "intent": go_item.get("intent") == py_item.get("intent"),
            "stored_content": go_item.get("stored_content") == py_item.get("stored_content"),
        }
        detail = {"id": vector_id, "checks": checks}
        if not all(checks.values()):
            detail["go_intent"] = go_item.get("intent")
            detail["python_intent"] = py_item.get("intent")
            detail["go_stored"] = go_item.get("stored_content")
            detail["python_stored"] = py_item.get("stored_content")
            report["passed"] = False
        report["vectors"].append(detail)

    missing = sorted(set(py_vectors) - set(go_vectors))
    if missing:
        report["passed"] = False
        report["python_only"] = missing
    (out / "vectors-compare.json").write_text(json.dumps(report, indent=2) + "\n")
    passed_count = sum(1 for item in report["vectors"] if all(item["checks"].values()))
    print(f"vectors: {passed_count}/{len(report['vectors'])} byte-identical")
    for item in report["vectors"]:
        if not all(item["checks"].values()):
            print(json.dumps(item, indent=2)[:3000])
    return 0 if report["passed"] else 1


def compare(out: Path) -> int:
    """Assert the two evidence files agree on every observable of the chain."""
    sides = {}
    for impl in ("go", "python"):
        path = out / impl / "evidence.json"
        if not path.exists():
            fail(f"missing {path}; run --impl {impl} first")
            return 1
        sides[impl] = json.loads(path.read_text())

    go, py = sides["go"], sides["python"]
    checks: dict[str, bool] = {}
    for impl, side in sides.items():
        checks[f"{impl}_passed"] = bool(side.get("passed"))
    checks["same_marker"] = CAPTION_MARKER in go.get("injected_tool_result", "") and CAPTION_MARKER in py.get("injected_tool_result", "")
    checks["same_system_prompt_sha"] = (
        go.get("caption", {}).get("request_system_prompt_sha256")
        == py.get("caption", {}).get("request_system_prompt_sha256")
        and bool(go.get("caption", {}).get("request_system_prompt_sha256"))
    )
    # The injection boundary — "\n\n[vision-fallback caption]\n" — is the bytes
    # the loop owns. The tool result JSON before it belongs to attach_file and
    # is compared separately (see the doc's observations).
    checks["same_injection_boundary"] = _injection_boundary(go) == _injection_boundary(py) and bool(_injection_boundary(go))
    checks["same_intent_head"] = _intent_head(go) == _intent_head(py) and bool(_intent_head(go))
    checks["same_vision_model"] = go.get("vision_model") == py.get("vision_model")
    checks["same_main_model"] = go.get("main_model") == py.get("main_model")
    checks["both_have_coordinates"] = bool(go.get("caption", {}).get("coordinates")) and bool(py.get("caption", {}).get("coordinates"))

    report = {
        "checks": checks,
        "passed": all(checks.values()),
        "go_caption": go.get("caption"),
        "python_caption": py.get("caption"),
        "go_injection_boundary": _injection_boundary(go),
        "python_injection_boundary": _injection_boundary(py),
        "go_intent_head": _intent_head(go),
        "python_intent_head": _intent_head(py),
        "go_reasoning": _reasoning(go),
        "python_reasoning": _reasoning(py),
    }
    (out / "compare.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2)[:4000])
    return 0 if report["passed"] else 1


def _injection_boundary(side: dict) -> str:
    """The bytes the loop owns around the caption: the two newlines before the
    marker, the marker itself and the newline that opens the caption text."""
    text = side.get("stored_tool_result") or ""
    index = text.find(CAPTION_MARKER)
    if index < 0:
        return ""
    return text[max(0, index - 2): index + len(CAPTION_MARKER) + 1]


def _intent_head(side: dict) -> str:
    """The intent's fixed head: the tool descriptor plus the reasoning label."""
    intent = side.get("caption", {}).get("intent") or ""
    marker = "Reasoning before call:\n"
    index = intent.find(marker)
    if index < 0:
        return ""
    head = intent[: index + len(marker)]
    return re.sub(r"/\S+\.png", "<path>", head)


def _reasoning(side: dict) -> str:
    """The agent reasoning the intent carried (run-dependent, reported only)."""
    intent = side.get("caption", {}).get("intent") or ""
    marker = "Reasoning before call:\n"
    index = intent.find(marker)
    if index < 0:
        return ""
    return intent[index + len(marker):][:200]


if __name__ == "__main__":
    raise SystemExit(main())
