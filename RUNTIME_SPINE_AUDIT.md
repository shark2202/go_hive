# Go 运行时 × Runtime Spine 八层接线审计

- 审计基线：`4daf4222db49e1013e4543c808d67128342f3750`（`docs: R1 更新为 48/10/22 并记录新发现缺陷`，2026-11-19 14:04 CST）
- 更新：worker 身份与声明工具两行已按 `193ace3d`（`al-worker-identity`）修正，其余行仍以基线为准
- 审计日期：2026-11-19
- 审计对象：`docs/key_concepts/runtime_spine.md` 的八层栈；Go 模块根为 `go_hive/go-hive/`
- 分层依据说明：`runtime_spine.md` 在审计基线提交中尚未入库（`git status` 为 `??`），本审计按工作区现有文本核对八层定义，不把该文件计入本次提交。
- 判定口径（严格）：
  - **已接**＝从 `cmd/hive` → `internal/cli` 或 `internal/server` 的 HTTP 路由 / 会话工具 / agent loop 真实可达，且在生产路径上生效；仅测试调用、仅枚举、仅提示文案不算。
  - **部分**＝有真实入口但只完成一半：只读不写、只写配置不生效、只覆盖一个角色/阶段、只在内存不落盘，或一个合并机制里只有一种生效。
  - **未接**＝代码存在但生产入口走不到（死代码 / 仅测试 / 只有类型、枚举或提示文案）。
  - 管理配置路由（MCP、Sentinel 通知、Soul 编辑）本身可达不等于其运行时机制已接；这类行按“配置可用、机制未运行”判为部分或未接。
- 证据口径：
  - `L`＝交付侧既有 live 实测记录，出处 `go_hive/docs/support-matrix.md`（本轮只读审计，未重跑）。
  - `C`＝本轮从 `cmd/hive` 逐段静态追踪；`go build ./...` 通过，但测试本身不计入接线判定。
- 说明：只读审计，不改产品代码、不改测试、不改 `go.mod`。下表路径均相对 Go 模块根 `go_hive/go-hive/`；行号以 `4daf4222` 为准。

## 生产入口与追踪方法

`cmd/hive/main.go:7` → `internal/cli.NewRunner`（`internal/cli/cli.go:112`）→ `Runner.Run`（`:264`）→ `serve` → `defaultServe`（`internal/cli/loader.go:266`）→ `hiveserver.NewApp`（`internal/server/app.go:88`）→ 各路由表。

二级入口只有两个：

1. **chat Queen**：`createSessionWithRuntime`（`internal/cli/loader.go:536`）→ `NewChatRuntime`（`internal/server/chat_runtime.go:164`）→ 每轮 `runTurn`（`:429`）→ `AgentLoop.ExecuteTurn`（`internal/agentloop/loop.go:478`）。
2. **colony runtime**：`newColonyHost`（`internal/cli/loader.go:444`）→ `colonyhost.New`（`internal/host/host.go:68`）→ `ColonyRuntime`；`run_worker` 经 `RunWorkerSpawnerFromContext` 进入 `SpawnBatchWithOptions`（`internal/colony/runtime.go:523`）。

本审计的每一行都从这两个入口之一回溯；没有回到这两个入口的机制判为未接。

---

## 1. 八层汇总

| 层 | 已接 | 部分 | 未接 | 合计 |
| --- | ---: | ---: | ---: | ---: |
| 1. 执行（The Loop） | 5 | 4 | 1 | 10 |
| 2. 组织（Colony / Queen / Worker） | 5 | 2 | 3 | 10 |
| 3. 状态（Plan / Tracker） | 8 | 0 | 2 | 10 |
| 4. 信号（Event Bus / Reminder Hub） | 4 | 1 | 3 | 8 |
| 5. 方法（Skill / Playbook） | 1 | 0 | 6 | 7 |
| 6. 自治（Trigger / Sentinel） | 0 | 2 | 5 | 7 |
| 7. 沉淀（Memory / Soul） | 3 | 0 | 4 | 7 |
| 8. 能力边界（Tools / MCP / Credentials / Budgets） | 4 | 3 | 4 | 11 |
| **合计** | **30** | **12** | **28** | **70** |

读法：越靠下的层（执行、组织、状态）越完整；越靠上的“方法 / 自治 / 沉淀”越空。这与 `runtime_spine.md` 的“底层可跑、上层未长成”一致，但具体断点不在同一处：状态层比概念审计显示的更完整，方法层和自治层则整体缺失。

---

## 2. 分层明细

### 第 1 层：执行（The Loop）

