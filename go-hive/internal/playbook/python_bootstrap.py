"""Hive Python playbook bridge — bootstrap shim (embedded in the Go binary).

The Go runner (internal/playbook/python.go) writes this file to a temporary
directory and starts it with the host interpreter:

    python3 -u <tmpdir>/bootstrap.py

It re-implements the hook surface of the upstream runner
(core/framework/host/playbook/runner.py, ``PlaybookRun``) so an unmodified
``.play.py`` script written for Python Hive behaves the same: converge /
worker / tracker_query / tracker_count / lane / deadletter / log / phase are
available in the script namespace, and ``run()`` returns the same result
envelope (meta / result / logs / deadletter / dispatched).

Only the two callables upstream injects from the colony cross the process
boundary here — ``dispatch_one`` (spawn ONE worker + await its terminal
report) and ``query_rows`` (a tracker SELECT) — as newline-delimited JSON-RPC
over the child's stdin/stdout.

Protocol (one JSON object per line):

    host  -> bridge   {"type": "init", "script": "...", "default_timeout": 600.0}
                      {"type": "run", "args": ..., "concurrency_cap": 8}
    bridge -> host    {"type": "loaded", "meta": {...}, "python_version": "3.9.6"}
                      {"type": "load_error", "error": {kind, message, concise, traceback}}
                      {"id": 1, "method": "query_rows",   "params": {"sql": "..."}}
                      {"id": 2, "method": "dispatch_one", "params": {...}}
                      {"type": "result",    "result": {meta, result, logs, deadletter, dispatched}}
                      {"type": "run_error", "error": {...}}
                      {"type": "fatal",     "error": {...}}
    host  -> bridge   {"id": 1, "result": [...]}  /  {"id": 1, "error": "..."}

Script ``print()`` is redirected to stderr so it cannot corrupt the RPC
stream; stdout belongs to the protocol.
"""

from __future__ import annotations

import asyncio
import inspect
import json
import os
import queue
import sys
import threading
import traceback
from typing import Any

# ---------------------------------------------------------------------------
# stdio: stdout is the host RPC channel
# ---------------------------------------------------------------------------

_PROTOCOL = os.fdopen(os.dup(1), "w", encoding="utf-8", buffering=1)
sys.stdout = sys.stderr  # playbook print() must never corrupt the protocol

for _stream in (sys.stdin, sys.stderr):
    try:
        _stream.reconfigure(encoding="utf-8")  # type: ignore[union-attr]
    except Exception:  # pragma: no cover - non-TextIOWrapper stdin
        pass

_write_lock = threading.Lock()


def _json_default(value: Any) -> Any:
    # Transport must not fail on a result carrying a datetime/set/object:
    # upstream's fast path would raise in json.dumps; we coerce with str() so
    # the run still returns a faithful (stringified) result.
    return str(value)


def _emit(payload: dict[str, Any]) -> None:
    line = json.dumps(payload, ensure_ascii=False, default=_json_default)
    with _write_lock:
        _PROTOCOL.write(line + "\n")
        _PROTOCOL.flush()


# ---------------------------------------------------------------------------
# errors
# ---------------------------------------------------------------------------


class PlaybookError(Exception):
    """Base error for playbook execution."""


class PlaybookScriptError(PlaybookError):
    """The playbook script is malformed (bad meta, missing run, raised)."""


class BridgeError(RuntimeError):
    """The host bridge failed (connection lost, dispatch failure, ...)."""


def _error_detail(exc: BaseException) -> tuple[str, str]:
    """Return ``(concise, full_traceback)`` — a port of the upstream tool's
    ``_error_detail`` so the queen sees the ROOT cause and the playbook line,
    not our wrapper."""
    full = "".join(traceback.format_exception(type(exc), exc, exc.__traceback__)).strip()

    root: BaseException = exc
    while root.__cause__ is not None:
        root = root.__cause__

    lineno: int | None = None
    tb = root.__traceback__
    while tb is not None:
        if tb.tb_frame.f_code.co_filename == "<playbook>":
            lineno = tb.tb_lineno
        tb = tb.tb_next

    where = f" (playbook line {lineno})" if lineno is not None else ""
    concise = f"{type(root).__name__}: {root}{where}"
    return concise, full


def _root_kind_message(exc: BaseException) -> tuple[str, str]:
    root: BaseException = exc
    while root.__cause__ is not None:
        root = root.__cause__
    return type(root).__name__, str(root)


def _error_payload(exc: BaseException) -> dict[str, Any]:
    concise, full = _error_detail(exc)
    kind, message = _root_kind_message(exc)
    return {"kind": kind, "message": message, "concise": concise, "traceback": full}


