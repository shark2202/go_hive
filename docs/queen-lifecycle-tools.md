> 触发器专题页（timer/webhook scheme 与实现）：`docs/triggers-webhook.html`
> Webhook 使用手册（创建/测试/运维/排障）：`docs/webhook-guide.html`
# Queen lifecycle 工具对齐说明（10 个 agent 工具）

上游 `core/framework/tools/queen_lifecycle_tools.py::register_queen_lifecycle_tools` 注册 12 个
Queen 生命周期工具。Go 侧此前只有 `run_worker`（`internal/colony`）与 `write_skill`
（`internal/skills`），其余 10 个只有 REST/运行期能力，模型无法直接调用。本次补齐后，
12 个工具全部在 **queen 聊天会话**（`hive serve` 创建/恢复/预加载会话）的工具表里可达。

`go_hive/docs/tool-parity-matrix.html` 是生成页，本次不手改；由 integrator 重跑矩阵。

## 1. 注册位置与工具面边界

| 位置 | 内容 |
| --- | --- |
| `internal/server/queen_lifecycle_tools.go` | 10 个工具定义（`QueenLifecycleToolDefs`）、`QueenLifecycleBinding` 绑定与分发 |
| `internal/server/queen_lifecycle_worker_status.go` | `get_worker_status`（summary / activity / memory + 冷却 / 门控） |
| `internal/server/queen_lifecycle_worker_status_full.go` | `get_worker_status` 的 tools / issues / progress 分节与 `focus='full'` JSON |
| `internal/server/queen_lifecycle_workers.go` | `stop_worker`、`inject_message`、`update_worker_profile`、`list_browser_profiles` |
| `internal/server/queen_lifecycle_questions.go` | `list_worker_questions`、`reply_to_worker`（+ Session 收件箱顺序/出队方法） |
| `internal/server/queen_orchestrator.go` | `InstallWorkerEscalationRouting`（colony 作用域订阅 → 收件箱 + `[WORKER_ESCALATION]` 注入） |
| `internal/server/session_chat_colony.go` | `BindChatColony` 幂等安装/卸载升级订阅（生产调用点；见 §2.1） |
| `internal/agentloop/escalate.go` | legacy `worker` 流的 `escalate` 执行（发事件、停车等女王、收线重发）+ `escalateDoomLoop`（parallel worker 的自动升级生产者） |
| `internal/colony/worker_escalation.go` | 事件生产：`ESCALATION_REQUESTED` 的 colony 戳 + `request_id` |
| `internal/server/queen_lifecycle_triggers.go` | `set_trigger`、`list_triggers`、`remove_trigger` |
| `internal/server/queen_worker_monitoring.go` | `get_worker_health_summary`（`worker_monitoring_tools.py`；定义/返回逐字，读 colony 的 worker run 记录） |
| `internal/cli/session_tools_queen_lifecycle.go` | `enableQueenLifecycleTools`：把 10 个定义 + `get_worker_health_summary` 挂到会话 catalogue |
| `internal/cli/loader.go` / `loader_preloaded_session.go` | 仅在 queen 聊天会话上启用（与 `enableQueenMailTools` 并列） |
| `internal/server/session.go` | `PendingEscalations` 到达顺序（`pendingOrder`）、`WorkerHandoffRuntime`（订阅所属总线） |

工具面规则（对齐上游 `_QUEEN_INDEPENDENT_TOOLS` / `_QUEEN_COLONY_TOOLS`）：

* **colony 门控**：`run_worker`、`get_worker_status`、`inject_message`、`stop_worker`
  只在绑定 colony 后出现在 `toolsForTurn` 的定义列表里（`colonyBoundQueenTool`）；无 colony
  时直接调用仍返回工具自身的明确错误，不伪造结果。
* **queen 专属**：其余 6 个（worker questions、triggers、worker profile、browser profiles）
  常驻 queen 会话工具表；它们的错误语义覆盖“无 colony / 无收件箱”等场景。
