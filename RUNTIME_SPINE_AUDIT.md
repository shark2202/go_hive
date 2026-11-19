# Go 运行时 × Runtime Spine 八层接线审计

- 审计基线：`4b6f1cf4`（`merge mcp-loading: MCP 启动装载与工具注册`）
- 上一版基线：`4daf4222`（汇总表 30 已接 / 12 部分 / 28 未接；按行逐条重算为 32 / 11 / 27，见 §1 注）
- 审计日期：2026-11-19
- 审计对象：`runtime_spine.md` 的八层栈；Go 模块根为 `go_hive/go-hive/`
- 分层依据说明：`runtime_spine.md` 仍未入库，本审计与上一版一样按既有文本核对八层定义，不把该文件计入提交。
- 判定口径（严格）：
  - **已接**＝从 `cmd/hive` → `internal/cli` 或 `internal/server` 的 HTTP 路由 / 会话工具 / agent loop 真实可达，且在生产路径上生效；仅测试调用、仅枚举、仅提示文案不算。
  - **部分**＝有真实入口但只完成一半：只读不写、只写配置不生效、只覆盖一个角色/阶段、只在内存不落盘，或一个合并机制里只有一种生效。
  - **未接**＝代码存在但生产入口走不到（死代码 / 仅测试 / 只有类型、枚举或提示文案）。
  - 管理配置路由（MCP、Sentinel 通知、Soul 编辑）本身可达不等于其运行时机制已接；本轮 MCP 与 Sentinel 的运行时装配已补齐，因此相应行按"运行时是否真的跑"重新判定，而不是继续按配置路由计分。
- 证据口径：
  - `L`＝交付侧既有 live 实测记录，出处 `go_hive/docs/support-matrix.md`（本轮只读审计，未重跑）。该表写于本轮新接线之前，不覆盖 skill 工具、run_playbook、trigger/Sentinel、MCP 装载、soul 沉淀。
  - `C`＝本轮从 `cmd/hive` 逐段静态追踪，并用 `go build ./...` 与定向 `go test` 验证可编译；测试本身不计入接线判定。
- 说明：只读审计，不改产品代码、不改测试、不改 `go.mod`。下表路径均相对 Go 模块根 `go_hive/go-hive/`；行号以 `4b6f1cf4` 为准。

## 生产入口与追踪方法

`cmd/hive/main.go:7` → `internal/cli.NewRunner`（`internal/cli/cli.go:112`）→ `Runner.Run`（`:264`）→ `serve` → `defaultServe`（`internal/cli/loader.go:266`）→ `hiveserver.NewApp`（`internal/server/app.go:88`）→ 各路由表。

`defaultServe` 在任何会话之前先做三件事，它们决定了本轮几个层的判定：

1. `SyncCredentialEnv`（`loader.go:286`）：已保存凭据进入进程环境。
2. `startMCPRuntime`（`loader.go:292` + `mcp_loader.go:66`）：读 MCP 配置、连服务器、把 loader 装成进程级 attacher；之后每个 `NewToolRegistry` 自动 attach（`tools/mcp_bootstrap.go:66`、`registry.go:150`）。
3. `NewSentinelManager` + `Start`（`loader.go:300-304` + `sentinel_runtime.go:41`）：建 manager、开 escalation drain 与 inbox/Telegram/Slack listener。

三级执行入口：

1. **chat Queen**：`createSessionWithRuntime`（`loader.go:536`）→ `NewChatRuntime`（`chat_runtime.go:164`）→ 每轮 `runTurn`（`:479`）→ `AgentLoop.ExecuteTurn`（`agentloop/loop.go:478`）。
2. **colony runtime**：`newColonyHost`（`loader.go:435`）→ `colonyhost.New`（`host.go:68`）→ `ColonyRuntime`；`run_worker` 经 `RunWorkerSpawnerFromContext` 进入 `SpawnBatchWithOptions`（`runtime.go:563`）。
3. **`serve --colony` 预载会话**：`loadPreloadedColonySession`（`loader_preloaded_session.go:33`）→ `buildColonyHost` + `StartOverseer` → `attachPreloadedChatRuntime`（`:91`）→ `RestoreSessionTriggers`。

本审计的每一行都从这三个入口之一回溯；没有回到这些入口的机制判为未接。

---

## 1. 八层汇总

| 层 | 已接 | 部分 | 未接 | 合计 |
| --- | ---: | ---: | ---: | ---: |
| 1. 执行（The Loop） | 5 | 4 | 1 | 10 |
| 2. 组织（Colony / Queen / Worker） | 8 | 2 | 0 | 10 |
| 3. 状态（Plan / Tracker） | 9 | 0 | 1 | 10 |
| 4. 信号（Event Bus / Reminder Hub） | 4 | 1 | 3 | 8 |
| 5. 方法（Skill / Playbook） | 6 | 1 | 0 | 7 |
| 6. 自治（Trigger / Sentinel） | 7 | 0 | 0 | 7 |
| 7. 沉淀（Memory / Soul） | 4 | 0 | 3 | 7 |
| 8. 能力边界（Tools / MCP / Credentials / Budgets） | 7 | 2 | 2 | 11 |
| **合计** | **50** | **10** | **10** | **70** |

注：上一版汇总表写 30/12/28，但按行逐条重算是 32/11/27（组织层多 1 已接、能力边界层多 1 已接，对应少 2 部分、少 2 未接）。本版同样按行级口径重算，为 **50/10/10**；相对 32/11/27 的变化是 +18 已接、-1 部分、-17 未接。

读法：这轮的重心整体上移。第 5 层（方法）与第 6 层（自治）从"几乎全空"变成"主路径全通"（方法层只剩 playbook status/stop 一个半截）；第 2 层（组织）只剩 overseer 的输入源问题；第 7 层（沉淀）补上了 soul 自动写，但 soul 仍不进 persona。新的短板集中在第 4 层（信号）与第 8 层（能力边界）的"半截"机制：colony 事件不进 UI、worker 没有 reminder hub、MCP 白名单不生效、rate limit/花费上限不执行。

---

## 2. 分层明细

### 第 1 层：执行（The Loop）

