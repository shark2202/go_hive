# 兼容政策：后续交付如何保证旧用户无损升级

> 适用对象：`hive`（Go 交付，`go_hive/`）每一次 slot 版 / 归档版 / 正式发布。
> 目标：**任何一次交付都必须能被上一交付的用户无损、无痛升级**，并能在需要时
> 退回上一二进制继续工作；这条承诺由常设门禁持续验证，而不是一次性演练。
>
> 相关文档：`upgrade-and-rollback.md`（G2 一次性证据）、`../docs/storage-contract-diff.md`
> （存储/凭据逐字段差异）、`support-matrix.md` §1.1（老用户无损替换判定）、
> `../RELEASE_CHECKLIST.md`（发布出口标准）、`e2e-assets/upgrade-compat/REPORT.md`
> （本政策的首次实测证据）。

---

## 1. 版本语义：每个对外面的“当前版本”与冻结产物

| 面 | 当前契约 | 冻结产物 / 校验入口 | 兼容承诺 |
| --- | --- | --- | --- |
| `HIVE_HOME` 布局 | **v3**（`queens/`、`colonies/<c>/{tracker,queens,workers}`、`memories/`、`credentials/`、`secrets/credential_key`） | 启动迁移链（`internal/storage/migrate_v2.go`、`migrate_v3.go`、`migrate_colony_sessions.go`）+ 标记文件 | 旧布局 **v1/v2 永久可升级**（链式迁移，幂等、只搬移/复制、内容守恒）；不接受“新版本只能从 v2 起”的简化 |
| 存储字段 | `contracts/storage-fields.json`（18 个文件类：10 观测类 + 8 代码类） | `gen_storage_contract.py`（车道内重生成）；`check_compat_contracts.py` C4 自洽 | 已有字段只增不改语义；删字段/改类型 = 破坏性 |
| DB schema | tracker `PRAGMA user_version` / `_tracker_meta.schema_version` = **3**（两侧常数一致：Python `SCHEMA_VERSION`、Go `SchemaVersion`）；session `state.json` `schema_version` = **1.1** | `check_compat_contracts.py` C5（版本常数 + bootstrap DDL 表集） | schema 升级必须**两侧同时**推进并带迁移；单侧先行 = 破坏性 |
| `configuration.json` | 134 个键（`contracts/config-keys.json`） | `freeze_env_config_contract.py`（重生成）；C1/C3（清单、锚点） | 新键必须带默认值、旧文件缺键可用；删键/改名/改默认语义 = 破坏性 |
| 环境变量 | 124 个名字：Python 89 / Go 118 / 共有 83 / Python 独有 6（GCU 平台差异）/ Go 独有 35（`contracts/env-vars.json`） | `freeze_env_config_contract.py --check`（重生成）；C2（审计计数） | 新增 env 必须向后兼容（未设置时行为不变）；删除 env 或翻转解析口径（如 deny-list）= 破坏性 |
| HTTP API | Python 217 路由 / Go 220 路由（`contracts/api-routes.json`；`missing_in_target_effective=0`）；响应形状 77 端点（`contracts/api-responses/`）+ `api.proto` | `export_api_routes.py --check`（C6）；`check_api_proto.py`（C7）；`api-responses` 语料（C9） | 新增路由/新增字段 = 兼容；删除路由、改状态码语义、改字段类型/必填性 = 破坏性 |
| 工具面（LLM 可见） | `contracts/tool-definitions.json`：54 个参考工具 + 103 个 Go 侧工具；名称/描述/`input_schema` 逐字冻结 | `freeze_tool_contract.py check`（C8）；`gen_tool_surface_flat.py --check`、`gen_browser_tool_defs.py --check`（C10） | 删除/改名工具、收紧 `input_schema`（新增必填、收窄 enum）= 破坏性；新增工具/新增可选参数 = 兼容 |
| CLI | `contracts/cli-surface.json`（递归子命令树 / flag / 默认值 / 无副作用退出码探针） | `cli_surface_contract.py --check`（全量重采，发布时跑；不进 `ci`）；**C11** 逐 slot 外观门禁（进 `ci`） | 删除子命令/flag、改默认值、退出码语义或 usage 行 = 破坏性；新增子命令/flag = 兼容（需重采契约） |
| 交付形态 | 四二进制同级（`hive`、`hive-cred`、`hive-browser`、`browserbridge`）+ `SHA256SUMS` + `install.sh` | `release.sh`、`install.sh`、`verify_release_archive.sh` | 归档布局、文件名、SHA256 校验与 `install.sh` 原地升级语义不变 |

