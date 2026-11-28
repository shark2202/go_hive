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
<style>.wrap{{overflow-wrap:break-word}}.wrap h1 span{{white-space:nowrap}}.wrap h2,.wrap p,.wrap .sub{{word-break:normal}}.wrap .sub,.wrap .card .d{{word-break:keep-all}}.wrap code{{overflow-wrap:break-word;word-break:normal}}</style>
</head><body>
<div class="wrap">
<h1>Goal 模型与「目标驱动」设计 <span class="pill ok">可复用资料</span></h1>
<p class="sub">生成：{datetime.now().strftime('%Y-%m-%d %H:%M %Z')} · 数据现场重算自源码（AST + 注册表）·
生成器 <code>go_hive/scripts/gen_goal_doc.py</code>（可复跑；数字不手抄）</p>

<h2>§1 结论速览</h2>
<div class="cards">
<div class="card ok"><div class="k">goal 设计</div><div class="v">有</div><div class="d">结构 + 权重 + 约束 + 版本字段；Python 当前无自动执行闭环</div></div>
<div class="card warn"><div class="k">独立「goal 模式」开关</div><div class="v">无</div><div class="d">宿主是 colony（多体）；单聊为对话驱动</div></div>
<div class="card"><div class="k">CLI 子命令</div><div class="v">{len(CLI_SUBCOMMANDS)}</div><div class="d">{'、'.join(CLI_SUBCOMMANDS)}</div></div>
<div class="card"><div class="k">Go 对齐</div><div class="v">ported</div><div class="d">schema / 评估 / 结果持久化 三件套</div></div>
</div>

<h2>§2 数据模型</h2>
<p class="sub"><code>core/framework/schemas/goal.py</code> 现场解析（AST）</p>
<p><b>GoalStatus</b>：{ESC(' · '.join(statuses))}</p>
<table><tr><th>Goal 字段</th><th>类型</th><th>默认</th></tr>
{rows(goal_fields)}</table>
<table><tr><th>SuccessCriterion</th><th>类型</th><th>默认</th></tr>
{rows(crit_fields)}</table>
<p class="sub">metric 可选值（从源码 description 解析）：{ESC(' · '.join(metrics))}；weight 约束：{ESC(weight.get('type', ''))}（{ESC(weight.get('default', ''))}）</p>
<table><tr><th>Constraint</th><th>类型</th><th>默认</th></tr>
{rows(cons_fields)}</table>
<p class="sub">约束类型：{ESC(' / '.join(cons_kinds))}；类别：{ESC(' · '.join(categories))}</p>

<h2>§3 设计回路（概念；Python 当前未自动执行）</h2>
<pre>
trigger（timer / playbook）
   → queen marshal（goal 分解为任务 / 派 worker）
   → worker 执行（worker.json 可带 success_criteria）
   → OutcomeAggregator 聚合（{ESC(', '.join(agg_methods) or 'OutcomeAggregator')}）
   → agentloop goal_eval 按成功标准×权重判定（加权比例，LLM judge 可参与）
   → 设计意图：未达标 → 重试 / 升级；达标 → 推进 / 结束
（宿主 = colony runtime：{ESC(PY_FILES['colony 宿主'])} 持有 overall goal 与 get_goal_progress）
</pre>
<p class="sub"><b>重要：</b>Hive Python 当前把 Goal 作为声明式 prompt 上下文；<code>Goal.is_success()</code> 无生产调用，<code>OutcomeAggregator</code> 是 stub，<code>get_goal_progress()</code> 恒返回空/0，未达标重试与达标收工是设计意图，不是当前自动行为。实际流转见 §6.6。</p>
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

<h2>§6 何时创建 / 何时生效 / 怎么使用</h2>
<p><b>一句话：Goal 是 colony 的内部执行模型，不是每次聊天创建的对象，也没有 <code>/goal</code> 或 <code>goal</code> tool。</b></p>

<h3>6.1 三层对象，不要混</h3>
<table><tr><th>对象</th><th>存储/来源</th><th>谁创建</th><th>用途</th></tr>
<tr><td><b>Colony Goal</b><br><code>schemas.Goal</code></td><td>colony 的 <code>worker.json</code> 里的 <code>goal</code> block；运行时对象不单独落盘</td><td>colony 初始化 / fork 时写定义；session 加载时构造对象</td><td>criteria 判定、prompt context、goal_progress / goal_evaluation</td></tr>
<tr><td><b>Task plan goal</b></td><td>session 的 <code>tasks.json</code> 的 <code>meta.goal</code></td><td>第一次 <code>task_create(goal=...)</code>；后续可继承/覆盖；pivot 必须提供</td><td>task reminders、task list、pivot</td></tr>
<tr><td><b>Worker display goal</b></td><td>worker 状态/结果字段</td><td>queen 调 <code>run_worker(goal=...)</code></td><td>UI 标题/描述；不参与 Goal 评估</td></tr>
</table>

