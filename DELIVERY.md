# 交付报告（2026-11-19 13:50）

> 口径：`RELEASE_PLAN.md` 的 G6 商业发布标准（R1–R10）。
> 代码快照：主线 `c3880106`（tag `v1.0.15-go.1` 为 11:35 的发布快照，此后已补 `run_worker`、tracker、提醒中枢、judge、真流式）。

## 1. 结论

| 判定 | 结果 |
| --- | --- |
| R1 功能完整（与上游行为对齐） | **不满足**：按 `ed457d75` 重审为 **48 已接 / 10 部分 / 22 未接**（80 行）。已接含 `run_worker`、tracker、提醒中枢、judge、OpenAI-compatible 真流式；未接：Sentinel、`write_skill`、triggers/`QueenExecutor`、playbook runner、`resume_worker_ids`、`search_messages`/`stall_check`/并行只读工具。已知缺陷：overseer 工具面缺 `run_worker`/tracker/task；`serve --colony` 预载会话无 ChatRuntime；fork 的 `worker.json` 把 Queen persona 写成 worker system prompt；`worker.json.tools` 未应用 |
| R2 分发形态 | 满足：六平台归档 + SHA256SUMS + 安装脚本，解压即用 |
| R3 用户文档 | 部分满足：快速开始/配置/支持矩阵齐备，但支持矩阵明确标注 81 个需凭据模块「未验证」 |
| R4 许可 | 满足：Apache-2.0 LICENSE + NOTICE |
| R5 安全 | 部分满足：已修 3 个真实漏洞（附件读写 symlink 逃逸、任务体无限流、`/api` 点段 307），但**未做在线漏洞库比对**（`govulncheck` 装不上） |
| R6 可靠性 | 满足：`-race` 全绿、故障注入与并发修复已合入 |
| R7 可观测 | 满足：会话事件链、SSE、`/stats` 字段对齐、诊断包 |
| R8 质量证据 | 满足：七道门禁、4,313 测试、QC 8/8、smoke 13/13、六平台归档校验 |
| R9 发布流程 | 满足：版本、CHANGELOG、发布说明、tag、归档 |
| R10 真实第三方集成 | 不适用（用户决定 2026-11-19）：本阶段不提供第三方凭据，以支持矩阵「未验证」声明代替 |
| **总判定** | **可运行的移植快照（试用版），不是「完整复刻交付」**。工程发布条件（构建/测试/打包/安全修复）成立；行为对齐条件（概念机制生产可达）不成立 |

> 纠正记录（2026-11-19）：本文件此前把 R1–R9 整体标为「满足」，把「门禁全绿 + 归档可用」误当成「行为已对齐」。这是口径错误——门禁测的是代码质量与移植完整性，不测概念机制是否在生产路径上生效。

## 1.1 未对齐清单（对外必须同时声明）

| 概念 | 状态 | 生产缺口 |
| --- | --- | --- |
| queen | 可用 | 已有 `run_worker` 按需扩 worker；剩 per-queen 工具白名单、playbook 运行时 |
| colony | 部分 | `run_worker` 与 tracker 已接；scaffold 不建 `tracker/`、`skills/`、`triggers.json`；定时唤醒与重启恢复未接 |
| worker_agent | 部分 | `run_worker → SpawnBatch` 生产可达并回传 `[WORKER_REPORT]`；剩 `resume_worker_ids`、worker 转录在内存、overseer 工具面无 `run_worker` |
| coordination | 部分 | tracker 写路径、提醒中枢（idle/task/parallel）已接；剩 Sentinel、park/resume 完整语义 |
| goals_outcome | 部分 | judge 对带 `success_criteria` 的 worker 生效并发布 `judge_verdict`；chat 无 criteria，playbook 判定未接 |
| improvement | 部分 | 技能写入（`WriteSkill`）、instinct/lessons 沉淀未接 |
| the_loop | 可用 | 真流式、提醒注入、judge 阶段已接；剩 playbook 循环与触发恢复 |
| 集成工具 | 未验证 | 81 个需第三方凭据的模块无真实调用；仅 26 个免凭据模块离线验证 |


## 2. 已验证证据（可复现）

