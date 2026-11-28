#!/usr/bin/env python3
"""Generate the Hive reminders & nudges dossier (zh-CN HTML + JSON).

The page answers four questions in one place:
  1. Which ReminderSource types exist, and at which ReminderPoint do they fire?
  2. Which of them are "nudges" (action-oriented) vs context reminders?
  3. Why does each one exist; what failure mode does it cover?
  4. What is the current Go alignment status, including the known residuals?

Source of truth: the upstream Python framework under core/framework. The Go
status is curated in this file and cross-checked against
go_hive/docs/reminders-coverage.md by --check, so a stale status fails the
check instead of silently rotting.

Usage:
  uv run python go_hive/scripts/gen_reminders_and_nudges.py
  uv run python go_hive/scripts/gen_reminders_and_nudges.py --check
"""
from __future__ import annotations

import argparse
import html
import json
import subprocess
from datetime import datetime
from pathlib import Path
from typing import Any

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive" / "docs" / "reminders-and-nudges.html"
OUT_JSON = REPO / "go_hive" / "docs" / "reminders-and-nudges.json"
COVERAGE = REPO / "go_hive" / "docs" / "reminders-coverage.md"
README = REPO / "go_hive" / "README.md"

ESC = lambda value: html.escape(str(value), quote=True)


def code(value: str) -> str:
    return f"<code>{ESC(value)}</code>"


def pill(text: str, cls: str = "") -> str:
    return f'<span class="pill {cls}">{ESC(text)}</span>'


def status_pill(text: str) -> str:
    if text.startswith("✅"):
        return pill(text, "ok")
    if text.startswith("⚠️"):
        return pill(text, "warn")
    if text.startswith("❌"):
        return pill(text, "bad")
    return pill(text, "info")


def table(headers: list[str], rows: list[list[str]], widths: list[str] | None = None) -> str:
    if widths:
        head = "".join(f'<th style="width:{widths[i]}">{h}</th>' for i, h in enumerate(headers))
    else:
        head = "".join(f"<th>{h}</th>" for h in headers)
    body = "".join("<tr>" + "".join(f"<td>{cell}</td>" for cell in row) + "</tr>" for row in rows)
    return (
        '<div class="tablewrap"><table><thead><tr>'
        + head
        + "</tr></thead><tbody>"
        + body
        + "</tbody></table></div>"
    )


SOURCE_ROWS: list[dict[str, str]] = [
    {
        "name": "tasks",
        "kind": "计划 nudge",
        "points": "SESSION_START, USER_PROMPT_SUBMIT, POST_TOOL_USE, POST_COMPACT, STOP",
        "roles": "有 task 写工具的 agent",
        "why": "计划漂移：未跟踪工作、长期不更新、完成太多未清理、停前未收尾；task list 还能跨 compaction 存活。",
        "go": "✅ 完全对齐（worker 私有 store / overseer colony 计划均绑定）",
    },
    {
        "name": "colony_parallel",
        "kind": "行动 nudge",
        "points": "POST_TOOL_USE",
        "roles": "colony queen",
        "why": "queen 自己跑完一条 pilot 并写入 tracker 后，推她 factor 成 skill + playbook，用 run_playbook 并行收敛，别继续逐行内联。",
        "go": "✅ 完全对齐",
    },
    {
        "name": "active_workers",
        "kind": "协调",
        "points": "USER_PROMPT_SUBMIT",
        "roles": "queen",
        "why": "提醒还有 in-flight worker，避免重复派单；只列仍 active 的 worker。",
        "go": "✅ 完全对齐",
    },
    {
        "name": "past_conversations",
        "kind": "上下文",
        "points": "SESSION_START, USER_PROMPT_SUBMIT",
        "roles": "queen + recall provider",
        "why": "跨会话连续性：把相关旧对话摘录放在当前消息前，避免用户重复解释。",
        "go": "✅ 完全对齐（dfbf65b2 起进 hub）",
    },
    {
        "name": "memory_recall",
        "kind": "上下文",
        "points": "SESSION_START, USER_PROMPT_SUBMIT, POST_COMPACT",
        "roles": "queen + recall provider",
        "why": "memory_recall.mode=wait：模型先挑出相关记忆，再回答；只在该模式生效。",
        "go": "✅ 完全对齐",
    },
    {
        "name": "memory_index",
        "kind": "上下文",
        "points": "SESSION_START, USER_PROMPT_SUBMIT, POST_COMPACT",
        "roles": "queen + index provider",
        "why": "memory_recall.mode=index：列出所有记忆的 manifest，便宜且不丢发现性。",
        "go": "✅ 完全对齐",
    },
    {
        "name": "turn_context",
        "kind": "上下文",
        "points": "SESSION_START, USER_PROMPT_SUBMIT",
        "roles": "queen + turn-context provider",
        "why": "注入时间、账号、环境等 turn context，避免模型凭空猜；没有 provider 时自跳过。",
        "go": "✅ 完全对齐",
    },
    {
        "name": "searchable_tools",
        "kind": "上下文",
        "points": "SESSION_START, USER_PROMPT_SUBMIT, POST_TOOL_USE, POST_COMPACT",
        "roles": "queen / 带 provider 的 agent",
        "why": "工具太多时按需公告“现在可搜索哪些工具”，避免全量 schema 撑爆上下文。",
        "go": "✅ queen；worker 上游 provider 条件不适用",
    },
    {
        "name": "skills_catalog",
        "kind": "上下文",
        "points": "SESSION_START, USER_PROMPT_SUBMIT, POST_TOOL_USE, POST_COMPACT",
        "roles": "queen",
        "why": "只公告当前 phase 相关的 skill，避免把整个 skills 库塞进 prompt。",
        "go": "✅ 完全对齐",
    },
    {
        "name": "tracker_snapshot",
        "kind": "状态 nudge",
        "points": "POST_TOOL_USE",
        "roles": "colony queen",
        "why": "把 tracker 表/行数快照带回上下文，避免 queen 凭记忆猜表状态；无表时自跳过。",
        "go": "✅ 完全对齐",
    },
    {
        "name": "worker_tracker_snapshot",
        "kind": "状态 nudge",
        "points": "POST_TOOL_USE",
        "roles": "colony worker",
        "why": "worker 每 20 次工具调用提醒一次“先 tracker_upsert 再继续”；空表不消耗 cadence。",
        "go": "✅ 完全对齐",
    },
    {
        "name": "colony_worker_snapshot",
        "kind": "状态 nudge",
        "points": "TOOL_BUDGET_CHECKPOINT",
        "roles": "colony queen",
        "why": "在工具预算检查点给 queen 一份 fleet 快照：active/total worker、在干什么，避免重复派单。",
        "go": "✅ 完全对齐",
    },
    {
        "name": "idle_nudge",
        "kind": "行动 nudge",
        "points": "IDLE_TICK",
        "roles": "所有 loop",
        "why": "会话静默/停泊/流卡住时推一下；按 substate 分 cap，用户回复后重置。",
        "go": "✅ 完全对齐（worker 已接 OpenTasksProvider / SessionGoalProvider）",
    },
    {
        "name": "sentinel_escalation",
        "kind": "自治/升级",
        "points": "IDLE_TICK",
        "roles": "colony queen（opt-in）",
        "why": "停车后让 Sentinel 分类：continue 就 nudge，needs_human 就升级，done 就收工。",
        "go": "✅ source 注册；投递通道另算",
    },
    {
        "name": "stream_stall",
        "kind": "行动 nudge",
        "points": "STREAM_STALLED",
        "roles": "所有 loop",
        "why": "流式传输卡住时注入 continue-nudge，重跑同一 inner turn，而不是无限等。",
        "go": "✅ 完全对齐（worker/overseer 已接共享 stream watchdog）",
    },
]

