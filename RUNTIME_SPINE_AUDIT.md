# Go 运行时 × Runtime Spine 八层接线审计

- 审计基线：`ab5c3dc7`（`release: 1.0.24-go.1（接线收尾 + Queen 两阶段/overseer 桥）`，Go 1.0.24；相对 `d650199d` 合并 credential 提交、预载 catalog、doom reason、judge criteria、两阶段落盘与 overseer 消息桥）
- 上一版基线：`d650199d`（`release: 1.0.23-go.1（提醒/持久性/loop 保护）`；67 已接 / 3 部分 / 0 未接，70 行）
- 更早基线：`6723ab78`（63/7/0）；`c444c192`（61/9/0）；`4b6f1cf4`（50/10/10）；`4daf4222`（汇总表 30 已接 / 12 部分 / 28 未接；按行逐条重算为 32 / 11 / 27，见 §1 注）
- 审计日期：2026-11-19
- 本轮增量：复核 `d650199d..ab5c3dc7` 落地的 1.0.24 收尾——credential-form 提交（加密 store + 清 park + 恢复）、预载会话 `SkillsCatalog`、chat 失败分支保留 doom-loop park reason、chat Queen/overseer 继承 colony goal criteria、Queen 两阶段 phase 落 `meta.json` 并把 Tool Library 的 PATCH 接到 live 会话工具集、绑定 colony 的客户端消息/报告/trigger 单进 live overseer 且回复以 queen stream 呈现。
- 审计对象：`runtime_spine.md` 的八层栈；Go 模块根为 `go_hive/go-hive/`
- 分层依据说明：`runtime_spine.md` 仍未入库，本审计与上一版一样按既有文本核对八层定义，不把该文件计入提交。
- 判定口径（严格）：
  - **已接**＝从 `cmd/hive` → `internal/cli` 或 `internal/server` 的 HTTP 路由 / 会话工具 / agent loop 真实可达，且在生产路径上生效；仅测试调用、仅枚举、仅提示文案不算。
  - **部分**＝有真实入口但只完成一半：只读不写、只写配置不生效、只覆盖一个角色/阶段、只在内存不落盘，或一个合并机制里只有一种生效。
  - **未接**＝代码存在但生产入口走不到（死代码 / 仅测试 / 只有类型、枚举或提示文案）。
  - 管理配置路由（MCP、Sentinel 通知、Soul 编辑）本身可达不等于其运行时机制已接；MCP 与 Sentinel 的运行时装配已在早前版本补齐，本轮仍按"运行时是否真的跑"判定，而不是按配置路由计分。
- 证据口径：
  - `L`＝交付侧既有 live 实测记录，出处 `go_hive/docs/support-matrix.md`（本轮只读审计，未重跑）。该表随 1.0.24 只更新版本号与测试计数，不覆盖 skill 工具、run_playbook、trigger/Sentinel、MCP 白名单、soul、检索、并行只读、park 落盘，以及本轮的 credential 提交、预载 catalog、doom park reason、judge criteria、两阶段 phase、overseer 中继。
  - `C`＝本轮从 `cmd/hive` 逐段静态追踪，并用 `go build ./...` 与聚焦测试复核；测试本身不计入接线判定。
- 说明：只读审计，不改产品代码、不改测试、不改 `go.mod`。下表路径均相对 Go 模块根 `go_hive/go-hive/`；行号以 `ab5c3dc7` 为准；本轮改写的行号已复核，未改写行沿用上一版（以符号名为准）。

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
| 1. 执行（The Loop） | 10 | 0 | 0 | 10 |
| 2. 组织（Colony / Queen / Worker） | 10 | 0 | 0 | 10 |
| 3. 状态（Plan / Tracker） | 10 | 0 | 0 | 10 |
| 4. 信号（Event Bus / Reminder Hub） | 8 | 0 | 0 | 8 |
| 5. 方法（Skill / Playbook） | 7 | 0 | 0 | 7 |
| 6. 自治（Trigger / Sentinel） | 7 | 0 | 0 | 7 |
| 7. 沉淀（Memory / Soul） | 7 | 0 | 0 | 7 |
| 8. 能力边界（Tools / MCP / Credentials / Budgets） | 11 | 0 | 0 | 11 |
| **合计** | **70** | **0** | **0** | **70** |

注：`4b6f1cf4` 版按行级口径为 **50/10/10**；`c444c192` 版为 **61/9/0**；`6723ab78` 版为 **63/7/0**；`d650199d` 版为 **67/3/0**。本版为 **70/0/0**，相对 `d650199d` 是 **+3 已接、-3 部分**（第 1 层 judge、第 2 层 overseer、第 8 层凭据），未接仍为 0。

读法：这一轮补的是最后三个"部分"的收口，而不是新层。第 1 层把 chat/overseer 的 judge criteria 与 credential-form 提交接上，doom-loop park reason 不再被覆盖；第 2 层让 live overseer 成为唯一执行 lead，chat Queen 退为前端；第 8 层补上凭据表单的安全写回。剩余是已知差异（两份 conversation、catalog 作用域、MiniMax/Python playbook/81 模块），不再是接线断点。

---

## 2. 分层明细

### 第 1 层：执行（The Loop）

概念主张：一个多轮流式 LLM 会话循环；每轮流式输出、并行跑只读工具、回注结果、决定停止/继续；judge 自纠错；Queen/worker 只是一组 `LoopConfig` 差异；内建崩溃恢复、上下文压缩、计量与提醒。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 单一多轮 agent loop（Queen 与 worker 共用） | `internal/agentloop/loop.go:478`（`ExecuteTurn`）；`internal/server/chat_runtime.go:271/479`；`internal/colony/runtime.go:982/1024` | chat：`NewChatRuntime` → `runTurn` → `loop.ExecuteTurn`；worker：`run_worker` → `spawnBatchOn` → `runGatedWorker` → `runWorker` → 同一 `AgentLoop` | 已接 | C | worker 与 overseer 都经 `newWorkerAgentLoop` → `NewAgentLoop`（`internal/colony/worker_reminders.go:114`），因此也有 `Hub`（见第 4 层）；差别只在 `LoopConfig` |
| 流式输出逐帧推 UI | `internal/server/chat_stream.go:43`；`internal/server/chat_runtime.go:496/699/709`；`internal/llm/stream_openai.go:42` | CLI provider 实现 `StreamCompleter` → `newChatStreamingCompleter` → `StreamTextDelta` → `client_output_delta` → session SSE | 已接 | L | MiniMax 明确不支持流式并回退非流式；Anthropic 已实现 Messages SSE（`internal/llm/stream_openai.go:67` → `stream_anthropic.go:48`），tool-call 参数按 `input_json_delta` 聚合，不逐片下发 |
| 只读工具并行执行 | `internal/agentloop/loop.go:61`（`ParallelReadOnlyTools`）、`:88`（默认 true）、`:878`（`executeToolBatch`）、`:895`（`allToolsConcurrencySafe`） | 工具批内整批显式 `ConcurrencySafe` 才并发；否则沿用顺序执行 | 已接 | C | 未声明 `ConcurrencySafe` 的工具继续串行；事件仍按原顺序发 |
| 工具结果回注、循环直到不再调用工具 | `internal/agentloop/loop.go:614`、`:756`（`AddToolResult`） | `ChatRuntime.runTurn` / `runWorker` 都走该路径 | 已接 | C | — |
| judge：accept / retry / escalate | `internal/agentloop/judge.go:92/109/127`；`internal/colony/runtime.go:1078`（worker）/`:1160-1167`（overseer）；`internal/server/chat_runtime.go:321/1061/1077-1089` | worker 构造 loop 后 `ConfigureJudge`；spec 无 criteria 时用 goal 的 criterion 描述回填（`runtime.go:1073-1077`、`criteria.go:37`）；overseer 无 spec criteria 时继承 colony goal（`:1160-1167`）；chat runtime 有 `ChatRuntimeDeps.SuccessCriteria` 或绑定 colony 的 goal 时 `ConfigureJudge`（`chat_runtime.go:321/1077-1089`）；RETRY 回注 `[Judge feedback]` 用户消息 | 已接 | C | plain chat（无 criteria、无 bound goal）仍 `Judge == nil`；绑定 colony 且有 live overseer 时 chat Queen 不执行 model turn，评估发生在 overseer 的 loop；`GoalCriteriaText` 只取描述，不取权重。 |
| worker grace + 更紧预算 | `internal/colony/worker_definition.go:9`；`internal/colony/worker_loop_config.go:46`；`internal/agentloop/loop.go:529-556` | `run_worker` → `BuildWorkerLoopConfig` 应用 3 轮 + 1 grace / 每轮 30 / 硬停 90 / 终身 200；grace 轮只允许 terminal tools | 已接 | C | `task_update` 现在可达（worker 自带 task 工具），三项终态工具均可用 |
| 崩溃恢复：transcript cursor + 会话恢复 | `internal/agentloop/conversation.go:1126/1213`；`internal/server/chat_runtime.go:255`；worker 侧 `colony/worker_transcript.go:40`、`resume_workers.go:38`、`worker_state.go:78/226`、`colony/runtime.go:523`；预载 `loader_preloaded_session.go:53/62` | chat 每条消息 `persist`；`NewChatRuntime` 恢复 parts；worker transcript/state 落盘，`ColonyRuntime.Start` 自动续跑崩溃时非终态 worker；预载会话按 `queen_resume_from` 复用旧 id | 已接 | C | 自动续跑只覆盖崩溃时 queued/running（completed/failed/stopped 不复活）；`POST /api/sessions` 带 colony_id 仍不走 `queen_resume_from`；自定义 report status 未归一为终态时会被当作可续跑（待验证）。 |
| park / resume（`ask_user` 停车） | `internal/server/chat_park.go:29/89/116`；`internal/server/chat_runtime.go:228/1137/1286`、`:1078/1127/1141`；`internal/server/chat_credential_park.go:43/68/77/90`；`internal/server/routes_credentials_form.go:36`；`internal/agentloop/park_reason.go:16` | `ask_user` / 缺凭据 → `pendingQuestions`/`pendingCredentialForm` + `client_input_requested` / `client_credential_form_requested`；park 时把 `pending_input` 写进 `cursor.json`，`NewChatRuntime` 恢复等待，用户消息/trigger 清除；`POST /api/sessions/{id}/credential-form` 校验 pending form → 写加密 store → 同步 env → 清 park → `InjectEvent` 恢复（`cancelled` 只清 park） | 已接 | C | 人类回答与 credential_form 都能落盘/恢复；提交只服务 chat runtime 的 pending form（缺凭据工具错误的生产者），overseer 执行的会话不会产生该 form，overseer 的 park 状态也仍只在内存。 |
| 自动上下文压缩 / 工具结果 spillover | `internal/agentloop/loop.go:320`（`compactConversationIfNeeded`）、`:224`（`restoreSpillCounter`）、`:60-74`（`MaxToolResultChars`/`SpilloverDir`）、`:1011/1021/1023`（每次工具返回经 spillover）；`internal/agentloop/conversation.go:736/931`；`internal/agentloop/llm_compact.go:22`；`internal/agentloop/tool_result_spillover.go:73/79/117`；`internal/server/chat_memory_wiring.go:17-21`；`internal/colony/worker_loop_config.go:65/122` | 每轮 LLM 调用前 `NeedsCompaction()` → `LlmCompact` → `conv.Compact`，触发 PRE/POST_COMPACT；非错误工具结果超过 30k 时写盘，消息替换为带 `Full result at:` 的 5000 字符预览并置 `SpilloverPath`；chat 写 `<session>/data`，worker 写 `cache/worker-tool-results/<pid-seq>` | 已接 | C | worker spill 目录由 `hivehome.Resolve()`（进程 `HIVE_HOME`）决定，不是 loader 的 `HomeRoot`；自定义 `Runner.HomeRoot` 下实际路径需实测（待验证）。 |
| 每次 LLM 调用计量（tokens/cost） | `internal/agentloop/loop.go:595`；`internal/server/chat_runtime.go:713/721` | `LLMTurnComplete` → `chatTurnEmitter` → `llm_turn_complete`（input/output/cached/cost） | 已接 | C | — |

