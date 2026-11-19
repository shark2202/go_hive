# Go 运行时 × Key Concepts 接线审计

- 审计基线：`ab5c3dc7`（`release: 1.0.24-go.1（接线收尾 + Queen 两阶段/overseer 桥）`，Go 1.0.24；相对 `d650199d` 合并 credential 提交、预载 catalog、doom reason、judge criteria、两阶段落盘与 overseer 消息桥）
- 上一版基线：`d650199d`（`release: 1.0.23-go.1（提醒/持久性/loop 保护）`；77 已接 / 3 部分 / 0 未接，80 行）
- 更早基线：`6723ab78`（71/9/0）；`c444c192`（64/13/3）；`4b6f1cf4`（61/9/10）；`ed457d75`（48/10/22）
- 审计日期：2026-11-19
- 本轮增量：复核 `d650199d..ab5c3dc7` 落地的 1.0.24 收尾——`POST /api/sessions/{id}/credential-form`（加密 store + 清 park + 恢复）、预载会话注入 `SkillsCatalog`、chat 失败分支保留 doom-loop park reason、chat Queen/overseer 从 colony goal 继承 judge criteria、Queen 两阶段 phase 落 `meta.json` 且 PATCH 刷新 live 会话工具集、绑定 colony 的客户端消息/报告/trigger 只进 live overseer 并把回复经 session bridge 以 queen stream 呈现。
- 审计对象：`docs/key_concepts/` 下 7 篇（colony、coordination、goals_outcome、improvement、queen、the_loop、worker_agent）
- 判定口径（严格）：
  - **已接**＝从 `cmd/hive` → `internal/cli` 或 `internal/server` 的 HTTP 路由 / 会话工具 / agent loop 真实可达，且在生产路径上生效；仅测试调用、仅枚举、仅提示文案不算。机制本身是为某类 agent 设计的（如 worker 专用工具），该类 agent 在生产可达即算已接。
  - **部分**＝有真实入口但只实现了一半（只读不写、只内存不落盘、只覆盖一个阶段），或一个合并行里两种机制只有一种生效。
  - **未接**＝代码存在但生产入口走不到（死代码 / 仅测试 / 只有枚举与名字）。
- 证据口径：
  - `L`＝交付侧既有 live 实测，出处 `go_hive/docs/support-matrix.md`（本轮只读审计未重跑）。该表随 1.0.24 只更新了版本号与测试计数，**仍不覆盖** skill 工具、run_playbook、trigger/Sentinel、MCP 白名单、soul、会话检索、并行只读、park 落盘，以及本轮的 credential 提交、预载 `SkillsCatalog`、doom park reason、judge criteria、两阶段 phase、overseer 中继。
  - `C`＝本轮从 `cmd/hive` → CLI/server 路由逐段静态追踪，并用 `go build ./...` 与聚焦测试复核；测试本身不计入接线判定。
- 说明：只读审计，不改产品代码、不改测试、不改 `go.mod`。行号以 `ab5c3dc7` 为准；本轮改写的行号已复核，未改写行沿用上一版（以符号名为准）；Go 模块根为 `go_hive/go-hive/`。

## 汇总

| 概念 | 已接 | 部分 | 未接 | 合计 |
| --- | ---: | ---: | ---: | ---: |
| colony | 12 | 0 | 0 | 12 |
| coordination | 11 | 0 | 0 | 11 |
| goals_outcome | 10 | 0 | 0 | 10 |
| improvement | 8 | 0 | 0 | 8 |
| queen | 13 | 0 | 0 | 13 |
| the_loop | 15 | 0 | 0 | 15 |
| worker_agent | 11 | 0 | 0 | 11 |
| **合计** | **80** | **0** | **0** | **80** |

---

## 1. colony — 殖民地即部署单元

概念主张：colony 是单目录、可移植、可定时唤醒、跨会话持久的部署单元；Queen 与 worker 是同一个 loop；Queen 通过 `run_worker` 在运行时按需扩展；Create Colony 对话框确认后才 fork。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 单目录 `~/.hive/colonies/<name>/` + scaffold | `internal/hivehome/home.go`（`ColoniesDir`）、`internal/server/routes_colonies.go:409`（`handleScaffoldColony`）、`:440`（`ensureMinimalColony`）、`internal/server/colony_scaffold.go:31`（`ensureColonyScaffold`） | `POST /api/colonies/{colony_id}/scaffold`（`routes_colonies.go:60`）→ `ensureMinimalColony` → 写 `metadata.json`、`worker.json`，并建 `skills/`、`runs/`、空 `triggers.json`（`routes_colonies.go:474`，幂等） | 已接 | C | fork（`cli/colony_worker_definition.go:31`）仍只写两个 JSON，不建目录；`skills/`、`runs/` 在 fork 树上靠首次写入懒建，`tracker/` 由 runtime `Start` 补建（`colony/tracker.go:61`）。 |
| 单目录可移植 / tar 导入 | `internal/server/routes_colonies.go:42`、`internal/server/routes_colonies_import.go:705`（`handleImportColony`） | `POST /api/colonies/import`、`/import/init`、`/import/chunk/*`、`/import/finalize/*` → 落盘 `home.ColoniesDir()` | 已接 | C | — |
| Queen 与 worker 是同一个 agent loop | `internal/colony/runtime.go:982`（`runWorker`）、`:1137`（`runOverseer`）、`internal/agentloop/loop.go:478`（`ExecuteTurn`） | `run_worker` → `SpawnBatchWithOptions` → `spawnBatchOn` → `runGatedWorker` → `runWorker`；load/fork/`serve --colony` → `StartOverseer` → `runOverseer`。overseer 现在是持久 loop，`wakeCh` 醒来消费注入输入（`runtime.go:1196-1224`） | 已接 | C | 两边都跑同一个 `agentloop.AgentLoop`。绑定 colony 且有 live overseer 时，客户端输入由 overseer 执行、chat Queen 只做前端（`chat_runtime.go:359-372`），但两者仍是两个实例、两份 conversation；pending question/park 不共享（见 queen 最后一行）。 |
| Queen 用 `run_worker` 在运行时扩展 | `internal/colony/queen_tools.go:75`（`RunWorkerToolDef`）、`:204`（`RunWorker`）、`internal/cli/session_tools.go:111`（定义入表）、`:258`（执行）；`internal/server/chat_runtime.go:879`（`toolsForTurn`）、`:896`（`toolExecuteContext`）、`:913`（`BindColony`） | 会话工具表常驻 `run_worker`；未绑 colony 时 `toolsForTurn` 过滤掉；绑定后每次调用把 runtime 放进 ctx → `RunWorker` → `SpawnBatchWithOptions`，返回 worker_ids 并启动软/硬超时看门狗（`queen_tools.go:571`） | 已接 | L | 现在 `serve --colony` 预载会话也有 `ChatRuntime`（`cli/loader_preloaded_session.go:91`），因此同样可派活；仍需带 `ChatRuntime`。colony overseer 也有 `run_worker`（`overseer_tools.go:112`），并已通过 `chat_colony_bridge.go:187` 获得客户端消息入口。 |
| Create Colony 对话框 → fork 出 colony | `internal/server/routes_session_create.go:212`、`:245`；`internal/cli/loader.go:806`（`CreateColonyFromSource`）；`internal/cli/colony_worker_definition.go:31`（`writeColonyFromSession`） | `POST /api/sessions`（`colony_id` + `source_session_id`）→ `deps.CreateColonyFromSource` → 写 colony → build/start host → `createSessionWithRuntime(prebuilt)` | 已接 | C | fork 的 `worker.json` 现在写 worker 人格（`BuildSystemPrompt`）与声明工具（`colonyToolsFor`），不再抄 Queen persona；runs/workers 仍为空。 |
| colony 生命周期 load / unload | `internal/server/routes_session_colony.go:55`（load）、`:170`（unload）；`internal/cli/loader.go:727`（`LoadColony`）、`:746`（`UnloadColony`）、`:767`（`attach`） | `POST /api/sessions/{id}/colony` / `DELETE /api/sessions/{id}/colony` → `deps.Colony` → 建 runtime、`Start` + `StartOverseer`、替换/停掉旧 runtime | 已接 | C | unload 后 chat 侧绑定要等下一次 `GetSession` 才解除（`internal/server/session.go:411-420`）；卸载瞬间在跑的 turn 仍可能持有旧 runtime。 |
| `serve --colony` 启动即加载 runtime、overseer 与 chat | `internal/cli/loader.go:418`（`buildColonyHost`）、`:469`（`startColonyHost`）；`internal/cli/loader_preloaded_session.go:33`（`loadPreloadedColonySession`）、`:91`（`attachPreloadedChatRuntime`）、`:199`（`SkillsCatalog`） | `hive serve --colony <path>` → `loadPreloadedColonySession` → host `Start` + `StartOverseer` → 有 provider 时装配 `ChatRuntime` + `QueenExecutor` → `RestoreSessionTriggers` → `AddSession` | 已接 | C | 旧 `L` 只覆盖"colony 能加载"，不覆盖 chat 附件。预载会话现在按 metadata 的 `queen_session_id` 复用旧 id 与转录（`loader_preloaded_session.go:53/62/106`）；首次启动才新建 id。无 provider 时 `Chat` 保持 nil，`POST /chat` 仍 503；`SkillsCatalog` 已注入（`loader_preloaded_session.go:199`），但预载记录 `QueenName` 为空，实际走 `chatSessionQueenScope("") = "default"` 的 queen scope。 |
| tracker 作为 colony 的共享账本（建库 + 读写） | `internal/colony/tracker.go:61`（`provisionTracker`）、`:137`（`workerInputData`）；`internal/colony/runtime.go:489`（`Start`）、`:563`（`SpawnBatchWithOptions`）；`internal/server/routes_colony_workers_data.go:134`、`:704`、`:731`、`:790` | colony `Start`/每次 spawn → `BindingForColonyDir` → `tracker/tracker.db`；worker spawn 把 binding 写进 `input_data`；REST 读表/读行/PATCH 改行走同一 DB | 已接 | L | 只对带 `metadata.json` 的目录建库；没有 metadata 的旧目录不建。 |
| colony 内 `skills/`（Queen 写、worker 加载） | `internal/skills/agent_tools.go:54`（`AgentToolDefs`）、`:178`（`ExecuteAgentTool`）、`internal/skills/skill_writer.go:169`（`WriteSkill`）、`internal/cli/session_tools.go:94`（注册）、`:250`（执行）、`internal/colony/runtime.go:404`（`SkillDirs`）、`internal/colony/skills_surface.go:40`、`:48`、`internal/server/routes_colony_workers.go:571` | chat Queen 工具表含 `write_skill` / `skill_view` / `list_skills`；`write_skill(scope='colony')` → `<colony>/skills/`；worker spawn 时 `SkillDirs` + 目录 prompt + `skill_view`/`list_skills`；`GET .../colony/skills` 从磁盘读（`runtime.go:219-224` 回退） | 已接 | C | scaffold 已预建 `skills/`（`colony_scaffold.go:31`）；worker 只在 spawn 时快照，运行中 Queen 新写的 skill 下一批才可见；overseer 工具面没有 skill 工具，不能写/读。 |
| 任务计划（Queen 的 spine） | `internal/tasks/session_tools.go:747`（`BuildSessionTools`）、`internal/cli/task_tools.go:47`（`newTaskToolSet`）、`internal/cli/session_tools.go:75`；`internal/colony/overseer_tools.go:51`（`newOverseerTaskSet`）、`internal/colony/worker_task_tools.go:42/77`（`newWorkerTaskToolSet`） | `hive serve` → `createSessionWithRuntime` → `newSessionToolCatalogue` → `task_create/task_update/task_list/task_get`；overseer 用同一组工具，落在 `<colony>/_misc/overseer/tasks.json`；worker 也有自己的 4 个 task 工具，落在 `<workerDir>/_misc/<id>/tasks.json` | 已接 | C | worker 的 task list 只属于 worker，不汇总到 Queen；overseer 的 task 作用域是独立的 `overseer` 会话，不与 chat Queen 共享计划。 |
| cron triggers 定时唤醒 colony | `internal/server/trigger_runtime.go:416`（`StartTriggerTimer`）、`:220`（`RestoreSessionTriggers`）、`:455`（`runTimerLoop`）；`internal/cli/loader.go:606`（`QueenExecutor` 赋值）、`internal/server/chat_runtime.go:351`（`InjectTrigger`）、`:419`（`relayTriggerToOverseer`） | `POST/PATCH /api/sessions/{id}/triggers*` → `StartTriggerTimer`；fire 时 `queenNode` 非 nil → `InjectTrigger` → 有 live overseer 时转投 overseer，否则排一轮 chat trigger turn；`serve`/load 时从 `triggers.json` 恢复 | 已接 | C | 只对 `session.ColonyID != ""` 的会话生效（符合"唤醒 colony"）；普通 DM 会话即使写了 trigger 也不会 fire。重启恢复只覆盖启动时重建的 `--colony` 会话与随后 load 的 colony。 |
| colony 跨会话持久 / 重启恢复 | `internal/colony/runtime.go:694`（`StartOverseer` 恢复 overseer 转录）、`internal/colony/worker_transcript.go:40`、`internal/colony/resume_workers.go:38`、`internal/colony/worker_state.go:78/226`（运行态落盘 + 自动续跑）；`internal/cli/loader_preloaded_session.go:53/62/106`（预载会话复用 id） | 磁盘保留 colony 目录、`tracker.db`、overseer `conversations/`、每个 worker 的 `conversations/ + meta.json + state.json + result.json + events.jsonl`；`ColonyRuntime.Start` → `ResumeInFlightWorkers` 重建崩溃时非终态 worker；`serve --colony` 读 metadata 的 `queen_session_id` → `queen_resume_from` 复用旧会话与转录 → `RestoreSessionTriggers` | 已接 | C | 自动续跑只覆盖崩溃时非终态的记录（正常为 queued/running；completed/failed/stopped 不复活），优雅停止的 `stopped` 不自动复活；`POST /api/sessions` 带 colony_id 的普通创建仍不走 `queen_resume_from`（`cli/loader_session_resume.go:73`，仅预载路径复用）；旧 `internal/colony/resume.go:11-69` 三函数仍零生产调用（由 `resume_workers.go` 取代）。 |

