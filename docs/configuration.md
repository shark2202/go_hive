# 配置参考

> HTML 版（逐键 134 键、状态与读取点，自动生成防漂移）：[`configuration.html`](configuration.html)（`python3 scripts/gen_configuration_html.py --check`）

> 适用版本：`hive 1.0.26-go.1`
> 本文只写当前 Go 代码实际读取的配置项。标「代码路径」的条目由实现直接确认、但文档验证时没有单独实跑；标「实测」的条目跑过真实二进制。

## 1. HIVE_HOME 与磁盘布局

### 1.1 解析规则

| 顺序 | 条件 | 结果 |
| --- | --- | --- |
| 1 | 环境变量 `HIVE_HOME` 非空 | 转成绝对路径后使用 |
| 2 | 未设置 | `os.UserHomeDir()/.hive`（macOS/Linux 通常是 `~/.hive`） |

`HIVE_HOME` 不做 `~` 展开；需要展开时由 shell 完成（`export HIVE_HOME="$HOME/hive-home"`）。路径不存在也可以启动，组件在第一次写入时才创建需要的子目录——`hivehome.Home.EnsureDirs` 在 `serve` 路径中**不会**被调用（实测：空目录启动服务后，只有真正用到会话/凭据时才出现 `queens/`、`credentials/`）。

### 1.2 目录与文件

| 路径（相对 `$HIVE_HOME`） | 内容 | 何时出现 |
| --- | --- | --- |
| `configuration.json` | 全局配置（见第 2 节） | 你或设置接口写入时 |
| `queens/<queen>/sessions/<session_id>/` | 会话目录：`conversations/`、`meta.json` 等 | 创建/运行会话时 |
| `queens/default/sessions/…` | 未指定 queen 的聊天会话落在 `default` 下（实测） | 聊天会话创建后 |
| `colonies/<name>/` | colony 目录：`metadata.json`、worker 定义、`tracker/tracker.db` | colony 创建/导入时 |
| `colonies/<name>/queens/…` | colony 内 queen 的会话树 | fork 进 colony 时 |
| `memories/` | 记忆文件 | 记忆子系统写入时 |
| `credentials/credentials/` | 加密后的凭据文件 | 凭据保存时（实测） |
| `credentials/metadata/index.json` | 凭据索引：名称、键名、别名、过期时间；**不含明文值** | 凭据保存时（实测） |
| `mcp_registry/` | MCP 服务器安装记录 `installed.json` 等 | `hive mcp` 使用时 |
| `secrets/credential_key` | `HIVE_CREDENTIAL_KEY` 的持久副本（权限 `0600`） | 设置/保存密钥时 |
| `cache/` | 运行缓存 | 需要时 |
| `event_logs/` | 事件日志 | 事件落盘时 |
| `tool-artifacts/` | 工具产物 | 工具产生附件时 |
| `compaction_log/` | 上下文压缩日志 | 发生压缩时 |
| `llm_logs/` | LLM 调试日志（`hive debugger` 读取） | 开启 LLM 日志时 |
| `trusted_repos.json` | 已信任的技能来源仓库 | `hive skill` 信任项目时 |

会话转录的实际文件（实测）：

```text
$HIVE_HOME/queens/default/sessions/<session_id>/conversations/
├── meta.json        # 系统提示、压缩阈值、max_context_tokens 等
├── cursor.json      # 下一个 seq
└── parts/0000000000.json, 0000000001.json, …
```

`parts/<seq>.json` 的形状：用户消息 `{"content","is_client_input":true,"role":"user","seq"}`；助手消息 `{"content","role":"assistant","seq"}`。

## 2. configuration.json

路径固定为 `$HIVE_HOME/configuration.json`。读取语义（代码路径）：

- 容忍 UTF-8 BOM；
- 文件不存在、JSON 非法、或 JSON 之后还有尾随内容时，整体按空配置处理，不报错；
- 通过设置接口写回时保留未知键，使用两空格缩进 + 尾换行 + 原子替换。

### 2.1 最小可用示例（实测）

```json
{
  "llm": {
    "model": "gpt-4o-mini",
    "provider": "openai",
    "api_base": "http://127.0.0.1:8900/v1",
    "api_key_env_var": "MY_TEST_KEY"
  }
}
```