概念主张：一个多轮流式 LLM 会话循环；每轮流式输出、并行跑只读工具、回注结果、决定停止/继续；judge 自纠错；Queen/worker 只是一组 `LoopConfig` 差异；内建崩溃恢复、上下文压缩、计量与提醒。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 单一多轮 agent loop（Queen 与 worker 共用） | `internal/agentloop/loop.go:478`（`ExecuteTurn`）；`internal/server/chat_runtime.go:501`；`internal/colony/runtime.go:864/902` | chat：`NewChatRuntime` → `runTurn` → `loop.ExecuteTurn`；worker：`run_worker` → `spawnBatchOn` → `runGatedWorker` → `runWorker` → 同一 `AgentLoop` | 已接 | C | worker/overseer 手工构造 `&AgentLoop{}`，不走 `NewAgentLoop`，因此没有 `Hub`（见第 4 层） |
| 流式输出逐帧推 UI | `internal/server/chat_stream.go:43`；`internal/server/chat_runtime.go:633/637`；`internal/llm/stream_openai.go:42` | CLI provider 实现 `StreamCompleter` → `newChatStreamingCompleter` → `StreamTextDelta` → `client_output_delta` → session SSE | 已接 | L | Anthropic/MiniMax 明确不支持流式并回退非流式；tool-call 参数仍按完整对象聚合，不逐片下发 |
| 只读工具并行执行 | `internal/agentloop/loop.go:660` | 工具批内 `for i, tc := range toolCalls` 串行执行 | 未接 | — | `ConcurrencySafe` 只在 `internal/colony/tracker.go:99` 写进工具定义，loop 从不读取；没有并发分组 |
| 工具结果回注、循环直到不再调用工具 | `internal/agentloop/loop.go:614`（无 tool calls 返回）、`:756`（`AddToolResult`） | `ChatRuntime.runTurn` / `runWorker` 都走该路径 | 已接 | C | — |
| judge：accept / retry / escalate | `internal/agentloop/judge.go:92/109/127`；`internal/colony/runtime.go:898/950` | colony worker/overseer 构造 loop 后 `ConfigureJudge`；criteria 非空才评估；RETRY 回注 `[Judge feedback]` 用户消息 | 部分 | C | 只有 colony worker 有 criteria；chat Queen 无 criteria 且从不 `ConfigureJudge`；overseer 的 `queenOverseerSpec` 无 criteria，实际不评估 |
| worker grace + 更紧预算 | `internal/colony/worker_definition.go:9`；`internal/colony/worker_loop_config.go:46`；`internal/agentloop/loop.go:529-556` | `run_worker` → `BuildWorkerLoopConfig` 应用 3 轮 + 1 grace / 每轮 30 / 硬停 90 / 终身 200；grace 轮只允许 terminal tools | 已接 | C | grace 白名单含 `task_update`，但 worker 工具表没有 task 工具，该项不可达 |
| 崩溃恢复：transcript cursor + 会话恢复 | `internal/agentloop/conversation.go:1126/1213`；`internal/server/chat_runtime.go:243`；`internal/cli/loader_session_resume.go:69-73` | chat 每条消息 `persist`；`NewChatRuntime` 恢复 parts；`queen_resume_from` 复用旧 id/旧目录 | 部分 | C | worker 转录只在内存（`internal/colony/runtime.go:874` 新建 conversation，无 Store）；colony-bound 会话明确不走 `queen_resume_from` |
| park / resume（`ask_user` 停车） | `internal/server/chat_runtime.go:964/975`；`internal/agentloop/park_reason.go:16` | `ask_user` → `pendingQuestions` + `client_input_requested(park_reason=ask_user)`；下一条用户消息清空 | 部分 | C | 停车状态只在内存（`pendingQuestions` + `parkReason`）；无 park 落盘、无 resume 入口；进程重启即丢 |
| 自动上下文压缩 | `internal/agentloop/loop.go:320`；`internal/agentloop/conversation.go:736/931`；`internal/agentloop/llm_compact.go:22` | 每轮 LLM 调用前 `NeedsCompaction()` → `LlmCompact` → `conv.Compact`；触发 PRE/POST_COMPACT | 部分 | C | 自动压缩已接；超大工具结果 spillover 仍无生产者（`internal/agentloop/conversation.go:203` 只读 `SpilloverPath`） |
| 每次 LLM 调用计量（tokens/cost） | `internal/agentloop/loop.go:595`；`internal/server/chat_runtime.go:641/649` | `LLMTurnComplete` → `chatTurnEmitter` → `llm_turn_complete`（input/output/cached/cost） | 已接 | C | — |

**小结**：执行层骨架完整，流式、工具回注、judge（worker 路径）、grace、压缩、计量都真实运行。缺口集中在“并行只读工具”“park/resume 落盘”“spillover 生产者”，以及 chat Queen 没有 judge/criteria。

---

### 第 2 层：组织（Colony / Queen / Worker）

概念主张：colony 是单目录、可移植、可定时唤醒、跨会话持久的部署单元；Queen 与 worker 是同一个 loop；Queen 通过 `run_worker` 在运行时按需扩展；Create Colony 对话框确认后才 fork。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Colony 单目录 + scaffold | `internal/server/routes_colonies.go:409`（`handleScaffoldColony`）、`:440`（`ensureMinimalColony`）；`internal/hivehome/home.go:88/129` | `POST /api/colonies/{colony_id}/scaffold` → 只写 `metadata.json`、`worker.json` | 部分 | C | scaffold 不建 `skills/`、`triggers.json`、runs；`tracker/` 由 runtime `Start` 首次加载时补建（`internal/colony/tracker.go:61`） |
| Colony 导入 / tar 可移植 | `internal/server/routes_colonies_import.go:705`（`handleImportColony`） | `POST /api/colonies/import*` → 落盘 `home.ColoniesDir()` | 已接 | C | — |
| Colony load / unload | `internal/cli/loader.go:722`（`LoadColony`）、`:741`（`UnloadColony`）、`:759`（`attach`）；`internal/server/routes_session_colony.go:55/170` | `POST/DELETE /api/sessions/{id}/colony` → `deps.Colony` → `newColonyHost` + `startColonyHost` | 已接 | C | unload 后 chat 侧绑定要等下一次 `GetSession` 才解除（`internal/server/session.go:411-420`）；在跑的 turn 仍可能持有旧 runtime |
| `serve --colony` 启动即加载 runtime 与 overseer | `internal/cli/loader.go:293-326`、`:312`、`:317` | `hive serve --colony <path>` → `buildColonyHost` → `Start` + `StartOverseer` → `AddSession` | 部分 | C | 预载 session 的 `Chat` 恒为 nil；`internal/server/chat_routes.go:115` 对 `Chat == nil` 直接 503，不能 chat、不能 `run_worker` |
| Queen chat 会话 + persona | `internal/cli/loader.go:536`；`internal/server/chat_runtime.go:164/937` | `POST /api/sessions` → `createSessionWithRuntime` → `NewChatRuntime` → `IdentityPrompt` | 已接 | C | `default` queen 无 profile 时回退通用身份（预期） |
| Queen 用 `run_worker` 运行时扩展 | `internal/colony/queen_tools.go:75/203`；`internal/cli/session_tools.go:88/227`；`internal/server/chat_runtime.go:807/824` | 会话工具表常驻 `run_worker`；`toolsForTurn` 在未绑 colony 时过滤；绑定后 `SpawnBatchWithOptions` 并启动软/硬超时看门狗 | 已接 | L | 只有带 `ChatRuntime` 的 chat 会话；`serve --colony` 预载会话不可用；`resume_worker_ids` 显式不支持 |
| Worker 是同一 loop + 终态报告 | `internal/colony/runtime.go:864/902/959`；`internal/colony/queen_tools.go:457`；`internal/server/chat_runtime.go:841` | `spawnBatchOn` → `runGatedWorker` → `runWorker` → `publishReport` → `SUBAGENT_REPORT` → `[WORKER_REPORT]` | 已接 | L | 报告缺 `duration_seconds`；overseer 报告被跳过；预载 colony 无 chat runtime 时无人消费 |
| Colony overseer 是 client-facing Queen | `internal/colony/runtime.go:931-956`；`internal/cli/loader.go:464/494` | `StartOverseer` → `runOverseer` → 跑一轮 `ExecuteTurn` 后 `<-ctx.Done()` | 未接 | C | 工具面只有 builtin；无 `run_worker`/tracker/task；不消费 chat 输入；不共享 chat Queen 的对话 |
| Worker 是“聚焦克隆”：worker prompt + 配置的工具子集 | `internal/colony/worker_definition.go:39/113`；`internal/cli/colony_worker_definition.go`；`internal/colony/runtime.go` | fork 写 `worker.json.system_prompt`（`BuildSystemPrompt`）；`runWorker` 用 `systemPromptFor(spec)`；`selectWorkerDeclaredTools` 应用 `worker.json.tools` | 已接 | L | `193ace3d` 修复：不再写 Queen persona，声明工具按分类展开并执行；仍缺 `loop_config`/`identity_prompt`/skills 字段 |
| Worker 崩溃恢复 / `resume_worker_ids` 续跑 | `internal/colony/queen_tools.go:224`；`internal/colony/runtime.go:874`；`internal/colony/resume.go:11-69` | `resume_worker_ids` 直接返回 unsupported | 未接 | — | worker 转录不落盘；`resume.go` 三函数零生产调用 |

