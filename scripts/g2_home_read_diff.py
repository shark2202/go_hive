#!/usr/bin/env python3
"""Diff the read-probe JSON documents produced by the Python and Go halves.

The read probes are what each implementation *sees* in a migrated HIVE_HOME
(session lists, histories, colony discovery, tracker query output, sidecars,
senders, skills, memories, credentials). Values carry two kinds of expected
noise that are normalized before comparison:

  * the absolute home path (the two copies live in different directories),
  * timestamps (wall-clock / copy-mtime derived, not part of the storage
    contract; the tree diff already compares every stored timestamp verbatim).

Everything else must match exactly.

Usage:
  python3 g2_home_read_diff.py --py py-read.json --go go-read.json \
      --out docs/e2e-assets/g2-home-e2e/v2-read-diff.json
"""

from __future__ import annotations

import argparse
import json
import os
import re
from pathlib import Path

ISO_RE = re.compile(r"\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?")


def normalise(value, home: str):
    if isinstance(value, str):
        value = value.replace(home, "{{HOME}}")
        if ISO_RE.fullmatch(value):
            return "{{TS}}"
        return value
    if isinstance(value, list):
        return [normalise(item, home) for item in value]
    if isinstance(value, dict):
        return {key: normalise(item, home) for key, item in value.items()}
    if isinstance(value, (int, float)) and not isinstance(value, bool) and abs(value) >= 1e9:
        # Epoch-derived values (copy mtimes, frozen fixture stamps) are not
        # storage facts; the tree diff already compares stored timestamps.
        return "{{EPOCH}}"
    return value


def walk(py, go, path: str, result: dict) -> None:
    if isinstance(py, bool) or isinstance(go, bool):
        if py != go:
            result["value_mismatches"].append({"path": path, "py": py, "go": go, "kind": "value"})
        return
    if isinstance(py, (int, float)) and isinstance(go, (int, float)):
        if float(py) != float(go):
            result["value_mismatches"].append({"path": path, "py": py, "go": go, "kind": "value"})
        return
    if type(py) is not type(go):
        result["value_mismatches"].append({"path": path, "py": py, "go": go, "kind": "type"})
        return
    if isinstance(py, dict):
        for key in sorted(set(py) - set(go)):
            result["only_py"].append({"path": f"{path}.{key}", "value": py[key]})
        for key in sorted(set(go) - set(py)):
            result["only_go"].append({"path": f"{path}.{key}", "value": go[key]})
        for key in sorted(set(py) & set(go)):
            walk(py[key], go[key], f"{path}.{key}", result)
        return
    if isinstance(py, list):
        if len(py) != len(go):
            result["value_mismatches"].append({"path": path, "py_len": len(py), "go_len": len(go), "kind": "length"})
            return
        for index, (left, right) in enumerate(zip(py, go)):
            walk(left, right, f"{path}[{index}]", result)
        return
    if py != go:
        result["value_mismatches"].append({"path": path, "py": py, "go": go, "kind": "value"})


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--py", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--label", default="")
    args = parser.parse_args()

    py = json.loads(Path(args.py).read_text(encoding="utf-8"))
    go = json.loads(Path(args.go).read_text(encoding="utf-8"))
    py_home = str(py.get("home", ""))
    go_home = str(go.get("home", ""))
    result = {
        "label": args.label,
        "py": args.py,
        "go": args.go,
        "py_home": py_home,
        "go_home": go_home,
        "normalisations": ["home path -> {{HOME}}", "ISO timestamps -> {{TS}}", "epoch numbers -> {{EPOCH}}"],
        "only_py": [],
        "only_go": [],
        "value_mismatches": [],
    }
    py_norm = normalise(normalise(py, os.path.realpath(py_home)), py_home)
    go_norm = normalise(normalise(go, os.path.realpath(go_home)), go_home)
    walk(py_norm, go_norm, "$", result)
    result["counts"] = {
        "only_py": len(result["only_py"]),
        "only_go": len(result["only_go"]),
        "value_mismatches": len(result["value_mismatches"]),
    }
    result["equal"] = not any(result["counts"].values())
    Path(args.out).parent.mkdir(parents=True, exist_ok=True)
    Path(args.out).write_text(json.dumps(result, indent=2, sort_keys=True), encoding="utf-8")
    print(json.dumps({"label": args.label, **result["counts"], "equal": result["equal"]}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
