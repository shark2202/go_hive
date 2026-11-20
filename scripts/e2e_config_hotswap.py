#!/usr/bin/env python3
"""Live E2E: model-slot PUT hot switching against a real `hive serve`.

Starts two local OpenAI-compatible fake gateways ("old" and "new"), points a
temporary HIVE_HOME at the old one, starts the real binary, and drives the
three model slots through their HTTP write routes:

  queen_llm.<id>   PUT /api/queen/qa/llm         -> next chat turn hits the new
                                                   base + model (upstream
                                                   provider.reconfigure path)
  worker_llm       PUT /api/config/llm-sections  -> the next session's colony
                                                   worker hits the new base +
                                                   model (upstream: new-session
                                                   effective, no running swap)
  vision_fallback  PUT /api/config/llm-sections  -> the next tool image caption
                                                   hits the new base + model
                                                   (upstream reads the slot per
                                                   caption call)

Every gateway request is appended to a JSONL log; assertions are made against
the recorded model/api_base, not against the model's self-report. The first
session keeps running across the queen swap (no crash, turn still answers).

Usage:
  uv run --no-project python go_hive/scripts/e2e_config_hotswap.py \
      [--binary go_hive/go-hive/dist/hive] [--keep]

Exit code 0 = every assertion passed; 1 = a failure (details printed).
"""

import argparse
import json
import os
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

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

PASS = 0
FAIL = 0


def ok(message):
    global PASS
    PASS += 1
    print(f"  PASS  {message}")


def fail(message):
    global FAIL
    FAIL += 1
    print(f"  FAIL  {message}")


def require(condition, message):
    if condition:
        ok(message)
    else:
        fail(message)
    return condition


def free_port():
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port


def wait_http(url, timeout=30.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1) as response:
                if response.status == 200:
                    return True
        except Exception:
            pass
        time.sleep(0.1)
    return False


def http_json(method, url, body=None, timeout=30.0):
    data = None
    headers = {}
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read().decode()
            return response.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as error:
        raw = error.read().decode()
        try:
            return error.code, json.loads(raw)
        except Exception:
            return error.code, {"raw": raw}


def assistant_turn(message, tool_call=None):
    """An OpenAI chat-completion body for one assistant reply."""
    body = {
        "id": "chatcmpl-e2e",
        "object": "chat.completion",
        "model": "fake",
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": message},
                "finish_reason": "stop",
            }
        ],
        "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
    }
    if tool_call is not None:
        name, arguments = tool_call
        body["choices"][0]["message"]["content"] = None
        body["choices"][0]["message"]["tool_calls"] = [
            {
                "id": "call_e2e_1",
                "type": "function",
                "function": {"name": name, "arguments": json.dumps(arguments)},
            }
        ]
        body["choices"][0]["finish_reason"] = "tool_calls"
    return body


class FakeGateway:
    """Records every POSTed request body; answers from a per-port script."""

    def __init__(self, name, port, log_path, responder):
        self.name = name
        self.port = port
        self.log_path = log_path
        self.responder = responder
        self.lock = threading.Lock()
        gateway = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format, *args):  # silence
                pass

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                raw = self.rfile.read(length)
                try:
                    body = json.loads(raw.decode())
                except Exception:
                    body = {"_raw": raw.decode(errors="replace")}
                with gateway.lock:
                    with open(gateway.log_path, "a") as handle:
                        handle.write(json.dumps({"path": self.path, "body": body}) + "\n")
                payload = gateway.responder(body)
                encoded = json.dumps(payload).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)

            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"data":[]}')

        self.server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def start(self):
        self.thread.start()

    def stop(self):
        self.server.shutdown()
        self.server.server_close()

    def requests(self):
        if not os.path.exists(self.log_path):
            return []
        with self.lock:
            with open(self.log_path) as handle:
                return [json.loads(line) for line in handle if line.strip()]


