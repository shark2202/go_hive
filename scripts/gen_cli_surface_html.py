#!/usr/bin/env python3
"""Render go_hive/docs/cli-surface.html — CLI entry points, command tree, parameters.

Sources (nothing is hard-coded; every count comes from these):
  * go_hive/contracts/cli-surface.json — frozen recursive `--help` capture of
    `hive` / `hive-browser` on both sides, plus the diff, probes, defaults and
    alignment sections.
  * core/pyproject.toml, tools/pyproject.toml — Python `[project.scripts]`.
  * go_hive/go-hive/cmd/*/main.go, go_hive/browserbridge/cmd/*/main.go —
    Go entry points.
  * A live `hive-cred --help` capture on both sides (Python script through the
    repo interpreter; Go through a freshly built `cmd/hive-cred` binary).

Usage:
  python3 go_hive/scripts/gen_cli_surface_html.py            # write the page
  python3 go_hive/scripts/gen_cli_surface_html.py --check    # fail if stale

Environment overrides: CLI_SURFACE_HTML_SCRATCH (binaries + isolated HIVE_HOME),
CLI_SURFACE_PYTHON, CLI_SURFACE_GO_BIN_DIR.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tomllib
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
DEFAULT_CONTRACT = REPO / "go_hive" / "contracts" / "cli-surface.json"
DEFAULT_OUTPUT = REPO / "go_hive" / "docs" / "cli-surface.html"
GO_MODULE = REPO / "go_hive" / "go-hive"
BRIDGE_MODULE = REPO / "go_hive" / "browserbridge"

sys.path.insert(0, str(HERE))
from cli_surface_contract import collapse, parse_help  # noqa: E402

TITLE = "CLI 对外面：入口 / 命令 / 参数 · Hive × go_hive"
MAX_CRED_DEPTH = 2
RUN_TIMEOUT = 60

CSS = """
:root{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}
*{box-sizing:border-box} html{scroll-behavior:auto} body{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif}
a{color:var(--accent2);text-decoration:none} a:hover{text-decoration:underline}
.wrap{max-width:1400px;margin:0 auto;padding:44px 26px 80px}
header{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}
.eyebrow{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}
h1{font-size:34px;line-height:1.2;margin:10px 0 10px;letter-spacing:-.02em} h1 span{color:var(--accent);white-space:nowrap}
.lede{color:var(--muted);max-width:1080px;font-size:15px} .meta{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px} .meta b{color:var(--text)}
section{margin:38px 0} h2{font-size:22px;margin:0 0 8px} h2 .num{color:var(--accent);font-size:14px;margin-right:8px} h3{font-size:16px;margin:26px 0 8px;color:#cfdae6} h4{font-size:14px;margin:18px 0 6px;color:#cfdae6}
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
@media(max-width:900px){table{min-width:920px}}
.cmd{background:var(--panel2);border:1px solid var(--line);border-radius:11px;margin:8px 0;overflow:hidden}
.cmd>summary{cursor:pointer;padding:9px 12px;display:flex;gap:9px;flex-wrap:wrap;align-items:center;border-bottom:1px solid transparent}
.cmd[open]>summary{border-bottom-color:var(--line);background:#0d141d}
.cmd>summary code{font-size:12.5px;background:transparent;border:0;padding:0;color:#e6edf3}
.cmd .body{padding:6px 12px 12px}
.sidechip{font-size:10.5px;letter-spacing:.06em;text-transform:uppercase;color:var(--muted);font-weight:700;min-width:58px}
.usagerow{display:grid;grid-template-columns:62px minmax(0,1fr) auto;gap:8px;align-items:start;margin:5px 0}
.usagerow pre{margin:0;white-space:pre-wrap;word-break:break-word}
.cmd .desc{margin:6px 0;color:#c6d2df}
.cmd .desc .sidechip{min-width:52px;display:inline-block}
.chips{display:flex;flex-wrap:wrap;gap:6px;margin:8px 0}
.chips a{font-size:11.5px;border:1px solid var(--line);background:var(--panel);padding:3px 8px;border-radius:999px;color:var(--muted)}
.cols2{display:grid;grid-template-columns:1fr 1fr;gap:12px} @media(max-width:900px){.cols2{grid-template-columns:1fr}}
details.raw{margin:8px 0} details.raw summary{cursor:pointer;color:var(--muted);font-size:12.5px}
.footer-note{color:var(--muted);font-size:12px}
.anchorback{color:var(--muted);font-size:11px;margin-left:6px}
"""


# ---------------------------------------------------------------------------
# small helpers
# ---------------------------------------------------------------------------


def esc(text: object) -> str:
    return (
        str(text)
        .replace("&", "&amp;")
        .replace("<", "&lt;")
        .replace(">", "&gt;")
        .replace('"', "&quot;")
    )


def md_inline(text: str) -> str:
    out = esc(text)
    out = re.sub(r"`([^`]+)`", r"<code>\1</code>", out)
    out = re.sub(r"\*\*([^*]+)\*\*", r"<b>\1</b>", out)
    return out


def clip(text: str, width: int = 110) -> str:
    text = collapse(text or "")
    return text if len(text) <= width else text[: width - 1] + "…"


def sha12(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()[:12]


def pill(text: str, kind: str) -> str:
    return f'<span class="pill {kind}">{esc(text)}</span>'


def rel(path: Path) -> str:
    try:
        return path.relative_to(REPO).as_posix()
    except ValueError:
        return str(path)


def child_env(repo: Path, home: Path) -> dict:
    env = dict(os.environ)
    env.update(
        {
            "HIVE_HOME": str(home),
            "COLUMNS": "100",
            "NO_COLOR": "1",
            "TERM": "dumb",
            "PYTHONIOENCODING": "utf-8",
            "PYTHONDONTWRITEBYTECODE": "1",
            "PYTHONPATH": os.pathsep.join([str(repo / "core"), str(repo / "tools" / "src")]),
        }
    )
    return env


def run_capture(argv: list[str], env: dict, cwd: Path) -> tuple[int, str, str]:
    proc = subprocess.run(
        argv, env=env, cwd=str(cwd), capture_output=True, text=True, timeout=RUN_TIMEOUT
    )
    return proc.returncode, proc.stdout or "", proc.stderr or ""


# ---------------------------------------------------------------------------
# entry-point discovery
# ---------------------------------------------------------------------------

ENTRY_NOTES = {
    ("python", "hive"): "根入口：serve / open / queen / colony / session / chat / skill / debugger / mcp / janitor；用户与 agent 的默认入口。",
    ("python", "hive-cred"): "凭据 CLI：token / browse / inspect，脚本按需取新鲜凭据值；见 §04 两侧实时 --help 对照。",
    ("python", "hive-browser"): "浏览器 CLI（替代 browser_* MCP 工具）；agent 经 terminal_exec 以 --json 调用。",
    ("go", "hive"): "上游同名入口的 Go 实现；逐命令对拍见 §02。",
    ("go", "hive-cred"): "上游 hive-cred 的 Go 实现；逐命令对拍见 §04。",
    ("go", "hive-browser"): "上游 hive-browser 的 Go 实现；命令经 HTTP 打到 browserbridge。",
    ("go", "browserbridge"): "浏览器桥守护进程：扩展 WS 14829 / agent HTTP 14830 / control 14831 / legacy 9229+9230；由 hive serve / hive-browser 自动拉起，也可手动运行。不属用户 CLI，不进 help 契约。",
    ("go", "hive-longmemeval"): "内部评测工具，不属对外 CLI。",
    ("go", "chromeloader"): "内部扩展加载器（browserbridge 模块下），不属对外 CLI。",
}
INTERNAL_GO = {"hive-longmemeval", "chromeloader"}


def resolve_module_source(repo: Path, module: str) -> str:
    parts = module.split(".")
    for base in ("core", "tools/src"):
        candidate = repo / base / Path(*parts).with_suffix(".py")
        if candidate.exists():
            return rel(candidate)
        package = repo / base / Path(*parts) / "__init__.py"
        if package.exists():
            return rel(package)
    return ""


def discover_entries(contract: dict, repo: Path) -> list[dict]:
    entries: list[dict] = []
    for pyproject in (repo / "core" / "pyproject.toml", repo / "tools" / "pyproject.toml"):
        if not pyproject.exists():
            continue
        data = tomllib.loads(pyproject.read_text(encoding="utf-8"))
        project = data.get("project", {})
        for name, target in sorted((project.get("scripts") or {}).items()):
            module = target.split(":")[0]
            entries.append(
                {
                    "name": name,
                    "side": "python",
                    "impl": resolve_module_source(repo, module),
                    "install": f'<code>[project.scripts] {name} = "{target}"</code>'
                    f'<div class="small">{esc(project.get("name",""))} · '
                    f"<code>{esc(rel(pyproject))}</code></div>",
                    "version": f"{project.get('name','')} {project.get('version','')}".strip(),
                    "internal": False,
                }
            )
    for module_dir in (GO_MODULE, BRIDGE_MODULE):
        cmd_dir = module_dir / "cmd"
        if not cmd_dir.exists():
            continue
        for main_go in sorted(cmd_dir.glob("*/main.go")):
            name = main_go.parent.name
            entries.append(
                {
                    "name": name,
                    "side": "go",
                    "impl": rel(main_go),
                    "install": f"<code>go build -o dist/{name} ./cmd/{name}</code>"
                    f'<div class="small">模块 <code>{esc(rel(module_dir))}</code></div>',
                    "version": "",
                    "internal": name in INTERNAL_GO,
                }
            )
    # Versions the contract already observed without extra probes.
    for cli_name in ("hive", "hive-browser"):
        block = contract.get("clis", {}).get(cli_name)
        if not block:
            continue
        for probe in block.get("go", {}).get("battery", []):
            if probe.get("probe") == "version":
                version = str(probe.get("stdout_head", "")).strip()
                for entry in entries:
                    if entry["side"] == "go" and entry["name"] == cli_name and version:
                        entry["version"] = version
    return entries


def entry_pair(entries: list[dict], name: str) -> dict[str, dict]:
    return {e["side"]: e for e in entries if e["name"] == name}


def render_entries_section(contract: dict, entries: list[dict], cred: dict) -> str:
    block = contract.get("clis", {})
    contract_facts = {}
    for cli_name, label in (("hive", "hive"), ("hive-browser", "hive-browser")):
        cli = block.get(cli_name)
        if not cli:
            continue
        totals = cli["diff"]["totals"]
        go_only = totals.get("go_only", [])
        contract_facts[cli_name] = (
            f'命令路径 {totals.get("paths_python",0)}（Python）/ {totals.get("paths_go",0)}（Go），'
            f'共有 {totals.get("paths_shared",0)}，仅 Python {len(totals.get("python_only",[]))}，'
            f"仅 Go {len(go_only)}"
            + (f'（<code>{esc(" ".join(go_only))}</code>）' if go_only else "")
        )
    rows = []
    for entry in entries:
        name = entry["name"]
        pair = entry_pair(entries, name)
        if entry["side"] == "python":
            note = ENTRY_NOTES.get(("python", name), "")
            if name in ("hive", "hive-cred", "hive-browser") and "go" in pair:
                other = pair["go"]
                correspondence = f"↔ Go <code>{esc(other['impl'])}</code>"
            else:
                correspondence = "—"
            if name in contract_facts:
                delta = f'<div class="small">{contract_facts[name]}</div>'
            elif name == "hive-cred":
                delta = '<div class="small">见 §04（两侧实时 <code>--help</code> 对照）</div>'
            else:
                delta = "—"
        else:
            note = ENTRY_NOTES.get(("go", name), "")
            if name in ("hive", "hive-cred", "hive-browser") and "python" in pair:
                other = pair["python"]
                correspondence = f"↔ Python <code>{esc(other['impl'])}</code>"
            elif name == "browserbridge":
                correspondence = "Python 侧无同名入口（上游 bridge 由浏览器工具进程内提供）"
            else:
                correspondence = "—"
            if name in contract_facts:
                delta = f'<div class="small">{contract_facts[name]}</div>'
            else:
                delta = "—"
        status = ""
        if entry["internal"]:
            status = pill("内部工具（不属对外面）", "warn")
        elif name == "browserbridge":
            status = pill("守护进程（非用户 CLI）", "info")
        else:
            status = pill("对外入口", "ok")
        version = f"<code>{esc(entry['version'])}</code>" if entry["version"] else "—"
        rows.append(
            "<tr>"
            f"<td><code>{esc(name)}</code><div class=\"small\">{esc(entry['side'])}</div></td>"
            f"<td>{esc(entry['impl'])}</td>"
            f"<td>{entry['install']}</td>"
            f"<td>{version}</td>"
            f"<td>{correspondence}</td>"
            f"<td>{status}</td>"
            f"<td>{note}{delta}</td>"
            "</tr>"
        )
    return (
        '<div class="tablewrap"><table><colgroup>'
        '<col style="width:9%"><col style="width:15%"><col style="width:20%"><col style="width:11%">'
        '<col style="width:13%"><col style="width:9%"><col style="width:23%">'
        "</colgroup><thead><tr>"
        "<th>入口</th><th>实现</th><th>安装 / 构建</th><th>版本</th><th>对应关系</th><th>定位</th><th>备注</th>"
        "</tr></thead><tbody>" + "".join(rows) + "</tbody></table></div>"
    )


# ---------------------------------------------------------------------------
# flag / positional tables (shared by the contract tree and the live hive-cred tree)
# ---------------------------------------------------------------------------


def _flag_equal(a: dict, b: dict) -> bool:
    for field in ("names", "takes_value", "placeholder", "choices", "default_in_help", "help"):
        if a.get(field) != b.get(field):
            return False
    return True


def flag_rows(py_node: dict | None, go_node: dict | None) -> list[dict]:
    """Union of the two sides' flags; kind: shared | differ | python_only | go_only."""
    pf = (py_node or {}).get("flags", {})
    gf = (go_node or {}).get("flags", {})
    rows = []
    for key in list(pf) + [k for k in gf if k not in pf]:
        a, b = pf.get(key), gf.get(key)
        if a and b:
            kind = "shared" if _flag_equal(a, b) else "differ"
        elif a:
            kind = "python_only"
        else:
            kind = "go_only"
        rows.append({"key": key, "python": a, "go": b, "kind": kind})
    return rows


def render_flag_rows(rows: list[dict], stats: dict) -> str:
    html_rows = []
    side_cells = {
        "shared": pill("两侧共有", "ok"),
        "python_only": pill("仅 Python", "warn"),
    }
    for row in rows:
        entries = []
        if row["kind"] == "shared":
            entries.append(("共享", row["python"], side_cells["shared"]))
        elif row["kind"] == "differ":
            entries.append(("Python", row["python"], pill("Python（与 Go 不同）", "warn")))
            entries.append(("Go", row["go"], pill("Go（与 Python 不同）", "warn")))
        elif row["kind"] == "python_only":
            entries.append(("Python", row["python"], side_cells["python_only"]))
        else:
            go_flag = row["go"] or {}
            # `--help` is not a Go extension; only the flags a Go-only command adds are.
            label = "仅 Go" if go_flag.get("key") == "--help" else "仅 Go · 内部扩展（已/将隐藏）"
            entries.append(("Go", row["go"], pill(label, "info")))
        for _label, flag, side_html in entries:
            names = ", ".join(flag.get("names") or [flag.get("key", "")])
            takes = "是" if flag.get("takes_value") else "否"
            placeholder = flag.get("placeholder") or "—"
            choices = flag.get("choices")
            choices_html = " | ".join(esc(c) for c in choices) if choices else "—"
            default = flag.get("default_in_help") or "—"
            help_text = flag.get("help") or "—"
            html_rows.append(
                "<tr>"
                f"<td><code>{esc(names)}</code></td>"
                f"<td>{takes}</td>"
                f"<td><code>{esc(placeholder)}</code></td>"
                f"<td>{choices_html}</td>"
                f"<td><code>{esc(default)}</code></td>"
                f"<td>{esc(help_text)}</td>"
                f"<td>{side_html}</td>"
                "</tr>"
            )
    stats["flag_rows"] += len(html_rows)
    if not html_rows:
        return '<p class="sub">无参数（仅位置参数或无参命令）。</p>'
    return (
        '<div class="tablewrap"><table><colgroup>'
        '<col style="width:20%"><col style="width:5%"><col style="width:13%"><col style="width:13%">'
        '<col style="width:12%"><col style="width:24%"><col style="width:13%">'
        "</colgroup><thead><tr>"
        "<th>参数</th><th>取值</th><th>placeholder</th><th>choices</th><th>默认（help 声明）</th><th>说明</th><th>侧</th>"
        "</tr></thead><tbody>" + "".join(html_rows) + "</tbody></table></div>"
    )


def positional_rows(py_node: dict | None, go_node: dict | None) -> list[dict]:
    pp = list((py_node or {}).get("positionals", []))
    gp = list((go_node or {}).get("positionals", []))
    pr = set((py_node or {}).get("required_positionals", []))
    gr = set((go_node or {}).get("required_positionals", []))
    rows = []
    for name in pp + [n for n in gp if n not in pp]:
        a = name in pp
        b = name in gp
        if a and b:
            kind = "shared" if (name in pr) == (name in gr) else "differ"
        else:
            kind = "python_only" if a else "go_only"
        rows.append(
            {
                "name": name,
                "python": name in pr if a else None,
                "go": name in gr if b else None,
                "kind": kind,
            }
        )
    return rows


def render_positional_rows(rows: list[dict], stats: dict) -> str:
    if not rows:
        return ""
    html_rows = []
    for row in rows:
        required_cells = []
        if row["kind"] == "shared":
            required_cells.append(("", "必填" if row["python"] else "可选", pill("两侧共有", "ok")))
        elif row["kind"] == "differ":
            required_cells.append(("Python", "必填" if row["python"] else "可选", pill("Python（与 Go 不同）", "warn")))
            required_cells.append(("Go", "必填" if row["go"] else "可选", pill("Go（与 Python 不同）", "warn")))
        elif row["kind"] == "python_only":
            required_cells.append(("", "必填" if row["python"] else "可选", pill("仅 Python", "warn")))
        else:
            required_cells.append(("", "必填" if row["go"] else "可选", pill("仅 Go", "info")))
        for label, required, side_html in required_cells:
            name_cell = f"<code>{esc(row['name'])}</code>"
            if label:
                name_cell += f'<div class="small">{esc(label)}</div>'
            html_rows.append(f"<tr><td>{name_cell}</td><td>{required}</td><td>{side_html}</td></tr>")
    stats["positional_rows"] += len(html_rows)
    return (
        '<div class="tablewrap"><table><colgroup><col style="width:34%"><col style="width:18%"><col style="width:48%">'
        "</colgroup><thead><tr><th>位置参数</th><th>必填</th><th>侧</th></tr></thead><tbody>"
        + "".join(html_rows)
        + "</tbody></table></div>"
    )


# ---------------------------------------------------------------------------
# contract command tree
# ---------------------------------------------------------------------------


def command_order(py_cmds: dict, go_cmds: dict) -> list[str]:
    order: list[str] = []
    seen: set[str] = set()
    visited: set[tuple[int, str]] = set()

    def walk(cmds: dict, key: str) -> None:
        if key not in cmds or (id(cmds), key) in visited:
            return
        visited.add((id(cmds), key))
        if key not in seen:
            seen.add(key)
            order.append(key)
        for sub in cmds[key].get("subcommands", []):
            walk(cmds, f"{key} {sub}".strip())

    walk(py_cmds, "")
    walk(go_cmds, "")
    for key in sorted(set(py_cmds) | set(go_cmds)):
        if key not in seen:
            seen.add(key)
            order.append(key)
    return order


def usage_block(py_node: dict | None, go_node: dict | None) -> str:
    rows = []
    py_usage = (py_node or {}).get("usage")
    go_usage = (go_node or {}).get("usage")
    if py_usage and go_usage and collapse(py_usage) == collapse(go_usage):
        rows.append(
            '<div class="usagerow"><span class="sidechip">usage</span>'
            f'<pre>{esc(py_usage)}</pre>{pill("两侧逐字一致", "ok")}</div>'
        )
        return "".join(rows)
    for label, node in (("python", py_node), ("go", go_node)):
        if not node or not node.get("usage"):
            continue
        rows.append(
            f'<div class="usagerow"><span class="sidechip">{label}</span>'
            f'<pre>{esc(node["usage"])}</pre>{pill("文本不同", "warn") if py_usage and go_usage else ""}</div>'
        )
    return "".join(rows)


def description_block(py_node: dict | None, go_node: dict | None) -> str:
    py_desc = (py_node or {}).get("text_block", "")
    go_desc = (go_node or {}).get("text_block", "")
    if not py_desc and not go_desc:
        return ""
    if py_desc and go_desc and py_desc == go_desc:
        return f'<div class="desc">{esc(py_desc)}</div>'
    parts = []
    if py_desc:
        parts.append(f'<div class="desc"><span class="sidechip">python</span> {esc(py_desc)}</div>')
    if go_desc:
        parts.append(f'<div class="desc"><span class="sidechip">go</span> {esc(go_desc)}</div>')
    return "".join(parts)


def render_command(
    cli_name: str,
    key: str,
    py_node: dict | None,
    go_node: dict | None,
    ctx: dict,
    stats: dict,
) -> str:
    path = f"{cli_name} {key}".strip() if key else f"{cli_name}（根）"
    anchor = f"{cli_name}-" + (key.replace(" ", "-") if key else "root")
    badges = []
    if key in ctx["go_only"]:
        badges.append(pill("Go 内部扩展（已/将隐藏）", "info"))
    if key in ctx["python_only"]:
        badges.append(pill("仅 Python", "warn"))
    if key in ctx["pos_mismatch"]:
        badges.append(pill("位置参数差异", "warn"))
    if key in ctx["text_diff"]:
        badges.append(pill("描述文案差异", "warn"))
    flag_diff = ctx["flag_diff"].get(key, {})
    frows = flag_rows(py_node, go_node)
    if flag_diff.get("python_only"):
        badges.append(pill(f'仅 Python flag ×{len(flag_diff["python_only"])}', "warn"))
    if flag_diff.get("go_only"):
        badges.append(pill(f'仅 Go flag ×{len(flag_diff["go_only"])}', "info"))
    if flag_diff.get("help_mismatch"):
        badges.append(pill(f'flag 说明差异 ×{len(flag_diff["help_mismatch"])}', "warn"))
    if any(
        row["kind"] == "differ"
        and (row["python"] or {}).get("default_in_help") != (row["go"] or {}).get("default_in_help")
        for row in frows
    ):
        badges.append(pill("默认值声明差异", "warn"))
    folded = (go_node or {}).get("recursion_stopped")
    if folded:
        badges.append(pill("help 折叠为父命令（无独立 help 面）", "info"))
    stats["commands"] += 1
    if key and len(key.split()) == 1:
        stats["top_commands"] += 1

    body = []
    body.append(description_block(py_node, go_node))
    usage = usage_block(py_node, go_node)
    if usage:
        body.append(usage)
    flags_html = render_flag_rows(frows, stats)
    body.append(flags_html)
    pos_rows = positional_rows(py_node, go_node)
    if pos_rows:
        body.append(render_positional_rows(pos_rows, stats))
    mismatch = ctx["pos_mismatch"].get(key)
    if mismatch:
        body.append(
            '<div class="small">契约 positional_mismatch：Python '
            f'{esc(", ".join(mismatch.get("python") or []) or "—")}（必填 '
            f'{esc(", ".join(mismatch.get("python_required") or []) or "—")}） · Go '
            f'{esc(", ".join(mismatch.get("go") or []) or "—")}（必填 '
            f'{esc(", ".join(mismatch.get("go_required") or []) or "—")}）</div>'
        )
    if not py_node:
        body.insert(0, '<div class="small">该命令路径仅存在于 Go 侧（Python 侧无对应 help 面）。</div>')
    if not go_node:
        body.insert(0, '<div class="small">该命令路径仅存在于 Python 侧。</div>')
    badge_html = "".join(badges)
    return (
        f'<details class="cmd" id="{esc(anchor)}"><summary><code>{esc(path)}</code>{badge_html}</summary>'
        f'<div class="body">{"".join(body)}</div></details>'
    )


def render_cli_section(number: str, cli_name: str, block: dict, stats: dict) -> str:
    py = block.get("python") or {}
    py_cmds = py.get("commands", {})
    go_cmds = block.get("go", {}).get("commands", {})
    totals = block["diff"]["totals"]
    flag_diff = block["diff"]["flags"]
    ctx = {
        "go_only": set(totals.get("go_only", [])),
        "python_only": set(totals.get("python_only", [])),
        "pos_mismatch": {entry["command"]: entry for entry in totals.get("positional_mismatch", [])},
        "text_diff": {entry["command"]: entry for entry in block.get("text_diff", [])},
        "flag_diff": flag_diff,
    }
    order = command_order(py_cmds, go_cmds)
    help_mismatches = sum(len(v.get("help_mismatch", [])) for v in flag_diff.values())
    cards = [
        ("Python 命令路径", str(totals.get("paths_python", 0)), "合同 reference 侧递归 --help 覆盖"),
        ("Go 命令路径", str(totals.get("paths_go", 0)), "合同 target 侧递归 --help 覆盖"),
        ("共有 / 仅 Python / 仅 Go", f"{totals.get('paths_shared',0)} / {len(totals.get('python_only',[]))} / {len(totals.get('go_only',[]))}", "仅 Go 路径即 Go 内部扩展（已/将隐藏）"),
        ("逐命令探针漂移", str(len(block["diff"].get("probes", []))), "两侧都可探测且不同的条目才会出现在这里"),
        ("行为 battery 漂移", str(len(block["diff"].get("battery", []))), "无副作用退出码/输出头对照"),
        ("help 文案差异", f"{help_mismatches} + {len(block.get('text_diff', []))}", "flag 级 + 命令级（描述）"),
    ]
    card_html = "".join(
        f'<div class="card"><div class="k">{esc(k)}</div><div class="v">{esc(v)}</div><div class="d">{esc(d)}</div></div>'
        for k, v, d in cards
    )
    top = [k for k in order if k and " " not in k and k in set(py_cmds) | set(go_cmds)]
    chips = "".join(
        f'<a href="#{esc(cli_name + "-" + k)}"><code>{esc(k)}</code></a>' for k in top
    )
    body = "".join(
        render_command(cli_name, key, py_cmds.get(key), go_cmds.get(key), ctx, stats) for key in order
    )
    return (
        f'<section id="s{number}"><h2><span class="num">{number}</span>{esc(cli_name)} 命令树</h2>'
        f'<p class="sub">按命令路径递归展开（<code>--help</code> 契约，command paths 为两侧并集）：'
        f'usage、参数表（名称 / 取值 / placeholder / choices / 默认 / 说明）与位置参数；'
        f'仅单侧存在、位置参数或说明不同、Go 内部扩展均按徽标标注。差异计数全部读自契约 JSON，未在页面上手写。</p>'
        f'<div class="grid g4">{card_html}</div>'
        f'<div class="chips">{chips}</div>'
        f"{body}</section>"
    )


# ---------------------------------------------------------------------------
# hive-cred live capture
# ---------------------------------------------------------------------------


def capture_help_tree(base_argv: list[str], env: dict, cwd: Path, cli_name: str) -> tuple[dict | None, str | None]:
    node: dict[str, dict] = {}
    code, out, err = run_capture(base_argv + ["--help"], env, cwd)
    text = out if out.strip() else err
    if code != 0 or not text.strip():
        return None, f"`{' '.join(base_argv)} --help` exit {code}: {clip(err, 160)}"
    root = parse_help(text, cli_name)
    root["help_exit_code"] = code
    root["raw"] = text
    node[""] = root
    frontier = [[sub] for sub in root["subcommands"]]
    depth = 1
    while frontier and depth <= MAX_CRED_DEPTH:
        nxt: list[list[str]] = []
        for path in frontier:
            key = " ".join(path)
            if key in node:
                continue
            code, out, err = run_capture(base_argv + path + ["--help"], env, cwd)
            text = out if out.strip() else err
            parsed = parse_help(text, " ".join([cli_name] + path))
            parsed["help_exit_code"] = code
            parsed["raw"] = text
            node[key] = parsed
            if parsed.get("subcommands") and collapse(text) != collapse(node.get("", {}).get("raw", "")):
                nxt.extend(path + [sub] for sub in parsed["subcommands"])
        frontier = nxt
        depth += 1
    return node, None


def find_python_bin(repo: Path, explicit: str | None) -> str | None:
    candidates = []
    if explicit:
        candidates.append(Path(explicit))
    candidates += [
        repo / ".venv" / "bin" / "python",
        Path.home() / "hive" / ".venv" / "bin" / "python",
        Path(sys.executable),
    ]
    for candidate in candidates:
        if candidate.exists():
            return str(candidate)
    return None


def find_hive_cred_python(repo: Path, explicit: str | None, env: dict, cwd: Path) -> tuple[list[str] | None, str | None]:
    """Prefer the repo script (worktree code) through the venv interpreter."""
    python_bin = find_python_bin(repo, os.environ.get("CLI_SURFACE_PYTHON"))
    if explicit:
        return [explicit], None
    if python_bin:
        script = repo / "core" / "framework" / "credentials" / "cli.py"
        if script.exists():
            probe_argv = [python_bin, str(script)]
            code, out, err = run_capture(probe_argv + ["--help"], env, cwd)
            if code == 0 and out.strip():
                return probe_argv, None
    wrapper = shutil.which("hive-cred")
    if wrapper:
        return [wrapper], None
    return None, "未找到可用的 Python hive-cred（--python-bin / <repo>/.venv / PATH）"


def build_go_cred(scratch: Path, bin_dir: str | None) -> tuple[list[str] | None, str | None]:
    if bin_dir:
        candidate = Path(bin_dir) / "hive-cred"
        if candidate.exists():
            return [str(candidate)], None
        return None, f"{bin_dir}/hive-cred 不存在"
    scratch.mkdir(parents=True, exist_ok=True)
    out = scratch / "hive-cred"
    env = dict(os.environ)
    env.update({"CGO_ENABLED": "0", "GOPROXY": "off", "GOSUMDB": "off", "GOFLAGS": "-mod=mod"})
    try:
        proc = subprocess.run(
            ["go", "build", "-o", str(out), "./cmd/hive-cred"],
            cwd=str(GO_MODULE),
            env=env,
            capture_output=True,
            text=True,
            timeout=300,
        )
    except FileNotFoundError:
        return None, "go 不可用（--go-bin-dir 或安装 Go 工具链）"
    if proc.returncode != 0:
        return None, f"go build ./cmd/hive-cred 失败：{clip(proc.stderr, 200)}"
    return [str(out)], None


def render_cred_section(number: str, cred: dict, stats: dict) -> str:
    if cred.get("error"):
        return (
            f'<section id="s{number}"><h2><span class="num">{number}</span>hive-cred（实时 --help 对照）</h2>'
            f'<div class="callout bad"><b>捕获失败：</b>{esc(cred["error"])}</div></section>'
        )
    py_tree, go_tree = cred["python"], cred["go"]
    order: list[str] = []
    for tree in (py_tree, go_tree):
        for key in tree:
            if key not in order:
                order.append(key)
    # Stable depth ordering: same-depth keys keep the contract's own order.
    order = sorted(order, key=lambda k: k.count(" "))
    blocks = []
    identical_usage = 0
    identical_help = 0
    for key in order:
        p, g = py_tree.get(key), go_tree.get(key)
        title = "hive-cred" + (f" {key}" if key else "")
        badges = []
        usage_same = bool(p and g and collapse(p.get("usage", "")) == collapse(g.get("usage", "")))
        if usage_same:
            identical_usage += 1
            badges.append(pill("usage 行逐字一致", "ok"))
        else:
            badges.append(pill("usage 行不同", "warn"))
        raw_same = bool(p and g and p.get("raw") == g.get("raw"))
        if raw_same:
            identical_help += 1
            badges.append(pill("help 全文一致", "ok"))
        else:
            badges.append(pill("help 全文有差异", "warn"))
        if p is None:
            badges.append(pill("仅 Go 侧有该路径", "info"))
        if g is None:
            badges.append(pill("仅 Python 侧有该路径", "warn"))
        usage = usage_block(p, g)
        flags_html = render_flag_rows(flag_rows(p, g), stats)
        pos_rows = positional_rows(p, g)
        pos_html = render_positional_rows(pos_rows, stats) if pos_rows else ""
        raws = []
        for label, node in (("Python", p), ("Go", g)):
            if node is not None:
                raws.append(f'<div><div class="sidechip">{label}</div><pre>{esc(node.get("raw",""))}</pre></div>')
        raw_html = (
            f'<details class="raw"><summary>原始 --help（{"两侧文本相同，仅列一次" if raw_same else "Python / Go 两侧"}）</summary>'
            f'<div class="cols2">{raws[0] if raw_same else "".join(raws)}</div></details>'
        )
        blocks.append(
            f'<details class="cmd" id="cred-{esc(key.replace(" ", "-") or "root")}"><summary>'
            f"<code>{esc(title)}</code>{''.join(badges)}</summary><div class=\"body\">"
            f"{description_block(p, g)}{usage}{flags_html}{pos_html}{raw_html}</div></details>"
        )
    stats["cred_paths"] = len(order)
    stats["cred_usage_identical"] = identical_usage
    stats["cred_help_identical"] = identical_help
    header = (
        f'<p class="sub">实时捕获（不来自契约 JSON）：Python 侧 <code>{esc(cli_display(cred["python_cmd"]))}</code> · '
        f'Go 侧 <code>{esc(cli_display(cred["go_cmd"]))}</code>（<code>go build ./cmd/hive-cred</code>）。'
        f"共 {len(order)} 条路径：usage 行逐字一致 {identical_usage} 条，help 全文一致 {identical_help} 条；"
        "不一致处按原文并列，不做归因。</p>"
    )
    return (
        f'<section id="s{number}"><h2><span class="num">{number}</span>hive-cred（实时 --help 对照）</h2>'
        f"{header}{''.join(blocks)}</section>"
    )


def cli_display(argv: list[str]) -> str:
    if not argv:
        return "—"
    home = str(Path.home())
    parts = []
    for item in argv:
        if item.startswith(home + os.sep):
            parts.append("~" + item[len(home):])
        elif Path(item).is_absolute() and Path(item).exists():
            parts.append(rel(Path(item)))
        else:
            parts.append(item)
    return " ".join(parts)


# ---------------------------------------------------------------------------
# alignment / summary section
# ---------------------------------------------------------------------------


def _probe_verdict(py_exit, go_exit) -> str:
    if py_exit is None or go_exit is None:
        return pill("单侧可探测", "info")
    if py_exit == go_exit:
        return pill(f"一致（{py_exit}）", "ok")
    return pill(f"漂移（{py_exit} vs {go_exit}）", "bad")


def render_status_section(number: str, contract: dict) -> str:
    summary = contract.get("summary", {})
    clis = contract.get("clis", {})
    cards = [
        ("probe 漂移总数", summary.get("probe_drift_total", 0), "逐命令探针（两侧取值不同的条目）"),
        ("其中两侧都可探测", summary.get("probe_drift_both_sides", 0), "真正的行为漂移候选"),
        ("battery 漂移", summary.get("battery_drift_total", 0), "无副作用探针（退出码 + 输出头）"),
        ("命令级文案差异", summary.get("text_diff_total", 0), "描述文本不同"),
        ("默认值字面量比对", f"{summary.get('defaults_literal_compared',0)} / 不一致 {summary.get('defaults_literal_mismatch',0)}", "Go argSpec vs Python add_argument"),
        ("仅符号默认值", summary.get("defaults_symbolic_only", 0), "无法逐字比对（--timeout-ms）"),
    ]
    card_html = "".join(
        f'<div class="card"><div class="k">{esc(k)}</div><div class="v">{esc(v)}</div><div class="d">{esc(d)}</div></div>'
        for k, v, d in cards
    )

    probe_rows = []
    for cli_name in ("hive", "hive-browser"):
        block = clis.get(cli_name)
        if not block:
            continue
        for item in block["diff"].get("probes", []):
            probe_rows.append(
                "<tr>"
                f"<td><code>{esc(cli_name)}</code></td>"
                f"<td>{esc(item.get('probe',''))}</td>"
                f"<td class=\"num\">{'—' if item.get('python') is None else item.get('python')}</td>"
                f"<td class=\"num\">{'—' if item.get('go') is None else item.get('go')}</td>"
                f"<td>{_probe_verdict(item.get('python'), item.get('go'))}</td>"
                "</tr>"
            )
    probe_table = (
        '<div class="tablewrap"><table><colgroup><col style="width:10%"><col style="width:44%">'
        '<col style="width:9%"><col style="width:9%"><col style="width:28%"></colgroup><thead><tr>'
        "<th>CLI</th><th>探针</th><th>Python 退出码</th><th>Go 退出码</th><th>判定</th>"
        "</tr></thead><tbody>" + ("".join(probe_rows) or '<tr><td colspan="5">无漂移条目</td></tr>') + "</tbody></table></div>"
    )

    battery_rows = []
    for cli_name in ("hive", "hive-browser"):
        block = clis.get(cli_name)
        if not block:
            continue
        py_bat = {tuple(p.get("argv", [])): p for p in (block.get("python") or {}).get("battery", [])}
        go_bat = {tuple(p.get("argv", [])): p for p in block.get("go", {}).get("battery", [])}
        for argv in list(py_bat) + [a for a in go_bat if a not in py_bat]:
            p, g = py_bat.get(argv), go_bat.get(argv)
            probe_item = p or g or {}
            py_head = (p or {}).get("stdout_head") or (p or {}).get("stderr_head") or ""
            go_head = (g or {}).get("stdout_head") or (g or {}).get("stderr_head") or ""
            if py_head and go_head and py_head != go_head:
                sample = f"py: {clip(py_head, 58)} · go: {clip(go_head, 58)}"
            else:
                sample = clip(py_head or go_head, 116)
            verdict = _probe_verdict(p.get("exit") if p else None, g.get("exit") if g else None)
            battery_rows.append(
                "<tr>"
                f"<td><code>{esc(cli_name)}</code></td>"
                f"<td>{esc(probe_item.get('probe',''))}</td>"
                f"<td><code>{esc(' '.join(argv) or '(none)')}</code></td>"
                f"<td class=\"num\">{'—' if not p else p.get('exit')}</td>"
                f"<td class=\"num\">{'—' if not g else g.get('exit')}</td>"
                f"<td>{verdict}</td>"
                f'<td><span class="small">{esc(clip(sample, 90))}</span></td>'
                "</tr>"
            )
    battery_table = (
        '<div class="tablewrap"><table><colgroup><col style="width:8%"><col style="width:16%"><col style="width:22%">'
        '<col style="width:7%"><col style="width:7%"><col style="width:16%"><col style="width:24%"></colgroup><thead><tr>'
        "<th>CLI</th><th>探针</th><th>命令</th><th>Python</th><th>Go</th><th>判定</th><th>输出头样本</th>"
        "</tr></thead><tbody>" + ("".join(battery_rows) or '<tr><td colspan="7">无 battery 数据</td></tr>') + "</tbody></table></div>"
    )

    defaults = contract.get("defaults", {})
    default_rows = []
    for flag, data in (defaults.get("flags_seen") or {}).items():
        kinds = data.get("kinds", {})
        mismatch = any(
            entry.get("flag") == flag for entry in defaults.get("literal_mismatch", [])
        )
        symbolic = any(entry.get("flag") == flag for entry in defaults.get("symbolic_only", []))
        if mismatch:
            verdict = pill("字面量不一致", "bad")
        elif symbolic:
            verdict = pill("仅符号（人工核对）", "info")
        else:
            verdict = pill("已逐值核对", "ok")
        default_rows.append(
            "<tr>"
            f"<td><code>{esc(flag)}</code></td>"
            f"<td>{esc(', '.join(data.get('python') or []) or '—')}</td>"
            f"<td>{esc(', '.join(data.get('go') or []) or '—')}</td>"
            f"<td>{esc(kinds.get('python',''))} / {esc(kinds.get('go',''))}</td>"
            f"<td>{verdict}</td>"
            "</tr>"
        )
    defaults_table = (
        '<div class="tablewrap"><table><colgroup><col style="width:16%"><col style="width:22%"><col style="width:22%">'
        '<col style="width:16%"><col style="width:24%"></colgroup><thead><tr>'
        "<th>参数</th><th>Python 默认</th><th>Go 默认</th><th>kind（py/go）</th><th>状态</th>"
        "</tr></thead><tbody>" + "".join(default_rows) + "</tbody></table></div>"
    )

    alignment = contract.get("alignment", {})
    align_cards = []
    for key, title in (("fixed", "已修"), ("kept", "保留"), ("deferred", "待办"), ("verified", "已核对")):
        items = alignment.get(key) or []
        list_html = "".join(f"<li>{md_inline(item)}</li>" for item in items) or "<li>—</li>"
        align_cards.append(
            f'<div class="card"><div class="k">{esc(title)} · {len(items)}</div><ul>{list_html}</ul></div>'
        )
    align_html = f'<div class="cols2">{"".join(align_cards)}</div>'

    return (
        f'<section id="s{number}"><h2><span class="num">{number}</span>对齐状态（摘要 / 探针 / battery / 默认值 / 台账）</h2>'
        f'<p class="sub">全部读自 <code>contracts/cli-surface.json</code> 的 <code>summary</code>、'
        f"<code>diff.probes</code>、<code>battery</code>、<code>defaults</code>、<code>alignment</code> 段；"
        f"本页不重算、不四舍五入。</p>"
        f'<div class="grid g4">{card_html}</div>'
        f"<h3>逐命令探针漂移（{len(probe_rows)} 条）</h3>{probe_table}"
        f"<h3>无副作用 battery 对照（{len(battery_rows)} 条）</h3>{battery_table}"
        f"<h3>默认值核对（{len(default_rows)} 个参数）</h3>"
        f'<p class="sub">方法：{esc(defaults.get("method",""))}</p>{defaults_table}'
        f"<h3>已修 / 保留 / 待办 / 已核对台账</h3>{align_html}"
        '<div class="callout ok"><b>复现命令（在 <code>go_hive/go-hive</code> 目录执行）：</b>'
        "<pre>make cli-surface-check    # 重采两侧 help + 探针，并与冻结契约对拍（约 2–3 分钟，不进 make ci）</pre>"
        "契约重算后重跑本页生成器即可刷新上面全部数字。</div>"
        "</section>"
    )


# ---------------------------------------------------------------------------
# page
# ---------------------------------------------------------------------------


def build_html(contract: dict, contract_bytes: bytes, entries: list[dict], cred: dict) -> tuple[str, dict]:
    stats = {"commands": 0, "top_commands": 0, "flag_rows": 0, "positional_rows": 0, "cred_paths": 0,
             "cred_usage_identical": 0, "cred_help_identical": 0}
    meta = contract.get("meta", {})
    summary = contract.get("summary", {})
    clis = contract.get("clis", {})
    digest = sha12(contract_bytes)

    outer = sum(1 for e in entries if not e["internal"])
    inner = sum(1 for e in entries if e["internal"])
    py_entries = sum(1 for e in entries if e["side"] == "python")
    go_entries = sum(1 for e in entries if e["side"] == "go")

    sections = []
    sections.append(
        '<section id="s01"><h2><span class="num">01</span>入口清单</h2>'
        f'<p class="sub">两侧对外入口共 {outer} 个（Python {py_entries} · Go {go_entries - inner}），'
        f"另有 {inner} 个内部工具（不属对外面）。安装/构建列来自被发现的 pyproject <code>[project.scripts]</code> "
        f"与 <code>go_hive/*/cmd/*/main.go</code>；契约差异列读自契约 JSON。</p>"
        f"{render_entries_section(contract, entries, cred)}"
        '<div class="callout info"><b>口径：</b>「对外入口」指用户或 agent 直接调用的 CLI；'
        "<code>browserbridge</code> 是 <code>hive serve</code> / <code>hive-browser</code> 拉起的守护进程，"
        "有 <code>-version</code> 等自身 flags 但没有面向用户的 help 契约，因此不进 §02/§03 的命令树；"
        "<code>hive-longmemeval</code>、<code>chromeloader</code> 只服务内部流程。</div>"
        "</section>"
    )
    sections.append(render_cli_section("02", "hive", clis.get("hive", {}), stats))
    sections.append(render_cli_section("03", "hive-browser", clis.get("hive-browser", {}), stats))
    sections.append(render_cred_section("04", cred, stats))
    sections.append(render_status_section("05", contract))
    sections.append(
        '<section id="s06"><h2><span class="num">06</span>生成与复现</h2>'
        "<ul>"
        f"<li>生成器：<code>go_hive/scripts/gen_cli_surface_html.py</code>；契约：<code>go_hive/contracts/cli-surface.json</code>（sha256 <code>{digest}</code>）。</li>"
        "<li>防漂移：<code>python3 go_hive/scripts/gen_cli_surface_html.py --check</code>（重生成与落盘不一致退出 1）。</li>"
        "<li>契约重算：<code>make cli-surface-check</code>（<code>go_hive/go-hive/</code>，递归 <code>--help</code> + 退出码探针 + 对拍）。</li>"
        "<li>本页渲染计数（生成时打印）："
        f"命令块 {stats['commands']}（顶层 {stats['top_commands']}）· 参数行 {stats['flag_rows']} · "
        f"位置参数行 {stats['positional_rows']} · hive-cred 路径 {stats['cred_paths']}。</li>"
        "<li>相关文档：<code>go_hive/docs/cli-surface-diff.md</code>（逐命令 diff 报告）· "
        "<code>go_hive/docs/cli-surface-alignment.json</code>（已修/保留/待办）· "
        '<a href="module-interfaces.html">模块接口不变量</a> · '
        '<a href="browser-bridge.html">Browser Bridge</a> · '
        '<a href="environment-variables.html">环境变量</a>。</li>'
        "</ul></section>"
    )

    meta_html = "".join(
        [
            f'<span>契约：<code>contracts/cli-surface.json</code> · sha256 <b>{digest}</b></span>',
            f'<span>契约采集：<b>{esc(meta.get("generated_at","—"))}</b> · 基线 <code>{esc(meta.get("baseline_commit","—"))}</code></span>',
            f'<span>入口：<b>{outer}</b>（Python {py_entries} / Go {go_entries - inner}）+ 内部 {inner}</span>',
            f'<span>命令路径：hive <b>{clis.get("hive",{}).get("diff",{}).get("totals",{}).get("paths_python","—")}</b>'
            f' / <b>{clis.get("hive",{}).get("diff",{}).get("totals",{}).get("paths_go","—")}</b>（Python/Go）· '
            f'hive-browser <b>{clis.get("hive-browser",{}).get("diff",{}).get("totals",{}).get("paths_python","—")}</b>'
            f' / <b>{clis.get("hive-browser",{}).get("diff",{}).get("totals",{}).get("paths_go","—")}</b></span>',
            f'<span>probe 漂移 <b>{summary.get("probe_drift_total","—")}</b> · battery 漂移 <b>{summary.get("battery_drift_total","—")}</b></span>',
        ]
    )

    return (
        f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{esc(TITLE)}</title>
<style>{CSS}</style>
</head><body>
<div class="wrap">

<header>
  <div class="eyebrow">GO_HIVE · CLI SURFACE</div>
  <h1>CLI 对外面：<span>入口 / 命令 / 参数</span></h1>
  <p class="lede">两侧 CLI 的对外面落盘：入口清单、<code>hive</code> 与 <code>hive-browser</code> 的递归命令树
  （usage / 参数 / choices / 默认 / 位置参数）、<code>hive-cred</code> 的实时 <code>--help</code> 对照，
  以及探针、battery、默认值与对齐台账。命令树与差异计数全部读自
  <code>go_hive/contracts/cli-surface.json</code>（本页不手写任何数字），<code>hive-cred</code> 段为生成时实时捕获。
  Go 内部扩展（如 <code>queen-mail-mcp</code> 与浏览器 legacy 动词）带「已/将隐藏」徽标；隐藏后重采契约本页会自动消失。</p>
  <div class="meta">{meta_html}</div>
  <nav class="toc">
    <a href="#s01">01 入口清单</a><a href="#s02">02 hive 命令树</a><a href="#s03">03 hive-browser 命令树</a>
    <a href="#s04">04 hive-cred</a><a href="#s05">05 对齐状态</a><a href="#s06">06 生成与复现</a>
  </nav>
</header>

{''.join(sections)}

<footer>由 <code>go_hive/scripts/gen_cli_surface_html.py</code> 从 <code>contracts/cli-surface.json</code> +
实时 <code>hive-cred --help</code> 生成 · 命令块 {stats['commands']} · 参数行 {stats['flag_rows']} ·
位置参数行 {stats['positional_rows']} · <code>--check</code> 可重放 · 不写入任何凭据值</footer>
</div></body></html>
""",
        stats,
    )


def capture_hive_cred(repo: Path, scratch: Path, args) -> dict:
    home = scratch / "home"
    home.mkdir(parents=True, exist_ok=True)
    env = child_env(repo, home)
    py_cmd, py_error = find_hive_cred_python(repo, args.cred_python, env, repo)
    if args.cred_go:
        explicit_go = Path(args.cred_go)
        go_cmd = [str(explicit_go)] if explicit_go.exists() else None
        go_error = None if go_cmd else f"{args.cred_go} 不存在"
    else:
        go_cmd, go_error = build_go_cred(scratch / "bin", args.go_bin_dir)
    result: dict = {"python_cmd": py_cmd, "go_cmd": go_cmd, "error": None}
    if py_cmd is None or go_cmd is None:
        result["error"] = py_error or go_error
        return result
    py_tree, py_error = capture_help_tree(py_cmd, env, repo, "hive-cred")
    if py_tree is None:
        result["error"] = f"Python 侧：{py_error}"
        return result
    go_tree, go_error = capture_help_tree(go_cmd, env, repo, "hive-cred")
    if go_tree is None:
        result["error"] = f"Go 侧：{go_error}"
        return result
    result["python"] = py_tree
    result["go"] = go_tree
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--check", action="store_true", help="regenerate and compare with the committed page")
    parser.add_argument("--contract", default=str(DEFAULT_CONTRACT))
    parser.add_argument("--output", default=str(DEFAULT_OUTPUT))
    parser.add_argument("--repo", default=str(REPO))
    parser.add_argument("--python-bin", default=os.environ.get("CLI_SURFACE_PYTHON"))
    parser.add_argument("--go-bin-dir", default=os.environ.get("CLI_SURFACE_GO_BIN_DIR"))
    parser.add_argument("--scratch", default=os.environ.get("CLI_SURFACE_HTML_SCRATCH"))
    parser.add_argument("--cred-python", default=os.environ.get("CLI_SURFACE_HIVE_CRED"))
    parser.add_argument("--cred-go", default=os.environ.get("CLI_SURFACE_HIVE_CRED_GO"))
    args = parser.parse_args()

    repo = Path(args.repo).resolve()
    contract_path = Path(args.contract)
    output_path = Path(args.output)
    scratch = Path(args.scratch) if args.scratch else REPO / "go_hive" / ".cli-surface-scratch" / "cli-surface-html"

    if args.python_bin:
        os.environ["CLI_SURFACE_PYTHON"] = args.python_bin
    if args.cred_python:
        os.environ["CLI_SURFACE_HIVE_CRED"] = args.cred_python

    contract_bytes = contract_path.read_bytes()
    contract = json.loads(contract_bytes)
    entries = discover_entries(contract, repo)
    cred = capture_hive_cred(repo, scratch, args)
    html, stats = build_html(contract, contract_bytes, entries, cred)

    counts = (
        f"entries={len(entries)} (python {sum(1 for e in entries if e['side']=='python')} / "
        f"go {sum(1 for e in entries if e['side']=='go')}) · "
        f"command blocks={stats['commands']} · flag rows={stats['flag_rows']} · "
        f"positional rows={stats['positional_rows']} · hive-cred paths={stats['cred_paths']} "
        f"(usage 一致 {stats['cred_usage_identical']}, help 一致 {stats['cred_help_identical']})"
    )
    if args.check:
        if output_path.exists() and output_path.read_text(encoding="utf-8") == html:
            print(f"ok: {rel(output_path)} matches {rel(contract_path)} · {counts}")
            return 0
        print(f"stale: {rel(output_path)} differs from {rel(contract_path)}; run gen_cli_surface_html.py · {counts}")
        return 1
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(html, encoding="utf-8")
    print(f"wrote {rel(output_path)} ({len(html)} bytes) · {counts}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
