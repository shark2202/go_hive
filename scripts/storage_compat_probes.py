#!/usr/bin/env python3
"""Python half of the HIVE_HOME storage-compat / G2 double run.

Subcommands (all take --home; set HIVE_HOME before importing framework):

  migrate   Run the same storage-prep sequence SessionManager.__init__ runs:
            migrate_v2 -> migrate_v3 -> migrate_colony_sessions ->
            ensure_all_colony_tracker_dbs.
  probes    Write one instance of each storage artifact through the real
            writer APIs (state.json, conversations/*, checkpoints/*,
            credentials, tracker.db for a fresh colony), for field-level
            comparison with the Go harness.
  read-probes  Report what the Python APIs see in a migrated home: cold /
            colony session lists, per-session history (parts/state/summary/
            tasks), colony discovery, tracker query output, senders, skills,
            memories, credentials. Mirrors `storagecompat read-probes`.
  creds-save  Encrypt fake credentials into <home>/credentials (Fernet key
            from <home>/secrets/credential_key, generated if absent).
  creds-load  Decrypt every credential in <home>/credentials and print it.

Run from the `core/` directory with the project venv, e.g.:
  cd core && uv run python ../go_hive/scripts/storage_compat_probes.py migrate --home /tmp/fx
"""

from __future__ import annotations

import argparse
import asyncio
import hashlib
import json
import os
import sqlite3
import sys
from pathlib import Path


def bootstrap(home: Path) -> None:
    os.environ["HIVE_HOME"] = str(home)


def out(data) -> None:
    print(json.dumps(data, indent=2, default=str))


# ---------------------------------------------------------------------------
# migrate
# ---------------------------------------------------------------------------


def cmd_migrate(args) -> int:
    home = Path(args.home).resolve()
    bootstrap(home)

    from framework.storage import migrate_colony_sessions, migrate_v2, migrate_v3
    from framework.host.tracker_db import ensure_all_colony_tracker_dbs

    report = {
        "home": str(home),
        "needs_v2": migrate_v2.needs_migration(),
        "needs_v3": migrate_v3.needs_migration(),
        "needs_colony_sessions": migrate_colony_sessions.needs_migration(),
    }
    if not args.dry_run:
        migrate_v2.run_migration(exports_dir=home / "exports")
        migrate_v3.run_migration()
        migrate_colony_sessions.run_migration()
        ensured = ensure_all_colony_tracker_dbs()
        report["tracker_dbs_ensured"] = [str(p.relative_to(home)) for p in ensured]
    report["post_needs_v2"] = migrate_v2.needs_migration()
    report["post_needs_v3"] = migrate_v3.needs_migration()
    report["post_needs_colony_sessions"] = migrate_colony_sessions.needs_migration()
    out(report)
    return 0


# ---------------------------------------------------------------------------
# writer probes
# ---------------------------------------------------------------------------


def _probe_session_dir(home: Path) -> Path:
    return home / "queens" / "queen_growth" / "sessions" / "session_compat_probe_py"


async def _write_conversation(session_dir: Path) -> None:
    from framework.agent_loop.conversation import NodeConversation
    from framework.storage.conversation_store import FileConversationStore

    store = FileConversationStore(session_dir / "conversations")
    conv = NodeConversation(
        system_prompt="You are Hive.",
        store=store,
        output_keys=["answer"],
        compaction_buffer_tokens=4096,
        compaction_buffer_ratio=0.05,
        compaction_warning_buffer_tokens=2048,
    )
    await conv.add_user_message("hello", is_client_input=True)
    await conv.add_assistant_message(
        "let me look",
        tool_calls=[{"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": "{}"}}],
        reasoning_content="checking the file",
    )
    await conv.add_tool_result("call_1", "file contents", is_skill_content=True, spillover_path=str(session_dir / "artifacts" / "spill.txt"))


