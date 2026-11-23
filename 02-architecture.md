# 02 · 总体架构

> **本文描述上游 Hive（Python 参考实现）。**
> go_hive 的对应实现：`internal/server`、`internal/agentloop`、`internal/colony`、`internal/host`、`internal/tools`。
> 状态：已移植；差异登记见 `docs/alignment-differences.md`。

## 1. 分层视图

```
┌─────────────────────────────────────────────────────────────────────┐
│                          用户界面层                                  │
│  React Dashboard (core/frontend)  ·  CLI (framework/cli.py)         │
│  Chat / Colony Data / Org Chart / Skills / Models / Memory          │
└──────────────────────────────┬──────────────────────────────────────┘
                               │ HTTP + SSE (127.0.0.1:8787)
┌──────────────────────────────▼──────────────────────────────────────┐
│                          服务层                                      │
│  aiohttp app (framework/server/app.py)                              │
│  SessionManager · QueenOrchestrator · routes_* (20+ 路由模块)        │
│  channels（聊天应用接入） · turn_context（外部包注入上下文）          │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
┌──────────────────────────────▼──────────────────────────────────────┐
│                        运行时 / 宿主层                                │
│  ColonyRuntime · Worker · EventBus · ReminderHub                     │
│  TrackerDB · Playbook Runner · Triggers · Sentinel                   │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
┌──────────────────────────────▼──────────────────────────────────────┐
│                        执行原语层                                     │
│  AgentLoop（唯一执行单元）                                            │
│  conversation · compaction · cursor_persistence · stall_detector    │
│  judge_pipeline · tool_result_handler · vision_fallback             │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
┌──────────────────────────────▼──────────────────────────────────────┐
│                     模型 / 工具 / 存储层                              │
│  LiteLLMProvider · AnthropicProvider · model_catalog                │
│  ToolRegistry · MCP Registry · in-process tools · browser bridge     │
│  FileConversationStore · SessionStore · CheckpointStore              │
└─────────────────────────────────────────────────────────────────────┘
```

## 2. 一条消息的完整生命周期

以「用户在 Dashboard 里给 Queen 发一条消息」为例：

1. **前端** `POST /api/sessions/<id>/chat`，携带消息与附件。
2. **路由层** `routes_sessions.py` 校验会话、把消息交给 `SessionManager`。
3. **SessionManager** 找到（或恢复）该会话的 Queen `AgentLoop`，通过 `inject_event` 把用户消息注入正在运行（或已停泊）的循环。
4. **AgentLoop** 进入 `_run_turn_loop`：
   - 构造本轮消息（含系统提醒、记忆召回、任务计划、可用技能/工具清单）。
   - 调用 `LLMProvider.stream()` 流式取回文本、推理、工具调用。
   - 对工具调用做并发执行（标记为 concurrency-safe 的最多 10 个并行，其余按序）。
   - 工具结果超过阈值（默认 30,000 字符）时落盘为文件，只把预览与路径放回上下文。
   - 达到工具预算检查点、触发压缩阈值、被 judge 打回、流停顿等情况下注入提醒或采取动作。
5. **事件** 通过 `EventBus` 发布 `CLIENT_*` 事件，由 `sse.py` 推给前端；`SUBAGENT_REPORT` 推给 Queen。
6. **回合结束**：Queen 若只回复文本不调用工具，则循环**停泊（park）**，游标写入磁盘，等待下一条用户输入、触发器或 Worker 报告。
7. **持久化**：每个消息部分写穿到 `FileConversationStore`（一 part 一 JSON），游标写 `cursor.json`，因此进程重启后可精确恢复。

## 3. 核心设计决策

### 3.1 只有一个执行原语

`AgentLoop`（`core/framework/agent_loop/agent_loop.py`，约 7,500 行；2026-11-23 实测 7,501）同时是：

- Queen：长会话、近乎无限回合、大上下文、每回合 30 次工具调用预算（开 Colony 后 50）。
- Worker：3 个工作回合 + 1 个宽限回合，每回合 30 次调用，生命周期 200 次，宽限回合只允许 `report_to_parent` / `tracker_upsert` / `task_update`。
- 独立 Agent（SDK 用户）：通过 `AgentSpec` 配置。

**收益**：持久化、压缩、停顿检测、预算、视觉回退、judge 只需实现一次。
**代价**：`AgentLoop` 成为全系统最复杂、最大的单文件，任何改动都需要理解大量分支。

### 3.2 编排是运行时工具调用，不是图

没有 DAG、节点、边、数据缓冲区。`ColonyRuntime` 的模块 docstring 写得非常直白：

> There are no graphs, no edges, no nodes, no data buffers.
> Just: spawn N independent clones, let them run, collect results.

Queen 通过 `run_worker(tasks=[...])` 一次提交 N 个任务，调用立即返回。Worker 在后台跑，完成后发 `SUBAGENT_REPORT`，作为 Queen 对话中的新回合出现。

### 3.3 协调靠四种「基质」

| 基质 | 作用 | 实现 |
| --- | --- | --- |
| **Tracker** | 共享结果账本，唯一事实来源 | `tracker/tracker.db`（SQLite + WAL） |
| **Task Plan** | Queen 的持久待办，跨会话存在 | 会话内 `tasks.json`，工具 `task_create/update/list/get` |
| **Event Bus** | Worker 报告回传、UI 实时流 | `host/event_bus.py` |
| **Reminder Hub** | 在固定时点注入上下文，保持长会话连贯 | `agent_loop/reminders.py` + 各 `*_reminder.py` |

这四者取代了传统图编排里的「边」和「共享 buffer」。

### 3.4 状态在磁盘，不在内存

