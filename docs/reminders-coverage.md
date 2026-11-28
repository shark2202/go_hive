# Reminders 覆盖对齐说明（source 矩阵 / 生命周期 / 预载 colony queen scope）

> 可视化资料：[reminders-and-nudges.html](reminders-and-nudges.html)（点-线-面：15 源、9 注入点、8 条链路、9 个维度、Go 15/15 行为对齐；生成器 `gen_reminders_and_nudges.py`）

本文回答 `queen-worker-colony-parity-audit.html` 维度 #12（Reminders / idle tick）与 GAP-06 的
修复情况：Go 侧 reminder source 的注册矩阵、触发/抑制边界、会话启停生命周期，以及预载
colony 会话的 queen scope 解析。基线：`main@46939ab5` 之上的 `reminders-coverage` 分支；
上游参考为 `core/framework`（hive 1.0.14）。**2026-11-21 在本工作树（main@c4eb266d）逐条
复核了生产代码并做了真实 serve 实测（§8.1），其中 past_conversations 已在 `dfbf65b2`
进 chat ReminderHub（SESSION_START / USER_PROMPT_SUBMIT 双 point）；审查记录见
`docs/e2e-assets/reminders/2026-11-21-audit.md`。**

## 1. 结论

- `STREAM_STALLED` 从「有 source、零注册、零触发」变为**注册 + 真实触发**：流式补全的
  TTFT / 事件间静默看门狗在 `internal/server/chat_stream.go` 里判定，循环把判定转成
  `<system-reminder>` 并**重跑同一 inner turn**（上游 continue-nudge 语义）。
- worker hub 从 task + idle 扩到 **task + worker_tracker_snapshot + idle + stream_stall**；
  overseer loop 从 **nil hub** 变成 queen role 的 6 个 source（parallel / active_workers /
  tracker / fleet / tasks / stream_stall）。
- queen chat hub 补入 `colony_worker_snapshot`（tool-budget checkpoint 的 fleet 快照）。
- 预载 colony 会话不再固定 `default` queen scope：从 colony `metadata.json` 的 `queen_name`
  解析（与上游 `_create_session_for_existing_colony` 一致），会话目录、记忆目录与 skills
  catalog 一并按该 queen 落位。
- **2026-11-21 修正**：`past_conversations` 曾在 hub 外直接注入，注入顺序已对齐上游
  `USER_PROMPT_SUBMIT`（提醒帧在用户消息**之前**）并改用共享 `WrapReminder` 包装。
  **2026-11-23 `dfbf65b2` 已把它搬进 chat ReminderHub**：`SESSION_START` 与
  `USER_PROMPT_SUBMIT` 两个 point 都触发，`PastConversationRecallProvider` 由
  `chat_reminders.go` 接线；`chat_past_conversations_test.go` 覆盖两个 point。此项已关闭。
- **2026-11-22 计数修正**：上游注册数 **13 → 15**，补 `memory_recall` / `memory_index`
  （两者均在 `chat_reminders.go:221-222` 注册，provider 由 `queen_orchestrator.py:1722-1723`
  接线）；`ReminderPoint` 共 **9** 个（含 `STREAM_STALLED`）。
- **2026-11-23 复核（main@2ea9caa6）**：15/15 个 source 类型都已实现；按行为面计
  **12 个完全对齐、3 个有条件差异**（`tasks`、`stream_stall`、`idle_nudge`，见 §7），
  不是 15/15 逐字一致。Go 四个包 **2,422 tests PASS**；Python 8 个 reminder 测试文件
  **155 passed**。
- **2026-11-23 17:18 复核（main@d403b0a6）**：`w86/stream-stall` 把 worker/overseer 的
  completer 接到共享 `StreamingCompleter`（TTFT 600s / inter-event 120s 看门狗），
  `stream_stall` 已从“仅 chat queen 真触发”变为 worker/overseer 也走同一 recovery 路径；
  行为面计 **13 个完全对齐、2 个有条件差异**（`tasks`、`idle_nudge`，见 §7）。
  全量 CI **5,976 tests / 6,685 refs / 0 missing**；Python reminder 155 仍 PASS。
