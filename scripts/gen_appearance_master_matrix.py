#!/usr/bin/env python3
"""Render the appearance master matrix from the scope registry.

Source of truth: ``go_hive/docs/appearance-scope-registry.json``.
Output:          ``go_hive/docs/appearance-master-matrix.html``.

Deterministic, stdlib only. ``--check`` re-renders in memory and exits 1 when
the checked-in HTML differs, so the pair can be gated.

Usage:
    uv run python go_hive/scripts/gen_appearance_master_matrix.py
    uv run python go_hive/scripts/gen_appearance_master_matrix.py --check
"""

from __future__ import annotations

import html
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]          # go_hive/
REGISTRY = ROOT / "docs" / "appearance-scope-registry.json"
OUTPUT = ROOT / "docs" / "appearance-master-matrix.html"

STATUS_PILL = {
    "measured_aligned": ("ok", "已实测一致"),
    "partial": ("warn", "部分对齐"),
    "contract_unverified": ("info", "有契约未验证"),
    "not_aligned": ("bad", "未对齐"),
    "unaudited": ("bad", "未审计"),
    "out_of_scope": ("scope", "范围外（声明）"),
}
CONTRACT_LABEL = {
    "frozen_contract": ("ok", "冻结契约"),
    "doc_only": ("warn", "只有文档"),
    "test_annotated": ("info", "测试注解"),
    "none": ("bad", "无"),
}

CSS = """:root{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}
*{box-sizing:border-box} html{scroll-behavior:auto} body{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}
a{color:var(--accent2);text-decoration:none} a:hover{text-decoration:underline}
.wrap{max-width:1400px;margin:0 auto;padding:44px 26px 80px}
header{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}
.eyebrow{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}
h1{font-size:34px;line-height:1.2;margin:10px 0 10px;letter-spacing:-.02em} h1 span{color:var(--accent);white-space:nowrap}
.lede{color:var(--muted);max-width:1040px;font-size:15px} .meta{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px} .meta b{color:var(--text)}
section{margin:38px 0} h2{font-size:22px;margin:0 0 8px} h2 .num{color:var(--accent);font-size:14px;margin-right:8px} h3{font-size:15px;margin:24px 0 8px;color:#cfdae6}
p{color:#c6d2df;margin:8px 0} .sub{color:var(--muted);font-size:13px}
.grid{display:grid;gap:12px} .grid>*{min-width:0} .g3{grid-template-columns:repeat(3,minmax(0,1fr))} .g6{grid-template-columns:repeat(6,minmax(0,1fr))} .g2{grid-template-columns:repeat(2,minmax(0,1fr))}
@media(max-width:1180px){.g6{grid-template-columns:repeat(3,minmax(0,1fr))}} @media(max-width:900px){.g3,.g2{grid-template-columns:1fr 1fr}} @media(max-width:560px){.g3,.g2,.g6{grid-template-columns:minmax(0,1fr)}.wrap{padding:30px 16px 60px}h1{font-size:25px}}
.card{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px} .card .k{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700} .card .v{font-size:21px;font-weight:800;margin-top:5px;letter-spacing:-.01em} .card .d{color:var(--muted);font-size:12px;margin-top:5px}
.tablewrap{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2)}
table{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed} th,td{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word} th{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1} tr:hover td{background:#121a25} tr:last-child td{border-bottom:0}
td.num{font-variant-numeric:tabular-nums;text-align:right} code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}
pre{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2}
.pill{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal} .pill.ok{color:var(--ok);border-color:#1d3b26;background:#0f1a13} .pill.warn{color:var(--warn);border-color:#3d3013;background:#1a150a} .pill.bad{color:var(--bad);border-color:#3d1a1a;background:#1a0f0f} .pill.info{color:var(--accent2);border-color:#17324d;background:#0d1622} .pill.scope{color:var(--muted);border-color:#2b3a4d;background:#0e141c}
.small{font-size:11.5px;color:var(--muted);margin-top:3px} .ev{display:block;margin:2px 0;font-size:12px;color:#b9c7d6}
.callout{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4} .callout.bad{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7} .callout.info{border-color:var(--accent2);background:#0d1622;color:#cfe3ff}
.toc{display:flex;flex-wrap:wrap;gap:7px;margin-top:18px} .toc a{font-size:12px;border:1px solid var(--line);background:var(--panel);padding:5px 9px;border-radius:999px;color:var(--muted)} .toc a:hover{color:var(--text);text-decoration:none}
ul,ol{margin:7px 0 7px 20px;padding:0} li{margin:4px 0;color:#c6d2df} footer{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}
@media(max-width:560px){table{font-size:12px}th,td{padding:7px 6px}}
@media(max-width:760px){table{min-width:900px}}
@media(max-width:560px){.toc a{padding:8px 12px;font-size:13px;min-height:44px;display:inline-flex;align-items:center}}
"""


