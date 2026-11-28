# 15 · 源码地图

> 目的：让贡献者能在一张表里找到“某能力在哪、从哪读起、改哪里”。
> 行数为本报告基线实测，仅用于估算复杂度。

## 1. 顶层

| 路径 | 作用 |
|---|---|
| `core/framework/` | Hive 运行时实现（内部 API） |
| `core/hive/` | 公开稳定面（`from hive import ...`） |
| `core/frontend/` | React Dashboard |
| `core/tests/` | 跨模块测试（172 文件） |
| `tools/src/` | 内置工具、集成目录、浏览器 CLI |
| `tools/browser-extension/` | Chrome 扩展（Bridge） |
| `docs/` | 仓库文档、发布说明、架构/概念 |
| `scripts/` | 构建、门禁、调试脚本 |
| `contracts/` | 契约/清单（模块清单等） |
| `templates/derivative/` | 派生项目起始 AGENTS.md |

## 2. `core/framework` 逐目录

### 2.1 `agent_loop/`（10,346 行）

| 文件 | 行数 | 职责 |
|---|---:|---|
| `agent_loop.py` | 7,501 | 唯一执行原语：回合、流、工具、预算、压缩、停泊、judge、视觉 |
| `types.py` | 404 | `AgentSpec` / `AgentContext` / `AgentResult` / `AgentProtocol` |
| `internals/types.py` | 480 | `LoopConfig`、`TriggerEvent`、`JudgeVerdict`、`OutputAccumulator` |
| `conversation.py` | 1,873 | `Message`、`NodeConversation`、`ConversationStore` 协议、微压缩入口 |
| `reminders.py` | 568 | `ReminderPoint`、`ParkReason`、`LoopActivity`、`ReminderHub` |
| `idle_nudge.py` | ~300 | 停泊/停顿唤醒 |
| `stream_stall.py` | — | 流卡住续写 |
| `memory_reminders.py` | — | 记忆索引/召回提醒 |
| `past_conversations_reminder.py` | — | BM25 历史召回提醒 |
| `tracker_snapshot_reminder.py` | — | Queen/Worker Tracker 快照 |
| `active_workers_reminder.py` | — | 在飞 Worker 列表 |
| `colony_worker_snapshot_reminder.py` | — | Colony Worker 舰队快照 |
| `colony_parallel_nudge.py` | — | 并行扇出建议 |
| `tool_skill_reminders.py` | — | 可搜索工具/技能目录增量 |
| `turn_context_reminder.py` | — | 外部 turn context 注入 |
| `internals/compaction.py` | — | 0–4 级压缩管线 |
| `internals/cursor_persistence.py` | — | 恢复/游标/队列 |
| `internals/judge_pipeline.py` | — | 回合验收 |
| `internals/stall_detector.py` | — | n-gram/指纹检测 |
| `internals/tool_result_handler.py` | — | 截断/落盘/错误分类/线程池 |
| `internals/synthetic_tools.py` | — | `ask_user` / `suggest_colony` / `set_output` / `escalate` / `report_to_parent` / `collect_result` |
| `internals/credential_tool.py` | — | `credentials` 工具 |
| `internals/vision_fallback.py` | — | 纯文本模型图片 caption |
| `internals/sentinel_tool.py` | — | Sentinel 相关工具 |
| `internals/event_publishing.py` | — | 事件发布辅助 |
| `internals/tool_input_coercer.py` | — | 工具参数强制类型 |

### 2.2 `agents/`（Queen 人格与记忆）

| 文件 | 职责 |
|---|---|
| `queen/queen_defaults/*.yaml` | 13 个人格 + 默认工具类别 + 预设技能 |
| `queen/queen_profiles.py` | 加载/CRUD/选择器/默认 Queen |
| `queen/queen_tools_defaults.py` | 工具类别、eager/worker keep-set、角色默认 |
| `queen/queen_tools_config.py` | 每 Queen 的 `tools.json` 侧车 |
| `queen/agent.py` | `queen_goal`、`queen_loop_config`、`queen_colony_loop_config` |
| `queen/worker_definition.py` | Worker 身份、系统提示、`DEFAULT_LOOP_CONFIG`、`worker.json` 序列化 |
| `queen/queen_memory_v2.py` | 记忆作用域、文件模型、frontmatter |
| `queen/memory_log.py` | 记忆变更日志、作者/确认日期/恢复 |
| `queen/reflection_agent.py` | 短/长反思、`write_memory_file`/`delete_memory_file` |
| `queen/recall_selector.py` | index/wait/background 的模型挑选 |
| `queen/timeline.py` | 日期事件/事实/计划抽取 |
| `queen/soul.py` | Soul 数据模型、版本、提案、回滚 |
| `queen/soul_reflection.py` | 每日反思、证据校验 |
| `queen/soul_probes.py` | 语气探针 |
| `queen/nodes/__init__.py` | Queen 阶段工具列表、系统提示组装、`ALL_QUEEN_TOOLS` |
| `discovery.py` | Agent/Worker 发现与元数据 |

