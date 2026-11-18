# 12 · 设计

> 本文描述 Go 移植的模块边界与关键决策。已实现的包以当前代码为准；尚未实现的运行时（AgentLoop 与 Colony 的编排闭环、服务端）标注为「目标设计」，用于对齐接口，不代表已经落地。

## 1. 设计原则

1. **外部行为优先于内部惯用法。** 文件格式、错误分支、提示词文本与上游一致；Go 内部可以更严格、更显式。
2. **边界处宽松，核心处严格。** 读外部数据（JSON、SQLite、SKILL.md、.enc）一律容忍损坏与缺字段；写出去的数据必须满足 schema。
3. **显式优于隐式。** Python 的 duck typing 用 Go 接口替代；dict 用带字段的结构体替代；可选能力用接口断言替代 `hasattr`。
4. **单写者。** 每个文件/数据库只有一个写入路径，配合原子 rename 或 SQLite 单连接，避免跨进程锁。
5. **可离线、可交叉编译。** 任何设计如果引入 cgo 或运行时下载，即视为不可接受。

## 2. 分层

```
                    cmd/hive（CLI，已实现）
                              │
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
   internal/server      internal/colony      internal/agentloop
   （HTTP 配置路由已接线）  （Playbook，待实现）  （唯一执行原语，已实现）
        │                     │                     │
        └──────────┬──────────┴──────────┬──────────┘
                   │                     │
        ┌──────────▼─────────┐  ┌────────▼────────┐
        │ 领域服务            │  │ 运行时设施       │
        │ skills / memory    │  │ triggers        │
        │ credentials        │  │ eventbus        │
        │ tracker / session  │  │ tools / llm     │
        └──────────┬─────────┘  └────────┬────────┘
                   │                     │
                   └──────────┬──────────┘
                              │
                  ┌───────────▼────────────┐
                  │ 基础设施                │
                  │ atomicfs / hivehome    │
                  │ version / platform     │
                  └────────────────────────┘
```

依赖方向**只向下**：领域服务不 import 服务端或 AgentLoop；基础设施不 import 领域服务。`hivehome` 与 `atomicfs` 是叶子包。

## 3. 模块边界

| 包 | 职责 | 明确不负责 |
| --- | --- | --- |
| `internal/atomicfs` | 原子写、JSON 读写、JSONL 追加、文件复制 | 业务 schema 校验 |
| `internal/hivehome` | 解析 `HIVE_HOME` 与 `~/.hive` 布局，派生 Colony 路径 | 创建业务目录内容 |
| `internal/session` | `state.json`、对话 part、cursor、partial、检查点 | 决定何时推进 cursor（属于 AgentLoop） |
| `internal/credentials` | Fernet、存储后端、Provider、OAuth2、启动引导 | 具体集成的鉴权流程 |
| `internal/skills` | 发现、解析、信任、覆写、热重载、写盘、catalog | 技能的实际执行 |
| `internal/memory` | 记忆文件扫描、召回选择、反思、时间线抽取 | 何时触发反思（属于 AgentLoop/Queen） |
| `internal/tracker` | SQLite schema、SQL 门禁、绑定解析、四个工具 | Worker 调度 |
| `internal/llm` | 补全类型（`Completer`）、模型目录/端点/密钥池、流事件，LiteLLM 的可离线纯逻辑层（模型规整、缓存断点、重试退避、成本核算、JSON 修复、工具格式转换），请求塑形（`parallel_tool_calls` 锁存、OpenRouter tool-compat 消息、`ExtraLLMKWargs`），非流式完成路径（`complete` 的请求组装、429/空响应重试、响应归一化），以及 `AnthropicProvider` 向后兼容包装（密钥解析、委托、类型桥接） | 真实 HTTP/SSE 传输（待开始） |
| `internal/mcpregistry` | MCP 服务器安装、配置解析、健康检查、`mcp2cli` 桥 | MCP 传输协议（属于 `internal/tools`） |
| `internal/cli` | 根入口命令树（`serve`/`open`/`queen`/`colony`/`session`/`chat`）、12 个 `skill` 子命令，以及以 `Passthrough` 模式整段透传的 `mcp`（14 个子命令由 `internal/mcpregistry` 自行解析，避免通用解析器吞掉 `--json`、`--args/--env`、`add local -- <cmd>` 尾部）；argparse 兼容的 `parsedArgs`、表格/JSON 输出、前端构建、Windows `npm.cmd` 分支 | 各子系统的业务逻辑（委托给 `skills`/`mcpregistry`/`server`）；凭据与 maintenance 根子命令尚未注册 |
| `internal/queen` | 默认 queen 身份档案（`go:embed` 13 个 YAML）、`catalog.json` 读写、标题继承与移除标记、目录列表辅助、角色默认工具/技能查询（`DefaultToolsFor` / `DefaultSkillsFor`）；`profiles_test.go` 10 条对等测试 | queen 会话运行时（待开始）与 sentinel |
| `internal/llmjudge` | LLM Judge 评估：`Provider` 注入、响应 JSON 解析（裸 JSON / Markdown 围栏）、惰性 Anthropic 客户端、OpenAI 回退 | provider 的传输实现（属于 `internal/llm`） |
| `internal/ratelimit` | 社交平台限流：SQLite `actions` 计数、env/配置/默认三层限额解析、最短间隔与日/周窗判定、停用建议 | 决定重试或停止（属于调用方/调度器） |
| `internal/tools` | 工具注册表、执行器、参数转换、MCP 客户端（stdio/http/unix/SSE 传输、重试、内容转换）、进程级 MCP 连接池（按服务器名复用、refcount、并发 latch、健康检查、重连、清理） | 具体集成工具实现（104 个，待开始） |
| `internal/server` | 服务端辅助设施：fork-compaction 状态标记（`compaction_status.json`）、worker 事件过滤/升级/读端点、global-db 数据路由决策（lead_id 派生 / insert 计划 / changes 游标 / 错误信封）、会话目录解析（DM 优先惰性遍历 / colony 归属 / resume 绑定）、配置类 HTTP 路由全量（`RegisterConfigRoutes`：models / sentinel / features / llm-sections / llm / provider-library / rate-limits / profile+avatar / external-skills，共 10 组 20 条，严格 JSON 解析与配置读改写串行化） | 其余 HTTP 路由（26 个路由模块）、SSE、SessionManager、服务装配（待开始） |

## 4. 关键 Go 化决策

### 4.1 接口替代 duck typing

Python 用 `hasattr(storage, "load_all_for_provider")` 探测可选能力。Go 侧拆成基础接口 + 可选接口：

```go
type CredentialStorage interface {
    Save(*CredentialObject) error
    Load(string) (*CredentialObject, error)
    Delete(string) (bool, error)
    ListAll() ([]string, error)
    Exists(string) (bool, error)
}

type ProviderAccountStorage interface {   // 可选能力
    LoadAllForProvider(string) ([]*CredentialObject, error)
}
type AliasStorage interface {
    LoadByAlias(provider, alias string) (*CredentialObject, error)
}
```

调用方用 `if s, ok := storage.(ProviderAccountStorage); ok { ... }`，与 `hasattr` 语义一致，但编译期可查。技能热重载同理：`SkillReloader` 只要求 `ReloadSkills() (map[string]any, error)`，`ManagerReloadAdapter` 负责加锁。

### 4.2 有序结构替代 dict

Python 3.7+ 的 dict 保序，凭据 JSON 的 key 顺序会被索引和测试观察到。`credentials.KeyMap` 用 `names []string` + `values map[string]*CredentialKey` 实现插入有序；序列化时按 `names` 输出。会话 `State`、`Cursor`、`Checkpoint` 全部是显式结构体，字段顺序即 JSON 顺序。

### 4.3 宽松 JSON 读取

`atomicfs.ReadJSON` 对缺失字段保持零值，不因未知字段报错（`json.Unmarshal` 默认行为）。在此之上：

- `index.json` 解析失败 → 视为空索引并重建，而不是让构造失败；
- `cursor.json` 缺失 → 返回零值 `Cursor{}`；
- 对话 part 损坏 → 跳过该 part，继续读后续；
- 技能覆写/安装请求 JSON 损坏 → 按空处理。

这与上游「永不因缓存/索引损坏而丢主数据」的策略一致；`.enc` 与 SQLite 才是权威数据。

### 4.4 原子写

`atomicfs.WriteFile` 的流程：同目录创建唯一临时文件 `.<name>.*.tmp` → `Write` → `Sync` → `Close` → `Chmod` → `Rename`。`WriteJSON` 额外做 `indent=2` 并补尾随换行，保证与 Python 的 `json.dump(..., indent=2)` 输出可比对。

会话目录创建顺序、part 序号（`%010d.json`）、checkpoint 文件名都刻意与 Python 相同，使两套进程可以读同一棵树。

### 4.5 Python 兼容助手

技能与记忆模块里有三处必须逐字兼容的地方：

| 助手 | 作用 |
| --- | --- |
| `pyStr` | Python `str()` 的布尔/None/浮点表示（`True`/`False`/`None`），用于 frontmatter 与提示词 |
| `pyFloatRepr` | 避免 Go 把 `1.0` 输出成 `1`，保持 Python `repr` 风格 |
| `globMatch` | Python `fnmatch` 语义（`*`、`?`、`[seq]`），用于信任规则匹配 |

### 4.6 错误即值，文案即契约

- `tracker` 的 `DenylistError` 携带被拒关键字/函数，调用方可直接呈现给模型；
- `ErrNoBinding` 的提示文本与上游一致，指导模型改用 `colony_id` 或 `tracker_db_path`；
- 凭据解密失败包装为 `CredentialDecryptionError`，区分「不存在」与「解不开」。

### 4.7 SQLite

`modernc.org/sqlite` 纯 Go 实现，驱动名 `sqlite`。连接池 `MaxOpenConns(1)`：Tracker 是单写者场景，串行化换来可预测的 WAL 行为与事务顺序。PRAGMA 在每次打开时设置：`journal_mode=WAL`、`synchronous=NORMAL`、`foreign_keys=ON`、`busy_timeout=5000`。

### 4.8 内置技能包

