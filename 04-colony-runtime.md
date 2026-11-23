# 04 · Colony / Worker 运行时

> **本文描述上游 Hive（Python 参考实现）。**
> go_hive 的对应实现：`internal/colony`、`internal/host`、`internal/playbook`、`internal/triggers`。
> 状态：部分（24 维对齐：12 已对齐 / 10 部分 / 2 未验证）；差异登记见 `docs/alignment-differences.md`。

> 关键文件：
> `core/framework/host/colony_runtime.py`（2,935 行）
> `core/framework/host/worker.py`
> `core/framework/tools/queen_lifecycle_tools.py`（4,000 行）
> `core/framework/host/playbook/runner.py`

## 1. ColonyRuntime 的定位

`ColonyRuntime` 的模块 docstring 是理解整个设计的最好入口：

> Each worker is an exact copy of the queen's AgentLoop — same tools, same prompt, same LLM.
> Workers run independently and report results back to the queen via the event bus.
>
> The ColonyRuntime replaces both AgentHost and ExecutionManager.
> There are no graphs, no edges, no nodes, no data buffers.
> Just: spawn N independent clones, let them run, collect results.

它负责：

- 注册/启动/停止 Worker；
- 并发上限与待运行队列（`_pending_queue`）；
- 软超时与硬超时看门狗（`watch_batch_timeouts`）；
- 触发器（`TriggerSpec`）的注册与调度；
- 与 `EventBus`、`TrackerDB`、`ColonyBinding` 的接线；
- Worker 状态查询与恢复。

## 2. Worker 是什么

`Worker`（`host/worker.py`）有两种模式：

| 模式 | 行为 |
| --- | --- |
| **Ephemeral（默认）** | 跑一个 `AgentLoop` 执行，结束时发 `SUBAGENT_REPORT`，然后终止。用于并行扇出。 |
| **Persistent** | 启动后常驻，通过 `inject(message)` 把消息注入已运行的循环。用于长期面向用户的 Overseer。 |

### 2.1 Worker 的预算（硬编码默认 + 可调）

`agents/queen/worker_definition.py` 中的 worker profile：

```python
{
    "max_iterations": 3,
    "grace_iterations": 1,
    "tool_call_budget": 30,
    "tool_call_hard_multiple": 3,      # 30 × 3 = 90 硬停止
    "tool_call_lifetime_budget": 200,
}
```

- **3 个工作回合 + 1 个宽限回合**。
- 宽限回合只允许 `report_to_parent`、`tracker_upsert`、`task_update`，确保 Worker 耗尽预算也能汇报。
- `tool_call_hard_multiple` 对 Worker 固定为 3，队列只允许覆盖 `max_iterations`、`grace_iterations`、`tool_call_budget`、`tool_call_lifetime_budget`、`max_context_tokens`，并有上下界校验（见 `colony_runtime.py` 的 `_LOOP_OVERRIDE_BOUNDS`）。

### 2.2 Worker 的能力边界

Worker：

- 没有长期记忆（每次全新）；
- 不能 spawn 其他 Worker；
- 不能直接和用户说话；
- 阻塞时把已有结果写 Tracker 并 `report_to_parent`，而不是无限尝试绕过；
- 通过 `escalate` 向 Queen 升级，而不是直接找用户。

这些限制是刻意的：Worker 是「窄而可靠的工作单元」，不是「小号 Queen」。

## 3. `run_worker`：一次调用，异步扇出

定义在 `tools/queen_lifecycle_tools.py:1379`。

```python
run_worker(tasks=[{"task": "...", "data": {...}}, ...], timeout=600)
```

语义：

1. **立即返回**。Worker 在后台运行，Queen 可以继续和用户对话或继续派活。
2. **所有任务先入队**，运行中的数量不超过 `max_concurrent_workers`（默认 4，`HIVE_MAX_CONCURRENT_WORKERS` 可覆盖），其余在 `_pending_queue` 等待。
3. **报告作为新回合回到 Queen**：Worker 结束时发 `SUBAGENT_REPORT`，Queen 收到 `[WORKER_REPORT]` 消息（状态、摘要、可选结构化 payload）。
4. **超时是软的，然后才是硬的**：
   - `soft_timeout = timeout`（默认 600s）：向仍在运行的 Worker 注入 `[SOFT TIMEOUT]`，要求立刻 `report_to_parent`。
   - `hard_timeout = min(max(soft × 4, soft + 600), 3600)`：强制停止仍未汇报者。
   - 被停止的 Worker 可以用 `resume_worker_ids` + 可选指导从保存的对话恢复。

