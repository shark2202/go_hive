#!/usr/bin/env python3
"""Generate the reusable Colony import/export reference (zh-CN HTML + JSON).

DATA is the single source of truth. The page records the current capability
boundary:

  * import: six HTTP routes, no frontend/CLI entry;
  * export: no route, no CLI, no UI (only a Finder reveal action);
  * exact one-shot and chunked upload recipes, archive layouts and limits.

Usage:
  python3 go_hive/scripts/gen_colony_import_export_doc.py
  python3 go_hive/scripts/gen_colony_import_export_doc.py --check

Outputs:
  go_hive/docs/colony-import-export.html
  go_hive/docs/colony-import-export.json
"""

from __future__ import annotations

import argparse
import html
import json
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive" / "docs" / "colony-import-export.html"
OUT_JSON = REPO / "go_hive" / "docs" / "colony-import-export.json"
ESC = lambda value: html.escape(str(value), quote=True)


class Raw(str):
    """A trusted HTML fragment produced by code()/inline formatters."""


def code(value: str) -> Raw:
    return Raw(f"<code>{ESC(value)}</code>")


def table(headers, rows, widths=None):
    th = "".join(
        f'<th style="width:{widths[i]}">{ESC(h)}</th>' if widths else f"<th>{ESC(h)}</th>"
        for i, h in enumerate(headers)
    )
    body = "".join(
        "<tr>"
        + "".join(f"<td>{c if isinstance(c, Raw) else ESC(c)}</td>" for c in row)
        + "</tr>"
        for row in rows
    )
    return f'<div class="tablewrap"><table><thead><tr>{th}</tr></thead><tbody>{body}</tbody></table></div>'

