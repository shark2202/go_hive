# Changelog

本文件记录 Go 移植（`github.com/aden-hive/hive-go`）的版本内容。上游 Python 版本的发布说明在仓库根目录的 [`CHANGELOG.md`](../../CHANGELOG.md)。

## 1.0.14-go.1 — 2026-11-19

对应上游 `aden-hive` / `aden-tools` 1.0.14（提交 `c6c2cdcf`）。版本号规则：`<上游版本>-go.<移植修订号>`；`go.1` 是第一个可运行的 Go 移植版本。

### 新增

**CLI 与 HTTP 服务**

- `hive` 根命令：`serve`、`open`、`queen`、`colony`、`session`、`chat`、`skill`、`debugger`、`mcp`、`janitor`。
- `hive serve` 默认监听 `127.0.0.1:8787`，提供 `GET /api/health`、`GET /api/startup-status`、会话创建/列表/详情/停止、会话 chat、会话事件 SSE、事件历史分页、触发器生命周期、配置/模型/凭据/记忆/维护等路由（完整清单以 `internal/server/app.go` 的注册为准）。
- `hive chat <session_id> <message> [--server URL]`：通过 HTTP API 向活会话投递消息。
- `hive session list [--cold] [--json] [--server URL]`、`hive session stop <id>`。
- `hive queen list/show/sessions`：13 个内嵌默认 queen 档案。
- `hive colony list/info/delete`：磁盘 colony 的查看与删除。
- `hive-cred token/browse/inspect`：脚本化取凭据，OAuth 先刷新再打印。

**会话运行时**

- `POST /api/sessions` 创建真实会话：真实 provider、内建工具、`ChatRuntime`、落盘 transcript。
- `POST /api/sessions/{id}/chat`：空消息 400、未知会话 404、无 LLM 503、已 spawn colony 409。
- `GET /api/sessions/{id}/events`：会话事件 SSE，20 秒心跳，`?types=` 过滤。
- 转录持久化到 `$HIVE_HOME/queens/default/sessions/<session_id>/conversations/parts/*.json`；同 id 会话重建时恢复。
- 上下文压缩、停滞检测、提醒 hub、运行预算与工具超时。

**LLM**

- 真实 HTTP 传输：OpenAI-compatible `POST /chat/completions`（Bearer）与 Anthropic Messages `POST /v1/messages`（`x-api-key` + `anthropic-version: 2023-06-01`）。
- 协议按模型族选择：`anthropic/*`、`claude-*`、`kimi/*`、`hive/*` → Anthropic；`openrouter/*` 与其余 → OpenAI。
- 模型/provider/base/key 的四层解析（请求 > `serve --model` > `configuration.json` > 默认/环境变量）。
- 凭据缺失时明确 503，不伪造投递成功。
- 模型目录、能力目录、密钥池、429 重试与退避、缓存断点、成本计算。

**工具**

- 内建：`read_file`、`write_file`、`terminal_exec`（沙箱根为进程 cwd；默认 60s、上限 600s；每路输出 256 KiB）。
- 集成：10 个模块 / 165 个 action 接入聊天会话（account_info、airtable、csv、github、risk_score、slack、stripe、telegram、time、trello）。
- 另行移植但**未接入**会话目录：batch2 10 个模块 / 19 个 action（arxiv、duckduckgo、wikipedia、openmeteo、yahoo finance、youtube transcript、dns/http/tls/port 扫描），batch3 3 个模块 / 8 个 action（excel、pdf_read、web_scrape）。
- MCP 客户端（stdio/http/sse/unix）、连接池、注册表与 `hive mcp` 14 个子命令。

**子系统**

- `internal/hivehome`、`session`、`storage`、`credentials`（Fernet、四种存储、OAuth2、模板）、`skills`、`memory`、`tracker`、`agentloop`、`agents`、`colony`、`orchestrator`、`playbook`、`ratelimit`、`triggers`、`eventbus`、`tasks`、`mcpregistry`、`maintenance`（三档 janitor）、`sessionreport`、`attachments`、`sentinel`、`qmail`、`charts`。
- 离线、无 cgo 构建：`CGO_ENABLED=0`，SQLite 使用 `modernc.org/sqlite`，六个平台交叉编译。

### 变更

- `hive serve` 没有模型 key 时仍会创建并列出会话，但 `chat` 返回 `503 {"error":"No LLM configured for this session"}`；这是有意行为，不再静默排队。
- `hive session list` 默认连 `http://127.0.0.1:8787`；服务未启动时提示改用 `--cold`。
- 会话事件 SSE 的事件名是小写（如 `client_input_received`），按事件名精确过滤。

### 验证（2026-11-19，本工作树）

| 项 | 命令 | 结果 |
| --- | --- | --- |
| 构建 | `make build`（`CGO_ENABLED=0 GOPROXY=off`） | 通过，`dist/hive` |
| 版本 | `./dist/hive --version` | `hive 1.0.14-go.1` |
| 单元/包测试 | `go test ./...` | 全部 `ok`，退出码 0 |
| 六平台交叉编译 | `make cross` | 6/6 通过 |
| cgo | `grep -rn 'import "C"' --include='*.go' .` | 0 |
| parity 注解 | `check_parity.sh` | 3024/3024，0 missing |
| parity 引用 | `check_parity_targets.sh` | 2636 refs，0 missing，134 shorthand/prose |
| 文档流程 | `quickstart.md` | 构建、serve、健康检查、创建会话、无 key 503、两种协议（假上游）、SSE、停止均实跑 |

`go test -tags e2e ./e2e/...` 不在本次文档验证的执行范围内；仓库的交付记录称其通过，本文不重复声称。

### 已知缺口

1. **集成工具**：81 个 backlog 模块未移植；已移植的 27 个 action（batch2 + batch3）没有接入会话工具目录；全部集成只对假上游验证，没有真实第三方调用。
2. **Colony**：`serve --colony` 能加载并注册会话，但多 worker 编排闭环没有用真实模型跑过。
3. **Queen mail**：路由已挂载，`serve` 未注入后端。
4. **Claude Code queens**：运行时未接入。
5. **Browser 工具**：未移植。
6. **Dashboard**：仓库不附带 `frontend/dist`；`serve` 只提供 API。
7. **逐 token 模型流**：没有；SSE 只发会话事件。
8. **Sentinel**：引擎单测通过，未接真实 Telegram/Slack。
9. **平台**：只有 darwin/arm64 跑过测试；Windows 的原子替换、信号、PTY 行为未验证。
10. **安全**：没有在线 CVE 扫描（无 `govulncheck`/OSV/nancy）；依赖版本锁定、无 cgo、无运行时下载。
11. **持久化兼容**：未验证与 Python 版共用同一个 `HIVE_HOME`；建议给 Go 版独立目录。

### 升级说明

这是第一个 Go 移植版本，没有从更早 Go 版本升级的迁移路径。`HIVE_HOME` 默认仍是 `~/.hive`；如果本机已有 Python 版数据，建议显式设置独立的 `HIVE_HOME` 再启动，避免两套实现互相覆盖。
