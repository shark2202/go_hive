# 18 · 重启与状态恢复

> 上一章描述“磁盘上有什么”。本章回答另一个问题：**进程崩溃、被 kill、断电、升级、桌面退出后再启动时，哪些状态会回来，怎么回来，哪些回不来。**
>
> 核心结论先给：**Hive 不是“启动即全量恢复”的系统。** 它把状态写穿到磁盘，但恢复是**按需触发**的：打开某个会话、加载某个 Colony、收到某条消息时才恢复那一条链路。没有全局扫描，也没有“所有 Agent 自动续跑”。

---

## 1. 恢复模型

### 1.1 三种恢复语义

| 语义 | 含义 | 例子 |
|---|---|---|
| **自动（automatic）** | 进程启动时无条件执行 | 目录迁移、`tracker.db` 回填、过期 compaction 标记清理、MCP 子进程清理 |
| **按需（on-demand）** | 只有对应入口被访问才恢复 | 打开 Queen 会话、打开 Colony、发一条消息、Channel 收到消息 |
| **手动（manual）** | 必须显式调用工具/CLI | `run_worker(resume_worker_ids=…)`、重跑 Playbook、`hive serve --colony` 预加载 |

### 1.2 恢复的真相来源

```text
会话恢复     parts/*.json + cursor.json + partials/*.json + conversations/meta.json
Queen 身份   queens/<id>/profile.yaml + soul/ + skills/
Queen 阶段   meta.json["phase"] + meta.json["loaded_tools"]
计划         tasks.json（highwatermark + tasks）
提醒计数     reminder_state.json
Colony 事实  colonies/<c>/tracker/tracker.db
Colony 账本  colonies/<c>/metadata.json + worker.json + tools.json
Worker      colonies/<c>/workers/<sid>/{meta.json,conversations/,tool_tiers.json}
触发器      colonies/<c>/triggers.json
Sentinel    colonies/<c>/escalations/*.json
Playbook    colonies/<c>/colony.db playbook_runs + tracker.db
记忆        memories/**/*.md + log.jsonl
Soul        queens/<id>/soul/{soul.json,history,proposals,state.json}
凭据        credentials/*.enc + secrets/credential_key
```

### 1.3 不恢复的东西

| 状态 | 为什么 |
|---|---|
| 正在运行的 asyncio 任务（流、工具、Playbook 循环、Worker 队列） | 进程内对象，随进程消失 |
| `EventBus` 环形历史 | 内存；磁盘 `events.jsonl` 只用于回放 |
| 注入队列 / 触发器队列 | 内存；未消费的消息不重放 |
| `WorkerStatus` / `WorkerResult` | 内存；Worker 的会话在磁盘，但状态不自动重建 |
| `_budget_applied` / 自适应预算样本 | 内存；重启后从默认重新学习 |
| SSE 客户端计数 | 内存；重启后为 0 |
| LLM Key 池健康状态 | 内存；重启后全部视为健康 |
| 资源采样历史 / 上游健康标记 | 内存 |
| 浏览器标签组 / 桥 RPC 连接 | 进程间；桥可能存活，但 Hive 侧不持久化标签状态 |
| Playbook 正在收敛的循环 | 内存；重启后需手动重跑 |
| Queen Mail 的链深度 / 待回信 | 内存；`mail.jsonl` 只是审计 |
| Claude Code Queen 等待中的信 | 内存；Hive 停止即丢（源码注释明确） |

---

## 2. 启动序列

### 2.1 `hive serve` / `hive open`

```text
进程启动
  │
  ├─ 解析参数（--host/--port/--colony/--model）
  ├─ configure_logging（stdout/stderr + logs/hive-server.log）
  ├─ create_app()
  │    ├─ 凭据存储（EncryptedFileStorage 或自愈回退）
  │    ├─ SessionManager(...)
  │    │    ├─ run_v2()                    # 幂等；失败仅 warning
  │    │    ├─ run_v3()                    # 幂等；失败仅 warning
  │    │    ├─ run_colony_sessions()       # 后 v3 的 Colony 会话归位
  │    │    └─ ensure_all_colony_tracker_dbs()
  │    ├─ compaction_status.sweep_stale_in_progress(QUEENS_DIR)
  │    ├─ run_ga_tool_migration()
  │    ├─ 注册 on_startup：
  │    │    ├─ _start_parent_watchdog
  │    │    ├─ start_streamtoken_refresh
  │    │    ├─ start_resource_sampler
  │    │    ├─ start_soul_scheduler
  │    │    ├─ start_queen_mail
  │    │    └─ write_server_marker
  │    └─ 注册 23 个路由模块 + channels + turn_context + 静态 SPA
  │
  ├─ run_server()
  │    ├─ 信号处理（SIGINT/SIGTERM）
  │    ├─ bridge keepalive task
  │    ├─ preload --colony（每个 colony 调 create_session）
  │    ├─ AppRunner.setup + TCPSite.start
  │    ├─ QueenMail.base_url 设置
  │    └─ SentinelManager.start()
  │         ├─ 重建 open escalation 的 token 索引
  │         └─ 启动已配置渠道的 inbound listeners
  │
  └─ shutdown_event.wait()
```

**关键点**：

