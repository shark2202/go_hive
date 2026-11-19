# Go 运行时 × Key Concepts 接线审计

- 审计基线：`c444c192`（`docs(qc): 1.0.21 终检 8/8 与产物表`，Go 1.0.21）
- 上一版基线：`4b6f1cf4`（61 已接 / 9 部分 / 10 未接，80 行）
- 更早基线：`ed457d75`（48 已接 / 10 部分 / 22 未接，80 行）
- 审计日期：2026-11-19
- 本轮增量：复核 `4b6f1cf4..c444c192` 内落地的接线，覆盖 1.0.18–1.0.21（会话检索、doom-loop、worker task、playbook 控制；并行只读、预算软提醒、`credential_status`；限速、会话花费上限、`memory_timeline`；Anthropic 流式、park 落盘、soul 进 persona），并补核 1.0.16–1.0.17 的 worker reminder hub、colony→session 事件桥、MCP 白名单、overseer 输入与 REST reminder 镜像。
- 审计对象：`docs/key_concepts/` 下 7 篇（colony、coordination、goals_outcome、improvement、queen、the_loop、worker_agent）
- 判定口径（严格）：
  - **已接**＝从 `cmd/hive` → `internal/cli` 或 `internal/server` 的 HTTP 路由 / 会话工具 / agent loop 真实可达，且在生产路径上生效；仅测试调用、仅枚举、仅提示文案不算。机制本身是为某类 agent 设计的（如 worker 专用工具），该类 agent 在生产可达即算已接。
  - **部分**＝有真实入口但只实现了一半（只读不写、只内存不落盘、只覆盖一个阶段），或一个合并行里两种机制只有一种生效。
  - **未接**＝代码存在但生产入口走不到（死代码 / 仅测试 / 只有枚举与名字）。
- 证据口径：
  - `L`＝交付侧既有 live 实测，出处 `go_hive/docs/support-matrix.md`（本轮只读审计未重跑）。该表早于 1.0.16–1.0.21，**不覆盖** skill 工具、run_playbook、trigger/Sentinel、MCP 白名单、soul、会话检索、并行只读、park 落盘等新增项。
  - `C`＝本轮从 `cmd/hive` → CLI/server 路由逐段静态追踪，并用 `go build ./...` 验证可编译；测试本身不计入接线判定。
- 说明：只读审计，不改产品代码、不改测试、不改 `go.mod`。行号以 `c444c192` 为准；Go 模块根为 `go_hive/go-hive/`。

## 汇总

| 概念 | 已接 | 部分 | 未接 | 合计 |
| --- | ---: | ---: | ---: | ---: |
| colony | 10 | 2 | 0 | 12 |
| coordination | 10 | 1 | 0 | 11 |
| goals_outcome | 6 | 1 | 3 | 10 |
| improvement | 7 | 1 | 0 | 8 |
| queen | 9 | 4 | 0 | 13 |
| the_loop | 11 | 4 | 0 | 15 |
| worker_agent | 11 | 0 | 0 | 11 |
| **合计** | **64** | **13** | **3** | **80** |

---

## 1. colony — 殖民地即部署单元

概念主张：colony 是单目录、可移植、可定时唤醒、跨会话持久的部署单元；Queen 与 worker 是同一个 loop；Queen 通过 `run_worker` 在运行时按需扩展；Create Colony 对话框确认后才 fork。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 单目录 `~/.hive/colonies/<name>/` + scaffold | `internal/hivehome/home.go`（`ColoniesDir`）、`internal/server/routes_colonies.go:409`（`handleScaffoldColony`）、`:440`（`ensureMinimalColony`） | `POST /api/colonies/{colony_id}/scaffold`（`routes_colonies.go:60`）→ 只写 `metadata.json`、`worker.json` | 部分 | C | scaffold 仍不建 `skills/`、`triggers.json`、runs；`skills/` 改为首次 `write_skill(scope='colony')` 时懒建（`skills/skill_writer.go:170`），`tracker/` 由 runtime `Start` 补建（`colony/tracker.go:61`）。fork（`cli/colony_worker_definition.go:31`）同样只写两个 JSON。 |
| 单目录可移植 / tar 导入 | `internal/server/routes_colonies.go:42`、`internal/server/routes_colonies_import.go:705`（`handleImportColony`） | `POST /api/colonies/import`、`/import/init`、`/import/chunk/*`、`/import/finalize/*` → 落盘 `home.ColoniesDir()` | 已接 | C | — |
| Queen 与 worker 是同一个 agent loop | `internal/colony/runtime.go:982`（`runWorker`）、`:1061`（`runOverseer`）、`internal/agentloop/loop.go:478`（`ExecuteTurn`） | `run_worker` → `SpawnBatchWithOptions` → `spawnBatchOn` → `runGatedWorker` → `runWorker`；load/fork/`serve --colony` → `StartOverseer` → `runOverseer`。overseer 现在是持久 loop，`wakeCh` 醒来消费注入输入（`runtime.go:1091-1111`） | 已接 | C | 两边都跑同一个 `agentloop.AgentLoop`，但 overseer 与 chat Queen 仍是两个实例、两份对话；overseer 能收 worker 报告与 trigger，却没有客户端用户消息的投递源，因此"同一个 loop"成立、"同一个 Queen"不成立（见 queen 最后一行）。 |
| Queen 用 `run_worker` 在运行时扩展 | `internal/colony/queen_tools.go:75`（`RunWorkerToolDef`）、`:204`（`RunWorker`）、`internal/cli/session_tools.go:111`（定义入表）、`:258`（执行）；`internal/server/chat_runtime.go:879`（`toolsForTurn`）、`:896`（`toolExecuteContext`）、`:913`（`BindColony`） | 会话工具表常驻 `run_worker`；未绑 colony 时 `toolsForTurn` 过滤掉；绑定后每次调用把 runtime 放进 ctx → `RunWorker` → `SpawnBatchWithOptions`，返回 worker_ids 并启动软/硬超时看门狗（`queen_tools.go:571`） | 已接 | L | 现在 `serve --colony` 预载会话也有 `ChatRuntime`（`cli/loader_preloaded_session.go:91`），因此同样可派活；仍需带 `ChatRuntime`。colony overseer 也有 `run_worker`（`overseer_tools.go:112`），但它仍没有客户端消息入口。 |
| Create Colony 对话框 → fork 出 colony | `internal/server/routes_session_create.go:212`、`:245`；`internal/cli/loader.go:806`（`CreateColonyFromSource`）；`internal/cli/colony_worker_definition.go:31`（`writeColonyFromSession`） | `POST /api/sessions`（`colony_id` + `source_session_id`）→ `deps.CreateColonyFromSource` → 写 colony → build/start host → `createSessionWithRuntime(prebuilt)` | 已接 | C | fork 的 `worker.json` 现在写 worker 人格（`BuildSystemPrompt`）与声明工具（`colonyToolsFor`），不再抄 Queen persona；runs/workers 仍为空。 |
| colony 生命周期 load / unload | `internal/server/routes_session_colony.go:55`（load）、`:170`（unload）；`internal/cli/loader.go:727`（`LoadColony`）、`:746`（`UnloadColony`）、`:767`（`attach`） | `POST /api/sessions/{id}/colony` / `DELETE /api/sessions/{id}/colony` → `deps.Colony` → 建 runtime、`Start` + `StartOverseer`、替换/停掉旧 runtime | 已接 | C | unload 后 chat 侧绑定要等下一次 `GetSession` 才解除（`internal/server/session.go:411-420`）；卸载瞬间在跑的 turn 仍可能持有旧 runtime。 |
| `serve --colony` 启动即加载 runtime、overseer 与 chat | `internal/cli/loader.go:418`（`buildColonyHost`）、`:469`（`startColonyHost`）；`internal/cli/loader_preloaded_session.go:33`（`loadPreloadedColonySession`）、`:91`（`attachPreloadedChatRuntime`） | `hive serve --colony <path>` → `loadPreloadedColonySession` → host `Start` + `StartOverseer` → 有 provider 时装配 `ChatRuntime` + `QueenExecutor` → `RestoreSessionTriggers` → `AddSession` | 已接 | C | 旧 `L` 只覆盖"colony 能加载"，不覆盖 chat 附件。每个 `--colony` 仍新建 session id，不恢复旧会话/旧 worker；无 provider 时 `Chat` 保持 nil，`POST /chat` 仍 503。 |
| tracker 作为 colony 的共享账本（建库 + 读写） | `internal/colony/tracker.go:61`（`provisionTracker`）、`:137`（`workerInputData`）；`internal/colony/runtime.go:489`（`Start`）、`:563`（`SpawnBatchWithOptions`）；`internal/server/routes_colony_workers_data.go:134`、`:704`、`:731`、`:790` | colony `Start`/每次 spawn → `BindingForColonyDir` → `tracker/tracker.db`；worker spawn 把 binding 写进 `input_data`；REST 读表/读行/PATCH 改行走同一 DB | 已接 | L | 只对带 `metadata.json` 的目录建库；没有 metadata 的旧目录不建。 |
| colony 内 `skills/`（Queen 写、worker 加载） | `internal/skills/agent_tools.go:54`（`AgentToolDefs`）、`:178`（`ExecuteAgentTool`）、`internal/skills/skill_writer.go:169`（`WriteSkill`）、`internal/cli/session_tools.go:94`（注册）、`:250`（执行）、`internal/colony/runtime.go:404`（`SkillDirs`）、`internal/colony/skills_surface.go:40`、`:48`、`internal/server/routes_colony_workers.go:571` | chat Queen 工具表含 `write_skill` / `skill_view` / `list_skills`；`write_skill(scope='colony')` → `<colony>/skills/`；worker spawn 时 `SkillDirs` + 目录 prompt + `skill_view`/`list_skills`；`GET .../colony/skills` 从磁盘读（`runtime.go:219-224` 回退） | 已接 | C | 目录按需创建，scaffold 不预建；worker 只在 spawn 时快照，运行中 Queen 新写的 skill 下一批才可见；overseer 工具面没有 skill 工具，不能写/读。 |
| 任务计划（Queen 的 spine） | `internal/tasks/session_tools.go:747`（`BuildSessionTools`）、`internal/cli/task_tools.go:47`（`newTaskToolSet`）、`internal/cli/session_tools.go:75`；`internal/colony/overseer_tools.go:51`（`newOverseerTaskSet`） | `hive serve` → `createSessionWithRuntime` → `newSessionToolCatalogue` → `task_create/task_update/task_list/task_get`；overseer 用同一组工具，落在 `<colony>/_misc/overseer/tasks.json` | 已接 | C | worker 仍没有 task 工具；overseer 的 task 作用域是独立的 `overseer` 会话，不与 chat Queen 共享计划。 |
| cron triggers 定时唤醒 colony | `internal/server/trigger_runtime.go:416`（`StartTriggerTimer`）、`:220`（`RestoreSessionTriggers`）、`:455`（`runTimerLoop`）；`internal/cli/loader.go:606`（`QueenExecutor` 赋值）、`internal/server/chat_runtime.go:351`（`InjectTrigger`） | `POST/PATCH /api/sessions/{id}/triggers*` → `StartTriggerTimer`；fire 时 `queenNode` 非 nil → `InjectTrigger` → 排一轮 trigger turn；`serve`/load 时从 `triggers.json` 恢复 | 已接 | C | 只对 `session.ColonyID != ""` 的会话生效（符合"唤醒 colony"）；普通 DM 会话即使写了 trigger 也不会 fire。重启恢复只覆盖启动时重建的 `--colony` 会话与随后 load 的 colony。 |
| colony 跨会话持久 / 重启恢复 | `internal/colony/runtime.go:694`（`StartOverseer` 恢复 overseer 转录）、`internal/colony/worker_transcript.go:40`、`internal/colony/resume_workers.go:38`；`internal/cli/loader_preloaded_session.go:74` | 磁盘保留 colony 目录、`tracker.db`、overseer `conversations/`、每个 worker 的 `conversations/ + meta.json + result.json + events.jsonl`；重启后 `serve --colony` 恢复会话与 trigger；worker 可用 `run_worker(resume_worker_ids=…)` 续跑 | 部分 | C | 没有"进程重启自动续跑在飞 worker"的入口；`internal/colony/resume.go:11-69` 三个旧函数仍零生产调用（由 `resume_workers.go` 取代）；colony-bound chat 会话仍不走 `queen_resume_from`（`cli/loader_session_resume.go:73`）。 |

