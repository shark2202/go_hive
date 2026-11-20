#!/usr/bin/env python3
"""定时刷新 go_hive 外观对齐快照。

跑三件事，然后把结果写进两个落盘文件（供人对账、供 cron 原样投递摘要）：

1. 静态扫描：Python `add_*` 路由 vs Go `HandleFunc` 路由（method+path）、
   前端 vs Go 前端文件哈希、skills/config 路由计数；
2. `surface_audit.py --json`：前端→Go / Python→Go 缺失与豁免；
3. `api_parity.py --skip-flow --skip-restart`：18 个端点契约探针（不调 LLM）。

输出：
- `go_hive/docs/surface-tracking-matrix.html`：更新 SNAPSHOT 标记区块与页头时间；
- `go_hive/docs/module-interfaces.html`：更新 INTERFACES 标记区块与页头时间；
- `go_hive/docs/tool-parity-matrix.html`：更新 TOOLS 标记区块（上游模块/action 实测）、页头与页脚时间；
- `go_hive/docs/surface-matrix-snapshot.json`：机器可读快照；
- stdout 一行摘要（no_agent cron 原样投递）。

用法：
    uv run python go_hive/scripts/refresh_surface_matrix.py [--skip-probe]

只依赖标准库。
"""

from __future__ import annotations

import argparse
import ast
import contextlib
import datetime
import hashlib
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve()
ROOT = HERE.parents[2]                     # 仓库根 /Users/mac/hive
GO_ROOT = ROOT / "go_hive"
GO_MODULE = GO_ROOT / "go-hive"
HTML_PATH = GO_ROOT / "docs" / "surface-tracking-matrix.html"
INTERFACES_HTML = GO_ROOT / "docs" / "module-interfaces.html"
SNAP_PATH = GO_ROOT / "docs" / "surface-matrix-snapshot.json"
TOOL_MATRIX = GO_ROOT / "docs" / "tool-parity-matrix.html"
UPSTREAM_TOOLS_DIR = ROOT / "tools" / "src" / "aden_tools" / "tools"
AUDIT_SCRIPT = GO_ROOT / "scripts" / "surface_audit.py"
PARITY_SCRIPT = GO_ROOT / "scripts" / "api_parity.py"
PY_SERVER_DIR = ROOT / "core" / "framework" / "server"
GO_SERVER_DIR = GO_MODULE / "internal" / "server"
FE_PY = ROOT / "core" / "frontend"
FE_GO = GO_MODULE / "frontend"
EVENT_TYPE_FILE = GO_MODULE / "internal" / "eventbus" / "event_type.go"
EXTENSION_JS = ROOT / "tools" / "browser-extension" / "background.js"
MCP_INDEX_SCHEMA = GO_ROOT / "docs" / "mcp-registry" / "index.schema.json"

# cron 的 PATH 很干净；把本机工具链目录补进来。
PATH_EXTRA = [
    str(Path.home() / ".local" / "bin"),
    str(Path.home() / ".vfox" / "sdks" / "golang" / "bin"),
    "/opt/homebrew/bin",
    str(Path.home() / ".npm-global" / "bin"),
    "/usr/local/bin",
]
os.environ["PATH"] = os.pathsep.join(PATH_EXTRA + [os.environ.get("PATH", "")])

SKIP_DIRS = {"node_modules", "dist", ".next", "coverage", ".vite", ".DS_Store"}
PY_ROUTE_RE = re.compile(r'add_(get|post|put|patch|delete)\(\s*["\']([^"\']+)["\']')
GO_ROUTE_RE = re.compile(r'HandleFunc?\(\s*"(GET|POST|PUT|PATCH|DELETE) ([^"]+)"')


def now_stamp() -> str:
    return datetime.datetime.now().astimezone().strftime("%Y-%m-%d %H:%M %Z")


def git_head() -> str:
    try:
        proc = subprocess.run(
            ["git", "-C", str(GO_MODULE), "rev-parse", "--short", "HEAD"],
            capture_output=True, text=True, timeout=10,
        )
        return proc.stdout.strip() if proc.returncode == 0 else ""
    except Exception:
        return ""


def python_version() -> str:
    try:
        text = (ROOT / "core" / "pyproject.toml").read_text(encoding="utf-8")
        block = re.search(r"\[project\]([\s\S]*?)(?=\n\[)", text)
        if block:
            match = re.search(r'^version\s*=\s*"([^"]+)"', block.group(1), re.MULTILINE)
            if match:
                return match.group(1)
    except OSError:
        pass
    return ""


