#!/usr/bin/env python3
"""Recompute the LongMemEval n=60 Go-vs-Python comparison from results.jsonl.

Both harnesses write one JSON row per question with the same field names
(question_id, question_type, answer, hypothesis, correct, judge, seconds,
optional error/judge_error). This script reads the two run files, prints and
saves the overall / per-type accuracies, the cross-side disagreement list, and
normalized per-question JSON (id / type / expect / pred / hit / seconds), so
every number in the report can be recomputed from the raw rows.

Usage:
  uv run --offline python go_hive/scripts/summarize_lme_60q.py \
    --go  ~/.hive-go/longmemeval-data/runs/go-60/results.jsonl \
    --py  ~/.hive-go/longmemeval-data/runs/py-60/results.jsonl \
    --json go_hive/docs/e2e-assets/longmemeval/60q/summary-60q.json \
    --per-question-dir go_hive/docs/e2e-assets/longmemeval/60q
"""

from __future__ import annotations

import argparse
import json
import math
import statistics
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


def read_rows(path: Path) -> list[dict[str, Any]]:
    rows = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        rows.append(json.loads(line))
    return rows


def wilson(k: int, n: int, z: float = 1.96) -> tuple[float, float]:
    if not n:
        return 0.0, 0.0
    p = k / n
    centre = (p + z * z / (2 * n)) / (1 + z * z / n)
    half = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / (1 + z * z / n)
    return centre - half, centre + half


def pct(k: int, n: int) -> str:
    return f"{100.0 * k / n:.1f}%" if n else "n/a"


def side_stats(rows: list[dict[str, Any]]) -> dict[str, Any]:
    by_id: dict[str, dict[str, Any]] = {}
    for row in rows:
        by_id[row["question_id"]] = row  # last row for an id wins
    rows = list(by_id.values())
    correct = sum(1 for r in rows if r.get("correct") is True)
    types: dict[str, dict[str, int]] = {}
    for t in TYPES:
        subset = [r for r in rows if r.get("question_type") == t]
        if subset:
            types[t] = {"n": len(subset), "correct": sum(1 for r in subset if r.get("correct") is True)}
    for t in sorted({t for r in rows if (t := r.get("question_type")) and t not in TYPES}):
        subset = [r for r in rows if r.get("question_type") == t]
        types[t] = {"n": len(subset), "correct": sum(1 for r in subset if r.get("correct") is True)}
    abst = [r for r in rows if r.get("abstention")]
    lo, hi = wilson(correct, len(rows))
    seconds = [float(r["seconds"]) for r in rows if isinstance(r.get("seconds"), (int, float))]
    return {
        "questions": len(rows),
        "correct": correct,
        "accuracy": correct / len(rows) if rows else 0.0,
        "wilson95": [lo, hi],
        "by_type": types,
        "abstention": {"n": len(abst), "correct": sum(1 for r in abst if r.get("correct") is True)},
        "errors": sum(1 for r in rows if r.get("error")),
        "ungraded": sum(1 for r in rows if r.get("judge_error")),
        "median_seconds": statistics.median(seconds) if seconds else None,
        "ids": sorted(by_id),
    }


