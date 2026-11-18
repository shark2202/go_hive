# 11 · 功能需求清单

> 状态快照：2026-11-18（P0 可运行切片集成后；`make ci` 退出码 0，parity 2,238/2,238、2,045 refs、0 missing）。
> 代码在 `go_hive/go-hive/`，Python 参考在 `core/`。
> 测试总数为 P0 集成后门禁实测 **2,238**（各包测试列已更新，新增 e2e 行）；文件/行数为 P0 前实测（223 个非测试 `.go` 文件 / 71,036 行、168 个测试文件 / 56,004 行），未按 P0 后重算。
> 代码风格：除 `// Parity:` / `// Parity-exempt:` 门禁注解外不含任何注释。

## 1. 状态口径

| 状态 | 判定标准 |
| --- | --- |
| **已完成** | Go 包有实现文件，且存在对应测试；核心公开行为已落地 |
| **进行中** | 有实现文件但能力不完整，或只有接口/骨架、无测试 |
| **待开始** | 目录存在但没有 `.go` 文件，或仅规划 |
| **豁免** | 上游没有对应物，Go 侧额外提供的工程设施 |

> 「已完成」指该子系统的**核心行为**已实现并有测试，不等于 100% 覆盖上游。具体缺口以测试账本（第 6 节）与 `15-test-parity-matrix.md` 为准。

## 2. 总览

| 子系统 | Go 包 | 源文件 / 行数 | 测试函数 | 已注解 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 原子文件 | `internal/atomicfs` | 1 / 116 | 4 | 4 | 已完成 |
| Hive 主目录 | `internal/hivehome` | 1 / 140 | 3 | 3 | 已完成 |
| 会话存储 | `internal/session` | 3 / 986 | 17 | 17 | 已完成 |
| 持久化层 | `internal/storage` | 8 / 2,050 | 55 | 55 | 已完成（会话/对话/检查点/v2+v3 迁移） |
| 凭据 | `internal/credentials` | 14 / 3,597 | 48 | 48 | 已完成 |
| 技能 | `internal/skills` | 18 / 3,839 | 219 | 219 | 已完成 |
| 记忆 | `internal/memory` | 4 / 2,213 | 46 | 46 | 已完成 |
| Tracker | `internal/tracker` | 4 / 2,078 | 35 | 35 | 已完成 |
| 模型结构 | `internal/schemas` | 7 / 2,578 | 10 | 10 | 已完成（决策/目标/运行/检查点等 schema） |
| LLM 抽象 | `internal/llm` | 12 / 3,437 | 222 | 222 | 已完成（能力目录/端点/密钥池/流事件 + provider 纯逻辑层：重试退避/缓存断点/成本核算/JSON 修复 + 请求塑形：parallel_tool_calls 锁存、OpenRouter tool-compat 消息、extra kwargs + 非流式 `complete`：请求组装、429/空响应重试、响应归一化；P0 新增 `transport.go` 真实 HTTP 传输与 `AComplete`（`transport_test.go` 7 条）） |
| 版本信息 | `internal/version` | 1 / 5 | 0 | 0 | 已完成（常量） |
| 全局网格库 | `internal/globaldb` | 3 / 772 | 23 | 23 | 已完成（HTTP 客户端/查询/计数缓存） |
| AgentLoop | `internal/agentloop` | 19 / 6,485 | 318 | 318 | 已完成（核心循环/压缩/停顿/恢复/提醒 hub 时钟/idle nudge/stream stall 提醒源/每工具超时预算与最长前缀解析/预算上限收缩与运行时遥测读取） |
| 代理发现 | `internal/agents` | 6 / 781 | 34 | 34 | 已完成（colony 发现分组/元数据/worker 配置解析/软删除跳过、会话与运行计数、`last_active` 时间戳、Python `str.title()` 与 `pathlib` suffix 语义、允许根校验与符号链接/NUL/穿越拒绝） |
| Colony | `internal/colony` | 11 / 2,625 | 84 | 84 | 进行中（运行时组件已落地，编排未闭环；worker loop-config 覆盖白名单与自适应 worker 运行预算已落地；worker `started_at` 墙钟字段已对等） |
| 编排器 | `internal/orchestrator` | 3 / 3,410 | 40 | 40 | 已完成（数据缓冲 + 输出校验） |
| Playbook | `internal/playbook` | 2 / 1,100 | 32 | 32 | 已完成（行提取/运行助手） |
| 社交限流 | `internal/ratelimit` | 1 / 582 | 21 | 21 | 已完成（SQLite 计数 + env/配置/默认三层限额解析 + 停用判定） |
| 触发器 | `internal/triggers` | 3 / 653 | 17 | 17 | 已完成 |
| 事件总线 | `internal/eventbus` | 9 / 2,249 | 113 | 113 | 已完成 |
| 任务/提醒 | `internal/tasks` | 13 / 3,310 | 128 | 128 | 已完成（存储/作用域/提醒/工具/路由） |
| MCP 注册表 | `internal/mcpregistry` | 8 / 3,934 | 160 | 160 | 已完成（安装/解析/健康检查/版本/CLI 模式/mcp2cli 桥） |
| 工具 | `internal/tools`、`internal/tools/mcp` | 14 / 5,986 | 148 | 148 | 进行中（注册表/执行器（专属池、上下文传播、超时探测策略）/参数转换/分层 keep-set 不变量/MCP 注册与批加载/MCP 客户端传输 stdio·http·unix·sse/MCP 连接池（按服务器名复用、refcount、并发 latch、健康检查、重连、清理）；`internal/tools/builtin` 提供 P0 的 `read_file`/`write_file`/`terminal_exec`（cwd 沙箱、越界与符号链接拒绝、原子写、进程组超时）；104 个集成工具与浏览器桥未开始，`internal/tools/mcp` 目录仍为空） |
| 附件工具 | `internal/attachments` | 1 / 157 | 5 | 5 | 已完成（文件名净化与去重后缀、二进制/UTF-8 文本探测、MIME 允许清单） |
| 保留策略 | `internal/maintenance` | 8 / 2,432 | 42 | 42 | 已完成（三档 janitor + `hive janitor run/status` CLI 入口） |
| 运行时健康 | `internal/runtimehealth` | 1 / 124 | 3 | 3 | 已完成（网络错误标记分类、上游健康生命周期、降级原因 240 字符截断） |
| 服务端 | `internal/server` | 35 / 9,782 | 281 | 281 | 进行中（代理路由组：`GET /api/discover` 发现分组与 `is_loaded` 标记、`DELETE /api/agents` 软删除/purge 与框架代理 403、`PATCH /api/agents/metadata` 的 icon 写回（`ensure_ascii=False`）、`POST /api/credentials/check-agent` 的允许根校验；会话创建：`POST /api/sessions` 普通与 fork-into-new-colony 分支；`GET /api/sessions/{id}/live_tools` 活会话工具清单与冷会话投影；会话读写路由组 W1：`GET /api/sessions/history` 冷会话磁盘列举 + live overlay、`GET /api/colonies/{id}/sessions` 与 `/active-session`、`DELETE /api/sessions/history/{id}`（含符号链接拒绝）、`PATCH /api/sessions/{id}/title`（DM 限定、空串清除、写失败通用 500）、`POST /api/sessions/{id}/reveal`（跨平台文件管理器打开）；触发器生命周期六路由：`POST /api/sessions/{id}/triggers` 创建（name/task 必填、仅 timer、cron/interval 校验、slug 归一与冲突后缀、计时器启动失败回滚、`TRIGGER_ACTIVATED`）、`PATCH .../{trigger_id}`（任务/配置合并、活动触发器重挂、`TRIGGER_UPDATED`）、`POST .../{trigger_id}/run`（colony/queen 门禁、`forced: true` 注入、火统计）、`POST .../activate`/`deactivate`（timer/webhook 启停、`already_*` 短路、`config_out` 合并 next-fire 与火统计）、`POST .../colony/resolve_missed`（`invalid_decision:` 标记）；运行时 `trigger_runtime.go` 覆盖 slugify、next-due、timer 播种与循环、webhook 按 stream 订阅、`PersistActiveTriggers`；内嵌 `webhook_server.go` 提供精确路径、HMAC-SHA256、JSON-or-raw 与 `WEBHOOK_RECEIVED` 发布；会话详情四路由：`GET /api/sessions/{id}` 的 live/loading(202)/cold/404 分支与 `entry_points` 顶层 next-fire 字段、`DELETE /api/sessions/{id}`、`GET /api/sessions/{id}/stats`、`GET /api/sessions/{id}/triggers`、`SessionManager.is_loading` 与 `get_cold_session_info` 磁盘投影、历史 worker 读端点、worker 列表预算遥测序列化、queen 升级路由、fork-compaction 状态标记、session 自动标题、global-db 数据路由决策层：lead_id 派生 / insert 计划 / changes 游标 / 错误信封、会话目录解析：DM 优先遍历 / colony 归属推导 / resume 绑定、判官槽与停滞检查、配置特性开关 / LLM 槽位 / provider 库 / 元信息路由 / `GET/PUT /api/config/llm` 决策层及其 HTTP 路由接线：`RegisterConfigRoutes` 的 20 个挂载点、严格 JSON 解析、配置数值拼写透传、`valid is False` 校验、尾随垃圾读空、订阅陈旧键清理、`GET /api/sessions` 实时会话列表：`_session_to_live_dict` 15 字段对等 / 插入序 SessionManager、`GET /api/sessions/{id}/events/history` 游标分页：1MB 反向 tail 阈值 / 64KB 字段截断 / 20s 读超时 / ASCII-only 空行过滤 / Python `int()` 查询参数 / `json.dumps` 数字尺寸估计 / `GET /api/sessions/{id}/snapshot` 投影或 404 / `GET /api/sessions/live/stream` 首帧快照 / 附件上传与读取（文件名净化、文本探测、MIME 允许清单、colony 根解析）、`POST /api/sessions/{id}/report-bundle` 凭据脱敏诊断包（`queen_resume_from` 存储 ID 优先、404 分支、marker 与统计信封、tar.gz 落盘临时目录；P0 会话切片：`POST /api/sessions/{id}/chat`、`GET /api/sessions/{id}/events`（会话事件 SSE）与 `ChatRuntime`（agent loop + transcript 持久化）） |
| 会话报告 | `internal/sessionreport` | 1 / 346 | 8 | 8 | 已完成（八类凭据脱敏正则、`user_report.json` 标记、tar.gz 打包与 `data/reports` 排除、SHA-256、manifest 非 ASCII 转义） |
| Queen / Sentinel | `internal/queen`、`internal/sentinel` | 1 / 381 | 10 | 10 | 进行中（13 个默认 queen 身份档案 + catalog/目录列表辅助 + `profiles_test.go` 10 条对等测试已落地；sentinel 与 queen agent 节点待开始） |
| 平台抽象 | `internal/platform`、`internal/platform/term` | 1 / 24 | 0 | 0 | 进行中（`OpenPath` 跨平台启动器：darwin `open` / windows `explorer` / 其他 `xdg-open`，Popen-无 wait 语义；`term` 待开始） |
| CLI | `cmd/hive`、`cmd/hive-cred`、`internal/cli`、`internal/llmjudge` | 10 / 4,864 | 110 | 110 | 已完成（根入口 `serve`/`open`/`queen`/`colony`/`session`/`chat` + 12 个 `skill` 子命令 + `debugger` 1 个子命令 + `mcp` 14 个子命令 + `janitor` 2 个子命令 + LLMJudge；独立入口 `hive-cred token/browse/inspect`；P0 起 `serve`/`chat` 接真实 provider、内建工具与持久化 transcript） |

