# 14 · 质量保证

> 质量目标只有一句话：**每一条上游行为都有可追溯的 Go 测试，且门禁能在任何人本地与 CI 上以相同结果运行。**
> 本文描述门禁的规则、命令、当前基线与缺口。数值是 2026-11-18 13:10 的快照，实时结果以命令输出为准。

## 1. parity 注解即契约

### 1.1 语法

```go
// Parity: <python 文件路径>::<python 测试函数名>
func TestSessionIDAndTraversal(t *testing.T) { ... }

// Parity: core/tests/test_tracker_db.py::test_ensure_tracker_db_creates_fresh_db
func TestEnsureTrackerDBCreatesFreshDB(t *testing.T) { ... }
```

一个测试覆盖多条上游用例时，逐条列出：

```go
// Parity:
//   core/tests/test_skill_parser.py::test_frontmatter_colon
//   core/tests/test_skill_parser.py::test_name_fallback
func TestParserFrontmatter(t *testing.T) { ... }
```

Go 特有的测试必须显式豁免并说明原因：

```go
// Parity-exempt: 验证 6 平台交叉编译常量，上游无对应行为。
func TestPlatformMatrix(t *testing.T) { ... }
```

### 1.2 放置规则

- 注解必须位于 `func Test...` **正上方的注释块**中；注释块与函数之间最多允许一个空行。
- `// Parity:` 请写**单行**、多条引用用逗号分隔（`path::test_a, path::test_b`）。`check_parity_targets.sh` 只解析单行注解，多行块中的引用不会进入引用审计。
- 任何非注释代码行都会切断归属，防止注解「漂移」到下一个测试。
- `// Parity:` 指向的 Python 文件与测试必须真实存在；评审按路径回溯，而不是看名字。
- 豁免不是免死金牌：`Parity-exempt` 必须写清「为什么上游没有对应物」，且不应滥用。

### 1.3 门禁

两道门禁，彼此互补：

**第一道 — 存在性**：`go_hive/scripts/check_parity.sh`

1. 遍历所有 `*_test.go`；
2. 对每个 `func Test` 检查前置注释块是否含 `Parity:` 或 `Parity-exempt:`；
3. 按包汇总 tests / annotated / missing，并打印未标注测试的 `file:line`；
4. 任一缺失 → 退出码 1；全部通过 → 退出码 0。

**第二道 — 可回溯性**：`go_hive/scripts/check_parity_targets.sh`

1. 抽取每条 `// Parity:` 中逗号分隔的引用；
2. `*.py::test_xxx` 必须在 Python 树中真实存在同名 `def`（硬失败）；
3. `*.py` 无测试名、类名锚点、散文说明记为「shorthand/prose」软警告；
4. 引用的 `.py` 文件或测试函数不存在 → 退出码 1。找不到 Python 树时 SKIP（退出 0），便于独立检出。

```bash
bash go_hive/scripts/check_parity.sh
bash go_hive/scripts/check_parity_targets.sh go_hive/go-hive /path/to/python-root
# 或
make -C go_hive/go-hive parity parity-targets PY_ROOT=/path/to/python-root
```

### 1.4 当前可回溯性

截至 2026-11-18（agents 发现/删除/元数据路由与会话创建、live_tools 落地后的最新基线，与第 2 节测试账本一致）：

- 2,215 个 Go 测试全部带注解（`check_parity.sh` OK）；
- 2,035 条 `// Parity:` 引用全部通过目标审计（`check_parity_targets.sh` OK，0 missing file / 0 missing test）；
- 46 条属于 shorthand/prose（类名锚点、跨语言向量说明等），由人工评审确认语义。

历史快照（2026-11-18 02:54，MCP 连接池落地后）：1766 个 Go 测试、1801 条引用；保留仅为追踪演进。

第二道门禁已经兑现价值：它抓到过 `tracker` 包十余条「名字看起来像 pytest、实际不存在」的注解（例如 `test_upsert_insert_then_update` 实际叫 `test_upsert_inserts_then_updates`），这些已按真实 Python 测试名逐条改正。评审时仍应把「语义一致」作为独立检查项——脚本只能证明引用存在，不能证明断言等价。

## 2. 测试命名与映射

- Go 测试名采用 `Test<Domain><Behavior>`，不逐字复制 Python 的 snake_case；对应关系由注解承载，而不是由名字猜测。
- 测试文件按「被测模块」聚合（如 `trust_test.go` 覆盖信任门禁的全部用例），不要求与 Python 的 `test_*.py` 一一同名。
- 一个 Go 测试可以覆盖一个 Python 测试的多个分支；但**不允许**多个 Go 测试共享一个宽泛注解却各自验证不同行为。
- 断言应验证**可观察行为**：返回值、错误、落盘内容、提示词文本。避免只断言「不 panic」。

文件级映射（节选，完整表见 `15-test-parity-matrix.md`）：

| Go 测试 | Python 参考 |
| --- | --- |
| `internal/skills/discovery_test.go` | `core/tests/test_skill_discovery.py` |
| `internal/skills/trust_test.go` | `core/tests/test_skill_trust.py` |
| `internal/memory/memory_test.go` | `core/tests/test_queen_memory.py` |
| `internal/tracker/tracker_test.go` | `core/tests/test_tracker_db.py` |
| `internal/credentials/storage_test.go` | `core/framework/credentials/tests/test_credential_store.py` |
| `internal/llm/provider_test.go` | `core/tests/test_litellm_provider.py` |
| `internal/ratelimit/rate_limiter_test.go` | `core/tests/test_social_rate_limiter.py` |
| `internal/session/session_test.go` | `core/tests/test_node_conversation.py`、`core/tests/test_path_traversal_fix.py` |
| `internal/colony/worker_loop_config_test.go` | `core/tests/test_loop_config_overrides.py` |
| `internal/tools/execute_test.go` | `core/tests/test_tool_executor_isolation.py`、`core/tests/test_tool_context_propagation.py`、`core/tests/test_tool_timeout_probe_policy.py` |
| `internal/tools/tier_invariants_test.go` | `core/tests/test_toolset_tier_invariants.py` |
| `internal/agentloop/loop_tool_timeout_test.go` | `core/tests/test_tool_timeout_probe_policy.py` |

## 3. 离线确定性构建

所有门禁默认在以下环境运行：

```bash
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod
```

| 变量 | 作用 |
| --- | --- |
| `CGO_ENABLED=0` | 禁止 cgo，与发布形态一致 |
| `GOPROXY=off` | 不访问代理；依赖必须已在 module cache |
| `GOSUMDB=off` | 不访问校验数据库 |
| `GOFLAGS=-mod=mod` | 允许按 `go.mod` 解析，不隐式 vendor |

CI 的做法是**先在线 `go mod download` 预热缓存，再离线执行 `make ci`**，从而同时满足「首次构建能拿到依赖」与「构建过程可复现」。

本地首次运行前需要一次联网的 `go mod download`；之后即可完全离线。当前 module cache 已包含全部依赖，实测离线通过。

## 4. 静态检查

| 检查 | 命令 | 当前状态 |
| --- | --- | --- |
| 格式 | `make fmt-check`（`gofmt -l .`） | 通过，无输出 |
| 静态分析 | `make vet`（`go vet ./...`） | 通过，无输出 |
| 编译 | `make build`（`go build ./...`） | 通过 |
| 竞态 | `make race`（`go test -race ./...`） | 通过（darwin/arm64，全部包） |

Go 编译器本身提供完整的静态类型检查，因此不需要 Python 项目常见的 mypy/pyright 步骤；`go vet` 补充检查可疑构造（printf 格式、锁复制、不可达代码等）。

## 5. 竞态与并发测试

`make race` 需要 cgo（`-race` 不支持 `CGO_ENABLED=0`），因此该目标显式用 `CGO_ENABLED=1` 运行 `go test -race ./...`（需要本机有 C 编译器）；实测全部通过：

```
ok  internal/atomicfs     1.735s
ok  internal/credentials  2.064s
ok  internal/hivehome     1.418s
ok  internal/memory       2.385s
ok  internal/session      3.062s
ok  internal/skills       3.387s
ok  internal/tracker      4.665s
```

竞态检测**不在** `make ci` 中（耗时明显长于常规测试），而是由 CI 的 `race` job 在 nightly `schedule` 与 `workflow_dispatch` 下运行；本地仍建议在改动并发相关包（`credentials` 缓存、`skills` 重载、`atomicfs`、`eventbus`、`tasks`）时主动跑 `-race`。

## 6. 当前 parity 基线

`bash go_hive/scripts/check_parity.sh` 实测输出（摘要）：