概念主张：一个多轮流式 LLM 会话循环；每轮流式输出、并行跑只读工具、回注结果、决定停止/继续；judge 自纠错；Queen/worker 只是一组 `LoopConfig` 差异；内建崩溃恢复、上下文压缩、计量与提醒。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 单一多轮 agent loop（Queen 与 worker 共用） | `internal/agentloop/loop.go:478`（`ExecuteTurn`）；`internal/server/chat_runtime.go:271/479`；`internal/colony/runtime.go:982/1024` | chat：`NewChatRuntime` → `runTurn` → `loop.ExecuteTurn`；worker：`run_worker` → `spawnBatchOn` → `runGatedWorker` → `runWorker` → 同一 `AgentLoop` | 已接 | C | worker/overseer 手工构造 `&AgentLoop{}`，不走 `NewAgentLoop`，因此没有 `Hub`（见第 4 层） |
| 流式输出逐帧推 UI | `internal/server/chat_stream.go:43`；`internal/server/chat_runtime.go:496/699/709`；`internal/llm/stream_openai.go:42` | CLI provider 实现 `StreamCompleter` → `newChatStreamingCompleter` → `StreamTextDelta` → `client_output_delta` → session SSE | 已接 | L | Anthropic/MiniMax 明确不支持流式并回退非流式；tool-call 参数仍按完整对象聚合，不逐片下发 |
| 只读工具并行执行 | `internal/agentloop/loop.go:660-770` | 工具批内 `for i, tc := range toolCalls` 串行执行 | 未接 | — | `ConcurrencySafe` 只在工具定义里写入，loop 从不读取；没有并发分组 |
| 工具结果回注、循环直到不再调用工具 | `internal/agentloop/loop.go:614`、`:756`（`AddToolResult`） | `ChatRuntime.runTurn` / `runWorker` 都走该路径 | 已接 | C | — |
| judge：accept / retry / escalate | `internal/agentloop/judge.go:92/109/127`；`internal/colony/runtime.go:1028/1080` | colony worker/overseer 构造 loop 后 `ConfigureJudge`；criteria 非空才评估；RETRY 回注 `[Judge feedback]` 用户消息 | 部分 | C | 只有 colony worker 有 criteria；chat Queen 无 criteria 且从不 `ConfigureJudge`；overseer 的 `queenOverseerSpec` 无 criteria，实际不评估 |
| worker grace + 更紧预算 | `internal/colony/worker_definition.go:9`；`internal/colony/worker_loop_config.go:46`；`internal/agentloop/loop.go:529-556` | `run_worker` → `BuildWorkerLoopConfig` 应用 3 轮 + 1 grace / 每轮 30 / 硬停 90 / 终身 200；grace 轮只允许 terminal tools | 已接 | C | grace 白名单含 `task_update`，但 worker 工具表没有 task 工具，该项不可达 |
| 崩溃恢复：transcript cursor + 会话恢复 | `internal/agentloop/conversation.go:1126/1213`；`internal/server/chat_runtime.go:255`；worker 侧 `colony/worker_transcript.go:40`、`resume_workers.go:38` | chat 每条消息 `persist`；`NewChatRuntime` 恢复 parts；worker transcript 落盘，`resume_worker_ids` 可续跑 | 部分 | C | colony-bound chat 会话仍不走 `queen_resume_from`（`cli/loader_session_resume.go:73`）；没有进程重启自动续跑在飞 worker 的入口 |
| park / resume（`ask_user` 停车） | `internal/server/chat_runtime.go:1019/1057/1068`；`internal/agentloop/park_reason.go:16` | `ask_user` → `pendingQuestions` + `client_input_requested(park_reason=ask_user)`；下一条用户消息清空 | 部分 | C | 停车状态只在内存；无 park 落盘、无 resume 入口；凭据等待未实现 |
| 自动上下文压缩 | `internal/agentloop/loop.go:320`；`internal/agentloop/conversation.go:736/931`；`internal/agentloop/llm_compact.go:22` | 每轮 LLM 调用前 `NeedsCompaction()` → `LlmCompact` → `conv.Compact`；触发 PRE/POST_COMPACT | 部分 | C | 自动压缩已接；超大工具结果 spillover 仍无生产者（只读 `SpilloverPath`） |
| 每次 LLM 调用计量（tokens/cost） | `internal/agentloop/loop.go:595`；`internal/server/chat_runtime.go:713/721` | `LLMTurnComplete` → `chatTurnEmitter` → `llm_turn_complete`（input/output/cached/cost） | 已接 | C | — |

**小结**：执行层骨架完整，流式、工具回注、judge（worker 路径）、grace、压缩、计量都真实运行；worker transcript 落盘让崩溃恢复不再只覆盖 chat。缺口仍是"并行只读工具""park/resume 落盘""spillover 生产者"，以及 chat Queen 没有 judge/criteria。

---

### 第 2 层：组织（Colony / Queen / Worker）

