# 支持矩阵

> 版本：`hive 1.0.14-go.1`
> 快照：2026-11-19，分支 `w5/docs` 工作树。
> 状态定义：
> - **已实现**：代码存在，且本次文档验证或仓库测试实际跑过；
> - **部分**：有实现，但只覆盖上游能力的一部分，或没有接到用户入口；
> - **未实现**：这一版没有对应代码；
> - **未验证**：有代码/测试，但没有对真实外部系统跑过（没有凭据或没有环境）。
>
> 证据列里的 `实测` 指本次文档任务用真实二进制跑过；`测试` 指仓库内 Go 测试；`代码` 指只读了实现，没有运行。

## 1. 总览

| 能力 | 状态 | 证据 |
| --- | --- | --- |
| 构建（无 cgo、离线） | 已实现 | `make build` 实测；`CGO_ENABLED=0 GOPROXY=off` |
| `hive --version` | 已实现 | 实测 `hive 1.0.14-go.1` |
| `hive serve` + `/api/health` | 已实现 | 实测默认 `127.0.0.1:8787`，健康检查 200 |
| 创建会话 `POST /api/sessions` | 已实现 | 实测 201，返回 `session_id` |
| 会话投递 `POST /api/sessions/{id}/chat` | 已实现 | 实测 200；无 key 时 503；未知会话 404；空消息 400 |
| 会话列表 `GET /api/sessions` / `hive session list` | 已实现 | 实测 |
| 停止会话 `hive session stop` | 已实现 | 实测成功；不存在时 404 |
| 会话转录持久化 | 已实现 | 实测 `conversations/parts/*.json` 落盘 |
| 同 id 会话恢复上下文 | 已实现，未复跑 | E2E 覆盖；本文档未单独验证 |
| 会话事件 SSE | 已实现 | 实测 `client_input_received`，20s 心跳 |
| OpenAI-compatible 传输 | 已实现 | 实测（本地假上游）：`/chat/completions` + Bearer |
| Anthropic Messages 传输 | 已实现 | 实测（本地假上游）：`/v1/messages` + `x-api-key` |
| 逐 token 模型流 | 未实现 | 只有会话事件 SSE；无 `stream` 增量转发 |
| 内建工具 `read_file` / `write_file` / `terminal_exec` | 已实现，未在文档中触发 | 单测覆盖；本次假模型未发工具调用 |
| 集成工具（会话可用） | 部分 | 10 模块 / 165 action 接入会话目录 |
| 集成工具（已移植未接入） | 部分 | batch2 10/19、batch3 3/8，未注册进 `serve` 的会话目录 |
| Colony 磁盘加载 + 会话注册 | 已实现 | 实测 `serve --colony docs_probe` |
| Colony 多 worker 编排闭环 | 部分 | 包级测试；未用真实模型跑多 worker 任务 |
| 13 个默认 queen 档案 | 已实现 | 实测 `hive queen list` |
| 技能 / 记忆 / Tracker / 触发器 / 事件总线 | 部分 | 库层与单测存在；本文档未走通用户入口 |
| Sentinel | 部分 | 8 个包移植、测试通过；未接真实 Telegram/Slack |
| Queen mail | 部分 | 路由已挂载，`serve` 未注入后端（代码路径；预期 401） |
| Claude Code queens | 部分 | 路由存在；未接入运行时（列表为空，代码路径） |
| Browser 工具 / `hive-browser` | 未实现 | 未移植 |
| React dashboard | 未附带 | 仓库无 `frontend/dist`；`serve` 不打印 Dashboard 行 |
| Janitor | 部分 | `hive janitor status` 实测；`run --execute` 未执行 |
| `hive-cred` | 已实现 | 实测 `browse` / `inspect` |
| MCP 注册表与 CLI | 部分 | 14 个子命令注册；未对真实 MCP server 验证 |
| 在线 CVE 扫描 | 未实现 | 仓库无 `govulncheck` / OSV / nancy 集成 |

## 2. 集成工具（按 INTEGRATION_BACKLOG.md）

### 2.1 口径

`INTEGRATION_BACKLOG.md` 的基线是「上游 `aden_tools` 104 个模块 / 865 个 action，生成时已实现 20 个模块 / 184 个 action，未实现 84 个模块 / 680 个 action」。

在本工作树上重新核对后：

| 类别 | 模块 | action | 说明 |
| --- | ---: | ---: | --- |
| 已移植并接入聊天会话 | 10 | 165 | `integrations.New()` 由 `newSessionToolCatalogue` 安装 |
| 已移植、未接入 | 13 | 27 | batch2（10/19）+ batch3（3/8），只有包级 `InstallBatch*` 入口，生产代码没有调用 |
| **已移植合计** | **23** | **192** | |
| 未移植 | 81 | 672 | 84 个 backlog 模块减去 batch3 补上的 excel/pdf_read/web_scrape（action 按 backlog 小计 680 − 8） |

