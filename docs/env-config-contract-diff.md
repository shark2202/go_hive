# 环境变量与 configuration.json 契约差异（Python 基准 → go_hive）

- reference: `python@bd5041862d5df219a029b190141dda3db8c7eeab`（Hive 1.0.14）
- target: `go_hive@bd5041862d5df219a029b190141dda3db8c7eeab`（1.0.26-go.1）
- 生成器：`go_hive/scripts/freeze_env_config_contract.py`（确定性；`--check` 可重放）
- 机器可读：`go_hive/contracts/env-vars.json`、`go_hive/contracts/config-keys.json`

## 0. 结论摘要

- 环境变量清单以已发布的审计（`go_hive/docs/env-surface-audit.json` @ 622cc236）为基线，本次按同一口径独立复算：Python **89** / Go **118** / 共有 **83**；Python 独有 **6**，Go 独有 **35**。复算与「本车道期望」一致。与发布审计基线（Python 89 / Go 96 / 共有 66 / Python 独有 23 / Go 独有 30）相比，差额来自 16 个名字由 Python 独有转为共有：`HIVE_MAX_ITER`、`HIVE_SPLIT_WORKER_EVENTS` 由本车道新增 Go 读取点，`HIVE_AGENT_NAME`、`HIVE_COMPACTION_DEBUG`、`HIVE_ENABLE_LLM_LOGS`、`HIVE_HISTORY_READ_WORKERS`、`HIVE_NO_FILE_LOG`、`HIVE_PLAYBOOK_FAST_GRACE`、`HIVE_RESOURCE_CRIT_AVAIL_FRAC`、`HIVE_RESOURCE_CRIT_RENDERERS`、`HIVE_RESOURCE_HISTORY`、`HIVE_RESOURCE_INTERVAL_S`、`HIVE_RESOURCE_WARN_AVAIL_FRAC`、`HIVE_RESOURCE_WARN_RENDERERS`、`HIVE_SENTINEL_LOG`、`HIVE_TRACKER_WORKER_TOOL_CALLS` 在审计提交之后、本车道之前已进入 Go 树；Go 独有清单 30→35（`HIVE_PYTHON` 新增、`HIVE_NETWORK_SCANNERS` 移除，另有审计后进入 Go 树的开发/评估变量）。
- configuration.json：**135** 个键 / **21** 个 section；其中 `aligned` 126，`go_partial` 4，`python_only` 0，`go_only` 4，`unread` 1。
- 本车道（config-verbatim）把主槽位 `llm.*` 的消费键接进运行时：会话构建统一走 `MainSlotProvider`（键序 = endpoint → api_keys 池 → literal → claude/codex/kimi/antigravity 订阅 → 凭据库 → `api_key_env_var` → provider env；`MainSlotAPIBase` 复刻订阅端点与 openrouter 默认；`extra_body`/`num_ctx`/订阅请求头随 provider 构建下发），`llm.max_tokens` 预算 queen 回合、`llm.max_context_tokens` 参与 queen 窗口的主槽位回退，`llm.api_keys` 以 `llm.KeyPool` 轮转并在 429 时停键；kimi 订阅运行时接通（`~/.kimi/config.toml` token + `https://api.kimi.com/coding`）。
- 模型槽位状态收敛：`queen_llm.*`（8）、`worker_llm.*`（10）、`vision_fallback.*`（7）全部转 `aligned`（逐键读取点与热切换矩阵见 §2 与 `docs/configuration.md` §3.8）；`worker_llm.max_tokens` 增加读取点 `WorkerMaxTokens`（与上游 `get_worker_max_tokens` 同为无调用点）；`channels.<name>.enabled` 由 `python_only` 变为 `go_partial`：字段按上游形状读取、写入时保留、GET /api/channels 形状不变，启用时 serve 启动打印「无渠道宿主、不会运行」的告警。
- 仍未对齐的 4 个残余（结构性理由 + 替代验证见 `contracts/config-support-ledger.json`）：`llm.antigravity_client_id` / `llm.antigravity_client_secret`（只读；唯一上游消费者是未移植的 AntigravityProvider 内的 OAuth 刷新）、`llm.use_antigravity_subscription`（同上：会话构建对该标志显式报错而非静默发无键请求）、`channels.<name>.enabled`（无渠道插件宿主，读到但不生效）。`mcp.*` 4 键为逐项确认过的 Go 扩展（上游 MCP 来自 registry/`.mcp.json`，从不读 configuration.json 的 `mcp` 块）；`created_at` 为安装器写入、两侧运行时都不读（Go 在配置写入时保留，见测试）。
- 环境变量侧，env-verbatim-port 车道已把 `HIVE_TRACKER_WORKER_TOOL_CALLS`、`HIVE_HISTORY_READ_WORKERS`、`HIVE_NO_FILE_LOG`、`HIVE_COMPACTION_DEBUG`、`HIVE_ENABLE_LLM_LOGS`、`HIVE_SENTINEL_LOG`、`HIVE_PLAYBOOK_FAST_GRACE`、`HIVE_AGENT_NAME`（注入）与 6 个 `HIVE_RESOURCE_*` 接进 Go 运行时；剩余 Python 独有仅 GCU memtrace / GCU 截图并发（6 个名字），是显式平台差异，逐项理由与替代证据见 `contracts/config-support-ledger.json`。

## 1. 环境变量核对（采用既有审计清单，独立复算）

审计口径：两侧源码中的带引号 `HIVE_*` 字面量，排除 tests/testdata/_test.go；Python 范围 `core/` + `tools/` + 根启动脚本，Go 范围 `go_hive/go-hive/{internal,cmd}`。本次用同一规则复算，并与「本车道期望」（基线 + 本车道已知变更）逐项核对：

| 项 | 本次复算 | 本车道期望 | 发布审计基线 | 判定 |
|---|---|---|---|---|
| Python | 89 | 89 | 89 | ✅ |
| Go | 118 | 118 | 96 → 118 | ✅ |
| 共有 | 83 | 83 | 66 → 83 | ✅ |
| Python 独有 | 6 | 6 | 23 → 6 | ✅ |
| Go 独有 | 35 | 35 | 30 → 35 | ✅ |

> `env-vars.json` 逐个名字给出两侧的读取点/默认值，并标注 `audit_membership`；只出现在字面量里、没有读取点的名字标为 `literal_only`，避免把 MCP env 字段名或注释当成读取面。

逐名单比对（相对发布审计）：common 不一致（新增 `HIVE_AGENT_NAME`、`HIVE_COMPACTION_DEBUG`、`HIVE_ENABLE_LLM_LOGS`、`HIVE_HISTORY_READ_WORKERS`、`HIVE_MAX_ITER`、`HIVE_NO_FILE_LOG`、`HIVE_PLAYBOOK_FAST_GRACE`、`HIVE_RESOURCE_CRIT_AVAIL_FRAC`、`HIVE_RESOURCE_CRIT_RENDERERS`、`HIVE_RESOURCE_HISTORY`、`HIVE_RESOURCE_INTERVAL_S`、`HIVE_RESOURCE_WARN_AVAIL_FRAC`、`HIVE_RESOURCE_WARN_RENDERERS`、`HIVE_SENTINEL_LOG`、`HIVE_SPLIT_WORKER_EVENTS`、`HIVE_TRACKER_WORKER_TOOL_CALLS`，移除 无）、python_only 不一致（减少 `HIVE_AGENT_NAME`、`HIVE_COMPACTION_DEBUG`、`HIVE_ENABLE_LLM_LOGS`、`HIVE_HISTORY_READ_WORKERS`、`HIVE_MAX_ITER`、`HIVE_NO_FILE_LOG`、`HIVE_PLAYBOOK_FAST_GRACE`、`HIVE_RESOURCE_CRIT_AVAIL_FRAC`、`HIVE_RESOURCE_CRIT_RENDERERS`、`HIVE_RESOURCE_HISTORY`、`HIVE_RESOURCE_INTERVAL_S`、`HIVE_RESOURCE_WARN_AVAIL_FRAC`、`HIVE_RESOURCE_WARN_RENDERERS`、`HIVE_SENTINEL_LOG`、`HIVE_SPLIT_WORKER_EVENTS`、`HIVE_TRACKER_WORKER_TOOL_CALLS`）、go_only 不一致（published 'HIVE_RETENTION_' (trailing underscore) maps to 'HIVE_RETENTION_*'）。