DATA = {
    "schema": "hive-colony-import-export-doc/1",
    "title": "Colony 导入导出：能力、用法与缺口",
    "subtitle": "导入服务端完整、前端/CLI 没有入口；导出尚未实现。本文是可复用的 API 操作手册与现状快照。",
    "status": "import implemented · export absent · UI/CLI import entry absent",
    "quick": [
        ("导入", "6 条 HTTP 路由", "小包 one-shot multipart；大包 init → chunk → finalize"),
        ("前端入口", "0 处调用", "frontend/src 没有 colonies/import 调用"),
        ("CLI 入口", "0 个子命令", "hive colony 只有 list / info / delete"),
        ("导出", "0 条路由", "没有 export API；只有 reveal 打开 Finder"),
    ],
    "routes": [
        ("POST", "/api/colonies/import", "handleImportColony", "一次性 multipart 导入，返回 201"),
        ("POST", "/api/colonies/import/init", "handleInitUpload", "初始化分片上传，返回 upload_id 和 received=0"),
        ("PUT", "/api/colonies/import/chunk/{upload_id}?offset=N", "handleChunkUpload", "按 offset 顺序写入分片，单片 ≤128 MiB"),
        ("GET", "/api/colonies/import/status/{upload_id}", "handleUploadStatus", "查询已接收字节数，可断点续传"),
        ("POST", "/api/colonies/import/finalize/{upload_id}", "handleFinalizeUpload", "校验 sha256、解包并落盘，返回 201"),
        ("DELETE", "/api/colonies/import/{upload_id}", "handleCancelUpload", "取消未完成的上传并清理 staging"),
    ],
    "one_shot": {
        "title": "小包：一次性 multipart",
        "steps": [
            "在本地把 colony 目录打成 tar / tar.gz / tar.bz2，保留一个顶层目录。",
            "POST /api/colonies/import，字段 file / name / replace_existing。",
        ],
        "curl": """tar -czf /tmp/mycolony.tar.gz -C /Users/mac/hive/go_hive/.hive_go/colonies mycolony

curl -sS -X POST 'http://127.0.0.1:8787/api/colonies/import' \\
  -F 'file=@/tmp/mycolony.tar.gz' \\
  -F 'name=mycolony_imported' \\
  -F 'replace_existing=false'""",
        "response": """{
  "name": "mycolony_imported",
  "path": "/Users/mac/hive/go_hive/.hive_go/colonies/mycolony_imported",
  "files_imported": 123,
  "replaced": false
}""",
        "fields": [
            ("file", "必填", "归档文件；tar / tar.gz / tar.bz2，最大 2 GiB"),
            ("name", "可选", "导入后的 colony slug；不传就用归档顶层目录名"),
            ("replace_existing", "可选", "true 覆盖已存在的目标；false 时冲突返回 409"),
        ],
    },
    "chunked": {
        "title": "大包：分片上传",
        "steps": [
            "POST /api/colonies/import/init，提交 filename / total_bytes / sha256 / name / replace_existing。",
            "PUT /api/colonies/import/chunk/{upload_id}?offset=N 逐片上传；offset 必须等于服务端已接收字节数。",
            "GET /api/colonies/import/status/{upload_id} 查询进度。",
            "POST /api/colonies/import/finalize/{upload_id} 校验 sha256 并解包。",
            "DELETE /api/colonies/import/{upload_id} 可取消未完成上传。",
        ],
        "init": """curl -sS -X POST 'http://127.0.0.1:8787/api/colonies/import/init' \\
  -H 'Content-Type: application/json' \\
  -d '{
    "filename": "mycolony.tar.gz",
    "total_bytes": 123456789,
    "sha256": "<64 位十六进制 sha256>",
    "name": "mycolony_imported",
    "replace_existing": false
  }'""",
        "chunk": """curl -sS -X PUT \\
  'http://127.0.0.1:8787/api/colonies/import/chunk/<upload_id>?offset=0' \\
  -H 'Content-Type: application/octet-stream' \\
  --data-binary '@/tmp/chunk-000'""",
        "finish": """curl -sS 'http://127.0.0.1:8787/api/colonies/import/status/<upload_id>'

curl -sS -X POST \\
  'http://127.0.0.1:8787/api/colonies/import/finalize/<upload_id>'""",
        "cancel": """curl -sS -X DELETE 'http://127.0.0.1:8787/api/colonies/import/<upload_id>'""",
    },
    "archive": {
        "single_root": """mycolony/
  metadata.json
  worker.json
  tools.json
  skills/
  ...""",
        "multi_root": """colonies/mycolony/...
queens/...
memories/...""",
        "limits": [
            ("归档上限", "2 GiB", "单次一次性上传的最大字节数"),
            ("分片上限", "128 MiB", "单次 PUT 的 Content-Length 上限"),
            ("并发上传", "32", "超过限制返回 429"),
            ("staging TTL", "24 小时", "超时未完成的上传会被回收"),
            ("支持格式", "tar / tar.gz / tar.bz2", "xz / zstd 明确拒绝"),
        ],
    },
    "errors": [
        ("409", "目标 colony 已存在", "加 replace_existing=true，或换 name"),
        ("400", "归档格式/路径非法、缺少 file、字段非法", "检查 multipart 字段和 tar 内容"),
        ("413", "归档或分片超过上限", "使用分片上传或缩小归档"),
        ("429", "并发上传达到 32", "完成或取消一个上传后重试"),
        ("415", "不是 multipart/form-data", "一次性导入必须用 multipart，不要直接 PUT tar"),
        ("500", "staging 或解包 IO 失败", "查看 serve 日志；失败会回滚目标目录"),
    ],
    "gaps": {
        "ui": [
            "frontend/src/api/colonies.ts 没有 import 方法",
            "frontend/src 没有 /api/colonies/import 调用",
            "Sidebar / colony 页面没有 Import 按钮",
        ],
        "cli": [
            "contracts/cli-surface.json 的 hive colony 只有 list / info / delete",
            "没有 hive colony import 这样的命令；不要按伪命令操作",
        ],
        "export": [
            "没有 export / download 路由",
            "没有 CLI export",
            "没有前端导出入口",
            "POST /api/colonies/{id}/reveal 只是在 Finder 打开目录，不产生可下载字节",
            "要导出只能自行 tar/zip 目录，或后续新增 export 能力",
        ],
    },
    "security": [
        "safeExtractArchive 拒绝路径穿越（.. / 绝对路径 / 越界 symlink）",
        "目标已存在且没有 replace_existing 时返回 409，不静默覆盖",
        "解包失败会 RemoveAll 目标目录，避免留下半成品",
        "name 走 validateColonyName，只接受合法 colony slug",
    ],
    "verification": [
        "internal/server/routes_colonies.go 注册 6 条 import 路由",
        "internal/server/routes_colonies_import_test.go 有 45 个 Test 函数",
        "contracts/api-routes.json 收录 6 条 import 路由",
        "frontend/src 对 colonies/import 的调用为 0",
        "hive colony CLI 面没有 import 子命令",
        "全仓没有 export / download 的 colony 路由",
    ],
    "sources": [
        ("实现", "go_hive/go-hive/internal/server/routes_colonies_import.go", "6 个 handler、格式、限额、安全解包"),
        ("路由", "go_hive/go-hive/internal/server/routes_colonies.go:42-58", "import 路由注册"),
        ("测试", "go_hive/go-hive/internal/server/routes_colonies_import_test.go", "45 个 Test"),
        ("前端 API", "go_hive/go-hive/frontend/src/api/colonies.ts", "只有 list/scaffold/getTools/updateTools/rename/delete"),
        ("旧审计", "go_hive/docs/queen-tools-and-colony-import-export.md", "导入/导出四层审计"),
        ("契约", "go_hive/contracts/api-routes.json", "6 条 import 路由已冻结"),
    ],
    "related": [
        ("Colony 导入导出审计", "queen-tools-and-colony-import-export.md"),
        ("Colony fork 与 clone", "colony-fork-clone.md"),
        ("Colony 建议与 pivot", "colony-suggestion-dispatch.md"),
        ("API 路由契约", "../contracts/api-routes.json"),
    ],
}


