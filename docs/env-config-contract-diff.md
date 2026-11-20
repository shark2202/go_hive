# 环境变量与 configuration.json 契约差异（Python 基准 → go_hive）

- reference: `python@bd5041862d5df219a029b190141dda3db8c7eeab`（Hive 1.0.14）
- target: `go_hive@bd5041862d5df219a029b190141dda3db8c7eeab`（1.0.26-go.1）
- 生成器：`go_hive/scripts/freeze_env_config_contract.py`（确定性；`--check` 可重放）
- 机器可读：`go_hive/contracts/env-vars.json`、`go_hive/contracts/config-keys.json`

## 0. 结论摘要

- 环境变量清单沿用已发布的审计（`go_hive/docs/env-surface-audit.json` @ 622cc236），本次独立复算：Python **89** / Go **96** / 共有 **67**；Python 独有 **22**，Go 独有 **29**。数字与发布审计一致。
- configuration.json：**134** 个键 / **20** 个 section；其中 `aligned` 96，`go_partial` 27，`python_only` 6，`go_only` 4，`unread` 1。
- 最重的配置缺口都集中在“Go 有 REST/CRUD、运行时没接”：`worker_llm.*`、`vision_fallback.*`、`queen_llm.<id>.*` 三类槽位在 Go 侧只被读写端点使用，运行时不消费；`llm.aux_max_tokens`、`loop.max_tool_result_chars`、`worker_tools.always_enabled_categories` 则完全没有 reader。
- 环境变量侧，Python 独有的生产开关主要是 `HIVE_MAX_ITER`、`HIVE_SPLIT_WORKER_EVENTS`、`HIVE_TRACKER_WORKER_TOOL_CALLS`、`HIVE_HISTORY_READ_WORKERS`、`HIVE_NO_FILE_LOG`、`HIVE_COMPACTION_DEBUG`、`HIVE_PLAYBOOK_FAST_GRACE`；其余 Python 独有项是资源采样 / GCU memtrace / 日志等运维与调试面。

## 1. 环境变量核对（采用既有审计，不复扫）

审计口径：两侧源码中的带引号 `HIVE_*` 字面量，排除 tests/testdata/_test.go；Python 范围 `core/` + `tools/` + 根启动脚本，Go 范围 `go_hive/go-hive/{internal,cmd}`。本次用同一规则独立复算，结果逐项相等：

| 项 | 复算 | 发布审计 | 一致 |
|---|---|---|---|
| Python | 89 | 89 | ✅ |
| Go | 96 | 96 | ✅ |
| 共有 | 67 | 67 | ✅ |
| Python 独有 | 22 | 22 | ✅ |
| Go 独有 | 29 | 29 | ✅ |

> `env-vars.json` 逐个名字给出两侧的读取点/默认值，并标注 `audit_membership`；只出现在字面量里、没有读取点的名字标为 `literal_only`，避免把 MCP env 字段名或注释当成读取面。

逐名单比对（common / python_only / go_only）与发布审计：common 一致、python_only 一致、go_only 一致（published 'HIVE_RETENTION_' (trailing underscore) maps to 'HIVE_RETENTION_*'）。

### 1.1 Python 独有：对齐建议清单

| 变量 | 优先级 | 作用 / 建议 |
|---|---|---|
| HIVE_MAX_ITER | P1 | caps queen reply-loop iterations for single-reply deployments |
| HIVE_SPLIT_WORKER_EVENTS | P1 | splits worker event streams; affects transcript/event consumers |
| HIVE_TRACKER_WORKER_TOOL_CALLS | P2 | tracker reminder cadence for worker tool calls |
| HIVE_HISTORY_READ_WORKERS | P2 | parallelism for session history reads (default 8) |
| HIVE_NO_FILE_LOG | P2 | turns off file logging; operators rely on it in containers |
| HIVE_COMPACTION_DEBUG | P2 | compaction debug log switch |
| HIVE_PLAYBOOK_FAST_GRACE | P2 | playbook fast-path grace seconds (default 1.5) |
| HIVE_SENTINEL_LOG | P3 | sentinel log switch |
| HIVE_ENABLE_LLM_LOGS | P3 | LLM debug log switch |
| HIVE_RESOURCE_* | P3 | host resource sampling knobs (6 vars) |
| HIVE_GCU_MEMTRACE* | P3 | GCU memory tracing (5 vars) |
| HIVE_GCU_SCREENSHOT_CONCURRENCY | P3 | GCU screenshot concurrency |

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

