# Go 运行时 × Key Concepts 接线审计

- 审计基线：`af55d368`（`docs: 纠正交付口径——工程可发布≠行为对齐，功能对齐标为不满足`）
- 上一版基线：`e5f7c2fa`（merge w15/queen-memory）
- 审计日期：2026-11-19
- 审计对象：`docs/key_concepts/` 下 7 篇（colony、coordination、goals_outcome、improvement、queen、the_loop、worker_agent）
- 判定口径（严格）：
  - **已接**＝从 `cmd/hive` → `internal/cli` 或 `internal/server` 的 HTTP 路由真实可达，且在生产路径上生效；仅测试调用不算。
  - **部分**＝有真实入口但只覆盖概念的一部分，或只读不写。
  - **未接**＝代码存在但生产入口走不到（死代码/仅测试/只有枚举与名字）。
- 说明：只读审计，不改产品代码、不改测试、不改 `go.mod`。行号以 `af55d368` 为准；Go 模块根为 `go_hive/go-hive/`。
- 计数口径：一行＝一个可独立判定的机制。部分行由上一版合并行拆分而来，故总数与本表结构相关，跨版本比较请看第 10 节逐条差异，不要直接比总数。

## 汇总

| 概念 | 已接 | 部分 | 未接 | 合计 |
| --- | --- | --- | --- | --- |
| colony | 5 | 4 | 3 | 12 |
| coordination | 3 | 2 | 6 | 11 |
| goals_outcome | 1 | 1 | 7 | 9 |
| improvement | 3 | 0 | 5 | 8 |
| queen | 7 | 1 | 5 | 13 |
| the_loop | 6 | 4 | 5 | 15 |
| worker_agent | 0 | 0 | 11 | 11 |
| **合计** | **25** | **12** | **42** | **79** |

---

## 1. colony — 殖民地即部署单元

概念主张：colony 是单目录、可移植、可定时唤醒、跨会话持久的部署单元；Queen 与 worker 是同一个 loop；Queen 通过 `run_worker` 在运行时按需扩展；Create Colony 对话框确认后才 fork。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 缺口 |
| --- | --- | --- | --- | --- |
| 单目录 `~/.hive/colonies/<name>/` + scaffold | `internal/hivehome/home.go`（`ColoniesDir`）、`internal/server/routes_colonies.go:409`（`handleScaffoldColony`）、`:440`（`ensureMinimalColony`） | `POST /api/colonies/{colony_id}/scaffold`（`routes_colonies.go:59`）→ `handleScaffoldColony` → 只写 `metadata.json`、`worker.json` | 部分 | scaffold 不建 `tracker/`、`skills/`、`triggers.json`、任务计划；fork 路径 `internal/cli/loader.go:940`（`writeColonyFromSession`）同样只写两个 JSON |
| 单目录可移植 / tar 导入 | `internal/server/routes_colonies.go:41-56`、`internal/server/routes_colonies_import.go:705`（`handleImportColony`） | `POST /api/colonies/import`、`/import/init`、`/import/chunk/*`、`/import/finalize/*` → 落盘 `home.ColoniesDir()` | 已接 | — |
| Queen 与 worker 是同一个 agent loop | `internal/colony/runtime.go:860`（`runOverseer`）、`:875`（内置 `agentloop.AgentLoop`）、`:794`（`runWorker`，worker 克隆） | `hive serve --colony` → `cli/loader.go:312`（`buildColonyHost`）→ `:317`（`StartOverseer`）；load/fork → `loader.go:483`（`startColonyHost`）→ `StartOverseer`（`internal/colony/runtime.go:602`） | 部分 | 生产只跑 Queen overseer：注入 "Hello" 跑完**一轮**后阻塞在 `<-ctx.Done()`（`runtime.go:879-884`），之后不再消费输入；worker 克隆（`runWorker`）没有生产入口 |
| Queen 用 `run_worker` 在运行时扩展 | 无工具实现；`internal/colony/runtime.go:485`（`SpawnBatchWithOptions`）无生产调用；仅有名字痕迹：`internal/playbook/rows.go:279`、`internal/agentloop/colony_parallel_nudge.go:35` | 无 | 未接 | 会话工具表（`internal/cli/session_tools.go:46-90`）与 colony worker 工具表（`internal/colony/runtime.go:699`）都没有 `run_worker`；HTTP 也没有 worker spawn 路由（只有 list/get/stop/stream） |
| Create Colony 对话框 → fork 出 colony | `internal/server/routes_session_create.go:212`（分支）、`:238`（要求 seam）、`:245`（调用）；`internal/cli/loader.go:796`（`CreateColonyFromSource`）、`:940`（写目录） | `POST /api/sessions`（`colony_id` + `source_session_id`）→ `deps.CreateColonyFromSource`（`loader.go:343` 注入）→ 写 colony → `newColonyHost` + `startColonyHost` → 建立绑定会话 | 已接 | fork 出的 colony 只有 `metadata.json`/`worker.json`；runs/workers 为空；`ColonySpawned` 锁在 `loader.go:909` |
| colony 生命周期 load / unload | `internal/server/routes_session_colony.go:55`（`handleLoadColony`）、`:170`（`handleUnloadColony`）；`internal/cli/loader.go:723`（`LoadColony`）、`:742`（`UnloadColony`）、`:760`（`attach`） | `POST /api/sessions/{id}/colony`（`routes_sessions.go:114`）/ `DELETE /api/sessions/{id}/colony`（`routes_sessions.go:69`）→ `deps.Colony`（`loader.go:342`）→ 建 runtime、`StartOverseer`、替换/停掉旧 runtime | 已接 | 失败已按 4xx/5xx 上报（不再是 500 空分支） |
| `serve --colony` 启动即加载 runtime 与 overseer | `internal/cli/loader.go:293-326`、`:427`（`buildColonyHost`）、`:444`（`newColonyHost`） | `hive serve --colony <path>` → `defaultServe` → `record.Colony = buildColonyHost(...)` → `StartOverseer` | 已接 | 每次启动新建 session id（`loader.go:300`），不恢复旧会话/旧 worker |
| tracker 作为 colony 的共享账本 | `internal/tracker/tracker.go:234`（`EnsureTrackerDB`）、`internal/server/routes_colony_workers_data.go:134`、`:790`（`handleColonyUpdateRow`）；`internal/colony/runtime.go:332-339` | `GET /api/colonies/{id}/data/tables`、`.../data/rows`、`PATCH .../rows` → `EnsureTrackerDB`（读时建库）/ `updateTableRow`；UI 可改行 | 部分 | 只有读 + UI 改行；agent 没有写工具；`ColonyRuntime.Start`（`runtime.go:417`）不建库；`SpawnBatch` 无入口，binding 不会注入 |
| colony 内 `skills/`（Queen 写、worker 加载） | `internal/skills/skill_writer.go:169`（`WriteSkill`）、`internal/skills/manager.go:48`（`NewSkillsManager`）、`internal/host/host.go:50`（`SkillsProvider`）、`internal/colony/worker_surface.go:246` | 无 | 未接 | `WriteSkill`、`NewSkillsManager`、`SkillsProvider` 全零生产调用；`newColonyHost`（`loader.go:458-472`）不设 `SkillsProvider`，`GET /colony/skills`（`routes_colony_workers.go:571`）恒为空 |
| 任务计划（Queen 的 spine） | `internal/tasks/session_tools.go:747`（`BuildSessionTools`）、`internal/cli/task_tools.go:47`（`newTaskToolSet`）、`internal/cli/session_tools.go:62,70` | `hive serve` → `createSessionWithRuntime` → `newSessionToolCatalogue` → `task_create/task_update/task_list/task_get`；每次调用 `tasks.SetExecContext`（`task_tools.go:116`） | 已接 | 只有 chat Queen 有；colony overseer 与 worker 工具表都没有 `task_*`；任务存放在 `HIVE_HOME` 会话树，不在 colony 目录 |
| cron triggers 定时唤醒 colony | `internal/server/trigger_runtime.go:291`（`StartTriggerTimer`）、`internal/triggers/triggers_store.go:20`（`ReadAgentTriggersJSON`） | `POST/PATCH /api/sessions/{id}/triggers*` → `StartTriggerTimer`，但前置 `session.QueenExecutor != nil`（`trigger_runtime.go:46`） | 未接 | `QueenExecutor` 生产代码零赋值；启动不扫描 `triggers.json`；定时器从不启动 |
| colony 跨会话持久 / 重启恢复 | `internal/colony/resume.go:11`（`IterQueenSessionDirs`）、`:42`（`ColonyIDForSessionDir`）、`:57`（`DeriveResumeColonyID`）；`internal/host/host.go:120`（`Start`） | 无 | 部分 | colony 目录持久；但 runs/workers/对话不落盘恢复，`resume.go` 三函数零生产调用，重启后是新的空 session；`Host.Start` 只起内存 context，不恢复 runs/workers |

