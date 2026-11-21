#!/usr/bin/env python3
"""G2 rollback/upgrade drill probe: snapshot and read a HIVE_HOME.

Subcommands:

  manifest --home <dir> [--out <file>]
      JSON snapshot of the whole home: every file's sha256+size, directory
      list, a normalized logical digest (row counts + row hashes) for every
      SQLite DB, and selected tracker meta rows. Two snapshots taken around
      an activity (Go run, Python read, re-run) diffed with `diff` are the
      evidence that the activity did not destroy state.

  read --home <dir> [--out <file>]
      Rollback read-back through the *Python reference implementation* APIs:
      queen profiles, queen DM sessions (meta/state/events), conversation
      history parts, colony metadata, tracker DB meta, credentials (Fernet
      decrypted), and cold-session listing via SessionManager. This is what
      "Python can still read the Go-written home" means.

  diff --before <a.json> --after <b.json> [--out <file>]
      Classify the two manifests into added / removed / modified files,
      per-SQLite-DB table digest changes, and a small summary.

Run from the `core/` directory with the project venv, e.g.:
  cd core && uv run --no-sync python ../go_hive/scripts/g2_rollback_probe.py \
      manifest --home /tmp/home --out snap.json
"""

from __future__ import annotations

import argparse
import asyncio
import hashlib
import json
import os
import shutil
import sqlite3
import sys
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]


def out(data, dest: str | None = None) -> None:
    text = json.dumps(data, indent=2, default=str, sort_keys=False)
    if dest:
        Path(dest).write_text(text + "\n", encoding="utf-8")
        print(f"wrote {dest}")
    else:
        print(text)


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


# ---------------------------------------------------------------------------
# manifest
# ---------------------------------------------------------------------------


def sqlite_digest(src: Path) -> dict:
    """Logical digest of a SQLite file, computed on a copy (never touches src)."""
    tmp = Path(tempfile.mkdtemp(prefix="g2probe-db-"))
    try:
        dst = tmp / src.name
        shutil.copy2(src, dst)
        for suffix in ("-wal", "-shm"):
            side = Path(str(src) + suffix)
            if side.exists():
                shutil.copy2(side, Path(str(dst) + suffix))
        con = sqlite3.connect(f"file:{dst}?mode=ro", uri=True)
        con.text_factory = bytes
        try:
            tables = [
                r[0].decode("utf-8", "replace")
                for r in con.execute(
                    "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
                )
            ]
            result = {"tables": {}}
            for table in tables:
                if table.startswith("sqlite_"):
                    continue
                try:
                    rows = [list(r) for r in con.execute(f'SELECT * FROM "{table}"')]
                except sqlite3.Error as exc:  # unreadable table -> report, don't crash
                    result["tables"][table] = {"error": str(exc)}
                    continue
                rows.sort(key=lambda r: json.dumps(r, default=str))
                digest = hashlib.sha256(
                    json.dumps(rows, default=str, ensure_ascii=False).encode()
                ).hexdigest()
                result["tables"][table] = {"rows": len(rows), "row_sha256": digest}
            return result
        finally:
            con.close()
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def marker_state(root: Path) -> dict:
    return {
        ".migrated-v2": (root / ".migrated-v2").exists(),
        ".migrated-v3": (root / ".migrated-v3").exists(),
        "colony_sessions_v1": (root / "colonies" / ".migrations" / "colony_sessions_v1").exists(),
    }


def cmd_manifest(args) -> int:
    home = Path(args.home).resolve()
    files: dict[str, dict] = {}
    dirs: list[str] = []
    sqlite: dict[str, dict] = {}
    for dirpath, dirnames, filenames in os.walk(home):
        dirnames.sort()
        rel_dir = os.path.relpath(dirpath, home)
        if rel_dir != ".":
            dirs.append(rel_dir.replace(os.sep, "/"))
        for name in sorted(filenames):
            full = Path(dirpath) / name
            rel = os.path.relpath(full, home).replace(os.sep, "/")
            if full.is_symlink():
                files[rel] = {"symlink": os.readlink(full)}
                continue
            files[rel] = {"sha256": sha256_file(full), "size": full.stat().st_size}
            if name.endswith(".db"):
                sqlite[rel] = sqlite_digest(full)
    out(
        {
            "probe": "g2-rollback-manifest",
            "version": 1,
            "home": str(home),
            "file_count": len(files),
            "dir_count": len(dirs),
            "markers": marker_state(home),
            "files": files,
            "dirs": sorted(dirs),
            "sqlite": sqlite,
        },
        args.out,
    )
    return 0