共 134 个键。状态定义见生成器数据文件头：`aligned` 两侧读取且类型/默认/语义一致；`go_partial` Go 只有 REST/CRUD 或部分路径；`python_only` Go 无读取；`go_only` Python 无对应键；`unread` 两侧都不读。

### 2.1 `channels`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `channels.<name>.enabled` | bool | false | — | `core/framework/server/channels.py:501` | — | python_only |

- `channels.<name>.enabled`：P2: optional-package channels (OneBot, etc.) are Python-only; Go has no channels.<name> runtime reader.

### 2.2 `judge`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `judge.protocol` | string | null | — | `core/framework/server/judge.py:75` | `go_hive/go-hive/internal/server/judge.go:45` | aligned |
| `judge.api_base` | string | null | — | `core/framework/server/judge.py:33` | `go_hive/go-hive/internal/server/judge.go:29` | aligned |
| `judge.model` | string | null | — | `core/framework/server/judge.py:45` | `go_hive/go-hive/internal/server/judge.go:71` | aligned |
| `judge.api_key` | string | null | — | `core/framework/server/judge.py:50` | `go_hive/go-hive/internal/server/judge.go:29` | aligned |
| `judge.threshold` | number | 0.5 | — | `core/framework/server/stall_check.py:90` | `go_hive/go-hive/internal/server/stall_check.go:105` | aligned |

- `judge.protocol`：only typesafe exists; an unknown protocol logs and returns no answer on both sides.
- `judge.api_base`：both api_base and api_key are required for the slot to count.
- `judge.model`：sent as-is in the /systemone body.
- `judge.api_key`：literal key only; no env indirection on either side.
- `judge.threshold`：stall check only; invalid value falls back to 0.5.

### 2.3 `llm`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `llm.provider` | string | null | — | `core/framework/config.py:283` | `go_hive/go-hive/internal/server/llm_config.go:340` | aligned |
| `llm.model` | string | null | — | `core/framework/config.py:285` | `go_hive/go-hive/internal/server/llm_config.go:344` | aligned |
| `llm.api_key` | string | null | — | `core/framework/config.py:784` | `go_hive/go-hive/internal/server/llm_config.go:431` | aligned |
| `llm.api_keys` | list[string] | null | — | `core/framework/config.py:728` (+1) | `go_hive/go-hive/internal/server/llm_config.go:423` (+1) | aligned |
| `llm.api_base` | string | null | — | `core/framework/config.py:1362` | `go_hive/go-hive/internal/server/llm_config.go:405` | aligned |
| `llm.api_key_env_var` | string | null | name read from the file | `core/framework/config.py:852` | `go_hive/go-hive/internal/server/llm_config.go:458` | aligned |
| `llm.max_tokens` | int | 8192 | — | `core/framework/config.py:584` | `go_hive/go-hive/internal/server/llm_config.go:399` | aligned |
| `llm.max_context_tokens` | int | 32000 | — | `core/framework/config.py:574` | `go_hive/go-hive/internal/server/llm_config.go:400` | aligned |
| `llm.aux_max_tokens` | int | min(8192, max_tokens) | — | `core/framework/config.py:700` | — | python_only |
| `llm.endpoint` | string (endpoint name) | null | — | `core/framework/config.py:341` | `go_hive/go-hive/internal/server/llm_config.go:417` | aligned |
| `llm.extra_body` | object | null | — | `core/framework/config.py:642` | `go_hive/go-hive/internal/llm/request_shape.go:180` | aligned |
| `llm.num_ctx` | int | 16384 | — | `core/framework/config.py:1411` | `go_hive/go-hive/internal/llm/request_shape.go:178` | aligned |
| `llm.use_claude_code_subscription` | bool | false | — | `core/framework/config.py:790` | `go_hive/go-hive/internal/llm/request_shape.go:158` | aligned |
| `llm.use_codex_subscription` | bool | false | — | `core/framework/config.py:802` | `go_hive/go-hive/internal/llm/request_shape.go:163` | aligned |
| `llm.use_kimi_code_subscription` | bool | false | — | `core/framework/config.py:814` | `go_hive/go-hive/internal/server/llm_config.go:80` | go_partial |
| `llm.use_antigravity_subscription` | bool | false | — | `core/framework/config.py:826` | `go_hive/go-hive/internal/server/llm_config.go:210` | go_partial |
| `llm.antigravity_client_id` | string | null | ANTIGRAVITY_CLIENT_ID | `core/framework/config.py:911` | — | python_only |
| `llm.antigravity_client_secret` | string | null | ANTIGRAVITY_CLIENT_SECRET | `core/framework/config.py:935` | — | python_only |