- **没有全局会话扫描**。启动不枚举 `queens/*/sessions`，也不枚举 `colonies/*/workers`。只有 `--colony` 显式预加载。
- **迁移是同步、非致命的**。v2/v3/colony sessions 失败只 warning；可能留下半迁移状态，但服务继续起。
- **`ensure_all_colony_tracker_dbs` 会回填每个 Colony 的 tracker 并重写顶层 `worker.json` 的 binding**（不重写 `profiles/*/worker.json`，见 8 章）。
- **compaction sweep 只扫 `queens/` 树**，不扫 `colonies/*/queens/`；Colony fork 中断的 `in_progress` 标记不会被清，下一次打开该 Colony 会等 180s。
- **Sentinel 启动只重建索引和监听**；它不会主动恢复任何停泊 Queen，除非该会话被加载并且 idle ticker 触发。

### 2.2 优雅关闭 vs 崩溃

| 路径 | 行为 |
|---|---|
| **SIGINT/SIGTERM**（`hive serve`） | 取消 bridge keepalive → 关闭 QueenMail（阻止新信/杀 Claude Code 运行）→ `manager.shutdown_all()` 停止每个 session（触发 shutdown reflection + timeline flush）→ `finish_background_tasks(6s)` → 关闭 HTTP runner → 5s 后 `faulthandler` 退出 |
| **SIGKILL / OOM / 断电** | 无 shutdown reflection、无 timeline flush、MCP 子进程可能成为孤儿、浏览器桥可能等父进程 watchdog；已写 parts/cursor 保留，未写部分丢失 |
| **桌面父进程退出** | bridge 监视 `HIVE_DESKTOP_PARENT_PID`，两次确认后自行退出；Hive server 可能被一起杀 |
| **`hive serve` 单独运行** | bridge 的 idle watchdog 在没有 gcu 连接时清理；`HIVE_DESKTOP_PARENT_PID` 回退为自身 PID |

**shutdown reflection 的边界**：它只在 `stop_session` 时异步触发，给 6s 宽限；如果模型调用超过 6s 会被取消。**崩溃不会跑 shutdown reflection**，因此最后一次反思之后的对话不会被写入记忆；下次会话加载时 `subscribe_reflection_triggers` 会把当前 parts 长度标记为“已反思”，不会补反思旧回合（见 8.4）。

---

## 3. 恢复矩阵

| 组件 | 恢复方式 | 触发点 | 来源 | 丢失/上限 |
|---|---|---|---|---|
| Queen DM 会话 | **按需** | 打开 Queen 页 / `POST /api/queen/{id}/session` | 最新 cold DM（按目录名倒序） | 没有自动恢复；必须 UI/请求 |
| Queen 会话（直接 ID） | **按需** | `POST /api/sessions`（`queen_resume_from`） | 指定 session 目录 | 同上 |
| Colony | **按需** | 打开 `/colony/<id>` / `hive serve --colony` | `metadata.json` + `queen_session_id` | 不扫描全部 Colony |
| AgentLoop 回合 | **自动（恢复时）** | 会话加载 | parts + cursor + partials | 注入队列/触发器队列丢失；未完成回合可能 `COLD_INTERRUPTED` |
| Worker | **手动** | `run_worker(resume_worker_ids)` | `workers/<sid>/conversations` | 不自动恢复；队列/状态丢失；`result.json` 可能不存在 |
| Task Plan | **自动（会话加载时）** | 任务工具/UI 读取 | `tasks.json` | 未完成的 `in_progress` 30 分钟后才标 abandoned |
| 提醒计数 | **自动（会话加载时）** | 提醒源读取 | `reminder_state.json` | 丢失则从零开始 |
| Trigger | **自动（Colony 加载时）** | `_load_worker_core` | `triggers.json` | 错过的 tick **静默跳过**，不发 `MISSED_TRIGGERS` |
| Sentinel 升级 | **自动（启动重建索引）** | `SentinelManager.start` | `escalations/*.json` | 未送达的升级不重发；队列丢失 |
| Playbook | **手动** | 重新 `run_playbook` | `tracker.db` + `colony.db` | 运行循环丢失；崩溃可能没有 run-log 行 |
| Memory | **自动（会话加载时）** | 反思订阅 | `.md` + `log.jsonl` | 崩溃丢失最后一段未反思对话 |
| Soul | **自动（scheduler）** | 启动后每日 due | `soul/state.json` + `soul.json` | 错过多天只补今天；重试 3 次/天 |
| Timeline | **自动（会话加载时）** | `subscribe_timeline_triggers` | `timeline.jsonl` + `timeline_meta.json` | 回填最多 10 个更早会话；崩溃丢失最后一次抽取 |
| 凭据 | **自动（首次使用）** | `resolve_secret` | `.enc` + `credential_key` | key 丢失则不可读；OAuth 自动刷新 |
| MCP server | **自动（会话加载时）** | MCP registry | `installed.json` | 子进程重启；健康状态重新探测 |
| EventBus 历史 | **回放** | SSE `events/history` | `events.jsonl` | 环形历史丢失；`seq` 进程内重置 |
| Channel 会话 | **按需** | 插件 `open_session` | 插件自己的映射 | Hive 不持久化 conversation→session |
| Turn Context | **自动（启动）** | 插件 entry point | 插件 state dir | 插件自管 |
| Claude Code Queen | **按需** | 收到信 / 打开只读页 | `conversations.json` + Claude 自己的 jsonl | 等待中的信在内存，Hive 停止即丢 |
| Queen Mail 链 | **部分** | 会话加载 | `mail.jsonl` + conversations.json | 链深度/待回信在内存，重启重置 |
| 浏览器桥 | **进程级** | supervisor 重启 | `browser_contexts.json` / `browser_default.json` | 标签组不保证回收；状态硬编码 `~/.hive` |
| Senders | **自动** | 发信/回信 | `senders/threads.json` + `usage.json` + 云端 | 未记录的 token 无法路由；用量按 UTC 日重置 |
| 文件沙箱 | **保留** | 工具访问 | `~/.hive/workdir/workspaces/default/**` | 无自动清理；无锁 |
| 前端存储 | **按浏览器** | 页面加载 | `localStorage` / `sessionStorage` | 清除站点数据重置；服务端才是跨设备真相 |
| Legacy 图执行 | **手动** | checkpoint 恢复 | `agents/**/state.json` + `checkpoints/` | 不自动恢复；需显式 checkpoint |
| 资源/健康 | **重置** | — | 内存 | 历史清空 |

