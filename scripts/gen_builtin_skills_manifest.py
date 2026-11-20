#!/usr/bin/env python3
"""Freeze the bundled skill trees as a byte-exact manifest.

go-hive embeds two built-in skill trees that mirror this repository's Python
reference tree ``core/framework/skills``:

    _default_skills  (12 skills, 13 files incl. pdf/REFERENCE.md)
    _preset_skills   (8 skills, 12 files incl. references/*.md)

``internal/skills/discovery.go`` embeds both with ``//go:embed all:...``, and
``internal/skills/builtin_manifest_test.go`` verifies the on-disk copies and
the embedded copies against the checked-in manifest.  Every file is hashed
verbatim, so SKILL.md frontmatter is frozen byte-for-byte too.

Usage, from the repository root:

    uv run --no-project python go_hive/scripts/gen_builtin_skills_manifest.py
    uv run --no-project python go_hive/scripts/gen_builtin_skills_manifest.py --check
    uv run --no-project python go_hive/scripts/gen_builtin_skills_manifest.py \
        --source core/framework/skills --output go_hive/contracts/builtin-skills.json
    uv run --no-project python go_hive/scripts/gen_builtin_skills_manifest.py \
        --verify-tree go_hive/go-hive/internal/skills

``--check`` regenerates in memory and exits non-zero when the checked-in
manifest is stale.  ``--verify-tree`` compares another skills root (e.g. the
Go copy) against the generated entries and reports every missing, extra, or
changed file.  Only the Python standard library is used.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path
from typing import Any

DESCRIPTION = "Freeze the bundled skill trees as a byte-exact manifest."

SCHEMA = 1
SCOPES = ("_default_skills", "_preset_skills")
GENERATOR = "go_hive/scripts/gen_builtin_skills_manifest.py"
DEFAULT_SOURCE = Path("core/framework/skills")
DEFAULT_OUTPUT = Path("go_hive/contracts/builtin-skills.json")


def repo_root() -> Path | None:
    """Return the repository root (contains both core/ and go_hive/) if any."""
    here = Path(__file__).resolve()
    for candidate in here.parents:
        if (candidate / DEFAULT_SOURCE).is_dir() and (candidate / "go_hive").is_dir():
            return candidate
    return None


def scan_tree(root: Path) -> list[dict[str, Any]]:
    """Hash every regular file under ``root/_default_skills`` and ``_preset_skills``."""
    if not root.is_dir():
        raise SystemExit(f"gen_builtin_skills_manifest: skills tree not found: {root}")
    entries: list[dict[str, Any]] = []
    for scope in SCOPES:
        scope_dir = root / scope
        if not scope_dir.is_dir():
            raise SystemExit(f"gen_builtin_skills_manifest: missing {scope}/ under {root}")
        for path in sorted(scope_dir.rglob("*")):
            if path.is_dir():
                continue
            if path.is_symlink() or not path.is_file():
                raise SystemExit(
                    f"gen_builtin_skills_manifest: unsupported entry (not a regular file): {path}"
                )
            data = path.read_bytes()
            entries.append(
                {
                    "path": path.relative_to(root).as_posix(),
                    "sha256": hashlib.sha256(data).hexdigest(),
                    "bytes": len(data),
                }
            )
    entries.sort(key=lambda entry: str(entry["path"]))
    return entries


def build_manifest(entries: list[dict[str, Any]]) -> dict[str, Any]:
    return {
        "schema": SCHEMA,
        "generator": GENERATOR,
        "source": DEFAULT_SOURCE.as_posix(),
        "count": len(entries),
        "total_bytes": sum(int(entry["bytes"]) for entry in entries),
        "files": entries,
    }


def render(payload: dict[str, Any]) -> str:
    return json.dumps(payload, ensure_ascii=False, indent=2) + "\n"


def index(entries: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    return {str(entry["path"]): entry for entry in entries}


def verify_tree(tree: Path, entries: list[dict[str, Any]]) -> bool:
    """Compare another skills root against the generated entries."""
    expected = index(entries)
    actual = index(scan_tree(tree))
    problems: list[str] = []
    for path in sorted(set(expected) - set(actual)):
        problems.append(f"missing: {path}")
    for path in sorted(set(actual) - set(expected)):
        problems.append(f"unexpected: {path}")
    for path in sorted(set(actual) & set(expected)):
        want, got = expected[path], actual[path]
        if got["bytes"] != want["bytes"]:
            problems.append(
                f"bytes differ: {path} ({got['bytes']} != {want['bytes']})"
            )
        elif got["sha256"] != want["sha256"]:
            problems.append(
                f"sha256 differs: {path} ({got['sha256']} != {want['sha256']})"
            )
    if problems:
        print(f"gen_builtin_skills_manifest: {tree} does not match the manifest:")
        for problem in problems:
            print(f"  {problem}")
        return False
    print(
        f"gen_builtin_skills_manifest: {tree} matches "
        f"({len(expected)} files, {sum(int(e['bytes']) for e in entries)} bytes)"
    )
    return True


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=DESCRIPTION)
    parser.add_argument(
        "--source",
        type=Path,
        default=None,
        help=f"skills root containing {'/'.join(SCOPES)} "
        f"(default: <repo>/{DEFAULT_SOURCE.as_posix()})",
    )
    parser.add_argument(
        "--output",
        type=Path,
        default=None,
        help=f"manifest path (default: <repo>/{DEFAULT_OUTPUT.as_posix()})",
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="do not write; fail when the checked-in manifest is stale",
    )
    parser.add_argument(
        "--verify-tree",
        type=Path,
        default=None,
        metavar="DIR",
        help="compare another skills root (e.g. the Go copy) against the manifest",
    )
    args = parser.parse_args(argv)

    root = repo_root()
    source = args.source if args.source is not None else (
        root / DEFAULT_SOURCE if root else None
    )
    if source is None:
        parser.error("--source is required when the repository root cannot be found")
    output = args.output if args.output is not None else (
        root / DEFAULT_OUTPUT if root else None
    )
    if output is None:
        parser.error("--output is required when the repository root cannot be found")

    entries = scan_tree(source)
    payload = build_manifest(entries)
    text = render(payload)

    ok = True
    if args.verify_tree is not None:
        ok = verify_tree(args.verify_tree, entries) and ok

    if args.check:
        if not output.is_file():
            print(f"gen_builtin_skills_manifest: manifest not found: {output}", file=sys.stderr)
            return 1
        current = output.read_text(encoding="utf-8")
        if current != text:
            print(
                f"gen_builtin_skills_manifest: STALE — {output} differs from "
                f"{source}.\nRun: uv run --no-project python {GENERATOR}",
                file=sys.stderr,
            )
            return 1
        print(
            f"gen_builtin_skills_manifest: OK — {output} is up to date "
            f"({payload['count']} files, {payload['total_bytes']} bytes)"
        )
        return 0 if ok else 1

    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(text, encoding="utf-8")
    print(
        f"gen_builtin_skills_manifest: wrote {output} "
        f"({payload['count']} files, {payload['total_bytes']} bytes, "
        f"manifest sha256 {hashlib.sha256(text.encode()).hexdigest()})"
    )
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
