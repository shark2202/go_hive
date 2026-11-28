# 00 · 调研方法与基线

## 1. 范围

本报告只分析**本仓库的 Python 版 Hive**：

- `core/framework/`：完整运行时实现，约 **298** 个非测试 Python 文件、**123,900** 行。
- `core/hive/`：公开发布面（`from hive import ...`），是 `framework.*` 的稳定再导出。
- `core/frontend/src/`：React Dashboard，**230** 个 TS/TSX 文件、**65,649** 行。
- `tools/src/`：内置工具与集成目录，**400** 个 Python 文件、**96,651** 行。
- `core/tests/` 与各模块 `tests/`：**172 + 43** 个测试文件、**62,871** 行测试代码。
- `docs/`：仓库自带文档，仅用于交叉核对；与源码冲突时**以源码为准**。

明确排除：

- `go_hive/` 的实现与结论（仅把 `go_hive/docs/` 当作“特性面清单”参照，不引用其判断）。
- 未安装在本仓库的可选闭源包（例如 `framework.crm`、`framework.cloud_sync` 在这份 OSS 工作区中不存在；代码以 `find_spec` / 导入失败降级）。

## 2. 证据规则

本报告中的事实分三类，写法上尽量可区分：

| 标记 | 含义 | 例子 |
|---|---|---|
| **源码事实** | 直接来自当前工作区代码，附 `file:line` 或符号名 | Worker 默认 `max_iterations=3`，见 `agents/queen/worker_definition.py:70` |
| **配置默认值** | 代码中的 dataclass / dict 默认，或 `configuration.json` 缺省分支 | `LoopConfig.max_context_tokens=180_000` |
| **文档记载** | 仓库文档或 release notes 的描述，用于解释“为什么” | LongMemEval 成绩、版本叙事 |

刻意**不**做的事：

- 不把 README/文档里的宣传语当机制。例如文档写“95+ 工具”“13 个 Queen”，报告会给出代码中的真实枚举。
- 不把“有同名文件”当“已接线”。每个能力都追到注册点或调用点。
- 不运行真实 LLM 端到端（本环境无凭据）；涉及模型行为的结论来自代码路径、提示词与仓库内已有基准记录。

## 3. 版本与依赖基线

| 项 | 值 | 证据 |
|---|---|---|
| 包版本 | `aden-hive` **1.0.15** | `core/pyproject.toml:2` |
| 工具包 | `aden-tools` **1.0.15**（uv workspace 成员） | `tools/pyproject.toml:2` |
| Python | `>=3.11` | `core/pyproject.toml` |
| 许可 | Apache-2.0 | `core/pyproject.toml` |
| 仓库 HEAD | `f195ebaf3`（2026-11-27） | `git log -1` |
| 核心依赖 | `pydantic>=2`、`anthropic>=0.40`、`httpx>=0.27`、`litellm==1.83.4`、`mcp>=1.0`、`fastmcp>=2`、`mcp2cli>=3.8,<4`、`croniter`、`psutil` | `core/pyproject.toml` |
| 可选依赖 | `server`（aiohttp）、`tools`（aden-tools）、`webhook`、`testing` | 同上 |
| 前端 | React 18、Vite 6、Tailwind 4、ECharts 5、Mermaid 11、Shiki 4 | `core/frontend/package.json` |

> 版本说明：`docs/releases/` 最新为 v1.0.15；`go_hive/docs` 中的 release notes 到 1.0.26 是 Go 发行线的编号，**不是** Python 包版本，本报告不混用。

## 4. 代码规模与结构

| 区域 | 文件数 | 行数 | 说明 |
|---|---:|---:|---|
| `core/framework`（非测试） | 298 | 123,900 | 运行时主体 |
| 其中 `agent_loop/` 核心 4 文件 | 4 | 10,346 | 执行原语与其内部件 |
| `core/tests` + 模块 tests | 215 | 62,871 | 测试代码约为实现的 50% |
| `core/frontend/src` | 230 | 65,649 | 含 38 个前端测试文件 |
| `tools/src` | 400 | 96,651 | 工具与 104 个集成目录 |

