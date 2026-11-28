# go-hive-cloud 部署件（M7）

> 状态：M7（2026-11-23）。本目录提供单机私有部署件：systemd / launchd、Docker
> compose、备份/恢复/校验/升级脚本、部署校验与一次可复现的备份→删除→恢复→健康检查
> 演练。**没有在本机执行真实生产部署**；演练证据与未验证边界见
> `docs/m7-notes.md` 和 `evidence/m7-deploy-drill.json`。
>
> 存储仍是 M1 的纯 Go SQLite（每 team 一个文件）；Postgres 适配器仍未实现（架构文档
> 中的 M1b）。所有服务端配置/数据目录/JWT/streamToken 语义沿用 M1–M3。

## 1. 产物与前置

| 路径 | 用途 |
| --- | --- |
| `systemd/hive-cloud.service` | Linux systemd unit（非 root、环境文件、重启、日志、加固） |
| `systemd/hive-cloud.env.example` | systemd 环境文件模板（不含密钥） |
| `launchd/com.hive.cloud.plist` | macOS LaunchDaemon（同等语义） |
| `launchd/hive-cloud-run.example.sh` | launchd 环境文件包装脚本（launchd 无 EnvironmentFile） |
| `docker/Dockerfile` | 静态构建 + 非 root(65532) + 只读根 + 健康检查 |
| `docker/compose.yaml` | 单节点 compose：健康检查、命名卷、只读根、cap_drop、环回端口 |
| `docker/.env.example` | compose 非密钥变量模板 |
| `scripts/backup.sh` | 在线/离线备份数据目录与 SQLite |
| `scripts/restore.sh` | 校验、解除非空目标保护、恢复、权限、可选 chown |
| `scripts/verify-backup.sh` | 只读验证（校验和 + SQLite integrity_check） |
| `scripts/upgrade.sh` | 版本检查 → 备份 → 原子替换 → 重启/健康门禁 → 回滚 |
| `scripts/lint-deploy.sh` | 脚本语法、unit 指令、plist、compose config 静态门禁 |
| `scripts/deploy-drill.sh` | 临时数据目录的备份/删除/恢复/升级演练，写证据 JSON |

构建二进制（无 cgo）：

```bash
source /Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh
cd go_hive/go-hive-cloud
CGO_ENABLED=0 go build -trimpath -o /tmp/hive-cloud ./cmd/hive-cloud
/tmp/hive-cloud version        # hive-cloud 0.1.0-m3
```

数据目录布局（`HIVE_CLOUD_DATA_DIR`）：

```
<data-dir>/
  teams/<team_id>.db      # 每 team SQLite（WAL）
  jwt_secret              # 0600，operator JWT 签名密钥（自动生成）
  stream_secret           # 0600，streamToken 当前签名密钥（自动生成）
  stream_secret_previous  # 0600，可选，轮换窗口内仍接受旧 token（不自动生成）
  config.json             # 可选，0600
```

- 默认监听 `127.0.0.1:8788`；健康检查 `GET /health`（`status/version/time`）。
- 服务本身只讲 HTTP，不内置 TLS。远程客户端必须经反向代理（Caddy/nginx）终止 TLS，
  且不要把 `8788` 直接暴露到公网。
- 配置优先级：flag > 环境变量 `HIVE_CLOUD_*` > `config.json` > 默认值；密钥文件在
  `HIVE_CLOUD_*_SECRET` 未设置时从 `<data-dir>` 读取/生成。

## 2. Linux（systemd）

