# 01 · 项目概览

## 1. 它是什么

OpenHive（包名 `aden-hive`，仓库 `aden-hive/hive`）是一个开源 Agent 运行时。官方 README 的定位是：

> An open-source runtime for colonies of AI agents that do real business work.

关键词是 **colonies（集群）** 和 **real business work（真实业务工作）**。它不把「一个 Agent 完成一次对话」当作终点，而是把「一个业务过程被稳定、并行、可审计地跑完」当作终点。

从代码结构看，它同时承担两个角色：

1. **一个完整应用**：`./quickstart.sh` 安装后运行 `hive open`，得到 Dashboard、Queen 聊天界面、Colony 数据页、技能库、模型配置等。
2. **一个可依赖的库**：`pip install aden-hive` 后可以 `from hive import AgentLoop, LiteLLMProvider, EventBus, ...`，在自己的项目里复用它的循环、模型层和事件总线。

这种「应用 + SDK 双层」是理解仓库结构的关键：`core/hive/` 是公开导出面，`core/framework/` 是完整实现。

## 2. 它要解决的问题

README 的「Is Hive for you?」一节把目标场景说得很清楚，归纳起来是四类特征：

| 特征 | 传统单 Agent / 图编排的痛点 | Hive 的答案 |
| --- | --- | --- |
| 大量同质工作单元（线索、账户、工单、文档） | 要么串行慢，要么需要手写并行编排 | `run_worker` 一次分发 N 个 Worker，默认 4 并发、其余排队 |
| 运行数小时或按计划运行 | 进程一挂就前功尽弃 | 所有状态落盘，崩溃/重启后从游标恢复；`set_trigger` 支持 cron/interval/webhook |
| 结果需要检查、查询、审计 | 聊天记录不可结构化查询 | 共享 SQLite Tracker，Queen 用 SQL 验收，Worker 用 `tracker_upsert` 写入 |
| 人必须保留最终决定权 | 自动化一旦跑偏难以拦截 | `ask_user` 停泊等待、`suggest_colony` 需确认、Sentinel 人在回路 |

反过来，官方也明确说：**单次提示或一次性脚本，用普通 Agent 更简单。** 这是一个有明确适用边界的工具，而不是万能框架。

## 3. 核心概念

| 概念 | 定义 | 代码位置 |
| --- | --- | --- |
| **Queen（女王）** | 长期存在、面向用户的 Agent。拥有对话、计划、记忆和 Colony。13 个内置人格。 | `core/framework/agents/queen/` |
| **Colony（集群）** | 一个自包含目录，包含 Queen、Worker 规格、Tracker、技能、触发器。是部署与移植单位。 | `core/framework/host/colony_runtime.py` |
| **Worker（工蜂）** | Queen 的 `AgentLoop` 克隆，一个任务、更窄的工具、严格预算、无记忆。 | `core/framework/host/worker.py` |
| **Tracker（追踪表）** | Colony 共享的 SQLite 账本，是「做完什么、还剩什么」的唯一事实来源。 | `core/framework/host/tracker_db.py`、`tools/tracker_tools.py` |
| **Playbook（剧本）** | 把已验证方法在 Tracker 的每一行上重放的脚本，带重试、退避、限速和死信。 | `core/framework/host/playbook/runner.py` |
| **Skill（技能）** | 开放 Agent Skills 格式的 `SKILL.md` 包，Queen 在方法验证后自己写出来。 | `core/framework/skills/` |
| **Sentinel（哨兵）** | 可选的人不在场守护：Queen 停泊时判断「继续推 / 标记完成 / 升级给人」。 | `core/framework/sentinel/` |
| **Reminder Hub（提醒中枢）** | 在固定时点向上下文注入短 `<system-reminder>`，让长会话保持连贯。 | `core/framework/agent_loop/reminders.py` |

