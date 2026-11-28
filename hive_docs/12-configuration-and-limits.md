# 12 · 配置、环境变量与限制

> 配置的唯一权威是 `~/.hive/configuration.json`（`HIVE_HOME` 可移动）加环境变量。
> 本章把散落在代码中的键、默认值与限制汇总成可查表。

## 1. 配置加载顺序

```text
HIVE_HOME（环境变量）→ ~/.hive 或桌面指定目录
└── configuration.json
     ├── 显式配置值
     ├── 缺失时：dataclass / dict 默认
     └── 部分键：环境变量覆盖（写在对应 getter 的 docstring 里）
```

`HIVE_CONFIG_FILE` 可指向另一个配置文件。

## 2. `configuration.json` 顶层键

| 键 | 作用 | 主要字段 |
|---|---|---|
| `llm` | 主模型 | `provider`、`model`、`max_tokens`、`max_context_tokens`、`api_key`、`api_key_env_var`、`api_base`、`endpoint`、`extra_body`、`api_keys`、`aux_max_tokens`、`use_*_subscription` |
| `worker_llm` | Worker 模型 | 同 `llm`，外加 `num_ctx`（Ollama） |
| `queen_llm` | 单 Queen 模型 | `{queen_id: <llm 形状>}` |
| `vision_fallback` | 视觉回退模型 | 模型槽位 |
| `judge` | yes/no 判定器 | `protocol`（目前 `typesafe`）、`api_base`、`model`、`api_key`、`threshold` |
| `model_endpoints` | OpenAI 兼容端点 | `{name: {api_base}}`；Magpie 内置 |
| `provider_library` | 保存的 Provider 组合 | UI 管理 |
| `memory_reflection` | 反思节奏 | `every_turns=6`、`long_every=4`、`model="worker"` |
| `memory_recall` | 记忆到达方式 | `mode="index"`、`wait_seconds=10` |
| `memory_timeline` | 时间线开关 | `false` |
| `soul` | Soul 配置 | `queens.<id>.{enabled,mode,hour,max_edits,sections,...}` |
| `queen_mail` | Queen 通信 | `enabled`、`queens`、`daily_limit=100`、`max_depth=8`、`max_parallel_turns=2`、`claude_code` |
| `channels` | 聊天应用 | `{name: {enabled, ...}}` |
| `turn_context` | 每轮上下文插件 | `{name: {enabled, ...}}` |
| `sentinel` | 全局 Sentinel | `classify_after_seconds=300`、`max_nudges_before_escalate=5`、`escalate_when_ui_attached=false` |
| `stall_check` | 停留检查开关 | `false` |
| `adaptive_tool_budget` | 自适应 Worker 预算 | `true` |
| `email_senders` | 团队邮件发件人 | `false` |
| `queen_tools` | Queen 工具类别覆盖 | `always_enabled_categories` |
| `worker_tools` | Worker eager 覆盖 | `always_enabled_categories` |
| `loop` | 循环级覆盖 | `max_tool_result_chars` |
| `external_skills` | 外部技能目录 | 路径列表 |
| `rate_limits` | 社媒限速 | `{platform.action.daily: N}` 等 |
| `retention` | Janitor 保留策略 | 见 13 章 |
| `user_profile` | 用户资料 | 姓名/头像等 |
| `gcu_enabled` / `gcu_viewport_scale` | 浏览器桥 | 开关/缩放 |
| `pipeline` | 传统 pipeline 阶段 | `{stages: [...]}`（非 Queen 主路径） |

## 3. 关键默认值

### 3.1 循环与预算

| 参数 | Queen | Colony Queen | Worker | 来源 |
|---|---:|---:|---:|---|
| `max_iterations` | 999,999 | 999,999 | 3 | `agents/queen/agent.py`、`worker_definition.py` |
| `grace_iterations` | 0 | 0 | 1 | 同上 |
| `tool_call_budget` | 30 | 50 | 30 | 同上 |
| `tool_call_hard_multiple` | 5 | 10 | 3 | 同上 |
| 硬停 | 150 | 500 | 90 | 计算 |
| `tool_call_lifetime_budget` | 0（不限） | 0 | 200 | 同上 |
| `max_context_tokens` | 180,000 | 180,000 | 180,000 | 可被模型目录/配置覆盖 |
| `max_tool_result_chars` | 30,000 | 30,000 | 30,000 | `LoopConfig` / `loop` |
| `max_iterations` 上限（覆盖） | — | — | 1,000 | `_LOOP_OVERRIDE_BOUNDS` |
| `grace_iterations` 上限 | — | — | 3 | 同上 |
| `tool_call_budget` 上限 | — | — | 200 | 同上 |
| `tool_call_lifetime_budget` 上限 | — | — | 2,000 | 同上 |