```bash
# 1) 安装二进制
sudo install -m 0755 /tmp/hive-cloud /usr/local/bin/hive-cloud

# 2) 专用非 root 账号（Debian/RHEL 的 useradd 均可用；shell 不可登录）
sudo useradd --system --home-dir /var/lib/hive-cloud --shell /usr/sbin/nologin hive-cloud

# 3) 目录与配置
sudo install -d -o hive-cloud -g hive-cloud -m 0700 /var/lib/hive-cloud
sudo install -d -o root -g hive-cloud -m 0750 /etc/hive-cloud
sudo install -m 0640 -o root -g hive-cloud \
  deploy/systemd/hive-cloud.env.example /etc/hive-cloud/hive-cloud.env

# 4) unit
sudo install -m 0644 deploy/systemd/hive-cloud.service \
  /etc/systemd/system/hive-cloud.service
sudo systemctl daemon-reload
sudo systemctl enable --now hive-cloud

# 5) 验证
systemctl status hive-cloud
journalctl -u hive-cloud -n 50 --no-pager
curl -fsS http://127.0.0.1:8788/health
```

unit 已是 `User=hive-cloud`、`StateDirectory=hive-cloud`（0700）、
`EnvironmentFile=/etc/hive-cloud/hive-cloud.env`、`Restart=on-failure`、
日志进 journald，并启用 `NoNewPrivileges/ProtectSystem=strict/ProtectHome/PrivateTmp/
ReadWritePaths=/var/lib/hive-cloud` 等加固。`MemoryDenyWriteExecute=true` 对纯 Go、
无 cgo 的二进制成立；若未来引入需要可执行内存的组件要重新评估。

客户端接入：`HIVE_CLOUD_BASE=http://127.0.0.1:8788`，`HIVE_CLOUD_JWT` 用
`sudo -u hive-cloud /usr/local/bin/hive-cloud token --data-dir /var/lib/hive-cloud
--user-id <u> --team-id <team>` 签发（`token` 与 `serve` 必须使用同一数据目录）。

## 3. macOS（launchd）

launchd 不支持 `EnvironmentFile`，因此 plist 调用包装脚本，由包装脚本 source 与
systemd 同格式的 env 文件。plist 里没有密钥。

```bash
# 1) 二进制与包装脚本
sudo install -m 0755 /tmp/hive-cloud /usr/local/bin/hive-cloud
sudo install -m 0755 deploy/launchd/hive-cloud-run.example.sh \
  /usr/local/bin/hive-cloud-launchd

# 2) 专用非 root 账号（示例；按组织标准/MDM 建号，确保无登录 shell、非 UID 0）
sudo sysadminctl -addUser hivecloud -fullName "Hive Cloud" \
  -shell /usr/bin/false -home /usr/local/var/hive-cloud

# 3) 目录、配置、日志
sudo install -d -o hivecloud -g staff -m 0700 /usr/local/var/hive-cloud
sudo install -d -o root -g staff -m 0750 /usr/local/etc/hive-cloud
sudo install -m 0640 -o root -g staff \
  deploy/systemd/hive-cloud.env.example /usr/local/etc/hive-cloud/hive-cloud.env
# 把 env 文件里的 HIVE_CLOUD_DATA_DIR 改成 /usr/local/var/hive-cloud
sudo install -d -o hivecloud -g staff -m 0750 /usr/local/var/log/hive-cloud

# 4) LaunchDaemon
sudo install -m 0644 -o root -g wheel deploy/launchd/com.hive.cloud.plist \
  /Library/LaunchDaemons/com.hive.cloud.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/com.hive.cloud.plist
sudo launchctl kickstart -k system/com.hive.cloud

# 5) 验证
sudo launchctl print system/com.hive.cloud | head -40
tail -n 50 /usr/local/var/log/hive-cloud/stderr.log
curl -fsS http://127.0.0.1:8788/health
```

plist 语义：`UserName=hivecloud`、`WorkingDirectory=/usr/local/var/hive-cloud`、
`RunAtLoad`/`KeepAlive`、`ThrottleInterval=10`、stdout/stderr 落
`/usr/local/var/log/hive-cloud/`、`Umask=63`（0o077）。`GroupName=staff` 与建号时的
主组一致；若你的账号主组不同，改 plist 的该字段。日志轮转用 `newsyslog`/MDM，
本仓库不安装轮转配置。

卸载：