**小结**：colony 的磁盘面（目录、tar 导入）与生命周期面（load/unload、Create Colony fork、`serve --colony` 启动 overseer）已经真接；但 colony 内部的协作面（worker 派发、`run_worker`、tracker 写、skills、triggers、重启恢复）仍未接。当前 colony 是一个"能加载、能起一个 Queen 空转一轮"的部署单元，不是一个能 fan-out 的运行时。

## 2. coordination — tracker、任务计划、事件总线、提醒中枢

概念主张：colony 没有图；Queen 与 worker 靠四个轻量底座同步——共享 tracker、持久任务计划、事件总线、提醒中枢；人机协同靠 Queen 提问停车 + Sentinel 兜底。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 缺口 |
| --- | --- | --- | --- | --- |
| tracker 作为每 colony 的 SQLite 共享账本（读侧） | `internal/tracker/tracker.go:234`、`internal/server/routes_colony_workers_data.go:134`、`:912-921` | `GET /api/colonies/{id}/data/tables`、`.../data/changes`、`.../data/rows`、`PATCH .../rows` → `EnsureTrackerDB` / `updateTableRow` | 部分 | 读与 UI 行编辑可用；没有 agent 写路径，账本在生产运行中不会被 worker 写入 |
| Queen 声明可写列、worker upsert、Queen 用 SQL 校验 | `internal/tracker/tools.go:808`（`BuildTrackerTools`）、`:992`（`RegisterTrackerTools`）、`:177`（`RegisterWritable`）、`:405`（`Upsert`）、`:156`（`TrackerSQL`）、`:633`（`Query`） | 无 | 未接 | `RegisterTrackerTools`/`BuildTrackerTools` 全仓零调用；会话工具表（`session_tools.go:46-90`）与 worker 工具表（`colony/runtime.go:699`）都不含 `tracker_sql/tracker_upsert/tracker_query` |
| tracker binding（无 binding 拒绝写） | `internal/tracker/binding.go:4`、`internal/tracker/tools.go:74`（`ResolveBinding`）、`:407`（`ErrNoBinding`）、`internal/colony/runtime.go:334`、`internal/colony/worker.go:373` | 无（`BuildWorker` 会解析，但 worker 不 spawn） | 未接 | `ResolveBinding` 零调用；拒绝分支不可达，binding 约束在生产中从未执行 |
| 持久文件型任务计划（Queen 的 spine） | `internal/tasks/session_tools.go:747`、`internal/cli/task_tools.go:47`、`internal/cli/session_tools.go:62`、`internal/server/routes_tasks.go:39` | `hive serve` 会话创建 → 工具表含 4 个 task 工具；`GET /api/sessions/{id}/tasks` 读同一 store | 已接 | worker 没有 task 工具；colony overseer 没有 task 工具 |
| 事件总线：会话实时 transcript → UI SSE | `internal/server/chat_runtime.go:437-530`（`chatTurnEmitter`）、`internal/eventbus/emit.go`、`internal/server/chat_routes.go`（`GET /api/sessions/{id}/events`） | `hive serve` → `NewChatRuntime` 注入 `Bus`（`loader.go:602`）→ 每轮 emit execution/loop/assistant/tool/usage；SSE 路由读同一 bus，`events.jsonl` 由 `chat_runtime.go:218` 落盘 | 已接 | — |
| 事件总线：worker 报告 → Queen 的 `[WORKER_REPORT]` | 发布 `internal/colony/runtime.go:955`（`publishEvent`）、`:959`（`EventSubagentReport`）；消费 `internal/server/queen_orchestrator.go:19`（只订阅 `EventEscalationRequested`） | 无 | 未接 | 没有任何代码把 `SUBAGENT_REPORT` 注入 Queen 会话；`eventbus/bus.go:571` 只做 worker 日志旁路 |
| 提醒中枢：session start / post-tool / budget / compact / idle 注入 | `internal/agentloop/reminder_hub.go:51`、`internal/server/routes_reminders.go:82-90`（每 session `hub.Register(TaskReminderSource)` + `Bind`）、`:124`（`TurnComplete`）、`:139`、`internal/server/chat_runtime.go:580`（`injectReminders`） | `hive serve` → `serveReminders = NewReminderRuntime()`（`loader.go:278`）→ 每轮 `runTurn` 前 `TakeBlock` 注入、成功后 `TurnComplete` → 发 `POST_TOOL_USE` / `stop`；REST `GET/DELETE /api/sessions/{id}/reminders`（`routes_reminders.go:307/310`） | 部分 | 只注册 `tasks` 一个 source、只发 `POST_TOOL_USE`/`STOP` 两个点；`SESSION_START`/`USER_PROMPT_SUBMIT`/`POST_COMPACT`/`IDLE_TICK` 无生产触发；`Hub.Start`（idle ticker）零调用；loop 自己的 `l.Hub` 从未注册 source |
| 提醒内容：fleet 感知 / tracker 快照 / 工具与技能清单 | `internal/agentloop/idle_nudge.go:33`、`internal/agentloop/colony_parallel_nudge.go:54`、`internal/agentloop/tool_skill_reminders.go:23`、`internal/tasks/reminders.go:491`（task source `Render`） | 无 | 未接 | 三个 source 零生产注册；`ReminderAgentContext` 没有 fleet/tracker/skill 字段填充，只有 task 提醒能渲染 |
| 人机协同：Queen 用 `ask_user` 提问并停车 | `internal/agentloop/synthetic_tools.go:94`（`BuildAskUserTool`）、`internal/server/chat_runtime.go:146`（追加工具）、`:622`、`:668`（`executeAskUser`）、`:679`（`EmitClientInputRequested`）、`:375-379`（park reason） | `hive serve` → 会话工具表 + `ask_user` → executor 记录 `pendingQuestions` 并发 `client_input_requested(park_reason=ask_user)`；本轮结束快照为 parked | 已接 | 停车状态只在内存（`pendingQuestions`），下一条用户消息清空；没有落盘 park 状态，进程重启即丢 |
| parked loop 落盘 + 恢复 | `internal/agentloop/park_reason.go`（仅枚举）、`internal/server/chat_runtime.go`（无 park 持久化） | 无 | 未接 | 没有 park 状态写入、没有 resume 入口；凭据等待无实现 |
| Sentinel：停车分类、nudge 或升级 inbox/Telegram/Slack | `internal/sentinel/manager.go:99`（`NewManager`）、`internal/sentinel/classifier.go`、`internal/sentinel/listeners.go`、`internal/sentinel/escalation_source.go` | 只有通知配置/凭据校验/测试路由（`internal/server/routes_sentinel.go:104-110`）与 store/notifier 适配器（`internal/server/adapters_sentinel.go`） | 未接 | `sentinel.NewManager` 零生产调用；classifier/listener/escalation source 从不运行 |

