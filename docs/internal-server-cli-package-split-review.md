# internal/server 与 internal/cli 子包化整理评估

> **实施状态（2026-11-28）**：Phase 1 已落地并通过 full CI：`internal/server/routeutil` + `internal/server/routesbrowser`、`internal/cli/cliguard`、`internal/cli/cliargs`。Phase 2 追加 `internal/server/routesapp`；后续追加 `internal/cli/browsersupervisor`、`internal/server/routesagents`、`internal/cli/browsercli`、`internal/cli/toolcatalog`。`internal/tools/integrations` 已抽出 `image`、`scrape` 两个子包。`colonyfork` 以及 server 更多叶子路由、integrations 剩余根包收敛仍未实施；本文以下保留原始评估，作为后续拆分的设计依据。

> 结论文档。**本文最初生成时未改动任何代码。** 所有判定附 `file:line` / 实测数字。
> 日期：2026-11-27 · 基线：go_hive/go-hive HEAD
> 目标：回答“这两个包能否整理、分类成子包”，并给出可执行的分阶段方案与门禁清单。

---

## 0. TL;DR

| 包 | 现状 | 能否子包化 | 推荐策略 |
|---|---|---|---|
| `internal/server` | 326 个 `.go`，**142 非测试**，约 **47,486 行**；包内平铺，只有 `testdata/`、`uiembed/` 两个子目录 | **能**，但只适合 leaf-first | 先抽 `browser panel` → `app extra` → `agents`；保留 `app.go`/`session.go`/`chat_runtime.go` 作为组合根 |
| `internal/cli` | 154 个 `.go`，**68 非测试**，约 **18,025 行**；**没有子目录**；86 个测试文件全是 `package cli` | **能**，且比 server 更适合先动 | 先抽 `cliguard` → `cliargs` → `browsersupervisor` → `browsercli` → `toolcatalog` → `colonyfork`；保留 `cli.go`/`loader*.go`/`skill.go` |

一句话：**两个包都能拆，但不能按“文件数量”平均切；要按依赖缝拆，先拆叶子路由/命令族，保留组合根，子包禁止反向 import 父包。**

---

## 1. 现状测量

### 1.1 internal/server

```text
总 .go 文件        326
非测试 .go         142
非测试 LOC        47,486
测试文件           184
子目录             testdata/、uiembed/（均非业务子包）
```

最大的非测试文件：

| 文件 | 行数 |
|---|---:|
| `routes_skills.go` | 1,462 |
| `chat_runtime.go` | 1,385 |
| `routes_colonies_import.go` | 1,353 |
| `routes_queens.go` | 1,121 |
| `routes_models.go` | 972 |
| `routes_colony_workers_data.go` | 924 |
| `app.go` | 869 |
| `trigger_runtime.go` | 844 |
| `llm_config.go` | 805 |
| `session.go` | 756 |

### 1.2 internal/cli

```text
总 .go 文件        154
非测试 .go          68
非测试 LOC        18,025
测试文件            86（全部 package cli）
子目录             无
```

最大的非测试文件：

| 文件 | 行数 |
|---|---:|
| `loader.go` | 1,766 |
| `skill.go` | 1,601 |
| `browser_cli_commands.go` | 894 |
| `args.go` | 807 |
| `browser_supervisor.go` | 682 |
| `session_tools.go` | 654 |
| `colony_pivot_lean.go` | 636 |
| `cli.go` | 589 |
| `tool_surface_flat.go` | 579 |
| `browser_control.go` | 522 |
| `browser_cli.go` | 520 |

---

## 2. 拆分原则

1. **Go 的包边界 = API 边界**：子包只能看到导出符号，且不能反向 import 父包。
2. **禁止循环**：`internal/cli` 可以 import 子包；子包**不能** import `internal/cli`。需要父包能力时用 `Deps` struct / 回调 / 窄接口注入。
3. **leaf-first**：先拆只依赖标准库或独立包（`hivehome`、`agents`、`resources`）的叶子，再拆依赖 `Session`/`SessionManager` 的核心。
4. **组合根先不动**：`app.go`、`session.go`、`chat_runtime.go`、`cli.go`、`loader*.go` 是装配与共享状态，先留在大包。
5. **行为冻结**：这是结构重构，不是 parity 变更；路由集合、响应形状、CLI 外观、命令行为必须逐字节/逐字段不变。

---

## 3. internal/server：三个文件的抽取评估

这三个文件已经具备 `RegisterXxxRoutes(mux, Deps)` 形态，接线在 `app.go`：

