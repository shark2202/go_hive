# 02 · 总体架构

## 1. 八层视图

Hive 的运行时可以按“从执行到边界”分为八层。这不是目录分层，而是**职责依赖**：

```text
┌──────────────────────────────────────────────────────────────┐
│ 8. 能力与边界   Tools / MCP · Credentials · Budgets · Models  │
├──────────────────────────────────────────────────────────────┤
│ 7. 沉淀         Memory · Soul                                 │
├──────────────────────────────────────────────────────────────┤
│ 6. 自治         Trigger · Sentinel                            │
├──────────────────────────────────────────────────────────────┤
│ 5. 方法         Skill · Playbook                              │
├──────────────────────────────────────────────────────────────┤
│ 4. 信号         Event Bus · Reminder Hub                      │
├──────────────────────────────────────────────────────────────┤
│ 3. 状态         Plan（意图）· Tracker（事实）                  │
├──────────────────────────────────────────────────────────────┤
│ 2. 组织         Colony · Queen · Worker                       │
├──────────────────────────────────────────────────────────────┤
│ 1. 执行         AgentLoop（唯一原语）                          │
└──────────────────────────────────────────────────────────────┘
```

| 层 | 原语 | 回答的问题 | 章节 |
|---|---|---|---|
| 1 执行 | AgentLoop | 一个 Agent 如何走一步？ | 03 |
| 2 组织 | Colony / Queen / Worker | 谁负责什么、谁跟谁说话？ | 04 |
| 3 状态 | Plan / Tracker | 想做什么？实际做完什么？ | 05 |
| 4 信号 | EventBus / ReminderHub | 刚发生了什么？现在该注意什么？ | 05 |
| 5 方法 | Skill / Playbook | 怎么做？如何批量做？ | 07 |
| 6 自治 | Trigger / Sentinel | 何时醒？何时找人？ | 08 |
| 7 沉淀 | Memory / Soul | 学到了什么？成了谁？ | 06 |
| 8 边界 | Tools / MCP / Credentials / Budgets | 能做什么、不能做什么？ | 09–12 |

## 2. 进程与部署拓扑

典型部署只有一个常驻进程：

```text
浏览器 / Electron 渲染进程
        │ HTTP + SSE（默认 127.0.0.1:8787）
        ▼
┌─────────────────────────────── aiohttp server（hive serve）───────────────────────────────┐
│  SessionManager                                                                          │
│    ├─ Session A：Queen AgentLoop + EventBus + 会话目录                                     │
│    ├─ Session B：Colony Queen（绑定 ColonyBinding）+ ColonyRuntime                        │
│    └─ ChannelHost / TurnContextHost / SentinelManager                                     │
│                                                                                          │
│  进程内：MCP stdio 子进程、工具线程池、浏览器桥 supervisor、Playbook asyncio 任务          │
└──────────────────────────────────────────────────────────────────────────────────────────┘
        │
        ├── ~/.hive/             （HIVE_HOME：配置、凭据、Queen、Colony、记忆、日志）
        ├── 外部 MCP servers      （stdio / HTTP / SSE / unix；可选 CLI 模式）
        └── 用户的 Chrome         （通过 Browser Bridge 扩展 + hive-browser CLI）
```

关键事实：

- **单进程多会话**：`SessionManager` 管理所有 live session；每个 session 自带 `EventBus` 与 LLM provider（`server/session_manager.py:1`）。
- **没有外部数据库**：状态全部是文件 + 每个 Colony 一个 SQLite（`config.py`、`storage/`）。
- **浏览器桥独立进程**：`tools/src/gcu/bridge_host.py` 有 supervisor 模式，避免工具服务器的 60s 超时/重启把扩展连接带走。
- **桌面模式**：Electron 通过 `HIVE_DESKTOP_MODE` / `HIVE_DESKTOP_PARENT_PID` 等环境变量控制前端归属与桥生命周期；CLI `hive serve` 也完整可用。

## 3. 目录拓扑（`HIVE_HOME`）

v3 布局（`config.py:80`）：

