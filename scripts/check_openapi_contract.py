#!/usr/bin/env python3
"""Validate the generated OpenAPI 3.1 and AsyncAPI 3 contracts.

Checks performed:

* JSON shape and version fields for both documents;
* every local ``$ref`` resolves (no external refs);
* every route/method in ``contracts/api-routes.json`` appears exactly once,
  with no extra operations;
* every one of the 107 frozen response snapshots maps to a route and its
  status/endpoint is wired into that operation;
* operation ids, tags, path parameters and response objects are well formed;
* AsyncAPI channels/operations/messages resolve, the WebSocket surface stays
  explicitly design-only (and is not claimed as a production route), and SSE
  channels point at real GET routes with the frozen event sets;
* source digests in ``x-contract.sources`` still match the artifacts on disk;
* the committed documents are byte-identical to a fresh generator run.

Exit code is non-zero on any drift or validation error.

Usage:
    uv run python go_hive/scripts/check_openapi_contract.py
    uv run python go_hive/scripts/check_openapi_contract.py --json
    uv run python go_hive/scripts/check_openapi_contract.py --no-drift
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path
from typing import Any

SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parents[1]
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

import gen_openapi_contract as gen  # noqa: E402

HTTP_METHODS = {"get", "put", "post", "delete", "options", "head", "patch", "trace"}
PATH_ITEM_KEYS = HTTP_METHODS | {"$ref", "summary", "description", "servers", "parameters"}
ALLOWED_OPERATION_STATUS = {"complete", "partial", "skipped"}
SSE_CHANNEL_KEYS = {
    "sessionEventsLive": "live",
    "sessionEventsHistory": "history",
    "sessionEventsReplay": "replay",
    "sessionEventsStop": "stop",
}


# ---------------------------------------------------------------------------
# Generic helpers
# ---------------------------------------------------------------------------


def resolve_pointer(document: Any, ref: str) -> tuple[bool, Any]:
    if not ref.startswith("#/"):
        return False, None
    node = document
    for raw in ref[2:].split("/"):
        part = raw.replace("~1", "/").replace("~0", "~")
        if isinstance(node, dict):
            if part not in node:
                return False, None
            node = node[part]
        elif isinstance(node, list):
            try:
                node = node[int(part)]
            except (ValueError, IndexError):
                return False, None
        else:
            return False, None
    return True, node


def walk_refs(document: Any, node: Any, where: str, errors: list[str]) -> None:
    if isinstance(node, dict):
        ref = node.get("$ref")
        if ref is not None:
            if not isinstance(ref, str):
                errors.append(f"{where}: $ref is not a string")
            elif not ref.startswith("#/"):
                errors.append(f"{where}: non-local $ref {ref!r}")
            else:
                found, _ = resolve_pointer(document, ref)
                if not found:
                    errors.append(f"{where}: unresolved $ref {ref!r}")
        for key, value in node.items():
            if key == "$ref":
                continue
            walk_refs(document, value, f"{where}.{key}", errors)
    elif isinstance(node, list):
        for index, value in enumerate(node):
            walk_refs(document, value, f"{where}[{index}]", errors)


def _schema_errors(schema: Any, where: str, errors: list[str]) -> None:
    if not isinstance(schema, dict):
        errors.append(f"{where}: schema is not an object")
        return
    declared = schema.get("type")
    if declared is not None and not isinstance(declared, (str, list)):
        errors.append(f"{where}: schema type must be a string or array")
    if isinstance(declared, list) and not all(isinstance(item, str) for item in declared):
        errors.append(f"{where}: schema type array must contain strings")
    required = schema.get("required")
    if required is not None:
        if not isinstance(required, list) or not all(isinstance(item, str) for item in required):
            errors.append(f"{where}: required must be an array of strings")
        else:
            properties = schema.get("properties")
            if isinstance(properties, dict):
                missing = [name for name in required if name not in properties]
                if missing:
                    errors.append(f"{where}: required keys not in properties: {missing}")
            if len(set(required)) != len(required):
                errors.append(f"{where}: duplicate required keys")


def _check_sources(document: dict[str, Any], where: str, errors: list[str]) -> None:
    contract = document.get("x-contract")
    if not isinstance(contract, dict):
        errors.append(f"{where}: missing x-contract metadata")
        return
    sources = contract.get("sources")
    if not isinstance(sources, list) or not sources:
        errors.append(f"{where}.x-contract.sources is missing or empty")
        return
    for entry in sources:
        if not isinstance(entry, dict) or not isinstance(entry.get("path"), str):
            errors.append(f"{where}.x-contract.sources has a malformed entry")
            continue
        path = REPO_ROOT / entry["path"]
        if not path.exists():
            errors.append(f"{where}.x-contract.sources: missing artifact {entry['path']}")
            continue
        expected = entry.get("sha256")
        if not isinstance(expected, str):
            errors.append(f"{where}.x-contract.sources: {entry['path']} has no sha256")
            continue
        if entry.get("kind", "file") == "file":
            actual = hashlib.sha256(path.read_bytes()).hexdigest()
        elif path.is_dir():
            actual = gen.tree_sha256(sorted(path.glob("*.json")))
        else:
            actual = hashlib.sha256(path.read_bytes()).hexdigest()
        if actual != expected:
            errors.append(f"{where}.x-contract.sources: digest drift for {entry['path']}")


# ---------------------------------------------------------------------------
# OpenAPI validation
# ---------------------------------------------------------------------------


def _route_manifest() -> tuple[dict[str, Any], list[dict[str, Any]], list[dict[str, Any]]]:
    manifest = gen.load_json(gen.API_ROUTES_PATH)
    routes = manifest.get("go", {}).get("routes", [])
    index, snapshots = gen.load_snapshots()
    return manifest, routes, snapshots


def validate_openapi(
    document: dict[str, Any],
    routes: list[dict[str, Any]],
    snapshots: list[dict[str, Any]],
    errors: list[str],
) -> dict[str, Any]:
    where = "openapi"
    if document.get("openapi") != gen.OPENAPI_VERSION:
        errors.append(f"{where}: openapi must be {gen.OPENAPI_VERSION}")
    info = document.get("info")
    if not isinstance(info, dict) or not info.get("title") or not info.get("version"):
        errors.append(f"{where}: info.title and info.version are required")
    paths = document.get("paths")
    if not isinstance(paths, dict) or not paths:
        errors.append(f"{where}: paths must be a non-empty object")
        return {}

    operation_ids: dict[str, str] = {}
    operation_keys: set[tuple[str, str]] = set()
    status_counts = {"complete": 0, "partial": 0, "skipped": 0}
    response_count = 0
    operations_with_snapshot = 0

    for path, path_item in paths.items():
        if not isinstance(path, str) or not path.startswith("/"):
            errors.append(f"{where}: invalid path key {path!r}")
            continue
        if not isinstance(path_item, dict):
            errors.append(f"{where}.paths.{path}: path item is not an object")
            continue
        unexpected = set(path_item) - PATH_ITEM_KEYS
        if unexpected:
            errors.append(f"{where}.paths.{path}: unexpected path item keys {sorted(unexpected)}")
        template_parameters = gen.path_parameters(path)
        for method, operation in path_item.items():
            if method not in HTTP_METHODS:
                continue
            op_where = f"{where}.paths.{path}.{method}"
            if not isinstance(operation, dict):
                errors.append(f"{op_where}: operation is not an object")
                continue
            operation_keys.add((method.upper(), path))

            operation_id = operation.get("operationId")
            if not isinstance(operation_id, str) or not operation_id:
                errors.append(f"{op_where}: missing operationId")
            elif operation_id in operation_ids:
                errors.append(
                    f"{op_where}: duplicate operationId {operation_id!r} "
                    f"(already used by {operation_ids[operation_id]})"
                )
            else:
                operation_ids[operation_id] = f"{method.upper()} {path}"

            tags = operation.get("tags")
            if not isinstance(tags, list) or not tags or not all(isinstance(tag, str) and tag for tag in tags):
                errors.append(f"{op_where}: tags must be a non-empty array of strings")

            status = operation.get("x-contract-status")
            if status not in ALLOWED_OPERATION_STATUS:
                errors.append(f"{op_where}: invalid x-contract-status {status!r}")
            else:
                status_counts[status] += 1

            parameters = operation.get("parameters", [])
            declared_path_params: list[str] = []
            if not isinstance(parameters, list):
                errors.append(f"{op_where}: parameters must be an array")
                parameters = []
            for parameter in parameters:
                if not isinstance(parameter, dict):
                    errors.append(f"{op_where}: parameter is not an object")
                    continue
                name = parameter.get("name")
                location = parameter.get("in")
                if location == "path":
                    if not isinstance(name, str):
                        errors.append(f"{op_where}: path parameter without a name")
                        continue
                    declared_path_params.append(name)
                    if parameter.get("required") is not True:
                        errors.append(f"{op_where}: path parameter {name!r} must be required")
            if sorted(declared_path_params) != sorted(template_parameters):
                errors.append(
                    f"{op_where}: path parameters {declared_path_params} do not match template {template_parameters}"
                )

            responses = operation.get("responses")
            if not isinstance(responses, dict) or not responses:
                errors.append(f"{op_where}: responses must be a non-empty object")
                continue
            for status_key, response in responses.items():
                if status_key != "default" and not (len(status_key) == 3 and status_key.isdigit()):
                    errors.append(f"{op_where}: invalid response status key {status_key!r}")
                if not isinstance(response, dict):
                    errors.append(f"{op_where}.responses.{status_key}: not an object")
                    continue
                if not response.get("description"):
                    errors.append(f"{op_where}.responses.{status_key}: description is required")
                content = response.get("content")
                if content is not None:
                    if not isinstance(content, dict) or not content:
                        errors.append(f"{op_where}.responses.{status_key}: content must be a non-empty object")
                    else:
                        for media_type, media in content.items():
                            if "/" not in media_type:
                                errors.append(
                                    f"{op_where}.responses.{status_key}: invalid media type {media_type!r}"
                                )
                            if not isinstance(media, dict) or "schema" not in media:
                                errors.append(
                                    f"{op_where}.responses.{status_key}.{media_type}: schema is required"
                                )
                            elif isinstance(media.get("schema"), dict):
                                _schema_errors(
                                    media["schema"],
                                    f"{op_where}.responses.{status_key}.{media_type}.schema",
                                    errors,
                                )
                    response_count += 1

            snapshots_for_route = []
            for snapshot in snapshots:
                matched = gen.match_route(routes, snapshot["method"], snapshot["path"])
                if (
                    matched is not None
                    and matched["path"] == path
                    and matched["method"].upper() == method.upper()
                ):
                    snapshots_for_route.append(snapshot)
            if snapshots_for_route:
                operations_with_snapshot += 1

    expected_keys = {(route["method"].upper(), route["path"]) for route in routes}
    missing = sorted(expected_keys - operation_keys)
    extra = sorted(operation_keys - expected_keys)
    for method, path in missing:
        errors.append(f"{where}: missing operation {method} {path}")
    for method, path in extra:
        errors.append(f"{where}: unexpected operation {method} {path} (not in api-routes.json)")

    # Every frozen snapshot must be wired into the matching operation.
    mapped = 0
    for snapshot in snapshots:
        route = gen.match_route(routes, snapshot["method"], snapshot["path"])
        if route is None:
            errors.append(
                f"{where}: snapshot {snapshot['endpoint']} has no target route "
                f"({snapshot['method']} {snapshot['path']})"
            )
            continue
        operation = paths.get(route["path"], {}).get(route["method"].lower())
        if not isinstance(operation, dict):
            errors.append(f"{where}: snapshot {snapshot['endpoint']} maps to a missing operation")
            continue
        status = str(snapshot["payload"].get("status", snapshot["reference_status"]))
        response = operation.get("responses", {}).get(status)
        if not isinstance(response, dict):
            errors.append(
                f"{where}: snapshot {snapshot['endpoint']} status {status} "
                f"missing from {route['method']} {route['path']}"
            )
            continue
        if snapshot["endpoint"] not in response.get("x-snapshot-endpoints", []):
            errors.append(
                f"{where}: snapshot {snapshot['endpoint']} not recorded on "
                f"{route['method']} {route['path']} response {status}"
            )
            continue
        mapped += 1

    # Schema components.
    schemas = document.get("components", {}).get("schemas", {})
    if not isinstance(schemas, dict) or not schemas:
        errors.append(f"{where}: components.schemas must be a non-empty object")
        schemas = {}
    for name, schema in schemas.items():
        _schema_errors(schema, f"{where}.components.schemas.{name}", errors)
        if isinstance(schema, dict) and "ErrorResponse" == name and schema.get("required") != ["error"]:
            errors.append(f"{where}: ErrorResponse must require the error field")

    contract = document.get("x-contract", {})
    coverage = contract.get("coverage", {}) if isinstance(contract, dict) else {}
    expected_counts = {
        "target_routes": len(routes),
        "operations": len(operation_keys),
        "paths": len(paths),
        "snapshot_endpoints": len(snapshots),
        "snapshot_endpoints_mapped": mapped,
        "schemas": len(schemas),
        "operations_complete": status_counts["complete"],
        "operations_partial": status_counts["partial"],
        "operations_skipped": status_counts["skipped"],
    }
    for key, value in expected_counts.items():
        if coverage.get(key) != value:
            errors.append(f"{where}.x-contract.coverage.{key}: {coverage.get(key)!r} != {value}")
    if mapped != len(snapshots):
        errors.append(f"{where}: mapped {mapped}/{len(snapshots)} snapshot endpoints")

    walk_refs(document, document, where, errors)
    _check_sources(document, where, errors)

    return {
        "operations": len(operation_keys),
        "paths": len(paths),
        "snapshots_mapped": mapped,
        "snapshots_total": len(snapshots),
        "schemas": len(schemas),
        "responses": response_count,
        "operations_with_snapshot": operations_with_snapshot,
        "status": dict(status_counts),
    }


# ---------------------------------------------------------------------------
# AsyncAPI validation
# ---------------------------------------------------------------------------


def _message_refs(operation: dict[str, Any]) -> list[str]:
    messages = operation.get("messages", [])
    if not isinstance(messages, list):
        return []
    refs: list[str] = []
    for entry in messages:
        if isinstance(entry, dict) and isinstance(entry.get("$ref"), str):
            refs.append(entry["$ref"])
    return refs


def validate_asyncapi(
    document: dict[str, Any],
    routes: list[dict[str, Any]],
    frames: dict[str, list[dict[str, Any]]],
    errors: list[str],
) -> dict[str, Any]:
    where = "asyncapi"
    if document.get("asyncapi") != gen.ASYNCAPI_VERSION:
        errors.append(f"{where}: asyncapi must be {gen.ASYNCAPI_VERSION}")
    info = document.get("info")
    if not isinstance(info, dict) or not info.get("title") or not info.get("version"):
        errors.append(f"{where}: info.title and info.version are required")

    channels = document.get("channels")
    operations = document.get("operations")
    components = document.get("components", {})
    messages = components.get("messages", {}) if isinstance(components, dict) else {}
    schemas = components.get("schemas", {}) if isinstance(components, dict) else {}
    if not isinstance(channels, dict) or not channels:
        errors.append(f"{where}: channels must be a non-empty object")
        channels = {}
    if not isinstance(operations, dict) or not operations:
        errors.append(f"{where}: operations must be a non-empty object")
        operations = {}
    if not isinstance(messages, dict) or not messages:
        errors.append(f"{where}: components.messages must be a non-empty object")
        messages = {}
    if not isinstance(schemas, dict) or not schemas:
        errors.append(f"{where}: components.schemas must be a non-empty object")
        schemas = {}

    route_keys = {(route["method"].upper(), route["path"]) for route in routes}
    production_ws_routes = {path for method, path in route_keys if method == "GET"}

    ws_channel = channels.get("channelWebsocket")
    if not isinstance(ws_channel, dict):
        errors.append(f"{where}: design-only channelWebsocket channel is missing")
    else:
        ws_where = f"{where}.channels.channelWebsocket"
        if ws_channel.get("x-contract-status") != "design-only":
            errors.append(f"{ws_where}: x-contract-status must be design-only")
        if ws_channel.get("x-production") is not False:
            errors.append(f"{ws_where}: x-production must be false")
        if ws_channel.get("address") in production_ws_routes:
            errors.append(f"{ws_where}: design-only address {ws_channel.get('address')!r} is a production route")
        if not ws_channel.get("x-design-doc"):
            errors.append(f"{ws_where}: x-design-doc is required")

    sse_seen: set[str] = set()
    for channel_key, stream in SSE_CHANNEL_KEYS.items():
        channel = channels.get(channel_key)
        if not isinstance(channel, dict):
            errors.append(f"{where}: missing SSE channel {channel_key}")
            continue
        sse_seen.add(stream)
        channel_where = f"{where}.channels.{channel_key}"
        if channel.get("x-stream") != stream:
            errors.append(f"{channel_where}: x-stream must be {stream!r}")
        if channel.get("x-contract-status") != "implemented":
            errors.append(f"{channel_where}: SSE channel must be x-contract-status implemented")
        route_ref = channel.get("x-route")
        if not isinstance(route_ref, str) or " " not in route_ref:
            errors.append(f"{channel_where}: x-route must be 'METHOD /path'")
        else:
            route_method, route_path = route_ref.split(" ", 1)
            if (route_method.upper(), route_path) not in route_keys:
                errors.append(f"{channel_where}: x-route {route_ref!r} is not in api-routes.json")
            if channel.get("address") != route_path:
                errors.append(f"{channel_where}: address does not match x-route path")
        bound_frames = frames.get(stream, [])
        if channel.get("x-frame-count") != len(bound_frames):
            errors.append(f"{channel_where}: x-frame-count does not match the frozen frames")
        operation_key = f"receiveSessionEvents{gen.pascal(stream)}"
        if operation_key not in operations:
            errors.append(f"{channel_where}: missing operation {operation_key}")

    if sse_seen != set(gen.SSE_STREAMS):
        errors.append(f"{where}: SSE streams covered {sorted(sse_seen)} != {sorted(gen.SSE_STREAMS)}")

    referenced_messages: set[str] = set()
    for operation_key, operation in operations.items():
        op_where = f"{where}.operations.{operation_key}"
        if not isinstance(operation, dict):
            errors.append(f"{op_where}: operation is not an object")
            continue
        if operation.get("action") not in {"send", "receive"}:
            errors.append(f"{op_where}: action must be send or receive")
        channel_ref = operation.get("channel", {}).get("$ref") if isinstance(operation.get("channel"), dict) else None
        if not isinstance(channel_ref, str) or not channel_ref.startswith("#/channels/"):
            errors.append(f"{op_where}: channel must reference #/channels/...")
        elif channel_ref[len("#/channels/") :] not in channels:
            errors.append(f"{op_where}: channel ref {channel_ref!r} does not resolve")
        refs = _message_refs(operation)
        if not refs:
            errors.append(f"{op_where}: messages must reference at least one message")
        for ref in refs:
            name = ref[len("#/components/messages/") :] if ref.startswith("#/components/messages/") else None
            if name is None:
                errors.append(f"{op_where}: unexpected message ref {ref!r}")
            elif name not in messages:
                errors.append(f"{op_where}: message ref {ref!r} does not resolve")
            else:
                referenced_messages.add(name)

    for message_key, message in messages.items():
        msg_where = f"{where}.components.messages.{message_key}"
        if not isinstance(message, dict):
            errors.append(f"{msg_where}: message is not an object")
            continue
        if "payload" not in message:
            errors.append(f"{msg_where}: payload is required")
        event_type = message.get("x-event-type")
        if event_type is not None:
            data_ref = None
            payload = message.get("payload")
            if isinstance(payload, dict):
                for entry in payload.get("allOf", []):
                    if isinstance(entry, dict) and isinstance(entry.get("properties"), dict):
                        data = entry["properties"].get("data")
                        if isinstance(data, dict):
                            data_ref = data.get("$ref")
            if not isinstance(data_ref, str):
                errors.append(f"{msg_where}: event message has no data $ref")
        if message_key not in referenced_messages:
            errors.append(f"{msg_where}: message is not referenced by any operation")

    # SSE event sets must match the frozen frames exactly.
    observed_by_stream = {
        stream: sorted({frame.get("type") for frame in frames.get(stream, []) if frame.get("type")})
        for stream in gen.SSE_STREAMS
    }
    for channel_key, stream in SSE_CHANNEL_KEYS.items():
        operation_key = f"receiveSessionEvents{gen.pascal(stream)}"
        operation = operations.get(operation_key)
        if not isinstance(operation, dict):
            continue
        actual_types: list[str] = []
        for ref in _message_refs(operation):
            name = ref[len("#/components/messages/") :] if ref.startswith("#/components/messages/") else None
            message = messages.get(name) if name is not None else None
            event_type = message.get("x-event-type") if isinstance(message, dict) else None
            if isinstance(event_type, str):
                actual_types.append(event_type)
        actual = sorted(actual_types)
        if actual != observed_by_stream.get(stream, []):
            errors.append(
                f"{where}.operations.{operation_key}: event types {actual} != frozen "
                f"{observed_by_stream.get(stream, [])}"
            )

    # WebSocket status metadata and protocol-method coverage.
    contract = document.get("x-contract", {})
    ws_meta = contract.get("websocket", {}) if isinstance(contract, dict) else {}
    if ws_meta.get("status") != "design-only" or ws_meta.get("production") is not False:
        errors.append(f"{where}.x-contract.websocket: must stay design-only and production=false")
    ws_methods_in_messages = {
        message.get("x-method")
        for message in messages.values()
        if isinstance(message, dict) and message.get("x-direction") in {"host_to_plugin", "plugin_to_host"}
    }
    if ws_meta.get("plugin_methods"):
        missing_methods = sorted(set(ws_meta["plugin_methods"]) - ws_methods_in_messages)
        if missing_methods:
            errors.append(f"{where}.x-contract.websocket: methods missing from messages: {missing_methods}")

    coverage = contract.get("coverage", {}) if isinstance(contract, dict) else {}
    all_event_types = {event_type for types in observed_by_stream.values() for event_type in types}
    expected_counts = {
        "channels": len(channels),
        "operations": len(operations),
        "messages": len(messages),
        "schemas": len(schemas),
        "sse_event_types": len(all_event_types),
        "sse_channels": len(SSE_CHANNEL_KEYS),
        "websocket_status": "design-only",
    }
    for key, value in expected_counts.items():
        if coverage.get(key) != value:
            errors.append(f"{where}.x-contract.coverage.{key}: {coverage.get(key)!r} != {value}")

    walk_refs(document, document, where, errors)
    _check_sources(document, where, errors)

    return {
        "channels": len(channels),
        "operations": len(operations),
        "messages": len(messages),
        "schemas": len(schemas),
        "event_types": expected_counts["sse_event_types"],
        "websocket_status": coverage.get("websocket_status"),
    }


# ---------------------------------------------------------------------------
# Entry points
# ---------------------------------------------------------------------------


def validate_documents(
    openapi: dict[str, Any],
    asyncapi: dict[str, Any],
) -> tuple[list[str], dict[str, Any]]:
    errors: list[str] = []
    _, routes, snapshots = _route_manifest()
    _, frames = gen.load_sse_frames()
    openapi_summary = validate_openapi(openapi, routes, snapshots, errors)
    asyncapi_summary = validate_asyncapi(asyncapi, routes, frames, errors)
    return errors, {"openapi": openapi_summary, "asyncapi": asyncapi_summary}


def drift_errors() -> list[str]:
    errors: list[str] = []
    expected_documents = gen.build_documents()
    for path, expected in expected_documents.items():
        if not path.is_file():
            errors.append(f"drift: missing {path.relative_to(REPO_ROOT)}; run gen_openapi_contract.py --write")
            continue
        if path.read_text(encoding="utf-8") != expected:
            errors.append(
                f"drift: {path.relative_to(REPO_ROOT)} differs from generator output; "
                "run gen_openapi_contract.py --write"
            )
    return errors


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--json", action="store_true", help="print a machine-readable summary")
    parser.add_argument("--no-drift", action="store_true", help="skip byte-for-byte generator drift check")
    args = parser.parse_args(argv)

    errors: list[str] = []
    try:
        openapi = gen.load_json(gen.OPENAPI_PATH)
        asyncapi = gen.load_json(gen.ASYNCAPI_PATH)
    except gen.ContractError as exc:
        print(f"[contract-check] error: {exc}", file=sys.stderr)
        return 2

    validation_errors, summary = validate_documents(openapi, asyncapi)
    errors.extend(validation_errors)
    if not args.no_drift:
        errors.extend(drift_errors())

    if args.json:
        print(
            json.dumps(
                {"ok": not errors, "summary": summary, "errors": errors},
                ensure_ascii=False,
                indent=2,
                sort_keys=True,
            )
        )
    else:
        state = "ok" if not errors else "FAILED"
        print(
            f"[contract-check] {state}: openapi operations={summary['openapi'].get('operations')} "
            f"paths={summary['openapi'].get('paths')} "
            f"snapshots={summary['openapi'].get('snapshots_mapped')}/"
            f"{summary['openapi'].get('snapshots_total')} "
            f"schemas={summary['openapi'].get('schemas')}; "
            f"asyncapi channels={summary['asyncapi'].get('channels')} "
            f"operations={summary['asyncapi'].get('operations')} "
            f"messages={summary['asyncapi'].get('messages')} "
            f"events={summary['asyncapi'].get('event_types')} "
            f"ws={summary['asyncapi'].get('websocket_status')}"
        )
        for error in errors[:60]:
            print(f"[contract-check]   {error}", file=sys.stderr)
        if len(errors) > 60:
            print(f"[contract-check]   ... {len(errors) - 60} more", file=sys.stderr)
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
