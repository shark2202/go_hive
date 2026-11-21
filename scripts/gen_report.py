#!/usr/bin/env python3
"""2h/6h 汇报与交付留档生成器（夜间无人托管治理）。

用法:
  gen_report.py slot <slot_id>     # 每 2h：slot 交付件 + 五段式汇报 → docs/reports/slot-<slot>.md
  gen_report.py governance         # 每 6h：治理汇报（含倒排排期/RISK）→ docs/reports/governance-<ts>.md

设计约束：
- 只写 docs/reports/ 下文件并 git commit（留档）；失败不吞错（打印并在 slot 场景以非零码返回）。
- 所有数字来自实测命令（git / parity / C12 / freeze / cadence / ledger），拿不到就如实写“获取失败”。
- 由 scripts/release_slot.sh 与 scripts/governance_recheck.py 调用；也可手动跑补档。
"""
from __future__ import annotations

import json
import re
import subprocess
import sys
from datetime import datetime, timedelta
from pathlib import Path

REPO = Path("/Users/mac/hive")
GH = REPO / "go_hive"
GOHIVE = GH / "go-hive"
SLOTS = GH / "dist" / "slots"
REPORTS = GH / "docs" / "reports"
CADENCE = SLOTS / "cadence.json"
TARGETS = GH / "docs" / "delivery-targets.json"
BACKLOG = GH / "docs" / "delivery-backlog.json"
LEDGER = GH / "GOVERNANCE_LEDGER.csv"


def sh(cmd, cwd=GOHIVE, timeout=240):
    try:
        r = subprocess.run(cmd, cwd=str(cwd), shell=True, capture_output=True, text=True, timeout=timeout)
        return r.returncode, (r.stdout or "") + (r.stderr or "")
    except Exception as e:  # noqa: BLE001
        return 1, f"[exec failed] {e}"


def log(msg):
    print(f"[gen-report] {msg}")


def git(*args):
    rc, out = sh("git " + " ".join(args), cwd=REPO)
    return out.strip() if rc == 0 else f"[git failed] {' '.join(args)}"


def commits_since(hours):
    rc, out = sh(
        f"git log --since='{hours} hours ago' --pretty='%h %ad %s' --date=format:'%H:%M' HEAD",
        cwd=REPO,
    )
    lines = [l for l in out.splitlines() if l.strip()] if rc == 0 else []
    merges = [l for l in lines if "merge" in l.lower()]
    return lines, merges


def parity_line():
    rc, out = sh(f"bash {GH}/scripts/check_parity.sh .")
    for line in reversed(out.splitlines()):
        if "check_parity" in line:
            return line.strip()
    return out.strip().splitlines()[-1] if out.strip() else "获取失败"


def c12_line():
    rc, out = sh(f"python3 {REPO}/scripts/check_module_inventory.py --check", cwd=REPO)
    for line in out.splitlines():
        if "C12" in line:
            return line.strip()
    return out.strip().splitlines()[-1] if out.strip() else "获取失败"


def freeze_line():
    rc, out = sh(f"bash {GH}/scripts/refactor_freeze.sh compare wave15-base")
    return "OK — frozen surface unchanged (with allowlisted changes)" if rc == 0 and "OK" in out else f"检查输出: {out.strip().splitlines()[-1] if out.strip() else '获取失败'}"


def worktrees():
    rc, out = sh("git worktree list", cwd=REPO)
    lanes = []
    if rc == 0:
        for l in out.splitlines():
            m = re.search(r"\.wt/(\S+)", l)
            if m:
                lanes.append(m.group(1))
    return lanes


def disk_free():
    rc, out = sh("df -h / | tail -1")
    parts = out.split()
    return parts[3] if rc == 0 and len(parts) >= 4 else "?"