POINT_ROWS: list[dict[str, str]] = [
    {
        "name": "SESSION_START",
        "when": "会话 bringup，第一次模型调用前",
        "sources": "tasks, past_conversations, memory_recall/index, turn_context, searchable_tools, skills_catalog",
        "why": "首轮定向：计划、相关旧对话、记忆、时间/账号、工具与技能目录一次到位。",
    },
    {
        "name": "USER_PROMPT_SUBMIT",
        "when": "每条用户/外部消息进入模型前",
        "sources": "tasks, active_workers, past_conversations, memory_recall/index, turn_context, searchable_tools, skills_catalog",
        "why": "先于用户消息注入相关上下文，模型看到消息时已经知道计划、并行工作和可用的工具/技能。",
    },
    {
        "name": "POST_TOOL_USE",
        "when": "一批工具执行完、结果回填后",
        "sources": "tasks, colony_parallel, tracker_snapshot, worker_tracker_snapshot, searchable_tools, skills_catalog",
        "why": "根据工具结果推进：发现计划漂移、pilot 完成该 fan-out、tracker 状态变化、需要发现新工具。",
    },
    {
        "name": "TOOL_BUDGET_CHECKPOINT",
        "when": "工具预算检查点",
        "sources": "colony_worker_snapshot",
        "why": "queen 在继续花工具预算前，先看 fleet 的 active/total 和每个 worker 的任务，避免重复派单。",
    },
    {
        "name": "PRE_COMPACT",
        "when": "上下文压缩前",
        "sources": "—（预留，当前没有 source 使用）",
        "why": "压缩前清理/保存上下文的 hook；目前没有注册 source，保留给未来需要 pre-compaction 快照的 source。",
    },
    {
        "name": "POST_COMPACT",
        "when": "上下文压缩后",
        "sources": "tasks, memory_recall/index, searchable_tools, skills_catalog",
        "why": "压缩会丢工具结果和旧消息；重新公告计划、记忆、工具和技能目录，避免“刚做过又想不起来”。",
    },
    {
        "name": "STOP",
        "when": "text-only 回合即将收尾",
        "sources": "tasks",
        "why": "停前检查：还有 open task 就不要悄悄收尾；干净的 TURN_DONE 才允许结束。",
    },
    {
        "name": "IDLE_TICK",
        "when": "ReminderHub 的后台节拍器；park 期间也运行",
        "sources": "idle_nudge, sentinel_escalation",
        "why": "时间型恢复：会话静默/停泊时推一下；colony queen opt-in 后交给 Sentinel 分类。",
    },
    {
        "name": "STREAM_STALLED",
        "when": "流式补全的 TTFT 或 inter-event 看门狗触发",
        "sources": "stream_stall",
        "why": "传输层恢复：取消卡住的流，注入 continue-nudge，重跑同一 inner turn，不把一次传输故障当模型失败。",
    },
]

NUDGE_ROWS: list[dict[str, str]] = [
    {
        "name": "idle_nudge",
        "trigger": "IDLE_TICK；会话静默/停泊超过预算",
        "guard": "120s / 3 次；awaiting 120s；broken 30s；按 substate 分 cap；用户消息 reset；Sentinel autopilot 让位",
        "why": "防止 loop 静默停住；broken park 用更短预算快速恢复，干净 TURN_DONE 不打扰。",
    },
    {
        "name": "colony_parallel",
        "trigger": "POST_TOOL_USE；pilot 工作 + 刚写 tracker",
        "guard": "cooldown 4 回合 / cap 4；run_playbook 后静默；colony phase 工具门控",
        "why": "queen 跑通一条后，应该把协议 factor 成 skill + playbook 并行收敛，而不是继续逐行内联。",
    },
    {
        "name": "stream_stall",
        "trigger": "STREAM_STALLED；TTFT > 600s 或 inter-event > 120s",
        "guard": "continue-nudge ≤3/回合；cap 按 turn ResetTurn",
        "why": "传输卡住时重跑同一 turn；cap 防止一个坏流把整个回合预算烧光。",
    },
    {
        "name": "tasks",
        "trigger": "生命周期点；计划漂移/未跟踪/停前未收尾",
        "guard": "warmup 4；drift 10；cooldown 10；soft cap 2 / backoff 2；snapshot heartbeat 12",
        "why": "task list 是跨 compaction 的计划真相；提醒模型把工作和计划对齐，停前收尾。",
    },
    {
        "name": "tracker_snapshot",
        "trigger": "POST_TOOL_USE；queen 在 colony 内",
        "guard": "首次第 3 个工具 turn；有表 cooldown 20 / 无表 10；无表 self-skip",
        "why": "tracker 是 colony 的表状态真相；让 queen 看到行数/表名，避免凭记忆猜。",
    },
    {
        "name": "worker_tracker_snapshot",
        "trigger": "POST_TOOL_USE；worker 每 20 次工具调用",
        "guard": "tracker_* 调用重置；空表不消耗 cadence",
        "why": "worker 容易忘记先 upsert；空表时不要用“无表”提醒消耗 cadence。",
    },
    {
        "name": "colony_worker_snapshot",
        "trigger": "TOOL_BUDGET_CHECKPOINT",
        "guard": "active/total 都为 0 静默；最多 8 个 worker；task 截断 80 字符",
        "why": "queen 在花更多工具预算前先看 fleet，避免重复派单，优先等 WORKER_REPORT。",
    },
    {
        "name": "active_workers",
        "trigger": "USER_PROMPT_SUBMIT",
        "guard": "只列 in-flight worker；无 colony/无 active 时静默",
        "why": "用户新消息进来时，提醒 queen 还有并行工作，别重复做同一件事。",
    },
    {
        "name": "sentinel_escalation",
        "trigger": "IDLE_TICK；colony queen opt-in",
        "guard": "classify_after 300s；最多 5 次 nudge 后升级；hive/telegram/slack",
        "why": "停车后的自治：continue 推一把，needs_human 升级给人，done 收工；避免无人值守时烂在 park。",
    },
]

GUARD_ROWS: list[dict[str, str]] = [
    {
        "name": "worker_stall",
        "what": "worker 连续 text-only turn，没有工具调用；grace 2 后：parallel worker 合成 failed report 终止，legacy worker 发 ESCALATION_REQUESTED 并 park。",
        "why": "这是 turn 级策略，不是 ReminderHub source；它决定“这个 worker 该失败/该找人”，不是“注入一段上下文”。",
    },
    {
        "name": "stall_check",
        "what": "judge 判断“说了没做”：回合宣称/承诺动作却没调用工具就收尾；概率 ≥ 0.5 时注入一次提醒。",
        "why": "需要 judge 模型和阈值，且每条用户消息最多一次；不是 source 的 applies_to 能表达的。",
    },
    {
        "name": "doom-loop breaker",
        "what": "相同工具 + 相同参数反复调用；breaker 阻断该调用，注入“换条路”的引导。",
        "why": "阻断的是调用本身，不是提醒；它先于 reminder 发生，避免把坏调用继续喂给模型。",
    },
    {
        "name": "Sentinel manager",
        "what": "colony queen park 后分类 continue / needs_human / done；escalation 记录、投递、token 路由、人回复后 resume。",
        "why": "source 只负责在 IDLE_TICK 产出“该分类”的提醒；真正的分类、投递、恢复是 manager 的职责。",
    },
]

