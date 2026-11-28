# 05 · 状态与协调：Tracker、Plan、EventBus、ReminderHub

> Colony 没有图、没有边、没有共享数据缓冲区。它靠四种“基底”协调：
> **Tracker（事实）× Plan（意图）× EventBus（信号）× ReminderHub（注意力）**。

## 1. Tracker：Colony 的共享账本

### 1.1 位置与归属

每个 Colony 一个 SQLite：

```text
~/.hive/colonies/<name>/tracker/tracker.db（+ -wal / -shm）
```

身份由不可变 `ColonyBinding {name, dir, tracker_db}` 表示（`host/colony_binding.py`）。它通过：

- Queen 的 `ToolRegistry` 执行上下文；
- Worker 的 `input_data`；

两条路注入。**没有 binding 的 Tracker 工具直接拒绝执行**，绝不从 session id 合成路径。这防止了“两个 Colony 或一个 Colony 与游离会话写到同一个库”的事故。

### 1.2 四个工具

| 工具 | 谁能用 | 作用 |
|---|---|---|
| `tracker_sql(sql)` | Queen only | 原生 SQL；允许 DDL/DML，但有 denylist |
| `tracker_register_writable(table, write_columns, key_columns, mode)` | Queen only | 把表登记进 `_tracker_registry`，Worker 才可写 |
| `tracker_upsert(table, row)` | Queen + Worker | 按 key 做 `INSERT ... ON CONFLICT DO UPDATE`（或 append） |
| `tracker_query(sql)` | Queen + Worker | 只读 SELECT |

`tracker_register_writable` 会校验：

- 表存在；
- 列存在；
- key 列有**唯一索引**覆盖（否则 upsert 不幂等，重派 Worker 会写重复行）。

### 1.3 框架自有表

Schema v3（`host/tracker_db.py`）：

```sql
_tracker_registry(table_name, write_columns JSON, key_columns JSON, mode, registered_at)
_tracker_meta(key, value, updated_at)
_tracker_changes(id, table_name, pk JSON, op, changed_at)
```

- 所有 `_` 前缀表由框架拥有：`tracker_sql` 拒绝对其 DDL/DML，但允许 SELECT 以自省。
- 注册表时自动安装 `AFTER INSERT/UPDATE` 触发器，把每次写入记入 `_tracker_changes`，UI 可显示“哪一行变了”。
- 变更日志最多保留 **10,000** 条，写路径机会式清理。

### 1.4 SQL 安全边界

Denylist 很小，但覆盖关键逃逸面：

| 拒绝项 | 原因 |
|---|---|
| `ATTACH` / `DETACH` | 逃出 tracker.db |
| `PRAGMA` | 运维/内省；表结构可用 `sqlite_master` 查 |
| `VACUUM` / `REINDEX` | 运维，不是业务 |
| `load_extension(...)` | 可加载任意共享库 |
| 对 `_*` 表的 DDL/DML | 框架命名空间 |
| 单次 > 20 条语句 | 限制爆炸半径 |

