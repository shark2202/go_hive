# Python Hive ↔ Go Hive 逐源码包一致性审计

- 审计日期：2026-11-25
- Python 参考：`core/framework`、`core/hive`、`tools/src/*`（workspace 工具包）
- Go 目标：`go_hive/go-hive/internal/*`、`go_hive/go-hive/hive`
- 审计快照：会话开始 `672ba6229` → 定稿时 `0bc5f0f49`（期间仅治理报告、parity 刷新与 credentials 测试新增）
- 方式：只读源码逐行对照 + 仓库内已冻结的实测契约；未修改任何源码

> **2026-11-25 晚更新：** w166–w171 合并后本页快照已过期；当前 C12 为 **228 ported / 27 partial / 8 out_of_scope / 10 unported**，full CI RC=0。以 `docs/reports/package-parity-20261125.md` 与 `contracts/module-inventory.json` 为准。

## 0. 结论摘要

**对外契约层已达到可发布的实测一致；执行层不是逐语义等价。**

| 层 | 判定 |
| --- | --- |
| 对外契约（HTTP/SSE/CLI/prompt/skill catalog/事件类型） | 实测一致（见 §3） |
| 核心运行时（agentloop/colony/server） | 主体等价，存在 15+ 处可观察语义差异 |
| 图编排（orchestrator/execution_manager） | 结构性缺口，Go 以 colony `run_worker` 扇出取代 |
| 工具/集成 | 同名契约 39/50 对齐；3 处功能面缺口 |
| 公共 SDK | Python 47 个导出中 33 个有对应，14 个缺失（多为有意裁剪） |

模块账本（`contracts/module-inventory.json`，当前 HEAD 已由治理任务刷新）：
273 个 Python 模块 → **225 ported / 27 partial / 12 unported / 9 out-of-scope**。
其中 partial 约半数只是包初始化/re-export 面，不构成行为缺口。

## 1. 总量与映射

| 侧 | 规模 |
| --- | --- |
| Python `core/framework` | 25 包 / 351 文件 / ~123k 非测试 LOC |
| Python `tools/src` | `aden_tools` 71.4k、`gcu` 17.6k、`terminal_tools` 3.9k、`memory_tools` 2.7k、`chart_tools` 1.1k |
| Go `go-hive/internal` + `hive` | 56 包 / ~969 文件 / ~230k 非测试 LOC |

**关键映射修正**：Python 内置工具的真实实现在 `tools/src/aden_tools`（324 文件/71.4k），
`core/framework/tools` 只是装配壳（7.2k）；Go 对应 `internal/tools`（318 文件/83.4k）。

## 2. 逐包判定