**小结**：执行层骨架完整，流式（含 Anthropic SSE）、并行只读、工具回注、judge（worker + overseer + 有 criteria 的 chat Queen）、grace、压缩、计量都真实运行；worker transcript/运行态落盘让崩溃恢复覆盖 chat 与 colony，stall 已在 `AddSession` 订阅并发布 `node_stalled`，doom-loop 达到 5 次即停车并发 `node_tool_doom_loop`（park reason 不再被覆盖），Queen 30/50 与 worker spillover 也已生效；credential-form 提交补上加密写入、清 park 与恢复。剩余是作用域与已知边界：plain chat 无 judge、overseer 执行时 chat Queen 不跑 model turn、overseer park 不共享、MiniMax 非流式。

---

### 第 2 层：组织（Colony / Queen / Worker）

概念主张：colony 是单目录、可移植、可定时唤醒、跨会话持久的部署单元；Queen 与 worker 是同一个 loop；Queen 通过 `run_worker` 在运行时按需扩展；Create Colony 对话框确认后才 fork。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Colony 单目录 + scaffold | `internal/server/routes_colonies.go:409`、`:440`、`:474`；`internal/server/colony_scaffold.go:31`；`internal/hivehome/home.go:88/129` | `POST /api/colonies/{colony_id}/scaffold` → `ensureMinimalColony` → 写 `metadata.json`/`worker.json`，并建 `skills/`、`runs/`、空 `triggers.json`（幂等） | 已接 | C | fork（`cli/colony_worker_definition.go:31`）仍只写两个 JSON；`tracker/` 由 runtime `Start` 补建（`colony/tracker.go:61`） |
| Colony 导入 / tar 可移植 | `internal/server/routes_colonies_import.go:705` | `POST /api/colonies/import*` → 落盘 `home.ColoniesDir()` | 已接 | C | — |
| Colony load / unload | `internal/cli/loader.go:727/746/767`；`internal/server/routes_session_colony.go:55/170` | `POST/DELETE /api/sessions/{id}/colony` → `deps.Colony` → `newColonyHost` + `startColonyHost` | 已接 | C | unload 后 chat 侧绑定要等下一次 `GetSession` 才解除；在跑的 turn 仍可能持有旧 runtime |
| `serve --colony` 启动即加载 runtime、overseer 与 chat | `internal/cli/loader.go:418/469`；`internal/cli/loader_preloaded_session.go:33/53/62/91/199` | `hive serve --colony <path>` → `resolvePreloadedColonySession`（metadata `queen_session_id`）→ host `Start` + `StartOverseer` → 有 provider 时 `ChatRuntime` + `QueenExecutor` → `RestoreSessionTriggers` → `AddSession` | 已接 | C | 旧 `L` 只覆盖加载；预载会话现在复用旧 id/转录（首次启动才新建），并注入 `SkillsCatalog`（`loader_preloaded_session.go:199`）；预载 `QueenName` 为空 → catalog 走 default queen scope；无 provider 时 `Chat` 为 nil，`POST /chat` 503 |
| Queen chat 会话 + persona | `internal/cli/loader.go:536`；`internal/server/chat_runtime.go:164/1030` | `POST /api/sessions` → `createSessionWithRuntime` → `NewChatRuntime` → `IdentityPrompt` | 已接 | C | `default` queen 无 profile 时回退通用身份（预期） |
| Queen 用 `run_worker` 运行时扩展 | `internal/colony/queen_tools.go:75/204`；`internal/cli/session_tools.go:111/258`；`internal/server/chat_runtime.go:879/896` | 会话工具表常驻 `run_worker`；`toolsForTurn` 在未绑 colony 时过滤；绑定后 `SpawnBatchWithOptions` 并启动软/硬超时看门狗 | 已接 | L | 预载 `serve --colony` 会话现在也可派活；`resume_worker_ids` 已实现（见 worker 行） |
| Worker 是同一 loop + 终态报告 | `internal/colony/runtime.go:982/1118`；`internal/colony/queen_tools.go:492`；`internal/server/chat_runtime.go:315/942/1122` | `spawnBatchOn` → `runGatedWorker` → `runWorker` → `publishReport` → `SUBAGENT_REPORT` → 有 live overseer 时只投 overseer；overseer 自身报告回 chat 前端成 `[WORKER_REPORT]` | 已接 | L | 报告已有 duration/output_file；预载 colony 无 chat runtime 时无人消费；非 overseer worker 的 client IO/tool/token 不镜像 |
| Colony overseer 是 client-facing Queen | `internal/colony/runtime.go:694/1137`；`internal/colony/overseer_tools.go:112`；`internal/cli/loader.go:484`；`internal/server/chat_colony_bridge.go:187/293/302`（`relayClientInputToOverseer`/报告/trigger）、`:139`（`mirrorColonyEvent`）；`internal/colony/session_bridge.go:66`（`InjectOverseer`）；`internal/colony/overseer_emitter.go:63` | `StartOverseer` → `runOverseer`：先跑一轮 seed，然后 `wakeCh` 醒来消费注入输入，用 `overseerToolDefs`（`run_worker` + queen tracker + task）；绑定 colony 的 chat runtime 把客户端消息、worker 报告与 trigger 都转投它；overseer 的文本/执行事件以 queen stream 镜像 | 已接 | C | 操作面已是单执行 lead；仍两份 conversation（overseer 回复不写回 chat transcript），chat 的 pending/park 不由 overseer 更新，overseer 无 `ask_user`；中继失败回退 chat Queen |
| Worker 是"聚焦克隆"：worker prompt + 配置的工具子集 | `internal/colony/worker_definition.go:39/113`；`internal/cli/colony_worker_definition.go:58/92`；`internal/colony/worker_tool_selection.go:28`；`internal/colony/runtime.go:856` | fork 写 worker `system_prompt` 与 `tools`；`runWorker` 用 `systemPromptFor(spec)`；`baseWorkerTools` 把声明项从 integration/MCP 池展开 | 已接 | L | builtin base 始终保留；空 `tools` 只剩 builtin；193ace3d 之前 fork 的旧 worker.json 仍是 Queen persona |
| Worker 崩溃恢复 / `resume_worker_ids` 续跑 | `internal/colony/resume_workers.go:38`；`internal/colony/worker_state.go:78/226`；`internal/colony/worker_transcript.go:40`；`internal/colony/runtime.go:523`；`internal/colony/queen_tools.go:231-280` | transcript 落盘 `<colony>/workers/<id>/conversations`；运行态写 `runs/<id>.json` + `state.json`；`Start` 自动续跑崩溃时非终态 worker；`run_worker(resume_worker_ids=…)` → `ResumeWorkers` 恢复同一 conversation（可带 guidance）→ 继续 loop；逐 id 返回失败 | 已接 | C | 自动续跑只覆盖 queued/running（终态不复活）；活动中的 worker 拒绝手动 resume；无 meta.json 的旧 worker 不能 resume；resume 后预算 pinned，不做自适应收紧 |

**小结**：组织层的"可部署、可加载、可派活、可恢复"都通了：scaffold 产出完整持久目录，`serve --colony` 既是真 colony 又是复用旧会话的真 chat session（并注入 skills catalog），worker 有身份、工具面、运行态与 resume。1.0.24 把 overseer 变成客户端消息/报告/trigger 的唯一执行 lead，chat Queen 退为前端；结构性残留是两份 conversation 与不共享的 pending/park，而不是输入断点。

---

### 第 3 层：状态（Plan / Tracker）

