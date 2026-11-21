#!/usr/bin/env python3
"""Generate legacy (v1 / v2 / post-v3) HIVE_HOME fixtures for storage-compat runs.

The fixtures are plain directories with the same shape a real Hive install
had at each layout generation. They are built from the DDL / file shapes of
the *Python* implementation (the reference), so both the Python and the Go
migration pipelines can be pointed at an identical starting tree.

Versions:
  v1          pre-v2 layout: exports/<colony>/, queen/session/<sid>/,
              queen/global_memory/. Exercises migrate_v2 -> migrate_v3 -> colony_sessions.
  v2          pre-v3 layout: agents/queens/<q>/{profile,sessions}, agents/<colony>/,
              colonies/<c>/data/tracker.db, plus post-v3 leftovers (queens/ fork
              sessions, colonies/<c>/queen/<sid> legacy dirs) for the
              colony_sessions migration. Marker .migrated-v2 present.
  colony-fork standalone post-v3 layout focused on migrate_colony_sessions only
              (three cases: move, overwrite-empty, archive).

Absolute paths inside meta.json are written with the placeholder
``__HIVE_HOME__``; run ``--fix-paths <home>`` after copying a fixture so
``agent_path`` points at the copy that is about to be migrated.

Usage:
  python3 storage_compat_gen_fixture.py --dest /tmp/fx-v2 --version v2
  python3 storage_compat_gen_fixture.py --fix-paths /tmp/fx-v2
  python3 storage_compat_gen_fixture.py --manifest /tmp/fx-v2 > manifest.json
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sqlite3
import sys
from pathlib import Path

HOME_TOKEN = "__HIVE_HOME__"

# ---------------------------------------------------------------------------
# Python v3 tracker DDL (mirrors core/framework/host/tracker_db.py)
# ---------------------------------------------------------------------------

TRACKER_SCHEMA = """
CREATE TABLE IF NOT EXISTS _tracker_registry (
    table_name      TEXT PRIMARY KEY,
    write_columns   TEXT NOT NULL,
    key_columns     TEXT NOT NULL,
    mode            TEXT NOT NULL CHECK (mode IN ('upsert', 'append')),
    registered_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS _tracker_meta (
    key             TEXT PRIMARY KEY,
    value           TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS _tracker_changes (
    id              INTEGER PRIMARY KEY,
    table_name      TEXT NOT NULL,
    pk              TEXT NOT NULL,
    op              TEXT NOT NULL CHECK (op IN ('insert', 'update')),
    changed_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS _tracker_changes_table_id
    ON _tracker_changes (table_name, id);
"""

COLONY_DB_SCHEMA = """
CREATE TABLE IF NOT EXISTS playbook_runs (
    run_id        TEXT PRIMARY KEY,
    name          TEXT,
    status        TEXT NOT NULL,
    dispatched    INTEGER NOT NULL DEFAULT 0,
    dead_lettered INTEGER NOT NULL DEFAULT 0,
    error         TEXT,
    result_json   TEXT,
    logs_json     TEXT,
    created_at    TEXT NOT NULL
);
"""


def write_json(path: Path, data) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2), encoding="utf-8")


def write_text(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def state_json(session_id: str, goal_id: str, agent_id: str) -> dict:
    return {
        "schema_version": "1.1",
        "session_id": session_id,
        "stream_id": "",
        "correlation_id": "",
        "status": "paused",
        "goal_id": goal_id,
        "agent_id": agent_id,
        "entry_point": "start",
        "timestamps": {
            "started_at": "2026-01-01T00:00:00+00:00",
            "updated_at": "2026-01-01T00:10:00+00:00",
            "completed_at": None,
            "paused_at_time": None,
        },
        "progress": {
            "current_node": None,
            "paused_at": None,
            "resume_from": None,
            "steps_executed": 2,
            "total_tokens": 42,
            "total_latency_ms": 1234,
            "path": ["start"],
            "total_retries": 0,
            "nodes_with_failures": [],
            "retry_details": {},
            "had_partial_failures": False,
            "execution_quality": "clean",
            "node_visit_counts": {"start": 1},
        },
        "result": {"success": None, "error": None, "output": {}},
        "data_buffer": {},
        "metrics": {
            "decision_count": 0,
            "problem_count": 0,
            "total_input_tokens": 0,
            "total_output_tokens": 0,
            "nodes_executed": [],
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
        "is_resumable": False,
        "is_resumable_from_checkpoint": False,
    }


def conversation_files(session_dir: Path, phase_id: str = "start") -> None:
    """Seed conversations/{meta.json,cursor.json,parts/*}.

    Field shapes mirror the writers: meta = NodeConversation._persist_meta,
    cursor = {next_seq}, parts = Message.to_storage_dict.
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
    write_json(conv / "cursor.json", {"next_seq": 2})
    write_json(
        conv / "parts" / "0000000000.json",
        {
            "seq": 0,
            "role": "user",
            "content": "hello",
            "phase_id": phase_id,
            "is_client_input": True,
        },
    )
    write_json(
        conv / "parts" / "0000000001.json",
        {
            "seq": 1,
            "role": "assistant",
            "content": "hi there",
            "phase_id": phase_id,
        },
    )


def session_meta(session_id: str, *, agent_path: str | None = None, colony_fork: bool = False, queen_id: str = "", colony_id: str = "") -> dict:
    meta: dict = {
        "session_id": session_id,
        "created_at": "2026-01-01T00:00:00+00:00",
        "updated_at": "2026-01-01T00:10:00+00:00",
    }
    if agent_path:
        meta["agent_path"] = agent_path
    if colony_fork:
        meta["colony_fork"] = True
    if queen_id:
        meta["queen_id"] = queen_id
    if colony_id:
        meta["colony_id"] = colony_id
    return meta


def make_tracker_db(path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    con = sqlite3.connect(str(path))
    try:
        con.executescript(TRACKER_SCHEMA)
        con.execute("PRAGMA user_version = 3")
        con.execute(
            "INSERT OR REPLACE INTO _tracker_meta (key, value, updated_at) VALUES (?, ?, ?)",
            ("schema_version", "3", "2026-01-01T00:00:00+00:00"),
        )
        con.execute("CREATE TABLE IF NOT EXISTS leads (id TEXT PRIMARY KEY, name TEXT, funding INTEGER)")
        con.execute(
            "INSERT OR REPLACE INTO _tracker_registry (table_name, write_columns, key_columns, mode, registered_at) VALUES (?, ?, ?, ?, ?)",
            ("leads", json.dumps(["name", "funding"]), json.dumps(["id"]), "upsert", "2026-01-01T00:00:00+00:00"),
        )
        con.execute("INSERT OR REPLACE INTO leads (id, name, funding) VALUES ('l1', 'Acme', 100)")
        con.commit()
    finally:
        con.close()


def make_colony_db(path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    con = sqlite3.connect(str(path))
    try:
        con.execute("PRAGMA journal_mode = WAL")
        con.executescript(COLONY_DB_SCHEMA)
        con.execute(
            "INSERT OR REPLACE INTO playbook_runs VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
            ("run-1", "demo", "done", 1, 0, None, "{}", '["line"]', "2026-01-01T00:00:00+00:00"),
        )
        con.commit()
    finally:
        con.close()


# ---------------------------------------------------------------------------
# v1 fixture
# ---------------------------------------------------------------------------


def build_v1(dest: Path) -> None:
    home = dest
    write_json(home / "configuration.json", {"llm": {"provider": "anthropic", "model": "claude-sonnet-4-20250514"}})
    write_json(home / "trusted_repos.json", {"repos": []})

    # exports/<colony> — copied to colonies/<name> by migrate_v2.
    colony = home / "exports" / "legacy_colony"
    write_json(colony / "metadata.json", {"name": "legacy_colony", "created_at": 1767225600.0, "queen_name": "default"})
    write_json(
        colony / "worker.json",
        {"name": "Legacy Colony", "description": "Colony legacy_colony", "tools": [], "goal": {"description": "Work on tasks for colony legacy_colony"}},
    )
    make_tracker_db(colony / "data" / "tracker.db")

    # queen/session/<sid> — moved to agents/queens/default/sessions/<sid>.
    qsid = "session_20260101_000001_aaaaaaaa"
    qdir = home / "queen" / "session" / qsid
    write_json(qdir / "meta.json", session_meta(qsid, queen_id="default"))
    write_json(qdir / "state.json", state_json(qsid, "goal_v1", "default"))
    conversation_files(qdir)

    # queen/global_memory — copied to memories/global.
    write_text(home / "queen" / "global_memory" / "global.md", "# Global memory\n- prefers concise answers\n")
    write_text(home / "queen" / "global_memory" / "work.md", "# Work memory\n- ship storage parity\n")


# ---------------------------------------------------------------------------
# v2 fixture (pre-v3, plus post-v3 leftovers)
# ---------------------------------------------------------------------------


def build_v2(dest: Path) -> None:
    home = dest
    write_text(home / ".migrated-v2", "1\n")
    write_json(home / "configuration.json", {"llm": {"provider": "anthropic", "model": "claude-sonnet-4-20250514"}})
    write_json(home / "trusted_repos.json", {"repos": ["https://github.com/aden-hive/hive"]})
    write_json(home / "mcp_registry" / "installed.json", {"servers": []})

    queens = home / "agents" / "queens"
    # 1) plain DM session
    dm = "session_20260201_000001_bbbbbbbb"
    write_text(queens / "queen_growth" / "profile.yaml", "name: queen_growth\nrole: growth\n")
    write_text(queens / "queen_growth" / "skills" / "briefing" / "SKILL.md", "# briefing\n")
    write_json(queens / "queen_growth" / "sessions" / dm / "meta.json", session_meta(dm, queen_id="queen_growth"))
    write_json(queens / "queen_growth" / "sessions" / dm / "state.json", state_json(dm, "goal_dm", "queen_growth"))
    conversation_files(queens / "queen_growth" / "sessions" / dm)

    # 2) overseer session bound to colonies/alpha via agent_path
    ov = "session_20260201_000002_cccccccc"
    write_json(
        queens / "queen_growth" / "sessions" / ov / "meta.json",
        session_meta(ov, agent_path=f"{HOME_TOKEN}/colonies/alpha", queen_id="queen_growth", colony_id="alpha"),
    )
    conversation_files(queens / "queen_growth" / "sessions" / ov)

    # 3) pre-v3 colony_fork session (v3 itself relocates it into the colony tree)
    fk = "session_20260201_000003_dddddddd"
    write_json(
        queens / "queen_growth" / "sessions" / fk / "meta.json",
        session_meta(fk, agent_path=f"{HOME_TOKEN}/colonies/alpha", colony_fork=True, queen_id="queen_growth", colony_id="alpha"),
    )
    conversation_files(queens / "queen_growth" / "sessions" / fk)

    # agents/<colony>/ subtrees
    write_json(
        home / "agents" / "alpha" / "worker" / "conversations" / "meta.json",
        {"system_prompt": "You are Hive.", "max_context_tokens": 180000},
    )
    write_json(home / "agents" / "alpha" / "worker" / "conversations" / "cursor.json", {"next_seq": 1})
    write_json(
        home / "agents" / "alpha" / "worker" / "conversations" / "parts" / "0000000000.json",
        {"seq": 0, "role": "user", "content": "seed", "is_client_input": True},
    )
    write_json(home / "agents" / "alpha" / "scout" / "worker.json", {"name": "Scout", "tools": ["tracker_sql"]})
    sid2 = "session_20260201_000004_eeeeeeee"
    write_json(home / "agents" / "alpha" / "scout" / "sessions" / sid2 / "state.json", state_json(sid2, "goal_scout", "scout"))

    # colonies/alpha — complete colony
    alpha = home / "colonies" / "alpha"
    write_json(
        alpha / "metadata.json",
        {
            "name": "alpha",
            "created_at": 1767225600.0,
            "queen_name": "queen_growth",
            "worker_profiles": [{"name": "default", "description": "Default worker"}],
        },
    )
    write_json(alpha / "worker.json", {"name": "Alpha Worker", "tools": [], "goal": {"description": "alpha goals"}})
    write_json(alpha / "tools.json", {"enabled_mcp_tools": None, "updated_at": "2026-01-01T00:00:00+00:00", "saved_on_version": "1.0.14"})
    write_json(
        alpha / "triggers.json",
        [
            {
                "id": "trg-1",
                "trigger_type": "timer",
                "trigger_config": {"interval_minutes": 30},
                "description": "morning check",
                "task": "check the pipeline",
                "enabled": True,
                "last_fired_at": "2026-01-01T00:00:00+00:00",
                "next_due_at": "2026-01-01T00:30:00+00:00",
            }
        ],
    )
    write_text(alpha / "skills" / "briefing" / "SKILL.md", "# colony briefing\n")
    make_tracker_db(alpha / "data" / "tracker.db")
    make_colony_db(alpha / "colony.db")

    # colonies/beta — minimal colony, tracker present, no colony.db
    beta = home / "colonies" / "beta"
    write_json(beta / "metadata.json", {"name": "beta", "created_at": 1767225600.0})
    make_tracker_db(beta / "data" / "tracker.db")

    # Post-v3 leftovers (not touched by migrate_v3):
    # (a) colony_fork session in queens/ whose colony tree has no copy -> "moved"
    fk1 = "session_20260202_000001_ffffffff"
    write_json(
        home / "queens" / "queen_growth" / "sessions" / fk1 / "meta.json",
        session_meta(fk1, agent_path=f"{HOME_TOKEN}/colonies/alpha", colony_fork=True, queen_id="queen_growth", colony_id="alpha"),
    )
    conversation_files(home / "queens" / "queen_growth" / "sessions" / fk1)
    # (b) colony_fork session whose colony copy is live (events.jsonl) -> "archived"
    fk2 = "session_20260202_000002_11111111"
    write_json(
        home / "queens" / "queen_growth" / "sessions" / fk2 / "meta.json",
        session_meta(fk2, agent_path=f"{HOME_TOKEN}/colonies/alpha", colony_fork=True, queen_id="queen_growth", colony_id="alpha"),
    )
    conversation_files(home / "queens" / "queen_growth" / "sessions" / fk2)
    write_text(home / "colonies" / "alpha" / "queens" / "queen_growth" / "sessions" / fk2 / "events.jsonl", '{"seq": 1, "type": "loop_state_changed"}\n')
    # (c) legacy singular colonies/<c>/queen/<sid>
    lsid = "session_20260203_000001_22222222"
    write_json(home / "colonies" / "beta" / "queen" / lsid / "meta.json", session_meta(lsid, queen_id="queen_ops"))
    conversation_files(home / "colonies" / "beta" / "queen" / lsid)

    # memories
    write_text(home / "memories" / "global" / "global.md", "# Global\n")
    write_text(home / "memories" / "agents" / "queens" / "queen_growth" / "profile.md", "# Queen growth profile\n")


# ---------------------------------------------------------------------------
# colony-fork-only fixture (post-v3; migrate_colony_sessions in isolation)
# ---------------------------------------------------------------------------


def build_colony_fork(dest: Path) -> None:
    home = dest
    write_text(home / ".migrated-v2", "1\n")
    write_text(home / ".migrated-v3", "1\n")
    write_json(home / "colonies" / "alpha" / "metadata.json", {"name": "alpha", "created_at": 1767225600.0})

    # case 1: move (no target)
    s1 = "session_20260301_000001_33333333"
    write_json(
        home / "queens" / "queen_growth" / "sessions" / s1 / "meta.json",
        session_meta(s1, agent_path=f"{HOME_TOKEN}/colonies/alpha", colony_fork=True, queen_id="queen_growth"),
    )
    conversation_files(home / "queens" / "queen_growth" / "sessions" / s1)

    # case 2: overwrite-empty (target exists, no events.jsonl)
    s2 = "session_20260301_000002_44444444"
    write_json(
        home / "queens" / "queen_growth" / "sessions" / s2 / "meta.json",
        session_meta(s2, agent_path=f"{HOME_TOKEN}/colonies/alpha", colony_fork=True, queen_id="queen_growth"),
    )
    conversation_files(home / "queens" / "queen_growth" / "sessions" / s2)
    write_json(home / "colonies" / "alpha" / "queens" / "queen_growth" / "sessions" / s2 / "state.json", state_json(s2, "goal_empty", "queen_growth"))

    # case 3: archive (target has a live events.jsonl)
    s3 = "session_20260301_000003_55555555"
    write_json(
        home / "queens" / "queen_growth" / "sessions" / s3 / "meta.json",
        session_meta(s3, agent_path=f"{HOME_TOKEN}/colonies/alpha", colony_fork=True, queen_id="queen_growth"),
    )
    conversation_files(home / "queens" / "queen_growth" / "sessions" / s3)
    write_text(home / "colonies" / "alpha" / "queens" / "queen_growth" / "sessions" / s3 / "events.jsonl", '{"seq": 1, "type": "loop_state_changed"}\n')

    # not a fork: must be skipped
    s4 = "session_20260301_000004_66666666"
    write_json(home / "queens" / "queen_growth" / "sessions" / s4 / "meta.json", session_meta(s4, queen_id="queen_growth"))
    conversation_files(home / "queens" / "queen_growth" / "sessions" / s4)

    # legacy singular dir
    s5 = "session_20260302_000001_77777777"
    write_json(home / "colonies" / "alpha" / "queen" / s5 / "meta.json", session_meta(s5, queen_id="queen_ops"))
    conversation_files(home / "colonies" / "alpha" / "queen" / s5)


BUILDERS = {"v1": build_v1, "v2": build_v2, "colony-fork": build_colony_fork}


def fix_paths(home: Path) -> int:
    """Rewrite the __HIVE_HOME__ placeholder to this copy's absolute path."""
    replaced = 0
    for path in sorted(home.rglob("*")):
        if not path.is_file() or path.suffix not in {".json", ".yaml", ".md", ".jsonl", ".txt"}:
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            continue
        if HOME_TOKEN not in text:
            continue
        path.write_text(text.replace(HOME_TOKEN, str(home.resolve())), encoding="utf-8")
        replaced += 1
    return replaced


def manifest(home: Path) -> dict:
    files = {}
    for path in sorted(home.rglob("*")):
        if not path.is_dir():
            rel = str(path.relative_to(home))
            info = path.stat()
            files[rel] = {"size": info.st_size, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()[:16]}
    return {"root": str(home), "files": files}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dest")
    parser.add_argument("--version", choices=sorted(BUILDERS))
    parser.add_argument("--fix-paths", metavar="HOME")
    parser.add_argument("--manifest", metavar="HOME")
    args = parser.parse_args()

    if args.fix_paths:
        count = fix_paths(Path(args.fix_paths))
        print(json.dumps({"fixed": count}))
        return 0
    if args.manifest:
        print(json.dumps(manifest(Path(args.manifest)), indent=2, sort_keys=True))
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
