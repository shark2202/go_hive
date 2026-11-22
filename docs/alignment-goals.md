# 对齐目标报告（go_hive ↔ Hive 上游）

> 更新：2026-11-22 18:40 CST · 快照口径全部为当日实测 · 明细见 [差异报告](alignment-differences.md) 与 [处理方案报告](alignment-treatment-plan.md)。

## 1. 目标（五轴 + 迁移）

| 轴 | 定义 | 验收口径 |
|---|---|---|
| **外观** | API / WS / tool 定义 / skill / env / configuration.json / CLI 字段级逐字 | 冻结契约 + 逐字 diff（contracts/*.json + 各自 diff 报告） |
| **语义** | loop / colony / playbook / memory 行为等价 | Python 参考实跑对照 + 门禁测试（parity 注解） |
| **功能** | 上游工具 / 路由 / 命令可用且可调用 | 契约缺项 = 0（有效缺失） |
| **特性** | 机制完整性（热重载 / 调度 / 多模态 / 升级兼容 / 恢复） | 真机 E2E 证据（Docker/浏览器/崩溃恢复等） |
| **迁移** | 同一 HIVE_HOME 直接可用、数据零丢失、可回滚 | slot 每版 upgrade-compat（v1/v2/v3 三代） |

**判定线（本轮确立）**：凡**模型 / 客户端 / 持久化数据 / CLI** 任一方可观测的差异 → 必须逐字一致或显式登记（带复查条件）；四者都观测不到的 → 内部实现自由（goroutine vs asyncio、JSON vs SQLite 等）。

## 2. 参考基线

- **上游参考**：`aden-hive / aden-tools` **1.0.15「Notes in Time」**（提交 `6703b434`，2026-11-22 合并）。
- **冻结契约 reference**：`python@1b9c458a`（契约冻结时点，含 tool/api/skills 逐字证据）；1.0.15 带来的增量已单独重算（env/config/routes/module-inventory 均已刷新，见差异报告）。
- **版本双轨**：Go `1.0.26-go.1` / Python `1.0.15`，两侧独立演进，不进契约、不阻断（既定口径，已更新至 1.0.15）。

## 3. 交付门槛与现状（2026-11-22 18:40 实测）

| 门槛 | 目标日期 | 现状 |
|---|---|---|
| **G1 字段级逐字** | 2026-11-22 | 净面清零；残余全部为已登记例外 / 待拍板项（见处理方案） |
| **G2 无损替换 RC** | 2026-11-22 | 净面清零；slot 每版 v1/v2/v3 迁移 57 项 PASS；回滚路径已验 |
| **G3 商业外发布** | 2026-12-18 | 内部项闭环（installer-verify 含 Docker 真机 20/20；govulncheck 23→0 已转绿）；外部 3 项待环境 |

## 4. 门禁与实测数字

| 门禁 | 通过标准 | 当前（实测） |
|---|---|---|
| 九道 make 门禁 | 全 0 | **全绿**（wave-66，含 govulncheck 0 OSV） |
| parity | 0 缺注解 | **5,722/5,722** |
| parity refs | 0 missing | 全解析 |
| compat-check | 12/12 | **12/12**（C12：273 条 = 217 ported / 27 partial / 9 oos / 20 unported（triage 修正 11 假标 + coercion 接线升级）） |
| QC | 9/9 | **9/9** |
| arch | 7 生成物一致 | **82 包 / 219 边 / 8 层 / 1 info** |
| govulncheck | 0 OSV | **0（go1.26.11，同 scanner/DB 23→0）** |
| 冻结面 | allowlist 逐条理由 | OK |
| slot | 2h 一版 | 今日 10 版（00–18）；最新 `20261122-18` |
| 发布镜像 | 可拉取 | `shark2202/go_hive` 与 slot/CI 同步（每次 publish 校验 ls-remote） |

## 5. 证据矩阵（按轴）

- **外观**：`docs/surface-tracking-matrix.html`（218/225 路由一致、端点 18/18、前端缺失 0）· `tool-parity-matrix.html` · `api-contract-diff.md` / `env-config-contract-diff.md` / `cli-surface-diff.md` / `error-taxonomy-diff.md`
- **语义**：`judge-parity.md`（J2 已对齐）· `memory-notes-parity.md`（1.0.15 全族）· `session-titles-parity.md` · `stall-check-parity.md` · `goal-marshal-parity.md`
- **特性**：`docs/e2e-assets/`（dashboard-memory-1015 5 截图 · installer-verify Docker · reliability 27/27 · longmemeval 60q/200q · memory-notes 探针）
- **迁移**：`dist/slots/*/compat.json`（v1/v2/v3）· `docs/exports-baseline-decision.md`
- **性能**：`docs/perf-recall-15k.md`（15k 轮：冷建 388.5ms、warm p50 **12.7ms**（perf-opt 9.7×，p95 13.5ms）、索引 7.05MB、RSS +12%）

## 6. 与 Python 上游的关系

- 同构而非分叉；上游工作树 `core/`、`tools/` 是唯一事实来源。
- 双向兼容窗口：Go 读 Python 写下的会话/凭据/tracker；反向至少读（每 slot 验证）。
- 演进策略：上游升级先更新参考测试再改实现；契约重冻结走独立决策。