---

## 4. 详细恢复路径

### 4.1 Queen 会话冷恢复

**入口**：

- `POST /api/queen/{queen_id}/session`：找 live DM → 找最新 cold DM（目录名倒序）→ resume；没有则新建。
- `POST /api/sessions` 带 `queen_resume_from`：恢复指定 session。
- 桌面端在 SSE 404（session gone）时自动重试 `resume`。

**恢复步骤**：

```text
1. 解析 session 目录：
   DM:             queens/<queen>/sessions/<sid>/
   Colony overseer: colonies/<c>/queens/<queen>/sessions/<sid>/
2. 读 meta.json：
   - queen_id / agent_name / agent_path / created_at
   - phase / loaded_tools / crm_setup / channel
   - colony_id / colony_spawned / spawned_colony_id
   - superseded_by / forked_from / compacted_from
3. 等待 .janitor.lock 释放（最多 120s；超过 1h 视为 stale）
4. 扫描 events.jsonl 的 max(iteration) → iteration_offset
5. event_bus.set_session_log(events.jsonl, iteration_offset)
6. create_queen → AgentLoop._restore：
   - conversations/meta.json（没有 → 视为新会话）
   - parts/*.json（按 seq）
   - cursor.json（iteration / outputs / pending_input / user_stopped / tool_calls_used / recent_*）
   - partials/*.json（seq >= next_seq 的作为 truncated assistant 恢复）
   - _repair_orphaned_tool_calls（丢弃孤儿/重复结果，给缺失结果补合成错误）
7. 若 pending_input：恢复 ASK_USER / CREDENTIAL_FORM / COLONY_SUGGESTION / TURN_DONE
8. 若 user_stopped：保持停泊，不自动运行
9. 若没有 pending_input 且是 cold resume：写 COLD_INTERRUPTED，停泊等用户
10. 恢复 phase / loaded_tools；重新绑定工具面
11. 启动提醒源、记忆反思/召回、timeline 回填、标题、Soul
```

**语义细节**：

- `resolved_session_id = queen_resume_from or session_id`：冷恢复**复用原目录**，不新建 session 目录；历史连续。
- `events.jsonl` **不会被截断**（尽管 `routes_sessions.py` 的注释声称 `_start_queen` 会 truncate）。实际是：`iteration_offset` 从旧事件最大 iteration 开始，历史接口把 `seq` 重写为绝对行号；实时 SSE 的 `seq` 仍从进程内 1 开始。两者用 `timestamp|seq` 去重，能工作，但 `seq` 不是全局单调。
- **`restore` 返回 None 但 parts 非空**：`_execute_impl` 会记录 warning 并**清空 parts**（视为损坏/不可恢复），这是潜在数据丢失点；原因是 `conversations/meta.json` 缺失或不可读。
- **`partials`**：流式中断时恢复为 `truncated=True` 的 assistant 消息；下一次模型调用会看到“被打断的半成品”，可决定重做或继续。
- **悬空工具调用**：`_repair_orphaned_tool_calls` 给没有结果的 tool_use 补 `[Tool call interrupted]` 错误结果，避免 provider 400。
- **`user_stopped`**：持久化在 cursor；恢复后保持停泊。任意 `inject_event(wake=True)` 会清除它（包括 Worker 报告、Channel 消息），不只是用户消息。
- **`COLD_INTERRUPTED`**：只有当没有 pending_input、也没有新注入/触发器时才会写；它表示“上一回合没干净结束”。如果上一回合是 `TURN_DONE`，会以 `TURN_DONE` 恢复（pending_input 里有记录）。

### 4.2 Colony 恢复

**入口**：`hive serve --colony <name>`、`POST /api/sessions(colony_id=...)`、前端打开 `/colony/<id>`。

**步骤**：

```text
1. vacate_soft_deleted_colony（若 metadata.deleted=true，先把死目录挪开）
2. 若 metadata.json 存在：
   - create_session 走 _create_session_for_existing_colony
   - 读取 metadata：queen_session_id / source_session_id / worker_profiles / max_concurrent_workers
   - 若 queen_session_id 存在，恢复该 overseer 会话
   - 等待 compaction_status.await_completion(180s)
3. _load_worker_core：
   - 绑定 ColonyBinding
   - 读 triggers.json：加载定义，自动启动 enabled 的 timer/webhook
   - 计算 missed ticks：**静默跳过**（把 last_fired_at 设为 now），不发 MISSED_TRIGGERS
   - 清理 stale active sessions
4. _start_queen：恢复 Queen 会话（同 4.1）
5. ColonyRuntime 构造：从 metadata 读 max_concurrent_workers；恢复 skills/tools/MCP
6. 不恢复 Worker：workers/ 目录保留，但 registry 为空；需要 Queen 重新 run_worker 或 resume
```

**关键点**：

