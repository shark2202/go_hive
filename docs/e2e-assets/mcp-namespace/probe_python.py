"""Python reference-side probe for MCP namespace folding / long names / content blocks.

Runs the real framework code paths in-process with a fake MCP client; no server
is started, no network is touched. Prints JSON evidence to stdout.

Usage (from core/): uv run python <this file>
"""

from __future__ import annotations

import json
import sys
from types import SimpleNamespace

from framework.llm.antigravity import _clean_tool_name
from framework.llm.provider import ToolResult, ToolUse
from framework.loader import mcp_client as mcp_client_module
from framework.loader.mcp_client import MCPTool, _split_mcp_content
from framework.loader.tool_registry import ToolRegistry


class FakeMCPClient:
    """Stands in for MCPClient: list_tools() from a fixture, call_tool() from a queue."""

    fixtures: dict[str, dict] = {}
    calls: list[tuple[str, str]] = []

    def __init__(self, config):
        self.config = config
        fx = self.fixtures[config.name]
        self._tool_names = fx["tools"]
        self._results = list(fx.get("results", []))

    def connect(self):
        return None

    def disconnect(self):
        return None

    def list_tools(self):
        return [
            MCPTool(
                name=n,
                description=n,
                input_schema={"type": "object", "properties": {}, "required": []},
                server_name=self.config.name,
            )
            for n in self._tool_names
        ]

    def call_tool(self, name, arguments):
        FakeMCPClient.calls.append((self.config.name, name))
        if self._results:
            return self._results.pop(0)
        return f"{self.config.name}:{name}"


def register(registry: ToolRegistry, server: str) -> dict:
    count = registry.register_mcp_server(
        {"name": server, "transport": "stdio", "command": "fake"},
        use_connection_manager=False,
        preserve_existing_tools=True,
    )
    return {
        "server": server,
        "registered_count": count,
        "server_tools": sorted(registry.get_server_tool_names(server)),
        "registry_names": sorted(registry.get_registered_names()),
    }


def executor_content(registry: ToolRegistry, name: str) -> dict:
    result = registry.get_executor()(ToolUse(id="call_1", name=name, input={}))
    assert isinstance(result, ToolResult)
    return {
        "content": result.content,
        "is_error": result.is_error,
        "image_content": result.image_content,
    }


