# 从 Python 版 hive 迁到 Go 版：已知不支持与已知差异

> 这份文档只讲一件事：**Python 版里有、Go 版里没有等价物或被降级的地方**。
> 每一条都写清楚"你会在什么时候撞上它"和"撞上之后怎么办"。如果你没用到下面这些
> 用法，迁移基本无感；如果你用到了，这里给的都是可执行的替代路径。
>
> 本文不是完整性承诺。它记录的是 **已核实** 的缺口，不是"除此外全都一致"的保证。
> 逐字段的存储/凭据差异见 `storage-compat.html`，通用兼容政策见
> `compatibility-policy.md`，工具面逐条对齐见 `tool-contract-diff.md`。

文档中所有源码位置都经过实际核对。凡与既有说法不符之处，在对应小节末尾用
**核实修正** 标出。

---

## 一、总表

| # | 能力 | Python 版 | Go 版 | 用户可见影响 | 有绕过方式吗 |
| --- | --- | --- | --- | --- | --- |
| 1 | OAuth2 凭证 provider 体系 | `credentials/oauth2/` 完整 provider，含授权码换 token、client credentials、吊销、内省，以及 Zoho / HubSpot provider 各自的 quirks | **仅保留 token 数据模型与刷新类型，未接入凭证 store 的 provider 注册表**；store 层没有任何 provider 能处理 `oauth2` 类型凭证 | 无法用 `hive-cred` 走 OAuth2 授权流程；除 google / hubspot / zoho_crm 外，任何 OAuth2 凭证在 store 里都不会被自动刷新 | 部分可以。这三个 provider 的 **刷新** 改由模块层 + 环境变量完成；授权本身需要在 Go 版外部（上游 Python 版 / Aden）完成 |
| 2 | DecisionTracker 后验决策审计 | `core/framework/tracker/decision_tracker.py`（405 行），把 run / decision / outcome 写成 `runs/{run_id}.json` | **库已移植、生产写入方缺失（partial）**：`internal/tracker/decision_tracker.go` / `stream_runtime.go` 有完整 API 与落盘，colony `buildContext` 也接线（`decision_runtime.go:88`）；但两侧都没有 `decide()` / `record_outcome()` 的生产调用方，图宿主（唯一归属）out_of_scope | 缺少按"决策 → 选项 → 理由 → 结果"粒度的后验分析数据。**不影响运行时行为** | 无等价替代。可在 Go 侧继续看 runtime 日志（`details.jsonl` / `summary.json`）做粗粒度排查；不要因 API 齐全就宣称审计链已对齐（守卫见第 2 节） |
| 3 | `collect_result` 与 `credentials` 两个模型面工具 | 模型可直接调用 | 名字仍然对外广播；`collect_result` 由 agent loop 的 background-tool registry 执行，`credentials` 由 session catalogue 执行，`set_output` 在 executor 前按上游拒绝 | 模型可继续按上游提示词调用 `credentials` / `collect_result`；`set_output` 的拒绝文案与上游一致 | 不需要绕过；旧的 `credentials → hive-cred` / `collect_result → terminal_job_logs` 建议已过时 |
| 4 | 图执行引擎（checkpoint 续跑 / node 级重试 / 条件路由 / EntryPoint 热替换） | `orchestrator` + `node_worker` + `edge` + `agent_host` 一整套图执行器 | 架构替换为 colony + agentloop 双运行时，**无图对象，无等价执行器** | 依赖图能力的用法需要改写：多节点编排、断点续跑、按节点失败重试、按条件分支、动态换入口节点 | 大部分可以用 colony（多 worker 调度）+ agent loop 内部循环表达；但**没有逐项 1:1 映射**，需要重新设计 |

---

## 二、逐条详述

### 1. OAuth2 凭证 provider 体系

**Python 版有什么。** `core/framework/credentials/oauth2/` 是一个完整的 OAuth2
provider 家族：