**判据来源**：上表的“当前值”由 `make compat-check`（C1–C11）在每次 `make ci` 中
核对；任何一项变化都必须同步更新对应冻结产物，并在发布说明里写明兼容影响。

---

## 2. 什么变更算破坏性

| 变更 | 判定 | 需要的动作 |
| --- | --- | --- |
| 新增 `configuration.json` 键 / env 变量 / API 路由 / 工具 / CLI 子命令 | **兼容** | 更新对应冻结产物 + 文档 |
| 新增 JSON 字段、放宽校验、放宽 `input_schema` | **兼容** | 同上 |
| 新增 `HIVE_HOME` 迁移步骤（旧 home → 新 home） | **兼容**（前提：幂等 + 内容守恒 + 旧二进制仍可读） | 注册进启动迁移链 + `upgrade_compat_check.sh` 增加夹具 |
| 删除/重命名配置键、env、路由、工具、CLI flag | **破坏性** | major 版本；先弃用一个周期 |
| 改默认值、改解析口径（`bool` deny-list、空串、越界回退）、改状态码或错误文案语义 | **破坏性** | major 版本；冻结产物 + 行为测试同步 |
| 删除 `HIVE_HOME` 字段 / 改变字段类型 / 单侧推进 DB `schema_version` | **破坏性** | 双侧迁移 + major 版本 |
| 把旧二进制能读的数据**原地改写**成旧二进制读不出的形状 | **破坏性**（禁止） | 只能新增副本/新路径；旧路径保持可读 |
| 迁移中删除或修改用户文件（不是搬移） | **破坏性**（禁止） | 只允许搬移/复制；删除必须能证明内容已在别处（门禁 M8 内容守恒） |
| 删除 `HIVE_HOME` 布局代（例如不再支持 v1） | **破坏性** | major + 提前一个发布周期公告；`upgrade_compat_check.sh --fixtures` 同步收缩 |

**“兼容”的最低证明**：上面任一“兼容”变更合入后，`make compat-check` 与
`upgrade_compat_check.sh`（对上一交付二进制）都必须通过，且证据落盘。

---

## 3. 迁移与弃用窗口

| 面 | 迁移方式 | 弃用窗口 | 可退出（删除）的条件 |
| --- | --- | --- | --- |
| 存储布局 v1 / v2 | 启动时链式迁移（v1→v2→v3→colony_sessions→tracker ensure），marker 短路 | 保持 ≥ 2 个发布周期 | major 版本 + 公告一个周期 + 兼容门禁夹具移除 |
| 存储字段 | 只增不改；读取保持宽容（缺字段走默认） | 字段标 deprecated 起 ≥ 1 个发布周期 | major 版本 |
| DB schema | 每一级迁移幂等、可重入；`user_version` 单调 | schema 升级与两侧实现同版本发布 | 不支持降级 schema（回滚方向只读） |
| 配置键 / env | 新键带默认值；旧键“先读新再读旧” | 旧键标 deprecated 起 ≥ 1 个发布周期（读侧同时接受新旧） | major 版本 |
| API / CLI / 工具 | 旧字段/旧参数保留并标 deprecated；文档 + 日志提示 | ≥ 1 个发布周期 | major 版本 |
| 二进制回退（N → N-1） | 新版本不得做不可逆改写；N-1 在新 home 上只读 | 永久承诺（对上一发布） | 只有 major 版本可声明“跨越式升级不再支持回退到 N-1” |

窗口的起止必须写进发布说明（`docs/release-notes-*.md`）与 `CHANGELOG`，并在
`support-matrix.md` §1.1 的例外表里登记。

---

## 4. 回滚保证

1. **旧二进制可读**：对任意发布 N，N-1 的 `hive` 必须能在 N 写过的 `HIVE_HOME`
   上启动（`serve` health 200），并通过 CLI 读取会话/colony；读取过程
   **0 新增 / 0 删除 / 0 修改**（门禁 M11/M12/M13，snapshot diff 判定）。
