#!/usr/bin/env python3
"""Deterministic query corpus for paritydumps: first user message of the
first 40 sessions (sorted) of each queen scope, whitespace-collapsed, capped
at 72 chars, deduplicated in order. 120 lines total."""
import json
import os
import sys

home = sys.argv[1]
out = sys.argv[2]
owners = ["queen_alpha", "queen_beta", "queen_gamma"]
per_owner = 40
queries = []
seen = set()
for owner in owners:
    root = os.path.join(home, "queens", owner, "sessions")
    sessions = sorted(d for d in os.listdir(root) if d.startswith("session_"))
    taken = 0
    for session in sessions:
        if taken >= per_owner:
            break
        path = os.path.join(root, session, "events.jsonl")
        text = None
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if event.get("type") == "client_input_received":
                    text = event.get("data", {}).get("content", "")
                    break
        if not text:
            continue
        text = " ".join(text.split())
        if len(text) > 72:
            text = text[:72].rstrip()
        if text and text not in seen:
            seen.add(text)
            queries.append(text)
            taken += 1
        if len(queries) >= 3 * per_owner:
            break
    if len(queries) >= 3 * per_owner:
        break
with open(out, "w", encoding="utf-8") as fh:
    for query in queries:
        fh.write(query + "\n")
print(f"queries: {len(queries)} -> {out}")