**小结**：colony 这轮补齐了三块：`serve --colony` 预载会话接上真正 chat runtime，colony skills 写读闭环，cron trigger 从"只写配置"变成真唤醒。仍然存在的问题是结构性的：overseer 有了 Queen 工具面、也能收 worker 报告与 trigger，但仍没有客户端消息入口，chat Queen 与 overseer 是两个 lead；重启只恢复"会话 + 转录"，不自动恢复在飞工作。

## 2. coordination — tracker、任务计划、事件总线、提醒中枢

概念主张：colony 没有图；Queen 与 worker 靠四个轻量底座同步——共享 tracker、持久任务计划、事件总线、提醒中枢；人机协同靠 Queen 提问停车 + Sentinel 兜底。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| tracker 共享账本（读侧 + UI 行编辑） | `internal/tracker/tracker.go:234`（`EnsureTrackerDB`）、`internal/server/routes_colony_workers_data.go:704`（列表）、`:731`（读行）、`:790`（改行）、`:913-922` | `GET /api/colonies/{id}/data/tables`、`.../rows`、`PATCH .../rows` → `EnsureTrackerDB` / `updateTableRow` | 已接 | C | 读与 UI 行编辑可用；全局数据路由另有 insert/delete。 |
| Queen 声明可写列、worker upsert、Queen 用 SQL 校验 | `internal/tracker/tools.go:1030`（注册）、`:846`（`BuildTrackerTools`）、`:156`（`TrackerSQL`）、`:731`（`WithResolver`）；`internal/cli/session_tools.go:118-137`（queen 表）、`:265-279`（执行）；`internal/colony/tracker.go:84-110`（worker 表） | queen 会话工具表含 `tracker_sql/tracker_register_writable/tracker_upsert/tracker_query`；worker 工具表含 `tracker_upsert/tracker_query`；执行时按 binding 解析 | 已接 | L | 无 colony 的 plain queen 会话也看得到 tracker 工具，调用返回 `ErrNoBinding`（没有按绑定过滤定义）。 |
| tracker binding（无 binding 拒绝写） | `internal/tracker/binding.go:67`（`BindingForColonyDir`）、`internal/colony/tracker.go:180`（`TrackerBindingFromContext`）、`internal/colony/worker.go:377`（resolver）、`internal/cli/session_tools.go:267` | spawn 时 `input_data` 带 binding；worker resolver 读 `input_data`；queen 每次调用从 ctx 取 bound runtime 的 binding；nil → `ErrNoBinding` | 已接 | L | binding 由 runtime `Start` 派生；`RuntimeOptions.Binding` 仍没有生产注入者（只有测试/宿主自备）。 |
| 持久文件型任务计划（Queen 的 spine） | `internal/tasks/session_tools.go:747`、`internal/cli/task_tools.go:47`、`internal/cli/session_tools.go:75`、`internal/server/routes_tasks.go:39` | `hive serve` 会话创建 → 工具表含 4 个 task 工具；`GET /api/sessions/{id}/tasks` 读同一 store | 已接 | C | worker 现在有自己的 task list（与 Queen 的计划互不相通）；chat Queen 与 overseer 的计划也仍各自独立。 |
| 事件总线：会话实时 transcript → UI SSE | `internal/server/chat_runtime.go:629-709`（emitter）、`internal/eventbus/emit.go`、`internal/server/chat_routes.go`（`GET /api/sessions/{id}/events`） | 每轮 emit execution/loop/assistant/tool/usage；SSE 路由读同一 bus，`events.jsonl` 落盘 | 已接 | C | — |
| 事件总线：worker 报告 → Queen 的 `[WORKER_REPORT]` | 发布 `internal/colony/runtime.go:1118`（`publishReport`）、`:1200`（`publishEvent`）；订阅/格式化 `internal/colony/queen_tools.go:492`（`SubscribeQueenReports`）、`:426`（`FormatWorkerReport`）；消费 `internal/server/chat_runtime.go:315`（`InjectWorkerReport`）、`:942`（`BindColony` 订阅） | 绑定 colony 时订阅 `SUBAGENT_REPORT` → `FormatWorkerReport` → `InjectWorkerReport` → 下一轮以非 client user message（`[External event]`）注入 | 已接 | L | 报告现在带 `duration_seconds` 与 `output_file`（`colony/worker.go:458-462`），overseer 报告不再被跳过（只过滤 `stream_id == "queen"`）。预载 colony 无 chat runtime 时仍无人消费。 |
| 提醒中枢：session start / post-tool / budget / compact / idle 注入 | `internal/server/chat_reminders.go:54`（注册 + `Hub.Start`）、`:80`；`internal/agentloop/reminder_hub.go:235`（`Start`）；触发点 `internal/agentloop/loop.go:281`（USER_PROMPT_SUBMIT）、`:340`（POST_COMPACT）、`:496`（SESSION_START）、`:740`（POST_TOOL_USE）、`:813`（TOOL_BUDGET_CHECKPOINT）；`internal/server/chat_runtime.go:537/544/602`（SESSION_START/USER_PROMPT_SUBMIT/STOP） | 建 `ChatRuntime` → 注册 6 个 source（task、colony_parallel、active_workers、searchable_tools、skills_catalog、idle）+ Sentinel escalation source（`sentinel_runtime.go:107`）→ `Bind` → `Hub.Start` | 已接 | C | `STREAM_STALLED` 的 source 没注册（`stream_stall.go` 零注册）；REST `GET /api/sessions/{id}/reminders` 已改为镜像 live hub（`routes_reminders.go:374`），不再是恒空的旧 `ReminderRuntime`。 |
| 提醒内容：fleet 感知 / tracker 快照 / 工具与技能清单 | `internal/agentloop/active_workers_reminder.go:22`、`internal/agentloop/colony_parallel_nudge.go:54`、`internal/agentloop/tool_skill_reminders.go:23`、`internal/server/chat_reminders.go:70` | active_workers + colony_parallel + searchable_tools + skills_catalog + task 均已注册 | 部分 | C | 没有 tracker 快照 source；`skills_catalog` 的 provider 在 loader 从未注入（`chat_reminders.go:36-42` 给空 provider，恒静默）；chat 没有 `SearchableToolsProvider`，该 source 只在 worker 侧有数据。 |
| 人机协同：Queen 用 `ask_user` 提问并停车 | `internal/agentloop/synthetic_tools.go:94`（`BuildAskUserTool`）、`internal/server/chat_runtime.go:1019`（入表）、`:1057`（`executeAskUser`）、`:1068`（`EmitClientInputRequested`） | 会话工具表 + `ask_user` → executor 记录 `pendingQuestions` 并发 `client_input_requested(park_reason=ask_user)`；本轮结束快照为 parked | 已接 | C | 停车状态经 `pending_input` 落盘（`chat_park.go:29`），重启后由 `restoreParkState` 恢复等待；凭据等待未实现。 |
| parked loop 落盘 + 恢复 | `internal/server/chat_park.go:29`（`persistParkState`）、`:81`（`clearParkPendingInput`）、`:108`（`restoreParkState`）；`internal/server/chat_runtime.go:216`（构造时恢复）、`:1115`（park 时落盘）、`:1247`（用户消息清除） | `ask_user` 停车 → 写 `<conversation>/cursor.json` 的 `pending_input`（reason/questions/parked_at）；`NewChatRuntime` 读到标记就恢复等待，而不是按 `turn_done` 继续；下一条真实用户消息或 trigger 清除标记 | 已接 | C | 凭据等待（`credential_form`）仍无生产者；overseer 的 park 状态仍只在内存。 |
| Sentinel：停车分类、nudge 或升级 inbox/Telegram/Slack | `internal/server/sentinel_runtime.go:41`（`NewSentinelManager`）、`:107`（`configureSentinel`）、`:165`（`newChatSentinelSource`）；`internal/sentinel/manager.go:174`（`Start`）、`:230`（`RefreshListeners`）；`internal/cli/loader.go:300-304` | `hive serve` 启动即建 manager + `Start`（打开 escalation drain 与 inbox/Telegram/Slack listener）→ 每个 chat runtime 注册 `EscalationSource`（受 colony 通知配置 `SentinelEnabled` 门控）→ 停车时分类 continue/nudge/done/escalate；live 会话的回复经 `InjectEvent` 回到 Queen | 已接 | C | 只在 colony 绑定且 `notifications.sentinel_enabled` 打开时运行；已关闭会话无法冷恢复（`ErrSentinelColdRestoreUnsupported`）；分类质量未做 live 验证。 |