- `llm.provider`：both require provider+model before the slot counts as configured; otherwise the main model falls back to the hardcoded default pair.
- `llm.model`：openrouter/<id> prefix is stripped on both sides before the provider/model join.
- `llm.api_key`：a literal key beats ambient sources on both sides; Go's hot-swap path mirrors Python's credential-store-before-env order.
- `llm.api_keys`：empty strings are filtered; non-list is ignored on both sides.
- `llm.api_base`：openrouter with no api_base resolves to https://openrouter.ai/api/v1 on both sides.
- `llm.api_key_env_var`：the env var named here is a fallback AFTER the credential store for Python's get_api_key; Go's llm_config hot-swap follows the same order.
- `llm.max_tokens`：missing -> model catalog -> DEFAULT_MAX_TOKENS=8192. Go's chat runtime seeds 8192 and the model catalog the same way.
- `llm.max_context_tokens`：missing -> catalog -> 32000 (DEFAULT_MAX_CONTEXT_TOKENS).
- `llm.aux_max_tokens`：P1: Go never reads llm.aux_max_tokens. internal/llm/complete.go has DefaultAuxMaxTokens=8192 but no configuration.json reader, so a user-pinned aux budget is ignored.
- `llm.endpoint`：named endpoint must exist in model_endpoints; its stored credential wins over ambient keys on both sides.
- `llm.extra_body`：non-object/empty -> not sent; copied verbatim into every request body.
- `llm.num_ctx`：only sent for provider=ollama on both sides.
- `llm.use_claude_code_subscription`：adds Bearer auth headers; token source differs by platform but the config gate matches.
- `llm.use_codex_subscription`：adds ChatGPT backend headers (Authorization/User-Agent/ChatGPT-Account-Id) on both sides.
- `llm.use_kimi_code_subscription`：P1: Go lists/detects the Kimi subscription in the REST surface; the runtime token path is not wired like Python's get_api_key branch.
- `llm.use_antigravity_subscription`：P1: Go has an antigravity token resolver in llm_config; no equivalent of Python's full provider branch (api_base=None, token refresh).
- `llm.antigravity_client_id`：Go has no configuration.json reader for the Antigravity OAuth client id (Python falls back to the public upstream constants).
- `llm.antigravity_client_secret`：same as antigravity_client_id.

### 2.4 `loop`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `loop.max_tool_result_chars` | int | 30000 | — | `core/framework/config.py:714` | `go_hive/go-hive/internal/agentloop/loop.go:109` | python_only |

- `loop.max_tool_result_chars`：P1: Go has the 30_000 default but no configuration.json reader for the global `loop` section; a file override is ignored.

### 2.5 `mcp`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `mcp.enabled` | bool | true | — | — | `go_hive/go-hive/internal/cli/mcp_config.go:67` | go_only |
| `mcp.namespace` | string | mcp__ | — | — | `go_hive/go-hive/internal/cli/mcp_config.go:68` | go_only |
| `mcp.timeout_seconds` | number | 240.0 | HIVE_MCP_CALL_RESULT_TIMEOUT | — | `go_hive/go-hive/internal/cli/mcp_config.go:69` (+1) | go_only |
| `mcp.servers` | list \| object(name->spec) | [] | — | — | `go_hive/go-hive/internal/cli/mcp_config.go:70` | go_only |

- `mcp.enabled`：Go reads an `mcp` block from configuration.json; Python's MCP servers come from .mcp.json / the registry. A Go extension, not an upstream key.
- `mcp.namespace`：tool-name namespace; blank keeps the default.
- `mcp.timeout_seconds`：priority REVERSED vs the general rule: file wins over env here (>0 only). This is the only configuration.json key in either tree with file > env.
- `mcp.servers`：accepts both the list and map shapes; a same-named registry entry is replaced by the file entry.

### 2.6 `memory_reflection`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `memory_reflection.every_turns` | int >= 1 | 6 | — | `core/framework/config.py:1023` | `go_hive/go-hive/internal/server/chat_runtime.go:1479` | aligned |
| `memory_reflection.long_every` | int >= 1 | 4 | — | `core/framework/config.py:1025` | `go_hive/go-hive/internal/server/chat_runtime.go:1480` | aligned |
| `memory_reflection.model` | "main" \| "worker" | main | — | `core/framework/config.py:1041` | `go_hive/go-hive/internal/server/chat_runtime.go:1481` | aligned |

