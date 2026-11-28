# 20 · Reminder 机制穷举

> Reminder 是 Hive 的**注意力控制平面**：它不改变 Agent 能做什么，只决定“在什么时刻、把什么短上下文塞回模型眼前”。
> 本章按源码穷举：**15 个 ReminderSource、9 个 ReminderPoint、15 个注册项、每个源的门禁/上限/状态，以及不走 ReminderHub 的“类提醒”注入。**
>
> 关键文件：
> `core/framework/agent_loop/reminders.py`
> `core/framework/agent_loop/agent_loop.py`
> `core/framework/agent_loop/*reminder*.py`
> `core/framework/agent_loop/idle_nudge.py`
> `core/framework/agent_loop/stream_stall.py`
> `core/framework/tasks/reminders.py`
> `core/framework/sentinel/escalation_source.py`
> `core/framework/server/stall_check.py`
> `core/framework/server/queen_orchestrator.py`

---

## 1. 一句话结论

> **Hive 的 reminder 不是“几个 hook”，而是一套自注册的 source 总线：9 个生命周期/时间点 × 15 个按角色过滤的 source；每个 source 自管触发条件、冷却、上限与状态；hub 只负责 fan-out、包装、放置与唤醒。**
>
> 但它**没有全局优先级、没有全局 token 预算、没有按 source 的完整遥测**；`PRE_COMPACT` 没有生产者、`Placement` 枚举未生效、生产 source 没有 `energizes=True`。这是“穷举”时必须看到的边界。

---

## 2. 架构：Hub × Source × Point

### 2.1 三个对象

```python
ReminderPoint   # 何时：9 个时点
ReminderSource  # 什么：15 个生产者，每个自声明 points/applies_to/render
ReminderHub     # 怎么：注册、按角色绑定、fan-out、包装、pending、ticker
```

### 2.2 Source 契约

`ReminderSource`（`agent_loop/reminders.py:279`）：

| 成员 | 作用 | 默认 |
|---|---|---|
| `name` | 遥测/调试标识 | `"reminder"` |
| `points()` | 在哪些时点被咨询 | 必须实现 |
| `applies_to(agent_ctx)` | 是否适用于这个 Agent；`bind()` 时评估一次 | `True` |
| `observe_turn(tool_names)` | 每个内层回合推进计数器 | no-op |
| `tick_interval()` | 时间点的轮询间隔；`None` 表示不轮询 | `None` |
| `render(rctx)` | 返回 `str` / `Reminder` / `None` | 必须实现 |
| `energizes` | 产出的提醒是否应唤醒即将停泊的 loop | `False` |

`render` 返回：

- `str`：hub 自动包成默认 `Reminder`；
- `Reminder(body, source, placement, meta)`：可控制元数据；
- `None` / 空 body：不注入。

### 2.3 Hub 行为

`ReminderHub`：

- `register(source)`：按注册顺序保存；
- `bind(agent_ctx)`：每个 session 一次，用 `applies_to` 过滤；**过滤函数抛异常时保留该 source（fail-open）**；
- `observe_turn(tool_names)`：对绑定后的 source 逐个调用，异常吞掉；
- `collect(point, ...)`：对 `points()` 含该点的 source 逐个 `render`，异常吞掉；
- `fire(point, ...)`：`collect` 后合并成一个 `<system-reminder>` 块；
- `fire_energized(point, ...)`：同 `fire`，额外返回“是否有 source 要求唤醒”；
- `start(...)`：如果有 source 声明 `tick_interval`，启动一个 ticker；
- ticker：`collect(IDLE_TICK)` → `_pending` → `wake()`（通常是 `_input_ready.set`）；
- `post(reminder)`：reactive 提醒进入同一个 `_pending`；
- `take_pending()`：loop 在迭代边界取走，避免写竞争；
- `stop()`：取消 ticker，清空 pending。

### 2.4 包装与页脚

```text
<system-reminder>
{合并后的 source bodies}

Only act on this if relevant to the current work. NEVER mention this reminder to the user.
</system-reminder>
```

- 多个 source 的 body 用 `\n\n` 拼接；
- 没有按 source 分隔的标签；
- 页脚固定。

---

## 3. 穷举：15 个 ReminderSource

> 统计口径：`core/framework` 下生产代码中的 `ReminderSource` 子类；测试里的 `_Source` / `_FakeSource` 不计。
> OSS 工作区没有 `framework.crm`，因此没有 CRM 相关 source。

