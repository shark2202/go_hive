# Trigger pipeline（配置驱动的 trigger 中间件）

> 上游参考：`core/framework/pipeline/`（`stage.py` / `runner.py` / `registry.py` / `stages/*.py`）
> Go 实现：`go_hive/go-hive/internal/pipeline/` + `go_hive/go-hive/internal/colony/trigger.go`
> 配置键：`configuration.json` → `pipeline.stages`（`contracts/config-keys.json` 第 135 键，状态 `aligned`）

## 1. 机制

`pipeline` 是 **trigger 入口的中间件**：colony runtime 每收到一次 `Trigger`，在 spawn worker
之前把请求依次交给按 `order` 升序排列的 stage。机制本身是 opt-in 的——没有配置
`pipeline.stages` 就得到一个空管线，`Trigger` 直接 spawn，与上游 `PipelineRunner([])`
的行为一致。

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
| `host/colony_runtime.py::trigger` | `internal/colony/trigger.go::Trigger` | 等价（管线 → task 提取 → spawn → 幂等回填） |
| `host/colony_runtime.py::initialize_all` + `_apply_pipeline_results` | `initializePipeline` + `applyPipelineOutputs`（`internal/colony/trigger.go`） | 子集：见 §6 |
| `pipeline/stages` 作为 `configuration.json` 键 | `pipeline.stages`（`contracts/config-keys.json`，`aligned`） | 两侧读取点均有契约锚点与测试 |

## 5. 测试与门禁

```bash
cd go_hive/go-hive
go build ./...
go test -count=1 ./internal/pipeline/ ./internal/colony/ ./internal/server/
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
  定时 trigger 自动 spawn 的旧路径（Go 的定时 trigger 走 queen 事件注入）。
- 说明：`llm_provider` 的默认解析是 `get_preferred_model` + `get_api_keys` 的 Go 子集；
  Antigravity 订阅、凭据库与 provider 环境变量兜底在 `internal/server` 的
  `MainSlotProvider` 中，未在 pipeline 内重复。
