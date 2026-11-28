#!/usr/bin/env python3
"""Generate a per-package, AST-derived function alignment matrix.

Inputs:
  - Python reference: core/framework/**/*.py
  - Go port: go_hive/go-hive/internal/**/*.go
  - Package mapping: go_hive/docs/package-parity.json
  - Go function inventory: a temporary stdlib-only go/parser helper

The matrix is deliberately evidence-first:
  * exact `// Parity: ...::symbol` annotations are annotation matches;
  * a normalized-name match is reported separately, never as annotation proof;
  * unmatched Python and Go functions remain visible.

Outputs:
  go_hive/docs/function-parity-matrix.json
  go_hive/docs/function-parity-matrix.html

Use --check to fail when either generated file is stale.
"""

from __future__ import annotations

import argparse
import ast
import html
import json
import os
import re
import subprocess
import sys
import tempfile
from collections import defaultdict
from pathlib import Path
from typing import Any

SCRIPT = Path(__file__).resolve()
ROOT = SCRIPT.parents[2]
FRAMEWORK = ROOT / "core" / "framework"
GO_ROOT = ROOT / "go_hive" / "go-hive"
GO_INTERNAL = GO_ROOT / "internal"
PACKAGE_PARITY = ROOT / "go_hive" / "docs" / "package-parity.json"
OUT_JSON = ROOT / "go_hive" / "docs" / "function-parity-matrix.json"
OUT_HTML = ROOT / "go_hive" / "docs" / "function-parity-matrix.html"


GO_HELPER = r'''package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Function struct {
	Pkg     string   `json:"pkg"`
	File    string   `json:"file"`
	Name    string   `json:"name"`
	Recv    string   `json:"recv,omitempty"`
	Qual    string   `json:"qual"`
	Line    int      `json:"line"`
	Doc     []string `json:"doc,omitempty"`
	Exported bool    `json:"exported"`
}

func receiverName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return receiverName(x.X)
	case *ast.IndexExpr:
		return receiverName(x.X)
	case *ast.IndexListExpr:
		return receiverName(x.X)
	case *ast.ParenExpr:
		return receiverName(x.X)
	case *ast.SelectorExpr:
		return x.Sel.Name
	default:
		return ""
	}
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ast-func-dump <internal-root>")
		os.Exit(2)
	}
	root := os.Args[1]
	fset := token.NewFileSet()
	var out []Function
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		relDir, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(relDir)
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil {
				continue
			}
			recv := ""
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				recv = receiverName(fd.Recv.List[0].Type)
				if fd.Recv.List[0].Type != nil {
					var b strings.Builder
					if err := format.Node(&b, fset, fd.Recv.List[0].Type); err == nil {
						recv = receiverName(fd.Recv.List[0].Type)
						_ = b
					}
				}
			}
			qual := fd.Name.Name
			if recv != "" {
				qual = recv + "." + qual
			}
			var doc []string
			if fd.Doc != nil {
				for _, c := range fd.Doc.List {
					text := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), "/*"))
					text = strings.TrimSpace(strings.TrimSuffix(text, "*/"))
					doc = append(doc, text)
				}
			}
			out = append(out, Function{
				Pkg: pkg,
				File: "go_hive/go-hive/internal/" + pkg + "/" + filepath.Base(path),
				Name: fd.Name.Name,
				Recv: recv,
				Qual: qual,
				Line: fset.Position(fd.Pos()).Line,
				Doc: doc,
				Exported: fd.Name.IsExported(),
			})
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pkg != out[j].Pkg { return out[i].Pkg < out[j].Pkg }
		if out[i].File != out[j].File { return out[i].File < out[j].File }
		if out[i].Line != out[j].Line { return out[i].Line < out[j].Line }
		return out[i].Qual < out[j].Qual
	})
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
'''


def norm_name(value: str) -> str:
    return re.sub(r"[^a-z0-9]", "", value.lower())


def norm_py_file(value: str) -> str:
    value = value.strip().rstrip(".,;")
    value = value.replace("\\", "/")
    if value.startswith("core/"):
        value = value[5:]
    if value.startswith("framework/"):
        value = "core/" + value
    return value


def py_package_for(rel: str) -> str:
    parts = rel.split("/")
    if len(parts) <= 3:
        return "(root)"
    return parts[2]


