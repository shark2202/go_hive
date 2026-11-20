# Reminders 覆盖对齐说明（source 矩阵 / 生命周期 / 预载 colony queen scope）

本文回答 `queen-worker-colony-parity-audit.html` 维度 #12（Reminders / idle tick）与 GAP-06 的
修复情况：Go 侧 reminder source 的注册矩阵、触发/抑制边界、会话启停生命周期，以及预载
colony 会话的 queen scope 解析。基线：`main@46939ab5` 之上的 `reminders-coverage` 分支；
上游参考为 `core/framework`（hive 1.0.14）。

## 1. 结论

- `STREAM_STALLED` 从「有 source、零注册、零触发」变为**注册 + 真实触发**：流式补全的
  TTFT / 事件间静默看门狗在 `internal/server/chat_stream.go` 里判定，循环把判定转成
  `<system-reminder>` 并**重跑同一 inner turn**（上游 continue-nudge 语义）。
- worker hub 从 task + idle 扩到 **task + worker_tracker_snapshot + idle + stream_stall**；
  overseer loop 从 **nil hub** 变成 queen role 的 5 个 source（parallel / active_workers /
  tracker / fleet / stream_stall）。
- queen chat hub 补入 `colony_worker_snapshot`（tool-budget checkpoint 的 fleet 快照）。
- 预载 colony 会话不再固定 `default` queen scope：从 colony `metadata.json` 的 `queen_name`
  解析（与上游 `_create_session_for_existing_colony` 一致），会话目录、记忆目录与 skills
  catalog 一并按该 queen 落位。
- 残余（见 §7）：`past_conversations` 在 Go 由 runtime 直接注入、未搬进 hub；
  overseer 的 skills catalog 走既有 relay；worker 没有 searchable_tools tier。
  这三项均不改变上游语义，但登记为「实现形态差异」而非静默缺口。

## 2. 上游 source 全量清单

`core/framework/agent_loop/agent_loop.py::AgentLoop.__init__` 在**每个** AgentLoop 的
`ReminderHub` 上注册 13 个 source，再由各 source 的 `applies_to` 按角色/阶段过滤：

| # | source | points | applies_to（上游） |
| --- | --- | --- | --- |
| 1 | `tasks` | SESSION_START, USER_PROMPT_SUBMIT, POST_TOOL_USE, POST_COMPACT, STOP | 工具表含 task 写工具 |
| 2 | `colony_parallel` | POST_TOOL_USE | queen |
| 3 | `active_workers` | USER_PROMPT_SUBMIT | queen |
| 4 | `past_conversations` | SESSION_START, USER_PROMPT_SUBMIT | queen + recall provider |
| 5 | `turn_context` | SESSION_START, USER_PROMPT_SUBMIT | queen + turn-context provider |
| 6 | `searchable_tools` | SESSION_START, USER_PROMPT_SUBMIT, POST_TOOL_USE, POST_COMPACT | queen，或自带 provider 的分层 worker |
| 7 | `skills_catalog` | 同上 | queen |
| 8 | `tracker_snapshot` | POST_TOOL_USE | queen + colony binding provider |
| 9 | `worker_tracker_snapshot` | POST_TOOL_USE | **worker** + colony binding provider |
| 10 | `colony_worker_snapshot` | TOOL_BUDGET_CHECKPOINT | queen + colony binding provider |
| 11 | `idle_nudge` | IDLE_TICK（ticker） | 所有 agent |
| 12 | `sentinel_escalation` | IDLE_TICK（ticker） | queen + 已绑定 colony |
| 13 | `stream_stall` | STREAM_STALLED（reactive，同步 collect） | 所有 agent |

说明：

- `worker-report` **不是** reminder source。上游 worker 的 `[WORKER_REPORT]` 是
  `queen_orchestrator` 注入的 user turn（`queen_lifecycle_tools.py` 的 report 路径），Go 对应
  `chat_colony_bridge.go` 的 relay；两者都不经过 ReminderHub。
- `past_conversations` 与 `turn_context` 的 provider 由 queen_orchestrator 在创建会话时接线
  （`queen_skills_catalog_provider` / `colony_binding_provider` / `active_workers_provider` /
  `colony_stats_provider` / `turn_context_provider`）。

## 3. 注册矩阵

### 3.1 Queen chat 会话（`internal/server/chat_reminders.go`）

