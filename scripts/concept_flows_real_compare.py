#!/usr/bin/env python3
"""Compare the Go and Python halves of the concept-flow real-model dual run.

Reads `<out>/go/<scenario>-<variant>/evidence.json` and
`<out>/python/<scenario>-<variant>/evidence.json` and writes:

  <out>/comparison/diff.json    machine-readable field-by-field differences
  <out>/comparison/summary.md   human-readable verdict per scenario

Both halves write the same evidence shape (see concept_flows_real_python.py and
e2e/concept_flows_real_llm_test.go). The comparison is structural: real-model
runs are not byte-identical, so it compares the checks, the worker actions /
report, the event-type sets, session shape and the model-slot request summary,
not model prose.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

SCENARIO_ORDER = [
    "worker_dispatch",
    "worker_llm_probe",
    "escalation_chain",
    "ask_user_park",
    "queen_llm_probe",
    "resume_after_restart",
]

# Checks whose value is an observation rather than a pass/fail gate. Gate
# failures only consider boolean checks; these keys carry counts, ids, status
# codes or tool lists instead.
OBSERVATION_CHECKS = {
    "worker_tools",
    "probe_request_advertised",
    "probe_request_status",
    "worker_state_status",
    "worker_report_status",
    "queen_id",
    "identical_task_list_calls",
}


def load(path: Path) -> dict | None:
    if not path.is_file():
        return None
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except ValueError:
        return None


def scenario_dirs(root: Path) -> dict[str, dict[str, Path]]:
    """Map scenario -> variant -> evidence dir for one side."""
    out: dict[str, dict[str, Path]] = {}
    for directory in sorted(root.glob("*-*")):
        if not (directory / "evidence.json").is_file():
            continue
        name = directory.name
        scenario, _, variant = name.rpartition("-")
        if not scenario:
            continue
        out.setdefault(scenario, {})[variant] = directory
    return out


def normalized_checks(checks: dict | None) -> dict:
    """Boolean gate checks only; observations are reported separately."""
    out = {}
    for key, value in (checks or {}).items():
        if key in OBSERVATION_CHECKS:
            continue
        if isinstance(value, bool):
            out[key] = value
    return out


def observation_checks(checks: dict | None) -> dict:
    out = {}
    for key, value in (checks or {}).items():
        if key in OBSERVATION_CHECKS and value is not None:
            out[key] = value
    return out


def event_type_set(evidence: dict) -> list[str]:
    session = evidence.get("session") or {}
    counts = session.get("event_types") or {}
    return sorted(counts.keys())


def session_shape(evidence: dict) -> dict:
    live = evidence.get("session_live") or {}
    return {
        "queen_phase": live.get("queen_phase"),
        "has_worker": live.get("has_worker"),
        "colony_id": live.get("colony_id"),
    }


def worker_summary(evidence: dict) -> dict:
    workers = evidence.get("workers") or []
    if not workers:
        return {"count": 0}
    first = workers[0]
    actions = list(first.get("actions") or [])
    report = first.get("report") or {}
    return {
        "count": len(workers),
        "actions": actions,
        "reported": bool(report),
        "report_status": report.get("status"),
        "has_get_current_time": "get_current_time" in actions,
        "has_report_to_parent": "report_to_parent" in actions,
    }


def model_summary(evidence: dict) -> dict:
    records = evidence.get("model_requests") or []
    kinds: dict[str, int] = {}
    models = set()
    for record in records:
        kind = str(record.get("kind"))
        kinds[kind] = kinds.get(kind, 0) + 1
        if record.get("model"):
            models.add(str(record["model"]))
    return {"count": len(records), "kinds": kinds, "models": sorted(models)}


def compare_checks(go: dict, python: dict) -> list[dict]:
    go_checks = normalized_checks(go.get("checks"))
    py_checks = normalized_checks(python.get("checks"))
    diffs = []
    for key in sorted(set(go_checks) | set(py_checks)):
        go_value = go_checks.get(key)
        py_value = py_checks.get(key)
        if key == "worker_report_status":
            # Real workers may successfully report either 'success' or
            # 'partial'; compare the presence of a terminal status.
            go_value = bool(go_value)
            py_value = bool(py_value)
        if key == "probe_request_status":
            go_value = (go_value or 0) != 200
            py_value = (py_value or 0) != 200
        if go_value != py_value:
            diffs.append({"field": f"checks.{key}", "go": go_checks.get(key), "python": py_checks.get(key)})
    return diffs


def compare_evidence(go: dict, python: dict) -> dict:
    """Gate differences decide the verdict; observations are recorded too.

    Real-model runs differ in cosmetic event vocabulary and tool-list shape;
    the gate is the checks both halves assert plus the model-slot evidence.
    """
    diffs = compare_checks(go, python)
    observations: list[dict] = []

    go_events = event_type_set(go)
    py_events = event_type_set(python)
    if go_events != py_events:
        observations.append({"field": "event_types", "go": go_events, "python": py_events})

    shape_go = session_shape(go)
    shape_py = session_shape(python)
    if shape_go != shape_py and shape_go != {"queen_phase": None, "has_worker": None, "colony_id": None}:
        diffs.append({"field": "session_shape", "go": shape_go, "python": shape_py})

    workers_go = worker_summary(go)
    workers_py = worker_summary(python)
    if workers_go.get("count") != workers_py.get("count"):
        diffs.append({"field": "workers.count", "go": workers_go.get("count"), "python": workers_py.get("count")})
    if workers_go.get("count") and workers_py.get("count"):
        for key in ("reported", "has_report_to_parent"):
            if workers_go.get(key) != workers_py.get(key):
                diffs.append({"field": f"workers.{key}", "go": workers_go.get(key), "python": workers_py.get(key)})
        if workers_go.get("report_status") != workers_py.get("report_status"):
            observations.append(
                {"field": "workers.report_status", "go": workers_go.get("report_status"), "python": workers_py.get("report_status")}
            )
        if workers_go.get("has_get_current_time") != workers_py.get("has_get_current_time"):
            observations.append(
                {
                    "field": "workers.has_get_current_time",
                    "go": workers_go.get("has_get_current_time"),
                    "python": workers_py.get("has_get_current_time"),
                    "note": "worker tool surface: Python inherits the queen's spawn tools; the Go worker base is the builtin set plus worker.json's declared categories",
                }
            )

    models_go = model_summary(go)
    models_py = model_summary(python)
    if models_go.get("models") != models_py.get("models"):
        observations.append({"field": "model_ids", "go": models_go.get("models"), "python": models_py.get("models")})

    for key, value in observation_checks(go.get("checks")).items():
        other = observation_checks(python.get("checks")).get(key)
        if other != value:
            observations.append({"field": f"checks.{key}", "go": value, "python": other})

    checks_go = normalized_checks(go.get("checks"))
    checks_py = normalized_checks(python.get("checks"))
    gate_failures = [key for key, value in checks_go.items() if value is not True]
    gate_failures += [key for key, value in checks_py.items() if value is not True]
    return {
        "go_checks": checks_go,
        "python_checks": checks_py,
        "differences": diffs,
        "observations": observations,
        "gate_failures": sorted(set(gate_failures)),
        "verdict": "aligned" if not diffs else "differences",
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    root = Path(args.out).resolve()
    go_root = root / "go"
    py_root = root / "python"
    out_dir = root / "comparison"
    out_dir.mkdir(parents=True, exist_ok=True)

    go_scenarios = scenario_dirs(go_root)
    py_scenarios = scenario_dirs(py_root)
    results: dict[str, dict] = {}
    for scenario in SCENARIO_ORDER + sorted((set(go_scenarios) | set(py_scenarios)) - set(SCENARIO_ORDER)):
        variants = sorted(set(go_scenarios.get(scenario, {})) | set(py_scenarios.get(scenario, {})))
        for variant in variants:
            go_dir = go_scenarios.get(scenario, {}).get(variant)
            py_dir = py_scenarios.get(scenario, {}).get(variant)
            go = load(go_dir / "evidence.json") if go_dir else None
            python = load(py_dir / "evidence.json") if py_dir else None
            key = f"{scenario}-{variant}"
            if go is None or python is None:
                results[key] = {
                    "missing": "go" if go is None else "python",
                    "go_dir": str(go_dir) if go_dir else None,
                    "python_dir": str(py_dir) if py_dir else None,
                }
                continue
            results[key] = compare_evidence(go, python)

    (out_dir / "diff.json").write_text(json.dumps(results, ensure_ascii=False, indent=2), encoding="utf-8")

    lines = ["# Concept-flow real-model dual run — comparison", ""]
    lines.append("| scenario | verdict | differences | gate failures |")
    lines.append("|---|---|---|---|")
    for key, result in results.items():
        if "missing" in result:
            lines.append(f"| {key} | missing {result['missing']} side | — | — |")
            continue
        diffs = ", ".join(diff["field"] for diff in result["differences"]) or "—"
        failures = ", ".join(result["gate_failures"]) or "—"
        lines.append(f"| {key} | {result['verdict']} | {diffs} | {failures} |")
    lines.append("")
    for key, result in results.items():
        if "missing" in result:
            continue
        if not result["differences"] and not result["observations"]:
            continue
        lines.append(f"## {key}")
        for diff in result["differences"]:
            lines.append(f"- **gate** `{diff['field']}`: go={diff['go']!r} python={diff['python']!r}")
            if diff.get("note"):
                lines.append(f"  - {diff['note']}")
        for observation in result["observations"]:
            lines.append(f"- observation `{observation['field']}`: go={observation['go']!r} python={observation['python']!r}")
            if observation.get("note"):
                lines.append(f"  - {observation['note']}")
        lines.append("")
    (out_dir / "summary.md").write_text("\n".join(lines), encoding="utf-8")

    failing = [key for key, result in results.items() if "missing" in result or result.get("gate_failures")]
    print(f"compared {len(results)} scenario runs; gate failures/missing: {failing or 'none'}")
    print(f"wrote {out_dir / 'diff.json'} and {out_dir / 'summary.md'}")
    return 1 if failing else 0


if __name__ == "__main__":
    raise SystemExit(main())
