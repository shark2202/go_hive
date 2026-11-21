#!/usr/bin/env python3
"""Fake Chrome extension + control-RPC driver for the R09/R10/R11 e2e probe.

Speaks the same WebSocket the shipped extension uses (hello, ping/pong,
tab.list, cdp.attach, cdp) against the *real* browserbridge binary, and can
also act as a control-RPC client (14831) to drive concurrent `_cdp` calls.

Two modes:

    # extension side: dial the bridge and answer commands
    python e2e_browser_residuals_fakeext.py --serve --port 18929 \
        --events /tmp/ev.jsonl --release /tmp/release

    # driver side: concurrent _cdp + 17 MiB frame + /status fields
    python e2e_browser_residuals_fakeext.py --drive --control-port 18931 \
        --http-port 18930 --events /tmp/ev.jsonl --release /tmp/release \
        --expect-version 0.3.0-l3a

Frame handling is concurrent (one task per command), so a held `cdp` cannot
mask whether the bridge sent the next one: the R09 assertion reads the event
log, which records the moment each command is *read off the socket*.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys
import time
import urllib.request

TAB_ID = 101
BIG_BYTES = 17 * 1024 * 1024  # above the old 16 MiB cap, below upstream 50 MiB


def append_event(path: str, event: str, **fields) -> None:
    record = {"event": event, "ts": time.time(), **fields}
    with open(path, "a", encoding="utf-8") as fh:
        fh.write(json.dumps(record) + "\n")
        fh.flush()


def read_events(path: str) -> list[dict]:
    try:
        with open(path, "r", encoding="utf-8") as fh:
            return [json.loads(line) for line in fh if line.strip()]
    except FileNotFoundError:
        return []


# ──────────────────────────────────────────────────────────────────────────
# Extension side
# ──────────────────────────────────────────────────────────────────────────


async def respond(ws, msg: dict, events: str, release: str) -> None:
    typ = msg.get("type")
    mid = msg.get("id")
    if mid is None:
        return
    params = msg.get("params") or {}
    result = {"ok": True}
    if typ == "tab.list":
        result = {"tabs": [{"id": TAB_ID, "url": "https://example.com/", "title": "Example", "groupId": -1}]}
    elif typ in ("cdp.attach", "cdp.detach", "tab.audit"):
        result = {"ok": True}
    elif typ == "cdp":
        method = msg.get("method")
        params = msg.get("params") or {}
        expression = str(params.get("expression", ""))
        if method == "Runtime.evaluate" and expression == "__slow__":
            append_event(events, "slow-arrived")
            while not os.path.exists(release):
                await asyncio.sleep(0.02)
            append_event(events, "slow-released")
            result = {"result": {"value": "slow-result"}}
        elif method == "Runtime.evaluate" and expression == "__big__":
            append_event(events, "big-arrived")
            result = {"result": {"value": "x" * BIG_BYTES}}
        else:
            append_event(events, "cdp", method=method, expression=expression)
            result = {"result": {"value": "ok"}}
    await ws.send(json.dumps({"id": mid, "result": result}))


async def session(ws, events: str, release: str) -> None:
    await ws.send(
        json.dumps(
            {
                "type": "hello",
                "protocolVersion": 6,
                "extensionId": "e2e-residuals-extension",
                "version": "0.0.0-e2e",
                "profileLabel": "e2e-profile",
            }
        )
    )
    tasks: set[asyncio.Task] = set()
    try:
        async for raw in ws:
            msg = json.loads(raw)
            if msg.get("type") == "ping":
                await ws.send(json.dumps({"type": "pong"}))
                continue
            task = asyncio.create_task(respond(ws, msg, events, release))
            tasks.add(task)
            task.add_done_callback(tasks.discard)
    finally:
        for task in tasks:
            task.cancel()


async def serve(port: int, events: str, release: str) -> None:
    import websockets

    url = f"ws://127.0.0.1:{port}/bridge"
    while True:
        try:
            async with websockets.connect(url, max_size=64 * 1024 * 1024) as ws:
                print(f"fake extension: connected to {url}", flush=True)
                await session(ws, events, release)
        except OSError as exc:
            print(f"fake extension: connect retry ({exc})", file=sys.stderr, flush=True)
        await asyncio.sleep(0.2)


# ──────────────────────────────────────────────────────────────────────────
# Driver side (control RPC + /status assertions)
# ──────────────────────────────────────────────────────────────────────────


def check(condition: bool, label: str) -> None:
    status = "PASS" if condition else "FAIL"
    print(f"{status}: {label}", flush=True)
    if not condition:
        raise SystemExit(1)


async def drive(control_port: int, http_port: int, events: str, release: str, expect_version: str) -> None:
    import websockets

    if os.path.exists(release):
        os.remove(release)
    if os.path.exists(events):
        os.remove(events)

    pending: dict[str, asyncio.Future] = {}
    url = f"ws://127.0.0.1:{control_port}/control"
    async with websockets.connect(url, max_size=64 * 1024 * 1024) as ws:

        async def reader() -> None:
            async for raw in ws:
                msg = json.loads(raw)
                rpc_id = str(msg.get("rpc_id"))
                future = pending.get(rpc_id)
                if future is not None and not future.done():
                    future.set_result(msg)

        reader_task = asyncio.create_task(reader())

        def call(rpc_id: str, method: str, args: list) -> asyncio.Future:
            fut = asyncio.get_running_loop().create_future()
            pending[rpc_id] = fut
            payload = json.dumps({"rpc_id": rpc_id, "method": method, "args": args, "params": {}})
            asyncio.create_task(ws.send(payload))
            return fut

        await call("hello", "client_hello", [os.getpid()])

        slow = call("slow", "_cdp", [TAB_ID, "Runtime.evaluate", {"expression": "__slow__"}])
        # Wait until the extension has read the first command off the wire.
        for _ in range(250):
            if any(e["event"] == "slow-arrived" for e in read_events(events)):
                break
            await asyncio.sleep(0.02)
        check(any(e["event"] == "slow-arrived" for e in read_events(events)), "R09 slow cdp reached the extension")

        quick = call("quick", "_cdp", [TAB_ID, "Runtime.evaluate", {"expression": "__quick__"}])
        await asyncio.sleep(0.5)
        early = [e for e in read_events(events) if e["event"] == "cdp" and e.get("expression") == "__quick__"]
        check(not early, "R09 same-tab cdp serialized: quick frame did not reach the extension before slow finished")

        with open(release, "w", encoding="utf-8") as fh:
            fh.write("go")

        slow_reply = await asyncio.wait_for(slow, 10)
        quick_reply = await asyncio.wait_for(quick, 10)
        check(bool(slow_reply.get("ok")), f"R09 slow cdp ok ({slow_reply.get('result')})")
        check(bool(quick_reply.get("ok")), f"R09 quick cdp ok ({quick_reply.get('result')})")

        order = [e["event"] for e in read_events(events)]
        check(
            order.index("slow-released") < order.index("cdp"),
            f"R09 quick frame arrived only after the slow reply was released ({order})",
        )

        big = await asyncio.wait_for(call("big", "_cdp", [TAB_ID, "Runtime.evaluate", {"expression": "__big__"}]), 30)
        big_value = (((big.get("result") or {}).get("result") or {}).get("value")) or ""
        check(
            big.get("ok") and len(big_value) == BIG_BYTES,
            f"R10 17 MiB extension frame accepted and relayed ({len(big_value)} bytes)",
        )
        again = await asyncio.wait_for(call("again", "_cdp", [TAB_ID, "Runtime.evaluate", {"expression": "__after__"}]), 10)
        check(bool(again.get("ok")), "R10 extension connection survived the 17 MiB frame")

        reader_task.cancel()

    with urllib.request.urlopen(f"http://127.0.0.1:{http_port}/status", timeout=5) as resp:
        status = json.loads(resp.read().decode())
    check(status.get("connected") is True, "R11 /status connected:true with the extension attached")
    check(status.get("bridge_version") == expect_version, f"R11 bridge_version={status.get('bridge_version')!r} wants {expect_version!r}")
    check("runtime_version" in status and status.get("runtime_version") is None, "R11 runtime_version present and null (no embedder value)")
    check(status.get("bind_errors") == {}, f"R11 bind_errors empty on a healthy bridge ({status.get('bind_errors')!r})")
    listening = status.get("listening") or {}
    check(
        listening.get("ws") is True
        and listening.get("status") is True
        and listening.get("control_rpc") is True
        and listening.get("legacy_status") is False,
        f"R11 listening block shape ({listening})",
    )
    print("ALL PASS", flush=True)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--serve", action="store_true")
    parser.add_argument("--drive", action="store_true")
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--control-port", type=int, default=0)
    parser.add_argument("--http-port", type=int, default=0)
    parser.add_argument("--events", default="")
    parser.add_argument("--release", default="")
    parser.add_argument("--expect-version", default="")
    args = parser.parse_args()
    if args.serve and args.port and args.events and args.release:
        asyncio.run(serve(args.port, args.events, args.release))
        return
    if args.drive and args.control_port and args.http_port and args.events and args.release and args.expect_version:
        asyncio.run(drive(args.control_port, args.http_port, args.events, args.release, args.expect_version))
        return
    parser.error("choose --serve (--port --events --release) or --drive (--control-port --http-port --events --release --expect-version)")
    sys.exit(2)


if __name__ == "__main__":
    main()