### 2.3 `host/`（Colony 运行时）

| 文件 | 行数 | 职责 |
|---|---:|---|
| `colony_runtime.py` | 2,935 | Worker 调度、并发、超时、自适应预算、TriggerSpec |
| `worker.py` | — | Worker 生命周期、状态、报告、停止、浏览器清理 |
| `event_bus.py` | 2,269 | 事件类型、发布/订阅、历史、写盘 |
| `event_log.py` | — | 事件日志文件 |
| `events_policy.py` | — | worker 事件可见性策略 |
| `tracker_db.py` | — | Tracker schema、denylist、执行、变更日志、binding 回写 |
| `colony_binding.py` | — | `ColonyBinding` 不可变身份 |
| `colony_db.py` | — | `colony.db`（playbook run log 等） |
| `colony_metadata.py` | — | `metadata.json` |
| `colony_tools_config.py` | — | Colony 工具允许列表 |
| `worker_profiles.py` | — | Worker profile 模型与路径 |
| `triggers.py` | — | TriggerDefinition、错过的 tick 计算 |
| `webhook_server.py` | — | 本地 webhook 端点 |
| `playbook/runner.py` | — | `worker`/`converge`/`lane`/dead-letter |
| `execution_manager.py` | — | 传统入口/执行管理 |
| `agent_host.py` | 1,879 | 传统 AgentHost/GraphSpec 宿主 |
| `stream_runtime.py` | — | 流运行时 |
| `outcome_aggregator.py` | — | 结果聚合 |
| `runtime_health.py` | — | 上游网络健康 |
| `runtime_resources.py` | — | 资源采样 |
| `isolation.py` | — | 隔离辅助 |
| `shared_state.py` | — | 图共享状态（stub/legacy） |

### 2.4 `server/`（HTTP/会话）

| 文件 | 行数 | 职责 |
|---|---:|---|
| `app.py` | ~1,240 | 应用工厂、中间件、路由注册、启动钩子 |
| `session_manager.py` | 2,298 | Session 生命周期、冷恢复、触发器、标题 |
| `queen_orchestrator.py` | 2,037 | Queen 装配、阶段工具、报告注入、记忆接线 |
| `routes_sessions.py` | 3,265 | 会话/聊天/分支/快照/统计 |
| `routes_execution.py` | 2,350 | 执行、建群 fork、queen-only 工具解析 |
| `routes_colonies.py` | 1,597 | Colony 列表/数据/导入 |
| `routes_colony_workers.py` | 1,881 | Worker 监控/控制 |
| `routes_config.py` | 1,500 | 配置/模型/features |
| `routes_queens.py`/`routes_queen_tools.py` | — | 组织/人格/工具/技能 |
| `routes_memories.py`/`routes_soul.py` | — | 记忆/Soul |
| `routes_skills.py`/`routes_prompts.py` | — | 技能/Prompt |
| `routes_credentials.py`/`routes_models.py` | — | 凭据/模型 |
| `routes_mcp.py`/`routes_tasks.py`/`routes_events.py` | — | MCP/任务/事件 |
| `routes_sentinel.py`/`routes_logs.py`/`routes_maintenance.py` | — | Sentinel/日志/清理 |
| `channels.py` | — | Channel 插件宿主 |
| `turn_context.py` | — | Turn Context 插件宿主 |
| `queen_mail.py` + `queen_mail_mcp.py` | — | Queen 通信 |
| `claude_code_queens.py` + `routes_claude_code_queens.py` | — | Claude Code Queen |
| `session_fork.py` | — | 会话分支 |
| `session_titles.py` | — | 自动标题 |
| `stall_check.py` | — | 停留检查 |
| `judge.py` | — | judge 槽位/TypeSafe |
| `sse.py` | — | SSE 封装 |
| `boot_status.py` | — | 启动状态 |
| `compaction_status.py` | — | 压缩标记 |
| `streamtoken_refresh.py` | — | 云 streamToken 刷新 |
| `_session_report.py` | — | 会话报告包（凭据脱敏） |