LINES: list[dict[str, str]] = [
    {
        "name": "turn",
        "title": "Turn 生命周期：一次模型调用被哪些点包围",
        "diagram": """SESSION_START
  → USER_PROMPT_SUBMIT（reminder 在用户消息之前）
  → model call
  → tool batch
  → POST_TOOL_USE
  → text-only? STOP
  → context threshold? PRE_COMPACT → compact → POST_COMPACT
  → stream stall? STREAM_STALLED""",
        "steps": "每个点只做一件事：SESSION_START 定向；USER_PROMPT_SUBMIT 注入上下文；POST_TOOL_USE 根据工具结果推进；STOP 检查计划；PRE/POST_COMPACT 保连续性；STREAM_STALLED 恢复传输。",
        "invariants": "reminder 在用户消息之前；所有 body 合并成一个 <system-reminder>；observe_turn 每 turn 一次；source 异常不拖垮 loop。",
        "failure": "某个点没有 source 时，模型仍能工作，但退化为“无提醒”；source 注册了但 provider 为空时必须 self-skip，而不是产出空 body。",
        "anchors": "agent_loop.py fire points · reminders.py ReminderHub · loop.go ResetTurn",
    },
    {
        "name": "park-idle",
        "title": "Park → idle → nudge / escalate → resume",
        "diagram": """turn end
  → park(reason)
  → IDLE_TICK
  → idle_nudge gates:
       user_stopped / external_wait / silent_park
       broken / awaiting / between_turns / slow_ttft
  → cap / rate-limit
  → nudge body
  → drain / resume

colony queen + opt-in:
  → sentinel_escalation
  → classify(continue / needs_human / done)""",
        "steps": "park 先落 reason；idle tick 周期轮询；idle_nudge 按 substate 选预算；Sentinel 只对 colony queen + opt-in 接管；drain 后回到 loop 边界。",
        "invariants": "user_stopped 不自动恢复；external_wait / silent_park 不打扰；cap 按“用户回复周期”重置；broken park 用更短预算。",
        "failure": "TURN_DONE + 无 open task 应保持安静；cap 用尽后等待下一条用户消息；Sentinel 开启时 idle_nudge 让位，不能双推。",
        "anchors": "idle_nudge.py · chat_runtime.go:968 reset · sentinel_runtime.go · escalation_source.py",
    },
    {
        "name": "tool-result",
        "title": "工具结果 → 计划 / tracker / fleet",
        "diagram": """POST_TOOL_USE
  ├─ tasks: drift / untracked / stale
  ├─ colony_parallel: pilot + tracker write
  ├─ tracker_snapshot: queen table state
  ├─ worker_tracker_snapshot: worker cadence 20
  └─ searchable_tools / skills_catalog: discovery

TOOL_BUDGET_CHECKPOINT
  └─ colony_worker_snapshot: active/total fleet""",
        "steps": "工具结果回填后统一进入 POST_TOOL_USE；任务源看计划漂移；并行源看 pilot + tracker 写入；tracker 源看表状态；worker 源按工具调用节奏；fleet 源在预算检查点。",
        "invariants": "tracker 写入是 colony_parallel 的“完成一条”信号；没有表时 tracker 源 self-skip；worker cadence 不被空表消耗；fleet 快照最多 8 个 worker。",
        "failure": "worker 的 tasks 源已绑定 workerDir/workerID；overseer 已绑定 overseerTaskSet，生产测试覆盖；不再有“注册了但不会产出”的 tasks 缺口。",
        "anchors": "tracker_snapshot_reminder.py · colony_parallel_nudge.py · worker_reminders.go · tasks/reminders.go",
    },
    {
        "name": "stream-stall",
        "title": "Stream stall 恢复：传输故障不等于模型失败",
        "diagram": """stream open
  → TTFT > 600s 或 inter-event > 120s
  → watchdog cancels stream
  → *llm.StreamStalledError
  → injectStreamStallNudge
  → WrapReminder
  → inner--; continue（重跑同一 inner turn）

cap: ≤3 / turn
reset: ExecuteTurn 开头""",
        "steps": "看门狗只负责判定和取消；loop 负责注入 reminder 并重跑；source 只产出 body；cap 防止一个坏流烧光整回合。",
        "invariants": "不换 turn、不重跑整个任务；cap 按 turn 重置；TTFT 与 inter-event 是两个独立预算。",
        "failure": "worker/overseer 已接共享流式 watchdog；真实网络 worker/overseer serve E2E 仍需单独验证，不能把 fake provider 测试当生产证据。",
        "anchors": "stream_stall.py · streaming_completer.go · stream_wiring.go · loop.go · stream_stall_recovery.go",
    },
    {
        "name": "compaction",
        "title": "Compaction：上下文压缩后的再定向",
        "diagram": """context threshold
  → PRE_COMPACT（当前没有 source）
  → compact
  → POST_COMPACT
  → tasks：重读计划
  → memory_recall / memory_index：重放记忆
  → searchable_tools / skills_catalog：重放目录""",
        "steps": "压缩前 hook 已存在；压缩后由 tasks / memory / tools / skills 重新公告；past_conversations 已在 hub（dfbf65b2），不再由 runtime 外挂。",
        "invariants": "task list 从 store 重新读；memory mode 决定 index/wait；同一个 catalog 不重复公告；past 的包装/顺序与 hub 一致。",
        "failure": "PRE_COMPACT 没有 source；worker tasks / idle open-task 的 provider/store 缺口已关闭（w87/reminders-residual）。",
        "anchors": "agent_loop.py PRE/POST_COMPACT · chat_memory_wiring.go · tasks/reminders.go",
    },
    {
        "name": "user-message",
        "title": "用户消息 → 上下文注入 → 模型",
        "diagram": """user message
  → reset idle caps
  → active_workers
  → past_conversations
  → memory_recall / memory_index
  → turn_context
  → searchable_tools / skills_catalog
  → one <system-reminder> before user message
  → model""",
        "steps": "每条用户消息先重置 idle cap，再按 provider 注入并行工作、旧对话、记忆、时间/账号、工具/技能目录；最后合并成一个 reminder 块。",
        "invariants": "reminder 在用户消息之前；provider 为空时 self-skip；不产生用户气泡；不让 source 直接写 conversation。",
        "failure": "worker context 已接 OpenTasksProvider / SessionGoalProvider；worker tasks 绑定私有 store；past 已进 hub（dfbf65b2），不再是缺口。",
        "anchors": "chat_reminders.go · chat_memory_wiring.go · worker_reminders.go · reminders.py wrap_reminder",
    },
    {
        "name": "worker-failure",
        "title": "Worker 失败 / 升级：三个 stall 不是一件事",
        "diagram": """text-only turn
  → grace 2
  → parallel worker? synthesized failed report
  → legacy worker? ESCALATION_REQUESTED + park
  → stall_check? judge reminder
  → doom-loop? breaker blocks the call""",
        "steps": "worker_stall 是 turn 级策略；stall_check 是 judge；doom-loop 是 breaker；三者都在 ReminderHub 之外，不能混成 nudge。",
        "invariants": "任何工具调用都算 progress；parallel worker 不发 escalation，直接 failed report；legacy worker 才 park 等 queen；stall_check 每条用户消息最多一次。",
        "failure": "worker task reminder 与 worker idle open-task 已由真实 worker 路径测试证明；结构门禁全绿仍不能替代功能门禁。",
        "anchors": "worker_stall.go · stall_check.py · stream_stall.py · worker_escalation.go",
    },
    {
        "name": "sentinel",
        "title": "Sentinel：停车后的分类、升级、恢复",
        "diagram": """colony queen park
  → classify_after 300s
  → classify:
       continue  → nudge
       needs_human → escalation
       done      → close
  → channel: hive / telegram / slack
  → human reply
  → token → resume""",
        "steps": "Sentinel 只在 colony queen + opt-in 时工作；source 在 IDLE_TICK 产出分类请求；manager 负责分类、记录、投递、token 路由和 resume。",
        "invariants": "硬阻塞（认证/崩溃）在 classify 之前决定；最多 5 次 nudge 后升级；escalation 有盘上记录；人回复按 token 恢复。",
        "failure": "未知 channel 会报错；source 注册不等于投递可用；OSS 前端没有 escalation 历史/收件箱 UI。",
        "anchors": "escalation_source.py · sentinel/manager.py · sentinel/store.py · routes_sentinel.py",
    },
]