async def _write_checkpoint(session_dir: Path) -> None:
    from framework.schemas.checkpoint import Checkpoint
    from framework.storage.checkpoint_store import CheckpointStore

    store = CheckpointStore(session_dir)
    checkpoint = Checkpoint(
        checkpoint_id="cp_node_complete_start_20260101T000000",
        checkpoint_type="node_complete",
        session_id=session_dir.name,
        run_id="run-compat",
        created_at="2026-01-01T00:00:00+00:00",
        current_node="start",
        next_node="finish",
        execution_path=["start"],
        data_buffer={"k": "v"},
        accumulated_outputs={"answer": "42"},
        metrics_snapshot={"total_tokens": 7},
        is_clean=True,
        description="compat probe",
    )
    await store.save_checkpoint(checkpoint)


async def _write_state_and_checkpoint(session_dir: Path) -> None:
    from framework.schemas.session_state import SessionProgress, SessionState, SessionStatus, SessionTimestamps
    from framework.storage.session_store import SessionStore

    store = SessionStore(session_dir.parent.parent)
    state = SessionState(
        session_id=session_dir.name,
        status=SessionStatus("paused"),
        goal_id="goal_compat",
        agent_id="queen_growth",
        timestamps=SessionTimestamps(
            started_at="2026-01-01T00:00:00+00:00",
            updated_at="2026-01-01T00:05:00+00:00",
            completed_at=None,
            paused_at_time=None,
        ),
        progress=SessionProgress(current_node="start", steps_executed=1, path=["start"]),
        active_triggers=["trg-1"],
        trigger_tasks={"trg-1": "check"},
        worker_configured=True,
    )
    await store.write_state(session_dir.name, state)


def _write_credentials(home: Path) -> dict:
    from framework.credentials.models import CredentialKey, CredentialObject, CredentialType
    from framework.credentials.storage import EncryptedFileStorage

    key_path = home / "secrets" / "credential_key"
    key_path.parent.mkdir(parents=True, exist_ok=True)
    if not key_path.exists():
        from cryptography.fernet import Fernet

        key_path.write_text(Fernet.generate_key().decode(), encoding="utf-8")
    key = key_path.read_text(encoding="utf-8").strip()

    storage = EncryptedFileStorage(home / "credentials", encryption_key=key.encode())

    api = CredentialObject(
        id="demo_api_key",
        credential_type=CredentialType.API_KEY,
        keys={"api_key": CredentialKey(name="api_key", value="sk-demo-123", metadata={"scope": "read"})},
        description="demo api key",
        tags=["demo", "compat"],
    )
    storage.save(api)

    oauth = CredentialObject(
        id="demo_oauth",
        credential_type=CredentialType.OAUTH2,
        keys={
            "access_token": CredentialKey(
                name="access_token",
                value="at-demo",
                expires_at="2026-12-31T00:00:00+00:00",
            ),
            "refresh_token": CredentialKey(name="refresh_token", value="rt-demo"),
            "_alias": CredentialKey(name="_alias", value="work"),
            "_integration_type": CredentialKey(name="_integration_type", value="google"),
            "_identity_email": CredentialKey(name="_identity_email", value="demo@example.com"),
        },
        provider_id="oauth2",
        auto_refresh=True,
        description="demo oauth",
    )
    storage.save(oauth)
    return {"saved": ["demo_api_key", "demo_oauth"], "key_path": str(key_path)}


def cmd_probes(args) -> int:
    home = Path(args.home).resolve()
    bootstrap(home)
    session_dir = _probe_session_dir(home)
    session_dir.mkdir(parents=True, exist_ok=True)

    asyncio.run(_write_state_and_checkpoint(session_dir))
    asyncio.run(_write_conversation(session_dir))
    asyncio.run(_write_checkpoint(session_dir))

    # A fresh colony + tracker ensure (exercises EnsureTrackerDB's DDL path).
    colony = home / "colonies" / "probe_colony"
    colony.mkdir(parents=True, exist_ok=True)
    (colony / "metadata.json").write_text(json.dumps({"name": "probe_colony", "created_at": 1767225600.0}, indent=2), encoding="utf-8")
    from framework.host.tracker_db import ensure_tracker_db

    ensure_tracker_db(colony)

    creds = _write_credentials(home)
    out({"probes": "written", "session_dir": str(session_dir), "credentials": creds})
    return 0


# ---------------------------------------------------------------------------
# credentials cross-read
# ---------------------------------------------------------------------------


def _load_key(home: Path) -> str:
    key_path = home / "secrets" / "credential_key"
    if not key_path.exists():
        raise SystemExit(f"missing credential key at {key_path}")
    return key_path.read_text(encoding="utf-8").strip()


