#!/usr/bin/env python3
"""Real-aiohttp parity probe for webhook header/query/body-limit semantics.

Measures the real behaviour of framework.host.webhook_server.WebhookServer
(aiohttp, the Python reference implementation) for the three open
micro-differences between upstream and the Go port:

  A) duplicate request headers  -> dict(request.headers)
  B) duplicate query parameters -> dict(request.query)
  C) request body size limit    -> aiohttp default client_max_size behaviour

A raw socket is used so the exact request bytes (duplicate header lines,
exact body sizes) are fully controlled. Every response is the bytes the
server actually sent; captured events are what the real handler emitted.

Results are printed as JSON and written to probe-results.json next to this
script.

Run (from the repo/worktree root):
  VIRTUAL_ENV=/Users/mac/hive/.venv uv run --no-project python \
    go_hive/docs/e2e-assets/webhook-semantics/probe_aiohttp.py
"""

from __future__ import annotations

import asyncio
import json
import select
import socket
import sys
import time
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parents[4]
sys.path.insert(0, str(REPO_ROOT / "core"))

import aiohttp  # noqa: E402

from framework.host.event_bus import EventBus, EventType  # noqa: E402
from framework.host.webhook_server import (  # noqa: E402
    WebhookRoute,
    WebhookServer,
    WebhookServerConfig,
)

READ_TIMEOUT = 15.0


def _response_complete(buf: bytes) -> bool:
    head, sep, rest = buf.partition(b"\r\n\r\n")
    if not sep:
        return False
    for line in head.decode("latin-1").split("\r\n")[1:]:
        if line.lower().startswith("content-length:"):
            try:
                return len(rest) >= int(line.split(":", 1)[1].strip())
            except ValueError:
                return False
    return False


def raw_http(
    port: int,
    method: str,
    target: str,
    header_lines: list[tuple[str, str]],
    body: bytes,
) -> dict:
    """Send one raw HTTP/1.1 request (Host first, Connection: close) and parse
    the exact response bytes.

    Non-blocking select loop: a server that starts responding before the whole
    body is consumed must not deadlock the client. Partial sends are recorded
    instead of aborting, because the delivered response is the evidence.
    """
    lines = [f"{method} {target} HTTP/1.1", f"Host: 127.0.0.1:{port}"]
    for name, value in header_lines:
        lines.append(f"{name}: {value}")
    lines.append(f"Content-Length: {len(body)}")
    lines.append("Connection: close")
    raw = ("\r\n".join(lines) + "\r\n\r\n").encode("latin-1") + body

    deadline = time.monotonic() + READ_TIMEOUT
    sent = 0
    chunks: list[bytes] = []
    send_error = None
    eof = False
    complete_at = None

    sock = socket.create_connection(("127.0.0.1", port), timeout=READ_TIMEOUT)
    sock.setblocking(False)
    try:
        while True:
            now = time.monotonic()
            if now > deadline:
                break
            if complete_at is not None and now - complete_at >= 0.3:
                break
            want_send = sent < len(raw) and send_error is None
            try:
                readable, writable, _ = select.select(
                    [sock], [sock] if want_send else [], [], 0.1
                )
            except (OSError, ValueError):
                break
            if readable:
                try:
                    data = sock.recv(65536)
                except (BlockingIOError, InterruptedError):
                    data = None
                except ConnectionResetError as exc:
                    send_error = send_error or f"recv:{type(exc).__name__}"
                    break
                if data == b"":
                    eof = True
                    break
                if data:
                    chunks.append(data)
            if want_send and writable:
                try:
                    sent += sock.send(raw[sent : sent + 65536])
                except (BlockingIOError, InterruptedError):
                    pass
                except (BrokenPipeError, ConnectionResetError) as exc:
                    send_error = f"send:{type(exc).__name__}"
            if chunks and _response_complete(b"".join(chunks)) and complete_at is None:
                complete_at = time.monotonic()
    finally:
        sock.close()

    payload = b"".join(chunks)
    head, _, rest = payload.partition(b"\r\n\r\n")
    head_lines = head.decode("latin-1").split("\r\n")
    status_line = head_lines[0] if head_lines else ""
    status = None
    parts = status_line.split(" ")
    if len(parts) >= 2 and parts[1].isdigit():
        status = int(parts[1])
    headers: dict[str, str] = {}
    for line in head_lines[1:]:
        if ":" in line:
            key, _, value = line.partition(":")
            headers[key.strip().lower()] = value.strip()
    return {
        "status": status,
        "status_line": status_line,
        "headers": headers,
        "body_text": rest.decode("utf-8", errors="replace"),
        "raw_response_bytes": len(payload),
        "request_bytes_total": len(raw),
        "request_bytes_sent": sent,
        "request_fully_sent": sent == len(raw),
        "send_error": send_error,
        "server_closed": eof,
    }


def json_body_of_exact_size(total: int) -> bytes:
    """Build a valid JSON object body of exactly `total` bytes."""
    prefix = b'{"pad":"'
    suffix = b'"}'
    pad = total - len(prefix) - len(suffix)
    if pad < 0:
        raise ValueError(f"size {total} too small")
    return prefix + b"x" * pad + suffix


