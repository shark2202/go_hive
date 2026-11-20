#!/usr/bin/env python3
"""Validate Hive MCP registry files against the local JSON Schemas.

Usage:
    python3 validate_mcp_registry.py index     path/to/registry_index.json
    python3 validate_mcp_registry.py config    path/to/config.json
    python3 validate_mcp_registry.py installed path/to/installed.json

Requires the `jsonschema` package (the tests/CI image provides it; it is not a
runtime dependency of hive). Exit code is 0 only when every file validates.
"""
from __future__ import annotations

import json
import pathlib
import sys

KINDS = ("index", "config", "installed")
HERE = pathlib.Path(__file__).resolve().parent
SCHEMA_DIR = HERE.parent / "docs" / "mcp-registry"


def load(path: pathlib.Path):
    return json.loads(path.read_text())


def registry_for():
    from referencing import Registry, Resource

    resources = {}
    for name in ("index.schema.json", "config.schema.json", "installed.schema.json"):
        resources[name] = Resource.from_contents(load(SCHEMA_DIR / name))
    return Registry().with_resources(resources.items())


def main(argv: list[str]) -> int:
    if len(argv) < 3:
        print(__doc__.strip(), file=sys.stderr)
        return 2
    kind, target = argv[1], pathlib.Path(argv[2])
    if kind not in KINDS:
        print(f"unknown kind {kind!r}; use one of {KINDS}", file=sys.stderr)
        return 2
    try:
        from jsonschema import Draft202012Validator
    except ImportError:
        print("jsonschema is required: pip install jsonschema", file=sys.stderr)
        return 2

    schema = load(SCHEMA_DIR / f"{kind}.schema.json")
    data = load(target)
    validator = Draft202012Validator(schema, registry=registry_for())
    errors = sorted(validator.iter_errors(data), key=lambda e: list(e.path))
    if not errors:
        print(f"OK {kind}: {target}")
        return 0
    print(f"FAIL {kind}: {target} ({len(errors)} error(s))", file=sys.stderr)
    for err in errors[:20]:
        where = "/".join(str(p) for p in err.path) or "<root>"
        print(f"  {where}: {err.message}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