| 包 | tests | annotated | missing | 覆盖率 |
| --- | --- | --- | --- | --- |
| `internal/agentloop` | 318 | 318 | 0 | 100% |
| `internal/agents` | 34 | 34 | 0 | 100% |
| `internal/atomicfs` | 4 | 4 | 0 | 100% |
| `internal/attachments` | 5 | 5 | 0 | 100% |
| `internal/cli` | 88 | 88 | 0 | 100% |
| `internal/colony` | 84 | 84 | 0 | 100% |
| `internal/credentials` | 48 | 48 | 0 | 100% |
| `internal/eventbus` | 113 | 113 | 0 | 100% |
| `internal/globaldb` | 23 | 23 | 0 | 100% |
| `internal/hivehome` | 3 | 3 | 0 | 100% |
| `internal/llm` | 215 | 215 | 0 | 100% |
| `internal/llmjudge` | 22 | 22 | 0 | 100% |
| `internal/maintenance` | 42 | 42 | 0 | 100% |
| `internal/mcpregistry` | 160 | 160 | 0 | 100% |
| `internal/memory` | 46 | 46 | 0 | 100% |
| `internal/orchestrator` | 40 | 40 | 0 | 100% |
| `internal/playbook` | 32 | 32 | 0 | 100% |
| `internal/queen` | 10 | 10 | 0 | 100% |
| `internal/ratelimit` | 21 | 21 | 0 | 100% |
| `internal/runtimehealth` | 3 | 3 | 0 | 100% |
| `internal/schemas` | 10 | 10 | 0 | 100% |
| `internal/server` | 279 | 279 | 0 | 100% |
| `internal/session` | 17 | 17 | 0 | 100% |
| `internal/sessionreport` | 8 | 8 | 0 | 100% |
| `internal/skills` | 219 | 219 | 0 | 100% |
| `internal/storage` | 55 | 55 | 0 | 100% |
| `internal/tasks` | 128 | 128 | 0 | 100% |
| `internal/tools` | 136 | 136 | 0 | 100% |
| `internal/tracker` | 35 | 35 | 0 | 100% |
| `internal/triggers` | 17 | 17 | 0 | 100% |
| **合计** | **2,215** | **2,215** | **0** | **100%** |

脚本退出码为 **0**。注解形态不是「一个测试一条注解」的平均分布：一个 Go 测试覆盖多条 Python 用例时在注解中逐条列出，Go 特有行为则写 `Parity-exempt` 并给出理由（例如 `internal/credentials/fernet_test.go` 的跨语言向量）。因此 2,215 个测试对应 2,035 条可解析的 `path::test` 引用加 46 条 shorthand/prose；按块统计，1,838 个测试的注释块仅含带 `.py` 路径的 `Parity:` 行，376 个仅有 `Parity-exempt` 说明（另有 1 个注释块两者兼有）。

近期增量集中在以下各处：`internal/tools/mcp_connection_manager_test.go` 直接移植 `core/tests/test_mcp_connection_manager.py` 的 16 个用例（引用、refcount、并发 acquire/release、connect/disconnect 失败、健康检查四种传输、重连保留 refcount、完全释放后的重连报错、cleanup_all），另有 1 条 `Parity-exempt` 固定上游 `get_instance()` 的单例契约；`internal/agentloop/budget_cap_test.go`（5 条）、`internal/colony/adaptive_budget_test.go`（14 条）与 `internal/server/worker_list_test.go`（1 条）覆盖自适应运行预算及 worker 列表序列化，详见 6.7；`internal/server/global_data_test.go`（8 条）覆盖 global-db 数据路由决策层，详见 6.8；`internal/server/session_dirs_test.go`（14 条：8 条上游对等 + 6 条 `Parity-exempt` 补强）覆盖会话目录解析与 resume 归属，详见 6.9；`internal/server/judge_test.go`（3 条）与 `stall_check_test.go`（7 条上游对等，含经 `RegisterConfigRoutes` 驱动的判官槽保存用例）覆盖判官槽与停滞检查，详见 6.10；`internal/server/config_features_test.go`（10 条）与 `provider_library_test.go`（8 条）覆盖配置特性开关、LLM 槽位与 provider 库决策层；`internal/server/routes_config_test.go`（15 条上游对等 + 2 条 `Parity-exempt`）把其中 15 个 HTTP 用例经真实 `httptest` 服务跑通，与决策层的 3 个 getter 用例合起来覆盖全部 18 个上游配置用例，详见 6.11。两条豁免中，一条钉住严格 JSON 解析（坏 JSON 400、可解析的非对象 500），一条用 25 个并发 PUT 复现并钉住配置文件的读改写串行化——上游靠单事件循环天然避免的丢写，在 Go 并发 handler 下由包级互斥量保证。并发用例在 `-race` 下通过。其后 `internal/server/routes_config_meta_test.go`（19 条：1 条上游对等 + 18 条 `Parity-exempt`）覆盖模型目录、sentinel、rate limits、profile、avatar 与 external skills 六组元信息路由；`internal/server/llm_config_test.go`（24 条：2 条上游对等 + 22 条 `Parity-exempt`）覆盖 `GET/PUT /api/config/llm` 的读取与 custom、订阅、API key 三条写入分支，并驱动真实 `httptest` 服务断言状态码、响应体与落盘；本轮新增 7 条钉住配置数值的源拼写透传（`UseNumber`/`stringPointer`）、provider 别名跳过、无 `api_base` 订阅的陈旧键清理、仅布尔 `false` 拒绝写入、custom 变更中止时不落盘、尾随垃圾读空。补齐此前漏列的两份测试文件：`internal/server/app_test.go`（8 条：2 条上游对等 + 6 条 `Parity-exempt`）覆盖健康检查、启动状态、desktop token、no-store 与错误兜底；`internal/server/boot_status_test.go`（3 条 `Parity-exempt`）覆盖启动阶段历史与就绪切换。

最新一轮（agents 发现/删除/元数据路由 + 会话创建与 live_tools，2150→**2215**）：新增包 `internal/agents`（`framework/agents/discovery.py` 与 `framework/agents/validate.py`，6 个实现文件 / 781 行，34 条测试）：`discovery_test.go`（14 条 `Parity-exempt`，上游无 Python 测试）钉住 colony 分组与元数据、worker 配置解析、软删除跳过、目录名 title 回退与 birthtime、`len()` 怪癖、worker+queen 会话计数、`session_` 前缀与递归 `runs.jsonl`、`state.json` 时间戳词序最大值、排序与排除 stem、`str.title()` 词边界与 `WorkerEntry.to_dict`；`validate_test.go`（20 条对等）逐条移植 `core/tests/test_validate_agent_path.py` 的允许根、正反路径、符号链接与 NUL 字节用例。服务端新增 `routes_agents.go`（`GET /api/discover`、`DELETE /api/agents`、`PATCH /api/agents/metadata`、`POST /api/credentials/check-agent`）、`routes_session_create.go`（`POST /api/sessions` 的普通与 fork-into-new-colony 分支）、`routes_session_live_tools.go`（`GET /api/sessions/{id}/live_tools`），对应测试 12 + 14 + 5 条，其中仅 `test_check_agent_credentials_rejects_traversal` 有上游对等测试，其余 30 条为 `Parity-exempt` 并写明契约。主树 2150→**2215**（+65：agents 34、server 31），refs 2007→**2035**，仅含 `Parity-exempt` 的注释块 337→**376**（1 个块两者兼有），仅含 `.py` `Parity:` 的注释块 1,812→**1,838**。两条并发车道合并后 `make ci` 实测退出码 0：`fmt-check`、`vet`、`test`、六平台 `cross`、两个 parity 脚本全绿；独立验证待跑。

