#!/usr/bin/env python3
"""SC-04: HIVE_HOME permission-bit probe + diff.

`probe` walks a home written by one implementation and records every directory
and file mode (octal, permission bits only). `diff` compares two probes after
path renames and groups the differences, so the report can state exactly which
directories/files each side creates wider or narrower.

Stdlib only. Output feeds docs/e2e-assets/storage-sc04-06/sc04-perms.json and
the SC-04 table in docs/storage-contract-diff.md.
"""

from __future__ import annotations

import argparse
import json
import os
import stat
import sys
from pathlib import Path


def probe(home: Path) -> dict:
    home = home.resolve()
    mask = os.umask(0o022)
    os.umask(mask)
    entries: dict[str, dict] = {}
    for path in sorted(home.rglob("*")):
        rel = str(path.relative_to(home))
        try:
            info = os.lstat(path)
        except OSError:
            continue
        if stat.S_ISDIR(info.st_mode):
            kind = "dir"
        elif stat.S_ISREG(info.st_mode):
            kind = "file"
        else:
            kind = "other"
        entries[rel] = {"kind": kind, "mode": format(stat.S_IMODE(info.st_mode), "04o")}
    return {"home": str(home), "umask": format(mask, "04o"), "count": len(entries), "entries": entries}


def apply_rename(entries: dict[str, dict], rename: dict[str, str], label: str) -> dict[str, dict]:
    out: dict[str, dict] = {}
    for rel, value in entries.items():
        for src, dst in rename.items():
            if rel == src or rel.startswith(src + "/"):
                rel = dst + rel[len(src):]
                break
        out[rel] = value
    return out


def parse_renames(values: list[str]) -> dict[str, str]:
    out = {}
    for item in values:
        if "=>" not in item:
            raise SystemExit(f"--rename wants FROM=>TO, got {item!r}")
        src, dst = item.split("=>", 1)
        out[src] = dst
    return out


def area_of(rel: str) -> str:
    parts = rel.split("/")
    head = parts[0]
    if head in ("queens", "colonies"):
        if any(seg == "sessions" for seg in parts):
            return "sessions (session dirs + files)"
        if len(parts) >= 3 and parts[2] == "workers":
            return "worker dirs + files"
        return "colony dirs + files"
    if head in ("credentials", "secrets"):
        return "credentials + secrets"
    if head == "mcp_registry":
        return "mcp_registry"
    if head in ("exports", "agents", "memories", "senders", "skills"):
        return head
    return "other"


def cmd_probe(args) -> int:
    result = probe(Path(args.home))
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(result, indent=2, sort_keys=True), encoding="utf-8")
    print(json.dumps({"home": result["home"], "entries": result["count"], "umask": result["umask"]}))
    return 0


def cmd_diff(args) -> int:
    py = json.loads(Path(args.py).read_text(encoding="utf-8"))
    go = json.loads(Path(args.go).read_text(encoding="utf-8"))
    rename = parse_renames(args.rename or [])
    py_entries = apply_rename(py["entries"], rename, "py")
    go_entries = apply_rename(go["entries"], rename, "go")

    mismatches, only_py, only_go = [], [], []
    for rel in sorted(set(py_entries) | set(go_entries)):
        left, right = py_entries.get(rel), go_entries.get(rel)
        if left is None:
            only_go.append({"path": rel, "go": right})
        elif right is None:
            only_py.append({"path": rel, "py": left})
        elif left["mode"] != right["mode"] or left["kind"] != right["kind"]:
            mismatches.append({"path": rel, "py": left, "go": right})

    groups: dict[tuple, dict] = {}
    for item in mismatches:
        key = (item["py"]["kind"], item["py"]["mode"], item["go"]["kind"], item["go"]["mode"])
        group = groups.setdefault(key, {"py": item["py"]["mode"], "go": item["go"]["mode"],
                                        "py_kind": item["py"]["kind"], "go_kind": item["go"]["kind"],
                                        "count": 0, "examples": []})
        group["count"] += 1
        if len(group["examples"]) < 12:
            group["examples"].append(item["path"])

    area_summary: dict[str, dict] = {}
    for item in mismatches:
        area = area_of(item["path"])
        bucket = area_summary.setdefault(area, {"mismatch_count": 0, "examples": [],
                                                "py_modes": {}, "go_modes": {}})
        bucket["mismatch_count"] += 1
        bucket["py_modes"][item["py"]["mode"]] = bucket["py_modes"].get(item["py"]["mode"], 0) + 1
        bucket["go_modes"][item["go"]["mode"]] = bucket["go_modes"].get(item["go"]["mode"], 0) + 1
        if len(bucket["examples"]) < 8:
            bucket["examples"].append(item["path"])

    report = {
        "py_home": py["home"],
        "go_home": go["home"],
        "umask": {"py": py.get("umask"), "go": go.get("umask")},
        "counts": {
            "py": len(py_entries),
            "go": len(go_entries),
            "mode_equal": len(set(py_entries) & set(go_entries)) - len(mismatches),
            "mode_mismatch": len(mismatches),
            "only_py": len(only_py),
            "only_go": len(only_go),
        },
        "groups": [
            {"py_mode": key[1], "go_mode": key[3], "py_kind": key[0], "go_kind": key[2],
             "count": value["count"], "examples": value["examples"]}
            for key, value in sorted(groups.items(), key=lambda item: -item[1]["count"])
        ],
        "area_summary": area_summary,
        "mismatches": mismatches,
        "only_py": only_py[:200],
        "only_go": only_go[:200],
        "renames": rename,
    }
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, indent=2, sort_keys=True), encoding="utf-8")

    print(json.dumps({
        "counts": report["counts"],
        "groups": [{"py": g["py_mode"], "go": g["go_mode"], "count": g["count"], "examples": g["examples"][:4]}
                   for g in report["groups"]],
        "areas": {area: data["mismatch_count"] for area, data in sorted(area_summary.items())},
    }, indent=2))
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("probe")
    p.add_argument("--home", required=True)
    p.add_argument("--out", required=True)
    p.set_defaults(func=cmd_probe)

    p = sub.add_parser("diff")
    p.add_argument("--py", required=True)
    p.add_argument("--go", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--rename", action="append")
    p.set_defaults(func=cmd_diff)

    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
