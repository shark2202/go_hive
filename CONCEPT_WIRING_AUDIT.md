# Go 运行时 × Key Concepts 接线审计

- 审计基线：`ed457d75`（`docs: 测试/引用数字更新为 4,313 / 4,582`）
- 上一版基线：`af55d368`（25 已接 / 12 部分 / 42 未接，79 行）
- 审计日期：2026-11-19 13:57 CST
- 审计对象：`docs/key_concepts/` 下 7 篇（colony、coordination、goals_outcome、improvement、queen、the_loop、worker_agent）
- 判定口径（严格）：
  - **已接**＝从 `cmd/hive` → `internal/cli` 或 `internal/server` 的 HTTP 路由真实可达，且在生产路径上生效；仅测试调用不算。机制本身是为某类 agent 设计的（如 worker 专用工具），该类 agent 在生产可达即算已接。
  - **部分**＝有真实入口但只实现了一半（只读不写、只内存不落盘、只覆盖一个阶段），或一个合并行里两种机制只有一种生效。
  - **未接**＝代码存在但生产入口走不到（死代码/仅测试/只有枚举与名字）。
- 证据口径：
  - `L`＝live 实测：交付侧 2026-11-19 用真二进制 + 本地假上游跑过，出处 `go_hive/docs/support-matrix.md`（本轮只读审计未重跑）。
  - `C`＝仅代码可达：本轮从 `cmd/hive` → CLI/server 路由逐段追踪；`go build ./...` 与相关定向 `go test` 通过，但测试本身不计入接线判定。
- 说明：只读审计，不改产品代码、不改测试、不改 `go.mod`。行号以 `ed457d75` 为准；Go 模块根为 `go_hive/go-hive/`。

## 汇总

| 概念 | 已接 | 部分 | 未接 | 合计 |
| --- | --- | --- | --- | --- |
| colony | 7 | 3 | 2 | 12 |
| coordination | 8 | 1 | 2 | 11 |
| goals_outcome | 6 | 1 | 3 | 10 |
| improvement | 4 | 0 | 4 | 8 |
| queen | 8 | 1 | 4 | 13 |
| the_loop | 10 | 3 | 2 | 15 |
| worker_agent | 5 | 1 | 5 | 11 |
| **合计** | **48** | **10** | **22** | **80** |

---

## 1. colony — 殖民地即部署单元

概念主张：colony 是单目录、可移植、可定时唤醒、跨会话持久的部署单元；Queen 与 worker 是同一个 loop；Queen 通过 `run_worker` 在运行时按需扩展；Create Colony 对话框确认后才 fork。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 单目录 `~/.hive/colonies/<name>/` + scaffold | `internal/hivehome/home.go`（`ColoniesDir`）、`internal/server/routes_colonies.go:409`（`handleScaffoldColony`）、`:440`（`ensureMinimalColony`） | `POST /api/colonies/{colony_id}/scaffold`（`routes_colonies.go:59`）→ 只写 `metadata.json`、`worker.json` | 部分 | C | scaffold 仍不建 `skills/`、`triggers.json`、runs；`tracker/` 改由 runtime `Start` 首次加载时补建（`internal/colony/tracker.go:61`）。fork 路径 `internal/cli/loader.go:939`（`writeColonyFromSession`）同样只写两个 JSON。 |
| 单目录可移植 / tar 导入 | `internal/server/routes_colonies.go:41-56`、`internal/server/routes_colonies_import.go:705`（`handleImportColony`） | `POST /api/colonies/import`、`/import/init`、`/import/chunk/*`、`/import/finalize/*` → 落盘 `home.ColoniesDir()` | 已接 | C | — |
| Queen 与 worker 是同一个 agent loop | `internal/colony/runtime.go:864`（`runWorker`）、`:931`（`runOverseer`）、`internal/agentloop/loop.go:478`（`ExecuteTurn`） | `run_worker` → `SpawnBatchWithOptions` → `spawnBatchOn` → `runGatedWorker` → `runWorker`（同一个 `agentloop.AgentLoop`）；load/fork/`serve --colony` → `StartOverseer` → `runOverseer` | 部分 | C | 两边确实都跑 `AgentLoop`，但 overseer 仍是独立 agent：只跑一轮 seed（无 seed 时 "Hello"）后阻塞在 `<-ctx.Done()`（`runtime.go:951-956`），不消费 chat 输入；工具面只有 builtin，没有 `run_worker`/tracker/task。 |
| Queen 用 `run_worker` 在运行时扩展 | `internal/colony/queen_tools.go:75`（`RunWorkerToolDef`）、`:203`（`RunWorker`）、`internal/cli/session_tools.go:88`（定义入表）、`:227`（执行）；`internal/server/chat_runtime.go:807`（`toolsForTurn`）、`:824`（`toolExecuteContext`）、`:841`（`BindColony`） | 会话工具表常驻 `run_worker`；未绑 colony 时 `toolsForTurn` 过滤掉；绑定后每次调用把 runtime 放进 ctx → `RunWorker` → `SpawnBatchWithOptions`，返回 worker_ids 并启动软/硬超时看门狗（`queen_tools.go:539`） | 已接 | L | 只有带 `ChatRuntime` 的会话可达；`serve --colony` 预载会话 `Chat == nil`（`chat_routes.go:115` 直接 503），不能 chat 也不能 `run_worker`；colony overseer 工具面没有 `run_worker`；`resume_worker_ids` 明确报不支持（`queen_tools.go:223`）。 |
| Create Colony 对话框 → fork 出 colony | `internal/server/routes_session_create.go:212`（分支）、`:238`（要求 seam）、`:245`（调用）；`internal/cli/loader.go:795`（`CreateColonyFromSource`）、`:939`（写目录） | `POST /api/sessions`（`colony_id` + `source_session_id`）→ `deps.CreateColonyFromSource` → 写 colony → build/start host → `createSessionWithRuntime(prebuilt)` | 已接 | C | fork 出的 `worker.json` 的 `system_prompt` 直接抄 Queen persona（`loader.go:962`），`tools` 列表也未被 runtime 使用；runs/workers 为空；`ColonySpawned` 锁在 `loader.go:910/931`。 |
| colony 生命周期 load / unload | `internal/server/routes_session_colony.go:55`（`handleLoadColony`）、`:170`（`handleUnloadColony`）；`internal/cli/loader.go:722`（`LoadColony`）、`:741`（`UnloadColony`）、`:759`（`attach`） | `POST /api/sessions/{id}/colony`（`routes_sessions.go:114`）/ `DELETE /api/sessions/{id}/colony`（注册为 `DELETE /api/sessions/{first}/{second}`，`routes_sessions.go:61-73`）→ `deps.Colony` → 建 runtime、`Start` + `StartOverseer`、替换/停掉旧 runtime | 已接 | C | unload 后 chat 侧绑定要等下一次 `GetSession` 才解除（`internal/server/session.go:411-420`）；卸载瞬间在跑的 turn 仍可能持有旧 runtime。 |
| `serve --colony` 启动即加载 runtime 与 overseer | `internal/cli/loader.go:293-326`、`:427`（`buildColonyHost`）、`:479`（`startColonyHost`） | `hive serve --colony <path>` → `defaultServe` → `record.Colony = buildColonyHost(...)` → `Start` + `StartOverseer` → `AddSession` | 已接 | L | 每个 `--colony` 新建 session id（`loader.go:300`），不恢复旧会话/旧 worker；预载 session 没有 `ChatRuntime`，只能看不能聊。 |
| tracker 作为 colony 的共享账本（建库 + 读写） | `internal/colony/tracker.go:61`（`provisionTracker`）、`:147`（`workerInputData`）；`internal/colony/runtime.go:449`（`Start`）、`:523`（`SpawnBatchWithOptions`）；`internal/server/routes_colony_workers_data.go:134`、`:704`、`:731`、`:790`、`:912-921` | colony `Start`/每次 spawn → `BindingForColonyDir` → `tracker/tracker.db`；worker spawn 把 binding 写进 `input_data`；REST 读表/读行/PATCH 改行走同一 DB | 已接 | L | 只对带 `metadata.json` 的目录建库；没有 metadata 的旧目录不建。 |
| colony 内 `skills/`（Queen 写、worker 加载） | `internal/skills/skill_writer.go:169`（`WriteSkill`）、`internal/skills/manager.go:48`（`NewSkillsManager`）、`internal/colony/worker_surface.go:246`、`internal/server/routes_colony_workers.go:571`（`handleListColonySkills`） | 无 | 未接 | — | `WriteSkill`/`NewSkillsManager` 零生产调用；`newColonyHost` 不设 `SkillsProvider`（`loader.go:458-472`），`GET /colony/skills` 恒空；`AgentContext.SkillDirs` 从不赋值，`loop.go:828` 的拦截永不触发。 |
| 任务计划（Queen 的 spine） | `internal/tasks/session_tools.go:747`（`BuildSessionTools`）、`internal/cli/task_tools.go:47`（`newTaskToolSet`）、`internal/cli/session_tools.go:70`、`:78` | `hive serve` → `createSessionWithRuntime` → `newSessionToolCatalogue` → `task_create/task_update/task_list/task_get`；每次调用 `tasks.SetExecContext` | 已接 | C | 只有 chat Queen 有；colony overseer 与 worker 工具表都没有 `task_*`；任务存放在 `HIVE_HOME` 会话树，不在 colony 目录。 |
| cron triggers 定时唤醒 colony | `internal/server/trigger_runtime.go:291`（`StartTriggerTimer`）、`internal/triggers/triggers_store.go:20`（`ReadAgentTriggersJSON`） | `POST/PATCH /api/sessions/{id}/triggers*` → `StartTriggerTimer`，前置 `session.QueenExecutor != nil`（`trigger_runtime.go:46`） | 未接 | — | `QueenExecutor` 生产代码零赋值；启动不扫描 `triggers.json`；定时器从不启动。 |
| colony 跨会话持久 / 重启恢复 | `internal/colony/resume.go:11`（`IterQueenSessionDirs`）、`:42`（`ColonyIDForSessionDir`）、`:57`（`DeriveResumeColonyID`）；`internal/host/host.go:120`（`Start`）；`internal/cli/loader_session_resume.go:73` | 无 | 部分 | C | colony 目录与 `tracker.db` 持久；runs/workers/worker 转录不落盘；`resume.go` 三函数零调用；colony-bound 会话明确不走 `queen_resume_from`（`loader_session_resume.go:69-73`）。 |

