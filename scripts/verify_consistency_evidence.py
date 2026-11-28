#!/usr/bin/env python3
"""Validate the evidence tracked by go_hive/docs/consistency-matrix.json.

The consistency matrix answers three axes (static / contract / behavior) and
gives every claim a stable trace id.  This verifier answers the next question:
*is the evidence behind that id still resolvable and current?*

Checks
------
artifact            path exists, byte count and SHA-256 still match
package             the package-parity selector ``pairs[py=<name>]`` resolves
c12-module          ``python_path`` exists; ``go_counterpart`` resolves if set
contract            the source artifact exists and the row's refs resolve
behavior-registry   the source artifact exists
rows                every ``evidence_refs`` entry points at a catalog id
inputs.manifest     path / bytes / sha256 match the artifact catalog
c12 totals          status counts sum to the recorded total
codegraph (opt-in)  index is present and key symbols resolve on both sides

Usage
-----
    uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py
    uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py --write
    uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py --check
    uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py --codegraph --write

Exit codes: 0 = ok, 1 = drift (missing/stale evidence), 2 = cannot run.
"""

from __future__ import annotations

import argparse
import datetime as _dt
import hashlib
import html
import json
import shutil
import subprocess
import sys
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any

SCRIPT = Path(__file__).resolve()
ROOT = SCRIPT.parents[2]
DOCS = ROOT / "go_hive" / "docs"
MATRIX = DOCS / "consistency-matrix.json"
REPORT_JSON = DOCS / "consistency-evidence-report.json"
REPORT_MD = DOCS / "consistency-evidence-report.md"
REPORT_HTML = DOCS / "consistency-evidence-report.html"

# Artifact id -> how to regenerate it.  Kept here rather than in the matrix so
# the matrix stays deterministic (a timestamp in the generated JSON would make
# --check fail on every run).
PRODUCERS: dict[str, dict[str, str]] = {
    "artifact:package-parity": {
        "producer": "go_hive/scripts/gen_package_parity.py",
        "reproduce": "uv run --no-project python3 go_hive/scripts/gen_package_parity.py",
    },
    "artifact:function-parity": {
        "producer": "go_hive/scripts/gen_function_parity.py",
        "reproduce": "uv run --no-project python3 go_hive/scripts/gen_function_parity.py",
    },
    "artifact:c12-module-inventory": {
        "producer": "go_hive/go-hive/Makefile::module-inventory-check",
        "reproduce": "make -C go_hive/go-hive module-inventory-check",
    },
    "artifact:api-routes": {
        "producer": "go_hive/scripts/export_api_routes.py",
        "reproduce": "uv run --no-project python3 go_hive/scripts/export_api_routes.py",
    },
    "artifact:cli-surface": {
        "producer": "go_hive/scripts/cli_surface_contract.py",
        "reproduce": "make -C go_hive/go-hive cli-surface-check",
    },
    "artifact:env-vars": {
        "producer": "go_hive/scripts/freeze_env_config_contract.py",
        "reproduce": "uv run --no-project python3 go_hive/scripts/freeze_env_config_contract.py",
    },
    "artifact:config-keys": {
        "producer": "go_hive/scripts/freeze_env_config_contract.py",
        "reproduce": "uv run --no-project python3 go_hive/scripts/freeze_env_config_contract.py",
    },
    "artifact:tool-definitions": {
        "producer": "go_hive/scripts/freeze_tool_contract.py",
        "reproduce": "uv run --no-project python3 go_hive/scripts/freeze_tool_contract.py build",
    },
    "artifact:builtin-skills": {
        "producer": "go_hive/scripts/gen_builtin_skills_manifest.py",
        "reproduce": "uv run --no-project python3 go_hive/scripts/gen_builtin_skills_manifest.py",
    },
    "artifact:coverage-registry": {
        "producer": "go_hive/scripts/gen_coverage_registry.py",
        "reproduce": "uv run --no-project python3 go_hive/scripts/gen_coverage_registry.py",
    },
    "artifact:integration-coverage": {
        "producer": "go_hive/scripts/gen_package_parity.py",
        "reproduce": "uv run --no-project python3 go_hive/scripts/gen_package_parity.py",
    },
}