上一轮（W3 report-bundle 与 `internal/sessionreport`，并行车道合并后的快照，2120→**2150**）：新增包 `internal/sessionreport`（`framework/server/_session_report.py`，1 个实现文件 / 346 行，8 条 `Parity-exempt`，上游无 Python 测试），逐条钉住八类凭据正则（JWT、Bearer、api-key/secret 赋值、OAuth 回调 URL、`sk/pk/rk`、GitHub、Slack、Google）、调试上下文保留、`user_report.json` 的 label/label_source/reporter 与非法 severity 回退 medium 及覆盖写、tar.gz 的 `data/reports` 排除与文本脱敏/二进制原样、非法 UTF-8 的 U+FFFD 替换、二进制单文件与总量预算、manifest 统计、`ensure_ascii` 风格的 `\uXXXX` 转义以及 SHA-256 向量。服务端新增 `routes_session_report.go` 与 `routes_session_report_test.go`（246 行，4 条 `Parity-exempt`）：`POST /api/sessions/{id}/report-bundle` 的 404 分支、响应信封/size/sha256/marker/stats、活会话的 `queen_resume_from` 存储 ID 优先级与 `queen_dir` 标记落点、坏 JSON 空体与未知 severity 回退。并行车道（colony 生命周期与 dismiss-pivot）同时合入 18 条服务端测试，server 包 226→**248**；主树 2120→**2150**，refs 保持 **2007**，仅含 `Parity-exempt` 的注释块 307→**337**（1 个块两者兼有），仅含 `.py` `Parity:` 的注释块保持 **1,812**。本快照时 `internal/sessionreport` 与 `internal/server` 的作用域测试、`gofmt`、`go vet`、`go build`、两个 parity 脚本实测为绿；树级 `make test` 与 `make cross` 受并发车道（W3 agents、W4 create-session）在途改动影响，待其收尾后复跑；独立验证待跑。

上一轮（W2 实时会话/附件路由组 + 全库注释清理，2101→**2120**）：`internal/server/routes_session_live_test.go`（2 条，均 `Parity-exempt`）覆盖 `framework/server/routes_sessions.py::handle_get_session_snapshot` 的投影/404 分支与 `handle_live_sessions_stream` 的首帧快照；`internal/server/routes_session_attachment_test.go`（6 条，均 `Parity-exempt`）覆盖 `handle_upload_attachment` 的文件名净化、文本探测与 MIME 校验、`handle_session_attachment` 的读取分支与 `_colony_root_for_session_dir` 的根目录解析；新增包 `internal/attachments`（`tools/src/aden_tools/utils/attachments.py`，5 条 `Parity-exempt`，上游无 Python 测试）钉住 basename 路径剥离与扩展名强制、空白折叠与 200 字符上限、重名后缀、二进制/非法 UTF-8 拒绝、MIME 允许清单；新增包 `internal/runtimehealth`（`framework/host/runtime_health.py`，3 条 `Parity-exempt`，上游无 Python 测试）钉住网络错误标记分类、健康生命周期与降级原因 240 字符截断；`internal/colony/worker_started_at_test.go`（2 条：1 条上游对等 `core/tests/test_worker_started_at.py::test_info_started_at_is_wall_clock`，1 条指向 `framework/host/worker.py::WorkerInfo` 的零值默认）补齐 worker 墙钟 `started_at`。主树 2101→**2120**，refs 2004→**2007**，仅含 `Parity-exempt` 的注释块 290→**307**，仅含 `.py` `Parity:` 的注释块 1,810→**1,812**（1 个块两者兼有）。同一轮按「除 parity 注解外禁止代码注释」的要求清理全库：用 `go/parser` 按字节区间删除非注解注释，累计移除 4,604 + 820 + 3 个注释区间，`gofmt -l` 归零，`//go:build`、`//go:embed` 等编译指令保留；清理后六道门禁实测全绿。独立验证待跑。

上一轮（会话读写路由组 W1，2088→**2101**）：`internal/server/routes_session_history_test.go`（10 条：1 条上游对等 + 9 条 `Parity-exempt`）覆盖 `core/tests/test_session_titles.py::test_title_route_sets_and_clears_a_users_title`（PATCH 设置去空白标题、空串清除后返回 `null`、`ReadTitle` 读到空）与九条 Go 侧边界：非 DM/非字符串标题拒绝且不落盘、写失败返回通用 500 信封、历史列表的冷/活 overlay 与 colony fork 排除、colony id 路径段校验、colony 列表保留 fork、active-session 跳过无消息会话、删除目录并停止 live 会话、reveal 建目录并调用启动器、删除时拒绝符号链接。实现侧新增 `internal/server/routes_session_history.go`（262 行，六个 handler：history/colony 列表/active/delete/title/reveal，含 `decodeJSONValue` 的 `UseNumber` 与尾随数据拒绝、live overlay、`validateColonyIDSegment`）、`internal/platform/open.go`（39 行，darwin `open` / windows `explorer` / 其他 `xdg-open`，Popen-无 wait 语义）、`internal/session/cold.go`（241 行，冷会话列举、colony 列表与 newest-with-messages 选择）、`internal/server/session_dirs.go`（211 行，DM/colony 目录解析与 `FindQueenSessionDir`/`QueenDMSessionDir`）并接线 `internal/hivehome/home.go`（173 行）与 `app.go`。`internal/session/cold_test.go` 2→4 条（+2 豁免，覆盖 colony fork 保留与空会话跳过），`internal/server/session_dirs_test.go` 13→14 条（+1 豁免，`TestQueenDMSessionDirAcceptsOnlyDMSessions`）。主树 2088→**2101**，refs 2003→**2004**，仅含 `Parity-exempt` 的注释块 278→**290**，仅含 `.py` `Parity:` 的注释块 1,809→**1,810**。六道门禁实测全绿，独立验证待跑。

上一轮（触发器路由组与运行时，2059→**2088**）：`internal/server/routes_session_triggers_test.go`（29 条：8 条上游对等 + 21 条 `Parity-exempt`）覆盖 `framework/server/tests/test_api.py::TestSessionCRUD` 的 3 条 PATCH 用例（`test_update_trigger_task`、`test_update_trigger_cron_restarts_active_timer`、`test_update_trigger_cron_rejects_invalid_expression`，478–539 行）与 `tests/test_missed_triggers.py` 的 5 条 `test_resolve_missed_*`（fire_latest 注入补跑、skip 推进锚点而不触发、reschedule 设定未来 next_due、未知 trigger 返回 marker、非法 decision 返回 marker）；21 条豁免逐条钉住 Go 侧分支：创建校验/成功/ID 冲突后缀、坏 JSON、`run` 的 colony/queen 守卫与 forced 注入及注入失败 500、activate/deactivate 计时器生命周期、webhook 触发注册与事件分发、无 webhook server 时的 500、`resolve_missed` 路由包装、PATCH 守卫与配置合并、`EmitTriggerFired` 载荷、`PersistActiveTriggers`、计时器触发与 fire 统计、`NextDueFrom`、`slugifyTriggerID`、webhook server 的四种响应与无路由不启动。实现侧新增 `routes_session_triggers.go`（586 行，六条 HTTP 路由）、`trigger_runtime.go`（708 行，slugify/next-due/计时器播种/webhook 流订阅/`PersistActiveTriggers`/`ResolveMissed`）与 `webhook_server.go`（270 行，精确路径、HMAC-SHA256、JSON-or-raw、`WEBHOOK_RECEIVED`）。主树 2059→**2088**，refs 1995→**2003**，仅含 `Parity-exempt` 的注释块 257→**278**，仅含 `.py` `Parity:` 的注释块 1,801→**1,809**。

上一轮（会话详情四路由，2050→**2059**）：`internal/server/routes_session_detail_test.go`（9 条：5 条上游对等 + 4 条 `Parity-exempt`）覆盖 `framework/server/tests/test_api.py::TestSessionCRUD` 的 `test_get_session_found`（200、`session_id`、`has_worker`、`entry_points` 存在）、`test_get_session_not_found`（404 与逐字 `Session 'x' not found`）、`test_stop_session`（DELETE 200 `stopped: true`，随后 GET 404）、`test_stop_session_not_found`（404）、`test_session_stats`（透传 colony `get_stats`）；4 条豁免分别钉住 `is_loading` 的 202 分支、`get_cold_session_info` 的磁盘投影（`cold/live`、`has_messages`、`meta.json` 覆盖 `created_at`、agent 与 colony 字段）、`entry_points` 的顶层 `next_fire_in`/`next_fire_at`/`fire_count`/`last_fired_at`（且不混入 `trigger_config`）、`GET /api/sessions/{id}/triggers` 的路由包装。实现侧新增 `routes_session_detail.go`，`SessionRouteDeps`/`AppDeps` 增加 `Manager` 并让它优先于 `LiveSessions`（对应上游 `_get_manager(request).list_sessions()`），`app.go` 接线。主树 2050→**2059**，refs 1990→**1995**，仅含 `Parity-exempt` 的注释块 253→**257**，仅含 `.py` `Parity:` 的注释块 1,796→**1,801**。