合计：P0 集成后门禁实测 **2,238 个 `Test` 函数（2,238/2,238 已注解，0 missing；`check_parity_targets` 2,045 refs、55 shorthand/prose）**；文件/行数沿用 P0 前实测 223 个非测试 `.go` 文件、71,036 行、168 个测试文件、56,004 行，单行明细计数未按 P0 后重算。

### 2.1 P0 可运行切片（2026-11-18，门禁 PASS）

P0 是**可运行子集**，不是 Python 全量对等。以下能力已在当前代码中接通（生产路径：`internal/cli/loader.go` 的 `defaultServe` → `createChatSession` → `resolveChatProvider`；末行 E2E 为验证入口）：

| 能力 | 已接通的契约 |
| --- | --- |
| `hive serve` + 健康检查 | 默认 `127.0.0.1:8787`；`--host/--port/--colony/--model/--open/--verbose/--debug`；`GET /api/health` |
| `POST /api/sessions` | 创建真实会话，201 + 会话投影；请求键含 `model`、`initial_prompt`、`colony_id`、`session_id`、`queen_name`、`queen_resume_from`、`initial_phase` |
| `POST /api/sessions/{id}/chat` | `{"message":"..."}` → 200 `{"status":"queen","delivered":true}`；空消息 400、未知会话 404、已 spawn colony 409 `session_locked`、无 LLM 503 `No LLM configured for this session` |
| `GET /api/sessions/{id}/events` | 会话事件 SSE：`text/event-stream`、20 秒心跳、`?types=` 过滤；只推送会话总线事件（如 `CLIENT_INPUT_RECEIVED`），**不含**逐 token 模型流 |
| 真实 HTTP LLM | `llm.NewHTTPProvider` + `(*Provider).AComplete`：OpenAI-compatible `POST {api_base}/chat/completions`（基址缺 `/v1` 时自动补，默认 `https://api.openai.com/v1`，Bearer）、Anthropic Messages `POST {api_base}/v1/messages`（默认 `https://api.anthropic.com`，`x-api-key` + `anthropic-version: 2023-06-01`）；协议按模型族选择 |
| 内建工具 | `internal/tools/builtin` 的 `read_file`/`write_file`/`terminal_exec`；沙箱根 = 服务进程 cwd，拒绝越界与符号链接逃逸；`terminal_exec` 默认 60 秒、上限 600 秒、每路 256 KiB |
| transcript 持久化 | `<HIVE_HOME>/queens/default/sessions/<id>/conversations/parts/*.json`（`agentloop.FileConversationStore`）；同 ID 会话重建时恢复 |
| CLI | `hive chat <session_id> <message> [--server URL]`；`hive session list/stop` |
| E2E | `go test -tags e2e ./e2e/...`：create → chat → 持久化 transcript → 再 chat；`httptest` + 脚本化 `llm.Completer`，不访问网络 |

**凭据前置**（模型优先级：请求 `model` > `serve --model` > `configuration.json` 的 `llm.model` > `gpt-4o-mini`；`llm.api_key_env_var` 可覆盖 key 变量名，`HIVE_API_KEY` 兜底）：Anthropic/Claude 协议（`anthropic/*`、`claude-*`、`kimi/*`、`hive/*`）需 `ANTHROPIC_API_KEY`，可选 `ANTHROPIC_API_BASE`/`ANTHROPIC_BASE_URL`；OpenAI-compatible 需 `OPENAI_API_KEY`，可选 `OPENAI_BASE_URL`/`OPENAI_API_BASE`；本地模型（`ollama/*`、`vllm/*`、`lm_studio/*`、`llamacpp/*`）无需 key。缺 key 时会话仍可创建/列出，chat 返回 503，不伪造成功。

**明确延期**：216 条 Python 路由的完整对等、104 个集成工具、Sentinel、browser、React dashboard（仅当存在 `frontend/dist` 时提供静态资源）、queen/colony 多 worker 编排闭环、逐 token 模型流。

