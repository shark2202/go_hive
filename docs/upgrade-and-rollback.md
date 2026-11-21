# 升级与回滚（G2）：Go↔Python 无损替换、安装/升级演练

> **2026-11-21 起升级为常设门禁**：本文是一次性演练证据；后续每个交付由
> `scripts/upgrade_compat_check.sh`（v1/v2/v3 夹具、旧二进制 0 写入回读、slot 联动）
> 与 `make compat-check`（契约自洽，已进 `make ci`）持续验证。政策、破坏性变更
> 定义、迁移/弃用窗口与发布检查清单见 **`compatibility-policy.md`**；首次门禁实测
> 见 `e2e-assets/upgrade-compat/REPORT.md`。

- 基线：`main` `13e6a4bb`；工作树 `.wt/g2-rollback-install`
- 被测版本：Go `1.0.26-go.1`（HEAD 构建）；上一发布 `v1.0.25-go.1`（用于“老用户”）
- 证据目录：`docs/e2e-assets/g2-rollback/`（快照 manifest、逐步 diff、命令输出、归档校验）
- 一键复现：`bash go_hive/scripts/g2_rollback_drill.sh --work <scratch>`（`--skip-upgrade` 只跑回滚段）
- 关联证据：`docs/storage-contract-diff.md`（迁移等价/凭据互读）、`RELEASE_CHECKLIST.md`（六平台归档与发布面）

本文回答两个问题：

1. 一个已经被 Go 版本迁移到 v3 的 `HIVE_HOME`，回滚到 Python 版本后还能不能完整读取？读的过程会不会破坏数据？
2. 一个 v1.0.25 老用户（v2 布局 home）升级到新二进制、以及用 `install.sh` 原地升级，是否安全？

---

## 1. 结论摘要

| 验收项 | 结论 | 关键证据 |
| --- | --- | --- |
| Go→Python 回滚读取（会话列表/历史/凭据/colony/tracker） | **通过**：Python CLI 与 Python 运行时 API 都能读取 Go 迁移/写出的同一 home；会话 ID、消息数、凭据明文、tracker schema 全部一致 | §3.2、`rollback/output/py-readback.json`、`rollback/output/py-session-list.json` |
| 回滚的破坏性写入 | **未发现**：0 删除、0 覆盖既有文件；仅新增 `summary.json` 缓存（两侧同构）与追加 `logs/hive-server.log` | §3.3、`rollback/snap/D1a-*.json`、`rollback/snap/D1b-*.json`、`rollback/snap/D2-*.json` |
| Go 二次启动幂等（Python 读过之后） | **通过**：除自身日志追加外，全 home 零变化；markers 不重写、tracker 不重建 | §3.4、`rollback/snap/D4-go2.json` |
| 老用户升级（v1.0.25 v2 home → 1.0.26 新二进制） | **通过**：启动即完成 v2→v3 迁移，health 200、内嵌 dashboard `/` 200；迁移前旧二进制看不到的会话在升级后可读 | §4.2、`upgrade/snap/DU-upgrade-migration.json` |
| 升级后二进制回退（新迁移 home → v1.0.25 二进制） | **通过**：旧二进制在迁移后 home 上健康、能读到迁移后的会话，且 **0 写入** | §4.3、`upgrade/snap/DU2-old-binary-reads.json` |
| `install.sh` 安装/原地升级（四二进制） | **通过**：SHA256 强校验；四二进制落盘；`hive --version` = `1.0.26-go.1`；安装目录 `serve` health 200、`/` 200 | §4.4、`upgrade/output/install-sh.log.txt` |
| 归档自验证 | **通过**：`verify_release_archive.sh` **13/13 PASS**（修复了下述脚本解析缺陷后） | §4.5、`upgrade/output/verify-release-archive.txt` |
| serve 生命周期中的 janitor `server.lock` | **通过**：Go serve 现在与 Python 一致——启动写 `maintenance/server.lock`（pid 为本进程），优雅退出清除；非默认端口下的离线 janitor 保护不再依赖端口探测 | §5-F7、`rollback/output/go-serve-1.log.probe.txt` |

未发现需要“回滚迁移”或修复数据的情况：回滚方向是只读的，两个方向都没有数据损坏。

---

## 2. 数据布局与读取契约（本次演练的范围）