**小结**：colony 从"能加载、能起一个空转 Queen"变成了"chat Queen 能在绑定的 colony 里 fan-out"。`run_worker`、tracker 建库/binding/读写、worker 回报都已真接。还剩三个结构性问题：colony 自己的 overseer 没有 `run_worker`/tracker、没有任务工具，仍是影子 Queen；`serve --colony` 预载会话没有 chat runtime；skills、triggers、重启恢复仍未接。

## 2. coordination — tracker、任务计划、事件总线、提醒中枢

概念主张：colony 没有图；Queen 与 worker 靠四个轻量底座同步——共享 tracker、持久任务计划、事件总线、提醒中枢；人机协同靠 Queen 提问停车 + Sentinel 兜底。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| tracker 共享账本（读侧 + UI 行编辑） | `internal/tracker/tracker.go:234`（`EnsureTrackerDB`）、`internal/server/routes_colony_workers_data.go:704`（`handleColonyListTables`）、`:731`（`handleColonyTableRows`）、`:790`（`handleColonyUpdateRow`）、`:912-921` | `GET /api/colonies/{id}/data/tables`、`.../data/rows`、`PATCH .../rows` → `EnsureTrackerDB` / `updateTableRow` | 已接 | C | 读与 UI 行编辑可用；全局数据路由另有 insert/delete（`routes_colony_workers_global.go`）。 |
| Queen 声明可写列、worker upsert、Queen 用 SQL 校验 | `internal/tracker/tools.go:1030`（`RegisterTrackerTools`）、`:846`（`BuildTrackerTools`）、`:156`（`TrackerSQL`）、`:405`（`Upsert`）；`internal/cli/session_tools.go:95-115`（queen 表）、`:234-247`（执行）；`internal/colony/tracker.go:84`（worker defs）、`:126`（worker registry）、`:174`（执行） | queen 会话工具表含 `tracker_sql/tracker_register_writable/tracker_upsert/tracker_query`；worker 工具表含 `tracker_upsert/tracker_query`；执行时按 binding 解析 | 已接 | L | 工具名与角色过滤一致；无 colony 的 plain queen 会话也看得到 tracker 工具，调用返回 `no binding`（没有按绑定过滤定义）。 |
| tracker binding（无 binding 拒绝写） | `internal/tracker/binding.go:59`（`BindingForColonyDir`）、`internal/colony/tracker.go:190`（`TrackerBindingFromContext`）、`internal/colony/worker.go:377`（`TrackerResolver`）、`internal/cli/session_tools.go:236` | spawn 时 `input_data` 带 binding；worker resolver 读 `input_data`；queen 每次调用从 ctx 取 runtime binding；nil → `ErrNoBinding` | 已接 | L | binding 由 runtime `Start` 派生；`RuntimeOptions.Binding` 目前没有生产注入者（只有测试/宿主自备）。 |
| 持久文件型任务计划（Queen 的 spine） | `internal/tasks/session_tools.go:747`、`internal/cli/task_tools.go:47`、`internal/cli/session_tools.go:70`、`internal/server/routes_tasks.go:39` | `hive serve` 会话创建 → 工具表含 4 个 task 工具；`GET /api/sessions/{id}/tasks` 读同一 store | 已接 | C | worker 没有 task 工具；colony overseer 没有 task 工具。 |
| 事件总线：会话实时 transcript → UI SSE | `internal/server/chat_runtime.go:570-700`（emitter）、`internal/eventbus/emit.go`、`internal/server/chat_routes.go`（`GET /api/sessions/{id}/events`） | 每轮 emit execution/loop/assistant/tool/usage；SSE 路由读同一 bus，`events.jsonl` 落盘 | 已接 | C | — |
| 事件总线：worker 报告 → Queen 的 `[WORKER_REPORT]` | 发布 `internal/colony/runtime.go:959`（`publishReport`）、`:1027`（`publishEvent`）；订阅/格式化 `internal/colony/queen_tools.go:457`（`SubscribeQueenReports`）、`:393`（`FormatWorkerReport`）；消费 `internal/server/chat_runtime.go:299`（`InjectWorkerReport`）、`:841`（`BindColony`） | 绑定 colony 时订阅 `SUBAGENT_REPORT` → `FormatWorkerReport` → `InjectWorkerReport` → 下一轮以非 client user message（`[External event]`）注入 | 已接 | L | 报告 payload 没有 `duration_seconds`，`FormatWorkerReport` 会跳过该字段；没有 `output_file`；persistent overseer 的报告被跳过；预载 colony 没有 chat runtime 时无人消费。 |
| 提醒中枢：session start / post-tool / budget / compact / idle 注入 | `internal/server/chat_reminders.go:54`（注册 + `Hub.Start`）、`:80`；`internal/agentloop/reminder_hub.go:235`（`Start`）；触发点 `internal/agentloop/loop.go:281`（USER_PROMPT_SUBMIT）、`:340`（POST_COMPACT）、`:496`（SESSION_START）、`:740`（POST_TOOL_USE）、`:813`（TOOL_BUDGET_CHECKPOINT）；`internal/server/chat_runtime.go:475/479/530`（SESSION_START/USER_PROMPT_SUBMIT/STOP） | 建 `ChatRuntime` → 注册 6 个 source（task、colony_parallel、active_workers、searchable_tools、skills_catalog、idle）→ `Bind` → `Hub.Start`（idle ticker）→ 各生命周期点 `Fire` 并把 block 写回 conversation | 已接 | C | `STREAM_STALLED` 的 source 没注册（`stream_stall.go` 零注册）；REST `GET /api/sessions/{id}/reminders` 用的是另一套 `ReminderRuntime`，chat 已不再 `TurnComplete` 喂它，恒空。 |
| 提醒内容：fleet 感知 / tracker 快照 / 工具与技能清单 | `internal/agentloop/active_workers_reminder.go:22`、`internal/agentloop/colony_parallel_nudge.go:54`、`internal/agentloop/tool_skill_reminders.go:23`、`internal/tasks/reminders.go:491` | active_workers + colony_parallel + searchable_tools + skills_catalog + task 均已注册 | 部分 | C | 没有 tracker 快照 source；`skills_catalog` 的 provider 在 loader 从未注入（`deps.SkillsCatalog` 空），该 source 恒静默；searchable_tools 只有 worker 侧有 provider；plain chat 的 fleet/tracker 视图只有 active_workers。 |
| 人机协同：Queen 用 `ask_user` 提问并停车 | `internal/agentloop/synthetic_tools.go:94`（`BuildAskUserTool`）、`internal/server/chat_runtime.go:785`、`:964`（`executeAskUser`）、`:975`（`EmitClientInputRequested`） | 会话工具表 + `ask_user` → executor 记录 `pendingQuestions` 并发 `client_input_requested(park_reason=ask_user)`；本轮结束快照为 parked | 已接 | C | 停车状态只在内存（`pendingQuestions` + `parkReason`），下一条用户消息清空；无落盘 park 状态，进程重启即丢。 |
| parked loop 落盘 + 恢复 | `internal/agentloop/park_reason.go`（仅枚举）、`internal/server/chat_runtime.go`（只有内存 park 状态） | 无 | 未接 | — | 没有 park 状态写入、没有 resume 入口；凭据等待无实现。 |
| Sentinel：停车分类、nudge 或升级 inbox/Telegram/Slack | `internal/sentinel/manager.go:99`（`NewManager`）、`internal/sentinel/classifier.go`、`internal/sentinel/listeners.go`、`internal/sentinel/escalation_source.go` | 只有通知配置/凭据校验/测试路由（`internal/server/routes_sentinel.go:104-110`）与 store/notifier 适配器（`internal/server/adapters_sentinel.go`） | 未接 | — | `sentinel.NewManager` 零生产调用；classifier/listener/escalation source 从不运行。 |

