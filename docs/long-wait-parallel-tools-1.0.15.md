# 1.0.15 对齐登记：长等待不耗模型回合 + 非 Anthropic 并行工具调用

> 上游：`aden-hive` 1.0.15（`docs/releases/v1.0.15.md` §Changed 第 3、4 条）。
> 对应提交：`68b6c230`（a long wait costs no model turns, and a new message ends it）、
> `a7ca0113`（task updates ride along with real tool calls）。
> 本文是这两项「Changed」行为的 Go 现状取证（探针实跑）与差异登记；工具面（tool-definitions）未变。
> 复现命令见 §6。登记时间 2026-11-22（lane `wait-parallel-1015`，基线 main `6ce8decc`）。

## 0. 结论摘要

| # | 行为 | 判定 | 处置 |
|---|---|---|---|
| A | 长等待不耗模型回合（terminal/worker，最长 10 分钟、每秒检查、新消息结束等待） | **一致**（agent 层探针通过；一处默认值差异已修；一处未实现能力登记） | 修 `DefaultReportWait` 60s→600s；`collect_result` 长等待登记（Go 无 background tools） |
| B | 非 Anthropic 模型并行工具调用（拒绝则去掉再问）+ 任务更新与真实工具同回合 | **一致**（请求字段与批量执行探针通过；chat-app 提示词分支已对齐，生产接线差异登记） | 无代码修；chat-app 运行时来源登记 |

上游侧两个「上游测试」文件（`core/tests/test_background_tools.py`、`test_midturn_input.py`）的意图已被 Go 侧探针逐条覆盖，见下两表。

## 1. 表 A：长等待不耗模型回合

上游机制（`core/framework/agent_loop/agent_loop.py`）：

- `_WAIT_TICK_S = 1.0`；`_wait_for_job` 在**循环里**（不在 tool call 里）做状态轮询：`terminal_job_logs(wait_until_exit=True)` 每 1s 查一次 `max_bytes=0`，直到 job 非 running / `tool_wait_limit_seconds`（默认 600）到点 / `_input_waiting()`（有未读消息）为真；然后才做模型原本要求的读取（`wait_until_exit=False`）。
- `_collect_background_result` 同构：`collect_result(handle)` 等到后台工作完成，或 `wait_seconds`（上限 600）到点，或新消息到达（返回 `{"status":"pending"}`，工作继续跑）。
- `_wait_budget`：`max(0, min(requested, limit))`，非数值 → limit。
- 工具侧（`tools/src/terminal_tools/jobs/tools.py`）：tool call 内部仍最多等 45s（`min(wait_timeout_sec, 45)`），默认 `wait_timeout_sec=600` —— 长等待由循环承担，避免占用 Windows 上共享的 MCP 连接。

