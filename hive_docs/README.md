# Hive 全面调研报告（源码版）

> 调研对象：本仓库中的 **Python 版 Hive 本体**（`core/framework/`、`core/hive/`、`core/frontend/`、`tools/`）。
> 基线：`aden-hive` / `aden-tools` **1.0.15**，仓库 HEAD `f195ebaf3`（2026-11-27）。
> 调研方式：以源码与可执行测试为准；`go_hive/docs/` 只作为“需要覆盖哪些特性面”的参照清单，**不作为事实来源**，本报告不分析 go_hive 实现。

---

## 1. 这份报告回答什么

一句话：**Hive 不是一个“会调用工具的聊天机器人”，而是一套让多个 Agent 在真实业务里长期、并行、可审计地工作的运行时。**

它由三件事构成：

1. **一个执行原语**：`AgentLoop`。Queen 是它，每个 Worker 也是它；没有节点图、没有边、没有共享数据缓冲区。
2. **一层控制平面**：预算、停泊/恢复、上下文压缩、提醒注入、事件总线、判定器、看门狗。它们不改变“谁在思考”，只决定“何时继续、何时停、何时找人、何时收尾”。
3. **一组入口与沉淀**：Dashboard / CLI / 渠道 / 触发器 / Sentinel / 记忆 / 技能 / Playbook。

本报告把这三层拆开，逐层给出机制、参数、源码位置与边界条件。

---

## 2. 目录

| 章节 | 内容 | 适合谁读 |
|---|---|---|
| [00 方法与基线](00-method-and-baseline.md) | 证据规则、代码规模、版本、术语 | 所有人 |
| [01 产品与定位](01-product-and-positioning.md) | Hive 是什么、适用/不适用、双层结构 | 产品、架构 |
| [02 总体架构](02-architecture.md) | 八层栈、消息生命周期、进程与目录拓扑 | 架构、后端 |
| [03 执行内核 AgentLoop](03-agent-loop.md) | 回合、流、工具、预算、压缩、停泊、恢复、判定 | 核心开发 |
| [04 Colony / Queen / Worker](04-colony-queen-worker.md) | 组织模型、生命周期、并发、超时、报告 | 核心开发 |
| [05 状态与协调](05-state-and-coordination.md) | Tracker、Plan、EventBus、ReminderHub、会话存储 | 核心开发 |
| [06 记忆与 Soul](06-memory-and-soul.md) | 三作用域记忆、反思、召回、时间线、人格沉淀 | 产品、算法 |
| [07 技能与 Playbook](07-skills-and-playbooks.md) | Skill 格式/作用域/信任/门控，Playbook 契约 | 平台、集成 |
| [08 自治与入口](08-autonomy-and-ingress.md) | Trigger、Sentinel、Channel、Queen Mail、Turn Context | 平台、运维 |
| [09 工具、MCP 与浏览器](09-tools-mcp-browser.md) | 内置工具、MCP 两种模式、工具分层、浏览器桥 | 集成、工具开发 |
| [10 模型与凭据](10-models-and-credentials.md) | Provider、模型槽位、视觉回退、加密凭据、OAuth | 平台、安全 |
| [11 Server / API / 前端](11-server-api-frontend.md) | aiohttp、SSE、路由家族、页面、导入导出 | 后端、前端 |
| [12 配置、环境变量与限制](12-configuration-and-limits.md) | `configuration.json`、env、feature flag、预算表 | 运维、部署 |
| [13 工程质量](13-quality-engineering.md) | 测试、CI、打包、发布、可观测性、清理 | 工程效能 |
| [14 评估与建议](14-assessment-and-recommendations.md) | 优点、风险、适用边界、改进优先级 | 决策者 |
| [15 源码地图](15-source-map.md) | 逐模块职责、关键符号、文件体量 | 贡献者 |
| [16 生命周期与交互序列](16-lifecycles-and-sequences.md) | 14 个生命周期状态机、15 条关键序列、失败/超时矩阵、顺序不变量 | 所有人 |
| [17 文件系统与持久化契约](17-filesystem-and-persistence.md) | 目录规范、逐文件 Schema、生命周期、内存对象映射、原子性 | 平台、运维、贡献者 |
| [18 重启与状态恢复](18-restart-recovery.md) | 启动序列、恢复矩阵、崩溃点、Runbook、已知恢复缺口 | 运维、值班、核心开发 |
| [19 Tracker DB 机制深入](19-tracker-db.md) | schema、四个工具、写入门禁、SQL 安全、并发、变更日志、失败模式 | 核心开发、平台、数据 |
| [20 Reminder 机制穷举](20-reminder-mechanism.md) | 15 个 Source、9 个 Point、门禁/上限/状态、旁路提醒、缺口 | 核心开发、提示工程、运维 |
| [21 提示词与上下文构成](21-prompts-and-context.md) | 初始系统提示词、Queen/Worker/图节点/SDK 全场景、辅助 LLM 提示、缓存与体积、实测缺口 | 提示工程、核心开发、产品 |

另附：

