#!/usr/bin/env python3
"""Integrations evidence-audit anchor gate.

`go_hive/docs/integrations-evidence-audit.json` carries `file:line` evidence
anchors for each module's auth wiring.  Package splits and identifier renames
invalidated them silently before (P2 split: GA 96/623, GW 192, freshdesk 285),
so this gate resolves every registered anchor against the checked-out tree:

  * the audit JSON still names the anchor this gate pins (`file:line`);
  * the file exists under `internal/tools/integrations/` (or the module root
    for `internal/...` paths);
  * the line is inside the file;
  * the line contains the expected token — the semantic marker the anchor
    claims (function / env var / header scheme), so a bare renumber cannot
    pass.

Exit code 0 = every anchor resolves, 1 = drift.  `--fix` prints candidate
locations for failing anchors; it never edits files.

Update procedure: when a split or rename moves an anchored line, update the
JSON evidence string *and* the matching entry in `ANCHORS` in the same commit.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

# One entry per audit module occurrence: the same source site can be the
# evidence for several modules (calendar/gmail/docs/sheets share one client).
ANCHORS = [
    {
        "module": "google_analytics",
        "file": "marketing_productivity.go",
        "line": 70,
        "expect": "marketingGoogleTokenURI",
        "claim": "SA JWT 换 token 的 OAuth token endpoint",
    },
    {
        "module": "google_analytics",
        "file": "google_analytics.go",
        "line": 94,
        "expect": "googleJWT",
        "claim": "SA JWT 换 token（service_account_mint）",
    },
    {
        "module": "google_search_console",
        "file": "google_analytics_search_console.go",
        "line": 127,
        "expect": "GOOGLE_SEARCH_CONSOLE_TOKEN",
        "claim": "GSC token 缺失错误（Bearer / SA JWT）",
    },
    *[
        {
            "module": module,
            "file": "gworkspace/google_workspace.go",
            "line": 215,
            "expect": "Bearer",
            "claim": "Google 共享客户端 Bearer 头",
        }
        for module in ("calendar", "gmail", "google_docs", "google_sheets")
    ],
    {
        "module": "freshdesk",
        "file": "crm/freshdesk.go",
        "line": 285,
        "expect": "basicAuth",
        "claim": "Basic base64(api_key:X)",
    },
]

# Anchors in these file families must all be registered above, so a new module
# reusing a split family cannot slip in without a token check.
FAMILIES = ("google_analytics*.go", "google_workspace*.go", "freshdesk*.go")


def audit_path(root: Path) -> Path:
    return root / "go_hive/docs/integrations-evidence-audit.json"


def package_dir(root: Path) -> Path:
    return root / "go_hive/go-hive/internal/tools/integrations"


def module_root(root: Path) -> Path:
    return root / "go_hive/go-hive"


def resolve_file(root: Path, rel: str) -> Path:
    if rel.startswith("internal/"):
        return module_root(root) / rel
    return package_dir(root) / rel


def load_modules(root: Path) -> dict[str, dict]:
    path = audit_path(root)
    if not path.is_file():
        raise SystemExit(f"integrations-evidence: 缺审计文件 {path}")
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise SystemExit(f"integrations-evidence: {path} 不是合法 JSON：{exc}")
    return {m.get("module"): m for m in data.get("modules", [])}


def evidence_of(module: dict | None) -> str:
    if not module:
        return ""
    return (module.get("auth") or {}).get("evidence", "")


def check_anchor(root: Path, entry: dict, modules: dict[str, dict]) -> tuple[list[str], str]:
    """Return (problems, snippet) for one registered anchor."""
    problems: list[str] = []
    anchor = f"{entry['file']}:{entry['line']}"

    module = modules.get(entry["module"])
    if module is None:
        problems.append(f"审计 JSON 缺模块 {entry['module']!r}")
    else:
        evidence = evidence_of(module)
        if anchor not in evidence:
            problems.append(f"JSON auth.evidence 未指向 {anchor}（当前 {evidence!r}）")

    path = resolve_file(root, entry["file"])
    if not path.is_file():
        return problems + [f"文件不存在：{path}"], ""
    try:
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError as exc:
        return problems + [f"文件不可读：{path}: {exc}"], ""
    if not 1 <= entry["line"] <= len(lines):
        return problems + [f"{anchor} 越界（文件共 {len(lines)} 行）"], ""

    text = lines[entry["line"] - 1]
    if entry["expect"] not in text:
        problems.append(f"{anchor} 行内不含预期 token {entry['expect']!r}：{text.strip()[:120]}")
    return problems, text.strip()


def coverage_problems(modules: dict[str, dict]) -> list[str]:
    """Every module anchored into a split family must be registered above."""
    import fnmatch

    registered = {entry["module"] for entry in ANCHORS}
    problems: list[str] = []
    for name, module in modules.items():
        evidence = evidence_of(module)
        files = [f for f in evidence.split() if f.endswith(".go") or ".go:" in f]
        files = [f.split(":")[0] for f in files]
        hits = [f for f in files if any(fnmatch.fnmatch(Path(f).name, pat) for pat in FAMILIES)]
        if hits and name not in registered:
            problems.append(f"模块 {name!r} 的 auth 证据引用 {', '.join(sorted(set(hits)))}，但未在 ANCHORS 注册")
    return problems


def suggest(root: Path, entry: dict) -> list[str]:
    """Print candidate lines for a failing anchor (never edits)."""
    import fnmatch

    pkg = package_dir(root)
    family = entry.get("family") or f"{Path(entry['file']).stem}*.go"
    hits: list[str] = []
    for path in sorted(pkg.rglob("*.go")):
        if not path.is_file() or not fnmatch.fnmatch(path.name, family):
            continue
        for number, line in enumerate(path.read_text(encoding="utf-8", errors="replace").splitlines(), 1):
            if entry["expect"] in line:
                hits.append(f"    {path.relative_to(root)}:{number}: {line.strip()[:120]}")
                if len(hits) >= 5:
                    return hits
    return hits


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="校验 integrations 证据审计的 file:line 锚点")
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[2]), help="仓库根目录")
    parser.add_argument("--fix", action="store_true", help="失败时打印候选位置（不写文件）")
    args = parser.parse_args(argv)

    root = Path(args.root).resolve()
    modules = load_modules(root)

    failures: list[tuple[dict, list[str]]] = []
    print(f"== integrations evidence anchors (root={root}) ==")
    for entry in ANCHORS:
        problems, snippet = check_anchor(root, entry, modules)
        anchor = f"{entry['file']}:{entry['line']}"
        if problems:
            failures.append((entry, problems))
            print(f"FAIL {entry['module']:22} {anchor:44} {entry['claim']}")
            for problem in problems:
                print(f"     - {problem}")
        else:
            print(f"PASS {entry['module']:22} {anchor:44} {entry['claim']}  [{entry['expect']}]")

    coverage = coverage_problems(modules)
    for problem in coverage:
        print(f"FAIL coverage  {problem}")

    if args.fix and (failures or coverage):
        print("== --fix 候选（请人工复核后同时改 JSON 与 ANCHORS）==")
        for entry, problems in failures:
            anchor = f"{entry['file']}:{entry['line']}"
            print(f"* {entry['module']} {anchor}")
            for problem in problems:
                print(f"    {problem}")
            candidates = suggest(root, entry)
            if candidates:
                print(f"  含预期 token {entry['expect']!r} 的候选：")
                print("\n".join(candidates))
            else:
                print(f"  {entry['family'] if 'family' in entry else entry['file']} 内未找到 {entry['expect']!r}")

    sites = {(entry["file"], entry["line"]) for entry in ANCHORS}
    passed = len(ANCHORS) - len(failures)
    if failures or coverage:
        print(
            f"integrations-evidence-anchors: FAIL {passed}/{len(ANCHORS)} anchors "
            f"({len(sites)} sites), coverage_problems={len(coverage)}"
        )
        return 1
    print(f"integrations-evidence-anchors: {passed}/{len(ANCHORS)} anchors PASS ({len(sites)} sites)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
