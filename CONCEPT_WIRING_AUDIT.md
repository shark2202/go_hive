# Go 运行时 × Key Concepts 接线审计

- 审计基线：`e5f7c2fa`（merge w15/queen-memory，含 w13 聊天事件链）
- 审计对象：`docs/key_concepts/` 下 7 篇（colony、coordination、goals_outcome、improvement、queen、the_loop、worker_agent）
- 判定口径：
  - **生产调用链**＝从 `cmd/hive` → `internal/cli`（`hive serve` / `hive chat` / `serve --colony`）或 `internal/server` 的 HTTP 路由真实可达；只有测试调用不算。
  - **已接**＝机制在生产路径上实际执行并生效；**部分**＝有真实入口但只覆盖概念的一部分，或只读不写；**未接**＝代码存在但生产入口走不到（死代码/仅测试）。
- 说明：本文件只做接线审计，不改产品代码、不改 `go.mod`。行号以基线提交为准。

## 汇总（逐概念完成后回填）

| 概念 | 已接 | 部分 | 未接 |
| --- | --- | --- | --- |
| colony | 1 | 2 | 8 |
| coordination | 1 | 1 | 7 |
| goals_outcome | 0 | 1 | 7 |
| improvement | 3 | 0 | 5 |
| queen | 3 | 1 | 7 |
| the_loop | 6 | 2 | 6 |
| worker_agent | 0 | 0 | 0 |

---

## 1. colony — 殖民地即部署单元

概念主张：colony 是单目录、可移植、可定时唤醒、跨会话持久的部署单元；Queen 与 worker 是同一个 loop；Queen 通过 `run_worker` 在运行时按需扩展；Create Colony 对话框确认后才 fork。

