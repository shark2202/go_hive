#!/usr/bin/env python3
"""Capture the Python runtime-layer tool schemas (schema-residuals lane).

The freeze contract used to reference the queen-lifecycle tools from the
Tool Library layer (name + description only, empty input schema). That layer is
not what the model sees: the model receives the registered ``Tool`` objects
projected through the request layer
(``framework/llm/litellm.py::LiteLLMProvider._tool_to_openai_format``), which
materializes ``type`` / ``properties`` / ``required``.

This probe runs the real registration path without any LLM:

  1. ``register_queen_lifecycle_tools(recording_registry, mock_session)`` from
     ``core/framework/tools/queen_lifecycle_tools.py`` (which also registers
     ``playbook_tools.py``'s four playbook tools) — identical to the dump the
     parity generator uses;
  2. the session pivot overlay for ``task_create``: ``build_session_tools``
     with the DM-phase ``PivotHandler`` (``new_session``), whose description
     text is extracted verbatim from ``queen_orchestrator.py``'s
     ``_NEW_SESSION_DESC`` via ``ast`` so the capture cannot drift from the
     runtime wiring;
  3. the request-layer projection for every captured tool;
  4. phase visibility from ``agents/queen/nodes/__init__.py``'s
     ``_QUEEN_INDEPENDENT_TOOLS`` / ``_QUEEN_COLONY_TOOLS``.

Run (needs the repo venv; no network):

    /Users/mac/hive/.venv/bin/python go_hive/docs/e2e-assets/schema-residuals/py_runtime_schema_probe.py

Writes ``python-runtime-tools.json`` next to itself (or --out DIR).
"""

from __future__ import annotations

import argparse
import ast
import hashlib
import json
import pathlib
import subprocess
import sys
from datetime import datetime, timezone

HERE = pathlib.Path(__file__).resolve().parent
DEFAULT_REPO = HERE.parents[3]  # go_hive/docs/e2e-assets/schema-residuals -> repo root

# The 12 same-name lifecycle tools whose contract reference this probe rebases
# onto the runtime layer.
LIFECYCLE_TOOLS = [
    "get_playbook_status",
    "get_worker_status",
    "inject_message",
    "list_playbook_runs",
    "remove_trigger",
    "reply_to_worker",
    "run_playbook",
    "run_worker",
    "set_trigger",
    "stop_playbook",
    "stop_worker",
    "update_worker_profile",
]


class RecordingRegistry:
    def __init__(self) -> None:
        self.tools = {}

    def register(self, name, tool, handler=None):
        self.tools[name] = tool


class MockSession:
    """Bare session object; handlers never run during registration."""

    colony_id = None
    colony_runtime = None
    event_bus = None


def _git(repo: pathlib.Path, *args: str) -> str:
    try:
        return subprocess.run(
            ["git", *args], cwd=repo, capture_output=True, text=True, check=True
        ).stdout.strip()
    except Exception as exc:  # pragma: no cover - provenance only
        return f"<unavailable: {exc}>"


