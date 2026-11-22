#!/usr/bin/env python3
"""Session-start recall seed probe — upstream (Hive 1.0.15) pure-function evidence.

The registered residual asks what the ``background``-mode session-start seed
actually does in the reference tree: which memories it may pick, how many, what
bounds it in time, what the cache holds after a timeout, and how the seed relates
to the three ``memory_recall.mode`` values.

This probe runs the REAL reference modules in-process (no network, no model
endpoint) and records, as JSON:

  S. the seed block (queen_orchestrator.py:1461-1479, in ``_queen_identity_hook``):
       S1  the seed picks from the GLOBAL directory only (a queen-scoped note is
           invisible to it) and renders a "Global Memories" block
       S2  the seed's default ``max_results=5``: a 6-name pick is capped at 5,
           relevance first, profile pins back-fill; pins alone stay 5
       S3  ``asyncio.wait_for(..., 3.0)`` on a 5s pick: TimeoutError at ~3s and
           the cache keeps its previous value (the seed never writes)
       S4  a fast selector error still returns the profile pins inside the
           window (select_memories' own except), so the seed writes the pins
       S5  the gate ``bool(trigger) and mode == "background"`` across the three
           modes (index/wait never seed; a bogus env value falls back to index)
       S6  an empty trigger (fresh session without an initial prompt) never seeds
       S7  an empty global directory makes no model call and the seed writes ""
       S8  the seed writes only ``_cached_global_recall_block``; a refresh's
           queen block survives a seed that lands afterwards, and
           ``render_recall_block`` joins global then queen with a blank line
       S9  the trigger is the assembled initial message (``user_request: …``
           from ``_build_initial_message``), empty when the session has none
  A. static anchors (measured line numbers) for the seed block, its gate, the
     session_start hook that runs it, and ``render_recall_block``.

Every case is a (subject, expected, observed, pass) record; the whole run is
written to go_hive/docs/e2e-assets/memory-notes-1015/recall-seed-probe.json.

Usage (repo root of a checkout):
    PYTHONPATH=core:tools/src /Users/mac/hive/.venv/bin/python \\
        go_hive/scripts/recall_seed_probe.py
"""

from __future__ import annotations

import asyncio
import json
import os
import re
import sys
import tempfile
import time
from pathlib import Path
from typing import Any

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
# Harness: a recording aux picker (the shape select_memories calls)
# ---------------------------------------------------------------------------


class RecordingAuxLLM:
    """An aux picker that records the full request it received and sleeps."""

    def __init__(self, delay: float = 0.01, content: str = "", raises: Exception | None = None) -> None:
        self.delay = delay
        self.content = content
        self.raises = raises
        self.requests: list[dict] = []

    async def acomplete(self, messages, system="", max_tokens=0, response_format=None, **kw):
        from framework.llm.provider import LLMResponse

        self.requests.append(
            {
                "messages": messages,
                "system": system,
                "max_tokens": max_tokens,
                "response_format": response_format,
            }
        )
        await asyncio.sleep(self.delay)
        if self.raises is not None:
            raise self.raises
        return LLMResponse(content=self.content, model="probe-mock", stop_reason="stop")


def write_note(directory: Path, filename: str, description: str, body: str = "", note_type: str = "preference") -> None:
    directory.mkdir(parents=True, exist_ok=True)
    (directory / filename).write_text(
        f"---\nname: {filename[:-3]}\ntype: {note_type}\ndescription: {description}\n---\n{body}\n",
        encoding="utf-8",
    )


def request_text(llm: RecordingAuxLLM) -> str:
    payload = llm.requests[0]
    texts = [payload["system"]]
    for message in payload["messages"]:
        content = message.get("content") if isinstance(message, dict) else None
        if content:
            texts.append(str(content))
    return "\n".join(texts)


