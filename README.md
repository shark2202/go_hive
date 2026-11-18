# Go Hive · OpenHive 的纯 Go 重写

> Go 模块：`github.com/aden-hive/hive-go`
> 上游参考：`aden-hive` / `aden-tools` 1.0.14（提交 `c6c2cdcf`），Python 代码在 `/Users/mac/hive/core`
> 状态快照：2026-11-18（P0 可运行切片集成后；`make ci` 退出码 0）
> 文档语言：简体中文

## 1. 这是什么

这是 OpenHive 的 Go 移植。目标不是「用 Go 写一个类似的东西」，而是：

1. **功能对等**：技能、记忆、会话、凭据、Tracker、AgentLoop、Colony、触发器、事件总线、工具、服务端、CLI 逐项覆盖；
2. **质量对等**：每条上游行为都有对应 Go 测试，并用 `// Parity:` 注解建立可审计映射；
3. **平台优势**：`CGO_ENABLED=0` 下 darwin / linux / windows × amd64 / arm64 六个目标可编译，离线可复现。

三项冲突时的优先级是 **功能对等 > 质量对等 > 平台优势**。完整判定标准见 [00-product-goals.md](00-product-goals.md)。

## 2. 技术栈与硬约束

| 项 | 选择 |
| --- | --- |
| Go | 1.26.5 |
| cgo | 全仓库禁用（`CGO_ENABLED=0`），不依赖系统 SQLite / OpenSSL |
| SQLite | `modernc.org/sqlite v1.60.1`（纯 Go 转译，驱动名 `sqlite`） |
| YAML | `gopkg.in/yaml.v3 v3.0.1`（`SKILL.md` frontmatter） |
| 动态库 | `github.com/ebitengine/purego v0.11.3` 已声明但**当前零 import**，仅按需预留 |
| TTY 探测 | `github.com/mattn/go-isatty v0.0.24`（对齐 Python `sys.stdin.isatty()` 的 ioctl 语义） |
| 离线构建 | `GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod` |
| 新增依赖 | 需要显式批准；不得引入 cgo 或运行时下载 |

## 3. 目录结构

```
go_hive/
├── README.md                  # 本文
├── STATUS.md                  # 实时进度看板（比本文更新更快）
├── 00-product-goals.md        # 产品目标与验收标准
├── 11-product-requirements.md # 功能需求清单（按当前代码推导状态）
├── 12-design.md               # 模块边界、关键决策、数据流
├── 13-cross-platform-and-purego.md
├── 14-quality-assurance.md
├── 15-test-parity-matrix.md   # 测试对等矩阵（快照）
├── scripts/
│   ├── check_parity.sh        # parity 注解门禁
│   ├── checkpoint.sh          # 检查点台账（路由/测试/工具/parity 计数）
│   ├── pace.py                # 步调核算（lane-hour 产能、ETA、EVM）
│   └── watchdog.sh            # 进度巡检
├── .github/workflows/go.yml   # CI（评审后并入根 .github）
└── go-hive/
    ├── go.mod
    ├── Makefile               # test / vet / fmt-check / build / cross / parity / race / ci
    ├── cmd/hive/              # CLI 入口（`hive` 根命令与子命令）
    ├── internal/              # 全部实现包
    └── parity/fixtures/       # 预留的跨语言固定装置
```

`01`–`10` 号文档及两个 HTML 是对 **Python 上游**的分析，保留作移植参考，不代表 Go 实现进度。

## 4. 快速开始

```bash
cd go_hive/go-hive

# 首次需要联网预热 module cache
go mod download

# 离线测试（Makefile 已内置离线环境变量）
make test

# 完整门禁：fmt-check + vet + test + cross + parity
make ci
```

等价的手工命令：

```bash
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test ./...
```

### Make 目标

| 目标 | 作用 |
| --- | --- |
| `make test` | 离线跑全部测试 |
| `make test-v` | 同上，`-v` 输出 |
| `make vet` | `go vet ./...` |
| `make fmt-check` | `gofmt -l .`，有输出即失败 |
| `make build` | `go build ./...`；有 main 包时产出 `dist/hive` |
| `make cross` | 六平台交叉编译（无 main 包时只验证可编译） |
| `make race` | `go test -race ./...`（较慢，未纳入 CI 必须项） |
| `make parity` | 调用 `scripts/check_parity.sh` |
| `make ci` | 依次执行 fmt-check、vet、test、cross、parity |