def go_version() -> str:
    try:
        text = (GO_MODULE / "internal" / "version" / "version.go").read_text(encoding="utf-8")
        match = re.search(r'const\s+Version\s*=\s*"([^"]+)"', text)
        if match:
            return match.group(1)
    except OSError:
        pass
    return ""


def count_event_types() -> int:
    try:
        text = EVENT_TYPE_FILE.read_text(encoding="utf-8")
        return len(re.findall(r'Event[A-Za-z0-9]*\s+EventType\s*=\s*"', text))
    except OSError:
        return 0


def extension_protocol_version() -> str:
    try:
        match = re.search(r"EXTENSION_PROTOCOL_VERSION\s*=\s*(\d+)", EXTENSION_JS.read_text(encoding="utf-8"))
        if match:
            return match.group(1)
    except OSError:
        pass
    return ""


def count_mcp_transports() -> int:
    try:
        text = MCP_INDEX_SCHEMA.read_text(encoding="utf-8")
        block = re.search(r'"transportName"[\s\S]*?"enum"\s*:\s*\[([^\]]*)\]', text)
        if block:
            return len(re.findall(r'"[a-z]+"', block.group(1)))
    except OSError:
        pass
    return 0


def scan_routes() -> dict:
    py = set()
    for path in sorted(PY_SERVER_DIR.glob("*.py")):
        for m in PY_ROUTE_RE.finditer(path.read_text(errors="ignore")):
            py.add((m.group(1).upper(), m.group(2)))
    go = set()
    for path in sorted(GO_SERVER_DIR.glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        for m in GO_ROUTE_RE.finditer(path.read_text(errors="ignore")):
            go.add((m.group(1), m.group(2)))
    return {
        "go_total": len(go),
        "python_total": len(py),
        "exact": len(py.intersection(go)),
        "python_only": sorted(f"{m} {p}" for m, p in py.difference(go)),
        "go_only": sorted(f"{m} {p}" for m, p in go.difference(py)),
        "skills_routes_exact": sum(1 for m, p in py.intersection(go) if "skill" in p),
        "skills_routes_total": sum(1 for m, p in py if "skill" in p),
        "config_routes_exact": sum(1 for m, p in py.intersection(go) if "/api/config" in p),
        "config_routes_total": sum(1 for m, p in py if "/api/config" in p),
    }


def scan_frontend() -> dict:
    def files(root: Path) -> dict:
        out = {}
        for p in root.rglob("*"):
            if any(part in SKIP_DIRS for part in p.parts):
                continue
            if p.is_file():
                out[str(p.relative_to(root))] = hashlib.sha256(p.read_bytes()).hexdigest()
        return out

    a, b = files(FE_PY), files(FE_GO)
    shared = set(a).intersection(b)
    return {
        "shared_paths": len(shared),
        "go_only": len(set(b).difference(a)),
        "python_only": len(set(a).difference(b)),
        "content_diff": sum(1 for k in shared if a[k] != b[k]),
    }


def parse_tool_matrix() -> dict:
    out = {}
    if not TOOL_MATRIX.is_file():
        return out
    text = TOOL_MATRIX.read_text(errors="ignore")
    for key, label in (("modules", "集成模块"), ("actions", "集成 Action"), ("default_tools", "默认会话工具")):
        m = re.search(r"<div class=\"k\">" + label + r"</div><div class=\"v\">([^<]+)</div>", text)
        if m:
            out[key] = m.group(1).strip()
    return out


def count_upstream_tools() -> dict:
    """实测上游集成工具面：带 @mcp.tool 的模块数与 action 数。"""
    modules = 0
    actions = 0
    if not UPSTREAM_TOOLS_DIR.is_dir():
        return {"modules": modules, "actions": actions, "error": "upstream tools dir missing"}
    for path in sorted(UPSTREAM_TOOLS_DIR.glob("*/*.py")):
        if path.name.startswith("_"):
            continue
        try:
            tree = ast.parse(path.read_text(errors="ignore"))
        except SyntaxError:
            continue
        count = 0
        for node in ast.walk(tree):
            if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                continue
            for deco in node.decorator_list:
                if isinstance(deco, ast.Call) and getattr(deco.func, "attr", "") == "tool":
                    count += 1
                elif isinstance(deco, ast.Attribute) and deco.attr == "tool":
                    count += 1
        if count:
            modules += 1
            actions += count
    return {"modules": modules, "actions": actions}


def render_tools_block(snap: dict) -> str:
    live = snap.get("upstream_tools") or {}
    tools = snap.get("tools") or {}

    def esc(text: object) -> str:
        return str(text).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

    rec_mod = (tools.get("modules", "").split("/")[0].strip() or "?")
    rec_act = (tools.get("actions", "").split("/")[-1].strip() or "?")
    mod_ok = str(live.get("modules")) == rec_mod
    act_ok = str(live.get("actions")) == rec_act
    if mod_ok and act_ok:
        verdict = '<span class="pill ok">一致</span>'
        detail = ""
    else:
        verdict = '<span class="pill bad">不一致，需更新正文</span>'
        detail = (
            f' 正文卡片记录 {esc(rec_mod)} 模块 / {esc(rec_act)} action，'
            f'实测 {esc(live.get("modules"))} / {esc(live.get("actions"))}——请同步总览卡片与 §01 标题。'
        )
    go_side = "、".join(
        part for part in (
            f"集成 action {esc(tools.get('actions'))}" if tools.get("actions") else "",
            f"默认会话 {esc(tools.get('default_tools'))}" if tools.get("default_tools") else "",
        ) if part
    )
    err = live.get("error")
    err_note = f' <span class="pill bad">{esc(err)}</span>' if err else ""
    return (
        '<div class="callout info"><b>自动核对：</b>'
        f'刷新 {esc(snap["generated_at"])} · HEAD <code>{esc(snap.get("commit") or "?")}</code> · '
        f'上游实测 <b>{esc(live.get("modules"))} 模块 / {esc(live.get("actions"))} action</b>'
        '（AST 扫描 <code>tools/src/aden_tools/tools/*</code>）· '
        f'与正文卡片 {verdict}。{detail}{err_note}'
        f'Go 侧数字（{go_side}、browser 22/22）由正文人工维护，Go 变更后需同步；'
        '此块由 <code>refresh_surface_matrix.py</code> 定时重写。</div>'
    )


def update_tool_matrix(snap: dict) -> None:
    if not TOOL_MATRIX.is_file():
        return
    html = TOOL_MATRIX.read_text(encoding="utf-8")
    if "<!-- TOOLS:START -->" not in html or "<!-- TOOLS:END -->" not in html:
        return
    block = render_tools_block(snap)
    html = re.sub(
        r"<!-- TOOLS:START -->.*?<!-- TOOLS:END -->",
        "<!-- TOOLS:START -->\n" + block + "\n<!-- TOOLS:END -->",
        html,
        count=1,
        flags=re.DOTALL,
    )
    html = re.sub(
        r"(<span>生成：<b>)([^<]*)(</b></span>)",
        lambda m: m.group(1) + snap["generated_at"] + m.group(3),
        html,
        count=1,
    )
    html = re.sub(
        r"(<footer>生成：)\d{4}-\d{2}-\d{2} \d{2}:\d{2} [A-Z]+",
        lambda m: m.group(1) + snap["generated_at"],
        html,
        count=1,
    )
    if snap.get("commit"):
        html = re.sub(
            r"(Go HEAD：<b>)[0-9a-f]+(</b>)",
            lambda m: m.group(1) + snap["commit"] + m.group(2),
            html,
            count=1,
        )
    tmp = TOOL_MATRIX.with_suffix(".html.tmp")
    tmp.write_text(html, encoding="utf-8")
    os.replace(tmp, TOOL_MATRIX)


def run_surface_audit() -> dict:
    uv = shutil.which("uv")
    if uv is None:
        return {"error": "uv not found on PATH"}
    try:
        proc = subprocess.run(
            [uv, "run", "--no-project", "python", str(AUDIT_SCRIPT), "--json"],
            cwd=str(ROOT), capture_output=True, text=True, timeout=300,
        )
    except subprocess.TimeoutExpired:
        return {"error": "surface_audit timeout"}
    if proc.returncode not in (0, 1):  # --strict 未开时缺失也返回 0；1 留给严格模式
        return {"error": f"surface_audit exit {proc.returncode}", "stderr": proc.stderr[-400:]}
    try:
        payload = json.loads(proc.stdout)
    except ValueError:
        return {"error": "surface_audit output is not JSON", "stdout": proc.stdout[-400:]}
    counts = payload.get("counts", {})
    return {
        "generated_at": payload.get("generated_at"),
        "frontend_missing": counts.get("frontend_missing"),
        "python_missing": counts.get("python_missing"),
        "python_pending": counts.get("python_pending"),
        "go_extra": counts.get("go_extra"),
        "python_missing_items": [
            f"{item.get('method')} {item.get('path')} ({item.get('status')})"
            for item in payload.get("python_missing", [])
        ],
    }


def run_endpoint_probe() -> dict:
    """18 端点严格对比；--skip-flow 保证不调用 LLM。"""
    sys.path.insert(0, str(GO_ROOT / "scripts"))
    try:
        import api_parity as ap
    except Exception as exc:  # pragma: no cover - 环境问题
        return {"error": f"cannot import api_parity: {exc}"}

    report = Path(tempfile.mkdtemp(prefix="surface-matrix-")) / "api_parity.md"
    args = ap.parse_args(["--skip-flow", "--skip-restart", "--report", str(report)])
    harness = ap.Harness(args)
    buffer = io.StringIO()
    error = None
    try:
        with contextlib.redirect_stdout(buffer):
            harness.run()
    except Exception as exc:
        error = f"{type(exc).__name__}: {exc}"
    finally:
        for side in (harness.py, harness.go):
            if side is not None:
                with contextlib.suppress(Exception):
                    ap.terminate(side)
    rows = [
        {
            "name": r.name,
            "method": r.method,
            "path": r.path,
            "py": r.py_status,
            "go": r.go_status,
            "ok": r.ok,
            "diff": r.diff_count,
        }
        for r in harness.results
    ]
    passed = sum(1 for r in rows if r["ok"])
    result = {
        "total": len(rows),
        "passed": passed,
        "failed": [
            {"name": r["name"], "path": f'{r["method"]} {r["path"]}', "py": r["py"], "go": r["go"], "diff": r["diff"]}
            for r in rows if not r["ok"]
        ],
        "rows": rows,
    }
    if error:
        result["error"] = error
    if harness.py is not None and harness.py.start_seconds:
        result["python_start_s"] = harness.py.start_seconds
    if harness.go is not None and harness.go.start_seconds:
        result["go_start_s"] = harness.go.start_seconds
    return result


def render_block(snap: dict) -> str:
    routes = snap["routes"]
    audit = snap.get("audit", {})
    probe = snap.get("probe", {})
    fe = snap["frontend"]
    tools = snap.get("tools", {})

    def esc(text: object) -> str:
        return (
            str(text).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        )

    probe_cell = "未运行"
    if probe.get("error") and not probe.get("total"):
        probe_cell = f'<span class="missing">失败：{esc(probe["error"])}</span>'
    elif probe.get("total"):
        probe_cell = f'{probe.get("passed", 0)} / {probe.get("total", 0)} PASS'
        if probe.get("error"):
            probe_cell += f'（<span class="missing">{esc(probe["error"])}</span>）'

    failed_rows = "".join(
        f'<div class="small">✗ {esc(item["path"])} — Py {item["py"]} / Go {item["go"]}（差异 {item["diff"]}）</div>'
        for item in probe.get("failed", [])
    ) or '<div class="small">无</div>'

    audit_cell = "未运行"
    if audit.get("error"):
        audit_cell = f'<span class="missing">失败：{esc(audit["error"])}</span>'
    elif audit:
        audit_cell = (
            f'前端→Go 缺失 {audit.get("frontend_missing")}；Python→Go 缺失 {audit.get("python_missing")}'
            f'（待修 {audit.get("python_pending")}，全部豁免）'
        )
        if audit.get("python_missing_items"):
            audit_cell += '<div class="small">' + "；".join(esc(i) for i in audit["python_missing_items"]) + "</div>"

    tools_cell = "见 tool-parity-matrix.html"
    if tools:
        tools_cell = (
            f'模块 {esc(tools.get("modules", "?"))} · action {esc(tools.get("actions", "?"))}'
            f' · 默认会话工具 {esc(tools.get("default_tools", "?"))}（矩阵生成值，未重算）'
        )

    updated = esc(snap["generated_at"])
    return f"""<h3>最近一次自动刷新</h3>
<div class="tablewrap"><table><thead><tr><th style="width:26%">指标</th><th>最近一次自动刷新的实测值</th></tr></thead><tbody>
<tr><td>刷新时间</td><td>{updated}（脚本 refresh_surface_matrix.py）</td></tr>
<tr><td>路由注册 / 完全一致</td><td>Go {routes['go_total']} · Python {routes['python_total']} · 完全一致 {routes['exact']}</td></tr>
<tr><td>skills / config 路由</td><td>skills {routes['skills_routes_exact']}/{routes['skills_routes_total']} · config {routes['config_routes_exact']}/{routes['config_routes_total']}（method+path 完全一致）</td></tr>
<tr><td>surface_audit</td><td>{audit_cell}</td></tr>
<tr><td>端点契约（严格，18 条）</td><td>{probe_cell}</td></tr>
<tr><td>失败端点</td><td>{failed_rows}</td></tr>
<tr><td>前端源码</td><td>同路径 {fe['shared_paths']} · Go 独有 {fe['go_only']} · 内容不同 {fe['content_diff']}（Python 独有 {fe['python_only']}）</td></tr>
<tr><td>工具面</td><td>{tools_cell}</td></tr>
</tbody></table></div>
<div class="small">本区块由 <code>go_hive/scripts/refresh_surface_matrix.py</code> 定时重写；正文各表是人工维护的基线快照，两者不一致时以本区块为准。机器可读：<code>go_hive/docs/surface-matrix-snapshot.json</code>。</div>"""


def update_html(snap: dict) -> None:
    html = HTML_PATH.read_text(encoding="utf-8")
    if "<!-- SNAPSHOT:START -->" not in html or "<!-- SNAPSHOT:END -->" not in html:
        raise SystemExit("surface-tracking-matrix.html is missing SNAPSHOT markers")
    block = render_block(snap)
    html = re.sub(
        r"<!-- SNAPSHOT:START -->.*?<!-- SNAPSHOT:END -->",
        "<!-- SNAPSHOT:START -->\n" + block + "\n<!-- SNAPSHOT:END -->",
        html,
        count=1,
        flags=re.DOTALL,
    )
    html = re.sub(
        r"(<span>生成：<b>)([^<]*)(</b></span>)",
        lambda m: m.group(1) + snap["generated_at"] + m.group(3),
        html,
        count=1,
    )
    html = re.sub(
        r"(<footer>生成：)\d{4}-\d{2}-\d{2} \d{2}:\d{2} [A-Z]+",
        lambda m: m.group(1) + snap["generated_at"],
        html,
        count=1,
    )
    if snap.get("commit"):
        html = re.sub(
            r"(Go HEAD：<b>)[0-9a-f]+(</b>)",
            lambda m: m.group(1) + snap["commit"] + m.group(2),
            html,
            count=1,
        )
        html = re.sub(
            r"(快照：Go HEAD <code>)[0-9a-f]+(</code>)",
            lambda m: m.group(1) + snap["commit"] + m.group(2),
            html,
            count=1,
        )
    versions = snap.get("versions") or {}
    if versions.get("python"):
        html = re.sub(
            r"(Python hive：<b>)[^<]+(</b>)",
            lambda m: m.group(1) + versions["python"] + m.group(2),
            html,
            count=1,
        )
    if versions.get("go"):
        html = re.sub(
            r"(Go hive：<b>)[^<]+(</b>)",
            lambda m: m.group(1) + versions["go"] + m.group(2),
            html,
            count=1,
        )
    tmp = HTML_PATH.with_suffix(".html.tmp")
    tmp.write_text(html, encoding="utf-8")
    os.replace(tmp, HTML_PATH)


def render_interfaces_block(snap: dict) -> str:
    routes = snap["routes"]
    tools = snap.get("tools", {})
    event_types = count_event_types()
    transports = count_mcp_transports()
    protocol = extension_protocol_version()

    def show(value, suffix=""):
        return f"{value}{suffix}" if value else "未取到"

    return f"""<div class="tablewrap"><table><thead><tr><th style="width:34%">校验项</th><th>最近一次自动校验值</th></tr></thead><tbody>
<tr><td>刷新时间 / HEAD</td><td>{snap['generated_at']} · <code>{snap.get('commit') or '?'}</code></td></tr>
<tr><td>路由注册 / 完全一致</td><td>Go {routes['go_total']} · Python {routes['python_total']} · 完全一致 {routes['exact']}</td></tr>
<tr><td>skills / config 路由</td><td>skills {routes['skills_routes_exact']}/{routes['skills_routes_total']} · config {routes['config_routes_exact']}/{routes['config_routes_total']}</td></tr>
<tr><td>事件类型常量</td><td>{show(event_types, ' 个')}（<code>internal/eventbus/event_type.go</code>）</td></tr>
<tr><td>MCP transport 枚举</td><td>{show(transports, ' 种')}（<code>docs/mcp-registry/index.schema.json</code>）</td></tr>
<tr><td>工具面</td><td>模块 {tools.get('modules', '?')} · action {tools.get('actions', '?')} · 默认会话 {tools.get('default_tools', '?')}</td></tr>
<tr><td>Browser Bridge 扩展协议</td><td>{show(protocol, '')}</td></tr>
</tbody></table></div>
<div class="small">本区块由 <code>go_hive/scripts/refresh_surface_matrix.py</code> 定时重写；不变量正文由人维护，编号不复用。机器可读：<code>go_hive/docs/surface-matrix-snapshot.json</code>。</div>"""


def update_interfaces_html(snap: dict) -> None:
    if not INTERFACES_HTML.is_file():
        return
    html = INTERFACES_HTML.read_text(encoding="utf-8")
    if "<!-- INTERFACES:START -->" not in html or "<!-- INTERFACES:END -->" not in html:
        return
    block = render_interfaces_block(snap)
    html = re.sub(
        r"<!-- INTERFACES:START -->.*?<!-- INTERFACES:END -->",
        "<!-- INTERFACES:START -->\n" + block + "\n<!-- INTERFACES:END -->",
        html,
        count=1,
        flags=re.DOTALL,
    )
    html = re.sub(
        r"(<span>生成：<b>)([^<]*)(</b></span>)",
        lambda m: m.group(1) + snap["generated_at"] + m.group(3),
        html,
        count=1,
    )
    html = re.sub(
        r"(<footer>生成：)\d{4}-\d{2}-\d{2} \d{2}:\d{2} [A-Z]+",
        lambda m: m.group(1) + snap["generated_at"],
        html,
        count=1,
    )
    if snap.get("commit"):
        html = re.sub(r"(Go HEAD：<b>)[0-9a-f]+(</b>)", lambda m: m.group(1) + snap["commit"] + m.group(2), html, count=1)
        html = re.sub(
            r"(快照：Go HEAD <code>)[0-9a-f]+(</code>)",
            lambda m: m.group(1) + snap["commit"] + m.group(2),
            html,
            count=1,
        )
    versions = snap.get("versions") or {}
    if versions.get("python"):
        html = re.sub(
            r"(Python hive：<b>)[^<]+(</b>)",
            lambda m: m.group(1) + versions["python"] + m.group(2),
            html,
            count=1,
        )
    if versions.get("go"):
        html = re.sub(
            r"(Go hive：<b>)[^<]+(</b>)",
            lambda m: m.group(1) + versions["go"] + m.group(2),
            html,
            count=1,
        )
    tmp = INTERFACES_HTML.with_suffix(".html.tmp")
    tmp.write_text(html, encoding="utf-8")
    os.replace(tmp, INTERFACES_HTML)


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--skip-probe", action="store_true", help="跳过 18 端点探针（只刷新静态扫描）")
    args = parser.parse_args(argv)

    snap = {
        "generated_at": now_stamp(),
        "commit": git_head(),
        "versions": {"python": python_version(), "go": go_version()},
        "repo": str(ROOT),
        "routes": scan_routes(),
        "frontend": scan_frontend(),
        "tools": parse_tool_matrix(),
        "upstream_tools": count_upstream_tools(),
        "audit": run_surface_audit(),
    }
    if args.skip_probe:
        snap["probe"] = {"skipped": True}
    else:
        snap["probe"] = run_endpoint_probe()

    SNAP_PATH.write_text(json.dumps(snap, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    update_html(snap)
    update_interfaces_html(snap)
    update_tool_matrix(snap)

    probe = snap["probe"]
    probe_line = (
        f"端点 {probe.get('passed', 0)}/{probe.get('total', 0)} PASS"
        if probe.get("total")
        else "端点未运行" + (f"（{probe.get('error')}）" if probe.get("error") else "")
    )
    routes = snap["routes"]
    print(
        f"[surface-matrix] {snap['generated_at']} · 路由 {routes['exact']}/{routes['go_total']} 一致 · "
        f"{probe_line} · 前端缺失 {snap['audit'].get('frontend_missing', '?')} · "
        f"已更新 {HTML_PATH.name} 与 {SNAP_PATH.name}"
        + (f" 与 {INTERFACES_HTML.name}" if INTERFACES_HTML.is_file() else "")
        + (f" 与 {TOOL_MATRIX.name}" if TOOL_MATRIX.is_file() else "")
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
