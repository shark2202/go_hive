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
| goals_outcome | 0 | 0 | 0 |
| improvement | 0 | 0 | 0 |
| queen | 0 | 0 | 0 |
| the_loop | 0 | 0 | 0 |
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
