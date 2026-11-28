# Trigger pipeline（配置驱动的 trigger 中间件）

> 上游参考：`core/framework/pipeline/`（`stage.py` / `runner.py` / `registry.py` / `stages/*.py`）
> Go 实现：`go_hive/go-hive/internal/pipeline/` + `go_hive/go-hive/internal/colony/trigger.go`
> 配置键：`configuration.json` → `pipeline.stages`（`contracts/config-keys.json` 第 135 键，状态 `aligned`）
> 生产可达性：**partial（机制/API surface）+ out_of_scope（生产触发路径）**。`Trigger` 没有生产调用方，
> 上游同样没有；生产触发器走 queen 事件注入。调用图与证据见 §7。

## 1. 机制

`pipeline` 是 **trigger 入口的中间件**：API 调用方每调用一次 `ColonyRuntime.Trigger`，在 spawn worker
之前把请求依次交给按 `order` 升序排列的 stage。机制本身是 opt-in 的——没有配置
`pipeline.stages` 就得到一个空管线，`Trigger` 直接 spawn，与上游 `PipelineRunner([])`
的行为一致。注意：这是公开 runtime API 的形状，不是 server 的生产触发器路径；生产触发
（手动/定时）注入 queen 节点，两侧都不经过 `Trigger`（§7）。

三个核心类型（`internal/pipeline`）：

| 类型 | 字段 / 方法 | 对应上游 |
| --- | --- | --- |
| `Context` | `EntryPointID`、`InputData`、`CorrelationID`、`SessionState`、`Metadata` | `PipelineContext` |
| `Result` | `Action`（`continue` / `reject` / `transform`）、`InputData`、`RejectionReason` | `PipelineResult` |
| `Stage` | `Name`、`Order`、`Initialize`、`Process`、`PostProcess` | `PipelineStage` |

`Runner` 的语义与 `runner.py` 逐条对齐：

- 构造时按 `Order()` **稳定排序**（`sort.SliceStable`），同序保持注册顺序；
- `Run` 顺序执行 stage：`reject` 立即短路并返回 `*RejectedError`（消息
  `Pipeline rejected by <stage>: <reason>`，空 reason 用上游的 `(no reason given)` 占位）；
  `transform` 且 `InputData != nil` 时替换上下文输入；其余（含未知 action）按 `continue` 处理；
- stage 自身报错返回 `*StageError`（包装原始错误，`errors.Is/As` 可区分「拒绝」与「故障」）；
- `InitializeAll` 启动时依次初始化，**第一个失败即中止启动**，不静默跳过；
- `RunPost` 依次调用 `PostProcess`，单个 hook 的失败被吞掉（不破坏已成功执行）；
- 空 runner 的 `Run` 是 no-op，原样返回上下文。

## 2. 配置示例

```json
{
  "pipeline": {
    "stages": [
      {"type": "llm_provider", "config": {"model": "anthropic/claude-sonnet-5-5"}},
      {"type": "credential_resolver"},
      {"type": "mcp_registry", "config": {"server_refs": [{"name": "github"}]}},
      {"type": "skill_registry", "config": {"project_root": "~/.hive/colonies/demo"}},
      {"type": "input_validation", "order": 100,
       "config": {"schemas": {"webhook": {"ticket_id": "str", "priority": "int"}}}},
      {"type": "rate_limit", "order": 200, "config": {"max_requests_per_minute": 30}},
      {"type": "cost_guard", "order": 300, "config": {"max_cost_per_request": 0.5}}
    ]
  }
}
```

条目形状与上游一致：`type`（必填，未注册 → 启动报错并列出可用类型）、
`order`（可选，覆盖 stage 默认序）、`config`（可选，按 stage 参数展开）。
未识别的 `config` 键被忽略（上游 `**kwargs` 的等价行为）。

内置 stage 与默认序（与上游一致）：

| type | order | 作用 | 参数（config.*） |
| --- | --- | --- | --- |
| `llm_provider` | 10 | 解析 LLM completer | `model`、`mock_mode` |
| `credential_resolver` | 40 | 快照已连接账号 + tool→provider 表 | 无（typed 注入 `Adapter`） |
| `mcp_registry` | 50 | 解析 MCP server 引用并把工具装进 ToolRegistry | `server_refs`、`agent_path` |
| `skill_registry` | 60 | 加载 SkillsManager（目录/默认开关/额外 scope） | `project_root`、`interactive`、`skills_config`、`extra_scope_dirs` |
| `input_validation` | 100 | 按 entry point 的 schema 校验 `input_data` | `schemas` |
| `rate_limit` | 200 | 每 `entry:session` 的 60s 滑动窗口限流 | `max_requests_per_minute` |
| `cost_guard` | 300 | `metadata.estimated_cost` 超预算即拒绝 | `max_cost_per_request` |