```bash
sudo launchctl bootout system /Library/LaunchDaemons/com.hive.cloud.plist
```

## 4. Docker compose

```bash
cd go_hive/go-hive-cloud/deploy/docker
cp .env.example .env          # 仅非密钥覆盖；.env 已被根 .gitignore 忽略
docker compose config         # 静态校验（不需要 daemon 拉镜像）
docker compose up -d --build
docker compose ps
curl -fsS http://127.0.0.1:8788/health
docker compose logs --tail=50 hive-cloud
```

compose 的关键约束：

- `read_only: true`，仅 `/var/lib/hive-cloud`（命名卷 `hive-cloud-data`）与 16MB
  `/tmp` tmpfs 可写；
- `user: 65532:65532`（镜像内 `hive-cloud` 系统用户），`cap_drop: ALL`，
  `no-new-privileges`；
- 端口只发布到 `127.0.0.1:${HIVE_CLOUD_PORT:-8788}`；
- 健康检查 `wget /health`，`interval=30s`、`start_period=10s`；
- 密钥在卷里自动生成（0600），镜像/ compose/env 均无密钥。

compose 下的备份（命名卷不能直接给宿主机脚本读，先停服务再复制）：

```bash
cd go_hive/go-hive-cloud/deploy/docker
docker compose stop hive-cloud
mkdir -p /tmp/hive-cloud-snapshot
docker compose cp hive-cloud:/var/lib/hive-cloud/. /tmp/hive-cloud-snapshot/
../scripts/backup.sh --data-dir /tmp/hive-cloud-snapshot --out /var/backups/hive-cloud \
  --offline --label docker
docker compose start hive-cloud
```

恢复进卷（先复制，再用一次性 root 容器修正属主，最后启动）：

```bash
docker compose stop hive-cloud
../scripts/restore.sh --archive /var/backups/hive-cloud/<archive>.tar.gz \
  --data-dir /tmp/hive-cloud-restored
docker compose cp /tmp/hive-cloud-restored/. hive-cloud:/var/lib/hive-cloud/
docker compose run --rm --no-deps --user 0 --entrypoint chown hive-cloud \
  -R 65532:65532 /var/lib/hive-cloud
docker compose start hive-cloud
curl -fsS http://127.0.0.1:8788/health
```

> 镜像构建需要 registry 网络。本 lane 的 M7 验证只跑了 `docker compose config`
> 与 compose 静态约束；本机 `docker pull` 超时，**镜像构建未验证（BLOCKED）**，
> 见 `docs/m7-notes.md` §5。若你的环境能拉取 `golang:1.26.11-alpine` 与
> `alpine:3.22`，用上面的 `up -d --build` 完成构建。

升级 compose 部署：见 §6.3（备份 → 重建镜像 → `up -d` → 健康检查）。

## 5. 备份、恢复与备份验证

### 5.1 备份

```bash
deploy/scripts/backup.sh --data-dir /var/lib/hive-cloud \
  --out /var/backups/hive-cloud --label daily --binary /usr/local/bin/hive-cloud
```

- 在线模式（默认）对每个 `teams/*.db` 执行 `sqlite3 .backup`（WAL 安全），可在服务
  运行时执行；根目录文件（`config.json`、secret 文件）一起打包。
- 离线模式 `--offline` 直接复制 DB 与 `-wal/-shm`，**要求服务已停止**；`sqlite3`
  不可用时只能走离线模式。
- 产物：`hive-cloud-backup-<UTC>-<label>.tar.gz`（0600）+ `.sha256` sidecar；
  内含 `BACKUP-META.txt`、`SHA256SUMS`（逐文件校验和，含 secret 文件哈希）、
  `teams/`、根配置文件。归档可能含密钥，按机密件保管（0600、受控目录）。
- 脚本从不打印 secret 内容；manifest 只有键值元数据和哈希。

### 5.2 恢复