| 机制 | 实现位置（file:line） | 是否有生产调用链（从 internal/cli / internal/server 入口） | 状态 |
| --- | --- | --- | --- |
| colony 目录 `~/.hive/colonies/<name>/` | `internal/hivehome/home.go:89`（`ColoniesDir`）、`internal/server/routes_colonies.go:440`（`ensureMinimalColony`） | 是。`POST /api/colonies/{colony_id}/scaffold` → `handleScaffoldColony`（`routes_colonies.go:409`）→ `ensureMinimalColony`，只创建 `metadata.json` 与 `worker.json`（`routes_colonies.go:446,457`） | 部分：目录存在，但概念里的 `tracker/`、`skills/`、`triggers.json`、任务计划都不在 scaffold 内 |
| 单目录可移植 / tar 导入 | `internal/server/routes_colonies.go:41-58`（import 路由）、`internal/server/routes_colonies_import.go:939,1236` | 是。`POST /api/colonies/import*` → `handleImportColony` 等，落盘到 `home.ColoniesDir()` | 已接 |
| Queen 与 worker 是同一个 agent loop | `internal/colony/runtime.go:794`（`runWorker` 里 `agentloop.AgentLoop`）、`internal/colony/worker_loop_config.go:46`（worker loop 3+1/30/200） | 否。`ColonyRuntime.SpawnBatch`（`runtime.go:480`）/`SpawnRun`（`run.go:220`）/`Host.Spawn`（`internal/host/host.go:147`）/`Host.Run`（`host.go:163`）在生产代码中没有调用者；`Host.Start`（`host.go:120`）也只有测试调用 | 未接：loop 代码在，但没有生产入口真正启动 colony 运行 |
| Queen 通过 `run_worker` 工具在运行时扩展 | 仅有名字常量：`internal/playbook/rows.go:268-283`（`run_worker` 在 `ColonyQueenTools` 名单里）；`ColonyRuntime.SpawnBatch` 在 `internal/colony/runtime.go:480` | 否。会话工具表 `internal/cli/session_tools.go:43-80` 只装 builtin + integrations；worker 工具表 `internal/colony/runtime.go:699` = runtime tools + `report_to_parent`。没有任何 `run_worker` 工具定义或执行器 | 未接 |
| Create Colony 对话框 → fork 出 colony | `internal/server/routes_session_create.go:238`（要求 `deps.CreateColonyFromSource`）、`internal/cli/loader.go:311-328`（`NewApp` 未注入该 seam） | 否。CLI 构造 `AppDeps` 时只注入 `CreateSession`，`CreateColonyFromSource` 为 nil → 该分支直接 500 | 未接 |
| colony 生命周期 load / unload | `internal/server/routes_session_colony.go:55`（`handleLoadColony`）、`:170`（`handleUnloadColony`）、所需 seam 在 `internal/server/routes_session_colony.go:30-33` | 否。`AppDeps.Colony` 未在 `internal/cli/loader.go:311-328` 注入，两个路由都走 `deps.Colony == nil` 的 500 分支 | 未接 |
| colony 的 tracker 作为共享账本 | `internal/tracker/tracker.go:234`（`EnsureTrackerDB`）、`internal/server/routes_colony_workers_data.go:134`、`internal/colony/runtime.go:334-338`（binding 只从 `InputData["binding"]` 读） | 部分。`EnsureTrackerDB` 只在读接口（workers/data）和 `tracker/tools.go` 内部被调用；`ColonyRuntime.Start`（`runtime.go:417-430`）不建库，`SpawnBatch` 也不给 worker 注入 binding；`tracker.BuildTrackerTools`（`tracker/tools.go:808`）零调用 | 部分：UI 能读 tracker，worker 拿不到 tracker 写工具，账本不会在生产运行中被写 |
| colony 内 `skills/`（Queen 写、worker 加载） | `internal/skills/skill_writer.go:169`（`WriteSkill`）、`internal/skills/manager.go:48`（`NewSkillsManager`） | 否。`WriteSkill` 与 `NewSkillsManager` 均无生产调用者；scaffold 不创建 `skills/`；会话/worker 工具表里无 `write_skill`、`skill_view` 类工具 | 未接 |
| task 计划（持久文件型 to-do） | `internal/tasks/store.go:155`（`TaskStore`）、`internal/server/app.go:158`（仅注册 GET/unarchive/clear 路由） | 否（就创建/更新工具而言）。服务端只有 `GET /api/sessions/{id}/tasks` 等只读/维护路由（`routes_tasks.go:36-43`）；`tasks.RegisterTaskTools`（`tasks/register.go:5`）零调用，会话工具表也没有 `task_create/task_update/task_list` | 未接：任务计划在运行时无法被创建 |
| cron triggers 定时唤醒 colony | `internal/server/trigger_runtime.go:291`（`StartTriggerTimer`）、`internal/triggers/triggers_store.go:68`（写 `triggers.json`） | 否。`StartTriggerTimer` 只在 `routes_session_triggers.go:182,320,462` 被调用，而这些路径要求 `session.QueenExecutor != nil`；`QueenExecutor` 在生产代码里从未赋值（仅测试）；服务启动也不会恢复 `triggers.json` | 未接 |
| colony 跨会话持久 / 重启恢复 | `internal/host/host.go:120`（`Start`）、`internal/colony/resume.go:11`（会话目录探测） | 否。`Host.Start`、`resume.go` 的探测函数没有生产调用者；`serve --colony` 只构造 host（`cli/loader.go:406-444`），不启动、不恢复 | 未接 |

**小结**：colony 只有「磁盘目录 + tar 导入」这一层是真正接通的；运行时的 Queen/worker、`run_worker`、Create Colony、load/unload、tracker 写路径、skills、任务计划、定时唤醒、重启恢复全部没有生产入口。概念文档描述的 colony 生命周期在 Go 运行时基本不存在。

## 2. coordination — tracker、任务计划、事件总线、提醒中枢

概念主张：colony 没有图；Queen 与 worker 靠四个轻量底座同步——共享 tracker、持久任务计划、事件总线、提醒中枢；人机协同靠 Queen 提问停车 + Sentinel 兜底。

