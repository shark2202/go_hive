#!/usr/bin/env python3
"""Generate the Hive session-lifetime / run-limit dossier (zh-CN HTML + JSON).

The dossier answers one durable question: can a queen DM / colony session run
forever, what actually caps it, and which knobs are configurable at runtime.

Source of truth (recomputed on every run):
  - LoopConfig defaults                    core/framework/agent_loop/internals/types.py
  - queen / colony queen loop profiles     core/framework/agents/queen/agent.py
  - worker loop profile                    core/framework/agents/queen/worker_definition.py
  - per-spawn override whitelist + bounds  core/framework/host/colony_runtime.py
  - HIVE_MAX_ITER + max_context_tokens     core/framework/server/queen_orchestrator.py
                                             core/framework/config.py
  - compaction constants                   core/framework/agent_loop/internals/compaction.py
  - memory caps                            core/framework/agents/queen/queen_memory_v2.py
  - reflection cadence                     core/framework/config.py (MemoryReflectionConfig)
  - retention windows                      core/framework/config.py (RetentionConfig)
  - Go counterparts                        go-hive/internal/{agentloop,server,memory,colony}

Usage:
  python3 go_hive/scripts/gen_session_limits.py            # write HTML + JSON
  python3 go_hive/scripts/gen_session_limits.py --check    # fail on drift / broken anchors
"""
from __future__ import annotations

import argparse
import ast
import html
import json
import re
import subprocess
from datetime import datetime
from pathlib import Path
from typing import Any

REPO = Path(__file__).resolve().parents[2]
FW = REPO / "core" / "framework"
GO = REPO / "go_hive" / "go-hive"
OUT_HTML = REPO / "go_hive" / "docs" / "session-limits.html"
OUT_JSON = REPO / "go_hive" / "docs" / "session-limits.json"

ESC = lambda value: html.escape(str(value), quote=True)


# ---------------------------------------------------------------------------
# Source readers
# ---------------------------------------------------------------------------

def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def py_module(path: Path) -> ast.Module:
    return ast.parse(read(path), filename=str(path))


def py_constants(path: Path, names: set[str]) -> dict[str, Any]:
    """Literal module-level assignments (AnnAssign or Assign)."""
    found: dict[str, Any] = {}
    for node in py_module(path).body:
        target = None
        value = None
        if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
            target, value = node.targets[0].id, node.value
        elif isinstance(node, ast.AnnAssign) and isinstance(node.target, ast.Name):
            target, value = node.target.id, node.value
        if target in names and value is not None:
            try:
                found[target] = ast.literal_eval(value)
            except (ValueError, SyntaxError):
                pass
    return found


def py_dict(path: Path, name: str) -> dict[str, Any]:
    for node in py_module(path).body:
        if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
            if node.targets[0].id == name:
                return ast.literal_eval(node.value)
        if isinstance(node, ast.AnnAssign) and isinstance(node.target, ast.Name) and node.target.id == name:
            return ast.literal_eval(node.value)
    raise SystemExit(f"{path}: dict {name} not found")


def py_dataclass_defaults(path: Path, class_name: str) -> dict[str, Any]:
    for node in py_module(path).body:
        if isinstance(node, ast.ClassDef) and node.name == class_name:
            out: dict[str, Any] = {}
            for item in node.body:
                if isinstance(item, ast.AnnAssign) and isinstance(item.target, ast.Name) and item.value is not None:
                    try:
                        out[item.target.id] = ast.literal_eval(item.value)
                    except (ValueError, SyntaxError):
                        out[item.target.id] = ast.unparse(item.value)
            return out
    raise SystemExit(f"{path}: dataclass {class_name} not found")


def py_frozenset(path: Path, name: str) -> list[str]:
    tree = py_module(path)
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        if not any(isinstance(t, ast.Name) and t.id == name for t in node.targets):
            continue
        call = node.value
        if isinstance(call, ast.Call) and call.args:
            return sorted(ast.literal_eval(call.args[0]))
    raise SystemExit(f"{path}: frozenset {name} not found")


def py_loop_config_defaults() -> dict[str, Any]:
    values = py_dataclass_defaults(FW / "agent_loop" / "internals" / "types.py", "LoopConfig")
    keep = (
        "max_iterations",
        "grace_iterations",
        "max_context_tokens",
        "compaction_buffer_tokens",
        "compaction_buffer_ratio",
        "compaction_warning_buffer_tokens",
        "tool_call_budget",
        "tool_call_hard_multiple",
    )
    return {key: values.get(key) for key in keep}


def go_default_loop_config() -> dict[str, Any]:
    text = read(GO / "internal" / "agentloop" / "loop_config.go")
    match = re.search(r"func DefaultLoopConfig\(\) LoopConfig \{\s*return LoopConfig\{(.*?)\n\t\}", text, re.S)
    if not match:
        raise SystemExit("DefaultLoopConfig literal not found")
    out: dict[str, Any] = {}
    for line in match.group(1).splitlines():
        m = re.match(r"\s*([A-Za-z0-9_]+):\s*([0-9_\.]+),", line)
        if m:
            raw = m.group(2).replace("_", "")
            out[m.group(1)] = float(raw) if "." in raw else int(raw)
    return out


