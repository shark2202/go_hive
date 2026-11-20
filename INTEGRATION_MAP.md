# 装配地图（integrator 唯一作业依据）

> 由 Coordinator 依据合并后代码实测生成，行号为 `internal/server/*.go` 中的定义位置。
> 装配 lane 只改 `internal/server/app.go` 与新增适配器文件；不得改各路由文件的行为。

## 1. 现状

`app.go` 当前只接线 4 个：

```
RegisterConfigRoutes(mux, deps.Config)     // app.go:84
RegisterSessionRoutes(mux, SessionRouteDeps{...})
RegisterAgentRoutes(mux, AgentRouteDeps{...})
RegisterChatRoutes(mux, ChatRouteDeps{Manager: deps.Manager})
```

**未接线 21 个**（下节），因此 wave 1 新增的 154 条路径在生产进程中全部不可达。

## 2. 待接线清单（26 个导出注册函数）

| 注册函数 | 文件 | 关键依赖 / 适配要求 |
| --- | --- | --- |
| `RegisterColonyWorkerRoutes` | `routes_colony_workers.go:642` | `ColonyWorkerDeps.Runtime func(*Session) ColonyWorkerReadSurface`；需把 `*colony.ColonyRuntime` 适配为 `ListWorkers/Worker/ColonyID/StoragePath/Skills/Tools` |
| `RegisterQueenRoutes` | `routes_queens.go:1005` | `CreateSession/StopSession/ValidateProviderKey/ApplyQueenModel/RefreshQueenIdentity/MCPCatalog/LifecycleTools/RefreshSessionTools` |
| `RegisterQueenToolRoutes` | `routes_queens_tools.go:702` | 工具 allowlist 读写；需 `tools.ToolRegistry` 视图 |
| `RegisterQueenMailRoutes` | `routes_queens_mail.go:96` | `QueenMailBackend` 接口；后端未移植，先接内存/磁盘实现或返回 503 |
| `RegisterClaudeCodeQueenRoutes` | `routes_queens_claude_code.go:124` | `Conversations`/`Transcript` 后端未移植，同上 |
| `RegisterCredentialRoutes` | `routes_credentials.go:58` | 复用 `internal/credentials`；`Specs`/`Resync` 是 seam，未接时结构化 500/503 |
| `RegisterSoulRoutes` | `routes_credentials_soul.go:35` | `Reflect` seam 未接时结构化失败 |
| `RegisterChannelRoutes` | `routes_credentials_channels.go:17` | 只读 `~/.hive/queens` 频道清单 |
| `RegisterSentinelRoutes` | `routes_sentinel.go:91` | `SentinelStore`/`SentinelNotifier`；需 3 个适配：`UpdateNotificationsConfig` 的 `ErrColonyNotFound`→`os.ErrNotExist`、`HTTPNotifier.Send` 签名、`LoadNotificationsConfig` 无 error |
| `RegisterMCPRoutes` | `routes_mcp.go:61` | `MCPRouteDeps{Home}`；接 `internal/mcpregistry` |
| `RegisterMaintenanceRoutes` | `routes_maintenance.go:60` | `{Home, Manager, ProcessStartTS}` |
| `RegisterColonyRoutes` | `routes_colonies.go:40` | import/scaffold/rename/reveal/worker-profiles；文件系统根 `Home` |
| `RegisterColonyToolRoutes` | `routes_colony_tools.go:44` | `MCPCatalog/Connected/Providers/Lifecycle` 为 seam，未接时 GET 返回 `stale:true` 空目录 |
| `RegisterExecutionRoutes` | `routes_execution.go:32` | queen-context/record-message/cancel-queen/presence/mark-colony-spawned |
| `RegisterWorkerRoutes` | `routes_workers.go:90` | `LiveWorkerRuntime` 接口；需在 `*colony.ColonyRuntime` 上导出 `LiveWorkers/StopLiveWorker/StopAllLiveWorkers` |
| `RegisterModelRoutes` | `routes_models.go:126` | `SessionModelReconfigurer`（含 extraBody）；无实现时返回 501/503，不得假成功 |
| `RegisterEventRoutes` | `routes_events.go:69` | `/api/events/global` SSE + `/api/crm/changed` |
| `RegisterAppExtraRoutes` | `routes_app_extra.go:104` | `/api/health/resources` 需注入采样器（无采样器时 `available:false`） |
| `RegisterPromptRoutes` | `routes_prompts_extra.go:49` | `{Home}` |
| `RegisterMemoryRoutes` | `routes_memories_extra.go:21` | `{Home}` |
| `RegisterMessageRoutes` | `routes_messages_extra.go:63` | `{Home, BuildLLM}` |
| `tasks.RegisterTaskRoutes` | `internal/tasks/routes.go:149` | 已自带 mux 组装，直接调用 |
| `integrations.New(...).Install(registry)` | `internal/tools/integrations/integration.go:157` | 返回安装数量；接入会话工具注册表（`internal/cli/loader.go` 的 `builtin.New`） |
| `host.New(host.Config{...})` | `internal/host/host.go` | Colony spawn/run/SSE 入口，供 execution 路由与 SSE 推送使用 |

## 3. 接线顺序与冲突约束

1. **先注册具体路径，再注册 SPA catch-all**；`HandleFunc` 模式冲突会 panic，装配后必须跑一次真实 `app.Handler()` 构造测试。
2. `chat_routes.go` 已占用 `GET /api/sessions/{id}/events`、`POST .../chat`，新增 execution 路由不得重复。
3. `routes_colony_workers` 与 `routes_workers` 都含 `/api/sessions/{id}/workers`，以 `routes_colony_workers.go` 为准，`routes_workers.go` 只注册 stream/stop-all/stop/reap-timeline。
4. 所有 seam 未接线时必须返回明确的 501/503，**不得返回空 200 或假数据**。

## 4. 验收（G3）

```
cd go_hive/go-hive && make ci
CGO_ENABLED=0 go test -tags e2e -count=1 ./e2e/...
go build -o dist/hive ./cmd/hive && HIVE_HOME=/tmp/hive_smoke ./dist/hive serve --port 8787
# 冒烟：/api/health 200；POST /api/sessions 201；无 key POST /chat 503；GET / 200 或明确 404；新增路由抽样 200/4xx 非 404
```