实测结果：进程里只设置 `MY_TEST_KEY=<值>`，不设置 `OPENAI_API_KEY`；新建会话不传 `model`；上游收到

```json
{"path":"/v1/chat/completions","authorization":"Bearer <MY_TEST_KEY>","model":"gpt-4o-mini"}
```

说明四个键都被读取并生效。

### 2.2 `llm` 段字段

| 键 | 类型 | 作用 |
| --- | --- | --- |
| `model` | string | 默认模型；见 3.1 的优先级 |
| `provider` | string | 选择凭据环境变量；空或 `custom` 时按模型名推断（3.2） |
| `api_base` | string | 上游基址；空时按 provider/模型取默认值（3.3） |
| `api_key` | string | 字面 key，优先于环境/凭据库；命中即用（3.4） |
| `api_keys` | list[string] | 多 key 池：取第一个非空项建 provider；2 个及以上时按 `llm.KeyPool` 轮转并在 429 时停键 60s（3.4） |
| `api_key_env_var` | string | 覆盖读取 key 的环境变量名（3.4） |
| `endpoint` | string | 命名端点（`model_endpoints`）；其凭据库 key 优先，没有则用端点占位符（3.4） |
| `max_tokens` | int | 输出预算；queen 回合用它（缺省 → 模型目录 → 8192，3.1） |
| `max_context_tokens` | int | queen 循环上下文窗口的主槽回退（缺省 → 模型目录 → 32000） |
| `aux_max_tokens` | int | 小工具调用（记忆召回筛选、judge/sentinel、技能自测等）的输出上限；缺省 `min(8192, max_tokens)` |
| `extra_body` | object | 每个请求体原样携带的字段（如 `reasoning_effort`）；非对象/空不发送 |
| `num_ctx` | int | 仅 `provider=ollama` 时发送，缺省 16384 |
| `use_claude_code_subscription` | bool | 用 `~/.claude/.credentials.json` 的 OAuth token，并加 Bearer 头 |
| `use_codex_subscription` | bool | 用 `~/.codex/auth.json` 的 token，走 ChatGPT backend 并加对应头 |
| `use_kimi_code_subscription` | bool | 用 `~/.kimi/config.toml` 的 token，base 固定 `https://api.kimi.com/coding` |
| `use_antigravity_subscription` | bool | **不支持**：上游会整会话切到 AntigravityProvider（未移植）；Go 读取该标志后对会话构建显式报错 |
| `antigravity_client_id` / `antigravity_client_secret` | string | 只读项（env 优先）；唯一消费者是未移植的 OAuth 刷新 |

### 2.3 其他顶层键

以下键由服务端读取，本文只登记语义，不做逐项实测：

