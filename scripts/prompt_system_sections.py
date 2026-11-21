#!/usr/bin/env python3
"""Attribute the first-turn system-prompt diff to sections.

Reads the two canonicalized captures produced by prompt_system_parity.py and
writes a machine-readable + human-readable section table:

  * the byte-identical common prefix (queen identity + soul),
  * Python-only sections (queen base prompt + credentials block),
  * Go-only sections (tool-gated pre-activated skill bodies).

Usage:
    uv run --offline python go_hive/scripts/prompt_system_sections.py \
        --python <dir>/system-prompt.python.txt --go <dir>/system-prompt.go.txt \
        --out <dir>
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

PYTHON_SECTIONS = [
    ("queen_character_core", "Before every response, internally calibrate"),
    ("queen_role_independent", "You are in INDEPENDENT mode"),
    ("queen_tools_independent", "# Tools"),
    ("queen_system_rules", "# System Rules"),
    ("queen_communication", "## Communicating with the user"),
    ("queen_memory_instructions", "## Your Memory"),
    ("credentials_block", "# Credentials"),
]

GO_SECTION_RE = re.compile(r"(?=--- Pre-Activated Skill:)")


def common_prefix(left: str, right: str) -> int:
    limit = min(len(left), len(right))
    index = 0
    while index < limit and left[index] == right[index]:
        index += 1
    return index


def python_sections(text: str, start: int) -> list[dict]:
    tail = text[start:]
    found = []
    for name, marker in PYTHON_SECTIONS:
        offset = tail.find(marker)
        if offset >= 0:
            found.append({"name": name, "marker": marker, "offset": offset})
    found.sort(key=lambda entry: entry["offset"])
    sections = []
    for position, entry in enumerate(found):
        end = found[position + 1]["offset"] if position + 1 < len(found) else len(tail)
        sections.append(
            {
                "name": entry["name"],
                "chars": end - entry["offset"],
                "preview": tail[entry["offset"] : entry["offset"] + 90].splitlines()[0],
            }
        )
    return sections


def go_sections(text: str, start: int) -> list[dict]:
    tail = text[start:]
    parts = [part for part in GO_SECTION_RE.split(tail) if part]
    sections = []
    for part in parts:
        first_line = part.strip().splitlines()[0] if part.strip() else "(preamble)"
        name = "preamble"
        match = re.match(r"--- Pre-Activated Skill: (.+?) ---", first_line)
        if match:
            name = "pre_activated:" + match.group(1).strip()
        sections.append({"name": name, "chars": len(part), "preview": first_line[:120]})
    return sections


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--python", required=True)
    parser.add_argument("--go", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    python_text = Path(args.python).read_text(encoding="utf-8")
    go_text = Path(args.go).read_text(encoding="utf-8")
    prefix = common_prefix(python_text, go_text)
    cjk_pattern = re.compile(r"[\u4e00-\u9fff]")
    report = {
        "python_chars": len(python_text),
        "go_chars": len(go_text),
        "common_prefix_chars": prefix,
        "common_prefix_tail": {
            "python": python_text[prefix : prefix + 80],
            "go": go_text[prefix : prefix + 80],
        },
        "python_only_sections": python_sections(python_text, prefix),
        "go_only_sections": go_sections(go_text, prefix),
        "cjk_chars": {"python": len(cjk_pattern.findall(python_text)), "go": len(cjk_pattern.findall(go_text))},
    }
    report["python_only_chars"] = sum(section["chars"] for section in report["python_only_sections"])
    report["go_only_chars"] = sum(section["chars"] for section in report["go_only_sections"])

    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    (out / "system-prompt.sections.json").write_text(
        json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )

    lines = [
        "first-turn system prompt — section attribution (canonicalized)",
        f"  python={report['python_chars']} chars  go={report['go_chars']} chars",
        f"  common prefix (queen identity + soul): {prefix} chars, byte-identical",
        "",
        f"  Python-only sections ({report['python_only_chars']} chars):",
    ]
    for section in report["python_only_sections"]:
        lines.append(f"    {section['chars']:>6}  {section['name']}")
    lines.append("")
    lines.append(f"  Go-only sections ({report['go_only_chars']} chars):")
    for section in report["go_only_sections"]:
        lines.append(f"    {section['chars']:>6}  {section['name']}")
    lines.append("")
    lines.append(f"  CJK characters: python={report['cjk_chars']['python']} go={report['cjk_chars']['go']}")
    text = "\n".join(lines) + "\n"
    (out / "system-prompt.sections.txt").write_text(text, encoding="utf-8")
    print(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
