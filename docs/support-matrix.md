# 支持矩阵

> 版本：`hive 1.0.19-go.1`
> 快照：2026-11-19，分支 `w9/release` 工作树（发布包构建快照）。
> 状态定义：
> - **已实现**：代码存在，且本次文档验证或仓库测试实际跑过；
> - **部分**：有实现，但只覆盖上游能力的一部分，或没有接到用户入口；
> - **未实现**：这一版没有对应代码；
> - **未验证**：有代码/测试，但没有对真实外部系统跑过（没有凭据或没有环境）。
>
> 证据列里的 `实测` 指本次发布任务用真实二进制跑过；`测试` 指仓库内 Go 测试；`代码` 指只读了实现，没有运行。

## 1. 总览

| 能力 | 状态 | 证据 |
| --- | --- | --- |
| 构建（无 cgo、离线） | 已实现 | `make build`、`make ci` 实测；`CGO_ENABLED=0 GOPROXY=off` |
| `hive --version` | 已实现 | 实测（发布包解压后）`hive 1.0.19-go.1` |
| `hive serve` + `/api/health` | 已实现 | 实测默认 `127.0.0.1:8787`；发布包实测 `--port 8797` 健康检查 200 |
| 创建会话 `POST /api/sessions` | 已实现 | 实测 201，返回 `session_id` |
| 会话投递 `POST /api/sessions/{id}/chat` | 已实现 | 实测 200；无 key 时 503；未知会话 404；空消息 400 |
| 会话列表 `GET /api/sessions` / `hive session list` | 已实现 | 实测 |
| 停止会话 `hive session stop` | 已实现 | 实测成功；不存在时 404 |
| 会话转录持久化 | 已实现 | 实测 `conversations/parts/*.json` 落盘 |
| 同 id 会话恢复上下文 | 已实现，未复跑 | E2E 覆盖；本文档未单独验证 |
| 会话事件 SSE | 已实现 | 实测 `client_input_received`，20s 心跳 |
| OpenAI-compatible 传输 | 已实现 | 实测（本地假上游）：`/chat/completions` + Bearer |
| Anthropic Messages 传输 | 已实现 | 实测（本地假上游）：`/v1/messages` + `x-api-key` |
| 逐 token 模型流 | 已实现（OpenAI-compatible） | 实测 live SSE 单轮 12 帧增量；Anthropic/MiniMax 明确 unsupported 并回退非流式 |
| `run_worker` 并行 worker | 已实现 | 实测 queen 派 worker → worker 执行 → `[WORKER_REPORT]` 回传；`resume_worker_ids` 未实现 |
| tracker 共享账本 | 已实现 | 实测 colony 建库、worker 注入 binding、`tracker_*` 建表/upsert/query |
| 提醒中枢 | 已实现 | idle / task / parallel 等 source 注册并随会话启停 |
| judge（success_criteria） | 已实现 | colony worker 带 criteria 时评估并发布 `judge_verdict`；chat 会话无 criteria 不评估 |
| 内建工具 `read_file` / `write_file` / `terminal_exec` | 已实现，未在文档中触发 | 单测覆盖；本次假模型未发工具调用 |
| 集成工具（会话可用） | 已实现 | 23 个安装器 102 模块 / 854 action，加 senders 1 模块 / 9 action，合计 **103 模块 / 863 action**；会话目录实测 103 个集成工具（扫描器与 senders 打开时） |
| 集成工具（默认关闭） | 已实现，默认关闭 | 6 个网络扫描器 + senders；见 §2.4，实测默认目录 96 个工具 |
| 有意排除的 action | 未实现（有意） | `csv_sql`、`excel_sql` 依赖 DuckDB；纯 Go 无 cgo 不引入，见 §2.3 |
| Colony 磁盘加载 + 会话注册 | 已实现 | 实测 `serve --colony docs_probe` |
| Colony 多 worker 编排闭环 | 部分 | 包级测试；未用真实模型跑多 worker 任务 |
| 13 个默认 queen 档案 | 已实现 | 实测 `hive queen list` |
| 技能 / 记忆 / Tracker / 触发器 / 事件总线 | 部分 | 库层与单测存在；本文档未走通用户入口 |
| Sentinel | 部分 | 8 个包移植、测试通过；未接真实 Telegram/Slack |
| Queen mail | 部分 | 路由已挂载，`serve` 未注入后端（代码路径；预期 401） |
| Claude Code queens | 部分 | 路由存在；未接入运行时（列表为空，代码路径） |
| Browser 工具 / `hive-browser` | 未实现 | 未移植 |
| React dashboard | 已内嵌（发布包） | `release.sh` 默认以 `-tags embed_ui` 构建；实测发布包 `/`、`/sessions`（深链）、`/favicon.png` 均 200。源码树不附带 `frontend/dist`，需 `embed_ui.sh` 现场构建 |
| 附件多模态（`attach_file`） | 部分 | 工具结果层有 `Attachments`/`ImageContent`；`serve` 的 chat runtime 只把文本传给模型，PDF 无 `file` 块，见 §2.5 |
| Janitor | 部分 | `hive janitor status` 实测；`run --execute` 未执行 |
| `hive-cred` | 已实现 | 实测 `browse` / `inspect` |
| MCP 注册表与 CLI | 部分 | 14 个子命令注册；未对真实 MCP server 验证 |
| 在线 CVE 扫描 | 未实现 | Makefile 与 `.github/workflows/` 均无 `govulncheck` / OSV / nancy；见 §6 |