**小结**：colony 从“能加载、能起一个空转 Queen”变成“chat Queen 能在绑定的 colony 里 fan-out”。`run_worker`、worker 报告、worker 身份与声明工具都真正接通（`193ace3d`）。但 colony 自己的 overseer 仍是影子 Queen；`serve --colony` 预载会话没有 chat runtime。组织层完成的是“可派的 chat Queen”，不是“colony 自己的 Queen”。

---

### 第 3 层：状态（Plan / Tracker）

概念主张：Plan 是 Queen 的意图（`tasks.json`，Action Plan 面板）；Tracker 是 colony 的事实（`tracker/tracker.db`，每个工作单元一行）。两者不能混。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Plan 文件存储：`tasks.json` 创建 / 更新 / 读取 | `internal/tasks/store.go:159/244`；`internal/tasks/session_tools.go:747`；`internal/cli/task_tools.go:47` | 会话创建 → `newSessionToolCatalogue` → `newTaskToolSet(sessionDir)` → `task_create/task_update/task_list/task_get` → `TaskStore` 写 `tasks.json` | 已接 | C | 只有 chat Queen 有；worker/overseer 工具表都没有 task 工具 |
| Plan REST 快照 / 归档清理 | `internal/server/routes_tasks.go:37`；`internal/server/app.go:166` | `GET /api/sessions/{id}/tasks`、`POST .../tasks/unarchive`、`POST .../tasks/clear-completed` | 已接 | C | 只有读快照和两个归档动作；没有 REST 创建/编辑（由 agent 工具写） |
| Plan 跨 reload 可见 | `internal/tasks/store.go:106/174`；`internal/cli/task_tools.go:60` | 同一 session 目录重建工具集，读同一 `tasks.json`；`newTaskToolSet` 先 `MkdirAll(root)` 让 canonical 目录命中 | 已接 | C | colony-bound 会话不走 `queen_resume_from`；session 目录迁移时 `sessionDirCache` 可能需重启刷新 |
| Worker 自己的 task list | `internal/colony/runtime.go:754`；`internal/tasks/register.go:5` | worker 工具 = builtin + tracker + `report_to_parent` | 未接 | — | worker 没有 task 工具；`RegisterTaskTools` 零生产调用 |
| Colony overseer 的 task list | `internal/colony/runtime.go:754/931` | overseer 的 stream 不是 `worker:` 前缀，不追加 tracker/report | 未接 | — | 无 task、无 tracker、无 `run_worker` |
| Tracker 建库 + binding | `internal/colony/tracker.go:61`（`provisionTracker`）；`internal/tracker/binding.go:67`（`BindingForColonyDir`）；`internal/colony/runtime.go:449/523` | colony `Start` / 每次 spawn → `provisionTracker` → `BindingForColonyDir` → `tracker/tracker.db` | 已接 | L | 只对含 `metadata.json` 的目录建库；没有 metadata 的旧目录不建 |
| Tracker binding 注入 worker | `internal/colony/tracker.go:147`（`workerInputData`）；`internal/colony/runtime.go:363-371`；`internal/colony/worker.go:377` | spawn → `workerInputData` 带 binding → `BuildWorker` 设 `ColonyBindingProvider` | 已接 | L | `RuntimeOptions.Binding` 没有生产注入者（只有测试/宿主自备） |
| Queen tracker 工具（SQL / 声明可写列 / upsert / query） | `internal/tracker/tools.go:1030`；`internal/cli/session_tools.go:95-115/234-247` | 会话工具表常驻 4 个 tracker 工具；执行时 `TrackerBindingFromContext` 取绑定，无绑定返回 `ErrNoBinding` | 已接 | L | 未绑 colony 时工具仍可见但不可用；plain Queen 拿不到 colony tracker |
| Worker tracker 工具（upsert / query） | `internal/colony/tracker.go:108/126`；`internal/colony/runtime.go:761` | worker 工具表追加 role=worker 表；`executorFor` 用 `workerTrackerRegistry` | 已接 | L | 只有 `tracker_upsert/tracker_query`；无 SQL；`RuntimeOptions.Binding` 同样无生产注入者 |
| Tracker REST 读表 / 读行 / 改行 | `internal/server/routes_colony_workers_data.go:912/918/921` | `GET /api/colonies/{id}/data/tables`、`.../tables/{table}/rows`、`PATCH .../rows` | 已接 | C | 这三个路由无 insert/delete（全局数据路由另有） |

**小结**：状态层是八层里完成度最高的一层。Plan 对 chat Queen 真实可用（4 个工具 + REST 快照 + 落盘），Tracker 的建库、binding、Queen/worker 双侧工具、REST 读写全部接通。缺口是“状态只属于 chat Queen”：worker 没有自己的 task list，overseer 连 tracker/task 都没有。

---

### 第 4 层：信号（Event Bus / Reminder Hub）