| 键 | 读取方 | 说明 |
| --- | --- | --- |
| `stall_check` | `server.StallCheckEnabled` | 布尔；`HIVE_STALL_CHECK` 环境变量优先于它 |
| `adaptive_tool_budget` | `/api/config/features` | 布尔；缺省 true |
| `email_senders` | `/api/config/features` | 布尔；同步到 `HIVE_EMAIL_SENDERS` |
| `memory_timeline` | `/api/config/features` | 布尔 |
| `rate_limits` | `internal/ratelimit` | `platform.action.window` → 整数限额；环境变量 `HIVE_RATE_<PLATFORM>_<ACTION>_<WINDOW>` 优先 |
| `provider_library` | `/api/config/provider-library` | 保存的 provider 模板，按名称逐字存取 |
| `queen_mail` | `internal/qmail`、`internal/cli` | queen 互写：`enabled`/`queens`/`daily_limit`/`max_depth`；工具面与投递细节见 [queen-mail.md](queen-mail.md)。启动时读取，改后需重启 |
| `gcu_enabled` | `cli.browserBridgeRequested` | 布尔；`HIVE_BROWSER_BRIDGE` 环境变量优先，未设置时决定 serve 是否拉起 `browserbridge`（缺省开） |
| `gcu_viewport_scale` | `server.GcuViewportScale` → `HIVE_BROWSER_VIEWPORT_SCALE` | 数字 0.1–1.0，默认 0.8；非数字/越界回落 0.8，显式 env 覆盖按 [0.1, 1.0] 夹取。桥在 `GET /status` 的 `viewport_scale` 与启动日志上报（实测 0.65 / 0.55 / 0.8）。上游 getter 自扩展化浏览器后无消费点，Go 只读并上报，不改截图/抓取几何 |
| `turn_context` | `server.StartTurnContexts` | `turn_context.<name>.enabled` 为真时启动该已安装包，其文本作为 `<system-reminder>` 插在 queen 每回合用户消息之前；queen-only，单包 5s 超时，异常/启动失败跳过。Go 二进制内置 `probe` 包（编译期注册，默认未启用：日志按上游口径报 `installed but off`，不注入）；未配置任何包时与上游「无包安装」行为一致 |
| `loop` | `hiveconf.MaxToolResultChars`、chat/worker 循环配置 | `loop.max_tool_result_chars`：工具结果溢出阈值（int，默认 30000）；queen chat 循环与 colony worker 循环都读取（代码路径；`TestChatLoopConfigAppliesTheLoopSection`、`TestBuildWorkerLoopConfigAppliesLoopSection`） |
| `worker_llm` / `vision_fallback` / `queen_llm` | `cli.queenAwareChatProvider`、`cli.workerChatProvider`、`cli.visionCaptionerFor`、`server.ReconfigureSection` | 模型槽位，字段同 `llm`（`provider`/`model`/`api_key`/`api_base`/`api_key_env_var`/`extra_body`/`num_ctx`/`max_context_tokens`）。热切换矩阵见 §3.8：`queen_llm.<queen_id>` 写入即重建该 queen 运行中会话的 provider（她的 colony 沿用）；`vision_fallback` 每次 caption 重新解析，写入即对下一张工具图片生效；`worker_llm` 按上游语义在下一个 colony/会话构建时生效。缺省或没有 `provider`+`model` 时回落到 `llm`（代码路径 + 测试） |
| `worker_tools` | `cli.loader_colony_host` → `server.WorkerKeepSetFromConfig` | `worker_tools.always_enabled_categories`：worker eager 工具类别覆盖（`list[string]`）；空列表关闭 worker tiering，缺省用内置 keep-set；混合类型列表不视为覆盖（与上游一致） |
| `mcp` | `cli.mcp_loader`、`internal/tools.MCPCallResultTimeout` | Go 扩展（Python 无此块）：`mcp.timeout_seconds` 是 MCP 调用超时的文件兜底；`HIVE_MCP_CALL_RESULT_TIMEOUT` 优先于它（env > file > 240s，`TestMCPCallResultTimeoutPrecedence`） |
| `channels` | `server.ChannelSections` → serve 启动告警 | **字段级对齐、不生效**：上游用 `hive.channels` entry-point 插件宿主按 `channels.<name>.enabled` 启动渠道；Go 端口没有插件宿主与渠道生命周期。Go 按上游形状读取该段（对象 → 每渠道对象 → truthy `enabled`，默认 false），任何配置写入都原样保留，`GET /api/channels` 保持上游回体形状（无已安装渠道时为空列表），启用项在 serve 启动时打印「无渠道宿主、不会运行」的告警。登记在 `contracts/config-support-ledger.json` |

`llm`、`worker_llm`、`vision_fallback`、`judge` 与 `queen_llm.<queen_id>` 是模型「槽位」，由设置接口写入。本版本运行时消费：`llm`（默认会话模型）、`judge`（judge 槽）、`queen_llm.<queen_id>`（会话启动按 queen 选模型，写后原地切换）、`worker_llm`（colony worker 专用模型）、`vision_fallback`（文本主模型的图片描述，逐次 caption 解析）。热切换矩阵见 §3.8：主槽 `llm` 与 `queen_llm.<id>` 写入即重建运行中会话的 transport，`vision_fallback` 写入即对下一张工具图片生效，`worker_llm` 与上游一致只对下一个 colony/会话生效；进行中的回合始终用旧 provider 跑完（回合级 pin）。