概念主张：colony 是单目录、可移植、可定时唤醒、跨会话持久的部署单元；Queen 与 worker 是同一个 loop；Queen 通过 `run_worker` 在运行时按需扩展；Create Colony 对话框确认后才 fork。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Colony 单目录 + scaffold | `internal/server/routes_colonies.go:409`、`:440`；`internal/hivehome/home.go:88/129` | `POST /api/colonies/{colony_id}/scaffold` → 只写 `metadata.json`、`worker.json` | 部分 | C | scaffold 不建 `skills/`、`triggers.json`、runs；`skills/` 由首次 `write_skill` 懒建，`tracker/` 由 runtime `Start` 补建（`colony/tracker.go:61`） |
| Colony 导入 / tar 可移植 | `internal/server/routes_colonies_import.go:705` | `POST /api/colonies/import*` → 落盘 `home.ColoniesDir()` | 已接 | C | — |
| Colony load / unload | `internal/cli/loader.go:727/746/767`；`internal/server/routes_session_colony.go:55/170` | `POST/DELETE /api/sessions/{id}/colony` → `deps.Colony` → `newColonyHost` + `startColonyHost` | 已接 | C | unload 后 chat 侧绑定要等下一次 `GetSession` 才解除；在跑的 turn 仍可能持有旧 runtime |
| `serve --colony` 启动即加载 runtime、overseer 与 chat | `internal/cli/loader.go:418/469`；`internal/cli/loader_preloaded_session.go:33/91` | `hive serve --colony <path>` → host `Start` + `StartOverseer` → 有 provider 时 `ChatRuntime` + `QueenExecutor` → `RestoreSessionTriggers` → `AddSession` | 已接 | C | 旧 `L` 只覆盖加载；每个 `--colony` 仍新建 session id，不恢复旧会话/旧 worker；无 provider 时 `Chat` 为 nil，`POST /chat` 503 |
| Queen chat 会话 + persona | `internal/cli/loader.go:536`；`internal/server/chat_runtime.go:164/1030` | `POST /api/sessions` → `createSessionWithRuntime` → `NewChatRuntime` → `IdentityPrompt` | 已接 | C | `default` queen 无 profile 时回退通用身份（预期） |
| Queen 用 `run_worker` 运行时扩展 | `internal/colony/queen_tools.go:75/204`；`internal/cli/session_tools.go:111/258`；`internal/server/chat_runtime.go:879/896` | 会话工具表常驻 `run_worker`；`toolsForTurn` 在未绑 colony 时过滤；绑定后 `SpawnBatchWithOptions` 并启动软/硬超时看门狗 | 已接 | L | 预载 `serve --colony` 会话现在也可派活；`resume_worker_ids` 已实现（见 worker 行） |
| Worker 是同一 loop + 终态报告 | `internal/colony/runtime.go:982/1118`；`internal/colony/queen_tools.go:492`；`internal/server/chat_runtime.go:315/942` | `spawnBatchOn` → `runGatedWorker` → `runWorker` → `publishReport` → `SUBAGENT_REPORT` → `[WORKER_REPORT]` | 已接 | L | 报告已有 duration/output_file；预载 colony 无 chat runtime 时无人消费 |
| Colony overseer 是 client-facing Queen | `internal/colony/runtime.go:694/1061`；`internal/colony/overseer_tools.go:112`；`internal/cli/loader.go:484` | `StartOverseer` → `runOverseer`：先跑一轮 seed，然后 `wakeCh` 醒来消费注入输入，用 `overseerToolDefs`（`run_worker` + queen tracker + task） | 部分 | C | 有 Queen 工具面、持久 loop、transcript 恢复，但没有输入源：没有 HTTP/总线把用户消息或 worker 报告投递给它；客户端对话的是另一个 chat Queen |
| Worker 是"聚焦克隆"：worker prompt + 配置的工具子集 | `internal/colony/worker_definition.go:39/113`；`internal/cli/colony_worker_definition.go:58/92`；`internal/colony/worker_tool_selection.go:28`；`internal/colony/runtime.go:856` | fork 写 worker `system_prompt` 与 `tools`；`runWorker` 用 `systemPromptFor(spec)`；`baseWorkerTools` 把声明项从 integration/MCP 池展开 | 已接 | L | builtin base 始终保留；空 `tools` 只剩 builtin；193ace3d 之前 fork 的旧 worker.json 仍是 Queen persona |
| Worker 崩溃恢复 / `resume_worker_ids` 续跑 | `internal/colony/resume_workers.go:38`；`internal/colony/worker_transcript.go:40`；`internal/colony/queen_tools.go:231-280` | transcript 落盘 `<colony>/workers/<id>/conversations`；`run_worker(resume_worker_ids=…)` → `ResumeWorkers` 恢复同一 conversation（可带 guidance）→ 继续 loop；逐 id 返回失败 | 已接 | C | 活动中的 worker 拒绝 resume；无 meta.json 的旧 worker 不能 resume；resume 后预算 pinned，不做自适应收紧 |

**小结**：组织层的"可部署、可加载、可派活、可恢复"都通了：`serve --colony` 现在既是真 colony 又是真 chat session，worker 有身份、工具面和 resume。唯一的结构性缺口是 overseer：它已经长得像 Queen，却没有被接上任何输入。

---

### 第 3 层：状态（Plan / Tracker）

概念主张：Plan 是 Queen 的意图（`tasks.json`，Action Plan 面板）；Tracker 是 colony 的事实（`tracker/tracker.db`，每个工作单元一行）。两者不能混。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Plan 文件存储：`tasks.json` 创建 / 更新 / 读取 | `internal/tasks/store.go:159/244`；`internal/tasks/session_tools.go:747`；`internal/cli/task_tools.go:47` | 会话创建 → `newSessionToolCatalogue` → `newTaskToolSet(sessionDir)` → `task_create/task_update/task_list/task_get` → `TaskStore` 写 `tasks.json` | 已接 | C | 只有 chat Queen 与 overseer；worker 没有 |
| Plan REST 快照 / 归档清理 | `internal/server/routes_tasks.go:37`；`internal/server/app.go:166` | `GET /api/sessions/{id}/tasks`、`POST .../tasks/unarchive`、`POST .../tasks/clear-completed` | 已接 | C | 只有读快照和两个归档动作；没有 REST 创建/编辑（由 agent 工具写） |
| Plan 跨 reload 可见 | `internal/tasks/store.go:106/174`；`internal/cli/task_tools.go:60` | 同一 session 目录重建工具集，读同一 `tasks.json`；`newTaskToolSet` 先 `MkdirAll(root)` | 已接 | C | colony-bound 会话不走 `queen_resume_from`；session 目录迁移时 `sessionDirCache` 可能需重启刷新 |
| Worker 自己的 task list | `internal/colony/runtime.go:825`；`internal/tasks/register.go:5` | worker 工具 = builtin + 声明项 + tracker + `report_to_parent` | 未接 | — | worker 没有 task 工具；`RegisterTaskTools` 零生产调用；grace 白名单里的 `task_update` 因此不可达 |
| Colony overseer 的 task list | `internal/colony/overseer_tools.go:51/112`；`internal/colony/runtime.go:218` | `newOverseerTaskSet(storage)` 建在 `<colony>/_misc/overseer/tasks.json`；`overseerToolDefs` 追加 4 个 task 工具 | 已接 | C | 作用域是独立的 `overseer` 会话，不与 chat Queen 共享计划 |
| Tracker 建库 + binding | `internal/colony/tracker.go:61`；`internal/tracker/binding.go:67`；`internal/colony/runtime.go:489/563` | colony `Start` / 每次 spawn → `provisionTracker` → `BindingForColonyDir` → `tracker/tracker.db` | 已接 | L | 只对含 `metadata.json` 的目录建库 |
| Tracker binding 注入 worker | `internal/colony/tracker.go:137`；`internal/colony/runtime.go:606`；`internal/colony/worker.go:377` | spawn → `workerInputData` 带 binding → `BuildWorker` 设 `ColonyBindingProvider` | 已接 | L | `RuntimeOptions.Binding` 没有生产注入者（只有测试/宿主自备） |
| Queen tracker 工具（SQL / 声明可写列 / upsert / query） | `internal/tracker/tools.go:1030`；`internal/cli/session_tools.go:118-137/265-279` | 会话工具表常驻 4 个 tracker 工具；执行时 `TrackerBindingFromContext` 取绑定，无绑定返回 `ErrNoBinding` | 已接 | L | 未绑 colony 时工具仍可见但不可用 |
| Worker tracker 工具（upsert / query） | `internal/colony/tracker.go:109/126`；`internal/colony/runtime.go:841` | worker 工具表追加 role=worker 表；`executorFor` 用 `workerTrackerRegistry` | 已接 | L | 只有 `tracker_upsert/tracker_query`；无 SQL（符合概念） |
| Tracker REST 读表 / 读行 / 改行 | `internal/server/routes_colony_workers_data.go:913-922` | `GET /api/colonies/{id}/data/tables`、`.../tables/{table}/rows`、`PATCH .../rows` | 已接 | C | 这三个路由无 insert/delete（全局数据路由另有） |