* **worker 工具面不多给**：`enableQueenLifecycleTools` 只对 queen 会话调用；colony worker 的
  catalogue（`colonyWorkerToolSurface`）不启用，测试
  `TestWorkerCatalogueExcludesQueenLifecycleTools` 断言 10 个名字都不出现。
* **分类补齐**：`internal/playbook/rows.go::ColonyQueenTools` 补入上游 COLONY 阶段列出的 6 个
  （`get_worker_status`、`inject_message`、`stop_worker`、`set_trigger`、`remove_trigger`、
  `list_triggers`），使 worker 剥离分类与上游一致。
* overseer 工具面**未新增**：Go overseer 运行在 colony 运行时内，没有会话级 escalation 收件箱 /
  trigger 状态，给它会得到无法执行的工具；上游 overseer 拿的是 queen 注册表整体，这一点在本
  port 保持现状并在下文“未对齐项”记录。

## 2. 复用的既有存储/运行期（未重写）

| 能力 | 复用点 |
| --- | --- |
| worker 注册表 / 停止级联 | `internal/colony`：`WorkerList`、`GetWorker`、`InjectInput`、`StopWorkers`、`WaitForWorkerReports`、`ListWorkers` |
| playbook 收敛循环取消 | `internal/playbook::StopPlaybooksForColony` |
| worker 提问收件箱 | `server.Session.PendingEscalations`（生产由 `BindChatColony` 安装的 `InstallWorkerEscalationRouting` 写入）、`PendingEscalation/TakePendingEscalation` |
| trigger 存储与运行期 | `server.Session` 的 `AvailableTriggerDefs`/`ActiveTriggers` + `trigger_runtime.go`（`StartTriggerTimer`、`StartTriggerWebhook`、`PersistActiveTriggers`、`saveTriggerToAgent`、`triggers.RemoveTriggerFromAgent`） |
| worker profile 注册表 | `routes_colonies_profiles.go`：`listWorkerProfilesWithHome`、`upsertWorkerProfile`、`validateWorkerProfileName`（写 `colonies/<id>/metadata.json`） |
| 浏览器桥探测 | `bridgeHTTPRequest` + `bridgeResponseBody`（与 `routes_app_extra.go` 的 app.py 探针同一实现） |

### 2.1 Worker escalation 生产链路

上游 `framework/server/queen_orchestrator.py` 在 queen 会话的 colony 阶段安装
`install_worker_escalation_routing`。本移植此前只有工具与 handler、没有生产调用点，导致
`list_worker_questions` / `reply_to_worker` 生产恒空。现在链路是（上游为 parallel worker
保留的 doom-loop 生产者）：

```text
worker 连续重复同一工具调用 ×3（DoomLoopDetector，默认阈值 3）
  → AgentLoop.escalateDoomLoop：发 ESCALATION_REQUESTED（reason="Tool doom loop detected"，
    colony 戳 + request_id），工具结果尾部追加提醒与
    "[SYSTEM] Escalated tool doom loop to queen for intervention."；worker 继续（不等待）
  → ColonyRuntime.requestWorkerEscalation：总线发布（colony_id / node_id / execution_id / reason / context）
  → Session.BindChatColony 安装的 colony 作用域订阅：写 Session.PendingEscalations（按到达顺序）
    + 向 chat queen 注入 [WORKER_ESCALATION]（非 client input，转录落盘）
  → queen 回合调用 list_worker_questions（读真实收件箱）
  → queen 调用 reply_to_worker → ColonyRuntime.InjectInput(worker, "[QUEEN_REPLY] request_id=…\n…")
  → worker 仍在运行：回复作为 client user 回合入会话，worker 继续（通常 report_to_parent 收尾）；
    worker 已停车/终止：工具返回 worker_not_active，条目仍出队
```