| 文件 | 行数 |
| --- | --- |
| `base_provider.py` | 479 |
| `lifecycle.py` | 361 |
| `zoho_provider.py` | 196 |
| `hubspot_provider.py` | 112 |
| `provider.py` | 213 |
| `__init__.py` | 94 |

`base_provider.py` 提供 `BaseOAuth2Provider`，方法覆盖
`get_authorization_url` / `exchange_code` / `client_credentials_grant` /
`refresh_access_token` / `revoke_token` / `should_refresh` / `revoke`
（`base_provider.py:113`、`:149`、`:181`、`:214`、`:249`、`:341`、`:357`）。
`lifecycle.py` 的 `TokenLifecycleManager` 负责"拿一个确定有效的 token"并回写
（`lifecycle.py:39`、`:184`、`:291`）。`zoho_provider.py` / `hubspot_provider.py`
是 provider 专项差异：Zoho 的 token URL 要从 accounts 域名推导
（`zoho_provider.py:89`），并覆写 `format_for_request` / `validate` / `refresh`
（`:109`、`:124`、`:168`）。

**Go 版实际有什么（2026-11-26 更新）。**

- `go_hive/go-hive/internal/credentials/models.go` 定义了
  `CredentialTypeOAuth2 CredentialType = "oauth2"`，`CredentialObject` 带
  `provider_id` / `auto_refresh` / `last_refreshed`。
- `internal/credentials/oauth2.go` 定义 token/config/error 模型；
  `oauth2_provider.go`、`oauth2_hubspot.go`、`oauth2_zoho.go`、
  `oauth2_lifecycle.go` 是完整的 provider family：Base/HubSpot/Zoho 的
  授权 URL、code exchange、client credentials、refresh token grant、
  RFC 7009 revocation、五种 token placement、validate/should-refresh，以及
  `TokenLifecycleManager`。
- `internal/credentials/store.go` 已有 `WithProviders` / `RegisterProvider`
  以及 refresh-on-access：命中过期 token 时先 `provider.Refresh`，成功再
  持久化并更新缓存。
- `internal/credentials/adapter.go` 的 `DefaultAdapterWithAden` 是 `hive-cred`
  使用的生产适配器：设置 `ADEN_API_KEY` 时，它把 encrypted+env composite
  包进 `AdenCachedStorage`、注册 `AdenSyncProvider` 并做一次 best-effort
  sync——这是上游 `CredentialStoreAdapter.default()` 唯一默认接线的
  OAuth2-capable provider；没有 key 时退回本地 composite store。
- `internal/credentials/oauth_refresh.go` 与
  `internal/tools/integrationkit/oauth.go` 仍负责 Google/HubSpot/Zoho
  工具侧的 env→token 刷新。

**默认 provider 的边界（重要）。**

1. **默认 store 只注册 `StaticProvider`，与上游一致。**
   `StaticProvider.SupportedTypes()` 是 `{api_key, basic_auth, custom}`，
   `BearerTokenProvider` 是 `{bearer_token}`，默认注册表里没有 OAuth2
   provider。HubSpot/Zoho provider 必须显式
   `WithProviders(...)` / `RegisterProvider(...)` 注册；不要默认注册它们：
   两者都需要 client id/secret，而且按注册顺序做类型回退会把“没有
   provider_id 的通用 oauth2”错误地判给第一个 provider。
2. **没有 provider 时不再把过期 token 说成“已刷新”。** best-effort
   `GetCredential` 仍按上游返回 stale 值（`last_refreshed` 不变）；
   但 `raise_on_refresh_failure=true` 的调用（如 `hive-cred token`）
   现在返回 `CredentialExpiredError` → CLI `reauth_required`，不会再把
   过期 token 当刷新结果交出去。