| # | Python 包 | Go 对应 | 判定 | 核心依据 |
| --- | --- | --- | --- | --- |
| 1 | `agent_loop` | `agentloop` + `attachments` | 核心一致，6 处语义偏差 | 循环/提醒/持久化/合成工具高保真；压缩管线、空响应、set_output、cursor、judge、token 计量有差异 |
| 2 | `agents`（queen/soul/memory） | `agents` + `queen` + `soul` + `memory` | 高保真等价 | 13 个 queen YAML 逐字节；prompt sections `--check`；memory/recall/reflection/timeline/soul 逐项对应 |
| 3 | `credentials` | `credentials` | 高保真，边界明确 | 118 specs `--check` OK；113/114 字段一致；4 个 OAuth provider 已补齐；交互式 setup CLI 未移植 |
| 4 | `debugger` | `cli/debugger.go` | 等价 | 仍 exec `python3` |
| 5 | `global_db` | `globaldb` | 客户端等价，CLI 缺失 | 无 `hive-global-db` |
| 6 | `host` | `host` + `colony` + `eventbus` + `triggers` + `session` + `playbook` + `channels` + ... | 主路径等价，图宿主缺失 + 4 处运行时偏差 | 停止收敛、BrowserReaper、meta lineage、批次坐标 |
| 7 | `llm` | `llm` + `visionfallback` | 等价（最强证据） | `model_catalog.json` 字节一致；SSE/限速实测；细节：ResponseFormat/antigravity/NFKC |
| 8 | `loader` | `mcpregistry` + `tools` + `agents` | 基本等价 | MCP 注册表格式一致；`preload_validation` 属图路径；`agent_loader` 部分 |
| 9 | `maintenance` | `maintenance` | 等价 | Janitor 规则对应 |
| 10 | `observability` | `logging` + `runtimelogs` + `runtimehealth` | partial | 结构化 logger 缺失 |
| 11 | `orchestrator` | `orchestrator` | 结构性缺口 | 图执行引擎缺失；Go 包 0 生产导入 |
| 12 | `pipeline` | `pipeline` | 编排等价，BYOK 缺 | 缺 credential-store / endpoint key |
| 13 | `runtime` | — | 不适用 | 仅测试壳 |
| 14 | `schemas` | `schemas` | formats 等价，wire partial | 空时间戳（goal `id` 已按必填常驻） |
| 15 | `sentinel` | `sentinel` | 主路径等价 | Slack 入站缺失；notifier/store 细节 |
| 16 | `server` | `server` + `session` + `sessionreport` + `channels` + `claudecode` + `queenmail` | 路由完整，语义 6 处 P0 | SSE 背压/在线计数/凭据事件/streamToken/trigger 重放/hooks |
| 17 | `skills` | `skills` | 逐字节一致 | 25 文件/229,354 bytes；catalog 10/10 |
| 18 | `storage` | `storage` + `hivehome` | 布局等价，边界差异 | HIVE_HOME 解析、空时间戳（会话/运行/摘要文件权限已对齐 0600） |
| 19 | `tasks` | `tasks` | 基本等价 | 提醒源注册残余；bool/meta 边界 |
| 20 | `testing` | `llmjudge` + `toolgolden` | 1/8 移植 | 7 个开发工具缺失（P3） |
| 21 | `tools`（含 `tools/src`） | `tools` + `charts` + `senders` + `playbook` + `queenmail` | 契约 39/50 对齐，3 处功能面缺口 | `credentials` 只读化、`csv_sql`/`excel_sql` 缺失、browser 32→22 |
| 22 | `tracker` | `tracker` + `runtimelogs` | 运行日志等价，DecisionTracker 缺 | 图路径遗留 |
| 23 | `trackerdb` | `tracker` | 等价（合并） | — |
| 24 | `utils` | `atomicfs` + `platform` + `hiveclock` + `pyenv` + `arghelp` | 基本等价 | `python_env`/`task_registry` 未移植 |
| 25 | `core/hive` 公共 SDK | `hive` | 33/47 有对应，14 缺失（多为有意裁剪） | facade 已落地；`AgentLoader` 与图缺口同源 |

**Go-only（非缺口）**：`lsp`、`astgrep`、`quality`、`arghelp`、`hiveclock`、`release`、
`hive-browser`、`hive-longmemeval`、隐藏 `queen-mail-mcp`。

## 3. 实测一致证据（可放心）