- **Colony 不会在启动时自动加载**；必须显式 `--colony` 或前端打开。
- **Worker 不会自动恢复**。进程崩溃时正在跑的 Worker 的 `SUBAGENT_REPORT` 永远不会到达 Queen；Worker 会话在 `workers/<sid>/conversations` 里，Queen 可以用 `resume_worker_ids` 恢复，但她需要知道 worker_id（通常来自她之前的工具结果/报告）。
- **Trigger 自动启动**：`enabled=true` 的 timer/webhook 会在 Colony 加载时重新启动；`last_fired_at/next_due_at` 保留。
- **错过的 tick 不补跑**：当前实现静默把 `last_fired_at` 设为 now。`MISSED_TRIGGERS` 事件类型和 `resolve_missed` 端点仍在，但**没有生产者**（源码中无 emit 点）。这是文档与实现漂移，也是“重启后不自动补任务”的实际语义。
- **Compassion sweep 范围有限**：`compaction_status.sweep_stale_in_progress(QUEENS_DIR)` 只扫 DM 树；Colony fork 的 `in_progress` 标记在 `colonies/.../queens/.../sessions` 下，重启后不会被清，下一次打开会等 180s。

### 4.3 Worker 恢复

**手动入口**：`run_worker(resume_worker_ids=[...], guidance="...")`。

**前置检查**（`ColonyRuntime.resume_worker`）：

```text
worker 必须：
  - 不是 active（否则拒绝，防止两个 AgentLoop 写同一 store）
  - 目录存在：colonies/<c>/workers/<sid>/
  - result.json 不存在，或不是 _janitor 墓碑
  - conversations/parts/ 至少一个 part
  - meta.json 的 colony_id 与当前 Colony 一致
```

**恢复**：

- 从 `meta.json` 恢复 `task` / `profile_name` / `goal` / batch 坐标；
- 从 parts + cursor 恢复对话、累计工具调用、预算；
- 继续执行，结束后 `report_to_parent`。

**不恢复**：

- 原 Worker 的 `WorkerStatus` / `WorkerResult` / 队列位置；
- 原 batch 的 `batch_remaining` 计数（新报告会重新计算）；
- 原 Worker 的浏览器标签组（不保证回收）。

**风险**：

- 崩溃时 Worker 可能已经产生副作用（发邮件、写文件、提交表单），恢复会**再次执行**；Worker 任务必须设计为幂等，或通过 Tracker 的 key 去重。
- Worker 的 `result.json` 在当前 runtime **不由 Hive 写**（只有 Janitor 墓碑/历史/桌面可能写）；`resume` 不依赖它，但 UI 历史详情可能显示 `status=historical`。
- `profiles/<name>/worker.json` 的 binding 不会被 `ensure_tracker_db._patch_worker_configs` 回填（只 glob 顶层 `*.json`），如果 fork 时写错，profile Worker 会拿不到正确 Tracker 路径。

### 4.4 Trigger 恢复

**定义持久化**：`colonies/<c>/triggers.json`。

**加载**：

```text
for each trigger in triggers.json:
  - trigger_type in ("timer","webhook")
  - enabled 缺省为 true
  - 加入 session.available_triggers
  - enabled → 加入 autostart
```

**自动启动**：

- timer → `_start_trigger_timer` 计算下一次；
- webhook → `_start_trigger_webhook` 绑定本地端点；
- 成功后加入 `active_trigger_ids`，发 `TRIGGER_ACTIVATED`。

**错过补偿**：

- `compute_missed` 计算 `last_fired_at` 到 `now` 之间的 tick；
- 当前代码**静默跳过**：把每个 trigger 的 `last_fired_at` 设为 now，保存；
- 不发 `MISSED_TRIGGERS`，不弹握手，不补跑。

**重启影响**：

- 会话未加载 → 触发器不运行；
- 加载后 → 只从“现在”开始下一次；
- 如果用户期望补跑，需要手动触发或重跑任务。

### 4.5 Sentinel 恢复

**启动**：

```text
SentinelManager.start()
  → 安装 sentinel.log
  → store.list_all_open() 重建 token → (colony, escalation)
  → 启动 escalation sender 协程
  → refresh_listeners()：按渠道 token 启动 Telegram/Slack/Hive 监听
```

**恢复语义**：

- **升级记录**持久化在 `escalations/*.json`；`status=open` 的记录会重建索引。
- **已送达但未回复**：记录 open；用户回复时通过 token/thread 找到记录并 resume。
- **未送达**（发送失败）：`_handle_escalation` 仍写记录（open），但不重试；重启后记录仍在，但没有自动重发。
- **升级队列**：内存 `asyncio.Queue`；优雅关闭时取消；崩溃时丢失正在排队的升级。
- **停泊 Queen**：不会自动恢复；只有该 session 被加载且 idle ticker 再次触发，才会重新评估。
- **`sse_client_count`** 重启后为 0：`escalate_when_ui_attached=false` 时，即使你正准备打开页面，也可能先往外部渠道发升级。
- **`recent_chat`** 内存；重启后 `detect_chat` 需要新消息才能恢复。
- **Claude Code Queen 的等待信**：内存，Hive 停止即丢；源码注释明确“when Hive stops, they are dropped, and the writer isn't told”。

### 4.6 Playbook 恢复

**持久化**：

- 脚本：`playbooks/<name>.play.py`；
- run log：`colony.db.playbook_runs`（**只在结束时写**：done/error）；
- 业务状态：`tracker.db`。

**重启语义**：

