# MCP 跨实现双跑核对（Python ↔ Go）

基线：`85595e30`（worktree `.wt/mcp-crosscheck`）。
方法：**同一个 stdio MCP server 进程、同一份 server 条目 JSON，分别喂给 Python hive 与 Go hive 的真实装载路径，抓两边实际注册给模型的工具定义后逐字段对照。**
证据：`go_hive/docs/e2e-assets/mcp-crosscheck/{evidence.json,python-side.json,go-side.json,summary.txt}`，可重放命令见文末。

## 0. 结论

| 维度 | 结果 | 证据位置 |
|---|---|---|
| 工具定义（description） | **逐字一致**（`echo_text` 两侧同 sha） | `evidence.json#/tools` |
| inputSchema 透传 | **一致**：两侧都保留 `type/properties/required/default/enum/minimum/maximum`，未改写类型或必填；`expected_schema_sha256` 两侧均命中 | `evidence.json#/dimensions/schema_passthrough` |
| 工具命名 | **有意差异**：Python 用 server 侧裸名；Go 加 `mcp__<namespace>__<server>__<tool>` 前缀并折叠非法字符 | `evidence.json#/dimensions/namespace` |
| 同名冲突 | **有意差异**：Python 先到先得（内置 `read_file` 压掉 server 的 `read_file`）；Go 命名空间让两者共存 | `evidence.json#/dimensions/collision` |
| server 启停 | **语义一致**：两侧都按存储的 `enabled` 生效，禁用后装载 0 个 server | `evidence.json#/dimensions/enable_disable` |
| reload | **Go 扩展**：Python 无 reload 路由，下个 queen 会话生效；Go 有 `POST /api/mcp/reload` + generation 重挂 | `evidence.json#/dimensions/reload` |
| 超时优先级 | **Go 扩展**：Go 为 env > `configuration.json mcp.timeout_seconds` > 240s；Python 只有 env（import 时读一次），无文件读取点 | `evidence.json#/dimensions/timeout` |
| transport 枚举 | **同效**：共有 stdio/http/unix/sse；Python 的 `harness` 在 Go 侧是 `HarnessGroupNames` 按名分派，不是 transport case | `evidence.json#/dimensions/transports` |
| 路由面 | Python 7 条；Go 8 条，唯一多出 `POST /api/mcp/reload` | `evidence.json#/dimensions/routes` |

`evidence.json` 的 `verdict.unexpected_differences` 为空；`verdict.status = partial` 的原因是上面 5 条**明确例外**（命名/冲突/reload/超时文件开关/transport 表示），不是未知差异。

**⚠️ 本节的夹具是自建 stdlib server（`echo_server.py`），只覆盖两侧都实现好的那部分协议。第 8 节的真实第三方 server 双跑复现了其中 3 条，并暴露了 3 个夹具看不到的差异。**

## 1. 夹具与两侧入口

夹具 `go_hive/docs/e2e-assets/mcp-crosscheck/echo_server.py` 是零依赖 stdlib 的 MCP stdio server（`initialize` / `tools/list` / `tools/call` / `ping`），故意放两个工具：

- `echo_text`：`text`（string, required）、`count`（integer, `minimum/maximum/default`）、`mode`（string, `enum/default`）——用来验证 schema 是否原样透传。
- `read_file`：与内置文件工具同名——用来观察命名空间/同名冲突。

同一份 server 条目（`evidence.json#/fixture/entry`）：

```json
{"name": "mcp-crosscheck-echo", "transport": "stdio", "command": "python3",
 "args": ["<repo>/go_hive/docs/e2e-assets/mcp-crosscheck/echo_server.py"]}
```

| 侧 | 配置文件语法 | 装载路径 |
|---|---|---|
| Python | `mcp_servers.json` = `{"servers": [entry]}` | `framework.loader.tool_registry.ToolRegistry.load_mcp_config`（`core/framework/loader/tool_registry.py:671`），先注册内置 `files-tools` harness 组模拟真实会话顺序 |
| Go | `configuration.json` = `{"mcp": {"enabled": true, "timeout_seconds": 12, "servers": [entry]}}` | `newMCPRuntime().Reload()` → `configuredMCPServers`（`go_hive/go-hive/internal/cli/mcp_config.go`）→ `newSessionToolCatalogue(...).Defs()`（`go_hive/go-hive/internal/cli/session_tools.go`） |