连接统一开启：

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
```

### 1.5 设计含义

- **Queen 是 schema 设计者**：她可以 `CREATE TABLE leads (...)`，然后 `tracker_register_writable`。
- **Worker 是写行者**：只能 upsert 注册表，不能改结构。
- **“做完没”永远是新鲜查询**，不是内存计数；这是 Playbook 可续跑、崩溃可恢复的根。

风险：Tracker 的 schema 质量完全依赖 Queen 的 SQL 判断；没有迁移框架、没有列类型校验、没有业务约束（除唯一索引）。复杂业务需要人审 schema。

## 2. Plan：Queen 的意图

### 2.1 文件与模型

每个 session 一个 `tasks.json`（`core/framework/tasks/`）：

- 路径由 session 目录决定；
- 结构是 `TaskListDocument`：`meta`（goal、creator、schema_version）+ `tasks[]`；
- 任务 ID 为单调整数，不复用；
- 状态：`pending | in_progress | completed | abandoned | archived`。

`abandoned` 由 sweep 产生：`in_progress` 超过 **30 分钟**未更新（`IDLE_TASK_THRESHOLD_SECONDS`）。`archived` 是 Queen 整理计划时移入 History，可通过 `task_update` 恢复。

### 2.2 工具

`task_create`、`task_update`、`task_list`、`task_get`：

- 支持 `blocks` / `blocked_by` 依赖；
- 完成一个被阻塞任务前，会检查 blocker 是否完成；
- `task_update(status="archived")` 归档；`include_archived=true` 可查看；
- 任务工具对 Worker 也开放，用于自管理和对抗上下文裁剪。

### 2.3 生命周期钩子

`tasks/hooks.py`：

- `task_created`：写入后、返回前触发；钩子抛 `BlockingHookError` 会删除刚创建的任务并返回错误；
- `task_completed`：转为 completed 时触发；阻塞错误会把状态回滚到 `in_progress`。

这给外部系统一个“否决任务”的同步点（例如合规检查）。

### 2.4 Plan 与 Tracker 的区别

| | Plan | Tracker |
|---|---|---|
| 内容 | 意图：要做什么、顺序 | 事实：每行结果 |
| 写入者 | Queen（Worker 可写自己的小列表） | Worker upsert，Queen 建表/验收 |
| 粒度 | 任务级 | 业务单元级 |
| 丢失后果 | 长会话失去方向 | 无法并行、审计、续跑 |
| 位置 | 会话 `tasks.json` | Colony `tracker.db` |

两者不能互相替代：只有 Plan 没有 Tracker，Queen 无法知道 200 行里哪行完成；只有 Tracker 没有 Plan，Queen 会失去“为什么做这些行”的线索。

## 3. EventBus：信号

### 3.1 结构

`host/event_bus.py`（2,269 行）：

- 每个 session 一个 `EventBus`，保存环形历史（默认 `max_history=1000`）；
- 支持订阅/取消订阅、按类型等待、全局事件总线（`get_global_event_bus`）；
- 事件持久化到 `<session>/events.jsonl`；Worker 另有独立事件日志；
- 写盘时剥除诊断大字段（`full_request`），避免日志爆炸；
- `HIVE_DEBUG_EVENTS=1` 可把每个事件写 JSONL 到 `~/.hive/event_logs/`。

### 3.2 事件模型

`AgentEvent`：

```python
{
  "type": EventType,
  "stream_id": "queen" | "worker:<id>",
  "node_id": ..., "execution_id": ...,
  "data": {...},
  "timestamp": ...,
  "correlation_id": ...,
  "colony_id": ...,
  "run_id": ...,
  "seq": ...    # 单调发布计数，用于 SSE/历史去重
}
```

### 3.3 事件家族（77 种类型，节选）

| 家族 | 代表 |
|---|---|
| 执行生命周期 | `EXECUTION_STARTED/COMPLETED/FAILED/PAUSED/RESUMED`、`PAYMENT_REQUIRED` |
| 循环/节点 | `NODE_LOOP_STARTED/ITERATION/COMPLETED`、`LOOP_STATE_CHANGED` |
| LLM 流 | `LLM_TEXT_DELTA`、`LLM_REASONING_DELTA`、`LLM_TURN_COMPLETE` |
| 工具 | `TOOL_CALL_STARTED/COMPLETED`、`TOOL_CALL_REPLAY_DETECTED` |
| 客户端 | `CLIENT_OUTPUT_DELTA`、`CLIENT_REASONING`、`CLIENT_INPUT_REQUESTED/RECEIVED/COMMITTED`、`CLIENT_CREDENTIAL_FORM_REQUESTED` |
| 建群/阶段 | `COLONY_SUGGESTION_REQUESTED`、`COLONY_CREATED`、`QUEEN_PHASE_CHANGED` |
| Worker | `WORKER_COMPLETED/FAILED`、`SUBAGENT_REPORT` |
| 上下文 | `CONTEXT_COMPACTION_STARTED`、`CONTEXT_COMPACTED`、`CONTEXT_USAGE_UPDATED` |
| 提醒 | `REMINDER_INJECTED` |
| 流健康 | `STREAM_TTFT_EXCEEDED`、`STREAM_INACTIVE`、`STREAM_NUDGE_SENT` |
| Trigger | `TRIGGER_AVAILABLE/ACTIVATED/DEACTIVATED/FIRED/REMOVED/UPDATED`、`MISSED_TRIGGERS` |
| 任务 | `TASK_CREATED/UPDATED/DELETED/LIST_RESET` |
| 会话 | `SESSION_FORKED`、`SESSION_TITLE_UPDATED`、`SESSION_SNAPSHOT` |
| 升级 | `ESCALATION_REQUESTED`、`CREDENTIALS_REQUIRED` |
| 全局 | `CREDENTIAL_PROVIDER_CONNECTED/DISCONNECTED`、`TOOL_CATALOG_REFRESHED`、`TOOLS_CONFIG_CHANGED`、`CRM_CHANGED` |

### 3.4 两条消费路径

```text
AgentLoop / Worker / Runtime
        │ publish
        ▼
EventBus ──► event_log（events.jsonl）
        ├──► SSE（/api/sessions/<id>/events）
        ├──► QueenOrchestrator（把 SUBAGENT_REPORT 变成 [WORKER_REPORT]）
        ├──► 提醒源（observe_turn / 状态）
        └──► 前端/监控/标题/记忆反思等订阅者