| 项 | 结果 | 复现命令 |
| --- | --- | --- |
| 全量门禁 | 退出码 0（fmt/vet/test/cross/parity/parity-targets/wiring-check） | `cd go_hive/go-hive && make ci` |
| 测试 | **4,313**（全部带 parity 注解或豁免） | 同上 |
| parity 引用 | **4,582** 条，0 失效文件 / 0 失效测试；含生产代码 **943** 条 0 失效符号 | `make parity-targets` |
| QC 全量 | **8/8 PASS**（含重跑 make ci 与冒烟） | `uv run --no-project python go_hive/scripts/qc_check.py --full --smoke` |
| 集成冒烟 | **13/13 PASS**（真二进制 + 真 HTTP） | `bash go_hive/scripts/smoke.sh` |
| 发布包 | 6 归档 + SHA256SUMS，`shasum -c` 全 OK | `bash go_hive/scripts/release.sh` |
| 归档可用性 | 解压 darwin-arm64 → `--version` 正确，`/`、`/api/health`、`/assets/*` 全 200 | 见 §5 |
| cgo | `import "C"` 计数 **0** | `grep -rn 'import "C"' --include=*.go .` |
| 版本 | `hive 1.0.15-go.1` | `./dist/hive --version` |

## 3. 覆盖面

| 子系统 | 交付内容 |
| --- | --- |
| 集成模块 | **102 integrations + senders = 103/104**（`file_system_toolkits` 非工具模块），**863/865 action**；23 个安装器全部接入会话工具表，并有「Install\* 必被引用」守护测试 |
| HTTP 路由 | 194 条 method+path；`NewApp` 注册 113 条 pattern |
| Sentinel | 8 模块移植，100 测试 |
| Colony | spawn → 多 worker（并发上限/排队晋升）→ 汇总 → 终态；agent-spec 解析器已接入 |
| 前端 | React 构建产物 embed 进 `hive-embed`，单二进制自带 dashboard（实测 `/`、深链、assets 200） |
| 分发 | 六平台归档 + SHA256SUMS + `install.sh`（篡改拒装已验证） |
| 文档 | quickstart / configuration / support-matrix / CHANGELOG / release-notes / 检查单 |
| 许可 | LICENSE（Apache-2.0 逐字节）+ NOTICE + THIRD_PARTY_NOTICES（随归档分发） |

## 4. 未满足与已声明差异

### 4.1 R10：真实第三方验证（本阶段不适用）
- 863 个 action 只有 `httptest`/假上游证据；**没有任何一次真实厂商调用**。
- 用户已明确本阶段不提供第三方工具凭据，因此该项不再作为交付阻塞；发布以支持矩阵的「未验证」声明为准。
- 无需凭据的 26 个模块（终端/文件/csv/excel/pdf/arxiv/wikipedia/openmeteo 等）可真实验证。

### 4.2 结构性差异（非遗漏，已声明）
1. `attach_file` 的多模态只到工具结果层：`ToolResult.Attachments` 与 `ImageContent` 已实现，但 `serve` 的 chat runtime 只把文本传给模型。
2. `csv_sql` / `excel_sql` 依赖 DuckDB，明确返回未知 action，不做 stub。
3. `account` 别名参数在多个模块保留但不生效（单凭据运行时）。
4. 六个网络扫描器默认关闭，需 `HIVE_NETWORK_SCANNERS=1` 或配置开启。
5. 在线 CVE 扫描未做（`govulncheck` 因无外网不可安装），依赖清单与离线构建证据在 `DEPENDENCIES.md`。

## 5. 发布包与验收

```
go_hive/go-hive/dist/release/
  hive-1.0.15-go.1-darwin-amd64.tar.gz   13.8 MB
  hive-1.0.15-go.1-darwin-arm64.tar.gz   13.0 MB
  hive-1.0.15-go.1-linux-amd64.tar.gz    13.8 MB
  hive-1.0.15-go.1-linux-arm64.tar.gz    12.8 MB
  hive-1.0.15-go.1-windows-amd64.zip     13.8 MB
  hive-1.0.15-go.1-windows-arm64.zip     12.8 MB
  SHA256SUMS
```

归档根目录：`hive[.exe]  LICENSE  NOTICE  THIRD_PARTY_NOTICES.md  README.md  SHA256SUMS`。
独立验收（解压后直接运行）：`--version` = `hive 1.0.15-go.1`；`serve` 后 `/` 200、`/api/health` 200、`/assets/index-*.js` 200。

## 6. 未做（需发布权限或外部条件）

1. **tag 已打但未推送**：`v1.0.15-go.1` 指向 `d1111162`；`git push origin v1.0.15-go.1` 会触发 `release.yml` 上传 Release。
2. 代码签名/公证（macOS notarization、Windows Authenticode）未做，需证书。
3. R10 按决定不验证：以支持矩阵「未验证」声明为准，不写成可用。