# Symbols the matrix uses as its structural anchors.  "expect" is "both" for a
# same-named Go counterpart, "python" for a deliberate Python-only area
# (graph-era Orchestrator).  Hints disambiguate generic names: the query can
# return a route method or an import before the real type.
CODEGRAPH_SYMBOLS: list[dict[str, str]] = [
    {"name": "AgentLoop", "expect": "both", "py": "core/framework/agent_loop/", "go": "internal/agentloop/"},
    {"name": "ToolRegistry", "expect": "both", "py": "core/framework/loader/tool_registry.py", "go": "internal/tools/registry.go"},
    {"name": "CredentialStore", "expect": "both", "py": "core/framework/credentials/store.py", "go": "internal/credentials/store.go"},
    {"name": "SessionManager", "expect": "both", "py": "core/framework/server/session_manager.py", "go": "internal/server/session.go"},
    {"name": "ColonyRuntime", "expect": "both", "py": "core/framework/host/colony_runtime.py", "go": "internal/colony/runtime.go"},
    {"name": "SkillsManager", "expect": "both", "py": "core/framework/skills/manager.py", "go": "internal/skills/manager.go"},
    {"name": "EventBus", "expect": "both", "py": "core/framework/host/event_bus.py", "go": "internal/eventbus/"},
    {"name": "ReminderHub", "expect": "both", "py": "core/framework/agent_loop/reminders.py", "go": "internal/agentloop/reminder_hub.go"},
    {"name": "MCPClient", "expect": "both", "py": "core/framework/loader/mcp_client.py", "go": "internal/tools/mcp.go"},
    {"name": "PlaybookRun", "expect": "both", "py": "core/framework/host/playbook/runner.py", "go": "internal/playbook/runner.go"},
    {"name": "TriggerDefinition", "expect": "both", "py": "core/framework/host/triggers.py", "go": "internal/triggers/"},
    {"name": "MemoryFile", "expect": "both", "py": "core/framework/agents/queen/queen_memory_v2.py", "go": "internal/memory/"},
    {"name": "Orchestrator", "expect": "python", "py": "core/framework/orchestrator/", "go": ""},
]

# Prefer a declared type over an accessor/import when a query returns both.
SYMBOL_KIND_RANK = {
    "class": 0,
    "struct": 0,
    "interface": 1,
    "type_alias": 1,
    "function": 2,
    "method": 3,
    "import": 9,
}


def pick_symbol(rows: list[dict[str, Any]], language: str, name: str, hint: str) -> dict[str, Any] | None:
    """Choose the best node for one (language, symbol) pair."""
    candidates: list[dict[str, Any]] = []
    for row in rows:
        node = row.get("node") or {}
        if node.get("language") != language:
            continue
        candidates.append(
            {
                "file": str(node.get("filePath") or ""),
                "line": node.get("startLine"),
                "kind": node.get("kind"),
                "name": node.get("name"),
            }
        )
    if not candidates:
        return None
    if hint:
        hinted = [c for c in candidates if hint in c["file"]]
        if hinted:
            candidates = hinted
    candidates.sort(
        key=lambda c: (
            0 if c.get("name") == name else 1,
            SYMBOL_KIND_RANK.get(str(c.get("kind")), 5),
            len(c["file"]),
        )
    )
    return candidates[0]


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def load_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def rel(path: Path) -> str:
    try:
        return str(path.relative_to(ROOT))
    except ValueError:
        return str(path)


def git_commit() -> str:
    try:
        return subprocess.check_output(
            ["git", "-C", str(ROOT), "rev-parse", "HEAD"], text=True, stderr=subprocess.DEVNULL
        ).strip()
    except Exception:
        return "?"


def strip_anchor(value: str) -> str:
    """Turn ``path:12`` / ``path::symbol`` / ``path # note`` into a path."""
    for sep in ("::", "#", " ("):
        if sep in value:
            value = value.split(sep, 1)[0]
    # Keep a Windows drive prefix (C:) intact; otherwise a trailing :digits is a line anchor.
    if ":" in value:
        head, tail = value.rsplit(":", 1)
        if tail.isdigit():
            value = head
    return value.strip().rstrip(",")


def resolve_path(value: str) -> Path | None:
    """Resolve a repo-relative path, tolerating internal/ shorthand."""
    raw = strip_anchor(value)
    if not raw:
        return None
    candidates = [ROOT / raw]
    if raw.startswith("internal/") or raw.startswith("cmd/"):
        candidates.append(ROOT / "go_hive" / "go-hive" / raw)
    for candidate in candidates:
        if candidate.exists():
            return candidate
    return candidates[0]