**小结**：四个底座里三个半真正生效：任务计划、transcript 事件总线、tracker、以及 6 source + Sentinel 的提醒中枢。Sentinel 从"只有配置"变成"serve 启动即装配的停车分类器"，park 状态也从内存变成 `pending_input` 落盘并可在重启后恢复。仍缺的是提醒内容里的 tracker 快照、skills catalog 与 chat 侧 searchable tools 数据源（凭据等待亦未实现）。

## 3. goals_outcome — 目标与结果驱动

概念主张：用可度量的成功标准、硬/软约束和上下文声明结果；结构化 `Goal` 的成功标准成为 loop judge 的判据；judge 可 accept/retry/escalate，retry 反馈回注；会话式结果则落到任务计划与 tracker 列上校验。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 从 agent 定义加载结构化 `Goal` | `internal/agents/agent_spec.go:439`、`internal/host/host.go:28`、`internal/colony/runtime.go:240`（`Goal()`） | `hive serve --colony` / load / fork → `newColonyHost` → `LoadAgentSpec` → `Host.Config.Goal` → `RuntimeOptions.Goal` | 部分 | C | runtime 只保存不消费；`Goal()` 仍无生产调用者（只有测试）；`Goal.Description/Context/Constraints` 不进 prompt；chat Queen 根本没有 Goal。 |
| 成功标准带权重、可度量（`output_contains` / `output_equals` / `llm_judge` / `custom`） | `internal/schemas/goal.go:142`（`IsSuccess`）、`:201`（`Validate`） | 无 | 未接 | — | `IsSuccess` 零调用；没有代码按 `Metric` 执行检查；judge 只读自然语言 `AgentSpec.SuccessCriteria`，不读加权标准。 |
| 约束（hard=MUST / soft=SHOULD）与 Context 写入 agent prompt | `internal/schemas/goal.go:157`（`ToPromptContext`）、`internal/agentloop/types.go:109`（`GoalContext`）、`internal/agentloop/loop.go:460` | 无 | 未接 | — | `ToPromptContext` 零调用；`AgentContext.GoalContext` 生产代码从未赋值，`BuildInitialMessage` 里的拼接永不命中。 |
| success_criteria → `AgentSpec` 编译 | `internal/agents/agent_spec.go:439-441`、`internal/agentloop/criteria.go:12`、`internal/agentloop/judge.go:92`（`ConfigureJudge`） | `LoadAgentSpec` 把 node-level 字符串或 goal 列表编译成 `AgentSpec.SuccessCriteria`；worker/overseer 构造 loop 后 `ConfigureJudge` 据此决定是否建 judge | 已接 | C | chat Queen 没有 criteria 来源；worker.json 的 `goal.success_criteria` 列表是可用的 Go 侧扩展。 |
| loop judge：每轮评估 accept / retry / escalate | `internal/agentloop/judge.go:109`（`shouldJudge`）、`:127`（`evaluateJudge`）、`internal/agentloop/loop.go:618`、`:772` | worker/overseer 构造 loop → `ConfigureJudge`（`colony/runtime.go:1028`、`:1080`）；criteria 非空时在自然停止/工具失败/每 N 轮评估 | 已接 | C | 只有 colony worker 路径有 criteria；chat Queen 无 criteria 也无 `ConfigureJudge`；overseer 的 `queenOverseerSpec` 无 criteria，实际不评估。 |
| retry 反馈回注会话 | `internal/agentloop/judge.go:198`（`injectJudgeFeedback`）、`internal/agentloop/loop.go:626`、`:784` | RETRY → 写一条 `[Judge feedback]` user message，下一轮继续 | 已接 | C | 依赖上一条；chat Queen 无 judge 即无 RETRY。 |
| judge 小流水线（输出键检查 → 自定义 judge / LLM 质量检查） | `internal/agentloop/llm_judge.go:36` / `:57`、`internal/llmjudge/judge.go:111`、`internal/eventbus/emit.go:363` | `ConfigureJudge` → adapter → `llmjudge.Evaluate`；verdict 通过 bus 发 `judge_verdict` | 已接 | C | 只在 colony worker 路径；`hive skill eval` 仍用同一 `llmjudge` 但不同入口。 |
| 会话式结果：Queen 把消息转成任务计划 | `internal/tasks/session_tools.go:747`、`internal/cli/task_tools.go:47`、`internal/cli/session_tools.go:75` | 同 coordination 任务计划 | 已接 | C | — |
| 会话式结果：以 tracker 列作为 "done" 并用 SQL 校验 | `internal/tracker/tools.go:156/633`、`internal/cli/session_tools.go:265` | Queen 可 `tracker_sql`/`tracker_query`；worker `tracker_upsert` 推进状态；`run_playbook` 也以 `pending_sql` 选未完成行 | 已接 | L | 框架不强制判定 "done"；是否查询/校验由模型自行发起。 |
| 未达标信号驱动改进（missed criterion / 空 tracker 列） | 无 | 无 | 未接 | — | judge RETRY 只回注文本，未映射成 improvement 信号；空 tracker 列无检测。 |

**小结**：执行侧完整（criteria → judge → retry → tracker/playbook 收敛），声明侧仍空：结构化 `Goal` 的权重/约束/Context 不消费，chat Queen 没有 criteria 来源。"目标驱动"主要发生在 worker，不在 Queen。

## 4. improvement — colony 如何变好

