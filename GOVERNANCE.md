# 目标治理 · 质量保障 · 时间治理

> 维护者：Coordinator。目标日期：**2026-11-19 18:00**（硬边界），内部封版 16:00。
> 基线：`8a1d2646`。仪表：`bash go_hive/scripts/checkpoint.sh "备注"` → `go_hive/ledger.csv`。

## 1. 目标治理：交付定义与范围冻结

### 1.1 已核实的剩余盘子（2026-11-18 20:22 实测）

| 盘子 | 规模 | 说明 |
| --- | --- | --- |
| 服务端缺失路由 | 104 条 / 约 1.1 万行 | 7 个路由 lane 承接 |
| 集成工具 | **104 模块 / 324 文件 / 71,393 行 / 811 action** | 位于 `tools/src/aden_tools`，非 `core/framework`；799 个 action 需环境凭据 |
| Sentinel 引擎 | 2,090 行 | 1 个 lane |
| Colony / host 编排 | 约 7 千行 | 1 个 lane |
| 执行与杂项 | 约 3 千行 | 分摊 |

合计约 **9.4 万行 Python**，折 Go 产物 + 测试约 18–22 万行。按实测 5,400 行/时，纯吞吐 33–40 小时；可用窗口 21.6 小时。**结论：19 日 18:00 无法完成 811 action 的全量对等。**

### 1.2 19 日交付范围（冻结）

**必达（发布阻塞）**：
1. 141 条路由全部注册、可达、错误分支对齐；
2. Sentinel 引擎可运行，`/api/config/sentinel` 读写闭环；
3. Colony 多 worker 编排闭环可跑通一条真实链路；
4. 前端构建产物嵌入，`/` 返回 200 且能发起会话；
5. `make ci` + parity 门禁全绿，六平台产物可产出；
6. 集成工具：框架层 + **优先子集**（以 lane H 回报的确切数量为准，目标 ≥ 20 个模块 / ≥ 150 action），其余模块登记为缺口。

**明确不在 19 日范围（登记，不阻塞）**：
- 剩余集成模块的移植与全部 811 action；
- 依赖第三方凭据的端到端验证（无凭据即无证据，不得以单测冒充）；
- 逐 token 流式对等的完整回归；
- 发布打包、文档定稿、安装脚本。

### 1.3 变更控制

- 20:25 起范围冻结：新增需求一律进 backlog，不改当期交付。
- 任何 scope 变更由 Coordinator 单点批准并写入本节，禁止 lane 自行扩缩。

## 2. 倒排排期（自 11-19 18:00 反推）

| 时间窗 | 阶段 | 责任 | 出口条件 |
| --- | --- | --- | --- |
| 11-18 20:19–22:30 | Wave 1 执行（10 lane） | lane a–j | 各自 G1 通过并提交 |
| 11-18 22:30–00:30 | 合并 + G2 全量门禁 | Coordinator | `make ci` 绿；失败 lane 打回一次 |
| 11-19 00:30–04:30 | Wave 2 补齐（仍缺的路由/引擎/闭环） | lane 复用 + 新 lane | 缺口表清零或转 blocker |
| 11-19 04:30–08:30 | 装配集成（路由注册、工具注册、SSE、前端嵌入） | w2-int | 141/141 可达；`/` 200 |
| 11-19 08:30–12:00 | 回归与修复（G4/G5） | w2-ver + 原 lane | parity 抽样 0 造假；e2e 绿 |
| 11-19 12:00–16:00 | 缓冲（仅修缺陷，不加功能） | Coordinator | 门禁持续绿 |
| 11-19 16:00–18:00 | 封版与交付包 | w2-rel | 六平台产物 + 冒烟报告 |

## 3. 质量保障：五道门禁

| 门 | 内容 | 失败处理 |
| --- | --- | --- |
| G1 lane 自检 | `go build`、包测试、`gofmt -l`、parity 注解完整 | lane 内修复，不计返工 |
| G2 合并门禁 | 全量 `make ci`（fmt/vet/test/cross/parity/parity-targets/wiring-check） | 打回原 lane，仅一次；二次失败即砍范围 |
| G3 集成门禁 | `-tags e2e` 全绿；`bash go_hive/scripts/smoke.sh` 全项 PASS（health 200 / create 201 / 无 key chat 503 / `/` 200 / SSE 200 / 未知会话 404） | integrator 修复 |
| G4 复核门禁 | verifier 只读抽查 ≥10% parity 注解是否指向真实上游用例 | 造假注解整 lane 回退 |
| G5 发布门禁 | 六平台交叉编译、`--version`、无 cgo（`grep -r 'import "C"'` 为 0） | 阻断发布 |

质量红线（不可为赶工期突破）：
- 不得删断言、不得跳过测试、不得放宽 parity 检查；
- 不得把「未验证」写成「已验证」；缺凭据必须标 `Parity-exempt: 需真实凭据，未验证`；
- 不得用 stub 冒充实现；未完成的路由必须返回明确的未实现错误并登记。

## 4. 时间治理：检查点与止损

- **检查点节奏**：每个 wave 边界 + 每 2 小时一次，执行 `checkpoint.sh` 写入台账。
- **步调核算**：每个 wave 边界执行 `uv run --no-project python go_hive/scripts/pace.py`，输出实测产能（lane-hour）、单位产出（行/路由/测试 per lane-hour）、各维度 ETA 与 EVM（EV/PV/SPI）。计划校准只依据该核算结果，不依据感觉。
- **独立时间记账**：常驻 `scripts/timegov_record.sh` 每 5 分钟写 `TIME_EVENTS.csv`；`scripts/timegov.py` 依 `TIME_POLICY.json` 对六维（目标完成时间/执行预算/阶段进度/等待时间/决策延迟/停止时点）出裁决到 `TIME_REPORT.md`。任何 BREACH 必须在当轮处理，不得带入下一 wave。
- **独立看门狗**：常驻 `scripts/watchdog_loop.sh`，路由/测试数下降、出现 cgo、worktree 长时间脏即写 `ALERTS.md`。
- **产能口径**：并行阶段墙钟会低估投入，一律以 lane-hour（并发 lane 数 × 墙钟小时）为分母；路由与测试在合并时跳变，按波次记账而非连续速率。
- **台账指标**：Go 路由数 / 上游路由数、测试数、集成工具文件数、parity missing、备注。
- **lane 时间盒**：软 90 分钟、硬 150 分钟。超硬时限由 Coordinator `steer` 或 `stop`，未完成部分转 wave 2 或砍。
- **止损线**：
  - 11-19 04:30 若路由覆盖 < 120/141 → 放弃剩余低价值端点，标注为已知缺口；
  - 11-19 08:30 若 Colony 闭环仍不可运行 → 降级为「单 worker 可跑」，文档明示；
  - 11-19 12:00 之后只允许缺陷修复，任何功能请求进 backlog。
- **返工规则**：同一 lane 同一问题第二轮仍失败，即改派或砍范围，不允许拖全局。

## 5. 当前状态（每轮更新）

见 `go_hive/ledger.csv` 与 `go_hive/STATUS.md`。Coordinator 在每次 wave 结束更新本节。
