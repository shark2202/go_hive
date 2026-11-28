# 10 · 模型与凭据

> 关键文件：
> `core/framework/llm/`（provider、litellm、anthropic、antigravity、model_catalog、endpoints、capabilities、key_pool、stream_events）
> `core/framework/credentials/`（models、storage、store、resolve、validation、catalog、cli）
> `core/framework/agent_loop/internals/credential_tool.py`

## 1. 模型抽象

### 1.1 Provider 接口

`llm/provider.py` 定义统一抽象：

```python
class LLMProvider(ABC):
    async def complete(...) -> LLMResponse
    async def acomplete(...) -> LLMResponse
    async def stream(...) -> AsyncIterator[StreamEvent]
```

`LLMResponse` 包含：

- 文本内容、工具调用；
- `input_tokens` / `output_tokens`；
- `cost_usd`（provider 或定价表能算时）；
- `finish_reason`；
- 原始响应（调试用）。

`Tool` 是 JSON Schema 描述；`ToolUse` / `ToolResult` 是循环与 provider 之间的标准交换格式。

### 1.2 实现

| 实现 | 用途 |
|---|---|
| `LiteLLMProvider` | 主路径；通过 LiteLLM 覆盖大量 provider（`litellm==1.83.4` 锁定） |
| `AnthropicProvider` | Anthropic 原生；支持 `cache_control`、thinking、原生图片 |
| `AntigravityProvider` | Antigravity 订阅/端点 |
| `MockLLMProvider` | 测试/结构验证，不调用真实模型 |

`litellm.py` 是最大的 provider 实现（3,678 行），处理：

- 流式解析与工具调用增量；
- 不同 provider 的 reasoning 字段（Qwen/DeepSeek/GLM 的 `reasoning_content`）；
- 错误分类（transient / capacity / context-too-large / auth）；
- 定价与 token 统计；
- `extra_body`（provider 专有字段，如 `reasoning_effort`、`thinking`）；
- OpenAI 兼容端点。

### 1.3 模型目录

`llm/model_catalog.json` 是**精选目录**（不是 LiteLLM 全量）：

- **12 个 provider**：Anthropic、OpenAI、Gemini、Groq、Cerebras、MiniMax、Mistral、Together、DeepSeek、Kimi、Hive、OpenRouter；
- 每个 provider 有 `default_model` 与 `models[]`；
- 每个模型可带：`max_tokens`、`max_context_tokens`、`supports_vision`、`pricing_usd_per_mtok`；
- **8 个预设**：`claude_code`、`zai_code`、`codex`、`minimax_code`、`kimi_code`、`hive_llm`、`antigravity`、`ollama_local`。

目录的作用：

- UI 的模型选择器；
- 上下文/输出上限的默认来源（配置未显式写时）；
- 视觉能力判断；
- 成本估算。

### 1.4 模型槽位

| 槽位 | 配置键 | 用途 |
|---|---|---|
| 主模型 | `llm` | Queen 默认、后台小调用 |
| Worker 模型 | `worker_llm` | Worker；未设置则继承主模型 |
| 单 Queen 模型 | `queen_llm.<queen_id>` | 某个 Queen 专属 |
| 视觉回退 | `vision_fallback` | 纯文本模型的图片 caption |
| Judge | `judge` | stall check、Channel 的 yes/no 判断 |
| 记忆反思/时间线 | `memory_reflection.model` | `worker` 或 `main` |
| Soul | `soul.queens.<id>.model` | `queen` 或 `worker` |

槽位解析顺序（以 Queen 上下文为例）：

```text
queen_llm[queen_id] → llm → 目录默认
```

Worker 使用 `worker_llm`（若设置）否则 Queen 的模型。

### 1.5 OpenAI 兼容端点

`configuration.json`：

```json
"model_endpoints": {
  "magpie": {"api_base": "http://127.0.0.1:3425/v1"},
  "ollama": {"api_base": "http://localhost:11434/v1"}
}
```