- **2026-11-23 `w87/reminders-residual` 收尾**：两条残余均已关闭。worker 的 task source 绑定其
  私有 store（`workerDir` / `workerID`），overseer 的 task source 绑定 `overseerTaskSet`
  （`<colony>/_misc/overseer`）；worker 的 reminder context 接上 `OpenTasksProvider` /
  `SessionGoalProvider`，`parked_open_tasks` 与 goal 前缀按上游语义工作。行为面计
  **15 个完全对齐、0 个有条件差异**；旧代码上 worker task snapshot / worker real turn、worker
  idle open-task、overseer hub / real turn 相关行为测试均红，修复后绿。目标包
  `go test ./internal/colony ./internal/agentloop ./internal/tasks` PASS（测试提交 `edd40f3d`）。

## 2. 上游 source 全量清单

`core/framework/agent_loop/agent_loop.py::AgentLoop.__init__` 在**每个** AgentLoop 的
`ReminderHub` 上注册 15 个 source，再由各 source 的 `applies_to` 按角色/阶段过滤：

| # | source | points | applies_to（上游） |
| --- | --- | --- | --- |
| 1 | `tasks` | SESSION_START, USER_PROMPT_SUBMIT, POST_TOOL_USE, POST_COMPACT, STOP | 工具表含 task 写工具 |
| 2 | `colony_parallel` | POST_TOOL_USE | queen |
| 3 | `active_workers` | USER_PROMPT_SUBMIT | queen |
| 4 | `past_conversations` | SESSION_START, USER_PROMPT_SUBMIT | queen + recall provider |
| 5 | `memory_recall` | SESSION_START, USER_PROMPT_SUBMIT, POST_COMPACT | queen + recall provider |
| 6 | `memory_index` | SESSION_START, USER_PROMPT_SUBMIT, POST_COMPACT | queen + memory index provider |
| 7 | `turn_context` | SESSION_START, USER_PROMPT_SUBMIT | queen + turn-context provider |
| 8 | `searchable_tools` | SESSION_START, USER_PROMPT_SUBMIT, POST_TOOL_USE, POST_COMPACT | queen，或自带 provider 的分层 worker |
| 9 | `skills_catalog` | 同上 | queen |
| 10 | `tracker_snapshot` | POST_TOOL_USE | queen + colony binding provider |
| 11 | `worker_tracker_snapshot` | POST_TOOL_USE | **worker** + colony binding provider |
| 12 | `colony_worker_snapshot` | TOOL_BUDGET_CHECKPOINT | queen + colony binding provider |
| 13 | `idle_nudge` | IDLE_TICK（ticker） | 所有 agent |
| 14 | `sentinel_escalation` | IDLE_TICK（ticker） | queen + 已绑定 colony |
| 15 | `stream_stall` | STREAM_STALLED（reactive，同步 collect） | 所有 agent |

说明：

- `worker-report` **不是** reminder source。上游 worker 的 `[WORKER_REPORT]` 是
  `queen_orchestrator` 注入的 user turn（`queen_lifecycle_tools.py` 的 report 路径），Go 对应
  `chat_colony_bridge.go` 的 relay；两者都不经过 ReminderHub。
- `past_conversations` 与 `turn_context` 的 provider 由 queen_orchestrator 在创建会话时接线
  （`queen_skills_catalog_provider` / `colony_binding_provider` / `active_workers_provider` /
  `colony_stats_provider` / `turn_context_provider`）。