DIMENSIONS: list[dict[str, str]] = [
    {
        "name": "角色视角",
        "question": "谁有资格看到这条 source？",
        "observes": "chat queen / colony overseer / worker / judge / Sentinel",
        "finding": "同一 source 跨角色复用，applies_to 是唯一边界；chat queen 13 个 hub source + Sentinel 1 = 14；worker 4 个；overseer 6 个 queen-role source（含 tasks）；past_conversations 已在 hub（dfbf65b2）。",
        "evidence": "chat_reminders.go / worker_reminders.go / overseer_reminders.go; 角色矩阵测试",
    },
    {
        "name": "时间视角",
        "question": "什么时候触发？",
        "observes": "lifecycle / temporal / reactive",
        "finding": "9 个注入点；IDLE_TICK 是唯一后台节拍器；STREAM_STALLED 是唯一 reactive 点；PRE_COMPACT 目前没有 source。",
        "evidence": "reminders.py ReminderPoint; 9/9 枚举；ticker 0.01–5s",
    },
    {
        "name": "状态视角",
        "question": "park 后处于什么状态？",
        "observes": "ParkReason / idle substate / activity / escalation status",
        "finding": "11 个 ParkReason；idle_nudge 5 个 substate；只碰异常与 unknown，不碰干净 TURN_DONE 与外部等待。",
        "evidence": "idle_nudge.py; ParkReason 表；activity 单点推导",
    },
    {
        "name": "数据视角",
        "question": "body 从哪里来？",
        "observes": "task store / tracker DB / memory / skills / tools / conversation",
        "finding": "provider 为空时必须 self-skip；task/tracker 源读盘；memory/skills 源读目录；past 读旧会话；没有 provider 就没有 body。",
        "evidence": "providers in chat_reminders.go; tasks/reminders.go; tracker_snapshot_reminder.py",
    },
    {
        "name": "控制视角",
        "question": "如何限速、唤醒、重置？",
        "observes": "applies_to / points / cap / cooldown / energizes / reset",
        "finding": "idle 120s/3；parallel cooldown 4/cap 4；stream cap 3/turn；Sentinel 300s/5；只有行动型 source 才 energize。",
        "evidence": "idle_nudge.py; colony_parallel_nudge.py; stream_stall.py; sentinel/store.py",
    },
    {
        "name": "失败视角",
        "question": "什么坏了？",
        "observes": "stream stall / worker stall / stall_check / doom loop",
        "finding": "四套不同的检测-决策-动作；stream 治传输，worker_stall 治 turn，stall_check 治言行，doom-loop 治重复调用。",
        "evidence": "stream_stall.py; worker_stall.go; server/stall_check.py; doom-loop breaker",
    },
    {
        "name": "实现视角",
        "question": "Python 与 Go 差在哪？",
        "observes": "source 类型 / 注册矩阵 / 真实触发 / provider / store",
        "finding": "15/15 类型与行为面全部对齐；worker/overseer 的 tasks 与 worker idle open-task 变体均绑定真实 store/provider，并有生产路径测试；结构门禁 ≠ 功能门禁仍是审查原则。",
        "evidence": "reminders-coverage.md; 目标包 3/3 PASS; Python 155 + 97（基线）",
    },
    {
        "name": "运维视角",
        "question": "怎么知道它工作了？",
        "observes": "reminder_injected / stream_* / execution_* / logs / tests",
        "finding": "source 产出有事件和日志；worker task snapshot / worker real turn / worker idle open-task / overseer real turn 均有生产路径测试；真实网络 worker/overseer serve E2E 仍属外部验证项。",
        "evidence": "event bus; e2e-assets/reminders; test names in §09",
    },
    {
        "name": "决策视角",
        "question": "为什么不是更少/更多？",
        "observes": "source 数量 / cap / 角色过滤 / 上下文经济",
        "finding": "每个 source 对应一种可独立测试的失败模式；cap 防 wallpaper；provider 为空 self-skip；拆开比一个大 source 更容易治理。",
        "evidence": "tradeoff 表; 设计原则; 反模式",
    },
]

ANGLES: list[dict[str, str]] = [
    {
        "name": "模型视角",
        "see": "一条 <system-reminder> 文本；不知道是哪个 source 产出的。",
        "blind": "source 的身份、cap、provider、注册矩阵。",
        "implication": "body 必须自包含；不能依赖“模型记得这是第几次提醒”。",
    },
    {
        "name": "source 视角",
        "see": "points / applies_to / render / observe_turn / tick_interval。",
        "blind": "loop 的内部状态，除了 ReminderContext 暴露的 signals、tool_names、user_text。",
        "implication": "source 不能直接写 conversation；只能返回 body，由 hub 统一包装。",
    },
    {
        "name": "loop 视角",
        "see": "observe_turn / collect / fire / reset / pending drain。",
        "blind": "source 的业务细节；它只按 point 和 applies_to 调用。",
        "implication": "新增 source 不改 loop；只注册 + 实现接口。",
    },
    {
        "name": "运维视角",
        "see": "reminder_injected、stream_*、execution_*、日志、测试。",
        "blind": "模型是否真的照做；只能看“注入了什么”。",
        "implication": "需要功能 E2E 和真实转录，不能只看事件计数。",
    },
    {
        "name": "Go 视角",
        "see": "注册表、ticker、provider、store、真实触发面。",
        "blind": "上游 source 的隐含语义；必须逐条对照。",
        "implication": "注册 ≠ 工作；结构门禁 ≠ 功能门禁。",
    },
]

TRADEOFFS: list[dict[str, str]] = [
    {
        "choice": "拆成 15 个 source",
        "why": "每个 source 的触发条件、状态、cap、文案都不同；拆开才能独立测试、独立调参、独立关闭。",
        "cost": "注册矩阵和角色过滤更复杂；新增 source 要补 provider、测试、文档。",
    },
    {
        "choice": "nudge 推自己，escalate 找人",
        "why": "能自己恢复的不要打扰人；真正需要人的才升级。",
        "cost": "分类错误会导致该找人时自己空转，或该自己干时过早打扰。",
    },
    {
        "choice": "cap + cooldown",
        "why": "没有限速的提醒会变成 wallpaper，模型学会忽略。",
        "cost": "极端情况下会少推一次；所以 cap 按用户回复周期重置，而不是整个会话。",
    },
    {
        "choice": "一个 hub，按 applies_to 过滤",
        "why": "queen DM、overseer、worker 共用 AgentLoop；避免三套 loop 各写一遍。",
        "cost": "source 必须正确实现 applies_to；provider 为空时要 self-skip。",
    },
    {
        "choice": "部分机制放在 hub 外",
        "why": "worker_stall 是 turn 策略；stall_check 是 judge；doom-loop 是 breaker；它们不是“注入一段上下文”。",
        "cost": "对齐时要跨文件审计，容易把“注册了”误当成“完整”。",
    },
    {
        "choice": "上下文经济",
        "why": "工具 schema、skill 目录、记忆 manifest 都很大；只公告相关部分。",
        "cost": "发现性下降；需要 searchable_tools / skills_catalog 的 phase 过滤。",
    },
    {
        "choice": "fail-open",
        "why": "source 的 applies_to / render 抛异常时，不能静默丢掉所有提醒。",
        "cost": "坏 source 可能反复失败；需要日志和测试兜底。",
    },
    {
        "choice": "结构门禁 + 功能门禁分开",
        "why": "注册/ticker 只能证明接线；真实注入要跑会话。",
        "cost": "CI 更慢；但能避免“全绿但 worker 不工作”的假绿。",
    },
]

MATRIX: list[tuple[str, ...]] = [
    ("tasks", "●", "●", "●", "●", "●", "●", "●", "●", "●"),
    ("colony_parallel", "●", "●", "●", "●", "●", "●", "●", "●", "●"),
    ("active_workers", "●", "●", "○", "●", "○", "●", "●", "●", "●"),
    ("past_conversations", "○", "●", "○", "●", "○", "●", "●", "●", "●"),
    ("memory_recall", "○", "●", "○", "●", "○", "●", "●", "●", "●"),
    ("memory_index", "○", "●", "○", "●", "○", "●", "●", "●", "●"),
    ("turn_context", "○", "●", "—", "●", "—", "●", "●", "●", "●"),
    ("searchable_tools", "●", "●", "○", "●", "○", "●", "●", "●", "●"),
    ("skills_catalog", "●", "●", "○", "●", "○", "●", "●", "●", "●"),
    ("tracker_snapshot", "●", "●", "●", "●", "○", "●", "●", "●", "●"),
    ("worker_tracker_snapshot", "●", "●", "●", "●", "○", "●", "●", "●", "●"),
    ("colony_worker_snapshot", "●", "●", "●", "●", "○", "●", "●", "●", "●"),
    ("idle_nudge", "●", "●", "●", "○", "●", "●", "●", "●", "●"),
    ("sentinel_escalation", "○", "●", "●", "●", "●", "●", "●", "●", "●"),
    ("stream_stall", "●", "●", "●", "○", "●", "●", "●", "●", "●"),
]

