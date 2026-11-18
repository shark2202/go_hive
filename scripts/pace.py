#!/usr/bin/env python3
"""进度步调核算：用实际发生的工作量反推速率，校准计划。

单位说明：以 lane-hour（并发 agent 数 × 墙钟小时）为核心产能单位，
因为同一时刻有 N 个 lane 并行，墙钟时间会低估真实投入。
"""
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BASE = "8a1d2646"
TARGETS = {"routes": 141, "tool_modules": 20, "tests": None}
# wave 1 的实际派工时刻（10 个 lane 同时起跑）；git 里只有合并时刻，会低估并行产能
WAVE1_START = datetime(2026, 11, 18, 20, 18, 59).astimezone()
WAVE1_LANES = 10
PLAN_HOURS = 21.6  # 20:19 → 次日 18:00
PLANNED_CURVE = [  # (累计小时, 应完成的 DoD 权重)
    (2.2, 0.35),   # wave1 实现
    (8.2, 0.60),   # 装配集成
    (15.5, 0.85),  # G3/G4 回归
    (21.6, 1.00),  # 封版
]


def git(*args: str) -> str:
    return subprocess.run(["git", "-C", str(ROOT), *args],
                          capture_output=True, text=True, check=False).stdout.strip()


def numstat_total(rev_range: str) -> tuple[int, int]:
    added = deleted = 0
    for line in git("log", "--numstat", "--format=", rev_range).splitlines():
        parts = line.split("\t")
        if len(parts) == 3 and parts[0].isdigit():
            added += int(parts[0])
            deleted += int(parts[1])
    return added, deleted


def merge_events() -> list[tuple[datetime, str]]:
    out = []
    for line in git("log", "--merges", "--format=%ct|%s", f"{BASE}..HEAD").splitlines():
        ts, _, subject = line.partition("|")
        out.append((datetime.fromtimestamp(int(ts), tz=timezone.utc).astimezone(), subject))
    return sorted(out)


def commits_total() -> int:
    return len(git("log", "--format=%H", f"{BASE}..HEAD").splitlines())


def main() -> int:
    events = merge_events()
    now = datetime.now().astimezone()
    start = events[0][0] if events else now
    elapsed_h = (now - start).total_seconds() / 3600
    added, deleted = numstat_total(f"{BASE}..HEAD")

    # 波次表：相邻合并事件之间为一个窗口
    waves = []
    for i, (ts, subject) in enumerate(events):
        nxt = events[i + 1][0] if i + 1 < len(events) else now
        dur = (nxt - ts).total_seconds() / 3600
        waves.append((subject[:44], ts.strftime("%H:%M"), nxt.strftime("%H:%M"), dur))

    ledger = ROOT / "go_hive" / "ledger.csv"
    rows = []
    if ledger.exists():
        for line in ledger.read_text().splitlines()[1:]:
            parts = line.split(",")
            if len(parts) >= 8:
                rows.append(parts)

    def latest_metric(idx: int) -> int:
        return int(rows[-1][idx]) if rows else 0

    routes = latest_metric(1)
    tests = latest_metric(3)
    tool_files = latest_metric(4)
    modules_done = 10 if tool_files else 0  # 第一批实测 10 模块 / 165 action

    # 产能：以 lane-hour 计。wave1 是 10 lane 并行，用「派工 → 最后一个 lane 合并」算真实墙钟。
    lane_merges = [ts for ts, subject in events if "merge lane/" in subject]
    wave1_end = max(lane_merges) if lane_merges else (events[0][0] if events else now)
    wave1_h = max(0.05, (wave1_end - WAVE1_START).total_seconds() / 3600)
    tail_h = max(0.0, elapsed_h - wave1_h)
    lane_hours = WAVE1_LANES * wave1_h + tail_h * 3  # 后续按 3 路并行估
    lane_hours = max(lane_hours, 0.1)

    def rate(done: int) -> float:
        return done / lane_hours

    print(f"窗口      wave1 派工 {WAVE1_START:%m-%d %H:%M} → 现在 {now:%m-%d %H:%M}  墙钟 {elapsed_h:.2f}h")
    print(f"提交      {commits_total()} 个  代码 {added:+,} 行 / 删除 {deleted:,} 行")
    print(f"产能      {lane_hours:.2f} lane-hour（{WAVE1_LANES} lane × {wave1_h:.2f}h + 后续 3 路 × {tail_h:.2f}h）")
    print(f"单位产出  {added/lane_hours:,.0f} 行/lane-h   {routes/lane_hours:.1f} 路由/lane-h   {tests/lane_hours:.0f} 测试/lane-h")
    print()
    print("波次                        起    止    时长")
    for subject, a, b, dur in waves:
        print(f"  {subject:<42} {a}  {b}  {dur*60:5.1f}m")
    print()
    print(f"{'维度':<14}{'当前':>8}{'目标':>8}{'速率(/lane-h)':>16}{'ETA(h)':>10}")
    print(f"{'路由':<14}{routes:>8}{TARGETS['routes']:>8}{rate(routes):>16.1f}{max(0,(TARGETS['routes']-routes))/max(rate(routes),0.1):>10.2f}")
    print(f"{'工具模块':<14}{modules_done:>8}{TARGETS['tool_modules']:>8}{rate(modules_done):>16.1f}{max(0,(TARGETS['tool_modules']-modules_done))/max(rate(modules_done),0.1):>10.2f}")
    print(f"{'测试':<14}{tests:>8}{'—':>8}{rate(tests):>16.1f}{'—':>10}")
    print()

    # EVM：按里程碑权重计量，PV 用计划曲线插值
    milestones = [
        ("wave1 实现合并 + G2 门禁", 0.35, True),
        ("装配接线 + G3 冒烟", 0.30, False),
        ("G4 parity 修正", 0.15, False),
        ("wave2 缺口/工具补齐", 0.10, False),
        ("G5 封版", 0.10, False),
    ]
    ev = sum(w for _, w, done in milestones if done)
    deadline = datetime(2026, 11, 19, 18, 0).astimezone()
    pv = 0.0
    prev_h, prev_w = 0.0, 0.0
    for h, w in PLANNED_CURVE:
        if elapsed_h <= h:
            span = h - prev_h
            pv = prev_w + (w - prev_w) * ((elapsed_h - prev_h) / span if span else 1.0)
            break
        prev_h, prev_w = h, w
    else:
        pv = 1.0
    spi = ev / pv if pv > 0 else 0.0
    remaining_h = (deadline - now).total_seconds() / 3600
    pending = sum(w for _, w, done in milestones if not done)
    print(f"EVM       EV {ev:.0%}  PV {pv:.0%}  SPI {spi:.2f}  （按计划曲线插值，非等权计数）")
    print(f"          待完成权重 {pending:.0%}，其中装配占 30% —— 关键路径尚未开始")
    print(f"          剩余窗口 {remaining_h:.1f}h")
    for name, w, done in milestones:
        print(f"          [{'x' if done else ' '}] {name:<28} {w:.0%}")
    print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