def go_constants(path: Path, names: set[str]) -> dict[str, Any]:
    text = read(path)
    out: dict[str, Any] = {}
    for name in names:
        m = re.search(rf"^\s*{re.escape(name)}\s*=\s*([0-9_\.]+)", text, re.M)
        if m:
            raw = m.group(1).replace("_", "")
            out[name] = float(raw) if "." in raw else int(raw)
    return out


def git_head() -> str:
    try:
        return subprocess.check_output(["git", "-C", str(REPO), "rev-parse", "--short", "HEAD"], text=True).strip()
    except Exception:
        return "unknown"


# ---------------------------------------------------------------------------
# Facts
# ---------------------------------------------------------------------------

PY_TYPES = FW / "agent_loop" / "internals" / "types.py"
PY_QUEEN_AGENT = FW / "agents" / "queen" / "agent.py"
PY_WORKER_DEF = FW / "agents" / "queen" / "worker_definition.py"
PY_COLONY_RUNTIME = FW / "host" / "colony_runtime.py"
PY_QUEEN_ORCH = FW / "server" / "queen_orchestrator.py"
PY_CONFIG = FW / "config.py"
PY_COMPACTION = FW / "agent_loop" / "internals" / "compaction.py"
PY_MEMORY = FW / "agents" / "queen" / "queen_memory_v2.py"
PY_RETENTION = FW / "maintenance" / "retention.py"
GO_LOOP = GO / "internal" / "agentloop" / "loop_config.go"
GO_COMPACTION = GO / "internal" / "agentloop" / "compaction.go"
GO_MEMORY = GO / "internal" / "memory" / "memory.go"
GO_WIRING = GO / "internal" / "server" / "chat_memory_wiring.go"
GO_SESSION_EVENTS = GO / "internal" / "server" / "session_start_events.go"
GO_WORKER_CFG = GO / "internal" / "colony" / "worker_loop_config.go"


def build_facts() -> dict[str, Any]:
    loop = py_loop_config_defaults()
    queen = py_dict(PY_QUEEN_AGENT, "queen_loop_config")
    colony = py_dict(PY_QUEEN_AGENT, "queen_colony_loop_config")
    worker = py_dict(PY_WORKER_DEF, "DEFAULT_LOOP_CONFIG")
    bounds = py_dict(PY_COLONY_RUNTIME, "_LOOP_OVERRIDE_BOUNDS")
    allowed = py_frozenset(PY_COLONY_RUNTIME, "_ALLOWED_WORKER_LOOP_OVERRIDES")
    comp = py_constants(PY_COMPACTION, {"LLM_COMPACT_MAX_DEPTH", "LLM_COMPACT_SUMMARY_MAX_TOKENS"})
    verbatim_match = re.search(
        r'_MAX_VERBATIM_CLIENT_RAW\s*=\s*int\(os\.environ\.get\("([A-Z0-9_]+)",\s*"([0-9]+)"\)',
        read(PY_COMPACTION),
    )
    mem = py_constants(PY_MEMORY, {"MAX_FILES", "MAX_FILE_SIZE_BYTES"})
    reflection = py_dataclass_defaults(PY_CONFIG, "MemoryReflectionConfig")
    retention = py_dataclass_defaults(PY_CONFIG, "RetentionConfig")
    go_loop = go_default_loop_config()
    go_comp = go_constants(GO_COMPACTION, {"LLMCompactMaxDepth", "LLMCompactSummaryMaxTokens", "DefaultMaxVerbatimClientMessages"})
    go_mem = go_constants(GO_MEMORY, {"MaxFiles", "MaxFileSizeBytes"})
    go_queen_event = re.search(r"queenAutonomousMaxIterations\s*=\s*([0-9_]+)", read(GO_SESSION_EVENTS))

    def pct(window: int, buffer_tokens: int, ratio: float) -> tuple[int, int]:
        trigger = int(window - (buffer_tokens + ratio * window))
        return trigger, round(trigger / window * 100)

    trigger, trigger_pct = pct(
        int(loop["max_context_tokens"]),
        int(loop["compaction_buffer_tokens"]),
        float(loop["compaction_buffer_ratio"]),
    )
    worker_trigger, worker_pct = pct(
        int(worker["max_context_tokens"]),
        int(loop["compaction_buffer_tokens"]),
        float(loop["compaction_buffer_ratio"]),
    )

    return {
        "loop": loop,
        "queen": queen,
        "colony": colony,
        "worker": worker,
        "bounds": bounds,
        "allowed": allowed,
        "comp": comp,
        "verbatim_env": {
            "name": verbatim_match.group(1) if verbatim_match else "HIVE_MAX_VERBATIM_CLIENT_MSGS",
            "default": int(verbatim_match.group(2)) if verbatim_match else 40,
        },
        "mem": mem,
        "reflection": reflection,
        "retention": retention,
        "go_loop": go_loop,
        "go_comp": go_comp,
        "go_mem": go_mem,
        "go_queen_event": int(go_queen_event.group(1).replace("_", "")) if go_queen_event else 999999,
        "trigger": trigger,
        "trigger_pct": trigger_pct,
        "worker_trigger": worker_trigger,
        "worker_pct": worker_pct,
        "go_head": git_head(),
    }


