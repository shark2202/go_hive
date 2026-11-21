#!/usr/bin/env python3
"""Diff two HIVE_HOME trees produced by the Python and Go halves.

Compares:
  * directory trees (relative paths, sha256 with the home path normalised)
  * every JSON file's field set + value types (recursive)
  * every SQLite file's schema (tables, columns, indexes, triggers, user_version)

Output JSON is designed to feed both the human report
(docs/storage-contract-diff.md) and the machine contract
(contracts/storage-fields.json). No third-party deps.

Usage:
  python3 storage_compat_diff.py run --py <home> --go <home> --label v2 \
      --out <dir> [--rename session_compat_probe_py=>session_compat_probe ...]
  python3 storage_compat_diff.py summary --dir <out>
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import shutil
import sqlite3
import tempfile
from pathlib import Path

TEXT_SUFFIXES = {".json", ".jsonl", ".yaml", ".yml", ".md", ".txt", ".log"}
TIMESTAMP_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$")


# ---------------------------------------------------------------------------
# tree walk
# ---------------------------------------------------------------------------


def normalise(text: str, home: Path) -> str:
    return text.replace(str(home), "{{HOME}}")


def file_hash(path: Path, home: Path) -> str:
    data = path.read_bytes()
    if path.suffix.lower() in TEXT_SUFFIXES:
        try:
            return hashlib.sha256(normalise(data.decode("utf-8"), home).encode()).hexdigest()
        except UnicodeDecodeError:
            pass
    return hashlib.sha256(data).hexdigest()


def walk(root: Path, rename: dict[str, str]) -> dict[str, Path]:
    files: dict[str, Path] = {}
    for path in sorted(root.rglob("*")):
        if path.is_dir():
            continue
        rel = str(path.relative_to(root))
        for src, dst in rename.items():
            if rel == src or rel.startswith(src + "/"):
                rel = dst + rel[len(src):]
                break
        files[rel] = path
    return files


def diff_trees(py_root: Path, go_root: Path, rename: dict[str, str]) -> dict:
    py_files = walk(py_root, rename)
    go_files = walk(go_root, rename)
    result = {
        "py_root": str(py_root),
        "go_root": str(go_root),
        "counts": {"py": len(py_files), "go": len(go_files)},
        "only_py": sorted(set(py_files) - set(go_files)),
        "only_go": sorted(set(go_files) - set(py_files)),
        "changed": [],
        "changed_opaque": [],
        "equal": [],
        "py_dirs": sorted(str(p.relative_to(py_root)) for p in py_root.rglob("*") if p.is_dir()),
        "go_dirs": sorted(str(p.relative_to(go_root)) for p in go_root.rglob("*") if p.is_dir()),
    }
    for rel in sorted(set(py_files) & set(go_files)):
        ph = file_hash(py_files[rel], py_root)
        gh = file_hash(go_files[rel], go_root)
        if ph == gh:
            result["equal"].append(rel)
        elif rel.endswith((".db", ".enc")):
            # SQLite/fernet files are never byte-identical across engines
            # (page layout, random IV). They are verified structurally /
            # by cross-read instead of by hash.
            result["changed_opaque"].append(rel)
        else:
            result["changed"].append({"path": rel, "py_sha256": ph, "go_sha256": gh, "py_size": py_files[rel].stat().st_size, "go_size": go_files[rel].stat().st_size})
    result["dirs_only_py"] = sorted(set(result["py_dirs"]) - set(result["go_dirs"]))
    result["dirs_only_go"] = sorted(set(result["go_dirs"]) - set(result["py_dirs"]))
    return result


# ---------------------------------------------------------------------------
# JSON field/type extraction
# ---------------------------------------------------------------------------


def type_name(value) -> str:
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "bool"
    if isinstance(value, int):
        return "int"
    if isinstance(value, float):
        return "float"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        if not value:
            return "array[]"
        return "array[" + type_name(value[0]) + "]"
    if isinstance(value, dict):
        return "object"
    return type(value).__name__


def flatten(value, prefix: str, out: dict[str, str]) -> None:
    out[prefix or "$"] = type_name(value)
    if isinstance(value, dict):
        for key in sorted(value):
            flatten(value[key], f"{prefix}.{key}" if prefix else key, out)
    elif isinstance(value, list) and value:
        flatten(value[0], prefix + "[]", out)


def load_json(path: Path):
    try:
        return json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, json.JSONDecodeError):
        return None


def diff_json_file(py_path: Path, go_path: Path) -> dict:
    py = load_json(py_path)
    go = load_json(go_path)
    py_fields: dict[str, str] = {}
    go_fields: dict[str, str] = {}
    if py is not None:
        flatten(py, "", py_fields)
    if go is not None:
        flatten(go, "", go_fields)
    return {
        "only_py": {k: py_fields[k] for k in sorted(set(py_fields) - set(go_fields))},
        "only_go": {k: go_fields[k] for k in sorted(set(go_fields) - set(py_fields))},
        "type_mismatch": {
            k: {"py": py_fields[k], "go": go_fields[k]}
            for k in sorted(set(py_fields) & set(go_fields))
            if py_fields[k] != go_fields[k]
        },
    }


def diff_all_json(py_root: Path, go_root: Path, rename: dict[str, str]) -> dict:
    py_files = walk(py_root, rename)
    go_files = walk(go_root, rename)
    out = {}
    for rel in sorted(set(py_files) & set(go_files)):
        if not rel.endswith(".json"):
            continue
        diff = diff_json_file(py_files[rel], go_files[rel])
        if diff["only_py"] or diff["only_go"] or diff["type_mismatch"]:
            out[rel] = diff
    return out


# ---------------------------------------------------------------------------
# SQLite schema
# ---------------------------------------------------------------------------


def _normalise_timestamps(value):
    """Replace ISO-8601 timestamp strings with a placeholder.

    Row comparison must not fail on wall-clock values (e.g. the
    _tracker_meta.updated_at row each side writes when it first creates the
    DB). Format and drift are reported separately from content drift.
    """
    if isinstance(value, str) and TIMESTAMP_RE.match(value):
        return "{{TS}}"
    if isinstance(value, list):
        return [_normalise_timestamps(v) for v in value]
    if isinstance(value, dict):
        return {k: _normalise_timestamps(v) for k, v in value.items()}
    return value


def _normalise_rows(rows: dict) -> dict:
    return {table: {"row_count": data.get("row_count"), "rows": _normalise_timestamps(data.get("rows"))} for table, data in rows.items()}


def sqlite_schema(path: Path) -> dict:
    # WAL-mode DBs refuse a read-only open when the -shm/-wal siblings are
    # absent, and opening them in place would create those siblings inside the
    # tree we are diffing. Work on a copy instead.
    tmp_dir = tempfile.mkdtemp(prefix="storage-compat-sqlite-")
    try:
        copy = Path(tmp_dir) / path.name
        shutil.copy2(path, copy)
        return _sqlite_schema_inplace(copy)
    finally:
        shutil.rmtree(tmp_dir, ignore_errors=True)


def _sqlite_schema_inplace(path: Path) -> dict:
    con = sqlite3.connect(str(path))
    try:
        result: dict = {"user_version": con.execute("PRAGMA user_version").fetchone()[0]}
        try:
            result["journal_mode"] = con.execute("PRAGMA journal_mode").fetchone()[0]
        except sqlite3.DatabaseError:
            result["journal_mode"] = None
        tables = {}
        for name, sql in con.execute("SELECT name, sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"):
            cols = [
                {"name": row[1], "type": row[2], "notnull": bool(row[3]), "default": row[4], "pk": row[5]}
                for row in con.execute(f"PRAGMA table_info({quote(name)})")
            ]
            tables[name] = {"sql": sql, "columns": cols}
        indexes = {}
        for name, tbl, sql in con.execute("SELECT name, tbl_name, sql FROM sqlite_master WHERE type='index' AND sql IS NOT NULL ORDER BY name"):
            unique = 0
            try:
                unique = con.execute(f"PRAGMA index_list({quote(tbl)})").fetchall()
                unique = next((row[2] for row in unique if row[1] == name), 0)
            except sqlite3.DatabaseError:
                unique = 0
            cols = [row[2] for row in con.execute(f"PRAGMA index_info({quote(name)})")]
            indexes[name] = {"table": tbl, "unique": bool(unique), "columns": cols, "sql": sql}
        triggers = {}
        for name, tbl, sql in con.execute("SELECT name, tbl_name, sql FROM sqlite_master WHERE type='trigger' ORDER BY name"):
            triggers[name] = {"table": tbl, "sql": sql}
        rows = {}
        for name in tables:
            try:
                data = con.execute(f"SELECT * FROM {quote(name)} ORDER BY rowid").fetchall()
                rows[name] = {"row_count": len(data), "rows": [list(row) for row in data][:200]}
            except sqlite3.DatabaseError as exc:
                rows[name] = {"error": str(exc)}
        result.update({"tables": tables, "indexes": indexes, "triggers": triggers, "rows": rows})
        return result
    finally:
        con.close()


def quote(identifier: str) -> str:
    return '"' + identifier.replace('"', '""') + '"'


def diff_sqlite(py_root: Path, go_root: Path, rename: dict[str, str]) -> dict:
    py_files = walk(py_root, rename)
    go_files = walk(go_root, rename)
    out = {}
    for rel in sorted(set(py_files) & set(go_files)):
        if not rel.endswith((".db", ".sqlite")):
            continue
        try:
            py = sqlite_schema(py_files[rel])
            go = sqlite_schema(go_files[rel])
        except sqlite3.DatabaseError as exc:
            out[rel] = {"error": str(exc)}
            continue
        diff = {"py": py, "go": go, "tables_only_py": sorted(set(py["tables"]) - set(go["tables"])),
                "tables_only_go": sorted(set(go["tables"]) - set(py["tables"])),
                "indexes_only_py": sorted(set(py["indexes"]) - set(go["indexes"])),
                "indexes_only_go": sorted(set(go["indexes"]) - set(py["indexes"])),
                "triggers_only_py": sorted(set(py["triggers"]) - set(go["triggers"])),
                "triggers_only_go": sorted(set(go["triggers"]) - set(py["triggers"])),
                "column_mismatches": {},
                "user_version": {"py": py["user_version"], "go": go["user_version"]},
                "journal_mode": {"py": py["journal_mode"], "go": go["journal_mode"]},
                "schema_equal": py["tables"] == go["tables"] and py["indexes"] == go["indexes"] and py["triggers"] == go["triggers"],
                "rows_equal_raw": py["rows"] == go["rows"],
                "rows_equal": _normalise_rows(py["rows"]) == _normalise_rows(go["rows"])}
        for table in sorted(set(py["tables"]) & set(go["tables"])):
            if py["tables"][table]["columns"] != go["tables"][table]["columns"]:
                diff["column_mismatches"][table] = {"py": py["tables"][table]["columns"], "go": go["tables"][table]["columns"]}
        if diff["schema_equal"] and diff["rows_equal"] and not diff["tables_only_py"] and not diff["tables_only_go"]:
            diff.pop("py")
            diff.pop("go")
        out[rel] = diff
    return out


# ---------------------------------------------------------------------------
# summary
# ---------------------------------------------------------------------------

RUNS = ["v1", "v2", "colony-fork", "probes"]


def write_summary(out_dir: Path) -> None:
    lines = ["# Storage compat double-run summary", "", "Generated by `go_hive/scripts/storage_compat_diff.py summary`.", ""]
    for label in RUNS:
        tree_path = out_dir / f"{label}-tree-diff.json"
        field_path = out_dir / f"{label}-field-diff.json"
        sqlite_path = out_dir / f"{label}-sqlite-diff.json"
        if not tree_path.exists():
            continue
        tree = json.loads(tree_path.read_text())
        fields = json.loads(field_path.read_text()) if field_path.exists() else {}
        sqlite = json.loads(sqlite_path.read_text()) if sqlite_path.exists() else {}
        lines += [f"## {label}", ""]
        lines += [
            f"- files: python {tree['counts']['py']} / go {tree['counts']['go']}",
            f"- identical files: {len(tree['equal'])}",
            f"- changed files: {len(tree['changed'])}",
            f"- opaque files (db/enc; byte-different by design, verified structurally): {len(tree['changed_opaque'])}",
            f"- only python: {len(tree['only_py'])} {tree['only_py'][:8]}",
            f"- only go: {len(tree['only_go'])} {tree['only_go'][:8]}",
            f"- dirs only python: {len(tree['dirs_only_py'])} {tree['dirs_only_py'][:8]}",
            f"- dirs only go: {len(tree['dirs_only_go'])} {tree['dirs_only_go'][:8]}",
            f"- JSON files with field diffs: {len(fields)}",
            f"- SQLite files with schema/row mismatch: {sum(1 for v in sqlite.values() if not v.get('schema_equal', False) or not v.get('rows_equal', False))}",
            "",
        ]
        for rel, diff in list(fields.items())[:20]:
            if diff["only_py"] or diff["only_go"]:
                lines.append(f"  - `{rel}`: py-only {sorted(diff['only_py'])[:6]} go-only {sorted(diff['only_go'])[:6]}")
            if diff["type_mismatch"]:
                sample = list(diff["type_mismatch"].items())[:3]
                lines.append(f"  - `{rel}`: type mismatches {sample}")
        lines.append("")
    (out_dir / "summary.md").write_text("\n".join(lines), encoding="utf-8")


def parse_renames(values: list[str]) -> dict[str, str]:
    out = {}
    for item in values:
        if "=>" not in item:
            raise SystemExit(f"--rename wants FROM=>TO, got {item!r}")
        src, dst = item.split("=>", 1)
        out[src] = dst
    return out


def cmd_run(args) -> int:
    py_root = Path(args.py).resolve()
    go_root = Path(args.go).resolve()
    out_dir = Path(args.out).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    rename = parse_renames(args.rename or [])

    tree = diff_trees(py_root, go_root, rename)
    fields = diff_all_json(py_root, go_root, rename)
    sqlite = diff_sqlite(py_root, go_root, rename)

    (out_dir / f"{args.label}-tree-diff.json").write_text(json.dumps(tree, indent=2, sort_keys=True), encoding="utf-8")
    (out_dir / f"{args.label}-field-diff.json").write_text(json.dumps(fields, indent=2, sort_keys=True), encoding="utf-8")
    (out_dir / f"{args.label}-sqlite-diff.json").write_text(json.dumps(sqlite, indent=2, sort_keys=True), encoding="utf-8")

    print(json.dumps({
        "label": args.label,
        "files_py": tree["counts"]["py"],
        "files_go": tree["counts"]["go"],
        "identical": len(tree["equal"]),
        "changed": len(tree["changed"]),
        "changed_opaque": len(tree["changed_opaque"]),
        "only_py": len(tree["only_py"]),
        "only_go": len(tree["only_go"]),
        "json_field_diffs": len(fields),
        "sqlite_schema_mismatches": sum(1 for v in sqlite.values() if not v.get("schema_equal", False) or not v.get("rows_equal", False)),
    }, indent=2))
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("run")
    p.add_argument("--py", required=True)
    p.add_argument("--go", required=True)
    p.add_argument("--label", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--rename", action="append")
    p.set_defaults(func=cmd_run)
    p = sub.add_parser("summary")
    p.add_argument("--dir", required=True)
    p.set_defaults(func=lambda a: write_summary(Path(a.dir).resolve()) or 0)
    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
