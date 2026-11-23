#!/usr/bin/env python3
"""Enable souls for many queens at once (configuration.json `soul.queens`).

The product has no "enable for all" switch: each queen has her own
`soul.queens.<id>` entry because her cadence (hour), mode (review/auto),
sections and model are per-queen, and her reflection keeps its own cursors and
proposals. This helper is the missing convenience: it writes the same minimal
entry for every queen that has none, and leaves existing entries alone.

Usage:
    python3 go_hive/scripts/enable_souls.py --home ~/.hive-go --dry-run
    python3 go_hive/scripts/enable_souls.py --home ~/.hive-go            # all queens
    python3 go_hive/scripts/enable_souls.py --home ~/.hive-go --queen queen_growth
    python3 go_hive/scripts/enable_souls.py --home ~/.hive-go --disable  # remove the switch only

Idempotent: an existing `soul.queens.<id>` entry is reported and kept
(its `enabled` value untouched) unless --force is passed.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import sys
import tempfile
import time
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
DEFAULT_QUEENS_DIR = REPO / "go_hive" / "go-hive" / "internal" / "queen" / "queen_defaults"


def queen_ids(home: Path, explicit: list[str], include_default: bool = False) -> list[str]:
    ids: set[str] = set(explicit)
    queens_dir = home / "queens"
    if queens_dir.is_dir():
        ids.update(entry.name for entry in queens_dir.iterdir() if entry.is_dir())
    if DEFAULT_QUEENS_DIR.is_dir():
        ids.update(path.stem for path in DEFAULT_QUEENS_DIR.glob("*.yaml"))
    # "default" is the shared fallback persona, not one of the 13 shipped
    # queens; enabling her soul is opt-in only.
    if not include_default:
        ids.discard("default")
    return sorted(ids)


def load(home: Path) -> dict:
    path = home / "configuration.json"
    if not path.is_file():
        return {}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise SystemExit(f"configuration.json is not valid JSON: {exc}")
    return data if isinstance(data, dict) else {}


def write_atomic(path: Path, data: dict) -> None:
    backup = path.with_suffix(path.suffix + time.strftime(".bak-%Y%m%d-%H%M%S"))
    if path.is_file():
        shutil.copy2(path, backup)
    text = json.dumps(data, indent=2, ensure_ascii=False) + "\n"
    handle = tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=str(path.parent), delete=False)
    try:
        handle.write(text)
        handle.flush()
        os.fsync(handle.fileno())
        handle.close()
        os.replace(handle.name, path)
    except Exception:
        os.unlink(handle.name)
        raise
    print(f"backup: {backup}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--home", default=os.environ.get("HIVE_HOME") or str(Path.home() / ".hive-go"))
    parser.add_argument("--queen", action="append", default=[], help="queen id (repeatable; default: every queen found)")
    parser.add_argument("--mode", choices=("review", "auto"), default="review")
    parser.add_argument("--hour", type=int, default=None, help="reflection hour (default: leave the product default, 4)")
    parser.add_argument("--disable", action="store_true", help="write enabled=false instead of true")
    parser.add_argument("--force", action="store_true", help="overwrite mode/hour/enabled of existing entries")
    parser.add_argument("--include-default", action="store_true", help="also write the shared fallback queen id \"default\"")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--json-out")
    args = parser.parse_args()

    home = Path(os.path.expanduser(args.home)).resolve()
    config = load(home)
    soul = config.setdefault("soul", {})
    if not isinstance(soul, dict):
        raise SystemExit("configuration.json `soul` is not an object")
    queens = soul.setdefault("queens", {})
    if not isinstance(queens, dict):
        raise SystemExit("configuration.json `soul.queens` is not an object")

    ids = queen_ids(home, args.queen, args.include_default)
    report = {"home": str(home), "dry_run": args.dry_run, "queens": []}
    changed = 0

    for queen_id in ids:
        entry = queens.get(queen_id)
        row = {"queen_id": queen_id, "action": "", "enabled": None, "mode": None, "hour": None}
        if isinstance(entry, dict):
            row["enabled"] = entry.get("enabled", True)
            row["mode"] = entry.get("mode")
            row["hour"] = entry.get("hour")
            if args.force:
                entry["enabled"] = not args.disable
                entry["mode"] = args.mode
                if args.hour is not None:
                    entry["hour"] = args.hour
                row["action"] = "updated"
                changed += 1
            else:
                row["action"] = "kept"
        else:
            new_entry = {"enabled": not args.disable}
            if not args.disable:
                new_entry["mode"] = args.mode
                if args.hour is not None:
                    new_entry["hour"] = args.hour
            queens[queen_id] = new_entry
            row.update({"action": "created", "enabled": new_entry["enabled"], "mode": new_entry.get("mode"), "hour": new_entry.get("hour")})
            changed += 1
        report["queens"].append(row)

    report["changed"] = changed
    report["enabled_total"] = sum(
        1 for entry in queens.values() if isinstance(entry, dict) and entry.get("enabled", True) is not False
    )

    width = max((len(row["queen_id"]) for row in report["queens"]), default=10)
    print(f"{'queen_id'.ljust(width)}  action   enabled  mode    hour")
    for row in report["queens"]:
        print(
            f"{row['queen_id'].ljust(width)}  {row['action']:7}  {str(row['enabled']):7}  {str(row['mode'] or '-'):6}  {row['hour'] if row['hour'] is not None else '-'}"
        )
    print(f"\n{changed} changed · {report['enabled_total']} of {len(queens)} entries enabled · dry_run={args.dry_run}")

    if args.json_out:
        Path(args.json_out).write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        print(f"report: {args.json_out}")

    if args.dry_run or changed == 0:
        return 0
    write_atomic(home / "configuration.json", config)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())