VIEW_CUBE = """                  ┌──────────── 角色 ────────────┐
                  │  queen / overseer / worker     │
                  │  judge / sentinel              │
                  └──────────────┬─────────────────┘
                                 │
        ┌──────────── 时间 ──────┼──────── 状态 ─────────┐
        │ lifecycle / ticker     │ park reason / substate │
        │ reactive               │ activity / escalation  │
        └────────────┬───────────┴───────────┬────────────┘
                     │                       │
        ┌──────────── 数据 ──────┐  ┌──────── 控制 ────────┐
        │ store / provider / body│  │ cap / cooldown /     │
        │ wrapper / conversation │  │ energize / reset     │
        └────────────┬───────────┘  └───────────┬──────────┘
                     │                          │
        ┌──────────── 失败 ──────┐  ┌──────── 实现 ────────┐
        │ stream / worker /      │  │ Python vs Go /       │
        │ stall_check / doom     │  │ 结构 vs 功能         │
        └────────────┬───────────┘  └───────────┬──────────┘
                     │                          │
                     └────────── 运维 / 决策 ──┘"""

PRINCIPLES: list[tuple[str, str]] = [
    (
        "一个 hub，按角色过滤",
        "queen DM、colony overseer、worker 共用 AgentLoop；每个 source 用 applies_to 自报“我属于谁”，避免三套 loop 各写一遍。",
    ),
    (
        "生命周期点，不是到处轮询",
        "source 只声明 points；loop 在 SESSION_START / USER_PROMPT_SUBMIT / POST_TOOL_USE / POST_COMPACT / STOP / IDLE_TICK / STREAM_STALLED 等已知点调用，时序可测。",
    ),
    (
        "cap 与 cooldown",
        "每个 source 自己限速；没有 cap 的提醒会变成 wallpaper，模型会学会忽略。",
    ),
    (
        "上下文经济",
        "工具 schema、skill 目录、记忆 manifest 都很大；searchable_tools / skills_catalog / memory_index 只公告“现在相关”的部分。",
    ),
    (
        "advisory wrapper",
        "所有 body 合并后包成一个 <system-reminder>；模型可忽略，不污染用户消息，也不假装是用户指令。",
    ),
    (
        "可观测 + 失败隔离",
        "reminder_injected 事件记录 source / substate / nudge 计数；source 的 applies_to / render 抛异常只跳过该 source，不拖垮 loop。",
    ),
    (
        "守卫与自治分开",
        "stream stall / stall check / doom loop 是三种不同守卫；worker_stall 是 turn 策略；Sentinel 是停车 supervisor。不要把它们都叫 nudge。",
    ),
]

STATUS_ROWS: list[dict[str, str]] = []

TESTS: list[tuple[str, str]] = [
    ("Go 目标包", "internal/colony / agentloop / tasks：PASS（本分支实跑）"),
    ("Go 全量 CI", "基线 5,976 tests PASS（本分支未重跑全量 make ci）"),
    ("Python reminder", "8 个 reminder 测试文件：155 passed（基线）"),
    ("Python Sentinel", "8 个 Sentinel 测试文件：97 passed（基线）"),
    (
        "真实链路",
        "TestWorkerTaskReminderReachesARealWorkerTurn / TestOverseerTaskReminderReachesARealOverseerTurn / "
        "TestWorkerIdleNudgeParkedOpenTasksCarriesGoalAndNamesTasks / "
        "TestChatRuntimeStreamStallReminderReachesTheModel / TestWorkerTrackerSnapshotReachesARealWorkerTurn",
    ),
]

ANCHOR_CHECKS: list[tuple[Path, str, str]] = [
    (REPO / "core/framework/agent_loop/agent_loop.py", "self._reminder_hub.register(", "15 source 注册点"),
    (REPO / "core/framework/agent_loop/agent_loop.py", "ReminderPoint.PRE_COMPACT", "PRE_COMPACT 预留点"),
    (REPO / "core/framework/agent_loop/reminders.py", "class ReminderSource", "ReminderSource 基类"),
    (REPO / "go_hive/go-hive/internal/server/chat_reminders.go", "hub.Register(r.idleSource)", "chat idle source"),
    (REPO / "go_hive/go-hive/internal/server/chat_reminders.go", "hub.Register(agentloop.NewPastConversationsReminderSource())", "chat past_conversations source"),
    (REPO / "go_hive/go-hive/internal/colony/worker_reminders.go", "hub.Register(idle)", "worker idle source"),
    (REPO / "go_hive/go-hive/internal/colony/overseer_reminders.go", "hub.Register(agentloop.NewColonyParallelNudgeSource())", "overseer 6-source 子集"),
    (REPO / "go_hive/go-hive/internal/colony/worker_reminders.go", "hub.Register(&workerTaskReminderSource{inner: tasks.NewTaskReminderSourceFor(taskStore, worker.ID)})", "worker task source bound to worker store"),
    (REPO / "go_hive/go-hive/internal/colony/worker_reminders.go", "ctx.OpenTasksProvider = func() []agentloop.IdleTask {", "worker idle open-task provider"),
    (REPO / "go_hive/go-hive/internal/colony/overseer_reminders.go", "hub.Register(&workerTaskReminderSource{inner: tasks.NewTaskReminderSourceFor(store, sessionID)})", "overseer task source bound to colony plan"),
    (REPO / "go_hive/go-hive/internal/tasks/reminders.go", "func NewTaskReminderSourceFor(store *TaskStore, sessionID string)", "scoped task reminder source"),
    (REPO / "go_hive/go-hive/internal/colony/worker_task_tools.go", "tasks.NewTaskStore(workerDir)", "worker 私有 task store"),
    (REPO / "go_hive/go-hive/internal/server/chat_reminders.go", "PastConversationRecallProvider", "past_conversations provider"),
    (COVERAGE, "15 个完全对齐、0 个有条件差异", "状态口径"),
    (COVERAGE, "w87/reminders-residual", "残余关闭记录"),
]

DIAGRAM = """turn end / park(reason)
  ├─ IDLE_TICK ── idle_nudge ──┐
  │                sentinel_escalation ── classify(continue / needs_human / done)
  ├─ STREAM_STALLED ── stream_stall ── retry same inner turn
  └─ POST_TOOL_USE ── tasks / colony_parallel / tracker / fleet

context points:
  SESSION_START / USER_PROMPT_SUBMIT / POST_COMPACT / STOP

guards outside the hub:
  worker_stall (turn policy) · stall_check (judge) · doom-loop breaker"""


def git_head() -> str:
    try:
        return (
            subprocess.run(
                ["git", "-C", str(REPO), "rev-parse", "--short", "HEAD"],
                capture_output=True,
                text=True,
                timeout=5,
            ).stdout.strip()
            or "unknown"
        )
    except Exception:
        return "unknown"


def now_cst() -> str:
    return datetime.now().strftime("%Y-%m-%d %H:%M") + " CST"


def build_data() -> dict[str, Any]:
    return {
        "schema": "hive-reminders-and-nudges/1",
        "generated_at": now_cst(),
        "baseline": git_head(),
        "counts": {
            "sources": len(SOURCE_ROWS),
            "points": len(POINT_ROWS),
            "lines": len(LINES),
            "dimensions": len(DIMENSIONS),
            "full": 15,
            "partial": 0,
        },
        "sources": SOURCE_ROWS,
        "points": POINT_ROWS,
        "lines": LINES,
        "dimensions": DIMENSIONS,
        "angles": ANGLES,
        "tradeoffs": TRADEOFFS,
        "matrix": [{"source": row[0], "cells": list(row[1:])} for row in MATRIX],
        "view_cube": VIEW_CUBE,
        "nudges": NUDGE_ROWS,
        "guards": GUARD_ROWS,
        "principles": [{"name": name, "why": why} for name, why in PRINCIPLES],
        "status": STATUS_ROWS,
        "tests": [{"name": name, "evidence": evidence} for name, evidence in TESTS],
        "maintenance": {
            "generator": "go_hive/scripts/gen_reminders_and_nudges.py",
            "check": "uv run python go_hive/scripts/gen_reminders_and_nudges.py --check",
            "coverage": "go_hive/docs/reminders-coverage.md",
        },
    }


