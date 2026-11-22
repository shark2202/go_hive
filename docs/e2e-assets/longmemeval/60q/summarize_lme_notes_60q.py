#!/usr/bin/env python3
"""Recompute the LongMemEval n=60 four-way comparison (Go/Py x notes/no-notes).

Reads the four results.jsonl files (one JSON row per question, same field
names on both sides), prints and saves:

- overall / per-type accuracy with 95% Wilson intervals per group,
- paired transitions between groups (correct<->wrong flips, exact McNemar),
- the notes telemetry the notes runs recorded (notes / notes_mode /
  notes_indexed / notes_recalled / notes_read),
- normalized per-question JSON for both notes runs.

Usage:
  uv run --offline python go_hive/docs/e2e-assets/longmemeval/60q/summarize_lme_notes_60q.py \
    --go-notes ~/.hive-go/longmemeval-data/runs/go-60-notes/results.jsonl \
    --py-notes ~/.hive-go/longmemeval-data/runs/py-60-notes/results.jsonl \
    --go-base  ~/.hive-go/longmemeval-data/runs/go-60/results.jsonl \
    --py-base  ~/.hive-go/longmemeval-data/runs/py-60/results.jsonl \
    --json go_hive/docs/e2e-assets/longmemeval/60q-notes/summary-60q-notes.json \
    --per-question-dir go_hive/docs/e2e-assets/longmemeval/60q-notes
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
        if line:
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


def mcnemar_exact(a_only: int, b_only: int) -> float:
    discordant = a_only + b_only
    if not discordant:
        return 1.0
    k = min(a_only, b_only)
    tail = sum(math.comb(discordant, i) for i in range(k + 1)) / 2**discordant
    return min(1.0, 2 * tail)


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
    abst = [r for r in rows if r.get("abstention")]
    lo, hi = wilson(correct, len(rows))
    seconds = [float(r["seconds"]) for r in rows if isinstance(r.get("seconds"), (int, float))]
    stats: dict[str, Any] = {
        "questions": len(rows),
        "correct": correct,
        "accuracy": correct / len(rows) if rows else 0.0,
        "wilson95": [lo, hi],
        "task_averaged": statistics.mean([v["correct"] / v["n"] for v in types.values()]) if types else 0.0,
        "by_type": types,
        "abstention": {"n": len(abst), "correct": sum(1 for r in abst if r.get("correct") is True)},
        "errors": sum(1 for r in rows if r.get("error")),
        "ungraded": sum(1 for r in rows if r.get("judge_error")),
        "median_seconds": statistics.median(seconds) if seconds else None,
        "searched": sum(1 for r in rows if r.get("searched") is True),
        "evidence_retrieved": sum(1 for r in rows if r.get("evidence_retrieved") is True),
        "ids": sorted(by_id),
    }
    notes_rows = [r for r in rows if isinstance(r.get("notes"), int)]
    if notes_rows:
        stats["notes_telemetry"] = {
            "rows_with_notes": len(notes_rows),
            "mean_notes": round(statistics.mean(r["notes"] for r in notes_rows), 1),
            "min_notes": min(r["notes"] for r in notes_rows),
            "max_notes": max(r["notes"] for r in notes_rows),
            "notes_modes": sorted({str(r.get("notes_mode")) for r in notes_rows}),
            "indexed": sum(1 for r in notes_rows if r.get("notes_indexed") is True),
            "recalled": sum(1 for r in notes_rows if r.get("notes_recalled") is True),
            "read_any": sum(1 for r in notes_rows if r.get("notes_read")),
            "read_total": sum(len(r.get("notes_read") or []) for r in notes_rows),
            "rows": [
                {
                    "question_id": r["question_id"],
                    "notes": r.get("notes"),
                    "notes_mode": r.get("notes_mode"),
                    "notes_indexed": r.get("notes_indexed"),
                    "notes_recalled": r.get("notes_recalled"),
                    "notes_read": r.get("notes_read"),
                }
                for r in sorted(notes_rows, key=lambda r: r["question_id"])
            ],
        }
    return stats


def compare(a_rows: list[dict[str, Any]], b_rows: list[dict[str, Any]], a_name: str, b_name: str) -> dict[str, Any]:
    a_by = {r["question_id"]: r for r in a_rows}
    b_by = {r["question_id"]: r for r in b_rows}
    shared = sorted(set(a_by) & set(b_by))
    both_correct = both_wrong = 0
    a_only: list[dict[str, Any]] = []
    b_only: list[dict[str, Any]] = []
    for qid in shared:
        a = a_by[qid].get("correct") is True
        b = b_by[qid].get("correct") is True
        if a and b:
            both_correct += 1
        elif not a and not b:
            both_wrong += 1
        elif a:
            a_only.append({"question_id": qid, "question_type": a_by[qid].get("question_type")})
        else:
            b_only.append({"question_id": qid, "question_type": a_by[qid].get("question_type")})
    return {
        "a": a_name,
        "b": b_name,
        "shared_questions": len(shared),
        "both_correct": both_correct,
        "both_wrong": both_wrong,
        f"{a_name}_only": a_only,
        f"{b_name}_only": b_only,
        f"{a_name}_only_n": len(a_only),
        f"{b_name}_only_n": len(b_only),
        "mcnemar_exact_p": mcnemar_exact(len(a_only), len(b_only)),
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
        for key in ("notes", "notes_mode", "notes_indexed", "notes_recalled", "notes_read"):
            if key in r:
                item[key] = r[key]
        if r.get("error"):
            item["error"] = r["error"]
        if r.get("judge_error"):
            item["judge_error"] = r["judge_error"]
        out.append(item)
    return out


def show(name: str, s: dict[str, Any]) -> None:
    lo, hi = s["wilson95"]
    print(
        f"[{name}] {s['questions']} questions, {s['correct']} correct = {s['accuracy'] * 100:.1f}% "
        f"(95% CI {lo * 100:.1f}-{hi * 100:.1f}), task-avg {s['task_averaged'] * 100:.1f}%, "
        f"median {s['median_seconds']}s, errors={s['errors']}, ungraded={s['ungraded']}"
    )
    for t, v in s["by_type"].items():
        print(f"  {t:26s} {v['correct']:2d}/{v['n']:<2d} = {pct(v['correct'], v['n'])}")
    print(
        f"  {'abstention (_abs)':26s} {s['abstention']['correct']:2d}/{s['abstention']['n']:<2d} "
        f"= {pct(s['abstention']['correct'], s['abstention']['n'])}"
    )
    tel = s.get("notes_telemetry")
    if tel:
        print(
            f"  notes: mean {tel['mean_notes']}/question (min {tel['min_notes']}, max {tel['max_notes']}), "
            f"modes={tel['notes_modes']}, indexed {tel['indexed']}/{tel['rows_with_notes']}, "
            f"recalled {tel['recalled']}/{tel['rows_with_notes']}, "
            f"read-any {tel['read_any']}/{tel['rows_with_notes']} (total {tel['read_total']} reads)"
        )


def show_compare(c: dict[str, Any]) -> None:
    a, b = c["a"], c["b"]
    print(
        f"[{a} vs {b}] shared={c['shared_questions']} both_correct={c['both_correct']} "
        f"both_wrong={c['both_wrong']} {a}_only={c[f'{a}_only_n']} {b}_only={c[f'{b}_only_n']} "
        f"(exact McNemar p={c['mcnemar_exact_p']:.3f})"
    )
    for q in c[f"{a}_only"]:
        print(f"  {a} only: {q['question_id']:16s} {q['question_type']}")
    for q in c[f"{b}_only"]:
        print(f"  {b} only: {q['question_id']:16s} {q['question_type']}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go-notes", type=Path, required=True)
    parser.add_argument("--py-notes", type=Path, required=True)
    parser.add_argument("--go-base", type=Path, required=True)
    parser.add_argument("--py-base", type=Path, required=True)
    parser.add_argument("--json", type=Path, default=None)
    parser.add_argument("--per-question-dir", type=Path, default=None)
    args = parser.parse_args()

    groups = {
        "go_base": read_rows(args.go_base),
        "py_base": read_rows(args.py_base),
        "go_notes": read_rows(args.go_notes),
        "py_notes": read_rows(args.py_notes),
    }
    stats = {name: side_stats(rows) for name, rows in groups.items()}

    summary: dict[str, Any] = {"groups": stats, "comparisons": {}}
    comparisons = {
        "go_notes_vs_py_notes": ("go_notes", "py_notes"),
        "go_notes_vs_go_base": ("go_notes", "go_base"),
        "py_notes_vs_py_base": ("py_notes", "py_base"),
        "go_base_vs_py_base": ("go_base", "py_base"),
    }
    for name, (a, b) in comparisons.items():
        summary["comparisons"][name] = compare(groups[a], groups[b], a, b)

    for name in ("go_base", "py_base", "go_notes", "py_notes"):
        show(name, stats[name])
        print()
    for name in comparisons:
        show_compare(summary["comparisons"][name])
        print()

    if args.json:
        args.json.parent.mkdir(parents=True, exist_ok=True)
        args.json.write_text(json.dumps(summary, indent=1, ensure_ascii=False), encoding="utf-8")
        print(f"wrote {args.json}")
    if args.per_question_dir:
        args.per_question_dir.mkdir(parents=True, exist_ok=True)
        for name in ("go_notes", "py_notes"):
            (args.per_question_dir / f"{name.replace('_', '-')}-per-question.json").write_text(
                json.dumps(per_question(groups[name]), indent=1, ensure_ascii=False), encoding="utf-8"
            )
        print(f"wrote per-question JSON to {args.per_question_dir}")


if __name__ == "__main__":
    main()
