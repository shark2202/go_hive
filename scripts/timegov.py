#!/usr/bin/env python3
"""独立时间治理大师：对六个维度做约束与调节，并给出裁决。

维度：目标完成时间、执行时间预算、阶段进度、等待时间、决策延迟、停止时点。
只读：不修改代码，不改台账，只写 TIME_REPORT.md。
"""
import csv
import json
import subprocess
from datetime import datetime
from pathlib import Path

ROOT = Path("/Users/mac/hive")
GOV = ROOT / "go_hive"
LEDGER = GOV / "TIME_LEDGER.csv"
POLICY = json.loads((GOV / "TIME_POLICY.json").read_text())
REPORT = GOV / "TIME_REPORT.md"
FMT = "%Y-%m-%d %H:%M"


def git(*args):
    out = subprocess.run(["git", "-C", str(ROOT), *args], capture_output=True, text=True)
    return out.stdout.strip()


def parse(v):
    v = (v or "").strip()
    for fmt in (FMT, "%Y-%m-%d %H:%M:%S"):
        try:
            return datetime.strptime(v, fmt)
        except ValueError:
            pass
    return None


def num(v):
    try:
        return float((v or "").strip())
    except ValueError:
        return 0.0


now = datetime.now()
deadline = parse(POLICY["deadline"])
rows = list(csv.DictReader(LEDGER.open()))
verdicts = []

# 1) 目标完成时间
remaining_h = max((deadline - now).total_seconds() / 3600, 0)
DONE_ACTIONS, TOTAL_ACTIONS, LANE_H_MEASURED = 184, 865, 4.17
rate = DONE_ACTIONS / LANE_H_MEASURED
need_lane_h = (TOTAL_ACTIONS - DONE_ACTIONS) / rate
forecast_h = need_lane_h / 9 * 1.6
forecast_at = datetime.fromtimestamp(now.timestamp() + forecast_h * 3600)
verdicts.append(("目标完成时间", "OK" if forecast_at <= deadline else "BREACH",
                 f"剩余 {remaining_h:.1f}h；预测 {forecast_at:%m-%d %H:%M}（{forecast_h:.1f}h）"))

# 2) 执行预算
budget_lines = []
for r in rows:
    planned, actual = num(r["planned_h"]), num(r["actual_h"])
    if r["status"] == "done" and planned:
        budget_lines.append((r["milestone"], planned, actual, (actual - planned) / planned * 100))
    elif r["status"] == "in_flight" and planned:
        start = parse(r["actual_start"]) or parse(r["planned_start"])
        elapsed = max((now - start).total_seconds() / 3600, 0) if start else 0
        budget_lines.append((r["milestone"], planned, elapsed, (elapsed - planned) / planned * 100))
worst = max(budget_lines, key=lambda x: x[3]) if budget_lines else ("-", 0, 0, 0)
budget_state = "BREACH" if worst[3] > 100 else ("WARN" if worst[3] > 30 else "OK")
verdicts.append(("执行时间预算", budget_state, f"最差：{worst[0][:22]} 超 {worst[3]:+.0f}%"))

# 3) 阶段进度
planned_total = sum(num(r["planned_h"]) for r in rows)
done_weight = sum(num(r["planned_h"]) for r in rows if r["status"] == "done")
progress = done_weight / planned_total if planned_total else 0
starts = [parse(r["planned_start"]) for r in rows if parse(r["planned_start"])]
start = min(starts, default=now)
elapsed_frac = (now - start).total_seconds() / max((deadline - start).total_seconds(), 1)
gap = progress - elapsed_frac
progress_state = "BREACH" if gap < -0.4 else ("WARN" if gap < -0.1 else "OK")
verdicts.append(("阶段进度", progress_state,
                 f"完成 {progress:.0%} vs 时间已用 {elapsed_frac:.0%}（差 {gap:+.0%}）"))

# 4) 等待时间（脏 worktree 闲置）
waits = []
wt_list = [ln.split()[1] for ln in git("worktree", "list", "--porcelain").splitlines() if ln.startswith("worktree ")]
for wt in wt_list:
    if not any(k in wt for k in ("/w4-", "/w5-", "/lane-", "/wave")):
        continue
    n = len(git("-C", wt, "status", "--short").splitlines())
    if n == 0:
        continue
    # 活跃度以「最近一次文件改动」为准，而不是最后一次提交：
    # lane 在读上游/跑构建时尚未提交，用提交时间会误判为停滞。
    newest = 0.0
    for p in Path(wt).rglob("*"):
        if ".git" in p.parts or not p.is_file():
            continue
        try:
            newest = max(newest, p.stat().st_mtime)
        except OSError:
            continue
    idle = (now.timestamp() - newest) / 60 if newest else 0
    waits.append((Path(wt).name, n, idle))