- Magpie 即使不配置也内置在 `127.0.0.1:3425/v1`；
- 端点的 key 存凭据库（`endpoint_<name>`），**不写入 configuration.json**；
- 无 key 的本地端点会收到占位 key，防止 Hive 持有的 `OPENAI_API_KEY` 被误发；
- 端点模型列表通过 `GET {api_base}/models` 实时拉取，带 60s 缓存；
- 上下文窗口从 `context_length` / `max_model_len` / `max_context_tokens` / `context_window` 读取。

### 1.6 Key 池

`llm/key_pool.py`：

- 多 key 轮询；
- 429 后把该 key 冷却（默认 60s）；
- 全部冷却时选最早恢复的 key，避免直接睡眠；
- 线程安全（并行工具调用会并发取 key）。

### 1.7 视觉能力

`llm/capabilities.py`：

- `model_supports_vision(model)`：目录显式 false 才关闭；未知模型默认 true；
- `filter_tools_for_model(tools, model)`：纯文本模型看不到“只产图”的工具；
- `supports_image_tool_results(model)`：决定图片能否放在工具结果里，还是移到下一条用户消息。

## 2. 凭据：加密存储与按需解析

### 2.1 数据模型

`credentials/models.py`：

```python
CredentialObject(
  id="github_oauth",
  credential_type=OAUTH2,
  keys={
    "access_token": CredentialKey(value=SecretStr(...), expires_at=...),
    "refresh_token": CredentialKey(value=SecretStr(...)),
  },
  provider_id="oauth2",
  alias="work",
  identity=CredentialIdentity(email=...),
  auto_refresh=True,
)
```

支持的 `CredentialType`：`api_key`、`oauth2`、`basic_auth`、`bearer_token`、`custom`。

### 2.2 存储

`credentials/storage.py`：

- **`EncryptedFileStorage`**（默认）：每个凭据一个 `.enc` 文件，Fernet（AES-128-CBC + HMAC）加密；
- 密钥来自 `HIVE_CREDENTIAL_KEY`；缺失时生成临时 key（重启即失效，会警告）；
- `index.json` 是**未加密**的元数据快照：id、provider、alias、可见 key 名、identity、过期时间、最后刷新时间；不包含 secret 值；
- 其他实现：`EnvVarStorage`、`InMemoryStorage`、`CompositeStorage`；
- 原子写（临时文件 + replace）。

### 2.3 解析与刷新

`credentials/resolve.py`：

- 先查本地 account（alias）；
- 再查加密存储；
- 读取时 `refresh_if_needed`；
- OAuth 过期自动刷新；刷新失败抛 `CredentialExpiredError`，而不是返回一个坏 token；
- 多账号：`account=<alias>` 选择；无 alias 时默认取唯一账号或 `default`。

### 2.4 智能体的凭据工具

`agent_loop/internals/credential_tool.py` 只暴露一个 CLI 风格的 `credentials` 工具：

| action | 行为 |
|---|---|
| `help` / 无参数 | 使用说明 |
| `collect` | 停泊循环，弹安全表单；secret 由前端直接 POST 到加密存储，**不进入对话** |
| `attach` / `detach` | 把凭据固定/解绑到当前会话的提示 |
| `browse` / `inspect` / `reveal` | **已退役**，改为 `hive-cred` CLI |

安全设计：

- 任何 action 都不返回 secret 值；
- `collect` 只传字段规格与 correlation id；
- 前端 `POST /api/sessions/{id}/credential-form` 直接写存储；
- `hive-cred token` 在脚本调用时现取（并触发刷新），避免把短期 OAuth token 粘进文件。

### 2.5 `hive-cred` CLI

```bash
hive-cred browse [query] --json
hive-cred inspect <credential_id> [--account alias] --json
hive-cred token <credential_id> [--account alias] [--key name]
```

设计目标：**脚本按需取新鲜值**，不在文件/环境里长期持有短期 token。

### 2.6 预检（preflight）

