#!/usr/bin/env python3
"""Generate the upstream -> Go behaviour coverage registry.

The registry freezes machine-extracted, upstream-observable behaviours that the
regular shape gates (routes / tool descriptions / schemas / config / env) do
not see:

  1. synthetic tool dispatch branches in the Python agent loop,
  2. the EventType surface in both directions (Python <-> Go <-> frontend),
  3. queen phase-gated tool lists and the task_create pivot fields,
  4. Python test volume per directory against Go tests / ``// Parity:`` refs.

Every row carries an upstream anchor, a Go anchor (or empty), a status, an
owner and notes.  A missing Go side is never silently skipped: it is a row
with status ``missing``.  ``known_gaps`` records gaps found by human review
with a status (open / in_fix / fixed) and a link to the lane / commit / test.

Usage:
    python3 go_hive/scripts/gen_coverage_registry.py           # write JSON + Markdown
    python3 go_hive/scripts/gen_coverage_registry.py --check   # exit 1 on drift

The output is deterministic: sorted rows, no wall-clock timestamps, and all
paths are repository-relative.  ``--check`` recomputes the registry and
compares it (and the Markdown rendering) with the checked-in copies.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from collections import OrderedDict, defaultdict
from pathlib import Path
from typing import Any, Iterable

GENERATOR_VERSION = 1
ROOT = Path(__file__).resolve().parents[2]
GO = ROOT / "go_hive" / "go-hive"
GO_INTERNAL = GO / "internal"
GO_E2E = GO / "e2e"
FRONTEND_SRC = GO / "frontend" / "src"
JSON_PATH = ROOT / "go_hive" / "docs" / "coverage-registry.json"
MD_PATH = ROOT / "go_hive" / "docs" / "coverage-registry.md"

EXCLUDED_DIR_PARTS = {
    "__pycache__",
    ".pytest_cache",
    ".git",
    "node_modules",
    "dist",
    "test-results",
    "testdata",
    "vendor",
}

PY_SYNTHETIC = ROOT / "core" / "framework" / "agent_loop" / "internals" / "synthetic_tools.py"
PY_AGENT_LOOP = ROOT / "core" / "framework" / "agent_loop" / "agent_loop.py"
PY_EVENT_BUS = ROOT / "core" / "framework" / "host" / "event_bus.py"
PY_QUEEN_NODES = ROOT / "core" / "framework" / "agents" / "queen" / "nodes" / "__init__.py"
PY_QUEEN_ORCH = ROOT / "core" / "framework" / "server" / "queen_orchestrator.py"

GO_EVENT_TYPE = GO_INTERNAL / "eventbus" / "event_type.go"
GO_SYNTHETIC = GO_INTERNAL / "agentloop" / "synthetic_tools.go"
GO_QUEEN_PROMPT = GO_INTERNAL / "queen" / "prompt_sections.go"
GO_QUEEN_STATIC = GO_INTERNAL / "queen" / "static_prompt.go"


# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------


def rel(path: Path) -> str:
    return path.relative_to(ROOT).as_posix()


_TEXT_CACHE: dict[str, str] = {}
_LINES_CACHE: dict[str, list[str]] = {}


def read_text(path: Path) -> str:
    key = str(path)
    if key in _TEXT_CACHE:
        return _TEXT_CACHE[key]
    try:
        text = path.read_text(encoding="utf-8")
    except UnicodeDecodeError:
        text = path.read_text(encoding="utf-8", errors="ignore")
    _TEXT_CACHE[key] = text
    return text


def lines_for(path: Path) -> list[str]:
    key = str(path)
    if key not in _LINES_CACHE:
        _LINES_CACHE[key] = read_text(path).splitlines()
    return _LINES_CACHE[key]


def iter_files(base: Path, pattern: str) -> Iterable[Path]:
    if not base.exists():
        return
    for path in sorted(base.rglob(pattern)):
        if any(part in EXCLUDED_DIR_PARTS for part in path.relative_to(base).parts):
            continue
        if path.is_file():
            yield path


def count_lines(text: str) -> list[str]:
    return text.splitlines()


def find_line(path: Path, needle: str, start: int = 1) -> int:
    """Return the 1-based line number of the first occurrence of *needle*."""
    text = read_text(path)
    for number, line in enumerate(text.splitlines(keepends=True), start=1):
        if number >= start and needle in line:
            return number
    raise SystemExit(f"anchor not found: {needle!r} in {rel(path)}")


def find_line_optional(path: Path, needle: str) -> int:
    try:
        return find_line(path, needle)
    except SystemExit:
        return 0


def anchor(path: Path, line: int, kind: str = "") -> str:
    suffix = f" ({kind})" if kind else ""
    return f"{rel(path)}:{line}{suffix}"


def sha256_manifest(paths: Iterable[Path]) -> str:
    digest = hashlib.sha256()
    for path in sorted(paths, key=lambda item: rel(item)):
        digest.update(rel(path).encode("utf-8"))
        digest.update(b"\0")
        digest.update(read_text(path).encode("utf-8"))
        digest.update(b"\0")
    return digest.hexdigest()


def md_cell(value: str) -> str:
    return str(value).replace("|", "\\|").replace("\n", " ")


# ---------------------------------------------------------------------------
# Dimension A: synthetic tool dispatch
# ---------------------------------------------------------------------------


def parse_python_synthetic_tools() -> dict[str, Any]:
    """Extract builders, the phase registry, and agent_loop dispatch branches."""
    text = read_text(PY_SYNTHETIC)
    lines = count_lines(text)

    # Builder functions and the tool name they construct.
    builder_fns: dict[str, dict[str, Any]] = {}
    fn_starts = [
        (index, match.group(1))
        for index, line in enumerate(lines)
        if (match := re.match(r"^def (build_[a-z0-9_]+_tool)\(", line))
    ]
    for position, (index, fn_name) in enumerate(fn_starts):
        end = fn_starts[position + 1][0] if position + 1 < len(fn_starts) else len(lines)
        body = "\n".join(lines[index:end])
        name_match = re.search(r'\bname\s*=\s*"([^"]+)"', body)
        tool_name = name_match.group(1) if name_match else fn_name
        builder_fns[tool_name] = {"function": fn_name, "line": index + 1}

    # SYNTHETIC_PHASE_TOOL_BUILDERS keys.
    phase_builders: dict[str, dict[str, Any]] = {}
    for index, line in enumerate(lines):
        if "SYNTHETIC_PHASE_TOOL_BUILDERS" in line and "=" in line:
            for inner in range(index + 1, len(lines)):
                if lines[inner].strip() == "}":
                    break
                match = re.match(r'\s*"([^"]+)"\s*:\s*([A-Za-z0-9_]+)', lines[inner])
                if match:
                    phase_builders[match.group(1)] = {
                        "builder": match.group(2),
                        "line": inner + 1,
                    }
            break

    # agent_loop.py tc.tool_name == "<name>" dispatch branches.
    loop_lines = count_lines(read_text(PY_AGENT_LOOP))
    branches: dict[str, dict[str, Any]] = {}
    for index, line in enumerate(loop_lines):
        match = re.search(r'tc\.tool_name\s*==\s*"([^"]+)"', line)
        if not match:
            continue
        name = match.group(1)
        entry = branches.setdefault(name, {"line": index + 1, "guards": []})
        if "new_colony" in line and "task_create" in line:
            entry["guards"].append("new_colony=true")

    return {
        "builders": builder_fns,
        "phase_builders": phase_builders,
        "branches": branches,
        "manifest": sha256_manifest([PY_SYNTHETIC, PY_AGENT_LOOP]),
    }


def parse_go_tool_constants(go_files: list[Path]) -> dict[str, str]:
    constants: dict[str, str] = {}
    pattern = re.compile(
        r"^\s*(?:const\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*"
        r'(?:[A-Za-z_][A-Za-z0-9_.\[\]]*\s*)?=\s*"([^"]+)"'
    )
    for path in go_files:
        for line in lines_for(path):
            match = pattern.match(line)
            if match and not line.lstrip().startswith("//"):
                constants.setdefault(match.group(1), match.group(2))
    return constants


# Files whose Name comparisons are history/log/surface classification, not dispatch.
# tool_surface_flat.go renders the CLI tool catalogue and contains `case
# "suggest_colony":` prose branches that explicitly say the tool is not ported;
# treating those as dispatch would hide the real gap.
NON_DISPATCH_FILES = {"conversation.go", "runtime_log.go", "tool_surface_flat.go"}
REGISTRY_FILES = {
    "synthetic_tools.go",
    "dynamic_tools.go",
    "routes_session_live_tools.go",
    "phase_tool_surface.go",
    "tool_surface_flat.go",
}
PROMPT_FILES = {"prompt_sections.go", "static_prompt.go"}


def scan_go_name(
    name: str,
    go_files: list[Path],
    constants: dict[str, str],
) -> list[dict[str, Any]]:
    """Collect same-name Go evidence, classified by kind."""
    evidence: list[dict[str, Any]] = []
    literal = re.escape(name)
    for path in go_files:
        in_non_dispatch_file = path.name in NON_DISPATCH_FILES
        for index, line in enumerate(lines_for(path)):
            if (
                name not in line
                and "Name:" not in line
                and ".Name" not in line
                and "ToolName" not in line
                and not line.lstrip().startswith("case ")
            ):
                continue
            if line.lstrip().startswith("//"):
                continue
            number = index + 1
            kind = ""

            # Name / ToolName == "literal"
            if not in_non_dispatch_file and re.search(
                rf'\b(?:[A-Za-z_][A-Za-z0-9_]*\.)?(?:ToolName|Name)\s*(?:==|!=)\s*"{literal}"',
                line,
            ):
                kind = "dispatch"
            # Name / ToolName == Constant where the constant resolves to name.
            elif not in_non_dispatch_file:
                match = re.search(
                    r"\b(?:[A-Za-z_][A-Za-z0-9_]*\.)?(?:ToolName|Name)\s*(?:==|!=)\s*([A-Za-z_][A-Za-z0-9_]*)",
                    line,
                )
                if match and constants.get(match.group(1)) == name:
                    kind = "dispatch"
            # case "<literal>":
            if not kind and not in_non_dispatch_file and re.match(rf'^\s*case\s+"{literal}"\s*:', line):
                kind = "dispatch"
            # case Constant where the constant resolves to name (e.g.
            # `case SuggestColonyToolName:` in colony_dispatch.go).
            if not kind and not in_non_dispatch_file:
                match = re.match(r'^\s*case\s+([A-Za-z_][A-Za-z0-9_]*)\s*:', line)
                if match and constants.get(match.group(1)) == name:
                    kind = "dispatch"
            # Tool definition literal or a named constant that resolves to it.
            if not kind and re.search(rf'\bName:\s*"{literal}"', line):
                kind = "builder" if path.name == "synthetic_tools.go" else "tool_def"
            if not kind:
                match = re.search(r"\bName:\s*([A-Za-z_][A-Za-z0-9_]*)", line)
                if match and constants.get(match.group(1)) == name:
                    kind = "builder" if path.name == "synthetic_tools.go" else "tool_def"
            # Registry map key (phase builders, dynamic refresh, live tools).
            if not kind and path.name in REGISTRY_FILES and re.match(rf'^\s*"{literal}"\s*:', line):
                kind = "registration"
            # Named constant for the tool.
            if not kind and re.match(
                rf"\s*(?:const\s+)?[A-Za-z_][A-Za-z0-9_]*\s*"
                rf'(?:[A-Za-z_][A-Za-z0-9_.\[\]]*\s*)?=\s*"{literal}"',
                line,
            ):
                kind = "builder"
            # Prompt prose mention.
            if not kind and path.name in PROMPT_FILES and f'"{name}"' in line:
                kind = "prompt"

            if kind:
                evidence.append(
                    {
                        "kind": kind,
                        "path": rel(path),
                        "line": number,
                        "text": line.strip()[:160],
                    }
                )
    return evidence


def build_synthetic_rows(py: dict[str, Any], go_files: list[Path], constants: dict[str, str]) -> list[dict[str, Any]]:
    names = sorted(set(py["builders"]) | set(py["phase_builders"]) | set(py["branches"]))
    rows: list[dict[str, Any]] = []
    for name in names:
        builder = py["builders"].get(name)
        phase = py["phase_builders"].get(name)
        branch = py["branches"].get(name)

        anchors: list[str] = []
        sources: list[str] = []
        if branch:
            anchors.append(anchor(PY_AGENT_LOOP, branch["line"], "dispatch branch"))
            sources.append("agent_loop dispatch")
        if builder:
            anchors.append(anchor(PY_SYNTHETIC, builder["line"], f"{builder['function']}"))
            sources.append("synthetic builder")
        if phase:
            anchors.append(anchor(PY_SYNTHETIC, phase["line"], "SYNTHETIC_PHASE_TOOL_BUILDERS"))
            sources.append("phase builder registry")

        evidence = scan_go_name(name, go_files, constants)
        kinds = {item["kind"] for item in evidence}
        if "dispatch" in kinds or "tool_def" in kinds:
            status = "dispatch_port"
        elif "builder" in kinds:
            status = "builder_only"
        else:
            status = "missing"

        priority = {"dispatch": 0, "tool_def": 1, "builder": 2, "registration": 3, "prompt": 4}
        ordered = sorted(
            evidence,
            key=lambda item: (priority.get(item["kind"], 9), item["path"], item["line"]),
        )
        go_anchors = "; ".join(anchor(ROOT / item["path"], item["line"], item["kind"]) for item in ordered[:4])

        notes = []
        if phase:
            notes.append("phase-gated synthetic (builders map only)")
        if branch and branch["guards"]:
            notes.append("branch guard: " + ", ".join(sorted(set(branch["guards"]))))
        if "prompt" in kinds and not ({"dispatch", "tool_def", "builder", "registration"} & kinds):
            notes.append("Go has prompt prose only")
        if not evidence:
            notes.append("no same-name Go reference found")
        if status == "builder_only":
            notes.append("builder/definition only; no same-name dispatch/intercept in Go")
        if status == "missing" and evidence:
            notes.append("name registered in Go but no callable tool definition")
        if name == "task_create":
            notes.append("new_colony pivot absent at HEAD; see gap:task_create_new_colony_pivot")

        rows.append(
            {
                "id": f"synthetic:{name}",
                "tool": name,
                "upstream_anchor": "; ".join(anchors),
                "go_anchor": go_anchors,
                "status": status,
                "owner": "agentloop",
                "notes": "; ".join(notes),
                "sources": sources,
            }
        )
    return rows


# ---------------------------------------------------------------------------
# Dimension B: events, both directions
# ---------------------------------------------------------------------------


def parse_python_events() -> list[dict[str, Any]]:
    text = read_text(PY_EVENT_BUS)
    lines = count_lines(text)
    start = None
    for index, line in enumerate(lines):
        if line.startswith("class EventType(StrEnum):"):
            start = index + 1
            break
    if start is None:
        raise SystemExit("EventType class not found in event_bus.py")
    events = []
    for index in range(start, len(lines)):
        line = lines[index]
        if line and not line.startswith((" ", "\t")):
            break
        match = re.match(r'^\s{4}([A-Z][A-Z0-9_]*)\s*=\s*"([^"]+)"', line)
        if match:
            events.append(
                {
                    "constant": match.group(1),
                    "value": match.group(2),
                    "path": PY_EVENT_BUS,
                    "line": index + 1,
                }
            )
    return events


def parse_go_events() -> tuple[list[dict[str, Any]], dict[str, str]]:
    events = []
    constants: dict[str, str] = {}
    pattern = re.compile(r'^\s*(?:const\s+)?(Event[A-Za-z0-9_]+)\s+EventType\s*=\s*"([^"]+)"')
    for index, line in enumerate(lines_for(GO_EVENT_TYPE)):
        match = pattern.match(line)
        if match:
            events.append(
                {
                    "constant": match.group(1),
                    "value": match.group(2),
                    "line": index + 1,
                }
            )
            constants[match.group(1)] = match.group(2)
    return events, constants


def go_function_spans(lines: list[str]) -> list[tuple[int, int, str, str]]:
    starts = [index for index, line in enumerate(lines) if line.startswith("func ")]
    spans = []
    for position, start in enumerate(starts):
        end = starts[position + 1] if position + 1 < len(starts) else len(lines)
        header = lines[start]
        match = re.match(r"func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)", header)
        name = match.group(1) if match else ""
        spans.append((start, end, name, "\n".join(lines[start:end])))
    return spans


def find_function(span_list: list[tuple[int, int, str, str]], line_index: int) -> tuple[str, str]:
    chosen = ("", "")
    for start, end, name, body in span_list:
        if start <= line_index < end:
            chosen = (name, body)
        elif start > line_index:
            break
    return chosen


def build_event_emitter_index(
    go_files: list[Path],
) -> tuple[dict[str, list[tuple[str, int]]], dict[str, list[tuple[str, int]]]]:
    """Map Go event constant -> emitter sites, plus every non-declaration usage."""
    index: dict[str, list[tuple[str, int]]] = defaultdict(list)
    usages: dict[str, list[tuple[str, int]]] = defaultdict(list)
    for path in go_files:
        if path.name in {"event_type.go", "event_type_lookup.go"}:
            continue
        lines = lines_for(path)
        spans = go_function_spans(lines)
        for number, line in enumerate(lines, start=1):
            stripped = line.strip()
            if not stripped or stripped.startswith("//"):
                continue
            for ident in re.findall(r"\bEvent[A-Za-z0-9_]+\b", line):
                if stripped == ident or stripped == ident + ",":
                    continue
                usages[ident].append((rel(path), number))
                func_name, body = find_function(spans, number - 1)
                is_emitter = func_name.startswith("Emit") or "Publish(" in body
                if is_emitter:
                    index[ident].append((rel(path), number))
    return index, usages


def build_frontend_event_index(values: set[str]) -> dict[str, list[tuple[str, int]]]:
    index: dict[str, list[tuple[str, int]]] = defaultdict(list)
    quoted = re.compile(r'["\'`]([a-z][a-z0-9_]{2,})["\'`]')
    for path in iter_files(FRONTEND_SRC, "*.ts"):
        if ".test." in path.name or ".spec." in path.name:
            continue
        for number, line in enumerate(lines_for(path), start=1):
            for match in quoted.finditer(line):
                value = match.group(1)
                if value in values:
                    index[value].append((rel(path), number))
    for path in iter_files(FRONTEND_SRC, "*.tsx"):
        if ".test." in path.name or ".spec." in path.name:
            continue
        for number, line in enumerate(lines_for(path), start=1):
            for match in quoted.finditer(line):
                value = match.group(1)
                if value in values:
                    index[value].append((rel(path), number))
    return index


def build_event_rows(
    py_events: list[dict[str, Any]],
    go_events: list[dict[str, Any]],
    emitter_index: dict[str, list[tuple[str, int]]],
    usage_index: dict[str, list[tuple[str, int]]],
    frontend_index: dict[str, list[tuple[str, int]]],
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    go_by_value = {item["value"]: item for item in go_events}
    py_values = {item["value"] for item in py_events}
    rows: list[dict[str, Any]] = []

    for event in sorted(py_events, key=lambda item: item["value"]):
        value = event["value"]
        go = go_by_value.get(value)
        emit_sites = emitter_index.get(go["constant"], []) if go else []
        usages = usage_index.get(go["constant"], []) if go else []
        front_sites = frontend_index.get(value, [])
        has_const = go is not None
        has_emitter = bool(emit_sites)
        has_frontend = bool(front_sites)

        if not has_const:
            status = "missing"
        elif has_emitter and has_frontend:
            status = "ported"
        elif has_emitter or has_frontend:
            status = "partial"
        else:
            status = "go_const_only"

        go_anchor_parts = []
        if go:
            go_anchor_parts.append(anchor(GO_EVENT_TYPE, go["line"], "const"))
        if emit_sites:
            path, line = sorted(emit_sites)[0]
            go_anchor_parts.append(anchor(ROOT / path, line, "emitter"))
        if front_sites:
            path, line = sorted(front_sites)[0]
            go_anchor_parts.append(anchor(ROOT / path, line, "frontend"))

        missing = []
        if not has_const:
            missing.append("Go const")
        if not has_emitter:
            missing.append("Go emitter")
        if not has_frontend:
            missing.append("frontend handler")
        notes = "missing: " + ", ".join(missing) if missing else "all three layers present"
        if emit_sites:
            notes += f"; emitter sites={len(emit_sites)}"
        elif usages:
            notes += f"; {len(usages)} non-Publish reference(s) — may be a direct queue, not an Emit*/Publish emitter"
        if front_sites:
            notes += f"; frontend refs={len(front_sites)}"

        rows.append(
            {
                "id": f"event:{value}",
                "name": value,
                "upstream_anchor": anchor(event["path"], event["line"], event["constant"]),
                "go_anchor": "; ".join(go_anchor_parts),
                "status": status,
                "owner": "eventbus" if has_const else "agentloop+frontend",
                "notes": notes,
                "go_const": has_const,
                "go_emitter": has_emitter,
                "frontend_handler": has_frontend,
            }
        )

    go_only = [item for item in go_events if item["value"] not in py_values]
    for event in sorted(go_only, key=lambda item: item["value"]):
        value = event["value"]
        emit_sites = emitter_index.get(event["constant"], [])
        front_sites = frontend_index.get(value, [])
        rows.append(
            {
                "id": f"event:go_only:{value}",
                "name": value,
                "upstream_anchor": "(no Python EventType constant)",
                "go_anchor": anchor(GO_EVENT_TYPE, event["line"], "const"),
                "status": "go_only",
                "owner": "eventbus",
                "notes": (
                    f"frontend_handler={'yes' if front_sites else 'no'}; go_emitter={'yes' if emit_sites else 'no'}"
                ),
                "go_const": True,
                "go_emitter": bool(emit_sites),
                "frontend_handler": bool(front_sites),
            }
        )

    summary = {
        "python": len(py_events),
        "go": len(go_events),
        "ported": sum(1 for row in rows if row["status"] == "ported"),
        "partial": sum(1 for row in rows if row["status"] == "partial"),
        "go_const_only": sum(1 for row in rows if row["status"] == "go_const_only"),
        "missing_go_const": sum(1 for row in rows if row["status"] == "missing"),
        "go_only": len(go_only),
        "emitter_missing": sum(1 for row in rows if row["go_const"] and not row["go_emitter"]),
        "frontend_missing": sum(1 for row in rows if row["go_const"] and not row["frontend_handler"]),
    }
    return rows, summary


# ---------------------------------------------------------------------------
# Dimension C: phase-gated behaviours
# ---------------------------------------------------------------------------


def parse_phase_tool_lists() -> tuple[dict[str, list[int]], list[tuple[str, int]]]:
    """Return name -> [line numbers], plus conditional appends as (phase, line)."""
    lines = count_lines(read_text(PY_QUEEN_NODES))
    names: dict[str, list[int]] = OrderedDict()
    current_phase: str | None = None
    for index, line in enumerate(lines):
        if line.startswith("_QUEEN_INDEPENDENT_TOOLS"):
            current_phase = "independent"
            continue
        if line.startswith("_QUEEN_COLONY_TOOLS"):
            current_phase = "colony"
            continue
        if current_phase and line.startswith("]"):
            current_phase = None
            continue
        if current_phase:
            match = re.match(r'\s*"([a-z0-9_]+)"\s*,', line)
            if match:
                names.setdefault(match.group(1), []).append(index + 1)

    appends: list[tuple[str, int]] = []
    for index, line in enumerate(lines):
        match = re.search(r'_QUEEN_(INDEPENDENT|COLONY)_TOOLS\.append\("([^"]+)"\)', line)
        if match:
            phase = "independent" if match.group(1) == "INDEPENDENT" else "colony"
            names.setdefault(match.group(2), []).append(index + 1)
            appends.append((phase, index + 1))
    return names, appends


def phase_status(kinds: set[str]) -> str:
    if "dispatch" in kinds or "tool_def" in kinds:
        return "port_ungated"
    if "builder" in kinds or "registration" in kinds:
        return "builder_only"
    if "prompt" in kinds:
        return "prompt_only"
    return "missing"


def build_phase_rows(
    phase_names: dict[str, list[int]],
    go_files: list[Path],
    constants: dict[str, str],
) -> tuple[list[dict[str, Any]], list[dict[str, Any]], dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    text_lines = count_lines(read_text(PY_QUEEN_NODES))
    for name, line_numbers in sorted(phase_names.items()):
        # Phase membership is decided by the list section each hit falls in.
        phases: list[str] = []
        for line_no in line_numbers:
            # Find the nearest preceding list declaration.
            preceding = text_lines[: line_no - 1]
            last_independent = max(
                (i for i, value in enumerate(preceding) if value.startswith("_QUEEN_INDEPENDENT_TOOLS")),
                default=-1,
            )
            last_colony = max(
                (i for i, value in enumerate(preceding) if value.startswith("_QUEEN_COLONY_TOOLS")),
                default=-1,
            )
            # Conditional appends are outside either block; they belong to both.
            if ".append(" in text_lines[line_no - 1]:
                phases.extend(["independent", "colony"])
                continue
            if last_independent > last_colony:
                phases.append("independent")
            elif last_colony > last_independent:
                phases.append("colony")
            else:
                phases.extend(["independent", "colony"])
        phases = sorted(set(phases))

        evidence = scan_go_name(name, go_files, constants)
        kinds = {item["kind"] for item in evidence}
        status = phase_status(kinds)
        priority = {"dispatch": 0, "tool_def": 1, "builder": 2, "registration": 3, "prompt": 4}
        ordered = sorted(
            evidence,
            key=lambda item: (priority.get(item["kind"], 9), item["path"], item["line"]),
        )
        go_anchor = "; ".join(anchor(ROOT / item["path"], item["line"], item["kind"]) for item in ordered[:4])
        notes = f"Python phases: {', '.join(phases) or 'conditional'}"
        if status == "port_ungated":
            notes += "; Go tool exists but has no explicit phase-list gate"
        elif status == "builder_only":
            notes += "; Go has only a builder/registration surface"
        elif status == "prompt_only":
            notes += "; Go prompt only"
        rows.append(
            {
                "id": f"phase_tool:{name}",
                "name": name,
                "phases": phases,
                "upstream_anchor": "; ".join(anchor(PY_QUEEN_NODES, line, "phase list") for line in line_numbers),
                "go_anchor": go_anchor,
                "status": status,
                "owner": "queen",
                "notes": notes,
            }
        )

    # Pivot fields.
    pivot_rows: list[dict[str, Any]] = []
    py_pivot = find_line(PY_QUEEN_NODES, "the field is `new_session` in DM, `new_colony`")
    new_session_line = find_line(PY_QUEEN_ORCH, 'field_name="new_session"')
    new_colony_line = find_line(PY_QUEEN_ORCH, 'field_name="new_colony"')
    prod_new_session = find_line_optional(
        GO_INTERNAL / "cli" / "session_tools_task_pivot.go", 'FieldName:          "new_session"'
    )
    prod_new_colony = find_line_optional(GO_INTERNAL / "cli" / "session_tools_task_pivot.go", "tasks.NewColonyPivotField")
    test_new_colony = find_line_optional(GO_INTERNAL / "tasks" / "tools_test.go", 'FieldName:        "new_colony"')

    pivot_rows.append(
        {
            "id": "phase_pivot:new_session",
            "name": "new_session",
            "phases": ["independent"],
            "upstream_anchor": (
                f"{anchor(PY_QUEEN_NODES, py_pivot, 'prompt')}; "
                f"{anchor(PY_QUEEN_ORCH, new_session_line, 'PivotHandler')}"
            ),
            "go_anchor": (
                anchor(GO_INTERNAL / "cli" / "session_tools_task_pivot.go", prod_new_session, "PivotHandler")
                if prod_new_session
                else ""
            ),
            "status": "ported" if prod_new_session else "missing",
            "owner": "tasks",
            "notes": "DM pivot wired in the CLI session catalogue",
        }
    )
    pivot_rows.append(
        {
            "id": "phase_pivot:new_colony",
            "name": "new_colony",
            "phases": ["colony"],
            "upstream_anchor": (
                f"{anchor(PY_QUEEN_NODES, py_pivot, 'prompt')}; "
                f"{anchor(PY_QUEEN_ORCH, new_colony_line, 'PivotHandler')}"
            ),
            "go_anchor": (
                anchor(GO_INTERNAL / "cli" / "session_tools_task_pivot.go", prod_new_colony, "PivotHandler")
                if prod_new_colony
                else (anchor(GO_INTERNAL / "tasks" / "tools_test.go", test_new_colony, "test only") if test_new_colony else "")
            ),
            "status": "ported" if prod_new_colony else "missing",
            "owner": "tasks",
            "notes": (
                "Colony-phase pivot wired in the CLI session catalogue (tasks.NewColonyPivotField); "
                "the lean-handoff accept endpoint remains 501 (gap:colony_pivot_lean_handoff)"
            ),
        }
    )

    # Phase prompt entry points.
    prompt_rows: list[dict[str, Any]] = []
    for phase, py_needle, go_needle, go_path in (
        ("independent", '_queen_role_independent = """', "func IndependentPhasePrompt()", GO_QUEEN_STATIC),
        ("colony", '_queen_role_colony = """', "func ColonyPhasePrompt()", GO_QUEEN_STATIC),
    ):
        py_line = find_line(PY_QUEEN_NODES, py_needle)
        go_line = find_line(go_path, go_needle)
        prompt_rows.append(
            {
                "id": f"phase_prompt:{phase}",
                "name": phase,
                "phases": [phase],
                "upstream_anchor": anchor(PY_QUEEN_NODES, py_line, "role prompt"),
                "go_anchor": anchor(go_path, go_line, "PhasePrompt"),
                "status": "ported",
                "owner": "queen",
                "notes": "Go PhasePrompt entry point exists; prompt text parity is covered by golden tests",
            }
        )

    summary = {
        "phase_tools": len(rows),
        "port_ungated": sum(1 for row in rows if row["status"] == "port_ungated"),
        "builder_only": sum(1 for row in rows if row["status"] == "builder_only"),
        "prompt_only": sum(1 for row in rows if row["status"] == "prompt_only"),
        "missing": sum(1 for row in rows if row["status"] == "missing"),
        "pivot_ported": sum(1 for row in pivot_rows if row["status"] == "ported"),
        "pivot_missing": sum(1 for row in pivot_rows if row["status"] == "missing"),
        "explicit_phase_lists_in_go": False,
    }
    return rows, pivot_rows + prompt_rows, summary