### 1.1 Python 独有：对齐建议清单

- config-env-align 车道已接入：`HIVE_MAX_ITER`、`HIVE_SPLIT_WORKER_EVENTS`、`HIVE_PYTHON`；env-verbatim-port 车道已接入：`HIVE_TRACKER_WORKER_TOOL_CALLS`、`HIVE_HISTORY_READ_WORKERS`、`HIVE_NO_FILE_LOG`、`HIVE_COMPACTION_DEBUG`、`HIVE_ENABLE_LLM_LOGS`、`HIVE_SENTINEL_LOG`、`HIVE_PLAYBOOK_FAST_GRACE`、`HIVE_AGENT_NAME`、`HIVE_RESOURCE_INTERVAL_S`、`HIVE_RESOURCE_HISTORY`、`HIVE_RESOURCE_WARN_AVAIL_FRAC`、`HIVE_RESOURCE_CRIT_AVAIL_FRAC`、`HIVE_RESOURCE_WARN_RENDERERS`、`HIVE_RESOURCE_CRIT_RENDERERS`（Go 读取点与测试见 `contracts/config-support-ledger.json`）。
- 剩余 Python 独有：`HIVE_GCU_MEMTRACE`、`HIVE_GCU_MEMTRACE_FILE`、`HIVE_GCU_MEMTRACE_FRAMES`、`HIVE_GCU_MEMTRACE_INTERVAL_S`、`HIVE_GCU_MEMTRACE_TOP`、`HIVE_GCU_SCREENSHOT_CONCURRENCY` — 均为 GCU 平台差异（Go 侧以 browserbridge 替代 Python GCU 进程），已在支持矩阵逐项登记理由与替代证据；20 个名字的逐项事实表（Python 读取点/默认/解析 → Go 读取点/默认/解析 → 状态与证据）见 `docs/env-verbatim-port.md`。

| 变量 | 优先级 | 作用 / 建议 |
|---|---|---|
| HIVE_GCU_MEMTRACE* | P3 | GCU memory tracing (5 vars); platform difference — see contracts/config-support-ledger.json |
| HIVE_GCU_SCREENSHOT_CONCURRENCY | P3 | GCU screenshot concurrency; platform difference — the Go bridge has its own concurrency control |

### 1.2 Go 独有：保留并写清为扩展

| 变量 | 说明 |
|---|---|
| HIVE_BROWSERBRIDGE_ARGS | documented Go bridge flag pass-through |
| HIVE_BROWSERBRIDGE_BIN | documented Go bridge binary path |
| HIVE_BROWSERBRIDGE_CONTROL | documented Go bridge control port |
| HIVE_BROWSERBRIDGE_HTTP | documented Go bridge HTTP port |
| HIVE_BROWSER_BRIDGE | documented Go bridge on/off switch |
| HIVE_BROWSER_VIEWPORT_SCALE | injected into the bridge from gcu_viewport_scale |
| HIVE_MCP2CLI | native/external/auto mcp2cli selection (Go-only) |
| HIVE_SESSION_COST_LIMIT_USD | per-session cost ceiling (Go-only) |

## 2. configuration.json 逐键契约

共 135 个键。状态定义见生成器数据文件头：`aligned` 两侧读取且类型/默认/语义一致；`go_partial` Go 有该键（REST/CRUD 或部分路径）但运行时读取点缺失或未接线；`python_only` Go 无读取；`go_only` Python 无对应键；`unread` 两侧都不读。

### 2.0 读取 / 写入 / 生效速查（逐键明细在各小节）

| 段 | Go 运行时读取点 | 写入接口 | 生效时机（热切换矩阵） |
|---|---|---|---|
| `llm` | `MainSlotProvider`（会话构建时整体解析） | `PUT /api/config/llm`、`PUT /api/config/llm-sections`(role=llm)、`PUT /api/models/slots/llm` | 写入即重建运行中会话 transport（跳过有自己模型的 queen），**下一回合**生效；进行中回合保持旧 provider（回合级 pin，见 `docs/configuration.md` §3.8） |
| `queen_llm.<id>` | `queenAwareChatProvider` → `QueenLLMSection` | `PUT /api/queen/{id}/llm`、`PUT /api/models/slots/queen:<id>` | 同上（`sessions_switched` 计数）；清空 section 回主模型 |
| `worker_llm` | `workerChatProvider` → `WorkerLLMSection` | `PUT /api/config/llm-sections`(role=worker_llm)、`PUT /api/models/slots/worker_llm` | **下一个 colony/会话**构建时（与上游 `_write_role_section` 一致，运行中会话不换） |
| `vision_fallback` | `visionCaptionerFor`（每次 caption 重新解析） | `PUT /api/config/llm-sections`(role=vision_fallback)、`PUT /api/models/slots/vision_fallback` | **下一张工具图片**即用新值，无需重建会话 |
| `judge` | `server/judge.go`（每次判定新建 client） | `PUT /api/config/llm-sections`(role=judge) | 立即 |
| `channels.<name>.enabled` | `ChannelSections`（serve 启动告警） | 无接口（配置文件/安装器写入） | **不生效**（无渠道宿主）；字段按上游形状读取并在任何配置写入时保留 |
| `mcp.*` | `readMCPFileSettings`（serve 启动装载） | 无接口（配置文件） | serve 启动；`mcp.timeout_seconds` 还受 `HIVE_MCP_CALL_RESULT_TIMEOUT` 覆盖（env > file） |
| 顶层开关 / `retention` / `sentinel` / `soul` / `queen_mail` / `turn_context` / `user_profile` / `rate_limits` 等 | 各自模块的读取点（见各小节表格） | `PUT /api/config/features`、按模块接口或配置文件 | 各自模块在“每次读取/启动/判定”时重新解析，详见各小节 notes 与 `docs/configuration.md` §7 |

### 2.1 `channels`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `channels.<name>.enabled` | bool | false | — | `core/framework/server/channels.py:501` | `go_hive/go-hive/internal/server/channels_config.go:38` (+1) | go_partial |

- `channels.<name>.enabled`：partial: no plugin host, so the flag cannot start a channel — the Go port has no entry-point discovery or channel lifecycle. Field-level parity instead: Go reads the section with upstream's shape (object, per-channel object, truthy enabled), preserves it on every configuration write, keeps GET /api/channels' upstream response shape (empty with no installed channels), and names enabled channels in a serve-time no-effect warning. Recorded in contracts/config-support-ledger.json.

### 2.2 `judge`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `judge.protocol` | string | null | — | `core/framework/server/judge.py:75` | `go_hive/go-hive/internal/server/judge.go:46` | aligned |
| `judge.api_base` | string | null | — | `core/framework/server/judge.py:33` | `go_hive/go-hive/internal/server/judge.go:30` | aligned |
| `judge.model` | string | null | — | `core/framework/server/judge.py:45` | `go_hive/go-hive/internal/server/judge.go:76` | aligned |
| `judge.api_key` | string | null | — | `core/framework/server/judge.py:50` | `go_hive/go-hive/internal/server/judge.go:30` | aligned |
| `judge.threshold` | number | 0.5 | — | `core/framework/server/stall_check.py:90` | `go_hive/go-hive/internal/server/stall_check.go:105` | aligned |

- `judge.protocol`：only typesafe exists; an unknown protocol logs and returns no answer on both sides.
- `judge.api_base`：both api_base and api_key are required for the slot to count.
- `judge.model`：sent as-is in the /systemone body.
- `judge.api_key`：literal key only; no env indirection on either side.
- `judge.threshold`：stall check only; invalid value falls back to 0.5.

