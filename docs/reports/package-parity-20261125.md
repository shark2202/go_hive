# go_hive ↔ hive 逐源码包一致性（当前状态）

- 复核时间：2026-11-28（eef1048cb 结构 lane Phase 3 合并后；8787 已部署最新构建；full CI 7,016/7,016）
- 基线：Python `core/framework/**` + `tools/src/**`（1.0.15）；Go `go_hive/go-hive/internal/**` + `cmd/**` + `frontend/**`（1.0.27-go.1）
- 关系：本文件是 `docs/reports/package-parity-refresh-20261124.md` 的状态刷新；详细 `file:line` 证据仍见该文件。结构化矩阵以 `docs/package-parity.json` 为准（`gen_package_parity.py --check` 已 **OK**；2026-11-27 HTTP lane 新增的 `internal/nativehttp`、`internal/netguard` 已按 **Go-only** 映射，不计入 Python parity；25 行 = 24 个产品包 + 1 个 test-only `runtime`）。
- C12 账本：**239 ported / 24 partial / 10 out_of_scope / 0 unported**（baseline `00eeecc8`；`internal/orchestrator` 的 safe_eval/validator 已转为 out_of_scope）。
- 口径：功能一致 = 模型/客户端/持久化/CLI 任一方可观测的能力存在且可调用；语义一致 = 同一输入两侧产生相同可观察行为。
- 标记：✅ 一致；◐ 一致但有已登记/窄面差异；⚠ 未闭环缺口；— 不适用。

## 0. 结论

24 组产品包对：**17 组功能逐项一致，6 组有已登记降级/部分面（config、observability、tracker、sentinel、pipeline、utils），1 组开发工具（testing，已完成移植但不计入用户面）**；语义上 **4 组逐项一致，19 组仅余已登记/窄面差异，1 组开发工具不适用**。

> **2026-11-27 更新**：C12 已到 **239 ported / 24 partial / 10 out_of_scope / 0 unported**。w182–w192 已落地 pipeline 可达性审计（API partial/生产 out_of_scope）、queen-mail Flush/thread lock、subscription token refresh、MCP R3/L3 与分页、OAuth2 Aden adapter 边界、DecisionTracker partial 守卫、global_db CLI 六命令、python_env/task_registry、Senders threads/provider seam、testing 七模块移植；w196 将 Go-only `ast_grep` 改为 opt-in 默认关闭；w197–w201 完成 trace 生产接线、PDF 引导、similarweb alias、集成账本范围和 orchestrator 死码清理；2026-11-27 11:50 HTTP lane 新增 `nativehttp`/`netguard` 三支 HTTP 工具面，已按 Go-only 记入包账本（`gen_package_parity.py --check` OK），不计入 Python parity。w197–w201 代码 HEAD `9b9f8bc68` 的 full `make ci` **RC=0**（tests 6,965/6,965、staticcheck 370/370 new 0、deadcode 441、gosec 775、compat 13/13、arch-check 一致）；HTTP lane 的 full CI 已 **RC=0**（tests **6,991/6,991**、staticcheck 370/370 new 0、deadcode 441、gosec 775、compat 13/13、arch-check 一致；HEAD `b7b06e3f2`）。随后新增 `astanalyze` 后，full CI 为 **7,004/7,004**、staticcheck 370/new 0、deadcode 441/new 0、gosec 775/new 0、arch-check 95 包 / 277 边（HEAD `96e0bc136`）；w206 将 `Analyze` 212→186 行；结构 lane Phase 3 合并后 full CI **7,016/7,016**（HEAD `eef1048cb`）。真实 Slack/Senders 外部投递、Aden/OAuth、Windows/真实 MCP 与 91 个无凭据集成仍属验证缺口，不据结构门禁宣称全部一致。

