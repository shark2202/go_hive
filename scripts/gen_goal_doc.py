#!/usr/bin/env python3
"""Generate the Hive goal-model reference (zh-CN HTML + JSON).

Recomputes the numbers it cites from source:
  - Goal / SuccessCriterion / Constraint fields + GoalStatus  (core/framework/schemas/goal.py)
  - orchestrator Goal copy + to_prompt_context                (core/framework/orchestrator/goal.py)
  - OutcomeAggregator methods                                 (core/framework/host/outcome_aggregator.py)
  - Go counterparts + key symbols                             (go_hive/go-hive/internal/**)
  - C12 registry status per module                            (contracts/module-inventory.json)
  - CLI subcommands (asserted to exist in internal/cli sources)

Usage: python3 go_hive/scripts/gen_goal_doc.py [--check]
"""
from __future__ import annotations

import ast
import html
import json
import re
import subprocess
import sys
from datetime import datetime
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive/docs/goal-design.html"
OUT_JSON = REPO / "go_hive/docs/goal-design.json"
STYLE_SOURCE = REPO / "go_hive/docs/memory-and-soul.html"
ESC = lambda s: html.escape(str(s), quote=True)

CLI_SUBCOMMANDS = ["serve", "open", "queen", "colony", "session", "chat", "skill", "debugger", "mcp", "janitor"]

GO_FILES = {
    "结构": "go_hive/go-hive/internal/schemas/goal.go",
    "评估": "go_hive/go-hive/internal/agentloop/goal_eval.go",
    "结果持久化": "go_hive/go-hive/internal/colony/goal_outcome.go",
}
PY_FILES = {
    "schema": "core/framework/schemas/goal.py",
    "orchestrator": "core/framework/orchestrator/goal.py",
    "聚合器": "core/framework/host/outcome_aggregator.py",
    "colony 宿主": "core/framework/host/colony_runtime.py",
}


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def parse_py(path: Path) -> ast.Module:
    return ast.parse(read(path))


def class_fields(tree: ast.Module, cls: str) -> list[dict]:
    for node in ast.walk(tree):
        if isinstance(node, ast.ClassDef) and node.name == cls:
            out = []
            for stmt in node.body:
                if isinstance(stmt, ast.AnnAssign) and isinstance(stmt.target, ast.Name):
                    ann = ast.unparse(stmt.annotation)
                    default = ast.unparse(stmt.value) if stmt.value is not None else None
                    out.append({"name": stmt.target.id, "type": ann, "default": default})
            return out
    return []


def enum_members(tree: ast.Module, cls: str) -> list[str]:
    for node in ast.walk(tree):
        if isinstance(node, ast.ClassDef) and node.name == cls:
            names = []
            for stmt in node.body:
                if isinstance(stmt, ast.Assign) and stmt.targets and isinstance(stmt.targets[0], ast.Name):
                    names.append(stmt.targets[0].id)
            return names
    return []


def quoted_items(text: str) -> list[str]:
    return re.findall(r"'([^']+)'", text)


def method_names(path: Path) -> list[str]:
    tree = parse_py(path)
    out = []
    for node in ast.walk(tree):
        if isinstance(node, ast.ClassDef):
            for stmt in node.body:
                if isinstance(stmt, ast.FunctionDef):
                    out.append(f"{node.name}.{stmt.name}")
    return out


def go_symbols(path: Path) -> dict:
    s = read(path)
    funcs = re.findall(r"^func (?:\([^)]*\) )?([A-Za-z_][A-Za-z0-9_]*)", s, re.M)
    types = re.findall(r"^type ([A-Za-z_][A-Za-z0-9_]*) ", s, re.M)
    return {"funcs": funcs[:12], "types": types[:12], "lines": len(s.splitlines())}


def c12_status() -> dict:
    inv = json.loads(read(REPO / "contracts/module-inventory.json"))
    files = inv.get("files") or inv.get("modules") or []
    out = {}
    for key, rel in PY_FILES.items():
        for m in files:
            if m.get("python_path") == rel:
                out[key] = {"python_path": rel, "status": m.get("status"), "go": m.get("go_counterpart")}
                break
    return out