两侧都是**真子进程 + 真 MCP 握手**；Go 侧走的是 `hive serve` 启动时装 MCP 的同一条路径（`internal/cli/mcp_loader.go`），Python 侧走 queen 会话装载 registry 的同一条路径。

## 2. 工具定义逐字对照

| server 工具 | Python 注册名 | Go 注册名 | description | schema |
|---|---|---|---|---|
| `echo_text` | `echo_text` | `mcp__mcp_crosscheck_echo__echo_text` | 逐字一致 | 两侧 sha 都等于夹具 `0786d219…` |
| `read_file` | **未注册**（被内置 `read_file` 压掉，origin=`files-tools`） | `mcp__mcp_crosscheck_echo__read_file` | — | Go sha 等于夹具 `c35bd42a…` |

- 内置 `read_file` 本身两侧也一致（sha `a0461db8…`，来自各自 harness/builtin 定义）。
- 两侧实际调用都成功：`echo_text` 返回 `HELLO MCP\nHELLO MCP`，Go 的 `mcp__…__read_file` 返回 `fixture-read_file:/tmp/fixture.txt`（`evidence.json#/sides/*/calls`）。
- schema 归一化口径：两侧都只保留 `{type: object, properties, required}` 并对 `CONTEXT_PARAMS`（`agent_id`/`data_dir`/`profile`/`profile_display_name`/`session_cwd`/`colony_id`/`principal`）做剥离；本夹具未声明这些参数，所以输出应逐字等于 `inputSchema` 的 `properties`+`required`。实测两侧均相等。

## 3. 明确例外（逐条）

### 3.1 工具命名：Python 裸名 vs Go 命名空间

- Python `ToolRegistry._convert_mcp_tool_to_framework_tool` 的 `name=mcp_tool.name`（`core/framework/loader/tool_registry.py:1422`），server 名不进入注册键，因此**两个 server 暴露同名工具时后者被静默压掉**（first-wins）。
- Go `NamespacedMCPToolName(namespace, server, tool)`（`go_hive/go-hive/internal/tools/mcp_registry.go:553`）生成 `mcp__<namespace>__<server>__<tool>`，并把 `[^A-Za-z0-9_]+` 折叠成 `_`：server `mcp-crosscheck-echo` → `mcp_crosscheck_echo`，所以最终名是 `mcp__mcp_crosscheck_echo__echo_text`。这是 `docs/architecture/contracts.md` 已冻结的 Go 契约（命名空间阻止远端 `read_file` 覆盖沙箱内置工具），本核对只把它从「契约」升级为「实测」。

**残余风险（未修，登记）**：折叠不可逆——`a-b` 与 `a_b` 两个 server 会得到同一前缀，工具名再按 first-wins 处理；命名空间还会把名字长度推高（server+tool 各 64 字符时可能超过 provider 的 64 字符工具名上限）。这两点都是 Go 扩展自身的边界，不影响与 Python 的行为对照，但调用方不应依赖「名字可还原 server 名」。

### 3.2 同名冲突：first-wins vs 命名空间共存

实测（`evidence.json#/dimensions/collision`）：

- Python：夹具 server 的 `read_file` **没有**进入注册表；`_mcp_server_tools["mcp-crosscheck-echo"] == ["echo_text"]`，`read_file` 的 origin 是 `files-tools`。把内置组撤掉再装（`lifecycle.fresh_session_fixture_tools`）时，server 的 `read_file` 才会以裸名出现——证明 Python 的规则是「载入顺序决定归属」。
- Go：`bare_registered_names.read_file == true`（内置）且 `mcp__mcp_crosscheck_echo__read_file` 同时存在；`echo_text` 不会以裸名泄漏。

### 3.3 reload 语义

- Python：`routes_mcp.py` 的模块 docstring 明确「新 server 下个 queen 会话生效；存活会话保持启动时的工具表」，没有 reload 路由，`MCPRegistry` 也没有 `reload` 方法（实测 `registry_has_reload_method=false`）。实测同一 home 里 `add_local` 后，新会话装载能看到 `['echo_text','read_file']`，旧 registry 不变。
- Go：`POST /api/mcp/reload`（`go_hive/go-hive/internal/server/routes_mcp.go:92`，代码内标注 Parity-exempt）重读配置、重建连接池并 bump generation；新会话目录拿到新 server 集，存活会话在下一次工具调用时按 generation 重挂（`ResyncMCPServersIfNeeded`）。实测改名后旧 server 的工具**消失**（`renamed_stale_tools_present=false`）、新 server 工具出现（`renamed_new_tools_present=true`）。

