#!/usr/bin/env python3
"""Generate the ast_grep requirements -> design -> acceptance chain.

Source of truth is the DATA object below. The generated files are:
  - go_hive/docs/ast-grep-design.html
  - go_hive/docs/ast-grep-design.json

Usage:
  python3 go_hive/scripts/gen_ast_grep_design.py
  python3 go_hive/scripts/gen_ast_grep_design.py --check   # drift gate
"""

from __future__ import annotations

import argparse
import html
import json
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive" / "docs" / "ast-grep-design.html"
OUT_JSON = REPO / "go_hive" / "docs" / "ast-grep-design.json"
ESC = lambda value: html.escape(str(value), quote=True)


def p(text: str) -> str:
    return f"<p>{ESC(text)}</p>"


DATA = {
    "schema_version": 1,
    "feature": "ast_grep",
    "status": "design",
    "decision": "Implement ast_grep in Go with github.com/odvcencio/gotreesitter; keep CGO_ENABLED=0. Model path inputs are absolute-only and must resolve inside the workspace root.",
    "package_layout": {
        "package": "internal/astgrep",
        "adapter": "internal/tools/builtin/ast_grep.go",
        "doc": "internal/astgrep/doc.go",
        "rule": "The core package owns parsing, language detection, the structural matcher, path safety and result rendering. It must not import internal/server, internal/cli or internal/tools/builtin. The tool schema, registration and model-facing adapter live in the adapter package.",
        "maintenance": "Maintain astgrep as an independent Go package: its own API, doc.go, package tests, gofmt/size/lane-prefix gates, and a caller contract that can be exercised without a session or tool registry.",
    },
    "source_refs": [
        {
            "label": "oh-my-pi ast_grep tool contract",
            "url": "https://github.com/can1357/oh-my-pi/blob/main/docs/tools/ast-grep.md",
            "use": "input fields, pattern grammar, supported languages, output shape",
        },
        {
            "label": "gotreesitter v0.57.0",
            "url": "https://github.com/odvcencio/gotreesitter",
            "use": "pure-Go tree-sitter runtime, grammars registry, parser/query API",
        },
        {
            "label": "oh-my-pi native crates",
            "url": "https://github.com/can1357/oh-my-pi/blob/main/docs/native-crates.md",
            "use": "omp-ast role: tree-sitter/ast-grep registry, matching/editing, summarization",
        },
    ],
    "problem": {
        "statement": "go_hive has no model-facing structural code search tool. Regex search cannot match AST shape, captured metavariables, or multi-line syntax.",
        "user_value": [
            "Find code by structure rather than exact text.",
            "Use one tool across Go, Python, TypeScript, Rust, JSON, YAML and other supported grammars.",
            "Keep the six-platform pure-Go build: no C toolchain, no cgo, no browser or Node sidecar.",
        ],
        "why_not_now": "This is a post-1.0, opt-in capability. Hive upstream has no ast_grep tool, so it is not part of the current parity surface.",
    },
    "requirements": [
        {
            "id": "R1",
            "priority": "P0",
            "statement": "Expose a model-facing tool named ast_grep with the oh-my-pi input fields pat, path, lang and skip.",
            "acceptance": "Tool schema and tool-definition golden match; empty pat is rejected; path defaults to the workspace root.",
            "traces": ["D3", "A1", "A2"],
        },
        {
            "id": "R2",
            "priority": "P0",
            "statement": "The Go core and all six release targets build with CGO_ENABLED=0.",
            "acceptance": "Cross-build and test matrix pass with cgo off; no C compiler is required.",
            "traces": ["D2", "A3"],
        },
        {
            "id": "R3",
            "priority": "P0",
            "statement": "Support ast-grep metavariables $NAME, $_, $$$NAME and $$$, including repeated-variable equality and lazy variadic matching.",
            "acceptance": "Golden matcher cases cover captures, wildcards, repeated captures, zero-or-more captures, multi-line matches and backtracking.",
            "traces": ["D5", "A4"],
        },
        {
            "id": "R4",
            "priority": "P0",
            "statement": "Infer language from the path, or use an explicit lang override when the extension is ambiguous.",
            "acceptance": "Language matrix covers .go, .py, .ts/.tsx, .rs, .json, .yaml, .md and an explicit .h -> cpp override.",
            "traces": ["D4", "A5"],
        },
        {
            "id": "R5",
            "priority": "P0",
            "statement": "Preserve the model-visible result shape: grouped paths, line anchors, match text, optional metavariable metadata and explicit no-match/limit notices.",
            "acceptance": "Text golden matches the chosen display mode; Go tests pin grouping, indentation, continuation lines, skip and cap behavior.",
            "traces": ["D6", "A6"],
        },
        {
            "id": "R6",
            "priority": "P0",
            "statement": "Never escape the allowed workspace roots, follow symlinks outside them, or read unbounded/ binary files.",
            "acceptance": "Path traversal, symlink escape, binary, oversized file and semicolon-list tests fail closed with a typed error.",
            "traces": ["D7", "A7"],
        },
        {
            "id": "R7",
            "priority": "P1",
            "statement": "Bound scan work: result cap, per-file byte cap, per-file parse budget, cancellation and parser/tree cleanup.",
            "acceptance": "Fifty-result cap, skip paging, cancelled context, large-repo smoke and goroutine/tree leak checks pass.",
            "traces": ["D8", "A8"],
        },
        {
            "id": "R8",
            "priority": "P1",
            "statement": "Keep the dependency surface explicit and small: one new Go module plus only the grammar packages compiled into the tool.",
            "acceptance": "go.mod diff contains only gotreesitter; binary-size and import audit recorded; no runtime download of grammars.",
            "traces": ["D2", "D4", "A9"],
        },
        {
            "id": "R9",
            "priority": "P0",
            "statement": "模型输入 path 必须是绝对路径且在 workspace root 内；相对路径返回 path must be absolute；绝对越界/../symlink 仍 path escape。",
            "acceptance": "相对 path 被拒绝；绝对根内通过；越界/../symlink 逃逸被拒绝；semicolon 列表逐项校验。",
            "traces": ["D9", "A11"],
        },
    ],
    "non_goals": [
        "Do not port omp-ast's editor/rewrite/staging engine in v1; read-only search only.",
        "Do not support internal URLs or fetched web URLs in v1; start with local workspace paths and globs.",
        "Do not promise byte-exact tree-sitter recovery on malformed source; gotreesitter publishes a measured parity scope, not universal equivalence.",
        "Do not add ast_grep to the 1.0 parity claim; it is an opt-in post-1.0 capability.",
    ],
    "options": [
        {
            "id": "O1",
            "name": "cgo tree-sitter (smacker/go-tree-sitter or tree-sitter/go-tree-sitter)",
            "verdict": "rejected",
            "reason": "Requires a C toolchain per target, violates CGO_ENABLED=0 and the six-platform static build invariant.",
        },
        {
            "id": "O2",
            "name": "Rust omp-ast / ast-grep sidecar",
            "verdict": "fallback",
            "reason": "Highest behavioral fidelity, but adds a native process and per-platform binaries. Keep as fallback if pure-Go matching fails acceptance.",
        },
        {
            "id": "O3",
            "name": "github.com/odvcencio/gotreesitter",
            "verdict": "chosen",
            "reason": "Pure-Go runtime, 206 grammar registry, parser/query/incremental APIs, no cgo. Sufficient base for a read-only structural matcher; ast-grep-specific pattern semantics must be implemented above it.",
        },
        {
            "id": "O4",
            "name": "tree-sitter compiled to WASM + wazero",
            "verdict": "deferred",
            "reason": "Preserves pure Go, but adds a WASM build pipeline and a second matching engine; more complex than gotreesitter for no v1 benefit.",
        },
    ],
    "design": {
        "D1": {
            "title": "Tool registration",
            "text": "ast_grep is opt-in and default-off. Registration is gated by ASTGrepEnabled(home), which reads only <home>/ast_grep.json (user; an empty home falls back to HIVE_HOME). There is no project/session-level file: the session directory is the file tools' sandbox root and is agent-writable, so reading <sessionDir>/.hive/ast_grep.json would let the model opt itself into a tool the operator left off (see go_hive/docs/config-roots.html). The file is a JSON object with a top-level boolean enabled; missing config means disabled, and malformed or unreadable config fails closed. There is no environment-variable override. When enabled, the model sees a normal tool definition in the lazy tier; a build tag is considered only if binary size becomes a problem.",
        },
        "D2": {
            "title": "Dependency boundary",
            "text": "Add github.com/odvcencio/gotreesitter v0.57.x to go.mod. Import the grammar registry or selected grammar packages; never import a C binding. Keep a small language set in the default binary and make additional grammars opt-in if size demands it.",
        },
        "D3": {
            "title": "Input contract",
            "text": "pat: string, required, trimmed, non-empty. path: absolute file, directory, glob, or semicolon-separated list inside the workspace root; default the workspace root. Relative paths are rejected. lang: optional canonical language override. skip: non-negative integer offset, default 0. No limit field in v1; the implementation caps visible results at 50 and pages with skip.",
        },
        "D4": {
            "title": "Language registry",
            "text": "Resolve explicit lang through a canonical alias table. Otherwise infer from the filename through grammars.DetectLanguage. Unknown language or unsupported extension is a typed unsupported-language result, never a silent skip. .cu/.cuh map to cpp; .h needs an explicit lang when ambiguous.",
        },
        "D5": {
            "title": "Pattern matcher",
            "text": "Parse the pattern with the same grammar as the candidate. Compile the pattern AST into a small matcher IR: node kind, field names, literal text, metavariable captures, wildcard, and variadic sequence. Walk candidate nodes with backtracking; repeated $NAME compares the captured source text; $$$NAME greedily consumes zero or more sibling nodes but backtracks to the next satisfiable match. Do not treat tree-sitter query syntax as ast-grep pattern syntax.",
        },
        "D6": {
            "title": "Result renderer",
            "text": "Group by path, sort by path and byte offset, slice by skip, cap at 50. Render the same line-anchor shape as oh-my-pi: path header, *LINE:text anchors, continuation lines, optional meta: NAME=value, then a limit notice. No matches returns the explicit no-match string. Parse errors are reported separately and deduplicated.",
        },
        "D7": {
            "title": "Safety boundary",
            "text": "Resolve every path under the session workspace; reject .. after cleaning; evaluate symlinks and reject targets outside the root. Skip binary files, files above the per-file cap, and VCS/cache directories. Semicolon entries are validated independently; one bad entry fails the request rather than silently dropping it.",
        },
        "D8": {
            "title": "Execution model",
            "text": "Walk candidate files with a bounded worker pool; parse with a per-file timeout; retain only the current tree; release trees explicitly; stop on context cancellation. Cache compiled patterns per (language, pattern) within one call. Return a typed error for timeout, cancellation, parse-failure threshold and unsupported language.",
        },
        "D9": {
            "title": "Path input is absolute",
            "text": "go_hive has no project cwd, so the model-facing path is absolute-only: the adapter splits the semicolon list, rejects an empty entry, and requires filepath.IsAbs on every entry before calling astgrep.Search. The core still resolves relative paths for standalone library callers and its own tests, but the tool contract does not. Absolute paths are resolved through symlinks before root containment so a symlinked workspace root (macOS /var -> /private/var) is accepted while a target outside the root is rejected.",
        },
    },
    "pipeline": [
        "normalize and validate pat/path/lang/skip",
        "resolve paths and infer languages",
        "compile pattern against the selected grammar",
        "walk candidates, parse with gotreesitter, run structural matcher",
        "capture metavariables and source ranges",
        "sort, dedupe, apply skip and 50-result cap",
        "render text + structured details",
        "release trees and return",
    ],
    "tool_contract": {
        "name": "ast_grep",
        "input_schema": {
            "type": "object",
            "additionalProperties": False,
            "properties": {
                "pat": {"type": "string", "description": "Single structural pattern using $NAME, $_, $$$NAME or $$$."},
                "path": {"type": "string", "description": "Absolute file, directory, glob or semicolon-separated list inside the workspace root; defaults to the workspace root. Relative paths are rejected."},
                "lang": {"type": "string", "description": "Optional canonical language override."},
                "skip": {"type": "integer", "minimum": 0, "description": "Match offset; defaults to 0."},
            },
            "required": ["pat"],
        },
        "result_fields": {
            "text": "model-facing grouped matches, no-match or parse-error notice",
            "details": ["matchCount", "fileCount", "filesSearched", "limitReached", "parseErrors", "parseErrorsTotal", "scopePath", "searchPath"],
        },
    },
    "acceptance": [
        {"id": "A1", "test": "Tool definition exact", "pass": "name, description and input_schema match the frozen ast_grep contract."},
        {"id": "A2", "test": "Empty/invalid input", "pass": "empty pat, negative/fractional skip and malformed semicolon list return typed errors."},
        {"id": "A3", "test": "No-cgo matrix", "pass": "CGO_ENABLED=0 go test + six GOOS/GOARCH builds pass."},
        {"id": "A4", "test": "Pattern semantics", "pass": "golden matcher corpus covers capture, wildcard, repeated, variadic, multi-line and backtracking."},
        {"id": "A5", "test": "Language matrix", "pass": "Go/Python/TypeScript/Rust/JSON/YAML/Markdown and .h override pass."},
        {"id": "A6", "test": "Result golden", "pass": "grouping, line anchors, skip, cap, no-match and parse-error text match goldens."},
        {"id": "A7", "test": "Safety", "pass": "traversal, symlink escape, binary and oversized files fail closed."},
        {"id": "A8", "test": "Bounds", "pass": "50-result cap, cancellation, timeout and tree-release checks pass."},
        {"id": "A9", "test": "Dependency audit", "pass": "go.mod has only gotreesitter added; no runtime grammar download; binary size recorded."},
        {"id": "A10", "test": "Differential smoke", "pass": "on the fixture corpus, pure-Go results agree with ast-grep CLI for supported patterns; differences are registered."},
        {"id": "A11", "test": "Absolute path", "pass": "relative path returns path must be absolute; absolute under a symlinked root passes; outside/../symlink escape rejected; semicolon entries validated independently."},
    ],
    "risks": [
        {"risk": "gotreesitter recovery parity is measured, not universal", "impact": "malformed-source matches may differ from C tree-sitter", "mitigation": "register as an acceptance exception; test clean fixtures first; report parse issues instead of hiding them."},
        {"risk": "ast-grep metavariable semantics are nontrivial", "impact": "variadic/repeated captures can diverge", "mitigation": "build a matcher IR and differential fixture corpus before wiring the tool into the model surface."},
        {"risk": "206 grammar blobs increase binary size", "impact": "six-platform archives grow", "mitigation": "start with a curated 12--17 grammar set; opt-in build tags for the rest."},
        {"risk": "arbitrary file scan can escape workspace or be abusive", "impact": "data exposure / resource exhaustion", "mitigation": "workspace-rooted path checks, symlink resolution, per-file cap, cancellation and result cap."},
        {"risk": "output format drift from oh-my-pi", "impact": "model-facing behavior differs", "mitigation": "freeze golden text before enabling by default; use the display mode explicitly."},
    ],
    "open_questions": [
        "Should the first release expose hashline or line-number rendering by default?",
        "Is a 12-grammar default set acceptable, or must all 17 omp-ast linked grammars ship in the first cut?",
        "Should internal URLs and fetched web URLs be out of scope for v1?",
        "Do we want a read-only tool only, or a later rewrite/edit phase?",
        "Should the core astgrep package also reject relative paths, or only the model-facing adapter?",
    ],
    "implementation_plan": [
        {"phase": "P0", "work": "freeze requirements/contract and add dependency spike; prove gotreesitter parses the seven-language matrix with CGO_ENABLED=0."},
        {"phase": "P1", "work": "implement path/language resolution, parser pool and structural matcher with golden tests."},
        {"phase": "P2", "work": "implement result renderer, safety limits, cancellation and parse-error reporting."},
        {"phase": "P3", "work": "differential corpus vs ast-grep CLI, six-platform build, binary-size audit, opt-in config gate."},
        {"phase": "P4", "work": "promote from design to shipped only after A1-A11 pass on the release code point."},
    ],
}