# ---------------------------------------------------------------------------
# read (Python reference implementation)
# ---------------------------------------------------------------------------


def _bootstrap_core(home: Path) -> None:
    os.environ["HIVE_HOME"] = str(home)
    core = str(REPO_ROOT / "core")
    if core not in sys.path:
        sys.path.insert(0, core)


def _read_json(path: Path):
    if not path.exists():
        return None
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, ValueError) as exc:
        return {"_unreadable": str(exc)}


async def _read_conversation_summary(session_dir: Path) -> dict:
    from framework.storage.conversation_store import FileConversationStore

    store = FileConversationStore(session_dir / "conversations")
    parts = await store.read_parts()
    meta = await store.read_meta()
    cursor = await store.read_cursor()
    return {
        "meta_ok": meta is not None,
        "cursor": cursor,
        "part_count": len(parts),
        "parts": [
            {
                "seq": p.get("seq"),
                "role": p.get("role"),
                "content": (str(p.get("content", "")))[:80],
            }
            for p in parts[:4]
        ],
    }


def _session_report(session_dir: Path) -> dict:
    entry = {
        "meta": _read_json(session_dir / "meta.json"),
        "state": _read_json(session_dir / "state.json"),
    }
    ev = session_dir / "events.jsonl"
    entry["events_lines"] = (
        sum(1 for _ in ev.open(encoding="utf-8", errors="replace")) if ev.exists() else 0
    )
    entry["conversation"] = asyncio.run(_read_conversation_summary(session_dir))
    return entry


def _colony_report(colony_root: Path) -> dict:
    rep = {
        "metadata": _read_json(colony_root / "metadata.json"),
        "tracker_db": str(colony_root / "tracker" / "tracker.db"),
        "tracker_exists": (colony_root / "tracker" / "tracker.db").exists(),
        "colony_db_exists": (colony_root / "colony.db").exists(),
    }
    db = colony_root / "tracker" / "tracker.db"
    if db.exists():
        # Read a copy: opening a WAL-mode DB in place can create -shm/-wal
        # sidecar files, which would show up in the snapshot diff as if the
        # Python runtime had written to the home.
        tmp = Path(tempfile.mkdtemp(prefix="g2probe-read-"))
        try:
            dst = tmp / "tracker.db"
            shutil.copy2(db, dst)
            for suffix in ("-wal", "-shm"):
                side = Path(str(db) + suffix)
                if side.exists():
                    shutil.copy2(side, Path(str(dst) + suffix))
            con = sqlite3.connect(f"file:{dst}?mode=ro", uri=True)
            try:
                meta_rows = dict(con.execute("SELECT key, value FROM _tracker_meta"))
                tables = [
                    r[0]
                    for r in con.execute(
                        "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
                    )
                ]
                rep["tracker_meta"] = meta_rows
                rep["tracker_tables"] = tables
            except sqlite3.Error as exc:
                rep["tracker_error"] = str(exc)
            finally:
                con.close()
        finally:
            shutil.rmtree(tmp, ignore_errors=True)
    queens = colony_root / "queens"
    if queens.is_dir():
        rep["queen_sessions"] = {
            q.name: sorted(d.name for d in (q / "sessions").iterdir() if d.is_dir())
            for q in sorted(queens.iterdir())
            if q.is_dir() and (q / "sessions").is_dir()
        }
    return rep


def _credential_report(home: Path) -> dict:
    key_path = home / "secrets" / "credential_key"
    if not key_path.exists():
        return {"error": "missing secrets/credential_key"}
    from framework.credentials.storage import EncryptedFileStorage

    storage = EncryptedFileStorage(
        home / "credentials", encryption_key=key_path.read_text(encoding="utf-8").strip().encode()
    )
    ids = sorted(storage.list_all())
    creds = {}
    for cid in ids:
        cred = storage.load(cid)
        if cred is None:
            creds[cid] = None
            continue
        keys = {
            name: {
                "value": k.get_secret_value(),
                "expires_at": k.expires_at.isoformat() if k.expires_at else None,
                "metadata": k.metadata,
            }
            for name, k in cred.keys.items()
        }
        creds[cid] = {
            "credential_type": cred.credential_type.value,
            "provider_id": cred.provider_id,
            "auto_refresh": cred.auto_refresh,
            "description": cred.description,
            "tags": list(cred.tags),
            "keys": keys,
        }
    index_path = home / "credentials" / "metadata" / "index.json"
    return {
        "ids": ids,
        "credentials": creds,
        "index": _read_json(index_path),
    }