- `memory_reflection.every_turns`：int (bool rejected), >=1; invalid keeps 6.
- `memory_reflection.long_every`：same validation as every_turns.
- `memory_reflection.model`：other strings keep "main".

### 2.7 `model_endpoints`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `model_endpoints.<name>.api_base` | object(str->str) | {} | — | `core/framework/llm/endpoints.py:63` | `go_hive/go-hive/internal/llm/endpoints.go:79` | aligned |

- `model_endpoints.<name>.api_base`：name must match [a-z0-9_-]+; blank api_base skipped; trailing / trimmed. magpie is built in.

### 2.8 `provider_library`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `provider_library.<name>` | object (slot-shaped) | {} | — | `core/framework/server/routes_config.py:1224` | `go_hive/go-hive/internal/server/provider_library.go:65` | aligned |

- `provider_library.<name>`：named saved slots; applying copies the object into a role slot, no indirection. Non-object entries ignored.

### 2.9 `queen_llm`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `queen_llm.<queen_id>.provider` | string | inherit llm | — | `core/framework/config.py:614` (+1) | `go_hive/go-hive/internal/server/routes_queens.go:526` | go_partial |
| `queen_llm.<queen_id>.model` | string | inherit llm | — | `core/framework/config.py:624` | `go_hive/go-hive/internal/server/routes_queens.go:564` | go_partial |
| `queen_llm.<queen_id>.api_key` | string | inherit llm | — | `core/framework/config.py:655` | `go_hive/go-hive/internal/server/routes_queens.go:569` | go_partial |
| `queen_llm.<queen_id>.api_base` | string | inherit llm | — | `core/framework/config.py:671` | `go_hive/go-hive/internal/server/routes_queens.go:575` | go_partial |
| `queen_llm.<queen_id>.api_key_env_var` | string | null | name read from the file | `core/framework/config.py:660` | `go_hive/go-hive/internal/server/routes_queens.go:571` | go_partial |
| `queen_llm.<queen_id>.max_context_tokens` | int | inherit llm | — | `core/framework/config.py:684` | `go_hive/go-hive/internal/server/routes_queens.go:526` | go_partial |
| `queen_llm.<queen_id>.endpoint` | string | null | — | `core/framework/config.py:652` | `go_hive/go-hive/internal/server/routes_queens.go:526` | go_partial |
| `queen_llm.<queen_id>.extra_body` | object | null | — | `core/framework/config.py:642` | `go_hive/go-hive/internal/server/routes_queens.go:526` | go_partial |

- `queen_llm.<queen_id>.provider`：P1: Go reads queen_llm only for the REST endpoints; ApplyQueenModel has no production assignment, so a queen's own model is not honored at session start.
- `queen_llm.<queen_id>.model`：same runtime wiring gap.
- `queen_llm.<queen_id>.api_key`：same runtime wiring gap.
- `queen_llm.<queen_id>.api_base`：same runtime wiring gap.
- `queen_llm.<queen_id>.api_key_env_var`：same runtime wiring gap.
- `queen_llm.<queen_id>.max_context_tokens`：same runtime wiring gap.
- `queen_llm.<queen_id>.endpoint`：same runtime wiring gap.
- `queen_llm.<queen_id>.extra_body`：same runtime wiring gap.

