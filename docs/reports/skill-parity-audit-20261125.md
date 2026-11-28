# Skill 机制对齐审计：扫描 · 加载 · 应用 · 创建

- 日期：2026-11-25 11:38 CST
- 基线：仓库 `b8e1c7a06`（Python `core/framework/skills/**` vs Go `internal/skills/**`）
- 范围：发现/解析/目录提示/预设与覆盖/信任/工具门控/读工具/写入/安装审批/HTTP/CLI/热重载
- 方法：逐文件核对两侧实现与现有对位测试；未运行跨语言 golden 全量套件
- 结论：**主干对齐度很高，但不是“完全对齐”**。本轮修复 1 个语义差异（colony worker/overseer 忽略 queen 覆盖），其余 5 项登记为已知差异/缺口。

## 1. 判定总表

| 面 | 状态 | 说明 |
|---|---|---|
| 扫描 | 基本一致 | 9 类根、顺序、last-wins、深度/预算、跳过表、符号链接、name 冲突键一致；内置 skill 的 `location` 已改为稳定缓存根（S2 已修），与 Python 包目录的路径形态差异保留 |
| 加载 | 已修复 1 项 | parser/manager/trust/override/preset/tool-gating 对齐；colony worker/overseer 现在读取 queen 的 `skills_overrides.json`（S1） |
| 应用 | 基本一致 | catalog prompt 有跨语言 golden；reminder、tool-gating、visibility 一致；Go 多两个读工具与一个 reload 端点（有意扩展，S6）；缺 `SKILL_INSTALL_REQUESTED` 事件（S4） |
| 创建 | 一致 + 有意加固 | `write_skill` 契约逐字节、三 scope、安装审批、CLI 12 子命令一致；A30 路径加固有意保留（S7）；write→reload 为时序差异（S5） |
| 契约 | 已补齐 | `contracts/skill-discovery.json` + Go/Python 各一条对位测试（S3 已修） |

## 2. 对齐证据（本轮核对）

| 维度 | 两侧实现 | 证据 |
|---|---|---|
| 扫描根与顺序 | `discovery.py:103-200` ↔ `discovery.go:100-260` | `discovery_test.go`；`docs/reports/storage-lifecycle-audit-20261125.md` §2 |
| 解析宽松度/frontmatter | `parser.py:180-250` ↔ `parser.go:96-190` | `parser_test.go` |
| manager 流水线 | discovery → trust → overrides → catalog（`manager.py:151-200` ↔ `manager.go:89-145`） | `manager_test.go` |
| override 文件 schema/原子写 | `overrides.py` ↔ `overrides.go` | `overrides_test.go`（含 Python isoformat 时间戳） |
| 目录提示渲染 | header/XML/5000 compact/300 cap/visibility | `catalog_prompt_parity_test.go`（跨语言 golden） |
| 预设默认关闭 | `_apply_overrides` ↔ `ApplyManagerOverrides` | `manager_test.go:112` |
| 信任门控 | `trust.py` ↔ `trust.go`（同名 env、repo 归一、`trusted_repos.json`） | `trust_test.go` |
| 工具门控 | `tool_gating.py` ↔ `tool_gating.go` | `tool_gating_test.go` |
| `write_skill` 工具契约 | `queen_lifecycle_tools.py:2085` ↔ `agent_tools.go:55-180` | `write_skill_contract.go`、`agent_tools_test.go` |
| 安装请求/审批 | `install_requests.py` + `routes_skills.py` ↔ `install_requests.go` + `routes_skills.go` | 两侧路由测试 |
| HTTP | Python 14 条 ↔ Go 14 条 + `POST /api/skills/reload` | `docs/api-contract-diff.md` |
| CLI | 12 个子命令一致 | `cli/skill_test.go` |
| 热重载 | watchfiles（1s debounce）↔ 轮询（1s interval + 1s debounce） | `watcher.go`、`live_reload.go` |

## 3. 差异与缺口

### S1（已修，本轮）：colony worker/overseer 忽略 queen 覆盖

- 修复前：`internal/colony/skills_surface.go` 调 `ApplyManagerOverrides(..., nil, ...)`，第三个参数（queen store）恒为 `nil`。worker 与常驻 overseer 都走这个 surface（`runtime.go:469, 962`）。
- 上游：`core/framework/host/colony_runtime.py:715,750` 把 `queen_home/skills_overrides.json` 交给 `SkillsManager`，所以 UI 禁用/墓碑/master switch 以及对 preset 的显式启用都会作用于 colony worker。
- 后果：Go 下被禁用的 skill 仍会出现在 worker/overseer 的 `<available_skills>` 与 `/colony/skills`。
- 修复：
  - 新增 `ColonyRuntime.queenOverrides()` / `queenOverridesPath()`（`internal/colony/skills_surface.go`）。
  - `workerSkillSurface` 改用 queen store。
  - colony watcher 的 `WatchTargets.Files` 加入 `skills_overrides.json`，与上游 manager 的 `watched_files` 对齐；UI 开关无需等下一次 SKILL.md 变化。
