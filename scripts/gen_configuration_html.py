#!/usr/bin/env python3
"""Render go_hive/docs/configuration.html from contracts/config-keys.json.

Usage:
  python3 scripts/gen_configuration_html.py            # write
  python3 scripts/gen_configuration_html.py --check    # fail if stale
"""
from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CONTRACT = ROOT / "contracts" / "config-keys.json"
OUTPUT = ROOT / "docs" / "configuration.html"
TITLE = "configuration.json 配置参考 · go_hive × Hive"

CSS = """
:root{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}
*{box-sizing:border-box} html{scroll-behavior:auto} body{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif}
a{color:var(--accent2);text-decoration:none} a:hover{text-decoration:underline}
.wrap{max-width:1360px;margin:0 auto;padding:44px 26px 80px}
header{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}
.eyebrow{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}
h1{font-size:34px;line-height:1.2;margin:10px 0 10px;letter-spacing:-.02em} h1 span{color:var(--accent);white-space:nowrap}
.lede{color:var(--muted);max-width:1040px;font-size:15px} .meta{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px} .meta b{color:var(--text)}
section{margin:38px 0} h2{font-size:22px;margin:0 0 8px} h2 .num{color:var(--accent);font-size:14px;margin-right:8px} h3{font-size:15px;margin:24px 0 8px;color:#cfdae6}
p{color:#c6d2df;margin:8px 0} .sub{color:var(--muted);font-size:13px}
.grid{display:grid;gap:12px} .g4{grid-template-columns:repeat(4,minmax(0,1fr))} .g5{grid-template-columns:repeat(5,minmax(0,1fr))}
@media(max-width:1100px){.g5{grid-template-columns:repeat(3,minmax(0,1fr))}} @media(max-width:900px){.g4{grid-template-columns:1fr 1fr}} @media(max-width:560px){.g4,.g5{grid-template-columns:1fr}.wrap{padding:30px 16px 60px}h1{font-size:25px}}
.card{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px} .card .k{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700} .card .v{font-size:21px;font-weight:800;margin-top:5px;letter-spacing:-.01em} .card .d{color:var(--muted);font-size:12px;margin-top:5px;overflow-wrap:anywhere;word-break:break-word}
.tablewrap{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2)}
table{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed} th,td{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word} th{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1} tr:hover td{background:#121a25} tr:last-child td{border-bottom:0}
td.num{font-variant-numeric:tabular-nums;text-align:right} code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}
pre{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2}
.pill{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal} .pill.ok{color:var(--ok);border-color:#1d3b26;background:#0f1a13} .pill.warn{color:var(--warn);border-color:#3d3013;background:#1a150a} .pill.bad{color:var(--bad);border-color:#3d1a1a;background:#1a0f0f} .pill.info{color:var(--accent2);border-color:#17324d;background:#0d1622}
.small{font-size:11.5px;color:var(--muted);margin-top:3px}
.callout{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4} .callout.bad{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7} .callout.info{border-color:var(--accent2);background:#0d1622;color:#cfe3ff} .callout.ok{border-color:var(--ok);background:#0f1a13;color:#c8e8cf}
.toc{display:flex;flex-wrap:wrap;gap:7px;margin-top:18px} .toc a{font-size:12px;border:1px solid var(--line);background:var(--panel);padding:5px 9px;border-radius:999px;color:var(--muted)} .toc a:hover{color:var(--text);text-decoration:none}
ul,ol{margin:7px 0 7px 20px;padding:0} li{margin:4px 0;color:#c6d2df} footer{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}
@media(max-width:560px){table{font-size:12px}th,td{padding:7px 6px}}
@media(max-width:900px){table{min-width:900px}}
"""

STATUS_PILL = {
    "aligned": "ok",
    "go_partial": "warn",
    "python_only": "info",
    "go_only": "info",
    "unread": "bad",
}


def esc(text: object) -> str:
    return (
        str(text)
        .replace("&", "&amp;")
        .replace("<", "&lt;")
        .replace(">", "&gt;")
        .replace('"', "&quot;")
    )


def fmt_default(value: object) -> str:
    if value is None:
        return "<code>null</code>"
    if isinstance(value, bool):
        return "<code>true</code>" if value else "<code>false</code>"
    if isinstance(value, (int, float)):
        return f"<code>{value}</code>"
    if isinstance(value, str):
        return f"<code>{esc(value)}</code>" if value else "<code>\"\"</code>"
    return f"<code>{esc(json.dumps(value, ensure_ascii=False))}</code>"


