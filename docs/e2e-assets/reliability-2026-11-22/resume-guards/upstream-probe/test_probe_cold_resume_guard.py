"""Probe (temporary, untracked): upstream cold-resume mid-turn guard (§6b1½).

Reproduces the crash shape the Go reliability lane observed — a mid-turn
cursor (pending_input=null) on a queen stream — and asserts exactly what
`AgentLoop.execute` writes/emits on the cold resume:

  * conversation gets the "[System] The session was interrupted ..." user note
  * cursor pending_input = {"reason": "cold_interrupted", "emit_client_request": True}
  * CLIENT_INPUT_REQUESTED is re-emitted with park_reason=cold_interrupted
  * LOOP_STATE_CHANGED announces activity=awaiting_user, park_reason=cold_interrupted
  * the LLM is never called (the in-flight work is NOT silently resumed)

Run:  cd core && ../.venv/bin/python -m pytest tests/test_probe_cold_resume_guard.py -q
"""

from __future__ import annotations

import asyncio
import json
from typing import Any
from unittest.mock import MagicMock

import pytest

from framework.agent_loop.agent_loop import AgentLoop
from framework.agent_loop.conversation import NodeConversation
from framework.agent_loop.internals.types import LoopConfig
from framework.host.event_bus import EventBus, EventType
from framework.llm.provider import LLMProvider, LLMResponse
from framework.llm.stream_events import FinishEvent, TextDeltaEvent
from framework.orchestrator.node import DataBuffer, NodeContext, NodeSpec
from framework.storage.conversation_store import FileConversationStore
from framework.tracker.decision_tracker import DecisionTracker as Runtime


class MockStreamingLLM(LLMProvider):
    model: str = "mock"

    def __init__(self, scenarios=None):
        self.scenarios = scenarios or []
        self._call_index = 0

    async def stream(self, messages, system="", tools=None, max_tokens=4096, **kwargs):
        self._call_index += 1
        for event in self.scenarios[(self._call_index - 1) % max(1, len(self.scenarios))]:
            yield event

    def complete(self, messages, system="", **kwargs):
        return LLMResponse(content="unused", model="mock", stop_reason="stop")


def text_scenario(text: str):
    return [TextDeltaEvent(content=text, snapshot=text), FinishEvent(stop_reason="stop", input_tokens=1, output_tokens=1, model="mock")]