def bullets(items):
    return "<ul>" + "".join(f"<li>{ESC(item)}</li>" for item in items) + "</ul>"


def build(data: dict) -> str:
    d = data
    routes = [(code(m), code(p), code(h), ESC(desc)) for m, p, h, desc in d["routes"]]
    one = d["one_shot"]
    chunk = d["chunked"]
    archive = d["archive"]
    cards = "".join(
        f'<div class="card"><div class="k">{ESC(k)}</div><div class="v">{ESC(v)}</div><div class="d">{ESC(note)}</div></div>'
        for k, v, note in d["quick"]
    )
    limits = [(ESC(a), Raw(f"<b>{ESC(b)}</b>"), ESC(c)) for a, b, c in archive["limits"]]
    errors = [(code(a), ESC(b), ESC(c)) for a, b, c in d["errors"]]
    source_rows = [(ESC(a), code(b), ESC(c)) for a, b, c in d["sources"]]
    related = " · ".join(f'<a href="{ESC(href)}">{ESC(label)}</a>' for label, href in d["related"])
    status_class = "warn"

    css = """
:root{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}
*{box-sizing:border-box}body{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}
a{color:var(--accent2);text-decoration:none}a:hover{text-decoration:underline}.wrap{max-width:1280px;margin:0 auto;padding:44px 26px 80px}
header{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}.eyebrow{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}
h1{font-size:30px;line-height:1.25;margin:10px 0;letter-spacing:-.02em}h1 span{color:var(--accent)}.lede{color:var(--muted);max-width:1040px;font-size:15px}
.meta{display:flex;gap:12px;flex-wrap:wrap;margin-top:16px}.pill{display:inline-block;border:1px solid var(--line);background:var(--panel);border-radius:999px;padding:5px 10px;color:var(--muted);font-size:12px}.pill b{color:var(--text)}.pill.warn{border-color:#59451c;color:#efd089;background:#1a1509}
section{margin:38px 0}h2{font-size:22px;margin:0 0 8px}h2 .num{color:var(--accent);font-size:14px;margin-right:8px}h3{font-size:15.5px;margin:22px 0 8px;color:#cfdae6}p{color:#c6d2df;margin:8px 0}.sub{color:var(--muted);font-size:12.5px}
.grid{display:grid;gap:12px}.g4{grid-template-columns:repeat(4,minmax(0,1fr))}.g2{grid-template-columns:repeat(2,minmax(0,1fr))}.grid>*{min-width:0}
@media(max-width:900px){.g4,.g2{grid-template-columns:1fr 1fr}}@media(max-width:560px){.g4,.g2{grid-template-columns:1fr}.wrap{padding:30px 16px 60px}h1{font-size:24px}}
.card{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px;min-width:0}.card .k{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700}.card .v{font-size:20px;font-weight:800;margin-top:5px;letter-spacing:-.01em}.card .d{color:var(--muted);font-size:12px;margin-top:5px}
.tablewrap{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2);max-width:100%}table{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed}th,td{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word}th{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d}tr:hover td{background:#121a25}tr:last-child td{border-bottom:0}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}
pre{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2;max-width:100%}
.callout{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4;overflow-wrap:anywhere}.callout.bad{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7}.callout.info{border-color:var(--accent2);background:#0d1622;color:#cfe3ff}
.toc{display:flex;flex-wrap:wrap;gap:7px;margin-top:18px}.toc a{font-size:12px;border:1px solid var(--line);background:var(--panel);padding:5px 9px;border-radius:999px;color:var(--muted)}.toc a:hover{color:var(--text);text-decoration:none}
ul,ol{margin:7px 0 7px 20px;padding:0}li{margin:4px 0;color:#c6d2df}footer{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}
@media(max-width:560px){table{font-size:12px}th,td{padding:7px 6px}.toc a{padding:8px 12px;font-size:13px;min-height:44px;display:inline-flex;align-items:center}}
"""

    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{ESC(d['title'])}</title>
