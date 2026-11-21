#!/usr/bin/env python3
"""Render the Python reference side of the prompt/skill-catalog parity probe.

Python half of ``prompt_catalog_parity.py``. The driver materializes the shared
fixture (``go_hive/go-hive/internal/skills/testdata/prompt-catalog/fixture.json``)
into a scratch HIVE_HOME/HOME pair and invokes this script once per profile with
HOME / HIVE_HOME exported (``framework.config`` reads HIVE_HOME at import time).

It builds the same ``SkillsManager`` a queen chat session builds (see
``framework/server/queen_orchestrator.py``) and writes the catalog text for each
phase, plus the description-cap table and the reminder wrapper. Every
machine-specific path is canonicalized to ``<BUNDLED>`` / ``<HIVE_HOME>`` /
``<HOME>`` so the output is byte-comparable with the Go capture.

Usage:
    uv run --offline python go_hive/scripts/prompt_catalog_render.py \
        --fixture <fixture.json> --profile small --out <dir>
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

import framework.skills as framework_skills
from framework.agent_loop.reminders import wrap_reminder
from framework.skills.catalog import SkillCatalog
from framework.skills.discovery import ExtraScope
from framework.skills.manager import SkillsManager, SkillsManagerConfig
from framework.skills.parser import ParsedSkill

PHASES = ("none", "independent", "colony")
QUEEN_ID = "queen_growth"
SYNTHETIC_SKILL_ROOT = "/fixture/skills"


def env_path(name: str) -> Path:
    value = os.environ.get(name, "").strip()
    if not value:
        raise SystemExit(f"{name} must be set by the driver")
    # resolve() matches what the skill parser stores in ParsedSkill.location
    # (macOS /tmp is /private/tmp; the replacement must target the real path).
    return Path(value).resolve()


def queen_manager(hive: Path) -> SkillsManager:
    """The exact SkillsManagerConfig a queen chat session builds."""
    queen_home = hive / "queens" / QUEEN_ID
    manager = SkillsManager(
        SkillsManagerConfig(
            queen_id=QUEEN_ID,
            queen_overrides_path=queen_home / "skills_overrides.json",
            extra_scope_dirs=[
                ExtraScope(directory=queen_home / "skills", label="queen_ui", priority=2),
            ],
            project_root=None,
            skip_community_discovery=True,
            interactive=False,
        )
    )
    manager.load()
    return manager


def canonicalize(text: str, roots: dict[str, str]) -> str:
    for token, value in roots.items():
        if value:
            text = text.replace(value, token)
    return text


def synthetic_catalog(caps: list[dict]) -> SkillCatalog:
    skills = [
        ParsedSkill(
            name=entry["name"],
            description=entry["description"],
            location=f"{SYNTHETIC_SKILL_ROOT}/{entry['name']}/SKILL.md",
            base_dir=f"{SYNTHETIC_SKILL_ROOT}/{entry['name']}",
            source_scope="project",
            body="Body.",
        )
        for entry in caps
    ]
    return SkillCatalog(skills)


def render_turn_context_body() -> str:
    """The body one started turn-context package contributes to the reminder.

    Mirrors framework/server/turn_context.py::context_for: every started
    package's text, trimmed and joined with a blank line. The probe registers
    one provider, exactly like the Go parity test does.
    """
    import asyncio

    from framework.server import turn_context

    class _Probe:
        async def start(self) -> None:
            return None

        async def stop(self) -> None:
            return None

        async def context(self, queen_id: str, message: str) -> str:
            return "Turn-context probe body for the parity probe."

    turn_context._running["probe"] = _Probe()
    try:
        body = asyncio.run(turn_context.context_for("queen_growth", "PARITY_PROBE_MESSAGE"))
    finally:
        turn_context._running.pop("probe", None)
    return body or ""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--fixture", required=True)
    parser.add_argument("--profile", required=True, choices=["small", "edge"])
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    fixture = json.loads(Path(args.fixture).read_text(encoding="utf-8"))
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)

    home = env_path("HOME")
    hive = env_path("HIVE_HOME")
    bundled = str(Path(framework_skills.__file__).resolve().parent)
    roots = {"<BUNDLED>": bundled, "<HIVE_HOME>": str(hive), "<HOME>": str(home)}

    manager = queen_manager(hive)
    meta: dict[str, dict] = {}
    for phase in PHASES:
        if phase == "none":
            raw = manager.skills_catalog_prompt
        else:
            raw = manager.skills_catalog_prompt_for_phase(phase)
        text = canonicalize(raw, roots)
        (out / f"{args.profile}.{phase}.txt").write_text(text, encoding="utf-8")
        meta[f"{args.profile}.{phase}"] = {"chars": len(text), "raw_chars": len(raw)}

    if args.profile == "edge":
        text = synthetic_catalog(fixture["caps"]).to_prompt()
        (out / "caps.none.txt").write_text(text, encoding="utf-8")
        meta["caps.none"] = {"chars": len(text), "raw_chars": len(text)}

        compact_text = synthetic_catalog(fixture["caps_compact"]).to_prompt()
        (out / "caps.compact.txt").write_text(compact_text, encoding="utf-8")
        meta["caps.compact"] = {"chars": len(compact_text), "raw_chars": len(compact_text)}

        turn_body = render_turn_context_body()
        (out / "turn-context.txt").write_text(turn_body, encoding="utf-8")
        meta["turn-context"] = {"chars": len(turn_body), "raw_chars": len(turn_body)}

        reminder_bodies = [
            "Turn context example body for the parity probe.",
            "Searchable tools manifest example.\nbeta-tool",
        ]
        block = wrap_reminder(reminder_bodies)
        (out / "reminder.block.txt").write_text(block, encoding="utf-8")
        meta["reminder.block"] = {"chars": len(block), "raw_chars": len(block)}

    (out / f"meta.{args.profile}.json").write_text(
        json.dumps(meta, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    print(json.dumps(meta, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
