# 13 · 工程质量：测试、CI、打包、可观测性与清理

## 1. 测试体系

### 1.1 规模

| 区域 | 文件数 | 行数 |
|---|---:|---:|
| `core/tests/` | 172 | 与模块测试合计 62,871 |
| `core/framework/**/tests/` | 43 | 同上 |
| `core/frontend/src/**/*.test.*` | 38 | 包含在 65,649 行前端代码中 |

测试代码约为实现代码的 **50%**，对一个 12 万行的运行时来说是认真投入。

### 1.2 分层

```text
单元测试        纯函数、schema、解析、限速、信任、预算
组件/集成测试   AgentLoop、ColonyRuntime、EventBus、存储、路由
端到端          core/tests/e2e、live（真实外部 API，CI 默认排除）
前端            Vitest + jsdom + Testing Library
```

`core/tests/` 的文件名直接对应子系统：

```text
test_event_loop_node.py      循环节点
test_colony_runtime_overseer.py / test_colony_scheduler.py
test_idle_nudge.py / test_colony_parallel_nudge.py
test_memory_reminders.py / test_memory_timeline_recall.py
test_compaction_sweep.py / test_fork_compaction.py
test_janitor_*.py           保留策略
test_mcp_*.py               MCP 客户端/注册表/CLI
test_playbook_runner.py / test_playbook_tools.py
test_worker_escalation_e2e.py
test_hive_home_isolation.py 测试不写真实 HOME
```

### 1.3 测试基础设施

- `pytest` + `pytest-asyncio` + `pytest-xdist`；
- `core/conftest.py` 提供 fixture；
- `pytest.ini` 默认 `-m 'not live'`，live 测试必须显式开启；
- `HIVE_HOME` 隔离：测试使用临时目录，防止写入用户真实数据；
- 部分测试用 `MockLLMProvider`，不调用真实模型；
- `core/tests/live/` 与 `e2e/` 保存需要外部环境的用例。

### 1.4 前端测试

- Vitest + jsdom；
- Testing Library；
- 覆盖：路由切换、聊天排序、模型选择、Soul 区块、记忆库、Claude Code Queen 页面等。

### 1.5 评测型测试

除了单元/集成，仓库还保留：

- **LongMemEval** 端到端记忆评测（见 06 章）；
- **playbook** 契约测试（`test_playbook_runner.py` / `test_playbook_tools.py`）；
- **stall check / worker escalation** 的 e2e 用例；
- **gate** 脚本（`scripts/`）用于契约/回归门禁。

## 2. CI

`.github/workflows/ci.yml`：

| Job | 内容 |
|---|---|
| `lint` | Ruff check + format check（`core/`、`tools/`） |
| `test` | Ubuntu + Windows 矩阵；`uv sync` + `pytest tests/`（排除 dummy agents） |
| `test-tools` | 安装 ripgrep、Playwright Chromium，跑 `tools/tests` |
| `package` | 构建前端 → 构建 `aden-hive`/`aden-tools` wheel → 干净 venv 安装 → 运行 `scripts/check_installed_package.py` |
| `validate` | 校验 `exports/*/agent.json` 结构 |
| `go-vulncheck` | Go 依赖漏洞扫描（本报告不分析 Go 实现） |

值得肯定的点：

- **Windows 进入测试矩阵**，不是只测 Linux；
- **打包后在干净环境安装并运行**，能抓住“源码能跑、wheel 不能跑”的问题；
- **前端构建进入 package job**，保证 dashboard 真的能进 wheel。

不足：

- CI 没有覆盖率门槛；
- 没有 `ty`/mypy 类型检查（`ty` 只在 dev 依赖里）；
- live/e2e 不在 CI 常规路径，模型/浏览器行为回归依赖人工或专门流水线；
- 没有性能基准门禁（只有零散 benchmark 文档）。

## 3. Lint 与格式

`core/pyproject.toml`：

```toml
[tool.ruff]
target-version = "py311"
line-length = 150

lint.select = ["B", "C4", "E", "F", "I", "Q", "UP", "W"]
```

- 选择 bugbear、comprehensions、pycodestyle、pyflakes、isort、quotes、pyupgrade；
- 根目录 `pyproject.toml` 另有 `line-length = 120`（工具/脚本）；
- `.pre-commit-config.yaml` 提供本地钩子。