概念主张：四条 in-band 改进机制——会话内 reflexion（含 judge retry 反馈）、跨会话的作用域记忆（反射写入 + recall 召回 + 过往对话召回）、学到的 skills（`write_skill` → `skills/` → worker 目录），以及把 pilot 固化成 playbook（确定性 runner、重试/退避/死信、按 tracker 现状 resume）。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Reflexion 第一层：工具结果/错误回注，下一轮自我修正 | `internal/agentloop/loop.go:756`（`AddToolResult(..., IsError)`）、`internal/agentloop/conversation.go` | `ChatRuntime.runTurn` → `ExecuteTurn`，每批工具结果写回 conversation | 已接 | C | — |
| Reflexion 第二层：judge retry 的 critique 回注 | `internal/agentloop/judge.go:198`、`internal/agentloop/loop.go:626/784` | colony worker（带 criteria）RETRY → `[Judge feedback]` user message | 已接 | C | 只覆盖 colony worker；chat Queen 无 judge。 |
| 跨会话记忆：冷却门控的 reflection 写入 global / per-queen markdown | `internal/server/chat_runtime.go:221-231`（`ReflectionScheduler`）、`:607`（`OnTurnDone` 调用点）；`internal/memory/reflection.go:1026` | `hive serve` → `createSessionWithRuntime` → `NewChatRuntime`（`HomeRoot` 非空）→ `runTurn` 成功后 `OnTurnDone(ctx,"queen","turn_done")` | 已接 | C | 仅 chat Queen；worker/colony 无 reflection。 |
| 跨会话记忆：recall selector 在后续会话召回相关笔记 | `internal/server/chat_runtime.go:784`（`injectRecall`）；`internal/memory/recall.go:174` | 每条用户消息后、`ExecuteTurn` 前以 `<system-reminder>` 注入；内容不变时用 `lastRecall` 去重 | 已接 | C | — |
| 过往对话召回：每轮最多 6 条 dated excerpts；`search_messages` 可搜历史 | `internal/cli/session_tools_search.go:43`（`searchToolDefs`）、`:168`（`executeSearchTool`）、`internal/cli/session_tools.go:103`（入会话工具表）；`internal/memory/search.go:534`（`SearchMessages`）、`:678`（envelope）、`internal/memory/search_timeline.go:63` | 会话创建 → `newSessionToolCatalogue` 常驻 `search_messages`；`search_timeline` 在 `memory_timeline` flag 打开时追加；执行按会话 queen 目录解析 scope（不从模型输入取） → `memory.SearchMessages` 读全部历史会话 | 部分 | C | 工具侧已通；但 `chat_runtime` 仍不主动注入"每轮 6 条 dated excerpts"，召回仍靠模型主动调用。 |
| 学到的 skills：`write_skill` 写进 colony `skills/`，worker 目录按需加载 | `internal/skills/agent_tools.go:54/178`、`internal/skills/skill_writer.go:169`、`internal/cli/session_tools.go:94/250`、`internal/colony/runtime.go:404`、`internal/colony/skills_surface.go:40` | Queen 调 `write_skill(scope='colony')` → `<colony>/skills/<name>/SKILL.md`；worker spawn 时拿到 `SkillDirs` + `SkillsCatalogPrompt` + `skill_view`/`list_skills`；`GET .../colony/skills` 读同一目录 | 已接 | C | 只在 spawn 时快照；没有自动"从成功中提炼 skill"的触发器，仍靠模型主动写。 |
| Playbook：确定性 runner 收敛剩余批次（重试/退避/死信） | `internal/colony/playbook_tools.go:129`（`RunPlaybook`）、`internal/playbook/runner.go:190`（`NewPlaybookRun`）、`:423`（`Converge`）、`:37-56`（`DeadLetter`） | chat Queen 会话工具表含 `run_playbook`（`cli/session_tools.go:104`）；绑定 colony 后 → `RunPlaybook` → 解析 JSON playbook → `playbook.NewPlaybookRun` → 按 `pending_sql` 查行、并发派 worker、重试/退避/死信、同步返回 | 已接 | C | 只支持 native JSON playbook；`.play.py` 明确 `status='unsupported'` 且不派任何 worker（`playbook_tools.go:235`）；同步执行，长批次会占住一轮工具调用；`action='status'/'stop'` 提供在飞运行查看与取消（`playbook_control.go:120/157`）。 |
| Playbook 的 resume by construction（以 tracker 现状决定剩余工作） | `internal/playbook/rows.go`（`QueryRows`）、`internal/colony/playbook_tools.go:178-185`（inline 落盘）、`:198`（`recordPlaybookRun`） | 每次 `run_playbook(playbook_name=…)` 重新查 `pending_sql`，已 done 行自然退出；run log 写 colony.db 的 `playbook_runs` | 已接 | C | 有 `action='status'`（在飞 + 历史）与 `action='stop'`（按 `run_id` 取消，已 done 行保留）；没有后台 [PLAYBOOK_COMPLETE] 通知，resume 仍靠重跑。 |

**小结**：improvement 从"记住 + 重试"扩展到"记住 + 重试 + 固化"：skill 写读闭环、Go-native playbook 与 `search_messages` 会话检索都接上了。仍缺"每轮自动 dated excerpts"的召回，也没有自动提炼 skill/playbook 的触发器——固化仍靠 Queen 主动发起。

## 5. queen — 常驻的客户面向 lead

概念主张：Queen 是身份化 persona（13 个 YAML）而非通用 coordinator；她拥有会话、计划、tracker；带作用域记忆（global + per-queen）；两个阶段（independent → colony）；提问时停车、Sentinel 兜底。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 13 个 Queen persona（YAML profile，含 traits/background/behavior） | `internal/queen/queen_defaults/*.yaml`（13 个）、`internal/queen/profiles.go:37`、`internal/server/routes_queens.go:1006` | `hive serve` 注册 Queen 路由，profile 列表/详情/创建/改名/删除读写 YAML 目录 | 已接 | L | — |
| persona 注入 Queen 的 system prompt | `internal/queen/identity.go:85`、`internal/server/chat_runtime.go:1085-1095`（identity + soul + base prompt）、`:248`；`internal/cli/loader.go:484`（`queenOverseerSpec`） | `createSessionWithRuntime` → `NewChatRuntime` → `IdentityPrompt`；chat 路径先拼 persona，再拼 `soul.PromptFor`（`internal/soul/store.go:405`）；colony 路径 → `StartOverseer(queenOverseerSpec(...))` | 已接 | C | `default` queen 无 profile 时回退通用 `chatIdentityPrompt`（预期）；soul 只叠加在 chat Queen，overseer 的 spec 不读 soul。 |
| 选择一个 Queen 并开一个绑定会话 | `internal/server/routes_queens_sessions.go:85`、`:58`、`internal/cli/loader_session_resume.go:38` | `POST /api/queen/{queen_id}/sessions` → `deps.CreateSession` → `QueenName` 进入会话记录、`queens/<name>/sessions/<id>` 目录、记忆 scope | 已接 | C | `initial_phase` 仍被忽略（不影响绑定本身）。 |
| 两个阶段：Independent → Colony | `internal/server/routes_session_create.go:155-172`、`:212-252`、`internal/server/session.go:50`（`PhaseState`） | `POST /api/sessions` 可 fork；但 `createSessionWithRuntime` 不消费 `InitialPhase`，`PhaseState` 恒 nil（全仓无赋值） | 部分 | C | phase 状态没有落点；`GET /api/queen/{id}/tools` 在 `PhaseState == nil` 时直接 404（`routes_queens_tools.go:581`）。 |
| Queen 的持久会话与历史恢复 | `internal/server/chat_runtime.go:255`、`internal/agentloop/conversation.go:1126`、`:1213`、`internal/cli/loader_session_resume.go:38` | `NewChatRuntime` 从会话目录恢复 parts；`queen_resume_from` 复用旧 id 与旧目录 | 已接 | C | colony-bound 会话仍不走 `queen_resume_from`（`loader_session_resume.go:73`）。 |
| Queen 的 per-queen 记忆：reflection 写入 + recall 召回 | `internal/server/chat_runtime.go:221/607/784`；`internal/memory/memory.go`、`recall.go:174` | 同 improvement 3/4 | 已接 | C | — |
| Queen 的过往对话召回：每轮 6 条 dated excerpts + `search_messages` | `internal/cli/session_tools_search.go:43/168`、`internal/cli/session_tools.go:103`；`internal/memory/search.go:534`、`search_timeline.go:63` | 会话工具表常驻 `search_messages`（scope 来自会话目录）；`search_timeline` 受 `memory_timeline` flag 门控 | 部分 | C | 工具已通；每轮自动 dated excerpts 仍未注入。 |
| `memory_timeline` flag 打开后生成/召回事件时间线 | `internal/cli/memory_timeline.go:42`（`memoryTimelineToolDefs`）、`:104`（`executeMemoryTimelineTool`）、`internal/cli/session_tools.go:112`；`internal/memory/timeline.go:616`、`:491`、`:528`、`:645` | flag（env `HIVE_MEMORY_TIMELINE` 或 `hive.config.json`）打开后，`memory_timeline` 作为按天浏览工具进入会话表，`search_timeline` 也一并广告；两者读 `memory.SearchTimeline` | 部分 | C | 读/浏览侧已接；`ExtractTimeline`/`UpdateSessionTimeline`/`BackfillTimelines`/`SubscribeTimelineTriggers` 仍零生产调用，时间线没有写入者，工具通常读到空。 |
| Queen 拥有任务计划（计划可见、可编辑、跨 reload） | `internal/tasks/session_tools.go:747`、`internal/server/routes_tasks.go:39` | 同 coordination 任务计划 | 已接 | C | — |
| Queen 拥有 tracker（建 schema、派活、SQL 校验） | `internal/tracker/tools.go:1030`、`internal/cli/session_tools.go:118-137`、`internal/colony/tracker.go:180` | 会话工具表常驻 4 个 tracker 工具；绑定 colony 后按 runtime binding 执行；无绑定返回 `ErrNoBinding` | 已接 | L | 未绑 colony 时工具仍可见但不可用。 |
| 提问停车（`ask_user`） | `internal/agentloop/synthetic_tools.go:94`、`internal/server/chat_runtime.go:1019/1057/1068` | chat 会话工具表始终追加 `ask_user`；executor 记录 `pendingQuestions` 并发 `client_input_requested(park_reason=ask_user)`；本轮结束快照为 parked | 已接 | C | 停车状态经 `pending_input` 落盘（`chat_park.go:29`），重启后恢复；凭据等待未实现。 |
| Sentinel：停车后 nudge / 升级 inbox/Telegram/Slack 并恢复 | `internal/server/sentinel_runtime.go:41/107`、`internal/sentinel/manager.go:174/230` | `serve` 启动 manager；每个 colony-bound chat runtime 注册 escalation source；parked 时分类并 nudge/升级；live 会话回复经 `InjectEvent` 回到 Queen | 已接 | C | 需要 colony 通知配置打开 Sentinel；dead session 不能冷恢复；闭环未 live 实测。 |
| Queen 与 colony overseer 是同一个 lead | `internal/cli/loader.go:484`（`queenOverseerSpec`）、`internal/colony/runtime.go:1061`（`runOverseer`）、`internal/colony/overseer_tools.go:112`、`internal/server/chat_colony_bridge.go:112`（`forwardToOverseer`）、`internal/colony/session_bridge.go:66`（`InjectOverseer`） | load/fork/`serve --colony` → `StartOverseer` 起持久 worker（`overseer:<colony>`）；chat runtime 绑定 colony 后，worker 报告（`chat_runtime.go:982`）与 framework trigger（`:376`）经 `InjectOverseer` 投递进 overseer | 部分 | C | 已有报告与 trigger 输入源，但仍没有"客户端用户消息 → overseer"的投递路径；它与 chat Queen 不共享对话/计划/tracker 视图。客户端实际对话的仍是另一个 chat Queen。 |

