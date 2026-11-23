#!/usr/bin/env python3
"""Rebase the 12 queen-lifecycle tool references onto the Python runtime layer.

The round-1 freeze captured the lifecycle names from the Tool Library layer,
where each row renders as name + description and the schema is an empty
object — not what the model receives. The model receives the registered
``Tool`` objects projected through the request layer
(``framework/llm/litellm.py::LiteLLMProvider._tool_to_openai_format``), which
materializes ``type`` / ``properties`` / ``required``.

This script is the official re-freeze step for that rebase:

  1. reads the runtime-layer capture
     ``go_hive/docs/e2e-assets/schema-residuals/python-runtime-tools.json``
     (produced by the probe next to it — no LLM);
  2. rewrites the 12 lifecycle entries of
     ``go_hive/contracts/tool-definitions.json`` (input_schema, source,
     layer_detail, proto block) and recomputes ``statistics``;
  3. re-emits ``go_hive/contracts/tool-types.proto`` from the same manifest;
  4. re-runs ``go test ./internal/cli -run TestToolContractFreeze -v`` and
     writes the section-C live-recheck numbers into ``statistics.live_recheck``
     and ``go_hive/docs/tool-contract-recheck.json`` (round 9);
  5. prints the residual lists so the docs can quote real output.

Run (repo venv; goenv sourced for `go`):

    source /Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh
    /Users/mac/hive/.venv/bin/python go_hive/scripts/refresh_lifecycle_runtime_reference.py

The Go snapshot (``go`` blocks) stays the round-1 capture: this only changes
the reference side, exactly like rounds 6–8's surgical refreshes.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
import re
import subprocess
import sys
from datetime import datetime, timezone

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
GO_HIVE = REPO_ROOT / "go_hive"
CONTRACTS = GO_HIVE / "contracts"
MANIFEST_PATH = CONTRACTS / "tool-definitions.json"
META_PATH = CONTRACTS / "tool-capture-meta.json"
PROTO_PATH = CONTRACTS / "tool-types.proto"
RECHECK_PATH = GO_HIVE / "docs" / "tool-contract-recheck.json"
PROBE_PATH = GO_HIVE / "docs" / "e2e-assets" / "schema-residuals" / "python-runtime-tools.json"
GO_MODULE = GO_HIVE / "go-hive"

LIFECYCLE_TOOLS = [
    "get_playbook_status",
    "get_worker_status",
    "inject_message",
    "list_playbook_runs",
    "remove_trigger",
    "reply_to_worker",
    "run_playbook",
    "run_worker",
    "set_trigger",
    "stop_playbook",
    "stop_worker",
    "update_worker_profile",
]

REQUEST_PROJECTION = "framework/llm/litellm.py::LiteLLMProvider._tool_to_openai_format"
PROBE_REL = "go_hive/docs/e2e-assets/schema-residuals/py_runtime_schema_probe.py"


def _load_freeze_module():
    spec = importlib.util.spec_from_file_location(
        "freeze_tool_contract", GO_HIVE / "scripts" / "freeze_tool_contract.py"
    )
    if spec is None or spec.loader is None:
        raise SystemExit("cannot load go_hive/scripts/freeze_tool_contract.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _sha256_json(value) -> str:
    import hashlib

    canonical = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def _run_freeze_test() -> dict:
    env_command = ["go", "test", "./internal/cli", "-run", "TestToolContractFreeze", "-count=1", "-v"]
    result = subprocess.run(env_command, cwd=GO_MODULE, capture_output=True, text=True)
    if result.returncode != 0:
        sys.stderr.write(result.stdout[-4000:])
        sys.stderr.write(result.stderr[-4000:])
        raise SystemExit(f"TestToolContractFreeze failed (exit {result.returncode})")
    output = result.stdout
    unified = re.search(
        r"C\. unified metric \(authoritative\): session reference (\d+)/(\d+) "
        r"\((\d+) reference entries outside the session surface\), "
        r"description (\d+)/(\d+), schema raw (\d+)/(\d+), schema normalized (\d+)/(\d+)",
        output,
    )
    if not unified:
        raise SystemExit("could not parse the C-section unified metric from the test output")
    normalized = re.search(
        r"C\. schema differs after normalization \(\d+\): \[([^\]]*)\]", output
    )
    raw = re.search(r"A\. input_schema differs \(\d+\): \[([^\]]*)\]", output)
    missing = re.search(r"A\. missing in Go \(\d+\): \[([^\]]*)\]", output)

    def names(match) -> list[str]:
        if not match or not match.group(1).strip():
            return []
        # logList prints Go's %v form: [a b c] — whitespace separated.
        return match.group(1).split()

    return {
        "reference_tools": int(unified.group(2)),
        "live_present": int(unified.group(1)),
        "outside_session": int(unified.group(3)),
        "description_verbatim": int(unified.group(4)),
        "input_schema_raw_verbatim": int(unified.group(6)),
        "input_schema_normalized_verbatim": int(unified.group(8)),
        "live_missing": names(missing),
        "normalized_residual": names(normalized),
        "raw_residual": names(raw),
        "exit_code": result.returncode,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--probe", type=pathlib.Path, default=PROBE_PATH)
    parser.add_argument("--skip-live", action="store_true",
                        help="do not run the Go test (leaves the previous live_recheck block)")
    args = parser.parse_args()

    freeze = _load_freeze_module()
    probe = json.loads(args.probe.read_text(encoding="utf-8"))
    manifest = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
    meta = json.loads(META_PATH.read_text(encoding="utf-8"))

    by_name = {tool["name"]: tool for tool in manifest["tools"]}
    captured = probe["lifecycle"]
    problems: list[str] = []
    for name in LIFECYCLE_TOOLS:
        entry = by_name.get(name)
        if entry is None:
            problems.append(f"{name}: missing from the freeze manifest")
            continue
        if captured[name]["description"] != entry["description"]:
            problems.append(
                f"{name}: probe description differs from the frozen reference "
                f"({len(captured[name]['description'].encode())} vs {len(entry['description'].encode())} bytes)"
            )
        if entry.get("layers") != ["lifecycle"]:
            problems.append(f"{name}: unexpected layers {entry.get('layers')!r}")
    if problems:
        for problem in problems:
            print("FAIL:", problem)
        return 1

    for name in LIFECYCLE_TOOLS:
        entry = by_name[name]
        captured_entry = captured[name]
        entry["input_schema"] = captured_entry["parameters"]
        entry["source"] = "python:lifecycle_runtime"
        entry["layer_detail"] = {
            "lifecycle": {
                "surface": "runtime_registered_tool",
                "request_projection": REQUEST_PROJECTION,
                "phase_visible": captured_entry["phase_visible"],
                "probe": PROBE_REL,
                "parameters_sha256": captured_entry["sha256_parameters"],
            }
        }
        entry["proto"] = freeze._manifest_proto(
            {"name": name, "input_schema": entry["input_schema"]}
        )
        entry["reference_round"] = 9

    sources = manifest["contract"].setdefault("sources", [])
    if "python:lifecycle_runtime" not in sources:
        sources.append("python:lifecycle_runtime")
    if "python:lifecycle_runtime =" not in manifest["contract"].get("layers_note", ""):
        manifest["contract"]["layers_note"] = (
            manifest["contract"].get("layers_note", "")
            + " | lifecycle_runtime = the registered Python Tool objects projected through the "
            "request layer (_tool_to_openai_format materializes type/properties/required); "
            "round 9 rebased the 12 lifecycle references from the schema-less Tool Library "
            "renderings onto this runtime layer."
        )

    # Recompute the manifest-level statistics from the rebased reference; the
    # capture-surface counters (python_llm / mcp_catalog / lifecycle / synthetic)
    # are not derivable from the manifest, so keep them.
    manifest["statistics"] = {**manifest["statistics"], **freeze.stats(manifest["tools"], manifest["go_only_tools"])}
    manifest["statistics"]["live_recheck"] = {
        "computed_by": "go test ./internal/cli -run TestToolContractFreeze (section C)",
        "surface": (
            "runtime layer: registered Tool objects projected through the request layer; "
            "task_create is compared with the DM session pivot overlay installed "
            "(session_tools_task_pivot.go), matching the reference capture"
        ),
        "normalization": (
            "strip CONTEXT_PARAMS + pydantic title; empty required == absent; "
            "numeric equivalence (25.0 == 25)"
        ),
    }
    manifest["recheck"] = {
        **manifest.get("recheck", {}),
        "current_round": 9,
        "report": "go_hive/docs/tool-contract-diff.md §3.1 / §18",
        "note": (
            "Round 9 (schema-residuals) rebased the 12 queen-lifecycle references from the "
            "Tool Library layer (name+description, empty schema) onto the Python runtime "
            "layer — the registered Tool objects projected through _tool_to_openai_format — "
            "and fixed the Go side to match byte for byte (browser_setup additionalProperties, "
            "run_worker parameter text, playbook required materialization, task_create pivot "
            "overlay in the recheck). The manifest keeps the round-1 Go snapshot as provenance; "
            "statistics.live_recheck is the machine-readable copy of the current section-C metric."
        ),
    }
    manifest["generated_at_utc"] = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

    # Provisional write so the Go test reads the rebased reference.
    MANIFEST_PATH.write_text(json.dumps(manifest, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    proto_text = freeze.emit_proto(manifest["tools"], manifest["go_only_tools"], meta)
    PROTO_PATH.write_text(proto_text, encoding="utf-8")

    live = {"skipped": True}
    if not args.skip_live:
        live = _run_freeze_test()
        manifest["statistics"]["live_recheck"].update(
            {
                "reference_tools": live["reference_tools"],
                "live_present": live["live_present"],
                "outside_session": live["outside_session"],
                "live_missing": live["live_missing"],
                "description_verbatim": live["description_verbatim"],
                "input_schema_raw_verbatim": live["input_schema_raw_verbatim"],
                "input_schema_normalized_verbatim": live["input_schema_normalized_verbatim"],
                "normalized_residual": live["normalized_residual"],
                "normalized_residual_reasons": {},
                "raw_residual": live["raw_residual"],
                "raw_residual_reasons": {
                    "numeric_spelling": [
                        name for name in live["raw_residual"]
                        if name in {"attach_file", "pdf_read", "terminal_job_logs", "terminal_pty_run"}
                    ],
                    "context_param_reference_layer": [
                        name for name in live["raw_residual"]
                        if name in {"edit_file", "read_file", "terminal_job_start", "terminal_pty_open"}
                    ],
                    "note": (
                        "Raw differs only in JSON-schema representation: numeric literal "
                        "spelling (25 vs 25.0, decoded by Go's float64 marshaling) and the "
                        "framework session_cwd context parameter present in the Python Tool "
                        "Library reference layer. The normalized metric treats both as equal "
                        "(§14.1)."
                    ),
                },
                "gate_exit_code": live["exit_code"],
            }
        )
        MANIFEST_PATH.write_text(json.dumps(manifest, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")

        recheck = json.loads(RECHECK_PATH.read_text(encoding="utf-8"))
        recheck["current_round"] = 9
        # Idempotent: a re-run replaces the round-9 entry instead of duplicating it.
        recheck["rounds"] = [r for r in recheck["rounds"] if r.get("round") != 9]
        recheck["rounds"].append(
            {
                "round": 9,
                "label": (
                    "round 9 @ schema-residuals（lifecycle 运行时层重取基线；browser_setup / "
                    "task_create / run_worker / playbook required 收口）"
                ),
                "generated_at_utc": manifest["generated_at_utc"],
                "scope": (
                    "Rebase the 12 queen-lifecycle references onto the Python runtime layer "
                    "(registered Tool objects projected through _tool_to_openai_format) and "
                    "align the Go side byte for byte: browser_setup drops "
                    "additionalProperties:false; run_worker parameter descriptions adopt the "
                    "Python text; run_playbook / list_playbook_runs publish required: []; the "
                    "freeze report installs the DM task_create pivot overlay so the comparison "
                    "is the runtime surface the model receives."
                ),
                "baseline": {
                    "round": 8,
                    "note": (
                        "Round 8 (tool-surface-fixes) froze the live recheck as the single "
                        "authoritative metric: present 47/54, description 47/47, raw 25/47, "
                        "normalized 33/47, residual 14 (12 lifecycle layer + browser_setup + "
                        "task_create)."
                    ),
                },
                "recheck": {
                    "method": (
                        "live registry union, no dual-runtime capture; Python reference rebased "
                        "onto the runtime layer by "
                        "go_hive/docs/e2e-assets/schema-residuals/py_runtime_schema_probe.py"
                    ),
                    "command": "go test ./internal/cli -run TestToolContractFreeze -v",
                    "python": {
                        "role": "reference",
                        "version": "1.0.14",
                        "commit": "4e737cbc63523ba5eeda84820c09f28ebd43a670",
                        "core_commit": "a756b1e51765bc75b0a40e362859452a0a54081b",
                        "tools_commit": "68b6c23095aef8bc9a0dcbc05aeb16e61b1ba8db",
                        "runtime_layer": (
                            "registered Tool objects (queen_lifecycle_tools.py + "
                            "playbook_tools.py) projected via _tool_to_openai_format; "
                            "probe output go_hive/docs/e2e-assets/schema-residuals/"
                            "python-runtime-tools.json"
                        ),
                    },
                    "go": {
                        "role": "target",
                        "worktree": str(REPO_ROOT),
                        "branch": "schema-residuals",
                        "base": "f9eb5720",
                        "method": (
                            "newSessionToolCatalogue registry union + "
                            "server.QueenLifecycleToolDefs, with the DM task_create pivot "
                            "overlay installed"
                        ),
                    },
                    "statistics": {
                        key: live[key]
                        for key in (
                            "reference_tools",
                            "live_present",
                            "outside_session",
                            "description_verbatim",
                            "input_schema_raw_verbatim",
                            "input_schema_normalized_verbatim",
                            "normalized_residual",
                            "raw_residual",
                        )
                    }
                    | {
                        "live_missing": live["live_missing"],
                        "normalized_residual_reasons": {},
                    },
                },
                "regressions": [],
                "evidence": [
                    "docs/tool-contract-diff.md §18",
                    "docs/e2e-assets/schema-residuals/REPORT.md",
                    "docs/e2e-assets/schema-residuals/python-runtime-tools.json",
                    "go test ./internal/cli -run TestToolContractFreeze -v (section C line)",
                ],
            }
        )
        RECHECK_PATH.write_text(
            json.dumps(recheck, ensure_ascii=False, indent=1) + "\n", encoding="utf-8"
        )

    print(f"rebased {len(LIFECYCLE_TOOLS)} lifecycle references onto the runtime layer")
    print(f"wrote {MANIFEST_PATH}")
    print(f"wrote {PROTO_PATH}")
    if not args.skip_live:
        print(f"wrote {RECHECK_PATH}")
        print(
            "live recheck: present {live_present}/{reference_tools}, description "
            "{description_verbatim}, raw {input_schema_raw_verbatim}, normalized "
            "{input_schema_normalized_verbatim}".format(**live)
        )
        print("normalized residual:", live["normalized_residual"] or "[]")
        print("raw residual:", live["raw_residual"] or "[]")
    return 0


if __name__ == "__main__":
    sys.exit(main())