def per_question(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    out = []
    for r in sorted(rows, key=lambda r: (str(r.get("question_type")), str(r.get("question_id")))):
        item = {
            "question_id": r.get("question_id"),
            "question_type": r.get("question_type"),
            "abstention": r.get("abstention"),
            "expect": r.get("answer"),
            "pred": r.get("hypothesis"),
            "hit": r.get("correct"),
            "seconds": r.get("seconds"),
            "judge": r.get("judge"),
            "judge_model": r.get("judge_model"),
            "searched": r.get("searched"),
            "search_calls": r.get("search_calls"),
            "evidence_retrieved": r.get("evidence_retrieved"),
            "tools": r.get("tools"),
        }
        if r.get("error"):
            item["error"] = r["error"]
        if r.get("judge_error"):
            item["judge_error"] = r["judge_error"]
        out.append(item)
    return out


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", type=Path, required=True)
    parser.add_argument("--py", type=Path, required=True)
    parser.add_argument("--json", type=Path, default=None)
    parser.add_argument("--per-question-dir", type=Path, default=None)
    args = parser.parse_args()

    go_rows = read_rows(args.go)
    py_rows = read_rows(args.py)
    go = side_stats(go_rows)
    py = side_stats(py_rows)
    go_by = {r["question_id"]: r for r in go_rows}
    py_by = {r["question_id"]: r for r in py_rows}
    shared = sorted(set(go_by) & set(py_by))

    disagreements = []
    both_correct = both_wrong = 0
    for qid in shared:
        g = go_by[qid].get("correct") is True
        p = py_by[qid].get("correct") is True
        if g and p:
            both_correct += 1
        elif not g and not p:
            both_wrong += 1
        else:
            disagreements.append(
                {
                    "question_id": qid,
                    "question_type": go_by[qid].get("question_type"),
                    "go_correct": g,
                    "py_correct": p,
                    "go_seconds": go_by[qid].get("seconds"),
                    "py_seconds": py_by[qid].get("seconds"),
                    "go_hypothesis": go_by[qid].get("hypothesis"),
                    "py_hypothesis": py_by[qid].get("hypothesis"),
                    "expect": go_by[qid].get("answer"),
                    "go_judge": go_by[qid].get("judge"),
                    "py_judge": py_by[qid].get("judge"),
                }
            )

    go_only = sum(1 for d in disagreements if d["go_correct"])
    py_only = sum(1 for d in disagreements if d["py_correct"])
    discordant = go_only + py_only
    if discordant:
        # Two-sided exact McNemar (binomial with p=0.5 on the discordant pairs).
        k = min(go_only, py_only)
        tail = sum(math.comb(discordant, i) for i in range(k + 1)) / 2**discordant
        mcnemar_p = min(1.0, 2 * tail)
    else:
        mcnemar_p = 1.0

    summary = {
        "go": go,
        "py": py,
        "comparison": {
            "shared_questions": len(shared),
            "both_correct": both_correct,
            "both_wrong": both_wrong,
            "go_only": go_only,
            "py_only": py_only,
            "mcnemar_exact_p": mcnemar_p,
            "missing_in_go": sorted(set(py_by) - set(go_by)),
            "missing_in_py": sorted(set(go_by) - set(py_by)),
            "disagreements": disagreements,
        },
    }

    def show(name: str, s: dict[str, Any]) -> None:
        lo, hi = s["wilson95"]
        print(f"[{name}] {s['questions']} questions, {s['correct']} correct = {s['accuracy'] * 100:.1f}% "
              f"(95% CI {lo * 100:.1f}-{hi * 100:.1f}), median {s['median_seconds']}s, "
              f"errors={s['errors']}, ungraded={s['ungraded']}")
        for t, v in s["by_type"].items():
            print(f"  {t:26s} {v['correct']:2d}/{v['n']:<2d} = {pct(v['correct'], v['n'])}")
        print(f"  {'abstention (_abs)':26s} {s['abstention']['correct']:2d}/{s['abstention']['n']:<2d} "
              f"= {pct(s['abstention']['correct'], s['abstention']['n'])}")

    show("go", go)
    show("py", py)
    comp = summary["comparison"]
    print(f"[cross] shared={comp['shared_questions']} both_correct={comp['both_correct']} both_wrong={comp['both_wrong']} "
          f"go_only={comp['go_only']} py_only={comp['py_only']} (exact McNemar p={comp['mcnemar_exact_p']:.3f})")
    if comp["missing_in_go"]:
        print("  missing in go:", ",".join(comp["missing_in_go"]))
    if comp["missing_in_py"]:
        print("  missing in py:", ",".join(comp["missing_in_py"]))
    for d in comp["disagreements"]:
        print(f"  disagree {d['question_id']:16s} {d['question_type']:26s} go={d['go_correct']} py={d['py_correct']}")

    if args.json:
        args.json.parent.mkdir(parents=True, exist_ok=True)
        args.json.write_text(json.dumps(summary, indent=1, ensure_ascii=False), encoding="utf-8")
        print(f"wrote {args.json}")
    if args.per_question_dir:
        args.per_question_dir.mkdir(parents=True, exist_ok=True)
        (args.per_question_dir / "go-60-per-question.json").write_text(
            json.dumps(per_question(go_rows), indent=1, ensure_ascii=False), encoding="utf-8"
        )
        (args.per_question_dir / "py-60-per-question.json").write_text(
            json.dumps(per_question(py_rows), indent=1, ensure_ascii=False), encoding="utf-8"
        )
        print(f"wrote per-question JSON to {args.per_question_dir}")


if __name__ == "__main__":
    main()
