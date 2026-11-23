# 待拍板决策清单（2026-11-22）

> **状态（2026-11-23）：D1 = 批准（go.mod→1.26.11，合并中）；D3 = 终选 B′（MCP 默认裸名，2026-11-23 拍板；worktree `.wt/mcp-bare-default` 实现）；D2 = 选 A（维持 $HIVE_HOME 基准，无需改代码）。**

> 本页只汇总**需要用户决策**的三项，其余对齐/修复由流水线自主推进。
> 每项给出：现状实测、选项、推荐、影响面、回滚方式。决策前不做改动。

## D1. govulncheck：stdlib CVE 修复需要依赖变更批准

**现状**：`go.mod` 固定在 `go 1.26.5`；`govulncheck` 报告 stdlib（net/http、crypto/tls 等）存在
已在 go1.26.6–1.26.11 修复的 CVE。门禁因此保持红，未做假绿处理。

| 选项 | 做法 | 影响 |
|---|---|---|
| A（推荐） | `go.mod` 升到 go1.26.11（或当前最新 patch）并重跑全量门禁 | 一次性依赖变更；构建产物无行为变化；需重新生成 cross/归档 |
| B | 维持 1.26.5，把 vulncheck 登记为已知例外（含理由与复查条件） | 门禁转绿但带风险例外；外发布（G3）前必须复议 |
| C | 只升到满足 CVE 的最小 patch（1.26.6） | 变更最小；后续仍需再升 |

**为什么需要你**：项目纪律规定依赖变更（go.mod/go.sum）必须显式批准。
**回滚**：`git revert` 对应提交 + 重新构建；无数据面影响。

## D2. exports/ 基准：Python=CWD vs Go=$HIVE_HOME

**现状**：两侧对 `exports/` 的迁移基准不同（实测矩阵见 `docs/exports-baseline-decision.md`）：
Python 以 CWD 为基准（吃任何 CWD 下的 exports/），Go 以 `$HIVE_HOME` 为基准；API 直调时两侧都支持显式绝对路径。
`.migrations` 进 `colony list` 的行为已复核：两侧逐字一致（sha `30e73cca`），登记为对齐、无需修。

| 选项 | 做法 | 迁移兼容 | 可发现性 | 与上游一致度 |
|---|---|---|---|---|
| A（推荐） | 维持 home 基准 + 文档写明 | 高（旧 home 自动迁移） | 中 | 中 |
| B | 改成 CWD 基准 | 低（v1 检出根用户可能漏迁移） | 高 | 高 |
| C′ | home 优先 + 检出根回退（A+B 混合） | 高 | 高 | 中高 |

**需要你答两个问题**：
- **Q1**：是否必须保证「v1 时代在检出根跑 serve 的用户」升级后自动迁移？（是 → 选 C′）
- **Q2**：回退锚点用 CWD 还是检出根（git 根）？

**回滚**：纯读路径逻辑，回滚无数据风险；迁移只搬不改。

## D3. MCP 工具命名：Python 裸名 vs Go 前缀扩展

**现状**（`docs/mcp-namespace-residuals.md`，13 条冻结用例）：
- Python 注册键 = 裸 `mcp_tool.name`，无命名空间折叠；跨 server 同名 first-wins。
- Go 使用 `mcp__<ns>__<server>__<tool>`，非 `[A-Za-z0-9_]` → `_`；该方案已写入冻结契约。
- 后果：含 `-` 的工具名在两侧**模型可见名不同**；`a-b` 与 `a_b` 两个 server 在 Go 侧会折叠成 1 个工具（Python 侧 2 个）。
- 长名（≥64）两侧注册层都不截断（Python 的 64 截断只发生在 Antigravity provider，Go 未移植该 provider）。

| 选项 | 做法 | 影响 |
|---|---|---|
| A（推荐，若以兼容为先） | 保留 Go 前缀方案，登记差异，路由层做双向别名（旧名→新名） | 不动冻结契约；需补 alias 层与测试 |
| B | 切回裸名（与 Python 完全一致） | 动所有含 `-` 的模型可见工具名与 `mcpToolNames()` 反查；契约需改版 |
| C | 双模式（配置开关：namespaced / bare） | 面最大、验证成本最高，但可两者兼容 |

**为什么需要你**：这不是 bug 修复，是**对外可见命名方案**的取舍，且改动面覆盖契约与全部 MCP 工具名。
**回滚**：A 只加别名可随时撤；B/C 需契约版本化，回滚需反向迁移。

**决策记录（2026-11-23，用户拍板终选 B′）**：

- **默认裸名**：`mcp.namespace` 默认值改为空。namespace 为空时注册键 = server 侧原始工具名，**不折叠、不消毒**（`x-y` 与 `x_y` 并存，同 Python `core/framework/loader/tool_registry.py::register_mcp_server`）。
- **冲突语义不变**：跨 server 同名工具仍按现有 preserve / first-wins；裸名模式下 `mcpAliases` 恒等（不记录别名）。
- **前缀模式保留**：显式设非空 `mcp.namespace` 时行为与旧版一致（`mcp__<ns>__<srv>__<tool>` + `[^A-Za-z0-9_]`→`_` + 裸名回退别名）。它是 Go-only 可选模式，不再是默认，也不再是契约描述的主路径。
- **证据**：`docs/mcp-namespace-residuals.md`（R1/R2 在默认模式已一致）、`docs/e2e-assets/mcp-bare-default/REPORT.md`（13 用例实测 + 模式矩阵）、`contracts/config-keys.json`（`mcp.namespace` default `""`）、`contracts/tool-definitions.json` 与 `tool_surface_flat.json` 无 MCP 名漂移。
- **回滚**：`configuration.json` 写 `"mcp": {"namespace": "mcp"}` 即回到前缀/折叠/别名回退的旧行为；或 revert 本分支提交。无数据迁移。
