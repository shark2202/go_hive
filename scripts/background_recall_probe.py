#!/usr/bin/env python3
"""Background recall-mode timing probe — upstream (Hive 1.0.15) pure-function evidence.

The registered residual asks what ``memory_recall.mode="background"`` actually
does in the reference tree: is the pick made before the turn (blocking) or
while she answers? Where does the block land? What bounds it in time?

This probe runs the REAL reference modules in-process (no network, no model
endpoint) and records, as JSON:

  E. ``AgentLoop`` injection mechanics (the two mechanisms
     ``_recall_on_user_input`` composes):
       E1  a passive injection queued before the user's message is drained
           AFTER it (order inside the turn's first request)
       E2  a passive injection while the loop is parked does not start a
           turn; it is delivered with the NEXT message
       E3  a passive injection arriving while an iteration is in flight
           lands in the NEXT iteration's request (mid-turn)
  F. ``recall_selector`` timing:
       F1  the two scope picks run concurrently (asyncio.gather)
       F2  no timeout inside the selector — the pick's full duration is
           awaited by ``build_scoped_recall_blocks``
       F3  the session-start seed is capped: ``wait_for(..., 3.0)``
           raises TimeoutError on a 5s pick and leaves the cache empty
       F4  seed default ``max_results=5`` vs the refresh's 3 (profile pins)
       F5  an empty query still reaches the model through
           ``build_scoped_recall_blocks`` (the caller's guard is what
           skips it)
  H. ``EventBus.publish`` awaits handlers (0.25s handler → publish ~0.25s);
     a handler that spawns a task returns fast while the task completes
     later — why the immediate injection carries no model call.

Every case is a (subject, expected, observed, pass) record; the whole run is
written to go_hive/docs/e2e-assets/memory-notes-1015/bg-recall-probe.json.

Usage (repo root of a checkout):
    PYTHONPATH=core:tools/src /Users/mac/hive/.venv/bin/python \
        go_hive/scripts/background_recall_probe.py
"""

from __future__ import annotations

import asyncio
import json
import sys
import tempfile
import time
from collections.abc import AsyncIterator
from pathlib import Path
from typing import Any
from unittest.mock import MagicMock

CASES: list[dict] = []


def record(case: str, subject: str, expected: Any, observed: Any, ok: bool, note: str = "") -> None:
    CASES.append(
        {
            "case": case,
            "subject": subject,
            "expected": expected,
            "observed": observed,
            "pass": bool(ok),
            "note": note,
        }
    )


# ---------------------------------------------------------------------------
# Shared harness: a scripted streaming LLM + minimal NodeContext
# ---------------------------------------------------------------------------


class Event:
    """One streamed completion: optional gate + the events to yield."""

    def __init__(self, events: list, gate: asyncio.Event | None = None) -> None:
        self.events = events
        self.gate = gate


class ScriptedLLM:
    """A fake provider that records every stream() call and its messages."""

    model = "probe-mock"

    def __init__(self, script: list[Event]) -> None:
        self.script = script
        self.calls: list[dict] = []
        self.started = asyncio.Event()
        self._index = 0

    async def stream(self, messages, system="", tools=None, max_tokens=4096, **kw) -> AsyncIterator:
        index = self._index
        self._index += 1
        self.calls.append({"messages": messages, "at": time.time(), "index": index})
        self.started.set()
        step = self.script[min(index, len(self.script) - 1)]
        if step.gate is not None:
            await step.gate.wait()
        for ev in step.events:
            yield ev

    def complete(self, messages, system="", **kw):
        from framework.llm.provider import LLMResponse

        return LLMResponse(content="summary", model="probe-mock", stop_reason="stop")


def text_turn(text: str) -> list:
    from framework.llm.stream_events import FinishEvent, TextDeltaEvent

    return [
        TextDeltaEvent(content=text, snapshot=text),
        FinishEvent(stop_reason="stop", input_tokens=10, output_tokens=5, model="probe-mock"),
    ]


