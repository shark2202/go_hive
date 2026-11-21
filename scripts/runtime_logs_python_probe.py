#!/usr/bin/env python3
"""Python half of the three-level runtime-log parity run (lane tool-logs-runtime).

Replays the same deterministic scenario as the Go harness
(go_hive/go-hive/e2e/cmd/runtimelogs) through the real production classes
``framework.tracker.runtime_log_store.RuntimeLogStore`` and
``framework.tracker.runtime_logger.RuntimeLogger``:

  run 1  session_20250101_000000_dualog01 — two steps (a RETRY with one timed
         web_search tool call, an ACCEPT), a successful node, end_run
         success/clean.
  run 2  session_20250101_000000_dualog02 — one partial error step (stacktrace,
         is_partial) and a failed node; end_run failure/failed.

The trace context is pinned to fixed ids on both sides; only the random span_id
differs (the diff script compares its shape).

Usage:
  python go_hive/scripts/runtime_logs_python_probe.py --out <dir>

Writes <dir>/sessions/<run_id>/logs/{tool_logs.jsonl,details.jsonl,summary.json}
(the upstream layout for a base path of <dir>/runtime_logs) and prints a JSON
manifest.
"""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

RUN1_ID = "session_20250101_000000_dualog01"
RUN2_ID = "session_20250101_000000_dualog02"

TRACE_ID = "a1b2c3d4e5f6789012345678abcdef01"
EXECUTION_ID = "b2c3d4e5f6789012345678abcdef0123"

TOOL_START = "2026-01-01T00:00:00.000000+00:00"
TIMEOUT_STACKTRACE = (
    "Traceback (most recent call last):\n"
    '  File "test.py", line 10, in run\n'
    "    raise TimeoutError()\n"
    "TimeoutError: Connection timeout"
)


def bootstrap(out_dir: Path) -> None:
    """Make ``core/`` importable and give framework.config a HOME to resolve."""
    core_dir = Path(__file__).resolve().parents[2] / "core"
    if str(core_dir) not in sys.path:
        sys.path.insert(0, str(core_dir))
    os.environ.setdefault("HIVE_HOME", str(out_dir / "home"))


def main(argv: list[str]) -> int:
    out_dir: Path | None = None
    read_dir: Path | None = None
    args = list(argv[1:])
    while args:
        arg = args.pop(0)
        if arg == "--out" and args:
            out_dir = Path(args.pop(0))
        elif arg == "--read" and args:
            read_dir = Path(args.pop(0))
        else:
            print(f"runtime_logs_python_probe: unknown arg {arg}", file=sys.stderr)
            return 2
    if read_dir is not None:
        bootstrap(read_dir)
        import asyncio

        return asyncio.run(_read(read_dir))
    if out_dir is None:
        print("runtime_logs_python_probe: --out is required", file=sys.stderr)
        return 2
    out_dir.mkdir(parents=True, exist_ok=True)
    bootstrap(out_dir)

    import asyncio

    return asyncio.run(_run(out_dir))


async def _read(read_dir: Path) -> int:
    """Read a store tree through the Python store API (cross-read evidence).

    Prints the same stable readback shape as `runtimelogs --read` on the Go
    side: no random ids, so the two JSON documents compare equal.
    """
    from framework.tracker.runtime_log_store import RuntimeLogStore

    store = RuntimeLogStore(read_dir / "runtime_logs")
    runs = await store.list_runs()
    out = []
    for summary in runs:
        record = {
            "run_id": summary.run_id,
            "status": summary.status,
            "steps": 0,
            "details": 0,
            "verdicts": [],
            "tool_names": [],
            "needs_attention": summary.needs_attention,
        }
        tool_logs = await store.load_tool_logs(summary.run_id)
        if tool_logs is not None:
            record["steps"] = len(tool_logs.steps)
            for step in tool_logs.steps:
                record["verdicts"].append(step.verdict)
                for call in step.tool_calls:
                    record["tool_names"].append(call.tool_name)
        details = await store.load_details(summary.run_id)
        if details is not None:
            record["details"] = len(details.nodes)
        out.append(record)
    print(json.dumps({"runs": out}, indent=2, ensure_ascii=False))
    return 0