def collect_python() -> tuple[list[dict[str, Any]], list[str]]:
    funcs: list[dict[str, Any]] = []
    parse_errors: list[str] = []
    for path in sorted(FRAMEWORK.rglob("*.py")):
        rel = path.relative_to(ROOT).as_posix()
        parts = path.relative_to(FRAMEWORK).parts
        if not parts or "tests" in parts:
            continue
        if path.name.startswith("test_") or path.name.endswith("_test.py"):
            continue
        try:
            tree = ast.parse(path.read_text(encoding="utf-8", errors="replace"), filename=str(path))
        except SyntaxError as exc:
            parse_errors.append(f"{rel}:{exc.lineno}: {exc.msg}")
            continue
        pkg = py_package_for(rel)

        def visit(node: ast.AST, classes: tuple[str, ...]) -> None:
            if isinstance(node, ast.ClassDef):
                for child in node.body:
                    visit(child, classes + (node.name,))
                return
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                qual = ".".join(classes + (node.name,))
                funcs.append({
                    "package": pkg,
                    "file": rel,
                    "module": rel,
                    "qualname": qual,
                    "name": node.name,
                    "kind": "method" if classes else "function",
                    "line": node.lineno,
                    "end_line": getattr(node, "end_lineno", node.lineno),
                    "async": isinstance(node, ast.AsyncFunctionDef),
                })
                # Nested functions are not source-package API surface.
                return
            for child in ast.iter_child_nodes(node):
                visit(child, classes)

        for child in tree.body:
            visit(child, ())
    return funcs, parse_errors