**小结**：四个底座现在三个真正生效：任务计划、transcript 事件总线、tracker（读 + 写 + binding）；提醒中枢也补全到 6 个 source、`Hub.Start` 与全部主要注入点。仍缺的是 Sentinel、park 落盘/resume，以及提醒内容里的 tracker 快照与 skills catalog 数据源。

## 3. goals_outcome — 目标与结果驱动

概念主张：用可度量的成功标准、硬/软约束和上下文声明结果；结构化 `Goal` 的成功标准成为 loop judge 的判据；judge 可 accept/retry/escalate，retry 反馈回注；会话式结果则落到任务计划与 tracker 列上校验。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 从 agent 定义加载结构化 `Goal` | `internal/agents/agent_spec.go:439`、`internal/host/host.go:28/84`、`internal/colony/runtime.go:221`（`Goal()`） | `hive serve --colony` / load / fork → `newColonyHost` → `LoadAgentSpec` → `Host.Config.Goal` → `RuntimeOptions.Goal` | 部分 | C | runtime 只保存不消费；`Goal()` 没有调用者；`Goal.Description/Context/Constraints` 不进 prompt；chat Queen 根本没有 Goal。 |
| 成功标准带权重、可度量（`output_contains` / `output_equals` / `llm_judge` / `custom`） | `internal/schemas/goal.go:142`（`IsSuccess`）、`:201`（`Validate`） | 无 | 未接 | — | `IsSuccess` 零调用；没有代码按 `Metric` 执行检查；judge 只读自然语言 `AgentSpec.SuccessCriteria`，不读加权标准。 |
| 约束（hard=MUST / soft=SHOULD）与 Context 写入 agent prompt | `internal/schemas/goal.go:157`（`ToPromptContext`）、`internal/agentloop/types.go:109`（`GoalContext`）、`internal/agentloop/loop.go:460` | 无 | 未接 | — | `ToPromptContext` 零调用；`AgentContext.GoalContext` 生产代码从未赋值，`BuildInitialMessage` 里的拼接永不命中。 |
| success_criteria → `AgentSpec` 编译（新判定行） | `internal/agents/agent_spec.go:439-441`、`internal/agentloop/criteria.go:12`（`SuccessCriteriaText`）、`internal/agentloop/judge.go:92`（`ConfigureJudge`） | `LoadAgentSpec` 把 node-level 字符串或 goal 列表编译成 `AgentSpec.SuccessCriteria`；colony worker/overseer 构造 loop 后 `ConfigureJudge` 据此决定是否建 judge | 已接 | C | chat Queen 没有 criteria 来源；worker.json 的 `goal.success_criteria` 列表是可用的 Go 侧扩展。 |
| loop judge：每轮评估 accept / retry / escalate | `internal/agentloop/judge.go:109`（`shouldJudge`）、`:127`（`evaluateJudge`）、`internal/agentloop/loop.go:618`（自然停止）、`:772`（工具失败/节奏） | colony worker/overseer 构造 loop → `loop.ConfigureJudge(worker.Context.AgentSpec, r.workerCompleter(), r.bus)`（`colony/runtime.go:898/950`）；criteria 非空时在自然停止/工具失败/每 N 轮评估 | 已接 | C | 只有 colony worker 路径有 criteria；chat Queen 无 criteria 也无 `ConfigureJudge`（`chat_runtime_judge_test` 明确 judge-free）；overseer 的 `queenOverseerSpec` 无 criteria，实际不评估。 |
| retry 反馈回注会话 | `internal/agentloop/judge.go:198`（`injectJudgeFeedback`）、`internal/agentloop/loop.go:626`、`:784` | RETRY → 写一条 `[Judge feedback]` user message，下一轮继续 | 已接 | C | 依赖上一条；chat Queen 无 judge 即无 RETRY。 |
| judge 小流水线（输出键检查 → 自定义 judge / LLM 质量检查） | `internal/agentloop/llm_judge.go:36`（`NewLLMJudgeAdapter`）、`:57`（`NewLLMJudgeAdapterFromCompleter`）、`internal/llmjudge/judge.go:111`（`Evaluate`）、`internal/eventbus/emit.go:363`（`EmitJudgeVerdict`） | `ConfigureJudge` → adapter → `llmjudge.Evaluate`；verdict 通过 bus 发 `judge_verdict` | 已接 | C | 只在 colony worker 路径；`hive skill eval` 仍用同一 `llmjudge` 但不同入口。 |
| 会话式结果：Queen 把消息转成任务计划 | `internal/tasks/session_tools.go:747`、`internal/cli/task_tools.go:47`、`internal/cli/session_tools.go:70` | 同 coordination 任务计划 | 已接 | C | — |
| 会话式结果：以 tracker 列作为 "done" 并用 SQL 校验 | `internal/tracker/tools.go:156/633`、`internal/cli/session_tools.go:234` | Queen 可 `tracker_sql`/`tracker_query`；worker `tracker_upsert` 推进状态 | 已接 | L | 框架不强制判定 "done"；是否查询/校验由模型自行发起。 |
| 未达标信号驱动改进（missed criterion / 空 tracker 列） | 无 | 无 | 未接 | — | judge RETRY 只回注文本，未映射成 improvement 信号；空 tracker 列无检测。 |

