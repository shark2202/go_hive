#!/usr/bin/env python3
"""J2 probe: the Python judge-failure contract, exercised directly.

Registered diff (blocked #8): on a judge failure Python retries and then falls
back to ACCEPT in ``AgentLoop._judge_turn`` (agent_loop.py:6202-6270), while
Go normalizes the error to RETRY in ``normalizeJudgeVerdict``
(internal/agentloop/judge.go:148-153).

This probe runs the real functions — ``AgentLoop._judge_turn`` (retry +
fallback), ``judge_pipeline.judge_turn`` (Level 1 custom-judge seam),
``tool_result_handler.is_transient_error`` (classification), and the real
``LoopConfig`` defaults — against a scripted ``JudgeProtocol``. No service, no
model, no network.

Run:
    PYTHONPATH=$PWD/core .venv/bin/python go_hive/scripts/judge_j2_probe.py
"""

from __future__ import annotations

import asyncio
import json
import sys
from types import SimpleNamespace

from framework.agent_loop.agent_loop import AgentLoop
from framework.agent_loop.conversation import NodeConversation
from framework.agent_loop.internals.types import (
    JudgeVerdict,
    LoopConfig,
    OutputAccumulator,
)
from framework.agent_loop.types import AgentSpec


class ScriptedJudge:
    """JudgeProtocol stand-in: each call raises or returns the scripted step.

    The last step repeats forever, so a case can express "every attempt does X"
    by listing one step.
    """

    def __init__(self, steps: list[object]) -> None:
        self._steps = list(steps)
        self.calls = 0

    async def evaluate(self, context: dict) -> JudgeVerdict:
        index = min(self.calls, len(self._steps) - 1)
        self.calls += 1
        step = self._steps[index]
        if isinstance(step, BaseException):
            raise step
        return step  # type: ignore[return-value]


class LoopStub:
    """The AgentLoop surface the real ``_judge_turn`` touches, nothing else."""

    def __init__(self, judge: ScriptedJudge, config: LoopConfig) -> None:
        self._judge = judge
        self._config = config
        self.bumps: list[str] = []
        # Instance attribute (not class attr) so no self is bound into the call.
        self._is_transient_error = AgentLoop._is_transient_error

        def missing(accumulator, output_keys, nullable_keys=None):
            return AgentLoop._get_missing_output_keys(self, accumulator, output_keys, nullable_keys)

        self._get_missing_output_keys = missing

    def _bump(self, name: str, amount: int = 1) -> None:
        self.bumps.append(name)


def make_agent_context() -> SimpleNamespace:
    spec = AgentSpec(id="j2", name="J2", description="probe")
    return SimpleNamespace(agent_spec=spec, llm=None)


async def run_case(name: str, steps: list[object], expected: dict, **overrides) -> dict:
    """Drive the real ``_judge_turn`` for one scripted scenario."""
    config = LoopConfig()
    # Zero only the sleep durations, never the attempt count, so the probe is
    # fast while the retry budget stays at the real defaults unless overridden.
    config.stream_retry_backoff_base = 0.0
    config.stream_retry_max_delay = 0.0
    for key, value in overrides.items():
        setattr(config, key, value)

    judge = ScriptedJudge(steps)
    stub = LoopStub(judge, config)
    ctx = make_agent_context()
    conversation = NodeConversation("system")
    accumulator = OutputAccumulator()

    action = ""
    feedback = ""
    raised = ""
    try:
        verdict = await AgentLoop._judge_turn(
            stub,
            ctx,
            conversation,
            accumulator,
            "assistant text",
            [],
            0,
        )
        action = verdict.action
        feedback = verdict.feedback or ""
    except Exception as exc:  # the re-raise path
        raised = f"{type(exc).__name__}: {exc}"

    observed = {
        "action": action,
        "raised": raised,
        "judge_calls": judge.calls,
        "bumps": stub.bumps,
    }
    check = "OK"
    for key, want in expected.items():
        if observed[key] != want:
            check = f"MISMATCH ({key}: want {want!r})"
    return {
        "case": name,
        "expected": expected,
        **observed,
        "feedback": feedback,
        "max_stream_retries": config.max_stream_retries,
        "check": check,
    }