def check_artifact(entry: dict[str, Any]) -> list[str]:
    problems: list[str] = []
    path = ROOT / str(entry.get("path", ""))
    if not path.is_file():
        return [f"missing artifact: {entry.get('path')}"]
    expected_sha = entry.get("sha256")
    if expected_sha and sha256(path) != expected_sha:
        problems.append(f"sha256 changed: {entry.get('path')}")
    expected_bytes = entry.get("bytes")
    if isinstance(expected_bytes, int) and path.stat().st_size != expected_bytes:
        problems.append(f"bytes changed: {entry.get('path')}")
    return problems


def load_artifact(entry: dict[str, Any], artifacts: dict[str, dict[str, Any]]) -> Any:
    source = entry.get("source")
    if not source or source not in artifacts:
        return None
    path = ROOT / str(artifacts[source].get("path", ""))
    if not path.is_file():
        return None
    try:
        return load_json(path)
    except Exception:
        return None


def selector_resolves(data: Any, selector: str) -> bool:
    """Resolve the small selector dialect the matrix emits."""
    if not selector:
        return True
    if "[" in selector and selector.endswith("]"):
        head, tail = selector.split("[", 1)
        key, _, needle = tail[:-1].partition("=")
        needle = needle.strip().strip("'\"")
        value: Any = data
        for part in head.split("."):
            if part:
                if not isinstance(value, dict):
                    return False
                value = value.get(part)
        if isinstance(value, list):
            return any(
                isinstance(item, dict) and str(item.get(key)) == needle
                for item in value
            )
        if isinstance(value, dict):
            return needle in value
        return False
    return True


def check_entry(
    entry: dict[str, Any],
    artifacts: dict[str, dict[str, Any]],
) -> dict[str, Any]:
    kind = entry.get("kind")
    problems: list[str] = []
    if kind == "artifact":
        problems = check_artifact(entry)
    elif kind == "package":
        data = load_artifact(entry, artifacts)
        if data is None:
            problems.append(f"missing source artifact: {entry.get('source')}")
        elif not selector_resolves(data, str(entry.get("selector", ""))):
            problems.append(f"selector not found: {entry.get('selector')}")
    elif kind == "c12-module":
        py = entry.get("python_path")
        if py:
            resolved = resolve_path(str(py))
            if resolved is None or not resolved.exists():
                problems.append(f"missing python anchor: {py}")
        go = entry.get("go_counterpart")
        if go:
            resolved = resolve_path(str(go))
            if resolved is None or not resolved.exists():
                problems.append(f"missing go anchor: {go}")
    elif kind == "contract":
        source = str(entry.get("source") or "")
        # The generator stores the artifact id here; older matrices stored a path.
        if source in artifacts:
            path = ROOT / str(artifacts[source].get("path", ""))
        else:
            path = resolve_path(source)
        if path is None or not path.exists():
            problems.append(f"missing contract source: {source}")
    elif kind == "behavior-registry":
        if entry.get("source") not in artifacts:
            problems.append(f"missing behavior source: {entry.get('source')}")
    else:
        problems.append(f"unknown evidence kind: {kind}")

    return {
        "id": entry.get("id"),
        "kind": kind,
        "path": entry.get("path") or entry.get("python_path") or entry.get("source"),
        "status": "ok" if not problems else "drift",
        "problems": problems,
        "producer": (PRODUCERS.get(str(entry.get("id"))) or {}).get("producer"),
    }