```bash
# 服务必须先停：systemd `systemctl stop hive-cloud`；launchd
# `sudo launchctl bootout system /Library/LaunchDaemons/com.hive.cloud.plist`；
# compose `docker compose stop hive-cloud`。
deploy/scripts/restore.sh --archive /var/backups/hive-cloud/<archive>.tar.gz \
  --data-dir /var/lib/hive-cloud --owner hive-cloud:hive-cloud
```

- 默认拒绝非空目标；确认要覆盖时加 `--force`。旧数据目录会改名为
  `<data-dir>.pre-restore-<UTC>` 保留，不删除，失败可换回。
- 恢复前验证 sidecar、归档内 `SHA256SUMS`、每个 DB 的 `PRAGMA integrity_check`；
  绝对路径/`..`/符号链接一律拒绝。
- 恢复后强制目录 0700、文件 0600；`--owner` 用于把树交还给服务账号。
- 恢复不启动服务：启动后先 `GET /health`，再放流量。

### 5.3 备份验证（建议每周 + 每次升级前）

```bash
deploy/scripts/verify-backup.sh --archive /var/backups/hive-cloud/<archive>.tar.gz
```

只读校验：sidecar → SHA256SUMS → SQLite `integrity_check`。退出码 0=通过，
2=校验失败；失败时不要删除旧备份。`sqlite3` 缺失时会 WARN 并跳过 integrity
（仅校验和仍验证），生产主机应安装 `sqlite3`。

cron 示例（每日备份 + 每周日验证；删除时连 `.sha256` 一起删）：

```cron
15 3 * * * /opt/hive-cloud/deploy/scripts/backup.sh --data-dir /var/lib/hive-cloud --out /var/backups/hive-cloud --label daily
45 3 * * 0 /opt/hive-cloud/deploy/scripts/verify-backup.sh --archive "$(ls -1t /var/backups/hive-cloud/*.tar.gz | head -1)" || echo "backup verification FAILED" | logger -t hive-cloud-backup
```

确认最近备份已验证后，手动清理 14 天前的归档（先验证，后删除）：

```bash
find /var/backups/hive-cloud -maxdepth 1 -name 'hive-cloud-backup-*.tar.gz*' -mtime +14 -delete
```

备份目录不要和 `data-dir` 放在同一块磁盘/同一故障域；归档含密钥，传输用受控通道。

## 6. 升级与回滚

M7 的实现没有 schema migration：每个 team 的 schema 由二进制惰性创建/补齐，升级
就是“备份 → 原子替换二进制 → 重启 → 健康门禁”。旧二进制回滚后若新版本已经写过
不兼容的新结构，应改用备份恢复，而不是只换回二进制。

### 6.1 systemd

```bash
# 预检：版本匹配 + 打印备份/安装计划，不写文件
deploy/scripts/upgrade.sh --check --binary /tmp/hive-cloud-0.1.1 \
  --data-dir /var/lib/hive-cloud --install-dir /usr/local/bin

# 升级：自动先备份；原子替换；systemctl restart；等待 /health
deploy/scripts/upgrade.sh --binary /tmp/hive-cloud-0.1.1 \
  --data-dir /var/lib/hive-cloud --install-dir /usr/local/bin \
  --backup-dir /var/backups/hive-cloud --expect-version 0.1.1 \
  --service hive-cloud --health-url http://127.0.0.1:8788/health
```

成功后旧二进制保留在 `/usr/local/bin/hive-cloud.prev`。健康检查失败退出码 3，并打印
可直接复制的回滚命令；加 `--rollback-on-failure` 会自动换回并重启。

### 6.2 macOS launchd

```bash
sudo deploy/scripts/upgrade.sh --binary /tmp/hive-cloud-0.1.1 \
  --data-dir /usr/local/var/hive-cloud --install-dir /usr/local/bin \
  --backup-dir /usr/local/var/backups/hive-cloud --expect-version 0.1.1 \
  --restart-cmd "launchctl kickstart -k system/com.hive.cloud" \
  --health-url http://127.0.0.1:8788/health
```

### 6.3 Docker compose

