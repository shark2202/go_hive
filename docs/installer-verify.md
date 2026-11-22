# 六平台安装包复验（G3 installer-verify）

本页记录 G3「六平台安装器复验」的方法与实测结果：在基线 `151dbe66` 上用
`scripts/release.sh`（默认路径，embed UI）重建六个平台的发布归档，逐归档做
静态 + 安装器核对，并在**真实 linux/arm64 容器**里执行归档内二进制
（`--version` / `--help` / 全新 `HIVE_HOME` 下 `serve` 健康检查 / `hive-cred --help`），
另加 linux/amd64 容器（docker 模拟 x86_64）与 darwin 双架构本机执行。**全程未改
产品代码，也未改既有发布脚本。**

## 0. 结论

| 层 | 范围 | 结果 |
| --- | --- | --- |
| A 静态 + `install.sh` | 六平台归档 | **36/36 PASS** |
| B 容器真跑 | linux/arm64（debian:stable-slim，真机） | **20/20 PASS** |
| B 容器真跑 | linux/amd64（docker 模拟 x86_64） | 20/20 PASS |
| C 本机执行 | darwin/arm64 原生、darwin/amd64（Rosetta） | 全部入口 PASS |
| — 未执行 | windows/amd64、windows/arm64 | 仅 A 层（无 Windows 宿主，见 §6） |

没有发现产品缺陷；无需修代码。

## 1. 基线、环境与方法

| 项 | 值 |
| --- | --- |
| 仓库基线 | `151dbe66e39100bd54e4076dcebc161485ede746`（main） |
| 构建位置 | 工作树 `.wt/installer-verify`（分支 `installer-verify`，自基线拉出） |
| 版本 | `1.0.26-go.1`（`internal/version/version.go`） |
| 工具链 | go1.26.5 darwin/arm64；离线三件套 `CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod` |
| 前端 | `FRONTEND_DIST=<主检出>/go-hive/frontend/dist` + `SKIP_FRONTEND_BUILD=1`（预构建 dashboard，未跑 npm；归档为 hive-embed 嵌入 UI 构建，日志无 fallback 警告） |
| 容器 | `debian:stable-slim`（Debian 13.7 trixie；arm64 digest `sha256:fbfb4059d2127120aa05b6e6064523d2102552f0cf0026ddf0701c12f1f8f3e0`）；容器内 bash 5.2.37、coreutils 9.7 |
| docker | 29.9.1（aarch64 host）；linux/amd64 变体由 Docker Desktop 模拟执行 |
| 证据目录 | `docs/e2e-assets/installer-verify/2026-11-22/` |

三层验证各由仓库脚本驱动（新增，见 §7）：

1. **A 静态 + 安装器**（`scripts/verify_installer_archives.sh`，六平台）：
   - A1 归档存在，外层 sha256 == 发布目录 `SHA256SUMS` 对应项；
   - A2 归档根含 `hive[.exe] hive-cred[.exe] hive-browser[.exe] browserbridge[.exe] LICENSE NOTICE README.md SHA256SUMS`，四二进制可执行；
   - A3 归档内层 `SHA256SUMS`（8 个文件）逐字节复算；
   - A4 `install.sh --from` 经 SHA256 校验后把四二进制安装到临时目录并置可执行
     （跨架构二进制在 `--version`/`--help` 探针处按设计仅告警）。
2. **B 容器真跑**（`scripts/docker_installer_verify.sh` 主机侧 +
   `scripts/docker_installer_probe.sh` 容器内）：发布目录只读挂载进容器，
   `sha256sum -c` 校验、解包、C1–C10 逐条记录命令/输出/退出码。
3. **C 本机执行**：darwin/arm64 原生、darwin/amd64 经 Rosetta，入口与 B 层一致。

## 2. 复现命令

```bash
cd go_hive
export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod

# 1) 六平台归档（默认 embed UI；离线环境用预构建 dashboard 跳过 npm）
FRONTEND_DIST=/abs/path/to/frontend/dist SKIP_FRONTEND_BUILD=1 bash scripts/release.sh
#    -> go-hive/dist/release/hive-1.0.26-go.1-{darwin,linux}-{amd64,arm64}.tar.gz
#       + hive-1.0.26-go.1-windows-{amd64,arm64}.zip + SHA256SUMS

# 2) A 层：六平台静态 + install.sh（36 项）
bash scripts/verify_installer_archives.sh

# 3) B 层：容器真跑（需 docker；写入证据目录）
OUT_DIR=$PWD/docs/e2e-assets/installer-verify/2026-11-22 bash scripts/docker_installer_verify.sh
ARCH=amd64 OUT_DIR=$PWD/docs/e2e-assets/installer-verify/2026-11-22 bash scripts/docker_installer_verify.sh
```

