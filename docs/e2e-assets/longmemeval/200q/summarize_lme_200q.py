#!/usr/bin/env python3
"""Recompute the LongMemEval n=200 comparison (Go/Py x notes/no-notes).

Reads each group's results.jsonl (same row schema on both sides: question_id,
question_type, answer, hypothesis, correct, judge, seconds, optional
error/judge_error, notes telemetry) and recomputes every number in the report:

  * overall accuracy with Wilson 95% CI, per-type accuracy, abstention,
    searched/evidence-retrieved, errors/ungraded, median seconds per group;
  * every pairwise comparison on shared ids (2x2 table + exact McNemar);
  * agreement with the n=60 run on the shared 60-question subset (per-question
    agreement and accuracy shift), when --prior label=path is given;
  * normalized per-question JSON per group and a collapsible markdown table.

Usage:
  uv run --offline python 200q/summarize_lme_200q.py \
    --run go-base=~/.hive-go/longmemeval-data/runs/go-200/results.jsonl \
    --run py-base=~/.hive-go/longmemeval-data/runs/py-200/results.jsonl \
    --run go-notes=~/.hive-go/longmemeval-data/runs/go-200-notes/results.jsonl \
    --prior go-base=60q/go-60-results.jsonl --prior py-base=60q/py-60-results.jsonl \
    --json 200q/summary-200q.json --per-question-dir 200q --markdown-out 200q/per-question-200q.md
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
TYPE_ABBR = {
    "single-session-user": "SSU",
    "single-session-assistant": "SSA",
    "multi-session": "MS",
    "temporal-reasoning": "TR",
    "knowledge-update": "KU",
    "single-session-preference": "SP",
}


def read_rows(path: Path) -> list[dict[str, Any]]:
    rows = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line:
            rows.append(json.loads(line))
    return rows


def dedupe(rows: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    by_id: dict[str, dict[str, Any]] = {}
    for row in rows:
        by_id[row["question_id"]] = row  # last row for an id wins (retry rows)
    return by_id


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


def group_stats(by_id: dict[str, dict[str, Any]]) -> dict[str, Any]:
    rows = list(by_id.values())
    correct = sum(1 for r in rows if r.get("correct") is True)
    types: dict[str, dict[str, int]] = {}
    for t in TYPES + sorted({str(r.get("question_type")) for r in rows} - set(TYPES)):
        subset = [r for r in rows if r.get("question_type") == t]
        if subset:
            types[t] = {
                "n": len(subset),
                "correct": sum(1 for r in subset if r.get("correct") is True),
            }
    abst = [r for r in rows if r.get("abstention")]
    seconds = [float(r["seconds"]) for r in rows if isinstance(r.get("seconds"), (int, float))]
    lo, hi = wilson(correct, len(rows))
    searched = sum(1 for r in rows if r.get("searched") is True)
    evidence = sum(1 for r in rows if r.get("evidence_retrieved") is True)
    type_rates = [v["correct"] / v["n"] for v in types.values() if v["n"]]
    notes_rows = [r for r in rows if isinstance(r.get("notes"), (int, float))]
    stats: dict[str, Any] = {
        "questions": len(rows),
        "correct": correct,
        "accuracy": correct / len(rows) if rows else 0.0,
        "task_averaged": sum(type_rates) / len(type_rates) if type_rates else 0.0,
        "wilson95": [lo, hi],
        "by_type": types,
        "abstention": {"n": len(abst), "correct": sum(1 for r in abst if r.get("correct") is True)},
        "errors": sum(1 for r in rows if r.get("error")),
        "ungraded": sum(1 for r in rows if r.get("judge_error")),
        "searched": {"n": searched, "rate": searched / len(rows) if rows else 0.0},
        "evidence_retrieved": {"n": evidence, "rate": evidence / len(rows) if rows else 0.0},
        "median_seconds": statistics.median(seconds) if seconds else None,
        "total_seconds": sum(seconds) if seconds else None,
        "ids": sorted(by_id),
    }
    if notes_rows:
        stats["notes_telemetry"] = {
            "questions": len(notes_rows),
            "notes_mean": sum(float(r["notes"]) for r in notes_rows) / len(notes_rows),
            "indexed_true": sum(1 for r in notes_rows if r.get("notes_indexed") is True),
            "recalled_true": sum(1 for r in notes_rows if r.get("notes_recalled") is True),
            "read_nonempty": sum(1 for r in notes_rows if r.get("notes_read")),
            "reader_used_notes": sum(
                1 for r in notes_rows if isinstance(r.get("notes_read"), list) and len(r["notes_read"]) > 0
            ),
        }
    return stats


def compare(a: dict[str, dict[str, Any]], b: dict[str, dict[str, Any]]) -> dict[str, Any]:
    shared = sorted(set(a) & set(b))
    both = only_a = only_b = both_wrong = 0
    disagreements = []
    for qid in shared:
        ca = a[qid].get("correct") is True
        cb = b[qid].get("correct") is True
        if ca and cb:
            both += 1
        elif ca:
            only_a += 1
        elif cb:
            only_b += 1
        else:
            both_wrong += 1
        if ca != cb:
            disagreements.append(qid)
    return {
        "shared_questions": len(shared),
        "both_correct": both,
        "both_wrong": both_wrong,
        "a_only": only_a,
        "b_only": only_b,
        "mcnemar_exact_p": mcnemar_exact(only_a, only_b),
        "a_minus_b_questions": only_a - only_b,
        "disagreement_ids": disagreements,
        "missing_in_a": sorted(set(b) - set(a)),
        "missing_in_b": sorted(set(a) - set(b)),
    }


def per_question_rows(by_id: dict[str, dict[str, Any]]) -> list[dict[str, Any]]:
    out = []
    for r in sorted(
        by_id.values(),
        key=lambda r: (TYPES.index(r["question_type"]) if r.get("question_type") in TYPES else 99,
                       str(r.get("question_id"))),
    ):
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
            "reader_model": r.get("reader_model"),
            "searched": r.get("searched"),
            "search_calls": r.get("search_calls"),
            "evidence_retrieved": r.get("evidence_retrieved"),
            "tools": r.get("tools"),
        }
        for key in ("notes", "notes_mode", "notes_indexed", "notes_recalled", "notes_read"):
            if r.get(key) is not None:
                item[key] = r.get(key)
        if r.get("error"):
            item["error"] = r["error"]
        if r.get("judge_error"):
            item["judge_error"] = r["judge_error"]
        out.append(item)
    return out


def short(text: Any, limit: int = 44) -> str:
    if text is None:
        return ""
    s = str(text).replace("\n", " ").replace("|", "\\|")
    return s if len(s) <= limit else s[: limit - 1] + "…"


def markdown_table(groups: list[tuple[str, dict[str, dict[str, Any]]]], sample: dict[str, Any]) -> str:
    order = [q["question_id"] for q in sample["questions"]]
    meta = {q["question_id"]: q for q in sample["questions"]}
    labels = [label for label, _ in groups]
    header = "| # | qid | type | expect (short) | " + " | ".join(labels) + " |"
    sep = "|---:|---|---|---|" + "|".join(":--:" for _ in labels) + "|"
    lines = [header, sep]
    for i, qid in enumerate(order, 1):
        q = meta[qid]
        cells = []
        for _, by_id in groups:
            hit = by_id.get(qid, {}).get("correct")
            cells.append("✅" if hit is True else ("❌" if hit is False else "—"))
        expect = short(by_id_of(groups, qid).get("answer"))
        lines.append(
            f"| {i} | `{qid}` | {TYPE_ABBR.get(q['question_type'], q['question_type'])} | {expect} | "
            + " | ".join(cells) + " |"
        )
    return "\n".join(lines)


def by_id_of(groups: list[tuple[str, dict[str, dict[str, Any]]]], qid: str) -> dict[str, Any]:
    for _, by_id in groups:
        if qid in by_id:
            return by_id[qid]
    return {}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", action="append", default=[], metavar="LABEL=PATH")
    parser.add_argument("--prior", action="append", default=[], metavar="LABEL=PATH")
    parser.add_argument("--sample", type=Path, default=Path(__file__).with_name("sample-n200.json"))
    parser.add_argument("--json", type=Path, default=None)
    parser.add_argument("--per-question-dir", type=Path, default=None)
    parser.add_argument("--markdown-out", type=Path, default=None)
    args = parser.parse_args()

    if not args.run:
        raise SystemExit("--run LABEL=PATH is required")
    groups: list[tuple[str, dict[str, dict[str, Any]]]] = []
    raw: dict[str, list[dict[str, Any]]] = {}
    for spec in args.run:
        label, _, path = spec.partition("=")
        rows = read_rows(Path(path).expanduser())
        raw[label] = rows
        groups.append((label, dedupe(rows)))

    priors: dict[str, dict[str, dict[str, Any]]] = {}
    prior_paths: dict[str, str] = {}
    for spec in args.prior:
        label, _, path = spec.partition("=")
        prior_paths[label] = path
        priors[label] = dedupe(read_rows(Path(path).expanduser()))

    sample = json.loads(args.sample.read_text(encoding="utf-8"))
    sample_ids = sample["question_ids"]

    summary: dict[str, Any] = {"groups": {}, "comparisons": {}, "prior_consistency": {}}
    for label, by_id in groups:
        stats = group_stats(by_id)
        stats["missing_sample_ids"] = [qid for qid in sample_ids if qid not in by_id]
        stats["extra_ids"] = sorted(set(by_id) - set(sample_ids))
        summary["groups"][label] = stats

    for i, (la, a) in enumerate(groups):
        for lb, b in groups[i + 1:]:
            summary["comparisons"][f"{la}_vs_{lb}"] = compare(a, b)

    # Same-side notes effect and cross-side notes-on comparison are just the
    # pairwise entries; nothing extra to compute.

    for label, by_id in groups:
        prior = priors.get(label)
        if not prior:
            continue
        shared = sorted(set(prev for prev in prior) & set(by_id))
        prior_correct = sum(1 for qid in shared if prior[qid].get("correct") is True)
        now_correct = sum(1 for qid in shared if by_id[qid].get("correct") is True)
        agree = sum(1 for qid in shared if (prior[qid].get("correct") is True) == (by_id[qid].get("correct") is True))
        flipped_right = [qid for qid in shared if prior[qid].get("correct") is not True and by_id[qid].get("correct") is True]
        flipped_wrong = [qid for qid in shared if prior[qid].get("correct") is True and by_id[qid].get("correct") is not True]
        summary["prior_consistency"][label] = {
            "prior": prior_paths.get(label, ""),
            "shared_questions": len(shared),
            "prior_correct": prior_correct,
            "now_correct": now_correct,
            "agreement": agree / len(shared) if shared else 0.0,
            "flipped_right": flipped_right,
            "flipped_wrong": flipped_wrong,
        }

    if args.json:
        args.json.parent.mkdir(parents=True, exist_ok=True)
        args.json.write_text(json.dumps(summary, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
        print(f"wrote {args.json}")

    if args.per_question_dir:
        args.per_question_dir.mkdir(parents=True, exist_ok=True)
        for label, by_id in groups:
            path = args.per_question_dir / f"{label}-per-question.json"
            path.write_text(json.dumps(per_question_rows(by_id), indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
            print(f"wrote {path}")

    if args.markdown_out:
        args.markdown_out.write_text(markdown_table(groups, sample) + "\n", encoding="utf-8")
        print(f"wrote {args.markdown_out}")

    # ---- console report ----
    for label, by_id in groups:
        s = summary["groups"][label]
        lo, hi = s["wilson95"]
        print(f"[{label}] {s['questions']} q, {s['correct']} correct = {s['accuracy'] * 100:.1f}% "
              f"(95% CI {lo * 100:.1f}-{hi * 100:.1f}), task-avg {s['task_averaged'] * 100:.1f}%, "
              f"median {s['median_seconds']}s, "
              f"errors={s['errors']}, ungraded={s['ungraded']}, "
              f"searched={pct(s['searched']['n'], s['questions'])}, "
              f"evidence={pct(s['evidence_retrieved']['n'], s['questions'])}")
        if "notes_telemetry" in s:
            nt = s["notes_telemetry"]
            print(f"    notes: mean {nt['notes_mean']:.1f}/q, indexed {nt['indexed_true']}/{nt['questions']}, "
                  f"recalled {nt['recalled_true']}, reader-read-notes {nt['reader_used_notes']}/{nt['questions']}")
        for t in TYPES:
            if t in s["by_type"]:
                v = s["by_type"][t]
                print(f"    {t:26s} {v['correct']:3d}/{v['n']:<3d} = {pct(v['correct'], v['n'])}")
        print(f"    {'abstention (_abs)':26s} {s['abstention']['correct']:3d}/{s['abstention']['n']:<3d}")
        if s["missing_sample_ids"]:
            print(f"    MISSING {len(s['missing_sample_ids'])}: {','.join(s['missing_sample_ids'][:8])}")

    for name, c in summary["comparisons"].items():
        print(f"[{name}] shared={c['shared_questions']} both={c['both_correct']} both_wrong={c['both_wrong']} "
              f"a_only={c['a_only']} b_only={c['b_only']} diff={c['a_minus_b_questions']:+d} "
              f"McNemar p={c['mcnemar_exact_p']:.4f}")

    for label, c in summary["prior_consistency"].items():
        print(f"[prior {label}] n={c['shared_questions']} prior={c['prior_correct']} now={c['now_correct']} "
              f"agreement={c['agreement'] * 100:.1f}% (+{len(c['flipped_right'])}/-{len(c['flipped_wrong'])})")


if __name__ == "__main__":
    main()