def esc(text: object) -> str:
    return html.escape(str(text), quote=True)


def pill(kind: str, label: str) -> str:
    return f'<span class="pill {kind}">{esc(label)}</span>'


def status_pill(status: str) -> str:
    kind, label = STATUS_PILL.get(status, ("info", status))
    return pill(kind, label)


def contract_pill(state: str) -> str:
    kind, label = CONTRACT_LABEL.get(state, ("info", state))
    return pill(kind, label)


def evidence_html(items: list[dict]) -> str:
    out = []
    for ev in items:
        path = str(ev.get("path", ""))
        note = esc(ev.get("note", ""))
        if path.startswith("go_hive/docs/"):
            ref = f'<a href="{esc(path[len("go_hive/docs/"):])}">{esc(path)}</a>'
        elif path.startswith("go_hive/"):
            ref = f'<a href="../{esc(path[len("go_hive/"):])}">{esc(path)}</a>'
        elif path.startswith("docs/"):
            ref = f'<a href="../../{esc(path)}">{esc(path)}</a>'
        elif path.startswith("core/"):
            ref = f'<a href="../../{esc(path)}">{esc(path)}</a>'
        else:
            ref = f"<code>{esc(path)}</code>"
        out.append(f'<span class="ev">{ref} <span class="small">[{esc(ev.get("kind", ""))}]</span> {note}</span>')
    return "".join(out)


def refs_html(items: list[str]) -> str:
    return "<br>".join(f"<code>{esc(x)}</code>" for x in items)