**小结**：状态层只剩"worker 没有自己的 task list"一个缺口；overseer 现在也有自己的 Plan。Tracker 的建库、binding、Queen/worker/overseer 三侧工具与 REST 全部接通。

---

### 第 4 层：信号（Event Bus / Reminder Hub）

概念主张：Event bus 传瞬时信号（worker 报告、实时 transcript、SSE）；Reminder hub 在已知生命周期点注入短提示；二者都不是 tracker，也不是 memory。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 会话实时 transcript → SSE → UI | `internal/server/chat_runtime.go:629-709`；`internal/server/chat_routes.go:168`；`internal/eventbus/bus.go:154/415` | 每轮 emit execution / loop / assistant / tool / usage → session `EventBus` → `GET /api/sessions/{id}/events`；同时写 `events.jsonl` | 已接 | L | — |
| Worker 终态报告 → Queen 的 `[WORKER_REPORT]` | `internal/colony/runtime.go:1118/1200`；`internal/colony/queen_tools.go:492/426`；`internal/server/chat_runtime.go:315/942` | 终态 `SUBAGENT_REPORT` → `FormatWorkerReport` → `InjectWorkerReport` → 下一轮以非 client user message 注入 | 已接 | L | 预载 colony 无 chat 时无人消费；报告只回 chat Queen，不回 overseer |
| Colony 事件 → 会话 / UI SSE | `internal/host/host.go:82-84`；`internal/cli/loader.go:458-472`；`internal/server/chat_routes.go:36` | colony host 自建 bus；session bus 独立；没有 `/api/colonies/{id}/events` 路由 | 未接 | C | worker 进度只能轮询 `GET /api/sessions/{id}/workers`；colony bus 的事件不进 UI，也不进 session `events.jsonl`（只有 `SUBAGENT_REPORT` 被 chat 订阅后转成 user message） |
| Reminder Hub：6 个 source 注册 + `Hub.Start` | `internal/server/chat_reminders.go:54-80`；`internal/agentloop/reminder_hub.go:235` | `NewChatRuntime` → register task / colony_parallel / active_workers / searchable_tools / skills_catalog / idle + Sentinel escalation → `Bind` → `Hub.Start`（idle ticker） | 已接 | L | 只有 chat Queen；worker/overseer 的 loop 没有 Hub |
| Reminder 触发点 | `internal/server/chat_runtime.go:537/544/602`；`internal/agentloop/loop.go:281/324/340/496/740/813` | SESSION_START / USER_PROMPT_SUBMIT / POST_TOOL_USE / PRE_COMPACT / POST_COMPACT / STOP / IDLE_TICK / TOOL_BUDGET_CHECKPOINT | 已接 | C | `STREAM_STALLED` 没有 source 注册；chat 侧 `SESSION_START` 可能在 runtime 和 loop 各触发一次（重复注入风险） |
| Reminder 内容供给（fleet / task / tools / skills / tracker） | `internal/server/chat_reminders.go:26-49/70`；`internal/agentloop/active_workers_reminder.go:27`；`internal/agentloop/colony_parallel_nudge.go:54`；`internal/agentloop/tool_skill_reminders.go:23` | 注册 task、colony_parallel、active_workers、searchable_tools、skills_catalog、idle；provider 从 live session 读 | 部分 | C | 没有 tracker 快照 source；`skills_catalog` provider 恒空（loader 未传 `SkillsCatalog`）；chat 没有 `SearchableToolsProvider`，该 source 在 chat 恒静默 |
| Worker 侧提醒 | `internal/colony/runtime.go:1024`（手工 `AgentLoop{}`，Hub 为 nil） | `runWorker` 不走 `NewAgentLoop` | 未接 | — | worker 没有 reminder hub；grace/budget 提示是 loop 内硬编码文案，不是 hub source |
| REST reminder surface 与 chat hub 一致 | `internal/server/routes_reminders.go:302/124` | `GET/DELETE /api/sessions/{id}/reminders` 读独立 `ReminderRuntime`；chat 不再调用 `TurnComplete` | 未接 | C | REST surface 恒空，与 chat 的 hub 是两套状态；`serveReminders` 只在 `loader.go:277/315` 注入，没有生产者 |

**小结**：信号层的主路径（chat transcript SSE、worker 报告回 Queen）是通的，且有 live 记录；Sentinel 也作为第 7 个 source 接进来了。但 colony 自己的事件总线没有桥到会话/UI；worker/overseer 没有 reminder hub；REST reminder surface 与 chat hub 脱节；提醒内容缺 tracker 快照和 skills catalog 数据。

---

### 第 5 层：方法（Skill / Playbook）