def run_codegraph() -> dict[str, Any]:
    exe = shutil.which("codegraph")
    if not exe:
        return {"status": "skipped", "reason": "codegraph not on PATH"}
    result: dict[str, Any] = {"status": "ok", "symbols": [], "problems": []}
    try:
        raw = subprocess.check_output(
            [exe, "status", "--json"], text=True, timeout=30, stderr=subprocess.DEVNULL
        )
        status = json.loads(raw)
        result["index"] = {
            "version": status.get("version"),
            "lastIndexed": status.get("lastIndexed"),
            "fileCount": status.get("fileCount"),
            "nodeCount": status.get("nodeCount"),
            "edgeCount": status.get("edgeCount"),
            "pendingChanges": status.get("pendingChanges"),
            "state": (status.get("index") or {}).get("state"),
        }
    except Exception as exc:
        return {"status": "skipped", "reason": f"codegraph status failed: {exc}"}

    for spec in CODEGRAPH_SYMBOLS:
        name = spec["name"]
        expectation = spec.get("expect", "both")
        try:
            raw = subprocess.check_output(
                [exe, "query", name, "--json", "-l", "8"],
                text=True,
                timeout=30,
                stderr=subprocess.DEVNULL,
            )
            rows = json.loads(raw)
        except Exception as exc:
            result["problems"].append(f"{name}: query failed: {exc}")
            continue
        python_node = pick_symbol(rows, "python", name, spec.get("py", ""))
        go_node = pick_symbol(rows, "go", name, spec.get("go", ""))
        ok = True
        if expectation in ("both", "python") and python_node is None:
            ok = False
            result["problems"].append(f"{name}: python anchor missing")
        if expectation == "both" and go_node is None:
            ok = False
            result["problems"].append(f"{name}: go anchor missing")
        result["symbols"].append(
            {
                "name": name,
                "expectation": expectation,
                "status": "ok" if ok else "drift",
                "python": python_node,
                "go": go_node,
            }
        )
    if result["problems"]:
        result["status"] = "drift"
    return result


def build_report(*, with_codegraph: bool) -> dict[str, Any]:
    if not MATRIX.is_file():
        raise RuntimeError(
            f"matrix missing: {rel(MATRIX)} (run gen_consistency_matrix.py first)"
        )
    matrix = load_json(MATRIX)
    entries: list[dict[str, Any]] = list(matrix.get("evidence_catalog") or [])
    artifacts = {e["id"]: e for e in entries if e.get("kind") == "artifact" and e.get("id")}

    evidence: list[dict[str, Any]] = []
    problems: list[str] = []
    seen_ids: set[str] = set()
    for entry in entries:
        eid = str(entry.get("id") or "")
        if not eid:
            problems.append("evidence entry without id")
            continue
        if eid in seen_ids:
            problems.append(f"duplicate evidence id: {eid}")
        seen_ids.add(eid)
        record = check_entry(entry, artifacts)
        evidence.append(record)
        problems.extend(record["problems"])

    # Row-level traceability: every ref must resolve to a catalog id.  A ref may
    # carry a "#selector" fragment; the base id is what the catalog defines.
    dangling: list[dict[str, str]] = []
    rows: list[dict[str, Any]] = []
    for row in matrix.get("packages") or []:
        row_problems = []
        for ref in row.get("evidence_refs") or []:
            base = str(ref).split("#", 1)[0]
            if base not in seen_ids:
                dangling.append({"row": row.get("trace_id"), "ref": ref})
                row_problems.append(f"dangling ref: {ref}")
        rows.append({"trace_id": row.get("trace_id"), "status": "drift" if row_problems else "ok"})
    for row in matrix.get("contracts") or []:
        row_problems = []
        for ref in row.get("evidence_refs") or []:
            base = str(ref).split("#", 1)[0]
            if base not in seen_ids:
                dangling.append({"row": row.get("trace_id"), "ref": ref})
                row_problems.append(f"dangling ref: {ref}")
        rows.append({"trace_id": row.get("trace_id"), "status": "drift" if row_problems else "ok"})
    behavior = matrix.get("behavior") or {}
    behavior_problems = []
    for ref in behavior.get("evidence_refs") or []:
        base = str(ref).split("#", 1)[0]
        if base not in seen_ids:
            dangling.append({"row": behavior.get("trace_id"), "ref": ref})
            behavior_problems.append(f"dangling ref: {ref}")
    rows.append({"trace_id": behavior.get("trace_id"), "status": "drift" if behavior_problems else "ok"})

    # inputs.manifest must mirror the artifact catalog exactly.
    manifest = ((matrix.get("inputs") or {}).get("manifest") or [])
    for item in manifest:
        eid = str(item.get("id") or "")
        art = artifacts.get(eid)
        if art is None:
            problems.append(f"manifest id missing from catalog: {eid}")
            continue
        if item.get("path") != art.get("path"):
            problems.append(f"manifest path mismatch: {eid}")
        if item.get("sha256") != art.get("sha256"):
            problems.append(f"manifest sha mismatch: {eid}")

    # C12 totals must agree with the status map.
    c12 = (matrix.get("totals") or {}).get("c12_by_status") or {}
    c12_total = (matrix.get("totals") or {}).get("c12_total")
    if c12_total is not None and sum(int(v) for v in c12.values()) != int(c12_total):
        problems.append("C12 total does not match status counts")

    status_counts = Counter(e["status"] for e in evidence)
    kind_status = defaultdict(Counter)
    for e in evidence:
        kind_status[str(e["kind"])][e["status"]] += 1
    row_drift = [r for r in rows if r["status"] != "ok"]

    codegraph = run_codegraph() if with_codegraph else {"status": "not_requested"}

    failures = list(problems)
    failures.extend(f"{e['id']}: {p}" for e in evidence for p in e["problems"])
    failures.extend(f"{r['row']}: dangling ref {r['ref']}" for r in dangling)
    failures.extend(codegraph.get("problems") or [])
    overall = "ok" if not failures else "drift"

    return {
        "schema_version": 1,
        "generated_at": _dt.datetime.now(_dt.UTC).isoformat(),
        "git_commit": git_commit(),
        "matrix": {
            "path": rel(MATRIX),
            "sha256": sha256(MATRIX),
            "generated_at_utc": (MATRIX.stat().st_mtime and _dt.datetime.fromtimestamp(
                MATRIX.stat().st_mtime, _dt.UTC
            ).isoformat()),
        },
        "status": overall,
        "totals": {
            "evidence": len(evidence),
            "by_status": dict(sorted(status_counts.items())),
            "by_kind": {k: dict(sorted(v.items())) for k, v in sorted(kind_status.items())},
            "rows": len(rows),
            "row_drift": len(row_drift),
            "dangling_refs": len(dangling),
        },
        "failures": failures,
        "rows": rows,
        "evidence": evidence,
        "codegraph": codegraph,
    }


