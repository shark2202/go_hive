#!/usr/bin/env python3
"""Build the current go_hive ↔ Hive consistency matrix.

Three axes are kept separate:
  static   — source-package shape and AST-derived function inventory;
  contract — frozen API / CLI / env / config / tool / skill surfaces;
  behavior — C12 registry, event coverage, known gaps, and test references.

The matrix is an evidence index, not proof of semantic equivalence.  A package
with an entry in this table still needs its listed contracts and behavior rows
checked before it can be called equivalent.

Usage:
  uv run --no-project python3 go_hive/scripts/gen_consistency_matrix.py
  uv run --no-project python3 go_hive/scripts/gen_consistency_matrix.py --check
"""

from __future__ import annotations

import argparse
import hashlib
import html
import json
import sys
from collections import Counter
from pathlib import Path
from typing import Any

SCRIPT = Path(__file__).resolve()
ROOT = SCRIPT.parents[2]
DOCS = ROOT / "go_hive" / "docs"
CONTRACTS = ROOT / "go_hive" / "contracts"
PARITY = DOCS / "package-parity.json"
FUNCTIONS = DOCS / "function-parity-matrix.json"
MODULES = ROOT / "contracts" / "module-inventory.json"
API = CONTRACTS / "api-routes.json"
CLI = CONTRACTS / "cli-surface.json"
ENV = CONTRACTS / "env-vars.json"
CONFIG = CONTRACTS / "config-keys.json"
TOOLS = CONTRACTS / "tool-definitions.json"
SKILLS = CONTRACTS / "builtin-skills.json"
COVERAGE = DOCS / "coverage-registry.json"
INTEGRATION = DOCS / "integration-coverage.json"

OUT_JSON = DOCS / "consistency-matrix.json"
OUT_HTML = DOCS / "consistency-matrix.html"