- `sentinel_escalation` 的 `applies_to` 还额外要求该 colony 在 notifications config 里
  opt-in（`core/framework/sentinel/escalation_source.py:101-111`）；文档原表只写到
  「queen + 已绑定 colony」。

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
| memory_index | ✅ | ✅ | 既有注册（本次补记）；provider 按会话 queen scope |
| memory_recall | ✅ | ✅ | 既有注册（本次补记）；`memory_recall.mode` 决定 index/wait |
| idle_nudge | ✅ | ✅ | ticker 随会话启停 |
| sentinel_escalation | ✅ | ✅ | `sentinel_runtime.go` 注册 |
| stream_stall | ❌（零注册） | ✅ | 本分支补 + 真实触发 |
| past_conversations | 注入在 hub 外 | ✅ 已进 hub（dfbf65b2） | 2026-11-23 起 `SESSION_START` / `USER_PROMPT_SUBMIT` 两个 point 都触发；provider 由 `chat_reminders.go` 接线 |

### 3.2 Colony worker（`internal/colony/worker_reminders.go`）

| source | before | after |
| --- | --- | --- |
| tasks | ✅ | ✅（绑定 worker 私有 store：`workerDir` + `workerID`，与 `task_*` 工具同 scope；§7-5 已关闭） |
| worker_tracker_snapshot | ❌ | ✅（cadence 20 calls，tracker_* 重置，空表不自耗） |
| idle_nudge | ✅ | ✅（worker context 已接 `OpenTasksProvider` / `SessionGoalProvider`，`parked_open_tasks` 与 goal 前缀可触发；§7-6 已关闭） |
| stream_stall | ❌ | ✅（worker completer 已接 `StreamingCompleter`，TTFT/idle 看门狗真实触发，见 §7） |
| colony_parallel / active_workers / tracker_snapshot / colony_worker_snapshot / skills_catalog / turn_context / memory_recall / memory_index | ❌ | ❌（上游 `applies_to` 对 worker 为 False，注册也会被 bind 过滤） |
| searchable_tools | ❌ | ❌（Go worker 无 search_tools tier；上游同样只在 provider 存在时绑定） |

### 3.3 Colony overseer（`internal/colony/overseer_reminders.go`）

上游 `ColonyRuntime.start_overseer` 构造的是完整 `AgentLoop`（15 个 source 全部注册，靠
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
| tasks | ❌ | ✅ | 绑定 colony 存储域的 `overseerTaskSet`（`<colony>/_misc/overseer`），与 overseer 的 `task_*` 工具同 scope |
| memory_recall / memory_index | ❌ | ❌ | 上游需 memory provider；overseer 无 recall/index provider，注册也会自跳过 |

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

1. **worker 的 searchable_tools**：Go worker 没有分层工具表（无 `search_tools`），上游的
   `applies_to` 也只在 provider 存在时绑定，因此不注册；若将来给 worker 加 tier，需要同时接线
   `SearchableToolsProvider`。
2. **overseer 无 idle ticker**：同一会话已有 chat runtime 的 idle ticker 并把唤醒 relay 给
   overseer；再加一条会双唤醒。上游 overseer 即会话 queen，只有一条 ticker。
3. **overseer 的 skills catalog**：走预载会话首次客户端消息的 relay（与上游 overseer 的
   prompt 路径对应），不在其 hub 重复注册，避免同一 catalog 两次公告。
4. ~~**worker/overseer 的 STREAM_STALLED 触发面**：source 已注册且 recovery 路径与 queen 相同，
   但 worker/overseer 的 completer 目前没有流式适配器，因此该点只在 chat queen 会话上真实
   触发；一旦 worker/overseer 接流式传输，触发面自动打开（无需再改接线）。~~
   **已关闭（2026-11-23，`w86/stream-stall`）**：worker/overseer 的 `AgentContext.LLM`
   现由 `internal/colony/stream_wiring.go` 包装共享 `agentloop.StreamingCompleter`，
   worker/overseer 的 `LoopSignals` 读取适配器的 live `StreamActive` / `FirstEventSeen`；
   fake streaming provider 的生产路径测试证明 TTFT stall → `<system-reminder>` → 同 inner
   turn 重试 → cap 抑制。真实网络 worker/overseer 的 serve E2E 仍属外部验证项，不以 fake
   冒充。