3. **`hive-cred` 没有 `login` / `authorize`，这是对齐上游而不是缺口。**
   `framework/credentials/cli.py` 只注册了 `token` / `browse` / `inspect`；
   Go 的 `internal/cli/cred.go` 保持同样的三个 leaf。授权发生在
   Aden 控制台/桌面端或外部 consent 流程，之后 `hive-cred token` 通过
   Aden provider 刷新；本地-only 模式不提供交互式授权。

**你会不会被影响？** 分四种情况：

- **Google / HubSpot / Zoho CRM + `ADEN_API_KEY`** → `hive-cred token`
  会先从 Aden sync，再在过期时通过 `AdenSyncProvider` 刷新；工具侧仍由
  `TokenRefresher` 独立刷新。**可用。**
- **同一组 provider，但只有本地 refresh token、没有 Aden key** → 工具侧
  仍能用 `TokenRefresher`；`hive-cred token` 走本地 store，没有默认
  OAuth2 provider，过期时会得到 `reauth_required`。要么设置
  `ADEN_API_KEY`，要么在库调用里显式注册 provider。**部分可用。**
- **想用 `hive-cred` 走授权流程** → 两侧 CLI 都没有这个命令；用 Aden
  控制台/桌面端完成授权。**不支持（对齐）。**
- **其它 OAuth2 凭证** → 可以存；默认不会刷新。过期且 `auto_refresh`
  为 true 时，raise 路径报 `reauth_required` 而不是静默 401；要真正刷新，
  显式注册一个配置好 token URL 的 `BaseOAuth2Provider`。**需要显式配置。**

**历史核实修正：** 本文早期版本说“没有 provider 认领 oauth2、类型无生产
消费、没有 Zoho/HubSpot 专项逻辑”，这些在 OAuth2 provider family 落地后
已不成立；仍成立的是“默认 store 不自动注册 OAuth2 provider”以及
“`hive-cred` 没有 login/authorize”，两者都与上游一致。

---

### 2. DecisionTracker 后验决策审计

**Python 版有什么。** `core/framework/tracker/decision_tracker.py`（405 行）定义
`DecisionTracker`（`:24`），供 agent 记录三层结构：run 生命周期
（`start_run` / `end_run`）、决策（`decide(intent, options, chosen, reasoning)`）、
结果（`record_outcome(decision_id, success, result)`）。落盘位置是
`{storage_path}/runs/{run_id}.json`
（`core/framework/storage/concurrent.py:192-206`、`save_run_sync` 在 `:475`）。

接在女王链路上的位置是 `core/framework/server/queen_orchestrator.py:1669`：

```python
runtime=DecisionTracker(queen_dir),
```

import 在同文件 `:1648`。这个实例同样没有写入方：`ctx.runtime` 在 Python 生产
代码里只出现在 `node.py` 的 docstring 示例中，agent loop 从不调用它的
`decide()` / `record_outcome()`。

**Go 版实际有什么（状态：partial — 库已移植，生产写入方缺失）。**
`internal/tracker/decision_tracker.go`（`DecisionTracker`）与
`internal/tracker/stream_runtime.go`（`StreamDecisionTracker` /
`StreamRuntimeAdapter`）已逐函数移植 run / decision / outcome 三层 API 与
`runs/{run_id}.json` 落盘（证据：`decision_tracker_test.go` /
`stream_runtime_test.go`）。`internal/colony/decision_runtime.go:88` 在
worker/overseer 的 `AgentContext.Runtime` 上接线每-worker 流（上游
`_make_runtime_adapter` 同点位，`colony_runtime.py:2837-2841`）。
`internal/runtimelogs` 是**另一个模块**（`runtime_logger.py`）的移植，只写
step / 工具调用日志，与这条链无关。

**缺口在生产写入方，不在类型层。** 两侧生产代码都没有 `decide()` /
`record_outcome()` 调用方：

- `core/framework/orchestrator/node.py:619-647` 的 `NodeProtocol` docstring
  里出现的 `ctx.runtime.decide(...)` / `record_outcome(...)`（`:633` / `:644`）
  经 AST 核对是**字符串常量**，不是活代码。