`//go:embed all:_default_skills all:_preset_skills` 把 12 个 default + 8 个 preset 技能打进二进制；首次使用时物化到进程级临时目录，再走普通目录扫描。这样安装包不依赖源码树相对路径，同时复用同一套解析器。

### 4.9 fork-compaction 状态标记

「Create Colony」叉出 Queen 会话时，继承的 DM 记录交给一次 LLM 调用压缩，可能超过默认 60s 工具超时，因此压缩在后台跑，状态写在 `<queen_dir>/compaction_status.json`，供后续冷加载先等待再读对话：

| 状态 | 字段 |
| --- | --- |
| `in_progress` | `started_at` |
| `done` | `completed_at`、`messages_compacted`、`summary_chars` |
| `failed` | `completed_at`、`error`（前 500 个字符，按 rune 截断以对齐 Python `str[:500]`） |

- 每次写入都 fail-soft：失败只记日志，绝不让压缩拖垮调用方；缺失或损坏的文件读作「无待处理压缩」。
- `AwaitCompletion` 轮询到状态离开 `in_progress` 即返回；超时返回最后一次观测（仍是 `in_progress`），调用方回落到原始记录。Go 版额外接收 `context.Context`，服务端关闭时可中断轮询。
- 进程崩溃会留下孤儿 `in_progress`；`SweepStaleInProgress` 在服务启动时把它们改写为 `failed`（“server restarted while compaction was in progress”），避免每次冷加载都等满超时。
- 时间戳用 RFC3339Nano（项目统一的 UTC 规则），写入走 `atomicfs`（顺带补建父目录）；两处均对共享测试不可观测。

Parity: `framework/server/compaction_status.py` / `tests/test_compaction_sweep.py`。

### 4.10 每次 spawn 的 LoopConfig 覆盖

Queen 可以在 spawn worker 时传 `loop_config_overrides`，这是唯一能改动 worker 预算的入口，因此 `colony.BuildWorkerLoopConfig` 是收敛点（对应上游 `framework/host/colony_runtime.py::_build_worker_loop_config`）：

- **基线**来自 worker profile（`DefaultWorkerLoopConfig`：3 次工作迭代 + 1 次宽限、30 次软工具预算、框架锁定的 3 倍硬上限、200 次累计上限），而不是裸 `LoopConfig` 默认值——避免 live spawn 路径与 `worker_definition.py` 漂移。`max_context_tokens` 保持 `LoopConfig` 自身的 180,000。
- **白名单只有 5 项**：`max_iterations`、`grace_iterations`、`tool_call_budget`、`tool_call_lifetime_budget`、`max_context_tokens`。判断算法在 `/turn`（judge cadence）、停顿检测、压缩缓冲、`tool_call_hard_multiple` 上都不对 Queen 开放。
- **未知键静默丢弃**（拼写错误不该杀死 spawn），**越界或非整数返回 `*LoopOverrideError`**。错误文案逐字对齐上游：`loop_config override 'k' must be int, got str` / `loop_config override 'k'=0 out of range [1, 1000]`，便于运维日志跨语言对照。
- **0 是哨兵而非缺省**：`tool_call_budget=0` 表示无软/硬限制，`tool_call_lifetime_budget=0` 表示关闭累计上限，`grace_iterations=0` 表示放弃宽限轮；三者都通过边界检查。
- **两个 Python `isinstance(value, int)` 怪癖被保留**：`bool` 是 `int` 子类（`true` → 1）；`json` 把 `2.0` 解码为 float 因此被拒。Go 侧因此要求调用方用 `json.Decoder.UseNumber()`，并以 `json.Number`（无 `.`/`e`/`E`）作为整数载体；这条契约由一条 `Parity-exempt` 测试钉住。
- 为承载这些字段，`agentloop.LoopConfig` 增补 `GraceIterations` / `ToolCallBudget` / `ToolCallHardMultiple` / `ToolCallLifetimeBudget` / `JudgeEveryNTurns`。`LoopConfig` 只经 `DefaultLoopConfig()` 构造（`internal/colony/runtime.go` 的两处 spawn 路径），因此这是纯增字段、无行为变化。

Parity: `framework/host/colony_runtime.py::_build_worker_loop_config` / `tests/test_loop_config_overrides.py`。

### 4.11 session 自动标题

会话没有标题时，Queen 回合结束（`stop_reason` 不是 `tool_use`/`tool_calls`）后由辅助模型命名，结果写回会话的 `meta.json`：

| 字段 | 含义 |
| --- | --- |
| `title` | 清理后的标题（≤60 个码点，含省略号） |
| `title_source` | `auto` 或 `user`；`user` 标题永不被自动覆盖 |

- **清理规则**逐条对齐上游 `clean_title`：剥离 `<think>` / `<system-reminder>` 块 → 取第一条非空行 → 空白折叠为单个空格 → 剥两端引号/反引号/星号/井号/中日韩括号 → 剥右端句读 → `strip` → 剥行首 `title:`/`title：` → 超长截为 59 码点加 `…`。上游正则用了反向引用 `</\1>`，Go 的 RE2 不支持，因此写成两条具名分支的择一匹配；RE2 的最左最先匹配语义与 Python `re` 的扫描顺序一致，嵌套/交叉标签的结果相同。
- **Python 语义助手**：`str.strip`/`\s`/`splitlines` 都是 Unicode 感知的，Go 的 ASCII `\s`、`strings.Fields`、`Split("\n")` 会静默分歧。`session_titles.go` 因此自带 `pySpace`（0x09–0x0d、0x1c–0x1f、0x20、0x85、Zs/Zl/Zp）、`pySplitLines`（含 `\v`/`\f`/0x1c–0x1e/0x85/U+2028/U+2029，`\r\n` 视作一个边界）与 `collapsePythonWhitespace`；这些行为由一条 `Parity-exempt` 测试钉住，因为上游参数化用例没有覆盖它们。
- **发布即等待**：`eventbus.Publish` 会等待处理器返回，Python 的 `publish` 则把命名丢进 `asyncio.create_task`。Go 在处理器里起 goroutine（`running` 防重入、`WaitGroup` 计数），并把 `Wait()` / `Close()` 暴露给测试作为确定性检查点；否则 LLM 调用会阻塞发布方。
- **失败可重试**：命名失败只记日志、不落标题，下个回合继续尝试；写入前会重读一次标题，用户在此期间命名则以用户为准。
- `TitleTokenBudget` 目前是 `func() int { return 8192 }` 占位，对应 `get_aux_max_tokens()`（`llm.aux_max_tokens` 或 `min(DEFAULT_MAX_TOKENS, get_max_tokens())`），待 P2 配置包落地后接上。命名成功后广播 `session_title_updated`（`session_id` + `title`）。
- 上游的标题 PATCH 路由（`test_title_route_sets_and_clears_a_users_title`）属于 aiohttp 路由表，随服务端路由车道落地；`WriteTitle` 的设/清语义已由对等测试先钉住。

Parity: `framework/server/session_titles.py` / `tests/test_session_titles.py`。

### 4.12 工具执行：专属池、上下文传播与每工具超时预算

工具执行的入口是 `internal/tools.ExecuteTool`（对应上游 `tool_result_handler.py` 的执行半边）与 `agentloop.AgentLoop.callExecutor`（决定预算并调用执行器）。

- **专属执行池**：工具运行在 `hive-tool*` 线程/池上（`SharedToolPool()`，可用 `SetToolPool` 替换），而不是默认池；池满时只排队工具调用，不阻塞 HTTP/事件等其它工作。池名经 `context.Context` 的私有键传入执行器，`ToolPoolNameFromContext` 可读取，便于测试与诊断。
- **执行上下文**：上游用模块级 `ContextVar` 暴露 `get_execution_context()`；Go 用进程级栈（`SetExecutionContext` / `ResetExecutionContext` / `GetExecutionContext`），在 `ExecuteTool` 进入执行器前可见。
- **超时预算解析**（`AgentLoop.ResolveToolTimeout`，对应 `_resolve_tool_timeout`）：按工具名前缀在 `LoopConfig.ToolTimeoutOverrides` 中匹配，**最长前缀优先**；无匹配则用 `ToolCallTimeoutSeconds`（默认 60s），默认覆盖表为 `{"browser_": 180}`。`ToolTimeoutOverrides == nil` 表示使用上游默认表；空非 nil map 表示关闭覆盖；`ToolCallTimeoutSeconds <= 0` 视为未设置（取 60，呼应 Go 零值惯例）。
- **预算执行**：`callExecutor` 用 goroutine + `select`（结果 / `ctx.Done()` / 定时器）执行调用；超时即放弃并返回 `IsError` 结果，文案含 `timed out after Ns`、`DO NOT kill` 与「不是应用或浏览器崩溃」的提示。Go 执行器同步且没有 liveness probe，因此超时**不做 kill**；上游「探测存活后决定是否 kill」的完整策略由 `internal/tools` 的 `timeoutResult` 在 `ExecuteTool` 层实现（alive/resetting 不 kill，dead/unknown/无探测能力回退 legacy kill）。
- **不变量**：所有 agent 侧预算必须低于 MCP 单次调用结果上限 `tools.MCPCallResultTimeout()`（默认 240s，可由 `HIVE_MCP_CALL_RESULT_TIMEOUT` 调整）；该不变量由 `TestLoopConfigPrefixTimeoutResolution` 断言，防止预算比传输层还长而永远等不到结果。

Parity: `framework/agent_loop/internals/types.py`（`tool_call_timeout_seconds` / `tool_timeout_overrides`）、`AgentLoop._resolve_tool_timeout`、`tests/test_tool_timeout_probe_policy.py`。

### 4.13 工具分层默认值：类别表与 eager keep-set

`internal/tools/tier_defaults.go` 移植 `framework/agents/queen/queen_tools_defaults.py` 的静态部分，负责「哪些工具默认出现在 prompt 里」：