5. ~~**tasks 源在 worker/overseer**：worker 的 task 工具写 `workerDir` 下的私有 store，
   session id 是 `workerID`；而 `TaskReminderSource` 读进程级 `GetTaskStore()`，session id 是
   `<colony>/<workerID>`。所以 worker hub 虽注册 `tasks`，`Render` 实际自跳过，属**无效注册**。
   overseer 的 `overseerTaskSet` 在 colony 存储域，且 `tasks` **未注册**（见 §3.3），属
   **未实现**。~~ **已关闭（2026-11-23，`w87/reminders-residual`）**：`TaskReminderSource` 新增
   `NewTaskReminderSourceFor(store, sessionID)`，worker source 绑定 `workerDir` + `workerID`，
   overseer source 绑定 `overseerTaskSet` 的 colony 存储域；两边与各自的 `task_*` 工具同
   scope。`TestWorkerTaskReminderRendersTheWorkersOwnPlan` /
   `TestWorkerTaskUpdateReminderRendersForTheWorkerSession` /
   `TestWorkerTaskReminderReachesARealWorkerTurn` / `TestOverseerTaskReminderReadsTheOverseerPlan` /
   `TestOverseerTaskReminderReachesARealOverseerTurn` 覆盖；旧代码分别红在空 snapshot、无
   `tasks` active source、真实首请求无 `<system-reminder>`。
6. ~~**worker 的 idle_nudge open-task 变体**：worker 的 `ReminderAgentContext` 只接了
   `ColonyBindingProvider`，没有 `OpenTasksProvider` / `SessionGoalProvider`。因此
   `parked_open_tasks` 永远看不到 worker 的任务列表；`TURN_DONE` + open tasks 会被
   `turn_done_idle` 抑制，goal 前缀也缺失。chat 侧 provider 已接，worker 侧未接；这是
   **注册了但变体不工作**，不是完全对齐。~~ **已关闭（2026-11-23，`w87/reminders-residual`）**：
   `attachWorkerTaskReminderProviders` 在 worker 的 `AgentContext` 上装 `OpenTasksProvider` /
   `SessionGoalProvider`，读同一个 `workerDir` / `workerID` store；ticker 的
   `ReminderAgentContext` 也由 `workerReminderContext` 接同一对 provider。
   `TestWorkerLoopInstallsTaskReminderProvidersOnTheTurnContext` 与
   `TestWorkerIdleNudgeParkedOpenTasksCarriesGoalAndNamesTasks` 覆盖；旧代码红在 provider 为
   nil、nudge 退化为 `parked_no_question`。`TURN_DONE` + 无 open task 仍按上游
   `is_silent_park` 保持安静。
7. ~~**用户 Stop / `user_stopped`（2026-11-25 新发现，非 hub source）**：`/api/sessions/{id}/cancel-queen`
   依赖 `CancelCurrentTurn` / `MarkUserStopped` 类型断言，但生产注册的 queen node 是
   `*ChatRuntime`（`internal/cli/loader_session_runtime.go:122-124`），其方法集没有这两个方法，
   Stop 实际是空操作（响应 `cancelled` 恒为 `false`），`idle_nudge` 的 `gate=user_stopped` 永不
   触发；路由测试用实现接口的 fake node，未覆盖真实装配。~~
   **已关闭（2026-11-25，`w147`，提交 `3425aecf6`，合并 `b5fc7ac23`）**：`*ChatRuntime` 实现
   `CancelCurrentTurn` / `MarkUserStopped` 并引入 per-turn 取消句柄——Stop 返回
   `{"cancelled": true, "workers": ...}`，只取消在飞 turn（`turnMu` + 序号，旧取消不会打到后续
   turn），被 Stop 的 turn 以 `user_stopped` park + `client_input_requested` 收尾，不再发布
   `execution_failed`；`user_stopped` 闸门自下一个 tick 起抑制 idle nudge，且只有下一次真实
   客户端输入才清除（对齐上游 `inject_event`）。回归以真实 `ChatRuntime` + 阻塞模型注册为
   queen node 驱动 `POST /cancel-queen`：`TestCancelQueenStopsRealChatRuntimeTurn`、
   `TestCancelQueenKeepsQueuedTurnRunning`、`TestCancelCurrentTurnWithoutInflightTurn`
   （`internal/server/chat_turn_cancel_test.go`）。`user_stopped` 的 cursor 持久化/冷启动恢复
   由提交 `53fd28042`（合并 `049738d80`）补齐。详见
   `docs/reports/park-reminder-budget-audit-20261125.md`（P0 已闭环；P2 设计差异与 P3 验证边界
   仍按该报告开放）。