<style>{css}</style></head><body><div class="wrap">
<header>
<div class="eyebrow">Hive · Colony Import / Export</div>
<h1>Colony 导入导出 <span>能力、用法与缺口</span></h1>
<p class="lede">{ESC(d['subtitle'])}</p>
<div class="meta"><span class="pill {status_class}"><b>状态</b> {ESC(d['status'])}</span><span class="pill"><b>口径</b> 以当前 Go 源码与 API 为准</span><span class="pill"><b>产物</b> HTML + JSON</span></div>
<div class="toc"><a href="#s00">总览</a><a href="#s01">导入路由</a><a href="#s02">一次性导入</a><a href="#s03">分片上传</a><a href="#s04">归档与限额</a><a href="#s05">错误</a><a href="#s06">UI/CLI 缺口</a><a href="#s07">导出</a><a href="#s08">代码索引</a><a href="#s09">维护</a></div>
</header>

<section id="s00"><h2><span class="num">00</span>总览</h2>
<div class="grid g4">{cards}</div>
<div class="callout info"><b>一句话：</b>导入是服务端完整能力，但目前只有 HTTP API；前端和 CLI 都没有入口。导出完全没做，不能把 <code>reveal</code> 当成导出。</div>
</section>

<section id="s01"><h2><span class="num">01</span>导入路由</h2>
<p>注册位置：{code('internal/server/routes_colonies.go:42-58')}。六条路由对应 {code('routes_colonies_import.go')} 的六个 handler。</p>
{table(["方法", "路径", "handler", "用途"], routes, widths=["9%", "40%", "23%", "28%"])}
</section>

<section id="s02"><h2><span class="num">02</span>小包：一次性 multipart</h2>
<p>适合不需要断点续传的小归档。先打 tar，再 POST；归档里保留一个顶层 colony 目录。</p>
<pre>{ESC(one['curl'])}</pre>
<p>成功返回 {code('201')}：</p>
<pre>{ESC(one['response'])}</pre>
<h3>字段</h3>
{table(["字段", "必填", "说明"], [(code(a), ESC(b), ESC(c)) for a, b, c in one['fields']], widths=["18%", "12%", "70%"])}
</section>

<section id="s03"><h2><span class="num">03</span>大包：分片上传</h2>
<p>超过单次上限、网络不稳定或需要断点续传时使用。分片必须按 offset 顺序发送；服务端检查 {code('Content-Length')} 和累计字节数。</p>
<ol>{''.join(f'<li>{ESC(step)}</li>' for step in chunk['steps'])}</ol>
<h3>1. 初始化</h3><pre>{ESC(chunk['init'])}</pre>
<h3>2. 上传分片</h3><pre>{ESC(chunk['chunk'])}</pre>
<h3>3. 查询与完成</h3><pre>{ESC(chunk['finish'])}</pre>
<h3>4. 取消</h3><pre>{ESC(chunk['cancel'])}</pre>
</section>