def tool_turn(name: str, args: dict, tool_use_id: str) -> list:
    from framework.llm.stream_events import FinishEvent, ToolCallEvent

    return [
        ToolCallEvent(tool_use_id=tool_use_id, tool_name=name, tool_input=args),
        FinishEvent(stop_reason="tool_calls", input_tokens=10, output_tokens=5, model="probe-mock"),
    ]


def build_ctx(llm: ScriptedLLM) -> Any:
    from framework.orchestrator.node import DataBuffer, NodeContext, NodeSpec
    from framework.tracker.decision_tracker import DecisionTracker

    rt = MagicMock(spec=DecisionTracker)
    rt.start_run = MagicMock(return_value="session_20260101_000000_bgrecall")
    rt.decide = MagicMock(return_value="dec_1")
    for name in ("record_outcome", "end_run", "report_problem", "set_node"):
        setattr(rt, name, MagicMock())
    spec = NodeSpec(
        id="bg_recall_probe",
        name="BG Recall Probe",
        description="background recall timing probe",
        node_type="event_loop",
        output_keys=[],
        system_prompt="You are a test assistant.",
    )
    ctx = NodeContext(
        runtime=rt,
        node_id=spec.id,
        node_spec=spec,
        buffer=DataBuffer(),
        input_data={"task": "probe"},
        llm=llm,
        available_tools=[],
        stream_id="judge",  # bypass worker auto-escalation/queen blocking
    )
    ctx.session_id = "s1"
    return ctx


def flat_texts(messages: Any) -> list[str]:
    out: list[str] = []

    def walk(x: Any) -> None:
        if isinstance(x, str):
            out.append(x)
        elif isinstance(x, dict):
            for v in x.values():
                walk(v)
        elif isinstance(x, (list, tuple)):
            for v in x:
                walk(v)

    walk(messages)
    return out


def request_text(messages: Any) -> str:
    """One string in message order — the drain can merge consecutive user
    messages, so substring order (not per-message index) is the observable."""
    return "\n".join(flat_texts(messages))


def ordered(request: str, needles: list[str]) -> dict[str, Any]:
    positions = {n: request.find(n) for n in needles}
    present = [n for n in needles if positions[n] >= 0]
    ordered_ok = all(positions[present[i]] < positions[present[i + 1]] for i in range(len(present) - 1))
    return {"positions": positions, "present": present, "ordered": ordered_ok}


def make_loop(llm: ScriptedLLM, tool_executor=None, loop_cls=None):
    from framework.agent_loop.agent_loop import AgentLoop, LoopConfig

    cls = loop_cls or AgentLoop
    return cls(tool_executor=tool_executor, config=LoopConfig(max_iterations=5))


# ---------------------------------------------------------------------------
# E. AgentLoop injection mechanics
# ---------------------------------------------------------------------------

RECALL = "<system-reminder>\nRecalled memories relevant to the latest user message (supersedes earlier recall reminders):\n\n--- Global Memories ---\n\n### tea.md\n\nlikes tea\n\n--- End Global Memories ---\n</system-reminder>"


async def probe_drain_order() -> None:
    """E1: passive before the message, both queued while parked."""
    llm = ScriptedLLM([Event(text_turn("ok"))])
    loop = make_loop(llm)
    await loop.inject_event(RECALL, wake=False)  # the cached-recall injection
    await loop.inject_event("What tea do I like?", is_client_input=True)
    result = await loop.execute(build_ctx(llm))
    request = request_text(llm.calls[0]["messages"] if llm.calls else [])
    order = ordered(request, ["What tea do I like?", "Recalled memories relevant"])
    record(
        "E1.loop_drain_order",
        "passive recall queued before the user message is drained AFTER it "
        "in the same first request",
        {"user message before recall": True, "both present": True},
        order,
        result.success and order["ordered"] and len(order["present"]) == 2,
    )


