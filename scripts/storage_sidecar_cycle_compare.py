#!/usr/bin/env python3
"""Field-level comparator for the SC-06 sidecar write-read-rewrite cycle.

The cycle (driven by go_hive/scripts/storage_sc04_06_run.sh) runs on one home:

  1. Python writes session meta / tasks / colony sidecars through the real
     Python writers                     -> py-write.json
  2. Go reads the same home through the real Go readers -> go-read.json
  3. Go rewrites the same artifacts through the real Go writers -> go-write.json
  4. Python reads the rewritten home   -> py-read.json

This script compares the *views* (what each implementation's APIs return) and
the file-hash transitions:

  * go-read view must equal py-write view   (Go reads Python's writes)
  * py-read view must equal go-write view   (Python reads Go's rewrites)
  * the rewrite must actually change the four mutable files and leave
    metadata.json / worker.json byte-identical
  * the Api checks below assert the controlled values moved through the cycle

Stdlib only. Exits 1 on any mismatch. Output is designed to land in
docs/e2e-assets/storage-sc04-06/.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

NUMBER_TOLERANCE = 1e-6

# Controlled values seeded by the Python write and rewritten by the Go write.
EXPECT_PYTHON_WRITE = {
    "meta.title": "Sidecar cycle (python)",
    "meta.phase": "independent",
    "tasks.count": 2,
    "colonies.cycle_colony.tools_enabled": ["pdf_read"],
    "colonies.cycle_colony.trigger_ids": ["trg_py"],
    "colonies.cycle_colony_go.present": False,
}

EXPECT_GO_WRITE = {
    "meta.title": "Sidecar cycle (go rewritten)",
    "meta.phase": "colony",
    "tasks.count": 3,
    "tasks.status.2": "completed",
    "colonies.cycle_colony.tools_enabled": ["pdf_read", "excel_read"],
    "colonies.cycle_colony.trigger_ids": ["trg_py", "trg_go"],
    "colonies.cycle_colony_go.present": True,
    "colonies.cycle_colony_go.tools_enabled": ["pdf_read", "excel_read"],
}

MUST_CHANGE_ON_GO_WRITE = [
    "session/meta.json",
    "session/tasks.json",
    "colonies/cycle_colony/tools.json",
    "colonies/cycle_colony/triggers.json",
]
MUST_NOT_CHANGE_ON_GO_WRITE = [
    "colonies/cycle_colony/metadata.json",
    "colonies/cycle_colony/worker.json",
]
CREATED_BY_GO_WRITE = [
    "colonies/cycle_colony_go/metadata.json",
    "colonies/cycle_colony_go/worker.json",
    "colonies/cycle_colony_go/tools.json",
    "colonies/cycle_colony_go/triggers.json",
]


# ---------------------------------------------------------------------------
# deep compare
# ---------------------------------------------------------------------------


def diff_values(py, go, path, out):
    """Collect field-level differences between two parsed JSON views.

    Numbers compare numerically (the two encoders render the same float
    differently: 1767225600.0 vs 1767225600, both parse to the same value).
    """
    if isinstance(py, dict) and isinstance(go, dict):
        for key in sorted(set(py) | set(go)):
            child = f"{path}.{key}" if path else key
            if key not in py:
                out.append({"path": child, "kind": "only_go", "go": go[key]})
            elif key not in go:
                out.append({"path": child, "kind": "only_py", "py": py[key]})
            else:
                diff_values(py[key], go[key], child, out)
        return
    if isinstance(py, list) and isinstance(go, list):
        if len(py) != len(go):
            out.append({"path": path, "kind": "length_mismatch", "py": len(py), "go": len(go)})
            return
        for index, (left, right) in enumerate(zip(py, go)):
            diff_values(left, right, f"{path}[{index}]", out)
        return
    if isinstance(py, bool) or isinstance(go, bool):
        if py != go:
            out.append({"path": path, "kind": "value_mismatch", "py": py, "go": go})
        return
    if isinstance(py, (int, float)) and isinstance(go, (int, float)):
        if abs(float(py) - float(go)) > NUMBER_TOLERANCE:
            out.append({"path": path, "kind": "value_mismatch", "py": py, "go": go})
        return
    if py != go:
        out.append({"path": path, "kind": "value_mismatch", "py": py, "go": go})


def view_diff(py_view, go_view):
    out: list[dict] = []
    diff_values(py_view, go_view, "", out)
    return out


# ---------------------------------------------------------------------------
# API-level checks
# ---------------------------------------------------------------------------


def _get(view, dotted):
    current = view
    for part in dotted.split("."):
        if isinstance(current, dict) and part in current:
            current = current[part]
        else:
            return None
    return current


def _colony_present(view, colony):
    entry = (view.get("colonies") or {}).get(colony) or {}
    return any(entry.get(key) is not None for key in ("metadata", "worker", "tools"))


def _task_status(view, task_id):
    for record in (view.get("tasks") or {}).get("tasks") or []:
        if record.get("id") == task_id:
            return record.get("status")
    return None


def _trigger_ids(view, colony):
    entry = (view.get("colonies") or {}).get(colony) or {}
    return [item.get("id") for item in entry.get("triggers") or []]


def api_checks(view, expected, label):
    failures = []
    for key, want in expected.items():
        if key == "tasks.count":
            got = len(((view.get("tasks") or {}).get("tasks")) or [])
        elif key.startswith("tasks.status."):
            got = _task_status(view, int(key.rsplit(".", 1)[1]))
        elif key.endswith(".trigger_ids"):
            got = _trigger_ids(view, key.split(".")[1])
        elif key.endswith(".present"):
            got = _colony_present(view, key.split(".")[1])
        else:
            got = _get(view, key)
        if got != want:
            failures.append({"check": f"{label}:{key}", "want": want, "got": got})
    return failures


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--py-write", required=True)
    parser.add_argument("--go-read", required=True)
    parser.add_argument("--go-write", required=True)
    parser.add_argument("--py-read", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--label", default="sc06")
    args = parser.parse_args()

    steps = {
        "python_write": load(Path(args.py_write)),
        "go_read": load(Path(args.go_read)),
        "go_write": load(Path(args.go_write)),
        "python_read": load(Path(args.py_read)),
    }

    report: dict = {"label": args.label, "steps": sorted(steps)}

    # 1. Go reads what Python wrote; Python reads what Go rewrote.
    go_read_of_python = view_diff(steps["python_write"]["view"], steps["go_read"]["view"])
    python_read_of_go = view_diff(steps["go_write"]["view"], steps["python_read"]["view"])
    report["view_diffs"] = {
        "go_read_vs_python_write": {"equal": not go_read_of_python, "differences": go_read_of_python},
        "python_read_vs_go_write": {"equal": not python_read_of_go, "differences": python_read_of_go},
    }

    # 2. Controlled API values at each stage.
    checks = []
    checks += api_checks(steps["python_write"]["view"], EXPECT_PYTHON_WRITE, "python_write")
    checks += api_checks(steps["go_read"]["view"], EXPECT_PYTHON_WRITE, "go_read")
    checks += api_checks(steps["go_write"]["view"], EXPECT_GO_WRITE, "go_write")
    checks += api_checks(steps["python_read"]["view"], EXPECT_GO_WRITE, "python_read")
    checks_run = 2 * len(EXPECT_PYTHON_WRITE) + 2 * len(EXPECT_GO_WRITE)
    report["api_checks"] = {"checks_run": checks_run, "failures": checks, "passed": len(checks) == 0}

    # 3. File-hash transitions across the rewrite.
    py_files = steps["python_write"]["files"]
    go_files = steps["go_write"]["files"]
    changed, unchanged, created, missing = [], [], [], []
    for key in sorted(set(py_files) | set(go_files)):
        before, after = py_files.get(key), go_files.get(key)
        if before is None and after is not None:
            created.append(key)
        elif before is not None and after is None:
            missing.append(key)
        elif before == after:
            unchanged.append(key)
        else:
            changed.append(key)
    transitions = {
        "changed": changed,
        "unchanged": unchanged,
        "created_by_go_write": created,
        "missing_after_go_write": missing,
    }
    transition_problems = []
    for key in MUST_CHANGE_ON_GO_WRITE:
        if key not in changed:
            transition_problems.append(f"{key} did not change across the go rewrite")
    for key in MUST_NOT_CHANGE_ON_GO_WRITE:
        if key not in unchanged:
            transition_problems.append(f"{key} changed across the go rewrite")
    for key in CREATED_BY_GO_WRITE:
        if key not in created:
            transition_problems.append(f"{key} was not created by the go rewrite")
    if missing:
        transition_problems.append(f"files missing after the go rewrite: {missing}")
    report["file_transitions"] = transitions
    report["file_transition_problems"] = transition_problems

    # 4. Declared writer-stamped differences: the two implementations render the
    #    same field with different text (values parse the same on the other
    #    side). Recorded, not treated as failures.
    def writer_stamps(step):
        tools = ((step["view"].get("colonies") or {}).get("cycle_colony") or {}).get("tools") or {}
        meta = step["view"].get("meta") or {}
        return {
            "tools.updated_at": tools.get("updated_at"),
            "tools.saved_on_version": tools.get("saved_on_version"),
            "meta.fields.created_at": (meta.get("fields") or {}).get("created_at"),
        }

    report["declared_writer_differences"] = {
        "note": (
            "Writer-stamped fields render differently per implementation "
            "(Python isoformat microseconds + '+00:00'; Go RFC3339Nano 'Z'); both "
            "sides parse each other's text. Values below are the observed stamps."
        ),
        "python_write": writer_stamps(steps["python_write"]),
        "go_write": writer_stamps(steps["go_write"]),
    }

    verdict_failures = []
    if go_read_of_python:
        verdict_failures.append(f"go_read != python_write ({len(go_read_of_python)} field diffs)")
    if python_read_of_go:
        verdict_failures.append(f"python_read != go_write ({len(python_read_of_go)} field diffs)")
    if checks:
        verdict_failures.append(f"{len(checks)} API checks failed")
    verdict_failures.extend(transition_problems)
    report["verdict"] = "PASS" if not verdict_failures else "FAIL"
    report["failures"] = verdict_failures

    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(report, indent=2, sort_keys=True), encoding="utf-8")

    print(json.dumps({
        "verdict": report["verdict"],
        "go_read_vs_python_write_diffs": len(go_read_of_python),
        "python_read_vs_go_write_diffs": len(python_read_of_go),
        "api_checks_passed": not checks,
        "changed": changed,
        "created": created,
        "failures": verdict_failures,
    }, indent=2))
    return 0 if report["verdict"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