> 计数来源：`internal/tools/integrations/integration_test.go` 钉住 10/165；`batch2_registry_test.go` 钉住 10/19；batch3 的 excel 6、pdf_read 1、web_scrape 1 由各自测试钉住。上游总数取自 backlog 抬头。`680 - 8 = 672`，与「865 - 192 = 673」相差 1，是生成器统计口径差，不影响模块级判断。

已接入会话的 10 个模块：

`get_account_info`、`airtable`、`csv`、`github`、`risk_score`、`slack`、`stripe`、`telegram`、`get_current_time`、`trello`。

### 2.2 按 backlog 优先级

| 优先级 | backlog 未实现 | 现在的状态 | 结论 |
| --- | --- | --- | --- |
| P0（默认角色必备） | 8 模块 / 21 action | `excel_tool`、`pdf_read_tool`、`web_scrape_tool` 已用 batch3 移植（8/9 action，缺 1 个 excel action）；`senders_tool` 有库层 `internal/senders` 但未作为 aden_tools 工具接入；`attach_file_tool` 有附件上传/存储基础但未做工具；`file_system_toolkits` 仅由内建 `read_file`/`write_file`/`terminal_exec` 覆盖基础能力；`image_gen_tool`、`web_search_tool` 未实现 | **部分** |
| P1（单 token SaaS） | 16 模块 / 161 action | 均未移植 | **未实现** |
| P2（企业系统/CRM/财务/BI） | 19 模块 / 150 action | 均未移植 | **未实现** |
| P3（基础设施/可观测/数据平台） | 15 模块 / 147 action | 均未移植 | **未实现** |
| P4（数据获取/营销/通讯长尾） | 26 模块 / 201 action | 均未移植 | **未实现** |

P0 逐项：

| 模块 | 需要的凭据 | 状态 |
| --- | --- | --- |
| `senders_tool` | 无（本地） | 部分：`internal/senders` 有 mailer/engine/store，未接工具目录 |
| `excel_tool` | 无（本地） | 已移植（batch3），未接入会话；6 个 action |
| `image_gen_tool` | `HIVE_API_KEY` 等 | 未实现 |
| `web_search_tool` | Brave / Google CSE | 未实现 |
| `pdf_read_tool` | 无（本地） | 已移植（batch3），未接入会话 |
| `web_scrape_tool` | `HIVE_HOME` 等 | 已移植（batch3），未接入会话 |
| `attach_file_tool` | `HIVE_STORAGE_PATH` | 部分：附件子系统有 sanitize/MIME/预览，未做工具 |
| `file_system_toolkits` | 无（本地） | 部分：内建三件套覆盖基础读写与命令执行 |

### 2.3 不属于 aden_tools 的原生缺口

| 缺口 | 状态 |
| --- | --- |
| `terminal_rg` / `terminal_glob` / `terminal_output_get` / `terminal_job_logs` / `terminal_job_manage` | 未移植为同名工具；终端执行由 `terminal_exec` 承担 |
| `terminal_job_start` / `terminal_pty_open` / `run` / `close` | 未接入（有 `internal/tools/builtin/terminal_pty_*` 平台文件，但没有工具入口） |
| `search_messages` / `search_timeline` | 库层 `internal/memory` 有检索；未做工具入口 |
| `browser_setup` + `hive-browser` | 未移植 |
| `@server:chart-tools` | 未移植；`internal/charts` 有渲染包 |
| `csv_sql` | 未移植（需要 DuckDB 等价物） |

## 3. 平台与 cgo 矩阵

### 3.1 cgo

| 检查 | 结果 | 命令 |
| --- | --- | --- |
| 源码中的 `import "C"` | **0** | `grep -rn 'import "C"' --include='*.go' . \| wc -l` |
| SQLite | `modernc.org/sqlite v1.60.1`（纯 Go） | `go.mod` |
| 构建环境 | `CGO_ENABLED=0` | `Makefile` 的 `OFFLINE` |

### 3.2 六个目标

`make cross` 实测（2026-11-19，本工作树）：

| GOOS | GOARCH | 编译 | 测试执行 | 产物 |
| --- | --- | --- | --- | --- |
| darwin | amd64 | 通过 | 未执行 | `dist/hive-darwin-amd64` |
| darwin | arm64 | 通过 | **本机全量通过** | `dist/hive-darwin-arm64` |
| linux | amd64 | 通过 | 未执行 | `dist/hive-linux-amd64` |
| linux | arm64 | 通过 | 未执行 | `dist/hive-linux-arm64` |
| windows | amd64 | 通过 | 未执行 | `dist/hive-windows-amd64.exe` |
| windows | arm64 | 通过 | 未执行 | `dist/hive-windows-arm64.exe` |

实测输出：