```bash
cd go_hive/go-hive-cloud/deploy/docker
# 1) 先按 §4 的 stop/cp/backup 流程做备份
# 2) 构建并滚动替换（compose 会重建容器；命名卷保留）
docker compose build --pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:8788/health
# 3) 回滚：设置 HIVE_CLOUD_IMAGE 为旧 tag（或 git 回到旧 Dockerfile）后
#    docker compose up -d；没有旧镜像时用 §5.2 恢复数据卷
```

容器内版本核对：`docker compose run --rm --no-deps hive-cloud version`。

### 6.4 手动回滚

```bash
deploy/scripts/upgrade.sh --rollback --install-dir /usr/local/bin \
  --service hive-cloud --health-url http://127.0.0.1:8788/health
# launchd 用 --restart-cmd "launchctl kickstart -k system/com.hive.cloud"
```

回滚会原子换回 `hive-cloud.prev` 并重启/健康检查；不删除升级前备份。若健康仍失败，
按 §5.2 用 `<data-dir>.pre-restore-*` 或升级前归档恢复数据，再排查日志。

## 7. 密钥轮换

密钥文件在数据目录，均为 0600；轮换必须先停服务（或使用配置注入 + 重启）。JWT 与
streamToken 是两把独立密钥，互不影响。

### 7.1 JWT（立即失效，无双钥窗口）

服务只支持一把 JWT 签名密钥，没有 `jwt_secret_previous`：换钥即让所有已签发 JWT
立即 401，客户端需要重新注入 `HIVE_CLOUD_JWT`。

```bash
sudo systemctl stop hive-cloud
sudo mv /var/lib/hive-cloud/jwt_secret \
  /var/lib/hive-cloud/jwt_secret.old.$(date -u +%Y%m%dT%H%M%SZ)
sudo systemctl start hive-cloud       # 无 jwt_secret 时自动生成新密钥（0600）
# 用新密钥重新签发并分发 JWT：
sudo -u hive-cloud /usr/local/bin/hive-cloud token --data-dir /var/lib/hive-cloud \
  --user-id <u> --team-id <team> --ttl 24h
# 客户端全部切换后再删除旧文件
sudo rm /var/lib/hive-cloud/jwt_secret.old.*
```

### 7.2 streamToken（双钥轮换窗口，≤24h）

```bash
sudo systemctl stop hive-cloud
# 旧 current 变成 previous；current 缺失会在启动时生成新密钥
sudo mv /var/lib/hive-cloud/stream_secret /var/lib/hive-cloud/stream_secret_previous
sudo systemctl start hive-cloud
```

窗口内旧 streamToken 仍可校验并换新（`POST /user/refresh-stream-token-from-stream`）；
客户端刷新周期 ≤24h。确认旧 token 不再需要后关闭窗口：

```bash
sudo systemctl stop hive-cloud
sudo rm /var/lib/hive-cloud/stream_secret_previous
sudo systemctl start hive-cloud
```

若已有 `stream_secret_previous`（上一轮未清理），先确认再覆盖；不要同时删除 current
和 previous 后启动两次，否则旧 token 立刻失效。密钥也可由
`HIVE_CLOUD_JWT_SECRET`/`HIVE_CLOUD_STREAM_SECRET`（及
`HIVE_CLOUD_STREAM_SECRET_PREVIOUS`）注入；此时 env 文件必须 0600 且不进版本库。

## 8. 端口、健康检查与反向代理

| 项 | 值 |
| --- | --- |
| 默认监听 | `127.0.0.1:8788`（`HIVE_CLOUD_LISTEN`） |
| 健康检查 | `GET /health` → `{"status":"ok","service":"hive-cloud","version":"...","time":"..."}` |
| 无鉴权探活 | `GET /v1/gateway/availability`（客户端启动 ping） |
| 就绪判据 | HTTP 200 且 `status=ok`；数据库按 team 惰性打开，`/health` 不触库 |
| 日志 | systemd journald / launchd 文件 / compose `docker compose logs` |

