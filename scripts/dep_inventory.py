#!/usr/bin/env python3
"""Generate and verify go-hive's dependency inventory and licence ledger.

The inventory is derived from the module graph, not from prose: every number
and every row comes from `go list` plus the module cache that the offline build
already requires. Two artifacts are owned here:

  go_hive/DEPENDENCY_INVENTORY.md        fully generated
  go_hive/go-hive/THIRD_PARTY_NOTICES.md generated block between markers

Usage (from go_hive/go-hive):
    python3 ../scripts/dep_inventory.py --module . --check
    python3 ../scripts/dep_inventory.py --module . --write

Exit codes:
    0  artifacts are current (--check) or were rewritten (--write)
    1  drift detected (--check)
    2  BLOCKED — go.mod, module graph, or markers are unusable
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import zipfile
from collections import Counter
from pathlib import Path

SCHEMA_NOTE = "本文件由 `go_hive/scripts/dep_inventory.py` 生成，请勿手工编辑。"
NOTICE_BEGIN = "<!-- BEGIN GENERATED:third-party-notices -->"
NOTICE_END = "<!-- END GENERATED:third-party-notices -->"

# Ordered most-specific first: BSD-3 must be tested before BSD-2.
LICENCE_PATTERNS: list[tuple[str, re.Pattern[str]]] = [
    ("Apache-2.0", re.compile(r"Apache License\s+Version 2\.0", re.I)),
    ("MPL-2.0", re.compile(r"Mozilla Public License,?\s+version 2\.0", re.I)),
    ("ISC", re.compile(r"\bISC License\b", re.I)),
    ("MIT", re.compile(r"\bMIT License\b|Permission is hereby granted, free of charge", re.I)),
    (
        "BSD-3-Clause",
        re.compile(r"Redistribution and use in source and binary forms.*?Neither the name", re.I | re.S),
    ),
    (
        "BSD-2-Clause",
        re.compile(r"Redistribution and use in source and binary forms", re.I | re.S),
    ),
    ("Unlicense", re.compile(r"This is free and unencumbered software released into the public domain", re.I)),
]

LICENCE_FILE = re.compile(r"^(LICEN[CS]E|COPYING|NOTICE)", re.I)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--module", default=".", help="module root containing go.mod")
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--check", action="store_true", help="fail if the committed artifacts are stale")
    mode.add_argument("--write", action="store_true", help="rewrite the generated artifacts")
    return parser.parse_args()


def offline_env() -> dict[str, str]:
    env = dict(os.environ)
    env.update(
        {
            "CGO_ENABLED": "0",
            "GOPROXY": "off",
            "GOSUMDB": "off",
            "GOFLAGS": "-mod=mod",
        }
    )
    return env


def run(args: list[str], cwd: Path) -> str:
    completed = subprocess.run(
        args,
        cwd=cwd,
        text=True,
        capture_output=True,
        check=False,
        env=offline_env(),
        timeout=300,
    )
    if completed.returncode != 0:
        print(
            f"dep-inventory: BLOCKED — {' '.join(args)} exited {completed.returncode}\n"
            f"{completed.stderr.strip()}",
            file=sys.stderr,
        )
        raise SystemExit(2)
    return completed.stdout


def parse_module_stream(text: str) -> list[dict]:
    modules: list[dict] = []
    decoder = json.JSONDecoder()
    index = 0
    while index < len(text):
        while index < len(text) and text[index].isspace():
            index += 1
        if index >= len(text):
            break
        payload, index = decoder.raw_decode(text, index)
        if isinstance(payload, dict):
            modules.append(payload)
    return modules


def escape_module_path(path: str) -> str:
    # Go module cache escaping: uppercase letters become !lowercase.
    return "".join("!" + char.lower() if char.isupper() else char for char in path)


def licence_from_dir(directory: Path) -> tuple[str, str]:
    for candidate in sorted(directory.iterdir()):
        if candidate.is_file() and LICENCE_FILE.match(candidate.name):
            return detect_licence(candidate.read_text(encoding="utf-8", errors="replace")), candidate.name
    return "UNKNOWN", ""


def licence_from_zip(module_cache: Path, module: dict) -> tuple[str, str]:
    archive = (
        module_cache
        / "cache"
        / "download"
        / escape_module_path(module["Path"])
        / "@v"
        / f"{module['Version']}.zip"
    )
    if not archive.is_file():
        return "UNVERIFIED", ""
    try:
        with zipfile.ZipFile(archive) as bundle:
            for name in sorted(bundle.namelist()):
                base = name.rsplit("/", 1)[-1]
                if LICENCE_FILE.match(base):
                    text = bundle.read(name).decode("utf-8", errors="replace")
                    return detect_licence(text), name.rsplit("/", 1)[-1]
    except (OSError, zipfile.BadZipFile):
        return "UNVERIFIED", ""
    return "UNKNOWN", ""


def detect_licence(text: str) -> str:
    compact = " ".join(text.split())
    apache = bool(re.search(r"Apache License", compact, re.I)) and bool(
        re.search(r"Version 2\.0", compact, re.I)
    )
    mit = bool(LICENCE_PATTERNS[3][1].search(compact))
    # gopkg.in/yaml.v3 and similar projects license files by origin: report the
    # split rather than the first template that happens to match.
    if apache and mit:
        return "MIT AND Apache-2.0"
    for name, pattern in LICENCE_PATTERNS:
        if pattern.search(compact):
            return name
    return "UNKNOWN"


def module_licence(module_cache: Path, module: dict) -> tuple[str, str]:
    directory = module.get("Dir") or ""
    if directory and Path(directory).is_dir():
        licence, file_name = licence_from_dir(Path(directory))
        if licence != "UNKNOWN" or file_name:
            return licence, file_name
    return licence_from_zip(module_cache, module)


def main() -> int:
    args = parse_args()
    module_root = Path(args.module).resolve()
    if not (module_root / "go.mod").is_file():
        print(f"dep-inventory: BLOCKED — no go.mod under {module_root}", file=sys.stderr)
        return 2

    go_version = run(["go", "version"], module_root).strip()
    module_cache = Path(run(["go", "env", "GOMODCACHE"], module_root).strip())

    require_raw = json.loads(run(["go", "mod", "edit", "-json"], module_root))
    requires = {item["Path"]: item for item in require_raw.get("Require", []) or []}

    modules = [m for m in parse_module_stream(run(["go", "list", "-m", "-json", "all"], module_root)) if not m.get("Main")]
    modules.sort(key=lambda m: m["Path"])

    closure = {
        line.strip()
        for line in run(
            ["go", "list", "-deps", "-f", "{{with .Module}}{{.Path}}{{end}}", "./..."],
            module_root,
        ).splitlines()
        if line.strip()
    }

    rows = []
    role_counts: Counter[str] = Counter()
    licence_counts: Counter[str] = Counter()
    for module in modules:
        path = module["Path"]
        if path not in requires:
            role = "仅图内"
        elif requires[path].get("Indirect"):
            role = "间接"
        else:
            role = "直接"
        compiled = path in closure
        licence, licence_file = module_licence(module_cache, module)
        role_counts[role] += 1
        licence_counts[licence] += 1
        rows.append(
            {
                "path": path,
                "version": module.get("Version") or "",
                "role": role,
                "compiled": compiled,
                "licence": licence,
                "licence_file": licence_file,
            }
        )

    external = len(rows)
    compiled = sum(1 for row in rows if row["compiled"])
    unverified = [row for row in rows if row["licence"] == "UNVERIFIED"]
    unknown = [row for row in rows if row["licence"] == "UNKNOWN"]
    licence_line = ", ".join(
        f"{name} x{count}" for name, count in sorted(licence_counts.items())
    )

    inventory_lines = [
        "# go-hive 依赖清单",
        "",
        f"> {SCHEMA_NOTE}",
        "> 校验：`cd go_hive/go-hive && make deps-check`；重生成：`make deps-update`。",
        "",
        "## 0. 摘要",
        "",
        f"- 工具链：`{go_version}`",
        f"- 外部模块 **{external}** 个 = 直接 **{role_counts['直接']}** + 间接 **{role_counts['间接']}** + 仅图内 **{role_counts['仅图内']}**。",
        f"- 进入编译闭包的外部模块 **{compiled}** 个；其余 **{external - compiled}** 个不产生代码。",
        f"- 许可证已识别 **{external - len(unverified) - len(unknown)}** / {external}。",
        "",
        "## 1. 依赖清单",
        "",
        "类别口径：直接 = `go.mod` 首个 `require` 块；间接 = 带 `// indirect` 的条目；"
        "仅图内 = 只参与最小版本选择、不写进 `go.mod`。编译闭包由 `go list -deps ./...` 判定。",
        "",
        "| 模块 | 版本 | 类别 | 编译闭包 | 许可证 |",
        "| --- | --- | --- | --- | --- |",
    ]
    for row in rows:
        inventory_lines.append(
            f"| `{row['path']}` | `{row['version']}` | {row['role']} | "
            f"{'✓' if row['compiled'] else '—'} | {row['licence']} |"
        )
    inventory_lines += ["", f"许可证分布（{external} 个外部模块）：{licence_line}。"]

    inventory_lines += ["", "## 2. 编译闭包", ""]
    compiled_rows = [row for row in rows if row["compiled"]]
    inventory_lines.append(f"共 {len(compiled_rows)} 个外部模块（另有本模块自身）：")
    inventory_lines.append("")
    for row in compiled_rows:
        inventory_lines.append(f"- `{row['path']}` `{row['version']}` — {row['licence']}")

    inventory_lines += ["", "## 3. 许可证核验缺口", ""]
    if not unverified and not unknown:
        inventory_lines.append("无：每个外部模块的许可证都已从模块内容的 `LICENSE`/`COPYING`/`NOTICE` 识别。")
    else:
        inventory_lines.append(
            "以下模块未能在本机 module cache 中找到许可证原文。它们在发行前必须补齐核验："
        )
        inventory_lines.append("")
        for row in unverified + unknown:
            reason = "模块未在 cache 中" if row["licence"] == "UNVERIFIED" else "未识别出已知许可证模板"
            inventory_lines.append(
                f"- `{row['path']}` `{row['version']}`（{row['role']}，{'编译闭包内' if row['compiled'] else '不编译'}）— {reason}"
            )
        inventory_lines.append("")
        inventory_lines.append(
            "补齐方式：在联网机器执行 `go mod download <module>`，或人工读取该版本归档内的许可证文件后更新判定规则。"
        )

    inventory = "\n".join(inventory_lines) + "\n"

    notices_lines = [
        NOTICE_BEGIN,
        "",
        f"<!-- {SCHEMA_NOTE} -->",
        "",
        f"Go module graph as generated from `go.mod` / `go.sum` under `{go_version}`: "
        f"{external} external modules, {compiled} in the compile closure.",
        "",
        "| Module | Version | Licence | Role | Source |",
        "| --- | --- | --- | --- | --- |",
    ]
    for row in rows:
        source = f"https://pkg.go.dev/{row['path']}@{row['version']}"
        notices_lines.append(
            f"| `{row['path']}` | `{row['version']}` | {row['licence']} | "
            f"{'direct' if row['role'] == '直接' else 'indirect' if row['role'] == '间接' else 'build list only'} | "
            f"[{row['path']}]({source}) |"
        )
    notices_lines += [
        "",
        f"Licence count: {licence_line} ({external} modules total).",
        "",
        "Licence identifiers were read from each module's own `LICENSE` / `COPYING` / `NOTICE` file "
        "in the Go module cache. `UNVERIFIED` means the module was not present in the cache used to "
        "generate this table; it must be checked before a release that links it.",
        "",
        NOTICE_END,
    ]
    notices_block = "\n".join(notices_lines)

    inventory_path = module_root.parent / "DEPENDENCY_INVENTORY.md"
    notices_path = module_root / "THIRD_PARTY_NOTICES.md"

    if args.write:
        inventory_path.write_text(inventory, encoding="utf-8")
        print(f"dep-inventory: wrote {inventory_path} ({external} modules, {compiled} compiled)")
        write_notice_block(notices_path, notices_block)
        print(f"dep-inventory: updated {notices_path}")
        return 0

    failures = 0
    try:
        current = inventory_path.read_text(encoding="utf-8")
    except FileNotFoundError:
        print(f"dep-inventory: BLOCKED — missing {inventory_path}; run --write", file=sys.stderr)
        return 2
    if current != inventory:
        print(f"dep-inventory: STALE — {inventory_path} does not match the module graph; run: make deps-update")
        failures += 1
    if not notice_block_matches(notices_path, notices_block):
        print(f"dep-inventory: STALE — {notices_path} generated block does not match; run: make deps-update")
        failures += 1
    if failures:
        return 1
    print(f"dep-inventory: OK ({external} modules, {compiled} compiled, {role_counts['直接']} direct)")
    return 0


def write_notice_block(path: Path, block: str) -> None:
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        text = "# Third-Party Notices\n\n" + NOTICE_BEGIN + "\n" + NOTICE_END + "\n"
    text = replace_block(text, block, path)
    path.write_text(text, encoding="utf-8")


def notice_block_matches(path: Path, block: str) -> bool:
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return False
    return extract_block(text) == block


def extract_block(text: str) -> str | None:
    start = text.find(NOTICE_BEGIN)
    end = text.find(NOTICE_END)
    if start < 0 or end < 0 or end < start:
        return None
    return text[start : end + len(NOTICE_END)]


def replace_block(text: str, block: str, path: Path) -> str:
    existing = extract_block(text)
    if existing is None:
        print(f"dep-inventory: BLOCKED — markers missing in {path}", file=sys.stderr)
        raise SystemExit(2)
    return text.replace(existing, block)


if __name__ == "__main__":
    raise SystemExit(main())