概念主张：Plan 是 Queen 的意图（`tasks.json`，Action Plan 面板）；Tracker 是 colony 的事实（`tracker/tracker.db`，每个工作单元一行）。两者不能混。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Plan 文件存储：`tasks.json` 创建 / 更新 / 读取 | `internal/tasks/store.go:159/244`；`internal/tasks/session_tools.go:747`；`internal/cli/task_tools.go:47` | 会话创建 → `newSessionToolCatalogue` → `newTaskToolSet(sessionDir)` → `task_create/task_update/task_list/task_get` → `TaskStore` 写 `tasks.json` | 已接 | C | chat Queen、overseer 与 worker 各有自己的 store；worker 的列表不汇总到 Queen |
| Plan REST 快照 / 归档清理 | `internal/server/routes_tasks.go:37`；`internal/server/app.go:166` | `GET /api/sessions/{id}/tasks`、`POST .../tasks/unarchive`、`POST .../tasks/clear-completed` | 已接 | C | 只有读快照和两个归档动作；没有 REST 创建/编辑（由 agent 工具写） |
| Plan 跨 reload 可见 | `internal/tasks/store.go:106/174`；`internal/cli/task_tools.go:60` | 同一 session 目录重建工具集，读同一 `tasks.json`；`newTaskToolSet` 先 `MkdirAll(root)` | 已接 | C | `serve --colony` 预载会话可复用旧 id；`POST /api/sessions` 带 colony_id 的普通创建仍不走 `queen_resume_from`；session 目录迁移时 `sessionDirCache` 可能需重启刷新 |
| Worker 自己的 task list | `internal/colony/worker_task_tools.go:42/77`；`internal/colony/runtime.go:853/889/916` | worker 工具表追加 4 个 task 定义；executor 用 worker 自己的目录 `<workerDir>/_misc/<id>/tasks.json` | 已接 | C | 列表只属于 worker；Queen 不轮询、不汇总 |
| Colony overseer 的 task list | `internal/colony/overseer_tools.go:51/112`；`internal/colony/runtime.go:218` | `newOverseerTaskSet(storage)` 建在 `<colony>/_misc/overseer/tasks.json`；`overseerToolDefs` 追加 4 个 task 工具 | 已接 | C | 作用域是独立的 `overseer` 会话，不与 chat Queen 共享计划 |
| Tracker 建库 + binding | `internal/colony/tracker.go:61`；`internal/tracker/binding.go:67`；`internal/colony/runtime.go:489/563` | colony `Start` / 每次 spawn → `provisionTracker` → `BindingForColonyDir` → `tracker/tracker.db` | 已接 | L | 只对含 `metadata.json` 的目录建库 |
| Tracker binding 注入 worker | `internal/colony/tracker.go:137`；`internal/colony/runtime.go:606`；`internal/colony/worker.go:377` | spawn → `workerInputData` 带 binding → `BuildWorker` 设 `ColonyBindingProvider` | 已接 | L | `RuntimeOptions.Binding` 没有生产注入者（只有测试/宿主自备） |
| Queen tracker 工具（SQL / 声明可写列 / upsert / query） | `internal/tracker/tools.go:1030`；`internal/cli/session_tools.go:118-137/265-279` | 会话工具表常驻 4 个 tracker 工具；执行时 `TrackerBindingFromContext` 取绑定，无绑定返回 `ErrNoBinding` | 已接 | L | 未绑 colony 时工具仍可见但不可用 |
| Worker tracker 工具（upsert / query） | `internal/colony/tracker.go:109/126`；`internal/colony/runtime.go:849` | worker 工具表追加 role=worker 表；`executorFor` 用 `workerTrackerRegistry` | 已接 | L | 只有 `tracker_upsert/tracker_query`；无 SQL（符合概念） |
| Tracker REST 读表 / 读行 / 改行 | `internal/server/routes_colony_workers_data.go:913-922` | `GET /api/colonies/{id}/data/tables`、`.../tables/{table}/rows`、`PATCH .../rows` | 已接 | C | 这三个路由无 insert/delete（全局数据路由另有） |

**小结**：状态层已无未接项：worker 有了自己的 task list，overseer 也有独立的 Plan；Tracker 的建库、binding、Queen/worker/overseer 三侧工具与 REST 全部接通。

---

### 第 4 层：信号（Event Bus / Reminder Hub）

概念主张：Event bus 传瞬时信号（worker 报告、实时 transcript、SSE）；Reminder hub 在已知生命周期点注入短提示；二者都不是 tracker，也不是 memory。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| 会话实时 transcript → SSE → UI | `internal/server/chat_runtime.go:629-709`；`internal/server/chat_routes.go:168`；`internal/eventbus/bus.go:154/415` | 每轮 emit execution / loop / assistant / tool / usage → session `EventBus` → `GET /api/sessions/{id}/events`；同时写 `events.jsonl` | 已接 | L | — |
| Worker 终态报告 → Queen 的 `[WORKER_REPORT]` | `internal/colony/runtime.go:1118/1200`；`internal/colony/queen_tools.go:492/426`；`internal/server/chat_runtime.go:315/942` | 终态 `SUBAGENT_REPORT` → `FormatWorkerReport` → `InjectWorkerReport` → 下一轮以非 client user message 注入 | 已接 | L | 预载 colony 无 chat 时无人消费；报告同时转投 overseer（`chat_runtime.go:982`） |
| Colony 事件 → 会话 / UI SSE | `internal/server/chat_colony_bridge.go:44`（`syncColonyEventBridge`）、`:72`（订阅并转投 session bus）；`internal/server/chat_runtime.go:950`（`BindColony` 挂桥） | 绑定 colony 的 chat runtime 订阅 colony bus 的 execution/worker/report 事件并 publish 到 session bus；UI 经 `GET /api/sessions/{id}/events` 看到 worker 生命周期 | 已接 | C | 没有独立的 `/api/colonies/{id}/events` 路由；只在有 chat 会话绑定时可见 |
| Reminder Hub：7 个 source 注册 + `Hub.Start` | `internal/server/chat_reminders.go:69/85-96`；`internal/agentloop/reminder_hub.go:235` | `NewChatRuntime` → register task / colony_parallel / active_workers / tracker_snapshot / searchable_tools / skills_catalog / idle + Sentinel escalation → `Bind` → `Hub.Start`（idle ticker） | 已接 | L | chat、worker、overseer 三个 loop 都有 Hub；worker/overseer 只注册 task + idle 两个 source，没有 chat 的 fleet/tracker/skills 源 |
| Reminder 触发点 | `internal/server/chat_runtime.go:577/584/643`；`internal/agentloop/loop.go:355/414/579/806/887` | SESSION_START / USER_PROMPT_SUBMIT / POST_TOOL_USE / PRE_COMPACT / POST_COMPACT / STOP / IDLE_TICK / TOOL_BUDGET_CHECKPOINT | 已接 | C | `STREAM_STALLED` 没有 source 注册；chat 侧 `SESSION_START` 可能在 runtime 和 loop 各触发一次（重复注入风险） |
| Reminder 内容供给（fleet / task / tools / skills / tracker） | `internal/server/chat_reminders.go:26-64/88-90`；`internal/agentloop/active_workers_reminder.go:27`；`internal/agentloop/colony_parallel_nudge.go:54`；`internal/agentloop/tool_skill_reminders.go:23`；`internal/agentloop/tracker_snapshot_reminder.go:105/135`；`internal/server/chat_tool_tiers.go:43/153`；`internal/cli/loader_reminders.go:26/30` | 注册 task、colony_parallel、active_workers、tracker_snapshot、searchable_tools、skills_catalog、idle；provider 从 live session 读；tracker provider 取 bound runtime 的 `TrackerBinding`，skills provider 从 queen 技能目录加载，searchable provider 从 chat MCP tier 取 | 已接 | C | 预载 `serve --colony` 已注入 `SkillsCatalog`（`loader_preloaded_session.go:199`），但固定 default queen scope；tracker source 只在绑定 colony 后触发；无 MCP 时 chat searchable tier 为 nil（无工具可搜，预期）；overseer 执行时 chat hub 的 skills source 不参与该次输入 |
| Worker 侧提醒 | `internal/colony/worker_reminders.go:107`（`newWorkerAgentLoop`）、`:114`（`NewAgentLoop`）、`:120`（task source）、`:127`（idle nudge）、`:135`（`Hub.Start`） | `runWorker` → `newWorkerAgentLoop` 建 Hub；task 快照与 idle 提醒在工具批尾部/空闲时注入 worker 的 loop | 已接 | C | 只有 task + idle 两个 source；没有 fleet/skills/tracker 源 |
| REST reminder surface 与 chat hub 一致 | `internal/server/routes_reminders.go:40`（`registerSessionReminderHub`）、`:374`（GET 镜像 live hub）、`:422`（DELETE 走 hub）；`internal/server/chat_runtime.go:310`（构造时注册） | `GET/DELETE /api/sessions/{id}/reminders` 优先读写同一 live hub；`ReminderRuntime` 只作为无 live session 的回退 | 已接 | C | 没有 live runtime 的会话（未加载/已关闭）仍读回退 runtime，可能为空 |

**小结**：信号层的主路径（chat transcript SSE、worker 报告回 Queen、colony 生命周期 → session bus）是通的，且有 live 记录；Sentinel 也作为第 8 个 source 接进来了；worker/overseer 拿到自己的 hub，REST reminder surface 与 chat hub 同源。提醒内容补齐 tracker 快照、真实 skills catalog 与 chat 侧 searchable tools，预载会话也已注入 catalog；剩余只有 `STREAM_STALLED` 未注册、预载 catalog 固定 default scope，以及 overseer 执行时 chat hub 的 skills source 不参与该次输入。

---

### 第 5 层：方法（Skill / Playbook）

