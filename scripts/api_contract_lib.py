#!/usr/bin/env python3
"""Shared helpers for the API contract freeze scripts.

Standard library only.  Two consumers:

* ``freeze_api_responses.py`` — canonicalises live responses, writes the
  frozen JSON, and renders ``go_hive/docs/api-contract-diff.md``.
* ``check_api_proto.py`` — validates the frozen shapes against the
  hand-written proto3 spec in ``go_hive/contracts/api.proto`` without
  pulling in a protobuf runtime.

Canonicalisation replaces values that legitimately differ between two
independent processes (ids, timestamps, ports, temp paths, gauges) while
keeping *presence* intact — a ``null`` stays ``null`` and a missing key stays
missing, because those are exactly the distinctions a proto3 spec has to rule
on.
"""

from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path
from typing import Any

# ---------------------------------------------------------------------------
# volatile values
# ---------------------------------------------------------------------------

# Keys whose values change per process / per run.  Value is replaced with
# ``<key>``; ``None`` is preserved so presence semantics survive.
VOLATILE_KEYS = {
    "session_id",
    "colony_id",
    "spawned_colony_id",
    "agent_path",
    "location",
    "path",
    # Absolute directory a skill was loaded from: the Python reference reads
    # the checkout, the Go target materializes its built-ins under the
    # process temp dir, so the value is deployment-specific, not contract.
    "base_dir",
    "home",
    "root",
    "cwd",
    "loaded_at",
    "uptime_seconds",
    "port",
    "pid",
    "seq",
    "snapshot_seq",
    "last_event_seq",
    "execution_id",
    "correlation_id",
    "captured_at",
    "started_at",
}

VOLATILE_KEY_SUFFIXES = ("_at",)

