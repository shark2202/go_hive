"""stdio JSON-line JSON-RPC transport for the Hive channel host.

The host sends ``hello`` / ``start`` / ``stop`` requests and ``output``
notifications. The plugin sends ``open_session`` / ``send`` / ``subscribe``
requests back. stdout is a protocol channel only; every log goes to stderr.
"""

from __future__ import annotations

import asyncio
import json
import logging
import sys
import threading
from typing import Any, Awaitable, Callable, BinaryIO

PROTOCOL_VERSION = "hive-channel/1"

CODE_INVALID_REQUEST = -32600
CODE_METHOD_NOT_FOUND = -32601
CODE_INVALID_PARAMS = -32602
CODE_INTERNAL = -32603
CODE_CHANNEL_ERROR = -32000


class RpcError(Exception):
    """A JSON-RPC error returned by the host or raised by bridge code."""

    def __init__(self, code: int, message: str, kind: str | None = None):
        super().__init__(message)
        self.code = code
        self.message = message
        self.kind = kind


class StdioTransport:
    """One newline-delimited JSON object per frame.

    Reader runs on a daemon thread so the asyncio loop can wait on host
    requests and plugin calls at the same time. Writer calls are serialized:
    concurrent host requests and output handling must not interleave bytes.
    """

    def __init__(
        self,
        *,
        reader: BinaryIO | None = None,
        writer: BinaryIO | None = None,
        logger: logging.Logger | None = None,
    ) -> None:
        self.reader = reader or sys.stdin.buffer
        self.writer = writer or sys.stdout.buffer
        self.log = logger or logging.getLogger("wxbot_channel.transport")
        self._write_lock = threading.Lock()
        self._pending: dict[str, asyncio.Future[Any]] = {}
        self._next_id = 0
        self._closed = False

    # ── outbound ──────────────────────────────────────────────────────

    def _write(self, frame: dict[str, Any]) -> None:
        data = (
            json.dumps(frame, ensure_ascii=False, separators=(",", ":")) + "\n"
        ).encode("utf-8")
        with self._write_lock:
            if self._closed:
                raise EOFError("transport closed")
            self.writer.write(data)
            self.writer.flush()

    def _send_result(self, request_id: Any, result: Any) -> None:
        self._write({"jsonrpc": "2.0", "id": request_id, "result": result})

    def _send_error(
        self, request_id: Any, code: int, message: str, kind: str | None = None
    ) -> None:
        error: dict[str, Any] = {"code": code, "message": message}
        if kind:
            error["data"] = {"kind": kind}
        self._write({"jsonrpc": "2.0", "id": request_id, "error": error})

    async def call(self, method: str, params: dict[str, Any] | None = None,
                   timeout: float = 60.0) -> Any:
        """Send a plugin→host request and wait for its response."""
        loop = asyncio.get_running_loop()
        self._next_id += 1
        request_id = f"wx-{self._next_id}"
        future: asyncio.Future[Any] = loop.create_future()
        self._pending[str(request_id)] = future
        try:
            self._write(
                {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "method": method,
                    "params": params or {},
                }
            )
            return await asyncio.wait_for(future, timeout=timeout)
        except asyncio.TimeoutError as exc:
            raise RpcError(CODE_CHANNEL_ERROR, f"{method} timed out", "internal") from exc
        finally:
            self._pending.pop(str(request_id), None)

    def close(self) -> None:
        self._closed = True
        for future in self._pending.values():
            if not future.done():
                future.cancel()
        self._pending.clear()

    # ── inbound ───────────────────────────────────────────────────────

    async def serve(
        self, handler: Callable[[str, dict[str, Any]], Awaitable[Any]]
    ) -> None:
        loop = asyncio.get_running_loop()
        queue: asyncio.Queue[bytes | None] = asyncio.Queue()

        def pump() -> None:
            try:
                while not self._closed:
                    line = self.reader.readline()
                    if not line:
                        loop.call_soon_threadsafe(queue.put_nowait, None)
                        return
                    loop.call_soon_threadsafe(queue.put_nowait, line)
            except Exception:  # pragma: no cover - EOF/closed pipe
                loop.call_soon_threadsafe(queue.put_nowait, None)

        thread = threading.Thread(target=pump, name="wxbot-stdin", daemon=True)
        thread.start()
        tasks: set[asyncio.Task[Any]] = set()

        def spawn(coro: Awaitable[Any]) -> None:
            task = asyncio.ensure_future(coro)
            tasks.add(task)
            task.add_done_callback(tasks.discard)

        try:
            while True:
                line = await queue.get()
                if line is None:
                    break
                try:
                    frame = json.loads(line.decode("utf-8"))
                except (UnicodeDecodeError, json.JSONDecodeError) as exc:
                    self.log.error("invalid JSON frame from host: %s", exc)
                    continue
                if not isinstance(frame, dict) or frame.get("jsonrpc") != "2.0":
                    self.log.error("invalid JSON-RPC frame from host")
                    continue
                method = frame.get("method")
                request_id = frame.get("id")
                if isinstance(method, str):
                    params = frame.get("params")
                    if not isinstance(params, dict):
                        params = {}
                    if request_id is None:
                        # The only host→plugin notification in the contract.
                        spawn(self._dispatch_notification(handler, method, params))
                    else:
                        spawn(self._dispatch_request(handler, request_id, method, params))
                    continue
                if request_id is not None:
                    self._resolve_response(request_id, frame)
        finally:
            # Let a request/notification that was already queued finish before
            # tearing down the writer (the host normally closes stdin only
            # after its stop request). Bound the grace period so a hung
            # handler cannot keep the plugin alive.
            if tasks:
                done, pending = await asyncio.wait(tasks, timeout=1.0)
                for task in pending:
                    task.cancel()
                await asyncio.gather(*tasks, return_exceptions=True)
            self.close()

    def _resolve_response(self, request_id: Any, frame: dict[str, Any]) -> None:
        future = self._pending.get(str(request_id))
        if future is None or future.done():
            self.log.debug("response for unknown request id %r", request_id)
            return
        error = frame.get("error")
        if isinstance(error, dict):
            raw_data = error.get("data")
            data: dict[str, Any] = raw_data if isinstance(raw_data, dict) else {}
            future.set_exception(
                RpcError(
                    int(error.get("code", CODE_CHANNEL_ERROR)),
                    str(error.get("message", "channel host error")),
                    data.get("kind"),
                )
            )
            return
        future.set_result(frame.get("result"))

    async def _dispatch_request(
        self,
        handler: Callable[[str, dict[str, Any]], Awaitable[Any]],
        request_id: Any,
        method: str,
        params: dict[str, Any],
    ) -> None:
        try:
            result = await handler(method, params)
        except RpcError as exc:
            self.log.warning("host request %s failed: %s", method, exc.message)
            self._send_error(request_id, exc.code, exc.message, exc.kind)
        except Exception as exc:  # pragma: no cover - defensive boundary
            self.log.exception("host request %s crashed", method)
            self._send_error(request_id, CODE_INTERNAL, str(exc), "internal")
        else:
            self._send_result(request_id, result if result is not None else {})

    async def _dispatch_notification(
        self,
        handler: Callable[[str, dict[str, Any]], Awaitable[Any]],
        method: str,
        params: dict[str, Any],
    ) -> None:
        try:
            await handler(method, params)
        except Exception as exc:  # pragma: no cover - defensive boundary
            # A notification has no response frame; log and keep serving.
            self.log.exception("host notification %s failed: %s", method, exc)