async def probe_parked_never_wakes() -> None:
    """E2: passive while parked changes nothing until the next message."""
    llm = ScriptedLLM([Event(text_turn("first")), Event(text_turn("second"))])
    loop = make_loop(llm)
    await loop.inject_event("first message", is_client_input=True)
    result = await loop.execute(build_ctx(llm))
    calls_after_turn = len(llm.calls)

    await loop.inject_event(RECALL, wake=False)
    await asyncio.sleep(0.05)
    parked = {
        "llm_calls_after": len(llm.calls),
        "passive_held": any("Recalled memories relevant" in item for item in loop._passive_injections),
        "conversation_untouched": "Recalled memories relevant" not in request_text(llm.calls[-1]["messages"]),
    }
    record(
        "E2.parked_never_wakes",
        "a passive injection while parked starts no turn and sits in the passive list",
        {"llm_calls_after": calls_after_turn, "passive_held": True, "conversation_untouched": True},
        parked,
        result.success and parked["llm_calls_after"] == calls_after_turn and parked["passive_held"] and parked["conversation_untouched"],
    )

    await loop.inject_event("second message", is_client_input=True)
    await loop.execute(build_ctx(llm))
    request = request_text(llm.calls[-1]["messages"])
    order = ordered(request, ["second message", "Recalled memories relevant"])
    record(
        "E2.parked_never_wakes",
        "the parked recall is delivered with the NEXT message, after it",
        {"second message before recall": True, "both present": True},
        order,
        order["ordered"] and len(order["present"]) == 2,
    )


async def probe_midturn_delivery() -> None:
    """E3: a passive injection arriving while the turn is in flight is NOT
    drained inside the turn's inner LLM calls (the drain sits at the outer
    loop's iteration start); it lands with the next message."""
    from framework.llm.provider import ToolResult, ToolUse

    gate1 = asyncio.Event()
    gate2 = asyncio.Event()
    llm = ScriptedLLM(
        [
            Event(tool_turn("lookup", {"q": "tea"}, "call_1"), gate=gate1),
            Event(text_turn("done"), gate=gate2),
        ]
    )

    def tool_exec(tu: ToolUse) -> ToolResult:
        return ToolResult(tool_use_id=tu.id, content="tool result body", is_error=False)

    loop = make_loop(llm, tool_executor=tool_exec)
    ctx = build_ctx(llm)
    await loop.inject_event("What tea do I like?", is_client_input=True)
    task = asyncio.create_task(loop.execute(ctx))
    # Wait until iteration 1 is blocked inside the model call.
    while not llm.calls:
        await asyncio.sleep(0.01)
    await loop.inject_event(RECALL, wake=False)  # "refresh finished mid-turn"
    gate1.set()
    while len(llm.calls) < 2:
        await asyncio.sleep(0.01)
    in_flight = [
        "Recalled memories relevant" in request_text(call["messages"]) for call in llm.calls
    ]
    gate2.set()
    result = await task
    record(
        "E3.midturn_delivery",
        "a passive injection during the turn's first LLM call is absent from "
        "every request of that turn (no inner-iteration drain)",
        {"turn1_requests": [False, False]},
        {"turn1_requests": in_flight},
        result.success and in_flight == [False, False],
    )

    await loop.inject_event("second message", is_client_input=True)
    await loop.execute(build_ctx(llm))
    request = request_text(llm.calls[-1]["messages"])
    order = ordered(request, ["second message", "Recalled memories relevant"])
    record(
        "E3.midturn_delivery",
        "the mid-turn recall is delivered with the next message, after it",
        {"second message before recall": True, "both present": True},
        order,
        order["ordered"] and len(order["present"]) == 2,
    )


