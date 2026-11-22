> 参考基线动态（2026-11-22）：origin/main 已推进到 1.0.15「Notes in Time」（6703b434，35 文件 delta）；本次 merge 后新增 5 项 G1 工作（memory notes 同族 ~23h）。冻结契约的 reference 锚点（python@1b9c458a）是否整体切换待用户拍板。

# 00 · 产品目标与验收标准

> 上游参考：`aden-hive` / `aden-tools`（工具契约 reference commit 见 `contracts/*.json` 的 `reference` 字段）
> Go 模块：`github.com/aden-hive/hive-go`
> 本文定义「什么叫移植完成」，是所有实现与评审的共同口径。
> 最近修订：2026-11-22 00:55（五轴口径 + 当前状态快照）

## 1. 一句话目标

用**纯 Go、无 cgo** 重写 OpenHive，做到 **五个一致 + 一条迁移路**：

> **外观一致、语义一致、功能一致、特性一致；hive 用户可用无痛无损迁移到 go_hive。**

## 2. 五个不可妥协的目标

| 目标 | 含义 | 验收证据（可复现命令/产物） |
| --- | --- | --- |
| **外观一致** | 模型/前端/脚本能看到的每一层字段级逐字：API/WS 字段、tool 定义（name/description/input schema）、skill 内容、env、configuration.json、CLI help/输出 | `contracts/*.json` + `docs/tool-contract-diff.md`、`api-contract-diff.md`、`env-config-contract-diff.md`、`sse-payload-parity.md`、`cli-surface.html`；`make compat-check` C1–C12 |
| **语义一致** | 同一输入产生同一行为与同一可观测结果：AgentLoop/Colony/playbook/memory/soul/queen-mail 全链路 | 逐文件 `// Parity:` 注解（`check_parity.sh`）+ 真实双跑证据（`docs/e2e-assets/**`） |
| **功能一致** | 上游每个工具/路由/命令/机制在 Go 侧都存在且可被真实调用 | C12 模块清单 271 条登记 0 未登记；`docs/tool-parity-matrix.html`；缺口登记在 `docs/delivery-backlog.json` |
| **特性一致** | 机制完整性：热重载、调度（cron/trigger/slot）、多模态、升级兼容、热插拔 skill、MCP 热重载等 | `docs/compatibility-policy.md` + `scripts/upgrade_compat_check.sh`（每 slot 自动跑）；各特性页 |
| **无痛无损迁移** | 同一 `HIVE_HOME` 直接可用：旧数据零丢失、启动自动迁移、可回滚 | G2 清单 6/6：旧 home e2e、回滚演练、安装升级、sidecar 一致、G2 文档、支持矩阵（`docs/g2-lossless-replacement.html`） |

四类「一致」冲突时优先级：**语义一致 > 外观一致 > 功能一致 > 特性一致**；迁移路是硬门槛，任何一条不满足即不得宣称交付。不允许为了「Go 风格更漂亮」而改变 Python 的外部行为——对等优先于惯用法。

## 3. 判定标准

### 3.1 外观一致（字段级）

1. **接口面**：导出的函数、结构体、工具名、HTTP 路由、CLI 子命令与上游一一对应。
2. **数据格式**：落盘文件可双向读取——`state.json` schema、会话 part 文件名、凭据 `.enc` + `metadata/index.json` v2.0、Tracker SQLite v3（`_tracker_registry`/`_tracker_changes`）、技能 `SKILL.md` frontmatter、`~/.hive` 目录布局。
3. **逐字层**：工具 description/参数 schema（含 `anyOf[string,null]` 与 default）、FastMCP 生成物（title/outputSchema/structuredContent）、SSE 帧、CLI help 版面。
4. **当前权威指标**：工具一致度以 `docs/tool-contract-recheck.json`（live recheck）为唯一权威口径；冻结快照 diff（`tool-contract-diff.md`）仅作历史对照。两套口径的统一说明见该 diff 页 §0。

### 3.2 语义一致

- 边界语义：错误分支、空值、越界、损坏文件与上游一致（会话损坏 part 跳过、凭据索引损坏重建、未知 JSON 字段忽略）。
- 可观测文本：提示词、错误消息、通知文案在需要一致处逐字（如 `[PLAYBOOK_COMPLETE]`、`wrap_reminder` 包装、queen identity 注入）。
- 无法一一对应处必须**登记例外**（`Parity-exempt` 注解 + 文档页例外清单），不允许静默差异。

### 3.3 功能/特性一致

- 每条上游能力要么有 Go 生产调用点，要么在 `delivery-backlog.json` 有登记缺口与工时；两者皆无 = 未完成。
- 机制类特性（调度、热重载、升级兼容、多模态）必须有真实端到端证据或明确标「未验证」，不得用单测冒充。

### 3.4 迁移（无痛无损）