```text
== darwin/amd64 ==
  -> dist/hive-darwin-amd64
== darwin/arm64 ==
  -> dist/hive-darwin-arm64
== linux/amd64 ==
  -> dist/hive-linux-amd64
== linux/arm64 ==
  -> dist/hive-linux-arm64
== windows/amd64 ==
  -> dist/hive-windows-amd64.exe
== windows/arm64 ==
  -> dist/hive-windows-arm64.exe
cross: 全部平台编译通过（无 main 包时不产出二进制）
```

「编译通过」只证明能为该目标生成二进制；**测试只在 darwin/arm64 本机跑过**。CI 定义（`go_hive/.github/workflows/go.yml`）包含 Ubuntu 上的 `make ci` 与六目标交叉编译，但本次没有运行 GitHub Actions。

Windows 已知未验证点：

- `os.Rename` 覆盖已存在文件的原子写语义；
- 目录 fsync 不存在；
- `SIGTERM` 不存在，停止路径只能用 `os.Interrupt`；
- PTY 工具在 Windows 上没有可用实现。

## 4. 离线与网络

| 项 | 限制 | 验证 |
| --- | --- | --- |
| 构建 | 首次需要 `go mod download` 预热 module cache；之后 `GOPROXY=off GOSUMDB=off` 可离线 | 实测 `make build` 与 `make cross` 均在该环境变量下完成 |
| 运行时下载 | 无：不下载可执行代码、不装依赖 | 代码/构建约束 |
| Dashboard | 仓库**不附带** `frontend/dist`；没有该目录时 `serve` 只提供 API，不打印 Dashboard 行 | 实测 |
| `make embed-ui` | 需要 Node/npm（或预构建 `FRONTEND_DIST` + `SKIP_FRONTEND_BUILD=1`） | 代码路径，未执行 |
| `hive open` | 启动服务后向 `https://api.adenhq.com/v1/gateway/availability` 发一次可用性 ping | 代码路径；`serve` 不发 |
| 集成工具 | 真正调用时访问第三方 API；没有凭据就不会成功 | 测试只用假上游 |
| 技能注册表 / MCP `update` | 使用时访问注册表或索引 | 代码路径 |

## 5. 未验证清单（按风险排序）

| 项 | 为什么没验证 | 影响 |
| --- | --- | --- |
| 真实厂商模型调用 | 文档任务没有可用的真实 key | 请求形状已验证；鉴权/额度/模型可用性未知 |
| 192 个集成 action 的真实调用（其中 165 个已接入会话） | 需要大量第三方凭据（backlog 里 74 个模块需凭据） | 只有 `httptest`/假上游证据；真实 API 的字段漂移未覆盖 |
| Colony 多 worker 闭环 | 需要真实模型与长任务 | 调度/报告/预算有单测，端到端未跑 |
| Sentinel 通知 | 需要 Telegram/Slack 凭据 | 引擎单测通过，真实投递未验证 |
| Queen mail | `serve` 未注入后端 | 路由预期 401（代码路径）；`internal/qmail` 与 adapter 存在但未接线 |
| Claude Code queens | 运行时未接入 | 列表为空（代码路径） |
| Browser 工具 | 未移植 | 无 |
| Windows 运行 | 无 Windows 机器 | 只有交叉编译 |
| 在线 CVE 扫描 | **仓库没有做**：Makefile/CI 中没有 `govulncheck`、OSV、nancy 或等价步骤 | 依赖漏洞状态未知；只能确认依赖锁定、无 cgo、无运行时下载 |
| `hive janitor run --execute` | 会删除数据，未在文档验证中执行 | 只验证了 `status` |
| `hive colony delete` | 破坏性操作，未执行 | 只验证了 `list`/`info` |

## 6. 质量门禁实测

在 `go_hive/go-hive/` 下：

```bash
make cross
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test ./...
bash ../scripts/check_parity.sh .
bash ../scripts/check_parity_targets.sh . ../../core
```

实测结果：

| 项 | 结果 |
| --- | --- |
| `make cross` | 六目标全部编译通过，退出码 0 |
| `go test ./...` | 全部包 `ok`（`platform`、`uiembed`、`soul`、`version` 无测试文件），退出码 0 |
| `check_parity.sh` | `3024/3024` 个测试有 `Parity:` 或 `Parity-exempt:` 注解，0 missing |
| `check_parity_targets.sh` | 2636 条引用，0 missing files/tests，134 条 shorthand/prose |

这些数字是**本次工作树**的实测值，与 `STATUS.md` 中的 2,238/2,045 等历史快照不同（文档快照时间不同）。

## 7. 复现

```bash
cd go_hive/go-hive
make build
./dist/hive --version
HIVE_HOME="$HOME/.hive-support-matrix" ./dist/hive serve --port 8787
```

另开终端：

```bash
curl -sS http://127.0.0.1:8787/api/health
curl -sS -X POST http://127.0.0.1:8787/api/sessions -H 'Content-Type: application/json' -d '{}'
```

`quickstart.md` 有完整的分步流程与无 key 行为；`configuration.md` 有模型、凭据与 colony 配置的解析优先级。