**小结**：colony 这轮补齐了持久性：scaffold 预建 `skills/`、`runs/` 与空 `triggers.json`；worker 运行态落盘，`ColonyRuntime.Start` 自动续跑崩溃时 queued/running 的 worker；`serve --colony` 预载会话按 `queen_session_id` 复用旧会话与转录。1.0.24 又把 overseer 接到客户端消息源，并给预载会话注入 skills 目录；成对存在的仍是两份 conversation（chat 前端 + overseer），pending/park 不共享。`POST /api/sessions` 带 colony_id 的普通创建仍不复用旧会话，fork 也仍只写两个 JSON。

## 2. coordination — tracker、任务计划、事件总线、提醒中枢

概念主张：colony 没有图；Queen 与 worker 靠四个轻量底座同步——共享 tracker、持久任务计划、事件总线、提醒中枢；人机协同靠 Queen 提问停车 + Sentinel 兜底。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| tracker 共享账本（读侧 + UI 行编辑） | `internal/tracker/tracker.go:234`（`EnsureTrackerDB`）、`internal/server/routes_colony_workers_data.go:704`（列表）、`:731`（读行）、`:790`（改行）、`:913-922` | `GET /api/colonies/{id}/data/tables`、`.../rows`、`PATCH .../rows` → `EnsureTrackerDB` / `updateTableRow` | 已接 | C | 读与 UI 行编辑可用；全局数据路由另有 insert/delete。 |
| Queen 声明可写列、worker upsert、Queen 用 SQL 校验 | `internal/tracker/tools.go:1030`（注册）、`:846`（`BuildTrackerTools`）、`:156`（`TrackerSQL`）、`:731`（`WithResolver`）；`internal/cli/session_tools.go:118-137`（queen 表）、`:265-279`（执行）；`internal/colony/tracker.go:84-110`（worker 表） | queen 会话工具表含 `tracker_sql/tracker_register_writable/tracker_upsert/tracker_query`；worker 工具表含 `tracker_upsert/tracker_query`；执行时按 binding 解析 | 已接 | L | 无 colony 的 plain queen 会话也看得到 tracker 工具，调用返回 `ErrNoBinding`（没有按绑定过滤定义）。 |
| tracker binding（无 binding 拒绝写） | `internal/tracker/binding.go:67`（`BindingForColonyDir`）、`internal/colony/tracker.go:180`（`TrackerBindingFromContext`）、`internal/colony/worker.go:377`（resolver）、`internal/cli/session_tools.go:267` | spawn 时 `input_data` 带 binding；worker resolver 读 `input_data`；queen 每次调用从 ctx 取 bound runtime 的 binding；nil → `ErrNoBinding` | 已接 | L | binding 由 runtime `Start` 派生；`RuntimeOptions.Binding` 仍没有生产注入者（只有测试/宿主自备）。 |
| 持久文件型任务计划（Queen 的 spine） | `internal/tasks/session_tools.go:747`、`internal/cli/task_tools.go:47`、`internal/cli/session_tools.go:75`、`internal/server/routes_tasks.go:39` | `hive serve` 会话创建 → 工具表含 4 个 task 工具；`GET /api/sessions/{id}/tasks` 读同一 store | 已接 | C | worker 现在有自己的 task list（与 Queen 的计划互不相通）；chat Queen 与 overseer 的计划也仍各自独立。 |
| 事件总线：会话实时 transcript → UI SSE | `internal/server/chat_runtime.go:629-709`（emitter）、`internal/eventbus/emit.go`、`internal/server/chat_routes.go`（`GET /api/sessions/{id}/events`） | 每轮 emit execution/loop/assistant/tool/usage；SSE 路由读同一 bus，`events.jsonl` 落盘 | 已接 | C | — |
| 事件总线：worker 报告 → Queen 的 `[WORKER_REPORT]` | 发布 `internal/colony/runtime.go:1118`（`publishReport`）、`:1200`（`publishEvent`）；订阅/格式化 `internal/colony/queen_tools.go:492`（`SubscribeQueenReports`）、`:426`（`FormatWorkerReport`）；消费 `internal/server/chat_runtime.go:315`（`InjectWorkerReport`）、`:942`（`BindColony` 订阅）、`:1122`（报告转投 overseer） | 绑定 colony 时订阅 `SUBAGENT_REPORT` → `FormatWorkerReport` → 有 live overseer 时经 `relayWorkerReportToOverseer` 只投 overseer；overseer 自己的终态报告才注入 chat 前端成为 `[WORKER_REPORT]` 回合 | 已接 | L | 报告现在带 `duration_seconds` 与 `output_file`（`colony/worker.go:458-462`）。预载 colony 无 chat runtime 时仍无人消费；非 overseer worker 的 client IO/tool/token 事件不镜像到会话流。 |
| 提醒中枢：session start / post-tool / budget / compact / idle 注入 | `internal/server/chat_reminders.go:69`（`configureReminders`）、`:85-96`（注册 + `Hub.Start`）；`internal/agentloop/reminder_hub.go:235`（`Start`）；触发点 `internal/agentloop/loop.go:355`（USER_PROMPT_SUBMIT）、`:414`（POST_COMPACT）、`:579`（SESSION_START）、`:806`（POST_TOOL_USE）、`:887`（TOOL_BUDGET_CHECKPOINT）；`internal/server/chat_runtime.go:577/584/643`（SESSION_START/USER_PROMPT_SUBMIT/STOP） | 建 `ChatRuntime` → `configureReminders` 注册 7 个 source（task、colony_parallel、active_workers、tracker_snapshot、searchable_tools、skills_catalog、idle）+ Sentinel escalation source（`sentinel_runtime.go:107`）→ `Bind` → `Hub.Start` | 已接 | C | `STREAM_STALLED` 的 source 没注册（`stream_stall.go` 零注册）；tracker 快照只在绑定 colony 且 `IsQueenStream` 时触发；REST `GET /api/sessions/{id}/reminders` 读同一 live hub（`routes_reminders.go:374`），无 live runtime 时才回退旧 `ReminderRuntime`。绑定 colony 且有 live overseer 时执行在 overseer，chat hub 的 `skills_catalog` 不再对这次输入生效；目录改由首条中继携带。 |
| 提醒内容：fleet 感知 / tracker 快照 / 工具与技能清单 | `internal/agentloop/active_workers_reminder.go:22`、`internal/agentloop/colony_parallel_nudge.go:54`、`internal/agentloop/tracker_snapshot_reminder.go:105/135`、`internal/agentloop/tool_skill_reminders.go:23`、`internal/server/chat_reminders.go:38-54/62-64/88-90`、`internal/server/chat_tool_tiers.go:43/153`、`internal/cli/loader_reminders.go:26/30` | active_workers + colony_parallel + tracker_snapshot + searchable_tools + skills_catalog + task 均已注册；tracker source 经 `colonyBinding` 读 bound `tracker.db` 的表名与行数（50ms/10 表预算）；skills provider 从 queen 技能目录 `Load()` 后取 `SkillsCatalogPrompt()`（loader 注入，`loader.go:605`）；chat 用 `newChatToolTier` 把 MCP 工具分 eager/searchable，供 `searchable_tools` 提醒读取 | 已接 | C | 预载 `serve --colony` 已注入 `SkillsCatalog`（`loader_preloaded_session.go:199`），但 `record.QueenName` 为空 → `chatSessionQueenScope` 回退 `default`，命名 queen 的私有 skills 不会出现；tracker source 只在绑定 colony 后触发；没有 MCP loader/工具时 chat tier 为 nil，searchable 提醒无内容（无工具可搜，预期）。 |
| 人机协同：Queen 用 `ask_user` 提问并停车 | `internal/agentloop/synthetic_tools.go:94`（`BuildAskUserTool`）、`internal/server/chat_runtime.go:1078`（入表）、`:1127`（`executeAskUser`）、`:1141`（`EmitClientInputRequested`） | 会话工具表 + `ask_user` → executor 记录 `pendingQuestions` 并发 `client_input_requested(park_reason=ask_user)`；本轮结束快照为 parked | 已接 | C | 停车状态经 `pending_input` 落盘（`chat_park.go:29`），重启后由 `restoreParkState` 恢复等待；`credential_form` 的生产者（`chat_credential_park.go:43/68`）与提交路由（`routes_credentials_form.go:36`）都已存在。overseer 工具面没有 `ask_user`，其提问/park 状态也不回写 chat 的 `pendingQuestions`。 |
| parked loop 落盘 + 恢复 | `internal/server/chat_park.go:29`（`persistParkState`）、`:89`（`clearParkPendingInput`）、`:116`（`restoreParkState`）；`internal/server/chat_runtime.go:228`（构造时恢复）、`:1137`（park 时落盘）、`:1286`（用户消息清除）；`internal/server/chat_credential_park.go:43`（credential park）、`:77`（`PendingCredentialForm`）、`:90`（`ClearCredentialFormPark`）；`internal/server/routes_credentials_form.go:36`（提交路由） | `ask_user` / 缺凭据停车 → 写 `<conversation>/cursor.json` 的 `pending_input`；`NewChatRuntime` 读到标记就恢复等待；下一条真实用户消息或 trigger 清除标记；`POST /api/sessions/{id}/credential-form` 读 pending form → 写加密 store（`routes_credentials_form.go:121`）→ `syncEnv` → 清 park（`:139`）→ `InjectEvent(confirmation, true)` 恢复会话（`:144`），`cancelled` 分支只清 park 并注入取消说明（`:69-82`） | 已接 | C | 提交只接受 runtime 当前 `PendingCredentialForm`，`credential_id` 不匹配返回 400；store 只读返回 503。writer 仍是 chat runtime 的工具错误路径（`parkForMissingCredential`），overseer 执行时不会生产该 form；overseer 的 park 状态仍只在内存。 |
| Sentinel：停车分类、nudge 或升级 inbox/Telegram/Slack | `internal/server/sentinel_runtime.go:41`（`NewSentinelManager`）、`:107`（`configureSentinel`）、`:165`（`newChatSentinelSource`）；`internal/sentinel/manager.go:174`（`Start`）、`:230`（`RefreshListeners`）；`internal/cli/loader.go:300-304` | `hive serve` 启动即建 manager + `Start`（打开 escalation drain 与 inbox/Telegram/Slack listener）→ 每个 chat runtime 注册 `EscalationSource`（受 colony 通知配置 `SentinelEnabled` 门控）→ 停车时分类 continue/nudge/done/escalate；live 会话的回复经 `InjectEvent` 回到 Queen | 已接 | C | 只在 colony 绑定且 `notifications.sentinel_enabled` 打开时运行；已关闭会话无法冷恢复（`ErrSentinelColdRestoreUnsupported`）；分类质量未做 live 验证。 |

**小结**：四个底座里三个半真正生效：任务计划、transcript 事件总线、tracker、以及 7 source + Sentinel 的提醒中枢。提醒内容补齐 tracker 快照、真实 skills_catalog 与 chat 侧 searchable tools 数据源；Sentinel 从"只有配置"变成"serve 启动即装配的停车分类器"，park 状态（含 `credential_form`）从内存变成 `pending_input` 落盘并可在重启后恢复。1.0.24 补上 credential-form 提交路由与预载会话的 SkillsCatalog；仍缺 `STREAM_STALLED` source，且预载 catalog 固定走 default queen scope。

## 3. goals_outcome — 目标与结果驱动

