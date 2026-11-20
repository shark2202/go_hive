# 发布检查单：hive 1.0.26-go.1

> 出口标准定义见 `RELEASE_PLAN.md` §1（R1–R10）。
> 检查对象：`hive 1.0.26-go.1`；§2 与 R9 为 2026-11-20 本工作树（`release-ci-contract`，main `853a15e9` 之上）的复跑结果，其余行为 2026-11-19 快照。
> 状态口径：**PASS** = 判据满足且有本次可复现证据；**部分** = 有实现/部分证据但判据未完整满足；**阻塞** = 缺外部条件，代码无法补齐。

## 1. 逐项状态

| # | 验收面 | 判据 | 状态 | 证据命令（在 `go_hive/` 或 `go_hive/go-hive/` 下） | 本次结果 |
| --- | --- | --- | --- | --- | --- |
| R1 | 功能完整性 | 104/104 集成模块、原生工具齐全，或发布支持矩阵并明示缺口 | **PASS（仅集成模块与支持矩阵口径；概念机制对齐未满足，见 `DELIVERY.md` §1.1）** | `go test ./internal/cli -run 'TestSessionToolCatalogue' -v`；`go test ./internal/tools/integrations/...`；见 `docs/support-matrix.md` §2 | 102 模块 / 854 action（23 个安装器）+ `senders` 1/9 = **103 模块 / 863 action**；模块面 **102+1/104**（`file_system_toolkits` 非工具模块）；2 个 DuckDB SQL action 有意排除；`attach_file` 多模态限制已声明 |
| R2 | 分发形态 | 六平台归档 + SHA256 + 安装脚本；单二进制自带 UI | **PASS** | `bash scripts/embed_ui.sh && bash scripts/release.sh`；`cd go-hive/dist/release && shasum -a 256 -c SHA256SUMS`；`bash install.sh --from go-hive/dist/release --dir <tmp>` | 6 归档 + `SHA256SUMS`；六个 `OK`；`install.sh` 双重校验后安装成功；解压 darwin-arm64 后 `hive --version` = `hive 1.0.26-go.1` |
| R3 | 文档 | 用户 README、快速开始、配置参考、支持矩阵、变更日志 | **PASS** | `ls docs/`；`grep -n "103\|863\|DuckDB\|HIVE_NETWORK_SCANNERS" docs/*.md` | `README.md`、`docs/quickstart.md`、`docs/configuration.md`、`docs/support-matrix.md`、`docs/CHANGELOG.md`、`docs/release-notes-v1.0.26-go.1.md`；本次统一为 103/863 口径 |
| R4 | 许可合规 | Apache-2.0 LICENSE、NOTICE、第三方依赖与许可证清单 | **满足** | `shasum -a 256 LICENSE` 与归档内 `LICENSE` 对比；`ls NOTICE THIRD_PARTY_NOTICES.md` | LICENSE 逐字节一致（`2cda9484…9748de`）；`NOTICE` 为仓库内正式文件（1,203 B）；`THIRD_PARTY_NOTICES.md`（10,766 B，28 Go + 27 前端直接依赖）现已随归档分发 |
| R5 | 安全 | 路径穿越/命令注入/密钥落盘/日志脱敏审查；`govulncheck` 无高危 | **部分** | `grep -rn "govulncheck\|osv\|nancy" Makefile .github/workflows/`；`less SECURITY_REVIEW.md` | 人工审查与加固记录在 `SECURITY_REVIEW.md`；**在线 CVE 扫描未做**（Makefile/CI 无 `govulncheck`/OSV/nancy），已在支持矩阵与发布说明中声明 |
| R6 | 可靠性 | 超时、并发、资源回收、崩溃恢复有测试；无已知数据损坏路径 | **PASS** | `go test -race -count=1 ./...`；`RELIABILITY_REPORT.md` | `-race` 退出码 **0**，37 包 `ok`；故障注入 5 项记录在 `RELIABILITY_REPORT.md`（本次未复跑注入，只复跑 `-race`） |
| R7 | 可观测 | `--version`、`/api/health`、结构化日志、诊断包 | **PASS** | `./hive --version`；`curl -sS localhost:PORT/api/health`；`curl -X POST .../api/sessions/<id>/report-bundle` | 前三项本次实测（`--version`、health 200、`[access] GET … -> 200 (0ms)`）；`report-bundle` 路由与测试存在（`internal/server/routes_session_report*`），本次未实调 |
| R8 | 质量证据 | `make ci` + smoke + e2e + 跨平台 + parity + QA/QC 报告 | **PASS** | `make ci`；`go test -tags e2e -count=1 ./e2e/...`；`bash scripts/smoke.sh <port>`；`bash scripts/check_parity.sh .`；`bash scripts/check_parity_targets.sh . ../../core` | `make ci` 退出码 0；4,256 测试声明；`make test` 退出码 0（38 包 ok、0 fail）；e2e `ok 2.064s`；交叉编译 6/6；parity 4256/4256；refs 4,489 0 missing；源码 smoke **13/13**（端口 8803） |
| R9 | 发布流程 | tag、release notes、CI release job、制品上传 | **workflow 已对齐，Actions 未跑** | `python3 -c "import yaml,pathlib; yaml.safe_load(pathlib.Path('.github/workflows/release.yml').read_text())"`；`go run github.com/rhysd/actionlint/cmd/actionlint@latest .github/workflows/release.yml`；本机逐步仿真（见本次结果） | `release.yml` 已按现行 `release.sh` 契约重写：4 job（meta/build/verify/publish），`VERSION/DIST/RELEASE_DIR/FRONTEND_DIST` 环境变量 + 仓库根调用 `go_hive/scripts/release.sh`，产物为六平台归档 + `SHA256SUMS`，发布资产里的 `install.sh` 取自 `go_hive/install.sh`；YAML 解析 OK、actionlint 0 问题、本机逐步仿真全 PASS（六平台归档构建、`sha256sum -c`、归档冒烟 13/13、内嵌 dashboard `/` 200、`install.sh` 安装成功）；**GitHub Actions 从未运行**，未打 tag、未上传制品，不得按通过处理 |
| R10 | 真实集成验证 | 关键模块至少各有一条真实凭据的端到端记录 | **不适用（用户决定 2026-11-19）** | 本阶段不提供第三方工具凭据，仅提供 LLM 凭据；以支持矩阵的「未验证」声明为准 | 无任何真实第三方调用记录；全部集成仅对假上游验证 |