2. **升级不改用户数据**：N 在旧 home 上的启动迁移只允许搬移/复制（内容守恒，
   M8）、不得修改用户数据文件（M7，churn 白名单之外 0 修改）、不得改动已有
   SQLite 的逻辑内容（M9）。
3. **迁移幂等**：第二次启动不产生新迁移、不改 marker、不改 DB（M10）。
4. **例外（必须文档化）**：旧版本在尚未迁移的旧 home 上看不到新布局会话（F5，
   升级即迁移）；JSON 数字序列化/键序等值格式级差异不属数据损坏（见
   `storage-contract-diff.md` §5）。

---

## 5. 发布必须跑的检查清单

每次交付（slot 版、归档版、正式发布）按顺序：

| # | 检查 | 命令 | 通过标准 |
| --- | --- | --- | --- |
| 1 | 契约自洽（env/config/storage/DB schema/API 路由/响应形状/工具面/CLI 外观） | `make compat-check`（`make ci` 已包含；C11 还需本机能 `go build` 且 `python hive` 可用） | `verdict=PASS`；`SKIP` 逐条列出且不计为通过 |
| 2 | 冻结产物重生成（车道内，不进 `ci`） | `freeze_env_config_contract.py`；`freeze_tool_contract.py build`；`export_api_routes.py`；`freeze_api_responses.py`；`gen_storage_contract.py` | 产物 diff 已审阅并提交 |
| 3 | **升级兼容**（对上一交付） | `bash go_hive/scripts/upgrade_compat_check.sh --old-dir <上一交付目录> --new-dir <候选目录>`（或 `make upgrade-compat-check PREV_SLOT=...`） | `verdict=PASS`（3 夹具 × 19 项）；报告 JSON 落盘 |
| 4 | 归档 + 安装 + 回滚 | `release.sh` → `shasum -c SHA256SUMS` → `verify_release_archive.sh` → `install.sh`；G2 方向演练 `g2_rollback_drill.sh` | 归档 4 二进制 + SHA 校验；`verify_release_archive.sh` 全 PASS；`install.sh` 原地升级后 `--version` 正确 |
| 5 | slot 节奏自动门禁 | `release_slot.sh`（自动） | `cadence.json.last_status=ok`、`last_compat=ok`；失败则 `compat_failed` + 看门狗告警，slot 不得视为可交付 |
| 6 | 文档 | 更新 `support-matrix.md` §1.1、`RELEASE_CHECKLIST.md`、本政策（若规则变化）、发布说明 | 例外/窗口/回退结论写清 |

`make compat-check` 的 SKIP 语义：缺 `uv` 时工具面扩展检查（C10）SKIP；
缺 `python hive`（`<repo>/.venv` 或 `~/hive/.venv`）、缺 `go`/`git`、或契约基线
提交不在本地克隆（浅克隆）时，CLI 外观检查（C11）SKIP 并注明对应项「未验证」；
缺 `python3` 时整个目标 `BLOCKED` 并退出 1——**没验过不算绿**。

---

## 6. 门禁实现与覆盖矩阵

### 6.1 升级兼容测试器（`upgrade_compat_check.sh` + `upgrade_compat_probe.py`）

输入：**上一交付的 `hive`** 与**候选 `hive`**（目录或单二进制；`--prev-slot`
自动取 `dist/slots` 中上一 slot）。对三个夹具 home 各跑一遍：

| 步骤 | v1（pre-v2 布局） | v2（pre-v3 布局） | v3（已是当前布局） |
| --- | --- | --- | --- |
| 夹具来源 | `storage_compat_gen_fixture.py --version v1` + Python 参考写入 1 条 Fernet 凭据 | `--version v2` + 同左 | 由上一交付先在 v2 上启动迁移得到（若上一交付更早、无启动迁移，则由候选补迁移并在报告里记录 `v3_prepared_by`） |
| 候选 `serve` #1 | 迁移 v1→v2→v3→colony_sessions→tracker；health / read API；快照 diff | 迁移 v2→v3→…；同上 | 不迁移；同上 |
| 候选 `serve` #2 | 幂等（0 新迁移） | 同左 | 同左 |
| 旧二进制回读 | CLI 读会话/colony；快照 diff 必须 0/0/0 | 同左 | 同左 |
| 旧二进制 `serve` | health 200；0 用户数据写入 | 同左 | 同左 |