# ---------------------------------------------------------------------------
# roster primitives (ports of runner.py)
# ---------------------------------------------------------------------------


class DeadLetter:
    """Terminal-failure store for the queen to review. No silent drops."""

    def __init__(self) -> None:
        self._items: list[dict[str, Any]] = []

    def add(self, item: dict[str, Any]) -> None:
        self._items.append(item)

    def list(self) -> list[dict[str, Any]]:
        return list(self._items)

    @property
    def size(self) -> int:
        return len(self._items)

    def __len__(self) -> int:
        return len(self._items)


class _RateGate:
    """Per-lane min-interval throttle — a port of upstream ``_RateGate``.

    ``acquire`` reserves the next slot under a lock, then sleeps OUTSIDE it,
    so a ``concurrency>1`` lane keeps running N workers in parallel while the
    START rate stays bounded.
    """

    def __init__(self, rate_per_min: float) -> None:
        self.min_interval = 60.0 / rate_per_min
        self._next_at: float | None = None
        self._lock = asyncio.Lock()

    async def acquire(self) -> None:
        loop = asyncio.get_event_loop()
        async with self._lock:
            now = loop.time()
            start = now if self._next_at is None else max(now, self._next_at)
            self._next_at = start + self.min_interval
            wait = start - now
        if wait > 0:
            await asyncio.sleep(wait)


async def _maybe_await(value: Any) -> Any:
    if inspect.isawaitable(value):
        return await value
    return value


def _is_failure(result: Any) -> bool:
    if isinstance(result, BaseException):
        return True
    if isinstance(result, dict):
        return result.get("status") not in ("done", None)
    return False


# ---------------------------------------------------------------------------
# JSON-RPC bridge
# ---------------------------------------------------------------------------

_control: "queue.Queue[dict[str, Any]]" = queue.Queue()
_pending_lock = threading.Lock()
_pending: dict[int, Any] = {}
_id_lock = threading.Lock()
_next_id = 0
_loop: asyncio.AbstractEventLoop | None = None


class _SyncSlot:
    """A blocking request slot for the synchronous hooks (tracker_query)."""

    __slots__ = ("event", "result", "error")

    def __init__(self) -> None:
        self.event = threading.Event()
        self.result: Any = None
        self.error: Any = None


def _set_future_exception(fut: "asyncio.Future[Any]", exc: BaseException) -> None:
    if not fut.done():
        fut.set_exception(exc)


def _deliver(slot: Any, msg: dict[str, Any]) -> None:
    if isinstance(slot, _SyncSlot):
        slot.result = msg.get("result")
        slot.error = msg.get("error")
        slot.event.set()
        return
    loop = _loop
    if loop is None:  # pragma: no cover - responses only arrive after run()
        return
    try:
        if "error" in msg:
            loop.call_soon_threadsafe(_set_future_exception, slot, BridgeError(str(msg.get("error"))))
        else:
            loop.call_soon_threadsafe(slot.set_result, msg.get("result"))
    except RuntimeError:  # loop already closed (host stopping)
        pass


def _fail_pending(exc: BaseException) -> None:
    with _pending_lock:
        slots = list(_pending.values())
        _pending.clear()
    for slot in slots:
        _deliver(slot, {"error": str(exc)})


def _reader() -> None:
    """Read host messages until EOF; resolve RPC responses, queue control."""
    while True:
        line = sys.stdin.readline()
        if not line:
            _fail_pending(BridgeError("playbook bridge: host closed the connection"))
            _control.put({"type": "eof"})
            return
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except Exception as exc:
            _fail_pending(BridgeError(f"playbook bridge: malformed host message: {exc}"))
            _control.put({"type": "protocol_error", "message": f"malformed host message: {exc}"})
            return
        if not isinstance(msg, dict):
            _fail_pending(BridgeError("playbook bridge: malformed host message (not an object)"))
            _control.put({"type": "protocol_error", "message": "host message is not a JSON object"})
            return
        if "id" in msg and ("result" in msg or "error" in msg):
            with _pending_lock:
                slot = _pending.pop(msg["id"], None)
            if slot is not None:
                _deliver(slot, msg)
            continue
        _control.put(msg)


def _rpc_send(method: str, params: dict[str, Any]) -> int:
    global _next_id
    with _id_lock:
        _next_id += 1
        request_id = _next_id
    _emit({"id": request_id, "method": method, "params": params})
    return request_id


def _rpc_sync(method: str, params: dict[str, Any]) -> Any:
    slot = _SyncSlot()
    request_id = _rpc_send(method, params)
    with _pending_lock:
        _pending[request_id] = slot
    slot.event.wait()
    if slot.error is not None:
        raise BridgeError(str(slot.error))
    return slot.result


