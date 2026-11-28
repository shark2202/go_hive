# Budget / Reminder / Park / Stall Check 对齐审计

- 日期：2026-11-25 12:06 CST
- 复核：2026-11-25 14:35 CST，基线 `5486a5fab`；P0 由 `3425aecf6`（merge `b5fc7ac23`）修复，P1 由 `53fd28042`（merge `049738d80`）修复；`make ci` RC=0，parity 6,511
- 基线：仓库 `9f19dc0b5`（Python `core/framework/**` ↔ Go `go_hive/go-hive/internal/**`）
- 范围：迭代/工具/上下文/成本预算，15 个 reminder source 与 9 个注入点，park 状态机与 idle nudge，四个 stall 守卫（响应 n-gram、worker text-only、stream stall、judge stall check）
- 方法：逐文件核对 + 复用既有 reminder 覆盖审计与测试证据；2026-11-25 14:35 复核时补跑 P0/P1 专项测试与全量 `make ci`
- 结论：**预算与 reminder 源高度一致；P0（用户 Stop 生命周期）与 P1（冷启动 loop 状态恢复）已于 2026-11-25 修复并有测试与全量 CI 证据；剩余 P2 为已登记设计差异，P3 为验证边界。**

## 1. 判定总表

| 面 | 判定 | 说明 |
|---|---|---|
| 预算数值/算法 | ✅ | queen 30/150、colony queen 50/500、worker 30/90 + lifetime 200；上下文、工具结果、流、成本、自适应预算一致 |
| Reminder source | ✅ | 15/15 行为面对齐；3 个“设计上不注册”的边界已登记 |
| Park 枚举/闸门 | ✅ | 11 个 reason、external/silent/broken 分类、idle 闸门与预算一致 |
| Park 持久化 | ✅ | ask_user/credential/suggestion/turn_done 的 `pending_input` 持久化；2026-11-25 起 `user_stopped`、`tool_calls_used`、doom 指纹也写入 cursor 并在冷启动恢复（P1，`53fd28042`） |
| 用户 Stop | ✅ | `*ChatRuntime` 已实现 `MarkUserStopped` / `CancelCurrentTurn` 并通过路由断言；`/cancel-queen` 可取消在飞 turn，`user_stopped` 闸门生效（P0，`3425aecf6`，merge `b5fc7ac23`） |
| Stall 守卫 | ✅（外部验证除外） | 四个守卫规则/文案一致；worker/overseer stream stall 与 Sentinel 真实投递未做真实网络 E2E（P3） |

## 2. 机制与配置

### 2.1 预算

| 预算 | 配置来源 | 默认 / 规则 | 两侧实现 |
|---|---|---|---|
| 迭代 | LoopConfig / worker override | queen 999999；worker 3 + grace 1 | `agent.py:21-34`、`worker_definition.py:69-76` ↔ `loop_config.go:181-271`、`colony/worker_definition.go:10-17` |
| 每 turn 工具调用 | LoopConfig | queen 30×5=150；colony queen 50×10=500；worker 30×3=90 | 同上；`worker_loop_config.go:51-90` |
| 软检查点 | 每个 budget 倍数（严格低于 hard） | 发 `TOOL_BUDGET_CHECKPOINT` + 软提醒 | `loop_guards.go:35-57`、`loop.go:984-997` |
| 生命周期工具预算 | worker profile / resume | 200；resume 累计（Go 冷恢复已恢复计数，P1 `53fd28042`） | `ToolCallLifetimeBudget` |
| 自适应 colony 预算 | `HIVE_ADAPTIVE_TOOL_BUDGET`、`_MIN_SAMPLES`、`_FLOOR` | median×2、max×1.25、floor 30、窗口 50、min 3 | `colony/adaptive_budget.go` |
| 上下文 | LoopConfig | 180k；8k+40%；warning 12k | `loop_config.go` |
| 工具结果 | `loop.max_tool_result_chars` | 30k + spillover | `worker_loop_config.go:74-90` |
| 流重试/超时 | LoopConfig | 5 次、2–60s、TTFT 600s、inter-event 120s | `stream_stall.go`、`loop_config.go` |
| 成本 | `HIVE_SESSION_COST_LIMIT_USD` | 未设=关 | `agentloop/session_cost.go` |
| per-worker override | `loop_config_overrides` | iterations 1–1000、grace 0–3、budget 0–200、lifetime 0–2000、context 1k–1M | `limits-surface.md` T8 |