- **类别表**：`ToolCategories` 把类别名映射到具体工具名或 `@server:NAME` 简写；Go map 不保序，另用 `categoryOrder` 保留声明顺序，`ListCategoryNames` 返回副本。`ResolveCategoryTools` 展开简写（catalog 为 nil 时静默丢弃，与上游一致）、按首次出现去重。
- **两个 keep-set**：`AlwaysEnabledCategories`（queen 的全局 eager 层：`file_ops` / `terminal_basic` / `context_awareness` / `browser_core`）与 `WorkerAlwaysEnabledCategories`（worker：`browser_core` / `terminal_core` / `files_core` / `context_core` / `web_core`）。展开后分别是 13 与 9 个具体工具名。
- **结构不变量**（`tier_invariants_test.go`，逐条对应 `tests/test_toolset_tier_invariants.py`）：browser 的 core/extended 只在共享锚点 `browser_setup` 上重叠，terminal 的 core/extended 不相交，两者并集分别等于 legacy 类别；keep-set 引用的类别必须存在；11 个 `MEASURED_COLD` 工具（browser 冷尾、`search_messages`、`terminal_rg`/`glob`、`pdf_read`）不得回到 eager 层；worker keep-set 不超过 24 个名字且保留实测热核。测试侧常量与上游同样定义在测试文件内，类别表本身只由生产代码维护。
- **尚未移植**：上游 `configured_always_enabled_categories()` / `configured_worker_always_enabled_categories()` 会读取 `configuration.json` 的 `queen_tools` / `worker_tools` 覆盖硬编码 keep-set；Go 侧目前只实现硬编码默认，配置覆盖留给 queen 配置 lane。角色默认类别（`QUEEN_DEFAULT_CATEGORIES`）同理依赖 queen profiles，尚未落地。

Parity: `framework/agents/queen/queen_tools_defaults.py`、`tests/test_toolset_tier_invariants.py`。

### 4.14 LLM provider 纯逻辑层

`internal/llm/llm.go` 只定义 provider 中立的类型（`ChatMessage` / `ToolDef` / `CompletionRequest` / `Completion` / `Completer`）；`provider.go` 把上游 `framework/llm/litellm.py` 里**不需要网络**的 helper 全部落成纯 Go 函数，共 84 条测试逐条镜像 `core/tests/test_litellm_provider.py`。这一层刻意不持有连接，因此可以用 `llm.FakeCompleter` 在任何测试中替代真实 provider。

已锁定的行为：

- **模型规整**：`NewProvider` 复刻构造函数语义——默认 `gpt-4o-mini`，Ollama 模型补 `ollama_chat/` 前缀（`EnsureOllamaChatPrefix`），未显式给 `api_base` 时按 `DefaultAPIBaseForModel` 填充（minimax / openrouter / kimi / hive）。`IsMinimaxModel`、`IsLocalModel`、`IsOllamaModel` 是后续路由与成本判断的判定原语。
- **system 分块与缓存**：`ModelSupportsCacheControl` 用**大小写敏感**的前缀白名单判定模型；`BuildSystemMessage` 在支持时把静态 system 与每轮变化的 `SystemDynamicSuffix` 拆成两个 block，只在静态块打 `cache_control`。`HistoryCacheBreakpointIndex` / `ApplyHistoryCacheBreakpoint` 把断点打在**最后一条非 system 消息**上，system-only 或空历史时不打点。`AppendJSONInstruction` 只在 `JSONMode` 下追加指令。
- **重试与退避**：`ParseRetryAfter` 依 `retry-after-ms` → `retry-after`（秒）→ HTTP-date 的顺序解析，结果钳制到 `[0, RateLimitMaxDelay]`；`ComputeRetryDelay` 在上游有 Retry-After 时用它（长配额窗口原样保留，`< ShortRetryAfterThreshold` 的短延迟加 `[1.0,1.5]` 抖动 + `[0,0.5]` 偏移），否则用 `RateLimitBackoffBase * 2^attempt` 封顶 120 再乘 `[0.75,1.25]` 抖动；代理报告 backpressure 但剥掉 Retry-After 时用 `BackpressureRetryFloor = 1.0`。
- **成本**：`ExtractCacheTokens` 先读 `prompt_tokens_details`，再回退顶层 `cache_creation_input_tokens`。`ExtractCost` 的优先级为 `usage.cost` → hidden `response_cost` → 目录定价；`CostFromCatalogPricing` 按 `model_catalog.json` 的每百万 token 单价计算 `(plain*in + cached*read + creation*write + output*out)/1e6`，其中 `plain = max(input-cached-creation, 0)`。`Completion.CostUSD` 承载结果，使 agent loop → 事件总线 → 前端链路不丢成本。
- **工具与截断修复**：`ToolToOpenAIFormat` 产出 OpenAI 形状；`CloseTruncatedJSONFragment` / `RepairTruncatedToolArguments` 先最多裁掉 256 个字符再平衡引号与括号，`ParseToolCallArguments` 在修复失败时返回带工具名的错误。

三处**有意替换**（与上游不同，均在测试中写明理由）：

| 上游 | Go | 理由 |
| --- | --- | --- |
| `litellm.completion_cost()` / `cost_per_token()` | `CostFromCatalogPricing` + `model_catalog.json` | LiteLLM 运行时依赖无法离线复现；目录价与测试期望逐项核对 |
| Python `None` 组件参与成本计算 | 负数钳 0 | Go 无 `None`，钳制保证不出现负成本 |
| `response=None` 与缺属性两条异常路径 | 单一 `ProviderError` | Go 结构体字段缺失即零值，两条路径合并后仍各有断言 |

### 4.15 LLM 请求塑形与 extra kwargs

`request_shape.go` 承接 `provider.go` 之后剩下的离线可判定逻辑：请求实际发出去之前要对 provider 做的最后几处改写，以及 `framework/config.py::get_llm_extra_kwargs` 的 kwargs 推导。共 5 条测试（10 条 `path::test` 引用 + 2 条 `Parity-exempt` 契约）。

- **Anthropic 判定**：`IsAnthropicModel` 只看小写后的 `anthropic/` 与 `claude-` 前缀；经 OpenRouter 转发的 Anthropic 模型不命中——上游就是这样写的，Go 不"顺手"扩展。
- **`parallel_tool_calls` 与拒绝锁存**：`Provider.RequestParallelToolCalls(hasTools)` 仅在「有工具 + 非 Anthropic + 未被拒绝」时返回 true。`NoteError` 在错误文本含 `parallel_tool_calls` 时把 `refusal` 锁存为 true 并返回一次 true，让调用方原样重试一遍；此后不再询问。锁按 provider 实例持有（`parallelToolsRefusalLatch`，互斥保护），与上游把位放在实例上的语义一致。
- **OpenRouter tool-compat 消息**：`BuildOpenRouterToolCompatMessages` 产出 "persona + 空行 + 兼容指令 + 工具表" 的 system 消息。工具表用 `marshalToolSpecs` 手工拼成 Python `json.dumps` 的形状（`"name"`, `"description"`, `"parameters"` 固定顺序、`, ` / `: ` 分隔）。当被路由的模型支持 `cache_control` 时，静态前缀与动态后缀被拆成两个 block、只在静态块打缓存标记；否则二者拼成一个字符串（liquid 这类子提供商）。随后打滚动历史断点，并丢弃 **content 为空且无 tool_calls** 的 assistant 消息。调用方消息会被浅拷贝，避免滚动断点污染调用方。
- **extra kwargs**：`ExtraLLMKWargs(section, apiKey, codexAccountID)` 按上游顺序判定：claude-code 订阅 → codex 订阅 → ollama `num_ctx` → `extra_body` 透传。两个订阅分支都要求 key 非空，否则落到后续分支（与上游 fall-through 一致）；codex 额外发 `User-Agent: CodexBar`、`store=false`、`allowed_openai_params: ["store"]`（账号 ID 可选）。ollama 的 `num_ctx` 默认 16384——上游注释指明默认 2048 会静默截断 Queen prompt。`extra_body` 做浅拷贝，空对象不输出。

尚未移植（不影响已绑定的纯函数契约）：真实 HTTP `AComplete`、流式 tool-compat/thinking-block 管线的消费侧接线、`hive/` → `anthropic/` 与 `kimi/` → `anthropic/` 的代理模型改写（上游无测试覆盖）。

Parity: `framework/llm/litellm.py`、`framework/config.py`、`tests/test_litellm_provider.py`。

### 4.16 LLM 非流式完成路径

`complete.go` 复刻 `LiteLLMProvider.complete`（`framework/llm/litellm.py:1858`）与 `_completion_with_rate_limit_retry`（同文件 `:1686`）。与前面几层一样，它不直接持有网络：一次请求就是对一个 `CompletionCall` 的调用，退避等待经 `SleepFunc` 注入，因此整个包可以在无网络、无时钟等待的条件下测试。

**请求组装**（顺序与上游一致）：

1. `max_tokens` 缺省时解析为 `ResolveAuxMaxTokens(0)`，即 `DEFAULT_MAX_TOKENS = 8192`（上游是 `min(8192, get_max_tokens())`；Go 侧配置值超过 8192 也回落到 8192）。
2. 显式 `System` 以**裸** `{"role": "system", "content": ...}` 前置——注意这里不用 `BuildSystemMessage`，上游 `complete` 也不分块、不打 `cache_control`（分块只发生在流式路径的 system 构造里）。
3. 调用方消息**浅拷贝**后再 `ApplyHistoryCacheBreakpoint`，所以构建请求不会改写会话切片。这是与上游的一处有意分歧：Python 直接在调用方 dict 上打点。
4. `JSONMode` 为真时 `AppendJSONInstruction` 追加 JSON 指令。
5. kwargs 以 `model` / `messages` / `max_tokens` 起底，叠加 `ExtraKwargs`，再补 `api_key` 与 `api_base`（二者为空时不出现）。也就是说 `ExtraKwargs` 可以覆盖前者，而 `api_key`/`api_base` 由 provider 配置决定。
6. 有工具时逐个 `ToolToOpenAIFormat` 后写入 `tools`；`tool_choice` 按"键不存在才补"的 `setdefault` 语义：Ollama 补 `auto`，Hive 代理（`HiveProxyAuth`）补 `required`，其余不补。`ExtraKwargs` 里已有的 `tool_choice` 因此优先。
7. `ResponseFormat` 非空时原样透传为 `response_format`。

**重试循环**：