# ---------------------------------------------------------------------------
# Dimension D: test mapping (counts only)
# ---------------------------------------------------------------------------


TEST_DIR_MAP: dict[str, list[str]] = {
    "core/tests": ["internal"],
    "core/tests/e2e": ["e2e"],
    "core/tests/live": ["e2e"],
    "core/framework/agent_loop/tests": ["internal/agentloop"],
    "core/framework/server/tests": ["internal/server"],
    "core/framework/tasks/tests": ["internal/tasks"],
    "core/framework/agents/queen/tests": ["internal/queen"],
    "core/framework/credentials/tests": ["internal/credentials"],
    "core/framework/credentials/aden/tests": ["internal/credentials"],
    "core/framework/global_db/tests": ["internal/globaldb"],
    "core/framework/runtime/tests": ["internal/runtimehealth"],
    "core/framework/testing": [],
    "tools/tests": ["internal/tools"],
    "tools/tests/credentials": ["internal/credentials"],
    "tools/tests/integrations": ["internal/tools/integrations"],
    "tools/tests/tools": ["internal/tools"],
}


def normalize_parity_ref(raw: str) -> str:
    raw = raw.strip().rstrip(".,;)")
    if raw.startswith("core/"):
        return raw
    if raw.startswith(("tests/", "framework/")):
        return "core/" + raw
    if raw.startswith("agents/"):
        return "core/framework/" + raw
    if raw.startswith("tools/"):
        return raw
    return raw