### 2.2 Reminder / nudge

- 15 个 source：tasks、colony_parallel、active_workers、past_conversations、memory_recall、memory_index、turn_context、searchable_tools、skills_catalog、tracker_snapshot、worker_tracker_snapshot、colony_worker_snapshot、idle_nudge、sentinel_escalation、stream_stall。
- 9 个注入点：SESSION_START、USER_PROMPT_SUBMIT、POST_TOOL_USE、TOOL_BUDGET_CHECKPOINT、PRE_COMPACT（预留）、POST_COMPACT、STOP、IDLE_TICK、STREAM_STALLED。
- 配置：`HIVE_TRACKER_WORKER_TOOL_CALLS=20`、`HIVE_TRACKER_QUEEN_COOLDOWN_WITH_TABLE=20`；idle 预算走 LoopConfig（120s/3、awaiting 120s、broken 30s）；角色过滤与 provider 自跳过。
- 证据：`docs/reminders-coverage.md` §2–§8（15/15 行为面，残余已关闭），`internal/agentloop/reminder_source_matrix_test.go`。

### 2.3 Park

- `ParkReason`：ask_user、credential_form、colony_suggestion、turn_done、awaiting_queen、user_stopped、cold_interrupted、llm_error、empty_responses、doom_loop、unknown。
- 分类：external_wait（ask_user/credential/suggestion/awaiting_queen）、silent_park（turn_done/user_stopped/cold_interrupted）、broken（llm_error/empty_responses/doom_loop）。
- idle 闸门顺序：`user_stopped` → `external_wait` → `silent_park` → broken/awaiting 预算 → `turn_done_idle`；用户消息后 `Reset()` 重挂。
- 持久化：`<conversation>/cursor.json` 的 `pending_input` + `park_reason` + `parked_at`；冷启动按 ask_user/credential/suggestion/turn_done 恢复等待，并恢复 `user_stopped`（P1，`53fd28042`）与 loop 计数/指纹。
- 证据：`agentloop/park_reason.go`、`agentloop/idle_nudge.go`、`server/chat_park.go` ↔ `reminders.py`、`idle_nudge.py`、`agent_loop.py`。

### 2.4 Stall 守卫

| 守卫 | 规则 | 配置 | Go |
|---|---|---|---|
| 响应 n-gram | 最近 3 条回复 bigram Jaccard ≥0.85 → `node_stalled`，turn 失败 | LoopConfig 3 / 0.85 | `stall_detector.go` |
| worker text-only | 连续 2 turn 容忍，第 3 个：parallel 合成 failed report；legacy 升级并 park | `worker_escalation_grace_turns=2` | `worker_stall.go` |
| stream stall | TTFT 600s / inter-event 120s → 取消、注入 continue-nudge、重跑同一 inner turn、≤3/turn | LoopConfig | `stream_stall.go`、`stream_wiring.go` |
| judge stall_check | turn 结束且 `park_reason=turn_done`，judge p≥threshold 时提醒一次；每条用户消息最多一次 | `stall_check`（默认关）、`HIVE_STALL_CHECK`、`judge` slot、threshold 0.5 | `server/stall_check.go`、`server/judge.go` |

## 3. 差异与缺口

### P0（已修复 2026-11-25）：Go 的 Stop / cancel-queen 生产路径无效

**原缺陷（2026-11-25 12:06 审计时）**

- 路由对 queen node 做两个类型断言：
  - `queenTurnCanceller { CancelCurrentTurn() []<-chan struct{} }`
  - `queenUserStopper { MarkUserStopped() }`
  （`internal/server/routes_execution.go:137-176`）
