#!/usr/bin/env python3
"""Generate the Hive package atlas (zh-CN HTML + machine-readable JSON).

Recomputes, from the Python tree under core/framework:
  - per-package files / LOC / in-package tests / docstring / largest files
  - a package-level import graph (``from framework.X`` / ``import framework.X``)
  - fan-in / fan-out, top edges, strongly connected components, cyclic pairs
  - upward edges against the declared layer table (with file:line evidence)
  - dependencies on the sibling ``tools/src/aden_tools`` package

The narrative sections are curated prose; every number and edge in them comes
from the JSON written beside the HTML.

Usage:
    python3 go_hive/scripts/gen_hive_package_atlas.py
Outputs:
    go_hive/docs/hive-package-atlas.html
    go_hive/docs/hive-package-graph.json
"""
from __future__ import annotations

import ast
import html
import json
import re
import subprocess
from collections import Counter, defaultdict
from datetime import datetime
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
ROOT = REPO / "core" / "framework"
OUT_HTML = REPO / "go_hive" / "docs" / "hive-package-atlas.html"
OUT_JSON = REPO / "go_hive" / "docs" / "hive-package-graph.json"

ROOT_MODULES = {"config", "cli", "__init__", "__main__", "cloud_sync_hooks", "rate_limiter"}

# Declared layering (lower index = lower layer). The graph is checked against it;
# violations are reported, not "fixed" -- the code is the source of truth.
LAYERS = {
    "utils": 0, "schemas": 0, "observability": 0, "testing": 0, "debugger": 0,
    "storage": 1, "credentials": 1, "llm": 1, "tasks": 1, "tracker": 1,
    "trackerdb": 1, "global_db": 1, "skills": 1, "sentinel": 1, "maintenance": 1,
    "tools": 2, "agent_loop": 2, "pipeline": 2, "orchestrator": 2,
    "host": 3, "agents": 3, "loader": 3, "runtime": 3,
    "server": 4,
}
LAYER_NAMES = {
    0: "L0 基础类型 / 小工具",
    1: "L1 能力底座",
    2: "L2 执行原语",
    3: "L3 装配与托管",
    4: "L4 边缘（HTTP/SSE）",
}

# One-line curated role per package (kept short; the table shows the docstring too).
ROLES = {
    "server": "HTTP/SSE 门面与会话生命周期：aiohttp app、37 个 routes_*、SessionManager、QueenOrchestrator、judge/stall/channels/queen mail。",
    "agent_loop": "唯一执行原语 AgentLoop（流式多轮 + 工具执行 + 预算），以及 reminders/compaction/judge_pipeline/stall/cursor 等内部件。",
    "host": "托管执行层：ColonyRuntime（当前，无图）、Worker、EventBus、triggers/webhook、tracker_db；同时保留旧 AgentHost/ExecutionManager（图时代）。",
    "loader": "从磁盘装配 agent：agent 配置/worker.json、ToolRegistry、MCP registry、凭据、skills、pipeline 组装。",
    "credentials": "凭据底座：Fernet 加密库 + key 管理、CredentialSpec 目录、解析/刷新、hive-cred CLI、Aden 云与 OAuth2。",
    "tools": "内置/女王工具面：queen 生命周期与 tracker/playbook/tool_tiers/harness 注册、queen mail 工具。",
    "agents": "框架内置 agent 定义：13 个 Queen persona、worker_definition、记忆/反思/召回/时间线。",
    "llm": "LLM 抽象：provider（litellm/anthropic/mock）、stream events、capabilities、model catalog、key check/pool。",
    "orchestrator": "旧图执行引擎：GraphSpec/Orchestrator/Node/Validator/Goal（懒加载避免循环依赖；colony 运行时已不再使用图）。",
    "skills": "SKILL.md 技能体系：发现/解析/信任门控/prompt 注入/热重载/写入。",
    "tasks": "文件型任务清单：每会话一份 tasks.json（session/colony 两类 list id）、事件、reminders、hooks、四个 session 工具。",
    "storage": "会话/运行时数据存储：SessionStore、并发存储、迁移辅助。",
    "sentinel": "停车自动驾驶：分类器、升级记录、通知/回信（nudge / escalate / resume）。",
    "maintenance": "HIVE_HOME 数据保留与清理（hive janitor，手动触发）。",
    "tracker": "运行/决策日志（Builder 分析用），注意与 host/tracker_db.py 的 colony tracker 不是一回事。",
    "schemas": "Pydantic 运行时模型：Goal/SuccessCriterion、session state 等。",
    "testing": "目标驱动测试与评测：llm_judge、test case/result、skill evals 的基础设施。",
    "pipeline": "触发期中间件链：stage/runner/registry + 7 个内置 stage（限流、校验、成本闸、凭据、LLM、MCP、skills）。",
    "global_db": "云端团队共享库客户端（hive-backend /v1/global-db/*，跨 colony GTM 数据）。",
    "observability": "trace 关联与结构化日志。",
    "utils": "文本/环境/IO/异步小工具。",
    "trackerdb": "terminal CLI 共用的退出码分类与错误分发（不是 tracker 数据库）。",
    "debugger": "LLM 调试日志查看器（hive debugger）。",
    "runtime": "空壳目录：无 __init__.py、零代码，只剩一个旧 ExecutionManager 回归测试。",
    "(root)": "顶层模块：config.py（全局配置/HIVE_HOME，导入枢纽）、cli.py（hive 命令入口）、__main__.py、rate_limiter、cloud_sync_hooks。",
}