**小结**：goals_outcome 的**执行侧**这轮真正接上了：`success_criteria` 从 worker.json 编译进 `AgentSpec`，worker loop 的 judge 会评估、发 `judge_verdict`、RETRY 回注。**声明侧**仍缺：结构化 `Goal` 的权重/约束/Context 不消费，chat Queen 没有 criteria 来源，因此"目标驱动"主要发生在 worker，不在 Queen。

## 4. improvement — colony 如何变好

概念主张：四条 in-band 改进机制——会话内 reflexion（含 judge retry 反馈）、跨会话的作用域记忆（反射写入 + recall 召回 + 过往对话召回）、学到的 skills（`write_skill` → `skills/` → worker 目录），以及把 pilot 固化成 playbook（确定性 runner、重试/退避/死信、按 tracker 现状 resume）。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Reflexion 第一层：工具结果/错误回注，下一轮自我修正 | `internal/agentloop/loop.go:756`（`AddToolResult(..., IsError)`）、`internal/agentloop/conversation.go` | `ChatRuntime.runTurn` → `ExecuteTurn`，每批工具结果写回 conversation | 已接 | C | — |
| Reflexion 第二层：judge retry 的 critique 回注 | `internal/agentloop/judge.go:198`、`internal/agentloop/loop.go:626/784` | colony worker（带 criteria）RETRY → `[Judge feedback]` user message | 已接 | C | 只覆盖 colony worker；chat Queen 无 judge。 |
| 跨会话记忆：冷却门控的 reflection 写入 global / per-queen markdown | `internal/server/chat_runtime.go:211-219`（构造 `ReflectionScheduler`）、`:535`（`OnTurnDone`）、`internal/memory/reflection.go:919`、`:1030` | `hive serve` → `createSessionWithRuntime` → `NewChatRuntime`（`HomeRoot` 非空）→ `runTurn` 成功后 `OnTurnDone(ctx,"queen","turn_done")` | 已接 | C | 仅 Queen chat；worker/colony 无 reflection。 |
| 跨会话记忆：recall selector 在后续会话召回相关笔记 | `internal/server/chat_runtime.go:704`（`injectRecall`）、`:716`；`internal/memory/recall.go:174`（`BuildScopedRecallBlocks`） | 每条用户消息后、`ExecuteTurn` 前以 `<system-reminder>` 注入；内容不变时用 `lastRecall` 去重 | 已接 | C | — |
| 过往对话召回：每轮最多 6 条 dated excerpts；`search_messages` 可搜历史 | `internal/memory/search.go:534`（`SearchMessages`）、`:678`（envelope）、`internal/memory/search_timeline.go`；工具名只在 `internal/tools/tier_defaults.go:69-70` | 无 | 未接 | — | `SearchMessages`/`SearchTimeline` 零生产调用；会话工具表不含 `search_messages`/`search_timeline`；`chat_runtime` 不注入历史对话摘要。 |
| 学到的 skills：`write_skill` 写进 colony `skills/`，worker 目录按需加载 | `internal/skills/skill_writer.go:169`（`WriteSkill`）、`internal/skills/manager.go:48`（`NewSkillsManager`）、`internal/agentloop/skill_resources.go:11` | 无 | 未接 | — | `WriteSkill`/`NewSkillsManager` 零调用；没有 `write_skill`/`skill_view` 工具；`AgentContext.SkillDirs`（`types.go:144`）从未赋值，`loop.go:828` 的拦截永不触发；`SkillsCatalogPrompt` 从未生成。 |
| Playbook：确定性 runner 收敛剩余批次（重试/退避/死信） | `internal/playbook/runner.go:190`（`NewPlaybookRun`）、`:423`（`Converge`）、`:37-56`（`DeadLetter`）、`internal/playbook/rows.go:268-283` | 无 | 未接 | — | `internal/playbook` 在 `internal/`、`cmd/` 零 import；`run_playbook`/`get_playbook_status`/`stop_playbook` 没有工具定义/执行器/路由。 |
| Playbook 的 resume by construction（以 tracker 现状决定剩余工作） | `internal/playbook/rows.go`（`QueryRows`）、`internal/colony/colony_db.go:19-27`（`playbook_runs` 表） | 无 | 未接 | — | playbook 无入口；tracker 写路径虽已接，但 playbook 不运行。 |

**小结**：improvement 从"只记住"变成了"记住 + 评估后重试"：judge RETRY 的 critique 现在真的会回注到 worker 会话。但"把成功固化成可复用流程"（skills + playbook）和"过往对话召回"仍零入口，改进闭环的上半段仍不完整。

## 5. queen — 常驻的客户面向 lead