def render_markdown(report: dict[str, Any]) -> str:
    t = report["totals"]
    lines = [
        "# Consistency evidence report",
        "",
        f"- generated: `{report['generated_at']}`",
        f"- commit: `{report['git_commit']}`",
        f"- matrix: `{report['matrix']['path']}` (`{report['matrix']['sha256']}`)",
        f"- status: **{report['status'].upper()}**",
        "",
        "## Totals",
        "",
        "| metric | value |",
        "| --- | ---: |",
        f"| evidence entries | {t['evidence']} |",
        f"| rows | {t['rows']} |",
        f"| row drift | {t['row_drift']} |",
        f"| dangling refs | {t['dangling_refs']} |",
    ]
    for status, count in t["by_status"].items():
        lines.append(f"| evidence {status} | {count} |")
    lines += ["", "## Evidence by kind", "", "| kind | ok | drift |", "| --- | ---: | ---: |"]
    for kind, counts in t["by_kind"].items():
        lines.append(f"| {kind} | {counts.get('ok', 0)} | {counts.get('drift', 0)} |")
    if report["failures"]:
        lines += ["", "## Failures", ""]
        lines.extend(f"- {item}" for item in report["failures"][:200])
        if len(report["failures"]) > 200:
            lines.append(f"- … and {len(report['failures']) - 200} more (see JSON)")
    cg = report.get("codegraph") or {}
    if cg.get("status") == "ok":
        idx = cg.get("index") or {}
        lines += [
            "",
            "## CodeGraph anchors",
            "",
            f"Index: {idx.get('fileCount')} files / {idx.get('nodeCount')} nodes / "
            f"{idx.get('edgeCount')} edges · state `{idx.get('state')}`",
            "",
            "| symbol | expectation | Python | Go | status |",
            "| --- | --- | --- | --- | --- |",
        ]
        for row in cg.get("symbols") or []:
            py = row.get("python") or {}
            go = row.get("go") or {}
            py_s = f"{py.get('file')}:{py.get('line')}" if py else "—"
            go_s = f"{go.get('file')}:{go.get('line')}" if go else "—"
            lines.append(f"| `{row['name']}` | {row['expectation']} | {py_s} | {go_s} | {row['status']} |")
    elif cg.get("status") == "skipped":
        lines += ["", f"## CodeGraph anchors", "", f"Skipped: {cg.get('reason')}"]
    lines += [
        "",
        "## Reproduce",
        "",
        "```bash",
        "uv run --no-project python3 go_hive/scripts/gen_consistency_matrix.py",
        "uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py --write",
        "uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py --codegraph --check",
        "```",
        "",
    ]
    return "\n".join(lines)