- 上游唯一拥有 run 生命周期的调用方是图宿主：
  `core/framework/orchestrator/orchestrator.py:638`（`start_run`）、`:1695`
  （`end_run`），以及 `core/framework/host/execution_manager.py:677` / `:805`
  / `:945`（graph execution stream）。前者在
  `contracts/module-inventory.json:2004-2011` 登记为 `out_of_scope`（Go 无
  图执行器），后者 `:1375-1382` 登记为 `partial`（图执行主循环未移植）。
- Go 侧 `internal/agentloop/types.go:100` 的 `Runtime any` 只有 colony
  `buildContext` 写入；agent loop 从不读取（上游
  `core/framework/agent_loop/types.py:134` 同样注明
  "the agent loop never reads it"）。
- 守卫：`internal/agentloop/decision_audit_guard_test.go` 用 AST 扫描拒绝
  `AgentContext.Runtime` 上的 ledger 写方法调用点，并用真实
  `StreamDecisionTracker` 跑完整一轮，断言 loop 不产生 `runs/*.json`。它一旦
  失败，说明出现了新的写入方——此时应更新本节与 package-parity 注记，而不是
  直接宣称审计链已对齐；增加 synthetic no-op 决策事件不算写入方。

**你会不会被影响？** 运行时**不受影响**。受影响的是"事后分析"：你想回答
"这轮为什么选了这条路、当时有哪几个备选、结果符合预期吗"这类问题，Go 版没有这个
粒度的数据（上游也只有图节点这一条路径会写）。`core/framework/agent_loop/types.py:134`
的注释原文是：

```python
runtime: DecisionTracker | None  # None when nothing records decisions; the agent loop never reads it
```

意思是 **agent loop 自己不回读**这份数据 —— 它是给事后分析用的流水账，不是运行时的
控制输入。所以缺它的代价是"审计能力下降"，不是"行为变样"。

**核实修正（原说法需要收窄）：**

- "只写不读的决策日志"这个定语**过窄**。它确实**会被读**，只是读取方不是 agent
  loop：`decision_tracker.py` 模块自述（`:1-6`）写明设计目的是
  "in a way that **Builder can analyze**"。准确的表述是"agent loop 不回读、供事后
  分析用的日志"。
- "写入方"也要说准：**生产写入方不存在**。`stream_runtime.py:40/56/64` 是类
  docstring 示例（`:437` / `:450` 的 `StreamRuntimeAdapter` 只是把调用转发给
  `_runtime`）；`execution_manager.py:260` 与 `colony_runtime.py:2837-2841` 是
  **构造点**，不是调用方。不能把构造点当写入证据。
- 旁证：这条数据链的主要消费者（图执行器 `orchestrator.py:638` / `:989` / `:1243` /
  `:1695`）本身在 `contracts/module-inventory.json` 里是 `out_of_scope`，与第 4 条
  缺口同源。

---

### 3. `collect_result` 与 `credentials` 两个模型面工具被降级

**事实。** `go_hive/go-hive/internal/cli/tool_surface_flat.go:173-180`：

```go
var flatSurfaceStandaloneTools = map[string]string{
	"ask_user":       "synthetic: injected by the chat runtime, executed by the agent loop",
	"collect_result": "not implemented in this build (no background tool handles)",
	"credentials":    "not implemented as a model tool in this build (use the hive-cred CLI)",
	"search_tools":   "agent-loop tool tier when present; inventory fallback otherwise",
	"sentinel_setup": "executed by the session catalogue (internal/sentinel); advertised from the frozen contract",
	"suggest_colony": "not implemented as a model tool in this build (colony UI)",
}
```

调用时不会静默失败，而是返回一个明确的包络
（`tool_surface_flat.go:526-531`）：

