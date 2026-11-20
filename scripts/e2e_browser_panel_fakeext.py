#!/usr/bin/env python3
"""Fake Chrome extension for the browser-panel e2e probe.

Speaks the bridge's extension protocol over the same WebSocket the shipped
extension uses (hello, ping/pong, tab.list, cdp.attach/detach, cdp), so the
e2e script can drive the *real* bridge and *real* server binaries without
Chrome. Only the frames the panel routes and the `hive-browser` CLI touch are
implemented.

Usage: uv run --offline --project tools python e2e_browser_panel_fakeext.py --port 18829
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import json
import os
import sys

# 1x1 transparent PNG — enough for a real Page.captureScreenshot round trip.
PNG_DATA = base64.b64encode(
    bytes.fromhex(
        "89504e470d0a1a0a0000000d494844520000000100000001080600000"
        "01f15c4890000000a49444154789c6360000002000154a24f6f000000"
        "0049454e44ae426082"
    )
).decode()

GROUP_ID = 777
TABS = [
    {"id": 101, "url": "https://example.com/", "title": "Example", "groupId": GROUP_ID},
    {"id": 102, "url": "https://other.example/", "title": "Other", "groupId": -1},
]


def handle(typ: str, msg: dict) -> dict:
    if typ == "tab.list":
        group = msg.get("groupId")
        if isinstance(group, int):
            return {"tabs": [t for t in TABS if t["groupId"] == group]}
        return {"tabs": TABS}
    if typ in ("cdp.attach", "cdp.detach"):
        return {"ok": True}
    if typ == "cdp":
        method = msg.get("method")
        if method == "Page.captureScreenshot":
            return {"data": PNG_DATA, "format": "png"}
        if method == "Page.getLayoutMetrics":
            return {"contentSize": {"width": 1280, "height": 720}}
        return {}
    if typ == "context.create":
        return {"groupId": GROUP_ID, "windowId": 1, "tabId": 101}
    if typ == "context.destroy":
        return {"closedTabs": 1}
    if typ in ("tab.create", "tab.reveal", "tab.activate", "tab.close"):
        return {"tabId": 101, "groupId": GROUP_ID, "ok": True}
    return {"ok": True}


async def session(websocket) -> None:
    await websocket.send(
        json.dumps(
            {
                "type": "hello",
                "protocolVersion": 6,
                "extensionId": "e2e-fake-extension",
                "version": "0.0.0-e2e",
                "profileLabel": "e2e-profile",
            }
        )
    )
    async for raw in websocket:
        msg = json.loads(raw)
        typ = msg.get("type")
        if typ == "ping":
            await websocket.send(json.dumps({"type": "pong"}))
            continue
        if typ == "hello":
            continue
        mid = msg.get("id")
        if mid is None:
            continue
        try:
            result = handle(typ, msg)
        except Exception as exc:  # noqa: BLE001 - surface as a bridge error frame
            await websocket.send(json.dumps({"id": mid, "error": str(exc)}))
            continue
        await websocket.send(json.dumps({"id": mid, "result": result}))


async def control_create_context(host: str, port: int, agent_id: str) -> None:
    """One-shot control-RPC call: hello + create_context, print the reply."""
    import websockets

    url = f"ws://{host}:{port}/control"
    async with websockets.connect(url) as websocket:
        await websocket.send(
            json.dumps(
                {
                    "rpc_id": "1",
                    "method": "client_hello",
                    "args": [os.getpid()],
                    "params": {},
                }
            )
        )
        await websocket.send(
            json.dumps(
                {
                    "rpc_id": "2",
                    "method": "create_context",
                    "args": [],
                    "params": {"agent_id": agent_id, "display_name": agent_id, "browser_profile": ""},
                }
            )
        )
        while True:
            msg = json.loads(await websocket.recv())
            if str(msg.get("rpc_id")) == "2":
                print(json.dumps(msg))
                return


async def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--control-port", type=int, default=0)
    parser.add_argument("--create-context", metavar="AGENT_ID", default="")
    args = parser.parse_args()
    if not args.port and not args.control_port:
        parser.error("--port or --control-port is required")
    if args.control_port and args.create_context:
        await control_create_context(args.host, args.control_port, args.create_context)
        return
    url = f"ws://{args.host}:{args.port}/bridge"
    async for websocket in _reconnect(url):
        try:
            await session(websocket)
        except Exception as exc:  # noqa: BLE001 - reconnecting loop below
            print(f"fake extension: session ended: {exc}", file=sys.stderr, flush=True)


async def _reconnect(url: str):
    import websockets

    while True:
        try:
            async with websockets.connect(url, max_size=16 * 1024 * 1024) as websocket:
                print(f"fake extension: connected to {url}", flush=True)
                yield websocket
        except OSError as exc:
            print(f"fake extension: connect retry ({exc})", file=sys.stderr, flush=True)
        await asyncio.sleep(0.2)


if __name__ == "__main__":
    asyncio.run(main())
