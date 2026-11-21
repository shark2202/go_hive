#!/usr/bin/env python3
"""Cross-check MCP server: a dependency-free stdio JSON-RPC fixture.

Both Hive runtimes (Python `framework.loader.mcp_client` and Go
`internal/tools/mcp_client`) spawn this file with the same server entry, then
the MCP cross-check probe dumps the tool definitions each runtime registers for
the model and diffs them.

Protocol surface implemented (newline-delimited JSON, MCP 2024-11-05..2025-06-18):
  - initialize / notifications/initialized
  - ping
  - tools/list
  - tools/call

Tools (deliberate fixture design):
  - `echo_text`  — unique name; schema exercises string/integer/enum/default.
  - `read_file`  — same name as the built-in file tool, to observe how each
                   runtime handles a name collision (namespace vs first-wins).
"""

from __future__ import annotations

import json
import os
import sys

SERVER_NAME = "crosscheck-echo"
SERVER_VERSION = "1.0.0"

TOOLS = [
    {
        "name": "echo_text",
        "description": "Echo the supplied text back, optionally repeated and upper-cased.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "text": {
                    "type": "string",
                    "description": "Text to echo.",
                },
                "count": {
                    "type": "integer",
                    "description": "How many times to repeat the text.",
                    "minimum": 1,
                    "maximum": 5,
                    "default": 1,
                },
                "mode": {
                    "type": "string",
                    "description": "Output casing.",
                    "enum": ["plain", "upper"],
                    "default": "plain",
                },
            },
            "required": ["text"],
        },
        "annotations": {"readOnlyHint": True},
    },
    {
        "name": "read_file",
        "description": "Fixture tool that collides with the built-in read_file.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "path": {
                    "type": "string",
                    "description": "Path to read (fixture returns a stub).",
                },
                "limit": {
                    "type": "integer",
                    "description": "Maximum characters to return.",
                    "minimum": 0,
                },
            },
            "required": ["path"],
        },
        "annotations": {"readOnlyHint": True},
    },
]


def _log(message: str) -> None:
    # Never write diagnostics to stdout: stdout is the JSON-RPC channel.
    if os.environ.get("MCP_CROSSCHECK_LOG"):
        print(message, file=sys.stderr, flush=True)


def _result(request_id, result):
    return {"jsonrpc": "2.0", "id": request_id, "result": result}


def _error(request_id, code, message):
    return {"jsonrpc": "2.0", "id": request_id, "error": {"code": code, "message": message}}


def _call_tool(name: str, arguments: dict):
    if name == "echo_text":
        text = arguments.get("text")
        if not isinstance(text, str):
            return {"content": [{"type": "text", "text": "text is required"}], "isError": True}
        count = arguments.get("count", 1)
        try:
            count = int(count)
        except (TypeError, ValueError):
            count = 1
        count = max(1, min(5, count))
        mode = arguments.get("mode", "plain")
        output = "\n".join([text] * count)
        if mode == "upper":
            output = output.upper()
        return {"content": [{"type": "text", "text": output}], "isError": False}

    if name == "read_file":
        path = arguments.get("path")
        if not isinstance(path, str) or not path:
            return {"content": [{"type": "text", "text": "path is required"}], "isError": True}
        limit = arguments.get("limit")
        try:
            limit = int(limit) if limit is not None else 64
        except (TypeError, ValueError):
            limit = 64
        stub = f"fixture-read_file:{path}"
        return {"content": [{"type": "text", "text": stub[: max(0, limit)]}], "isError": False}

    return _error(None, -32602, f"unknown tool: {name}")


def handle(message: dict):
    method = message.get("method")
    request_id = message.get("id")
    params = message.get("params") or {}

    # A message without an id is a notification: never answer it.
    if request_id is None and method is not None:
        return None

    if method == "initialize":
        requested = params.get("protocolVersion") or "2025-06-18"
        _log(f"initialize protocolVersion={requested}")
        return _result(
            request_id,
            {
                "protocolVersion": requested,
                "capabilities": {"tools": {"listChanged": False}},
                "serverInfo": {"name": SERVER_NAME, "version": SERVER_VERSION},
            },
        )
    if method == "ping":
        return _result(request_id, {})
    if method == "tools/list":
        return _result(request_id, {"tools": TOOLS})
    if method == "tools/call":
        name = params.get("name")
        arguments = params.get("arguments") or {}
        _log(f"tools/call name={name}")
        if not isinstance(name, str) or not name:
            return _error(request_id, -32602, "tool name is required")
        if not isinstance(arguments, dict):
            return _error(request_id, -32602, "arguments must be an object")
        result = _call_tool(name, arguments)
        if "error" in result:
            result["id"] = request_id
            return result
        return _result(request_id, result)
    if method in ("resources/list", "prompts/list"):
        return _result(request_id, {"resources" if method.startswith("resources") else "prompts": []})

    return _error(request_id, -32601, f"method not found: {method}")


def main() -> int:
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            message = json.loads(line)
        except json.JSONDecodeError:
            _log(f"bad json: {line[:200]}")
            continue
        response = handle(message)
        if response is not None:
            sys.stdout.write(json.dumps(response, separators=(",", ":")) + "\n")
            sys.stdout.flush()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
