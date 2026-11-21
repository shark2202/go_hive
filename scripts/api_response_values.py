#!/usr/bin/env python3
"""Freeze the REST value-level golden from the canonical API responses.

``freeze_api_responses.py`` captures one canonical response per endpoint on
both implementations (Python reference and Go target) and compares their
*shape*. This script adds the value layer on top of the same committed
payloads:

  go_hive/contracts/api-responses/value-golden.json

For every endpoint (the 14 success paths plus the 400/404/409 failure paths)
it flattens the canonical payload into scalar leaf paths, compares the two
sides, and classifies every remaining difference:

  * strict match          — no entry in the ledger, verified equal
  * value-level exception — declared, with the observed values and a reason
  * runtime-internal      — ignored with a reason (process gauges)
  * undeclared            — fails the check, must be fixed or declared

Arrays whose elements carry a stable identity key (credential_id, skill_id,
tool_name, id, name) are compared order-insensitively by that key, so a map
iteration order change is not a contract change; scalar arrays are compared
as multisets. Everything else is compared positionally.

Usage::

    uv run python go_hive/scripts/freeze_api_responses.py     # refresh captures
    uv run python go_hive/scripts/api_response_values.py     # write ledger
    uv run python go_hive/scripts/api_response_values.py --check

``--check`` never starts a server: it re-derives the ledger from the committed
canonical payloads and fails on drift or an undeclared difference.

Standard library only.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from datetime import datetime
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
RESPONSES_DIR = REPO_ROOT / "go_hive" / "contracts" / "api-responses"
GOLDEN_PATH = RESPONSES_DIR / "value-golden.json"
SCHEMA = "hive-api-values/1"

IDENTITY_KEYS = ("credential_id", "skill_id", "tool_name", "id", "name")

# Paths compared but allowed to differ, with the observed values recorded.
VALUE_EXCEPTIONS: list[dict[str, str]] = [
    {
        "id": "tool-description-text",
        "endpoint": "queen_tools",
        "path_regex": r"(^|\.)description$",
        "class": "value-level",
        "reason": (
            "Tool description text is still being aligned with the reference "
            "corpus (tool-description / prompt parity lane). Tool names, counts, "
            "parameters and lifecycle structure are compared strictly."
        ),
    },
    {
        "id": "sse-frame-model",
        "endpoint": "session_events",
        "path_regex": r"^frames\[\d+\]\.envelope\.data\.model$",
        "class": "value-level",
        "reason": "Same litellm provider-prefix difference as the SSE frame golden.",
    },
    {
        "id": "sse-frame-system-prefix-sha",
        "endpoint": "session_events",
        "path_regex": r"^frames\[\d+\]\.envelope\.data\.system_prefix_sha$",
        "class": "value-level",
        "reason": "System prompt text still differs (prompt-parity lane).",
    },
    {
        "id": "history-model",
        "endpoint": "session_events_history",
        "path_regex": r"^events\[\d+\]\.data\.model$",
        "class": "value-level",
        "reason": "Same litellm provider-prefix difference as the SSE frame golden.",
    },
    {
        "id": "history-system-prefix-sha",
        "endpoint": "session_events_history",
        "path_regex": r"^events\[\d+\]\.data\.system_prefix_sha$",
        "class": "value-level",
        "reason": "System prompt text still differs (prompt-parity lane).",
    },
    {
        "id": "history-loop-max-iterations",
        "endpoint": "session_events_history",
        "path_regex": r"^events\[\d+\]\.data\.max_iterations$",
        "class": "intentional-structural",
        "reason": (
            "Python's permanent queen loop reports 999999; Go's per-turn loop "
            "reports its configured cap (50). Same field, same type."
        ),
    },
    {
        "id": "history-context-usage",
        "endpoint": "session_events_history",
        "path_regex": (
            r"^events\[\d+\]\.data\.(usage_ratio|usage_pct|estimated_tokens|message_count|breakdown\..*)$"
        ),
        "class": "value-level",
        "reason": (
            "Context readout numbers derive from the system prompt/tool-definition "
            "char counts, which still differ (prompt-parity lane); emission points "
            "and trigger values are pinned by the SSE frame golden."
        ),
    },
    {
        "id": "event-bus-subscriptions",
        "endpoint": "session_stats",
        "path_regex": r"^event_bus\.subscriptions$",
        "class": "runtime-internal",
        "reason": (
            "Number of live EventBus subscribers: scheduling/internal, not part of "
            "the HTTP contract (the existing semantic check already excludes it)."
        ),
    },
    {
        "id": "config-models-pricing-float",
        "endpoint": "config_models",
        "path_regex": r"^models\.[^.]+\[[^\]]+\]\.pricing_usd_per_mtok\.",
        "class": "non-verbatim-numeric",
        "reason": (
            "Integer-valued prices render as 4.0 (Python) vs 4 (Go): the same JSON "
            "number, the documented int/float non-verbatim case (NV-02)."
        ),
    },
    {
        "id": "browser-contexts-empty-state",
        "endpoint": "browser_contexts",
        "path_regex": r"^(available|count|reason)$",
        "class": "empty-state-divergence",
        "reason": (
            "With no bridge/extension: Python's in-process gcu registry answers "
            "available:true + count:0; Go proxies the bridge and answers available:false "
            "+ reason. Both keys are pinned by the endpoint declaration."
        ),
    },
    {
        "id": "browser-tabs-reason-text",
        "endpoint": "browser_tabs",
        "path_regex": r"^reason$",
        "class": "env-dependent-value",
        "reason": (
            "Both sides report available:false with an unreachable bridge; the reason text is "
            "implementation-specific (Python 'bridge not connected'/'bridge connect failed: …', "
            "Go 'browser bridge not reachable at <addr>'). Shape is identical."
        ),
    },
    {
        "id": "config-llm-put-swapped-count",
        "endpoint": "config_llm_put",
        "path_regex": r"^sessions_swapped$",
        "class": "runtime-state-gap",
        "reason": (
            "The hot-swap count walks session.llm plus the session's eager ColonyRuntime LLM; "
            "Python attaches a colony runtime to the queen session (2), the Go REST session has "
            "none (1). Same root cause as the colony-tools gap."
        ),
    },
    {
        "id": "live-tools-mcp-universe",
        "endpoint": "session_live_tools",
        "path_regex": r"^(mcp_tool_count_registered|diagnostic\.registry_mcp_total|diagnostic\.phase_state\.(mcp_tool_names_all_total|independent_tools_total))$",
        "class": "value-level",
        "reason": (
            "Go's harness catalog records 33 MCP-origin names against the reference's 28: the Go "
            "hive_tools group implements the excel_* tools and keeps a browser group the Python "
            "probe's installed extras do not register, while Python lists csv_sql which this build "
            "leaves unimplemented. The universe size, the registry MCP total and the independent "
            "total derived from them differ by exactly that surplus (33/40 vs 28/35); the gate "
            "snapshot (817 credentialed tools, 80 providers) and every allowlist-filtered count the "
            "model surface gates on match exactly."
        ),
    },
]

# Endpoints whose difference is a declared, documented gap: every differing
# path is collapsed into a single ledger entry (count + sample) instead of
# failing the check or bloating the ledger with hundreds of rows.
#
# Empty since session_live_tools moved to per-value comparison: both its tool
# surface and its registry-side admission-gate diagnostic now match the
# reference, and the remaining MCP-universe counts are declared in
# VALUE_EXCEPTIONS above.
ENDPOINT_EXCEPTIONS: list[dict[str, Any]] = []

# Paths not compared at all, with the reason recorded in the ledger.
VALUE_IGNORED: list[dict[str, str]] = [
    {
        "id": "health-resources",
        "endpoint": "health",
        "path_regex": r"^resources\.",
        "class": "runtime-internal",
        "reason": "Per-process resource gauges (memory/renderer sampling), not API contract fields.",
    },
]


# ---------------------------------------------------------------------------
# canonical flattening
# ---------------------------------------------------------------------------


class GoldenError(RuntimeError):
    pass


def now_iso() -> str:
    return datetime.now().astimezone().isoformat(timespec="seconds")


def identity_key(items: list[Any]) -> str | None:
    if not items or not all(isinstance(item, dict) for item in items):
        return None
    for key in IDENTITY_KEYS:
        values = [item.get(key) for item in items]
        if all(isinstance(value, str) and value for value in values) and len(set(values)) == len(values):
            return key
    return None


def flatten(value: Any, path: str, out: list[tuple[str, str]]) -> None:
    """Collect (leaf path, JSON-encoded leaf value) pairs, deterministically."""
    if isinstance(value, dict):
        for key in sorted(value):
            flatten(value[key], f"{path}.{key}" if path else key, out)
        return
    if isinstance(value, list):
        key = identity_key(value)
        if key is not None:
            for item in sorted(value, key=lambda entry: entry[key]):
                flatten(item, f"{path}[{item[key]}]", out)
            return
        if all(not isinstance(item, (dict, list)) for item in value):
            for item in sorted(value, key=lambda entry: json.dumps(entry, ensure_ascii=False, sort_keys=True)):
                out.append((f"{path}[]", json.dumps(item, ensure_ascii=False)))
            return
        for index, item in enumerate(value):
            flatten(item, f"{path}[{index}]", out)
        return
    out.append((path, json.dumps(value, ensure_ascii=False)))


def load_json(path: Path) -> Any:
    if not path.is_file():
        raise GoldenError(f"missing input: {path} (run freeze_api_responses.py first)")
    return json.loads(path.read_text(encoding="utf-8"))


def load_payload(side: str, endpoint: str) -> dict[str, Any]:
    record = load_json(RESPONSES_DIR / side / f"{endpoint}.json")
    if not isinstance(record, dict) or "canonical" not in record:
        raise GoldenError(f"{side}/{endpoint}.json has no canonical payload")
    return record


def describe(value: str) -> Any:
    """Keep the ledger readable: digest long strings instead of copying them."""
    if len(value) <= 200:
        return value
    digest = hashlib.sha256(value.encode("utf-8")).hexdigest()[:16]
    return {
        "sha256": digest,
        "chars": len(value),
        "preview": value[:160],
    }


def matching_exception(endpoint: str, path: str) -> dict[str, str] | None:
    for exception in VALUE_EXCEPTIONS:
        if exception["endpoint"] != endpoint:
            continue
        if re.search(exception["path_regex"], path):
            return exception
    return None


def ignored(endpoint: str, path: str) -> dict[str, str] | None:
    for entry in VALUE_IGNORED:
        if entry["endpoint"] != endpoint:
            continue
        if re.search(entry["path_regex"], path):
            return entry
    return None


def endpoint_exception(endpoint: str) -> dict[str, Any] | None:
    for entry in ENDPOINT_EXCEPTIONS:
        if entry["endpoint"] == endpoint:
            return entry
    return None


def compare_endpoint(entry: dict[str, Any]) -> dict[str, Any]:
    endpoint = entry["endpoint"]
    reference = load_payload("reference", endpoint)
    target = load_payload("target", endpoint)
    reference_paths: list[tuple[str, str]] = []
    target_paths: list[tuple[str, str]] = []
    flatten(reference["canonical"], "", reference_paths)
    flatten(target["canonical"], "", target_paths)
    reference_map: dict[str, list[str]] = {}
    target_map: dict[str, list[str]] = {}
    for path, value in reference_paths:
        reference_map.setdefault(path, []).append(value)
    for path, value in target_paths:
        target_map.setdefault(path, []).append(value)

    compared_paths = 0
    ignored_paths = 0
    declared: list[dict[str, Any]] = []
    undeclared: list[dict[str, Any]] = []
    for path in sorted(set(reference_map) | set(target_map)):
        if ignored(endpoint, path):
            ignored_paths += 1
            continue
        compared_paths += 1
        ref_values = sorted(reference_map.get(path, []))
        tgt_values = sorted(target_map.get(path, []))
        if ref_values == tgt_values:
            continue
        exception = matching_exception(endpoint, path)
        row = {
            "path": path,
            "reference": [describe(value) for value in ref_values],
            "target": [describe(value) for value in tgt_values],
        }
        if exception is not None:
            row["class"] = exception["class"]
            row["exception_id"] = exception["id"]
            row["reason"] = exception["reason"]
            declared.append(row)
        else:
            undeclared.append(row)

    status_equal = reference["status"] == target["status"]
    if not status_equal:
        undeclared.append(
            {
                "path": "status",
                "reference": [reference["status"]],
                "target": [target["status"]],
            }
        )
    if entry.get("kind") == "failure":
        expected = entry.get("expect_status")
        if expected is not None and (reference["status"] != expected or target["status"] != expected):
            undeclared.append(
                {
                    "path": "expect_status",
                    "reference": [reference["status"], expected],
                    "target": [target["status"], expected],
                }
            )

    path_digest = hashlib.sha256(
        "\n".join(sorted(reference_map)).encode("utf-8")
    ).hexdigest()[:16]
    collapsed: dict[str, Any] | None = None
    declared_exception = endpoint_exception(endpoint)
    if declared_exception is not None and (declared or undeclared):
        rows = declared + undeclared
        collapsed = {
            "path": "<endpoint>",
            "class": declared_exception["class"],
            "exception_id": "endpoint:" + endpoint,
            "reason": declared_exception["reason"],
            "difference_count": len(rows),
            "sample_paths": [row["path"] for row in rows][: int(declared_exception.get("sample", 12))],
        }
        declared = [collapsed]
        undeclared = []
    return {
        "endpoint": endpoint,
        "label": entry.get("label"),
        "method": entry.get("method"),
        "path": entry.get("path"),
        "kind": entry.get("kind", "success"),
        "expect_status": entry.get("expect_status"),
        "reference_status": reference["status"],
        "target_status": target["status"],
        "status_equal": status_equal,
        "compared_paths": compared_paths,
        "ignored_paths": ignored_paths,
        "path_digest": path_digest,
        "endpoint_exception": declared_exception["class"] if declared_exception else None,
        "endpoint_collapsed": collapsed,
        "declared_differences": declared,
        "undeclared_differences": undeclared,
        "verdict": "PASS" if not undeclared else "FAIL",
    }


def build_ledger() -> dict[str, Any]:
    index = load_json(RESPONSES_DIR / "index.json")
    endpoints = index.get("endpoints")
    if not isinstance(endpoints, list) or not endpoints:
        raise GoldenError("index.json has no endpoints")
    records = [compare_endpoint(entry) for entry in endpoints]
    totals = {
        "endpoints": len(records),
        "pass": sum(1 for record in records if record["verdict"] == "PASS"),
        "fail": sum(1 for record in records if record["verdict"] != "PASS"),
        "success_paths": sum(1 for record in records if record["kind"] == "success"),
        "failure_paths": sum(1 for record in records if record["kind"] == "failure"),
        "paths_compared": sum(record["compared_paths"] for record in records),
        "ignored_paths": sum(record["ignored_paths"] for record in records),
        "declared_differences": sum(len(record["declared_differences"]) for record in records),
        "undeclared_differences": sum(len(record["undeclared_differences"]) for record in records),
        "endpoint_exceptions": sum(1 for record in records if record.get("endpoint_collapsed")),
        "endpoint_exception_differences": sum(
            int(record["endpoint_collapsed"]["difference_count"])
            for record in records
            if record.get("endpoint_collapsed")
        ),
    }
    verdict = "PASS" if totals["undeclared_differences"] == 0 and totals["fail"] == 0 else "FAIL"
    return {
        "schema": SCHEMA,
        "generated_at": now_iso(),
        "reference": index.get("reference"),
        "target": index.get("target"),
        "exceptions": VALUE_EXCEPTIONS,
        "ignored": VALUE_IGNORED,
        "endpoints": records,
        "totals": totals,
        "verdict": verdict,
    }


# ---------------------------------------------------------------------------
# output
# ---------------------------------------------------------------------------


def render(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True)


def strip_volatile(document: dict[str, Any]) -> dict[str, Any]:
    out = dict(document)
    out.pop("generated_at", None)
    return out


def summary(ledger: dict[str, Any]) -> str:
    lines = [
        "REST value golden — canonical responses (reference vs target)",
        f"generated: {ledger['generated_at']}",
        (
            f"endpoints: {ledger['totals']['pass']}/{ledger['totals']['endpoints']} PASS "
            f"({ledger['totals']['success_paths']} success + {ledger['totals']['failure_paths']} failure)"
        ),
        (
            f"paths compared: {ledger['totals']['paths_compared']} "
            f"(ignored {ledger['totals']['ignored_paths']}); "
            f"declared differences {ledger['totals']['declared_differences']}; "
            f"undeclared {ledger['totals']['undeclared_differences']}"
        ),
        (
            f"endpoint-level exceptions: {ledger['totals'].get('endpoint_exceptions', 0)} "
            f"(collapsed paths {ledger['totals'].get('endpoint_exception_differences', 0)})"
        ),
        "",
    ]
    for record in ledger["endpoints"]:
        status = "PASS" if record["verdict"] == "PASS" else "FAIL"
        lines.append(
            f"{status:4s} {record['endpoint']:32s} {record['method']:6s} {record['path']} "
            f"({record['reference_status']}/{record['target_status']}, {record['compared_paths']} paths)"
        )
        for row in record["undeclared_differences"]:
            lines.append(f"     UNDECLARED {row['path']}: {row['reference']} vs {row['target']}")
        for row in record["declared_differences"]:
            lines.append(f"     {row['class']:22s} {row['path']}")
    lines.append("")
    lines.append(f"VERDICT: {ledger['verdict']}")
    return "\n".join(lines) + "\n"


def check(ledger: dict[str, Any]) -> int:
    if not GOLDEN_PATH.is_file():
        print(f"value golden --check FAILED: missing {GOLDEN_PATH}")
        return 1
    committed = json.loads(GOLDEN_PATH.read_text(encoding="utf-8"))
    problems: list[str] = []
    if render(strip_volatile(committed)) != render(strip_volatile(ledger)):
        problems.append(f"ledger drift: {GOLDEN_PATH} (rerun api_response_values.py)")
    if ledger["verdict"] != "PASS":
        problems.append(f"verdict is {ledger['verdict']}")
        for record in ledger["endpoints"]:
            for row in record["undeclared_differences"]:
                problems.append(f"{record['endpoint']} {row['path']}: {row['reference']} vs {row['target']}")
    if problems:
        print("value golden --check FAILED")
        for problem in problems:
            print(f"  - {problem}")
        return 1
    print(
        "value golden --check OK: "
        f"{ledger['totals']['endpoints']} endpoints, {ledger['totals']['paths_compared']} value paths, "
        f"{ledger['totals']['declared_differences']} declared differences, verdict={ledger['verdict']}"
    )
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Freeze the REST value-level golden")
    parser.add_argument("--check", action="store_true", help="verify the committed ledger; never writes")
    args = parser.parse_args(argv)
    try:
        ledger = build_ledger()
    except GoldenError as exc:
        print(f"[api-values] {exc}", file=sys.stderr)
        return 1
    if args.check:
        return check(ledger)
    GOLDEN_PATH.write_text(render(ledger) + "\n", encoding="utf-8")
    print(summary(ledger))
    print(f"[api-values] wrote {GOLDEN_PATH}")
    return 0 if ledger["verdict"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