def anchor(files: list[dict]) -> str:
    if not files:
        return "—"
    first = files[0]
    return f"<code>{esc(first.get('file',''))}:{esc(first.get('line',''))}</code>"


def render_section_rows(section: dict) -> str:
    rows = []
    for key in section["keys"]:
        status = key.get("status", "unread")
        pill = STATUS_PILL.get(status, "info")
        notes = key.get("notes")
        note_html = f'<div class="small">{esc(notes)}</div>' if notes else ""
        env = key.get("env_override")
        env_html = f"<code>{esc(env)}</code>" if env else "—"
        rows.append(
            "<tr>"
            f'<td><code>{esc(key["path"])}</code>{note_html}</td>'
            f'<td><code>{esc(key.get("type","—"))}</code></td>'
            f"<td>{fmt_default(key.get('default'))}</td>"
            f"<td>{env_html}</td>"
            f'<td><span class="pill {pill}">{esc(status)}</span></td>'
            f"<td>{anchor(key.get('python') or [])}</td>"
            f"<td>{anchor(key.get('go') or [])}</td>"
            "</tr>"
        )
    return "".join(rows)


def build_html(contract: dict) -> str:
    sections = contract["sections"]
    summary = contract.get("summary", {})
    status_counts = summary.get("status_counts", {})
    ref = contract.get("reference", {})
    tgt = contract.get("target", {})
    digest = hashlib.sha256(CONTRACT.read_bytes()).hexdigest()[:12]
    total_keys = summary.get("keys") or sum(len(s["keys"]) for s in sections)

    toc = "".join(
        f'<a href="#sec-{esc(s["name"])}">{esc(s["name"])} ({len(s["keys"])})</a>'
        for s in sections
    )

    overview_rows = "".join(
        "<tr>"
        f'<td><a href="#sec-{esc(s["name"])}"><code>{esc(s["name"])}</code></a></td>'
        f'<td class="num">{len(s["keys"])}</td>'
        f'<td>{sum(1 for k in s["keys"] if k.get("status") == "aligned")}</td>'
        f'<td>{sum(1 for k in s["keys"] if k.get("status") == "go_partial")}</td>'
        f'<td>{sum(1 for k in s["keys"] if k.get("status") in ("python_only", "go_only", "unread"))}</td>'
        "</tr>"
        for s in sections
    )

    section_blocks = []
    for s in sections:
        env_overrides = sum(1 for k in s["keys"] if k.get("env_override"))
        section_blocks.append(
            f'<h3 id="sec-{esc(s["name"])}"><code>{esc(s["name"])}</code> · {len(s["keys"])} 键'
            f'<span class="sub">（env 覆盖 {env_overrides}）</span></h3>'
            '<div class="tablewrap"><table><colgroup>'
            '<col style="width:20%"><col style="width:8%"><col style="width:10%"><col style="width:12%">'
            '<col style="width:9%"><col style="width:20%"><col style="width:21%">'
            "</colgroup><thead><tr>"
            "<th>path</th><th>type</th><th>default</th><th>env 覆盖</th><th>status</th><th>Python 读取点</th><th>Go 读取点</th>"
            "</tr></thead><tbody>" + render_section_rows(s) + "</tbody></table></div>"
        )

    boundary_rows = "".join(
        "<tr>"
        f'<td><b>{esc(r.get("rule",""))}</b></td>'
        f'<td><code>{esc(r.get("contract",""))}</code></td>'
        f'<td><span class="pill {"ok" if r.get("status")=="aligned" else "warn"}">{esc(r.get("status",""))}</span></td>'
        f'<td>{esc(r.get("notes",""))}</td>'
        "</tr>"
        for r in contract.get("boundary_rules", [])
    )

    counts_html = " · ".join(
        f'{esc(name)} <b>{count}</b>' for name, count in status_counts.items()
    )

    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{esc(TITLE)}</title>
<style>{CSS}</style>
</head><body>
<div class="wrap">