### 3.4 超时优先级

| | 默认 | `configuration.json` `mcp.timeout_seconds` | `HIVE_MCP_CALL_RESULT_TIMEOUT` |
|---|---|---|---|
| Python | 240s | **不读**（实测 file=12 时仍是 240） | 37s（import 时读一次；实测子进程环境 37 → `MCPClient._CALL_RESULT_TIMEOUT=37`） |
| Go | 240s | 12s（实测 `MCPCallResultTimeout()=12s`） | 37s（每次调用读，env 赢 file） |

Go 的 `env > configuration.json > 240s` 有 `TestMCPCallResultTimeoutPrecedence` 看守（Parity-exempt：Python 没有文件值）。

### 3.5 transport 与路由

- transport 枚举：Python `Literal["stdio","http","unix","sse","harness"]`；Go `switch config.Transport` 支持 `stdio/http/unix/sse`，`harness` 由 `HarnessGroupNames` 在建立 client 之前按 server 名分派（`go_hive/go-hive/internal/tools/mcp_registry.go:356`），所以不是缺 case。
- 路由：Python 7 条（list/add/remove/enable/disable/health/mode），Go 8 条 = 同 7 条 + `POST /api/mcp/reload`（Go 扩展）。
- 附带说明：`GET /api/mcp/servers` 的 `tool_names` 报的是 **server 侧工具名**（`tools/list` 原名），不是带 `mcp__` 前缀的会话名；模型实际拿到的是带前缀的名字（`chat_tool_tiers.mcpToolNames()` 负责映射）。Python 的 `tool_count` 只报数量，没有这个字段。

## 4. 本次修复（双跑暴露的真实缺陷）

**缺陷**：`MCPServerStatus.ToolNames` 存的是 server 侧原名，但 `withMCPWorkerTools()`（`go_hive/go-hive/internal/cli/mcp_loader.go:277`）直接拿它和「带命名空间的会话工具定义」比对——`mcp__<server>__<tool>` 永远匹配不上原名，**MCP 工具因此从未被提升进 colony worker 的 base surface**。原单测用一个「本来就是命名空间名」的假 status 把这个问题盖住了。同时 `chat_tool_tiers.mcpToolNames()` 写死默认命名空间 `mcp`，`configuration.json mcp.namespace` 自定义后会比对错名。

**修复**（低风险，语义不变）：

1. `tools.MCPServerStatus` 增加 `Namespace` 字段（`go_hive/go-hive/internal/tools/mcp_bootstrap.go`），loader 在成功与超时两条 status 路径都填 `m.namespace`（`go_hive/go-hive/internal/cli/mcp_loader.go`）。
2. `mcpLoadedToolNames()` 用 `status.Namespace`（空则回落 `mcp`）对每个 server 工具名做 `NamespacedMCPToolName` 再比对。
3. `chat_tool_tiers.mcpToolNames()` 同上，支持自定义命名空间。
4. 回归：`TestWithMCPWorkerToolsPromotesLoadedTools` 改用真实形状（status 存原名 + 默认/自定义 namespace 两例）；`TestMCPRuntimeReachesColonyWorkerPool` 增加「提升后必须出现在 worker base surface」断言；新增 `internal/server/mcp_tool_names_test.go` 锁定自定义命名空间映射；`TestMCPRuntimeLoadsToolsIntoSessionCatalogue` 断言 `status.Namespace == "mcp"`。

## 5. 复现

```bash
cd /Users/mac/hive/.wt/mcp-crosscheck
uv run python go_hive/scripts/mcp_crosscheck.py           # exit 0 = 无未知差异
# 单独跑一侧
cd core && uv run python ../go_hive/scripts/mcp_crosscheck_python.py \
    --servers /tmp/mcp_servers.json --out /tmp/python-side.json --home /tmp/python-home
cd go_hive/go-hive && MCP_CROSSCHECK_OUT=/tmp/go-side.json \
    MCP_CROSSCHECK_SERVERS=/tmp/mcp_servers.json \
    go test ./internal/cli -run TestMCPCrosscheckDump -count=1 -v
```