| # | name | 类 / 文件 | 角色 | points | 触发/门禁 | 状态 |
|---:|---|---|---|---|---|---|
| 1 | `tasks` | `TaskReminderSource` / `tasks/reminders.py` | 有 task 写工具的 Agent（Queen、Worker） | SESSION_START、USER_PROMPT_SUBMIT、POST_TOOL_USE、POST_COMPACT、STOP | `available_tools` 含 `task_create`/`task_update`；chat app 中抑制 drift | `reminder_state.json` |
| 2 | `colony_parallel` | `ColonyParallelNudgeSource` | Queen | POST_TOOL_USE | 已完成 pilot（`browser_*`/`web_scrape`）+ 本回合写 Tracker + 未 `run_playbook` + colony 阶段 | 内存 |
| 3 | `active_workers` | `ActiveWorkersReminderSource` | Queen | USER_PROMPT_SUBMIT | `active_workers_provider` 返回非空 | 无 |
| 4 | `past_conversations` | `PastConversationsReminderSource` | Queen | SESSION_START、USER_PROMPT_SUBMIT | provider 存在；消息 ≥8 字符；3s 超时 | 无 |
| 5 | `memory_recall` | `MemoryRecallReminderSource` | Queen | SESSION_START、USER_PROMPT_SUBMIT、POST_COMPACT | `memory_recall.mode == "wait"`；provider 存在 | `_last` 内存 |
| 6 | `memory_index` | `MemoryIndexReminderSource` | Queen | SESSION_START、USER_PROMPT_SUBMIT、POST_COMPACT | `memory_recall.mode == "index"`；provider 存在 | `_shown`/`_labels` 内存 |
| 7 | `turn_context` | `TurnContextReminderSource` | Queen | SESSION_START、USER_PROMPT_SUBMIT | provider 存在；每条 package 5s | 无 |
| 8 | `searchable_tools` | `SearchableToolsReminderSource` | Queen / 分层 Worker | SESSION_START、USER_PROMPT_SUBMIT、POST_TOOL_USE、POST_COMPACT | Queen 总是；Worker 需 `searchable_tools_provider` | `_announced` 内存 |
| 9 | `skills_catalog` | `SkillsCatalogReminderSource` | Queen | SESSION_START、USER_PROMPT_SUBMIT、POST_TOOL_USE、POST_COMPACT | provider 存在；内容 hash 变化才发 | `_last_sig` 内存 |
| 10 | `tracker_snapshot` | `TrackerSnapshotReminderSource` | Queen | POST_TOOL_USE | 有 binding；首 3 个工具回合；10/20 回合冷却；tracker 工具重置 | 内存 |
| 11 | `worker_tracker_snapshot` | `WorkerTrackerSnapshotReminderSource` | Worker | POST_TOOL_USE | 有 binding；每 20 次工具调用；有表才发 | 内存 |
| 12 | `colony_worker_snapshot` | `ColonyWorkerSnapshotReminderSource` | Queen | TOOL_BUDGET_CHECKPOINT | 有 binding；active/total > 0 | 无 |
| 13 | `idle_nudge` | `IdleNudgeSource` | 所有 | IDLE_TICK | 预算、停泊原因、user_stopped、Sentinel 互斥 | 内存 |
| 14 | `sentinel_escalation` | `EscalationSource` | Colony Queen | IDLE_TICK | Colony opt-in；`is_queen_stream`；非 skip 停泊 | 内存 + escalation records |
| 15 | `stream_stall` | `StreamStallSource` | 所有 | STREAM_STALLED | `continue_nudge_enabled`；每回合 ≤3 | 内存 |

### 3.1 每个 source 的职责

1. **tasks**：任务计划快照 + 漂移 nudge（untracked / stale / all_done / too_many_completed）。它是唯一持久化计数器的 source。
2. **colony_parallel**：Queen 做完 pilot 并写入 Tracker 后，催她 `write_skill` + `run_playbook`，不要继续逐行手工做。
3. **active_workers**：用户再次发言时，提醒“还有 N 个 Worker 在跑”，避免重复派发/过早总结。
4. **past_conversations**：回答前注入最多 6 段相关历史对话摘录（BM25）。
5. **memory_recall**：`wait` 模式下，用模型挑选记忆笔记，回答前注入。
6. **memory_index**：`index` 模式下，列出所有笔记的“文件名 + 一行描述”，让 Queen 自己打开需要的。
7. **turn_context**：外部包在回答前注入的上下文。
8. **searchable_tools**：可搜索工具的增删清单，提示用 `search_tools` 加载。
9. **skills_catalog**：Queen 的阶段过滤技能目录，变化时/压缩后重发。
10. **tracker_snapshot**：Queen 的工具回合中提醒 Tracker 表与行数，主要目的是催建表/落表。
11. **worker_tracker_snapshot**：Worker 每 20 次工具调用提醒“先持久化再继续”。
12. **colony_worker_snapshot**：Queen 自己狂用工具时，提醒“还有 Worker 舰队在跑，别重复做”。
13. **idle_nudge**：停泊/停顿时的唤醒；分 general / awaiting / broken 三档预算。
14. **sentinel_escalation**：停泊时判定继续/完成/找人；nudge 或升级。
15. **stream_stall**：流看门狗判定卡住时，注入“从上次工具结果继续，不要重复调用”的续写提示。

