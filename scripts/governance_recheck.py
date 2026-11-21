#!/usr/bin/env python3
"""每 6 小时治理重算：度量、ETA、单调不后移的截止时间。

- 数据：docs/delivery-backlog.json（剩余 lane-hours）、docs/delivery-targets.json（基线/当前/历史）
- 度量：git 提交/合并、测试数、QC、磁盘、worktree、slot 节奏
- 规则：新截止时间 <= 上一次；估算晚于基线时保留基线并标 RISK（不得延后）
- 产物：GOVERNANCE_LEDGER.csv、delivery-targets.json（history）、治理板卡片更新

Usage: governance_recheck.py [--print-only]
"""
from __future__ import annotations

import json
import re
import subprocess
import sys
from datetime import date, datetime, timedelta
from pathlib import Path

REPO = Path("/Users/mac/hive")
GH = REPO / "go_hive"
TARGETS = GH / "docs" / "delivery-targets.json"
BACKLOG = GH / "docs" / "delivery-backlog.json"
GOV_HTML = GH / "docs" / "delivery-governance.html"
LEDGER = GH / "GOVERNANCE_LEDGER.csv"
STATE = GH / "dist" / "slots" / "governance.json"
BUFFER = 1.5          # 估算缓冲
DEFAULT_LH_PER_DAY = 12.0  # 首跑默认；之后用台账里的实测值


def sh(cmd: str) -> str:
    try:
        return subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=30).stdout.strip()
    except Exception:
        return ""


def measure() -> dict:
    today = date.today().isoformat()
    commits = sh(f"git -C {REPO} log --since='{today} 00:00' --oneline | wc -l").strip()
    merges = sh(f"git -C {REPO} log --since='{today} 00:00' --oneline --merges | wc -l").strip()
    worktrees = sh(f"git -C {REPO} worktree list | wc -l").strip()
    df = sh("df -k /System/Volumes/Data | awk 'NR==2{print $4}'").strip()
    free_gib = round(int(df) / 1048576, 1) if df.isdigit() else None
    # tests 口径：parity 注解覆盖数（全仓实测），不再依赖不存在的 /tmp/main_ci_*.log
    # （2026-11-22 修复：曾长期读到过期日志里的 5383，与实测偏差 200+）。
    tests = 0
    try:
        out = sh(f"bash {GH}/scripts/check_parity.sh {GH}/go-hive | tail -1")
        m = re.search(r"(\d+)\s*个测试", out)
        if m:
            tests = int(m.group(1))
    except Exception:
        pass
    qc = "unknown"
    qc_report = GH / "QC_REPORT.md"
    if qc_report.exists():
        tail = qc_report.read_text(errors="ignore")[-2000:]
        m = re.search(r"(\d+)/(\d+)\s*PASS", tail)
        qc = f"{m.group(1)}/{m.group(2)}" if m else "unknown"
    slot = "none"
    cadence = GH / "dist" / "slots" / "cadence.json"
    if cadence.exists():
        d = json.loads(cadence.read_text())
        arts = d.get("last_artifact") or ""
        slot_id = ""
        for part in str(arts).split("/"):
            if re.fullmatch(r"20\d{6}-\d{2}", part):
                slot_id = part
        slot = f"{slot_id or '?'}:{d.get('last_status','?')}"
    return {
        "at": datetime.now().strftime("%Y-%m-%d %H:%M"),
        "commits_today": int(commits or 0),
        "merges_today": int(merges or 0),
        "worktrees": int(worktrees or 0),
        "disk_free_gib": free_gib,
        "tests": tests,
        "qc": qc,
        "slot": slot,
    }


def lane_hours_per_day(metrics: dict) -> float:
    """用今日已过小时数外推 lane-hours/天；无法判断时用默认。"""
    if STATE.exists():
        try:
            prev = json.loads(STATE.read_text())
            if prev.get("lh_per_day"):
                return float(prev["lh_per_day"])
        except Exception:
            pass
    hours_elapsed = datetime.now().hour + datetime.now().minute / 60
    if hours_elapsed < 2:
        return DEFAULT_LH_PER_DAY
    # 今日累计 lane-hours 由治理汇报手工落盘；无来源时用保守默认值（不拿提交数代理）。
    lh_file = GH / "dist" / "slots" / "lane_hours_today.txt"
    if lh_file.exists():
        try:
            today_lh = float(lh_file.read_text().strip())
            return round(min(max(today_lh * 24 / hours_elapsed, 6.0), 24.0), 1)
        except Exception:
            pass
    return DEFAULT_LH_PER_DAY