### 2.10 `queen_mail`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `queen_mail.enabled` | bool | false | — | `core/framework/config.py:1127` | `go_hive/go-hive/internal/qmail/config.go:120` | aligned |
| `queen_mail.queens` | list[string] | null | — | `core/framework/config.py:1128` | `go_hive/go-hive/internal/qmail/config.go:122` | aligned |
| `queen_mail.daily_limit` | int >= 1 | 100 | — | `core/framework/config.py:1100` | `go_hive/go-hive/internal/qmail/config.go:134` | aligned |
| `queen_mail.max_depth` | int >= 1 | 8 | — | `core/framework/config.py:1102` | `go_hive/go-hive/internal/qmail/config.go:135` | aligned |
| `queen_mail.max_parallel_turns` | int >= 1 | 2 | — | `core/framework/config.py:1106` | `go_hive/go-hive/internal/qmail/config.go:136` | aligned |
| `queen_mail.claude_code.<id>.name` | string | id | — | `core/framework/config.py:1162` | `go_hive/go-hive/internal/qmail/config.go:157` | aligned |
| `queen_mail.claude_code.<id>.title` | string | Claude Code | — | `core/framework/config.py:1163` | `go_hive/go-hive/internal/qmail/config.go:158` | aligned |
| `queen_mail.claude_code.<id>.command` | string \| list[string] | ["claude"] | — | `core/framework/config.py:1156` | `go_hive/go-hive/internal/qmail/config.go:159` | aligned |
| `queen_mail.claude_code.<id>.auth` | "api_key" \| "subscription" | api_key | — | `core/framework/config.py:1165` | `go_hive/go-hive/internal/qmail/config.go:160` | aligned |
| `queen_mail.claude_code.<id>.api_key` | string | null | — | `core/framework/config.py:1166` | `go_hive/go-hive/internal/qmail/config.go:161` | aligned |
| `queen_mail.claude_code.<id>.api_base` | string | null | — | `core/framework/config.py:1167` | `go_hive/go-hive/internal/qmail/config.go:162` | aligned |
| `queen_mail.claude_code.<id>.config_dir` | string | HIVE_HOME/claude_code/<id>/config | — | `core/framework/config.py:1168` | `go_hive/go-hive/internal/qmail/config.go:163` | aligned |
| `queen_mail.claude_code.<id>.cwd` | string | HIVE_HOME/claude_code/<id>/workspace | — | `core/framework/config.py:1169` | `go_hive/go-hive/internal/qmail/config.go:164` | aligned |
| `queen_mail.claude_code.<id>.permission_mode` | string | default | — | `core/framework/config.py:1170` | `go_hive/go-hive/internal/qmail/config.go:165` | aligned |
| `queen_mail.claude_code.<id>.allowed_tools` | list[string] | [] | — | `core/framework/config.py:1171` | `go_hive/go-hive/internal/qmail/config.go:166` | aligned |
| `queen_mail.claude_code.<id>.disallowed_tools` | list[string] | [] | — | `core/framework/config.py:1172` | `go_hive/go-hive/internal/qmail/config.go:167` | aligned |
| `queen_mail.claude_code.<id>.add_dirs` | list[string] | [] | — | `core/framework/config.py:1173` | `go_hive/go-hive/internal/qmail/config.go:168` | aligned |
| `queen_mail.claude_code.<id>.model` | string | null | — | `core/framework/config.py:1174` | `go_hive/go-hive/internal/qmail/config.go:169` | aligned |
| `queen_mail.claude_code.<id>.timeout_minutes` | int >= 1 | 30 | — | `core/framework/config.py:1082` | `go_hive/go-hive/internal/qmail/config.go:170` | aligned |
| `queen_mail.claude_code.<id>.system_prompt` | string | null | — | `core/framework/config.py:1176` | `go_hive/go-hive/internal/qmail/config.go:171` | aligned |
| `queen_mail.claude_code.<id>.env` | object str->str | {} | — | `core/framework/config.py:1177` | `go_hive/go-hive/internal/qmail/config.go:172` | aligned |

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

### 2.11 `queen_tools`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `queen_tools.always_enabled_categories` | list[string] | null | — | `core/framework/agents/queen/queen_tools_defaults.py:398` | `go_hive/go-hive/internal/server/routes_queens_tools.go:494` | aligned |

- `queen_tools.always_enabled_categories`：list of strings; non-list/entries ignored; applies to the default tool catalogue.

### 2.12 `retention`

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

### 2.13 `sentinel`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `sentinel.classify_after_seconds` | number >= 1 | 300.0 | — | `core/framework/sentinel/store.py:56` | `go_hive/go-hive/internal/sentinel/store.go:124` | aligned |
| `sentinel.max_nudges_before_escalate` | int >= 1 | 5 | — | `core/framework/sentinel/store.py:64` | `go_hive/go-hive/internal/sentinel/store.go:141` | aligned |
| `sentinel.escalate_when_ui_attached` | bool | false | — | `core/framework/sentinel/store.py:72` | `go_hive/go-hive/internal/sentinel/store.go:159` | aligned |

- `sentinel.classify_after_seconds`：float() coercion; values < 1 clamp to 1.0; invalid -> 300.0.
- `sentinel.max_nudges_before_escalate`：int(); values < 1 clamp to 1; invalid -> 5.
- `sentinel.escalate_when_ui_attached`：Python bool() admits truthy strings; Go uses PyTruthy to match.

### 2.14 `soul`

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

