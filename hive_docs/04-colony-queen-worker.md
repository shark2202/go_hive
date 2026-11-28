# 04 · 组织模型：Colony / Queen / Worker

> 关键文件：
> `core/framework/host/colony_runtime.py`（2,935 行）
> `core/framework/host/worker.py`
> `core/framework/tools/queen_lifecycle_tools.py`（4,000 行）
> `core/framework/agents/queen/worker_definition.py`
> `core/framework/server/queen_orchestrator.py`

## 1. ColonyRuntime：Worker 的宿主

`ColonyRuntime` 的模块注释定义了整个设计：

> Each worker is an exact copy of the queen's AgentLoop — same tools, same prompt, same LLM.
> Workers run independently and report results back to the queen via the event bus.
> There are no graphs, no edges, no nodes, no data buffers.
> Just: spawn N independent clones, let them run, collect results.

它负责：

- 注册、启动、停止 Worker；
- 并发上限与待运行队列（`_pending_queue`）；
- 软超时 / 硬超时看门狗（`watch_batch_timeouts`）；
- 触发器（`TriggerSpec`）的注册与调度；
- 与 `EventBus`、`tracker.db`、`ColonyBinding` 接线；
- Worker 状态查询、停止、恢复、结果保留。

### 1.1 默认参数

| 参数 | 默认 | 说明 |
|---|---:|---|
| `max_concurrent_workers` | **4** | `HIVE_MAX_CONCURRENT_WORKERS` 可覆盖 |
| `adaptive_tool_budget` | **True** | `HIVE_ADAPTIVE_TOOL_BUDGET=0` 关闭 |
| `cache_ttl` | 60s | 缓存 |
| `batch_interval` | 0.1s | 调度批间隔 |
| `max_history` | 1,000 | 事件历史 |
| `result_retention_max` | 1,000 | Worker 结果保留上限 |
| `idempotency_ttl` | 300s | 幂等键 |
| `max_resurrections` | 3 | 自动复活次数 |
| `STOP_TIMEOUT_SEC` | 10s | 单个 Worker 停止宽限 |

超过并发上限的任务不会丢：它们在 `_pending_queue` 里等待，前面 Worker 终止后按序提升。

## 2. Queen：长期存在的负责人

### 2.1 13 个内置人格

Queen 不是“通用调度器”，而是带领域判断的人格。每个是 `queen_defaults/<queen_id>.yaml`：

| Queen ID | 人格名 | 职位 | 默认工具类别（摘要） |
|---|---|---|---|
| `queen_growth` | Steve Jobs | Head of Growth | email、file、terminal、browser、research、charts、media |
| `queen_sales` | Larry Ellison | Head of RevOps | email、file、terminal、browser、research、charts、media |
| `queen_content_creation` | Gary Vaynerchuk | Head of Content | email、file、terminal、browser、research、media |
| `queen_lead_gen` | Alex Hormozi | Head of Lead Generation | email、file、terminal、browser、research、charts、media |
| `queen_outbound` | Chris Voss | Head of Outbound | email、file、terminal、browser、research、charts、media |
| `queen_brand_design` | Jony Ive | Head of Brand & Design | email、file、terminal、browser、research、media |
| `queen_technology` | Elon Musk | Head of Technology | email、file、terminal、**code_editing**、browser、research、charts、media |
| `queen_operations` | Tim Cook | Head of Operations | email、file、terminal、**spreadsheet**、browser、charts |
| `queen_market_research` | Mary Meeker | Head of Market Research | email、file、terminal、**spreadsheet**、browser、charts |
| `queen_product_strategy` | Steve Jobs | Head of Product Strategy | email、file、terminal、browser、research、charts、media |
| `queen_finance_fundraising` | Warren Buffett | Head of Finance | email、file、terminal、**spreadsheet**、browser、charts |
| `queen_legal` | Ruth Bader Ginsburg | Head of Legal | email、file、terminal、browser、research |
| `queen_talent` | Laszlo Bock | Head of Talent | email、file、terminal、browser、research |

新用户默认激活 **6** 个（`frontend/src/lib/colony-registry.ts:158`）：Growth、RevOps（`queen_sales`）、Content、Lead Gen、Outbound、Brand & Design。其余从 Org Chart “雇用”。

> 注意：`queen_growth` 与 `queen_product_strategy` 使用同一个“Steve Jobs”人格名，但领域与工具配置不同。这不是错误，是刻意复用原型。

### 2.2 两阶段