| 机制 | 实现位置（file:line） | 是否有生产调用链（从 internal/cli / internal/server 入口） | 状态 |
| --- | --- | --- | --- |
| tracker 作为每 colony 的 SQLite 共享账本 | `internal/tracker/tracker.go:234`（`EnsureTrackerDB`）、`internal/tracker/binding.go:4`（`BindingFromInputData`）、`internal/tracker/tools.go:74`（`ResolveBinding`） | 部分。读接口 `GET /api/colony/{id}/workers/data` 会 `EnsureTrackerDB`（`routes_colony_workers_data.go:134`）；但 `ResolveBinding` 零调用，`ColonyRuntime.Start`（`runtime.go:417-430`）与 `SpawnBatch`（`runtime.go:480-560`）都不建立/注入 binding | 部分：库能建、UI 能读；没有生产写路径 |
| Queen 声明可写列、worker upsert、Queen 用 SQL 校验 | `internal/tracker/tools.go:177`（`RegisterWritable`）、`:405`（`Upsert`）、`:156`（`TrackerSQL`）、`:633`（`Query`）；工具装配 `internal/tracker/tools.go:808`（`BuildTrackerTools`） | 否。`BuildTrackerTools` 在全仓零调用；会话工具表（`internal/cli/session_tools.go:43-80`）与 worker 工具表（`internal/colony/runtime.go:699`）都不含 `tracker_sql/tracker_upsert/tracker_query`。无 binding 时的拒绝逻辑（`Upsert` 的 `ErrNoBinding`，`tools.go:406`）因此永远走不到 | 未接 |
| 持久文件型任务计划（Queen 的 spine） | `internal/tasks/store.go:155`（`TaskStore`）、`internal/tasks/session_tools.go:20`（task 工具定义）、`internal/tasks/register.go:5`（`RegisterTaskTools`） | 否（创建/更新）。只有 `GET /api/sessions/{id}/tasks`、unarchive、clear-completed（`internal/server/routes_tasks.go:36-43`）；`RegisterTaskTools` 零调用，会话工具表无 `task_create/task_update/task_list` | 未接：任务计划无法在运行时生成，UI 只能读到一个空/旧列表 |
| 事件总线：worker 报告 → Queen 的 `[WORKER_REPORT]` | 发布 `internal/colony/runtime.go:955-963`（`publishEvent` → `EventSubagentReport`）、事件类型 `internal/eventbus/event_type.go:115` | 否。发布端只在 colony runtime 的 worker 完成路径上（该路径无生产入口）；消费端只有 `queen_orchestrator.go:76` 的 escalation 路由，没有任何代码把 `SUBAGENT_REPORT` 注入 Queen 会话（`eventbus/bus.go:571` 仅关闭 worker 日志） | 未接 |
| 事件总线：会话实时 transcript → UI SSE | `internal/server/chat_routes.go:36`（`GET /api/sessions/{id}/events`）、`internal/server/chat_runtime.go:397-472`（`chatTurnEmitter`）、`internal/eventbus/emit.go` | 是。`hive serve` → `createChatSession`（`cli/loader.go:450-530`）→ `NewChatRuntime` 注入 `Bus`（`loader.go:503-511`）→ `runTurn` 每轮 emit；SSE 路由读同一 bus | 已接 |
| 提醒中枢：session start / post-tool / budget / compact / idle 注入 | `internal/agentloop/reminder_hub.go:51`（`NewReminderHub`）、`internal/agentloop/loop.go:144,307-316,459,683,736`（`Fire` 调用点） | 否。`AgentLoop` 会建空 Hub，但生产代码从未 `Hub.Register` 任何 source：`NewIdleNudgeSource`（`idle_nudge.go:33`）、`NewTaskReminderSource`（`tasks/reminders.go:440`）、`NewColonyParallelNudgeSource`（`colony_parallel_nudge.go:54`）、`NewSearchableToolsReminderSource`（`tool_skill_reminders.go:23`）全部零生产调用；`Hub.Start` 也没有调用者（idle ticker 不启动） | 未接：所有提醒点都是空 Hub 的 no-op |
| 提醒内容：fleet 感知 / tracker 快照 / 工具与技能清单 | 各 source 的 `Render`：`idle_nudge.go`、`colony_parallel_nudge.go:96`、`tool_skill_reminders.go:59`、`tasks/reminders.go:491` | 否。source 未注册；且 `ReminderAgentContext` 的 `OpenTasksProvider` / `QueenSkillsCatalogProvider` 等字段在 `chat_runtime.go:163-176` 没有赋值 | 未接 |
| 人机协同：Queen 用 `ask_user` 提问并停车 | `internal/agentloop/synthetic_tools.go:94`（`BuildAskUserTool`）、`internal/agentloop/park_reason.go:16`、`internal/agentloop/synthetic_tools.go:399`（`suggest_colony` 工厂表） | 否。`BuildAskUserTool` 零调用；`ask_user` 只出现在 `DynamicRefreshSyntheticNames`（`loop.go:321`）和路由的“应有工具”清单（`routes_session_live_tools.go:65`），从未加入模型可见的 tool list；`chat_runtime.runTurn` 也不处理停车，执行完即 `awaiting_user` | 未接 |
| Sentinel：停车时分类、nudge 或升级到 inbox/Telegram/Slack | `internal/sentinel/manager.go:99`（`NewManager`）、`internal/sentinel/classifier.go`、`internal/sentinel/listeners.go`、`internal/sentinel/escalation_source.go:70` | 否。只有通知配置/凭据校验路由（`routes_sentinel.go:104-122`）与 store/notifier 适配器（`adapters_sentinel.go:28,62`）；`sentinel.NewManager` 零生产调用，classifier/listener/escalation source 从不运行 | 未接 |