自 11-24 报告以来已闭环：
- **D-1** 用户资料镜像改走 `memory.Write`（`server/profile.go:277-281`）
- **D-2** 反射频率只接受真实整数（`server/chat_memory_wiring.go:308-336`）
- **S-2** colony pivot 恢复 fork-kickoff / pending-popup 否决（`server/chat_colony_dispatch.go:49,72`）
- **A5** turn 级 doom-loop 窗口与 `node_tool_doom_loop` 事件已接线（`agentloop/doom_loop_window.go:69`、`loop.go:1151`、`loop_guards.go:211`）
- **A39** 前端 ghost 路由/i18n 登记为 Go 侧有意改进
- **默认执行者**：colony-bound session 回到 chat queen 执行，live overseer 保持 dormant
- **P0 Stop 生命周期**：`CancelCurrentTurn` / `MarkUserStopped` 已实现（`3425aecf6`/`b5fc7ac23`），Stop 返回 `cancelled:true`，被 Stop 的 turn 落 `user_stopped` park
- **P1 cold-resume cursor**：`user_stopped` / `tool_calls_used` / `recent_tool_fingerprints` 持久化并恢复（`049738d80`）
- **trigger/webhook**：AI 校验、重启失败传播、卸载清目录、405 Allow / 空方法 404 已修（`f72af6b3e`/`335bb0a38`/`d5540e86c`/`7fff8cc3b`）
- **MCP R5**：stdio 回应 server 主动 `ping` / `roots/list` / sampling / elicitation（`bb8cccb1a`）
- **skills**：colony worker/overseer 现已遵守 queen `skills_overrides.json`
- **judge colony 激活（w195）**：移除 Go-only 的 colony goal criteria judge 激活——女王 spec 固定 `SkipJudge=true`，worker/overseer 不再 `GoalCriteriaText` 继承、不调用 `ConfigureJudge`，自然停点与显式 `report_to_parent` 都不注入 `[Judge feedback]`；通用 `Judge`/`ConfigureJudge` API 保留给 graph-era 与显式调用方。回归 `internal/server/chat_runtime_criteria_test.go`、`internal/colony/judge_wiring_test.go`、`internal/colony/overseer_judge_test.go`，与 Hive 1.0.15 上游口径一致。
- **credentials C1 更正**：两侧 store 默认都是 `StaticProvider`；真实缺口是 Aden provider/storage 与可选 `OAuth2Provider`

仍未闭环：**V-1/V-2** 验证缺口、Sentinel 真实 Slack 凭据 E2E（代码路径与假 dialer 已验证）、Senders 云/ESP 真实投递、park P2/P3、真实图片/cloud/多进程 E2E；MCP R3/L3（HTTP 首个非文本块键序）与 Go 侧 `tools/list` 分页已在 w185 关闭，Python 侧分页仍单页。Memory/Soul S1/S3 已修，S2 经上游核对为平台 no-op；trigger SSE #11 已修。C12 的 unported 已清零；剩余 7 个 testing 文件已在 w192 移植。

> **台账口径提醒**：`docs/package-parity.json` 的 `summary.open_gaps: 0` 只统计其自带的 `coverage-registry` gap 列表（当前 11 条全部 fixed），**不包含** `alignment-differences.md` 的登记差异与 §2 的开放项。不要把 `open_gaps: 0` 读成“没有已知差异”；§2 才是当前开放清单。

## 1. 逐包总表