| # | 检查项 | 上游（1.0.15） | Go 现状（实测） | 判定 |
|---|---|---|---|---|
| A1 | terminal 等待时长上限 | `LoopConfig.tool_wait_limit_seconds` 默认 600s | `DefaultLoopConfig().ToolWaitLimitSeconds` = 600（`internal/agentloop/loop_config.go:130`）；未配置时 `waitLimit()` 兜底 600（`loop.go:194`） | 一致 |
| A2 | 检查节奏 | 每 1s | `waitTick()` = 1s（`loop.go:201`），探针断言 | 一致 |
| A3 | 等待预算语义 | `max(0, min(requested, 600))`；非数值→600 | 探针：缺省→600、30→30、1200→600、-5→0、"abc"→600、600→600 | 一致 |
| A4 | 等行为发生在循环里（不占 tool 连接、不耗模型回合） | `_execute_tool` 拦截 `terminal_job_logs`+`wait_until_exit` → `_wait_for_job` | `executeTool` 同点拦截 → `waitForJob`（`loop.go:1013/1087`），状态检查输入 `{job_id, max_bytes:0}`、收尾读取 `wait_until_exit=false` | 一致 |
| A5 | 轮询不耗模型回合（实测数字） | 每次重复轮询 = 1 个模型回合（改前） | 探针 `TestAJobWaitSpendsNoModelTurnPerStatusCheck`：3 次状态检查（≈2.0s 墙钟）+1 次读取 → **模型调用数 = 2**（即工具轮 + 回答轮，轮询 0 个回合） | 一致 |
| A6 | 新消息结束等待，并拿到已到达的内容 | `_input_waiting()`（`_input_unread or 队列非空`）为真即停，随后读取 | `InputWaiting()`（`unread || queue>0`，`loop.go:289`）；`TestAMessageSentDuringTheResponseStopsItsToolFromWaiting` 断言等待被跳过、恰好 1 次读取、消息进入下一轮上下文 | 一致 |
| A7 | 未知 job 不等待直接回报 | `_job_status` 非 running 即跳出 | 探针 `TestAJobThatCantBeFoundIsReportedWithoutWaiting`：2 次 executor 调用（1 状态检查+1 读取）、<1s 返回错误 | 一致 |
| A8 | 工具面文案/schema | `wait_timeout_sec: float = 600.0`；描述含 "up to 600"/新消息措辞；tool 内 clamp 45s | `internal/tools/builtin/terminal_tools.go:137` schema 默认 `600.0`；`terminal_jobs.go:25-26` clamp 45s；描述逐字含同两句（`terminal_tool_descriptions.go:135-152`） | 一致（工具面 sha 不变） |
| A9 | `collect_result` 长等待（worker/后台结果） | 循环等待（同 A4/A5），结束条件为完成/预算/新消息；取消不杀后台工作 | **Go 无 background tool 机制**：`collect_result` 是 flat 表面的 shim（"not implemented in this build (no background tool handles)"，`internal/cli/tool_surface_flat.go:171,528`） | **差异→登记**（见 §3.1；不重写：Go 未实现 background tools，单独移植项） |
| A10 | colony worker 等待默认时长 | `wait_for_worker_reports(timeout: float = 600.0)` | 修前 `DefaultReportWait = 60s`；**本次修为 600s**（`internal/colony/runtime.go:31`），探针 `TestDefaultReportWaitIsTenMinutes` | **差异→已修** |
| A11 | playbook 等 worker 的时长 | `PlaybookRun(default_timeout=600)`，`dispatch_one` 等待 600s；`run_playbook` 先给 1.5s fast-grace（`HIVE_PLAYBOOK_FAST_GRACE`），超窗转后台 + `[PLAYBOOK_COMPLETE]` 通知 | `playbookDefaultTimeout = 600s`（`internal/colony/playbook_tools.go:62`）；fast-grace 默认 1.5s（`internal/playbook/grace.go`）；`dispatchOne` 用 `WaitForWorkerReports(timeout)`（`playbook_tools.go:772`） | 一致 |
| A12 | playbook 父侧静默守护（Go 特有保护） | 上游进程内执行，无父侧守护（无对应物） | 900s = 600s（dispatch 默认）+ 300s grace，任何子进程输出都会重置（`internal/playbook/python.go:68-72,345,485`）；探针 `TestDefaultStallTimeoutOutlastsAWorkerDispatch` 断言 900s 且不短于一次完整 dispatch。它是「桥死了」的守护，不在模型回合内，也无新消息中断语义（与上游 playbook 等待一致） | 一致（保护性差异，登记说明） |
| A13 | 等待期间 idle-nudge 保持安静 | `_mark_session_progress()` 每次检查都算进展，idle nudge 不响 | Go 无 per-tick progress；会话整轮 `turnActive=true`，idle nudge 的 `StreamActive` 分支在整轮内直接 `return nil`（`internal/server/chat_reminders.go:206`、`idle_nudge.go:175-183`），观测结果等价：长等待不会招来 idle nudge | 一致（机制不同，登记说明） |

## 2. 表 B：并行工具调用 + 任务更新同回合

上游机制（`a7ca0113`）：