---

## 4. 穷举：9 个 ReminderPoint 与调用点

| # | Point | 调用点 | 放置 | 当前 source |
|---:|---|---|---|---|
| 1 | `SESSION_START` | `AgentLoop._execute_impl`，seed 首条用户消息前 | 普通 user message（含 `<system-reminder>` 文本） | #1 tasks、#4 past、#5 recall、#6 index、#7 turn、#8 searchable、#9 skills（7） |
| 2 | `USER_PROMPT_SUBMIT` | 主循环 drain 前 + 队列内 drain 前（两个 call site） | 普通 user message | #1 tasks、#3 active、#4 past、#5 recall、#6 index、#7 turn、#8 searchable、#9 skills（8） |
| 3 | `POST_TOOL_USE` | 工具批执行后 | **追加到最后一条真实 tool result 的 content 尾部** | #1 tasks、#2 colony_parallel、#8 searchable、#9 skills、#10 tracker、#11 worker_tracker（6） |
| 4 | `TOOL_BUDGET_CHECKPOINT` | 软检查点 / 硬停 | 软：tool result 尾部；硬：`hub.post` → pending → 注入 | #12 colony_worker_snapshot（1）+ loop 自己的预算 body |
| 5 | `PRE_COMPACT` | `_compact` 前 | 普通 user message | **0 个 source（死点）** |
| 6 | `POST_COMPACT` | `_compact` 后 | 普通 user message | #1 tasks、#5 recall、#6 index、#8 searchable、#9 skills（5） |
| 7 | `STOP` | 文本回合结束、即将停泊 | 普通 user message | #1 tasks（1） |
| 8 | `IDLE_TICK` | hub ticker（5s/60s） | pending → `_inject_reminder`（tagged `is_system_reminder`） | #13 idle、#14 sentinel（2） |
| 9 | `STREAM_STALLED` | 流看门狗判定后同步 `collect` | 立即 tagged user message，重流同一回合 | #15 stream（1） |

### 4.1 `PRE_COMPACT` 是死点

`_compact` 会 `fire(PRE_COMPACT)`，但**没有任何生产 source 的 `points()` 包含 `PRE_COMPACT`**。`tasks` 注释明确说“用 POST_COMPACT，不用 PRE_COMPACT”。因此：

- PRE_COMPACT 目前不产生任何提醒；
- 它仍是一个公开契约点，未来 source 可以使用；
- 当前不要假设“压缩前会收到某些提醒”。

### 4.2 `POST_TOOL_USE` 的放置

不是 `Placement` 枚举决定的；是 loop 硬编码：

```python
_task_tail = await hub.fire(POST_TOOL_USE, ...)
...
_combined_tail = "\n\n".join(t for t in (_task_tail, _budget_tail) if t)
...
if _combined_tail and tc.tool_use_id == _tail_target_id:
    stored_content = f"{stored_content}\n\n{_combined_tail}"
```

- 目标：本批最后一条非 `ask_user`/`escalate` 的真实 tool result；
- 与软预算检查点合并成**一个** `<system-reminder>`；
- 不单独发 `REMINDER_INJECTED` 事件（见第 11 节）。

### 4.3 `TOOL_BUDGET_CHECKPOINT`

- **软检查点**：每跨过一个 `tool_call_budget` 倍数（低于硬停），loop 生成预算提示 + `collect(TOOL_BUDGET_CHECKPOINT)` 的 source body，合并后骑 tool result 尾部；
- **硬停**：loop 生成“剩余调用被推迟”的 body + `collect(TOOL_BUDGET_CHECKPOINT)` 的 source body，`hub.post` 成一个 `source="tool_budget"` 的 `Reminder`，下一迭代边界注入；
- 当前该点的 source 只有 `colony_worker_snapshot`。

### 4.4 `STOP`

只在 `_run_turn_loop` 结束且 `real_tool_results` 为空时触发：

```python
if not real_tool_results:
    _stop_reminder_energized = await self._fire_reminder(STOP, ...)
```

- 只有 `tasks` source；
- 若 `tasks` 产生 drift nudge，会作为普通 user message 插入；
- **没有生产 source 设置 `energizes=True`**，所以这条提醒不会阻止 auto-block；Queen 仍会停泊，提醒留到下一次用户消息才被读到（见第 9 节）。