def render_html(data: dict) -> str:
    def list_items(items):
        return "".join(f"<li>{ESC(item)}</li>" for item in items)

    req_rows = "".join(
        "<tr>"
        f"<td><code>{ESC(r['id'])}</code></td>"
        f"<td>{ESC(r['priority'])}</td>"
        f"<td>{ESC(r['statement'])}</td>"
        f"<td>{ESC(r['acceptance'])}</td>"
        f"<td>{ESC(', '.join(r['traces']))}</td>"
        "</tr>"
        for r in data["requirements"]
    )
    option_rows = "".join(
        "<tr>"
        f"<td><code>{ESC(o['id'])}</code></td>"
        f"<td>{ESC(o['name'])}</td>"
        f"<td><span class=\"pill {'ok' if o['verdict'] == 'chosen' else 'warn' if o['verdict'] == 'fallback' else 'muted'}\">{ESC(o['verdict'])}</span></td>"
        f"<td>{ESC(o['reason'])}</td>"
        "</tr>"
        for o in data["options"]
    )
    design_cards = "".join(
        f"<div class=\"card\"><div class=\"k\">{ESC(key)}</div><h3>{ESC(value['title'])}</h3><p>{ESC(value['text'])}</p></div>"
        for key, value in data["design"].items()
    )
    acc_rows = "".join(
        "<tr>"
        f"<td><code>{ESC(a['id'])}</code></td>"
        f"<td>{ESC(a['test'])}</td>"
        f"<td>{ESC(a['pass'])}</td>"
        "</tr>"
        for a in data["acceptance"]
    )
    risk_rows = "".join(
        "<tr>"
        f"<td>{ESC(r['risk'])}</td>"
        f"<td>{ESC(r['impact'])}</td>"
        f"<td>{ESC(r['mitigation'])}</td>"
        "</tr>"
        for r in data["risks"]
    )
    source_items = "".join(
        f"<li><a href=\"{ESC(s['url'])}\">{ESC(s['label'])}</a> — {ESC(s['use'])}</li>"
        for s in data["source_refs"]
    )
    plan_items = "".join(
        f"<li><b>{ESC(x['phase'])}</b> — {ESC(x['work'])}</li>" for x in data["implementation_plan"]
    )
    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>ast_grep · gotreesitter 需求—设计链</title>