- **运行中的收敛循环丢失**；没有 “running” 行可恢复；
- 已完成的行在 Tracker 里，不会重复派发；
- 未完成的行仍在 `pending()` 查询里；
- 用 `run_playbook(playbook_name=...)` 重跑即续跑；
- 如果崩溃发生在 `record_playbook_run` 之前，`colony.db` 可能没有这次 run 的记录（但 Tracker 仍正确）。

**风险**：

- 正在执行的 Worker 可能已产生副作用；重跑前要确认幂等。
- `deadletter` 只在内存；崩溃后丢失，但对应行仍在 Tracker，重跑会再次尝试。
- 没有自动恢复，也没有“上次跑到哪”的游标；这是设计选择（Tracker 是唯一真相）。

### 4.7 Task / Plan / Reminder 恢复

**Task Plan**：

- `tasks.json` 持久化；`highwatermark` 保证 ID 不复用；
- 会话加载后任务工具直接读文件；
- `in_progress` 超过 30 分钟未更新 → `abandoned`（由 `sweep_idle_tasks` 在任务列表路由调用时触发，不是后台定时器）；
- `archived` 任务保留，可恢复。

**ReminderState**：

- `reminder_state.json` 持久化 `turns_total` / `turns_since_task_op` / `drift_reminders_sent` / `snapshots_sent` 等；
- 重启后继续计数，避免重复 nudge；
- 丢失则从零开始（可接受）。

**风险**：

- 任务文件是等级 A 原子写，但 `TaskStore` 的 session 目录解析依赖扫描；如果 session 目录被手动移动，可能落到 `_misc/<slug>`，看起来“任务丢了”。
- 没有跨进程文件锁；桌面/CLI 同时操作同一 session 可能 last-write-wins。

### 4.8 Memory / Soul / Timeline 恢复

**Memory reflection**：

- `subscribe_reflection_triggers` 每次会话加载重新订阅；
- 每 6 个 `turn_done/ask_user` 回合反思一次；
- `_reflected_parts.setdefault(session_dir, len(parts))`：把当前 parts 长度标记为“已反思”，**不会补反思重启前的未反思回合**；
- 优雅关闭时 `stop_session` 触发 `run_shutdown_reflection`（6s 宽限）；崩溃没有；
- 反思失败写 `memories/**/.reflection_error.txt`（best-effort）。

**Soul**：

- `soul/state.json` 记录每个会话读到哪、`last_run`、`attempts`；
- `soul_queens()` 从配置读启用 Queen；
- `due()` 判断：过了 `hour`、今天未跑、review 模式无待审提案、当天尝试 <3、距上次失败 ≥1h；
- 重启后 scheduler 继续；错过多天只补今天（`last_run == today` 才跳过，不会逐日回放）；
- 待审提案持久化在 `proposals/`，重启不丢；
- 应用/回滚是原子写版本。

**Timeline**：

- `timeline_meta.json` 记录 `user_turns` 游标；
- 会话加载时 `subscribe_timeline_triggers` 会 backfill 最多 10 个更早会话；
- 每 3 条新用户消息或 300s 冷却抽取；
- 会话停止时 flush；
- 崩溃丢失最后一次抽取，下一次加载会从游标继续（最多回填 10 个会话）。

### 4.9 Credentials / Models / MCP 恢复

**Credentials**：

- `HIVE_CREDENTIAL_KEY` 优先从环境变量；否则从 `secrets/credential_key`；否则从 shell 配置；
- 找到后回写 `secrets/credential_key`（0600）；
- `.enc` 解密失败 → `CredentialDecryptionError`；服务端启动时会自愈删除不可读 `.enc`（如果 key 完全缺失）；
- `metadata/index.json` 缺失/版本旧 → 从 `.enc` 重建；
- OAuth 在 `resolve_secret` 时按需刷新；刷新失败显式报错；
- **key 丢失 = 数据永久丢失**。

**Models**：

- 配置在 `configuration.json`；模型目录在包内 `model_catalog.json`；
- 没有运行时状态需要恢复；Key 池健康状态重置。

**MCP**：

- `installed.json` 持久化 server 定义；
- 子进程随 Hive 退出；重启后按需重新 spawn；
- `health` 重新探测；
- OAuth token 刷新后触发 server 重启；
- 崩溃可能留下孤儿 MCP 子进程；`atexit` 只在正常退出跑，SIGKILL 不会。

### 4.10 Event / SSE / 历史恢复

**持久化**：

- `events.jsonl` 追加；流式 delta 合并；`full_request` 剥除；
- `events/history` 支持分页（tail / before_offset / before_index）；
- 历史接口把 `seq` 重写为**绝对 1-based 行号**，保证分页 join 与去重稳定。

**重启语义**：

- `EventBus._seq_counter` 从 0 开始；实时事件 `seq=1..N`；
- `events.jsonl` 保留旧事件；`iteration_offset` 从旧最大 iteration 开始，避免消息 ID 碰撞；
- 历史接口的 `seq` 是行号，不是运行时 `seq`；前端用 `timestamp|seq` 去重；
- `SESSION_SNAPSHOT` 从环形历史构建；重启后环形历史为空，首次订阅可能没有 snapshot，直到历史回放/新事件；
- **注释与实现漂移**：`routes_sessions.py` 注释说冷恢复会 truncate `events.jsonl`，当前代码没有 truncate。实际依赖 iteration offset + 历史 seq 重写。

### 4.11 Browser / Channel / Turn Context 恢复

**Browser**：

