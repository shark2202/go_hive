#!/usr/bin/env python3
"""Generate the model-visible tool-contract parity manifest for the Go port.

The manifest is the frozen Python side of the P1 contract audit: for the 36
tools both runtimes expose under the same name it records the *model-visible*
description and input schema, and whether this lane applies them.

Description rule (verified against the Python sources, not just the registry):
  - harness tools (core/framework/tools/harness_tools.py) : inspect.cleandoc
  - aden_tools tools (FastMCP default/all registry)       : raw docstring
  - the four file tools (aden_tools/file_ops.py)          : explicit *_DOC
    constants passed to @register_tool(description=...).

Model-visible schema = upstream input_schema minus CONTEXT_PARAMS
(agent_id, colony_id, data_dir, principal, profile, profile_display_name,
session_cwd) and minus pydantic ``title`` keys.

Usage (from a checkout that also contains core/ and tools/):
    cd tools && uv run python ../go_hive/scripts/gen_tool_contract_parity.py

Writes go_hive/go-hive/internal/cli/tool_contract_parity.json.
"""
from __future__ import annotations

import asyncio
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
OUT = REPO_ROOT / "go_hive/go-hive/internal/cli/tool_contract_parity.json"

CONTEXT_PARAMS = {
    "agent_id", "colony_id", "data_dir", "principal", "profile",
    "profile_display_name", "session_cwd",
}

# The 42 same-name tools of the contract audit (Python 857/270 vs Go 137).
# The seven aden_tools security tools are registered unconditionally upstream
# and (since the scanners-parity lane) unconditionally in Go as well, so all
# seven are part of the same-name surface.
CORE_TOOLS = [
    "attach_file", "chart_render", "csv_append", "csv_info", "csv_read",
    "csv_write", "dns_security_scan", "edit_file", "exa_search", "excel_append",
    "excel_info", "excel_read", "excel_search", "excel_sheet_list", "excel_write",
    "get_account_info", "get_current_time", "http_headers_scan", "image_generate",
    "pdf_read", "port_scan", "read_file", "risk_score", "search_files",
    "search_messages", "search_wikipedia", "ssl_tls_scan", "subdomain_enumerate",
    "tech_stack_detect", "terminal_exec", "terminal_glob", "terminal_job_logs",
    "terminal_job_manage", "terminal_job_start", "terminal_output_get",
    "terminal_pty_close", "terminal_pty_open", "terminal_pty_run",
    "terminal_rg", "web_scrape", "web_search", "write_file",
]

TERMINAL_TOOLS = {
    "terminal_exec", "terminal_glob", "terminal_job_logs",
    "terminal_job_manage", "terminal_job_start", "terminal_output_get",
    "terminal_pty_close", "terminal_pty_open", "terminal_pty_run",
    "terminal_rg",
}

HARNESS_GROUPS = ("files-tools", "terminal-tools", "chart-tools", "memory-tools", "hive_tools")

# Tools whose frozen contract reference is the Python REQUEST layer (the freeze
# capture recorded them under layers including llm_request). The request path
# (`framework/llm/litellm.py::_tool_to_openai_format`) always writes
# `required`, even as an empty list, so their model-visible schema publishes
# `required: []` while the registry dump below has no such key. The three names
# are review-verified against go_hive/contracts/tool-definitions.json; a tool
# whose only reference is the Tool Library layer (edit_file, terminal_pty_open)
# stays without the key, because that layer omits it.
REQUEST_REQUIRED_MATERIALIZED = {"get_account_info", "get_current_time", "pdf_read"}


def strip_titles(node):
    if isinstance(node, list):
        return [strip_titles(item) for item in node]
    if not isinstance(node, dict):
        return node
    out = {}
    for key, value in node.items():
        if key == "title":
            continue
        if key == "properties" and isinstance(value, dict):
            out[key] = {k: strip_titles(v) for k, v in value.items()}
        else:
            out[key] = strip_titles(value)
    return out


