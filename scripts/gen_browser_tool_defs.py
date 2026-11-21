#!/usr/bin/env python3
"""Freeze the Python-side browser tool surface for the Go tool registry.

The Python runtime exposes the browser bridge as an MCP server: ``gcu``'s
``register_tools()`` registers 22 active tools on a FastMCP instance, and the
model sees each tool's ``name``, ``description`` and ``inputSchema`` verbatim.
The Go runtime has no equivalent definition layer today, so its browser surface
is only observable through ``hive-browser help``.

This script reads the *live* FastMCP registration (not the source text) and
writes the embedded golden the Go registry installs:

    go_hive/go-hive/internal/tools/integrations/browser_tool_defs.json

Nothing is rewritten: name/description/parameters are copied from the FastMCP
tool manager exactly as the Python runtime would publish them. Each entry also
carries a sha256 over its canonical (sorted-key, compact) JSON so a Go test can
recompute the digest from its own served definitions and report 22/22 matches.

Usage (from the repository root, so the `tools` workspace is importable):

    uv run python go_hive/scripts/gen_browser_tool_defs.py
    uv run python go_hive/scripts/gen_browser_tool_defs.py --check

``--check`` exits 1 when the committed artifact differs from a fresh
extraction (the drift gate for this lane).
"""
from __future__ import annotations

import hashlib
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
TOOLS_SRC = REPO_ROOT / "tools"
OUT = REPO_ROOT / "go_hive/go-hive/internal/tools/integrations/browser_tool_defs.json"

# The seven modules register_tools() imports, in registration order. The file
# list is recorded as provenance (and hashed) so a change in the Python source
# is visible in the artifact even before the tool list moves.
SOURCE_MODULES = (
    "tools/src/gcu/browser/tools/lifecycle.py",
    "tools/src/gcu/browser/tools/tabs.py",
    "tools/src/gcu/browser/tools/navigation.py",
    "tools/src/gcu/browser/tools/inspection.py",
    "tools/src/gcu/browser/tools/interact.py",
    "tools/src/gcu/browser/tools/advanced.py",
    "tools/src/gcu/browser/tools/script.py",
)


def canonical_sha(tool: dict) -> str:
    payload = {
        "name": tool["name"],
        "description": tool["description"],
        "input_schema": _normalize_numbers(tool["input_schema"]),
    }
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(encoded.encode("utf-8")).hexdigest()


def _normalize_numbers(value):
    """Canonicalise integral floats to ints.

    pydantic emits ``60.0`` where a Go json.Unmarshal records float64(60) and
    re-marshals ``60``; normalising here keeps the digest comparable from both
    sides without rewriting the verbatim schema in the artifact.
    """
    if isinstance(value, dict):
        return {key: _normalize_numbers(item) for key, item in value.items()}
    if isinstance(value, list):
        return [_normalize_numbers(item) for item in value]
    if isinstance(value, float) and value.is_integer():
        return int(value)
    return value


def extract() -> list[dict]:
    sys.path.insert(0, str(TOOLS_SRC / "src"))
    from fastmcp import FastMCP  # noqa: PLC0415

    from gcu.browser import register_tools  # noqa: PLC0415

    server = FastMCP("gcu-tools")
    register_tools(server)
    tools = []
    for name, tool in server._tool_manager._tools.items():  # noqa: SLF001 - FastMCP has no public list API
        tools.append(
            {
                "name": name,
                "description": tool.description or "",
                "input_schema": tool.parameters,
            }
        )
    tools.sort(key=lambda entry: entry["name"].encode("utf-8"))
    for entry in tools:
        entry["sha256"] = canonical_sha(entry)
    return tools


def build() -> dict:
    tools = extract()
    source_hashes = {}
    for relative in SOURCE_MODULES:
        path = REPO_ROOT / relative
        source_hashes[relative] = hashlib.sha256(path.read_bytes()).hexdigest()
    return {
        "schema_version": "1.0",
        "kind": "hive-browser-tool-definitions",
        "provenance": {
            "purpose": (
                "Model-visible browser tool definitions served by the Go tool registry; "
                "field-level copy of the Python gcu FastMCP registration"
            ),
            "source_package": "tools/src/gcu/browser/tools",
            "source_modules": list(SOURCE_MODULES),
            "source_sha256": source_hashes,
            "extraction": "FastMCP ToolManager after gcu.browser.register_tools(server)",
            "generator": "go_hive/scripts/gen_browser_tool_defs.py",
            "canonical_order": "bytewise by name",
            "sha_rule": "sha256 of {name, description, input_schema} as sorted-key compact JSON (UTF-8)",
            "counts": {"active": len(tools)},
        },
        "tools": tools,
    }


def main(argv: list[str]) -> int:
    payload = build()
    text = json.dumps(payload, indent=1, ensure_ascii=False) + "\n"
    if "--check" in argv:
        current = OUT.read_text(encoding="utf-8") if OUT.exists() else ""
        if current != text:
            print(f"{OUT} is out of date; re-run {pathlib.Path(__file__).name}", file=sys.stderr)
            return 1
        print(f"{OUT} is up to date ({payload['provenance']['counts']})")
        return 0
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(text, encoding="utf-8")
    print(f"wrote {OUT} ({payload['provenance']['counts']})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
