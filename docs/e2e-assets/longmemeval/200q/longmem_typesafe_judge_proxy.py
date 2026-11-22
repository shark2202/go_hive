#!/usr/bin/env python3
"""OpenAI-compatible bridge from the LongMemEval judge call to the TypeSafe judge slot.

Both LongMemEval harnesses grade a question by POSTing the official judge
prompt to ``{judge_api_base}/chat/completions`` and reading ``"yes"`` in the
reply. The judge slot configured for this machine is TypeSafe
``POST {api_base}/systemone`` (the ``noul`` probability question); it is not a
chat endpoint. This bridge forwards the judge prompt as the ``state`` of a
``noul`` question and turns p(yes) into a plain ``yes``/``no`` completion, so
both sides are graded by the same real model through the same path.

Every call is appended to ``--log`` as JSONL, including the exact prompt,
p_yes and the verdict, so the grading is auditable and replayable.

Usage:
  uv run --offline python go_hive/scripts/longmem_typesafe_judge_proxy.py \
    --port 18098 --judge http://127.0.0.1:18099/v1 \
    --model 0.8B-Instant-MLX-4bit --threshold 0.5 --label go-60 \
    --log <run-dir>/judge-log.jsonl
"""

from __future__ import annotations

import argparse
import hashlib
import json
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# The instruction the judge slot is asked. The official judge prompt itself is
# the `state`; this is the question put to the decision model about it. This
# wording is the best of four piloted variants (see the 2026-11-22 report).
INSTRUCTIONS = "Does the model response contain the correct answer?"
CRITERIA = {"true": "the model response contains the correct answer", "false": "the model response does not contain the correct answer"}

_LOG_LOCK = threading.Lock()


def call_typesafe(api_base: str, model: str, prompt: str, timeout: float, attempts: int = 4) -> float:
    """p(yes) for *prompt* from the TypeSafe /systemone endpoint."""
    body = {
        "state": prompt,
        "model": model,
        "questions": {"answer": {"type": "noul", "instructions": INSTRUCTIONS, "criteria": CRITERIA}},
    }
    data = json.dumps(body).encode("utf-8")
    url = api_base.rstrip("/") + "/systemone"
    last: Exception | None = None
    for attempt in range(attempts):
        req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                payload = json.load(resp)
            return float(payload["answers"]["answer"]["noul"])
        except urllib.error.HTTPError as exc:
            last = exc
            if exc.code in (429, 500, 502, 503, 504) and attempt < attempts - 1:
                time.sleep(2**attempt)
                continue
            raise
        except (urllib.error.URLError, TimeoutError, KeyError, ValueError, TypeError) as exc:
            last = exc
            if attempt < attempts - 1:
                time.sleep(2**attempt)
                continue
            raise
    raise RuntimeError(f"typesafe judge call failed: {last}")


class JudgeBridgeServer(ThreadingHTTPServer):
    """HTTP server carrying the bridge configuration (one per run)."""

    daemon_threads = True

    def __init__(self, address: tuple[str, int], judge: str, model: str, threshold: float, label: str, log_path: str, timeout_s: float) -> None:
        super().__init__(address, Handler)
        self.judge = judge
        self.model = model
        self.threshold = threshold
        self.label = label
        self.log_path = log_path
        self.timeout_s = timeout_s


class Handler(BaseHTTPRequestHandler):
    server_version = "typesafe-judge-bridge/1.0"

    def log_message(self, format: str, *args) -> None:  # noqa: A002, ANN002 - stdlib API
        print(f"[{time.strftime('%H:%M:%S')}] " + format % args, flush=True)

    def _json(self, status: int, payload: dict) -> None:
        data = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self) -> None:  # noqa: N802 - stdlib API
        if self.path.rstrip("/") in ("/v1/models", "/models"):
            self._json(200, {"object": "list", "data": [{"id": self.server.model, "object": "model"}]})
            return
        if self.path.rstrip("/") in ("/health", "/v1/health"):
            self._json(200, {"status": "ok", "model": self.server.model})
            return
        self._json(404, {"error": {"message": f"no route {self.path}"}})

    def do_POST(self) -> None:  # noqa: N802 - stdlib API
        server: JudgeBridgeServer = self.server  # type: ignore[assignment]
        if self.path.rstrip("/") not in ("/v1/chat/completions", "/chat/completions"):
            self._json(404, {"error": {"message": f"no route {self.path}"}})
            return
        try:
            length = int(self.headers.get("Content-Length") or 0)
            body = json.loads(self.rfile.read(length) or b"{}")
            messages = body.get("messages") or []
            prompt = ""
            for message in reversed(messages):
                if message.get("role") == "user":
                    prompt = message.get("content") or ""
                    break
            if not prompt:
                raise ValueError("no user message in request")
            requested_model = str(body.get("model") or "")
            started = time.time()
            p_yes = call_typesafe(server.judge, server.model, prompt, server.timeout_s)
            verdict = "yes" if p_yes >= server.threshold else "no"
            elapsed_ms = round((time.time() - started) * 1000.0, 1)
            line = {
                "ts": time.strftime("%Y-%m-%dT%H:%M:%S"),
                "label": server.label,
                "requested_model": requested_model,
                "prompt_sha256": hashlib.sha256(prompt.encode("utf-8")).hexdigest()[:16],
                "prompt_chars": len(prompt),
                "p_yes": p_yes,
                "verdict": verdict,
                "ms": elapsed_ms,
                "prompt": prompt,
            }
            with _LOG_LOCK, open(server.log_path, "a", encoding="utf-8") as fh:
                fh.write(json.dumps(line, ensure_ascii=False) + "\n")
            self._json(
                200,
                {
                    "id": "chatcmpl-bridge-" + line["prompt_sha256"],
                    "object": "chat.completion",
                    "created": int(time.time()),
                    "model": requested_model or server.model,
                    "choices": [
                        {"index": 0, "message": {"role": "assistant", "content": verdict}, "finish_reason": "stop"}
                    ],
                    "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
                },
            )
        except Exception as exc:  # noqa: BLE001 - the harness retries 5xx
            self._json(502, {"error": {"message": f"{type(exc).__name__}: {exc}"}})


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=18098)
    parser.add_argument("--judge", default="http://127.0.0.1:18099/v1", help="TypeSafe api_base")
    parser.add_argument("--model", default="0.8B-Instant-MLX-4bit")
    parser.add_argument("--threshold", type=float, default=0.5)
    parser.add_argument("--label", default="run")
    parser.add_argument("--log", required=True)
    parser.add_argument("--timeout", type=float, default=120.0)
    args = parser.parse_args()

    server = JudgeBridgeServer(
        ("127.0.0.1", args.port),
        judge=args.judge,
        model=args.model,
        threshold=args.threshold,
        label=args.label,
        log_path=args.log,
        timeout_s=args.timeout,
    )
    print(
        f"judge bridge on 127.0.0.1:{args.port}: /v1/chat/completions -> {args.judge}/systemone "
        f"(model={args.model}, threshold={args.threshold}, label={args.label}, log={args.log})",
        flush=True,
    )
    server.serve_forever()


if __name__ == "__main__":
    main()