- 回归：`internal/colony/skills_surface_test.go::TestWorkerCatalogHonorsQueenDisableForColonySkills`（禁用后从 prompt 与读目录消失；重新启用并经 `ReloadSkills` 后恢复）。
- 命令：`go test ./internal/colony -run 'TestWorkerCatalogHonorsQueenDisableForColonySkills|TestWorkerSkillSurface|TestWorkerCatalog' -count=1` → ok；`go test ./internal/skills -count=1` → ok。

### S2（已修）：内置 skill 的 `location` 曾使用随机临时路径

原问题（F1）：Go 用 `//go:embed` + `MkdirTemp("", "hive-sk-")`，`location` 每次进程启动变化，且进入系统提示、`/api/skills`、`hive skill list`；Python 是稳定包路径。现 `internal/skills/discovery.go::bundledSkillsRoot` 把嵌入树展开到 `$HIVE_HOME/cache/skills/bundled-<bundle-id>/`（内容哈希寻址，版本变化换目录，启动时校验/修复），`location` 对同一 bundle 稳定；`internal/skills/bundled_root_test.go` 覆盖。Python 仍是包目录路径，路径形态差异保留（08 §3.4 已登记）。

### S3（已修）：机器可读的扫描面契约

新增 `contracts/skill-discovery.json`（scopes/priority/roots/limits/skip_dirs/collision）与两侧对位校验：`internal/skills/skill_discovery_contract_test.go`、`core/tests/test_skill_discovery_contract.py`。新增 scope 或改顺序现在必须同时改契约与两侧测试，否则对位测试失败。

### S4（未修）：Go 不发布 `SKILL_INSTALL_REQUESTED`

Python 在 `write_skill(scope="global")` 时发布该事件（`queen_lifecycle_tools.py:2398`），前端 `queen-dm.tsx:2155` / `colony-chat.tsx:1593` 依赖它刷新待决列表。Go 只在 `eventbus` 定义类型，`requestGlobalSkill`（`agent_tools.go:324`）只落请求不发布；`SKILL_INSTALL_DECIDED` 两侧都发（`routes_skills.go:1310` 附近）。影响：live 页面要等下一次拉取才看到待决请求。

### S5（未修，时序）：写入后的同步 reload

Python 的 `write_skill`（queen/colony）在处理函数内 `reload_runtimes`；Go 不 reload，依赖 1s 轮询 watcher（interval + debounce 各 1s）。同 turn spawn 的 worker 不受影响（`workerSkillSurface` 每次重算），但已在运行的 worker/queen catalog 最多滞后约 2s。

### S6（有意扩展，已登记）：Go 独有的读工具与 reload 端点

- `skill_view` / `list_skills`（`internal/skills/agent_tools.go:36-37`）不在 Python 工具面；登记于 `docs/tool-contract-diff.md`、`docs/tool-output-contract.md`。
- `POST /api/skills/reload` 为 Go 扩展，登记于 `docs/alignment-differences.md`。

### S7（有意加固，已登记 A30）：安装路径校验

Go 的 `hive skill install` 拒绝越界名称/子目录；Python 未加固。见 `docs/alignment-differences.md` A30。

## 4. 建议优先级

1. ~~S2：内置 skill 稳定 location + 清理临时根~~ 已修（`bundledSkillsRoot` 内容哈希缓存；Python 包目录形态差异保留）。
2. S4：在 `requestGlobalSkill` 发布 `SKILL_INSTALL_REQUESTED`，与前端契约对齐。
3. ~~S3：补 `contracts/skill-discovery.json` 与两侧对位测试~~ 已修（两侧对位测试在 `internal/skills` 与 `core/tests`）。
4. S5：写入路径补同步 reload（或明确“≤2s 最终一致”为产品口径）。
5. S6/S7 保持登记，不改实现。

## 5. 验证边界

- 已运行：`go test ./internal/colony -run ...`、`go test ./internal/skills`、`go vet ./internal/colony ./internal/skills`。
- 全量 `go test ./internal/colony` 出现过一次 `TestPlaybookRunLogErrorRowHasNullName` 失败（时间戳跨秒断言），单独重跑通过，属既有 flaky，与本次改动无关。
- 未运行：跨语言 prompt golden 全量、前端 E2E、真实 Tauri 桌面会话。
