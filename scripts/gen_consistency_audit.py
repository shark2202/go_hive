#!/usr/bin/env python3
"""Generate the consolidated hive↔go_hive consistency audit.

Reads the frozen registries/audits and emits:
  - go_hive/docs/consistency-audit.md   (human table, every module listed)
  - go_hive/docs/consistency-audit.json (machine copy + per-item basis)

Usage (repo root):  uv run --no-project python go_hive/scripts/gen_consistency_audit.py
"""
from __future__ import annotations

import json
import subprocess
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
OUT_MD = ROOT / "go_hive/docs/consistency-audit.md"
OUT_JSON = ROOT / "go_hive/docs/consistency-audit.json"

PRIMITIVES = [
    ("Loop", "✅", "agentloop 执行核：回合/工具/超时/judge/coercion；5,816 测试；SSE 42/42"),
    ("Colony", "✅", "colony runtime：spawn/kill/report/escalation；崩溃恢复 7/7"),
    ("Queen", "✅+登记", "prompt 21,584B 逐字；13 人格；模型槽；标题 LLM 槽；pin 语义登记"),
    ("Worker", "✅", "worker.json 工具面 / 升级 / stall→failed / 转录 resume"),
    ("Plan", "✅", "loop 内规划；长任务计划（chat-app）修复"),
    ("Tracker", "✅", "schema v3 两侧；C5 PASS；queen DDL/worker 门控"),
    ("EventBus", "✅+登记", "77/77 事件；SSE 42/42；events.jsonl append-only；异步派发登记"),
    ("ReminderHub", "✅", "15 source 矩阵；reminders-coverage §7 残余登记"),
    ("Skill", "✅", "25 内置文件逐字节；双 scope；端点实测"),
    ("Playbook", "✅", "Python .play.py 120/120；真实 LLM E2E 19/19；900s 守护登记"),
    ("Trigger", "✅", "timer 真机注入；triggers.json 双跑 0 差异"),
    ("Sentinel", "✅ 运行时+🔧工具", "三通道/停车/索引；sentinel_setup 工具已移植（真实投递未验证）"),
    ("Memory", "✅+登记", "v2 + notes 全族；recall 12.7ms；JSON 索引 vs SQLite 登记"),
    ("Soul", "✅", "每日反思；反向依赖消除"),
    ("Tools", "✅+登记", "desc 47/47；schema raw 25/47；输出 golden 12 例外"),
    ("MCP", "✅+登记", "协议面双跑；D3 别名层；真实外部 server 未验证"),
    ("Credentials", "✅+登记", "store/keys/health/cli/check-agent；oauth2 4 模块 + aden/client 未移植"),
    ("Budgets", "✅", "grace/hard/lifetime 门限（budget_cap 测试）"),
]
MECH_EXTRA = {
    "M06": "park", "M07": "doom-loop", "M08": "stall-check", "M09": "compaction",
    "M10": "spillover", "M11": "idle-nudge", "M12": "reminder注入",
}


def load(path: Path):
    return json.loads(path.read_text(encoding="utf-8"))


def short(p: str) -> str:
    return p.replace("go_hive/go-hive/", "").replace("go_hive/", "")


def git(*args: str) -> str:
    try:
        return subprocess.check_output(["git", "-C", str(ROOT), *args], text=True).strip()
    except Exception:
        return "?"


