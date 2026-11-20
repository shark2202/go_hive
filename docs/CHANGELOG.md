# Changelog

本文件记录 Go 移植（`github.com/aden-hive/hive-go`）的版本内容。上游 Python 版本的发布说明在仓库根目录的 [`CHANGELOG.md`](../../CHANGELOG.md)。

## 1.0.26-go.1 — 2026-11-20

overseer 回复写入会话转录；events.jsonl flush 边界；归档补齐 `hive-browser`/`browserbridge`（六平台，包内 SHA256SUMS 全覆盖）；Orca 浏览器 UI E2E 7/7。测试 4,566、引用 4,843。

## 1.0.25-go.1 — 2026-11-20

worker 进度镜像、overseer credential park、共享 park/ask_user、overseer 提问到客户端；timeline 竞态修复。测试 4,561、引用 4,840。

## 1.0.24-go.1 — 2026-11-19

credential 提交、预载 SkillsCatalog、doom park reason、judge criteria；Queen 两阶段落盘与 overseer 桥接。测试 4,551、引用 4,798。

## 1.0.23-go.1 — 2026-11-19

提醒供给、持久性（scaffold/续跑/resume/credential park）、loop 保护（stall/doom park/Queen 检查点/worker spillover）。测试 4,527、引用 4,765。

## 1.0.22-go.1 — 2026-11-19

目标评估闭环、约束进 prompt、改进信号；excerpts 注入、timeline 写入、spillover、search_tools。测试 4,495、引用 4,713。

## 1.0.21-go.1 — 2026-11-19

1.0.20 补丁：Anthropic 流式、park 落盘 resume、soul 进 persona；测试 4,468、引用 4,681。MiniMax 保持非流式回退。

## 1.0.20-go.1 — 2026-11-19

1.0.19 补丁：rate limit 执行、会话花费硬上限、memory_timeline 工具，修复 soul 测试竞态；测试 4,460、引用 4,664。

## 1.0.19-go.1 — 2026-11-19

1.0.18 补丁：并行只读工具、预算软提醒、只读 credential_status；测试 4,429、引用 4,661。

## 1.0.18-go.1 — 2026-11-19

1.0.17 补丁：search_messages/search_timeline、doom-loop 检测、worker task list、run_playbook status/stop；测试 4,422、引用 4,657。

## 1.0.17-go.1 — 2026-11-19

1.0.16 的补丁版：colony 事件桥 UI、overseer 输入源、MCP 白名单、worker reminder hub、REST reminders 镜像；测试 4,406、parity 引用 4,647。

## 1.0.16-go.1 — 2026-11-19

对应上游 `aden-hive` / `aden-tools` 1.0.14（`c6c2cdcf`）。本版补齐 runtime spine 的方法层、自治层、沉淀层与能力边界，并修复 worker 身份与工具声明。

### 新增
- `run_worker` 闭环与 `[WORKER_REPORT]`（含 `duration_seconds`/`output_file`），worker 转录落盘与 `resume_worker_ids`。
- overseer 工具面（`run_worker`/tracker/task）与 `serve --colony` 可对话会话。
- `write_skill`/`skill_view`/`list_skills`；`run_playbook`（Go 原生 JSON；Python 脚本明确 unsupported）。
- `QueenExecutor`/`InjectTrigger`：timer/webhook 唤醒与 `triggers.json` 重启恢复。
- Sentinel 停车分类与升级落盘。
- reflection 后 soul 自动沉淀。
- MCP 启动装载：`mcp__<server>__<tool>` 注册进会话与 worker。
- 看门狗异常告警（六类 + macOS 通知 + 去抖）。

### 修复
- fork 的 `worker.json` 不再写 Queen persona；`worker.json.tools` 真实生效。
- 看门狗状态文件共用、变量名多字节、误报 dirty 三类问题。

### 质量
| 项 | 结果 |
| --- | --- |
| 测试 | 4,395（4395/4395 注解） |
| parity 引用 | 4,645（3,674 test + 971 prod），0 missing |
| 门禁 / QC / 冒烟 | 7/7、8/8、13/13 |

## 1.0.15-go.1 — 2026-11-19

对应上游 `aden-hive` / `aden-tools` 1.0.14（提交 `c6c2cdcf`）。本版是 `1.0.14-go.1` 之后的行为对齐与安全修复版本，安装与升级无需迁移。

### 新增