| 维度 | 结果 | 证据 |
| --- | --- | --- |
| 首轮 system prompt | 21,584 chars 整段逐字一致，diff 0 字节 | `docs/prompt-parity.md`、`docs/e2e-assets/prompt-catalog/` |
| Skill catalog 渲染 | 10/10 逐字一致（含 NBSP、300/304 截断、symlink、相位过滤） | 同上 |
| 内置技能 | 25 文件 / 229,354 bytes 逐字节 | `contracts/builtin-skills.json` |
| Queen 人格 YAML | 13/13 逐字节 | `check_queen_profiles.py --check` |
| Queen prompt sections | 生成器 `--check` 通过，golden 一致 | `internal/queen/testdata/prompt/*` |
| 模型目录 | `model_catalog.json` 字节一致（12 providers / 8 presets / 54 models） | `cmp` |
| Credentials specs | `--check` OK；运行时 113/114 字段一致；tool_provider_map 817/817 | `a7832ddc…` |
| 事件类型 | 77/77 值集合相同 | `event_bus.py:102+` vs `eventbus/event_type.go` |
| HTTP 路由 | Python 222 / Go 226；220 条共同；1 条 alias；5 条 Go 扩展；method mismatch 0 | `contracts/api-routes.json` |
| API 返回值 | 107 端点 / 8,716 值路径 / 0 未声明差异 / 46 声明差异 / PASS | `contracts/api-responses/value-golden.json` |
| SSE 帧 | live 34/34、replay 17/17、history 42/42 相等 | `contracts/sse-frames/summary.txt` |
| CLI | `hive` 47/47、`hive-browser` 27/27、flag 全等、默认值 0 差异 | `contracts/cli-surface.json` |
| Reminder 源 | 15/15 完全对齐 | `docs/reminders-and-nudges.json` |
| MCP 注册表 | 路径、`installed.json`/`config.json`/缓存布局一致 | `mcp_registry.py:80-83` vs `mcpregistry/registry.go:103-118` |
| 会话存储布局 | `sessions/session_*/state.json` + 原子写 + 穿越校验一致 | 两侧 session_store |

SSE 的 3 个**有意差异**：
1. `iteration`：Python 会话累计，Go 每回合重置。
2. `llm_turn_complete.model`：Python 带 litellm `provider/` 前缀，Go 带配置原值。
3. `context_usage_updated.usage_ratio`：由字符估算推导，底层字符数不同。

## 4. 差异清单

### 4.1 P0（功能会坏 / 会挂死）

| # | 问题 | Python | Go | 后果 |
| --- | --- | --- | --- | --- |
| 1 | `background_tools` / `collect_result` 缺失 | `internals/types.py:219-242`、`agent_loop.py:6613` | 无分支 `loop.go:1216-1238`；仅 schema `synthetic_tools.go:271` | `image_generate` 撞 60s；`collect_result` 广告不可执行 |
| 2 | 空响应策略不同 | 输出齐全即 ACCEPT（`agent_loop.py:2474-2510`） | 一律计数→nudge→阈值 park（`turn_text_guards.go:100-124`） | 有 output_keys 的 worker 不会因“输出已齐”成功结束 |
| 3 | Colony 停止无终态 | `STOP_TIMEOUT_SEC=10`，超时强制 STOPPED（`host/worker.py:66,837-900`） | `Stop()` 无界 `wg.Wait()`（`colony/runtime.go:653-663`）；超时只记 `timed_out`（`worker_surface.go:166-220`） | 吞 cancel 的 worker 让 colony 关闭挂起 |
| 4 | `BrowserReaper` 零调用 | per-worker + colony 停止双路径（`worker.py:782-835`、`colony_runtime.py:2593-2617`） | 实现完整但无生产引用（`colony/reap.go:19-86`） | Chrome tab group 泄漏 |
| 5 | Pipeline LLM 取键链缺两步 | `get_api_key` 含 credential-store + endpoint（`pipeline/stages/llm_provider.py:73`、`config.py:335,841`） | 仅 pool→literal→subscription→env（`pipeline/stage_llm_provider.go:117-176`，注释自认） | 仅靠凭据库/endpoint 配置的部署拿到空 key |
| 6 | `credentials` 只有只读 `credential_status` | `agent_loop.py:1592`、`internals/credential_tool.py:129-145`（help/collect/attach/detach） | `session_tools_credentials.go:26`；collect 靠自动 park（`chat_credential_park.go:43-75`）；attach/detach 无 agent 入口 | agent 不能主动收集/钉住凭据 |
| 7 | ~~SSE 背压不等价~~ **已修** | ~~queue=1000，关键事件满则断开（`routes_events.py:245-291`）~~ | ~~queue=256，全部静默丢弃~~ → `sessionEventsQueueSize=1000`，关键事件满断开（`chat_routes.go:24-30,264-289`） | 已不再丢 `turn_done` 等状态迁移 |
| 8 | ~~`SSEClientCount` 只读不写~~ **已修** | ~~连接 +1/断开 -1（`routes_events.py:313,465`）~~ | `atomic.Int64`，SSE 连接 +1/断开 -1（`session.go:121-125`、`chat_routes.go:299-300`） | `HasAttachedUI` 可正确反映在线 UI |
| 9 | `streamtoken_refresh` 缺失 | 启动后台刷新（`app.py:1069-1072`、`streamtoken_refresh.py`） | 全仓无引用 | 云部署 24h 后 LLM 调用失败 |
| 10 | 凭据/工具目录事件未发射 | CONNECTED/DISCONNECTED + TOOL_CATALOG_REFRESHED（`routes_credentials.py:74-90`） | 类型已定义，生产无发射（`routes_events.go:54` 仅白名单） | 跨标签页不刷新 |
| 11 | 触发器 SSE 重放不等价 | 连接时从 live session 重建全量 trigger（`routes_events.py:174-208,418-422`） | 仅 snapshot + ring replay（`chat_routes.go:264-279`） | 新开 colony 页面可能看不到完整 trigger |
| 12 | `stream_runtime` / `DecisionTracker` 缺失 | `host/stream_runtime.py`、`tracker/decision_tracker.py`；被 colony/execution_manager 引用 | 无对应 | 图路径的决策账本不可用（主循环不读） |

