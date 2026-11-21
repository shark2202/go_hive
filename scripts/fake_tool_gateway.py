#!/usr/bin/env python3
"""Scripted fake OpenAI-compatible gateway for tool-output captures.

Every request body is logged verbatim (that is how the tool results are
captured: each following request carries the previous results as `role: tool`
messages). Responses follow a script: each model call consumes the next step,
so one chat can drive a fixed tool sequence.

Step forms:
    {"tool": "read_file", "arguments": {"path": "probe.txt"}}
    {"text": "done"}

Usage:
    python3 fake_tool_gateway.py --port 8807 --log requests.jsonl --script script.json
"""

from __future__ import annotations

import argparse
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG_PATH = ""
LOG_LOCK = threading.Lock()
SCRIPT: list[dict] = []
SCRIPT_LOCK = threading.Lock()
CALL_INDEX = 0


def _log(entry: dict) -> None:
    with LOG_LOCK:
        with open(LOG_PATH, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(entry, ensure_ascii=False) + "\n")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, format, *args):  # type: ignore[override]  # noqa: A002
        return

    def _read_body(self) -> bytes:
        length = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(length) if length else b""

    def _send_sse(self, chunks: list[dict]) -> None:
        payload = "".join(f"data: {json.dumps(chunk)}\n\n" for chunk in chunks) + "data: [DONE]\n\n"
        raw = payload.encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def _json(self, payload: dict, status: int = 200) -> None:
        raw = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):  # noqa: N802
        if self.path.endswith("/models"):
            self._json({"object": "list", "data": [{"id": "fake-tool-golden", "object": "model"}]})
            return
        self._json({"error": "not found"}, 404)

    def do_POST(self):  # noqa: N802
        global CALL_INDEX
        raw = self._read_body()
        try:
            body = json.loads(raw.decode("utf-8"))
        except Exception:
            body = {"_raw": raw.decode("utf-8", "replace")}
        with SCRIPT_LOCK:
            step = SCRIPT[CALL_INDEX] if CALL_INDEX < len(SCRIPT) else {"text": "ack"}
            index = CALL_INDEX
            CALL_INDEX += 1
        _log(
            {
                "ts": time.time(),
                "index": index,
                "path": self.path,
                "model": body.get("model"),
                "stream": bool(body.get("stream")),
                "tool_count": len(body.get("tools") or []),
                "tools": body.get("tools"),
                "messages": body.get("messages"),
            }
        )
        model = body.get("model") or "fake-tool-golden"
        tool = step.get("tool")
        if tool:
            arguments = json.dumps(step.get("arguments") or {})
            chunks = [
                {
                    "id": "chatcmpl-fake",
                    "object": "chat.completion.chunk",
                    "created": int(time.time()),
                    "model": model,
                    "choices": [
                        {
                            "index": 0,
                            "delta": {
                                "role": "assistant",
                                "tool_calls": [
                                    {
                                        "index": 0,
                                        "id": f"call_{index}",
                                        "type": "function",
                                        "function": {"name": tool, "arguments": arguments},
                                    }
                                ],
                            },
                            "finish_reason": "tool_calls",
                        }
                    ],
                }
            ]
        else:
            text = step.get("text", "ack")
            chunks = [
                {
                    "id": "chatcmpl-fake",
                    "object": "chat.completion.chunk",
                    "created": int(time.time()),
                    "model": model,
                    "choices": [
                        {"index": 0, "delta": {"role": "assistant", "content": text}, "finish_reason": None}
                    ],
                },
                {
                    "id": "chatcmpl-fake",
                    "object": "chat.completion.chunk",
                    "created": int(time.time()),
                    "model": model,
                    "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                },
            ]
        if body.get("stream"):
            self._send_sse(chunks)
            return
        message: dict = {"role": "assistant", "content": None if tool else step.get("text", "")}
        if tool:
            message["tool_calls"] = [
                {
                    "id": f"call_{index}",
                    "type": "function",
                    "function": {"name": tool, "arguments": json.dumps(step.get("arguments") or {})},
                }
            ]
        self._json(
            {
                "id": "chatcmpl-fake",
                "object": "chat.completion",
                "created": int(time.time()),
                "model": model,
                "choices": [
                    {
                        "index": 0,
                        "message": message,
                        "finish_reason": "tool_calls" if tool else "stop",
                    }
                ],
                "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
            }
        )


def main() -> None:
    global LOG_PATH
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--log", required=True)
    parser.add_argument("--script", required=True)
    args = parser.parse_args()
    LOG_PATH = args.log
    open(LOG_PATH, "a", encoding="utf-8").close()
    with open(args.script, encoding="utf-8") as handle:
        SCRIPT.extend(json.load(handle))
    ThreadingHTTPServer(("127.0.0.1", args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