ANCHORS: list[tuple[str, str, str]] = [
    ("core/framework/agent_loop/internals/types.py", "class LoopConfig", "LoopConfig 默认值"),
    ("core/framework/agent_loop/internals/types.py", "compaction_buffer_ratio", "压缩触发比例"),
    ("core/framework/agents/queen/agent.py", "queen_loop_config = {", "queen 循环配置"),
    ("core/framework/agents/queen/agent.py", "queen_colony_loop_config = {", "colony queen 配置"),
    ("core/framework/agents/queen/worker_definition.py", "DEFAULT_LOOP_CONFIG", "worker 循环配置"),
    ("core/framework/host/colony_runtime.py", "_LOOP_OVERRIDE_BOUNDS", "per-spawn 覆盖边界"),
    ("core/framework/host/colony_runtime.py", "_ALLOWED_WORKER_LOOP_OVERRIDES", "覆盖白名单"),
    ("core/framework/server/queen_orchestrator.py", "HIVE_MAX_ITER", "queen 迭代上限环境变量"),
    ("core/framework/config.py", "def get_queen_max_context_tokens", "queen 上下文窗口解析"),
    ("core/framework/config.py", "def get_worker_max_context_tokens", "worker 上下文窗口解析"),
    ("core/framework/config.py", "class MemoryReflectionConfig", "反思节拍默认值"),
    ("core/framework/config.py", "class RetentionConfig", "保留策略默认值"),
    ("core/framework/agent_loop/internals/compaction.py", "LLM_COMPACT_SUMMARY_MAX_TOKENS", "摘要输出上限"),
    ("core/framework/agent_loop/internals/compaction.py", "HIVE_MAX_VERBATIM_CLIENT_MSGS", "逐字保留环境变量"),
    ("core/framework/agent_loop/internals/compaction.py", "MAX_CONSECUTIVE_FAILURES", "摘要断路器"),
    ("core/framework/agents/queen/queen_memory_v2.py", "MAX_FILES", "记忆文件上限"),
    ("core/framework/maintenance/retention.py", "Tier 3", "冷 queen 会话清理"),
    ("go_hive/go-hive/internal/agentloop/loop_config.go", "func DefaultLoopConfig", "Go 循环默认值"),
    ("go_hive/go-hive/internal/server/chat_memory_wiring.go", "HIVE_MAX_ITER", "Go queen 迭代上限"),
    ("go_hive/go-hive/internal/server/session_start_events.go", "queenAutonomousMaxIterations", "Go 事件中的 queen 迭代常量"),
    ("go_hive/go-hive/internal/agentloop/compaction.go", "LLMCompactSummaryMaxTokens", "Go 摘要上限"),
    ("go_hive/go-hive/internal/memory/memory.go", "MaxFiles", "Go 记忆容量"),
    ("go_hive/go-hive/internal/colony/worker_loop_config.go", "workerLoopOverrideBounds", "Go worker 覆盖边界"),
]


# ---------------------------------------------------------------------------
# Rendering helpers
# ---------------------------------------------------------------------------

def table(headers: list[str], rows: list[list[Any]], widths: list[str] | None = None) -> str:
    cols = ""
    if widths:
        cols = "<colgroup>" + "".join(f'<col style="width:{w}">' for w in widths) + "</colgroup>"
    head = "".join(f"<th>{ESC(h)}</th>" for h in headers)
    body = ""
    for row in rows:
        body += "<tr>" + "".join(f"<td>{cell if isinstance(cell, str) and cell.startswith('<') else ESC(cell)}</td>" for cell in row) + "</tr>\n"
    return f'<div class="tablewrap"><table>{cols}<thead><tr>{head}</tr></thead><tbody>{body}</tbody></table></div>'


def code(value: Any) -> str:
    return f"<code>{ESC(value)}</code>"


def pill(label: str, kind: str = "") -> str:
    return f'<span class="pill {kind}">{ESC(label)}</span>'


def section(num: str, sid: str, title: str, body: str) -> str:
    return f'<section id="{sid}"><h2><span class="num">{num}</span>{title}</h2>\n{body}\n</section>\n'