async def probe_no_continuation_after_tool_turn() -> None:
    """E4: a tool-using turn ends at its tool-free response — the judge sees no
    real tool results (they reset each inner turn) and returns ACCEPT, so no
    extra outer iteration exists and the next drain is the next message."""
    from framework.agent_loop.agent_loop import AgentLoop
    from framework.llm.provider import ToolResult, ToolUse

    class SpyLoop(AgentLoop):
        def __init__(self, **kwargs):
            super().__init__(**kwargs)
            self.judge_observations: list[dict] = []

        async def _judge_turn(self, ctx, conversation, accumulator, assistant_text, tool_results, iteration):
            self.judge_observations.append({"iteration": iteration, "real_tool_results": len(tool_results)})
            return await super()._judge_turn(ctx, conversation, accumulator, assistant_text, tool_results, iteration)

    def tool_exec(tu: ToolUse) -> ToolResult:
        return ToolResult(tool_use_id=tu.id, content="tool result body", is_error=False)

    llm = ScriptedLLM([Event(tool_turn("lookup", {"q": "tea"}, "call_1")), Event(text_turn("done"))])
    loop = make_loop(llm, tool_executor=tool_exec, loop_cls=SpyLoop)
    ctx = build_ctx(llm)
    ctx.stream_id = "queen"  # a queen conversation
    # event_triggered keeps supports_direct_user_io false so the turn does not
    # park waiting for a next message (the probe has none to send).
    ctx.event_triggered = True
    result = await loop.execute(ctx)
    record(
        "E4.no_continuation_after_tool_turn",
        "a tool-using turn parks at its tool-free response: the judge sees 0 real "
        "tool results and accepts, so the next drain is the next message",
        {"llm_calls": 2, "judge_calls": 1, "real_tool_results": 0},
        {"llm_calls": len(llm.calls), "judge_calls": loop.judge_observations, "real_tool_results": None},
        result.success and len(llm.calls) == 2 and len(loop.judge_observations) == 1 and loop.judge_observations[0]["real_tool_results"] == 0,
    )


# ---------------------------------------------------------------------------
# F. recall_selector timing
# ---------------------------------------------------------------------------


class RecordingAuxLLM:
    """An aux picker that records call start/end and sleeps a fixed time."""

    def __init__(self, delay: float, content: str = "") -> None:
        self.delay = delay
        self.content = content
        self.spans: list[tuple[str, float, float]] = []
        from framework.llm.provider import LLMResponse

        self._response = LLMResponse

    async def acomplete(self, messages, system="", max_tokens=0, response_format=None, **kw):
        label = ""
        try:
            label = str(messages[0]["content"]).split("## User query", 1)[1][:40]
        except Exception:
            label = "?"
        start = time.time()
        await asyncio.sleep(self.delay)
        end = time.time()
        self.spans.append((label, start, end))
        return self._response(content=self.content, model="probe-mock", stop_reason="stop")


def write_note(directory: Path, filename: str, description: str, body: str = "", note_type: str = "preference") -> None:
    directory.mkdir(parents=True, exist_ok=True)
    (directory / filename).write_text(
        f"---\nname: {filename[:-3]}\ntype: {note_type}\ndescription: {description}\n---\n{body}\n",
        encoding="utf-8",
    )


async def probe_selector_concurrency() -> None:
    from framework.agents.queen.recall_selector import build_scoped_recall_blocks

    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        gl = root / "global"
        qn = root / "queen"
        write_note(gl, "tea.md", "tea preference", "likes pu-erh")
        write_note(qn, "project.md", "project notes", "ships on friday")
        llm = RecordingAuxLLM(delay=0.15, content='{"selected_memories": []}')
        start = time.time()
        await build_scoped_recall_blocks("query", llm, global_memory_dir=gl, queen_memory_dir=qn, queen_id="probe")
        elapsed = time.time() - start
        spans = sorted(llm.spans, key=lambda s: s[1])
        concurrent = len(spans) == 2 and spans[0][2] > spans[1][1]
        record(
            "F1.selector_concurrency",
            "both scope picks are in flight at once (asyncio.gather)",
            {"calls": 2, "overlapping": True},
            {"calls": len(spans), "overlapping": concurrent, "elapsed_s": round(elapsed, 3)},
            len(spans) == 2 and concurrent and elapsed < 0.28,
        )