def module_prefix(directory_rel: str) -> str:
    """Module a test directory covers.

    ``core/framework/agent_loop/tests`` covers ``core/framework/agent_loop/``;
    a bare top-level suite such as ``core/tests`` stays as-is.  This lets a
    ``// Parity: framework/agent_loop/agent_loop.py`` ref count toward the
    agent_loop test row instead of requiring a ref to the test file itself.
    """
    parts = directory_rel.split("/")
    if parts[-1] == "tests" and len(parts) >= 3:
        return "/".join(parts[:-1]) + "/"
    return directory_rel + "/"


def count_python_tests(path: Path) -> int:
    return len(re.findall(r"^\s*(?:async\s+)?def test_", read_text(path), re.M))


def count_go_tests(package_rel: str) -> tuple[int, str]:
    base = GO / package_rel
    if not base.exists():
        return 0, ""
    if package_rel in {"internal", "e2e"}:
        files = list(iter_files(base, "*_test.go"))
    else:
        files = sorted(
            path
            for path in base.glob("*_test.go")
            if path.is_file() and not any(part in EXCLUDED_DIR_PARTS for part in path.parts)
        )
    total = sum(len(re.findall(r"^func Test", read_text(path), re.M)) for path in files)
    first = rel(files[0]) if files else ""
    return total, first


