# 接口与契约（生成物，勿手改）

> 由 `python3 scripts/gen_arch.py` 从 `architecture/model.yaml` 的 `contracts:` / `disk_formats:`，加上 `internal/server` 路由注册与 `internal/eventbus` 常量生成。commit: `059f4afe0c97`
> 契约是模块之间「靠什么约束交互」的清单：谁提供、谁消费、定义在哪、保证什么、失败怎么表现、变更属于加法还是破坏、由什么机制强制。
> `providers` / `consumers` 里的 `ext:` 前缀表示外部消费面（前端、运维、MCP server、外部调用方），不是仓库内的包。

## 统计

| 维度 | 数量 |
| --- | ---: |
| 契约 | 34 |
| 其中 Go 接口 / 导出 API（`go-interface`） | 7 |
| 其中 HTTP 路由族群（`http`） | 8 |
| 其中 SSE 帧（`sse`） | 3 |
| 其中 事件总线（`event`） | 3 |
| 其中 磁盘格式（`disk`） | 8 |
| 其中 工具 schema（`tool-schema`） | 2 |
| 其中 CLI 命令（`cli`） | 3 |
| HTTP 路由（`internal/server`，method+path） | 220 |
| EventType 常量 | 77 |
| 磁盘格式（人工维护） | 8 |

## 兼容规则口径

| compatibility | 含义 | 允许的变更 |
| --- | --- | --- |
| `additive` | 加法兼容 | 新增端点、字段、事件类型、工具或可选 JSON key；消费方必须容忍未知字段。 |
| `breaking` | 破坏性 | 任何签名/路径/字段/字符串值的删除或改名都要同步改所有消费方；需要新接口或新版本，不能原地改。 |
| `versioned` | 版本化 | 文件或库自带版本号（soul.json `version`、tracker.db `PRAGMA user_version`），按版本迁移并保留回滚路径。 |

## 按 kind 汇总

| kind | 条数 | 提供方模块 | 消费方模块 | 强制手段 |
| --- | ---: | --- | --- | --- |
| Go 接口 / 导出 API（`go-interface`） | 7 | `internal/agentloop`、`internal/cli`、`internal/colony`、`internal/eventbus`、`internal/host`、`internal/llm`、`internal/server` | `internal/agentloop`、`internal/cli`、`internal/colony`、`internal/host`、`internal/memory`、`internal/server` | 编译器 / 接口实现、单元、集成或 e2e 测试 |
| HTTP 路由族群（`http`） | 8 | `internal/server` | `ext:external-caller`、`ext:frontend`、`internal/cli` | gen_arch --check 防漂移、单元、集成或 e2e 测试 |
| SSE 帧（`sse`） | 3 | `internal/eventbus`、`internal/runtimehealth`、`internal/server` | `ext:frontend` | gen_arch --check 防漂移、单元、集成或 e2e 测试 |
| 事件总线（`event`） | 3 | `internal/colony`、`internal/eventbus` | `ext:frontend`、`internal/cli`、`internal/colony`、`internal/host`、`internal/server`、`internal/tasks` | gen_arch --check 防漂移、编译器 / 接口实现、单元、集成或 e2e 测试 |
| 磁盘格式（`disk`） | 8 | `internal/agentloop`、`internal/colony`、`internal/server`、`internal/session`、`internal/soul`、`internal/storage`、`internal/tracker`、`internal/triggers` | `internal/agentloop`、`internal/agents`、`internal/cli`、`internal/colony`、`internal/host`、`internal/server`、`internal/session`、`internal/soul`、`internal/storage`、`internal/tracker`、`internal/triggers` | 单元、集成或 e2e 测试 |
| 工具 schema（`tool-schema`） | 2 | `internal/llm`、`internal/tools`、`internal/tools/integrations` | `internal/agentloop`、`internal/colony`、`internal/llm`、`internal/server` | 编译器 / 接口实现、单元、集成或 e2e 测试 |
| CLI 命令（`cli`） | 3 | `internal/cli`、`internal/colony`、`internal/server` | `ext:operator` | 单元、集成或 e2e 测试 |

## 契约明细

### Go 接口 / 导出 API（`go-interface`）

#### `agentloop.Judge`

- **定义**：`internal/agentloop/judge.go:Judge（Evaluate）`
- **提供方**：`internal/agentloop`
- **消费方**：`internal/agentloop`、`internal/colony`
- **保证**：成功标准判定通过该接口注入；agentloop 提供 LLMJudgeAdapter（底层 llmjudge），colony 用它做 worker 目标验收。
- **失败行为**：judge 返回 error 时归一化为 RETRY 并附反馈；无 judge 且声明了成功标准时判定为 unsupported，不计为 met，不假装通过。
- **兼容规则**：`breaking`
- **强制手段**：编译器 / 接口实现、单元、集成或 e2e 测试
- **所属层**：`runtime`

#### `agentloop.ToolExecutor`

- **定义**：`internal/agentloop/loop.go:ToolExecutor（func(ToolCallRequest) (ToolResult, error)）`
- **提供方**：`internal/colony`、`internal/server`、`internal/cli`
- **消费方**：`internal/agentloop`
- **保证**：循环只认这个函数：工具名解析、权限、限速、审计都在 executor 侧；循环负责批处理、并发安全分组与结果写回。
- **失败行为**：executor 返回 error 时，循环把错误文本作为 is_error 工具结果回给模型，不终止整轮。
- **兼容规则**：`breaking`
- **强制手段**：编译器 / 接口实现、单元、集成或 e2e 测试
- **所属层**：`runtime`

#### `eventbus.EventHandler`

- **定义**：`internal/eventbus/bus.go:EventHandler / Subscribe`
- **提供方**：`internal/eventbus`
- **消费方**：`internal/server`、`internal/colony`、`internal/host`
- **保证**：订阅者按 EventType 集合与 stream/node/execution/colony 过滤接收 *AgentEvent；Bus 同步分发，返回订阅 ID 供退订。
- **失败行为**：handler panic 被 recover 并记日志，不影响其他订阅者；handler 阻塞会拖慢同步发布，队列满的 SSE 桥会主动丢帧或断开。
- **兼容规则**：`breaking`
- **强制手段**：编译器 / 接口实现、单元、集成或 e2e 测试
- **所属层**：`foundation`

#### `host.Host`

- **定义**：`internal/host/host.go:Host（New / Start / Spawn / Run / Bus / Stats / Stop）`
- **提供方**：`internal/host`
- **消费方**：`internal/cli`
- **保证**：把 colony runtime、event bus 与 run 注册表封装成可 Start/Spawn 的宿主；Spawn 非阻塞，Run 阻塞，进度经 Bus/Stats 读取。
- **失败行为**：New 缺 LLM 或 AgentSpec.ID 返回 error；未 Start 时 Spawn/Run 先 ensureStarted；Stop 后由 Started() 反映状态。
- **兼容规则**：`breaking`
- **强制手段**：编译器 / 接口实现、单元、集成或 e2e 测试
- **所属层**：`orchestration`

#### `llm.Completer`

- **定义**：`internal/llm/llm.go:Completer（AComplete）`
- **提供方**：`internal/llm`
- **消费方**：`internal/agentloop`、`internal/colony`、`internal/host`、`internal/memory`、`internal/server`
- **保证**：所有非流式模型调用都走同一个 AComplete(ctx, CompletionRequest) → Completion；provider 选择、协议、鉴权、限速与重试封在 llm 内部，调用方不感知 Anthropic/OpenAI 差异。
- **失败行为**：返回 error 表示没有完整回复；调用方不得把部分文本当完成结果持久化。
- **兼容规则**：`breaking`
- **强制手段**：编译器 / 接口实现、单元、集成或 e2e 测试
- **所属层**：`capability`

#### `llm.StreamCompleter`

- **定义**：`internal/llm/stream.go:StreamCompleter（ACompleteStream）`
- **提供方**：`internal/llm`
- **消费方**：`internal/server`
- **保证**：支持流式的 provider 通过 emit 按线上顺序发 TextDelta / ToolCall / TextEnd / Finish；返回的 Completion 与非流式路径一致，可直接写历史。
- **失败行为**：ErrStreamingUnsupported 或流中断时 server 回退到 Completer.AComplete；error 时不得把部分文本当最终回答。
- **兼容规则**：`breaking`
- **强制手段**：编译器 / 接口实现、单元、集成或 e2e 测试
- **所属层**：`capability`

#### `server.ColonyRuntime`