wait_max = max((w[2] for w in waits), default=0)
wait_state = "BREACH" if wait_max > POLICY["dirty_wait_cap_min"] else ("WARN" if waits else "OK")
verdicts.append(("等待时间", wait_state,
                 f"脏 worktree {len(waits)} 个，最长闲置 {wait_max:.0f} 分钟（上限 {POLICY['dirty_wait_cap_min']}）"))

# 5) 决策延迟（lane 提交 → 合并）
merges = {}
for line in git("log", "--merges", "--format=%ct|%s").splitlines():
    ts, _, subject = line.partition("|")
    if ts.isdigit():
        merges[subject] = int(ts)
lat, pending = [], []
for ref in git("for-each-ref", "--format=%(refname:short)", "refs/heads").splitlines():
    if not (ref.startswith("lane/") or ref.startswith("wave")):
        continue
    cts = git("log", "-1", "--format=%ct", ref)
    if not cts.isdigit():
        continue
    matched = [v for k, v in merges.items() if ref.split("/")[-1] in k]
    latency = ((max(matched) - int(cts)) / 60) if matched else ((now.timestamp() - int(cts)) / 60)
    if latency >= 0:
        lat.append((ref, latency, bool(matched)))
        if not matched:
            pending.append((ref, latency))
lat_max = max((x[1] for x in pending), default=0)
dec_state = "BREACH" if lat_max > POLICY["decision_latency_cap_min"] else ("WARN" if lat_max > 5 else "OK")
verdicts.append(("决策延迟", dec_state,
                 f"未合并分支 {len(pending)} 个，最长已等 {lat_max:.0f} 分钟（上限 {POLICY['decision_latency_cap_min']}）"))

# 6) 停止时点
freeze = parse(POLICY["scope_freeze"])
hard = parse(POLICY["internal_freeze"])
if now >= hard:
    stop_state, stop_msg = "BREACH", "已过封版点：只允许缺陷修复"
elif now >= freeze:
    stop_state, stop_msg = "WARN", "已过范围冻结：新功能进 backlog"
else:
    stop_state = "OK"
    stop_msg = f"距离范围冻结 {max((freeze - now).total_seconds() / 3600, 0):.1f}h"
if budget_state == "BREACH":
    stop_msg += "；触发超预算止损：冻结该波未完成项"
verdicts.append(("停止时点", stop_state, stop_msg))

lines = ["# 时间治理报告", "", f"生成：{now:%Y-%m-%d %H:%M}　策略：`TIME_POLICY.json`", "",
         "## 裁决", "", "| 维度 | 状态 | 依据 |", "| --- | --- | --- |"]
for name, state, msg in verdicts:
    lines.append(f"| {name} | **{state}** | {msg} |")

lines += ["", "## 预算明细", "", "| 里程碑 | 计划(h) | 实际/已用(h) | 偏差 |", "| --- | ---: | ---: | ---: |"]
for m, p, a, over in budget_lines:
    lines.append(f"| {m[:30]} | {p:.2f} | {a:.2f} | {over:+.0f}% |")

lines += ["", "## 等待中的 lane", ""]
lines += [f"- `{n}`：{c} 个未提交文件，已闲置 {i:.0f} 分钟" for n, c, i in sorted(waits, key=lambda x: -x[2])] or ["- 无"]
lines += ["", "## 未合并的 lane（决策延迟）", ""]
lines += [f"- `{ref}`：已等 {l:.0f} 分钟" for ref, l in sorted(pending, key=lambda x: -x[1])] or ["- 无"]

breaches = [v for v in verdicts if v[1] == "BREACH"]
lines += ["", "## 必须动作", ""]
lines += [f"- [{v[0]}] {v[2]}" for v in breaches] or ["- 无违约项，按计划推进"]

REPORT.write_text("\n".join(lines) + "\n")
print("\n".join(lines[:12]))
print("...")
print("\n".join(lines[-5:]))
print("\nwritten:", REPORT)