**验证状态（PASS）**：`make ci` 退出码 0——`fmt-check`、`vet`、`test`、六平台 `cross`（darwin/linux/windows × amd64/arm64）与 `parity` 全部通过；parity **2,238/2,238 已注解、0 missing**，**2,045 refs、55 shorthand/prose**。`go test -tags e2e -count=1 ./e2e/...` 与 `-race` 版本均 ok；凭据门禁 `TestCredentialFlowEnvKeyReachesUpstream` 通过（真实二进制 + 假 OpenAI 上游收到 `Authorization: Bearer test-key`）；二进制冒烟 `dist/hive-darwin-arm64 --version` → `hive 1.0.14-go.1`，健康检查/启动就绪、创建会话 201、无 key 时 chat 503。

## 3. 已完成子系统需求明细

### 3.1 技能（`internal/skills`）

参考：`core/framework/skills/*`、`core/tests/test_skill_*.py`、`test_default_skills.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-SK-01 | 五级发现作用域，优先级 framework 0 / preset 1 / user 2 / queen_ui 3 / colony_ui 4 / project 5；同名冲突**后扫描者胜**并告警 | `discovery.go` 的 `ScopePriority`、`DiscoveryConfig` |
| R-SK-02 | 内置 default/preset 技能包用 `go:embed` 打进二进制，运行时物化到临时目录再解析 | `//go:embed all:_default_skills all:_preset_skills` |
| R-SK-03 | 扫描边界：最大深度 4、最多 2000 个目录、跳过 `.git`/`node_modules`/`__pycache__`/虚拟环境等 | `skipDirs`、`MaxDepth`、`MaxDirs` |
| R-SK-04 | 用户作用域包含 `~/.agents/skills`、`configuration.json` 的 `external_skills`、`HIVE_HOME/skills` | `DiscoveryConfig.ExternalSkills` |
| R-SK-05 | `SKILL.md` 解析：`---` 分隔 frontmatter、容忍未加引号的冒号、description 必填、name 缺省取目录名、长度上限 64 | `parser.go` |
| R-SK-06 | 生命周期 `Load` / `Reload`，目录或文件变更监听，catalog 提示词缓存 | `manager.go` |
| R-SK-07 | 项目级技能默认不信任；三态判定（始终可信 / 用户已信任 / 未信任），git `origin` 远程 3 秒超时检测 | `trust.go` |
| R-SK-08 | 信任持久化 `{version:1, entries:[{repo_key,added_at,project_path}]}`，远程 URL 归一化、localhost 特判、Python fnmatch 兼容 | `TrustedRepoStore`、`globMatch` |
| R-SK-09 | 交互式信任确认：1 本次会话 / 2 永久 / 3 拒绝；非交互模式输出安全提示 | `TrustGate.FilterAndGate` |
| R-SK-10 | 覆写存储 schema 1：`version`、`all_defaults_disabled`、`overrides{name:{enabled,provenance,trust,param_overrides,notes,...}}`、`deleted_ui_skills` | `overrides.go` |
| R-SK-11 | 覆写优先级：墓碑删除 > 显式启用/禁用 > preset 角色默认 > 全局默认开关 | `manager.go::ApplyManagerOverrides` |
| R-SK-12 | 目录变更后对全部 Colony 运行时与 Queen 会话热重载，单点 panic 不影响其它运行时 | `live_reload.go` |
| R-SK-13 | 按阶段生成 catalog 提示词；按已加载工具增补相关技能说明 | `catalog.go`、`tool_gating.go` |
| R-SK-14 | 技能写盘：校验名称/描述/文件、草稿生成、`--replace` 覆盖并清理陈旧文件、拒绝非法名称 | `skill_writer.go` |
| R-SK-15 | 安装请求存储的 JSON 形状与宽松读取 | `install_requests.go` |
| R-SK-16 | 技能注册中心客户端，环境变量回退配置 | `registry.go` |
| R-SK-17 | 默认技能集合与 preset 默认关闭策略（角色默认项除外） | `defaults.go`、`config.go` |
| R-SK-18 | 校验器：名称/描述/文件内容规则与稳定的错误文案 | `validator.go` |

### 3.2 记忆（`internal/memory`）