### 4.5 `IDLE_TICK` 与 ticker

- 只有声明 `tick_interval()` 的 source 会让 hub 启动 ticker；
- 当前：
  - `idle_nudge`: `max(0.01, min(5.0, min(active_budgets)/2))`，生产预算 120/120/30 → **5s**；
  - `sentinel_escalation`: `max(5, min(60, classify_after_seconds/2))`，默认 300 → **60s**；
  - hub 取最小间隔 → **5s**；
- ticker `collect(IDLE_TICK)` → `_pending` → `wake()`；loop 在迭代边界 `take_pending`；
- `post()` 的 reactive reminder 也进同一 `_pending`；
- `stop()` 在 `execute()` finally 取消 ticker。

### 4.6 `STREAM_STALLED`

- 流看门狗判定 `ttft` 或 `inactive` 后，loop **同步** `collect`；
- 有 nudge 就立即插入 user message（tagged），然后重流同一回合；
- 不是迭代边界 drain，因为要马上重流。

---

## 5. 角色矩阵

| Source | Queen（DM） | Queen（Colony） | Worker | 独立 SDK Agent |
|---|---|---|---|---|
| tasks | ✅（有 task 工具） | ✅ | ✅（有 task 工具） | ✅（有 task 工具） |
| colony_parallel | ✅（但 phase 检查会拒绝） | ✅ | ❌ | ❌ |
| active_workers | ✅（有 Worker 时） | ✅ | ❌ | ❌ |
| past_conversations | ✅ | ✅ | ❌ | ❌ |
| memory_recall | ✅（wait） | ✅（wait） | ❌ | ❌ |
| memory_index | ✅（index） | ✅（index） | ❌ | ❌ |
| turn_context | ✅ | ✅ | ❌ | ❌ |
| searchable_tools | ✅ | ✅ | ✅（tiered，有 provider） | ❌（除非 provider） |
| skills_catalog | ✅ | ✅ | ❌ | ❌ |
| tracker_snapshot | ❌（无 binding） | ✅ | ❌ | ❌ |
| worker_tracker_snapshot | ❌ | ❌ | ✅（有 binding） | ❌ |
| colony_worker_snapshot | ❌（无 binding） | ✅ | ❌ | ❌ |
| idle_nudge | ✅ | ✅ | ✅ | ✅ |
| sentinel_escalation | ❌（DM 无 colony） | ✅（opt-in） | ❌ | ❌ |
| stream_stall | ✅ | ✅ | ✅ | ✅ |

> `is_queen_stream` 是 Queen 的判别；Worker 为 false。`colony_binding_provider` 决定 Colony 相关 source。

---

## 6. 注册顺序 = 隐式优先级

`AgentLoop.__init__` 的注册顺序：

```text
1  tasks
2  colony_parallel
3  active_workers
4  past_conversations
5  memory_recall
6  memory_index
7  turn_context
8  searchable_tools
9  skills_catalog
10 tracker_snapshot
11 worker_tracker_snapshot
12 colony_worker_snapshot
13 idle_nudge
14 sentinel_escalation
15 stream_stall
```

`collect` 按这个顺序 fan-out，`fire` 按这个顺序拼接 body。**没有 priority 字段，也没有排序 API**；要调整顺序只能改注册顺序。

### 6.1 同一时点的实际顺序

| Point | 实际 body 顺序 |
|---|---|
| SESSION_START | tasks → past → memory_recall → memory_index → turn_context → searchable → skills |
| USER_PROMPT_SUBMIT | tasks → active_workers → past → memory_recall → memory_index → turn_context → searchable → skills |
| POST_TOOL_USE | tasks → colony_parallel → searchable → skills → tracker_snapshot → worker_tracker_snapshot |
| TOOL_BUDGET_CHECKPOINT | loop budget body → colony_worker_snapshot |
| POST_COMPACT | tasks → memory_recall → memory_index → searchable → skills |
| STOP | tasks |
| IDLE_TICK | idle_nudge → sentinel_escalation |
| STREAM_STALLED | stream_stall |
| PRE_COMPACT | （无） |

`memory_recall` 与 `memory_index` 同时注册，但按 `memory_recall.mode` 只有一个会 render；另一个返回 `None`。

---

## 7. 上限、冷却与状态（穷举）