### 2.5 `llm/`

| 文件 | 职责 |
|---|---|
| `provider.py` | `LLMProvider`、`Tool`、`ToolUse`、`ToolResult`、`LLMResponse` |
| `litellm.py` | 主 provider 实现（3,678 行） |
| `anthropic.py` | 原生 Anthropic（缓存/thinking/图片） |
| `antigravity.py` | Antigravity |
| `mock.py` | 测试 provider |
| `stream_events.py` | 统一流事件 |
| `model_catalog.py` + `model_catalog.json` | 精选模型目录 |
| `endpoints.py` | OpenAI 兼容端点（Magpie/Ollama…） |
| `capabilities.py` | 视觉/工具过滤 |
| `key_pool.py` | 多 key 轮询与冷却 |
| `key_check.py` | key 校验 |
| `rate_limiter.py`? | 注：根目录另有 `framework/rate_limiter.py`（社媒限速） |

### 2.6 `tools/`

| 文件 | 职责 |
|---|---|
| `queen_lifecycle_tools.py` | 4,000 行：`run_worker`、Playbook、Trigger、`write_skill`、`search_tools`、Worker 监控、`inject_message`、阶段切换 |
| `tracker_tools.py` | `tracker_sql/register/upsert/query` |
| `playbook_tools.py` | `run_playbook/get/stop/list`、脚本解析与持久化 |
| `worker_monitoring_tools.py` | 健康摘要/状态 |
| `queen_mail_tools.py` | `list_queens`/`message_queen` |
| `harness_tools.py` | 5 个内置工具组 |
| `browser_tools.py` | `browser_setup` |
| `tool_tiers.py` | eager/searchable 引擎、`search_tools` |
| `queen_lifecycle/` | 阶段状态/上下文拆分 |

### 2.7 `skills/`

`models.py`、`parser.py`、`discovery.py`、`catalog.py`、`manager.py`、`overrides.py`、`trust.py`、`tool_gating.py`、`installer.py`、`registry.py`、`skill_writer.py`、`validator.py`、`live_reload.py`、`defaults.py`、`config.py`、`cli.py`、`skill_errors.py`、`install_requests.py`。

### 2.8 其他

| 目录 | 职责 |
|---|---|
| `credentials/` | 模型、加密存储、解析、刷新、校验、catalog、CLI |
| `sentinel/` | 分类器、升级源、通知器、监听器、store、token |
| `storage/` | 会话/对话/检查点存储、v2/v3 迁移 |
| `tasks/` | 任务模型、store、工具、提醒源、事件、钩子 |
| `tracker/` | `DecisionTracker`、运行时日志、LLM debug 日志 |
| `pipeline/` | 请求中间件与 stages |
| `orchestrator/` | 传统图执行（Node/Edge/Graph/Checkpoint/Validator） |
| `loader/` | AgentLoader、ToolRegistry、MCP registry/client/manager、CLI |
| `maintenance/` | retention、janitor、archive、CLI |
| `testing/` | 目标驱动测试框架、LLM judge、审批、错误分类 |
| `observability/` | 结构化日志/trace |
| `global_db/` | 云 DB 客户端、计数缓存、安全 SQL |
| `debugger/` | LLM debug 查看器 CLI |
| `utils/` | IO、文本、Python 环境、任务注册 |
| `schemas/` | Goal、AgentConfig、Decision、Run、Checkpoint、SessionState |

## 3. 按能力追踪路径

### 3.1 “用户消息 → Queen 回复”

```text
routes_sessions.handle_chat
  → SessionManager / Session.queen_executor
  → AgentLoop.inject_event
  → AgentLoop._run_turn_loop
  → LLMProvider.stream
  → ToolRegistry executor
  → EventBus.publish(CLIENT_*)
  → sse / events.jsonl
```

### 3.2 “建群”

```text
Queen 调 suggest_colony（synthetic_tools.py）
  → COLONY_SUGGESTION_REQUESTED
  → 前端 Create Colony 弹窗
  → POST /api/sessions（colony_id + source_session_id）
  → routes_execution.fork_session_into_colony
  → 复制会话 / worker.json / seed_conversation / tracker
  → 原 session colony_spawned 锁定
```

### 3.3 “扇出 Worker”

```text
Queen 调 run_worker（queen_lifecycle_tools.py）
  → 凭据预检 + 工具裁剪
  → ColonyRuntime.spawn_batch
  → 并发调度 / 队列
  → Worker(AgentLoop).execute
  → report_to_parent → SUBAGENT_REPORT
  → QueenOrchestrator 注入 [WORKER_REPORT]
```