- 启动迁移链：v2→v3→colony_sessions→tracker bootstrap，非致命 warning，失败不破坏原数据。
- 旧 `HIVE_HOME` 双跑：Python 与 Go 交替读写同一 home，数据零丢失；回滚演练通过。
- 迁移相关差异（如 `exports/` 相对路径口径）必须显式登记并由用户裁定。

## 4. 「质量对等」的判定标准

- 每一个 `func TestXxx` 必须在注释块中声明来源：

  ```go
  // Parity: core/tests/test_session_store.py::test_session_id_traversal
  func TestSessionIDAndTraversal(t *testing.T) { ... }
  ```

- 无法一一对应的测试必须显式豁免（`// Parity-exempt: 具体理由`）。注解是契约：`check_parity.sh` 与 `check_parity_targets.sh` 双双退出码 0，引用必须指向真实存在的 Python 测试/符号。

## 5. 「平台优势」的判定标准

| 项 | 要求 |
| --- | --- |
| cgo | 全仓库 `import "C"` 为 0；`CGO_ENABLED=0` 是默认 |
| SQLite | `modernc.org/sqlite`（纯 Go 转译） |
| 交叉编译 | `make cross` 六平台（darwin/linux/windows × amd64/arm64）全部通过 |
| 离线 | `GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod` 下可构建、可测试 |
| 依赖 | `modernc.org/sqlite`、`purego`、`yaml.v3`、`go-rod/rod` + `go-rod/stealth`（浏览器渲染，2026-11-21 用户批准）；新增依赖需显式批准 |
| 脚本宿主 | Python `.play.py` 走外部解释器 + stdio JSON-RPC（§playbook），主二进制零 Python 链接 |

## 6. 非目标

- **不重新设计产品**：不新增上游没有的能力，不改 UI 交互，不改 Queen 人格与技能内容。
- **不做 Python 插件 ABI 兼容**：对等的是数据与协议，不是进程内扩展机制。
- **不移植死代码**：graph 时代机制（`internal/orchestrator`、`agent_host`、`shared_state` 等）登记 `out_of_scope` 并附源码依据。
- **不追求逐行翻译**：内部结构可用 Go 惯用法改写，外部行为必须一致。
- **不在本阶段做性能优化**：正确性与对等优先。

## 7. 交付物与目录约定

Go 代码与配套文档在 `go_hive/` 下：`go-hive/`（模块）、`contracts/`（冻结契约）、`scripts/`（门禁与生成器）、`docs/`（对齐证据与登记册）、`architecture/`（包结构 model + 生成物）、`dist/slots/`（每 2 小时 slot 版）。上游分析文档 `01`–`10` 描述 Python 版本，仅作参考。

## 8. 验收清单（快照 2026-11-22 00:55）

| 门禁 | 命令 | 通过标准 | 当前状态 |
| --- | --- | --- | --- |
| 格式/静态 | `make fmt-check && make vet` | 无输出 | 通过 |
| 单元测试 | `make test` | 全部包 `ok` | 通过（Makefile 已固化 `TEST_FLAGS ?= -count=1`） |
| 交叉编译 | `make cross` | 6 平台 | 通过 |
| parity 注解 | `make parity` | 退出码 0 | 通过：**5,702/5,702**（installer-verify / lm-notes / bg-recall 三 lane 合并后；refs 全解析） |
| parity 引用 | `make parity-targets` | 退出码 0 | 通过：5,934 条引用，0 missing |
| 接线 | `make wiring-check` | 退出码 0 | 通过 |
| 升级兼容 | `make compat-check` | 12 项全过 | 通过（C1–C12；C12 = 271 条登记 0 未登记：ported 202 / partial 10 / out_of_scope 7 / unported 52；config 135 键 / 21 section） |
| 架构 | `make arch-check` | 7 生成物一致 | 通过（82 包 / 214 边 / 8 层 / 1 条 info warning；soul→queen 反向依赖已消除） |
| 行为冻结 | `refactor_freeze.sh compare wave15-base` | OK（allowlist 逐条理由） | 通过（结构重构 lane 必跑） |
| QC | `scripts/qc_check.py` | 8/8 | 通过 |
| slot | 每 2 小时 `dist/slots/<slot>` | artifact+manifest+compat | 22:00/00:00 已产；24h 窗口 |

## 9. 与 Python 上游的关系

- **同构而非分叉**：Go 版本是上游语义的另一个实现；上游当前工作树的 `core/`、`tools/` 是唯一事实来源，契约冻结记录 reference commit。
- **兼容窗口**：Go 进程应能读取 Python 写下的会话、凭据与 Tracker；反向亦然（至少读）。
- **演进策略**：上游升级时先更新参考测试再改实现；不允许先改 Go 再改测试来「让测试变绿」。上游出现新版本时，作为独立决策评估（可能触发契约重冻结）。