| Source | 关键参数 | 默认 | 重置/持久化 |
|---|---|---|---|
| tasks | `REMINDER_WARMUP_TURNS` | 4 | 每 session 计数；`reminder_state.json` |
| tasks | `REMINDER_THRESHOLD_TURNS`（stale） | 10 | 任务写入重置 `turns_since_task_op` |
| tasks | `UNTRACKED_TURNS` | 5 | 首次任务写入后不再 untracked |
| tasks | `REMINDER_COOLDOWN_TURNS` | 10 | 每次 drift 重置；软上限后 ×2 |
| tasks | `DRIFT_SOFT_CAP` / `DRIFT_BACKOFF_FACTOR` | 2 / 2 | 任务写入重置 drift 计数 |
| tasks | `SNAPSHOT_HEARTBEAT_TURNS` | 12 | 每次快照重置 |
| tasks | `MAX_SNAPSHOTS` | 40 | 每 session 上限 |
| tasks | `SNAPSHOT_MAX_ITEMS` | 12 | 列表截断 |
| tasks | `COMPLETED_CLEANUP_THRESHOLD` | 20 | 完成数 >20 触发清理 nudge |
| colony_parallel | `HIVE_COLONY_PARALLEL_COOLDOWN` | 4 回合 | 每次触发重置 |
| colony_parallel | `HIVE_COLONY_PARALLEL_MAX` | 4/session | 内存；重启重置 |
| active_workers | `HIVE_ACTIVE_WORKERS_SNAPSHOT_MAX` | 8 | 列表截断 |
| active_workers | `HIVE_ACTIVE_WORKERS_TASK_MAX` | 80 字符 | 任务截断 |
| past_conversations | `HIVE_PAST_RECALL_TIMEOUT` | 3s | 超时跳过 |
| past_conversations | `MIN_MESSAGE_CHARS` | 8 | 短消息不召回 |
| memory_recall | `memory_recall.wait_seconds` | 10s | 超时跳过 |
| memory_recall | `_last` 去重 | — | 相同 pick 不重复 |
| memory_index | `_shown` 增量 | — | START/POST_COMPACT/first 全量 |
| turn_context | `CONTEXT_TIMEOUT_S` | 5s/package | 超时跳过 |
| searchable_tools | `_announced` 增量 | — | POST_COMPACT 全量 |
| skills_catalog | `_last_sig` hash | — | POST_COMPACT 全量 |
| tracker_snapshot | `HIVE_TRACKER_QUEEN_FIRST_FIRE` | 3 工具回合 | 首次后冷却 |
| tracker_snapshot | `HIVE_TRACKER_QUEEN_COOLDOWN_NO_TABLE` | 10 回合 | 无表 |
| tracker_snapshot | `HIVE_TRACKER_QUEEN_COOLDOWN_WITH_TABLE` | 20 回合 | 有表 |
| tracker_snapshot | `_MAX_TABLES` / `_QUERY_BUDGET_SECONDS` | 10 / 0.05s | 读取上限 |
| worker_tracker_snapshot | `HIVE_TRACKER_WORKER_TOOL_CALLS` | 20 | tracker 工具重置 |
| colony_worker_snapshot | `_MAX_LISTED` / `_TASK_BODY_MAX` | 8 / 80 | 无状态 |
| idle_nudge | general / awaiting / broken | 120s / 120s / 30s | 用户消息 reset |
| idle_nudge | max nudges | 3/session | 每 substate：1/1/3 |
| idle_nudge | tick | 5s | — |
| sentinel_escalation | `classify_after_seconds` | 300s | 每次评估 |
| sentinel_escalation | `max_nudges_before_escalate` | 5 | 每次 resume |
| sentinel_escalation | tick | 5–60s（默认 60s） | — |
| stream_stall | `continue_nudge_max_per_turn` | 3 | 每回合 reset |
| stream_stall | `continue_nudge_enabled` | True | 配置 |

### 7.1 状态持久化

| Source | 状态 | 重启 |
|---|---|---|
| tasks | `reminder_state.json` | **保留** |
| 其它 14 个 | 内存 | **重置** |
| sentinel | escalation records | open 记录保留；内存计数器重置 |

因此：任务提醒的冷却/上限跨重启保持；memory index/recall 在会话重启后会重新全量；tracker 快照重新从“首 3 回合”开始；colony_parallel 的 4 次上限重置；idle 计数重置。

---

## 8. 门禁层级与 fail-open

```text
1. AgentLoop.__init__: 注册 15 个 source
2. ReminderHub.bind(ctx): applies_to 过滤（异常 → 保留）
3. 每个 point: points() 是否包含
4. render(rctx): 自己的前置条件
5. 自己的 cap/cooldown
6. Hub.collect: 异常 → 跳过；空 body → 丢弃
7. fire: 合并；energized 标记
8. loop 放置：user message / tool result tail / pending
9. loop 行为：auto-block、wake、stream re-run
```

**fail-open 点**：

- `bind` 的 `applies_to` 异常：保留 source；
- `collect` 的 `render` 异常：跳过该 source；
- `observe_turn` 异常：吞掉；
- ticker 一轮异常：吞掉，继续下一轮；
- `fire` 异常：返回 False，不阻断回合；
- source 的 provider 异常：多数 source 自己 `try/except` 返回 `None`。

