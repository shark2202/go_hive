#!/usr/bin/env python3
"""Validate queen profile YAMLs against the frozen schema and the Python reference.

The spec lives in docs/queen-profile-spec.md and contracts/queen-profile.schema.json.
Every queen_defaults/*.yaml in the Go tree must:

  1. validate against the JSON Schema (the subset used by that file),
  2. be byte-identical to the same file in the Python reference tree,
  3. cover the same queen ids as the reference tree.

Usage:
    .venv/bin/python go_hive/scripts/check_queen_profiles.py            # report
    .venv/bin/python go_hive/scripts/check_queen_profiles.py --check    # exit 1 on drift
    .venv/bin/python go_hive/scripts/check_queen_profiles.py --json-out /tmp/q.json
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

try:
    import yaml
except ImportError:  # pragma: no cover - environment guard
    sys.stderr.write("PyYAML is required: run with .venv/bin/python or `uv run --with pyyaml`\n")
    raise SystemExit(2)

GO_REL = "go_hive/go-hive/internal/queen/queen_defaults"
REF_REL = "core/framework/agents/queen/queen_defaults"
SCHEMA_REL = "go_hive/contracts/queen-profile.schema.json"

TYPE_MAP = {
    "object": dict,
    "array": list,
    "string": str,
    "integer": int,
    "number": (int, float),
    "boolean": bool,
}


def find_repo_root(start: Path) -> Path:
    for candidate in [start, *start.parents]:
        if (candidate / GO_REL).is_dir() and (candidate / REF_REL).is_dir():
            return candidate
    raise SystemExit(f"cannot locate repo root above {start} (looked for {GO_REL} + {REF_REL})")


def validate(instance, schema: dict, path: str, errors: list[str]) -> None:
    expected = schema.get("type")
    if expected:
        py_type = TYPE_MAP[expected]
        ok = isinstance(instance, py_type) and not (expected != "boolean" and isinstance(instance, bool))
        if not ok:
            errors.append(f"{path}: expected {expected}, got {type(instance).__name__}")
            return
    if isinstance(instance, str) and "minLength" in schema and len(instance) < schema["minLength"]:
        errors.append(f"{path}: shorter than minLength {schema['minLength']}")
    if isinstance(instance, list):
        if "minItems" in schema and len(instance) < schema["minItems"]:
            errors.append(f"{path}: fewer than minItems {schema['minItems']}")
        item_schema = schema.get("items")
        if isinstance(item_schema, dict):
            for index, item in enumerate(instance):
                validate(item, item_schema, f"{path}[{index}]", errors)
    if isinstance(instance, dict):
        for key in schema.get("required", []):
            if key not in instance:
                errors.append(f"{path}: missing required key '{key}'")
        properties = schema.get("properties", {})
        if schema.get("additionalProperties") is False:
            for key in instance:
                if key not in properties:
                    errors.append(f"{path}: unknown key '{key}'")
        for key, value in instance.items():
            if key in properties:
                validate(value, properties[key], f"{path}.{key}", errors)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="exit 1 when the spec does not hold")
    parser.add_argument("--json-out", help="write the report as JSON")
    parser.add_argument("--repo-root", help="repo root (default: search upward from this script)")
    args = parser.parse_args()

    root = Path(args.repo_root).resolve() if args.repo_root else find_repo_root(Path(__file__).resolve().parent)
    go_dir = root / GO_REL
    ref_dir = root / REF_REL
    schema_path = root / SCHEMA_REL
    schema = json.loads(schema_path.read_text(encoding="utf-8"))

    go_files = {path.stem: path for path in sorted(go_dir.glob("*.yaml"))}
    ref_files = {path.stem: path for path in sorted(ref_dir.glob("*.yaml"))}

    rows = []
    failures: list[str] = []

    for queen_id in sorted(set(go_files) | set(ref_files)):
        row = {"queen_id": queen_id, "yaml": False, "bytes_equal": False, "errors": []}
        go_path = go_files.get(queen_id)
        ref_path = ref_files.get(queen_id)
        if go_path is None:
            row["errors"].append("missing in Go tree")
        if ref_path is None:
            row["errors"].append("missing in Python reference tree")
        if go_path is not None:
            try:
                data = yaml.safe_load(go_path.read_text(encoding="utf-8"))
                errors: list[str] = []
                validate(data, schema, queen_id, errors)
                row["yaml"] = not errors
                row["errors"].extend(errors)
            except yaml.YAMLError as exc:
                row["errors"].append(f"yaml parse error: {exc}")
        if go_path is not None and ref_path is not None:
            row["bytes_equal"] = go_path.read_bytes() == ref_path.read_bytes()
            if not row["bytes_equal"]:
                row["errors"].append("bytes differ from Python reference")
        if row["errors"]:
            failures.append(f"{queen_id}: " + "; ".join(row["errors"]))
        rows.append(row)

    report = {
        "repo_root": str(root),
        "schema": SCHEMA_REL,
        "count": {"go": len(go_files), "reference": len(ref_files)},
        "rows": rows,
        "failures": failures,
        "ok": not failures,
    }

    width = max((len(row["queen_id"]) for row in rows), default=10)
    print(f"{'queen_id'.ljust(width)}  yaml  bytes  errors")
    for row in rows:
        print(
            f"{row['queen_id'].ljust(width)}  "
            f"{'ok' if row['yaml'] else 'FAIL':4}  "
            f"{'same' if row['bytes_equal'] else 'DIFF':5}  "
            f"{'; '.join(row['errors']) or '-'}"
        )
    print(
        f"\n{len(go_files)} Go / {len(ref_files)} reference profiles · "
        f"{'OK' if report['ok'] else f'{len(failures)} FAILING'}"
    )

    if args.json_out:
        Path(args.json_out).write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(f"report: {args.json_out}")

    if args.check and not report["ok"]:
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())