`QUEEN_PHASES = {"independent", "colony"}`（`queen_lifecycle_tools.py:84`）。旧值 `working` / `reviewing` 归一化为 `colony`，`incubating` 归一化为 `independent`。

| 阶段 | 工具面 | 行为 |
|---|---|---|
| Independent | `suggest_colony`、任务工具、`write_skill`、`search_tools`、Queen Mail、全部动态 MCP 工具 | 自己做事；发现并行/周期/长时任务时提议建群 |
| Colony | 增加 `run_worker`、`run_playbook`、`stop_worker`、Tracker 工具、Trigger、Worker 监控 | 扇出、验收、写技能、编排 |

两个阶段都保留任务、技能、搜索工具、Queen Mail；区别只在编排工具与 Tracker 写权限。

### 2.3 循环配置

来自 `agents/queen/agent.py`：

| 配置 | Independent | Colony |
|---|---:|---:|
| `max_iterations` | 999,999 | 999,999 |
| `tool_call_budget` | 30 | **50** |
| `tool_call_hard_multiple` | 5（硬停 150） | **10（硬停 500）** |
| `max_context_tokens` | 180,000 | 180,000 |
| `skip_judge` | True | True |

`HIVE_MAX_ITER` 可把无限循环压成有限回合（例如单次聊天部署）。

### 2.4 Queen 的提示结构

Queen 的 system prompt 不是单一字符串，而是：

- **静态块**：身份、人格、角色、工具说明、协议；
- **动态后缀**：时间、召回记忆、经验、任务/Worker/Tracker 提醒；
- 两者用 provider 的 `cache_control` 断点分隔（Anthropic），以保持前缀缓存。

记忆召回、时间线、技能目录、工具清单都可以在每轮动态刷新，而不重写静态前缀。这是长会话成本控制的关键。

### 2.5 Queen 拥有的东西

- **对话**：唯一面向用户的界面；
- **计划**：文件型任务列表（Action Plan），跨重载存活；
- **Tracker**：建表、声明可写列、SQL 验收；
- **提问权**：`ask_user` 停泊等待；
- **建群权**：`suggest_colony` 必须用户确认；
- **技能作者权**：`write_skill`（colony / queen / global 三种 scope）；
- **调度权**：`set_trigger` 定时/ Webhook；
- **Worker 控制**：停止、恢复、注入消息（`inject_message`）、查看状态。

## 3. Worker：窄而可靠的工作单元

### 3.1 它是什么

`agents/queen/worker_definition.py` 定义：

- **Focused**：一个任务，严格在范围内；范围外的事只在报告里提一句。
- **Ephemeral**：无长期记忆、无人格，每次全新。
- **No escalation**：没有实时听众，不能问 Queen 或用户；阻塞就保存已有结果并报告。
- **Can't delegate**：不能 spawn 其他 Worker；扇出只属于 Queen。
- **Fail-fast**：瞬时错误重试一次，结构性错误修正一次，不可修复就停止，不绕圈。

Worker 的系统提示还包含：

- 输出简洁、直奔结果；
- 写给人看的消息要有“人味”（不要 em dash、句式长短交错）；
- 多步任务用 `task_*` 自管理；
- 在上下文被裁剪前把 URL/ID/路径等关键值写进 Tracker 或自己的消息；
- 只调用一次 `report_to_parent` 然后停止。

### 3.2 预算

`DEFAULT_LOOP_CONFIG`（唯一事实源）：

```python
{
  "max_iterations": 3,
  "grace_iterations": 1,
  "tool_call_budget": 30,
  "tool_call_hard_multiple": 3,      # 30 × 3 = 90 硬停
  "tool_call_lifetime_budget": 200,
  "max_context_tokens": 180_000,
}
```

- **3 个工作回合 + 1 个宽限回合**；
- 宽限回合只允许 `report_to_parent`、`tracker_upsert`、`task_update`；
- 生命周期累计 200 次工具调用后强制进入宽限；
- 每回合软检查点 30/60，硬停 90。

> 文档漂移：`run_worker` 的工具描述仍写“生命周期默认 150”，实际是 200。

### 3.3 Queen 可以按批覆盖什么

**当前 `run_worker` 的公开参数**（`tools/queen_lifecycle_tools.py:1379`）只有：

| 参数 | 作用 |
|---|---|
| `tasks[]` | 每个任务可带 `task`、`data`、`goal`（UI 标题）、`preload_tools`、`profile_name` |
| `max_iterations` | 批级回合上限 |
| `tool_call_lifetime_budget` | 批级生命周期工具调用上限 |
| `timeout` | 软截止（默认 600s） |
| `resume_worker_ids` / `guidance` | 恢复模式 |

