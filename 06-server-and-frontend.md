# 06 · 服务端与前端

> 关键路径：
> `core/framework/server/`（aiohttp）
> `core/frontend/`（React + Vite）
> 默认监听 `127.0.0.1:8787`

## 1. 服务端组成

### 1.1 应用工厂

`core/framework/server/app.py` 构建 aiohttp `Application`：

- 注册 20 多个路由模块（23 个 `routes_*.py` 加 `app.py`、`channels.py` 等）；
- 提供健康检查（`/api/health`、`/api/health/resources`、`/api/startup-status`）；
- 提供浏览器状态与 tab 操作（`/api/browser/*`）；
- 最后一个通配路由 `/{path:.*}` 提供 SPA 静态资源，实现前端路由回退；
- 为阻塞型处理器单独建 32 线程池（`HIVE_REQUEST_EXECUTOR_MAX`），避免 Queen 的阻塞工具调用拖垮 Colony UI。

### 1.2 核心协作对象

| 对象 | 职责 |
| --- | --- |
| `SessionManager` | 活的会话注册表：创建/恢复/停止会话，持有 Queen `AgentLoop` |
| `QueenOrchestrator` | Queen 的启动、阶段切换、工具配置、上下文构建 |
| `ColonyRuntime` | Worker 调度（见 [04](04-colony-runtime.md)） |
| `EventBus` | 内部事件 → SSE / Worker 报告 |
| `SSEResponse` | 服务端推送 |
| `stall_check` | 可选的“说要做却没做”检查 |
| `turn_context` | 外部包在 Queen 每回合前注入内容 |
| `channels` | 聊天应用接入（Slack/Telegram 等） |

## 2. HTTP API 家族

### 2.1 会话

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| POST | `/api/sessions` | 新建 Queen 会话；也可从源会话 fork 成 Colony |
| GET | `/api/sessions` | 列出活跃会话 |
| GET | `/api/sessions/live` | 活跃会话 SSE 流 |
| GET | `/api/sessions/history` | 历史会话 |
| GET | `/api/sessions/{id}/snapshot` | 会话快照 |
| DELETE | `/api/sessions/{id}` | 停止会话 |
| POST | `/api/sessions/{id}/fork` | 分支会话 |
| PATCH | `/api/sessions/{id}/title` | 重命名 |
| GET | `/api/sessions/{id}/attachment/{filename}` | 附件下载 |

### 2.2 执行

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| POST | `/api/sessions/{id}/chat` | 发送用户消息（核心入口） |
| POST | `/api/sessions/{id}/cancel-queen` | 取消当前回合 |
| POST | `/api/sessions/{id}/presence` | 用户在场状态（影响 Sentinel） |
| POST | `/api/sessions/{id}/record-message` | 记录但不一定注入 |
| POST | `/api/sessions/{id}/queen-context` | 构建 Queen 上下文 |

### 2.3 Colony / Worker

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| GET | `/api/sessions/{id}/workers` | 列出 Worker |
| GET | `/api/sessions/{id}/workers/{worker_id}` | Worker 详情 |
| GET | `/api/sessions/{id}/workers/stream` | Worker 实时流 |
| GET | `/api/colonies/{colony_id}/data/tables` | Tracker 表 |
| GET | `/api/colonies/{colony_id}/data/changes` | 数据变更 |
| GET | `/api/colonies/{colony_id}/data/tables/{table}/rows` | 行数据 |
| POST | `/api/colonies/{colony_id}/data/tables/{table}/query` | 查询 |
| POST | `/api/colonies/import` | 导入 Colony tarball |

### 2.4 其余资源

| 家族 | 代表路径 |
| --- | --- |
| Queen 档案 | `/api/queen/profiles`、`/api/queen/{id}/profile`、`/api/queen/{id}/persona` |
| Queen 记忆 | `/api/memories`、`/api/memories/file` |
| Queen 技能 | `/api/queen/{id}/skills`、`/api/skills`、`/api/skills/upload` |
| 技能安装审批 | `/api/sessions/{id}/skill-installs`、`/api/sessions/{id}/skill-installs/{request_id}` |
| 任务计划 | `/api/sessions/{id}/tasks` |
| 模型配置 | `/api/models/overview`、`/api/models/slots/{slot}`、`/api/models/endpoints/{name}` |
| 配置 | `/api/config/llm`、`/api/config/models`、`/api/config/features`、`/api/config/sentinel` |
| 凭证 | `/api/credentials`、`/api/credentials/specs`、`/api/sessions/{id}/credential-form` |
| MCP | `/api/mcp/*` |
| Sentinel | `/api/sentinel/*` |
| 维护 | `/api/maintenance/*` |
| 日志 | `/api/logs/*`（Runtime logs drawer） |