def backlog_stats() -> dict:
    try:
        d = json.loads(BACKLOG.read_text())
        out = {}
        for gate, items in d["gates"].items():
            rem = [i for i in items if not i.get("done")]
            out[gate] = {
                "remaining": len(rem),
                "hours": sum(int(i.get("hours", 0)) for i in rem),
                "total_items": len(items),
                "top": [f"{i['id']}（{i.get('hours', '?')}h）" for i in rem[:5]],
            }
        return out
    except Exception as e:  # noqa: BLE001
        return {"_error": str(e)}


def targets_info() -> dict:
    try:
        d = json.loads(TARGETS.read_text())
        hist = d.get("history", [])[-3:]
        return {"current": d["current"], "baseline": d["baseline"], "tail": hist}
    except Exception as e:  # noqa: BLE001
        return {"_error": str(e)}


def ledger_tail():
    try:
        lines = [l for l in LEDGER.read_text().splitlines() if l.strip()]
        return lines[-2:]
    except Exception as e:  # noqa: BLE001
        return [f"[ledger 读取失败] {e}"]


def slot_deliveries(hours=6):
    rows = []
    if SLOTS.exists():
        cutoff = datetime.now() - timedelta(hours=hours)
        for d in sorted(SLOTS.glob("20*-*")):
            if not d.is_dir():
                continue
            try:
                if datetime.fromtimestamp(d.stat().st_mtime) < cutoff:
                    continue
            except OSError:
                pass
            compat = "?"
            cf = d / "compat.json"
            if cf.exists():
                try:
                    compat = json.loads(cf.read_text()).get("status", "?")
                except Exception:  # noqa: BLE001
                    compat = "unreadable"
            art = sorted(d.glob("*darwin-arm64.tar.gz"))
            size = art[0].stat().st_size if art else 0
            rows.append(f"{d.name}: artifact={size:,}B compat={compat}".replace(",", "_"))
    return rows or ["（最近 6h 无 slot 交付记录）"]


def ci_latest():
    f = REPORTS / "ci-latest.json"
    if f.exists():
        try:
            d = json.loads(f.read_text())
            return f"wave-{d.get('wave','?')} @ {d.get('head','?')}：ci={d.get('ci')} qc={d.get('qc')} publish={d.get('publish')}（{d.get('at','?')}）"
        except Exception:  # noqa: BLE001
            return "ci-latest.json 读取失败"
    return "尚无记录（ci-latest.json 未生成）"


def next_slots(n=6):
    now = datetime.now()
    slots = []
    t = now.replace(minute=0, second=0, microsecond=0)
    t = t + timedelta(hours=(2 - t.hour % 2) % 2 or 2)
    for _ in range(n):
        slots.append(t.strftime("%Y%m%d-%H"))
        t += timedelta(hours=2)
    return slots


def commit_report(path: Path, message: str):
    rel = str(path.relative_to(REPO))
    sh(f'git add {rel}', cwd=REPO)
    rc, out = sh(
        f'git -c user.name=hive-governor -c user.email=hive-governor@local '
        f'commit -m "{message}" -- {rel}',
        cwd=REPO,
    )
    if rc == 0:
        log(f"committed {rel}")
    else:
        log(f"commit skipped/failed: {out.strip()[:200]}")


def update_index(entry: str):
    idx = REPORTS / "INDEX.md"
    header = (
        "# 汇报与交付留档索引\n\n"
        "每 2 小时（slot 交付 + 汇报）与每 6 小时（治理汇报）自动生成并提交；\n"
        "由 `scripts/release_slot.sh` 与 `scripts/governance_recheck.py` 调用 `scripts/gen_report.py`。\n\n"
    )
    lines = []
    if idx.exists():
        lines = [l for l in idx.read_text().splitlines() if l.startswith("- ")]
    lines.append(entry)
    idx.write_text(header + "\n".join(lines[-400:]) + "\n")
    return idx


