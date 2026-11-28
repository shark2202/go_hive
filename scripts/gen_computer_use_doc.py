#!/usr/bin/env python3
"""Generate the computer-use port assessment (zh-CN HTML + JSON).

This page is intentionally an *assessment*, not a parity claim. It captures the
external capability (`oh-my-pi` computer-use) and the go_hive decision boundary:
Hive has no desktop computer-use, go_hive is CGO-free, and full native
cross-platform computer-use would need a separate native sidecar.

Usage:
  uv run --no-project python go_hive/scripts/gen_computer_use_doc.py
  uv run --no-project python go_hive/scripts/gen_computer_use_doc.py --check
"""

from __future__ import annotations

import argparse
import html
import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive/docs/computer-use-port.html"
OUT_JSON = REPO / "go_hive/docs/computer-use-port.json"
STYLE_SOURCE = REPO / "go_hive/docs/goal-design.html"

AS_OF = "2026-11-23"
GO_BASELINE = "go_hive main @ d2093cba (assessment snapshot)"
SOURCE_URL = "https://github.com/can1357/oh-my-pi/blob/main/docs/computer-use.md"
SOURCE_RAW = "https://raw.githubusercontent.com/can1357/oh-my-pi/main/docs/computer-use.md"

ESC = lambda value: html.escape(str(value), quote=True)


def code(value: str) -> str:
    return f"<code>{ESC(value)}</code>"


def bullets(items: list[str]) -> str:
    return "\n".join(f"<li>{item}</li>" for item in items)


def table(headers: list[str], rows: list[list[str]], widths: list[str] | None = None) -> str:
    cols = ""
    if widths:
        cols = "<colgroup>" + "".join(f'<col style="width:{w}">' for w in widths) + "</colgroup>"
    head = "".join(f"<th>{ESC(h)}</th>" for h in headers)
    body = "\n".join(
        "<tr>" + "".join(f"<td>{cell}</td>" for cell in row) + "</tr>" for row in rows
    )
    return f'<div class="tablewrap"><table>{cols}<thead><tr>{head}</tr></thead><tbody>{body}</tbody></table></div>'


def load_style() -> str:
    if not STYLE_SOURCE.exists():
        return ""
    match = re.search(r"<style>.*?</style>", STYLE_SOURCE.read_text(encoding="utf-8"), re.S)
    return match.group(0) if match else ""


API_SURFACE = [
    ("窗口与显示器", "windows(app?, title?) / window(idOrFilter) / focusedWindow() / display(active|all|id)", "枚举、选择、等待目标窗口或显示器；窗口句柄带 id/app/title/pid/bounds。"),
    ("截图", "screenshot({silent?}) / zoom({x,y,width,height})", "全窗口或显示器截图；坐标基于最近一次全量截图；区域放大不替换点击帧。"),
    ("原生输入", "click / doubleClick / drag / holdMouse / type / press / holdKeys", "按键、鼠标、按住、拖拽；坐标输入必须先有截图。"),
    ("AX 无障碍", "ax() / find() / ref() / elementAt(x,y) / focusedElement()", "读取 OS Accessibility/AX 树，按角色/标题/描述定位元素；引用有 stale-ref 语义。"),
    ("元素动作", "el.click() / el.type() / el.press() / el.setValue() / el.perform()", "优先语义动作，不依赖截图；支持后台与 takeover 两种投递路径。"),
    ("菜单", "win.menu.items() / win.menu.select([...])", "按标题选择菜单路径；不猜快捷键。"),
    ("剪贴板", "computer.clipboard.read() / write()", "读写系统剪贴板。"),
    ("应用", "computer.apps.list() / open(idOrNameOrPath, {activate?, reveal?})", "枚举/打开应用；部分能力仅 macOS。"),
    ("批量脚本", "computer.run(fnOrCode, {args?, read_only?, timeout?})", "同一 worker session 内运行多步操作；返回结构化值，截图作为 Eval 图像。"),
    ("能力探测", "computer.capabilities()", "报告 capture/input/AX/permission/screenLocked/displayAsleep 等能力。"),
    ("安全控制", "control.acquire() / release() / state()", "请求显式前台控制；需要人工确认或策略批准。"),
    ("等待", "wait(fn, {timeout?, interval?})", "等待窗口/元素/条件出现。"),
]