### 2.3 `llm`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `llm.provider` | string | null | — | `core/framework/config.py:283` | `go_hive/go-hive/internal/server/llm_config.go:340` (+1) | aligned |
| `llm.model` | string | null | — | `core/framework/config.py:285` | `go_hive/go-hive/internal/server/llm_config.go:344` (+1) | aligned |
| `llm.api_key` | string | null | — | `core/framework/config.py:784` | `go_hive/go-hive/internal/server/llm_config.go:431` (+1) | aligned |
| `llm.api_keys` | list[string] | null | — | `core/framework/config.py:728` (+1) | `go_hive/go-hive/internal/server/llm_config.go:423` (+4) | aligned |
| `llm.api_base` | string | null | — | `core/framework/config.py:1362` | `go_hive/go-hive/internal/server/llm_config.go:405` (+1) | aligned |
| `llm.api_key_env_var` | string | null | name read from the file | `core/framework/config.py:852` | `go_hive/go-hive/internal/server/llm_config.go:458` (+1) | aligned |
| `llm.max_tokens` | int | 8192 | — | `core/framework/config.py:584` | `go_hive/go-hive/internal/server/llm_config.go:399` (+2) | aligned |
| `llm.max_context_tokens` | int | 32000 | — | `core/framework/config.py:574` | `go_hive/go-hive/internal/server/llm_config.go:400` (+1) | aligned |
| `llm.aux_max_tokens` | int | min(8192, max_tokens) | — | `core/framework/config.py:700` | `go_hive/go-hive/internal/server/config_slots.go:461` | aligned |
| `llm.endpoint` | string (endpoint name) | null | — | `core/framework/config.py:341` | `go_hive/go-hive/internal/server/llm_config.go:417` (+1) | aligned |
| `llm.extra_body` | object | null | — | `core/framework/config.py:642` | `go_hive/go-hive/internal/llm/request_shape.go:180` (+1) | aligned |
| `llm.num_ctx` | int | 16384 | — | `core/framework/config.py:1411` | `go_hive/go-hive/internal/llm/request_shape.go:178` (+1) | aligned |
| `llm.use_claude_code_subscription` | bool | false | — | `core/framework/config.py:790` | `go_hive/go-hive/internal/llm/request_shape.go:158` (+1) | aligned |
| `llm.use_codex_subscription` | bool | false | — | `core/framework/config.py:802` | `go_hive/go-hive/internal/llm/request_shape.go:163` (+1) | aligned |
| `llm.use_kimi_code_subscription` | bool | false | — | `core/framework/config.py:814` | `go_hive/go-hive/internal/server/llm_config.go:80` (+2) | aligned |
| `llm.use_antigravity_subscription` | bool | false | — | `core/framework/config.py:826` | `go_hive/go-hive/internal/server/llm_config.go:210` (+1) | go_partial |
| `llm.antigravity_client_id` | string | null | ANTIGRAVITY_CLIENT_ID | `core/framework/config.py:911` | `go_hive/go-hive/internal/server/config_slots.go:536` | go_partial |
| `llm.antigravity_client_secret` | string | null | ANTIGRAVITY_CLIENT_SECRET | `core/framework/config.py:935` | `go_hive/go-hive/internal/server/config_slots.go:543` | go_partial |

- `llm.provider`：both require provider+model before the slot counts as configured; the session builder joins them as provider/model (get_preferred_model shape), and without a usable pair both fall back to the same hardcoded default pair (anthropic/claude-sonnet-5-5).
- `llm.model`：openrouter/<id> prefix is stripped on both sides before the provider/model join. The Go session builder (MainSlotProvider → MainSlotModel) joins the pair the same way; a model request override is used as given, as RuntimeConfig(model=...) does.
- `llm.api_key`：a literal key beats ambient sources on both sides. Go resolves the runtime key with the same order in MainSlotKey (endpoint → pool → literal → subscriptions → credential store → api_key_env_var → provider env); the status and hot-swap routes mirror it. Pinned by TestMainSlotKeyFollowsTheUpstreamOrder.
- `llm.api_keys`：empty strings are filtered; non-list is ignored on both sides. Go installs the pool as llm.KeyPool on the session provider (first key for single-key callers, rotation on 2+) and parks a 429ed key for 60s, like Python's KeyPool. Boundary: a PUT-rebuilt transport takes the route-resolved key until the next session build installs the pool again.
- `llm.api_base`：subscription endpoints win first (codex/kimi/antigravity), then the configured api_base, then openrouter's https://openrouter.ai/api/v1, on both sides. Pinned by TestMainSlotAPIBasePrefersSubscriptionEndpointsThenTheSection.
- `llm.api_key_env_var`：the env var named here is a fallback AFTER the credential store for Python's get_api_key; Go's MainSlotKey (session build) and llm_config (status/hot swap) follow the same order.
- `llm.max_tokens`：missing -> model catalog -> DEFAULT_MAX_TOKENS=8192. Go consumes it for the queen turn budget (chatTurnMaxTokens → agentCtx.MaxTokens) with the same fallback chain, not only in the REST readback; the fallback chain and the built session's MaxTokens are pinned by TestChatTurnMaxTokensFollowsTheMainSlot / TestChatRuntimeBudgetsTheQueenTurnFromTheMainSlot.
- `llm.max_context_tokens`：missing -> catalog -> 32000 (DEFAULT_MAX_CONTEXT_TOKENS). A queen without her own section budgets her loop by this value first (QueenMaxContextTokens falls through the main slot), matching get_queen_max_context_tokens.
- `llm.aux_max_tokens`：both read the JSON int, else min(8192, llm.max_tokens). Wired: serve installs it process-wide with llm.SetAuxMaxTokens, and the recall/judge/sentinel/skill-test call sites read llm.AuxMaxTokens(). Pinned by TestConfiguredAuxMaxTokensReachesTheJudge.
- `llm.endpoint`：named endpoint must exist in model_endpoints; its stored credential (or its never-ambient placeholder) wins over ambient keys on both sides. Go's MainSlotKey checks it first, exactly like _endpoint_key.
- `llm.extra_body`：non-object/empty -> not sent; copied verbatim into every request body. Go's MainSlotProvider installs it as the provider's extra kwargs at session build (upstream get_llm_extra_kwargs does the same), and a section PUT carries it to the rebuilt transport.
- `llm.num_ctx`：only sent for provider=ollama on both sides. Go reads a JSON integer (json.Number included) and defaults to 16384; a non-integer/absent value keeps the default.
- `llm.use_claude_code_subscription`：adds Bearer auth headers; Go resolves the token from ~/.claude/.credentials.json at session build (upstream also checks the macOS Keychain and refreshes expiry; the port reads the CLI-refreshed file and does not refresh). The config gate and header shape match.
- `llm.use_codex_subscription`：adds ChatGPT backend headers (Authorization/User-Agent/ChatGPT-Account-Id) on both sides; Go resolves the token from ~/.codex/auth.json and the account id from tokens.account_id (else the JWT claim) at session build.
- `llm.use_kimi_code_subscription`：runtime wired end-to-end: MainSlotKey resolves the token from ~/.kimi/config.toml and MainSlotAPIBase returns https://api.kimi.com/coding, so a session on this flag authenticates and routes like Python's get_api_key/get_api_base branches (the kimi/ model prefix routes through the Anthropic Messages transport). Pinned by TestMainSlotProviderReadsTheSubscriptionTokens.
- `llm.use_antigravity_subscription`：partial: Python's flag swaps the whole session to AntigravityProvider (Gemini-style cloudcode API, OAuth refresh) in session_manager.build_llm; that provider is not ported. Go consumes the flag as far as it can — MainSlotKey reads the account file's access token for get_api_key's branch and MainSlotProvider refuses the session with an explicit error instead of silently sending an OpenAI request with no key. Structural reason and alternative verification are recorded in contracts/config-support-ledger.json (TestMainSlotProviderRefusesAntigravitySubscription).
- `llm.antigravity_client_id`：reader only: ANTIGRAVITY_CLIENT_ID, then llm.antigravity_client_id (no public-source fetch), tested by TestAntigravityClientCredentials. The only upstream consumers are the OAuth refresh flows inside the unported AntigravityProvider (framework/llm/antigravity.py, framework/loader/agent_loader.py::_refresh_antigravity_token); the port reads the pre-issued access token from antigravity-accounts.json and has no refresh to hand a client id to. Alternative verification: the reader's env-then-file precedence is pinned; no REST/CRUD path is claimed as consumption.
- `llm.antigravity_client_secret`：reader only: ANTIGRAVITY_CLIENT_SECRET, then llm.antigravity_client_secret (no public-source fetch), tested by TestAntigravityClientCredentials. Same structural reason as the client id: the only consumers are the unported AntigravityProvider's OAuth refresh flows, so there is no request path to hand a client secret to.