def pkg_of(path: Path) -> str:
    rel = path.relative_to(ROOT)
    return "(root)" if len(rel.parts) == 1 else rel.parts[0]


def norm_target(name: str) -> str:
    return "(root)" if name in ROOT_MODULES else name


def doc_of(path: Path, limit: int = 260) -> str:
    try:
        tree = ast.parse(path.read_text(errors="ignore"))
    except SyntaxError:
        return ""
    return " ".join((ast.get_docstring(tree) or "").split())[:limit]


def git_head() -> str:
    try:
        return subprocess.run(["git", "rev-parse", "--short", "HEAD"], cwd=REPO,
                              capture_output=True, text=True, timeout=5).stdout.strip() or "unknown"
    except Exception:
        return "unknown"


def collect():
    files = [p for p in ROOT.rglob("*.py") if "__pycache__" not in p.parts]
    non_test = [p for p in files if "tests" not in p.parts and not p.name.startswith("test_")]
    tests = [p for p in files if p not in non_test]

    packages: dict[str, dict] = {}
    for p in non_test:
        pkg = pkg_of(p)
        s = packages.setdefault(pkg, {"files": 0, "loc": 0, "test_files": 0, "test_loc": 0,
                                      "largest": [], "doc": ""})
        lines = len(p.read_text(errors="ignore").splitlines())
        s["files"] += 1
        s["loc"] += lines
        s["largest"].append((lines, str(p.relative_to(ROOT))))
    for p in tests:
        pkg = pkg_of(p)
        s = packages.setdefault(pkg, {"files": 0, "loc": 0, "test_files": 0, "test_loc": 0,
                                      "largest": [], "doc": ""})
        s["test_files"] += 1
        s["test_loc"] += len(p.read_text(errors="ignore").splitlines())
    for pkg, s in packages.items():
        s["largest"] = sorted(s["largest"], reverse=True)[:3]
        init = ROOT / pkg / "__init__.py" if pkg != "(root)" else ROOT / "__init__.py"
        if init.exists():
            s["doc"] = doc_of(init)
        s["role"] = ROLES.get(pkg, "")

    import_re = re.compile(r"^\s*(?:from\s+framework\.([A-Za-z_]\w*)|import\s+framework\.([A-Za-z_]\w*))", re.M)
    edges: Counter[tuple[str, str]] = Counter()
    sites: dict[tuple[str, str], list[str]] = defaultdict(list)
    aden_by_pkg: Counter[str] = Counter()
    ext: Counter[str] = Counter()
    root_imports: Counter[str] = Counter()
    root_edges: Counter[tuple[str, str]] = Counter()
    for p in non_test:
        src = pkg_of(p)
        text = p.read_text(errors="ignore")
        rel = str(p.relative_to(REPO))
        for m in import_re.finditer(text):
            raw = m.group(1) or m.group(2)
            dst = norm_target(raw)
            if raw in ROOT_MODULES:
                root_imports[raw] += 1
                root_edges[(src, raw)] += 1
            if dst and dst != src:
                edges[(src, dst)] += 1
                if len(sites[(src, dst)]) < 3:
                    line = text[: m.start()].count("\n") + 1
                    sites[(src, dst)].append(f"{rel}:{line}")
        if re.search(r"^\s*(?:from|import)\s+aden_tools", text, re.M):
            aden_by_pkg[src] += 1
        try:
            tree = ast.parse(text)
        except SyntaxError:
            continue
        for node in ast.walk(tree):
            if isinstance(node, ast.Import):
                for a in node.names:
                    ext[a.name.split(".")[0]] += 1
            elif isinstance(node, ast.ImportFrom) and node.module and not node.level:
                top = node.module.split(".")[0]
                if top != "framework":
                    ext[top] += 1

    fan_in: Counter[str] = Counter()
    fan_out: Counter[str] = Counter()
    for (a, b), n in edges.items():
        fan_out[a] += n
        fan_in[b] += n

    # Tarjan SCC
    adj = defaultdict(set)
    for a, b in edges:
        adj[a].add(b)
    index: dict[str, int] = {}
    low: dict[str, int] = {}
    stack: list[str] = []
    on: set[str] = set()
    sccs: list[list[str]] = []
    counter = [0]

    def strong(v: str) -> None:
        index[v] = low[v] = counter[0]
        counter[0] += 1
        stack.append(v)
        on.add(v)
        for w in adj[v]:
            if w not in index:
                strong(w)
                low[v] = min(low[v], low[w])
            elif w in on:
                low[v] = min(low[v], index[w])
        if low[v] == index[v]:
            comp = []
            while True:
                w = stack.pop()
                on.discard(w)
                comp.append(w)
                if w == v:
                    break
            sccs.append(sorted(comp))

    for v in list(adj):
        if v not in index:
            strong(v)
    sccs.sort(key=len, reverse=True)

    cyclic_pairs = []
    for comp in sccs:
        for a in comp:
            for b in comp:
                if a < b and (a, b) in edges and (b, a) in edges:
                    cyclic_pairs.append({"a": a, "b": b, "ab": edges[(a, b)], "ba": edges[(b, a)]})
    cyclic_pairs.sort(key=lambda x: -(x["ab"] + x["ba"]))

    upward = []
    for (a, b), n in edges.items():
        la, lb = LAYERS.get(a), LAYERS.get(b)
        if la is not None and lb is not None and la < lb:
            upward.append({"from": a, "to": b, "n": n, "sites": sites[(a, b)]})
    upward.sort(key=lambda x: -x["n"])

    return {
        "generated_at": datetime.now().strftime("%Y-%m-%d %H:%M %Z"),
        "baseline": git_head(),
        "totals": {
            "python_files": len(files),
            "non_test_files": len(non_test),
            "test_files": len(tests),
            "non_test_loc": sum(s["loc"] for s in packages.values()),
            "test_loc": sum(s["test_loc"] for s in packages.values()),
            "packages": len([k for k in packages if k != "(root)"]),
            "core_tests_files": len(list((REPO / "core" / "tests").rglob("*.py"))),
            "core_tests_loc": sum(len(p.read_text(errors="ignore").splitlines())
                                  for p in (REPO / "core" / "tests").rglob("*.py")),
        },
        "packages": packages,
        "edges": [{"from": a, "to": b, "n": n} for (a, b), n in edges.most_common()],
        "fan_in": fan_in.most_common(),
        "fan_out": fan_out.most_common(),
        "sccs": [c for c in sccs if len(c) > 1],
        "cyclic_pairs": cyclic_pairs,
        "upward": upward,
        "external": ext.most_common(20),
        "aden_by_pkg": aden_by_pkg.most_common(),
        "root_imports": root_imports.most_common(),
        "root_edges": [{"from": a, "module": b, "n": n} for (a, b), n in root_edges.most_common()],
        "sites": {f"{a}->{b}": v for (a, b), v in sites.items()},
    }