概念主张：用可度量的成功标准、硬/软约束和上下文声明结果；结构化 `Goal` 的成功标准成为 loop judge 的判据；judge 可 accept/retry/escalate，retry 反馈回注；会话式结果则落到任务计划与 tracker 列上校验。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 从 agent 定义加载结构化 `Goal` | `internal/agents/agent_spec.go:439`、`internal/host/host.go:28`、`internal/colony/runtime.go:244`（`Goal()`）、`:827`（`buildContext` 写 `GoalContext`）、`:1104`（worker 完成时评估） | `hive serve --colony` / load / fork → `newColonyHost` → `LoadAgentSpec` → `Host.Config.Goal` → `RuntimeOptions.Goal` → `r.goal`；worker 侧两处消费：系统提示（`runtime.go:1044` → `goal_outcome.go:33`）与完成时评估（`goal_outcome.go:74`）；chat 的 `inheritColonyCriteria` 也调用 `Goal()` 取 criteria 文本（`chat_runtime.go:1085`） | 已接 | C | chat Queen 没有 Goal（只继承 goal 的 criteria 文本给 judge）；persistent overseer 的 goal context 被显式置空（`runtime.go:827` 的 `!worker.Persistent`）。 |
| 成功标准带权重、可度量（`output_contains` / `output_equals` / `llm_judge` / `custom`） | `internal/agentloop/goal_eval.go:144`（`EvaluateGoal`）、`:181`（`weightedMetFraction`）、`:197`（逐条 metric）、`internal/schemas/goal.go:142`（`IsSuccess`）；调用 `internal/colony/goal_outcome.go:74/90`、`internal/colony/runtime.go:1104` | `run_worker` → `runWorker` 完成 → `evaluateWorkerGoal` → `agentloop.EvaluateGoal(ctx, r.goal, output, judge, request)`：逐条跑 metric、写回 `Met`、用 `Goal.IsSuccess()`（加权 ≥90%）定成败；结果进 `WorkerResult.goal_evaluation`、`result.json` 的 `data`、`[WORKER_REPORT]` 的 `<data>` 与 `goal_progress` 事件（`eventbus/emit.go:82`） | 已接 | C | `custom` 与未知 metric 明确 `supported:false`（不当作达标）；`llm_judge` 与默认 `success_rate` 需 judge，worker 路径经 `ConfigureJudge` 有；chat Queen 无 Goal，不做加权 `EvaluateGoal`，只通过继承的 criteria 参与 judge。 |
| 约束（hard=MUST / soft=SHOULD）与 Context 写入 agent prompt | `internal/schemas/goal.go:157`（`ToPromptContext`）、`internal/agentloop/types.go:109`（`GoalContext`）、`internal/agentloop/loop.go:513`（首条消息追加 `Goal:`）；赋值 `internal/colony/runtime.go:827-828`，渲染 `internal/colony/goal_outcome.go:33`（`workerSystemPrompt`） | `runWorker` → `workerSystemPrompt(worker, spec)` 把 `Goal.ToPromptContext()` 追加进 worker 系统提示（`runtime.go:1044`）；`buildContext` 同时写 `ctx.GoalContext`，空转录时首条用户消息再带一份（`loop.go:513`） | 已接 | C | 只进 worker：overseer 的 goal context 被显式置空，chat Queen 无 Goal；约束只是 prompt 文本，`EmitConstraintViolation`（`eventbus/emit.go:91`）仍无生产者，没有运行期校验。 |
| success_criteria → `AgentSpec` 编译 | `internal/agents/agent_spec.go:439-441`、`internal/agentloop/criteria.go:12/37`（`GoalCriteriaText`）、`internal/agentloop/judge.go:92`（`ConfigureJudge`）、`internal/server/chat_runtime.go:1077`（`inheritColonyCriteria`） | `LoadAgentSpec` 把 node-level 字符串或 goal 列表编译成 `AgentSpec.SuccessCriteria`；worker 构造 loop 后 `ConfigureJudge`（`colony/runtime.go:1078`）；spec 没带 criteria 时用 goal 的 criterion 描述回填（`runtime.go:1073-1077`），overseer 走 `:1160-1167`；chat runtime 在 `BindColony` 时从 bound colony 的 goal 回填并 `ConfigureJudge`（`chat_runtime.go:1077-1089`） | 已接 | C | chat Queen 自己的 `AgentSpec` 没有 criteria 来源，只有 `ChatRuntimeDeps.SuccessCriteria` 或绑定 colony 的 goal 继承；plain DM 会话 judge 仍关闭。`GoalCriteriaText` 只取 `Description`，权重与 metric 不参与 judge 文本（metric 由 `EvaluateGoal` 单独执行）。 |
| loop judge：每轮评估 accept / retry / escalate | `internal/agentloop/judge.go:109`（`shouldJudge`）、`:127`（`evaluateJudge`）、`internal/agentloop/loop.go:618`、`:772` | worker 构造 loop → `ConfigureJudge`（`colony/runtime.go:1078`）；overseer `:1160-1167` 在 spec 无 criteria 时继承 goal；chat runtime 构造时 `configureCriteriaJudge`（`chat_runtime.go:321/1061`），绑定 colony 后再用 goal 回填（`:1077-1089`）；criteria 非空时在自然停止/工具失败/每 N 轮评估 | 已接 | C | 无 criteria 的 plain chat 会话仍不评估；chat Queen 在绑定 colony 且有 live overseer 时只做前端，实际被 judge 的是 overseer 的回合；overseer 的 `queenOverseerSpec` 本身无 criteria。 |
| retry 反馈回注会话 | `internal/agentloop/judge.go:198`（`injectJudgeFeedback`）、`internal/agentloop/loop.go:626`、`:784` | RETRY → 写一条 `[Judge feedback]` user message，下一轮继续 | 已接 | C | 依赖上一条；无 criteria 的 chat 会话无 judge 即无 RETRY；绑定 colony 的 chat 前端不执行 model turn，retry 发生在 overseer/worker 的 loop 内。 |
| judge 小流水线（输出键检查 → 自定义 judge / LLM 质量检查） | `internal/agentloop/llm_judge.go:36` / `:57`、`internal/llmjudge/judge.go:111`、`internal/eventbus/emit.go:363` | `ConfigureJudge` → adapter → `llmjudge.Evaluate`；verdict 通过 bus 发 `judge_verdict` | 已接 | C | 现在覆盖 colony worker、继承 goal criteria 的 overseer、以及有 criteria 的 chat Queen；`hive skill eval` 仍用同一 `llmjudge` 但不同入口。 |
| 会话式结果：Queen 把消息转成任务计划 | `internal/tasks/session_tools.go:747`、`internal/cli/task_tools.go:47`、`internal/cli/session_tools.go:75` | 同 coordination 任务计划 | 已接 | C | — |
| 会话式结果：以 tracker 列作为 "done" 并用 SQL 校验 | `internal/tracker/tools.go:156/633`、`internal/cli/session_tools.go:265` | Queen 可 `tracker_sql`/`tracker_query`；worker `tracker_upsert` 推进状态；`run_playbook` 也以 `pending_sql` 选未完成行 | 已接 | L | 框架不强制判定 "done"；是否查询/校验由模型自行发起。 |
| 未达标信号驱动改进（missed criterion / 空 tracker 列） | `internal/colony/improvement.go:124`（`RecordImprovementSignal`）、`:151`（`improvement_signal` 事件）、`:197`（`recordGoalImprovementSignals`）、`:238`（judge RETRY/ESCALATE）、`:260`（`detectEmptyTrackerColumns`）；调用 `internal/colony/runtime.go:1081-1083`（`OnJudgeVerdict`）、`:1106`；落盘 `<colony>/improvement/signals.jsonl` | `runWorker` 完成 → 未达标/不支持的 criterion 逐条写 `signals.jsonl` 并发布 colony bus `kind=improvement_signal`；judge RETRY/ESCALATE 经 `loop.OnJudgeVerdict` 同路；注册过但零行的 tracker 表产生 `empty_tracker`；同一批信号并入 `publishReport` 的 `<data>`，随 `[WORKER_REPORT]` 到 Queen | 已接 | C | 信号只写不读：`ImprovementSignals()` 无调用者，也没有 REST/CLI 读面（release notes 已记为"尚无自动再派工消费者"）；`improvement_signal` 是 custom 事件，不在 `colonyBridgeEventTypes` 镜像清单内，UI 看不到。 |

**小结**：goals_outcome 本轮从 6/1/3 变为 10/0/0。worker 侧把结构化 `Goal` 真正跑完：加权标准在完成时逐条执行（`EvaluateGoal` → `IsSuccess`），`[MUST]`/`[SHOULD]` 约束与 Context 进系统提示，未达标 criterion 与 judge RETRY 变成可读的改进信号并随 `[WORKER_REPORT]` 回到 Queen。1.0.24 又把 criteria 继承接到 chat Queen（绑定 colony 时）与 overseer（无 spec criteria 时从 goal 回填）。剩余边界不是接线而是作用域：chat Queen 没有 Goal、约束没有运行期校验、改进信号还没有自动消费者。

## 4. improvement — colony 如何变好

概念主张：四条 in-band 改进机制——会话内 reflexion（含 judge retry 反馈）、跨会话的作用域记忆（反射写入 + recall 召回 + 过往对话召回）、学到的 skills（`write_skill` → `skills/` → worker 目录），以及把 pilot 固化成 playbook（确定性 runner、重试/退避/死信、按 tracker 现状 resume）。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Reflexion 第一层：工具结果/错误回注，下一轮自我修正 | `internal/agentloop/loop.go:756`（`AddToolResult(..., IsError)`）、`internal/agentloop/conversation.go` | `ChatRuntime.runTurn` → `ExecuteTurn`，每批工具结果写回 conversation | 已接 | C | — |
| Reflexion 第二层：judge retry 的 critique 回注 | `internal/agentloop/judge.go:198`、`internal/agentloop/loop.go:626/784` | colony worker（带 criteria）RETRY → `[Judge feedback]` user message；有 criteria 的 chat Queen 与继承 goal criteria 的 overseer 同路径 | 已接 | C | plain chat（无 criteria）仍无 judge；绑定 colony 的 chat 前端不执行 model turn，retry 在 overseer/worker 的 loop 内。 |
| 跨会话记忆：冷却门控的 reflection 写入 global / per-queen markdown | `internal/server/chat_runtime.go:221-231`（`ReflectionScheduler`）、`:607`（`OnTurnDone` 调用点）；`internal/memory/reflection.go:1026` | `hive serve` → `createSessionWithRuntime` → `NewChatRuntime`（`HomeRoot` 非空）→ `runTurn` 成功后 `OnTurnDone(ctx,"queen","turn_done")` | 已接 | C | 仅 chat Queen；worker/colony 无 reflection。 |
| 跨会话记忆：recall selector 在后续会话召回相关笔记 | `internal/server/chat_runtime.go:784`（`injectRecall`）；`internal/memory/recall.go:174` | 每条用户消息后、`ExecuteTurn` 前以 `<system-reminder>` 注入；内容不变时用 `lastRecall` 去重 | 已接 | C | — |
| 过往对话召回：每轮最多 6 条 dated excerpts；`search_messages` 可搜历史 | 工具侧 `internal/cli/session_tools_search.go:43`（`searchToolDefs`）、`:168`（`executeSearchTool`）、`internal/cli/session_tools.go:103`（入会话工具表）；注入侧 `internal/memory/past_conversations.go:222`（`RecallPastConversations`）、`:477`（`RenderPastConversationExcerpts`）、`:511`（`PastConversationRecallBlock`）、`internal/server/chat_memory_wiring.go:31`（`injectPastConversationRecall`）、调用点 `internal/server/chat_runtime.go:607` | 每条 client 用户消息在 `ExecuteTurn` 前：`injectRecall` 之后追加一段 `<system-reminder>`，BM25 + 中文切分取 `PastRecallTopK=6`（`past_conversations.go:13`）条带日期摘录，`lastPastRecall` 去重；`search_messages` 仍常驻会话工具表 | 已接 | C | 只在 chat Queen 的 client 输入轮注入（worker 报告/trigger 不触发）；`HIVE_PAST_RECALL_TIMEOUT` 默认 3s，超时即放弃；excerpts 与 `search_messages` 各自独立召回。 |
| 学到的 skills：`write_skill` 写进 colony `skills/`，worker 目录按需加载 | `internal/skills/agent_tools.go:54/178`、`internal/skills/skill_writer.go:169`、`internal/cli/session_tools.go:94/250`、`internal/colony/runtime.go:404`、`internal/colony/skills_surface.go:40` | Queen 调 `write_skill(scope='colony')` → `<colony>/skills/<name>/SKILL.md`；worker spawn 时拿到 `SkillDirs` + `SkillsCatalogPrompt` + `skill_view`/`list_skills`；`GET .../colony/skills` 读同一目录 | 已接 | C | 只在 spawn 时快照；没有自动"从成功中提炼 skill"的触发器，仍靠模型主动写。 |
| Playbook：确定性 runner 收敛剩余批次（重试/退避/死信） | `internal/colony/playbook_tools.go:129`（`RunPlaybook`）、`internal/playbook/runner.go:190`（`NewPlaybookRun`）、`:423`（`Converge`）、`:37-56`（`DeadLetter`） | chat Queen 会话工具表含 `run_playbook`（`cli/session_tools.go:104`）；绑定 colony 后 → `RunPlaybook` → 解析 JSON playbook → `playbook.NewPlaybookRun` → 按 `pending_sql` 查行、并发派 worker、重试/退避/死信、同步返回 | 已接 | C | 只支持 native JSON playbook；`.play.py` 明确 `status='unsupported'` 且不派任何 worker（`playbook_tools.go:235`）；同步执行，长批次会占住一轮工具调用；`action='status'/'stop'` 提供在飞运行查看与取消（`playbook_control.go:120/157`）。 |
| Playbook 的 resume by construction（以 tracker 现状决定剩余工作） | `internal/playbook/rows.go`（`QueryRows`）、`internal/colony/playbook_tools.go:178-185`（inline 落盘）、`:198`（`recordPlaybookRun`） | 每次 `run_playbook(playbook_name=…)` 重新查 `pending_sql`，已 done 行自然退出；run log 写 colony.db 的 `playbook_runs` | 已接 | C | 有 `action='status'`（在飞 + 历史）与 `action='stop'`（按 `run_id` 取消，已 done 行保留）；没有后台 [PLAYBOOK_COMPLETE] 通知，resume 仍靠重跑。 |

**小结**：improvement 从"记住 + 重试"扩展到"记住 + 重试 + 固化 + 主动召回"：skill 写读闭环、Go-native playbook、`search_messages`，以及每轮自动的 6 条过往对话 excerpts 都接上了。仍缺自动提炼 skill/playbook 的触发器——固化仍靠 Queen 主动发起。

## 5. queen — 常驻的客户面向 lead