上一轮（事件历史端点 Verifier 复审，2044→**2050**）：独立验证发现初版把 `_truncate_large_event` 误标为 `Parity-exempt`——上游 `core/tests/test_events_history_tail.py` 本有 4 条截断用例——另有三处语义与 CPython 有偏差。已逐项修复：`internal/server/routes_events_history_test.go` 从 9 条增至 15 条，新增 4 条上游对等截断用例（`test_truncate_strips_full_request_field`、`test_truncate_replaces_oversized_field`、`test_truncate_leaves_small_events_untouched`、`test_truncate_handles_missing_or_nondict_data`）与 2 条 `Parity-exempt`（Python `int()` 查询参数语法、ASCII-only 空白行过滤），并把 `json.dumps` 数值尺寸估计补成表驱动；实现侧 `ReadEventsPage` 的空白过滤由 `bytes.TrimSpace` 改为只剥 `" \t\n\r\v\f"`（Python `bytes.strip()` 不剥 U+00A0，该行仍占页位）、`queryIntParam` 改走 `parsePythonInt`（Unicode 空白、正负号、数字间单下划线）、`pythonJSONLen`/`writePythonNumber` 用任意精度整数与 `float.__repr__` 复刻 `len(json.dumps(value, default=str))`，并以 `json.Number` 解码日志行避免大整数落成 float64。`internal/eventbus/events_history_tail_test.go` 的移交注释同步为「9 条仍属 server 车道，4 条 truncate 已落地」。主树 2044→**2050**，refs 1986→**1990**，仅含 `Parity-exempt` 的注释块 251→**253**。

上一轮（事件历史游标分页初版，2035→**2044**）：`internal/server/routes_events_history_test.go` 新增 9 条——8 条逐条移植 `core/tests/test_events_history_page.py`：7 组「事件数 × limit」参数（0/1/5/10/12/50/3 与 5/10/500）在「有/无尾换行」两态下重放客户端翻页循环，断言拼接后顺序为 0..n−1、绝对 seq 为 1..n、单页不超 limit；300 条 × 8192 字节填充的日志跨过 1MB 反向读阈值后按 limit 50 恰好分 6 页且总数 300；首页即尾页（100 条取 10，得到 90..99 且 `start_offset > 0`）；游标页 `total = -1` 不再重算全文件；末页 `start_offset == 0`；显式偏移 0 返回空页且 `total = -1`；handler 成功路径落盘 3 行并断言 `returned/total == 3`；慢读取（200ms）配 20ms 预算返回 503 `events_history_timeout`。初版第 9 条以 `Parity-exempt` 覆盖 `_truncate_large_event` 的基本形状（该豁免在复审中被替换为 4 条上游对等用例）。实现侧 `internal/server/routes_events_history.go` 完整移植 `handle_session_events_history` 与 `_read_events_page`/`_count_events_total`/`_truncate_large_event`；`routes_sessions.go` 增加 `Home`/`EventsReadPage`/`EventsReadTimeout` seam 并挂载路由，`app.go` 把 `Home` 传入会话路由装配。主树 2035→**2044**，refs 1978→**1986**。

上一轮（`hive debugger` 命令，2027→**2035**）：`internal/cli/debugger_test.go` 新增 8 条，全部 `Parity-exempt`——上游 `framework/debugger/cli.py` 没有单元测试，所以每条都写明理由并钉住 `cmd_debugger` 的一个分支：命令形态（帮助文本、描述、七个 flag 的顺序/种类/默认值，以及 `rootCommand()` 中 skill < debugger < mcp < janitor 的注册顺序）、日志目录缺失时打印两行说明并退出 0、目录存在但无 `*.jsonl`（含把普通文件当作 `--logs-dir` 传入）走同一分支、可视化脚本缺失时打印带路径的提示并退出 1、七个 flag 按上游固定顺序透传（含显式 `--limit-files 0`）且子进程退出码原样返回、未指定或显式假值不转发、默认日志目录取 `HIVE_HOME/llm_logs` 且不作为 `--logs-dir` 转发、非法 `--port` 走 argparse 用法错误（退出 2，`'abc'` 单引号 repr）。实现侧 `internal/cli/debugger.go` 完整移植 `framework/debugger/cli.py`：`runtime.Caller(0)` 复现 `Path(__file__).resolve()` 后上溯到仓库根的 `scripts/llm_debug_log_visualizer.py`，`python3` 作为 `sys.executable` 的替身，`subprocess.call` 的继承 stdio 与退出码由 `defaultDebuggerExec` 复现；`cli.go` 增加 `DebuggerScript`/`DebuggerExec`/`DebuggerPython` 三个 seam，`args.go` 的非法 int 报错改用 `pyRepr` 以匹配 argparse 的 `%r` 单引号。主树 2027→**2035**，refs 不变（新测试无 `.py` 引用），仅含 `Parity-exempt` 的注释块 242→**250**。

上一轮（`hive-cred` 独立入口，2017→**2027**）：`internal/cli/cred_test.go` 新增 10 条，逐条移植 `core/tests/test_hive_cred_cli.py`——`token` 对过期 OAuth 先刷新再打印（值来自刷新后的 `access_token`、`expires_in_seconds > 3000`）、纯文本模式只输出值本身、刷新失败以 `reauth_required`（退出 1）而非陈旧 token 成功、未知账号 `not_found`（退出 4）、错误消息点名可用的「credential name」命名空间、已知凭据 ID 提示读取同一字段（adapter 失败时提示为空）、控制台脚本注册（读取 `cmd/hive-cred/main.go` 断言 `package main` 与 `cli.NewCredRunner()`）、`browse` 只列账号与 identity 而永不出现密钥值（含别名过滤）、`inspect` 只列 key 名与类型、`token` 解析本地收集账号（带/不带 `--account default` 均得 `sk_local_value`）。实现侧 `internal/cli/cred.go` 完整移植 `framework/credentials/cli.py` 并复用 `internal/tracker/errors.go` 的退出码族（用法错误 6、存储不可用 3、缺失 4、需重连 1）；`internal/credentials/resolve.go` 导出 `LocalRegistryDefault` 供收集账号查找；`cmd/hive-cred/main.go` 注册独立控制台入口。主树 2017→**2027**，refs 1968→**1978**。

上一轮（`hive janitor` 命令组，2005→**2017**）：`internal/cli/janitor_test.go` 新增 12 条，全部 `Parity-exempt`——上游 `framework/maintenance/cli.py` 没有单元测试（只有 `core/tests/test_janitor_*.py` 覆盖库层），所以每条都写明理由并指向它依赖的库层语义：组帮助列出 `run`/`status`、缺子命令退出 2、dry-run 报告文本与退出 0、两条非法 `--tiers` 分支、空白容忍、活服务拒绝对破坏性 tier 执行、tier 1 在服务在线时仍可执行、无记录时的 `status` 退出 1、已保存报告渲染、`_print_report` 逐字节格式（列宽/空目标跳过/错误计数/执行头）、manifest 逐行渲染与坏行截断、`_human` 单位阶梯。实现侧 `internal/cli/janitor.go` 完整移植 `framework/maintenance/cli.py`，`rootCommand()` 在 loader、skill、mcp 之后追加 `janitor`。主树 2005→**2017**，refs 不变（新测试无 `.py` 引用）。

上一轮（`hive mcp` 根入口接线，1999→**2005**）：`internal/cli/mcp_test.go` 新增 6 条——3 条上游对等（`core/tests/test_mcp_registry_cli.py` 的子命令全量注册、`mcp list` 空表、`mcp update nonexistent` 退出 1）与 3 条 `Parity-exempt`（根帮助列出 `mcp`、`--json`/`--` 参数整段字节透传、未知子命令退出 2）。实现侧在 `Runner` 增加 `MCPMain` seam，并在 `rootCommand()` 中以 `Passthrough` 组注册 `mcp`，把命令名之后的 token 原样交给 `mcpregistry` 自己的 argparse 兼容解析器；`args.go` 相应增加 `Passthrough`/`rest`/`rawArgs()`。测试通过 `mcpregistry.NewAtBase(t.TempDir())` 与 `RegistryFunc` 注入隔离注册表，不触网、不读真实 `HIVE_HOME`。主树 1999→**2005**，refs 1965→**1968**。

上一轮（queen 默认档案对等测试，1989→**1999**）：`internal/queen/profiles_test.go` 10 条逐条移植 `framework/agents/queen/tests/test_queen_profiles_defaults.py` 的全部用例——13 个默认 queen 从 `queen_defaults/*.yaml` 加载且 name/title 非空、capability 配置一律为空、`DefaultToolsFor` 对已知/未知 id 的行为、`_materialize_default_queen` 只写 persona（不落 `default_tool_categories` / `default_preset_skills`）、未改动默认的自愈等价性（`reflect.DeepEqual` 对齐 `default_profile_persona`）、未知 id 返回空、退役标题（`Head of Sales` → `Head of RevOps`）只在精确命中时替换而用户自选标题永不改动、无退役标题的 queen 原样透传。为了让 Python `default_skills_for` 的语义直接可测，`profiles.go` 新增 `DefaultSkillsFor`（返回 `map[string]struct{}`，对应上游 `frozenset`）。本轮另含一处账本校正：`profiles_test.go` 落盘前同一批次的两条用例在旧表中重复计数，已按实测归位；主树 1989→**1999**，refs 1955→**1965**。