`llm.antigravity_client_id` / `llm.antigravity_client_secret` 是**只读**项：Go 侧有读取点（`ANTIGRAVITY_CLIENT_ID` / `ANTIGRAVITY_CLIENT_SECRET` 环境变量优先，其次配置文件），但端口的 Antigravity 登录读预发的 access token（`antigravity-accounts.json`），没有 OAuth 刷新流程去消费 client id/secret。`llm.use_kimi_code_subscription` 的运行时分支已接通（token 读 `~/.kimi/config.toml`，base 固定 `https://api.kimi.com/coding`，`kimi/` 前缀走 Anthropic Messages 协议）；`llm.use_antigravity_subscription` 仍只有 REST 面——Python 会整会话切到 `AntigravityProvider`（Gemini cloudcode API + OAuth 刷新），Go 未移植该 provider，会话构建对标志显式报错而不是静默发无键请求。

## 3. 模型与 provider 解析优先级

解析入口是 `internal/server/main_slot.go` 的 `MainSlotProvider`（`internal/cli/loader.go::resolveChatProvider` 直接调用它），在**每次创建会话时**执行。改环境变量或配置后需要重启 `hive serve`（或新建会话）才会被读到；运行中的 provider 只由热切换路径替换（§3.8）。

### 3.1 模型

| 优先级 | 来源 | 实测 |
| --- | --- | --- |
| 1 | `POST /api/sessions` 请求体的 `model`（按原样） | 是（`{"model":"claude-haiku-4-5-20251001"}` 生效） |
| 2 | `hive serve --model` | 是（`--model claude-haiku-4-5-20251001` + 空请求体生效） |
| 3 | `configuration.json` 的 `llm.provider` + `llm.model`，按 Python `get_preferred_model` 拼成 `provider/model`（`openrouter/<id>` 的模型 id 先去前缀） | 代码路径（`TestMainSlotModelJoinsProviderAndModelLikePython`） |
| 4 | 内置默认 `anthropic/claude-sonnet-5-5`（与上游 `get_preferred_model` 一致） | 代码路径 |

### 3.2 provider

1. `llm.provider` 非空且不是 `custom` → 直接使用；
2. 否则看模型名前缀（`/` 之前）是否在已知 provider 集合里：`anthropic`、`openai`、`gemini`、`google`、`minimax`、`groq`、`cerebras`、`openrouter`、`mistral`、`together`、`deepseek`、`kimi`、`hive`、`zai`、`xai`；
3. 否则 `anthropic/*`、`claude-*` → `anthropic`；`gemini-*` → `gemini`；其余 → `openai`。

`provider` 只决定**用哪个环境变量取 key**；**线协议由模型名决定**：

| 模型名 | 协议 | 端点 |
| --- | --- | --- |
| `anthropic/*`、`claude-*`、`kimi/*`、`hive/*` | Anthropic Messages | `POST <base>/v1/messages`，`x-api-key` + `anthropic-version: 2023-06-01` |
| `openrouter/*` | OpenAI-compatible（即使路由到 Anthropic 模型） | `POST <base>/chat/completions` |
| 其余 | OpenAI-compatible | `POST <base>/chat/completions`，`Authorization` 头为 `Bearer` + key |

`<base>` 末尾缺 `/v1` 时会补上；已经以 `/v1` 或完整端点结尾时不会重复拼接。

### 3.3 API base

| 优先级 | 来源 |
| --- | --- |
| 1 | 订阅端点（与上游 `get_api_base` 同序）：`use_codex_subscription` → `https://chatgpt.com/backend-api/codex`；`use_kimi_code_subscription` → `https://api.kimi.com/coding`；`use_antigravity_subscription` → 空（上游由 AntigravityProvider 直连） |
| 2 | `configuration.json` 的 `llm.api_base` |
| 3 | provider 为 `openrouter` → `https://openrouter.ai/api/v1` |
| 4 | 模型内置：`minimax/*` → `https://api.minimax.io/v1`；`kimi/*` → `https://api.kimi.com/coding`；`hive/*` → Hive 网关；本地模型 → `OLLAMA_HOST` 或 `http://127.0.0.1:11434` |
| 5 | `https://api.anthropic.com`（Anthropic 系）或 `https://api.openai.com/v1`（其他） |

### 3.4 API key