> 路由实现分散在 23 个 `routes_*.py` 中，单文件最大的是 `routes_sessions.py`（3,265 行）与 `routes_execution.py`（2,350 行）。

## 3. SSE 事件模型

`SSEResponse`（`server/sse.py`）把 `EventBus` 事件推给浏览器。`EventType`（`host/event_bus.py:102`）定义了两类事件：

**内部/运行时事件**

- `EXECUTION_STARTED/COMPLETED/FAILED/PAUSED/RESUMED`
- `STATE_CHANGED`、`STATE_CONFLICT`
- `GOAL_PROGRESS`、`GOAL_ACHIEVED`、`CONSTRAINT_VIOLATION`
- `NODE_LOOP_*`
- `LLM_TEXT_DELTA`、`LLM_REASONING_DELTA`、`LLM_TURN_COMPLETE`
- `TOOL_CALL_STARTED`、`TOOL_CALL_COMPLETED`
- `SUBAGENT_REPORT`

**客户端交互事件**

- `CLIENT_OUTPUT_DELTA`、`CLIENT_REASONING`
- `CLIENT_INPUT_REQUESTED`、`CLIENT_INPUT_RECEIVED`、`CLIENT_INPUT_COMMITTED`
- `CLIENT_CREDENTIAL_FORM_REQUESTED`
- `COLONY_SUGGESTION_REQUESTED`
- `SKILL_INSTALL_REQUESTED`

其中几个值得注意：

- `CLIENT_INPUT_RECEIVED` vs `CLIENT_INPUT_COMMITTED`：前者是「收到消息」，后者是「消息真正注入对话」，后者带真实时间戳，UI 用它给用户气泡排序。
- `CLIENT_CREDENTIAL_FORM_REQUESTED`：凭证通过独立 POST 端点直接写入加密存储，**不经过事件或对话**。
- `COLONY_SUGGESTION_REQUESTED`：携带建议的 colony_id，前端弹出 Create Colony 对话框。
- `SKILL_INSTALL_REQUESTED`：`write_skill(scope="global")` 的审批请求，持久化在会话目录，重启后仍在。

## 4. SessionManager 与会话恢复

`session_manager.py`（2,270 行）是「活会话」的中心：

- 创建会话时决定：普通 Queen 会话，还是 Colony Queen 会话。
- 从磁盘恢复：读 `state.json`、`conversations/`、`cursor.json`。
- 注入消息：`inject_event` 到 Queen 循环。
- 处理停泊：会话对象保留，SSE 可断线重连。
- 与 Sentinel、channels、turn_context 协作。

会话冷启动的可靠性依赖 [08-storage-and-data.md](08-storage-and-data.md) 描述的「写穿 + 原子写 + 游标」三件套。

## 5. 前端技术栈

`core/frontend/package.json`：

| 项 | 版本/选择 |
| --- | --- |
| React | 18.3 |
| TypeScript | 5.6 |
| 构建 | Vite 6 |
| 路由 | React Router 7 |
| 样式 | Tailwind CSS 4 + `tailwind-merge` |
| 组件基础 | `clsx`、`lucide-react` |
| 图表 | ECharts 5 + Mermaid 11 |
| Markdown | `react-markdown` + `remark-gfm` + Shiki 高亮 |
| 字体 | Geist / Geist Mono |
| 测试 | Vitest 4 + Testing Library + jsdom |

227 个 `.ts/.tsx` 文件，测试文件与实现文件相邻（如 `ChatPanel.tsx` 与 `ChatPanel.order.test.tsx`）。

## 6. 前端页面

`core/frontend/src/pages/`：

| 页面 | 作用 |
| --- | --- |
| `home.tsx` | 首页：Queen 与 Colony 的蜂巢地图，输入任务并选择 Queen |
| `queen-dm.tsx` | 与 Queen 的直接对话 |
| `colony-chat.tsx` | Colony 会话与执行视图 |
| `queen-profile.tsx` | Queen 档案：人格、模型、灵魂、工具 |
| `org-chart.tsx` | 组织架构：雇佣/移除 Queen，位置目录 |
| `skills-library.tsx` | 技能库 |
| `prompt-library.tsx` | 提示词库 |
| `memory-library.tsx` | 记忆库 |
| `credentials.tsx` | 凭证管理 |
| `models.tsx` | 模型与端点配置（1.0.14 新增） |
| `tool-library.tsx` | 工具目录 |
| `claude-code-queen.tsx` | Claude Code 作为 Queen 的只读视图 |
| `not-found.tsx` | 404 |

