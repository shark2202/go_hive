#!/usr/bin/env python3
"""Generate non-empty (rich) legacy v1 / v2 HIVE_HOME fixtures.

Companion to ``storage_compat_gen_fixture.py``: same idea (build the exact
old-layout tree both the Python and the Go migration/read paths will see), but
every artifact carries realistic content across the full data surface the G2
lossless-migration run checks:

  sessions      several DM / overseer / worker sessions, each with
                meta.json + state.json + conversations/parts (tool calls,
                tool results with is_skill_content, reasoning) + the
                summary.json sidecar + events.jsonl / artifacts
  colonies      metadata / worker / tools / triggers / skills sidecars,
                colony.db playbook runs, tracker DBs with registered user
                tables, a change log and rows
  tracker       user_version < 3 registries (trigger backfill) and
                user_version 0 (full bootstrap) cases
  memories      memories/global, memories/agents/queens/<q>,
                memories/colonies/<c>
  senders       senders/senders.json (enabled + disabled row, fake secrets)
  skills        $HIVE_HOME/skills user skill, queen skills, colony skills
  credentials   written by the harness with the fixed Fernet key
                (``--fresh-summaries`` also normalises summary.json mtimes so
                neither side rebuilds the sidecar during read probes)

``Absolute paths are written with the ``__HIVE_HOME__`` placeholder; run
``--fix-paths <home>`` (from the base module) after copying a fixture.

Usage:
  python3 g2_home_fixture.py --dest /tmp/fx --version v2-rich
  python3 g2_home_fixture.py --fix-paths /tmp/fx
  python3 g2_home_fixture.py --manifest /tmp/fx
  python3 g2_home_fixture.py --fresh-summaries /tmp/fx
"""

from __future__ import annotations

import argparse
import json
import os
import sqlite3
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from storage_compat_gen_fixture import (  # noqa: E402  (same-directory import)
    COLONY_DB_SCHEMA,
    HOME_TOKEN,
    fix_paths,
    manifest,
    write_json,
    write_text,
)

# ---------------------------------------------------------------------------
# shared builders
# ---------------------------------------------------------------------------

SKILL_MD = """---
name: {name}
description: {description}
---

# {title}

{body}
"""


def rich_state(
    session_id: str,
    goal_id: str,
    agent_id: str,
    *,
    status: str = "paused",
    started: str = "2026-02-01T09:00:00+00:00",
    updated: str = "2026-02-01T09:30:00+00:00",
    completed: str | None = None,
    current_node: str | None = "start",
    steps: int = 3,
    tokens: int = 812,
) -> dict:
    """SessionState-shaped state.json (same schema as the base fixture)."""
    return {
        "schema_version": "1.1",
        "session_id": session_id,
        "stream_id": "",
        "correlation_id": "",
        "status": status,
        "goal_id": goal_id,
        "agent_id": agent_id,
        "entry_point": "start",
        "timestamps": {
            "started_at": started,
            "updated_at": updated,
            "completed_at": completed,
            "paused_at_time": None,
        },
        "progress": {
            "current_node": current_node,
            "paused_at": None,
            "resume_from": None,
            "steps_executed": steps,
            "total_tokens": tokens,
            "total_latency_ms": 4321,
            "path": ["start"],
            "total_retries": 1,
            "nodes_with_failures": [],
            "retry_details": {},
            "had_partial_failures": False,
            "execution_quality": "clean",
            "node_visit_counts": {"start": 1},
        },
        "result": {"success": True, "error": None, "output": {"summary": "ok"}},
        "data_buffer": {"lead_id": "l1"},
        "metrics": {
            "decision_count": 1,
            "problem_count": 0,
            "total_input_tokens": 512,
            "total_output_tokens": 300,
            "nodes_executed": ["start"],
            "edges_traversed": [],
        },
        "problems": [],
        "decisions": [],
        "input_data": {},
        "current_run_id": None,
        "pid": None,
        "isolation_level": "shared",
        "checkpoint_enabled": False,
        "latest_checkpoint_id": None,
        "active_triggers": [],
        "trigger_tasks": {},
        "worker_configured": False,
        "duration_ms": 0,
        "is_resumable": status in ("paused",),
        "is_resumable_from_checkpoint": False,
    }


def _part(seq: int, role: str, content, **extra) -> dict:
    part = {"seq": seq, "role": role, "content": content}
    part.update(extra)
    return part


def _text(content) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return " ".join(b.get("text", "") for b in content if isinstance(b, dict) and b.get("type") == "text")
    return ""