| 优先级 | 来源 |
| --- | --- |
| 1 | `llm.endpoint` 指向的命名端点：其凭据库密钥，否则端点占位符（永不落到环境里的共享 key） |
| 2 | `llm.api_keys` 池的第一个非空项；2 个及以上时在 provider 上启用 `llm.KeyPool` 轮转（429 停键 60s） |
| 3 | `llm.api_key`（字面 key） |
| 4 | 订阅 token（按 `use_claude_code_subscription` → `use_codex_subscription` → `use_kimi_code_subscription` → `use_antigravity_subscription` 顺序，逐个尝试，取到即用）：Claude Code 读 `~/.claude/.credentials.json`（macOS 上游还查 Keychain；Go 只读文件、不刷新）、Codex 读 `~/.codex/auth.json`、Kimi 读 `~/.kimi/config.toml`、Antigravity 读 `antigravity-accounts.json` 的预发 access token |
| 5 | 凭据库（`HIVE_CREDENTIAL_KEY` 存在时按 provider 映射） |
| 6 | `llm.api_key_env_var` 指定的环境变量 |
| 7 | 该 provider 的默认变量（3.5 表） |
| 8 | `HIVE_API_KEY` |
| 9 | 都没有 → 会话构建返回错误 `no API key configured for model ...`（服务端打印 `Warning: chat runtime unavailable ...`）；`POST /api/sessions` 仍返回 201，该会话的 `chat` 返回 503（§3.7） |

### 3.5 provider 与默认环境变量

| provider | 环境变量 |
| --- | --- |
| `anthropic` | `ANTHROPIC_API_KEY` |
| `openai` | `OPENAI_API_KEY` |
| `gemini` | `GEMINI_API_KEY` |
| `google` | `GOOGLE_API_KEY` |
| `minimax` | `MINIMAX_API_KEY` |
| `groq` | `GROQ_API_KEY` |
| `cerebras` | `CEREBRAS_API_KEY` |
| `openrouter` | `OPENROUTER_API_KEY` |
| `mistral` | `MISTRAL_API_KEY` |
| `together` | `TOGETHER_API_KEY` |
| `deepseek` | `DEEPSEEK_API_KEY` |
| `kimi` | `KIMI_API_KEY` |
| `hive` | `HIVE_API_KEY` |
| `zai` | `ZAI_API_KEY` |
| `xai` | `XAI_API_KEY` |

实测覆盖：`openai`（`Authorization: Bearer`）、`anthropic`（`x-api-key`）；其余条目由代码表确认，文档验证没有逐个调用。

### 3.6 本地模型

模型名以 `ollama/`、`ollama_chat/`、`vllm/`、`lm_studio/`、`llamacpp/` 开头时视为本地模型：**不要求 API key**，`api_base` 取配置或默认值（Ollama 可用 `OLLAMA_HOST` 覆盖）。`ollama/` 在发出请求前会归一成 `ollama_chat/`，`wire model` 去掉 `ollama_chat/` 前缀。此路径为代码确认，未接真实 Ollama 实跑。

### 3.7 「没有 key」到底发生什么

1. `POST /api/sessions` 返回 201，会话进入列表；
2. 服务端打印 `Warning: chat runtime unavailable for <session_id>: no API key configured …`；
3. 该会话的 `chat` 返回 503，而不是把消息丢进一个永远不工作的队列；
4. 该会话只存在于内存：`hive session list` 能看到，服务重启后消失；没有 transcript 落盘，`hive session list --cold` 不会显示它。

### 3.8 模型槽位热切换（PUT 后何时生效）

`llm.Provider` 在构造后只读（请求在读它的字段，原地改字段会与在读请求竞态）。可替换面是 `internal/llm.ProviderSlot`：`Reconfigure(fullModel, apiKey, apiBase, extraBody)` 用**同一个** `NewHTTPProvider` 工厂重建 transport 并原子替换（不复制 provider 请求逻辑），会话（及 colony 的 worker 槽）持有 slot。`ChatRuntime` 在**每回合开始**把当前 provider pin 住，因此写入落在回合中间时：

- 进行中的回合：继续用旧 provider 跑完（不撕裂，回合内迭代不会换模型）；
- 下一回合：解析到重建后的 provider（新 model / api_base / api_key / extra_body）。