## 2. 集成工具（按 INTEGRATION_BACKLOG.md）

### 2.1 口径

上游 `aden_tools/tools/*` 有 **104 个目录 / 865 个 action**。其中 `file_system_toolkits` 只提供共享 helper（`@mcp.tool` 为 0），不是工具模块，所以与工具对齐的模块面是 **103 个**。

本工作树的覆盖：

| 类别 | 模块 | action | 说明 |
| --- | ---: | ---: | --- |
| `internal/tools/integrations` 的 23 个安装器 | 102 | 854 | 全部由 `sessionIntegrationInstallers` 接入会话目录 |
| `internal/senders`（`hiveserver.RegisterSenderTools`） | 1 | 9 | 不在 integrations 包内；受 `email_senders` 开关控制，默认关闭 |
| **Go 合计** | **103** | **863** | 模块面 102+1 / 104（`file_system_toolkits` 非工具模块） |
| 有意排除 | 0 | 2 | `csv_sql`、`excel_sql`（DuckDB 依赖），见 §2.3 |

23 个安装器的 action 明细见 `REMAINING_MODULES.md` §5（各安装器测试各自钉住 `installed` 数）。安装器之间无重名工具：`TestSessionToolCatalogueInstallsEveryBatch` 用「逐个安装后名字集合 ⊆ 会话目录」+「`installed` 计数等于 registry 大小」双重校验。

会话目录的实测口径（2026-11-19，本工作树）：

```text
HIVE_NETWORK_SCANNERS=1 HIVE_EMAIL_SENDERS=1 → installed=103 registry=103
默认（都不设）                                  → installed=96  registry=96
```

默认的 96 = 103 − 6 个网络扫描器 − senders。

### 2.2 按 backlog 优先级

| 优先级 | backlog 未实现 | 现在的状态 | 结论 |
| --- | --- | --- | --- |
| P0（默认角色必备） | 8 模块 / 21 action | `excel`、`pdf_read`、`web_scrape`、`web_search`、`image_generate` 已移植并接入会话；`senders` 已接入但默认关闭；`attach_file` 已接入（多模态见 §2.5）；`file_system_toolkits` 由内建三件套覆盖基础能力 | **已覆盖** |
| P1（单 token SaaS） | 16 模块 / 161 action | 已移植并接入会话目录 | **已覆盖** |
| P2（企业系统/CRM/财务/BI） | 19 模块 / 150 action | 已移植并接入会话目录 | **已覆盖** |
| P3（基础设施/可观测/数据平台） | 15 模块 / 147 action | 已移植并接入会话目录 | **已覆盖** |
| P4（数据获取/营销/通讯长尾） | 26 模块 / 201 action | 已移植并接入会话目录 | **已覆盖** |

P0 逐项：

| 模块 | 需要的凭据 | 状态 |
| --- | --- | --- |
| `senders_tool` | 无（本地） | 已接入；`email_senders` / `HIVE_EMAIL_SENDERS` 默认关闭 |
| `excel_tool` | 无（本地） | 已接入；6 个 action，`excel_sql` 有意排除 |
| `image_gen_tool` | `HIVE_API_KEY` 等 | 已接入（`image_generate`，batch3-media） |
| `web_search_tool` | Brave / Google CSE | 已接入（`web_search`，batch3-media） |
| `pdf_read_tool` | 无（本地） | 已接入 |
| `web_scrape_tool` | `HIVE_HOME` 等 | 已接入 |
| `attach_file_tool` | `HIVE_STORAGE_PATH` | 已接入；多模态限制见 §2.5 |
| `file_system_toolkits` | 无（本地） | 上游为共享 helper，无工具；基础读写由内建 `read_file`/`write_file`/`terminal_exec` 承担 |

### 2.3 有意不移植（2 个 action，非遗漏）