- 会话：`state.json` + `conversations/parts/*.json` + `cursor.json`。
- Colony：`worker.json`、`tracker/tracker.db`、`queens/.../sessions/`、`workers/`。
- 记忆：`~/.hive/memories/` 下的 Markdown。
- 检查点：`checkpoints/index.json` + 单个 checkpoint JSON。

所有写入走 `utils/io.atomic_write`，避免半写状态。

### 3.5 工具分层：eager + searchable

上下文里只放一小批 eager 工具的完整 schema；其余工具以一行清单出现在 `<searchable_tools>` 中，Agent 用 `search_tools` 按需加载。实现见 `tools/tool_tiers.py` 与 `agents/queen/queen_tools_defaults.py`。这直接控制了 prompt 体积与缓存稳定性（`get_current_tools()` 在无变化时返回同一列表对象，保证 prompt cache 命中）。

## 4. 并发模型

- **进程内 asyncio**：每个 Agent 一个异步循环。
- **Worker 并发**：默认 4 个同时运行（`HIVE_MAX_CONCURRENT_WORKERS`），其余排队。
- **工具并发**：标记为 concurrency-safe 的工具每回合最多 10 个并行。
- **Playbook 并发**：默认 8 个 Worker，最多 32（可配 lane 与速率）。
- **HTTP 线程池**：`app.py` 为请求处理器单独建 32 线程池（`HIVE_REQUEST_EXECUTOR_MAX`），避免 Queen 的阻塞工具调用饿死 Colony UI。
- **数据库**：SQLite WAL 模式；Tracker 的写入主要来自 Worker 的 `tracker_upsert`，读取来自 Queen 的 `tracker_query`。

## 5. 错误处理与韧性

| 机制 | 行为 | 位置 |
| --- | --- | --- |
| 停泊/恢复 | 等待用户、凭证、确认、停止、错误、崩溃时都写游标 | `agent_loop/internals/cursor_persistence.py` |
| 大结果落盘 | >30,000 字符的结果写成 `<tool>_<n>.txt`，上下文留预览 | `internals/tool_result_handler.py` |
| 压缩 | 先清理旧工具结果指向文件，再对最旧回合做 LLM 摘要，最近回合保持原样 | `internals/compaction.py` |
| 停顿检测 | 流看门狗 + n-gram 相似度 + 工具调用指纹 | `internals/stall_detector.py` |
| Judge | 成功标准由 judge 验收，`RETRY` 以 `[Judge feedback]` 回到下一回合 | `internals/judge_pipeline.py` |
| 硬预算 | 每回合与生命周期工具调用上限、Worker 回合上限、并发上限 | `agent_loop/types.py`、`host/colony_runtime.py` |
| Sentinel | Queen 停泊时分类并决定推一下 / 标记完成 / 升级 | `sentinel/` |

## 6. 代码地图（按关注点）

| 关注点 | 关键路径 |
| --- | --- |
| 执行循环 | `core/framework/agent_loop/agent_loop.py` |
| 循环内部机制 | `core/framework/agent_loop/internals/` |
| 会话与消息模型 | `core/framework/agent_loop/conversation.py` |
| 提醒注入 | `core/framework/agent_loop/reminders.py`、`*_reminder.py` |
| Colony / Worker | `core/framework/host/colony_runtime.py`、`host/worker.py` |
| Queen 工具（`run_worker`/`suggest_colony`/触发器） | `core/framework/tools/queen_lifecycle_tools.py` |
| Tracker | `core/framework/host/tracker_db.py`、`host/colony_binding.py`、`tools/tracker_tools.py` |
| Playbook | `core/framework/tools/playbook_tools.py`、`host/playbook/runner.py` |
| Queen 与记忆 | `core/framework/agents/queen/` |
| 内置工具注册 | `core/framework/tools/harness_tools.py`、`tools/src/` |
| MCP | `core/framework/loader/mcp_registry.py`、`loader/mcp_client.py` |
| Skill | `core/framework/skills/` |
| Sentinel | `core/framework/sentinel/` |
| HTTP / SSE | `core/framework/server/` |
| 前端 | `core/frontend/src/` |
| 配置 | `core/framework/config.py` |
| 存储 | `core/framework/storage/` |

## 7. 与 `core/hive/` 的关系

`core/hive/__init__.py` 是一个**惰性导出表**：`_EXPORTS` 把公开名字映射到 `framework.*` 模块，首次访问时才 import。这样 `import hive` 很便宜，SDK 用户只为实际用到的部分付出导入成本。

它导出的核心面包括：

- 循环：`AgentLoop`、`LoopConfig`、`AgentContext`、`AgentSpec`、`AgentResult`
- 模型：`LLMProvider`、`LiteLLMProvider`、`AnthropicProvider`、`MockLLMProvider`、各类流事件
- 宿主：`EventBus`、`ColonyRuntime`、`ColonyConfig`、`Worker`、`AgentLoader`
- 配置：`HIVE_HOME`、`RuntimeConfig`、`get_api_key` 等
- 扩展点：`Channel`/`ChannelHost`（聊天应用）、`TurnContext`/`TurnContextHost`（外部包注入上下文）

`framework` 仍然是可导入的完整实现，但 `hive` 是承诺语义化版本的公开契约（见 `AGENTS.md`：不得在非大版本中重命名/删除导出或做不兼容行为变更）。

Go 侧没有等价的惰性导出表：公开面是 Go 包路径，而不是 `core/hive/` 式的名字导出；SDK exports 的解析基准属待拍板 D2（见 `docs/alignment-differences.md` 与 `docs/exports-baseline-decision.md`）。
