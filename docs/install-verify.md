# 安装 / 升级端到端复验（G3）

本页是 `go_hive/scripts/install_verify.sh` 与 `go_hive/scripts/vulncheck.sh` 的
运行说明和实测记录。两个脚本覆盖 G3 中**不依赖外部环境**的两项：

1. **依赖漏洞门禁**（`make vulncheck`）：有 `govulncheck` 就真扫；扫不了就明确
   `UNVERIFIED` 并以退出码 3 结束，**绝不假绿**。
2. **安装/升级端到端**（`make install-verify`）：在临时 `HOME` 里通过
   `go_hive/install.sh` 安装已发布 slot 归档（SHA256 门禁），校验四二进制、版本、
   冷启动 health 200；再用上一交付的 1.0.26 home 做原地升级，做数据级无损比对。

`install.sh` 的存在性已用 `git grep -l install.sh` 核实（仓库内为
`go_hive/install.sh`，发布资产同名文件取自该处）。

## 1. 依赖漏洞门禁

```bash
cd go_hive/go-hive
make vulncheck
# 等价：bash go_hive/scripts/vulncheck.sh
#   可选：--module DIR（重复） --report DIR --binary-dir DIR --offline
#         --no-install --strict
```

行为：

| 条件 | 行为 | 退出码 |
| --- | --- | --- |
| `govulncheck` 可用（PATH / `GOVULNCHECK_BIN` / `~/.cache/hive-vulncheck`） | 对 `go-hive` + `browserbridge` 跑 `-json ./...`（source/symbol），汇总 unique OSV | 0 无发现 / 1 有可达漏洞 |
| 工具缺失且允许安装 | `GOBIN=<独立缓存> go install golang.org/x/vuln/cmd/govulncheck@latest`（仓库 `go.mod`/`go.sum` 不变），再扫描 | 同上 |
| 工具缺失且 `--offline`/`--no-install`，或安装失败，或某模块扫描报错 | 跑 `scripts/vuln_offline_review.py`（stdlib-only：go.mod/go.sum/`go mod verify`/`go version -m` 清单对照），输出 **UNVERIFIED** | 3 |
| 用法/环境错误 | 直接报错 | 2 |

`--strict` 把 UNVERIFIED 变成 1，供“没有真扫就不许过”的门禁使用。CI 里
（`.github/workflows/ci.yml` 的 `go-vulncheck` job）直接跑 `make vulncheck`，
不加 `continue-on-error`：工具/数据库不可达时 job 会红，而不是绿色。

### 1.1 本次实测（2026-11-21，darwin/arm64）

```text
govulncheck v1.10.0 · DB vuln.go.dev 2026-11-11T02:51:11Z · source/symbol · go1.26.5
go-hive:       23 unique OSV / 778 finding events / 13 SBOM modules
browserbridge: 23 unique OSV / 608 finding events /  2 SBOM modules
verdict: FAIL (exit 1)
```

23 个 unique OSV 全部落在 **Go 标准库**（net/http、crypto/tls、crypto/x509、
html/template、encoding/asn1、encoding/xml、net/url、net/textproto 等），
fixed in `go1.26.6` / `go1.26.9` / `go1.26.10` / `go1.26.11`；没有第三方模块
finding。明细见 `docs/e2e-assets/vulncheck/20261121/SUMMARY.md`。

对**已发布归档二进制**复扫（`govulncheck -mode=binary`，slot `20261121-18` 的
`hive`）：**18 个标准库可达漏洞**，exit 3；另有 4 条标准库 advisories 存在于
包/模块层但未被调用。即发布物本身确实带上这些 CVE，不是“只有源码扫描环境”。
日志：`docs/e2e-assets/vulncheck/20261121/binary-scan-slot18.log.txt`。

**结论**：门禁本身工作正常，当前结果是 **FAIL**。仓库 `go.mod` 的 `go` 指令是
`1.26.5`（发布/CI 都按它取工具链），所以这不是“只有本机旧工具链”的问题。
修复动作（本车道未做，改 pinned toolchain 属需要批准的依赖变更）：
把 `go 1.26.5` 提升到已修复版本（≥ `1.26.11`）并按发布节奏重建 slot 归档，
然后复跑 `make vulncheck`。

离线降级实测：`--offline` 在同一棵树上 exit **3 = UNVERIFIED**，清单审查
（V1–V5）全 PASS，V6 明确“未查 OSV/CVE”。日志：
`docs/e2e-assets/vulncheck/20261121/offline-fallback-demo.log.txt`。

## 2. 安装 / 升级端到端

```bash
cd go_hive/go-hive
make install-verify
# 等价：bash go_hive/scripts/install_verify.sh \
#   --archive-dir go_hive/dist/slots/20261121-18 \
#   --old-dir    go_hive/dist/slots/20261121-16
#   可选：--install-sh PATH --work DIR --out FILE --port-base N --skip-upgrade
```

`dist/slots` 是 gitignore 的构建产物；在独立工作树里没有归档时，显式传
`--archive-dir/--old-dir`（指向主检出的 `go_hive/dist/slots/<slot>`）。

### 2.1 检查清单（脚本逐条 PASS/FAIL，I4 为 SKIP）