概念主张：Skill 是"怎么做"的可复用方法（Queen 写、worker 按需加载）；Playbook 是"怎么批量做"的确定性 runner（查询 tracker 未完成行、并发派发、重试/退避/死信、按 tracker 现状 resume）。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Skill 解析 / 安装 / 校验库 + `hive skill` CLI | `internal/skills/parser.go:51`、`installer.go:125/196`；`internal/cli/skill.go:72`；`internal/cli/cli.go:308` | `hive skill ...` → `Runner.skillCommands()` → 文件系统 | 已接 | C | operator 面；安装/校验后的技能不会自动进入 agent 上下文 |
| Agent 写技能 `write_skill` | `internal/skills/agent_tools.go:54/63/178`；`internal/cli/session_tools.go:94/250` | chat Queen 会话工具表常驻 `write_skill`；`ExecuteAgentTool` → `WriteSkill` 写 colony/queen/global 三个 scope | 已接 | C | overseer 工具面没有 `write_skill`；global scope 只把请求落到会话目录，不直接改共享库 |
| Agent 读技能 `skill_view` / 技能目录加载 | `internal/skills/agent_tools.go:54/178`；`internal/agentloop/types.go:144`；`internal/agentloop/loop.go:828`；`internal/colony/runtime.go:404`；`internal/colony/skills_surface.go:40` | chat Queen 有 `skill_view`/`list_skills`；worker spawn 时拿到 `SkillDirs` + `SkillsCatalogPrompt`，`view_file` 在技能目录内被拦截成 skill 内容 | 已接 | C | 运行中 Queen 新写的 skill 要等 worker 下一批才可见 |
| Colony `skills/` 目录生产写入 | `internal/server/colony_scaffold.go:31`；`internal/server/routes_colonies.go:440`；`internal/cli/colony_worker_definition.go:31`；`internal/skills/skill_writer.go:170` | scaffold 预建 `skills/`；fork 仍只写 `metadata.json`/`worker.json`；`write_skill(scope='colony')` → `MkdirAll(<colony>/skills)` → 写 `SKILL.md`；`GET /colony/skills` 从磁盘读 | 已接 | C | fork 树上的 `skills/` 靠首次写入懒建；没有"空目录即就绪"的显式初始化（scaffold 路径除外） |
| Playbook runner（确定性批次、重试/退避/死信） | `internal/colony/playbook_tools.go:129`；`internal/playbook/runner.go:190/423`；`internal/playbook/rows.go:102/144` | `run_playbook` → `RunPlaybook` → `playbook.NewPlaybookRun` → 按 `pending_sql` 查行、并发派 worker、重试/退避/死信、同步返回 | 已接 | C | 只支持 native JSON；Python `.play.py` 返回 `status='unsupported'` 且 0 派发；同步执行 |
| Agent Playbook 工具（`run_playbook` / status / stop） | `internal/cli/session_tools.go:104/254`；`internal/colony/playbook_tools.go:76/168`（`action` 分派）；`internal/colony/playbook_control.go:120/157` | 会话工具表注册 `run_playbook`；`action='status'` 列在飞 + 历史，`action='stop'` 按 `run_id` 取消；执行器调 `colony.RunPlaybook` | 已接 | C | 控制动作合并在同一工具参数里，没有独立工具名/路由；stop 只取消未派发行，已 done 行保留 |
| Playbook run log / 按 tracker resume | `internal/colony/colony_db.go:78/107`；`internal/colony/playbook_tools.go:198`；`internal/playbook/rows.go` | 每次 run 写 `playbook_runs`；再次 `run_playbook(playbook_name=…)` 重查 `pending_sql`，done 行自然消失 | 已接 | C | 没有后台完成通知；单次 run 可用 `action='stop'` 取消，但"继续"仍靠重跑 `pending_sql` |

**小结**：方法层从"八层里最空"变成"主路径全通"：Queen 能写/读 skill，worker 能加载，playbook 有真 runner、run log 与 status/stop，resume 靠 tracker 现状成立。边界是 Python playbook 明确不支持、playbook 同步执行、控制动作没有独立工具名。

---

### 第 6 层：自治（Trigger / Sentinel）

概念主张：Trigger 从外部唤醒 colony（cron / interval / webhook）；Sentinel 在 Queen 停车时决定 nudge、标记完成或升级到人。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Trigger CRUD + activate/deactivate | `internal/server/routes_session_triggers.go:88/245/437/511`；`internal/server/routes_sessions.go:96-111` | `POST/PATCH/GET /api/sessions/{id}/triggers*` → `PutTrigger` + `PersistActiveTriggers` + `saveTriggerToAgent`；activate 启动 timer/webhook | 已接 | C | 只对 `ColonyID != ""` 的会话真正生效 |
| Trigger timer 真实唤醒 | `internal/server/trigger_runtime.go:416/455`；`:505-525`；`internal/server/chat_runtime.go:419`（`relayTriggerToOverseer`） | `StartTriggerTimer` → `runTimerLoop`；到点 `queenNode(session)` → `InjectTrigger` → 有 live overseer 时只投 overseer，否则排队一轮 trigger turn | 已接 | C | 无 colony 的会话在 `:505` 直接 continue；错过 tick 只盖章不补跑；live overseer 时 chat Queen 不重复回答同一 tick |
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
| 过往对话召回 / `search_messages` | 工具侧 `internal/cli/session_tools_search.go:43/168`；`internal/cli/session_tools.go:103`；`internal/memory/search.go:534`、`search_timeline.go:63`；注入侧 `internal/memory/past_conversations.go:222/477/511`（`PastRecallTopK=6`，`:13`）、`internal/server/chat_memory_wiring.go:31`、调用点 `internal/server/chat_runtime.go:607` | 会话工具表常驻 `search_messages`；每条 client 用户消息在 `ExecuteTurn` 前另注入最多 6 条带日期摘录（`<system-reminder>`，`lastPastRecall` 去重）；scope 来自会话 queen 目录 | 已接 | C | 自动召回只在 client 输入轮（worker 报告/trigger 不触发）；excerpts 与工具检索各自独立，超时（默认 3s）即放弃 |
| Memory timeline | 读侧 `internal/cli/memory_timeline.go:42/104`；`internal/cli/session_tools.go:112`；`internal/server/config_features.go:74-79`；写侧 `internal/server/chat_memory_wiring.go:59/89`、`internal/memory/timeline.go:426/491/528`；调用点 `internal/server/chat_runtime.go:303/645` | flag（env `HIVE_MEMORY_TIMELINE` 或 `hive.config.json`）打开后：`memory_timeline` 与 `search_timeline` 进入会话工具表；`NewChatRuntime` 启动回填近 10 个会话（排除当前，并发 2），每轮完成后按 `TimelinePending` 后台 `UpdateSessionTimeline`，写 `<sessionDir>/timeline.jsonl` + `timeline_meta.json` | 已接 | C | `SubscribeTimelineTriggers`（`timeline.go:645`）本身仍零调用，写入由等价的 `startTimeline` + `timelineAfterTurn` 承担；只在 chat Queen、session 有 LLM 且 flag 打开时生效，worker/overseer 不写 |
| Soul HTTP 读 / 改 / 决定 / 回滚 | `internal/server/routes_credentials_soul.go:35-46`；`internal/server/app.go:152` | `GET/PUT /api/queen/{id}/soul`、proposal decide、rollback | 已接 | C | `POST .../soul/reflect` 仍因 `deps.Reflect == nil` 返回 503（`routes_credentials_soul.go:420`） |
| Soul reflection 自动写 | `internal/server/chat_runtime.go:229`（`OnReflect`）、`:1207`（`sedimentSoul`）；`internal/soul/sediment.go` | reflection 完成 → `OnReflect` 回调 → `soul.Sediment` 写 queen soul 新版本；`GET soul` 显示最近时间与条数 | 已接 | C | 只在配置了 soul 的 queen 上生效；不是每轮；HTTP reflect 仍 503 |
| Soul 进入 persona / prompt | `internal/server/chat_runtime.go:1085-1095`（identity + `soul.PromptFor`）、`internal/soul/store.go:405`；`internal/queen/identity.go:85` | chat Queen 的静态 prompt 在 persona 之后拼 soul：`chatQueenIdentityPrompt` → `soul.PromptFor` → `Render(Load(...))`；不是每轮 reminder | 已接 | C | 只覆盖 chat Queen；`queenOverseerSpec`（`cli/loader.go:484`）只读 profile，不叠加 soul |

**小结**：Memory 的 chat Queen 路径完整，Soul 从"只有人工编辑"补上了 reflection 自动沉淀，并已进入 chat Queen 的 identity prompt；`search_messages`/`search_timeline` 已接，timeline 也从"有读无写"补上了写入侧（flag 门控，启动回填 + 每轮更新）。沉淀层现在的边界是作用域与实现方式：`SubscribeTimelineTriggers` 本身仍零调用、timeline 只覆盖 chat Queen、overseer 不读 soul。

---

### 第 8 层：能力边界（Tools / MCP / Credentials / Budgets）

概念主张：工具目录、MCP、凭据和预算共同定义 agent 能做什么、不能做什么；能力边界不是"有多少工具"，而是"哪些工具在什么角色下真实可用、受什么限制"。

