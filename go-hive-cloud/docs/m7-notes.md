# M7 实现笔记 —— 部署件、备份/恢复与升级演练

> 状态：M7（2026-11-23）。范围 = README §4 的 M7 行：systemd/launchd、docker compose、
> 备份/迁移、升级演练。部署件在 `deploy/`，演练证据在
> `evidence/m7-deploy-drill.json`。
>
> 口径：只提供**可复用、无密钥、可验证**的单机部署件；不声称真实生产部署，
> 不伪造 systemd/launchd 引导或 Docker 镜像构建结果。存储/认证/SSE 语义完全沿用
> M1–M3：每 team SQLite、`<data-dir>/jwt_secret` + `stream_secret`（0600）、
> `stream_secret_previous` 轮换窗口、`serve`/`token` 子命令、`GET /health`。

## 1. 交付物

| 产物 | 关键语义 | 验证 |
| --- | --- | --- |
| `deploy/systemd/hive-cloud.service` | `User/Group=hive-cloud`、`StateDirectory=hive-cloud`(0700)、`EnvironmentFile=/etc/hive-cloud/hive-cloud.env`、`Restart=on-failure`、journald、`NoNewPrivileges/ProtectSystem=strict/ProtectHome/PrivateTmp/ReadWritePaths`、`UMask=0077` | `lint-deploy.sh` 指令断言；`systemd-analyze` 在本机不可用（BLOCKED） |
| `deploy/systemd/hive-cloud.env.example` | 仅非密钥配置（listen/data_dir/超时/限流）；密钥默认落在数据目录 | 无 secret 值 |
| `deploy/launchd/com.hive.cloud.plist` | `UserName=hivecloud`、`WorkingDirectory`、`RunAtLoad/KeepAlive`、`ThrottleInterval=10`、stdout/stderr 文件、`Umask=63` | `plutil -lint` + 键断言 |
| `deploy/launchd/hive-cloud-run.example.sh` | 以 `set -a` source 同格式 env 文件后 `exec hive-cloud serve`（launchd 无 EnvironmentFile） | `sh -n`；未实际加载 |
| `deploy/docker/Dockerfile` | `CGO_ENABLED=0` 静态构建；runtime 非 root 65532、数据目录属主、`HEALTHCHECK wget /health` | 未构建（registry 拉取超时，BLOCKED） |
| `deploy/docker/compose.yaml` | 命名卷、`read_only` + tmpfs、`user 65532`、`cap_drop ALL`、`no-new-privileges`、环回端口、healthcheck、`stop_grace_period` | `docker compose config -q` 通过 |
| `deploy/scripts/backup.sh` | 在线 `sqlite3 .backup`；`--offline` 复制 DB+WAL；`SHA256SUMS`、`BACKUP-META.txt`；0600 归档 | 在线/离线各跑一次；`verify-backup` 通过 |
| `deploy/scripts/restore.sh` | sidecar/逐文件校验和/`integrity_check`；拒绝绝对路径/`..`/符号链接；非空目标需 `--force` 并把旧目录改名保留；0700/0600；可选 `--owner` | 正常恢复 + `--force` 旧数据保留 + 篡改归档拒绝 |
| `deploy/scripts/verify-backup.sh` | 只读解包校验（校验和 + SQLite integrity） | 在线/离线归档均通过 |
| `deploy/scripts/upgrade.sh` | 版本预检（`--check` 不写盘）→ 备份 → 原子替换 + `hive-cloud.prev` → 重启 → `/health` 门禁 → 失败打印回滚命令；`--rollback`；`--rollback-on-failure` | `--check`/安装/健康/回滚/错误版本拒绝均通过 |
| `deploy/scripts/lint-deploy.sh` | 所有脚本 `bash -n` + `sh -n`、unit 指令、plist、compose config | 通过 |
| `deploy/scripts/deploy-drill.sh` | 临时目录完整演练并写 JSON 证据（无 token/secret） | 19/19 检查通过 |
| `deploy/README.md` | 安装/升级/回滚/密钥轮换/备份验证/端口/健康检查 | 人工审阅 |

## 2. 与既有配置/数据/令牌语义的对齐

- **数据目录**：`<data-dir>/teams/<team_id>.db`（WAL）、`jwt_secret`、`stream_secret`、
  可选 `stream_secret_previous`、可选 `config.json`；备份/恢复按此布局校验（必须含
  `teams/` 与 `BACKUP-META.txt`）。
- **密钥**：沿用 `config.ResolveSecrets`：缺失时自动生成 0600 文件；`stream_secret_previous`
  只读取、不生成——这与 M3 的“上一把 secret 存在即打开轮换窗口”一致。systemd/compose/
  launchd 模板默认不注入密钥，避免把 secret 写进部署件或镜像。
- **环境变量**：`HIVE_CLOUD_LISTEN`、`HIVE_CLOUD_DATA_DIR`、
  `HIVE_CLOUD_STATEMENT_TIMEOUT_MS`、`HIVE_CLOUD_INBOX_RATE_LIMIT`、
  `HIVE_CLOUD_INBOX_RATE_WINDOW_MS`、`HIVE_CLOUD_STREAM_SECRET_PREVIOUS`（注释示例）；
  与 `internal/config/config.go` 完全一致。