def _sha256_json(value) -> str:
    canonical = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def extract_new_session_desc(repo: pathlib.Path) -> str:
    """Pull _NEW_SESSION_DESC verbatim from queen_orchestrator.py."""
    source_path = repo / "core/framework/server/queen_orchestrator.py"
    tree = ast.parse(source_path.read_text(encoding="utf-8"))
    for node in ast.walk(tree):
        if isinstance(node, ast.Assign) and len(node.targets) == 1:
            target = node.targets[0]
            if isinstance(target, ast.Name) and target.id == "_NEW_SESSION_DESC":
                value = ast.literal_eval(node.value)
                if not isinstance(value, str):
                    raise SystemExit("_NEW_SESSION_DESC is not a string")
                return value
    raise SystemExit("_NEW_SESSION_DESC not found in queen_orchestrator.py")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=pathlib.Path, default=DEFAULT_REPO,
                        help="repo root containing core/ (default: worktree root)")
    parser.add_argument("--out", type=pathlib.Path, default=HERE,
                        help="output directory (default: this script's directory)")
    args = parser.parse_args()
    repo = args.repo.resolve()
    core = repo / "core"
    if not (core / "framework").is_dir():
        raise SystemExit(f"core/ not found under {repo}")

    sys.path.insert(0, str(core))
    from framework.llm.litellm import LiteLLMProvider  # noqa: PLC0415
    from framework.tasks.tools.session_tools import PivotHandler, build_session_tools  # noqa: PLC0415
    from framework.tools.queen_lifecycle_tools import register_queen_lifecycle_tools  # noqa: PLC0415
    from framework.agents.queen.nodes import _QUEEN_COLONY_TOOLS, _QUEEN_INDEPENDENT_TOOLS  # noqa: PLC0415

    registry = RecordingRegistry()
    registered = register_queen_lifecycle_tools(
        registry, session=MockSession(), session_id="schema-residuals-runtime-probe"
    )

    def request_layer(tool) -> dict:
        fmt = LiteLLMProvider._tool_to_openai_format(None, tool)
        return {
            "description": fmt["function"]["description"],
            "parameters": fmt["function"]["parameters"],
        }

    lifecycle = {}
    for name in sorted(registry.tools):
        tool = registry.tools[name]
        projected = request_layer(tool)
        entry = {
            "registered": True,
            "description": projected["description"],
            "parameters": projected["parameters"],
            "parameters_raw_tool_object": tool.parameters,
            "phase_visible": {
                "independent": name in _QUEEN_INDEPENDENT_TOOLS,
                "colony": name in _QUEEN_COLONY_TOOLS,
            },
            "request_projection": "framework/llm/litellm.py::LiteLLMProvider._tool_to_openai_format",
        }
        entry["sha256_parameters"] = _sha256_json(entry["parameters"])
        lifecycle[name] = entry

    missing = [name for name in LIFECYCLE_TOOLS if name not in lifecycle]
    if missing:
        raise SystemExit(f"lifecycle tools missing from the runtime registry: {missing}")

    # task_create runtime schema with the DM-phase session pivot overlay.
    new_session_desc = extract_new_session_desc(repo)
    pivot = PivotHandler(
        field_name="new_session",
        field_description=new_session_desc,
        handle=lambda **_kwargs: {"success": True},
    )
    task_tools = {}
    for tool, _executor in build_session_tools(store=object(), pivot_handler=pivot):
        projected = request_layer(tool)
        task_tools[tool.name] = {
            "description": projected["description"],
            "parameters": projected["parameters"],
            "sha256_parameters": _sha256_json(projected["parameters"]),
        }
    plain_tools = {}
    for tool, _executor in build_session_tools(store=object(), pivot_handler=None):
        plain_tools[tool.name] = {
            "description": request_layer(tool)["description"],
            "parameters": request_layer(tool)["parameters"],
        }

    payload = {
        "captured_at_utc": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "repo": {
            "root": str(repo),
            "head": _git(repo, "rev-parse", "HEAD"),
            "core_head": _git(repo, "log", "-1", "--format=%H", "--", "core"),
            "queen_lifecycle_tools_sha256": hashlib.sha256(
                (core / "framework/tools/queen_lifecycle_tools.py").read_bytes()
            ).hexdigest(),
            "playbook_tools_sha256": hashlib.sha256(
                (core / "framework/tools/playbook_tools.py").read_bytes()
            ).hexdigest(),
            "session_tools_sha256": hashlib.sha256(
                (core / "framework/tasks/tools/session_tools.py").read_bytes()
            ).hexdigest(),
            "note": (
                "Captured from the repo's runtime without an LLM: registered Tool objects "
                "projected through the request layer. queen_lifecycle_tools.py / "
                "playbook_tools.py / session_tools.py are unchanged since the frozen "
                "reference commit a756b1e5; only memory-recall code moved in 1.0.15."
            ),
        },
        "registered_lifecycle_tool_count": registered,
        "lifecycle_tools_of_interest": LIFECYCLE_TOOLS,
        "lifecycle": lifecycle,
        "task_create": {
            "runtime_dm_pivot": task_tools.get("task_create"),
            "plain_no_pivot": plain_tools.get("task_create"),
            "pivot_field": "new_session",
            "pivot_description_source": "core/framework/server/queen_orchestrator.py::_NEW_SESSION_DESC (ast-extracted)",
        },
    }

    out_dir = args.out.resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    out_path = out_dir / "python-runtime-tools.json"
    out_path.write_text(json.dumps(payload, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")

    aligned = [
        name for name in LIFECYCLE_TOOLS
        if name in lifecycle and lifecycle[name]["parameters"]
    ]
    print(f"registered lifecycle tools: {registered}")
    print(f"captured {len(aligned)}/{len(LIFECYCLE_TOOLS)} lifecycle tools + task_create pivot schema")
    print(f"wrote {out_path} ({out_path.stat().st_size} bytes)")
    for name in LIFECYCLE_TOOLS:
        entry = lifecycle[name]
        print(f"  {name:24s} sha256(parameters)={entry['sha256_parameters'][:16]} "
              f"phase={entry['phase_visible']}")


if __name__ == "__main__":
    main()