| 文件 | 行数 | Deps | 注册点 | 难度 |
|---|---:|---|---|---|
| `routes_browser_panel.go` | 340 | `BrowserPanelDeps{BaseURL func, Client *http.Client}` | `app.go:301` | **低（首选）** |
| `routes_app_extra.go` | 659 | `AppExtraRouteDeps{Resources, StatusAddr, StatusAddrs, ProbeStatus, ProbeContext, RevealTab, PollInterval, KeepaliveTicks}` | `app.go:300` | **中** |
| `routes_agents.go` | 331 | `AgentRouteDeps{Manager *SessionManager, Home, Roots, EnsureCredentialEnv}` | `app.go:257` | **较高** |

### 3.1 `routes_browser_panel.go`（低）

- 只做 HTTP 代理：`GET /api/browser/contexts`、`GET /api/browser/tabs`、`POST /api/browser/contexts/close`。
- 依赖只有 `BaseURL` 和 `Client`，另一个是 `writeJSON`。
- 没有 `SessionManager`/`Session`。
- **结论：零争议的第一块。**

### 3.2 `routes_app_extra.go`（中）

- 路由：`/api/health/resources`、`/api/browser/status`、`/api/browser/status/stream`、`/api/browser/tab/reveal`。
- Deps 全是函数 seam；本地 helper（`pyTruthy`、`browserStatusPayload`、`activeTabForProfile`、`probeBridgeStatus` 等）可以随文件搬走。
- 共享 helper：`writeJSON`、`decodeJSONValue`、`pyTruthy`。
- **结论：需要先有 `routeutil`（或把少量 helper 导出），然后可直接抽。**

### 3.3 `routes_agents.go`（较高）

- 路由：`GET /api/discover`、`DELETE /api/agents`、`PATCH /api/agents/metadata`、`POST /api/credentials/check-agent`。
- 依赖：
  - `deps.Manager.ListSessions()`，只用了 `session.WorkerPath`；
  - `deps.Manager.StopSession(session.ID)`；
  - `deps.checkAgentCredentialRequirements(...)` —— 该方法定义在 `routes_credentials_check.go:62`，不是本文件；
  - `repositoryRoot()`、`marshalPythonIndent`、`escapeNonASCIIBytes` 是本地 helper。
- **抽取方式**：不要搬 `SessionManager`/`Session`，改为回调：

```go
type SessionRef struct {
    ID         string
    WorkerPath string
}
type Deps struct {
    Home       hivehome.Home
    Roots      *agents.AllowedRoots
    Sessions   func() []SessionRef
    StopSession func(id string)
    CheckAgentCredentials func(path string, verify bool) (map[string]any, error)
}
```

### 3.4 共享 helper 的归属

| helper | 当前定义 |
|---|---|
| `writeJSON` | `routes_config.go:25` |
| `writeJSONBodyFailure` | `http_limits.go:27` |
| `decodeJSONValue` | `routes_session_history.go:24` |
| `pyTruthy` | `hive_config.go:34` |
| `internalServerError` | `config_features.go:17` |

建议新建 `internal/server/routeutil`（或 `internal/httpjson`），导出：

```go
WriteJSON / WriteBodyFailure / DecodeJSONValue / PyTruthy / InternalServerError
```

`internal/server` 内部保留薄别名，避免一次性改所有调用点。子包只 import `routeutil`，不 import `server`。

### 3.5 下一批候选（按依赖缝）

适合继续抽的叶子路由族：

- `routes_models.go`
- `routes_tasks.go`
- `routes_prompts.go`
- `routes_channels.go`
- `routes_runtime_logs.go`
- `routes_soul*.go`
- `routes_mcp.go`（注意 MCP 注册表依赖）

**暂不拆**：`app.go`、`session.go`、`chat_runtime.go`、`routes_colonies_import.go`、`routes_colonies.go`、`routes_queens.go`、`routes_skills.go`。它们是组合根、共享状态或高频交叉依赖。

### 3.6 目标结构

```text
internal/server/                 # 组合根：AppDeps、mux 装配、live route tables
internal/server/routeutil/       # HTTP/JSON 共享 helper
internal/server/routesbrowser/   # browser panel 三条路由
internal/server/routesapp/       # health/resources/browser status/reveal
internal/server/routesagents/    # discover/delete/update/check-agent
```

---

## 4. internal/cli：现状与阻塞

### 4.1 最大阻塞

| 阻塞 | 说明 |
|---|---|
| `args.go`（807 行） | `commandSpec`、`argSpec`、`positionalSpec`、`parsedArgs`、`parseCommand`、`usageErrorBlock` 全部未导出；`browser_cli*.go` 和所有 command spec 都依赖它 |
| `Runner` 方法 | `loader*.go`、`skill.go` 大量 `func (r *Runner)`；先留在组合根 |
| 测试同包 | 86 个 `_test.go` 全是 `package cli`，访问未导出符号；搬文件要连测试一起搬 |
| 路径型基线 | `quality/size_test.go` 冻结 `loader.go` 1936 行、`skill.go` 1601 行；`gosec_baseline.json` 按路径记 G204/G301/G304；`deadcode_baseline.json` 按“文件+符号” |

