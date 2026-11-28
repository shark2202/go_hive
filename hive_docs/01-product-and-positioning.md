# 01 · 产品与定位

## 1. Hive 是什么

Hive 的自我定位是：

> An open-source runtime for colonies of AI agents that do real business work.

关键词是 **colony（集群）** 与 **real business work**。它不把“一次对话完成一个任务”当作终点，而把“一个业务过程被并行、稳定、可审计地跑完”当作终点。

从代码结构看，它同时是两样东西：

| 角色 | 入口 | 说明 |
|---|---|---|
| **一个完整应用** | `hive open` / `hive serve` | Dashboard、Queen 聊天、Colony 数据页、组织图、技能/记忆/凭据/模型库 |
| **一个可依赖的 SDK** | `pip install aden-hive`，`from hive import AgentLoop, ...` | 复用循环、模型层、事件总线、ColonyRuntime、Channel 接口 |

这种“应用 + SDK 双层”是理解仓库结构的关键：

- `core/hive/__init__.py` 是**公开稳定面**，按需惰性导入，名字遵循语义化版本。
- `core/framework/` 是**完整实现**，任何 `framework.*` 导入都属于内部 API，可随时变化。

## 2. 它要解决的问题

Hive 面向的是这样一类工作：

| 特征 | 传统单 Agent / 图编排的痛点 | Hive 的答案 |
|---|---|---|
| 大量同质工作单元（线索、账户、工单、文档） | 串行慢；手写并行编排又脆 | `run_worker` 一次分发 N 个任务，默认 4 并发、其余排队 |
| 运行数小时或按计划运行 | 进程一挂前功尽弃 | 所有状态落盘，崩溃/重启从游标恢复；`set_trigger` 支持 cron/interval/webhook |
| 结果需要检查、查询、审计 | 聊天记录不可结构化查询 | 共享 SQLite Tracker，Queen 用 SQL 验收，Worker 用 `tracker_upsert` 写入 |
| 人必须保留最终决定权 | 自动化跑偏难拦截 | `ask_user` 停泊等待；建群需确认；Sentinel 三选一（继续/完成/找人） |
| 方法会重复使用 | 每次重新提示、重新试错 | `write_skill` 沉淀方法；`run_playbook` 在 Tracker 剩余行上重放 |

反过来，Hive 明确**不**适合：

- 一次提示就能完成的任务；
- 一次性脚本、纯函数式数据处理；
- 强多租户、强沙箱、不可信代码执行；
- 要求毫秒级确定性、不能接受 LLM 非确定性的流程。

## 3. 核心概念

| 概念 | 定义 | 代码位置 |
|---|---|---|
| **AgentLoop** | 唯一的执行原语：多轮、流式、工具调用、判定、停泊 | `core/framework/agent_loop/agent_loop.py` |
| **Queen** | 长期存在、面向用户的 Agent；拥有对话、计划、记忆、Colony | `core/framework/agents/queen/`、`server/queen_orchestrator.py` |
| **Colony** | 自包含目录：Queen 的 overseer 会话 + Worker 规格 + Tracker + 技能 + 触发器 | `core/framework/host/colony_runtime.py`、`config.py:118` |
| **Worker** | Queen 的 AgentLoop 克隆：一个任务、更窄工具、严格预算、无记忆 | `core/framework/host/worker.py`、`agents/queen/worker_definition.py` |
| **Tracker** | Colony 共享 SQLite 账本；“做完什么、还剩什么”的唯一事实来源 | `host/tracker_db.py`、`tools/tracker_tools.py` |
| **Playbook** | 在 Tracker 行上编排 Worker 的 Python 脚本；重试、退避、限速、死信 | `host/playbook/runner.py`、`tools/playbook_tools.py` |
| **Skill** | Agent Skills 标准的 `SKILL.md` 包；Queen 可自己写 | `core/framework/skills/` |
| **Sentinel** | 可选“人不在场”守护：停泊时判定继续/完成/升级 | `core/framework/sentinel/` |
| **ReminderHub** | 在固定时点注入短 `<system-reminder>`，维持长会话注意力 | `agent_loop/reminders.py` |
| **Trigger** | 定时或 Webhook 唤醒 Colony Queen | `host/triggers.py`、`tools/queen_lifecycle_tools.py:3730` |
| **Channel** | 外部聊天应用接入，每个会话映射为一个 Queen session | `server/channels.py` |
| **Soul** | Queen 的自我叙事，每日从对话中反思修订 | `agents/queen/soul.py`、`soul_reflection.py` |

这些概念的共同点：**没有一个是编译期结构**。Queen 在运行时决定是否成为 Colony、开多少 Worker、Tracker 有哪些表；框架只提供原语与约束。

## 4. 设计哲学：execute first, then systematize

Hive 的 Queen 有两阶段（`tools/queen_lifecycle_tools.py:84`）：

1. **Independent**：她是一个普通对话 Agent，直接做事。当任务呈现并行、周期或长时特征时，调用 `suggest_colony` 提议建群。
2. **Colony**：用户在 Create Colony 弹窗确认后，会话被 fork 进 Colony 目录，她切换为扇出模式。

关键动作顺序：

```text
用户说出结果
  → Queen 先亲手做一个工作单元（pilot），把结果写进 Tracker
  → 把证明可行的方法写成 Skill
  → 用 Playbook 在剩余行上重放
  → Tracker 始终知道“做完/未做”，所以重跑即续跑
```

与之配套的是 **Outcome-Driven Development（ODD）**：

- 结果不是“步骤跑完”，而是“可度量的成功标准 + 硬约束 + 上下文”；
- 结构化 `Goal` 可声明 `success_criteria`（`output_contains` / `output_equals` / `llm_judge` / `custom`，可加权）与 `constraints`（hard/soft）；
- 内置 Queen 与 Worker 使用空 `Goal`，把“完成”交给对话、Plan 与 Tracker。

## 5. 与三种常见形态的对比

| 维度 | 单 Agent | 手写图编排 | Hive Colony |
|---|---|---|---|
| 拓扑 | 固定一个 | 编译期节点/边 | 运行时由 Queen 决定 |
| 扩展方式 | 加提示/加工具 | 改图、改边 | `run_worker` / `run_playbook` |
| 状态 | 上下文窗口 | 图级共享缓冲 | 磁盘 Tracker + Plan + 游标 |
| 恢复 | 通常从头 | 检查点 | 停泊点写穿，精确续跑 |
| 审计 | 聊天记录 | 节点日志 | 每行可查询、可导出 |
| 适用 | 一次性任务 | 稳定、可预先设计的流程 | 形状未知、需要试错与规模化的业务流程 |

## 6. 它刻意不做什么

- **不做通用工作流引擎**：没有 DAG / BPMN / 跨 Playbook 依赖 / 版本回滚。
- **不做多租户沙箱**：工具、Playbook、浏览器桥都在宿主机权限内运行；隔离靠部署边界，不靠进程内沙箱。
- **不承诺“更聪明的推理”**：反思、记忆、技能、Playbook 让**同类问题更可靠**，不是让模型获得通用智能。
- **不隐藏人在回路**：建群要确认，问题要回答，凭据要用户填；Sentinel 只是把“等人”变成可异步。
- **不把 Queen 当可互换调度器**：13 个 Queen 是人格与领域判断，不是同一提示的不同名字。

## 7. 一句话

> **Hive 把“一个业务过程”建模成“一个 Queen + 若干 Worker + 一个 Tracker”，用同一个 AgentLoop 驱动全部角色，用磁盘状态替代图编排，用提醒注入替代长期记忆的缺失。**