| source | before | after | 备注 |
| --- | --- | --- | --- |
| tasks | ✅ | ✅ | 适配 `tasks.TaskReminderSource` |
| colony_parallel | ✅ | ✅ | |
| active_workers | ✅ | ✅ | |
| tracker_snapshot | ✅ | ✅ | |
| colony_worker_snapshot | ❌ | ✅ | 本分支补 |
| searchable_tools | ✅ | ✅ | |
| skills_catalog | ✅ | ✅ | provider 现按会话 queen scope |
| turn_context | ✅ | ✅ | 保持 wiring-check 约束 |
| idle_nudge | ✅ | ✅ | ticker 随会话启停 |
| sentinel_escalation | ✅ | ✅ | `sentinel_runtime.go` 注册 |
| stream_stall | ❌（零注册） | ✅ | 本分支补 + 真实触发 |
| past_conversations | 注入在 hub 外 | 注入在 hub 外 | 形态差异，§7 |

### 3.2 Colony worker（`internal/colony/worker_reminders.go`）

| source | before | after |
| --- | --- | --- |
| tasks | ✅ | ✅ |
| worker_tracker_snapshot | ❌ | ✅（cadence 20 calls，tracker_* 重置，空表不自耗） |
| idle_nudge | ✅ | ✅ |
| stream_stall | ❌ | ✅（注册；worker completer 目前非流式，见 §7） |
| colony_parallel / active_workers / tracker_snapshot / colony_worker_snapshot / skills_catalog | ❌ | ❌（上游 `applies_to` 对 worker 为 False，注册也会被 bind 过滤） |
| searchable_tools | ❌ | ❌（Go worker 无 search_tools tier；上游同样只在 provider 存在时绑定） |

### 3.3 Colony overseer（`internal/colony/overseer_reminders.go`）

上游 `ColonyRuntime.start_overseer` 构造的是完整 `AgentLoop`（13 个 source 全部注册，靠
`applies_to` 过滤）；Go 之前是裸 `&AgentLoop{}`，**hub 为 nil**。本分支为它建立 hub：

| source | before | after | 说明 |
| --- | --- | --- | --- |
| colony_parallel | ❌ | ✅ | 工具表含 `run_worker` 才在 render 阶段放行 |
| active_workers | ❌ | ✅ | provider 接 colony `ListWorkers()` |
| tracker_snapshot | ❌ | ✅ | provider 接 `TrackerBinding()` |
| colony_worker_snapshot | ❌ | ✅ | provider 接 `GetStats()` + active workers |
| stream_stall | ❌ | ✅ | 与 chat/worker 同一 source |
| idle_nudge | ❌ | ❌ | 会话级 idle 由 chat runtime 唯一持有，避免同一会话双 ticker 双唤醒 |
| skills_catalog | ❌ | ❌ | 预载会话首次客户端消息的 relay 已携带 catalog（上游 overseer 也是 prompt 路径） |
| tasks | ❌ | ❌ | task 提醒读进程级 task store，overseer 任务在 colony 存储域；注册会变成空转 |

## 4. 触发 / 抑制边界

### 4.1 STREAM_STALLED（reactor）

- **检测**：`chatStreamingCompleter` 对 conversation 流调用加看门狗。
  - `ttft`：流打开后首个事件前超过 `LLMStreamTTFTTimeoutSeconds`（默认 600s）；
  - `inactive`：首个事件后静默超过 `LLMStreamInterEventIdleSeconds`（默认 120s；
    legacy `LLMStreamInactivityTimeoutSeconds` 低于该值时覆盖，与上游一致）。
  - 只对带 bus 的会话生效（与既有 streaming 条件一致）；任一预算为 0 即关闭该预算，
    两者都 0 完全关闭。
- **判定 → 恢复**：看门狗取消流并返回 `*llm.StreamStalledError`。循环（`loop.go`）在
  AComplete 错误分支调用 `injectStreamStallNudge`：`Hub.CollectWithSignals(STREAM_STALLED,
  LoopSignals{StallReason, StallElapsed})` → 取首个 body → 包 `<system-reminder>` 写入
  conversation → `inner--; continue`（重跑同一 inner turn，不计迭代）。
- **抑制**：`StreamStallSource` 的 `enabled=false`、`maxPerTurn<=0`、或本 turn 已用满 cap
  （默认 3）时不再产出；此时错误按普通 LLM 失败上抛。cap 在 `ExecuteTurn` 开头经
  `ReminderHub.ResetTurn()` 按 turn 重置（对应上游 `_run_turn_loop` 的
  `self._stream_stall_source.reset_turn()`）。
