# MCP 裸名别名预实现（D3 选项 A，待批）

> 分支 `prep-mcp-alias`（worktree `/Users/mac/hive/.wt/prep-mcp-alias`），基线
> `f0a1899e`。本页是 **待用户拍板** 的 D3 选项 A 预实现说明：保留 Go 前缀
> 命名方案，只加“旧裸名 → 前缀名”的解析回退。**未合并、未改冻结契约字节**。
>
> **状态更新（2026-11-23）：D3 终选 B′ —— 默认注册 server 侧裸名（见
> `docs/pending-decisions.md` §D3、`docs/mcp-namespace-residuals.md`）。本页的
> “选项 A”不再是默认路径；别名层保留但只在显式 `mcp.namespace` 的前缀模式下
> 有意义：裸名模式 `recordMCPAlias` 恒等、`mcpAliases` 为空
> （`TestMCPBareDefaultAliasesStayIdentity`）。下文保留当初的预实现设计作为
> 历史记录，实现现状以 `docs/e2e-assets/mcp-bare-default/REPORT.md` 为准。**
>
> 决策背景：`docs/pending-decisions.md` §D3、`docs/alignment-treatment-plan.md`
> §B；命名与折叠事实：`docs/mcp-namespace-residuals.md` R1/R2。

## 0. 结论

| 项 | 结果 |
|---|---|
| 裸名调用是否可达前缀工具 | 是：`echo_text` → `mcp__<ns>__<srv>__echo_text`；命中后执行器仍向 server 发 `echo_text` |
| 冲突语义 | 与现有 first-wins 一致：同名裸名按 server 注册顺序先到先得；折叠键的归属仍由注册期 first-wins/preserve 决定 |
| 反向（前缀名） | 不变：前缀名是唯一注册键/工具面名，精确匹配永远优先；不新增“前缀 → 裸名”映射 |
| 冻结契约 | 未改：`docs/architecture/contracts.md` `tool-schema.mcp-namespace` 与 `contracts/tool-definitions.json` 字节未动 |
| 工具面 sha | 未变：`make tool-surface-check` → `tool_surface_flat.json is up to date ({'llm_request': 24, 'lifecycle': 16, 'synthetic': 1, 'unique': 39})` |
| `mcpToolNames()` 反查 | 不受影响（见 §5） |
| 测试 | 新增 8 个用例全 PASS；`go test ./internal/tools`、`./internal/server`、`./internal/cli`、`./internal/colony`、`./internal/host` 全绿；parity 5717/5717 |
| 合并 | 先生成 2 个提交（代码+测试、文档），**不合并**，等 D3 拍板 |

## 1. 设计

### 1.1 落点：唯一的解析入口

所有模型/客户端工具调用最终都走
`internal/tools/registry.go:RegistryExecutor.Execute`（agent loop 的
`ToolExecutor`、chat/colony 的 `tools.ExecuteTool`、CLI `sessionToolCatalogue`
均经此入口）。别名层就落在这里：**只在精确注册名查不到时**做一次回退解析，
不碰注册表内容、不下发别名、不改工具定义。

```
ToolUse.Name ──► r.tools[name] 精确命中? ──是──► 原执行器（行为不变）
                        │否
                        ▼
              mcpAliases[bare] 命中且目标仍注册? ──是──► 用目标键的执行器
                        │否
                        ▼
                原 “Unknown tool: …” 错误（含 close-match 建议）
```

### 1.2 别名索引：注册期建立、first-wins

`ToolRegistry.mcpAliases map[string]string`（bare → registered）：

- 在 `registerMCPServerInner` 的工具循环里，对**通过 admission gate** 的每个
  remote 工具名 `remoteName` 记录一次映射；目标键必须是 **MCP 自己的注册键**
  （`mcpToolNames` 命中），因此：
  - 同一 server 内 `x-y`/`x_y` 折叠、跨 server `a-b`/`a_b` 前缀折叠时，两个
    裸名都能解析到那个唯一存活的前缀键；键的归属仍由现有 first-wins /
    `preserve_existing_tools` 决定（不新增第二套冲突规则）。
  - 若前缀键被**非 MCP** 工具（内置/集成）占着，则不建别名——裸名不会借道
    别名打到别人的工具（命名空间保护不被绕过）。
- 同一个裸名被多个 server 暴露时，**第一个登记者保留别名**（注册顺序 =
  现有 first-wins）；后来的 server 仍可用自己的前缀名调用。
- `ToolNamespace == ""`（旧配置加载路径）时注册名就是裸名，别名跳过（恒等，
  无意义）。