各 stage 的行为细节：

- **llm_provider**：已注入 `LLM` 时直接返回（上游 `if self.llm is not None: return`）；
  `mock_mode` 安装 `llm.MockProvider`；否则用 `DefaultLLMResolver` 读 `llm` 段
  （`get_preferred_model` 的 `provider/model` 拼接、`api_base`、`api_key` / `api_keys` 池 /
  `api_key_env_var`）。订阅 token 与凭据库链路属于 server 的 `MainSlotProvider`，
  这里不重复实现（见 §6）。空模型回退 `anthropic/claude-sonnet-5-5`。
- **credential_resolver**：`CredentialAdapter` 注入或 `credentials.DefaultAdapter(CatalogNamedSpecs())`；
  失败只记日志、不阻断启动（上游的 non-fatal 语义）；输出 `AccountsData` 与
  `ToolProviderMap`（生成表）。
- **mcp_registry**：解析顺序与上游一致——`server_refs` → `agent_path/mcp_servers.json`
  → `Profile="all"` 回退 → 最后确保 `files-tools`（harness 组，进程内加载）。
  支持注入 `ToolRegistry` 与 `Resolver`；默认 `tools.NewToolRegistry()` 与
  `mcpregistry.NewAtHome(home)`。
- **skill_registry**：`skills.SkillsManager` 加载；`skills_config` 复用
  `SkillsConfigFromAgentVars`（`_all` 主开关语义与上游 `is_default_enabled` 相同：
  主开关优先于单项）；`extra_scope_dirs` → `skills.ExtraScope{Directory, Label, Priority}`。
- **input_validation**：`schemas` 形如 `{entry_point_id: {key: type_name}}`，类型名取
  JSON 能表达的一组：`str` / `int` / `float` / `bool` / `list` / `dict` / `none`（`NoneType`）。
  保留 Python `isinstance` 的两处怪癖：`bool` 满足 `int`，`int` 不满足 `float`；
  未知类型名是错误而不是放行（上游会 `TypeError`）。缺失键 → `Missing required input key: '<k>'`；
  类型不符 → `Input key '<k>' has type <got>, expected <want>`。
- **rate_limit**：key = `<entry_point_id>:<session_id>`（无 session_state 用 `default`，
  显式 `null` 按 Python `str(None)` 记为 `None`）；60s 窗口内达到上限即拒绝，拒绝的请求不计数。
- **cost_guard**：`metadata` 无 `estimated_cost`（或为 null）直接放行，等于「未配置即通过」；
  超预算按上游 `$%.4f` 格式拒绝；非数值估计是 stage 错误（上游比较会 `TypeError`）。

## 3. 默认行为与启动语义

| 场景 | 行为 |
| --- | --- |
| 未配置 `pipeline` / `pipeline.stages` 缺失或为 `[]` | 空管线 no-op；`Trigger` 直接 spawn（上游 `PipelineRunner([])`） |
| 配置了未注册的 `type` | **启动失败**：`NewColonyRuntime` 记录错误，`Start`（以及 `Trigger`）返回 `unknown pipeline stage type "..." . Available: ...`（上游构造期 `KeyError`） |
| `pipeline.stages` 不是列表 | 启动失败（`pipeline.stages must be a list`） |
| stage `Initialize` 失败 | `Start` 失败；不会静默跳过 |
| `RuntimeOptions.PipelineStages` 非空 | 注入 stage 优先，完全不读配置（上游 `pipeline_stages=[...]`） |
| `Trigger` 时管线拒绝 | 返回 `*pipeline.RejectedError`，不 spawn worker |
| `Trigger` 带 `IdempotencyKey` | 先查缓存（TTL 300s、上限 10000 键、插入序淘汰）再跑管线；命中直接返回原 worker id |
| 生产中手动/定时触发 | **不调用 `Trigger`**：`handleRunTrigger` / `StartTriggerTimer` 把 trigger event 注入 queen 节点（§7） |

`Trigger` 的其余语义与上游 `ColonyRuntime.trigger` 对齐：未启动返回
`ColonyRuntime is not running`；任务文本取 `input_data["task"]`，没有则用 Python 风格的
`json.dumps(input_data)`（复用 `pyJSONDumps`）；spawn 成功后回填幂等缓存。

## 4. 与上游逐项对照