def is_client_facing(part: dict) -> bool:
    if part.get("is_transition_marker"):
        return False
    if part["role"] == "tool":
        return False
    if part["role"] == "assistant" and part.get("tool_calls"):
        return False
    return True


def rich_conversation(session_dir: Path, parts: list[dict], *, last_active_at: float, phase_id: str = "start") -> None:
    """Write conversations/{meta,cursor,parts} plus a summary.json sidecar.

    The summary mirrors what the writers produce (message_count / last_message
    / last_active_at / last_part_seq) so neither side has to rebuild it.
    """
    conv = session_dir / "conversations"
    write_json(
        conv / "meta.json",
        {
            "system_prompt": "You are Hive.",
            "max_context_tokens": 180000,
            "compaction_threshold": 0.8,
            "compaction_buffer_tokens": None,
            "compaction_buffer_ratio": None,
            "compaction_warning_buffer_tokens": None,
            "output_keys": None,
        },
    )
    write_json(conv / "cursor.json", {"next_seq": max(p["seq"] for p in parts) + 1})
    for part in parts:
        payload = dict(part)
        payload.setdefault("phase_id", phase_id)
        write_json(conv / "parts" / f"{part['seq']:010d}.json", payload)

    facing = [p for p in parts if is_client_facing(p)]
    last_message = None
    for part in reversed(facing):
        if part["role"] == "assistant":
            text = _text(part["content"]).strip()
            if text:
                last_message = text[:120]
                break
    write_json(
        session_dir / "summary.json",
        {
            "message_count": len(facing),
            "last_message": last_message,
            "last_active_at": last_active_at,
            "last_part_seq": max(p["seq"] for p in parts),
        },
    )


def session_files(
    session_dir: Path,
    session_id: str,
    *,
    meta: dict,
    state: dict,
    parts: list[dict],
    last_active_at: float,
    events: list[dict] | None = None,
) -> None:
    write_json(session_dir / "meta.json", meta)
    write_json(session_dir / "state.json", state)
    rich_conversation(session_dir, parts, last_active_at=last_active_at)
    if events:
        write_text(session_dir / "events.jsonl", "".join(json.dumps(e) + "\n" for e in events))