### 4.2 P1（可观察语义偏差）

**agentloop**
- ~~压缩兜底少两级：Python truncate→microcompact→check→prune→check→LLM；Go `compactConversationIfNeeded` 只做 truncate→check→LLM（`loop.go:431-503`），更频繁调用摘要模型。~~ **已修（w176）**：`compaction_run.go` 按上游顺序执行 truncate→microcompact→check→prune→check→LLM，免费阶段恢复预算即短路；`CONTEXT_COMPACTED` 现在每条退出路径都发（含免费阶段与摘要失败）。
- ~~API input tokens 未使用：`UpdateTokenCount` 全仓零调用（`conversation.go:717`），只靠字符估算。~~ **已修（w176）**：`_run_turn_loop` 的 FinishEvent 点位现在调用 `recordPromptTokenCount`（仅 `input_tokens > 0` 时覆盖估算），与上游同点位。
- ~~`set_output` 语义相反：Python 拒绝（`agent_loop.py:4860-4867`）；Go 执行（`synthetic_tools.go:490-531`）。~~ **已修（w176）**：活循环在 executor 之前拒绝并返回逐字错误 `set_output is no longer available. Report your results via conversation instead.`；builder/handler 保留为遗留合成面（上游同样无调用方）。
- ~~cursor 缺 `iteration`/`outputs`/`node_id`~~ **已修（w177/cursor-replay）**：`cursor_resume.go` 按 `cursor_persistence.py::write_cursor` 的 JSON 形状写入 `iteration`（数字）/`node_id`（字符串）/`outputs`（对象，空为 `{}`），每迭代边界与 turn 边界 checkpoint；restore 恢复三者并按 `iteration+1` 续跑，缺字段/旧形状取上游默认（`outputs≈{}`、`node_id≈""`），无 `iteration` 的旧 cursor 从 0 起算。回归 `TestCursorCheckpointPersistsIterationAndNodeID`、`TestCursorCheckpointRoundTripsOutputs`、`TestCursorRestoreDefaultsMissingFields`、`TestCursorRestoreToleratesLegacyShapes`、`TestCursorOutputsPreferTranscriptAndKeepRestoredFallback`。
- ~~重放断路器跨轮被放行~~ **已修（w177/cursor-replay）**：`applyReplayBreaker` 不再按 stream 是否有升级通道放行跨轮重复，阈值即拒、文本轮为断链边界，与 `agent_loop.py:5477-5492` 一致；旧契约测试 `TestCrossTurnRepeatsStayWithTheDoomLoopDetector` 由 `TestCrossTurnReplayBreakerRefusesWorkerThirdIdenticalCall`、`TestCrossTurnReplayBreakerResetsAtTextOnlyTurn` 取代，负向回归覆盖豁免 poll 与 detector 关闭。Go 任务提醒路径所需的只读 `task_list`/`task_get` 仅对断路器豁免（仍受 doom-loop detector 计数），见 `portReplayBreakerExemptToolNames`。
- Judge：工具失败强制评估、隐式门抢先自定义 judge、ACCEPT 缺键不覆盖、提示语料不同（`judge.go:143-150,211-257`、`loop_judge.go:41-44`、`llm_judge.go:77-90`）。
- ~~`context_compaction_started` 无发射（`event_type.go:90` 仅类型）；POST_COMPACT 仅成功触发（`loop.go:476-502`）。~~ **已修（w176）**：新增 `AgentLoop.OnContextCompactionStarted`（payload：`usage_before`/`message_count`/`llm_compaction_skipped`），chat runtime 发布到会话总线；POST_COMPACT 与 `CONTEXT_COMPACTED` 在所有 pass 出口触发。
- 截断参数 `_raw` 保护缺失（`tool_input.go:11-23`）；worker `ask_user` 必须带 options 规则缺失。
- `max_output_value_chars` / `cf_grace_turns` / 后台工具字段缺失；doom-loop 阈值硬编码 3/5。
- CJK 按字节截断（`compaction.go:105-106,255-257,271-273` vs Python 码点）。
- 硬停提醒写入顺序在工具结果之前（`loop.go:927,932,1028`）。