### 2.15 `top-level`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `adaptive_tool_budget` | bool | true | HIVE_ADAPTIVE_TOOL_BUDGET | `core/framework/config.py:963` (+1) | `go_hive/go-hive/internal/server/config_features.go:47` (+1) | aligned |
| `email_senders` | bool | false | HIVE_EMAIL_SENDERS | `core/framework/config.py:992` (+1) | `go_hive/go-hive/internal/server/config_features.go:55` (+1) | aligned |
| `stall_check` | bool | false | HIVE_STALL_CHECK | `core/framework/config.py:1009` (+1) | `go_hive/go-hive/internal/server/hive_config.go:37` (+1) | aligned |
| `memory_timeline` | bool | false | HIVE_MEMORY_TIMELINE | `core/framework/config.py:1310` (+1) | `go_hive/go-hive/internal/server/config_features.go:63` (+1) | aligned |
| `gcu_enabled` | bool | true | — | `core/framework/config.py:945` | `go_hive/go-hive/internal/cli/browser_supervisor.go:118` | aligned |
| `gcu_viewport_scale` | number (0.1-1.0) | 0.8 | — | `core/framework/config.py:1344` | `go_hive/go-hive/internal/server/hive_config.go:93` | aligned |
| `created_at` | string (ISO time) | null | — | `quickstart.sh:1048` | — | unread |
| `external_skills` | list[string] | [] | — | `core/framework/skills/discovery.py:149` | `go_hive/go-hive/internal/skills/discovery.go:212` | aligned |
| `rate_limits` | object (dotted platform.action.window) | built-in table | HIVE_RATE_<PLATFORM>_<ACTION>_<WINDOW> | `core/framework/rate_limiter.py:30` | `go_hive/go-hive/internal/ratelimit/rate_limiter.go:123` | aligned |

- `adaptive_tool_budget`：env > configuration.json > default(true); empty env string falls through to the file on both sides.
- `email_senders`：env > file > default(false); sync_email_senders_env publishes the resolved flag back to the process env on both sides.
- `stall_check`：empty/whitespace env falls through to the file; falsey = (0,false,no,off).
- `memory_timeline`：same env > file > default(false) chain.
- `gcu_enabled`：file only. Go additionally lets HIVE_BROWSER_BRIDGE force the bridge on/off (documented operator override, not a parity break).
- `gcu_viewport_scale`：out-of-range / non-number -> 0.8; Python's isinstance(True, int) quirk (true -> 1.0) is replicated in Go.
- `created_at`：written by the installers, read by neither runtime. Documented example only.
- `external_skills`：non-string entries ignored; paths may be nonexistent and resolve later.
- `rate_limits`：file values can raise defaults but never past the hard ceiling; env overrides exist on the Python side (HIVE_RATE_...) and are documented in the rate-limiter module.

### 2.16 `turn_context`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `turn_context.<name>.enabled` | bool | false | — | `core/framework/server/turn_context.py:114` | `go_hive/go-hive/internal/server/turn_context.go:159` | aligned |

- `turn_context.<name>.enabled`：section must be an object; a truthy enabled starts the installed entry point. `{"voice": true}` does not enable anything.

### 2.17 `user_profile`

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

### 2.18 `vision_fallback`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `vision_fallback.provider` | string | inherit llm | — | `core/framework/config.py:324` | `go_hive/go-hive/internal/server/routes_models.go:28` | go_partial |
| `vision_fallback.model` | string | null | — | `core/framework/config.py:325` | `go_hive/go-hive/internal/server/routes_models.go:28` | go_partial |
| `vision_fallback.api_key` | string | null | — | `core/framework/config.py:363` | `go_hive/go-hive/internal/server/routes_models.go:28` | go_partial |
| `vision_fallback.api_base` | string | null | — | `core/framework/config.py:376` | `go_hive/go-hive/internal/server/routes_models.go:28` | go_partial |
| `vision_fallback.api_key_env_var` | string | null | name read from the file | `core/framework/config.py:365` | `go_hive/go-hive/internal/server/routes_models.go:28` | go_partial |
| `vision_fallback.max_tokens` | int | aux_max_tokens | — | `core/framework/agent_loop/internals/vision_fallback.py:329` | `go_hive/go-hive/internal/server/routes_models.go:28` | go_partial |
| `vision_fallback.endpoint` | string | null | — | `core/framework/config.py:360` | `go_hive/go-hive/internal/server/routes_models.go:28` | go_partial |

- `vision_fallback.provider`：P1: slot is CRUD-only in Go; find_vision_fallback's caption chain is not ported (runtime never reads it).
- `vision_fallback.model`：same runtime gap.
- `vision_fallback.api_key`：same runtime gap.
- `vision_fallback.api_base`：same runtime gap.
- `vision_fallback.api_key_env_var`：same runtime gap.
- `vision_fallback.max_tokens`：same runtime gap; 0/absent falls back to aux_max_tokens.
- `vision_fallback.endpoint`：same runtime gap.