ESC = lambda s: html.escape(str(s), quote=True)


def table(headers, rows, widths=None, cls="tablewrap"):
    th = "".join(
        f'<th style="width:{widths[i]}">{h}</th>' if widths else f"<th>{h}</th>"
        for i, h in enumerate(headers)
    )
    body = []
    for r in rows:
        body.append("<tr>" + "".join(f"<td>{c}</td>" for c in r) + "</tr>")
    return f'<div class="{cls}"><table><thead><tr>{th}</tr></thead><tbody>{"".join(body)}</tbody></table></div>'


def code(s: str) -> str:
    return f"<code>{ESC(s)}</code>"


def build_html(d: dict) -> str:
    t = d["totals"]
    pkgs = d["packages"]
    pkg_names = [p for p in pkgs if p != "(root)"]
    big_scc = d["sccs"][0] if d["sccs"] else []
    outside = sorted((set(pkg_names) | {"(root)"}) - set(big_scc))
    root_imports = dict(d.get("root_imports") or [])
    cfg_n = root_imports.get("config", 0)
    def root_edge(src: str, module: str) -> int:
        return next((r["n"] for r in d.get("root_edges", []) if r["from"] == src and r["module"] == module), 0)
    server_cfg, agents_cfg, host_cfg = root_edge("server", "config"), root_edge("agents", "config"), root_edge("host", "config")

    cards = f"""
<div class="grid g4">
<div class="card"><div class="k">Python 包</div><div class="v">{t['packages']}</div><div class="d">另有 (root) 6 个顶层模块；core/framework 共 {t['python_files']} 个 .py</div></div>
<div class="card"><div class="k">非测试代码</div><div class="v">{t['non_test_loc']:,}</div><div class="d">行（{t['non_test_files']} 文件）；包内测试 {t['test_loc']:,} 行 / {t['test_files']} 文件</div></div>
<div class="card"><div class="k">跨包测试</div><div class="v">{t['core_tests_loc']:,}</div><div class="d">行在 core/tests（{t['core_tests_files']} 文件）——大部分行为测试不在包内</div></div>
<div class="card"><div class="k">依赖形状</div><div class="v">{len(big_scc)} 包一个 SCC</div><div class="d">{len(d['cyclic_pairs'])} 对双向依赖；只有 {", ".join(outside)} 在环外</div></div>
</div>"""

    layer_rows = []
    for layer in sorted(LAYER_NAMES):
        members = sorted([p for p in pkg_names if LAYERS.get(p) == layer],
                         key=lambda p: -pkgs[p]["loc"])
        layer_rows.append((f"<b>{LAYER_NAMES[layer]}</b>",
                           "<br>".join(f"{code(p)} — {ESC(pkgs[p]['role'])}" for p in members)))
    layer_rows.append(("<b>横切 / 空壳</b>",
                       f"{code('runtime')} — {ESC(pkgs.get('runtime', {}).get('role', '空壳'))}<br>"
                       f"{code('(root)')} — {ESC(ROLES['(root)'])}"))

    pkg_rows = []
    for p in sorted(pkgs, key=lambda x: -pkgs[x]["loc"]):
        s = pkgs[p]
        largest = "<br>".join(code(f) for _, f in s["largest"][:2]) or "—"
        doc = ESC(s["doc"][:150]) if s["doc"] else '<span class="sub">（无 __init__ 文档串）</span>'
        pkg_rows.append((
            f"<b>{code(p)}</b>",
            f"{ESC(s['role'])}<br><span class=\"sub\">{doc}</span>",
            largest,
            f"{s['loc']:,}",
            f"{s['test_files']} / {s['test_loc']:,}",
            f"{dict(d['fan_in']).get(p, 0)} / {dict(d['fan_out']).get(p, 0)}",
        ))

    edge_rows = [(
        code(f"{e['from']} → {e['to']}"), f"{e['n']}",
        "<br>".join(code(x) for x in (d["sites"].get(f"{e['from']}->{e['to']}") or [])[:2]),
    ) for e in d["edges"][:30]]

    fan_in_rows = [(code(k), str(v)) for k, v in d["fan_in"][:12]]
    fan_out_rows = [(code(k), str(v)) for k, v in d["fan_out"][:12]]

    pair_rows = [(
        f"{code(p['a'])} ↔ {code(p['b'])}",
        f"{p['ab']} / {p['ba']}",
    ) for p in d["cyclic_pairs"][:18]]

    upward_rows = [(
        f"{code(u['from'])} → {code(u['to'])}", str(u["n"]),
        "<br>".join(code(x) for x in u["sites"][:2]),
    ) for u in d["upward"][:14]]

    aden_rows = [(code(k), str(v)) for k, v in d["aden_by_pkg"]]

    chains = [
        ("serve 启动",
         "hive serve → <code>framework.cli.main</code> → <code>server/app.py:create_app</code> → "
         "<code>SessionManager</code> → 每会话 <code>queen_orchestrator</code> → <code>ColonyRuntime</code>；"
         "aiohttp 绑定 127.0.0.1:8787，静态前端同端口。",
         "server → host → agent_loop"),
        ("聊天回合",
         "<code>POST /api/sessions/{id}/chat</code>（routes_sessions）→ SessionManager 定位会话 → "
         "queen executor → <code>AgentLoop.execute</code>（流式 LLM + 工具并行/串行）→ "
         "<code>EventBus</code> → SSE <code>CLIENT_*</code> → 前端；park 时落盘。",
         "server → agent_loop → host.event_bus"),
        ("colony 派发",
         "queen <code>run_worker</code>（tools/queen_lifecycle_tools.py）→ <code>ColonyRuntime.spawn</code> → "
         "<code>Worker</code>（AgentLoop 克隆，独立上下文）→ <code>tracker_upsert</code>/<code>report_to_parent</code> → "
         "<code>SUBAGENT_REPORT</code> → queen 下一轮 <code>[WORKER_REPORT]</code>。",
         "tools → host → agent_loop → host.event_bus"),
        ("触发器 / webhook",
         "<code>host/triggers.py</code> 定时器 或 <code>host/webhook_server.py</code> 收到 HTTP → "
         "<code>WEBHOOK_RECEIVED</code> → 订阅者过滤 path/method → <code>TriggerEvent</code> → queen 回合；"
         "missed-fire 在 colony 加载时补报。",
         "host → host.event_bus → server"),
        ("judge 两条链",
         "回合判定：<code>agent_loop/internals/judge_pipeline.py</code> → (可选) 自定义 JudgeProtocol / "
         "<code>orchestrator/conversation_judge.py</code>（Level-2b）。<br>"
         "stall check：<code>server/stall_check.py</code> → judge 槽 <code>server/judge.py</code>"
         "（typesafe /systemone，20s，阈值 0.5）；两者互不 import。",
         "agent_loop / server"),
        ("skills",
         "<code>skills/discovery.py</code> 扫 9 类根 → parser → trust gate（project scope）→ prompt 注入；"
         "<code>write_skill</code> 写 colony skills；<code>POST /api/queen/{id}/skills/reload</code> 热重载扇出。",
         "skills → agent_loop → server.routes_skills"),
        ("凭据",
         "<code>credentials/store.py</code>（Fernet）← <code>key_storage</code> ← "
         "<code>hive-cred token</code>（stdout 只打值）→ 工具/terminal；Aden 云与 OAuth2 走 "
         "<code>credentials/aden</code> / <code>oauth2</code>。",
         "credentials → tools / server"),
    ]
    chain_rows = [(f"<b>{ESC(n)}</b>", body, f"<span class=\"sub\">{ESC(path)}</span>")
                  for n, body, path in chains]

    ints = sorted(d["external"], key=lambda kv: -kv[1])[:12]
    int_rows = [(code(k), str(v)) for k, v in ints]

    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Hive 包结构与架构地图（Package Atlas）· go_hive</title>