**colony / host**
- worker `meta.json` 缺 `queen_session_id/queen_name/spawned_at/worker_seq`，而 server 读取它们（`worker_transcript.go:68-85` vs `colony_runtime.py:1774-1788`；`routes_colony_workers.go:347-379`）。
- 批次坐标：Python 1 基 + `rpw_<ts>_<uuid8>`；Go 0 基 + `batch_N`（`runtime.go:774` vs `colony_runtime.py:2068-2071`）。
- 每 worker 身份：cwd 有 factory 且已接线（`loader_colony_host.go:112`），但 `usage_agent_id/principal/profile/colony_id/binding` 无注入路径。
- queenmail 持久队列 `Flush` 未接生产（`mail.go:427-469` 仅测试）；缺 per-thread 锁（`adapters_qmail.go:182-222`）。
- `senders` 仅本地 SMTP，ESP/OAuth 直接拒绝（`sender.go:1-7`、`engine.go:188-239`），无回复路由（对照 `providers.py:267-346`、`threads.py:99-163`）。
- `queen_tools.always_enabled_categories` 不生效：会话 eager 面固定 24 个（`chat_runtime.go:135-141`、`tool_surface_flat.go:334-348`）。
- 订阅 token 只读不刷新（`hiveconf/subscription.go:26-43`），Claude Code/Codex 过期后失败。