概念主张：Queen 是身份化 persona（13 个 YAML）而非通用 coordinator；她拥有会话、计划、tracker；带作用域记忆（global + per-queen）；两个阶段（independent → colony）；提问时停车、Sentinel 兜底。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 13 个 Queen persona（YAML profile，含 traits/background/behavior） | `internal/queen/queen_defaults/*.yaml`（13 个）、`internal/queen/profiles.go:37`（`DefaultQueens`）、`internal/server/routes_queens.go:1006`（`GET /api/queen/profiles`） | `hive serve` 注册 Queen 路由，profile 列表/详情/创建/改名/删除读写 YAML 目录 | 已接 | L | — |
| persona 注入 Queen 的 system prompt | `internal/queen/identity.go:85`（`FormatQueenIdentityPrompt`）、`internal/server/chat_runtime.go:937`（`chatQueenIdentityPrompt`）、`:236`；`internal/cli/loader.go:494-513`（`queenOverseerSpec`） | `createSessionWithRuntime` → `NewChatRuntime` → `IdentityPrompt`；colony 路径 → `StartOverseer(queenOverseerSpec(...))` | 已接 | C | `default` queen 无 profile 时回退通用 `chatIdentityPrompt`（预期）。 |
| 选择一个 Queen 并开一个绑定会话 | `internal/server/routes_queens_sessions.go:85`（`handleQueenSession`）、`:58`（`queenCreateBoundSession`）、`internal/cli/loader_session_resume.go:38`（`planChatSession`）、`loader.go:1040`（`chatSessionDir`） | `POST /api/queen/{queen_id}/sessions` → `deps.CreateSession` → `QueenName` 进入会话记录、`queens/<name>/sessions/<id>` 目录、记忆 scope | 已接 | C | `initial_phase` 仍被忽略（不影响绑定本身）。 |
| 两个阶段：Independent → Colony | `internal/server/routes_session_create.go:155-172`（接收 `initial_phase`）、`:212-252`（fork 分支）、`internal/server/session.go:50`（`PhaseState`） | `POST /api/sessions` 可 fork；但 `createSessionWithRuntime` 不消费 `InitialPhase`，`PhaseState` 恒 nil（全仓无赋值） | 部分 | C | phase 状态没有落点；`GET /api/queen/{id}/tools` 在 `PhaseState == nil` 时直接 404（`routes_queens_tools.go:581`）。 |
| Queen 的持久会话与历史恢复 | `internal/server/chat_runtime.go:243`（`RestoreConversation`）、`internal/agentloop/conversation.go:1126`（`persist`）、`:1213`（`RestoreConversation`）、`internal/cli/loader_session_resume.go:38` | `NewChatRuntime` 从会话目录恢复 parts；`queen_resume_from` 复用旧 id 与旧目录，`resumeLiveSession`（`loader_session_resume.go:142`）防重复 runtime | 已接 | C | colony-bound 会话不走 `queen_resume_from`（`loader_session_resume.go:69-73`）。 |
| Queen 的 per-queen 记忆：reflection 写入 + recall 召回 | `internal/server/chat_runtime.go:211`、`:704`；`internal/memory/memory.go`、`recall.go:174` | 同 improvement 3/4 | 已接 | C | — |
| Queen 的过往对话召回：每轮 6 条 dated excerpts + `search_messages` | `internal/memory/search.go:534`、`search_timeline.go`；工具名只在 `tier_defaults.go:69-70` | 无 | 未接 | — | 同 improvement 5。 |
| `memory_timeline` flag 打开后生成/召回事件时间线 | `internal/memory/timeline.go:616`（`MemoryTimelineEnabled`）、`:491`（`UpdateSessionTimeline`）、`:528`（`BackfillTimelines`）、`:645`（`SubscribeTimelineTriggers`） | flag 只在 `server/config_features.go:74-79` 读出并出现在 `/api/config`（`:189`） | 未接 | — | timeline 的提取/回填/订阅函数零生产调用。 |
| Queen 拥有任务计划（计划可见、可编辑、跨 reload） | `internal/tasks/session_tools.go:747`、`internal/server/routes_tasks.go:39` | 同 coordination 任务计划 | 已接 | C | — |
| Queen 拥有 tracker（建 schema、派活、SQL 校验） | `internal/tracker/tools.go:1030`、`internal/cli/session_tools.go:95-115`、`internal/colony/tracker.go:190` | 会话工具表常驻 4 个 tracker 工具；绑定 colony 后按 runtime binding 执行；无绑定返回 `ErrNoBinding` | 已接 | L | 未绑 colony 时工具仍可见但不可用；plain Queen 会话拿不到 colony tracker。 |
| 提问停车（`ask_user`） | `internal/agentloop/synthetic_tools.go:94`、`internal/server/chat_runtime.go:785/964/975` | chat 会话工具表始终追加 `ask_user`；executor 记录 `pendingQuestions` 并发 `client_input_requested(park_reason=ask_user)`；本轮结束快照为 parked | 已接 | C | 停车状态只在内存，进程重启即丢。 |
| Sentinel：停车后 nudge / 升级 inbox/Telegram/Slack 并恢复 | `internal/sentinel/manager.go:99`、`classifier.go`、`listeners.go`、`escalation_source.go` | 只有通知配置/凭据校验/测试路由与 store/notifier 适配器 | 未接 | — | `sentinel.NewManager` 零生产调用；没有"parked Queen 被 nudge 或升级"的链路。 |
| Queen 与 colony overseer 是同一个 lead | `internal/cli/loader.go:494`（`queenOverseerSpec`）、`internal/colony/runtime.go:931`（`runOverseer`） | load/fork/`serve --colony` → `StartOverseer` 起一个独立 worker（`overseer:<colony>`） | 未接 | C | overseer 与 chat session 不共享对话/工具/输入：工具面只有 builtin，跑一轮后阻塞；chat Queen 有 `run_worker`/tracker/task，overseer 都没有。用户看到的 Queen 与 colony 里的 Queen 仍是两个 agent。 |

**小结**：Queen 的 persona、绑定会话、持久/恢复、记忆、任务计划、`ask_user`、以及**tracker**都真正接通了；`run_worker` 让 chat Queen 成为真正的编排者。最大的残留断点从"Queen 不会派活"变成"colony 里的 overseer 不是这个 Queen"：它是无 `run_worker`、无 tracker、无 task、只跑一轮的影子。

## 6. the_loop — 唯一的执行原语