### 4.2 候选分组

| 目标子包 | 文件 | 规模 | 依赖 | 难度 |
|---|---|---|---:|---|---|
| `internal/cli/cliguard` | `crash.go` | 小 | `tracker` | **低** |
| `internal/cli/browsersupervisor` | `browser_supervisor.go` | 682 | `hivehome`、`hiveserver`（仅 viewport 常量/函数） | **低** |
| `internal/cli/browsercli` | `browser_cli.go`、`browser_cli_commands.go`、`browser_cli_client.go`、`browser_control.go` | ~2.8k | 依赖 `args.go` 的解析器 | **中（先抽 cliargs）** |
| `internal/cli/toolcatalog` | `session_tools.go`、`session_tools_search.go`、`task_tools.go`、`queen_tool_library.go`、`tool_surface_flat.go` | ~2.4k | `server`/`colony`/`tools`/`tasks`，**无 Runner 方法** | **中** |
| `internal/cli/colonyfork` | `colony_pivot_lean.go`、`colony_fork_seed.go`、`colony_worker_definition.go`、`loader_colony_fork.go` | ~1.7k | `hiveserver`/`colony`/`tracker`，**无 Runner 方法** | **中** |
| 暂不拆 | `cli.go`、`loader*.go`、`skill.go`、`args.go` | 组合根/框架 | — | — |

### 4.3 启用性重构：抽 `cliargs`

`browser_cli.go` 直接使用 `commandSpec`、`argSpec`、`positionalSpec`、`parsedArgs`、`parseCommand`、`usageErrorBlock`、`helpRequest`、`usageError`。

建议：

1. 新建 `internal/cli/cliargs`；
2. 搬 `args.go` 的类型/函数 + 对应测试；
3. 在 `internal/cli` 保留薄别名（`type commandSpec = cliargs.CommandSpec` 等），避免一次性改所有 command spec；
4. 保持 `internal/arghelp` 的 help 版式不变（C11 CLI 外观门禁会抓漂移）。

### 4.4 目标结构

```text
internal/cli/                  # Runner、命令分发、loader*、serve 装配
internal/cli/cliargs/          # commandSpec / parse / help
internal/cli/cliguard/         # GuardMain / crash 形状
internal/cli/browsersupervisor/
internal/cli/browsercli/
internal/cli/toolcatalog/
internal/cli/colonyfork/
```

**硬规则**：子包不能 import `internal/cli`；需要的钩子用 `Deps`/回调注入。`hiveserver`、`colony`、`hivehome` 等独立包可以直接 import。

---

## 5. 分阶段计划

### Phase 1（server + cli 可并行）

- server：`routeutil` + `routesbrowser`。
- cli：`cliguard`。
- 验证：`go build ./...`、`go test ./internal/server/... ./internal/cli/...`、`make compat-check`。

### Phase 2

- server：`routesapp`。
- cli：`cliargs`（`args.go` + 测试搬迁 + 别名）。
- 验证：C11 CLI 外观；`make parity parity-targets`。

### Phase 3

- server：`routesagents`（回调化 `SessionManager`/`checkAgentCredentialRequirements`）。
- cli：`browsersupervisor`。
- 验证：`make arch-check`、`make deadcode-check`。

### Phase 4

- cli：`browsercli`（依赖 `cliargs`）。
- server：继续抽 `models`/`tasks`/`prompts`/`channels` 等叶子。
- 验证：`make compat-check`、CLI surface tests。

### Phase 5

- cli：`toolcatalog`、`colonyfork`。
- 最后再考虑 `skill.go`（Runner 方法）与 `loader*.go`（先更新 `quality/size_test.go` 的冻结上限，只允许下调）。

**每个 phase 保持一个可回滚的提交；不要在一个提交里跨 server + cli + loader。**

---

## 6. 门禁与基线清单

| 门禁/基线 | 影响 | 命令 |
|---|---|---|
| API 路由集合 | 抽走后注册点变化 | `make compat-check`（C6） |
| API 响应形状 | handler 搬家 | C7/C9 |
| CLI 外观 | `cliargs`/命令族搬家 | C11；`make cli-surface-check` |
| 架构文档 | `go list` 路径变化 | `make arch-check`（重新生成 `architecture/model.yaml` 产物） |
| parity 注解 | 测试随文件搬 | `make parity`、`make parity-targets` |
| 接线点 | loader/serve 装配路径 | `make wiring-check`；必要时更新 `scripts/wiring_check.py` path |
| 死代码基线 | 文件+符号键 | `make deadcode-check`；必要时 `deadcode-update` |
| gosec 基线 | 按文件路径记 G204/G301/G304 | 更新 `internal/quality/gosec_baseline.json` |
| 尺寸冻结节 | `loader.go` 1936、`skill.go` 1601 | `internal/quality/size_test.go:48`，只允许下调 |
| 模块清单 anchor | Python→Go 文件映射 | `contracts/module-inventory.json`；C12 |
| 契约冻结 | 若动 contracts 文件 | `make refactor-freeze` |
| 构建/测试 | 全量 | `go build ./...`、`go test ./...`、`make ci` |