| 机制 | 实现位置（file:line） | 生产调用链 | 状态 | 证据 | 缺口 |
| --- | --- | --- | --- | --- | --- |
| Chat 会话工具目录 | `internal/cli/session_tools.go:58-176`；`internal/server/chat_runtime.go:879/1019` | `newSessionToolCatalogue` → builtin + integration installer + senders + task + `run_worker` + `run_playbook` + tracker + skill 工具 + `search_messages`/`search_timeline` + `memory_timeline` + `credential_status` + `ask_user` | 已接 | L | 工具数量大；名称冲突保留 builtin；扫描器默认关闭 |
| Worker 工具面 | `internal/colony/runtime.go:825/856`；`internal/colony/worker_tool_selection.go:28`；`internal/cli/colony_worker_definition.go:92`；`internal/colony/worker_task_tools.go:77` | worker = builtin base + `worker.json.tools` 从 integration/MCP 池展开 + tracker + `report_to_parent` + `skill_view`/`list_skills` + 4 个 task 工具 | 已接 | L | 不是严格 Queen 子集；空声明只有 builtin；未声明 integration 不下发 |
| Colony overseer 工具面 | `internal/colony/overseer_tools.go:112`；`internal/colony/runtime.go:825` | `worker.Persistent` → `overseerToolDefs` = colony tools + queen tracker + `run_worker` + 4 个 task 工具 | 已接 | C | 没有 `skill_view`/`write_skill`/`run_playbook`/`ask_user`；客户端消息/报告/trigger 已可注入（`chat_colony_bridge.go:187/293/302`） |
| MCP 服务器配置管理 | `internal/server/routes_mcp.go:61-92`；`internal/server/app.go:157` | `GET/POST/DELETE /api/mcp/servers*`、enable/disable/health/mode/reload → `mcpregistry.NewAtHome` | 已接 | C | 只管理配置文件与健康检查 |
| MCP 客户端 / 工具装载 | `internal/cli/mcp_loader.go:66/102/131`；`internal/tools/mcp_bootstrap.go:66`；`internal/tools/registry.go:150`；`internal/tools/mcp_registry.go:224` | `serve` 启动 `startMCPRuntime` → `Reload`（读配置、连 pool）→ `SetProcessMCP`；每个 `NewToolRegistry` 自动 `attachProcessMCP` → `LoadRegistryServers` 命名空间注册 | 已接 | C | 连接失败的 server 退化为 warning；`/api/mcp/reload` 重连后新 registry 才 attach |
| MCP 工具白名单 | `internal/host/host.go:89-112`（`discoveredMCPToolNames` + `SetToolAllowlist`）；`internal/colony/runtime.go:304/321`；`internal/server/chat_mcp_allowlist.go:42`；`internal/server/routes_colony_tools.go:528-530` | 建 host 时把进程发现的 MCP 名字集交给 runtime（`tools.json` 的 enabled 子集为白名单）；`applyToolAllowlist` 按名字过滤，chat 侧 `filterToolsByColonyAllowlist` 同源；PUT 路由更新 live runtime 的 enabled 集 | 已接 | C | 未配置 `tools.json` 白名单时 `enabled == nil`，不过滤（预期）；运行中 PUT 传 `nil` 名字集，依赖 host 已注入的名字集 |
| 凭据存储 + 路由 + 环境同步 | `internal/server/routes_credentials.go:72-84/134`；`internal/cli/loader.go:286`；`internal/credentials/store.go:52` | `hive serve` 启动 `SyncCredentialEnv` → 集成工具从 `os.Getenv` 取值；`POST /api/sessions/{id}/credential-form` 写同一加密 store 后 `syncEnv` | 已接 | C | 只覆盖 env 型凭据；`get_account_info` 的 `AccountSource` 没有注入 |
| Agent 凭据工具 | `internal/cli/session_tools_credentials.go:26/37`（`credential_status`）；`internal/cli/session_tools.go:120`（入表）；`internal/server/chat_credential_park.go:43/68/77/90`（缺凭据 → credential_form park）；`internal/server/routes_credentials_form.go:36`（提交） | 会话工具表常驻 `credential_status`：列出凭据名/env/配置方式，不返回值；工具因缺凭据失败时生产 form、发 `client_credential_form_requested`、写 `pending_input`；提交路由写加密 store、清 park 并恢复 | 已接 | C | 没有 agent 主动发起的 `credentials(collect)` 写工具，form 只在缺凭据工具错误时生产；overseer 执行会话不经过该生产者（待验证其它入口） |
| Loop 预算（迭代 / 工具调用） | `internal/agentloop/loop.go:78-91`（默认 50 轮、soft 30/hard 50）；`internal/agentloop/loop_guards.go:18/36`；`internal/colony/worker_definition.go:9`；`internal/colony/worker_loop_config.go:50-65`；`internal/agentloop/session_cost.go:38/86` | chat Queen 用 `DefaultLoopConfig`（30 soft / 50 hard，`softBudgetCheckpointBlock` 在 30 触发）；worker 3+1 / 30 / 90 / 200，显式清 hard limit；会话花费上限由 `HIVE_SESSION_COST_LIMIT_USD` 播种，到额后拒绝下一轮 | 已接 | C | `max_cost_per_run` 仍只是 schema 字段；soft 检查点是单 turn-loop 内计数 |
| 速率 / 配额限制执行时生效 | `internal/llm/transport.go:45`（`AcquireLLMRequest`）；`internal/agentloop/tool_rate_limit.go:23/33`；`internal/agentloop/loop.go:994`；`internal/ratelimit/call_limiter.go:396/404` | HTTP provider 每次请求走 LLM 桶；每批工具在 `executeToolBatch` 前按 `rate_limits.tools.*` 取桶，拒绝时回注可重试结果；未配置时 no-op | 已接 | C | 只按 `hive.config.json` 的 `rate_limits` 生效；默认无限流，本轮未做 live 压测 |
| Worker 工具分层 / 搜索 | `internal/agentloop/search_tools.go:49`（`ensureSearchToolsTool`）、`:66`（`executeTieredTool`）、`:16`（`SearchToolsToolDef`）；`internal/agentloop/loop.go:577`（每轮广告）、`:633`（`RefreshDynamicTools`）、`:1011`（dispatch 前拦截）；`internal/colony/runtime.go:413-476`（`buildWorkerContext` 建 `ToolTierState` 与 `DynamicToolsProvider`）、`:349`（`gateableNames`）；`internal/server/chat_tool_tiers.go:43/91/153/276`（chat tier + allowlist）；`internal/tools/tiers.go:414/448`；`internal/tools/tier_defaults.go:163` | worker 的 `ToolTierState` 非空时，loop 自动把 `search_tools` 加进工具表；`search_tools` 在 loop 内执行（`tools.RunSearchTools` → `PromoteSearchedTools`），下一轮 `RefreshDynamicTools` 把 promote 的工具放出；未加载的 searchable 工具在 dispatch 前被拒；chat 侧也由 `newChatToolTier` 建同一分层；`PATCH /api/queen/{id}/tools` 经 `ApplyQueenMCPToolAllowlist` 重建会话的 tier | 已接 | C | 工具分层在 worker 与 chat 都生效；无 MCP 时 gateable 集合为空（全部 eager，预期）；预载会话也走 chat tier |

**小结**：能力边界的"有工具"一面很强：chat Queen 有 builtin + integration + 声明项 + tracker + dispatch + 检索/凭据状态；MCP 在 serve 启动时连接、注册，并且 `tools.json` 白名单真的过滤；worker 与 chat 都拿到了 `search_tools` 分层，Queen 30/50 预算也生效。1.0.24 补上 credential-form 的安全写回（加密 store + 清 park + 恢复），并让 Tool Library 的 PATCH 重建会话 MCP tier；剩余是 form 只在缺凭据工具错误时生产、overseer 执行路径不经过该生产者。worker 侧 spillover 已配置，实际 home 路径待实测（第 1 层）。

---

## 3. 与 `CONCEPT_WIRING_AUDIT.md` 的关系

两份审计是互补关系，不是重复计数。

| 维度 | `CONCEPT_WIRING_AUDIT.md` | 本审计 |
| --- | --- | --- |
| 组织方式 | 按 7 个概念页（colony / coordination / goals / improvement / queen / the_loop / worker_agent） | 按 8 层栈（执行 / 组织 / 状态 / 信号 / 方法 / 自治 / 沉淀 / 能力边界） |
| 回答的问题 | 每个概念主张的机制是否存在、是否从生产入口可达 | 每一层的 substrate 是否真实承重；缺一层会堵住上面哪层 |
| 判定颗粒 | 80 条行为/机制行，按概念归类 | 70 条 substrate 行，按层归类；同一机制只在其所属层计一次 |
| 计数 | 80 已接 / 0 部分 / 0 未接（基线 `ab5c3dc7`） | 70 已接 / 0 部分 / 0 未接（基线 `ab5c3dc7`） |
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
| Plan：`tasks.json` 的创建/更新/读取是否真在 queen 会话可用 | **已接（chat Queen + overseer + worker）** | `internal/cli/task_tools.go:47` + `internal/tasks/session_tools.go:747` + `internal/tasks/store.go:244`；overseer 用 `overseer_tools.go:51` 写在 `<colony>/_misc/overseer/tasks.json`；worker 用 `worker_task_tools.go:42` 写自己的目录。 |
| Tracker：写路径、binding、queen/worker/overseer 工具 | **已接（live）** | `colony/tracker.go:61/137` + `binding.go:67` + `tracker/tools.go:1030` + `session_tools.go:118-137` + `runtime.go:849` + `overseer_tools.go:123`。 |
| Event bus：worker 报告 → queen | **已接（live）** | `colony/runtime.go:1118/1200` → `queen_tools.go:492/426` → `chat_runtime.go:315/942`；有 live overseer 时只投 overseer（`:1122`），overseer 自身报告再回 chat 前端；报告作为非 client user turn 注入，带 duration/output_file。 |
| Event bus：SSE → UI | **已接** | 会话 transcript SSE（`chat_routes.go:168` + `chat_runtime.go:629-709`）；colony 生命周期事件经 `chat_colony_bridge.go:44/72` 镜像到同一 session bus；overseer 文本/执行事件重盖成 queen stream（`:139`）。仍无独立的 `/api/colonies/{id}/events`。 |
| Reminder hub：source 注册与触发点 | **已接（chat + worker + overseer）** | `chat_reminders.go:69/85-96` 注册 7 个 source + Sentinel source；`chat_runtime.go:577/584/643` + `loop.go:355/414/579/806/887` 覆盖主要点；worker/overseer 的 hub 在 `worker_reminders.go:114-135`。缺口：`STREAM_STALLED` 未注册、预载会话 catalog 走 default queen scope、无 MCP 时 chat searchable 数据为空。 |
| 执行保护：stall / doom-loop / Queen 预算 | **已接（新）** | `session.go:431` → `chat_loop_guards.go:39/81` → `stall_check.go:79/124`（`node_stalled`）；`doom_loop.go:28-34/178` + `loop.go:837/976` + `loop_guards.go:109`（5 次停车 + `node_tool_doom_loop`）；chat 失败分支保留 `doom_loop`（`chat_runtime.go:673/682/716`）；`loop.go:78-91`（30/50）。 |
| Colony scaffold / worker 持久运行态 | **已接（新）** | `colony_scaffold.go:31` 建 `skills/`、`runs/`、`triggers.json`；`worker_state.go:78/226` + `runtime.go:523` 落盘并自动续跑 queued/running；`loader_preloaded_session.go:53/62/199` 复用旧 queen 会话并注入 catalog（default scope）。缺口：fork 不建目录；POST colony_id 不 resume。 |
| Skill：`write_skill` / `skill_view` 是否注册；写入是否生产可达 | **已接** | `skills/agent_tools.go:54/178` + `cli/session_tools.go:94/250` + `colony/runtime.go:404`；write/view/list 全通，worker 侧 `SkillDirs` 生效。 |
| Playbook：`internal/playbook/runner.go` 是否有生产调用或 `run_playbook` 工具注册 | **已接（Go native，含 status/stop）** | `colony/playbook_tools.go:129/168` + `cli/session_tools.go:104/254` + `playbook.NewPlaybookRun` + `playbook_control.go:120/157`；Python `.play.py` 明确 unsupported，0 派发。 |
| Trigger：`StartTriggerTimer` 依赖的 `QueenExecutor` 是否有赋值；重启后 `triggers.json` 是否恢复 | **已接** | `loader.go:606` + `loader_preloaded_session.go:122` 赋值；`chat_runtime.go:351` 实现 `InjectTrigger`；`trigger_runtime.go:220` 恢复。普通 DM 会话不在 boot 重建。 |
| Sentinel：`sentinel.NewManager` 是否有生产调用 | **已接** | `loader.go:300-304` + `sentinel_runtime.go:41`；manager.Start 打开 drain 与 listeners，chat runtime 注册 escalation source。 |
| Memory：recall / reflection 是否每轮生效 | **已接（口径：reflection 非每轮）** | recall 每条 client 消息后都注入（`chat_runtime.go:784`），并追加每轮 6 条过往对话摘录（`chat_memory_wiring.go:31`、`chat_runtime.go:607`）；reflection 不是每轮，默认每 6 个完成 turn（`memory/reflection.go:1026` + `DefaultReflectionConfig`）；只有 chat Queen。 |
| Goal：加权标准执行 / 约束进 prompt / 改进信号 | **已接（新）** | `agentloop/goal_eval.go:144` + `colony/goal_outcome.go:74/90` + `runtime.go:827/1104/1106` + `colony/improvement.go:124/197/238/260`；chat Queen 无 Goal，`signals.jsonl` 尚无读取者。 |
| Soul：除 HTTP 读写路由外，reflection 是否会写 soul | **已接** | `chat_runtime.go:229/1259` + `soul/sediment.go`；HTTP `soul/reflect` 仍 503（`routes_credentials_soul.go:420`）；soul 已进 chat persona（`:1085-1095`），overseer 不读。 |
| Tools / MCP / credentials / budgets | **已接（凭据表单已收口）** | chat/worker/overseer 工具面已接；MCP 装载与白名单已生效；worker 与 chat 的 `search_tools` 分层已闭合；Queen 30/50 已启用；`credential_status` 只读 + credential-form 生产/提交（`routes_credentials_form.go:36`）；LLM/工具限速与花费上限已执行。仍无 agent 主动发起的写凭据工具，form 只在缺凭据工具错误时生产。 |