<header class="hero">
  <div class="eyebrow">GO_HIVE · CONFIGURATION</div>
  <h1>配置参考：<span>configuration.json</span></h1>
  <p class="lede">全局配置只有一份 <code>$HIVE_HOME/configuration.json</code>；环境变量按 <code>env &gt; configuration.json &gt; 内置默认</code> 覆盖，另有若干卫星文件（凭据、MCP 安装记录、colony metadata 等）。本页由 <code>scripts/gen_configuration_html.py</code> 从 <code>contracts/config-keys.json</code> 生成，字段、类型、默认值、env 覆盖与两侧读取点均以契约为准。</p>
  <div class="meta">
    <span>契约：<code>contracts/config-keys.json</code> · sha256 <b>{digest}</b></span>
    <span>键：<b>{total_keys}</b> · section：<b>{len(sections)}</b></span>
    <span>状态：{counts_html}</span>
    <span>reference：<code>{esc(ref.get("name","python"))}@{esc(str(ref.get("head",""))[:8])}</code> · 版本 <code>{esc(ref.get("version",""))}</code></span>
    <span>target：<code>{esc(tgt.get("name","go_hive"))}@{esc(str(tgt.get("head",""))[:8])}</code> · 版本 <code>{esc(tgt.get("version",""))}</code></span>
  </div>
</header>

<nav class="toc">
  <a href="#s1">01 结论</a><a href="#s2">02 文件与磁盘布局</a><a href="#s3">03 键总览</a><a href="#s4">04 逐键参考</a><a href="#s5">05 优先级与覆盖</a><a href="#s6">06 模型槽位与 judge</a><a href="#s7">07 生效时机</a><a href="#s8">08 已知差异</a><a href="#s9">09 生成与文档</a>
</nav>

<section id="s1"><h2><span class="num">01</span>结论</h2>
<div class="grid g4">
  <div class="card"><div class="k">主配置</div><div class="v">单一文件</div><div class="d">全局只有 <code>$HIVE_HOME/configuration.json</code>；Go 与 Python 同路径、同 JSON 形状。</div></div>
  <div class="card"><div class="k">字段</div><div class="v">{total_keys} 键 / {len(sections)} 段</div><div class="d">含类型、默认值、env 覆盖、Python/Go 读取点。</div></div>
  <div class="card"><div class="k">覆盖优先级</div><div class="v">env &gt; json &gt; 默认</div><div class="d">{summary.get("env_overrides", "—")} 个键支持 env 覆盖；cli/运行时另有边界规则。</div></div>
  <div class="card"><div class="k">对齐状态</div><div class="v">{status_counts.get("aligned", 0)}/{total_keys} aligned</div><div class="d">{counts_html}</div></div>
</div>
<div class="callout info"><b>口径：</b>“aligned”指键名/类型/默认值/读取行为在两侧契约中一致；“go_partial”指 Go 有读取点但语义或热切换未完全对齐；“python_only / go_only”是平台差异（见 <a href="#s8">08</a>）。状态来自契约文件，随 <code>scripts/freeze_env_config_contract.py</code> 重算。</div>
</section>

<section id="s2"><h2><span class="num">02</span>文件与磁盘布局</h2>
<h3>主配置</h3>
<div class="tablewrap"><table><thead><tr><th style="width:28%">路径（相对 <code>$HIVE_HOME</code>）</th><th>内容</th><th style="width:22%">何时出现</th></tr></thead><tbody>
<tr><td><code>configuration.json</code></td><td>全局配置（本页 §04 的 {total_keys} 键）</td><td>设置接口或人工写入时</td></tr>
<tr><td><code>env</code></td><td>环境变量/密钥文件（如凭据 key 的环境变量值），<code>hive cred</code> 与之配合</td><td>首次配置时</td></tr>
<tr><td><code>secrets/credential_key</code></td><td><code>HIVE_CREDENTIAL_KEY</code> 的持久副本（0600）</td><td>保存密钥时</td></tr>
<tr><td><code>credentials/credentials/&lt;id&gt;.enc</code> + <code>credentials/metadata/index.json</code></td><td>Fernet 加密凭据与索引（不含明文值）</td><td>凭据保存时</td></tr>
<tr><td><code>mcp_registry/installed.json</code></td><td>MCP server 安装/启用记录，与 <code>mcp.*</code> 配置配合</td><td><code>hive mcp</code> 使用时</td></tr>
<tr><td><code>colonies/&lt;name&gt;/metadata.json</code></td><td>colony 级配置与 worker 定义</td><td>colony 创建/导入时</td></tr>
<tr><td><code>queens/&lt;queen&gt;/sessions/&lt;id&gt;/</code></td><td>会话树：<code>conversations/</code>、<code>tool_tiers.json</code>、<code>meta.json</code> 等</td><td>会话创建/运行</td></tr>
<tr><td><code>trusted_repos.json</code></td><td>已信任技能来源仓库</td><td><code>hive skill</code> 信任项目时</td></tr>
<tr><td><code>senders/senders.json</code></td><td>发件账户（Go 发送面）</td><td>配置 sender 时</td></tr>
</tbody></table></div>
<p class="sub">主配置的键可被环境变量覆盖（§05）；磁盘其余文件是运行数据/卫星配置，不是第二份主配置。</p>
</section>