概念主张：Event bus 传瞬时信号（worker 报告、实时 transcript、SSE）；Reminder hub 在已知生命周期点注入短提示；二者都不是 tracker，也不是 memory。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 会话实时 transcript → SSE → UI | `internal/server/chat_runtime.go:591-700`；`internal/server/chat_routes.go:168`；`internal/eventbus/bus.go:154/415` | 每轮 emit execution / loop / assistant / tool / usage → session `EventBus` → `GET /api/sessions/{id}/events`；同时写 `events.jsonl` | 已接 | L | — |
| Worker 终态报告 → Queen 的 `[WORKER_REPORT]` | `internal/colony/runtime.go:959/1027`；`internal/colony/queen_tools.go:457/393`；`internal/server/chat_runtime.go:299/860` | 终态 `SUBAGENT_REPORT` → `FormatWorkerReport` → `InjectWorkerReport` → 下一轮以非 client user message 注入 | 已接 | L | payload 没有 `duration_seconds`（`internal/colony/worker.go:388-420`）；没有 `output_file`；persistent overseer 的报告被跳过；预载 colony 无 chat 时无人消费 |
| Colony 事件 → 会话 / UI SSE | `internal/host/host.go:78-81`；`internal/cli/loader.go:458-472`；`internal/server/chat_routes.go:36` | colony host 自建 bus；session bus 独立；没有 `/api/colonies/{id}/events` 路由 | 未接 | C | worker 进度只能轮询 `GET /api/sessions/{id}/workers`；colony bus 的事件不进 UI，也不进 session `events.jsonl` |
| Reminder Hub：6 个 source 注册 + `Hub.Start` | `internal/server/chat_reminders.go:54-80`；`internal/agentloop/reminder_hub.go:235` | `NewChatRuntime` → register task / colony_parallel / active_workers / searchable_tools / skills_catalog / idle → `Bind` → `Hub.Start`（idle ticker） | 已接 | L | 只有 chat Queen；worker/overseer 的 loop 没有 Hub |
| Reminder 触发点 | `internal/server/chat_runtime.go:475/479/530`；`internal/agentloop/loop.go:281/324/340/496/740/813` | SESSION_START / USER_PROMPT_SUBMIT / POST_TOOL_USE / PRE_COMPACT / POST_COMPACT / STOP / IDLE_TICK / TOOL_BUDGET_CHECKPOINT | 已接 | C | `STREAM_STALLED` 没有 source 注册；chat 侧 `SESSION_START` 可能在 runtime 和 loop 各触发一次（重复注入风险） |
| Reminder 内容供给（fleet / task / tools / skills / tracker） | `internal/server/chat_reminders.go:26-49`；`internal/tasks/reminders.go:444`；`internal/agentloop/active_workers_reminder.go:27` | 注册 task、colony_parallel、active_workers、searchable_tools、skills_catalog、idle；provider 从 live session 读 | 部分 | C | 没有 tracker 快照 source；`skills_catalog` provider 恒空（loader 未传 `SkillsCatalog`）；chat 没有 `SearchableToolsProvider`，该 source 在 chat 恒静默 |
| Worker 侧提醒 | `internal/colony/runtime.go:894`（手工 `AgentLoop{}`，Hub 为 nil） | `runWorker` 不走 `NewAgentLoop` | 未接 | — | worker 没有 reminder hub；grace/budget 提示是 loop 内硬编码文案，不是 hub source |
| REST reminder surface 与 chat hub 一致 | `internal/server/routes_reminders.go:302`；`internal/server/routes_reminders.go:124` | `GET/DELETE /api/sessions/{id}/reminders` 读独立 `ReminderRuntime`；chat 不再调用 `TurnComplete` | 未接 | C | REST surface 恒空，与 chat 的 hub 是两套状态；`serveReminders` 只在 `internal/cli/loader.go:278/329` 注入，没有生产者 |

**小结**：信号层的主路径（chat transcript SSE、worker 报告回 Queen）是通的，且有 live 记录。但 colony 自己的事件总线没有桥到会话/UI；worker 没有 reminder hub；REST reminder surface 与 chat hub 脱节；提醒内容缺 tracker 快照和 skills catalog 数据。

---

### 第 5 层：方法（Skill / Playbook）

概念主张：Skill 是“怎么做”的可复用方法（Queen 写、worker 按需加载）；Playbook 是“怎么批量做”的确定性 runner（查询 tracker 未完成行、并发派发、重试/退避/死信、按 tracker 现状 resume）。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Skill 解析 / 安装 / 校验库 + `hive skill` CLI | `internal/skills/parser.go:51`、`internal/skills/installer.go:125/196`；`internal/cli/skill.go:72`；`internal/cli/cli.go:308` | `hive skill ...` → `Runner.skillCommands()` → 文件系统 | 已接 | C | 只有 operator 面；安装/校验后的技能不进任何 agent 上下文 |
| Agent 写技能 `write_skill` | `internal/skills/skill_writer.go:169`（`WriteSkill`） | 无工具定义、无执行器、无路由 | 未接 | — | `WriteSkill` 零生产调用；只有提示文案 `internal/agentloop/colony_parallel_nudge.go:39` 提到 `write_skill`，工具本身不存在 |
| Agent 读技能 `skill_view` / 技能目录加载 | `internal/agentloop/types.go:144`（`SkillDirs`）；`internal/agentloop/loop.go:828`；`internal/colony/worker_surface.go:246` | 无 | 未接 | — | `SkillDirs` 从不赋值，`loop.go:828` 的拦截永不触发；`GET /api/sessions/{id}/colony/skills` 恒空（`routes_colony_workers.go:571` + `SkillsProvider` nil） |
| Colony `skills/` 目录生产写入 | `internal/cli/loader.go:795/939`；`internal/server/routes_colonies.go:440` | scaffold / fork 只写 `metadata.json`、`worker.json` | 未接 | — | 不建 `skills/`；`newColonyHost` 不设 `SkillsProvider`（`loader.go:458-472`） |
| Playbook runner（确定性批次、重试/退避/死信） | `internal/playbook/runner.go:190/423`；`internal/playbook/rows.go:103/129` | 无 | 未接 | — | `internal/playbook` 在 `internal/`、`cmd/` 零 import；runner 只有测试 |
| Agent Playbook 工具（`run_playbook` / status / stop） | `internal/agentloop/colony_parallel_nudge.go:28/39`；`internal/agentloop/active_workers_reminder.go:70` | 只有提示文案 | 未接 | — | 没有工具定义、执行器或路由；模型被提示可以 `run_playbook`，实际无法调用 |
| Playbook run log / 按 tracker resume | `internal/colony/colony_db.go:78/107`；`internal/playbook/runner.go:284` | 无 | 未接 | — | `RecordPlaybookRun` / `ListPlaybookRuns` 零生产调用；`TrackerQuery` 无入口；`playbook_runs` 表只为未来预留 |