## 8. 测试与真实触发证据

| 测试 | 证明 |
| --- | --- |
| `TestStreamStallNudgeRecoversTheTurn` | stall → 注入 → 同一 inner turn 重试；`stream_stall_nudged=1` |
| `TestStreamStallNudgeCapSurfacesTheError` / `TestStreamStallCapResetsPerTurn` | cap 抑制与按 turn 重置 |
| `TestStreamWatchdogTripsOnTTFT` / `...OnInactivityAfterFirstEvent` | 两种看门狗判定与 reason 标签 |
| `TestChatRuntimeStreamStallReminderReachesTheModel` | **真实 ChatRuntime 会话**：Deliver → 流 stall → 重试请求携带 `<system-reminder>` 与 TTFT 文案；总线收到 `stream_ttft_exceeded` / `stream_nudge_sent` / `reminder_injected` |
| `TestChatHubRegistersStreamStallAndFleetSources` | chat hub 注册矩阵 + Close 不泄漏 |
| `TestWorkerAgentLoopRegistersWorkerRoleSources` | worker hub 注册矩阵（无 queen-only 源） |
| `TestWorkerTaskReminderRendersTheWorkersOwnPlan` | worker `SESSION_START` snapshot 读自己的 store；旧代码为空 |
| `TestWorkerTaskUpdateReminderRendersForTheWorkerSession` | worker `POST_TOOL_USE` drift/goal 读 worker session |
| `TestWorkerTaskReminderReachesARealWorkerTurn` | **真实 SpawnBatch worker**：`task_create` 后 worker 转录出现任务 snapshot |
| `TestWorkerLoopInstallsTaskReminderProvidersOnTheTurnContext` | worker `AgentContext` 带 `workerID` + `OpenTasksProvider` / `SessionGoalProvider` |
| `TestWorkerIdleNudgeParkedOpenTasksCarriesGoalAndNamesTasks` | worker `parked_open_tasks` body 含 goal 前缀并点名 open tasks；旧代码退化为 `parked_no_question` |
| `TestOverseerTaskReminderReadsTheOverseerPlan` | overseer hub 的 `tasks` active source 读 `overseerTaskSet` |
| `TestOverseerTaskReminderReachesARealOverseerTurn` | **真实 `StartOverseer` loop**：首个模型请求携带 overseer 计划 snapshot |
| `TestWorkerTrackerSnapshotReachesARealWorkerTurn` | **真实 SpawnBatch worker**：tracker 有 1 行 → 工具调用后 worker 转录出现 tracker 提醒 |
| `TestWorkerLoopStreamStallTripsAndRecovers` | **生产 worker loop + fake streaming provider**：TTFT stall → `<system-reminder>` → 同 inner turn 重试；live `StreamActive`/`FirstEventSeen` |
| `TestOverseerLoopStreamStallTripsAndRecovers` | **生产 overseer loop + fake streaming provider**：同一 recovery 路径；非流式 provider 保持不包装 |
| `TestOverseerReminderWiringRegistersQueenSources` | overseer hub 6 个 queen-role 源（含 tasks）+ 无 ticker |
| `TestColonyWorkerSnapshotGatesAndBody` / `TestWorkerTrackerSnapshotCadenceAndGates` | 触发/抑制边界 |
| `TestPreloadedColonySessionInjectsSkillsCatalog` / `...RestartReusesID` | queen scope 解析 |