概念主张：Queen 是身份化 persona（13 个 YAML）而非通用 coordinator；她拥有会话、计划、tracker；带作用域记忆（global + per-queen）；两个阶段（independent → colony）；提问时停车、Sentinel 兜底。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 13 个 Queen persona（YAML profile，含 traits/background/behavior） | `internal/queen/queen_defaults/*.yaml`（13 个）、`internal/queen/profiles.go:37`、`internal/server/routes_queens.go:1006` | `hive serve` 注册 Queen 路由，profile 列表/详情/创建/改名/删除读写 YAML 目录 | 已接 | L | — |
| persona 注入 Queen 的 system prompt | `internal/queen/identity.go:85`、`internal/server/chat_runtime.go:1085-1095`（identity + soul + base prompt）、`:248`；`internal/cli/loader.go:484`（`queenOverseerSpec`） | `createSessionWithRuntime` → `NewChatRuntime` → `IdentityPrompt`；chat 路径先拼 persona，再拼 `soul.PromptFor`（`internal/soul/store.go:405`）；colony 路径 → `StartOverseer(queenOverseerSpec(...))` | 已接 | C | `default` queen 无 profile 时回退通用 `chatIdentityPrompt`（预期）；soul 只叠加在 chat Queen，overseer 的 spec 不读 soul。 |
| 选择一个 Queen 并开一个绑定会话 | `internal/server/routes_queens_sessions.go:85`、`:58`、`internal/cli/loader_session_resume.go:38`、`:163`（resume 时应用 `initial_phase`） | `POST /api/queen/{queen_id}/sessions` → `deps.CreateSession` → `QueenName` 进入会话记录、`queens/<name>/sessions/<id>` 目录、记忆 scope；`initial_phase` 随创建参数进入 phase cascade | 已接 | C | `initial_phase` 已消费；命名 queen 的 profile 只在 `queenScope != "default"` 时写入 phase identity。 |
| 两个阶段：Independent → Colony | `internal/server/routes_session_create.go:155-172`、`:212-252`、`internal/server/session.go:50`（`PhaseState`）、`internal/server/queen_phase_state.go:150/180/220/244/262`、`internal/cli/loader.go:561`、`:806`、`internal/cli/loader_preloaded_session.go:65` | `POST /api/sessions`/queen session → `createSessionWithRuntime` → `AttachSessionPhaseState`：显式 `initial_phase` > `meta.json["phase"]` > 物理 binding > independent；`load_colony`/fork 用 `ApplyQueenPhase` 切到 colony；`SetPhase` 合并写 `meta.json` 并发 `queen_phase_changed`。`GET /api/sessions/{id}` 返回 `queen_phase`，`GET /api/sessions/{id}/tools` 返回 `phase` | 已接 | C | 两阶段已真实落盘并可重启恢复。`GET /api/queen/{id}/tools` 的响应体**没有** phase 字段（发布说明第 13 行的说法不准确）；该路由的 PATCH 通过 `refreshQueenToolSessions` 只刷新 `PhaseState != nil` 的 live 会话。phase 变更历史未单独审计。 |
| Queen 的持久会话与历史恢复 | `internal/server/chat_runtime.go:255`、`internal/agentloop/conversation.go:1126`、`:1213`、`internal/cli/loader_session_resume.go:38`、`internal/cli/loader_preloaded_session.go:53/62` | `NewChatRuntime` 从会话目录恢复 parts；`queen_resume_from` 复用旧 id 与旧目录；预载 `serve --colony` 会话按 metadata 的 `queen_session_id` 走同一复用路径 | 已接 | C | `POST /api/sessions` 带 colony_id 的普通创建仍不走 `queen_resume_from`（`loader_session_resume.go:73`）。 |
| Queen 的 per-queen 记忆：reflection 写入 + recall 召回 | `internal/server/chat_runtime.go:221/607/784`；`internal/memory/memory.go`、`recall.go:174` | 同 improvement 3/4 | 已接 | C | — |
| Queen 的过往对话召回：每轮 6 条 dated excerpts + `search_messages` | 工具侧 `internal/cli/session_tools_search.go:43/168`、`internal/cli/session_tools.go:103`；注入侧 `internal/memory/past_conversations.go:222/477/511`、`internal/server/chat_memory_wiring.go:31`、调用点 `internal/server/chat_runtime.go:607` | 同 improvement 过往对话召回：每条 client 消息后自动注入最多 6 条带日期摘录（`PastRecallTopK`），`search_messages` 常驻（scope 来自会话 queen 目录） | 已接 | C | 只在 client 输入轮；worker 报告/trigger 不触发；excerpts 与工具检索互不替代。 |
| `memory_timeline` flag 打开后生成/召回事件时间线 | 读侧 `internal/cli/memory_timeline.go:42`（`memoryTimelineToolDefs`）、`:104`（`executeMemoryTimelineTool`）、`internal/cli/session_tools.go:112`；写侧 `internal/server/chat_memory_wiring.go:59`（`startTimeline`）/`:89`（`timelineAfterTurn`）、`internal/memory/timeline.go:426`（`ExtractTimeline`）、`:491`（`UpdateSessionTimeline`）、`:528`（`BackfillTimelines`）；调用点 `internal/server/chat_runtime.go:303`（构造）/`:645`（每轮后） | flag（env `HIVE_MEMORY_TIMELINE` 或 `hive.config.json`）打开后：`NewChatRuntime` 启动回填（近 10 个会话、排除当前、并发 2），每轮完成 `TimelinePending` 有新增就后台 `UpdateSessionTimeline` 写 `<sessionDir>/timeline.jsonl` + `timeline_meta.json`（单条受 `HIVE_TIMELINE_COOLDOWN`≈300s 冷却，≥3 条立即跑）；`memory_timeline`/`search_timeline` 照旧读 `memory.SearchTimeline` | 已接 | C | `SubscribeTimelineTriggers`（`timeline.go:645`）本身仍零调用——写入由等价的 `startTimeline` + `timelineAfterTurn` 承担（release notes 同口径）；只在 chat Queen、session 有 LLM 且 flag 打开时生效；worker/overseer 不写。 |
| Queen 拥有任务计划（计划可见、可编辑、跨 reload） | `internal/tasks/session_tools.go:747`、`internal/server/routes_tasks.go:39` | 同 coordination 任务计划 | 已接 | C | — |
| Queen 拥有 tracker（建 schema、派活、SQL 校验） | `internal/tracker/tools.go:1030`、`internal/cli/session_tools.go:118-137`、`internal/colony/tracker.go:180` | 会话工具表常驻 4 个 tracker 工具；绑定 colony 后按 runtime binding 执行；无绑定返回 `ErrNoBinding` | 已接 | L | 未绑 colony 时工具仍可见但不可用。 |
| 提问停车（`ask_user`） | `internal/agentloop/synthetic_tools.go:94`、`internal/server/chat_runtime.go:1078/1127/1141` | chat 会话工具表始终追加 `ask_user`；executor 记录 `pendingQuestions` 并发 `client_input_requested(park_reason=ask_user)`；本轮结束快照为 parked | 已接 | C | 停车状态经 `pending_input` 落盘（`chat_park.go:29`），重启后恢复；`credential_form` 有生产者/事件/落盘/提交路由（`chat_credential_park.go:43/68`、`routes_credentials_form.go:36`）。overseer 没有 `ask_user` 工具，其提问/park 不写入 chat 的 `pendingQuestions`。 |
| Sentinel：停车后 nudge / 升级 inbox/Telegram/Slack 并恢复 | `internal/server/sentinel_runtime.go:41/107`、`internal/sentinel/manager.go:174/230` | `serve` 启动 manager；每个 colony-bound chat runtime 注册 escalation source；parked 时分类并 nudge/升级；live 会话回复经 `InjectEvent` 回到 Queen | 已接 | C | 需要 colony 通知配置打开 Sentinel；dead session 不能冷恢复；闭环未 live 实测。 |
| Queen 与 colony overseer 是同一个 lead | `internal/cli/loader.go:484`（`queenOverseerSpec`）、`internal/colony/runtime.go:1137`（`runOverseer`）、`internal/colony/overseer_tools.go:112`、`internal/server/chat_colony_bridge.go:187`（`relayClientInputToOverseer`）、`:293`（报告）、`:302`（trigger）、`:139`（`mirrorColonyEvent`）、`internal/colony/session_bridge.go:66`（`InjectOverseer`）、`internal/colony/overseer_emitter.go:63` | load/fork/`serve --colony` → `StartOverseer` 起持久 worker；绑定 colony 且有 live overseer 时，`ChatRuntime.Deliver` 把客户端消息转投 overseer（`chat_runtime.go:359-372`），worker 报告与 trigger 同样只进 overseer；chat 只把用户 turn 记入 transcript（`recordOnly`），不跑 model turn；overseer 的文本/执行事件由 `overseerEmitter` 发出，`mirrorColonyEvent` 重盖到 `chatStreamID`/`chatNodeID`（`relayed_from=overseer`），UI 以 queen stream 呈现；首条中继由 `relaySkillsCatalogBlock` 附带 skills 目录 | 已接 | C | 操作面已是单执行 lead，但仍是两份 conversation 存储：overseer 的回复不写回 chat transcript，chat 的 `pendingQuestions`/park 不由 overseer 更新；overseer 没有 `ask_user`。非 overseer worker 的 client IO 与 tool/token 事件不镜像；中继失败会回退 chat Queen。 |

**小结**：Queen 的 persona、绑定会话、持久/恢复、记忆、任务计划、`ask_user`、tracker、Sentinel 都接通；`run_worker` 与 `run_playbook` 让她能派活和跑批次；soul 叠加进她的 identity prompt；过往对话从"模型主动搜"变成"每轮自动 6 条摘录 + 工具可搜"，memory timeline 也在 flag 打开后有了写入者（启动回填 + 每轮更新）；预载 colony 会话现在也按 `queen_resume_from` 复用旧 id；two-phase `PhaseState` 已落 `meta.json` 并可重启恢复；credential_form 有生产者、落盘与提交路由。1.0.24 还把客户端消息、报告与 trigger 的单一执行路径交给 live overseer，chat Queen 作为前端；残留是两份 conversation 与不共享的 pending/park，而不是接线断点。

## 6. the_loop — 唯一的执行原语

