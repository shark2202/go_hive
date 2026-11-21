#!/usr/bin/env python3
"""Python-side runner for the real-LLM crosscheck (Python Hive ↔ Go Hive).

Runs the *real* Python provider path (``LlmProviderStage`` →
``LiteLLMProvider``) against a fixture HIVE_HOME whose ``llm`` slot points at
the local recording pass-through proxy (``llm_real_crosscheck.py``), which
forwards to the real upstream in ``~/.hive-go/configuration.json``. Records,
for every scenario, the framework-level stream events, the final text, the
finish reason and the token usage; the raw wire request/response bodies are
captured by the proxy.

Scenarios (identical prompts on both sides, temperature=0):

  chat_minimal   3 × stream "Reply with exactly: pong", minimal system prompt
  frozen_system  1 × stream with the frozen queen system prompt
                 (go_hive/docs/e2e-assets/prompt-catalog/system-prompt.python.txt,
                 byte-identical to the Go side per docs/prompt-parity.md §3)
  tool_time      2 × stream "What time is it? Use get_current_time" with the
                 real harness ``get_current_time`` tool definition; round two
                 backfills the assistant tool_call + tool result the way the
                 runtimes serialize them
  unauthorized   1 × stream with a wrong API key → upstream 401

Opt-in: HIVE_REAL_LLM_E2E=1. Output JSON is written to ``--out``.

This is a probe at the provider layer: round-two messages are assembled with
the runtime's own serializers (``Message.to_llm_dict``) so the wire bodies
reflect each side's real conventions; the full agent loop (reminders, judge,
system-reminder injection) is out of scope for this lane.
"""

from __future__ import annotations

import argparse
import asyncio
import dataclasses
import hashlib
import json
import os
import sys
import time
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
for _extra in (REPO_ROOT / "core", REPO_ROOT / "tools" / "src"):
    if _extra.is_dir() and str(_extra) not in sys.path:
        sys.path.insert(0, str(_extra))

USER_PONG_PROMPT = "Reply with exactly: pong"
TOOL_PROMPT = "What time is it? Use get_current_time"
MINIMAL_SYSTEM = "You are a crosscheck probe. Follow the user's instruction exactly and reply with nothing else."
FROZEN_SYSTEM_PATH = (
    REPO_ROOT / "go_hive" / "docs" / "e2e-assets" / "prompt-catalog" / "system-prompt.python.txt"
)
BAD_KEY = "sk-llm-crosscheck-invalid-000"

ALL_SCENARIOS = ("chat_minimal", "frozen_system", "tool_time", "unauthorized")


def log(message: str) -> None:
    print(f"[llm-crosscheck-python] {message}", flush=True)


def event_to_dict(event: Any) -> dict[str, Any]:
    data = dataclasses.asdict(event)
    # dataclasses.asdict is the shared projection: same keys the Go runner
    # records via llm.StreamEventToMap for the fields this lane compares.
    return data


def sha256_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