- `bridge_host` 是独立 supervisor；Hive 重启不必然杀死它；
- `hive serve` 的 keepalive 任务保持控制连接；没有桌面父进程时 bridge 空闲 30s 自行退出；
- 标签组由扩展/桥管理；Hive 不持久化“哪个 Worker 开了哪些标签”；
- 崩溃后 Worker 的标签组不保证被回收；需要用户手动关闭或等桥清理。

**Channel**：

- 插件在 `hive serve` 启动时加载；
- 每个聊天→session 的映射由插件持久化（Hive 只给 `state_dir`）；
- 重启后插件调用 `open_session` 恢复；
- Hive 不持久化 Channel 的输出队列；未送达的输出丢失（至多一次）。

**Turn Context**：

- 插件在启动时加载；每次回答前调用；
- 状态在插件自己的 `state_dir`；Hive 不持久化插件状态。

### 4.12 Claude Code Queen / Queen Mail 恢复

**Claude Code Queen**：

- `claude_code/<id>/conversations.json` 持久化“通信者→Claude Code session”；
- `mcp/<sid>.json` 每轮生成，0600，运行结束删除；
- Claude Code 自己的会话在 `config/projects/*/<sid>.jsonl`；
- 重启后收到信会 `--resume` 原会话；
- **等待中的信在内存**，Hive 停止即丢，写信人不会收到失败通知。

**Queen Mail**：

- `queens/mail.jsonl` 只是审计（id/from/to/sessions/depth/chars/dropped）；
- `_depth` / `_answering` / `_pending` / `_routes` 在内存；
- 重启后链深度重置；回信路由依赖 `conversations.json`（Claude Code）或 session 历史；
- 日限额 `_sent` 在内存，重启后重置（可被滥用，但单进程假设下可接受）。

### 4.13 Legacy 图执行路径恢复（独立/导出 Agent）

Colony 主路径不使用这一套；但 `AgentLoader`/`GraphExecutor` 仍支持传统图格式，恢复语义不同：

| 文件 | 作用 | 恢复方式 |
|---|---|---|
| `agents/<name>/sessions/<sid>/state.json` | `SessionState`：状态、进度、数据缓冲、最新 checkpoint | 读 `pid` 判断是否 stale；`checkpoint_enabled` + `latest_checkpoint_id` 才可 resume |
| `checkpoints/index.json` | checkpoint 清单 | `CheckpointStore.load_index` |
| `checkpoints/cp_*.json` | 单次 checkpoint（data_buffer、execution_path） | `load_checkpoint` 从指定点续跑 |
| `runs/<run>.json` / `summaries/<run>.json` | Run 审计与摘要 | 只读；用于调试/导出 |
| `logs/summary.json` / `details.jsonl` / `tool_logs.jsonl` | RuntimeLogger L1/L2/L3 | 只读 |

**关键差异**：

- **没有自动恢复**：`SessionState.is_resumable` 只在 `checkpoint_enabled && latest_checkpoint_id` 时为真；调用方必须显式从 checkpoint 恢复。
- **`pid` 是 stale 判断**：`state.json` 记录写它的进程 PID；跨进程恢复时用它识别“上一个进程已经死了”。
- **`data_buffer` 是图级共享状态**：与 Colony 的 Tracker 完全不同；它只存在于 checkpoint/state 里。
- **`runs/`/`summaries/` 是审计**：丢失不影响恢复。
- **Standalone Agent 目录**（`exports/<agent>/`）是另一套：`agent.json` 定义图，`config.py` 定义模型/工具，`mcp_servers.json` / `mcp_registry.json` 定义 MCP；重启后由 `AgentLoader` 重新加载，没有“会话恢复”概念，除非显式使用 checkpoint。

### 4.14 浏览器状态、发件人状态、文件沙箱与前端存储

**浏览器桥 / GCU**：

- `~/.hive/browser_contexts.json`：profile→tab group 索引；gcu 重启时 `rehydrate_contexts` 重新附着到仍存活的 tab group；组内 tab 全关则丢弃。
- `~/.hive/browser_default.json`：星标默认 Chrome profile；重启后仍生效。
- `~/.hive/browser-logs/*.jsonl`、`~/.hive/bridge_host.log`：诊断日志；不参与恢复。
- **Hive 不持久化“哪个 Worker 开了哪些 tab”**；崩溃后 Worker 的 tab group 不保证被回收。

**发件人（Senders）**：

- `~/.hive/senders/threads.json`：外发邮件 `Message-ID` token → 会话；重启后回信仍能路由。
- `~/.hive/senders/usage.json`：每发件人每日用量；按 UTC 日重置，跨重启保持。
- 云侧 `team_sender_sends`：真正的配额/审计记录；离线时，有 `daily_limit` 的发件人**拒绝发送**。
- 崩溃在“已发送、未记录”窗口：`threads.json` 可能缺少该 token，回信无法路由；云侧发送日志仍可用于人工排查。

**Tracker UI 快照**：

- Dashboard 的 Colony Data 页读取 `/var/tmp/hive-tracker-snapshots/*.db`（2s TTL 的本地副本），不是 `tracker.db` 本身。
- 重启后快照缓存丢失，下一次读取重建；残留临时文件可安全删除。
- 恢复权威仍是 `tracker/tracker.db`；快照只影响浏览的新鲜度（可能落后最后一次 WAL checkpoint 几秒）。

**文件工具沙箱**：

- `~/.hive/workdir/workspaces/default/<agent_id>/current/` 是普通文件；重启后保留。
- 没有自动清理；Janitor 不把它列入已知顶层，可能被 junk detector 报告为 unknown。
- 没有文件锁；多个 Worker 同时写同一路径会 last-write-wins。