概念主张：一个多轮流式 LLM 会话循环；每轮流式输出、并行跑只读工具、回注结果、决定停止/继续；judge 自纠错；Queen/worker 只是一组 `LoopConfig` 差异；内建崩溃恢复（cursor、park/resume）、上下文压缩、计量与提醒。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 单一 agent loop 作为 Queen 的执行体 | `internal/agentloop/loop.go:478`（`ExecuteTurn`）、`internal/server/chat_runtime.go:271`（`NewAgentLoop`）、`:479`（`runTurn`） | `hive serve` → `createChatSession` → `NewChatRuntime` → 每轮 `runTurn` 调用同一 loop | 已接 | C | — |
| 流式输出（文本边生成边推） | `internal/llm/stream.go:33`、`internal/llm/stream_openai.go:42`、`internal/server/chat_stream.go:113`、`internal/server/chat_runtime.go:496`、`:699/709` | CLI provider 实现 `StreamCompleter` → `newChatStreamingCompleter` 包装 → loop 的 `AComplete` 内部走 SSE → 每帧 `client_output_delta` | 已接 | L | MiniMax 仍返回 `ErrStreamingUnsupported` 并回退非流式；Anthropic 已走 Messages SSE（`internal/llm/stream_openai.go:67` → `stream_anthropic.go:48`）；tool-call 参数对 Anthropic 也按 `input_json_delta` 聚合，不逐片下发。 |
| 只读工具并行执行、其余按序 | `internal/agentloop/loop.go:61`（`ParallelReadOnlyTools`）、`:88`（默认 `true`）、`:878`（`executeToolBatch` 只在整批都是 `ConcurrencySafe` 时并发）、`:895-915`（`allToolsConcurrencySafe`） | 同一 loop；`NewAgentLoop` 的 `DefaultLoopConfig` 开并行，chat 与 worker 都走 `NewAgentLoop`；批内全部显式安全才并发，任一未标记则整批串行 | 已接 | C | 并发执行仍按原顺序发 `ToolCallStarted`/`Completed`；未声明 `ConcurrencySafe` 的工具（含 MCP）继续串行。 |
| 工具结果回注，循环直到不再调用工具 | `internal/agentloop/loop.go:614`、`:756`（`AddToolResult`） | `ChatRuntime.runTurn` 使用该路径 | 已接 | C | — |
| 自纠错第一层：错误结果进入下一轮上下文 | `internal/agentloop/loop.go:756`（`ToolResultOptions{IsError: ...}`） | 同上 | 已接 | C | — |
| judge：accept / retry / escalate，retry 反馈注入 | `internal/agentloop/judge.go:92/109/127/198`、`internal/agentloop/loop.go:618/772`、`internal/server/chat_runtime.go:321/1061/1077`、`internal/colony/runtime.go:1160-1167` | `ConfigureJudge` 在 colony worker 构造时注入（`colony/runtime.go:1078`）；overseer 无 spec criteria 时从 colony goal 回填（`:1160-1167`）；chat runtime 有 `ChatRuntimeDeps.SuccessCriteria` 或绑定 colony 的 goal 时 `ConfigureJudge`（`chat_runtime.go:321/1077-1089`）；criteria 非空才评估 | 已接 | C | 无 criteria 的 plain chat 仍 `Judge == nil`，永不评估；绑定 colony 且有 live overseer 时 chat Queen 不执行 model turn，评估发生在 overseer 的 loop。 |
| stall / doom-loop 检测 | `internal/agentloop/doom_loop.go:28-34`（默认 3 次警告 / 5 次停车）、`:108`（`Observe`）、`:178`（`ShouldPark`）、`internal/agentloop/loop_guards.go:109`（`parkOnDoomLoop`）、`internal/agentloop/loop.go:837/976`；stall 侧 `internal/server/session.go:417/431`（`AddSession` → `startLoopGuards`）、`internal/server/chat_loop_guards.go:39/81/97`、`internal/server/stall_check.go:79/124` | `AddSession` → `startLoopGuards` 幂等订阅 `SubscribeStallCheck`（session bus，judge 判定 turn_done 后发 `node_stalled` 并 `InjectEvent` 提醒）；doom-loop 连续 3 次同名同参先 advisory，达到 5 次 `ShouldPark` → `parkOnDoomLoop` 结束 turn、置 `TurnResult.ParkReason=doom_loop`、经 `ParkSink` 发 `node_tool_doom_loop`；chat 失败分支用 `turnFailureParkReason` 保留该 reason（`chat_runtime.go:673/682/716`） | 已接 | C | stall 受 `stall_check` flag 与 judge slot 门控；doom-loop 的 `TurnResult.ParkReason` 现在写回 chat park state，事件、`loop_state_changed` 与 `pending_input` 三处一致（`TestChatDoomLoopParkReasonReachesEventsAndPendingInput`）。worker/overseer 未设 `ParkSink`。 |
| Queen 与 worker 的 `LoopConfig` 差异（worker 3+1、30/90/200；Queen soft checkpoint 30/50） | `internal/agentloop/loop.go:78-91`（`DefaultLoopConfig` 50 轮、soft 30 / hard 50）、`internal/agentloop/loop_guards.go:18/36`、`internal/colony/worker_definition.go:9`、`worker_loop_config.go:50-65` | chat Queen 用 `DefaultLoopConfig`（`chatLoopConfig` 只覆盖 spillover）；worker 由 `BuildWorkerLoopConfig` 应用 3+1/30/90/200，并显式 `ToolCallHardLimit=0` 保持 30×3=90；soft checkpoint 在 30 次工具调用触发，50 次硬停 | 已接 | C | `max_cost_per_run` 仍只是 schema 字段；会话花费上限走 `HIVE_SESSION_COST_LIMIT_USD`（`session_cost.go:38/53/86`、loop 调用点）；soft 计数是单 turn-loop 内计数。 |
| Grace iteration：worker 预算耗尽仍有最后一次 `report_to_parent` | `internal/agentloop/loop.go:96`（`graceTerminalTools`）、`:529-556`、`internal/colony/worker_definition.go:9-16` + `worker_loop_config.go:46-52` | worker `GraceIterations=1`；grace 轮只允许 `report_to_parent`/`tracker_upsert`/`task_update` | 已接 | C | `task_update` 现在可达（worker 自带 4 个 task 工具，`worker_task_tools.go:77`），三项终态工具均可用。 |
| 迭代上限与干净停止 | `internal/agentloop/loop.go:797`、`internal/server/chat_runtime.go:479` | `MaxIterations=50` 时 `runTurn` 走 `executionFailed` | 已接 | C | — |
| 崩溃恢复：cursor 持久化 + 会话恢复 | `internal/agentloop/conversation.go:1126`（`persist`）、`:1213`（`RestoreConversation`）、`internal/server/chat_runtime.go:255`；worker 侧 `colony/worker_transcript.go:40`、`resume_workers.go:38`、`worker_state.go:78/226`、`colony/runtime.go:523`；预载 `loader_preloaded_session.go:53/62` | chat 每条消息落盘并更新 cursor；`NewChatRuntime` 恢复 parts；worker 运行态写 `runs/<id>.json` + `workers/<id>/state.json`，`ColonyRuntime.Start` 自动续跑崩溃时非终态 worker；预载会话复用旧 id 与转录 | 已接 | C | 自动续跑只覆盖崩溃时 queued/running（completed/failed/stopped 不复活）；`POST /api/sessions` 带 colony_id 仍不走 `queen_resume_from`；自定义 report status 未归一为终态时会被当作可续跑（待验证）。 |
| park / resume：等待人类回答或凭据后从磁盘继续 | `internal/server/chat_park.go:29`（落盘）、`:89`（清除）、`:116`（恢复）；`internal/server/chat_runtime.go:228/1137/1286`、`:824`（`injectHubReminders`）；`internal/server/chat_credential_park.go:43/68/77/90`；`internal/server/routes_credentials_form.go:36`（提交）；`internal/agentloop/park_reason.go` | `ask_user` / 缺凭据停车 → `cursor.json` 的 `pending_input` 落盘；`NewChatRuntime` 恢复等待；下一条用户消息或 trigger 清除标记后继续。`POST /api/sessions/{id}/credential-form`：校验 pending form 与 `credential_id` → `credentials.NewCredentialObject` + `SaveCredential` 写加密 store → 同步 env → `ClearCredentialFormPark` → `InjectEvent(confirmation, true)` 恢复；`status=cancelled` 只清 park 并注入取消说明；响应与恢复消息都不含密钥 | 已接 | C | 人类回答与 credential_form 都能落盘/恢复；提交路由只服务 chat runtime 的 pending form（缺凭据工具错误的生产者），overseer 执行的会话不会产生该 form，overseer 的 park 状态也仍只在内存。 |
| 上下文压缩：自动检测并压缩；超大工具结果落盘可搜 | `internal/agentloop/loop.go:320`（`compactConversationIfNeeded`）、`:224`（`restoreSpillCounter`）、`internal/agentloop/conversation.go:736`（`NeedsCompaction`）、`:931`（`Compact`）、`internal/agentloop/llm_compact.go:22`；spillover `internal/agentloop/tool_result_spillover.go:73`（`truncateToolResult`）、`:79`（`MkdirAll`）、`:117`（`applyToolResultSpillover`）、配置 `loop.go:60-74`（`MaxToolResultChars`/`SpilloverDir`）；手动路由 `routes_execution.go:444` → `:638`；worker 配置 `internal/colony/worker_loop_config.go:65/122` | 每轮 LLM 调用前检查 `NeedsCompaction()` → `LlmCompact` → `conv.Compact`，并触发 PRE/POST_COMPACT；每次工具返回经 `applyToolResultSpillover`：超过 `MaxToolResultChars`（默认 30k）的非错误结果写盘，消息改为带 `Full result at:` 的 5000 字符预览并置 `SpilloverPath`；chat 注入 `<session>/data`，worker 由 `BuildWorkerLoopConfig` 注入 `cache/worker-tool-results/<pid-seq>` | 已接 | C | worker spill 目录由 `hivehome.Resolve()`（进程 `HIVE_HOME`）决定，不是 loader 传入的 `HomeRoot`；serve 环境的实际路径需实测（待验证）；目录在首次 spill 时 `MkdirAll`，恢复时扫描已有文件避免覆盖。 |
| 每次 LLM 调用计量（tokens/cost）与工具预算上限 | `internal/agentloop/loop.go:595`（`LLMTurnComplete`）、`internal/server/chat_runtime.go:713`（emit usage）、`loop.go:625-760`（soft/hard/lifetime 预算） | 每次调用发 `llm_turn_complete`（input/output/cached/cost）；工具计数与 defer 在 loop 内执行 | 已接 | C | — |
| 框架注入提醒保持连贯 | `internal/agentloop/reminder_hub.go`、`internal/server/chat_reminders.go:69/85-96`、`internal/server/chat_runtime.go:577/584/643` | 同 coordination 提醒 | 已接 | C | `STREAM_STALLED` 未注册；REST reminder surface 已镜像 live hub（`routes_reminders.go:374`），与 chat hub 同源。 |

**小结**：the_loop 骨架完整：流式（含 Anthropic Messages SSE）、并行只读工具、judge、grace、压缩、计量、提醒都真实运行；worker transcript 与运行态落盘补上了崩溃恢复，stall 在 `AddSession` 订阅、doom-loop 达到 5 次即停车，Queen 30/50 软硬预算生效，worker spillover 也配上了独立目录。1.0.24 补上 credential-form 提交路由（加密 store + 清 park + 恢复）并让 chat 失败分支保留 doom-loop park reason；loop 侧不再有"部分"行，剩余是执行作用域与已知边界（overseer park 不共享、MiniMax 非流式、Python playbook 不可执行）。

## 7. worker_agent — 短命的并行克隆

概念主张：worker 是 Queen loop 的克隆，带 worker prompt、Queen 工具的子集、更紧的预算、一个任务注入；专注/短命/fail-fast，不能升级、不能再委派；终态 `report_to_parent(status, summary, data)` → Queen 收到 `[WORKER_REPORT]`；有 grace iteration；通过 tracker / 自己的 task list / colony skills 共享；会话可崩溃恢复，`run_worker(resume_worker_ids=…)` 可续跑；大量工作以 headless 方式运行。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| worker 是 Queen loop 的克隆（同一 `AgentLoop`） | `internal/colony/runtime.go:982`（`runWorker`）、`:563`（`spawnBatchOn` 上游入口）、`internal/colony/run.go:220`、`internal/host/host.go:165` | `run_worker` → `SpawnBatchWithOptions` → `spawnBatchOn` → `runGatedWorker` → `runWorker` 新建 `agentloop.AgentLoop` | 已接 | C | `SpawnBatchWithOptions` 生产可达；`SpawnBatch`（无 options）、`SpawnRun`、`Host.Spawn`/`Run` 仍无生产调用（没有路由/工具使用它们）。 |
| worker prompt（聚焦、无升级、无委派、fail-fast 规则）真正注入 | `internal/colony/worker_definition.go:39`（`WorkerSystemPrompt`）、`:113`（`BuildSystemPrompt`）、`internal/cli/colony_worker_definition.go:58` | fork 写 `worker.json.system_prompt = BuildSystemPrompt(goal)`；`runWorker` 用 `systemPromptFor(spec)`，spec 来自 `LoadAgentSpec` 读取的同一 `system_prompt` | 已接 | C | 旧 colony（193ace3d 之前 fork 的）仍带 Queen persona，读取时原样保留；没有迁移。 |
| 工具子集 + 更紧预算（3+1 轮、30/轮、硬停 90、200 终身） | `internal/colony/runtime.go:825`（`workerTools`）、`:856`（`baseWorkerTools`）、`internal/colony/worker_tool_selection.go:28`（`selectWorkerDeclaredTools`）、`internal/colony/worker_definition.go:9`、`worker_loop_config.go:46` | worker 工具 = builtin base + `worker.json.tools` 从 integration/MCP 池选出的项 + worker tracker + `report_to_parent` + `skill_view`/`list_skills`；预算由 `BuildWorkerLoopConfig` 应用 | 已接 | C | builtin base 始终保留（不是严格"Queen 子集"）；`worker.json.tools` 为空时只能拿到 builtin；声明了未注册的名字会被静默丢弃。 |
| `report_to_parent(status, summary, data)` 终态工具 | `internal/agentloop/synthetic_tools.go:279`、`internal/colony/runtime.go:842`（加入 worker 表）、`:876`（执行器记录 report） | worker 工具表注入；executor 调 `worker.RecordExplicitReport`；`StopAfterToolBatch` 让本轮终止 | 已接 | C | `HandleReportToParent` 本身仍零调用（执行器走 `RecordExplicitReport` 等价实现）。 |
| worker 报告 → Queen 的 `[WORKER_REPORT]` 回合 | `internal/colony/runtime.go:1118`（`publishReport`）、`internal/colony/queen_tools.go:492`（订阅）、`:426`（格式化）、`internal/server/chat_runtime.go:315`、`:1122`（转投 overseer） | 终态 `SUBAGENT_REPORT` → `FormatWorkerReport` → 有 live overseer 时只投 overseer；overseer 自己的终态报告回 chat 前端成为 `[WORKER_REPORT]` 回合；无 overseer 时仍直接 `InjectWorkerReport` | 已接 | L | 报告已有 `duration_seconds`/`output_file`；预载 colony 无 chat 时无消费者；非 overseer worker 的 client IO/tool/token 事件不镜像到会话流。 |
| Grace iteration：预算耗尽后仅 `report_to_parent`/`task_update`/`tracker_upsert` 可执行 | `internal/agentloop/loop.go:96`、`:529-556`、`internal/colony/worker_definition.go:9-16` + `worker_loop_config.go:46-52` | worker `GraceIterations=1`；grace 轮过滤非终态工具 | 已接 | C | `task_update` 可达（worker 自带 task 工具），三项终态工具均可用。 |
| 结果共享：tracker | `internal/tracker/tools.go:1030`、`internal/colony/tracker.go:109-130`、`runtime.go:849` | worker 工具表带 `tracker_upsert/tracker_query`；executor 按 `input_data.binding` 执行 | 已接 | L | 只覆盖 worker 角色表；`tracker_sql`/`register_writable` 仍是 Queen 专用（符合概念）。 |
| 结果共享：自己的 task list | `internal/colony/worker_task_tools.go:42`（`newWorkerTaskToolSet`）、`:77`（`workerTaskToolDefs`）、`internal/colony/runtime.go:853`（定义入 worker 表）、`:889`（按 worker 目录建 set）、`:916`（执行） | `runWorker` → `workerTools` 追加 4 个 task 定义 → executor 用 worker 自己的目录 `<workerDir>/_misc/<id>/tasks.json` 执行；两个 worker 不共享列表 | 已接 | C | 列表只属于 worker 自己，Queen 不轮询；worker 结束时任务状态不汇总到 Queen 的计划。 |
| 结果共享：colony skills | `internal/skills/agent_tools.go:54`、`internal/colony/runtime.go:404`、`internal/colony/skills_surface.go:40` | spawn 时 `SkillDirs` + `SkillsCatalogPrompt`；worker 工具表带 `skill_view`/`list_skills` | 已接 | C | 无 `write_skill`（符合"worker 只读"）；运行中新增 skill 要等下一批。 |
| 会话崩溃恢复 + `run_worker(resume_worker_ids=…)` 续跑 | `internal/colony/resume_workers.go:38`（`ResumeWorkers`）、`internal/colony/worker_state.go:78/226`、`internal/colony/worker_transcript.go:40`、`internal/colony/runtime.go:523`、`internal/colony/queen_tools.go:231-280` | worker transcript 落盘到 `<colony>/workers/<id>/conversations`；运行态写 `runs/<id>.json` + `state.json`；`ColonyRuntime.Start` 自动续跑崩溃时非终态 worker；`run_worker(resume_worker_ids=…)` → `ResumeWorkers` 恢复同一 conversation（可带 guidance）→ 继续 loop；逐 id 返回失败原因 | 已接 | C | 自动续跑只覆盖崩溃时 queued/running（completed/failed/stopped 不复活）；活动中的 worker 拒绝手动 resume；没有 meta.json 的旧 worker 不能 resume；resume 后的预算按 pinned 处理，不做自适应收紧。 |
| headless 运行 + Sentinel 升级人工 | `internal/cli/loader.go:418/469`、`internal/cli/loader_preloaded_session.go:33`、`internal/server/sentinel_runtime.go:41/107` | `hive serve --colony` 在无 UI 情况下起 runtime + overseer + chat；worker 在服务端跑；Sentinel 在停车时升级 inbox/Telegram/Slack | 已接 | C | 入口是常驻 `serve`，没有一次性 headless run 命令；Sentinel 受 colony 通知配置门控。 |

