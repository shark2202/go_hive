#!/usr/bin/env python3
"""Python 侧 judge 可达性动态探针：真实 AgentLoop + 脚本化 LLM，三条用例。

为什么需要：静态计数只能证明「符号在哪」，不能证明「机制真的会跑」。这三条用例
直接驱动 framework.agent_loop.AgentLoop，把 judge 面的三个事实变成可观察结果：

  1. 有 success_criteria（judge=None，即 graph 节点的构造方式）→ Level 2b
     conversation judge 真的被调用，RETRY 反馈进了对话，第二次 ACCEPT 结束回合。
  2. 没有 success_criteria → judge 一次都不调用（不花额外模型调用）。
  3. 声明了 output_keys 且没有写入点（当前 runtime 的现状）→ Level 2 隐式门
     每轮都回 "Task incomplete … not yet produced"，直到 max_iterations。

用例 3 是「记录现状」：一旦上游给 output key 接上写入点（set_output 复活或别的
通道），它会失败，提醒重开 docs/judge-parity.md 的 J3 判定。

用法（需要可 import framework 的 Python 环境）:
    PYTHONPATH=<repo>/core uv run python go_hive/scripts/judge_python_probe.py
    PYTHONPATH=<repo>/core uv run python go_hive/scripts/judge_scope_probe.py --dynamic
"""

from __future__ import annotations

import asyncio
import sys
from collections.abc import AsyncIterator
from typing import Any
from unittest.mock import MagicMock

from framework.agent_loop.agent_loop import AgentLoop
from framework.agent_loop.internals.types import LoopConfig
from framework.llm.provider import LLMProvider, LLMResponse
from framework.llm.stream_events import FinishEvent, TextDeltaEvent
from framework.orchestrator.node import DataBuffer, NodeContext, NodeSpec
from framework.tracker.decision_tracker import DecisionTracker


class ScriptedLLM(LLMProvider):
    """One scripted text per streaming turn; scripted replies for judge calls."""

    model = "mock"

    def __init__(self, texts: list[str], judge_replies: list[str]) -> None:
        self.texts = texts
        self.judge_replies = list(judge_replies)
        self.stream_calls: list[dict[str, Any]] = []
        self.judge_calls: list[dict[str, Any]] = []

    async def stream(self, messages, system="", tools=None, max_tokens=4096, **kw) -> AsyncIterator:
        self.stream_calls.append({"messages": messages, "system": system})
        text = self.texts[min(len(self.stream_calls) - 1, len(self.texts) - 1)]
        yield TextDeltaEvent(content=text, snapshot=text)
        yield FinishEvent(stop_reason="stop", input_tokens=10, output_tokens=5, model="mock")

    def complete(self, messages, system="", **kw) -> LLMResponse:
        return LLMResponse(content="summary", model="mock", stop_reason="stop")

    async def acomplete(self, messages, system="", **kw) -> LLMResponse:
        self.judge_calls.append({"messages": messages, "system": system})
        reply = self.judge_replies[min(len(self.judge_calls) - 1, len(self.judge_replies) - 1)]
        return LLMResponse(content=reply, model="mock", stop_reason="stop")


def build_ctx(llm: LLMProvider, spec: NodeSpec) -> NodeContext:
    runtime = MagicMock(spec=DecisionTracker)
    runtime.start_run = MagicMock(return_value="session_20260101_000000_probe001")
    runtime.decide = MagicMock(return_value="dec_1")
    for name in ("record_outcome", "end_run", "report_problem", "set_node"):
        setattr(runtime, name, MagicMock())
    ctx = NodeContext(
        runtime=runtime,
        node_id=spec.id,
        node_spec=spec,
        buffer=DataBuffer(),
        input_data={},
        llm=llm,
        available_tools=[],
        stream_id="judge",
    )
    ctx.session_id = "s1"
    return ctx


async def case_level2b_runs_and_retries() -> str:
    llm = ScriptedLLM(
        texts=["first pass", "second pass"],
        judge_replies=[
            "ACTION: RETRY\nCONFIDENCE: 0.9\nFEEDBACK: the risks section is empty",
            "ACTION: ACCEPT\nCONFIDENCE: 0.9\nFEEDBACK:",
        ],
    )
    spec = NodeSpec(
        id="phase_agent",
        name="Phase Agent",
        description="phase description text",
        node_type="event_loop",
        output_keys=[],
        success_criteria="the report names every risk",
        system_prompt="test",
    )
    result = await AgentLoop(config=LoopConfig(max_iterations=4)).execute(build_ctx(llm, spec))

    prompt = llm.judge_calls[0]["messages"][0]["content"]
    assert len(llm.judge_calls) == 2, f"judge calls = {len(llm.judge_calls)}, want 2 (RETRY then ACCEPT)"
    for fragment in (
        "SUCCESS CRITERIA:\nthe report names every risk",
        "PHASE: Phase Agent",
        "DESCRIPTION: phase description text",
        "OUTPUTS SET:\n(none)",
    ):
        assert fragment in prompt, f"judge prompt missing {fragment!r}:\n{prompt[:600]}"
    assert result.success is True, result
    assert len(llm.stream_calls) == 2, f"model turns = {len(llm.stream_calls)}"
    replay = "\n".join(str(m.get("content")) for m in llm.stream_calls[1]["messages"] if isinstance(m, dict))
    assert "the risks section is empty" in replay, "RETRY feedback never reached the conversation"
    return f"Level 2b: judge_calls={len(llm.judge_calls)} model_turns={len(llm.stream_calls)} accept={result.success}"


async def case_no_criteria_no_judge() -> str:
    llm = ScriptedLLM(texts=["done"], judge_replies=["ACTION: ACCEPT\nCONFIDENCE: 0.9\nFEEDBACK:"])
    spec = NodeSpec(
        id="plain_agent",
        name="Plain Agent",
        description="no criteria",
        node_type="event_loop",
        output_keys=[],
        system_prompt="test",
    )
    result = await AgentLoop(config=LoopConfig(max_iterations=3)).execute(build_ctx(llm, spec))
    assert llm.judge_calls == [], f"judge ran without criteria: {len(llm.judge_calls)} call(s)"
    assert result.success is True, result
    return f"no criteria: judge_calls=0 model_turns={len(llm.stream_calls)} accept={result.success}"


async def case_output_key_gate_is_degenerate() -> str:
    llm = ScriptedLLM(texts=["did work", "did more work"], judge_replies=[])
    spec = NodeSpec(
        id="output_agent",
        name="Output Agent",
        description="declares an output key",
        node_type="event_loop",
        output_keys=["findings"],
        system_prompt="test",
    )
    result = await AgentLoop(config=LoopConfig(max_iterations=2)).execute(build_ctx(llm, spec))
    second_turn = "\n".join(str(m.get("content")) for m in llm.stream_calls[1]["messages"] if isinstance(m, dict))
    assert "Required outputs not yet produced: ['findings']" in second_turn, second_turn[-800:]
    assert result.success is False, f"loop accepted despite an unsatisfiable output key: {result}"
    return (
        "output-key gate: model_turns="
        f"{len(llm.stream_calls)} accept={result.success} "
        "feedback=\"Task incomplete. Required outputs not yet produced: ['findings']\""
    )


async def run_cases() -> list[str]:
    results = [await case_level2b_runs_and_retries(), await case_no_criteria_no_judge()]
    results.append(await case_output_key_gate_is_degenerate())
    return results


async def _main() -> int:
    print("framework:", sys.modules["framework"].__file__)
    for line in await run_cases():
        print(line)
    print("judge_python_probe: OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(_main()))