- `dropMCPServers`（reload/resync）清空索引，重挂时重建；解析时再校验目标键
  当前仍注册，避免指向已被移除的工具。
- `RegistryExecutor.ProbeForTool` / `KillForTool` 走同一解析，所以裸名调用的
  超时探活/强制断连也落到正确的前缀键。

### 1.3 与“双向别名”的口径

决策表写的是“双向别名（旧名→新名）”；本实现按任务口径落地为**可双向调用、
单向解析**：

- 旧裸名可调用（新增回退）；
- 新前缀名可调用（原有路径，且精确匹配优先）；
- 不建立“前缀名 → 裸名”的注册或重写，前缀名仍是唯一模型可见名/注册键，因此
  工具面、目录、反查全部不变。

## 2. 改动点

| 文件 | 改动 |
|---|---|
| `go_hive/go-hive/internal/tools/registry.go` | `ToolRegistry.mcpAliases` 字段与初始化；`RegistryExecutor.Execute` 的裸名回退（精确优先）；`ProbeForTool`/`KillForTool` 先解析别名 |
| `go_hive/go-hive/internal/tools/mcp_registry.go` | `recordMCPAlias`（仅 MCP 键、first-wins）、`resolveMCPAlias`（目标仍注册才返回）、`resetMCPAliases`；注册成功与 first-wins 跳过两条路径都登记别名；`dropMCPServers` 清空 |
| `go_hive/go-hive/internal/tools/mcp_alias_test.go` | 新增 8 个用例（见 §3），全部带 `// Parity-exempt:` 说明 |

未改：`contracts/tool-definitions.json`、`contracts/**`、`go.mod`、`Makefile`、
前端、`docs/architecture/contracts.md`、`tool_surface_flat.json`。

代码量：`registry.go +26`、`mcp_registry.go +47`、测试文件新增 12,063 字节。

## 3. 测试结果（本 worktree 实跑）

命令（均离线）：

```bash
cd .wt/prep-mcp-alias/go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/tools -run 'TestMCPBareNameAlias' -count=1 -v
```

| 用例 | 钉住的行为 | 结果 |
|---|---|---|
| `TestMCPBareNameAliasResolvesPrefixedTool` | 单 server：裸名解析、前缀名不变、server 收到的仍是 remote 名 | PASS |
| `TestMCPBareNameAliasFoldConflictUsesFirstWins` | server `a-b`(`get-sum`) + `a_b`(`get_sum`) 折叠成一键：两个裸名都解析到 `mcp__a_b__get_sum`，执行 `a-b:get-sum`（first-wins） | PASS |
| `TestMCPBareNameAliasSameServerFold` | 同 server `x-y`/`x_y` 折叠：两个裸名都解析到 `mcp__fold__x_y`，执行 `fold:x-y` | PASS |
| `TestMCPBareNameAliasSameRemoteNameFirstWinsAcrossServers` | 两个 server 同名 `echo_text`：裸名归第一个；`mcp__s2__echo_text` 仍可调用（反向不受影响） | PASS |
| `TestMCPBareNameAliasDoesNotShadowBuiltin` | 内置 `echo_text` 与远端同名：裸名仍走内置，前缀名走 MCP | PASS |
| `TestMCPBareNameAliasLongName` | 70 字符裸名可解析；注册名仍为 `mcp__srv__`+70、不截断 | PASS |
| `TestMCPBareNameAliasUnknownNameStaysUnknown` | 未知名/仅折叠相似的 `echo-text` 仍是 `Unknown tool`，不猜测解析 | PASS |
| `TestMCPBareNameAliasClearedOnServerDrop` | `dropMCPServers` 后别名清空、裸名回到未知名 | PASS |

回归与门禁：

| 检查 | 命令 | 结果 |
|---|---|---|
| tools 包全量 | `go test ./internal/tools -count=1` | ok（2.5s） |
| 依赖包 | `go test ./internal/server ./internal/cli ./internal/colony ./internal/host -count=1` | 全部 ok（45.2s / 34.0s / 36.5s / 4.9s） |
| vet | `go vet ./internal/tools` | 退出 0 |
| 构建 | `go build ./...` | 退出 0 |
| parity 注解 | `make parity` | 5717/5717，missing=0 |
| gofmt | `make fmt-check` | `gofmt: OK` |
| 工具面 sha | `make tool-surface-check` | `up to date`，unique=39 不变 |
| 全模块 | `make test` | 见 §3.1 |

### 3.1 全模块 `make test`

`make test`（`go test -count=1 ./...`，离线）：**64 个包 `ok`、0 个 `FAIL`、
`FULLTEST_EXIT=0`**（日志：`~/.hermes/profiles/omh-tui/cache/scratch/prep-mcp-alias-fulltest.log`，
本机 lane 运行存档；关键行 `FULLTEST_EXIT=0`）。