**前端存储**：

- `localStorage` / `sessionStorage` 由浏览器 Profile 管理；Hive 重启不影响。
- `sessionStorage` 关闭 tab 即清；`localStorage` 保持。
- 服务端 `configuration.json` / `custom_prompts.json` / Memory 是跨设备真相；前端存储只是渲染缓存/设备偏好。

---

## 5. 崩溃点矩阵

| 崩溃时机 | 磁盘状态 | 恢复行为 | 风险 |
|---|---|---|---|
| 流式输出中 | 已写 parts + 部分 partial | partial 恢复为 truncated assistant；`COLD_INTERRUPTED` 停泊 | 最后一段文本可能重复/缺失 |
| 工具执行中 | assistant tool_calls 已写；无 tool result | `_repair_orphaned_tool_calls` 补中断错误 | 工具副作用可能已发生，恢复后可能重试 |
| 工具结果落盘后、写 part 前 | 溢写文件存在；part 缺失 | 模型看不到结果；文件成为 orphan | 需要 Janitor/手动找回 |
| 压缩删除旧 parts 后、写 summary 前 | 旧 parts 已删；summary 未写 | 只剩 recent messages | **历史丢失**（compaction 非事务） |
| 压缩中 LLM 超时 | 旧 parts 未删或部分清理 | 下一轮重试；连续 3 次熔断 | 上下文可能持续超预算 |
| 写 cursor 前 | parts 更新；cursor 旧 | `_next_seq` 从 parts 最大 seq 回退 | 可能重复 seq/消息 |
| 写 meta 中 | meta 截断 | 读方降级为空；可能丢 phase/标题/锁 | 需要手工修复 |
| 写 triggers.json 中 | 文件截断 | 触发器定义丢失 | 需要重建 |
| fork Colony 压缩中 | `compaction_status=in_progress` | 下次打开等 180s；sweep 不扫 Colony 树 | 启动变慢 |
| Playbook 运行中 | Tracker 行；无 run log | 手动重跑；done 行不重复 | 未完成行重试；副作用可能重复 |
| Worker 运行中 | worker 会话 parts | 不自动恢复；手动 resume | 状态/报告丢失；需知道 worker_id |
| Sentinel 发送中 | escalation record 可能已写 | 记录 open；不自动重发 | 用户可能收不到 |
| 凭据 `.enc` 写入中 | 文件截断/不可解密 | 解密失败；服务端可能自愈删除 | 该凭据永久丢失 |
| MCP 调用中 | 子进程死 | 下次会话重新 spawn | 孤儿进程；工具结果丢失 |
| 浏览器操作中 | 桥/扩展状态 | 重连；标签组可能残留 | 登录态/页面状态不确定 |
| 断电 | 取决于 fsync | A/D/E 文件可恢复；C 可能丢最后写 | 需要备份 |

---

## 6. 恢复不变量

1. **一个 session 目录只有一个活跃 AgentLoop**；恢复前必须确认没有 live 实例（Worker resume 会拒绝 active）。
2. **parts 的 seq 单调**；恢复时用 `max(parts.seq, cursor.next_seq)` 校正。
3. **`pending_input` 是冷启动的唯一“等待真相”**；没有它就不要猜测上一回合在等什么。
4. **`user_stopped` 优先于自动恢复**；任何自动 nudge 都不能绕过。
5. **Tracker 是 Colony 的唯一业务真相**；Playbook/Worker 恢复都从它重新查询。
6. **`events.jsonl` 只追加**；历史接口的 seq 重写是只读视图，不改变文件。
7. **`compaction_status` 是“等待”信号，不是恢复权威**；超时后应继续用原始 transcript。
8. **凭据 key 必须先于凭据存储加载**；否则 `.enc` 永久不可读。
9. **迁移幂等**；失败只 warning，不阻塞启动，但可能留下半迁移状态。
10. **恢复是显式的**；没有全局扫描，也没有“所有 Agent 自动续跑”。

---

## 7. 已知缺口与文档漂移（重要）

| # | 缺口 | 影响 | 证据 |
|---|---|---|---|
| 1 | `events.jsonl` 注释称冷恢复会 truncate，实际没有 | 旧事件保留；依赖 iteration offset + 历史 seq 重写 | `routes_sessions.py` 注释 vs `session_manager.py` 无 truncate |
| 2 | `MISSED_TRIGGERS` 没有生产者 | 错过的 tick 静默跳过，UI 不会弹握手 | `session_manager.py:1084` 静默改 `last_fired_at`；无 emit |
| 3 | `seq` 是进程内计数 | 重启后实时 seq 从 1 开始；历史 seq 是行号；依赖 `timestamp|seq` 去重 | `event_bus.py:440`、`routes_sessions.py:2200` |
| 4 | `meta.json` / `triggers.json` / `worker.json` / `custom_prompts.json` / `attached_credentials.json` / `compaction_status.json` 是直接写 | 崩溃可能截断 | 见 17 章原子性表 |
| 5 | `restore` 返回 None 但 parts 存在 → 清空 parts | 可能丢历史 | `agent_loop.py:1488` |
| 6 | compaction 先删 parts 再写 summary | 崩溃丢历史 | `conversation.py:1610` |
| 7 | `result.json` 当前 runtime 不写 | Worker 历史详情可能 `status=historical` | 仅 Janitor/测试/桌面写 |
| 8 | `profiles/*/worker.json` binding 不被回填 | profile Worker 可能拿不到 Tracker | `tracker_db._patch_worker_configs` 只 glob 顶层 |
| 9 | `compaction_status` sweep 只扫 `queens/` | Colony fork 中断会等 180s | `app.py:1049` 传 `QUEENS_DIR` |
| 10 | `user_stopped` 被任意 wake 注入清除 | Worker 报告/Channel 消息可能解除停止 | `agent_loop.py:3593` |
| 11 | Playbook 运行中无 run-log 行 | 崩溃后无“running”记录 | `playbook_tools._record_run` 只在结束写 |
| 12 | Sentinel 送达失败不重试 | 升级可能丢失 | `manager._handle_escalation` 只 log |
| 13 | Claude Code 等待信在内存 | Hive 停止即丢 | `claude_code_queens.py` 注释 |
| 14 | 浏览器标签组不保证回收 | 残留标签/登录态 | 无持久化 |
| 15 | 没有全局会话扫描 | 重启后 Agent 不会自动续跑 | 必须 UI/`--colony` |
| 16 | `tracker_sql` 的 schema-qualified 写可绕过 `_` 保护 | `main._tracker_registry` / `main._tracker_changes` / 触发器可被改/删；`user_version=3` 时 `ensure_tracker_db` 不会重建 | `tracker_db._referenced_tables_for_mutation` 未剥离 `main.` 前缀；见 19 章 6.3 |