def build_test_rows() -> tuple[list[dict[str, Any]], dict[str, Any]]:
    directories: dict[Path, int] = OrderedDict()
    for base in (ROOT / "core", ROOT / "tools"):
        for path in iter_files(base, "test_*.py"):
            directories.setdefault(path.parent, 0)
            directories[path.parent] += count_python_tests(path)

    parity_refs: dict[str, list[tuple[str, int]]] = defaultdict(list)
    prefixes = {module_prefix(rel(directory)): directory for directory in directories}
    for path in iter_files(GO, "*.go"):
        for number, line in enumerate(lines_for(path), start=1):
            if "// Parity:" not in line:
                continue
            tail = line.split("// Parity:", 1)[1]
            for match in re.finditer(
                r"(?:(?:core/)?(?:framework|tests|tools|agents)/[A-Za-z0-9_./-]+\.py)",
                tail,
            ):
                normalized = normalize_parity_ref(match.group(0))
                for prefix in prefixes:
                    if normalized.startswith(prefix):
                        parity_refs[prefix].append((rel(path), number))

    rows: list[dict[str, Any]] = []
    for directory, python_tests in sorted(directories.items(), key=lambda item: rel(item[0])):
        if python_tests == 0:
            # test_case.py / test_storage.py in core/framework/testing are the
            # test harness itself, not test cases.
            continue
        directory_rel = rel(directory)
        packages = TEST_DIR_MAP.get(directory_rel)
        if packages is None:
            # Heuristic fallback for a new upstream test directory.
            if directory_rel.startswith("core/framework/"):
                guessed = directory_rel.split("/")[2].replace("_", "")
                packages = [f"internal/{guessed}"]
            elif directory_rel.startswith("tools/"):
                packages = ["internal/tools"]
            else:
                packages = []

        go_tests = 0
        go_anchor = ""
        for package in packages:
            count, first = count_go_tests(package)
            go_tests += count
            if first and not go_anchor:
                go_anchor = f"{rel(GO / package)}/ (first: {first})"
        refs = parity_refs.get(module_prefix(directory_rel), [])
        ref_count = len(refs)

        if ref_count > 0:
            status = "mapped"
        elif go_tests > 0:
            status = "go_tests_no_parity_ref"
        else:
            status = "missing"

        notes = []
        if "+" in directory_rel:
            notes.append("unexpected '+' in path")
        if len(packages) > 1:
            notes.append("multi-package mapping")
        if directory_rel == "core/tests":
            notes.append("cross-cutting aggregate; overlaps package rows")
        if not packages:
            notes.append("no Go package mapped (Python-side test harness)")
        if ref_count == 0 and go_tests > 0:
            notes.append("Go tests exist but none cite this Python directory")

        rows.append(
            {
                "id": f"tests:{directory_rel}",
                "path": directory_rel,
                "upstream_anchor": f"{directory_rel}/ ({python_tests} def test_)",
                "go_anchor": go_anchor,
                "status": status,
                "owner": "qa",
                "notes": "; ".join(notes),
                "python_tests": python_tests,
                "go_tests": go_tests,
                "parity_refs": ref_count,
                "go_packages": packages,
            }
        )

    all_go_tests = sum(len(re.findall(r"^func Test", read_text(path), re.M)) for path in iter_files(GO, "*_test.go"))
    all_parity_refs = sum(1 for path in iter_files(GO, "*.go") for line in lines_for(path) if "// Parity:" in line)
    summary = {
        "python_tests": sum(row["python_tests"] for row in rows),
        "directories": len(rows),
        "go_tests": all_go_tests,
        "parity_refs": all_parity_refs,
        "mapped": sum(1 for row in rows if row["status"] == "mapped"),
        "go_tests_no_parity_ref": sum(1 for row in rows if row["status"] == "go_tests_no_parity_ref"),
        "missing": sum(1 for row in rows if row["status"] == "missing"),
    }
    return rows, summary