| id | 检查 | 说明 |
| --- | --- | --- |
| I1 | `manifest.json` 解析 | slot 构建的 version / build_version |
| I2 | 归档 sha256 + 字节数 == manifest | 独立于 install.sh 的第二来源 |
| I3 | 归档含 `hive`/`hive-cred`/`hive-browser`/`browserbridge` | 四二进制必须同目录 |
| I4 | 归档内层 `SHA256SUMS` 校验 | slot 归档是纯二进制包，无内层文件 → SKIP（release.sh 归档有，会执行） |
| I5 | `install.sh --from …` 接受归档 | 必须出现 `verified SHA256` |
| I6 | 四二进制安装到 `--dir` 且可执行 | 临时 `$HOME/.local/bin` |
| I7 | `hive --version` == manifest.version | 防止装错版本 |
| I7b | `hive-cred --help` / `hive-browser help` / `browserbridge -version` | 退出码 0 |
| I8 | 冷启动：`/api/health`、`/`、`/api/sessions/history` 均 200 | 全新 `HIVE_HOME`，`HIVE_BROWSER_BRIDGE=0` |
| I8b | 冷启动 serve 收到 SIGTERM 正常退出 | 煞尾 |
| I9 | **负向对照**：篡改 `SHA256SUMS` 必须被拒绝 | 证明 checksum 门禁不是装饰 |
| I10 | 上一交付（old slot）二进制 + 归档存在 | 默认取 `--archive-dir` 之外的次新 slot |
| I10b | old 归档经 `install.sh` 安装成功 | 报告其版本 |
| I11 | v2 旧版 `HIVE_HOME` 夹具生成 | 复用 `storage_compat_gen_fixture.py` 的夹具思路，脚本独立 |
| I11b | old 二进制迁移并读取夹具 | health 200；queen-only 会话 + colony 可见 |
| I12 | 原地升级：同目录替换为 new 构建 | 安装后 `hive` sha256 == 新归档内 `hive` |
| I13 | 升级后 new 二进制服务同一 home | health 200；会话仍在；colony session_count 与升级前一致；`.migrated-v2`/`.migrated-v3`/`colonies/.migrations/colony_sessions_v1` 在 |
| I14 | 用户数据无损 | 快照 diff：0 新增 / 0 删除 / 0 非白名单修改；白名单仅 `logs/`、`*.log`、`*.db-wal`、`*.db-shm`、`maintenance/server.lock` |

### 2.2 本次实测（2026-11-21，darwin/arm64）

| 项 | 值 |
| --- | --- |
| new 归档 | `20261121-18/hive-1.0.26-go.1+build.2026112118-darwin-arm64.tar.gz`，sha256 `4962451131dc6245…`，48,956,018 B（与 manifest 一致）；manifest 记录构建 head `f4c75566`，基线 `982136f8` 仅其后文档提交 |
| old 归档 | `20261121-16/…build.2026112116-darwin-arm64.tar.gz`（`hive 1.0.26-go.1`） |
| 安装 | `install.sh` 校验 SHA256 通过；四二进制装到临时 `$HOME/.local/bin` |
| 冷启动 | health 200、`/` 200、history 200；SIGTERM 正常退出 |
| 负向对照 | 篡改 SHA256SUMS → `install: ERROR: SHA256 mismatch …`，安装被拒绝 |
| 升级 | 同一安装目录替换为新构建（sha256 匹配）；旧 v2 home 的健康检查/会话/colony/迁移 marker 全部保留 |
| 数据无损 | 快照 62 文件：0 added / 0 removed / 1 allowlisted modified（`logs/hive-server.log`），**0 用户数据改动** |
| 结果 | **17 PASS / 0 FAIL / 1 SKIP**（I4，slot 归档无内层 SHA256SUMS），退出码 0 |

证据目录：`docs/e2e-assets/install-verify/`（`report.json`、`run.log.txt`、
`snapshot-diff.json`、`serve-{old,new}.log.txt`、`install-{new,old,upgrade,negative}.log.txt`）。

## 3. 未验证项（本轮明确不做 / 无环境）

| 项 | 状态 | 说明 |
| --- | --- | --- |
| Windows 安装/升级 | **未验证** | 无 Windows 实机；`install.sh` 的 `zip`/`.exe` 分支只做了代码审查 |
| Linux 安装/升级 | **未验证** | 仅 darwin/arm64 实跑；脚本本身只用 POSIX 工具 + `shasum`/`sha256sum` 分支 |
| GitHub Actions 实跑 | **未验证** | 已加 `go-vulncheck` job，但本环境无 Actions 执行权限/运行记录 |
| 真实 browser bridge | 未覆盖 | 本演练设 `HIVE_BROWSER_BRIDGE=0`，桥的接管/回收由 `scripts/verify_release_archive.sh` 单独覆盖 |
| 凭据跨版本 | 未覆盖 | v2 夹具不含凭据；凭据互读/回滚由 `upgrade_compat_check.sh`（v1/v2/v3 + Fernet 夹具）覆盖 |
| 归档内层 SHA256SUMS | SKIP | slot 归档按设计纯二进制；发布级 `release.sh` 归档有内层文件，届时 I4 会实跑 |
| 六个发布归档（含 zip） | 未验证 | 本机只有 `darwin-arm64` slot 归档；六平台归档由 `release.sh`/Actions 产出 |

## 4. 复现

```bash
# 依赖漏洞门禁（有网/有工具时真扫；否则 exit 3 UNVERIFIED）
cd go_hive/go-hive && make vulncheck

# 离线环境强制走降级审查（仍 exit 3）
bash ../scripts/vulncheck.sh --offline

# 安装 + 升级端到端（主检出里默认自动选最新/次新 slot）
cd go_hive/go-hive && make install-verify

# 独立工作树（无 dist/slots）显式指定归档
bash go_hive/scripts/install_verify.sh \
  --archive-dir /Users/mac/hive/go_hive/dist/slots/20261121-18 \
  --old-dir     /Users/mac/hive/go_hive/dist/slots/20261121-16 \
  --work /tmp/hive-install-verify --out /tmp/install-verify-report.json
```