概念主张：一个多轮流式 LLM 会话循环；每轮流式输出、并行跑只读工具、回注结果、决定停止/继续；judge 自纠错；Queen/worker 只是一组 `LoopConfig` 差异；内建崩溃恢复（cursor、park/resume）、上下文压缩、计量与提醒。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 单一 agent loop 作为 Queen 的执行体 | `internal/agentloop/loop.go:478`（`ExecuteTurn`）、`internal/server/chat_runtime.go:259`（`NewAgentLoop`）、`:501` | `hive serve` → `createChatSession` → `NewChatRuntime` → 每轮 `runTurn` 调用同一 loop | 已接 | C | — |
| 流式输出（文本边生成边推） | `internal/llm/stream.go:33`（`StreamCompleter`）、`internal/llm/stream_openai.go:42`（`Provider.ACompleteStream`）、`internal/server/chat_stream.go:113`（`AComplete`）、`internal/server/chat_runtime.go:446`（`beginTurn`）、`:618/633`（delta 发布） | CLI provider 实现 `StreamCompleter` → `newChatStreamingCompleter` 包装 → loop 的 `AComplete` 内部走 SSE → 每帧 `client_output_delta` | 已接 | L | Anthropic/MiniMax 明确 `ErrStreamingUnsupported` 并回退非流式；tool-call 参数仍按完整对象在末尾聚合，没有逐片下发。 |
| 只读工具并行执行、其余按序 | `internal/agentloop/loop.go:660-770`（`for i, tc := range toolCalls` 顺序执行） | 同一 loop；`ToolCallStarted`/`Completed` 逐个发 | 未接 | — | 没有 `ConcurrencySafe` 分组/并发执行；该字段只被定义和传递，从未被 loop 消费。 |
| 工具结果回注，循环直到不再调用工具 | `internal/agentloop/loop.go:614`（无 tool calls → 返回）、`:756`（`AddToolResult`） | `ChatRuntime.runTurn` 使用该路径 | 已接 | C | — |
| 自纠错第一层：错误结果进入下一轮上下文 | `internal/agentloop/loop.go:756`（`ToolResultOptions{IsError: ...}`） | 同上 | 已接 | C | — |
| judge：accept / retry / escalate，retry 反馈注入 | `internal/agentloop/judge.go:109/127/198`、`internal/agentloop/loop.go:618/772` | `ConfigureJudge` 在 colony worker/overseer 构造时注入（`colony/runtime.go:898/950`）；criteria 非空才评估 | 已接 | C | chat Queen 无 criteria → `Judge == nil`，永不评估；overseer 无 criteria 同样不评估。 |
| stall / doom-loop 检测 | `internal/server/stall_check.go:79`（`SubscribeStallCheck`）、`internal/agentloop/park_reason.go:26`（`ParkReasonDoomLoop`）、`internal/agentloop/idle_nudge.go:329` | 无 | 未接 | — | `SubscribeStallCheck` 零调用（内部 `AskJudge` 从不运行）；`ParkReasonDoomLoop` 从不产生；没有重复调用指纹检测。 |
| Queen 与 worker 的 `LoopConfig` 差异（worker 3+1、30/90/200；Queen soft checkpoint 30/50） | `internal/agentloop/loop.go:49-68`（默认 50 轮、`ToolCallBudget=0`、`ToolCallLifetimeBudget=0`）、`internal/colony/worker_definition.go:9`（worker 3/1/30/3/200）、`worker_loop_config.go:46` | `run_worker` → `BuildWorkerLoopConfig` 应用 worker 数值；Queen 用 `DefaultLoopConfig()` | 部分 | C | worker 数值现在生产可达；Queen 仍没有 30/50 软检查点（budget=0）。 |
| Grace iteration：worker 预算耗尽仍有最后一次 `report_to_parent` | `internal/agentloop/loop.go:96`（`graceTerminalTools`）、`:529-556`、`internal/colony/worker_definition.go:9-16` + `worker_loop_config.go:46-52` | worker `GraceIterations=1`；grace 轮只允许 `report_to_parent`/`tracker_upsert`/`task_update` | 已接 | C | — |
| 迭代上限与干净停止 | `internal/agentloop/loop.go:795`（超限返回 error）、`internal/server/chat_runtime.go:501-519`（失败上报） | `MaxIterations=50` 时 `runTurn` 走 `executionFailed` | 已接 | C | — |
| 崩溃恢复：cursor 持久化 + 会话恢复 | `internal/agentloop/conversation.go:1126`（`persist`）、`:1213`（`RestoreConversation`）、`internal/server/chat_runtime.go:243` | 每条消息落盘并更新 cursor；`NewChatRuntime` 恢复 parts；`queen_resume_from` 支持按旧 id 恢复 | 已接 | C | worker 转录不落盘；colony-bound 会话不走 resume。 |
| park / resume：等待人类回答或凭据后从磁盘继续 | `internal/agentloop/park_reason.go`（仅枚举）、`internal/server/chat_runtime.go`（内存 park 状态）、`:752`（`injectHubReminders`） | `ask_user` 产生的停车在内存；idle nudge 读 `parkReason`；下一轮用户消息清空 pending | 部分 | C | 没有 park 状态落盘、没有 resume 入口；凭据等待未实现；进程重启即丢。 |
| 上下文压缩：自动检测并压缩；超大工具结果落盘可搜 | `internal/agentloop/loop.go:320`（`compactConversationIfNeeded`）、`internal/agentloop/conversation.go:736`（`NeedsCompaction`）、`:931`（`Compact`）、`internal/agentloop/llm_compact.go:22`；手动路由 `routes_execution.go:444` → `:638`（`LlmCompact`） | 每轮 LLM 调用前检查 `NeedsCompaction()` → `LlmCompact` → `conv.Compact`，并触发 PRE/POST_COMPACT；另有 `POST /api/sessions/{id}/compact-and-fork` | 部分 | C | 自动压缩已接；spillover 仍没有生成者（`SpilloverPath`/`ExtractSpilloverFilename` 只读不写），超长工具结果不会自动落盘。 |
| 每次 LLM 调用计量（tokens/cost）与工具预算上限 | `internal/agentloop/loop.go:595`（`LLMTurnComplete`）、`internal/server/chat_runtime.go:641`（emit usage）、`loop.go:625-760`（soft/hard/lifetime 预算） | 每次调用发 `llm_turn_complete`（input/output/cached/cost）；工具计数与 defer 在 loop 内执行 | 已接 | C | — |
| 框架注入提醒保持连贯 | `internal/agentloop/reminder_hub.go`、`internal/server/chat_reminders.go:54`、`internal/server/chat_runtime.go:752` | 同 coordination 提醒 | 已接 | C | `STREAM_STALLED` 未注册；REST reminder surface 与 chat hub 脱节。 |

**小结**：the_loop 的骨架现在完整得多：流式、judge、grace、自动压缩、提醒中枢都真实运行。剩下的硬缺口是"并行只读工具"和"stall/doom-loop 检测"；park/resume 仍是内存级，spillover 仍只读不写。

## 7. worker_agent — 短命的并行克隆

概念主张：worker 是 Queen loop 的克隆，带 worker prompt、Queen 工具的子集、更紧的预算、一个任务注入；专注/短命/fail-fast，不能升级、不能再委派；终态 `report_to_parent(status, summary, data)` → Queen 收到 `[WORKER_REPORT]`；有 grace iteration；通过 tracker / 自己的 task list / colony skills 共享；会话可崩溃恢复，`run_worker(resume_worker_ids=…)` 可续跑；大量工作以 headless 方式运行。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| worker 是 Queen loop 的克隆（同一 `AgentLoop`） | `internal/colony/runtime.go:864`（`runWorker`）、`:543`（`spawnBatchOn`）、`run.go:220`（`SpawnRun`）、`host.go:165`（`Host.Spawn`） | `run_worker` → `SpawnBatchWithOptions` → `spawnBatchOn` → `runGatedWorker` → `runWorker` 新建 `agentloop.AgentLoop` | 已接 | C | `SpawnBatchWithOptions` 生产可达；`SpawnBatch`（无 options）、`SpawnRun`、`Host.Spawn`/`Run` 仍无生产调用（run.go/host.go 的入口没有路由/工具使用）。 |
| worker prompt（聚焦、无升级、无委派、fail-fast 规则）真正注入 | `internal/colony/worker_definition.go:39`（`WorkerSystemPrompt`）、`:113`（`BuildSystemPrompt`）、`:169`（`BuildMeta`） | `runWorker` 用 `systemPromptFor(spec)`（`runtime.go:720`），即磁盘 `worker.json` 的 `system_prompt` | 未接 | — | `BuildSystemPrompt`/`BuildMeta` 零调用；fork 的 `worker.json` 把 `system_prompt` 写成 **Queen persona**（`loader.go:962`），worker 实际在 Queen 人格下跑；详细 worker 规则从未进入模型上下文。 |
| 工具子集 + 更紧预算（3+1 轮、30/轮、硬停 90、200 终身） | `internal/colony/runtime.go:754`（`workerTools`）、`:302`（`applyToolAllowlist`）、`internal/colony/worker_definition.go:9-16`（`DefaultWorkerLoopConfig`）、`worker_loop_config.go:46` | worker 工具 = `r.tools`（只有 builtin）+ tracker（worker 角色）+ `report_to_parent`；预算由 `BuildWorkerLoopConfig` 应用 | 部分 | C | 预算与 tiering 真生效；但 `worker.json` 的 `tools` 列表完全没被使用，`newColonyHost` 只传 builtin `toolbox.Defs()`，worker 拿不到任何 integration 工具；"Queen 工具子集"没有实现。 |
| `report_to_parent(status, summary, data)` 终态工具 | `internal/agentloop/synthetic_tools.go:279`（`BuildReportToParentTool`）、`:325`（`HandleReportToParent`）、`internal/colony/runtime.go:761`（加入 worker 工具表）、`:773`（执行器记录 report） | worker 工具表注入；executor 调 `worker.RecordExplicitReport`；`StopAfterToolBatch` 让本轮终止 | 已接 | C | `HandleReportToParent` 本身仍零调用（执行器走 `RecordExplicitReport` 等价实现）。 |
| worker 报告 → Queen 的 `[WORKER_REPORT]` 回合 | `internal/colony/runtime.go:959`（`publishReport`）、`internal/colony/queen_tools.go:457`（订阅）、`:393`（格式化）、`internal/server/chat_runtime.go:299` | 终态 `SUBAGENT_REPORT` → `FormatWorkerReport` → `InjectWorkerReport` → Queen 下一轮 | 已接 | L | 报告缺 `duration_seconds`；`output_file` 不提供；预载 colony 无 chat 时无消费者。 |
| Grace iteration：预算耗尽后仅 `report_to_parent`/`task_update`/`tracker_upsert` 可执行 | `internal/agentloop/loop.go:96`、`:529-556`、`internal/colony/worker_definition.go:9-16` + `worker_loop_config.go:46-52` | worker `GraceIterations=1`；grace 轮过滤非终态工具 | 已接 | C | `task_update` 在 worker 工具表里并不存在，grace 白名单里的这一项实际不可用。 |
| 结果共享：tracker | `internal/tracker/tools.go:1030`、`internal/colony/tracker.go:84/126`、`runtime.go:761` | worker 工具表带 `tracker_upsert/tracker_query`；executor 按 `input_data.binding` 执行 | 已接 | L | 只覆盖 worker 角色表；`tracker_sql`/`register_writable` 仍是 Queen 专用（符合概念）。 |
| 结果共享：自己的 task list | `internal/tasks/session_tools.go:747`、`internal/tasks/register.go:5` | 无 | 未接 | — | worker 工具表（`runtime.go:754`）没有 task 工具；`RegisterTaskTools` 零调用。 |
| 结果共享：colony skills | `internal/skills/skill_writer.go:169`、`internal/skills/manager.go:48` | 无 | 未接 | — | 写与读都没有入口（见 colony）。 |
| 会话崩溃恢复 + `run_worker(resume_worker_ids=…)` 续跑 | `internal/colony/resume.go:11-69`、`internal/colony/queen_tools.go:211-228`、`runtime.go:523` | `RunWorker` 收到 `resume_worker_ids` 直接返回 unsupported；worker 转录只在内存 | 未接 | — | `resume.go` 三函数零调用；没有 worker 转录落盘，也没有 resume 参数/路由。 |
| headless 运行 + Sentinel 升级人工 | `internal/sentinel/manager.go:99`、`internal/sentinel/escalation_source.go`；`cmd/hive` 只有 serve/chat/queen/colony 管理命令 | 无 | 未接 | — | 没有 headless colony run 入口；`hive serve --colony` 只起一个空转 overseer；Sentinel manager 零生产调用。 |

