#!/usr/bin/env python3
"""Compare the Go and Python halves of the memory double run.

Reads the deterministic gateway's request logs and the timeline files each run
wrote, then reports whether the two implementations send the same prompts and
store the same schema. Text differences that are configuration (model name,
queen id, JSON spacing) are called out separately from semantic differences.

Usage:
  uv run --no-project python go_hive/scripts/memory_e2e_compare.py \
      --go /tmp/memory-e2e-go --py /tmp/memory-e2e-py --out <dir>
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from pathlib import Path

TIMELINE_SYSTEM = "You build a dated timeline of what a user has told an assistant about their life."


def load_requests(path: Path) -> list[dict]:
    if not path.is_file():
        raise SystemExit(f"missing gateway log: {path}")
    out = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.strip():
            out.append(json.loads(line))
    return out


def chat_parts(body: str) -> tuple[str, list[dict]]:
    payload = json.loads(body)
    messages = payload.get("messages") or []
    system = "\n".join(
        message.get("content", "")
        for message in messages
        if message.get("role") == "system" and isinstance(message.get("content"), str)
    )
    return system, messages


def user_text(messages: list[dict]) -> str:
    for message in reversed(messages):
        if message.get("role") != "user":
            continue
        content = message.get("content")
        if isinstance(content, str):
            return content
        if isinstance(content, list):
            return "".join(part.get("text", "") for part in content if isinstance(part, dict))
    return ""


def timeline_requests(records: list[dict]) -> list[tuple[str, str]]:
    out = []
    for record in records:
        if record.get("kind") != "timeline":
            continue
        system, messages = chat_parts(record["body"])
        out.append((system, user_text(messages)))
    return out


def recall_requests(records: list[dict]) -> list[tuple[str, str]]:
    out = []
    for record in records:
        if record.get("kind") != "recall":
            continue
        system, messages = chat_parts(record["body"])
        out.append((system, user_text(messages)))
    return out


def sha(text: str) -> str:
    return hashlib.sha256(text.encode()).hexdigest()[:16]


def session_block(prompt: str, session_id: str) -> str | None:
    """Extract the labelled block for one session, normalising the S-label."""
    # Prompts look like:
    #   Session S1, 2025-01-01 (Wednesday)
    #   [1] ...
    #   [2] ...
    # blank line between sessions
    pattern = re.compile(
        r"Session (S\d+), (\d{4}-\d{2}-\d{2}) \(([^)]+)\)\n((?:\[\d+\] [^\n]*\n?)+)"
    )
    for match in pattern.finditer(prompt + "\n"):
        body = match.group(4).strip().splitlines()
        if len(body) < 2:
            continue
        # The crafted session is the only one dated 2025-01-01.
        if match.group(2) == "2025-01-01":
            return "Session S?, " + match.group(2) + " (" + match.group(3) + ")\n" + "\n".join(body)
    return None


def timeline_items(path: Path) -> list[dict]:
    if not path.is_file():
        return []
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def item_signature(items: list[dict]) -> set[tuple]:
    return {
        (item.get("turn"), item.get("kind"), item.get("text"), item.get("start"), item.get("end"), tuple(item.get("aliases") or []))
        for item in items
    }


def block_from_transcript(path: Path, needle: str) -> str:
    """The injected block itself, not the model's truncated echo of it."""
    if not path.is_file():
        return ""
    parts = json.loads(path.read_text(encoding="utf-8"))
    best = ""
    for part in parts:
        content = part.get("content")
        if not isinstance(content, str) or needle not in content:
            continue
        if part.get("role") == "user" and len(content) > len(best):
            best = content
    return best


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--go", default="/tmp/memory-e2e-go")
    parser.add_argument("--py", default="/tmp/memory-e2e-py")
    parser.add_argument("--out", default="")
    args = parser.parse_args()

    go_dir, py_dir = Path(args.go), Path(args.py)
    go_records = load_requests(go_dir / "go-gateway-requests-main.jsonl")
    py_records = load_requests(py_dir / "py-gateway-requests.jsonl")

    go_timeline = timeline_requests(go_records)
    py_timeline = timeline_requests(py_records)
    go_recall = recall_requests(go_records)
    py_recall = recall_requests(py_records)
    if not go_timeline or not py_timeline:
        raise SystemExit("one side produced no timeline extraction request")
    if not go_recall or not py_recall:
        raise SystemExit("one side produced no recall selection request")

    go_system, go_prompt = go_timeline[0]
    py_system, py_prompt = py_timeline[0]
    results: list[tuple[str, bool, str]] = []

    results.append(("timeline system prompt identical", go_system == py_system, f"{sha(go_system)} vs {sha(py_system)}"))
    results.append(("timeline system prompt carries the schema", TIMELINE_SYSTEM in go_system and TIMELINE_SYSTEM in py_system, ""))

    def find_block(prompts: list[str]) -> str | None:
        for prompt in prompts:
            block = session_block(prompt, "meme2e01")
            if block:
                return block
        return None

    go_block = find_block([prompt for _, prompt in go_timeline])
    py_block = find_block([prompt for _, prompt in py_timeline])
    results.append(("crafted-session prompt block identical", bool(go_block) and go_block == py_block, f"go={bool(go_block)} py={bool(py_block)}"))

    go_recall_system, go_recall_user = go_recall[0]
    py_recall_system, py_recall_user = py_recall[0]
    # Python requests structured output via response_format; Go's JSONMode
    # adds the prompt-engineering suffix instead. Compare the base prompt.
    json_suffix = "\n\nPlease respond with a valid JSON object."
    go_base = go_recall_system.removesuffix(json_suffix)
    py_base = py_recall_system.removesuffix(json_suffix)
    results.append(("recall base system prompt identical", go_base == py_base, f"{sha(go_base)} vs {sha(py_base)}"))
    results.append(
        (
            "recall user message format identical",
            go_recall_user.split("## Available memories")[0].strip()
            == py_recall_user.split("## Available memories")[0].strip(),
            "",
        )
    )
    go_manifest = sorted((go_recall_user.split("## Available memories", 1) + [""])[1].strip().splitlines())
    py_manifest = sorted((py_recall_user.split("## Available memories", 1) + [""])[1].strip().splitlines())
    results.append(("recall manifest lines identical", go_manifest == py_manifest, f"{len(go_manifest)} vs {len(py_manifest)} lines"))

    go_file_items = timeline_items(go_dir / "go-timeline-backfill-2025.jsonl")
    py_file_items = timeline_items(py_dir / "py-timeline-backfill-2025.jsonl")
    results.append(("backfill timeline items semantically identical", item_signature(go_file_items) == item_signature(py_file_items), f"{len(go_file_items)} vs {len(py_file_items)} items"))
    go_keys = sorted(go_file_items[0].keys()) if go_file_items else []
    py_keys = sorted(py_file_items[0].keys()) if py_file_items else []
    results.append(("timeline item schema (keys) identical", go_keys == py_keys, ",".join(go_keys)))

    go_meta = json.loads((go_dir / "go-timeline-s2-stop-meta.json").read_text())
    py_meta = json.loads((py_dir / "py-timeline-s2-stop-meta.json").read_text())
    results.append(("timeline_meta schema (keys) identical", sorted(go_meta.keys()) == sorted(py_meta.keys()), ",".join(sorted(go_meta.keys()))))
    results.append(("timeline_meta version/user_turns agree", go_meta.get("version") == py_meta.get("version") == 1 and go_meta.get("user_turns") == py_meta.get("user_turns") == 1, f"go={go_meta} py={py_meta}"))

    go_stop = timeline_items(go_dir / "go-timeline-s2-stop-flush.jsonl")
    py_stop = timeline_items(py_dir / "py-timeline-s2-stop-flush.jsonl")
    results.append(("stop flush wrote the same item texts", {i.get("text") for i in go_stop} == {i.get("text") for i in py_stop}, f"{len(go_stop)} vs {len(py_stop)} items"))

    go_excerpt = block_from_transcript(go_dir / "go-conversation-s6-topk.json", "Excerpts from earlier conversations")
    py_excerpt = block_from_transcript(py_dir / "py-conversation-s6-topk.json", "Excerpts from earlier conversations")
    go_dated = re.findall(r"^- \d{4}-\d{2}-\d{2} \([A-Z][a-z]{2}\), session_", go_excerpt, re.M)
    py_dated = re.findall(r"^- \d{4}-\d{2}-\d{2} \([A-Z][a-z]{2}\), session_", py_excerpt, re.M)
    results.append(("TopK excerpt lines = 6 on both sides", len(go_dated) == 6 and len(py_dated) == 6, f"go={len(go_dated)} py={len(py_dated)}"))
    def intro(block: str) -> str:
        for line in block.splitlines():
            if line.startswith("Excerpts from earlier conversations"):
                return line
        return ""

    results.append(("excerpt block introduction identical", bool(intro(go_excerpt)) and intro(go_excerpt) == intro(py_excerpt), ""))

    width = max(len(name) for name, _, _ in results)
    lines = ["| check | result | detail |", "| --- | --- | --- |"]
    failed = 0
    for name, ok, detail in results:
        lines.append(f"| {name} | {'PASS' if ok else 'FAIL'} | {detail} |")
        if not ok:
            failed += 1
        print(f"{'PASS' if ok else 'FAIL'}  {name:<{width}}  {detail}")

    # Config differences are expected and must not be counted as failures.
    config_notes = [
        "recall selector wire prompt: Go appends the JSONMode hint "
        f"({'suffix present' if go_recall_system.endswith(json_suffix) else 'absent'}), "
        f"Python sends response_format instead ({'suffix present' if py_recall_system.endswith(json_suffix) else 'absent'})",
        f"timeline meta model: go={go_meta.get('model')!r} py={py_meta.get('model')!r} (configured model names)",
        f"timeline item JSON encoding: go=compact py=spaced (both `json.dumps`/`Encoder` defaults)",
        "recall injection timing: Go computes per turn synchronously; Python caches and refreshes in the background",
    ]
    lines += ["", "### Expected configuration differences", ""] + [f"- {note}" for note in config_notes]
    report = "\n".join(lines) + "\n"

    out = Path(args.out) if args.out else None
    if out:
        out.mkdir(parents=True, exist_ok=True)
        (out / "memory-e2e-compare.md").write_text(report, encoding="utf-8")
    print()
    print(report)
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