def main() -> int:
    out: dict = {}

    # ---------------------------------------------------------------- group 1
    # a-b vs a_b: same tool name on two servers, and two names in one server.
    FakeMCPClient.fixtures = {
        "a-b": {"tools": ["echo_text", "x-y", "x_y", "t" * 70]},
        "a_b": {"tools": ["echo_text", "only_b"]},
    }
    mcp_client_module.MCPClient = FakeMCPClient  # type: ignore[assignment]
    reg = ToolRegistry()
    out["collision_cross_server"] = [register(reg, "a-b"), register(reg, "a_b")]
    # In Python the registry key is the bare tool name; fold never happens.
    out["collision_tool_names_in_one_server"] = {
        "x-y_registered": reg.has_tool("x-y"),
        "x_y_registered": reg.has_tool("x_y"),
        "both_servers_have_echo_text": [
            sorted(reg.get_server_tool_names("a-b")),
            sorted(reg.get_server_tool_names("a_b")),
        ],
    }
    FakeMCPClient.calls.clear()
    out["collision_call_x_y"] = executor_content(reg, "x_y")

    # preserve_existing_tools=False: the later server overwrites the earlier
    # executor under the same bare name (both Python and Go then call the
    # second server's tool).
    reg_ow = ToolRegistry()
    reg_ow.register_mcp_server(
        {"name": "a-b", "transport": "stdio", "command": "fake"},
        use_connection_manager=False,
        preserve_existing_tools=False,
    )
    reg_ow.register_mcp_server(
        {"name": "a_b", "transport": "stdio", "command": "fake"},
        use_connection_manager=False,
        preserve_existing_tools=False,
    )
    FakeMCPClient.calls.clear()
    out["collision_call_echo_text_preserve_false"] = executor_content(reg_ow, "echo_text")
    out["collision_calls_seen_preserve_false"] = list(FakeMCPClient.calls)

    # ---------------------------------------------------------------- group 2
    long_name = "t" * 70
    out["long_name"] = {
        "registered_key": next(n for n in reg.get_registered_names() if n.startswith("t" * 10)),
        "registered_len": len(next(n for n in reg.get_registered_names() if n.startswith("t" * 10))),
        "clean_tool_name_70": _clean_tool_name(long_name),
        "clean_tool_name_70_len": len(_clean_tool_name(long_name)),
        "clean_tool_name_space_slash": _clean_tool_name("my tool/name"),
        "clean_tool_name_lead_digit": _clean_tool_name("9lives"),
        "clean_tool_name_hyphen": _clean_tool_name("a-b"),
        "clean_tool_name_128": _clean_tool_name("u" * 128),
    }

    # ---------------------------------------------------------------- group 3
    # _split_mcp_content (stdio/SSE aggregation; pure function, real module).
    text_item = lambda t: SimpleNamespace(text=t)  # noqa: E731
    image_item = lambda d, m, meta=None: SimpleNamespace(data=d, mimeType=m, _meta=meta)  # noqa: E731
    data_only = SimpleNamespace(data="raw-blob")
    data_only_bytes = SimpleNamespace(data=b"bytes-blob")
    resource_link = SimpleNamespace(uri="https://example.test/x", name="x", mimeType="text/plain")
    unknown = SimpleNamespace(foo="bar")

    def split(items):
        text, images = _split_mcp_content(items)
        return {"text": text, "images": images}

    out["split"] = {
        "two_texts": split([text_item("hello"), text_item("world")]),
        "text_image": split([text_item("alt"), image_item("AAA", "image/png")]),
        "text_pdf_meta": split([image_item("JVBE", "application/pdf", {"filename": "q3.pdf"})]),
        "pdf_upper_mime": split([image_item("JVBE", "Application/PDF")]),
        "empty_mime": split([image_item("AAA", "")]),
        "null_mime": split([SimpleNamespace(data="AAA", mimeType=None)]),
        "data_only": split([data_only]),
        "data_only_bytes": split([data_only_bytes]),
        "resource_link_only": split([resource_link]),
        "unknown_object": split([unknown]),
        "mixed_all": split(
            [
                text_item("a"),
                image_item("img1", "image/jpeg"),
                resource_link,
                text_item("b"),
                image_item("pdf1", "application/pdf"),
            ]
        ),
    }

    # Model-visible ToolResult for each transport's result shape (registry
    # executor + _wrap_result are the real code path).
    FakeMCPClient.fixtures = {
        "stdio-like": {"tools": ["echo"], "results": ["a\nb"]},
        "stdio-image": {
            "tools": ["shot"],
            "results": [
                {
                    "_text": "caption",
                    "_images": [
                        {
                            "type": "image_url",
                            "image_url": {"url": "data:image/png;base64,AAA"},
                        }
                    ],
                }
            ],
        },
        "http-like": {
            "tools": ["echo"],
            "results": [
                [
                    {"type": "text", "text": "a"},
                    {"type": "text", "text": "b"},
                ]
            ],
        },
        "http-image": {
            "tools": ["shot"],
            "results": [
                [
                    {"type": "text", "text": "caption"},
                    {"type": "image", "data": "AAA", "mimeType": "image/png"},
                ]
            ],
        },
        "http-image-first": {
            "tools": ["shot"],
            "results": [
                [
                    {"type": "image", "data": "AAA", "mimeType": "image/png"},
                    {"type": "text", "text": "caption"},
                ]
            ],
        },
        "http-empty": {"tools": ["echo"], "results": [[]]},
        "http-none": {"tools": ["echo"], "results": [None]},
    }
    out["model_visible"] = {}
    for server in [
        "stdio-like",
        "stdio-image",
        "http-like",
        "http-image",
        "http-image-first",
        "http-empty",
        "http-none",
    ]:
        r = ToolRegistry()
        register(r, server)
        tool_name = FakeMCPClient.fixtures[server]["tools"][0]
        out["model_visible"][server] = executor_content(r, tool_name)

    json.dump(out, sys.stdout, indent=2, ensure_ascii=False)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