上一轮（会话列表路由 + TTY 语义 + 快照校正，1974→**1989**）：`internal/server/routes_sessions.go` 移植 `handle_list_live_sessions` 与 `_session_to_live_dict` 的 15 字段负载（`has_worker` 由 `colony_id` 派生、`agent_path` 取 `worker_path`、`queen_phase` 的 staging/planning 回退、`queen_supports_images` 走模型能力目录、`uptime_seconds` 按 `round(…, 1)` 半偶舍入），`SessionManager` 以切片维护插入序并支持原位替换与停用；`routes_sessions_test.go` 6 条（2 条上游对等 + 4 条 `Parity-exempt`）。同期修正 stdin TTY 判定：Python `sys.stdin.isatty()` 是 ioctl 语义，早期 Go 用 `os.ModeCharDevice` 会把 `/dev/null` 误判为终端；改用 `go-isatty` 后与上游一致，并由 `TestStdinIsTTYFalseForDevNull`、`TestPromptYesNoNonInteractive`、`TestSkillForkNonInteractiveAborts` 三条 `Parity-exempt` 钉住。快照校正另计入 6 条此前已落地但未写入文档的用例：`loader_test.go` 的 4 条会话列表 HTTP 用例（cli 45→52 中的 +4）与 `session/cold_test.go` 的 2 条冷会话用例（session 13→15）；因此本轮实际新增 9 条（server +6、cli +3），其余 6 条为账本补齐。

更早一轮（CLI 子系统，1907→**1974**）：`internal/cli/skill_test.go`（36 条）逐条移植 `core/tests/test_skill_cli_commands.py` 全部 9 个测试类——`init` / `validate` / `doctor` / `install` / `remove` / `search` / `info` / `test` 与 `--json` 输出形态，经 Runner 的注入缝装配假注册表、假 provider 与假判官，断言退出码、stdout/stderr 文本和解析后的 JSON 结构；`internal/cli/cli_test.go`（6 条）与 `loader_test.go`（3 条）覆盖根入口帮助/未知命令与前端构建、`hive open` 的 ping→serve 顺序；`internal/llmjudge/judge_test.go`（22 条）覆盖注入 provider、五种响应解析、错误处理、懒加载 Anthropic 客户端与 LiteLLM OpenAI 回退。Python 的 `sys.path` 引导（`TestConfigurePaths`）在 Go 中由编译器静态解析取代，作为文档化缺口记录，不上报为未注解测试。

### 6.1 已完成的补齐

上一轮基线（349 tests / 267 annotated / 82 missing）已清零，路径是：

| 包 | 曾缺失 | 处理方式 |
| --- | --- | --- |
| `credentials` | 27 | 补到 `framework/credentials/tests/*`；Fernet 原语、密钥引导细节等 Python 交给 `cryptography` 库的行为写 `Parity-exempt` 并说明 |
| `skills` | 28 | 补到 `tests/test_skill_*.py`；纯 Go 的目录/排序细节写 `Parity-exempt` |
| `tracker` | 12 | 补到 `tests/test_tracker_db.py` / `test_tracker_tools.py`；休眠的 legacy 路径回退写 `Parity-exempt` |
| `session` | 5 | 失效路径 `tests/test_session_store.py` 改为真实存在的 `test_node_conversation.py`、`test_path_traversal_fix.py` |
| `memory` | 6 | 补路径并收窄弱注解 |
| `atomicfs` | 2 | 失效路径 `framework/utils/tests/test_io.py` 改为 `tests/test_skill_trust.py::TestTrustedRepoStore::test_atomic_write` 与 `tests/test_skill_registry.py::TestCacheAtomicWrite::test_atomic_write_uses_tmp_then_replace` |
| `hivehome` | 2 | 补到 `tests/test_hive_home_isolation.py` |
| `tools` 执行器 | 9 | `execute.go` 此前零测试；补 `test_tool_executor_isolation.py` / `test_tool_context_propagation.py` / `test_tool_timeout_probe_policy.py` 异步探测 5 例 |
| `agentloop` 超时预算 | 2 | `test_tool_timeout_probe_policy.py::test_loopconfig_prefix_timeout_resolution` + 1 条 Go 接线豁免；同时补上 `LoopConfig` 超时字段与 `ResolveToolTimeout` |

本轮另行补齐的 `tools` 分层 keep-set 7 条不是历史缺口，而是为上游 `test_toolset_tier_invariants.py` 建档：浏览器/终端类别拆分、keep-set 类别存在性、实测冷尾排除、worker 上限（24）与热核保留。常量与上游一致地放在测试文件内，生产表不变。

新增的第二道门禁（targets）在补齐过程中又抓出 12 条「名字近似但不存在」的 tracker 注解，已全部改为真实测试名。原则保持：**每条注解必须能指向真实存在的 Python 测试；无法对应的写 `Parity-exempt` 并说明**。

### 6.2 LiteLLM provider 纯逻辑层

`internal/llm/provider.go`（904 行）把 `core/framework/llm/litellm.py` 中可离线验证的 helper 全部落成纯 Go，并以 `internal/llm/provider_test.go` 的 84 条测试逐条镜像 `core/tests/test_litellm_provider.py`：模型/端点分类、缓存控制前缀白名单、静态/动态 system 分块、历史缓存断点、Retry-After 解析与指数退避+抖动、backpressure 地板、缓存 token 提取优先级、成本优先级（usage.cost → hidden response_cost → 目录定价）、截断 JSON 修复、工具格式转换、日志摘要。

三处有意替换，均在测试内写明：

| 上游 | Go | 理由 |
| --- | --- | --- |
| `litellm.completion_cost` / `cost_per_token` | `CostFromCatalogPricing` + `model_catalog.json` | LiteLLM 运行时依赖无法离线复现；目录价与测试中的期望数值逐项核对 |
| Python `None` 组件参与成本计算 | 负数钳 0 | Go 无 `None` 语义，钳制保证不产生负成本 |
| `response=None` 与缺属性两种异常路径 | 单一 `ProviderError` 形状 | Go 结构体字段缺失即零值，两条路径合并后仍有测试覆盖 |

### 6.3 LLM 请求塑形与 extra kwargs

`internal/llm/request_shape.go`（261 行）补齐 `core/framework/llm/litellm.py` 与 `core/framework/config.py::get_llm_extra_kwargs` 中剩余的离线可验证逻辑，由 `request_shape_test.go` 的 5 条测试承载（4 条带 `Parity:`、1 条 `Parity-exempt`，合计 10 条 `path::test` 引用）：

- `IsAnthropicModel`：小写前缀 `anthropic/` 或 `claude-`；经 OpenRouter 路由的 Anthropic 模型不命中。
- `RequestParallelToolCalls` / `NoteError` / `ParallelToolCallsRefused`：仅在有工具且非 Anthropic 时发送；网关报错文本含 `parallel_tool_calls` 时锁存一次并重试一次，之后不再询问（每 provider 实例独立）。
- `BuildOpenRouterToolCompatMessages`：persona + `"\n\n"` + 兼容指令 + Python `json.dumps` 形状的工具表；支持 cache_control 的子提供商拆成「静态缓存块 + 动态块」，否则拼接为单串；滚动历史断点落在最后一条消息；丢弃无 tool_calls 的空 assistant 消息；输入消息浅拷贝不被改写。
- `ExtraLLMKWargs`：claude-code 订阅 → codex 订阅（`User-Agent: CodexBar`、`store=false`、`allowed_openai_params: ["store"]`）→ ollama `num_ctx`（默认 16384）→ `extra_body` 透传；订阅分支缺 key 时按上游语义落到后续分支。

尚未移植：`hive/`→`anthropic/`、`kimi/`→`anthropic/` 的代理模型改写（上游无测试覆盖）、真实 HTTP `AComplete`、流式 tool-compat/thinking-block 管线的消费侧接线。这些列入 backlog，不影响本层已锁定的纯函数契约。

### 6.4 LLM complete 非流式路径