### P0 可运行范围（2026-11-18）

P0 是**可运行切片**，不是 Python 全量对等。以下能力已在当前代码中接通（生产路径为 `defaultServe` → `internal/cli/loader.go` 的 `createChatSession`；末行 E2E 为验证入口）：

| 能力 | 现状 |
| --- | --- |
| `hive serve` | 默认 `127.0.0.1:8787`，支持 `--host/--port/--colony/--model/--open/--verbose/--debug`；启动后 `GET /api/health` 可用 |
| 会话创建 | `POST /api/sessions` → `201`，响应含 `session_id`；请求键含 `model`、`initial_prompt`、`colony_id`、`session_id`、`queen_name`、`queen_resume_from`、`initial_phase` |
| 对话投递 | `POST /api/sessions/{id}/chat`，体 `{"message":"..."}` → `200 {"status":"queen","delivered":true}`；空消息 `400`、未知会话 `404`、未配置 LLM `503`、已 spawn colony `409` |
| 会话事件流 | `GET /api/sessions/{id}/events`：订阅会话事件总线的 SSE，20 秒心跳，支持 `?types=a,b` 过滤；当前只推送会话总线事件（如 `CLIENT_INPUT_RECEIVED`），**不**推送逐 token 模型流 |
| 真实 HTTP LLM | `llm.NewHTTPProvider` + `Provider.AComplete`：OpenAI-compatible `POST {api_base}/chat/completions`（基址缺 `/v1` 时自动补，默认 `https://api.openai.com/v1`，Bearer）；Anthropic Messages `POST {api_base}/v1/messages`（默认 `https://api.anthropic.com`，`x-api-key` + `anthropic-version: 2023-06-01`）。协议按模型族选择：`anthropic/*`、`claude-*`、`kimi/*`、`hive/*` 用 Anthropic，`openrouter/*` 保持 OpenAI |
| 内建工具 | `read_file`、`write_file`、`terminal_exec`，经 agent loop 真实执行；沙箱根为服务进程 cwd，拒绝越界路径与符号链接逃逸；`terminal_exec` 用 `/bin/bash -lc`，默认 60 秒、上限 600 秒、每路输出 256 KiB |
| 对话持久化 | `<HIVE_HOME>/queens/default/sessions/<session_id>/conversations/parts/*.json`（`agentloop.FileConversationStore`）；以同一 `session_id` 重建会话时从该目录恢复 |
| CLI | `hive chat <session_id> <message> [--server URL]`；`hive session list/stop` |
| E2E | `go test -tags e2e ./e2e/...`：create → chat → 持久化 transcript → 二次 chat；使用 `httptest` 与脚本化 LLM，不访问网络 |

**凭据前置条件**（`resolveChatProvider`：请求 `model` > `serve --model` > `configuration.json` 的 `llm.model` > `gpt-4o-mini`；`llm.api_key_env_var` 可覆盖 key 变量名，`HIVE_API_KEY` 兜底）：

| 模型 | 需要 |
| --- | --- |
| Anthropic / Claude 协议（`anthropic/*`、`claude-*`、`kimi/*`、`hive/*`） | `ANTHROPIC_API_KEY`；可选 `ANTHROPIC_API_BASE` 或 `ANTHROPIC_BASE_URL` |
| OpenAI-compatible（含默认 `gpt-4o-mini`） | `OPENAI_API_KEY`；可选 `OPENAI_BASE_URL` 或 `OPENAI_API_BASE` |
| 本地模型（`ollama/*`、`vllm/*`、`lm_studio/*`、`llamacpp/*`） | 无需 key；`OLLAMA_HOST` 可覆盖端点 |

缺 key 时 `POST /api/sessions` 仍返回可列出的会话，但 `POST /api/sessions/{id}/chat` 返回 `503 {"error":"No LLM configured for this session"}`，不伪造投递成功。