---

## 5. 最关键的剩余缺口

### 5.1 执行层已无接线缺口（第 1 层）

并行只读工具、doom-loop 两级保护、stall 订阅、spillover 都已接；1.0.24 又补了两处：chat 失败分支用 `turnFailureParkReason` 保留 `doom_loop`（`chat_runtime.go:673/682/716`），事件、`loop_state_changed` 与 `pending_input` 三处一致；`POST /api/sessions/{id}/credential-form` 消费 credential_form park，写加密 store、清 park 并恢复（`routes_credentials_form.go:36/121/139/144`）。judge criteria 也覆盖了 overseer 与有 criteria 的 chat Queen（`colony/runtime.go:1160-1167`、`chat_runtime.go:1077-1089`）。**执行层不再有"部分"行；剩余是作用域边界，不是断点。**

### 5.2 Colony 的 overseer 是唯一执行 lead，但仍有两份 conversation（第 2 层）

overseer 现在有 `run_worker`、queen tracker、task 工具、持久 loop 与 transcript 恢复（`runtime.go:694/1137`、`overseer_tools.go:112`）；绑定 colony 的 chat runtime 把客户端消息、worker 报告与 trigger 都转投它（`chat_colony_bridge.go:187/293/302` → `session_bridge.go:66`），chat Queen 只记录用户 turn、不跑 model turn（`chat_runtime.go:359-372`）。overseer 的文本/执行事件经 `overseer_emitter.go` 发到 colony bus，再由 `mirrorColonyEvent` 重盖到 `chatStreamID`/`chatNodeID`（`chat_colony_bridge.go:139`），UI 以 queen stream 呈现；首条中继带 skills 目录（`:214`）。**操作面已经是单 lead；仍不是单状态：overseer 的回复不写回 chat transcript，chat 的 pending/park 不由 overseer 更新，overseer 没有 `ask_user`。**

### 5.3 信号层只剩 `STREAM_STALLED` 与 catalog 作用域（第 4 层）

chat transcript 的 SSE、worker 报告回 Queen、colony 生命周期 → session bus 都已接（`chat_colony_bridge.go:44/72`）；worker/overseer 也有自己的 reminder hub（`worker_reminders.go:114-135`），REST reminder surface 镜像同一 live hub（`routes_reminders.go:374`）。`tracker_snapshot` source 读 bound `tracker.db`（`tracker_snapshot_reminder.go:105/135`、`chat_reminders.go:88`），`skills_catalog` 由 loader 注入真实 provider（`loader.go:605`、`loader_reminders.go:26/30`），chat 用 `newChatToolTier` 提供 searchable tools（`chat_tool_tiers.go:43/153`）。预载 `serve --colony` 也已注入 `SkillsCatalog`（`loader_preloaded_session.go:199`），但固定 default queen scope。仍缺 `STREAM_STALLED` source；overseer 执行时 chat hub 的 skills source 不参与该次输入。

### 5.4 能力边界凭据表单已收口（第 8 层）

MCP 工具在 serve 启动时连上并注册，`MCPToolNames` 由 host 从进程发现的名字集注入，`applyToolAllowlist` 与 chat 侧过滤同源（`host.go:89-112`、`colony/runtime.go:321`、`chat_mcp_allowlist.go:42`）。LLM/工具限速与会话花费上限也已在执行点生效（`transport.go:45`、`tool_rate_limit.go:23/33`、`session_cost.go:38/86`）。worker 的 tool tiering 已闭合，chat 侧也由 `newChatToolTier` 建同一分层；Queen 的 30/50 软硬预算已启用（`loop.go:78-91`、`loop_guards.go:36`）；`PATCH /api/queen/{id}/tools` 现在会重建 live 会话的 MCP tier（`routes_queens_tools.go:575-584`、`chat_tool_tiers.go:91/276`）。凭据侧补上了 credential-form 提交（`routes_credentials_form.go:36`），但 form 仍只在 chat runtime 的缺凭据工具错误时生产，overseer 执行路径不经过该生产者。

### 5.5 沉淀层已通，timeline 只剩等价实现与作用域（第 7 层）

reflection 能把结论自动写进 soul（`chat_runtime.go:229/1259`），soul 已拼进 chat Queen 的 identity prompt（`:1085-1095`）；`search_messages`/`search_timeline` 已注册进会话工具表，每轮 6 条 dated excerpts 也由 `chat_memory_wiring.go:31` 自动注入。timeline 的写入侧在 1.0.23 接上：`startTimeline` 启动回填、`timelineAfterTurn` 每轮更新，`ExtractTimeline`/`UpdateSessionTimeline`/`BackfillTimelines` 均不再零调用。仍缺的是：`SubscribeTimelineTriggers` 本身零调用（由等价实现承担）、timeline 只覆盖 chat Queen 且受 flag 门控、worker/overseer 不写。

---

## 6. 层间依赖结论

八层不是并列功能清单，而是有承重顺序的栈。按 `runtime_spine.md` 的 remove test，从下往上：

| 层 | 缺了会堵住 | 当前 Go 状态 |
| --- | --- | --- |
| 1. 执行 | 上面 2–8 层全部失去执行体；没有 loop 就没有 agent 的一步 | 完整：stall 已订阅、doom-loop 5 次停车且 park reason 可读、Queen 30/50 与 worker spillover 已生效、credential-form 提交已接、judge criteria 覆盖 worker/overseer/有 criteria 的 chat |
| 2. 组织 | 状态没有写者、信号没有生产者、方法/自治/沉淀没有作用对象 | chat Queen + `run_worker` + worker 身份/工具/task/resume/运行态自动续跑可用；goal 闭环在 worker 侧成立；scaffold 完整；live overseer 是客户端消息/报告/trigger 的唯一执行 lead；`serve --colony` 复用旧会话并注入 catalog；仍两份 conversation |
| 3. 状态 | 并行工作失去共享事实；Queen 无法校验/恢复；playbook 没有未完成行可查 | Plan（chat + overseer + worker）与 Tracker（Queen/worker/overseer + REST）已接 |
| 4. 信号 | worker 结果不回 Queen；UI 静默；长会话漂移、重复派活 | worker→Queen、colony→session SSE、worker reminder hub、REST 镜像都已接；提醒内容已补 tracker/skills/chat searchable；预载 catalog 固定 default scope；STREAM_STALLED 未注册 |
| 5. 方法 | 一次性工作仍可做，但每个单元重复发现方法；批量只能逐行手工做 | 主路径全通：skill 写读 + Go-native playbook + run log/resume + status/stop |
| 6. 自治 | colony 只能等人来唤醒；Queen 停车时没人知道 | 主路径全通：trigger timer/webhook/manual + 恢复（live overseer 时只投 overseer）；Sentinel 分类/升级；剩冷恢复 |
| 7. 沉淀 | 每个会话从零开始；Queen 的人格不演化 | memory 已接（chat Queen），soul 自动写并进 chat persona；search 与每轮 excerpts 已接；timeline 读+写（flag 门控，`SubscribeTimelineTriggers` 用等价实现）；overseer 不读 soul |
| 8. 能力边界 | agent 只能用 builtin；外部系统、凭据、限额无法按角色收放 | chat/worker/overseer 工具面已接；MCP 装载 + 白名单已生效；chat/worker `search_tools` 分层闭合；Queen 30/50 已启用；credential_status + credential-form 提交已接；Tool Library PATCH 重建会话 tier；仍无 agent 主动写凭据工具 |

按"修复收益 / 依赖顺序"排：

1. **第 2 层（overseer/chat 状态合一）**：执行 lead 已唯一，剩下的高收益项是把 overseer 的回复与 pending/park 写回 session 状态（或明确声明 chat transcript 只是事件镜像），并决定 overseer 是否补 `ask_user`。
2. **第 1/8 层（凭据生产路径）**：提交路由已有；让 overseer 执行的工具错误也能生产 credential_form，或补一个 agent 主动发起的 collect 工具。
3. **第 4 层（catalog 作用域 + STREAM_STALLED）**：把预载会话的 queen scope 从固定 `default` 改为 metadata/记录中的实际 queen；补 `STREAM_STALLED` source。
4. **第 7 层（timeline 订阅 + 作用域）**：写入已由等价实现承担；补 `SubscribeTimelineTriggers` 或删除该函数，并让 worker/overseer 也能写。
5. **生态边界**：MiniMax 流式、Python playbook、81 个第三方凭据模块属于已知差异，不是本轮接线项。

一句话结论：**1.0.24 把最后六个"部分"收口：credential-form 提交、预载 catalog、doom park reason、judge criteria、Queen 两阶段、overseer 单执行 lead。70 行 substrate 现在是 70/0/0。剩下的不是"有没有机制"，而是"两份 conversation 是否要合一、catalog 作用域、凭据生产路径与生态验证"。**

## 7. 上一轮变更记录（历史存档：1.0.16–1.0.21，基线 `c444c192`）

> 本节保留 `c444c192` 版的迁移记录，不再代表当前状态；当前状态见 §1 与 §9。

该版计数 **61 已接 / 9 部分 / 0 未接（70 行）**，相对 `4b6f1cf4` 的 50/10/10 是 +11 已接、-1 部分、-10 未接。状态变化 13 条。