概念主张：Skill 是"怎么做"的可复用方法（Queen 写、worker 按需加载）；Playbook 是"怎么批量做"的确定性 runner（查询 tracker 未完成行、并发派发、重试/退避/死信、按 tracker 现状 resume）。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Skill 解析 / 安装 / 校验库 + `hive skill` CLI | `internal/skills/parser.go:51`、`installer.go:125/196`；`internal/cli/skill.go:72`；`internal/cli/cli.go:308` | `hive skill ...` → `Runner.skillCommands()` → 文件系统 | 已接 | C | operator 面；安装/校验后的技能不会自动进入 agent 上下文 |
| Agent 写技能 `write_skill` | `internal/skills/agent_tools.go:54/63/178`；`internal/cli/session_tools.go:94/250` | chat Queen 会话工具表常驻 `write_skill`；`ExecuteAgentTool` → `WriteSkill` 写 colony/queen/global 三个 scope | 已接 | C | overseer 工具面没有 `write_skill`；global scope 只把请求落到会话目录，不直接改共享库 |
| Agent 读技能 `skill_view` / 技能目录加载 | `internal/skills/agent_tools.go:54/178`；`internal/agentloop/types.go:144`；`internal/agentloop/loop.go:828`；`internal/colony/runtime.go:404`；`internal/colony/skills_surface.go:40` | chat Queen 有 `skill_view`/`list_skills`；worker spawn 时拿到 `SkillDirs` + `SkillsCatalogPrompt`，`view_file` 在技能目录内被拦截成 skill 内容 | 已接 | C | 运行中 Queen 新写的 skill 要等 worker 下一批才可见 |
| Colony `skills/` 目录生产写入 | `internal/server/routes_colonies.go:440`；`internal/cli/colony_worker_definition.go:31`；`internal/skills/skill_writer.go:170` | scaffold/fork 不预建；`write_skill(scope='colony')` → `MkdirAll(<colony>/skills)` → 写 `SKILL.md`；`GET /colony/skills` 从磁盘读 | 已接 | C | 目录是懒创建；没有"空目录即就绪"的显式初始化 |
| Playbook runner（确定性批次、重试/退避/死信） | `internal/colony/playbook_tools.go:129`；`internal/playbook/runner.go:190/423`；`internal/playbook/rows.go:102/144` | `run_playbook` → `RunPlaybook` → `playbook.NewPlaybookRun` → 按 `pending_sql` 查行、并发派 worker、重试/退避/死信、同步返回 | 已接 | C | 只支持 native JSON；Python `.play.py` 返回 `status='unsupported'` 且 0 派发；同步执行 |
| Agent Playbook 工具（`run_playbook` / status / stop） | `internal/cli/session_tools.go:104/254`；`internal/colony/playbook_tools.go:76` | 会话工具表注册 `run_playbook`；执行器直接调 `colony.RunPlaybook` | 部分 | C | 只有 `run_playbook`；没有独立的 status/stop 工具或路由（状态只在返回值与 `playbook_runs` 表里） |
| Playbook run log / 按 tracker resume | `internal/colony/colony_db.go:78/107`；`internal/colony/playbook_tools.go:198`；`internal/playbook/rows.go` | 每次 run 写 `playbook_runs`；再次 `run_playbook(playbook_name=…)` 重查 `pending_sql`，done 行自然消失 | 已接 | C | 没有后台完成通知；没有暂停/继续单次 run 的入口，resume 靠重跑 |

**小结**：方法层从"八层里最空"变成"主路径全通"：Queen 能写/读 skill，worker 能加载，playbook 有真 runner 和 run log，resume 靠 tracker 现状成立。边界是 Python playbook 明确不支持、playbook 同步执行、没有独立 status/stop 工具。

---

### 第 6 层：自治（Trigger / Sentinel）

概念主张：Trigger 从外部唤醒 colony（cron / interval / webhook）；Sentinel 在 Queen 停车时决定 nudge、标记完成或升级到人。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Trigger CRUD + activate/deactivate | `internal/server/routes_session_triggers.go:88/245/437/511`；`internal/server/routes_sessions.go:96-111` | `POST/PATCH/GET /api/sessions/{id}/triggers*` → `PutTrigger` + `PersistActiveTriggers` + `saveTriggerToAgent`；activate 启动 timer/webhook | 已接 | C | 只对 `ColonyID != ""` 的会话真正生效 |
| Trigger timer 真实唤醒 | `internal/server/trigger_runtime.go:416/455`；`:505-525` | `StartTriggerTimer` → `runTimerLoop`；到点 `queenNode(session)` → `InjectTrigger` → 排队一轮 trigger turn | 已接 | C | 无 colony 的会话在 `:505` 直接 continue；错过 tick 只盖章不补跑 |
| Trigger webhook | `internal/server/trigger_runtime.go:589-629` | webhook 事件 → `handleTriggerWebhook` → `queenNode` → `InjectTrigger` | 已接 | C | 同样要求 `ColonyID` |
| Trigger 手动 run | `internal/server/routes_session_triggers.go:351-394` | `POST .../triggers/{id}/run` → 强制 `InjectTrigger`；无 colony 409、无 queen node 409 | 已接 | C | 只有 colony-bound 会话可用 |
| Trigger 重启恢复 `triggers.json` | `internal/server/trigger_runtime.go:220`；`internal/triggers/triggers_store.go:20` | `serve` 的 `--colony` 预载会话、`load_colony`、创建会话后都调用 `RestoreSessionTriggers`；enabled 的定义重建 timer/webhook | 已接 | C | 只覆盖启动时重建的会话；普通 DM 会话不在 boot 时重建，其 trigger 不会自动恢复 |
| Sentinel 管理器（分类 / 监听 / 升级 / 恢复） | `internal/server/sentinel_runtime.go:41/107/165`；`internal/sentinel/manager.go:174/230/709`；`internal/sentinel/escalation_source.go:118` | `serve` 启动 manager + `Start`（drain + listeners）→ 每个 chat runtime 注册 `EscalationSource`（colony 通知配置门控）→ 停车分类 continue/nudge/done/escalate；live 回复 `InjectEvent` 回 Queen | 已接 | C | 只在 `notifications.sentinel_enabled` 打开时运行；dead session 不能冷恢复；分类质量未 live 验证 |
| Sentinel 通知配置 / 测试路由 | `internal/server/routes_sentinel.go:104-122`；`internal/server/adapters_sentinel.go:24/61` | `GET/PUT /api/colony/{id}/notifications`、`POST .../test`、credential validate | 已接 | C | 现在不只是配置：manager 会读同一份配置决定是否启用 autopilot 与 listener |

**小结**：自治层从"实际为零"变成"主路径全通"。Trigger 的 timer/webhook/手动 run 都能把 Queen 叫醒，`triggers.json` 在 colony 重建时恢复；Sentinel 由 `serve` 装配、按 colony 配置门控，能分类停车并升级。剩下的问题是冷恢复（进程退出后的会话）与分类质量验证，而不是接线。

---

### 第 7 层：沉淀（Memory / Soul）