- 会话事件链：`execution_started/completed/failed`、`loop_state_changed`、`node_loop_started/iteration/completed`、`client_output_delta`、`llm_turn_complete`、`tool_call_started/completed`；SSE 首帧 `session_snapshot`。
- queen 人格注入（`queens/<id>/profile.yaml` → 系统提示词，缺失时回退通用提示）。
- `ask_user` 工具与会话停车：`awaiting_user` + `client_input_requested`，回答后恢复同一轮。
- queen 记忆注入：每轮 global + queen 两块 `<system-reminder>` recall，两 queen 互不可见。
- `queen_resume_from`：复用旧会话 id 与目录（上游 `session_manager.py` 语义），重启后旧 id 不再 404。
- 任务工具：`task_create / task_update / task_list / task_get`。
- colony 生命周期接线：`CreateColonyFromSource` 注入、`Host.StartOverseer` 生产入口、colony 缺失 404、启动失败 5xx。
- `agentloop.Judge`：`SuccessCriteria` 触发、`ACCEPT/RETRY/ESCALATE`、verdict 事件（chat 会话暂无成功标准来源）。
- `GET /api/sessions/{id}/stats` 对齐上游 7 个顶层键 + `event_bus.events_by_type` 展开，colony 统计逐字段合并且保留额外键。

### 修复

- 附件读 symlink 逃逸：`EvalSymlinks` + `HIVE_HOME` 边界校验，拒绝时不回显路径。
- 附件写 symlink 逃逸：以 queen 目录为锚 `os.OpenRoot` 逐段校验，失败只回通用错误。
- `/api` 点段路径：含 `..`（含 `%2e`）的 API 请求在 `ServeMux` 前 400，堵住 307 跳出 API 面。
- 任务请求体限流：`http.MaxBytesReader` + 读满 body，超限 413。
- 会话停止时同时停止 colony 宿主，避免 overseer/worker 泄漏。
- `/api/*` 未注册路径返回 JSON 404，不再回退 `index.html`。

### 质量

| 项 | 结果 |
| --- | --- |
| 测试声明数（parity 口径） | 4,256 |
| `check_parity.sh` | 4256/4256，0 missing |
| `check_parity_targets.sh` | 4,489 refs（3,591 test + 898 prod），0 missing |
| 七道门禁 | 全部退出码 0 |

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
- 集成：23 个安装器把 **102 个模块 / 854 个 action** 接入聊天会话目录（首个 10 模块 / 165 action 来自 `integrations.New` 的声明式 catalog，其余由 batch2–batch8 安装器注册）；另有 `internal/senders` 提供 1 个模块 / 9 个 action，受 `email_senders` 开关控制，默认关闭。合计 **103 个模块 / 863 个 action**（模块面 102+1/104）。
- 有意排除 2 个依赖 DuckDB 的 SQL action（`csv_sql`、`excel_sql`），调用时明确返回未知 action。
- 默认关闭的探测类工具：`port_scan`、`dns_security_scan`、`ssl_tls_scan`、`http_headers_scan`、`subdomain_enumerate`、`tech_stack_detect`，由 `HIVE_NETWORK_SCANNERS` 或 `hive.config.json: network_scanners` 开启；默认会话目录实测 96 个集成工具（103 − 6 扫描器 − senders）。
- `attach_file`：本地文件转 attachment chip + 文本预览；图片生成 `ImageContent`，但 `serve` 的 chat runtime 只传文本，PDF 无 `file` 块，结果 JSON 的 `inline` 字段如实标注（多模态只到工具结果层）。
- MCP 客户端（stdio/http/sse/unix）、连接池、注册表与 `hive mcp` 14 个子命令。

**子系统**

- `internal/hivehome`、`session`、`storage`、`credentials`（Fernet、四种存储、OAuth2、模板）、`skills`、`memory`、`tracker`、`agentloop`、`agents`、`colony`、`orchestrator`、`playbook`、`ratelimit`、`triggers`、`eventbus`、`tasks`、`mcpregistry`、`maintenance`（三档 janitor）、`sessionreport`、`attachments`、`sentinel`、`qmail`、`charts`。
- 离线、无 cgo 构建：`CGO_ENABLED=0`，SQLite 使用 `modernc.org/sqlite`，六个平台交叉编译。

**打包与分发**

- `scripts/release.sh` 产出六平台归档（darwin/linux `tar.gz`、windows `zip`）与发布级 `SHA256SUMS`；包内另带 `hive[.exe] LICENSE NOTICE README.md SHA256SUMS`。
- 默认以 `-tags embed_ui` 构建：`scripts/embed_ui.sh` 先构建/暂存 `core/frontend/dist`，dashboard 因此内嵌在单二进制里，`serve` 打印 `Dashboard:` 行并直接提供 `/`。前端不可用时会退回普通二进制并明确告警。
- `install.sh` 按平台选归档、强制校验发布级与包内 `SHA256SUMS`、安装到 `~/.local/bin`。
- 本次归档的大小与 SHA-256 见 `release-notes-v1.0.14-go.1.md`。