与上游的差异要写清：Python 的 `LiteLLMProvider.reconfigure` 原地改共享对象，所以**同一回合的下一次模型调用**就用新值；Go 为了不与会话中正在读 provider 字段的请求竞态，保留 `*Provider` 不可变 + slot 替换，pin 粒度是**回合**而不是单次调用。二者在「PUT 后第一个新回合」达成一致，差别只在跨回合边界内的长工具循环。

另一个边界：PUT 重建的 transport 使用**路由解析出的** key（section 的 literal / endpoint / env），不会重新挂 `llm.api_keys` 池；配置键池的轮转由会话构建路径（`MainSlotProvider`）安装，重建后的 transport 在下一次会话构建时恢复池。上游的 `api_key_resolver` 是逐调用解析，这一点 Go 以「重建时传 key」记录差异。

| 槽位 | 写入接口 | 生效时机 |
| --- | --- | --- |
| `llm`（主槽） | `PUT /api/config/llm`、`PUT /api/config/llm-sections`(role=llm)、`PUT /api/models/slots/llm` | 写入即重建所有运行中会话（跳过有自己模型的 queen）及其 colony worker slot；下一回合生效 |
| `queen_llm.<id>` | `PUT /api/queen/{id}/llm`、`PUT /api/models/slots/queen:<id>` | 写入即重建该 queen 运行中会话的 provider（`sessions_switched` 计数）；清空 section 则回主模型 |
| `vision_fallback` | `PUT /api/config/llm-sections`、`PUT /api/models/slots/vision_fallback` | 每次 caption 重新读槽位：写入后下一张工具图片即用新 model/api_base，无需重建会话 |
| `worker_llm` | `PUT /api/config/llm-sections`、`PUT /api/models/slots/worker_llm` | 与上游 `_write_role_section` 一致：**不改运行中会话**（`sessions_swapped: 0`），下一个 colony/会话构建的 worker 用新 provider |
| `judge` | `PUT /api/config/llm-sections` | 每次判定新建 client，写入即生效 |

写入返回体沿用上游形状：槽位接口 `{role, section, sessions_swapped}`，queen 路由 `{queen_id, section, sessions_switched}`；错误仍是 400 + `{"error": ...}`（缺 `model`、非法 role、queen 不存在、key 校验失败）。并发 PUT 由 `MutateHiveConfig` 的文件锁与 slot 的读写下串行化；不同会话并发写同一槽位各自重建自己的 transport。

实测（`go_hive/scripts/e2e_config_hotswap.py`：真实 `serve` + 两个假 OpenAI 网关，网关记录每个请求的 model 与图片块；原始输出见 `docs/e2e-assets/config-hotswap/`）：

- 改 `queen_llm.qa`：POST 前请求落在旧网关（`queen-old`）；`PUT /api/queen/qa/llm` 返回 `sessions_switched: 1`；下一回合请求落在新网关（`queen-new`），同一会话两轮都答出（3 个 assistant part），GET 回读等于写入的 section。
- 改 `vision_fallback`（会话创建时该槽位还不存在）：下一张 `attach_file` 工具图片的 caption 请求落在新网关（`caption-new`，请求体带 `image_url`）。
- 改 `worker_llm`：返回 `sessions_swapped: 0`；新建 colony 会话后其 worker 的请求落在新网关（`worker-new`），queen/overseer 仍用 queen 模型。
- 单测：`internal/llm/hotswap_test.go`（重建/替换/并发，`-race`）、`internal/server/model_hotswap_test.go`（回合 pin、queen 路由、config 路由、`sessions_swapped` 语义）、`internal/colony/worker_model_slot_test.go`（worker 槽消费）、`internal/visionfallback/visionfallback_hotswap_test.go`（逐次解析）。

## 4. 凭据

### 4.1 加密与存储

| 项 | 说明 |
| --- | --- |
| 加密算法 | Fernet（与 Python `cryptography` 兼容） |
| 密钥变量 | `HIVE_CREDENTIAL_KEY` |
| 密钥文件 | `$HIVE_HOME/secrets/credential_key`（`0600`） |
| 未设密钥时 | 进程内生成临时密钥并打印 `credentials: generated a new encryption key; set HIVE_CREDENTIAL_KEY to persist credentials across restarts`；重启后旧密文无法解密 |
| 索引 | `credentials/metadata/index.json` 只含元数据，不含值（实测） |
| 日志重定向 | `HIVE_CRED_LOG=<file>` 把 `hive-cred` 日志写文件，保持 stdout 干净 |