```

`SESSION_SNAPSHOT` 是特殊事件：新 SSE 订阅建立时**先发一次**，从环形历史重建“Queen 现在忙不忙”，不需要等下一个实时事件。

## 4. ReminderHub：工程化的注意力

> 15 个 Source、9 个 Point、逐源门禁/上限/状态与旁路提醒的穷举见 [20 Reminder 机制穷举](20-reminder-mechanism.md)。

### 4.1 为什么需要它

一个长期、高扇出的循环会“忘事”：

- 忘了还有 Worker 在跑，重复派发；
- 忘了把上下文裁剪前需要保存的值写盘；
- 忘了自己有哪些可加载工具/技能；
- 压缩后忘了当前计划；
- 用户走开后无限停泊。

ReminderHub 在这些已知时点注入短上下文，而不是把全部状态常驻系统提示。

### 4.2 九个注入点

`ReminderPoint`：

| 时点 | 触发 |
|---|---|
| `SESSION_START` | 会话启动/首条消息 |
| `USER_PROMPT_SUBMIT` | 每条外部消息 |
| `POST_TOOL_USE` | 工具批执行后（骑在工具结果尾部） |
| `TOOL_BUDGET_CHECKPOINT` | 跨过每回合工具预算倍数 / 硬停 |
| `PRE_COMPACT` | 压缩前 |
| `POST_COMPACT` | 压缩后（重新宣告被摘要丢掉的东西） |
| `STOP` | 文本回合结束、即将停泊 |
| `IDLE_TICK` | 后台 ticker 定时 |
| `STREAM_STALLED` | 流看门狗判定卡住 |

两种性质：

- **生命周期点**：loop 同步调用 `fire()`，自己把 `<system-reminder>` 放进对话；
- **时间点**：hub 的 ticker 调用 `collect()`，把提醒放入 pending，loop 在迭代边界 `take_pending()` 取出，避免写竞争。

### 4.3 当前注册的源

| Source | 适用 | 时点 | 内容 |
|---|---|---|---|
| `tasks` | 有任务工具的 Agent | START / USER / POST_TOOL / POST_COMPACT / STOP | 计划快照、漂移提醒、无计划提醒 |
| `memory_index` | Queen | START / USER / POST_COMPACT | 记忆文件清单（名 + 一行描述），增量 |
| `memory_recall` | Queen（配置为 wait/background） | START / USER / POST_COMPACT | 模型挑选的记忆内容 |
| `past_conversations` | Queen | START / USER | BM25 召回的 6 段历史对话 |
| `turn_context` | Queen | START / USER | 外部包注入的上下文 |
| `active_workers` | Queen | USER | 在飞 Worker 列表（防重复派发） |
| `colony_worker_snapshot` | Colony Queen | BUDGET_CHECKPOINT | Worker 舰队快照（最多 8 个） |
| `tracker_snapshot` | Colony Queen | POST_TOOL | Tracker 表与行数（最多 10 表，50ms 查询预算） |
| `worker_tracker_snapshot` | Worker | POST_TOOL | 每 20 次工具调用一次 Tracker 快照 |
| `colony_parallel` | Colony Queen | POST_TOOL | 建议并行扇出的 nudge（冷却 4 回合，会话上限 4） |
| `searchable_tools` | Queen / 分层 Worker | START / USER / POST_TOOL / POST_COMPACT | 可搜索工具的增删清单 |
| `skills_catalog` | Queen / Worker | 同上 | 可用技能目录 |
| `idle_nudge` | 所有 | IDLE_TICK | 停泊/停顿唤醒 |
| `stream_stall` | 所有 | STREAM_STALLED | 流卡住续写 |
| `sentinel_escalation` | 开启 Sentinel 的 Colony Queen | IDLE_TICK | 继续/完成/找人三选一 |

### 4.4 Hub 的工程约束

- **按角色绑定一次**：`bind(agent_ctx)` 用 `applies_to` 过滤；过滤函数抛异常时**保留**该源（fail-open），避免一个坏过滤器悄悄丢提醒。
- **单写者**：时间点提醒只进 pending，由 loop 在迭代边界 drain；ticker 从不直接写对话。
- **异常隔离**：任何源 render 失败只记 debug，不影响循环。
- **统一包装**：`<system-reminder>...\n\nOnly act on this if relevant... NEVER mention this reminder to the user.\n</system-reminder>`。
- **energizes 策略**：默认提醒不会唤醒已停泊的 Agent；只有明确需要 Agent 行动、而用户又无法提供的提醒才 `energizes=True`（例如 CRM reveal）。
- **冷却与上限**：各源自管（任务提醒 10 回合冷却、漂移软上限 2；Tracker 快照 Queen 首触发 3 回合、之后 10/20 回合；idle 每用户响应周期有限次数）。

### 4.5 设计评价

ReminderHub 是 Hive 最有辨识度的机制：它把“长期上下文管理”从**扩大窗口**转成**在正确时刻说正确的一句话**。它也是最大的隐性复杂度：

- 行为效果依赖提示措辞与模型遵从度；
- 多个源可能在同一时点叠加，导致提示膨胀；
- 源之间的优先级没有统一调度，只有各自冷却；
- 很难写端到端断言，测试多为“渲染结果包含 X”。

## 5. 会话存储：写穿与恢复

### 5.1 布局

`storage/conversation_store.py`：

```text
<session>/conversations/
├── meta.json          # 当前节点配置
├── cursor.json        # 迭代、输出、待输入、停止标记、累计工具调用
├── parts/
│   ├── 0000000000.json
│   ├── 0000000001.json
│   └── ...
└── partials/          # 流式中间快照（崩溃恢复用）
```

每个 part 是一条消息（user / assistant / tool_result），带 `seq`、`phase_id`、`run_id`、`is_system_reminder`、`is_client_input`、`is_skill_content` 等标记。

### 5.2 为什么“一条消息一个 JSON”

- 追加写简单，崩溃只丢最后一条；
- 可以按 `seq` 删除/压缩旧段，不需要重写整个文件；
- 便于人工检查与工具处理；
- 代价是 inode 数与读取时的多次小 IO；会话很长时压缩/清理是必要的。

### 5.3 写穿

- 每条消息写入后立即落盘；
- 游标在关键节点写（每轮、停泊、压缩前后）；
- `partials/` 保存流式中间态，进程崩溃后可判断“最后一条 assistant 是否完整”；
- 恢复时若发现悬空 tool_use，会补一条中断结果。

### 5.4 会话元数据

`meta.json` 里除标题外，还有：

- `agent_path` / `agent_name`（Colony 归属）；
- `queen_id`；
- `forked_from`（分支来源）；
- `colony_fork`（是否 Colony overseer）；
- `colony_spawned` / `spawned_colony_id`（DM 是否已建群并锁定）；
- `compact` 相关标记；
- `title` / `title_source`（auto/user）。

### 5.5 迁移

- `migrate_v2.py`：旧会话格式迁移；
- `migrate_v3.py`：`agents/queens/<q>` → `queens/<q>`，`data/tracker.db` → `tracker/tracker.db`，Worker 会话归位；
- 迁移是幂等的，有 `.migrated-v3` 标记；只迁移真实旧布局，不误伤新目录。

## 6. 崩溃恢复的完整链路

```text
进程崩溃
  → 重启 hive serve
  → SessionManager 扫描 live/cold 会话
  → 打开会话目录
  → FileConversationStore 读 parts + cursor
  → 若有 pending_input：恢复对应停泊（问题/凭据/建群）
  → 若 user_stopped：保持停泊，不自动运行
  → 若最后回合未完成：标记 COLD_INTERRUPTED，等待用户消息
  → 触发器的 last_fired_at 与 next_due_at：计算错过的 tick；当前实现静默跳过（把 last_fired_at 设为 now），不发 MISSED_TRIGGERS