- **为什么不是 escalate 工具**：上游 1.0.14 的 `escalate` 只发给 legacy primary worker
  （`stream_id="worker"`，无冒号）；`run_worker` 的 `worker:<id>` 是 parallel worker，工具面
  只有 `report_to_parent`，失败即终态。本 port 全部 worker 都是 `worker:<id>`，故对齐为
  “无 escalate 工具 + doom-loop 自动升级”，并在分发层同时拒绝 `worker:<id>` 的 escalate
  （防幻觉调用把 fail-fast worker 停泊）。详见 `worker-escalation-e2e.md` §1。
- **安装/卸载**：`BindChatColony` 每次会话查找都会同步（幂等）；colony 卸载（`UnloadColony`）与
  `StopSession` 会从**订阅所在总线**（colony 总线优先，session 总线兜底）退订。
- **背压**：收件箱 ≥ `MaxPendingEscalations`（32）时自动回 worker
  `[QUEEN_REPLY] queue_full …`，不让 worker 永久卡住（与上游一致）。
- **收线重发**：legacy `worker` 流的停车在运行取消时再发一条
  `Blocked waiting for queen guidance - no input received` 的升级；`worker:<id>` 的 doom-loop
  升级是信息性的（worker 不等待），该分支不适用于 colony worker。
- **持久化口径**：收件箱与上游一样是**内存字典**（不落盘）；durable 的一半是 queen 转录里的
  `[WORKER_ESCALATION]` 记录。真实端到端见 `worker-escalation-e2e.md`。

## 3. 工具、参数与返回形状

| 工具 | 参数（必需加粗） | 成功返回 |
| --- | --- | --- |
| `get_worker_status` | `focus?`（enum activity/memory/tools/issues/progress/full）、`last_n?` | 默认散文摘要；focus 分节文本；`full` 为 JSON（`worker_colony_id`、`worker_goal`、`status`、`active_executions`、`workers`、`pending_question`、`running_tools`、`recent_tool_calls`、`node_transitions`、`retries`、`issues`、`subagent_activity`、`constraint_violations`、`token_summary`、`execution_outcomes`、`agent_idle_seconds`） |
| `inject_message` | **`content`** | `{status:"delivered", node_id, worker_ids, delivered_count, content_preview}`（多 worker 时 `node_id` 为空串，另给 `worker_ids`） |
| `stop_worker` | `reason?`、`grace_seconds?` | `{status:"stopped"\|"no_active_workers", workers_stopped, queued_stopped, playbooks_stopped, reports, timers_paused, reason, errors}` |
| `list_worker_questions` | – | `{count, pending:[{request_id, worker_id, colony_id, node_id, reason, context_preview, waiting_seconds}]}`（按到达顺序） |
| `reply_to_worker` | **`request_id`**、**`reply`** | `{status:"delivered"\|"worker_not_active", worker_id, request_id}` |
| `set_trigger` | **`trigger_id`**、`trigger_type?`、`trigger_config?`、`task?` | `{status:"activated", trigger_id, trigger_type, trigger_config}`；webhook 另带 `webhook_url` |
| `remove_trigger` | **`trigger_id`** | `{status:"deactivated", trigger_id}` |
| `list_triggers` | – | `{triggers:[{id, trigger_type, trigger_config, description, task, enabled}]}` |
| `update_worker_profile` | **`colony_id`**、**`profile_name`**、`integrations?`、`task?`、`skill_name?`、`concurrency_hint?`、`prompt_override?`、`tool_filter?`、`browser_profile?` | `{ok:true, colony_id, profile_name, worker_profiles:[…]}` |
| `list_browser_profiles` | – | 桥可达：`{ok:true, profiles:[{label,is_default,starred,version,protocol_version}], hint?}`；不可达：`{ok:false, error:"browser bridge not reachable", hint, profiles:[]}` |
| `get_worker_health_summary` | `session_id?`、`last_n_steps?`（默认 40） | JSON：`{worker_agent_id, worker_graph_id, session_id, session_status, total_steps, recent_verdicts, steps_since_last_accept, last_step_time_iso, stall_minutes, evidence_snippet}`（字段与顺序逐字；`session_id` 省略或 `auto` → 优先 queen 自己的 session id，否则取最近 active 的 worker session） |