实现细节：`_compute_hard_timeout()`（`queen_lifecycle_tools.py:1368`）与 `watch_batch_timeouts()`（`colony_runtime.py:2627`）。

## 4. Tracker：共享账本

Tracker 是 Colony 的唯一事实来源。

### 4.1 数据流

```
Queen: tracker_sql(DDL)                # 建表
Queen: tracker_register_writable(...)  # 声明 Worker 可写列
Worker: tracker_upsert(row)            # 每个工作单元一行
Queen: tracker_query(SQL)              # 只读验收
```

### 4.2 关键约束

- 每个 Colony 一个 `tracker/tracker.db`，由不可变的 `ColonyBinding {name, dir, tracker_db}` 标识（`host/colony_binding.py`）。
- Binding 通过 Queen 的工具上下文、Worker 的任务输入传播，确保双方打开同一个库。
- **没有 Binding 的 tracker 工具调用会拒绝执行**，而不是猜一个路径。
- Worker 只能写 Queen 声明的列；其余列受保护。
- Queen 的查询是只读 SQL。

### 4.3 为什么用 SQLite

- 单文件、零外部依赖，符合「一个 Colony 一个目录」的可移植性。
- WAL 模式支持一写多读，契合「Worker 写、Queen 读」的模式。
- 「做完/未做」永远是一次新鲜查询，不会因为崩溃丢失内存状态。

风险在 [10-assessment.md](10-assessment.md) 讨论：高并发写入、长事务、跨平台文件锁是这条路的天然边界。

## 5. Task Plan：Queen 的持久脊椎

- 每个会话有一个文件支撑的计划（会话目录下的 `tasks.json`）。
- 工具：`task_create`、`task_update`、`task_list`、`task_get`。
- 前端表现为 **Action Plan** 面板。
- 它独立于 Worker 的生命周期：Worker 来来去去，计划一直在。

Task Plan 与 Tracker 的分工：

| | Task Plan | Tracker |
| --- | --- | --- |
| 粒度 | Queen 的执行步骤 | 业务数据行 |
| 写入者 | Queen | Worker（+ Queen） |
| 用途 | 自我管理、给用户看进度 | 结果账本、验收、续跑 |
| 形式 | `tasks.json` | `tracker.db` |

## 6. Playbook：可续跑的方法重放

> `tools/playbook_tools.py`、`host/playbook/runner.py`

Playbook 是一个 Python 脚本（`playbooks/<name>.play.py`），定义 `async def run(args)`。运行器提供：

| 能力 | 说明 |
| --- | --- |
| `tracker_query` / `tracker_count` | 读取「还剩哪些行」 |
| `worker(...)` | 分发一个 Worker 处理一行/一批 |
| `lane(name, concurrency, rate_per_min)` | 限速车道 |
| `converge(...)` | 多轮收敛直到没有剩余 |
| 重试 + 退避 | `_backoff_sleep` |
| 死信（DeadLetter） | 持续失败的行单独列出 |
| 断路器 | 防止系统性故障下无限重试 |

关键设计：**Playbook 不拥有持久状态**。它每次都从 Tracker 重新查询剩余工作，因此：

- 崩溃后重跑即续跑；
- 可以安全地重复执行；
- 「进度」不需要额外的一致性协议。

默认 8 个 Worker 并发，最多 32。

## 7. 触发器：让 Colony 自己醒来

`host/triggers.py` 支持：

- **cron**：`croniter`
- **interval**：固定分钟数
- **webhook**：`host/webhook_server.py`

`TriggerDefinition` 记录触发器元数据；`compute_missed()` 在 Colony 重新加载时计算「离线期间错过了多少次」，并把错过的 tick 报告给 Queen。

`set_trigger` 工具（`queen_lifecycle_tools.py:3732`）由 Queen 调用，写入 `triggers.json`。