**明确延期（P0 不含）**：216 条 Python 路由的完整对等、集成工具（`tools/src/aden_tools` 104 模块 / 324 文件 / 811 action，本期只做优先子集）、Sentinel、browser 工具、React dashboard（只有存在 `frontend/dist` 时才提供静态资源，仓库未附带该构建产物）、queen/colony 多 worker 编排闭环。

**门禁状态（P0 集成后实测，PASS）**：`make ci` 退出码 0——`fmt-check`、`vet`、`test`、`cross`（darwin/linux/windows × amd64/arm64）与 `parity` 全部通过；parity **2,238/2,238 已注解、0 missing**，`check_parity_targets` **2,045 refs、0 missing files/tests、55 shorthand/prose**。`go test -tags e2e -count=1 ./e2e/...` 与其 `-race` 版本均 ok；凭据门禁用真实二进制 + 假上游验证 `Authorization: Bearer test-key`；`dist/hive-darwin-arm64 --version` 输出 `hive 1.0.14-go.1`，健康检查/启动就绪、创建会话 201、无 key 时 chat 返回 503。

## 5. 当前实现状态

以本工作区实际代码为准（2026-11-18 19:14）：

| 包 | 状态 | 说明 |
| --- | --- | --- |
| `internal/atomicfs` | 已完成 | 原子写、JSON/JSONL、复制 |
| `internal/hivehome` | 已完成 | `HIVE_HOME` 与 `~/.hive` 布局 |
| `internal/session` | 已完成 | `state.json` 1.1、parts、cursor、partials、checkpoints、冷会话扫描 |
| `internal/storage` | 已完成 | 会话/对话/检查点持久化、v2→v3 迁移 |
| `internal/credentials` | 已完成 | Fernet、四种存储、模板、provider、store、引导、OAuth2、shell |
| `internal/skills` | 已完成 | 发现、解析、信任、覆写、热重载、写盘、catalog、registry |
| `internal/memory` | 已完成 | 扫描、召回、反思、时间线 |
| `internal/tracker` | 已完成 | SQLite v3、SQL 门禁、绑定、四个工具 |
| `internal/llm` | 已完成 | 能力目录、端点、密钥池、流事件、provider 纯逻辑层与请求塑形；P0 新增真实 HTTP 传输 `transport.go`（OpenAI-compatible / Anthropic Messages + `AComplete`） |
| `internal/version` | 已完成 | 版本常量 |
| `internal/agentloop` | 已完成 | 核心循环、压缩、停顿/恢复、提醒 hub、运行预算与超时 |
| `internal/agents` | 已完成 | colony 发现分组、元数据与软删除、会话/运行计数、`last_active`、Python `str.title()`/`pathlib` suffix 语义、允许根路径校验 |
| `internal/colony` | 进行中 | 运行时组件已落地，编排未闭环 |
| `internal/orchestrator`、`internal/playbook` | 已完成 | 数据缓冲、输出校验、行提取 |
| `internal/ratelimit`、`internal/triggers`、`internal/eventbus` | 已完成 | SQLite 限流、触发器、事件总线 |
| `internal/tasks`、`internal/mcpregistry` | 已完成 | 任务/提醒、MCP 安装/解析/健康检查 |
| `internal/tools` | 进行中 | 注册表/执行器/MCP 客户端与连接池；`internal/tools/builtin` 提供 P0 的 `read_file`/`write_file`/`terminal_exec`；集成工具（104 模块 / 811 action）本期交付框架与优先子集，其余登记缺口 |
| `internal/maintenance` | 已完成 | 三档 janitor + `hive janitor run/status` CLI 入口 |
| `internal/sessionreport` | 已完成 | 凭据脱敏 tar.gz、`user_report.json` 标记、SHA-256 与 manifest |
| `internal/server` | 进行中 | P0 会话切片：`POST /api/sessions/{id}/chat`、`GET /api/sessions/{id}/events`（会话事件 SSE）、`ChatRuntime`（agent loop + 持久化 transcript）；另有 `GET /api/discover`、`DELETE /api/agents`、`PATCH /api/agents/metadata`、`POST /api/credentials/check-agent`、`POST /api/sessions`（普通 + fork-into-new-colony）、`GET /api/sessions/{id}/live_tools`、历史 worker、配置/LLM/元信息路由、`GET /api/sessions` 实时会话列表、`GET/DELETE /api/sessions/{id}`（live/loading/cold/404 四分支 + `entry_points`）、`GET /api/sessions/{id}/stats`、`GET /api/sessions/{id}/triggers`、`GET /api/sessions/{id}/events/history` 游标分页、`POST /api/sessions/{id}/report-bundle`（凭据脱敏诊断包） |
| `internal/queen`、`internal/sentinel` | 进行中 | 13 个默认 queen 档案 + catalog/列表辅助 + 10 条对等测试；sentinel 待开始 |
| `internal/platform` | 已完成 | `OpenPath` 外部打开（darwin `open` / windows `explorer` / 其他 `xdg-open`）；`term` 目录预留 |
| `cmd/hive`、`internal/cli`、`internal/llmjudge` | 已完成 | 根入口 + 12 个 `skill` 子命令 + `debugger` + `mcp` 14 个子命令 + `janitor` 2 个子命令 + LLMJudge；`serve`/`chat` 已接到真实 provider、内建工具与持久化 transcript |
| `cmd/hive-cred` | 已完成 | 独立凭据入口 `token`/`browse`/`inspect`，OAuth 先刷新再打印 |