- [`index.html`](index.html)：单文件可视化总览（无需构建，直接用浏览器打开）。
- [`research-gaps.html`](research-gaps.html)：研究空白地图（未穷举清单 + 路线图，带可点击进度状态，可离线复用）。
- [`gaps-index.html`](gaps-index.html)：逐项穷举详情（G-01 ~ G-15、C-01 ~ C-10，共 25 份可复用 HTML）。
- 补充深入分析：[H-02 AST 清单](H-02-ast-inventory.html) · [ast-inventory.json](ast-inventory.json) · [H-01 结构/契约矩阵](H-01-structure-contracts-matrix.html) · [C-11 Credentials](C-11-credentials.html) · [A-01 附件与生成文件](A-01-attachments.html) ·[B-01 基础工具](B-01-builtin-tools.html) · [B-02 工具运行时](B-02-tool-runtime.html) · [B-03 Tool 接口与 MCP](B-03-tool-interface-mcp.html) · [T-01 测试全景](T-01-test-atlas.html) · [T-02 测试质量与缺口](T-02-test-quality.html)。
- [`metrics.json`](metrics.json)：本报告引用的机器可读统计。
- [`sources.md`](sources.md)：主要证据文件清单与复核命令。

---

## 3. 十个最重要的发现

1. **只有一个执行原语。** `AgentLoop` 同时承担 Queen、Worker、独立 SDK Agent。可靠性能力（持久化、压缩、预算、停顿检测、视觉回退、judge）因此只实现一次，所有角色继承。代价是 `agent_loop.py` 约 **7,501 行**，是全系统复杂度的绝对中心。
2. **磁盘是真相，内存只是缓存。** 会话按 `parts/<seq>.json` 写穿，游标记录迭代号、输出、待处理输入、用户停止标记与累计工具调用数；Queen 可以在一个问题、一次凭据收集或一次崩溃后停泊数天再恢复。
3. **协调不靠图，靠四种“基底”。** Tracker 存事实，Plan 存意图，EventBus 传信号，ReminderHub 管注意力。没有 DAG、没有边、没有共享数据缓冲区——编排是 Queen 运行时的一次工具调用。
4. **预算不是一道闸，而是五层。** 回合数、每回合工具调用软/硬上限、生命周期工具调用上限、上下文预算、时间/流预算；超额时优先进入“宽限收尾”（grace），让 Worker 仍能 `report_to_parent`，而不是静默死亡。
5. **控制平面大量由“提示注入”实现。** 提醒中枢在 9 个时点、按角色筛选约十余个 ReminderSource，把任务计划、在飞 Worker、Tracker 快照、技能/工具清单塞回上下文。它是 Hive 最独特、也最难测试的机制。
6. **Tracker 是一个“有护栏的 SQLite 自由舞台”。** Queen 用原生 SQL 建表，Worker 只能通过注册过的表 upsert；`_` 前缀表由框架保护，`ATTACH/PRAGMA/VACUUM/load_extension` 被拒绝。行级变更日志让 UI 能显示“哪一行变了”。护栏有边界：`main._...` 这类 schema-qualified 写可绕过 `_` 保护（见 19 章 6.3）。
7. **记忆是可测量的工程，不是玄学。** markdown 笔记 + BM25 会话召回 + 可选时间线；官方 LongMemEval-M 82.0%，同时诚实公布“整段历史直接喂模型”在 S 集上高 8.4 分。
8. **方法在运行中沉淀。** Queen 先亲手做一个单元（pilot），再 `write_skill` 写成技能，最后用 `run_playbook` 在 Tracker 剩余行上收敛。Playbook 不持有业务状态，重跑即续跑；它是“可执行 Python 编排脚本”，**不是沙箱**。
9. **自治有明确的“人”的边界。** 建群必须用户确认；Sentinel 只在停泊时做三选一（继续/完成/找人）；触发器只在会话被加载时运行；当前实现会静默跳过错过的 tick（补偿握手端点存在但无生产者）。
10. **安全边界是“操作系统级”的。** 终端工具、任意 Python Playbook、驱动用户已登录 Chrome 的浏览器桥、可向外部渠道发消息的 Sentinel——Hive 的能力上限等于运行它的用户权限。凭据加密、技能信任门禁、工具白名单只是边界内的减法，不是沙箱。

---

## 4. 阅读路径建议

- **只想判断是否采用**：01 → 14 → 12。
- **要基于 Hive 做二次开发**：01 → 02 → 03 → 16 → 04 → 05 → 07 → 09 → 10 → 17。
- **要运维/私有化部署**：02 → 16 → 17 → 18 → 19 → 11 → 12 → 13 → 08。
- **要改核心循环**：03 → 16 → 04 → 05 → 15，并先读 `core/tests/` 中对应测试。
- **要做 Colony 数据/Playbook 设计**：04 → 05 → 19 → 07。
- **要调提示/长会话行为**：21 → 16 → 20 → 03；先看提示词在哪一层、哪一份真正生效，再改 source。
- **要排查“为什么卡住/为什么恢复不了”**：16 的状态机与失败矩阵 → 18 的恢复矩阵与 Runbook → 03 / 04 对应机制。
- **要备份/迁移/排障磁盘**：17 的目录契约 → 18 的崩溃点矩阵。

---

## 5. 结论先行

Hive 的核心竞争力不在“工具多”或“模型多”，而在于它把**长会话、并行扇出、可恢复、可审计**这四件通常由外部编排系统承担的事，收进了同一个 `AgentLoop` 及其运行时里。它因此非常适合“同质工作单元多、需要跑很久、结果要能查”的业务流程；不适合一次性脚本、强沙箱多租户、或要求亚秒级确定性的场景。

它的主要风险也同样集中：**复杂度集中在一个 7.5k 行的循环里，行为大量写在提示词里，能力边界等于宿主权限。** 这三条决定了采用它时需要配套的工程纪律，而不是把它当成一个可以随手嵌入的库。