`internal/llm/complete.go`（259 行）把 `LiteLLMProvider.complete`（`litellm.py:1858`）与 `_completion_with_rate_limit_retry`（`litellm.py:1686`）落成可离线验证的 Go 实现，由 `complete_test.go` 的 10 条测试承载（7 条带 `Parity:`、3 条 `Parity-exempt`，合计 7 条 `path::test` 引用）：

- **请求组装**：`max_tokens` 缺省 → `ResolveAuxMaxTokens`（`DEFAULT_MAX_TOKENS = 8192` 上限）；显式 system 以裸 `{"role":"system","content":...}` 前置（上游此处不走 `_build_system_message`，Go 保持同一线格式）；`ApplyHistoryCacheBreakpoint`；`json_mode` 追加 JSON 指令；kwargs 顺序 base → `ExtraKwargs` → `api_key`/`api_base`；工具 `ToolToOpenAIFormat` 后按 `setdefault` 补 `tool_choice`（Ollama `auto`、Hive 代理 `required`）；`response_format` 透传。
- **重试循环**：有内容或工具调用即返回；空响应重试上限 `min(retries, EMPTY_RESPONSE_MAX_RETRIES=3)`，末条非 system 消息为 `assistant` 或 `finish_reason == "length"` 时短路；429 以 `ComputeRetryDelay(..., jitter)` 退避、预算 `RATE_LIMIT_MAX_RETRIES=10`；其他错误不重试。
- **响应归一化**：`Content`/`Model`（空则回落 provider 模型）/`StopReason`/`ToolCalls`/`CostUSD`（`ExtractCost`）/输入输出 token/缓存与 creation token。
- **可测性**：网络经 `CompletionCall` 注入，退避经 `SleepFunc` 注入，包内零网络依赖；调用方消息浅拷贝后再打缓存断点，避免改写会话（上游会原地 mutate，这是有意的安全分歧）。
- 3 条 `Parity-exempt` 的理由已写在注释里：上游重试循环依赖 `litellm.RateLimitError` 异常类型、`tool_choice`/`extra_kwargs` 优先级无独立测试，故用注入传输层锁定接线与 kwargs 组装。

### 6.5 AnthropicProvider 向后兼容包装

`internal/llm/anthropic.go`（171 行）复刻 `core/framework/llm/anthropic.py` 的薄包装，由 `anthropic_test.go` 的 9 条测试承载（7 条带 `Parity:`、2 条 `Parity-exempt`，合计 7 条 `path::test` 引用）：

- **构造契约**：默认模型 `claude-haiku-4-5-20251001`，自定义模型原样保留；`APIKey` / `Model` / `Provider`（`_provider` 对应物）在构造后立即可读，内部 provider 拿到同一模型与密钥。
- **密钥解析**：显式参数 → 注入的凭据存储（`SetAnthropicCredentialLookup`）→ `ANTHROPIC_API_KEY`；全部缺失时报上游原文错误文本。凭据存储以包级注入点接入，避免 `internal/llm` 依赖 `internal/credentials` 的具体单例。
- **委托**：`Complete` 原样透传 messages 与全部 `CompleteOptions`（含 `max_tokens`、`response_format`），`json_mode` 仍走提示词注入而非 `response_format`，与上游一致。
- **类型桥接**：`AComplete` 经 `ChatMessagesToMaps` 把结构化消息转成 OpenAI dict 形态——assistant `tool_calls` 为 `{"id","type":"function","function":{"name","arguments"}}`、纯工具轮 content 为 `nil`、tool 结果带 `tool_call_id`；`SystemDynamicSuffix` 按 `LLMProvider.acomplete` 的 fallback 以空行拼接。这两项是 Go 特有的胶水，故对应测试标 `Parity-exempt` 并写明理由。
- 上游 7 条 `TestAnthropicProviderBackwardCompatibility` / `test_anthropic_provider_passes_json_mode` 逐条对应，断言口径（返回值、kwargs、system 内容）与 Python 保持一致。

### 6.6 社交平台限流器

`internal/ratelimit/rate_limiter.go`（576 行，纯 Go + `modernc.org/sqlite`）复刻 `core/framework/rate_limiter.py`，由 `rate_limiter_test.go` 的 21 条测试承载（20 条带 `Parity:`、1 条 `Parity-exempt`，合计 20 条 `path::test` 引用）：逐条镜像 `core/tests/test_social_rate_limiter.py`——建库建表、空库放行、小时/日/周限额、账户与跨平台隔离、最小间隔、记录与归一化、4 条环境变量覆盖（提升、钳天花板、非法回退、周限额钳制）、日/周计数、未知动作放行、`min_delay` 表。

- **限额解析顺序**（env > `configuration.json` > 默认）：env 名 `HIVE_RATE_<PLATFORM>_<ACTION>_<WINDOW>` 使用调用方原始拼写的大写形式，解析后钳到天花板；配置文件键为小写 `<platform>.<action>.<window>`，按 `max(1, int(v))` 取整但不钳天花板（与上游的差异被单独测试锁定）；非法 env 值继续下探；配置文件缺失/损坏静默无覆盖。
- **判定顺序**：最小间隔 → hourly(3600, 不停) → daily(86400, 停) → weekly(604800, 停)。阻断结果携带已经算出的 count/limit，未定义窗口保持 nil 以对应 Python `None`；重试秒数按 `(最早记录 + 窗口) - now` 计算、保留一位小数并钳 ≥0。
- **存储契约**：`actions` 表与 `idx_actions_lookup` 索引名和列序与上游一致，默认库 `$HIVE_HOME/social_rate_limits.db`，因此 Go 宿主与 Python 宿主可共享同一 SQLite 文件；记录写入时 platform/action 统一小写化。
- **可测性**：时钟函数可替换；`SetConfigOverrides` 注入配置源，避免开发机 `~/.hive/configuration.json` 影响期望值。测试用第二条 `sql.Open` 直插记录，等价于 Python 测试里的 `sqlite3.connect`。
- `Parity-exempt` 的 1 条注释写明理由：上游没有针对配置文件中间优先级及其失败回退的测试，也没有覆盖「配置值钳 1 但不钳天花板」的差异。

### 6.7 自适应 worker 运行预算

`internal/colony/adaptive_budget.go`（262 行，纯 Go）复刻 `core/framework/host/colony_runtime.py` 的 `_maybe_adapt_colony_budget` / `_apply_adaptive_budget` / `get_stats().adaptive_tool_budget`；`internal/agentloop/loop.go` 增加配套的 `ApplyLifetimeBudgetCap` / `LifetimeBudget` / `ToolCallsUsed` / `BudgetLimited` 读取器。测试分两处，逐条镜像 `core/tests/test_adaptive_tool_budget.py`：

| 层 | 测试文件 | 条数 | 覆盖 |
| --- | --- | --- | --- |
| agentloop | `internal/agentloop/budget_cap_test.go` | 5 | setter 收缩成功；拒绝抬升/相等/零/负数/无上限循环；拒绝无宽限轮；`ToolCallsUsed` 读取；`BudgetLimited` 读实时 `tool_lifetime_budget_grace` |
| colony | `internal/colony/adaptive_budget_test.go` | 14 | 样本不足不动；收缩到地板并钳在途 worker；公式 max-guard；中位数抬升被拒；证据抬升从陈旧低值恢复；抬升封顶 profile base；抬升不波及已钳在途 worker；max-guard 跨窗口淘汰；离散度守卫；常态昂贵时封顶 base 不收缩；样本排除矩阵；kill switch；准入钳制；`get_stats` 名义值 |

- **政策矩阵被直接驱动**：测试不经 agent 循环与调度器，而是用合成 `SUBAGENT_REPORT` payload 直接调 `maybeAdaptColonyBudget`，与上游用 `_success()` / `_StubWorker` 的做法一致。`withFixedBudgetConstants` 钉住 `AdaptiveBudgetFloor=30`、`AdaptiveBudgetMinSamples=3`，避免开发机 env 改变期望值（对应上游 autouse fixture）。
- **可观测断言**：空间里断言的是 worker 的**有效预算**（`AgentLoop.LifetimeBudget()`，上游 stub 的 `effective_budget`），而不是内部字段；`get_stats` 测试断言 `enabled` / `nominal`（首次收缩前为 nil）/ `samples` 三元组。
- **路由序列化（已闭合）**：上游 `test_workers_route_serializes_budget_telemetry` 已由 `internal/server/worker_list_test.go` 覆盖——`WorkerInfoToDict` 复刻 `_worker_info_to_dict`，在 result 中输出 `tool_calls_used` / `budget_limited`，缺字段的旧结果按 Go 零值回退为 0 / false（等价上游 `getattr` 默认）。HTTP handler 接线仍属 P3，但序列化契约本身已对等，不再记为缺口。

