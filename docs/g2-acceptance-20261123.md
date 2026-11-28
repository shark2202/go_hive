# G2 验收复核：HEAD 42f2b932（2026-11-23）

> 范围：在当前 main（`42f2b932`）重新复核 G2 无损替换，并关闭 P0-G2-1 的文档状态。
> 工作树：`/Users/mac/hive/.wt/w91-g2-acceptance`（分支 `w91/g2-acceptance`）。
> 本文只记录本次实测；旧报告（`docs/g2-lossless-migration.md`、`go_hive/docs/upgrade-and-rollback.md`）保留其原始日期与数字。

## 1. 结论

**ACCEPTED（G2 数据级无损替换在本 HEAD 复核通过）。**

| 验收面 | 本次结果 | 判定 |
| --- | --- | --- |
| 非空 v1 旧 home 双跑 | 50/50 文件；47 逐字节相同 + 3 个 SQLite 按结构比较；语义差异 0 | PASS |
| 非空 v2 旧 home 双跑 | 99/99 文件；95 逐字节相同 + 1 个仅键序差异 + 3 个 SQLite；语义差异 0 | PASS |
| JSON 字段 / SQLite schema+行 / 读探针 / API 探针 | 0 / 0 / 0 / 0 差异；启动迁移树一致 | PASS |
| 凭据双向互读 | 四方视图一致（Python 自读、Go 自读、Go 读 Python store、Python 读 Go store） | PASS |
| 交叉消费 | Python 迁移后 home 交给 Go 迁移不重写任何文件；双向读 diff 0 | PASS |
| Go→Python→Go 回滚 | Python CLI/API/`serve` 可读；0 删除、0 覆盖；Go 二次启动幂等 | PASS |
| 安装/升级演练 | v1.0.25 v2 home → HEAD 构建启动即迁移；旧二进制回读 0 写入；`install.sh` 四二进制 + SHA256；归档自验 13/13 | PASS |
| P0-G2-1 文档状态 | 已按 D2 **决策 A** 同步；未改产品 exports 行为 | PASS |

无 G2 blocker。已知残余与未验证边界见 §6/§7。

## 2. 冻结与工作树状态

- HEAD：`42f2b93215edddcd3f7f9ef5ca246162753e6d7b`（`42f2b932`，current main）
- 分支：`w91/g2-acceptance`
- 运行前工作树：干净（`git status --porcelain` 为空；见 `e2e-assets/g2-acceptance-20261123/head-freeze.txt`）
- 运行后变更仅限本 lane 的文档与证据目录；未改产品代码。

## 3. 命令与退出码

```bash
# 1) 冻结 HEAD / 记录干净状态
git rev-parse HEAD && git status --porcelain=v1
# exit 0（输出为空 = 干净）

# 2) G2 主证据：非空 v1/v2 双跑（迁移、字段/SQLite、读、API、凭据、交叉消费）
UV_NO_SYNC=1 bash go_hive/scripts/g2_home_e2e_run.sh \
  --work /Users/mac/.hermes/profiles/omh-tui/cache/scratch/g2-home-e2e-rerun
# exit 0；summary: GREEN

# 3) 回滚 + 安装/升级演练
UV_NO_SYNC=1 FRONTEND_DIST=/Users/mac/hive/go_hive/go-hive/frontend/dist \
  bash go_hive/scripts/g2_rollback_drill.sh \
  --work /Users/mac/.hermes/profiles/omh-tui/cache/scratch/g2-rollback-rerun
# 首次调用 exit 1（scratch 目录尚未创建，脚本 cd 失败）；mkdir 后重跑 exit 0

# 4) G2 相关定向单测（非全量 CI）
cd go_hive/go-hive && GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off \
  go test ./internal/server -run TestStorageStartup -count=1
# exit 0

cd go_hive/go-hive && GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off \
  go test ./internal/storage ./internal/tracker -count=1
# exit 0
```

依赖：本机 Go `go1.26.11`；Python 参考实现用仓库 venv（Python 3.11.16，`core/` 源码）+ `UV_NO_SYNC=1`；Go 构建 `GOPROXY=off`。夹具为脚本生成的合成非空 home，**未使用 HTTP mock**；API 证据来自真实 `hive serve` 二进制。凭据为固定 Fernet key 写入的**假值**，只验证格式/互读，不代表真实第三方 token 可用。

## 4. G2 主证据（`g2_home_e2e_run.sh`，exit 0）

证据目录：`docs/e2e-assets/g2-home-e2e/`（本次原地刷新；`summary.json`/`summary.md` 与 2026-11-21 版本逐字节相同）。

| 夹具 | 文件 py/go | 逐字节相同 | 排版级差异 | 语义差异 | 单侧文件 | JSON 字段 | SQLite | 读探针 | API | 凭据 | 交叉消费 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| v1-rich | 50 / 50 | 47 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 一致 | 干净 |
| v2-rich | 99 / 99 | 95 | 1 | 0 | 0 | 0 | 0 | 0 | 0 | 一致 | 干净 |