## 8. Event Bus：Worker 报告与 UI 流

`host/event_bus.py`（2,269 行）承担两类事件：

| 事件 | 消费者 |
| --- | --- |
| `SUBAGENT_REPORT` | Queen（作为新回合） |
| `CLIENT_*` | 前端（SSE 实时流） |
| 生命周期事件 | 调试、日志、监控 |

Event Bus 是「运行时内部状态」与「外部可观测性」之间的唯一通道。

## 9. Reminder Hub：长会话的连贯性

`agent_loop/reminders.py` 定义了：

- `ReminderPoint`：注入时点枚举。
- `Reminder`：提醒载荷。
- `ReminderHub`：注册/触发/去重。

具体的提醒源是独立模块（`*_reminder.py`），例如：

- `tracker_snapshot_reminder`：把当前 Tracker 概览放进上下文。
- `active_workers_reminder`：告诉 Queen 哪些 Worker 在跑，避免重复派活。
- `colony_parallel_nudge`：提示「这个任务可以并行」。
- `past_conversations_reminder`：注入相关历史。
- `tool_skill_reminders`：列出可加载工具与技能。

这些提醒是「软约束」：它们影响模型行为，但不强制。真正的硬约束在工具层与预算层。

## 10. Sentinel：人不在场时的守护

`core/framework/sentinel/`：

| 模块 | 作用 |
| --- | --- |
| `classifier.py` | 判断 Queen 停泊时该「推一下 / 标记完成 / 升级」 |
| `manager.py` | 生命周期、监听器、升级队列、恢复 |
| `listeners.py` | Telegram / Slack 等入站监听 |
| `notifier.py` | 出站通知 |
| `store.py` | 升级记录持久化 |
| `token.py` | 令牌/鉴权 |

工作方式：

1. Colony 级 opt-in。
2. Queen 停泊时，分类器读取现场（最近消息、工具错误、停泊原因）。
3. 决策：
   - **nudge**：注入一条提醒让 Queen 继续。
   - **done**：标记工作完成。
   - **escalate**：通过 Hive inbox / Telegram / Slack 找人；用户回复后恢复 Queen。
4. `has_attached_ui()` 避免在用户就在界面前时打扰。

Sentinel 是「异步人类在回路」的实现，而不是简单的 timeout。

## 11. Queen 的两阶段

`QUEEN_PHASES = frozenset({"independent", "colony"})`（`queen_lifecycle_tools.py:84`）。

| 阶段 | 工具面 | 行为 |
| --- | --- | --- |
| Independent | 普通工具 + `suggest_colony` | 自己做事，判断是否需要建群 |
| Colony | `run_worker`、Tracker 工具、`write_skill`、`run_playbook`、`set_trigger` | 扇出、验收、系统化 |

从 Independent 到 Colony 需要用户在 Create Colony 对话框中确认。短的聊天记录原样带入 Colony；长的先摘要。

## 12. 评价

**强项**

- 扇出、并发上限、软/硬超时、恢复语义定义得清楚，边界情况考虑充分（例如宽限回合、显式报告优先于硬停止）。
- Tracker 作为唯一事实来源，让重跑/续跑不需要额外状态机。
- Playbook 的「无持久状态」设计非常干净。

**风险**

- `colony_runtime.py` 与 `queen_lifecycle_tools.py` 都很大，调度、超时、恢复、工具 schema 混在一起。
- Tracker 的 schema 由 Queen 运行时创建，缺少迁移/版本管理；Colony 升级时可能出现表结构漂移。
- Worker 报告是自然语言 + 可选 JSON，验收依赖 Queen 的判断；没有强类型的「行级契约」。
- 触发器在 Colony 未加载时不会触发，错过的 tick 只能事后报告，不能补跑（设计上可接受，但需要用户知情）。

**建议**

- 为 Tracker 增加 schema 版本与最小迁移机制。
- 把 Worker 报告的结构化 payload 提升为一等契约（JSON Schema），减少 Queen 的模糊验收。
- 为 ColonyRuntime 画一张「Worker 状态机」图（queued → running → soft-timeout → hard-stopped → reported → resumed），并补状态迁移测试。