def visible(schema, ensure_required: bool = False):
    schema = json.loads(json.dumps(schema or {}))
    props = schema.get("properties") or {}
    schema["properties"] = {k: v for k, v in props.items() if k not in CONTEXT_PARAMS}
    if "required" in schema:
        schema["required"] = [r for r in (schema.get("required") or []) if r not in CONTEXT_PARAMS]
    elif ensure_required:
        # The request layer materializes the key (see below); the registry dump
        # does not. Restore it for tools whose frozen reference is the request.
        schema["required"] = []
    return strip_titles(schema)


def dump_harness():
    sys.path.insert(0, str(REPO_ROOT / "core"))
    from framework.tools.harness_tools import build_harness_group  # noqa: PLC0415

    harness = {}
    group_of = {}
    counts = {}
    for gid in HARNESS_GROUPS:
        rows = build_harness_group(gid, scope_env=lambda: {})
        counts[gid] = len(rows)
        for tool in rows:
            harness[tool.name] = {
                "description": tool.description or "",
                "inputSchema": tool.input_schema,
            }
            group_of[tool.name] = gid
    return harness, group_of, counts


def dump_aden_tools():
    sys.path.insert(0, str(REPO_ROOT / "tools/src"))
    from fastmcp import FastMCP  # noqa: PLC0415
    from aden_tools.tools import register_all_tools  # noqa: PLC0415

    async def collect(include_unverified):
        mcp = FastMCP("parity-manifest")
        register_all_tools(mcp, include_unverified=include_unverified)
        return {
            t.name: {"description": t.description or "", "inputSchema": t.parameters}
            for t in (await mcp.get_tools()).values()
        }

    return asyncio.run(collect(False)), asyncio.run(collect(True))


def main() -> None:
    harness, group_of, counts = dump_harness()
    py_default, py_all = dump_aden_tools()

    tools = {}
    for name in CORE_TOOLS:
        if name in harness:
            py, source = harness[name], f"harness:{group_of[name]}"
        elif name in py_default:
            py, source = py_default[name], "aden_tools:default"
        elif name in py_all:
            py, source = py_all[name], "aden_tools:all"
        else:
            raise SystemExit(f"{name}: not found in any Python registry")
        if name in TERMINAL_TOOLS:
            status, reason = "dependency", (
                "terminal-schema-p0 lane owns internal/tools/builtin/terminal_*.go; "
                "description/required/schema not modified by this lane")
        elif name == "exa_search":
            status, reason = "partial", (
                "Go registers Exa as a 7-action module tool; replacing the module "
                "description/schema with the flat exa_search def would orphan the "
                "other six actions. Action params carry the enum/items fix instead.")
        else:
            status, reason = "aligned", ""
        tools[name] = {
            "status": status,
            "source": source,
            "reason": reason,
            "apply_description": status == "aligned",
            "apply_parameters": status == "aligned",
            "description": py["description"],
            "parameters": visible(py["inputSchema"], ensure_required=name in REQUEST_REQUIRED_MATERIALIZED),
        }

    manifest = {
        "provenance": {
            "purpose": "Python ⇄ Go model-visible tool contract for the 42 same-name core tools (P1 lane)",
            "python_default_tools": len(py_default),
            "python_all_tools": len(py_all),
            "harness_groups": counts,
            "context_params_stripped": sorted(CONTEXT_PARAMS),
            "excluded_schema_keys": ["title"],
            "description_rule": "harness tools use inspect.cleandoc(docstring); aden_tools tools use the raw FastMCP docstring; file_ops tools use their explicit *_DOC constants",
            "generator": "go_hive/scripts/gen_tool_contract_parity.py",
        },
        "tools": tools,
    }
    OUT.write_text(json.dumps(manifest, ensure_ascii=False, indent=1) + "\n")
    aligned = [n for n, t in tools.items() if t["status"] == "aligned"]
    print(f"wrote {OUT}: {len(tools)} tools · aligned {len(aligned)} · "
          f"partial {[n for n, t in tools.items() if t['status'] == 'partial']} · "
          f"dependency {len([n for n, t in tools.items() if t['status'] == 'dependency'])}")


if __name__ == "__main__":
    main()