- 回复有内容或有工具调用 → 立即返回。
- 空回复：若最后一条非 system 消息是 `assistant`（空是预期），或 `finish_reason == "length"`（重试无用），直接返回；否则重试，上限 `min(retries, EMPTY_RESPONSE_MAX_RETRIES = 3)`，退避用不带抖动的 `ComputeRetryDelay`。
- `429`（由 `ProviderError.StatusCode` 判定，`IsRateLimitError`）：预算 `RATE_LIMIT_MAX_RETRIES = 10`（`CompleteOptions.MaxRetries` 可覆盖，0 表示不重试），退避为 `ComputeRetryDelay(attempt, err, RetryPolicy{Jitter: true})`；预算耗尽返回原错误。
- 其他错误原样返回，不重试。

**响应归一化**：`Content` / `Model`（provider 未回报时回落为配置模型）/ `StopReason`（`finish_reason`）/ `ToolCalls` / `CostUSD`（`ExtractCost` 三级优先级）/ `InputTokens`、`OutputTokens`（`Usage` 非空时）/ `CachedTokens`、`CacheCreationTokens`（`ExtractCacheTokens`）。缓存计数是输入 token 的子集，累加时**不得**重复计入。

Parity: `framework/llm/litellm.py`、`tests/test_litellm_provider.py`（7 条 `test_complete_*` / `test_json_mode_*` 对等；重试循环与 kwargs 组装因上游无独立测试而写 `Parity-exempt`）。

### 4.17 LLM 向后兼容包装 AnthropicProvider

`anthropic.go` 复刻 `framework/llm/anthropic.py`：上游把 `AnthropicProvider` 保留成一层薄包装，只校验密钥、记住模型，然后把 `complete()` / `acomplete()` 连同全部关键字参数转发给内部 `LiteLLMProvider`。Go 侧保持同样的委托形状，只多一层类型桥接。

- **构造**：`NewAnthropicProvider(apiKey, model)`；`model` 为空取 `claude-haiku-4-5-20251001`。Go 构造函数不能 `raise`，因此密钥缺失以 error 返回，文本与上游一致（`Anthropic API key required. Set ANTHROPIC_API_KEY env var or pass api_key.`）。
- **密钥优先级**：显式参数 → 凭据存储 → `ANTHROPIC_API_KEY`。上游 `_get_api_key_from_credential_store` 用的是进程级默认适配器；该包没有默认单例，Go 侧改为包级注入点 `SetAnthropicCredentialLookup(func() (string, bool))`，由宿主在启动时安装。
- **暴露字段**：`APIKey` / `Model` 对应上游 `.api_key` / `.model`，`Provider` 对应 `._provider`，测试可像上游断言 `isinstance(provider, LLMProvider)` 一样断言 `*AnthropicProvider` 满足 `Completer`。
- **委托与桥接**：`Complete` 原样透传 OpenAI 形状的消息与 `CompleteOptions`（`json_mode` 仍走提示词注入而非 `response_format`，与上游行为一致）。`AComplete` 用 `ChatMessagesToMaps` 把类型化消息还原成上游期望的字典：assistant 工具调用写成 `{"id","type":"function","function":{"name","arguments"}}`，纯工具轮 content 为 `nil`，工具结果带 `tool_call_id`；`SystemDynamicSuffix` 按 `LLMProvider.acomplete` 的 fallback 以空行拼到 `System` 后。

测试共 9 条（`anthropic_test.go`）：7 条逐条对应 `core/tests/test_litellm_provider.py` 的 `TestAnthropicProviderBackwardCompatibility`，另 2 条 `Parity-exempt` 覆盖密钥解析优先级与 `AComplete` 类型桥接。

Parity: `framework/llm/anthropic.py`、`tests/test_litellm_provider.py`。

### 4.18 社交平台限流：可共享的 SQLite 账本

`internal/ratelimit` 复刻 `framework/rate_limiter.py`。它存在的理由不是「做个限流器」，而是**跨进程、跨语言共享同一份动作历史**：Python 宿主写入的记录，Go 宿主必须看得见，反之亦然。因此 schema、表名、列名、窗口边界与结果字段全部按上游字面量复刻，不做「Go 风格」重构。

- **存储**：单个 SQLite 文件（默认 `$HIVE_HOME/social_rate_limits.db`），`actions(id, platform, account_id, action_type, target_id, performed_at, session_id)` + 复合索引。`performed_at` 用 Unix 秒（`float64`），与 Python `time.time()` 同量纲。每次操作新开连接、`SetMaxOpenConns(1)`、WAL、`busy_timeout=5000` —— 单写者原则（§1 第 4 条）在这里体现为「不缓存连接、不跨 goroutine 共享」，避免与 Python 侧并发写时出现锁升级。
- **限额表**：8 个 `(platform, action)` 条目硬编码，含 hourly/daily/weekly 的默认值与天花板；weekly 可为空（`nil`），表示该动作没有周限额。`limitOrder` 单独保存声明顺序，因为 Go map 无序而上游 dict 有序，`GetAllLimits` 的输出必须逐行一致。
- **三层解析**（`resolveLimit`）：env `HIVE_RATE_<PLATFORM>_<ACTION>_<WINDOW>` > `configuration.json` 的 `rate_limits` 段 > 默认值。两个**不对称**的上游细节被刻意保留：env 值经 `int()` 后**钳到天花板**（防止误配把账号打到封禁），配置值只做 `max(1, int(v))`、**不钳天花板**（配置被视为可信意图）。env 名用原始拼写大写，查表用小写，二者不能混用。
- **判定顺序**（`Check`）：先最短间隔（同账号同动作最近一次 `performed_at`），不足即 `too_fast` 且**不熔断**；再用 `>= now-window` 计数，hourly(3600s) 不熔断、daily(86400s) 与 weekly(604800s) 熔断。`retry_after_seconds` 统一保留 1 位小数。未检查到的窗口字段保持 `nil`（对应上游 `None`），因此 `CheckResult` 的计数字段全是指针 —— 这是「零值不等于未定义」的必要代价。
- **可测试性**：配置源是包级函数变量（`SetConfigOverrides`），`nil` 复位到真实 `configuration.json`；测试 `init()` 先置空，避免开发机 `~/.hive` 的 `rate_limits` 影响期望。时钟 `now` 可替换，用来钉住窗口边界。
- **未做**：没有后台清理旧记录的任务（上游同样没有；历史表大小由调用方决定），也没有自动重试 —— `Check` 只回答「现在能不能做」，调度与退避属于调用方。

Parity: `framework/rate_limiter.py`、`tests/test_social_rate_limiter.py`。21 条测试中 20 条逐条对应上游；唯一 `Parity-exempt` 钉住「配置层夹在 env 与默认之间」与「钳 1 不钳天花板」这两个上游测试未覆盖的不变量。

### 4.19 MCP 连接池：refcount 与每服务器 transition latch

`internal/tools/mcp_connection_manager.go` 复刻 `framework/loader/mcp_connection_manager.py`。存在的理由与上游一致：queen 与多个 worker 可能同时使用同一个 MCP 服务器，而每条连接都是一个昂贵的子进程或 socket；池按**服务器名**复用连接，并保证「最后一个使用者释放时才断开」。

- **状态**：一把 `sync.Mutex` 保护四张表——`pool`（server name → client）、`refcounts`、`configs`（重连时需要的配置快照）、`transitions`（server name → latch）。所有表的增删都在锁内完成；网络/子进程操作（`Connect`/`Disconnect`/`ListTools`）一律在锁外执行，避免阻塞其他服务器。
- **并发模型**：上游的 `threading.Event` 在 Go 侧映射为 `mcpPoolTransition{done chan struct{}; once sync.Once}`：`set()` 是幂等 `close(done)`，`wait(timeout)` 用定时器 select，`isSet()` 用非阻塞 select。每个服务器最多一个 transition；拿到 latch 的调用者是唯一连接者，其余调用者在 latch 上等待后被唤醒重试——这是「并发 acquire 只建一个客户端」的根据。等待超过 30s（`mcpPoolTransitionTimeout`）视为卡死：强制删除该 transition 并置位，让等待者重试。
- **Acquire/Release 生命周期**：Acquire 命中已连接且无 transition 的条目时只增 refcount；否则建 latch、在锁外 `Connect`，成功后安装 client 并增 refcount。若发现自己在 transition 上输掉竞争（期间被清理/替换），丢弃刚建的连接并重试。Release 在 refcount > 1 时只减计数；减到 0 时先从表中摘除 client/config 并安装一个清理 latch，再在锁外 `Disconnect`，最后释放 latch。`Disconnect` 错误被吞掉（上游 log-and-swallow）：状态已经清干净，错误不影响后续使用同一服务器名。
- **健康检查**：`stdio`/`sse` 调一次 `ListTools`，并通过可选接口 `mcpListErrorReporter`/`mcpConnectedReporter` 读取失败与连接状态——Go 的 `MCPClient.ListTools()` 没有 error 返回值，这两个接口承接上游 `list_tools()` 抛异常与 `_connected` 的语义。`http`/`unix` 走 `GET <url>/health`（5s 超时、携带配置 headers、unix 直接 dial socket），2xx 才算健康。未知服务器或未知传输返回 false。
- **Reconnect**：保留 refcount，先断开旧 client 再建新连接并装回表中；服务器名从未登记时报 `Unknown MCP server: <name>`（上游 `KeyError`）。若重连期间最后一个持有者已 Release，则丢弃新 client 并报「fully released during reconnect」，绝不制造幽灵引用。
- **CleanupAll**：先等待所有 in-flight transition 收敛（超时则强制清掉未完成的 latch），再在锁内一次性摘走 client 表并清空 refcounts/configs，锁外逐个断开，最后释放清理 latch。顺序保证清理不会与正在进行的 connect 交叉。
- **注入点**：`NewClient`（客户端工厂）与 `HealthProbe`（HTTP 探针）是字段，测试注入 fake；`SharedMCPClientPool()` 提供进程级单例（上游 `get_instance`），测试则各自 `NewMCPClientPool()` 持有独立实例。

Parity: `framework/loader/mcp_connection_manager.py`、`tests/test_mcp_connection_manager.py`。17 条测试中 16 条直接对等（复用/refcount、并发、清理、重连、健康检查、错误路径），1 条 `Parity-exempt` 钉住单例契约。

### 4.20 自适应 worker 运行预算：统计收缩、证据抬升