async def main() -> int:
    accept = JudgeVerdict(action="ACCEPT", feedback="criteria met")
    retry = JudgeVerdict(action="RETRY", feedback="criteria not met yet")
    escalate = JudgeVerdict(action="ESCALATE", feedback="hopeless")

    cases = [
        # {首次判失败}: the judge answers "fail" on the first evaluation.
        # A verdict is not an exception, so there is no retry.
        await run_case(
            "first-eval-fails(RETRY)",
            [retry],
            {"action": "RETRY", "judge_calls": 1, "raised": "", "bumps": []},
        ),
        # {重试后通过}: first attempt raises a transient error, the retry
        # succeeds. The retried evaluation's verdict is returned.
        await run_case(
            "transient-then-accept",
            [TimeoutError("judge blip"), accept],
            {
                "action": "ACCEPT",
                "judge_calls": 2,
                "raised": "",
                "bumps": ["judge_transient_retry"],
            },
        ),
        # {重试后仍失败}: first attempt raises transient, the retry returns a
        # failing verdict. The verdict is returned as-is (no third attempt).
        await run_case(
            "transient-then-eval-fails",
            [ConnectionError("judge blip"), retry],
            {"action": "RETRY", "judge_calls": 2, "raised": "", "bumps": ["judge_transient_retry"]},
        ),
        # {连续两次异常} at an exhausted budget (max_stream_retries=2):
        # fall back to ACCEPT with the unavailability feedback.
        await run_case(
            "transient-exhausted(cap=2)",
            [TimeoutError("a"), TimeoutError("b")],
            {
                "action": "ACCEPT",
                "judge_calls": 2,
                "raised": "",
                "bumps": ["judge_transient_retry", "judge_fallback_accept"],
            },
            max_stream_retries=2,
        ),
        # Same, on the real default budget: five attempts, then ACCEPT.
        await run_case(
            "transient-exhausted(default cap=5)",
            [TimeoutError("always")],
            {
                "action": "ACCEPT",
                "judge_calls": 5,
                "raised": "",
                "bumps": ["judge_transient_retry", "judge_transient_retry", "judge_transient_retry", "judge_transient_retry", "judge_fallback_accept"],
            },
        ),
        # Non-transient failure: re-raised to the caller, never retried.
        await run_case(
            "non-transient-raises",
            [ValueError("bad judge wiring")],
            {"action": "", "judge_calls": 1, "raised": "ValueError: bad judge wiring", "bumps": []},
        ),
        # Control: ESCALATE is returned verbatim.
        await run_case(
            "verdict-escalate",
            [escalate],
            {"action": "ESCALATE", "judge_calls": 1, "raised": "", "bumps": []},
        ),
    ]

    print("== J2 matrix: real AgentLoop._judge_turn + judge_pipeline.judge_turn ==")
    failures = 0
    for case in cases:
        if case["check"] != "OK":
            failures += 1
        print(json.dumps(case))

    # Classification table: the same helper _judge_turn consults.
    from framework.agent_loop.internals.tool_result_handler import is_transient_error

    print("\n== is_transient_error (real helper) ==")
    for exc in (
        TimeoutError("t"),
        ConnectionError("c"),
        OSError("o"),
        ValueError("v"),
        FileNotFoundError("f"),
        RuntimeError("Stream error: 429 rate limit"),
    ):
        print(json.dumps({"exc": f"{type(exc).__name__}: {exc}", "transient": bool(is_transient_error(exc))}))

    print(f"\ncheck: {'OK' if failures == 0 else f'{failures} MISMATCH'}")
    return 0 if failures == 0 else 1


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