def cmd_read(args) -> int:
    home = Path(args.home).resolve()
    _bootstrap_core(home)

    from framework.config import COLONIES_DIR, QUEENS_DIR
    from framework.server.session_manager import SessionManager

    report: dict = {"probe": "g2-rollback-read", "home": str(home)}
    report["markers"] = marker_state(home)

    queens: dict = {}
    if QUEENS_DIR.is_dir():
        for queen in sorted(QUEENS_DIR.iterdir()):
            if not queen.is_dir():
                continue
            profile = queen / "profile.yaml"
            sessions = {}
            sdir = queen / "sessions"
            if sdir.is_dir():
                for s in sorted(sdir.iterdir()):
                    if s.is_dir():
                        sessions[s.name] = _session_report(s)
            queens[queen.name] = {
                "profile_yaml": profile.read_text(encoding="utf-8") if profile.exists() else None,
                "sessions": sessions,
            }
    report["queens"] = queens

    colonies: dict = {}
    if COLONIES_DIR.is_dir():
        for colony in sorted(COLONIES_DIR.iterdir()):
            if colony.is_dir() and not colony.name.startswith("."):
                colonies[colony.name] = _colony_report(colony)
    report["colonies"] = colonies

    report["cold_sessions"] = SessionManager.list_cold_sessions()
    report["colony_sessions"] = {
        name: SessionManager.list_colony_sessions(name) for name in colonies
    }
    report["credentials"] = _credential_report(home)

    out(report, args.out)
    return 0


# ---------------------------------------------------------------------------
# diff
# ---------------------------------------------------------------------------


def cmd_diff(args) -> int:
    before = json.loads(Path(args.before).read_text(encoding="utf-8"))
    after = json.loads(Path(args.after).read_text(encoding="utf-8"))

    added, removed, modified = [], [], []
    for rel, info in after["files"].items():
        if rel not in before["files"]:
            added.append({"path": rel, "size": info.get("size")})
        elif before["files"][rel].get("sha256") != info.get("sha256"):
            modified.append(
                {
                    "path": rel,
                    "before_sha256": before["files"][rel].get("sha256"),
                    "after_sha256": info.get("sha256"),
                    "before_size": before["files"][rel].get("size"),
                    "after_size": info.get("size"),
                }
            )
    for rel in before["files"]:
        if rel not in after["files"]:
            removed.append({"path": rel, "size": before["files"][rel].get("size")})

    sqlite_changes = {}
    for rel in sorted(set(before.get("sqlite", {})) | set(after.get("sqlite", {}))):
        b = before.get("sqlite", {}).get(rel)
        a = after.get("sqlite", {}).get(rel)
        if b == a:
            continue
        tables = {}
        b_tables = (b or {}).get("tables", {})
        a_tables = (a or {}).get("tables", {})
        for name in sorted(set(b_tables) | set(a_tables)):
            if b_tables.get(name) != a_tables.get(name):
                tables[name] = {"before": b_tables.get(name), "after": a_tables.get(name)}
        sqlite_changes[rel] = tables

    marker_changes = {
        key: {"before": before["markers"].get(key), "after": after["markers"].get(key)}
        for key in sorted(set(before["markers"]) | set(after["markers"]))
        if before["markers"].get(key) != after["markers"].get(key)
    }

    out(
        {
            "probe": "g2-rollback-diff",
            "before": args.before,
            "after": args.after,
            "added": added,
            "removed": removed,
            "modified": modified,
            "sqlite_changes": sqlite_changes,
            "marker_changes": marker_changes,
            "summary": {
                "added": len(added),
                "removed": len(removed),
                "modified": len(modified),
                "sqlite_changed": len(sqlite_changes),
            },
        },
        args.out,
    )
    return 0


# ---------------------------------------------------------------------------


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="cmd", required=True)

    p = sub.add_parser("manifest")
    p.add_argument("--home", required=True)
    p.add_argument("--out")
    p.set_defaults(func=cmd_manifest)

    p = sub.add_parser("read")
    p.add_argument("--home", required=True)
    p.add_argument("--out")
    p.set_defaults(func=cmd_read)

    p = sub.add_parser("diff")
    p.add_argument("--before", required=True)
    p.add_argument("--after", required=True)
    p.add_argument("--out")
    p.set_defaults(func=cmd_diff)

    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