P0 集成后（2026-11-18）整树 `make ci` 实测退出码 0：`fmt-check`、`vet`、`test`、六平台 `cross`、`parity` 与 `parity-targets` 全部通过；**2,238/2,238** 已注解、**2,045** 条引用、55 条 shorthand/prose、0 missing（P0 集成前为 2,215/2,035）。新增 `internal/llm/transport.go`、`internal/tools/builtin`、`internal/server/chat_*`、`e2e/p0_chat_test.go` 与 `internal/cli` 装配均包含在本次门禁内；E2E 以 `-tags e2e` 与 `-race` 复跑均通过。

缺口与优先级见 [14-quality-assurance.md](14-quality-assurance.md) 与 [15-test-parity-matrix.md](15-test-parity-matrix.md)。

## 6. 文档索引

| 文件 | 内容 |
| --- | --- |
| [00-product-goals.md](00-product-goals.md) | 什么叫「移植完成」、三层验收标准、非目标 |
| [11-product-requirements.md](11-product-requirements.md) | 子系统需求 ID（R-SK/R-ME/R-SE/R-CR/R-TR/R-X）与状态 |
| [12-design.md](12-design.md) | 分层、模块边界、Go 化决策、单轮数据流、触发器计算 |
| [13-cross-platform-and-purego.md](13-cross-platform-and-purego.md) | 无 cgo 设计、六平台矩阵、平台差异清单 |
| [14-quality-assurance.md](14-quality-assurance.md) | parity 契约、离线构建、静态检查、CI、缺口 |
| [15-test-parity-matrix.md](15-test-parity-matrix.md) | Go 测试文件 ↔ Python 测试的逐包矩阵 |
| [STATUS.md](STATUS.md) | 进度看板与并行团队说明 |

上游分析（Python，仅作参考）：[01](01-project-overview.md)、[02](02-architecture.md)、[03](03-agent-loop.md)、[04](04-colony-runtime.md)、[05](05-tools-and-integrations.md)、[06](06-server-and-frontend.md)、[07](07-memory-and-skills.md)、[08](08-storage-and-data.md)、[09](09-engineering.md)、[10](10-assessment.md)，以及单页 [hive-features.html](hive-features.html)。

## 7. 贡献约定

1. **对等优先于惯用法**：落盘格式、错误分支、提示词文本与上游一致；Go 内部结构可以更严格。
2. **每个测试必须声明来源**：`// Parity: core/...py::test_name`，上游没有对应物时用 `// Parity-exempt: 理由`。
3. **不要为了变绿而放宽门禁**：删除断言、跳过测试、放宽 parity 检查都不被接受。
4. **离线与无 cgo 是硬约束**：任何引入 cgo 或运行时下载的方案直接否决。
5. **平台分支集中管理**：业务包不得直接写 `runtime.GOOS`；`internal/platform` 落地后统一收口。

详细评审清单见 [14-quality-assurance.md](14-quality-assurance.md) 第 10 节。
