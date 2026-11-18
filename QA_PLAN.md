# 全流程全域质量保障：QA（过程）+ QC（结果）

> 分工：QA 管「怎么做的」，QC 管「做出来的是什么」。两者都不由实现 lane 自证。

## 1. QA · 过程质量（在过程中拦住）

| 控制点 | 时机 | 判据 | 证据 |
| --- | --- | --- | --- |
| Q1 基线与所有权 | 派工前 | 从 `make ci` 绿的 HEAD 派生；文件所有权清单明确 | `git worktree list`、`COORDINATION.md` §3 |
| Q2 SOP 合规 | lane 执行中 | 逐文件提交、包级符号带前缀、不改共享装配文件 | `scripts/qa_audit.py` |
| Q3 lane 自检 G1 | 提交前 | build + 包测试 + gofmt + parity 注解完整 | lane 返回的门禁输出 |
| Q4 合并门禁 G2 | 合并时 | `make ci` 退出码 0 | 协调者实跑 |
| Q5 集成门禁 G3 | 装配后 | `smoke.sh` 全 PASS + e2e | `scripts/smoke.sh` |
| Q6 复核门禁 G4 | 波次末 | parity 引用真实存在；豁免理由站得住 | `QC_REPORT.md` |
| Q7 时间与预算 | 持续 | 阶段预算、等待时间、决策延迟不越线 | `scripts/timegov.py` |
| Q8 停止时点 | 到点 | 范围冻结后只修缺陷 | `TIME_POLICY.json` |

**过程红线**：不删断言、不跳测试、不放宽 parity、不用 stub 冒充实现、不把未验证写成已验证。违反即整 lane 回退。

## 2. QC · 结果质量（对交付物独立检验）

| 检验 | 判据 | 工具 |
| --- | --- | --- |
| C1 门禁结果 | `make ci` 0 退出码 | `make ci` |
| C2 测试真实性 | parity 引用 100% 可解析；豁免抽样可辩护 | `scripts/qc_check.py` + 独立复核 |
| C3 运行时行为 | 13 项 HTTP 断言全 PASS | `scripts/smoke.sh` |
| C4 制品完整性 | 六平台二进制、静态链接、版本号；磁盘 dist 存在且 `embed_ui.sh` 可构建（UI 可分发性） | `dist/` + `file` + `--version` |
| C5 约束合规 | cgo 0、离线可构建 | `grep 'import "C"'`、`GOPROXY=off` |
| C6 文档一致性 | 四份文档的口径数字一致（865/680 等） | `scripts/qc_check.py` |
| C7 回归趋势 | 路由/测试数不低于检查点台账峰值 | `ledger.csv` + 看门狗 |

## 3. 独立性

- QA/QC 均由**非实现者**执行：过程审计由 `qa_audit.py` 自动跑，结果检验由 `qc_check.py` + 独立复核 agent 跑。
- 实现 lane 的自报只作为线索，不作为证据；任何「已完成」必须有可复现命令的输出。
- 违规处理：QA 违规 → 打回原 lane；QC 失败 → 阻断合并/发布，不得带病交付。