async def echo_app_probe(requests: list[dict]) -> dict:
    """Auxiliary: same aiohttp version, minimal app echoing the multi-dict
    structure (getall vs dict) so the flattening rule is visible directly."""
    out: dict = {}
    seen: dict[str, dict] = {}

    async def echo(request: aiohttp.web.Request) -> aiohttp.web.Response:
        seen[request.query_string] = {
            "dict_headers": dict(request.headers),
            "getall_xtest": request.headers.getall("X-Test", []),
            "get_xtest": request.headers.get("X-Test"),
            "getall_xmixcase": request.headers.getall("X-Mix-Case", []),
            "dict_query": dict(request.query),
            "getall_a": request.query.getall("a", []),
            "get_a": request.query.get("a"),
            "raw_query_string": request.query_string,
        }
        return aiohttp.web.json_response({"ok": True})

    app = aiohttp.web.Application()
    app.router.add_route("POST", "/echo", echo)
    runner = aiohttp.web.AppRunner(app)
    await runner.setup()
    site = aiohttp.web.TCPSite(runner, "127.0.0.1", 0)
    await site.start()
    echo_port = site._server.sockets[0].getsockname()[1]
    try:
        for req in requests:
            await asyncio.to_thread(
                raw_http,
                echo_port,
                req["method"],
                req["target"],
                [tuple(h) for h in req["header_lines"]],
                req["body"].encode("utf-8"),
            )
            await asyncio.sleep(0.05)
    finally:
        await runner.cleanup()
    out["by_query_string"] = seen
    out["echo_port"] = echo_port
    return out


async def main() -> None:
    results: dict = {
        "measurement": {
            "what": "WebhookServer (framework/host/webhook_server.py) real aiohttp behaviour",
            "how": "raw socket HTTP/1.1, Host header first, Connection: close",
            "aiohttp": aiohttp.__version__,
            "python": sys.version.split()[0],
            "server_file": str(REPO_ROOT / "core" / "framework" / "host" / "webhook_server.py"),
            "read_timeout_s": READ_TIMEOUT,
        },
        "probes": {},
    }

    bus = EventBus()
    captured: list[dict] = []

    async def on_event(event) -> None:
        data = event.data if isinstance(event.data, dict) else {}
        payload = data.get("payload")
        captured.append(
            {
                "stream_id": event.stream_id,
                "path": data.get("path"),
                "method": data.get("method"),
                "headers": data.get("headers"),
                "query_params": data.get("query_params"),
                "payload_keys": list(payload.keys()) if isinstance(payload, dict) else type(payload).__name__,
                "payload_pad_len": len(payload.get("pad", "")) if isinstance(payload, dict) and isinstance(payload.get("pad"), str) else None,
            }
        )

    bus.subscribe(event_types=[EventType.WEBHOOK_RECEIVED], handler=on_event)

    server = WebhookServer(bus, WebhookServerConfig(host="127.0.0.1", port=0))
    server.add_route(WebhookRoute(source_id="probe", path="/hook", methods=["POST", "GET"]))
    await server.start()
    port = server.port
    results["measurement"]["server_port"] = port

    async def probe(method: str, target: str, header_lines: list[tuple[str, str]], body: bytes) -> dict:
        before = len(captured)
        # The server runs on this same event loop: the raw socket client must
        # live in a worker thread or the loop (and thus the server) never runs.
        response = await asyncio.to_thread(raw_http, port, method, target, header_lines, body)
        await asyncio.sleep(0.15)
        result = {
            "request": {
                "method": method,
                "target": target,
                "header_lines": [list(h) for h in header_lines],
                "body_size": len(body),
            },
            "response": response,
            "captured_events": captured[before:],
        }
        print(
            f"probe {method} {target} body={len(body)} -> status={response['status']} "
            f"sent={response['request_bytes_sent']}/{response['request_bytes_total']} "
            f"events={len(result['captured_events'])} send_error={response['send_error']}",
            flush=True,
        )
        return result

    # ---- A) duplicate request headers -------------------------------------
    dup_headers = [
        ("Content-Type", "application/json"),
        ("X-Test", "alpha"),
        ("X-Test", "beta"),
        ("X-Mix-Case", "FirstCase"),
        ("x-mix-case", "secondcase"),
        ("X-Hub-Signature-256", "sha256=aaaa"),
        ("X-Hub-Signature-256", "sha256=bbbb"),
    ]
    results["probes"]["A_duplicate_headers"] = await probe("POST", "/hook", dup_headers, b"{}")

    # ---- B) duplicate query parameters ------------------------------------
    results["probes"]["B_duplicate_query_pure"] = await probe("POST", "/hook?a=1&a=2", [], b"{}")
    results["probes"]["B_duplicate_query_mixed"] = await probe(
        "POST", "/hook?a=1&a=2&b=3&empty=&a=4", [], b"{}"
    )

    # ---- C) body size limit (aiohttp default client_max_size) -------------
    c_probes: dict[str, dict] = {}
    for size in (1048575, 1048576, 1048577, 1572864):
        c_probes[str(size)] = await probe(
            "POST", "/hook", [("Content-Type", "application/json")], json_body_of_exact_size(size)
        )
    results["probes"]["C_body_limit"] = {
        "sizes_probed": [1048575, 1048576, 1048577, 1572864],
        "by_size": c_probes,
    }

    await server.stop()

    # ---- auxiliary: same aiohttp version, raw multi-dict structure --------
    results["probes"]["aux_multidict_structure"] = await echo_app_probe(
        [
            {
                "method": "POST",
                "target": "/echo",
                "header_lines": [list(h) for h in dup_headers],
                "body": "{}",
            },
            {
                "method": "POST",
                "target": "/echo?a=1&a=2&b=3&empty=&a=4",
                "header_lines": [["Content-Type", "application/json"]],
                "body": "{}",
            },
        ]
    )

    text = json.dumps(results, indent=2, ensure_ascii=False)
    (SCRIPT_DIR / "probe-results.json").write_text(text + "\n", encoding="utf-8")
    print(text)


if __name__ == "__main__":
    asyncio.run(main())
