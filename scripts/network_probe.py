#!/usr/bin/env python3
"""Direct network-tool cross-check: Python reference vs the captured Go result.

`http_headers_scan` is registered by the Python integrations MCP server but is
not part of the queen harness surface the capture session runs, so the
model-visible capture records "Unknown tool" on that side. This probe closes
the gap: it starts the same local fixture server the capture uses, invokes the
Python tool function directly (a stub FastMCP captures the registered
function), and compares the payload with the Go session result committed in
`raw/go-results.json`.

The comparison normalises the per-run fixture port (`http://127.0.0.1:<port>`
→ `<LOCAL_URL>`) and compares the JSON trees. Output goes to
`raw/network-direct.json`; exit code 1 when the two payloads differ.

Usage (from go_hive/):
    uv run --project /Users/mac/hive python scripts/network_probe.py \
        --python-root /Users/mac/hive
"""

from __future__ import annotations

import argparse
import asyncio
import json
import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from capture_tool_output import start_fixture_server  # noqa: E402

LOCAL_URL_RE = re.compile(r"http://127\.0\.0\.1:\d+")

URLS = {
    "http_headers_ok": "/headers-all",
    "http_headers_bare": "/headers-missing",
}


class StubMCP:
    """Collects the tool functions a register_tools(mcp) call defines."""

    def __init__(self) -> None:
        self.tools: dict[str, object] = {}

    def tool(self, *args, **kwargs):
        def decorator(function):
            self.tools[function.__name__] = function
            return function

        # `@mcp.tool()` is the shape both modules use.
        return decorator if not args else decorator(args[0])


def load_http_headers_tool(python_root: Path):
    src = python_root / "tools" / "src"
    if not src.is_dir():
        raise SystemExit(f"missing tools/src under {python_root}")
    sys.path.insert(0, str(src))
    from aden_tools.tools.http_headers_scanner import http_headers_scanner

    stub = StubMCP()
    http_headers_scanner.register_tools(stub)
    function = stub.tools.get("http_headers_scan")
    if function is None:
        raise SystemExit("http_headers_scan was not registered")
    return function


def normalize(value):
    if isinstance(value, str):
        return LOCAL_URL_RE.sub("<LOCAL_URL>", value)
    if isinstance(value, list):
        return [normalize(item) for item in value]
    if isinstance(value, dict):
        return {key: normalize(item) for key, item in value.items()}
    return value


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--python-root", default="/Users/mac/hive")
    parser.add_argument("--raw", default=str(HERE.parent / "docs/e2e-assets/tool-output/raw"))
    parser.add_argument("--out", default=str(HERE.parent / "docs/e2e-assets/tool-output/raw/network-direct.json"))
    args = parser.parse_args()

    python_root = Path(args.python_root)
    raw_dir = Path(args.raw)
    go_results = json.loads((raw_dir / "go-results.json").read_text(encoding="utf-8"))
    tool = load_http_headers_tool(python_root)

    work_dir = Path("/tmp/network-probe-ws")
    work_dir.mkdir(parents=True, exist_ok=True)
    (work_dir / "fixture-page.html").write_text("<html><body><h1>fixture</h1></body></html>\n", encoding="utf-8")
    server, base = start_fixture_server(work_dir)

    evidence: dict = {"fixture": "<LOCAL_URL>", "scenarios": {}}
    failed = False
    try:
        for scenario_id, path in URLS.items():
            python_payload = asyncio.run(tool(url=base + path))
            go_entry = go_results.get(scenario_id) or {}
            go_content = go_entry.get("content") or ""
            try:
                go_payload = json.loads(go_content)
            except json.JSONDecodeError:
                go_payload = {"_unparsed": go_content}
            python_normal = normalize(python_payload)
            go_normal = normalize(go_payload)
            equal = python_normal == go_normal
            failed = failed or not equal
            evidence["scenarios"][scenario_id] = {
                "url_path": path,
                "python": python_payload,
                "go": go_payload,
                "equal": equal,
            }
    finally:
        server.shutdown()
        server.server_close()

    evidence["all_equal"] = not failed
    Path(args.out).write_text(json.dumps(evidence, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
    print(json.dumps({"all_equal": evidence["all_equal"],
                      "scenarios": {k: v["equal"] for k, v in evidence["scenarios"].items()}}, indent=1))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
