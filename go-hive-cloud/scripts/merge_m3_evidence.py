#!/usr/bin/env python3
"""Merge the two M3 L2 harness reports into evidence/m3-smoke.json."""

from __future__ import annotations

import json
import sys
from datetime import datetime, timezone
from pathlib import Path


def load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def main(argv: list[str]) -> int:
    if len(argv) != 4:
        print("usage: merge_m3_evidence.py <rotation.json> <llm.json> <out.json>", file=sys.stderr)
        return 2
    rotation, llm, out = map(Path, argv[1:])
    docs = [load(rotation), load(llm)]
    checks = [check for doc in docs for check in doc.get("checks", [])]
    ok = bool(checks) and all(check.get("ok") for check in checks) and all(doc.get("ok") for doc in docs)
    started = [doc.get("started_at", "") for doc in docs if doc.get("started_at")]
    finished = [doc.get("finished_at", "") for doc in docs if doc.get("finished_at")]
    evidence = {
        "harness": "m3-smoke (streamToken rotation + hive LLM credential validation)",
        "base_url": docs[0].get("base_url", ""),
        "base_url_without_previous_secret": docs[0].get("base_url_without_previous_secret", ""),
        "started_at": min(started) if started else datetime.now(timezone.utc).isoformat(),
        "finished_at": max(finished) if finished else datetime.now(timezone.utc).isoformat(),
        "ok": ok,
        "checks": checks,
        "components": [doc.get("harness", "") for doc in docs],
    }
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
    failed = [check.get("name") for check in checks if not check.get("ok")]
    print(json.dumps({"ok": ok, "checks": len(checks), "failed": failed}))
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
