#!/usr/bin/env python3
"""Secondary grading of the stored n=60 answers with the local TypeSafe judge slot.

The primary judge of the n=60 runs is the real LLM (deepseek-v4-pro, the same
judge as the 2026-11-21 n=20 baseline). This script re-grades the same stored
hypotheses with the machine's judge slot — typesafe `/v1/systemone`, model
`0.8B-Instant-MLX-4bit` — so the report can show how judge choice moves the
numbers. It reuses the frozen noul wording from the bridge and the harness's
own `judge_prompt`, so the prompt bytes match the primary grading exactly.

Usage:
  HIVE_LME_DATA=$HOME/.hive-go/longmemeval-data \
  uv run --offline python go_hive/docs/e2e-assets/longmemeval/60q/grade_with_typesafe_judge.py \
    --go  ~/.hive-go/longmemeval-data/runs/go-60/results.jsonl \
    --py  ~/.hive-go/longmemeval-data/runs/py-60/results.jsonl \
    --out go_hive/docs/e2e-assets/longmemeval/60q/typesafe-judge-60q.json
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
CORE = HERE.parents[4] / "core"
sys.path.insert(0, str(CORE))
sys.path.insert(0, str(HERE))

from longmem_typesafe_judge_proxy import CRITERIA, INSTRUCTIONS, call_typesafe  # noqa: E402
from tests.e2e import longmemeval as lme  # noqa: E402

DATA = Path.home() / ".hive-go" / "longmemeval-data"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", type=Path, required=True)
    parser.add_argument("--py", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--judge", default="http://127.0.0.1:18099/v1")
    parser.add_argument("--model", default="0.8B-Instant-MLX-4bit")
    parser.add_argument("--threshold", type=float, default=0.5)
    args = parser.parse_args()

    report = {}
    for side, path in (("go", args.go), ("py", args.py)):
        rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
        out = []
        for row in rows:
            qid = row["question_id"]
            item = json.loads((DATA / "s_cleaned" / f"{qid}.json").read_text())
            prompt = lme.judge_prompt(item, row.get("hypothesis") or "")
            p_yes = call_typesafe(args.judge, args.model, prompt, timeout=120.0)
            out.append(
                {
                    "question_id": qid,
                    "question_type": item["question_type"],
                    "abstention": qid.endswith("_abs"),
                    "primary_correct": row.get("correct") is True,
                    "typesafe_p_yes": p_yes,
                    "typesafe_correct": p_yes >= args.threshold,
                    "agree": (row.get("correct") is True) == (p_yes >= args.threshold),
                }
            )
        n = len(out)
        agree = sum(1 for r in out if r["agree"])
        report[side] = {
            "n": n,
            "correct": sum(1 for r in out if r["typesafe_correct"]),
            "agree_with_primary": agree,
            "agreement": agree / n if n else 0.0,
            "rows": out,
        }
        correct = report[side]["correct"]
        print(f"[{side}] typesafe judge: {correct}/{n} = {100 * correct / n:.1f}%  "
              f"agreement with primary: {agree}/{n} = {100 * agree / n:.1f}%")
        for t in sorted({r["question_type"] for r in out}):
            sub = [r for r in out if r["question_type"] == t]
            k = sum(1 for r in sub if r["typesafe_correct"])
            print(f"  {t:26s} {k:2d}/{len(sub):<2d}")

    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(report, indent=1, ensure_ascii=False), encoding="utf-8")
    print(f"wrote {args.out}")


if __name__ == "__main__":
    main()
