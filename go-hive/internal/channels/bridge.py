"""Run one installed ``hive.channels`` entry point behind the Go channel host.

Upstream ``framework.server.channels`` imports the factory named by a
``hive.channels`` entry point and calls ``factory(ChannelHost)`` inside the
server's event loop.  A Go ``hive serve`` has no Python interpreter of its own,
so this bridge runs the same factory in a child interpreter and gives it a host
object whose methods cross to the Go host as newline-delimited JSON-RPC
requests -- the exact stdio contract ``internal/channels`` already speaks for Go
plugins (protocol ``hive-channel/1``).

What stays the same, as observed by the channel:

* the entry-point group (``hive.channels``), the entry-point name, and
  ``factory(host)`` returning an object with async ``start()`` / ``stop()``;
* ``host.name``, ``host.config`` (the channel's section of
  ``configuration.json``) and ``host.state_dir``;
* ``await host.open_session(...)``, ``await host.send(...)``,
  ``await host.judge(...)``, ``host.subscribe(session_id, handler)``,
  ``host.unsubscribe(session_id)`` and ``host.set_title(...)``;
* ``QueenOutput`` fields and kinds, with one handler call at a time, in order;
* ``subscribe`` is written to the host immediately, and a ``send`` issued after
  it in the same session waits for the subscription, so subscribe-then-send
  ordering matches upstream;
* the 30-second ``start()`` / ``stop()`` budgets and ``ChannelError`` for a
  refused session operation.

Process-boundary differences (unavoidable):

* host calls are JSON round trips to another process rather than object calls;
* ``subscribe`` and ``set_title`` keep upstream's synchronous signatures, so a
  host refusal is logged instead of raised at the call site -- making them
  awaitable would change the channel-facing API;
* stdout is the protocol channel: this bridge points ``sys.stdout`` at stderr
  before loading the plugin, so ``print()`` and library chatter become channel
  logs instead of corrupting a frame;
* logging is a stderr stream handler; the Go host forwards each line as
  ``channel <name>: ...``;
* entry-point discovery runs in the configured interpreter, so the channel
  package must be installed for *that* interpreter (or on its ``python_path``).

Run ``python hive_channel_bridge.py --list`` to print the installed
``hive.channels`` entry points as one JSON line.

Parity: framework/server/channels.py::start_channels
Parity: framework/server/channels.py::_installed
Parity: framework/server/channels.py::ChannelHost
Parity: framework/server/channels.py::QueenOutput
Parity: framework/server/channels.py::Channel
"""

from __future__ import annotations

import asyncio
import base64
import importlib.metadata
import json
import logging
import os
import sys
import threading
import traceback
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Optional

ENTRY_POINT_GROUP = "hive.channels"
# Upstream's asyncio.wait_for budgets for Channel.start()/stop().
_START_TIMEOUT_S = 30.0
_STOP_TIMEOUT_S = 30.0
# Outputs that reach the bridge before the subscribe answer names their remote
# subscription are buffered here. Bounded like the host's queue: newest wins.
_EARLY_OUTPUT_LIMIT = 64

_PROTOCOL_VERSION = "hive-channel/1"
_BRIDGE_VERSION = "hive-python-bridge/1"

logger = logging.getLogger("hive.channels.bridge")


class _LocalChannelError(RuntimeError):
    """Stand-in when the Hive core is not importable in this interpreter."""


try:  # Share the real class so ``except ChannelError`` around hive still works.
    from framework.server.channels import ChannelError as _ChannelErrorBase
except Exception:  # pragma: no cover - the standalone bridge and tests
    _ChannelErrorBase = _LocalChannelError


class ChannelError(_ChannelErrorBase):
    """A queen session refused a channel operation (upstream's ChannelError)."""

    def __init__(self, message: str, kind: Optional[str] = None) -> None:
        super().__init__(message)
        #: data.kind of the Go host's refusal (a Go-only refinement).
        self.kind = kind


class ProtocolError(RuntimeError):
    """The host broke the stdio framing contract."""