### 3.2 上下文与压缩

| 参数 | 默认 | 说明 |
|---|---:|---|
| `compaction_buffer_tokens` | 8,000 | 固定预留 |
| `compaction_buffer_ratio` | 0.4 | 比例预留 |
| `compaction_warning_buffer_tokens` | 12,000 | 提前警告 |
| `max_tool_result_chars` | 30,000 | 溢写阈值 |
| `max_output_value_chars` | 2,000 | `set_output` 溢写 |
| `max_retained_screenshots` | 2 | 历史图片保留 |
| 微压缩保留最近结果 | 6 | `HIVE_MICROCOMPACT_KEEP_RECENT` |
| 连续压缩失败熔断 | 3 | 停止自动压缩 |
| LLM 摘要最大输出 | 8,192 tokens | 防止“总结比原文还长” |
| 超长用户消息阈值 | 100,000 字符 | 截头尾 |

### 3.3 流与时间

| 参数 | 默认 |
|---|---:|
| 流首事件（TTFT） | 600s |
| 流事件间静默 | 120s |
| 有界流重试 | 5 次，退避 2s 起，单次 ≤60s |
| 容量错误持续重试 | 600s，单次 ≤60s |
| 继续提示/回合 | 3 次 |
| 会话 idle nudge | 120s |
| 失败停泊 nudge | 30s |
| 每会话 idle nudge 上限 | 3 |
| 单工具超时 | 60s |
| `browser_` 工具超时 | 180s |
| MCP 客户端结果上限 | 240s |
| 后台工具超时 | 235s |
| 等待预算 | 600s |
| 子 Agent 墙钟超时 | 3600s |
| 子 Agent 不活动超时 | 300s |

### 3.4 Colony 与并发

| 参数 | 默认 | 环境变量 |
|---|---:|---|
| Worker 并发 | 4 | `HIVE_MAX_CONCURRENT_WORKERS` |
| 自适应预算 | 开启 | `HIVE_ADAPTIVE_TOOL_BUDGET=0` 关闭 |
| 最小成功样本 | 3 | `HIVE_ADAPTIVE_BUDGET_MIN_SAMPLES` |
| 预算下限 | 30 | `HIVE_ADAPTIVE_BUDGET_FLOOR` |
| 预算倍率 | 2.0 | 代码常量 |
| 预算 max-guard | 1.25 | 代码常量 |
| 离散度阈值 | 4.0 | 代码常量 |
| 样本窗口 | 50 | 代码常量 |
| Worker 软超时 | 600s | `run_worker.timeout` |
| Worker 硬超时 | `max(4×soft, soft+600)`，≤3600s | 代码 |
| Worker 停止宽限 | 10s | `STOP_TIMEOUT_SEC` |
| Playbook 并发 | 8，最大 32 | `HIVE_DEFAULT_PLAYBOOK_CONCURRENCY` / `HIVE_MAX_PLAYBOOK_CONCURRENCY` |
| Playbook 快速返回窗口 | 1.5s | `HIVE_PLAYBOOK_FAST_GRACE` |
| 工具执行线程池 | max(8, 64) | `HIVE_TOOL_EXECUTOR_WORKERS` |
| HTTP 请求线程池 | 32 | `HIVE_REQUEST_EXECUTOR_MAX` |

### 3.5 记忆与技能

| 参数 | 默认 |
|---|---:|
| 记忆文件数/作用域 | 200 |
| 单记忆文件 | 4 KB |
| 反思间隔 | 6 回合 |
| 长反思间隔 | 4 次反思 |
| 召回模式 | `index` |
| `wait` 最长等待 | 10s |
| 会话召回条数 | 6 |
| 会话召回查询词 | 12 |
| 召回摘录长度 | 用户/助手各 500 字符 |
| 召回超时 | 3s |
| 时间线批大小 | 12,000 字符 |
| 时间线触发 | 3 条消息或 300s |
| 技能发现最大目录 | 2,000 |
| Soul 默认 sections | 5 个，300–800 字符/个 |
| Soul 每日最大改动 | 5 |
| Soul 首稿最大改动 | 30 |

### 3.6 Sentinel