v3 home（Go 与 Python 相同）：

```
$HIVE_HOME/
  .migrated-v2 / .migrated-v3            # 迁移标记（写入后不再重跑）
  queens/<queen_id>/{profile.yaml,sessions/<sid>/{meta.json,state.json,events.jsonl,conversations/parts/*.json,summary.json}}
  colonies/<name>/{metadata.json,tracker/tracker.db,queens/<q>/sessions/<sid>,workers/…}
  colonies/.migrations/colony_sessions_v1  # colony fork 归并标记
  credentials/**, secrets/credential_key   # Fernet 加密凭据 + 密钥
  logs/hive-server.log                     # 追加式服务日志（两实现共用路径）
  mcp_registry/, memories/, cache/, …      # 其余运行期目录
```

读取面（本次逐项验证）：

- **会话列表**：`queens/*/sessions/*`（DM）与 `colonies/*/queens/*/sessions/*`（colony overseer）；迁移后 v2 的 `agents/queens/<q>/sessions/*` 会被分流到这两处。
- **历史**：`conversations/parts/*.json`（`FileConversationStore.read_parts`）、`events.jsonl`、`state.json`。
- **凭据**：`credentials/*.enc` + `secrets/credential_key`（Fernet），`credentials/metadata/index.json` 为索引。
- **colony/tracker**：`colonies/<c>/metadata.json`、`colonies/<c>/tracker/tracker.db`（`_tracker_meta.schema_version`、注册表、变更日志）。
- **缓存副作用**：`summary.json` 是两侧共有的“冷会话列表”缓存，缺失/过期（`conversations/parts` mtime 更新）时由读路径重建；不是用户数据。

---

## 3. 回滚演练：Go 迁移后的 v3 home → Python

### 3.1 方法

1. 用 `go_hive/scripts/storage_compat_gen_fixture.py --version v2` 生成真实形状的 v2 旧 home（54 个文件：queen profile/sessions、colony worker 子树、`colonies/<c>/data/tracker.db`、post-v3 colony_fork 残留等）。
2. **Go 跑一轮**：当前二进制 `hive serve`（启动时执行 v2→v3→colony_sessions→tracker ensure），随后用 Go 生产写入 API 写一个会话（state/conversations/checkpoints）与两条凭据（`demo_api_key`、`demo_oauth`）。
3. 每个阶段之间对整 home 做 manifest 快照：文件级 sha256+size、SQLite 逻辑行摘要（表/行数/行哈希）、迁移 markers。用 `g2_rollback_probe.py diff` 归类 added/removed/modified。
4. **Python 读**：`hive session list --cold --json`、`hive queen list/sessions`、`hive colony list`，再用 Python 运行时 API（`SessionManager.list_cold_sessions`、`FileConversationStore`、`EncryptedFileStorage`、tracker）逐项读取并落 JSON；最后真实启动 Python `hive serve` 对同一 home 做 health/`/api/sessions` 读取。
5. **Go 二次启动**：确认幂等。

### 3.2 回滚矩阵

| # | 动作 | 读取面 | 相对上一快照的写入 | 判定 |
| --- | --- | --- | --- | --- |
| S0 | 生成 v2 夹具 | 54 文件 | — | 起点 |
| 1 | Go `serve` #1 + Go 写探针 | 启动迁移：`.migrated-v3`、`colony_sessions_v1`；`tracker_db: ensured 3`；health **200**；运行期写 `maintenance/server.lock`（`marker=present`），SIGTERM 后 `marker-after=absent`；迁移/写出会话 + 凭据 | `+59 / -40 / modified 0`（`agents/` 按设计搬移；tracker 由 `data/` → `tracker/`） | 符合迁移契约 |
| 2 | Python CLI 读 | `session list --cold` 2 条（迁移的 DM + Go 写探针），消息数 2/1；`queen sessions`、`colony list` 正常 | `+2`（两处 `summary.json` 缓存），modified 0 / removed 0 | **不破坏** |
| 3 | Python API 读 | DM 历史 2 条 part（`hello`→`hi there`）；Go 探针会话 3 parts；colony `alpha/beta/probe_colony` tracker `schema_version=3`；凭据 `demo_api_key=sk-demo-123`、`demo_oauth` 的 token/过期/元数据全部解出 | `+4`（colony 会话的 `summary.json`），modified 0 / removed 0 | **不破坏** |
| 4 | Python `serve`（同 home） | health **200**、`/api/sessions` **200**；启动无迁移告警（markers 生效）；运行期 `marker=present {"pid": …}`，优雅退出后 `marker-after=absent` | `modified 1`：`logs/hive-server.log` 追加（Go serve 创建的文件，Python 只 append，未截断）；marker 自写自清，净变化 0 | **不破坏** |
| 5 | Go CLI 读 | `session list --cold` 与 Python 输出逐字段一致（session_id/message_count/queen_id 全等）；读取 Python 写的 `summary.json` 不重写 | **0 变化** | **不破坏（双向缓存兼容）** |
| 6 | Go `serve` #2 | health **200**；marker 同步骤 1（present → 退出后 absent） | `modified 1`：同一日志文件追加（marker 退出后已清除，无残留） | **幂等** |