_STRING_MASKS: list[tuple[re.Pattern[str], str]] = [
    (re.compile(r"session_\d{8}_\d{6}_[0-9a-f]+"), "<session_id>"),
    (re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"), "<uuid>"),
    (re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?"), "<timestamp>"),
    (re.compile(r"(/private)?/var/folders/[^\s\"']+"), "<tmp-path>"),
    (re.compile(r"/tmp/[^\s\"']+"), "<tmp-path>"),
    (re.compile(r"/Users/[^\s\"']+"), "<home-path>"),
]

LONG_STRING_LIMIT = 600


def _mask_string(value: str) -> str:
    out = value
    for pattern, replacement in _STRING_MASKS:
        out = pattern.sub(replacement, out)
    if len(out) > LONG_STRING_LIMIT:
        digest = hashlib.sha256(value.encode("utf-8")).hexdigest()[:12]
        out = out[:LONG_STRING_LIMIT] + f"…[+{len(value) - LONG_STRING_LIMIT} chars sha256:{digest}]"
    return out


def canonicalize(value: Any, key: str | None = None) -> Any:
    if isinstance(value, dict):
        return {k: canonicalize(v, k) for k, v in sorted(value.items())}
    if isinstance(value, list):
        return [canonicalize(item, key) for item in value]
    if value is None:
        return None
    if key is not None and (key in VOLATILE_KEYS or key.endswith(VOLATILE_KEY_SUFFIXES)):
        # Keep the JSON type and presence, drop the value: volatile numbers
        # become 0.0 (JSON number) and strings a named placeholder.
        if isinstance(value, bool):
            return value
        if isinstance(value, (int, float)):
            return 0.0
        return f"<{key}>"
    if isinstance(value, bool):
        return value
    if isinstance(value, float):
        if value != value:
            return "<nan>"
        if value in (float("inf"), float("-inf")):
            return "<inf>"
        return value
    if isinstance(value, str):
        return _mask_string(value)
    return value


# ---------------------------------------------------------------------------
# shape extraction
# ---------------------------------------------------------------------------


def json_type(value: Any) -> str:
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "boolean"
    if isinstance(value, int):
        return "number(int)"
    if isinstance(value, float):
        return "number(float)"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    return type(value).__name__


def shape_of(value: Any, prefix: str = "", out: dict[str, str] | None = None) -> dict[str, str]:
    """Map ``dotted.path -> json type``; array elements contribute ``path[]``.

    Unlike the parity probe this unions *every* element, not the first three:
    the frozen shape has to see a field that appears on any element.
    """
    out = {} if out is None else out
    if isinstance(value, dict):
        for key in sorted(value):
            child = f"{prefix}.{key}" if prefix else key
            out[child] = json_type(value[key])
            if isinstance(value[key], (dict, list)):
                shape_of(value[key], child, out)
    elif isinstance(value, list):
        for item in value:
            if isinstance(item, (dict, list)):
                shape_of(item, f"{prefix}[]", out)
            else:
                element_key = f"{prefix}[]" if not prefix.endswith("[]") else prefix
                out[element_key] = json_type(item)
    return out


def count_arrays(value: Any, prefix: str = "", out: dict[str, int] | None = None, depth: int = 0) -> dict[str, int]:
    """Array lengths for every array at depth <= 2 (counts are contract data)."""
    out = {} if out is None else out
    if depth > 2:
        return out
    if isinstance(value, dict):
        for key in sorted(value):
            count_arrays(value[key], f"{prefix}.{key}" if prefix else key, out, depth + 1)
    elif isinstance(value, list):
        out[prefix] = len(value)
        if value and isinstance(value[0], dict):
            count_arrays(value[0], f"{prefix}[]", out, depth + 1)
    return out


def strip_paths(value: Any, dotted_paths: list[str]) -> Any:
    """Return a copy with the given dotted paths removed.

    Used to drop probe-declared non-contract fields (per-process sampler
    readings) from the frozen shape: a field that only exists when the sampler
    happens to fire must not become part of the contract, or the committed
    snapshot would demand it from every later run.
    """
    if not dotted_paths:
        return value
    removals = [path.split(".") for path in dotted_paths]
    return _strip_paths(value, removals)


def _strip_paths(value: Any, removals: list[list[str]]) -> Any:
    if isinstance(value, dict):
        out: dict[str, Any] = {}
        for key, item in value.items():
            if any(len(path) == 1 and path[0] == key for path in removals):
                continue
            nested = [path[1:] for path in removals if len(path) > 1 and path[0] == key]
            out[key] = _strip_paths(item, nested) if nested else item
        return out
    if isinstance(value, list):
        return [_strip_paths(item, removals) for item in value]
    return value


def array_container(path: str) -> str:
    index = path.rfind("[]")
    return path[:index] if index >= 0 else ""


def resolve_array(payload: Any, dotted: str) -> list | None:
    """Resolve ``a[].b`` style paths to the list at that point."""
    current = payload
    for raw in dotted.split("."):
        is_array = raw.endswith("[]")
        name = raw[:-2] if is_array else raw
        if isinstance(current, dict):
            current = current.get(name)
        elif isinstance(current, list) and current:
            current = current[0].get(name) if isinstance(current[0], dict) else None
        else:
            return None
        if is_array:
            if not isinstance(current, list):
                return None
            return current if not current else current
    return current if isinstance(current, list) else None


# ---------------------------------------------------------------------------
# proto spec parsing (no protobuf runtime)
# ---------------------------------------------------------------------------

SCALAR_TYPES = {
    "double",
    "float",
    "int32",
    "int64",
    "uint32",
    "uint64",
    "sint32",
    "sint64",
    "fixed32",
    "fixed64",
    "sfixed32",
    "sfixed64",
    "bool",
    "string",
    "bytes",
}

WELL_KNOWN_MESSAGES = {
    "google.protobuf.Timestamp",
    "google.protobuf.Struct",
    "google.protobuf.Value",
    "google.protobuf.Duration",
    "google.protobuf.Empty",
    "google.protobuf.Any",
}


class ProtoField:
    __slots__ = ("message", "name", "type", "repeated", "optional", "number", "map_key", "map_value", "oneof")

    def __init__(
        self,
        message: str,
        name: str,
        type_: str,
        repeated: bool,
        optional: bool,
        number: int,
        map_key: str = "",
        map_value: str = "",
        oneof: str = "",
    ) -> None:
        self.message = message
        self.name = name
        self.type = type_
        self.repeated = repeated
        self.optional = optional
        self.number = number
        self.map_key = map_key
        self.map_value = map_value
        self.oneof = oneof

    def rendered_type(self) -> str:
        if self.map_key:
            return f"map<{self.map_key}, {self.map_value}>"
        prefix = ""
        if self.repeated:
            prefix = "repeated "
        elif self.optional:
            prefix = "optional "
        return f"{prefix}{self.type}"

    def is_message(self) -> bool:
        return self.type not in SCALAR_TYPES


class ProtoSpec:
    def __init__(self, messages: dict[str, dict[str, ProtoField]], enums: dict[str, list[str]]) -> None:
        self.messages = messages
        self.enums = enums

    @classmethod
    def parse(cls, text: str) -> "ProtoSpec":
        # Strip comments, keep line structure.
        stripped_lines = []
        for raw in text.splitlines():
            line = raw.split("//", 1)[0]
            stripped_lines.append(line)
        text = "\n".join(stripped_lines)

        messages: dict[str, dict[str, ProtoField]] = {}
        enums: dict[str, list[str]] = {}
        stack: list[tuple[str, str]] = []  # (kind, name)
        current: dict[str, ProtoField] | None = None
        current_message = ""
        oneof_name = ""
        for raw in text.splitlines():
            line = raw.strip()
            if not line:
                continue
            if line.startswith("message "):
                name = line[len("message ") :].split("{")[0].strip()
                stack.append(("message", name))
                full = ".".join(part[1] for part in stack if part[0] == "message")
                messages.setdefault(full, {})
                current = messages[full]
                current_message = full
                continue
            if line.startswith("enum "):
                name = line[len("enum ") :].split("{")[0].strip()
                stack.append(("enum", name))
                enums.setdefault(name, [])
                continue
            if line.startswith("oneof "):
                stack.append(("oneof", line[len("oneof ") :].split("{")[0].strip()))
                oneof_name = stack[-1][1]
                continue
            if line == "}":
                if stack:
                    kind, _ = stack.pop()
                    if kind == "oneof":
                        oneof_name = ""
                    elif kind == "enum":
                        continue
                    else:
                        current = messages.get(".".join(part[1] for part in stack if part[0] == "message"))
                        current_message = current_message if current else ""
                continue
            if stack and stack[-1][0] == "enum":
                token = line.split("=")[0].strip()
                if token and token != "option" and not token.startswith("reserved"):
                    enums[stack[-1][1]].append(token)
                continue
            if current is None:
                continue
            if line.startswith(("option ", "reserved", "syntax")):
                continue
            # map<k, v> name = N;  |  [repeated|optional] type name = N;
            match = re.match(
                r"(?:(repeated|optional)\s+)?"
                r"(map<\s*([A-Za-z0-9_.]+)\s*,\s*([A-Za-z0-9_.]+)\s*>|([A-Za-z0-9_.]+))"
                r"\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(\d+)",
                line,
            )
            if not match:
                continue
            label, map_full, map_key, map_value, scalar_type, name, number = match.groups()
            if map_full and map_full.startswith("map<"):
                field = ProtoField(current_message, name, map_value, False, False, int(number), map_key, map_value, oneof_name)
            else:
                field = ProtoField(
                    current_message,
                    name,
                    scalar_type,
                    label == "repeated",
                    label == "optional",
                    int(number),
                    "",
                    "",
                    oneof_name,
                )
            current[name] = field
        return cls(messages, enums)


# ---------------------------------------------------------------------------
# endpoint -> proto message mapping + path resolution
# ---------------------------------------------------------------------------

ENDPOINT_MESSAGES: dict[str, str] = {
    "health": "HealthResponse",
    "queen_profiles": "QueenProfilesResponse",
    "queen_tools": "QueenToolsResponse",
    "skills": "SkillsResponse",
    "config_llm": "LlmConfigResponse",
    "models_overview": "ModelsOverviewResponse",
    "credentials_specs": "CredentialSpecsResponse",
    "session_create": "SessionView",
    "sessions_list": "SessionsListResponse",
    "session_detail": "SessionDetailResponse",
    "session_events": "SseStream",
    "session_events_history": "EventsHistoryResponse",
    "session_snapshot": "SnapshotResponse",
    "session_stats": "SessionStatsResponse",
    # Failure paths share the `{"error": "..."}` body.
    "session_detail_missing": "ErrorResponse",
    "session_create_invalid_body": "ErrorResponse",
    "session_resolve_missed_conflict": "ErrorResponse",
}

# Struct-typed fields accept arbitrary sub-paths (free-form JSON maps).
STRUCT_LIKE = {"google.protobuf.Struct", "google.protobuf.Value", "google.protobuf.Any"}

PROTO_ROOT = Path(__file__).resolve().parents[1] / "contracts" / "api.proto"


def load_proto(path: Path | None = None) -> ProtoSpec:
    target = path or PROTO_ROOT
    return ProtoSpec.parse(target.read_text(encoding="utf-8"))


class ResolvedField:
    def __init__(self, field: ProtoField | None, struct_key: bool = False, note: str = "") -> None:
        self.field = field
        self.struct_key = struct_key
        self.note = note


def resolve_field(spec: ProtoSpec, endpoint: str, path: str) -> ResolvedField:
    """Resolve a dotted shape path (``specs[].credential_id``) to its proto field."""
    message_name = ENDPOINT_MESSAGES.get(endpoint)
    if message_name is None or message_name not in spec.messages:
        return ResolvedField(None, note=f"no message mapped for endpoint {endpoint}")
    current = spec.messages[message_name]
    parts = path.split(".")
    last_field: ProtoField | None = None
    for index, raw in enumerate(parts):
        is_array = raw.endswith("[]")
        name = raw[:-2] if is_array else raw
        if name not in current:
            if last_field is not None and last_field.map_key:
                # Path step is a map key; the remaining path is inside the value.
                if last_field.map_value in STRUCT_LIKE:
                    return ResolvedField(last_field, struct_key=True)
                return ResolvedField(last_field, note="map value path")
            if last_field is not None and last_field.type in STRUCT_LIKE:
                return ResolvedField(last_field, struct_key=True)
            return ResolvedField(None, note=f"undeclared field {name!r} under {message_name}")
        field = current[name]
        last_field = field
        if field.map_key:
            if index < len(parts) - 1:
                # The next path segment is a map key, not a message field.
                if field.map_value in STRUCT_LIKE:
                    return ResolvedField(field, struct_key=True)
                return ResolvedField(field, note="map key")
            return ResolvedField(field)
        if is_array and field.repeated:
            target_type = field.type
        else:
            target_type = field.type
        if index == len(parts) - 1:
            return ResolvedField(field)
        if target_type in STRUCT_LIKE:
            return ResolvedField(field, struct_key=True)
        if target_type in SCALAR_TYPES:
            return ResolvedField(None, note=f"{name!r} is scalar, cannot descend")
        if target_type not in spec.messages:
            return ResolvedField(None, note=f"unknown message type {target_type!r}")
        current = spec.messages[target_type]
    return ResolvedField(last_field)


def render_json_kind(expected_json_type: str) -> str:
    mapper = {
        "string": "string",
        "boolean": "bool",
        "number(int)": "int64",
        "number(float)": "double",
        "array": "repeated",
        "object": "message",
        "null": "optional",
    }
    return mapper.get(expected_json_type, expected_json_type)


def read_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))