| 参数 | 默认 |
|---|---:|
| 分类等待 | 300s |
| 升级前最大 nudge | 5 |
| UI 在线也升级 | false |
| 分类器失败回退 | `continue`（标记 errored） |
| 升级 token 长度 | 16（base32） |
| HMAC secret | `$HIVE_HOME/sentinel_secret`，0600 |

## 4. 环境变量

### 4.1 基础与路径

| 变量 | 作用 |
|---|---|
| `HIVE_HOME` | 根目录（默认 `~/.hive`） |
| `HIVE_CONFIG_FILE` | 配置文件路径 |
| `HIVE_DESKTOP_MODE` | 桌面模式 |
| `HIVE_DESKTOP_PARENT_PID` | 桥/看门狗监视的父 PID |
| `HIVE_DESKTOP_TOKEN` | 桌面共享密钥（HTTP 认证） |
| `HIVE_APP_VERSION` | 版本（工具迁移比较用） |
| `HIVE_RUNTIME_ID` / `HIVE_RUNTIME_KIND` / `HIVE_RUNTIME_PORT` | 运行时标识 |
| `HIVE_CREDENTIAL_KEY` | 凭据加密 key |
| `HIVE_API_KEY` / `HIVE_API_BASE` / `HIVE_LLM_ENDPOINT` | Hive LLM |
| `HIVE_CLOUD_BASE` / `HIVE_CLOUD_JWT` | 云登录（Inbox/global DB/streamToken） |

### 4.2 模型与网络

`ANTHROPIC_API_KEY`、`OPENAI_API_KEY`、`OPENROUTER_API_KEY`、`GEMINI/GOOGLE_API_KEY`、`GROQ_API_KEY`、`CEREBRAS_API_KEY`、`DEEPSEEK_API_KEY`、`MISTRAL_API_KEY`、`TOGETHER_API_KEY`、`XAI_API_KEY`、`PERPLEXITY_API_KEY`、`KIMI_API_KEY`、`MINIMAX_API_KEY`、`ZAI_API_KEY` 等。

### 4.3 运行时开关（常见）

| 变量 | 作用 |
|---|---|
| `MOCK_MODE=1` | 不走真实 LLM |
| `HIVE_MAX_ITER` | 覆盖 Queen 无限回合 |
| `HIVE_MAX_CONCURRENT_WORKERS` | Worker 并发 |
| `HIVE_ADAPTIVE_TOOL_BUDGET` | 自适应预算 |
| `HIVE_STALL_CHECK` | 停留检查 |
| `HIVE_MEMORY_RECALL` | 召回模式覆盖 |
| `HIVE_MEMORY_TIMELINE` | 时间线开关 |
| `HIVE_EMAIL_SENDERS` | 邮件发件人工具 |
| `HIVE_TRUST_PROJECT_SKILLS` | 跳过项目技能信任门禁（CI） |
| `HIVE_OWN_REMOTES` | 自有 remote glob（技能信任） |
| `HIVE_REGISTRY_URL` | 技能注册表 URL |
| `HIVE_PAST_RECALL_TIMEOUT` | 会话召回超时 |
| `HIVE_MICROCOMPACT_KEEP_RECENT` | 微压缩保留数 |
| `HIVE_MAX_VERBATIM_CLIENT_MSGS` | 群聊逐字保留上限 |
| `HIVE_TASK_REMINDER_*` | 任务提醒节奏 |
| `HIVE_TRACKER_QUEEN_*` | Tracker 快照节奏 |
| `HIVE_COLONY_PARALLEL_*` | 并行 nudge 节奏 |
| `HIVE_ACTIVE_WORKERS_*` | 在飞 Worker 快照上限 |
| `HIVE_SENTINEL_LOG` | Sentinel 日志文件 |
| `HIVE_DEBUG_EVENTS` | 全量事件 JSONL |
| `HIVE_ENABLE_LLM_LOGS` | LLM debug 日志 |
| `HIVE_COMPACTION_DEBUG` | 压缩调试 |
| `HIVE_NO_FILE_LOG` | 禁用文件日志 |
| `HIVE_REQUEST_EXECUTOR_MAX` | HTTP 线程池 |
| `HIVE_TOOL_EXECUTOR_WORKERS` | 工具线程池 |
| `HIVE_MCP_CALL_RESULT_TIMEOUT` | MCP 结果超时 |
| `HIVE_PLAYBOOK_FAST_GRACE` / `HIVE_DEFAULT_PLAYBOOK_CONCURRENCY` / `HIVE_MAX_PLAYBOOK_CONCURRENCY` | Playbook |
| `HIVE_BRIDGE_PORT` / `HIVE_BROWSER_SESSION` / `HIVE_BROWSER_PROFILE_DISPLAY_NAME` | 浏览器桥 |
| `HIVE_GCU_*` | 浏览器/GCU 调试与并发 |
| `HIVE_VP_PROBE_JS` | 视口探针 |
| `HIVE_RATE_<PLATFORM>_<ACTION>_DAILY` | 社媒限速 |
| `HIVE_RETENTION_<FIELD>` | Janitor 保留策略 |
| `HIVE_TASK_*` | 任务清理/提醒/快照 |
| `HIVE_TIMELINE_*` | 时间线批/冷却 |
| `HIVE_HISTORY_READ_WORKERS` | 历史读取并发 |