补充：把预构建的 dashboard 放到 plain 二进制的 CWD（`frontend/dist`）再起 serve，`/` 从 404 变为 **200**，而数据面仍只有日志追加（`rollback/snap/D5-go-ui.json`）。因此 **stage-1 的 `/` 404 是构建形态/环境前提**（plain `go build`，无 `-tags embed_ui`，且 CWD 无 `frontend/dist`），不是路由或数据问题；归档里的 embed 二进制 `/` 为 200（§4.2、§4.4）。

### 3.3 读路径写入清单（逐条）

| 写入 | 类型 | 证据 | 风险 |
| --- | --- | --- | --- |
| `summary.json`（6 个） | 读缓存，缺失/过期时重建；两侧同一 schema/过期规则 | `D1a`（+2）、`D1b`（+4）；`D3` 中 Go 读后 0 写入 | 低：不覆盖用户数据；Go/Python 互认 |
| `logs/hive-server.log` 追加 | 服务日志（两实现共用路径、append-only） | `D2`、`D4`；Python 与 Go 行格式不同但互不覆盖 | 低 |
| 迁移搬移（`agents/` → `queens/`、`colonies/...`） | 仅由**新版本**启动触发；marker 幂等 | `D0`（-40/+59，modified 0） | 预期行为（v3 契约） |
| `maintenance/server.lock` | serve 生命周期文件：启动写入本进程 pid，优雅退出删除（Python `write_server_marker`/`clear_server_marker` 的同一语义；本 lane 修好 Go 的接线，见 F7） | `go-serve-*.log.probe.txt` 的 `marker=present` / `marker-after=absent`；快照净变化 0 | 低：pid-scoped 清除，不误删他人 |

**没有**观察到：会话目录/凭据/tracker 的删除或覆盖；markers 被重写；SQLite 行内容变化（读阶段 `sqlite_changed=0`）。

### 3.4 Go 二次启动幂等

第 6 步与第 5 步相比只有日志追加（`rollback/snap/D4-go2.json`：added 0 / removed 0 / modified 1=log），markers 保持不变，tracker 未重建。连续启动不会重复迁移或重写任何用户文件。

---

## 4. 安装/升级演练

### 4.1 归档构建（`scripts/release.sh`）

```bash
VERSION=1.0.26-go.1 DIST=<work>/dist RELEASE_DIR=<work>/dist/release \
  PLATFORMS=darwin/arm64 FRONTEND_DIST=<prebuilt dashboard> \
  bash go_hive/scripts/release.sh
```

实测（darwin/arm64）：`hive-1.0.26-go.1-darwin-arm64.tar.gz`，**28,209,800 bytes**（含内嵌 dashboard；tar 带文件时间戳，大小/哈希随构建微变）；`SHA256SUMS` 校验通过；归档内 `hive/hive-cred/hive-browser/browserbridge` 四二进制 + `LICENSE/NOTICE/README/SHA256SUMS/THIRD_PARTY_NOTICES`，包内 `SHA256SUMS` 复核全 `OK`。六个平台归档的完整构建属 `RELEASE_CHECKLIST` 面，本演练只取宿主平台以减少噪声。

### 4.2 老用户升级：v1.0.25 v2 home → 1.0.26 新二进制

“老用户”用 `git archive v1.0.25-go.1` 现场构建的 `hive 1.0.25-go.1`，在一个 v2 夹具 home 上真实 `serve` 过一轮（旧版本不在启动链里跑迁移）：