- **会话事件**：循环通过 `OnStreamStallNudge` 钩子把判定交给 chat runtime，后者在会话总线上
  发布 `stream_ttft_exceeded`（ttft）或 `stream_inactive`（inactive）、`stream_nudge_sent`
  与 `reminder_injected`（source=`stream_stall`），与上游 `_run_turn_loop` 的诊断事件一致。
- **可调 env**：`HIVE_LLM_STREAM_TTFT_TIMEOUT_SECONDS`、
  `HIVE_LLM_STREAM_INTER_EVENT_IDLE_SECONDS`、`HIVE_CONTINUE_NUDGE_MAX_PER_TURN`、
  `HIVE_CONTINUE_NUDGE_ENABLED`（上游为 `hive.config.json` 的 `loop.*` 键）。

### 4.2 worker_tracker_snapshot

- 只在 worker（`!IsQueenStream`）且存在 colony binding provider 时绑定；
- 每累计 20 次工具调用（`HIVE_TRACKER_WORKER_TOOL_CALLS`）尝试一次；
- 任一 `tracker_*` 工具调用把计数清零；
- tracker 无表/无行时**不**产出、也**不**消耗 cadence（worker 只 upsert，不建表）；
- 渲染 `Tracker state right now: … rows` + 「先 tracker_upsert 再继续」提示。

### 4.3 colony_worker_snapshot

- 只在 queen + colony binding 时绑定，只在 `TOOL_BUDGET_CHECKPOINT` 触发；
- active/total 都为 0 时静默；最多列 8 个 worker，task 截断到 80 字符；
- 文案含「不要重复派单、优先等 WORKER_REPORT」。

## 5. 会话生命周期（不泄漏）

| 载体 | 启动 | 停止 |
| --- | --- | --- |
| chat hub | `configureReminders` 注册后 `Bind` + `Start`（idle ticker） | `ChatRuntime.Close` → `hub.Stop()`（停 ticker、清 pending） |
| worker hub | `newWorkerAgentLoop` 注册/绑定/`Start`，cleanup 由 `runWorker` defer | `stopHub()` = `hub.Stop()` |
| overseer hub | `runOverseer` 建 loop 后 `startOverseerReminders`（无 ticker） | `defer stopReminders()`（清 pending；无 ticker 可停） |

测试锚定：`TestChatHubRegistersStreamStallAndFleetSources`（Close 后 ticker 停、pending 0）、
`TestOverseerReminderTeardownClearsPending`、`TestHubResetTurnReachesConsultedSourcesOnly`。

## 6. 预载 colony 的 queen scope

修复前 `loadPreloadedColonySession` 固定 `resolvePreloadedColonySession(home, colonyID, "default")`，
导致会话目录、记忆与 skills catalog 都落在 default queen 下，而 overseer 已按
`colonyQueenName()` 的 queen 运行。

修复后（`internal/cli/loader_preloaded_session.go`）：

1. `queenName := colonyQueenName(home, colonyID)`（读 colony `metadata.json` 的 `queen_name`）；
2. `resolvePreloadedColonySession(home, colonyID, queenName)` → 会话目录
   `colonies/<id>/queens/<queen>/sessions/<sid>` 或 `queens/<queen>/sessions/<sid>`；
3. `record.QueenName = queenName` → `ChatRuntimeDeps.QueenName` → 记忆目录与
   `liveSkillsCatalog(..., chatSessionQueenScope(record.QueenName))`；
4. 无 `queen_name` 的旧 colony 回退 `default`（`chatSessionQueenScope` 的既有语义）。

测试：`TestPreloadedColonySessionInjectsSkillsCatalog`（queen scope 的 catalog 出现、
default scope 的 probe 不出现）、`TestPreloadedColonySessionRestartReusesID`（会话目录与
`QueenName` 均按 colony queen）。

## 7. 残余 / 未实现项（显式登记）

1. **past_conversations 未搬进 hub**：Go 在 `runTurn` 里对客户端输入调用
   `injectPastConversationRecall`（USER_PROMPT_SUBMIT 等价时机），行为与上游 source 对齐，
   但不在 ReminderHub 注册矩阵里；SESSION_START 时不会重复召回（上游会两个 point 都触发）。
2. **worker 的 searchable_tools**：Go worker 没有分层工具表（无 `search_tools`），上游的
   `applies_to` 也只在 provider 存在时绑定，因此不注册；若将来给 worker 加 tier，需要同时接线
   `SearchableToolsProvider`。