def last_user_text(body):
    text = ""
    for message in body.get("messages", []):
        if message.get("role") == "user":
            content = message.get("content")
            if isinstance(content, str):
                text = content
            elif isinstance(content, list):
                text = " ".join(
                    block.get("text", "")
                    for block in content
                    if isinstance(block, dict) and block.get("type") == "text"
                )
    return text


def has_tool_message(body):
    return any(message.get("role") == "tool" for message in body.get("messages", []))


def image_parts(body):
    parts = []
    for message in body.get("messages", []):
        content = message.get("content")
        if isinstance(content, list):
            for block in content:
                if isinstance(block, dict) and block.get("type") == "image_url":
                    parts.append(block)
    return parts


def wait_for(predicate, timeout, interval=0.1):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if predicate():
            return True
        time.sleep(interval)
    return False


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default=os.path.join(ROOT, "go-hive", "dist", "hive"))
    parser.add_argument("--keep", action="store_true", help="keep the temp HIVE_HOME")
    args = parser.parse_args()

    binary = os.path.abspath(args.binary)
    if not os.path.exists(binary):
        print(f"binary not found: {binary}")
        return 1

    workspace = tempfile.mkdtemp(prefix="hive-hotswap-e2e-")
    home = os.path.join(workspace, "home")
    os.makedirs(home)
    image_path = os.path.join(workspace, "pixel.png")
    # 1x1 transparent PNG.
    with open(image_path, "wb") as handle:
        handle.write(
            bytes.fromhex(
                "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c4"
                "890000000d4944415408d763f8ffff3f0005fe02fea735f6f70000000049454e44ae426082"
            )
        )

    old_port, new_port = free_port(), free_port()
    serve_port = free_port()
    old_log = os.path.join(workspace, "gateway-old.jsonl")
    new_log = os.path.join(workspace, "gateway-new.jsonl")

    def old_responder(body):
        # The text-only main model asks for the image; after the tool result
        # arrived it answers normally.
        if "look at the image" in last_user_text(body) and not has_tool_message(body):
            return assistant_turn(None, ("attach_file", {"paths": [image_path]}))
        return assistant_turn("old gateway reply")

    def new_responder(body):
        # The colony overseer asked to spawn a worker.
        if "spawn a worker" in last_user_text(body) and not has_tool_message(body):
            return assistant_turn(None, ("run_worker", {"tasks": [{"task": "say hi", "goal": "Say hi"}]}))
        return assistant_turn("new gateway reply")

    old_gateway = FakeGateway("old", old_port, old_log, old_responder)
    new_gateway = FakeGateway("new", new_port, new_log, new_responder)
    old_gateway.start()
    new_gateway.start()

    old_base = f"http://127.0.0.1:{old_port}/v1"
    new_base = f"http://127.0.0.1:{new_port}/v1"
    config = {
        # A catalogued text-only main model: the vision_fallback gate keys off
        # the model's supports_vision flag, and the fake base serves the calls.
        "llm": {
            "provider": "groq",
            "model": "llama-3.1-8b-instant",
            "api_base": old_base,
            "api_key": "sk-e2e",
        },
        "queen_llm": {
            "qa": {
                "provider": "openai",
                "model": "queen-old",
                "api_base": old_base,
                "api_key": "sk-e2e",
            }
        },
    }
    with open(os.path.join(home, "configuration.json"), "w") as handle:
        json.dump(config, handle, indent=2)

    # Queen profiles must exist for POST /api/sessions to accept the names.
    for queen_id, name in (("qa", "QA"), ("vision", "Vision")):
        queen_dir = os.path.join(home, "queens", queen_id)
        os.makedirs(queen_dir, exist_ok=True)
        with open(os.path.join(queen_dir, "profile.yaml"), "w") as handle:
            handle.write(f"name: {name}\ntitle: Head of {name}\n")

    env = dict(os.environ)
    env["HIVE_HOME"] = home
    env["HIVE_CREDENTIAL_KEY"] = "e2e-credential-key"
    # The main-slot session builder resolves the key from the provider env var
    # (the fake gateway ignores its value; it must merely be non-empty).
    env["GROQ_API_KEY"] = "sk-e2e"
    env.pop("OPENAI_API_KEY", None)
    env.pop("HIVE_API_KEY", None)

    serve_log = open(os.path.join(workspace, "serve.log"), "w")
    process = subprocess.Popen(
        [binary, "serve", "--port", str(serve_port)],
        env=env,
        stdout=serve_log,
        stderr=subprocess.STDOUT,
        cwd=workspace,
    )
    base_url = f"http://127.0.0.1:{serve_port}"

    def cleanup():
        try:
            process.send_signal(signal.SIGTERM)
            process.wait(timeout=10)
        except Exception:
            process.kill()
        old_gateway.stop()
        new_gateway.stop()
        serve_log.close()
        if not args.keep:
            shutil.rmtree(workspace, ignore_errors=True)
        else:
            print(f"kept workspace: {workspace}")

    try:
        if not wait_http(base_url + "/api/health", timeout=30):
            print("serve did not become healthy; log tail:")
            print(open(os.path.join(workspace, "serve.log")).read()[-4000:])
            return 1

        # ---------------------------------------------------------------
        # 1. queen_llm.<id>: her running session switches on the next turn.
        # ---------------------------------------------------------------
        print("[1] queen_llm hot switch")
        status, created = http_json("POST", base_url + "/api/sessions", {"queen_name": "qa"})
        session_id = str(created.get("session_id") or created.get("id") or "")
        if not require(status in (200, 201) and bool(session_id), f"create qa session ({status} {created})"):
            return 1
        status, delivered = http_json(
            "POST", base_url + f"/api/sessions/{session_id}/chat", {"message": "hello queen"}
        )
        require(status == 200 and delivered.get("delivered") is True, "first chat delivered")
        got_old = wait_for(
            lambda: any("queen-old" in json.dumps(entry["body"].get("model", "")) for entry in old_gateway.requests()),
            timeout=20,
        )
        require(got_old, "pre-PUT turn reached the old base with model queen-old")

        new_section = {
            "provider": "openai",
            "model": "queen-new",
            "api_base": new_base,
            "api_key": "sk-e2e",
        }
        status, switched = http_json(
            "PUT", base_url + "/api/queen/qa/llm", {"section": new_section, "validate": False}
        )
        require(status == 200, f"PUT queen llm status ({status} {switched})")
        require(switched.get("sessions_switched") == 1, f"sessions_switched == 1 ({switched})")

        status, delivered = http_json(
            "POST", base_url + f"/api/sessions/{session_id}/chat", {"message": "hello again"}
        )
        require(status == 200 and delivered.get("delivered") is True, "post-PUT chat delivered")
        got_new = wait_for(
            lambda: any(
                entry["body"].get("model") == "queen-new"
                for entry in new_gateway.requests()
            ),
            timeout=20,
        )
        require(got_new, "next turn reached the new base with model queen-new")
        # The running session survived the swap: it answered the post-PUT turn.
        assistant_parts = []
        for root, _dirs, files in os.walk(os.path.join(home, "queens", "qa", "sessions", session_id)):
            if os.path.basename(os.path.dirname(root)) == "conversations" or "parts" in root:
                for name in files:
                    if name.endswith(".json"):
                        try:
                            with open(os.path.join(root, name)) as handle:
                                part = json.load(handle)
                        except Exception:
                            continue
                        if part.get("role") == "assistant":
                            assistant_parts.append(part)
        require(len(assistant_parts) >= 2, f"old session answered both turns ({len(assistant_parts)} assistant parts)")

        status, readback = http_json("GET", base_url + "/api/queen/qa/llm")
        require(
            status == 200 and readback.get("section", {}).get("model") == "queen-new",
            f"queen llm read-back is the written section ({readback.get('section')})",
        )

        # ---------------------------------------------------------------
        # 2. vision_fallback: the next tool-image caption uses the new slot.
        # ---------------------------------------------------------------
        print("[2] vision_fallback hot switch")
        status, created = http_json("POST", base_url + "/api/sessions", {"queen_name": "vision"})
        vision_session = str(created.get("session_id") or created.get("id") or "")
        if not require(status in (200, 201) and bool(vision_session), f"create vision session ({status} {created})"):
            return 1
        vision_section = {
            "provider": "openai",
            "model": "caption-new",
            "api_base": new_base,
            "api_key": "sk-e2e",
        }
        status, body = http_json(
            "PUT",
            base_url + "/api/config/llm-sections",
            {"role": "vision_fallback", "section": vision_section, "validate": False},
        )
        require(status == 200, f"PUT vision_fallback status ({status} {body})")
        status, delivered = http_json(
            "POST", base_url + f"/api/sessions/{vision_session}/chat", {"message": "look at the image"}
        )
        require(
            status == 200 and delivered.get("delivered") is True,
            f"vision turn delivered ({status} {delivered})",
        )
        captioned = wait_for(
            lambda: any(
                entry["body"].get("model") == "caption-new" and image_parts(entry["body"])
                for entry in new_gateway.requests()
            ),
            timeout=30,
        )
        require(captioned, "next caption reached the new base/model with the image attached")
        status, readback = http_json("GET", base_url + "/api/config/llm-sections")
        require(
            status == 200
            and readback.get("vision_fallback", {}).get("model") == "caption-new",
            f"vision_fallback read-back is the written section ({readback.get('vision_fallback')})",
        )

        # ---------------------------------------------------------------
        # 3. worker_llm: the next session's colony worker uses the new slot.
        # ---------------------------------------------------------------
        print("[3] worker_llm write -> next session's worker")
        status, body = http_json("POST", base_url + "/api/colonies/hotswap_col/scaffold", {"queen_name": "qa"})
        require(status == 200, f"scaffold colony ({status} {body})")
        worker_section = {
            "provider": "openai",
            "model": "worker-new",
            "api_base": new_base,
            "api_key": "sk-e2e",
        }
        status, body = http_json(
            "PUT",
            base_url + "/api/config/llm-sections",
            {"role": "worker_llm", "section": worker_section, "validate": False},
        )
        require(status == 200, f"PUT worker_llm status ({status} {body})")
        require(
            body.get("sessions_swapped") == 0,
            f"worker_llm write leaves running sessions alone ({body.get('sessions_swapped')})",
        )
        status, created = http_json(
            "POST",
            base_url + "/api/sessions",
            {"colony_id": "hotswap_col", "queen_name": "qa"},
        )
        colony_session = str(created.get("session_id") or created.get("id") or "")
        if not require(status in (200, 201) and bool(colony_session), f"create colony session ({status} {created})"):
            return 1
        status, delivered = http_json(
            "POST", base_url + f"/api/sessions/{colony_session}/chat", {"message": "please spawn a worker"}
        )
        require(status == 200 and delivered.get("delivered") is True, "colony session chat delivered")
        worker_seen = wait_for(
            lambda: any(
                entry["body"].get("model") == "worker-new"
                for entry in new_gateway.requests()
            ),
            timeout=45,
        )
        require(worker_seen, "the new session's worker reached the new base/model (worker-new)")
        status, readback = http_json("GET", base_url + "/api/config/llm-sections")
        require(
            status == 200
            and readback.get("worker_llm", {}).get("model") == "worker-new",
            f"worker_llm read-back is the written section ({readback.get('worker_llm')})",
        )

        # ---------------------------------------------------------------
        # Report the actual requests the gateways saw.
        # ---------------------------------------------------------------
        print("\nrecorded gateway requests:")
        for gateway in (old_gateway, new_gateway):
            for entry in gateway.requests():
                body = entry["body"]
                print(
                    f"  {gateway.name}: model={body.get('model')} "
                    f"api_base={body.get('api_base') or '(provider default)'} "
                    f"images={len(image_parts(body))} "
                    f"last_user={json.dumps(last_user_text(body))[:60]}"
                )
    finally:
        cleanup()

    print(f"\nE2E: {PASS} passed, {FAIL} failed")
    return 0 if FAIL == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