## 4. 错误语义（对齐上游文案）

* `get_worker_status`：无 colony → `No colony running.`；无 worker → `No workers loaded.`；未知
  focus → `Unknown focus '<x>'. Valid options: activity, memory, tools, issues, progress, full.`；
  冷却期内 → `{"status":"cooldown","message":"Status '<tier>' was checked Ns ago. Wait Ms …"}`
  （summary 免费，detail 10s，full 30s）。
* `inject_message`：无 colony → `{"error":"No colony running in this session."}`；无活动 worker →
  `{"error":"No active graph node found — graph may be idle."}`。
* `reply_to_worker`：无 colony / 无收件箱 / 未知 request_id / 条目缺 worker_id 各有独立错误；
  投递失败（worker 已结束或未知）返回 `worker_not_active`，**条目仍然出队**。
* `stop_worker`：无运行期 → `{"error":"No runtime on this session."}`；`grace_seconds` 夹取到
  `[0,25]`（默认 12），避免超出工具调用预算造成“超时→重试”循环。
* `set_trigger`：已激活 / 未找到且未给 type+config / 缺 task / webhook path 非 `/` 开头 /
  非法 HTTP 方法 / 不支持的 type / 非法 cron / `interval_minutes` 非正值 / 缺 `cron` 与
  `interval_minutes` —— 文案逐条对齐上游。
* `remove_trigger`：未激活 → `Trigger '<id>' is not active.`。
* `update_worker_profile`：colony_id 非 `^[a-z0-9_]+$`、profile 名非法、colony 不存在 —— 分别
  返回上游同款文案（profile 名校验与 `worker_profiles.validate_profile_name` 一致）。
* `list_browser_profiles`：**不伪造 profile**——桥不可达时 `ok=false` + `profiles: []`；桥可达但
  无已连接扩展时 `ok=true` + 空列表 + 提示文案。

## 5. 测试

| 测试文件 | 覆盖 |
| --- | --- |
| `internal/server/queen_lifecycle_tools_test.go` | 定义/schema、colony 门控、`get_worker_status`（无 colony、空 colony、6 个 focus、冷却、未知 focus）、`inject_message`（无 colony/空闲/成功）、`stop_worker`（无运行期/成功/二次 no_active_workers）、worker questions 闭环（列出→回复→出队、未知 id、worker_not_active、缺 worker_id、无 colony、无收件箱） |
| `internal/server/worker_handoff_test.go` | 注入形状（`[WORKER_ESCALATION]` 文本、非 client input、收件箱落条）、queen 流忽略、queen 不在线时回落 client input、Stop 退订 |
| `internal/server/worker_escalation_chain_test.go` | 生产链路：真实 colony runtime + 脚本化 worker（doom-loop 触发 → `ESCALATION_REQUESTED` → 收件箱 → `reply_to_worker`）：worker 存活时 delivered 并恢复、已停车时 `worker_not_active`；`BindChatColony` 安装/卸载路由（卸载后事件不进 queen、不进收件箱） |
| `internal/agentloop/escalate_test.go` | legacy `worker` 流的 escalate 停车/唤醒与回复入会话、queen/judge/overseer 拒绝、`worker:<id>` 分发拒绝（不产生事件、不停车）、无 sink 拒绝、收线重发 |
| `internal/colony/worker_escalation_test.go` | worker 工具面不含 `escalate`（含 `report_to_parent`；overseer 两者都不含）、事件带 colony 戳与 `request_id`、doom-loop 升级一次 → 注入回复 → worker 继续并 report、无重复调用时不产生事件 |
| `internal/server/queen_lifecycle_actions_test.go` | trigger CRUD（timer 激活/持久化/列出/移除 + 全部分支错误 + webhook 激活与取消订阅清理）、`update_worker_profile`（新增、局部更新保字段、空值清除、3 条错误）、`list_browser_profiles`（不可达 / 有 profile / 空 profile）、chat runtime 门控、取消上下文下 stop 仍生效 |
| `internal/server/queen_worker_monitoring_test.go` | `get_worker_health_summary` 定义逐字（description/parameters vs Python 源码文本）、colony 门控、colony 布局读取（workers/&lt;id&gt; state.json + transcript 的 steps/evidence/timing）、upstream 布局读取（sessions/&lt;id&gt;/logs/tool_logs.jsonl 的 verdicts/stall/截断 + `last_n_steps` 窗口）、自动发现（queen 自身 session id 优先；否则最近 active）、空存储与无 colony 的 error envelope |
| `internal/cli/session_tools_queen_lifecycle_test.go` | catalogue 注册（启用前不可见、启用后 10 个全在 + `get_worker_health_summary`）、经 catalogue 执行、worker catalogue 不含这 11 个工具 |
| `e2e/worker_escalation_e2e_test.go`（`-tags e2e`） | 真实 `hive serve` + 真实 colony：worker 重复工具调用触发 doom-loop 升级 → queen `list_worker_questions` → `reply_to_worker` → worker 继续并 report；恰好 1 条 `[WORKER_ESCALATION]`；证据落盘 `docs/e2e-assets/worker-escalation-e2e/` |

