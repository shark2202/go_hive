#!/usr/bin/env python3
"""Python-side MCP cross-check probe.

Runs inside the core virtualenv:

    cd core && uv run python ../go_hive/scripts/mcp_crosscheck_python.py \
        --servers <servers.json> --out <python-side.json> [--home <dir>]

It drives the exact loader a queen session uses
(``ToolRegistry.load_mcp_config`` over an ``mcp_servers.json`` file), with the
built-in ``files-tools`` harness group registered first so the fixture's
``read_file`` collides with the real built-in file tool. The output JSON is the
Python half of the evidence consumed by ``go_hive/scripts/mcp_crosscheck.py``.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
from dataclasses import asdict
from pathlib import Path


def canonical(value) -> str:
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"))


def sha256_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def tool_record(tool) -> dict:
    parameters = tool.parameters
    return {
        "name": tool.name,
        "description": tool.description,
        "parameters": parameters,
        "parameters_sha256": sha256_text(canonical(parameters)),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--servers", required=True, help="shared mcp_servers.json fixture")
    parser.add_argument("--out", required=True, help="where to write the Python-side JSON")
    parser.add_argument("--home", default="", help="HIVE_HOME to pin before framework import")
    parser.add_argument("--server-name", default="", help="fixture server name (default: config entry)")
    args = parser.parse_args()

    if args.home:
        os.environ["HIVE_HOME"] = args.home

    from framework.llm.provider import ToolUse
    from framework.loader.mcp_client import MCPClient
    from framework.loader.tool_registry import ToolRegistry

    servers_path = Path(args.servers).resolve()
    config = json.loads(servers_path.read_text(encoding="utf-8"))
    entry = config["servers"][0]
    server_name = args.server_name or entry["name"]

    registry = ToolRegistry()
    harness_results = registry.load_registry_servers(
        [{"name": "files-tools", "transport": "harness"}],
        log_summary=False,
        preserve_existing_tools=True,
    )
    registry.load_mcp_config(servers_path)

    all_tools = registry.get_tools()
    mcp_names = set(registry._mcp_tool_names)
    registered_names = sorted(all_tools)
    target_names = sorted(n for n in all_tools if n in mcp_names or n == "read_file")

    # Name mapping: fixture tool name -> the name actually registered for the
    # model, or why the fixture's tool never made it into the registry.
    advertised = {t["name"] for t in entry.get("tools", [])} or {"echo_text", "read_file"}
    fixture_registered = set(registry.get_server_tool_names(server_name))
    name_mapping = {}
    for remote in sorted(advertised):
        if remote in fixture_registered:
            name_mapping[remote] = {"registered_name": remote, "owned_by": server_name}
        elif remote in all_tools:
            name_mapping[remote] = {
                "registered_name": None,
                "shadowed_by": registry._find_mcp_origin_server_for_tool(remote),
            }
        else:
            name_mapping[remote] = {"registered_name": None, "shadowed_by": None}

    calls: dict[str, dict] = {}
    executor = registry.get_executor()
    if "echo_text" in all_tools:
        result = executor(
            ToolUse(
                id="crosscheck-1",
                name="echo_text",
                input={"text": "Hello MCP", "count": 2, "mode": "upper"},
            )
        )
        calls["echo_text"] = {"content": result.content, "is_error": result.is_error}

    collision = {
        "read_file_in_tools": "read_file" in all_tools,
        "read_file_origin_server": registry._find_mcp_origin_server_for_tool("read_file"),
        "fixture_server_registered_tools": sorted(registry.get_server_tool_names(server_name)),
        "fixture_read_file_has_executor": "read_file" in registry._mcp_tool_clients,
    }

    # Enable/disable through the same MCPRegistry the HTTP routes and the queen
    # session use, plus the "a new session sees the new server" reload path.
    from framework.loader.mcp_registry import MCPRegistry

    lifecycle = {
        "registry_enabled_resolves": None,
        "registry_disabled_resolves": None,
        "registry_reenabled_resolves": None,
        "registry_has_reload_method": hasattr(MCPRegistry, "reload"),
        "fresh_session_fixture_tools": None,
    }
    if args.home:
        mcp_registry = MCPRegistry(base_path=Path(args.home) / "mcp_registry")
        mcp_registry.initialize()
        if mcp_registry.get_server(server_name) is None:
            mcp_registry.add_local(
                name=server_name,
                transport=entry.get("transport", "stdio"),
                command=entry.get("command"),
                args=entry.get("args") or [],
                env=entry.get("env") or {},
                cwd=entry.get("cwd"),
                description="crosscheck fixture",
            )
        lifecycle["registry_enabled_resolves"] = [
            c.name for c in mcp_registry.resolve_for_agent(include=[server_name])
        ]
        mcp_registry.disable(server_name)
        lifecycle["registry_disabled_resolves"] = [
            c.name for c in mcp_registry.resolve_for_agent(include=[server_name])
        ]
        mcp_registry.enable(server_name)
        lifecycle["registry_reenabled_resolves"] = [
            c.name for c in mcp_registry.resolve_for_agent(include=[server_name])
        ]

        # A "reload" in this runtime is the next session: load the resolved
        # config into a fresh ToolRegistry exactly like McpRegistryStage does.
        fresh = ToolRegistry()
        fresh_configs = mcp_registry.resolve_for_agent(include=[server_name])
        if fresh_configs:
            fresh.load_registry_servers([asdict(c) for c in fresh_configs], log_summary=False)
            lifecycle["fresh_session_fixture_tools"] = sorted(fresh.get_server_tool_names(server_name))

    config_json_path = Path(args.home) / "configuration.json" if args.home else None
    config_json_mcp = None
    if config_json_path is not None and config_json_path.exists():
        config_json_mcp = json.loads(config_json_path.read_text(encoding="utf-8")).get("mcp")

    payload = {
        "side": "python",
        "python_version": sys.version.split()[0],
        "framework_file": str(Path(__import__("framework").__file__).resolve()),
        "config_file": str(servers_path),
        "config_sha256": sha256_file(servers_path),
        "server_entry": entry,
        "server_entry_sha256": sha256_text(canonical(entry)),
        "server_name": server_name,
        "harness_group_results": harness_results,
        "registered_names": registered_names,
        "server_tool_names": sorted(registry.get_server_tool_names(server_name)),
        "name_mapping": name_mapping,
        "namespace_prefix": None,
        "tools": [tool_record(all_tools[n]) for n in target_names],
        "collision": collision,
        "lifecycle": lifecycle,
        "calls": calls,
        "timeout": {
            "env_HIVE_MCP_CALL_RESULT_TIMEOUT": os.environ.get("HIVE_MCP_CALL_RESULT_TIMEOUT"),
            "call_result_timeout_seconds": MCPClient._CALL_RESULT_TIMEOUT,
            "source": "framework.loader.mcp_client module constant (read once at import)",
        },
        "configuration_json_mcp_block": config_json_mcp,
        "hive_home": args.home or os.environ.get("HIVE_HOME", ""),
    }

    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"wrote": str(out_path), "registered": len(registered_names)}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