| 上游 | 未移植 action | 原因 |
| --- | --- | --- |
| `csv_tool` | `csv_sql` | 上游用 DuckDB（native 依赖）；纯 Go 不做 cgo，`gen_catalog.py` 与 `doc.go` 均已注明排除 |
| `excel_tool` | `excel_sql` | 同上；`batch3_excel.go` 与测试断言该 action 返回 `ErrUnknownAction` |

这两个 action 在工具描述里不出现，调用时明确返回未知 action，不假装支持。

### 2.4 默认关闭的开关

| 工具 | 开关 | 默认 | 说明 |
| --- | --- | --- | --- |
| `port_scan`、`dns_security_scan`、`ssl_tls_scan`、`http_headers_scan` | `HIVE_NETWORK_SCANNERS=1` 或 `hive.config.json` 的 `network_scanners` | 关闭 | 会向第三方主机发探测流量 |
| `subdomain_enumerate`、`tech_stack_detect` | 同上 | 关闭 | batch8 的两个扫描器共用同一开关 |
| `senders` | `HIVE_EMAIL_SENDERS=1` 或 `email_senders` | 关闭 | 需要本地发信配置 |

扫描器先按批次注册，再由 `installIntegrationBatches` 在开关关闭时从会话目录摘除；`TestSessionToolCatalogueKeepsNetworkScannersOffByDefault` 同时钉住「默认不在」与「打开后在」。

### 2.5 `attach_file` 的多模态限制

`attach_file` 已接入会话目录（batch8-final），但**多模态只到工具结果层**：

- `ToolResult.Attachments` 与 `ImageContent` 已实现；真实图片（png/jpg/jpeg/webp/gif）会生成 `ImageContent` 块，PDF 没有 content 块。
- `hive serve` 的 chat runtime 只把工具结果的**文本**传给模型（`server.ChatRuntimeDeps.Execute`），所以 `ImageContent` 到不了 LLM；PDF 也没有 Go 侧的 `file` 块（Anthropic 会拒绝 `application/pdf` image 块）。
- 结果 JSON 用 `inline` 字段如实标注每个路径走的是 `text_preview`、`image_block` 还是 `none`，不声称模型看到了字节。
- 文本预览内联上限 8 KiB，完整文件仍会复制到会话附件目录并生成 chip。

## 3. 平台与 cgo 矩阵

### 3.1 cgo

| 检查 | 结果 | 命令 |
| --- | --- | --- |
| 源码中的 `import "C"` | **0** | `grep -rn 'import "C"' --include='*.go' . \| wc -l` |
| SQLite | `modernc.org/sqlite v1.60.1`（纯 Go） | `go.mod` |
| 构建环境 | `CGO_ENABLED=0` | `Makefile` 的 `OFFLINE` |

### 3.2 六个目标

`release.sh` 实测（2026-11-19，本工作树，全部 `-tags embed_ui`）：

| GOOS | GOARCH | 编译 | 测试执行 | 归档 |
| --- | --- | --- | --- | --- |
| darwin | amd64 | 通过 | 未执行 | `hive-1.0.19-go.1-darwin-amd64.tar.gz` |
| darwin | arm64 | 通过 | **本机全量通过** | `hive-1.0.19-go.1-darwin-arm64.tar.gz` |
| linux | amd64 | 通过 | 未执行 | `hive-1.0.19-go.1-linux-amd64.tar.gz` |
| linux | arm64 | 通过 | 未执行 | `hive-1.0.19-go.1-linux-arm64.tar.gz` |
| windows | amd64 | 通过 | 未执行 | `hive-1.0.19-go.1-windows-amd64.zip` |
| windows | arm64 | 通过 | 未执行 | `hive-1.0.19-go.1-windows-arm64.zip` |

「编译通过」只证明能为该目标生成二进制；**测试只在 darwin/arm64 本机跑过**。CI 定义（`.github/workflows/go.yml`）包含 Ubuntu 上的 `make ci` 与六目标交叉编译，`release.yml` 负责打包，但本次没有运行 GitHub Actions。

Windows 已知未验证点：

- `os.Rename` 覆盖已存在文件的原子写语义；
- 目录 fsync 不存在；
- `SIGTERM` 不存在，停止路径只能用 `os.Interrupt`；
- PTY 工具在 Windows 上没有可用实现。

## 4. 离线与网络