概念主张：Memory 是跨会话的持久知识（reflection 写入 + recall 召回）；Soul 是 Queen 从实际行为中演化出的身份（reflection 提议、用户审批、进入 persona）。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 跨会话记忆：reflection 写入 global / per-queen | `internal/server/chat_runtime.go:221-231/607`；`internal/memory/reflection.go:1026` | `NewChatRuntime` 构造 `ReflectionScheduler`；每个完成 turn `OnTurnDone`，每 6 个 turn 跑 short/long reflection 写 memory markdown | 已接 | C | 只有 chat Queen；不是每轮（默认 `EveryTurns=6`，可由 `memory_reflection.every_turns` 覆盖）；worker/colony 无 reflection |
| 跨会话记忆：recall 召回 | `internal/server/chat_runtime.go:784`；`internal/memory/recall.go:174` | 每条 client 用户消息后、`ExecuteTurn` 前 `BuildScopedRecallBlocks` → `<system-reminder>` 注入 | 已接 | C | 内容不变时用 `lastRecall` 去重；只有 chat Queen |
| 过往对话召回 / `search_messages` | `internal/memory/search.go:534`；`internal/memory/search_timeline.go:63`；`internal/tools/tier_defaults.go:69-70` | 无 | 未接 | — | `SearchMessages` / `SearchTimeline` 零生产调用；会话工具表不含 `search_messages` / `search_timeline` |
| Memory timeline | `internal/memory/timeline.go:616/491/528/645`；`internal/server/config_features.go:74-79` | flag 只在 `/api/config` 展示 | 未接 | — | timeline 的提取、回填、订阅函数零生产调用 |
| Soul HTTP 读 / 改 / 决定 / 回滚 | `internal/server/routes_credentials_soul.go:35-46`；`internal/server/app.go:152` | `GET/PUT /api/queen/{id}/soul`、proposal decide、rollback | 已接 | C | `POST .../soul/reflect` 仍因 `deps.Reflect == nil` 返回 503（`routes_credentials_soul.go:420`） |
| Soul reflection 自动写 | `internal/server/chat_runtime.go:229`（`OnReflect`）、`:1207`（`sedimentSoul`）；`internal/soul/sediment.go` | reflection 完成 → `OnReflect` 回调 → `soul.Sediment` 写 queen soul 新版本；`GET soul` 显示最近时间与条数 | 已接 | C | 只在配置了 soul 的 queen 上生效；不是每轮；HTTP reflect 仍 503 |
| Soul 进入 persona / prompt | `internal/queen/identity.go:85`；`internal/server/chat_runtime.go:1030` | 只读 queen profile，不读 soul | 未接 | C | soul 内容不影响模型行为；"who have we become" 没有进入上下文 |

**小结**：Memory 的 chat Queen 路径完整，Soul 也从"只有人工编辑"补上了 reflection 自动沉淀。但 soul 仍不进 persona，过往对话搜索与 timeline 未接；沉淀层是"记忆已接、身份会写不会用"。

---

### 第 8 层：能力边界（Tools / MCP / Credentials / Budgets）

概念主张：工具目录、MCP、凭据和预算共同定义 agent 能做什么、不能做什么；能力边界不是"有多少工具"，而是"哪些工具在什么角色下真实可用、受什么限制"。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Chat 会话工具目录 | `internal/cli/session_tools.go:58-153`；`internal/server/chat_runtime.go:879/1019` | `newSessionToolCatalogue` → builtin + integration installer + senders + task + `run_worker` + `run_playbook` + tracker + skill 工具 + `ask_user` | 已接 | L | 工具数量大；名称冲突保留 builtin；扫描器默认关闭 |
| Worker 工具面 | `internal/colony/runtime.go:825/856`；`internal/colony/worker_tool_selection.go:28`；`internal/cli/colony_worker_definition.go:92` | worker = builtin base + `worker.json.tools` 从 integration/MCP 池展开 + tracker + `report_to_parent` + `skill_view`/`list_skills` | 已接 | L | 不是严格 Queen 子集；空声明只有 builtin；未声明 integration 不下发 |
| Colony overseer 工具面 | `internal/colony/overseer_tools.go:112`；`internal/colony/runtime.go:825` | `worker.Persistent` → `overseerToolDefs` = colony tools + queen tracker + `run_worker` + 4 个 task 工具 | 已接 | C | 没有 `skill_view`/`write_skill`/`run_playbook`；没有输入源 |
| MCP 服务器配置管理 | `internal/server/routes_mcp.go:61-92`；`internal/server/app.go:157` | `GET/POST/DELETE /api/mcp/servers*`、enable/disable/health/mode/reload → `mcpregistry.NewAtHome` | 已接 | C | 只管理配置文件与健康检查 |
| MCP 客户端 / 工具装载 | `internal/cli/mcp_loader.go:66/102/131`；`internal/tools/mcp_bootstrap.go:66`；`internal/tools/registry.go:150`；`internal/tools/mcp_registry.go:224` | `serve` 启动 `startMCPRuntime` → `Reload`（读配置、连 pool）→ `SetProcessMCP`；每个 `NewToolRegistry` 自动 `attachProcessMCP` → `LoadRegistryServers` 命名空间注册 | 已接 | C | 连接失败的 server 退化为 warning；`/api/mcp/reload` 重连后新 registry 才 attach |
| MCP 工具白名单 | `internal/server/routes_colony_tools.go:516-533`；`internal/colony/runtime.go:304/323` | `PUT /api/colonies/{id}/tools` 写配置并对 live runtime `SetToolAllowlist(enabled, nil)` | 部分 | C | `newColonyHost` 从不传 `MCPToolNames`，runtime 的 `mcpToolNamesAll` 恒空，`applyToolAllowlist` 没有可 gate 的名字；MCP 工具始终可见，白名单不生效 |
| 凭据存储 + 路由 + 环境同步 | `internal/server/routes_credentials.go:72-79/134`；`internal/cli/loader.go:286`；`internal/credentials/store.go:52` | `hive serve` 启动 `SyncCredentialEnv` → 集成工具从 `os.Getenv` 取值 | 已接 | C | 只覆盖 env 型凭据；`get_account_info` 的 `AccountSource` 没有注入 |
| Agent 凭据工具 | `internal/agentloop/loop.go:359`（仅 synthetic 名单）；`internal/agentloop/synthetic_tools.go` | 无 `credentials` 工具定义/执行器 | 未接 | — | 模型不能主动查询/写入凭据；等待凭据的 park 也没有实现 |
| Loop 预算（迭代 / 工具调用） | `internal/agentloop/loop.go:49-67`；`internal/colony/worker_definition.go:9`；`internal/colony/worker_loop_config.go:46` | chat 默认 50 轮 / 无工具预算；worker 3+1 / 30 / 200 | 已接 | C | Queen 没有 30/50 软检查点；没有会话级花费上限（`max_cost_per_run` 只有 schema 字段） |
| 速率 / 配额限制执行时生效 | `internal/ratelimit/rate_limiter.go:426`；`internal/server/config_meta.go:83` | 只在 `/api/config` 展示 limits | 未接 | — | `SocialRateLimiter.Check` 零生产调用；`get_all_limits` 只是元数据 |
| Worker 工具分层 / 搜索 | `internal/colony/runtime.go:398-465`；`internal/tools/tier_defaults.go:163`；`internal/tools/tiers.go:191/427` | `buildWorkerContext` 建 `ToolTierState`；`GateableNames` 来自 runtime 的 MCP 集合 | 部分 | C | MCP 集合恒空 → 所有工具都被判 eager，分层实际不收紧；没有可执行的 `search_tools` 工具（只存在于名字表与提示文案） |