**小结**：worker_agent 这轮补上了身份、工具面、skills、task list、resume 与运行态持久化：fork 写 worker 人格，`worker.json.tools` 真的展开成工具，worker 有自己的 4 个 task 工具，transcript/state 落盘且能手动或随 colony 启动续跑，报告字段齐全；worker loop 也配了独立 spillover 目录。仍未完成的是 worker 与 Queen 的计划/提醒面共享（worker 的 task list 自用，不汇总）。

---

## 8. 重点核查清单（按当前 HEAD 逐项）

| 核查项 | 结论 | 依据 / 缺口 |
| --- | --- | --- |
| `run_worker` 工具 | **已接（live）** | `queen_tools.go:75/204` + `cli/session_tools.go:111/258` + `chat_runtime.go:879/896`；绑定 colony 后真实 spawn 并回传；预载 `serve --colony` 会话同样可用。 |
| `resume_worker_ids` | **已接（新）** | `colony/resume_workers.go:38` + `queen_tools.go:231-280` + `worker_transcript.go:40`；逐 id 从磁盘 transcript 续跑。 |
| worker `SpawnBatch` / `SpawnRun` | **部分（不计入 80 行）** | `SpawnBatchWithOptions` 经 `run_worker` 生产可达；`SpawnBatch`、`SpawnRun`、`Host.Spawn`/`Host.Run` 仍零生产调用。 |
| `[WORKER_REPORT]` 注入 | **已接（live）** | `queen_tools.go:492/426` + `chat_runtime.go:315/942`；报告带 duration/output_file；有 live overseer 时报告只转投 overseer（`chat_runtime.go:1122`），overseer 自己的报告再回 chat 前端。 |
| colony 事件 → session SSE | **已接（新）** | `server/chat_colony_bridge.go:44/72` 把 colony 生命周期事件镜像到 session bus；`BindColony`/`AddSession` 时挂桥（`chat_runtime.go:950`）。没有独立的 `/api/colonies/{id}/events`，只在有 chat 会话绑定时可见；非 overseer worker 的 client IO/tool/token 事件不镜像。 |
| worker reminder hub | **已接（新）** | `colony/worker_reminders.go:114` 用 `NewAgentLoop`；`:120` task source、`:127` idle nudge、`:135` `Hub.Start`；worker 侧提醒真正注入 loop。 |
| overseer 输入 | **已接（新）** | `chat_colony_bridge.go:187`（客户端消息）+ `:293`（报告）+ `:302`（trigger）→ `session_bridge.go:66`；绑定 colony 且有 live overseer 时 chat Queen 只做前端，overseer 文本/执行事件经 `overseer_emitter.go` 与 `mirrorColonyEvent` 以 queen stream 呈现。两份 conversation 与 pending/park 仍不共享。 |
| REST reminders | **已接（新）** | `routes_reminders.go:374` 读 live hub（`chat_runtime.go:310` 注册），GET 只读镜像、DELETE 直接 dismiss；不再是恒空的旧 `ReminderRuntime`。 |
| `tracker_*` 工具（queen / worker / overseer） | **已接（live）** | queen 4 个（`session_tools.go:118-137`）、worker 2 个（`colony/tracker.go:109`）、overseer queen 表（`overseer_tools.go:123`）；均按 binding 执行。 |
| tracker binding | **已接（live）** | `tracker/binding.go:67` + `colony/tracker.go:137/180` + `session_tools.go:267`；无 binding 返回 `ErrNoBinding`。 |
| 提醒 sources 与 `Hub.Start` | **已接（新）** | `chat_reminders.go:69/85-96` 注册 7 个 source（新增 `tracker_snapshot`）+ Sentinel source；SESSION_START/USER_PROMPT_SUBMIT/POST_TOOL_USE/PRE+POST_COMPACT/STOP/IDLE_TICK/TOOL_BUDGET_CHECKPOINT 均触发。缺口：STREAM_STALLED 未注册、预载会话 catalog 走 default queen scope、无 MCP 时 chat searchable tools 无内容；overseer 执行时 chat hub 的 skills source 不参与该次输入。 |
| judge / LLM adapter 与 `judge_verdict` | **已接（colony worker + overseer + 有 criteria 的 chat Queen）** | `judge.go:92/109/127` + `colony/runtime.go:1078/1160-1167` + `chat_runtime.go:321/1077-1089` + `eventbus/emit.go:363`；plain chat（无 criteria）仍 judge-free。 |
| `StreamCompleter` 与 `client_output_delta` | **已接（live）** | `llm/stream.go:33` + `stream_openai.go:42/67` + `chat_stream.go:113` + `chat_runtime.go:699/709`；Anthropic 走 Messages SSE，MiniMax 回退。 |
| `ask_user` / task 工具 | **已接** | `synthetic_tools.go:94` + `chat_runtime.go:1078/1127`；`task_*` 在 `cli/session_tools.go:75/83`；worker 也有自己的 task 工具（`worker_task_tools.go:77`）；overseer 同样有（`overseer_tools.go:112`）。 |
| colony scaffold 持久目录 | **已接（新）** | `POST .../scaffold` → `ensureColonyScaffold`（`colony_scaffold.go:31`，`routes_colonies.go:474`）建 `skills/`、`runs/`、空 `triggers.json`；幂等。fork 仍只写 `metadata.json`/`worker.json`。 |
| worker 运行态 / 自动续跑 | **已接（新）** | `worker_state.go:78/226` + `colony/runtime.go:523`；`runs/<id>.json` + `workers/<id>/state.json`，`Start` 自动续跑 queued/running；终态不复活，不可恢复记录标 `unrecoverable`。 |
| `credential_form` park | **已接（新）** | `chat_credential_park.go:43/68` 由缺凭据工具结果生产 form、发 `client_credential_form_requested`、写 `pending_input`；`routes_credentials_form.go:36` 校验 pending form → 写加密 store（`:121`）→ `syncEnv` → 清 park（`:139`）→ 注入确认恢复（`:144`）；`cancelled` 分支只清 park。仍只在 chat runtime 的工具错误路径生产。 |
| colony load / unload / `CreateColonyFromSource` | **已接** | `loader.go:727/746/806` + 路由 + chat 绑定；失败按 4xx/5xx 上报；`load_colony` 同时 `ApplyQueenPhase(session, colony)`。 |
| `serve --colony` 预载会话 | **已接（新）** | `loader_preloaded_session.go:33/91/199`：`Chat` + `QueenExecutor` + trigger 恢复 + `SkillsCatalog`；按 metadata `queen_session_id` 走 `queen_resume_from` 复用旧 id/转录；无 provider 时仍 503；catalog 固定 `default` queen scope。 |
| Queen persona / memory / resume | **已接** | `chat_runtime.go:1085` persona（identity + soul）、`:221/607` reflection、`:784` recall、`loader_session_resume.go:38` resume；预载 colony 会话可 resume，`POST /api/sessions` 带 colony_id 的普通创建仍不行。 |
| Sentinel | **已接（新）** | `sentinel_runtime.go:41/107` + `loader.go:300-304` + `manager.Start/RefreshListeners`；受 colony 通知配置门控。 |
| `skills.WriteSkill` / agent skill 工具 | **已接（新）** | `skills/agent_tools.go:54/178` + `cli/session_tools.go:94/250` + `colony/runtime.go:404`；write/view/list 全通。 |
| triggers / `QueenExecutor` | **已接（新）** | `loader.go:606` 与 `loader_preloaded_session.go:122` 赋值；`chat_runtime.go:351` 实现 `InjectTrigger`；`trigger_runtime.go:220/293/416` 恢复与 fire。 |
| playbook runner / `run_playbook` | **已接（新，Go native）** | `colony/playbook_tools.go:76/129/168` + `internal/playbook`；`action='status'/'stop'` 见 `playbook_control.go:120/157`；Python `.play.py` 明确 unsupported，0 派发。 |
| MCP 启动装载 | **已接（新）** | `cli/mcp_loader.go:66/102/131` + `tools/mcp_bootstrap.go:66` + `registry.go:150`；每个新 catalogue 自动 attach。 |
| MCP 工具白名单 | **已接（新）** | `host/host.go:89-112` 把进程发现的名字集并入 runtime；`applyToolAllowlist`（`colony/runtime.go:321`）按 `tools.json` 过滤；chat 侧 `filterToolsByColonyAllowlist` 同源。未配置白名单时不过滤（预期）。 |
| Soul 沉淀 | **已接（新）** | `chat_runtime.go:229`（`OnReflect`）+ `:1259`（`sedimentSoul`）+ `soul/sediment.go`；soul 已进 chat Queen 的 identity prompt（`:1085-1095`），overseer spec 不读 soul。 |
| `search_messages` / `search_timeline` | **已接（新）** | 工具已入会话表并可执行（`cli/session_tools_search.go:43/168`、`session_tools.go:103`）；"每轮 6 条 dated excerpts" 已由 `server/chat_memory_wiring.go:31` + `chat_runtime.go:607` 自动注入。 |
| `memory_timeline` 浏览工具 | **已接（新）** | 读侧 `cli/memory_timeline.go:42/104`；写侧 `server/chat_memory_wiring.go:59/89` + `chat_runtime.go:303/645` 承担启动回填与每轮更新；`SubscribeTimelineTriggers` 本身仍零调用（等价实现）。 |
| goal 加权成功标准执行（`EvaluateGoal`） | **已接（新）** | `agentloop/goal_eval.go:144` + `colony/goal_outcome.go:74/90` + `runtime.go:1104`；结果进 `WorkerResult.goal_evaluation`、`result.json` 的 `data`、worker 报告与 `goal_progress`；`custom`/未知 metric 标 unsupported。 |
| Goal 约束 / Context 进 worker prompt | **已接（新）** | `colony/runtime.go:827-828` + `goal_outcome.go:33`（系统提示）+ `agentloop/loop.go:513`（首条消息）；overseer 显式置空，chat Queen 无 Goal。 |
| 未达标 → 改进信号 | **已接（新）** | `colony/improvement.go:124/197/238/260` + `runtime.go:1081/1106`；写 `signals.jsonl`、发 `improvement_signal` 事件、并入报告；尚无读取者。 |
| 每轮 6 条过往对话 excerpts | **已接（新）** | `memory/past_conversations.go:13/222/477/511` + `server/chat_memory_wiring.go:31` + `chat_runtime.go:607`。 |
| 工具结果 spillover（超大结果落盘） | **已接（新）** | `agentloop/tool_result_spillover.go:73/79/117` + `loop.go:224`；chat 注入 `<session>/data`（`chat_memory_wiring.go:17-21`），worker 由 `BuildWorkerLoopConfig` 注入 `cache/worker-tool-results/<pid-seq>`（`worker_loop_config.go:65/122`）。worker 路径依赖进程 `HIVE_HOME`，需实测（待验证）。 |
| worker `search_tools` / 工具分层 | **已接（新）** | `agentloop/search_tools.go:49/66` + `loop.go:577/633/1011`；worker 的 `ToolTierState`（`colony/runtime.go:413-476`）非空即暴露 `search_tools`，searchable 工具在 dispatch 前被拒。 |
| `stall_check` / doom-loop | **已接（新）** | stall：`session.go:431` → `chat_loop_guards.go:39/81` → `stall_check.go:79/124`（judge turn_done → `node_stalled` + 提醒）；doom-loop：`doom_loop.go:28-34/178` + `loop.go:837/976` + `loop_guards.go:109`，5 次同名同参停车并发 `node_tool_doom_loop`；chat 失败分支用 `turnFailureParkReason` 保留 `doom_loop`（`chat_runtime.go:673/682/716`）。 |
| 并行只读工具 | **已接（新）** | `loop.go:61/88/878`：`ParallelReadOnlyTools` 默认开，整批 `ConcurrencySafe` 才并发。 |
| 预算执行（Queen 软检查点 / 会话花费上限） | **已接（新）** | `loop.go:78-91` 给 chat Queen 默认 soft 30 / hard 50，`softBudgetCheckpointBlock`（`loop_guards.go:36`）在 `loop.go:811` 触发；worker 显式清 `ToolCallHardLimit` 保持 30×3=90（`worker_loop_config.go:60`）。会话花费硬上限仍由 `HIVE_SESSION_COST_LIMIT_USD` 执行（`session_cost.go:38/53/86`）。`max_cost_per_run` 仍无执行者。 |
| rate limit（LLM + 工具） | **已接（新）** | `llm/transport.go:45` 给 HTTP provider 装 `AcquireLLMRequest`；`agentloop/tool_rate_limit.go:23/33` + `loop.go:994` 按 `rate_limits.tools.*` 拒绝并回注可重试结果；未配置时 no-op。 |
| `credential_status` | **已接（只读，新）** | `cli/session_tools_credentials.go:26/37` + `session_tools.go:120`；只列可用性，不返回值；无写凭据工具。 |
| park 落盘 / resume | **已接** | `chat_park.go:29/89/116` + `chat_runtime.go:228/1137/1286`；`ask_user` 与 `credential_form` 都可跨重启恢复；credential 提交路由 `routes_credentials_form.go:36` 写加密 store、清 park 并恢复；仍只在 chat runtime 的工具错误路径生产 form。 |
| Anthropic 流式 | **已接（新）** | `llm/stream_openai.go:67` 分流到 `stream_anthropic.go:48`（Messages SSE，含 `input_json_delta` 聚合）。 |