**server**
- ~~PDF 文本不提取~~ **已修**：≤10MB 的 PDF 上传按页提取并返回 `[PDF page N]` 文本（`routes_session_attachment.go::pdfTextPreview`，复用 `internal/tools/integrations/pdf.ExtractTextPages`）；>10MB 与解析失败保持空文本。测试：`TestUploadAttachmentExtractsPDFText`、`TestUploadAttachmentSkipsExtractionForLargePDF`、`TestUploadAttachmentToleratesUnreadablePDF`。
- ~~JSON body：Python 20MB/413，Go 8MiB/400~~ **已修**：共享上限改为 20 MiB（`http_limits.go`），超限路由返回 413 `Request body too large`；`decodeJSONValue`/`decodeChatBody`/trigger/config/memory/CRM 读取全部走同一上限。测试：`TestReadLimitedBodyAcceptsExactlyTheCap`、`TestCreateSessionOversizedJSONIs413`、`TestOversizedJSONIs413OnUncaughtRoutes`。
- ~~畸形 JSON：Python 部分 500，Go 统一 400~~ **已修**：上游未捕获 `request.json()` 的路由（chat、queen-context、record-message、mark-colony-spawned、load-colony、credentials 保存/check-agent）返回 500 `Internal server error`；上游 `except Exception` 的路由保持各自 400。测试：`TestMalformedJSONIs500WhereUpstreamLeavesItUncaught`、`TestCaughtJSONErrorsStay400`、`TestLoadColonyRejectsBadBodies`。
- ~~`stats`：Python 无 colony 返回 `{}`，Go 返回完整 baseline~~ **已修**：无 colony（或 colony 无 stats provider）返回 `{}`；colony 存在时仍做字段级合并。测试：`TestSessionStatsQueenOnlyWithoutColonyIsEmpty`、`TestSessionStatsQueenOnlyRunningWithChat`。
- ~~`MarkLoading/ClearLoading` 无调用点~~ **已修**：`MarkLoading` 改为原子 check-and-set；`createSessionWithRuntime` 的 colony 创建与 `colonyLifecycle.attach` 的 load_colony 均标记/清理，重复加载返回 409 `currently loading a colony`。测试：`TestCreateChatSessionRejectsConcurrentColonyLoad`、`TestLoadColonyHonoursAndClearsLoadingMarker`。
- 附件上传上限：Python 无显式限制，Go 100MiB。
- ~~features 热应用：`ConfigRouteDeps.Sessions` 未注入，`colonies_applied` 恒 0~~ **已修**：`configRouteDeps` 透传 `AppDeps.Sessions`，loader 快照携带 live colony runtime（`featureSessionsSnapshot`），`Host` 转发 `AdaptiveToolBudget/SetAdaptiveToolBudget`。测试：`TestFeatureUpdateHotAppliesThroughAppDepsSessions`、`TestFeatureSessionsSnapshotCarriesColonyRuntime`。
- 冷会话 `created_at`：Python `st_ctime`，Go `ModTime`。
- MCP 列表多 `connected/tool_names`；SSE heartbeat 15s vs 20s。
- 历史删除非法 ID：Python 200，Go 400/500；创建会话 Go 额外校验。

**storage / schemas / config**
- ~~文件权限 0644 vs 0600（`concurrent.go:394`、`conversation_store.go:43`、`session_summary.go:267`、`retention.go:815`）。~~ **已修**：四处改为 0600（对齐 Python `framework/utils/io.py::atomic_write` 的 mkstemp 语义）；`internal/storage/permissions_test.go`、`internal/maintenance/permissions_test.go` 逐路径断言 0600 且无 group/world 位（非 POSIX 平台跳过）。
- ~~`goal.id` 用 `omitempty`（`schemas/goal.go:29,121,167`），空 id 产出被 Python pydantic 判为缺必填。~~ **已修**：三个 `id` 去掉 `omitempty`，空 id 输出 `"id":""`（Python 接受的值），缺失/null id 在解析时被拒；`internal/schemas/goal_test.go` 正反两向钉住。
- CRM 水位 API 缺失（Python `host/tracker_db.py` + `tracker_tools.py:50,211` 无条件引用）。
- HIVE_HOME：Python 导入时冻结 + `expanduser()`；Go 每次读 + `filepath.Abs`。
- ~~`get_max_tool_result_chars` 仅接受可解析整数~~ **已修**：`hiveconf.MaxToolResultChars` 按 Python int 字段的强制转换接受数字字符串（`"4096"`、`" 4096 "`），空值（null/`""`/空白）与不可解析值保留调用点默认值（0 会关闭截断，不得回落为 0）；`internal/hiveconf/hiveconf_test.go`、`internal/server/config_slots_test.go` 钉住。空时间戳字符串会让 state 不可读（未修）。

**llm / sentinel / tools**
- LLM `CompletionRequest` 缺 `ResponseFormat`；antigravity 事件时序/超时；key_check 缺 NFKC。
- Sentinel Slack 入站缺失；notifier 接线；store 时间戳/排序。
- `csv_sql`/`excel_sql` 未移植；browser 32→22（10 个交互动作折叠进 `browser_interact`）。
- `credentials` flat 面广告但 `not_implemented`；`collect_result` 同（`suggest_colony` 有 agentloop 拦截，实际可用，提示是死路径）。