```go
case "collect_result", "credentials", "suggest_colony":
	return tools.PythonJSONDumps(map[string]any{
		"error":   "not_implemented",
		"tool":    name,
		"message": flatSurfaceNotImplementedMessage(name),
	}), true, true
```

具体的替代指引在 `flatSurfaceNotImplementedMessage`（`:541-556`），原文点名了两个
替代路径：`collect_result` → `terminal_job_logs(job_id, wait_until_exit=true)`；
`credentials` → `hive-cred browse --json` / `hive-cred inspect <id> --json` /
`hive-cred token <id> --account <alias>`。

**你会不会被影响？** 两种情况：

- **模型自己用** → 受影响。名字还在，但结果是 `not_implemented`。
- **你在提示词 / 技能 / 自定义 agent 里教模型用 `credentials` 工具查凭证** → 受影响，
  且是最容易踩的一种：模型会拿到 `not_implemented`，然后要么自己重试、要么直接
  告诉你"我没有权限查凭证"。改法是把那几句改成"用 `terminal_exec` 跑 `hive-cred`"。

**顺带两点（原说法未覆盖，但同属这一类）：**

- 降级的不止两个，还有第三个：`suggest_colony`（`:179`）。
- `collect_result` 的**工具定义其实是存在的** ——
  `internal/agentloop/synthetic_tools.go:271` 的 `BuildCollectResultTool()`，
  描述和参数 schema 都写全了。但全仓 grep 该函数名**没有任何调用方**，
  所以它从未被注册进模型可见的工具表。可见的是"定义有了、接线没做"，
  这一点在 release note 里值得单列，因为它离可用只差一次注册。

---

### 4. 图执行引擎被架构替换

**Python 版有什么。** 四个文件构成图执行引擎：

| 文件 | 行数 |
| --- | --- |
| `core/framework/orchestrator/orchestrator.py` | 1,740 |
| `core/framework/orchestrator/node_worker.py` | 812 |
| `core/framework/orchestrator/edge.py` | 564 |
| `core/framework/host/agent_host.py` | 1,879 |

`contracts/module-inventory.json` 把这四个文件连同
`shared_state.py`、`preload_validation.py`、`checkpoint_config.py`、`context.py`
一起标记为 `status: "out_of_scope"`（共 8 个文件）；
`streamtoken_refresh.py` 已从该集合移出，Go 端口在 `internal/streamtoken`（C12 `ported`）。
`go_counterpart` 对其余 8 个文件一律为 `null`。清单自己给的定义（`module-inventory.json:16`）：

> `"out_of_scope": "Python 调用链只存在于 Go 不实现的宿主（图执行引擎），basis 同时给出 python 调用链锚点与 Go 侧证据"`

**Go 版实际有什么。** 双运行时替换：

- `internal/colony` — 非测试 **11,306** 行（含测试 24,275）
- `internal/agentloop` — 非测试 **14,187** 行（含测试 29,518）

`internal/agents/agent_spec.go:9` 的自述说得很直白：

> `The Python loader finishes by constructing an AgentLoader/GraphSpec; this port stops one layer earlier and returns the worker spec itself, because the Go colony host takes an agentloop.AgentSpec plus a schemas.Goal rather than a graph object.`

即 **Go 侧根本没有"图"这个东西**。`internal/orchestrator/` 目录只剩三个文件
（`buffer.go`、`safe_eval.go`、`validator.go`），**没有执行器**。

**逐项影响：**