## 2. 发布产物（本机实测）

2026-11-20 在本工作树（`release-ci-contract`，main `853a15e9` 之上）按 `release.yml` 的调用方式重跑。目录：`dist/release/`（`release.sh` 默认输出在 `go_hive/go-hive/dist/release/`）。生成命令（仓库根）：

```bash
VERSION=1.0.26-go.1 DIST="$PWD/dist" RELEASE_DIR="$PWD/dist/release" \
  FRONTEND_DIST="$PWD/go_hive/go-hive/frontend/dist" bash go_hive/scripts/release.sh
( cd dist/release && shasum -a 256 -c SHA256SUMS )   # Linux 上用 sha256sum -c
```

| 归档 | 字节 | SHA-256 |
| --- | ---: | --- |
| `hive-1.0.26-go.1-darwin-amd64.tar.gz` | 29,469,556 | `c525054a0e6694324e90500c144cd2a01e494c230243b6b8e06cc317592677f1` |
| `hive-1.0.26-go.1-darwin-arm64.tar.gz` | 27,760,344 | `0410b30010ea1e398901cf035eaf9381fea3ef69f12cd27f2c29acf3418f684b` |
| `hive-1.0.26-go.1-linux-amd64.tar.gz` | 29,481,538 | `deb1bd4420b4d7e2de4514c92c596ade5ae2a309fe66f37c37295baa8d95d52e` |
| `hive-1.0.26-go.1-linux-arm64.tar.gz` | 27,187,310 | `15b568d88989119bf0244fe16f19b8e6f963df5a83cf7d278f840411f5082af2` |
| `hive-1.0.26-go.1-windows-amd64.zip` | 29,628,892 | `10ae2777b533b87ec31f18af12e1c8c20c6a30c010a2c4cfdc818abc58627cd5` |
| `hive-1.0.26-go.1-windows-arm64.zip` | 27,078,613 | `b9549b9e2f48971372a87ca5c6448bf8f1329df8a50e719120396206ec63949c` |
| `SHA256SUMS` | 612 | 上述六项（`shasum -a 256 -c` 六项全 `OK`） |

说明：本轮归档比 2026-11-19 表内数值大约翻倍，原因是 11-20 的 `c45473b3` 起归档才纳入 hive-browser / browserbridge 两个约 8 MB 级的二进制（hive-cred 更早加入），旧表是内容更少的旧快照；tar/zip 带文件时间戳，**哈希随构建变化、不可复现**，上表是本轮实测值，重跑即得新值。上一轮快照的哈希（含发布说明 §4.2）不再代表当前制品。

## 3. 发布包验收（darwin-arm64）

```bash
mkdir -p /tmp/hive-rel && tar -xzf dist/release/hive-1.0.26-go.1-darwin-arm64.tar.gz -C /tmp/hive-rel
/tmp/hive-rel/hive --version                       # hive 1.0.26-go.1
HIVE_HOME=/tmp/hive-rel/home /tmp/hive-rel/hive serve --port 8797
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8797/    # 200
```

本轮实测（2026-11-20）：`--version` = `hive 1.0.26-go.1`；`/` 200（HTML，内嵌 dashboard）、`/api/health` 200；`serve` 打印 `Dashboard:` 行；SIGTERM 优雅退出；`bash go_hive/scripts/verify_release_archive.sh dist/release` = 13/13 PASS；`bash go_hive/install.sh --from dist/release --version 1.0.26-go.1` 四个二进制安装成功。`/sessions`、`/favicon.png` 为上一轮记录，本轮未复跑。

## 4. 未满足项与风险

| 项 | 影响 | 处理 |
| --- | --- | --- |
| ~~R4 缺 `THIRD_PARTY` 依赖清单~~ | 已解决：`go_hive/go-hive/THIRD_PARTY_NOTICES.md` 存在，`release.sh` 已改为随归档分发 | — |
| R5 无在线 CVE 扫描 | 依赖漏洞状态未知 | 已在支持矩阵 §5/§6 与发布说明 §5 明示；有外网时补 `govulncheck ./...` |
| R9 Actions 未跑（workflow 已对齐） | CI 路径没有执行证据；本机仿真通过不等于 Actions 通过 | 有发布权限后：`git tag v1.0.26-go.1 && git push origin v1.0.26-go.1`（触发 `release.yml`），或用 workflow_dispatch（`dry_run=false`，`draft` 可先留 true 人工过目） |
| R10 无真实凭据 | 集成工具只有假上游证据 | 已决定本阶段不验证（用户 2026-11-19）；发布时以支持矩阵「未验证」声明为准，不得写成可用 |

## 5. 结论

**可作为 `1.0.26-go.1` 快照发布**：R1、R2、R3、R6、R7、R8 满足；R4、R5 为部分满足且已在文档中明示；R9 仅到「workflow 已对齐，Actions 未跑」，不构成 CI 通过；R10 按用户决定本阶段不验证（以支持矩阵声明代替），不作为本版发布声明的一部分。