try:  # Share the real dataclass so ``isinstance(output, QueenOutput)`` holds.
    from framework.server.channels import QueenOutput as QueenOutput  # type: ignore[no-redef]
except Exception:  # pragma: no cover - the standalone bridge and tests

    @dataclass(frozen=True)
    class QueenOutput:  # type: ignore[no-redef]
        """Something the queen said in a channel's session.

        ``kind`` is message, question, error, files or idle; ``files`` are
        absolute paths inside the session's data/attachments.

        Parity: framework/server/channels.py::QueenOutput
        """

        session_id: str
        kind: str
        text: str = ""
        questions: List[Dict[str, Any]] = field(default_factory=list)
        files: List[Path] = field(default_factory=list)


def _installed() -> Dict[str, Any]:
    """Installed ``hive.channels`` entry points by name; the last one wins.

    Upstream builds ``{ep.name: ep}`` from ``entry_points(group=...)``. Python
    3.9 returns a group mapping instead of the 3.10+ selectable collection, so
    both shapes are accepted.

    Parity: framework/server/channels.py::_installed
    """
    entries = importlib.metadata.entry_points()
    if hasattr(entries, "select"):
        selected = entries.select(group=ENTRY_POINT_GROUP)
    else:  # pragma: no cover - Python 3.9
        selected = entries.get(ENTRY_POINT_GROUP, ())
    return {entry_point.name: entry_point for entry_point in selected}


def _distribution_name(entry_point: Any) -> str:
    distribution = getattr(entry_point, "dist", None)
    name = getattr(distribution, "name", None) if distribution is not None else None
    return str(name) if name else ""


class _Subscription:
    """Turns one session's output stream into in-order handler calls.

    Parity: framework/server/channels.py::_Subscription
    """

    def __init__(self, host: "ChannelHost", session_id: str, handler: Any) -> None:
        self.host = host
        self.session_id = session_id
        self.handler = handler
        self.remote_id: Optional[str] = None
        self.closed = False
        self.queue: "asyncio.Queue[Any]" = asyncio.Queue()
        self._worker: Optional[asyncio.Task] = None
        # A send issued after subscribe in the same session waits for this, so
        # the host cannot deliver the queen's first outputs into a subscription
        # that does not exist yet (upstream's subscribe is synchronous).
        self.ready: "asyncio.Future[Any]" = host._loop.create_future()

    def start(self) -> None:
        self._worker = self.host._loop.create_task(self._deliver())
        try:
            key, future = self.host._dispatch_request(
                "subscribe", {"session_id": self.session_id}
            )
        except Exception as error:
            logger.warning(
                "channel %r: subscribe %r failed: %s", self.host.name, self.session_id, error
            )
            self._finish_ready()
            self.close()
            return
        self.host._await_request(
            key, future, self._subscribed, "subscribe %r" % (self.session_id,), self._finish_ready
        )

    def _subscribed(self, result: Dict[str, Any]) -> None:
        remote_id = str(result.get("subscription_id") or "")
        if not remote_id:
            logger.warning(
                "channel %r: subscribe %r returned no subscription id",
                self.host.name,
                self.session_id,
            )
            self._finish_ready()
            self.close()
            return
        if self.closed:
            # Unsubscribed while the answer was in flight; release the host's
            # subscription instead of leaking it.
            self._finish_ready()
            self.host._fire_request(
                "unsubscribe", {"subscription_id": remote_id}, "unsubscribe %r" % remote_id
            )
            return
        self.remote_id = remote_id
        self.host.attach_remote(self, remote_id)
        self._finish_ready()

    def _finish_ready(self) -> None:
        if not self.ready.done():
            self.ready.set_result(None)

    def enqueue(self, params: Dict[str, Any]) -> None:
        if self.closed:
            return
        session_id = params.get("session_id")
        session_id = str(session_id) if session_id else self.session_id
        if session_id != self.session_id:
            self.host.move_subscription(self, session_id)
        questions = params.get("questions")
        files = [
            Path(path)
            for path in (params.get("files") or [])
            if isinstance(path, str) and path
        ]
        output = QueenOutput(
            session_id=session_id,
            kind=str(params.get("kind") or "message"),
            text=str(params.get("text") or ""),
            questions=list(questions) if isinstance(questions, list) else [],
            files=files,
        )
        self.queue.put_nowait(output)

    async def _deliver(self) -> None:
        # Handlers do network I/O; calling them here instead of inside the
        # host's reader keeps a slow chat app from stalling the protocol.
        while True:
            output = await self.queue.get()
            try:
                await self.handler(output)
            except asyncio.CancelledError:
                raise
            except Exception:
                logger.exception(
                    "channel: handler failed for a %s in session %s",
                    output.kind,
                    output.session_id,
                )

    def close(self) -> None:
        if self.closed:
            return
        self.closed = True
        self._finish_ready()
        if self._worker is not None:
            self._worker.cancel()
        if self.remote_id is not None:
            self.host._fire_request(
                "unsubscribe",
                {"subscription_id": self.remote_id},
                "unsubscribe %r" % self.remote_id,
            )
        self.host.forget_subscription(self)

    def detach(self) -> None:
        """Drop the local stream without asking the host (it is shutting down)."""
        if self.closed:
            return
        self.closed = True
        self._finish_ready()
        if self._worker is not None:
            self._worker.cancel()
        self.host.forget_subscription(self)