def self_test() -> int:
    """Prove the verifier itself catches missing/stale/ambiguous evidence."""
    checks: list[tuple[str, bool]] = []

    missing = check_entry({"kind": "artifact", "path": "__missing__"}, {})
    checks.append(("missing artifact", missing["status"] == "drift"))

    real = DOCS / "package-parity.json"
    bad_sha = check_entry(
        {
            "kind": "artifact",
            "path": rel(real),
            "sha256": "0" * 64,
            "bytes": real.stat().st_size if real.is_file() else 0,
        },
        {},
    )
    checks.append(("sha mismatch", bad_sha["status"] == "drift"))

    checks.append(("selector hit", selector_resolves({"pairs": [{"py": "x"}]}, "pairs[py=x]")))
    checks.append(("selector miss", not selector_resolves({"pairs": [{"py": "x"}]}, "pairs[py=y]")))

    rows = [
        {"node": {"language": "go", "kind": "import", "name": "X", "filePath": "hive/x.go", "startLine": 1}},
        {"node": {"language": "go", "kind": "struct", "name": "X", "filePath": "internal/x/x.go", "startLine": 2}},
    ]
    picked = pick_symbol(rows, "go", "X", "internal/x/")
    checks.append(("kind/path preference", bool(picked) and picked["kind"] == "struct"))

    failed = [name for name, ok in checks if not ok]
    if failed:
        print(f"self-test: FAIL — {', '.join(failed)}", file=sys.stderr)
        return 1
    print(f"self-test: OK — {len(checks)} checks")
    return 0