<section id="s04"><h2><span class="num">04</span>归档布局与限额</h2>
<div class="grid g2">
<div class="card"><div class="k">单 colony</div><pre>{ESC(archive['single_root'])}</pre><div class="d">顶层目录名默认作为 colony id；可用 name 覆盖。</div></div>
<div class="card"><div class="k">多根布局</div><pre>{ESC(archive['multi_root'])}</pre><div class="d">多根导入要求存在 colonies/&lt;name&gt;/。</div></div>
</div>
{table(["限制", "值", "说明"], limits, widths=["18%", "24%", "58%"])}
</section>

<section id="s05"><h2><span class="num">05</span>常见错误</h2>
{table(["状态", "含义", "处理"], errors, widths=["10%", "42%", "48%"])}
<div class="callout"><b>覆盖语义：</b>目标存在且没有 {code('replace_existing=true')} 时返回 {code('409')}，不会隐式覆盖。覆盖会先删除目标；解包失败会回滚，不留下半成品。</div>
</section>

<section id="s06"><h2><span class="num">06</span>为什么没有入口</h2>
<div class="grid g2">
<div class="card"><div class="k">前端</div>{bullets(d['gaps']['ui'])}</div>
<div class="card"><div class="k">CLI</div>{bullets(d['gaps']['cli'])}</div>
</div>
<div class="callout bad"><b>不要把 API 能力当成产品入口：</b>服务端能导入，不代表用户在页面上能点到。当前只能 curl / API，或让 queen 用 {code('terminal_exec')} 执行 curl。</div>
</section>

<section id="s07"><h2><span class="num">07</span>导出：不存在，不是未接线</h2>
<p>路由、CLI、前端三层都没有 export/download；唯一接近的是 {code('POST /api/colonies/{id}/reveal')}，它只在 Finder 打开目录，不产生可下载字节。</p>
{bullets(d['gaps']['export'])}
<div class="callout info"><b>结论：</b>当前是“能进不能出”。要真正导出，需要新增服务端 tar/zip 下载路由（含权限、大小、路径过滤），再补 CLI/UI；仅靠前端按钮无法完成。</div>
</section>

<section id="s08"><h2><span class="num">08</span>代码索引与验证</h2>
{table(["层", "文件", "内容"], source_rows, widths=["12%", "43%", "45%"])}
<h3>验证锚点</h3>
{bullets(d['verification'])}
</section>

<section id="s09"><h2><span class="num">09</span>维护与复用</h2>
<ol>
<li><b>生成：</b>{code('python3 go_hive/scripts/gen_colony_import_export_doc.py')}</li>
<li><b>防漂移：</b>{code('python3 go_hive/scripts/gen_colony_import_export_doc.py --check')}；HTML 或 JSON 与 DATA 不一致时退出 1。</li>
<li><b>改代码后：</b>更新 DATA 中的路由、限额或缺口，重跑生成器并提交 HTML + JSON。</li>
<li><b>相关：</b>{related}</li>
</ol>
</section>

<footer><p>生成器：<code>go_hive/scripts/gen_colony_import_export_doc.py</code> · 数据：<a href="colony-import-export.json">colony-import-export.json</a> · 本文不替代 API 契约；以源码与 contracts 为准。</p></footer>
</div></body></html>"""


def render(data: dict) -> tuple[str, str]:
    return build(data), json.dumps(data, ensure_ascii=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true", help="fail if generated files are stale")
    args = parser.parse_args()

    expected_html, expected_json = render(DATA)
    if args.check:
        problems = []
        for path, expected in ((OUT_HTML, expected_html), (OUT_JSON, expected_json)):
            if not path.exists():
                problems.append(f"missing {path}")
            elif path.read_text(encoding="utf-8") != expected:
                problems.append(f"stale {path}")
        if problems:
            print("colony import/export doc: " + "; ".join(problems))
            return 1
        print("colony import/export doc: current")
        return 0

    OUT_HTML.write_text(expected_html, encoding="utf-8")
    OUT_JSON.write_text(expected_json, encoding="utf-8")
    print(f"wrote {OUT_HTML} ({OUT_HTML.stat().st_size:,} bytes)")
    print(f"wrote {OUT_JSON} ({OUT_JSON.stat().st_size:,} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
