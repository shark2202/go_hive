#!/usr/bin/env python3
"""QA 过程审计：按合并提交回溯每个 lane 实际带入了什么。只读。

v2 修正：合并后 `main..branch` 恒为空，v1 因此永远报 0（审计已证伪）。
改为逐条 merge commit 用 `^1..sha` 取差集，归属到被合并的分支。
"""
import re
import subprocess
from pathlib import Path

ROOT = Path("/Users/mac/hive")
OUT = ROOT / "go_hive/QA_REPORT.md"
SHARED = ("internal/server/app.go", "go.mod", "Makefile", "internal/tools/registry.go")
INTEGRATOR = ("merge wave3/int", "merge w4/int", "merge fix/integration")


def git(*a):
    return subprocess.run(["git", "-C", str(ROOT), *a], capture_output=True, text=True).stdout


merges = []
for line in git("log", "--merges", "--format=%H|%s", "-80").splitlines():
    sha, _, subject = line.partition("|")
    if sha:
        merges.append((sha, subject))

rows = []
for sha, subject in merges:
    diff = git("diff", f"{sha}^1", sha)
    files = [l for l in git("diff", "--name-only", f"{sha}^1", sha).splitlines() if l]
    tests = [l for l in diff.splitlines() if l.startswith("+func Test")]
    notes = [l for l in diff.splitlines() if re.match(r"\+//\s*Parity", l)]
    shared = [f for f in files if any(f.endswith(s) for s in SHARED)]
    rows.append((subject[:44], len(files), len(tests), len(notes), shared))

lines = ["# QA 过程审计（v2：按合并提交回溯）", "",
         "| 合并 | 文件 | 新增测试 | parity 注解 | 共享文件 | 结论 |",
         "| --- | ---: | ---: | ---: | --- | --- |"]
violations = 0
for subject, files, tests, notes, shared in rows:
    verdict = "OK"
    if shared and not any(k in subject for k in INTEGRATOR):
        verdict = "VIOLATION：非 integrator 改共享文件"
        violations += 1
    elif tests and notes < tests:
        verdict = f"WARN：注解 {notes} < 测试 {tests}（以 make ci 为准）"
    lines.append(f"| {subject} | {files} | {tests} | {notes} | {', '.join(shared) or '-'} | {verdict} |")

lines += ["", "## 结论", "",
          f"- 合并 {len(rows)} 个；共享文件越界 {violations} 个。",
          "- 共享文件只允许 integrator 合并改；其余出现即越界。",
          "- 「注解 < 测试」只是线索；硬门禁是 `make ci` 里的 check_parity（逐测试必须注解）。"]
OUT.write_text("\n".join(lines) + "\n")
print("\n".join(lines[:6]))
print("...")
print("\n".join(lines[-3:]))
print("written:", OUT)