---

## 7. 风险与“不要先做”

1. **不要子包 import 父包**：必成循环；用 `Deps`/回调。
2. **不要先搬 `Session`/`SessionManager`**：会把核心类型变成跨包 god type，波及 184 个 server 测试。
3. **不要复制 `args.go`**：两份 parser 会让 C11 CLI 外观漂移；必须抽共享包 + 别名。
4. **不要一次性把 86 个 `package cli` 测试改成外部包**：先随实现文件搬迁，共享夹具放 `clitest`。
5. **不要跨 phase 混合重构**：server 路由、cli 命令、loader 装配分别提交。
6. **不要只看编译通过**：抽完要跑 API/CLI 契约测试，否则“编译通过 + 单测通过”仍可能静默退化（参见 `scripts/wiring_check.py` 的存在理由）。
7. **不要为拆分而改行为**：这是结构重构，不是 parity 变更；任何响应/文案变化都应视为 bug。

---

## 8. 验证命令（每 phase）

```bash
cd go_hive/go-hive

go build ./...
go test ./internal/server/... ./internal/cli/...

make parity parity-targets wiring-check
make compat-check arch-check
make deadcode-check gosec-check
make ci
```

针对性测试：

```bash
go test ./internal/server -run 'BrowserPanel|AppExtra|AgentRoutes' -count=1
go test ./internal/cli -run 'BrowserCLI|CommandSpec|Parse' -count=1
```

---

## 9. 证据索引

| 位置 | 说明 |
|---|---|
| `internal/server/app.go:257,300,301` | 三个路由族的注册点 |
| `internal/server/routes_browser_panel.go:49-54,85` | `BrowserPanelDeps` / `RegisterBrowserPanelRoutes` |
| `internal/server/routes_app_extra.go:55-75,145` | `AppExtraRouteDeps` / `RegisterAppExtraRoutes` |
| `internal/server/routes_agents.go:18-31,54,301` | `AgentRouteDeps` / 注册 / 本地 helper |
| `internal/server/routes_credentials_check.go:62` | `checkAgentCredentialRequirements` 定义在别的文件 |
| `internal/server/routes_config.go:25` | `writeJSON` |
| `internal/server/http_limits.go:27` | `writeJSONBodyFailure` |
| `internal/server/routes_session_history.go:24` | `decodeJSONValue` |
| `internal/server/hive_config.go:34` | `pyTruthy` |
| `internal/server/config_features.go:17` | `internalServerError` |
| `internal/cli/args.go:23,48,60,87,187,217` | parser 类型与函数 |
| `internal/cli/browser_cli.go:57,75,104,170` | browser CLI 对 parser 的依赖 |
| `internal/cli/browser_supervisor.go:16-33` | supervisor 依赖 `hivehome`/`hiveserver` |
| `internal/cli/skill.go:72,282` | Runner 方法（暂不拆） |
| `internal/quality/size_test.go:46,48` | `skill.go`/`loader.go` 尺寸冻结 |
| `internal/quality/gosec_baseline.json` | 按路径记录的 G204/G301/G304 |
| `internal/quality/deadcode_baseline.json` | 按文件+符号记录的不可达基线 |
| `scripts/wiring_check.py` | 关键装配点的路径检查 |
| `contracts/module-inventory.json` | Python→Go 模块/文件 anchor |

---

## 10. 结论

- **internal/server 能拆**：`browser panel` 是零争议第一块，`app extra` 次之，`agents` 需要先回调化 `SessionManager` 与 credentials 检查。整体 142 文件/47k 行，不适合一次性拆。
- **internal/cli 能拆，且更适合先动**：`cliguard` → `cliargs` → `browsersupervisor` → `browsercli` → `toolcatalog` → `colonyfork`。真正的门槛是 parser 抽包 + 86 个同包测试迁移 + 路径型基线同步。
- **两个包都保留组合根**：server 的 `app.go/session.go/chat_runtime.go`，cli 的 `cli.go/loader*.go/skill.go`。
- **验收标准不是“目录更漂亮”**，而是：路由集合、响应形状、CLI 外观、命令行为全部不变，且所有门禁/基线同步通过。满足这些，拆分才是可回滚的整理，而不是新的不一致来源。