## 4. 打包与发布

### 4.1 uv workspace

```toml
[tool.uv.workspace]
members = ["core", "tools"]
```

- `aden-hive`：运行时 + `hive` 公开面；
- `aden-tools`：工具库与集成目录；
- `core` 依赖 workspace 内的 `aden-tools`（`[tool.uv.sources]`）。

### 4.2 wheel 内容

`core/pyproject.toml`：

```toml
[tool.hatch.build.targets.wheel]
packages = ["framework", "hive"]
```

前端：

- `core/hatch_build.py` 自定义构建钩子，把 `frontend/dist` 放进 `framework/server/frontend`；
- sdist 也携带构建后的 dashboard（`artifacts = ["frontend/dist/"]`），保证从 sdist 构建的 wheel 有前端。

### 4.3 入口

| 命令 | 入口 |
|---|---|
| `hive` | `framework.cli:main` |
| `hive-cred` | `framework.credentials.cli:main` |

### 4.4 版本与稳定性

- `core/hive/__init__.py` 明确：公开面名字遵循语义化版本；
- `framework.*` 是内部 API，可随时变化；
- `hive` 的导入是惰性的，`import hive` 很轻；
- `SSEResponse` 需要 `server` extra；Colony/工具需要 `tools` extra。

### 4.5 发布

- `docs/releases/` 保存每个版本的详细说明；
- `CHANGELOG.md` 汇总架构性变更；
- release workflow 构建归档、校验 checksum、烟测（包含嵌入式 dashboard 与 Go 归档）；
- 版本号以 `core/pyproject.toml` 为准（前端 `0.1.0` 只是包内版本，UI 显示 Hive 版本）。

## 5. 可观测性

### 5.1 结构化日志

`observability/logging.py`：

- `StructuredFormatter` 输出 JSON：时间、level、logger、message、trace context、event、latency_ms、tokens_used、model、node_id；
- `ContextVar` 传播 `trace_id` / `execution_id` / `agent_id` / `node_id`；
- 终端模式可读，生产模式 JSON；
- ANSI 转义会被剥离，避免污染 JSON；
- `recent_logs` 环形缓冲供 Dashboard 的 Runtime logs 抽屉轮询（`GET /api/runtime-logs`）。

### 5.2 事件与决策

- `events.jsonl`：每个 session 的事件流（大字段剥除）；
- `HIVE_DEBUG_EVENTS`：全量事件 JSONL；
- `DecisionTracker`：记录 Agent 的决策（选项、选择、理由、结果），用于 Builder/调试；
- `RuntimeLogger` / `RuntimeLogStore`：传统图执行的 L1/L2/L3 日志，JSONL 追加，崩溃可保留；
- `HIVE_ENABLE_LLM_LOGS`：完整的 LLM 请求/响应 debug 日志（默认关闭，体积大）。

### 5.3 健康与资源

- `GET /api/health`：存活；
- `GET /api/health/resources`：进程树 + 系统内存采样，滚动历史；
- `host/runtime_health.py`：上游 LLM 网络故障标记，前端连接横幅；
- `host/runtime_resources.py`：资源采样与阈值告警；
- `/api/sessions/live`：live 会话 + 上游健康。

### 5.4 诊断工具

- `hive debugger`：本地 LLM debug 日志查看器；
- `HIVE_SENTINEL_LOG=1` + `scripts/sentinel_classify_debug.py`：Sentinel 分类决策；
- `scripts/llm_debug_log_visualizer.py`；
- `Colony data` 页：直接查看 Tracker 表/行/变更；
- `report-bundle`：用户主动上报问题时打包会话（凭据脱敏）。

## 6. 数据保留与 Janitor

`maintenance/`：

### 6.1 三层策略

| Tier | 对象 | 行为 |
|---|---|---|
| 1 | `event_logs/`、`llm_logs/`、`compaction_log/`、`tool-artifacts/`、轮转日志 | 直接删除（无消费方） |
| 2 | 已完成的 Colony Worker | 归档/删除 transcript + `data/`，写 `result.json` 墓碑，保留 `meta.json`/`tasks.json` |
| 3 | 冷 Queen 会话 | 删除孤儿 `data/*.txt`；重写 `events.jsonl` 去掉 `context_usage_updated` 遥测与 legacy 大 payload |