def build() -> tuple[str, dict]:
    gpy = REPO / PY_FILES["schema"]
    opy = REPO / PY_FILES["orchestrator"]
    gtree = parse_py(gpy)
    otree = parse_py(opy)

    goal_fields = class_fields(gtree, "Goal")
    crit_fields = class_fields(gtree, "SuccessCriterion")
    cons_fields = class_fields(gtree, "Constraint")
    statuses = enum_members(gtree, "GoalStatus")

    crit_metric = next((f for f in crit_fields if f["name"] == "metric"), {})
    metrics = quoted_items(str(crit_metric.get("default", "")))
    cons_type = next((f for f in cons_fields if f["name"] == "constraint_type"), {})
    cons_kinds = quoted_items(str(cons_type.get("default", "")))
    cons_cat = next((f for f in cons_fields if f["name"] == "category"), {})
    categories = quoted_items(str(cons_cat.get("default", "")))
    weight = next((f for f in crit_fields if f["name"] == "weight"), {})

    o_methods = [m for m in method_names(opy) if m.startswith("Goal.")]
    agg_methods = method_names(REPO / PY_FILES["聚合器"])
    gos = {k: go_symbols(REPO / v) for k, v in GO_FILES.items()}
    reg = c12_status()

    # CLI subcommands must exist in the Go CLI sources
    cli_src = "\n".join(read(p) for p in (REPO / "go_hive/go-hive/internal/cli").glob("*.go")
                        if p.name.endswith(".go") and "_test" not in p.name)
    missing = [c for c in CLI_SUBCOMMANDS
               if f'"{c}"' not in cli_src and f'"hive {c}"' not in cli_src]

    # style reuse
    style = ""
    if STYLE_SOURCE.exists():
        m = re.search(r"<style>.*?</style>", read(STYLE_SOURCE), re.S)
        if m:
            style = m.group(0)

    rows = lambda items: "\n".join(
        f"<tr><td><code>{ESC(i['name'])}</code></td><td><code>{ESC(i['type'])}</code></td>"
        f"<td><code>{ESC(i['default'] or '—')}</code></td></tr>" for i in items)

    def note(md_items):
        return "\n".join(f"<li>{i}</li>" for i in md_items)

    body = f"""<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Hive Goal 模型 · 目标驱动设计参考</title>
{style}
</head><body>
<h1>Goal 模型与「目标驱动」设计 <span class="pill ok">可复用资料</span></h1>
<p class="sub">生成：{datetime.now().strftime('%Y-%m-%d %H:%M %Z')} · 数据现场重算自源码（AST + 注册表）·
生成器 <code>go_hive/scripts/gen_goal_doc.py</code>（可复跑；数字不手抄）</p>

<h2>§1 结论速览</h2>
<div class="cards">
<div class="card ok"><div class="k">goal 设计</div><div class="v">有</div><div class="d">结构 + 权重 + 约束 + 演化 + 评估闭环</div></div>
<div class="card warn"><div class="k">独立「goal 模式」开关</div><div class="v">无</div><div class="d">宿主是 colony（多体）；单聊为对话驱动</div></div>
<div class="card"><div class="k">CLI 子命令</div><div class="v">{len(CLI_SUBCOMMANDS)}</div><div class="d">{'、'.join(CLI_SUBCOMMANDS)}</div></div>
<div class="card"><div class="k">Go 对齐</div><div class="v">ported</div><div class="d">schema / 评估 / 结果持久化 三件套</div></div>
</div>

<h2>§2 数据模型（core/framework/schemas/goal.py 现场解析）</h2>
<p><b>GoalStatus</b>：{ESC(' · '.join(statuses))}</p>
<table><tr><th>Goal 字段</th><th>类型</th><th>默认</th></tr>
{rows(goal_fields)}</table>
<table><tr><th>SuccessCriterion</th><th>类型</th><th>默认</th></tr>
{rows(crit_fields)}</table>
<p class="sub">metric 可选值（从源码 description 解析）：{ESC(' · '.join(metrics))}；weight 约束：{ESC(weight.get('type', ''))}（{ESC(weight.get('default', ''))}）</p>
<table><tr><th>Constraint</th><th>类型</th><th>默认</th></tr>
{rows(cons_fields)}</table>
<p class="sub">约束类型：{ESC(' / '.join(cons_kinds))}；类别：{ESC(' · '.join(categories))}</p>

<h2>§3 驱动回路</h2>
<pre>
trigger（timer / playbook）
   → queen marshal（goal 分解为任务 / 派 worker）
   → worker 执行（worker.json 可带 success_criteria）
   → OutcomeAggregator 聚合（{ESC(', '.join(agg_methods) or 'OutcomeAggregator')}）
   → agentloop goal_eval 按成功标准×权重判定（加权比例，LLM judge 可参与）
   → 未达标 → 重试 / 升级；达标 → 推进 / 结束
（宿主 = colony runtime：{ESC(PY_FILES['colony 宿主'])} 持有 overall goal 与 get_goal_progress）
</pre>
<p>orchestrator 侧方法：{ESC(' · '.join(o_methods))}（<code>to_prompt_context</code> 把目标与约束注入模型上下文）。</p>

<h2>§4 go_hive 对应实现</h2>
<table><tr><th>面</th><th>Go 文件</th><th>关键符号（前 12）</th><th>行数</th></tr>
{''.join(f"<tr><td>{ESC(k)}</td><td><code>{ESC(GO_FILES[k])}</code></td><td><code>{ESC(', '.join(v['funcs']) or '—')}</code></td><td>{v['lines']}</td></tr>" for k, v in gos.items())}
</table>
<p class="sub">类型导出：{ESC('; '.join(f"{k}: {', '.join(v['types']) or '—'}" for k, v in gos.items()))}</p>

<h2>§5 C12 登记状态（contracts/module-inventory.json）</h2>
<table><tr><th>面</th><th>Python</th><th>状态</th><th>Go counterpart</th></tr>
{''.join(f"<tr><td>{ESC(k)}</td><td><code>{ESC(v['python_path'])}</code></td><td>{ESC(v['status'])}</td><td><code>{ESC(v['go'] or '—')}</code></td></tr>" for k, v in reg.items())}
</table>

<h2>§6 使用方式与入口</h2>
<ul>
<li><b>colony 带目标</b>：建 colony 时带 overall Goal；trigger（timer/playbook）驱动长跑；进度经 <code>get_goal_progress</code> 暴露。</li>
<li><b>单聊不跑 goal</b>：queen chat 是对话驱动；目标体系只在 colony 多体场景生效。</li>
<li><b>无 CLI 子命令</b>：<code>hive</code> 现有 {len(CLI_SUBCOMMANDS)} 个子命令（{'、'.join(CLI_SUBCOMMANDS)}），不含 goal；管理走 colony 配置/queen 工具链。</li>
<li><b>worker 级标准</b>：worker.json 的 success_criteria 进 judge；colony 级进 goal_eval。</li>
</ul>

<h2>§7 复用链接</h2>
<ul>
{note([
 f"<code>docs/goal-marshal-parity.md</code>：weight=0 语义 / ID 省略 / prompt context / colony path 双跑证据",
 f"<code>docs/hive-concepts-inventory.html</code>：18 原语与机制定位（Goal 属 Plan/Tracker 族）",
 f"<code>docs/loop-vocabulary.html</code>：等待/注意力/守卫/自治术语",
 f"<code>docs/module-interfaces.html</code> 与 <code>docs/hive-package-atlas.html</code>：模块与包定位",
 f"<code>docs/consistency-audit.md</code>：逐项核查表（模块级）",
])}
</ul>
<footer>本页由 <code>go_hive/scripts/gen_goal_doc.py</code> 生成；改动源码后重跑即可刷新（数字全部现场重算）。CLI 子命令断言：{'全部命中 Go CLI 源' if not missing else '缺失 ' + ', '.join(missing)}</footer>
</body></html>
"""
    payload = {
        "schema": "goal-design/v1",
        "generated_at": datetime.now().isoformat(timespec="seconds"),
        "goal_status_members": statuses,
        "criterion_metrics": metrics,
        "constraint_kinds": cons_kinds,
        "constraint_categories": categories,
        "goal_fields": goal_fields,
        "criterion_fields": crit_fields,
        "constraint_fields": cons_fields,
        "orchestrator_methods": o_methods,
        "aggregator_methods": agg_methods,
        "go": gos,
        "c12": reg,
        "cli_subcommands": CLI_SUBCOMMANDS,
        "cli_missing": missing,
    }
    return body, payload


def main() -> int:
    html_text, payload = build()
    if "--check" in sys.argv:
        current = read(OUT_HTML) if OUT_HTML.exists() else ""
        strip = lambda s: re.sub(r"生成：[^·]+·", "", s)
        if strip(current) == strip(html_text):
            print("goal-design: up to date")
            return 0
        print("goal-design: DRIFT — re-run without --check")
        return 1
    OUT_HTML.write_text(html_text, encoding="utf-8")
    OUT_JSON.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {OUT_HTML.relative_to(REPO)} and {OUT_JSON.relative_to(REPO)}")
    print("metrics:", payload["criterion_metrics"], "| statuses:", payload["goal_status_members"],
          "| cli_missing:", payload["cli_missing"])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