def go_helper() -> list[dict[str, Any]]:
    with tempfile.TemporaryDirectory(prefix="hive-ast-funcs-") as td:
        td_path = Path(td)
        (td_path / "go.mod").write_text("module astfuncs\n\ngo 1.26\n", encoding="utf-8")
        (td_path / "main.go").write_text(GO_HELPER, encoding="utf-8")
        env = os.environ.copy()
        env.update({"CGO_ENABLED": "0", "GOPROXY": "off", "GOSUMDB": "off"})
        proc = subprocess.run(
            ["go", "run", ".", str(GO_INTERNAL)],
            cwd=td_path,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        if proc.returncode != 0:
            raise RuntimeError(f"Go AST helper failed ({proc.returncode}): {proc.stderr.strip()}")
        return json.loads(proc.stdout)


def load_pairs() -> list[dict[str, Any]]:
    data = json.loads(PACKAGE_PARITY.read_text(encoding="utf-8"))
    return data["pairs"]


def package_of_go(go_pkg: str) -> str:
    return go_pkg.split("/", 1)[0]


def parity_targets(go_func: dict[str, Any]) -> list[dict[str, str]]:
    targets: list[dict[str, str]] = []
    for line in go_func.get("doc", []):
        m = re.search(r"\bParity:\s*(\S+)", line)
        if not m:
            continue
        raw = m.group(1).rstrip(".,;")
        if "::" not in raw:
            continue
        file_part, symbol = raw.split("::", 1)
        file_part = norm_py_file(file_part)
        symbol = symbol.rstrip(".,;")
        if not file_part.endswith(".py"):
            continue
        targets.append({"file": file_part, "symbol": symbol})
    return targets


def build_matrix() -> dict[str, Any]:
    py_funcs, py_errors = collect_python()
    go_funcs = go_helper()
    pairs = load_pairs()

    py_by_key: dict[tuple[str, str], dict[str, Any]] = {}
    py_by_pkg: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for f in py_funcs:
        f["_key"] = ("%s::%s" % (f["file"], f["qualname"]))
        f["_norm"] = norm_name(f["name"])
        py_by_key[(f["file"], f["qualname"])] = f
        py_by_pkg[f["package"]].append(f)

    go_pkg_to_pair: dict[str, str] = {}
    pair_go_names: dict[str, list[str]] = {}
    for pair in pairs:
        pyname = pair["py"]
        names = [g["name"] for g in pair.get("go", [])]
        pair_go_names[pyname] = names
        for name in names:
            go_pkg_to_pair[name] = pyname

    go_by_pair: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for f in go_funcs:
        top = package_of_go(f["pkg"])
        pair = go_pkg_to_pair.get(top)
        if pair is None:
            continue
        f["_pair"] = pair
        f["_norm"] = norm_name(f["name"])
        f["_targets"] = parity_targets(f)
        go_by_pair[pair].append(f)

    # Exact annotation matches, plus a clearly-labelled normalized-name fallback.
    py_to_go: dict[int, list[dict[str, Any]]] = defaultdict(list)
    go_to_py: dict[int, list[dict[str, Any]]] = defaultdict(list)
    for f in go_funcs:
        if "_pair" not in f:
            continue
        for target in f["_targets"]:
            key = (target["file"], target["symbol"])
            pyf = py_by_key.get(key)
            if pyf is not None and pyf["package"] == f["_pair"]:
                py_to_go[id(pyf)].append({"kind": "annotation", "go": f})
                go_to_py[id(f)].append({"kind": "annotation", "py": pyf})

    for pair in pairs:
        pyname = pair["py"]
        pyr = py_by_pkg.get(pyname, [])
        gor = go_by_pair.get(pyname, [])
        go_norm: dict[str, list[dict[str, Any]]] = defaultdict(list)
        for f in gor:
            go_norm[f["_norm"]].append(f)
        for pyf in pyr:
            if py_to_go.get(id(pyf)):
                continue
            for gof in go_norm.get(pyf["_norm"], []):
                py_to_go[id(pyf)].append({"kind": "name", "go": gof})
                go_to_py[id(gof)].append({"kind": "name", "py": pyf})

    def slim_go(f: dict[str, Any]) -> dict[str, Any]:
        return {
            "package": f["_pair"],
            "go_package": f["pkg"],
            "file": f["file"],
            "qualname": f["qual"],
            "line": f["line"],
        }

    def slim_py(f: dict[str, Any]) -> dict[str, Any]:
        return {
            "package": f["package"],
            "file": f["file"],
            "qualname": f["qualname"],
            "line": f["line"],
        }

    packages: list[dict[str, Any]] = []
    for pair in pairs:
        pyname = pair["py"]
        pyr = py_by_pkg.get(pyname, [])
        gor = go_by_pair.get(pyname, [])
        py_matches = {id(f): py_to_go.get(id(f), []) for f in pyr}
        go_matches = {id(f): go_to_py.get(id(f), []) for f in gor}
        py_ann = sum(1 for f in pyr if any(m["kind"] == "annotation" for m in py_matches[id(f)]))
        py_name = sum(1 for f in pyr if any(m["kind"] == "name" for m in py_matches[id(f)]))
        go_ann = sum(1 for f in gor if any(m["kind"] == "annotation" for m in go_matches[id(f)]))
        go_name = sum(1 for f in gor if any(m["kind"] == "name" for m in go_matches[id(f)]))
        py_matched = sum(1 for f in pyr if py_matches[id(f)])
        go_matched = sum(1 for f in gor if go_matches[id(f)])
        py_only = [f["qualname"] for f in pyr if not py_matches[id(f)]]
        go_only = [f["qual"] for f in gor if not go_matches[id(f)]]
        packages.append({
            "python_package": pyname,
            "verdict": pair.get("verdict"),
            "go_packages": pair.get("go", []),
            "python_functions": len(pyr),
            "go_functions": len(gor),
            "python_matched": py_matched,
            "go_matched": go_matched,
            "python_annotation_matches": py_ann,
            "python_name_matches": py_name,
            "go_annotation_matches": go_ann,
            "go_name_matches": go_name,
            "python_only": len(py_only),
            "go_only": len(go_only),
            "python_coverage_pct": round(100.0 * py_matched / len(pyr), 1) if pyr else None,
            "go_coverage_pct": round(100.0 * go_matched / len(gor), 1) if gor else None,
            "python_only_sample": sorted(py_only)[:12],
            "go_only_sample": sorted(go_only)[:12],
        })

    function_rows: list[dict[str, Any]] = []
    for f in py_funcs:
        matches = py_to_go.get(id(f), [])
        kinds = sorted({m["kind"] for m in matches})
        function_rows.append({
            "side": "python",
            "package": f["package"],
            "file": f["file"],
            "qualname": f["qualname"],
            "line": f["line"],
            "kind": f["kind"],
            "match": "annotation" if "annotation" in kinds else "name" if "name" in kinds else None,
            "matches": [slim_go(m["go"]) for m in matches],
        })
    for f in go_funcs:
        if "_pair" not in f:
            continue
        matches = go_to_py.get(id(f), [])
        kinds = sorted({m["kind"] for m in matches})
        function_rows.append({
            "side": "go",
            "package": f["_pair"],
            "go_package": f["pkg"],
            "file": f["file"],
            "qualname": f["qual"],
            "line": f["line"],
            "kind": "method" if f.get("recv") else "function",
            "match": "annotation" if "annotation" in kinds else "name" if "name" in kinds else None,
            "matches": [slim_py(m["py"]) for m in matches],
            "parity_targets": f.get("_targets", []),
        })

    function_rows.sort(key=lambda x: (x["package"], x["side"], x["file"], x["line"], x["qualname"]))
    packages.sort(key=lambda x: x["python_package"])
    total_py = len(py_funcs)
    total_go = sum(len(v) for v in go_by_pair.values())
    matched_py = sum(1 for f in py_funcs if py_to_go.get(id(f)))
    matched_go = sum(1 for f in go_funcs if "_pair" in f and go_to_py.get(id(f)))
    return {
        "schema_version": 1,
        "scope": {
            "python": "core/framework/**/*.py (non-test)",
            "go": "go_hive/go-hive/internal/**/*.go (non-test)",
            "package_mapping": "go_hive/docs/package-parity.json",
            "match_method": "Go AST doc comments (`// Parity: ...::symbol`) then normalized function-name fallback",
        },
        "totals": {
            "python_functions": total_py,
            "go_functions": total_go,
            "python_matched": matched_py,
            "go_matched": matched_go,
            "python_only": total_py - matched_py,
            "go_only": total_go - matched_go,
            "python_coverage_pct": round(100.0 * matched_py / total_py, 1) if total_py else None,
            "go_coverage_pct": round(100.0 * matched_go / total_go, 1) if total_go else None,
            "python_parse_errors": len(py_errors),
        },
        "python_parse_errors": py_errors,
        "packages": packages,
        "functions": function_rows,
    }


def render_html(matrix: dict[str, Any]) -> str:
    t = matrix["totals"]
    rows = []
    for p in matrix["packages"]:
        py_only = "<br>".join(html.escape(x) for x in p["python_only_sample"]) or "—"
        go_only = "<br>".join(html.escape(x) for x in p["go_only_sample"]) or "—"
        cov = "—" if p["python_coverage_pct"] is None else f'{p["python_coverage_pct"]:.1f}%'
        rows.append(
            "<tr>"
            f"<td><code>{html.escape(p['python_package'])}</code><div class=\"sub\">{html.escape(str(p['verdict']))}</div></td>"
            f"<td>{', '.join('<code>%s</code>' % html.escape(g['name']) for g in p['go_packages']) or '—'}</td>"
            f"<td class=\"num\">{p['python_functions']}</td><td class=\"num\">{p['go_functions']}</td>"
            f"<td class=\"num\">{p['python_annotation_matches']}</td><td class=\"num\">{p['python_name_matches']}</td>"
            f"<td class=\"num\">{p['python_matched']}</td><td class=\"num\">{cov}</td>"
            f"<td class=\"num\">{p['python_only']}</td><td class=\"num\">{p['go_only']}</td>"
            f"<td class=\"small\">{py_only}</td><td class=\"small\">{go_only}</td>"
            "</tr>"
        )
    return f"""<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>go_hive ↔ hive 函数级 AST 对齐矩阵</title>
<style>
:root{{--bg:#0b1020;--panel:#121a2e;--ink:#e7edf8;--muted:#94a3b8;--line:#263450;--accent:#67e8f9;--warn:#fbbf24;--bad:#fb7185;--ok:#4ade80}}
*{{box-sizing:border-box}}body{{margin:0;background:var(--bg);color:var(--ink);font:14px/1.55 ui-sans-serif,system-ui,-apple-system,Segoe UI,sans-serif}}
main{{max-width:1500px;margin:auto;padding:28px 22px 60px}}h1{{font-size:26px;margin:0 0 6px}}h2{{font-size:18px;margin:28px 0 10px;border-bottom:1px solid var(--line);padding-bottom:6px}}
p,li{{color:var(--muted)}}code{{color:#c4b5fd}}a{{color:var(--accent)}}.sub{{color:var(--muted);font-size:12px}}
.cards{{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px;margin:18px 0}}.card{{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:14px}}.card b{{display:block;font-size:25px;color:var(--accent)}}.card span{{color:var(--muted);font-size:12px}}
.tablewrap{{overflow:auto;border:1px solid var(--line);border-radius:10px;background:var(--panel)}}table{{border-collapse:collapse;width:100%;min-width:1200px}}th,td{{border-bottom:1px solid var(--line);padding:9px 10px;text-align:left;vertical-align:top}}th{{position:sticky;top:0;background:#16213a;color:#dbeafe;font-size:12px;white-space:nowrap}}.num{{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}}.small{{font-size:11px;color:var(--muted);max-width:250px}}.good{{color:var(--ok)}}.warn{{color:var(--warn)}}.bad{{color:var(--bad)}}
.note{{background:#15203a;border-left:3px solid var(--warn);padding:12px 14px;border-radius:6px;margin:14px 0}}
</style></head><body><main>
<header><h1>go_hive ↔ hive 函数级 AST 对齐矩阵</h1>
<p>按源码包逐项解析 Python <code>ast</code> 与 Go <code>go/parser</code>，仅比较非测试函数/方法。高置信匹配来自 Go 源码中的 <code>// Parity: module.py::symbol</code> 注释；归一化函数名匹配单独列为 name，不冒充 annotation 证据。</p></header>
<div class="cards">
<div class="card"><b>{t['python_functions']}</b><span>Python functions / methods</span></div>
<div class="card"><b>{t['go_functions']}</b><span>Go functions / methods</span></div>
<div class="card"><b>{t['python_matched']}</b><span>Python matched (annotation or name)</span></div>
<div class="card"><b>{t['go_matched']}</b><span>Go matched (annotation or name)</span></div>
<div class="card"><b>{t['python_coverage_pct']:.1f}%</b><span>Python function coverage</span></div>
<div class="card"><b>{t['go_coverage_pct']:.1f}%</b><span>Go function coverage</span></div>
</div>
<div class="note"><b>口径：</b>“coverage”只表示 AST 能配对到同名或带 <code>// Parity:</code> 注释的函数，不等于行为/语义已验证。未匹配项既可能是命名差异，也可能是真实缺口；逐条函数证据见 <a href="function-parity-matrix.json">function-parity-matrix.json</a>。</div>
<h2>逐包矩阵</h2>
<div class="tablewrap"><table><thead><tr><th>Python 包</th><th>Go 包</th><th>Py 函数</th><th>Go 函数</th><th>Py 注释匹配</th><th>Py 名称匹配</th><th>Py 已匹配</th><th>Py 覆盖率</th><th>Py-only</th><th>Go-only</th><th>Python-only 样例</th><th>Go-only 样例</th></tr></thead><tbody>{''.join(rows)}</tbody></table></div>
<h2>复现</h2>
<p><code>uv run --no-project python3 go_hive/scripts/gen_function_parity.py</code> · 漂移检查：<code>uv run --no-project python3 go_hive/scripts/gen_function_parity.py --check</code></p>
<p class="sub">生成器只使用 Python stdlib + Go stdlib；函数清单来自 AST，不依赖正则猜测。</p>
</main></body></html>
"""


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--check", action="store_true", help="fail if generated artifacts are stale")
    args = ap.parse_args()
    try:
        matrix = build_matrix()
    except Exception as exc:
        print(f"function-parity: FAIL: {exc}", file=sys.stderr)
        return 1
    payload = json.dumps(matrix, ensure_ascii=False, indent=2, sort_keys=False) + "\n"
    page = render_html(matrix)
    if args.check:
        bad = []
        if not OUT_JSON.exists() or OUT_JSON.read_text(encoding="utf-8") != payload:
            bad.append(OUT_JSON)
        if not OUT_HTML.exists() or OUT_HTML.read_text(encoding="utf-8") != page:
            bad.append(OUT_HTML)
        if bad:
            print("function-parity: DRIFT: " + ", ".join(str(p) for p in bad), file=sys.stderr)
            return 1
        print(f"function-parity: OK ({matrix['totals']['python_functions']} Python / {matrix['totals']['go_functions']} Go functions)")
        return 0
    OUT_JSON.write_text(payload, encoding="utf-8")
    OUT_HTML.write_text(page, encoding="utf-8")
    t = matrix["totals"]
    print(f"function-parity: wrote {OUT_JSON.relative_to(ROOT)} and {OUT_HTML.relative_to(ROOT)}")
    print(f"  python_functions={t['python_functions']} go_functions={t['go_functions']} python_matched={t['python_matched']} go_matched={t['go_matched']} python_only={t['python_only']} go_only={t['go_only']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