- 生产注册的 node 是 `*ChatRuntime`（`internal/cli/loader_session_runtime.go:122-124`），但当时 `*ChatRuntime` 的方法集只有 `InjectEvent` / `InjectQueenMail` / `InjectTrigger` / `InjectWorkerReport`；全仓非测试代码没有 `CancelCurrentTurn` / `MarkUserStopped` 的实现。
- 结果：断言失败，路由返回 `{"cancelled": false, "error": "Queen node not found"}`；前端 Stop（`execution.ts:58`、`queen-dm.tsx:2718`、`colony-chat.tsx:2196`）拿不到取消；`user_stopped` 永不置位，idle nudge 的 `gate=user_stopped` 在生产中是死代码。
- 上游：cancel 前 `mark_user_stopped()`，再 `cancel_current_turn()`；`user_stopped` 写入 cursor，冷启动恢复、下一条用户消息清除（`routes_execution.py:1033-1041`、`agent_loop.py:3592-3607,1443-1460`）。
- 测试盲区：`routes_execution_test.go` 的 fake node 实现了这两个接口，只覆盖路由逻辑，不覆盖真实装配。

**修复（`3425aecf6`，2026-11-25 13:17 +0800；merge `b5fc7ac23`，2026-11-25 13:20 +0800）**

- `internal/server/chat_turn_cancel.go` 在 `*ChatRuntime` 上实现 `MarkUserStopped()` 与 `CancelCurrentTurn()`，并用编译期断言 `_ queenTurnCanceller = (*ChatRuntime)(nil)`、`_ queenUserStopper = (*ChatRuntime)(nil)` 钉住真实装配。
- `MarkUserStopped` 先持久化 loop cursor（累计工具数、doom 指纹）与 `user_stopped`，再置内存 flag；`CancelCurrentTurn` 只取消在飞 turn 的派生 context，runtime context 与排队 turn 不动，返回 turn 卸载完成的通道。下一条真实客户端输入经 `clearUserStopped` 解除；runtime 构造时 `restoreUserStopped` 从 cursor 恢复。
- 回归：`internal/server/chat_turn_cancel_test.go` 的 `TestCancelQueenStopsRealChatRuntimeTurn`、`TestCancelQueenKeepsQueuedTurnRunning`、`TestCancelCurrentTurnWithoutInflightTurn`，其中第一条走真实 `*ChatRuntime` 与 `/cancel-queen` 路由，不再只依赖 fake node。
- 验证边界：上述是 route/runtime 级测试；**未做真实 serve 进程的 Stop HTTP E2E**。

### P1（已修复 2026-11-25）：冷启动 loop 状态恢复不完整

**原缺陷（2026-11-25 12:06 审计时）**

- Python cursor 持久化 `recent_responses`、`recent_tool_fingerprints`、`tool_calls_used`、`user_stopped`（`cursor_persistence.py:33-46,159-175`）；冷恢复后 lifetime budget 继续累计、doom 窗口保留、user-stop 闸门保持。
- Go 当时只从会话恢复 `recent_responses`（`loop.go:708-709`），没有持久化 `tool_calls_used` / fingerprints / user_stopped。
- 后果：resume 的 worker 生命周期预算被重置；cold mid-turn resume 后 doom 检测窗口为空；user-stop 状态无法跨重启。
- A5 已登记 fingerprints 的残差（`alignment-differences.md:98`）；`tool_calls_used` 与 `user_stopped` 未登记。

**修复（`53fd28042`，2026-11-25 13:42 +0800；merge `049738d80`，2026-11-25 13:43 +0800）**

- `internal/agentloop/cursor_resume.go` 新增 `RestoreCursorState` / `PersistCursorState`：把 `tool_calls_used` 与 `recent_tool_fingerprints` 写入会话 cursor，冷启动时替换（不累加）到 loop；窗口非空时保留到下一 turn 边界，恢复被中断 turn 的重复历史。写入走 `CursorMutator` 的原子 read-modify-write，避免覆盖 `next_seq`、`pending_input`、park 记录等其他写者字段。
- `tool_calls_used` 在批次准入时 checkpoint（`persistToolCallsUsed`），崩溃在批次中也不会退回预算；doom 窗口在清空/重置时同步持久化空值。
- `user_stopped` 由 `chat_turn_cancel.go` 持久化/恢复/清除，路径见 P0。
- 回归：`internal/agentloop/cursor_resume_test.go`（`TestCursorStateSurvivesColdRestart`、`TestRestoredToolCallCountCapsLifetimeBudget`）、`internal/server/chat_cursor_restore_test.go`（`TestColdResumeRestoresUserStoppedAndLoopState`、`TestColdResumePreservesAskUserParkAndCursorFields`、`TestUserStoppedPersistsAcrossRuntimeRestart`）。
- 验证边界：测试覆盖 cursor 与 runtime 冷恢复；**未做真实 serve 进程重启 E2E**。

