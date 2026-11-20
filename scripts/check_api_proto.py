#!/usr/bin/env python3
"""Validate the frozen API shapes against the hand-written proto3 spec.

No protobuf runtime: ``api_contract_lib.ProtoSpec`` parses
``go_hive/contracts/api.proto`` and this script walks every field path in
``go_hive/contracts/api-responses/shapes/{reference,target}/*.shape.json``,
resolving it through the message graph.

Checks:
  * the proto parses (message/enum/field syntax, unique field numbers,
    known scalar/message types, enum values unique);
  * every reference (contract) shape field resolves to a declared field;
  * every target shape field resolves too, so Go extensions are documented
    rather than silently appearing in the diff report;
  * JSON kind matches the declared proto type (string/bool/number/array/
    object), allowing the documented Timestamp/Struct/map cases.

Writes ``go_hive/contracts/api-proto-check.json`` and exits 1 on errors.
Warnings (unreachable messages, target-only extensions) do not fail.

Usage:
    uv run python go_hive/scripts/check_api_proto.py
    uv run python go_hive/scripts/check_api_proto.py --json-only
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))
import api_contract_lib as lib  # noqa: E402

REPO_ROOT = Path(__file__).resolve().parents[2]
PROTO_PATH = REPO_ROOT / "go_hive" / "contracts" / "api.proto"
RESPONSES_DIR = REPO_ROOT / "go_hive" / "contracts" / "api-responses"
OUT_PATH = REPO_ROOT / "go_hive" / "contracts" / "api-proto-check.json"

NUMERIC_SCALARS = {
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
}


def json_kind_for(proto_type: str) -> set[str]:
    if proto_type in {"string", "bytes"}:
        return {"string"}
    if proto_type == "bool":
        return {"boolean"}
    if proto_type in NUMERIC_SCALARS:
        return {"number(int)", "number(float)"}
    if proto_type in {"google.protobuf.Timestamp"}:
        return {"string"}
    if proto_type in {"google.protobuf.Struct", "google.protobuf.Value", "google.protobuf.Any"}:
        return {"object"}
    if proto_type == "google.protobuf.Duration":
        return {"string"}
    return {"object"}


def check_structure(spec: lib.ProtoSpec) -> list[dict[str, str]]:
    errors: list[dict[str, str]] = []

    def known(type_name: str) -> bool:
        return (
            type_name in lib.SCALAR_TYPES
            or type_name in lib.WELL_KNOWN_MESSAGES
            or type_name in spec.messages
            or type_name in spec.enums
        )

    for message, fields in spec.messages.items():
        numbers: dict[int, str] = {}
        for name, field in fields.items():
            if field.number in numbers:
                errors.append(
                    {
                        "kind": "duplicate-field-number",
                        "detail": f"{message}.{name} and {message}.{numbers[field.number]} both use {field.number}",
                    }
                )
            numbers[field.number] = name
            if field.map_key:
                for part in (field.map_key, field.map_value):
                    if not known(part):
                        errors.append({"kind": "unknown-map-type", "detail": f"{message}.{name}: {part}"})
                if field.map_key not in lib.SCALAR_TYPES:
                    errors.append({"kind": "invalid-map-key", "detail": f"{message}.{name}: {field.map_key}"})
            elif not known(field.type):
                errors.append({"kind": "unknown-field-type", "detail": f"{message}.{name}: {field.type}"})
        if not fields:
            errors.append({"kind": "empty-message", "detail": message})
    for enum_name, values in spec.enums.items():
        if len(values) != len(set(values)):
            errors.append({"kind": "duplicate-enum-value", "detail": enum_name})
    return errors


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Validate frozen API shapes against api.proto")
    parser.add_argument("--json-only", action="store_true", help="write the report and stay quiet")
    args = parser.parse_args(argv)

    spec = lib.load_proto(PROTO_PATH)
    errors = check_structure(spec)
    warnings: list[dict[str, str]] = []
    checked: list[dict[str, Any]] = []

    for side in ("reference", "target"):
        shape_dir = RESPONSES_DIR / "shapes" / side
        if not shape_dir.is_dir():
            errors.append({"kind": "missing-shapes", "detail": f"{shape_dir} does not exist; run freeze_api_responses.py"})
            continue
        for path in sorted(shape_dir.glob("*.shape.json")):
            data = json.loads(path.read_text(encoding="utf-8"))
            endpoint = data["endpoint"]
            if endpoint not in lib.ENDPOINT_MESSAGES:
                warnings.append({"kind": "unmapped-endpoint", "detail": endpoint})
                continue
            for field_path, json_type in sorted(data["shape"].items()):
                resolved = lib.resolve_field(spec, endpoint, field_path)
                entry = {"side": side, "endpoint": endpoint, "field": field_path, "json_type": json_type}
                if resolved.struct_key:
                    entry["proto"] = "google.protobuf.Struct"
                    entry["status"] = "ok"
                    checked.append(entry)
                    continue
                if resolved.field is None:
                    entry["proto"] = ""
                    entry["status"] = "undeclared"
                    entry["note"] = resolved.note
                    # A target-only field is an undocumented extension: warn.
                    if side == "target":
                        warnings.append({"kind": "undeclared-target-field", "detail": f"{endpoint}.{field_path}"})
                    else:
                        errors.append({"kind": "undeclared-reference-field", "detail": f"{endpoint}.{field_path}"})
                    checked.append(entry)
                    continue
                entry["proto"] = resolved.field.rendered_type()
                if resolved.field.map_key:
                    if resolved.note == "map key":
                        allowed = json_kind_for(resolved.field.map_value)
                    else:
                        allowed = {"object"}
                elif resolved.field.type in spec.enums:
                    allowed = {"string"}
                else:
                    allowed = json_kind_for(resolved.field.type)
                if json_type == "null":
                    # proto3 JSON: null means unset; the shape keeps it to make
                    # presence differences visible, it is never a type error.
                    entry["status"] = "ok"
                    entry["note"] = "null (unset)"
                elif field_path.endswith("[]"):
                    # Element-shape key: compare against the element type.
                    if json_type in allowed or (resolved.field.type in spec.messages and json_type == "object"):
                        entry["status"] = "ok"
                    else:
                        entry["status"] = "kind-mismatch"
                        entry["note"] = f"element of {resolved.field.type} expects {sorted(allowed)}, got {json_type}"
                        errors.append({"kind": "kind-mismatch", "detail": f"{endpoint}.{field_path} ({entry['note']})"})
                elif resolved.field.repeated:
                    if json_type != "array":
                        entry["status"] = "kind-mismatch"
                        entry["note"] = f"repeated field expects array, got {json_type}"
                        errors.append({"kind": "kind-mismatch", "detail": f"{endpoint}.{field_path} ({entry['note']})"})
                    else:
                        entry["status"] = "ok"
                elif json_type in allowed:
                    entry["status"] = "ok"
                else:
                    entry["status"] = "kind-mismatch"
                    entry["note"] = f"{resolved.field.type} expects {sorted(allowed)}, got {json_type}"
                    errors.append({"kind": "kind-mismatch", "detail": f"{endpoint}.{field_path} ({entry['note']})"})
                checked.append(entry)

    reachable = set(lib.ENDPOINT_MESSAGES.values())
    frontier = list(reachable)
    while frontier:
        name = frontier.pop()
        for field in spec.messages.get(name, {}).values():
            target = field.map_value or field.type
            if target in spec.messages and target not in reachable:
                reachable.add(target)
                frontier.append(target)
    for message in sorted(set(spec.messages) - reachable):
        warnings.append({"kind": "unreachable-message", "detail": message})

    report = {
        "schema": "hive-api-proto-check/1",
        "proto": str(PROTO_PATH.relative_to(REPO_ROOT)),
        "messages": len(spec.messages),
        "enums": len(spec.enums),
        "checked_fields": len(checked),
        "summary": {
            "errors": len(errors),
            "warnings": len(warnings),
            "undeclared_reference": sum(1 for e in errors if e["kind"] == "undeclared-reference-field"),
            "kind_mismatch": sum(1 for e in errors if e["kind"] == "kind-mismatch"),
        },
        "errors": errors,
        "warnings": warnings,
        "fields": checked,
    }
    OUT_PATH.parent.mkdir(parents=True, exist_ok=True)
    OUT_PATH.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    if not args.json_only:
        print(f"[proto] messages={len(spec.messages)} enums={len(spec.enums)} fields={len(checked)}")
        print(f"[proto] errors={len(errors)} warnings={len(warnings)} -> {OUT_PATH}")
        for error in errors[:20]:
            print(f"[proto]   ERROR {error['kind']}: {error['detail']}")
        for warning in warnings[:20]:
            print(f"[proto]   warn  {warning['kind']}: {warning['detail']}")
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
