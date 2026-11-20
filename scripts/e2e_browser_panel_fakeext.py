#!/usr/bin/env python3
"""Fake Chrome extension for the browser-panel e2e probe.

Speaks the bridge's extension protocol over the same WebSocket the shipped
extension uses (hello, ping/pong, tab.list, cdp.attach/detach, cdp), so the
e2e script can drive the *real* bridge and *real* server binaries without
Chrome. Only the frames the panel routes and the `hive-browser` CLI touch are
implemented — including Page.captureScreenshot (real Pillow-rendered images at
the requested clip/scale/format, so the 800px JPEG contract is exercised
end-to-end) and the Runtime.evaluate expressions the screenshot/snapshot
commands issue.

Usage: uv run --offline --project tools python e2e_browser_panel_fakeext.py --port 18829
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import io
import json
import os
import sys

# 1x1 transparent PNG — legacy fallback for callers that do not read the
# capture params (kept so older probes still get a decodable payload).
PNG_DATA = base64.b64encode(
    bytes.fromhex(
        "89504e470d0a1a0a0000000d494844520000000100000001080600000"
        "01f15c4890000000a49444154789c6360000002000154a24f6f000000"
        "0049454e44ae426082"
    )
).decode()

GROUP_ID = 777
VIEWPORT = {"width": 1280, "height": 720, "dpr": 1}
PAGE_URL = "https://example.com/"
TABS = [
    {"id": 101, "url": PAGE_URL, "title": "Example", "groupId": GROUP_ID},
    {"id": 102, "url": "https://other.example/", "title": "Other", "groupId": -1},
]

AX_NODES = [
    {
        "nodeId": "1",
        "ignored": False,
        "role": {"value": "RootWebArea"},
        "name": {"value": "Doc"},
        "childIds": ["2", "3"],
        "properties": [],
    },
    {
        "nodeId": "2",
        "ignored": False,
        "role": {"value": "button"},
        "name": {"value": "Go"},
        "childIds": [],
        "properties": [],
    },
    {
        "nodeId": "3",
        "ignored": False,
        "role": {"value": "link"},
        "name": {"value": "Docs"},
        "childIds": [],
        "properties": [],
    },
]

# <a href> names/hrefs in document order (matches AX node 3's accessible name).
LINK_PAIRS = [["Docs", "https://example.com/docs"]]


def render_capture(params: dict) -> str:
    """Render a real image for Page.captureScreenshot.

    Honors clip {x,y,width,height,scale} and format/quality exactly like
    Chrome: the bridge asks for format=jpeg&clip.scale so the returned payload
    is the 800px-wide JPEG upstream's PIL pass would produce.
    """
    from PIL import Image, ImageDraw

    fmt = params.get("format", "png")
    clip = params.get("clip") or {
        "x": 0,
        "y": 0,
        "width": VIEWPORT["width"],
        "height": VIEWPORT["height"],
        "scale": 1,
    }
    scale = clip.get("scale", 1) or 1
    width = max(1, round(clip.get("width", VIEWPORT["width"]) * scale))
    height = max(1, round(clip.get("height", VIEWPORT["height"]) * scale))

    img = Image.new("RGB", (width, height), (245, 245, 245))
    draw = ImageDraw.Draw(img)
    for x in range(0, width, 40):
        draw.line([(x, 0), (x, height)], fill=(200, 210, 220), width=1)
    for y in range(0, height, 40):
        draw.line([(0, y), (width, y)], fill=(200, 210, 220), width=1)
    draw.rectangle([0, 0, width - 1, 24], fill=(59, 130, 246))
    draw.text((6, 6), f"e2e {width}x{height} {fmt}", fill=(255, 255, 255))

    buf = io.BytesIO()
    if fmt == "jpeg":
        img.save(buf, format="JPEG", quality=int(params.get("quality", 75) or 75), optimize=True)
    else:
        img.save(buf, format="PNG")
    return base64.b64encode(buf.getvalue()).decode()


def evaluate(expression: str):
    """Answer the Runtime.evaluate expressions the bridge/commands issue."""
    if "devicePixelRatio" in expression:
        return {
            "url": PAGE_URL,
            "dpr": VIEWPORT["dpr"],
            "cssWidth": VIEWPORT["width"],
            "cssHeight": VIEWPORT["height"],
        }
    if "querySelectorAll('a[href]')" in expression:
        return LINK_PAIRS
    if "interactiveSelectors" in expression:
        return [
            {
                "tag": "a",
                "type": "a",
                "role": "link",
                "text": "Docs",
                "name": "",
                "href": "https://example.com/docs",
                "className": "",
                "rect": {"x": 0, "y": 0, "width": 100, "height": 20},
            }
        ]
    if "__hive_shot_hl" in expression:
        return "ok"
    if "window.location.href" in expression:
        return PAGE_URL
    if "innerWidth" in expression:
        return {"w": VIEWPORT["width"], "h": VIEWPORT["height"]}
    return None


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
        params = msg.get("params") or {}
        if method == "Page.captureScreenshot":
            return {"data": render_capture(params), "format": params.get("format", "png")}
        if method == "Page.getLayoutMetrics":
            return {"contentSize": {"width": 1280, "height": 720}}
        if method == "Accessibility.getFullAXTree":
            return {"nodes": AX_NODES}
        if method == "Runtime.evaluate":
            return {"result": {"type": "object", "value": evaluate(str(params.get("expression", "")))}}
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