### 2.4 `loop`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `loop.max_tool_result_chars` | int | 30000 | — | `core/framework/config.py:714` | `go_hive/go-hive/internal/agentloop/loop_config.go:152` (+1) | aligned |

- `loop.max_tool_result_chars`：both read the JSON int and fall back to the call site's value (30_000 global default). Wired: hiveconf.MaxToolResultChars applies to the queen chat loop (chatLoopConfig) and to each colony worker loop (BuildWorkerLoopConfig), so a pinned threshold moves the spillover point.

### 2.5 `mcp`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `mcp.enabled` | bool | true | — | — | `go_hive/go-hive/internal/cli/mcp_config.go:68` | go_only |
| `mcp.namespace` | string | mcp__ | — | — | `go_hive/go-hive/internal/cli/mcp_config.go:69` | go_only |
| `mcp.timeout_seconds` | number | 240.0 | HIVE_MCP_CALL_RESULT_TIMEOUT | — | `go_hive/go-hive/internal/cli/mcp_config.go:70` (+1) | go_only |
| `mcp.servers` | list \| object(name->spec) | [] | — | — | `go_hive/go-hive/internal/cli/mcp_config.go:71` | go_only |

- `mcp.enabled`：go_only: confirmed no upstream counterpart — Python configures MCP servers through the registry (`hive mcp add`) and .mcp.json, and never reads an `mcp` block from configuration.json. Go reads it as the operator pin: enabled gates the whole subsystem (default true).
- `mcp.namespace`：go_only: confirmed no upstream counterpart. Tool-name namespace; blank keeps the default "mcp__".
- `mcp.timeout_seconds`：env > file > 240s default, the general rule: internal/tools.MCPCallResultTimeout resolves HIVE_MCP_CALL_RESULT_TIMEOUT first, then the file value. Was file > env until the config-env-align lane; the reversal is fixed and pinned by TestMCPCallResultTimeoutPrecedence.
- `mcp.servers`：go_only: confirmed no upstream counterpart. Accepts both the list and map shapes; a same-named registry entry is replaced by the file entry and `enabled: false` removes it from the load set.

### 2.6 `memory_reflection`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `memory_reflection.every_turns` | int >= 1 | 6 | — | `core/framework/config.py:1023` | `go_hive/go-hive/internal/server/chat_memory_wiring.go:293` | aligned |
| `memory_reflection.long_every` | int >= 1 | 4 | — | `core/framework/config.py:1025` | `go_hive/go-hive/internal/server/chat_memory_wiring.go:294` | aligned |
| `memory_reflection.model` | "main" \| "worker" | main | — | `core/framework/config.py:1041` | `go_hive/go-hive/internal/server/chat_memory_wiring.go:295` | aligned |

- `memory_reflection.every_turns`：int (bool rejected), >=1; invalid keeps 6.
- `memory_reflection.long_every`：same validation as every_turns.
- `memory_reflection.model`：other strings keep "main".

### 2.7 `model_endpoints`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `model_endpoints.<name>.api_base` | object(str->str) | {} | — | `core/framework/llm/endpoints.py:63` | `go_hive/go-hive/internal/llm/endpoints.go:79` | aligned |

- `model_endpoints.<name>.api_base`：name must match [a-z0-9_-]+; blank api_base skipped; trailing / trimmed. magpie is built in.

### 2.8 `pipeline`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `pipeline.stages` | list[{type, order?, config?}] | [] | — | `core/framework/host/colony_runtime.py:687` | `go_hive/go-hive/internal/colony/trigger.go:119` | aligned |