`internal/colony/adaptive_budget.go` 复刻 `ColonyRuntime._maybe_adapt_colony_budget` / `_apply_adaptive_budget`，`internal/agentloop/loop.go` 提供配套的 `ApplyLifetimeBudgetCap`、`LifetimeBudget`、`ToolCallsUsed`、`BudgetLimited`。它解决的是「固定上限对所有 worker 一视同仁」的浪费：一个殖民地里成功的 worker 才定义常态，名义预算向其实测消耗收缩，使大概率失败的 worker 在预算-宽限机制里被提前收尾，而不是烧穿整个固定上限。

- **样本准入（严格，删失观测零影响力）**：只有 `status == "success"` 的终态报告入样；`budget_limited`、`budget_pinned` 为真者、`tool_calls_used == 0` 者、以及非整数计数（`pyInt` 复刻 `isinstance(x, int)`：bool 接受，字符串/浮点/None 拒绝）一律丢弃。滚动窗口 50 条，超出按最旧淘汰。`budgetMaxSuccess` 是**独立于窗口**的终身最大值——它不随样本淘汰而回退，这是 max-guard 在窗口滚动后仍能托住上限的原因。
- **名义公式**：`nominal = clamp(floor=30, base=200, max(ceil(2.0 × median), ceil(1.25 × maxSuccess)))`。`median` 用 `statistics.median` 语义（偶数取中间两数均值），**不是整数近似**；`base` 取 `DefaultWorkerLoopConfig["tool_call_lifetime_budget"]`，`base <= 0` 视为剖面禁用、直接返回。三条安全阀：`p90 > 4 × median` 视为异构负载、无单一常态，跳过本轮；样本不足 3 条不动作；env/选项 kill switch 关闭时整段逻辑短路（连样本都不收）。
- **收缩广播，抬升不广播**：`nominal < current` 时先更新名义与 `budgetAppliedSet`，**释放 `budgetMu` 之后**再 `clampInFlightWorkers`——它只遍历 pending/running 且未 pinned、非 persistent 的 worker，并在释放注册表锁后才逐个调用 `ApplyLifetimeBudgetCap`。锁序刻意写成「注册表锁 → 释放 → loop 状态锁」，避免与终态遥测的读取路径构成 ABBA。抬升走另一条路径：`evidenceFloor = min(base, max(floor, ceil(1.25 × maxSuccess)))`，仅当它超过当前名义才生效，且**从不广播**——已被收缩的在途 worker 以旧上限跑完，新准入的 worker 通过 `ApplyAdaptiveBudget` 拿到新名义。
- **为什么抬升必须由证据驱动**：收缩是统计性的（中位数可以随便宜样本下降而下降），抬升却不能是——一旦 clamp 正在删失昂贵尾部，上升的中位数只是删失后的假象。允许中位数抬升会把名义变成单向棘轮，让工作量变贵的殖民地永远回不到固定上限。因此只有「确实有人在某成本上成功过」这一不受删失影响的观测（`budgetMaxSuccess`）才有资格抬升，且结果以 profile base 封顶。
- **setter 的单向性**：`ApplyLifetimeBudgetCap` 拒绝抬升、相等值、非正值、无上限循环（`current <= 0`）以及 `GraceIterations <= 0` 的循环——最后一条是因为没有宽限轮的 worker 被截断时会静默死亡而非上报，反而制造删失样本。
- **实时遥测**：`ToolCallsUsed`/`LifetimeBudget` 读 loop 状态；`BudgetLimited` 读 `tool_lifetime_budget_grace` 计数，因此取消/崩溃等没有 `AgentResult` 的终态路径也能把「预算截断」如实标进报告，保持删失样本不被误纳入。
- **注入与开关**：`RuntimeOptions.AdaptiveToolBudget`（`*bool`，nil 时回落到进程默认 `HIVE_ADAPTIVE_TOOL_BUDGET`，默认开）、`HIVE_ADAPTIVE_BUDGET_MIN_SAMPLES`、`HIVE_ADAPTIVE_BUDGET_FLOOR`；`get_stats()` 的 `adaptive_tool_budget` 切片暴露 `enabled` / `nominal`（首次收缩前为 nil）/ `samples`，与上游同构。
- **不 panic 契约**：采样器在 worker 的终态路径上被调用，任何策略缺陷都不能让一个 worker 收不了尾——所有分支只返回，不抛错。

Parity: `framework/host/colony_runtime.py`（`_maybe_adapt_colony_budget`、`_apply_adaptive_budget`、`get_stats`）、`framework/agent_loop.py`（`apply_lifetime_budget_cap`、`tool_calls_used`）、`framework/server/routes_colony_workers.py`（`_worker_info_to_dict`）、`tests/test_adaptive_tool_budget.py`。上游 20 条全部对等：策略层 19 条（agentloop 5 + colony 14），序列化层 1 条由 `internal/server/worker_list_test.go` 覆盖；HTTP handler 接线随 P3 落地，序列化契约本身已对等，不再记缺口。

### 4.21 global-db 数据路由：决策层与 HTTP 适配器分离

`internal/server/global_data.go` 复刻 `routes_colony_workers.py` 的 global-db 代理决策，但不引入 HTTP 框架依赖。上游 handler 只做三件事——解析请求、调用 global-db 客户端、把结果映射成响应；Go 把其中的本地决策与响应映射拆成纯函数，使 `test_global_data_routes.py` 的断言语义不依赖 aiohttp `TestClient`。

- **lead 身份**：`NormalizeLinkedIn` 去 `http(s)://`、去 `www.`、从首个 `?`/`#` 截断、去尾斜杠并小写（只剥一层 scheme，与上游 `re.sub(r"^https?://", "", u)` 一致，`http://https://x` 仍保留内层 scheme）；`DeriveLeadID` 优先 LinkedIn slug，回退到去空白小写的 email，`isinstance(x, str)` 用类型断言复刻。
- **insert 计划**：`PlanInsertRow` 在返回前完成全部本地拒绝——leads.lead_id 缺失且无法从 email/LinkedIn 派生时返回 400，绝不产生 upsert；非 int 单 PK 铸 16 字节随机 hex（`uuid4().hex` 形状，`mintID` 可注入以固定测试）。`rowTruthy` 让 `0`/`False`/`""`/空容器与 Python `not row.get(pk)` 同义。
- **响应映射**：`MapInsertResult` 保留 attempted pk，使 409 冲突时 UI 能打开既有记录而不是覆盖；`GlobalDBErrorResponse` 用 `errors.As` 区分 `NotSignedInError`（401 + `signed_out`）、`GlobalDbError`（其 status，缺省 400）与未知错误（502）。
- **changes 游标**：`ChangesSinceParam` 复刻 `query.get("since") or None`——空串表示初始化，不向后端传游标。

Parity: `framework/server/routes_colony_workers.py`（`_normalize_linkedin`、`_derive_lead_id`、`handle_global_insert_row`、`_global_db_error_response`、`handle_global_changes`）、`framework/server/tests/test_global_data_routes.py`。8 条上游测试逐条对等；上游的 HTTP 外层由 aiohttp `TestClient` 驱动，Go 直接测决策层，handler 接线随 P3 落地。

### 4.22 会话目录解析：一次遍历、两种策略

会话 id 在磁盘上不唯一：queen DM 会话 fork 进 colony 后保留原 id，同一 id 可同时位于 `queens/<q>/sessions/<sid>` 与 `colonies/<c>/queens/<q>/sessions/<sid>`。`internal/server/session_dirs.go` 复刻 `framework/server/session_manager.py`，把「找到哪些目录」与「这些目录意味着什么」分开：

- **一次惰性遍历**：`forEachQueenSessionDir` 先扫 DM 树、再扫每个 colony 的 `queens/*`，只产出 `stat` 成功的候选，DM 候选永远排在前面。上游 `_iter_queen_session_dirs` 是生成器，首匹配消费者不会列举后续目录；Go 因此用访问器承载遍历（`visit` 返回 false 即停），排序靠后的坏目录不会让已命中的查找报错，错误只在遍历真正走到时上抛。`listDirIfExists` 精确复刻 `Path.exists()` 与 `iterdir()` 的错误差异——stat 失败读作「不存在」并跳过，列举失败则上抛，等价上游的 `OSError` 语义。
- **目录判定跟随符号链接**：`Path.is_dir()` 会跟随符号链接，而 `os.DirEntry.IsDir()` 不会；`entryIsDir` 用 `os.Stat` 判定，符号链接指向的 queen/colony 树仍是候选目录。
- **归属判定**：`ColonyIDForSessionDir` 只看祖先——先 `filepath.Clean` 规范化（等价 pathlib 在构造时去掉尾分隔符），再沿 `filepath.Dir` 链；某目录的父目录名为 `colonies` 时该目录即 colony slug。路径自身从不参与，所以 `/colonies/acme` 与 `/colonies/acme/` 单独都不会被误判为归属于 `colonies`。DM 路径返回 false。
- **两种消费策略**：`FindQueenSessionDir` 取首匹配并回退到 `queens/default/sessions/<sid>`（DM 优先）；`DeriveResumeColonyID` 在同一结果上跳过 DM 候选、绑定首个 colony 归属，因此即使重复的 DM 目录在前，resume 也不会丢 colony 上下文，真实 DM 会话也不会被误绑。
- **依赖注入**：上游用 monkeypatch 改模块全局 `QUEENS_DIR`/`COLONIES_DIR`；Go 把 `hivehome.Home` 作为参数传入，测试天然隔离临时目录。

Parity: `framework/server/session_manager.py`（`_colony_id_for_session_dir`、`_iter_queen_session_dirs`、`_find_queen_session_dir`、`_derive_resume_colony_id`）、`framework/server/tests/test_session_dir_resolution.py`。8 条上游测试逐条对等；另有 5 条 `Parity-exempt` 回归测试固定上游 fixture 未覆盖的三处语义：尾分隔符规范化、符号链接树跟随、遍历惰性（坏目录排在命中之后不得上抛，排在命中之前则上抛）。全部为纯文件系统读写，无 HTTP 依赖。

### 4.23 判官槽与停滞检查：可选模型、概率判定、异步收尾

`judge` 是配置里的一个可选模型槽，`stall_check` 是一个 Developer 选项；两者组合出「女王宣称要做却停手」的守卫。实现分为三层，互不耦合：