# ---------------------------------------------------------------------------
# Known gaps (human-reviewed, status is manual)
# ---------------------------------------------------------------------------


def build_known_gaps() -> list[dict[str, Any]]:
    def optional(path: Path, needle: str) -> str:
        line = find_line_optional(path, needle)
        return anchor(path, line) if line else rel(path)

    return [
        {
            "id": "gap:suggest_colony_dispatch",
            "title": "suggest_colony has no Go dispatch/intercept",
            "upstream_anchor": optional(PY_AGENT_LOOP, 'tc.tool_name == "suggest_colony"'),
            "go_anchor": optional(GO_SYNTHETIC, '"suggest_colony": BuildSuggestColonyTool'),
            "status": "fixed",
            "owner": "agentloop",
            "lane": "colony-suggestion-dispatch",
            "commit": "f23550cb (merge 72e7ce0c)",
            "tests": "internal/agentloop/colony_dispatch_test.go; internal/server/chat_colony_dispatch_test.go",
            "notes": (
                "Dispatch intercept, park reason, event payload and phase tool surface landed. "
                "The DM accept path still does not compact the source into a colony queen seed "
                "(separate open gap)."
            ),
        },
        {
            "id": "gap:task_create_new_colony_pivot",
            "title": "task_create(new_colony=true) pivot has no production Go handler",
            "upstream_anchor": optional(PY_QUEEN_ORCH, 'field_name="new_colony"'),
            "go_anchor": optional(
                GO_INTERNAL / "cli" / "session_tools_task_pivot.go", 'FieldName:          "new_session"'
            ),
            "status": "fixed",
            "owner": "tasks",
            "lane": "colony-suggestion-dispatch",
            "commit": "f23550cb (merge 72e7ce0c)",
            "tests": "internal/cli/colony_pivot_accept_test.go; internal/agentloop/colony_dispatch_test.go",
            "notes": (
                "The colony phase now exposes new_colony and the agentloop intercept/pending-pivot/"
                "event path landed. The lean-handoff accept endpoint is still 501 (separate open gap)."
            ),
        },
        {
            "id": "gap:colony_pivot_lean_handoff",
            "title": "colony pivot accept (lean-handoff sibling colony) is not implemented",
            "upstream_anchor": optional(ROOT / "core" / "framework" / "server" / "routes_sessions.py", "def _create_sibling_colony_from_colony"),
            "go_anchor": optional(GO_INTERNAL / "cli" / "loader.go", "the colony lean-handoff accept path is not implemented"),
            "status": "open",
            "owner": "server",
            "lane": "",
            "commit": "",
            "tests": "internal/cli/colony_pivot_accept_test.go::TestCreateColonyFromSourceRefusesColonyPivotAcceptPath",
            "notes": (
                "Accepting a colony-phase pivot currently returns 501. Upstream builds a sibling colony "
                "from goal/handoff/tasks, with no transcript copy, no compaction and no spawn lock."
            ),
        },
        {
            "id": "gap:dm_fork_seed_compaction",
            "title": "DM fork into a colony does not copy/compact the source session",
            "upstream_anchor": optional(ROOT / "core" / "framework" / "server" / "routes_execution.py", "compaction_status.mark_in_progress"),
            "go_anchor": optional(GO_INTERNAL / "cli" / "colony_worker_definition.go", "func writeColonyFromSession"),
            "status": "open",
            "owner": "server",
            "lane": "",
            "commit": "",
            "tests": "internal/cli/colony_pivot_accept_test.go::TestCreateColonyFromSourceWakesSourceQueenAndPublishesColonyCreated",
            "notes": (
                "Go creates metadata/worker.json and a fresh colony session only; upstream copytree's "
                "the queen session and schedules a background compaction into an inherited seed. "
                "Memories are not copied on either side (queen-scope memories stay shared)."
            ),
        },
        {
            "id": "gap:worker_skills_scope",
            "title": "colony worker skill catalog missed framework/user scopes",
            "upstream_anchor": optional(ROOT / "core" / "framework" / "skills" / "manager.py", "_apply_overrides"),
            "go_anchor": optional(
                GO_INTERNAL / "colony" / "skills_surface.go", "func (r *ColonyRuntime) workerSkillSurface"
            ),
            "status": "fixed",
            "owner": "skills",
            "lane": "skills-worker-scope",
            "commit": "f18d9e23 (merge 22170ab2)",
            "tests": "TestWorkerCatalogCoversFrameworkUserAndColonyVisibility",
            "notes": (
                "Worker surface now discovers framework/preset/user plus queen and colony "
                "scopes, with colony overriding same-name lower scopes."
            ),
        },
        {
            "id": "gap:queen_preset_defaults",
            "title": "queen profile default_preset_skills not wired into worker catalog",
            "upstream_anchor": optional(
                ROOT / "core" / "framework" / "agents" / "queen" / "queen_profiles.py",
                "def default_skills_for",
            ),
            "go_anchor": optional(GO_INTERNAL / "queen" / "profiles.go", "func PresetSkillsFromProfile"),
            "status": "fixed",
            "owner": "skills",
            "lane": "skills-worker-scope",
            "commit": "f18d9e23 (merge 22170ab2)",
            "tests": (
                "TestWorkerCatalogAppliesQueenProfileDefaultPresetSkills; TestPresetSkillsFromProfileReadsCustomList"
            ),
            "notes": "Role-default preset skills are applied when building the worker catalog.",
        },
        {
            "id": "gap:compaction_long_reflection",
            "title": "compaction did not publish context_compacted / run long reflection",
            "upstream_anchor": optional(
                ROOT / "core" / "framework" / "agents" / "queen" / "reflection_agent.py",
                "event_types=[EventType.CONTEXT_COMPACTED]",
            ),
            "go_anchor": optional(
                GO_INTERNAL / "server" / "chat_context_compacted.go",
                "func (r *ChatRuntime) wireContextCompactedReflection",
            ),
            "status": "fixed",
            "owner": "memory",
            "lane": "memory-compaction-reflect",
            "commit": "61acecb9 (merge 76c0835b)",
            "tests": (
                "TestContextCompactedTriggersLongReflectionOnce; "
                "TestCompactionPublishesContextCompactedOnce; "
                "TestBuildContextCompactedFieldsMatchUpstream"
            ),
            "notes": (
                "The loop now computes the upstream payload and the session bus subscription "
                "runs one long reflection; proactive microcompaction emits nothing."
            ),
        },
        {
            "id": "gap:memory_cloud_sync_schedule_push",
            "title": "memory routes do not schedule a cloud-sync push",
            "upstream_anchor": optional(
                ROOT / "core" / "framework" / "server" / "routes_memories.py",
                'schedule_push("memory", rel)',
            ),
            "go_anchor": optional(GO_INTERNAL / "server" / "routes_memories_extra.go", "type MemoryRouteDeps struct"),
            "status": "open",
            "owner": "memory",
            "lane": "",
            "commit": "",
            "tests": "",
            "notes": (
                "Go MemoryRouteDeps only carries Home; no SchedulePush seam and no cloud-sync "
                "implementation is wired. Upstream only pushes global/ and agents/queens/ "
                "paths via _maybe_sync_memory."
            ),
        },
        {
            "id": "gap:env_ledger_anchor",
            "title": "HIVE_COMPACTION_DEBUG ledger anchor went stale after loop split",
            "upstream_anchor": optional(
                ROOT / "core" / "framework" / "agent_loop" / "internals" / "compaction.py",
                'os.environ.get("HIVE_COMPACTION_DEBUG")',
            ),
            "go_anchor": optional(
                ROOT / "go_hive" / "contracts" / "config-support-ledger.json",
                '"HIVE_COMPACTION_DEBUG"',
            ),
            "status": "fixed",
            "owner": "governance",
            "lane": "wave83-fix",
            "commit": "abd646b7 (merge bb1bf6b1)",
            "tests": "TestCompactionDebugLogRunsOnTheLiveCompactionPath",
            "notes": (
                "The hand-maintained config-support-ledger anchor now points at "
                "writeCompactionReport(agentCtx, ratioBefore, ratioAfter, preInventory)."
            ),
        },
    ]