@pytest.mark.asyncio
async def test_cold_resume_midturn_guard_writes_cold_interrupted(tmp_path):
    store = FileConversationStore(tmp_path / "conv")
    conv = NodeConversation(system_prompt="You are a test assistant.", output_keys=[], store=store)
    conv.set_current_phase("queen")
    # A conversation with real parts, as after a crash mid-turn.
    await conv.add_user_message("long task: sleep then report")
    await conv.add_assistant_message(
        "",
        tool_calls=[
            {
                "id": "call_1",
                "type": "function",
                "function": {"name": "terminal_exec", "arguments": '{"command":"sleep 180"}'},
            }
        ],
    )
    await conv.add_tool_result("call_1", "started")
    # Crash shape: cursor written mid-turn, NO pending_input.
    await store.write_cursor({"iteration": 3, "next_seq": conv.next_seq})
    parts_before = len(await store.read_parts())

    spec = NodeSpec(id="queen", name="Queen", description="interactive queen", node_type="event_loop", output_keys=[])
    bus = EventBus()
    captured: list[Any] = []

    async def capture(ev):
        captured.append(ev)

    bus.subscribe(list(EventType), capture)
    llm = MockStreamingLLM([text_scenario("THIS MUST NOT RUN")])

    node = AgentLoop(event_bus=bus, conversation_store=store, config=LoopConfig(max_iterations=10))
    runtime = MagicMock(spec=Runtime)
    runtime.start_run = MagicMock(return_value="session_probe")
    runtime.decide = MagicMock(return_value="dec_1")
    ctx = NodeContext(
        runtime=runtime,
        node_id="queen",
        node_spec=spec,
        buffer=DataBuffer(),
        input_data={},
        llm=llm,
        available_tools=[],
        goal_context="",
        stream_id="queen",
    )

    async def shutdown_after_park():
        await asyncio.sleep(0.05)
        node.signal_shutdown()

    task = asyncio.create_task(shutdown_after_park())
    result = await node.execute(ctx)
    await task

    # ---- The guard fired and did not run the LLM. ----
    assert result.success is True
    assert llm._call_index == 0, "cold resume must NOT resume in-flight work"

    # ---- Cursor fields. ----
    cursor = await store.read_cursor()
    pending = cursor.get("pending_input")
    print("\n[cursor] ", json.dumps(cursor, default=str))
    assert isinstance(pending, dict), f"pending_input not written: {cursor}"
    assert pending["reason"] == "cold_interrupted"
    assert pending.get("emit_client_request") is True
    assert pending.get("questions") is None

    # ---- Conversation gains the system note. ----
    conv_after = await NodeConversation.restore(store)
    texts = [m.content for m in conv_after.messages if m.role == "user"]
    note = [t for t in texts if "The session was interrupted" in t]
    assert len(note) == 1, f"system note missing; user messages: {texts}"
    parts_after = len(await store.read_parts())
    assert parts_after == parts_before + 1

    # ---- Events (read the bus's own history: handler dispatch is async). ----
    await asyncio.sleep(0.05)
    published = list(bus._event_history)
    types = [ev.type for ev in published]
    print("[events] ", [t.value for t in types])
    req = [ev for ev in published if ev.type == EventType.CLIENT_INPUT_REQUESTED]
    state = [ev for ev in published if ev.type == EventType.LOOP_STATE_CHANGED]
    assert len(req) == 1, f"want exactly one re-emitted client_input_requested: {types}"
    print("[client_input_requested.data] ", json.dumps(req[0].data, default=str))
    assert req[0].data["park_reason"] == "cold_interrupted"
    assert req[0].data.get("questions") is None
    assert len(state) >= 1
    print("[loop_state_changed.data] ", json.dumps(state[-1].data, default=str))
    assert state[-1].data["activity"] == "awaiting_user"
    assert state[-1].data["park_reason"] == "cold_interrupted"


@pytest.mark.asyncio
async def test_cold_resume_with_queued_user_message_skips_guard(tmp_path):
    """A fresh queued user message (drained injection) suppresses the guard,
    exactly as the upstream `drained_injections == 0` condition requires."""
    store = FileConversationStore(tmp_path / "conv")
    conv = NodeConversation(system_prompt="You are a test assistant.", output_keys=[], store=store)
    conv.set_current_phase("queen")
    await conv.add_user_message("earlier request")
    await conv.add_assistant_message("done earlier")
    await store.write_cursor({"iteration": 0, "next_seq": conv.next_seq})

    spec = NodeSpec(id="queen", name="Queen", description="interactive queen", node_type="event_loop", output_keys=[])
    bus = EventBus()
    captured: list[Any] = []

    async def capture(ev):
        captured.append(ev)

    bus.subscribe(list(EventType), capture)
    llm = MockStreamingLLM([text_scenario("replied")])
    node = AgentLoop(event_bus=bus, conversation_store=store, config=LoopConfig(max_iterations=2))
    runtime = MagicMock(spec=Runtime)
    runtime.start_run = MagicMock(return_value="session_probe")
    runtime.decide = MagicMock(return_value="dec_1")
    ctx = NodeContext(
        runtime=runtime,
        node_id="queen",
        node_spec=spec,
        buffer=DataBuffer(),
        input_data={},
        llm=llm,
        available_tools=[],
        goal_context="",
        stream_id="queen",
    )
    await node.inject_event("resume with this", is_client_input=True)

    async def shutdown_after_turn():
        await asyncio.sleep(0.1)
        node.signal_shutdown()

    task = asyncio.create_task(shutdown_after_turn())
    result = await node.execute(ctx)
    await task

    cursor = await store.read_cursor()
    pending = cursor.get("pending_input")
    print("\n[cursor with queued input] ", json.dumps(cursor, default=str))
    # The queued message broke the wait: no cold_interrupted park was written
    # by the guard (the turn ran instead).
    assert llm._call_index >= 1, "queued message should start a normal turn"
    assert not (isinstance(pending, dict) and pending.get("reason") == "cold_interrupted")