**小结**：coordination 四个底座里只有「聊天 transcript 事件总线」真正生效；tracker 只有读侧、任务计划与提醒中枢完全没接、worker 报告没有回路。人机协同（ask_user/Sentinel）完全未接。

## 3. goals_outcome — 目标与结果驱动

概念主张：用可度量的成功标准、硬/软约束和上下文声明结果；结构化 `Goal` 的成功标准成为 loop judge 的判据；judge 可 accept/retry/escalate，retry 反馈回注；会话式结果则落到任务计划与 tracker 列上校验。

| 机制 | 实现位置（file:line） | 是否有生产调用链（从 internal/cli / internal/server 入口） | 状态 |
| --- | --- | --- | --- |
| 从 agent 定义加载结构化 `Goal` | `internal/agents/agent_spec.go:402-430`（`goal := &schemas.Goal{...}`，成功标准/约束逐条映射）、`internal/host/host.go:28,84`、`internal/colony/runtime.go:198`（`Goal()` getter） | 部分。`hive serve --colony` → `buildColonyHost`（`cli/loader.go:406-444`）→ `agents.LoadAgentSpec` 会加载 Goal 并传入 runtime；但 runtime 只保存不消费，且 colony host 本身没有运行入口 | 部分：数据能加载，行为未生效 |
| 成功标准带权重、可度量（`output_contains` / `llm_judge` / `custom`） | `internal/schemas/goal.go:29-46`（`SuccessCriterion` + `Validate`）、`:142-155`（`IsSuccess` 加权 90% 判定）、`internal/agents/agent_spec.go:52`（worker 标准固定 `llm_judge`） | 否。`IsSuccess` 无调用者；没有任何代码按 `Metric` 执行 `output_contains`/`output_equals`/`custom`；`llm_judge` 只在 `hive skill eval` 的测试评定里用（`internal/cli/skill.go:1269`），不在 agent 执行路径 | 未接 |
| 约束（hard=MUST / soft=SHOULD）与 Context 写入 agent prompt | `internal/schemas/goal.go:157-190`（`Goal.ToPromptContext` 生成 `## Constraints` / `[MUST]` / `## Context`） | 否。`ToPromptContext` 全仓零调用；`AgentContext.GoalContext`（`internal/agentloop/types.go:109`）只在 `BuildInitialMessage` 里被读（`loop.go:423-424`），生产代码从未给它赋值 | 未接 |
| loop judge：每轮评估 accept / retry / escalate | `internal/agentloop/loop.go:28-29,58`（只有 `JudgeEveryNTurns` 配置字段与默认值 1）；`internal/agentloop/types.go:58-59`（`SuccessCriteria *string`、`SkipJudge`） | 否。`JudgeEveryNTurns`、`SkipJudge` 均无执行点；`loop.go` 的 `ExecuteTurn` 在模型不再调用工具时直接返回成功（`loop.go:574-579`），没有 judge 调用 | 未接 |
| retry 反馈回注会话 | 无实现；概念中的 `agent_loop/internals/judge_pipeline.py` 在 Go 侧不存在对应包 | 否 | 未接 |
| judge 小流水线（输出键检查 → 自定义 judge / LLM 质量检查） | `internal/llmjudge/judge.go:111`（`Evaluate`）、`internal/server/judge.go:36`（`AskJudge`） | 部分/否。`llmjudge` 只被 `hive skill eval` 使用（`cli/skill.go:1269-1325`）；`AskJudge` 只被 `stall_check.go:101` 用于 stall 判定，不评估 Goal | 未接（就 Goal 判定而言） |
| 会话式结果：Queen 把消息转成任务计划，并以 tracker 列作为 “done” | 任务：`internal/tasks/session_tools.go`、tracker：`internal/tracker/tools.go:808`（均未装配到会话工具表） | 否。会话工具表只有 builtin + integrations（`internal/cli/session_tools.go:43-80`） | 未接 |
| 未达标信号驱动改进（missed criterion / 空 tracker 列） | 无：无 criteria 评估、无 tracker 写入 | 否 | 未接 |