- v2 唯一字节差异：`colonies/alpha/worker.json` 的 `input_data.binding` 键序（R-G2-1），解析后字段/值一致，交叉消费不重写。
- 3 个/侧的 `*.db` 按设计字节不同（SQLite 页布局，R-G2-2）；schema/行/触发器/`user_version` 全等。v1 的 3 个 tracker DB 里 `_tracker_meta.updated_at` 原始值因两侧迁移相隔约 1 秒而不同（`rows_equal_raw=false`），归一化时间戳后逻辑行相等；不是数据差异。
- 凭据四方视图：`demo_api_key`、`demo_oauth` 的 key/值/`expires_at`/metadata 一致；Go 读 Python store、Python 读 Go store 均通过。
- 交叉消费：Go 对 Python 迁移后的 home 再跑迁移，manifest 前后逐文件相同；双向读 diff 0/0/0。
- API：真实 Python/Go `serve` 在未迁移夹具上启动，`/api/discover`、`/api/colonies/<c>/data/{tables,rows,changes}` 归一化后 0 差异；启动迁移树文件集合一致（v1 50、v2 99）。
- P0-G2-1 证据复现：Python `serve` 以 `CWD=core/` 启动时，home 的 `exports/` 未复制进 `colonies/`，但 `.migrated-v2` 已写（`exports_migrated: false`）；Go 锚定 `$HIVE_HOME/exports`。

## 5. 回滚与安装/升级（`g2_rollback_drill.sh`，exit 0）

证据：`e2e-assets/g2-acceptance-20261123/run.txt`、`rollback/`、`upgrade/`。

### 5.1 Go→Python→Go 回滚

| 步骤 | 实测 | 写入 |
| --- | --- | --- |
| v2 夹具（54 文件）→ Go `serve` #1 + Go 写探针 | health 200；`.migrated-v2`/`.migrated-v3`/`colony_sessions_v1`；tracker schema 3 | `D0`：+59 / -40 / 0；sqlite_changed 6（迁移搬移） |
| Python CLI 冷读 | 2 条（迁移 DM + Go 探针，message_count 2/1）；queen/colony 可读 | `D1a`：+2（summary 缓存） |
| Python API 读回 | 历史/凭据（`demo_api_key`/`demo_oauth`）/tracker 均可读 | `D1b`：+4（summary 缓存） |
| Python `serve`（同 home） | health 200、`/api/sessions` 200；marker present→absent | `D2`：modified 1（共享日志追加） |
| Go CLI 冷读 | session_id/message_count/queen_id 与 Python 一致 | `D3`：0 变化 |
| Go `serve` #2 | health 200；marker present→absent | `D4`：modified 1（日志追加），幂等 |

破坏性写入：**0 删除 / 0 覆盖**；仅 `summary.json` 读缓存、`logs/hive-server.log` 追加、`maintenance/server.lock` 生命周期文件（自清）。

### 5.2 升级 / 安装

| 步骤 | 实测 |
| --- | --- |
| 旧二进制 `v1.0.25-go.1` 在 v2 home | health 200；`session list --cold` 为空（旧版按 v3 路径读） |
| HEAD 构建在旧 home 首次启动 | 启动迁移：`+45 / -40 / modified 0 / sqlite_changed 5`；health 200、内嵌 `/` 200；会话 `session_20260201_000001_bbbbbbbb`（2 条消息）可读 |
| 升级后旧二进制回读 | health 200；能读同一会话；`DU2`：0 added / 0 removed / 0 modified / 0 sqlite_changed |
| 新二进制二次启动 | health 200、`/` 200；仅日志追加（幂等） |
| `install.sh` 原地升级 | 1.0.25 → 构建产物；四二进制 `hive`/`hive-cred`/`hive-browser`/`browserbridge`；SHA256 校验；安装目录 `serve` health 200、`/` 200 |
| 归档 | `hive-1.0.26-go.1-darwin-arm64.tar.gz`，36,059,855 B，SHA256 校验通过；`verify_release_archive.sh` **13/13 PASS** |

口径说明：`g2_rollback_drill.sh` 硬编码 release 标签 `1.0.26-go.1`；本 HEAD 构建出的二进制自报 `hive 1.0.27-go.1`（`upgrade/output/release-artifact.txt`）。本演练实际验证的是「v1.0.25 旧 home → 当前 HEAD 构建」的升级方向与安装脚本行为，归档标签不影响数据面断言；这是演练脚本的标签遗留，已登记为 observation，不是 G2 blocker。

## 6. P0-G2-1：决策 A 与文档同步

**决定（`pending-decisions.md` §D2）**：保持 Go 的 `$HIVE_HOME/exports` 基准；登记与 Python `CWD/exports` 的差异；**不改产品 exports 行为**。发布说明写明 v1 用户先把旧 `exports/` 放到 `$HIVE_HOME/exports`。