**小结**：worker_agent 这轮从"零生产路径"变成"主路径可用"：`run_worker` → spawn → 独立 loop → 预算/grace → `report_to_parent` → `[WORKER_REPORT]` → tracker 全通，且有 live 实测。但 worker 的**人格与工具面**是错的：fork 场景下它跑 Queen 的 `system_prompt`、拿全部 builtin 而非配置的子集、没有 task/skills、转录不落盘、resume 直接报不支持。worker 是"能跑"，还不是概念里的"聚焦、受限、可恢复的克隆"。

---

## 8. 重点核查清单（按当前 HEAD 逐项）

| 核查项 | 结论 | 依据 / 缺口 |
| --- | --- | --- |
| `run_worker` 工具 | **已接（live）** | `queen_tools.go:75/203` + `cli/session_tools.go:88/227` + `chat_runtime.go:807/824`；绑定 colony 后真实 spawn 并回传。resume 模式显式不支持。 |
| worker `SpawnBatch` / `SpawnRun` | **部分** | `SpawnBatchWithOptions` 经 `run_worker` 生产可达；`SpawnBatch`（无 options）、`SpawnRun`、`Host.Spawn`/`Host.Run` 仍零生产调用。 |
| `[WORKER_REPORT]` 注入 | **已接（live）** | `queen_tools.go:457` 订阅 + `chat_runtime.go:299` 注入；报告作为非 client user turn 送达 Queen。 |
| `tracker_*` 工具（queen / worker） | **已接（live）** | queen 4 个工具（`session_tools.go:95-115`）、worker 2 个（`colony/tracker.go:84`）；均按 binding 执行。 |
| tracker binding | **已接（live）** | `tracker/binding.go:59` 建库 + `colony/tracker.go:147` 注入 `input_data` + `session_tools.go:236`/`worker.go:377` 解析；无 binding 返回 `ErrNoBinding`。 |
| 提醒 sources 与 `Hub.Start` | **已接** | `chat_reminders.go:54-80` 注册 6 个 source 并 `Hub.Start`；SESSION_START/USER_PROMPT_SUBMIT/POST_TOOL_USE/PRE+POST_COMPACT/STOP/IDLE_TICK/TOOL_BUDGET_CHECKPOINT 均触发。缺口：STREAM_STALLED 未注册、skills catalog 无数据、REST surface 与 chat hub 脱节。 |
| judge / LLM adapter 与 `judge_verdict` | **已接（colony worker 路径）** | `judge.go:92` + `colony/runtime.go:898/950` + `eventbus/emit.go:363`；带 criteria 的 worker 会评估并发 `judge_verdict`。chat Queen 仍 judge-free（无 criteria 来源）。 |
| `StreamCompleter` 与 `client_output_delta` | **已接（live）** | `llm/stream.go:33` + `stream_openai.go:42` + `chat_stream.go:113` + `chat_runtime.go:618/633`；OpenAI-compatible 逐帧；Anthropic/MiniMax 回退。 |
| `ask_user` / task 工具 | **已接** | `synthetic_tools.go:94` + `chat_runtime.go:785/964`；`task_*` 在 `cli/session_tools.go:70/78`；均只有 chat Queen，worker/overseer 没有。 |
| colony load / unload / `CreateColonyFromSource` | **已接** | `loader.go:722/741/795` + 路由 + chat 绑定（`session.go:395/419`）；失败按 4xx/5xx 上报。 |
| Queen persona / memory / resume | **已接** | `chat_runtime.go:937` persona、`:211/535` reflection、`:704` recall、`loader_session_resume.go:38` resume；colony-bound 会话不支持 resume。 |
| `Sentinel.NewManager` | **仍未接** | 零生产调用；只有配置/测试路由与 store/notifier 适配器。 |
| `skills.WriteSkill` | **仍未接** | 零生产调用；`SkillsProvider` 未注入，`SkillDirs` 未赋值。 |
| triggers 恢复 / `QueenExecutor` | **仍未接** | `QueenExecutor` 恒 nil；`StartTriggerTimer`、`injectQueenEvent`、`dismiss-colony-pivot`（`routes_session_colony.go:209`）都拿不到 Queen node。 |
| playbook runner | **仍未接** | `internal/playbook` 生产零 import。 |
| `resume_worker_ids` | **仍未接** | `queen_tools.go:223` 显式返回 unsupported；worker 转录在内存。 |

## 9. 总体结论

- **真正接通的**：chat Queen 的完整 loop（同步/流式）、`run_worker` + worker spawn + `[WORKER_REPORT]`、tracker 建库/binding/读写、提醒中枢（6 source + ticker + 主要注入点）、judge（colony worker + criteria）、自动压缩、任务计划、persona/记忆/恢复、colony load/unload/fork、`ask_user`、`serve --colony` 启动。
- **最大的结构性断点**：colony 里的 overseer 仍是影子 Queen——工具面只有 builtin，没有 `run_worker`/tracker/task，跑一轮后阻塞；`serve --colony` 预载会话又没有 `ChatRuntime`，既不能 chat 也不能派活。真正能 fan-out 的是"另建/load/fork 出来的 chat Queen + 绑定 colony"。
- **worker 的能力边界**：预算、grace、report、tracker 都对了；但 fork 出来的 worker 跑的是 Queen 的 system prompt，工具面是全量 builtin（不是配置子集，也没有 integration），没有 task/skills，转录不落盘，resume 明确不支持。因此"短命、聚焦、可恢复的克隆"只完成了"短命"。
- **"代码在但没接"的典型**：Sentinel、`skills.WriteSkill`、triggers/`QueenExecutor`、playbook、`resume_worker_ids`、`search_messages`、`stall_check`、并行只读工具。
- **口径提醒**：本审计只回答"代码能否从生产入口走到、走到了是否生效"，不评价上游 Python 的行为；`L` 只表示交付侧有实测记录，本轮没有重跑；`C` 是本轮静态追踪，不等于行为已经与上游逐项对齐。