**小结**：`Goal` 的数据模型与加载路径存在，但成功标准、约束、上下文都没有进入 prompt，也没有任何 judge 读取它们；loop 里连 judge 调用点都不存在。goals_outcome 在 Go 运行时实质上是「只解析、不执行」。

## 4. improvement — colony 如何变好

概念主张：四条 in-band 改进机制——会话内 reflexion（含 judge retry 反馈）、跨会话的作用域记忆（反射写入 + recall 召回 + 过往对话召回）、学到的 skills（`write_skill` → `skills/` → worker 目录），以及把 pilot 固化成 playbook（确定性 runner、重试/退避/死信、按 tracker 现状 resume）。

| 机制 | 实现位置（file:line） | 是否有生产调用链（从 internal/cli / internal/server 入口） | 状态 |
| --- | --- | --- | --- |
| Reflexion 第一层：工具结果/错误回注，下一轮自我修正 | `internal/agentloop/loop.go:699`（`conv.AddToolResult`，含 `IsError`）、`internal/agentloop/conversation.go`（工具结果进入上下文） | 是。`ChatRuntime.runTurn`（`chat_runtime.go:327`）→ `AgentLoop.ExecuteTurn`，每批工具结果都写回 conversation | 已接 |
| Reflexion 第二层：judge retry 的 critique 回注 | 无实现；`internal/agentloop` 无 judge 调用点（见 goals_outcome） | 否 | 未接 |
| 跨会话记忆：冷却门控的 reflection 写入 global / per-queen markdown | `internal/server/chat_runtime.go:151-160`（构造 `memory.ReflectionScheduler`）、`:344-347`（每轮 `OnTurnDone`）、`internal/memory/reflection.go:838-875`（每 6 轮 short、每 4 次 reflection 一次 long）、`:658-727`（`RunUnifiedShort/LongReflection`） | 是。`hive serve` → `createChatSession` → `NewChatRuntime`（`deps.HomeRoot` 非空时）→ `runTurn` 成功后 `OnTurnDone(ctx,"queen","turn_done")` | 已接（仅 Queen 聊天会话；worker/colony 无 reflection） |
| 跨会话记忆：recall selector 在后续会话召回相关笔记 | `internal/server/chat_runtime.go:496-529`（`injectRecall`）、`internal/memory/recall.go:174`（`BuildScopedRecallBlocks`）、`internal/memory/reflection.go:775`（调度器 scope 目录） | 是。每条用户消息后、`ExecuteTurn` 前以 `<system-reminder>` 注入；内容不变时去重（`lastRecall`） | 已接 |
| 过往对话召回：每轮最多 6 条 dated excerpts；`search_messages` 可搜历史 | `internal/memory/search.go:534`（`SearchMessages`）、`:678`（envelope）、`internal/memory/search_timeline.go:63`；工具名只在 `internal/tools/tier_defaults.go:69-70` 的允许名单里 | 否。`SearchMessages`/`SearchTimeline` 无生产调用者；会话工具表不含 `search_messages`/`search_timeline`；`chat_runtime` 只注入 memory recall，不注入历史对话摘要 | 未接 |
| 学到的 skills：`write_skill` 写进 colony `skills/`，worker 目录按需加载 | `internal/skills/skill_writer.go:169`（`WriteSkill`）、`internal/skills/manager.go:48`（`NewSkillsManager`/catalog）、`internal/agentloop/skill_resources.go:11`（skill 文件读取拦截） | 否。`WriteSkill` 与 `NewSkillsManager` 零生产调用；没有 `write_skill`/`skill_view` 工具注册；`AgentContext.SkillDirs`（`types.go:144`）在 chat/colony 上下文里从未赋值；`SkillsCatalogPrompt` 字段从未生成 | 未接 |
| Playbook：确定性 runner 收敛剩余批次（重试/退避/死信） | `internal/playbook/runner.go:190`（`NewPlaybookRun`）、`:404-416`（`Converge`/`DeadLetter`）、`internal/playbook/rows.go:268-283`（`run_playbook` 等工具名清单） | 否。`internal/playbook` 在 `internal/`、`cmd/` 中零 import；`run_playbook`/`get_playbook_status`/`stop_playbook` 只在名字清单里，没有工具定义/执行器/路由 | 未接 |
| Playbook 的 resume by construction（以 tracker 现状决定剩余工作） | `internal/playbook/rows.go`（`QueryRows`）、`internal/colony/colony_db.go:19-27`（`playbook_runs` 表）；tracker 写入未接 | 否。playbook 无入口；tracker 写路径也未接（见 coordination） | 未接 |