- `credentials/validation.py` 在启动与 spawn 前检查所需凭据；
- 缺失时结构化返回（`CREDENTIALS_REQUIRED` 事件），前端弹窗；
- `run_worker` 会在批量 spawn 前做一次预检，避免 N 个 Worker 各报一次同样的缺失；
- 预检失败的 Worker 工具会被过滤掉，而不是让每个 Worker 撞墙。

### 2.7 账号身份与多租户

- `CredentialIdentity`：email/username/workspace/account_id；
- `account=<alias>` 让同一 provider 的多个账号共存（工作/个人、多个社媒）；
- Worker Profile 的 `integrations: {provider: alias}` 把账号绑定到 Worker；
- 显式 `account` 参数优先于默认绑定；
- 这是“一个 Colony 驱动多个账号”的基础能力。

## 3. 订阅与 OAuth

`loader/agent_loader.py` 处理本机订阅凭据：

- **Claude Code**：读 `~/.claude/.credentials.json`；macOS 还会读 Keychain（`Claude Code-credentials`）；
- **OpenAI Codex**：读 `~/.codex/auth.json`；
- OAuth token 刷新有 5 分钟提前量；
- 其他订阅（Kimi Code、MiniMax、Z.AI、Antigravity）由目录预设/专用 provider 接入。

> 文档明确提示：把“编程订阅”用于无人值守的 Agent 可能违反订阅条款，API key 是受支持的方式。这是产品/合规风险，不是技术缺陷。

## 4. 模型与凭据的安全边界

### 4.1 做得好的地方

- 凭据静态加密；`SecretStr` 防止日志泄露；
- `index.json` 只存元数据，不存 secret；
- Agent 永远拿不到 secret 值；只能通过工具/CLI 使用；
- 端点 key 与全局环境变量隔离，避免误发；
- OAuth 刷新失败显式报错，不静默使用过期 token；
- `hive-cred token` 现取现用，缩小 token 生命周期。

### 4.2 仍需注意

- `HIVE_CREDENTIAL_KEY` 本身若泄露，加密存储等同于明文；它通常来自 shell 配置/桌面注入。
- 终端工具可以执行 `hive-cred token` 或读取进程环境；**Agent 的权限边界仍是操作系统用户**。
- 多账号 alias 是便利功能，不是租户隔离；一个 Colony 的 Worker 可以显式指定别的 alias（除非工具层限制）。
- Playbook 内可以 `import os` 读取环境变量；不要把 secret 放在 Playbook 可访问的环境里。
- 浏览器自动化使用用户已登录的账号，绕过了“凭据不出库”的保护；这是能力，也是最大的外泄面。

## 5. 成本与限额

Hive 记录每次调用的 token 与 `cost_usd`，并在 `llm_turn_complete` / `context_usage_updated` 事件中暴露。但：

- **没有全局美元预算硬停**；
- 有工具调用预算、上下文预算、并发上限；
- `pipeline/stages/cost_guard.py` 提供“每请求最大成本”的阶段，但主要服务传统 pipeline，不是 Queen 默认路径；
- 真正的成本控制靠：模型选择、上下文压缩、工具结果落盘、Worker 预算、Playbook 并发/车道。

如果要把 Hive 用于计费敏感场景，建议在网关/代理层加预算，而不是只依赖框架。

## 6. 评价

**优点**

- Provider 抽象干净；LiteLLM + 原生 Anthropic + 兼容端点覆盖了绝大多数部署。
- 模型槽位细（主/Worker/Queen/视觉/Judge/记忆/Soul），能按成本与能力分别选型。
- 凭据“Agent 永不接触 secret”的设计正确；`hive-cred` 现取现用是很好的工程习惯。
- Key 池、容量重试、上下文错误识别，都是生产向的细节。

**风险**

- 精选目录与 LiteLLM 全量能力之间有差距；新模型需要目录更新才能获得正确窗口/视觉/定价。
- 订阅接入的合规风险由用户承担；产品已明示，但部署者容易忽略。
- 凭据安全的上限是宿主安全；Shell/Playbook/浏览器三条路都能绕过“加密存储”的抽象。
- 没有美元级硬预算，成本治理需要外部兜底。