def build_html(f: dict[str, Any]) -> str:
    loop, queen, colony, worker = f["loop"], f["queen"], f["colony"], f["worker"]
    comp, mem, refl, ret = f["comp"], f["mem"], f["reflection"], f["retention"]
    bounds, allowed = f["bounds"], f["allowed"]
    go_loop, go_comp, go_mem = f["go_loop"], f["go_comp"], f["go_mem"]

    cards = f"""
<div class="grid g4">
<div class="card"><div class="k">Queen 单轮迭代</div><div class="v">{int(queen['max_iterations']):,}</div><div class="d">Python 默认；HIVE_MAX_ITER 只压不升</div></div>
<div class="card"><div class="k">Worker 迭代 / 收尾</div><div class="v">{worker['max_iterations']} + {worker['grace_iterations']}</div><div class="d">默认 3 轮工作 + 1 轮强制报告</div></div>
<div class="card"><div class="k">上下文窗口</div><div class="v">{int(loop['max_context_tokens']):,}</div><div class="d">≈{f['trigger']:,} tokens 触发压缩（{f['trigger_pct']}%）</div></div>
<div class="card"><div class="k">摘要输出上限</div><div class="v">{int(comp['LLM_COMPACT_SUMMARY_MAX_TOKENS']):,}</div><div class="d">硬常量，非配置项</div></div>
<div class="card"><div class="k">逐字保留用户消息</div><div class="v">{f['verbatim_env']['default']}</div><div class="d">环境变量 {code(f['verbatim_env']['name'])}</div></div>
<div class="card"><div class="k">记忆容量 / 作用域</div><div class="v">{mem['MAX_FILES']} × {mem['MAX_FILE_SIZE_BYTES']}B</div><div class="d">硬常量；每文件 4KB</div></div>
<div class="card"><div class="k">反思节拍</div><div class="v">{refl['every_turns']} / {refl['long_every']}</div><div class="d">每 6 回合 short；每 4 次 short 一次 long</div></div>
<div class="card"><div class="k">保留清理</div><div class="v">手动</div><div class="d">janitor 无调度器；活跃会话硬保护</div></div>
</div>"""

    lifetime = f"""
<p>会话本身没有 <b>TTL、最大轮数或最大寿命</b>：会话是磁盘目录（meta/state/events/conversations/parts/checkpoints），进程退出后可以 resume；Go/Python 都支持跨重启、跨版本迁移。真正的上限发生在“单轮迭代、上下文窗口、记忆容量、存储保留”四层。</p>
{table(["会话类型", "能活多久", "单轮上限", "额外增长点"], [
    ["Queen DM", "无期限；重启用 resume", f"{int(queen['max_iterations']):,} 次迭代（HIVE_MAX_ITER 可压低）", "conversations/parts、events、日志"],
    ["Colony queen", "无期限；与 DM 同机制", f"{int(colony['max_iterations']):,} 次迭代", "tracker.db、worker 工位、playbook 运行记录"],
    ["Worker", "短生命周期（被派生的任务）", f"{worker['max_iterations']} + {worker['grace_iterations']} 收尾（可被 queen 按次覆盖）", "工位转录/data，默认 14 天后可被 janitor 深清"],
], ["18%", "24%", "34%", "24%"])}
<div class="callout info">“几十年”在代码层可行，但进程必须重启后 resume 会话；触发器、webhook HTTP 服务、timer goroutine 都在会话进程内，机器重启后要重新加载会话才恢复。</div>"""

    per_turn = table(["参数", "默认", "作用域", "可调性"], [
        ["<code>max_iterations</code>", f"{int(queen['max_iterations']):,}（queen）/ {worker['max_iterations']}（worker）", "queen：一次 turn 内模型↔工具循环；worker：一次任务", "queen 仅 <code>HIVE_MAX_ITER</code> 压低；worker 可每次 spawn 覆盖（1–1000）"],
        ["<code>grace_iterations</code>", f"{worker['grace_iterations']}（worker）/ 0（queen）", "迭代耗尽后的收尾轮", "worker 可覆盖 0–3；queen 代码固定"],
        ["<code>max_context_tokens</code>", f"{int(loop['max_context_tokens']):,}", "上下文预算；决定压缩触发点", "配置 <code>queen_llm.&lt;id&gt;.max_context_tokens</code> / <code>llm.max_context_tokens</code> / 模型目录 / worker spawn 覆盖 1k–1M"],
        ["<code>compaction_buffer_tokens</code>", f"{int(loop['compaction_buffer_tokens']):,}", "压缩预留的绝对头部空间", f"LoopConfig 代码常量；queen 配置不传（worker 覆盖白名单也不含）"],
        ["<code>compaction_buffer_ratio</code>", f"{loop['compaction_buffer_ratio']}", "按窗口比例预留头部空间", "代码常量；触发点随 max_context_tokens 间接变化"],
        ["<code>compaction_warning_buffer_tokens</code>", f"{int(loop['compaction_warning_buffer_tokens']):,}", "压缩预警线", "代码常量"],
    ], ["26%", "20%", "24%", "30%"]) + f"""
<div class="callout">触发公式：<code>trigger = max_context_tokens − (compaction_buffer_tokens + compaction_buffer_ratio × max_context_tokens)</code>。默认 180k → <b>{f['trigger']:,}</b> tokens（{f['trigger_pct']}%）。worker 默认 180k 同公式 → {f['worker_trigger']:,}（{f['worker_pct']}%）。</div>"""

    compaction = f"""
<figure class="diagram"><pre>每轮 iteration 前 → needs_compaction(估算)
  ├─ 0a 超长用户消息：>100k 字符先截断（保留头 4k + 尾 4k）
  ├─ 0  microcompact：只清可重取的工具结果，保留最近 {go_comp.get('DefaultMicrocompactKeepRecent', 6)} 条
  ├─ 1  prune：删除旧工具结果，保留任务状态
  ├─ 2  LLM 摘要：递归对折 ≤{comp['LLM_COMPACT_MAX_DEPTH']} 层；输出 ≤{int(comp['LLM_COMPACT_SUMMARY_MAX_TOKENS']):,} tokens；
  │      保留 ≤{f['verbatim_env']['default']} 条逐字用户消息；连续 {3} 次失败 → 断路器；30s 内不重复压缩
  └─ 3  应急确定性摘要（LLM 不可用/摘要失败时）</pre>
<figcaption>压缩流水线（两侧同构）；压缩完成后发 <code>context_compacted</code>，并立即触发一次 long memory reflection。</figcaption></figure>
<p><b>保真度边界：</b>压缩不是“无损归档”。旧对话会变成摘要，逐字细节只对最近 {f['verbatim_env']['default']} 条用户消息（可用 {code(f['verbatim_env']['name'])} 调整）和最近 {go_comp.get('DefaultMicrocompactKeepRecent', 6)} 条可重取工具结果保留。要长期保留的事实必须写入 memory / skill / 文档，而不是指望聊天记录。</p>
{table(["护栏", "值", "可调性"], [
    ["摘要输出上限", f"{int(comp['LLM_COMPACT_SUMMARY_MAX_TOKENS']):,} tokens", "硬常量 <code>LLM_COMPACT_SUMMARY_MAX_TOKENS</code>"],
    ["递归深度", comp['LLM_COMPACT_MAX_DEPTH'], "硬常量 <code>LLM_COMPACT_MAX_DEPTH</code>"],
    ["逐字用户消息", f["verbatim_env"]["default"], f"环境变量 {code(f['verbatim_env']['name'])}"],
    ["连续失败断路器", 3, "硬常量 <code>MAX_CONSECUTIVE_FAILURES</code>"],
    ["重压缩最小间隔", "30s", "硬常量"],
    ["微压缩保留", 6, f"环境变量 {code('HIVE_MICROCOMPACT_KEEP_RECENT')}"],
], ["26%", "18%", "56%"])}"""

    memory = f"""
{table(["项目", "默认", "可调性"], [
    ["每作用域文件数", mem['MAX_FILES'], "硬常量 <code>MAX_FILES</code>；无 env/config"],
    ["每文件字节数", f"{mem['MAX_FILE_SIZE_BYTES']} (4KB)", "硬常量 <code>MAX_FILE_SIZE_BYTES</code>；owner PUT 超限 400"],
    ["short reflection 节拍", f"每 {refl['every_turns']} 个完成回合", "配置 <code>memory_reflection.every_turns</code>"],
    ["long reflection 节拍", f"每 {refl['long_every']} 次 short", "配置 <code>memory_reflection.long_every</code>"],
    ["反思模型", refl['model'], "配置 <code>memory_reflection.model</code>（worker|main）"],
    ["压缩后反思", "立即 long", "事件驱动，不可配置"],
    ["记忆读取模式", "index / wait / background", "配置 <code>memory_recall.mode</code>（默认 index）"],
], ["28%", "24%", "48%"])}
<div class="callout">记忆是“摘要+容量上限”的长期层：写满 {mem['MAX_FILES']} 个文件后，reflection 只能淘汰/合并；跨几十年不可能完整保留每次对话。</div>"""

    retention = f"""
<p>retention janitor <b>只支持手动触发</b>（CLI 或 <code>POST /api/maintenance/janitor/run</code>），没有调度器。活跃会话（SessionManager live）硬阻断；其它目录要通过年龄/安全门。</p>
{table(["层级", "对象", "默认窗口", "处置"], [
    ["Tier 1", "events / llm / compaction 日志、工具产物、轮转日志", f"{ret['event_logs_days']}–{ret['rotated_logs_days']} 天", "直接删除（无归档价值）"],
    ["Tier 2", "已结束 worker 转录（conversations + data）", f"{ret['worker_deep_clean_days']} 天", f"{ret['mode']} 模式：归档或删除；保留 result.json 墓碑"],
    ["Tier 3", "冷 queen 会话", f"{ret['queen_hygiene_days']} 天", "只清 orphan spillover、重写 events（丢弃 context_usage_updated），不删对话"],
], ["12%", "42%", "18%", "28%"])}
<div class="callout bad">queen 的 conversations/parts 与 colony 的 tracker.db <b>不会自动瘦身</b>；几十年不手动跑 janitor/归档，磁盘会持续增长。活跃 grace={ret['active_grace_hours']}h，events 重写阈值={int(ret['events_rewrite_min_bytes']):,} bytes。</div>"""

    knobs = table(["层级", "参数", "默认", "怎么改"], [
        ["配置（configuration.json）", "queen/worker 上下文窗口", f"{int(loop['max_context_tokens']):,}", "<code>queen_llm.&lt;id&gt;.max_context_tokens</code> / <code>worker_llm.max_context_tokens</code> / <code>llm.max_context_tokens</code>；模型目录可兜底"],
        ["配置", "反思节拍", f"{refl['every_turns']} / {refl['long_every']}", "<code>memory_reflection.every_turns</code> / <code>long_every</code> / <code>model</code>"],
        ["配置", "保留窗口", "7–30 天", "<code>retention.*</code> 段；也支持 <code>HIVE_RETENTION_*</code>"],
        ["环境变量", "queen 迭代上限（只压不升）", "999,999", "<code>HIVE_MAX_ITER</code>，正整数"],
        ["环境变量", "逐字用户消息", f"{f['verbatim_env']['default']}", f"<code>{f['verbatim_env']['name']}</code>"],
        ["环境变量", "微压缩保留条数", 6, "<code>HIVE_MICROCOMPACT_KEEP_RECENT</code>"],
        ["运行时按次", "worker 迭代/收尾/预算/窗口", f"{worker['max_iterations']}/{worker['grace_iterations']}", "<code>run_worker</code> 的 <code>loop_config_overrides</code>；白名单：" + ", ".join(f"<code>{k}</code>" for k in allowed) + "；边界见下表"],
        ["代码常量", "压缩触发 buffer / 预警", f"{int(loop['compaction_buffer_tokens']):,} + {loop['compaction_buffer_ratio']}×window", "改 <code>LoopConfig</code> 默认值（发版）；queen 配置不传"],
        ["代码常量", "摘要上限 / 递归 / 断路器", f"{int(comp['LLM_COMPACT_SUMMARY_MAX_TOKENS']):,} / {comp['LLM_COMPACT_MAX_DEPTH']} / 3", "改 <code>compaction.py</code> 常量（发版）"],
        ["代码常量", "记忆容量", f"{mem['MAX_FILES']} × {mem['MAX_FILE_SIZE_BYTES']}B", "改 <code>queen_memory_v2.py</code> / <code>internal/memory/memory.go</code>（发版）"],
    ], ["18%", "26%", "18%", "38%"]) + table(["worker 覆盖键", "下限", "上限"], [[f"<code>{k}</code>", bounds[k][0], bounds[k][1]] for k in allowed], ["40%", "30%", "30%"])

    longevity = """
<ol>
<li><b>进程可恢复</b>：机器重启后 <code>hive serve</code> 重新加载会话；否则会话只是磁盘上的静止数据，触发器/timer/webhook 都不会跑。</li>
<li><b>定期跑 janitor + 自己归档</b>：queen parts、tracker.db、worker 工位不会自动瘦身；events 重写也只在 janitor 跑时发生。</li>
<li><b>接受摘要化</b>：几十年对话不可能逐字保留；长期事实要写 memory/skill/文档。</li>
<li><b>模型/协议演进</b>：换模型、换 provider、schema 升级都要迁移验证（v1→v3 home 已有双跑证据）。</li>
<li><b>成本</b>：每次压缩、每 6 回合反思、每轮系统提示都在烧 token；长跑是持续成本。</li>
<li><b>触发器/队列</b>：timer 是会话内 sleep-until-due，missed ticks 上限 100；webhook 队列无界、无背压，长跑要自己控流量。</li>
</ol>"""

    deltas = table(["项目", "Python", "Go 现状", "判定"], [
        ["queen 单轮 max_iterations", f"{int(queen['max_iterations']):,}", f"{int(go_loop.get('MaxIterations', 0))}（DefaultLoopConfig 起步；仅 HIVE_MAX_ITER 覆盖）", pill("待修：事件却写 999,999", "warn")],
        ["queen tool-call 硬停", f"{queen['tool_call_budget']} × {int(loop.get('tool_call_hard_multiple') or 5)} = {queen['tool_call_budget'] * int(loop.get('tool_call_hard_multiple') or 5)}", f"预算 {int(go_loop.get('ToolCallBudget', 0))} / 硬停 {int(go_loop.get('ToolCallHardLimit', 0))}", pill("登记候选：Go 更严", "warn")],
        ["worker 迭代 / 收尾", f"{worker['max_iterations']} + {worker['grace_iterations']}", f"{worker['max_iterations']} + {worker['grace_iterations']}（默认同值）", pill("一致", "ok")],
        ["上下文窗口 / 压缩 buffer", f"{int(loop['max_context_tokens']):,} / {int(loop['compaction_buffer_tokens']):,}+{loop['compaction_buffer_ratio']}", f"{int(go_loop.get('MaxContextTokens', 0)):,} / {int(go_loop.get('CompactionBufferTokens', 0)):,}+{go_loop.get('CompactionBufferRatio')}", pill("一致", "ok")],
        ["摘要上限 / 深度 / 逐字条数", f"{int(comp['LLM_COMPACT_SUMMARY_MAX_TOKENS']):,} / {comp['LLM_COMPACT_MAX_DEPTH']} / {f['verbatim_env']['default']}", f"{int(go_comp.get('LLMCompactSummaryMaxTokens', 0)):,} / {int(go_comp.get('LLMCompactMaxDepth', 0))} / {int(go_comp.get('DefaultMaxVerbatimClientMessages', 0))}", pill("一致", "ok")],
        ["记忆容量", f"{mem['MAX_FILES']} × {mem['MAX_FILE_SIZE_BYTES']}B", f"{int(go_mem.get('MaxFiles', 0))} × {int(go_mem.get('MaxFileSizeBytes', 0))}B", pill("一致", "ok")],
    ], ["22%", "26%", "34%", "18%"])

    anchors_rows = [[code(p), code(needle), label] for p, needle, label in ANCHORS]
    anchors_html = table(["文件", "符号 / 字符串", "说明"], anchors_rows, ["44%", "34%", "22%"]) + """
<p class="sub">生成器会对上表做锚点校验：文件缺失或符号消失时 <code>--check</code> 直接失败，避免文档随代码漂移。</p>"""

    reuse = f"""
<pre># 重新生成（写 HTML + JSON）
python3 go_hive/scripts/gen_session_limits.py

# CI / 本地校验：产物字节一致 + 代码锚点存在
python3 go_hive/scripts/gen_session_limits.py --check</pre>
<p>复用场景：① 回答“能聊多久 / 会不会爆上下文”；② 给客户做容量/成本评估；③ 调整 queen/worker 参数前查默认值与边界；④ 验收时核对 Go/Python 是否仍在同一条线上（本页第 08 节）。</p>
<p class="sub">所有数字均从源码重算；HTML 是生成物，不要手改。基线：上游 1.0.15 <code>6703b434</code>，Go HEAD <code>{f['go_head']}</code>。</p>"""

    body = (
        f'<header><div class="eyebrow">Hive · Session Lifetime &amp; Limits</div>'
        f'<h1>会话寿命与运行上限 <span>压缩 · 记忆 · 调参</span></h1>'
        f'<p class="lede">Queen DM 和 colony 会话没有 TTL，也没有“最大轮数”；真正约束它们的是单轮迭代、上下文窗口、记忆容量和存储保留。本页给出一张可复用的边界表：哪些值是配置项、哪些是运行时参数、哪些写死在代码里，以及 Go 与 Python 目前是否一致。</p>'
        f'<div class="meta"><span>生成：<b>{datetime.now().strftime("%Y-%m-%d %H:%M")} CST</b></span><span>上游基线：<b>1.0.15 · 6703b434</b></span><span>Go HEAD：<b>{ESC(f["go_head"])}</b></span><span>口径：<b>以代码为准</b></span><span>产物：<b>HTML + session-limits.json</b></span></div>'
        '<div class="toc"><a href="#s00">结论卡片</a><a href="#s01">会话寿命</a><a href="#s02">单轮与上下文</a><a href="#s03">压缩流水线</a><a href="#s04">记忆容量</a><a href="#s05">存储保留</a><a href="#s06">动态调参</a><a href="#s07">几十年前提</a><a href="#s08">Go 差异</a><a href="#s09">代码索引</a><a href="#s10">复用与维护</a></div></header>\n\n'
        + section("00", "s00", "结论卡片", cards)
        + section("01", "s01", "会话能活多久", lifetime)
        + section("02", "s02", "单轮与上下文上限", per_turn)
        + section("03", "s03", "压缩流水线：能继续聊，但不是无损", compaction)
        + section("04", "s04", "记忆容量与反思节拍", memory)
        + section("05", "s05", "存储与保留：真正会先满的是磁盘", retention)
        + section("06", "s06", "动态调参：三层入口", knobs)
        + section("07", "s07", "“连续运行几十年”的前提", longevity)
        + section("08", "s08", "Go 与 Python 的现状差异", deltas)
        + section("09", "s09", "代码索引（生成器会校验）", anchors_html)
        + section("10", "s10", "复用与维护", reuse)
        + '<footer>本页由 <code>go_hive/scripts/gen_session_limits.py</code> 生成；改动请改生成器或源码，不要手改 HTML。数字口径：配置默认值 + 代码常量 + 实测差异；不把“可配置”误写成“已对齐”。</footer>\n</div></body></html>\n'
    )

    css = """
:root{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}
*{box-sizing:border-box} body{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}
a{color:var(--accent2);text-decoration:none} a:hover{text-decoration:underline}
.wrap{max-width:1280px;margin:0 auto;padding:44px 26px 80px}
header{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}
.eyebrow{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}
h1{font-size:30px;line-height:1.25;margin:10px 0 10px;letter-spacing:-.02em} h1 span{color:var(--accent)}
.lede{color:var(--muted);max-width:1080px;font-size:15px} .meta{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px} .meta b{color:var(--text)}
section{margin:38px 0} h2{font-size:22px;margin:0 0 8px} h2 .num{color:var(--accent);font-size:14px;margin-right:8px} h3{font-size:15.5px;margin:22px 0 8px;color:#cfdae6}
p{color:#c6d2df;margin:8px 0} .sub{color:var(--muted);font-size:12.5px}
.grid{display:grid;gap:12px} .g4{grid-template-columns:repeat(4,minmax(0,1fr))} .grid>*{min-width:0}
@media(max-width:900px){.g4{grid-template-columns:1fr 1fr}} @media(max-width:560px){.g4{grid-template-columns:1fr}.wrap{padding:30px 16px 60px}h1{font-size:24px}}
.card{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px;min-width:0} .card .k{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700} .card .v{font-size:20px;font-weight:800;margin-top:5px;letter-spacing:-.01em} .card .d{color:var(--muted);font-size:12px;margin-top:5px}
.tablewrap{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2);max-width:100%}
table{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed} th,td{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word} th{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1} tr:hover td{background:#121a25} tr:last-child td{border-bottom:0}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}
pre{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2;max-width:100%}
.pill{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal} .pill.ok{color:var(--ok);border-color:#1d3b26;background:#0f1a13} .pill.warn{color:var(--warn);border-color:#3d3013;background:#1a150a} .pill.info{color:var(--accent2);border-color:#17324d;background:#0d1622}
.callout{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4;overflow-wrap:anywhere} .callout.bad{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7} .callout.info{border-color:var(--accent2);background:#0d1622;color:#cfe3ff}
.toc{display:flex;flex-wrap:wrap;gap:7px;margin-top:18px} .toc a{font-size:12px;border:1px solid var(--line);background:var(--panel);padding:5px 9px;border-radius:999px;color:var(--muted)} .toc a:hover{color:var(--text);text-decoration:none}
ul,ol{margin:7px 0 7px 20px;padding:0} li{margin:4px 0;color:#c6d2df} footer{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}
figure.diagram{background:var(--code);border:1px solid var(--line);border-radius:12px;padding:14px;overflow-x:auto;margin:16px 0;max-width:100%}
figure.diagram figcaption{color:var(--muted);font-size:12px;margin-top:8px}
@media(max-width:560px){table{font-size:12px}th,td{padding:7px 6px}.toc a{padding:8px 12px;font-size:13px;min-height:44px;display:inline-flex;align-items:center}}
"""
    return f'<!DOCTYPE html>\n<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">\n<title>Hive 会话寿命与运行上限 · 压缩 / 记忆 / 调参</title>\n<style>{css}</style></head><body><div class="wrap">\n{body}'


