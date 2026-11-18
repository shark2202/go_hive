# 蜂群协作章程（RASCI + SOP + PDCA）

> 维护者：协调者（Coordinator）。lane 只读本章程，不得改写。
> 基线提交：`8a1d2646`。所有 lane worktree 由该提交派生。

## 1. 目标（Definition of Done）

用纯 Go、无 cgo 完整复刻 `core/framework`，达到 00-product-goals.md 的四层对等：接口面、数据格式、边界语义、可观测文本。判定以 `make ci` 退出码 0 + parity 0 missing 为准。

## 2. RASCI

| 角色 | 承担者 | R/A | 说明 |
| --- | --- | --- | --- |
| 需求口径、拆分、派工、合并裁决 | Coordinator | A | 不写产品代码；只写本章程、进度看板与合并提交 |
| 单 lane 实现与自测 | Lane Agent | R | 在自己 worktree 内完成，独立提交 |
| 交叉复核（不同 lane 互审） | Verifier Lane | C | 只在 wave 末尾对合并结果做只读复核 |
| 门禁执行 | Coordinator | R | 合并后跑 `make ci`，失败打回 |
| 上游语义裁定 | Python 源码 | C | `core/framework/**` 是唯一事实来源 |

## 3. 文件所有权（硬约束）

- lane **只创建/修改**自己名下的文件。越界即打回。
- 禁止修改共享装配文件：`internal/server/app.go`、`internal/server/routes_*.go`（已存在的）、`internal/tools/registry.go`、`internal/cli/*`、`Makefile`、`go.mod`。
- 路由 lane 只产出 `RegisterXxxRoutes(mux, deps)` 形式的新文件；装配由 wave 2 的 integrator lane 统一完成。
- 新增依赖一律禁止。需要新依赖即返回 blocker，不得自行加 `go.mod`。

## 4. Lane 划分（wave 1）

| Lane | 范围 | 上游参考 | 目标产物 |
| --- | --- | --- | --- |
| A | colony workers 路由 20 条 | `server/routes_colony_workers.py` | `internal/server/routes_colony_workers*.go` |
| B | queens / queen tools / queen mail / claude-code-queens 19 条 | `server/routes_queens.py`、`routes_queen_tools.py`、`queen_mail.py`、`routes_claude_code_queens.py` | `internal/server/routes_queens*.go` |
| C | credentials / soul / channels 14 条 | `server/routes_credentials.py`、`routes_soul.py`、`channels.py` | `internal/server/routes_credentials*.go` |
| D | sentinel 路由 / mcp 路由 / maintenance 14 条 | `server/routes_sentinel.py`、`routes_mcp.py`、`routes_maintenance.py` | `internal/server/routes_sentinel*.go` |
| E | colonies / colony tools / execution 12 条 | `server/routes_colonies.py`、`routes_colony_tools.py`、`routes_execution.py` | `internal/server/routes_colonies*.go` |
| F | workers / models / app 级路由 / events 15 条 | `server/routes_workers.py`、`routes_models.py`、`app.py`、`routes_events.py` | `internal/server/routes_workers*.go` |
| G | tasks / prompts / messages / memories / sessions 残余 9 条 | 同名 `routes_*.py` | `internal/server/routes_*_extra.go` |
| H | 集成工具（`tools/src/aden_tools`：104 模块 / 324 文件 / 811 action） | `tools/src/aden_tools/**`、`framework/host/colony_tools_config.py` | `internal/tools/integrations/` 新包（本期交付框架 + ≥20 模块 / ≥150 action） |
| I | Sentinel 引擎 | `framework/sentinel/**`（8 模块，2,090 行） | `internal/sentinel/` 新包 |
| J | Colony 多 worker 编排闭环 | `framework/host/**`、`framework/agents/**`、`runtime/` | `internal/colony`、`internal/host` 扩展 |

## 5. SOP（每个 lane 的执行顺序）

0. **从当前绿色基线派生**：Coordinator 先把 main 跑到 `make ci` 绿并提交，再 `git worktree add -b lane/<x> <path> HEAD`。禁止从旧提交派生——本轮已三次出现 lane 基于修复前提交、各自重复修同一处冲突的情况。
1. `cd /Users/mac/hive/.wt/lane-<x>/go_hive/go-hive`
2. 读上游 Python 实现与对应测试，列出行为清单（含错误分支、空值、越界、序列化字段顺序）。
3. 先写测试（`_test.go`），每条打 `// Parity: core/framework/...py::test_name`；上游无对应用例才用 `// Parity-exempt: 理由`。
4. 实现到测试通过。不改共享文件，不加依赖，不写除门禁注解外的注释。
   **包级标识符必须带 lane/scope 前缀**（如 `colonyWorkerPattern`、`mustJSONString`）：多个 lane 在同一包内声明同名变量会导致合并后 `redeclared in this block`，而单 lane 自检永远发现不了。`//go:build` 互斥的 OS 变体文件同名属正常，不算冲突。
5. 门禁（在本 worktree 内）：
   ```
   CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build ./...
   CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test ./internal/... -count=1
   gofmt -l .
   bash ../../scripts/check_parity.sh   # 仓库根 scripts/check_parity.sh
   ```
6. **每完成一个文件就 `git add <该文件> && git commit`**，不要攒到任务结束：本轮 10 个 lane 中有 5 个在 turn 结束时未提交，文件留在 worktree 里，只能由 Coordinator 代提交。
   消息格式 `feat(<scope>): <范围> 对等移植`。
7. 返回结构化结果：覆盖的路由/工具清单、测试数、门禁输出、未解 blocker。

## 6. PDCA 节奏

- **Plan**：Coordinator 按缺口表派工，明确所有权与验收。
- **Do**：lane 并行，worktree 隔离，独立提交。
- **Check**：合并后 Coordinator 跑 `make ci`；另派 Verifier lane 只读复核 parity 注解是否指向真实上游用例；每个 wave 边界跑 `uv run --no-project python go_hive/scripts/pace.py` 记录实测步调（lane-hour 产能、ETA、EVM），作为下一波派工量的依据。
- **Act**：门禁失败打回原 lane；系统性偏差（例如序列化字段顺序）写回本章程，禁止各 lane 各自发明。

## 7. 升级规则

- 上游行为需要真实第三方凭据/网络才能验证时，lane 必须标注 `Parity-exempt: 需真实凭据，未验证`，并在返回中列为 blocker，不得伪造成功路径。
- 同一文件出现两个 lane 的写需求时，立即上报 Coordinator 重新指派，不得自行协调。

## 8. 交付时间线与取舍（2026-11-18 20:25 定）

目标日期：**2026-11-19 18:00**。该日期下可交付的定义收窄为「候选发布版」：

1. 141 条路由全部注册且可达；
2. Sentinel 引擎与 Colony 编排闭环可运行；
3. 前端构建产物嵌入，`/` 可用；
4. `make ci` 与 parity 门禁全绿；
5. 集成工具：框架层 + 优先子集（≥20 模块 / ≥150 action），其余模块登记为缺口，不计入 19 日阻塞。

以下不计入 19 日交付，顺延至 11 月 25 日：剩余集成模块（约 84 个 / 约 680 个 action，优先级见 `INTEGRATION_BACKLOG.md`）、依赖第三方凭据的端到端验证、逐 token 流式对等的完整回归、发布打包与文档定稿。

压缩手段（按优先级）：
- 并行度维持 10，wave 边界只做门禁与装配，不做人工评审停顿；
- 文档、注释、示例一律让位于代码与测试；
- 任何 lane 返工超过一轮即降级为 blocker，由 Coordinator 改派或砍范围，不拖全局。

