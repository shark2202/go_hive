# 决策清单（维护至 2026-11-23）

> **当前状态：无待用户拍板项。** D1、D2、D3 均已决策并落地或登记；本页保留决策记录与证据，不再作为待办清单使用。
>
> 维护规则：新决策追加到本页；已决策项保留「决定 / 落地 / 证据 / 残余」，不删除历史理由。若后续出现新的待拍板项，再在本页顶部单列。

## 状态总览

| 编号 | 主题 | 状态 | 结论 | 证据 |
|---|---|---|---|---|
| D1 | govulncheck / stdlib CVE | **已完成** | 批准升 `go.mod` 到 `go 1.26.11` | `go.mod`、`govulncheck-bump-prep.md`、CI |
| D2 | `exports/` 基准 | **已决策 A** | 维持 Go `$HIVE_HOME` 基准，登记与 Python CWD 的差异，不改产品行为 | `exports-baseline-decision.md`、`storage-contract-diff.md` |
| D3 | MCP 工具命名 | **已完成 B′** | 默认裸名；显式非空 `mcp.namespace` 保留前缀模式 | `mcp-namespace-residuals.md`、`e2e-assets/mcp-bare-default/REPORT.md`、`config-keys.json` |

---

## D1. govulncheck：stdlib CVE（已决策，已完成）

**决定**：采用选项 A，将 `go.mod` 从 `1.26.5` 升到 `1.26.11`。

**落地**：

- `go_hive/go-hive/go.mod` 当前为 `go 1.26.11`。
- 1.26.11 覆盖 go1.26.6–1.26.11 的 stdlib 修复，包括 `GO-2026-6859`（`net/http` `MaxBytesReader` 头竞争）。
- `govulncheck` 已从 23 OSV / 1423 events 变为 **0 OSV / 0 events**；后续 wave 复验保持 0。
- 全量 `make ci` 已在 go1.26.11 下通过。

**回滚**：`git revert` 对应升版提交并重建；无数据面影响。

**残余**：无待决策项。G3 发布前按发布门禁再次执行 govulncheck，不把历史结果当永久豁免。

---

## D2. `exports/` 基准：Python=CWD vs Go=`$HIVE_HOME`（已决策 A）

**决定**：采用选项 A，**保持 Go 的 `$HIVE_HOME/exports` 基准**，在文档和发布说明中写明；不改为 Python 的 CWD 基准。

**理由**：

- 旧 home 自动迁移的兼容性优先；Go 的迁移不依赖启动目录，行为更稳定。
- API 直调时两侧都支持显式绝对路径，差异只在 serve 启动调用点。
- 两侧函数能力一致；差异已登记，不是静默缺口。

**保留的已登记差异**：

- Python `migrate_v2` 在 serve 启动时读取 **进程 CWD** 下的 `exports/`。
- Go serve 读取 **`$HIVE_HOME/exports`**。
- 两侧都会写 `<home>/.migrated-v2`；首跑没找到 exports 后不会在后续启动重试（两侧同）。
- `.migrations` 出现在 `colony list` 的行为两侧逐字一致，无需修。

**证据**：`go_hive/docs/exports-baseline-decision.md` 的实测矩阵与 `go_hive/docs/e2e-assets/exports-baseline/`。

**残余**：无待用户问题。Q1/Q2（是否必须自动迁移 CWD exports、回退锚点）已由 A 方案关闭；如未来要求 CWD 自动迁移，需重新开决策，不走本页默认路径。

---

## D3. MCP 工具命名（已决策 B′，已完成）

**决定**：采用 **B′：默认裸名 + 可选前缀模式**。

**落地**：

- `mcp.namespace` 默认值为空。
- 默认模式注册键 = MCP server 侧原始工具名，不折叠、不消毒；`x-y` 与 `x_y` 可并存，与 Python 的 `register_mcp_server` 默认语义一致。
- 跨 server 同名工具沿用 preserve / first-wins；默认模式下 `mcpAliases` 恒等。
- 显式设置非空 `mcp.namespace` 时，保留旧的 `mcp__<ns>__<server>__<tool>`、`[^A-Za-z0-9_] → _`、裸名回退别名。这是 Go-only 可选模式，不是默认，也不是契约主路径。
- 回滚：`configuration.json` 写 `"mcp": {"namespace": "mcp"}` 回到前缀/折叠/别名回退模式；或 revert 对应提交。无数据迁移。

**证据**：

- `go_hive/docs/mcp-namespace-residuals.md`：默认模式 R1/R2 与 Python 一致；13 条冻结用例。
- `go_hive/docs/e2e-assets/mcp-bare-default/REPORT.md`：13 用例实测 + 模式矩阵。
- `go_hive/contracts/config-keys.json`：`mcp.namespace` 默认 `""`。
- `contracts/tool-definitions.json` 与 `tool_surface_flat.json` 无 MCP 名漂移。

**残余**：无待决策项。前缀模式是明确的 Go-only 扩展，按登记口径保留。

---

## 维护记录

- 2026-11-23：三项决策全部闭环；本页从「待拍板」改为「决策记录」。D1 升版完成，D2 选 A，D3 选 B′。