PLATFORMS = [
    ["macOS x64/arm64", "ScreenCaptureKit（14+）/ CoreGraphics（12/13）+ native AX + input", "需要 Screen Recording + Accessibility；由启动 omp 的终端/IDE 授权；部分 apps.open 能力仅 macOS。"],
    ["Linux X11 x64/arm64", "X11 capture/input + AT-SPI", "需要可读 display、RandR/XTEST；后台输入依赖 XI2、/dev/uinput、udev/libinput 与 toolkit/window manager。"],
    ["Linux Wayland x64/arm64", "RemoteDesktop portal / LIBEI + AT-SPI；ScreenCast portal/PipeWire", "逐窗口后台输入不可用；需要 PipeWire/libpipewire；compositor 限制；权限会话级。"],
    ["Windows x64/arm64", "Win32 display/window capture + Win32 input + UI Automation", "background type/press 对无窗口元素会 BackgroundUnavailable；UIA/COM 与 DPI 适配。"],
    ["其它已发布目标", "Unsupported unless native addon reports capabilities", "没有 backend 就不能假装可用。"],
]

CONSTRAINTS = [
    "Hive 上游没有 computer-use；这是新增能力，不是 go_hive 的 parity 项。",
    "go_hive 当前约束是纯 Go、CGO_ENABLED=0；macOS ScreenCaptureKit/CoreGraphics/AX 通常需要 cgo/ObjC/Swift。",
    "现有 go_hive 只有浏览器域（Browser Bridge / browser_*）与 terminal_exec；没有窗口枚举、AX tree、原生鼠标键盘注入。",
    "当前发布规则：先完成 Hive 对齐，未完成前不新增功能；因此不能把 computer-use 塞进 1.0 对齐范围。",
    "安全模型不可省略：屏幕是不可信输入，原生输入要审批、可中断、可回滚，最好在专用账号/VM 中运行。",
    "跨平台一致性是 per-platform 能力矩阵，不是同一套语义；oh-my-pi 自己也列出了 Wayland/Windows/macOS 差异。",
]

OPTIONS = [
    ["A. OS CLI 包装", "调用 screencapture / osascript / grim / xdotool / PowerShell", "快；不改 Go 依赖", "没有 AX tree、后台输入、权限/审批；Linux/Wayland/Windows 差异大；不能称 computer-use 对齐"],
    ["B. 独立 native sidecar（推荐，若以后做）", "Go core ↔ JSON-RPC/stdio ↔ Swift/ObjC/Rust/C++ helper", "Go 主二进制仍可 CGO=0；平台能力隔离；可独立签名/授权", "新增发布产物、安装/公证、TCC、Linux 桌面矩阵、Windows UIA/DPI、安全审计"],
    ["C. 全部塞进 Go core", "cgo/native bindings 直接进核心", "单一二进制", "违反 CGO_ENABLED=0；平台条件编译复杂；安全边界差；不建议"],
    ["D. 不移植（当前建议）", "继续使用 Browser Bridge / terminal；需要截图时人工 screencapture + attach_file", "不扩大攻击面；不违反对齐约束", "不能控制原生应用；不是 computer-use"],
]