def render(reg: dict) -> str:
    dims = reg["dimensions"]
    s = reg["summary"]
    by_status = s["by_status"]

    rows = []
    for i, d in enumerate(dims, 1):
        nexts = "".join(f"<li>{esc(x)}</li>" for x in d.get("next", []))
        basis = esc(d["status_basis"])
        measured = d.get("measured_now")
        measured_html = f'<div class="small"><b>本次复算：</b>{esc(measured)}</div>' if measured else ""
        lane_state = "现有" if d.get("lane_state") == "existing" else "建议"
        rows.append(
            "<tr>"
            f'<td class="num">{i}</td>'
            f'<td><b>{esc(d["name"])}</b><div class="small">id: <code>{esc(d["id"])}</code></div>'
            f'<div class="small">优先级 {esc(d.get("priority", "—"))} · lane {esc(d.get("lane", "—"))}（{lane_state}）</div></td>'
            f'<td>{esc(d["scope"])}</td>'
            f'<td><div class="small"><b>参考（Python）</b></div>{refs_html(d["reference"])}'
            f'<div class="small" style="margin-top:6px"><b>Go 对应物</b></div>{refs_html(d["go"])}</td>'
            f"<td>{evidence_html(d.get('evidence', []))}</td>"
            f"<td>{status_pill(d['status'])}<div class=\"small\" style=\"margin-top:6px\">{basis}</div>{measured_html}</td>"
            f"<td>{contract_pill(d['contract_state'])}</td>"
            f"<td><ul>{nexts}</ul></td>"
            "</tr>"
        )

    vocab_rows = []
    for key, (_, label) in STATUS_PILL.items():
        vocab_rows.append(
            f"<tr><td>{status_pill(key)}</td><td><code>{esc(key)}</code></td>"
            f"<td>{esc(reg['status_vocabulary'][key])}</td></tr>"
        )
    contract_rows = []
    for key, (_, label) in CONTRACT_LABEL.items():
        contract_rows.append(
            f"<tr><td>{contract_pill(key)}</td><td><code>{esc(key)}</code></td>"
            f"<td>{esc(reg['contract_state_vocabulary'][key])}</td></tr>"
        )

    # gap list grouped by priority
    prio_order = ["P0", "P1", "P2", "P3"]
    prio_titles = {
        "P0": "P0 · 对外承诺最容易被依赖、需持续看守",
        "P1": "P1 · 机制/文案语义，行为差异的主要来源",
        "P2": "P2 · 协议/运维细节，静默坏的高发区",
        "P3": "P3 · 已对齐项的守护（防回归）",
    }
    prio_blocks = []
    for prio in prio_order:
        group = [d for d in dims if d.get("priority") == prio]
        if not group:
            continue
        items = []
        for d in group:
            nxt = "；".join(d.get("next", []))
            items.append(
                f'<li><b>{esc(d["name"])}</b> {status_pill(d["status"])}'
                f'<div class="small">{esc(d["status_basis"])}</div>'
                f'<div class="small">下一步：{esc(nxt)}</div></li>'
            )
        prio_blocks.append(
            f"<h3>{esc(prio_titles[prio])}（{len(group)} 项）</h3><ul>{''.join(items)}</ul>"
        )

    frozen = [d for d in dims if d["contract_state"] == "frozen_contract"]
    doc_only = [d for d in dims if d["contract_state"] == "doc_only"]
    annotated = [d for d in dims if d["contract_state"] == "test_annotated"]
    none_contract = [d for d in dims if d["contract_state"] == "none"]

    def name_list(items: list[dict]) -> str:
        return "、".join(f'<b>{esc(d["name"])}</b>' for d in items) if items else "—"

    generated = reg["generated_at"]
    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Hive ↔ go_hive 外观总览矩阵</title>
<style>
{CSS}</style></head><body><div class="wrap">
<header><div class="eyebrow">Hive · Appearance Scope Registry</div><h1>Hive ↔ go_hive <span>外观总览矩阵</span></h1>
<p class="lede">先把「外观」涉及哪些维度/层面/范围定清楚（{s["total"]} 维），再逐维给出状态与证据。状态只回答「等价性证据到哪一步」：<b>已实测一致 / 部分对齐 / 有契约未验证 / 未对齐 / 未审计 / 范围外（声明）</b>；每维另标契约形态（冻结契约 / 只有文档 / 测试注解 / 无）。<b>已有文档不等于已对齐</b>：doc_only 的维度最多只到「部分对齐」；范围外（声明）表示该面不在本仓库两侧实现范围内（如桌面壳），既不算对齐也不算未审计。</p>
<div class="meta"><span>生成：<b>{esc(generated)}</b></span><span>基线：<b>Go {esc(reg["baseline"]["go"])} · Python {esc(reg["baseline"]["python"])}</b></span><span>工作树：<b>{esc(reg["branch"])} @ {esc(reg["base_commit"])}</b></span><span>登记册：<b><a href="appearance-scope-registry.json">appearance-scope-registry.json</a></b></span></div>
<div class="toc"><a href="#scope">范围与口径</a><a href="#overview">总览</a><a href="#matrix">全维度表</a><a href="#gaps">缺口清单</a><a href="#contracts">契约分层</a><a href="#audits">已有审计</a><a href="#repro">复现</a></div></header>