Go 的 dump 测试默认跳过（`MCP_CROSSCHECK_OUT`/`MCP_CROSSCHECK_SERVERS` 未设时 `t.Skip`），不污染常规 `make test`。

## 6. 证据文件

| 文件 | 内容 |
|---|---|
| `go_hive/docs/e2e-assets/mcp-crosscheck/echo_server.py` | 夹具 MCP stdio server（2 个工具） |
| `go_hive/docs/e2e-assets/mcp-crosscheck/evidence.json` | 机器可读：两侧工具定义 + sha256 + 逐维判定 + 例外清单 |
| `go_hive/docs/e2e-assets/mcp-crosscheck/python-side.json` / `go-side.json` | 两侧原始 dump（工具名/description/inputSchema/calls/lifecycle/timeout） |
| `go_hive/docs/e2e-assets/mcp-crosscheck/summary.txt` | 人读摘要（同一次运行的表格） |
| `go_hive/scripts/mcp_crosscheck.py` | 双跑编排 + diff + 证据生成器 |
| `go_hive/scripts/mcp_crosscheck_python.py` | Python 侧探针（core venv） |
| `go_hive/go-hive/internal/cli/mcp_crosscheck_dump_test.go` | Go 侧探针（gated test） |

## 7. 范围外

- Python 在 LLM 边界把工具 description/schema 里的 `~/.hive` 重写成 `HIVE_HOME`（`core/framework/llm/litellm.py::_tool_to_openai_format` → `resolve_hive_paths_deep`）。这是**工具定义层**的通用行为，不属于 MCP 装载语义；Go 侧没有等价重写，归 tools 维度的已知差异，本页不重复登记。
- 第 8 节（真实第三方 server）已覆盖结果面：文本聚合、图片块、`resource_link`、工具级错误、长任务/日志通知。

## 8. 真实第三方 MCP server 双跑（stdio / SSE / HTTP）

基线：`c4eb266d`（worktree `.wt/mcp-real-crosscheck`）。
方法：**不再使用自建 server。** 两侧配置同一份 server 条目，直接对真实第三方实现双跑，并把结论与 server 自己的 `tools/list` 原始响应（本页自己抓的 raw JSON-RPC）对照——两侧任何一处不等于 raw 就不再算「透传」。
证据：`go_hive/docs/e2e-assets/mcp-real/**`（`diff.json`、`raw/<server>.json`、两侧 dump、`summary.md`、`commands.md`），编排脚本 `go_hive/scripts/mcp_real_crosscheck_run.sh`。

### 8.1 被测 server（全部真实下载，无夹具）

| 条目 | transport | 真实 server | 版本 |
|---|---|---|---|
| `everything-stdio` | stdio | `@modelcontextprotocol/server-everything`（npm，官方参考 server，16 工具） | 2.3.1 |
| `time-stdio` | stdio | `mcp-server-time`（PyPI，3 工具） | 2026.11.18 |
| `everything-sse` | sse | `mcp-proxy` 把上面的 stdio server 桥成 HTTP+SSE（`/sse`） | mcp-proxy 0.15.0 |
| `everything-http` | http | 同一 proxy 的 streamable-HTTP 端点（`/mcp`） | mcp-proxy 0.15.0 |
| `time-http` | http | `mcp-server-time --transport streamable-http`（原生 streamable HTTP） | 2026.11.18 |

两侧入口都是各自的生产路径：Python = `MCPRegistry.add_local` → `resolve_for_agent` → `ToolRegistry.load_registry_servers`（McpRegistryStage 的同一函数）+ 裸 `MCPClient`；Go = `mcpregistry.AddLocal` → `newMCPRuntime.Reload()` → `newSessionToolCatalogue`（`hive serve` 的同一路径）+ 裸 `tools.NewMCPClient`。

### 8.2 逐项结果（`diff.json` 的 `unexpected` 为空）

