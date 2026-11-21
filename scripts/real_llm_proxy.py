#!/usr/bin/env python3
"""Recording pass-through proxy in front of a real OpenAI-compatible API.

This is *not* a model stub: every request is forwarded to the configured
upstream (the real provider) and the upstream's answer is relayed back
unchanged. The proxy only records what went over the wire so the two
implementations (Go and Python) can be compared on the same real-model run.

Nothing secret is written: request headers are never recorded, and the
`Authorization` header is forwarded but never logged. The upstream key is
injected from an environment variable when the client did not send one, so a
temporary HIVE_HOME can keep the same `api_key_env_var` shape as the user's
main configuration without the key ever touching disk.

Usage:
  BROADTOKEN_API_KEY=... python3 real_llm_proxy.py \
      --upstream https://token.tokenbroad.com/v1 \
      --listen 127.0.0.1:0 \
      --log /tmp/go-proxy.jsonl --side go

The first stdout line is `PROXY READY http://127.0.0.1:<port>/v1` so a driver
can read the bound port.
"""

from __future__ import annotations

import argparse
import http.client
import json
import os
import socket
import ssl
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
}

KIND_MARKERS = (
    ("timeline", "You build a dated timeline of what a user has told an assistant"),
    ("recall", "You are selecting memories that will be useful to the Queen agent"),
    ("soul", "Once a day you read back what was said in your chats"),
    ("memory_reflection", "You are a reflection agent that distills durable knowledge"),
    ("memory_reflection", "You are a reflection agent performing a periodic housekeeping pass"),
)


def classify(body: str) -> str:
    for kind, marker in KIND_MARKERS:
        if marker in body:
            return kind
    return "chat"


class Recorder:
    def __init__(self, path: str, side: str):
        self.path = path
        self.side = side
        self.lock = threading.Lock()
        # Touch the file so an empty run is still visible.
        open(path, "a", encoding="utf-8").close()

    def write(self, entry: dict) -> None:
        entry["side"] = self.side
        line = json.dumps(entry, ensure_ascii=False)
        with self.lock:
            with open(self.path, "a", encoding="utf-8") as handle:
                handle.write(line + "\n")


class ProxyHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "hive-real-llm-recorder/1.0"

    # -- plumbing ---------------------------------------------------------

    def log_message(self, format: str, *args) -> None:  # noqa: A002 - stdlib signature
        if os.environ.get("REAL_LLM_PROXY_VERBOSE") == "1":
            sys.stderr.write("proxy: " + (format % args) + "\n")

    def do_GET(self) -> None:
        self._proxy("GET")

    def do_POST(self) -> None:
        self._proxy("POST")

    def do_PUT(self) -> None:
        self._proxy("PUT")

    def do_DELETE(self) -> None:
        self._proxy("DELETE")

    def _read_body(self) -> bytes:
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0:
            return b""
        return self.rfile.read(length)

    def _proxy(self, method: str) -> None:
        started = time.time()
        body = self._read_body()
        body_text = body.decode("utf-8", "replace")
        kind = classify(body_text)

        # Header names are case-insensitive: collapse them, keep the first
        # Authorization the client sent, and add the environment key only when
        # the client sent none. Sending two Authorization headers makes the
        # upstream ALB answer 400, so exactly one leaves this proxy.
        merged: dict[str, str] = {}
        for name, value in self.headers.items():
            lowered = name.lower()
            if lowered in HOP_HEADERS or lowered in ("host", "content-length", "accept-encoding"):
                continue
            if lowered == "authorization":
                if "authorization" not in merged:
                    merged["authorization"] = value
                continue
            merged[lowered] = value
        # The client asked for identity encoding so the recorded answer is the
        # text the model actually produced.
        merged["accept-encoding"] = "identity"
        if "authorization" not in merged:
            key = os.environ.get("BROADTOKEN_API_KEY", "")
            if key:
                merged["authorization"] = "Bearer " + key
        headers = merged
        # Evidence for transport-level debugging: header NAMES only; the
        # Authorization value is reduced to presence + length, never recorded.
        forwarded_headers = sorted(headers)
        auth_present = "authorization" in headers
        auth_len = len(headers.get("authorization", ""))

        upstream = self.server.upstream  # type: ignore[attr-defined]
        connection = upstream["connection"]()
        record: dict = {
            "ts": time.strftime("%Y-%m-%dT%H:%M:%S"),
            "method": method,
            "path": self.path,
            "kind": kind,
        }
        status = 502
        response_text = ""
        try:
            connection.request(method, upstream["prefix"] + self.path, body=body, headers=headers)
            response = connection.getresponse()
            status = response.status
            raw = response.read()
            response_text = raw.decode("utf-8", "replace")
            content_type = response.getheader("Content-Type") or ""
            self.send_response(status)
            if content_type:
                self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
        except Exception as exc:  # upstream/network failure: visible to the client
            response_text = f"proxy upstream error: {type(exc).__name__}: {exc}"
            try:
                payload = response_text.encode()
                self.send_response(502)
                self.send_header("Content-Type", "text/plain; charset=utf-8")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
            except Exception:
                pass
        finally:
            try:
                connection.close()
            except Exception:
                pass

        record["status"] = status
        record["latency_ms"] = int((time.time() - started) * 1000)
        record["forwarded_headers"] = forwarded_headers
        record["authorization_present"] = auth_present
        record["authorization_length"] = auth_len
        record["request"] = self._decode_json(body_text)
        record["response"] = self._decode_json(response_text)
        self.server.recorder.write(record)  # type: ignore[attr-defined]

    @staticmethod
    def _decode_json(text: str):
        text = text.strip()
        if not text:
            return None
        try:
            return json.loads(text)
        except ValueError:
            return text


def build_upstream(url: str):
    parts = urlsplit(url.rstrip("/"))
    if parts.scheme not in ("http", "https") or not parts.hostname:
        raise SystemExit(f"bad upstream URL: {url}")
    host = parts.hostname
    port = parts.port or (443 if parts.scheme == "https" else 80)
    context = ssl.create_default_context() if parts.scheme == "https" else None

    def factory():
        if parts.scheme == "https":
            return http.client.HTTPSConnection(host, port, timeout=900, context=context)
        return http.client.HTTPConnection(host, port, timeout=900)

    # Clients point at the proxy with the upstream's own path already in their
    # api_base (the driver advertises it), so the request path is forwarded
    # verbatim; the proxy adds no prefix of its own.
    return {"connection": factory, "prefix": ""}


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--upstream", required=True)
    parser.add_argument("--listen", default="127.0.0.1:0")
    parser.add_argument("--log", required=True)
    parser.add_argument("--side", default="")
    args = parser.parse_args()

    host, _, port_text = args.listen.partition(":")
    port = int(port_text) if port_text else free_port()
    recorder = Recorder(args.log, args.side)
    server = ThreadingHTTPServer((host or "127.0.0.1", port), ProxyHandler)
    server.daemon_threads = True
    server.upstream = build_upstream(args.upstream)  # type: ignore[attr-defined]
    server.recorder = recorder  # type: ignore[attr-defined]
    print(f"PROXY READY http://{host or '127.0.0.1'}:{server.server_address[1]}{urlsplit(args.upstream).path}", flush=True)
    try:
        server.serve_forever(poll_interval=0.2)
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