class ChannelHost:
    """What the Go host gives one channel: settings, state dir, queen sessions.

    Parity: framework/server/channels.py::ChannelHost
    """

    def __init__(self, bridge: "Bridge", name: str, config: Dict[str, Any], state_dir: str) -> None:
        self._bridge = bridge
        self.name = name
        self.config = config
        self._state_dir = Path(state_dir) if state_dir else Path(name)
        self._loop = asyncio.get_running_loop()
        self._next_id = 0
        self._pending: Dict[str, "asyncio.Future[Any]"] = {}
        self._subs: Dict[str, _Subscription] = {}
        self._remote_subs: Dict[str, _Subscription] = {}
        self._early: Dict[str, List[Dict[str, Any]]] = {}
        self._tasks: "set[asyncio.Task[Any]]" = set()
        self._closed = False

    @property
    def state_dir(self) -> Path:
        """``$HIVE_HOME/channels/<name>``; created on access like upstream."""
        self._state_dir.mkdir(parents=True, exist_ok=True)
        return self._state_dir

    async def open_session(
        self, queen_id: str, *, session_id: Optional[str] = None, title: Optional[str] = None
    ) -> str:
        """The session for one conversation with ``queen_id``, ready for send.

        Parity: framework/server/channels.py::ChannelHost.open_session
        """
        params: Dict[str, Any] = {"queen_id": queen_id}
        if session_id:
            params["session_id"] = session_id
        if title:
            params["title"] = title
        result = await self._request("open_session", params)
        return str(result.get("session_id") or "")

    async def send(
        self,
        session_id: str,
        text: str,
        *,
        images: Any = (),
        display_text: Optional[str] = None,
        correlation_id: Optional[str] = None,
    ) -> str:
        """Send a user message into a session; returns the session that took it.

        Parity: framework/server/channels.py::ChannelHost.send
        """
        params: Dict[str, Any] = {"session_id": session_id, "text": text}
        encoded = [base64.b64encode(bytes(image)).decode("ascii") for image in images]
        if encoded:
            params["images"] = encoded
        if display_text is not None:
            params["display_text"] = display_text
        if correlation_id is not None:
            params["correlation_id"] = correlation_id
        # A subscription registered just before this send must exist in the Go
        # host before the message can produce the queen's first output.
        sub = self._subs.get(session_id)
        if sub is not None and not sub.ready.done():
            await sub.ready
        result = await self._request("send", params)
        return str(result.get("session_id") or session_id)

    async def judge(
        self, question: str, state: Dict[str, Any], *, criteria: Optional[Dict[str, str]] = None
    ) -> Optional[float]:
        """The judge slot's probability that the answer to question is yes.

        Parity: framework/server/channels.py::ChannelHost.judge
        """
        params: Dict[str, Any] = {"question": question, "state": state}
        if criteria:
            params["criteria"] = criteria
        result = await self._request("judge", params)
        probability = result.get("probability")
        return float(probability) if isinstance(probability, (int, float)) else None

    def subscribe(self, session_id: str, handler: Any) -> None:
        """Call ``handler`` with what the queen says in session_id from now on.

        Upstream replaces the previous handler for the same session and
        delivers one output at a time, in order. The Go round trip happens in
        the background so this keeps upstream's synchronous signature.

        Parity: framework/server/channels.py::ChannelHost.subscribe
        """
        self.unsubscribe(session_id)
        sub = _Subscription(self, session_id, handler)
        self._subs[session_id] = sub
        sub.start()

    def unsubscribe(self, session_id: str) -> None:
        """Stop hearing session_id.

        Parity: framework/server/channels.py::ChannelHost.unsubscribe
        """
        sub = self._subs.pop(session_id, None)
        if sub is not None:
            sub.close()

    def set_title(self, session_id: str, title: str) -> None:
        """Name a session in the dashboard; an empty title clears it.

        Upstream raises ChannelError for an unknown session; here the call is
        synchronous and the round trip is scheduled, so a refusal is logged.

        Parity: framework/server/channels.py::ChannelHost.set_title
        """
        self._fire_request(
            "set_title", {"session_id": session_id, "title": title}, "set_title"
        )

    def close(self) -> None:
        """Drop every subscription and stop watching sessions.

        Parity: framework/server/channels.py::ChannelHost.close
        """
        if self._closed:
            return
        self._closed = True
        for sub in list(self._subs.values()):
            sub.detach()
        self._subs.clear()
        self._remote_subs.clear()
        self._early.clear()

    # -- transport -----------------------------------------------------

    def _dispatch_request(self, method: str, params: Dict[str, Any]) -> Any:
        """Write one request frame now and return its (key, future).

        Writing at call time (instead of when a scheduled task first runs)
        keeps the channel's call order on the wire: a subscribe issued before a
        send reaches the host first.
        """
        if self._closed:
            raise ChannelError("channel host is closed")
        self._next_id += 1
        request_id = "bridge-%d" % self._next_id
        future: "asyncio.Future[Any]" = self._loop.create_future()
        self._pending[request_id] = future
        try:
            self._bridge.write(
                {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}
            )
        except BaseException:
            self._pending.pop(request_id, None)
            raise
        return request_id, future

    async def _request(self, method: str, params: Dict[str, Any]) -> Dict[str, Any]:
        request_id, future = self._dispatch_request(method, params)
        try:
            return await future
        finally:
            self._pending.pop(request_id, None)

    def _await_request(
        self,
        request_id: str,
        future: "asyncio.Future[Any]",
        on_result: Any,
        label: str,
        on_error: Any = None,
    ) -> None:
        async def settle() -> None:
            try:
                result = await future
            except asyncio.CancelledError:
                raise
            except Exception as error:
                logger.warning("channel %r: %s failed: %s", self.name, label, error)
                if on_error is not None:
                    on_error()
                return
            finally:
                self._pending.pop(request_id, None)
            on_result(result)

        self._schedule(settle())

    def _fire_request(self, method: str, params: Dict[str, Any], label: str) -> None:
        """Send a request whose result only matters for logging (upstream's
        synchronous set_title/unsubscribe have no await point to surface it)."""
        try:
            request_id, future = self._dispatch_request(method, params)
        except Exception as error:
            logger.warning("channel %r: %s failed: %s", self.name, label, error)
            return
        self._await_request(request_id, future, lambda result: None, label)

    def handle_response(self, frame: Dict[str, Any]) -> None:
        raw_id = frame.get("id")
        key = raw_id if isinstance(raw_id, str) else json.dumps(raw_id, sort_keys=True)
        future = self._pending.pop(key, None)
        if future is None or future.done():
            logger.warning("channel %r: response for unknown bridge request %s", self.name, key)
            return
        error = frame.get("error")
        if isinstance(error, dict):
            data = error.get("data")
            kind = data.get("kind") if isinstance(data, dict) else None
            message = str(error.get("message") or "channel host error")
            future.set_exception(ChannelError(message, kind))
            return
        result = frame.get("result")
        future.set_result(result if isinstance(result, dict) else {})

    def handle_output(self, params: Dict[str, Any]) -> None:
        if not isinstance(params, dict):
            return
        remote_id = params.get("subscription_id")
        if not isinstance(remote_id, str) or not remote_id:
            return
        sub = self._remote_subs.get(remote_id)
        if sub is None:
            buffered = self._early.setdefault(remote_id, [])
            buffered.append(params)
            if len(buffered) > _EARLY_OUTPUT_LIMIT:
                del buffered[0]
            return
        sub.enqueue(params)

    def attach_remote(self, sub: _Subscription, remote_id: str) -> None:
        self._remote_subs[remote_id] = sub
        for params in self._early.pop(remote_id, []):
            sub.enqueue(params)

    def move_subscription(self, sub: _Subscription, session_id: str) -> None:
        if self._subs.get(sub.session_id) is sub:
            del self._subs[sub.session_id]
        sub.session_id = session_id
        self._subs[session_id] = sub

    def forget_subscription(self, sub: _Subscription) -> None:
        if self._subs.get(sub.session_id) is sub:
            del self._subs[sub.session_id]
        if sub.remote_id is not None and self._remote_subs.get(sub.remote_id) is sub:
            del self._remote_subs[sub.remote_id]

    def _schedule(self, coroutine: Any) -> None:
        task = self._loop.create_task(coroutine)
        self._tasks.add(task)

        def finished(completed: "asyncio.Task[Any]") -> None:
            self._tasks.discard(completed)
            if completed.cancelled():
                return
            error = completed.exception()
            if error is not None:
                logger.warning("channel %r: %s", self.name, error)

        task.add_done_callback(finished)

    def abort_pending(self) -> None:
        for future in list(self._pending.values()):
            if not future.done():
                future.cancel()
        self._pending.clear()

    async def drain(self, timeout: float = 2.0) -> None:
        if not self._tasks:
            return
        _, pending = await asyncio.wait(list(self._tasks), timeout=timeout)
        for task in pending:
            task.cancel()