def make_tracker_db(
    path: Path,
    *,
    user_version: int,
    registered: list[tuple[str, str, str, str]],
    tables: dict[str, tuple[str, list[tuple]]],
    meta: dict[str, str] | None = None,
    changes: list[tuple[int, str, str, str, str]] | None = None,
) -> None:
    """Old-style tracker DB: user tables + registry, no change triggers.

    ``registered`` rows are (table, write_columns_json, key_columns_json,
    mode). ``tables`` maps table name to (CREATE TABLE sql, rows). Trigger
    install / schema bootstrap is left to the two implementations under test.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    con = sqlite3.connect(str(path))
    try:
        con.execute("PRAGMA user_version = %d" % user_version)
        for name, (ddl, rows) in tables.items():
            con.execute(ddl)
            for row in rows:
                con.execute(f'INSERT INTO "{name}" VALUES ({", ".join("?" * len(row))})', row)
        if registered or meta is not None or changes:
            con.execute(
                "CREATE TABLE IF NOT EXISTS _tracker_registry ("
                " table_name TEXT PRIMARY KEY, write_columns TEXT NOT NULL,"
                " key_columns TEXT NOT NULL, mode TEXT NOT NULL,"
                " registered_at TEXT NOT NULL)"
            )
        for table, write_columns, key_columns, mode in registered:
            con.execute(
                "INSERT OR REPLACE INTO _tracker_registry VALUES (?, ?, ?, ?, ?)",
                (table, write_columns, key_columns, mode, "2026-02-01T09:00:00+00:00"),
            )
        if meta is not None:
            con.execute(
                "CREATE TABLE IF NOT EXISTS _tracker_meta ("
                " key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL)"
            )
            for key, value in meta.items():
                con.execute(
                    "INSERT OR REPLACE INTO _tracker_meta VALUES (?, ?, ?)",
                    (key, value, "2026-02-01T09:00:00+00:00"),
                )
        if changes:
            con.execute(
                "CREATE TABLE IF NOT EXISTS _tracker_changes ("
                " id INTEGER PRIMARY KEY, table_name TEXT NOT NULL, pk TEXT NOT NULL,"
                " op TEXT NOT NULL, changed_at TEXT NOT NULL)"
            )
            con.execute("CREATE INDEX IF NOT EXISTS _tracker_changes_table_id ON _tracker_changes (table_name, id)")
            for row in changes:
                con.execute("INSERT INTO _tracker_changes VALUES (?, ?, ?, ?, ?)", row)
        con.commit()
    finally:
        con.close()


def make_colony_db(path: Path, runs: list[tuple]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    con = sqlite3.connect(str(path))
    try:
        con.execute("PRAGMA journal_mode = WAL")
        con.executescript(COLONY_DB_SCHEMA)
        con.executemany("INSERT OR REPLACE INTO playbook_runs VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", runs)
        con.commit()
    finally:
        con.close()


WORKER_JSON = {
    "description": "Colony worker",
    "goal": {"description": "Work on tasks for colony alpha"},
    "input_data": {
        "colony_data_dir": "legacy-value",
        "db_path": "legacy-value",
    },
    "name": "Alpha Worker",
    "spawned_at": "2026-02-01T09:00:00+00:00",
    "system_prompt": "You are a colony worker.",
    "tools": ["tracker_query", "tracker_upsert"],
}

SENDERS = {
    "version": 1,
    "senders": [
        {
            "auth_type": "api_key",
            "can_receive": True,
            "created_at": "2026-01-05T09:00:00+00:00",
            "daily_limit": 250,
            "enabled": True,
            "from_email": "ops@example.com",
            "from_name": "Ops",
            "id": "snd_smtp_ops",
            "name": "Ops SMTP",
            "provider": "smtp",
            "secret": {"password": "fake-smtp-password"},
            "tags": ["ops", "compat"],
            "weight": 100,
        },
        {
            "auth_type": "oauth",
            "can_receive": False,
            "created_at": "2026-01-06T10:00:00+00:00",
            "daily_limit": None,
            "enabled": False,
            "from_email": "sales@example.com",
            "integration_id": "int_google_1",
            "id": "snd_google_sales",
            "name": "Sales",
            "provider": "google",
            "secret": {"refresh_token": "fake-refresh-token"},
            "tags": ["sales"],
            "weight": 50,
        },
    ],
}


def tasks_doc(goal: str, tasks: list[dict]) -> dict:
    return {
        "meta": {"role": "session", "creator_agent_id": "queen_growth", "goal": goal, "created_at": 1767240000.0, "schema_version": 1},
        "highwatermark": max((t["id"] for t in tasks), default=0) + 1,
        "tasks": tasks,
    }


def task_row(task_id: int, subject: str, *, status: str = "pending", active_form: str | None = None, owner: str | None = None) -> dict:
    return {
        "id": task_id,
        "subject": subject,
        "description": f"Work item: {subject}",
        "active_form": active_form,
        "owner": owner,
        "status": status,
        "blocks": [],
        "blocked_by": [],
        "metadata": {"source": "fixture"},
        "created_at": 1767240000.0 + task_id,
        "updated_at": 1767240600.0 + task_id,
    }


def write_common_extras(home: Path) -> None:
    """Layout-version-independent data: skills / senders / memories bits."""
    write_json(home / "configuration.json", {"llm": {"provider": "anthropic", "model": "claude-sonnet-4-20250514"}})
    write_json(home / "trusted_repos.json", {"repos": ["https://github.com/aden-hive/hive"]})
    write_json(home / "senders" / "senders.json", SENDERS)
    write_text(
        home / "skills" / "house-style" / "SKILL.md",
        SKILL_MD.format(
            name="house-style",
            description="Team writing conventions",
            title="House style",
            body="- Write plainly.\n- Cite the source row ids.\n",
        ),
    )
    write_text(
        home / "skills" / "incident-runbook" / "SKILL.md",
        SKILL_MD.format(
            name="incident-runbook",
            description="How to run an incident",
            title="Incident runbook",
            body="1. Declare.\n2. Triage.\n3. Write the postmortem.\n",
        ),
    )
    write_text(home / "skills" / "incident-runbook" / "scripts" / "collect.sh", "#!/usr/bin/env bash\nset -euo pipefail\necho collect\n")
    write_text(home / "memories" / "global" / "global.md", "# Global memory\n\n- Prefers concise, sourced answers\n- Works in CST\n")
    write_text(home / "memories" / "global" / "work.md", "# Work memory\n\n- Ship the storage parity lane\n- Track leads in the colony tracker\n")


# ---------------------------------------------------------------------------
# v1-rich
# ---------------------------------------------------------------------------


def build_v1_rich(dest: Path) -> None:
    home = dest
    write_common_extras(home)
    write_text(home / ".install_notice_shown", "1\n")

    # exports/<colony> -> colonies/<name> (migrate_v2 copytree). The source
    # tree must survive the copy, so the rich content lives here.
    legacy = home / "exports" / "legacy_colony"
    write_json(legacy / "metadata.json", {"name": "legacy_colony", "created_at": 1767225600.0, "queen_name": "default"})
    write_json(legacy / "worker.json", {"name": "Legacy Worker", "description": "Colony legacy_colony", "tools": ["web_search"], "goal": {"description": "Work on tasks for colony legacy_colony"}})
    write_text(legacy / "skills" / "briefing" / "SKILL.md", SKILL_MD.format(name="briefing", description="Daily briefing", title="Briefing", body="Summarise the pipeline.\n"))
    make_tracker_db(
        legacy / "data" / "tracker.db",
        user_version=0,
        registered=[("deals", '["amount", "stage"]', '["id"]', "upsert")],
        tables={
            "deals": (
                "CREATE TABLE deals (id TEXT PRIMARY KEY, account TEXT, amount INTEGER, stage TEXT)",
                [("d1", "Acme", 42000, "qualified"), ("d2", "Globex", 18000, "discovery")],
            )
        },
    )

    ops = home / "exports" / "ops_colony"
    write_json(ops / "metadata.json", {"name": "ops_colony", "created_at": 1767225600.0, "queen_name": "default"})
    write_json(ops / "worker.json", {"name": "Ops Worker", "description": "Colony ops_colony", "tools": [], "goal": {"description": "Work on tasks for colony ops_colony"}})
    make_tracker_db(ops / "data" / "tracker.db", user_version=0, registered=[], tables={})

    # queen/session/<sid> -> agents/queens/default/sessions/<sid> (migrate_v2),
    # then agents/queens/default -> queens/default (migrate_v3).
    sid1 = "session_20260101_000001_aaaaaaaa"
    qdir1 = home / "queen" / "session" / sid1
    write_json(
        qdir1 / "tasks.json",
        tasks_doc("goal_v1_a", [task_row(1, "Review the deals pipeline"), task_row(2, "Send Acme a recap", status="completed")]),
    )
    session_files(
        qdir1,
        sid1,
        meta={"session_id": sid1, "created_at": "2026-01-01T09:00:00+00:00", "updated_at": "2026-01-01T09:20:00+00:00", "queen_id": "default"},
        state=rich_state(sid1, "goal_v1_a", "default", started="2026-01-01T09:00:00+00:00", updated="2026-01-01T09:20:00+00:00"),
        parts=[
            _part(0, "user", "Pull the deals table.", is_client_input=True),
            _part(
                1,
                "assistant",
                "Checking the tracker now.",
                tool_calls=[{"id": "call_a", "type": "function", "function": {"name": "tracker_query", "arguments": "{\"sql\": \"SELECT * FROM deals\"}"}}],
                reasoning_content="The deals table holds the pipeline.",
            ),
            _part(2, "tool", '{"ok": true, "kind": "rows", "columns": ["id", "account", "amount", "stage"], "rows": [["d1", "Acme", 42000, "qualified"]], "rowcount": 1, "truncated": false}', tool_use_id="call_a"),
            _part(3, "assistant", "Acme is qualified at 42k."),
        ],
        last_active_at=1767264000.0,
        events=[{"seq": 1, "type": "loop_state_changed", "at": "2026-01-01T09:05:00+00:00"}, {"seq": 2, "type": "session_paused", "at": "2026-01-01T09:20:00+00:00"}],
    )

    sid2 = "session_20260101_000002_bbbbbbbb"
    qdir2 = home / "queen" / "session" / sid2
    session_files(
        qdir2,
        sid2,
        meta={"session_id": sid2, "created_at": "2026-01-02T10:00:00+00:00", "updated_at": "2026-01-02T10:05:00+00:00", "queen_id": "default"},
        state=rich_state(
            sid2,
            "goal_v1_b",
            "default",
            status="completed",
            started="2026-01-02T10:00:00+00:00",
            updated="2026-01-02T10:05:00+00:00",
            completed="2026-01-02T10:05:00+00:00",
            steps=5,
        ),
        parts=[
            _part(0, "user", "Wrap up the pipeline review.", is_client_input=True),
            _part(1, "assistant", "Review wrapped; notes are in the tracker."),
        ],
        last_active_at=1767348300.0,
    )

    # queen/global_memory -> memories/global (migrate_v2 copies).
    write_text(home / "queen" / "global_memory" / "global.md", "# Global memory (v1)\n\n- Prefers simple answers\n")
    write_text(home / "queen" / "global_memory" / "projects.md", "# Projects\n\n- pipeline review\n- hiring loop\n")


# ---------------------------------------------------------------------------
# v2-rich
# ---------------------------------------------------------------------------


def build_v2_rich(dest: Path) -> None:
    home = dest
    write_text(home / ".migrated-v2", "1\n")
    write_common_extras(home)
    write_json(home / "mcp_registry" / "installed.json", {"servers": [{"name": "github", "enabled": True}, {"name": "notion", "enabled": False}]})

    queens = home / "agents" / "queens"

    # ---- queen_growth: profile + skills sidecars --------------------------
    write_text(queens / "queen_growth" / "profile.yaml", "name: queen_growth\nrole: growth\nvoice: direct\n")
    write_json(queens / "queen_growth" / "tools.json", {"enabled_mcp_tools": None, "updated_at": "2026-02-01T09:00:00+00:00", "saved_on_version": "1.0.14"})
    write_json(queens / "queen_growth" / "skills_overrides.json", {"briefing": {"enabled": True}, "pipeline-review": {"enabled": False}})
    write_text(queens / "queen_growth" / "skills" / "briefing" / "SKILL.md", SKILL_MD.format(name="briefing", description="Daily briefing", title="Briefing", body="Summarise the pipeline every morning.\n"))
    write_text(queens / "queen_growth" / "skills" / "pipeline-review" / "SKILL.md", SKILL_MD.format(name="pipeline-review", description="Weekly pipeline review", title="Pipeline review", body="Compare tracked leads with last week's snapshot.\n"))

    # ---- DM session with tool traffic + sidecar --------------------------
    dm = "session_20260201_000001_aaa11111"
    write_json(
        queens / "queen_growth" / "sessions" / dm / "tasks.json",
        tasks_doc("goal_dm_a", [task_row(1, "Pull the pipeline", status="completed"), task_row(2, "Draft the Acme follow-up", active_form="Drafting the Acme follow-up", owner="queen_growth")]),
    )
    session_files(
        queens / "queen_growth" / "sessions" / dm,
        dm,
        meta={"session_id": dm, "created_at": "2026-02-01T09:00:00+00:00", "updated_at": "2026-02-01T09:30:00+00:00", "queen_id": "queen_growth", "title": "Pipeline check", "channel": "desktop"},
        state=rich_state(dm, "goal_dm_a", "queen_growth", updated="2026-02-01T09:30:00+00:00"),
        parts=[
            _part(0, "user", "Morning - what is on the pipeline?", is_client_input=True),
            _part(
                1,
                "assistant",
                "Pulling the tracker rows.",
                tool_calls=[{"id": "call_1", "type": "function", "function": {"name": "tracker_query", "arguments": "{\"sql\": \"SELECT * FROM leads\"}"}}],
                reasoning_content="Needs the leads table.",
            ),
            _part(2, "tool", '{"ok": true, "kind": "rows", "columns": ["id", "name", "stage"], "rows": [["l1", "Acme", "qualified"], ["l2", "Globex", "discovery"]], "rowcount": 2, "truncated": false}', tool_use_id="call_1", is_skill_content=True, spillover_path=f"{HOME_TOKEN}/queens/queen_growth/sessions/{dm}/artifacts/leads.json"),
            _part(3, "assistant", "Acme is qualified at 42k; Globex is still early."),
            _part(4, "user", "Draft the Acme follow-up.", is_client_input=True),
            _part(5, "assistant", "Drafted: recap the pilot scope and propose Thursday."),
        ],
        last_active_at=1767226200.0,
        events=[{"seq": 1, "type": "loop_state_changed", "at": "2026-02-01T09:10:00+00:00"}, {"seq": 2, "type": "tool_call", "at": "2026-02-01T09:12:00+00:00"}],
    )
    write_text(
        queens / "queen_growth" / "sessions" / dm / "artifacts" / "leads.json",
        json.dumps({"leads": [{"id": "l1", "name": "Acme"}, {"id": "l2", "name": "Globex"}]}, indent=2),
    )

    # ---- completed DM session -------------------------------------------
    dm2 = "session_20260201_000002_bbb22222"
    session_files(
        queens / "queen_growth" / "sessions" / dm2,
        dm2,
        meta={"session_id": dm2, "created_at": "2026-02-01T11:00:00+00:00", "updated_at": "2026-02-01T11:15:00+00:00", "queen_id": "queen_growth"},
        state=rich_state(
            dm2,
            "goal_dm_b",
            "queen_growth",
            status="completed",
            started="2026-02-01T11:00:00+00:00",
            updated="2026-02-01T11:15:00+00:00",
            completed="2026-02-01T11:15:00+00:00",
            steps=4,
        ),
        parts=[
            _part(0, "user", "Close out the Globex thread.", is_client_input=True),
            _part(1, "assistant", "Globex thread closed; updated the tracker row."),
        ],
        last_active_at=1767237300.0,
    )

    # ---- overseer session bound to colonies/alpha via agent_path --------
    ov = "session_20260201_000003_ccc33333"
    session_files(
        queens / "queen_growth" / "sessions" / ov,
        ov,
        meta={"session_id": ov, "created_at": "2026-02-01T12:00:00+00:00", "updated_at": "2026-02-01T12:20:00+00:00", "queen_id": "queen_growth", "agent_path": f"{HOME_TOKEN}/colonies/alpha", "colony_id": "alpha", "agent_name": "Alpha"},
        state=rich_state(ov, "goal_ov", "queen_growth", started="2026-02-01T12:00:00+00:00", updated="2026-02-01T12:20:00+00:00"),
        parts=[
            _part(0, "user", "Kick off the alpha pipeline review.", is_client_input=True),
            _part(1, "assistant", "On it - reading the alpha tracker first."),
        ],
        last_active_at=1767241200.0,
    )

    # ---- queen_ops: profile + DM session --------------------------------
    write_text(queens / "queen_ops" / "profile.yaml", "name: queen_ops\nrole: ops\n")
    dm3 = "session_20260201_000004_ddd44444"
    session_files(
        queens / "queen_ops" / "sessions" / dm3,
        dm3,
        meta={"session_id": dm3, "created_at": "2026-02-01T13:00:00+00:00", "updated_at": "2026-02-01T13:10:00+00:00", "queen_id": "queen_ops"},
        state=rich_state(dm3, "goal_ops", "queen_ops", started="2026-02-01T13:00:00+00:00", updated="2026-02-01T13:10:00+00:00"),
        parts=[
            _part(0, "user", "Any invoices stuck in draft?", is_client_input=True),
            _part(1, "assistant", "One draft invoice for Globex; I flagged it."),
        ],
        last_active_at=1767244200.0,
    )

    # ---- agents/<colony>/ subtrees --------------------------------------
    write_json(queens.parent / "alpha" / "worker" / "conversations" / "meta.json", {"system_prompt": "You are Hive.", "max_context_tokens": 180000})
    write_json(queens.parent / "alpha" / "worker" / "conversations" / "cursor.json", {"next_seq": 2})
    write_json(
        queens.parent / "alpha" / "worker" / "conversations" / "parts" / "0000000000.json",
        {"seq": 0, "role": "user", "content": "Seed: open the alpha pipeline.", "is_client_input": True},
    )
    write_json(
        queens.parent / "alpha" / "worker" / "conversations" / "parts" / "0000000001.json",
        {"seq": 1, "role": "assistant", "content": "Seed acknowledged."},
    )

    write_json(queens.parent / "alpha" / "scout" / "worker.json", {"name": "Scout", "description": "Market scout", "tools": ["tracker_query", "web_search"], "goal": {"description": "scan"}})
    scout_sid = "session_20260201_000005_eee55555"
    write_json(
        queens.parent / "alpha" / "scout" / "sessions" / scout_sid / "state.json",
        rich_state(scout_sid, "goal_scout", "scout", status="completed", started="2026-02-01T14:00:00+00:00", updated="2026-02-01T14:30:00+00:00", completed="2026-02-01T14:30:00+00:00", steps=7),
    )
    write_text(queens.parent / "alpha" / "scout" / "workspace" / "runs.jsonl", '{"run_id": "run_scout_1", "status": "done"}\n{"run_id": "run_scout_2", "status": "done"}\n')

    # ---- colonies/alpha --------------------------------------------------
    alpha = home / "colonies" / "alpha"
    write_json(
        alpha / "metadata.json",
        {
            "name": "alpha",
            "created_at": 1767225600.0,
            "queen_name": "queen_growth",
            "worker_profiles": [{"name": "default", "description": "Default worker"}, {"name": "researcher", "description": "Deep research worker"}],
        },
    )
    write_json(alpha / "worker.json", WORKER_JSON)
    write_json(alpha / "tools.json", {"enabled_mcp_tools": None, "updated_at": "2026-02-01T09:00:00+00:00", "saved_on_version": "1.0.14"})
    write_json(
        alpha / "triggers.json",
        [
            {"id": "trg-1", "trigger_type": "timer", "trigger_config": {"interval_minutes": 30}, "description": "morning check", "task": "check the pipeline", "enabled": True, "last_fired_at": "2026-02-01T09:00:00+00:00", "next_due_at": "2026-02-01T09:30:00+00:00"},
            {"id": "trg-2", "trigger_type": "event", "trigger_config": {"event": "lead_added"}, "description": "enrich new leads", "task": "enrich the lead", "enabled": False, "last_fired_at": None, "next_due_at": None},
        ],
    )
    write_text(alpha / "skills" / "briefing" / "SKILL.md", SKILL_MD.format(name="briefing", description="Colony briefing", title="Colony briefing", body="Read tracker tables, write the briefing to memory.\n"))
    write_text(alpha / "skills" / "house-style" / "SKILL.md", SKILL_MD.format(name="house-style", description="Team writing conventions", title="House style", body="Plain prose; cite rows.\n"))
    make_tracker_db(
        alpha / "data" / "tracker.db",
        user_version=2,
        registered=[
            ("leads", '["name", "stage", "value"]', '["id"]', "upsert"),
            ("invoices", '["lead_id", "amount", "status"]', '["id"]', "upsert"),
            ("activity", '["lead_id", "note"]', "[]", "append"),
        ],
        tables={
            "leads": (
                "CREATE TABLE leads (id TEXT PRIMARY KEY, name TEXT, stage TEXT, value INTEGER)",
                [("l1", "Acme", "qualified", 42000), ("l2", "Globex", "discovery", 18000), ("l3", "Initech", "won", 65000)],
            ),
            "invoices": (
                "CREATE TABLE invoices (id TEXT PRIMARY KEY, lead_id TEXT, amount REAL, status TEXT)",
                [("inv-1", "l1", 12000.5, "sent"), ("inv-2", "l3", 65000.0, "paid")],
            ),
            "activity": (
                "CREATE TABLE activity (id INTEGER PRIMARY KEY, lead_id TEXT, note TEXT, occurred_at TEXT)",
                [(1, "l1", "call scheduled", "2026-02-03T10:00:00Z"), (2, "l2", "email sent", "2026-02-03T11:30:00Z")],
            ),
        },
        meta={"schema_version": "2", "crm_link": "leads", "crm_watermark": "3", "custom_key": "keep-me"},
        changes=[
            (1, "leads", '{"id": "l1"}', "insert", "2026-02-02T08:00:00Z"),
            (2, "leads", '{"id": "l1"}', "update", "2026-02-02T09:00:00Z"),
            (3, "invoices", '{"id": "inv-1"}', "insert", "2026-02-02T10:00:00Z"),
        ],
    )
    make_colony_db(
        alpha / "colony.db",
        [
            ("run-1", "pipeline-review", "done", 1, 0, None, '{"leads": 3}', '["line-1"]', "2026-02-01T09:00:00+00:00"),
            ("run-2", "invoice-followup", "failed", 1, 1, "smtp timeout", None, '["line-1"]', "2026-02-01T10:00:00+00:00"),
        ],
    )

    # Post-v3 leftover: colony_fork snapshot already live in the colony tree.
    fk_live = "session_20260202_000010_fff66666"
    write_text(alpha / "queens" / "queen_growth" / "sessions" / fk_live / "events.jsonl", '{"seq": 1, "type": "loop_state_changed"}\n')
    write_json(
        alpha / "queens" / "queen_growth" / "sessions" / fk_live / "meta.json",
        {"session_id": fk_live, "created_at": "2026-02-02T09:00:00+00:00", "updated_at": "2026-02-02T09:10:00+00:00", "colony_fork": True, "queen_id": "queen_growth", "colony_id": "alpha", "agent_path": f"{HOME_TOKEN}/colonies/alpha"},
    )
    rich_conversation(alpha / "queens" / "queen_growth" / "sessions" / fk_live, [_part(0, "user", "Forked alpha chat.", is_client_input=True)], last_active_at=1767330000.0)

    # ---- colonies/beta (minimal + legacy singular queen tree) ------------
    beta = home / "colonies" / "beta"
    write_json(beta / "metadata.json", {"name": "beta", "created_at": 1767225600.0, "queen_name": "queen_ops"})
    make_tracker_db(
        beta / "data" / "tracker.db",
        user_version=0,
        registered=[("contacts", '["name", "email"]', '["id"]', "upsert")],
        tables={
            "contacts": (
                "CREATE TABLE contacts (id TEXT PRIMARY KEY, name TEXT, email TEXT)",
                [("c1", "Ada", "ada@example.com"), ("c2", "Grace", "grace@example.com")],
            )
        },
    )
    legacy_sid = "session_20260203_000001_ggg77777"
    session_files(
        beta / "queen" / legacy_sid,
        legacy_sid,
        meta={"session_id": legacy_sid, "created_at": "2026-02-03T09:00:00+00:00", "updated_at": "2026-02-03T09:05:00+00:00", "queen_id": "queen_ops"},
        state=rich_state(legacy_sid, "goal_beta", "queen_ops", started="2026-02-03T09:00:00+00:00", updated="2026-02-03T09:05:00+00:00"),
        parts=[
            _part(0, "user", "Import the beta contacts.", is_client_input=True),
            _part(1, "assistant", "Imported 2 contacts."),
        ],
        last_active_at=1767411900.0,
    )

    # Post-v3 leftover: colony_fork session still under queens/ (no target).
    fk_move = "session_20260202_000001_hhh88888"
    session_files(
        queens / "queen_growth" / "sessions" / fk_move,
        fk_move,
        meta={"session_id": fk_move, "created_at": "2026-02-02T10:00:00+00:00", "updated_at": "2026-02-02T10:10:00+00:00", "colony_fork": True, "queen_id": "queen_growth", "colony_id": "alpha", "agent_path": f"{HOME_TOKEN}/colonies/alpha"},
        state=rich_state(fk_move, "goal_fork", "queen_growth", started="2026-02-02T10:00:00+00:00", updated="2026-02-02T10:10:00+00:00"),
        parts=[
            _part(0, "user", "Fork this chat into alpha.", is_client_input=True),
            _part(1, "assistant", "Forked."),
        ],
        last_active_at=1767334200.0,
    )

    # ---- memories --------------------------------------------------------
    write_text(home / "memories" / "agents" / "queens" / "queen_growth" / "profile.md", "# Queen growth profile\n\n- Owns growth pipeline memory\n")
    write_text(home / "memories" / "agents" / "queens" / "queen_growth" / "preferences.md", "# Preferences\n\n- Brief answers over essays\n")
    write_text(home / "memories" / "colonies" / "alpha" / "context.md", "# Alpha context\n\n- Acme is the top account\n")
    write_text(home / "memories" / "agents" / "scout" / "notes.md", "# Scout notes\n\n- Competitor pricing moves quarterly\n")


BUILDERS = {
    "v1-rich": build_v1_rich,
    "v2-rich": build_v2_rich,
}


def fresh_summaries(home: Path) -> int:
    """Touch summary.json newer than the parts dirs it caches and freeze the
    mtimes discovery reads.

    Read probes must observe the sidecar exactly as the old home had it. A
    stale summary makes both implementations rebuild (and rewrite) the file,
    which would turn a read into a write and add copy-order mtime noise.
    Session-directory mtimes feed ``discover``'s ``last_active``, so they are
    pinned to one fixed instant on both copies.
    """
    fixed = 0
    now = time.time()
    pinned = 1767312000.0  # 2026-02-02T00:00:00Z
    for summary in sorted(home.rglob("summary.json")):
        session_dir = summary.parent
        convs = session_dir / "conversations"
        dirs = [session_dir]
        if convs.is_dir():
            dirs.append(convs)
            dirs += [child for child in convs.iterdir() if child.is_dir()]
        stamp = now
        for d in dirs:
            os.utime(d, (stamp, stamp))
        os.utime(summary, (stamp + 30, stamp + 30))
        os.utime(session_dir, (pinned, pinned))
        fixed += 1
    # Session dirs without a sidecar (worker sessions) also feed discovery.
    for pattern in (
        "queens/*/sessions/*",
        "colonies/*/queens/*/sessions/*",
        "colonies/*/workers/*/sessions/*",
        "agents/*/queens/*/sessions/*",
        "agents/*/*/sessions/*",
    ):
        for d in sorted(home.glob(pattern)):
            if d.is_dir():
                os.utime(d, (pinned, pinned))
    return fixed


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dest")
    parser.add_argument("--version", choices=sorted(BUILDERS))
    parser.add_argument("--fix-paths", metavar="HOME")
    parser.add_argument("--manifest", metavar="HOME")
    parser.add_argument("--fresh-summaries", metavar="HOME")
    args = parser.parse_args()

    if args.fix_paths:
        print(json.dumps({"fixed": fix_paths(Path(args.fix_paths))}))
        return 0
    if args.manifest:
        print(json.dumps(manifest(Path(args.manifest)), indent=2, sort_keys=True))
        return 0
    if args.fresh_summaries:
        print(json.dumps({"summaries": fresh_summaries(Path(args.fresh_summaries))}))
        return 0
    if not args.dest or not args.version:
        parser.error("--dest and --version are required to build a fixture")
    dest = Path(args.dest)
    if dest.exists():
        print(f"refusing to overwrite existing {dest}", file=sys.stderr)
        return 1
    BUILDERS[args.version](dest)
    print(json.dumps({"built": args.version, "dest": str(dest), "files": sum(1 for _ in dest.rglob("*") if _.is_file())}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