class PythonSide:
    def __init__(self) -> None:
        from framework.pipeline.stages.llm_provider import LlmProviderStage

        self.stage = LlmProviderStage()
        self.provider: Any = None

    async def initialize(self) -> None:
        await self.stage.initialize()
        self.provider = self.stage.llm
        # Same probe parameter both sides: temperature=0 through the
        # provider's extra kwargs (the normal agent path never sets one).
        self.provider.extra_kwargs["temperature"] = 0.0

    # ------------------------------------------------------------------
    # recording helpers
    # ------------------------------------------------------------------

    async def _stream_once(
        self,
        *,
        messages: list[dict[str, Any]],
        system: str,
        tools: list[Any] | None,
        api_key_override: str | None = None,
    ) -> dict[str, Any]:
        saved_key = self.provider.api_key
        if api_key_override is not None:
            self.provider.api_key = api_key_override
        started = time.time()
        events: list[dict[str, Any]] = []
        try:
            async for event in self.provider.stream(
                messages=messages,
                system=system,
                tools=tools,
            ):
                events.append(event_to_dict(event))
        finally:
            self.provider.api_key = saved_key
        return self._fold_events(events, started)

    @staticmethod
    def _fold_events(events: list[dict[str, Any]], started: float) -> dict[str, Any]:
        final_text = ""
        finish_reason = ""
        usage: dict[str, Any] | None = None
        tool_calls: list[dict[str, Any]] = []
        error: dict[str, Any] | None = None
        reasoning_chunks: list[str] = []
        for event in events:
            etype = event.get("type")
            if etype == "text_delta":
                final_text = event.get("snapshot", final_text)
            elif etype == "text_end":
                final_text = event.get("full_text", final_text)
            elif etype == "reasoning_delta":
                reasoning_chunks.append(event.get("content", ""))
            elif etype == "tool_call":
                tool_calls.append(
                    {
                        "id": event.get("tool_use_id", ""),
                        "name": event.get("tool_name", ""),
                        "arguments_parsed": event.get("tool_input", {}),
                    }
                )
            elif etype == "finish":
                finish_reason = event.get("stop_reason", "")
                usage = {
                    "prompt_tokens": event.get("input_tokens", 0),
                    "completion_tokens": event.get("output_tokens", 0),
                    "total_tokens": event.get("input_tokens", 0) + event.get("output_tokens", 0),
                    "cached_tokens": event.get("cached_tokens", 0),
                }
            elif etype == "error":
                error = {
                    "error": event.get("error", ""),
                    "recoverable": event.get("recoverable", False),
                    "error_type": event.get("error_type"),
                    "upstream_status": event.get("upstream_status"),
                }
        return {
            "events": events,
            "event_types": [e.get("type") for e in events],
            "final_text": final_text,
            "finish_reason": finish_reason,
            "usage": usage,
            "tool_calls": tool_calls,
            "reasoning_content": "".join(reasoning_chunks),
            "error": error,
            "wall_ms": round((time.time() - started) * 1000, 1),
        }

    # ------------------------------------------------------------------
    # scenarios
    # ------------------------------------------------------------------

    async def chat_minimal(self, reps: int) -> dict[str, Any]:
        results = []
        for index in range(reps):
            call = await self._stream_once(
                messages=[{"role": "user", "content": USER_PONG_PROMPT}],
                system=MINIMAL_SYSTEM,
                tools=None,
            )
            call["rep"] = index
            results.append(call)
        return {"reps": results}

    async def frozen_system(self, reps: int) -> dict[str, Any]:
        prompt_text = FROZEN_SYSTEM_PATH.read_text(encoding="utf-8")
        results = []
        for index in range(reps):
            call = await self._stream_once(
                messages=[{"role": "user", "content": USER_PONG_PROMPT}],
                system=prompt_text,
                tools=None,
            )
            call["rep"] = index
            results.append(call)
        return {
            "system_source": str(FROZEN_SYSTEM_PATH.relative_to(REPO_ROOT)),
            "system_sha256": sha256_text(prompt_text),
            "system_chars": len(prompt_text),
            "reps": results,
        }

    def _time_tool(self) -> tuple[Any, Any]:
        """The real harness get_current_time: framework Tool + in-process invoke."""
        from framework.llm.provider import Tool
        from framework.loader.tool_registry import ToolRegistry
        from framework.tools.harness_tools import build_harness_group

        harness_tool = next(
            t for t in build_harness_group("hive_tools", scope_env=lambda: {}) if t.name == "get_current_time"
        )
        # Same conversion ToolRegistry applies when registering a harness tool
        # (CONTEXT_PARAMS stripping included). The method only reads class
        # attributes, so a minimal shim instance is enough to reuse it.
        shim = type(
            "_RegistryShim",
            (),
            {
                "CONTEXT_PARAMS": ToolRegistry.CONTEXT_PARAMS,
                "CONCURRENCY_SAFE_TOOLS": ToolRegistry.CONCURRENCY_SAFE_TOOLS,
            },
        )()
        framework_tool = ToolRegistry._convert_mcp_tool_to_framework_tool(shim, harness_tool)
        tool = Tool(
            name=framework_tool.name,
            description=framework_tool.description,
            parameters=framework_tool.parameters,
            produces_image=framework_tool.produces_image,
            concurrency_safe=framework_tool.concurrency_safe,
        )
        return tool, harness_tool

    async def tool_time(self, reps: int) -> dict[str, Any]:
        tool, harness_tool = self._time_tool()
        wire_tool = self.provider._tool_to_openai_format(tool)
        results = []
        for index in range(reps):
            round1 = await self._stream_once(
                messages=[{"role": "user", "content": TOOL_PROMPT}],
                system=MINIMAL_SYSTEM,
                tools=[tool],
            )
            round2 = None
            if round1["tool_calls"]:
                call = round1["tool_calls"][0]
                tool_args = call["arguments_parsed"] if isinstance(call["arguments_parsed"], dict) else {}
                try:
                    raw_output = harness_tool.invoke(tool_args)
                    # Harness tools return the model-facing string already
                    # (get_current_time returns JSON text); a structured value
                    # is serialized once.
                    tool_content = raw_output if isinstance(raw_output, str) else json.dumps(raw_output, ensure_ascii=False)
                    tool_error = False
                except Exception as exc:  # noqa: BLE001 - recorded, not raised
                    tool_content = f"{type(exc).__name__}: {exc}"
                    tool_error = True

                # The runtime's own serializers for the follow-up turn.
                from framework.agent_loop.conversation import Message

                tc_dicts = [
                    {
                        "id": call["id"],
                        "type": "function",
                        "function": {
                            "name": call["name"],
                            "arguments": json.dumps(call["arguments_parsed"]),
                        },
                    }
                ]
                round2_messages = [
                    Message(seq=0, role="user", content=TOOL_PROMPT).to_llm_dict(),
                    Message(
                        seq=1,
                        role="assistant",
                        content=round1["final_text"],
                        tool_calls=tc_dicts,
                        reasoning_content=round1["reasoning_content"] or None,
                    ).to_llm_dict(),
                    Message(
                        seq=2,
                        role="tool",
                        content=tool_content,
                        tool_use_id=call["id"],
                        is_error=tool_error,
                    ).to_llm_dict(),
                ]
                round2 = await self._stream_once(
                    messages=round2_messages,
                    system=MINIMAL_SYSTEM,
                    tools=[tool],
                )
            call_result = {
                "rep": index,
                "round1": round1,
                "round2": round2,
                "tool_definition": {
                    "framework": {
                        "name": tool.name,
                        "description": tool.description,
                        "parameters": tool.parameters,
                    },
                    "wire": wire_tool,
                },
            }
            results.append(call_result)
        return {"reps": results}

    async def unauthorized(self, reps: int) -> dict[str, Any]:
        results = []
        for index in range(reps):
            call = await self._stream_once(
                messages=[{"role": "user", "content": USER_PONG_PROMPT}],
                system=MINIMAL_SYSTEM,
                tools=None,
                api_key_override=BAD_KEY,
            )
            call["rep"] = index
            results.append(call)
        return {"reps": results}


