# 商业外发布计划（G6 标准）

> 定义「达到可对外商业发布」的验收面。当前只满足其中第 8 项的一部分，其余按 wave 推进。

## 1. 出口标准（全部满足才算可发布）

| # | 面 | 判据 | 现状 |
| --- | --- | --- | --- |
| R1 | 功能完整性 | 104/104 集成模块、原生工具齐全，或发布支持矩阵并明示缺口 | **代码面已齐**：102 integrations 模块 + senders = 103/104（`file_system_toolkits` 非工具模块）；863/865 action，2 个 DuckDB SQL action 有意排除；`attach_file` 多模态只到工具结果层（chat runtime 仅传文本，已声明） |
| R2 | 分发形态 | 六平台归档 + SHA256 + 安装脚本；单二进制自带 UI | **已完成**：`release.sh` 实测产出 6 归档 + SHA256SUMS，`install.sh` 校验/拒装均验证；`hive-embed` 实测 `/`、深链、assets 200 |
| R3 | 文档 | 用户 README、快速开始、配置参考、支持矩阵、变更日志 | **已完成**：`docs/` 四份合并，命令均实跑取证 |
| R4 | 许可合规 | Apache-2.0 LICENSE、NOTICE、第三方依赖与许可证清单 | **已完成**：LICENSE 逐字节副本 + NOTICE + THIRD_PARTY（28 Go / 27 前端直接依赖） |
| R5 | 安全 | 路径穿越/命令注入/密钥落盘/日志脱敏审查；`govulncheck` 无高危 | **人工审查完成**：14 类加固 + `SECURITY_REVIEW.md`；在线 CVE 扫描因无外网未做（已声明） |
| R6 | 可靠性 | 超时、并发、资源回收、崩溃恢复有测试；无已知数据损坏路径 | **已完成**：故障注入 5 项全 PASS，修 11 处真实缺陷，`-race` 全仓通过 |
| R7 | 可观测 | `--version`、`/api/health`、结构化日志、诊断包 | 已有前三项 |
| R8 | 质量证据 | `make ci` + smoke + e2e + 跨平台 + parity + QA/QC 报告 | **QC 8/8 PASS**（`qc_check.py --full --smoke`）：4,160 测试、4,357 parity 引用 0 失效、prod 830 条 0 missing、六平台产物、cgo 0、smoke 13/13 |
| R9 | 发布流程 | tag、release notes、CI release job、制品上传 | **已完成**：`release.yml` 5 job（meta/frontend/build×6/package/publish），语法与 shell 片段实跑校验 |
| R10 | 真实集成验证 | 关键模块至少各有一条真实凭据的端到端记录 | **不适用（用户决定，2026-11-19）**：本阶段只提供 LLM 凭据，不提供第三方工具凭据。发布以支持矩阵的「未验证」声明为准；仅对无需凭据的工具做真实验证 |

## 2. 剩余 wave 划分

| Wave | 范围 | 责任 | 出口 |
| --- | --- | --- | --- |
| W4（进行中） | P0 原生工具（terminal/search/charts）+ P0 模块（excel/pdf/scrape/senders/image/search）+ qmail/agent-spec/embed | 9 lane + QA 审计 | 合并后 `make ci` + smoke 全绿 |
| W5 | 打包（六平台 tar/zip + SHA256 + install.sh）、LICENSE/NOTICE、用户文档、变更日志 | 4 lane | 产物可解压即用；文档可照做 |
| W6 | 安全与可靠性：`govulncheck`、路径/命令/密钥审查、故障注入、限流与超时回归 | 3 lane | 无高危；故障路径有测试 |
| W7 | P1 模块批量（Notion/Linear/Jira/Postgres/Redis/Google 系…） | 5–8 lane | 模块数 ≥40 |
| W8 | 全量回归 + 发布候选 + tag + release job | 协调者 | G6 全表通过（R10 除外交付支持矩阵） |

## 3. 不可绕过的红线

1. **R10 不再作为阻塞项**：用户已明确本阶段不提供第三方工具凭据。发布口径改为「无需凭据的工具已真实验证 + 需凭据的集成在支持矩阵标注未验证」，不得写成已可用。
2. 前端 14 MB 资产必须 embed 或随包分发，二者择一并在文档写明。
3. 任何「已完成」以可复现命令输出为准，不以 lane 自报为准（QA/QC 双轨执行）。
4. 到 11-19 16:00 硬封版后只修缺陷；未完成项进支持矩阵，不延期。

## 4. 发布包结构（目标）

```
hive-1.0.25-go.1-darwin-arm64.tar.gz
  ├── hive                 # 单二进制（含 UI）
  ├── LICENSE
  ├── NOTICE
  ├── README.md
  └── SHA256SUMS
install.sh                 # 检测平台 → 解压 → 安装到 PATH
```