## 10. 与上一版差异

上一版基线 `af55d368` 的计数为 25 已接 / 12 部分 / 42 未接（79 行）。本版为 **48 / 10 / 22（80 行）**。行数 +1 是 goals_outcome 新增了"success_criteria → AgentSpec 编译"这一独立判定行；其余 79 行沿用，逐条迁移如下。

### 10.1 未接 → 已接（18 条）

| # | 条目 | 依据 |
| --- | --- | --- |
| 1 | colony：Queen 用 `run_worker` 扩展 | `queen_tools.go:203` + `session_tools.go:88/227` + `chat_runtime.go:807/824`；live 实测 |
| 2 | coordination：Queen 声明可写列 / worker upsert / SQL | `tracker/tools.go:1030` + `session_tools.go:95-115/234-247` + `colony/tracker.go:84/126` |
| 3 | coordination：tracker binding | `tracker/binding.go:59` + `colony/tracker.go:147/190` + `session_tools.go:236` |
| 4 | coordination：worker 报告 → `[WORKER_REPORT]` | `queen_tools.go:457/393` + `chat_runtime.go:299/841` |
| 5 | goals：loop judge accept/retry/escalate | `judge.go:92/109/127` + `colony/runtime.go:898/950` |
| 6 | goals：retry 反馈回注 | `judge.go:198` + `loop.go:626/784` |
| 7 | goals：judge 小流水线 / LLM adapter | `llm_judge.go:57` + `llmjudge/judge.go:111` + `emit.go:363` |
| 8 | goals：tracker 列作为 done + SQL 校验 | `tracker/tools.go:156/633` + `session_tools.go:234` |
| 9 | improvement：reflexion 第二层（judge retry） | 同 5/6 |
| 10 | queen：Queen 拥有 tracker | 同 2/3 |
| 11 | the_loop：流式输出 | `stream_openai.go:42` + `chat_stream.go:113` + `chat_runtime.go:618/633`；live 实测 |
| 12 | the_loop：judge | 同 5 |
| 13 | the_loop：grace iteration | `loop.go:96/529-556` + `worker_definition.go:9-16` + `worker_loop_config.go:46-52`，经 `run_worker` 可达 |
| 14 | worker_agent：worker 是 loop 克隆 | `runtime.go:864/543`，经 `run_worker` 可达 |
| 15 | worker_agent：`report_to_parent` | `synthetic_tools.go:279` + `runtime.go:761/773` |
| 16 | worker_agent：报告 → Queen | 同 4 |
| 17 | worker_agent：grace | 同 13 |
| 18 | worker_agent：tracker 共享 | 同 2/3 |

### 10.2 未接 → 部分（2 条）

| # | 条目 | 依据 |
| --- | --- | --- |
| 1 | coordination：提醒内容（fleet / tracker / tools / skills） | `chat_reminders.go:54` 注册了 active_workers / colony_parallel / searchable_tools / skills_catalog / task，但缺 tracker 快照 source，skills catalog provider 恒空 |
| 2 | worker_agent：工具子集 + 更紧预算 | 预算/tiering 生效；`worker.json` 的 `tools` 未应用，worker 只拿 builtin + tracker + report |

### 10.3 部分 → 已接（4 条）

| # | 条目 | 依据 |
| --- | --- | --- |
| 1 | colony：tracker 作为共享账本 | `Start` 建库（`colony/tracker.go:61`）+ worker 写入 + REST 读，三面齐了 |
| 2 | coordination：tracker 读侧 | 读 + UI 行编辑 + 全局数据路由；不再是"只有读" |
| 3 | coordination：提醒中枢注入点 | 6 source + `Hub.Start` + 7 个触发点，不再只有一个 source 两个点 |
| 4 | the_loop：框架注入提醒 | 同上 |

### 10.4 新增判定行（1 条）

| 条目 | 状态 | 说明 |
| --- | --- | --- |
| goals：success_criteria → `AgentSpec` 编译 | 已接 | `agent_spec.go:439-441` + `criteria.go:12`；上一版把这段并在 judge 行里，这版单独判定，因为它是 judge 能运行的前提 |

### 10.5 本轮新发现 / 变化了的缺口

| 缺口 | 说明 |
| --- | --- |
| `serve --colony` 预载会话没有 `ChatRuntime` | `loader.go:305-321` 只建 colony；`chat_routes.go:115` 对 `Chat == nil` 直接 503，所以预载 colony 不能 chat、不能 `run_worker` |
| colony overseer 工具面只有 builtin | `loader.go:457-464` 传 `toolbox.Defs()`；`runtime.go:754-767` 只对 `worker:` 流追加 tracker/report，overseer 没有 `run_worker`/tracker/task |
| fork 的 worker 继承 Queen persona | `loader.go:962` 把 `worker.json.system_prompt` 写成 `queenOverseerSpec(...).SystemPrompt`；`runWorker` 用 `systemPromptFor(spec)`，worker 实际按 Queen 人格运行 |
| worker 工具不是配置子集 | `applyToolAllowlist` 只过滤 MCP（`runtime.go:302`），`worker.json.tools` 未参与；worker 没有 integration 工具 |
| tracker 工具在无 colony 会话仍可见 | `session_tools.go:95-115` 无条件把 4 个工具加入 defs；执行时才返回 `ErrNoBinding` |
| REST reminders 恒空 | loader 仍把 `serveReminders` 传给 AppDeps（`loader.go:329`），但 chat 不再 `TurnComplete`；`GET /api/sessions/{id}/reminders` 读不到东西 |
| `[WORKER_REPORT]` 缺 duration | `runtime.go:959-972` 的 payload 没有 `duration_seconds`；`queen_tools.go:417` 有读无值 |
| `grace` 白名单含不可用工具 | `loop.go:96-100` 允许 `task_update`，但 worker 工具表没有 task 工具，该项实际不可达 |
| colony-bound 会话不能 resume | `loader_session_resume.go:69-73` 明确只对非 colony 会话按 id 恢复 |

### 10.6 仍未接、原因已变 / 未变

| 条目 | 上一版原因 | 当前原因 |
| --- | --- | --- |
| Sentinel | 无 manager 调用 | 仍无；只有配置/适配器 |
| skills（write_skill） | 无调用 | 仍无；`SkillsProvider` 也未注入 |
| triggers 定时唤醒 | 依赖 `QueenExecutor` | 仍依赖，且 `QueenExecutor` 生产零赋值；`dismiss-colony-pivot` 同样拿不到 Queen node |
| playbook | 无 import | 仍无；`run_playbook` 只有提示文案 |
| `resume_worker_ids` | 无参数 | 现在有参数并显式返回 unsupported（`queen_tools.go:223`）；worker 转录在内存 |
| worker 详细 prompt | `BuildSystemPrompt` 零调用 | 仍零调用；fork 还让 worker 用上 Queen persona，问题更具体了 |
| stall / doom-loop | 仅常量 | 仍仅常量；`SubscribeStallCheck` 零调用 |
| 并行只读工具 | 顺序执行 | 仍顺序执行；`ConcurrencySafe` 仍未消费 |
| park 落盘/resume | 仅内存 | 仍仅内存；只多了 idle nudge 对 parkReason 的读取 |