- **定义**：`internal/server/session.go:ColonyRuntime（StreamID / SubscribeToEvents / InjectInput）`
- **提供方**：`internal/colony`、`internal/host`
- **消费方**：`internal/server`
- **保证**：传输层只通过这三个方法观察 colony 流、订阅其事件、向 worker 注入输入，不触达 *colony.ColonyRuntime 的内部字段。
- **失败行为**：少实现一个方法即编译失败；SubscribeToEvents 返回错误时 HTTP 层按 500 结束该请求。
- **兼容规则**：`breaking`
- **强制手段**：编译器 / 接口实现、单元、集成或 e2e 测试
- **所属层**：`transport`

### HTTP 路由族群（`http`）

#### `http.colonies`

- **定义**：`internal/server/routes_colonies.go:RegisterColonyRoutes + routes_colony_tools.go + routes_colony_workers_data.go`
- **提供方**：`internal/server`
- **消费方**：`ext:frontend`
- **保证**：colony 生命周期与数据面：scaffold/rename/delete/import、worker profiles、colony 数据表行查询与 PATCH；scaffold 幂等，不覆盖已有 triggers.json。
- **失败行为**：不存在 404；非法 colony 名 400；导入分片缺 upload_id 404；重复 scaffold 保持原内容。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`transport`

#### `http.config`

- **定义**：`internal/server/routes_config.go:RegisterConfigRoutes + routes_models.go`
- **提供方**：`internal/server`
- **消费方**：`ext:frontend`
- **保证**：configuration.json 的读写面：features / llm / rate-limits / profile / provider-library / sentinel / external-skills，以及只读 models 目录；写入以 JSON 合并 + 原子替换落盘。
- **失败行为**：非法 JSON 或未知枚举 400；读取失败回退默认配置；写盘失败 500，内存状态不先行生效。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`transport`

#### `http.credentials`

- **定义**：`internal/server/routes_credentials.go:RegisterCredentialRoutes + routes_credentials_specs.go + routes_credentials_form.go`
- **提供方**：`internal/server`
- **消费方**：`ext:frontend`
- **保证**：凭证 CRUD、规格列表、OAuth 状态、凭证表单提交；GET 只返回元数据，秘密值只写不读。
- **失败行为**：未知凭证 404；缺字段 400；解密失败时单条 GET 返回 409 且 recoverable=true，列表把不可读 id 放进 unreadable_credentials，不泄漏秘密；表单提交会恢复被 park 的 turn。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`transport`

#### `http.events`

- **定义**：`internal/server/routes_events.go:RegisterEventRoutes`
- **提供方**：`internal/server`
- **消费方**：`ext:frontend`、`ext:external-caller`
- **保证**：全局事件入口：GET /api/events/global 是 SSE（见 sse.global-events）；POST /api/crm/changed 接受 {entities[], schema} 并广播 crm_changed。
- **失败行为**：非 SSE 客户端 500；/api/crm/changed 的坏 JSON 被当空对象；未知实体丢弃；成功恒为 200 {ok:true}。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`transport`

#### `http.global-data`

- **定义**：`internal/server/routes_colony_workers_global.go（/api/global/* 与 /api/crm/grid/*）`
- **提供方**：`internal/server`
- **消费方**：`ext:frontend`
- **保证**：global data 与 CRM grid 的通用表读写：表名/实体名走路径，查询体为自由 JSON，行操作返回 rows/columns 等通用结构。
- **失败行为**：未知表/实体 404；非法查询或未知列 400；SQLite 约束/写冲突由 handler 映射为 4xx 或 5xx，响应体带 error。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`transport`

#### `http.mcp`

- **定义**：`internal/server/routes_mcp.go:RegisterMCPRoutes`
- **提供方**：`internal/server`
- **消费方**：`ext:frontend`
- **保证**：MCP 服务器注册表 HTTP 面：list/add/delete/enable/disable/health/mode/reload；返回 server 视图，工具命名空间由 tools 层决定。
- **失败行为**：未安装 server 404；健康检查失败在响应体标 ok=false 而非 5xx；reload 部分失败返回 500 且带 reloaded=false。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`transport`

#### `http.queens`

- **定义**：`internal/server/routes_queens.go:RegisterQueenRoutes + routes_queens_tools.go + routes_queens_claude_code.go + routes_queens_mail.go`
- **提供方**：`internal/server`
- **消费方**：`ext:frontend`
- **保证**：queen 档案、人格、头像、工具 allowlist、会话绑定与 queen 间信件；列表响应带 profiles/queens 键，单对象按 profile/overview 返回。
- **失败行为**：未知 queen 404；非法 profile 400；删除/恢复是软删除标记，重复操作幂等。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`transport`

#### `http.sessions`

- **定义**：`internal/server/routes_sessions.go:RegisterSessionRoutes + chat/task/worker/reminder/attachment 路由`
- **提供方**：`internal/server`
- **消费方**：`ext:frontend`、`internal/cli`
- **保证**：会话 REST：创建/查询/fork/发送消息/任务/worker/提醒/触发器/报告；请求与响应为 JSON，错误统一 {error:...}；chat 接受后立即返回，输出走 SSE。
- **失败行为**：未知 session 404；colony 已 spawn 或已 fork 409 session_locked；未配置 LLM 503；坏 JSON 400。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`transport`

### SSE 帧（`sse`）

#### `sse.global-events`

- **定义**：`internal/server/routes_events.go:handleGlobalEvents（GET /api/events/global）`
- **提供方**：`internal/server`、`internal/eventbus`
- **消费方**：`ext:frontend`
- **保证**：只转发 5 类全局事件（credential connect/disconnect、tool catalog refresh、tools config、crm changed）；帧是 data-only（无 event 名），空闲发 ': keepalive'。
- **失败行为**：非 Flusher 500；队列满即断开让客户端重连；globalEventTypes 是白名单，新增全局事件必须同步改它。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`transport`

#### `sse.live-streams`

- **定义**：`internal/server/routes_session_live.go:handleLiveSessionsStream + routes_workers.go:handleLiveWorkersStream + routes_app_extra.go:handleBrowserStatusStream`
- **提供方**：`internal/server`、`internal/runtimehealth`
- **消费方**：`ext:frontend`
- **保证**：轮询式 SSE：/api/sessions/live 与 /api/sessions/{id}/workers/stream 发 event: snapshot，/api/browser/status/stream 发 event: status；只有签名变化才推送，空闲发 keepalive 注释。
- **失败行为**：非 Flusher 500；会话或运行时缺失返回 404/空快照；轮询无变化时不发帧。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`transport`

#### `sse.session-events`

- **定义**：`internal/server/chat_routes.go:handleSessionEventsStream（GET /api/sessions/{session_id}/events）`
- **提供方**：`internal/server`、`internal/eventbus`
- **消费方**：`ext:frontend`
- **保证**：每帧 event: <EventType> + data: <AgentEvent.ToDict()>；订阅时先发 event: session_snapshot；空闲发 ': heartbeat' 注释；?types=a,b 可过滤。
- **失败行为**：非 Flusher 500；会话不存在 404；队列满时非阻塞丢帧，客户端靠 snapshot 与重连恢复。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`transport`

### 事件总线（`event`）

#### `event.agent-event-envelope`

- **定义**：`internal/eventbus/event.go:AgentEvent / ToDict`
- **提供方**：`internal/eventbus`
- **消费方**：`internal/server`、`internal/colony`、`internal/host`、`internal/tasks`、`internal/cli`、`ext:frontend`
- **保证**：统一信封：type / stream_id / node_id / execution_id / data / timestamp / correlation_id / colony_id / seq，可选 run_id；timestamp 为本地 ISO8601（微秒可选）；落盘剥离 data.full_request。
- **失败行为**：Data 为 nil 时 ToDict 归一为空 map；字段不做运行期类型校验，消费方按 map 断言，缺失即零值。
- **兼容规则**：`additive`
- **强制手段**：编译器 / 接口实现、单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`foundation`

#### `event.type-registry`

- **定义**：`internal/eventbus/event_type.go:EventType / AllEventTypes`
- **提供方**：`internal/eventbus`
- **消费方**：`internal/server`、`internal/colony`、`internal/host`、`internal/tasks`、`internal/cli`
- **保证**：事件类型是字符串常量集合，AllEventTypes 是订阅与文档的唯一清单；新增类型必须同时加常量与清单。
- **失败行为**：拼写错误不会编译失败（字符串类型）；未知类型仍可发布，订阅方收不到；改已有字符串值等于破坏订阅方。
- **兼容规则**：`additive`
- **强制手段**：编译器 / 接口实现、单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`foundation`

#### `event.worker-report`

