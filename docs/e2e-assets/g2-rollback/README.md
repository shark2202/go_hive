# G2 证据：Go→Python 回滚与安装/升级演练

- 基线：`main` `13e6a4bb`（工作树 `.wt/g2-rollback-install`）
- 主机：macOS `darwin/arm64`；Go `1.0.26-go.1`（HEAD）；旧版 `v1.0.25-go.1`；Python 参考实现（`core/`）
- 生成脚本：`go_hive/scripts/g2_rollback_drill.sh`（本目录 `run.txt` 是一次完整运行记录，EXIT=0）
- 读取/快照探针：`go_hive/scripts/g2_rollback_probe.py`（`manifest` / `read` / `diff`）
- 结论文档：`go_hive/docs/upgrade-and-rollback.md`

SQLite 快照是按“表/行数/行内容哈希”做的逻辑摘要（不落原始行）；`home` 绝对路径属于运行时刻的 scratch 目录，diff 只比较相对路径与内容。

## 目录

```
rollback/snap/S0…S4, D0…D5         # 回滚段：各阶段 manifest + 逐步 diff
rollback/output/                     # 回滚段：serve 日志/probe 输出、Python CLI/API 读取结果
upgrade/snap/U0…U3, DU*            # 升级段：老 home/迁移后/旧二进制回读/新二进制二次启动
upgrade/output/                      # 升级段：release 构建、归档校验、旧/新版本、install.sh、verify
run.txt                            # 全流程 stdout（含每一步 health/root/marker）
```

## 结果摘要

| 项 | 结果 | 证据 |
| --- | --- | --- |
| Python 读 Go 迁移后的 v3 home | **通过**：`session list --cold` 2 条（迁移 DM + Go 探针会话，message_count 2/1）；历史 parts 可读；`demo_api_key`/`demo_oauth` 解密一致；`alpha/beta/probe_colony` tracker `schema_version=3` | `rollback/output/py-readback.json`、`py-session-list.json`、`py-queen-sessions.json`、`py-colony-list.json` |
| 回滚读的破坏性写入 | **无**：0 删除、0 覆盖既有文件；仅 `summary.json` 缓存新增 6 个（`D1a` +2、`D1b` +4） | `rollback/snap/D1a*`、`D1b*` |
| Python serve 在同一 home | health **200**、`/api/sessions` **200**；只追加 `logs/hive-server.log`（295→1366 B）；janitor marker 自写自清 | `rollback/snap/D2*`、`rollback/output/py-serve.log.probe.txt` |
| Go 冷读 Python 写的 summary | 与 Python 输出逐字段一致（session_id/message_count/queen_id）；**0 写入** | `rollback/output/go-session-list-after-py.json`、`rollback/snap/D3*` |
| Go 二次启动 | health **200**；仅日志追加（1661 B）；marker `present`→`after=absent`；**幂等** | `rollback/snap/D4*`、`go-serve-2.log.probe.txt` |
| plain 二进制 + 磁盘 dashboard | `/` 404 → **200**（把 `frontend/dist` 放到 CWD 后）；数据面仍只日志追加 | `rollback/snap/D5*`、`go-serve-ui.log.probe.txt` |
| 老用户升级（v1.0.25 v2 home → 1.0.26） | 启动即迁移（`+45/-40/modified 0`）；health **200**、`/` **200**；迁移前旧二进制冷列表 `[]` → 升级后读到 `session_20260201_000001_bbbbbbbb`（2 条消息） | `upgrade/snap/DU*`、`upgrade/output/new-session-list-after-upgrade.json`、`old-session-list.json` |
| 升级后回退旧二进制 | health **200**、能读同一会话；**0 写入** | `upgrade/snap/DU2*`、`old-session-list-after-migration.json` |
| 新二进制二次启动 | health **200**、`/` **200**；仅日志追加 | `upgrade/snap/DU3*` |
| `install.sh` 原地升级 | 旧 1.0.25 → 新 1.0.26；四二进制落盘；`--version`/health 200、`/` 200 | `upgrade/output/install-sh.log.txt`、`install-*.txt`、`install-serve.log.probe.txt` |
| 归档（`release.sh`，darwin/arm64） | `hive-1.0.26-go.1-darwin-arm64.tar.gz` 28,209,800 B；外层 `SHA256SUMS` OK；包内 8 项 sha OK | `upgrade/output/release-build.log.txt`、`release-archive-sha.txt` |
| 归档自验证 | `verify_release_archive.sh` **13/13 PASS** | `upgrade/output/verify-release-archive.txt` |

## 逐步 diff（added / removed / modified / sqlite_changed）

| diff | 含义 | 结果 |
| --- | --- | --- |
| `D0-go1-migration` | v2 夹具 → Go serve 迁移 + Go 写探针 | 59 / 40 / 0 / 6（搬移 + 新增；tracker `data/`→`tracker/`） |
| `D1a-py-cli` | Python CLI 冷读 | 2 / 0 / 0 / 0（2 个 `summary.json`） |
| `D1b-py-read` | Python API 读（历史/凭据/tracker） | 4 / 0 / 0 / 0（colony 会话的 4 个 `summary.json`） |
| `D2-py-serve` | Python `serve` 启动/健康/退出 | 0 / 0 / 1 / 0（仅共享日志追加） |
| `D3-go-cli` | Go 冷读（复用 Python 缓存） | 0 / 0 / 0 / 0 |
| `D4-go2` | Go serve #2 | 0 / 0 / 1 / 0（仅日志追加；marker 已清） |
| `D5-go-ui` | Go plain serve + 磁盘 dashboard | 0 / 0 / 1 / 0（仅日志追加；`/`=200） |
| `DU-upgrade-migration` | 旧 home → 新二进制 serve | 45 / 40 / 0 / 5（v2→v3 布局） |
| `DU2-old-binary-reads` | 旧二进制读迁移后 home | 0 / 0 / 0 / 0 |
| `DU3-new-serve2` | 新二进制 serve #2 | 0 / 0 / 1 / 0（仅日志追加） |

`sqlite_changed` 是相对路径上的表/行摘要变化；回滚与旧二进制回读阶段均为 0。

## 环境前提（`/` 404 的说明）

回滚段使用 plain `go build`（不带 `-tags embed_ui`），且 serve 的 CWD 没有 `frontend/dist`，因此 `/` 返回 404 属**构建形态/环境前提**，与数据读取无关：

- 把预构建 dashboard 放到 CWD 的 `frontend/dist` 后，同一 binary `/`=**200**（`D5-go-ui`）；
- 发布归档里的 embed 二进制 `/`=**200**（升级段、安装段均实测）；
- 数据面始终 `/api/health`=200、`/api/sessions`=200，会话/历史/凭据/tracker 读取不受影响。

## 复现

```bash
FRONTEND_DIST=/path/to/frontend/dist \
  bash go_hive/scripts/g2_rollback_drill.sh --work /tmp/g2-rollback
# 只跑回滚段
bash go_hive/scripts/g2_rollback_drill.sh --work /tmp/g2-rollback --skip-upgrade
```

`--no-embed`（或无 `FRONTEND_DIST`）时归档不含内嵌 dashboard，`/` 404 属预期，其余断言不变。