**小结**：四个底座里，任务计划与 transcript 事件总线是真正生效的；tracker 只有读侧 + UI 行编辑、提醒中枢只覆盖 task 一个 source 的两个注入点；worker 报告回路、Sentinel、park 落盘全部未接。`ask_user` 本身已经能用（w23 真接），但"park 到磁盘再恢复"没有。

## 3. goals_outcome — 目标与结果驱动

概念主张：用可度量的成功标准、硬/软约束和上下文声明结果；结构化 `Goal` 的成功标准成为 loop judge 的判据；judge 可 accept/retry/escalate，retry 反馈回注；会话式结果则落到任务计划与 tracker 列上校验。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 缺口 |
| --- | --- | --- | --- | --- |
| 从 agent 定义加载结构化 `Goal` | `internal/agents/agent_spec.go:407-430`（`goal := &schemas.Goal{...}`）、`internal/host/host.go:28,84`、`internal/colony/runtime.go:198`（`Goal()` getter） | `hive serve --colony` / load / fork → `newColonyHost`（`loader.go:452-461`）→ `agents.LoadAgentSpec` → `Host.Config.Goal` → `RuntimeOptions.Goal` | 部分 | runtime 只保存不消费；`Goal()` 没有调用者；没有任何代码把 criteria/constraints/context 变成行为 |
| 成功标准带权重、可度量（`output_contains` / `output_equals` / `llm_judge` / `custom`） | `internal/schemas/goal.go:29-46`（`SuccessCriterion` + `Validate`）、`:142-155`（`IsSuccess` 加权 90% 判定） | 无 | 未接 | `IsSuccess` 零调用；没有代码按 `Metric` 执行任何检查；`llm_judge` 只在 `hive skill eval`（`internal/cli/skill.go:1269`）里用 |
| 约束（hard=MUST / soft=SHOULD）与 Context 写入 agent prompt | `internal/schemas/goal.go:157-190`（`ToPromptContext`）、`internal/agentloop/types.go:109`（`GoalContext`）、`internal/agentloop/loop.go:430` | 无 | 未接 | `ToPromptContext` 零调用；`AgentContext.GoalContext` 生产代码从未赋值，`BuildInitialMessage` 里的拼接永不命中 |
| loop judge：每轮评估 accept / retry / escalate | `internal/agentloop/judge.go:78`（`shouldJudge`）、`:96`（`evaluateJudge`）、`internal/agentloop/loop.go:587`（自然停止）、`:741`（工具失败/节奏） | 无 | 未接 | judge 钩子已进 loop，但生产构造的 loop 从不注入 `Judge`：`internal/server/chat_runtime.go:210` 用 `NewAgentLoop`（只设 Config/Executor/Hub），`internal/colony/runtime.go:875` 用结构体字面量（只有 ToolExecutor/Config）。`shouldJudge` 因 `l.Judge == nil` 恒 false |
| retry 反馈回注会话 | `internal/agentloop/judge.go:165`（`injectJudgeFeedback`） | 无 | 未接 | 依赖上一条；没有 Judge 就不会产生 RETRY |
| judge 小流水线（输出键检查 → 自定义 judge / LLM 质量检查） | `internal/agentloop/llm_judge.go:36`（`NewLLMJudgeAdapter`）、`:57`（`NewLLMJudgeAdapterFromCompleter`）、`internal/llmjudge/judge.go:111`（`Evaluate`） | `hive skill eval`（`internal/cli/skill.go:1269-1325`） | 未接 | `NewLLMJudgeAdapter*` 生产零构造；`internal/llmjudge` 只服务 skill 评测，不评估 `Goal` |
| 会话式结果：Queen 把消息转成任务计划 | `internal/tasks/session_tools.go:747`、`internal/cli/task_tools.go:47`、`internal/cli/session_tools.go:62` | 同 coordination 任务计划 | 已接 | — |
| 会话式结果：以 tracker 列作为 "done" 并用 SQL 校验 | `internal/tracker/tools.go`（工具存在）、`internal/colony/runtime.go:334`（binding） | 无 | 未接 | tracker 写工具未装配，SQL 校验不存在 |
| 未达标信号驱动改进（missed criterion / 空 tracker 列） | 无 | 无 | 未接 | 无 criteria 评估、无 tracker 写入，改进信号没有来源 |