## 7. 前端组件骨架

组件目录体现了产品的主要交互面：

- **对话**：`ChatPanel`、`MessageAttachments`、`MarkdownContent`、`ThinkingBlock`、`ParallelSubagentBubble`、`WorkerRunBubble`、`TaskListPanel`。
- **Colony**：`ColonyPanel`、`ColonySettingsModal`、`ColonyToolsSection`、`HiveMap`。
- **Queen**：`QueenAvatar`、`QueenPortraitGlyph`、`QueenSelect`、`QueenSessionSwitcher`、`QueenModelSection`、`QueenSoulSection`、`QueenToolsSection`、`PositionCatalog`。
- **配置**：`SettingsModal`、`ModelPicker`、`ProviderManager`、`McpServersPanel`、`CredentialsModal`、`SentinelSection`、`SentinelConnectorsSection`。
- **运行观测**：`RuntimeLogsDrawer`、`DebugPanel`、`ConnectivityBanner`、`BrowserStatusBadge`。
- **交互部件**：`QuestionWidget`、`MultiQuestionWidget`、`ModelErrorCard`、`NoModelNotice`、`SkillInstallConsent`。
- **新手引导**：`Tutorial/`（`TutorialOverlay`、`TutorialColonyDemo`、`steps.ts`）。

## 8. 前端数据层

- `src/api/*.ts`：按领域拆分的客户端（sessions、colonies、workers、models、skills、memories、config……）。
- `src/api/client.ts`：统一 fetch/错误处理。
- `src/hooks/use-sse.ts`：SSE 连接与重连。
- 其他 hooks：`use-live-sessions`、`use-connectivity-status`、`use-runtime-logs`、`use-away-queen-notifications`、`use-pending-queue`、`use-skill-index`。
- `src/lib/*`：纯函数与领域逻辑（`colony-registry`、`chat-helpers`、`colony-session-restore`、`graphUtils`、`me`、`appLinks`），大多有对应测试。

把领域逻辑放进 `lib/` 并用 Vitest 覆盖，是这个前端比较健康的做法。

## 9. 构建与打包

- 开发：`npm run dev`（Vite）。
- 生产：`npm run build` → `core/frontend/dist/`。
- Python 包通过 `core/pyproject.toml` 的 hatch 自定义构建钩子 `hatch_build.py` 把 `frontend/dist` 打进 wheel，落在 `framework/server/frontend`。
- sdist 声明 `artifacts = ["frontend/dist/"]`，但排除 `frontend/node_modules`。
- 因此从 PyPI 安装的 `aden-hive` 自带 Dashboard，运行 `hive open` 即可，不需要 Node。

## 10. 优点与问题

**优点**

- 前端与后端同源同端口，部署模型极简（一个进程、一个端口）。
- SSE 事件设计细致（received vs committed、凭证表单、审批请求），说明 UI 与运行时的契约是认真设计过的。
- 前端领域逻辑有测试，组件也有针对性测试（排序、锚点、开关等）。
- 打包把 Dashboard 塞进 wheel，终端用户体验好。

**问题**

- `routes_sessions.py` 与 `routes_execution.py` 都在 2,000–3,000 行量级，路由、校验、业务逻辑混杂。
- 20 多个路由模块之间共享状态（SessionManager、EventBus、ColonyRuntime），依赖关系不显式，改动容易牵连。
- SSE 事件类型多且语义微妙，缺少一份「事件时序图」文档。
- 前端 `ChatPanel.tsx` 等组件体量很大，状态来源多（会话、Worker、任务、附件、审批），维护成本高。
- 没有端到端浏览器测试的明显证据（CI 未跑 Playwright 套件；`core/tests/e2e` 存在但未在 CI 中看到）。

**建议**

- 给 SSE 事件补一份「从用户发消息到 Queen 回复」的时序文档，标注每个事件的触发点和消费者。
- 把 `routes_*` 的业务逻辑下沉到 service 层，路由只做解析与校验。
- 增加一条 Playwright/Cypress 冒烟链路（创建会话 → 发消息 → 收到回复 → 建 Colony），覆盖前后端契约。