参考：`core/framework/agents/queen/queen_memory_v2.py`、`recall_selector.py`、`reflection_agent.py`、`timeline.py`、`core/tests/test_queen_memory.py`、`test_memory_timeline_recall.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-ME-01 | 记忆目录与分类：`profile` / `preference` / `environment` / `feedback` | `memory.go` `GlobalMemoryCategories` |
| R-ME-02 | 单文件上限 4 KB、最多 200 个文件、frontmatter 头部 30 行；宽松解析，损坏文件跳过 | `ParseFrontmatter`、`ScanMemoryFiles` |
| R-ME-03 | 文件名 slug 化与冲突分配；按新到旧排序 | `Slugify`、`Allocate` |
| R-ME-04 | 记忆文档与清单的稳定格式（供注入与提示词使用） | `BuildMemoryDocument`、`FormatMemoryManifest` |
| R-ME-05 | 召回选择：默认 5 条、profile 必选、LLM 输出 JSON 解析（含正则回退）、别名去重、数量上限 | `recall.go::SelectMemories` |
| R-ME-06 | 相对时间格式：超过 48 小时显示「N days old」 | `FormatRelativeAge` |
| R-ME-07 | 召回注入块 `--- label ---` 格式；全局/Queen 分域注入默认各 3 条 | `FormatRecallInjection`、`BuildScopedRecallBlocks` |
| R-ME-08 | 反思代理：作用域解析、安全路径、错误写入、记忆文件列举、提示词构造 | `reflection.go` |
| R-ME-09 | 时间线抽取：会话起始时间解析、只读客户端输入、内容寻址缓存键、容忍代码围栏的 JSON 解析 | `timeline.go` |

### 3.3 会话存储（`internal/session`）

参考：`core/framework/storage/session_store.py`、`conversation_store.py`、`core/tests/test_session_store.py`、`test_node_conversation.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-SE-01 | 会话 ID 规范 `session_YYYYMMDD_HHMMSS_{uuid8}`；拒绝空、`/`、`\`、`.`、`..` 与越界路径 | `GenerateID`、`ValidID`、`Dir` |
| R-SE-02 | `state.json` schema `1.1`，状态 active/paused/completed/failed/cancelled，字段名与 Python 完全一致 | `State` |
| R-SE-03 | 目录骨架：`conversations/parts`、`partials`、`artifacts`、`checkpoints`、`logs` | `Store.Create` |
| R-SE-04 | 对话 part 每消息一个 JSON、十位零填充序号、按文件名排序、损坏 part 跳过 | `AddPart`、`ReadParts` |
| R-SE-05 | `cursor.json` 持久化迭代/轮次/累积输出/停顿状态/待处理输入；缺失读作零值 | `Cursor` |
| R-SE-06 | 流式中间态 `partials/{id}.json` 写入/读取/删除，ID 做安全字符替换 | `WritePartial` 等 |
| R-SE-07 | 检查点：一文件一检查点 + `index.json` 索引，按创建顺序排序 | `CheckpointStore` |
| R-SE-08 | 列表按 `created_at` 倒序，支持状态过滤与 limit | `Store.List` |

### 3.4 凭据（`internal/credentials`）

参考：`core/framework/credentials/*`、`core/tests/test_credential_bootstrap.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-CR-01 | 纯标准库 Fernet：AES-CBC + HMAC-SHA256 + PKCS7，兼容 Python 生成的 token | `fernet.go` |
| R-CR-02 | 加密文件存储：`{base}/credentials/{id}.enc`，原子写、`0600` | `storage.go` |
| R-CR-03 | 明文索引 `metadata/index.json` 版本 `2.0`：类型、provider、alias、identity、key_names、时间戳、自动刷新、标签 | `IndexEntry`、`IndexEnvelope` |
| R-CR-04 | 索引损坏按空处理并重建；不可解密文件跳过；版本不匹配触发重建 | `rebuildIndex` |
| R-CR-05 | 环境变量存储只读；支持 `KEY=VALUE`、`export`、注释、单双引号；缺省变量名 `{ID}_API_KEY` | `EnvVarStorage` |
| R-CR-06 | 内存存储与组合存储（写主、读主后回退、列表并集、存在性全查） | `InMemoryStorage`、`CompositeStorage` |
| R-CR-07 | 凭据仓库：默认缓存 300 秒、可配 TTL、可关缓存、自动刷新、刷新失败策略 | `store.go` |
| R-CR-08 | Provider 优先级与账号/别名查询（用接口断言替代 Python 的 `hasattr`） | `ProviderAccountStorage`、`AliasStorage` |
| R-CR-09 | 取值顺序：`value` > `api_key` > `access_token` > 首个 key | `CredentialStore.Get` |
| R-CR-10 | 凭据模型：类型（api_key/oauth2/bearer）、插入有序的 `KeyMap`、身份标记、时间戳 | `models.go` |
| R-CR-11 | OAuth2：token 过期判断、配置校验、错误语义 | `oauth2.go` |
| R-CR-12 | 启动引导：从 `ADEN_API_KEY` 等环境建立默认凭据 | `bootstrap.go` |
| R-CR-13 | Shell 配置检测与增删环境变量（bash/zsh/fish/PowerShell 候选） | `shell.go` |
| R-CR-14 | 模板解析：缺失模式、变量替换 | `template.go` |

### 3.5 Tracker（`internal/tracker`）

参考：`core/framework/host/tracker_db.py`、`framework/tools/tracker_tools.py`、`core/tests/test_tracker_db.py`、`test_tracker_tools.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-TR-01 | 每 Colony 一个 `tracker/tracker.db`，schema 版本 3，WAL、`synchronous=NORMAL`、外键、`busy_timeout=5000` | `tracker.go` |
| R-TR-02 | 内建表 `_tracker_registry`、`_tracker_meta`、`_tracker_changes` 与 `_tracker_chg__*` 变更触发器 | `bootstrap` |
| R-TR-03 | SQL 拒绝清单：`ATTACH`/`DETACH`/`PRAGMA`/`VACUUM`/`REINDEX` 等前导关键字、`load_extension` 函数、破坏性写关键字、受保护 `_` 前缀表 | `validateSQL` |
| R-TR-04 | 单次最多 20 条语句；脚本/查询/执行三种返回形状；结果截断 | `ExecuteSQL` |
| R-TR-05 | 绑定解析优先级：显式 binding > `tracker_db_path` > `colony_id`；兼容旧 `<colony>/data/tracker.db` | `BindingFromTrackerDBPath` |
| R-TR-06 | 四个工具：`tracker_sql`（Queen 专属）、`tracker_register_writable`（Queen 专属）、`tracker_upsert`（Worker）、`tracker_query`（共享） | `tools.go` |
| R-TR-07 | 注册表 upsert/replace、写列与主键校验、未注册表写入不记变更 | `RegisterWritable`、`Upsert` |
| R-TR-08 | 批量迁移：为所有 Colony 建库或升级，回填触发器，修补 Worker 配置，解析符号链接 | `EnsureAllColonyTrackerDBs` |
| R-TR-09 | 变更日志上限 10,000 条，按时间保留最新 | `ChangeLogMax`、prune |

### 3.6 保留策略 / Janitor（`internal/maintenance`）

参考：`framework/maintenance/{retention,janitor,archive}.py`、`core/tests/test_janitor_{tier1,safety,queen_hygiene,worker_deepclean,review_fixes}.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-JN-01 | 三档保留：Tier 1 调试存储按年龄清理、Tier 2 已完成 Worker 深清理、Tier 3 冷 Queen 会话卫生 | `retention.go` |
| R-JN-02 | **默认 dry-run**：`RunOptions{Tiers:nil}` 选三档，空非 nil map 选零档；`Execute=false` 只记账并写候选 manifest，不动磁盘（含不写墓碑） | `janitor.go::RunOnce` |
| R-JN-03 | 门禁顺序：受保护会话 ID → live 会话目录 → fork 压缩中 → 新鲜 session lock（重检时忽略 janitor 自有锁）→ idle 门槛 `max(ageGate, grace)` → 未完成 partial；任一命中即跳过并记录原因 | `IsSafeToPrune` |
| R-JN-04 | 全局锁 TTL 2 小时、session 锁 TTL 3600 秒；disposer 先构造再取锁，构造失败不得泄漏全局锁 | `janitor.go` |
| R-JN-05 | 孤儿 spillover 扫描 **fail-closed**：语料为 parts/partials 原始 JSON + `cursor.json`，任一读取失败则整轮跳过该会话；`tool_calls.arguments` 中的路径同样计入引用 | `SpilloverReferenceCorpus`、`FindOrphanSpillovers` |
| R-JN-06 | 离线与在线互斥：破坏性档位写 `maintenance/server.lock`；离线发现 marker 指向其他存活 pid 时拒绝执行；Tier 1 不受限；`ClearServerMarker` 只删自己 pid 的 marker | `WriteServerMarker`、`LiveServerOwnsHiveHome` |
| R-JN-07 | 墓碑先行：Worker 深清理先写 `result.json`（status/summary/`_janitor`）再删 transcript；重跑只补删剩余文件 | `DeepCleanWorker` |
| R-JN-08 | 归档：每个目标一个 `*.tar.gz`，成员保留 `colonies/<c>/workers/<id>/...` 相对结构；archive 失败时源目录原样保留 | `archive.go` |
| R-JN-09 | 清理后 Queen 会话重写 `events.jsonl` 并重置消息索引，落 `.pruned.json` 标记 | `PruneQueenSession` |

### 3.7 MCP 注册表（`internal/mcpregistry`）

参考：`framework/loader/mcp_registry.py`、`core/tests/test_mcp_registry.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-MR-01 | 目录布局 `<base>/{installed.json,config.json,cache/,logs/,servers/}`；初始化幂等，损坏 JSON 报 `DecodeError` 而不静默覆盖 | `Initialize`、`readJSONFile` |
| R-MR-02 | 安装条目 17 个字段（source/transport/enabled/manifest/overrides/时间戳/健康状态/`manifest_version` 等），写入走原子替换、`0600` | `makeEntry`、`atomicfs.WriteJSON` |
| R-MR-03 | 本地安装四种 transport：stdio（command/args/env/cwd）、http（url/headers）、sse、unix（socket_path）；缺必填项给出可执行修复建议 | `AddLocal`、`AddLocalOptions` |
| R-MR-04 | 索引安装：校验版本 pin 与 `transport` 覆盖，安装时把目录 manifest 快照进条目以便离线解析 | `Install`、`registryManifestSnapshot` |
| R-MR-05 | 内置/预置服务器退役：`manage_bundled=false` 时按 argv 内容识别历史占位条目并移除，保留用户自有同名同命令条目 | `retireBundledServers` |
| R-MR-06 | 启停/移除/模式切换、`SetOverride`（env/headers）类型校验与「server not found / not supported」错误语义 | `Enable`、`Disable`、`Remove`、`SetMode`、`SetOverride` |
| R-MR-07 | 面向 agent 的解析：include / tags / exclude / profile / versions / `max_tools`，按名称排序后截断；禁用条目跳过，缺失名称只告警 | `ResolveForAgent` |
| R-MR-08 | harness 合成：`profile="all"` 预置全部 `tools.HarnessGroupNames`，显式 include 同名组时按 `transport: "harness"` 合成 | `ResolveForAgent`、`tools.HarnessGroups` |
| R-MR-09 | manifest → `ServerConfig` 映射：env/headers 合并（override 胜出）、cwd/description、unix transport；不支持 transport 返回 nil | `manifestToServerConfig`、`ServerConfig.ToDict` |
| R-MR-10 | `load_agent_selection`：从 `<agent>/mcp_registry.json` 读取 include/tags/exclude/profile/versions/max_tools，字段类型不符的项静默丢弃 | `LoadAgentSelection`、`expectedSelectionTypes` |
| R-MR-11 | 健康检查：优先复用连接池（acquire/release 各一次，池内不健康为硬错误），否则新建客户端；记录 `last_health_status`/`last_error`/`last_tool_count`/`last_health_check_at`，批量探测全部已安装项 | `HealthCheck`、`probe`、`ConnectionManager` |
| R-MR-12 | 索引刷新与陈旧判断：默认 24h，`last_fetched` 时间戳与旧版 `<base>/last_fetched.json` 兼容；无时间戳按陈旧处理；搜索匹配名称/描述/tags | `UpdateIndex`、`IsIndexStale`、`Search` |
| R-MR-13 | 版本上报：优先使用编译期版本，为空时从 `pyproject.toml` 的 `[project].version` 回退（section-aware），缺失返回 `"unknown"` | `HiveVersion`、`ProjectVersion` |
| R-MR-14 | 每服务器 `mode`（`tools`/`cli`）：`cli` 模式的服务器永不进入 agent 工具列表，由 agent 通过终端执行 `hive mcp run <name>`；`SetMode` 对未知服务器与非法模式给出修复建议 | `SetMode`、`ResolveForAgent`、`CLIServers` |
| R-MR-15 | `hive mcp run` 经 `mcp2cli` 执行：stdio 合并为单条命令行并 shell-quote；http/sse 用 `--transport`；headers 经 `HIVE_MCP_HEADER_n` 环境变量传递，不落入 argv；unix 明确拒绝 | `MCP2CLIInvocation`、`RunMCP2CLI`、`ShellJoin`/`ShellSplit` |
| R-MR-16 | `mcp2cli` 缓存键 = argv 与 env 的稳定摘要（Python `json.dumps` 兼容转义），env 值变化会触发重新拉取工具清单 | `cacheKey`、`pyJSONString` |
| R-MR-17 | CLI 模式提示块：列出每个启用服务器的描述与命令数，并说明 `--list` / `--search` / `--help` / `--json` 用法；注册表不可读时返回空块而不阻塞会话 | `CLIServersPrompt`、`RegistryCLIServersPrompt` |
| R-MR-18 | `hive mcp` 子命令集：install/add/mode/run/remove/enable/disable/list/info/config/search/health/init/update，支持表格与 `--json` 输出、凭据提示、首次使用安全提示、`add -- <command...>` 用法 | `internal/mcpregistry/cli.go` |

### 3.8 社交平台限流（`internal/ratelimit`）

参考：`framework/rate_limiter.py`、`core/tests/test_social_rate_limiter.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-RL-01 | SQLite 单一数据库文件 `$HIVE_HOME/social_rate_limits.db`（可用 `HIVE_HOME` 覆盖），`actions` 表 + `(platform, account_id, action_type, performed_at)` 索引，初始化幂等 | `NewSocialRateLimiter`、`schema` |
| R-RL-02 | 8 个 `(platform, action)` 限额条目（linkedin profile_view/invite/message/comment、x reply/dm/post、instagram dm），按声明顺序输出，未定义的窗口存 `nil` | `limits`、`limitOrder`、`GetAllLimits` |
| R-RL-03 | 限额解析三层优先级：env `HIVE_RATE_<PLATFORM>_<ACTION>_<WINDOW>`（钳到天花板）> `configuration.json` 的 `rate_limits.<platform>.<action>.<window>`（`max(1, int(v))`，不钳天花板）> 默认值 | `resolveLimit`、`clamp`、`coerceInt` |
| R-RL-04 | 检查顺序：先最短间隔（同账号同动作最近一次），不足间隔返回 `too_fast` + `retry_after_seconds`（保留 1 位小数）+ `min_delay_seconds`，`halt_campaign=false` | `Check`、`minDelay`、`round1` |
| R-RL-05 | 再按 hourly(3600s, 不熔断) → daily(86400s, 熔断) → weekly(604800s, 熔断) 计数判定；超限返回 `<window>_limit_reached` 与已计数的窗口字段 | `windows`、`Check` |
| R-RL-06 | 通过时返回全部已定义窗口的 count/limit（未检查的窗口为 `nil`，对应上游 `None`） | `CheckResult` |
| R-RL-07 | 记录动作：`Record` 归一化 platform/action 为小写，`target_id`/`session_id` 可选，`performed_at` 为 Unix 秒 | `Record` |
| R-RL-08 | 日/周聚合：`GetDailyCounts`（近 24h）、`GetWeeklyCounts`（近 7d），按 `action_type` 分组返回 map | `counts` |
| R-RL-09 | 跨平台/跨账号计数互相隔离；未知 `(platform, action)` 一律放行且无限额字段 | `Check` |
| R-RL-10 | 连接策略：每连接 `SetMaxOpenConns(1)`、WAL 日志、`busy_timeout=5000`，与 Python 侧可共享同一数据库文件 | `connect` |
| R-RL-11 | 可测试性：配置源可注入（`SetConfigOverrides`，`nil` 复位到 `configuration.json`），时钟可替换以固定时间边界 | `configOverrides`、`now` |

### 3.9 自适应 worker 运行预算（`internal/colony` + `internal/agentloop`）

参考：`framework/host/colony_runtime.py`（`_maybe_adapt_colony_budget` / `_apply_adaptive_budget` / `get_stats`）、`framework/agent_loop`（预算上限收缩）、`core/tests/test_adaptive_tool_budget.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-AB-01 | 成功 worker 定义常态：每个终态报告在发布前先喂给采样器；样本窗口固定 50 条，超出丢弃最旧样本 | `maybeAdaptColonyBudget`、`adaptiveBudgetSampleWindow` |
| R-AB-02 | 严格采样准入：仅 `status == "success"`；`budget_limited` / `budget_pinned` 为真的报告与零调用成功全部排除；`tool_calls_used` 必须是整数（bool 视作 1/0，字符串/浮点/None 拒绝） | `payloadTruthy`、`pyInt` |
| R-AB-03 | 标称预算 = `max(2.0 × median, ceil(1.25 × 生命周期内最大成功用量))`，钳到 `[floor=30, worker profile base=200]`；样本数不足 3 条不动作 | `maybeAdaptColonyBudget`、`AdaptiveBudgetFloor`、`AdaptiveBudgetMinSamples` |
| R-AB-04 | 异构守卫：p90 > 4 × median 时视为混合负载、没有单一常态，不收缩 | `adaptiveBudgetDispersionLimit` |
| R-AB-05 | 收缩立即广播：所有非 pinned、非 persistent 的 PENDING/RUNNING worker 在新标称下收缩（在下一迭代边界生效）；QUEUED/终态/pinned/persistent 不动 | `clampInFlightWorkers` |
| R-AB-06 | 提升只由证据驱动：中位数上升永不抬升标称；只有 1.25 × max-guard（未被删失的实测成功成本）可以抬升，且封顶 profile base | `evidenceFloor` |
| R-AB-07 | 提升从不广播：已收缩的 worker 用旧上限跑完，新准入 / 被提升的排队 worker 经准入钩子取当前标称 | `ApplyAdaptiveBudget` |
| R-AB-08 | 预算上限 setter 只收缩：拒绝提升、等值、≤0、无 grace 轮的循环与无上限循环（`current <= 0`） | `AgentLoop.ApplyLifetimeBudgetCap` |
| R-AB-09 | 实时遥测：`LifetimeBudget` / `ToolCallsUsed` / `BudgetLimited` 可读，取消/崩溃路径用实时 grace 计数器判定删失，保证被硬停的 worker 不被当成成功样本 | `LifetimeBudget`、`ToolCallsUsed`、`BudgetLimited` |
| R-AB-10 | 开关：每 colony 经 `RuntimeOptions.AdaptiveToolBudget` 或 `HIVE_ADAPTIVE_TOOL_BUDGET`；关闭时采样器完全不收集样本 | `adaptiveEnabled` |
| R-AB-11 | `get_stats()` 暴露 `adaptive_tool_budget`：`enabled`、`nominal`（首次收缩前为 `nil`，对应 Python `None`）、`samples` | `AdaptiveToolBudgetStats`、`GetStats` |

### 3.10 判官槽与停滞检查（`internal/server`）

参考：`framework/server/judge.py`、`framework/server/stall_check.py`、`framework/config.py`、`framework/server/tests/test_judge.py`、`framework/server/tests/test_stall_check.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-JD-01 | 判官槽为 `configuration.json` 的可选模型段，必须含真值 `api_base` 与 `api_key`；每次提问重读，改动即时生效 | `JudgeSlot`、`HiveConfig` |
| R-JD-02 | 协议注册表按槽的 `protocol` 选择，签名 `(slot, question, state, criteria) -> (probability, error)`；内置 `typesafe`，可注入覆盖 | `JudgeProtocol`、`JudgeProtocols`、`AskJudge` |
| R-JD-03 | 无槽、未知协议、协议失败一律回答「无答案」（`ok=false`）并记日志，不向调用方抛错 | `AskJudge` |
| R-JD-04 | `typesafe` 向 `<api_base>/systemone` POST `state`/`model`/`questions.answer{noul,instructions,criteria?}`，20 秒超时；非 2xx、缺 `answers.answer.noul`、值不可转 float 均失败 | `TypesafeJudge`、`JudgeTimeout` |
| R-JD-05 | 开关：`HIVE_STALL_CHECK` 环境变量（`0/false/no/off` 关）优先于文件内顶层 `stall_check`；选项关时判官槽不可用 | `StallCheckEnabled`、`StallJudgeSlot` |
| R-JD-06 | 判定时机：queen 流以 `turn_done` 结束、有回复、非本回合已判、且回复不是可见错误（仅 `[Error: LLM call failed: ` 前缀，允许前导空白；`[System:` 消息是注入的用户消息，不在判定范围内）；每条用户消息至多一次判定与一次提醒 | `SubscribeStallCheck`、`IsVisibleError` |
| R-JD-07 | 判定上下文：用户消息尾 2000 字符、最后一次模型调用的 reasoning 与 reply 各尾 1500 字符、本回合工具名列表（空为 `[]`）、上一回合回复尾 600 字符与工具列表（仅当存在）；工具调用会重置 reasoning/reply | `stallTurn`、`tailRunes` |
| R-JD-08 | p ≥ 阈值（槽 `threshold` 经 `float()`，缺省 0.5）时以 `is_client_input=false` 注入提醒；提醒文本逐字保留上游 | `StallReminder`、`InjectEvent` |
| R-JD-09 | 判定不得阻塞事件发布：在 goroutine 中执行，句柄暴露订阅 id、`Wait` 与 `Close`（退订后等待在途判定） | `StallCheckSubscription`、`sync.WaitGroup` |

### 3.11 配置特性开关（`internal/server`）

参考：`framework/config.py`、`framework/server/routes_config.py`、`core/tests/test_routes_config_features.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-CF-01 | 四个特性开关的解析顺序固定为「环境变量 > `configuration.json` 顶层布尔值 > 默认值」；环境变量非空白即生效，`0/false/no/off`（忽略大小写与首尾空白）为假 | `envFeatureFlag`、`boolConfigFlag` |
| R-CF-02 | 默认值：`adaptive_tool_budget` 为真，`email_senders`、`memory_timeline`、`stall_check` 为假。文件层前三个键只认布尔值，非布尔回落默认；`stall_check` 复刻上游 `bool(...)` 真值语义（`"true"`、`"false"` 等非空字符串以及非空容器皆为真） | `AdaptiveToolBudgetEnabled`、`EmailSendersEnabled`、`MemoryTimelineEnabled`、`StallCheckEnabled` |
| R-CF-03 | GET 报告四个键的当前解析值；PUT 只接受白名单键的布尔值，非法值或未知键返回 400，且不写文件 | `FeaturesResponse`、`UpdateFeatures` |
| R-CF-04 | PUT 以顶层键持久化，保留 `configuration.json` 其余内容；写失败返回 500 | `UpdateFeatures`、`WriteHiveConfig` |
| R-CF-05 | `adaptive_tool_budget` 变更即时热应用：仅翻转有显式值且与目标不同的存活 colony；`metadata.json` 内布尔 pin 优先，读/解析失败静默跳过 | `ApplyAdaptiveBudgetFlag`、`adaptiveBudgetPinned` |
| R-CF-06 | `email_senders` 变更同时把 `HIVE_EMAIL_SENDERS` 重发布为 `"1"`/`"0"`，供后续 MCP 子进程继承 | `SyncEmailSendersEnv` |
| R-CF-07 | 配置写入原子：同目录唯一临时文件（`.tmp`）→ 缩进 2 的 JSON + 结尾换行 → rename 覆盖 | `WriteHiveConfig` |
| R-CF-08 | HTTP 路由 `GET/PUT /api/config/features`：请求体必须整体是 JSON 对象（空体、坏 JSON、非对象、尾随垃圾一律 400 `Invalid JSON body`，与 aiohttp `request.json()` 对齐）；`features` 键可缺省，缺省时把整个请求体当作特性块；响应为 `{"features": {...}}` | `RegisterConfigRoutes`、`decodeConfigBody` |

### 3.12 LLM 槽位与 provider 库（`internal/server`）

参考：`framework/server/routes_config.py`（`_write_role_section` / `handle_*_llm_section` / `handle_*_provider_library`）、`core/tests/test_routes_provider_library.py`。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-PL-01 | 槽位固定为 `llm` / `worker_llm` / `vision_fallback` / `judge`；GET 逐槽返回原文，缺失或假值为 `null` | `RoleSections`、`GetLLMSections` |
| R-PL-02 | 写槽要求 `model` 非空；`provider` 缺省 `openai`；`api_key_env_var` 仅在 `api_key` 为空时读取同名环境变量 | `WriteRoleSection` |
| R-PL-03 | 健康检查仅在 `validate` 为真（缺省真）、key 与 base 均非空、且角色不是 `judge` 时执行；仅显式 `false` 阻断，错误文本为 `Key check failed against <base>: <message>` | `ProviderKeyValidator` |
| R-PL-04 | 段内容逐字存储；主 `llm` 槽写入后对运行中会话热切换，返回 `sessions_swapped` | `WriteRoleSection`、`RoleWriteDeps.HotSwap` |
| R-PL-05 | `llm` 槽不可清空（400，提示运行时始终需要主模型）；其他槽以 `null` 清空，仅当键存在时写文件 | `PutLLMSection` |
| R-PL-06 | provider 库只保留对象值；保存/删除均不改动槽位，保存从不健康检查；删除最后一项时剪除 `provider_library` 键 | `GetProviderLibrary`、`PutProviderLibrary` |
| R-PL-07 | 应用库条目走同一校验写入路径；未知名称返回 404，未知角色返回 400；库自身保留副本 | `ApplyProviderLibrary` |
| R-PL-08 | HTTP 路由：`GET/PUT /api/config/llm-sections`、`GET/PUT /api/config/provider-library`、`POST /api/config/provider-library/apply`；四条写路径共用同一严格 JSON 解析（失败 400 `Invalid JSON body`），状态码与响应体原样透传决策层 | `RegisterConfigRoutes`、`decodeConfigBody` |

### 3.13 元信息路由与 LLM 配置（`internal/server`）

参考：`framework/server/routes_config.py`（`handle_get_models`、`handle_get/update_sentinel_config`、`handle_get/update_rate_limits`、`handle_get/update_profile`、`handle_upload/get_user_avatar`、`handle_get/put_external_skills`、`handle_get/update_llm_config`）。

| ID | 需求 | 关键实现 |
| --- | --- | --- |
| R-MT-01 | `GET /api/config/models` 返回 `{"models": {provider: [...]}}`，形状与共享模型目录一致；Go 侧每次请求解析目录，便于测试先布置环境 | `ModelsResponse`、`llm.GetModelsCatalogue` |
| R-MT-02 | `GET/PUT /api/config/sentinel`：GET 把任何假值块报为 `{}`；PUT 接受裸对象或 `{"sentinel": {...}}`，非对象 400，空对象删除顶层键，写盘后唤醒监听器且刷新失败不影响 200 | `SentinelResponse`、`UpdateSentinel` |
| R-MT-03 | `GET/PUT /api/config/rate-limits`：GET 返回生效限额（环境变量/配置/默认三层解析）；PUT 只接受已存在的 `<platform>.<action>.<hourly|daily|weekly>` 键，值按 `int()` 转换并下限夹到 1，超推荐上限按文档顺序告警，合并 overrides 后返回生效全集；`limits` 非对象 400 | `RateLimitsResponse`、`UpdateRateLimits`、`ratelimit.GetAllLimitsWithOverrides` |
| R-MT-04 | `GET/PUT /api/config/profile`：只接受白名单主题 `light/dark`、密度 `spacious/compact`、排序键 `date/name/popular`；字符串经 Python `str()` 语义后去空白；保存后把身份镜像进 `global-memory/user-profile.md` 的 `## Identity` 段并保留其余段，镜像失败不影响响应；存储 profile 非对象时 500 | `ProfileResponse`、`UpdateProfile`、`SyncUserProfileMemory` |
| R-MT-05 | 头像：上传只收 JPEG/PNG/WebP，≤ 2 MB 且非空，字段名必须为 `avatar`；写入前删除旧 `avatar.*`；GET 按 `.jpg → .png → .webp` 返回首个存在文件与 `public, max-age=3600`，缺失 404 | `UploadUserAvatar`、`GetUserAvatar` |
| R-MT-06 | 外部技能：GET 解析配置中的根列表并剪掉失效路径，附带建议列表；PUT 校验列表形状（坏列表 400），写回裁剪后的有效路径；存储值不可迭代 500 | `ExternalSkillsResponse`、`PutExternalSkills` |
| R-LC-01 | `GET /api/config/llm` 报告 provider/model/`has_api_key`/token 预算/`connected_providers`/`active_subscription`/`detected_subscriptions`/订阅定义/api_base/env var；`has_api_key` 按运行时实际取 key 的顺序判定，且只在左侧为假时求值右侧；`connected_providers` 跳过 `google`/`together_ai` | `LLMConfigResponse`、`LLMRouteDeps` |
| R-LC-02 | `PUT /api/config/llm` 分派顺序固定为 custom → subscription → API key；custom 保留端点、只换模型、清除订阅旗标并热切换；subscription 写 preset 限额与旗标、删除 `api_key_env_var`、要求 token；API key 要求非空 provider/model、模型在目录中、key 可取且健康检查未显式失败 | `UpdateLLMConfig`、`updateLLMCustom`、`updateLLMSubscription`、`updateLLMAPIKey` |
| R-LC-03 | 三条写路径均经 `MutateHiveConfig` 串行化读改写；网络健康检查与热切换留在锁外；任何校验失败都不落盘 | `MutateHiveConfig`、`configMu` |
| R-LC-04 | 运行时接缝全部可注入：凭据 key 查询、订阅 token、订阅探测、模型校验、热切换；零值回退真实凭据库、`~/.claude`/`~/.codex` 与共享目录 | `LLMRouteDeps.withDefaults` |
| R-LC-05 | 配置中的数值保持源拼写：`configuration.json` 用 `UseNumber` 解码，`provider` 写入 `"123"` 而不是 `"123.0"`；数值形态的 `api_base`/`model` 经 Python `str()` 拼写传给校验与热切换 | `HiveConfig`、`stringPointer` |
| R-LC-06 | 订阅与 API key 的健康检查只在 `valid` 恰好为布尔 `false` 时阻断；`0`、`"false"`、`null` 与缺省一律放行（复刻上游 `is False` 身份判断） | `updateLLMSubscription`、`updateLLMAPIKey` |
| R-LC-07 | 激活无 `api_base` 的 preset（如 `claude_code`）时删除陈旧的 `api_base` 与 `api_key_env_var`，不保留上一 provider 的端点 | `updateLLMSubscription` |
| R-LC-08 | 配置文件在顶层值之后还有尾随内容时整体读作空配置（复刻 `json.load` 拒绝尾随数据），因此 GET 报告空 provider/model 而非部分解析结果 | `HiveConfig` |

## 4. 部分完成与待开始

### 4.1 LLM 抽象（部分完成）

已完成（215 个测试）：模型能力目录与视觉能力判定（`capabilities.go`、`catalog.go`）、端点解析（`endpoints.go`）、密钥池与健康检查（`keypool.go`、`keycheck.go`）、流式事件模型（`stream_events.go`）、可注入的 `mock.go`，provider 纯逻辑层（`provider.go`）：模型/端点分类与 api_base 默认、cache-control 前缀白名单与静态/动态 system 分块、历史缓存断点、Retry-After 解析与指数退避+抖动、backpressure 地板、缓存 token 提取、成本优先级（usage.cost → hidden response_cost → 目录定价）、截断 JSON 修复、工具格式转换、日志摘要，请求塑形层（`request_shape.go`）：`IsAnthropicModel`、`parallel_tool_calls` 的发送判定与拒绝锁存、OpenRouter tool-compat 消息构造（静态缓存块/拼接两种形状、空 assistant 过滤）、`ExtraLLMKWargs`（订阅头、ollama `num_ctx`、`extra_body`），以及非流式完成路径（`complete.go`）：`max_tokens` 辅助预算解析、裸 system 前置 + 历史缓存断点 + JSON 指令、kwargs 组装顺序（base → extra kwargs → `api_key`/`api_base`）、Ollama/Hive 代理的 `tool_choice` 默认、429 与空响应重试（上限 10 / 3，assistant 结尾与 `length` 短路）、响应归一化（内容/模型回落/停止原因/工具调用/成本/输入输出与缓存 token）。`Completion` 增加 `CostUSD` 与 token 字段。向后兼容包装（`anthropic.go`）：`AnthropicProvider` 记住模型、按「显式参数 → 凭据存储 → `ANTHROPIC_API_KEY`」解析密钥并在缺失时报上游原文错误，`Complete`/`AComplete` 原样委托内部 provider，`ChatMessagesToMaps` 负责 OpenAI 字典形状的类型桥接。

P0 已补真实 HTTP 传输（`transport.go`）：`NewHTTPProvider` 为 `Provider.Call` 装上 `httpCompletion`，`(*Provider).AComplete` 直接满足 `Completer`；按模型族分派 OpenAI-compatible chat completions（`POST {api_base}/chat/completions`，基址缺 `/v1` 时自动补，Bearer）与 Anthropic Messages（`POST {api_base}/v1/messages`，`x-api-key` + `anthropic-version: 2023-06-01`），错误响应映射与文本/工具调用/tool_use 场景见 `transport_test.go`（7 条）。P0 测试另含 `internal/tools/builtin` 12 条、`internal/server/chat_runtime_test.go` 2 条与 `e2e/p0_chat_test.go` 1 条（`//go:build e2e`，不计入 `go test ./...`）。

仍缺：流式 SSE 消费侧（运行时走非流式 `AComplete`，`internal/llm` 尚无 HTTP 流式客户端接入 agent loop 的增量渲染）、流式 tool-compat/thinking-block 管线、`hive/`/`kimi/` 代理模型改写（上游无测试覆盖）、订阅/额度查询、服务端把 `SetAnthropicCredentialLookup` 接到真实凭据存储。`Complete` 在 `Call` 为空时返回 `llm: provider has no completion transport`，注入式接口保留。

### 4.2 部分完成与待开始子系统

| 子系统 | 上游参考（行数量级） | 现状 / 待落地能力 |
| --- | --- | --- |
| AgentLoop | `framework/agent_loop/agent_loop.py`（7,497 行） | 已完成核心（318 测试）；剩余为与 Colony/Server 的联调路径 |
| Colony | `framework/host/colony_runtime.py`（2,935 行） | 进行中：`colony_db`、`reap`、`resume`、`runtime`、`scheduler`、`seed`、Worker 定义、worker loop-config 覆盖白名单、自适应 worker 运行预算已落地；`run_worker` / fork / 压缩编排待闭环 |
| 工具 | `tools/src`（96,651 行）、`framework/tools` | 进行中：注册表、分层默认、执行器、参数强制转换、MCP 注册、MCP 客户端传输（stdio/http/unix/SSE，含重试与内容转换）已落地；P0 增设 `internal/tools/builtin` 的 `read_file`/`write_file`/`terminal_exec`（cwd 沙箱、越界与符号链接拒绝、原子写、进程组超时）；104 个集成工具、浏览器桥待开始 |
| 服务端 | `framework/server/*` | 进行中：历史 Worker 读端点、Queen 升级路由、fork-compaction 状态标记（`compaction_status.py`）、session 自动标题（`session_titles.py`，含辅助模型命名与 `session_title_updated` 广播）、judge/stall check、`routes_config.py` 全部 20 个配置端点（特性 / LLM 槽位 / provider 库 / 模型目录 / sentinel / 限流 / profile / avatar / external-skills / LLM 配置）及其决策层已落地；会话侧已接 `GET/DELETE /api/sessions/{id}`、`/stats`、`/triggers`、标题 PATCH、事件历史分页与触发器生命周期六路由（含 timer/webhook 运行时与内嵌 webhook 监听器）；P0 另接 `POST /api/sessions/{id}/chat`、`GET /api/sessions/{id}/events`（会话事件 SSE）与 `ChatRuntime`；其余 aiohttp 路由模块（execution、colonies、colony workers、skills、queens、credentials 等）、完整模型流式 SSE（当前只有会话事件 SSE 与 `/live` 快照流）、集成的 SessionManager 生命周期路由余项与执行接口待开始 |
| 平台抽象 | 上游散落各处的 `sys.platform` 分支 | 待开始：终端、信号、路径、Shell 的统一封装（计划用 purego 规避 cgo） |
| CLI | `framework/cli.py` 及各子命令 | 已完成根入口：`hive serve/open/queen/colony/session/chat`、`--version`/`--model` 全局选项，`internal/cli` 的 12 个 `skill` 子命令（list/install/remove/info/init/validate/doctor/update/search/fork/test/trust）、`debugger`、整段透传的 `mcp`、`janitor` 与 LLMJudge；独立控制台入口 `hive-cred`；P0 起 `serve` 装配真实 `ChatRuntime`（HTTP provider + 内建工具 + transcript 持久化），`chat` 投递到 `POST /api/sessions/{id}/chat` 并提示事件流地址 |
| MCP CLI | `framework/loader/mcp_registry_cli.py`、`mcp_cli.py`（74 测试） | 已完成：库内 14 个子命令（`add/install/mode/run/remove/enable/disable/list/info/config/search/health/init/update`）、表格/JSON 输出、CLI 模式与 `mcp2cli` 桥；`cmd/hive` 根入口以 `Passthrough` 模式注册 `hive mcp`，子命令与 `--` 之后的原始参数交给 `internal/mcpregistry` 自己的解析器，`mcp_test.go` 6 条测试（3 对等 + 3 豁免） |
| Queen / Sentinel | `framework/agents/queen/*`、`host/sentinel*` | 进行中：`internal/queen` 已移植默认身份档案（13 个 embed YAML）、`catalog.json` 读写、标题继承/移除标记与 `hive queen list/show/sessions` 所需的目录辅助，尚无直接测试；queen 会话运行时与 `internal/sentinel` 待开始 |
| Janitor CLI | `framework/maintenance/cli.py` | 已完成：`internal/cli/janitor.go` 移植 `run`/`status`，12 条 CLI 测试（全部 `Parity-exempt`，上游 CLI 模块无单测） |

## 5. 跨切面需求

| ID | 需求 | 状态 |
| --- | --- | --- |
| R-X-01 | `~/.hive` 布局与 `HIVE_HOME` 覆盖，测试不得写入真实主目录 | 已完成（`hivehome`） |
| R-X-02 | 所有状态文件原子写：唯一临时文件 + fsync + rename | 已完成（`atomicfs`） |
| R-X-03 | JSON 宽松读取：未知字段忽略，缺失字段取零值 | 已完成（各包） |
| R-X-04 | 时间统一 UTC、RFC3339Nano 落盘 | 已完成 |
| R-X-05 | 每个测试带 `// Parity:` 或 `// Parity-exempt:` | 已完成（2,238/2,238；2,045 条引用审计 0 missing） |
| R-X-06 | `CGO_ENABLED=0` 六平台编译 | 已完成（库层面） |
| R-X-07 | 无网络、可复现测试 | 已完成 |
| R-X-08 | 竞态检测纳入可选门禁 | 进行中（`make race`） |

## 6. 测试账本（按包）

| 包 | 测试函数 | 已注解 | 未标注 | 注解覆盖率 |
| --- | --- | --- | --- | --- |
| agentloop | 318 | 318 | 0 | 100% |
| agents | 34 | 34 | 0 | 100% |
| atomicfs | 4 | 4 | 0 | 100% |
| attachments | 5 | 5 | 0 | 100% |
| cli | 88 | 88 | 0 | 100% |
| colony | 84 | 84 | 0 | 100% |
| credentials | 48 | 48 | 0 | 100% |
| eventbus | 113 | 113 | 0 | 100% |
| globaldb | 23 | 23 | 0 | 100% |
| hivehome | 3 | 3 | 0 | 100% |
| llm | 222 | 222 | 0 | 100% |
| llmjudge | 22 | 22 | 0 | 100% |
| maintenance | 42 | 42 | 0 | 100% |
| mcpregistry | 160 | 160 | 0 | 100% |
| memory | 46 | 46 | 0 | 100% |
| orchestrator | 40 | 40 | 0 | 100% |
| playbook | 32 | 32 | 0 | 100% |
| queen | 10 | 10 | 0 | 100% |
| ratelimit | 21 | 21 | 0 | 100% |
| runtimehealth | 3 | 3 | 0 | 100% |
| schemas | 10 | 10 | 0 | 100% |
| server | 281 | 281 | 0 | 100% |
| session | 17 | 17 | 0 | 100% |
| sessionreport | 8 | 8 | 0 | 100% |
| skills | 219 | 219 | 0 | 100% |
| storage | 55 | 55 | 0 | 100% |
| tasks | 128 | 128 | 0 | 100% |
| tools | 148 | 148 | 0 | 100% |
| e2e（`//go:build e2e`） | 2 | 2 | 0 | 100% |
| tracker | 35 | 35 | 0 | 100% |
| triggers | 17 | 17 | 0 | 100% |
| **合计** | **2,238** | **2,238** | **0** | **100%** |

注解构成：P0 前为 1,838 个测试带 `.py` 路径的 `Parity:`、376 个仅 `Parity-exempt:`（1 个注释块两者兼有），该构成明细尚未按 P0 后重算；当前 `check_parity_targets` 实测 **2,045 条可解析 `path::test` 引用，另有 55 条 shorthand/prose** 由人工评审。

> 该表是快照。权威数据以 `bash go_hive/scripts/check_parity.sh` 的实时输出为准。

## 7. 假设与依赖

- Go 1.26.5；模块依赖固定为 `modernc.org/sqlite v1.60.1`、`purego v0.11.3`、`yaml.v3 v3.0.1`。
- 上游行为以 `core/` 的 1.0.14 代码与测试为准；文档 01–10 仅作背景。
- 多车道并行开发：本清单的状态会随其他车道落地而变化，`STATUS.md` 与 `check_parity.sh` 是更新更快的两个视图。