Parity 注解：生产文件指向 `framework/tools/queen_lifecycle_tools.py::<symbol>`；测试指向
`tests/test_trigger_fires_into_queen.py::test_colony_tools_include_monitoring_and_review`、
`…::test_remove_trigger_cleans_up_webhook_subscription`、
`tests/test_session_manager_worker_handoff.py::test_worker_handoff_injects_addressed_request_into_queen`。
上游没有测试的 3 个工具（`update_worker_profile`、`list_browser_profiles` 及若干 port 特有断言）
用 `Parity-exempt` 注明原因。

## 6. 未对齐项（已知差异，均为显式记录）

1. **`list_browser_profiles`**：按上游语义探测本地桥（`HIVE_BRIDGE_PORT` 默认 14829 + 1，再试
   9230），复用主模块的 HTTP-over-TCP 探针。无桥时返回 `ok=false` + 空列表；**不会**用
   `bridges/` 或其它本地状态合成 profile。Go 侧 browserbridge 是独立 module，主模块不依赖它，
   因此这里始终是网络探测而非同进程查询。
2. **`get_worker_status` 的 memory focus**：Go worker 是隔离 agent loop，没有框架级 shared
   buffer，故返回明确说明（改用 `tracker_query`），不编造 buffer 内容。
3. **`get_worker_status` 数据面**：上游读单个 AgentHost graph 的执行/节点事件；本 port 读
   worker 注册表 + 会话事件总线（桥接自 colony 总线），因此 `full` 多出 `workers` 数组，
   `agent_idle_seconds` 固定 `-1`（无 per-agent 空闲时钟）。
4. **`inject_message` 目标**：上游注入“当前 graph 的活动节点”；本 port 注入所有活动 worker 并
   回报 `worker_ids`（colony 无单一 graph）。
5. **`stop_worker` 的 `timers_paused`**：上游暂停 legacy AgentHost 定时器；本 port 暂停会话的
   timer trigger（取消 timer task，保留定义与激活标记，重启后 `RestoreSessionTriggers` 恢复），
   仅在确实取消了定时器时为 `true`。webhook 订阅不在此列（与上游只暂停 cron 一致）。
6. **`list_worker_questions` / `reply_to_worker`**：上游把两者从 phase 列表移除（worker 无工具面
   升级通道），但仍注册在 queen 注册表；本 port 与上游注册面一致（常驻 queen 会话工具表），由
   上游保留的 doom-loop 升级收件箱驱动，行为按 `reply_to_worker` 文档实现。收件箱的生产写入方是
   `BindChatColony` 安装的 colony 作用域订阅（§2.1）；**工具面已对齐上游 1.0.14**：`escalate`
   只发给 legacy primary worker（`stream_id="worker"`），parallel worker（`worker:<id>`）工具面
   只有 `report_to_parent`，失败即终态；本 port 的 `run_worker` worker 全是 `worker:<id>`，因此
   不再广告 `escalate`，并在分发层拒绝 `worker:<id>` 的幻觉调用（比上游分发门控更严格，防把
   fail-fast worker 停泊）。worker 的升级生产者为 doom-loop 自动升级（唯一对 parallel worker
   开放的通道，与上游一致；阈值 3 一次、每 turn-loop 至多一次）。收件箱本身与上游一样是内存
   字典（不落盘），重启后为空的语义相同；queen 转录里的 `[WORKER_ESCALATION]` 记录是 durable 的
   一半。详见 `worker-escalation-e2e.md` §1/§2。