### 7.1 未接 → 已接（8 条）

| # | 层 / 条目 | 依据（file:line @ c444c192） |
| --- | --- | --- |
| 1 | 第 1 层：只读工具并行执行 | `internal/agentloop/loop.go:61`（`ParallelReadOnlyTools`）、`:88`（默认 true）、`:878`（`executeToolBatch`）、`:895`（`allToolsConcurrencySafe`） |
| 2 | 第 3 层：Worker 自己的 task list | `internal/colony/worker_task_tools.go:42/77`；`internal/colony/runtime.go:853/889/916` |
| 3 | 第 4 层：Colony 事件 → 会话 / UI SSE | `internal/server/chat_colony_bridge.go:44/72`；`internal/server/chat_runtime.go:950` |
| 4 | 第 4 层：Worker 侧提醒 | `internal/colony/worker_reminders.go:107/114/120/127/135` |
| 5 | 第 4 层：REST reminder surface | `internal/server/routes_reminders.go:40/374/422`；`internal/server/chat_runtime.go:297` |
| 6 | 第 7 层：过往对话召回 / `search_messages` | `internal/cli/session_tools_search.go:43/168`；`internal/cli/session_tools.go:103`；`internal/memory/search.go:534` |
| 7 | 第 7 层：Soul 进入 persona / prompt | `internal/server/chat_runtime.go:1085-1095`；`internal/soul/store.go:405` |
| 8 | 第 8 层：速率 / 配额限制执行 | `internal/llm/transport.go:45`；`internal/agentloop/tool_rate_limit.go:23/33`；`internal/agentloop/loop.go:994`；`internal/ratelimit/call_limiter.go:396/404` |

### 7.2 未接 → 部分（2 条）

| # | 层 / 条目 | 依据 / 仍缺的一半 |
| --- | --- | --- |
| 1 | 第 7 层：Memory timeline | 读侧：`internal/cli/memory_timeline.go:42/104` + `session_tools.go:112`；仍缺：`ExtractTimeline`/`UpdateSessionTimeline`/`BackfillTimelines`/`SubscribeTimelineTriggers` 零生产调用（`internal/memory/timeline.go:426/491/528/645`） |
| 2 | 第 8 层：Agent 凭据工具 | `internal/cli/session_tools_credentials.go:26/37` + `session_tools.go:120` 提供只读 `credential_status`；仍缺写凭据工具与 `credential_form` 停车 |

### 7.3 部分 → 已接（3 条）

| # | 层 / 条目 | 依据 |
| --- | --- | --- |
| 1 | 第 1 层：park / resume（`ask_user`） | `internal/server/chat_park.go:29/81/108`；`internal/server/chat_runtime.go:216/1115/1247` |
| 2 | 第 5 层：Agent Playbook 工具（status / stop） | `internal/colony/playbook_tools.go:168`；`internal/colony/playbook_control.go:120/157` |
| 3 | 第 8 层：MCP 工具白名单 | `internal/host/host.go:89-112`；`internal/colony/runtime.go:304/321`；`internal/server/chat_mcp_allowlist.go:42` |

### 7.4 状态不变但缺口收窄 / 改写

| 条目 | 旧缺口 | 当前缺口 |
| --- | --- | --- |
| 第 1 层：judge（部分） | chat 无 criteria；overseer 不评估 | 不变；只补了 worker/overseer 走 `NewAgentLoop` 的事实 |
| 第 1 层：崩溃恢复（部分） | colony-bound 不走 resume；无自动续跑 | 不变 |
| 第 1 层：压缩（部分） | spillover 无生产者 | 不变 |
| 第 2 层：overseer（部分） | 无任何输入源 | 报告与 trigger 已投递（`chat_colony_bridge.go:112/129/142`）；仍缺客户端用户消息入口 |
| 第 2 层：scaffold（部分） | 不建 skills/triggers/runs | 不变 |
| 第 4 层：提醒内容（部分） | 无 tracker 快照、skills catalog 空、chat 无 searchable tools | 不变 |
| 第 8 层：worker tiering（部分） | MCP 集合空 → 全 eager；无 `search_tools` | MCP 集合已非空 → gateable 工具被判 searchable；`search_tools` 仍零注册，工具没有加载入口 |

### 7.5 备注 / 待验证

| 事项 | 说明 |
| --- | --- |
| 限速 / 花费上限的 live 行为 | 静态确认执行点与拒绝路径；只在配置 `rate_limits` / `HIVE_SESSION_COST_LIMIT_USD` 时生效，本轮未做 live 压测，证据仍为 `C` |
| `memory_timeline` 数据 | 工具已接但写入侧零调用；无历史 timeline 文件时返回空 |
| `search_tools` | `BuildSearchTools`（`internal/tools/tiers.go:414`）存在但零生产调用；本轮不把它记为已接 |
| overseer 用户消息 | 未找到 HTTP/总线路由，保留"部分" |
| `credential_form` | `ParkReasonCredentialForm` 有枚举与 idle-nudge 分支，无生产者，保留"部分" |

## 8. 历史变更记录（1.0.22，基线 `6723ab78`）

本版计数 **63 已接 / 7 部分 / 0 未接（70 行）**，相对 `c444c192` 的 61/9/0 是 **+2 已接、-2 部分**。状态变化 2 条，另有 3 条状态不变但缺口改写。行集不变。

### 8.1 部分 → 已接（2 条）

| # | 层 / 条目 | 依据（file:line @ 6723ab78） |
| --- | --- | --- |
| 1 | 第 7 层：Memory timeline | 写侧 `internal/server/chat_memory_wiring.go:59`（`startTimeline`，启动回填近 10 个会话、排除当前、并发 2）/`:89`（`timelineAfterTurn`，按 `TimelinePending` + 批量/冷却门控）；调用点 `chat_runtime.go:303/645`；落盘 `internal/memory/timeline.go:426`（`ExtractTimeline`）→`:491`（`UpdateSessionTimeline`）→`appendTimelineItems` 写 `<sessionDir>/timeline.jsonl` + `timeline_meta.json`；`:528`（`BackfillTimelines`）不再零调用。 |
| 2 | 第 8 层：Worker 工具分层 / 搜索 | `internal/agentloop/search_tools.go:49`（`ensureSearchToolsTool`，`ToolTierState` 非空才广告）、`:66`（`executeTieredTool`，in-loop 执行 + 未加载工具拒绝）；`loop.go:577`（每轮广告）、`:633`（`RefreshDynamicTools` 放出 promote 的工具）、`:1011`（dispatch 前拦截）；worker 侧 `colony/runtime.go:413-476` 建 tier 与 `DynamicToolsProvider`。 |

### 8.2 状态不变、缺口改写（3 条）

| 条目 | 旧缺口 | 当前缺口 |
| --- | --- | --- |
| 第 1 层：自动上下文压缩（部分） | 超大工具结果 spillover 仍无生产者 | 生产者已接：`tool_result_spillover.go:73/117` + `loop.go:1011/1021/1023`，默认 30k、预览 5000 字符；但只有 chat 注入 `SpilloverDir`（`chat_memory_wiring.go:17-21`），worker 的 `BuildWorkerLoopConfig` 不设，仍不落盘。 |
| 第 1 层：judge（部分） | 只有 colony worker 的 `AgentSpec.SuccessCriteria` 能触发 judge | spec 无 criteria 时由 goal 的 criterion 描述回填（`colony/runtime.go:1073-1077`、`criteria.go:37`）；chat Queen 与 overseer 仍不评估。 |
| 第 7 层：过往对话召回（已接） | 每轮自动 dated excerpts 未注入 | 已注入：`memory/past_conversations.go:222/477/511` + `chat_memory_wiring.go:31` + `chat_runtime.go:607`（每轮最多 6 条，`lastPastRecall` 去重）；只在 client 输入轮。 |

### 8.3 备注 / 待验证

| 事项 | 说明 |
| --- | --- |
| 验证方式 | `go build ./...` 通过；`go test ./internal/agentloop`（`-run` 过滤 Goal / Spillover / SearchTools）、`go test ./internal/colony`（Goal / Improvement / SearchTools）通过。`TestChatRuntimeWritesTimelineAfterTurn` 在整组 `-run` 下出现一次 `TempDir RemoveAll` 清理竞态（异步 timeline 写入未及收尾），单独重跑 3 次通过，记为测试侧 flake。 |
| `SubscribeTimelineTriggers` | `memory/timeline.go:645` 本身仍零调用；写入由等价的 `startTimeline` + `timelineAfterTurn` 承担（release notes 同口径），不计为缺陷但保留在此。 |
| worker spillover | `BuildWorkerLoopConfig`（`colony/worker_loop_config.go:46`）只继承 30k 上限、不设 `SpilloverDir`；`truncateToolResult` 在 `dir==""` 时原样返回，故 worker 侧"部分"。 |
| goal 的层归属 | 本审计没有 goals 层，goal 闭环体现在第 1 层 judge（criteria 回填）与第 2 层组织（worker 侧执行）；概念页口径见 `CONCEPT_WIRING_AUDIT.md` §3。 |
| live 证据 | 本轮新增能力均无交付侧 live 记录，全部标 `C`；support-matrix 1.0.22 只更新版本与测试计数。 |

## 9. 上一轮变更记录（1.0.23，基线 `d650199d`）

> 以下为 1.0.23 的迁移记录；1.0.24 的状态与变化见 §10。

本版计数 **67 已接 / 3 部分 / 0 未接（70 行）**，相对 `6723ab78` 的 63/7/0 是 **+4 已接、-4 部分**。状态变化 4 条，全部为"部分→已接"；另有 7 条状态不变但缺口改写。行集不变。

### 9.1 部分 → 已接（4 条）