- `LiteLLMProvider.stream`：带 tools 且非 Anthropic 且未被拒过 → `kwargs["parallel_tool_calls"] = True`；异常里若消息含 `parallel_tool_calls` → 警告、置 `_parallel_tools_refused=True`、删参并**继续**（重发一次）；之后该 provider 不再发。`_is_anthropic_model()` = 前缀 `anthropic/` 或 `claude-`。**非流式 `complete()` 不发该参**。
- 提示词：`task_create`/`task_update` 与当步真实工具**同一响应**（queen 提示 3 处 + task_create schema 说明 + task reminder drift 文案）；chat app 场景（看不到面板）跳过 `task_create` 的强默认、drift nudge 关闭（`chat_app_provider=lambda: session.channel`）。

| # | 检查项 | 上游（1.0.15） | Go 现状（实测） | 判定 |
|---|---|---|---|---|
| B1 | 流式请求字段 | 带 tools + 非 Anthropic → `parallel_tool_calls: true` | `stream_openai.go:99-109`：同门控（`IsAnthropicModel` 判前缀 `anthropic/`/`claude-`，与上游逐字一致）；探针 `TestParallelToolCallsSentAndLatchedOffOnRefusal` 抓请求体：第 1 发 `parallel_tool_calls=true` 且含 tools | 一致 |
| B2 | 端点拒绝 → 去掉再问一次，之后不再发 | 异常含参名 → latch + 删参 + 重试；`_parallel_tools_refused` 后不再发 | 同测试：共 3 个请求 = 被拒(400) + 无参重试 + 后续请求仍不带参（latch 生效）；`request_shape.go:28-48` | 一致 |
| B3 | Anthropic 模型不发（默认批量） | `_is_anthropic_model()` 排除 | `TestParallelToolCallsDecision`：`anthropic/claude-x` → 不发；无 tools 的请求也不发 | 一致 |
| B4 | 非流式路径 | 不发该参 | Go `Complete` 路径同样不发（只有 `stream_openai.go` 注入） | 一致 |
| B5 | 任务更新与真实工具同回合（提示词） | queen 提示 3 处 + drift 文案 + schema 说明 | `internal/queen/prompt_sections.go:144,155,161`（"in the same response as the first step's tool call" / "riding along with that step's tool calls" / Heartbeat discipline 全文）、`internal/tasks/reminders.go:308`、`internal/tasks/session_tools.go:185` 均为上游同文 | 一致 |
| B6 | 同回合行为（多 call 批次） | 一个响应可带多个 tool call；模型是否批量由模型决定（上游无独立测试） | 探针 `TestTaskUpdatesRideAlongWithARealToolInOneResponse`：一次响应 `[task_update, some_tool]` → 2 个工具同批执行（顺序保持）、**模型调用数 = 2**；下一轮请求的 assistant 消息带 2 个 tool_calls、两个工具结果按序回填 | 一致 |
| B7 | chat app 分支（跳过强默认 + drift 关闭） | 提示词 chat-app 条目；`TaskReminderSource._drift_kind` 在 chat app 返回 None；orchestrator 注入 `session.channel` | 提示词条目已对齐（`prompt_sections.go:152`）；门控（`inChatApp`）与 tasks 源测试已有；**生产已接线**（lane `g1-resid`，2026-11-22）：`Session.channel`/`ChatAppProvider` → `ReminderAgentContext` → tasks adapter 全链路打通，queen mail 线程按上游 claim 为 `"queens"` 渠道会话，dashboard 仍无渠道 | **一致（queens 渠道）**；外部渠道宿主见 §3.2 |

## 3. 差异登记（不修的原因 + 重看条件）

### 3.1 `collect_result` 长等待（A9）