### 变更

- `hive serve` 没有模型 key 时仍会创建并列出会话，但 `chat` 返回 `503 {"error":"No LLM configured for this session"}`；这是有意行为，不再静默排队。
- `hive session list` 默认连 `http://127.0.0.1:8787`；服务未启动时提示改用 `--cold`。
- 会话事件 SSE 的事件名是小写（如 `client_input_received`），按事件名精确过滤。

### 验证（2026-11-19，本工作树）

| 项 | 命令 | 结果 |
| --- | --- | --- |
| 构建 | `make ci`（`CGO_ENABLED=0 GOPROXY=off`） | 退出码 0；gofmt OK；vet 无输出 |
| 版本 | `./dist/hive --version`（发布包解压后同） | `hive 1.0.14-go.1` |
| 单元/包测试 | `go test ./...` | 4,158 条执行：4,154 pass / 4 skip / 0 fail；37 包 `ok`，6 包无测试文件 |
| 测试声明数 | `bash scripts/check_parity.sh .` | 4,160（含 `e2e` 构建标签下的 2 条） |
| E2E | `go test -tags e2e -count=1 ./e2e/...` | `ok …/e2e 3.247s` |
| 六平台交叉编译 | `make cross` + `scripts/release.sh` | 6/6 通过 |
| cgo | `grep -rn 'import "C"' --include='*.go' .` | 0 |
| parity 注解 | `check_parity.sh` | 4160/4160，0 missing |
| parity 引用 | `check_parity_targets.sh` | 4,357 refs（3,527 test + 830 prod），0 missing |
| 发布归档 | `scripts/release.sh` | 6 归档 + `SHA256SUMS`；`shasum -a 256 -c` 全部 OK |
| 发布包运行 | 解压 `hive-1.0.14-go.1-darwin-arm64.tar.gz` | `--version` 正确；`/`、`/sessions`、`/favicon.png` 均 200 |
| 文档流程 | `quickstart.md` | 构建、serve、健康检查、创建会话、无 key 503、两种协议（假上游）、SSE、停止均实跑 |

`go test -tags e2e ./e2e/...` 在本次发布验证中执行：`ok github.com/aden-hive/hive-go/e2e 3.247s`。它用 `httptest` + 脚本化 completer，不访问网络。

### 已知缺口

1. **集成工具**：覆盖 102+1/104 个模块、863/865 个 action；`csv_sql`、`excel_sql` 因 DuckDB 依赖有意排除。全部集成只对假上游验证，没有真实第三方调用（按用户决定本阶段不验证）。
2. **`attach_file` 多模态**：图片/PDF 只到 attachment chip 与文本预览；`serve` 的 chat runtime 只传文本，模型看不到图片或 PDF 字节。
3. **Colony**：`serve --colony` 能加载并注册会话，但多 worker 编排闭环没有用真实模型跑过。
4. **Queen mail**：路由已挂载，`serve` 未注入后端。
5. **Claude Code queens**：运行时未接入。
6. **Browser 工具**：未移植。
7. **Dashboard**：发布包内嵌 dashboard；源码树不附带 `frontend/dist`，需要 `scripts/embed_ui.sh`（或 `make embed-ui`）现场构建。
8. **逐 token 模型流**：没有；SSE 只发会话事件。
9. **Sentinel**：引擎单测通过，未接真实 Telegram/Slack。
10. **平台**：只有 darwin/arm64 跑过测试；Windows 的原子替换、信号、PTY 行为未验证。
11. **安全**：没有在线 CVE 扫描（无 `govulncheck`/OSV/nancy）；依赖版本锁定、无 cgo、无运行时下载。
12. **默认关闭**：6 个网络扫描器需 `HIVE_NETWORK_SCANNERS`，`senders` 需 `HIVE_EMAIL_SENDERS`；不设置时它们不在会话目录里。
13. **持久化兼容**：未验证与 Python 版共用同一个 `HIVE_HOME`；建议给 Go 版独立目录。

### 升级说明

这是第一个 Go 移植版本，没有从更早 Go 版本升级的迁移路径。`HIVE_HOME` 默认仍是 `~/.hive`；如果本机已有 Python 版数据，建议显式设置独立的 `HIVE_HOME` 再启动，避免两套实现互相覆盖。

从发布归档安装：

```bash
bash go_hive/install.sh --from go_hive/go-hive/dist/release
hive --version
```

或手工解压任意平台归档（包内自带 `SHA256SUMS`），把 `hive`（Windows 为 `hive.exe`）放到 `PATH`。升级时用同一条 `install.sh` 覆盖安装即可；发布包不读写旧 Go 二进制，`HIVE_HOME` 下的会话与凭据格式未变。
