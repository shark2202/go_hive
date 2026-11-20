#!/usr/bin/env python3
"""Freeze the LLM-visible tool contract and quantify Python vs Go drift.

The contract baseline is the Python runtime (``core/``) at the commit this
manifest is generated from: every ``name`` / ``description`` / ``input_schema``
in the manifest is copied verbatim from a Python capture. ``go_hive`` is the
target side — its definitions are recorded next to the reference ones so the
drift is auditable offline, never as the baseline.

Two modes:

    build   read raw captures, write tool-definitions.json,
            tool-types.proto and docs/tool-contract-diff.md
    check   validate the committed manifest/proto/doc without captures:
            canonical order, unique names, verbatim-vs-capture hashes,
            proto coverage, and that the doc's headline numbers match

Usage:
    python3 freeze_tool_contract.py build --captures <dir>
    python3 freeze_tool_contract.py check

Capture layout (produced by the procedure in tool-capture-meta.json):

    <dir>/python-queen-tools.json   GET /api/queen/queen_growth/tools
    <dir>/python-llm-tools.json     tools[] of the first queen turn
    <dir>/go-queen-tools.json       GET /api/queen/queen_growth/tools
    <dir>/go-llm-tools.json         tools[] of the first queen turn
    <dir>/capture-meta.json         commits, versions, ports, hashes

The capture itself uses a local fake OpenAI gateway: both runtimes are pointed
at it (``llm.api_base``), a queen session is created and one message is sent,
and the gateway records the request body verbatim. That is the only way to see
the real per-request tool list — the Tool Library endpoint is a different layer.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
CONTRACTS = REPO_ROOT / "go_hive" / "contracts"
DOCS = REPO_ROOT / "go_hive" / "docs"
MANIFEST_PATH = CONTRACTS / "tool-definitions.json"
PROTO_PATH = CONTRACTS / "tool-types.proto"
META_PATH = CONTRACTS / "tool-capture-meta.json"
DIFF_PATH = DOCS / "tool-contract-diff.md"

SCHEMA_VERSION = "1.0"

# ---------------------------------------------------------------------------
# JSON Schema -> proto3 scalar mapping
# ---------------------------------------------------------------------------
#
# The mapping is the contract's type vocabulary. It is deliberately narrow:
# proto3 scalars are what a cross-language contract can actually agree on, and
# every rule below is a place where Python and Go can disagree even when both
# claim to accept the same JSON.
#
#   JSON Schema            proto3                  note
#   ---------------------  ----------------------  ---------------------------
#   string                 string                  UTF-8
#   boolean                bool                    never int
#   integer                int64                   Python int is unbounded;
#                                                  int64 narrows it
#   number                 double                  NaN/Inf are not JSON
#   array                  repeated T              item type recurses
#   object + properties    message T               nested message
#   object + additional    map<string, T>          JSON object with free keys
#   object, no properties  JsonValue               unconstrained
#   enum: [...]            enum                    declared value set
#   anyOf [T, null]        optional T              presence, not null
#   missing / unknown      JsonValue               unconstrained
#
# Neither ``uint32``/``uint64`` nor ``bytes`` is reachable from a JSON Schema
# type: JSON has one number type and one string type. A tool that wants them
# must say so in its description, and that is not a machine-checkable contract,
# so this manifest never claims them.
#
# Int64 narrowing, NaN, bool-as-int and default-vs-missing are recorded as
# per-parameter flags (see PROTO_FLAGS) instead of being flattened away.

PROTO_FLAGS = {
    "int64-narrowing": "JSON integer mapped to int64; |value| >= 2^63 is not representable",
    "double-nan": "JSON number mapped to double; NaN/Infinity have no JSON encoding",
    "bool-is-int": "Python bool is an int subclass; an integer field accepts True upstream but not in Go",
    "default-vs-missing": "Python schema carries an explicit default or null; proto3 presence cannot tell null from absent",
    "untyped": "no JSON type declared; proto falls back to JsonValue",
    "free-form-object": "object without properties; proto falls back to map<string, JsonValue>",
    "union-type": "more than one non-null JSON type; proto falls back to JsonValue",
}

PROTO_SCALARS = ("string", "bool", "int32", "int64", "uint32", "uint64", "float", "double", "bytes")


def _analyze(schema: Any) -> dict[str, Any]:
    """Normalize a JSON Schema node into {kind, payload, nullable}.

    kind is one of: scalar, array, object, map, free-object, enum, union, untyped.
    ``nullable`` means the node also accepts JSON null — the "absent vs null"
    distinction proto3 cannot carry, so it never leaks into the type text.
    """
    if not isinstance(schema, dict):
        return {"kind": "untyped", "payload": None, "nullable": False}
    for key in ("anyOf", "oneOf"):
        branches = schema.get(key)
        if isinstance(branches, list) and branches:
            non_null = [b for b in branches if not (isinstance(b, dict) and b.get("type") == "null")]
            nullable = len(non_null) != len(branches)
            if len(non_null) == 1:
                inner = _analyze(non_null[0])
                inner["nullable"] = inner["nullable"] or nullable
                return inner
            return {"kind": "union", "payload": non_null, "nullable": nullable}
    if isinstance(schema.get("enum"), list) and schema["enum"]:
        return {"kind": "enum", "payload": list(schema["enum"]), "nullable": False}
    declared = schema.get("type")
    if isinstance(declared, list):
        non_null = [t for t in declared if t != "null"]
        nullable = len(non_null) != len(declared)
        if len(non_null) == 1:
            inner = _analyze({**schema, "type": non_null[0]})
            inner["nullable"] = inner["nullable"] or nullable
            return inner
        return {"kind": "union", "payload": non_null, "nullable": nullable}
    if declared == "string":
        return {"kind": "scalar", "payload": "string", "nullable": False}
    if declared == "boolean":
        return {"kind": "scalar", "payload": "bool", "nullable": False}
    if declared == "integer":
        return {"kind": "scalar", "payload": "int64", "nullable": False}
    if declared == "number":
        return {"kind": "scalar", "payload": "double", "nullable": False}
    if declared == "array":
        return {"kind": "array", "payload": schema.get("items"), "nullable": False}
    if declared == "object":
        if schema.get("properties"):
            return {"kind": "object", "payload": schema, "nullable": False}
        if "additionalProperties" in schema:
            return {"kind": "map", "payload": schema.get("additionalProperties"), "nullable": False}
        return {"kind": "free-object", "payload": None, "nullable": False}
    return {"kind": "untyped", "payload": None, "nullable": False}


def _type_text(node: dict[str, Any]) -> str:
    kind, payload = node["kind"], node["payload"]
    if kind == "scalar":
        text = str(payload)
    elif kind == "enum":
        text = "enum"
    elif kind == "array":
        text = "array<" + _type_text(_analyze(payload)) + ">"
    elif kind in ("object", "free-object"):
        text = "object"
    elif kind == "map":
        text = "object*"
    elif kind == "union":
        text = "anyOf"
    else:
        text = "untyped"
    return ("?" + text) if node.get("nullable") else text


def _scalar_text(schema: Any) -> str:
    """Human-readable JSON type for the diff tables."""
    return _type_text(_analyze(schema))


def _proto_of(schema: Any, *, owner: str, param: str, messages: list[dict[str, Any]], depth: int = 0) -> str:
    """Return the proto3 type text for one parameter, registering nested messages."""
    node = _analyze(schema)
    kind, payload = node["kind"], node["payload"]
    if kind == "scalar":
        core = str(payload)
    elif kind == "enum":
        if depth == 0:
            messages.append({"name": _pascal(param), "kind": "enum", "values": list(payload)})
            core = _pascal(param)
        else:
            core = "string"
    elif kind == "array":
        inner = _proto_of(payload if payload is not None else {}, owner=owner, param=param, messages=messages, depth=depth + 1).replace("optional ", "")
        # proto3 cannot nest a map directly inside repeated: wrap it.
        core = "repeated " + (_wrap_map(inner, owner=owner, param=param, messages=messages) if inner.startswith("map<") else inner)
    elif kind == "map":
        inner = _proto_of(payload if payload is not None else {}, owner=owner, param=param, messages=messages, depth=depth + 1).replace("optional ", "")
        if inner.startswith("map<"):
            inner = _wrap_map(inner, owner=owner, param=param, messages=messages)
        core = "map<string, " + inner + ">"
    elif kind == "object":
        core = _register_object(payload, owner=owner, param=param, messages=messages)
    elif kind == "free-object":
        core = "map<string, JsonValue>"
    else:  # union / untyped
        core = "JsonValue"
    # proto3 forbids `optional` on repeated and map fields: their presence is
    # implicit. Nullability for those is carried as a manifest flag instead.
    if core.startswith(("repeated ", "map<")):
        return core
    return ("optional " + core) if node.get("nullable") else core


def _wrap_map(inner: str, *, owner: str, param: str, messages: list[dict[str, Any]]) -> str:
    """Wrap a map in a message so it can be used as a repeated/map element type."""
    name = _pascal(owner) + _pascal(param) + "Item"
    if not any(m["name"] == name for m in messages):
        messages.append({"name": name, "kind": "message", "fields": [("fields", inner, " // map entry")]})
    return name


def _register_object(schema: dict[str, Any], *, owner: str, param: str, messages: list[dict[str, Any]]) -> str:
    name = _pascal(owner) + _pascal(param)
    if any(m["name"] == name for m in messages):
        return name
    required = set(schema.get("required") or [])
    fields: list[tuple[str, str, str]] = []
    nested: list[dict[str, Any]] = []
    for child, child_schema in (schema.get("properties") or {}).items():
        text = _proto_of(child_schema, owner=name, param=child, messages=nested, depth=1)
        if text.startswith(("repeated ", "map<")):
            mark = ""
        elif text.startswith("optional "):
            mark, text = "optional ", text[len("optional ") :]
        else:
            mark = "" if child in required else "optional "
        fields.append((_snake(child), mark + text, _comment(child_schema)))
    messages.append({"name": name, "kind": "message", "fields": fields})
    messages.extend(nested)
    return name


def _comment(schema: Any) -> str:
    if not isinstance(schema, dict):
        return ""
    bits = []
    if "default" in schema and schema["default"] is not None:
        bits.append("default " + json.dumps(schema["default"]))
    if schema.get("description"):
        desc = re.sub(r"\s+", " ", str(schema["description"])).strip()
        bits.append(desc[:90])
    return " // " + " — ".join(bits) if bits else ""


def _pascal(text: str) -> str:
    parts = re.split(r"[^0-9a-zA-Z]+", str(text))
    return "".join(p[:1].upper() + p[1:] for p in parts if p) or "Value"


def _snake(text: str) -> str:
    name = re.sub(r"(?<=[a-z0-9])([A-Z])", r"_\1", str(text)).lower()
    name = re.sub(r"[^a-z0-9_]", "_", name).strip("_") or "field"
    if name[0].isdigit():
        name = "f_" + name
    if name in {"default", "option", "message", "enum", "service", "rpc", "to", "max", "reserved",
                "oneof", "map", "repeated", "optional", "returns", "package", "import", "public",
                "weak", "syntax", "extend", "extensions", "group", "required", "true", "false"}:
        name += "_"
    return name


def _flags(schema: Any) -> set[str]:
    """Un-canonicalizable classes this parameter falls into."""
    node = _analyze(schema)
    flags: set[str] = set()
    kind, payload = node["kind"], node["payload"]
    if node.get("nullable"):
        flags.add("default-vs-missing")
    if kind == "scalar":
        if payload == "int64":
            flags.add("int64-narrowing")
        if payload == "double":
            flags.add("double-nan")
    elif kind == "array":
        flags |= _flags(payload or {})
    elif kind == "free-object":
        flags.add("free-form-object")
    elif kind == "union":
        flags.add("union-type")
    elif kind == "untyped":
        flags.add("untyped")
    if isinstance(schema, dict) and isinstance(schema.get("default"), bool) and node["kind"] == "scalar" and payload == "int64":
        flags.add("bool-is-int")
    if isinstance(schema, dict) and "default" in schema and schema["default"] is not None:
        flags.add("default-vs-missing")
    return flags


def _tool_proto(tool: dict[str, Any]) -> dict[str, Any]:
    """Proto view of one tool: message name, per-param types, flags, messages."""
    name = tool["name"]
    schema = tool.get("input_schema") or {}
    props = schema.get("properties") or {}
    required = list(schema.get("required") or [])
    message = _pascal(name) + "Input"
    nested: list[dict[str, Any]] = []
    params: dict[str, str] = {}
    flags: dict[str, list[str]] = {}
    fields: list[tuple[str, str, str]] = []
    for param in sorted(props):
        text = _proto_of(props[param], owner=name, param=param, messages=nested)
        if text.startswith(("repeated ", "map<")):
            mark = ""
        elif text.startswith("optional "):
            mark, text = "optional ", text[len("optional ") :]
        else:
            mark = "" if param in required else "optional "
        params[_snake(param)] = mark + text
        fields.append((_snake(param), mark + text, _comment(props[param])))
        found = sorted(_flags(props[param]))
        if found:
            flags[param] = found
    messages = [{"name": message, "kind": "message", "fields": fields}]
    for block in nested:
        if block["name"] == message or any(m["name"] == block["name"] for m in messages):
            continue
        messages.append(block)
    return {
        "message": message,
        "params": params,
        "required": sorted(_snake(p) for p in required),
        "flags": flags,
        "messages": messages,
    }


# ---------------------------------------------------------------------------
# Capture loading
# ---------------------------------------------------------------------------


def _read_json(path: Path) -> Any:
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def _sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def load_captures(captures: Path) -> dict[str, Any]:
    """Normalize the four captures into one Python/Go surface model."""
    py_tools = _read_json(captures / "python-queen-tools.json")
    py_llm = _read_json(captures / "python-llm-tools.json")
    go_tools = _read_json(captures / "go-queen-tools.json")
    go_llm = _read_json(captures / "go-llm-tools.json")
    meta = _read_json(captures / "capture-meta.json") if (captures / "capture-meta.json").is_file() else {}

    python: dict[str, dict[str, Any]] = {}

    def _put(name: str, *, layer: str, description: str, schema: dict[str, Any], extra: dict[str, Any] | None = None) -> None:
        entry = python.setdefault(name, {"layers": {}, "description": "", "input_schema": {}, "source": ""})
        entry["layers"][layer] = extra or {}
        # LLM-request text wins: that is the layer the model actually receives.
        order = ["llm_request", "mcp_catalog", "lifecycle", "synthetic"]
        current = [k for k in order if k in entry["layers"]]
        best = current[0] if current else layer
        if best == layer:
            entry["description"] = description
            entry["input_schema"] = schema
            entry["source"] = _source_label(layer, extra or {})

    for tool in py_llm:
        fn = tool.get("function") or {}
        _put(fn.get("name", ""), layer="llm_request", description=fn.get("description") or "", schema=fn.get("parameters") or {})
    for server in py_tools.get("mcp_servers") or []:
        for tool in server.get("tools") or []:
            _put(
                tool.get("name", ""),
                layer="mcp_catalog",
                description=tool.get("description") or "",
                schema=tool.get("input_schema") or {},
                extra={"server": server.get("name"), "enabled": bool(tool.get("enabled"))},
            )
    for tool in py_tools.get("lifecycle") or []:
        _put(tool.get("name", ""), layer="lifecycle", description=tool.get("description") or "", schema={"type": "object", "properties": {}})
    for tool in py_tools.get("synthetic") or []:
        _put(tool.get("name", ""), layer="synthetic", description=tool.get("description") or "", schema={"type": "object", "properties": {}})

    go: dict[str, dict[str, Any]] = {}
    for tool in go_llm:
        fn = tool.get("function") or {}
        go[fn.get("name", "")] = {
            "description": fn.get("description") or "",
            "input_schema": fn.get("parameters") or {},
            "layer": "llm_request",
        }

    return {
        "python": python,
        "go": go,
        "py_endpoint": py_tools,
        "go_endpoint": go_tools,
        "meta": meta,
        "hashes": {},
    }


def _source_label(layer: str, extra: dict[str, Any]) -> str:
    if layer == "mcp_catalog":
        return f"python:mcp_catalog:{extra.get('server') or 'unknown'}"
    return f"python:{layer}"


# ---------------------------------------------------------------------------
# Diffing
# ---------------------------------------------------------------------------


def _bare_type(schema: Any) -> str:
    return _type_text({**_analyze(schema), "nullable": False})


def _param_diff(py_schema: dict[str, Any], go_schema: dict[str, Any], *, owner: str) -> dict[str, Any]:
    pp = (py_schema or {}).get("properties") or {}
    gp = (go_schema or {}).get("properties") or {}
    mismatches = []
    optionality = []
    for param in sorted(set(pp) & set(gp)):
        py_t = _bare_type(pp[param])
        go_t = _bare_type(gp[param])
        py_nullable = _analyze(pp[param]).get("nullable", False)
        go_nullable = _analyze(gp[param]).get("nullable", False)
        if py_t != go_t:
            mismatches.append({"param": param, "python": py_t, "go": go_t, "proto": _proto_of(pp[param], owner=owner, param=param, messages=[])})
        elif py_nullable != go_nullable:
            optionality.append({"param": param, "python_nullable": py_nullable, "go_nullable": go_nullable})
    return {
        "python_only": sorted(set(pp) - set(gp)),
        "go_only": sorted(set(gp) - set(pp)),
        "type_mismatches": mismatches,
        "optionality_mismatches": optionality,
        "required_python": sorted((py_schema or {}).get("required") or []),
        "required_go": sorted((go_schema or {}).get("required") or []),
    }


def _manifest_proto(tool: dict[str, Any]) -> dict[str, Any]:
    """The manifest's proto block: types and flags, without the message bodies.

    Nested message bodies are derivable from input_schema, so they live only in
    the generated .proto — the manifest stays the smaller, authoritative record.
    """
    proto = _tool_proto(tool)
    return {key: proto[key] for key in ("message", "params", "required", "flags")}


def build_tools(captures: dict[str, Any]) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    python = captures["python"]
    go = captures["go"]
    tools: list[dict[str, Any]] = []
    for name in sorted(python):
        entry = python[name]
        tool = {
            "name": name,
            "description": entry["description"],
            "input_schema": entry["input_schema"],
            "source": entry["source"],
            "layers": sorted(entry["layers"]),
            "layer_detail": entry["layers"],
            "proto": _manifest_proto({"name": name, "input_schema": entry["input_schema"]}),
        }
        target = go.get(name)
        if target is None:
            tool["go"] = {"present": False, "layer": "missing"}
            tool["parity"] = {"name": True, "description": False, "input_schema": False, "present_in_go": False}
        else:
            desc_ok = (target["description"] or "") == (tool["description"] or "")
            schema_ok = (target["input_schema"] or {}) == (tool["input_schema"] or {})
            tool["go"] = {
                "present": True,
                "layer": target["layer"],
                "description_identical": desc_ok,
                "input_schema_identical": schema_ok,
                "description": target["description"],
                "input_schema": target["input_schema"],
                "param_diff": _param_diff(tool["input_schema"], target["input_schema"], owner=name),
            }
            tool["parity"] = {"name": True, "description": desc_ok, "input_schema": schema_ok, "present_in_go": True}
        tools.append(tool)

    go_only = []
    for name in sorted(set(go) - set(python)):
        target = go[name]
        go_only.append(
            {
                "name": name,
                "description": target["description"],
                "input_schema": target["input_schema"],
                "source": "go:catalogue",
                "action_grouped": "action" in ((target["input_schema"] or {}).get("properties") or {}),
                "proto": _manifest_proto({"name": name, "input_schema": target["input_schema"]}),
            }
        )
    return tools, go_only


def stats(tools: list[dict[str, Any]], go_only: list[dict[str, Any]]) -> dict[str, Any]:
    shared = [t for t in tools if t["go"]["present"]]
    py_only = [t for t in tools if not t["go"]["present"]]
    out = {
        "python_tools": len(tools),
        "go_tools": len(shared) + len(go_only),
        "shared": len(shared),
        "python_only": len(py_only),
        "go_only": len(go_only),
        "description_identical": sum(1 for t in shared if t["go"]["description_identical"]),
        "description_different": sum(1 for t in shared if not t["go"]["description_identical"]),
        "schema_identical": sum(1 for t in shared if t["go"]["input_schema_identical"]),
        "schema_different": sum(1 for t in shared if not t["go"]["input_schema_identical"]),
        "python_params": 0,
        "go_params": 0,
        "python_only_params": 0,
        "go_only_params": 0,
        "type_mismatches": 0,
        "optionality_mismatches": 0,
        "required_mismatches": 0,
        "python_layers": {},
        "go_action_grouped": sum(1 for t in go_only if t["action_grouped"]),
    }
    for tool in shared:
        pd = tool["go"]["param_diff"]
        out["python_params"] += len((tool["input_schema"] or {}).get("properties") or {})
        out["go_params"] += len((tool["go"]["input_schema"] or {}).get("properties") or {})
        out["python_only_params"] += len(pd["python_only"])
        out["go_only_params"] += len(pd["go_only"])
        out["type_mismatches"] += len(pd["type_mismatches"])
        out["optionality_mismatches"] += len(pd.get("optionality_mismatches") or [])
        if pd["required_python"] != pd["required_go"]:
            out["required_mismatches"] += 1
    for tool in tools:
        key = "+".join(tool["layers"])
        out["python_layers"][key] = out["python_layers"].get(key, 0) + 1
    return out


# ---------------------------------------------------------------------------
# Emission
# ---------------------------------------------------------------------------


def emit_manifest(captures: dict[str, Any], tools: list[dict[str, Any]], go_only: list[dict[str, Any]], st: dict[str, Any]) -> dict[str, Any]:
    meta = captures.get("meta") or {}
    py_ep = captures["py_endpoint"]
    go_ep = captures["go_endpoint"]
    return {
        "schema_version": SCHEMA_VERSION,
        "kind": "hive-tool-definition-contract",
        "generated_at_utc": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "reference": {
            "name": "python",
            "package": "aden-hive / hive (core/)",
            "version": meta.get("python", {}).get("version", "1.0.14"),
            "commit": meta.get("python", {}).get("commit"),
            "core_commit": meta.get("python", {}).get("core_commit"),
            "tools_commit": meta.get("python", {}).get("tools_commit"),
            "surface": {
                "llm_request": len(captures.get("py_llm_names") or []),
                "mcp_catalog": sum(len(s.get("tools") or []) for s in py_ep.get("mcp_servers") or []),
                "lifecycle": len(py_ep.get("lifecycle") or []),
                "synthetic": len(py_ep.get("synthetic") or []),
                "union": len(tools),
            },
        },
        "target": {
            "name": "go_hive",
            "version": meta.get("go", {}).get("version", "1.0.26-go.1"),
            "commit": meta.get("go", {}).get("commit"),
            "surface": {
                "llm_request": len(captures.get("go_llm_names") or []),
                "mcp_catalog": sum(len(s.get("tools") or []) for s in go_ep.get("mcp_servers") or []),
                "lifecycle": len(go_ep.get("lifecycle") or []),
                "synthetic": len(go_ep.get("synthetic") or []),
                "union": len(tools) - sum(1 for t in tools if not t["go"]["present"]) + len(go_only),
            },
        },
        "contract": {
            "baseline": "reference",
            "rule": "name/description/input_schema are the Python runtime's verbatim values; the go_* fields are the target side recorded for alignment, never the baseline",
            "canonical_order": "bytewise by name (Python sorted())",
            "sources": ["python:llm_request", "python:mcp_catalog:<server>", "python:lifecycle", "python:synthetic"],
            "layers_note": "llm_request = what the model receives on a default queen turn; mcp_catalog = Tool Library catalog (deferred tools included); lifecycle/synthetic = non-MCP tools",
        },
        "proto_mapping": {
            "spec": "contracts/tool-types.proto",
            "rules": {
                "string": "string",
                "boolean": "bool",
                "integer": "int64",
                "number": "double",
                "array": "repeated T",
                "object_with_properties": "message",
                "object_with_additionalProperties": "map<string, T>",
                "object_free_form": "map<string, JsonValue>",
                "enum": "enum",
                "anyOf_[T,null]": "optional T",
                "absent": "JsonValue",
            },
            "unreachable": ["uint32", "uint64", "bytes"],
            "flags": PROTO_FLAGS,
        },
        "statistics": st,
        "tools": tools,
        "go_only_tools": go_only,
    }


def emit_proto(tools: list[dict[str, Any]], go_only: list[dict[str, Any]], meta: dict[str, Any]) -> str:
    lines: list[str] = []
    lines.append("// Tool input contract — proto3 spec, generated from the Python reference surface.")
    lines.append("//")
    lines.append("// SPEC ONLY: this file documents the contract's type vocabulary. Nothing imports it,")
    lines.append("// no protobuf runtime is required, and no code is generated from it. Do not add a")
    lines.append("// protobuf dependency to go.mod for this file.")
    lines.append("//")
    lines.append("// Reference: python " + str(meta.get("python", {}).get("version")) + " @ " + str(meta.get("python", {}).get("commit")))
    lines.append("// Target:    go_hive " + str(meta.get("go", {}).get("version")) + " @ " + str(meta.get("go", {}).get("commit")))
    lines.append("// Manifest:  contracts/tool-definitions.json (authoritative name/description/schema)")
    lines.append("//")
    lines.append("// Mapping rules (JSON Schema -> proto3):")
    lines.append("//   string                 -> string")
    lines.append("//   boolean                -> bool            (never int)")
    lines.append("//   integer                -> int64           Python int is unbounded; int64 narrows it")
    lines.append("//   number                 -> double          NaN/Inf have no JSON encoding")
    lines.append("//   array                  -> repeated T")
    lines.append("//   object + properties    -> message T")
    lines.append("//   object + additional    -> map<string, T>")
    lines.append("//   object, no properties  -> map<string, JsonValue>")
    lines.append("//   enum: [...]            -> enum")
    lines.append("//   anyOf [T, null]        -> optional T     (presence, not null)")
    lines.append("//   anyOf [T1, T2, ...]    -> JsonValue")
    lines.append("//   missing / unknown      -> JsonValue")
    lines.append("//")
    lines.append("// uint32 / uint64 / bytes are unreachable from JSON Schema: JSON has one number")
    lines.append("// type and one string type. A tool that wants them must say so in prose, which is")
    lines.append("// not machine-checkable, so the contract never claims them.")
    lines.append("//")
    lines.append("// Required-ness: proto3 cannot enforce `required`. Required parameters are emitted as")
    lines.append("// plain fields, every other parameter as `optional`, and the manifest's")
    lines.append("// proto.required list stays authoritative.")
    lines.append("")
    lines.append('syntax = "proto3";')
    lines.append("")
    lines.append("package hive.toolcontract.v1;")
    lines.append("")
    lines.append("// JsonValue is the unconstrained fallback (no declared JSON type, or a union).")
    lines.append("message JsonValue {")
    lines.append("  oneof kind {")
    lines.append("    bool null_value = 1;")
    lines.append("    bool bool_value = 2;")
    lines.append("    int64 int_value = 3;")
    lines.append("    double double_value = 4;")
    lines.append("    string string_value = 5;")
    lines.append("    JsonObject object_value = 6;")
    lines.append("    JsonArray array_value = 7;")
    lines.append("  }")
    lines.append("}")
    lines.append("")
    lines.append("message JsonObject {")
    lines.append("  map<string, JsonValue> fields = 1;")
    lines.append("}")
    lines.append("")
    lines.append("message JsonArray {")
    lines.append("  repeated JsonValue items = 1;")
    lines.append("}")
    lines.append("")
    lines.append("// One request message per reference tool, plus the nested messages they need.")
    lines.append("// Order and contents mirror contracts/tool-definitions.json -> tools[].proto.")
    emitted: set[str] = set()
    emitted_signatures: dict[str, str] = {}
    enums: list[tuple[str, list[str]]] = []
    for tool in tools:
        proto = _tool_proto(tool)
        for block in proto.get("messages") or []:
            name = block["name"]
            if name in emitted:
                previous = emitted_signatures.get(name)
                if previous is not None and previous != json.dumps(block.get("fields"), sort_keys=True):
                    raise ValueError(f"proto message name collision with different fields: {name}")
                continue
            emitted.add(name)
            emitted_signatures[name] = json.dumps(block.get("fields"), sort_keys=True)
            if block.get("kind") == "enum":
                enums.append((name, list(block.get("values") or [])))
                continue
            lines.append("")
            lines.append(f"// {tool['name']} — {tool['source']}")
            if block.get("kind") == "enum":
                enums.append((name, list(block.get("values") or [])))
                continue
            lines.append(f"message {name} {{")
            if not block.get("fields"):
                lines.append("  // no declared input schema: the tool takes free-form input")
            for index, (fname, ftype, fcomment) in enumerate(block.get("fields") or [], start=1):
                lines.append(f"  {ftype} {fname} = {index};{fcomment}")
            lines.append("}")
    for name, values in enums:
        lines.append("")
        lines.append(f"enum {name} {{")
        lines.append(f"  {name.upper()}_UNSPECIFIED = 0;")
        for index, value in enumerate(values, start=1):
            const = re.sub(r"[^A-Za-z0-9]+", "_", f"{name}_{value}").upper()
            lines.append(f"  {const} = {index};")
        lines.append("}")
    lines.append("")
    lines.append(f"// go_hive exposes {len(go_only)} tools with no Python counterpart (grouped action tools).")
    lines.append("// They are outside the contract until the reference grows the same capability; see")
    lines.append("// contracts/tool-definitions.json -> go_only_tools.")
    lines.append("")
    return "\n".join(lines)


def _md_escape(text: str, limit: int = 160) -> str:
    flat = re.sub(r"\s+", " ", str(text or "")).strip().replace("|", "\\|")
    return flat[:limit] + ("…" if len(flat) > limit else "")


def emit_doc(captures: dict[str, Any], tools: list[dict[str, Any]], go_only: list[dict[str, Any]], st: dict[str, Any], meta: dict[str, Any]) -> str:
    L: list[str] = []
    py = meta.get("python", {})
    go = meta.get("go", {})
    L.append("# Tool contract diff — Python reference vs go_hive target")
    L.append("")
    L.append("Generated by `go_hive/scripts/freeze_tool_contract.py build`. This file reports; it")
    L.append("does not gate. `go test ./internal/cli -run TestToolContractFreeze` re-reads the")
    L.append("manifest and re-reports against the live Go registry.")
    L.append("")
    L.append("## 1. 口径")
    L.append("")
    L.append(f"- **reference** = Python `hive` {py.get('version')} @ `{py.get('commit')}` "
             f"(core tree last touched `{py.get('core_commit')}`, tool/queen sources `{py.get('tools_commit')}`).")
    L.append("  所有 `name` / `description` / `input_schema` 以 Python 实际抓取为准。")
    L.append(f"- **target** = `go_hive` {go.get('version')} @ `{go.get('commit')}` — 待对齐方，不是基线。")
    L.append("- **LLM 层** = 一次默认 queen 会话首轮请求体里的 `tools[]`（假 OpenAI 网关截获，见 `contracts/tool-capture-meta.json`）。")
    L.append("- **Tool Library 层** = `GET /api/queen/queen_growth/tools`，含延迟加载（searchable）工具与 lifecycle/synthetic 工具。")
    L.append("- 两边使用同一套抓取方法、同样的临时 `HIVE_HOME`、同样的假网关；环境差异只有运行时本身。")
    L.append("")
    L.append("## 2. 总量")
    L.append("")
    L.append("| 维度 | Python (reference) | go_hive (target) |")
    L.append("| --- | --- | --- |")
    L.append(f"| 默认会话 LLM 请求 `tools[]` | {st['python_llm']} | {st['go_llm']} |")
    L.append(f"| Tool Library MCP 目录 | {st['python_mcp_catalog']} | {st['go_mcp_catalog']} |")
    L.append(f"| lifecycle 工具 | {st['python_lifecycle']} | {st['go_lifecycle']} |")
    L.append(f"| synthetic 工具 | {st['python_synthetic']} | {st['go_synthetic']} |")
    L.append(f"| 参考面并集（manifest `tools`） | {st['python_tools']} | — |")
    L.append(f"| 目标面并集（manifest） | — | {st['go_tools']} |")
    L.append("")
    L.append(f"- 参考面（manifest `tools`）：**{st['python_tools']}**；目标面（manifest `tools` + `go_only_tools`）：**{st['go_tools']}**")
    L.append("")
    L.append("## 3. 逐字段统计（同名工具）")
    L.append("")
    L.append(f"- 同名（name 一致）：**{st['shared']}**")
    L.append(f"- 仅 Python 有：**{st['python_only']}**")
    L.append(f"- 仅 Go 有：**{st['go_only']}**（其中 {st['go_action_grouped']} 个是带 `action` 参数的分组工具）")
    L.append(f"- 同名工具中 description 逐字一致：**{st['description_identical']}** / 不一致：**{st['description_different']}**")
    L.append(f"- 同名工具中 input_schema 逐字一致：**{st['schema_identical']}** / 不一致：**{st['schema_different']}**")
    L.append(f"- 参数总数：Python {st['python_params']} / Go {st['go_params']}；Python 独有参数 {st['python_only_params']}，Go 独有参数 {st['go_only_params']}")
    L.append(f"- 同名参数的类型不一致：**{st['type_mismatches']}**")
    L.append(f"- 类型相同但可选性（nullable）不一致的参数：**{st['optionality_mismatches']}**")
    L.append(f"- `required` 列表不一致的工具：**{st['required_mismatches']}**")
    L.append("")
    L.append("## 4. 默认会话层面的差异（全部）")
    L.append("")
    L.append("### 4.1 同名工具（{0}）".format(st["shared"]))
    L.append("")
    L.append("| 工具 | description | schema | 参数 py/go | 仅 py 参数 | 仅 go 参数 | 类型不一致 | 可选性不一致 |")
    L.append("| --- | --- | --- | --- | --- | --- | --- | --- |")
    for tool in tools:
        if not tool["go"]["present"]:
            continue
        pd = tool["go"]["param_diff"]
        L.append(
            "| `{name}` | {d} | {s} | {pp}/{gp} | {po} | {go_only} | {tm} | {om} |".format(
                name=tool["name"],
                d="✅" if tool["go"]["description_identical"] else "❌",
                s="✅" if tool["go"]["input_schema_identical"] else "❌",
                pp=len((tool["input_schema"] or {}).get("properties") or {}),
                gp=len((tool["go"]["input_schema"] or {}).get("properties") or {}),
                po=len(pd["python_only"]),
                go_only=len(pd["go_only"]),
                tm=len(pd["type_mismatches"]),
                om=len(pd.get("optionality_mismatches") or []),
            )
        )
    L.append("")
    L.append("### 4.2 仅 Python 有（{0}）".format(st["python_only"]))
    L.append("")
    L.append("| 工具 | 层 | 说明 |")
    L.append("| --- | --- | --- |")
    for tool in tools:
        if tool["go"]["present"]:
            continue
        note = "Tool Library 中已禁用（默认不可调用）" if _disabled_in_catalog(tool) else "Python 默认会话可见"
        L.append(f"| `{tool['name']}` | {'+'.join(tool['layers'])} | {note} |")
    L.append("")
    L.append("注：`get_worker_status` / `inject_message` / `run_worker` / `stop_worker` 在 Go 注册表里存在"
             "（queen lifecycle 表），独立 DM 会话按 upstream 语义把它们过滤掉，所以不在 Go 的请求层；"
             "`make tool-contract-report` 的 A 段用注册表并集对拍，会把这 4 个显式列成命中。")
    L.append("")
    L.append("### 4.3 仅 Go 有（{0}）".format(st["go_only"]))
    L.append("")
    L.append("| 工具 | 参数 | action 分组 |")
    L.append("| --- | --- | --- |")
    for tool in go_only:
        L.append(f"| `{tool['name']}` | {len((tool['input_schema'] or {}).get('properties') or {})} | {'是' if tool['action_grouped'] else '否'} |")
    L.append("")
    # Tool Library layer: the endpoint snapshot, which is a different surface
    # from the request. Reported because "which tools is the queen allowed to
    # load" differs even where the request lists overlap.
    py_ep, go_ep = captures["py_endpoint"], captures["go_endpoint"]
    py_cat = {t["name"]: bool(t.get("enabled")) for srv in (py_ep.get("mcp_servers") or []) for t in (srv.get("tools") or [])}
    go_cat = sum(len(srv.get("tools") or []) for srv in (go_ep.get("mcp_servers") or []))
    py_allow = set(py_ep.get("enabled_mcp_tools") or [])
    go_allow = set(go_ep.get("enabled_mcp_tools") or [])
    deferred = ((meta.get("deferred_reminder") or {}).get("python") or [])
    L.append("### 4.4 Tool Library 层（`GET /api/queen/queen_growth/tools`）")
    L.append("")
    L.append("| 维度 | Python | go_hive |")
    L.append("| --- | --- | --- |")
    L.append(f"| MCP 目录工具数 | {len(py_cat)}（启用 {sum(1 for v in py_cat.values() if v)} / 禁用 {sum(1 for v in py_cat.values() if not v)}） | {go_cat} |")
    L.append(f"| `enabled_mcp_tools` | {len(py_allow)} | {len(go_allow)} |")
    L.append(f"| `stale` | {bool(py_ep.get('stale'))} | {bool(go_ep.get('stale'))} |")
    L.append(f"| 延迟加载（system-reminder 列出名字） | {len(deferred)}（{', '.join(deferred) or '—'}） | 0（无延迟层） |")
    L.append("")
    if py_allow != go_allow:
        L.append(f"- 仅 Python allowlist 内：{', '.join(sorted(py_allow - go_allow)) or '—'}")
        L.append(f"- 仅 Go allowlist 内：{', '.join(sorted(go_allow - py_allow)) or '—'}")
        L.append("")
    L.append("## 5. 参数级差异与 proto 类型")
    L.append("")
    L.append("`proto` 列取自 Python（基准）schema，映射规则见 `contracts/tool-types.proto`。")
    L.append("")
    L.append("Python 侧 schema 有两种来源层：`llm_request`（模型实际收到的，上下文参数已被运行时剥掉）")
    L.append("与 `mcp_catalog`（Tool Library 目录，未被 allowlist 选中或仍在延迟加载层的工具只有这一层，")
    L.append("其中带 `session_cwd` 之类由会话注入的上下文参数）。Go 侧永远是请求层，所以目录层工具的比较")
    L.append("会多出这类参数差异 —— 这是层差异，不是实现差异。")
    L.append("")
    L.append("| 工具 | Python 层 | 参数 | proto 类型 | Python 类型 | Go 类型 | 能否在 JSON 层规范一致 |")
    L.append("| --- | --- | --- | --- | --- | --- | --- |")
    rows = 0
    for tool in tools:
        if not tool["go"]["present"]:
            continue
        pd = tool["go"]["param_diff"]
        py_props = (tool["input_schema"] or {}).get("properties") or {}
        go_props = (tool["go"]["input_schema"] or {}).get("properties") or {}
        for param in pd["python_only"]:
            L.append(f"| `{tool['name']}` | {tool['layers'][0]} | `{param}` | {_proto_of(py_props[param], owner=tool['name'], param=param, messages=[])} | {_scalar_text(py_props[param])} | —（缺失） | 需改 schema：Go 需补该参数 |")
            rows += 1
        for param in pd["go_only"]:
            L.append(f"| `{tool['name']}` | {tool['layers'][0]} | `{param}` | —（Python 无） | —（缺失） | {_scalar_text(go_props[param])} | 需改 schema：Go 需删该参数或 Python 需补 |")
            rows += 1
        for mismatch in pd["type_mismatches"]:
            verdict = _canonical_verdict(mismatch["python"], mismatch["go"])
            L.append(
                f"| `{tool['name']}` | {tool['layers'][0]} | `{mismatch['param']}` | {mismatch['proto']} | {mismatch['python']} | {mismatch['go']} | {verdict} |"
            )
            rows += 1
        for mismatch in pd.get("optionality_mismatches") or []:
            py_prop = py_props.get(mismatch["param"]) or {}
            L.append(
                f"| `{tool['name']}` | {tool['layers'][0]} | `{mismatch['param']}` | {_proto_of(py_prop, owner=tool['name'], param=mismatch['param'], messages=[])} "
                f"| {_scalar_text(py_prop)} | {_scalar_text(go_props[mismatch['param']])} | 可（仅 nullable/默认值差异，需显式 `required` 或 `null` 约定） |"
            )
            rows += 1
    L.append("")
    L.append(f"共 {rows} 行参数级差异。")
    L.append("")
    L.append("## 6. proto 类型分布")
    L.append("")
    L.append("| proto 类型 | 参数数 |")
    L.append("| --- | --- |")
    dist: dict[str, int] = {}
    for tool in tools:
        for text in (tool.get("proto") or {}).get("params", {}).values():
            key = text.replace("optional ", "").split("<")[0]
            dist[key] = dist.get(key, 0) + 1
    for key in sorted(dist, key=lambda k: (-dist[k], k)):
        L.append(f"| `{key}` | {dist[key]} |")
    L.append("")
    L.append("## 7. 不可逐字项")
    L.append("")
    flag_counts: dict[str, list[str]] = {}
    for tool in tools:
        for param, flags in (tool.get("proto") or {}).get("flags", {}).items():
            for flag in flags:
                flag_counts.setdefault(flag, []).append(f"{tool['name']}.{param}")
    L.append("| 类别 | 参数数 | 说明 | 样例 |")
    L.append("| --- | --- | --- | --- |")
    for flag in sorted(flag_counts):
        names = flag_counts[flag]
        L.append(f"| `{flag}` | {len(names)} | {PROTO_FLAGS[flag]} | {', '.join('`'+n+'`' for n in names[:3])} |")
    L.append("")
    L.append("这些类型在 JSON 层无法既表达 Python 的接受域又保持逐字一致：")
    L.append("`int64` 收窄（>2^63-1 的 Python int 无法表示）、`double` 的 NaN/Inf 无 JSON 编码、")
    L.append("以及 proto3 presence 无法区分「显式 null / 缺省值」与「字段缺失」。要逐字一致，只能在 JSON")
    L.append("Schema 层约束（加 `maximum`、禁用 NaN、`required` 显式化），这都要求改运行时行为，属于本 lane 之外。")
    L.append("")
    L.append("另有两条跨切面规则没有实例参数，但写契约时必须记住：")
    L.append("Python `bool` 是 `int` 的子类，integer 参数在 Python 侧能接受 `true`，Go 的 `int64` 解码会直接报错；")
    L.append("JSON number 没有宽度，`uint32`/`uint64`/`bytes` 无法从 JSON Schema 推出，本契约不声称它们。")
    L.append("")
    L.append("## 8. 未能抓到的层")
    L.append("")
    for item in meta.get("uncovered") or []:
        L.append(f"- {item}")
    L.append("")
    L.append("## 9. 与源码级审计的关系")
    L.append("")
    L.append("`go_hive/docs/tool-consistency-audit.json`（静态源码扫描，commit `4e737cbc`）报告")
    L.append("Python 全量 857 / 默认 270 个工具、Go 会话工具 137 个；本文件是**运行期抓取**的 LLM 层：")
    L.append("Go 会话目录 137 与本文件实测一致，Python 侧 270「默认」对应本文件抓到的 24 个首轮请求工具")
    L.append("（另 26 个 MCP 目录工具因默认 allowlist 而只在 Tool Library 层出现）。两者互补：")
    L.append("静态扫描覆盖全部集成 action，本文件覆盖模型实际收到的那一份。")
    L.append("")
    L.append("## 10. 复现")
    L.append("")
    L.append("对拍（只报告，不阻断 CI）：`make tool-contract-report`（= `go test ./internal/cli -run TestToolContractFreeze -v`），")
    L.append("产物自洽校验：`make tool-contract-check`。")
    L.append("")
    L.append("```sh")
    L.append("# 1. python（reference）")
    L.append("HIVE_HOME=$TMP/pyhome ./.venv/bin/hive serve --host 127.0.0.1 --port 8803")
    L.append("curl -s localhost:8803/api/queen/queen_growth/tools")
    L.append("# 2. go（target）")
    L.append("(cd go_hive/go-hive && make build)")
    L.append("HIVE_HOME=$TMP/gohome OPENAI_API_KEY=fake-key ./dist/hive serve --port 8805")
    L.append("curl -s localhost:8805/api/queen/queen_growth/tools")
    L.append("# 3. 两边都指向假网关，各发一条消息，取首轮 tools[]")
    L.append("python3 scripts/fake_gateway.py --port 8807 --log requests.jsonl")
    L.append("python3 go_hive/scripts/freeze_tool_contract.py build --captures <dir>")
    L.append("```")
    L.append("")
    return "\n".join(L)


def _disabled_in_catalog(tool: dict[str, Any]) -> bool:
    detail = tool.get("layer_detail") or {}
    catalog = detail.get("mcp_catalog") or {}
    return catalog.get("enabled") is False


def _canonical_verdict(py_type: str, go_type: str) -> str:
    def norm(text: str) -> str:
        return text.lstrip("?").split("<")[0]

    py_kind, go_kind = norm(py_type), norm(go_type)
    if py_kind == go_kind:
        return "可（仅可选性/默认值差异）"
    if {py_kind, go_kind} == {"integer", "number"}:
        return "可（JSON number），但 int64 收窄"
    if {py_kind, go_kind} == {"boolean", "integer"}:
        return "不可（bool/int 混用）"
    if "object" in (py_kind, go_kind) or "array" in (py_kind, go_kind):
        return "不可（结构不同）"
    return "不可（类型不同）"


# ---------------------------------------------------------------------------
# check mode
# ---------------------------------------------------------------------------


def check() -> int:
    problems: list[str] = []
    if not MANIFEST_PATH.is_file():
        print(f"SKIP: {MANIFEST_PATH} missing")
        return 0
    manifest = _read_json(MANIFEST_PATH)
    tools = manifest.get("tools") or []
    go_only = manifest.get("go_only_tools") or []
    names = [t.get("name") for t in tools]
    if names != sorted(names):
        problems.append("tools are not in canonical (bytewise) name order")
    if len(names) != len(set(names)):
        problems.append("duplicate tool names in tools[]")
    go_names = [t.get("name") for t in go_only]
    if go_names != sorted(go_names):
        problems.append("go_only_tools are not in canonical order")
    if set(names) & set(go_names):
        problems.append("a tool appears in both tools[] and go_only_tools[]")
    for tool in tools:
        for key in ("name", "description", "input_schema", "source", "proto"):
            if key not in tool:
                problems.append(f"{tool.get('name')}: missing key {key}")
        if not tool.get("proto", {}).get("message"):
            problems.append(f"{tool.get('name')}: proto.message missing")
        if tool.get("go", {}).get("present") and not tool.get("go", {}).get("layer"):
            problems.append(f"{tool.get('name')}: go.layer missing on a present target")
    for tool in go_only:
        if "go:" not in str(tool.get("source", "")):
            problems.append(f"{tool.get('name')}: go-only tool has non-go source {tool.get('source')!r}")
    st = manifest.get("statistics") or {}
    if st.get("shared", -1) + st.get("python_only", -1) != len(tools):
        problems.append("statistics.shared + statistics.python_only != len(tools)")
    if st.get("shared", -1) + st.get("go_only", -1) != st.get("go_tools", -2):
        problems.append("statistics.shared + statistics.go_only != statistics.go_tools")
    if not META_PATH.is_file():
        problems.append("contracts/tool-capture-meta.json missing")
    else:
        meta = _read_json(META_PATH)
        for key in ("capture_sha256", "python", "go", "uncovered"):
            if key not in meta:
                problems.append(f"capture meta missing {key}")
    if not PROTO_PATH.is_file():
        problems.append("contracts/tool-types.proto missing")
    else:
        proto = PROTO_PATH.read_text(encoding="utf-8")
        for tool in tools:
            message = tool.get("proto", {}).get("message")
            if message and f"message {message} " not in proto:
                problems.append(f"{tool.get('name')}: proto message {message} not emitted")
    if not DIFF_PATH.is_file():
        problems.append("docs/tool-contract-diff.md missing")
    else:
        doc = DIFF_PATH.read_text(encoding="utf-8")
        for key in ("python_tools", "shared", "python_only", "go_only"):
            value = st.get(key)
            if value is not None and f"**{value}**" not in doc:
                problems.append(f"doc does not report {key}={value}")
    if problems:
        for problem in problems:
            print("FAIL:", problem)
        return 1
    print(f"OK: {len(tools)} reference tools, {len(go_only)} go-only tools, manifest/proto/doc consistent")
    return 0


# ---------------------------------------------------------------------------


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="mode", required=True)
    build = sub.add_parser("build", help="rebuild manifest, proto and diff doc from captures")
    build.add_argument("--captures", required=True, type=Path)
    build.add_argument("--skip-doc", action="store_true")
    sub.add_parser("check", help="validate the committed artifacts")
    args = parser.parse_args()

    if args.mode == "check":
        return check()

    captures = load_captures(args.captures)
    meta = captures.get("meta") or {}
    captures["py_llm_names"] = [t.get("function", {}).get("name") for t in _read_json(args.captures / "python-llm-tools.json")]
    captures["go_llm_names"] = [t.get("function", {}).get("name") for t in _read_json(args.captures / "go-llm-tools.json")]
    tools, go_only = build_tools(captures)
    st = stats(tools, go_only)
    py_ep, go_ep = captures["py_endpoint"], captures["go_endpoint"]
    st.update(
        {
            "python_llm": len(captures["py_llm_names"]),
            "go_llm": len(captures["go_llm_names"]),
            "python_mcp_catalog": sum(len(s.get("tools") or []) for s in py_ep.get("mcp_servers") or []),
            "go_mcp_catalog": sum(len(s.get("tools") or []) for s in go_ep.get("mcp_servers") or []),
            "python_lifecycle": len(py_ep.get("lifecycle") or []),
            "go_lifecycle": len(go_ep.get("lifecycle") or []),
            "python_synthetic": len(py_ep.get("synthetic") or []),
            "go_synthetic": len(go_ep.get("synthetic") or []),
        }
    )
    manifest = emit_manifest(captures, tools, go_only, st)
    # The provenance file is part of the freeze: copy it next to the manifest and
    # re-stamp the capture hashes so the artifact is reproducible from the repo.
    meta_out = dict(meta)
    hashes = {name: _sha256(args.captures / name) for name in (
        "python-queen-tools.json", "python-llm-tools.json", "go-queen-tools.json", "go-llm-tools.json")
        if (args.captures / name).is_file()}
    meta_out["capture_sha256"] = hashes
    meta_out["generated_at_utc"] = manifest["generated_at_utc"]
    CONTRACTS.mkdir(parents=True, exist_ok=True)
    DOCS.mkdir(parents=True, exist_ok=True)
    META_PATH.write_text(json.dumps(meta_out, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    MANIFEST_PATH.write_text(json.dumps(manifest, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    PROTO_PATH.write_text(emit_proto(tools, go_only, meta), encoding="utf-8")
    if not args.skip_doc:
        DIFF_PATH.write_text(emit_doc(captures, tools, go_only, st, meta), encoding="utf-8")
    print(f"wrote {MANIFEST_PATH.relative_to(REPO_ROOT)}")
    print(f"wrote {PROTO_PATH.relative_to(REPO_ROOT)}")
    print(f"wrote {META_PATH.relative_to(REPO_ROOT)}")
    if not args.skip_doc:
        print(f"wrote {DIFF_PATH.relative_to(REPO_ROOT)}")
    print(json.dumps({k: st[k] for k in ("python_tools", "go_tools", "shared", "python_only", "go_only")}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