**小结**：`Goal` 的数据模型与加载路径在，但成功标准、约束、上下文都没有进入 prompt；`judge` 的**代码**在（w26），**接线**不在——生产 loop 永远拿到 `Judge == nil`，所以 accept/retry/escalate 一次都不会发生。goals_outcome 依然是"只解析、不执行"。

## 4. improvement — colony 如何变好

概念主张：四条 in-band 改进机制——会话内 reflexion（含 judge retry 反馈）、跨会话的作用域记忆（反射写入 + recall 召回 + 过往对话召回）、学到的 skills（`write_skill` → `skills/` → worker 目录），以及把 pilot 固化成 playbook（确定性 runner、重试/退避/死信、按 tracker 现状 resume）。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 缺口 |
| --- | --- | --- | --- | --- |
| Reflexion 第一层：工具结果/错误回注，下一轮自我修正 | `internal/agentloop/loop.go:726`（`AddToolResult(..., IsError)`）、`internal/agentloop/conversation.go` | `hive serve` → `ChatRuntime.runTurn`（`chat_runtime.go:332`）→ `ExecuteTurn`，每批工具结果写回 conversation | 已接 | — |
| Reflexion 第二层：judge retry 的 critique 回注 | 无（judge 未注入，见 goals_outcome） | 无 | 未接 | 依赖 judge 接线 |
| 跨会话记忆：冷却门控的 reflection 写入 global / per-queen markdown | `internal/server/chat_runtime.go:166-175`（构造 `ReflectionScheduler`）、`:385-387`（每轮 `OnTurnDone`）、`internal/memory/reflection.go:809`（Scheduler）、`:919`（`OnTurnDone`）、`:1030`（`InitializeReflectionState`） | `hive serve` → `createSessionWithRuntime` → `NewChatRuntime`（`HomeRoot` 非空）→ `runTurn` 成功后 `OnTurnDone(ctx,"queen","turn_done")` | 已接 | 仅 Queen chat；worker/colony 无 reflection |
| 跨会话记忆：recall selector 在后续会话召回相关笔记 | `internal/server/chat_runtime.go:540`（`injectRecall`）、`internal/memory/recall.go:174`（`BuildScopedRecallBlocks`） | 每条用户消息后、`ExecuteTurn` 前以 `<system-reminder>` 注入；内容不变时用 `lastRecall` 去重 | 已接 | — |
| 过往对话召回：每轮最多 6 条 dated excerpts；`search_messages` 可搜历史 | `internal/memory/search.go:534`（`SearchMessages`）、`:678`（envelope）、`internal/memory/search_timeline.go:63`；工具名只在 `internal/tools/tier_defaults.go:69-70` | 无 | 未接 | `SearchMessages`/`SearchTimeline` 零生产调用；会话工具表不含 `search_messages`/`search_timeline`；`chat_runtime` 不注入历史对话摘要 |
| 学到的 skills：`write_skill` 写进 colony `skills/`，worker 目录按需加载 | `internal/skills/skill_writer.go:169`（`WriteSkill`）、`internal/skills/manager.go:48`（`NewSkillsManager`）、`internal/agentloop/skill_resources.go:11`（读取拦截） | 无 | 未接 | `WriteSkill`/`NewSkillsManager` 零调用；没有 `write_skill`/`skill_view` 工具；`AgentContext.SkillDirs`（`types.go:144`）从未赋值，`loop.go:798` 的拦截永不触发；`SkillsCatalogPrompt` 从未生成 |
| Playbook：确定性 runner 收敛剩余批次（重试/退避/死信） | `internal/playbook/runner.go:190`（`NewPlaybookRun`）、`:423`（`Converge`）、`:37-56`（`DeadLetter`）、`internal/playbook/rows.go:268-283`（`run_playbook` 等名字清单） | 无 | 未接 | `internal/playbook` 在 `internal/`、`cmd/` 零 import；`run_playbook`/`get_playbook_status`/`stop_playbook` 没有工具定义/执行器/路由 |
| Playbook 的 resume by construction（以 tracker 现状决定剩余工作） | `internal/playbook/rows.go`（`QueryRows`）、`internal/colony/colony_db.go:19-27`（`playbook_runs` 表） | 无 | 未接 | playbook 无入口；tracker 写路径也未接 |

**小结**：improvement 真正生效的仍然只有「工具错误回注 + Queen 的 reflection/recall」（w15 已接）。w26 的 judge 没有让第二条 reflexion 生效；learned skills 与 playbook 依然零入口。改进闭环目前只覆盖"记住"，不覆盖"评估 → 重试"和"把成功固化成可复用流程"。

## 5. queen — 常驻的客户面向 lead

