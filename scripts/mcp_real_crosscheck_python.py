#!/usr/bin/env python3
"""Python-side probe for the *real* MCP server cross-check.

Drives the production Python paths against third-party MCP servers (no fixture
server of our own):

    cd core && uv run python ../go_hive/scripts/mcp_real_crosscheck_python.py \\
        --servers <servers.json> --out <python-side.json> --home <HIVE_HOME>

Per server entry the probe registers it through ``MCPRegistry.add_local`` (the
same call ``hive mcp add`` makes), resolves it back with
``resolve_for_agent``, loads a fresh ``ToolRegistry`` from that resolved config
(the McpRegistryStage path), and records:

* the tool surface the model would see (name/description/inputSchema digest),
* tool calls (text, image, structured, unknown-tool) through the real executor,
* the raw ``MCPClient`` result shape behind those calls,
* enable/disable/reload lifecycle counts,
* what happens when the server process is killed mid-session (real crash).

The Go half is ``internal/cli/mcp_real_crosscheck_dump_test.go``; both halves
write the same JSON shape and ``go_hive/scripts/mcp_real_crosscheck.py`` diffs
them.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import signal
import subprocess
import sys
import time
from dataclasses import asdict
from pathlib import Path

# (label, tool, arguments) every probe calls, filtered by the tools the server
# actually exposes. Kept identical to the Go probe so both sides call the same
# tools with the same arguments. ``get-sum-error`` is a server-side validation
# failure (isError:true), which exercises the tool-error path rather than the
# transport.
CALL_SPECS: list[tuple[str, str, dict]] = [
    ("echo", "echo", {"message": "Hello MCP"}),
    ("get-sum", "get-sum", {"a": 19, "b": 23}),
    ("get-sum-error", "get-sum", {"a": 1}),
    ("get-tiny-image", "get-tiny-image", {}),
    ("get-structured-content", "get-structured-content", {"location": "Chicago"}),
    ("get_current_time", "get_current_time", {"timezone": "Asia/Shanghai"}),
    ("convert_time", "convert_time", {"time": "15:30", "source_timezone": "Asia/Shanghai", "target_timezone": "America/New_York"}),
    ("echo_text", "echo_text", {"text": "Hello MCP", "count": 2, "mode": "upper"}),
    ("get-resource-links", "get-resource-links", {"count": 2}),
    ("trigger-log-messages", "trigger-log-messages", {}),
    ("trigger-long-running-operation", "trigger-long-running-operation", {"duration": 2, "steps": 2}),
]
UNKNOWN_TOOL = "no_such_tool_xyz"
# Fallback substring identifying the stdio server process the crash probe
# kills; entries normally carry their own "crash_marker".
CRASH_MARKER = "server-everything"


def canonical(value) -> str:
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"))


def sha256_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def sha256_value(value) -> str:
    return sha256_text(canonical(value))


def error_record(exc: BaseException) -> dict:
    return {"ok": False, "error_type": type(exc).__name__, "error": str(exc)}


def tool_record(name: str, tool) -> dict:
    parameters = getattr(tool, "parameters", None) or {}
    description = getattr(tool, "description", "") or ""
    return {
        "registered_name": name,
        "description": description,
        "description_sha256": sha256_text(description),
        "parameters": parameters,
        "parameters_sha256": sha256_value(parameters),
    }


def descendants_of_self() -> list[tuple[int, str]]:
    """(pid, command) for every descendant of this process."""
    listing = subprocess.run(
        ["ps", "-o", "pid=,ppid=,command=", "-ax"], capture_output=True, text=True, check=True
    ).stdout.splitlines()
    children: dict[int, list[int]] = {}
    commands: dict[int, str] = {}
    for line in listing:
        parts = line.strip().split(None, 2)
        if len(parts) < 3:
            continue
        pid, ppid, command = int(parts[0]), int(parts[1]), parts[2]
        children.setdefault(ppid, []).append(pid)
        commands[pid] = command
    out: list[tuple[int, str]] = []
    stack = list(children.get(os.getpid(), []))
    while stack:
        pid = stack.pop()
        out.append((pid, commands.get(pid, "")))
        stack.extend(children.get(pid, []))
    return out


def probe_crash(marker: str) -> dict:
    """Kill every descendant whose command mentions ``marker``; report the tree."""
    killed = []
    for pid, command in descendants_of_self():
        if marker in command:
            try:
                os.kill(pid, signal.SIGKILL)
                killed.append({"pid": pid, "command": command[:200]})
            except ProcessLookupError:
                pass
    time.sleep(0.5)
    return {"killed": killed}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--servers", required=True, help="server entry fixture (shared with Go)")
    parser.add_argument("--out", required=True)
    parser.add_argument("--home", required=True, help="HIVE_HOME for the probe")
    parser.add_argument("--only", default="", help="comma-separated subset of server names")
    args = parser.parse_args()

    os.environ["HIVE_HOME"] = args.home
    Path(args.home).mkdir(parents=True, exist_ok=True)

    from framework.llm.provider import ToolUse
    from framework.loader.mcp_client import MCPClient, MCPServerConfig
    from framework.loader.mcp_registry import MCPRegistry
    from framework.loader.tool_registry import ToolRegistry

    fixture = json.loads(Path(args.servers).read_text(encoding="utf-8"))
    entries = fixture["servers"]
    wanted = {name.strip() for name in args.only.split(",") if name.strip()}
    if wanted:
        entries = [e for e in entries if e["name"] in wanted]

    registry = MCPRegistry()
    registry.initialize()

    results = []
    for entry in entries:
        name = entry["name"]
        result: dict = {
            "side": "python",
            "name": name,
            "transport": entry.get("transport", "stdio"),
            "entry": entry,
            "entry_sha256": sha256_value(entry),
        }

        # ── registry: same call `hive mcp add` makes ────────────────────
        try:
            if registry.get_server(name) is None:
                registry.add_local(
                    name=name,
                    transport=entry.get("transport", "stdio"),
                    command=entry.get("command"),
                    args=entry.get("args") or [],
                    env=entry.get("env") or {},
                    cwd=entry.get("cwd"),
                    url=entry.get("url"),
                    headers=entry.get("headers") or {},
                    socket_path=entry.get("socket_path"),
                    description="real server cross-check",
                )
            result["registry_added"] = True
            installed = registry.get_server(name) or {}
            result["installed_entry"] = installed
        except Exception as exc:  # noqa: BLE001
            result["registry_added"] = False
            result["registry_error"] = error_record(exc)

        configs = registry.resolve_for_agent(include=[name])
        result["resolved_configs"] = [asdict(c) for c in configs]

        # ── session tool surface (McpRegistryStage path) ────────────────
        surface: dict = {}
        try:
            session = ToolRegistry()
            summary = session.load_registry_servers(
                [asdict(c) for c in configs], log_summary=False, log_collisions=True
            )
            surface["load_summary"] = [
                {"server": s.get("server"), "status": s.get("status"), "tools_loaded": s.get("tools_loaded")}
                for s in summary
            ]
            server_tools = sorted(session.get_server_tool_names(name))
            all_tools = session.get_tools()
            surface["server_tool_names"] = server_tools
            surface["tools"] = [tool_record(t, all_tools[t]) for t in server_tools if t in all_tools]
            surface["namespace_prefix"] = None  # Python registers bare names

            # ── calls through the real executor ─────────────────────────
            executor = session.get_executor()
            calls: dict[str, dict] = {}
            for label, remote, arguments in CALL_SPECS:
                if remote not in all_tools:
                    continue
                try:
                    tool_result = executor(
                        ToolUse(id=f"{name}-{label}", name=remote, input=arguments)
                    )
                    calls[label] = {
                        "ok": not tool_result.is_error,
                        "content": tool_result.content,
                        "is_error": bool(tool_result.is_error),
                        "image_blocks": len(tool_result.image_content or []),
                        "image_kinds": [
                            block.get("type") for block in (tool_result.image_content or [])
                        ],
                    }
                except Exception as exc:  # noqa: BLE001
                    calls[label] = error_record(exc)
            surface["calls"] = calls

            try:
                unknown = executor(ToolUse(id=f"{name}-unknown", name=UNKNOWN_TOOL, input={}))
                surface["unknown_tool"] = {
                    "raised": False,
                    "ok": not unknown.is_error,
                    "is_error": bool(unknown.is_error),
                    "content": unknown.content,
                }
            except Exception as exc:  # noqa: BLE001
                surface["unknown_tool"] = error_record(exc) | {"raised": True}
        except Exception as exc:  # noqa: BLE001
            surface["error"] = error_record(exc)
        result["surface"] = surface

        # ── raw client: exact result shape + unknown tool error body ────
        raw: dict = {}
        client = MCPClient(MCPServerConfig(**{k: v for k, v in asdict(configs[0]).items()})) if configs else None
        if client is not None:
            try:
                client.connect()
                raw["connected"] = True
                listed = client.list_tools()
                raw["tool_names"] = sorted(t.name for t in listed)
                raw["tools_sha256"] = {
                    t.name: sha256_value({"description": t.description, "inputSchema": t.input_schema})
                    for t in listed
                }
                raw["tools"] = [
                    {"name": t.name, "description": t.description, "inputSchema": t.input_schema}
                    for t in listed
                ]
                raw_calls: dict[str, dict] = {}
                exposed = {t.name for t in listed}
                for label, remote, arguments in CALL_SPECS:
                    if remote not in exposed:
                        continue
                    try:
                        value = client.call_tool(remote, arguments)
                        if isinstance(value, dict) and "_images" in value:
                            raw_calls[label] = {
                                "ok": True,
                                "shape": "text+images",
                                "text": value.get("_text"),
                                "image_blocks": len(value.get("_images") or []),
                                "image_types": [
                                    b.get("type") for b in (value.get("_images") or [])
                                ],
                            }
                        else:
                            raw_calls[label] = {"ok": True, "shape": type(value).__name__, "value": value}
                    except Exception as exc:  # noqa: BLE001
                        raw_calls[label] = error_record(exc)
                raw["calls"] = raw_calls
                try:
                    client.call_tool(UNKNOWN_TOOL, {})
                    raw["unknown_tool"] = {"raised": False}
                except Exception as exc:  # noqa: BLE001
                    raw["unknown_tool"] = error_record(exc) | {"raised": True}
            except Exception as exc:  # noqa: BLE001
                raw["connected"] = False
                raw["connect_error"] = error_record(exc)
            finally:
                try:
                    client.disconnect()
                except Exception:  # noqa: BLE001
                    pass
        result["raw"] = raw

        # ── lifecycle: disable / enable / reload ────────────────────────
        lifecycle: dict = {}
        try:
            registry.disable(name)
            disabled = registry.resolve_for_agent(include=[name])
            lifecycle["disabled_resolved"] = [c.name for c in disabled]
            disabled_session = ToolRegistry()
            disabled_session.load_registry_servers(
                [asdict(c) for c in disabled], log_summary=False
            )
            lifecycle["disabled_tool_count"] = len(disabled_session.get_server_tool_names(name))
            registry.enable(name)
            reenabled = registry.resolve_for_agent(include=[name])
            lifecycle["reenabled_resolved"] = [c.name for c in reenabled]
            reloaded_session = ToolRegistry()
            reloaded_session.load_registry_servers(
                [asdict(c) for c in reenabled], log_summary=False
            )
            lifecycle["reenabled_tool_count"] = len(reloaded_session.get_server_tool_names(name))
            lifecycle["registry_has_reload_method"] = hasattr(MCPRegistry, "reload")
        except Exception as exc:  # noqa: BLE001
            lifecycle["error"] = error_record(exc)
        result["lifecycle"] = lifecycle

        # ── crash: kill the server process, call again on the live client ─
        crash: dict = {}
        if result["transport"] == "stdio" and configs:
            crash_client = MCPClient(MCPServerConfig(**asdict(configs[0])))
            try:
                crash_client.connect()
                available = {t.name for t in crash_client.list_tools()}
                # Any exposed tool works; "echo" first for readability.
                probe = next(
                    (
                        (spec_tool, arguments)
                        for label, spec_tool, arguments in CALL_SPECS
                        if label != "get-sum-error" and spec_tool in available
                    ),
                    None,
                )
                if probe is not None:
                    try:
                        before = crash_client.call_tool(*probe)
                        crash["call_before_kill"] = {"ok": True, "tool": probe[0], "value": before}
                    except Exception as exc:  # noqa: BLE001
                        crash["call_before_kill"] = error_record(exc)
                crash["probe_tool"] = probe[0] if probe else None
                crash["kill"] = probe_crash(entry.get("crash_marker") or CRASH_MARKER)
                crash["killed_count"] = len(crash["kill"]["killed"])
                if probe is not None:
                    try:
                        after = crash_client.call_tool(*probe)
                        crash["call_after_kill"] = {"ok": True, "tool": probe[0], "value": after}
                    except Exception as exc:  # noqa: BLE001
                        crash["call_after_kill"] = error_record(exc)
                crash["connected_after_kill"] = crash_client.is_connected
            except Exception as exc:  # noqa: BLE001
                crash["error"] = error_record(exc)
            finally:
                try:
                    crash_client.disconnect()
                except Exception:  # noqa: BLE001
                    pass
        result["crash"] = crash

        results.append(result)
        print(f"[python] {name}: tools={len(surface.get('server_tool_names', []))} ok", file=sys.stderr)

    payload = {
        "side": "python",
        "python_version": sys.version.split()[0],
        "hive_home": args.home,
        "servers": results,
    }
    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(payload, indent=2, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8")
    print(json.dumps({"wrote": str(out_path), "servers": len(results)}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