def compute(targets: dict, backlog: dict, lh_per_day: float) -> tuple[dict, dict, list[str]]:
    today = date.today()
    gates = backlog["gates"]
    notes: list[str] = []
    eta: dict[str, date] = {}
    cumulative = 0.0
    for gate in ("G1", "G2", "G3"):
        items = gates.get(gate, [])
        non_external = [i for i in items if not i.get("external") and not i.get("done")]
        external = [i for i in items if i.get("external") and not i.get("done")]
        cumulative += sum(float(i.get("hours", 0)) for i in non_external)
        if external:
            # 外部依赖不压缩日期：保持既有日期，风险标注
            prev = datetime.strptime(targets["current"][gate], "%Y-%m-%d").date()
            eta[gate] = prev
            notes.append(f"{gate}: {len(external)} 项外部依赖未完成，保持 {prev}（RISK）")
            continue
        days = max(1, int(-(-(cumulative * BUFFER) // lh_per_day)))  # ceil
        candidate = today + timedelta(days=days)
        prev = datetime.strptime(targets["current"][gate], "%Y-%m-%d").date()
        if candidate > prev:
            notes.append(f"{gate}: 估算 {candidate} 晚于当前 {prev}，按规则不后移，保持 {prev}（RISK）")
            eta[gate] = prev
        else:
            eta[gate] = candidate
            if candidate < prev:
                notes.append(f"{gate}: 提前 {prev} → {candidate}")
            else:
                notes.append(f"{gate}: 保持 {candidate}")
    return {k: v.isoformat() for k, v in eta.items()}, {"lh_per_day": lh_per_day, "cumulative_hours": round(cumulative, 1)}, notes


def update_governance_html(dates: dict[str, str]) -> None:
    if not GOV_HTML.exists():
        return
    html = GOV_HTML.read_text()
    new_vals = iter([f"目标 {dates['G1'][5:]}", f"目标 {dates['G2'][5:]}", f"目标 {dates['G3'][5:]}"])
    def repl(m: re.Match) -> str:
        try:
            return f'<div class="v">{next(new_vals)}</div>'
        except StopIteration:
            return m.group(0)
    html = re.sub(r'<div class="v">目标 [0-9]{2}-[0-9]{2}</div>', repl, html, count=3)
    GOV_HTML.write_text(html)


def main() -> int:
    targets = json.loads(TARGETS.read_text())
    backlog = json.loads(BACKLOG.read_text())
    metrics = measure()
    lh = lane_hours_per_day(metrics)
    new_dates, extra, notes = compute(targets, backlog, lh)
    metrics.update(extra)
    metrics.update({"eta": new_dates, "notes": notes})
    print(json.dumps(metrics, ensure_ascii=False, indent=1))
    if "--print-only" in sys.argv:
        return 0
    for gate, new in new_dates.items():
        targets["current"][gate] = new
    targets["history"].append({"at": metrics["at"], "source": "recheck", **new_dates, "lh_per_day": lh})
    TARGETS.write_text(json.dumps(targets, ensure_ascii=False, indent=2))
    LEDGER.parent.mkdir(parents=True, exist_ok=True)
    header = "at,commits_today,merges_today,tests,qc,disk_free_gib,lh_per_day,G1,G2,G3\n"
    row = f"{metrics['at']},{metrics['commits_today']},{metrics['merges_today']},{metrics['tests']},{metrics['qc']},{metrics['disk_free_gib']},{lh},{new_dates['G1']},{new_dates['G2']},{new_dates['G3']}\n"
    if not LEDGER.exists():
        LEDGER.write_text(header)
    with LEDGER.open("a") as fh:
        fh.write(row)
    STATE.parent.mkdir(parents=True, exist_ok=True)
    STATE.write_text(json.dumps(metrics, ensure_ascii=False, indent=1))
    update_governance_html(new_dates)
    notify = f"G1 {new_dates['G1']} · G2 {new_dates['G2']} · G3 {new_dates['G3']}"
    sh(f"osascript -e 'display notification \"{notify}\" with title \"Hive 治理重算\"'")
    # 6h 治理汇报留档：docs/reports/governance-<ts>.md（gen_report.py 内部提交）
    # capture_output：子进程被 timeout kill 时其块缓冲输出会全部丢失
    # （2026-11-22 06:10 事故：report 静默失踪），所以显式捕获并回放。
    report_py = str(Path(__file__).parent / "gen_report.py")
    for attempt in (1, 2):
        try:
            proc = subprocess.run(
                [sys.executable or "python3", report_py, "governance"],
                timeout=240, capture_output=True, text=True,
            )
            sys.stdout.write(proc.stdout or "")
            sys.stderr.write(proc.stderr or "")
            if proc.returncode == 0:
                break
            print(f"[governance] report generation rc={proc.returncode} (attempt {attempt})", file=sys.stderr)
        except subprocess.TimeoutExpired as exc:
            sys.stdout.write((exc.stdout or b"").decode() if isinstance(exc.stdout, bytes) else (exc.stdout or ""))
            print(f"[governance] report generation timed out (attempt {attempt})", file=sys.stderr)
        except Exception as exc:  # noqa: BLE001
            print(f"[governance] report generation failed: {exc}", file=sys.stderr)
    print("[governance] governance report step done", file=sys.stdout)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