**小结**：能力边界的"有工具"一面很强：chat Queen 有 builtin + integration + 声明项 + tracker + dispatch；MCP 现在真的在 serve 启动时连接并注册到每个新 catalogue；worker 也拿到了声明工具。但"边界"仍有两处半截：MCP 白名单不生效（runtime 的 gated 集合为空），worker 的 tool tiering 因为没有 `search_tools` 而退化成"全 eager"；credential 仍无 agent 工具，rate limit / 花费上限只展示不执行。

---

## 3. 与 `CONCEPT_WIRING_AUDIT.md` 的关系

两份审计是互补关系，不是重复计数。

| 维度 | `CONCEPT_WIRING_AUDIT.md` | 本审计 |
| --- | --- | --- |
| 组织方式 | 按 7 个概念页（colony / coordination / goals / improvement / queen / the_loop / worker_agent） | 按 8 层栈（执行 / 组织 / 状态 / 信号 / 方法 / 自治 / 沉淀 / 能力边界） |
| 回答的问题 | 每个概念主张的机制是否存在、是否从生产入口可达 | 每一层的 substrate 是否真实承重；缺一层会堵住上面哪层 |
| 判定颗粒 | 80 条行为/机制行，按概念归类 | 70 条 substrate 行，按层归类；同一机制只在其所属层计一次 |
| 计数 | 61 已接 / 9 部分 / 10 未接（基线 `4b6f1cf4`） | 50 已接 / 10 部分 / 10 未接（基线 `4b6f1cf4`） |
| 独有覆盖 | goals_outcome 的 judge/retry、improvement 的 reflexion、the_loop 的流式/压缩/计量等概念细节 | 第 5 层 Skill/Playbook、第 6 层 Trigger/Sentinel、第 7 层 Soul、第 8 层 MCP/Credentials/Budgets，以及层间依赖 |
| 状态差异的原因 | 概念页按机制逐条判定，配置路由与运行时机制可能分开计分 | 本审计按层汇总，配置路由不算层已接；只有配置路由、运行时机制不运行的子系统降为部分或未接 |

使用建议：

- 要看某个概念页（例如 queen、worker_agent、improvement）的逐条细节，读 `CONCEPT_WIRING_AUDIT.md`。
- 要看"为什么某些能力看起来都有、实际仍缺一层"，读本审计的层间依赖结论。
- 两份文档的计数不能相加：70 行是 substrate 视角，80 行是 concept 视角，存在交叉但口径不同。

---

## 4. 重点核查清单（按任务逐项）

本节是摘要；完整路径与行号以上文分层明细表为准。

| 核查项 | 结论 | 依据 / 缺口 |
| --- | --- | --- |
| Plan：`tasks.json` 的创建/更新/读取是否真在 queen 会话可用 | **已接（chat Queen + overseer）** | `internal/cli/task_tools.go:47` + `internal/tasks/session_tools.go:747` + `internal/tasks/store.go:244`；overseer 用 `overseer_tools.go:51` 写在 `<colony>/_misc/overseer/tasks.json`。worker 没有 task 工具。 |
| Tracker：写路径、binding、queen/worker/overseer 工具 | **已接（live）** | `colony/tracker.go:61/137` + `binding.go:67` + `tracker/tools.go:1030` + `session_tools.go:118-137` + `runtime.go:841` + `overseer_tools.go:123`。 |
| Event bus：worker 报告 → queen | **已接（live）** | `colony/runtime.go:1118/1200` → `queen_tools.go:492/426` → `chat_runtime.go:315/942`；报告作为非 client user turn 注入，带 duration/output_file。 |
| Event bus：SSE → UI | **部分** | 会话 transcript SSE 已接（`chat_routes.go:168` + `chat_runtime.go:629-709`）；colony host 的 bus 与 session bus 分离，仍无 `/api/colonies/{id}/events`。 |
| Reminder hub：source 注册与触发点 | **已接（chat Queen）** | `chat_reminders.go:54-80` 注册 6 个 source + Sentinel source；`chat_runtime.go:537/544/602` + `loop.go:281/324/340/496/740/813` 覆盖主要点。缺口：`STREAM_STALLED` 未注册、无 tracker 快照、skills catalog 恒空、worker 无 Hub。 |
| Skill：`write_skill` / `skill_view` 是否注册；写入是否生产可达 | **已接** | `skills/agent_tools.go:54/178` + `cli/session_tools.go:94/250` + `colony/runtime.go:404`；write/view/list 全通，worker 侧 `SkillDirs` 生效。 |
| Playbook：`internal/playbook/runner.go` 是否有生产调用或 `run_playbook` 工具注册 | **已接（Go native）** | `colony/playbook_tools.go:129` + `cli/session_tools.go:104/254` + `playbook.NewPlaybookRun`；Python `.play.py` 明确 unsupported，0 派发。 |
| Trigger：`StartTriggerTimer` 依赖的 `QueenExecutor` 是否有赋值；重启后 `triggers.json` 是否恢复 | **已接** | `loader.go:606` + `loader_preloaded_session.go:122` 赋值；`chat_runtime.go:351` 实现 `InjectTrigger`；`trigger_runtime.go:220` 恢复。普通 DM 会话不在 boot 重建。 |
| Sentinel：`sentinel.NewManager` 是否有生产调用 | **已接** | `loader.go:300-304` + `sentinel_runtime.go:41`；manager.Start 打开 drain 与 listeners，chat runtime 注册 escalation source。 |
| Memory：recall / reflection 是否每轮生效 | **部分** | recall 每条 client 消息后都注入（`chat_runtime.go:784`）；reflection 不是每轮，默认每 6 个完成 turn（`memory/reflection.go:1026` + `DefaultReflectionConfig`）；只有 chat Queen。 |
| Soul：除 HTTP 读写路由外，reflection 是否会写 soul | **已接** | `chat_runtime.go:229/1207` + `soul/sediment.go`；HTTP `soul/reflect` 仍 503（`routes_credentials_soul.go:420`）；soul 不进 persona。 |
| Tools / MCP / credentials / budgets | **部分** | chat/worker/overseer 工具面已接；MCP 真的装载；但 MCP 白名单不生效、无 agent `credentials` 工具、rate limit 与花费上限不执行、worker tiering 退化。 |

