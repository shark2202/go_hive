# 配置参考

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
| `api_key_env_var` | string | 覆盖读取 key 的环境变量名（3.4） |
| `aux_max_tokens` | int | 小工具调用（记忆召回筛选、judge/sentinel、技能自测等）的输出上限；缺省 `min(8192, max_tokens)` |

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
| `turn_context` | `server.StartTurnContexts` | `turn_context.<name>.enabled` 为真时启动该已安装包，其文本作为 `<system-reminder>` 插在 queen 每回合用户消息之前；queen-only，单包 5s 超时，异常/启动失败跳过；默认无包 |
| `loop` | `hiveconf.MaxToolResultChars`、chat/worker 循环配置 | `loop.max_tool_result_chars`：工具结果溢出阈值（int，默认 30000）；queen chat 循环与 colony worker 循环都读取（代码路径；`TestChatLoopConfigAppliesTheLoopSection`、`TestBuildWorkerLoopConfigAppliesLoopSection`） |
| `worker_llm` / `vision_fallback` / `queen_llm` | `cli.queenAwareChatProvider`、`cli.workerChatProvider`、`cli.visionCaptionerFor` | 模型槽位，字段同 `llm`（`provider`/`model`/`api_key`/`api_base`/`api_key_env_var`/`extra_body`/`num_ctx`/`max_context_tokens`）。会话启动时读取：`queen_llm.<queen_id>` 选 queen 自己的模型（她的 colony 沿用），`worker_llm` 选 colony worker 专用模型，`vision_fallback` 为文本主模型提供工具图片描述；缺省或没有 `provider`+`model` 时回落到 `llm`（代码路径 + 测试） |
| `worker_tools` | `cli.loader_colony_host` → `server.WorkerKeepSetFromConfig` | `worker_tools.always_enabled_categories`：worker eager 工具类别覆盖（`list[string]`）；空列表关闭 worker tiering，缺省用内置 keep-set；混合类型列表不视为覆盖（与上游一致） |
| `mcp` | `cli.mcp_loader`、`internal/tools.MCPCallResultTimeout` | Go 扩展（Python 无此块）：`mcp.timeout_seconds` 是 MCP 调用超时的文件兜底；`HIVE_MCP_CALL_RESULT_TIMEOUT` 优先于它（env > file > 240s，`TestMCPCallResultTimeoutPrecedence`） |
| `channels` | 无 | **不支持**：上游用 `hive.channels` entry-point 插件宿主；Go 端口没有插件宿主与渠道生命周期，`channels.<name>.enabled` 没有可门控的消费点（登记在 `contracts/config-support-ledger.json`） |

`llm`、`worker_llm`、`vision_fallback`、`judge` 与 `queen_llm.<queen_id>` 是模型「槽位」，由设置接口写入。本版本运行时消费：`llm`（默认会话模型）、`judge`（judge 槽）、`queen_llm.<queen_id>`（会话启动时按 queen 选模型）、`worker_llm`（colony worker 专用模型）、`vision_fallback`（文本主模型的图片描述）。设置接口 `PUT` 写入后新会话生效；已运行会话只有主槽 `llm` 有热切换路径，其余槽位的原地切换未实现（`llm.Provider` 无 Reconfigure），需重建会话或重启 `serve`。

`llm.antigravity_client_id` / `llm.antigravity_client_secret` 是**只读**项：Go 侧有读取点（`ANTIGRAVITY_CLIENT_ID` / `ANTIGRAVITY_CLIENT_SECRET` 环境变量优先，其次配置文件），但端口的 Antigravity 登录读预发的 access token（`antigravity-accounts.json`），没有 OAuth 刷新流程去消费 client id/secret。`llm.use_antigravity_subscription` / `llm.use_kimi_code_subscription` 也只有 REST 面，运行时订阅 token 分支未移植。

## 3. 模型与 provider 解析优先级

解析入口是 `internal/cli/loader.go` 的 `resolveChatProvider`，在**每次创建会话时**执行。改环境变量或配置后需要重启 `hive serve` 才会被新会话读到。

### 3.1 模型

| 优先级 | 来源 | 实测 |
| --- | --- | --- |
| 1 | `POST /api/sessions` 请求体的 `model` | 是（`{"model":"claude-haiku-4-5-20251001"}` 生效） |
| 2 | `hive serve --model` | 是（`--model claude-haiku-4-5-20251001` + 空请求体生效） |
| 3 | `configuration.json` 的 `llm.model` | 是（不传 model 时用了配置里的 `gpt-4o-mini`） |
| 4 | 内置默认 `gpt-4o-mini` | 代码路径 |

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
| 1 | `configuration.json` 的 `llm.api_base` |
| 2 | `ANTHROPIC_API_BASE`，再 `ANTHROPIC_BASE_URL`（provider 为 anthropic 时） |
| 2 | `OPENAI_BASE_URL`，再 `OPENAI_API_BASE`（provider 为 openai 时） |
| 3 | 模型内置：`minimax/*` → `https://api.minimax.io/v1`；`openrouter/*` → `https://openrouter.ai/api/v1`；`kimi/*` → `https://api.kimi.com/coding`；`hive/*` → Hive 网关；本地模型 → `OLLAMA_HOST` 或 `http://127.0.0.1:11434` |
| 4 | `https://api.anthropic.com`（Anthropic 系）或 `https://api.openai.com/v1`（其他） |

### 3.4 API key

| 优先级 | 来源 |
| --- | --- |
| 1 | 环境变量名由 `llm.api_key_env_var` 指定 |
| 2 | 该 provider 的默认变量（3.5 表） |
| 3 | 再一次查 provider 默认变量（用于 `api_key_env_var` 指错时的兜底） |
| 4 | `HIVE_API_KEY` |
| 5 | 都没有 → 会话可创建，但 `POST /api/sessions/<id>/chat` 返回 503 `{"error":"No LLM configured for this session"}` |

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
| 设置接口（`PUT /api/config/...`） | 写入后新会话；主槽 `llm` 另有热切换路径，本文未实测。`worker_llm` / `vision_fallback` / `queen_llm` 的原地热切换不支持（`llm.Provider` 无 Reconfigure），需重建会话或重启 `serve` |
| colony 的 worker 定义 | 下次 spawn 时读取 |