**小结**：Queen 的 persona、绑定会话、持久/恢复、记忆、任务计划、`ask_user`、tracker、Sentinel 都接通；`run_worker` 与 `run_playbook` 让她能派活和跑批次；soul 现在叠加进她的 identity prompt。残留断点从"Queen 不会派活"变成"colony 里有两个 Queen"：overseer 已能收到 worker 报告与 trigger，但没有客户端消息入口，chat Queen 有输入但不在 overseer 的对话里。

## 6. the_loop — 唯一的执行原语

概念主张：一个多轮流式 LLM 会话循环；每轮流式输出、并行跑只读工具、回注结果、决定停止/继续；judge 自纠错；Queen/worker 只是一组 `LoopConfig` 差异；内建崩溃恢复（cursor、park/resume）、上下文压缩、计量与提醒。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 单一 agent loop 作为 Queen 的执行体 | `internal/agentloop/loop.go:478`（`ExecuteTurn`）、`internal/server/chat_runtime.go:271`（`NewAgentLoop`）、`:479`（`runTurn`） | `hive serve` → `createChatSession` → `NewChatRuntime` → 每轮 `runTurn` 调用同一 loop | 已接 | C | — |
| 流式输出（文本边生成边推） | `internal/llm/stream.go:33`、`internal/llm/stream_openai.go:42`、`internal/server/chat_stream.go:113`、`internal/server/chat_runtime.go:496`、`:699/709` | CLI provider 实现 `StreamCompleter` → `newChatStreamingCompleter` 包装 → loop 的 `AComplete` 内部走 SSE → 每帧 `client_output_delta` | 已接 | L | MiniMax 仍返回 `ErrStreamingUnsupported` 并回退非流式；Anthropic 已走 Messages SSE（`internal/llm/stream_openai.go:67` → `stream_anthropic.go:48`）；tool-call 参数对 Anthropic 也按 `input_json_delta` 聚合，不逐片下发。 |
| 只读工具并行执行、其余按序 | `internal/agentloop/loop.go:61`（`ParallelReadOnlyTools`）、`:88`（默认 `true`）、`:878`（`executeToolBatch` 只在整批都是 `ConcurrencySafe` 时并发）、`:895-915`（`allToolsConcurrencySafe`） | 同一 loop；`NewAgentLoop` 的 `DefaultLoopConfig` 开并行，chat 与 worker 都走 `NewAgentLoop`；批内全部显式安全才并发，任一未标记则整批串行 | 已接 | C | 并发执行仍按原顺序发 `ToolCallStarted`/`Completed`；未声明 `ConcurrencySafe` 的工具（含 MCP）继续串行。 |
| 工具结果回注，循环直到不再调用工具 | `internal/agentloop/loop.go:614`、`:756`（`AddToolResult`） | `ChatRuntime.runTurn` 使用该路径 | 已接 | C | — |
| 自纠错第一层：错误结果进入下一轮上下文 | `internal/agentloop/loop.go:756`（`ToolResultOptions{IsError: ...}`） | 同上 | 已接 | C | — |
| judge：accept / retry / escalate，retry 反馈注入 | `internal/agentloop/judge.go:92/109/127/198`、`internal/agentloop/loop.go:618/772` | `ConfigureJudge` 在 colony worker/overseer 构造时注入（`colony/runtime.go:1028/1080`）；criteria 非空才评估 | 已接 | C | chat Queen 无 criteria → `Judge == nil`，永不评估；overseer 无 criteria 同样不评估。 |
| stall / doom-loop 检测 | `internal/agentloop/doom_loop.go:54`（`DoomLoopDetector`）、`:81`（`Observe`）、`internal/agentloop/loop.go:932`（每批工具后观察）、`:786`（advisory 追加到工具结果尾部）；stall 侧 `internal/server/stall_check.go:79`（`SubscribeStallCheck`） | doom-loop：`NewAgentLoop` 建 detector，连续 3 次同名同参调用后往回注结果追加 `<system-reminder>`（不打断 turn）；stall：`SubscribeStallCheck` 仍无生产调用 | 部分 | C | doom-loop 只有 advisory 提醒，不产生 `ParkReasonDoomLoop`；stall_check 仍零订阅，其内部 `AskJudge` 从不运行。 |
| Queen 与 worker 的 `LoopConfig` 差异（worker 3+1、30/90/200；Queen soft checkpoint 30/50） | `internal/agentloop/loop.go:49-68`（默认 50 轮、`ToolCallBudget=0`、`ToolCallLifetimeBudget=0`）、`internal/colony/worker_definition.go:9`、`worker_loop_config.go:46` | `run_worker` → `BuildWorkerLoopConfig` 应用 worker 数值；Queen 用 `DefaultLoopConfig()` | 部分 | C | worker 数值生产可达；Queen 仍没有 30/50 软检查点（`ToolCallBudget=0`，`softBudgetCheckpointBlock` 因此永不触发）。会话花费上限另有实现：`SessionCostLimitUSD` 由 `HIVE_SESSION_COST_LIMIT_USD` 播种，到额后下一轮 `ExecuteTurn` 返回 `SessionCostLimitError`（`session_cost.go:38/53/86`、`loop.go:524/636`），但 `max_cost_per_run` 仍只是 schema 字段。 |
| Grace iteration：worker 预算耗尽仍有最后一次 `report_to_parent` | `internal/agentloop/loop.go:96`（`graceTerminalTools`）、`:529-556`、`internal/colony/worker_definition.go:9-16` + `worker_loop_config.go:46-52` | worker `GraceIterations=1`；grace 轮只允许 `report_to_parent`/`tracker_upsert`/`task_update` | 已接 | C | `task_update` 现在可达（worker 自带 4 个 task 工具，`worker_task_tools.go:77`），三项终态工具均可用。 |
| 迭代上限与干净停止 | `internal/agentloop/loop.go:797`、`internal/server/chat_runtime.go:479` | `MaxIterations=50` 时 `runTurn` 走 `executionFailed` | 已接 | C | — |
| 崩溃恢复：cursor 持久化 + 会话恢复 | `internal/agentloop/conversation.go:1126`（`persist`）、`:1213`（`RestoreConversation`）、`internal/server/chat_runtime.go:255`；worker 侧 `colony/worker_transcript.go:40`、`resume_workers.go:38` | chat 每条消息落盘并更新 cursor；`NewChatRuntime` 恢复 parts；worker transcript 落盘且可用 `resume_worker_ids` 续跑 | 已接 | C | colony-bound chat 会话仍不走 `queen_resume_from`。 |
| park / resume：等待人类回答或凭据后从磁盘继续 | `internal/server/chat_park.go:29`（落盘）、`:81`（清除）、`:108`（恢复）；`internal/server/chat_runtime.go:216/1115/1247`、`:824`（`injectHubReminders`）；`internal/agentloop/park_reason.go` | `ask_user` 停车 → `cursor.json` 的 `pending_input` 落盘；`NewChatRuntime` 恢复等待；下一条用户消息或 trigger 清除标记后继续 | 部分 | C | 人类回答的落盘/恢复已接；`credential_form` 停车仍无生产者，overseer 的 park 状态也仍只在内存。 |
| 上下文压缩：自动检测并压缩；超大工具结果落盘可搜 | `internal/agentloop/loop.go:320`（`compactConversationIfNeeded`）、`internal/agentloop/conversation.go:736`（`NeedsCompaction`）、`:931`（`Compact`）、`internal/agentloop/llm_compact.go:22`；手动路由 `routes_execution.go:444` → `:638` | 每轮 LLM 调用前检查 `NeedsCompaction()` → `LlmCompact` → `conv.Compact`，并触发 PRE/POST_COMPACT；另有 `POST /api/sessions/{id}/compact-and-fork` | 部分 | C | 自动压缩已接；spillover 仍没有生成者（`SpilloverPath`/`ExtractSpilloverFilename` 只读不写），超长工具结果不会自动落盘。 |
| 每次 LLM 调用计量（tokens/cost）与工具预算上限 | `internal/agentloop/loop.go:595`（`LLMTurnComplete`）、`internal/server/chat_runtime.go:713`（emit usage）、`loop.go:625-760`（soft/hard/lifetime 预算） | 每次调用发 `llm_turn_complete`（input/output/cached/cost）；工具计数与 defer 在 loop 内执行 | 已接 | C | — |
| 框架注入提醒保持连贯 | `internal/agentloop/reminder_hub.go`、`internal/server/chat_reminders.go:54`、`internal/server/chat_runtime.go:824` | 同 coordination 提醒 | 已接 | C | `STREAM_STALLED` 未注册；REST reminder surface 已镜像 live hub（`routes_reminders.go:374`），与 chat hub 同源。 |

**小结**：the_loop 骨架完整：流式（含 Anthropic Messages SSE）、并行只读工具、judge、grace、压缩、计量、提醒都真实运行；worker transcript 落盘补上了崩溃恢复的一大块。硬缺口只剩 stall 检测（doom-loop 已是 advisory，不打断 turn），park 的凭据等待与 spillover 生产者仍是半截。

## 7. worker_agent — 短命的并行克隆