<section id="scope"><h2><span class="num">00</span>范围与判定口径</h2>
<div class="callout info"><b>「外观」定义：</b>{esc(reg["definition"]["what"])}<br><b>不登记：</b>{esc(reg["definition"]["not"])}</div>
<div class="tablewrap"><table><thead><tr><th style="width:20%">状态</th><th style="width:22%">枚举值</th><th>含义</th></tr></thead><tbody>
{"".join(vocab_rows)}
</tbody></table></div>
<div class="tablewrap"><table><thead><tr><th style="width:20%">契约形态</th><th style="width:22%">枚举值</th><th>含义</th></tr></thead><tbody>
{"".join(contract_rows)}
</tbody></table></div>
</section>

<section id="overview"><h2><span class="num">01</span>总览</h2>
<div class="grid g6">
<div class="card"><div class="k">维度总数</div><div class="v">{s["total"]}</div><div class="d">覆盖任务要求的最小集合 + proto 与 config/env 拆分</div></div>
<div class="card"><div class="k">已实测一致</div><div class="v" style="color:var(--ok)">{by_status.get("measured_aligned", 0)}</div><div class="d">{"、".join(esc(i) for i in s["measured_aligned_ids"]) or "当前无"}</div></div>
<div class="card"><div class="k">部分对齐</div><div class="v" style="color:var(--warn)">{by_status.get("partial", 0)}</div><div class="d">主路径可达，差异已量化或边界未验证</div></div>
<div class="card"><div class="k">有契约未验证</div><div class="v" style="color:var(--accent2)">{by_status.get("contract_unverified", 0)}</div><div class="d">{"、".join(esc(i) for i in s["contract_unverified_ids"]) or "当前无"}</div></div>
<div class="card"><div class="k">未对齐</div><div class="v">{by_status.get("not_aligned", 0)}</div><div class="d">外观面上暂无「完全缺失」维度（缺口以 partial 形式存在）</div></div>
<div class="card"><div class="k">未审计</div><div class="v" style="color:var(--bad)">{by_status.get("unaudited", 0)}</div><div class="d">{"、".join(esc(i) for i in s["unaudited_ids"]) or "当前无"}</div></div>
<div class="card"><div class="k">范围外（声明）</div><div class="v" style="color:var(--muted)">{by_status.get("out_of_scope", 0)}</div><div class="d">{"、".join(esc(i) for i in s["out_of_scope_ids"]) or "当前无"} · 不在本仓库两侧实现范围内，非对齐也非未审计</div></div>
</div>
<div class="callout"><b>读法：</b>本页不把「路由存在」「审计写着已接」「测试带注解」当成对齐；每维的 <b>status_basis</b> 写清覆盖边界，<b>本次复算</b> 只在本次真有测量时出现。</div>
</section>

<section id="matrix"><h2><span class="num">02</span>全维度表（{s["total"]} 维）</h2>
<div class="tablewrap"><table><thead><tr><th style="width:3%">#</th><th style="width:11%">维度</th><th style="width:15%">范围（核什么）</th><th style="width:13%">参考源 / Go 对应物</th><th style="width:23%">已有证据</th><th style="width:17%">状态</th><th style="width:6%">契约</th><th>后续工作</th></tr></thead><tbody>
{"".join(rows)}
</tbody></table></div>
</section>

<section id="gaps"><h2><span class="num">03</span>缺口清单与建议优先级</h2>
{"".join(prio_blocks)}
<div class="callout bad"><b>交付口径：</b>「路由全绿 / 门禁通过 / 能打包」不等于外观对齐。P0 维度（{esc("、".join(d["name"] for d in dims if d.get("priority") == "P0"))}）在差异收敛前，不应在交付说明里写「接口面已对齐」。</div>
</section>

<section id="contracts"><h2><span class="num">04</span>契约分层：哪些维度可以依赖、哪些只有文档</h2>
<div class="grid g2">
<div class="card"><div class="k">有冻结契约（{len(frozen)}）</div><div class="d">{name_list(frozen)}</div><div class="d small">有机器可读产物或可重放校验器，其它 lane 可直接编码依赖；仍不等于行为已验证。</div></div>
<div class="card"><div class="k">只有文档（{len(doc_only)}）</div><div class="d">{name_list(doc_only)}</div><div class="d small">只有散文/HTML/注释描述，无可执行契约；这些维度状态最高只到「部分对齐」。</div></div>
<div class="card"><div class="k">仅测试注解（{len(annotated)}）</div><div class="d">{name_list(annotated)}</div><div class="d small">有 parity 测试与 e2e 资产，但没有跨实现契约产物。</div></div>
<div class="card"><div class="k">无契约（{len(none_contract)}）</div><div class="d">{name_list(none_contract)}</div><div class="d small">既无契约也无逐项证据；其中 desktop 判「范围外（声明）」。</div></div>
</div>
</section>