| 步骤 | 实测 |
| --- | --- |
| 旧二进制 serve（旧 home） | health **200**；`session list --cold` 为空（v2 的 DM/overseer 会话在 `agents/queens/…`，旧版本按 v3 路径读） |
| 新归档二进制 `--version` | `hive 1.0.26-go.1` |
| 新二进制 serve（同一 home） | 启动即迁移：`.migrated-v3`、`colony_sessions_v1` 写入；`tracker_db: ensured 3`；health **200**；内嵌 dashboard `/` **200**；`/api/sessions` 200；运行期 `maintenance/server.lock` 写入、退出清除 |
| 升级后会话可读 | `session list --cold` 出现迁移后的 DM 会话 `session_20260201_000001_bbbbbbbb`（message_count=2，last_message=`hi there`）；`queen sessions queen_growth` 同步可读 |
| 升级写入 | `+45 / -40 / modified 0`（`upgrade/snap/DU-upgrade-migration.json`，v2→v3 布局搬移） |

### 4.3 升级后回退二进制（新 home → v1.0.25）

在**已迁移**的 home 上运行旧二进制：

- `session list --cold` 能读到迁移后的会话（与新版一致），`serve` health **200**；
- 全 home 快照 **0 added / 0 removed / 0 modified / 0 sqlite changed**（`upgrade/snap/DU2-old-binary-reads.json`）。

结论：二进制回退到上一发布版本不会破坏或重写 v3 home；但**布局迁移本身只由新版本触发**——若用户从未启动过新版本，旧版本看不到 v2 会话（§4.2 第一行），这是铺垫好的文档前提，不是损坏。

### 4.4 `install.sh` 安装 / 原地升级

```bash
# 模拟已安装旧版：先把 v1.0.25 的 hive/hive-cred 放进安装目录
bash go_hive/install.sh --from <work>/dist/release --version 1.0.26-go.1 --dir <install-dir>
```

实测输出（节选）：

```
install: using local archive: …/hive-1.0.26-go.1-darwin-arm64.tar.gz
install: verified SHA256: hive-1.0.26-go.1-darwin-arm64.tar.gz
install: verified archive contents
install: installed hive / hive-cred / hive-browser / browserbridge -> <install-dir>
hive 1.0.26-go.1
install: verified hive-cred --help
install: verified hive-browser help
install: verified browserbridge -version
```

- 原安装目录里的 `hive 1.0.25-go.1`、`hive-cred` 被新版本覆盖，缺失的 `hive-browser`/`browserbridge` 补齐（四件套同目录，满足 `serve` 的 sibling 解析）。
- 安装目录直接 `serve`：health **200**、内嵌 dashboard `/` **200**。

### 4.5 归档自验证

`bash go_hive/scripts/verify_release_archive.sh <release-dir>`：解压归档 → 无 `HIVE_BROWSERBRIDGE_BIN` 启动 `serve` → 断言 sibling `browserbridge` 被解析、启动、可健康探测、SIGTERM 后回收。

- 修复前：**FAIL** 1/11（`no live bridge child process`，另有 2 项被跳过）。
- 修复后：**13/13 PASS**（本目录 `upgrade/output/verify-release-archive.txt`）。

原因与修复见 §5-F1。

---

## 5. 发现的问题（含风险等级与处置）