- **定义**：`internal/colony/runtime.go:publishReport + internal/colony/worker.go:reportPayload`
- **提供方**：`internal/colony`
- **消费方**：`internal/server`、`ext:frontend`
- **保证**：worker 终态统一发 subagent_report：worker_id / colony_id / status / summary / task / data，可选 goal_evaluation、improvement_signals、batch_id/index/size、duration_seconds；ChatRuntime 将其转成 [WORKER_REPORT] 用户回合。
- **失败行为**：缺 data 时补空 map；status 只认 success/partial/failed；报告发布无返回值，失败只记日志，不回滚 worker 终态。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试、gen_arch --check 防漂移
- **所属层**：`orchestration`

### 磁盘格式（`disk`）

#### `disk.colony-metadata`

- **定义**：`internal/server/routes_colonies.go:updateColonyMetadata（~/.hive/colonies/{colony}/metadata.json）`
- **提供方**：`internal/server`
- **消费方**：`internal/server`、`internal/host`、`internal/cli`
- **保证**：colony 级元数据自由 map：name / created_at / queen_name / scaffolded / deleted / source_session；scaffold、rename、delete 都走原子写。
- **失败行为**：缺失或坏 JSON 视为空 map；写入失败返回 500；deleted 是软删除标记，不立刻删目录。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试
- **所属层**：`orchestration`

#### `disk.colony-worker-json`

- **定义**：`internal/server/routes_colonies.go:ensureMinimalColony（~/.hive/colonies/{colony}/worker.json）`
- **提供方**：`internal/server`、`internal/colony`
- **消费方**：`internal/agents`、`internal/colony`、`internal/cli`
- **保证**：colony 的 worker 模板：name / description / tools[] / goal{description,success_criteria,constraints} / queen_name / loop_config；agents.LoadAgentSpec 按文件名排序取第一个 worker 配置。
- **失败行为**：缺失时 scaffold 生成最小模板；JSON 损坏时 LoadAgentSpec 报错、colony 无法派工；未知字段忽略。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试
- **所属层**：`orchestration`

#### `disk.cursor-json`

- **定义**：`internal/session/session.go:Cursor / WriteCursor + internal/agentloop/store.go`
- **提供方**：`internal/agentloop`、`internal/session`、`internal/storage`
- **消费方**：`internal/agentloop`、`internal/session`、`internal/storage`、`internal/server`
- **保证**：turn 游标：iteration / turn / accumulated_output / tool_calls / stall_repeat / last_fingerprint / pending_inputs / park_reason / updated_at，park 后可精确恢复。
- **失败行为**：损坏或截断按不存在处理（readDerivedJSON），从 parts 重建；写失败只影响恢复精度，不影响当前 turn。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试
- **所属层**：`orchestration`

#### `disk.session-meta`

- **定义**：`internal/server/queen_phase_state.go:mergeSessionMeta（sessions/{id}/meta.json）`
- **提供方**：`internal/server`、`internal/session`
- **消费方**：`internal/server`、`internal/session`
- **保证**：会话级元数据：title / title_source、queen phase、colony_spawned / spawned_colony_id / spawned_colony_at；写入按 key 合并并原子替换。
- **失败行为**：损坏时读为空，标题与 phase 回退默认；缺 colony_spawned 时按未 spawn 处理。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试
- **所属层**：`orchestration`

#### `disk.soul-json`