| # | Python 包 | Go 包 | 功能 | 语义 | 当前结论 |
|---:|---|---|:--:|:--:|---|
| 1 | `agent_loop/**` | `agentloop` + `visionfallback` | ✅ | ◐ | P0-14~18、A1~A9、cold-resume cursor 已闭；残余 P2 queen park 形态、P3 冷重启验证 |
| 2 | `llm/**` | `llm` | ✅ | ◐ | thinking/StreamError/402/订阅头/Codex Responses/credits 已实现；SSE `model` 前缀、断流帧为登记值差 |
| 3 | `pipeline/**` | `pipeline` | ◐ | ◐ | 7 stage/runner 已移植；生产触发路径没有调用方，`ColonyRuntime.Trigger` 只是 API surface（out_of_scope）；无行为级双跑（V-2） |
| 4 | `schemas/**` | `schemas` | ✅ | ✅ | datetime/必填/显式 0/started_at 全对齐（A19） |
| 5 | `config.py` | `hiveconf`/`hivehome`/`server` | ◐ | ◐ | 131/137 aligned（4 go_only/1 partial/1 unread）；JSONInt/HIVE_HOME 已修；D-2 已闭 |
| 6 | `rate_limiter.py` | `ratelimit` | ✅ | ◐ | 限值/判定/SQLite 一致；`halt_campaign` omitempty 等微差 |
| 7 | `observability/**` | `logging` | ◐ | ◐ | 文件轮转/路径一致；JSON 日志/`[LEVEL]`/trace 为 Python-only（登记） |
| 8 | `host/**` | `colony`/`host`/`eventbus`/`triggers`/`playbook`/… | ✅ | ◐ | P0-1/19/20、A21~A26、S-2、trigger/webhook、Stop 生命周期、skills overrides 已闭；SSE/doom park 形态登记 |
| 9 | `tracker/**` + `trackerdb` | `tracker` | ◐ | ◐ | 运行日志/追踪子集对齐；`decision_tracker.py` 与 `stream_runtime.py` 已移植（C12 5 ported / 0 unported），但 Go 侧尚无 graph-node decide 生产调用方，不能按包名判全绿 |
| 10 | `agents/**`（queen/memory/soul） | `queen`/`memory`/`soul`/`queenmail` | ✅ | ◐ | 13 个 queen YAML 逐字节；P0-21/22、A27~A29、D-1 已闭；新增 S1 sediment/S2 云推送/S3 关停登记 |
| 11 | `credentials/**` | `credentials` | ✅ | ◐ | 本地子集一致；A15 有意保留；OAuth2 provider family、AdenSyncProvider/AdenCachedStorage、ValidateAccount 已接；credentials attach/detach 工具面与凭据目录事件已落地；C12 该包 19 ported / 3 partial / 0 unported |
| 12 | `skills/**` | `skills` | ✅ | ◐ | 25 内置文件逐字节；queen overrides 已修；内置 location 临时路径、安装事件、reload 时序、无扫描契约仍开放 |
| 13 | `loader/**` | `agents`/`mcpregistry`/`cli` | ✅ | ◐ | 默认 MCP 路径一致；R4/R5/R6 已闭合；R7 为有意扩展并已验证；A31 YAML/对象形扩展有意保留 |
| 14 | `server/**` | `server`/`session`/`sessionreport` | ✅ | ◐ | P0-3/6/7、A16/A17、D-1/D-2、Stop 生命周期、trigger 校验、streamtoken 刷新已闭；SSE iteration/model-prefix/停机尾帧登记 |
| 15 | `storage/**` | `storage` | ✅ | ◐ | 迁移/并发/checkpoint 一致；A15/A36/A38 登记 |
| 16 | `tasks/**` | `tasks` | ✅ | ✅ | P0-10、A37、A38 已修 |
| 17 | `maintenance/**` | `maintenance` | ✅ | ✅ | 四文件对应；0600 已对齐 |
| 18 | `sentinel/**` | `sentinel` | ◐ | ◐ | 引擎/配置/决策/投递/存储一致；Slack Socket Mode dialer 与冷会话恢复已落地（本地假 server 验证）；真实 Slack 凭据 E2E 未跑 |
| 19 | `global_db/**` | `globaldb` | ✅ | ✅ | client/count_cache/grid 一致；6 个 CLI 命令 `tables/query/sql/upsert/import/export` 已移植；C12 4 ported / 0 unported |
| 20 | `testing/**` | `quality`/`toolgolden`/`llmjudge` + `testframework` | — | — | 8/8 已移植；`hive test-approval` 为隐藏 dev-only 命令，不注册模型工具；不计入用户面功能 |
| 21 | `utils/**` | `atomicfs`/`pyenv`/`platform` | ◐ | ◐ | atomic_write/int 解析已对齐；`drop_launcher_pythonhome`、`TaskRegistry` 已补；C12 utils 3 ported / 1 partial / 0 unported |
| 22 | `tools/**` + `tools/src/**` | `tools` + senders/channels/… | ✅ | ◐ | 会话面 48/48；集成 102/854+9；A34/A35 已修；唯缺 DuckDB（有意）；Senders 云/ESP 缺口；91 个无凭据验证 |
| 23 | `core/hive` 公共 SDK | `go_hive/go-hive/hive` | ✅ | ◐ | 公共 facade 已落地；外部 consumer smoke 通过；不暴露 internal/HTTP serve/credentials/channels/sentinel/cloud |
| 24 | `core/frontend/**` | `frontend` | ✅ | ◐ | 233 文件全覆盖；167 字节相同/66 i18n 等；A39 已登记 |

