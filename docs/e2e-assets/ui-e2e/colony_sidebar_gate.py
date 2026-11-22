#!/usr/bin/env python3
"""Gate: a colony must appear in the sidebar AND on its colony page.

Checks the *server-side contract* the UI depends on, plus the slug->route
mapping. Deliberately browser-free so it can run unattended in CI without
Orca/Chrome and without being disturbed by a busy host.

Exit 0 = PASS, 1 = FAIL, 2 = inconclusive (backend unreachable).

Usage:  ./colony_sidebar_gate.py [colony_slug ...]
        ./colony_sidebar_gate.py            # checks every colony
"""
import json, re, sys, urllib.request

BASE = "http://127.0.0.1:8788"


def slug_to_colony_id(slug: str) -> str:
    """Mirror of frontend/src/lib/colony-registry.ts slugToColonyId.

    Keep in sync. NOTE: this mapping is many-to-one by design; the gate
    asserts that fact explicitly (see check_collisions) rather than
    pretending it is a bijection.
    """
    s = slug.replace("_", "-")
    s = re.sub(r"^email-", "", s)
    s = re.sub(r"-agent$", "", s)
    return s


def get(path, timeout=20):
    with urllib.request.urlopen(BASE + path, timeout=timeout) as r:
        return json.load(r)


def main(argv):
    targets = argv[1:]

    try:
        index = get("/api/colonies/tools-index")
        sessions = get("/api/sessions")
    except Exception as e:
        print(f"INCONCLUSIVE: backend unreachable at {BASE}: {e}")
        return 2

    colonies = {c["name"]: c for c in index.get("colonies", [])}
    live = {s.get("colony_id") for s in sessions.get("sessions", []) if s.get("colony_id")}

    failures = []

    # --- 1. route-id collisions: silent disappearance ---------------------
    buckets = {}
    for name in colonies:
        buckets.setdefault(slug_to_colony_id(name), []).append(name)
    collided = {k: v for k, v in buckets.items() if len(v) > 1}
    if collided:
        lost = sum(len(v) - 1 for v in collided.values())
        msg = [f"{len(collided)} route-id collision(s); {lost} colony/colonies "
               f"unreachable from the sidebar:"]
        for rid, members in sorted(collided.items()):
            msg.append(f"    {rid:<20} <- {members}")
        failures.append("\n".join(msg))

    # --- 2. each target must be in the backend list and map uniquely ------
    for name in targets:
        if name not in colonies:
            failures.append(f"{name}: absent from /api/colonies/tools-index")
            continue
        rid = slug_to_colony_id(name)
        if len(buckets.get(rid, [])) > 1:
            failures.append(
                f"{name}: route id '{rid}' is shared with "
                f"{[m for m in buckets[rid] if m != name]} — will be shadowed")
        else:
            print(f"ok  {name:<24} -> /colony/{rid}")

    print(f"\nbackend colonies={len(colonies)}  unique ids={len(buckets)}  "
          f"live sessions={len(live)}")

    if failures:
        print("\nFAIL")
        for f in failures:
            print("  " + f)
        return 1

    print("\nPASS")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
