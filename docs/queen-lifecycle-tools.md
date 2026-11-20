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
| `internal/server/queen_lifecycle_triggers.go` | `set_trigger`、`list_triggers`、`remove_trigger` |
| `internal/cli/session_tools_queen_lifecycle.go` | `enableQueenLifecycleTools`：把 10 个定义挂到会话 catalogue |
| `internal/cli/loader.go` / `loader_preloaded_session.go` | 仅在 queen 聊天会话上启用（与 `enableQueenMailTools` 并列） |
| `internal/server/session.go` | `PendingEscalations` 增加到达顺序（`pendingOrder`），`list_worker_questions` 按序输出 |

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
| worker 提问收件箱 | `server.Session.PendingEscalations`（`InstallWorkerEscalationRouting` 写入）、`PendingEscalation/TakePendingEscalation` |
| trigger 存储与运行期 | `server.Session` 的 `AvailableTriggerDefs`/`ActiveTriggers` + `trigger_runtime.go`（`StartTriggerTimer`、`StartTriggerWebhook`、`PersistActiveTriggers`、`saveTriggerToAgent`、`triggers.RemoveTriggerFromAgent`） |
| worker profile 注册表 | `routes_colonies_profiles.go`：`listWorkerProfilesWithHome`、`upsertWorkerProfile`、`validateWorkerProfileName`（写 `colonies/<id>/metadata.json`） |
| 浏览器桥探测 | `bridgeHTTPRequest` + `bridgeResponseBody`（与 `routes_app_extra.go` 的 app.py 探针同一实现） |

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
| `internal/server/queen_lifecycle_actions_test.go` | trigger CRUD（timer 激活/持久化/列出/移除 + 全部分支错误 + webhook 激活与取消订阅清理）、`update_worker_profile`（新增、局部更新保字段、空值清除、3 条错误）、`list_browser_profiles`（不可达 / 有 profile / 空 profile）、chat runtime 门控、取消上下文下 stop 仍生效 |
| `internal/cli/session_tools_queen_lifecycle_test.go` | catalogue 注册（启用前不可见、启用后 10 个全在）、经 catalogue 执行、worker catalogue 不含 10 个工具 |

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
6. **`list_worker_questions` / `reply_to_worker`**：上游把两者从 phase 列表移除（worker 无升级
   通道），但仍注册在 queen 注册表；本 port 与上游注册面一致（常驻 queen 会话工具表），由
   上游保留的 escalation 收件箱驱动，行为按 `reply_to_worker` 文档实现。
7. **overseer**：上游 overseer 拿到 queen 全量 `queen_tools`；本 port 的 overseer 运行在 colony
   运行时内，没有会话级工具状态，故本次不向其暴露这 10 个工具（不多给，也不给无法执行的工具）。
8. **collector 差异**：上游 `stop_worker` 会用 `_suppress_report_inject_for` 抑制重复
   `[WORKER_REPORT]`；Go 会话的 worker 报告订阅没有该重复注入路径，故未移植该抑制集合。