### 3.4 “Playbook 收敛”

```text
run_playbook（playbook_tools.py）
  → 解析/保存脚本
  → 快速窗口内同步跑，否则后台
  → host/playbook/runner.py
  → converge → worker(...) → ColonyRuntime.spawn
  → pending() 重新查 Tracker
  → deadletter / 完成注入 [PLAYBOOK_COMPLETE]
```

### 3.5 “记忆召回”

```text
用户消息
  → PastConversationsReminderSource.render
  → memory_tools.recall.recall
  → turn_index (BM25)
  → 渲染摘录 → <system-reminder>
```

### 3.6 “定时触发”

```text
set_trigger → TriggerDefinition → session.active_timer_tasks
  → 到点 → inject_trigger → Queen 回合
  → 会话加载时 compute_missed → 当前实现静默跳过（MISSED_TRIGGERS 无生产者）
```

### 3.7 “停泊升级”

```text
Queen park (ParkReason)
  → Sentinel EscalationSource.render（IDLE_TICK）
  → 确定性硬阻塞? 或 classify_park
  → continue → Reminder 唤醒
  → needs_human → Notifier（Hive/Telegram/Slack）
  → 用户回复 → token 校验 → resume
```

## 4. 测试与源码的对应

| 能力 | 主要测试 |
|---|---|
| 循环/预算/宽限 | `core/tests/test_event_loop_node.py`、`test_loop_config_overrides.py`、`test_adaptive_tool_budget.py` |
| 停泊/恢复 | `test_cancel_user_stop_flow.py`、`test_idle_nudge.py`、`test_midturn_input.py` |
| Colony | `test_colony_runtime_overseer.py`、`test_colony_scheduler.py`、`test_colony_fork_flow.py` |
| Tracker | `test_colony_db.py`、`test_colony_fork_flow.py`、`test_mcp_*` |
| Playbook | `test_playbook_runner.py`、`test_playbook_tools.py` |
| 记忆 | `test_memory_reminders.py`、`test_memory_timeline_recall.py`、`test_memory_log.py` |
| 技能 | `test_default_skills.py`、skills 模块 tests |
| MCP | `test_mcp_client*.py`、`test_mcp_registry*.py`、`test_mcp_cli.py` |
| 清理 | `test_janitor_*.py` |
| 前端 | `core/frontend/src/**/*.test.*` |

## 5. 推荐阅读顺序（贡献者）

1. `docs/architecture/README.md` 与 `docs/key_concepts/*`：建立概念地图。
2. `agent_loop/internals/types.py` 的 `LoopConfig`：先理解所有旋钮。
3. `agents/queen/worker_definition.py`：Worker 是什么、预算从哪来。
4. `host/colony_runtime.py` 的模块 docstring 与 spawn/scheduler：扇出如何发生。
5. `tools/tracker_tools.py` + `host/tracker_db.py`：事实基底。
6. `agent_loop/reminders.py` + 各 `*_reminder.py`：注意力如何注入。
7. `server/queen_orchestrator.py`：Queen 如何被装配。
8. `tools/queen_lifecycle_tools.py` 的 `run_worker` / `run_playbook`：编排面。
9. `host/playbook/runner.py`：批量收敛。
10. `sentinel/`、`server/channels.py`：自治与入口。

## 6. 修改时的“影响面”提示

| 你想改 | 先看 | 可能影响 |
|---|---|---|
| `LoopConfig` 默认 | `internals/types.py` | Queen/Worker/SDK/测试全部 |
| Worker 预算 | `worker_definition.py` + `colony_runtime.py` 的 bounds | 扇出成本、grace、自适应 |
| Queen 工具面 | `queen/nodes/__init__.py` + `queen_tools_defaults.py` | 阶段可见性、Worker 继承、前端 Tool Library |
| Tracker schema | `tracker_db.py` + `migrate_v3.py` | 旧 Colony 兼容、变更日志、导入 |
| 提醒 | `reminders.py` + 具体 source | 长会话行为、prompt cache、测试 |
| 模型目录 | `model_catalog.json` | 上下文/输出上限、视觉、定价、UI |
| 路由 | `server/routes_*.py` + 前端 `src/api` | 前后端契约 |
| Playbook | `runner.py` + `playbook_tools.py` | 并发、重试、持久化、安全 |
| 凭据 | `credentials/store.py` + `resolve.py` | OAuth、alias、预检、工具注入 |
