#!/usr/bin/env python3
"""Canonicalise and diff tool *results* (success and failure envelopes).

Both runtimes are captured through their own `serve` against a scripted fake
OpenAI gateway (see capture_tool_output.py), so the compared text is exactly
what the model sees as the `role: tool` message content. That text is not
directly comparable across runs: it carries session ids, timestamps, absolute
paths and pids. This module applies one documented set of canonical rules
(docs/tool-output-contract.md §2) and reports, per scenario, whether the two
sides agree.

Usage:
    python3 go_hive/scripts/tool_output_golden.py report \
        --scenarios go_hive/docs/e2e-assets/tool-output/scenarios.json \
        --raw go_hive/docs/e2e-assets/tool-output/raw \
        --out go_hive/docs/e2e-assets/tool-output

    python3 go_hive/scripts/tool_output_golden.py report --check   # exit 1 on drift

`--check` re-derives the canonical results from the committed raw captures and
fails when results.json / diff.json no longer match, so the artifacts cannot
silently rot.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent

# ── canonical rules ────────────────────────────────────────────────────────
#
# Applied to both sides with no side-specific cases. Every rule exists to
# remove a value that cannot be identical across two processes, never to hide a
# difference in shape, key set, types or wording.

SESSION_RE = re.compile(r"session_\d{8}_\d{6}_[0-9a-f]{8}")
CALL_RE = re.compile(r"call_\d+")
ISO_RE = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})")
DATE_RE = re.compile(r"\d{4}-\d{2}-\d{2}")
TIME_RE = re.compile(r"\b\d{2}:\d{2}:\d{2}\b")
UNIX_RE = re.compile(r"\b1[0-9]{9}\b")
CHAR_COUNT_RE = re.compile(r"returned [\d,]+ characters")
# The capture's fixture HTTP server and PTY prompt sentinel are minted per run.
LOCAL_URL_RE = re.compile(r"http://127\.0\.0\.1:\d+")
PTY_SENTINEL_RE = re.compile(r"__TERMINALTOOLS_PROMPT_[0-9a-f]+__")
REMINDER_RE = re.compile(r"\n*<system-reminder>.*?</system-reminder>\s*$", re.S)

# Values keyed by JSON member name. A key is masked only when its value is
# produced by the runtime (clock, session directory, process id); everything
# else is compared verbatim.
KEY_MASKS = {
    "session_id": "<SESSION_ID>",
    "datetime": "<DATETIME>",
    "date": "<DATE>",
    "time": "<TIME>",
    "day_of_week": "<DAY_OF_WEEK>",
    "unix_timestamp": "<UNIX_TS>",
    "created_at": "<CREATED_AT>",
    "updated_at": "<UPDATED_AT>",
    "started_at": "<STARTED_AT>",
    "resolved": "<RESOLVED_PATH>",
    "pid": "<PID>",
    "runtime_ms": "<RUNTIME_MS>",
    "output_handle": "<HANDLE>",
    "job_id": "<JOB_ID>",
    # Runtime-specific recovery advice: Python names pdfplumber/poppler, Go
    # names qpdf. The key set and the surrounding payload are the contract.
    "next_step": "<ADVISORY>",
    "terminal_hint": "<ADVISORY>",
}

PATH_MASKS = (
    # macOS reports /private/tmp for the same directory /tmp points at.
    ("/private/tmp", "/tmp"),
)
HOME_RE = re.compile(r"/tmp/[^\s\"']*?/(?:python|go)-home")
WS_RE = re.compile(r"/tmp/[^\s\"']*?/ws")


def mask_text(text: str) -> str:
    text = SESSION_RE.sub("session_<ID>", text)
    text = CALL_RE.sub("call_<ID>", text)
    text = ISO_RE.sub("<TIMESTAMP>", text)
    text = DATE_RE.sub("<DATE>", text)
    text = TIME_RE.sub("<TIME>", text)
    text = UNIX_RE.sub("<UNIX_TS>", text)
    text = CHAR_COUNT_RE.sub("returned <CHARS> characters", text)
    text = LOCAL_URL_RE.sub("<LOCAL_URL>", text)
    text = PTY_SENTINEL_RE.sub("<PTY_SENTINEL>", text)
    for needle, replacement in PATH_MASKS:
        text = text.replace(needle, replacement)
    text = HOME_RE.sub("<HOME>", text)
    text = WS_RE.sub("<WS>", text)
    return text


def canon_value(key: str, value):
    # Null stays null: "no handle" / "no job_id" is part of the contract, and
    # masking it would hide a type/shape difference.
    if key in KEY_MASKS and value is not None:
        return KEY_MASKS[key]
    if isinstance(value, str):
        return mask_text(value)
    if isinstance(value, list):
        return [canon_value(key, item) for item in value]
    if isinstance(value, dict):
        return {member: canon_value(member, member_value) for member, member_value in value.items()}
    if isinstance(value, float) and value == int(value) and abs(value) < 1e15:
        # Python prints 2.0, Go prints 2 for the same float; compare the number.
        return int(value)
    return value


def strip_error_prefix(content: str) -> tuple[bool, str]:
    """Split the loop's is_error marker. Both runtimes prefix `ERROR: ` and the
    Python loop can double it (its tool text already starts with the marker)."""
    is_error = False
    while content.startswith("ERROR: "):
        is_error = True
        content = content[len("ERROR: "):]
    return is_error, content


def split_reminder(content: str) -> tuple[str, bool]:
    match = REMINDER_RE.search(content)
    if not match:
        return content, False
    return content[: match.start()], True


def split_leading_json(content: str) -> tuple[dict | None, str]:
    """Split `<json>\\n<trailer>` into its two parts.

    attach_file returns the upstream TextContent list flattened: the JSON
    summary followed by the text preview block.
    """
    stripped = content.lstrip(" \t\r\n")
    if not stripped.startswith("{"):
        return None, content
    decoder = json.JSONDecoder()
    try:
        payload, end = decoder.raw_decode(stripped)
    except ValueError:
        return None, content
    if not isinstance(payload, dict):
        return None, content
    return payload, stripped[end:]


def canonicalize(content: str) -> dict:
    """Canonical envelope: is_error flag, body form, canonical body, trailer."""
    content = content or ""
    is_error, body = strip_error_prefix(content)
    body, reminder = split_reminder(body)
    payload, trailer = split_leading_json(body)
    if payload is None:
        return {
            "is_error": is_error,
            "form": "text",
            "payload": mask_text(body.rstrip("\n")),
            "trailer": "",
            "reminder": reminder,
        }
    return {
        "is_error": is_error,
        "form": "json",
        "payload": json.dumps(canon_value("", payload), ensure_ascii=False, sort_keys=True, separators=(",", ":")),
        "trailer": mask_text(trailer.strip("\n")),
        "reminder": reminder,
    }


def shape_of(canonical: dict) -> dict:
    """Envelope shape: form, is_error, top-level key set and value types."""
    shape: dict = {"form": canonical["form"], "is_error": canonical["is_error"]}
    if canonical["form"] == "json":
        payload = json.loads(canonical["payload"])
        shape["keys"] = sorted(payload)
        shape["types"] = {key: type(payload[key]).__name__ for key in payload}
    return shape


# ── reporting ─────────────────────────────────────────────────────────────


def compare(scenario: dict, python: dict, go: dict) -> tuple[str, list[str]]:
    mode = scenario.get("compare", "exact")
    if scenario.get("exception") and mode == "none":
        return "recorded", []
    if mode == "none":
        return "recorded", []
    if mode == "shape":
        shape_python, shape_go = shape_of(python), shape_of(go)
        if shape_python != shape_go:
            return "different", [f"shape: python={shape_python} go={shape_go}"]
        return "shape-aligned", []
    if mode == "lines":
        # Same matches, order-insensitive: each runtime walks the tree its own
        # way (Python shells out to ripgrep, Go walks natively).
        differences = []
        for field in ("is_error", "form", "trailer"):
            if python[field] != go[field]:
                differences.append(field)
        py_lines = sorted(line for line in python["payload"].splitlines() if line.strip())
        go_lines = sorted(line for line in go["payload"].splitlines() if line.strip())
        if py_lines != go_lines:
            differences.append("lines")
        return ("aligned" if not differences else "different"), differences
    differences: list[str] = []
    # `reminder` is loop noise (task reminders), recorded for diagnostics only.
    for field in ("is_error", "form", "payload", "trailer"):
        if python[field] != go[field]:
            differences.append(field)
    return ("aligned" if not differences else "different"), differences


def build_report(scenarios_doc: dict, raw: dict) -> dict:
    scenarios = scenarios_doc["scenarios"]
    report = {
        "schema_version": 1,
        "note": "Canonical tool-result envelopes per scenario (docs/tool-output-contract.md).",
        "counts": {},
        "scenarios": {},
    }
    status_counts: dict[str, int] = {}
    for scenario in scenarios:
        scenario_id = scenario["id"]
        python_raw = (raw["python"].get(scenario_id) or {}).get("content")
        go_raw = (raw["go"].get(scenario_id) or {}).get("content")
        if python_raw is None or go_raw is None:
            report["scenarios"][scenario_id] = {
                "tool": scenario["tool"],
                "arguments": scenario.get("arguments"),
                "status": "missing-capture",
            }
            status_counts["missing-capture"] = status_counts.get("missing-capture", 0) + 1
            continue
        python_canonical = canonicalize(python_raw)
        go_canonical = canonicalize(go_raw)
        status, differences = compare(scenario, python_canonical, go_canonical)
        status_counts[status] = status_counts.get(status, 0) + 1
        entry = {
            "tool": scenario["tool"],
            "arguments": scenario.get("arguments"),
            "compare": scenario.get("compare", "exact"),
            "status": status,
            "python": python_canonical,
            "go": go_canonical,
        }
        if differences:
            entry["differences"] = differences
        if scenario.get("exception"):
            entry["exception"] = scenario["exception"]
        report["scenarios"][scenario_id] = entry
    report["counts"] = {
        "scenarios": len(scenarios),
        **{status: status_counts.get(status, 0) for status in sorted(status_counts)},
        "exceptions": sum(1 for entry in report["scenarios"].values() if entry.get("exception")),
    }
    return report


def render_summary(report: dict) -> str:
    counts = report["counts"]
    lines = [
        "tool output golden — Python reference vs go_hive",
        "",
        f"scenarios: {counts.get('scenarios', 0)}",
        f"aligned:   {counts.get('aligned', 0)}",
        f"shape-aligned (tool libraries differ): {counts.get('shape-aligned', 0)}",
        f"recorded divergences (reviewed exceptions): {counts.get('recorded', 0)}",
        f"different (unexplained): {counts.get('different', 0)}",
        "",
        f"{'scenario':38} {'tool':18} {'status':16} differs",
    ]
    for scenario_id, entry in report["scenarios"].items():
        differs = ",".join(entry.get("differences") or [])
        lines.append(f"{scenario_id:38} {entry.get('tool', ''):18} {entry.get('status', ''):16} {differs}")
    return "\n".join(lines) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=["report"])
    artifact_dir = HERE.parent / "docs/e2e-assets/tool-output"
    parser.add_argument("--scenarios", default=str(artifact_dir / "scenarios.json"))
    parser.add_argument("--raw", default=str(artifact_dir / "raw"))
    parser.add_argument("--out", default=str(artifact_dir))
    parser.add_argument("--check", action="store_true", help="fail when the committed artifacts are stale")
    args = parser.parse_args()

    scenarios_doc = json.loads(Path(args.scenarios).read_text(encoding="utf-8"))
    raw_dir = Path(args.raw)
    raw = {
        "python": json.loads((raw_dir / "python-results.json").read_text(encoding="utf-8")),
        "go": json.loads((raw_dir / "go-results.json").read_text(encoding="utf-8")),
    }
    report = build_report(scenarios_doc, raw)
    summary = render_summary(report)
    diff = {
        "counts": report["counts"],
        "different": {
            scenario_id: entry
            for scenario_id, entry in report["scenarios"].items()
            if entry["status"] == "different"
        },
        "exceptions": {
            scenario_id: entry["exception"]
            for scenario_id, entry in report["scenarios"].items()
            if entry.get("exception")
        },
    }
    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    rendered = {
        "results.json": json.dumps(report, ensure_ascii=False, indent=1) + "\n",
        "diff.json": json.dumps(diff, ensure_ascii=False, indent=1) + "\n",
        "summary.txt": summary,
    }
    stale = []
    for name, text in rendered.items():
        path = out_dir / name
        if args.check:
            if not path.is_file() or path.read_text(encoding="utf-8") != text:
                stale.append(name)
            continue
        path.write_text(text, encoding="utf-8")
    if args.check:
        if stale:
            print(f"tool output golden: stale artifacts: {', '.join(stale)}", file=sys.stderr)
            return 1
        print(f"tool output golden: OK ({report['counts']})")
        return 0
    print(summary)
    if report["counts"].get("different"):
        print("unexplained differences present", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