3. **overseer 无 idle ticker**：同一会话已有 chat runtime 的 idle ticker 并把唤醒 relay 给
   overseer；再加一条会双唤醒。上游 overseer 即会话 queen，只有一条 ticker。
4. **overseer 的 skills catalog**：走预载会话首次客户端消息的 relay（与上游 overseer 的
   prompt 路径对应），不在其 hub 重复注册，避免同一 catalog 两次公告。
5. **worker/overseer 的 STREAM_STALLED 触发面**：source 已注册且 recovery 路径与 queen 相同，
   但 worker/overseer 的 completer 目前没有流式适配器，因此该点只在 chat queen 会话上真实
   触发；一旦 worker/overseer 接流式传输，触发面自动打开（无需再改接线）。
6. **tasks 源在 worker/overseer**：worker 的 task 工具用 worker 私有 task store，而
   `TaskReminderSource` 读进程级 store；overseer 同理（`overseerTaskSet` 在 colony 存储域）。
   现状注册但实际自跳过，属既有行为，未在本分支改变。

## 8. 测试与真实触发证据

| 测试 | 证明 |
| --- | --- |
| `TestStreamStallNudgeRecoversTheTurn` | stall → 注入 → 同一 inner turn 重试；`stream_stall_nudged=1` |
| `TestStreamStallNudgeCapSurfacesTheError` / `TestStreamStallCapResetsPerTurn` | cap 抑制与按 turn 重置 |
| `TestStreamWatchdogTripsOnTTFT` / `...OnInactivityAfterFirstEvent` | 两种看门狗判定与 reason 标签 |
| `TestChatRuntimeStreamStallReminderReachesTheModel` | **真实 ChatRuntime 会话**：Deliver → 流 stall → 重试请求携带 `<system-reminder>` 与 TTFT 文案；总线收到 `stream_ttft_exceeded` / `stream_nudge_sent` / `reminder_injected` |
| `TestChatHubRegistersStreamStallAndFleetSources` | chat hub 注册矩阵 + Close 不泄漏 |
| `TestWorkerAgentLoopRegistersWorkerRoleSources` | worker hub 注册矩阵（无 queen-only 源） |
| `TestWorkerTrackerSnapshotReachesARealWorkerTurn` | **真实 SpawnBatch worker**：tracker 有 1 行 → 工具调用后 worker 转录出现 tracker 提醒 |
| `TestOverseerReminderWiringRegistersQueenSources` | overseer hub 5 个 queen-role 源 + 无 ticker |
| `TestColonyWorkerSnapshotGatesAndBody` / `TestWorkerTrackerSnapshotCadenceAndGates` | 触发/抑制边界 |
| `TestPreloadedColonySessionInjectsSkillsCatalog` / `...RestartReusesID` | queen scope 解析 |

复现：

```bash
cd go_hive/go-hive
GOPROXY=off go test ./internal/agentloop ./internal/colony ./internal/server ./internal/cli -count=1
make ci
```

## 9. 代码索引

| 文件 | 内容 |
| --- | --- |
| `internal/agentloop/loop_config.go` | `LoopConfig`（从 `loop.go` 拆出，含 stream watchdog / continue-nudge 字段） |
| `internal/agentloop/loop.go` | 每 turn `Hub.ResetTurn()`；AComplete 错误分支接 `injectStreamStallNudge` |
| `internal/agentloop/stream_stall_recovery.go` | collect → 注入 → 重试 的恢复实现 |
| `internal/agentloop/stream_stall.go` | `StreamStallSource`（既有，现被注册） |
| `internal/agentloop/reminder_hub.go` | `TurnResetter` 接口与 `ResetTurn` |
| `internal/agentloop/tracker_snapshot_reminder.go` | 新增 `WorkerTrackerSnapshotReminderSource` |
| `internal/agentloop/colony_worker_snapshot_reminder.go` | 新增 queen fleet 快照 source |
| `internal/server/chat_stream.go` | 流看门狗 + `StreamStalledError` 判定 |
| `internal/server/chat_reminders.go` | chat hub 注册 stream_stall / colony_worker_snapshot |
| `internal/server/chat_memory_wiring.go` | `chatLoopConfig` 的 env 覆盖 |
| `internal/colony/worker_reminders.go` | worker hub 新源 + binding/stats provider |
| `internal/colony/overseer_reminders.go` | overseer hub（queen role 源） |
| `internal/cli/loader_preloaded_session.go` | 预载 colony 的 queen scope |

（`docs/support-matrix.md` 的「提醒中枢」一行同步更新。）