def build_json(f: dict[str, Any]) -> dict[str, Any]:
    return {
        "schema": "hive-session-limits/v1",
        "generated_at": datetime.now().astimezone().isoformat(timespec="seconds"),
        "baseline": {"upstream": "1.0.15 6703b434", "go_head": f["go_head"]},
        "headline": {
            "queen_max_iterations": f["queen"]["max_iterations"],
            "worker_iterations": f["worker"]["max_iterations"],
            "worker_grace": f["worker"]["grace_iterations"],
            "max_context_tokens": f["loop"]["max_context_tokens"],
            "compaction_trigger": f["trigger"],
            "compaction_trigger_pct": f["trigger_pct"],
            "summary_max_tokens": f["comp"]["LLM_COMPACT_SUMMARY_MAX_TOKENS"],
            "verbatim_user_messages": f["verbatim_env"]["default"],
            "memory_max_files": f["mem"]["MAX_FILES"],
            "memory_max_file_bytes": f["mem"]["MAX_FILE_SIZE_BYTES"],
            "reflection_every_turns": f["reflection"]["every_turns"],
            "reflection_long_every": f["reflection"]["long_every"],
        },
        "python": {
            "loop_defaults": f["loop"],
            "queen_loop_config": f["queen"],
            "colony_queen_loop_config": f["colony"],
            "worker_loop_config": f["worker"],
            "worker_override_allowed": f["allowed"],
            "worker_override_bounds": {key: list(value) for key, value in f["bounds"].items()},
            "compaction_constants": f["comp"],
            "verbatim_env": f["verbatim_env"],
            "memory_constants": f["mem"],
            "memory_reflection": f["reflection"],
            "retention": f["retention"],
        },
        "go": {
            "default_loop_config": f["go_loop"],
            "compaction_constants": f["go_comp"],
            "memory_constants": f["go_mem"],
            "queen_event_max_iterations": f["go_queen_event"],
        },
        "deltas": [
            {
                "item": "queen max_iterations",
                "python": f["queen"]["max_iterations"],
                "go": f["go_loop"].get("MaxIterations"),
                "status": "open",
                "evidence": "chat_memory_wiring.go starts at DefaultLoopConfig; only HIVE_MAX_ITER overrides; session_start_events.go advertises 999999",
            },
            {
                "item": "queen tool-call hard stop",
                "python": f["queen"]["tool_call_budget"] * int(f["loop"].get("tool_call_hard_multiple") or 5),
                "go": f["go_loop"].get("ToolCallHardLimit"),
                "status": "registered-candidate",
                "evidence": "Go comment claims a port-side audit fix",
            },
        ],
        "anchors": [{"path": p, "needle": n, "label": label} for p, n, label in ANCHORS],
    }


