# 09 · 工程化、测试与发布

## 1. 仓库工程结构

- **Monorepo + uv workspace**：`core`（`aden-hive`）与 `tools`（`aden-tools`）两个 Python 包。
- **根 `pyproject.toml`** 只声明 workspace 成员和 Ruff 行宽。
- **`Makefile`** 提供 `lint`、`format`、`check`、`test`、`test-tools`、`test-live`、`test-all`、`install-hooks`、前端构建等目标。
- **`AGENTS.md` / `CLAUDE.md`**：给编码 Agent 的仓库规则（语义化版本、依赖变更需批准、多 Agent 安全等）。
- **`.editorconfig`、`.gitattributes`**：统一换行（`.sh/.ps1` LF，`.bat/.cmd` CRLF）。
- **`uv.lock`**：约 689 KB，锁定全部依赖。

## 2. 代码规模

| 区域 | 行数 | 文件数 |
| --- | --- | --- |
| `core/framework` | 135,653 | 349 |
| `tools/src` | 96,651 | 400 |
| `core/tests` | 50,732 | — |
| `tools/tests` | 49,359 | — |
| `core/frontend/src` | 227 个 TS/TSX | — |

最大的几个 Python 文件：

| 文件 | 行数 |
| --- | --- |
| `core/framework/agent_loop/agent_loop.py` | 7,497 |
| `core/framework/tools/queen_lifecycle_tools.py` | 4,000 |
| `core/framework/llm/litellm.py` | 3,678 |
| `core/framework/server/routes_sessions.py` | 3,265 |
| `core/framework/host/colony_runtime.py` | 2,935 |
| `core/framework/server/routes_execution.py` | 2,350 |
| `core/framework/server/session_manager.py` | 2,270 |
| `core/framework/host/event_bus.py` | 2,269 |

AGENTS.md 明确要求「保持文件合理小；必要时拆分而不是无限增长」，但现实是核心文件仍然很大。这是历史积累与迭代速度的折中。

## 3. 测试体系

### 3.1 三层结构

| 层 | 位置 | 内容 |
| --- | --- | --- |
| 跨模块集成 | `core/tests/` | 会话、Colony、Worker、存储、工具、迁移等（约 50.7k 行） |
| 框架单元 | `core/framework/*/tests/` | 与代码同目录的单元测试 |
| 工具测试 | `tools/tests/` | 集成工具的 mock 测试 + `integrations/` + `live/` |

另有：

- `core/tests/e2e/`：端到端场景。
- `core/tests/live/`：需要真实凭证的 live 测试。
- `tools/tests/live/`：标记 `pytest.mark.live`。

统计：约 392 个测试文件，5,891 个 `test_` 函数，测试代码合计约 100k 行，与生产代码量级相当。

### 3.2 测试基础设施

- `core/conftest.py`、`core/tests/conftest.py`、`framework/server/tests/conftest.py` 等提供夹具。
- 关键约束（在 1.0.12 release notes 中提到）：测试运行不会写入真实 `~/.hive`。
- `MOCK_MODE=1` 可在无 LLM 调用的情况下做结构校验。
- `MockLLMProvider` 支持确定性的循环测试。
- `core/framework/testing/` 是一个测试库（模块名 `test_*.py`，因此在 CI 中被排除）。

### 3.3 测试评价

**优点**

- 测试量真实，覆盖了存储、恢复、Worker、工具、路由等关键路径。
- 单元测试与代码同目录，定位方便。
- live 测试与 mock 测试分离，CI 可稳定运行。

**问题**

- 没有公开的覆盖率门槛（CI 不跑 coverage）。
- 前端测试数量相对少（36 个 `.test.*` 文件，对应 227 个 TS/TSX 文件），核心交互（ChatPanel、Worker 气泡）有针对性测试，但缺少端到端。
- `core/tests/e2e` 未在 CI 中显式运行。
- 大量测试依赖具体文案/常量，重构时容易大面积失败。

## 4. CI

`.github/workflows/ci.yml` 五个 job：

| Job | 平台 | 内容 |
| --- | --- | --- |
| `lint` | Ubuntu | Ruff check + format check（core 与 tools） |
| `test` | Ubuntu + Windows | `core/tests/` + `framework/` 单元测试 |
| `test-tools` | Ubuntu 24.04 + Windows | 安装 Playwright Chromium，跑 `tools/tests/` |
| `package` | Ubuntu | 构建前端 → 构建两个 wheel → 干净 venv 安装 → 运行 `scripts/check_installed_package.py` |
| `validate` | Ubuntu | 校验 `exports/*/agent.json`（若存在） |

值得肯定的点：

- **双平台**（Linux + Windows）测试，符合其跨平台承诺。
- **打包验证**：不仅构建，还在干净环境安装并运行，能抓到「workspace 里能跑、装出来不能跑」的问题。
- `ubuntu-24.04` 而非 `latest` 的注释说明遇到 Playwright 兼容问题，处理务实。

其他 workflow：