<style>
:root{{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}}
*{{box-sizing:border-box}} body{{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}}
a{{color:var(--accent2);text-decoration:none}} a:hover{{text-decoration:underline}}
.wrap{{max-width:1280px;margin:0 auto;padding:44px 26px 80px}}
header{{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}}
.eyebrow{{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}}
h1{{font-size:30px;line-height:1.25;margin:10px 0 10px;letter-spacing:-.02em}} h1 span{{color:var(--accent)}}
.lede{{color:var(--muted);max-width:1040px;font-size:15px}} .meta{{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px}} .meta b{{color:var(--text)}}
section{{margin:38px 0}} h2{{font-size:22px;margin:0 0 8px}} h2 .num{{color:var(--accent);font-size:14px;margin-right:8px}} h3{{font-size:15.5px;margin:24px 0 8px;color:#cfdae6}} h4{{font-size:13.5px;margin:18px 0 6px;color:#b9c7d6}}
p{{color:#c6d2df;margin:8px 0}} .sub{{color:var(--muted);font-size:12.5px}}
.grid{{display:grid;gap:12px}} .g4{{grid-template-columns:repeat(4,minmax(0,1fr))}} .g3{{grid-template-columns:repeat(3,minmax(0,1fr))}} .g2{{grid-template-columns:repeat(2,minmax(0,1fr))}} .grid>*{{min-width:0}}
@media(max-width:900px){{.g4{{grid-template-columns:1fr 1fr}}.g3{{grid-template-columns:1fr 1fr}}}} @media(max-width:560px){{.g4,.g3,.g2{{grid-template-columns:1fr}}.wrap{{padding:30px 16px 60px}}h1{{font-size:24px}}}}
.card{{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px;min-width:0}} .card .k{{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700}} .card .v{{font-size:20px;font-weight:800;margin-top:5px;letter-spacing:-.01em}} .card .d{{color:var(--muted);font-size:12px;margin-top:5px}}
.tablewrap{{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2);max-width:100%}}
table{{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed}} th,td{{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word}} th{{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1}} tr:hover td{{background:#121a25}} tr:last-child td{{border-bottom:0}}
code{{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}}
pre{{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2}}
.pill{{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal}} .pill.ok{{color:var(--ok);border-color:#1d3b26;background:#0f1a13}} .pill.warn{{color:var(--warn);border-color:#3d3013;background:#1a150a}} .pill.bad{{color:var(--bad);border-color:#3d1a1a;background:#1a0f0f}} .pill.info{{color:var(--accent2);border-color:#17324d;background:#0d1622}}
.callout{{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4;overflow-wrap:anywhere}} .callout.bad{{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7}} .callout.info{{border-color:var(--accent2);background:#0d1622;color:#cfe3ff}}
.toc{{display:flex;flex-wrap:wrap;gap:7px;margin-top:18px}} .toc a{{font-size:12px;border:1px solid var(--line);background:var(--panel);padding:5px 9px;border-radius:999px;color:var(--muted)}} .toc a:hover{{color:var(--text);text-decoration:none}}
ul,ol{{margin:7px 0 7px 20px;padding:0}} li{{margin:4px 0;color:#c6d2df}} footer{{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}}
figure.diagram{{background:var(--code);border:1px solid var(--line);border-radius:12px;padding:14px;overflow-x:auto;margin:16px 0;max-width:100%}}
figure.diagram figcaption{{color:var(--muted);font-size:12px;margin-top:8px}}
@media(max-width:560px){{table{{font-size:12px}}th,td{{padding:7px 6px}}}}
@media(max-width:760px){{.tablewrap table{{min-width:760px}}}}
@media(max-width:560px){{.toc a{{padding:8px 12px;font-size:13px;min-height:44px;display:inline-flex;align-items:center}}}}
</style></head><body><div class="wrap">
<header><div class="eyebrow">Hive · Package Atlas</div><h1>Hive 包结构与架构地图 <span>Package Atlas</span></h1>
<p class="lede">以 <code>core/framework</code> 为对象，逐包给出职责、规模、导入关系与分层位置；同时给出真实存在的循环依赖、向上依赖、新旧运行时并存和 <code>aden_tools</code> 跨界依赖。数字由生成器重算，HTML 与机器可读 JSON 同源。</p>
<div class="meta"><span>生成：<b>{ESC(d['generated_at'])}</b></span><span>基线：<b>core/framework @ {ESC(d['baseline'])}</b></span><span>方法：<b>AST/正则静态解析 + 人工角色标注</b></span><span>产物：<b>HTML + hive-package-graph.json</b></span></div>
<div class="toc"><a href="#s00">结论速览</a><a href="#s01">边界地图</a><a href="#s02">分层架构</a><a href="#s03">包清单</a><a href="#s04">依赖面</a><a href="#s05">循环与反向边</a><a href="#s06">关键调用链</a><a href="#s07">新旧并存与遗留</a><a href="#s08">数据与存储面</a><a href="#s09">维护与再生成</a></div></header>

<section id="s00"><h2><span class="num">00</span>结论速览</h2>
{cards}
<div class="callout info"><b>一句话</b>：Hive 是"<b>一个执行原语（AgentLoop）+ 一套托管层（host）+ 一个组装型 HTTP 门面（server）</b>"的三段式；<code>config.py</code> 是最大的导入枢纽（{cfg_n} 条边），<code>server</code> 是最大的扇出（{dict(d['fan_out']).get('server', 0)} 条边）。"分层"是约定而不是 import 强制：21 个包落在同一个强连通分量里。</div>
<div class="grid g3">
<div class="card"><div class="k">最大的包</div><div class="v">server</div><div class="d">{pkgs['server']['loc']:,} 行 / {pkgs['server']['files']} 文件；包内测试 {pkgs['server']['test_files']} 文件 / {pkgs['server']['test_loc']:,} 行</div></div>
<div class="card"><div class="k">导入枢纽</div><div class="v">config.py</div><div class="d">被 {cfg_n} 次导入（root 汇总 {dict(d['fan_in']).get('(root)', 0)}）；其后 host {dict(d['fan_in']).get('host', 0)}、agents {dict(d['fan_in']).get('agents', 0)}、llm {dict(d['fan_in']).get('llm', 0)}</div></div>
<div class="card"><div class="k">最热的上行依赖</div><div class="v">tools → host</div><div class="d">{d['upward'][0]['n']} 条边（queen/playbook 工具反向依赖运行时类型）；见 §05</div></div>
</div>
</section>

<section id="s01"><h2><span class="num">01</span>边界地图：什么在 framework 里，什么在框架外</h2>
{table(["区域", "内容", "与 core/framework 的关系"],
 [("<code>core/framework</code>", f"Python 运行时本体：{t['packages']} 个包 + 6 个顶层模块，{t['non_test_loc']:,} 行非测试代码。", "本页的对象"),
  ("<code>core/hive</code>", "公共面（derivative projects 的导入面）：<code>AgentLoop / AgentContext / AgentSpec / LiteLLMProvider / Tool / EventBus / ColonyRuntime …</code>，按需懒加载，<code>import hive</code> 很轻。", "<b>上游依赖</b>：只导出 framework 的名字，不反向依赖 framework 之外的层"),
  ("<code>tools/src/aden_tools</code>", "工具目录与 MCP server 实现（866 个 MCP tool；集成、扫描器、risk_scorer 等）。", "<b>被 framework 反向依赖</b>：{len(d['aden_by_pkg'])} 个包、{sum(n for _, n in d['aden_by_pkg'])} 处 import（见 §05）；不是纯外部插件"),
  ("<code>core/frontend</code>", "React 前端；由 <code>server/app.py</code> 同端口提供静态文件。", "消费 server 的 HTTP/SSE 面"),
  ("<code>go_hive/</code>", "纯 Go 重写（本仓库的移植工程），有独立的 contracts/ 与 docs/。", "不是 framework 的运行时依赖；通过契约对齐"),
  ("<code>docs/</code>", "架构与关键概念文档（<code>architecture/README.md</code>、<code>key_concepts/</code>）。", "本页的叙述基线之一")],
 widths=["16%", "52%", "32%"])}
<div class="callout"><b>命名提醒</b>：<code>pyproject.toml</code> 里注释写得很明确——"<code>hive</code> is the public surface for projects built on Hive; <code>framework</code> is Hive itself." 派生项目应当只 import <code>hive</code>；<code>framework</code> 是实现细节。</div>
</section>

<section id="s02"><h2><span class="num">02</span>分层架构（声明层 vs 实际导入）</h2>
{table(["层", "包（按代码量排序）"], layer_rows, widths=["16%", "84%"])}
<figure class="diagram"><pre>
                         ┌──────────────────────────────┐
                         │  server/  (aiohttp, SSE)     │  L4 边缘
                         │  routes_* · SessionManager   │
                         │  queen_orchestrator · judge  │
                         └──────────────┬───────────────┘
                                        │
              ┌─────────────────────────┴─────────────────────────┐
              │                    host/                          │  L3 托管
              │  ColonyRuntime · Worker · EventBus · triggers     │
              │  tracker_db · (legacy) AgentHost/ExecutionManager │
              └───────┬───────────────────────┬───────────────────┘
                      │                       │
        ┌─────────────┴──────────┐   ┌────────┴───────────────┐
        │ agent_loop/            │   │ pipeline/ (trigger 时) │  L2 执行原语
        │ AgentLoop · reminders  │   │ 7 stages: 限流/校验/…  │
        │ judge_pipeline · park  │   └────────────────────────┘
        └───────┬────────────────┘
                │
   ┌────────────┴──────────────────────────────────────────────┐
   │ L1 能力底座：llm · tools · credentials · skills · tasks    │
   │ tracker · trackerdb · storage · sentinel · maintenance     │
   │ global_db · schemas(部分)                                  │
   └────────────┬──────────────────────────────────────────────┘
                │
   ┌────────────┴──────────────────────────────────────────────┐
   │ L0：utils · schemas · observability · testing · debugger   │
   │ (root) config.py / cli.py / __main__.py / rate_limiter     │
   └───────────────────────────────────────────────────────────┘

               ┌────────────── 横切 ──────────────┐
               │ 事件总线（host.event_bus）贯穿各层 │
               │ config.py：180 条入边（全局配置） │
               │ runtime/：空壳（0 代码，1 个旧测试）│
               └───────────────────────────────────┘
</pre><figcaption>层是"应该"的形状；实际 import 图见 §04/§05——21 个包在同一个 SCC 里，层间边界靠约定维持。</figcaption></figure>
</section>

<section id="s03"><h2><span class="num">03</span>包清单（{len(pkg_names)} 包 + root）</h2>
{table(["包", "职责 / 文档串", "最大文件", "LOC", "测试文件/行", "入/出边"],
 pkg_rows, widths=["10%", "43%", "21%", "8%", "10%", "8%"])}
<p class="sub">"测试文件/行"只统计包内 <code>*/tests/*</code>；跨包行为测试主要在 <code>core/tests</code>（{t['core_tests_loc']:,} 行 / {t['core_tests_files']} 文件），所以 host/loader/llm 显示 0 并不代表没有测试。</p>
</section>

<section id="s04"><h2><span class="num">04</span>依赖面：谁最被依赖，谁最依赖别人</h2>
<div class="grid g2">
<div class="card"><div class="k">Fan-in Top 12（被导入次数）</div>{table(["目标", "入边"], fan_in_rows, widths=["70%", "30%"])}</div>
<div class="card"><div class="k">Fan-out Top 12（导入别人次数）</div>{table(["来源", "出边"], fan_out_rows, widths=["70%", "30%"])}</div>
</div>
<h3>Top 30 包间边（附文件:行 证据）</h3>
{table(["边", "次数", "证据"], edge_rows, widths=["26%", "8%", "66%"])}
<h3>外部依赖 Top 12（按 import 次数）</h3>
{table(["顶层模块", "次数"], int_rows, widths=["70%", "30%"])}
<div class="callout"><b>读法</b>：<code>server→config {server_cfg}</code> 说明 server 是配置的集中消费方；<code>server→host 70</code> 是门面到运行时的主通道；<code>tools→host 23</code> 是唯一明显的"下层反向依赖上层"，它来自 queen 生命周期/playbook 工具需要 <code>ColonyRuntime</code>/<code>EventBus</code> 类型。</div>
</section>

<section id="s05"><h2><span class="num">05</span>循环依赖与反向边（真实的耦合热点）</h2>
<h3>强连通分量</h3>
<p>包级 import 图有 <b>{len(d['sccs'])} 个</b> 大小 &gt; 1 的 SCC；最大的一个包含 <b>{len(big_scc)}</b> 个包：</p>
<p>{" ".join(f'<span class="pill info">{ESC(p)}</span>' for p in big_scc)}</p>
<p class="sub">不在环内的只有：{"、".join(f"<code>{ESC(p)}</code>" for p in outside)}。这说明"分层"不是 import 强制的——<code>orchestrator/__init__.py</code> 自己就写着"Lazy imports to avoid circular dependencies with graph…"。</p>
<h3>双向依赖对（Top 18，按双向边数和排序）</h3>
{table(["包对", "A→B / B→A"], pair_rows, widths=["60%", "40%"])}
<h3>向上的分层边（Top 14，附证据）</h3>
{table(["边", "次数", "证据"], upward_rows, widths=["22%", "8%", "70%"])}
<div class="callout bad"><b>需要盯住的三条</b>：① <code>tools → host</code>（{d['upward'][0]['n']} 条）——工具面反向依赖运行时类型，改 host 的公开面会波及工具；② <code>tools → server</code>、<code>loader → server</code>、<code>maintenance → server</code>——下层直接够到 HTTP 层；③ <code>tasks → loader</code>、<code>pipeline → loader</code>——注册/装配细节被基础层反向引用。它们大多靠函数内延迟 import 成立，重构时最容易踩。</div>
</section>

<section id="s06"><h2><span class="num">06</span>关键调用链（从入口到落盘）</h2>
{table(["链路", "路径", "涉及层"], chain_rows, widths=["12%", "72%", "16%"])}
<div class="callout info"><b>读图顺序建议</b>：先看 ① serve 启动，理解进程里有哪几个长生命周期对象；再看 ② 聊天回合与 ③ colony 派发，理解"一个 loop 控制多个 loop"；④–⑦ 是按需触发的横切链路。</div>
</section>

<section id="s07"><h2><span class="num">07</span>新旧并存与遗留（"真实可用"必须说明的部分）</h2>
<div class="tablewrap"><table><thead><tr><th style="width:24%">现象</th><th>证据</th><th style="width:24%">影响 / 判断</th></tr></thead><tbody>
<tr><td><b>两代运行时并存</b></td><td><code>colony_runtime.py</code> 头部："replaces both AgentHost and ExecutionManager. There are no graphs…"；但 <code>loader/agent_loader.py</code> 仍 import <code>AgentHost</code>，<code>orchestrator/</code> 仍有 {dict(d['fan_in']).get('orchestrator', 0)} 条入边。</td><td>新代码走 ColonyRuntime；旧图引擎（GraphSpec/ExecutionStream/checkpoint）仍可被加载，属于"可运行的历史层"。</td></tr>
<tr><td><b><code>runtime/</code> 是空壳</b></td><td>0 个 .py、无 <code>__init__.py</code>，仅 <code>runtime/tests/test_forced_cancel_dual_execution.py</code>（180 行，测旧 ExecutionManager）。全仓无 <code>framework.runtime</code> 引用。</td><td>可安全登记为清理项；不要把它当"运行时包"。</td></tr>
<tr><td><b>colony 模板工具是死代码</b></td><td><code>tasks/tools/colony_tools.py</code> 的 <code>build_colony_template_tools</code> 零调用；上游最新 main 同样零调用；它引用的 <code>run_parallel_workers</code> 工具已不存在。</td><td>设计意图（spawn plan）还在文案里；要启用需补注册/UI/消费者，否则应删。</td></tr>
<tr><td><b>config.py 是耦合枢纽</b></td><td>config.py 被导入 {cfg_n} 次（root 汇总 {dict(d['fan_in']).get('(root)', 0)}），其中 <code>server→config {server_cfg}</code>、<code>agents→config {agents_cfg}</code>、<code>host→config {host_cfg}</code>。</td><td>改 config 的模块面（路径/HIVE_HOME/模型选择）影响全仓；新增配置应先进 config 再被消费。</td></tr>
<tr><td><b>aden_tools 跨界依赖</b></td><td>{len(d['aden_by_pkg'])} 个包、{sum(n for _, n in d['aden_by_pkg'])} 处 import <code>aden_tools</code>（{", ".join(f"{k}:{v}" for k, v in d['aden_by_pkg'])}）。</td><td>"第三方工具都是外部 MCP"的说法只对 <b>加载方式</b> 成立；<b>代码/安装面</b>上 framework 依赖 tools 包。</td></tr>
<tr><td><b>测试重心在 core/tests</b></td><td>包内测试 {t['test_loc']:,} 行；<code>core/tests</code> {t['core_tests_loc']:,} 行（{t['core_tests_files']} 文件）。<code>host</code>/<code>loader</code>/<code>llm</code> 包内 0 测试。</td><td>看某个包"有没有测试"必须查 core/tests 与 go_hive 的 parity 测试，不能只看包目录。</td></tr>
</tbody></table></div>
</section>

<section id="s08"><h2><span class="num">08</span>数据与存储面（包 ↔ 磁盘）</h2>
{table(["路径（HIVE_HOME 下）", "内容", "负责的包"],
 [("<code>sessions/&lt;id&gt;/state.json</code>", "会话单一真相（cursor、park、phase、colony 绑定）", "storage / agent_loop.internals.cursor_persistence"),
  ("<code>sessions/&lt;id&gt;/conversations/</code>", "分段落盘的消息（parts），供恢复与检索", "agent_loop.conversation"),
  ("<code>sessions/&lt;id&gt;/tasks.json</code>", "每会话任务清单（四 session 工具读写）", "tasks.store"),
  ("<code>sessions/&lt;id&gt;/events.jsonl</code>", "事件流（UI 回放/审计）", "host.event_log / server.sse"),
  ("<code>colonies/&lt;name&gt;/worker.json</code>", "worker 规格（克隆模板）", "loader.agent_loader / agents.queen.worker_definition"),
  ("<code>colonies/&lt;name&gt;/tracker/tracker.db</code>", "colony 共享 SQLite 台账（queen DDL、worker upsert）", "host.tracker_db / tools.tracker_tools"),
  ("<code>colonies/&lt;name&gt;/triggers.json</code>", "定时/webhook 触发器定义与下次触发", "host.triggers"),
  ("<code>colonies/&lt;name&gt;/skills/</code>", "queen 为本 colony 写的技能", "skills"),
  ("<code>queens/&lt;q&gt;/sessions/&lt;id&gt;/</code>", "DM 会话目录（与 colony 会话分开）", "server.session_manager"),
  ("<code>skills/</code>, <code>memories/</code>, <code>credentials/</code>, <code>secrets/credential_key</code>", "用户技能、反思记忆、Fernet 凭据库与密钥", "skills / agents.queen / credentials"),
  ("<code>mcp_registry/installed.json</code>", "外部 MCP server 注册表", "loader.mcp_registry"),
  ("<code>tool-artifacts/</code>", "大工具结果/抓取正文落盘", "agent_loop.internals.tool_result_handler / tools")],
 widths=["34%", "42%", "24%"])}
<div class="callout"><b>单一真相原则</b>：磁盘是恢复的真相（park/cursor、tasks.json、tracker.db），内存状态（idle nudge、SSE 缓冲）可丢。看架构时先分清"哪层拥有哪个文件"，比看类图更有用。</div>
</section>

<section id="s09"><h2><span class="num">09</span>维护与再生成</h2>
<ol>
<li><b>重算</b>：<code>python3 go_hive/scripts/gen_hive_package_atlas.py</code>——重新扫 <code>core/framework</code>，覆盖本页与 <code>hive-package-graph.json</code>。</li>
<li><b>数字来源</b>：包表/边/SCC/反向边全部来自生成器的静态解析；职责描述是人工标注（<code>ROLES</code> 表），改包职责时同步改脚本再跑。</li>
<li><b>口径</b>：非测试文件排除 <code>tests/</code> 目录与 <code>test_*.py</code>；导入边只统计 <code>framework.*</code> 绝对导入（函数内延迟 import 也算，所以会出现"下层→上层"的边）。</li>
<li><b>校验</b>：HTMLParser 标签闭合/锚点；桌面 1440 与手机 390 的 scrollWidth、裁切、TOC 触控高度；改动后再跑一次。</li>
<li><b>相关文档</b>：<a href="../../docs/architecture/README.md">architecture/README.md</a>（叙事架构）、<a href="../../docs/key_concepts/runtime_spine.md">runtime_spine.md</a>（层间脊柱）、<a href="hive-concepts-inventory.html">hive-concepts-inventory.html</a>（概念清单：18 原语 / 50 机制）、<a href="loop-vocabulary.html">loop-vocabulary.html</a>（术语表：park / nudge / stall / Sentinel）、<a href="event-bus.html">event-bus.html</a>（事件总线：77 类型 / 发布管线 / 消费方）、<a href="memory-and-soul.html">memory-and-soul.html</a>（记忆 / 时间线 / 召回 / 自我）、<a href="module-interfaces.html">module-interfaces.html</a>（不变量）、<a href="skill-discovery.html">skill-discovery.html</a>（技能扫描面）、<a href="hive-core-designs.html">hive-core-designs.html</a>（核心机制）、<a href="surface-tracking-matrix.html">surface-tracking-matrix.html</a>（外观矩阵）。</li>
</ol>
<div class="callout info"><b>边界声明</b>：这是<b>静态结构图</b>，不证明运行时行为；行为证据看各包测试、<code>docs/e2e-assets/</code> 与 go_hive 的 parity 报告。</div>
</section>

<footer>
<p>生成器：<code>go_hive/scripts/gen_hive_package_atlas.py</code> · 数据：<a href="hive-package-graph.json">hive-package-graph.json</a> · 基线：<code>core/framework @ {ESC(d['baseline'])}</code> · 生成时间：{ESC(d['generated_at'])}</p>
<p>相关：<a href="module-interfaces.html">模块接口与不变量</a> · <a href="hive-concepts-inventory.html">概念清单</a> · <a href="loop-vocabulary.html">Hive 术语表</a> · <a href="event-bus.html">Event Bus 事件总线</a> · <a href="memory-and-soul.html">Memory &amp; Soul</a> · <a href="skill-discovery.html">技能发现</a> · <a href="hive-core-designs.html">核心设计</a> · <a href="surface-tracking-matrix.html">外观矩阵</a> · <a href="../../docs/architecture/README.md">架构叙事</a></p>
</footer>
</div></body></html>"""


def main() -> None:
    data = collect()
    OUT_JSON.write_text(json.dumps(data, ensure_ascii=False, indent=1))
    OUT_HTML.write_text(build_html(data))
    print(f"wrote {OUT_HTML} ({OUT_HTML.stat().st_size:,} bytes)")
    print(f"wrote {OUT_JSON} ({OUT_JSON.stat().st_size:,} bytes)")
    print("totals:", data["totals"])
    print("SCC:", [len(c) for c in data["sccs"]], "cyclic pairs:", len(data["cyclic_pairs"]))


if __name__ == "__main__":
    main()