- `pipeline.stages`：Declarative trigger middleware (upstream framework/pipeline). Stages run in ascending order on every ColonyRuntime.trigger before a worker spawns; a reject short-circuits as PipelineRejectedError and spawns nothing; a missing/empty list is the no-op default (PipelineRunner([])). Go builds the same runner in internal/pipeline from the same type/order/config spec, reads it in NewColonyRuntime, and fails startup on an unknown type (upstream's KeyError). The seven built-in stage semantics and the Go-side applied-output subset are documented in docs/pipeline.md.

### 2.9 `provider_library`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `provider_library.<name>` | object (slot-shaped) | {} | — | `core/framework/server/routes_config.py:1224` | `go_hive/go-hive/internal/server/provider_library.go:65` | aligned |

- `provider_library.<name>`：named saved slots; applying copies the object into a role slot, no indirection. Non-object entries ignored.

### 2.10 `queen_llm`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `queen_llm.<queen_id>.provider` | string | inherit llm | — | `core/framework/config.py:614` (+1) | `go_hive/go-hive/internal/server/routes_queens.go:526` (+2) | aligned |
| `queen_llm.<queen_id>.model` | string | inherit llm | — | `core/framework/config.py:624` | `go_hive/go-hive/internal/server/routes_queens.go:564` (+2) | aligned |
| `queen_llm.<queen_id>.api_key` | string | inherit llm | — | `core/framework/config.py:655` | `go_hive/go-hive/internal/server/routes_queens.go:569` (+2) | aligned |
| `queen_llm.<queen_id>.api_base` | string | inherit llm | — | `core/framework/config.py:671` | `go_hive/go-hive/internal/server/routes_queens.go:575` (+2) | aligned |
| `queen_llm.<queen_id>.api_key_env_var` | string | null | name read from the file | `core/framework/config.py:660` | `go_hive/go-hive/internal/server/routes_queens.go:571` (+2) | aligned |
| `queen_llm.<queen_id>.max_context_tokens` | int | inherit llm | — | `core/framework/config.py:684` | `go_hive/go-hive/internal/server/routes_queens.go:526` (+2) | aligned |
| `queen_llm.<queen_id>.endpoint` | string | null | — | `core/framework/config.py:652` | `go_hive/go-hive/internal/server/routes_queens.go:526` (+2) | aligned |
| `queen_llm.<queen_id>.extra_body` | object | null | — | `core/framework/config.py:642` | `go_hive/go-hive/internal/server/routes_queens.go:526` (+2) | aligned |

- `queen_llm.<queen_id>.provider`：wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.
- `queen_llm.<queen_id>.model`：wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.
- `queen_llm.<queen_id>.api_key`：wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.
- `queen_llm.<queen_id>.api_base`：wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.
- `queen_llm.<queen_id>.api_key_env_var`：wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.
- `queen_llm.<queen_id>.max_context_tokens`：wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.
- `queen_llm.<queen_id>.endpoint`：wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.
- `queen_llm.<queen_id>.extra_body`：wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.

### 2.11 `queen_mail`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `queen_mail.enabled` | bool | false | — | `core/framework/config.py:1127` | `go_hive/go-hive/internal/queenmail/config.go:120` | aligned |
| `queen_mail.queens` | list[string] | null | — | `core/framework/config.py:1128` | `go_hive/go-hive/internal/queenmail/config.go:122` | aligned |
| `queen_mail.daily_limit` | int >= 1 | 100 | — | `core/framework/config.py:1100` | `go_hive/go-hive/internal/queenmail/config.go:134` | aligned |
| `queen_mail.max_depth` | int >= 1 | 8 | — | `core/framework/config.py:1102` | `go_hive/go-hive/internal/queenmail/config.go:135` | aligned |
| `queen_mail.max_parallel_turns` | int >= 1 | 2 | — | `core/framework/config.py:1106` | `go_hive/go-hive/internal/queenmail/config.go:136` | aligned |
| `queen_mail.claude_code.<id>.name` | string | id | — | `core/framework/config.py:1162` | `go_hive/go-hive/internal/queenmail/config.go:157` | aligned |
| `queen_mail.claude_code.<id>.title` | string | Claude Code | — | `core/framework/config.py:1163` | `go_hive/go-hive/internal/queenmail/config.go:158` | aligned |
| `queen_mail.claude_code.<id>.command` | string \| list[string] | ["claude"] | — | `core/framework/config.py:1156` | `go_hive/go-hive/internal/queenmail/config.go:159` | aligned |
| `queen_mail.claude_code.<id>.auth` | "api_key" \| "subscription" | api_key | — | `core/framework/config.py:1165` | `go_hive/go-hive/internal/queenmail/config.go:160` | aligned |
| `queen_mail.claude_code.<id>.api_key` | string | null | — | `core/framework/config.py:1166` | `go_hive/go-hive/internal/queenmail/config.go:161` | aligned |
| `queen_mail.claude_code.<id>.api_base` | string | null | — | `core/framework/config.py:1167` | `go_hive/go-hive/internal/queenmail/config.go:162` | aligned |
| `queen_mail.claude_code.<id>.config_dir` | string | HIVE_HOME/claude_code/<id>/config | — | `core/framework/config.py:1168` | `go_hive/go-hive/internal/queenmail/config.go:163` | aligned |
| `queen_mail.claude_code.<id>.cwd` | string | HIVE_HOME/claude_code/<id>/workspace | — | `core/framework/config.py:1169` | `go_hive/go-hive/internal/queenmail/config.go:164` | aligned |
| `queen_mail.claude_code.<id>.permission_mode` | string | default | — | `core/framework/config.py:1170` | `go_hive/go-hive/internal/queenmail/config.go:165` | aligned |
| `queen_mail.claude_code.<id>.allowed_tools` | list[string] | [] | — | `core/framework/config.py:1171` | `go_hive/go-hive/internal/queenmail/config.go:166` | aligned |
| `queen_mail.claude_code.<id>.disallowed_tools` | list[string] | [] | — | `core/framework/config.py:1172` | `go_hive/go-hive/internal/queenmail/config.go:167` | aligned |
| `queen_mail.claude_code.<id>.add_dirs` | list[string] | [] | — | `core/framework/config.py:1173` | `go_hive/go-hive/internal/queenmail/config.go:168` | aligned |
| `queen_mail.claude_code.<id>.model` | string | null | — | `core/framework/config.py:1174` | `go_hive/go-hive/internal/queenmail/config.go:169` | aligned |
| `queen_mail.claude_code.<id>.timeout_minutes` | int >= 1 | 30 | — | `core/framework/config.py:1082` | `go_hive/go-hive/internal/queenmail/config.go:170` | aligned |
| `queen_mail.claude_code.<id>.system_prompt` | string | null | — | `core/framework/config.py:1176` | `go_hive/go-hive/internal/queenmail/config.go:171` | aligned |
| `queen_mail.claude_code.<id>.env` | object str->str | {} | — | `core/framework/config.py:1177` | `go_hive/go-hive/internal/queenmail/config.go:172` | aligned |

- `queen_mail.enabled`：only JSON true enables it; 1/"true" do not.
- `queen_mail.queens`：absent -> every queen; non-list -> every queen; empty entries dropped.
- `queen_mail.daily_limit`：bool is rejected as an int on both sides.
- `queen_mail.max_depth`：same validation.
- `queen_mail.max_parallel_turns`：same validation.
- `queen_mail.claude_code.<id>.name`：blank/whitespace falls back to the id.
- `queen_mail.claude_code.<id>.title`：same fallback.
- `queen_mail.claude_code.<id>.command`：a non-empty string is one argv element; a list is filtered to non-empty strings.
- `queen_mail.claude_code.<id>.auth`：anything else keeps api_key.
- `queen_mail.claude_code.<id>.api_key`：blank is none; ignored when auth=subscription.
- `queen_mail.claude_code.<id>.api_base`：passed as ANTHROPIC_BASE_URL.
- `queen_mail.claude_code.<id>.config_dir`：None is resolved by the runner, not the config reader.
- `queen_mail.claude_code.<id>.cwd`：same resolution split.
- `queen_mail.claude_code.<id>.permission_mode`：passed verbatim to --permission-mode; no allowlist of values.
- `queen_mail.claude_code.<id>.allowed_tools`：non-string entries and empties dropped.
- `queen_mail.claude_code.<id>.disallowed_tools`：same filtering.
- `queen_mail.claude_code.<id>.add_dirs`：same filtering.
- `queen_mail.claude_code.<id>.model`：None leaves Claude Code's own default.
- `queen_mail.claude_code.<id>.timeout_minutes`：bool/invalid keeps 30.
- `queen_mail.claude_code.<id>.system_prompt`：appended after Hive's note.
- `queen_mail.claude_code.<id>.env`：non-string keys/values dropped.

### 2.12 `queen_tools`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `queen_tools.always_enabled_categories` | list[string] | null | — | `core/framework/agents/queen/queen_tools_defaults.py:398` | `go_hive/go-hive/internal/server/routes_queens_tools.go:517` | aligned |

- `queen_tools.always_enabled_categories`：list of strings; non-list/entries ignored; applies to the default tool catalogue.

### 2.13 `retention`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `retention.enabled` | bool | true | HIVE_RETENTION_ENABLED | `core/framework/config.py:1455` | `go_hive/go-hive/internal/maintenance/config.go:33` | aligned |
| `retention.mode` | "archive" \| "delete" | archive | HIVE_RETENTION_MODE | `core/framework/config.py:1456` | `go_hive/go-hive/internal/maintenance/config.go:34` | aligned |
| `retention.archive_dir` | string | HIVE_HOME/archive | HIVE_RETENTION_ARCHIVE_DIR | `core/framework/config.py:1457` | `go_hive/go-hive/internal/maintenance/config.go:35` | aligned |
| `retention.event_logs_days` | int | 7 | HIVE_RETENTION_EVENT_LOGS_DAYS | `core/framework/config.py:1459` | `go_hive/go-hive/internal/maintenance/config.go:36` | aligned |
| `retention.llm_logs_days` | int | 7 | HIVE_RETENTION_LLM_LOGS_DAYS | `core/framework/config.py:1460` | `go_hive/go-hive/internal/maintenance/config.go:37` | aligned |
| `retention.compaction_log_days` | int | 7 | HIVE_RETENTION_COMPACTION_LOG_DAYS | `core/framework/config.py:1461` | `go_hive/go-hive/internal/maintenance/config.go:38` | aligned |
| `retention.tool_artifacts_days` | int | 14 | HIVE_RETENTION_TOOL_ARTIFACTS_DAYS | `core/framework/config.py:1462` | `go_hive/go-hive/internal/maintenance/config.go:39` | aligned |
| `retention.rotated_logs_days` | int | 30 | HIVE_RETENTION_ROTATED_LOGS_DAYS | `core/framework/config.py:1463` | `go_hive/go-hive/internal/maintenance/config.go:40` | aligned |
| `retention.worker_deep_clean_days` | int | 14 | HIVE_RETENTION_WORKER_DEEP_CLEAN_DAYS | `core/framework/config.py:1465` | `go_hive/go-hive/internal/maintenance/config.go:41` | aligned |
| `retention.queen_hygiene_days` | int | 30 | HIVE_RETENTION_QUEEN_HYGIENE_DAYS | `core/framework/config.py:1467` | `go_hive/go-hive/internal/maintenance/config.go:42` | aligned |
| `retention.events_rewrite_min_bytes` | int | 1000000 | HIVE_RETENTION_EVENTS_REWRITE_MIN_BYTES | `core/framework/config.py:1468` | `go_hive/go-hive/internal/maintenance/config.go:43` | aligned |
| `retention.active_grace_hours` | int | 48 | HIVE_RETENTION_ACTIVE_GRACE_HOURS | `core/framework/config.py:1470` | `go_hive/go-hive/internal/maintenance/config.go:44` | aligned |
| `retention.io_sleep_ms` | int | 10 | HIVE_RETENTION_IO_SLEEP_MS | `core/framework/config.py:1471` | `go_hive/go-hive/internal/maintenance/config.go:45` | aligned |
| `retention.batch_size` | int | 200 | HIVE_RETENTION_BATCH_SIZE | `core/framework/config.py:1472` | `go_hive/go-hive/internal/maintenance/config.go:46` | aligned |
| `retention.junk_min_bytes` | int | 50000000 | HIVE_RETENTION_JUNK_MIN_BYTES | `core/framework/config.py:1473` | `go_hive/go-hive/internal/maintenance/config.go:47` | aligned |

- `retention.enabled`：file: exact bool only; env: (0,false,no,'') false. Same on both sides.
- `retention.mode`：neither side validates the enum; an unknown string is kept (janitor treats non-delete as archive on both sides).
- `retention.archive_dir`：empty resolves to $HIVE_HOME/archive.
- `retention.event_logs_days`：file: JSON integer only (bool/float/string ignored); env: invalid int keeps the file/default value.
- `retention.llm_logs_days`：same rule.
- `retention.compaction_log_days`：same rule.
- `retention.tool_artifacts_days`：same rule.
- `retention.rotated_logs_days`：same rule.
- `retention.worker_deep_clean_days`：same rule.
- `retention.queen_hygiene_days`：same rule.
- `retention.events_rewrite_min_bytes`：int64 in Go; JSON integer only.
- `retention.active_grace_hours`：same rule.
- `retention.io_sleep_ms`：same rule.
- `retention.batch_size`：same rule.
- `retention.junk_min_bytes`：int64 in Go.

### 2.14 `sentinel`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `sentinel.classify_after_seconds` | number >= 1 | 300.0 | — | `core/framework/sentinel/store.py:56` | `go_hive/go-hive/internal/sentinel/store.go:123` | aligned |
| `sentinel.max_nudges_before_escalate` | int >= 1 | 5 | — | `core/framework/sentinel/store.py:64` | `go_hive/go-hive/internal/sentinel/store.go:140` | aligned |
| `sentinel.escalate_when_ui_attached` | bool | false | — | `core/framework/sentinel/store.py:72` | `go_hive/go-hive/internal/sentinel/store.go:158` | aligned |

- `sentinel.classify_after_seconds`：float() coercion; values < 1 clamp to 1.0; invalid -> 300.0.
- `sentinel.max_nudges_before_escalate`：int(); values < 1 clamp to 1; invalid -> 5.
- `sentinel.escalate_when_ui_attached`：Python bool() admits truthy strings; Go uses PyTruthy to match.

### 2.15 `soul`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `soul.queens.<id>.enabled` | bool | true when the entry exists | — | `core/framework/config.py:1251` | `go_hive/go-hive/internal/soul/config.go:164` | aligned |
| `soul.queens.<id>.mode` | "review" \| "auto" | review | — | `core/framework/config.py:1281` | `go_hive/go-hive/internal/soul/config.go:231` | aligned |
| `soul.queens.<id>.hour` | int 0..23 | 4 | — | `core/framework/config.py:1282` | `go_hive/go-hive/internal/soul/config.go:218` | aligned |
| `soul.queens.<id>.model` | "queen" \| "worker" | queen | — | `core/framework/config.py:1283` | `go_hive/go-hive/internal/soul/config.go:234` | aligned |
| `soul.queens.<id>.max_edits` | int >= 1 | 5 | — | `core/framework/config.py:1284` | `go_hive/go-hive/internal/soul/config.go:220` | aligned |
| `soul.queens.<id>.bootstrap_edits` | int >= 1 | 30 | — | `core/framework/config.py:1285` | `go_hive/go-hive/internal/soul/config.go:221` | aligned |
| `soul.queens.<id>.include_titles` | list[string] | [] | — | `core/framework/config.py:1286` | `go_hive/go-hive/internal/soul/config.go:222` | aligned |
| `soul.queens.<id>.exclude_titles` | list[string] | [] | — | `core/framework/config.py:1287` | `go_hive/go-hive/internal/soul/config.go:223` | aligned |
| `soul.queens.<id>.sources.file` | string | null | — | `core/framework/config.py:1288` | `go_hive/go-hive/internal/soul/config.go:241` | aligned |
| `soul.queens.<id>.sources.keys` | list[string] | [] | — | `core/framework/config.py:1289` | `go_hive/go-hive/internal/soul/config.go:256` | aligned |
| `soul.queens.<id>.bootstrap_memories` | list[string] | [] | — | `core/framework/config.py:1290` | `go_hive/go-hive/internal/soul/config.go:225` | aligned |
| `soul.queens.<id>.sections[].key` | string | default section list | — | `core/framework/config.py:1270` | `go_hive/go-hive/internal/soul/config.go:187` | aligned |
| `soul.queens.<id>.sections[].title` | string | null | — | `core/framework/config.py:1271` | `go_hive/go-hive/internal/soul/config.go:188` | aligned |
| `soul.queens.<id>.sections[].limit` | int >= 1 | 400 | — | `core/framework/config.py:1272` | `go_hive/go-hive/internal/soul/config.go:195` | aligned |
| `soul.queens.<id>.sections[].max_age_days` | int >= 1 | null | — | `core/framework/config.py:1273` | `go_hive/go-hive/internal/soul/config.go:198` | aligned |
| `soul.queens.<id>.sections[].evidence` | int >= 1 | 1 | — | `core/framework/config.py:1274` | `go_hive/go-hive/internal/soul/config.go:196` | aligned |
| `soul.queens.<id>.header` | string | built-in header | — | `core/framework/config.py:1292` | `go_hive/go-hive/internal/soul/config.go:245` | aligned |
| `soul.queens.<id>.session_chars` | int >= 1 | 12000 | — | `core/framework/config.py:1293` | `go_hive/go-hive/internal/soul/config.go:228` | aligned |
| `soul.queens.<id>.message_chars` | int >= 1 | 400 | — | `core/framework/config.py:1294` | `go_hive/go-hive/internal/soul/config.go:229` | aligned |

- `soul.queens.<id>.enabled`：only JSON false disables; an absent entry means no soul.
- `soul.queens.<id>.mode`：other values keep review.
- `soul.queens.<id>.hour`：out of range -> 4.
- `soul.queens.<id>.model`：other values keep queen.
- `soul.queens.<id>.max_edits`：bool rejected.
- `soul.queens.<id>.bootstrap_edits`：same validation.
- `soul.queens.<id>.include_titles`：regex strings; non-list/empty entries dropped.
- `soul.queens.<id>.exclude_titles`：same filtering.
- `soul.queens.<id>.sources.file`：relative paths resolve against HIVE_HOME.
- `soul.queens.<id>.sources.keys`：empty strings dropped.
- `soul.queens.<id>.bootstrap_memories`：same filtering.
- `soul.queens.<id>.sections[].key`：a missing/blank key drops the entry.
- `soul.queens.<id>.sections[].title`：blank title drops the entry.
- `soul.queens.<id>.sections[].limit`：invalid -> 400.
- `soul.queens.<id>.sections[].max_age_days`：None/0/invalid means keep forever.
- `soul.queens.<id>.sections[].evidence`：invalid -> 1.
- `soul.queens.<id>.header`：blank keeps the built-in header.
- `soul.queens.<id>.session_chars`：invalid -> 12000.
- `soul.queens.<id>.message_chars`：invalid -> 400.

### 2.16 `top-level`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `adaptive_tool_budget` | bool | true | HIVE_ADAPTIVE_TOOL_BUDGET | `core/framework/config.py:963` (+1) | `go_hive/go-hive/internal/server/config_features.go:47` (+1) | aligned |
| `email_senders` | bool | false | HIVE_EMAIL_SENDERS | `core/framework/config.py:992` (+1) | `go_hive/go-hive/internal/server/config_features.go:55` (+1) | aligned |
| `stall_check` | bool | false | HIVE_STALL_CHECK | `core/framework/config.py:1009` (+1) | `go_hive/go-hive/internal/server/hive_config.go:21` (+1) | aligned |
| `memory_timeline` | bool | false | HIVE_MEMORY_TIMELINE | `core/framework/config.py:1310` (+1) | `go_hive/go-hive/internal/server/config_features.go:63` (+1) | aligned |
| `gcu_enabled` | bool | true | — | `core/framework/config.py:945` | `go_hive/go-hive/internal/cli/browser_supervisor.go:118` | aligned |
| `gcu_viewport_scale` | number (0.1-1.0) | 0.8 | — | `core/framework/config.py:1344` | `go_hive/go-hive/internal/server/hive_config.go:77` | aligned |
| `created_at` | string (ISO time) | null | — | `quickstart.sh:1048` | — | unread |
| `external_skills` | list[string] | [] | — | `core/framework/skills/discovery.py:149` | `go_hive/go-hive/internal/skills/discovery.go:216` | aligned |
| `rate_limits` | object (dotted platform.action.window) | built-in table | HIVE_RATE_<PLATFORM>_<ACTION>_<WINDOW> | `core/framework/rate_limiter.py:30` | `go_hive/go-hive/internal/ratelimit/rate_limiter.go:123` | aligned |

- `adaptive_tool_budget`：env > configuration.json > default(true); empty env string falls through to the file on both sides.
- `email_senders`：env > file > default(false); sync_email_senders_env publishes the resolved flag back to the process env on both sides.
- `stall_check`：empty/whitespace env falls through to the file; falsey = (0,false,no,off).
- `memory_timeline`：same env > file > default(false) chain.
- `gcu_enabled`：file only. Go additionally lets HIVE_BROWSER_BRIDGE force the bridge on/off (documented operator override, not a parity break).
- `gcu_viewport_scale`：out-of-range / non-number -> 0.8; Python's isinstance(True, int) quirk (true -> 1.0) is replicated in Go.
- `created_at`：unread: written by the installers (quickstart.sh/quickstart.ps1), read by neither runtime — a write-once install timestamp, not a runtime knob. Go preserves it on every configuration write; TestConfigWritesPreserveChannelsAndCreatedAt pins that.
- `external_skills`：non-string entries ignored; paths may be nonexistent and resolve later.
- `rate_limits`：file values can raise defaults but never past the hard ceiling; env overrides exist on the Python side (HIVE_RATE_...) and are documented in the rate-limiter module.

### 2.17 `turn_context`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `turn_context.<name>.enabled` | bool | false | — | `core/framework/server/turn_context.py:114` | `go_hive/go-hive/internal/server/turn_context.go:159` | aligned |

- `turn_context.<name>.enabled`：section must be an object; a truthy enabled starts the installed entry point. `{"voice": true}` does not enable anything.

### 2.18 `user_profile`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `user_profile.displayName` | string | "" | — | `core/framework/server/routes_config.py:681` | `go_hive/go-hive/internal/server/profile.go:128` | aligned |
| `user_profile.about` | string | "" | — | `core/framework/server/routes_config.py:682` | `go_hive/go-hive/internal/server/profile.go:148` | aligned |
| `user_profile.theme` | "" \| "light" \| "dark" | "" | — | `core/framework/server/routes_config.py:683` | `go_hive/go-hive/internal/server/profile.go:150` | aligned |
| `user_profile.density` | "" \| "spacious" \| "compact" | "" | — | `core/framework/server/routes_config.py:684` | `go_hive/go-hive/internal/server/profile.go:152` | aligned |
| `user_profile.prompt_library_sort` | object {my,community} | null | — | `core/framework/server/routes_config.py:685` | `go_hive/go-hive/internal/server/profile.go:128` | aligned |

- `user_profile.displayName`：GET returns "" for missing; PUT trims. Go's ProfileResponse 500s on a non-object user_profile exactly like upstream's AttributeError path.
- `user_profile.about`：same trim; max length enforced by both (profile.go mirrors routes_config).
- `user_profile.theme`：PUT ignores other values.
- `user_profile.density`：PUT ignores other values.
- `user_profile.prompt_library_sort`：only known sort keys survive; absent -> null so the UI cache is not clobbered.

### 2.19 `vision_fallback`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `vision_fallback.provider` | string | inherit llm | — | `core/framework/config.py:324` | `go_hive/go-hive/internal/server/routes_models.go:28` (+2) | aligned |
| `vision_fallback.model` | string | null | — | `core/framework/config.py:325` | `go_hive/go-hive/internal/server/routes_models.go:28` (+2) | aligned |
| `vision_fallback.api_key` | string | null | — | `core/framework/config.py:363` | `go_hive/go-hive/internal/server/routes_models.go:28` (+2) | aligned |
| `vision_fallback.api_base` | string | null | — | `core/framework/config.py:376` | `go_hive/go-hive/internal/server/routes_models.go:28` (+2) | aligned |
| `vision_fallback.api_key_env_var` | string | null | name read from the file | `core/framework/config.py:365` | `go_hive/go-hive/internal/server/routes_models.go:28` (+2) | aligned |
| `vision_fallback.max_tokens` | int | aux_max_tokens | — | `core/framework/agent_loop/internals/vision_fallback.py:329` | `go_hive/go-hive/internal/server/routes_models.go:28` (+2) | aligned |
| `vision_fallback.endpoint` | string | null | — | `core/framework/config.py:360` | `go_hive/go-hive/internal/server/routes_models.go:28` (+2) | aligned |

- `vision_fallback.provider`：wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.
- `vision_fallback.model`：wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.
- `vision_fallback.api_key`：wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.
- `vision_fallback.api_base`：wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.
- `vision_fallback.api_key_env_var`：wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.
- `vision_fallback.max_tokens`：wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.
- `vision_fallback.endpoint`：wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.

### 2.20 `worker_llm`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `worker_llm.provider` | string | inherit llm | — | `core/framework/config.py:301` (+1) | `go_hive/go-hive/internal/server/provider_library.go:12` (+3) | aligned |
| `worker_llm.model` | string | inherit llm | — | `core/framework/config.py:302` | `go_hive/go-hive/internal/server/provider_library.go:12` (+2) | aligned |
| `worker_llm.api_key` | string | inherit | — | `core/framework/config.py:398` | `go_hive/go-hive/internal/server/provider_library.go:12` (+2) | aligned |
| `worker_llm.api_base` | string | inherit | — | `core/framework/config.py:471` | `go_hive/go-hive/internal/server/provider_library.go:12` (+2) | aligned |
| `worker_llm.api_key_env_var` | string | null | name read from the file | `core/framework/config.py:448` | `go_hive/go-hive/internal/server/provider_library.go:12` (+2) | aligned |
| `worker_llm.max_tokens` | int | inherit llm | — | `core/framework/config.py:547` | `go_hive/go-hive/internal/server/provider_library.go:12` (+2) | aligned |
| `worker_llm.max_context_tokens` | int | inherit llm | — | `core/framework/config.py:566` | `go_hive/go-hive/internal/server/provider_library.go:12` (+2) | aligned |
| `worker_llm.endpoint` | string | null | — | `core/framework/config.py:393` | `go_hive/go-hive/internal/server/provider_library.go:12` (+2) | aligned |
| `worker_llm.extra_body` | object | inherit llm | — | `core/framework/config.py:512` | `go_hive/go-hive/internal/server/provider_library.go:12` (+2) | aligned |
| `worker_llm.num_ctx` | int | 16384 | — | `core/framework/config.py:511` | `go_hive/go-hive/internal/server/provider_library.go:12` (+2) | aligned |

- `worker_llm.provider`：wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).
- `worker_llm.model`：wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).
- `worker_llm.api_key`：wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).
- `worker_llm.api_base`：wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).
- `worker_llm.api_key_env_var`：wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).
- `worker_llm.max_tokens`：wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).
- `worker_llm.max_context_tokens`：wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).
- `worker_llm.endpoint`：wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).
- `worker_llm.extra_body`：wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).
- `worker_llm.num_ctx`：wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).