# ---------------------------------------------------------------------------
# Registry assembly
# ---------------------------------------------------------------------------


def generate() -> dict[str, Any]:
    go_tool_files = [path for path in iter_files(GO_INTERNAL, "*.go") if not path.name.endswith("_test.go")]
    go_constants = parse_go_tool_constants(go_tool_files)

    synthetic_py = parse_python_synthetic_tools()
    synthetic_rows = build_synthetic_rows(synthetic_py, go_tool_files, go_constants)

    py_events = parse_python_events()
    go_events, _ = parse_go_events()
    emitter_index, usage_index = build_event_emitter_index(
        [path for path in iter_files(GO, "*.go") if not path.name.endswith("_test.go")]
    )
    frontend_index = build_frontend_event_index(
        {item["value"] for item in py_events} | {item["value"] for item in go_events}
    )
    event_rows, event_summary = build_event_rows(py_events, go_events, emitter_index, usage_index, frontend_index)

    phase_names, _ = parse_phase_tool_lists()
    phase_rows, phase_extra_rows, phase_summary = build_phase_rows(phase_names, go_tool_files, go_constants)

    test_rows, test_summary = build_test_rows()
    gaps = build_known_gaps()

    synthetic_summary = {
        "total": len(synthetic_rows),
        "dispatch_port": sum(1 for row in synthetic_rows if row["status"] == "dispatch_port"),
        "builder_only": sum(1 for row in synthetic_rows if row["status"] == "builder_only"),
        "missing": sum(1 for row in synthetic_rows if row["status"] == "missing"),
    }

    all_rows = synthetic_rows + event_rows + phase_rows + phase_extra_rows + test_rows
    gap_summary = {
        "total": len(gaps),
        "fixed": sum(1 for gap in gaps if gap["status"] == "fixed"),
        "in_fix": sum(1 for gap in gaps if gap["status"] == "in_fix"),
        "open": sum(1 for gap in gaps if gap["status"] == "open"),
    }

    test_manifest = sorted(rel(path) for path in iter_files(ROOT / "core", "test_*.py")) + sorted(
        rel(path) for path in iter_files(ROOT / "tools", "test_*.py")
    )
    test_manifest += sorted(rel(path) for path in iter_files(GO, "*_test.go"))

    return {
        "schema_version": GENERATOR_VERSION,
        "metadata": {
            "generator": "scripts/gen_coverage_registry.py",
            "upstream": ["core/", "tools/"],
            "go_root": rel(GO),
            "frontend_root": rel(FRONTEND_SRC),
            "manifests": {
                "synthetic": sha256_manifest([PY_SYNTHETIC, PY_AGENT_LOOP, GO_SYNTHETIC]),
                "events": sha256_manifest([PY_EVENT_BUS, GO_EVENT_TYPE]),
                "phase": sha256_manifest([PY_QUEEN_NODES, PY_QUEEN_ORCH, GO_QUEEN_PROMPT, GO_QUEEN_STATIC]),
                "tests": sha256_manifest([ROOT / path for path in test_manifest]),
            },
            "note": "No wall-clock timestamp is emitted; reruns are byte-identical for identical inputs.",
        },
        "summary": {
            "synthetic_dispatch": synthetic_summary,
            "events": event_summary,
            "phase_gates": phase_summary,
            "test_mapping": test_summary,
            "known_gaps": gap_summary,
            "rows_total": len(all_rows),
        },
        "synthetic_dispatch": synthetic_rows,
        "events": event_rows,
        "phase_gates": phase_rows + phase_extra_rows,
        "test_mapping": test_rows,
        "known_gaps": gaps,
    }


