# 11 · Server / API / 前端

> 关键文件：
> `core/framework/server/app.py`（应用工厂、中间件、路由注册）
> `core/framework/server/session_manager.py`（2,298 行，会话生命周期）
> `core/framework/server/queen_orchestrator.py`（2,037 行，Queen 装配）
> `core/framework/server/routes_*.py`（23 个路由模块）
> `core/frontend/src/`（React Dashboard）

## 1. 服务端

### 1.1 应用工厂

`create_app()` 构造 aiohttp `web.Application`，挂载：

- 中间件：访问日志 → 桌面 token 认证 → API no-cache → 错误处理；
- `SessionManager`；
- 凭据存储；
- 各路由模块；
- 静态前端（若构建产物存在）；
- 启动钩子：父进程看门狗、streamToken 刷新、资源采样、Soul 每日调度、Queen Mail。

默认监听 `127.0.0.1:8787`（`hive serve/open`）。

### 1.2 中间件与安全

| 中间件 | 作用 |
|---|---|
| `access_log_middleware` | 请求日志 |
| `desktop_auth_middleware` | 若设置了 `HIVE_DESKTOP_TOKEN`，校验共享密钥（常量时间比较）；`/api/queen-mail/` 例外（Claude Code Queen 用自己的会话 token） |
| `no_cache_api_middleware` | API 响应禁缓存 |
| `error_middleware` | 统一错误响应与日志 |

CORS 仅允许 localhost / 127.0.0.1 的任意端口。

**默认 OSS 模式没有认证**——它依赖“只绑定 127.0.0.1”。如果改为 `0.0.0.0`，必须在外层加认证/反代。

### 1.3 请求线程池

HTTP 处理器有独立线程池（`HIVE_REQUEST_EXECUTOR_MAX`，默认 32），与工具执行池、默认线程池隔离。原因写在 `app.py` 顶部：

> 一个失控的 Queen 工具调用不能把 Colony UI 的 `/api/colonies/{id}/data/*` 读请求拖死。

这是一个真实的线上故障（15s `colony_data_timeout` 503）驱动的设计。

### 1.4 SessionManager

`Session` 是运行时聚合根：

```python
@dataclass
class Session:
    id: str
    event_bus: EventBus
    llm: LLMProvider
    queen_executor / queen_task
    binding: ColonyBinding | None
    colony: ColonyRuntime | None
    phase_state: QueenPhaseState
    queen_name: str
    channel: str | None
    colony_id: str | None
    mode: "dm" | "colony"
    sse_client_count: int
    available_triggers / active_trigger_ids
    ...
```

职责：

- 创建 DM 会话 / Colony 会话 / fork；
- 冷恢复（从磁盘重建）；
- 启动/停止 Queen；
- 维护 live 列表、快照、事件；
- 加载/卸载 Colony；
- 触发器的可用/激活/错过；
- 记忆反思订阅、标题生成、Sentinel 观察。

### 1.5 QueenOrchestrator

负责把一个 session 装配成可运行的 Queen：

- 读取 Queen profile / persona；
- 构建系统提示（静态 + 动态后缀）；
- 构建工具面（阶段工具 + MCP 工具 + 动态工具层）；
- 选择模型与 LoopConfig（Independent 30 / Colony 50）；
- 设置 `ToolRegistry` 执行上下文（`profile`、`agent_id`、`session_id`、`binding`、`colony_id`、`principal`）；
- 启动 `AgentLoop`；
- 订阅 Worker 报告并注入 `[WORKER_REPORT]`；
- 接入 Sentinel、stall check、记忆、标题、Soul。

它是“一个会话如何变成一个 Queen”的唯一入口。

## 2. HTTP API 家族

约 **196** 个路由注册点，按资源分组：

### 2.1 会话与聊天