def main() -> int:
    inv = load(ROOT / "contracts/module-inventory.json")
    concepts = load(ROOT / "go_hive/docs/hive-concepts-inventory.json")
    integ = load(ROOT / "go_hive/docs/integrations-evidence-audit.json")
    api = load(ROOT / "go_hive/contracts/api-routes.json")
    env = load(ROOT / "go_hive/contracts/env-vars.json")
    cfg = load(ROOT / "go_hive/contracts/config-keys.json")
    tools = load(ROOT / "go_hive/contracts/tool-definitions.json")
    skills = load(ROOT / "go_hive/contracts/builtin-skills.json")

    def surface_count(side: dict, *names) -> str:
        surf = side.get("surface")
        for name in names:
            if isinstance(surf, dict) and name in surf:
                v = surf[name]
                return str(len(v)) if isinstance(v, (list, dict)) else str(v)
            if name in side:
                v = side[name]
                return str(len(v)) if isinstance(v, (list, dict)) else str(v)
        return "?"

    modules = inv.get("files") or inv.get("modules") or []
    by_status: dict[str, list] = {}
    for m in modules:
        by_status.setdefault(m.get("status", "?"), []).append(m)

    def tool_total(side: dict) -> int:
        surf = side.get("surface") or {}
        if isinstance(surf, dict):
            union = surf.get("union")
            if isinstance(union, int):
                return union
            if isinstance(union, (list, dict)):
                return len(union)
            total = 0
            for v in surf.values():
                if isinstance(v, int):
                    total += v
                elif isinstance(v, (list, dict)):
                    total += len(v)
            return total
        return 0

    tool_counts = {"reference": tool_total(tools.get("reference", {})), "go": tool_total(tools.get("target", {}))}
    api_ref = len((api.get("python") or {}).get("routes") or [])
    api_go = len((api.get("go") or {}).get("routes") or [])
    env_sum = env.get("summary", {})
    cfg_sum = cfg.get("summary", {})

    lines: list[str] = []
    add = lines.append

    add("# hive ↔ go_hive 逐项一致性核查表")
    add("")
    add("> **历史快照：** 本生成器保留 wave-71 的 2026-11-22 数据，当前三轴一致性矩阵以 "
        "`consistency-matrix.html` / `consistency-matrix.json` 为准；本文件不再作为当前核查结论。")
    add("")
    add(f"> 生成：{datetime.now().strftime('%Y-%m-%d %H:%M %Z')} · HEAD `{git('rev-parse', '--short', 'HEAD')}` · "
        "核查快照 = wave-71（`3bea069f`，ci=0 / govuln=0 / qc=0，之后仅文档提交）")
    add("> 数据源：`contracts/module-inventory.json`（C12）、`hive-concepts-inventory.json`、"
        "`integrations-evidence-audit.json`、`contracts/{api-routes,env-vars,config-keys,tool-definitions,builtin-skills}.json`。")
    add("")

    add("## 1. 一级原语（18，按 runtime_spine 8 层）")
    add("")
    add("| 原语 | 状态 | 核查证据 |")
    add("|---|---|---|")
    for name, st, ev in PRIMITIVES:
        add(f"| {name} | {st} | {ev} |")
    add("")

    add("## 2. core/framework 模块（C12 逐文件，273）")
    add("")
    for status in ("ported", "partial", "out_of_scope", "unported"):
        group = by_status.get(status, [])
        add(f"### {status}（{len(group)}）")
        add("")
        if not group:
            add("—")
            add("")
            continue
        add("| 模块 | Go counterpart |")
        add("|---|---|")
        for m in group:
            cp = m.get("go_counterpart") or "—"
            add(f"| `{m.get('python_path','')}` | `{short(str(cp))}` |")
        add("")

    add("## 3. tools/src/aden_tools 集成审计（逐模块分级）")
    add("")
    tot = integ.get("totals", {})
    add(f"- 行数 {tot.get('rows')} · actions {tot.get('actions')}（impl {tot.get('impl_actions')} / spec {tot.get('spec_actions')}）")
    add("- 上游 103 模块 / 865 actions；Go 102 模块 / 854 actions + senders 9 = 863（唯一缺口 csv_sql/excel_sql，DuckDB/cgo 有意排除）")
    add("- 分级：A 12 / B 91 / C 0 / D 1 / E 0；B = 实现完整但无真实第三方凭据（按决策标未验证）")
    add("- 逐模块明细：`docs/integrations-evidence-audit.md`（JSON/HTML 同源）")
    add("")

    add("## 4. 契约表面（逐层计数）")
    add("")
    add("| 表面 | 计数 | 参考文档 |")
    add("|---|---|---|")
    add(f"| API 路由 | 参考 {api_ref} / Go {api_go} | `docs/api-contract-diff.md`、C6 |")
    add(f"| Env 变量 | Python {env_sum.get('python','?')} / Go {env_sum.get('go','?')} / 共有 {env_sum.get('common','?')} / py-only {env_sum.get('python_only','?')} / go-only {env_sum.get('go_only','?')} | `docs/env-config-contract-diff.md`、C1/C2 |")
    add(f"| configuration.json | {cfg_sum.get('keys','?')} 键 / {cfg_sum.get('sections','?')} sections（{cfg_sum.get('status_counts',{})}） | 同上、C3 |")
    add(f"| Tool 定义 | 参考 {tool_counts['reference']} / Go 面 {tool_counts['go']}（llm_request 24 / lifecycle 16 / synthetic 1） | `docs/tool-contract-diff.md`、C8/C10 |")
    add(f"| 内置 Skill | {skills.get('count','?')} 文件逐字节 {skills.get('total_bytes','?')} B | `contracts/builtin-skills.json` |")
    add("| CLI | hive 47 路径 / hive-browser 27 / hive-cred 4；usage 错误 134 例 | `docs/cli-surface-diff.md`、C11 |")
    add("| Storage | 19 文件类；v1/v2/v3 迁移 57 项 PASS；SC-01…08 | `docs/storage-contract-diff.md`、C4/C5 |")
    add("| SSE | 42/42 帧逐字 | `contracts/sse-frames`、`docs/sse-payload-parity.md` |")
    add("")

    add("## 5. 机制面（50 机制 / 21 枚举）")
    add("")
    add("| ID | 层 | 机制 | Python 锚点 |")
    add("|---|---|---|---|")
    for m in concepts.get("mechanisms", []):
        add(f"| {m.get('id')} | {m.get('layer')} | {m.get('name')} | `{m.get('code','')}` |")
    add("")
    add("枚举面：" + "；".join(f"{e.get('name')} {e.get('count')}" for e in concepts.get("enumerations", [])))
    add("")

    add("## 6. 登记例外与外部未验证（不藏）")
    add("")
    add("- 设计性例外：浏览器渲染字节（Skia vs PIL）、字体度量、dpr 注解、父死时序、EventBus 异步派发、版本双轨、recall JSON 索引、playbook 900s 守护、工具输出 golden 12 例外")
    add("- 功能缺口：oauth2 平台 provider 4 模块（base/lifecycle/hubspot/zoho）、aden/client 云端 token 拉取、channels 宿主")
    add("- 外部未验证：Windows 实机、GitHub Actions release job、Sentinel 真实投递、91 个第三方集成、真实 MCP server")
    add("- 详见 `docs/trial/KNOWN-GAPS.md`、`docs/alignment-differences.md`、`docs/unported-triage.md`")
    add("")

    add("## 7. 核查命令与最新结果（wave-71）")
    add("")
    add("| 门禁 | 结果 |")
    add("|---|---|")
    add("| make ci（9 门禁） | **0** |")
    add("| govulncheck | **0 OSV** |")
    add("| QC | **9/9** |")
    add("| parity / refs | **5,816 / 6,391，0 missing** |")
    add("| C12 | **273 = 220/27/9/17** |")
    add("| compat | **12/12** |")
    add("| publish | 镜像同步（`7c2cccb`） |")
    add("")

    OUT_MD.write_text("\n".join(lines) + "\n", encoding="utf-8")

    payload = {
        "schema": "consistency-audit/v1",
        "status": "historical",
        "superseded_by": "go_hive/docs/consistency-matrix.html",
        "generated_at": datetime.now().isoformat(timespec="seconds"),
        "head": git("rev-parse", "--short", "HEAD"),
        "snapshot": "wave-71 @ 3bea069f",
        "primitives": [{"name": n, "status": s, "evidence": e} for n, s, e in PRIMITIVES],
        "modules": {"total": len(modules), "by_status": {k: len(v) for k, v in sorted(by_status.items())},
                    "items": [{"path": m.get("python_path"), "status": m.get("status"),
                               "go": m.get("go_counterpart"), "basis": m.get("basis")} for m in modules]},
        "integrations": {"totals": tot, "grades": integ.get("grades")},
        "mechanisms": concepts.get("mechanisms"),
        "enumerations": concepts.get("enumerations"),
    }
    OUT_JSON.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {OUT_MD.relative_to(ROOT)} and {OUT_JSON.relative_to(ROOT)}")
    print("modules:", payload["modules"]["by_status"])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