概念主张：Queen 是身份化 persona（13 个 YAML）而非通用 coordinator；她拥有会话、计划、tracker；带作用域记忆（global + per-queen）；两个阶段（independent → colony）；提问时停车、Sentinel 兜底。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 缺口 |
| --- | --- | --- | --- | --- |
| 13 个 Queen persona（YAML profile，含 traits/background/behavior） | `internal/queen/queen_defaults/*.yaml`、`internal/queen/profiles.go:37`（`DefaultQueens`）、`internal/server/routes_queens.go:1006`（`GET /api/queen/profiles`） | `hive serve` 注册 Queen 路由（`app.go:213`），profile 列表/详情/创建/改名/删除读写 YAML 目录 | 已接 | — |
| persona 注入 Queen 的 system prompt | `internal/queen/identity.go:85`（`FormatQueenIdentityPrompt`）、`internal/server/chat_runtime.go:641-660`（`chatQueenIdentityPrompt`）、`:187`、`internal/cli/loader.go:494-513`（`queenOverseerSpec`） | `createSessionWithRuntime` → `NewChatRuntime` → `IdentityPrompt: chatQueenIdentityPrompt(...)`；colony 路径 → `StartOverseer(queenOverseerSpec(...))` | 已接 | `default` queen 无 profile 时回退通用 `chatIdentityPrompt`（预期） |
| 选择一个 Queen 并开一个绑定会话 | `internal/server/routes_queens_sessions.go:85`（`handleQueenSession`）、`:57`（`queenCreateBoundSession`）、`internal/cli/loader.go:549`（`planChatSession`）、`:1041`（`chatSessionDir`） | `POST /api/queen/{queen_id}/sessions` → `deps.CreateSession` → `QueenName` 进入会话记录、`queens/<name>/sessions/<id>` 目录、记忆 scope | 已接 | `initial_phase` 仍被忽略（不影响绑定本身）；上一版的"目录硬编码 default"已修（P3-1 关闭） |
| 两个阶段：Independent → Colony | `internal/server/routes_session_create.go:155-172`（接收 `initial_phase`）、`:212-252`（fork 分支）、`internal/server/session.go:50`（`PhaseState`） | `POST /api/sessions` 可 fork；但 `createSessionWithRuntime` 不消费 `InitialPhase`，`PhaseState` 恒 nil | 部分 | phase 状态没有落点；`GET /api/queen/{id}/tools` 在 `PhaseState == nil` 时直接 404（`routes_queens_tools.go:581`） |
| Queen 的持久会话与历史恢复 | `internal/server/chat_runtime.go:194`（`RestoreConversation`）、`internal/agentloop/conversation.go:1126`（`persist`）、`:1213`（`RestoreConversation`）、`internal/cli/loader_session_resume.go:38`（`planChatSession`） | `NewChatRuntime` 从会话目录恢复 parts；`queen_resume_from`（w28）复用旧 id 与旧目录，`resumeLiveSession` 防止重复 runtime | 已接 | — |
| Queen 的 per-queen 记忆：reflection 写入 + recall 召回 | `internal/server/chat_runtime.go:160-175`、`:540`、`internal/memory/memory.go`、`internal/memory/recall.go:174` | 同 improvement 3/4 | 已接 | — |
| Queen 的过往对话召回：每轮 6 条 dated excerpts + `search_messages` | `internal/memory/search.go:534`、`internal/memory/search_timeline.go:63`；工具名只在 `internal/tools/tier_defaults.go:69-70` | 无 | 未接 | 同 improvement 5 |
| `memory_timeline` flag 打开后生成/召回事件时间线 | `internal/memory/timeline.go:616`（`MemoryTimelineEnabled`）、`:491`（`UpdateSessionTimeline`）、`:528`（`BackfillTimelines`）、`:645`（`SubscribeTimelineTriggers`） | flag 只在 `server/config_features.go:74-79` 读出并出现在 `/api/config`（`:189`） | 未接 | timeline 的提取/回填/订阅函数零生产调用 |
| Queen 拥有任务计划（计划可见、可编辑、跨 reload） | `internal/tasks/session_tools.go:747`、`internal/server/routes_tasks.go:39` | 同 coordination 任务计划 | 已接 | — |
| Queen 拥有 tracker（建 schema、派活、SQL 校验） | `internal/tracker/tools.go:992`（未装配）、`internal/server/routes_colony_workers_data.go:134`（只读） | 无 | 未接 | 写工具未装配；会话 Queen 的目录是 `queens/<name>/sessions`，也拿不到 colony tracker |
| 提问停车（`ask_user`） | `internal/agentloop/synthetic_tools.go:94`（`BuildAskUserTool`）、`internal/server/chat_runtime.go:146`（追加工具）、`:668`（`executeAskUser`）、`:679`（`EmitClientInputRequested`）、`:375-379`（park reason） | chat 会话工具表始终追加 `ask_user`；executor 记录 `pendingQuestions` 并发 `client_input_requested(park_reason=ask_user)`；本轮结束快照为 parked，下一条用户消息即回答 | 已接 | 停车状态只在内存（`pendingQuestions`），进程重启即丢 |
| Sentinel：停车后 nudge / 升级 inbox/Telegram/Slack 并恢复 | `internal/sentinel/manager.go:99`（`NewManager`）、`internal/sentinel/classifier.go`、`internal/sentinel/listeners.go`、`internal/sentinel/escalation_source.go` | 只有通知配置/凭据校验/测试路由（`internal/server/routes_sentinel.go:104-110`）与 store/notifier 适配器 | 未接 | `sentinel.NewManager` 零生产调用；classifier/listener/escalation source 从不运行；没有"parked Queen 被 nudge 或升级"的链路 |
| Queen 与 colony overseer 是同一个 lead | `internal/cli/loader.go:494`（`queenOverseerSpec`）、`internal/colony/runtime.go:860`（`runOverseer`） | load/fork/`serve --colony` → `StartOverseer` 起一个独立 worker（`overseer:<colony>`） | 未接 | overseer 与 chat session 不共享对话/工具/输入：它只有 builtin 工具，跑一轮 "Hello" 后阻塞，不能再收输入；chat Queen 也没有 `run_worker`/tracker 工具。用户看到的 Queen 与 colony 里的 Queen 是两个 agent |

**小结**：Queen 真正接通的有 persona 注入（w23 新接）、绑定会话（含按 queen 分目录）、会话持久/恢复（含 w28 `queen_resume_from`）、per-queen 记忆、任务计划、`ask_user`。缺的是 tracker、过往对话召回、timeline、Sentinel，以及最关键的**Queen ↔ colony 的同一性**：colony 的 overseer 是一个无工具、无输入、只跑一轮的影子 Queen。

## 6. the_loop — 唯一的执行原语