# ---------------------------------------------------------------------------
# Rendering
# ---------------------------------------------------------------------------


def render_json(data: dict[str, Any]) -> str:
    return json.dumps(data, indent=2, sort_keys=True, ensure_ascii=False) + "\n"


def render_markdown(data: dict[str, Any]) -> str:
    summary = data["summary"]
    out: list[str] = []
    out.append("# Upstream → Go behaviour coverage registry")
    out.append("")
    out.append(
        "Generated by `scripts/gen_coverage_registry.py`. Do not edit by hand; "
        "regenerate and commit both this file and `go_hive/docs/coverage-registry.json`."
    )
    out.append("")
    out.append("## Summary")
    out.append("")
    syn = summary["synthetic_dispatch"]
    out.append(
        f"- Synthetic/tool dispatch: **{syn['total']}** rows — "
        f"{syn['dispatch_port']} dispatch_port, {syn['builder_only']} builder_only, "
        f"{syn['missing']} missing."
    )
    ev = summary["events"]
    out.append(
        f"- Events: **{ev['python']}** Python / **{ev['go']}** Go — "
        f"{ev['ported']} ported, {ev['partial']} partial, {ev['go_const_only']} const_only, "
        f"{ev['missing_go_const']} missing Go const, {ev['go_only']} Go-only; "
        f"emitter missing on {ev['emitter_missing']}, frontend handler missing on "
        f"{ev['frontend_missing']}."
    )
    ph = summary["phase_gates"]
    out.append(
        f"- Phase gates: **{ph['phase_tools']}** phase tools — "
        f"{ph['port_ungated']} port_ungated, {ph['builder_only']} builder_only, "
        f"{ph['prompt_only']} prompt_only, {ph['missing']} missing; pivot rows "
        f"{ph['pivot_ported']} ported / {ph['pivot_missing']} missing. Go has no "
        "explicit `_QUEEN_*_TOOLS` equivalent, so phase membership itself is not "
        "machine-verifiable."
    )
    tm = summary["test_mapping"]
    out.append(
        f"- Tests: **{tm['python_tests']}** Python `def test_` in {tm['directories']} dirs "
        f"vs **{tm['go_tests']}** Go `func Test`; {tm['mapped']} dirs mapped, "
        f"{tm['go_tests_no_parity_ref']} with Go tests but no `// Parity:` ref, "
        f"{tm['missing']} missing."
    )
    kg = summary["known_gaps"]
    out.append(f"- Known gaps: **{kg['total']}** — {kg['fixed']} fixed, {kg['in_fix']} in_fix, {kg['open']} open.")
    out.append("")

    def table(title: str, headers: list[str], rows: list[list[str]]) -> None:
        out.append(f"## {title}")
        out.append("")
        out.append("| " + " | ".join(headers) + " |")
        out.append("|" + "|".join("---" for _ in headers) + "|")
        for row in rows:
            out.append("| " + " | ".join(md_cell(cell) for cell in row) + " |")
        out.append("")

    table(
        "Synthetic / tool dispatch",
        ["ID", "Upstream anchor", "Go anchor", "Status", "Owner", "Notes"],
        [
            [
                row["id"],
                row["upstream_anchor"],
                row["go_anchor"] or "—",
                row["status"],
                row["owner"],
                row["notes"],
            ]
            for row in data["synthetic_dispatch"]
        ],
    )

    table(
        "Events (Python ↔ Go ↔ frontend)",
        ["ID", "Upstream anchor", "Go anchor", "Status", "Const", "Emitter", "Frontend"],
        [
            [
                row["id"],
                row["upstream_anchor"],
                row["go_anchor"] or "—",
                row["status"],
                "yes" if row["go_const"] else "no",
                "yes" if row["go_emitter"] else "no",
                "yes" if row["frontend_handler"] else "no",
            ]
            for row in data["events"]
        ],
    )

    table(
        "Phase-gated behaviours",
        ["ID", "Phases", "Upstream anchor", "Go anchor", "Status", "Notes"],
        [
            [
                row["id"],
                ", ".join(row.get("phases", [])),
                row["upstream_anchor"],
                row["go_anchor"] or "—",
                row["status"],
                row["notes"],
            ]
            for row in data["phase_gates"]
        ],
    )

    table(
        "Test mapping (counts only)",
        ["Directory", "Python tests", "Go tests", "Parity refs", "Status", "Go package / notes"],
        [
            [
                row["path"],
                str(row["python_tests"]),
                str(row["go_tests"]),
                str(row["parity_refs"]),
                row["status"],
                "; ".join(row["go_packages"]) + (f" — {row['notes']}" if row["notes"] else ""),
            ]
            for row in data["test_mapping"]
        ],
    )

    table(
        "Known gaps",
        ["ID", "Status", "Lane", "Commit", "Tests", "Notes"],
        [
            [
                row["id"],
                row["status"],
                row["lane"] or "—",
                row["commit"] or "—",
                row["tests"] or "—",
                row["notes"],
            ]
            for row in data["known_gaps"]
        ],
    )

    out.append("## How to use")
    out.append("")
    out.append("```bash")
    out.append("python3 go_hive/scripts/gen_coverage_registry.py          # regenerate")
    out.append("python3 go_hive/scripts/gen_coverage_registry.py --check  # CI gate (exit 1 on drift)")
    out.append("```")
    out.append("")
    out.append(
        "A `missing` row means no same-name Go evidence was found; it is a review "
        "prompt, not proof that every path is unimplemented. A `partial` event row "
        "means at least one of const / emitter / frontend handler is absent."
    )
    out.append("")
    return "\n".join(out)