## 9. 总体结论

- **真正接通的**：chat Queen 的完整 loop（同步/流式，Anthropic 走 Messages SSE）、`run_worker` + worker spawn + worker 身份/声明工具 + worker 自带 task list + `[WORKER_REPORT]`（带 duration/output_file）、worker transcript/运行态落盘、手动 `resume_worker_ids` 与 colony `Start` 自动续跑（queued/running）、goal 闭环（加权标准执行 + 约束/Context 进 prompt + 未达标→改进信号）、tracker 建库/binding/读写、提醒中枢（7+1 source + ticker + 主要注入点 + worker 侧 hub + REST 镜像 + tracker 快照 + skills catalog + chat searchable tools）、judge（colony worker + overseer + 有 criteria 的 chat Queen，可由 goal 回填）、自动压缩、chat 与 worker spillover、任务计划、persona/记忆/恢复、每轮 6 条过往对话 excerpts、memory timeline 写入（flag 门控，启动回填 + 每轮更新）、colony load/unload/fork、scaffold 持久目录、`serve --colony` 预载 chat 与 `queen_resume_from`、`ask_user`/`credential_form` park 落盘、提交与恢复、triggers 唤醒与恢复、Sentinel 停车分类、skill 读写工具、Go-native `run_playbook`（含 status/stop）、MCP 启动装载与白名单、soul 沉淀并进 persona、会话检索三工具、并行只读工具、stall 订阅 + `node_stalled`、doom-loop 停车 + `node_tool_doom_loop`（park reason 可读）、Queen 30/50 软硬预算、worker `search_tools`/工具分层、`credential_status`、LLM/工具限速、会话花费硬上限、Queen 两阶段 phase 落盘、绑定 colony 的客户端消息/报告/trigger 单进 overseer 且回复以 queen stream 呈现、首条中继带 skills 目录。
- **最大的结构性差异**：colony 里不再有两个执行 lead——绑定 colony 且有 live overseer 时，客户端消息、worker 报告与 trigger 都只投 overseer，chat Queen 只做前端并记录用户 turn；overseer 的文本/执行事件经 session bridge 重盖成 queen stream。但两者仍是两份 conversation 存储：overseer 的回复不写回 chat transcript，chat 的 `pendingQuestions`/park 不由 overseer 更新，overseer 也没有 `ask_user`。
- **worker 的能力边界**：身份、预算、grace、report、tracker、skills、task list、resume、运行态落盘与独立 spillover 都对了；工具面是 "builtin + 声明项"，`search_tools` 分层已能按需加载 gateable 工具；运行中新增 skill 不可见，task list 自用不汇总，自动续跑只覆盖崩溃时 queued/running。
- **"代码在但没接"的典型**：`SubscribeTimelineTriggers`（写入已由等价实现承担，该函数本身仍零调用）、`ImprovementSignals` 读侧（`signals.jsonl` 只写不读，无自动再派工消费者）、`InstallWorkerEscalationRouting`（worker 升级到 session 的路由零调用）、旧 `colony/resume.go` 三函数（由 `resume_workers.go` 取代）。`credential-form` 提交路由与 doom-loop park reason 已在 1.0.24 接上，不再属于此类。
- **口径提醒**：本审计只回答"代码能否从生产入口走到、走到了是否生效"，不评价上游 Python 的行为；`L` 只表示交付侧有实测记录且未重跑，本轮所有新接线均为 `C`。MiniMax 不流式、Python playbook 不可执行、81 个第三方凭据模块未验证，均保留为已知差异而非接线断点。

## 10. 上一轮差异（历史存档：`4b6f1cf4` 相对 `ed457d75`）

> 本节保留上一版（基线 `4b6f1cf4`）的迁移记录，不再代表当前状态；当前状态见汇总表与 §13。

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

## 11. 上一轮变更记录（历史存档：1.0.16–1.0.21，基线 `c444c192`）

> 本节保留 `c444c192` 版的迁移记录，不再代表当前状态；当前状态见汇总表与 §13。

该版把两版之间落地的接线逐条对到代码，结论为 **64 已接 / 13 部分 / 3 未接（80 行）**。状态变化 7 条，另有 3 条状态不变但缺口收窄。

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

## 12. 历史变更记录（1.0.22，基线 `6723ab78`）

本版计数 **71 已接 / 9 部分 / 0 未接（80 行）**，相对 `c444c192` 的 64/13/3 是 **+7 已接、-4 部分、-3 未接**。状态变化 7 条（未接→已接 3、部分→已接 4），另有 3 条状态不变但缺口改写。行集不变。

### 12.1 未接 → 已接（3 条）

| # | 条目 | 依据（file:line @ 6723ab78） |
| --- | --- | --- |
| 1 | goals_outcome：成功标准带权重、可度量 | `agentloop/goal_eval.go:144`（`EvaluateGoal`）、`:181`（`weightedMetFraction`）、`:197`（逐条 metric）；`schemas/goal.go:142`（`IsSuccess`）定加权阈值；调用 `colony/goal_outcome.go:74/90` + `colony/runtime.go:1104`（`runWorker` 完成路径）。结果进 `WorkerResult.goal_evaluation`、`result.json` 的 `data`、`[WORKER_REPORT]` 的 `<data>` 与 `goal_progress` 事件（`eventbus/emit.go:82`）。`custom`/未知 metric 标 unsupported，不当作达标。 |
| 2 | goals_outcome：约束（hard/soft）与 Context 写入 prompt | `schemas/goal.go:157`（`ToPromptContext`）；`colony/runtime.go:827-828`（`buildContext` 写 `GoalContext`）；`colony/goal_outcome.go:33`（`workerSystemPrompt` 追加进系统提示，调用点 `runtime.go:1044`）；`agentloop/loop.go:513`（空转录时首条消息再带一份）。overseer 的 goal context 被 `!worker.Persistent` 显式置空。 |
| 3 | goals_outcome：未达标信号驱动改进 | `colony/improvement.go:124`（写 `signals.jsonl`）、`:151`（`kind=improvement_signal` custom 事件）、`:197`（未达标 criterion）、`:238`（judge RETRY/ESCALATE）、`:260`（空 tracker 表）；调用 `colony/runtime.go:1081`（`OnJudgeVerdict`）、`:1106`（`recordGoalImprovementSignals`）；信号并入 `publishReport` 的 `<data>`。 |

### 12.2 部分 → 已接（4 条）

| # | 条目 | 依据 / 收口的部分 |
| --- | --- | --- |
| 1 | goals_outcome：从 agent 定义加载结构化 `Goal` | `colony/runtime.go:244`（`Goal()`）+ `:827`（prompt）+ `:1104`（完成评估）；runtime 不再"只保存不消费"。`Goal()` 访问器本身仍只有测试调用。 |
| 2 | improvement：过往对话召回（每轮 6 条 dated excerpts） | `memory/past_conversations.go:222/477/511` + `server/chat_memory_wiring.go:31` + 调用点 `chat_runtime.go:607`；`PastRecallTopK=6`（`past_conversations.go:13`），`lastPastRecall` 去重。 |
| 3 | queen：Queen 的过往对话召回 | 同 2；`search_messages` 仍常驻会话工具表。 |
| 4 | queen：`memory_timeline` flag | 写侧 `server/chat_memory_wiring.go:59`（`startTimeline`，启动回填 10 个会话）/`:89`（`timelineAfterTurn`），调用点 `chat_runtime.go:303/645`，写 `<sessionDir>/timeline.jsonl` + meta；`memory/timeline.go:426/491/528` 三条提取/更新/回填函数不再零调用。 |

### 12.3 状态不变、缺口改写（3 条）

| 条目 | 旧缺口 | 当前缺口 |
| --- | --- | --- |
| the_loop：上下文压缩 / spillover（部分） | spillover 无生产者，超长结果不落盘 | 生产者已接（`tool_result_spillover.go:73/117`、`loop.go:1011/1021/1023`，默认 30k），但只有 chat 注入 `SpilloverDir`（`chat_memory_wiring.go:17-21`）；`BuildWorkerLoopConfig` 不设该目录，worker 仍不落盘。 |
| the_loop：judge（部分） | 只有 worker 的 `AgentSpec.SuccessCriteria` 能触发 judge | spec 无 criteria 时改由 goal 的 criterion 描述回填（`colony/runtime.go:1073-1077`、`criteria.go:37`）；chat Queen 与 overseer 仍不评估。 |
| worker_agent：工具子集 + 更紧预算（已接） | 工具面是 builtin + 声明项，gateable 工具没有加载入口 | 分层闭合：`ToolTierState` 非空时 loop 自动广告 `search_tools`（`agentloop/search_tools.go:49`、`loop.go:577`），promote 后下一轮 `RefreshDynamicTools`（`loop.go:633`）可见，未加载工具在 dispatch 前被拒（`search_tools.go:66/85`）。worker 侧仍不落盘 spillover。 |

### 12.4 备注 / 待验证

| 事项 | 说明 |
| --- | --- |
| 验证方式 | `go build ./...` 通过；新增单测通过：`go test ./internal/agentloop`（`-run` 过滤 Goal / Spillover / SearchTools）、`go test ./internal/colony`（Goal / Improvement / SearchTools）。`TestChatRuntimeWritesTimelineAfterTurn` 在整组 `-run` 下出现过一次 `TempDir RemoveAll` 竞态（异步 timeline 写入未及收尾），单独连跑 3 次均通过，记为测试侧 flake，不影响接线判定。 |
| `SubscribeTimelineTriggers` | `memory/timeline.go:645` 本身仍零调用；写入由等价的 `startTimeline` + `timelineAfterTurn` 承担（release notes 同口径）。已在行内注明，不计为缺陷。 |
| `signals.jsonl` 读侧 | `ImprovementSignals()` 无调用者，无 REST/CLI 读面；信号的实际消费是 `[WORKER_REPORT]` 的 `<data>`。release notes 亦记"尚无自动再派工消费者"。 |
| worker spillover | worker 的 `LoopConfig` 继承 30k 上限但 `SpilloverDir` 为空，`truncateToolResult` 直接返回原结果；这是"部分"而非"未接"的原因。 |
| live 证据 | 本轮新增能力均无交付侧 live 记录，全部标 `C`；support-matrix 1.0.22 只更新版本与测试计数。 |

## 13. 上一轮变更记录（1.0.23，基线 `d650199d`）

> 以下为 1.0.23 的迁移记录；1.0.24 的状态与变化见 §14。

本版计数 **77 已接 / 3 部分 / 0 未接（80 行）**，相对 `6723ab78` 的 71/9/0 是 **+6 已接、-6 部分**。状态变化 6 条，全部为"部分→已接"；另有 7 条状态不变但缺口改写。行集不变。

### 13.1 部分 → 已接（6 条）

| # | 条目 | 依据（file:line @ d650199d） |
| --- | --- | --- |
| 1 | colony：单目录 + scaffold | `colony_scaffold.go:31`（`ensureColonyScaffold`）+ `routes_colonies.go:474`：scaffold 建 `skills/`、`runs/`、空 `triggers.json`，幂等且不覆盖已有内容。fork 仍只写 `metadata.json`/`worker.json`，目录靠首次写入懒建。 |
| 2 | colony：跨会话持久 / 重启恢复 | `worker_state.go:78`（双份运行态）+ `:226`（`ResumeInFlightWorkers`）+ `runtime.go:523`（`Start` 调用）：崩溃时非终态 worker 自动重建；`loader_preloaded_session.go:53/62/106` 让预载会话按 metadata `queen_session_id` 走 `queen_resume_from`。 |
| 3 | coordination：提醒内容（tracker 快照 / skills / searchable tools） | `tracker_snapshot_reminder.go:105/135`（queen-only，读 bound `tracker.db`）+ `chat_reminders.go:88`（注册）；`loader.go:589` + `loader_reminders.go:26/30` 注入真实 `SkillsCatalog`；`chat_tool_tiers.go:43/153` 给 chat 建 MCP eager/searchable 分层。 |
| 4 | the_loop：stall / doom-loop | `session.go:431` → `chat_loop_guards.go:39/81` 订阅 `SubscribeStallCheck`，判定后发 `node_stalled` 并 `InjectEvent`；`doom_loop.go:28-34/178` + `loop.go:837/976` + `loop_guards.go:109` 在 5 次同名同参时停车、发 `node_tool_doom_loop`。 |
| 5 | the_loop：Queen 30/50 预算 | `loop.go:78-91` 的 `DefaultLoopConfig` 设 soft 30 / hard 50，`softBudgetCheckpointBlock`（`loop_guards.go:36`）在 `loop.go:811` 触发；worker 在 `worker_loop_config.go:60` 显式清 hard limit 保持 30×3=90。 |
| 6 | the_loop：压缩 / spillover | `worker_loop_config.go:65/122` 给每个 worker loop 注入 `cache/worker-tool-results/<pid-seq>`；`tool_result_spillover.go:73/79` 在首次 spill 时建目录，`loop.go:224` 恢复计数器。chat 仍用 `<session>/data`。 |

### 13.2 状态不变、缺口改写（7 条）