## 3. A 层结果：六平台归档与安装器（36/36 PASS）

外层 sha256 与发布目录 `SHA256SUMS` 一致（`shasum -a 256 -c SHA256SUMS` 全 OK
复核过），内层 `SHA256SUMS` 8 文件（4 二进制 + LICENSE + NOTICE + README.md +
THIRD_PARTY_NOTICES.md）全部复算通过，六个归档经 `install.sh` 安装到临时目录、
四二进制可执行。

| 平台 | 归档（bytes） | 外层 sha256 |
| --- | --- | --- |
| darwin/amd64 | 37,416,117 | `d2132fdd6f99dfbe97a71de4f6fc553b60baff40afd468ec7d92f48334afca7c` |
| darwin/arm64 | 35,684,216 | `bf2d2635798b988255eb56d2fc78efcd4ef93fa4a3c3805bb4de5047b7d68ff7` |
| linux/amd64 | 36,969,096 | `0de567919df37a5479c941a2fc037436bf27054fa8fd6cd21f0c8edfcab7d23a` |
| linux/arm64 | 34,535,219 | `6731aa6ab82f9fb297fb4e017ad5e0038368d1870a3575e2ad02a374e687ef9f` |
| windows/amd64 | 34,412,553 | `6c9b58f5c1b7ae562da803b78127203d85863feac2a35e07f886156ea9440a8a` |
| windows/arm64 | 31,720,053 | `05bf8eebd09a5c63d993c855ebffb4e77c08a0742354f61922ac706eb9e34ea4` |

归档内四二进制大小（bytes，`hive` 为 embed UI 构建）：

| 平台 | hive | hive-cred | hive-browser | browserbridge |
| --- | --- | --- | --- | --- |
| darwin/amd64 | 47,538,288 | 16,682,944 | 16,568,784 | 8,022,432 |
| darwin/arm64 | 45,919,826 | 16,176,690 | 16,045,122 | 7,436,642 |
| linux/amd64 | 46,981,282 | 16,392,354 | 16,285,858 | 7,880,866 |
| linux/arm64 | 45,023,394 | 15,794,338 | 15,728,802 | 7,274,658 |
| windows/amd64 | 46,165,504 | 15,293,440 | 15,180,288 | 8,098,304 |
| windows/arm64 | 43,892,736 | 14,495,744 | 14,380,544 | 7,360,512 |

证据：`archive-verify.log.txt`（含逐归档逐二进制 sha256）。

## 4. B 层结果：容器真跑（linux/arm64 = 核心）

容器信息（transcript 首行）：`Linux 7.0.14-linuxkit aarch64 PRETTY_NAME="Debian GNU/Linux 13 (trixie)"`。

| id | 检查 | 结果 |
| --- | --- | --- |
| C1 | 归档 sha256 == 发布 `SHA256SUMS`（`sha256sum -c`） | PASS |
| C2 | 解包得 `hive hive-cred hive-browser browserbridge` 且可执行 | PASS（5 项） |
| C3 | `hive --version` exit 0，且含期望版本 `1.0.26-go.1` | PASS |
| C4 | `hive --help` exit 0，含 usage | PASS |
| C5 | 全新 `HIVE_HOME`（不存在→由 serve 创建）；`hive serve` 监听；`GET /api/health` → 200 `{"status":"ok"}` | PASS（3 项） |
| C6 | `GET /` → 200（内嵌 dashboard，`<!DOCTYPE html>`） | PASS |
| C7 | `hive-cred --help` exit 0 | PASS |
| C8 | `hive-browser help` exit 0；`browserbridge -version` exit 0（`0.3.0-l3a`） | PASS（2 项） |
| C9 | `hive serve` 收到 SIGTERM 正常退出 | PASS |
| C10 | `HIVE_BROWSER_BRIDGE=1`：serve 解析兄弟 `browserbridge`、bridge ready、`/health` 200、serve 退出后 bridge 端口关闭（不残留） | PASS（3 项） |

关键 transcript 摘录（完整见 `docker-linux-arm64.log.txt`）：

```text
$ ./hive --version
hive 1.0.26-go.1
exit=0
$ ./hive --help            -> exit=0（23 行 usage）
$ GET /api/health -> 200
{"agents_loaded":0,"request_executor":{"max_workers":32,"active":0,"queued":0},
 "resources":{"avail_mb":2824,"renderers":0,"verdict":"ok"},"sessions":0,"status":"ok"}
$ GET / -> 200 (embedded dashboard)
$ HIVE_HOME=/tmp/hivehome-fresh ... ./hive serve --port 18787 &
  HIVE_HOME 由 serve 新建：logs/ mcp_registry/ maintenance/ .migrated-v2/
$ ./hive-cred --help       -> exit=0
$ HIVE_BROWSER_BRIDGE=1 ... ./hive serve --port 18787 &
Browser bridge: ready on 127.0.0.1:14930 (pid 209, viewport scale 0.8)
$ GET http://127.0.0.1:14930/health -> 200 {"connected":false,...,"ok":true,"status":"ok"}
RESULT: PASS (20 checks)
```