```

这套链路的关键不是“自动继续”，而是**不丢状态、不误继续、让人决定**。这是 Hive 对无人值守场景的基本态度。

## 7. Colony DB：与业务 Tracker 分离

`host/colony_db.py` 管理 `colonies/<name>/colony.db`，存框架簿记：

- Playbook 运行记录（`playbook_runs`：run_id、status、result_json、时间）；
- 其他不进入业务 Tracker 的元数据。

这样“业务事实”和“运行日志”不会混在同一张表里，导出/审计边界清晰。

## 8. 这一层的评价

**优点**

- 四种基底职责清晰：Plan 意图、Tracker 事实、EventBus 信号、Reminder 注意力。
- Tracker 的 binding + 注册表 + denylist，在“给 Agent 原生 SQL 自由”与“防止写坏框架/越界”之间取得了不错的平衡。
- 写穿 + 游标 + 停泊原因的恢复模型，是长任务可靠性的核心。

**风险**

- Tracker 的 schema/索引质量无人校验，Queen 的 SQL 能力直接决定 Colony 的可审计性。
- Reminder 源多且提示化，行为难以单测；提示词改动可能引发系统性行为漂移。
- 单进程 EventBus + 内存历史意味着水平扩展需要外部改造（多实例共享会话目前不在设计内）。
- 一条消息一个 JSON 在小文件密集场景有 IO 放大；超大会话需要依赖压缩与清理。