| 维度 | everything-stdio | time-stdio | everything-sse | time-http | everything-http |
|---|---|---|---|---|---|
| 工具清单（raw / Python / Go） | 16 / 16 / 16 | 3 / 3 / 3 | 16 / 16 / 16 | raw 3，两侧 0（见 8.4） | raw 16，两侧 0（见 8.4） |
| schema 归一化后 == raw | 16/16 | 3/3 | 16/16 | — | — |
| 调用逐字段相等（含错误/图片） | 8/8 | 2/2 | 8/8 | — | — |
| disable→0 / enable 计数 | 0 & 16 | 0 & 3 | 0 & 16 | 0 & 0 | 0 & 0 |
| 杀进程后调用恢复 | 两侧都恢复 | 两侧都恢复 | n/a | n/a | n/a |

对 raw 的透传口径（`raw/*.json` 是 server 自己回的字节）：

- **inputSchema**：raw 里每个工具带 `$schema`（2020-12）等顶层键与 `properties/required` 的完整细节（`default`/`enum`/`minimum`/`maximum`/`title`/`additionalProperties`）。两侧都**逐字保留 `properties` 与 `required`**，并**同样**把顶层信封重建成 `{type:"object", properties, required}`（`$schema`/`title` 两侧都不进模型可见 schema）——16/16、3/3 与归一化后的 raw 完全相等，两侧之间也完全相等（sha 一致）。
- **调用结果**：`echo`（纯文本）、`get-sum`（文本）、`get-sum-error`（server 侧 `isError:true`）、`get-tiny-image`（text + image 块）、`get-structured-content`（含 `structuredContent`）、`get-resource-links`（text + 2 个 `resource_link`）、`trigger-log-messages`（调用过程中 8 条 log 通知）、`trigger-long-running-operation`（2s、带 progress 通知），8 个用例两侧输出**逐字节相等**（时间 server 的 `datetime` 先做时间戳掩码）。
  - 图片路径：两侧都返回 `{"_text": …, "_images": [image_url 块]}`，最终都转成 1 个 `image_url` 块（data URL），文本一致。
  - 工具级错误：两侧都把 server 的原文包进 `{"error": "[Server: X] [Transport: Y] Tool 'get-sum' failed: <server 原文>"}`，判断句 `raw_error_text_in_both` 为 true。
  - 通知（log/progress）：两侧都不把通知误当响应，长任务 2s 正常返回。
- **崩溃恢复**：真的 `kill -9` 掉 server 进程树（`npx`/`uvx` wrapper + 其 node/python 子进程，两侧各杀 4 个），随后同一个已连接客户端再调用工具：两侧都拿到结果（`call_after_kill.ok=true`），即两侧都会在死会话信号上重连。
- **enable/disable**：两侧 disable 后装载 0 个工具、enable 后回到 16/3；Go 的 `Reload()` 会重建连接池，Python 是下一个会话生效（与第 3.3 节一致，真实 server 上复现）。

### 8.3 真实 server 暴露的、夹具看不到的差异（3 条）

1. **Go 的命名空间清洗会改写工具名本身（不只是前缀）。** `NamespacedMCPToolName` 把 `[^A-Za-z0-9_]` 折叠成 `_`，于是 server 的 `get-sum` 在模型面前是 `mcp__everything_stdio__get_sum`，`get-annotated-message` → `mcp__everything_stdio__get_annotated_message`：server-everything 的 16 个工具里 **14 个**被改名（`diff.json#/exceptions` → `tool_name_sanitization`）。执行时映射回 server 原名，功能不受影响；但
   - `a-b` 与 `a_b` 两个工具会**折叠成同一个模型可见名**，Go 侧按 first-wins 处理（夹具用的 `echo_text` 全是合法字符，所以看不到这个风险）；
   - 调用方的「提示词里写的名字」与 server 文档里的名字不再一致。
2. **两侧都静默丢弃 `resource_link` 内容块。** `get-resource-links` 返回 1 个 text + 2 个 `resource_link`（带 `uri`/`name`/`mimeType`），两侧都只把 text 交给模型（`image_blocks=0`），链接信息丢失——`_split_mcp_content`/`SplitMCPContent` 只认 `text` 与 `data+mimeType`。两侧行为一致，但这是真实 server 才暴露的覆盖缺口（`raw/everything-stdio.json#/calls/get-resource-links`）。
3. **未知工具的错误体不同，且 Go 的 did-you-mean 会因前缀而"误报"。** Python：`{"error": "Unknown tool: no_such_tool_xyz"}`；Go：`{"error": "Unknown tool: mcp__everything_stdio__no_such_tool_xyz. Did you mean 'mcp__everything_stdio__echo'?"}`。两侧用的是**同一个** difflib 移植 + `cutoff=0.6`，差别来自候选集：裸名 `no_such_tool_xyz` vs `echo` 相似度 0.30（不匹配），而带命名空间后公共前缀把相似度抬到 **0.79**（匹配）——所以 Go 在「名字其实并不接近」时也会给出建议。属提示文本差异，登记为明确例外。