**小结**：方法层是八层里最空的。Skill 只有 operator CLI，agent 既不能写也不能读；Playbook runner 完整但生产零 import，`run_playbook` 只活在提示文案里。底层已经有 worker + tracker（本可支撑 playbook），但“把成功固化成方法”这一层完全没有接上。

---

### 第 6 层：自治（Trigger / Sentinel）

概念主张：Trigger 从外部唤醒 colony（cron / interval / webhook）；Sentinel 在 Queen 停车时决定 nudge、标记完成或升级到人。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Trigger CRUD + activate/deactivate | `internal/server/routes_session_triggers.go:88/245/437`；`internal/server/routes_sessions.go:96-111` | `POST/PATCH/GET /api/sessions/{id}/triggers*` → `PutTrigger` + `PersistActiveTriggers` + `saveTriggerToAgent` | 部分 | C | 只写配置；`StartTriggerTimer` 启动的 timer 在 fire 时还要求 `ColonyID` 和 Queen node |
| Trigger timer 真实唤醒 | `internal/server/trigger_runtime.go:291/330`；`:383-386` | `StartTriggerTimer` → `runTimerLoop`；`queenNode(session)` 为 nil 时 `continue` | 未接 | C | `QueenExecutor` 生产零赋值（`internal/server/session.go:78` 只有字段和读取）；`ChatRuntime` 不实现 `InjectTrigger`；timer 永不注入 |
| Trigger webhook | `internal/server/trigger_runtime.go:424-500` | webhook 事件 → `handleTriggerWebhook` → `queenNode` 为 nil 直接 return | 未接 | C | webhook server 可以启动并收事件，但不会投递给 Queen |
| Trigger 手动 run | `internal/server/routes_session_triggers.go:351` | `POST .../triggers/{id}/run` → `queenNode` nil → 409 `Queen not ready` | 未接 | C | 生产路径恒定 409；`EmitTriggerFired` 不可达 |
| Trigger 重启恢复 `triggers.json` | `internal/server/trigger_runtime.go:177`（只写）；`internal/triggers/triggers_store.go:20`（只读给保存） | 启动不扫描 `triggers.json`，不重建 timer/webhook | 未接 | — | 只有显式 activate 时启动；进程重启后已激活 trigger 不再运行 |
| Sentinel 管理器（分类 / 监听 / 升级 / 恢复） | `internal/sentinel/manager.go:99`；`internal/sentinel/classifier.go:257`；`internal/sentinel/listeners.go:71/243/425`；`internal/sentinel/escalation_source.go:91` | 无 | 未接 | — | `sentinel.NewManager` 零生产调用；`EscalationSource` 未注册到 reminder hub；没有“parked Queen 被 nudge/升级”的链路 |
| Sentinel 通知配置 / 测试路由 | `internal/server/routes_sentinel.go:104-122`；`internal/server/adapters_sentinel.go:24/61` | `GET/PUT /api/colony/{id}/notifications`、`POST .../test`、credential validate | 部分 | C | 只读写配置、发测试消息；没有运行中的 Sentinel 消费 park 状态 |

**小结**：自治层实际上为零。Trigger 路由能写配置，但 `QueenExecutor` 恒 nil、`ChatRuntime` 没有 `InjectTrigger`、启动不恢复 `triggers.json`，所以 timer/webhook/手动 run 都不能唤醒 Queen；Sentinel 只有配置和通知适配器，`NewManager` 从不运行。colony 仍然只能靠人在 chat 里驱动。

---

### 第 7 层：沉淀（Memory / Soul）

概念主张：Memory 是跨会话的持久知识（reflection 写入 + recall 召回）；Soul 是 Queen 从实际行为中演化出的身份（reflection 提议、用户审批、进入 persona）。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 跨会话记忆：reflection 写入 global / per-queen | `internal/server/chat_runtime.go:211-219/535`；`internal/memory/reflection.go:919/1030` | `NewChatRuntime` 构造 `ReflectionScheduler`；每个完成 turn `OnTurnDone`，每 6 个 turn 跑 short/long reflection 写 memory markdown | 已接 | C | 只有 chat Queen；不是每轮（默认 `EveryTurns=6`，可用 `memory_reflection.every_turns` 覆盖）；worker/colony 无 reflection |
| 跨会话记忆：recall 召回 | `internal/server/chat_runtime.go:712`；`internal/memory/recall.go:174` | 每条 client 用户消息后、`ExecuteTurn` 前 `BuildScopedRecallBlocks` → `<system-reminder>` 注入 | 已接 | C | 内容不变时用 `lastRecall` 去重；只有 chat Queen |
| 过往对话召回 / `search_messages` | `internal/memory/search.go:534`；`internal/memory/search_timeline.go:63`；`internal/tools/tier_defaults.go:69-70` | 无 | 未接 | — | `SearchMessages` / `SearchTimeline` 零生产调用；会话工具表不含 `search_messages` / `search_timeline` |
| Memory timeline | `internal/memory/timeline.go:616/491/528/645`；`internal/server/config_features.go:74-79` | flag 只在 `/api/config` 展示 | 未接 | — | timeline 的提取、回填、订阅函数零生产调用 |
| Soul HTTP 读 / 改 / 决定 / 回滚 | `internal/server/routes_credentials_soul.go:35-46`；`internal/server/app.go:152` | `GET/PUT /api/queen/{id}/soul`、proposal decide、rollback | 已接 | C | 只有手动编辑/审批；不是自动沉淀 |
| Soul reflection 自动写 | `internal/server/routes_credentials_soul.go:391/409`；`internal/server/app.go:152` | `POST .../soul/reflect` 需要 `deps.Reflect`；App 只传 `Home` | 未接 | C | `Reflect` 恒 nil → 503；`internal/memory/reflection.go` 不写 soul |
| Soul 进入 persona / prompt | `internal/queen/identity.go:85`；`internal/server/chat_runtime.go:937` | 只读 queen profile，不读 soul | 未接 | C | soul 内容不影响模型行为；“who have we become” 没有进入上下文 |