async def probe_selector_no_timeout() -> None:
    from framework.agents.queen.recall_selector import build_scoped_recall_blocks

    with tempfile.TemporaryDirectory() as tmp:
        gl = Path(tmp) / "global"
        write_note(gl, "tea.md", "tea preference", "likes pu-erh")
        llm = RecordingAuxLLM(delay=0.6, content='{"selected_memories": ["tea.md"]}')
        start = time.time()
        block, _ = await build_scoped_recall_blocks("query", llm, global_memory_dir=gl, queen_memory_dir=None)
        elapsed = time.time() - start
        record(
            "F2.selector_no_timeout",
            "no cap inside the selector: the full pick time is awaited",
            {"elapsed_s >= 0.55": True, "block_rendered": True},
            {"elapsed_s": round(elapsed, 3), "block_rendered": "tea.md" in block},
            elapsed >= 0.55 and "tea.md" in block,
        )


async def probe_seed_cap() -> None:
    """F3: the session-start seed is what bounds the pick when background mode
    starts a session: wait_for(select_memories(...), timeout=3.0)."""
    from framework.agents.queen.recall_selector import select_memories

    with tempfile.TemporaryDirectory() as tmp:
        gl = Path(tmp) / "global"
        write_note(gl, "tea.md", "tea preference", "likes pu-erh")
        slow = RecordingAuxLLM(delay=5.0, content='{"selected_memories": ["tea.md"]}')
        cache: dict[str, str] = {}
        start = time.time()
        try:
            selected = await asyncio.wait_for(select_memories("seed query", slow, gl), timeout=3.0)
            cache["global"] = "set"
            outcome = f"returned {selected!r}"
        except TimeoutError:
            outcome = "TimeoutError"
        elapsed = time.time() - start
        record(
            "F3.seed_cap",
            "wait_for(..., 3.0) cancels a 5s seed pick; the cache is left unchanged",
            {"outcome": "TimeoutError", "elapsed_s ~= 3.0": True, "cache": {}},
            {"outcome": outcome, "elapsed_s": round(elapsed, 2), "cache": cache},
            outcome == "TimeoutError" and cache == {} and 2.9 <= elapsed < 4.0,
        )


async def probe_seed_max_results() -> None:
    from framework.agents.queen.recall_selector import build_scoped_recall_blocks, select_memories

    with tempfile.TemporaryDirectory() as tmp:
        gl = Path(tmp) / "global"
        for i in range(7):
            write_note(gl, f"char{i}.md", f"character sheet {i}", "profile body", note_type="profile")
        llm = RecordingAuxLLM(delay=0.01, content='{"selected_memories": []}')
        seeded = await select_memories("seed query", llm, gl)  # the seed call shape: default max_results
        block, _ = await build_scoped_recall_blocks("refresh query", llm, global_memory_dir=gl, queen_memory_dir=None)
        record(
            "F4.seed_max_results",
            "seed call keeps 5 profile pins; the refresh keeps 3 (scope default)",
            {"seed_pins": 5, "refresh_pins": 3},
            {"seed_pins": len(seeded), "refresh_pins": block.count("### ")},
            len(seeded) == 5 and block.count("### ") == 3,
        )


async def probe_empty_query() -> None:
    from framework.agents.queen.recall_selector import build_scoped_recall_blocks

    with tempfile.TemporaryDirectory() as tmp:
        gl = Path(tmp) / "global"
        write_note(gl, "tea.md", "tea preference", "likes pu-erh")
        llm = RecordingAuxLLM(delay=0.01, content='{"selected_memories": []}')
        await build_scoped_recall_blocks("", llm, global_memory_dir=gl, queen_memory_dir=None)
        record(
            "F5.empty_query",
            "build_scoped_recall_blocks has no empty-query guard: the pick still runs "
            "(queen_orchestrator._refresh_recall_cache is what skips it)",
            {"model_calls": 1},
            {"model_calls": len(llm.spans)},
            len(llm.spans) == 1,
        )


