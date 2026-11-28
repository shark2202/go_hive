#!/usr/bin/env python3
"""Generate the canonical OpenAPI 3.1 and AsyncAPI 3 contracts.

This is the single source of truth for ``go_hive/contracts/openapi.json`` and
``go_hive/contracts/asyncapi.json``.  It reads only frozen evidence:

* ``contracts/api-routes.json`` — the complete Go target route table;
* ``contracts/api-responses/index.json`` + ``reference/*.json`` — 107 frozen
  request/response snapshots (schema + example inference);
* ``contracts/sse-frames/index.json`` + ``reference/*.json`` — SSE frame
  goldens (event names, envelope, per-event payloads);
* ``contracts/channel-plugin-protocol.json`` — ``hive-channel/1`` JSON-RPC
  message schemas reused by the WebSocket design;
* ``docs/channel-websocket-design.json`` — the design-only WebSocket surface.

Nothing is captured live and no network access is needed.  Existing frozen
artifacts are never rewritten; this script only writes the two generated
documents.  Output is deterministic: no timestamps, sorted keys, stable
ordering, and a trailing newline.

Usage:
    uv run python go_hive/scripts/gen_openapi_contract.py --write
    uv run python go_hive/scripts/gen_openapi_contract.py --check
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any, Callable, Iterable
from urllib.parse import parse_qsl, urlsplit

REPO_ROOT = Path(__file__).resolve().parents[2]
GO_ROOT = REPO_ROOT / "go_hive"
CONTRACTS_DIR = GO_ROOT / "contracts"

API_ROUTES_PATH = CONTRACTS_DIR / "api-routes.json"
RESPONSES_INDEX_PATH = CONTRACTS_DIR / "api-responses" / "index.json"
RESPONSES_REFERENCE_DIR = CONTRACTS_DIR / "api-responses" / "reference"
SSE_INDEX_PATH = CONTRACTS_DIR / "sse-frames" / "index.json"
SSE_REFERENCE_DIR = CONTRACTS_DIR / "sse-frames" / "reference"
CHANNEL_PROTOCOL_PATH = CONTRACTS_DIR / "channel-plugin-protocol.json"
WS_DESIGN_PATH = GO_ROOT / "docs" / "channel-websocket-design.json"
SSE_CAPTURE_PATH = GO_ROOT / "docs" / "e2e-assets" / "sse-payload-parity" / "capture-reference.json"

OPENAPI_PATH = CONTRACTS_DIR / "openapi.json"
ASYNCAPI_PATH = CONTRACTS_DIR / "asyncapi.json"

OPENAPI_VERSION = "3.1.0"
ASYNCAPI_VERSION = "3.0.0"
OPENAPI_CONTRACT_SCHEMA = "hive-openapi-contract/1"
ASYNCAPI_CONTRACT_SCHEMA = "hive-asyncapi-contract/1"

HTTP_METHODS = ("get", "put", "post", "delete", "options", "head", "patch", "trace")
OPERATION_METHODS = ("GET", "POST", "PUT", "PATCH", "DELETE")
SSE_STREAMS = ("live", "history", "replay", "stop")

TAG_DESCRIPTIONS = {
    "agents": "Agent inventory and metadata.",
    "browser": "Browser bridge contexts, tabs, status and control.",
    "channels": "Channel plugin status and transport configuration.",
    "claude-code": "Claude Code queen conversations and session mirrors.",
    "colonies": "Colony runtime, data tables, imports and worker profiles.",
    "config": "Configuration sections, models, providers and rate limits.",
    "credentials": "Credential store, OAuth handshakes and provider specs.",
    "crm": "Colony CRM grid data.",
    "data": "Global data table inspection.",
    "events": "Global event multiplexer.",
    "global": "Global CRM and data table surfaces.",
    "health": "Health and readiness probes.",
    "janitor": "Janitor maintenance reports.",
    "mcp": "MCP server registry and health.",
    "memories": "Memory files and memory catalog.",
    "models": "Model catalog and endpoint probes.",
    "queen": "Queen profiles, personas, prompts, skills and growth tools.",
    "runtime-logs": "Process runtime log ring buffer.",
    "sessions": "Session lifecycle, events, workers, attachments and reminders.",
    "skills": "Skill library, scopes and installs.",
    "spa": "Single-page application fallback.",
    "startup-status": "Process startup stages.",
    "tools": "Tool catalog.",
}

CONVENTIONS = [
    "required lists the properties present in every reference sample; it is not a handler guarantee.",
    "Properties absent from the frozen reference snapshot are not declared, but additionalProperties is true.",
    "Arrays with no reference elements leave items unconstrained instead of inventing an item schema.",
    "Request bodies are not frozen; request schemas are intentionally absent.",
    "Non-2xx responses appear only when a frozen failure snapshot exists for that route.",
    "SSE and text bodies are typed as strings; frame-level SSE shapes live in asyncapi.json.",
    "Routes without a frozen response snapshot carry x-contract-status: partial and no invented schema.",
]

ERROR_RESPONSE_SCHEMA: dict[str, Any] = {
    "type": "object",
    "description": (
        "Generic Hive error body. Frozen failure snapshots show an `error` string; "
        "additional fields (code, signed_out, detail, ...) are preserved by "
        "additionalProperties and are documented on route-specific schemas when "
        "a snapshot carries them."
    ),
    "required": ["error"],
    "properties": {"error": {"type": "string"}},
    "additionalProperties": True,
    "x-schema-source": "observed-failure-snapshots",
}


class ContractError(RuntimeError):
    """Raised when a frozen artifact is internally inconsistent."""


# ---------------------------------------------------------------------------
# IO / rendering
# ---------------------------------------------------------------------------


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ContractError(f"missing contract artifact: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ContractError(f"invalid JSON in {path}: {exc}") from exc


def render_json(document: Any) -> str:
    return json.dumps(document, ensure_ascii=False, indent=2, sort_keys=True) + "\n"


def file_sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def tree_sha256(paths: Iterable[Path]) -> str:
    digest = hashlib.sha256()
    for path in sorted(paths):
        digest.update(path.relative_to(REPO_ROOT).as_posix().encode("utf-8"))
        digest.update(b"\0")
        digest.update(hashlib.sha256(path.read_bytes()).digest())
    return digest.hexdigest()


def source_entry(path: Path, *, kind: str = "file") -> dict[str, Any]:
    entry: dict[str, Any] = {
        "path": path.relative_to(REPO_ROOT).as_posix(),
        "sha256": file_sha256(path),
    }
    if kind != "file":
        entry["kind"] = kind
    return entry


# ---------------------------------------------------------------------------
# Route helpers
# ---------------------------------------------------------------------------


def split_path(path: str) -> list[str | None]:
    """Split a path template; ``{param}`` segments become ``None``."""
    raw = urlsplit(path).path
    segments: list[str | None] = []
    for segment in raw.split("/"):
        if segment.startswith("{") and segment.endswith("}"):
            segments.append(None)
        else:
            segments.append(segment)
    return segments


def path_parameters(path: str) -> list[str]:
    return re.findall(r"\{([^{}]+)\}", path)


def match_route(routes: list[dict[str, Any]], method: str, concrete_path: str) -> dict[str, Any] | None:
    """Find the route template matching a concrete snapshot path.

    Parameter names differ between the Python and Go route tables, so names are
    treated as wildcards.  Literal segments win over parameter segments, which
    disambiguates ``/api/credentials/specs`` from
    ``/api/credentials/{credential_id}``.
    """
    target = split_path(concrete_path)
    candidates: list[tuple[int, str, dict[str, Any]]] = []
    for route in routes:
        if route["method"].upper() != method.upper():
            continue
        template = split_path(route["path"])
        if len(template) != len(target):
            continue
        matched = True
        for expected, actual in zip(template, target):
            if expected is None:
                if actual == "":
                    matched = False
                    break
            elif expected != actual:
                matched = False
                break
        if not matched:
            continue
        parameter_count = sum(1 for segment in template if segment is None)
        candidates.append((parameter_count, route["path"], route))
    if not candidates:
        return None
    candidates.sort(key=lambda item: (item[0], item[1]))
    return candidates[0][2]


def pascal(text: str) -> str:
    parts = re.findall(r"[A-Za-z0-9]+", text)
    return "".join(part[:1].upper() + part[1:] for part in parts)


def route_operation_id(method: str, path: str) -> str:
    if path == "/":
        return f"{method.lower()}SpaFallback"
    parts: list[str] = [method.lower()]
    for segment in path.split("/"):
        if not segment:
            continue
        if segment == "api":
            parts.append("Api")
        elif segment.startswith("{") and segment.endswith("}"):
            parts.append("By" + pascal(segment[1:-1]))
        else:
            parts.append(pascal(segment))
    operation_id = "".join(parts)
    return operation_id or f"{method.lower()}Root"


def route_tag(path: str) -> str:
    if path == "/":
        return "spa"
    segments = [segment for segment in path.split("/") if segment and not segment.startswith("{")]
    if segments and segments[0] == "api":
        if len(segments) > 1:
            return segments[1].replace("_", "-")
        return "api"
    return segments[0] if segments else "root"


def base_media_type(content_type: str) -> str:
    return content_type.split(";", 1)[0].strip() or "application/octet-stream"


# ---------------------------------------------------------------------------
# JSON Schema inference
# ---------------------------------------------------------------------------


def _schema_key(schema: Any) -> str:
    return json.dumps(schema, ensure_ascii=False, sort_keys=True)


def _schema_types(schema: dict[str, Any]) -> set[str]:
    declared = schema.get("type")
    if isinstance(declared, str):
        return {declared}
    if isinstance(declared, list):
        return {item for item in declared if isinstance(item, str)}
    return set()


def hint_to_type(hint: str | None) -> str | None:
    if not hint:
        return None
    normalized = hint.strip()
    if normalized.startswith("number(int)"):
        return "integer"
    if normalized.startswith("number"):
        return "number"
    if normalized in {"string", "boolean", "integer", "number", "object", "array", "null"}:
        return normalized
    return None


def infer_schema(value: Any, hint_for: Callable[[str], str | None], path: str = "") -> dict[str, Any]:
    """Infer a JSON Schema from one example value.

    ``hint_for`` supplies a known JSON type for a path (shape maps for REST,
    the raw SSE capture for placeholder-typed values).
    """
    if isinstance(value, dict):
        properties = {
            key: infer_schema(value[key], hint_for, f"{path}.{key}" if path else key) for key in sorted(value)
        }
        return {
            "type": "object",
            "properties": properties,
            "required": sorted(properties),
            "additionalProperties": True,
        }
    if isinstance(value, list):
        child_path = f"{path}[]"
        item_schemas = [infer_schema(item, hint_for, child_path) for item in value]
        merged = merge_schemas(item_schemas) if item_schemas else {}
        schema: dict[str, Any] = {"type": "array", "items": merged}
        if not item_schemas:
            schema["description"] = (
                "No elements observed in the reference snapshot; item shape is not frozen."
            )
        return schema

    hint = hint_to_type(hint_for(path))
    if hint in {"integer", "number", "string", "boolean"}:
        return {"type": hint}
    if hint == "null":
        return {"type": "null"}
    if value is None:
        return {"type": "null"}
    if isinstance(value, bool):
        return {"type": "boolean"}
    if isinstance(value, int):
        return {"type": "integer"}
    if isinstance(value, float):
        return {"type": "number"}
    return {"type": "string"}


def merge_schemas(schemas: list[dict[str, Any]]) -> dict[str, Any]:
    """Merge schemas inferred from several samples into one schema."""
    unique: dict[str, dict[str, Any]] = {}
    for schema in schemas:
        unique.setdefault(_schema_key(schema), schema)
    values = list(unique.values())
    if not values:
        return {}
    if len(values) == 1:
        return values[0]

    types: set[str] = set()
    for schema in values:
        types |= _schema_types(schema)

    if "object" in types and types <= {"object", "null"}:
        objects = [schema for schema in values if "object" in _schema_types(schema)]
        required_sets = [set(schema.get("required", [])) for schema in objects]
        property_samples: dict[str, list[dict[str, Any]]] = defaultdict(list)
        for schema in objects:
            for key, prop in schema.get("properties", {}).items():
                property_samples[key].append(prop)
        merged: dict[str, Any] = {
            "type": ["object", "null"] if "null" in types else "object",
            "properties": {
                key: merge_schemas(property_samples[key]) for key in sorted(property_samples)
            },
            "required": sorted(set.intersection(*required_sets)) if required_sets else [],
            "additionalProperties": True,
        }
        return merged

    if "array" in types and types <= {"array", "null"}:
        arrays = [schema for schema in values if "array" in _schema_types(schema)]
        item_schemas = [schema.get("items", {}) for schema in arrays]
        if any(not item for item in item_schemas):
            items: dict[str, Any] = {}
        else:
            items = merge_schemas(item_schemas)
        return {"type": ["array", "null"] if "null" in types else "array", "items": items}

    scalars = types - {"null"}
    if scalars == {"integer", "number"}:
        scalars = {"number"}
    if len(scalars) == 1:
        scalar = next(iter(scalars))
        if "null" in types:
            return {"type": [scalar, "null"]}
        return {"type": scalar}
    return {"anyOf": sorted(values, key=_schema_key)}


def is_error_only(value: Any) -> bool:
    return isinstance(value, dict) and set(value) == {"error"} and isinstance(value.get("error"), str)


# ---------------------------------------------------------------------------
# Snapshot loading
# ---------------------------------------------------------------------------


def load_snapshots() -> tuple[dict[str, Any], list[dict[str, Any]]]:
    index = load_json(RESPONSES_INDEX_PATH)
    snapshots: list[dict[str, Any]] = []
    for endpoint in index.get("endpoints", []):
        reference_file = CONTRACTS_DIR / "api-responses" / endpoint["reference_file"]
        payload = load_json(reference_file)
        snapshots.append({**endpoint, "payload": payload, "reference_path": reference_file})
    snapshots.sort(key=lambda item: item["endpoint"])
    return index, snapshots


def skipped_by_route(
    index: dict[str, Any], routes: list[dict[str, Any]]
) -> dict[tuple[str, str], dict[str, Any]]:
    skipped: dict[tuple[str, str], dict[str, Any]] = {}
    for entry in index.get("skipped", []):
        route = match_route(routes, entry["method"], entry["path"])
        if route is None:
            continue
        skipped[(route["method"], route["path"])] = entry
    return skipped


def observed_query_parameters(snapshots: list[dict[str, Any]], method: str, path: str) -> list[dict[str, Any]]:
    """Return optional query parameters observed on matching snapshot URLs."""
    observed: dict[str, dict[str, Any]] = {}
    for snapshot in snapshots:
        if snapshot["method"].upper() != method.upper():
            continue
        if match_route([{"method": snapshot["method"], "path": path}], snapshot["method"], snapshot["path"]) is None:
            continue
        query = urlsplit(snapshot["path"]).query
        for name, value in parse_qsl(query, keep_blank_values=True):
            entry = observed.setdefault(
                name,
                {
                    "name": name,
                    "in": "query",
                    "required": False,
                    "description": "Observed in a frozen reference snapshot; requiredness is not frozen.",
                    "schema": {"type": "integer" if re.fullmatch(r"-?\d+", value) else "string"},
                    "x-snapshot-observed": True,
                },
            )
            if entry["schema"]["type"] == "string" and re.fullmatch(r"-?\d+", value):
                entry["schema"]["type"] = "integer"
    return [observed[name] for name in sorted(observed)]


# ---------------------------------------------------------------------------
# OpenAPI generation
# ---------------------------------------------------------------------------


def _unique_name(base: str, used: set[str]) -> str:
    candidate = base
    suffix = 2
    while candidate in used:
        candidate = f"{base}{suffix}"
        suffix += 1
    used.add(candidate)
    return candidate


def _content_type_category(content_type: str) -> str:
    media = base_media_type(content_type).lower()
    if "event-stream" in media:
        return "sse"
    if "json" in media:
        return "json"
    return "text"


def _body_example(canonical: Any) -> str | None:
    if isinstance(canonical, str):
        return canonical
    if isinstance(canonical, dict) and isinstance(canonical.get("body"), str):
        return canonical["body"]
    return None


def build_openapi() -> dict[str, Any]:
    manifest = load_json(API_ROUTES_PATH)
    raw_routes = manifest.get("go", {}).get("routes", [])
    if not raw_routes:
        raise ContractError("api-routes.json has no go.routes entries")
    routes = sorted(raw_routes, key=lambda route: (route["path"], route["method"]))

    index, snapshots = load_snapshots()
    skipped = skipped_by_route(index, routes)
    reference_files = sorted(RESPONSES_REFERENCE_DIR.glob("*.json"))

    snapshots_by_route: dict[tuple[str, str], list[dict[str, Any]]] = defaultdict(list)
    unmapped: list[str] = []
    for snapshot in snapshots:
        route = match_route(routes, snapshot["method"], snapshot["path"])
        if route is None:
            unmapped.append(f"{snapshot['method']} {snapshot['path']}")
            continue
        snapshots_by_route[(route["method"], route["path"])].append(snapshot)
    if unmapped:
        raise ContractError("snapshots without a target route: " + ", ".join(unmapped))

    components: dict[str, Any] = {"ErrorResponse": dict(ERROR_RESPONSE_SCHEMA)}
    used_names = set(components)
    paths: dict[str, Any] = {}
    tag_names: set[str] = set()
    counters = {
        "operations": 0,
        "operations_with_snapshot": 0,
        "operations_complete": 0,
        "operations_partial": 0,
        "operations_skipped": 0,
        "responses": 0,
        "response_schemas": 0,
        "snapshot_endpoints_mapped": 0,
        "example_bytes": 0,
    }
    methods: dict[str, int] = defaultdict(int)

    for route in routes:
        method = route["method"].upper()
        path = route["path"]
        key = (route["method"], route["path"])
        snapshots_for_route = sorted(
            snapshots_by_route.get(key, []), key=lambda item: (item["reference_status"], item["endpoint"])
        )
        by_status: dict[int, list[dict[str, Any]]] = defaultdict(list)
        for snapshot in snapshots_for_route:
            by_status[int(snapshot["payload"].get("status", snapshot["reference_status"]))].append(snapshot)

        responses: dict[str, Any] = {}
        operation_has_2xx_schema = False

        for status in sorted(by_status):
            group = by_status[status]
            primary = sorted(
                group, key=lambda item: (0 if item["kind"] == "success" else 1, item["endpoint"])
            )[0]
            payload = primary["payload"]
            content_type = str(payload.get("content_type", "application/json"))
            media_type = base_media_type(content_type)
            category = _content_type_category(content_type)
            canonical = payload.get("canonical")

            media: dict[str, Any] = {}
            if category == "json":
                if is_error_only(canonical):
                    schema: dict[str, Any] = {"$ref": "#/components/schemas/ErrorResponse"}
                else:
                    component_name = _unique_name(f"{pascal(primary['endpoint'])}Response", used_names)
                    component = infer_schema(
                        canonical,
                        lambda json_path, shape=payload.get("shape") or {}: shape.get(json_path),
                    )
                    component["x-schema-source"] = "reference-snapshot"
                    component["x-snapshot-endpoint"] = primary["endpoint"]
                    component["x-content-type"] = content_type
                    components[component_name] = component
                    schema = {"$ref": f"#/components/schemas/{component_name}"}
                media = {"schema": schema, "example": canonical}
                if 200 <= status < 300:
                    operation_has_2xx_schema = True
            elif category == "sse":
                media = {
                    "schema": {
                        "type": "string",
                        "description": (
                            "text/event-stream wire payload; frame-level event names and "
                            "payloads are frozen in asyncapi.json."
                        ),
                    }
                }
            else:
                body = _body_example(canonical)
                media = {
                    "schema": {"type": "string", "description": f"Raw {media_type} body."},
                }
                if body is not None:
                    media["example"] = body

            response: dict[str, Any] = {
                "description": (
                    f"Frozen reference snapshot: {primary['endpoint']} "
                    f"({status}, {content_type})."
                    if len(group) == 1
                    else "Frozen reference snapshots: "
                    + ", ".join(item["endpoint"] for item in group)
                    + f" ({status}, {content_type})."
                ),
                "content": {media_type: media},
                "x-snapshot-endpoints": sorted(item["endpoint"] for item in group),
                "x-content-type": content_type,
                "x-contract-status": "complete" if category == "json" else "partial",
            }
            if category == "json":
                counters["response_schemas"] += 1
            counters["example_bytes"] += len(json.dumps(canonical, ensure_ascii=False))
            responses[str(status)] = response
            counters["responses"] += 1
            counters["snapshot_endpoints_mapped"] += len(group)

        operation_status = "partial"
        if operation_has_2xx_schema:
            operation_status = "complete"
        if not snapshots_for_route:
            skip = skipped.get(key)
            if skip is not None:
                operation_status = "skipped"
            else:
                operation_status = "partial"
            responses["default"] = {
                "description": (
                    skip["reason"]
                    if skip is not None
                    else "No frozen response snapshot for this route; schema intentionally omitted."
                ),
                "x-contract-status": operation_status,
            }
            counters["responses"] += 1

        operation_id = route_operation_id(method, path)
        operation: dict[str, Any] = {
            "operationId": operation_id,
            "summary": f"{method} {path}",
            "description": (
                f"Target route from api-routes.json ({route.get('kind', 'api')}); "
                f"handler {route.get('handler', 'unknown')} at {route.get('source', 'unknown')}."
            ),
            "tags": [route_tag(path)],
            "responses": responses,
            "x-contract-status": operation_status,
            "x-route-kind": route.get("kind", "api"),
            "x-handler": route.get("handler", "unknown"),
            "x-source": route.get("source", "unknown"),
        }
        if snapshots_for_route:
            operation["x-snapshot-endpoints"] = sorted(item["endpoint"] for item in snapshots_for_route)
        if route.get("extension"):
            operation["x-extension"] = True
            if route.get("annotation"):
                operation["x-annotation"] = route["annotation"]
        skip = skipped.get(key)
        if skip is not None and not snapshots_for_route:
            operation["x-skip-class"] = skip.get("class")
            operation["x-skip-reason"] = skip.get("reason")
        if path.startswith("/api/sessions/") and path.endswith("/events"):
            operation["x-asyncapi-channel"] = "sessionEventsLive"
        elif path.startswith("/api/sessions/") and path.endswith("/events/history"):
            operation["x-asyncapi-channel"] = "sessionEventsHistory"

        params: list[dict[str, Any]] = []
        for parameter_name in path_parameters(path):
            params.append(
                {
                    "name": parameter_name,
                    "in": "path",
                    "required": True,
                    "description": "Path parameter preserved from api-routes.json.",
                    "schema": {"type": "string"},
                }
            )
        params.extend(observed_query_parameters(snapshots, method, path))
        if params:
            operation["parameters"] = params

        paths.setdefault(path, {})[method.lower()] = operation
        counters["operations"] += 1
        methods[method] += 1
        tag_names.add(operation["tags"][0])
        if snapshots_for_route:
            counters["operations_with_snapshot"] += 1
        if operation_status == "complete":
            counters["operations_complete"] += 1
        elif operation_status == "skipped":
            counters["operations_skipped"] += 1
        else:
            counters["operations_partial"] += 1

    tags = [
        {"name": name, "description": TAG_DESCRIPTIONS.get(name, f"Routes tagged `{name}`.")}
        for name in sorted(tag_names)
    ]

    go_version = str(manifest.get("go", {}).get("version", "")).strip()
    version = go_version.split()[-1] if go_version else "0.0.0"
    coverage = {
        "target_routes": len(routes),
        "operations": counters["operations"],
        "paths": len(paths),
        "tags": len(tags),
        "methods": {method: methods[method] for method in sorted(methods)},
        "snapshot_endpoints": len(snapshots),
        "snapshot_endpoints_mapped": counters["snapshot_endpoints_mapped"],
        "operations_with_snapshot": counters["operations_with_snapshot"],
        "operations_complete": counters["operations_complete"],
        "operations_partial": counters["operations_partial"],
        "operations_skipped": counters["operations_skipped"],
        "operations_without_snapshot": counters["operations"] - counters["operations_with_snapshot"],
        "responses": counters["responses"],
        "response_schemas": counters["response_schemas"],
        "schemas": len(components),
        "skipped_routes": sorted(
            f"{method} {route_path}" for method, route_path in skipped.keys()
        ),
    }
    sources = [
        source_entry(API_ROUTES_PATH),
        source_entry(RESPONSES_INDEX_PATH),
        {
            "path": RESPONSES_REFERENCE_DIR.relative_to(REPO_ROOT).as_posix(),
            "kind": "reference-snapshot-dir",
            "files": len(reference_files),
            "sha256": tree_sha256(reference_files),
        },
    ]

    return {
        "openapi": OPENAPI_VERSION,
        "info": {
            "title": "Hive HTTP API",
            "version": version,
            "description": (
                "Additive OpenAPI 3.1 contract for every route in "
                "contracts/api-routes.json. Response schemas and examples are inferred "
                "from the frozen reference snapshots in contracts/api-responses; routes "
                "without a snapshot are marked x-contract-status: partial rather than "
                "given an invented schema. Legacy frozen artifacts remain the evidence "
                "of record until migration completes."
            ),
        },
        "servers": [
            {
                "url": "/",
                "description": "hive serve origin (relative; host and port are deployment-configured).",
            }
        ],
        "tags": tags,
        "paths": paths,
        "components": {"schemas": {name: components[name] for name in sorted(components)}},
        "x-contract-status": "partial",
        "x-contract": {
            "schema": OPENAPI_CONTRACT_SCHEMA,
            "status": "partial",
            "generator": "go_hive/scripts/gen_openapi_contract.py",
            "checker": "go_hive/scripts/check_openapi_contract.py",
            "canonical": True,
            "sources": sources,
            "coverage": coverage,
            "conventions": CONVENTIONS,
        },
    }


# ---------------------------------------------------------------------------
# AsyncAPI generation
# ---------------------------------------------------------------------------

CHANNEL_DEF_NAMES = {
    "id": "ChannelId",
    "request": "ChannelRequest",
    "notification": "ChannelNotification",
    "response": "ChannelResponse",
    "error_response": "ChannelErrorResponse",
    "error": "ChannelError",
    "welcome": "ChannelWelcome",
    "question": "ChannelQuestion",
    "output": "ChannelOutput",
}


def rewrite_refs(value: Any) -> Any:
    if isinstance(value, dict):
        rewritten = {key: rewrite_refs(item) for key, item in value.items()}
        ref = rewritten.get("$ref")
        if isinstance(ref, str) and ref.startswith("#/$defs/"):
            name = ref[len("#/$defs/") :]
            mapped = CHANNEL_DEF_NAMES.get(name)
            if mapped is None:
                raise ContractError(f"unmapped channel protocol $def: {name}")
            rewritten["$ref"] = f"#/components/schemas/{mapped}"
        return rewritten
    if isinstance(value, list):
        return [rewrite_refs(item) for item in value]
    return value


def _json_type_name(value: Any) -> str:
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "boolean"
    if isinstance(value, int):
        return "integer"
    if isinstance(value, float):
        return "number"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        return "array"
    return "object"


def _raw_type_hints(capture: dict[str, Any]) -> dict[tuple[str, int], dict[str, str]]:
    """Map (stream, frame index, data-relative path) -> JSON type from the raw capture."""
    streams: list[tuple[str, list[dict[str, Any]]]] = []
    for name in ("live", "replay", "stop"):
        frames = (capture.get("captures", {}).get(name) or {}).get("frames") or []
        streams.append((name, frames))
    streams.append(("history", capture.get("history") or []))
    hints: dict[tuple[str, int], dict[str, str]] = {}
    for stream, frames in streams:
        for index, frame in enumerate(frames):
            table: dict[str, str] = {}

            def walk(value: Any, path: str) -> None:
                table[path] = _json_type_name(value)
                if isinstance(value, dict):
                    for key in sorted(value):
                        walk(value[key], f"{path}.{key}" if path else key)
                elif isinstance(value, list):
                    for item in value:
                        walk(item, f"{path}[]" if path else "[]")

            walk(frame.get("data") or {}, "")
            hints[(stream, index)] = table
    return hints


def _sse_data_hint_factory(hints: dict[tuple[str, int], dict[str, str]], stream: str, index: int) -> Callable[[str], str | None]:
    table = hints.get((stream, index), {})
    return lambda path: table.get(path)


def _frame_envelope_schema() -> dict[str, Any]:
    return {
        "type": "object",
        "description": (
            "SSE frame as observed in contracts/sse-frames. Volatile leaves are "
            "canonicalised to placeholders; field presence, event names and data "
            "shapes are frozen."
        ),
        "required": ["type", "data"],
        "properties": {
            "type": {"type": "string", "description": "Event type / SSE event name."},
            "event": {
                "type": ["string", "null"],
                "description": "Optional SSE `event:` field; observed frames use the default message event.",
            },
            "stream_id": {"type": "string"},
            "node_id": {"type": ["string", "null"]},
            "execution_id": {"type": ["string", "null"]},
            "correlation_id": {"type": ["string", "null"]},
            "colony_id": {"type": ["string", "null"]},
            "run_id": {"type": "string", "description": "Canonicalised run id (`<run_id>` or `<absent>`)."},
            "seq": {"type": "integer", "description": "Monotonic per-stream sequence number."},
            "timestamp": {"type": "string", "description": "ISO-8601 timestamp."},
            "data": {"type": "object"},
        },
        "additionalProperties": True,
        "x-schema-source": "contracts/sse-frames/reference/*.json",
    }


def load_sse_frames() -> tuple[dict[str, Any], dict[str, list[dict[str, Any]]]]:
    index = load_json(SSE_INDEX_PATH)
    frames: dict[str, list[dict[str, Any]]] = {}
    for stream in SSE_STREAMS:
        data = load_json(SSE_REFERENCE_DIR / f"{stream}.json")
        frames[stream] = list(data.get("frames", []))
    return index, frames


def build_asyncapi() -> dict[str, Any]:
    protocol = load_json(CHANNEL_PROTOCOL_PATH)
    design = load_json(WS_DESIGN_PATH)
    sse_index, sse_frames = load_sse_frames()
    routes_manifest = load_json(API_ROUTES_PATH)
    routes = routes_manifest.get("go", {}).get("routes", [])

    if protocol.get("protocol") != "hive-channel/1":
        raise ContractError("channel-plugin-protocol.json is not hive-channel/1")
    if design.get("status") != "design":
        raise ContractError("channel-websocket-design.json is not marked design")

    schemas: dict[str, Any] = {}
    for name, schema in protocol.get("$defs", {}).items():
        mapped = CHANNEL_DEF_NAMES.get(name)
        if mapped is None:
            raise ContractError(f"unmapped channel protocol $def: {name}")
        schemas[mapped] = rewrite_refs(schema)

    methods = {method["name"]: method for method in protocol.get("methods", [])}

    def method_schema(method_name: str, field: str) -> dict[str, Any]:
        method = methods.get(method_name)
        if method is None:
            raise ContractError(f"channel protocol method missing: {method_name}")
        value = method.get(field)
        return rewrite_refs(value) if isinstance(value, dict) else {"type": "object"}

    def request_payload(method_name: str) -> dict[str, Any]:
        params = method_schema(method_name, "params")
        return {
            "allOf": [
                {"$ref": "#/components/schemas/ChannelRequest"},
                {
                    "type": "object",
                    "required": ["method", "params"],
                    "properties": {"method": {"const": method_name}, "params": params},
                },
            ]
        }

    def response_payload(method_name: str) -> dict[str, Any]:
        result = method_schema(method_name, "result")
        return {
            "allOf": [
                {"$ref": "#/components/schemas/ChannelResponse"},
                {
                    "type": "object",
                    "required": ["result"],
                    "properties": {"result": result},
                },
            ]
        }
    METHOD_SUMMARIES = {method["name"]: method.get("summary", "") for method in protocol.get("methods", [])}

    def message(name: str, title: str, payload: dict[str, Any], *, method_name: str, direction: str) -> dict[str, Any]:
        return {
            "name": name,
            "title": title,
            "summary": METHOD_SUMMARIES.get(method_name, ""),
            "contentType": "application/json",
            "payload": payload,
            "x-method": method_name,
            "x-direction": direction,
        }

    messages: dict[str, Any] = {}
    messages["HelloRequest"] = message(
        "HelloRequest",
        "hello request",
        request_payload("hello"),
        method_name="hello",
        direction="host_to_plugin",
    )
    messages["WelcomeResponse"] = message(
        "WelcomeResponse",
        "hello response",
        response_payload("hello"),
        method_name="hello",
        direction="plugin_to_host",
    )
    messages["StartRequest"] = message(
        "StartRequest",
        "start request",
        request_payload("start"),
        method_name="start",
        direction="host_to_plugin",
    )
    messages["StartResponse"] = message(
        "StartResponse",
        "start response",
        response_payload("start"),
        method_name="start",
        direction="plugin_to_host",
    )
    messages["StopRequest"] = message(
        "StopRequest",
        "stop request",
        request_payload("stop"),
        method_name="stop",
        direction="host_to_plugin",
    )
    messages["StopResponse"] = message(
        "StopResponse",
        "stop response",
        response_payload("stop"),
        method_name="stop",
        direction="plugin_to_host",
    )

    for method_name, title in (
        ("open_session", "open_session"),
        ("send", "send"),
        ("subscribe", "subscribe"),
        ("unsubscribe", "unsubscribe"),
        ("set_title", "set_title"),
        ("judge", "judge"),
    ):
        message_key = pascal(method_name.replace("_", " ")) + "Request"
        messages[message_key] = message(
            message_key,
            f"{title} request",
            request_payload(method_name),
            method_name=method_name,
            direction="plugin_to_host",
        )
        response_key = pascal(method_name.replace("_", " ")) + "Response"
        messages[response_key] = message(
            response_key,
            f"{title} response",
            response_payload(method_name),
            method_name=method_name,
            direction="plugin_to_host",
        )

    messages["OutputNotification"] = {
        "name": "output",
        "title": "output notification",
        "summary": "Host pushes subscription output; no id, monotonically increasing seq.",
        "contentType": "application/json",
        "payload": {
            "allOf": [
                {"$ref": "#/components/schemas/ChannelNotification"},
                {
                    "type": "object",
                    "required": ["method", "params"],
                    "properties": {
                        "method": {"const": "output"},
                        "params": {"$ref": "#/components/schemas/ChannelOutput"},
                    },
                },
            ]
        },
        "x-method": "output",
        "x-direction": "host_to_plugin",
    }
    messages["ProtocolErrorResponse"] = {
        "name": "error",
        "title": "JSON-RPC error response",
        "summary": "Error response envelope; codes and data kinds are frozen in channel-plugin-protocol.json.",
        "contentType": "application/json",
        "payload": {"$ref": "#/components/schemas/ChannelErrorResponse"},
        "x-method": "error",
        "x-direction": "both",
    }

    channel_operations: dict[str, Any] = {}
    operation_specs = [
        ("receiveHello", "receive", "HelloRequest", "Channel plugin receives hello."),
        ("sendWelcome", "send", "WelcomeResponse", "Channel plugin answers hello."),
        ("receiveStart", "receive", "StartRequest", "Channel plugin receives start."),
        ("sendStartResponse", "send", "StartResponse", "Channel plugin acknowledges start."),
        ("sendOpenSession", "send", "OpenSessionRequest", "Plugin opens or resumes a session."),
        ("receiveOpenSessionResponse", "receive", "OpenSessionResponse", "Host returns the usable session id."),
        ("sendSend", "send", "SendRequest", "Plugin sends user text and/or images."),
        ("receiveSendResponse", "receive", "SendResponse", "Host returns the session id that received the turn."),
        ("sendSubscribe", "send", "SubscribeRequest", "Plugin subscribes to session output."),
        ("receiveSubscribeResponse", "receive", "SubscribeResponse", "Host returns the subscription id."),
        ("sendUnsubscribe", "send", "UnsubscribeRequest", "Plugin drops a subscription."),
        ("receiveUnsubscribeResponse", "receive", "UnsubscribeResponse", "Host acknowledges unsubscribe."),
        ("sendSetTitle", "send", "SetTitleRequest", "Plugin sets or clears the session title."),
        ("receiveSetTitleResponse", "receive", "SetTitleResponse", "Host acknowledges set_title."),
        ("sendJudge", "send", "JudgeRequest", "Plugin asks the host judge for a probability."),
        ("receiveJudgeResponse", "receive", "JudgeResponse", "Host returns the judge probability."),
        ("receiveOutput", "receive", "OutputNotification", "Host pushes subscription output."),
        ("receiveStop", "receive", "StopRequest", "Plugin receives stop on shutdown."),
        ("sendStopResponse", "send", "StopResponse", "Plugin acknowledges stop."),
        ("receiveError", "receive", "ProtocolErrorResponse", "Plugin receives a JSON-RPC error response."),
    ]
    for operation_id, action, message_key, summary in operation_specs:
        if message_key not in messages:
            raise ContractError(f"missing channel message: {message_key}")
        channel_operations[operation_id] = {
            "action": action,
            "channel": {"$ref": "#/channels/channelWebsocket"},
            "title": operation_id,
            "summary": summary,
            "messages": [{"$ref": f"#/components/messages/{message_key}"}],
        }

    # ---- SSE surface -----------------------------------------------------
    envelopes = _frame_envelope_schema()
    schemas["SseFrame"] = envelopes

    hints = _raw_type_hints(load_json(SSE_CAPTURE_PATH)) if SSE_CAPTURE_PATH.is_file() else {}
    instances: dict[str, list[tuple[str, int, dict[str, Any]]]] = defaultdict(list)
    stream_types: dict[str, set[str]] = defaultdict(set)
    for stream in SSE_STREAMS:
        for index, frame in enumerate(sse_frames[stream]):
            frame_type = frame.get("type")
            if not isinstance(frame_type, str):
                continue
            stream_types[stream].add(frame_type)
            instances[frame_type].append((stream, index, frame.get("data") or {}))

    event_types = sorted(instances)
    for event_type in event_types:
        data_schemas = [
            infer_schema(data, _sse_data_hint_factory(hints, stream, index))
            for stream, index, data in instances[event_type]
        ]
        merged = merge_schemas(data_schemas)
        merged["x-schema-source"] = "contracts/sse-frames/reference/*.json"
        schemas[f"Sse{pascal(event_type)}Data"] = merged

        observed_streams = sorted({stream for stream, _, _ in instances[event_type]})
        messages[f"Sse{pascal(event_type)}Event"] = {
            "name": event_type,
            "title": event_type,
            "summary": f"SSE `{event_type}` frame observed in streams: {', '.join(observed_streams)}.",
            "contentType": "application/json",
            "payload": {
                "allOf": [
                    {"$ref": "#/components/schemas/SseFrame"},
                    {
                        "type": "object",
                        "required": ["type", "data"],
                        "properties": {
                            "type": {"const": event_type},
                            "data": {"$ref": f"#/components/schemas/Sse{pascal(event_type)}Data"},
                        },
                    },
                ]
            },
            "x-event-type": event_type,
            "x-observed-streams": observed_streams,
            "x-observed-count": len(instances[event_type]),
        }

    channel_specs = [
        {
            "key": "sessionEventsLive",
            "stream": "live",
            "address": "/api/sessions/{session_id}/events",
            "route": "GET /api/sessions/{session_id}/events",
            "title": "Session live event stream",
            "description": (
                "Live SSE stream for one session (`GET /api/sessions/{session_id}/events`). "
                "Frame names and payloads are frozen in contracts/sse-frames/reference/live.json."
            ),
        },
        {
            "key": "sessionEventsHistory",
            "stream": "history",
            "address": "/api/sessions/{session_id}/events/history",
            "route": "GET /api/sessions/{session_id}/events/history",
            "title": "Session history event log",
            "description": (
                "Complete history log (`GET /api/sessions/{session_id}/events/history`). "
                "Frozen in contracts/sse-frames/reference/history.json."
            ),
        },
        {
            "key": "sessionEventsReplay",
            "stream": "replay",
            "address": "/api/sessions/{session_id}/events",
            "route": "GET /api/sessions/{session_id}/events",
            "title": "Session replay after reconnect",
            "description": (
                "Reconnect replay on the live endpoint. The client resumes with the last "
                "seen sequence id; the replayed frame set is frozen in "
                "contracts/sse-frames/reference/replay.json. This is a resume behavior, "
                "not a separate HTTP route."
            ),
            "resume": "Last-Event-ID",
        },
        {
            "key": "sessionEventsStop",
            "stream": "stop",
            "address": "/api/sessions/{session_id}/events",
            "route": "GET /api/sessions/{session_id}/events",
            "title": "Session shutdown tail",
            "description": (
                "Shutdown tail of the live stream observed around DELETE "
                "/api/sessions/{session_id}; frozen in "
                "contracts/sse-frames/reference/stop.json. Not a separate route."
            ),
        },
    ]

    route_keys = {(route["method"].upper(), route["path"]) for route in routes}
    channels: dict[str, Any] = {
        "channelWebsocket": {
            "address": "/channel/websocket/{name}",
            "title": "Channel WebSocket transport (design-only)",
            "description": (
                "Designed WebSocket transport for channels.<name>. The path is derived "
                "from the channel key, authentication is a channel-specific bearer token, "
                "and exactly one connection per channel is allowed. This route does not "
                "exist in the production route table yet; it is design-only."
            ),
            "servers": [{"$ref": "#/servers/channelWebsocketServer"}],
            "parameters": {
                "name": {
                    "description": "Channel key; equals channels.<name> and hello.params.name.",
                    "location": "$message.payload#/params/name",
                }
            },
            "bindings": {"ws": {"bindingVersion": "0.1.0"}},
            "x-contract-status": "design-only",
            "x-production": False,
            "x-design-doc": WS_DESIGN_PATH.relative_to(REPO_ROOT).as_posix(),
            "x-subprotocol": "hive-channel/1",
            "x-auth": "channel-specific bearer token",
            "x-single-connection-per-channel": True,
        }
    }

    for spec in channel_specs:
        method, route_path = spec["route"].split(" ", 1)
        if (method, route_path) not in route_keys:
            raise ContractError(f"SSE channel route is not in api-routes.json: {spec['route']}")
        channel = {
            "address": spec["address"],
            "title": spec["title"],
            "description": spec["description"],
            "servers": [{"$ref": "#/servers/sseServer"}],
            "parameters": {
                "session_id": {
                    "description": "Session path parameter; names match the Go target route.",
                }
            },
            "bindings": {"http": {"method": "GET", "bindingVersion": "0.3.0"}},
            "x-contract-status": "implemented",
            "x-transport": "sse",
            "x-stream": spec["stream"],
            "x-route": spec["route"],
            "x-source-artifact": (SSE_REFERENCE_DIR / f"{spec['stream']}.json")
            .relative_to(REPO_ROOT)
            .as_posix(),
            "x-frame-count": len(sse_frames[spec["stream"]]),
        }
        if spec.get("resume"):
            channel["x-resume-header"] = spec["resume"]
        if spec["stream"] == "stop":
            channel["x-note"] = "Captured shutdown tail; not a distinct route."
        channels[spec["key"]] = channel
        channel_operations[f"receiveSessionEvents{pascal(spec['stream'])}"] = {
            "action": "receive",
            "channel": {"$ref": f"#/channels/{spec['key']}"},
            "title": f"receive {spec['stream']} stream frames",
            "summary": f"Frames observed in the {spec['stream']} SSE stream.",
            "messages": [
                {"$ref": f"#/components/messages/Sse{pascal(event_type)}Event"}
                for event_type in sorted(stream_types[spec["stream"]])
            ],
        }

    close_codes = [
        {"code": item["code"], "meaning": item["meaning"]}
        for item in design.get("protocol_contract", {}).get("close_codes", [])
    ]

    ws_methods = sorted(
        method["name"]
        for method in protocol.get("methods", [])
        if method.get("direction") == "plugin_to_host" and method.get("kind") == "request"
    )
    sources = [
        source_entry(CHANNEL_PROTOCOL_PATH),
        source_entry(WS_DESIGN_PATH),
        source_entry(SSE_INDEX_PATH),
        {
            "path": SSE_REFERENCE_DIR.relative_to(REPO_ROOT).as_posix(),
            "kind": "sse-frame-dir",
            "files": len(SSE_STREAMS),
            "sha256": tree_sha256(sorted(SSE_REFERENCE_DIR.glob("*.json"))),
        },
    ]
    if SSE_CAPTURE_PATH.is_file():
        sources.append(source_entry(SSE_CAPTURE_PATH))

    coverage = {
        "channels": len(channels),
        "operations": len(channel_operations),
        "messages": len(messages),
        "schemas": len(schemas),
        "websocket_methods": len(ws_methods),
        "websocket_operations": len([name for name in channel_operations if not name.startswith("receiveSessionEvents")]),
        "websocket_status": "design-only",
        "sse_channels": len(channel_specs),
        "sse_event_types": len(event_types),
        "sse_frames": {stream: len(sse_frames[stream]) for stream in SSE_STREAMS},
    }

    return {
        "asyncapi": ASYNCAPI_VERSION,
        "id": "urn:hive:realtime-contract",
        "info": {
            "title": "Hive realtime surfaces",
            "version": str(routes_manifest.get("go", {}).get("version", "0.0.0")).split()[-1],
            "description": (
                "AsyncAPI 3 contract for Hive's realtime surfaces. The SSE channels are "
                "backed by frozen frame goldens in contracts/sse-frames. The channel "
                "WebSocket transport is design-only and is not a production route; the "
                "existing stdio channel transport remains unchanged."
            ),
            "x-perspective": (
                "Operations are described from the channel plugin's perspective: action "
                "send means plugin -> host, action receive means host -> plugin."
            ),
        },
        "defaultContentType": "application/json",
        "servers": {
            "hiveServe": {
                "host": "{host}",
                "protocol": "http",
                "description": "hive serve listener; host and port are deployment-configured.",
                "variables": {"host": {"default": "localhost", "description": "Bind host."}},
            },
            "channelWebsocketServer": {
                "host": "{host}",
                "protocol": "ws",
                "description": "Designed channel WebSocket endpoint; not implemented in the target yet.",
                "variables": {"host": {"default": "localhost", "description": "Bind host."}},
                "security": [{"bearerAuth": []}],
                "x-contract-status": "design-only",
                "x-production": False,
            },
            "sseServer": {
                "host": "{host}",
                "protocol": "sse",
                "description": "hive serve SSE endpoints (text/event-stream).",
                "variables": {"host": {"default": "localhost", "description": "Bind host."}},
            },
        },
        "channels": channels,
        "operations": channel_operations,
        "components": {
            "schemas": {name: schemas[name] for name in sorted(schemas)},
            "messages": {name: messages[name] for name in sorted(messages)},
            "securitySchemes": {
                "bearerAuth": {
                    "type": "http",
                    "scheme": "bearer",
                    "description": "Channel-specific bearer token (design-only).",
                }
            },
        },
        "x-contract-status": "partial",
        "x-contract": {
            "schema": ASYNCAPI_CONTRACT_SCHEMA,
            "status": "partial",
            "generator": "go_hive/scripts/gen_openapi_contract.py",
            "checker": "go_hive/scripts/check_openapi_contract.py",
            "canonical": True,
            "sources": sources,
            "coverage": coverage,
            "websocket": {
                "status": "design-only",
                "production": False,
                "route": "/channel/websocket/{name}",
                "subprotocol": "hive-channel/1",
                "auth": "channel-specific bearer token",
                "close_codes": close_codes,
                "plugin_methods": ws_methods,
                "design_doc": WS_DESIGN_PATH.relative_to(REPO_ROOT).as_posix(),
            },
            "sse": {
                "status": "implemented",
                "streams": list(SSE_STREAMS),
                "frame_golden": SSE_REFERENCE_DIR.relative_to(REPO_ROOT).as_posix(),
                "not_frozen": [
                    "GET /api/events/global",
                    "GET /api/sessions/live",
                    "GET /api/sessions/{session_id}/workers/stream",
                    "GET /api/browser/status/stream",
                ],
            },
            "notes": [
                "The WebSocket address is design-only and must not be described as a production route.",
                "SSE replay is a resume behavior on the live route, not a separate route.",
                "The SSE stop stream is the shutdown tail captured around DELETE, not a route.",
            ],
        },
    }


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def build_documents() -> dict[Path, str]:
    return {
        OPENAPI_PATH: render_json(build_openapi()),
        ASYNCAPI_PATH: render_json(build_asyncapi()),
    }


def write_documents() -> int:
    documents = build_documents()
    for path, text in documents.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        print(f"[openapi] wrote {path.relative_to(REPO_ROOT)} ({len(text)} bytes)")
    openapi = json.loads(documents[OPENAPI_PATH])
    coverage = openapi["x-contract"]["coverage"]
    print(
        "[openapi] routes={target_routes} operations={operations} paths={paths} "
        "snapshots={snapshot_endpoints}/{snapshot_endpoints_mapped} "
        "complete={operations_complete} partial={operations_partial} "
        "skipped={operations_skipped} schemas={schemas}".format(**coverage)
    )
    asyncapi = json.loads(documents[ASYNCAPI_PATH])
    async_coverage = asyncapi["x-contract"]["coverage"]
    print(
        "[asyncapi] channels={channels} operations={operations} messages={messages} "
        "schemas={schemas} sse_events={sse_event_types} ws={websocket_status}".format(**async_coverage)
    )
    return 0


def check_documents() -> int:
    documents = build_documents()
    drift = False
    for path, expected in documents.items():
        if not path.is_file():
            print(f"[openapi] MISSING {path.relative_to(REPO_ROOT)}: run with --write", file=sys.stderr)
            drift = True
            continue
        actual = path.read_text(encoding="utf-8")
        if actual != expected:
            print(
                f"[openapi] DRIFT {path.relative_to(REPO_ROOT)}: re-run gen_openapi_contract.py --write",
                file=sys.stderr,
            )
            drift = True
    if not drift:
        print("[openapi] generated contracts are current", file=sys.stderr)
    return 1 if drift else 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--write", action="store_true", help="write openapi.json and asyncapi.json (default)")
    mode.add_argument("--check", action="store_true", help="exit 1 when generated files differ")
    args = parser.parse_args(argv)
    try:
        if args.check:
            return check_documents()
        return write_documents()
    except ContractError as exc:
        print(f"[openapi] error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