### 6.8 global-db 数据路由决策层

`internal/server/global_data.go`（219 行，纯 Go）复刻 `core/framework/server/routes_colony_workers.py` 中 global-db 代理路由的决策逻辑；`internal/server/global_data_test.go` 的 8 条测试逐条镜像 `core/framework/server/tests/test_global_data_routes.py`：

| 上游测试 | Go 测试 | 覆盖 |
| --- | --- | --- |
| `test_derive_lead_id_prefers_linkedin_slug` | `TestDeriveLeadIDPrefersLinkedInSlug` | LinkedIn 优先：去 scheme/www、截断 `?`/`#`、去尾斜杠并小写 |
| `test_derive_lead_id_falls_back_to_email` | `TestDeriveLeadIDFallsBackToEmail` | email 去空白小写；两者都缺失时 ok=false |
| `test_insert_lead_derives_pk_and_uses_insert_mode` | `TestInsertLeadDerivesPKAndUsesInsertMode` | 派生 `lead_id`、`mode="insert"`、响应 `{"inserted":1,"pk":{...}}` |
| `test_insert_lead_without_identifier_400s_before_backend` | `TestInsertLeadWithoutIdentifierRejectsBeforeBackend` | 400 且错误含 "email or LinkedIn"，不产生 upsert |
| `test_insert_conflict_maps_to_409_with_pk` | `TestInsertConflictMapsTo409WithPK` | 409 信封 `error="conflict"` + attempted pk |
| `test_changes_init_omits_since` | `TestChangesInitOmitsSince` | 空 since 视为初始化、不传游标 |
| `test_changes_passes_cursor_through` | `TestChangesPassesCursorThrough` | 游标原样透传 |
| `test_changes_signed_out_maps_to_401` | `TestChangesSignedOutMapsTo401` | `NotSignedInError` → 401 + `signed_out=true` |

上游经 aiohttp `TestClient` 断言 HTTP 外层；Go 把 handler 的纯决策从 HTTP 适配器中拆出直接测试，`PlanInsertRow` 的 `mintID` 可注入以固定非 int PK 的生成。HTTP handler 接线仍属 P3，决策与序列化契约本身已对等。

### 6.9 会话目录解析

`internal/server/session_dirs.go`（211 行，纯 Go）复刻 `core/framework/server/session_manager.py` 的会话目录解析；`internal/server/session_dirs_test.go` 的 14 条测试中，8 条逐条镜像 `core/framework/server/tests/test_session_dir_resolution.py`：

| 上游测试 | Go 测试 | 覆盖 |
| --- | --- | --- |
| `test_colony_id_read_off_the_session_path` | `TestColonyIDReadOffTheSessionPath` | `colonies/acme/queens/<q>/sessions/s1` 读出 colony `acme` |
| `test_queen_dm_session_path_has_no_colony` | `TestQueenDMSessionPathHasNoColony` | `queens/<q>/sessions/s1` 无 colony 归属 |
| `test_generator_finds_a_colony_only_session` | `TestGeneratorFindsAColonyOnlySession` | 仅 colony 树存在时生成器恰好产出该目录 |
| `test_generator_yields_every_dir_sharing_an_id` | `TestGeneratorYieldsEveryDirSharingAnID` | DM + colony 同 id 时产出两目录，DM 在前 |
| `test_resume_colony_is_found_despite_a_duplicate_dm_dir` | `TestResumeColonyIsFoundDespiteADuplicateDMDir` | 重复 DM 目录排前时 resume 仍绑定 colony |
| `test_resume_colony_is_none_for_a_real_dm_session` | `TestResumeColonyIsNoneForARealDMSession` | 真实 DM 会话不绑定 colony |
| `test_resume_colony_is_none_when_nothing_matches` | `TestResumeColonyIsNoneWhenNothingMatches` | 无匹配时无 colony |
| `test_first_match_lookup_still_prefers_the_dm_tree` | `TestFirstMatchLookupStillPrefersTheDMTree` | 首匹配查找仍偏好 DM 树 |

上游用 monkeypatch 把模块全局 `QUEENS_DIR` / `COLONIES_DIR` 指向临时目录；Go 改为显式传入 `hivehome.Home`。`listDirIfExists` 保留 `Path.exists()` 吞掉 stat 错误（视为缺失）而 `iterdir()` 错误上抛的差异；`ColonyIDForSessionDir` 只认祖先，路径自身从不视为归属。

独立验证随后发现三处 Python 语义细节，已修正并由 6 条 `Parity-exempt` 测试钉住（上游 fixture 未覆盖）：

| 语义点 | 上游行为 | 修正 |
| --- | --- | --- |
| 生成器惰性 | `_iter_queen_session_dirs` 为生成器，首匹配消费者不会列举后续目录，排序靠后的坏目录不得上抛 | `forEachQueenSessionDir` 访问器，`visit` 返回 false 即停，错误仅在真正走到时返回 |
| 符号链接 | `Path.is_dir()` 跟随符号链接，符号链接的 queen/colony 树仍是候选 | `entryIsDir` 用 `os.Stat` 替代 `DirEntry.IsDir` |
| 路径规范化 | `pathlib.Path` 构造时去掉尾分隔符，`Path("/colonies/acme/")` 的父链不含 colony | `ColonyIDForSessionDir` 先 `filepath.Clean` 再走父目录 |

对应用例：`TestColonyRootWithTrailingSeparatorHasNoColony`、`TestSessionDirTraversalFollowsSymlinkedTrees`、`TestLookupsStopBeforeALaterListingError`、`TestFirstMatchStopsBeforeAnUnreadableColonyTree`、`TestLookupsPropagateAnEarlyListingError`。第 6 条 `TestQueenDMSessionDirAcceptsOnlyDMSessions` 钉住会话改名路由依赖的 DM 判定：`QueenDMSessionDir` 只接受 queen DM 会话，同 id 的 colony 副本返回未找到。坏目录用「本应是 queens 目录的位置放普通文件」构造 `ENOTDIR`，不依赖权限，root 与非 root 下行为一致。与 Python 参考实现的黑盒对照（20 个目录场景 × iter/find/derive、11 个纯路径探针、25 个规范化输入）差异数为 0。

### 6.10 判官槽与停滞检查

`internal/server/judge.go`（155 行）与 `stall_check.go`（243 行）均为纯 Go，逐行复刻 `core/framework/server/judge.py` 与 `stall_check.py`；配置读取集中在 `hive_config.go`，可见错误判定在 `internal/agentloop/visible_error.go`。

`judge_test.go` 的 3 条测试逐条镜像 `core/framework/server/tests/test_judge.py`：

| 上游测试 | Go 测试 | 覆盖 |
| --- | --- | --- |
| `test_a_channel_asks_the_configured_judge` | `TestAChannelAsksTheConfiguredJudge` | 槽的 `model`、问题、state、criteria 原样交给协议；返回概率 0.8 |
| `test_no_answer_without_a_usable_judge` | `TestNoAnswerWithoutAUsableJudge` | 无槽、无 key、未知协议三种情形均 `ok=false` 且协议未被调用 |
| `test_no_answer_when_the_judge_fails` | `TestNoAnswerWhenTheJudgeFails` | 协议报错吞为 `ok=false` |

上游经 `ChannelHost.judge` 间接驱动；channel host 尚未移植，Go 直接调用 `AskJudge`，可观察契约相同（配置的协议收到四元组、返回值或「无答案」）。

`stall_check_test.go` 的 7 条测试全部与上游对等：前 6 条覆盖宣布工作却无工具调用 → 恰一次提醒且 state 逐字段相等；判官认为无碍 → 不提醒；失败模型调用（网关以普通回复返回并带可见错误前缀）不判定；`stall_check` 关闭 → 不判定；工具调用后只看最后一次模型调用的 reasoning/reply 与工具名；用户指出上一件作品缺陷时携带 `previous_turn`（上一回复 600 字符尾部 + 工具列表），本回合工具为空。第 7 条经 `RegisterConfigRoutes` 启动真实 HTTP 服务，验证 `/api/config/llm-sections` 保存 judge 段时跳过聊天 key 校验且逐字回读，`/api/config/features` 打开 `stall_check` 后判官槽立即可用。

异步语义：上游 `asyncio.create_task` 让判定不阻塞 publish；Go 的 `Publish` 也等待处理器，因此判定在 goroutine 中运行，并用 `StallCheckSubscription` 的 `WaitGroup` 提供确定性 `Wait`/`Close`，测试以 `handle.Wait()` 同步，同时避免 20 秒判官超时拖住事件总线。空工具列表以 `[]string{}` 表示，序列化为 `[]` 而非 `null`。

