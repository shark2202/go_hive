#!/usr/bin/env python3
"""Prune failed rows from a LongMemEval results.jsonl and print their ids.

Used by the n=200 evidence scripts to honour the "retry a failed question
once" rule: a failed row (``error`` or ``judge_error``) would otherwise be
skipped by ``--resume`` / ``HIVE_LME_RESUME=1``, so it is moved to a backup
file and the printed ids are re-run with RETRY_IDS=<ids>.

Exit code 0 always; prints "RETRY_IDS=..." on the last line (empty list when
``RETRY_IDS=`).
"""

from __future__ import annotations

import argparse
import json
import time
from pathlib import Path


def failed(row: dict) -> bool:
    return bool(row.get("error")) or bool(row.get("judge_error")) or "correct" not in row


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("results", type=Path)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    rows = []
    for line in args.results.read_text(encoding="utf-8").splitlines():
        if line.strip():
            rows.append(json.loads(line))
    bad = [r for r in rows if failed(r)]
    if not bad:
        print(f"{args.results}: {len(rows)} rows, no failed rows")
        print("RETRY_IDS=")
        return
    ids = [r["question_id"] for r in bad]
    print(f"{args.results}: {len(rows)} rows, {len(ids)} failed: {','.join(ids)}")
    if not args.dry_run:
        backup = args.results.with_name(args.results.name + f".retry-bak-{int(time.time())}")
        backup.write_text(
            "".join(json.dumps(r, ensure_ascii=False) + "\n" for r in bad), encoding="utf-8"
        )
        kept = [r for r in rows if not failed(r)]
        args.results.write_text(
            "".join(json.dumps(r, ensure_ascii=False) + "\n" for r in kept), encoding="utf-8"
        )
        print(f"pruned {len(bad)} row(s) to {backup}; kept {len(kept)}")
    print(f"RETRY_IDS={','.join(ids)}")


if __name__ == "__main__":
    main()
