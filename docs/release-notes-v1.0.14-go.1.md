# 发布说明：hive 1.0.14-go.1

> 发布包版本：`hive 1.0.14-go.1`（Go 模块 `github.com/aden-hive/hive-go`）
> 对应上游：`aden-hive` / `aden-tools` 1.0.14（提交 `c6c2cdcf`）
> 构建日期：2026-11-19
> 构建环境：macOS 26.7.1（darwin/arm64）、Go 1.26.5、Node 24.21.0 / npm 11.19.0、`CGO_ENABLED=0`

这是第一个可运行的 Go 移植版本。发布包是**单二进制内嵌 dashboard**：解压即用，不需要 Node、Python 或外部静态资源目录。

## 1. 本版包含

**CLI 与 HTTP 服务**

- `hive` 根命令：`serve`、`open`、`queen`、`colony`、`session`、`chat`、`skill`、`debugger`、`mcp`、`janitor`；独立入口 `hive-cred`。
- `hive serve` 默认 `127.0.0.1:8787`，提供健康检查、启动状态、会话创建/列表/详情/停止、chat、会话事件 SSE、事件历史、触发器、配置/模型/凭据/记忆/维护等路由。
- 发布包内嵌 dashboard：`serve` 打印 `Dashboard:` 行，`/`、前端深链与静态资源由同一进程提供。

**会话运行时与模型**

- 真实 `ChatRuntime`：真实 provider、内建工具、转录落盘到 `$HIVE_HOME/queens/default/sessions/<id>/conversations/parts/*.json`，同 id 会话可恢复。
- OpenAI-compatible（`POST /chat/completions`，Bearer）与 Anthropic Messages（`POST /v1/messages`，`x-api-key` + `anthropic-version: 2023-06-01`）两种真实 HTTP 传输；按模型族选协议。
- 无 key 时 `chat` 明确 503，不伪造投递成功；空消息 400、未知会话 404、已 spawn colony 409。

**工具**

- 内建：`read_file`、`write_file`、`terminal_exec`（cwd 沙箱、原子写、进程组超时、输出限额）。
- 集成：23 个安装器 **102 模块 / 854 action** + `senders` **1 模块 / 9 action**，合计 **103 模块 / 863 action**。模块面覆盖 **102+1 / 104**（`file_system_toolkits` 是共享 helper，不是工具模块）。
- 有意排除 **2 个 DuckDB 依赖的 SQL action**：`csv_sql`、`excel_sql`。纯 Go 不引入 cgo/DuckDB，调用时返回未知 action。
- 默认关闭：6 个网络扫描器（`port_scan`、`dns_security_scan`、`ssl_tls_scan`、`http_headers_scan`、`subdomain_enumerate`、`tech_stack_detect`）需 `HIVE_NETWORK_SCANNERS=1` 或 `hive.config.json: network_scanners`；`senders` 需 `HIVE_EMAIL_SENDERS=1` 或 `email_senders`。默认会话目录实测 96 个集成工具。
- MCP 客户端（stdio/http/sse/unix）、连接池、注册表与 `hive mcp` 14 个子命令。

**子系统**

- `hivehome`、`session`、`storage`、`credentials`（Fernet、四种存储、OAuth2、模板）、`skills`、`memory`、`tracker`、`agentloop`、`agents`、`colony`、`orchestrator`、`playbook`、`ratelimit`、`triggers`、`eventbus`、`tasks`、`mcpregistry`、`maintenance`（三档 janitor）、`sessionreport`（诊断包）、`attachments`、`sentinel`、`qmail`、`charts`。
- 离线、无 cgo：`CGO_ENABLED=0`，SQLite 用 `modernc.org/sqlite`，六平台交叉编译。

## 2. 安装

### 2.1 用 install.sh（推荐）

```bash
cd go_hive
bash install.sh --from go-hive/dist/release
hive --version
```

`install.sh` 会按本机 `OS/ARCH` 选择归档，先校验发布级 `SHA256SUMS`，解压后再校验包内 `SHA256SUMS`，最后安装到 `~/.local/bin`（可用 `--dir` 改目录）。本次实测：

```text
install: using local archive: .../hive-1.0.14-go.1-darwin-arm64.tar.gz
install: verified SHA256: hive-1.0.14-go.1-darwin-arm64.tar.gz
install: verified archive contents
install: installed hive -> <dir>/hive
hive 1.0.14-go.1
```

从远端发布页安装时用 `--remote`（默认基址 `https://github.com/aden-hive/hive/releases/download/v<version>`）：

```bash
bash install.sh --remote
```

### 2.2 手工解压

```bash
tar -xzf hive-1.0.14-go.1-darwin-arm64.tar.gz
shasum -a 256 -c SHA256SUMS     # Windows: certutil 或 Get-FileHash 对照
./hive --version
./hive serve
```