async def main_async(args: argparse.Namespace) -> int:
    if os.environ.get("HIVE_REAL_LLM_E2E") != "1":
        log("opt-in required: set HIVE_REAL_LLM_E2E=1")
        return 2
    side = PythonSide()
    await side.initialize()
    provider = side.provider
    log(f"provider model={provider.model!r} api_base={provider.api_base!r}")

    scenarios = args.scenarios.split(",") if args.scenarios else list(ALL_SCENARIOS)
    out: dict[str, Any] = {
        "side": "python",
        "provider": {
            "model": provider.model,
            "api_base": provider.api_base,
            "extra_kwargs": {k: v for k, v in provider.extra_kwargs.items() if k != "api_key"},
        },
        "tenant": {
            "hive_home": os.environ.get("HIVE_HOME", ""),
            "python": sys.version.split()[0],
        },
        "scenarios": {},
    }

    for name in scenarios:
        log(f"scenario {name}")
        runner = getattr(side, name)
        reps = args.reps if name == "chat_minimal" else 1
        out["scenarios"][name] = await runner(reps)
        summary = out["scenarios"][name].get("reps", [{}])
        if name == "tool_time":
            log(
                f"  round1 tool_calls={[c['name'] for c in summary[0].get('round1', {}).get('tool_calls', [])]} "
                f"round2_text={summary[0].get('round2', {}).get('final_text', '')[:40]!r}"
            )
        else:
            log(f"  text={summary[0].get('final_text', '')[:60]!r} finish={summary[0].get('finish_reason')!r}")

    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(out, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
    log(f"wrote {out_path}")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, help="result JSON path")
    parser.add_argument("--scenarios", default="", help="comma list (default: all)")
    parser.add_argument("--reps", type=int, default=3, help="reps for chat_minimal/tool_time")
    args = parser.parse_args()
    return asyncio.run(main_async(args))


if __name__ == "__main__":
    raise SystemExit(main())