| # | 层 / 条目 | 依据（file:line @ d650199d） |
| --- | --- | --- |
| 1 | 第 1 层：崩溃恢复 | `worker_state.go:78/226` + `colony/runtime.go:523`：worker 运行态写 `runs/<id>.json` 与 `workers/<id>/state.json`，`Start` 自动续跑崩溃时非终态 worker；`loader_preloaded_session.go:53/62` 让预载会话按 `queen_resume_from` 复用旧 id。 |
| 2 | 第 1 层：压缩 / spillover | `worker_loop_config.go:65/122` 给每个 worker loop 注入 `cache/worker-tool-results/<pid-seq>`；`tool_result_spillover.go:73/79` 首次 spill 建目录，`loop.go:224` 恢复计数器；chat 仍写 `<session>/data`。 |
| 3 | 第 2 层：scaffold | `colony_scaffold.go:31` + `routes_colonies.go:474`：scaffold 建 `skills/`、`runs/`、空 `triggers.json`，幂等；fork 仍只写两个 JSON。 |
| 4 | 第 4 层：提醒内容供给 | `tracker_snapshot_reminder.go:105/135` + `chat_reminders.go:88`；`loader.go:589` + `loader_reminders.go:26/30`（真实 skills provider）；`chat_tool_tiers.go:43/153`（chat searchable tier）。 |

### 9.2 状态不变、缺口改写（7 条）

| 条目 | 旧缺口 | 当前缺口 |
| --- | --- | --- |
| 第 1 层：park / resume（已接） | 仅 ask_user；credential_form 无生产者 | credential_form 有生产者、事件、`pending_input` 落盘与重启恢复；无提交路由，提交后不自动清 park/续跑。 |
| 第 1 层：judge（部分） | chat 无 criteria；overseer 不评估 | 不变；chat Queen 仍无 criteria。 |
| 第 2 层：`serve --colony`（已接） | 每次新建 session id，不恢复旧会话 | 预载会话复用 metadata 的 `queen_session_id` 与旧转录；仍未注入 `SkillsCatalog`。 |
| 第 2 层：worker 恢复（已接） | 只有手动 `resume_worker_ids` | 增加运行态落盘与 `Start` 自动续跑；仍只覆盖 queued/running，终态不复活。 |
| 第 3 层：Plan 跨 reload（已接） | colony-bound 不走 `queen_resume_from` | 预载 colony 可 resume；`POST /api/sessions` 带 colony_id 的普通创建仍不行。 |
| 第 8 层：Agent 凭据（部分） | 只有只读 `credential_status`；credential_form 无生产者 | 增加 credential_form 生产/事件/落盘；仍无写凭据工具与提交路由。 |
| 第 8 层：Loop 预算（已接） | Queen 无 30/50 软检查点 | `DefaultLoopConfig` 设 30 soft / 50 hard，worker 显式清 hard limit 保持 90；`max_cost_per_run` 仍只是 schema 字段。 |

### 9.3 备注 / 待验证

| 事项 | 说明 |
| --- | --- |
| worker 自动续跑边界 | 只覆盖崩溃时非终态记录（正常为 queued/running）；completed/failed/stopped 不复活；自定义 `report_to_parent` status 未归一为终态时会被当作可续跑（待验证）。 |
| `POST /api/sessions` + colony_id | 普通创建路径不走 `queen_resume_from`（`loader_session_resume.go:73`）；只有 `serve --colony` 预载路径按 metadata 复用。 |
| credential-form 提交 | 没有消费 `pending_input.credential_form` / `correlation_id` 的路由；`POST /api/credentials` 只是通用保存，提交后不会自动清 park/续跑。 |
| 预载会话 SkillsCatalog | `attachPreloadedChatRuntime` 的 deps（`loader_preloaded_session.go:180-190`）未传 `SkillsCatalog`；`chat_reminders.go:38-44` 回退空 provider。 |
| doom-loop park reason | `parkOnDoomLoop` 置 `TurnResult.ParkReason=doom_loop`，但 chat 失败分支写 `unknown`（`chat_runtime.go:628`）或 `llm_error`（`:620`）；`node_tool_doom_loop` 事件仍发出。 |
| worker spillover 实路径 | `workerSpilloverDir`（`worker_loop_config.go:122`）用 `hivehome.Resolve()`（进程 `HIVE_HOME`），不是 loader 的 `HomeRoot`；自定义 HomeRoot 下实际路径需实测（待验证）。 |
| stall 门控 | `AddSession` 必定订阅；`StallJudgeSlot` 受 `stall_check` 配置与 judge slot 门控，未配置 judge 时订阅存在但不判定。 |
| 验证方式 | `go build ./...` 通过；聚焦测试通过：`go test ./internal/server`（scaffold / loop-guard / doom-park / credential-park 用例）、`go test ./internal/colony`（worker-state / 自动续跑 / worker spillover 用例）、`go test ./internal/agentloop`（Queen 30/50 用例）、`go test ./internal/cli`（预载会话重启复用用例），均 `-count=1`。 |
| live 证据 | 本轮新增能力均无交付侧 live 记录，全部标 `C`；support-matrix 1.0.23 只更新版本与测试计数。 |

## 10. 本轮变更记录（1.0.24，基线 `ab5c3dc7`）

本版计数 **70 已接 / 0 部分 / 0 未接（70 行）**，相对 `d650199d` 的 67/3/0 是 **+3 已接、-3 部分**。状态变化 3 条，全部为"部分→已接"；另有 6 条状态不变但缺口改写。行集不变。

### 10.1 部分 → 已接（3 条）

| # | 层 / 条目 | 依据（file:line @ ab5c3dc7） |
| --- | --- | --- |
| 1 | 第 1 层：judge accept/retry/escalate | `colony/runtime.go:1160-1167`（overseer 无 spec criteria 时从 `r.goal` 回填并 `ConfigureJudge`）；`chat_runtime.go:321/1061/1077-1089`（`ChatRuntimeDeps.SuccessCriteria` 或 `BindColony` 继承 goal 后 `ConfigureJudge`）；`judge.go:109/127/198` 的评估/RETRY 原路径不变。测试 `internal/colony` 的 `TestOverseerInheritsGoalCriteriaAndRunsJudge`、`internal/server` 的 `TestChatRuntimeConfiguredCriteriaRunsJudge` / `TestChatRuntimeInheritsColonyGoalCriteriaForJudge`。 |
| 2 | 第 2 层：Colony overseer 是 client-facing Queen | `chat_runtime.go:359-372`（`Deliver` 转投 + `recordOnly`）、`:419-427`（trigger）、`:1122`（报告）；`chat_colony_bridge.go:187/293/302`（消息/报告/trigger 中继）、`:139`（`mirrorColonyEvent` 重盖 queen stream）、`:214`（首条中继带 catalog）；`overseer_emitter.go:63`（文本事件）；`session_bridge.go:66`（`InjectOverseer`）。测试 `TestChatRuntimeRelaysClientMessageToOverseer` / `TestChatRuntimeRelayOwnsReportsAndTriggers` / `TestOverseerTurnPublishesClientOutput`。 |
| 3 | 第 8 层：Agent 凭据工具 | `routes_credentials.go:84`（注册 `POST /api/sessions/{id}/credential-form`）；`routes_credentials_form.go:36`（校验 pending form）、`:121`（写加密 store）、`:136`（`syncEnv`）、`:139`（清 park）、`:144`（恢复）；`chat_credential_park.go:77/90`；`chat_credential_park.go:43/68` 的生产者不变。测试 `TestCredentialFormSubmissionStoresSecretAndResumesQueen` / `TestCredentialFormSubmissionRequiresPendingForm`。 |

### 10.2 状态不变、缺口改写（6 条）

| 条目 | 旧缺口 | 当前缺口 |
| --- | --- | --- |
| 第 1 层：park / resume | credential-form 无提交路由 | 提交路由已接；仍只服务 chat runtime 的 pending form，overseer 执行会话不生产 form，overseer park 只在内存。 |
| 第 1 层：流式输出 | MiniMax 回退 | 不变；MiniMax 仍非流式。 |
| 第 2 层：`serve --colony` | 未注入 `SkillsCatalog` | 已注入（`loader_preloaded_session.go:199`）；固定 default queen scope。 |
| 第 2 层：Worker 报告 | 直接注入 chat Queen | live overseer 时只投 overseer；非 overseer worker 的 client IO/tool/token 不镜像。 |
| 第 4 层：提醒内容 | 预载 skills 为空 | 已注入，但固定 default scope；overseer 执行时 chat hub 的 skills source 不参与该次输入。 |
| 第 6 层：Trigger timer | 只排 chat turn | live overseer 时只投 overseer；仍只对 colony-bound 会话生效。 |

### 10.3 备注 / 待验证

| 事项 | 说明 |
| --- | --- |
| 两份 conversation | overseer 是唯一执行 lead，但回复不写回 chat transcript；chat 的 `pendingQuestions`/park 不由 overseer 更新；overseer 无 `ask_user`。 |
| chat Queen 不再执行 model turn | 仅限绑定 colony 且有 live overseer 时的客户端消息/报告/trigger；`recordOnly` 只记录用户 turn。overseer 自身终态报告仍会进 chat 前端，可能触发一次 chat 回合；无 overseer 时回退。 |
| 非 overseer worker 事件 | client IO 与 tool/token 事件不镜像；生命周期、报告、execution/loop 事件仍镜像。 |
| 预载 catalog 作用域 | `loader_preloaded_session.go:199` 用 `chatSessionQueenScope(record.QueenName)`；预载记录 `QueenName` 为空 → `default`。 |
| credential form 生产路径 | 只有 chat runtime 的 `parkForMissingCredential`；overseer 的 executor 不走该路径（待验证是否有其它入口）。 |
| `/api/queen/{id}/tools` phase | GET 响应体没有 phase 字段；phase 在 `GET /api/sessions/{id}`（`queen_phase`）与 `GET /api/sessions/{id}/tools`（`phase`）。PATCH 的 `refreshed_sessions` 依赖 `PhaseState != nil`，这部分成立。 |
| worker spillover 实路径 | `workerSpilloverDir`（`worker_loop_config.go:122`）用 `hivehome.Resolve()`（进程 `HIVE_HOME`），不是 loader 的 `HomeRoot`；自定义 HomeRoot 下实际路径需实测（待验证）。 |
| 已知边界 | MiniMax 不流式；Python `.play.py` 不可执行；81 个第三方凭据模块未验证。 |
| 验证方式 | `go build ./...` 通过；聚焦测试通过（`-count=1`）：`internal/server` 的 credential-form / doom-park / overseer-relay / criteria / phase-tool / allowlist 用例；`internal/cli` 的 phase 持久化/恢复/load-colony 与 preloaded catalog 用例；`internal/colony` 的 overseer judge 与 emitter 用例。 |
| live 证据 | 本轮新增能力均无交付侧 live 记录，全部标 `C`；support-matrix 1.0.24 只更新版本与测试计数。 |