**不是 fail-open 的点**：

- source 内部无上限的正文可能撑大 prompt；
- 多个 source 同时触发时没有全局 token 预算；
- `render` 返回超大 body 不会被截断（source 自己负责）。

---

## 9. `energizes` 与 `Placement` 的现状

### 9.1 `energizes`

- 默认 `False`；
- 生产代码中**没有 source 设置 `True`**（只有测试 `_Source(energizes=True)`）；
- `ReminderSource` 注释提到的 `CrmRevealReminderSource` 不在本 OSS 工作区；
- 因此当前：
  - `STOP` 的提醒不会阻止 Queen auto-block；
  - `tasks` 的 STOP drift nudge 会留在对话里，等下一次用户消息才被读到；
  - 只有 ticker 的 `wake()` 会唤醒停泊，`energizes` 不是唤醒机制。

### 9.2 `Placement`

- `Placement` 枚举有 `USER_MESSAGE` / `TOOL_RESULT_TAIL`；
- **生产代码没有读取 `Reminder.placement`**；
- 实际放置由 point + 调用点硬编码：
  - SESSION_START / USER / PRE / POST / STOP → user message；
  - POST_TOOL_USE / 软预算 → tool result tail；
  - 硬停 / IDLE → pending → tagged user message；
  - STREAM_STALLED → tagged user message。
- 因此 `placement` 字段目前是**声明性但未生效**的元数据。

### 9.3 `is_system_reminder` 标记不一致

| 注入路径 | `is_system_reminder` |
|---|---|
| `_fire_reminder`（生命周期） | **False**（普通 user message，正文含 `<system-reminder>`） |
| `_inject_reminder`（ticker/post） | **True** |
| STREAM_STALLED | **True** |
| POST_TOOL_USE / 软预算 | 不是独立 message；拼进 tool result |
| 硬停 | `_inject_reminder` → True |
| `grace_iteration` | `_inject_reminder` → True |

这会影响压缩/UI 对“系统提醒”的识别：生命周期提醒看起来像用户消息。

---

## 10. 不走 ReminderHub 的“类提醒”注入

这些也以 `<system-reminder>` 或类提醒文本进入上下文，但不是 `ReminderSource`：

| 机制 | 位置 | 触发 | 是否唤醒 | 遥测 |
|---|---|---|---|---|
| **stall check** | `server/stall_check.py` | `stall_check` 开启 + judge 槽位；Queen `turn_done`；judge p≥0.5 | `inject_event`（默认 wake） | 无 `REMINDER_INJECTED` |
| **background memory recall** | `server/queen_orchestrator.py` | `memory_recall.mode == "background"`；用户消息 | `inject_event(wake=False)` | 无 `REMINDER_INJECTED` |
| **grace iteration** | `agent_loop/agent_loop.py` | 回合/生命周期预算耗尽 | `_inject_reminder`（tagged） | 有 `REMINDER_INJECTED`，source `grace_iteration` |
| **tool budget hard stop** | `agent_loop/agent_loop.py` | 硬停 | `hub.post` → `_inject_reminder` | 有，source `tool_budget` |
| **soft budget body** | `agent_loop/agent_loop.py` | 软检查点 | 骑 tool result tail | **无** |
| **POST_TOOL_USE tail** | `agent_loop/agent_loop.py` | 工具批后 | 骑 tool result tail | **无** |
| **attachment listing** | `server/routes_execution.py` | 用户上传附件 | 嵌入 user message | 无 |
| **oversized user message notice** | `agent_loop/internals/compaction.py` | 用户消息 >100k 字符 | 嵌入该 user message | 无 |
| **grace skip placeholders** | `agent_loop/agent_loop.py` | 宽限回合跳过工具 | tool result 文本 | 无 |
| **inject_message** | Queen 工具 | Queen 向 Worker 注入 | `inject_event` | 否（是消息，不是提醒） |
| **worker report / trigger** | EventBus | 报告/定时器 | user 回合 | 否 |

**结论**：如果你想“穷举所有会出现在模型眼前的框架提示”，不能只看 `ReminderHub`；`stall_check`、background recall、预算 tail、附件块、压缩截断都是旁路。

---

## 11. 遥测与可观测性

`REMINDER_INJECTED` 事件：

```json
{
  "type": "reminder_injected",
  "stream_id": "queen",
  "node_id": "queen",
  "data": {
    "source": "idle_nudge",
    "detail": "parked_broken",
    "meta": {"substate": "parked_broken", "nudge_count": 1, "cap": 3}
  }
}
```