<section id="audits"><h2><span class="num">05</span>已有审计与证据文档</h2>
<div class="callout info">
<b>逐点/逐工具：</b><a href="surface-tracking-matrix.html">surface-tracking-matrix.html</a>（接口外形 + 自动快照）· <a href="tool-parity-matrix.html">tool-parity-matrix.html</a>（103 模块逐条）· <a href="tool-consistency-audit.html">tool-consistency-audit.html</a> · <a href="tool-contract-diff.md">tool-contract-diff.md</a> · <a href="tool-output-contract.md">tool-output-contract.md</a> · <a href="api-contract-diff.md">api-contract-diff.md</a> · <a href="cli-surface-diff.md">cli-surface-diff.md</a> · <a href="env-config-contract-diff.md">env-config-contract-diff.md</a><br>
<b>双跑/帧级对照：</b><a href="api-ws-golden.md">api-ws-golden.md</a>（REST 值级 + SSE 帧级 golden）· <a href="sse-payload-parity.md">sse-payload-parity.md</a> · <a href="prompt-parity.md">prompt-parity.md</a>（21,584 chars 逐字）· <a href="mcp-crosscheck.md">mcp-crosscheck.md</a>（§8 真实 server）· <a href="storage-compat.html">storage-compat.html</a> · <a href="g2-lossless-replacement.html">g2-lossless-replacement.html</a> · <a href="observability-surface-diff.md">observability-surface-diff.md</a> · <a href="../../docs/frontend-surface-diff.md">frontend-surface-diff.md</a> · <a href="../../docs/llm-real-crosscheck.md">llm-real-crosscheck.md</a> · <a href="../../docs/e2e-assets/playbook-verbatim/REPORT.md">playbook-verbatim/REPORT.md</a><br>
<b>范围视角：</b><a href="alignment-dimensions.html">alignment-dimensions.html</a>（18 维）· <a href="queen-worker-colony-parity-audit.html">queen-worker-colony-parity-audit.html</a>（24 维：12/10/0/2）· <a href="concept-flows-real-e2e.md">concept-flows-real-e2e.md</a> · <a href="module-interfaces.html">module-interfaces.html</a>（进程边界 + 不变量）· <a href="support-matrix.md">support-matrix.md</a>（能力状态 + 未验证清单）· <a href="platform-support.html">platform-support.html</a><br>
<b>机制接线：</b><a href="../CONCEPT_WIRING_AUDIT.md">CONCEPT_WIRING_AUDIT.md</a>（80/0/0）· <a href="../RUNTIME_SPINE_AUDIT.md">RUNTIME_SPINE_AUDIT.md</a>（70/0/0）· <a href="browser-bridge.html">browser-bridge.html</a> · <a href="browser-host.md">browser-host.md</a> · <a href="memory-and-soul.html">memory-and-soul.html</a>（记忆 / 时间线 / 召回 / soul）· <a href="memory-e2e.md">memory-e2e.md</a> · <a href="soul-daily-reflection.md">soul-daily-reflection.md</a> · <a href="playbook-python.md">playbook-python.md</a> · <a href="worker-escalation-e2e.md">worker-escalation-e2e.md</a> · <a href="integrations-evidence-audit.html">integrations-evidence-audit.html</a><br>
<b>契约产物：</b><a href="../contracts/api-routes.json">contracts/api-routes.json</a> · <a href="../contracts/api-responses/">contracts/api-responses/</a> · <a href="../contracts/api.proto">contracts/api.proto</a> · <a href="../contracts/cli-surface.json">contracts/cli-surface.json</a> · <a href="../contracts/sse-frames/">contracts/sse-frames/</a> · <a href="../contracts/tool-definitions.json">contracts/tool-definitions.json</a> · <a href="../contracts/builtin-skills.json">contracts/builtin-skills.json</a> · <a href="../contracts/env-vars.json">contracts/env-vars.json</a> · <a href="../contracts/config-keys.json">contracts/config-keys.json</a> · <a href="../contracts/config-support-ledger.json">contracts/config-support-ledger.json</a><br>
<b>浏览器审计：</b><a href="browser-bridge-surface-audit.html">browser-bridge-surface-audit.html</a>（Browser Bridge 13 维：已对齐 9 / 部分 4 / 未对齐 0；残差 R01–R20 已修 20/20，剩余为已量化近似）
</div>
</section>