- 事实：Go 的 flat/工具面保留了 `collect_result` 名字与契约文案（`internal/agentloop/synthetic_tools.go:270-283`），但执行时返回「not implemented in this build (no background tool handles)」。上游 1.0.15 改的是**已有** background tools 的等待方式；Go 没有这套机制（`LoopConfig.background_tools`、`_start_background_tool`、grace 后台化均未移植）。
- 所以「collect_result 拉长到 600s、新消息提前返回、轮询不耗回合」在 Go 上无载体；`TestAJobWaitSpendsNoModelTurnPerStatusCheck` 已把**循环侧等待**的等价行为钉住，一旦 background tools 落地，同一 `_wait_budget/_input_waiting` 机制可直接复用。
- 重看条件：G1 新增 background tools 移植项时，把 `collect_result` 的 `{status:pending}`/真实结果契约与 `wait_seconds` 预算一并实现并补探针。

### 3.2 chat-app 运行时接线（B7）——已接 `queens`，外部渠道宿主仍为前置

- 上游事实：`chat_app_provider=lambda: session.channel`（`queen_orchestrator.py`）；`session.channel` 由渠道宿主 claim 时设置（`channels.py::ChannelHost._claim`；queen mail 的线程就是 `"queens"` 渠道会话，见 `queen_mail.py` 模块注释），重启时从 `meta.json["channel"]` 恢复（`queen_orchestrator.py::create_queen`）。消费者只有 tasks reminder 的 drift 门控。
- 复核后的边界：Go 缺的不是字段而是**来源**，而 queen mail 在 Go 已是完整实现的渠道会话（`internal/server/adapters_qmail.go` 早已写 `meta.json channel="queens"`）——因此该行为可在无 Telegram/Slack 等外部连接器下单测复现，按可复现路径接线（lane `g1-resid`，2026-11-22）：
  - `internal/server/session.go`：新增 `Session.SetChannel` / `Session.Channel`（上游 `Session.channel`）；queen mail 投递（`markThread`、`DeliverReply`）按 `ChannelHost._claim` 置 `"queens"`，不再只写盘。
  - 会话恢复：`loader.go`（创建/恢复）与 `loader_preloaded_session.go` 从 `meta.json["channel"]` 恢复（`ReadSessionChannel`，上游 `create_queen` 的同字段恢复）。
  - `internal/server/chat_runtime.go`：`ChatRuntimeDeps.Channel` → `AgentContext.ChatAppProvider`（`chatAppProviderFromChannel`；无渠道时字段为 nil，dashboard 不受影响）。
  - 桥接：`agentloop.ReminderAgentContext.ChatAppProvider` 由 `NewReminderAgentContext` 透传，`taskReminderSourceAdapter` 再转发到 `tasks.TaskReminderAgentContext`（`routes_reminders.go`）。
  - 证据：`internal/server/chat_app_provider_test.go`（runtime 提供者、adapter 门控——chat app STOP 不响、面板列表仍可见、Session/meta 回环）、`internal/agentloop/reminder_chat_app_test.go`（桥接）、`internal/cli/loader_session_resume_test.go::TestCreateChatSessionResumeRestoresTheSessionsChannel`（恢复）、`adapters_qmail_test.go`（live claim）。
- 仍为前置（不修）：外部渠道宿主（QQ/onebot、Telegram、Slack 等渠道包）。Go 无 `hive.channels` entry-point 宿主（`channels_config.go` 只读配置并在 serve 时提示），这些渠道的会话在 Go 上无法产生；本次已把「渠道会话出现之后」的链路全部补好。
- 重看条件：移植渠道包宿主（entry-point 发现 + 生命周期）时，只需在每个渠道的会话 claim 点调用 `Session.SetChannel(name)`（或写 `meta.json["channel"]`），reminder/runtime 链路无需再改；并补该渠道的会话级 e2e。

### 3.3 已修的差异

| 位置 | 改前 | 改后 | 依据 |
|---|---|---|---|
| `internal/colony/runtime.go:31` `DefaultReportWait` | 60s | 600s | 上游 `ColonyRuntime.wait_for_worker_reports(timeout: float = 600.0)`；旧值会把长 worker 批次截断，逼出一次额外模型回合 |

## 4. LongMemEval e2e 同步评估（1.0.15）

上游 1.0.15 对 e2e 的改动只有一条：`169019c6`（test(e2e): LongMemEval can build the memory notes a history would have left）。