**小结**：improvement 真正生效的只有「工具错误回注 + Queen 的记忆 reflection/recall」；judge retry、历史对话召回、learned skills、playbook 四条全部未接。改进闭环目前只覆盖「记住」，不覆盖「评估 → 重试」和「把成功固化成可复用流程」。

## 5. queen — 常驻的客户面向 lead

概念主张：Queen 是身份化 persona（13 个 YAML）而非通用 coordinator；她拥有会话、计划、tracker；带作用域记忆（global + per-queen）；两个阶段（independent → colony）；提问时停车、Sentinel 兜底。

| 机制 | 实现位置（file:line） | 是否有生产调用链（从 internal/cli / internal/server 入口） | 状态 |
| --- | --- | --- | --- |
| 13 个 Queen persona（YAML profile，含 traits/background/behavior） | `internal/queen/queen_defaults/*.yaml`（13 个文件）、`internal/queen/profiles.go:41`（`DefaultQueens`）、`internal/server/routes_queens.go:1006`（`GET /api/queen/profiles`） | 是。`hive serve` 注册 Queen 路由（`app.go:198-201`），profile 列表/详情/创建/改名/删除都读写 YAML 目录 | 已接（仅 profile 数据面） |
| persona 注入 Queen 的 system prompt | `internal/queen/identity.go:85`（`FormatQueenIdentityPrompt`） | 否。该函数全仓零调用；`chat_runtime.go:36-38` 的 `chatIdentityPrompt` 是通用常量，`chat_runtime.go:172` 直接使用；`QueenName` 只用于记忆作用域 | 未接：Queen 的“人设”没有进入模型上下文 |
| 选择一个 Queen 并开一个绑定会话 | `internal/server/routes_queens_sessions.go:85`（`handleQueenSession`）、`:54`（`queenCreateBoundSession` → `deps.CreateSession`）、`internal/cli/loader.go:450-530`（`createChatSession`） | 部分。路由能把 `QueenName` 传进 `CreateSession`，会话记录与记忆按 queen 命名；但会话目录硬编码 `QueensDir()/"default"/sessions`（`loader.go:501`），`InitialPhase` 未消费，`PhaseState` 始终为 nil（`session.go:50`）。另有已退役的 classifier 路由 `POST /api/messages/classify`（`routes_messages_extra.go:66`）仍注册，但运行时未调用 | 部分：能开“某个 Queen 的聊天”，但不是概念里的 persona/phase 绑定 |
| 两个阶段：Independent → Colony | `internal/server/routes_session_create.go:166-177`（接收 `initial_phase`）、`routes_session_create.go:238`（colony fork 需要 `CreateColonyFromSource`） | 否。`initial_phase` 进入 `CreateSessionParams` 后被 `createChatSession` 忽略；colony fork seam 未注入（`cli/loader.go:311-328`） | 未接 |
| Queen 的持久会话与历史恢复 | `internal/server/chat_runtime.go:179`（`RestoreConversation`）、`internal/agentloop/conversation.go:1213`、`internal/agentloop/store.go` | 是。`NewChatRuntime` 从会话目录恢复 parts；每轮新增消息落盘 | 已接 |
| Queen 的 per-queen 记忆：reflection 写入 + recall 召回（w15） | `internal/server/chat_runtime.go:144-160`（`GlobalMemoryDir`/`QueenMemoryDir`）、`:496-529`（`injectRecall`）、`internal/memory/memory.go:19,27`、`internal/memory/recall.go:174` | 是。`HomeRoot` 非空即启用；reflection 每 6 轮 short、每 4 次 reflection 一次 long（`memory/reflection.go:838-875`）；recall 每轮注入并在内容不变时去重 | 已接 |
| Queen 的过往对话召回：每轮 6 条 dated excerpts + `search_messages` | `internal/memory/search.go:534`（`SearchMessages`）、`internal/memory/search_timeline.go:63`；工具名只在 `tools/tier_defaults.go:69-70` | 否。`SearchMessages`/`SearchTimeline` 无生产调用者；会话工具表无 `search_messages`/`search_timeline`；`chat_runtime` 不注入历史对话摘要 | 未接 |
| `memory_timeline` flag 打开后生成/召回事件时间线 | `internal/memory/timeline.go:616`（`MemoryTimelineEnabled`）、`:491`（`UpdateSessionTimeline`）、`:528`（`BackfillTimelines`）、`:645`（`SubscribeTimelineTriggers`） | 否（除 flag 展示）。flag 只在 `server/config_features.go:74-79` 读出并出现在 `/api/config`；timeline 的提取/回填/订阅函数零生产调用 | 未接 |
| Queen 拥有任务计划（计划可见、可编辑、跨 reload） | `internal/tasks/store.go:155`、`internal/server/routes_tasks.go:36-43`（只读/维护路由） | 否（创建/更新）。见 coordination：`RegisterTaskTools` 零调用，会话无 task 工具 | 未接 |
| Queen 拥有 tracker（建 schema、派活、SQL 校验） | `internal/tracker/tools.go:808`（`BuildTrackerTools`）、`internal/server/routes_colony_workers_data.go:134`（只读） | 否。写工具未装配，会话/worker 工具表都不含 `tracker_*` | 未接 |
| 提问停车 + Sentinel 升级 | `internal/agentloop/synthetic_tools.go:94`（`BuildAskUserTool`）、`internal/sentinel/manager.go:99` | 否。`ask_user` 未进入 tool list；Sentinel manager 无生产调用 | 未接 |