概念主张：一个多轮流式 LLM 会话循环；每轮流式输出、并行跑只读工具、回注结果、决定停止/继续；judge 自纠错；Queen/worker 只是一组 `LoopConfig` 差异；内建崩溃恢复（cursor、park/resume）、上下文压缩、计量与提醒。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 缺口 |
| --- | --- | --- | --- | --- |
| 单一 agent loop 作为 Queen 的执行体 | `internal/agentloop/loop.go:448`（`ExecuteTurn`）、`internal/server/chat_runtime.go:210`（`NewAgentLoop(DefaultLoopConfig(), ...)`）、`:356` | `hive serve` → `createChatSession` → `NewChatRuntime` → 每轮 `runTurn` 调用同一 loop | 已接 | — |
| 流式输出（文本与 tool call 边生成边推） | `internal/agentloop/loop.go:553`（`AComplete`）、`:558-560`（注释明确 Go transport non-streaming） | `chat_runtime.go:461-465` 把完整内容作为一次 `client_output_delta` 发出 | 未接 | `internal/llm` 只有 `AComplete`；`MockProvider.Stream` 仅测试；UI 在整轮完成后才收到整段文本 |
| 只读工具并行执行、其余按序 | `internal/agentloop/loop.go:630-745`（`for i, tc := range toolCalls` 顺序执行） | 同一 loop；`ToolCallStarted`/`Completed` 逐个发 | 未接 | 没有 `ConcurrencySafe` 分组，也没有并发执行；`LoopConfig`/工具元数据里的并发标记未被消费 |
| 工具结果回注，循环直到不再调用工具 | `internal/agentloop/loop.go:584`（无 tool calls → 返回）、`:726`（`AddToolResult`） | `ChatRuntime.runTurn` 使用该路径 | 已接 | — |
| 自纠错第一层：错误结果进入下一轮上下文 | `internal/agentloop/loop.go:726`（`ToolResultOptions{IsError: ...}`） | 同上 | 已接 | — |
| judge：accept / retry / escalate，retry 反馈注入 | `internal/agentloop/judge.go:78,96,165`、`loop.go:587,741` | 无（`Judge` 生产零注入） | 未接 | 钩子在 loop 内，但 `chat_runtime.go:210`/`colony/runtime.go:875` 都不设 `Judge`/`JudgeEvents`；`judge_verdict` 事件从不发出 |
| stall / doom-loop 检测 | `internal/server/stall_check.go:79`（`SubscribeStallCheck`）、`internal/agentloop/park_reason.go:26`（`ParkReasonDoomLoop`）、`internal/agentloop/idle_nudge.go:329` | 无 | 未接 | `SubscribeStallCheck` 零调用（其内部的 `AskJudge` 因此从不运行）；`ParkReasonDoomLoop` 从不产生；没有重复调用指纹检测 |
| Queen 与 worker 的 `LoopConfig` 差异（worker 3+1、30/90/200；Queen soft checkpoint 30/50） | `internal/agentloop/loop.go:53-66`（默认 50 轮、`ToolCallBudget=0`、`ToolCallLifetimeBudget=0`）、`internal/colony/worker_loop_config.go:46`（worker 3/1/30/3/200） | Queen：`chat_runtime.go:210` 用 `DefaultLoopConfig()`；worker 配置只在 colony runtime 使用，无 spawn 入口 | 部分 | Queen 没有 30/50 软检查点（budget=0）；worker 数值不可达 |
| Grace iteration：worker 预算耗尽仍有最后一次 `report_to_parent` | `internal/agentloop/loop.go:96`（`graceTerminalTools`）、`:498-520`（`inGrace` 门控）、`internal/colony/worker_loop_config.go:15,49` | 无 | 未接 | 唯一设置 `grace_iterations=1` 的 worker 路径无入口；chat Queen 的 grace=0 |
| 迭代上限与干净停止 | `internal/agentloop/loop.go:765`（超限返回 error）、`internal/server/chat_runtime.go:357-371`（失败上报） | `MaxIterations=50` 时 `runTurn` 走 `executionFailed` | 已接 | — |
| 崩溃恢复：cursor 持久化 + 会话恢复 | `internal/agentloop/conversation.go:1126`（`persist`）、`:1213`（`RestoreConversation`）、`internal/server/chat_runtime.go:194` | 每条消息落盘并更新 cursor；`NewChatRuntime` 恢复 parts；w28 支持按旧 id 恢复 | 已接 | — |
| park / resume：等待人类回答或凭据后从磁盘继续 | `internal/agentloop/park_reason.go`（仅枚举）、`internal/server/chat_runtime.go:346`（清 pending）、`:375-379`（park reason） | `ask_user` 产生的停车在内存；下一轮用户消息清空 pending | 部分 | 没有 park 状态落盘、没有 resume 入口；凭据等待未实现；进程重启即丢 |
| 上下文压缩：自动检测并压缩；超大工具结果落盘可搜 | `internal/agentloop/conversation.go:736`（`NeedsCompaction`）、`:931`（`Compact`）、`internal/agentloop/llm_compact.go:22`；手动路由 `internal/server/routes_execution.go:444`（`handleCompactAndFork`）→ `:638`（`LlmCompact`） | 只有 `POST /api/sessions/{id}/compact-and-fork` | 部分 | `NeedsCompaction`/`Compact` 零调用，loop 内无自动压缩；spillover 字段存在但 loop 不生成 |
| 每次 LLM 调用计量（tokens/cost）与工具预算上限 | `internal/agentloop/loop.go:561-566`（`LLMTurnComplete`）、`internal/server/chat_runtime.go:469-480`（emit usage）、`loop.go:617-690`（soft/hard/lifetime 预算） | 每次调用发 `llm_turn_complete`（input/output/cached/cost）；工具计数与 defer 在 loop 内执行 | 已接 | — |
| 框架注入提醒保持连贯 | `internal/agentloop/reminder_hub.go`、`internal/server/chat_runtime.go:580` | 同 coordination 提醒 | 部分 | 只有 task source 的 post-tool/stop；loop 自己的 Hub 无 source、无 Start |

**小结**：the_loop 的骨架（单 primitive、工具结果回注、迭代上限、cursor 持久化、usage 计量、手动 compact-and-fork）真实运行；「流式」「并行只读工具」「judge」「stall/doom-loop」「grace」「自动压缩」「完整提醒」都不存在或不可达。`ask_user` 给了它一个内存级 park，但不是"落盘恢复"。

## 7. worker_agent — 短命的并行克隆

概念主张：worker 是 Queen loop 的克隆，带 worker prompt、Queen 工具的子集、更紧的预算、一个任务注入；专注/短命/fail-fast，不能升级、不能再委派；终态 `report_to_parent(status, summary, data)` → Queen 收到 `[WORKER_REPORT]`；有 grace iteration；通过 tracker / 自己的 task list / colony skills 共享；会话可崩溃恢复，`run_worker(resume_worker_ids=…)` 可续跑；大量工作以 headless 方式运行。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 缺口 |
| --- | --- | --- | --- | --- |
| worker 是 Queen loop 的克隆（同一 `AgentLoop`） | `internal/colony/runtime.go:794`（`runWorker` 新建 `agentloop.AgentLoop`）、`internal/colony/worker.go` | 无 | 未接 | `SpawnBatch`（`runtime.go:480`）/`SpawnBatchWithOptions`（`:485`）/`SpawnRun`（`run.go:220`）/`Host.Spawn`（`host.go:165`）/`Host.Run`（`host.go:181`）在生产代码中零调用；没有 worker spawn 路由 |
| worker prompt（聚焦、无升级、无委派、fail-fast 规则）真正注入 | `internal/colony/worker_definition.go:39`（`WorkerSystemPrompt`）、`:113`（`BuildSystemPrompt`）、`:168`（`BuildMeta`） | 无 | 未接 | `BuildSystemPrompt`/`BuildMeta` 零调用；`runWorker` 用 `systemPromptFor(spec)`（`runtime.go:677`），即磁盘 `worker.json` 的 `system_prompt`，详细规则从未进入模型上下文 |
| 工具子集 + 更紧预算（3+1 轮、30/轮、硬停 90、200 终身） | `internal/colony/runtime.go:699`（`workerTools`）、`:345-375`（keep set + tier gating）、`internal/colony/worker_definition.go:9-16`（`DefaultWorkerLoopConfig`）、`internal/colony/worker_loop_config.go:46` | 无 | 未接 | 工具与配置只在 colony runtime 内使用，而 worker 从不 spawn；`newColonyHost` 传给 runtime 的工具只有 `builtin.New(colonyPath).Defs()`，`worker.json` 的 `tools` 列表未应用 |
| `report_to_parent(status, summary, data)` 终态工具 | `internal/agentloop/synthetic_tools.go:279`（`BuildReportToParentTool`）、`:325`（`HandleReportToParent`）、`internal/colony/runtime.go:702`（加入 worker 工具表）、`:709-711`（执行器记录 report） | 无 | 未接 | 只对 `worker:` 流加此工具；`HandleReportToParent` 零调用；没有 worker 会执行它 |
| worker 报告 → Queen 的 `[WORKER_REPORT]` 回合 | `internal/colony/runtime.go:955-963`（发 `EventSubagentReport`）、`internal/eventbus/event_type.go:115` | 无 | 未接 | 没有消费者把 worker report 写回 Queen conversation（见 coordination） |
| Grace iteration：预算耗尽后仅 `report_to_parent`/`task_update`/`tracker_upsert` 可执行 | `internal/agentloop/loop.go:96`、`:498-520`、`internal/colony/worker_loop_config.go:15,49` | 无 | 未接 | loop 代码在，但唯一 `grace_iterations=1` 的 worker 路径无入口 |
| 结果共享：tracker | `internal/tracker/tools.go:808,992` | 无 | 未接 | `tracker_*` 未装配到 worker 工具表 |
| 结果共享：自己的 task list | `internal/tasks/session_tools.go:747`、`internal/tasks/register.go:5` | 无 | 未接 | worker 工具表（`runtime.go:699`）只有 `r.tools`，没有 task 工具；`RegisterTaskTools` 零调用 |
| 结果共享：colony skills | `internal/skills/skill_writer.go:169`、`internal/skills/manager.go:48` | 无 | 未接 | 写与读都没有入口（见 colony） |
| 会话崩溃恢复 + `run_worker(resume_worker_ids=…)` 续跑 | `internal/colony/resume.go:11-69`、`internal/colony/runtime.go:485`（`SpawnBatchWithOptions` 无 resume 参数） | 无 | 未接 | `resume.go` 三函数零调用；没有 `run_worker` 工具，也没有 resume 参数/路由 |
| headless 运行 + Sentinel 升级人工 | `internal/sentinel/manager.go:99`、`internal/sentinel/escalation_source.go`；`cmd/hive` 只有 serve/chat/queen/colony 管理命令 | 无 | 未接 | 没有 headless colony run 入口；`hive serve --colony` 只起一个空转 overseer；Sentinel manager 零生产调用 |