### 11.1 有遥测的路径

- `_fire_reminder`（生命周期）：source 固定为 `point:<point>`，**丢失具体 source 名**；
- `_inject_reminder`（ticker/post/grace）：source 为 source 名或 `grace_iteration`；
- `STREAM_STALLED`：source `stream_stall`，meta 含 reason/elapsed/count/cap；
- 硬停：source `tool_budget`，meta 含 deferred/cause/limit。

### 11.2 无遥测的路径

- `POST_TOOL_USE` 的 task/colony/tracker/skills/searchable tail；
- 软预算检查点 body；
- `stall_check`；
- background recall；
- attachment / compaction notice。

### 11.3 计数器

`AgentLoop._bump`：

- `reminder_injected_<source>`；
- `reminder_injected_point:<point>`（生命周期）；
- `session_idle_nudge_<substate>`；
- 还有 `tool_budget_soft_reminder`、`tool_budget_deferred`、`stream_nudge_sent` 等。

---

## 12. 冲突与交互

| 组合 | 交互 |
|---|---|
| idle_nudge vs sentinel_escalation | Sentinel autopilot 开启时 idle 自动让位（`_sentinel_autopilot_active`），避免 double nudge |
| idle_nudge vs user_stopped | user_stopped 时 idle 完全静默 |
| idle_nudge vs external wait | ASK_USER / CREDENTIAL_FORM / COLONY_SUGGESTION / AWAITING_QUEEN 不 nudge |
| idle_nudge vs silent park | TURN_DONE / USER_STOPPED / COLD_INTERRUPTED 不 nudge |
| tasks vs tracker | 两者都可能催“落表/记任务”，但一个管 Plan，一个管 Tracker |
| colony_parallel vs tracker | colony_parallel 需要本回合写 Tracker才触发；tracker snapshot 是“写了吗/表有哪些” |
| memory_recall vs memory_index | 按 mode 二选一；同时注册，只有一个 render |
| searchable_tools vs skills_catalog | 都可能在同一时点发；body 各自独立，合并成一个 block |
| stream_stall vs idle | stream stall 处理“流卡住”，idle 处理“停泊/停顿”；流活跃时 idle 不触发 |
| tasks STOP vs auto-block | STOP 提醒不 energize，Queen 仍停泊；提醒留到下次用户消息 |
| 多个 source 同时触发 | 无全局 token 预算，body 直接拼接；顺序 = 注册顺序 |

---

## 13. 失败模式

| 现象 | 原因 | 诊断 |
|---|---|---|
| 提醒没出现 | `applies_to` false / point 不匹配 / provider 缺失 / cap 到顶 | 查 `REMINDER_INJECTED`；查 source 日志 |
| 提醒重复太多 | cooldown 配置过小 / 状态未持久化 / 重启 | 查 `reminder_state.json`；查 source 计数 |
| 停泊后没人管 | idle 被 Sentinel 让位但 Sentinel 未启用；或 user_stopped / silent park | 查 `ParkReason`、`sentinel_enabled`、`user_stopped` |
| Queen 重复派发 | active_workers 没触发（provider 空）/ 用户消息太短 | 查 `active_workers_provider`、`USER_PROMPT_SUBMIT` |
| 压缩后失忆 | PRE_COMPACT 无 source；POST_COMPACT 的 source 未覆盖 | 查 `POST_COMPACT` 注入；见 4.1 |
| 任务漂移 | `reminder_state.json` 丢失 / chat app 抑制 | 查 state 文件；`chat_app_provider` |
| 记忆笔记不出现 | mode 不匹配 / index provider 空 / POST_COMPACT 重置 | 查 `memory_recall.mode` |
| 流卡住不续写 | `continue_nudge_enabled=False` / cap 到顶 | 查 LoopConfig / `stream_stall` 日志 |
| `REMINDER_INJECTED` 缺 source | 生命周期 block 合并为 `point:<point>` | 查 `point` 与 body；无 per-source 遥测 |
| 提示词膨胀 | 多 source 同时触发、无全局预算 | 查 prompt 中 `<system-reminder>` 数量/大小 |
| STOP 提醒没人读 | 没有 source energizes，Queen 停泊 | 见 9.1；可让 source 设置 energizes 或不要在 STOP 注入 |

---

## 14. 评价与建议

### 14.1 优点

- **自注册、按角色过滤**：一个 loop 服务 Queen/Worker/SDK，source 自己声明归属。
- **时点设计完整**：启动、用户消息、工具后、预算、压缩前后、停止、空闲、流卡住。
- **fail-open**：任何 source 失败不会阻断 Agent。
- **上限与冷却**：大部分 source 有明确节流，避免“提醒墙”。
- **ticker 与生命周期分离**：时间点提醒通过 pending + iteration-boundary drain，避免对话写竞争。
- **任务提醒持久化**：唯一跨重启保留的计数器。