- **健康检查**：`/health` 是现有服务端公开端点（`server.go:handleHealth`），返回
  `status/service/version/time`；部署件不新增探活端点。`/health` 不触库，因此演练
  另外用真实 Global DB 行 + Inbox 行验证恢复后的数据完整性。
- **JWT/streamToken**：部署件只管理密钥文件与轮换步骤，不改变 M1/M3 的签发/校验
  语义。JWT 无双钥窗口（代码只支持一把 `jwt_secret`），文档明确轮换会使旧 JWT 立即
  401；streamToken 有 previous 窗口（M3）。
- **端口**：默认 `127.0.0.1:8788`；systemd/launchd 模板与 compose 都只发布环回地址，
  不内置 TLS；远程部署需自备反向代理（README §8）。

## 3. 备份/恢复/升级设计要点

- **在线备份**：对每个 team DB 调 `sqlite3 "$db" ".backup '<dest>'"`，能得到包含 WAL
  已提交页的一致性快照，服务无需停机。`sqlite3` 不存在时脚本拒绝在线模式，提示
  `--offline`（离线要求停机）。
- **归档**：`hive-cloud-backup-<UTC>-<label>.tar.gz` + `.sha256`；内含
  `BACKUP-META.txt`（键值元数据，无 secret 值）、`SHA256SUMS`（逐文件，含 secret
  文件哈希）、`teams/`、根配置。归档本身按机密件 0600 保管。
- **恢复**：先校验 sidecar 与逐文件 checksum，再对每个 DB 做
  `PRAGMA integrity_check`；拒绝绝对路径、`..`、符号链接。非空目标默认拒绝，`--force`
  时旧目录改名为 `<dir>.pre-restore-<UTC>`，不删除，作为回滚点。恢复后目录 0700、文件
  0600，`--owner` 便于 systemd/launchd 交还属主。
- **升级**：`--check` 只执行 `version` 并打印计划；正式升级先备份（备份失败即中止），
  把当前二进制复制为 `hive-cloud.prev`，以 `install-dir/.hive-cloud.new.$$` 暂存后
  `mv` 原子替换，再重启并等待 `/health`。健康失败退出码 3，打印可复制的回滚命令；
  `--rollback` 原子换回并重启/健康检查。没有 schema migration：schema 由二进制惰性
  创建；若回滚前新版本已写入不兼容结构，应恢复数据备份。
- **失败语义**：参数/安全拒绝=1，版本/备份/安装=2，健康门禁=3。脚本不启动长期服务，
  演练里的临时 server 全部在退出时终止。

## 4. 实际验证（2026-11-23，macOS 26.7.1 arm64）

| 命令 | 结果 |
| --- | --- |
| `bash -n` + `sh -n`（6 个脚本 + launchd wrapper） | 全部 0 |
| `plutil -lint deploy/launchd/com.hive.cloud.plist` | 0，`plutil: OK` |
| `cd deploy/docker && docker compose config -q` | 0 |
| `./deploy/scripts/lint-deploy.sh` | 0，PASS（含 unit 指令、Label/UserName/KeepAlive、compose） |
| `./deploy/scripts/deploy-drill.sh` | 0，19/19 检查通过，证据落盘 |
| `gofmt -l . && go vet ./... && go test ./... -count=1` | 0（M1–M3 测试无回归） |
| `python3 scripts/gen_cloud_surface.py --check` | 0（48 endpoints；16 implemented） |
| 额外边界：`restore --force` | 0，旧目录保留为 `target.pre-restore-*` |
| 额外边界：篡改归档后 `restore` | 2，checksum mismatch 拒绝 |
| 额外边界：`backup --offline` + `verify-backup` | 0/0 |
| 额外边界：升级健康失败（故意指到死端口） | 3，打印 `--rollback` 命令；随后 `--rollback` 成功 |

演练覆盖（`evidence/m7-deploy-drill.json` 的 19 个 check）：

1. `build_binary` — 用 go1.26.11 构建 `hive-cloud 0.1.0-m3`
2. `health_initial` — 临时 server `/health` 200，version 匹配
3. `seed_data` — 真实 Global DB `CREATE/INSERT`，Inbox 写入 1 条
4. `backup_live` — 服务运行中 `sqlite3 .backup`，归档 + sidecar
5. `verify_backup` — checksum + `PRAGMA integrity_check`
6. `delete_data` — 停服并 `rm -rf` 数据目录
7. `restore_archive` — 从归档恢复（无 `--force`，目标不存在）
8. `restore_permissions` — data dir 0700、jwt/stream 0600、1 个 DB
9. `restore_health` — 恢复后重启 `/health` 200
10. `restore_data_intact` — Global DB sentinel 行 = 1，Inbox 行 = 1
11. `upgrade_check` — `--check` 版本匹配，零写盘
12. `upgrade_install` — 备份 + 新二进制 + `hive-cloud.prev`
13. `upgrade_health_restart` — `--restart-cmd` 停旧起新，`/health` 200
14. `upgrade_rollback` — `--rollback` + 重启 + 健康
15. `upgrade_version_guard` — `9.9.9-m7-bad` 被 `--expect-version` 拒绝（exit 2），已装二进制不变
16. `launchd_plist` — `plutil -lint`
17. `compose_config` — `docker compose config -q`
18. `deploy_lint` — shell/unit/plist/compose 静态门禁
19. `systemd_unit_static` — unit 指令静态断言；runtime verify 标注 BLOCKED