### 4.3 P2（低影响 / 实现差异）
- `WriteJSON` 追加换行；Python 按 run 键 async lock vs Go 单 `ioMu`；checkpoint 索引损坏 Go 重建、Python 返回 None；`migrate_v2` 不保留 mtime。
- `HIVE_HOME=""` 语义；会话 slug 允许 emoji；task `id` 缺省 0；`goal=""`；`task_list` 的 `required: []`。
- 运行时日志 error 字段；LLM 日志时间戳格式；reminder 键序；globaldb 空响应体；grid 字面量格式。
- `WorkerStatus.PENDING` 未导出且内部基本不可达（`worker.go:426-434` 直接 Running）。
- `internal/session` 存在与当前 schema 不兼容的过时重复实现（正确实现在 `internal/schemas` + `internal/storage`）。
- 前端同源分叉：`core/frontend/src` 233 vs Go 255 文件，66 个同名文件不同，16 个 Go-only（i18n、PaymentRequiredCard、ColonyNameField）。
- Go 二进制仍依赖 Python 跑 playbook/channel bridge/debugger（`playbook/python.go`、`channels/doc.go:27-50`、`cli/debugger.go:80-157`）。
- 安装流程不含 OAuth 登录/模型配置（`quickstart.sh:1480,1569` vs `install.sh:1-30`）。

## 5. 误报与已更正

1. **“chart_render 是无实现占位”不成立**：`session_tools.go:452 executeChartTool` 先于 `:549 executeHarnessPlaceholder`，`charts.go:216` 是真实 Chrome 渲染；占位分支不可达。
2. **“Go 网络/5xx 不重试”不成立**：`llm/stream_retry.go:11-48` 对 500/502/503/504、超时、连接重置、EOF 重试 3 次。真实差异是预算（Python 瞬时 5 次 + 容量 600s；Go 3 次、无 600s）。
3. **“schedule_push 未接线是 P0”过度归因**：Python 的 `framework.cloud_sync` 在本 checkout 不存在，`schedule_push` 本身就是 no-op；Go nil hook 等价。真正缺的是 ConnectedProviders、PublishToolsConfigChanged、凭据事件。
4. **“核心 36 工具 0 个 schema 相同”已过时**：`docs/tool-consistency-audit.json` 比较的是注册表原始 schema；11-23 的 `internal/cli/tool_contract_parity.json` + overlay（`tool_contract_parity.go:92-106`，`session_tools.go:285`）已让 39/50 模型可见契约对齐，剩余 10 个 terminal + 1 个 exa。
5. **`architecture/model.yaml` 的 chart 注释已过时**（仍写“未接线”）。

## 6. 未验证项

- 全量 SSE 事件逐字段 schema/顺序（仅核对 snapshot、默认类型、replay 集合、帧格式）。
- LLM compaction 的摘要文本等价性。
- cron 方言（croniter vs Go parser）。
- MCP 健康检查/子进程/mode=cli 深度语义。
- Queen Mail 的 depth/daily_limit/mail.jsonl 语义。
- browser panel 响应、tar 导入安全规则。
- credential specs 全量集合、agent discovery 字段值。
- 运行时并发/背压细节、性能。

## 7. 修复优先级

### 已实施（2026-11-25，本次）