```bash
curl -fsS http://127.0.0.1:8788/health
curl -fsS 'http://127.0.0.1:8788/v1/gateway/availability?from=cli'
```

反向代理要点：只反代到环回地址；TLS 在代理终止；SSE 路径
`/v1/inbox/runtime/stream` 必须关闭响应缓冲（服务端已发 `X-Accel-Buffering: no`，
nginx 仍建议 `proxy_buffering off; proxy_read_timeout 1h;`）。Caddy 示例：

```
cloud.example.com {
  reverse_proxy 127.0.0.1:8788
}
```

防火墙只放行代理端口，不放行 8788。compose 已把发布地址固定为 `127.0.0.1`。

## 9. 迁移到新主机

1. 旧机 `systemctl stop hive-cloud`（或停 compose/launchd）；
2. `backup.sh`（可在线，也可停机离线）→ 传输 `.tar.gz` + `.sha256`（含密钥，走受控通道）；
3. 新机按 §2/§3 安装二进制、账号、unit/plist（先不启动）；
4. `restore.sh --archive <...> --data-dir <new> --owner <service-user>`；
5. 启动 → `GET /health` → 用一个新签发 JWT 调
   `/v1/global-db/tables` / `/v1/inbox/...` 做真实读写检查；
6. 客户端把 `HIVE_CLOUD_BASE` 指向新地址。密钥随数据目录恢复，旧 JWT 仍有效；
   若迁移时轮换了密钥，需重新分发。

没有外部数据库/对象存储依赖，迁移单位就是数据目录；端口若变化只需改
`HIVE_CLOUD_LISTEN`/代理配置。

## 10. 校验与演练

```bash
# 静态门禁：bash -n/sh -n、unit 指令、plist、docker compose config
deploy/scripts/lint-deploy.sh

# 备份→删除→恢复→健康检查 + 升级检查/安装/回滚/版本拒绝演练
deploy/scripts/deploy-drill.sh
# 证据：evidence/m7-deploy-drill.json（无 token/secret）
```

M7 实际执行范围（2026-11-23，macOS arm64）：

- ✅ `bash -n`/`sh -n` 全部脚本、`plutil -lint` plist、`docker compose config`、
  SQLite 在线备份/校验/恢复/权限、恢复后 `/health` 与数据（Global DB 行 + Inbox 行）、
  `upgrade --check`/安装/回滚/错误版本拒绝、重启后健康检查；
- ⛔ 未执行：真实 systemd/launchd 引导（本机无 systemd，launchd 未加载）、
  Docker 镜像构建（registry 拉取超时，BLOCKED）、真实生产流量/Postgres 适配器。

## 11. 故障排查

| 症状 | 检查 |
| --- | --- |
| systemd 启动失败 | `journalctl -u hive-cloud`；`/etc/hive-cloud/hive-cloud.env` 是否存在且可读；`/var/lib/hive-cloud` 属主 |
| launchd 启动失败 | `launchctl print system/com.hive.cloud`；UserName 是否存在；env 文件是否可读；stdout/stderr 日志 |
| `401` 全员 | JWT 是否过期/轮换；`HIVE_CLOUD_JWT` 是否来自同一数据目录的 `jwt_secret` |
| streamToken 401 | 是否已关闭 previous 窗口；旧 token 过期；server 的 `stream_secret` 是否一致 |
| 备份报 sqlite3 not found | 安装 sqlite3，或停服务后加 `--offline` |
| 恢复拒绝非空目标 | 确认后用 `--force`，旧目录会保留为 `.pre-restore-*` |
| 升级后健康失败（exit 3） | 按输出执行 `upgrade.sh --rollback ...`；再失败则恢复备份数据 |
| compose 里数据丢失 | 确认使用命名卷 `hive-cloud-data`，没有 `down -v`；恢复见 §4 |
| SSE 不实时 | 代理缓冲/超时；`proxy_buffering off`、读超时 ≥ 1h |