def load(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def count_cli_paths(side: dict[str, Any]) -> int:
    commands = side.get("commands")
    return len(commands) if isinstance(commands, dict) else 0


def pct(part: int, total: int) -> float | None:
    return round(100.0 * part / total, 1) if total else None


def build() -> dict[str, Any]:
    parity = load(PARITY)
    functions = load(FUNCTIONS)
    modules = load(MODULES)
    api = load(API)
    cli = load(CLI)
    env = load(ENV)
    config = load(CONFIG)
    tools = load(TOOLS)
    skills = load(SKILLS)
    coverage = load(COVERAGE)
    integration = load(INTEGRATION)

    module_rows = modules.get("files") or modules.get("modules") or []
    module_status = Counter(m.get("status") for m in module_rows)
    modules_by_domain: dict[str, list[dict[str, Any]]] = {}
    for m in module_rows:
        modules_by_domain.setdefault(m.get("domain"), []).append(m)
    fn_by_pkg = {p["python_package"]: p for p in functions.get("packages", [])}
    all_pairs = parity.get("pairs", [])

    package_rows: list[dict[str, Any]] = []
    for pair in all_pairs:
        f = fn_by_pkg.get(pair.get("py"), {})
        go_totals = pair.get("go_totals") or {}
        registry = pair.get("registry") or {}
        dims = pair.get("dims") or []
        c12 = pair.get("c12") or {}
        modules_for_pkg = modules_by_domain.get(pair.get("py"), [])
        go_packages = []
        for g in pair.get("go") or []:
            go_packages.append({
                "name": g.get("name"),
                "package": g.get("package"),
                "files": g.get("files"),
                "importers": g.get("importers"),
                "loc": g.get("loc"),
                "parity_refs": g.get("parity_refs"),
            })
        package_rows.append({
            "python_package": pair.get("py"),
            "go_packages": go_packages,
            "verdict": pair.get("verdict"),
            "basis": pair.get("basis"),
            "trace_id": f"pkg:{pair.get('py')}",
            "evidence_refs": [
                f"artifact:package-parity#pair:{pair.get('py')}",
                f"artifact:function-parity#package:{pair.get('py')}",
                *[f"c12:{m.get('python_path')}" for m in modules_for_pkg],
            ],
            "c12_modules": [
                {
                    "python_path": m.get("python_path"),
                    "status": m.get("status"),
                    "go_counterpart": m.get("go_counterpart"),
                    "basis": m.get("basis"),
                    "evidence": m.get("evidence"),
                }
                for m in modules_for_pkg
            ],
            "static": {
                "python_files": pair.get("py_files"),
                "python_loc": pair.get("py_loc"),
                "python_test_files": pair.get("py_test_files"),
                "go_files": go_totals.get("files"),
                "go_loc": go_totals.get("loc"),
                "go_parity_refs": go_totals.get("parity_refs"),
                "go_test_funcs": go_totals.get("test_funcs"),
                "python_functions": f.get("python_functions"),
                "go_functions": f.get("go_functions"),
                "python_ast_matched": f.get("python_matched"),
                "python_ast_coverage_pct": f.get("python_coverage_pct"),
                "go_ast_coverage_pct": f.get("go_coverage_pct"),
                "python_annotation_matches": f.get("python_annotation_matches"),
                "python_name_matches": f.get("python_name_matches"),
            },
            "contract": {
                "c12": c12,
                "dimensions": dims,
                "partial_dimensions": sum(1 for d in dims if d.get("status") not in ("aligned", "ok")),
                "registry_rows": registry.get("rows"),
                "registry_bad_rows": registry.get("bad_rows"),
                "registry_bad_ids": registry.get("bad_ids", []),
                "open_gaps": pair.get("open_gaps", []),
            },
            "behavior": {
                "c12": c12,
                "go_test_funcs": go_totals.get("test_funcs"),
                "go_parity_refs": go_totals.get("parity_refs"),
                "registry_rows": registry.get("rows"),
                "registry_bad_rows": registry.get("bad_rows"),
            },
        })

    api_sum = api.get("summary") or {}
    env_sum = env.get("summary") or {}
    cfg_sum = config.get("summary") or {}
    tool_stats = tools.get("statistics") or {}
    cli_totals = (cli.get("diff") or {}).get("totals") or {}
    cli_hive = ((cli.get("clis") or {}).get("hive") or {})
    cli_browser = ((cli.get("clis") or {}).get("hive-browser") or {})
    cov_sum = coverage.get("summary") or {}
    ev = cov_sum.get("events") or {}
    test_map = cov_sum.get("test_mapping") or {}
    fn_totals = functions.get("totals") or {}
    pp_sum = parity.get("summary") or {}

    contract_rows = [
        {
            "id": "api",
            "label": "HTTP API routes",
            "reference": api_sum.get("reference_routes"),
            "target": api_sum.get("target_routes"),
            "status": "aligned_with_extra" if not api_sum.get("missing_in_target_effective") and not api_sum.get("method_mismatch") else "partial",
            "detail": f"effective missing {api_sum.get('missing_in_target_effective')} · extra {api_sum.get('extra_in_target')} · method mismatch {api_sum.get('method_mismatch')} · alias-served {api_sum.get('missing_in_target_served_by_alias')}",
            "source": "go_hive/contracts/api-routes.json",
        },
        {
            "id": "cli",
            "label": "CLI surface",
            "reference": count_cli_paths((cli_hive.get("python") or {})) + count_cli_paths((cli_browser.get("python") or {})),
            "target": count_cli_paths((cli_hive.get("go") or {})) + count_cli_paths((cli_browser.get("go") or {})),
            "status": "aligned" if not cli_totals.get("probe_drift_total") and not cli_totals.get("text_diff_total") and not cli_totals.get("subcommand_mismatch") else "partial",
            "detail": f"probe drift {cli_totals.get('probe_drift_total', 0)} · text diff {cli_totals.get('text_diff_total', 0)} · subcommand mismatch {cli_totals.get('subcommand_mismatch', 'n/a')}",
            "source": "go_hive/contracts/cli-surface.json",
        },
        {
            "id": "env",
            "label": "Environment variables",
            "reference": env_sum.get("python"),
            "target": env_sum.get("go"),
            "status": "partial" if env_sum.get("python_only") or env_sum.get("go_only") or env_sum.get("status_counts", {}).get("parse_differs") or env_sum.get("status_counts", {}).get("default_and_parse_differ") else "aligned",
            "detail": f"common {env_sum.get('common')} · py-only {env_sum.get('python_only')} · go-only {env_sum.get('go_only')} · status {env_sum.get('status_counts')}",
            "source": "go_hive/contracts/env-vars.json",
        },
        {
            "id": "config",
            "label": "configuration.json keys",
            "reference": cfg_sum.get("keys"),
            "target": cfg_sum.get("keys"),
            "status": "partial" if any(k != "aligned" for k in (cfg_sum.get("status_counts") or {})) else "aligned",
            "detail": f"{cfg_sum.get('sections')} sections · status {cfg_sum.get('status_counts')} · boundary rules {cfg_sum.get('boundary_rules')}",
            "source": "go_hive/contracts/config-keys.json",
        },
        {
            "id": "tools",
            "label": "Tool definitions",
            "reference": tool_stats.get("python_tools"),
            "target": tool_stats.get("go_tools"),
            "status": "partial" if tool_stats.get("python_only") or tool_stats.get("go_only") or tool_stats.get("schema_different") else "aligned",
            "detail": f"shared {tool_stats.get('shared')} · py-only {tool_stats.get('python_only')} · go-only {tool_stats.get('go_only')} · desc different {tool_stats.get('description_different')} · schema different {tool_stats.get('schema_different')}",
            "source": "go_hive/contracts/tool-definitions.json",
        },
        {
            "id": "skills",
            "label": "Built-in skills",
            "reference": skills.get("count"),
            "target": skills.get("count"),
            "status": "aligned" if skills.get("count") else "unknown",
            "detail": f"{skills.get('count')} files · {skills.get('total_bytes')} bytes · source {skills.get('source')}",
            "source": "go_hive/contracts/builtin-skills.json",
        },
        {
            "id": "events",
            "label": "Event surface",
            "reference": ev.get("python"),
            "target": ev.get("go"),
            "status": "partial" if ev.get("partial") or ev.get("emitter_missing") or ev.get("frontend_missing") else "aligned",
            "detail": f"ported {ev.get('ported')} · partial {ev.get('partial')} · emitter missing {ev.get('emitter_missing')} · frontend missing {ev.get('frontend_missing')}",
            "source": "go_hive/docs/coverage-registry.json",
        },
        {
            "id": "c12",
            "label": "C12 module ledger",
            "reference": sum(module_status.values()),
            "target": sum(module_status.values()),
            "status": "partial" if module_status.get("partial") or module_status.get("unported") else "aligned",
            "detail": " · ".join(f"{k} {v}" for k, v in sorted(module_status.items())),
            "source": "contracts/module-inventory.json",
        },
    ]

    input_paths = [PARITY, FUNCTIONS, MODULES, API, CLI, ENV, CONFIG, TOOLS, SKILLS, COVERAGE, INTEGRATION]
    artifact_ids = {
        PARITY: "artifact:package-parity",
        FUNCTIONS: "artifact:function-parity",
        MODULES: "artifact:c12-module-inventory",
        API: "artifact:api-routes",
        CLI: "artifact:cli-surface",
        ENV: "artifact:env-vars",
        CONFIG: "artifact:config-keys",
        TOOLS: "artifact:tool-definitions",
        SKILLS: "artifact:builtin-skills",
        COVERAGE: "artifact:coverage-registry",
        INTEGRATION: "artifact:integration-coverage",
    }
    source_artifact = {str(p.relative_to(ROOT)): artifact_ids[p] for p in input_paths}
    for r in contract_rows:
        r["trace_id"] = f"contract:{r['id']}"
        r["evidence_refs"] = [source_artifact.get(r["source"], f"artifact:{r['id']}")]

    evidence_catalog: list[dict[str, Any]] = []
    for p in input_paths:
        evidence_catalog.append({
            "id": artifact_ids[p],
            "kind": "artifact",
            "path": str(p.relative_to(ROOT)),
            "sha256": sha256(p),
            "bytes": p.stat().st_size,
        })
    for pair in all_pairs:
        evidence_catalog.append({
            "id": f"pkg:{pair.get('py')}",
            "kind": "package",
            "source": "artifact:package-parity",
            "selector": f"pairs[py={pair.get('py')}]",
        })
    for m in module_rows:
        evidence_catalog.append({
            "id": f"c12:{m.get('python_path')}",
            "kind": "c12-module",
            "source": "artifact:c12-module-inventory",
            "selector": f"files[python_path={m.get('python_path')}]",
            "status": m.get("status"),
            "go_counterpart": m.get("go_counterpart"),
        })
    for r in contract_rows:
        evidence_catalog.append({
            "id": r["trace_id"],
            "kind": "contract",
            "source": r["evidence_refs"][0],
            "selector": r["source"],
            "claimed_status": r["status"],
        })
    for bid, label in (
        ("behavior:c12", "C12 status counts"),
        ("behavior:events", "event coverage counts"),
        ("behavior:known-gaps", "known gaps"),
        ("behavior:test-mapping", "test mapping counts"),
    ):
        evidence_catalog.append({
            "id": bid,
            "kind": "behavior-registry",
            "source": "artifact:c12-module-inventory" if bid == "behavior:c12" else "artifact:coverage-registry",
            "selector": label,
        })

    return {
        "schema_version": 1,
        "scope": {
            "python": "core/framework (package ledger + non-test functions)",
            "go": "go_hive/go-hive/internal (package ledger + non-test functions)",
            "axes": {
                "static": "package/file/function shape; AST function matching is evidence, not equivalence proof",
                "contract": "frozen API/CLI/env/config/tool/skill/event surfaces; counts are compared, not runtime behavior",
                "behavior": "C12 registry, event coverage, known gaps and parity references; execution is not re-run by this generator",
            },
        },
        "inputs": {
            "manifest": [
                {
                    "id": artifact_ids[p],
                    "path": str(p.relative_to(ROOT)),
                    "sha256": sha256(p),
                    "bytes": p.stat().st_size,
                }
                for p in input_paths
            ],
            "package_parity_baseline": pp_sum.get("c12_baseline"),
            "function_matrix_totals": fn_totals,
        },
        "totals": {
            "python_packages": len(all_pairs),
            "go_packages": pp_sum.get("go_packages"),
            "python_files": pp_sum.get("python_files"),
            "python_loc": pp_sum.get("python_non_test_loc"),
            "go_files": pp_sum.get("go_files"),
            "go_loc": pp_sum.get("go_loc"),
            "python_functions": fn_totals.get("python_functions"),
            "go_functions": fn_totals.get("go_functions"),
            "python_ast_matched": fn_totals.get("python_matched"),
            "go_ast_matched": fn_totals.get("go_matched"),
            "python_ast_coverage_pct": fn_totals.get("python_coverage_pct"),
            "go_ast_coverage_pct": fn_totals.get("go_coverage_pct"),
            "c12_total": sum(module_status.values()),
            "c12_by_status": dict(sorted(module_status.items())),
            "package_verdicts": pp_sum.get("verdicts"),
            "contracts": {r["id"]: r["status"] for r in contract_rows},
            "behavior": {
                "events": ev,
                "known_gaps": cov_sum.get("known_gaps"),
                "phase_gates": cov_sum.get("phase_gates"),
                "synthetic_dispatch": cov_sum.get("synthetic_dispatch"),
                "test_mapping": test_map,
            },
        },
        "packages": package_rows,
        "contracts": contract_rows,
        "behavior": {
            "trace_id": "behavior:test-mapping",
            "evidence_refs": [
                "behavior:c12",
                "behavior:events",
                "behavior:known-gaps",
                "behavior:test-mapping",
            ],
            "c12_by_status": dict(sorted(module_status.items())),
            "events": ev,
            "known_gaps": cov_sum.get("known_gaps"),
            "phase_gates": cov_sum.get("phase_gates"),
            "synthetic_dispatch": cov_sum.get("synthetic_dispatch"),
            "test_mapping": test_map,
            "coverage_registry_known_gaps": coverage.get("known_gaps", []),
            "integration_scope": {
                "status": integration.get("status"),
                "counts": integration.get("counts"),
                "warning": integration.get("warning"),
            },
        },
        "gaps": {
            "package_parity": parity.get("gaps", []),
            "coverage_registry": coverage.get("known_gaps", []),
            "integration": integration.get("warning"),
        },
        "evidence_catalog": evidence_catalog,
        "reproduction": [
            "uv run --no-project python3 go_hive/scripts/gen_function_parity.py --check",
            "uv run --no-project python3 go_hive/scripts/gen_consistency_matrix.py --check",
            "make -C go_hive/go-hive module-inventory-check",
        ],
        "boundary": "This matrix aligns structure and frozen contracts. It does not assert behavioral equivalence for any package; use C12 rows, the coverage registry, and executed tests for that claim.",
    }


def html_escape(v: Any) -> str:
    return html.escape("" if v is None else str(v))


def render(matrix: dict[str, Any]) -> str:
    t = matrix["totals"]
    rows = []
    for p in matrix["packages"]:
        s = p["static"]
        c = p["contract"]
        b = p["behavior"]
        go_pkgs = ", ".join(
            f"<code>{html_escape(g.get('name'))}</code>"
            for g in p["go_packages"]
        ) or "—"
        dims_partial = c.get("partial_dimensions", 0)
        dims = c.get("dimensions") or []
        dim_text = ", ".join(f"{html_escape(d.get('id'))}:{html_escape(d.get('status'))}" for d in dims) or "—"
        c12 = c.get("c12") or {}
        c12_text = f"{c12.get('ported', 0)}/{c12.get('partial', 0)}/{c12.get('out_of_scope', 0)}"
        reg = c.get("registry_rows")
        bad = c.get("registry_bad_rows")
        ast_py = "—" if s.get("python_ast_coverage_pct") is None else f"{s['python_ast_coverage_pct']:.1f}%"
        ast_go = "—" if s.get("go_ast_coverage_pct") is None else f"{s['go_ast_coverage_pct']:.1f}%"
        rows.append(
            "<tr>"
            f"<td><code>{html_escape(p['python_package'])}</code><div class=\"sub\">{html_escape(p['verdict'])}</div><div class=\"sub\">trace <code>{html_escape(p['trace_id'])}</code></div></td>"
            f"<td>{go_pkgs}</td>"
            f"<td class=\"num\">{html_escape(s.get('python_files'))} / {html_escape(s.get('python_functions'))}</td>"
            f"<td class=\"num\">{html_escape(s.get('go_files'))} / {html_escape(s.get('go_functions'))}</td>"
            f"<td class=\"num\">{ast_py} / {ast_go}</td>"
            f"<td class=\"num\">{c12_text}</td>"
            f"<td class=\"num\">{dims_partial}</td>"
            f"<td class=\"small\">{html_escape(dim_text)}</td>"
            f"<td class=\"num\">{html_escape(reg)} / {html_escape(bad)}</td>"
            f"<td class=\"num\">{html_escape(b.get('go_parity_refs'))} / {html_escape(b.get('go_test_funcs'))}</td>"
            "</tr>"
        )

    contract_rows = []
    for r in matrix["contracts"]:
        status = r["status"]
        klass = "ok" if status in ("aligned", "aligned_with_extra") else "warn" if status == "partial" else "info"
        contract_rows.append(
            "<tr>"
            f"<td><code>{html_escape(r['id'])}</code></td>"
            f"<td>{html_escape(r['label'])}</td>"
            f"<td class=\"num\">{html_escape(r['reference'])}</td>"
            f"<td class=\"num\">{html_escape(r['target'])}</td>"
            f"<td><span class=\"pill {klass}\">{html_escape(status)}</span></td>"
            f"<td class=\"small\">{html_escape(r['detail'])}</td>"
            f"<td class=\"small\"><code>{html_escape(r['trace_id'])}</code><br><code>{html_escape(r['source'])}</code></td>"
            "</tr>"
        )

    ev = matrix["behavior"].get("events") or {}
    gap = matrix["behavior"].get("known_gaps") or {}
    test_map = matrix["behavior"].get("test_mapping") or {}
    phase = matrix["behavior"].get("phase_gates") or {}
    integration = matrix["behavior"].get("integration_scope") or {}
    c12 = t.get("c12_by_status") or {}
    verdicts = t.get("package_verdicts") or {}
    catalog = matrix.get("evidence_catalog") or []
    catalog_rows = []
    for e in catalog:
        if e.get("kind") == "c12-module":
            continue
        sha = e.get("sha256")
        sha_text = f"<code>{html_escape(sha[:12])}…</code>" if sha else "—"
        catalog_rows.append(
            "<tr>"
            f"<td><code>{html_escape(e.get('id'))}</code></td>"
            f"<td>{html_escape(e.get('kind'))}</td>"
            f"<td class=\"small\">{html_escape(e.get('path') or e.get('source'))}</td>"
            f"<td class=\"small\">{html_escape(e.get('selector'))}</td>"
            f"<td class=\"small\">{sha_text}</td>"
            "</tr>"
        )
    repro = "".join(f"<li><code>{html_escape(x)}</code></li>" for x in (matrix.get("reproduction") or []))
    return f"""<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>go_hive ↔ Hive 源码包一致性矩阵</title>
<style>
:root{{--bg:#0b1020;--panel:#121a2e;--panel2:#0f1627;--ink:#e7edf8;--muted:#94a3b8;--line:#263450;--accent:#67e8f9;--warn:#fbbf24;--bad:#fb7185;--ok:#4ade80;--info:#93c5fd}}
*{{box-sizing:border-box}}body{{margin:0;background:var(--bg);color:var(--ink);font:14px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}}
main{{max-width:1600px;margin:auto;padding:28px 22px 70px}}h1{{font-size:27px;margin:0 0 8px}}h2{{font-size:18px;margin:30px 0 10px;border-bottom:1px solid var(--line);padding-bottom:7px}}p,li{{color:var(--muted)}}a{{color:var(--accent)}}code{{color:#c4b5fd}}.sub{{color:var(--muted);font-size:11.5px}}
.cards{{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px;margin:18px 0}}.card{{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:14px}}.card b{{display:block;font-size:24px;color:var(--accent)}}.card span{{color:var(--muted);font-size:12px}}
.tablewrap{{overflow:auto;border:1px solid var(--line);border-radius:10px;background:var(--panel2)}}table{{border-collapse:collapse;width:100%;min-width:1450px}}th,td{{border-bottom:1px solid var(--line);padding:9px 10px;text-align:left;vertical-align:top}}th{{position:sticky;top:0;background:#16213a;color:#dbeafe;font-size:11px;white-space:nowrap;z-index:1}}.num{{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}}.small{{font-size:11.5px;color:var(--muted);max-width:220px}}
.pill{{display:inline-block;font-size:11px;border:1px solid var(--line);border-radius:999px;padding:2px 8px;white-space:nowrap}}.pill.ok{{color:var(--ok);border-color:#1d4b2c;background:#0f2016}}.pill.warn{{color:var(--warn);border-color:#5b4614;background:#211b0b}}.pill.info{{color:var(--info);border-color:#27456d;background:#101d30}}
.note{{border-left:3px solid var(--warn);background:#171c30;padding:12px 14px;border-radius:0 7px 7px 0;margin:14px 0}}ul{{margin:6px 0 6px 20px;padding:0}}li{{margin:5px 0}}
@media(max-width:720px){{main{{padding:20px 12px}}h1{{font-size:22px}}}}
</style></head><body><main>
<header><h1>go_hive ↔ Hive 源码包一致性矩阵</h1>
<p>按 <b>静态结构</b>、<b>功能契约</b>、<b>行为证据</b> 三轴并列。包级“结构对应”不是行为等价；函数 AST 匹配也不是语义证明。</p></header>
<div class="cards">
<div class="card"><b>{t['python_packages']}</b><span>Python packages</span></div>
<div class="card"><b>{t['python_files']} / {t['go_files']}</b><span>Python / Go source files</span></div>
<div class="card"><b>{t['python_functions']} / {t['go_functions']}</b><span>AST functions / methods</span></div>
<div class="card"><b>{t['python_ast_coverage_pct']}%</b><span>Python AST matched</span></div>
<div class="card"><b>{c12.get('ported', 0)} / {c12.get('partial', 0)} / {c12.get('out_of_scope', 0)}</b><span>C12 ported / partial / out_of_scope</span></div>
<div class="card"><b>{gap.get('fixed', 0)} / {gap.get('open', 0)}</b><span>Known gaps fixed / open</span></div>
</div>
<div class="note"><b>证据边界：</b>静态列来自包扫描与 Python/Go AST；契约列来自冻结的 API/CLI/环境/配置/工具/事件账本；行为列是 C12、事件、测试引用等登记证据。生成器不执行测试、不跑服务，也不把计数一致当成行为等价。</div>
<h2>① 逐包矩阵</h2>
<p>列：包 / Go 对应物 / 文件·函数 / AST 匹配率 / C12 / 未完全对齐维度 / 行为登记行·坏行 / Go parity 引用·测试函数。逐行 Go 计数可能因一个 Go 包服务多个 Python 包而重复，<b>合计以去重后的 package ledger 为准</b>。AST 覆盖率见 <a href="function-parity-matrix.html">函数级 AST 矩阵</a>。</p>
<div class="tablewrap"><table><thead><tr><th>Python 包</th><th>Go 对应物</th><th>Py 文件 / 函数</th><th>Go 文件 / 函数</th><th>AST 覆盖 Py / Go</th><th>C12 ported/partial/oos</th><th>未对齐维度</th><th>维度明细</th><th>Registry 行 / 坏行</th><th>Parity 引用 / 测试函数</th></tr></thead><tbody>
{''.join(rows)}
</tbody></table></div>
<h2>② 功能契约核对</h2>
<div class="tablewrap"><table><thead><tr><th>契约</th><th>面</th><th>参考</th><th>Go</th><th>判定</th><th>差异 / 口径</th><th>证据</th></tr></thead><tbody>
{''.join(contract_rows)}
</tbody></table></div>
<h2>③ 行为证据</h2>
<div class="cards">
<div class="card"><b>{ev.get('ported', 0)} / {ev.get('partial', 0)}</b><span>Events ported / partial</span></div>
<div class="card"><b>{ev.get('emitter_missing', 0)}</b><span>Events missing emitter</span></div>
<div class="card"><b>{ev.get('frontend_missing', 0)}</b><span>Events missing frontend handler</span></div>
<div class="card"><b>{test_map.get('go_tests', '—')} / {test_map.get('python_tests', '—')}</b><span>Go / Python tests (registry)</span></div>
<div class="card"><b>{test_map.get('parity_refs', '—')}</b><span>Go parity references</span></div>
<div class="card"><b>{phase.get('port_ungated', 0)}</b><span>Phase-gate ungated</span></div>
</div>
<ul>
<li><b>C12：</b>{html_escape(t.get('c12_by_status'))}；包判定：{html_escape(verdicts)}。</li>
<li><b>事件：</b>Python {html_escape(ev.get('python'))} / Go {html_escape(ev.get('go'))}；emitter 缺失 {html_escape(ev.get('emitter_missing'))}；partial {html_escape(ev.get('partial'))}。</li>
<li><b>测试登记：</b>{html_escape(test_map)}。</li>
<li><b>已知缺口：</b>fixed {html_escape(gap.get('fixed'))} / open {html_escape(gap.get('open'))} / total {html_escape(gap.get('total'))}；详见 <code>coverage-registry.json</code>。</li>
<li><b>集成面：</b>状态 <code>{html_escape(integration.get('status'))}</code>；{html_escape(integration.get('warning'))}</li>
</ul>
<h2>④ 证据追踪</h2>
<p>每个结论都有稳定 trace id；下表展开 artifact、package、contract 与 behavior registry，C12 模块的 273 条逐文件 trace 保留在 JSON 的 <code>evidence_catalog</code> 中。artifact 行附输入文件 SHA-256。可验证报告见 <a href="consistency-evidence-report.html">HTML</a> / <a href="consistency-evidence-report.md">Markdown</a> / <a href="consistency-evidence-report.json">JSON</a>；运行 <code>make consistency-matrix-check</code> 会逐条校验哈希、锚点与引用。</p>
<div class="tablewrap"><table><thead><tr><th>Trace ID</th><th>Kind</th><th>Source</th><th>Selector</th><th>SHA-256</th></tr></thead><tbody>
{''.join(catalog_rows)}
</tbody></table></div>
<h2>⑤ 复现与维护</h2>
<ul>{repro}</ul>
<p>输入 manifest 含每个账本的 SHA-256；生成器只用 Python stdlib，不访问网络、不调用 LLM、不执行测试。</p>
<footer class="sub">Go API/CLI/配置数据来自冻结契约；Python/Go AST 函数数据来自 <a href="function-parity-matrix.json">function-parity-matrix.json</a>。行为等价仍需按行执行测试与真实 E2E。</footer>
</main></body></html>
"""


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--check", action="store_true", help="fail if generated artifacts are stale")
    args = ap.parse_args()
    try:
        matrix = build()
    except Exception as exc:
        print(f"consistency-matrix: FAIL: {exc}", file=sys.stderr)
        return 1
    payload = json.dumps(matrix, ensure_ascii=False, indent=2, sort_keys=False) + "\n"
    page = render(matrix)
    if args.check:
        bad = []
        if not OUT_JSON.exists() or OUT_JSON.read_text(encoding="utf-8") != payload:
            bad.append(OUT_JSON)
        if not OUT_HTML.exists() or OUT_HTML.read_text(encoding="utf-8") != page:
            bad.append(OUT_HTML)
        if bad:
            print("consistency-matrix: DRIFT: " + ", ".join(str(p) for p in bad), file=sys.stderr)
            return 1
        print(f"consistency-matrix: OK ({matrix['totals']['python_packages']} packages / C12 {matrix['totals']['c12_by_status']})")
        return 0
    OUT_JSON.write_text(payload, encoding="utf-8")
    OUT_HTML.write_text(page, encoding="utf-8")
    print(f"wrote {OUT_JSON.relative_to(ROOT)} and {OUT_HTML.relative_to(ROOT)}")
    print("c12:", matrix["totals"]["c12_by_status"])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