# ---------------------------------------------------------------------------
# H. EventBus.publish awaits handlers; a spawned task is what returns fast
# ---------------------------------------------------------------------------


async def probe_event_bus_publish() -> None:
    from framework.host.event_bus import AgentEvent, EventBus, EventType

    bus = EventBus()
    seen: list[str] = []
    spawn_later = asyncio.Event()

    async def slow_handler(event: AgentEvent) -> None:
        await asyncio.sleep(0.25)
        seen.append("slow")

    async def task_handler(event: AgentEvent) -> None:
        async def later() -> None:
            await asyncio.sleep(0.3)
            seen.append("later")
            spawn_later.set()

        asyncio.create_task(later())

    bus.subscribe([EventType.CLIENT_INPUT_RECEIVED], slow_handler)
    start = time.time()
    await bus.publish(AgentEvent(type=EventType.CLIENT_INPUT_RECEIVED, stream_id="queen", data={"content": "hi"}))
    slow_elapsed = time.time() - start

    bus2 = EventBus()
    bus2.subscribe([EventType.CLIENT_INPUT_RECEIVED], task_handler)
    start = time.time()
    await bus2.publish(AgentEvent(type=EventType.CLIENT_INPUT_RECEIVED, stream_id="queen", data={"content": "hi"}))
    task_elapsed = time.time() - start
    await asyncio.wait_for(spawn_later.wait(), timeout=2.0)
    record(
        "H.publish_awaits_handlers",
        "publish awaits handlers (0.25s handler → ~0.25s); a handler that spawns a "
        "task returns immediately while the task finishes later",
        {"slow_publish_s >= 0.2": True, "task_publish_s < 0.1": True, "later_ran": True},
        {
            "slow_publish_s": round(slow_elapsed, 3),
            "task_publish_s": round(task_elapsed, 3),
            "later_ran": "later" in seen,
        },
        slow_elapsed >= 0.2 and task_elapsed < 0.1 and "later" in seen,
    )


# ---------------------------------------------------------------------------
# G. Source anchors (measured line numbers in the reference tree)
# ---------------------------------------------------------------------------

ANCHORS = [
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r"async def _recall_on_user_input"),
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r"if get_memory_recall_config\(\)\.mode != \"background\""),
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r"await _inject_recall_if_changed\(\)"),
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r"_asyncio\.create_task\(_bg_refresh\(\)\)"),
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r"async def _inject_recall_if_changed"),
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r"if not block or block == _last_injected_recall"),
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r"async def _refresh_recall_cache"),
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r"if not query or not isinstance\(query, str\)"),
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r"async def _recall_for_message"),
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r'if trigger and get_memory_recall_config\(\)\.mode == "background"'),
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r"timeout=3\.0,"),
    ("G.anchors", "core/framework/server/queen_orchestrator.py", r"event\.data or \{\}\)\.get\(\"content\""),
    ("G.anchors", "core/framework/server/routes_execution.py", r"Publish BEFORE inject_event so handlers"),
    ("G.anchors", "core/framework/server/routes_execution.py", r"await session\.event_bus\.publish\("),
    ("G.anchors", "core/framework/server/routes_execution.py", r"await node\.inject_event\("),
    ("G.anchors", "core/framework/agent_loop/agent_loop.py", r"for content in self\._passive_injections"),
    ("G.anchors", "core/framework/agent_loop/agent_loop.py", r"drained_injections = await self\._drain_injection_queue"),
    ("G.anchors", "core/framework/agent_loop/agent_loop.py", r"self\._passive_injections\.append\(content\)"),
    ("G.anchors", "core/framework/agent_loop/internals/cursor_persistence.py", r"stamp = datetime\.now\(\)\.astimezone\(\)\.strftime"),
    ("G.anchors", "core/framework/config.py", r'"background": the same pick after she has started answering'),
    ("G.anchors", "core/framework/config.py", r'#: The longest "wait" holds a message for its pick\.'),
]