### 8.4 `http` transport：两侧 tools 路径都进不了标准 streamable HTTP（共享缺口，已登记）

`everything-http` / `time-http` 两条：本页的**裸 MCP 客户端能连**（raw 各拿到 16 / 3 个工具），但两侧**模型工具路径**的 http client 都失败，且失败方式一致地来自同一个设计：

| | Python | Go |
|---|---|---|
| 请求 | `POST <url>/mcp/v1`（base_url + 固定路径） | 同左（`/mcp/v1`） |
| `time-http`（原生 `/mcp`） | 404 | `HTTP 404: Not Found` |
| `everything-http`（proxy `/mcp`） | 406 | `HTTP 406: {…"Client must accept application/json"}` |

即：Hive 的 `transport: http` 说的是 **Hive 自家 `/mcp/v1` JSON-RPC 桥**，不是 MCP 的 streamable HTTP；两侧都没实现协商（这是共享缺口，不是两侧不一致，故两侧行为仍判为「一致」）。

同一次运行里，两侧**都**能用文档化的 CLI 模式连上标准 streamable HTTP，拿满 16 个工具：

```bash
hive mcp add --name everything-http --transport http --url http://127.0.0.1:<port>/mcp
hive mcp run everything-http --list      # python 与 go 各 16 行 Available tools
```

（`commands.md` 两个 `CLI mode <side>` 段落；Python 走 `framework.loader.mcp_cli` → 同进程 mcp2cli，Go 走 `mcpregistry.RunMCP2CLI` → 外部 mcp2cli，两侧同样 16 个工具。）

**结论**：需要连标准 HTTP MCP server 时用 CLI 模式（`hive mcp run`）；`transport: http` 保持给 Hive 自家桥用。若要修，应两侧一起改（新增「先试 streamable HTTP，再回退 `/mcp/v1`」的协商），不属于本次对拍范围。

### 8.5 复现

```bash
cd <worktree>
bash go_hive/scripts/mcp_real_crosscheck_run.sh          # 需要 node/npx + uvx + 网络
# exit 0 = 无未知差异；证据写到 docs/e2e-assets/mcp-real/
```

脚本自己起停真实 server（随机端口）、生成两侧 dump、抓 raw、diff 并打印例外清单；单侧可跑：

```bash
cd core && uv run python ../go_hive/scripts/mcp_real_crosscheck_python.py \
    --servers <servers.json> --out /tmp/python-side.json --home /tmp/py-home
cd go_hive/go-hive && MCP_REAL_OUT=/tmp/go-side.json MCP_REAL_SERVERS=<servers.json> \
    go test ./internal/cli -run TestMCPRealServerDump -count=1 -v
```

Go 侧的 dump 测试在 `MCP_REAL_OUT`/`MCP_REAL_SERVERS` 未设时 `t.Skip`，不污染 `make test`。

### 8.6 证据文件

| 文件 | 内容 |
|---|---|
| `docs/e2e-assets/mcp-real/servers.json` / `call-specs.json` | 两侧共同输入的 server 条目与调用矩阵 |
| `docs/e2e-assets/mcp-real/raw/<entry>.json` | 本页自己抓的 server 原始 JSON-RPC（`tools/list`、每次 `tools/call`、SSE/HTTP 的传输元数据） |
| `docs/e2e-assets/mcp-real/python-side.json` / `go-side.json` | 两侧 dump（工具面/schema sha/调用/生命周期/崩溃/裸客户端） |
| `docs/e2e-assets/mcp-real/diff.json` | 逐维度判定 + `exceptions`（明确例外）+ `unexpected`（未知差异，本次为 0） |
| `docs/e2e-assets/mcp-real/summary.md` / `commands.md` | 人读差异表 / 全部命令与输出（含 CLI 模式） |
| `docs/e2e-assets/mcp-real/server-versions.json` | server 包与版本、node/uv 版本 |