ACCEPTANCE = [
    "默认关闭，显式配置开启；capabilities() 必须报告当前平台真实能力，而不是假设可用。",
    "截图必须明确目标显示器/窗口、坐标帧、DPI/缩放；resize/display layout change 后必须重新截图。",
    "每次输入先校验坐标来自最近一次同目标全量截图；AX 引用要能报告 stale ref。",
    "审批/权限：读操作与写操作分级；对话式批准；Escape/中断释放按键与鼠标；前台 takeover 必须可回退焦点。",
    "不得把屏幕内容当作用户授权；危险操作（发送、购买、删除、权限变更）要二次确认。",
    "每个平台单独 E2E：macOS Screen Recording/Accessibility、Linux X11、Wayland portal、Windows UIA/DPI；无 backend 时返回明确错误。",
    "不在没有浏览器的 headless 服务器上伪造成功；返回 Unsupported/Unavailable，而不是空截图。",
]


def build() -> tuple[str, dict]:
    style = load_style()
    data = {
        "schema": "hive.computer-use-port-assessment/v1",
        "as_of": AS_OF,
        "go_baseline": GO_BASELINE,
        "source": {"url": SOURCE_URL, "raw": SOURCE_RAW},
        "verdict": "possible_but_not_now_new_feature_needs_native_sidecar",
        "api_surface": [{"area": a, "api": b, "notes": c} for a, b, c in API_SURFACE],
        "platforms": [
            {"platform": p, "backend": b, "constraints": c} for p, b, c in PLATFORMS
        ],
        "go_hive_constraints": CONSTRAINTS,
        "options": [
            {"id": o[0].split(".", 1)[0], "name": o[0], "approach": o[1], "benefits": o[2], "costs": o[3]}
            for o in OPTIONS
        ],
        "acceptance": ACCEPTANCE,
        "decision": "Do not add to go_hive 1.0; if needed later, build an opt-in native sidecar, not a cgo core change.",
    }

    api_rows = [[f"<b>{ESC(a)}</b>", f"<code>{ESC(b)}</code>", ESC(c)] for a, b, c in API_SURFACE]
    platform_rows = [[f"<b>{ESC(p)}</b>", f"<code>{ESC(b)}</code>", ESC(c)] for p, b, c in PLATFORMS]
    option_rows = [
        [f"<b>{ESC(o[0])}</b>", ESC(o[1]), ESC(o[2]), ESC(o[3])] for o in OPTIONS
    ]

    page = f"""<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Computer-use 移植评估 · go_hive</title>
{style}
<style>
.wrap{{overflow-wrap:break-word}}
.wrap h1 span{{white-space:nowrap}}
.wrap h2,.wrap p,.wrap .sub{{word-break:normal}}
.wrap .sub,.wrap .card .d{{word-break:keep-all}}
.wrap code{{overflow-wrap:break-word;word-break:normal}}
.pill{{display:inline-block;border:1px solid var(--line);border-radius:999px;padding:3px 9px;font-size:11px;color:var(--muted);margin-left:8px;vertical-align:middle}}
.pill.ok{{border-color:#1f6f3b;color:#7ee2a8;background:#0d1b12}}
.pill.warn{{border-color:#7a5a12;color:#f0c75e;background:#1b1608}}
.pill.bad{{border-color:#7f2a2a;color:#ff9e9e;background:#1c0e0e}}
.callout b{{color:#fff}}
</style>
</head><body>
<div class="wrap">
<header>
<div class="eyebrow">HIVE / GO_HIVE · CAPABILITY ASSESSMENT</div>
<h1>Computer-use 移植评估 <span class="pill warn">非对齐项</span></h1>
<p class="lede">把 <a href="{SOURCE_URL}">oh-my-pi 的 scriptable computer use</a> 作为外部能力样本，评估 go_hive 能否、以及应该以什么方式移植。结论先行：<b>技术上可以，但当前不应把它加入 go_hive 1.0；全量实现需要独立 native sidecar，而不是纯 Go 核心。</b></p>
<div class="meta"><span>评估快照：<b>{AS_OF}</b></span><span>项目快照：<code>{GO_BASELINE}</code></span><span>源文档：<a href="{SOURCE_URL}">docs/computer-use.md</a></span><span>生成器：<code>go_hive/scripts/gen_computer_use_doc.py</code></span></div>
</header>

<div class="callout info"><b>用途：</b>这是后续复用资料，不是 Hive parity 声明，也不是 go_hive 已实现的功能说明。任何“可用”结论都必须以本机真实平台 E2E 为准。</div>

<div class="toc"><a href="#s1">01 结论</a><a href="#s2">02 上游能力</a><a href="#s3">03 平台差异</a><a href="#s4">04 go_hive 约束</a><a href="#s5">05 可选方案</a><a href="#s6">06 验收清单</a><a href="#s7">07 决策</a></div>

<section id="s1"><h2><span class="num">01</span>结论速览</h2>
<div class="grid g4">
<div class="card ok"><div class="k">技术上可否</div><div class="v">可以</div><div class="d">需要 per-OS native backend</div></div>
<div class="card bad"><div class="k">纯 Go / CGO=0</div><div class="v">不能全量</div><div class="d">macOS AX/捕获、Windows UIA、Linux AT-SPI 都是原生接口</div></div>
<div class="card warn"><div class="k">当前是否该做</div><div class="v">不</div><div class="d">Hive 无此特性；属于新增功能，违反先对齐约束</div></div>
<div class="card"><div class="k">若未来做</div><div class="v">sidecar</div><div class="d">Go 主进程保持 CGO=0；默认关闭、显式审批</div></div>
</div>
<p><b>一句话：</b>{ESC("go_hive 可以移植这个能力，但不能在当前纯 Go 无 cgo、先完成 Hive 对齐的约束下，把它当作一个普通工具塞进核心。")}</p>
</section>

<section id="s2"><h2><span class="num">02</span>上游能力边界</h2>
<p>oh-my-pi 的 <code>computer</code> 不是浏览器 DOM API，而是控制宿主机桌面的 Eval prelude。它和 browser prelude 的分工是：</p>
{table(["域", "负责", "不负责"], [
    ["<b>computer</b>", "原生窗口/显示器、截图、原生输入、AX tree、剪贴板", "网页 DOM 选择器、页面 JS、CDP tab 控制"],
    ["<b>browser</b>", "网页 DOM/ARIA、页面 JS、CDP tab", "原生应用、桌面窗口、系统级键盘鼠标"],
], ["18%", "41%", "41%"])}
<p class="sub">关键 API 摘要（详细语义见源文档；这里只用于评估移植面，不作为完整 API 契约）。</p>
{table(["能力", "API 例子", "作用"], api_rows, ["16%", "45%", "39%"])}
<div class="callout"><b>重要：</b>它是一个带状态的 worker/session：截图帧、窗口句柄、AX 引用、输入锁、权限、审批和超时都属于语义的一部分。只复制几个函数名不构成移植。</div>
</section>

<section id="s3"><h2><span class="num">03</span>平台后端与差异</h2>
{table(["平台", "原生 backend", "必须接受的限制"], platform_rows, ["16%", "39%", "45%"])}
<p>所以“跨平台一致”应定义为 <b>每个平台按自己的能力矩阵正确工作</b>，不是所有平台拥有完全相同的 API/行为。仅凭 Cargo/Go 条件编译不能保证语义一致。</p>
</section>

<section id="s4"><h2><span class="num">04</span>go_hive 当前约束</h2>
<div class="callout bad"><b>当前判定：不进入 1.0。</b>Hive 上游没有 computer-use，go_hive 也没有桌面层。把它加入核心等于新增产品面，而不是补齐 Hive。</div>
<ul>{bullets(CONSTRAINTS)}</ul>
<h3>现有相近能力</h3>
{table(["现有能力", "能做什么", "不能替代 computer-use 的原因"], [
    [code("Browser Bridge / browser_*"), "网页 DOM、标签页、CDP、页面 JS", "不能操作原生应用；不是桌面截图/AX/系统输入"],
    [code("terminal_exec"), "运行 CLI，可间接调用系统命令", "没有窗口枚举、AX tree、坐标输入、权限/审批模型"],
    [code("screencapture + attach_file"), "人工截屏后作为附件", "不是 agent 可连续控制的桌面 runtime"],
], ["27%", "36%", "37%"])}
</section>

<section id="s5"><h2><span class="num">05</span>可选移植方案</h2>
{table(["方案", "形态", "收益", "代价/结论"], option_rows, ["20%", "27%", "23%", "30%"])}
<div class="callout"><b>推荐顺序：</b>当前选 D（不移植）；若未来产品明确需要，先做 B（独立 native sidecar）的 macOS 单平台 spike，再决定是否扩到 Linux/Windows。不要先做 A 然后宣称“已支持 computer-use”。</div>
<h3>为什么 sidecar 比 cgo 核心更合适</h3>
<ul>
<li>Go 主二进制继续满足 <code>CGO_ENABLED=0</code>，不把原生 ABI 带进核心。</li>
<li>macOS 的 ScreenCaptureKit/AX、Windows 的 UIA/COM、Linux 的 X11/AT-SPI 可以各自编译、签名、测试。</li>
<li>权限/审批/崩溃隔离在 helper 边界；helper 不可用时返回明确 Unsupported，而不是伪造截图。</li>
<li>升级和发布可以按平台独立灰度，不影响 go_hive 核心 slot。</li>
</ul>
</section>

<section id="s6"><h2><span class="num">06</span>若未来实施：验收清单</h2>
<ol>{bullets(ACCEPTANCE)}</ol>
<div class="callout warn"><b>安全底线：</b>屏幕内容是不可信输入，不能授权动作；高风险操作必须二次确认；优先专用账号或 VM。缺少审批、Escape 停止、输入锁和权限失败路径的实现，不算 computer-use。</div>
</section>

<section id="s7"><h2><span class="num">07</span>决策</h2>
<p><b>当前决策：</b>不把 computer-use 加入 go_hive 1.0；不为它改 <code>CGO_ENABLED=0</code>、不新增原生依赖、不把它登记为 Hive 对齐项。</p>
<p><b>重开条件：</b>Hive 对齐封版后，若出现明确的原生桌面自动化需求，再按“独立 native sidecar + 默认关闭 + 显式审批 + 每平台 E2E”立项；先做 macOS spike，用真实窗口/AX/输入证据决定是否扩平台。</p>
<p><b>复用方式：</b>把本页作为需求评估和架构决策输入；不要把本页的 API 摘要当作已实现契约，实际实现必须另立 schema、权限和测试。</p>
</section>

<footer>
<p>源：<a href="{SOURCE_URL}">oh-my-pi docs/computer-use.md</a> · 评估日期 {AS_OF} · 生成器 <code>go_hive/scripts/gen_computer_use_doc.py</code>（<code>--check</code> 防漂移）</p>
<p>这不是 Hive 上游能力；Hive/go_hive 当前没有原生桌面控制。所有未来结论以对应平台的真实 E2E 为准。</p>
</footer>
</div>
</body></html>
"""
    return page, data


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    page, data = build()
    payload = json.dumps(data, ensure_ascii=False, indent=2) + "\n"
    if args.check:
        problems = []
        if not OUT_HTML.exists() or OUT_HTML.read_text(encoding="utf-8") != page:
            problems.append(str(OUT_HTML))
        if not OUT_JSON.exists() or OUT_JSON.read_text(encoding="utf-8") != payload:
            problems.append(str(OUT_JSON))
        if problems:
            print("DRIFT: " + ", ".join(problems))
            return 1
        print("computer_use_doc: OK")
        return 0
    OUT_HTML.write_text(page, encoding="utf-8")
    OUT_JSON.write_text(payload, encoding="utf-8")
    print(f"wrote {OUT_HTML.relative_to(REPO)} ({len(page)} bytes)")
    print(f"wrote {OUT_JSON.relative_to(REPO)} ({len(payload)} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
