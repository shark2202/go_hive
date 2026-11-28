#!/usr/bin/env python3
"""Real third-party MCP server cross-check: raw capture + Python↔Go diff.

    python3 go_hive/scripts/mcp_real_crosscheck.py \
        --servers <servers.json> --raw-dir <dir> \
        --python <python-side.json> --go <go-side.json> \
        --out <dir>

Three jobs:

1. **raw** — talks to every server entry itself (stdio subprocess, or the SSE /
   streamable-HTTP endpoint) with a hand-rolled JSON-RPC client, and stores the
   literal server replies. This is the ground truth the two Hive
   implementations are compared against: a tool's schema or a call result that
   differs from this file is not "passed through".
2. **diff** — compares the Python dump, the Go dump and the raw capture across
   the four dimensions (inventory/schema, calls+errors, lifecycle, transports).
3. **evidence** — writes ``diff.json`` (machine) and ``summary.md`` (human),
   and exits non-zero when an *unexpected* difference appears. Differences that
   are known, documented consequences of a design decision (Go's tool
   namespacing, Hive's ``/mcp/v1`` http bridge) are listed as exceptions with
   their evidence instead.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
import sys
import threading
import time
from pathlib import Path

CONTEXT_PARAMS = {
    "agent_id",
    "data_dir",
    "profile",
    "profile_display_name",
    "session_cwd",
    "colony_id",
    "principal",
}
UNKNOWN_TOOL = "no_such_tool_xyz"
# Volatile fields: the time server embeds "now", which cannot be equal across
# two runs minutes apart. Masked before comparing call text.
TIMESTAMP_RE = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:[+-]\d{2}:\d{2}|Z)?")
PROTOCOL_VERSION = "2025-11-25"


def canonical(value) -> str:
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"))


def sha256_value(value) -> str:
    return hashlib.sha256(canonical(value).encode("utf-8")).hexdigest()


def mask_volatile(text: str) -> str:
    return TIMESTAMP_RE.sub("<timestamp>", text or "")


def normalize_schema(raw_schema: dict) -> dict:
    """The normalization both Hive implementations apply to a server schema."""
    properties = {
        key: value
        for key, value in (raw_schema.get("properties") or {}).items()
        if key not in CONTEXT_PARAMS
    }
    required = [name for name in (raw_schema.get("required") or []) if name not in CONTEXT_PARAMS]
    return {"type": "object", "properties": properties, "required": required}


# ── raw stdio ────────────────────────────────────────────────────────────────


def raw_stdio(entry: dict, call_specs: list[tuple[str, str, dict]]) -> dict:
    command = [entry["command"], *(entry.get("args") or [])]
    proc = subprocess.Popen(
        command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        text=True, bufsize=1, cwd=entry.get("cwd"),
    )
    exchanges: list[dict] = []

    def rpc(method: str, params: dict | None = None, notify: bool = False):
        message: dict = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            message["params"] = params
        if not notify:
            message["id"] = len(exchanges) + 1
        proc.stdin.write(json.dumps(message) + "\n")
        proc.stdin.flush()
        if notify:
            return None
        deadline = time.time() + 60
        while time.time() < deadline:
            line = proc.stdout.readline()
            if not line:
                raise TimeoutError(f"server closed during {method}")
            try:
                response = json.loads(line.strip() or "{}")
            except json.JSONDecodeError:
                continue
            if response.get("id") == message["id"]:
                exchanges.append({"request": message, "response": response})
                return response
        raise TimeoutError(f"no response for {method}")

    try:
        init = rpc(
            "initialize",
            {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": "hive-real-crosscheck-raw", "version": "1.0"},
            },
        )
        rpc("notifications/initialized", {}, notify=True)
        listed = rpc("tools/list", {})
        tools = (listed or {}).get("result", {}).get("tools", [])
        names = {tool["name"] for tool in tools}
        calls = {}
        for label, tool, arguments in call_specs:
            if tool in names:
                calls[label] = rpc("tools/call", {"name": tool, "arguments": arguments})
        calls["unknown_tool"] = rpc("tools/call", {"name": UNKNOWN_TOOL, "arguments": {}})
        return {
            "transport": "stdio",
            "server_info": (init or {}).get("result", {}).get("serverInfo"),
            "protocol_version": (init or {}).get("result", {}).get("protocolVersion"),
            "tools_list": listed,
            "tools_list_sha256": sha256_value(listed),
            "calls": calls,
            "exchanges": exchanges,
        }
    finally:
        proc.stdin.close()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()


# ── raw SSE / streamable HTTP ────────────────────────────────────────────────

import httpx  # noqa: E402  (after the stdio path so the import cost is only paid here)


def _parse_sse_events(text: str) -> list[tuple[str, str]]:
    events: list[tuple[str, str]] = []
    event = ""
    data: list[str] = []
    for line in text.splitlines():
        if line == "":
            if data:
                events.append((event, "\n".join(data)))
            event, data = "", []
            continue
        if line.startswith(":"):
            continue
        name, _, value = line.partition(":")
        value = value[1:] if value.startswith(" ") else value
        if name == "event":
            event = value
        elif name == "data":
            data.append(value)
    if data:
        events.append((event, "\n".join(data)))
    return events


def raw_sse(entry: dict, call_specs: list[tuple[str, str, dict]]) -> dict:
    """Legacy HTTP+SSE: the GET stream carries every server message, and the
    ``endpoint`` event names the POST target (mcp-proxy answers 202)."""
    url = entry["url"]
    headers = dict(entry.get("headers") or {})
    result: dict = {"transport": "sse", "url": url}
    client = httpx.Client(timeout=30.0, headers=headers)
    received: list[dict] = []
    state: dict = {"endpoint": "", "status": None, "content_type": None, "error": None, "done": False}
    ready = threading.Event()

    def pump() -> None:
        try:
            with client.stream("GET", url, headers={"Accept": "text/event-stream"}) as stream:
                state["status"] = stream.status_code
                state["content_type"] = stream.headers.get("content-type")
                if stream.status_code != 200:
                    state["error"] = stream.read().decode("utf-8", "replace")[:400]
                    return
                buffer = ""
                for chunk in stream.iter_text():
                    # SSE over HTTP may use CRLF; normalize before framing.
                    buffer += chunk.replace(chr(13) + chr(10), chr(10))
                    while "\n\n" in buffer:
                        block, buffer = buffer.split("\n\n", 1)
                        for event, data in _parse_sse_events(block):
                            if event == "endpoint":
                                state["endpoint"] = data.strip()
                                ready.set()
                            elif data:
                                try:
                                    received.append(json.loads(data))
                                except json.JSONDecodeError:
                                    pass
        except Exception as exc:  # noqa: BLE001
            state["error"] = f"{type(exc).__name__}: {exc}"
        finally:
            state["done"] = True
            ready.set()

    thread = threading.Thread(target=pump, daemon=True)
    thread.start()
    if not ready.wait(timeout=30) or not state["endpoint"]:
        result["status"] = state["status"]
        result["error"] = state["error"] or "no endpoint event"
        return result
    result["status"] = state["status"]
    result["content_type"] = state["content_type"]
    result["endpoint_event"] = state["endpoint"]
    post_url = str(httpx.URL(url).join(state["endpoint"]))

    def post_sse(message: dict) -> dict:
        response = client.post(post_url, json=message, headers={"Accept": "application/json"})
        if response.status_code < 200 or response.status_code >= 300:
            return {"transport_error": {"status": response.status_code, "body": response.text[:300]}}
        if response.status_code != 202 and response.content:
            try:
                return response.json()
            except json.JSONDecodeError:
                pass
        deadline = time.time() + 30
        while time.time() < deadline:
            for item in received:
                if item.get("id") == message.get("id"):
                    return item
            time.sleep(0.05)
        return {"transport_error": {"status": response.status_code, "body": "no reply on the SSE stream"}}

    initial = post_sse(
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": "hive-real-crosscheck-raw", "version": "1.0"},
            },
        }
    )
    client.post(post_url, json={"jsonrpc": "2.0", "method": "notifications/initialized"})
    listed = post_sse({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
    tools = (listed or {}).get("result", {}).get("tools", [])
    names = {tool["name"] for tool in tools}
    calls = {}
    counter = 10
    for label, tool, arguments in call_specs:
        if tool in names:
            counter += 1
            calls[label] = post_sse(
                {"jsonrpc": "2.0", "id": counter, "method": "tools/call", "params": {"name": tool, "arguments": arguments}}
            )
    counter += 1
    calls["unknown_tool"] = post_sse(
        {"jsonrpc": "2.0", "id": counter, "method": "tools/call", "params": {"name": UNKNOWN_TOOL, "arguments": {}}}
    )
    result["server_info"] = ((initial or {}).get("result") or {}).get("serverInfo")
    result["protocol_version"] = ((initial or {}).get("result") or {}).get("protocolVersion")
    result["tools_list"] = listed
    result["tools_list_sha256"] = sha256_value(listed)
    result["calls"] = calls
    return result


def raw_streamable_http(entry: dict, call_specs: list[tuple[str, str, dict]]) -> dict:
    """Streamable HTTP: every request is a POST; the reply is JSON or an SSE body."""
    url = entry["url"]
    result: dict = {"transport": "http", "url": url}
    client = httpx.Client(timeout=30.0, headers=dict(entry.get("headers") or {}))
    session_id = ""

    def post_http(message: dict) -> tuple[dict | None, dict]:
        nonlocal session_id
        request_headers = {"Accept": "application/json, text/event-stream", "Content-Type": "application/json"}
        if session_id:
            request_headers["Mcp-Session-Id"] = session_id
        response = client.post(url, json=message, headers=request_headers)
        meta = {"status": response.status_code, "content_type": response.headers.get("content-type")}
        if response.headers.get("mcp-session-id"):
            session_id = response.headers["mcp-session-id"]
        if response.status_code < 200 or response.status_code >= 300:
            meta["body"] = response.text[:400]
            return None, meta
        if not response.content:
            # Notifications are answered 202 Accepted with no body.
            return None, meta
        if response.headers.get("content-type", "").startswith("text/event-stream"):
            for _, data in _parse_sse_events(response.text):
                try:
                    payload = json.loads(data)
                except json.JSONDecodeError:
                    continue
                if payload.get("id") == message.get("id"):
                    return payload, meta
            return None, meta
        return response.json(), meta

    initial, meta = post_http(
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": "hive-real-crosscheck-raw", "version": "1.0"},
            },
        }
    )
    result["initialize_meta"] = meta
    if initial is None:
        result["error"] = "initialize failed"
        return result
    result["session_id_received"] = bool(session_id)
    post_http({"jsonrpc": "2.0", "method": "notifications/initialized"})
    listed, meta = post_http({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
    result["tools_list_meta"] = meta
    tools = (listed or {}).get("result", {}).get("tools", [])
    names = {tool["name"] for tool in tools}
    calls = {}
    counter = 10
    for label, tool, arguments in call_specs:
        if tool in names:
            counter += 1
            calls[label] = post_http(
                {"jsonrpc": "2.0", "id": counter, "method": "tools/call", "params": {"name": tool, "arguments": arguments}}
            )[0]
    counter += 1
    calls["unknown_tool"] = post_http(
        {"jsonrpc": "2.0", "id": counter, "method": "tools/call", "params": {"name": UNKNOWN_TOOL, "arguments": {}}}
    )[0]
    result["server_info"] = ((initial or {}).get("result") or {}).get("serverInfo")
    result["protocol_version"] = ((initial or {}).get("result") or {}).get("protocolVersion")
    result["tools_list"] = listed
    result["tools_list_sha256"] = sha256_value(listed)
    result["calls"] = calls

    # Transport error paths: a wrong path and a GET on the MCP endpoint.
    try:
        response = client.post(url.rstrip("/") + "/nope", json={"jsonrpc": "2.0", "id": 99, "method": "tools/list"})
        result["post_wrong_path"] = {"status": response.status_code, "body": response.text[:200]}
    except Exception as exc:  # noqa: BLE001
        result["post_wrong_path"] = {"error": f"{type(exc).__name__}: {exc}"}
    try:
        response = client.get(url)
        result["get_on_mcp"] = {"status": response.status_code, "body": response.text[:200]}
    except Exception as exc:  # noqa: BLE001
        result["get_on_mcp"] = {"error": f"{type(exc).__name__}: {exc}"}
    return result


def raw_http_like(entry: dict, call_specs: list[tuple[str, str, dict]]) -> dict:
    if entry.get("transport") == "sse":
        return raw_sse(entry, call_specs)
    return raw_streamable_http(entry, call_specs)


# ── capture ──────────────────────────────────────────────────────────────────


def capture_raw(entries: list[dict], raw_dir: Path, call_specs_path: Path) -> dict:
    """Talk to each server directly and store its literal replies."""
    raw_dir.mkdir(parents=True, exist_ok=True)
    call_specs = [
        (item["label"], item["tool"], item["arguments"])
        for item in json.loads(call_specs_path.read_text(encoding="utf-8"))
    ]
    captured = {}
    for entry in entries:
        name = entry["name"]
        if entry.get("transport") == "stdio":
            payload = raw_stdio(entry, call_specs)
        else:
            payload = raw_http_like(entry, call_specs)
        payload["server_entry"] = entry
        payload["server_entry_sha256"] = sha256_value(entry)
        (raw_dir / f"{name}.json").write_text(
            json.dumps(payload, indent=2, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8"
        )
        captured[name] = payload
        tools = ((payload.get("tools_list") or {}).get("result") or {}).get("tools", []) or []
        print(f"[raw] {name}: {len(tools)} tools")
    return captured


# ── diff ─────────────────────────────────────────────────────────────────────


def text_of_call(record: dict) -> str | None:
    """The text a Hive client would surface for one call result."""
    if not isinstance(record, dict):
        return None
    if "result" in record:
        result = record["result"] or {}
        parts = [item.get("text", "") for item in result.get("content", []) if isinstance(item, dict)]
        return "\n".join(part for part in parts if part) or None
    return None


def raw_call_failed(record: dict) -> str | None:
    if isinstance(record, dict) and "error" in record:
        return (record["error"] or {}).get("message")
    if isinstance(record, dict) and isinstance(record.get("result"), dict) and record["result"].get("isError"):
        return text_of_call(record)
    return None


GO_NAME_UNSAFE = re.compile("[^A-Za-z0-9_]+")


def sanitize_go_name(name: str) -> str:
    """Go's NamespacedMCPToolName collapses every run of non [A-Za-z0-9_]."""
    return GO_NAME_UNSAFE.sub("_", name)