**小结**：Memory 的 chat Queen 路径完整：reflection 每 6 个 turn 写 memory，recall 每轮注入；但 worker/colony 不沉淀，过往对话搜索和 timeline 未接。Soul 只有“存储 + 人工编辑/审批”，reflection 入口恒 503，soul 也从不进入 persona。沉淀层是“记忆已接、身份未长成”。

---

### 第 8 层：能力边界（Tools / MCP / Credentials / Budgets）

概念主张：工具目录、MCP、凭据和预算共同定义 agent 能做什么、不能做什么；能力边界不是“有多少工具”，而是“哪些工具在什么角色下真实可用、受什么限制”。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Chat 会话工具目录 | `internal/cli/session_tools.go:54-130`；`internal/server/chat_runtime.go:785/918` | `newSessionToolCatalogue` → builtin + 23 个 integration installer + senders + task + `run_worker` + tracker + `ask_user` | 已接 | L | 工具数量大；名称冲突保留 builtin；扫描器默认关闭 |
| Worker 工具面 | `internal/colony/runtime.go`；`internal/cli/colony_worker_definition.go`；`internal/colony/worker_definition.go:9` | `workerTools` = builtin + tracker + `report_to_parent` + `worker.json.tools` 展开项 | 已接 | L | `193ace3d` 后声明工具生效；仍无 MCP 工具，未声明的 integration 不下发 |
| Colony overseer 工具面 | `internal/colony/runtime.go:754/931`；`internal/cli/loader.go:464` | overseer 走 `workerTools`，stream 非 `worker:` 前缀 | 未接 | C | 无 `run_worker`/tracker/task；只有 builtin，仍是影子 Queen |
| MCP 服务器配置管理 | `internal/server/routes_mcp.go:61-90`；`internal/server/app.go:157` | `GET/POST/DELETE /api/mcp/servers*` → `mcpregistry.NewAtHome` | 已接 | C | 只管理配置文件、健康检查；不会把 MCP 工具装进任何会话 |
| MCP 客户端 / 工具装载 | `internal/tools/mcp_registry.go:138`（`LoadMCPConfig`）；`internal/tools/mcp_client.go:65`（`NewMCPClient`）；`internal/tools/registry.go:128` | 无 | 未接 | — | 生产零调用 `LoadMCPConfig` / `NewMCPClient`；`mcpManager` 只在测试赋值；`sessionToolCatalogue` 从不配置 MCP |
| MCP 工具白名单 | `internal/colony/runtime.go:285/302` | `SetToolAllowlist` / `applyToolAllowlist` 存在 | 部分 | C | 没有生产调用者设置 `enabledMCPTools`；没有 MCP 工具可过滤 |
| 凭据存储 + 路由 + 环境同步 | `internal/server/routes_credentials.go:72-79/134`；`internal/cli/loader.go:287`；`internal/credentials/store.go:52` | `hive serve` 启动 `SyncCredentialEnv` → 集成工具从 `os.Getenv` 取值 | 已接 | C | 只覆盖 env 型凭据；`get_account_info` 的 `AccountSource` 没有注入 |
| Agent 凭据工具 | `internal/agentloop/loop.go:359`（仅 synthetic 名单）；`internal/agentloop/synthetic_tools.go` | 无 `credentials` 工具定义/执行器 | 未接 | — | 模型不能主动查询/写入凭据；等待凭据的 park 也没有实现 |
| Loop 预算（迭代 / 工具调用） | `internal/agentloop/loop.go:49-67`；`internal/colony/worker_definition.go:9`；`internal/colony/worker_loop_config.go:46` | chat 默认 50 轮 / 无工具预算；worker 3+1 / 30 / 200 | 已接 | C | Queen 没有 30/50 软检查点；没有会话级花费上限 |
| 速率 / 配额限制执行时生效 | `internal/ratelimit/rate_limiter.go:426`；`internal/server/config_meta.go:83` | 只在 `/api/config` 展示 limits | 未接 | — | `SocialRateLimiter.Check` 零生产调用；`get_all_limits` 只是元数据 |
| Worker 工具分层 / 搜索 | `internal/colony/runtime.go:396-421`；`internal/tools/tier_defaults.go:163`；`internal/tools/tiers.go:427` | `buildWorkerContext` 建 `ToolTierState`，`workerKeepSet` 有默认类别 | 部分 | C | spawn 工具池只有 builtin，池里没有 `search_tools`；`preload_tools` 只能预加载 builtin 名称；无 integration/MCP |

**小结**：能力边界的“有工具”一面很强：chat Queen 默认有 96 个 integration 工具（扫描器/senders 打开时 103 个）、read/write/terminal、task、tracker、`run_worker`；凭据能通过 env 到达工具；loop 预算对 worker 生效。但“边界”没有真正建立：worker/overseer 只有 builtin，MCP 只有配置管理没有客户端，credential 没有 agent 工具，rate limit 只展示不执行，worker 的 tool tiering 没有可搜索的工具池。

---

## 3. 与 `CONCEPT_WIRING_AUDIT.md` 的关系

两份审计是互补关系，不是重复计数。

| 维度 | `CONCEPT_WIRING_AUDIT.md` | 本审计 |
| --- | --- | --- |
| 组织方式 | 按 7 个概念页（colony / coordination / goals / improvement / queen / the_loop / worker_agent） | 按 `runtime_spine.md` 的 8 层栈（执行 / 组织 / 状态 / 信号 / 方法 / 自治 / 沉淀 / 能力边界） |
| 回答的问题 | 每个概念主张的机制是否存在、是否从生产入口可达 | 每一层的 substrate 是否真实承重；缺一层会堵住上面哪层 |
| 判定颗粒 | 80 条行为/机制行，按概念归类 | 70 条 substrate 行，按层归类；同一机制只在其所属层计一次 |
| 计数 | 48 已接 / 10 部分 / 22 未接（基线 `ed457d75`，`4daf4222` 仅改文档） | 30 已接 / 12 部分 / 28 未接（基线 `4daf4222`） |
| 独有覆盖 | goals_outcome 的 judge/retry、improvement 的 reflexion、the_loop 的流式/压缩/计量等概念细节 | 第 5 层 Skill/Playbook、第 6 层 Trigger/Sentinel、第 7 层 Soul、第 8 层 MCP/Credentials/Budgets，以及层间依赖 |
| 状态差异的原因 | 概念页按机制逐条判定，配置路由与运行时机制可能分开计分 | 本审计按层汇总，配置路由不算层已接；只有配置路由、运行时机制不运行的子系统降为部分或未接（Trigger / Sentinel 通知 / Soul reflect 最明显） |