```text
$HIVE_HOME/
├── configuration.json          # 全局配置（模型、记忆、Sentinel、Soul、保留策略…）
├── credentials/                # Fernet 加密凭据 + index.json
├── mcp_registry/               # installed.json、config.json、cache/
├── memories/
│   ├── global/                 # 跨 Queen / Colony
│   ├── colonies/<name>/        # Colony 作用域
│   └── agents/queens/<id>/     # Queen 作用域
├── queens/<queen_id>/
│   ├── profile.yaml
│   ├── skills/
│   ├── soul/                   # soul.json、history/、proposals/、log.jsonl、state.json
│   └── sessions/<session_id>/
│       ├── meta.json
│       ├── events.jsonl
│       ├── conversations/      # parts/<seq>.json、cursor.json、meta.json
│       ├── tasks.json
│       ├── data/               # 附件、溢写文件
│       └── timeline.jsonl
├── colonies/<name>/
│   ├── metadata.json           # 来源 Queen、fork session、worker profiles
│   ├── worker.json             # 默认 Worker 模板
│   ├── profiles/<name>/worker.json
│   ├── tools.json / skills_overrides.json
│   ├── skills/
│   ├── playbooks/<name>.play.py
│   ├── tracker/tracker.db（+ -wal / -shm）
│   ├── colony.db               # playbook run log 等框架簿记
│   ├── seed_conversation/      # fork 时 Queen 对话快照，作为 Worker 起点
│   ├── queens/<queen>/sessions/<sid>/
│   ├── workers/<sid>/
│   ├── triggers.json
│   └── escalations/<id>.json   # Sentinel 升级记录
├── channels/<name>/            # Channel 自己的状态目录
├── turn_context/<name>/
├── logs/、event_logs/、llm_logs/、compaction_log/、archive/
└── trusted_repos.json          # 技能信任
```

要点：

- **DM 会话**在 `queens/<q>/sessions/`；**Colony overseer 会话**在 `colonies/<c>/queens/<q>/sessions/`。同一个 `session_id` 可能同时出现在多棵 Colony 的 overseer 树里（fork 保留 id），所以路径查找必须遍历候选，不能取首个（`session_manager.py` 的 `_iter_queen_session_dirs`）。
- **`tracker/` 而不是 `data/`**：v3 迁移把 Tracker 从歧义的 `data/` 移出（`storage/migrate_v3.py`）。
- **Colony 目录自包含**：可整体打包导入导出（`POST /api/colonies/import`）。

## 4. 一条消息的完整生命周期

以 Dashboard 向 Queen 发一条消息为例：

```text
[1] 前端 POST /api/sessions/<id>/chat
       │
[2] routes_sessions / routes_execution
       │  校验会话、附件落盘、扫描 PDF（大文件转“可选择性读取”提示）
       ▼
[3] SessionManager 找到/恢复该 session 的 Queen AgentLoop
       │  inject_event(message, is_client_input=True)
       ▼
[4] AgentLoop._run_turn_loop（单回合，内部可多次 model↔tool）
       ├─ 构建本轮输入：
       │   系统提示（静态身份/人格 + 动态召回/时间）
       │   ReminderHub 注入（会话开始/用户消息/工具后/预算/压缩/停泊）
       │   历史消息 + 工具结果（大结果已落盘）
       ├─ LLMProvider.stream() 流式输出（文本/推理/工具调用）
       ├─ 工具调用：
       │   concurrency_safe 的只读工具并行（上限 10）
       │   其余按序；超时/后台化/结果截断
       ├─ 工具结果回填；judge 判定（有成功标准的 Agent）
       └─ 预算/停顿/压缩/中断检查
       ▼
[5] 事件：CLIENT_OUTPUT_DELTA / TOOL_CALL_* / REMINDER_INJECTED / CONTEXT_USAGE_UPDATED
       │  EventBus → SSE → 前端；同时写 events.jsonl
       ▼
[6] 回合结束：模型只回复文本、不调用工具
       │  → ParkReason.TURN_DONE，游标写盘，等待下一条输入
       ▼
[7] 持久化：每条消息一个 parts/<seq>.json；cursor.json 记录迭代/输出/待输入/停止标记
```

关键设计点：

- **输入不是“直接塞进对话”**：`inject_event` 进入队列，在迭代边界由 loop 自己 drain，避免与 loop 的写竞争（`agent_loop.py:_drain_injection_queue`）。
- **Worker 报告不是回调**：Worker 结束时发 `SUBAGENT_REPORT`，`QueenOrchestrator` 把它格式化成 `[WORKER_REPORT]` **用户回合**注入 Queen 的对话（`queen_orchestrator.py:1784`）。
- **停止是显式状态**：用户点 Stop 后 `user_stopped=True` 写盘；重启后不会自动恢复（`cursor_persistence.py`）。

## 5. 设计不变量

以下不变量贯穿代码，违反它们通常就是 bug：

1. **一个需求只有一个 Queen 会话**；建群后原 session 被锁定（`colony_spawned`），必须 compact-and-fork 才能继续。
2. **一个 Colony 只有一个 `ColonyBinding {name, dir, tracker_db}`**；没有 binding 的 Tracker 工具拒绝执行，绝不猜路径（`host/colony_binding.py`）。
3. **Tracker 是事实，Plan 是意图**；两者不能互相替代。
4. **Worker 不直接面对用户**；它只能 `report_to_parent` 或 `escalate` 给 Queen。
5. **磁盘是恢复的唯一真相**；内存状态必须能由 parts + cursor 重建。
6. **工具调用必须有界**：每回合软/硬预算、生命周期预算、单调用超时、内容大小上限。
7. **提醒是建议，不是指令**：统一包在 `<system-reminder>` 中，并带“仅相关时行动、不要向用户提及”的页脚。