`ADEN_API_KEY` 是 `internal/credentials.LoadAdenAPIKey` 读取的 Aden 平台 key（代码路径）。

### 4.2 查看凭据（不打印值）

```bash
./dist/hive-cred browse
./dist/hive-cred inspect google
```

实测（空 HIVE_HOME）：

```text
# Available credentials

No credentials are connected yet.
```

```text
# google
No connected account found for this id.
```

`hive-cred token <id> [--account <alias>] [--key <name>]` 才会打印值；OAuth 凭据会先刷新再打印。脚本里用 `TOK=$(hive-cred token google)` 捕获。没有该凭据时退出码 4，并提示先用 `browse` 查账户别名（实测）。

## 5. Colony 配置

### 5.1 目录

```
$HIVE_HOME/colonies/<name>/
├── metadata.json        # colony 元数据
├── worker.json          # worker 定义（也可用 .yaml/.yml；可多个）
├── tracker/tracker.db   # SQLite，工具为 colony 绑定时使用
└── workers/<worker_id>/ # 运行后的 worker 转录
```

`hive serve --colony <arg>` / `hive colony info <arg>` 的解析顺序：

1. 参数按路径存在且是目录 → 用它；
2. 否则用 `$HIVE_HOME/colonies/<arg>`。

### 5.2 metadata.json

`hive colony list` / `info` 读取的键（缺省按空串/`?` 显示）：

| 键 | 用途 |
| --- | --- |
| `queen_name` | 创建该 colony 的 queen |
| `queen_session_id` | 来源 queen 会话 |
| `source_session_id` | 来源会话（可选） |
| `created_at` | 创建时间（RFC3339 字符串） |
| `deleted` | 为真时 `hive colony list` 跳过（发现逻辑） |

### 5.3 worker 定义

目录内所有 `.json` / `.yaml` / `.yml` 文件都会被视为候选，**按文件名字典序取第一个**。保留文件名不会被当成 worker：`agent`、`flowchart`、`triggers`、`configuration`、`metadata`（发现时还排除 `tasks`）。

实测可用示例：

```json
{
  "name": "Research Worker",
  "description": "Documentation probe worker",
  "system_prompt": "You are a worker in a colony.",
  "agent_type": "event_loop",
  "tools": ["read_file", "write_file", "terminal_exec"],
  "goal": {
    "description": "Collect facts for the docs",
    "success_criteria": ["facts recorded"],
    "constraints": []
  }
}
```

字段（代码路径，未知键忽略）：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `name` / `description` | string | 标识与描述 |
| `system_prompt` / `identity_prompt` | string | 系统提示 |
| `agent_type` | string | 缺省 `event_loop` |
| `model` | string | 解析进 `AgentSpec`；本版本 colony 运行时统一使用 `serve` 解析出的 provider，不会按该字段切换模型 |
| `tools` | list 或 `{"policy","allowed"}` | 工具白名单形式；`list` 是 worker-config 形态，对象形态用于导出的 agent |
| `goal` | object | `description`、`success_criteria[]`、`constraints[]` |
| `loop_config` | object | 循环覆盖；只有 5 个键允许 queen 调整 |
| `max_tokens` | int | 缺省 4096 |
| `conversation_mode` | string | 缺省 `continuous` |
| `max_retries` | int | 缺省 3 |
| `input_keys` / `output_keys` / `nullable_output_keys` | string[] | 数据契约 |
| `report_schema` | object | worker 报告 schema |
| `skip_judge` / `client_facing` | bool | 行为开关 |

### 5.4 验证过的 colony 命令

```bash
./dist/hive colony list
./dist/hive colony info docs_probe
./dist/hive colony list --json
./dist/hive serve --port 8896 --colony docs_probe
```

实测 `colony info`：

```text
Colony:           docs_probe
Queen:            queen_operations
Queen Session:    session_docs_probe
Workers (1):
  • worker
      goal:  Collect facts for the docs
      tools: 3
```

