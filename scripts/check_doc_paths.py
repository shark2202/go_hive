#!/usr/bin/env python3
"""Check backticked source paths in the six crosswalk docs.

Scope: go_hive/02-architecture.md, 03-agent-loop.md, 04-colony-runtime.md,
05-tools-and-integrations.md, 07-memory-and-skills.md, 08-storage-and-data.md.

Extracts only backtick-wrapped paths that start with ``core/framework/``,
``core/frontend/`` or ``internal/``. Globs (``*``, ``{}``) and decorative
suffixes (``(...)``, ``::Symbol``, ``:123``) are ignored/normalised.

Resolution:
  internal/...        relative to go_hive/go-hive/ (the Go module root)
  core/framework/...  relative to the repository root
  core/frontend/...   relative to the repository root

Usage:
  python3 go_hive/scripts/check_doc_paths.py           # report only
  python3 go_hive/scripts/check_doc_paths.py --check   # exit 1 on broken links
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
GO_ROOT = REPO_ROOT / "go_hive" / "go-hive"

DOC_FILES = [
    "go_hive/02-architecture.md",
    "go_hive/03-agent-loop.md",
    "go_hive/04-colony-runtime.md",
    "go_hive/05-tools-and-integrations.md",
    "go_hive/07-memory-and-skills.md",
    "go_hive/08-storage-and-data.md",
]

BACKTICK_RE = re.compile(r"`([^`\n]+)`")
PREFIXES = ("core/framework/", "core/frontend/", "internal/")
DECORATION_RE = re.compile(r"\s*\([^)]*\)\s*$")
SYMBOL_RE = re.compile(r"::.*$")
LINE_SUFFIX_RE = re.compile(r":\d+$")
TRAILING_PUNCT = "，。；、,;"


def normalise(raw: str) -> str | None:
    """Return a checkable path, or None when the token is not in scope."""
    path = raw.strip()
    if not any(path.startswith(prefix) for prefix in PREFIXES):
        return None
    # Decorative suffixes: "foo.py（2,935 行）" uses full-width parens, so
    # handle both ASCII and CJK parentheses before the generic glob filters.
    path = DECORATION_RE.sub("", path)
    if path.endswith("）"):
        head, sep, _ = path.rpartition("（")
        if sep:
            path = head.strip()
    path = SYMBOL_RE.sub("", path)
    path = LINE_SUFFIX_RE.sub("", path)
    path = path.rstrip(TRAILING_PUNCT + " ")
    if not path:
        return None
    if any(ch in path for ch in "*{}"):
        return None
    if any(ch.isspace() for ch in path):
        return None
    return path


def resolve(path: str) -> Path:
    if path.startswith("internal/"):
        return GO_ROOT / path
    return REPO_ROOT / path


def scan() -> tuple[int, list[tuple[str, int, str]]]:
    checked = 0
    missing: list[tuple[str, int, str]] = []
    for rel in DOC_FILES:
        doc = REPO_ROOT / rel
        if not doc.is_file():
            missing.append((rel, 0, "<missing doc file>"))
            continue
        for lineno, line in enumerate(doc.read_text(encoding="utf-8").splitlines(), 1):
            for match in BACKTICK_RE.finditer(line):
                path = normalise(match.group(1))
                if path is None:
                    continue
                checked += 1
                if not resolve(path).exists():
                    missing.append((rel, lineno, path))
    return checked, missing


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check",
        action="store_true",
        help="exit non-zero when any referenced path is missing",
    )
    args = parser.parse_args()

    checked, missing = scan()
    print(f"docs: {len(DOC_FILES)}")
    print(f"checked paths: {checked}")
    if missing:
        print(f"missing: {len(missing)}")
        for rel, lineno, path in missing:
            where = f"{rel}:{lineno}" if lineno else rel
            print(f"  {where}: {path}")
    else:
        print("missing: 0")
    return 1 if (args.check and missing) else 0


if __name__ == "__main__":
    sys.exit(main())