<style>
:root{{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}}
*{{box-sizing:border-box}} body{{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}}
a{{color:var(--accent2);text-decoration:none}} a:hover{{text-decoration:underline}}
.wrap{{max-width:1320px;margin:0 auto;padding:44px 26px 80px}}
header{{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}}
.eyebrow{{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}}
h1{{font-size:30px;line-height:1.25;margin:10px 0 10px;letter-spacing:-.02em}} h1 span{{color:var(--accent)}}
.lede{{color:var(--muted);max-width:1060px;font-size:15px}} .meta{{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px}} .meta b{{color:var(--text)}}
section{{margin:38px 0}} h2{{font-size:22px;margin:0 0 8px}} h2 .num{{color:var(--accent);font-size:14px;margin-right:8px}} h3{{font-size:15.5px;margin:16px 0 8px;color:#cfdae6}}
p{{color:#c6d2df;margin:8px 0}} .sub{{color:var(--muted);font-size:12.5px}}
.grid{{display:grid;gap:12px}} .g2{{grid-template-columns:repeat(2,minmax(0,1fr))}} .g3{{grid-template-columns:repeat(3,minmax(0,1fr))}} .grid>*{{min-width:0}}
@media(max-width:1000px){{.g3{{grid-template-columns:repeat(2,minmax(0,1fr))}}}} @media(max-width:560px){{.g3,.g2{{grid-template-columns:1fr}}.wrap{{padding:30px 16px 60px}}h1{{font-size:24px}}}}
.card{{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px;min-width:0}} .card .k{{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700}} .card .v{{font-size:20px;font-weight:800;margin-top:5px}} .card .d{{color:var(--muted);font-size:12px;margin-top:5px}}
.tablewrap{{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2);max-width:100%}}
table{{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed}} th,td{{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word}} th{{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1}} tr:hover td{{background:#121a25}} tr:last-child td{{border-bottom:0}}
code{{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}}
pre{{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2}}
.pill{{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal;margin:1px 2px 1px 0}} .pill.ok{{color:var(--ok);border-color:#1d3b26;background:#0f1a13}} .pill.warn{{color:var(--warn);border-color:#3d3013;background:#1a150a}} .pill.muted{{color:var(--muted);border-color:var(--line);background:#0f1620}}
.callout{{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4}} .callout.info{{border-color:var(--accent2);background:#0d1622;color:#cfe3ff}} .callout.bad{{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7}}
ul,ol{{margin:7px 0 7px 20px;padding:0}} li{{margin:4px 0;color:#c6d2df}} footer{{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}}
@media(max-width:560px){{table{{font-size:12px}}th,td{{padding:7px 6px}}}}
</style></head><body><div class="wrap">
<header><div class="eyebrow">GO_HIVE · AST_GREP · REQUIREMENTS → DESIGN</div>
<h1>ast_grep <span>需求—设计链</span></h1>
<p class="lede">用纯 Go 的 <code>gotreesitter</code> 为 go_hive 增加模型可调用的结构搜索工具。这个页面先冻结需求、决策、工具契约、匹配器和验收链，不把设计稿当成已实现能力。</p>
<div class="meta"><span>状态：<b>{ESC(data['status'])}</b></span><span>决策：<b>{ESC(data['decision'])}</b></span><span>目标：<b>CGO_ENABLED=0</b></span></div></header>

<section id="problem"><h2><span class="num">00</span>问题与价值</h2>
<div class="grid g2"><div class="card"><div class="k">问题</div><div class="d">{ESC(data['problem']['statement'])}</div></div><div class="card"><div class="k">为什么不是当前 parity 项</div><div class="d">{ESC(data['problem']['why_not_now'])}</div></div></div>
<h3>用户价值</h3><ul>{list_items(data['problem']['user_value'])}</ul></section>

<section id="requirements"><h2><span class="num">01</span>需求链</h2>
<p class="sub">R → D → A 是同一张链：每个需求必须落到一个设计项和至少一个验收项。</p>
<div class="tablewrap"><table><thead><tr><th style="width:7%">ID</th><th style="width:7%">优先级</th><th style="width:36%">需求</th><th style="width:36%">验收</th><th style="width:14%">链路</th></tr></thead><tbody>{req_rows}</tbody></table></div>
<h3>非目标</h3><ul>{list_items(data['non_goals'])}</ul></section>

<section id="options"><h2><span class="num">02</span>决策链</h2>
<div class="tablewrap"><table><thead><tr><th style="width:7%">ID</th><th style="width:28%">方案</th><th style="width:10%">结论</th><th style="width:55%">理由</th></tr></thead><tbody>{option_rows}</tbody></table></div>
<div class="callout info"><b>选择：</b>{ESC(data['decision'])} <b>备选：</b>如果纯 Go matcher 无法通过 A4/A10，退回 Rust sidecar，而不是改 cgo。</div></section>

<section id="design"><h2><span class="num">03</span>设计链</h2><div class="grid g2">{design_cards}</div>
<div class="callout info"><b>Package 边界：</b><code>{ESC(data['package_layout']['package'])}</code> 是独立可测的核心包；<code>{ESC(data['package_layout']['adapter'])}</code> 只做工具 schema / 注册适配。{ESC(data['package_layout']['rule'])} <b>维护：</b>{ESC(data['package_layout']['maintenance'])}</div>
<h3>处理流水线</h3><ol>{list_items(data['pipeline'])}</ol></section>

<section id="contract"><h2><span class="num">04</span>工具契约</h2>
<h3>输入 schema</h3><pre>{ESC(json.dumps(data['tool_contract']['input_schema'], ensure_ascii=False, indent=2))}</pre>
<h3>输出</h3><p>{ESC(data['tool_contract']['result_fields']['text'])}</p><p class="sub">details: {ESC(', '.join(data['tool_contract']['result_fields']['details']))}</p></section>

<section id="acceptance"><h2><span class="num">05</span>验收链</h2>
<div class="tablewrap"><table><thead><tr><th style="width:8%">ID</th><th style="width:28%">测试</th><th style="width:64%">通过条件</th></tr></thead><tbody>{acc_rows}</tbody></table></div></section>

<section id="plan"><h2><span class="num">06</span>实施阶段</h2><ol>{plan_items}</ol>
<h3>开放问题</h3><ul>{list_items(data['open_questions'])}</ul></section>

<section id="risks"><h2><span class="num">07</span>风险与边界</h2>
<div class="tablewrap"><table><thead><tr><th style="width:30%">风险</th><th style="width:28%">影响</th><th style="width:42%">缓解</th></tr></thead><tbody>{risk_rows}</tbody></table></div>
<h3>来源</h3><ul>{source_items}</ul></section>

<footer>ast_grep design · 需求—设计—验收链 · 由 <code>go_hive/scripts/gen_ast_grep_design.py</code> 生成；<code>--check</code> 防漂移 · 设计状态不冒充实现状态</footer>
</div></body></html>
"""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()

    json_text = json.dumps(DATA, ensure_ascii=False, indent=2) + "\n"
    html_text = render_html(DATA)
    if args.check:
        failures = []
        for path, expected in ((OUT_JSON, json_text), (OUT_HTML, html_text)):
            if not path.exists() or path.read_text(encoding="utf-8") != expected:
                failures.append(str(path))
        if failures:
            print("ast-grep-design: DRIFT: " + ", ".join(failures))
            return 1
        print("ast-grep-design: OK")
        return 0
    OUT_JSON.write_text(json_text, encoding="utf-8")
    OUT_HTML.write_text(html_text, encoding="utf-8")
    print(f"ast-grep-design: wrote {OUT_HTML} and {OUT_JSON}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