def probe_anchors() -> None:
    import re

    repo = Path(__file__).resolve().parents[2]
    for case, rel, pattern in ANCHORS:
        path = repo / rel
        text = path.read_text(encoding="utf-8")
        matches = [i + 1 for i, line in enumerate(text.splitlines()) if re.search(pattern, line)]
        record(
            case,
            f"{rel}::{pattern}",
            "at least one anchored line (multi-match recorded for the doc)",
            {"file": rel, "lines": matches},
            len(matches) >= 1,
        )

    # The handler's ordering is the load-bearing fact: deliver the cached
    # block first, then start the pick task. And routes_execution publishes
    # CLIENT_INPUT_RECEIVED before it queues the user message.
    orch = (repo / "core/framework/server/queen_orchestrator.py").read_text(encoding="utf-8").splitlines()
    handler_start = next(i for i, line in enumerate(orch) if "async def _recall_on_user_input" in line)
    handler_lines = orch[handler_start : handler_start + 40]
    inject_offsets = [i + handler_start + 1 for i, line in enumerate(handler_lines) if "await _inject_recall_if_changed()" in line]
    task_line = next(i + handler_start + 1 for i, line in enumerate(handler_lines) if "_asyncio.create_task(_bg_refresh())" in line)
    record(
        "G.anchors",
        "_recall_on_user_input delivers the cached block, THEN spawns the pick",
        "cached-inject line < create_task line",
        {"cached_inject_lines": inject_offsets, "create_task_line": task_line},
        bool(inject_offsets) and inject_offsets[0] < task_line,
    )

    routes = (repo / "core/framework/server/routes_execution.py").read_text(encoding="utf-8").splitlines()
    publish_line = next(i + 1 for i, line in enumerate(routes) if "await session.event_bus.publish(" in line and "event_bus" in line)
    # The first node.inject_event after deliver_to_queen's publish.
    inject_line = next(i + 1 for i, line in enumerate(routes) if i + 1 > publish_line and "await node.inject_event(" in line)
    record(
        "G.anchors",
        "deliver_to_queen publishes CLIENT_INPUT_RECEIVED before queueing the user message",
        "publish line < inject_event line",
        {"publish_line": publish_line, "inject_event_line": inject_line},
        publish_line < inject_line,
    )

    text = (repo / "core/framework/agent_loop/agent_loop.py").read_text(encoding="utf-8").splitlines()
    passive_line = next((i + 1 for i, line in enumerate(text) if "for content in self._passive_injections" in line), -1)
    queue_line = next((i + 1 for i, line in enumerate(text) if "self._injection_queue.put_nowait((content, False, None, None))" in line), -1)
    record(
        "G.anchors",
        "passive injections are appended to the queue AFTER anything already queued",
        "passive line < put_nowait line",
        {"passive_line": passive_line, "queue_line": queue_line},
        0 < passive_line < queue_line,
    )


# ---------------------------------------------------------------------------
# Driver
# ---------------------------------------------------------------------------


async def main() -> int:
    await probe_drain_order()
    await probe_parked_never_wakes()
    await probe_midturn_delivery()
    await probe_no_continuation_after_tool_turn()
    await probe_selector_concurrency()
    await probe_selector_no_timeout()
    await probe_seed_cap()
    await probe_seed_max_results()
    await probe_empty_query()
    await probe_event_bus_publish()
    probe_anchors()

    failed = [c for c in CASES if not c["pass"]]
    payload = {"total": len(CASES), "passed": len(CASES) - len(failed), "failed": len(failed), "cases": CASES}
    out = Path(__file__).resolve().parent.parent / "docs" / "e2e-assets" / "memory-notes-1015" / "bg-recall-probe.json"
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(payload, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    for case in CASES:
        mark = "PASS" if case["pass"] else "FAIL"
        print(f"{mark} {case['case']}: {case['subject']}")
        if not case["pass"]:
            print(f"     expected={case['expected']!r}\n     observed={case['observed']!r}")
    print(f"\n{len(CASES) - len(failed)}/{len(CASES)} cases passed -> {out}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