| 项 | 限制 | 验证 |
| --- | --- | --- |
| 构建 | 首次需要 `go mod download` 预热 module cache；之后 `GOPROXY=off GOSUMDB=off` 可离线 | 实测 `make ci`、`release.sh` 均在该环境变量下完成 |
| 运行时下载 | 无：不下载可执行代码、不装依赖 | 代码/构建约束 |
| Dashboard（源码树） | 仓库**不附带** `frontend/dist`；没有该目录时 `serve` 只提供 API | 代码路径；本次先跑 `embed_ui.sh` 生成 |
| Dashboard（发布包） | 单二进制内嵌；`serve` 会打印 `Dashboard: http://127.0.0.1:<port>` | 实测 `/`、`/sessions`、`/favicon.png` 200 |
| `embed_ui.sh` | 需要 Node/npm，或预构建 `FRONTEND_DIST` + `SKIP_FRONTEND_BUILD=1` | 实测：`npm ci` + `vite build` 6.32s，产出 41.8 MB 嵌入二进制 |
| `hive open` | 启动服务后向 `https://api.adenhq.com/v1/gateway/availability` 发一次可用性 ping | 代码路径；`serve` 不发 |
| 集成工具 | 真正调用时访问第三方 API；没有凭据就不会成功 | 测试只用假上游 |
| 网络扫描器 | 默认关闭；打开后会探测第三方主机 | 代码 + 会话目录测试 |
| 技能注册表 / MCP `update` | 使用时访问注册表或索引 | 代码路径 |

## 5. 未验证清单（按风险排序）

| 项 | 为什么没验证 | 影响 |
| --- | --- | --- |
| 真实厂商模型调用 | 发布任务没有可用的真实 key | 请求形状已验证；鉴权/额度/模型可用性未知 |
| 863 个集成 action 的真实调用 | 需要大量第三方凭据（backlog 里 74 个模块需凭据） | 只有 `httptest`/假上游证据；真实 API 的字段漂移未覆盖 |
| `attach_file` 让模型看到图片/PDF 字节 | chat runtime 只传文本 | 附件 chip 与预览可用，但不是多模态输入 |
| Colony 多 worker 闭环 | 需要真实模型与长任务 | 调度/报告/预算有单测，端到端未跑 |
| Sentinel 通知 | 需要 Telegram/Slack 凭据 | 引擎单测通过，真实投递未验证 |
| Queen mail | `serve` 未注入后端 | 路由预期 401（代码路径）；`internal/qmail` 与 adapter 存在但未接线 |
| Claude Code queens | 运行时未接入 | 列表为空（代码路径） |
| Browser 工具 | 未移植 | 无 |
| Windows 运行 | 无 Windows 机器 | 只有交叉编译 |
| 在线 CVE 扫描 | **仓库没有做**：Makefile/CI 中没有 `govulncheck`、OSV、nancy 或等价步骤 | 依赖漏洞状态未知；只能确认依赖锁定、无 cgo、无运行时下载 |
| `hive janitor run --execute` | 会删除数据，未在发布验证中执行 | 只验证了 `status` |
| `hive colony delete` | 破坏性操作，未执行 | 只验证了 `list`/`info` |

## 6. 质量门禁实测

在 `go_hive/go-hive/` 下：

```bash
make ci    # fmt-check + vet + test + cross + parity + parity-targets + wiring-check
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test -tags e2e -count=1 ./e2e/...
```

2026-11-19 本工作树实测：

| 项 | 结果 |
| --- | --- |
| `make ci` | 退出码 0；gofmt OK；vet 无输出；37 个包 `ok`，6 个包无测试文件 |
| 测试声明数（parity 口径） | 4,429（含 `e2e` 构建标签下的 2 条） |
| 默认构建执行的测试 | `make test` 退出码 0（38 个包 ok、0 fail；逐条 pass/skip 以 `go test -v` 为准） |
| `e2e`（`-tags e2e`） | `ok github.com/aden-hive/hive-go/e2e 3.247s` |
| `make cross` | 六目标全部编译通过，退出码 0 |
| cgo | `import "C"` 0 处 |
| `check_parity.sh` | `4429/4429` 个测试有 `Parity:` 或 `Parity-exempt:` 注解，0 missing |
| `check_parity_targets.sh` | 4,661 条引用（3,683 test + 978 prod），0 missing files/tests/symbols |

这些数字是**本工作树**的实测值，与 `STATUS.md` 中的 2,238/2,045 等历史快照不同（文档快照时间不同）。

发布包本身另有一轮验证（`hive --version`、`shasum -a 256 -c SHA256SUMS`、解压 darwin-arm64 后 `/` 200）记录在 `release-notes-v1.0.19-go.1.md`。

## 7. 复现

源码树：

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

发布包（带 dashboard）：

```bash
bash go_hive/scripts/embed_ui.sh
bash go_hive/scripts/release.sh
cd go_hive/go-hive/dist/release && shasum -a 256 -c SHA256SUMS
bash go_hive/install.sh --from go_hive/go-hive/dist/release
hive --version && hive serve
```

`quickstart.md` 有完整的分步流程与无 key 行为；`configuration.md` 有模型、凭据与 colony 配置的解析优先级；`release-notes-v1.0.19-go.1.md` 有发布包清单与升级说明。