## 4. 合并步骤与回滚

### 4.1 合并步骤（拍板 A 后）

1. 审查本分支两个提交（`git log --oneline f0a1899e..prep-mcp-alias`）。
2. `git merge --no-ff prep-mcp-alias`（或按提交 cherry-pick 到发布分支）。
3. 在合并后的树上跑：`make fmt-check vet test parity tool-surface-check`；
   若走 CI wave，跑完整 `make ci`。
4. 无需重生成任何契约/工具面产物（`tool-surface-check` 已验证零漂移）。
5. 合并后 `git worktree remove .wt/prep-mcp-alias`（分支可保留作审计）。

### 4.2 回滚

- 仅内存索引 + 一个解析回退，无数据迁移、无配置/契约变更：
  `git revert <code+test sha> <docs sha>`（或撤掉 merge commit）即可。
- 回滚后行为只剩“裸名 → Unknown tool”，前缀名路径自始至终未变，不需要任何
  反向迁移。
- 若只想关闭回退而保留代码：删除 `Execute` 中的 `resolveMCPAlias` 分支即可
  （一处），索引成为死数据，可后续再启。

## 5. 对 `mcpToolNames()` 反查的影响：无

`internal/server/chat_tool_tiers.go:mcpToolNames()`（以及
`internal/cli/mcp_loader.go:mcpLoadedToolNames()`、`internal/host` 白名单）的
链路是：

```
loader.Status().ToolNames（server 侧原名）
  └─ NamespacedMCPToolName(namespace, server, name) → 前缀名集合
       └─ 与会话目录里的工具定义名求交
```

本实现不改 `NamespacedMCPToolName`、不改 `MCPServerStatus`、不改注册名、不改
`mcpServerTools` / `mcpFullCatalog` / `mcpToolNames`，别名索引是
`ToolRegistry` 内部**新增的独立 map**（只在 `Execute`/探活入口读取）。因此：

- 反查的输入（status 原名）与输出（前缀名）逐字不变；
- `chat_tool_tiers` 的 tier/allowlist 过滤、Queen/colony 工具提升、活工具诊断
  均看不到别名（这正是“工具面 sha 不变”的原因）；
- 唯一可观测差异是：裸名调用从“Unknown tool”变成“解析到前缀工具并执行”，
  且在 info 日志里多一行
  `MCP tool called by bare name '<bare>' resolved to '<prefixed>'`。

## 6. 风险与待拍板点

| # | 风险/取舍 | 说明与缓解 |
|---|---|---|
| 1 | 折叠丢弃工具的裸名会“落在存活键上” | `a-b`/`a_b`、`x-y`/`x_y` 场景里被 first-wins 丢掉的 remote 名（`get_sum`、`x_y`）仍会解析到唯一存活键，实际执行的是先到工具。这与“first-wins”一致，但语义上是“同一槽位的另一个 remote 工具”；已在用例中钉住。若要求严格“只别名真实注册的工具”，把 `recordMCPAlias` 的调用移出 first-wins 跳过分支即可（一行），代价是这些旧裸名返回 Unknown tool |
| 2 | 跨 server 同名裸名有歧义 | 裸名只归第一个注册的 server；第二个只能走前缀名（与 Python 的 first-wins 一致）。不引入顺序无关的猜测 |
| 3 | 裸名可能撞内置/集成名 | 精确匹配永远优先；别名只在前缀键属于 MCP 时建立，双重保护（用例 5） |
| 4 | 长名（≥64）仍可能被 provider 拒绝 | 别名不改变注册名长度，R2 维持原状；provider 侧截断/哈希是独立决策 |
| 5 | reload 竞态 | 索引读写走 `ToolRegistry.mu`；drop 清空、解析再校验目标存在。最坏情况是一次裸名调用返回 Unknown tool，不会执行错键 |
| 6 | 可观测性 | 每次裸名命中记一条 info 日志；若担心噪声，可在合并时降为 debug（当前日志层无 debug 级别，保持 info） |
| 7 | 外部 MCP 客户端发现面 | 别名是**服务端分发回退**，不进入 `tools/list`。外部客户端仍只会看到前缀名；本层只帮助“已经用旧裸名发调用”的调用方（agent loop、内置 UI/脚本、API 调用） |

## 7. 复现

```bash
cd /Users/mac/hive/.wt/prep-mcp-alias/go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/tools -run 'TestMCPBareNameAlias|TestMCPNamespace' -count=1 -v
```