### P2（已登记，设计差异）：queen doom-loop park 的恢复形态

Go 用 server `pending_input` 的有界 turn park/resume，不是 Python turn 内 `_await_user_input` 阻塞；见 A5 残余。语义等价但恢复路径不同。

### P3（验证边界，非机制差异）

- worker/overseer 的 stream stall 只在 fake streaming provider 上验证；真实网络 serve E2E 未做。
- Sentinel 真实 Telegram/Slack 投递未验证（`support-matrix.md`）。
- Go 通用 `agentloop.DefaultLoopConfig()` 是 30/50，Python 裸 `LoopConfig()` 是 0（无软/硬限制）；当前生产全走 queen/worker profile，不可观测，但属于潜在差异。

## 4. 对齐证据

| 面 | 证据 |
|---|---|
| Reminder source 矩阵 | `docs/reminders-coverage.md` §2–§8；`reminder_source_matrix_test.go`；15/15 行为面 |
| 预算 profile | `agent.py` / `worker_definition.py` ↔ `loop_config.go` / `colony/worker_definition.go`；`limits-surface.md` T8 |
| 自适应预算 | `adaptive_budget_test.go`（与 `tests/test_adaptive_tool_budget.py` 对位） |
| Park 枚举/闸门 | `park_reason.go`、`idle_nudge.go`、`chat_park.go`；park 跨重启测试 |
| 用户 Stop（P0） | `server/chat_turn_cancel.go`、`chat_turn_cancel_test.go`；`TestCancelQueenStopsRealChatRuntimeTurn` 走真实 `*ChatRuntime` + 路由 |
| 冷启动恢复（P1） | `agentloop/cursor_resume.go`、`cursor_resume_test.go`、`server/chat_cursor_restore_test.go`；`TestCursorStateSurvivesColdRestart`、`TestUserStoppedPersistsAcrossRuntimeRestart` |
| 全量门禁 | 2026-11-25 14:35 CST，`5486a5fab`：`make ci` RC=0（parity 6,511）；P0/P1 专项测试同 HEAD 通过 |
| Stall 守卫 | `stall_detector_test.go`、`worker_stall_test.go`、`stream_stall_test.go`、`stall_check_test.go` |
| Reminder 文案 | catalog/prompt golden 与 reminder 文案 pin |

## 5. 建议

1. **P0（已完成，`3425aecf6` / merge `b5fc7ac23`）**：`*ChatRuntime` 已实现 `MarkUserStopped` / `CancelCurrentTurn`，真实装配由编译期断言与 `TestCancelQueenStopsRealChatRuntimeTurn` 覆盖；`user_stopped` 已进入 `LoopSignals` 并持久化。
2. **P1（已完成，`53fd28042` / merge `049738d80`）**：`tool_calls_used`、`recent_tool_fingerprints`、`user_stopped` 已写入 Go cursor 并在冷恢复时还原。
3. **P3（未完成）**：补一次真实网络 worker/overseer stream stall 与 Sentinel 投递的 E2E，或维持“外部未验证”标注；真实 serve 进程的 Stop/冷重启 E2E 同样未做。
4. 文档：`reminders-and-nudges.html` 的 15/15 仅指 hub 内 source，建议在页脚注明 non-hub 的 cancel/user-stop 不在该口径内。
5. 建议后续把 P0/P1 的专项测试纳入 CI 必跑清单（当前已随 `make ci` 的包级测试执行）。

## 6. 验证边界

- 2026-11-25 14:35 CST 复核：P0/P1 专项测试在 `5486a5fab` 通过；`make ci` RC=0（parity 6,511）。
- 复用：`storage-lifecycle-audit-20261125.md`、`reminders-coverage.md`、`alignment-differences.md` A5、`support-matrix.md`。
- **未运行**：真实 serve 进程的 Stop/冷重启 HTTP E2E、真实网络 worker/overseer stream stall、Sentinel 真实 Telegram/Slack 投递。P0/P1 结论来自 route/runtime 级测试与编译期断言，不等于真实 serve E2E；P2 设计差异与 P3 验证边界维持开放。
- 本轮未修改生产代码；本页只更新审计状态。