```text
POST   /api/sessions                        创建 DM / Colony / fork
GET    /api/sessions/{id}                   会话状态
POST   /api/sessions/{id}/chat              发送消息（文本 + 图片）
POST   /api/sessions/{id}/cancel-queen      停止当前回合
GET    /api/sessions/{id}/events            SSE 事件流
GET    /api/sessions/{id}/events/history    历史事件
GET    /api/sessions/{id}/snapshot          会话快照
GET    /api/sessions/{id}/stats              token/工具/成本统计
POST   /api/sessions/{id}/fork              分支（可指定消息点）
POST   /api/sessions/{id}/record-message    记录消息
GET    /api/sessions/live                    live 会话列表 + 上游健康
GET    /api/sessions/history                冷会话列表
```

### 2.2 任务与计划

```text
GET    /api/sessions/{id}/tasks
POST   /api/sessions/{id}/tasks/unarchive
```

### 2.3 Worker / Colony

```text
GET    /api/sessions/{id}/workers
GET    /api/sessions/{id}/workers/{wid}
GET    /api/sessions/{id}/workers/stream
GET    /api/colonies/{cid}/sessions
GET    /api/colonies/{cid}/data/tables
GET    /api/colonies/{cid}/data/changes
POST   /api/colonies/import/init
POST   /api/colonies/import/chunk/{upload_id}
POST   /api/colonies/import/finalize/{upload_id}
GET    /api/colonies/import/status/{upload_id}
```

### 2.4 配置 / 模型 / 凭据

```text
GET/PUT /api/config/llm
GET/PUT /api/config/features
GET/PUT /api/config/memory
GET/PUT /api/config/sentinel
GET/PUT /api/config/profile
GET/PUT /api/config/rate-limits
GET     /api/config/models
GET     /api/config/provider-library
POST    /api/config/provider-library/apply
GET/POST/DELETE /api/credentials...
GET     /api/models/overview
GET     /api/models/endpoints/{name}
GET     /api/models/slots/{slot}
```

### 2.5 Queen / 组织

```text
GET    /api/queen/profiles
POST   /api/queen/create
POST   /api/queen/initialize-preferences
GET/PUT /api/queen/{id}/profile
GET/PUT /api/queen/{id}/persona
GET/PUT /api/queen/{id}/llm
GET/PUT /api/queen/{id}/tools
GET/PUT /api/queen/{id}/skills...
GET    /api/queen/{id}/overview
GET    /api/queen/{id}/soul
POST   /api/queen/{id}/soul/reflect
POST   /api/queen/{id}/soul/proposals/{pid}/decide
POST   /api/queen/{id}/soul/rollback
GET    /api/queen/{id}/session
POST   /api/queen/{id}/session/new
POST   /api/queen/{id}/session/select
```

### 2.6 技能 / 记忆 / Prompt

```text
GET/POST /api/skills
GET/PUT/DELETE /api/skills/{name}
GET    /api/skills/scopes
POST   /api/skills/upload
GET    /api/memories
GET    /api/memories/file
GET    /api/memories/history
POST   /api/memories/restore
GET    /api/memories/retired
GET/POST /api/prompts
GET/PUT/DELETE /api/prompts/{id}
```

### 2.7 MCP / 工具

```text
GET    /api/mcp/servers
POST   /api/mcp/servers
GET/PUT/DELETE /api/mcp/servers/{name}
POST   /api/mcp/servers/{name}/enable|disable|mode
GET    /api/mcp/servers/{name}/health
GET    /api/agents
GET    /api/agents/metadata
GET    /api/sessions/{id}/live_tools
```

### 2.8 运维 / 诊断

```text
GET    /api/health
GET    /api/health/resources
GET    /api/startup-status
GET    /api/runtime-logs
GET    /api/events/global
POST   /api/maintenance/janitor/run
GET    /api/maintenance/janitor/report
GET    /api/browser/status
GET    /api/browser/status/stream
POST   /api/browser/tab/reveal
```

### 2.9 自治 / 渠道 / 邮件