### 2.21 `worker_tools`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `worker_tools.always_enabled_categories` | list[string] | null | — | `core/framework/agents/queen/queen_tools_defaults.py:462` | `go_hive/go-hive/internal/server/config_slots.go:516` | aligned |

- `worker_tools.always_enabled_categories`：same list-of-strings rule as queen_tools: a mixed list is not an override. Wired: server.WorkerKeepSetFromConfig expands the categories and hands the colony runtime its WorkerKeepSet; an empty list turns worker tiering off, an absent key keeps the hardcoded default.

## 3. 优先级与边界

总体规则：**env > configuration.json > 内置默认值**。下面把容易写错的边界单独冻结；逐条证据在 `config-keys.json.boundary_rules`。

| 规则 | 契约 | 状态 |
|---|---|---|
| general precedence | env > configuration.json > built-in default | aligned |
| empty env string | feature flags: empty/whitespace = not set -> fall through to configuration.json; retention bool fields: empty = false | aligned |
| non-boolean env values | deny-list (0/false/no/off) -> false; any other non-empty value -> true. Not strconv.ParseBool. | aligned |
| file value of the wrong JSON type | flag/section readers require the exact type; non-bool for a flag keeps the default; non-object section reads as empty | aligned |
| bad JSON / unreadable configuration.json | read as {} -> every reader falls back to its default; a warning is logged on the Python side | aligned |
| integer fields: invalid value | JSON: non-integer (bool/float/string) is ignored and the default/file value stays; env: invalid integer is ignored (Go) or warned+ignored (Python) | aligned |
| bounded / non-negative fields | soul hour 0..23, every count >= 1, sentinel seconds >= 1, gcu viewport 0.1..1.0; out-of-range keeps the default (or clamps where the doc says clamp) | aligned |
| credentials vs env | LLM key resolution: slot literal api_key -> named endpoint key -> subscription token -> credential store -> api_key_env_var -> main key | go_partial |
| MCP call timeout | env > file > 240s default: HIVE_MCP_CALL_RESULT_TIMEOUT wins over mcp.timeout_seconds (the reversal was fixed in the config-env-align lane) | go_only |