**小结**：worker_agent 在 Go 运行时依然没有任何生产路径。colony 现在能被加载、能起一个 overseer，但 worker 的 loop、prompt、工具子集、预算、`report_to_parent`、grace、tracker/task/skills 共享、resume、headless 全部只存在于 `internal/colony` 的代码与测试里。worker_agent.md 描述的能力在当前 Go 服务里不可用。

---

## 8. 已知可疑项核实（按当前 HEAD）

| 可疑项 | 核实结果 |
| --- | --- |
| `run_worker` 工具 | **仍不存在**。全仓只有名字痕迹（`internal/playbook/rows.go:279`、`internal/agentloop/colony_parallel_nudge.go:35`、`internal/tracker/tools.go:734` 的提示文案），没有工具定义、执行器或 HTTP 路由。 |
| `tracker_*` 工具 | **仍未装配**。`RegisterTrackerTools`（`internal/tracker/tools.go:992`）与 `BuildTrackerTools`（`:808`）零调用；会话工具表与 worker 工具表都不含 `tracker_sql/tracker_upsert/tracker_query`。`ResolveBinding` 也零调用。 |
| 提醒 source 注册 | **部分属实（新变化）**。`internal/server/routes_reminders.go:82-87` 现在为每个 session 注册 `tasks.NewTaskReminderSource()` 并 `Bind`；`TurnComplete`（`:124`）发 `POST_TOOL_USE`/`stop`，`chat_runtime.go:580` 每轮注入。但 idle/colony-parallel/tools-skills 三个 source 仍零注册，`Hub.Start` 仍零调用。 |
| `Sentinel.NewManager` | **属实：零生产调用**。只有通知配置/测试路由（`routes_sentinel.go:104-110`）与 store/notifier 适配器；classifier/listener/escalation source 从不运行。 |
| judge 触发（w26） | **属实：假接**。`internal/agentloop/judge.go` + `llm_judge.go` 已实现，`loop.go:587,741` 也有调用点；但生产构造的 loop 从不注入 `Judge`/`JudgeEvents`（`chat_runtime.go:210`、`colony/runtime.go:875`），`shouldJudge` 因 `l.Judge == nil` 恒 false。`NewLLMJudgeAdapter*` 零生产构造。 |
| `skills.WriteSkill` | **属实：零调用**。`WriteSkill`（`skill_writer.go:169`）与 `NewSkillsManager`（`manager.go:48`）都没有生产调用者；没有 `write_skill`/`skill_view` 工具。 |
| triggers 恢复 | **属实：未接**。`StartTriggerTimer`（`trigger_runtime.go:291`）依赖 `session.QueenExecutor`（`:46`），而 `QueenExecutor` 生产代码零赋值；启动流程不扫描 `triggers.json`。 |
| `QueenExecutor` | **属实：恒 nil**。`internal/server/session.go:18-30` 定义了类型，但全仓没有 `session.QueenExecutor = ...`；因此 triggers、worker escalation 注入、`dismiss-colony-pivot`（`routes_session_colony.go:209`）都拿不到 Queen node。 |
| worker `SpawnBatch` | **属实：零生产调用**。`SpawnBatch`/`SpawnBatchWithOptions`/`SpawnRun`/`Host.Spawn`/`Host.Run` 都没有生产入口；`serve --colony` 只 `StartOverseer`。 |
| token 流式 | **属实：仍未实现**。`loop.go:553` 用 `AComplete`，`:558-560` 注释明确 Go transport 非流式；`internal/llm` 没有生产 `Stream` 接口实现。 |
| 任务工具（w24） | **已真接**。`internal/cli/task_tools.go` + `session_tools.go:62,70` 把 `task_create/task_update/task_list/task_get` 装进会话工具表，`tasks.SetExecContext` 绑定 session；`GET /api/sessions/{id}/tasks` 读同一 store。 |
| Queen persona（w23） | **已真接**。`chat_runtime.go:187` 用 `chatQueenIdentityPrompt`（`queen.FormatQueenIdentityPrompt`）；colony overseer 也用 `queenOverseerSpec`。 |
| `ask_user` 停车（w23） | **已真接（内存级）**。工具进 tool list，executor 发 `client_input_requested` 并在本轮结束以 `park_reason=ask_user` 停车；但没有落盘 park/resume。 |
| colony 生命周期（w25） | **已真接**。`AppDeps.Colony` 与 `CreateColonyFromSource` 在 `loader.go:342-343` 注入；`LoadColony/UnloadColony/CreateColonyFromSource` 都实现并走 4xx/5xx 错误映射；`serve --colony` 启动 `StartOverseer`。 |
| queen memory 未注入 | **已变化（w15 之后）**。`chat_runtime.go:540-573` 每轮注入 global+queen recall；`:166-175` 建 `ReflectionScheduler`，`:385-387` 每轮 `OnTurnDone`。会话目录也已按 queen 分（`loader.go:1041`）。 |