这些概念的共同点是：**没有一个是编译期结构**。Queen 在运行时决定要不要成为 Colony、要开多少 Worker、Tracker 有哪些表；框架只提供原语和约束。

## 4. 设计哲学：execute first, then systematize

这是贯穿全项目的方法论，也是它区别于「先设计图再执行」的框架的地方：

1. **Independent 阶段**：Queen 作为普通对话 Agent 直接做事。
2. 当任务呈现并行、周期或长时特征时，她调用 `suggest_colony` 提议建群，用户确认后进入 Colony 阶段。
3. **Pilot（试点）**：她先亲手完整做一个工作单元，把结果写进 Tracker，证明路径可行。
4. **Systematize（系统化）**：她把验证过的方法写成 Skill，再用 Playbook 在剩余行上重放。
5. 因为 Tracker 永远知道「做完/未做」，重跑 Playbook 天然就是续跑。

代码中的阶段常量是 `QUEEN_PHASES = frozenset({"independent", "colony"})`（`core/framework/tools/queen_lifecycle_tools.py:84`）。

## 5. 模型与供应商策略

- 基座是 **LiteLLM**（锁定 `litellm==1.83.4`），因此理论上支持 LiteLLM 覆盖的所有供应商。
- 另有原生 `AnthropicProvider`、`AntigravityProvider`、`MockLLMProvider`。
- 模型目录在 `core/framework/llm/model_catalog.json`，由 `model_catalog.py` 读取。
- 支持任意 OpenAI 兼容端点（Ollama、vLLM、LM Studio、网关、Magpie）。
- 支持编程订阅（Claude Code、OpenAI Codex、Kimi Code、MiniMax、Z.AI、Antigravity）。
- Queen 与 Worker 可以跑不同模型（`worker_llm`），每个 Queen 也可以有自己的模型（`queen_llm`）。
- 纯文本模型可通过视觉回退模型「看」图片（`agent_loop/internals/vision_fallback.py`）。

## 6. 版本、许可与社区

| 项 | 内容 |
| --- | --- |
| 当前版本 | 1.0.14（`core/pyproject.toml`、`tools/pyproject.toml`） |
| 许可 | Apache-2.0 |
| 起源 | Y Combinator（Aden） |
| 文档 | `README.md`、`docs/`、托管站 docs.adenhq.com |
| 发布节奏 | 从 1.0.0 到 1.0.14 的 release notes 显示迭代非常密集 |
| 社区入口 | Discord、X、LinkedIn、HoneyComb |

## 7. 仓库的物理结构

```
hive/
├── core/                     # 主包：应用 + SDK
│   ├── framework/            # 完整实现（135k 行）
│   ├── hive/                 # 公开导出面（187 行）
│   ├── frontend/             # React Dashboard
│   ├── tests/                # 跨模块集成/端到端/live 测试
│   └── pyproject.toml        # aden-hive 1.0.15
├── tools/                    # aden-tools：工具与集成库（96k 行）
│   ├── src/aden_tools/tools/ # 104 个集成
│   ├── src/terminal_tools/   # shell / ripgrep / 后台任务
│   ├── src/memory_tools/     # conversation recall
│   ├── src/chart_tools/      # ECharts / Mermaid
│   ├── src/gcu/              # 浏览器桥（Chrome 扩展后端）
│   └── tests/
├── docs/                     # 架构、概念、发布说明、内部设计
├── scripts/                  # 运维与调试脚本
├── templates/derivative/     # 基于 Hive 建新项目的模板
├── quickstart.sh/.ps1        # 一键安装
└── pyproject.toml            # uv workspace 根
```

顶层 `pyproject.toml` 只声明 workspace 成员：

```toml
[tool.uv.workspace]
members = ["core", "tools"]
```

这解释了一个容易踩的坑：在仓库根执行 `pip install -e .` 只会装到一个占位包，正确方式是跑 `./quickstart.sh`，或安装 `aden-hive[tools]`。