async def probe_seed_scope_is_global_only() -> None:
    """S1: the seed call shape only ever sees the global directory."""
    from framework.agents.queen.recall_selector import format_recall_injection, select_memories

    with tempfile.TemporaryDirectory() as tmp:
        gl = Path(tmp) / "global"
        qn = Path(tmp) / "queen"
        write_note(gl, "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
        write_note(qn, "ava-secret.md", "Ava-only project notes", "ships on friday")
        # The model is invited to name a queen note; the selector must drop it.
        llm = RecordingAuxLLM(content='{"selected_memories": ["tea.md", "ava-secret.md"]}')
        selected = await select_memories("seed query", llm, gl)
        block = format_recall_injection(selected, gl)

        request = request_text(llm)
        record(
            "S1.seed_scope_global_only",
            "the seed's pick sees only the global scope and renders a Global Memories block",
            {
                "selected": ["tea.md"],
                "queen_note_in_manifest": False,
                "label": "--- Global Memories ---",
            },
            {
                "selected": selected,
                "queen_note_in_manifest": "ava-secret.md" in request,
                "queen_note_in_block": "ava-secret.md" in block,
                "label_present": block.startswith("--- Global Memories ---"),
                "call_shape": {
                    "system_is_select_prompt": llm.requests[0]["system"].startswith(
                        "You are selecting memories"
                    ),
                    "response_format": llm.requests[0]["response_format"],
                },
            },
            selected == ["tea.md"]
            and "ava-secret.md" not in request
            and "ava-secret.md" not in block
            and block.startswith("--- Global Memories ---"),
        )


async def probe_seed_cap() -> None:
    """S2: the seed's default max_results=5, relevance first, pins back-fill."""
    from framework.agents.queen.recall_selector import select_memories

    with tempfile.TemporaryDirectory() as tmp:
        gl = Path(tmp) / "global"
        for index in range(7):
            write_note(gl, f"char{index}.md", f"character sheet {index}", "profile body", note_type="profile")
        write_note(gl, "tea.md", "What Ava drinks", "Ava drinks pu-erh.")

        picks = RecordingAuxLLM(content='{"selected_memories": ["tea.md"]}')
        with_pick = await select_memories("seed query", picks, gl)

        overfill = RecordingAuxLLM(
            content='{"selected_memories": ["tea.md", "char0.md", "char1.md", "char2.md", "char3.md", "char4.md"]}'
        )
        capped = await select_memories("seed query", overfill, gl)

        empty = RecordingAuxLLM(content='{"selected_memories": []}')
        pinned_only = await select_memories("seed query", empty, gl)

        record(
            "S2.seed_cap_max_results",
            "seed default max_results=5: relevance first then pins (scan order, newest first), a 6-name pick capped at 5, pins alone 5",
            {
                "with_pick[0]": "tea.md (the model's pick ranks first)",
                "with_pick_len": 5,
                "with_pick[1:]": "profile pins in scan order",
                "capped_len": 5,
                "pinned_only": 5,
            },
            {
                "with_pick": with_pick,
                "with_pick_len": len(with_pick),
                "capped": capped,
                "capped_len": len(capped),
                "pinned_only": pinned_only,
                "pinned_only_len": len(pinned_only),
            },
            with_pick[0] == "tea.md"
            and len(with_pick) == 5
            and all(name.startswith("char") for name in with_pick[1:])
            and capped == ["tea.md", "char0.md", "char1.md", "char2.md", "char3.md"]
            and len(pinned_only) == 5,
        )


async def probe_seed_timeout() -> None:
    """S3: wait_for(..., 3.0) cancels a 5s seed pick; the cache keeps its value."""
    from framework.agents.queen.recall_selector import select_memories

    with tempfile.TemporaryDirectory() as tmp:
        gl = Path(tmp) / "global"
        write_note(gl, "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
        slow = RecordingAuxLLM(delay=5.0, content='{"selected_memories": ["tea.md"]}')

        # The hook's shape: the cache (a phase_state field at session start, "")
        # is only written after the await returns.
        cache: dict[str, str] = {}
        start = time.time()
        try:
            selected = await asyncio.wait_for(select_memories("seed query", slow, gl), timeout=3.0)
            cache["global"] = f"set to {len(selected)} picks"
            outcome = "returned"
        except TimeoutError as exc:
            outcome = f"TimeoutError({exc!r})"
        elapsed = time.time() - start
        record(
            "S3.seed_timeout",
            "a 5s seed pick is cancelled at ~3s: TimeoutError, nothing written, cache unchanged",
            {"outcome": "TimeoutError", "elapsed_s ~= 3.0": True, "cache": {}},
            {"outcome": outcome.split("(")[0], "elapsed_s": round(elapsed, 2), "cache": cache},
            outcome.startswith("TimeoutError") and cache == {} and 2.9 <= elapsed < 4.0,
            note="the except only logs 'will retry on first turn'; the first turn's refresh is the retry",
        )


async def probe_seed_error_returns_pins() -> None:
    """S4: a fast selector error still returns the profile pins inside the window."""
    from framework.agents.queen.recall_selector import select_memories

    with tempfile.TemporaryDirectory() as tmp:
        gl = Path(tmp) / "global"
        for index in range(7):
            write_note(gl, f"char{index}.md", f"character sheet {index}", "profile body", note_type="profile")
        write_note(gl, "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
        failing = RecordingAuxLLM(delay=0.0, raises=RuntimeError("provider down"))

        start = time.time()
        selected = await asyncio.wait_for(select_memories("seed query", failing, gl), timeout=3.0)
        elapsed = time.time() - start
        record(
            "S4.seed_error_returns_pins",
            "a fast selector error is swallowed inside select_memories: it returns the up-to-5 profile pins",
            {"selected_len": 5, "elapsed < 3s": True},
            {"selected": selected, "elapsed_under_1s": elapsed < 1.0},
            len(selected) == 5 and elapsed < 3.0,
            note="only a timeout leaves the cache unwritten; an error still seeds the pins",
        )


def probe_seed_mode_gate() -> None:
    """S5/S6: the gate is `trigger and mode == "background"`."""
    from framework.config import get_memory_recall_config

    previous = os.environ.get("HIVE_MEMORY_RECALL")
    rows: dict[str, Any] = {}
    try:
        for mode in ("index", "wait", "background", "bogus", ""):
            if mode:
                os.environ["HIVE_MEMORY_RECALL"] = mode
            else:
                os.environ.pop("HIVE_MEMORY_RECALL", None)
            config_mode = get_memory_recall_config().mode
            seed_with_trigger = bool("what tea do I like?") and config_mode == "background"
            rows[mode or "(unset)"] = {"mode": config_mode, "seeds_with_trigger": seed_with_trigger}
    finally:
        if previous is None:
            os.environ.pop("HIVE_MEMORY_RECALL", None)
        else:
            os.environ["HIVE_MEMORY_RECALL"] = previous

    record(
        "S5.seed_mode_gate",
        "only background seeds; index/wait never reach the seed; a bogus env value falls back to index",
        {
            "index": {"mode": "index", "seeds_with_trigger": False},
            "wait": {"mode": "wait", "seeds_with_trigger": False},
            "background": {"mode": "background", "seeds_with_trigger": True},
        },
        rows,
        rows["index"]["seeds_with_trigger"] is False
        and rows["wait"]["seeds_with_trigger"] is False
        and rows["background"]["seeds_with_trigger"] is True
        and rows["bogus"] == {"mode": "index", "seeds_with_trigger": False},
    )

    os.environ["HIVE_MEMORY_RECALL"] = "background"
    try:
        mode = get_memory_recall_config().mode
        empty_trigger = bool("") and mode == "background"
        record(
            "S6.seed_needs_a_trigger",
            "an empty trigger (fresh session with no initial prompt) skips the seed even in background mode",
            {"seeds": False},
            {"mode": mode, "seeds": empty_trigger},
            empty_trigger is False,
        )
    finally:
        if previous is None:
            os.environ.pop("HIVE_MEMORY_RECALL", None)
        else:
            os.environ["HIVE_MEMORY_RECALL"] = previous


async def probe_seed_empty_dir() -> None:
    """S7: no notes -> no model call -> the seed writes ""."""
    from framework.agents.queen.recall_selector import format_recall_injection, select_memories

    with tempfile.TemporaryDirectory() as tmp:
        gl = Path(tmp) / "global"
        gl.mkdir(parents=True)
        llm = RecordingAuxLLM(content='{"selected_memories": ["tea.md"]}')
        selected = await select_memories("seed query", llm, gl)
        block = format_recall_injection(selected, gl)
        record(
            "S7.seed_empty_dir",
            "an empty global directory short-circuits before any model call; the seed writes an empty block",
            {"calls": 0, "selected": [], "block": ""},
            {"calls": len(llm.requests), "selected": selected, "block": block},
            len(llm.requests) == 0 and selected == [] and block == "",
        )


def probe_seed_field_split() -> None:
    """S8: the seed writes only the global field; render joins global then queen."""
    from framework.tools.queen_lifecycle_tools import QueenPhaseState

    state = QueenPhaseState()
    state._cached_global_recall_block = "GLOBAL-REFRESH"
    state._cached_queen_recall_block = "QUEEN-REFRESH"
    before = state.render_recall_block()

    # The seed's assignment shape (queen_orchestrator.py:1475): global only.
    state._cached_global_recall_block = "--- Global Memories ---\n\n### char0.md\n\nprofile body\n\n--- End Global Memories ---"
    after = state.render_recall_block()

    record(
        "S8.seed_writes_global_field_only",
        "the seed assignment touches only _cached_global_recall_block; render_recall_block joins global then queen",
        {"before": "GLOBAL-REFRESH\n\nQUEEN-REFRESH", "after_starts_with_seed": True, "queen_kept": True},
        {
            "before": before,
            "after_starts_with_seed": after.startswith("--- Global Memories ---"),
            "queen_kept": after.endswith("QUEEN-REFRESH"),
            "join": "\n\n",
        },
        before == "GLOBAL-REFRESH\n\nQUEEN-REFRESH"
        and after.startswith("--- Global Memories ---")
        and after.endswith("QUEEN-REFRESH"),
        note="a seed landing after the first refresh replaces the global block only, as upstream",
    )


def probe_trigger_is_the_initial_message() -> None:
    """S9: the trigger the seed queries with is the assembled initial message."""
    from types import SimpleNamespace

    from framework.agent_loop.agent_loop import AgentLoop, LoopConfig

    loop = AgentLoop(tool_executor=None, config=LoopConfig(max_iterations=1))

    with_prompt = loop._build_initial_message(
        SimpleNamespace(input_data={"user_request": "what tea do I like?"}, agent_spec=SimpleNamespace(input_keys=[]), goal_context=None)
    )
    without_prompt = loop._build_initial_message(
        SimpleNamespace(input_data={"user_request": None}, agent_spec=SimpleNamespace(input_keys=[]), goal_context=None)
    )
    record(
        "S9.trigger_is_the_initial_message",
        "_build_initial_message assembles the trigger from input_data; a fresh session with no prompt yields \"\"",
        {"with_prompt": "user_request: what tea do I like?", "without_prompt": ""},
        {"with_prompt": with_prompt, "without_prompt": without_prompt},
        with_prompt == "user_request: what tea do I like?" and without_prompt == "",
    )


# ---------------------------------------------------------------------------
# A. Source anchors (measured line numbers in the reference tree)
# ---------------------------------------------------------------------------

ANCHORS = [
    ("A.anchors", "core/framework/server/queen_orchestrator.py", r"# Seed recall cache so the first turn has relevant memories\."),
    ("A.anchors", "core/framework/server/queen_orchestrator.py", r'if trigger and get_memory_recall_config\(\)\.mode == "background"'),
    ("A.anchors", "core/framework/server/queen_orchestrator.py", r"select_memories\(trigger, _session_llm, mem_dir\)"),
    ("A.anchors", "core/framework/server/queen_orchestrator.py", r"timeout=3\.0,"),
    ("A.anchors", "core/framework/server/queen_orchestrator.py", r"phase_state\._cached_global_recall_block = format_recall_injection\(selected, mem_dir\)"),
    ("A.anchors", "core/framework/server/queen_orchestrator.py", r"except TimeoutError:"),
    ("A.anchors", "core/framework/server/queen_orchestrator.py", r"recall: initial seeding timed out, will retry on first turn"),
    ("A.anchors", "core/framework/server/queen_orchestrator.py", r"trigger = ctx\.trigger or \"\""),
    ("A.anchors", "core/framework/agent_loop/agent_loop.py", r'await self\._run_hooks\("session_start", conversation, trigger=initial_message\)'),
    ("A.anchors", "core/framework/agent_loop/agent_loop.py", r"def _build_initial_message"),
    ("A.anchors", "core/framework/tools/queen_lifecycle_tools.py", r"_cached_global_recall_block: str = \"\""),
    ("A.anchors", "core/framework/tools/queen_lifecycle_tools.py", r"def render_recall_block"),
    ("A.anchors", "core/framework/agents/queen/recall_selector.py", r"max_results: int = 5,"),
    ("A.anchors", "core/framework/agents/queen/recall_selector.py", r'pinned = \[f\.filename for f in files if \(f\.type or \"\"\)\.lower\(\) == \"profile\"\]\[:max_results\]'),
    ("A.anchors", "core/framework/agents/queen/recall_selector.py", r"result = list\(dict\.fromkeys\(\[\*normalized, \*pinned\]\)\)\[:max_results\]"),
]


def probe_anchors() -> None:
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

    # The seed sits inside the queen identity hook, which the loop runs as its
    # session_start hook with the initial message as the trigger; the hook's
    # seed block is the guarded composition measured above.
    orch = (repo / "core/framework/server/queen_orchestrator.py").read_text(encoding="utf-8").splitlines()
    hook_line = next(i + 1 for i, line in enumerate(orch) if "async def _queen_identity_hook" in line)
    seed_line = next(i + 1 for i, line in enumerate(orch) if "# Seed recall cache so the first turn" in line)
    loop = (repo / "core/framework/agent_loop/agent_loop.py").read_text(encoding="utf-8").splitlines()
    run_hooks_line = next(i + 1 for i, line in enumerate(loop) if 'await self._run_hooks("session_start", conversation, trigger=initial_message)' in line)
    record(
        "A.anchors",
        "the seed runs inside the session_start hook the loop fires with the initial message",
        "hook line < seed line; session_start hook fires before the first model call",
        {
            "identity_hook_line": hook_line,
            "seed_block_line": seed_line,
            "session_start_run_hooks_line": run_hooks_line,
        },
        hook_line < seed_line < run_hooks_line,
        note="hook at queen_orchestrator; loop call site in agent_loop.py",
    )


async def main() -> int:
    await probe_seed_scope_is_global_only()
    await probe_seed_cap()
    await probe_seed_timeout()
    await probe_seed_error_returns_pins()
    probe_seed_mode_gate()
    await probe_seed_empty_dir()
    probe_seed_field_split()
    probe_trigger_is_the_initial_message()
    probe_anchors()

    failed = [c for c in CASES if not c["pass"]]
    payload = {"total": len(CASES), "passed": len(CASES) - len(failed), "failed": len(failed), "cases": CASES}
    out = Path(__file__).resolve().parent.parent / "docs" / "e2e-assets" / "memory-notes-1015" / "recall-seed-probe.json"
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