_SHUTDOWN = object()


class Bridge:
    """The stdio half: host requests in, host calls and outputs translated."""

    def __init__(self, entry_point: str, stdin: Any, stdout: Any) -> None:
        self.entry_point = entry_point
        self._stdin = stdin
        self._stdout = stdout
        self._host: Optional[ChannelHost] = None
        self._channel: Any = None
        self._tasks: "set[asyncio.Task[Any]]" = set()
        self._inbox: Optional["asyncio.Queue[Any]"] = None
        self._failed = False

    def _label(self) -> str:
        return self._host.name if self._host is not None else self.entry_point

    def write(self, frame: Dict[str, Any]) -> None:
        line = json.dumps(frame, ensure_ascii=False, separators=(",", ":")).encode("utf-8") + b"\n"
        self._stdout.write(line)
        self._stdout.flush()

    def _respond(self, request_id: Any, result: Any) -> None:
        self.write({"jsonrpc": "2.0", "id": request_id, "result": result})

    def _respond_error(
        self, request_id: Any, message: str, kind: str = "internal", code: int = -32000
    ) -> None:
        self.write(
            {
                "jsonrpc": "2.0",
                "id": request_id,
                "error": {"code": code, "message": message, "data": {"kind": kind}},
            }
        )

    def request_shutdown(self) -> None:
        if self._inbox is not None:
            self._inbox.put_nowait(_SHUTDOWN)

    async def run(self) -> int:
        self._inbox = asyncio.Queue()
        loop = asyncio.get_running_loop()
        thread = threading.Thread(
            target=_read_frames,
            args=(self._stdin, loop, self._inbox),
            name="hive-channel-bridge-reader",
            daemon=True,
        )
        thread.start()
        try:
            while True:
                frame = await self._inbox.get()
                if frame is _SHUTDOWN:
                    break
                if frame is None:
                    break
                if isinstance(frame, Exception):
                    logger.error("channel %r: %s", self._label(), frame)
                    self._failed = True
                    break
                if not isinstance(frame, dict):
                    logger.warning("channel %r: ignoring non-object host frame", self._label())
                    continue
                self._dispatch(frame)
        finally:
            if self._host is not None:
                self._host.close()
                self._host.abort_pending()
                await self._host.drain()
            for task in list(self._tasks):
                task.cancel()
            self._tasks.clear()
        return 1 if self._failed else 0

    def _dispatch(self, frame: Dict[str, Any]) -> None:
        method = frame.get("method")
        raw_id = frame.get("id")
        has_id = isinstance(raw_id, str) or (
            isinstance(raw_id, int) and not isinstance(raw_id, bool)
        )
        if method == "output" and not has_id:
            if self._host is not None:
                params = frame.get("params")
                self._host.handle_output(params if isinstance(params, dict) else {})
            return
        if isinstance(method, str) and method and has_id:
            task = asyncio.get_running_loop().create_task(self._serve_request(frame))
            self._tasks.add(task)
            task.add_done_callback(self._tasks.discard)
            return
        if isinstance(method, str) and method:
            logger.info("channel %r: ignoring host notification %r", self._label(), method)
            return
        if self._host is not None and ("result" in frame or "error" in frame):
            self._host.handle_response(frame)
            return
        logger.warning("channel %r: ignoring malformed host frame", self._label())

    async def _serve_request(self, frame: Dict[str, Any]) -> None:
        request_id = frame.get("id")
        method = frame.get("method")
        params = frame.get("params")
        if not isinstance(params, dict):
            params = {}
        try:
            if method == "hello":
                result = self._hello(params)
            elif method == "start":
                result = await self._start()
            elif method == "stop":
                result = await self._stop()
                self._respond(request_id, result)
                self.request_shutdown()
                return
            else:
                self._respond_error(
                    request_id,
                    'method "%s" is not supported by the Hive channel bridge' % method,
                    "invalid_params",
                    -32601,
                )
                return
        except Exception as error:
            logger.error("channel %r: %s failed: %s", self._label(), method, error)
            logger.debug("%s", traceback.format_exc())
            self._respond_error(
                request_id,
                _describe_error(error),
                "channel_error" if isinstance(error, ChannelError) else "internal",
            )
            if method == "start":
                self._failed = True
                self.request_shutdown()
            return
        self._respond(request_id, result)

    def _hello(self, params: Dict[str, Any]) -> Dict[str, Any]:
        protocol = params.get("protocol")
        if protocol != _PROTOCOL_VERSION:
            raise ChannelError(
                "unsupported protocol %r (want %r)" % (protocol, _PROTOCOL_VERSION)
            )
        name = str(params.get("name") or self.entry_point)
        config = params.get("config")
        state_dir = params.get("state_dir")
        self._host = ChannelHost(
            self,
            name,
            config if isinstance(config, dict) else {},
            str(state_dir or ""),
        )
        return {"plugin_version": _BRIDGE_VERSION, "capabilities": ["entry-point"]}

    async def _start(self) -> Dict[str, Any]:
        if self._host is None:
            raise ChannelError("start before hello")
        if self._channel is not None:
            return {}
        entry_point = _find_entry_point(self.entry_point)
        logger.info(
            "channel %r: loading %s entry point %s", self._host.name, ENTRY_POINT_GROUP, entry_point.value
        )
        factory = entry_point.load()
        if not callable(factory):
            raise TypeError(
                "entry point %r does not resolve to a callable factory" % self.entry_point
            )
        self._channel = factory(self._host)
        if self._channel is None or not hasattr(self._channel, "start"):
            raise TypeError(
                "factory %r returned no channel with a start() method" % self.entry_point
            )
        try:
            await asyncio.wait_for(self._channel.start(), _START_TIMEOUT_S)
        except asyncio.TimeoutError:
            raise TimeoutError(
                "start() did not return within %g seconds" % _START_TIMEOUT_S
            ) from None
        return {}

    async def _stop(self) -> Dict[str, Any]:
        channel = self._channel
        self._channel = None
        if channel is not None:
            try:
                await asyncio.wait_for(channel.stop(), _STOP_TIMEOUT_S)
            except asyncio.CancelledError:
                raise
            except Exception:
                # Upstream logs a stop failure and keeps shutting down.
                logger.exception("channel %r failed to stop cleanly", self._label())
        if self._host is not None:
            self._host.close()
            await self._host.drain()
        return {}