概念主张：worker 是 Queen loop 的克隆，带 worker prompt、Queen 工具的子集、更紧的预算、一个任务注入；专注/短命/fail-fast，不能升级、不能再委派；终态 `report_to_parent(status, summary, data)` → Queen 收到 `[WORKER_REPORT]`；有 grace iteration；通过 tracker / 自己的 task list / colony skills 共享；会话可崩溃恢复，`run_worker(resume_worker_ids=…)` 可续跑；大量工作以 headless 方式运行。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| worker 是 Queen loop 的克隆（同一 `AgentLoop`） | `internal/colony/runtime.go:982`（`runWorker`）、`:563`（`spawnBatchOn` 上游入口）、`internal/colony/run.go:220`、`internal/host/host.go:165` | `run_worker` → `SpawnBatchWithOptions` → `spawnBatchOn` → `runGatedWorker` → `runWorker` 新建 `agentloop.AgentLoop` | 已接 | C | `SpawnBatchWithOptions` 生产可达；`SpawnBatch`（无 options）、`SpawnRun`、`Host.Spawn`/`Run` 仍无生产调用（没有路由/工具使用它们）。 |
| worker prompt（聚焦、无升级、无委派、fail-fast 规则）真正注入 | `internal/colony/worker_definition.go:39`（`WorkerSystemPrompt`）、`:113`（`BuildSystemPrompt`）、`internal/cli/colony_worker_definition.go:58` | fork 写 `worker.json.system_prompt = BuildSystemPrompt(goal)`；`runWorker` 用 `systemPromptFor(spec)`，spec 来自 `LoadAgentSpec` 读取的同一 `system_prompt` | 已接 | C | 旧 colony（193ace3d 之前 fork 的）仍带 Queen persona，读取时原样保留；没有迁移。 |
| 工具子集 + 更紧预算（3+1 轮、30/轮、硬停 90、200 终身） | `internal/colony/runtime.go:825`（`workerTools`）、`:856`（`baseWorkerTools`）、`internal/colony/worker_tool_selection.go:28`（`selectWorkerDeclaredTools`）、`internal/colony/worker_definition.go:9`、`worker_loop_config.go:46` | worker 工具 = builtin base + `worker.json.tools` 从 integration/MCP 池选出的项 + worker tracker + `report_to_parent` + `skill_view`/`list_skills`；预算由 `BuildWorkerLoopConfig` 应用 | 已接 | C | builtin base 始终保留（不是严格"Queen 子集"）；`worker.json.tools` 为空时只能拿到 builtin；声明了未注册的名字会被静默丢弃。 |
| `report_to_parent(status, summary, data)` 终态工具 | `internal/agentloop/synthetic_tools.go:279`、`internal/colony/runtime.go:842`（加入 worker 表）、`:876`（执行器记录 report） | worker 工具表注入；executor 调 `worker.RecordExplicitReport`；`StopAfterToolBatch` 让本轮终止 | 已接 | C | `HandleReportToParent` 本身仍零调用（执行器走 `RecordExplicitReport` 等价实现）。 |
| worker 报告 → Queen 的 `[WORKER_REPORT]` 回合 | `internal/colony/runtime.go:1118`（`publishReport`）、`internal/colony/queen_tools.go:492`（订阅）、`:426`（格式化）、`internal/server/chat_runtime.go:315` | 终态 `SUBAGENT_REPORT` → `FormatWorkerReport` → `InjectWorkerReport` → Queen 下一轮 | 已接 | L | 报告已有 `duration_seconds`/`output_file`；预载 colony 无 chat 时无消费者。 |
| Grace iteration：预算耗尽后仅 `report_to_parent`/`task_update`/`tracker_upsert` 可执行 | `internal/agentloop/loop.go:96`、`:529-556`、`internal/colony/worker_definition.go:9-16` + `worker_loop_config.go:46-52` | worker `GraceIterations=1`；grace 轮过滤非终态工具 | 已接 | C | `task_update` 可达（worker 自带 task 工具），三项终态工具均可用。 |
| 结果共享：tracker | `internal/tracker/tools.go:1030`、`internal/colony/tracker.go:109-130`、`runtime.go:849` | worker 工具表带 `tracker_upsert/tracker_query`；executor 按 `input_data.binding` 执行 | 已接 | L | 只覆盖 worker 角色表；`tracker_sql`/`register_writable` 仍是 Queen 专用（符合概念）。 |
| 结果共享：自己的 task list | `internal/colony/worker_task_tools.go:42`（`newWorkerTaskToolSet`）、`:77`（`workerTaskToolDefs`）、`internal/colony/runtime.go:853`（定义入 worker 表）、`:889`（按 worker 目录建 set）、`:916`（执行） | `runWorker` → `workerTools` 追加 4 个 task 定义 → executor 用 worker 自己的目录 `<workerDir>/_misc/<id>/tasks.json` 执行；两个 worker 不共享列表 | 已接 | C | 列表只属于 worker 自己，Queen 不轮询；worker 结束时任务状态不汇总到 Queen 的计划。 |
| 结果共享：colony skills | `internal/skills/agent_tools.go:54`、`internal/colony/runtime.go:404`、`internal/colony/skills_surface.go:40` | spawn 时 `SkillDirs` + `SkillsCatalogPrompt`；worker 工具表带 `skill_view`/`list_skills` | 已接 | C | 无 `write_skill`（符合"worker 只读"）；运行中新增 skill 要等下一批。 |
| 会话崩溃恢复 + `run_worker(resume_worker_ids=…)` 续跑 | `internal/colony/resume_workers.go:38`（`ResumeWorkers`）、`internal/colony/worker_transcript.go:40`、`internal/colony/queen_tools.go:231-280` | worker transcript 落盘到 `<colony>/workers/<id>/conversations`；`RunWorker` 收到 `resume_worker_ids` → `ResumeWorkers` 恢复同一 conversation（可带 guidance）→ 继续 loop；逐 id 返回失败原因 | 已接 | C | 活动中的 worker 拒绝 resume；没有 meta.json 的旧 worker 不能 resume；resume 后的预算按 pinned 处理，不做自适应收紧。 |
| headless 运行 + Sentinel 升级人工 | `internal/cli/loader.go:418/469`、`internal/cli/loader_preloaded_session.go:33`、`internal/server/sentinel_runtime.go:41/107` | `hive serve --colony` 在无 UI 情况下起 runtime + overseer + chat；worker 在服务端跑；Sentinel 在停车时升级 inbox/Telegram/Slack | 已接 | C | 入口是常驻 `serve`，没有一次性 headless run 命令；Sentinel 受 colony 通知配置门控。 |

**小结**：worker_agent 这轮补上了身份、工具面、skills、task list 与 resume：fork 写 worker 人格，`worker.json.tools` 真的展开成工具，worker 有自己的 4 个 task 工具，transcript 落盘且能续跑，报告字段齐全。仍未完成的是 worker 与 Queen 的计划/提醒面共享（worker 的 task list 自用，不汇总）。

---

## 8. 重点核查清单（按当前 HEAD 逐项）