def render_html(report: dict[str, Any]) -> str:
    """Small self-contained report page (no Markdown dependency)."""
    t = report["totals"]
    esc = lambda v: html.escape("" if v is None else str(v))
    totals_rows = "".join(
        f"<tr><td>{esc(k)}</td><td class='num'>{esc(v)}</td></tr>"
        for k, v in {
            "evidence entries": t["evidence"],
            "rows": t["rows"],
            "row drift": t["row_drift"],
            "dangling refs": t["dangling_refs"],
            **{f"evidence {k}": v for k, v in t["by_status"].items()},
        }.items()
    )
    kind_rows = "".join(
        f"<tr><td>{esc(kind)}</td><td class='num'>{esc(counts.get('ok', 0))}</td>"
        f"<td class='num'>{esc(counts.get('drift', 0))}</td></tr>"
        for kind, counts in t["by_kind"].items()
    )
    failures = "".join(f"<li>{esc(x)}</li>" for x in report["failures"][:200])
    if len(report["failures"]) > 200:
        failures += f"<li>… and {len(report['failures']) - 200} more (see JSON)</li>"
    cg = report.get("codegraph") or {}
    if cg.get("status") == "ok":
        idx = cg.get("index") or {}
        cg_head = (
            f"<p>Index: {esc(idx.get('fileCount'))} files / {esc(idx.get('nodeCount'))} nodes / "
            f"{esc(idx.get('edgeCount'))} edges · state <code>{esc(idx.get('state'))}</code></p>"
        )
        cg_rows = "".join(
            "<tr>"
            f"<td><code>{esc(s['name'])}</code></td><td>{esc(s['expectation'])}</td>"
            f"<td class='small'>{esc((s.get('python') or {}).get('file') or '—')}:{esc((s.get('python') or {}).get('line') or '—')}</td>"
            f"<td class='small'>{esc((s.get('go') or {}).get('file') or '—')}:{esc((s.get('go') or {}).get('line') or '—')}</td>"
            f"<td>{esc(s['status'])}</td></tr>"
            for s in cg.get("symbols") or []
        )
        cg_html = (
            f"<h2>CodeGraph anchors</h2>{cg_head}"
            "<table><thead><tr><th>symbol</th><th>expectation</th><th>Python</th><th>Go</th><th>status</th></tr></thead>"
            f"<tbody>{cg_rows}</tbody></table>"
        )
    elif cg.get("status") == "skipped":
        cg_html = f"<h2>CodeGraph anchors</h2><p>Skipped: {esc(cg.get('reason'))}</p>"
    else:
        cg_html = ""
    return f"""<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Consistency evidence report</title>
<style>
:root{{--bg:#f7f7f5;--fg:#1f2328;--muted:#656d76;--line:#d8dee4;--card:#fff;--code:#f1f3f5;--ok:#1a7f37;--bad:#cf222e}}
@media(prefers-color-scheme:dark){{:root{{--bg:#0f1216;--fg:#e6edf3;--muted:#9aa4af;--line:#2b323a;--card:#161b22;--code:#1b2027;--ok:#3fb950;--bad:#f85149}}}}
*{{box-sizing:border-box}}body{{margin:0;background:var(--bg);color:var(--fg);font:15px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",sans-serif}}
.wrap{{max-width:1100px;margin:0 auto;padding:40px 24px 80px}}h1{{font-size:28px;margin:0 0 6px}}h2{{font-size:20px;margin:36px 0 10px;border-bottom:1px solid var(--line);padding-bottom:7px}}
p,li{{color:var(--muted)}}code{{font-family:ui-monospace,Menlo,Consolas,monospace;background:var(--code);padding:.1em .35em;border-radius:4px;font-size:.88em}}
table{{width:100%;border-collapse:collapse;margin:14px 0;background:var(--card);border:1px solid var(--line);border-radius:8px;overflow:hidden;font-size:13.5px}}
th,td{{text-align:left;padding:8px 10px;border-bottom:1px solid var(--line)}}th{{background:var(--code);font-size:12px}}.num{{text-align:right}}
.small{{font-size:12px;color:var(--muted)}}.ok{{color:var(--ok)}}.bad{{color:var(--bad)}}pre{{background:var(--code);border:1px solid var(--line);border-radius:8px;padding:14px;overflow:auto}}
</style></head><body><div class="wrap">
<h1>Consistency evidence report</h1>
<p>generated <code>{esc(report['generated_at'])}</code> · commit <code>{esc(report['git_commit'])}</code><br>
matrix <code>{esc(report['matrix']['path'])}</code> · sha256 <code>{esc(report['matrix']['sha256'])}</code><br>
status <strong class="{'ok' if report['status']=='ok' else 'bad'}">{esc(report['status'].upper())}</strong></p>
<h2>Totals</h2><table><tbody>{totals_rows}</tbody></table>
<h2>Evidence by kind</h2><table><thead><tr><th>kind</th><th>ok</th><th>drift</th></tr></thead><tbody>{kind_rows}</tbody></table>
<h2>Failures</h2>{'<ul>' + failures + '</ul>' if failures else '<p>None.</p>'}
{cg_html}
<h2>Reproduce</h2><pre>uv run --no-project python3 go_hive/scripts/gen_consistency_matrix.py
uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py --write
uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py --codegraph --check</pre>
</div></body></html>"""


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="exit 1 when evidence is missing/stale")
    parser.add_argument("--write", action="store_true", help="write JSON + Markdown report")
    parser.add_argument("--json", action="store_true", help="print the report as JSON")
    parser.add_argument("--codegraph", action="store_true", help="also validate CodeGraph symbol anchors")
    parser.add_argument("--self-test", action="store_true", help="test the verifier's detection logic")
    args = parser.parse_args(argv)

    if args.self_test:
        return self_test()

    try:
        report = build_report(with_codegraph=args.codegraph)
    except SystemExit:
        raise
    except Exception as exc:
        print(f"consistency-evidence: ERROR: {exc}", file=sys.stderr)
        return 2

    markdown = render_markdown(report)
    page = render_html(report)
    if args.write:
        REPORT_JSON.write_text(
            json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
        REPORT_MD.write_text(markdown, encoding="utf-8")
        REPORT_HTML.write_text(page, encoding="utf-8")
        print(f"wrote {rel(REPORT_JSON)}, {rel(REPORT_MD)}, {rel(REPORT_HTML)}")
    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        t = report["totals"]
        print(
            f"consistency-evidence: {report['status'].upper()} — "
            f"{t['evidence']} entries ({t['by_status']}), {t['rows']} rows, "
            f"{t['dangling_refs']} dangling refs"
        )
        for item in report["failures"][:20]:
            print(f"  - {item}")
        if len(report["failures"]) > 20:
            print(f"  … and {len(report['failures']) - 20} more")
        if report["codegraph"].get("status") == "ok":
            cg = report["codegraph"]
            print(
                f"  codegraph: {len(cg.get('symbols') or [])} symbols checked, "
                f"{sum(1 for s in cg.get('symbols') or [] if s['status'] == 'ok')} ok"
            )
        elif report["codegraph"].get("status") == "skipped":
            print(f"  codegraph: skipped ({report['codegraph'].get('reason')})")

    if args.check and report["status"] != "ok":
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