def _credential_to_dict(cred) -> dict:
    keys = {}
    for name, key in cred.keys.items():
        keys[name] = {
            "value": key.get_secret_value(),
            "expires_at": key.expires_at.isoformat() if key.expires_at else None,
            "metadata": key.metadata,
        }
    return {
        "id": cred.id,
        "credential_type": cred.credential_type.value,
        "provider_id": cred.provider_id,
        "auto_refresh": cred.auto_refresh,
        "description": cred.description,
        "tags": list(cred.tags),
        "created_at": cred.created_at.isoformat() if cred.created_at else None,
        "updated_at": cred.updated_at.isoformat() if cred.updated_at else None,
        "last_refreshed": cred.last_refreshed.isoformat() if cred.last_refreshed else None,
        "last_used": cred.last_used.isoformat() if cred.last_used else None,
        "use_count": cred.use_count,
        "keys": keys,
    }


def cmd_creds_load(args) -> int:
    home = Path(args.home).resolve()
    from framework.credentials.storage import EncryptedFileStorage

    storage = EncryptedFileStorage(home / "credentials", encryption_key=_load_key(home).encode())
    ids = storage.list_all()
    creds = []
    for cid in sorted(ids):
        cred = storage.load(cid)
        if cred is not None:
            creds.append(_credential_to_dict(cred))
    index_path = home / "credentials" / "metadata" / "index.json"
    index = json.loads(index_path.read_text(encoding="utf-8")) if index_path.exists() else None
    out({"ids": sorted(ids), "credentials": creds, "index": index})
    return 0


def cmd_creds_save(args) -> int:
    home = Path(args.home).resolve()
    bootstrap(home)
    out(_write_credentials(home))
    return 0


# ---------------------------------------------------------------------------
# read probes (G2): what each implementation sees in a migrated home
# ---------------------------------------------------------------------------