| 核查项 | 结论 | 依据 / 缺口 |
| --- | --- | --- |
| `run_worker` 工具 | **已接（live）** | `queen_tools.go:75/204` + `cli/session_tools.go:111/258` + `chat_runtime.go:879/896`；绑定 colony 后真实 spawn 并回传；预载 `serve --colony` 会话同样可用。 |
| `resume_worker_ids` | **已接（新）** | `colony/resume_workers.go:38` + `queen_tools.go:231-280` + `worker_transcript.go:40`；逐 id 从磁盘 transcript 续跑。 |
| worker `SpawnBatch` / `SpawnRun` | **部分** | `SpawnBatchWithOptions` 经 `run_worker` 生产可达；`SpawnBatch`、`SpawnRun`、`Host.Spawn`/`Host.Run` 仍零生产调用。 |
| `[WORKER_REPORT]` 注入 | **已接（live）** | `queen_tools.go:492/426` + `chat_runtime.go:315/942`；报告带 duration/output_file，overseer 报告不再被跳过；同一报告也转投 overseer（`chat_runtime.go:982`）。 |
| colony 事件 → session SSE | **已接（新）** | `server/chat_colony_bridge.go:44/72` 把 colony 生命周期事件镜像到 session bus；`BindColony`/`AddSession` 时挂桥（`chat_runtime.go:950`）。没有独立的 `/api/colonies/{id}/events`，只在有 chat 会话绑定时可见。 |
| worker reminder hub | **已接（新）** | `colony/worker_reminders.go:114` 用 `NewAgentLoop`；`:120` task source、`:127` idle nudge、`:135` `Hub.Start`；worker 侧提醒真正注入 loop。 |
| overseer 输入 | **部分** | `chat_colony_bridge.go:112` + `session_bridge.go:66`：worker 报告与 trigger 已能投递；客户端用户消息仍无投递路由。 |
| REST reminders | **已接（新）** | `routes_reminders.go:374` 读 live hub（`chat_runtime.go:297` 注册），GET 只读镜像、DELETE 直接 dismiss；不再是恒空的旧 `ReminderRuntime`。 |
| `tracker_*` 工具（queen / worker / overseer） | **已接（live）** | queen 4 个（`session_tools.go:118-137`）、worker 2 个（`colony/tracker.go:109`）、overseer queen 表（`overseer_tools.go:123`）；均按 binding 执行。 |
| tracker binding | **已接（live）** | `tracker/binding.go:67` + `colony/tracker.go:137/180` + `session_tools.go:267`；无 binding 返回 `ErrNoBinding`。 |
| 提醒 sources 与 `Hub.Start` | **已接** | `chat_reminders.go:54-80` 注册 6 个 source + Sentinel source；SESSION_START/USER_PROMPT_SUBMIT/POST_TOOL_USE/PRE+POST_COMPACT/STOP/IDLE_TICK/TOOL_BUDGET_CHECKPOINT 均触发。缺口：STREAM_STALLED 未注册、skills catalog 无数据、chat 无 searchable tools 数据。 |
| judge / LLM adapter 与 `judge_verdict` | **已接（colony worker 路径）** | `judge.go:92/109/127` + `colony/runtime.go:1028/1080` + `eventbus/emit.go:363`；chat Queen 仍 judge-free。 |
| `StreamCompleter` 与 `client_output_delta` | **已接（live）** | `llm/stream.go:33` + `stream_openai.go:42/67` + `chat_stream.go:113` + `chat_runtime.go:699/709`；Anthropic 走 Messages SSE，MiniMax 回退。 |
| `ask_user` / task 工具 | **已接** | `synthetic_tools.go:94` + `chat_runtime.go:1019/1057`；`task_*` 在 `cli/session_tools.go:75/83`；worker 也有自己的 task 工具（`worker_task_tools.go:77`）；overseer 同样有（`overseer_tools.go:112`）。 |
| colony load / unload / `CreateColonyFromSource` | **已接** | `loader.go:727/746/806` + 路由 + chat 绑定；失败按 4xx/5xx 上报。 |
| `serve --colony` 预载会话 | **已接（新）** | `loader_preloaded_session.go:33/91`：`Chat` + `QueenExecutor` + trigger 恢复；无 provider 时仍 503。 |
| Queen persona / memory / resume | **已接** | `chat_runtime.go:1085` persona（identity + soul）、`:221/607` reflection、`:784` recall、`loader_session_resume.go:38` resume；colony-bound 会话不支持 resume。 |
| Sentinel | **已接（新）** | `sentinel_runtime.go:41/107` + `loader.go:300-304` + `manager.Start/RefreshListeners`；受 colony 通知配置门控。 |
| `skills.WriteSkill` / agent skill 工具 | **已接（新）** | `skills/agent_tools.go:54/178` + `cli/session_tools.go:94/250` + `colony/runtime.go:404`；write/view/list 全通。 |
| triggers / `QueenExecutor` | **已接（新）** | `loader.go:606` 与 `loader_preloaded_session.go:122` 赋值；`chat_runtime.go:351` 实现 `InjectTrigger`；`trigger_runtime.go:220/293/416` 恢复与 fire。 |
| playbook runner / `run_playbook` | **已接（新，Go native）** | `colony/playbook_tools.go:76/129/168` + `internal/playbook`；`action='status'/'stop'` 见 `playbook_control.go:120/157`；Python `.play.py` 明确 unsupported，0 派发。 |
| MCP 启动装载 | **已接（新）** | `cli/mcp_loader.go:66/102/131` + `tools/mcp_bootstrap.go:66` + `registry.go:150`；每个新 catalogue 自动 attach。 |
| MCP 工具白名单 | **已接（新）** | `host/host.go:89-112` 把进程发现的名字集并入 runtime；`applyToolAllowlist`（`colony/runtime.go:321`）按 `tools.json` 过滤；chat 侧 `filterToolsByColonyAllowlist` 同源。未配置白名单时不过滤（预期）。 |
| Soul 沉淀 | **已接（新）** | `chat_runtime.go:229`（`OnReflect`）+ `:1259`（`sedimentSoul`）+ `soul/sediment.go`；soul 已进 chat Queen 的 identity prompt（`:1085-1095`），overseer spec 不读 soul。 |
| `search_messages` / `search_timeline` | **部分** | 工具已入会话表并可执行（`cli/session_tools_search.go:43/168`、`session_tools.go:103`）；但"每轮 6 条 dated excerpts"仍不注入。 |
| `memory_timeline` 浏览工具 | **部分** | `cli/memory_timeline.go:42/104` 读侧已接；timeline 提取/回填/订阅仍零生产调用。 |
| `stall_check` / doom-loop | **部分** | doom-loop 检测已接（`agentloop/doom_loop.go:81`、`loop.go:932/786`，advisory）；`SubscribeStallCheck` 仍零调用。 |
| 并行只读工具 | **已接（新）** | `loop.go:61/88/878`：`ParallelReadOnlyTools` 默认开，整批 `ConcurrencySafe` 才并发。 |
| 预算执行（Queen 软检查点 / 会话花费上限） | **部分** | 会话花费硬上限已执行（`session_cost.go:38/53/86`、`loop.go:524`）；Queen `ToolCallBudget=0`，软检查点代码存在但不触发；`max_cost_per_run` 仍无执行者。 |
| rate limit（LLM + 工具） | **已接（新）** | `llm/transport.go:45` 给 HTTP provider 装 `AcquireLLMRequest`；`agentloop/tool_rate_limit.go:23/33` + `loop.go:994` 按 `rate_limits.tools.*` 拒绝并回注可重试结果；未配置时 no-op。 |
| `credential_status` | **已接（只读，新）** | `cli/session_tools_credentials.go:26/37` + `session_tools.go:120`；只列可用性，不返回值；无写凭据工具。 |
| park 落盘 / resume | **部分** | `chat_park.go:29/81/108` + `chat_runtime.go:216/1115/1247`；`ask_user` 可跨重启恢复，`credential_form` 仍无生产者。 |
| Anthropic 流式 | **已接（新）** | `llm/stream_openai.go:67` 分流到 `stream_anthropic.go:48`（Messages SSE，含 `input_json_delta` 聚合）。 |

## 9. 总体结论

- **真正接通的**：chat Queen 的完整 loop（同步/流式，Anthropic 走 Messages SSE）、`run_worker` + worker spawn + worker 身份/声明工具 + worker 自带 task list + `[WORKER_REPORT]`（带 duration/output_file）、worker transcript 落盘与 `resume_worker_ids`、tracker 建库/binding/读写、提醒中枢（6+1 source + ticker + 主要注入点 + worker 侧 hub + REST 镜像）、judge（colony worker + criteria）、自动压缩、任务计划、persona/记忆/恢复、colony load/unload/fork、`serve --colony` 预载 chat、`ask_user` + park 落盘、triggers 唤醒与恢复、Sentinel 停车分类、skill 读写工具、Go-native `run_playbook`（含 status/stop）、MCP 启动装载与白名单、soul 沉淀并进 persona、会话检索三工具、并行只读工具、doom-loop advisory、`credential_status`、LLM/工具限速、会话花费硬上限。
- **最大的结构性断点**：colony 里有两个 Queen。overseer 现在有 `run_worker`/queen tracker/task 工具、持久 transcript，并能收到 worker 报告与 trigger；但仍没有"客户端用户消息 → overseer"的投递路径，客户端对话的是另建的 chat Queen。两者不共享对话、计划或视图。
- **worker 的能力边界**：身份、预算、grace、report、tracker、skills、task list、resume 都对了；工具面仍是"builtin + 声明项"而不是严格 Queen 子集，运行中新增 skill 不可见，task list 自用不汇总。
- **"代码在但没接"的典型**：`stall_check`（`SubscribeStallCheck` 零订阅）、`credential_form` 停车、`ExtractTimeline`/`BackfillTimelines`/`SubscribeTimelineTriggers`（时间线只有读侧工具）、`InstallWorkerEscalationRouting`（worker 升级到 session 的路由零调用）、spillover 生产者。
- **口径提醒**：本审计只回答"代码能否从生产入口走到、走到了是否生效"，不评价上游 Python 的行为；`L` 只表示交付侧有实测记录且未重跑，本轮所有新接线均为 `C`。

## 10. 上一轮差异（历史存档：`4b6f1cf4` 相对 `ed457d75`）

> 本节保留上一版（基线 `4b6f1cf4`）的迁移记录，不再代表当前状态；当前状态见汇总表与 §11。

上一版基线 `ed457d75` 的计数为 48 已接 / 10 部分 / 22 未接（80 行）。`4b6f1cf4` 版为 **61 / 9 / 10（80 行）**。行集不变，逐条迁移如下。

### 10.1 未接 → 已接（11 条）

| # | 条目 | 依据 |
| --- | --- | --- |
| 1 | colony：colony 内 `skills/` | `skills/agent_tools.go:54/178` + `cli/session_tools.go:94/250` + `colony/runtime.go:404` + `GET .../colony/skills` 读盘 |
| 2 | colony：cron triggers 定时唤醒 | `trigger_runtime.go:416/220` + `chat_runtime.go:351` + `loader.go:606` |
| 3 | coordination：Sentinel 停车分类 | `sentinel_runtime.go:41/107` + `loader.go:300-304` + `manager.Start` |
| 4 | improvement：学到的 skills | 同 1 |
| 5 | improvement：Playbook 确定性 runner | `colony/playbook_tools.go:129` + `internal/playbook` |
| 6 | improvement：Playbook resume by construction | `playbook_name` 重跑 + `pending_sql` 重查 + `RecordPlaybookRun` |
| 7 | queen：Sentinel | 同 3 |
| 8 | worker_agent：worker prompt 真正注入 | `colony_worker_definition.go:58` + `worker_definition.go:113` + `runWorker` |
| 9 | worker_agent：colony skills | 同 1（worker 侧 `SkillDirs` + `skill_view`/`list_skills`） |
| 10 | worker_agent：崩溃恢复 + `resume_worker_ids` | `resume_workers.go:38` + `worker_transcript.go:40` |
| 11 | worker_agent：headless + Sentinel 升级 | `serve --colony` + manager.Sentinel |

### 10.2 未接 → 部分（1 条）

| # | 条目 | 依据 |
| --- | --- | --- |
| 1 | queen：Queen 与 colony overseer 是同一个 lead | overseer 有 Queen 工具面、持久 loop、transcript 恢复（`overseer_tools.go:112`、`runtime.go:1061`），但没有输入源，仍是独立对话。 |