---

## 5. 最关键的 5 个缺口

### 5.1 执行层仍缺"并行只读工具"与"stall/doom-loop"（第 1 层）

`ConcurrencySafe` 仍未进入 loop 的工具批执行（`loop.go:660-770` 顺序跑），长批次的只读工具调用只能串行。`SubscribeStallCheck`（`stall_check.go:79`）仍零调用，`ParkReasonDoomLoop` 从不产生，重复调用指纹检测不存在。**这两项直接限制"一次 turn 能做多少事"和"跑偏时谁来踩刹车"。**

### 5.2 Colony 的 overseer 与 chat Queen 仍是两个 lead（第 2 层）

overseer 现在有 `run_worker`、queen tracker、task 工具、持久 loop 与 transcript 恢复（`runtime.go:694/1061`、`overseer_tools.go:112`），但它没有任何输入源：没有 HTTP/总线把用户消息或 worker 报告投递给它；`InjectInput` 只用于 worker 软超时与 escalation queue-full。客户端对话的是另一个 chat Queen，两者不共享对话、计划或视图。**组织层完成的是"可派的 chat Queen"，不是"colony 自己的 lead"。**

### 5.3 信号层只桥了一半（第 4 层）

chat transcript 的 SSE 与 worker 报告回 Queen 是通的；但 colony host 的 bus 没有桥到 session/UI（无 `/api/colonies/{id}/events`），worker/overseer 没有 reminder hub，REST reminder surface 仍读一套不再被喂的 `ReminderRuntime`。提醒内容也缺 tracker 快照与 skills catalog 数据。**结果是 UI 与 worker 侧都看不到实时层的一部分，长会话的提醒只在 chat Queen 一侧生效。**

### 5.4 能力边界有两处"装了但不收紧"（第 8 层）

MCP 工具在 serve 启动时真的连上并注册到每个 catalogue，但 colony 的 MCP 白名单不生效：`newColonyHost` 不传 `MCPToolNames`，runtime 的 gated 集合恒空，`applyToolAllowlist` 无可过滤项；`PUT /api/colonies/{id}/tools` 只写配置。worker 的 tool tiering 同理：`GateableNames` 空 → 所有工具 eager，且没有可执行的 `search_tools`。**"能力边界"目前靠工具是否被注册，而不是靠角色白名单与预算收放。**

### 5.5 沉淀层"会写不会用"（第 7 层）

reflection 已经能把结论自动写进 soul（`chat_runtime.go:229/1207`），但 soul 仍不进 persona（`identity.go:85`/`chat_runtime.go:1030` 只读 profile）；`search_messages` 与 memory timeline 仍零生产调用。Memory 能沉淀知识，Soul 能沉淀身份，但两者都还没有回到"下一轮怎么说话"。

---

## 6. 层间依赖结论

八层不是并列功能清单，而是有承重顺序的栈。按 `runtime_spine.md` 的 remove test，从下往上：

| 层 | 缺了会堵住 | 当前 Go 状态 |
| --- | --- | --- |
| 1. 执行 | 上面 2–8 层全部失去执行体；没有 loop 就没有 agent 的一步 | 基本完整，缺并行只读工具、park 落盘、spillover、stall 检测 |
| 2. 组织 | 状态没有写者、信号没有生产者、方法/自治/沉淀没有作用对象 | chat Queen + `run_worker` + worker 身份/工具/resume 可用；overseer 有工具面但无输入源；`serve --colony` 现在可 chat |
| 3. 状态 | 并行工作失去共享事实；Queen 无法校验/恢复；playbook 没有未完成行可查 | Plan（chat + overseer）与 Tracker（Queen/worker/overseer + REST）已接；worker 没有自己的 task list |
| 4. 信号 | worker 结果不回 Queen；UI 静默；长会话漂移、重复派活 | worker→Queen 已接；colony→UI SSE 未接；worker 无 reminder hub；REST reminders 恒空 |
| 5. 方法 | 一次性工作仍可做，但每个单元重复发现方法；批量只能逐行手工做 | 主路径全通：skill 写读 + Go-native playbook + run log/resume |
| 6. 自治 | colony 只能等人来唤醒；Queen 停车时没人知道 | 主路径全通：trigger timer/webhook/manual + 恢复；Sentinel 分类/升级；剩冷恢复 |
| 7. 沉淀 | 每个会话从零开始；Queen 的人格不演化 | memory 已接（chat Queen），soul 会自动写但不进 persona；search/timeline 未接 |
| 8. 能力边界 | agent 只能用 builtin；外部系统、凭据、限额无法按角色收放 | chat/worker/overseer 工具面已接；MCP 已装载但白名单不生效；无 credential 工具；rate/spend 不执行 |

按"修复收益 / 依赖顺序"排：

1. **第 2 层（overseer 输入源）**：现在只差一条投递路径（HTTP 或总线 → `InjectInput`/`overseer`），就能把"两个 Queen"收敛成一个 lead；否则第 5/6 层的能力都长在外挂 chat Queen 上。
2. **第 4 层（colony→UI SSE + worker reminder hub）**：底层事件与 worker 都已存在，缺的是桥；补上后 UI 才能看到 colony 实时状态，worker 也能收到节奏提醒。
3. **第 1 层（并行只读工具 + stall 检测）**：不依赖其他层，直接提升单 turn 吞吐与跑偏保护。
4. **第 8 层（MCP 白名单 + credential 工具 + rate/spend）**：MCP 装载已通，补 `MCPToolNames` 注入即可让白名单生效；credential/rate 是概念里"边界"的最后两块。
5. **第 7 层（soul 进 persona + search_messages/timeline）**：不是运行硬阻塞，但决定 Queen 是否真的"从行为中演化"。

一句话结论：**Go 版这轮把第 5 层（方法）与第 6 层（自治）从空壳接成了主路径，第 2/3 层也补上了 worker 身份、resume、overseer 工具面与预载 chat；现在它已经是一个"能被定时唤醒、能写/读方法、能批量收敛、能派活并回收报告"的 colony。剩下的不是"有没有"，而是"边界收不收得紧、两个 Queen 合不合一、信号桥不桥得全"。**