- **判官层（`internal/server/judge.go`）**：`JudgeSlot` 每次提问都重读 `configuration.json`，槽必须含真值的 `api_base` 与 `api_key`，否则视为不可用。`AskJudge` 按槽的 `protocol` 从注册表取协议（内置 `typesafe`，测试可覆盖），协议签名固定为 `(slot, question, state, criteria) -> (probability, error)`；任何不可用或失败都只记日志并返回 `ok=false`，不把错误抛给调用方。`TypesafeJudge` 以 20 秒超时向 `<api_base>/systemone` 提问 `noul`，非 2xx、缺失 `answers.answer.noul`、或值不可转 float 都是失败；客户端用 `http.ErrUseLastResponse` 显式不跟随重定向，3xx 即错误（httpx 默认 `follow_redirects=False` + `raise_for_status()` 的语义）。
- **配置层（`internal/server/hive_config.go`）**：`HiveConfig` 对 BOM、坏 JSON、非对象一律读作空配置；`StallCheckEnabled` 先看 `HIVE_STALL_CHECK` 环境变量（`0/false/no/off` 视为关），否则看文件内顶层 `stall_check` 的 Python 真值。`StallJudgeSlot` 把两者合成：选项关闭时判官槽不可用。由于每次判定都重读，开关和换判官对运行中的会话立即生效。
- **回合层（`internal/server/stall_check.go`）**：订阅 queen 流的五类事件，在互斥锁下维护当前回合的 `user/reasoning/reply/tools/judged/previous`。工具调用意味着新的模型调用，清空 reasoning 与 reply，使判定只看最后一次模型调用的言辞；收到新用户消息时，把上一回合的回复尾部 600 字符与工具列表存为 `previous_turn`，让判官能区分「纠正刚交付的作品」与「空谈」。仅当回合以 `turn_done` 结束、有回复、非判过、非可见错误（仅 `[Error: LLM call failed: ` 前缀，允许前导空白；`[System:` 消息由系统注入，不是模型回复）时才提问；每条用户消息至多判一次、提醒一次。state 截取用户消息尾部 2000 字符、reasoning 与 reply 各 1500 字符，空工具列表序列化为 `[]` 而非 `null`。p 达到槽阈值（缺省 0.5）时以 `is_client_input=false` 注入提醒文本。

异步边界：上游 `publish` 会等待 handler，故用 `asyncio.create_task` 把判定挪出发布路径；Go 的 `Publish` 同样等待 handler，因此判定在 goroutine 中执行，`StallCheckSubscription` 用 `sync.WaitGroup` 跟踪在途判定，暴露 `Subscriptions`/`Wait`/`Close`，与 `SubscribeTitleTrigger` 同一模式。测试用 `Wait` 获得确定性检查点，生产路径不会让 20 秒判官超时阻塞事件总线。

Parity: `framework/server/judge.py`、`framework/server/stall_check.py`、`framework/server/tests/test_judge.py`、`framework/server/tests/test_stall_check.py`。7 条 stall check 与 3 条 judge 上游测试逐条对等；第 7 条 stall check 用例经 `/api/config/llm-sections` 与 `/api/config/features` 驱动，路由接线后同样是真实 HTTP 对等。另有 1 条 `Parity-exempt` 用例以双服务器实测 302 不被跟随、重定向目标零命中。

### 4.24 配置特性开关、LLM 槽位与 provider 库：决策层与 HTTP 适配器分离

`routes_config.py` 里两类端点——布尔特性开关与模型槽/提供商库——都遵循同一模式：HTTP 层只做收包与回包，全部决策（解析、校验、持久化、副作用）落在纯函数层。Go 移植保留这条边界：`config_features.go` 与 `provider_library.go` 不 import 任何 HTTP 包，可以只以 `(status, body)` 断言；`routes_config.go` 是薄适配器，只解析请求、调用决策层、把返回值写回响应。

**特性开关（`internal/server/config_features.go`）。** 解析顺序固定为「环境变量 > `configuration.json` 顶层布尔 > 缺省值」，与 Python 逐字对齐：

- `envFeatureFlag` 只在环境变量存在且去空白后非空时生效；`0/false/no/off`（大小写不敏感）为假，其余为真。
- 文件层 `adaptive_tool_budget`/`email_senders`/`memory_timeline` 只认 `isinstance(value, bool)`；字符串 `"true"` 不算布尔，回退缺省。`stall_check` 例外：上游 `get_stall_check_enabled` 用 `bool(...)`，故非空字符串（含 `"false"`）与非空容器都为真。缺省值分别为 `adaptive_tool_budget=true`、`email_senders=false`、`memory_timeline=false`、`stall_check=false`。
- `UpdateFeatures` 接受裸对象或 `{"features": {...}}` 包装；只遍历白名单 `FeatureKeys`，任一键值非布尔即 400，全部白名单键都缺席也 400，错误文本逐字对齐（`"<key> must be a boolean"`、`"No known feature keys. Supported: ..."`）。校验失败发生在写盘之前，因此坏请求绝不触碰 `configuration.json`。
- 落盘走 `WriteHiveConfig`：同目录建临时文件、`json.MarshalIndent` 两空格缩进并补尾换行、`os.Rename` 原子替换；无关键原样保留。`WriteHiveConfig` 与 `MutateHiveConfig` 都持包级 `configMu`，后者把「读—改—写」作为一个临界区。
- 写盘后两个副作用按更新的键触发：`adaptive_tool_budget` 热应用（见下），`email_senders` 调 `SyncEmailSendersEnv` 把 `HIVE_EMAIL_SENDERS` 写成 `"1"/"0"`，让随后拉起的 MCP 子进程继承。
- 热应用 `ApplyAdaptiveBudgetFlag` 遍历运行中会话：无 colony 的跳过；`metadata.json` 里 `adaptive_tool_budget` 是布尔（无论何值）即视为该 colony 的持久 pin，跳过；只有「有显式值且与目标不同」的 colony 才被翻转并计数。metadata 读或解析失败静默跳过，与上游 debug-log-and-continue 一致。

**槽位与库（`internal/server/provider_library.go`）。** 四个槽位 `llm`/`worker_llm`/`vision_fallback`/`judge` 共享同一个写入器 `WriteRoleSection`：

- 段必须含非空 `model`（400 `"'model' is required"`）；`provider` 缺省 `openai`；`api_key` 为空时尝试读 `api_key_env_var` 指向的环境变量。
- 健康检查有四个前置条件同时成立才发生：请求未显式传 `validate: false`、key 非空、`api_base` 非空、角色不是 `judge`。结果只在验证器返回**显式** `false` 时阻断写盘（404/400/500 之外的 400，文本 `"Key check failed against <api_base>: <message>"`，message 缺省 `"unknown error"`）；`None`/`True` 都放行，保留上游「只拦明确失败」的宽松语义。
- 段逐字存入 `configuration.json` 的对应顶层键，不做字段裁剪或规整；保存 provider 库条目时同理，库内副本与槽内副本互相独立。
- 只有 `llm` 槽写入成功后触发热切换，模型串为 `provider + "/" + model`，key 与 base 一并传给注入的 `HotSwap`，返回被切换的会话数；`judge` 与其他槽不切换。
- `PutLLMSection` 支持清除：`section` 缺失或为 null 时删除该槽，但 `llm` 槽拒绝清除（`"The llm slot cannot be cleared - the runtime always needs a main model."`）。
- 库操作：`PutProviderLibrary` 的 `section` 为 null 即删除条目，删空后连 `provider_library` 键一起剪掉；`GetProviderLibrary` 只保留对象值，坏库读作空。`ApplyProviderLibrary` 先校验角色（400）再查库（404 `"No provider library entry named '<name>'"`），命中后复用 `WriteRoleSection`，因此 apply 与直接写槽走完全相同的校验、健康检查与热切换路径。

**HTTP 适配器（`internal/server/routes_config.go`）。** 20 条路由由 `RegisterConfigRoutes` 挂到 Go 1.22+ 模式 mux：`GET /api/config/models`、`GET/PUT /api/config/sentinel`、`GET/PUT /api/config/features`、`GET/PUT /api/config/llm-sections`、`GET/PUT /api/config/llm`、`GET/PUT /api/config/provider-library`、`POST /api/config/provider-library/apply`、`GET/PUT /api/config/rate-limits`、`GET/PUT /api/config/profile`、`POST/GET /api/config/profile/avatar`、`GET/PUT /api/config/external-skills`。所有写 handler 共用 `decodeConfigBody`：`io.ReadAll` 后整体解码到 `any`，空体、坏 JSON、尾随垃圾回 400 `{"error": "Invalid JSON body"}`（`request.json()` 本身失败，handler 自答）；能解析但不是对象（数组、`null`、标量）则回 500 通用 `{"error": "Internal server error"}`——上游拿到该值后调用 `.get` 抛 `AttributeError`，由 aiohttp 错误中间件统一兜底。`decodeConfigBodyRaw` 额外返回原始字节并用 `json.Decoder.UseNumber()`，让 rate-limit 端点既能按文档顺序合并覆盖，又能逐字回写未被请求改动的槽值。handler 不修改状态码或响应体，直接 `writeJSON` 决策层返回值；会话列表、验证器、热切换、凭据存储与哨兵唤醒均经 `ConfigRouteDeps` 注入。

**并发写串行化。** aiohttp 所有 handler 跑在同一个事件循环上，`get_hive_config()` 与 `_write_config_atomic()` 之间的读改写不会交错；Go 的 handler 是真并发，两个请求可能读到同一份底稿再各自覆盖，造成丢写。因此 `config_features.go` 用包级 `configMu` 把每次读改写包成临界区：`MutateHiveConfig(home, mutate)` 加锁读、交给 `mutate` 编辑、按返回值决定写盘。四处库/槽写入与特性更新全部经由它；`WriteRoleSection` 的健康检查留在锁外，只有最终写盘进锁，避免网络等待阻塞其他配置请求。原子替换只保证文件不撕裂，不保证更新不丢失，两者是不同层面的保障。