| 能力 | Python 侧 | Go 侧 | 你要做什么 |
| --- | --- | --- | --- |
| checkpoint 续跑 | `checkpoint_config.py`（81 行）配置图检查点状态 | 无。`internal/agentloop/loop.go:590` 的 "soft checkpoint" 指的是**预算软提醒**，与断点续跑无关 | 长任务要自己拆段，或用 `terminal` 后台 job + `terminal_job_logs(wait_until_exit=true)` 保活 |
| node 级重试 | `node_worker.py:102` `max_retries`、`:349-395` 指数退避重试与 retry 事件 | 字段存在但**从不生效**。`MaxRetries` / `RetryOn` 被解析进 `internal/agents/agent_spec.go:243-244`、赋值于 `:422-424`、拷进 `internal/agentloop/types.go:51-52`，然后全仓**零处读取** | 重试要写在 agent 自己的工具调用循环里（loop 有预算与恢复机制，但没有"同一步失败重跑 N 次"的语义） |
| edge 条件路由 | `edge.py:40` `EdgeCondition`（`ALWAYS` / `ON_SUCCESS` / `ON_FAILURE` / `CONDITIONAL` / `LLM_DECIDE`），`:161` `_evaluate_condition` 用 safe_eval | 无。grep `Conditional\|EdgeCondition\|OnSuccess\|LLMDecide` 在 `internal/colony` + `internal/agentloop` 命中为 **0** | 分支判断要交给模型在 loop 里决定，或用 colony 的 worker 分工 |
| EntryPointSpec 热替换 | `execution_manager.py:102` 定义，字段含 `entry_node`、`isolation_level`、`priority`、`max_concurrent`、`max_resurrections`；`agent_host.py:287` `register_entry_point` 运行期注册 | 无对应类型。`internal/server/routes_session_detail.go:141` 的 `buildEntryPoints` 是从 trigger 定义投影出来的，`entry_node` **硬编码为空串**（`:161`） | 入口节点这一层概念在 Go 侧不存在，触发即"起一个 session 跑 agent loop" |

**你会不会被影响？** 判断标准很简单：**你的 agent 是单节点（一个 worker 一件事）还是多节点图？**

- 单节点 → 不受影响。这也是绝大多数用法，`agent_spec.go` 的自述提到 loader
  本来就是"single-node graph"。
- 多节点图、或依赖上面表格任一行 → 受影响，且**没有 1:1 迁移路径**，需要按
  colony / agent loop 的模型重新设计。这类用法建议先在 Python 版上继续跑。

**核实修正（重要，原说法有误）：**

- 路径写错了。这些文件在 `core/framework/**orchestrator**/orchestrator.py`
  等同级目录下，不是 `core/framework/orchestrator.py`（后者不存在）。
  `agent_host.py` 在 `core/framework/**host**/agent_host.py`。
- **"共 6094 行"这个数字对不上。** 四个文件实际合计 **4,995** 行
  （1740 + 812 + 564 + 1879）。6094 − 4995 = 1,099，而仓库里没有任何一个是 1,099 行
  的文件；把 `execution_manager.py`（1,269 行）算进去会得到 6,264 也不是 6,094。
  该数字无法复现，本文改用可核验的 4,995。
- 其余全部核实通过：四个文件行数逐一精确匹配；9 个 `out_of_scope` 文件含全部四个；
  `internal/colony` 11,306 行与 `internal/agentloop` 14,187 行（非测试口径）
  **精确匹配**。

---

## 三、如果你的用法踩到这里

按"你想做什么"索引，不按缺口编号索引。

### 我要用 HubSpot / Zoho / Google，已经在上游授权过

**能做。** 把三件套存进去，Go 版会自动刷新：

```
# 凭证 id 用 google / hubspot / zoho_crm
hive-cred browse --json        # 先看现有账号与别名
hive-cred token google --account <alias>
```

Go 侧读的是 `internal/tools/integrations/oauth.go` 声明的环境变量
（`GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` / `GOOGLE_REFRESH_TOKEN` /
`GOOGLE_ACCESS_TOKEN_EXPIRES_AT`，HubSpot 与 Zoho CRM 同理，
由 `internal/credentials/oauth_refresh.go:29-45` 的 `OAuthRefreshEnvBindings()`
导出）。只要这四个值在，`integrationkit.TokenRefresher` 会在 token 过期前换新。

### 我要在 Go 版里从头走一遍 OAuth2 授权

