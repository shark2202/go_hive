"""Deterministic probe for the bridge's request/response ordering.

The host can answer ``query_rows`` / ``dispatch_one`` fast enough that its reply
is read before the child has finished registering the pending slot for the
request. This probe simulates exactly that by replacing the wire write with an
*instant* reply delivered synchronously inside ``_emit`` -- i.e. before
``_rpc_send`` could have returned in the old "send, then register" ordering.
That ordering lost the reply and hung the run.

With registration-first both RPC forms must return the reply normally, and no
reply may be dropped.

Usage:
    python3 bridge_race_probe.py <path to the embedded bootstrap.py>

Exit code 0 = both paths returned the reply; 1 = a reply was dropped (hang).
"""

from __future__ import annotations

import asyncio
import importlib.util
import sys
import threading


def load_bootstrap(path: str):
    spec = importlib.util.spec_from_file_location("hive_bridge_bootstrap", path)
    assert spec is not None and spec.loader is not None, path
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: bridge_race_probe.py <bootstrap.py>", file=sys.stderr)
        return 2
    bootstrap = load_bootstrap(sys.argv[1])
    dropped: list[int] = []

    def instant_reply(payload: dict) -> None:
        # Simulates the host answering the instant the request is on the wire:
        # the reply is processed while _emit is still on the caller's stack.
        message = {"id": payload["id"], "result": [{"slug": "a"}]}
        with getattr(bootstrap, "_pending_lock"):
            slot = getattr(bootstrap, "_pending").pop(message["id"], None)
        if slot is None:
            dropped.append(message["id"])
            return
        getattr(bootstrap, "_deliver")(slot, message)

    setattr(bootstrap, "_emit", instant_reply)

    # 1) synchronous path (tracker_query's transport)
    box: dict = {}

    def call_sync() -> None:
        try:
            box["rows"] = getattr(bootstrap, "_rpc_sync")(
                "query_rows", {"sql": "SELECT slug FROM leads WHERE done=0"}
            )
        except BaseException as exc:  # surfaced below
            box["error"] = repr(exc)

    worker = threading.Thread(target=call_sync, daemon=True)
    worker.start()
    worker.join(3.0)
    if worker.is_alive():
        print(
            "FAIL: _rpc_sync hung (its reply was dropped before registration)",
            file=sys.stderr,
        )
        return 1
    if box.get("error") or box.get("rows") != [{"slug": "a"}]:
        print(f"FAIL: _rpc_sync returned {box!r}", file=sys.stderr)
        return 1

    # 2) asynchronous path (worker's dispatch_one transport)
    async def call_async():
        return await getattr(bootstrap, "_rpc_async")("dispatch_one", {"task": "probe"})

    async def runner():
        setattr(bootstrap, "_loop", asyncio.get_running_loop())
        return await asyncio.wait_for(call_async(), 3.0)

    try:
        report = asyncio.run(runner())
    except asyncio.TimeoutError:
        print(
            "FAIL: _rpc_async hung (its reply was dropped before registration)",
            file=sys.stderr,
        )
        return 1
    if report != [{"slug": "a"}]:
        print(f"FAIL: _rpc_async returned {report!r}", file=sys.stderr)
        return 1

    if dropped:
        print(f"FAIL: replies dropped for id(s) {dropped}", file=sys.stderr)
        return 1
    print("OK: both rpc paths completed for replies that raced the registration")
    return 0


if __name__ == "__main__":
    sys.exit(main())