Parity: `framework/config.py`（`get_adaptive_tool_budget_enabled`、`get_email_senders_enabled`、`get_memory_timeline_enabled`、`sync_email_senders_env`）、`framework/server/routes_config.py`（`register_routes`、`_write_config_atomic`、`_write_role_section`、`handle_get_features`、`handle_update_features`、`_apply_adaptive_budget_flag`、`handle_get_llm_sections`、`handle_put_llm_section`、`_get_provider_library`、`handle_put_provider_library`、`handle_apply_provider_library`）、`core/tests/test_routes_config_features.py`、`core/tests/test_routes_provider_library.py`。上游 18 条测试逐条对等（特性 10 条、库与槽 8 条）。特性文件中的 3 条 getter 用例由决策层测试覆盖，其余 7 条特性 HTTP 用例与 8 条库/槽用例（共 15 条）经真实 `httptest` 服务器驱动路由并断言状态码/JSON/落盘；另有 2 条 `Parity-exempt`：一条以 5 个用例矩阵覆盖四条写路径共用的严格 JSON 解析（空体/坏体/尾随垃圾 400，数组/`null` 500），一条用 25 个并发 PUT 钉住配置文件读改写串行化（上游单事件循环无从复现丢写）。

### 4.25 元信息与 LLM 配置路由：无状态决策层与注入式运行时

同属 `routes_config.py` 的另外八组端点按同一分层移植：`config_meta.go` 承载模型目录、哨兵、限流、外部技能，`profile.go` 承载用户档案与头像，`llm_config.go` 承载 `GET/PUT /api/config/llm`。三者都不 import HTTP 包，路由文件只做收包、调用、回包。

**模型目录与哨兵（`internal/server/config_meta.go`）。**

- `ModelsResponse` 每次请求都解析 `llm.GetModelsCatalogue()`，而不是像 Python 那样在 import 时快照；这样测试可以先布置环境再提问，响应仍是 `{"models": {provider: [...]}}` 的同一形状。
- `SentinelResponse` 把任何假值块（缺席、`null`、空对象、`false`、`0`、`""`）报成 `{}`。`UpdateSentinel` 接受裸对象或 `{"sentinel": {...}}` 两种体；非对象回 400 `"sentinel must be an object"`，空对象删除该顶层键而非写入空块。写盘后调用注入的 `refresh`，其 panic 被 `safeCall` 吞掉，复现上游「刷新失败不影响 200 响应」。
- `UpdateRateLimits` 只接受 `<platform>.<action>.<window>` 三段键，window 限于 `hourly/daily/weekly`，platform 与 action 必须已在内置限额表中；值按 Python `int()` 语义强制转换，小于 1 夹到 1，超过推荐上限时按请求文档顺序生成告警文本。合法项合并进已有 overrides 后返回**生效限额全集**，不是仅本次改动项。`orderedKeys` 由 `decodeConfigBodyRaw` 从原始字节中解析，nil 时退回排序键。
- 外部技能：`ExternalSkillsResponse` 读配置中的路径列表并解析为 `ResolveExternalSkills` 行（失效路径被剪掉），同时给出 `SuggestedExternalSkills`；`PutExternalSkills` 校验列表形状（坏列表 400），把裁剪后的有效路径写回，存储值不是可迭代对象时按上游抛错语义回 500。

**档案与头像（`internal/server/profile.go`）。** GET/PUT 共用 `profileBody` 形状（`displayName`/`about`/`theme`/`density`/`prompt_library_sort`）。PUT 只接受白名单取值：主题 `light/dark`、密度 `spacious/compact`、排序键 `date/name/popular`，其余静默忽略；字符串字段经 `pyStrValue`（Python `str()` 的容器与标量渲染）后去空白。保存后 `SyncUserProfileMemory` 把身份写进 `global-memory/user-profile.md`，只替换 `## Identity` 段并保留反思代理添加的其他段，任何失败都被吞掉。头像上传按首个 multipart part 的 `Content-Type` 只收 JPEG/PNG/WebP，上限 2 MB，非空；写入前删除旧 `avatar.*`；GET 按 `.jpg → .png → .webp` 探测顺序返回第一个存在的文件与一小时公共缓存头。

**LLM 配置（`internal/server/llm_config.go`）。** `LLMRouteDeps` 把凭据存储、订阅 token、订阅探测、模型校验与热切换全部做成可注入接缝，零值回退到真实凭据库、`~/.claude`/`~/.codex` 与共享模型目录，因此决策层在没有网络的环境下可完整测试。

- GET 逐项复刻上游：`has_api_key` 先看运行时会实际使用的 key（命名端点 → `api_keys` 池 → 段内 `api_key` → `api_key_env_var` → provider 环境变量 → 订阅），只有左侧为假才求值右侧，故非字符串 provider 在左侧已命中时不会报错；`connected_providers` 跳过 `google`/`together_ai`；`active_subscription` 是第一个真值订阅旗标；`detected_subscriptions` 只保留能取到 token 的探测结果。存储的 `llm` 段不是对象时回 500。
- PUT 分派顺序固定为 custom → subscription → API key。custom 模式保留已配置端点、只切换模型，校验空白后的 `model` 非空，写入前删除所有订阅旗标，成功后以 `provider + "/" + model` 热切换并返回被切换会话数。subscription 模式按 preset 写入限额与旗标、删除 `api_key_env_var`、校验 token 存在，未知或不可哈希的 id 回对应错误。API key 模式要求非空 `provider`/`model`、模型在目录中、key 可取得，写盘前走与槽位相同的健康检查，校验失败不改文件。
- 三条写路径全部经由 `MutateHiveConfig`，与特性/槽位共享包级 `configMu`，保证并发 PUT 不丢写；热切换与外部网络校验留在锁外。
- 数值源拼写透传：配置经 `HiveConfig` 以 `UseNumber` 解码，手写的 `123` 仍是 `json.Number("123")` 而非 `123.0`，GET 回包与 Python `str()` 的拼写一致；`api_base`/`model` 等数值字段经 `stringPointer` 转成字符串，避免上游 `str()` 语义在 Go 侧退化为 `nil`。
- `valid is False` 严格校验：只有校验器显式返回布尔 `false` 才拒绝写入，`0`、`""`、缺失等假值都视为通过，复刻上游 `check.get("valid") is False` 的同一性判断。
- 订阅无 `api_base` 时删除陈旧键：preset 写入前先删掉配置里残留的 `api_base`/`api_key_env_var`，防止切换到订阅后仍打到旧自定义端点。
- 尾随垃圾读空：`HiveConfig` 在首个 JSON 值之后再解码一次，非 `io.EOF` 即视为空配置，对齐 Python `json.load` 拒绝尾随垃圾的行为。

Parity: `framework/server/routes_config.py`（`handle_get_models`、`handle_get_sentinel_config`、`handle_update_sentinel_config`、`handle_get_rate_limits`、`handle_update_rate_limits`、`handle_get_profile`、`handle_update_profile`、`handle_upload_user_avatar`、`handle_get_user_avatar`、`handle_get_external_skills`、`handle_put_external_skills`、`handle_get_llm_config`、`handle_update_llm_config`）。`routes_config_meta_test.go` 19 条中 1 条对等（`test_get_models_uses_shared_model_catalogue`），其余 18 条 `Parity-exempt` 覆盖上游未单测的元信息路由；`llm_config_test.go` 24 条中 2 条对等，22 条 `Parity-exempt` 覆盖 custom、订阅、API key 三条写入分支与 GET 的 active/detected 过滤，并钉住配置数值的源拼写透传、`valid is False` 的严格校验、无 `api_base` 订阅的陈旧键清理与尾随垃圾读空。全部经真实 `httptest` 服务器断言状态码、响应体与落盘。

## 5. 单轮数据流

AgentLoop 核心循环已落地（`internal/agentloop`，含压缩、停顿/恢复、提醒注入与每工具超时预算），以下是与上游 `agent_loop.py` 对齐的实际路径：

```
用户消息
  │
  ├─► 追加 conversation part（%010d.json）
  ├─► 更新 cursor.json（iteration/turn/accumulated_output）
  │
  ├─► 构造 LLM 请求
  │     ├─ system：Queen 人格 + 技能 catalog + 记忆召回块
  │     ├─ messages：历史 parts + 本轮输入
  │     └─ tools：当前工具清单
  │
  ├─► LLM 返回
  │     ├─ 纯文本 → 结束本轮（STOP），写 part，必要时写 partial
  │     └─ 工具调用 → 执行工具
  │            ├─ tracker_* → SQLite（变更触发器记 _tracker_changes）
  │            ├─ 结果截断 → 大输出落 artifacts/
  │            └─ 工具结果写 part，进入下一迭代
  │
  ├─► 提醒注入（ReminderHub，见第 8 节）
  │
  └─► 持久化 state.json（status/progress/metrics）
```

恢复路径：读 `state.json` → `cursor.json` → 已完成的 parts → 从 `partials/` 恢复未完成的助手轮次，再继续迭代。这正是「重跑即续跑」的实现基础。

## 6. 技能发现 / 信任 / 覆写流水线

### 6.1 发现

| 优先级 | 作用域 | 位置 |
| --- | --- | --- |
| 0 | framework | 内置 `_default_skills`（物化后扫描） |
| 1 | preset | 内置 `_preset_skills` |
| 2 | user | `~/.agents/skills`、`configuration.json: external_skills`、`HIVE_HOME/skills` |
| 3 | queen_ui | Queen 级 UI 创建目录 |
| 4 | colony_ui | Colony 级 UI 创建目录 |
| 5 | project | `<project>/.agents/skills`、`<project>/.hive/skills` |

扫描顺序即解析顺序，**同名后写入者覆盖先写入者**并记录告警；`ScopePriority` 表用于比较来源而非排序扫描。约束：最大深度 4、最多 2000 个目录、跳过 `.git`/`node_modules`/缓存目录。

### 6.2 信任

- 非项目作用域直接放行；项目作用域默认**不信任**。
- 判定三态：`AlwaysTrusted`（localhost 或显式全局信任）/ `TrustedByUser`（`trusted_repos.json` 命中）/ `Untrusted`。
- 仓库键来自 `git remote get-url origin`（3 秒超时），经 `NormalizeRemoteURL` 归一化后匹配。
- 交互模式询问「本次会话 / 永久 / 拒绝」；非交互模式输出安全提示并跳过。