### 10.3 部分 → 已接（2 条）

| # | 条目 | 依据 |
| --- | --- | --- |
| 1 | colony：Queen 与 worker 是同一个 loop | overseer 变持久并消费注入输入（`runtime.go:1091-1111`）；worker/overseer 共用 `ExecuteTurn` |
| 2 | worker_agent：工具子集 + 更紧预算 | `worker_tool_selection.go:28` 展开 `worker.json.tools`，integration/MCP 池可选；预算不变 |

### 10.4 未变（66 条）

其余 66 行状态不变：未出现在 10.1–10.3 的所有行（colony 的 scaffold/import/load-unload/`serve --colony`/tracker/task/persistence；coordination 的 tracker 三行、task、SSE、report、reminders、ask_user、park；goals 全部 10 行；improvement 的 reflexion 两行、reflection、recall、search_messages；queen 的 persona 两行、绑定、phase、resume、memory、search、timeline、task、tracker、ask_user；the_loop 全部 15 行；worker 的 loop、report、grace、tracker、task）。

### 10.5 当时新发现 / 变化了的缺口（历史）

| 缺口 | 说明 |
| --- | --- |
| overseer 无输入源 | 它有 `run_worker`/tracker/task 与持久 loop，但生产代码没有把用户消息或 worker 报告投递给它；`InjectInput` 只用于 worker 软超时与 escalation queue-full。 |
| 两个 Queen 两份状态 | chat Queen 与 overseer 各有 conversation、task list、tracker 视图；同一 colony 的"lead"没有唯一实例。 |
| MCP 白名单不生效 | `newColonyHost` 不传 `MCPToolNames`，`applyToolAllowlist` 的 gated 集合恒空；`routes_colony_tools.go:530` 的 `SetToolAllowlist` 只写配置，不改变可见工具。 |
| chat 的 skills catalog 仍空 | `chat_reminders.go:36-42` 给空 provider；`skills_catalog` source 注册但恒静默。 |
| REST reminders 恒空 | `serveReminders` 仍在 `loader.go:315` 注入，但 chat 不再 `TurnComplete`。 |
| worker 升级路由未装 | `InstallWorkerEscalationRouting`（`queen_orchestrator.go:19`）零生产调用，worker 的 escalation 事件不会进 session 队列。 |
| `run_playbook` 定义不按 colony 过滤 | `toolsForTurn` 只过滤 `run_worker`；无 colony 时会话仍看到 `run_playbook`，调用返回 no-colony 错误。 |
| `serve --colony` 不恢复旧 worker | 预载会话每次新 id；在飞 worker 只能靠 `resume_worker_ids` 手动续。 |

### 10.6 当时仍未接、原因已变 / 未变（历史）

| 条目 | 上一版原因 | 当前原因 |
| --- | --- | --- |
| `search_messages` / 过往对话召回 | 无工具定义 | 仍无；`SearchMessages` 零调用 |
| `stall_check` | 零订阅 | 仍零调用；只在 config 展示 |
| 并行只读工具 | 顺序执行 | 仍顺序；`ConcurrencySafe` 未消费 |
| Queen 预算软检查点 | budget=0 | 仍 0；无会话花费上限 |
| Anthropic 流式 | 明确 unsupported | 仍 unsupported |
| park 落盘/resume | 仅内存 | 仍仅内存 |
| memory timeline | flag 只展示 | 仍只展示 |
| worker 自己的 task list | 无 task 工具 | 仍无 |
| §8 的 MCP 白名单 | （上一版未单列） | 新发现：装载已接，白名单未生效 |

## 11. 本轮变更记录（1.0.16–1.0.21，基线 `c444c192`）

本轮把两版之间落地的接线逐条对到代码，结论为 **64 已接 / 13 部分 / 3 未接（80 行）**。状态变化 7 条，另有 3 条状态不变但缺口收窄。

### 11.1 未接 → 已接（3 条）

| # | 条目 | 依据（file:line @ c444c192） |
| --- | --- | --- |
| 1 | coordination：parked loop 落盘 + 恢复 | `internal/server/chat_park.go:29`（`persistParkState`）`:81`（`clearParkPendingInput`）`:108`（`restoreParkState`）；`internal/server/chat_runtime.go:216` 构造时恢复，`:1115` park 时落盘，`:1247` 用户消息清除。`ask_user` 停车写 `cursor.json` 的 `pending_input`，重启后重新等待。 |
| 2 | the_loop：只读工具并行执行、其余按序 | `internal/agentloop/loop.go:61`（`ParallelReadOnlyTools`）、`:88`（`DefaultLoopConfig` 默认 true）、`:878`（`executeToolBatch`）、`:895`（`allToolsConcurrencySafe`）。chat 与 worker 都经 `NewAgentLoop`，整批显式 `ConcurrencySafe` 才并发。 |
| 3 | worker_agent：结果共享：自己的 task list | `internal/colony/worker_task_tools.go:42`（`newWorkerTaskToolSet`）、`:77`（`workerTaskToolDefs`）；`internal/colony/runtime.go:853`（定义入 worker 表）、`:889`（按 worker 目录建 set）、`:916`（执行）。4 个 task 工具落 `<workerDir>/_misc/<id>/tasks.json`。 |

### 11.2 未接 → 部分（4 条）

| # | 条目 | 依据 / 仍缺的一半 |
| --- | --- | --- |
| 1 | improvement：过往对话召回（`search_messages`） | 工具已接：`internal/cli/session_tools_search.go:43/168` + `internal/cli/session_tools.go:103` + `internal/memory/search.go:534`。仍缺：`chat_runtime` 不注入每轮 6 条 dated excerpts。 |
| 2 | queen：Queen 的过往对话召回 | 同 1（`search_messages` 常驻会话工具表，scope 来自会话目录）。仍缺每轮自动召回。 |
| 3 | queen：`memory_timeline` flag | 读侧已接：`internal/cli/memory_timeline.go:42/104` + `session_tools.go:112`，`search_timeline` 同步广告（`session_tools_search.go:43`）。仍缺：`ExtractTimeline`/`UpdateSessionTimeline`/`BackfillTimelines`/`SubscribeTimelineTriggers` 零生产调用（`internal/memory/timeline.go:426/491/528/645`），时间线无写入者。 |
| 4 | the_loop：stall / doom-loop 检测 | doom-loop 已接：`internal/agentloop/doom_loop.go:54/81` + `internal/agentloop/loop.go:932/786`（连续 3 次同名同参，advisory，不打断）。仍缺：`SubscribeStallCheck`（`internal/server/stall_check.go:79`）零调用，`ParkReasonDoomLoop` 不产生。 |

### 11.3 部分 → 已接（0 条）

本轮没有 concept 行从"部分"翻成"已接"；`the_loop` 的 park 行因仍缺凭据等待，保持"部分"（缺口从"无落盘"收窄为"仅缺 credential_form"）。

### 11.4 状态不变但缺口收窄（3 条）

| 条目 | 旧缺口 | 当前缺口 |
| --- | --- | --- |
| queen：Queen 与 colony overseer 是同一个 lead（部分） | 无任何输入源 | `internal/server/chat_colony_bridge.go:112/129/142` + `internal/colony/session_bridge.go:66`：worker 报告与 trigger 已投递进 overseer（`chat_runtime.go:376/982`）；仍缺客户端用户消息入口，仍不共享对话/计划。 |
| the_loop：park / resume（部分） | 无落盘、无 resume、凭据等待未实现 | `chat_park.go:29/81/108` 已完成人类回答的落盘/恢复；仍缺 `credential_form` 生产者与 overseer 侧 park。 |
| coordination：提醒中枢（已接） | REST reminders 恒空 | `internal/server/routes_reminders.go:374` 镜像 live hub（`chat_runtime.go:297` 注册）；REST 与 chat hub 同源。 |

### 11.5 本轮首次计入的 1.0.16–1.0.17 接线

两版之间 `4b6f1cf4` 之后还落了五处接缝，old baseline 未覆盖；本轮一并核对，均已接：worker reminder hub（`colony/worker_reminders.go:114/120/127/135`）、colony→session 事件桥（`server/chat_colony_bridge.go:44/72`）、MCP 白名单（`host/host.go:89-112` + `colony/runtime.go:321`）、overseer 报告/trigger 输入（见 11.4）、REST reminder 镜像（见 11.4）。

### 11.6 备注 / 待验证

| 事项 | 说明 |
| --- | --- |
| 限速与花费上限的实际触发 | 静态确认执行点存在（`llm/transport.go:45`、`agentloop/tool_rate_limit.go:23/33`、`session_cost.go:38/86`），但只在配置了 `rate_limits` / `HIVE_SESSION_COST_LIMIT_USD` 时生效；本轮未跑 live 限额测试，标注为 `C`。 |
| `memory_timeline` 的读写闭环 | 读工具已接，写入侧零调用；若上游/旧数据没有 timeline 文件，工具会返回空。属"部分"，不是"已接"。 |
| overseer 的用户消息入口 | 代码中没有 HTTP/总线路由把客户端消息投给 overseer，保留"部分"；未发现隐藏入口。 |
| `credential_form` 停车 | `ParkReasonCredentialForm` 只有枚举与 idle-nudge 分支，无生产者；保留"部分"。 |
| `max_cost_per_run` | 仍是 `internal/schemas/agent_config.go:326` 的 schema 字段，运行期上限走 `HIVE_SESSION_COST_LIMIT_USD`；两者未打通，标注"部分"。 |