Windows 归档是 zip，二进制名为 `hive.exe`：

```powershell
Expand-Archive hive-1.0.14-go.1-windows-amd64.zip -DestinationPath hive
.\hive\hive.exe --version
```

### 2.3 归档内容

每个归档根部包含：

```text
hive[.exe]  LICENSE  NOTICE  THIRD_PARTY_NOTICES.md  README.md  SHA256SUMS
```

`LICENSE` 是仓库根 `LICENSE` 的逐字节副本（SHA-256 实测一致）；`NOTICE` 与 `THIRD_PARTY_NOTICES.md` 均为仓库内正式文件，随归档分发；包内 `SHA256SUMS` 覆盖二进制与四份文档。

## 3. 升级

- **这是第一个 Go 移植版本**，没有更早 Go 版本的迁移路径。
- 升级后续 Go 版本：用同一条 `install.sh` 覆盖安装即可；`HIVE_HOME` 下的会话/凭据格式未变。
- `HIVE_HOME` 默认仍是 `~/.hive`。**如果本机已有 Python 版数据，建议给 Go 版设置独立目录**（例如 `export HIVE_HOME="$HOME/.hive-go"`），避免两套实现互相覆盖；共用同一目录未经验证。
- 从源码构建：`cd go_hive/go-hive && make build && ./dist/hive --version`。首次需要 `go mod download` 预热 module cache；之后 `GOPROXY=off` 可离线构建。

## 4. 验证记录（2026-11-19，本工作树）

### 4.1 代码门禁

```bash
cd go_hive/go-hive
make ci
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test -tags e2e -count=1 ./e2e/...
```

| 项 | 结果 |
| --- | --- |
| `make ci` | 退出码 **0**；gofmt OK；vet 无输出 |
| 包测试 | 37 个包 `ok`，6 个包无测试文件；**4,158 条执行：4,154 pass / 4 skip / 0 fail** |
| 测试声明数 | **4,160**（parity 口径；含 `e2e` 构建标签下的 2 条） |
| E2E | `ok github.com/aden-hive/hive-go/e2e 3.247s` |
| `-race` | `go test -race -count=1 ./...` 退出码 **0**；37 包 `ok` |
| 六平台交叉编译 | 6/6 通过 |
| cgo | `import "C"` **0** 处 |
| `check_parity.sh` | **4160/4160** 有注解，0 missing |
| `check_parity_targets.sh` | **4,357** 条引用（3,527 test + 830 prod），0 missing files/tests/symbols |
| 会话目录计数 | 扫描器 + senders 打开时 `installed=103 registry=103`；默认 `96/96` |
| `scripts/smoke.sh`（源码构建） | 11/13 PASS；`/` 与 SPA 深链 404——普通构建不带 embed 标签，且 `go-hive/frontend/dist` 不存在（与「源码树不含 dist」一致） |
| `scripts/smoke.sh` 等价探针（发布包） | **13/13 PASS**：health、`/`、`/colonies/demo`、credentials/queen/prompts/mcp/models 均 200；创建会话 201；无 key chat 503；未知会话 404；workers 200；SSE 200 |

### 4.2 发布归档

```bash
bash go_hive/scripts/embed_ui.sh
bash go_hive/scripts/release.sh
cd go_hive/go-hive/dist/release && shasum -a 256 -c SHA256SUMS
```

`release.sh` 实测退出码 0，产出 6 个归档 + `SHA256SUMS`：

| 归档 | 字节 | 大小 | SHA-256 |
| --- | ---: | ---: | --- |
| `hive-1.0.14-go.1-darwin-amd64.tar.gz` | 13,546,678 | 12.9 MiB | `8a1b2ab5b2650835ca8634f46d5e11f45cdf82c031efc5649bf7742982bfd650` |
| `hive-1.0.14-go.1-darwin-arm64.tar.gz` | 12,854,878 | 12.3 MiB | `9c5c512531b37fa563606c826ec87fc139a74faa314a2e5f804073ecf2e8b51c` |
| `hive-1.0.14-go.1-linux-amd64.tar.gz` | 13,540,623 | 12.9 MiB | `21ea74dea5a28a6b5f2bf4c996ce123400f0aea7bf3984815e4e7b00700197d5` |
| `hive-1.0.14-go.1-linux-arm64.tar.gz` | 12,584,240 | 12.0 MiB | `9f1d5366a8349bfea75c96eb07ce0d89986d9bca98a9fdfe56a45d173541626f` |
| `hive-1.0.14-go.1-windows-amd64.zip` | 13,626,442 | 13.0 MiB | `e77140e3d1368328e1b508285de5dbe1eaa97412b004372525c212092f96390a` |
| `hive-1.0.14-go.1-windows-arm64.zip` | 12,577,680 | 12.0 MiB | `9789a875e33e9065995ef405ecc5439950542ebc3bf5593eb4f409ad86851acc` |