### 2.19 `worker_llm`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `worker_llm.provider` | string | inherit llm | — | `core/framework/config.py:301` (+1) | `go_hive/go-hive/internal/server/provider_library.go:12` (+1) | go_partial |
| `worker_llm.model` | string | inherit llm | — | `core/framework/config.py:302` | `go_hive/go-hive/internal/server/provider_library.go:12` | go_partial |
| `worker_llm.api_key` | string | inherit | — | `core/framework/config.py:398` | `go_hive/go-hive/internal/server/provider_library.go:12` | go_partial |
| `worker_llm.api_base` | string | inherit | — | `core/framework/config.py:471` | `go_hive/go-hive/internal/server/provider_library.go:12` | go_partial |
| `worker_llm.api_key_env_var` | string | null | name read from the file | `core/framework/config.py:448` | `go_hive/go-hive/internal/server/provider_library.go:12` | go_partial |
| `worker_llm.max_tokens` | int | inherit llm | — | `core/framework/config.py:547` | `go_hive/go-hive/internal/server/provider_library.go:12` | go_partial |
| `worker_llm.max_context_tokens` | int | inherit llm | — | `core/framework/config.py:566` | `go_hive/go-hive/internal/server/provider_library.go:12` | go_partial |
| `worker_llm.endpoint` | string | null | — | `core/framework/config.py:393` | `go_hive/go-hive/internal/server/provider_library.go:12` | go_partial |
| `worker_llm.extra_body` | object | inherit llm | — | `core/framework/config.py:512` | `go_hive/go-hive/internal/server/provider_library.go:12` | go_partial |
| `worker_llm.num_ctx` | int | 16384 | — | `core/framework/config.py:511` | `go_hive/go-hive/internal/server/provider_library.go:12` | go_partial |

- `worker_llm.provider`：P1: Go exposes worker_llm through the REST slot API but never resolves it for the runtime (colony/runtime.go comment: the port does not split queen/worker models by default). Python's session_manager.build_worker_llm does.
- `worker_llm.model`：same runtime gap as worker_llm.provider.
- `worker_llm.api_key`：runtime not wired in Go.
- `worker_llm.api_base`：runtime not wired in Go.
- `worker_llm.api_key_env_var`：runtime not wired in Go.
- `worker_llm.max_tokens`：runtime not wired in Go.
- `worker_llm.max_context_tokens`：runtime not wired in Go.
- `worker_llm.endpoint`：runtime not wired in Go.
- `worker_llm.extra_body`：runtime not wired in Go.
- `worker_llm.num_ctx`：runtime not wired in Go.

### 2.20 `worker_tools`

| 键 | 类型 | 默认 | env 覆盖 | Python 读取点 | Go 读取点 | 状态 |
|---|---|---|---|---|---|---|
| `worker_tools.always_enabled_categories` | list[string] | null | — | `core/framework/agents/queen/queen_tools_defaults.py:462` | — | python_only |

- `worker_tools.always_enabled_categories`：P1: Go has no worker_tools reader; worker tool-tier overrides from configuration.json are not honored.

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
| MCP call timeout | file > env (exception to the general rule): mcp.timeout_seconds wins over HIVE_MCP_CALL_RESULT_TIMEOUT | go_only |

**空串 / 非 bool / 越界 / 坏 JSON 的处理要点**

- 功能开关（`adaptive_tool_budget`/`email_senders`/`stall_check`/`memory_timeline`）env 为空串或纯空白时按“未设置”处理，落到 configuration.json；`retention.enabled` 的空串则按 false 处理。两侧实现一致，这是两个不同的约定，不要合并。
- 非 bool 的 env 值走 deny-list：`0/false/no/off` 为 false，其它任何非空值为 true。因此 `HIVE_STALL_CHECK=disabled` 在两侧都为 true（刻意与 strconv.ParseBool 不同）。
- JSON 里类型不对的值：布尔开关要求 JSON bool（`1` 不算 true）；整数要求 JSON integer（`7.0`、`true`、字符串都被忽略）；section 不是对象时按空对象处理。
- 坏 JSON / 文件不可读：两侧都按 `{}` 处理并走默认值（Python 记 warning）；都接受 UTF-8 BOM。
- 越界：soul `hour` 0..23、各 count ≥1、sentinel seconds ≥1、`gcu_viewport_scale` 0.1..1.0；越界时 soul/gcu 回默认，sentinel 夹到下限。
- **唯一的反向优先级**：Go 的 `mcp.timeout_seconds` 覆盖 `HIVE_MCP_CALL_RESULT_TIMEOUT`（file > env），与总体规则相反，已在 `config-keys.json` 标记 `go_only` 并注明。