# ---------------------------------------------------------------------------
# Check mode
# ---------------------------------------------------------------------------


def registry_diff(old: dict[str, Any], new: dict[str, Any]) -> list[str]:
    issues: list[str] = []
    if old.get("schema_version") != new.get("schema_version"):
        issues.append(f"schema_version: {old.get('schema_version')} -> {new.get('schema_version')}")
    if old.get("metadata") != new.get("metadata"):
        issues.append("metadata (input manifests) changed")
    if old.get("summary") != new.get("summary"):
        issues.append("summary counts changed")

    for section in (
        "synthetic_dispatch",
        "events",
        "phase_gates",
        "test_mapping",
        "known_gaps",
    ):
        old_rows = {row["id"]: row for row in old.get(section, [])}
        new_rows = {row["id"]: row for row in new.get(section, [])}
        added = sorted(set(new_rows) - set(old_rows))
        removed = sorted(set(old_rows) - set(new_rows))
        changed = sorted(row_id for row_id in set(old_rows) & set(new_rows) if old_rows[row_id] != new_rows[row_id])
        if added:
            issues.append(f"{section}: added {', '.join(added[:10])}")
        if removed:
            issues.append(f"{section}: removed {', '.join(removed[:10])}")
        if changed:
            issues.append(f"{section}: changed {', '.join(changed[:10])}")
    return issues


def check() -> int:
    if not JSON_PATH.exists():
        print(f"coverage-registry: missing {rel(JSON_PATH)}; run the generator", file=sys.stderr)
        return 1
    try:
        old = json.loads(read_text(JSON_PATH))
    except json.JSONDecodeError as exc:
        print(f"coverage-registry: invalid JSON in {rel(JSON_PATH)}: {exc}", file=sys.stderr)
        return 1

    new = generate()
    issues = registry_diff(old, new)

    expected_md = render_markdown(new)
    if not MD_PATH.exists():
        issues.append(f"missing {rel(MD_PATH)}")
    elif read_text(MD_PATH) != expected_md:
        issues.append(f"{rel(MD_PATH)} is stale (regenerate it)")

    if issues:
        print("coverage-registry: DRIFT detected", file=sys.stderr)
        for issue in issues:
            print(f"  - {issue}", file=sys.stderr)
        print(
            "Run `python3 go_hive/scripts/gen_coverage_registry.py` and commit both "
            "go_hive/docs/coverage-registry.json and go_hive/docs/coverage-registry.md.",
            file=sys.stderr,
        )
        return 1

    print(
        "coverage-registry: OK "
        f"({new['summary']['rows_total']} rows, {new['summary']['known_gaps']['total']} known gaps)"
    )
    return 0


def write() -> int:
    data = generate()
    JSON_PATH.parent.mkdir(parents=True, exist_ok=True)
    JSON_PATH.write_text(render_json(data), encoding="utf-8")
    MD_PATH.write_text(render_markdown(data), encoding="utf-8")
    print(f"coverage-registry: wrote {rel(JSON_PATH)} and {rel(MD_PATH)} ({data['summary']['rows_total']} rows)")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check",
        action="store_true",
        help="recompute and compare with the checked-in registry; exit 1 on drift",
    )
    args = parser.parse_args(argv)
    return check() if args.check else write()


if __name__ == "__main__":
    raise SystemExit(main())