合计 78,730,541 字节（75.1 MiB）。`shasum -a 256 -c SHA256SUMS` 六个归档全部 `OK`。

### 4.3 发布包运行

解压 `hive-1.0.14-go.1-darwin-arm64.tar.gz` 到空目录，独立 `HIVE_HOME`，`--port 8797`：

```text
Hive API server running on http://127.0.0.1:8797
Dashboard:  http://127.0.0.1:8797
Health:     http://127.0.0.1:8797/api/health
```

| 请求 | 结果 |
| --- | --- |
| `hive --version` | `hive 1.0.14-go.1` |
| `GET /` | **200** `text/html; charset=utf-8`，`<title>Hive</title>` |
| `GET /sessions`（前端深链） | **200** `text/html; charset=utf-8` |
| `GET /favicon.png` | **200** `image/png`，448,975 B |
| `GET /api/health` | **200** |

`serve` 收到 SIGTERM 后打印 `Received SIGTERM, shutting down gracefully…` 并退出。

## 5. 已知限制

1. **2 个 DuckDB SQL action 有意排除**：`csv_sql`、`excel_sql`。需要 SQL over CSV/Excel 时请在上游 Python 版或外部引擎完成。
2. **`attach_file` 多模态只到工具结果层**：attachment chip、文本预览（内联上限 8 KiB）可用；图片生成 `ImageContent` 块，但 `serve` 的 chat runtime 只把文本传给模型，PDF 没有 `file` 块。结果 JSON 的 `inline` 字段标注 `text_preview` / `image_block` / `none`，不声称模型看到了字节。
3. **6 个网络扫描器默认关闭**，需 `HIVE_NETWORK_SCANNERS=1` 或 `hive.config.json: network_scanners`；`senders` 需 `HIVE_EMAIL_SENDERS=1`，默认都不在会话目录里。
4. **未做在线 CVE 扫描**：Makefile 与 `.github/workflows/` 中没有 `govulncheck`、OSV、nancy 或等价步骤。可以确认的是依赖锁定、无 cgo、无运行时下载；依赖漏洞状态本身未知。
5. **测试只在 darwin/arm64 本机跑过**；其余五个目标只做了交叉编译。Windows 的原子替换、目录 fsync、`SIGTERM`、PTY 行为未验证。
6. **无逐 token 模型流**：SSE 只有会话事件（如 `client_input_received`），20 秒心跳。
7. **Colony**：`serve --colony` 能加载并注册会话，多 worker 编排闭环未用真实模型跑过。
8. **Queen mail**：路由已挂载，`serve` 未注入后端（预期 401）；**Claude Code queens** 运行时未接入（列表为空）。
9. **Browser 工具 / `hive-browser`**：未移植。
10. **Sentinel**：引擎单测通过，未接真实 Telegram/Slack。
11. **源码树不含 `frontend/dist`**：`make build` 产出的是纯 API 二进制；要 dashboard 必须 `make embed-ui` 或使用本发布包。
12. **在线集成调用未验证**：863 个 action 只用 `httptest`/假上游验证过请求形状，没有真实第三方凭据的端到端记录（按用户决定，本阶段不验证，见 §6）。
13. **共用 `HIVE_HOME`**：未验证与 Python 版共用同一目录，建议分开。

## 6. R10（真实集成验证）状态

**不适用（用户决定 2026-11-19）：本阶段只提供 LLM 凭据，不提供第三方工具凭据。** 因此没有任何第三方服务的真实调用记录；发布包按支持矩阵把这一点标注为「未验证」，不以框架测试代替真实集成证据。无需凭据的模块（终端、文件、csv/excel/pdf、arxiv、wikipedia、openmeteo 等）可真实验证。若后续提供凭据，最小验证路径是：

```bash
export HIVE_HOME="$HOME/.hive-r10"
export OPENAI_API_KEY=...            # 任意一个真实模型即可
hive serve --port 8787
# 另开终端
curl -sS -X POST http://127.0.0.1:8787/api/sessions -d '{}' -H 'Content-Type: application/json'
curl -sS -X POST http://127.0.0.1:8787/api/sessions/<id>/chat -d '{"message":"列出当前时间"}' -H 'Content-Type: application/json'
```

## 7. 相关文档

- `quickstart.md`：从构建到第一次对话的分步流程（每条命令都实跑过）。
- `configuration.md`：`HIVE_HOME` 布局、模型/凭据解析优先级、colony 配置。
- `support-matrix.md`：逐项能力状态、集成模块/action 口径、未验证清单。
- `CHANGELOG.md`：本版本完整变更。
- `../RELEASE_CHECKLIST.md`：R1–R10 出口标准与证据命令。