使用建议：

- 要看某个概念页（例如 queen、worker_agent、improvement）的逐条细节，读 `CONCEPT_WIRING_AUDIT.md`。
- 要看“为什么 Go 版看起来什么都有、实际却跑不起一个自治 colony”，读本审计的层间依赖结论。
- 两份文档的计数不能相加：本审计的 70 行是 substrate 视角，概念审计的 80 行是 concept 视角，存在交叉但口径不同。

---

## 4. 重点核查清单（按任务逐项）

本节是摘要；完整路径与行号以上文分层明细表为准。

| 核查项 | 结论 | 依据 / 缺口 |
| --- | --- | --- |
| Plan：`tasks.json` 的创建/更新/读取是否真在 queen 会话可用 | **已接（chat Queen）** | `internal/cli/task_tools.go:47` + `internal/tasks/session_tools.go:747` + `internal/tasks/store.go:244`；4 个 `task_*` 工具在会话目录上真实读写。worker/overseer 没有 task 工具。 |
| Tracker：写路径、binding、queen/worker 工具 | **已接（live）** | `internal/colony/tracker.go:61/147` + `binding.go:67` + `tracker/tools.go:1030` + `session_tools.go:95-115/234-247` + `colony/runtime.go:761`；queen 4 工具、worker 2 工具，均按 binding 执行。 |
| Event bus：worker 报告 → queen | **已接（live）** | `colony/runtime.go:959/1027` → `queen_tools.go:457/393` → `chat_runtime.go:299/860`；报告作为非 client user turn 注入。 |
| Event bus：SSE → UI | **部分** | 会话 transcript SSE 已接（`chat_routes.go:168` + `chat_runtime.go:591-700`）；colony host 的 bus 与 session bus 分离，worker/colony 事件没有 SSE 路由，UI 只能轮询 worker 列表。 |
| Reminder hub：source 注册与触发点 | **已接（chat Queen）** | `chat_reminders.go:54-80` 注册 6 个 source；`chat_runtime.go:475/479/530` + `loop.go:281/324/340/496/740/813` 覆盖主要点。缺口：`STREAM_STALLED` 未注册、无 tracker 快照、skills catalog 恒空、worker 无 Hub。 |
| Skill：`write_skill` / `skill_view` 类工具是否注册；skills 写入是否生产可达 | **未接** | 全仓无 `write_skill`/`skill_view` 工具定义；`skills.WriteSkill`（`skill_writer.go:169`）零调用；`SkillDirs`（`agentloop/types.go:144`）从不赋值；`GET /colony/skills` 恒空。 |
| Playbook：`internal/playbook/runner.go` 是否有生产调用或 `run_playbook` 工具注册 | **未接** | `internal/playbook` 在 `internal/`、`cmd/` 零 import；`NewPlaybookRun`/`Converge` 只有测试；`run_playbook` 只出现在提示文案（`colony_parallel_nudge.go:28/39`），没有工具定义/执行器/路由。 |
| Trigger：`StartTriggerTimer` 依赖的 `QueenExecutor` 是否有赋值；重启后 `triggers.json` 是否恢复 | **未接** | `QueenExecutor` 生产零赋值（只有 `internal/server/session.go:78` 字段和读取）；`ChatRuntime` 不实现 `InjectTrigger`；启动不扫描 `triggers.json`，已激活 trigger 重启即丢。 |
| Sentinel：`sentinel.NewManager` 是否有生产调用 | **未接** | `sentinel.NewManager` 只在 `manager_test.go` 调用；生产只有 `routes_sentinel.go` + `adapters_sentinel.go` 的配置/通知测试路由。 |
| Memory：recall / reflection 是否每轮生效 | **部分** | recall 每条 client 消息后都注入（`chat_runtime.go:712`）；reflection 不是每轮，默认每 6 个完成 turn（`memory/reflection.go:919` + `DefaultReflectionConfig`）；只有 chat Queen，worker/colony 无。 |
| Soul：除 HTTP 读写路由外，reflection 是否会写 soul | **未接** | `SoulRoutes` 的 reflect 依赖 `deps.Reflect`，而 `app.go:152` 只传 `Home`，`Reflect == nil` → 503；`memory/reflection.go` 不写 soul；soul 也不进 persona。 |
| Tools / MCP / credentials / budgets | **部分** | chat 工具目录和 loop 预算已接；worker 只有 builtin；MCP 只有配置路由，`LoadMCPConfig`/`NewMCPClient` 零生产调用；凭据只到 env 同步，无 agent `credentials` 工具；rate limit 只展示不执行。 |

---

## 5. 最关键的 5 个缺口

### 5.1 Trigger / Sentinel 全链路未接（第 6 层）

`QueenExecutor` 在生产代码中从未赋值；`ChatRuntime` 也没有实现 `InjectTrigger`；`triggers.json` 启动不恢复。结果是：定时 trigger 的 timer 即使被 `StartTriggerTimer` 建出来，`runTimerLoop` 也会在 `queenNode == nil` 时 `continue`；webhook 事件同样投递不到 Queen；手动 `POST .../triggers/{id}/run` 恒定 409。Sentinel 更彻底：`sentinel.NewManager` 零生产调用，只有配置和通知适配器。**colony 目前无法在无人值守时被唤醒，也无法在 Queen 停车时升级给人。**

### 5.2 Skill / Playbook 全链路未接（第 5 层）

没有 `write_skill` / `skill_view` 工具，`skills.WriteSkill` 零调用，`SkillDirs` 从不赋值，colony `skills/` 也不创建；`internal/playbook` 生产零 import，`run_playbook` 只活在提示文案里，`playbook_runs` 表没有写入者。**worker + tracker 已经能跑批量，但“把 pilot 固化成 skill / playbook，再按 tracker 现状 resume”这一整段没有接上。** 这是 `runtime_spine.md` 的“execute first, then systematize”闭环里缺口最大的一层。