def _sha(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()[:16]


def _file_inventory(root: Path, home: Path) -> dict[str, str]:
    if not root.is_dir():
        return {}
    return {str(p.relative_to(home)): _sha(p) for p in sorted(root.rglob("*")) if p.is_file()}


def _part_summary(part: dict) -> dict:
    content = part.get("content")
    if not isinstance(content, str):
        content = json.dumps(content, sort_keys=True, ensure_ascii=False) if content is not None else ""
    return {
        "seq": part.get("seq"),
        "role": part.get("role"),
        "content": content[:200],
        "tool_use_id": part.get("tool_use_id"),
        "tool_calls": [tc.get("function", {}).get("name") for tc in (part.get("tool_calls") or [])],
        "is_skill_content": bool(part.get("is_skill_content")),
        "reasoning_content": (part.get("reasoning_content") or "")[:120] or None,
        "spillover_path": part.get("spillover_path"),
    }


def _session_dirs(home: Path) -> list[Path]:
    dirs: list[Path] = []
    for pattern in (
        "queens/*/sessions/*",
        "colonies/*/queens/*/sessions/*",
        "colonies/*/workers/*/sessions/*",
    ):
        for d in sorted(home.glob(pattern)):
            if d.is_dir():
                dirs.append(d)
    return sorted(set(dirs))


def _read_state(home: Path, session_dir: Path) -> dict | None:
    from framework.storage.session_store import SessionStore

    store = SessionStore(session_dir.parent.parent)
    try:
        state = asyncio.run(store.read_state(session_dir.name))
    except Exception as exc:  # noqa: BLE001 - probe reports instead of raising
        return {"error": str(exc)}
    if state is None:
        return None
    return {
        "status": str(state.status),
        "goal_id": state.goal_id,
        "agent_id": state.agent_id,
        "updated_at": state.timestamps.updated_at,
        "steps_executed": state.progress.steps_executed,
    }


def _read_histories(home: Path) -> dict:
    from framework.storage.conversation_store import FileConversationStore

    histories = {}
    for session_dir in _session_dirs(home):
        rel = str(session_dir.relative_to(home))
        convs = session_dir / "conversations"
        parts: list[dict] = []
        if convs.is_dir():
            store = FileConversationStore(convs)
            try:
                parts = [_part_summary(p) for p in asyncio.run(store.read_parts())]
            except Exception as exc:  # noqa: BLE001
                parts = [{"error": str(exc)}]
        histories[rel] = {
            "state": _read_state(home, session_dir),
            "summary": json.loads((session_dir / "summary.json").read_text(encoding="utf-8")) if (session_dir / "summary.json").exists() else None,
            "tasks": json.loads((session_dir / "tasks.json").read_text(encoding="utf-8")) if (session_dir / "tasks.json").exists() else None,
            "parts": parts,
        }
    return histories


def _serialize_discover(home: Path) -> list[dict]:
    from framework.agents.discovery import discover_agents

    entries = []
    for category, group in sorted(discover_agents().items()):
        for entry in group:
            entries.append(
                {
                    "category": category,
                    "name": entry.name,
                    "path": str(entry.path.relative_to(home)) if entry.path.is_relative_to(home) else str(entry.path),
                    "description": entry.description,
                    "session_count": entry.session_count,
                    "run_count": entry.run_count,
                    "node_count": entry.node_count,
                    "tool_count": entry.tool_count,
                    "last_active": entry.last_active,
                    "created_at": entry.created_at,
                    "icon": entry.icon,
                    "workers": [{"name": w.name, "description": w.description, "tool_count": w.tool_count, "task": w.task} for w in entry.workers],
                }
            )
    return entries


def _tracker_probe(home: Path, colony_dir: Path) -> dict | None:
    from framework.host.tracker_db import execute_sql

    db_path = colony_dir / "tracker" / "tracker.db"
    if not db_path.exists():
        return None
    probe: dict = {"db": str(db_path.relative_to(home))}

    def query(sql: str) -> dict:
        try:
            return execute_sql(db_path, sql, row_cap=1000)
        except Exception as exc:  # noqa: BLE001
            return {"error": str(exc)}

    probe["tables"] = query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
    probe["registry"] = query("SELECT table_name, write_columns, key_columns, mode FROM _tracker_registry ORDER BY table_name")
    probe["meta"] = query("SELECT key, value FROM _tracker_meta ORDER BY key")
    probe["changes"] = query("SELECT table_name, pk, op FROM _tracker_changes ORDER BY id")
    names = []
    tables_result = probe["tables"]
    if tables_result.get("kind") == "rows":
        names = [row[0] for row in tables_result.get("rows", [])]
    probe["user_rows"] = {}
    for name in names:
        if name.startswith("_"):
            continue
        probe["user_rows"][name] = query(f'SELECT * FROM "{name}" ORDER BY rowid')
    if {"leads", "invoices"} <= set(names):
        probe["join"] = query(
            "SELECT l.id, l.name, COUNT(i.id) AS invoice_count FROM leads l"
            " LEFT JOIN invoices i ON i.lead_id = l.id GROUP BY l.id, l.name ORDER BY l.id"
        )
    else:
        probe["join"] = None
    return probe


def _public_sender(row: dict) -> dict:
    return {
        "id": row.get("id"),
        "name": row.get("name"),
        "provider": row.get("provider"),
        "auth_type": row.get("auth_type"),
        "from_email": row.get("from_email"),
        "from_name": row.get("from_name") or None,
        "weight": row.get("weight"),
        "daily_limit": row.get("daily_limit"),
        "enabled": row.get("enabled"),
        "tags": row.get("tags") or [],
        "can_receive": row.get("can_receive"),
    }


def _read_senders(home: Path) -> dict:
    path = home / "senders" / "senders.json"
    if not path.exists():
        return {"present": False, "senders": []}
    data = json.loads(path.read_text(encoding="utf-8"))
    return {"present": True, "version": data.get("version"), "senders": [_public_sender(row) for row in data.get("senders", [])]}


def _read_credentials(home: Path) -> list[dict]:
    from framework.credentials.storage import EncryptedFileStorage

    if not (home / "secrets" / "credential_key").exists():
        return []
    storage = EncryptedFileStorage(home / "credentials", encryption_key=_load_key(home).encode())
    creds = []
    for cid in sorted(storage.list_all()):
        cred = storage.load(cid)
        if cred is None:
            continue
        keys = {
            name: {
                "value": key.get_secret_value(),
                "expires_at": key.expires_at.isoformat() if key.expires_at else None,
                "metadata": key.metadata,
            }
            for name, key in sorted(cred.keys.items())
        }
        creds.append(
            {
                "id": cred.id,
                "credential_type": cred.credential_type.value,
                "provider_id": cred.provider_id,
                "auto_refresh": cred.auto_refresh,
                "description": cred.description,
                "tags": list(cred.tags),
                "keys": keys,
            }
        )
    return creds


def cmd_read_probes(args) -> int:
    home = Path(args.home).resolve()
    bootstrap(home)

    # Import after HIVE_HOME is set: framework.config resolves paths at import.
    from framework.config import COLONIES_DIR
    from framework.server.session_manager import SessionManager

    report: dict = {"home": str(home)}

    report["cold_sessions"] = SessionManager.list_cold_sessions()
    colony_sessions = {}
    for colony_dir in sorted(COLONIES_DIR.iterdir()) if COLONIES_DIR.is_dir() else []:
        if colony_dir.is_dir():
            colony_sessions[colony_dir.name] = SessionManager.list_colony_sessions(colony_dir.name)
    report["colony_sessions"] = colony_sessions

    report["histories"] = _read_histories(home)
    report["discover"] = _serialize_discover(home)

    colonies = {}
    for colony_dir in sorted(COLONIES_DIR.iterdir()) if COLONIES_DIR.is_dir() else []:
        if not colony_dir.is_dir():
            continue
        entry: dict = {"colony": colony_dir.name}
        meta_path = colony_dir / "metadata.json"
        entry["metadata"] = json.loads(meta_path.read_text(encoding="utf-8")) if meta_path.exists() else None
        entry["workers"] = {}
        for cfg in sorted(colony_dir.glob("*.json")):
            data = json.loads(cfg.read_text(encoding="utf-8")) if cfg.is_file() else None
            if isinstance(data, dict) and "system_prompt" in data:
                entry["workers"][cfg.name] = {
                    "name": data.get("name"),
                    "input_data": data.get("input_data"),
                    "tools": data.get("tools"),
                }
        entry["triggers"] = json.loads((colony_dir / "triggers.json").read_text(encoding="utf-8")) if (colony_dir / "triggers.json").exists() else None
        entry["skills"] = sorted(str(p.relative_to(home)) for p in (colony_dir / "skills").rglob("SKILL.md")) if (colony_dir / "skills").is_dir() else []
        runs = None
        if (colony_dir / "colony.db").exists():
            con = sqlite3.connect(str(colony_dir / "colony.db"))
            try:
                runs = [{"run_id": row[0], "name": row[1], "status": row[2], "error": row[5]} for row in con.execute("SELECT run_id, name, status, dispatched, dead_lettered, error FROM playbook_runs ORDER BY run_id")]
            finally:
                con.close()
        entry["playbook_runs"] = runs
        entry["tracker"] = _tracker_probe(home, colony_dir)
        colonies[colony_dir.name] = entry
    report["colonies"] = colonies

    report["senders"] = _read_senders(home)
    report["skills_user"] = _file_inventory(home / "skills", home)
    report["skills_queens"] = {q.name: _file_inventory(q / "skills", home) for q in sorted((home / "queens").iterdir()) if q.is_dir() and (q / "skills").is_dir()} if (home / "queens").is_dir() else {}
    report["memories"] = _file_inventory(home / "memories", home)
    report["credentials"] = _read_credentials(home)
    out(report)
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("migrate")
    p.add_argument("--home", required=True)
    p.add_argument("--dry-run", action="store_true")
    p.set_defaults(func=cmd_migrate)

    p = sub.add_parser("probes")
    p.add_argument("--home", required=True)
    p.set_defaults(func=cmd_probes)

    p = sub.add_parser("creds-save")
    p.add_argument("--home", required=True)
    p.set_defaults(func=cmd_creds_save)

    p = sub.add_parser("creds-load")
    p.add_argument("--home", required=True)
    p.set_defaults(func=cmd_creds_load)

    p = sub.add_parser("read-probes")
    p.add_argument("--home", required=True)
    p.set_defaults(func=cmd_read_probes)

    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