上表的“真实 worker / overseer”指生产 `SpawnBatch` / `StartOverseer` + 真实 `AgentLoop` 与持久化
task store；模型是脚本化 provider。真实网络 `hive serve` 下的 worker/overseer `tasks` 与
`idle_nudge` 触发仍属外部验证项，不以 in-process 测试冒充 serve E2E。

### 8.1 真实 serve 实测（2026-11-21，无假组件）

真实 `hive serve`（scratch HIVE_HOME + 真实 llm 槽 tokenbroad/deepseek-flash-1017）：

| 项 | 实测结果 | 证据 |
| --- | --- | --- |
| STREAM_STALLED 触发 | `HIVE_LLM_STREAM_TTFT_TIMEOUT_SECONDS=0.002` 下真实会话事件：`stream_ttft_exceeded` → `stream_nudge_sent(nudge_count=1)` → `reminder_injected(stream_stall)` → 重试仍 stall → `execution_failed`；会话 parts 含注入的 `<system-reminder>The previous stream stalled ...` | `docs/e2e-assets/reminders/stream-stall/trigger-events.txt`、`trigger-conversation-parts.txt` |
| STREAM_STALLED 抑制（cap / enabled=false） | 首 stall 用满 `HIVE_CONTINUE_NUDGE_MAX_PER_TURN=1` 后第二次不再注入；`HIVE_CONTINUE_NUDGE_ENABLED=false` 时完全无 `stream_*`/`reminder_injected(stream_stall)` 事件 | 同上 + `suppressed-events.txt` |
| worker_tracker_snapshot 注入 | 真实 `run_worker` 派生的 SpawnBatch worker，第一次工具批次后工具结果尾部出现 `Tracker state right now: - notes: 1 row ... tracker_upsert` | `docs/e2e-assets/reminders/worker-tracker/` |
| past_conversations 位置 | 已进 hub（dfbf65b2）：`SESSION_START` / `USER_PROMPT_SUBMIT` 两个 point 都触发；包装与上游 `wrap_reminder` 相同 | `docs/e2e-assets/reminders/past-conversations/before-fix-parts.txt` / `after-fix-parts.txt` |

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
| `internal/server/chat_stream.go` | 流看门狗 + `StreamStalledError` 判定；watchdog 预算委托 `agentloop.StreamWatchdogBudgets` |
| `internal/agentloop/streaming_completer.go` | worker/overseer 共享流式 watchdog 适配器（live signals、fallback、prompt scope） |
| `internal/colony/stream_wiring.go` | worker/overseer `AgentContext.LLM` 包装与 `LoopSignals` live 状态叠加 |
| `internal/server/chat_reminders.go` | chat hub 注册 stream_stall / colony_worker_snapshot |
| `internal/server/chat_memory_wiring.go` | `chatLoopConfig` 的 env 覆盖 |
| `internal/colony/worker_reminders.go` | worker hub + worker 私有 task source / idle providers |
| `internal/colony/overseer_reminders.go` | overseer hub（queen role 源含 colony 存储域 tasks） |
| `internal/colony/worker_task_tools.go` | worker 私有 task store（`workerDir` / `workerID`） |
| `internal/colony/overseer_tools.go` | `overseerTaskSet`（colony 存储域 / `overseer` session） |
| `internal/cli/loader_preloaded_session.go` | 预载 colony 的 queen scope |

（`docs/support-matrix.md` 的「提醒中枢」一行同步更新。）
