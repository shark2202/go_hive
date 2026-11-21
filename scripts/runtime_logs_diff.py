#!/usr/bin/env python3
"""Field-level diff of the three runtime-log levels across the Python and Go
implementations (lane tool-logs-runtime).

Both sides are produced by replaying the same deterministic scenario through
their production loggers:

  go_hive/go-hive/e2e/cmd/runtimelogs        (Go)
  go_hive/scripts/runtime_logs_python_probe.py  (Python)

The comparison walks every JSON document (JSONL line / summary.json) and checks:

  * object key ORDER (the Pydantic declaration order is the contract),
  * field presence (python_only / go_only keys are diffs),
  * JSON types (bool / int|float number / string / array / object / null),
  * values, with two documented normalizations:
      - span_id: random 16-hex id per record on both sides -> shape only,
      - summary.started_at: wall-clock ISO timestamp -> shape only.

Usage:
  python go_hive/scripts/runtime_logs_diff.py --py <dir> --go <dir> --out <dir>

Exit 0 with 0 differences; exit 1 with the diff list. Writes diff.json and
copies the compared raw files under <out>/python/ and <out>/go/.
"""

from __future__ import annotations

import argparse
import json
import re
import shutil
import sys
from pathlib import Path

SPAN_RE = re.compile(r"^[0-9a-f]{16}$")
ISO_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(\+00:00|Z)$")


def json_kind(value) -> str:
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "bool"
    if isinstance(value, (int, float)):
        return "number"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    return type(value).__name__


def normalize(path: str, value):
    """Return (normalized_value, note) for the documented volatile fields."""
    if path.endswith("span_id") and isinstance(value, str):
        return ("<span>" if SPAN_RE.match(value) else value), "span shape"
    if path.endswith("started_at") and isinstance(value, str):
        return ("<iso>" if ISO_RE.match(value) else value), "started_at shape"
    return value, ""


def compare(path: str, py_value, go_value, diffs: list[dict]) -> None:
    py_norm, py_note = normalize(path, py_value)
    go_norm, go_note = normalize(path, go_value)
    if py_note or go_note:
        if py_norm != go_norm:
            diffs.append({"path": path, "python": py_value, "go": go_value, "reason": "normalized shape mismatch"})
        return

    py_kind, go_kind = json_kind(py_value), json_kind(go_value)
    if py_kind != go_kind:
        diffs.append({"path": path, "python": py_value, "go": go_value, "reason": f"type {py_kind} != {go_kind}"})
        return
    if py_kind == "object":
        py_keys, go_keys = list(py_value.keys()), list(go_value.keys())
        if py_keys != go_keys:
            diffs.append(
                {
                    "path": path,
                    "python": py_keys,
                    "go": go_keys,
                    "reason": "key set/order differs",
                }
            )
            for key in sorted(set(py_keys) & set(go_keys)):
                compare(f"{path}.{key}", py_value[key], go_value[key], diffs)
            return
        for key in py_keys:
            compare(f"{path}.{key}", py_value[key], go_value[key], diffs)
        return
    if py_kind == "array":
        if len(py_value) != len(go_value):
            diffs.append({"path": path, "python": len(py_value), "go": len(go_value), "reason": "array length"})
            return
        for index, (py_item, go_item) in enumerate(zip(py_value, go_value)):
            compare(f"{path}[{index}]", py_item, go_item, diffs)
        return
    if py_value != go_value:
        diffs.append({"path": path, "python": py_value, "go": go_value, "reason": "value differs"})


def load_jsonl(path: Path) -> list:
    records = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.strip():
            records.append(json.loads(line))
    return records


LOG_FILES = ("tool_logs.jsonl", "details.jsonl", "summary.json")


def compare_run(py_dir: Path, go_dir: Path, diffs: list[dict]) -> dict:
    stats = {"run": py_dir.name, "files": {}, "documents": 0}
    for name in LOG_FILES:
        py_file, go_file = py_dir / name, go_dir / name
        if not py_file.exists() or not go_file.exists():
            diffs.append(
                {
                    "path": f"{py_dir.name}/{name}",
                    "python": py_file.exists(),
                    "go": go_file.exists(),
                    "reason": "file missing on one side",
                }
            )
            continue
        if name.endswith(".jsonl"):
            py_docs, go_docs = load_jsonl(py_file), load_jsonl(go_file)
            if len(py_docs) != len(go_docs):
                diffs.append(
                    {
                        "path": f"{py_dir.name}/{name}",
                        "python": len(py_docs),
                        "go": len(go_docs),
                        "reason": "jsonl line count",
                    }
                )
                continue
            stats["files"][name] = len(py_docs)
            for index, (py_doc, go_doc) in enumerate(zip(py_docs, go_docs)):
                stats["documents"] += 1
                compare(f"{py_dir.name}/{name}[{index}]", py_doc, go_doc, diffs)
        else:
            py_doc = json.loads(py_file.read_text(encoding="utf-8"))
            go_doc = json.loads(go_file.read_text(encoding="utf-8"))
            stats["files"][name] = 1
            stats["documents"] += 1
            compare(f"{py_dir.name}/{name}", py_doc, go_doc, diffs)
    return stats


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--py", required=True, help="Python-side store root")
    parser.add_argument("--go", required=True, help="Go-side store root")
    parser.add_argument("--out", required=True, help="artifact directory")
    args = parser.parse_args(argv)

    py_root, go_root = Path(args.py), Path(args.go)
    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)

    py_runs = sorted(p.name for p in (py_root / "sessions").iterdir() if p.is_dir())
    go_runs = sorted(p.name for p in (go_root / "sessions").iterdir() if p.is_dir())
    diffs: list[dict] = []
    if py_runs != go_runs:
        diffs.append({"path": "sessions", "python": py_runs, "go": go_runs, "reason": "run id set differs"})

    stats = []
    for run_id in py_runs:
        if run_id not in go_runs:
            continue
        stats.append(compare_run(py_root / "sessions" / run_id / "logs", go_root / "sessions" / run_id / "logs", diffs))
        for side, root in (("python", py_root), ("go", go_root)):
            dest = out_dir / side / "sessions" / run_id / "logs"
            dest.mkdir(parents=True, exist_ok=True)
            for name in LOG_FILES:
                source = root / "sessions" / run_id / "logs" / name
                if source.exists():
                    shutil.copy2(source, dest / name)

    documents = sum(item["documents"] for item in stats)
    report = {
        "method": "field-level JSON diff (key order, presence, type, value) with span_id/started_at shape normalization",
        "runs": stats,
        "documents_compared": documents,
        "differences": diffs,
        "ok": not diffs,
    }
    (out_dir / "diff.json").write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    if diffs:
        print(f"runtime-logs diff: FAIL — {len(diffs)} difference(s) across {documents} document(s)")
        for diff in diffs[:20]:
            print(f"  {diff['path']}: {diff['reason']}: python={diff['python']!r} go={diff['go']!r}")
        return 1
    print(f"runtime-logs diff: OK — {documents} document(s), 0 differences (span_id/started_at compared by shape)")
    for item in stats:
        counts = ", ".join(f"{name}={count}" for name, count in item["files"].items())
        print(f"  {item['run']}: {counts}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