### 6.11 配置特性开关、LLM 槽位与 provider 库

`internal/server/config_features.go`（250 行）与 `provider_library.go`（244 行）均为纯 Go，逐行复刻 `core/framework/server/routes_config.py` 的决策层与 `core/framework/config.py` 的开关解析；`routes_config.go`（135 行）是薄适配层，把 7 条路由挂到 Go 1.22+ 模式 mux，所有写 handler 共用严格整包 JSON 解析（空体/坏 JSON/非对象/尾随垃圾统一 400 `Invalid JSON body`），并经 `ConfigRouteDeps` 注入会话列表、验证器与热切换。

`config_features_test.go` 的 10 条测试逐条镜像 `core/tests/test_routes_config_features.py`：

| 上游测试 | Go 测试 | 覆盖 |
| --- | --- | --- |
| `test_getter_defaults_true` | `TestTheAdaptiveBudgetGetterDefaultsToOn` | 缺省 `adaptive_tool_budget=true` |
| `test_getter_reads_config_file` | `TestTheAdaptiveBudgetGetterReadsTheConfigFile` | 文件布尔优先、非布尔回退 |
| `test_getter_env_wins_over_file` | `TestTheAdaptiveBudgetEnvironmentVariableWinsOverTheFile` | `HIVE_*` 覆盖文件 |
| `test_get_features_default` | `TestTheFeaturesEndpointReportsTheDefaultToggles` | GET 四项缺省值 |
| `test_put_memory_timeline_persists` | `TestPuttingTheMemoryTimelinePersistsAndTheGetterAndGetSeeIt` | 落盘 + getter + GET 一致 |
| `test_put_email_senders_persists_and_publishes_env` | `TestPuttingEmailSendersPersistsAndRepublishesTheEnvironment` | 落盘 + `HIVE_EMAIL_SENDERS=1/0` |
| `test_put_features_persists_and_get_reflects` | `TestPuttingAFeaturePersistsItAndGetReflectsIt` | 响应回显与 GET 回读 |
| `test_put_features_preserves_other_config_keys` | `TestPuttingFeaturesPreservesUnrelatedConfigKeys` | 无关键原样保留 |
| `test_put_features_validation` | `TestPuttingFeaturesValidatesTheBody` | 非布尔/无已知键/非对象均 400 且不写盘 |
| `test_put_features_hot_applies_respecting_colony_pins` | `TestPuttingTheAdaptiveBudgetHotAppliesButColonyPinsWin` | `colonies_applied`、pin 优先、无 colony 跳过 |

`provider_library_test.go` 的 8 条测试逐条镜像 `core/tests/test_routes_provider_library.py`：

| 上游测试 | Go 测试 | 覆盖 |
| --- | --- | --- |
| `test_get_library_empty` | `TestTheProviderLibraryStartsEmpty` | 空库 `{}` |
| `test_put_saves_entry_verbatim` | `TestPuttingALibraryEntryStoresItVerbatim` | 含未知字段的段逐字存取 |
| `test_put_validation` | `TestPuttingALibraryEntryValidatesIt` | 缺名/缺 model/非对象均 400 |
| `test_put_null_deletes_and_prunes_empty_library` | `TestDeletingLibraryEntriesPrunesAnEmptyLibrary` | 删除、剪空库键、删不存在条目 200 |
| `test_apply_to_worker_slot` | `TestApplyingALibraryEntryToTheWorkerSlotCopiesItVerbatim` | 槽与库互相独立、无热切换 |
| `test_apply_to_llm_slot_hot_swaps_sessions` | `TestApplyingALibraryEntryToTheMainSlotHotSwapsSessions` | `deepseek/deepseek-chat`、key、空 base、计数 |
| `test_apply_unknown_name_and_role` | `TestApplyingRejectsAnUnknownNameOrRole` | 404 与 400 |
| `test_apply_health_checks_key_against_base` | `TestApplyingHealthChecksTheKeyAgainstTheBase` | 保存不校验、apply 校验、失败不改槽 |

验证器与热切换都以函数注入（`RoleWriteDeps`），测试能精确断言 `(provider, api_key, api_base, model)` 四元组与 `(full_model, api_key, api_base)` 三元的调用参数，而不依赖网络或运行中会话。`routes_config_test.go` 另行用 15 条测试经真实 `httptest` 服务重放上游的 15 个 HTTP 用例（features 7 + provider library 8），并补 1 条 `Parity-exempt` 用例验证四个写路由统一拒绝空体、坏 JSON、非对象与尾随垃圾；特性文件余下 3 个 getter 用例仍由本文件的决策层测试覆盖。

## 7. CI

`.github/workflows/go.yml`：

| Job | Runner | 内容 |
| --- | --- | --- |
| `quality` | ubuntu-latest | 预热 module cache → `make ci`（fmt-check + vet + test + cross + parity + parity-targets），`CGO_ENABLED=0` |
| `cross` | ubuntu-latest × 6 | 矩阵 `darwin/linux/windows × amd64/arm64`，逐目标 `go build ./...`，有 main 包时上传二进制 |
| `race` | ubuntu-latest | 仅 `schedule`（nightly 03:17 UTC）与 `workflow_dispatch` 触发；`CGO_ENABLED=1` 预热缓存后离线 `go test -race ./... -count=1` |

当前基线：`make ci` 全绿（本地实测，2026-11-18）。`parity-targets` 在只含 `go_hive` 的检出中因找不到 Python 树而 SKIP；在同时含有 Python 参考树的检出中（本地 `PY_ROOT=/Users/mac/hive/core`）以 1952 条引用全部命中通过。门禁没有为变绿而放宽：83 条历史缺口全部通过补注解或写明 `Parity-exempt` 理由关闭。

## 8. 本地验收命令

```bash
cd go_hive/go-hive

make fmt-check    # gofmt 检查
make vet          # go vet
make test         # 离线测试
make race         # 竞态检测（可选，较慢）
make cross        # 六平台编译
make parity       # parity 注解门禁（2215/2215）
make parity-targets PY_ROOT=/Users/mac/hive/core   # 引用可回溯性（2035 条）
make ci           # 依次执行 fmt-check + vet + test + cross + parity + parity-targets
```

当前预期结果：全部通过。`make ci` 默认 `PY_ROOT=../../core`（本仓库布局下即 Python 参考树），会执行完整引用审计；独立检出中该目录不存在时 parity-targets 自动 SKIP。其他布局可显式传 `PY_ROOT`。

## 9. 已知质量缺口

| 缺口 | 影响 | 建议 |
| --- | --- | --- |
| 46 条 shorthand/prose 注解（类名锚点、跨语言向量说明） | 不能逐条自动回溯 | 保持人工评审；新增注解优先写完整 `path::test` |
| Go 对等测试 2,215 个（2,035 条引用）；静态计数上游 `def test_` 约 1,971 个（`core/`）与 3,001 个（`tools/`，含类方法，参数化展开前） | 大多数上游行为尚无 Go 测试 | 按子系统推进（见 `15-test-parity-matrix.md`），先安全与格式路径 |
| `-race` 只覆盖 nightly | 并发回归可能在两次 nightly 之间漏检 | 改动并发包时本地跑 `-race`；如需更严可把 race 提到 PR 门禁（成本更高） |
| 无覆盖率统计 | 不知道哪些路径未覆盖 | 加 `go test -coverprofile`，先只展示趋势，不设硬门槛 |
| 未实现包无接口测试 | AgentLoop/Colony/工具落地时容易偏离上游 | 落地前先写「契约测试」，再写实现 |
| Windows 只编译不测试 | 平台差异可能直到用户报告才暴露 | CI 增加 windows runner 跑 `go test ./...`（排除 live 测试） |
| 没有 mutation/基准测试 | 关键路径的边界值靠人眼 | 对 Fernet、SQL 门禁、会话遍历等安全路径补表驱动边界用例 |

## 10. 评审清单

提交前自查：

- [ ] 新增/修改的每个 `Test` 都有 `Parity:` 或 `Parity-exempt:`；
- [ ] 注解指向的 Python 测试存在且语义一致（`make parity-targets` + 人工语义复核）；
- [ ] `gofmt -l .` 无输出；
- [ ] `go vet ./...` 无输出；
- [ ] `CGO_ENABLED=0` 下测试通过；
- [ ] 涉及并发时本地跑过 `-race`；
- [ ] 没有为了通过测试而删除断言或放宽门禁；
- [ ] 落盘格式变更同步更新了 `12-design.md` 与相关文档。