<section id="repro"><h2><span class="num">06</span>复现与再生成</h2>
<pre># 重新渲染本页（登记册是唯一事实来源）
uv run python go_hive/scripts/gen_appearance_master_matrix.py

# 校验本页与登记册一致（CI 可 gate）
uv run python go_hive/scripts/gen_appearance_master_matrix.py --check

# 登记册引用的离线校验器（只读，可重放）
uv run python go_hive/scripts/check_api_proto.py                 # wire-proto：42 消息 / errors=0 / warnings=120（60 端点未映射）
uv run python go_hive/scripts/api_response_values.py --check     # api-routes：77 端点值级账本（47 已声明 / 0 未声明）
uv run python go_hive/scripts/sse_frames_canonical.py --check    # sse：帧级 golden（113/148 帧，11 条例外）
uv run python go_hive/scripts/freeze_env_config_contract.py --check  # env-vars / config-json：89/118/83 与 125/134
uv run python go_hive/scripts/surface_audit.py                   # 路由可达性（--write-report 才落盘）</pre>
<p class="sub">其余维度的复跑命令写在各证据文件内（双跑/真模型类为 opt-in，不纳入 make ci）：frontend_surface_diff.py --all、cli_surface_contract.py、prompt_catalog_parity.py / prompt_system_parity.py、sse_payload_parity.py、tool_output_golden.py + capture_tool_output.py、mcp_real_crosscheck_run.sh、storage_compat_run.sh、g2_home_fixture.py + g2_rollback_drill.sh、playbook_verbatim_compare.py、memory_soul_real_e2e.py、concept_flows_real_compare.py、e2e_browser_transport.sh、gen_browser_tool_defs.py --check。结论逐条写进登记册的 <code>status_basis</code> / <code>measured_now</code>。</p>
</section>

<footer>生成：{esc(generated)} · 工作树 {esc(reg["branch"])} @ {esc(reg["base_commit"])} · 基线 Go {esc(reg["baseline"]["go"])} / Python {esc(reg["baseline"]["python"])} · 登记册 <a href="appearance-scope-registry.json">appearance-scope-registry.json</a> · 本页只做范围与状态登记，不重复逐点清单；逐点状态以 <a href="surface-tracking-matrix.html">surface-tracking-matrix.html</a> / <a href="tool-parity-matrix.html">tool-parity-matrix.html</a> 为准。</footer>
</div></body></html>
"""


def main() -> int:
    check = "--check" in sys.argv
    reg = json.loads(REGISTRY.read_text(encoding="utf-8"))
    rendered = render(reg)
    if check:
        current = OUTPUT.read_text(encoding="utf-8") if OUTPUT.exists() else ""
        if current != rendered:
            print(f"[appearance] STALE: {OUTPUT} differs from registry-rendered output", file=sys.stderr)
            return 1
        print(f"[appearance] ok: {OUTPUT} matches {REGISTRY}")
        return 0
    OUTPUT.write_text(rendered, encoding="utf-8")
    print(f"[appearance] wrote {OUTPUT} ({len(rendered)} bytes, {len(reg['dimensions'])} dimensions)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
