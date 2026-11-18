# Wave 5–8 预置派工单

> 目的：wave 4 一腾出并发槽即可立即派工，不浪费墙钟。每张单都自带所有权与门禁。

## 环境约束（已实测，2026-11-19 00:25）

- 外网不可用：`go install govulncheck@latest` 访问 `proxy.golang.org` 超时。
- 因此 **R5 无法用官方 CVE 扫描工具取证**，替代方案：人工安全审查 + `go list -m all` 依赖清单 + 发布说明标注「未做在线漏洞库比对」。
- `goreleaser` 未安装且不可装；打包用系统自带 `tar`/`zip`/`shasum` 手写脚本即可。

## W5 · 分发与合规（4 lane）

| lane | 范围 | 产物 | 门禁 |
| --- | --- | --- | --- |
| W5-a 打包 | `scripts/release.sh`：六平台 tar.gz/zip + `SHA256SUMS` + `install.sh`（检测平台/架构、解压到 `~/.local/bin`、校验和） | 可执行脚本 + 本地试跑产物 | 解压即运行 `hive --version` |
| W5-b 许可 | 复制上游 `LICENSE`（Apache-2.0）到 `go_hive/go-hive/`，生成 `NOTICE` 与 `THIRD_PARTY_NOTICES.md`（modernc.org/sqlite、yaml.v3、purego、go-isatty 及传递依赖的许可证） | 三份文件 | 每个依赖都有许可证名与来源 |
| W5-c 用户文档 | `docs/quickstart.md`、`docs/configuration.md`、`docs/support-matrix.md`（按 `INTEGRATION_BACKLOG.md` 标注 P0/P1 已实现与未验证项）、`CHANGELOG.md` | 四份文档 | 照文档可从零启动并完成一次会话 |
| W5-d CI/发布 job | `.github/workflows/release.yml`：tag 触发 → 六平台构建 → 打包 → 校验和 → 上传 release | workflow 文件 | `act`/本地 dry-run 语法通过 |

## W6 · 安全与可靠性（3 lane）

| lane | 范围 | 判据 |
| --- | --- | --- |
| W6-a 安全审查 | 路径穿越（附件/技能/会话/导入 tar）、命令注入（terminal、reveal、webhook）、密钥落盘权限（0600）、日志脱敏（诊断包）、HTTP 输入长度与类型 | 每条给出 `文件:行` 与结论；有缺陷必须附修复或明确风险 |
| W6-b 依赖与供应链 | `go list -m all` 全量清单、直接依赖版本锁定、`go.sum` 完整性、禁止运行时下载的验证（`GOPROXY=off` 构建） | 清单 + 离线构建证据 + 在线扫描缺失声明 |
| W6-c 可靠性 | 超时（LLM/工具/HTTP）、并发（会话/worker/SSE）、资源回收（goroutine/文件句柄）、崩溃恢复（损坏 state.json/part/索引） | 故障注入测试，逐项 PASS/FAIL |

## W7 · P1 模块批量（按需 5–8 lane）

顺序：`notion`(13) → `linear`(21) → `jira`(9) → `postgres`(8) → `redis`(11) → `google_sheets`(10) → `google_docs`(11) → `gmail`(10)。
每 lane 2–3 个模块，只新增 `batch4_*.go`，导出 `InstallBatch4(target)`；每个 action 至少一条 fake 上游测试。

## W8 · 发布候选（协调者）

1. 全量门禁四件套：`make ci` + `qa_audit.py` + `qc_check.py --smoke --full` + `timegov.py`。
2. 打包产物冒烟：解压 → `hive --version` → `serve` → `/` 200 → 创建会话 201。
3. 打 tag `v1.0.14-go.1`，生成 release notes（含未验证项与离线扫描缺失声明）。

## 红线复述

- 未验证不得写成已验证；支持矩阵必须列出未验证项。
- 无外网 = 无在线 CVE 扫描，这不是可以省略的说明，必须进 release notes。