### 6.3 覆写

`ApplyManagerOverrides` 的判定顺序（先命中先返回）：

1. `deleted_ui_skills` 墓碑 → 删除；
2. 显式 `enabled=false` → 删除；
3. 显式 `enabled=true` → 保留（即使 preset 默认关）；
4. `source_scope == "preset"` → 仅当它是该角色默认 preset 时保留，否则关闭；
5. 兜底：`skillsConfig.IsDefaultEnabled(name)`；若 `all_defaults_disabled` 且该技能是 framework 默认项 → 删除。

这条链保证「用户显式决定 > preset 角色默认 > 全局默认 > 内置默认」。

## 7. 热重载

`LiveSkillRuntimes` 汇总 Colony 运行时与 Queen 会话；`ReloadRuntimes` 并发调用每个运行时的 `ReloadSkills`，单个 panic 被 `recover` 捕获并记录，不影响其余运行时。`ManagerReloadAdapter` 在重载期间持有 `MutationLock`，避免 catalog 缓存与目录扫描交错。

## 8. 提醒（ReminderHub，实现说明）

提醒是**框架注入的建议性上下文**，用 `<system-reminder>` 包裹，区别于配置钩子与错误纠正。

| 注入点 | 触发时机 | 典型来源 |
| --- | --- | --- |
| `SESSION_START` | 会话启动一次 | TurnContext |
| `USER_PROMPT_SUBMIT` | 每次用户/外部消息 | Task、PastConversations |
| `POST_TOOL_USE` | 工具批次结束后，附在结果尾部 | ToolSkill、TrackerSnapshot |
| `TOOL_BUDGET_CHECKPOINT` | 工具预算跨过软阈值/硬上限 | ColonyParallelNudge |
| `PRE_COMPACT` / `POST_COMPACT` | 压缩前后 | 重新宣告被摘要丢掉的工具/技能 |
| `STOP` | 纯文本回合结束 | ActiveWorkers、Task |
| `IDLE_TICK` | 后台时钟，循环停泊时也可介入 | IdleNudge |
| `STREAM_STALLED` | 流停滞瞬间同步咨询（反应式，不入 ticker 缓冲） | StreamStall |

两种投放位置：`USER_MESSAGE` 与 `TOOL_RESULT_TAIL`。生命周期点由循环同步 `fire` 并自行放置；时间点由 Hub 的 ticker `collect` 暂存，循环在下一迭代边界 `take_pending` 取走——**这样对话写入永不与循环竞态**。`STREAM_STALLED` 是**反应式**点：看门狗取消停滞流的那一刻同步 `Collect`，赶在同一回合重新流式之前注入，不能延后到迭代边界。循环活动状态只有三种：`EXECUTING`、`AWAITING_USER`、`INTERRUPTED`。

实现落点（`internal/agentloop/`，均带 `// Parity: framework/agent_loop/reminders.py` 注解）：

- `reminder_hub.go`：`Reminder{Body, Source, Energizes, Meta}`；可选接口 `TurnObserver` / `EnergizingSource` / `TemporalSource`（`TickInterval`）/ `RichReminderSource`（`RenderReminder` 优先于裸 `Render`）。`ReminderHub` 持有已绑定子集、`pending` 缓冲与 ticker 生命周期：`Start` / `stopTicker` / `Stop` / `HasTicker` / `runTicker` / `tickOnce` / `Post` / `TakePending` / `PendingLen`。一次 fan-out 由单个 `mu` 串行化（ticker goroutine 与循环不会并发渲染同一 stateful 源），`pendingMu` 独立保护缓冲；`Stop` 通过 `done` 通道 join ticker goroutine。
- `park_reason.go`：`ParkReason` 11 值 + `AllParkReasons`；分类函数 `IsExternalWait`（ask_user / credential_form / colony_suggestion / awaiting_queen）、`IsSilentPark`（turn_done / user_stopped / cold_interrupted）、`IsBroken`（llm_error / empty_responses / doom_loop）；`Activity()` 是「停泊原因 → 顶层状态」的唯一映射点（六个刻意停泊 → `AWAITING_USER`，其余 → `INTERRUPTED`）。`LoopSignals` 是循环交给时间源的只读快照。
- `idle_nudge.go`：`IDLE_TICK` 的 `ReminderSource`。子态 `between_turns` / `slow_ttft` / `parked_no_question` / `parked_open_tasks` / `parked_broken`；三套预算（general 120s / awaiting 120s / broken 30s，经 `LoopConfig.SessionIdleNudge*` 注入）；变体封顶（`parked_no_question` 与 TurnDone 的 `parked_open_tasks` 各 1 次，其余 `MaxNudges`）；自限速以「上次求值时间」为键，任务查询因此每预算窗口最多一次；gate 日志按状态签名去重，触发 nudge 与 `Reset` 时清空。任务 / 目标 / Sentinel 自动驾控经 `ReminderAgentContext` 注入闭包读取，避免 `internal/tasks → internal/agentloop` 导入环。`TickInterval = clamp(最小正预算/2, 0.01, 5.0)`。
- `stream_stall.go`：`STREAM_STALLED` 的 `ReminderSource`（`StreamStallSource`，对齐 `framework/agent_loop/stream_stall.py`）。只在 `STREAM_STALLED` 触发，**每轮**上限（`ContinueNudgeMaxPerTurn`，默认 3）而非每会话；`ResetTurn` 由循环每轮开头调用重新武装。`enabled=false` 或上限 `<=0` → 不出；`StallReason` 为空 → 不出。原因标签 `ttft → "no tokens before TTFT budget"`、`inactive → "stream went silent after producing events"`，未知原因直出原串。正文用 %.0f 秒；meta 带 `reason` / `elapsed` / `nudge_count` / `cap`；产生时打 `[stream_stall] producing nudge (...)` 日志。看门狗仍负责探测与取消流，本源只管文案与上限。
- 循环侧：`LoopConfig` 增四项 `SessionIdleNudge*`（默认 120 / 3 / 120 / 30）与 `ContinueNudgeEnabled` / `ContinueNudgeMaxPerTurn`（默认 true / 3），均对齐 Python `internals/types.py`；`loop.go` 负责快照 `LoopSignals`、以 `signalsProvider` 启动 ticker、在迭代边界 `TakePending` 后注入，并在用户消息时 `Reset` 各源预算。
- **已知接线缺口**：Go 循环尚无流看门狗，因此 `StreamStallSource` 目前只有源本身与配置字段，`STREAM_STALLED` 的同步 `Collect` 与每轮 `ResetTurn` 尚未接到 `loop.go`；看门狗落地时按上方反应式契约接线。

## 9. 触发器错过的触发计算

参考 `core/framework/host/triggers.py`。只处理 `enabled=true` 且 `trigger_type=="timer"` 的条目；webhook 不重建；没有可解析 `last_fired_at` 的条目计 0（不为首次注册前的时间补触发）。

**cron**（`_missed_for_cron`）：

- 从 `since` 出发迭代，统计**严格晚于 since 且不晚于 now** 的匹配次数；
- `next_due_at` 是严格晚于 `now` 的第一个匹配；
- 表达式非法或 croniter 不可用 → `(0, [], nil)`；
- ticks 最多枚举 100 条，count 仍为真实值。

**interval**（`_missed_for_interval`，单位分钟）：

```
step  = interval * 1min
count = floor((now - since) / step)          # now > since 时
ticks = since+step, since+2*step, ... ≤ now  # 最多 100 条
next  = since + step * (count + 1)
```

边界：`interval <= 0` → 无；`now <= since` → count 0、next = `since+step`；`count <= 0` → 同上。同时配置 cron 与 interval 时 **cron 优先**。只有 `count > 0` 的条目才会进入 `MissedTrigger` 列表并触发 `MISSED_TRIGGERS` 事件。

## 10. 并发、一致性与错误处理

| 场景 | 策略 |
| --- | --- |
| 文件写 | 唯一临时文件 + fsync + rename；同目录保证 rename 原子性 |
| 会话状态 | `UpdateState` 读-改-写；进程内锁保护；跨进程假设单写者 |
| SQLite | `MaxOpenConns(1)` + WAL；事务内完成多语句 |
| MCP 连接池 | 每服务器一个 transition latch，锁内只改表、锁外做 I/O；refcount 归零才断开 |
| 技能重载 | `MutationLock`；单运行时 panic 隔离 |
| 凭据缓存 | 互斥锁保护 map；TTL 到期后台刷新，失败按配置抛出或降级 |
| 部分失败 | 索引/缓存损坏可重建；主数据（`.enc`、`tracker.db`、`state.json`）不静默丢弃 |

## 11. 演进与未决

- `internal/llm` 的纯逻辑层（§4.14）、请求塑形（§4.15）、非流式完成路径（§4.16）与 `AnthropicProvider` 包装（§4.17）已定型；剩余待决是真实 `AComplete` 的传输与错误分类映射——是复刻 LiteLLM 的异常层级，还是收敛成 `ProviderError` + `RetryHeaders()` 的单一形状（当前纯逻辑层与 `complete` 的 429 判定都已按后者实现）。
- `internal/ratelimit`（§4.18）已定型并与上游共享数据库；未决是「配置层不钳天花板」是否为有意设计——Go 侧按上游字面量保留，若上游后续修正，Go 侧只需改 `resolveLimit` 一处。
- `internal/tools` 的 MCP 连接池（§4.19）已定型。未决是单例的生命周期边界：库当前只在 `CleanupAll` 时释放全部连接，若未来 CLI 需要「每命令一个池」，应改用 `NewMCPClientPool` 而不是扩展单例语义。
- AgentLoop 落地后，`session.Cursor` 的字段可能需要扩展（例如 pending inputs 的具体形状）。
- `internal/platform` 尚未实现；在它出现之前，任何平台分支必须局部化并有 `//go:build` 约束。
- 第 8 节（提醒）与第 9 节（触发器错过计算）已按实现改写并补上 parity 注解。后续新增时间源须遵守「ticker `collect` + 迭代边界 `take_pending`」的投放契约；反应式源（如 StreamStall）例外，必须在其触发瞬间同步 `Collect`，并自带每轮 `ResetTurn`。StreamStall 的接线依赖尚未落地的流看门狗。
