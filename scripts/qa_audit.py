#!/usr/bin/env python3
"""QA 过程审计：检查各 lane 分支是否遵守 SOP。只读。"""
import re
import subprocess
from pathlib import Path

ROOT = Path("/Users/mac/hive")
OUT = ROOT / "go_hive/QA_REPORT.md"
SHARED = ("internal/server/app.go", "go.mod", "Makefile", "internal/tools/registry.go")
PARITY = re.compile(r"//\s*Parity(-exempt)?:")


def git(*a):
    return subprocess.run(["git", "-C", str(ROOT), *a], capture_output=True, text=True).stdout


branches = [b.strip() for b in git("for-each-ref", "--format=%(refname:short)", "refs/heads").splitlines()
            if b.strip().startswith(("lane/", "wave", "w4/", "fix/"))]

rows = []
for b in branches:
    commits = [l for l in git("log", "--format=%H", f"main..{b}").splitlines() if l]
    changed = [l for l in git("diff", "--name-only", f"main...{b}").splitlines() if l]
    tests = [l for l in git("diff", "-U0", f"main...{b}").splitlines()
             if l.startswith("+func Test")]
    # 新增测试前 3 行是否带 parity 注解（近似）
    diff = git("diff", f"main...{b}")
    added = [l[1:] for l in diff.splitlines() if l.startswith("+//")]
    annotated = len([l for l in added if PARITY.search(l)])
    shared_hits = [f for f in changed if any(f.endswith(s) for s in SHARED)]
    rows.append((b, len(commits), len(changed), len(tests), annotated, shared_hits))

lines = ["# QA 过程审计", "", "| 分支 | 提交 | 变更文件 | 新增测试 | parity 注解行 | 触碰共享文件 | 结论 |",
         "| --- | ---: | ---: | ---: | ---: | --- | --- |"]
violations = 0
for b, c, f, t, a, shared in rows:
    verdict = "OK"
    if t and a < t:
        verdict = "WARN：测试数 > 注解行"
    if shared:
        verdict = "VIOLATION：改了共享文件"
        violations += 1
    lines.append(f"| `{b}` | {c} | {f} | {t} | {a} | {', '.join(shared) or '-'} | {verdict} |")

lines += ["", "## 结论", "",
          f"- 分支 {len(rows)} 个；共享文件违规 {violations} 个。",
          "- 共享文件（app.go / go.mod / Makefile / registry.go）只允许 integrator 分支改；其余分支出现即视为越界。",
          "- 「parity 注解行 < 新增测试数」只作为线索，最终以 `make ci` 的 parity 门禁为准（它要求逐测试注解）。"]
OUT.write_text("\n".join(lines) + "\n")
print("\n".join(lines[:6]))
print("...")
print("\n".join(lines[-4:]))
print("written:", OUT)