| 上游（Python） | Go 实现 | 状态 |
| --- | --- | --- |
| `pipeline/stage.py::PipelineContext` / `PipelineResult` / `PipelineStage` / `PipelineRejectedError` | `internal/pipeline/stage.go` 的 `Context` / `Result` / `Stage` / `RejectedError` | 等价（含默认 `order=100` 的 `BaseStage`） |
| `pipeline/runner.py::PipelineRunner`（排序、run、run_post、initialize_all、add_stage） | `internal/pipeline/runner.go` | 等价；错误额外分类为 `StageError`（Go 侧增强，不改变控制流） |
| `pipeline/registry.py::register` / `build_stage` / `build_pipeline_from_config` | `internal/pipeline/registry.go` + `builtins.go` | 等价；内置 7 类型在 `init()` 注册；未知 type 报错并列出可用类型 |
| `stages/llm_provider.py` | `internal/pipeline/stage_llm_provider.go` | 机制等价；配置解析为 Go 子集（订阅/凭据库链路由 server 负责） |
| `stages/credential_resolver.py` | `internal/pipeline/stage_credential_resolver.go` | 账号快照 + tool→provider 表等价；`accounts_prompt` 未生成（见 §6） |
| `stages/mcp_registry.py` | `internal/pipeline/stage_mcp_registry.go` | 解析顺序与 `files-tools` 兜底等价；复用 `mcpregistry.ResolveForAgent` + `ToolRegistry.LoadRegistryServers` |
| `stages/skill_registry.py` | `internal/pipeline/stage_skill_registry.go` | 加载等价；上游的 `start_watching` 未移植（Go 侧按需 `Reload`） |
| `stages/input_validation.py` | `internal/pipeline/stage_input_validation.go` | 等价（含 isinstance 怪癖；类型名以字符串表达） |
| `stages/rate_limit.py` | `internal/pipeline/stage_rate_limit.go` | 等价（滑动窗口、桶 key、拒绝消息） |
| `stages/cost_guard.py` | `internal/pipeline/stage_cost_guard.go` | 等价（无估计放行、预算判断、消息格式） |
| `host/colony_runtime.py::_load_pipeline_from_config` / `__init__` 注入 | `internal/colony/trigger.go::loadPipelineFromConfig` + `NewColonyRuntime` | 等价 |
| `host/colony_runtime.py::trigger` | `internal/colony/trigger.go::Trigger` | **partial**：机制等价（管线 → task 提取 → spawn → 幂等回填）；生产可达性 **out_of_scope**——上游 `trigger` 仅由 `trigger_and_wait`/未注册的 colony timer 调用，server 走 queen 注入（§7） |
| `host/colony_runtime.py::trigger_and_wait` | 未移植（无 `TriggerAndWait`） | out_of_scope：上游无生产调用方，仅 embedding API；Go 未新增无调用方的 API（§7） |
| `host/colony_runtime.py::initialize_all` + `_apply_pipeline_results` | `initializePipeline` + `applyPipelineOutputs`（`internal/colony/trigger.go`） | 子集：见 §6 |
| `pipeline/stages` 作为 `configuration.json` 键 | `pipeline.stages`（`contracts/config-keys.json`，`aligned`） | 两侧读取点均有契约锚点与测试；`aligned` 只指读取/解析面，不代表 `Trigger` 的生产可达性（§7） |

## 5. 测试与门禁

```bash
cd go_hive/go-hive
go build ./...
go test -count=1 ./internal/pipeline/ ./internal/colony/ ./internal/server/
go test -count=1 ./internal/pipeline/ -run 'TestTriggerPipelineHasNoProductionCaller|TestPipelineReachabilityStatusDocumented'
bash ../scripts/check_parity.sh .
bash ../scripts/check_parity_targets.sh
python3 ../scripts/check_env_config_contract.py --strict
python3 ../scripts/gen_configuration_html.py --check
make arch-check
```

新测试全部带 `// Parity-exempt:` 注解（上游没有 pipeline 单测，行为按
`core/framework/pipeline/`/`colony_runtime.py` 源码钉住）；生产文件的
`// Parity:` 注解指向上游真实符号。

## 6. Go 侧应用范围与未实现项

`applyPipelineOutputs` 只应用 Go runtime 能消费的子集（上游 `_apply_pipeline_results`
还会替换工具面与账号提示词）：

- 应用：`LLMProviderStage.LLM`（runtime 没有 LLM 时）、`CredentialResolverStage.ToolProviderMap`
  （runtime 没有映射时）、`SkillRegistryStage.SkillsManager`（覆盖 skills provider）。
- 未应用：`McpRegistryStage.ToolRegistry` —— Go 的 worker 工具面在 host 装配阶段
  （`internal/cli`）已经成型，pipeline 不替换它；`accounts_prompt` 未生成，
  Go 的 queens 凭据块由 `queen.CredentialsSummaryBlock` 组装。
- 未移植：`skill_registry` 的上游文件监听（`start_watching`）；`TriggerAndWait` /
  `register_trigger` + colony `_start_timers`/`_timer_loop` 定时自动 spawn 的旧路径
  （Go 的定时 trigger 走 queen 事件注入）。这些在上游也没有生产调用方，见 §7；不新增
  无生产调用方的 API。