ColonyRuntime 内部还接受一份更宽的白名单（`colony_runtime.py:66`）：

| 字段 | 范围 |
|---|---|
| `max_iterations` | 1–1000 |
| `grace_iterations` | 0–3 |
| `tool_call_budget` | 0–200 |
| `tool_call_lifetime_budget` | 0–2000 |
| `max_context_tokens` | 1,000–1,000,000 |

但**不要把它当成 Queen 可用参数**：`run_worker` 在规范化任务时明确丢弃 per-task 的 `loop_config_overrides`（源码注释：异构批次通常是坏味道，应该拆成两批），只把批级的 `max_iterations` 与 `tool_call_lifetime_budget` 传到 `spawn_batch`。其余键服务内部调用/旧路径。

`tool_call_hard_multiple` **不允许**覆盖，框架锁定。

### 3.4 工具面

Worker 继承的不是“Queen 的全部工具”，而是：

```text
terminal_exec / terminal_rg / terminal_glob
set_output
report_to_parent
tracker_upsert / tracker_query
task_create / task_update / task_list / task_get
```

外加 Colony 动态注册的 MCP 工具（受 Queen 当前工具范围与凭据可用性过滤）。

Queen-only 工具会被 `_resolve_queen_only_tools()` 剥离：

- `run_worker` / `run_playbook` / `stop_*` 等编排工具；
- `suggest_colony`、`switch_to_*`；
- `write_skill`、`search_tools`、阈值/监控工具；
- `tracker_sql` / `tracker_register_writable`；
- Queen Mail 工具。

这样 Worker 既不能递归编排，也不能改 Queen 的阶段或 Tracker 结构。

### 3.5 生命周期状态

`WorkerStatus`：

```text
QUEUED → PENDING → RUNNING → COMPLETED / FAILED / STOPPED
```

- `QUEUED`：并发上限已满，等待槽位；
- `PENDING`：已注册，尚未启动；
- `RUNNING`：AgentLoop 执行中；
- `COMPLETED/FAILED/STOPPED`：终态。

`WorkerResult.status` 是另一层语义：`success | partial | failed | timeout | stopped`。

另有 `persistent=True` 模式：Worker 启动后常驻，通过 `inject(message)` 把消息泵进已运行的循环。它用于 Colony 的长期 overseer，不是并行扇出的常规路径。

### 3.6 报告

Worker 的终点是 `report_to_parent(status, summary, data)`：

- `success`：完成；
- `partial`：部分完成；
- `failed`：无法有效推进。

报告通过 `SUBAGENT_REPORT` 事件回到 Queen，`QueenOrchestrator` 格式化为 `[WORKER_REPORT]` 用户回合：

```text
[WORKER_REPORT]
worker_id: ...
status: success | partial | failed | timeout | stopped
summary: ...
data: {...}
batch_remaining: N
```

`batch_remaining` 统计“还在跑 + 还在排队”的同批 Worker；Queen 被明确要求：只有 `batch_remaining == 0` 才做最终验收，不要用轮询填补沉默。

### 3.7 超时

`run_worker` 的 `timeout` 是**软截止**（默认 600s）：

- 到点：所有仍活跃且未报告的 Worker 收到 `[SOFT TIMEOUT]`，要求立即 `report_to_parent`；
- 硬截止：`max(4 × soft, soft + 600)`，上限 **3600s**（`_compute_hard_timeout`）；
- 硬截止仍未报告：强制停止，合成 `timeout` 结果；
- 软窗口内提交的报告会保留。

### 3.8 恢复

`run_worker(resume_worker_ids=[...], guidance="...")`：

- 从 Worker 的保存对话恢复，继续未完成的回合；
- 可提高 `max_iterations` 给耗尽预算的 Worker 余量；
- `guidance` 作为一条用户回合注入，用于纠偏；
- 累计工具调用数从游标恢复，不会“重置预算”。

## 4. 自适应 Worker 预算

这是 Hive 比较少见的“运行时统计控制”：

- 只采样 **success 且未被预算截断** 的 Worker；
- 样本 ≥ 3 后，取中位数与 p90；
- 若 p90 > 4 × 中位数，说明工作异质，**不收缩**；
- 名义预算 = `max(ceil(2 × median), ceil(1.25 × max_success))`，下限 30，上限 profile 的 200；
- 只对 `PENDING` / `RUNNING` 且未被 Queen 显式 pin 的 Worker 应用；
- 收缩是统计性的；上调只依据“实际成功过的最大消耗”，防止死亡螺旋。