`serve --colony` 需要能解析出 provider；否则打印 `Warning: colony runtime unavailable for <id>: …`，会话以冷记录保留。

## 6. 常用环境变量

| 变量 | 作用 | 验证 |
| --- | --- | --- |
| `HIVE_HOME` | 状态根目录 | 实测 |
| `HIVE_API_KEY` | 所有 provider 的最后 key 兜底 | 代码路径 |
| `OPENAI_API_KEY` / `OPENAI_BASE_URL` / `OPENAI_API_BASE` | OpenAI-compatible 凭据与基址 | 实测（fake 上游） |
| `ANTHROPIC_API_KEY` / `ANTHROPIC_BASE_URL` / `ANTHROPIC_API_BASE` | Anthropic 凭据与基址 | 实测（fake 上游） |
| `OLLAMA_HOST` | 本地 Ollama 端点 | 代码路径 |
| `HIVE_CREDENTIAL_KEY` | 凭据加密密钥 | 实测（生成提示与落盘） |
| `HIVE_CRED_LOG` | `hive-cred` 日志文件 | 代码路径 |
| `ADEN_API_KEY` | Aden 平台 key | 代码路径 |
| `HIVE_STALL_CHECK` | 覆盖 `configuration.json` 的 `stall_check` | 代码路径 |
| `HIVE_LLM_BASE_URL` | 代理端点识别：命中时 Anthropic 代理改用 `Authorization` 头（`Bearer`） | 代码路径 |
| `HIVE_RUNTIME_PORT` | `serve` 写给子进程的监听端口 | 实测（serve 设置） |
| `HIVE_REQUEST_EXECUTOR_MAX` | 请求执行器最大 worker 数（`/api/health` 的 `request_executor.max_workers`） | 代码路径 |
| `HIVE_MAX_CONCURRENT_WORKERS` | colony 并发 worker 上限（默认 4） | 代码路径 |
| `HIVE_STORAGE_PATH` | 集成工具的存储根（`web_scrape` 下载目录；同时作为 MCP 子进程的会话 cwd） | 代码路径 |
| `HIVE_MCP_CALL_RESULT_TIMEOUT` | MCP 调用结果超时上限（240s）；优先于 `mcp.timeout_seconds`（env > file） | 代码路径 + 测试 |
| `HIVE_MAX_ITER` | 单回复聊天部署下 queen 回复循环上限（正整数；0/负数/非数字忽略） | 代码路径 + 测试 |
| `HIVE_SPLIT_WORKER_EVENTS` | `1`/`true`/`yes` 时把并行 worker 的逐回合 chatter 拆到独立 `events.jsonl`（默认关） | 代码路径 + 测试 |
| `HIVE_BASH_PATH` / `HIVE_RIPGREP_PATH` | 终端工具使用的 bash / ripgrep | 代码路径 |
| `HIVE_REGISTRY_URL` | 技能注册表地址 | 代码路径 |

桌面端/渠道内部变量（`HIVE_DESKTOP_*`、`HIVE_BRIDGE_PORT`、`HIVE_CLOUD_BASE`、`HIVE_EMAIL_SENDERS` 等）不属于用户配置面，本文不展开。

## 7. 配置生效时机

| 改动 | 何时生效 |
| --- | --- |
| 环境变量（key、base） | 重启 `hive serve` 后，新建的会话 |
| `configuration.json` 的 `llm` | 重启 `hive serve` 后，新建的会话 |
| `HIVE_MAX_ITER` | 会话循环创建时读取（已运行会话不受影响） |
| `HIVE_SPLIT_WORKER_EVENTS` | colony 启动时读取（重启 `serve` 后生效） |
| `HIVE_STALL_CHECK` | 服务端每次判定时读取（`StallCheckEnabled` 每次调用都读） |
| 设置接口（`PUT /api/config/...`） | 写入后新会话；主槽 `llm`/`queen_llm.<id>` 另有热切换路径并已实测（§3.8）：写入即重建运行中会话的 transport，下一回合生效；`vision_fallback` 逐次 caption 解析，写入即生效；`worker_llm` 与上游一致只对下一个 colony/会话生效。进行中的回合用旧 provider 跑完 |
| colony 的 worker 定义 | 下次 spawn 时读取 |