def build_html(data: dict[str, Any]) -> str:
    d = data["counts"]

    source_rows = [
        [
            f"<b>{i}</b>",
            code(row["name"]),
            pill(row["kind"], "info"),
            ESC(row["points"]),
            ESC(row["roles"]),
            ESC(row["why"]),
            status_pill(row["go"]),
        ]
        for i, row in enumerate(data["sources"], 1)
    ]
    point_rows = [
        [code(row["name"]), ESC(row["when"]), ESC(row["sources"]), ESC(row["why"])]
        for row in data["points"]
    ]
    nudge_rows = [
        [code(row["name"]), ESC(row["trigger"]), ESC(row["guard"]), ESC(row["why"])]
        for row in data["nudges"]
    ]
    guard_rows = [
        [f"<b>{ESC(row['name'])}</b>", ESC(row["what"]), ESC(row["why"])]
        for row in data["guards"]
    ]
    principle_rows = [
        [f"<b>{ESC(row['name'])}</b>", ESC(row["why"])] for row in data["principles"]
    ]
    status_rows = [
        [
            code(row["source"]),
            status_pill(row["status"]),
            ESC(row["gap"]),
            ESC(row["evidence"]),
        ]
        for row in data["status"]
    ]
    test_rows = [[f"<b>{ESC(name)}</b>", ESC(evidence)] for name, evidence in TESTS]
    line_cards = []
    for line in data["lines"]:
        line_cards.append(
            '<div class="card linecard">'
            f'<div class="k">{ESC(line["name"])}</div>'
            f'<h3>{ESC(line["title"])}</h3>'
            f'<pre>{ESC(line["diagram"])}</pre>'
            f'<p><b>步骤</b>：{ESC(line["steps"])}</p>'
            f'<p><b>不变量</b>：{ESC(line["invariants"])}</p>'
            f'<p><b>失败分支</b>：{ESC(line["failure"])}</p>'
            f'<p class="sub"><b>代码锚点</b>：{ESC(line["anchors"])}</p>'
            "</div>"
        )
    dimension_rows = [
        [
            f"<b>{ESC(row['name'])}</b>",
            ESC(row["question"]),
            ESC(row["observes"]),
            ESC(row["finding"]),
            code(row["evidence"]),
        ]
        for row in data["dimensions"]
    ]
    angle_rows = [
        [f"<b>{ESC(row['name'])}</b>", ESC(row["see"]), ESC(row["blind"]), ESC(row["implication"])]
        for row in data["angles"]
    ]
    tradeoff_rows = [
        [f"<b>{ESC(row['choice'])}</b>", ESC(row["why"]), ESC(row["cost"])]
        for row in data["tradeoffs"]
    ]
    matrix_rows = [
        [code(row["source"])] + [f'<span class="mx">{ESC(cell)}</span>' for cell in row["cells"]]
        for row in data["matrix"]
    ]

    style = """
:root{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}
*{box-sizing:border-box} body{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}
a{color:var(--accent2);text-decoration:none} a:hover{text-decoration:underline}
.wrap{max-width:1280px;margin:0 auto;padding:44px 26px 80px}
header{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}
.eyebrow{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}
h1{font-size:30px;line-height:1.25;margin:10px 0 10px;letter-spacing:-.02em} h1 span{color:var(--accent)}
.lede{color:var(--muted);max-width:1100px;font-size:15px} .meta{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px} .meta b{color:var(--text)}
section{margin:38px 0} h2{font-size:22px;margin:0 0 8px} h2 .num{color:var(--accent);font-size:14px;margin-right:8px} h3{font-size:15.5px;margin:22px 0 8px;color:#cfdae6}
p{color:#c6d2df;margin:8px 0} .sub{color:var(--muted);font-size:12.5px}
.grid{display:grid;gap:12px} .g4{grid-template-columns:repeat(4,minmax(0,1fr))} .g2{grid-template-columns:repeat(2,minmax(0,1fr))} .grid>*{min-width:0}
@media(max-width:900px){.g4{grid-template-columns:1fr 1fr}.g2{grid-template-columns:1fr}} @media(max-width:560px){.g4{grid-template-columns:1fr}.wrap{padding:30px 16px 60px}h1{font-size:24px}}
.linecard h3{margin:6px 0 10px;color:var(--text);font-size:15px} .linecard pre{font-size:11px;line-height:1.45} .linecard p{font-size:12.5px;margin:6px 0} .mx{font-size:16px;line-height:1}
.card{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px;min-width:0} .card .k{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700} .card .v{font-size:20px;font-weight:800;margin-top:5px;letter-spacing:-.01em} .card .d{color:var(--muted);font-size:12px;margin-top:5px}
.tablewrap{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2);max-width:100%}
table{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed} th,td{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word} th{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1} tr:hover td{background:#121a25} tr:last-child td{border-bottom:0}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}
pre{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2;max-width:100%}
.pill{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal} .pill.ok{color:var(--ok);border-color:#1d3b26;background:#0f1a13} .pill.warn{color:var(--warn);border-color:#3d3013;background:#1a150a} .pill.bad{color:var(--bad);border-color:#3d1d1d;background:#1a0f0f} .pill.info{color:var(--accent2);border-color:#17324d;background:#0d1622}
.callout{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4;overflow-wrap:anywhere} .callout.bad{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7} .callout.info{border-color:var(--accent2);background:#0d1622;color:#cfe3ff}
.toc{display:flex;flex-wrap:wrap;gap:7px;margin-top:18px} .toc a{font-size:12px;border:1px solid var(--line);background:var(--panel);padding:5px 9px;border-radius:999px;color:var(--muted)} .toc a:hover{color:var(--text);text-decoration:none}
ul,ol{margin:7px 0 7px 20px;padding:0} li{margin:4px 0;color:#c6d2df} footer{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}
figure.diagram{background:var(--code);border:1px solid var(--line);border-radius:12px;padding:14px;overflow-x:auto;margin:16px 0;max-width:100%}
figure.diagram figcaption{color:var(--muted);font-size:12px;margin-top:8px}
@media(max-width:560px){table{font-size:12px}th,td{padding:7px 6px}}
@media(max-width:760px){.tablewrap table{min-width:980px}}
@media(max-width:560px){.toc a{padding:8px 12px;font-size:13px;min-height:44px;display:inline-flex;align-items:center}}
"""

    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Hive Reminders &amp; Nudges · 点-线-面 · 源、链路、维度、为什么、Go 对齐</title>
<style>{style}</style></head><body><div class="wrap">
<header><div class="eyebrow">Hive · Reminders &amp; Nudges</div><h1>Reminder 与 Nudge <span>点 · 线 · 面</span></h1>
<p class="lede">这不只是 source 清单。本文按 <b>点（source / point / guard）→ 线（8 条生命周期链路）→ 面（9 个横切维度）</b> 展开：每个点是什么、每条线怎么走、每个面暴露了什么风险，以及 Go 现在对齐到哪一步。<b>nudge 不是独立类型</b>，而是 reminder 里带行动推力的一类；stall 与 Sentinel 也不能混为一谈。</p>
<div class="meta"><span>生成：<b>{ESC(data['generated_at'])}</b></span><span>基线：<b>{ESC(data['baseline'])}</b></span><span>口径：<b>以代码为准</b></span><span>产物：<b>HTML + JSON</b></span></div>
<div class="toc"><a href="#s00">点线面地图</a><a href="#s01">点：15 source</a><a href="#s02">点：9 注入点</a><a href="#s03">线：8 条链路</a><a href="#s04">面：9 个维度</a><a href="#s05">交叉矩阵</a><a href="#s06">nudge 子集</a><a href="#s07">非 hub 守卫</a><a href="#s08">为什么 / tradeoff</a><a href="#s09">Go 对齐</a><a href="#s10">代码索引</a><a href="#s11">维护</a></div></header>