```text
GET    /api/channels
GET    /api/sessions/{id}/triggers
POST   /api/sessions/{id}/triggers
DELETE /api/sessions/{id}/triggers/{tid}
GET    /api/sentinel/credentials
POST   /api/sentinel/telegram/validate
POST   /api/sentinel/slack/validate
GET    /api/queen-mail/queens
GET    /api/queen-mail/letters
GET    /api/claude-code-queens
GET    /api/claude-code-queens/{qid}/conversations/{sid}
```

### 2.10 可选 CRM / 云

```text
/api/crm/*、/api/global/data/*、/api/global/crm/status
```

这些只在 `framework.crm` 存在时可用；本 OSS 工作区不包含该包。

## 3. SSE：前端如何看到“正在发生”

### 3.1 传输

`server/sse.py`：

```http
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
X-Accel-Buffering: no
```

事件格式：

```text
id: <seq>
event: <type>
data: {"type": "...", "stream_id": "...", "data": {...}, ...}

: keepalive
```

### 3.2 首帧快照

每次新订阅，服务端先发 `SESSION_SNAPSHOT`：

- 从 EventBus 环形历史重建；
- 告诉前端“Queen 是否在忙、当前回合、上下文用量、Worker 状态”；
- 避免“刷新页面后要等下一个事件才知道状态”。

### 3.3 去重

`AgentEvent.seq` 是单调发布计数；前端可对“历史回放 + 实时流”两条路径的重复事件去重。

### 3.4 事件讨论

`CLIENT_OUTPUT_DELTA` 是增量文本；`snapshot` 字段是该回合累计文本，便于断线重连后直接渲染。`CLIENT_INPUT_COMMITTED` 标记用户消息真正进入对话的时点（而不是仅仅被接收），前端用它把气泡排到正确的 seq。

## 4. 前端

### 4.1 技术栈

- React 18 + TypeScript + Vite 6；
- Tailwind 4 + `tailwind-merge`；
- ECharts 5 + Mermaid 11（图表/流程图）；
- Shiki 4（代码高亮）；
- react-router-dom 7；
- react-markdown + remark-gfm。

### 4.2 路由与页面

`App.tsx`：

| 路径 | 页面 | 功能 |
|---|---|---|
| `/` | Home | Hive Map：Queen 与 Colony 总览；任务输入与 Queen 选择 |
| `/queen/:id` | QueenDM | Queen 聊天 |
| `/queen/:id/profile` | QueenProfile | 人格、模型、工具、Soul、技能 |
| `/colony/:id` | ColonyChat | Colony 聊天 + Data / Plan / Automations / Workers |
| `/org-chart` | OrgChart | 组织图、雇用/移除/恢复 Queen |
| `/claude-code/:id` | ClaudeCodeQueen | Claude Code Queen 的只读对话 |
| `/models` | Models | 模型槽位、端点、Provider key、订阅 |
| `/skills-library` | SkillsLibrary | 技能库 + MCP Tools |
| `/prompt-library` | PromptLibrary | 提示词库 |
| `/memory-library` | MemoryLibrary | 记忆查看/编辑/历史/恢复 |
| `/credentials` | Credentials | 凭据管理 |

### 4.3 关键组件

- `ChatPanel`：流式消息、工具调用气泡、附件、问题卡片、Worker 并行气泡；
- `ColonyPanel`：Data 表格、Plan、Automations、Workers；
- `HiveMap` / `PositionCatalog` / `QueenAvatar`：可视化组织；
- `MultiQuestionWidget` / `QuestionWidget`：`ask_user` 的多问题/选项；
- `ModelPicker` / `ProviderManager` / `ModelErrorCard`：模型配置与错误；
- `QueenSoulSection` / `QueenToolsSection` / `MemorySettingsSection`；
- `RuntimeLogsDrawer`：服务端日志；
- `BrowserStatusBadge`：浏览器桥状态。

### 4.4 构建与打包