---

## 8. 运维 Runbook（重启后）

### 8.1 正常重启

```bash
# 1. 优雅停止（让 shutdown reflection 跑完）
kill -TERM <hive-pid>

# 2. 启动（需要预加载的 Colony）
hive serve --colony <name> --port 8787

# 3. 健康检查
curl -s http://127.0.0.1:8787/api/health
curl -s http://127.0.0.1:8787/api/health/resources
```

### 8.2 崩溃后检查

```text
[ ] 日志：logs/hive-server.log 最后 200 行
[ ] 迁移：.migrated-v2 / .migrated-v3 是否存在
[ ] 损坏的 meta.json / triggers.json / worker.json（jq 解析）
[ ] 会话：parts 是否连续；cursor 是否存在
[ ] Tracker：sqlite3 tracker.db "PRAGMA integrity_check;"
[ ] WAL：tracker.db-wal / -shm 是否残留（正常）
[ ] 凭据：HIVE_CREDENTIAL_KEY 是否可读；.enc 是否可解密
[ ] MCP：孤儿进程（ps）；installed.json 是否正常
[ ] 浏览器：bridge 是否存活；标签组是否残留
[ ] Sentinel：escalations/*.json 是否有 open；listeners 是否启动
```

### 8.3 恢复动作

| 症状 | 动作 |
|---|---|
| Queen 不自动出现 | 打开 Queen 页 / `POST /api/queen/{id}/session` |
| Colony 不自动加载 | `hive serve --colony <name>` 或前端打开 |
| Worker 报告丢失 | 用 `run_worker(resume_worker_ids=...)`；worker_id 在 Queen 历史/`workers/` 目录 |
| Playbook 中断 | 用 `run_playbook(playbook_name=...)` 重跑；done 行不会重复 |
| Trigger 错过 | 当前不会自动补跑；手动触发或重跑任务 |
| Sentinel 升级未送达 | 检查 `escalations/*.json`；重新触发或手动联系 |
| 记忆缺失 | 检查 `memories/**/.reflection_error.txt`；必要时从 `log.jsonl` 恢复 |
| 凭据不可读 | 确认 `HIVE_CREDENTIAL_KEY`；否则只能删除重存 |
| 会话损坏 | 从 `parts/` 手工重建 `meta.json`（至少 `system_prompt`）或从备份恢复 |

---

## 9. 评价

**做得好的地方**

- **写穿 + cursor + partials + 事件日志**构成完整的会话恢复链；冷恢复能精确到“最后一条消息/停泊原因”。
- **显式停泊原因**让恢复可以区分“正常等待”和“异常中断”。
- **Tracker 作为唯一业务真相**让 Playbook/Worker 的恢复不需要额外游标。
- **优雅关闭**会跑 shutdown reflection + timeline flush，并给 6s 宽限。
- **迁移幂等**、`tracker.db` 回填、compaction 标记清理，都是启动时该做的事。
- **恢复是按需的**，避免“重启后所有 Agent 一起跑”的资源风暴。

**风险**

- **恢复不是全自动**：用户/运维必须知道要打开哪个会话、哪个 Colony、哪个 Worker。
- **多个关键文件仍是直接写**：`meta.json`、`triggers.json`、`worker.json`、`.enc`、`custom_prompts.json` 崩溃可能截断。
- **compaction 非事务**：先删 parts 再写 summary，崩溃窗口会丢历史。
- **`restore` 的“parts 非空但 meta 缺失 → 清空 parts”**是潜在数据丢失点。
- **`seq` 进程内重置 + 历史 seq 重写**让恢复依赖前端去重逻辑；不是强一致。
- **`MISSED_TRIGGERS` 没有生产者**：文档承诺的握手不存在，错过 tick 静默跳过。
- **Playbook/Worker/浏览器/Sentinel** 都有“内存态丢失、需手动”的边界。
- **单进程假设**：`server.lock` 不是互斥锁；多实例会破坏恢复。

### 一句话

> **Hive 的恢复是“磁盘真相 + 按需重放”，不是“启动即恢复”。**
> 它能把会话精确恢复到停泊点，但 Colony、Worker、Trigger、Playbook、Sentinel 的恢复各自有明确的触发条件与手工步骤；理解这些边界，比假设“它会自己回来”更重要。