def _describe_error(error: BaseException) -> str:
    message = str(error)
    if message:
        return "%s: %s" % (type(error).__name__, message)
    return type(error).__name__


def _find_entry_point(name: str) -> Any:
    entry_point = _installed().get(name)
    if entry_point is None:
        raise LookupError(
            "no installed %r entry point named %r (discovery runs in the configured "
            "interpreter; check its site-packages and python_path)" % (ENTRY_POINT_GROUP, name)
        )
    return entry_point


def _post(loop: "asyncio.AbstractEventLoop", inbox: "asyncio.Queue[Any]", item: Any) -> None:
    try:
        loop.call_soon_threadsafe(inbox.put_nowait, item)
    except RuntimeError:  # the loop is already closed; the process is exiting
        pass


def _read_frames(stream: Any, loop: "asyncio.AbstractEventLoop", inbox: "asyncio.Queue[Any]") -> None:
    try:
        for raw in stream:
            line = raw.strip()
            if not line:
                continue
            try:
                frame = json.loads(line)
            except ValueError as error:
                _post(loop, inbox, ProtocolError("malformed JSON frame from the host: %s" % error))
                return
            _post(loop, inbox, frame)
    except Exception as error:  # pragma: no cover - pipe errors during shutdown
        _post(loop, inbox, ProtocolError("reading the host stream failed: %s" % error))
        return
    _post(loop, inbox, None)


