#!/usr/bin/env python3
"""QC 结果检验：对交付物做独立检验并给出 PASS/FAIL。

用法：qc_check.py [--full] [--smoke]
  --full   额外跑 make ci（约 2-3 分钟）
  --smoke  额外跑真实二进制冒烟（约 20 秒）
"""
import argparse
import re
import subprocess
from pathlib import Path

ROOT = Path("/Users/mac/hive")
MOD = ROOT / "go_hive/go-hive"
OUT = ROOT / "go_hive/QC_REPORT.md"


def run(cmd, cwd=MOD, timeout=600):
    p = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=timeout, shell=isinstance(cmd, str))
    return p.returncode, (p.stdout + p.stderr)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--full", action="store_true")
    ap.add_argument("--smoke", action="store_true")
    args = ap.parse_args()

    checks = []

    # C2 parity 引用可解析（同时校验“全注解”与“引用存在”两件事）
    code, out = run(["bash", "../scripts/check_parity.sh"])
    m = re.search(r"(\d+)\s*/\s*(\d+)", out)
    all_annotated = code == 0 and bool(m) and m.group(1) == m.group(2)
    code2, out2 = run(["bash", "../scripts/check_parity_targets.sh"])
    missing = re.search(r"missing files:\s*(\d+).*missing tests:\s*(\d+)", out2)
    refs_ok = code2 == 0 and (not missing or (missing.group(1) == "0" and missing.group(2) == "0"))
    checks.append(("C2 parity 引用", all_annotated and refs_ok,
                   f"注解 {m.group(0) if m else '?'}；refs {'0 missing' if refs_ok else '有失效'}"))

    # C4 制品（区分 embed 二进制与磁盘依赖）
    dist = MOD / "dist"
    bins = sorted(p.name for p in dist.glob("hive*")) if dist.exists() else []
    platform_ok = sum(1 for b in bins if any(k in b for k in ("darwin", "linux", "windows"))) >= 6
    embed_bin = dist / "hive-embed"
    embed_ok = embed_bin.exists()
    has_disk_dist = (MOD / "frontend/dist/index.html").exists()
    code, out = run(["./dist/hive", "--version"])
    checks.append(("C4 六平台产物", platform_ok, f"{len(bins)} 个产物；version={out.strip()}"))
    checks.append(("C4b UI 可分发性", embed_ok and has_disk_dist,
                   f"hive-embed={'有' if embed_ok else '无'}；磁盘 dist={'有' if has_disk_dist else '无'}"))

    # C5 cgo
    code, out = run(["bash", "-c", "grep -rl 'import \"C\"' --include='*.go' . | wc -l"])
    cgo = int(out.strip() or 0)
    checks.append(("C5 无 cgo", cgo == 0, f"import \"C\" 文件 {cgo} 个"))

    # C6 文档口径一致
    docs = ["README.md", "DELIVERY.md", "COORDINATION.md", "11-product-requirements.md", "INTEGRATION_BACKLOG.md"]
    stale = []
    for d in docs:
        p = ROOT / "go_hive" / d
        if p.exists() and "811 action" in p.read_text():
            stale.append(d)
    checks.append(("C6 文档口径", not stale, f"残留 811 口径：{stale or '无'}"))

    # C7 回归趋势（对照检查点台账，而非同批进程写的采样）
    tests = int(run(["bash", "-c", "grep -rhoE '^func Test[A-Za-z0-9_]+' --include='*_test.go' . | wc -l"])[1].strip() or 0)
    ledger = ROOT / "go_hive/ledger.csv"
    peak = 0
    if ledger.exists():
        for line in ledger.read_text().splitlines()[1:]:
            parts = line.split(",")
            if len(parts) > 3 and parts[3].isdigit():
                peak = max(peak, int(parts[3]))
    checks.append(("C7 测试回归", tests >= peak, f"当前 {tests}，台账峰值 {peak}"))

    if args.full:
        code, out = run(["make", "ci"], timeout=900)
        checks.append(("C1 make ci", code == 0, (out.strip().splitlines() or [""])[-1][:120]))

    if args.smoke:
        code, out = run(["bash", "../scripts/smoke.sh", "8801"], timeout=300)
        checks.append(("C3 运行时冒烟", code == 0, (out.strip().splitlines() or [""])[-2][:120]))

    ok = sum(1 for _, v, _ in checks if v)
    lines = ["# QC 结果检验", "", f"| 检验 | 结果 | 证据 |", "| --- | --- | --- |"]
    for name, good, ev in checks:
        lines.append(f"| {name} | {'PASS' if good else 'FAIL'} | {ev} |")
    lines += ["", f"**{ok}/{len(checks)} PASS**", ""]
    if ok != len(checks):
        lines.append("失败项必须阻断合并/发布，修复后重跑。")
    OUT.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    return 0 if ok == len(checks) else 1


if __name__ == "__main__":
    raise SystemExit(main())