| 项 | 改动 | 测试 |
| --- | --- | --- |
| cursor 补 `iteration`/`outputs`/`node_id` | `cursor_resume.go` 按上游 JSON 形状写入并在 restore 时恢复；`iteration` 参与 `max_iterations` 续算，`outputs` 以会话为准、restored 值为 compaction 丢键兜底，`node_id` 取 `ctx.agent_id`；缺字段/旧形状取默认 | `TestCursorCheckpointPersistsIterationAndNodeID`、`TestCursorCheckpointRoundTripsOutputs`、`TestCursorRestoreDefaultsMissingFields`、`TestCursorRestoreToleratesLegacyShapes`、`TestCursorOutputsPreferTranscriptAndKeepRestoredFallback` |
| 重放断路器跨轮强制 | 删除 stream 升级通道放行；跨轮第三同参调用在 dispatch 前拒绝并 park `doom_loop`，文本轮为断链边界，与 `_run_turn_loop` 一致；只读 `task_list`/`task_get` 仅断路器豁免（detector 仍计数），保证 worker task reminder 回合可完成 | `TestCrossTurnReplayBreakerRefusesWorkerThirdIdenticalCall`、`TestCrossTurnReplayBreakerResetsAtTextOnlyTurn`、`TestCrossTurnReplayBreakerStillExemptsPolls`、`TestCrossTurnReplayBreakerHonorsDisabledDetector`、`TestWorkerTaskReadsSurviveRepeatedReminderTurns` |
| `image_generate` 60s 截断 | `ToolTimeoutOverrides` 增加 `image_generate: 235`（`loop_config.go`，零值 fallback 同步），作为 background handle 落地前的 stopgap | `TestLoopConfigPrefixTimeoutResolution` |
| SSE 关键事件背压 | 会话 SSE queue 256→1000；新增 `_CRITICAL_EVENTS` 等价集合，队列满时关键事件断开连接而非静默丢弃；heartbeat 20s→15s（`chat_routes.go`） | `TestChatSessionEventsStreamAndHistory` |
| `SSEClientCount` 恒 0 | 改为 `atomic.Int64`，SSE 连接 +1、断开 -1；Sentinel 读取改为 `Load()`（`session.go`、`chat_routes.go`、`sentinel_runtime.go`） | 同上（断言 streaming=1、断开后=0）+ sentinel 包测试 |

验证：`go test ./internal/server ./internal/agentloop ./internal/sentinel -count=1` 全部通过。

**A. agentloop（5）**
1. `background_tools` + `collect_result`（`image_generate` 超时已 stopgap；handle/collect 机制仍未实现）
2. 空响应策略
3. ~~压缩兜底补 microcompact → prune → LLM~~（w176 完成，`compaction_run.go` + `compaction_pipeline_test.go`）
4. ~~API input tokens 接入 `UpdateTokenCount`~~（w176 完成，`recordPromptTokenCount`）
5. cursor 补 `iteration`/`outputs`；~~`context_compaction_started`~~（w176 完成事件发射；cursor 字段仍缺）

**B. colony（3）**
6. 停止收敛（10s 终态 + 有界 `Stop`）
7. `BrowserReaper` 接线
8. worker meta lineage + 批次 1 基/`batch_id`

**C. server（4）**
9. ~~SSE 关键事件队列满断开 + `SSEClientCount` 增减~~（已完成，见上）
10. `streamtoken_refresh`
11. 凭据/工具目录事件发射；features sessions 注入
12. 触发器 SSE 全量重建

**D. LLM/配置/工具（3）**
13. pipeline credential-store/endpoint key；订阅 token refresh
14. `always_enabled_categories` 生效
15. 工具面决策：`credentials` collect/attach/detach、`csv_sql`/`excel_sql`、browser 折叠

**横切**：文件权限 0600；goal `id`；CRM 水位；BM25 `n` 口径；`match_source`；HIVE_HOME；空时间戳；frontend 双份同步；测试缺口（testing 框架、ExecutionStream、streamtoken refresh、live cache）。

## 8. 审计方法与限制

- 8 个并行只读代理 + 主审交叉复核；对高严重度条目逐条回读源码。
- 不把 `// Parity:` 注解当作正确性证据；注解只用于定位。
- 证据等级：实测（双跑/逐字/帧级）> 两侧代码对照 > 单侧/接口推断。
- 审计期间一个子代理曾重生成 3 个受版本控制的 parity 文件，已还原；随后治理任务又提交了刷新版本。
- 审计阶段未修改源码；定稿后按 §7「已实施」落地了 3 项修复（源码已修改，测试通过）。
