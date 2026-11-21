#!/usr/bin/env python3
"""Render / verify the judge prompt golden shared by llmjudge and the Python reference.

Why: the judge prompt is the one judge surface both implementations send to the
same model for the same contract. Go's ``llmjudge.buildPrompt`` is a literal port
of ``framework/testing/llm_judge.py::LLMJudge.evaluate``; without a frozen golden
a one-word edit on either side silently diverges.

The golden stores the prompt *template* with ``%s`` placeholders, so each side can
check it without the other tree being present:

  * Python (this script): extract the f-string from ``LLMJudge.evaluate``, rewrite
    ``{constraint}`` / ``{criteria}`` / ``{source_document}`` / ``{summary}`` to
    ``%s`` and unescape the doubled JSON braces, then compare/write the golden.
  * Go: ``internal/llmjudge/prompt_parity_golden_test.go`` calls
    ``buildPrompt("%s", "%s", "%s", "%s")`` and compares against the same file.

Usage:
    uv run --no-project python go_hive/scripts/judge_prompt_golden.py           # check
    uv run --no-project python go_hive/scripts/judge_prompt_golden.py --write   # regenerate

Exit codes: 0 = golden matches the Python reference; 1 = drift; 2 = roots invalid.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

SCRIPT = Path(__file__).resolve()
GO_HIVE = SCRIPT.parent.parent            # go_hive/
REPO_ROOT = GO_HIVE.parent                # repository root
PLACEHOLDERS = ("constraint", "criteria", "source_document", "summary")

PY_JUDGE = "core/framework/testing/llm_judge.py"
GOLDEN = "go_hive/go-hive/internal/llmjudge/testdata/prompt-parity/judge.prompt.txt"


def extract_python_template(source: str) -> str:
    """Return the LLMJudge prompt template with %s placeholders."""
    marker = 'prompt = f"""'
    start = source.find(marker)
    if start < 0:
        raise ValueError(f"{PY_JUDGE}: `{marker}` marker not found")
    start += len(marker)
    end = source.find('"""', start)
    if end < 0:
        raise ValueError(f"{PY_JUDGE}: unterminated prompt f-string")
    template = source[start:end]
    for name in PLACEHOLDERS:
        template = template.replace("{" + name + "}", "%s")
    # f-string escapes literal JSON braces as {{ }}; the rendered prompt has one.
    template = template.replace("{{", "{").replace("}}", "}")
    return template


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Verify the judge prompt golden against the Python reference.")
    parser.add_argument("--write", action="store_true", help="regenerate the golden from the Python reference")
    parser.add_argument("--repo-root", default=None, help="repository root (defaults to this script's repo)")
    args = parser.parse_args(argv)

    repo_root = Path(args.repo_root).resolve() if args.repo_root else REPO_ROOT
    py_path = repo_root / PY_JUDGE
    golden_path = repo_root / GOLDEN
    if not py_path.is_file():
        print(f"judge_prompt_golden: Python reference not found: {py_path}", file=sys.stderr)
        return 2

    try:
        template = extract_python_template(py_path.read_text(encoding="utf-8"))
    except ValueError as exc:
        print(f"judge_prompt_golden: {exc}", file=sys.stderr)
        return 2

    if args.write:
        golden_path.parent.mkdir(parents=True, exist_ok=True)
        golden_path.write_text(template, encoding="utf-8")
        print(f"judge_prompt_golden: wrote {golden_path.relative_to(repo_root)} ({len(template)} chars)")
        return 0

    if not golden_path.is_file():
        print(
            f"judge_prompt_golden: golden missing: {golden_path} "
            "(regenerate with --write)",
            file=sys.stderr,
        )
        return 1
    golden = golden_path.read_text(encoding="utf-8")
    if golden != template:
        print("judge_prompt_golden: DRIFT — Go golden no longer matches the Python prompt", file=sys.stderr)
        print(f"  golden: {golden!r}", file=sys.stderr)
        print(f"  python: {template!r}", file=sys.stderr)
        return 1
    print(f"judge_prompt_golden: OK — {len(template)} chars match {PY_JUDGE} and {GOLDEN}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