<section id="s3"><h2><span class="num">03</span>键总览</h2>
<div class="tablewrap"><table><thead><tr><th>section</th><th>键数</th><th>aligned</th><th>go_partial</th><th>其他</th></tr></thead><tbody>{overview_rows}</tbody></table></div>
<p class="sub">共 {total_keys} 键；点击 section 跳转逐键表。</p>
</section>

<section id="s4"><h2><span class="num">04</span>逐键参考</h2>
<p class="sub">每键列出：路径、类型、默认值、env 覆盖变量、对齐状态、Python 与 Go 的首个读取点。状态非 aligned 的键在路径下附注原因。</p>
{"".join(section_blocks)}
</section>

<section id="s5"><h2><span class="num">05</span>优先级与覆盖</h2>
<div class="callout"><b>总规则：</b><code>env &gt; configuration.json &gt; 内置默认</code>。示例：<code>HIVE_ADAPTIVE_TOOL_BUDGET</code>、<code>HIVE_STALL_CHECK</code>、<code>HIVE_MEMORY_TIMELINE</code>、<code>HIVE_RETENTION_&lt;FIELD&gt;</code> 等。</div>
<div class="tablewrap"><table><thead><tr><th style="width:22%">规则</th><th style="width:34%">契约</th><th style="width:10%">status</th><th>说明</th></tr></thead><tbody>{boundary_rows}</tbody></table></div>
</section>

<section id="s6"><h2><span class="num">06</span>模型槽位与 judge</h2>
<div class="tablewrap"><table><thead><tr><th style="width:24%">槽位</th><th>用途</th><th style="width:34%">生效时机</th></tr></thead><tbody>
<tr><td><code>llm</code></td><td>默认会话模型（对话与工具循环）</td><td>写入即重建运行中会话 transport；下一回合生效</td></tr>
<tr><td><code>queen_llm.&lt;queen_id&gt;</code></td><td>指定 queen 的模型</td><td>会话启动按 queen 选模型；写入即热切换</td></tr>
<tr><td><code>worker_llm</code></td><td>colony worker 专用模型</td><td>与上游一致：只对下一个 colony/会话生效</td></tr>
<tr><td><code>vision_fallback</code></td><td>文本主模型的工具图片脚注（caption）</td><td>逐次 caption 解析，写入即生效</td></tr>
<tr><td><code>judge</code></td><td>概率裁判（typesafe：<code>POST {{api_base}}/systemone</code>，读 <code>noul</code>）；stall check 等消费者</td><td>每次判定新建 client，写入即生效</td></tr>
</tbody></table></div>
<h3>judge 配置示例（本地 startlux-decide 实测）</h3>
<pre>{{
  "judge": {{
    "protocol": "typesafe",
    "api_base": "http://127.0.0.1:18099/v1",
    "model": "&lt;model&gt;",
    "api_key": "&lt;非空，可占位&gt;",
    "threshold": 0.5
  }},
  "stall_check": true
}}</pre>
<p class="sub">judge 槽要求 <code>api_base</code> 与 <code>api_key</code> 均非空，否则 <code>JudgeSlot</code> 返回空、消费者静默不工作；本地服务不校验时填占位值即可。真实 E2E：queen 只说不做的一轮 judge 返回 p=0.68 ≥ 0.5，注入 <code>&lt;system-reminder&gt;</code> 后女王补做。</p>
<h3>llm 段最小示例</h3>
<pre>{{
  "llm": {{
    "provider": "openai",
    "model": "&lt;model&gt;",
    "api_base": "https://token.tokenbroad.com/v1",
    "api_key_env_var": "BROADTOKEN_API_KEY"
  }}
}}</pre>
</section>