Go-only 扩展（不属于 parity 缺口）：`lsp`、`astgrep`、`astanalyze`、`nativehttp`、`netguard`、`browserbridge`、`wxbot_channel`、`mcpregistry/nativecli`、`mcp2cli`、`longmemeval`、`go-hive-cloud`。
`orchestrator/**`：上游图引擎 out_of_scope；Go `internal/orchestrator` 无生产引用（死码），不能算接线完成（见 `package-parity.json` 的 `unwired_packages`）。

## 2. 仍未闭环

| # | 面 | 差异 | 证据 | 状态 |
|---|---|---|---|---|
| S-1 | 生态 | 公共 Go SDK 已落地（`go_hive/go-hive/hive`），结构缺口关闭 | `hive/`、`docs/public-sdk.md` | 已修 |
| S-3 | channels | `hive.channels` Python entry-point bridge 已落地；Python 插件走进程桥，非 in-process | `channels/python.go`、`channels/bridge.py`、`core/framework/server/channels.py:3-43` | 已修（进程边界） |
| V-1 | 验证 | 写操作双跑 19/134 ≈ 14.2% | `docs/parity-analysis-20261124.md` §2.2 | 验证缺口 |
| V-2 | 验证 | pipeline 无行为 cross-check；91 集成无凭据；Windows/真实 MCP 未验 | 同上 §4.6 | 验证缺口 |
| Park-P2/P3 | agentloop | queen 窗口用 server 有界 park/resume（非 Python turn 内阻塞）；真实 serve Stop/冷重启 E2E 未验 | `docs/reports/park-reminder-budget-audit-20261125.md` | 已登记残余 |
| MCP-R4 | MCP content | 已修（w173）：只认 `mimeType`；`mimeType: null`/非 string `data` 按 Python `hasattr` + `str()` 语义 | `internal/tools/mcp_client.go::SplitMCPContent`；`mcp_namespace_parity_test.go` | 已修 |
| MCP-R6 | 工具定义 | 已修（w173）：LLM 边界深度重写 `~/.hive` → 实际 `HIVE_HOME` | `internal/llm/tool_descriptions.go`；`provider.go::ToolToOpenAIFormat` | 已修 |
| MCP-R7 | Go 扩展 | reload / timeout / `tool_names` / native mcp2cli / 注册表权限（5 项均有测试证据） | `internal/server/routes_mcp.go`；`internal/mcpregistry/registry_test.go::TestInitUsesPrivatePermissions` 等 | 已验证 |
| W-Meta | colony/server | 已修（w179）：worker `meta.json` 补齐 `queen_session_id`/`queen_name`/`spawned_at`/`worker_seq`；batch 坐标改上游形态（1-based、`rpw_`/`rsw_`）；server 盘面读写一致 | `internal/colony/worker_transcript.go::writeWorkerMeta`、`internal/server/routes_colony_workers.go`；`docs/reports/worker-meta-batch-parity-20261125.md` | 已修 |
| Mem-S1 | memory/soul | Go-only `sediment`：反思结论直接写 soul，不检查 `mode: review`/pending | `internal/soul/sediment.go`、`chat_memory_wiring.go:242-296` | 已登记 |
| Mem-S2 | memory | `SchedulePush` 生产未接线，云同步实际不 push | `app.go:254` | 已登记 |
| Mem-S3 | memory/soul | `ChatRuntime.Close` 阻塞（2s+30s flush），Python 为 fire-and-forget + 6s | `internal/server/chat_runtime.go` | 已登记 |
| Cred-Aden | credentials | `AdenSyncProvider`/`AdenCachedStorage`、force-fetch resync、`ValidateAccount` 已实现；真实 Aden OAuth 未跑（无凭据） | `aden_sync.go`、`routes_credentials.go:187-205` | 已修（fake client） |
| Sent-Slack | sentinel | Slack Socket Mode dialer 已接线；真实 Slack 网络/凭据 E2E 未跑 | `socket_dialer.go`、`manager.go`、`listeners_test.go` | 已修（fake server） |
| Sent-Cold | sentinel | 冷会话恢复改为可注入 `SessionRestorer`；`ErrSentinelColdRestoreUnsupported` 仅保留在未接线 seam | `sentinel_runtime.go`、`manager.go` | 已修（未跑完整 ChatRuntime E2E） |
| Sender-Cloud | senders | 无云端池/ESP/OAuth 投递；团队级配额缺失；无 `threads.json` | `sender.go:4-6`、`engine.go:464-470` | 已登记（cloud-dependencies） |
| C-1 | 契约 | OpenAPI 仍有多数路由无响应快照/请求体 schema；AsyncAPI WS 仍 design-only | closeout 报告 | 未闭环 |
| U-1 | 未移植 | C12 已 **0 unported**；testing 7、global_db CLI、python_env、task_registry 全部已移植 | `docs/package-parity.json` | 已修复 |