def _configure_logging() -> None:
    level = getattr(logging, (os.environ.get("HIVE_CHANNEL_LOG_LEVEL") or "INFO").upper(), logging.INFO)
    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(logging.Formatter("%(levelname)s %(name)s: %(message)s"))
    handler._hive_channel_bridge = True  # type: ignore[attr-defined]
    root = logging.getLogger()
    for existing in list(root.handlers):
        if getattr(existing, "_hive_channel_bridge", False):
            root.removeHandler(existing)
    root.addHandler(handler)
    root.setLevel(level)


def _list_entry_points() -> int:
    try:
        entries = _installed()
    except Exception as error:
        sys.stderr.write(
            "hive channel bridge: entry point discovery failed: %s: %s\n"
            % (type(error).__name__, error)
        )
        return 1
    payload = [
        {
            "name": entry_point.name,
            "value": entry_point.value,
            "distribution": _distribution_name(entry_point),
        }
        for entry_point in sorted(entries.values(), key=lambda entry: entry.name)
    ]
    sys.stdout.write(json.dumps(payload, ensure_ascii=False) + "\n")
    sys.stdout.flush()
    return 0


def main(argv: List[str]) -> int:
    if len(argv) >= 2 and argv[1] in ("--list", "-l"):
        return _list_entry_points()
    if len(argv) < 2 or not argv[1].strip():
        sys.stderr.write("hive channel bridge: missing hive.channels entry point name\n")
        return 2
    # stdout belongs to the protocol from here on; plugin prints become logs.
    protocol_out = sys.stdout.buffer
    sys.stdout = sys.stderr
    _configure_logging()
    bridge = Bridge(argv[1].strip(), sys.stdin.buffer, protocol_out)
    try:
        return asyncio.run(bridge.run())
    except KeyboardInterrupt:  # pragma: no cover - host shutdown is a stop request
        return 130


if __name__ == "__main__":
    sys.exit(main(sys.argv))