### 5.3 Colony 的 Queen 仍是影子（第 2 层）

`runOverseer` 的 Queen 只跑一轮 seed 就阻塞在 `<-ctx.Done()`；工具面只有 builtin，没有 `run_worker`/tracker/task；不消费 chat 输入。`serve --colony` 预载会话的 `Chat` 恒 nil，`chat_routes.go:115` 直接 503。**真正能 fan-out 的 Queen 是“另建 / load / fork 出来的 chat Queen + 绑定 colony”，不是 colony 自己的 overseer。** 组织层因此只完成了“可派的 chat Queen”，没有完成“colony 自己的 lead”。

### 5.4 Worker 不是聚焦、受限、可恢复的克隆（第 2 / 8 层）

fork 路径把 Queen persona 写进 `worker.json.system_prompt`（`loader.go:962`）；`runWorker` 用 `systemPromptFor(spec)`，所以 worker 实际按 Queen 人格运行。`worker.json.tools` 完全不参与，worker 只拿 builtin + tracker + `report_to_parent`；没有 task、没有 skills、没有 integration/MCP。worker 转录只在内存，`resume_worker_ids` 显式 unsupported。**worker 的预算、grace、report、tracker 是对的，但“短命、聚焦、可恢复”只完成了“短命”。**

### 5.5 MCP / 能力边界没有真正打开（第 8 层）

MCP 只有 `routes_mcp.go` 的配置/健康路由；`LoadMCPConfig`、`NewMCPClient`、`mcpManager` 都没有生产调用，任何会话都不会装载 MCP 工具。worker/overseer 的工具面只有 builtin；credential 只有 env 同步和 HTTP 管理，没有 agent 可调用的 `credentials` 工具；rate limit 只在 `/api/config` 展示，`SocialRateLimiter.Check` 零调用。**UI/配置看起来支持 MCP、凭据、限额，但模型实际能用到的能力仍主要是 builtin + integration + tracker。**

---

## 6. 层间依赖结论

八层不是并列功能清单，而是有承重顺序的栈。按 `runtime_spine.md` 的 remove test，从下往上：

| 层 | 缺了会堵住 | 当前 Go 状态 |
| --- | --- | --- |
| 1. 执行 | 上面 2–8 层全部失去执行体；没有 loop 就没有 agent 的一步 | 基本完整，缺并行只读工具、park 落盘、spillover |
| 2. 组织 | 状态没有写者（Queen/worker）、信号没有生产者、方法/自治/沉淀没有作用对象 | chat Queen + `run_worker` 可用；colony overseer 是影子，`serve --colony` 不能 chat |
| 3. 状态 | 并行工作失去共享事实；Queen 无法校验/恢复；playbook 没有未完成行可查 | Plan + Tracker 对 chat Queen/worker 已接；worker/overseer 没有 task list |
| 4. 信号 | worker 结果不回 Queen；UI 静默；长会话漂移、重复派活 | worker→Queen 已接；colony→UI SSE 未接；worker 无 reminder hub |
| 5. 方法 | 一次性工作仍可做，但每个单元重复发现方法；批量只能逐行手工做 | 几乎全空：skill 只有 CLI，playbook 零 import |
| 6. 自治 | colony 只能等人来唤醒；Queen 停车时没人知道 | 全空：Trigger 配置路由 + Sentinel 配置路由，运行机制不启动 |
| 7. 沉淀 | 每个会话从零开始；Queen 的人格不演化 | memory 已接（chat Queen），soul 只有人工读写 |
| 8. 能力边界 | agent 只能用 builtin；外部系统、凭据、限额无法按角色收放 | chat 工具目录强；worker/overseer 只有 builtin；MCP 无客户端；预算只在 loop 内 |

具体堵点按“修复收益 / 依赖顺序”排：

1. **第 6 层（Trigger/Sentinel）堵住“无人值守 colony”**：第 2 层的 chat Queen 已经能派活，第 3 层的 tracker 已经能记录，第 4 层的报告已经能回 Queen；只要补上 `QueenExecutor` 赋值 + `InjectTrigger` 实现 + `triggers.json` 启动恢复，就能让现有链路在定时/外部事件下自动跑。Sentinel 同理，缺的是 `NewManager` 的生产装配和 `EscalationSource` 注册，而不是底层能力。
2. **第 5 层（Skill/Playbook）堵住“批量与复用”**：它依赖的第 2/3/4 层大部分已就绪（worker、tracker、报告），但 skill 写/读和 playbook runner 都没接。结果是“能做一次，不能系统性做一百次”。这是 `runtime_spine.md` 里“execute first, then systematize”最关键的一段。
3. **第 2 层（overseer/worker 身份）堵住“colony 自己的 Queen / 真正的 worker 克隆”**：overseer 没有 `run_worker`/tracker/task，worker 又跑 Queen persona、拿全量 builtin、不能 resume。即使第 5/6 层补上，派活的仍是“外挂 chat Queen”，worker 也不是概念里的受限克隆。
4. **第 8 层（MCP/credentials/rate limit）堵住“外部能力边界”**：chat Queen 的 integration 工具很多，但 worker/overseer 只有 builtin；MCP 只配置不连接；credential 只到 env；rate limit 只展示。对“colony 作为一个能操作外部系统的部署单元”来说，能力边界还没有建立。
5. **第 7 层（Soul）堵住“身份演化”**：memory 已经能沉淀知识，但 soul reflection 入口恒 503，soul 也不进 persona。沉淀层因此只有“记忆”，没有“成为谁”。这不是当前运行的硬阻塞，但它是概念中 Queen 与通用 coordinator 的最后分界线。

一句话结论：**Go 版已经把第 1–4 层的地基和第 2/3 层的主要路径接上（chat Queen 能派 worker、worker 能写 tracker、报告能回 Queen、任务计划能落盘），但第 5 层（方法）和第 6 层（自治）整体未接，第 7 层（Soul）和第 8 层（MCP/能力边界）只有壳；因此它现在是一个“能手工驱动的多 worker chat colony”，还不是 `runtime_spine.md` 描述的“能唤醒、能固化方法、能沉淀身份、能按边界行动”的 colony。**