## 3. 已登记/有意保留

- **A15** 凭据目录/文件 0700/0600 + 原子写、索引损坏从 `.enc` 重建。
- **A30** `skill install` 拒绝越界名称/子目录。
- **A31** worker 定义支持 YAML/对象形 tools/node_type 等扩展。
- **A39** 前端 ghost 路由渲染 not-found、`refresh(): Promise`、i18n 包装不回流 Python。
- **SSE** 每回合 envelope +36 帧、`iteration` 会话累计 vs 每回合、`model` 前缀、`context_usage` 五字段、DELETE 停机尾帧。
- **logging** `LOG_FORMAT=json`、`[LEVEL]`、trace 上下文 Python-only。
- **图引擎** `orchestrator`/`node_worker`/`edge`/`agent_host` 整体 out_of_scope；Go 用 colony+agentloop。
- **工具** `credentials` / `collect_result` 已从 `not_implemented` 补齐；工具面仍受冻结 schema 看守。
- **云** `streamtoken_refresh` 已移植并接入 serve/LLM 401 重放；Aden 云端 token 拉取与真实 cloud E2E 仍未跑。
- **MCP** R7 为有意扩展并已验证；R4/R5/R6 已闭合。
- **Memory/Soul** S1 sediment、S2 云推送、S3 关停；`message_inventory` 字节/字符口径。
- **Trigger** NaN/Infinity、头名拼写、超限 drain 等 wire 形态微差。
- **表示层** 数值字面量、`session_cwd`、部分时间格式。

## 4. 本轮方法

- 直接读码/契约复核：`alignment-differences.md`、`package-parity.json`、`docs/reports/{park-reminder-budget-audit,trigger-webhook-parity,mcp-parity,memory-soul-parity,skill-parity-audit,senders-sentinel-parity}-20261125.md`。
- 变化核验：`e605e676a`（image preview/artifact/SSRF）、`39232b804`（credentials 工具面/事件）、`72295f8fb`（streamtoken）、`2e85653ff`（DecisionTracker/stream_runtime）、`582a785e8`（Sentinel Slack/冷恢复）、`14fb630c7`（NewApp budget）、`a8974b1ff`（trigger SSE replay）、`97ac3d593`（MCP R4/R6）、`64d5d2a2a`（Memory/Soul）、`cb1a2c67a`（storage lifecycle）。
- 已运行：代码 HEAD `9b9f8bc68` 的整树 full `make ci` RC=0（6,965 tests、staticcheck 370、deadcode 441、gosec 775、compat 13/13、arch-check 一致、C12 239/24/10/0）。未运行：真实第三方集成、真实 Slack/图片 provider/cloud E2E、Windows 实机、多进程 DecisionTracker E2E。本报告不宣称这些面已验证。