证据里只有版本、状态码、行数、字节数、归档 SHA256、权限位；没有 JWT/streamToken/
secret 内容（`jwt_secret=600` 只是文件名+权限位）。

## 5. BLOCKED / 未验证（诚实边界）

| 项 | 状态 | 原因 |
| --- | --- | --- |
| Docker 镜像构建 | **BLOCKED** | 本机 `docker pull` 120s 超时（registry 不可达）；只验证了 `docker compose config` 与静态约束 |
| systemd 运行时引导 | **BLOCKED** | 本机是 macOS，无 systemd/systemd-analyze；只做 unit 指令静态断言 |
| launchd 运行时加载 | **未执行** | 需要 root、建系统账号并改动 `/Library/LaunchDaemons`；只做 `plutil -lint` 与键断言 |
| Docker 卷备份/恢复流程 | **未执行** | 无镜像/容器；README 给出 stop→cp→`--offline`→restore→chown 步骤 |
| 真实新版本升级 | **部分** | 演练用同一 `0.1.0-m3` 二进制的副本，验证的是版本检查/替换/回滚/健康路径，不是真实版本差异 |
| `--rollback-on-failure` 自动回滚 | **未执行** | 演练验证了手动 `--rollback`、健康失败提示和错误版本拒绝；自动分支代码同路径但未单独触发 |
| 密钥轮换步骤 | **文档化，未演练** | 会改变密钥文件；M3 的 L1/L2 已覆盖 streamToken previous 窗口语义 |
| Postgres 适配器 / 真实云双跑 | **未做** | 属 M1b/L3，超出 M7 范围 |
| TLS/反向代理配置 | **未交付** | 部署件只讲 HTTP + 环回；README 给出 Caddy/nginx 要点 |
| 真实生产部署/流量 | **未做** | 全部演练在临时目录与环回端口，退出即清理（临时 server 已终止） |

## 6. 已知风险与注意事项

- **systemd 账号/环境文件**：unit 假定 `hive-cloud` 用户与组存在、`/etc/hive-cloud/hive-cloud.env`
  可读；README 给了创建命令但不同发行版 `useradd` 参数需按实际调整。`StateDirectory`
  会创建 0700 数据目录。
- **launchd 账号**：plist 的 `UserName=hivecloud` 必须真实存在且主组与 `GroupName=staff`
  一致；macOS 建号方式依组织/MDM 而异，README 的 `sysadminctl` 只是示例。日志轮转未交付。
- **compose 卷属主**：命名卷由镜像内 65532 拥有；宿主 `docker compose cp` 恢复后必须用
  一次性 root 容器 `chown -R 65532:65532`，否则服务无法读 secret（README §4 已写）。
- **备份一致性**：在线备份依赖 `sqlite3 .backup`；`--offline` 必须真的停服，否则可能
  复制到写入中的 WAL。归档含密钥，需与数据同等级保护；校验和计算覆盖 secret 文件，
  但 manifest 不含明文。
- **恢复的 `--force`**：通过 `mv` 保留旧目录；若目标本身是挂载点（例如容器卷挂载路径），
  `mv` 会失败并报错，此时应按 README 的容器复制流程恢复，不能直接对挂载点执行。
- **健康门禁的深度**：`/health` 只证明进程在监听，不触库；演练额外用真实行验证数据。
  生产监控建议同时探测 `/v1/global-db/tables`（需 JWT）或自建深健康脚本。
- **JWT 轮换**：没有 previous JWT key，轮换即全量 401，必须同步重新分发；这是现有代码
  语义，不是部署件遗漏。
- **Dockerfile 基础镜像 tag**：`golang:1.26.11-alpine` 与 `alpine:3.22` 的可拉取性未在
  本机验证；若 tag 不存在，按 `go.mod` 的 go1.26.11 选对应 golang 镜像或调整
  `GO_VERSION` build arg（不降级 go directive）。
- **端口/防火墙**：systemd/launchd 默认只听 127.0.0.1；如果要让其他主机直连，需要改
  `HIVE_CLOUD_LISTEN` 并在前面加 TLS/防火墙，本仓库不提供该公网拓扑。

## 7. 门禁命令

```bash
source /Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh
cd go_hive/go-hive-cloud

./deploy/scripts/lint-deploy.sh          # 静态部署门禁
./deploy/scripts/deploy-drill.sh         # 备份/恢复/升级演练 + 证据
gofmt -l . && go vet ./... && go test ./... -count=1
python3 scripts/gen_cloud_surface.py --check
```

辅助脚本用法见 `deploy/README.md`；每个脚本 `-h` 也有用法与退出码说明。