def side_surface(side: dict, raw_names: list[str] | None = None) -> dict:
    """Normalize one side's tool list to *remote* tool names.

    Python registers the server's own name. Go registers
    ``mcp__<ns>__<server>__<sanitized tool>``, so the inverse mapping needs the
    server's advertised names: sanitize each and pair it back.
    """
    surface = side["surface"]
    prefix = surface.get("namespace_prefix") or ""
    inverse = {}
    for raw_name in raw_names or []:
        inverse[sanitize_go_name(raw_name)] = raw_name
    tools = {}
    for record in surface["tools"]:
        name = record["registered_name"]
        if prefix and name.startswith(prefix):
            name = name[len(prefix):]
            name = inverse.get(name, name)
        tools[name] = record
    return {
        "names": sorted(tools),
        "tools": tools,
        "calls": surface.get("calls", {}),
        "unknown_tool": surface.get("unknown_tool", {}),
        "remote_names": sorted(surface.get("remote_tool_names") or surface.get("server_tool_names") or []),
        "namespace_prefix": prefix or None,
    }


def diff_all(raw: dict, python_side: dict, go_side: dict) -> dict:
    py_servers = {server["name"]: server for server in python_side["servers"]}
    go_servers = {server["name"]: server for server in go_side["servers"]}
    dimensions: dict[str, dict] = {}
    exceptions: list[dict] = []
    unexpected: list[dict] = []

    for name, server_raw in raw.items():
        py = py_servers.get(name)
        go = go_servers.get(name)
        if py is None or go is None:
            unexpected.append({"server": name, "kind": "missing_side", "detail": {"python": py is not None, "go": go is not None}})
            continue
        entry = server_raw["server_entry"]
        transport = entry.get("transport")
        raw_tools = ((server_raw.get("tools_list") or {}).get("result") or {}).get("tools", []) or []
        raw_names = sorted(tool["name"] for tool in raw_tools)
        raw_by_name = {tool["name"]: tool for tool in raw_tools}
        py_surface, go_surface = side_surface(py, raw_names), side_surface(go, raw_names)
        both_failed = not py["raw"].get("connected") and not go["raw"].get("connected")

        if both_failed:
            # Neither tools-path client could reach the server; the surface
            # comparison would be vacuous. Record the transport fact and let the
            # http_transport exception (below) explain it.
            dimensions[name] = {
                "transport": {
                    "transport": transport,
                    "python_connected": False,
                    "go_connected": False,
                    "python_connect_error": (py["raw"].get("connect_error") or {}).get("error"),
                    "go_connect_error": (go["raw"].get("connect_error") or {}).get("error"),
                    "python_tool_count": 0,
                    "go_tool_count": 0,
                    "raw_server_info": server_raw.get("server_info"),
                    "raw_tool_count": len(raw_names),
                },
                "inventory": {"skipped": "both sides failed to connect"},
                "schema": {"skipped": "both sides failed to connect"},
                "calls": {"skipped": "both sides failed to connect"},
                "lifecycle": {"verdict": {"skipped": "both sides failed to connect"}},
                "crash": {},
            }
            continue

        # 1. inventory + schema passthrough
        inventory = {
            "raw_names": raw_names,
            "python_names": py_surface["names"],
            "go_remote_names": go_surface["remote_names"],
            "python_registered": len(py_surface["names"]),
            "go_registered": len(go_surface["tools"]),
            "go_namespace_prefix": go_surface["namespace_prefix"],
            "names_match_raw": {
                "python": py_surface["names"] == raw_names,
                "go": go_surface["remote_names"] == raw_names,
            },
        }
        schema = {"matched": [], "python_vs_raw": [], "go_vs_raw": [], "python_vs_go": []}
        for tool_name in raw_names:
            expected = normalize_schema(raw_by_name[tool_name].get("inputSchema") or {})
            py_tool = py_surface["tools"].get(tool_name)
            go_tool = go_surface["tools"].get(tool_name)
            if py_tool is None or go_tool is None:
                schema["python_vs_go"].append({"tool": tool_name, "note": "missing_on_one_side"})
                continue
            py_schema = py_tool["parameters"]
            go_schema = go_tool["parameters"]
            if py_schema == expected and go_schema == expected:
                schema["matched"].append(tool_name)
            else:
                if py_schema != expected:
                    schema["python_vs_raw"].append({"tool": tool_name, "expected": expected, "actual": py_schema})
                if go_schema != expected:
                    schema["go_vs_raw"].append({"tool": tool_name, "expected": expected, "actual": go_schema})
            if py_schema != go_schema:
                schema["python_vs_go"].append({"tool": tool_name, "python": py_schema, "go": go_schema})
            if py_tool["description"] != go_tool["description"]:
                schema.setdefault("description_mismatch", []).append({"tool": tool_name})
        dimensions.setdefault(name, {})["inventory"] = inventory
        sanitized = [tool for tool in raw_names if sanitize_go_name(tool) != tool]
        if sanitized:
            exceptions.append(
                {
                    "server": name,
                    "dimension": "tool_name_sanitization",
                    "python": sanitized,
                    "go": [sanitize_go_name(tool) for tool in sanitized],
                    "reason": (
                        "Go's NamespacedMCPToolName collapses every run of characters outside [A-Za-z0-9_] "
                        "to '_' in the tool name too, so the model sees <server tool with '_'>; Python "
                        "registers the server's own name. Execution still targets the server's real tool "
                        "name in both implementations. Collisions are possible in Go when a server "
                        "advertises names that differ only in those characters."
                    ),
                }
            )
        dimensions[name]["schema"] = schema
        if not all(inventory["names_match_raw"].values()) or schema["python_vs_go"] or schema["python_vs_raw"] or schema["go_vs_raw"]:
            unexpected.append({"server": name, "kind": "tool_surface", "detail": {"inventory": inventory, "schema": schema}})

        # 2. calls + errors
        calls = {"rows": [], "mismatched": []}
        for label, raw_record in (server_raw.get("calls") or {}).items():
            if label == "unknown_tool":
                continue
            expected_text = text_of_call(raw_record)
            expected_error = raw_call_failed(raw_record)
            row = {"label": label, "raw_ok": expected_error is None}
            for side_name, side in (("python", py_surface), ("go", go_surface)):
                record = side["calls"].get(label)
                if record is None:
                    row[side_name] = "absent"
                    continue
                content = record.get("content")
                if isinstance(content, str) and content.startswith('{"error":'):
                    content = json.loads(content).get("error")
                row[side_name] = {
                    "ok": record.get("ok"),
                    "text": mask_volatile(content),
                    "image_blocks": record.get("image_blocks"),
                    "image_kinds": record.get("image_kinds"),
                }
            # Compare the two sides with each other, and check the *raw* server
            # reply is what surfaces: a tool error arrives as a JSON error body
            # with is_error false on both sides (a client-level failure, not a
            # transport one), so the verdict is "same text, same image blocks,
            # and the server's own error text is inside it".
            py_row, go_row = row.get("python"), row.get("go")
            row["raw_error"] = raw_call_failed(raw_record)
            if isinstance(py_row, dict) and isinstance(go_row, dict):
                same_verdict = (
                    py_row["ok"] == go_row["ok"]
                    and py_row["text"] == go_row["text"]
                    and (py_row["image_blocks"] or 0) == (go_row["image_blocks"] or 0)
                    and sorted(py_row["image_kinds"] or []) == sorted(go_row["image_kinds"] or [])
                )
                row["verdict"] = "match" if same_verdict else "mismatch"
                if not same_verdict:
                    calls["mismatched"].append(row)
                # The masked server text must appear in both sides' output.
                if row["raw_error"] is None and expected_text:
                    row["raw_text_in_both"] = all(
                        mask_volatile(expected_text) in (candidate["text"] or "")
                        for candidate in (py_row, go_row)
                    )
                    if not row["raw_text_in_both"]:
                        calls["mismatched"].append(row)
                if row["raw_error"]:
                    row["raw_error_text_in_both"] = all(
                        isinstance(candidate, dict) and mask_volatile(row["raw_error"]) in (candidate["text"] or "")
                        for candidate in (py_row, go_row)
                    )
                    if not row["raw_error_text_in_both"]:
                        calls["mismatched"].append(row)
            calls["rows"].append(row)
        dimensions[name]["calls"] = calls
        if calls["mismatched"]:
            unexpected.append({"server": name, "kind": "call_results", "detail": calls["mismatched"]})

        # unknown tool: registered-name error, listed as a namespacing exception
        unknown = {"python": py_surface["unknown_tool"], "go": go_surface["unknown_tool"]}
        dimensions[name]["unknown_tool"] = unknown
        py_unknown = (unknown["python"] or {}).get("content", "")
        go_unknown = (unknown["go"] or {}).get("content", "")
        for side_name, text in (("python", py_unknown), ("go", go_unknown)):
            if UNKNOWN_TOOL not in text:
                unexpected.append({"server": name, "kind": "unknown_tool_body", "detail": {side_name: text}})
        if py_unknown != go_unknown:
            exceptions.append(
                {
                    "server": name,
                    "dimension": "unknown_tool_message",
                    "python": py_unknown,
                    "go": go_unknown,
                    "reason": "Go reports the namespaced tool name it looked up and a did-you-mean candidate; Python reports the remote name. The remote names differ by design (Go registers mcp__<server>__<tool>).",
                }
            )

        # 3. lifecycle
        lifecycle = {
            "python": py["lifecycle"],
            "go": go["lifecycle"],
        }
        py_disabled = py["lifecycle"].get("disabled_tool_count")
        go_disabled = go["lifecycle"].get("disabled_tool_count")
        py_enabled = py["lifecycle"].get("reenabled_tool_count")
        go_enabled = go["lifecycle"].get("reenabled_tool_count")
        lifecycle["verdict"] = {
            "disabled_zero_both": py_disabled == 0 and go_disabled == 0,
            "reenabled_equal_both": py_enabled == go_enabled,
            "reenabled_equals_inventory": py_enabled == len(py_surface["names"]) == go_enabled,
        }
        dimensions[name]["lifecycle"] = lifecycle
        if not all(lifecycle["verdict"].values()):
            unexpected.append({"server": name, "kind": "lifecycle", "detail": lifecycle})

        # 4. transport / crash
        transport_facts = {
            "transport": transport,
            "python_connected": py["raw"].get("connected"),
            "go_connected": go["raw"].get("connected"),
            "python_connect_error": (py["raw"].get("connect_error") or {}).get("error"),
            "go_connect_error": (go["raw"].get("connect_error") or {}).get("error"),
            "python_tool_count": len(py["raw"].get("tool_names") or []),
            "go_tool_count": len(go["raw"].get("tool_names") or []),
            "raw_server_info": server_raw.get("server_info"),
        }
        crash = {"python": py.get("crash") or {}, "go": go.get("crash") or {}}
        if transport == "stdio" and crash["python"].get("killed_count") is not None:
            crash["verdict"] = {
                "python_killed": crash["python"].get("killed_count"),
                "go_killed": crash["go"].get("killed_count"),
                "python_recovered": bool((crash["python"].get("call_after_kill") or {}).get("ok")),
                "go_recovered": bool((crash["go"].get("call_after_kill") or {}).get("ok")),
                "python_connected_after": crash["python"].get("connected_after_kill"),
                "go_connected_after": crash["go"].get("connected_after_kill"),
            }
            crash["verdict"]["both_recovered"] = crash["verdict"]["python_recovered"] and crash["verdict"]["go_recovered"]
            crash["verdict"]["sides_agree"] = crash["verdict"]["python_recovered"] == crash["verdict"]["go_recovered"]
            if not crash["verdict"]["sides_agree"]:
                unexpected.append({"server": name, "kind": "crash_recovery", "detail": crash})
        dimensions[name]["transport"] = transport_facts
        dimensions[name]["crash"] = crash
        if transport_facts["python_connected"] != transport_facts["go_connected"]:
            unexpected.append({"server": name, "kind": "transport_connect", "detail": transport_facts})
        if transport == "http" and not transport_facts["python_connected"] and not transport_facts["go_connected"]:
            exceptions.append(
                {
                    "server": name,
                    "dimension": "http_transport",
                    "python": transport_facts["python_connect_error"],
                    "go": transport_facts["go_connect_error"],
                    "reason": (
                        "Both implementations' tools-path http client post JSON-RPC to <url>/mcp/v1 "
                        "(Hive's own bridge protocol) and never negotiate the MCP streamable-HTTP transport, "
                        "so both fail identically against a standard /mcp endpoint. The documented path for a "
                        "standard HTTP MCP server is CLI mode (hive mcp run -> mcp2cli), covered by the "
                        "cli_transport probe in this evidence set."
                    ),
                }
            )

    return {"dimensions": dimensions, "exceptions": exceptions, "unexpected": unexpected}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--servers", required=True)
    parser.add_argument("--raw-dir", required=True)
    parser.add_argument("--python", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--calls", default="", help="call spec JSON written by the runner")
    parser.add_argument("--capture-only", action="store_true")
    args = parser.parse_args()

    entries = json.loads(Path(args.servers).read_text(encoding="utf-8"))["servers"]
    calls_path = Path(args.calls) if args.calls else Path(args.raw_dir).parent / "call-specs.json"
    raw = capture_raw(entries, Path(args.raw_dir), calls_path)
    if args.capture_only:
        return 0

    python_side = json.loads(Path(args.python).read_text(encoding="utf-8"))
    go_side = json.loads(Path(args.go).read_text(encoding="utf-8"))
    diff = diff_all(raw, python_side, go_side)

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / "diff.json").write_text(
        json.dumps(diff, indent=2, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8"
    )

    lines = ["# 真实第三方 MCP server 双跑差异表", ""]
    for name, dimensions in sorted(diff["dimensions"].items()):
        inventory = dimensions["inventory"]
        schema = dimensions["schema"]
        lines.append(f"## {name} ({dimensions['transport']['transport']})")
        lines.append("")
        if inventory.get("skipped"):
            lines.append(f"- 两侧 tools 路径都连不上（{inventory['skipped']}）；raw 直连拿到 {dimensions['transport'].get('raw_tool_count')} 个工具")
            lines.append(
                f"  - Python: {str(dimensions['transport'].get('python_connect_error'))[:200]}"
            )
            lines.append(f"  - Go: {str(dimensions['transport'].get('go_connect_error'))[:200]}")
            lines.append("")
            continue
        lines.append(
            f"- 工具清单：raw {len(inventory['raw_names'])} / Python {inventory['python_registered']} / "
            f"Go {inventory['go_registered']}；名字与 server 一致："
            f"Python={inventory['names_match_raw']['python']}，Go(去命名空间)={inventory['names_match_raw']['go']}"
        )
        lines.append(
            f"- schema 透传：{len(schema['matched'])} 个工具两侧都等于 `{'{type,properties,required}'}` 归一化后的 server schema；"
            f"与 raw 不一致：Python={len(schema['python_vs_raw'])}，Go={len(schema['go_vs_raw'])}；两侧互不一致：{len(schema['python_vs_go'])}"
        )
        call_rows = dimensions["calls"]["rows"]
        lines.append(
            f"- 调用：{len(call_rows)} 个用例，逐字段一致 {sum(1 for row in call_rows if row.get('verdict') == 'match')}，"
            f"不一致 {len(dimensions['calls']['mismatched'])}"
        )
        for row in call_rows:
            if row.get("verdict") == "mismatch":
                lines.append(f"  - `{row['label']}`：{json.dumps(row, ensure_ascii=False)}")
        verdict = dimensions["lifecycle"]["verdict"]
        lines.append(
            f"- 生命周期：disable 后两侧 0 个工具={verdict['disabled_zero_both']}；enable 后计数相等="
            f"{verdict['reenabled_equal_both']}（Python {dimensions['lifecycle']['python'].get('reenabled_tool_count')} / "
            f"Go {dimensions['lifecycle']['go'].get('reenabled_tool_count')}）"
        )
        crash = dimensions["crash"].get("verdict")
        if crash:
            lines.append(
                f"- 崩溃恢复：杀掉 server 进程 Python {crash['python_killed']} 个 / Go {crash['go_killed']} 个；"
                f"杀后调用成功 Python={crash['python_recovered']} Go={crash['go_recovered']}"
            )
        transport_facts = dimensions["transport"]
        lines.append(
            f"- 传输：连接 Python={transport_facts['python_connected']} Go={transport_facts['go_connected']}；"
            f"工具数 Python={transport_facts['python_tool_count']} Go={transport_facts['go_tool_count']}"
        )
        lines.append("")
    lines.append("## 明确例外（非未知差异）")
    lines.append("")
    for exception in diff["exceptions"]:
        lines.append(f"- **{exception['server']} / {exception['dimension']}**：{exception['reason']}")
    lines.append("")
    lines.append(f"未知差异：{len(diff['unexpected'])}")
    (out_dir / "summary.md").write_text("\n".join(lines) + "\n", encoding="utf-8")

    print(json.dumps({"unexpected": len(diff["unexpected"]), "exceptions": len(diff["exceptions"])}))
    if diff["unexpected"]:
        print(json.dumps(diff["unexpected"], indent=2, ensure_ascii=False)[:4000], file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
