#!/usr/bin/env python3
"""Python half of the vision-caption byte-parity vectors.

Runs the reference implementation's real functions on the fixed vectors in
``docs/e2e-assets/vision-caption/vectors.json``:

* ``extract_intent_for_tool`` — the intent string handed to the vision model;
* ``remap_caption_for_crop`` — crop-relative → viewport-fraction coordinates;
* the tool-result splice from ``agent_loop.py`` (whose two f-string literals
  are asserted to still exist before the bytes are rebuilt).

Writes ``{"vectors": [{id, intent, stored_content}]}``, the same shape
``TestVisionCaptionParityVectors`` writes on the Go side, so the two files can
be diffed byte for byte.

Run from the repository's ``core`` directory:

    uv run --offline python ../go_hive/scripts/vision_caption_python_vectors.py \
        --vectors ../docs/e2e-assets/vision-caption/vectors.json \
        --out ../docs/e2e-assets/vision-caption/python-vectors.json
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from types import SimpleNamespace

AGENT_LOOP = Path(__file__).resolve().parents[2] / "core" / "framework" / "agent_loop" / "agent_loop.py"

MARKER_LITERAL = 'vision_fallback_marker = f"[vision-fallback caption]\\n{caption}"'
SPLICE_LITERAL = 'stored_content = f"{stored_content or \'\'}\\n\\n{vision_fallback_marker}"'


def assert_splice_literals(source: str) -> None:
    for literal in (MARKER_LITERAL, SPLICE_LITERAL):
        if literal not in source:
            raise SystemExit(
                "the reference agent loop no longer contains the pinned splice literal:\n  " + literal
            )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--vectors", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--agent-loop", default=str(AGENT_LOOP))
    args = parser.parse_args()

    assert_splice_literals(Path(args.agent_loop).read_text(encoding="utf-8"))

    from framework.agent_loop.internals.vision_fallback import (
        extract_intent_for_tool,
        remap_caption_for_crop,
    )

    vectors = json.loads(Path(args.vectors).read_text(encoding="utf-8"))["vectors"]
    results = []
    for vector in vectors:
        messages = []
        if vector.get("assistant_text"):
            messages.append(SimpleNamespace(role="assistant", content=vector["assistant_text"]))
        conversation = SimpleNamespace(_messages=messages)
        intent = extract_intent_for_tool(conversation, vector["tool_name"], vector.get("tool_args") or {})
        remapped = remap_caption_for_crop(vector.get("caption") or "", vector.get("tool_content") or "")
        # The two f-strings asserted above, in order: marker, then splice.
        marker = f"[vision-fallback caption]\n{remapped}"
        stored = f"{(vector.get('tool_content') or '')}\n\n{marker}"
        results.append({"id": vector["id"], "intent": intent, "stored_content": stored})

    Path(args.out).write_text(json.dumps({"vectors": results}, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"wrote {len(results)} vectors to {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())