- 说明：`llm_provider` 的默认解析是 `get_preferred_model` + `get_api_keys` 的 Go 子集；
  Antigravity 订阅、凭据库与 provider 环境变量兜底在 `internal/server` 的
  `MainSlotProvider` 中，未在 pipeline 内重复。

## 7. 生产可达性（out_of_scope）

**结论：`internal/pipeline` 的逐请求中间件（`Runner.Run`）与 `ColonyRuntime.Trigger` 没有生产调用方；上游也没有。**
这是 API-surface parity，不是 server 的触发器路径。把 `Trigger` 接进 `handleRunTrigger` 会改变语义
（spawn worker vs. 向 queen 注入 trigger event），因此不接线。

**上游调用图（本检出，2026-11-26）**

- `ColonyRuntime.trigger`：`core/framework/host/colony_runtime.py:2376`。
- 仅有的两处调用：`trigger_and_wait` 内部 `core/framework/host/colony_runtime.py:2425`/`:2432`，
  以及 colony 定时器 `_timer_loop` 的 `core/framework/host/colony_runtime.py:2904`。
- `_timer_loop` 只由 `_start_timers`（`:2860`–`:2875`）为 `self._triggers` 里的 timer 启动；
  该字典只由 `register_trigger`（`:2823`）填充，而 `register_trigger` 在整个检出中 **0 个调用方**
  （`grep -rn "register_trigger" --include=*.py .` 仅命中定义与 `unregister_trigger`）。
  因此 colony 定时器在生产中不会启动，`trigger` 也不会被定时器调用。
- 生产 session 的 colony 由 `SessionManager` 构造并显式传 `pipeline_stages: []`
  （`core/framework/server/session_manager.py:1777`，注释：queen pipeline runs in
  queen_orchestrator, not here），所以生产 colony runtime 的管线为空。

**上游生产触发器路径（不经过 `ColonyRuntime.trigger`）**

- 手动触发：`handle_run_trigger`（`core/framework/server/routes_sessions.py:1527`）→
  `queen_node.inject_trigger`（`:1557`）。
- 定时触发：`_start_trigger_timer`（`core/framework/tools/queen_lifecycle_tools.py:810`）→
  `queen_node.inject_trigger`（`:886`）。
- 图时代：`AgentHost.trigger`（`core/framework/host/agent_host.py:918`）确实跑 pipeline，
  `AgentLoader.trigger`（`core/framework/loader/agent_loader.py:1894`）是它的包装；但
  `routes_sessions.py:1091` 明确 “legacy graph-runtime entry points were removed with
  colony_runtime”，且生产 server 不构造 `AgentLoader`（`core/framework/server` 内无
  `AgentLoader(` 调用）。因此 pipeline 的 trigger 路径只属于图时代/嵌入 API，不属于
  Go 支持的 colony server 路径。

**Go 侧对应**

- 生产触发器与上游同形：`handleRunTrigger`（`internal/server/routes_session_triggers.go:367`）→
  `injectTrigger`（`:398`）；`StartTriggerTimer`（`internal/server/trigger_runtime.go:512`）→
  `injectTrigger`（`:617`）。
- `internal/host` 只暴露 `Spawn`/`Run`（`internal/host/host.go:211`/`:227`），没有 `Trigger`；
  `internal/cli/loader_colony_host.go:69` 构造 Host 时也没有 trigger 接线。
- `ColonyRuntime.Trigger`（`internal/colony/trigger.go:61`）只在 `internal/colony/trigger_test.go`
  等测试中被调用；全仓非测试 `.Trigger(` 调用为 0。
- Go 的 `serve --colony` 在 `NewColonyRuntime` 读取 `configuration.json` 的 `pipeline.stages`
  （`internal/colony/runtime.go:348`）并在 `Start` 初始化/应用（`:640`）；这是包级 helper/启动面，
  不是 `Trigger` 的逐请求路径。上游 session colony 显式传空管线（`session_manager.py:1777`），
  这个启动面差异登记在 `docs/alignment-differences.md` A40。
- 守卫测试：`internal/pipeline/reachability_test.go` 在出现生产 `.Trigger(...)` 调用、
  `triggerPipeline.Run` 外溢或文档删除 §7 标记时失败。

**为什么不接线**

1. 上游没有生产调用方，接线会发明一条上游不存在的路径。
2. `handleRunTrigger` 的既有语义是“把 trigger event 注入 queen 节点”
   （`internal/server/routes_session_triggers.go:392`–`:398`）；改成 `ColonyRuntime.Trigger`
   会 spawn 一个 worker，改变 session/queen 行为。
3. 没有安全的 Go producer：`internal/host` 没有 timer/API 触发面；新增 colony timer 需要先移植
   `register_trigger`/`_start_timers`，属于新功能而非 parity 接线。