<section id="s00"><h2><span class="num">00</span>点线面地图</h2>
<div class="grid g4">
<div class="card"><div class="k">点：Source 类型</div><div class="v">{d['sources']}</div><div class="d">ReminderHub 上注册的 source 类型</div></div>
<div class="card"><div class="k">点：注入点</div><div class="v">{d['points']}</div><div class="d">ReminderPoint 枚举</div></div>
<div class="card"><div class="k">线 / 面</div><div class="v">{d['lines']} / {d['dimensions']}</div><div class="d">生命周期链路 / 横切维度</div></div>
<div class="card"><div class="k">Go 行为面</div><div class="v">{d['full']} / {d['partial']}</div><div class="d">完全对齐 / 有条件差异</div></div>
<div class="card"><div class="k">回归证据</div><div class="v">15/15</div><div class="d">行为面完全对齐；目标包 3/3 PASS，真实 worker/overseer loop 测试覆盖</div></div>
</div>
<div class="callout info"><b>先记住一句话：</b>reminder 是机制；nudge 是其中“催行动”的子集；worker_stall / stall_check / doom loop 是独立守卫；Sentinel 是停车后的 supervisor。<b>点</b>看单条规则，<b>线</b>看状态怎么走，<b>面</b>看角色/时间/数据/控制/失败/实现/运维/决策如何交叉。</div>
<figure class="diagram"><pre>{ESC(VIEW_CUBE)}</pre><figcaption>观察角度：不是所有问题都能在 source 表里回答；角色、时间、状态、数据、控制、失败、实现、运维、决策是九个横切面。</figcaption></figure>
<figure class="diagram"><pre>{ESC(DIAGRAM)}</pre><figcaption>主流程：park / 生命周期点 → source 产出 body → 合并成一个 &lt;system-reminder&gt; → 模型下一轮读取；不产生用户气泡。</figcaption></figure>
</section>

<section id="s01"><h2><span class="num">01</span>15 个 ReminderSource</h2>
<p class="sub">上游在每个 AgentLoop 的 ReminderHub 上注册 15 个 source，再由 applies_to 按 queen / worker / overseer 角色过滤。下表“类型”是口语分类，不是代码继承关系。</p>
{table(["#", "source", "类型", "注入点", "适用角色", "为什么存在", "Go 状态"], source_rows, ["4%", "13%", "9%", "19%", "13%", "27%", "15%"])}
<div class="callout"><b>为什么是 15 个而不是一个大 source：</b>每个 source 的触发条件、状态、cap 和文案都不同；拆开才能单独测试、单独调参、单独关闭。</div>
</section>

<section id="s02"><h2><span class="num">02</span>9 个注入点</h2>
{table(["ReminderPoint", "何时触发", "当前 source", "为什么需要这个点"], point_rows, ["16%", "24%", "28%", "32%"])}
<div class="callout info"><b>PRE_COMPACT 是预留：</b>hook 已存在（loop 在压缩前 fire），但当前没有 source 声明这个点；新增 source 时可以用它做压缩前快照/清理。</div>
</section>

<section id="s03"><h2><span class="num">03</span>线：8 条生命周期链路</h2>
<p class="sub">点回答“这条规则是什么”；线回答“它什么时候、按什么顺序、和谁一起发生”。下面 8 条不是独立流程，而是同一个 loop 在不同故障下的走法。每条线都给出时序、步骤、不变量、失败分支和代码锚点。</p>
<div class="grid g2">
{''.join(line_cards)}
</div>
</section>

<section id="s04"><h2><span class="num">04</span>面：9 个横切维度</h2>
<p>面回答“从哪个角度看”。同一个 source 在角色、时间、状态、数据、控制、失败、实现、运维、决策九个维度上暴露的问题不同；只看 source 表，会漏掉 provider、store、ticker、结构假绿等关键风险。</p>
{table(["维度", "问题", "观察面", "结论", "证据"], dimension_rows, ["10%", "20%", "24%", "30%", "16%"])}
<h3>五种观察角度</h3>
<p class="sub">同一个系统，不同角色看到的东西不一样；这决定了“谁应该负责什么”。</p>
{table(["角度", "看到什么", "看不到什么", "含义"], angle_rows, ["12%", "32%", "28%", "28%"])}
</section>

<section id="s05"><h2><span class="num">05</span>点线面交叉矩阵</h2>
<p class="sub">行是 15 个 source，列是 9 个维度。符号：<b>●</b> 主承载 / <b>○</b> 条件或可选 / <b>—</b> 不适用 / <b>⚠</b> 有已知缺口。矩阵不是评分；⚠ 表示该 source 在该维度有真实缺口，不是“能力不存在”。</p>
{table(["source", "角色", "时间", "状态", "数据", "控制", "失败", "实现", "运维", "决策"], matrix_rows, ["16%", "9%", "9%", "9%", "9%", "9%", "9%", "9%", "9%", "12%"])}
<div class="callout info"><b>矩阵没有 ⚠：</b>worker 的 <code>tasks</code> 已绑定 workerDir/workerID，overseer 的 <code>tasks</code> 已绑定 <code>overseerTaskSet</code>；worker 的 <code>idle_nudge</code> 已接 <code>OpenTasksProvider</code> / <code>SessionGoalProvider</code>。<code>stream_stall</code> 已由 <code>w86/stream-stall</code> 接到 worker/overseer 共享 watchdog。结构门禁仍不等于功能门禁，但这两条现有真实 worker / overseer loop 测试。</div>
</section>

<section id="s06"><h2><span class="num">06</span>nudge 子集：哪些是“催行动”</h2>
<p class="sub">“nudge”不是基类；下面这些 source 的 body 会推动 loop 继续/恢复/改道，并且大多带 cap 或 cooldown。</p>
{table(["source", "触发", "默认 / 抑制", "为什么存在"], nudge_rows, ["14%", "24%", "28%", "34%"])}
<div class="callout"><b>nudge ≠ escalate ≠ retry：</b>nudge 推自己继续；escalate 升级找人；retry 重跑。Sentinel 的 continue 分支是 nudge，needs_human 分支是 escalate。</div>
</section>

<section id="s07"><h2><span class="num">07</span>不在 ReminderHub 里的守卫</h2>
<p class="sub">下面四个经常被混进 nudge，但它们是独立机制；把它们算进 15 个 source 会导致“看起来对齐、实际职责错了”。</p>
{table(["机制", "是什么", "为什么独立"], guard_rows, ["18%", "46%", "36%"])}
</section>

<section id="s08"><h2><span class="num">08</span>为什么会有这些？</h2>
<p>因为一个长生命周期 agent 有五种不同故障模式：等待恢复、上下文连续性、上下文经济、多 agent 协调、人机接管。每个 source 只覆盖一种，组合起来才是完整的注意力系统。</p>
{table(["设计原则", "解释"], principle_rows, ["24%", "76%"])}
<h3>关键 tradeoff</h3>
{table(["选择", "为什么", "代价"], tradeoff_rows, ["20%", "48%", "32%"])}
<div class="callout info"><b>仍然要盯住的误判：</b>“注册了”不等于“工作了”。本分支已把 worker/overseer 的 tasks 与 worker idle provider 接到真实 store/session，并用真实 worker/overseer loop 测试兜底；新增 source 时仍要补功能测试，不能只加注册断言。</div>
</section>