## 9. 总体结论

- **真正接通的**：聊天会话 agent loop（同步、非流式）、会话 transcript SSE + `events.jsonl`、会话持久化/cursor 恢复 + `queen_resume_from`、per-queen memory reflection/recall、colony 磁盘目录与 tar 导入、colony load/unload/Create Colony fork、`serve --colony` 启动 overseer、Queen persona 注入、任务计划工具、`ask_user` 内存级停车、Queen profile CRUD、tracker 只读 UI、手动 `compact-and-fork`、`/stats`。
- **最大的断点**：worker 运行时仍无生产入口（`SpawnBatch`/`SpawnRun`/`Host.Spawn`/`Host.Run` 零调用，无 spawn 路由、无 `run_worker`），因此 colony 的 fan-out、worker prompt/工具/预算、`report_to_parent`、grace、tracker 写、skills、playbook、headless、resume 这一整条链在运行的服务里不可用。
- **"代码在但没接"的典型**：w26 judge（有钩子、无 Judge 注入）、w20 提醒（只注册 task 一个 source）、w25 colony（能加载、不能派活）、`QueenExecutor`（类型在、恒 nil）、`skills.WriteSkill`、`triggers` 恢复、Sentinel。
- **口径提醒**：本审计只回答"代码能否从生产入口走到、走到了是否生效"，不评价上游 Python 的行为；标注 `已接` 的行也可能与概念文档的细节有偏差（如 Queen 的 30/50 软检查点、worker 详细 prompt）。

## 10. 与上一版差异

上一版基线 `e5f7c2fa` 的计数为 14 已接 / 7 部分 / 50 未接（71 行）。本版为 25 / 12 / 42（79 行）。行数和总数不同是因为本版拆细了若干合并行（如 ask_user 与 Sentinel、park 与 resume、tracker 读与写），**不要直接比总数**；逐条迁移如下。

### 10.1 未接 → 已接（6 条）

| # | 条目 | 依据 |
| --- | --- | --- |
| 1 | colony 生命周期 load / unload | `POST/DELETE /api/sessions/{id}/colony` → `loader.go:723/742`；上一版是 `deps.Colony == nil` 的 500 分支 |
| 2 | Create Colony 对话框 → fork | `routes_session_create.go:245` → `loader.go:796`；上一版 `CreateColonyFromSource` 未注入、直接 500 |
| 3 | `serve --colony` 启动 runtime 与 overseer | `loader.go:293-326`；上一版只构造 host，不 `Start`、不 `StartOverseer` |
| 4 | Queen persona 注入 system prompt | `chat_runtime.go:641-660` + `loader.go:494-513`；上一版 `FormatQueenIdentityPrompt` 零调用、用通用常量 |
| 5 | 任务计划（`task_create/update/list/get`） | `cli/task_tools.go` + `session_tools.go:62,70`；上一版会话工具表只有 builtin + integrations |
| 6 | `ask_user` 提问并停车 | `chat_runtime.go:146/622/668/679`；上一版 `BuildAskUserTool` 零调用、`ask_user` 只在名字清单 |

### 10.2 未接 → 部分（6 条）

| # | 条目 | 依据 |
| --- | --- | --- |
| 1 | Queen 与 worker 是同一个 agent loop | overseer 已跑 `AgentLoop`（`colony/runtime.go:860`），但 worker 克隆仍无入口 |
| 2 | colony 跨会话持久 / 重启恢复 | colony 目录与启动加载在，但 runs/workers/对话不恢复，`resume.go` 零调用 |
| 3 | 提醒中枢注入点 | `routes_reminders.go:82-87` 注册 task source、`chat_runtime.go:580` 每轮注入；但只有一个 source、两个点 |
| 4 | park / resume | `ask_user` 有真实停车，但只在内存、无落盘恢复 |
| 5 | 框架注入提醒保持连贯 | 同上，the_loop 侧只覆盖 task 提醒 |
| 6 | Queen 的两个阶段 Independent → Colony | fork 路径真实，但 `PhaseState` 恒 nil、`initial_phase` 不消费 |

### 10.3 部分 → 已接（1 条）

| # | 条目 | 依据 |
| --- | --- | --- |
| 1 | 选择一个 Queen 并开绑定会话 | 上一版：会话目录硬编码 `queens/default/sessions`、phase 不消费；现在 `chatSessionDir`（`loader.go:1041`）按 queen 分，`planChatSession` 支持 resume，记忆 scope 与目录一致（P3-1 关闭） |

### 10.4 新增机制行（上一版没有独立判定）

| 条目 | 状态 | 说明 |
| --- | --- | --- |
| `queen_resume_from` 会话恢复（w28） | 已接 | `loader_session_resume.go:38`、`loader.go:549-571`：按旧 id/旧目录恢复，`resumeLiveSession` 防重复 runtime |
| `/stats` 会话统计（w29） | 已接 | `routes_session_detail.go:200` → `session_stats.go:43/74`；基线字段 + colony 统计叠加 |
| Queen 与 colony overseer 的同一性 | 未接 | `loader.go:494` 起一个独立 overseer，与 chat session 不共享对话/工具/输入 |
| colony 启动即加载（预载 session） | 已接 | `loader.go:293-326`，每个 `--colony` 建一个空 session 并起 overseer |

### 10.5 仍未接、但原因已变（本轮重点核实）

| 条目 | 上一版原因 | 当前原因 |
| --- | --- | --- |
| judge / retry 回注 | 无实现 | **有实现、无注入**：`judge.go`、`llm_judge.go` 在，生产 loop 的 `Judge` 恒 nil |
| tracker_* 写工具 | 未装配 | 仍未装配；`BuildTrackerTools` 已存在但零调用 |
| run_worker / worker fan-out | 无工具 | 仍无工具、无 HTTP spawn 路由 |
| Sentinel | 无 manager 调用 | 仍无；只有通知配置/适配器 |
| skills（write_skill） | 无调用 | 仍无；`SkillsProvider` 也未注入，colony skills 读路由恒空 |
| triggers 定时唤醒 | 依赖 QueenExecutor | 仍依赖，且 `QueenExecutor` 生产零赋值 |
| worker 运行时（SpawnBatch 等） | 无生产入口 | colony 能被加载，但 spawn 仍无生产入口 |
| token 流式 | 未实现 | 仍未实现（`AComplete`） |
| stall / doom-loop | 仅常量 | 仍仅常量；`SubscribeStallCheck` 零调用 |