规则（19 项/夹具，任一 FAIL 即非 0）：M1 health、M2 读 API（`/api/sessions/history`、
`/api/discover`、`/api/credentials`、colony sessions）、M3 会话内容可见、
M4 colony 可见、M5 凭据元数据（未 seed 则 SKIP 并给原因）、M6 迁移 marker、
M7 用户数据零修改、M8 删除内容守恒（普通文件 sha256 / SQLite 逻辑摘要）、
M9 已有 DB 逻辑零变化、M10 幂等、M11 旧 CLI 退出码 0 + 零写入、M12 旧二进制
仍能看到迁移后的会话/colony、M13 旧 `serve` health + 零用户数据写入。

**churn 白名单**（非用户数据，允许变化）：`logs/**`、`maintenance/server.lock`、
`*.db-wal/-shm/-journal`、`credentials/metadata/index.json`、`**/summary.json`
（读缓存）。白名单之外的任何修改都判 FAIL。

**自测**：`bash go_hive/scripts/upgrade_compat_check.sh --self-test` 用合成夹具
注入 `health=500`、用户文件被修改、删除未守恒、旧二进制写入四类回归，断言判定
确实 FAIL（防止门禁退化成装饰）。

### 6.2 契约自洽门禁（`make compat-check`）

`check_compat_contracts.py` 的 C1–C11 见 §1 表；纯 stdlib、离线、不依赖项目
venv；`make ci` 已引用。`freeze_* --check` 的逐字节重生成不进 `ci`：冻结产物
记录读点行号，代码移动即 STALE（当前 main 上 env/config 清单即因行号漂移而
STALE，语义面无变化）；语义漂移由 C1–C3 捕获。

**C11（CLI 外观逐 slot）**：用 `git archive` + 本地 `go build` 把契约里记录的
基线提交（`contracts/cli-surface.json` 的 `meta.baseline_commit`）复现成旧构建，
再构建当前树作为新构建，逐 slot 核对 `hive --help`、`hive-browser --help`、
`hive mcp --help`、`hive-cred --help` 的 usage 行，以及契约里冻结的命令路径
集合与每路径 flag 集合：**旧构建 vs 新构建**、**新构建 vs 契约基线**、
**新构建 vs Python** 任一漂移即 FAIL（非 0 退出）；`hive-cred` 的 usage 行同时与
Python 侧对照。自检 `make compat-cli-selftest`（`--self-test`）拿契约副本注入
「删一个 flag」「改一个 usage 行」两类合成漂移，必须判定 FAIL、基线副本不得
误报。全量重采/退出码探针仍由 `cli_surface_contract.py --check`（`make
cli-surface-check`，发布时跑）负责；C11 是每次提交的廉价常设门禁。

---

## 7. 未覆盖边界（本政策的诚实边界）

- 只验证 **darwin/arm64 宿主机**（与 G2 一致）；Windows/Linux 实机、Windows
  归档的升级由发布面（`RELEASE_CHECKLIST`）与 CI 覆盖，未纳入本门禁。
- 夹具是**形状真实**的合成 home（会话/凭据/colony/tracker），不是真实用户数据
  长尾：超大 parts、损坏 JSON、权限错误、真实 OAuth token 不在其中。
- 迁移链与读 API 用真实二进制验证；**真实 LLM 会话**与 UI 交互不覆盖。
- slot 门禁比较“上一 slot → 新 slot”；跨多个 slot 的跳跃升级依靠存储迁移链
  （v1/v2 夹具）覆盖，不逐 slot 重放。
- C11 的旧构建由契约记录的基线提交在本地复现；浅克隆（基线提交不在本地）或
  本机缺 `go`/`git`/`python hive` 时 C11 明确 SKIP 并注明未验证，不猜绿。

---

## 8. 变更记录

| 日期 | 变更 |
| --- | --- |
| 2026-11-21 | 首版：把 G2 一次性证据升级为常设门禁（三夹具升级兼容测试器、slot 联动、`make compat-check` 进 `ci`）；首次实测见 `e2e-assets/upgrade-compat/REPORT.md` |
| 2026-11-21 | C11：CLI 外观逐 slot 接入 `make compat-check`（契约基线旧构建 vs 当前新构建 vs Python，usage 行 + 命令路径/flag 集合；任一漂移 FAIL）；新增 `--self-test` 合成漂移自检；`docs/cli-surface-diff.md` 与 `docs/delivery-governance.html` 同步声明 |