完整清单可用：

```bash
grep -rhoE 'HIVE_[A-Z0-9_]+' core/framework core/hive tools/src --include='*.py' | sort -u
```

## 5. Feature Flags

`GET/PUT /api/config/features`，代码中 4 个：

| Flag | 默认 | 作用 |
|---|---|---|
| `adaptive_tool_budget` | true | Colony 自适应 Worker 预算；可热应用到已加载 Colony |
| `email_senders` | false | 团队邮件发件人工具 |
| `memory_timeline` | false | 日期时间线 + `search_timeline` |
| `stall_check` | false | 停留检查（需要 `judge` 槽位） |

另有非 feature-flag 的开关：`gcu_enabled`、`queen_mail.enabled`、`soul.queens.<id>.enabled`、`channels.<name>.enabled`、`turn_context.<name>.enabled`、`sentinel`（按 Colony）。

## 6. 限制与配额总表

| 维度 | 限制 | 是否可调 |
|---|---|---|
| 单回合工具调用 | Queen 150 / Colony 500 / Worker 90 硬停 | 部分（Worker 软预算/生命周期） |
| 生命周期工具调用 | Worker 200 | Queen 可覆盖 0–2000 |
| 回合数 | Worker 3+1 | Queen 可覆盖 1–1000 |
| 内容大小 | 工具结果 30k；输出值 2k；用户消息 100k | 配置/覆盖 |
| 历史图片 | 2 张 | 代码 |
| 并发 Worker | 4 | 环境变量 |
| Playbook 并发 | 8 / 32 | 环境变量 |
| 单 Playbook 运行 | 无墙钟上限；由脚本/Worker 超时约束 | 脚本 |
| Tracker | 单次 SQL ≤20 条语句；变更日志 10k | 代码 |
| 记忆 | 200 文件 × 4KB/作用域 | 代码 |
| 技能扫描 | 2,000 目录 | 代码 |
| Queen Mail | 100 封/天，深度 8 | 配置 |
| Sentinel | 5 次 nudge 后升级 | 配置 |
| 触发器 | 每会话可配多个；只在加载时运行 | 配置 |
| 会话报告包 | 单文件 256MiB，总 1GiB | 代码 |
| Colony 导入 | 分块上传 | 代码 |
| 日志 | events 大字段剥除；保留策略见 13 章 | 配置 |

## 7. 配置的常见坑

1. **`max_context_tokens` 不等于模型窗口。** 配置未写时从目录读；目录不认识的模型回退 32k/180k。选新模型要确认目录条目。
2. **`tool_call_budget=0` 表示禁用**软/硬预算，不是“零次调用”。
3. **`grace_iterations=0` 完全关闭宽限**；Worker 耗尽预算会没有收尾回合。
4. **`HIVE_MAX_ITER` 会影响 Queen 的“永远在线”语义**；单次聊天部署可设 3，但长任务会被截断。
5. **`memory_recall.mode` 改动对运行中会话从下一条消息生效**，不需要重启。
6. **`stall_check` 需要 `judge` 槽位**；只开开关没有 judge 等于没开。
7. **Sentinel 只在 Colony opt-in**；全局 `sentinel` 只是默认参数，不是总开关。
8. **`rate_limits` 只约束调用该限速器的代码**（浏览器脚本/技能），不是所有工具。
9. **`HIVE_CREDENTIAL_KEY` 丢失会使已有 `.enc` 不可读**；服务端会自愈删除不可读文件，但那些凭据就没了。
10. **`HIVE_DESKTOP_TOKEN` 只保护 HTTP**；不保护本地文件、终端工具、浏览器桥。