<h3>6.2 何时创建</h3>
<ol>
<li><b>新建空 colony</b>：<code>POST /api/sessions</code> 发现 colony 目录没有 <code>metadata.json</code>，走 <code>ensureMinimalColony()</code>；它写出的 <code>worker.json</code> 带默认 goal：<code>{{"description":"Work on tasks for colony &lt;id&gt;"}}</code>。随后 <code>newColonyHost()</code> / <code>LoadAgentSpec()</code> 构造内存 <code>schemas.Goal</code>。</li>
<li><b>从源会话 fork / pivot 新 colony</b>：接受 <code>task_create(new_colony=true)</code> 的 pivot 时，<code>writeColonyFromSession()</code> 用 pivot 的 <code>goal</code>（没有则用 <code>initial_prompt</code>，再没有则用默认句）写 <code>worker.json</code> 的 <code>goal.description</code>；新 colony 加载时构造 Goal。</li>
<li><b>已有 / 导入 / 手工配置的 colony</b>：只在该 colony 第一次构建 session runtime 时读取 <code>worker.json</code>；如果 worker 配置里的 goal 已存在，不重写。</li>
<li><b>每次 session/runtime 启动</b>：Goal 是内存对象，重新从 <code>worker.json</code> 构造；没有单独的 <code>goal.json</code>，也没有 Goal CRUD API。</li>
</ol>
<p class="sub">上游 <code>session_manager.py</code> 还会为 session-level ColonyRuntime 创建 <code>colony_goal_&lt;session&gt;</code> 默认 Goal；Go 当前 colony host 直接采用 <code>worker.json</code> 的 Goal。这是实现口径差异，不要把两者当成同一份持久化数据。</p>

<h3>6.3 什么情况下真正生效</h3>
<p>Goal 对象总是会被构造，但 <b>description-only 的 Goal 不会进入 prompt，也不会触发评估</b>：<code>Goal.ToPromptContext()</code> 只有在存在 <code>success_criteria</code> / <code>constraints</code> / <code>context</code> 时才返回内容；<code>EvaluateGoal</code> 在没有 <code>success_criteria</code> 时直接返回 nil。</p>
<pre>
worker.json:
{{
  "goal": {{
    "description": "检查 20 个账号的收款状态",
    "success_criteria": ["列出全部 20 个账号", "每个账号给出状态"],
    "constraints": ["不要修改账号配置"]
  }}
}}
        │
        ▼
LoadAgentSpec → Goal{{ID:"&lt;colony&gt;-goal", Status:"draft", criteria:[sc-0, sc-1], constraints:[c-0]}}
        │
        ├─ 非 persistent worker: GoalContext = ToPromptContext()
        ├─ success_criteria → AgentSpec / judge 配置
        ├─ worker 完成后 EvaluateGoal(final text + explicit report)
        └─ goal_progress 事件 / goal_evaluation / improvement signals
</pre>
<p>默认 fresh colony 的 goal 只有 description，所以它是惰性的：不会注入 worker prompt，不会构建 judge，也不会产生 <code>goal_evaluation</code>。</p>

<h3>6.4 怎么用 / 输出</h3>
<ul>
<li><b>prompt context</b>：Goal 的 description、criteria、constraints、context 进入非 persistent worker 的 system prompt / 首轮上下文；empty context 不追加。</li>
<li><b>criteria 判定</b>：<code>worker.json</code> 的 criteria 转成 metric=<code>llm_judge</code>、weight=1 的 <code>SuccessCriterion</code>；最终输出和显式 report 一起交给 <code>EvaluateGoal</code>。支持 <code>output_contains</code>、<code>output_equals</code>、<code>llm_judge</code>、<code>success_rate</code>；<code>custom</code> 标 unsupported。</li>
<li><b>成功线</b>：按权重计算，满足权重达到 <b>90%</b> 时 <code>Goal.IsSuccess()</code> 为 true；score 通过 <code>goal_progress</code> 事件发布。</li>
<li><b>结果</b>：<code>goal_evaluation</code> 写进 worker result / report 的 data；未满足 criteria 生成 improvement signals。</li>
<li><b>状态</b>：<code>GoalStatus</code> 有 draft/ready/active/completed/failed/suspended 枚举，但 Go runtime 不自动写 Status；评估只生成 evaluation / signals，不凭分数直接重跑或结束 worker。</li>
</ul>