本次同步的文档（仅文档，不改产品代码）：

| 文件 | 变更 |
| --- | --- |
| `go_hive/docs/g2-lossless-replacement.html` | 开头/结论 callout/P0 行从「待用户决策」改为「已决策 A」；F4 从「待决策」改为「已登记（两侧对齐、无需修）」；补复核日期与验收链接 |
| `docs/g2-lossless-migration.md` | §6 标题与 P0 判定改为「已决策 A」；顶部补 2026-11-23 复核记录 |
| `go_hive/docs/support-matrix.md` | §1.1 P0 行改为「已决策 A」；F4 改为「已登记」；补复核日期 |
| `go_hive/docs/releases/v1.0.27-go.1.md` | 新增「迁移口径：v1 `exports/`」：基准、升级前放置指引、决策记录 |
| `go_hive/docs/CHANGELOG.md` | 1.0.27 摘要补一句迁移口径 |

决策依据与实测：`go_hive/docs/exports-baseline-decision.md`（不要复刻 Python CWD 语义；home 锚定更可预测、不会静默导入任意 CWD 的 `exports/`）、本次 `v1-rich-serve-cwd.json`。**未改 `exports/` 读取逻辑**；`go_hive/docs/g2-lossless-replacement.html` 的 F4 状态同步为已登记（此前与 `pending-decisions.md`「无待拍板项」和 `exports-baseline-decision.md` §7「行为对齐、无需修」不一致）。

## 7. 残余与未验证边界

### 残余（不阻塞）

- **R-G2-1**：`worker.json` 的 `input_data.binding` 键序（排版级，字段/值一致）。
- **R-G2-2**：新建/重建 SQLite DB 字节不同（页布局；按 schema+行+触发器验证）。
- **R-G2-3/4**：`/api/health.resources` 运行期指标；读探针归一化时间戳/epoch（非存储事实）。
- **R-G2-5**：`provider_id` Python `null` vs Go `""`（空值语义，回写不丢）。
- **F4**：`colonies/.migrations/` 被 `hive colony list` 当作 colony；两侧行为对齐，已登记无需修。若未来产品决定过滤点目录，必须两侧联合改。
- **F5/F6**：迁移前旧二进制看不到 v2 布局会话（升级即迁移；迁移后旧二进制可读）；CLI JSON 数字序列化差异（值相等）。
- **演练标签**：如 §5.2 所述，回滚脚本 release 标签硬编码 1.0.26，而 HEAD 二进制自报 1.0.27。

### 未验证（不冒充通过）

- Windows/Linux 实机；本次仅 macOS arm64。
- 真实用户数据长尾（超大 parts、损坏 JSON、权限错误）。
- 真实第三方 OAuth 刷新/provider token；G2 凭据是假值，格式兼容 ≠ 第三方可用。
- sessions/history 的 HTTP 端点抓取（存储层读探针覆盖同一数据）。
- 前端交互回归（仅验证 `/` 200 与静态资产可服务）。
- 本 lane 未跑全量 `make ci`。

## 8. CI 口径

按派工单**未跑全量 `make ci`**。有记录的全量 CI 与代码点如下：

- **最近有记录的全量 CI**：`wave-84 @ 88f05ff7`，`ci=0 / qc=0 / publish=0`（2026-11-23 12:38；`go_hive/docs/reports/ci-latest.json`）。
- **slot 20261123-22（生成于 `cea04d9e`）**：`check_parity: OK — 5979 个测试全部带 parity 注解或豁免说明`（`go_hive/docs/reports/slot-20261123-22.md`）。该报告的全量 CI 行仍指向 `88f05ff7`，**不是**在 `cea04d9e` 新跑的全量 CI；`5979` 是 parity 注解统计，不是全量 CI 测试数。
- **代码点**：HEAD `42f2b932` 比 `cea04d9e` 多 2 个提交（均为 reports）；产品代码与 `cea04d9e` 相同。`88f05ff7..HEAD` 之间 G2 相关包有变更（如 `internal/cli/loader.go`、`internal/server` 的 chat 路径），因此**不能把 wave-84 的全量 CI 当作 HEAD 的全覆盖**；本次以 HEAD 上直接重跑的 G2 E2E 为主证据，并补跑 `TestStorageStartup`、`internal/storage`、`internal/tracker` 定向测试（均 exit 0）。

## 9. 证据索引

- 主证据（本次原地刷新）：`docs/e2e-assets/g2-home-e2e/`（`summary.json`、`summary.md`、`commands.md`、tree/field/sqlite/read/api/credentials/cross diff）。
- 本次验收证据：`go_hive/docs/e2e-assets/g2-acceptance-20261123/`
  - `summary.json`（accepted/head/cases/evidence/residuals/ci）
  - `head-freeze.txt`
  - `run.txt`（回滚 + 升级全流程 stdout）
  - `rollback/snap/`、`rollback/output/`
  - `upgrade/snap/`、`upgrade/output/`