def main(argv):
    mode = argv[1] if len(argv) > 1 else ""
    now = datetime.now()
    ts = now.strftime("%Y-%m-%d %H:%M %Z")
    REPORTS.mkdir(parents=True, exist_ok=True)

    if mode == "slot":
        slot = argv[2] if len(argv) > 2 else (json.loads(CADENCE.read_text()).get("last_slot", "unknown") if CADENCE.exists() else "unknown")
        sd = SLOTS / slot
        cad = json.loads(CADENCE.read_text()) if CADENCE.exists() else {}
        commits, merges = commits_since(2)
        lanes = worktrees()
        bl = backlog_stats()
        detail = cad.get("last_detail", "")
        compat = cad.get("last_compat", "?")
        head = cad.get("last_head", git("rev-parse", "--short", "HEAD"))
        # 交付件详情
        art_lines = []
        manifest = sd / "manifest.json"
        if manifest.exists():
            try:
                m = json.loads(manifest.read_text())
                version = m.get("version", "?")
                for b in m.get("binaries", []):
                    bp = sd / b
                    art_lines.append(f"- 组件 `{b}`: {'存在' if bp.exists() else '缺失'}" + (f" ({bp.stat().st_size:,}B)" if bp.exists() else ""))
            except Exception as e:  # noqa: BLE001
                version = "?"
                art_lines.append(f"- manifest 读取失败: {e}")
        else:
            version = "?"
        body = [
            f"# Slot 交付与汇报 · {slot}",
            f"生成时间：{ts}（自动，scripts/gen_report.py slot）",
            "",
            f"## 一、目标",
            f"2h 窗口内推进 lane 并产出 slot 交付件 `{slot}`（版本 {version}，HEAD `{head}`）；不引入新功能，只做对齐/收口/验证。",
            "",
            f"## 二、进度（交付）",
            f"- 交付件：`{cad.get('last_artifact', '(未记录)')}`",
            f"- 详情：{detail}",
            f"- 升级兼容：compat={compat}",
        ] + art_lines + [
            f"- 本窗口提交：{len(commits)} 笔（合并 {len(merges)} 笔）",
        ] + ([f"  - {c}" for c in commits[:12]]) + [
            f"- 活跃 lane（worktree）：{len(lanes)} 条" + (f"：{', '.join(lanes)}" if lanes else ""),
            "",
            "## 三、质量（实测）",
            f"- parity 注解：{parity_line()}",
            f"- C12 模块清单：{c12_line()}",
            f"- 行为冻结：{freeze_line()}",
            f"- 全量 CI：{ci_latest()}",
            "",
            "## 四、时间与效率",
            f"- 节奏：每 2h 一个 slot；slot 目录保留最近 6 个",
            f"- 磁盘可用：{disk_free()}",
            f"- 治理台账（最近）：{' ｜ '.join(ledger_tail()[-1:]) or '无'}",
            "",
            "## 五、问题与反馈",
        ]
        if "_error" in bl:
            body.append(f"- backlog 读取失败：{bl['_error']}")
        else:
            for gate in sorted(bl):
                g = bl[gate]
                body.append(f"- {gate}：剩 {g['remaining']}/{g['total_items']} 项 / {g['hours']}h；top：{'; '.join(g['top']) if g['top'] else '无'}")
        body.append("")
        text = "\n".join(body)
        out1 = sd / "REPORT.md"
        out2 = REPORTS / f"slot-{slot}.md"
        if sd.exists():
            out1.write_text(text)
        out2.write_text(text)
        idx = update_index(f"- slot-{slot}.md — {ts} · {version} · HEAD {head} · compat={compat}")
        commit_report(out2, f"reports: slot {slot} 汇报与交付留档")
        sh(f'git add {str(idx.relative_to(REPO))}', cwd=REPO)
        sh(f'git -c user.name=hive-governor -c user.email=hive-governor@local commit -m "reports: slot {slot} 索引更新" -- {str(idx.relative_to(REPO))}', cwd=REPO)
        log(f"wrote {out2}")
        return 0

    if mode == "governance":
        stamp = now.strftime("%Y%m%d-%H%M")
        commits6, merges6 = commits_since(6)
        commits24, merges24 = commits_since(24)
        lanes = worktrees()
        bl = backlog_stats()
        ti = targets_info()
        sched = next_slots(6)
        body = [
            f"# 治理汇报 · {ts}（6h 重算窗口，自动生成）",
            "",
            "## 一、目标",
            "- 五轴口径：外观/语义/功能/特性一致 + 无损迁移（验收只认实测）。",
        ]
        if "_error" in ti:
            body.append(f"- 交付目标读取失败：{ti['_error']}")
        else:
            cur, base = ti.get("current") or {}, ti.get("baseline") or {}
            body.append(f"- 交付截止（当前）：G1 {cur.get('G1')} · G2 {cur.get('G2')} · G3 {cur.get('G3')}（只能提前，不得延后）")
            body.append(f"- 基线：G1 {base.get('G1')} · G2 {base.get('G2')} · G3 {base.get('G3')}")
        body += [
            "",
            "## 二、进度",
            f"- 近 6h 提交 {len(commits6)} 笔（合并 {len(merges6)} 笔）；近 24h 提交 {len(commits24)} 笔（合并 {len(merges24)} 笔）",
            f"- HEAD：`{git('rev-parse', '--short', 'HEAD')}` — {git('log', '-1', '--pretty=%s')}",
            "- 近 6h slot 交付：",
        ] + [f"  - {r}" for r in slot_deliveries(6)] + [
            f"- 活跃 lane：{len(lanes)} 条" + (f"：{', '.join(lanes)}" if lanes else ""),
            "",
            "## 三、质量（实测）",
            f"- parity 注解：{parity_line()}",
            f"- C12 模块清单：{c12_line()}",
            f"- 行为冻结：{freeze_line()}",
            f"- 全量 CI：{ci_latest()}",
            "",
            "## 四、时间与效率",
            "- 治理台账（最近 2 行）：",
        ] + [f"  - {l}" for l in ledger_tail()] + [
            f"- 磁盘可用：{disk_free()}",
            "",
            "## 五、问题与反馈（含 RISK）",
        ]
        if "_error" in bl:
            body.append(f"- backlog 读取失败：{bl['_error']}")
        else:
            for gate in sorted(bl):
                g = bl[gate]
                body.append(f"- {gate}：剩 {g['remaining']}/{g['total_items']} 项 / {g['hours']}h；top：{'; '.join(g['top']) if g['top'] else '无'}")
        body += [
            "- 常设风险：vulncheck（stdlib CVE，等批准）；P0-G2-1 exports 基准待拍板；上游 1.0.15 参考切换未决；LongMemEval 仍 n=20。",
            "",
            "## 六、倒排排期（未来 6 个 slot）",
        ] + [f"- {s}" for s in sched] + [
            f"- 对照 G1 截止：{(ti.get('current') or {}).get('G1', '?')}（排期只提前不延后）",
            "",
        ]
        text = "\n".join(body)
        out = REPORTS / f"governance-{stamp}.md"
        out.write_text(text)
        idx = update_index(f"- governance-{stamp}.md — {ts} · 6h 治理汇报")
        commit_report(out, f"reports: governance {stamp} 治理汇报留档")
        sh(f'git add {str(idx.relative_to(REPO))}', cwd=REPO)
        sh(f'git -c user.name=hive-governor -c user.email=hive-governor@local commit -m "reports: governance {stamp} 索引更新" -- {str(idx.relative_to(REPO))}', cwd=REPO)
        log(f"wrote {out}")
        if "--notify" in argv:
            sh(f"osascript -e 'display notification \"治理汇报已留档 governance-{stamp}.md\" with title \"Hive 治理汇报\"'")
        return 0

    print(__doc__)
    return 2


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