## 4. 差异清单

### go_partial（27）

- `llm.use_kimi_code_subscription` — P1: Go lists/detects the Kimi subscription in the REST surface; the runtime token path is not wired like Python's get_api_key branch.
- `llm.use_antigravity_subscription` — P1: Go has an antigravity token resolver in llm_config; no equivalent of Python's full provider branch (api_base=None, token refresh).
- `queen_llm.<queen_id>.provider` — P1: Go reads queen_llm only for the REST endpoints; ApplyQueenModel has no production assignment, so a queen's own model is not honored at session start.
- `queen_llm.<queen_id>.model` — same runtime wiring gap.
- `queen_llm.<queen_id>.api_key` — same runtime wiring gap.
- `queen_llm.<queen_id>.api_base` — same runtime wiring gap.
- `queen_llm.<queen_id>.api_key_env_var` — same runtime wiring gap.
- `queen_llm.<queen_id>.max_context_tokens` — same runtime wiring gap.
- `queen_llm.<queen_id>.endpoint` — same runtime wiring gap.
- `queen_llm.<queen_id>.extra_body` — same runtime wiring gap.
- `vision_fallback.provider` — P1: slot is CRUD-only in Go; find_vision_fallback's caption chain is not ported (runtime never reads it).
- `vision_fallback.model` — same runtime gap.
- `vision_fallback.api_key` — same runtime gap.
- `vision_fallback.api_base` — same runtime gap.
- `vision_fallback.api_key_env_var` — same runtime gap.
- `vision_fallback.max_tokens` — same runtime gap; 0/absent falls back to aux_max_tokens.
- `vision_fallback.endpoint` — same runtime gap.
- `worker_llm.provider` — P1: Go exposes worker_llm through the REST slot API but never resolves it for the runtime (colony/runtime.go comment: the port does not split queen/worker models by default). Python's session_manager.build_worker_llm does.
- `worker_llm.model` — same runtime gap as worker_llm.provider.
- `worker_llm.api_key` — runtime not wired in Go.
- `worker_llm.api_base` — runtime not wired in Go.
- `worker_llm.api_key_env_var` — runtime not wired in Go.
- `worker_llm.max_tokens` — runtime not wired in Go.
- `worker_llm.max_context_tokens` — runtime not wired in Go.
- `worker_llm.endpoint` — runtime not wired in Go.
- `worker_llm.extra_body` — runtime not wired in Go.
- `worker_llm.num_ctx` — runtime not wired in Go.

### python_only（6）

- `channels.<name>.enabled` — P2: optional-package channels (OneBot, etc.) are Python-only; Go has no channels.<name> runtime reader.
- `llm.aux_max_tokens` — P1: Go never reads llm.aux_max_tokens. internal/llm/complete.go has DefaultAuxMaxTokens=8192 but no configuration.json reader, so a user-pinned aux budget is ignored.
- `llm.antigravity_client_id` — Go has no configuration.json reader for the Antigravity OAuth client id (Python falls back to the public upstream constants).
- `llm.antigravity_client_secret` — same as antigravity_client_id.
- `loop.max_tool_result_chars` — P1: Go has the 30_000 default but no configuration.json reader for the global `loop` section; a file override is ignored.
- `worker_tools.always_enabled_categories` — P1: Go has no worker_tools reader; worker tool-tier overrides from configuration.json are not honored.

### go_only（4）

- `mcp.enabled` — Go reads an `mcp` block from configuration.json; Python's MCP servers come from .mcp.json / the registry. A Go extension, not an upstream key.
- `mcp.namespace` — tool-name namespace; blank keeps the default.
- `mcp.timeout_seconds` — priority REVERSED vs the general rule: file wins over env here (>0 only). This is the only configuration.json key in either tree with file > env.
- `mcp.servers` — accepts both the list and map shapes; a same-named registry entry is replaced by the file entry.

### unread（1）

- `created_at` — written by the installers, read by neither runtime. Documented example only.

非 `aligned` 键合计 **38** 个，已全部列出（上表）。

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
- 环境变量采用既有审计的清单；`env-vars.json` 里的读取点由扫描器补充，默认值/解析差异只对运行时相关变量做了人工核对。