<section id="s7"><h2><span class="num">07</span>生效时机</h2>
<div class="tablewrap"><table><thead><tr><th style="width:36%">改动</th><th>何时生效</th></tr></thead><tbody>
<tr><td>环境变量（key、base）</td><td>重启 <code>hive serve</code> 后，新建的会话</td></tr>
<tr><td><code>configuration.json</code> 的 <code>llm</code></td><td>重启 <code>hive serve</code> 后，新建的会话</td></tr>
<tr><td><code>HIVE_MAX_ITER</code></td><td>会话循环创建时读取（已运行会话不受影响）</td></tr>
<tr><td><code>HIVE_SPLIT_WORKER_EVENTS</code></td><td>colony 启动时读取（重启 serve 后生效）</td></tr>
<tr><td><code>HIVE_STALL_CHECK</code> / <code>stall_check</code></td><td>服务端每次判定时读取，写入即生效</td></tr>
<tr><td><code>judge.*</code></td><td>每次判定新建 client，写入即生效</td></tr>
<tr><td>设置接口 <code>PUT /api/config/...</code></td><td>新会话生效；主槽 <code>llm</code>/<code>queen_llm</code> 写入即热切换；<code>vision_fallback</code> 下一张图生效；<code>worker_llm</code> 只对下一个 colony/会话生效；进行中回合用旧 provider 跑完</td></tr>
<tr><td>colony 的 worker 定义</td><td>下次 spawn 时读取</td></tr>
</tbody></table></div>
</section>

<section id="s8"><h2><span class="num">08</span>已知差异</h2>
<div class="tablewrap"><table><thead><tr><th style="width:20%">状态</th><th style="width:10%">数量</th><th>含义与后续</th></tr></thead><tbody>
<tr><td><span class="pill ok">aligned</span></td><td class="num">{status_counts.get("aligned", "—")}</td><td>键名/类型/默认值/读取行为两侧一致</td></tr>
<tr><td><span class="pill warn">go_partial</span></td><td class="num">{status_counts.get("go_partial", "—")}</td><td>Go 有读取点，但语义或热切换尚未完全对齐；逐键原因见 §04 与 <code>docs/env-config-contract-diff.md</code></td></tr>
<tr><td><span class="pill info">python_only</span></td><td class="num">{status_counts.get("python_only", "—")}</td><td>上游有、Go 无消费路径（如 <code>channels.*</code> 插件宿主）；已在支持台账登记，不静默忽略</td></tr>
<tr><td><span class="pill info">go_only</span></td><td class="num">{status_counts.get("go_only", "—")}</td><td>Go 扩展键（如 <code>mcp.*</code> 部分字段）；Python 不读取，双向兼容时按扩展标注</td></tr>
<tr><td><span class="pill bad">unread</span></td><td class="num">{status_counts.get("unread", "—")}</td><td>尚未定性，待逐键核对</td></tr>
</tbody></table></div>
<div class="callout bad"><b>待办（config-verbatim lane 口径）：</b>先把 <code>go_partial</code> 能接的接入真实消费路径；不能接的（antigravity OAuth、channels 插件宿主）给出结构性理由；unread 键逐一定性。合并后本页由生成器自动刷新计数。</div>
</section>

<section id="s9"><h2><span class="num">09</span>生成与文档</h2>
<ul>
  <li>生成器：<code>go_hive/scripts/gen_configuration_html.py</code>；数据源 <code>go_hive/contracts/config-keys.json</code>（sha256 <code>{digest}</code>）。</li>
  <li>防漂移：<code>python3 scripts/gen_configuration_html.py --check</code>（不一致退出 1）。</li>
  <li>Markdown 参考：<code>go_hive/docs/configuration.md</code>；逐键差异报告：<code>go_hive/docs/env-config-contract-diff.md</code>；env 面：<code>go_hive/docs/environment-variables.html</code>。</li>
  <li>契约重算：<code>go_hive/scripts/freeze_env_config_contract.py</code> + <code>check_env_config_contract.py --strict</code>，然后重跑本生成器。</li>
</ul>
</section>

<footer>由 <code>gen_configuration_html.py</code> 从 <code>contracts/config-keys.json</code> 生成 · 键数 {total_keys} · 状态随契约重算 · 不写入任何密钥值</footer>
</div></body></html>
"""


def main() -> int:
    contract = json.loads(CONTRACT.read_text())
    html = build_html(contract)
    if "--check" in sys.argv:
        if OUTPUT.exists() and OUTPUT.read_text() == html:
            print(f"ok: {OUTPUT} matches {CONTRACT}")
            return 0
        print(f"stale: {OUTPUT} differs from {CONTRACT}; run gen_configuration_html.py")
        return 1
    OUTPUT.write_text(html)
    print(f"wrote {OUTPUT} ({len(html)} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