- `release.yml`：打 `v*` tag 时跑测试并创建 GitHub Release。
- `pr-requirements.yml`：要求 PR 关联 issue 且有 assignee（micro-fix/documentation 可豁免）。
- `pr-requirements-enforce.yml`：24 小时宽限期后关闭仍不合规的 PR。
- `auto-close-duplicates.yml`、`pr-check-command.yml`、`pr-requirements-backfill.yml`。

后者在开源项目中比较强硬，但配合 `CONTRIBUTING.md` 的「先领 issue」流程，能减少低质量 PR。

## 5. Lint 与格式

- **Ruff**（pre-commit `v0.15.0`）：
  - `core`：line-length 150，规则集 B/C4/E/F/I/Q/UP/W，target py311。
  - 根配置：line-length 120。
- **pre-commit** 在 core/tools 分别跑 ruff + ruff-format，自动修复。
- `.editorconfig` + `.gitattributes` 统一风格与换行。

没有看到 mypy/pyright 类型检查进 CI。对一个 135k 行的 Python 项目，这是明显的缺口。

## 6. 打包与发布

### 6.1 两个包

| 包 | 内容 | extras |
| --- | --- | --- |
| `aden-hive` | `framework` + `hive` + 构建后的 Dashboard | `webhook`、`server`、`tools`、`testing` |
| `aden-tools` | 终端、文件、图表、记忆、104 个集成、浏览器 | `dev`、`sandbox`、`browser` |

`aden-hive` 的核心依赖刻意保持精简（pydantic、anthropic、httpx、litellm、mcp、fastmcp、mcp2cli、croniter、psutil），把「工具」放到 `tools` extra，避免依赖膨胀。README 提到：只装 primitives 是 83 个包，装全部工具是 123 个。

### 6.2 Dashboard 打包

`core/pyproject.toml` 使用 hatch 自定义构建钩子 `hatch_build.py`，把 `frontend/dist` 打进 wheel 的 `framework/server/frontend`。因此：

- 从 PyPI 安装即自带 UI；
- sdist 也声明携带 `frontend/dist`，排除 `node_modules`。

### 6.3 CLI 入口

```toml
[project.scripts]
hive = "framework.cli:main"
hive-cred = "framework.credentials.cli:main"
```

`hive` 子命令通过注册函数分散在各模块：

- `framework.loader.cli`：serve/open/queen/colony/session/chat
- `framework.skills.cli`：skill 管理
- `framework.debugger.cli`：LLM 调试日志查看
- `framework.loader.mcp_registry_cli`：MCP 管理
- `framework.maintenance.cli`：janitor

## 7. 发布节奏与文档

- 版本从 1.0.0 到 1.0.14，release notes 非常密集，说明活跃开发。
- `CHANGELOG.md` 只摘要架构性版本，详细说明在 `docs/releases/`。
- 每个 release note 都有「New / Changed / Fixed」结构，且描述具体到行为，质量较高。
- `docs/i18n/` 提供 8 种语言的 README。
- `docs/internal/` 存放 PRD 与设计稿（如 `mcp-registry-prd.md`、`skill-registry-prd.md`、`browser-architecture-design.md`），说明有设计先行的习惯。

## 8. 依赖与供应链

- 锁定 `litellm==1.83.4`，这是合理的（LiteLLM 行为变化会影响所有 provider）。
- `aden-tools` 依赖较重（playwright、pandas、psycopg2、pytesseract 等），但被隔离在 `tools` extra。
- 使用 `uv.lock` 全量锁定。
- AGENTS.md 明确：版本升级、补丁依赖、override、vendored 变更需要显式批准。
- 没有看到 Dependabot/Renovate 配置；对于一个快速迭代的项目，这可能是刻意的（避免自动升级噪音）。

## 9. 工程化评价

**强项**

- 双平台 CI + 干净环境打包验证，是很多项目缺失的一环。
- 测试体量与生产代码相当，且分层清晰。
- pre-commit + Ruff 统一风格，`.gitattributes` 处理跨平台换行。
- 发布说明具体、可追溯。
- 依赖隔离（primitives vs tools）做得克制。

**缺口**

- **无静态类型检查**：135k 行 Python，没有 mypy/pyright。考虑到大量 `Any` 与动态工具 schema，类型检查成本高，但收益也大。
- **无覆盖率门槛**：测试多，但不知道哪些路径没覆盖。
- **E2E 未进 CI**：`core/tests/e2e` 存在但未在 workflow 中运行。
- **单文件过大**：`agent_loop.py` 7,497 行，重构风险随每次改动累积。
- **无依赖自动更新**：安全补丁可能滞后。
- **前端测试偏少**：227 个 TS/TSX 文件，36 个 `.test.*` 文件。

**建议**

1. 先上 mypy 的「渐进模式」：从 `core/hive`、`framework/llm`、`framework/storage` 这些边界清晰、类型价值高的模块开始，逐步扩大。
2. 加 coverage 报告（不一定要卡门槛，先让趋势可见）。
3. 把 `core/tests/e2e` 接进 CI（至少 nightly），并补一条前端 Playwright 冒烟链路。
4. 为 `agent_loop.py` 制定拆分计划：先抽 `TurnRunner`、`ToolExecutor`、`CompactionController`，每步保持行为不变。
5. 启用 Dependabot 的 security-only 更新，平衡噪音与安全。