### 6.2 默认保留

`RetentionConfig`：

| 字段 | 默认 |
|---|---:|
| `enabled` | true |
| `mode` | `archive`（可选 `delete`） |
| `archive_dir` | `$HIVE_HOME/archive` |
| `event_logs_days` | 7 |
| `llm_logs_days` | 7 |
| `compaction_log_days` | 7 |
| `tool_artifacts_days` | 14 |
| `rotated_logs_days` | 30 |
| `worker_deep_clean_days` | 14 |
| `queen_hygiene_days` | 30 |
| `events_rewrite_min_bytes` | 1,000,000 |
| `active_grace_hours` | 48 |
| `batch_size` | 200 |
| `junk_min_bytes` | 50,000,000 |

每个字段可用 `HIVE_RETENTION_<FIELD>` 覆盖；env > 文件 > 默认。

### 6.3 安全模型

- **只在手动触发**（CLI 或 `POST /api/maintenance/janitor/run`），没有调度器；
- 默认 **dry-run**，必须显式 `--execute`；
- live 会话硬阻断；
- 年龄门槛取多个磁盘活动信号的最大值，不只看目录 mtime；
- 全局锁（2h TTL）+ server marker，防止 CLI 与服务并发；
- `KEEP_ALWAYS` 列表：`memories`、`charts`、`credentials`、`secrets`、`mcp_registry`、`skills`、`workspace`、`archive` 等永不触碰；
- 未知顶层条目会被 junk detector 报告，而不是直接删除。

## 7. Pipeline 中间件（传统请求路径）

`pipeline/` 提供一个请求级中间件框架：

```text
rate_limit → input_validation → cost_guard → LLM/MCP/credential/skill 初始化
```

- 阶段可 `continue` / `transform` / `reject`；
- 基础设施阶段在 `initialize()` 设置 LLM、ToolRegistry、accounts、SkillsManager；
- `build_pipeline_from_config` 从 `pipeline.stages` 构建。

它与 Queen 主路径的关系：

- Queen 的 SessionManager/QueenOrchestrator 自己装配 LLM/工具/凭据，不依赖这条 pipeline；
- pipeline 主要服务传统 `AgentHost` / 图执行路径与需要请求级限流/成本守卫的部署；
- `cost_guard` 是“每请求最大成本”的示例，不是全局预算。

## 8. 测试框架（Goal-Based Testing）

`framework/testing/` 是一套“按成功标准生成/运行测试”的框架：

- 用 `Goal.success_criteria` / `constraints` 生成测试；
- `pytest` 子进程执行，支持 xdist；
- `LLMJudge` 做语义评分；
- `ErrorCategorizer` 分类失败原因，`DebugTool` 给修复建议；
- `TestStorage` 持久化测试与结果；
- 审批 CLI 支持人工确认测试。

它更偏“Agent 工程的评测基础设施”，不是 Hive 自身的单元测试框架。

## 9. 文档与漂移

仓库文档质量整体不错，但存在漂移：

- `docs/configuration.md` 的存储路径仍是 v2；
- `tracker_db.py` 模块 docstring 仍写 `data/`；
- `run_worker` 工具描述的默认生命周期预算写 150，实际 200；
- 部分 README 的“13 个 Queen 六个默认”与代码一致，但具体人格名/领域需要看 YAML。

工程建议：

- 把“文档中的数字/路径”纳入契约测试（例如从代码生成配置表）；
- 对关键工具描述做快照测试，避免提示与实现漂移；
- 在 CI 中运行一个“文档示例可执行”子集。

## 10. 评价

**做得好的地方**

- 测试密度高，且覆盖了最容易出事故的子系统（恢复、压缩、预算、并行、Janitor）。
- Windows 矩阵 + 干净安装烟测 + 前端打包，说明发布工程是认真的。
- 默认 dry-run、live 会话保护、凭据脱敏上报，体现了对“破坏性操作”的克制。
- 可观测性从 trace 到事件到资源到 LLM 日志，层次完整。

**不足**

- 无覆盖率/类型检查门禁；
- live/e2e 与模型行为回归不在常规 CI；
- 文档漂移需要自动化治理；
- 部分核心文件过大，单测难以覆盖分支组合，更多依赖集成测试；
- 没有公开的性能/成本基准门禁。