**linux/amd64（docker 模拟）**：同一脚本 `ARCH=amd64`，容器 `uname -m` = `x86_64`，
20/20 PASS（同为真实 x86_64 ELF 执行，只是宿主为 Apple Silicon 模拟）。
证据：`docker-linux-amd64.log.txt`。

## 5. C 层结果：本机执行（darwin）

- **darwin/arm64 原生**（`native-darwin-arm64.log.txt`）：`install.sh` exit 0 →
  `hive --version` = `hive 1.0.26-go.1`；`--help` / `hive-cred --help` /
  `hive-browser help` / `browserbridge -version` 全部 exit 0；全新 `HIVE_HOME`
  `serve` 后 `/api/health` 200、`/` 200；SIGTERM 退出码 0。
- **darwin/amd64（Rosetta）**（`native-darwin-amd64-rosetta.log.txt`）：确认二进制为
  `Mach-O 64-bit executable x86_64`，经 Rosetta 跑通同一组入口（version/help/
  serve `/api/health` 200、`/` 200、SIGTERM 0）。

## 6. 限制（明确未验证项）

- **windows/amd64、windows/arm64 未执行**：无 Windows 宿主，只做到 A1–A4
  （归档结构、双 SHA256 复算、`install.sh` 装出可执行 `.exe`）。G3 的
  Windows 实机验证仍是 external 项。
- **linux/amd64 为模拟执行**：由 Docker Desktop 在 arm64 宿主上跑 x86_64
  userspace 完成，未覆盖真实 x86 硬件。
- 容器发行版只覆盖 Debian 13；未测 RHEL/Alpine 等（纯 Go 无 cgo，理论上与
  libc 无关，但未实测）。
- 构建用的 dashboard 是主检出预构建 `frontend/dist`（未重跑 npm/tsc）；归档字节
  依赖该 dist 与工具链，跨次重建不保证逐字节一致——校验口径为发布目录
  `SHA256SUMS`。
- `install.sh` 的**远端下载分支**（GitHub release base URL）未测试；本轮只验证
  `--from` 本地归档 + SHA256 门禁。
- 容器内 browserbridge 只验证了启动/健康/回收，没有 Chrome，未驱动真实浏览器
  会话；`hive open`（需要显示环境）未测。
- 容器内解包时 GNU tar 会打印 `Ignoring unknown extended header keyword
  'LIBARCHIVE.xattr.com.apple.provenance'`：macOS `tar` 打包带入的 xattr 扩展头，
  对内容无影响（sha256 已覆盖完整性校验）。
- govulncheck 属另一 lane：结论见 `docs/install-verify.md`（发布物带标准库 CVE，
  当前为已知 FAIL，不因本页复验改变）。

## 7. 本轮新增脚本与证据

新增脚本（仅新增，未改既有脚本/产品代码）：

| 脚本 | 作用 |
| --- | --- |
| `scripts/verify_installer_archives.sh` | A 层：六平台归档静态核对 + `install.sh` 验收 |
| `scripts/docker_installer_verify.sh` | B 层主机侧：选归档、起 linux 容器、挂载证据目录 |
| `scripts/docker_installer_probe.sh` | B 层容器内：C1–C10 探针，命令+输出+退出码写入 transcript |

证据（`docs/e2e-assets/installer-verify/2026-11-22/`）：

| 文件 | 内容 |
| --- | --- |
| `release-build.log.txt` | `release.sh` 全量输出（embed 构建、六平台打包、SHA256SUMS 自校验） |
| `archive-verify.log.txt` | A 层 36 项逐条（含四二进制大小/sha256） |
| `docker-linux-arm64.log.txt` | B 层容器 transcript（20 项，含 C1–C10 命令/输出/退出码） |
| `docker-linux-amd64.log.txt` | 同上，linux/amd64 模拟 |
| `native-darwin-arm64.log.txt` | C 层 darwin/arm64 本机入口 |
| `native-darwin-amd64-rosetta.log.txt` | C 层 darwin/amd64（Rosetta）入口 |

复算方式：`cd go-hive/dist/release && shasum -a 256 -c SHA256SUMS`
（再跑一遍 §2 命令即可重建全部证据）。
