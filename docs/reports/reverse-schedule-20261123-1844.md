# 倒排排期复核 · 2026-11-23 18:44 CST

## 1. 复核口径

- 当前时间：2026-11-23 18:44 CST。
- 代码基线：`9e6b5379`（产品代码点 `d403b0a6`；本日 slot 18 head `50a981b4`）。
- 治理口径：截止只能提前，不能延后；G1/G2 当前账本日期为 **2026-11-22**，已经过去；G3 当前外限为 **2026-12-18**。
- 吞吐假设：**12.5 lane-hours/天**（治理台账既有值）；WIP 上限 4；每 2h 一个 slot。
- 估算包含 1.5 倍缓冲（与 `governance_recheck.py` 相同），不是承诺；外部依赖不压缩日期。

## 2. 账本结论

| 门禁 | 账本当前 | 结论 |
|---|---|---|
| G1 字段级逐字一致 | 2026-11-22 | **逾期**；G1 闭包证据已在，但不能改成未来日期 |
| G2 旧 home 无损替换 RC | 2026-11-22 | **逾期**；数据/回滚证据齐备，P0-G2-1 为已登记决策项 |
| G3 商业外发布 | 2026-12-18 | 外限不变；只能提前，不能后移 |

`delivery-backlog.json` 当前 G1/G2 剩余为 **0 lane-hours**，G3 只剩 12h 外部项；这不等于功能全部闭环。下列“功能闭环轨道”是未入账缺口的补充估算，不修改官方门禁日期。

## 3. 功能闭环轨道估算

| 工作项 | 估算 lane-hours | 依据/状态 |
|---|---:|---|
| go-hive-cloud M2–M7 | 40 | 既有 M2–M7 估算 |
| senders 云控制面 M4.5 | 7 | 6–8h 中值 |
| Aden credentials 同步 | 8 | 待实现 |
| Sender/CRM UI | 12 | 页面/接线/验收 |
| worker/overseer 真实 stream E2E | 8 | fake provider 已过，真实网络未跑 |
| reminders `tasks` / `idle_nudge` 残余 | 4.5 | 4–6h 中值 |
| `background_tools` / `collect_result` | 5.5 | 4–6h |
| `decision_tracker` | 8 | P3 |
| 回归、未知项、发布收口 | 7 | 估算 |
| **合计** | **100** | 含 1.5 缓冲约 **12 个工作日** |

> 若只按官方 G3 账本（Windows 8h + Actions 4h 外部项），内部剩余为 0；若按“go_hive 功能闭环 + 云/UI”口径，按上表约 100 lane-hours。两者必须分开汇报，不能把官方 0h 当成全部完成。

## 4. 倒排排期

### 4.1 今天的收口（G1/G2）

| 时间 | 动作 | 验收 |
|---|---|---|
| 20:00 slot | 只做 G1/G2 证据收口；不启新功能 lane | G1/G2 闭幕记录、CI/QC 复跑、slot 20 |
| 22:00 slot | 生成 `trial/20261123-20`；检查旧 home 启动 | SHA256、health、upgrade compat |
| 00:00 slot | G3 lane 预热；确认真实模型/旧 home 环境 | 无 fake 结果冒充 |

G1/G2 没有未来截止日期；如果 20:00 不能收口，它们保持 **OVERDUE**，G3 功能 lane 不得在未收口前扩张。

### 4.2 功能闭环（内控目标 2026-12-04）

| 日期 | lane 分配（约 12.5 lane-hours/天） | 里程碑 |
|---|---|---|
| 11-24 Tue | worker/overseer stream E2E 8h；reminders 残余 4.5h | 真实 E2E 证据；reminders 目标 15/15 或明确剩余 |
| 11-25 Wed | cloud M2 6h + M3 4h + senders 2.5h | Global DB / Inbox L2 起步 |
| 11-26 Thu | cloud M4 8h + senders 4.5h | CRM 契约 / sender runtime |
| 11-27 Fri | cloud M5 10h + background_tools 2.5h | LLM proxy |
| 11-28 Sat | cloud M6 6h + background_tools 3h + credentials 3.5h | 凭据代理起步 |
| 11-29 Sun | cloud M7 6h + credentials 4.5h + UI 2h | 部署面 / credentials |
| 11-30 Mon | UI 10h + decision_tracker 2.5h | Sender/CRM UI 主体 |
| 12-01 Tue | decision_tracker 5.5h + 未知项/回归 7h | 功能面收口 |
| 12-02 Wed | 全量 CI、旧 home 双跑、journey/UI E2E | 回归通过 |
| 12-03 Thu | RC 构建、回滚演练、trial bundle | RC 证据 |
| 12-04 Fri | 功能冻结；仅修回归 | **内控冻结日** |

### 4.3 外部依赖与 G3

| 日期 | 动作 | 约束 |
|---|---|---|
| 12-05–12-09 | Windows 实机 8h、Actions release 4h（外部日历） | 无环境则保持 RISK，不假绿 |
| 12-10 Thu | 外部复验目标日；全量 RC / 镜像 / 发布检查 | 只能提前 |
| 12-18 Fri | G3 外限；最终商业外发布 | 不得后移 |

## 5. 控制点

1. **G1/G2 是阻塞门，不是日期装饰**：20:00 slot 先收口；未收口则保持逾期，不启动新功能。
2. **每 2h slot 不变**：20:00、22:00、00:00、02:00…；slot 必须带 artifact + manifest + sha256 + upgrade compat。
3. **每 6h 治理不变**：18:05 已重算；下一次 00:05；日期只提前。
4. **外部项不折进吞吐**：Windows/Actions 各按外部日历；没有机器/权限就保持 RISK。
5. **证据分层**：结构门禁（CI/QC）≠ 功能门禁（journey/UI/真实模型/真实云）；不得用前者替代后者。

## 6. 结论

- **官方账本**：G1/G2 已逾期（无未来日期），G3 外限 12-18；内部 G3 账本仅剩 12h 外部项。
- **功能闭环**：约 100 lane-hours；按 12.5 lh/day + 1.5 缓冲，内控 **12-04 冻结**、**12-10 外部复验**、**12-18 G3 外限**。
- 这两个日期口径必须同时成立：**12-04 不是 G3 提前承诺，12-18 也不是自动延期**；任何外部项失败都只能标 RISK，不得把日期改晚。