| 条目 | 旧缺口 | 当前缺口 |
| --- | --- | --- |
| coordination：提醒中枢（已接） | 6 source；REST 曾恒空 | 7 source（新增 `tracker_snapshot`）+ Sentinel；REST 读 live hub（`routes_reminders.go:374`）。仍缺 `STREAM_STALLED` source。 |
| coordination：parked loop（已接） | `credential_form` 无生产者 | `chat_credential_park.go:43/68` 由缺凭据工具结果生产 form、发 `client_credential_form_requested`、写 `pending_input`；仍无提交路由。 |
| queen：提问停车（已接） | 凭据等待未实现 | 同 parked loop；`pendingParkReason`（`chat_runtime.go:1271`）让 `credential_form` 优先于 `ask_user`。 |
| queen：持久会话（已接） | colony-bound 不走 `queen_resume_from` | 预载 `serve --colony` 已走；`POST /api/sessions` 带 colony_id 的普通创建仍不走（`loader_session_resume.go:73`）。 |
| `serve --colony` 预载会话（已接） | 每次新建 id，不恢复旧会话 | 按 metadata `queen_session_id` 复用旧 id/转录；首次启动才新建。仍未注入 `SkillsCatalog`。 |
| the_loop：崩溃恢复（已接） | 无自动续跑；colony-bound chat 不 resume | `Start` 自动续跑 queued/running；预载会话可 resume；`POST /api/sessions` colony_id 仍不行。 |
| worker_agent：崩溃恢复（已接） | 只有手动 `resume_worker_ids` | 增加运行态落盘与 `Start` 自动续跑；仍只覆盖 queued/running，终态不复活。 |

### 13.3 备注 / 待验证

| 事项 | 说明 |
| --- | --- |
| worker 自动续跑的边界 | 自动续跑只覆盖崩溃时非终态记录（正常为 queued/running）；`completed`/`failed`/`stopped` 不复活，优雅停止的 `stopped` 不自动复活。终态判定见 `worker_state.go:57`；自定义 `report_to_parent` status 未归一为终态时会被当作可续跑（待验证）。 |
| `POST /api/sessions` + colony_id | 普通创建路径即使带 `queen_resume_from` 也不复用（`loader_session_resume.go:73` 的 `!chatSessionColonyBound` 条件）；只有 `serve --colony` 预载路径按 metadata 复用。 |
| credential-form 提交 | `POST /api/credentials`（`routes_credentials.go:77`）是通用凭据保存，不消费 `pending_input.credential_form` / `correlation_id`，也没有提交后清 park 并续跑的接口；当前只有生产者与事件。 |
| 预载会话 SkillsCatalog | `attachPreloadedChatRuntime` 的 `ChatRuntimeDeps`（`loader_preloaded_session.go:180-190`）未传 `SkillsCatalog`；`chat_reminders.go:38-44` 回退空 provider，所以 `serve --colony` 会话的 skills 提醒仍恒空。 |
| doom-loop park reason | `parkOnDoomLoop` 会把 `TurnResult.ParkReason` 置为 `doom_loop`，但 chat 失败分支不读它：`!result.Success` 写 `unknown`（`chat_runtime.go:628`），`ExecuteTurn` 返回错误的分支写 `llm_error`（`:620`）。`node_tool_doom_loop` 事件仍照发，snapshot 的 park reason 会显示 `unknown`/`llm_error`。 |
| worker spillover 实路径 | `workerSpilloverDir`（`worker_loop_config.go:122`）用 `hivehome.Resolve()`，即进程 `HIVE_HOME`；不是 loader 的 `HomeRoot`。带自定义 `Runner.HomeRoot`（嵌入/测试）时可能写到默认 `~/.hive`，serve 环境需实测落盘路径（待验证）。 |
| stall 门控 | `SubscribeStallCheck` 在 `AddSession` 必定订阅，但 `StallJudgeSlot` 受 `stall_check` 配置与 judge slot 门控；未配置 judge 时订阅存在但不判定。 |
| 验证方式 | `go build ./...` 通过；聚焦测试通过：`go test ./internal/server`（scaffold / loop-guard / doom-park / credential-park 用例）、`go test ./internal/colony`（worker-state / 自动续跑 / worker spillover 用例）、`go test ./internal/agentloop`（Queen 30/50 用例）、`go test ./internal/cli`（预载会话重启复用用例），均 `-count=1`。 |
| live 证据 | 本轮新增能力均无交付侧 live 记录，全部标 `C`；support-matrix 1.0.23 只更新版本与测试计数。 |

## 14. 本轮变更记录（1.0.24，基线 `ab5c3dc7`）

本版计数 **80 已接 / 0 部分 / 0 未接（80 行）**，相对 `d650199d` 的 77/3/0 是 **+3 已接、-3 部分**。状态变化 3 条，全部为"部分→已接"；另有 22 条状态不变但缺口改写（colony 5、coordination 5、goals 6、improvement 1、queen 2、the_loop 2、worker 1）。行集不变。

### 14.1 部分 → 已接（3 条）

| # | 条目 | 依据（file:line @ ab5c3dc7） |
| --- | --- | --- |
| 1 | queen：两个阶段 Independent → Colony | `queen_phase_state.go:150`（`SetPhase`）、`:180`（`PersistPhase` 合并写 `meta.json`）、`:220`（`ReadSessionPhase`）、`:244`（`ResolveQueenPhase`）、`:262`（`AttachSessionPhaseState`）；`loader.go:561`（创建/恢复时挂 state）、`:806`（`load_colony` 切 colony）；`loader_preloaded_session.go:65`（预载恢复）；`loader_session_resume.go:163`（resume 时应用 `initial_phase`）。phase 通过 `GET /api/sessions/{id}` 的 `queen_phase` 与 `GET /api/sessions/{id}/tools` 的 `phase` 可读；`PATCH /api/queen/{id}/tools` 经 `routes_queens_tools.go:575-584` 刷新 live 会话的 MCP tier（`chat_tool_tiers.go:91/276`）。 |
| 2 | queen：Queen 与 colony overseer 是同一个 lead | `chat_runtime.go:359-372`（`Deliver` 先转投 overseer，chat 只 `recordOnly`）、`:419-427`（trigger 只转投）、`:1122`（报告转投）；`chat_colony_bridge.go:187`（客户端消息中继）、`:293`（报告）、`:302`（trigger）、`:139`（`mirrorColonyEvent` 把 overseer 文本/执行事件重盖到 queen stream）、`:214`（首条中继带 skills 目录）；`overseer_emitter.go:63`（overseer 文本）；`session_bridge.go:66`（`InjectOverseer`）。 |
| 3 | the_loop：park / resume | `routes_credentials.go:84` 注册 `POST /api/sessions/{id}/credential-form`；`routes_credentials_form.go:36` 校验 pending form → `:121` 写加密 store → `:136` `syncEnv` → `:139` `ClearCredentialFormPark` → `:144` `InjectEvent` 恢复；`chat_credential_park.go:77/90` 暴露 pending/clear；`cancelled` 分支 `:69-82` 清 park 并注入取消说明。 |

### 14.2 状态不变、缺口改写（22 条）

| 条目 | 旧缺口 | 当前缺口 |
| --- | --- | --- |
| colony：Queen 与 worker 同一 loop | overseer 无客户端消息源 | 有，且只投 overseer；仍两份 conversation、pending/park 不共享。 |
| colony：`run_worker` | overseer 无客户端消息入口 | 已通过 `chat_colony_bridge.go:187` 获得；预载会话可派活。 |
| colony：`serve --colony` | 未注入 `SkillsCatalog` | 已注入（`loader_preloaded_session.go:199`）；预载 `QueenName` 为空 → 默认 queen scope。 |
| colony：任务计划 | worker 没有 task 工具 | 该项是旧版笔误：worker 自 1.0.22 起有 4 个 task 工具（`worker_task_tools.go:42/77`），只是不汇总。 |
| colony：triggers | fire 后只排 chat turn | live overseer 时 `relayTriggerToOverseer` 只投 overseer；仍只对 colony-bound 会话生效。 |
| coordination：worker 报告 | 直接注入 chat Queen | live overseer 时只投 overseer；overseer 自身报告才回 chat 前端。 |
| coordination：提醒中枢 | 预载 skills 为空 | 已注入；执行在 overseer 时 chat hub 的 skills source 不参与该次输入，目录随首条中继走。 |
| coordination：提醒内容 | 预载 skills 恒空 | 已注入，但固定 default queen scope。 |
| coordination：`ask_user` | credential_form 无提交路由 | 提交路由已存在；overseer 没有 `ask_user`，其 park 不回写 chat。 |
| coordination：parked loop | 无 credential-form 提交 | 已有提交/清 park/恢复；仍只在 chat runtime 工具错误路径生产 form。 |
| goals：加载 `Goal` | chat Queen 无 Goal | 仍无 Goal；只继承 goal 的 criteria 文本给 judge。 |
| goals：加权标准 | chat Queen 不评估 | 仍不做加权 `EvaluateGoal`；只通过 criteria 参与 judge。 |
| goals：criteria 编译 | chat Queen 无来源 | `ChatRuntimeDeps.SuccessCriteria` 或绑定 colony 的 goal 继承（`chat_runtime.go:1077-1089`）；plain chat 仍无。 |
| goals：loop judge | chat Queen/overseer 不评估 | overseer 继承 goal criteria（`colony/runtime.go:1160-1167`）；chat 有 criteria 时 `ConfigureJudge`；plain chat 仍关闭。 |
| goals：retry 回注 | chat 无 judge | 有 criteria 的 chat / overseer 同路径；无 criteria 仍无 RETRY。 |
| goals：judge 流水线 | 只在 colony worker | 覆盖 worker、overseer、有 criteria 的 chat Queen。 |
| improvement：judge retry 回注 | 只覆盖 colony worker | 同上；绑定 colony 的 chat 前端不执行 model turn。 |
| queen：选择 Queen 开会话 | `initial_phase` 被忽略 | 已消费（`loader_session_resume.go:163`）；命名 queen profile 进入 phase identity。 |
| queen：`ask_user` | credential_form 无提交 | 已有提交；overseer 无 `ask_user`。 |
| the_loop：judge | chat/overseer 无 criteria | 已覆盖；plain chat 仍无。 |
| the_loop：stall/doom | doom park reason 被覆盖 | `turnFailureParkReason` 保留 `doom_loop`；事件、park state、`pending_input` 一致。 |
| worker_agent：报告 → Queen | 直接注入 chat Queen | live overseer 时只投 overseer；非 overseer worker 的 client IO/tool/token 不镜像。 |

### 14.3 备注 / 待验证

| 事项 | 说明 |
| --- | --- |
| Queen/overseer 两份 conversation | 操作面已是单执行 lead，但 overseer 的回复不写回 chat transcript；chat 的 `pendingQuestions`/park 不由 overseer 更新；overseer 工具面没有 `ask_user`。 |
| chat Queen 不再执行 model turn | 仅限绑定 colony 且有 live overseer 时的客户端消息/报告/trigger；`recordOnly` 只记录用户 turn。overseer 自己的终态报告仍会作为 `[WORKER_REPORT]` 进入 chat 前端，可能触发一次 chat 回合；无 overseer 时回退 chat Queen。 |
| 非 overseer worker 事件 | client IO 与 tool/token 事件不在 `colonyBridgeEventTypes`/`mirrorColonyEvent` 内，不镜像到会话流；生命周期与报告仍镜像。 |
| 预载 catalog scope | `loader_preloaded_session.go:199` 用 `chatSessionQueenScope(record.QueenName)`；预载记录 `QueenName` 为空，因此固定 `default`，命名 queen 的私有 skills 不出现。 |
| credential form 生产者 | 只有 chat runtime 的 `parkForMissingCredential` 在缺凭据工具错误时生产 form；绑定 colony 且 live overseer 执行时，overseer 的 executor 不走该路径（待验证是否有其它生产者）。 |
| `/api/queen/{id}/tools` 的 phase | 该 GET 响应体没有 phase 字段；phase 在 `GET /api/sessions/{id}`（`queen_phase`）与 `GET /api/sessions/{id}/tools`（`phase`）。发布说明第 13 行"GET /api/queen/{id}/tools 有 phase"不准确；PATCH 的会话刷新依赖 `PhaseState != nil`，这部分成立。 |
| 已知边界 | MiniMax 仍不流式；Python `.play.py` 明确 `unsupported`、0 派发；81 个第三方凭据模块未验证。 |
| 验证方式 | `go build ./...` 通过；聚焦测试通过（`-count=1`）：`internal/server` 的 `TestCredentialFormSubmission*`、`TestChatDoomLoopParkReasonReachesEventsAndPendingInput`、`TestChatRuntimeRelaysClientMessageToOverseer`、`TestChatRuntimeRelayOwnsReportsAndTriggers`、`TestChatRuntimeWithoutOverseerKeepsChatQueen`、`TestChatRuntimeConfiguredCriteriaRunsJudge`、`TestChatRuntimeInheritsColonyGoalCriteriaForJudge`、`TestApplyQueenMCPToolAllowlistFiltersChatTier`、`TestQueenToolsRoutesRefreshPhaseSession`；`internal/cli` 的 `TestCreateSessionPersistsInitialPhase`、`TestCreateSessionRestoresPhaseAfterRestart`、`TestLoadColonySwitchesPhaseAndPersists`、`TestPreloadedColonySessionInjectsSkillsCatalog`；`internal/colony` 的 `TestOverseerInheritsGoalCriteriaAndRunsJudge`、`TestOverseerWithoutCriteriaNeverCallsJudge`、`TestOverseerTurnPublishesClientOutput`。 |
| live 证据 | 本轮新增能力均无交付侧 live 记录，全部标 `C`；support-matrix 1.0.24 只更新版本与测试计数。 |