**空串 / 非 bool / 越界 / 坏 JSON 的处理要点**

- 功能开关（`adaptive_tool_budget`/`email_senders`/`stall_check`/`memory_timeline`）env 为空串或纯空白时按“未设置”处理，落到 configuration.json；`retention.enabled` 的空串则按 false 处理。两侧实现一致，这是两个不同的约定，不要合并。
- 非 bool 的 env 值走 deny-list：`0/false/no/off` 为 false，其它任何非空值为 true。因此 `HIVE_STALL_CHECK=disabled` 在两侧都为 true（刻意与 strconv.ParseBool 不同）。
- JSON 里类型不对的值：布尔开关要求 JSON bool（`1` 不算 true）；整数要求 JSON integer（`7.0`、`true`、字符串都被忽略）；section 不是对象时按空对象处理。
- 坏 JSON / 文件不可读：两侧都按 `{}` 处理并走默认值（Python 记 warning）；都接受 UTF-8 BOM。
- 越界：soul `hour` 0..23、各 count ≥1、sentinel seconds ≥1、`gcu_viewport_scale` 0.1..1.0；越界时 soul/gcu 回默认，sentinel 夹到下限。
- **MCP 调用超时**：`HIVE_MCP_CALL_RESULT_TIMEOUT` > `mcp.timeout_seconds` > 240s 默认（env > file，符合总体规则）。Go 早期版本这里是 file > env 的反向优先级，已在本车道修复（`TestMCPCallResultTimeoutPrecedence` 钉住）；`mcp.*` 块是 Go 扩展，`config-keys.json` 标 `go_only`。