## 6. 模块地图（按目录）

| 目录 | 职责 | 关键入口 |
|---|---|---|
| `agent_loop/` | 执行原语、提醒、会话对象、内部件 | `AgentLoop.execute`、`ReminderHub`、`NodeConversation` |
| `agents/queen/` | 人格、提示、记忆反思、Soul、Worker 定义 | `queen_defaults/*.yaml`、`worker_definition.py` |
| `host/` | Colony 运行时、Worker、EventBus、Tracker、Trigger、Playbook、AgentHost | `ColonyRuntime`、`Worker`、`EventBus` |
| `server/` | aiohttp 应用、SessionManager、QueenOrchestrator、23 个路由模块、Channel、QueenMail | `app.create_app` |
| `tools/` | Queen 生命周期工具、Tracker/Playbook 工具、Worker 监控、浏览器发现、工具分层 | `register_queen_lifecycle_tools` |
| `loader/` | Agent 加载、工具注册表、MCP 注册表/客户端/连接管理、CLI | `ToolRegistry`、`MCPRegistry` |
| `skills/` | 发现、解析、信任、安装、热重载、工具门控 | `SkillsManager`、`write_skill` |
| `sentinel/` | 停泊分类器、升级源、通知器（Inbox/Telegram/Slack）、监听器 | `EscalationSource` |
| `llm/` | Provider 抽象、LiteLLM/Anthropic/Antigravity/Mock、流事件、模型目录、视觉能力 | `LLMProvider.stream` |
| `credentials/` | 加密存储、解析、刷新、catalog、CLI | `hive-cred` |
| `storage/` | 会话/对话/检查点存储、v2/v3 迁移 | `FileConversationStore` |
| `tasks/` | 文件型任务计划、工具、提醒源、事件 | `TaskStore` |
| `tracker/` | 决策记录、运行时日志、LLM debug 日志 | `DecisionTracker` |
| `pipeline/` | 请求级中间件（限流、成本、校验、LLM/MCP/凭据/技能阶段） | `PipelineRunner` |
| `orchestrator/` | 传统图执行器（节点/边/数据缓冲），仅用于独立 Agent | `GraphExecutor` |
| `maintenance/` | 保留策略与 Janitor | `run_once` |
| `testing/` | 基于成功标准的测试框架 | `LLMJudge`、`TestStorage` |
| `observability/` | 结构化日志、trace context | `StructuredFormatter` |
| `global_db/` | 团队云 DB 的 HTTP 客户端与安全 SQL 构造 | `global_db.client` |

## 7. 传统图执行器还在，但不在 Colony 路径上

`orchestrator/`（`GraphExecutor`、`NodeSpec`、`EdgeSpec`、`DataBuffer`、`CheckpointStore`）仍完整存在，`AgentLoader` 也能加载 `agent.json` 的 `nodes/edges` 格式。它服务的是：

- 独立/导出的单 Agent；
- 早期的“节点图”用法；
- 需要在节点之间传递共享数据缓冲、条件边、检查点的场景。

**Colony 运行时不使用它。** `ColonyRuntime` 的模块注释写得很直接：

> There are no graphs, no edges, no nodes, no data buffers. Just: spawn N independent clones, let them run, collect results.

因此阅读代码时要区分两条路径：

| 路径 | 入口 | 状态模型 | 适用 |
|---|---|---|---|
| Colony | `SessionManager` → `QueenOrchestrator` → `ColonyRuntime` | Tracker + Plan + EventBus | 生产主路径 |
| Graph | `AgentLoader` → `GraphExecutor` | 节点间 DataBuffer + Checkpoint | 独立 Agent / 兼容旧格式 |

## 8. 一次请求经过的“预算与保护”

一条用户消息最终会同时受到这些约束：

| 约束 | 默认 | 位置 |
|---|---|---|
| 单回合工具软预算 | Queen 30 / Colony Queen 50 / Worker 30 | `LoopConfig.tool_call_budget` |
| 单回合硬停 | 软预算 × 5（Queen）/ × 3（Worker） | `tool_call_hard_multiple` |
| 生命周期工具预算 | Queen 0（不限）/ Worker 200 | `tool_call_lifetime_budget` |
| 回合数 | Queen 999,999 / Worker 3+1 grace | `max_iterations` / `grace_iterations` |
| 上下文 | 180k（Queen/Worker 默认），随模型目录调整 | `max_context_tokens` |
| 工具结果 | 30k 字符后落盘 | `max_tool_result_chars` |
| 单工具超时 | 60s（`browser_` 180s） | `tool_call_timeout_seconds` |
| 流看门狗 | 首事件 600s / 事件间 120s | `llm_stream_*` |
| Worker 并发 | 4（可 `HIVE_MAX_CONCURRENT_WORKERS`） | `ColonyConfig` |
| Playbook 并发 | 8（最大 32） | `meta.concurrency` |

这些参数在 12 章汇总成表。