**当前不能。** 没有 `hive-cred login` / `authorize`，也没有
`get_authorization_url` / `exchange_code` 实现。

替代路径：在 Python 版完成一次授权，把 refresh token 取出来，存进 Go 版的凭证。
之后 Go 版就能自己刷新，不再需要 Python 版。

### 我用了 google / hubspot / zoho_crm 之外的 OAuth2 凭证

**需要你自己兜住刷新。** 这类凭证在 Go 版里能存、能读，但**不会自动刷新，也不报错**
（`store.go:408` 遇到 nil provider 直接原样返回）。做法是在你的自动化里显式换 token：

```
hive-cred inspect <credential_id> --json    # 看 expires_at
hive-cred token <credential_id> --account <alias>
```

或者干脆在外部用 refresh token 换好新 access token，再写回凭证。

### 我的提示词 / 技能里教模型调用 `credentials` 工具

**改一句就够。** 把

> 用 `credentials` 工具查一下有什么凭证

改成

> 用 `terminal_exec` 执行 `hive-cred browse --json` 查凭证，
> `hive-cred token <id> --account <alias>` 取值

`credentials` 工具会返回 `{"error": "not_implemented", ...}` 并附上同样的指引
（`tool_surface_flat.go:553-555`），但模型有时会因此直接放弃，不如提前改掉。

### 我的提示词里让模型用 `collect_result` 取后台结果

**改成 `terminal_job_logs`。** 两个等价写法：

- 直接等结果：`terminal_job_logs(job_id, wait_until_exit=true)`
- 要中途停：`terminal_job_manage(job_id, action="signal_term")`

`collect_result` 在 Go 版返回 `not_implemented`，理由写的是
"no background tool handles"（`tool_surface_flat.go:175`）。

### 我想要"这个 agent 这轮为什么这么决策"的审计数据

**Go 版没有。** 能够拿到的是 runtime 日志：
`{HIVE_HOME}/sessions/{run_id}/logs/details.jsonl`（step 级）、
`tool_logs.jsonl`（工具调用级）、`summary.json`（L1 汇总），
见 `internal/runtimelogs/runtime_log_store.go:45-71`、`:92`、`:105`、`:138`。

这些能回答"调了什么工具、花了多久、哪步失败"，但**回答不了**
"当时有哪几个备选、为什么选这个、结果是否符合预期"。要后者得留在 Python 版。

### 我的 agent 是多节点图 / 依赖断点续跑 / 依赖节点级重试 / 依赖条件分支

**这类用法建议先不要迁。** 表里四项在 Go 侧都没有等价物：

- 重试：`max_retries` / `retry_on` 会被读进配置但**从不生效**
  （`agent_spec.go:243-244`、`agentloop/types.go:51-52`，零处读取）。写进去不会报错，
  失败也不会重试 —— 这是最隐蔽的一种"看起来配了其实没用"。
- 条件分支：无 `EdgeCondition` 等价物。
- 断点续跑：loop 的 "soft checkpoint" 是预算提醒，不是可恢复的执行点。
- 入口节点热替换：无 `EntryPointSpec` 等价物，entry point 的 `entry_node`
  在 API 投影里恒为空串（`routes_session_detail.go:161`）。

要迁的话，正确做法是把"图"翻译成 colony 的 worker 分工 + agent loop 内部循环。
这不是配置项差异，是设计模型差异。

---

## 四、这份清单没覆盖什么

- 它只列 **已核实** 的四处缺口，不是穷举。逐字段的存储与凭据差异见
  `storage-compat.html`；工具名与 schema 的逐条对齐见 `tool-contract-diff.md`；
  环境变量与配置键的两侧差集见 `contracts/env-vars.json`、`contracts/config-keys.json`。
- 它不判断"整体是否兼容"。这份文档存在的前提就是承认差异存在；能不能迁取决于
  你的用法是否落在上面这些格子里。