<section id="s09"><h2><span class="num">09</span>Go 对齐状态</h2>
<p class="sub">15/15 个 source 类型都已实现；行为面 <b>15 个完全对齐、0 个有条件差异</b>（`tasks` / `idle_nudge` 的 worker/overseer 残余已关闭）。即便如此，结构门禁（注册/ticker）仍不等于功能门禁（真实注入），本页把两类证据分开列。</p>
{table(["source", "状态", "缺口", "证据"], status_rows, ["14%", "12%", "30%", "44%"])}
<div class="callout info"><b>残余为空：</b><code>tasks</code> 与 <code>idle_nudge</code> 两条有条件差异已关闭；<code>stream_stall</code> 的真实网络 serve E2E 仍属外部验证项，不以 fake provider 测试冒充。</div>
<h3>回归与真实触发</h3>
{table(["类别", "证据"], test_rows, ["18%", "82%"])}
<div class="callout info"><b>结构门禁之外的功能证据：</b><code>TestWorkerTaskReminderRendersTheWorkersOwnPlan</code> / <code>TestWorkerTaskReminderReachesARealWorkerTurn</code> / <code>TestWorkerIdleNudgeParkedOpenTasksCarriesGoalAndNamesTasks</code> / <code>TestOverseerTaskReminderReadsTheOverseerPlan</code> / <code>TestOverseerTaskReminderReachesARealOverseerTurn</code> 均断言真实 reminder 内容；旧代码分别红在空 snapshot、无 active source、provider 为 nil、首请求无 <code>&lt;system-reminder&gt;</code>。</div>
</section>

<section id="s10"><h2><span class="num">10</span>代码索引</h2>
<div class="tablewrap"><table><thead><tr><th style="width:38%">文件</th><th style="width:62%">内容</th></tr></thead><tbody>
<tr><td><code>core/framework/agent_loop/reminders.py</code></td><td>ReminderSource / ReminderHub / ReminderPoint / ParkReason / wrap_reminder</td></tr>
<tr><td><code>core/framework/agent_loop/agent_loop.py</code></td><td>15 个 source 的注册；SESSION_START / USER_PROMPT_SUBMIT / POST_TOOL_USE / PRE_COMPACT / POST_COMPACT / STOP 的 fire 点</td></tr>
<tr><td><code>core/framework/agent_loop/idle_nudge.py</code></td><td>idle_nudge：substate / cap / budget / body / reset</td></tr>
<tr><td><code>core/framework/agent_loop/colony_parallel_nudge.py</code></td><td>colony_parallel：pilot / tracker write / fan-out / phase gate</td></tr>
<tr><td><code>core/framework/agent_loop/stream_stall.py</code></td><td>stream_stall：STREAM_STALLED / cap / reset</td></tr>
<tr><td><code>core/framework/agent_loop/tracker_snapshot_reminder.py</code></td><td>queen + worker tracker snapshot</td></tr>
<tr><td><code>core/framework/tasks/reminders.py</code></td><td>tasks：drift / snapshot / stop / state</td></tr>
<tr><td><code>core/framework/sentinel/escalation_source.py</code></td><td>sentinel_escalation：IDLE_TICK / opt-in / classify</td></tr>
<tr><td><code>go_hive/go-hive/internal/agentloop/*</code></td><td>Go 侧 source 实现（idle / parallel / stream / tracker / memory / tool / turn）</td></tr>
<tr><td><code>go_hive/go-hive/internal/server/chat_reminders.go</code></td><td>chat queen hub 注册 + idle provider</td></tr>
<tr><td><code>go_hive/go-hive/internal/colony/worker_reminders.go</code></td><td>worker hub + worker 私有 task source / idle providers</td></tr>
<tr><td><code>go_hive/go-hive/internal/colony/overseer_reminders.go</code></td><td>overseer 的 6 个 queen-role source（含 colony 存储域 tasks）</td></tr>
<tr><td><code>go_hive/go-hive/internal/colony/worker_task_tools.go</code></td><td>worker 私有 task store（workerDir / workerID）</td></tr>
<tr><td><code>go_hive/go-hive/internal/colony/overseer_tools.go</code></td><td>overseerTaskSet（colony 存储域 / overseer session）</td></tr>
<tr><td><code>go_hive/docs/reminders-coverage.md</code></td><td>Go 对齐矩阵、残余、真实 E2E 证据</td></tr>
<tr><td><code>go_hive/docs/reminders-and-nudges.html</code></td><td>本页（生成物；点-线-面总览与交叉矩阵）</td></tr>
</tbody></table></div>
</section>

<section id="s11"><h2><span class="num">11</span>维护与复用</h2>
<ol>
<li><b>重算</b>：<code>uv run python go_hive/scripts/gen_reminders_and_nudges.py</code>（写 HTML + JSON）。</li>
<li><b>防漂移</b>：<code>uv run python go_hive/scripts/gen_reminders_and_nudges.py --check</code>；会比对 HTML/JSON 字节、15 个 source 注册点、Go 关键锚点、以及 reminders-coverage.md 的 15/0 状态口径。</li>
<li><b>新增 source</b>：先补上游 Python 实现与测试，再在本页 <code>SOURCE_ROWS / POINT_ROWS / LINES / DIMENSIONS / MATRIX / STATUS_ROWS</code> 加条目和 Go 证据；不要只手改 HTML。</li>
<li><b>状态纪律</b>：结构门禁（注册/ticker）与功能门禁（真实注入）分开写；未跑真实 E2E 的项标“未验证”，不要写成“已对齐”。</li>
</ol>
</section>

<footer>
<p>生成器：<code>go_hive/scripts/gen_reminders_and_nudges.py</code> · 数据：<a href="reminders-and-nudges.json">reminders-and-nudges.json</a> · 基线：<code>{ESC(data['baseline'])}</code> · 生成时间：{ESC(data['generated_at'])}</p>
<p>相关：<a href="loop-vocabulary.html">Loop Vocabulary（术语）</a> · <a href="reminders-coverage.md">Reminders 覆盖说明</a> · <a href="event-bus.html">Event Bus</a> · <a href="stall-check.html">stall check</a> · <a href="hive-inbox.html">Hive Inbox</a> · <a href="module-interfaces.html">模块接口</a></p>
</footer>
</div></body></html>"""


def write_outputs(data: dict[str, Any]) -> None:
    OUT_JSON.write_text(json.dumps(data, ensure_ascii=False, indent=1) + "\n")
    OUT_HTML.write_text(build_html(data))


def check_outputs() -> int:
    problems: list[str] = []
    if not OUT_HTML.exists():
        problems.append(f"missing {OUT_HTML}")
    if not OUT_JSON.exists():
        problems.append(f"missing {OUT_JSON}")
    if problems:
        for problem in problems:
            print(f"reminders-and-nudges: {problem}")
        return 1

    existing = json.loads(OUT_JSON.read_text())
    data = build_data()
    # Keep the checked-in timestamp/baseline stable for a pure --check run.
    data["generated_at"] = existing.get("generated_at", data["generated_at"])
    data["baseline"] = existing.get("baseline", data["baseline"])
    expected_html = build_html(data)
    expected_json = json.dumps(data, ensure_ascii=False, indent=1) + "\n"
    if OUT_HTML.read_text() != expected_html:
        problems.append(f"{OUT_HTML} is stale (run the generator)")
    if OUT_JSON.read_text() != expected_json:
        problems.append(f"{OUT_JSON} is stale (run the generator)")

    for path, needle, label in ANCHOR_CHECKS:
        if not path.exists():
            problems.append(f"missing {path} ({label})")
            continue
        if needle not in path.read_text():
            problems.append(f"{path}: missing {label!r} needle {needle!r}")

    if problems:
        for problem in problems:
            print(f"reminders-and-nudges: {problem}")
        return 1
    print(
        "reminders-and-nudges: OK "
        f"({len(SOURCE_ROWS)} sources, {len(POINT_ROWS)} points, "
        f"{len(NUDGE_ROWS)} nudges, {len(GUARD_ROWS)} guards, "
        f"status 15 full / 0 partial, HTML+JSON up to date)"
    )
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true", help="check generated files and anchors without writing")
    args = parser.parse_args()
    if args.check:
        return check_outputs()

    data = build_data()
    write_outputs(data)
    print(f"wrote {OUT_HTML} ({OUT_HTML.stat().st_size:,} bytes)")
    print(f"wrote {OUT_JSON} ({OUT_JSON.stat().st_size:,} bytes)")
    print(
        f"sources={len(SOURCE_ROWS)} points={len(POINT_ROWS)} "
        f"nudges={len(NUDGE_ROWS)} guards={len(GUARD_ROWS)} "
        f"status=15 full / 0 partial baseline={data['baseline']}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