### 14.2 风险

| 风险 | 说明 |
|---|---|
| **无全局优先级** | 注册顺序即优先级；不能按角色/紧急度动态排序 |
| **无全局 token 预算** | 多个 source 同时触发可能塞入大量文本 |
| **source 归因丢失** | 生命周期 block 合并为 `point:<point>`；tail reminders 无事件 |
| **PRE_COMPACT 死点** | 当前没有任何 source |
| **Placement 未生效** | 枚举存在但不被读取；实际放置硬编码 |
| **energizes 无生产使用** | STOP 提醒不会阻止停泊，可能长期未读 |
| **is_system_reminder 不一致** | 生命周期提醒被当作普通用户消息 |
| **状态易失** | 除 tasks 外，重启重置计数；可能重复 nudge |
| **旁路提醒分散** | stall_check、background recall、budget tail、附件块不在 hub |
| **source 顺序耦合** | 新增 source 可能改变 prompt 顺序与缓存命中 |
| **chat app 抑制 drift** | 在聊天应用中任务漂移提醒被关闭 |
| **无 source 健康指标** | 只能看日志/事件，不能看“哪个 source 最近没工作” |

### 14.3 建议（按优先级）

**P0**

1. **给 ReminderHub 加全局 per-turn 字符/token 预算**，超出时按优先级截断/丢弃，避免提醒墙。
2. **修复 `PRE_COMPACT`**：要么让 tasks 在 PRE_COMPACT 也发快照（但注释说 POST 更好），要么正式标记为 reserved；不要让调用点空转。
3. **统一 `is_system_reminder` 标记**：生命周期提醒也应 tagged，或明确文档说明差异。
4. **补遥测**：POST_TOOL_USE/软预算 tail 也发 `REMINDER_INJECTED`；生命周期 block 保留 per-source 列表（至少 meta 里带 sources[]）。

**P1**

5. **实现 `Placement` 或删除它**：不要让字段声明与实际放置矛盾。
6. **明确 `energizes` 策略**：STOP 的 task drift 应该 energize，还是不要在 STOP 注入？二者选一。
7. **持久化关键计数**：colony_parallel 的 per-session cap、tracker 首触发等，重启后不应完全重置（如果产品希望“每 session”而不是“每进程”）。
8. **加 source 健康/命中指标**：`reminder_source_rendered{source,point}`、`reminder_source_skipped{reason}`。

**P2**

9. **集中旁路提醒**：stall_check / background recall / attachment block 至少登记到一个统一清单，避免“穷举 ReminderHub 就以为穷举了全部”。
10. **给 source 增加 priority**，或者明确“注册顺序 = 优先级”并做测试。
11. **给每个 source 的 body 加长度上限**（source 自己或 hub 统一）。
12. **在 prompt 里给 reminder 块加 source 标签**（可选，可能影响缓存/模型行为，需评估）。

---

## 15. 源码地图

| 关注点 | 文件 |
|---|---|
| Hub / Point / Source / Reminder / wrap | `agent_loop/reminders.py` |
| 注册 15 个 source、fire 调用点 | `agent_loop/agent_loop.py:918`、`:1555`、`:1858`、`:2158`、`:4627`、`:5726`、`:5751`、`:6002`、`:6144`、`:6959`、`:6973`、`:7159` |
| tasks | `tasks/reminders.py` |
| colony parallel | `agent_loop/colony_parallel_nudge.py` |
| active workers | `agent_loop/active_workers_reminder.py` |
| past conversations | `agent_loop/past_conversations_reminder.py` |
| memory recall/index | `agent_loop/memory_reminders.py` |
| turn context | `agent_loop/turn_context_reminder.py`、`server/turn_context.py` |
| searchable/skills | `agent_loop/tool_skill_reminders.py` |
| tracker snapshots | `agent_loop/tracker_snapshot_reminder.py` |
| colony worker snapshot | `agent_loop/colony_worker_snapshot_reminder.py` |
| idle nudge | `agent_loop/idle_nudge.py` |
| stream stall | `agent_loop/stream_stall.py` |
| sentinel escalation | `sentinel/escalation_source.py`、`sentinel/classifier.py` |
| stall check（旁路） | `server/stall_check.py` |
| background recall（旁路） | `server/queen_orchestrator.py:1269` 起 |
| grace/budget synthetic | `agent_loop/agent_loop.py:1771`、`:6011` |
| 测试 | `agent_loop/tests/test_reminder_energize.py`、`tasks/tests/test_reminders.py`、`server/tests/test_turn_context.py` |