7. **overseer**：上游 overseer 拿到 queen 全量 `queen_tools`；本 port 的 overseer 运行在 colony
   运行时内，没有会话级工具状态，故本次不向其暴露这 10 个工具（不多给，也不给无法执行的工具）。
8. **collector 差异**：上游 `stop_worker` 会用 `_suppress_report_inject_for` 抑制重复
   `[WORKER_REPORT]`；Go 会话的 worker 报告订阅没有该重复注入路径，故未移植该抑制集合。

## 7. worker monitoring：`get_worker_health_summary`

上游 `core/framework/tools/worker_monitoring_tools.py::register_worker_monitoring_tools` 只在
`session.colony_id` 存在时（`queen_orchestrator.py:937`）把该工具注册进 queen registry，并在
`routes_execution.py:81` 的 `_QUEEN_LIFECYCLE_EXTRAS` 里保证 fork 出来的 worker 不继承。
Go 侧由 `internal/server/queen_worker_monitoring.go` 提供：

- **契约**：name / description / parameters 与 Python `Tool(...)` 逐字；返回是 Python
  `json.dumps` 的同一对象（字段名与顺序：`worker_agent_id`、`worker_graph_id`、`session_id`、
  `session_status`、`total_steps`、`recent_verdicts`、`steps_since_last_accept`、
  `last_step_time_iso`、`stall_minutes`、`evidence_snippet`）。
- **注册面**：`enableQueenLifecycleTools` 把定义挂上 queen 会话 catalogue；
  `colonyBoundQueenTool` 在未绑定 colony 时隐藏（等价 upstream 的 `if session.colony_id`）；
  `colonyWorkerToolSurface` 从不调用 lifecycle 注册，worker catalogue 结构性拿不到。
- **数据源映射**（环境差异，非契约）：上游读 `session.worker_path` 下的
  `sessions/<session_id>/{state.json,logs/tool_logs.jsonl}`；Go colony 把每个 worker 的 run
  记录放在 `<colony>/workers/<worker_id>/{state.json,conversations/parts/*.json}`（`runs/`
  是同一记录的索引副本）。工具优先解析 `tool_logs.jsonl`（存在时与原语义完全一致：verdicts、
  steps 行数、mtime 的 stall 秒表、最后一条 `llm_text` 截 500）；否则按 transcript 的
  assistant 轮次统计 `total_steps`、取最后一条 assistant 文本作 evidence、用最新 part 的
  mtime 作 `last_step_time_iso`（该布局没有 judge verdict 日志，`recent_verdicts` 为空、
  `steps_since_last_accept` 为 0，不伪造）。
- **自动发现**：`session_id` 省略或 `auto` 时，优先 queen 自己的 session id（若该目录存在），
  否则在 `workers/`＋`sessions/` 里按「active（in_progress/running/queued）优先、其后按
  state.json mtime 降序」取第一条；一个都没有时返回 upstream 同款
  `{"error": "No sessions found — worker has not started yet"}`；无 colony 存储时返回
  `{"error": "No colony running."}`（工具在会话面本就被隐藏，直接调用才可能触达）。
- **测试**：`internal/server/queen_worker_monitoring_test.go`（定义、两种布局、自动发现、
  空存储/无 colony）、`internal/cli/session_tools_queen_lifecycle_test.go`（catalogue 注册
  与执行、worker 面不含）。契约口径见 `tool-contract-diff.md` §17.4。