| ID | 类别 | 风险 | 现象/证据 | 处置 |
| --- | --- | --- | --- | --- |
| F1 | 验证脚本与 serve 输出漂移 | **低**（不影响运行时/数据，只让归档验证误报） | `serve` 的桥就绪行已变为 `(pid N, viewport scale S)`，而 `verify_release_archive.sh` / `e2e_browser_host.sh` 仍按 `(pid N)` 解析 PID，导致 `no live bridge child process` 假失败（11 检查中 1 FAIL、2 跳过） | **已修复**：两个脚本的 sed 改为 `(pid \([0-9][0-9]*\).*`；复跑 13/13 PASS |
| F2 | 读路径缓存写 | **低** | Python 冷会话读取会在 Go home 中新增 6 个 `summary.json`；Go 读 Python 写的同一文件不重写（D3=0 变化） | 无需修复：两侧同 schema、同过期规则、缺失才重建；不覆盖用户数据 |
| F3 | 共享日志追加 | **低** | Python `serve` 与 Go `serve` 都写 `logs/hive-server.log`，行格式不同但 append-only（295→1366→1661 bytes） | 无需修复；文档说明（日志消费者按行前缀区分） |
| F4 | 点目录被当作 colony | **低** | `colonies/.migrations/` 会被 `hive colony list` 列为名为 `.migrations` 的 colony（Python 侧同样行为，Go 逐字对齐）；存在被误 `colony delete .migrations` 的展示层风险 | **未在本 lane 修**：若只改 Go 会破坏 Python 对齐；建议在统一 parity 决策后两侧一起过滤点目录（后续 lane） |
| F5 | 旧版本读不到未迁移会话 | **中**（用户感知，非数据损坏） | v1.0.25 在 v2 home 上 `session list --cold` 为空；新版本启动迁移后可读，且旧二进制在迁移后仍可读（§4.2/4.3） | 文档化前提 + 升级即迁移；无需代码修复 |
| F6 | CLI JSON 数字序列化 | **低**（值相等，严格字符串比对会差） | 同一 `created_at`：Python `1767225600.0`，Go `1767225600`（`colony list` 输出） | **未修**：属值级 parity lane 的序列化口径；不影响读取 |
| F7 | Go serve 未维护 janitor `server.lock` | **中**（离线 janitor 的在线互斥保护缺口） | Python `create_app` 的 on_startup/on_shutdown 会写/清 `maintenance/server.lock`；Go 只有 `maintenance.WriteServerMarker/ClearServerMarker` 与测试，**serve 启动链没有调用**。回滚演练中 Python serve 写出了该文件而 Go serve 没有（run4）；非默认端口/桌面临时端口下，离线 `hive janitor --execute --tiers 2,3` 可能误判“无在线运行时” | **已修复**：`internal/cli/loader.go` 在 listener 绑定成功后 `WriteServerMarker(home, nil)` 并 `defer ClearServerMarker(home)`（与上游一致、失败不影响启动）；实测 `marker=present` → `marker-after=absent` |

没有任何一项达到“回滚会丢数据/需要手工修数据”的级别。

---

## 6. 未覆盖 / 后续

- **真实旧 HIVE_HOME 全量双跑**（G2 的 `old-home-e2e`）不在本 lane：本演练用可生成的 v2 夹具 + 真实旧二进制，覆盖布局/会话/凭据/tracker；真实用户数据的长尾（自定义 skills、memory 布局差异等）由该 lane 继续。
- **Windows 实机 / GitHub Actions**：本演练只做 darwin/arm64 宿主（六平台构建与 Actions 属 G3/发布面）。
- **真实 LLM 会话**：回滚读取用 Go 生产写入 API 写出的完整会话（state/conversations/checkpoints），未触发真实模型调用；`meta.json` 缺省是写探针的已知形状（两侧探针一致），生产 `SessionManager.create_session` 的 meta 由会话层写入（其单测/ parity 覆盖）。
- **UI 交互**：只验证 `/` 200 与静态资产可服务，未做浏览器端交互回归。

---

## 7. 复现

```bash
# 全量（回滚 + 升级/安装），约 6–8 分钟，产物在 <work>/{logs,snap,bin,bin-old,dist,install-bin,upgrade}
bash go_hive/scripts/g2_rollback_drill.sh --work /tmp/g2-rollback

# 只跑回滚段
bash go_hive/scripts/g2_rollback_drill.sh --work /tmp/g2-rollback --skip-upgrade

# 老版本 tag 可换（默认 v1.0.25-go.1）
OLD_TAG=v1.0.25-go.1 bash go_hive/scripts/g2_rollback_drill.sh --work /tmp/g2-rollback

# 无预构建 dashboard 时自动退化为 --no-embed（归档无内嵌 UI，`/` 404 属预期）
FRONTEND_DIST=/path/to/frontend/dist bash go_hive/scripts/g2_rollback_drill.sh --work /tmp/g2-rollback
```

证据文件命名：`snap/S*.json`（各阶段 manifest）、`snap/D*.json`（逐步 diff）、`snap/U*.json`（升级段）、`logs/*.probe.txt`（health/root/api 实测）、`logs/py-readback.json`（Python 读回明细）。
