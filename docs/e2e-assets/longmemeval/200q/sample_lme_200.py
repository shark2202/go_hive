#!/usr/bin/env python3
"""Deterministic, stratified LongMemEval sample (default n=200).

Reproduces the n=60 selection exactly (verified byte-for-byte against
``60q/sample-n60.json``): seed 0, one shared CPython ``random.Random(0)``;
for each question_type in the canonical report order
``single-session-user, single-session-assistant, multi-session,
temporal-reasoning, knowledge-update, single-session-preference``:
``ids = sorted(ids); rng.shuffle(ids); take the first quota`` where
quota = min(ceil(n / 6), supply of that type).

The sweep consumes the RNG identically for every quota, so the n=60 sample is
a per-type prefix of the n=200 sample.

At n=200 the six-type even target is ceil(200/6)=34; single-session-preference
has only 30 questions, so it contributes all 30 and the other five types
contribute 34 each (34*5+30 = 200). This is the most balanced allocation the
dataset permits.

Usage:
  uv run --offline python 200q/sample_lme_200.py --n 200 \
      --out 200q/sample-n200.json --verify 60q/sample-n60.json
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import random
from pathlib import Path
from typing import Any

TYPES = [
    "single-session-user",
    "single-session-assistant",
    "multi-session",
    "temporal-reasoning",
    "knowledge-update",
    "single-session-preference",
]
DATA_DIR = Path.home() / ".hive-go/longmemeval-data"


def load_index(data_dir: Path) -> list[dict[str, Any]]:
    return json.loads((data_dir / "s_cleaned/index.json").read_text(encoding="utf-8"))


def sweep(index: list[dict[str, Any]], n: int) -> tuple[list[str], dict[str, Any], dict[str, int]]:
    """Return (selected ids, selection metadata, rank_in_type per selected id)."""
    by_type: dict[str, list[str]] = {t: [] for t in TYPES}
    for item in index:
        by_type.setdefault(item["question_type"], []).append(item["question_id"])
    target = math.ceil(n / len(by_type))
    rng = random.Random(0)
    ids: list[str] = []
    quotas: dict[str, int] = {}
    supplies: dict[str, int] = {}
    rank: dict[str, int] = {}
    for qtype in TYPES:
        pool = sorted(by_type[qtype])
        supplies[qtype] = len(pool)
        rng.shuffle(pool)
        quota = min(target, len(pool))
        quotas[qtype] = quota
        ids.extend(pool[:quota])
        rank.update({qid: i for i, qid in enumerate(pool)})
    selection = {
        "seed": 0,
        "rng": "CPython random.Random(0)",
        "method": (
            "for each question_type in canonical order: ids = sorted(ids); "
            "rng.shuffle(ids); take first min(ceil(n/6), supply)"
        ),
        "type_order": TYPES,
        "per_type_target": target,
        "n_requested": n,
        "n": len(ids),
        "type_supply": supplies,
        "type_counts": quotas,
    }
    return ids, selection, rank


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--n", type=int, default=200)
    parser.add_argument("--data-dir", type=Path, default=DATA_DIR)
    parser.add_argument("--out", type=Path, default=Path(__file__).with_name("sample-n200.json"))
    parser.add_argument("--verify", type=Path, default=None, help="compare against an older sample JSON")
    args = parser.parse_args()

    index = load_index(args.data_dir)
    ids, selection, rank = sweep(index, args.n)
    meta = {item["question_id"]: item for item in index}
    questions = [
        {
            "question_id": qid,
            "question_type": meta[qid]["question_type"],
            "question_date": meta[qid].get("question_date"),
            "abstention": qid.endswith("_abs"),
            "sessions": meta[qid].get("sessions"),
            "rank_in_type": rank[qid],
        }
        for qid in ids
    ]
    digest = hashlib.sha256("\n".join(ids).encode()).hexdigest()
    payload = {
        "dataset": "longmemeval_s_cleaned (HuggingFace xiaowu0162/longmemeval-cleaned, 500 questions)",
        "data_dir": "~/.hive-go/longmemeval-data",
        "selection": selection,
        "ids_sha256": digest,
        "question_ids": ids,
        "questions": questions,
    }
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(payload, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")

    print(f"wrote {args.out}: n={len(ids)} ids_sha256={digest}")
    for qtype in TYPES:
        print(f"  {qtype:26s} supply={selection['type_supply'][qtype]:3d} "
              f"quota={selection['type_counts'][qtype]:2d}")
    print(f"  abstention (_abs): {sum(1 for q in questions if q['abstention'])}")

    if args.verify:
        old = json.loads(args.verify.read_text(encoding="utf-8"))
        old_ids = old["question_ids"]
        old_meta = {q["question_id"]: q["question_type"] for q in old["questions"]} if "questions" in old else {}
        missing = [qid for qid in old_ids if qid not in rank]
        # Per-type prefix check: the flat lists cannot be prefixes of each
        # other (quotas differ), but each old type segment must be the first
        # len(segment) ids of that type in the new sample.
        by_type_new: dict[str, list[str]] = {t: [] for t in TYPES}
        for qid in ids:
            by_type_new[meta[qid]["question_type"]].append(qid)
        old_by_type: dict[str, list[str]] = {}
        for qid in old_ids:
            qtype = old_meta.get(qid) or meta[qid]["question_type"]
            old_by_type.setdefault(qtype, []).append(qid)
        mismatches = [
            t for t, seg in old_by_type.items() if by_type_new.get(t, [])[: len(seg)] != seg
        ]
        print(f"verify {args.verify}: old n={len(old_ids)} missing={len(missing)} "
              f"per_type_prefix_ok={not mismatches} mismatched_types={mismatches}")
        if missing or mismatches:
            raise SystemExit(f"n={len(old_ids)} sample is not a per-type prefix of this one")


if __name__ == "__main__":
    main()
