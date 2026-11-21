#!/usr/bin/env python3
"""Python half of the HIVE_HOME storage-compat double run.

Subcommands (all take --home; set HIVE_HOME before importing framework):

  migrate   Run the same storage-prep sequence SessionManager.__init__ runs:
            migrate_v2 -> migrate_v3 -> migrate_colony_sessions ->
            ensure_all_colony_tracker_dbs.
  probes    Write one instance of each storage artifact through the real
            writer APIs (state.json, conversations/*, checkpoints/*,
            credentials, tracker.db for a fresh colony), for field-level
            comparison with the Go harness.
  creds-save  Encrypt fake credentials into <home>/credentials (Fernet key
            from <home>/secrets/credential_key, generated if absent).
  creds-load  Decrypt every credential in <home>/credentials and print it.

Run from the `core/` directory with the project venv, e.g.:
  cd core && uv run --no-sync python ../go_hive/scripts/storage_compat_probes.py migrate --home /tmp/fx
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
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

    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