体量最大的单文件（说明复杂度分布）：

| 文件 | 行数 |
|---|---:|
| `agent_loop/agent_loop.py` | 7,501 |
| `tools/queen_lifecycle_tools.py` | 4,000 |
| `llm/litellm.py` | 3,678 |
| `server/routes_sessions.py` | 3,265 |
| `host/colony_runtime.py` | 2,935 |
| `server/routes_execution.py` | 2,350 |
| `server/session_manager.py` | 2,298 |
| `host/event_bus.py` | 2,269 |
| `loader/agent_loader.py` | 2,082 |
| `server/queen_orchestrator.py` | 2,037 |

## 5. 枚举口径

| 对象 | 数量 | 口径 |
|---|---:|---|
| Queen 人格 | **13** | `agents/queen/queen_defaults/*.yaml` |
| 新用户默认激活 Queen | **6** | `frontend/src/lib/colony-registry.ts:158` |
| 框架默认技能 | **12** | `skills/_default_skills/*/SKILL.md` |
| 预设技能包 | **8** | `skills/_preset_skills/*/SKILL.md` |
| 内置工具组 | **5** | `tools/harness_tools.py`：terminal / files / chart / memory / hive_tools |
| `aden_tools` 集成目录 | **104** | `tools/src/aden_tools/tools/*/`（排除 `__pycache__`），共 866 个 `@mcp.tool` 注册点 |
| 精选模型 Provider | **12** | `llm/model_catalog.json` |
| 订阅/本地预设 | **8** | `claude_code`、`zai_code`、`codex`、`minimax_code`、`kimi_code`、`hive_llm`、`antigravity`、`ollama_local` |
| HTTP 路由注册点 | **约 196** | `server/*.py` 中 `add_get/post/put/patch/delete("/api/...")` |
| 路由模块 | **23** | `server/routes_*.py` |
| EventType | **77** | `host/event_bus.py:102` 起 |

## 6. 事实复核方式

报告中的关键参数都可以用以下命令复核（在仓库根目录执行）：

```bash
# 执行原语与默认预算
sed -n '49,140p' core/framework/agent_loop/internals/types.py
sed -n '60,90p' core/framework/agents/queen/worker_definition.py

# Queen 的两种循环配置
sed -n '15,40p' core/framework/agents/queen/agent.py

# 工具分类与 eager 层
sed -n '51,220p' core/framework/agents/queen/queen_tools_defaults.py

# Tracker 保护规则与 schema
sed -n '34,110p' core/framework/host/tracker_db.py

# 提醒注入点与 ParkReason
sed -n '48,205p' core/framework/agent_loop/reminders.py

# 路由清单
grep -rhoE 'add_(get|post|put|patch|delete)\("/api/[^"]+"' core/framework/server | sort -u
```

## 7. 已知偏差与限制

1. **文档漂移是真实存在的。** 例如：
   - `docs/configuration.md` 仍写 `agents/queens/<id>/` 与 `data/tracker.db`，而 v3 布局是 `queens/<id>/` 与 `tracker/tracker.db`（`config.py:118`）。
   - `host/tracker_db.py` 模块 docstring 说 DB 在 `data/` 下，`ensure_tracker_db` 实际写 `tracker/`（`tracker_db.py:349`）。
   - `run_worker` 工具描述写“生命周期默认 150”，`DEFAULT_LOOP_CONFIG` 实际是 **200**（`worker_definition.py:70`）。
   本报告一律以运行时代码为准，并在相关章节标注这类漂移。
2. **本工作区缺少可选包。** `framework.crm`、`framework.cloud_sync` 不存在，因此 CRM 与云同步只作为“存在接口/有降级”描述，不评价其实现。
3. **未运行 live 模型测试。** 报告不声称任何模型质量结论；记忆基准引用仓库自己的 LongMemEval 记录，并明确其口径。
4. **行号会随代码变化。** 以符号名与路径为主、行号为辅。