async def _rpc_async(method: str, params: dict[str, Any]) -> Any:
    loop = _loop
    if loop is None:  # pragma: no cover - async hooks only run under run()
        raise BridgeError("playbook bridge: event loop not initialized")
    fut = loop.create_future()
    request_id = _rpc_send(method, params)
    with _pending_lock:
        _pending[request_id] = fut
    return await fut


# ---------------------------------------------------------------------------
# PlaybookRun — the upstream hook surface
# ---------------------------------------------------------------------------


class PlaybookRun:
    """One execution of a playbook. Ports upstream ``PlaybookRun``: the same
    hooks, the same converge/retry/lane/dead-letter semantics; only
    ``dispatch_one`` and ``query_rows`` are remote."""

    def __init__(self, *, run_id: str, default_timeout: float = 600.0, concurrency_cap: int | None = None) -> None:
        self.run_id = run_id
        self._default_timeout = default_timeout
        self._concurrency_cap = concurrency_cap

        self.deadletter = DeadLetter()
        self.logs: list[str] = []
        self._lanes: dict[str, asyncio.Semaphore] = {}
        self._lane_specs: dict[str, dict[str, Any]] = {}
        self._lane_gates: dict[str, _RateGate] = {}
        self._phase: str | None = None
        self.dispatched = 0
        self._meta: dict[str, Any] | None = None
        self._run_fn: Any = None

    # ---- hooks injected into the script namespace --------------------------

    def log(self, message: str) -> None:
        text = str(message)
        self.logs.append(text)

    def phase(self, title: str) -> None:
        self._phase = str(title)
        self.log(f"phase: {title}")

    def lane(self, name: str, *, concurrency: int = 1, rate_per_min: int | None = None) -> None:
        if concurrency < 1:
            concurrency = 1
        self._lanes[name] = asyncio.Semaphore(concurrency)
        self._lane_specs[name] = {"concurrency": concurrency, "rate_per_min": rate_per_min}
        if rate_per_min is not None and rate_per_min > 0:
            self._lane_gates[name] = _RateGate(float(rate_per_min))
            self.log(
                f"lane '{name}': concurrency={concurrency}, rate_per_min={rate_per_min} "
                f"(>= {60.0 / rate_per_min:.1f}s between dispatches)"
            )
        cap = self._concurrency_cap
        if cap is not None and concurrency > cap:
            self.log(
                f"note: lane '{name}' concurrency={concurrency} exceeds the colony cap "
                f"({cap}) — at most {cap} of this account's workers run at once. Raise "
                f"max_concurrent_workers to go higher."
            )

    def tracker_query(self, sql: str) -> list[dict[str, Any]]:
        """Run a tracker SELECT and return rows as dicts. Synchronous — no await."""
        rows = _rpc_sync("query_rows", {"sql": str(sql)})
        if not isinstance(rows, list):
            return []
        return rows

    def tracker_count(self, sql: str) -> int:
        return len(self.tracker_query(sql))

    async def worker(
        self,
        task: str,
        *,
        profile: str | None = None,
        lane: str | None = None,
        skill: str | None = None,
        retries: int = 0,
        backoff: str | None = None,
        timeout: float | None = None,
        schema: dict[str, Any] | None = None,
        phase: str | None = None,
        data: dict[str, Any] | None = None,
        goal: str | None = None,
    ) -> dict[str, Any]:
        """Dispatch one worker job and await its receipt (upstream semantics:
        retries cover transient LIFECYCLE failures only; dead-lettering is
        owned by converge)."""
        task_text = str(task)
        if skill:
            if skill not in task_text:
                task_text = f"{task_text}\n\nFollow the '{skill}' skill."
        timeout = timeout if timeout is not None else self._default_timeout
        attempts = max(1, retries + 1)
        last_error: str | None = None
        receipt: dict[str, Any] = {}

        for attempt in range(1, attempts + 1):
            report = await self._dispatch_guarded(
                task_text,
                data=data,
                profile=profile,
                timeout=timeout,
                lane=lane,
                schema=schema,
                goal=goal,
            )
            self.dispatched += 1
            report = report if isinstance(report, dict) else {}
            status = str(report.get("status", "unknown"))
            receipt = report.get("data") or {}
            if not isinstance(receipt, dict):
                receipt = {}
            if status == "success":
                return {"status": "done", "receipt": receipt, "attempts": attempt, "error": None}
            last_error = report.get("error") or f"worker status={status}"
            if attempt < attempts:
                await self._backoff_sleep(backoff, attempt)

        return {"status": "failed", "receipt": receipt, "attempts": attempts, "error": last_error}

    async def _dispatch_guarded(
        self,
        task_text: str,
        *,
        data: dict[str, Any] | None,
        profile: str | None,
        timeout: float,
        lane: str | None,
        schema: dict[str, Any] | None = None,
        goal: str | None = None,
    ) -> dict[str, Any]:
        params: dict[str, Any] = {
            "task": task_text,
            "data": data,
            "profile": profile,
            "timeout": timeout,
            "schema": schema,
        }
        if goal is not None:
            params["goal"] = goal
        gate = self._lane_gates.get(lane) if lane else None
        if gate is not None:
            await gate.acquire()
        sem = self._lanes.get(lane) if lane else None
        if sem is not None:
            async with sem:
                return await _rpc_async("dispatch_one", params)
        if lane and sem is None:
            self.log(f"warning: lane '{lane}' not declared via lane(); dispatching unthrottled")
        return await _rpc_async("dispatch_one", params)

    @staticmethod
    async def _backoff_sleep(backoff: str | None, attempt: int) -> None:
        if not backoff:
            return
        if backoff == "exp":
            await asyncio.sleep(min(30.0, 2.0**attempt))
        elif backoff == "linear":
            await asyncio.sleep(min(30.0, 2.0 * attempt))

    async def converge(
        self,
        *,
        pending: Any,
        dispatch: Any,
        max_rounds: int = 1,
        circuit_breaker: float | None = None,
        chunk: int | None = None,
    ) -> dict[str, Any]:
        cap = self._concurrency_cap
        if chunk is None and cap is not None:
            chunk = cap
        if cap is not None:
            lane_total = sum(s["concurrency"] for s in self._lane_specs.values())
            if (chunk is not None and chunk > cap) or lane_total > cap:
                self.log(
                    f"note: colony max_concurrent_workers={cap} bounds this run; "
                    f"chunk={chunk} / total lane concurrency={lane_total} above it "
                    f"won't increase actual parallelism — raise the colony cap to go higher."
                )

        rounds_run = 0
        for _round in range(max(1, max_rounds)):
            rows = await _maybe_await(pending())
            if not rows:
                break
            rounds_run += 1
            results = await self._dispatch_round(rows, dispatch, chunk)
            failed = sum(1 for r in results if _is_failure(r))
            self.log(f"round {rounds_run}: {len(rows)} dispatched, {len(rows) - failed} ok, {failed} failed")
            if circuit_breaker is not None and rows and (failed / len(rows)) > circuit_breaker:
                self.log(f"circuit breaker tripped: {failed}/{len(rows)} failed > {circuit_breaker:.0%}; stopping")
                break

        remaining_rows = await _maybe_await(pending())
        for row in remaining_rows:
            self.deadletter.add({"row": row, "reason": f"unresolved after {rounds_run} round(s)"})
        return {
            "rounds": rounds_run,
            "converged": len(remaining_rows) == 0,
            "remaining": len(remaining_rows),
            "dead_lettered": len(remaining_rows),
        }

    async def _dispatch_round(self, rows: list[Any], dispatch: Any, chunk: int | None) -> list[Any]:
        sem = asyncio.Semaphore(chunk) if chunk and chunk > 0 else None

        async def run_one(row: Any, idx: int) -> Any:
            if sem is not None:
                async with sem:
                    return await _maybe_await(dispatch(row, idx))
            return await _maybe_await(dispatch(row, idx))

        return await asyncio.gather(
            *(run_one(row, i) for i, row in enumerate(rows)),
            return_exceptions=True,
        )

    # ---- script execution --------------------------------------------------

    def _namespace(self) -> dict[str, Any]:
        # Full builtins: the queen is a trusted author in her own process
        # (same posture as upstream — exec in-process is not a security boundary).
        return {
            "converge": self.converge,
            "worker": self.worker,
            "tracker_query": self.tracker_query,
            "tracker_count": self.tracker_count,
            "lane": self.lane,
            "deadletter": self.deadletter,
            "log": self.log,
            "phase": self.phase,
        }

    def load(self, script: str) -> Any:
        """Compile + exec the script body and validate it (synchronous)."""
        namespace = self._namespace()
        try:
            code = compile(script, "<playbook>", "exec")
            exec(code, namespace)  # noqa: S102 - intentional script execution
        except Exception as exc:
            raise PlaybookScriptError(f"playbook body failed to load: {exc}") from exc

        meta = namespace.get("meta")
        if not isinstance(meta, dict) or not meta.get("name"):
            raise PlaybookScriptError("playbook must define a `meta` dict with at least a 'name'")
        run_fn = namespace.get("run")
        if not callable(run_fn):
            raise PlaybookScriptError("playbook must define `async def run(args)`")

        self._meta = meta
        self._run_fn = run_fn
        return run_fn

    async def run_loaded(self, args: Any = None) -> dict[str, Any]:
        try:
            result = self._run_fn(args)
            result = await _maybe_await(result)
        except PlaybookScriptError:
            raise
        except Exception as exc:
            raise PlaybookScriptError(f"playbook run() raised: {exc}") from exc

        return {
            "meta": self._meta,
            "result": result,
            "logs": self.logs,
            "deadletter": self.deadletter.list(),
            "dispatched": self.dispatched,
        }


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