- 行数、路径、注释原文均基于核对时的仓库状态。源码演进后请以代码为准，
  本文档的证据位置可供复核。

---

## 附录：证据索引

| 编号 | 证据 | 位置 |
| --- | --- | --- |
| 1 | OAuth2 provider 家族行数 | `core/framework/credentials/oauth2/*.py`（479 / 361 / 196 / 112 / 213 / 94） |
| 1 | Python 侧 docstring 示例（非惰性导出） | `core/framework/credentials/__init__.py:31-32` |
| 1 | Go `oauth2` 类型定义 | `go_hive/go-hive/internal/credentials/models.go:15`、`internal/credentials/oauth2.go:1-194` |
| 1 | Go 刷新绑定 | `internal/credentials/oauth_refresh.go:29-45` |
| 1 | Go 模块侧刷新声明 | `internal/tools/integrations/oauth.go:1-212` |
| 1 | Go TokenRefresher | `internal/tools/integrationkit/oauth.go:129-420` |
| 1 | provider 注册表不含 oauth2 | `internal/credentials/provider.go:41-43`、`:69-71`；`internal/credentials/store.go:97-108`、`:408-411` |
| 1 | `hive-cred` 仅三个子命令 | `internal/cli/cred.go:320`、`:339`、`:350` |
| 2 | DecisionTracker 定义 | `core/framework/tracker/decision_tracker.py:24`（全文 405 行） |
| 2 | 女王链路接入点 | `core/framework/server/queen_orchestrator.py:1648`、`:1669` |
| 2 | "agent loop never reads it" 原文 | `core/framework/agent_loop/types.py:134` |
| 2 | 真实写入方 | `core/framework/host/stream_runtime.py:40/56/64`、`host/execution_manager.py:260`、`host/colony_runtime.py:2841` |
| 2 | 落盘路径 | `core/framework/storage/concurrent.py:192-206`、`:475` |
| 2 | Go 侧对应的是另一个模块 | `go_hive/go-hive/internal/runtimelogs/runtime_logger.go:5`；`runtime_log_store.go:92`、`:105`、`:138` |
| 3 | 降级工具原文 | `go_hive/go-hive/internal/cli/tool_surface_flat.go:173-180`（`collect_result` `:175`、`credentials` `:176`、`suggest_colony` `:179`） |
| 3 | `not_implemented` 包络 | `internal/cli/tool_surface_flat.go:526-531` |
| 3 | 替代指引原文 | `internal/cli/tool_surface_flat.go:541-556` |
| 3 | `collect_result` 定义存在但无调用方 | `internal/agentloop/synthetic_tools.go:271`（全仓零调用） |
| 4 | 四个图引擎文件行数 | `core/framework/orchestrator/orchestrator.py` 1740、`node_worker.py` 812、`edge.py` 564、`core/framework/host/agent_host.py` 1879（合计 4,995） |
| 4 | `out_of_scope` 标记 | `contracts/module-inventory.json`（9 个文件，定义见 `:16`） |
| 4 | Go 双运行时行数 | `internal/colony` 11,306（非测试）、`internal/agentloop` 14,187（非测试） |
| 4 | Go 无图对象自述 | `go_hive/go-hive/internal/agents/agent_spec.go:9` |
| 4 | Go 无图执行器 | `internal/orchestrator/`（仅 `buffer.go` / `safe_eval.go` / `validator.go`） |
| 4 | 重试字段零消费 | `internal/agents/agent_spec.go:243-244`、`:422-424`；`internal/agentloop/types.go:51-52` |
| 4 | Python 侧重试实现 | `core/framework/orchestrator/node_worker.py:102`、`:349-395` |
| 4 | 条件路由 | `core/framework/orchestrator/edge.py:40`、`:161`（Go 侧零命中） |
| 4 | EntryPointSpec | `core/framework/host/execution_manager.py:102`；Go 侧 `internal/server/routes_session_detail.go:141`、`entry_node` 空串于 `:161` |
