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
- MCP 调用结果的内容聚合（Python 把多个 text content 用 `\n` 连接并保留 image 块；Go 取第一个 content 的 text）属于工具结果面，本核对只验证「注册给模型的定义」，结果面未做 golden 对照。