async def _run(out_dir: Path) -> int:
    from framework.observability import clear_trace_context, set_trace_context
    from framework.tracker.runtime_logger import RuntimeLogger
    from framework.tracker.runtime_log_store import RuntimeLogStore

    set_trace_context(trace_id=TRACE_ID, execution_id=EXECUTION_ID)
    try:
        store = RuntimeLogStore(out_dir / "runtime_logs")

        # ---- run 1: the happy path -------------------------------------
        logger = RuntimeLogger(store=store, agent_id="agent-parity")
        logger.start_run(goal_id="goal-parity", session_id=RUN1_ID)
        logger.log_step(
            node_id="node-1",
            node_type="event_loop",
            step_index=0,
            llm_text="Searching for the answer.",
            tool_calls=[
                {
                    "tool_use_id": "tc_1",
                    "tool_name": "web_search",
                    "tool_input": {"query": "hive parity"},
                    "content": "Found 3 results",
                    "is_error": False,
                    "start_timestamp": TOOL_START,
                    "duration_s": 0.125,
                }
            ],
            input_tokens=100,
            output_tokens=50,
            latency_ms=1200,
            verdict="RETRY",
            verdict_feedback="Missing output keys: ['result']",
        )
        logger.log_step(
            node_id="node-1",
            node_type="event_loop",
            step_index=1,
            llm_text="Here is the answer.",
            tool_calls=[],
            input_tokens=90,
            output_tokens=40,
            latency_ms=800,
            verdict="ACCEPT",
            verdict_feedback="All outputs set",
        )
        logger.log_node_complete(
            node_id="node-1",
            node_name="Search Node",
            node_type="event_loop",
            success=True,
            total_steps=2,
            tokens_used=280,
            input_tokens=190,
            output_tokens=90,
            latency_ms=2000,
            attempt=1,
            exit_status="success",
            accept_count=1,
            retry_count=1,
        )
        await logger.end_run(
            status="success",
            duration_ms=2000,
            node_path=["node-1"],
            execution_quality="clean",
        )

        # ---- run 2: the failure path -----------------------------------
        logger2 = RuntimeLogger(store=store, agent_id="agent-parity")
        logger2.start_run(goal_id="goal-parity", session_id=RUN2_ID)
        logger2.log_step(
            node_id="node-1",
            node_type="event_loop",
            step_index=0,
            error="LLM call failed: Connection timeout",
            stacktrace=TIMEOUT_STACKTRACE,
            is_partial=True,
            latency_ms=1500,
        )
        logger2.log_node_complete(
            node_id="node-1",
            node_name="Search Node",
            node_type="event_loop",
            success=False,
            error="LLM call failed: Connection timeout",
            stacktrace=TIMEOUT_STACKTRACE,
            total_steps=1,
            latency_ms=1500,
            attempt=1,
            exit_status="failure",
        )
        await logger2.end_run(
            status="failure",
            duration_ms=1500,
            node_path=["node-1"],
            execution_quality="failed",
        )

        manifest = {"out": str(out_dir), "runs": []}
        for run_id in (RUN1_ID, RUN2_ID):
            run_dir = store._get_run_dir(run_id)
            manifest["runs"].append(
                {
                    "run_id": run_id,
                    "dir": str(run_dir),
                    "tool_logs": str(run_dir / "tool_logs.jsonl"),
                    "details": str(run_dir / "details.jsonl"),
                    "summary": str(run_dir / "summary.json"),
                }
            )
        print(json.dumps(manifest, indent=2, ensure_ascii=False))
        return 0
    finally:
        clear_trace_context()


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