- `core/frontend`：`npm run build` → `dist/`；
- `core/hatch_build.py` 把 `frontend/dist` 打进 wheel 的 `framework/server/frontend`；
- CI 的 `package` job 在干净 venv 中安装 wheel 并运行 `scripts/check_installed_package.py`；
- 开发模式：Vite dev server + Hive API；
- 桌面模式：Electron 拥有前端，Python 只提供 API（`HIVE_DESKTOP_MODE`）。

## 5. 会话分支与建群

### 5.1 分支

`server/session_fork.py`：

- 复制源会话目录到新 session；
- 可按“某条用户消息之前”截断；
- 复制 `parts` 到切点，丢弃之后的；
- 新会话的任务计划为空；
- `meta.forked_from` 记录来源；
- 压缩点之前的消息不能作为分支点（返回 `ForkError`）。

### 5.2 建群

见 04 章：`fork_session_into_colony` 复制会话 + 后台压缩 + seed conversation + tracker 初始化。

### 5.3 会话标题

`session_titles.py`：

- Queen 回合结束后，若会话无标题，用 worker 模型生成 ≤6 词标题；
- 用户手动命名的标题永不被覆盖；
- `title_source` 区分 `auto` / `user`；
- 发 `SESSION_TITLE_UPDATED`。

## 6. Colony 导入/导出

### 6.1 导入

`routes_colonies.py`：

- 支持 `.tar` / `.tar.gz` / `.tar.bz2` / `.tar.xz`；
- 支持单 Colony 目录，也支持 desktop 的“多根”归档（`colonies/`、`agents/`、`queens/`、`memories/`）；
- 分块上传：`init` → `chunk` → `finalize` → `status`；
- 安全解包：拒绝绝对路径、`..`、符号链接逃逸；
- 到 `HIVE_HOME` 后重新绑定 Tracker 路径。

> 代码注释明确：“Today, just the import side”。本报告不声称有对应的 HTTP 导出端点；导出通常由部署脚本/桌面侧完成。

### 6.2 会话报告包

`_session_report.py` + `POST /api/sessions/{id}/report-bundle`：

- 把整个会话目录打包成 `tar.gz`；
- **只脱敏凭据**（JWT、API key、Bearer、OAuth 回调 URL、常见 token 前缀）；
- 保留消息正文、姓名、邮箱、路径、截图，以便调试；
- 写 `user_report.json` 标记，供训练/反馈流水线识别；
- 单文件/总二进制有上限（256 MiB / 1 GiB）。

这是一个“用户主动上报问题”的能力，不是自动外传。

## 7. 可选包与降级

| 包 | 缺失时 |
|---|---|
| `framework.crm` | CRM 路由不可用；`crm_summary` 不注册 |
| `framework.cloud_sync` | `schedule_push` 静默 no-op；Inbox 需要 JWT/base 才可用 |
| `aden_tools` 可选 extras | `excel_*` 在无 openpyxl 时隐藏；`web_scrape` 在无 playwright 时隐藏；email senders 需显式开关 |

Hive 的 OSS 形态是“核心可跑，团队/云能力按包叠加”。

## 8. 评价

**优点**

- API 家族覆盖完整：会话、任务、Colony 数据、Worker、技能、记忆、模型、凭据、Sentinel、MCP、运维。
- SSE + 首帧快照 + seq 去重，前端体验接近“本地应用”。
- 独立请求线程池是真实的隔离设计，不是纸面优化。
- 前端页面与后端能力一一对应，没有“只有 CLI 才能用”的核心功能。

**风险**

- 默认无认证、绑定 localhost；一旦暴露到网络就是高风险。
- 单进程 SessionManager + 内存会话表，水平扩展需要外部改造。
- 23 个路由模块、196 个注册点，契约漂移需要 OpenAPI/契约测试补齐（当前主要靠测试与前端类型）。
- 前端与后端共享大量隐式 JSON 形状；没有统一 schema 版本化。