## 4. 差异清单

### go_partial（4）

- `channels.<name>.enabled` — partial: no plugin host, so the flag cannot start a channel — the Go port has no entry-point discovery or channel lifecycle. Field-level parity instead: Go reads the section with upstream's shape (object, per-channel object, truthy enabled), preserves it on every configuration write, keeps GET /api/channels' upstream response shape (empty with no installed channels), and names enabled channels in a serve-time no-effect warning. Recorded in contracts/config-support-ledger.json.
- `llm.use_antigravity_subscription` — partial: Python's flag swaps the whole session to AntigravityProvider (Gemini-style cloudcode API, OAuth refresh) in session_manager.build_llm; that provider is not ported. Go consumes the flag as far as it can — MainSlotKey reads the account file's access token for get_api_key's branch and MainSlotProvider refuses the session with an explicit error instead of silently sending an OpenAI request with no key. Structural reason and alternative verification are recorded in contracts/config-support-ledger.json (TestMainSlotProviderRefusesAntigravitySubscription).
- `llm.antigravity_client_id` — reader only: ANTIGRAVITY_CLIENT_ID, then llm.antigravity_client_id (no public-source fetch), tested by TestAntigravityClientCredentials. The only upstream consumers are the OAuth refresh flows inside the unported AntigravityProvider (framework/llm/antigravity.py, framework/loader/agent_loader.py::_refresh_antigravity_token); the port reads the pre-issued access token from antigravity-accounts.json and has no refresh to hand a client id to. Alternative verification: the reader's env-then-file precedence is pinned; no REST/CRUD path is claimed as consumption.
- `llm.antigravity_client_secret` — reader only: ANTIGRAVITY_CLIENT_SECRET, then llm.antigravity_client_secret (no public-source fetch), tested by TestAntigravityClientCredentials. Same structural reason as the client id: the only consumers are the unported AntigravityProvider's OAuth refresh flows, so there is no request path to hand a client secret to.

### go_only（4）

- `mcp.enabled` — go_only: confirmed no upstream counterpart — Python configures MCP servers through the registry (`hive mcp add`) and .mcp.json, and never reads an `mcp` block from configuration.json. Go reads it as the operator pin: enabled gates the whole subsystem (default true).
- `mcp.namespace` — go_only: confirmed no upstream counterpart. Tool-name namespace; blank keeps the default "mcp__".
- `mcp.timeout_seconds` — env > file > 240s default, the general rule: internal/tools.MCPCallResultTimeout resolves HIVE_MCP_CALL_RESULT_TIMEOUT first, then the file value. Was file > env until the config-env-align lane; the reversal is fixed and pinned by TestMCPCallResultTimeoutPrecedence.
- `mcp.servers` — go_only: confirmed no upstream counterpart. Accepts both the list and map shapes; a same-named registry entry is replaced by the file entry and `enabled: false` removes it from the load set.

### unread（1）

- `created_at` — unread: written by the installers (quickstart.sh/quickstart.ps1), read by neither runtime — a write-once install timestamp, not a runtime knob. Go preserves it on every configuration write; TestConfigWritesPreserveChannelsAndCreatedAt pins that.

非 `aligned` 键合计 **9** 个，已全部列出（上表）。

## 5. 复现与门禁

```bash
# 重新生成两份 manifest 与本报告（确定性输出）
python3 go_hive/scripts/freeze_env_config_contract.py
# 校验 manifest 与当前代码一致；报告型门禁（默认退出 0）
python3 go_hive/scripts/check_env_config_contract.py
python3 go_hive/scripts/check_env_config_contract.py --strict   # 有漂移/缺失/多余时退出 1
```

Go 侧另有一个报告型测试 `internal/envcontract`（读取 `../../../contracts/*.json`，只 t.Logf 计数，不阻断）。它带 `// Parity-exempt: ...` 注解：上游 Python 没有对应测试，这是 Go 特有的治理工件。

## 6. 覆盖与限制

- 本报告冻结的是**配置契约**，不是行为实现；`aligned` 表示两侧读取同一键且默认/类型/语义一致，不表示端到端行为已验证。
- `go_partial` 的判定基于生产代码里是否存在读取点；REST/CRUD 端点存在不等于运行时消费。
- 未覆盖：per-colony `notifications.json`、agent/worker 子配置文件、`.mcp.json`、前端 localStorage；这些不是全局 `configuration.json`。
- 环境变量采用既有审计（622cc236）的清单为基线；config-env-align 车道（`HIVE_MAX_ITER`、`HIVE_SPLIT_WORKER_EVENTS`、`HIVE_PYTHON`）与 env-verbatim-port 车道（14 个名字，含 `HIVE_AGENT_NAME` 注入）新增的 Go 读取点已计入，network-scanners 车道移除的 Go 独有 `HIVE_NETWORK_SCANNERS` 也已反映。`env-vars.json` 里的读取点由扫描器补充，默认值/解析差异对运行时相关变量做了人工核对。