async def _execute_run(run: PlaybookRun, args: Any) -> dict[str, Any]:
    global _loop
    _loop = asyncio.get_running_loop()
    return await run.run_loaded(args)


def _wait_for_control() -> dict[str, Any]:
    while True:
        msg = _control.get()
        if msg.get("type") == "eof":
            return msg
        if msg.get("type") == "protocol_error":
            return msg
        return msg


def main() -> int:
    if sys.version_info < (3, 8):
        _emit(
            {
                "type": "fatal",
                "error": {
                    "kind": "PythonVersionError",
                    "message": f"Python 3.8+ is required to run playbooks; got {sys.version.split()[0]}",
                    "concise": f"PythonVersionError: Python 3.8+ required, got {sys.version.split()[0]}",
                    "traceback": "",
                },
            }
        )
        return 2

    first = sys.stdin.readline()
    if not first:
        return 1
    try:
        init = json.loads(first)
    except Exception as exc:
        _emit(
            {
                "type": "fatal",
                "error": {
                    "kind": "BridgeProtocolError",
                    "message": f"malformed init message: {exc}",
                    "concise": f"BridgeProtocolError: malformed init message: {exc}",
                    "traceback": "",
                },
            }
        )
        return 2
    if not isinstance(init, dict) or init.get("type") != "init":
        _emit(
            {
                "type": "fatal",
                "error": {
                    "kind": "BridgeProtocolError",
                    "message": "expected an init message first",
                    "concise": "BridgeProtocolError: expected an init message first",
                    "traceback": "",
                },
            }
        )
        return 2

    try:
        default_timeout = float(init.get("default_timeout") or 600.0)
    except (TypeError, ValueError):
        default_timeout = 600.0
    run = PlaybookRun(run_id=str(init.get("run_id") or "playbook"), default_timeout=default_timeout)
    if init.get("concurrency_cap") is not None:
        # Mirrors upstream PlaybookRun(concurrency_cap=...) construction, so a
        # module-scope lane() call sees the cap during load().
        run._concurrency_cap = int(init.get("concurrency_cap"))

    threading.Thread(target=_reader, name="playbook-rpc", daemon=True).start()

    try:
        run.load(str(init.get("script") or ""))
    except PlaybookScriptError as exc:
        _emit({"type": "load_error", "error": _error_payload(exc)})
        return 0
    except BaseException as exc:  # never leave the host without an answer
        _emit({"type": "load_error", "error": _error_payload(exc)})
        return 0

    _emit({"type": "loaded", "meta": run._meta, "python_version": sys.version.split()[0]})

    control = _wait_for_control()
    if control.get("type") == "eof":
        return 1
    if control.get("type") == "protocol_error":
        _emit(
            {
                "type": "fatal",
                "error": {
                    "kind": "BridgeProtocolError",
                    "message": str(control.get("message") or "bad host message"),
                    "concise": f"BridgeProtocolError: {control.get('message') or 'bad host message'}",
                    "traceback": "",
                },
            }
        )
        return 2
    if control.get("type") != "run":
        _emit(
            {
                "type": "fatal",
                "error": {
                    "kind": "BridgeProtocolError",
                    "message": f"unexpected control message {control.get('type')!r}",
                    "concise": f"BridgeProtocolError: unexpected control message {control.get('type')!r}",
                    "traceback": "",
                },
            }
        )
        return 2

    run_cap = control.get("concurrency_cap")
    if run_cap is not None:
        run._concurrency_cap = run_cap

    try:
        out = asyncio.run(_execute_run(run, control.get("args")))
    except BaseException as exc:  # includes asyncio.CancelledError and BridgeError
        payload = _error_payload(exc)
        payload["logs"] = run.logs
        payload["dispatched"] = run.dispatched
        payload["deadletter"] = run.deadletter.list()
        _emit({"type": "run_error", "error": payload})
        return 0

    _emit({"type": "result", "result": out})
    return 0


if __name__ == "__main__":
    sys.exit(main())