- **定义**：`internal/soul/store.go:SoulFile / Save（~/.hive/queens/{queen}/soul/soul.json）`
- **提供方**：`internal/soul`
- **消费方**：`internal/soul`、`internal/server`
- **保证**：Queen 自我认知：version / updated_at / sections{key:[Item]}；history/v{n}.json 保留版本，proposals/*.json 存待审提案；写前做分区字符上限校验。
- **失败行为**：缺失时 Load 返回空 soul；越界 edit 被 Problems/ApplyEdits 拒绝并返回 SoulError；回滚按 version 读历史。
- **兼容规则**：`versioned`
- **强制手段**：单元、集成或 e2e 测试
- **所属层**：`capability`

#### `disk.tracker-db`

- **定义**：`internal/tracker/tracker.go:EnsureTrackerDB / bootstrapSchema（~/.hive/colonies/{colony}/tracker/tracker.db）`
- **提供方**：`internal/tracker`
- **消费方**：`internal/colony`、`internal/tracker`、`internal/server`
- **保证**：SQLite(WAL)：_tracker_registry 登记可写列/键/模式，_tracker_meta 存 schema_version，_tracker_changes 记录 insert/update；业务表由 queen 动态建。
- **失败行为**：PRAGMA user_version 小于 SchemaVersion(3) 时执行 bootstrap + backfill；busy_timeout 5s，写冲突由工具层归一为 is_error。
- **兼容规则**：`versioned`
- **强制手段**：单元、集成或 e2e 测试
- **所属层**：`orchestration`

#### `disk.triggers-json`

- **定义**：`internal/triggers/triggers_store.go:ReadAgentTriggersJSON / WriteAgentTriggersJSON`
- **提供方**：`internal/triggers`、`internal/server`
- **消费方**：`internal/triggers`、`internal/server`
- **保证**：顶层数组，每项 TriggerDefinition：id / trigger_type / trigger_config / description / task / enabled / last_fired_at / next_due_at；按 id 覆盖写。
- **失败行为**：文件缺失、坏 JSON、非数组一律读成空数组；写失败向上返回 error，调用方记日志。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试
- **所属层**：`orchestration`

#### `disk.worker-meta`

- **定义**：`internal/colony/worker_transcript.go:writeWorkerMeta（workers/{id}/meta.json）`
- **提供方**：`internal/colony`
- **消费方**：`internal/colony`、`internal/server`
- **保证**：worker 重启所需的血缘：worker_id / colony_id / task / profile_name / goal / batch_id / batch_index / batch_size；与 conversations/、result.json、events.jsonl 同目录。
- **失败行为**：缺失或坏 JSON 时 readWorkerMeta 返回空 map，resume 退化为按 parts 恢复；worker 目录无 parts 则不可 resume。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试
- **所属层**：`orchestration`

### 工具 schema（`tool-schema`）

#### `tool-schema.mcp-namespace`

- **定义**：`internal/tools/mcp_registry.go:NamespacedMCPToolName`
- **提供方**：`internal/tools`
- **消费方**：`internal/agentloop`、`internal/server`、`internal/colony`
- **保证**：MCP 工具注册名固定 mcp__<server>__<tool>；server/tool 中非 [A-Za-z0-9_] 字符折叠为 _；命名空间阻止远端 read_file 覆盖内置工具。
- **失败行为**：清洗后为空回退 server/tool；同名冲突按加载顺序 shadow 并记日志（preserve 模式跳过）；调用失败归一为 is_error 工具结果。
- **兼容规则**：`additive`
- **强制手段**：编译器 / 接口实现、单元、集成或 e2e 测试
- **所属层**：`capability`

#### `tool-schema.tool-def`

- **定义**：`internal/llm/llm.go:ToolDef + internal/tools/tool.go:Tool.Schema`
- **提供方**：`internal/tools`、`internal/llm`、`internal/tools/integrations`
- **消费方**：`internal/agentloop`、`internal/llm`
- **保证**：工具对模型的唯一描述：name / description / input_schema(JSON Schema object) / produces_image / concurrency_safe；registry 的 Tool 转成 {name,description,input_schema} 进入 CompletionRequest.Tools。
- **失败行为**：Parameters 为 nil 时补空 object schema；schema 不做本地校验，provider 可能 400；重名工具的取舍由 registry 加载规则决定。
- **兼容规则**：`additive`
- **强制手段**：编译器 / 接口实现、单元、集成或 e2e 测试
- **所属层**：`capability`

### CLI 命令（`cli`）

#### `cli.chat`

- **定义**：`internal/cli/loader.go:chatCommand + cmdChat`
- **提供方**：`internal/cli`、`internal/server`
- **消费方**：`ext:operator`
- **保证**：hive chat <session_id> <message> [--server URL] 通过 HTTP POST /api/sessions/{id}/chat 投递，成功打印 SSE 跟踪地址；默认 server 为 http://127.0.0.1:8787。
- **失败行为**：网络不可达或响应带 error 时打印错误并返回 1；409 session_locked 原样透出。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试
- **所属层**：`assembly`

#### `cli.colony`

- **定义**：`internal/cli/loader.go:colonyCommands（hive colony list|info|delete）`
- **提供方**：`internal/cli`、`internal/colony`
- **消费方**：`ext:operator`
- **保证**：list 枚举 ~/.hive/colonies 下的 colony；info 读 metadata.json/worker.json；delete 删目录（--purge-storage 同时删 agents 存储，--yes 跳过确认）；--json 输出机器可读结构。
- **失败行为**：不存在返回非 0 并打印 not found；delete 无 --yes 时交互确认；purge 失败返回非 0 并报告已删除/未删除部分。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试
- **所属层**：`assembly`

#### `cli.serve`

- **定义**：`internal/cli/loader.go:serveCommand（hive serve / hive open）`
- **提供方**：`internal/cli`、`internal/server`
- **消费方**：`ext:operator`
- **保证**：默认绑定 127.0.0.1:8787；支持 --host / --port / --colony(可重复) / --model / --open / --verbose / --debug；启动 HTTP+SSE 并阻塞到信号；退出码 0 正常、非 0 启动失败。
- **失败行为**：端口占用或监听失败打印错误并返回非 0；非 loopback 且未设 HIVE_DESKTOP_TOKEN 时拒绝启动返回 1；SIGINT/SIGTERM 触发优雅停止。
- **兼容规则**：`additive`
- **强制手段**：单元、集成或 e2e 测试
- **所属层**：`assembly`

## HTTP 路由表

从 `internal/server` 的非测试文件里正则提取 `mux.HandleFunc("METHOD /path", handler)`：**只覆盖标准库 ServeMux 的「方法 + 路径」式注册**。handler 列取注册闭包中第一个具名调用（优先 `handle*`），纯内联逻辑写 `(inline)`；它只用于阅读，不作为门禁判断。`http.*` 契约是按治理职责划分的族群，不是互斥路由集合；同一条路由可以同时属于一个族群与另一族群的语义边界，全量以本表为准。

共 **220** 条：DELETE 19、GET 89、PATCH 11、POST 84、PUT 17

| 方法 | 路径 | handler | 来源 |
| --- | --- | --- | --- |
| GET | `/` | `app.handleSPA` | `internal/server/app.go:331` |
| DELETE | `/api/agents` | `handleDeleteAgent` | `internal/server/routes_agents.go:30` |
| PATCH | `/api/agents/metadata` | `handleUpdateAgentMetadata` | `internal/server/routes_agents.go:33` |
| GET | `/api/browser/contexts` | `handleBrowserPanelContexts` | `internal/server/routes_browser_panel.go:86` |
| POST | `/api/browser/contexts/close` | `handleBrowserPanelClose` | `internal/server/routes_browser_panel.go:92` |
| GET | `/api/browser/status` | `handleBrowserStatus` | `internal/server/routes_app_extra.go:149` |
| GET | `/api/browser/status/stream` | `handleBrowserStatusStream` | `internal/server/routes_app_extra.go:152` |
| POST | `/api/browser/tab/reveal` | `handleBrowserTabReveal` | `internal/server/routes_app_extra.go:155` |
| GET | `/api/browser/tabs` | `handleBrowserPanelTabs` | `internal/server/routes_browser_panel.go:89` |
| GET | `/api/channels` | `handleListChannels` | `internal/server/routes_credentials_channels.go:18` |
| GET | `/api/claude-code-queens` | `handleClaudeCodeQueenList` | `internal/server/routes_queens_claude_code.go:136` |
| GET | `/api/claude-code-queens/{queen_id}/conversations/{session_id}` | `handleClaudeCodeQueenTranscript` | `internal/server/routes_queens_claude_code.go:139` |
| POST | `/api/colonies/import` | `handleImportColony` | `internal/server/routes_colonies.go:41` |
| PUT | `/api/colonies/import/chunk/{upload_id}` | `handleChunkUpload` | `internal/server/routes_colonies.go:47` |
| POST | `/api/colonies/import/finalize/{upload_id}` | `handleFinalizeUpload` | `internal/server/routes_colonies.go:53` |
| POST | `/api/colonies/import/init` | `handleInitUpload` | `internal/server/routes_colonies.go:44` |
| GET | `/api/colonies/import/status/{upload_id}` | `handleUploadStatus` | `internal/server/routes_colonies.go:50` |
| DELETE | `/api/colonies/import/{upload_id}` | `handleCancelUpload` | `internal/server/routes_colonies.go:56` |
| GET | `/api/colonies/tools-index` | `handleListColoniesToolsIndex` | `internal/server/routes_colony_tools.go:45` |
| DELETE | `/api/colonies/{colony_id}` | `handleDeleteColony` | `internal/server/routes_colonies.go:62` |
| GET | `/api/colonies/{colony_id}/active-session` | `handleActiveColonySession` | `internal/server/routes_sessions.go:141` |
| GET | `/api/colonies/{colony_id}/data/changes` | `handleColonyTableChanges` | `internal/server/routes_colony_workers_data.go:915` |
| GET | `/api/colonies/{colony_id}/data/tables` | `handleColonyListTables` | `internal/server/routes_colony_workers_data.go:912` |
| GET | `/api/colonies/{colony_id}/data/tables/{table}/rows` | `handleColonyTableRows` | `internal/server/routes_colony_workers_data.go:918` |
| PATCH | `/api/colonies/{colony_id}/data/tables/{table}/rows` | `handleColonyUpdateRow` | `internal/server/routes_colony_workers_data.go:921` |
| POST | `/api/colonies/{colony_id}/rename` | `handleRenameColony` | `internal/server/routes_colonies.go:68` |
| POST | `/api/colonies/{colony_id}/reveal` | `handleRevealColonyFolder` | `internal/server/routes_colonies.go:65` |
| POST | `/api/colonies/{colony_id}/scaffold` | `handleScaffoldColony` | `internal/server/routes_colonies.go:59` |
| GET | `/api/colonies/{colony_id}/sessions` | `handleListColonySessions` | `internal/server/routes_sessions.go:138` |
| GET | `/api/colonies/{colony_id}/worker_profiles` | `handleListWorkerProfiles` | `internal/server/routes_colonies.go:71` |
| POST | `/api/colonies/{colony_id}/worker_profiles` | `handleUpsertWorkerProfile` | `internal/server/routes_colonies.go:74` |
| DELETE | `/api/colonies/{colony_id}/worker_profiles/{profile_name}` | `handleDeleteWorkerProfile` | `internal/server/routes_colonies.go:77` |
| GET | `/api/colony/{colony_id}/notifications` | `handleGetNotifications` | `internal/server/routes_sentinel.go:104` |
| PUT | `/api/colony/{colony_id}/notifications` | `handlePutNotifications` | `internal/server/routes_sentinel.go:107` |
| POST | `/api/colony/{colony_id}/notifications/test` | `handleTestNotification` | `internal/server/routes_sentinel.go:110` |
| GET | `/api/colony/{colony_id}/tools` | `handleGetColonyTools` | `internal/server/routes_colony_tools.go:48` |
| PATCH | `/api/colony/{colony_id}/tools` | `handlePatchColonyTools` | `internal/server/routes_colony_tools.go:51` |
| GET | `/api/config/external-skills` | `handleGetExternalSkills` | `internal/server/routes_config.go:294` |
| PUT | `/api/config/external-skills` | `handlePutExternalSkills` | `internal/server/routes_config.go:295` |
| GET | `/api/config/features` | `handleGetFeatures` | `internal/server/routes_config.go:279` |
| PUT | `/api/config/features` | `handleUpdateFeatures` | `internal/server/routes_config.go:280` |
| GET | `/api/config/llm` | `handleGetLLMConfig` | `internal/server/routes_config.go:283` |
| PUT | `/api/config/llm` | `handleUpdateLLMConfig` | `internal/server/routes_config.go:284` |
| GET | `/api/config/llm-sections` | `handleGetLLMSections` | `internal/server/routes_config.go:281` |
| PUT | `/api/config/llm-sections` | `handlePutLLMSection` | `internal/server/routes_config.go:282` |
| GET | `/api/config/models` | `handleGetModels` | `internal/server/routes_config.go:276` |
| GET | `/api/config/profile` | `handleGetProfile` | `internal/server/routes_config.go:290` |
| PUT | `/api/config/profile` | `handleUpdateProfile` | `internal/server/routes_config.go:291` |
| GET | `/api/config/profile/avatar` | `handleGetAvatar` | `internal/server/routes_config.go:293` |
| POST | `/api/config/profile/avatar` | `handleUploadAvatar` | `internal/server/routes_config.go:292` |
| GET | `/api/config/provider-library` | `handleGetProviderLibrary` | `internal/server/routes_config.go:285` |
| PUT | `/api/config/provider-library` | `handlePutProviderLibrary` | `internal/server/routes_config.go:286` |
| POST | `/api/config/provider-library/apply` | `handleApplyProviderLibrary` | `internal/server/routes_config.go:287` |
| GET | `/api/config/rate-limits` | `handleGetRateLimits` | `internal/server/routes_config.go:288` |
| PUT | `/api/config/rate-limits` | `handleUpdateRateLimits` | `internal/server/routes_config.go:289` |
| GET | `/api/config/sentinel` | `handleGetSentinel` | `internal/server/routes_config.go:277` |
| PUT | `/api/config/sentinel` | `handleUpdateSentinel` | `internal/server/routes_config.go:278` |
| GET | `/api/credentials` | `routes.handleListCredentials` | `internal/server/routes_credentials.go:78` |
| POST | `/api/credentials` | `routes.handleSaveCredential` | `internal/server/routes_credentials.go:79` |
| POST | `/api/credentials/check-agent` | `handleCheckAgent` | `internal/server/routes_agents.go:36` |
| POST | `/api/credentials/local` | `routes.handleSaveLocalCredential` | `internal/server/routes_credentials_local.go:59` |
| DELETE | `/api/credentials/local/{credential_id}/{alias}` | `routes.handleDeleteLocalCredential` | `internal/server/routes_credentials_local.go:60` |
| GET | `/api/credentials/oauth-status` | `routes.handleOAuthStatus` | `internal/server/routes_credentials.go:75` |
| POST | `/api/credentials/resync` | `routes.handleResync` | `internal/server/routes_credentials.go:76` |
| GET | `/api/credentials/specs` | `routes.handleListSpecs` | `internal/server/routes_credentials.go:74` |
| POST | `/api/credentials/validate-key` | `routes.handleValidateKey` | `internal/server/routes_credentials.go:77` |
| DELETE | `/api/credentials/{credential_id}` | `routes.handleDeleteCredential` | `internal/server/routes_credentials.go:85` |
| GET | `/api/credentials/{credential_id}` | `routes.handleGetCredential` | `internal/server/routes_credentials.go:84` |
| POST | `/api/crm/changed` | `handleCRMChanged` | `internal/server/routes_events.go:73` |
| POST | `/api/crm/claim` | `handleCRMProxy` | `internal/server/routes_colony_workers_global.go:787` |
| GET | `/api/crm/grid/schema` | `handleCRMProxy` | `internal/server/routes_colony_workers_global.go:752` |
| POST | `/api/crm/grid/{entity}` | `handleCRMProxy` | `internal/server/routes_colony_workers_global.go:768` |
| POST | `/api/crm/grid/{entity}/group-counts` | `handleCRMProxy` | `internal/server/routes_colony_workers_global.go:759` |
| POST | `/api/crm/grid/{entity}/query` | `handleCRMProxy` | `internal/server/routes_colony_workers_global.go:755` |
| DELETE | `/api/crm/grid/{entity}/{id}` | `handleCRMProxy` | `internal/server/routes_colony_workers_global.go:777` |
| GET | `/api/crm/grid/{entity}/{id}` | `handleCRMProxy` | `internal/server/routes_colony_workers_global.go:763` |
| PATCH | `/api/crm/grid/{entity}/{id}` | `handleCRMProxy` | `internal/server/routes_colony_workers_global.go:772` |
| POST | `/api/crm/grid/{entity}/{id}/stage` | `handleCRMProxy` | `internal/server/routes_colony_workers_global.go:782` |
| POST | `/api/crm/release` | `handleCRMProxy` | `internal/server/routes_colony_workers_global.go:790` |
| GET | `/api/discover` | `handleDiscoverAgents` | `internal/server/routes_agents.go:27` |
| GET | `/api/events/global` | `handleGlobalEvents` | `internal/server/routes_events.go:70` |
| GET | `/api/global/crm/status` | `handleGlobalCRMStatus` | `internal/server/routes_colony_workers_global.go:722` |
| GET | `/api/global/data/changes` | `handleGlobalChanges` | `internal/server/routes_colony_workers_global.go:728` |
| GET | `/api/global/data/foreign-keys` | `handleGlobalForeignKeys` | `internal/server/routes_colony_workers_global.go:731` |
| GET | `/api/global/data/tables` | `handleGlobalListTables` | `internal/server/routes_colony_workers_global.go:725` |
| POST | `/api/global/data/tables/{table}/group-counts` | `handleGlobalTableGroupCounts` | `internal/server/routes_colony_workers_global.go:740` |
| POST | `/api/global/data/tables/{table}/query` | `handleGlobalTableQuery` | `internal/server/routes_colony_workers_global.go:737` |
| DELETE | `/api/global/data/tables/{table}/rows` | `handleGlobalDeleteRow` | `internal/server/routes_colony_workers_global.go:749` |
| GET | `/api/global/data/tables/{table}/rows` | `handleGlobalTableRows` | `internal/server/routes_colony_workers_global.go:734` |
| PATCH | `/api/global/data/tables/{table}/rows` | `handleGlobalUpdateRowRoute` | `internal/server/routes_colony_workers_global.go:746` |
| POST | `/api/global/data/tables/{table}/rows` | `handleGlobalInsertRow` | `internal/server/routes_colony_workers_global.go:743` |
| GET | `/api/health` | `app.handleHealth` | `internal/server/app.go:175` |
| GET | `/api/health/resources` | `handleHealthResources` | `internal/server/routes_app_extra.go:146` |
| GET | `/api/maintenance/janitor/report` | `handleJanitorReport` | `internal/server/routes_maintenance.go:80` |
| POST | `/api/maintenance/janitor/run` | `handleJanitorRun` | `internal/server/routes_maintenance.go:77` |
| POST | `/api/mcp/reload` | `handleReloadMCP` | `internal/server/routes_mcp.go:92` |
| GET | `/api/mcp/servers` | `handleListMCPServers` | `internal/server/routes_mcp.go:71` |
| POST | `/api/mcp/servers` | `handleAddMCPServer` | `internal/server/routes_mcp.go:74` |
| DELETE | `/api/mcp/servers/{name}` | `handleRemoveMCPServer` | `internal/server/routes_mcp.go:77` |
| POST | `/api/mcp/servers/{name}/disable` | `handleDisableMCPServer` | `internal/server/routes_mcp.go:83` |
| POST | `/api/mcp/servers/{name}/enable` | `handleEnableMCPServer` | `internal/server/routes_mcp.go:80` |
| POST | `/api/mcp/servers/{name}/health` | `handleMCPHealth` | `internal/server/routes_mcp.go:86` |
| POST | `/api/mcp/servers/{name}/mode` | `handleSetMCPMode` | `internal/server/routes_mcp.go:89` |
| GET | `/api/memories` | `handleListMemories` | `internal/server/routes_memories_extra.go:22` |
| DELETE | `/api/memories/file` | `handleDeleteMemory` | `internal/server/routes_memories_extra.go:34` |
| GET | `/api/memories/file` | `handleGetMemory` | `internal/server/routes_memories_extra.go:25` |
| POST | `/api/memories/file` | `handleCreateMemory` | `internal/server/routes_memories_extra.go:28` |
| PUT | `/api/memories/file` | `handleUpdateMemory` | `internal/server/routes_memories_extra.go:31` |
| POST | `/api/messages/classify` | `handleClassifyMessage` | `internal/server/routes_messages_extra.go:66` |
| POST | `/api/messages/classify-colony` | `handleClassifyColony` | `internal/server/routes_messages_extra.go:69` |
| DELETE | `/api/models/endpoints/{name}` | `handleDeleteModelEndpoint` | `internal/server/routes_models.go:143` |
| PUT | `/api/models/endpoints/{name}` | `handlePutModelEndpoint` | `internal/server/routes_models.go:140` |
| GET | `/api/models/endpoints/{name}/models` | `handleModelEndpointModels` | `internal/server/routes_models.go:137` |
| GET | `/api/models/overview` | `handleModelsOverview` | `internal/server/routes_models.go:128` |
| DELETE | `/api/models/slots/{slot}` | `handleDeleteModelSlot` | `internal/server/routes_models.go:134` |
| PUT | `/api/models/slots/{slot}` | `handlePutModelSlot` | `internal/server/routes_models.go:131` |
| GET | `/api/prompts` | `handleCreatePrompt` | `internal/server/routes_prompts_extra.go:50` |
| POST | `/api/prompts` | `handleCreatePrompt` | `internal/server/routes_prompts_extra.go:53` |
| DELETE | `/api/prompts/{prompt_id}` | `handleDeletePrompt` | `internal/server/routes_prompts_extra.go:59` |
| PATCH | `/api/prompts/{prompt_id}` | `handleUpdatePrompt` | `internal/server/routes_prompts_extra.go:56` |
| POST | `/api/queen-mail/letters` | `handleQueenMailLetter` | `internal/server/routes_queens_mail.go:100` |
| GET | `/api/queen-mail/queens` | `handleQueenMailQueens` | `internal/server/routes_queens_mail.go:97` |
| POST | `/api/queen/create` | `handleQueenCreate` | `internal/server/routes_queens.go:1050` |
| POST | `/api/queen/initialize-preferences` | `handleQueenInitializePreferences` | `internal/server/routes_queens.go:1053` |
| GET | `/api/queen/profiles` | `handleQueenListProfiles` | `internal/server/routes_queens.go:1044` |
| GET | `/api/queen/removed` | `handleQueenListRemoved` | `internal/server/routes_queens.go:1047` |
| DELETE | `/api/queen/{queen_id}/avatar` | `handleQueenDeleteAvatar` | `internal/server/routes_queens.go:1100` |
| GET | `/api/queen/{queen_id}/avatar` | `handleQueenGetAvatar` | `internal/server/routes_queens.go:1097` |
| POST | `/api/queen/{queen_id}/avatar` | `handleQueenUploadAvatar` | `internal/server/routes_queens.go:1094` |
| GET | `/api/queen/{queen_id}/llm` | `handleQueenGetLLM` | `internal/server/routes_queens.go:1079` |
| PUT | `/api/queen/{queen_id}/llm` | `handleQueenPutLLM` | `internal/server/routes_queens.go:1082` |
| GET | `/api/queen/{queen_id}/overview` | `handleQueenOverview` | `internal/server/routes_queens.go:1091` |
| GET | `/api/queen/{queen_id}/persona` | `handleQueenGetPersona` | `internal/server/routes_queens.go:1085` |
| PUT | `/api/queen/{queen_id}/persona` | `handleQueenPutPersona` | `internal/server/routes_queens.go:1088` |
| GET | `/api/queen/{queen_id}/profile` | `handleQueenGetProfile` | `internal/server/routes_queens.go:1073` |
| PATCH | `/api/queen/{queen_id}/profile` | `handleQueenUpdateProfile` | `internal/server/routes_queens.go:1076` |
| POST | `/api/queen/{queen_id}/remove` | `handleQueenGetProfile` | `internal/server/routes_queens.go:1065` |
| POST | `/api/queen/{queen_id}/restore` | `handleQueenGetProfile` | `internal/server/routes_queens.go:1069` |
| POST | `/api/queen/{queen_id}/session` | `handleQueenSession` | `internal/server/routes_queens.go:1056` |
| POST | `/api/queen/{queen_id}/session/new` | `handleQueenNewSession` | `internal/server/routes_queens.go:1062` |
| POST | `/api/queen/{queen_id}/session/select` | `handleQueenSelectSession` | `internal/server/routes_queens.go:1059` |
| GET | `/api/queen/{queen_id}/skills` | `deps.handleListQueenSkills` | `internal/server/routes_skills.go:62` |
| POST | `/api/queen/{queen_id}/skills` | `deps.handleCreateQueenSkill` | `internal/server/routes_skills.go:63` |
| POST | `/api/queen/{queen_id}/skills/reload` | `deps.handleReloadQueenSkills` | `internal/server/routes_skills.go:69` |
| DELETE | `/api/queen/{queen_id}/skills/{skill_name}` | `deps.handleDeleteQueenSkill` | `internal/server/routes_skills.go:68` |
| PATCH | `/api/queen/{queen_id}/skills/{skill_name}` | `deps.handlePatchQueenSkill` | `internal/server/routes_skills.go:64` |
| GET | `/api/queen/{queen_id}/skills/{skill_name}/body` | `deps.handleGetQueenSkillBody` | `internal/server/routes_skills.go:65` |
| PUT | `/api/queen/{queen_id}/skills/{skill_name}/body` | `deps.handlePutQueenSkillBody` | `internal/server/routes_skills.go:66` |
| POST | `/api/queen/{queen_id}/skills/{skill_name}/rename` | `deps.handleRenameQueenSkill` | `internal/server/routes_skills.go:67` |
| GET | `/api/queen/{queen_id}/soul` | `routes.handleGetSoul` | `internal/server/routes_credentials_soul.go:42` |
| PUT | `/api/queen/{queen_id}/soul` | `routes.handlePutSoul` | `internal/server/routes_credentials_soul.go:43` |
| POST | `/api/queen/{queen_id}/soul/proposals/{proposal_id}/decide` | `routes.handleDecideSoulProposal` | `internal/server/routes_credentials_soul.go:44` |
| POST | `/api/queen/{queen_id}/soul/proposals/{proposal_id}/probe` | `routes.handleProbeSoulProposal` | `internal/server/routes_soul_probe.go:42` |
| POST | `/api/queen/{queen_id}/soul/reflect` | `routes.handleReflectSoul` | `internal/server/routes_credentials_soul.go:46` |
| POST | `/api/queen/{queen_id}/soul/rollback` | `routes.handleRollbackSoul` | `internal/server/routes_credentials_soul.go:45` |
| DELETE | `/api/queen/{queen_id}/tools` | `handleQueenDeleteTools` | `internal/server/routes_queens_tools.go:732` |
| GET | `/api/queen/{queen_id}/tools` | `handleQueenGetTools` | `internal/server/routes_queens_tools.go:726` |
| PATCH | `/api/queen/{queen_id}/tools` | `handleQueenPatchTools` | `internal/server/routes_queens_tools.go:729` |
| GET | `/api/runtime-logs` | `routes.handleRuntimeLogs` | `internal/server/routes_runtime_logs.go:170` |
| GET | `/api/sentinel/credentials` | `handleSentinelCredentialStatus` | `internal/server/routes_sentinel.go:113` |
| POST | `/api/sentinel/slack/validate` | `handleSlackValidate` | `internal/server/routes_sentinel.go:122` |
| POST | `/api/sentinel/telegram/detect-chat` | `handleTelegramDetectChat` | `internal/server/routes_sentinel.go:119` |
| POST | `/api/sentinel/telegram/validate` | `handleTelegramValidate` | `internal/server/routes_sentinel.go:116` |
| GET | `/api/sessions` | `handleCreateSession` | `internal/server/routes_sessions.go:45` |
| POST | `/api/sessions` | `handleCreateSession` | `internal/server/routes_sessions.go:52` |
| GET | `/api/sessions/history` | `handleSessionHistory` | `internal/server/routes_sessions.go:58` |
| GET | `/api/sessions/live` | `handleLiveSessionsStream` | `internal/server/routes_sessions.go:55` |
| DELETE | `/api/sessions/{first}/{second}` | `handleDeleteHistorySession` | `internal/server/routes_sessions.go:61` |
| DELETE | `/api/sessions/{session_id}` | `handleStopSession` | `internal/server/routes_sessions.go:78` |
| GET | `/api/sessions/{session_id}` | `handleGetLiveSession` | `internal/server/routes_sessions.go:75` |
| GET | `/api/sessions/{session_id}/attachment/{filename}` | `handleSessionAttachment` | `internal/server/routes_sessions.go:90` |
| POST | `/api/sessions/{session_id}/cancel-queen` | `handleCancelQueen` | `internal/server/routes_execution.go:55` |
| POST | `/api/sessions/{session_id}/chat` | `handleSessionChat` | `internal/server/chat_routes.go:33` |
| POST | `/api/sessions/{session_id}/colony` | `handleLoadColony` | `internal/server/routes_sessions.go:114` |
| POST | `/api/sessions/{session_id}/colony/resolve_missed` | `handleResolveMissedTriggers` | `internal/server/routes_sessions.go:120` |
| GET | `/api/sessions/{session_id}/colony/skills` | `handleListColonySkills` | `internal/server/routes_colony_workers.go:652` |
| GET | `/api/sessions/{session_id}/colony/tools` | `handleListColonyTools` | `internal/server/routes_colony_workers.go:655` |
| POST | `/api/sessions/{session_id}/compact-and-fork` | `handleCompactAndFork` | `internal/server/routes_execution.go:64` |
| POST | `/api/sessions/{session_id}/credential-form` | `routes.handleCredentialForm` | `internal/server/routes_credentials.go:83` |
| POST | `/api/sessions/{session_id}/dismiss-colony-pivot` | `handleDismissColonyPivot` | `internal/server/routes_sessions.go:117` |
| GET | `/api/sessions/{session_id}/events` | `handleSessionEventsStream` | `internal/server/chat_routes.go:36` |
| GET | `/api/sessions/{session_id}/events/history` | `handleSessionEventsHistory` | `internal/server/routes_sessions.go:123` |
| POST | `/api/sessions/{session_id}/fork` | `handleForkSession` | `internal/server/routes_sessions.go:129` |
| GET | `/api/sessions/{session_id}/live_tools` | `handleSessionLiveTools` | `internal/server/routes_sessions.go:81` |
| POST | `/api/sessions/{session_id}/mark-colony-spawned` | `handleMarkColonySpawned` | `internal/server/routes_execution.go:61` |
| POST | `/api/sessions/{session_id}/presence` | `handleSessionPresence` | `internal/server/routes_execution.go:58` |
| POST | `/api/sessions/{session_id}/queen-context` | `handleQueenContext` | `internal/server/routes_execution.go:49` |
| POST | `/api/sessions/{session_id}/record-message` | `handleRecordUserMessage` | `internal/server/routes_execution.go:52` |
| DELETE | `/api/sessions/{session_id}/reminders` | `handleDismissSessionReminders` | `internal/server/routes_reminders.go:359` |
| GET | `/api/sessions/{session_id}/reminders` | `handleGetSessionReminders` | `internal/server/routes_reminders.go:356` |
| POST | `/api/sessions/{session_id}/report-bundle` | `handleReportSessionBundle` | `internal/server/routes_sessions.go:135` |
| POST | `/api/sessions/{session_id}/reveal` | `handleRevealSession` | `internal/server/routes_sessions.go:132` |
| GET | `/api/sessions/{session_id}/skill-installs` | `deps.handleListSkillInstalls` | `internal/server/routes_skills.go:71` |
| POST | `/api/sessions/{session_id}/skill-installs/{request_id}` | `deps.handleDecideSkillInstall` | `internal/server/routes_skills.go:72` |
| GET | `/api/sessions/{session_id}/snapshot` | `handleGetSessionSnapshot` | `internal/server/routes_sessions.go:87` |
| GET | `/api/sessions/{session_id}/stats` | `handleSessionStats` | `internal/server/routes_sessions.go:84` |
| GET | `/api/sessions/{session_id}/tasks` | `handleGetTaskList` | `internal/server/routes_tasks.go:39` |
| POST | `/api/sessions/{session_id}/tasks/clear-completed` | `handleClearCompletedTasks` | `internal/server/routes_tasks.go:45` |
| POST | `/api/sessions/{session_id}/tasks/unarchive` | `handleUnarchiveTaskList` | `internal/server/routes_tasks.go:42` |
| PATCH | `/api/sessions/{session_id}/title` | `handleSetSessionTitle` | `internal/server/routes_sessions.go:126` |
| GET | `/api/sessions/{session_id}/triggers` | `handleListTriggers` | `internal/server/routes_sessions.go:96` |
| POST | `/api/sessions/{session_id}/triggers` | `handleCreateTrigger` | `internal/server/routes_sessions.go:99` |
| PATCH | `/api/sessions/{session_id}/triggers/{trigger_id}` | `handleUpdateTrigger` | `internal/server/routes_sessions.go:102` |
| POST | `/api/sessions/{session_id}/triggers/{trigger_id}/activate` | `handleActivateTrigger` | `internal/server/routes_sessions.go:108` |
| POST | `/api/sessions/{session_id}/triggers/{trigger_id}/deactivate` | `handleDeactivateTrigger` | `internal/server/routes_sessions.go:111` |
| POST | `/api/sessions/{session_id}/triggers/{trigger_id}/run` | `handleRunTrigger` | `internal/server/routes_sessions.go:105` |
| POST | `/api/sessions/{session_id}/upload-attachment` | `handleUploadAttachment` | `internal/server/routes_sessions.go:93` |
| GET | `/api/sessions/{session_id}/workers` | `handleListColonyWorkers` | `internal/server/routes_colony_workers.go:643` |
| POST | `/api/sessions/{session_id}/workers/stop-all` | `handleStopAllLiveWorkers` | `internal/server/routes_workers.go:94` |
| GET | `/api/sessions/{session_id}/workers/stream` | `handleLiveWorkersStream` | `internal/server/routes_workers.go:91` |
| GET | `/api/sessions/{session_id}/workers/{worker_id}` | `handleGetColonyWorker` | `internal/server/routes_colony_workers.go:646` |
| GET | `/api/sessions/{session_id}/workers/{worker_id}/conversation` | `handleGetColonyWorkerConversation` | `internal/server/routes_colony_workers.go:649` |
| GET | `/api/sessions/{session_id}/workers/{worker_id}/reap-timeline` | `handleWorkerReapTimeline` | `internal/server/routes_workers.go:100` |
| POST | `/api/sessions/{session_id}/workers/{worker_id}/stop` | `handleStopLiveWorker` | `internal/server/routes_workers.go:97` |
| GET | `/api/skills` | `deps.handleListAllSkills` | `internal/server/routes_skills.go:74` |
| POST | `/api/skills/reload` | `deps.handleReloadLibrarySkills` | `internal/server/routes_skills.go:76` |
| GET | `/api/skills/scopes` | `deps.handleListScopes` | `internal/server/routes_skills.go:75` |
| POST | `/api/skills/upload` | `deps.handleUploadSkill` | `internal/server/routes_skills.go:77` |
| GET | `/api/skills/{skill_name}` | `deps.handleGetSkillDetail` | `internal/server/routes_skills.go:78` |
| GET | `/api/startup-status` | `app.handleStartupStatus` | `internal/server/app.go:176` |

> 覆盖说明：未被路由表覆盖的 HandleFunc 注册（无方法前缀或跨行写法）：mux.HandleFunc("/", s.handleRequest)

## 事件类型表

从 `internal/eventbus/event_type.go` 提取 `EventType = "..."` 常量。事件是字符串类型，新增类型必须同时补常量与 `AllEventTypes`；改已有字符串值等于破坏订阅方。

共 **77** 个 EventType。

| 常量 | 值 | 来源 |
| --- | --- | --- |
| `EventExecutionStarted` | `execution_started` | `internal/eventbus/event_type.go:6` |
| `EventExecutionCompleted` | `execution_completed` | `internal/eventbus/event_type.go:7` |
| `EventExecutionFailed` | `execution_failed` | `internal/eventbus/event_type.go:8` |
| `EventExecutionPaused` | `execution_paused` | `internal/eventbus/event_type.go:9` |
| `EventExecutionResumed` | `execution_resumed` | `internal/eventbus/event_type.go:10` |
| `EventExecutionResurrected` | `execution_resurrected` | `internal/eventbus/event_type.go:11` |
| `EventPaymentRequired` | `payment_required` | `internal/eventbus/event_type.go:14` |
| `EventStateChanged` | `state_changed` | `internal/eventbus/event_type.go:17` |
| `EventStateConflict` | `state_conflict` | `internal/eventbus/event_type.go:18` |
| `EventGoalProgress` | `goal_progress` | `internal/eventbus/event_type.go:22` |
| `EventGoalAchieved` | `goal_achieved` | `internal/eventbus/event_type.go:23` |
| `EventConstraintViolation` | `constraint_violation` | `internal/eventbus/event_type.go:24` |
| `EventStreamStarted` | `stream_started` | `internal/eventbus/event_type.go:28` |
| `EventStreamStopped` | `stream_stopped` | `internal/eventbus/event_type.go:29` |
| `EventNodeLoopStarted` | `node_loop_started` | `internal/eventbus/event_type.go:33` |
| `EventNodeLoopIteration` | `node_loop_iteration` | `internal/eventbus/event_type.go:34` |
| `EventNodeLoopCompleted` | `node_loop_completed` | `internal/eventbus/event_type.go:35` |
| `EventLLMTextDelta` | `llm_text_delta` | `internal/eventbus/event_type.go:39` |
| `EventLLMReasoningDelta` | `llm_reasoning_delta` | `internal/eventbus/event_type.go:40` |
| `EventLLMTurnComplete` | `llm_turn_complete` | `internal/eventbus/event_type.go:41` |
| `EventToolCallStarted` | `tool_call_started` | `internal/eventbus/event_type.go:45` |
| `EventToolCallCompleted` | `tool_call_completed` | `internal/eventbus/event_type.go:46` |
| `EventClientOutputDelta` | `client_output_delta` | `internal/eventbus/event_type.go:50` |
| `EventClientReasoning` | `client_reasoning` | `internal/eventbus/event_type.go:51` |
| `EventClientInputRequested` | `client_input_requested` | `internal/eventbus/event_type.go:52` |
| `EventClientInputReceived` | `client_input_received` | `internal/eventbus/event_type.go:53` |
| `EventClientInputCommitted` | `client_input_committed` | `internal/eventbus/event_type.go:54` |
| `EventClientCredentialFormRequested` | `client_credential_form_requested` | `internal/eventbus/event_type.go:55` |
| `EventColonySuggestionRequested` | `colony_suggestion_requested` | `internal/eventbus/event_type.go:56` |
| `EventSkillInstallRequested` | `skill_install_requested` | `internal/eventbus/event_type.go:57` |
| `EventSkillInstallDecided` | `skill_install_decided` | `internal/eventbus/event_type.go:58` |
| `EventNodeInternalOutput` | `node_internal_output` | `internal/eventbus/event_type.go:62` |
| `EventNodeInputBlocked` | `node_input_blocked` | `internal/eventbus/event_type.go:63` |
| `EventNodeStalled` | `node_stalled` | `internal/eventbus/event_type.go:64` |
| `EventNodeToolDoomLoop` | `node_tool_doom_loop` | `internal/eventbus/event_type.go:65` |
| `EventJudgeVerdict` | `judge_verdict` | `internal/eventbus/event_type.go:68` |
| `EventNodeRetry` | `node_retry` | `internal/eventbus/event_type.go:70` |
| `EventStreamTTFTExceeded` | `stream_ttft_exceeded` | `internal/eventbus/event_type.go:73` |
| `EventStreamInactive` | `stream_inactive` | `internal/eventbus/event_type.go:74` |
| `EventStreamNudgeSent` | `stream_nudge_sent` | `internal/eventbus/event_type.go:75` |
| `EventLoopStateChanged` | `loop_state_changed` | `internal/eventbus/event_type.go:78` |
| `EventReminderInjected` | `reminder_injected` | `internal/eventbus/event_type.go:80` |
| `EventToolCallReplayDetected` | `tool_call_replay_detected` | `internal/eventbus/event_type.go:82` |
| `EventWorkerCompleted` | `worker_completed` | `internal/eventbus/event_type.go:85` |
| `EventWorkerFailed` | `worker_failed` | `internal/eventbus/event_type.go:86` |
| `EventContextCompactionStarted` | `context_compaction_started` | `internal/eventbus/event_type.go:90` |
| `EventContextCompacted` | `context_compacted` | `internal/eventbus/event_type.go:91` |
| `EventContextUsageUpdated` | `context_usage_updated` | `internal/eventbus/event_type.go:92` |
| `EventWebhookReceived` | `webhook_received` | `internal/eventbus/event_type.go:95` |
| `EventCustom` | `custom` | `internal/eventbus/event_type.go:97` |
| `EventEscalationRequested` | `escalation_requested` | `internal/eventbus/event_type.go:99` |
| `EventWorkerColonyLoaded` | `worker_colony_loaded` | `internal/eventbus/event_type.go:102` |
| `EventColonyCreated` | `colony_created` | `internal/eventbus/event_type.go:103` |
| `EventCredentialsRequired` | `credentials_required` | `internal/eventbus/event_type.go:104` |
| `EventSessionForked` | `session_forked` | `internal/eventbus/event_type.go:107` |
| `EventSessionTitleUpdated` | `session_title_updated` | `internal/eventbus/event_type.go:109` |
| `EventQueenPhaseChanged` | `queen_phase_changed` | `internal/eventbus/event_type.go:111` |
| `EventQueenIdentitySelected` | `queen_identity_selected` | `internal/eventbus/event_type.go:113` |
| `EventSubagentReport` | `subagent_report` | `internal/eventbus/event_type.go:115` |
| `EventTriggerAvailable` | `trigger_available` | `internal/eventbus/event_type.go:118` |
| `EventTriggerActivated` | `trigger_activated` | `internal/eventbus/event_type.go:119` |
| `EventTriggerDeactivated` | `trigger_deactivated` | `internal/eventbus/event_type.go:120` |
| `EventTriggerFired` | `trigger_fired` | `internal/eventbus/event_type.go:121` |
| `EventTriggerRemoved` | `trigger_removed` | `internal/eventbus/event_type.go:122` |
| `EventTriggerUpdated` | `trigger_updated` | `internal/eventbus/event_type.go:123` |
| `EventMissedTriggers` | `missed_triggers` | `internal/eventbus/event_type.go:124` |
| `EventTaskCreated` | `task_created` | `internal/eventbus/event_type.go:128` |
| `EventTaskUpdated` | `task_updated` | `internal/eventbus/event_type.go:129` |
| `EventTaskDeleted` | `task_deleted` | `internal/eventbus/event_type.go:130` |
| `EventTaskListReset` | `task_list_reset` | `internal/eventbus/event_type.go:131` |
| `EventTaskListReattachMismatch` | `task_list_reattach_mismatch` | `internal/eventbus/event_type.go:132` |
| `EventSessionSnapshot` | `session_snapshot` | `internal/eventbus/event_type.go:135` |
| `EventCredentialProviderConnected` | `credential_provider_connected` | `internal/eventbus/event_type.go:138` |
| `EventCredentialProviderDisconnected` | `credential_provider_disconnected` | `internal/eventbus/event_type.go:139` |
| `EventToolCatalogRefreshed` | `tool_catalog_refreshed` | `internal/eventbus/event_type.go:140` |
| `EventToolsConfigChanged` | `tools_config_changed` | `internal/eventbus/event_type.go:141` |
| `EventCRMChanged` | `crm_changed` | `internal/eventbus/event_type.go:142` |

## 磁盘格式表

人工维护在 `architecture/model.yaml` 的 `disk_formats:`；契约层 `disk.*` 是它的逐条展开。JSON 文件没有本地 schema 校验，读取方普遍把「缺失/坏 JSON」降级为空对象或空数组，因此这里的版本/迁移策略是读代码得出的实际行为，不是生成器验证过的保证。

| 路径 | 生产者 | 消费者 | 关键字段 | 版本 / 迁移策略 |
| --- | --- | --- | --- | --- |
| `~/.hive/colonies/{colony}/worker.json` | internal/server（scaffold） | internal/agents、internal/colony、internal/cli | name、description、tools[]、goal{description, success_criteria[], constraints[]}、queen_name、loop_config | 无 schema 版本；解码按 map/结构体，未知字段忽略，新增字段属于加法兼容。 |
| `~/.hive/colonies/{colony}/metadata.json` | internal/server | internal/server、internal/host、internal/cli | name、created_at、queen_name、scaffolded、deleted、source_session_id | 无 schema 版本；自由 map，缺失或坏 JSON 读成空 map。 |
| `~/.hive/colonies/{colony}/workers/{worker_id}/meta.json` | internal/colony | internal/colony、internal/server | worker_id、colony_id、task、profile_name、goal、batch_id、batch_index、batch_size | 无 schema 版本；缺失时 resume 退化为按 conversations/parts 恢复。 |
| `~/.hive/queens/{queen}/sessions/{session}/meta.json` | internal/server、internal/session | internal/server（adapters_qmail.go）、internal/session | title、title_source、phase、colony_spawned、spawned_colony_id、spawned_colony_at | 无 schema 版本；按 key 合并，未知字段保留，坏文件回退默认。 |
| `…/sessions/{session}/conversations/cursor.json` | internal/agentloop、internal/session、internal/storage | internal/agentloop、internal/session、internal/storage、internal/server | iteration、turn、accumulated_output、tool_calls、stall_repeat、last_fingerprint、pending_inputs[]、park_reason、updated_at | 无 schema 版本；派生状态，损坏按不存在处理并从 parts 重建。 |
| `{session\|colony}/triggers.json` | internal/triggers、internal/server | internal/triggers、internal/server | id、trigger_type、trigger_config、description、task、enabled、last_fired_at、next_due_at | 顶层数组、无版本号；非数组/坏 JSON 读成空数组。 |
| `~/.hive/queens/{queen}/soul/soul.json（+ history/v{n}.json、proposals/*.json）` | internal/soul | internal/soul、internal/server、internal/agentloop | version、updated_at、sections{key:[Item]}；history 按 version 命名，proposal 带 base_version/edits/status | 文件内 version:int 即版本号；回滚按 version 读 history，迁移需同时处理 history 目录。 |
| `~/.hive/colonies/{colony}/tracker/tracker.db` | internal/tracker | internal/colony、internal/tracker、internal/server | _tracker_registry(table_name, write_columns, key_columns, mode)、_tracker_meta(schema_version)、_tracker_changes；业务表由 queen 动态创建 | PRAGMA user_version=3（SchemaVersion）；低于当前版本执行 bootstrap + backfill，只做加法迁移。 |

## 覆盖边界

- HTTP 路由表只统计带方法前缀的 `mux.HandleFunc`，共 220 条（含 `GET /` SPA 兜底）；`internal/server/webhook_server.go` 里独立的 `mux.HandleFunc("/", ...)` webhook 入口不在表内。
- SSE 端点没有单独一张表；它们的帧形状写在 `sse.*` 契约明细里，路由本身仍在 HTTP 路由表中。
- `contracts:` 是人工维护的治理视图，不是从代码自动推导的依赖图；生成器只校验字段完整性、包名与类别，不能证明每条保证在运行期成立。