<h3>6.5 使用入口</h3>
<ul>
<li><b>单聊不跑 colony Goal</b>：queen DM 是对话驱动；Goal 体系只在 colony 多体场景生效。</li>
<li><b>配置入口</b>：当前只能编辑 colony 的 <code>worker.json</code>（或走 fork/pivot 写入 description）；没有 Goal 管理页面/API。</li>
<li><b>没有 <code>/goal</code> / <code>goal</code> tool</b>：<code>hive</code> 现有 {len(CLI_SUBCOMMANDS)} 个子命令（{'、'.join(CLI_SUBCOMMANDS)}），不含 goal；工具注册表也没有名为 goal 的工具。</li>
<li><b>Task goal 是另一条线</b>：<code>task_create(goal=...)</code> 写 <code>tasks.json</code>；<code>run_worker(goal=...)</code> 只是 worker 展示标题，两者都不进 colony Goal 评估。</li>
</ul>
<p class="sub">代码锚点：<code>internal/server/routes_colonies.go::ensureMinimalColony</code>（默认 goal）、<code>internal/cli/colony_worker_definition.go::writeColonyFromSession</code>（fork goal）、<code>internal/agents/agent_spec.go::LoadAgentSpec</code>（构造 Goal）、<code>internal/cli/loader_colony_host.go::newColonyHost</code>（注入 runtime）、<code>internal/agentloop/goal_eval.go::EvaluateGoal</code>（评估）、<code>internal/colony/goal_outcome.go::evaluateWorkerGoal</code>（结果/事件）。</p>

<h3>6.6 Hive 上游（Python）的实际流转</h3>
<p>Hive 源码里有两条 Goal 路径，<code>schemas.Goal</code> 是 colony 路径；graph / exported agent 走另一份 <code>orchestrator.goal.Goal</code>。</p>
<table><tr><th>路径</th><th>Goal 类</th><th>创建位置</th><th>持有者</th></tr>
<tr><td>Colony runtime</td><td><code>framework.schemas.goal.Goal</code></td><td><code>session_manager._start_queen</code> 创建 <code>colony_goal_&lt;session&gt;</code> 默认 Goal</td><td><code>ColonyRuntime._goal</code></td></tr>
<tr><td>Graph / exported agent</td><td><code>framework.orchestrator.goal.Goal</code></td><td><code>AgentLoader</code> 从 <code>agent.json</code> / worker 配置构造</td><td><code>AgentRuntime.goal</code></td></tr>
</table>
<pre>
创建
  session_manager._start_queen
    → Goal(id=colony_goal_&lt;session&gt;, status=draft)
    → ColonyRuntime(goal=...)
  AgentLoader（graph 路径）
    → orchestrator Goal
    → AgentRuntime(goal=...)

应用
  ColonyRuntime._goal
    ├─ worker AgentContext.goal
    ├─ worker AgentContext.goal_context = goal.to_prompt_context()
    └─ AgentLoop 拼进 "Goal: ..."
  NodeContext（graph）
    └─ goal_context = goal.to_prompt_context()

“完成”/进度
  Goal.is_success()          ← 有定义，生产路径未调用
  GoalStatus                 ← 有枚举，生产不推进
  OutcomeAggregator          ← stub，progress 恒 0.0
  get_goal_progress()        ← 返回空进度
  emit_goal_progress()       ← 有 API，生产无调用
  colony stop/delete         ← 无自动触发
</pre>
<ul>
<li><b>创建</b>：colony 默认 Goal 由 <code>session_manager._start_queen</code> 构造；graph Goal 由 <code>AgentLoader</code> 构造。没有 <code>/goal</code>、没有 goal tool、没有 Goal CRUD 文件。</li>
<li><b>应用</b>：<code>AgentContext.goal_context</code> / <code>NodeContext.goal_context</code> 进入 prompt；<code>to_prompt_context()</code> 只对非 stub Goal 生效。worker 共享 colony Goal；overseer 拿 <code>goal</code> 但 <code>goal_context</code> 为空。</li>
<li><b>完成</b>：<code>is_success()</code> 无生产调用；<code>GoalStatus</code> 无自动转换；<code>OutcomeAggregator.evaluate_goal_progress</code> 是 stub，<code>get_goal_progress()</code> 恒返回 0.0 / 空 criteria；没有 <code>goal_achieved</code> 生产事件，也不会自动停 colony。</li>
<li><b>结果</b>：<code>schemas.Goal</code> 主要是声明式目标 + prompt 上下文 + 评估数据模型，不是自动工作流状态机；colony 完成后继续存在，除非显式 stop/delete。</li>
</ul>
<p class="sub">Go 扩展：<code>internal/agentloop/goal_eval.go::EvaluateGoal</code> 与 <code>internal/colony/goal_outcome.go</code> 增加了 criteria 执行、<code>goal_progress</code> / <code>goal_evaluation</code> / improvement signals；这是 Go 侧扩展并标了 <code>Parity-exempt</code>，不能把 Python 上游说成已有同样执行闭环。代码锚点：<code>schemas/goal.py::Goal.is_success</code>、<code>host/outcome_aggregator.py::OutcomeAggregator.evaluate_goal_progress</code>、<code>host/agent_host.py::AgentHost.get_goal_progress</code>、<code>agent_loop/agent_loop.py</code> 的 <code>goal_context</code> 拼接、<code>host/colony_runtime.py</code> 的 <code>_build_worker</code>/<code>start_overseer</code>。</p>

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
</div>
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