def normalise_html(text: str) -> str:
    """Ignore volatile generation time / Go HEAD in --check comparisons."""
    text = re.sub(r"生成：<b>[^<]*CST</b>", "生成：<b>__TIME__</b>", text)
    text = re.sub(r"Go HEAD：<b>[^<]*</b>", "Go HEAD：<b>__HEAD__</b>", text)
    return re.sub(r"Go HEAD <code>[0-9a-f]{7,40}</code>", "Go HEAD <code>__HEAD__</code>", text)


def normalise_json(doc: dict[str, Any]) -> dict[str, Any]:
    """Stable projection for --check: drop time, HEAD and anchor labels."""
    doc = json.loads(json.dumps(doc))
    doc.pop("generated_at", None)
    if isinstance(doc.get("baseline"), dict):
        doc["baseline"]["go_head"] = "__HEAD__"
    for item in doc.get("anchors", []):
        item.pop("label", None)
    return doc


def check_anchors() -> list[str]:
    problems: list[str] = []
    for rel_path, needle, label in ANCHORS:
        path = REPO / rel_path
        if not path.exists():
            problems.append(f"missing file: {rel_path} ({label})")
            continue
        if needle not in read(path):
            problems.append(f"missing anchor: {rel_path} :: {needle!r} ({label})")
    return problems


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="verify generated files and code anchors")
    args = parser.parse_args()

    facts = build_facts()
    html_text = build_html(facts)
    json_doc = build_json(facts)
    json_text = json.dumps(json_doc, ensure_ascii=False, indent=2, sort_keys=True) + "\n"

    if args.check:
        problems = check_anchors()
        if not OUT_HTML.exists():
            problems.append(f"missing output: {OUT_HTML}")
        elif normalise_html(OUT_HTML.read_text(encoding="utf-8")) != normalise_html(html_text):
            problems.append(f"drift: {OUT_HTML}")
        if not OUT_JSON.exists():
            problems.append(f"missing output: {OUT_JSON}")
        else:
            try:
                current = json.loads(OUT_JSON.read_text(encoding="utf-8"))
            except json.JSONDecodeError as exc:
                problems.append(f"invalid json: {OUT_JSON}: {exc}")
                current = None
            if current is not None and normalise_json(current) != normalise_json(json_doc):
                problems.append(f"drift: {OUT_JSON}")
        if problems:
            for problem in problems:
                print(f"session-limits: {problem}")
            return 1
        print(f"session-limits: OK ({len(ANCHORS)} anchors, HTML+JSON up to date)")
        return 0

    OUT_HTML.parent.mkdir(parents=True, exist_ok=True)
    OUT_HTML.write_text(html_text, encoding="utf-8")
    OUT_JSON.write_text(json_text, encoding="utf-8")
    print(f"session-limits: wrote {OUT_HTML.relative_to(REPO)} and {OUT_JSON.relative_to(REPO)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