**小结**：Queen 真正接通的只有「会话持久化 + per-queen 记忆 + profile 数据管理」。persona 注入、过往对话召回、timeline、任务计划、tracker、停车/Sentinel 全部未接；`initial_phase` 与 colony fork 也不生效。运行时的 Queen 仍是一个通用聊天 agent，只是记忆按 `queen_name` 分了目录。

## 6. the_loop — 唯一的执行原语

概念主张：一个多轮流式 LLM 会话循环；每轮流式输出、并行跑只读工具、回注结果、决定停止/继续；judge 自纠错；Queen/worker 只是一组 `LoopConfig` 差异；内建崩溃恢复（cursor、park/resume）、上下文压缩、计量与提醒。

| 机制 | 实现位置（file:line） | 是否有生产调用链（从 internal/cli / internal/server 入口） | 状态 |
| --- | --- | --- | --- |
| 单一 agent loop 作为 Queen 的执行体 | `internal/agentloop/loop.go:445`（`ExecuteTurn`）、`internal/server/chat_runtime.go:195`（`NewAgentLoop(DefaultLoopConfig(), ...)`）、`:327`（`ExecuteTurn`） | 是。`hive serve` → `createChatSession` → `NewChatRuntime` → 每轮 `runTurn` 调用同一 loop | 已接 |
| 流式输出（文本与 tool call 边生成边推） | `internal/agentloop/loop.go:546`（`AComplete` 非流式）、`:550-557` 注释明确「Go transport is non-streaming，整段作为一次 snapshot 发布」 | 是（但非流式）。`chat_runtime.go:421-426` 把完整内容作为一个 `client_output_delta` 发出 | 未接：概念中的 streaming 未实现，UI 只在整轮完成后收到整段文本 |
| 只读工具并行执行、其余按序 | `internal/agentloop/loop.go:603-641`（`for i, tc := range toolCalls` 顺序执行，无并发分支/只读标记） | 是（顺序执行）。`executeTool`（`loop.go:751` 起）逐个调用；不存在 “safe for concurrency” 分类 | 未接：全部工具串行 |
| 工具结果回注，循环直到不再调用工具 | `internal/agentloop/loop.go:574-579`（无 tool calls → 返回）、`:699`（`AddToolResult`）、`:699-712`（结果写回 conversation） | 是。`ChatRuntime.runTurn` 使用该路径 | 已接 |
| 自纠错第一层：错误结果进入下一轮上下文 | 同上，`AddToolResult(..., ToolResultOptions{IsError: results[i].IsError})`（`loop.go:699-701`） | 是 | 已接 |
| judge：accept / retry / escalate，retry 反馈注入 | 无实现；`LoopConfig.JudgeEveryNTurns`（`loop.go:29,58`）无执行点；`AgentSpec.SkipJudge`（`types.go:59`）无读取点 | 否 | 未接 |
| stall / doom-loop 检测 | 仅常量：`internal/agentloop/park_reason.go:26`（`ParkReasonDoomLoop`）、`idle_nudge.go:329-332`（提示文案）；`internal/server/stall_check.go:79`（`SubscribeStallCheck`） | 否。`SubscribeStallCheck` 只在测试中调用；`ParkReasonDoomLoop` 从不产生；没有重复调用指纹检测 | 未接 |
| Queen 与 worker 的 `LoopConfig` 差异（worker 3+1、30/90/200；Queen soft checkpoint 30/50） | `internal/agentloop/loop.go:38-66`（默认值：50 轮、`ToolCallBudget=0`、`ToolCallLifetimeBudget=0`）、`internal/colony/worker_loop_config.go:46-70`（worker 3/1/30/3/200） | 部分。`chat_runtime.go:195` 用 `DefaultLoopConfig()`，即 Queen 没有 30/50 的软检查点（budget=0）；worker 配置只在 colony runtime 使用，而 colony 运行无生产入口 | 部分：Queen 路径真实生效，但数值与概念不符；worker 数值不可达 |
| Grace iteration：worker 预算耗尽仍有最后一次 `report_to_parent` | `internal/agentloop/loop.go:600-615`（`inGrace` + 仅允许 `report_to_parent`/`tracker_upsert`/`task_update`）、`worker_loop_config.go:47`（`grace_iterations=1`） | 否（生产）。代码存在于 loop；唯一设置 grace=1 的 worker 路径没有生产入口；chat Queen 的 grace=0 | 未接 |
| 迭代上限与干净停止 | `internal/agentloop/loop.go:714-718`（超限返回 error）、`internal/colony/runtime.go:831-845`（失败/完成落状态） | 是（Queen 路径）。超过 `MaxIterations=50` 时 `runTurn` 走 `executionFailed`（`chat_runtime.go:328-341`） | 已接 |
| 崩溃恢复：cursor 持久化 + 会话恢复 | `internal/agentloop/conversation.go:1126-1160`（`persist`/`writeNextSeq`）、`:1188-1200`（`WriteCursor`）、`:1213`（`RestoreConversation`）、`internal/server/chat_runtime.go:179` | 是。每条消息落盘并更新 cursor；`NewChatRuntime` 恢复 parts | 已接 |
| park / resume：等待人类回答或凭据后从磁盘继续 | `internal/agentloop/park_reason.go`（仅枚举）、`internal/server/chat_runtime.go`（无 park 分支，turn 结束即 `awaiting_user`） | 否。没有 park 状态写入、没有 resume 入口；`ask_user` 也未接 | 未接 |
| 上下文压缩：自动检测并压缩；超大工具结果落盘可搜 | `internal/agentloop/conversation.go:736`（`NeedsCompaction`）、`:931`（`Compact`）、`internal/agentloop/compaction.go`、`internal/agentloop/llm_compact.go:22`；手动路由 `internal/server/routes_execution.go:444`（`handleCompactAndFork`）→ `:638`（`LlmCompact`） | 部分。`NeedsCompaction`/`Compact` 零调用，loop 内无自动压缩；只有 `POST /api/sessions/{id}/compact-and-fork` 手动压缩并 fork | 部分：手动可压，自动压缩未接 |
| 每次 LLM 调用计量（tokens/cost）与工具预算上限 | `internal/agentloop/loop.go:558-562`（`LLMTurnComplete`）、`internal/server/chat_runtime.go:429-443`（emit `llm_turn_complete`，含 input/output/cached/cost）、`loop.go:600-650`（soft/hard/lifetime 预算） | 是。每次调用发 usage 事件；工具调用计数与 defer 在 loop 内执行 | 已接 |
| 框架注入提醒保持连贯 | `internal/agentloop/reminder_hub.go`、`loop.go:459,683,736` | 否。Hub 无 source 注册（见 coordination） | 未接 |

**小结**：the_loop 的骨架（单 primitive、工具结果回注、迭代上限、cursor 持久化、usage 计量、手动 compact-and-fork）是真实运行的；但「流式」「并行只读工具」「judge」「stall/doom-loop」「park/resume」「自动压缩」「提醒」都不存在或不可达。概念文档描述的是完整上游 loop，Go 版目前是同步、无 judge、无停车的简化循环。