参数可用环境变量调：`HIVE_ADAPTIVE_BUDGET_MIN_SAMPLES`、`_FLOOR`、`HIVE_ADAPTIVE_TOOL_BUDGET`。

## 5. Colony 的创建（fork）

入口：`POST /api/sessions`，携带 `colony_id` + `source_session_id`；核心函数 `fork_session_into_colony`（`server/routes_execution.py:1804`）。

步骤：

1. **创建/复用 Colony 目录**；
2. **写 `worker.json`**：保存 Queen 当前的模型、系统提示、工具、技能、循环配置、`input_data`（含 `ColonyBinding`）；
3. **复制 Queen 会话**到 `colonies/<c>/queens/<q>/sessions/<new_sid>/`；
4. **后台压缩**继承对话（LLM 总结，180s 上限），失败则保留原始 transcript；
5. **复制 seed conversation** 到 `colonies/<c>/seed_conversation/`，作为 Worker 的起始上下文；
6. **初始化/迁移 `tracker/tracker.db`**，并把最新 binding 回写到所有 `worker.json`；
7. 返回 `colony_path`、`queen_session_id`、`is_new`、`compaction_status`。

创建完成后：

- 原 DM session 被标记 `colony_spawned` 并锁定；
- 前端跳到 `/colony/<id>`；
- Queen 的工具面切换到 Colony 阶段。

### 5.1 Worker Profiles

一个 Colony 可以有多个 Worker 模板（`host/worker_profiles.py`）：

```text
colonies/<c>/worker.json                  # 默认 / legacy profile
colonies/<c>/profiles/<name>/worker.json  # 命名 profile
colonies/<c>/metadata.json                # worker_profiles: [...]
```

`WorkerProfile` 字段：

- `name`、`task`、`skill_name`；
- `integrations`: `{provider: alias}`，为 Worker 绑定账号（如 `slack: work`）；
- `concurrency_hint`；
- `prompt_override`、`tool_filter`；
- `browser_profile`: 指定 Chrome Profile 标签，让同一 Colony 驱动多个已登录账号。

## 6. Colony 的元数据与配置

| 文件 | 作用 |
|---|---|
| `metadata.json` | 来源 Queen、fork session、创建时间、worker profiles、是否启用自适应预算 |
| `worker.json` / `profiles/*` | Worker 模板（运行时身份 + 序列化格式） |
| `tools.json` | Colony 级工具允许列表（用户可编辑） |
| `skills_overrides.json` | Colony 级技能启停覆盖 |
| `triggers.json` | 定时/ Webhook 触发器定义 |
| `notifications.json` | Sentinel 渠道与升级配置 |
| `colony.db` | Playbook run log、框架簿记（与业务 Tracker 分离） |

## 7. 监控与控制工具

Queen 在 Colony 阶段可用：

| 工具 | 用途 |
|---|---|
| `get_worker_status` | 单个/批量 Worker 状态；`focus="full"` 含浏览器标签 |
| `get_worker_health_summary` | 全 Colony 健康摘要 |
| `stop_worker` | 停止单 Worker 或整批；可先要求 `report_to_parent` |
| `inject_message` | 向运行中的 Worker 注入消息（不重启） |
| `run_worker` | 扇出/恢复 |
| `run_playbook` / `stop_playbook` / `get_playbook_status` / `list_playbook_runs` | Playbook 编排 |

Worker 不能反问 Queen；`inject_message` 是 Queen 单向的“纠偏”，不是问答通道。

## 8. 设计评价

**优点**

- “Queen + 克隆 Worker”模型极简：没有图、没有边，调试时可以逐个 Worker 看对话。
- Worker 的窄工具面、无记忆、无升级，显著降低并行冲突面。
- 软/硬超时 + 可恢复 + 报告兜底，使扇出在工程上可控。
- 自适应预算用实际成功样本约束规模，避免“一个坏 Worker 拖垮整批”。

**风险**

- Worker 之间没有协调原语；全靠 Queen 在任务文本里做**不重叠切片**。切片错了就双写/碰撞。
- Queen 的编排质量高度依赖提示词与模型能力；弱模型容易重复派发、过早验收。
- `run_worker` 一次可提交很多任务，但没有硬性上限（除并发与预算），批量规划失误会放大成本。
- Worker 的“无记忆”意味着同一上下文在每个 Worker 里重复付费；工具描述建议“5–10 个同质单元/Worker”正是为此。