- `core/tests/e2e/longmemeval.py`：新增 `build_notes()`（`HIVE_LME_NOTES=1` 时对每个种子会话按日期从旧到新跑生产 reflection，缓存于 `notes_cache/<model>/<prompt hash>`；`HIVE_LME_NOTES_VERSION` 复用旧版本，`HIVE_LME_NOTES_MODEL` 选写 notes 的模型）；`record()` 新增 `notes`，并记录 `notes_mode` / `notes_recalled` / `notes_indexed` / `notes_read`。
- `core/tests/e2e/test_longmemeval.py`：新增 `HIVE_LME_NOTES` 开关（**默认关**）。
- 判断：**我们的跑法暂不需要跟进，理由如下** ——（1）该变更依赖 memory notes（reflection 写 notes、recall selector、INDEX/RECALL header），属 `memory-notes-1.0.15-parity`（G1，未完成），Go 侧没有可构建的 notes；（2）Go 跑法不走 pytest，而是 `cmd/hive-longmemeval`（`docs/e2e-assets/longmemeval/60q/run-go-60.sh`），没有 `HIVE_LME_NOTES` 可映射的实现；（3）上游默认关，现有 n=60 双侧结果（`2026-11-22-60q-real-run.md`）不受影响。
- 后续条件：memory notes 落地后，在 Go runner 增加等价 `--notes`/`HIVE_LME_NOTES`（按种子会话时间序 reflection、按提示词哈希缓存、结果记录 notes 数/模式/是否进对话/读了哪些），届时再评估双侧分数（上游文档：108 题上下文中「等模型挑」与「先给清单」各 101/108，直接对照 `docs/memory-benchmark.md` 口径）。
- 本项登记在 `docs/delivery-backlog.json` G1 `longmemeval-e2e-1.0.15-sync`。

## 5. 本次改动清单

| 文件 | 改动 |
|---|---|
| `go-hive/internal/colony/runtime.go` | `DefaultReportWait` 60s→600s（+ Parity 注释） |
| `go-hive/internal/playbook/python.go` | 新增 `DefaultStallTimeout()`（900s = 600+300）并在 `StartPythonPlaybook` 默认路径使用（行为等价，值可测） |
| `go-hive/internal/agentloop/loop_wait_parity_test.go` | 新增 4 条探针：等待预算语义、轮询不耗模型回合（实测 3 检查→2 模型调用）、未知 job 不等、任务更新与真实工具同回合（实测 2 call→2 模型调用） |
| `go-hive/internal/colony/runtime_wait_parity_test.go` | 新增 1 条：`DefaultReportWait == 10m` |
| `go-hive/internal/playbook/stall_budget_test.go` | 新增 1 条：静默守护默认 900s 且 ≥ 一次完整 dispatch |

工具面（`go_hive/contracts/tool-definitions.json` 等）与冻结产物未触碰：`refactor_freeze.sh compare wave15-base` 结果与基线一致（见提交说明）。

## 6. 复现

```bash
cd go_hive/go-hive
export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod
go test -count=1 -run 'TestJobWaitBudget|TestAJobWait|TestAJobThat|TestTaskUpdates' ./internal/agentloop -v
go test -count=1 -run 'TestDefaultReportWait' ./internal/colony -v
go test -count=1 -run 'TestDefaultStallTimeout' ./internal/playbook -v
# 已有上游对应探针（本次复核通过，未改）
go test -count=1 -run 'TestParallelToolCalls|TestAMessageSentDuringTheResponseStopsItsToolFromWaiting' ./internal/llm ./internal/agentloop -v
```

## 7. 静态口径（非实跑）说明

- 上游侧结论来自树内 1.0.15 源码与提交 diff（`68b6c230` / `a7ca0113` / `169019c6`）及其测试文件；未在 Python 侧重跑 `test_background_tools.py` / `test_midturn_input.py`。
- 提示词一致性（B5）用文件文本逐句核对；未经 `prompt_catalog_parity.py` 重新生成 golden。
