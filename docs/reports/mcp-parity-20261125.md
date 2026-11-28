# MCP 机制一致性核对报告（Python Hive ↔ go_hive）

- 日期：2026-11-25
- 复核：2026-11-26（w185 lane，基线 `b2728f5d9`）；本轮关闭 R3/L3（HTTP 首块非 text 的 JSON 键序）与 Go 侧 `tools/list` 分页；R4/R6 维持 w173 修复，R5 维持 `bb8cccb1a` 修复，均未回归
- 复核：2026-11-25 21:50 CST（w173 lane，基线 `fb613f42e`）；本轮关闭 R4（content 块兼容）与 R6（工具定义路径重写），R7 补齐可执行证据；R5 维持 `bb8cccb1a`（2026-11-25 14:27 +0800）的修复；`make parity` 6,744/6,744
- 初版复核：2026-11-25 14:35 CST，基线 `5486a5fab`；`make ci` RC=0，parity 6,511
- 代码基线：`b2728f5d9`（w185 起点）；上一轮 `fb613f42e`（w173 起点；初版审计基线 `59dc7157f`，2026-11-25 14:00 +0800）
- 审计范围：MCP 装载/注册、工具命名与冲突、schema 与 content 块、`tools/list` 分页、传输与协议版本、连接生命周期、配置/注册表、REST/CLI 面、MCP2CLI
- 对照实现：`core/framework/loader/**`、`core/framework/server/routes_mcp.py`（Python）↔ `go_hive/go-hive/internal/{tools,mcpregistry,cli,server}/**`（Go）
- 既有证据沿用：`docs/mcp-namespace-residuals.md`、`docs/mcp-crosscheck.md`、`docs/mcp-alias-prep.md`、`docs/mcp2cli-native.md`、`docs/alignment-differences.md` §2
- 本文补记：2026-11-25 的第二轮代码复核，以及此前未登记的 MCP 边角项（R3–R7）；2026-11-26 补 R3/L3 关闭与分页收口

> 说明：审计期间 HEAD 从 `f848b7d12` 移动到 `59dc7157f`；MCP 行为文件最后一次变更是 `f0c11b801`（2026-11-24 21:20，协议版本/SSE liveness/字典顺序/`server_tool` 收口）。w173 lane 复核时基线为 `fb613f42e`，R4/R6 的修复与回归在该 lane 内提交；本报告以当前工作树为准。

---

## 0. 结论

**默认 MCP 路径基本一致；不是 100% 逐字一致。**

| 面 | 判定 | 说明 |
|---|---|---|
| 默认工具命名 | ✅ | D3 终选 B′：默认注册 server 侧**裸名**，不折叠、不消毒，与 Python 同键 |
| 同名冲突 | ✅ | 跨 server 同名 first-wins；`preserve_existing_tools=False` 后到覆盖；13 条冻结用例默认模式全 PASS |
| 工具定义 | ✅ | description 48/48 逐字；schema 归一 48/48；raw 40/48（8 条纯表示差） |
| 工具调用 | ✅ | 真实第三方 server：everything 16/16、time 3/3；8/8 调用逐字段相等（含错误、图片、structuredContent、resource_link、log/progress 通知） |
| 生命周期 | ✅ | 连接池、健康检查、30s 重连冷却、kill -9 后恢复、disable→0 / enable 恢复 |
| 传输/协议 | ✅ | stdio / HTTP streamable / HTTP+SSE / unix；`2025-11-25` offer + server 版本校验 |
| 配置/注册表 | ✅ | `mcp_registry/{installed.json,config.json,cache/registry_index.json}` 路径与格式一致；`mcp_servers.json` 字典顺序已对齐 |
| REST 主路径 | ✅ | Python 7 条；Go 8 条 = 同 7 条 + `POST /api/mcp/reload`（Go 扩展） |
| MCP2CLI | ✅ 等价 | Go 原生实现与 mcp2cli 3.8.0 的 78 条差分用例全 PASS |
| 可选前缀模式 | ⚠ Go-only | 只有显式 `mcp.namespace` 才启用；R1/R2 |
| content 块边角 | ✅ 已对齐（R3 关闭） | `mimeType` 别名、`data` 类型、`mimeType: null` 与 Python 探针逐字一致；HTTP 首块非 text 的 JSON 键序按 server 顺序复现；`resource_link` 仍是两侧共同丢弃 |
| tools/list 分页 | ✅ Go 已跟随（R3 侧关闭） | stdio / HTTP / SSE 都跟随 `nextCursor`：首块不带 cursor、空页续读、重复 cursor 或 100 页上限停止；Python agent 客户端仍单页（mcp SDK 1.26.0），差异记入 §4 |
| server 主动请求 | ✅ 已修复（R5） | Go stdio 现回应 server 请求：`ping` 返回 `{}`，`roots/list` 返回 `ErrorData -32600`，sampling/elicitation 返回 `-32600`，未知方法 `-32602`；与 mcp 1.26.0 默认行为对齐 |
| Go 扩展面 | ✅ 有意扩展（R7 已验证） | reload、timeout 文件、`tool_names`、原生 mcp2cli、注册表权限；五项都有可执行测试证据 |
| 工具定义路径重写 | ✅ 已对齐（R6 关闭） | Go 在 LLM 边界按 Python 同一规则深度重写 `~/.hive` → `HIVE_HOME`，含嵌套参数描述 |

R4 与 R6 已在 w173 lane 关闭，R3（含 L3 与 `tools/list` 分页的 Go 侧）已在 w185 lane 关闭；实现与回归见 §3。`resource_link`、resources/prompts 仍是**两侧共同**缺口（§4），不是 Go 单侧残差；Python agent 客户端的 `tools/list` 仍是单页，属于 Go 侧主动超前的有意差异。

---

## 1. 默认路径：已对齐

### 1.1 命名与冲突（D3 B′）

- Python：`ToolRegistry._convert_mcp_tool_to_framework_tool` 的 `name=mcp_tool.name`（`core/framework/loader/tool_registry.py:1422`），server 名不进键，跨 server 同名 first-wins。
- Go：默认 `mcp.namespace` 为空，`MCPRegistryToolName` 直接返回 server 侧裸名（`internal/tools/mcp_registry.go:590-598`）；显式命名空间才走 `NamespacedMCPToolName`。
- 13 条冻结用例（`internal/tools/mcp_namespace_parity_test.go`）默认模式全 PASS：
  - 单 server `x-y` / `x_y` 两个键并存；
  - 跨 server `get-sum` / `get_sum` 并存；
  - 跨 server 同名 `echo_text` first-wins；
  - `preserve_existing_tools=False` 后到覆盖；
  - 70 字符名原样注册，无截断。

### 1.2 工具定义

- description 48/48 逐字一致；schema 归一 48/48。
- raw 40/48：8 条纯表示差（4 条数值字面拼写 + 4 条参考层 `session_cwd`），无语义差。
- 真实第三方 server：16/16、3/3 工具；`inputSchema` 的 `properties`/`required` 逐字保留，顶层信封统一为 `{type:"object", properties, required}`。

### 1.3 调用与 content 块

- 文本按 `\n` 合并；图片 → `image_url`；PDF → `file`；HTTP legacy 列表取首块 text；空列表 → `[]`；None → `null`。
- 真实 server 8 个调用逐字段相等：`echo`、`get-sum`、`get-sum-error`、`get-tiny-image`、`get-structured-content`、`get-resource-links`、`trigger-log-messages`、`trigger-long-running-operation`。
- 工具级错误：两侧都包成 `{"error": "[Server: X] [Transport: Y] Tool 'get-sum' failed: <server 原文>"}`。
- `resource_link`：两侧都静默丢弃（共同缺口）。
- `mimeType: null`：Python 生成 `data:None;base64,…`；Go 回退为文本（L2，Go 更可用，已登记）。
- HTTP 首块非 text：Go 现在按 server 返回顺序复现 JSON 对象（L3 关闭，见 §3）；文本首块仍只返回 `text` 值。

### 1.4 传输与协议版本

- transport：`stdio`、`http`（streamable）、`unix`、`sse`；Python 的 `harness` 在 Go 侧由 `HarnessGroupNames` 按 server 名分派，不是缺 case。
- 协议版本：Go 现在 offer `2025-11-25`，并校验 server 协商版本（`2024-11-05` / `2025-03-26` / `2025-06-18` / `2025-11-25`）；Python MCP SDK 1.26.0 的 `LATEST_PROTOCOL_VERSION` 也是 `2025-11-25`。
- SSE liveness：Go `realSSESession.Ping` 走同一 SSE 流；Python `probe_liveness` 用 `send_ping`，语义一致。

### 1.5 生命周期

- 连接池：Go `MCPClientPool`（Acquire/Release/HealthCheck/Reconnect/CleanupAll）；Python `MCPConnectionManager`，30s 重连冷却两侧一致。
- kill -9：真实 server 双跑中两侧都在死会话信号上重连，同一客户端再调用成功。
- disable/enable：disable 后装载 0 个工具，enable 后恢复；Python 下个会话生效，Go `Reload()` 重建连接池。
- reload：Go 有 `POST /api/mcp/reload` + generation 重挂；Python 无 reload 路由，下个 queen 会话生效（Go 扩展）。

### 1.6 配置与注册表

- 路径：`$HIVE_HOME/mcp_registry/{installed.json,config.json,cache/registry_index.json}` 两侧一致。
- 原子写：两侧都是 temp + fsync + replace；Go 目录 0700 / 文件 0600，Python 依赖 umask（Go 更严）。
- `mcp_servers.json` 字典形态：Go 现在按文件顺序遍历（`topLevelJSONKeyOrder`，`f0c11b801`），与 Python `config.items()` 的 first-wins 归属一致。
- `installed.json` / `config.json` 的字段顺序：Go 保留文件顺序；Python 保留 dict 顺序。

### 1.7 REST / CLI

- Python 7 条：list/add/remove/enable/disable/health/mode。
- Go 8 条：同 7 条 + `POST /api/mcp/reload`。
- Go `GET /api/mcp/servers` 额外返回 `tool_names`（server 侧原名）；Python 只有 `tool_count`。这是展示层扩展。
- `hive mcp` CLI：help/usage 形状、`--json`、错误文案、退出码由 `internal/mcpregistry` 的 usage/help 测试钉住。
- `hive mcp run`：Go 原生 `internal/mcpregistry/nativecli`，与 mcp2cli 3.8.0 的 78 条差分用例全 PASS；OpenAPI/GraphQL/OAuth/`--toon` 等不支持项按 `auto` 回退外部二进制，`native` 报 UnsupportedError。

---

## 2. 已登记残差（R1–R3）

### R1 — 可选前缀模式折叠不可逆（仅显式 `mcp.namespace`）

- `NamespacedMCPToolName` 把 `[^A-Za-z0-9_]+` 折叠为 `_`，`a-b` 与 `a_b` 得到同一前缀；同 server 内或跨 server 的不同 remote 工具落入同一等价类时只剩一个注册键。
- Python 默认裸名没有这个问题；差异只在主动开启前缀模式时出现。
- 证据：`internal/tools/mcp_registry.go:601-620`；`docs/mcp-namespace-residuals.md` R1。

### R2 — 可选前缀模式长名加长（仅显式 `mcp.namespace`）

- 70 字符工具名变成 `mcp__<ns>__<srv>__` + 70 = 79 字符，可能超过 OpenAI 风格 function name 的 64 字符上限。
- 默认裸名模式 70 字符原样注册，无截断。
- 证据：`docs/mcp-namespace-residuals.md` R2。

### R3 — content 块序列化与 `tools/list` 分页（已关闭 2026-11-26，w185）

- L3（HTTP 首块非 text 的 JSON 键序）已关闭：Go 现在用有序 JSON 解码复现 server 的键序，与 Python `json.dumps(result[0])` 的探针输出逐字一致（见 §3 R3）。
- L2（`mimeType: null`）随 R4 关闭：Go 现在按字段存在性走 image 分支，与 Python 探针的 `data:None;base64,…` 逐字一致（见 §3 R4）。
- `tools/list` 分页已在 Go 侧关闭：stdio / HTTP / SSE 统一跟随 `nextCursor`（首块不带 cursor、空页续读、重复 cursor 与 100 页上限停止），语义与 mcp2cli 3.8.0 一致；Python agent 客户端仍单页（见 §4）。
- `resource_link`：两类实现都只认 `text` 与 `data+mimeType`，链接信息丢失；这是两侧共同缺口，不是 Go 单侧差异（`TestMCPSplitContentResourceLinkItemDropped` / `...ExtraFieldsMatchPydantic`）。
- 证据：`docs/mcp-namespace-residuals.md` R3、`docs/mcp-crosscheck.md` §8.3。

---

## 3. 残差收口（R3/R4/R6 已关闭；R5 已修复；R7 已验证）

### R3（已关闭 2026-11-26，w185）— HTTP 首块非 text 的 JSON 键序 + `tools/list` 分页

**原差异**：

- HTTP 工具结果的 `content[0]` 在 Go 侧是 `map[string]any`（解码丢序），`PythonJSONDumps` 又按字典序输出；Python `_call_tool_http` 返回 `data["result"]["content"]`，`_wrap_result` 的 `json.dumps` 保留 server 返回的插入序。同一 JSON 对象、不同字节，模型可见文本随之不同。
- 两侧 Hive agent 客户端都只发一次 `tools/list`，不跟随 `nextCursor`；分页 server 会少工具（mcp2cli 3.8.0 与 Go native CLI 已跟随）。

**修复**：

- 新增 `internal/tools/mcp_content_order.go`：`mcpOrderedObject` 保留对象插入序，`decodeMCPOrderedJSON` 用 `json.Decoder` token 流递归解码（数字保留 `json.Number` 字面量，重复键保持首位置/末值），`mcpOrderFirstNonTextContentBlock` 只在 HTTP legacy 列表首块为**非 text 对象**时替换为有序值；文本首块、标量首块、畸形 body 原样返回。
- `internal/tools/mcp.go::PythonJSONDumps` 增加 `*mcpOrderedObject` 分支，按 Python `json.dumps` 的分隔符（`, ` / `: `）输出；普通 map 仍按字典序（负向回归钉住）。
- 新增 `internal/tools/mcp_pagination.go`：`mcpListToolsPaged` 统一游标 walk，`mcpDecodeToolsListPage` 解析一页；`mcpToolsListParams` 保证首块不带 `cursor` 字段、后续页原样回传 opaque cursor。
- stdio（`stdioMCPClient.listToolsRPC`）、HTTP（`httpMCPClient.listToolsLocked`）、SSE（`realSSESession.ListTools`）都改为共用该 walk。停止条件：`nextCursor` 缺失/null/非 string/空串、重复 cursor、或 100 页上限；任一分页请求失败则整次 `Connect` 失败，不发布部分工具面。

**回归**：

- `TestMCPHTTPFirstNonTextContentBlockPreservesKeyOrder`（逐字等于 `python-output.json` 的 `model_visible.http-image-first`）、`TestMCPHTTPFirstNonTextContentNestedOrder`（嵌套 `_meta` 顺序 + 数字字面量）、`TestMCPHTTPFirstTextContentBlockStillReturnsText`（负向）、`TestMCPPythonJSONDumpsStillSortsPlainMaps`（负向）、`TestMCPDecodeOrderedJSONKeepsFirstSlotForDuplicateKeys`、`TestMCPOrderedContentHelperLeavesUnexpectedShapesAlone`、`TestMCPOrderedContentHelperDoesNotAliasInput`。
- `TestMCPHTTPToolsListFollowsCursorsAndEmptyPages`（含空页）、`TestMCPHTTPToolsListPaginationErrorFailsConnect`（负向）、`TestMCPToolsListPagedStopsOnRepeatedCursor`（负向）、`TestMCPToolsListPagedStopsAtPageCap`、`TestMCPDecodeToolsListPageCursorSemantics`（null/number/empty/missing tools）、`TestMCPStdioToolsListFollowsNextCursor`（真实子进程 stdio 线，游标 trace 断言 `(none) → page-2 → page-3`）、`TestMCPSSEToolsListFollowsNextCursor`（本地 fake SSE server，JSON-RPC 应答走事件流）。

**验证边界**：分页用 fake HTTP 传输、假 stdio 子进程与本地 fake SSE server 验证，未接第三方分页 server。Python 侧未改（仍然是单页），所以对分页 server 而言 Go 会多装载工具——这是有意的 Go 侧超前，不是回归。

### R4（已关闭 2026-11-25，w173）— content 块兼容：`mime_type` 别名 / `data` 类型 / `mimeType: null`

**原差异**：

- Go `SplitMCPContent`（`internal/tools/mcp_client.go`）在 `mimeType` 缺失时还会尝试 `mime_type`（snake_case）；Python `_split_mcp_content`（`core/framework/loader/mcp_client.py:89-140`）只认 `mimeType`。
- Go 要求 `data` 是 string，否则视为没有 data；Python 的 data-only 分支走 `str(item.data)` 文本回退。
- Go 用字符串类型断言判断 `mimeType`，`{data, mimeType: null}` 落文本回退；Python 用 `hasattr`，落 image 分支。

**修复**（`internal/tools/mcp_client.go::SplitMCPContent`）：

- 只认 camelCase `mimeType`，删除 `mime_type` 别名。
- 改为字段存在性判断（对应 Python `hasattr`）：同时有 `data` 与 `mimeType` 即 image/file 分支，`mimeType: null` 也走该分支。
- 新增 `mcpContentString`，按 Python `str()` 的拼写渲染非 string 值（`None` / `True` / `False` / 数字），用于 `data` 与 `mimeType`。
- `resource_link` 不特殊处理：合法块没有 `text`/`data`，与 Python 一样丢弃；SDK 的 `extra="allow"` 行为（额外 `text`/`data` 字段）由 key-presence 判断原样复现。

**与探针逐字对齐**（`go_hive/docs/e2e-assets/mcp-namespace/python-output.json`）：

| 输入 | Python | Go（本轮后） |
|---|---|---|
| `{data:"AAA", mimeType:null}` | `data:None;base64,AAA` | 同 |
| `{data:"AAA"}`（无 mimeType） | 文本 `AAA` | 同（number/bool/null 用 Python `str()` 拼写） |
| 合法 `resource_link` | 丢弃 | 丢弃 |

**影响**：只影响非标准/畸形 payload；合规 server 使用 string `data` + `mimeType`，两种实现输出相同。`data:None` 是 Python 探针的真实行为；注意 mcp 1.26.0 的 `ImageContent.mimeType` 是 `str`，畸形值会在 SDK 解析阶段先失败，所以该分支只在直接调用分块函数（或 Go 的非类型化解码路径）可见。

**回归**：`TestMCPSplitContentNullMimeTypeMatchesPython`、`TestMCPSplitContentMimeTypeAliasNotRecognised`、`TestMCPSplitContentNonStringDataUsesPythonStr`、`TestMCPSplitContentResourceLinkItemDropped`、`TestMCPSplitContentResourceLinkExtraFieldsMatchPydantic`。

### R5（已修复 2026-11-25）— server 主动请求（ping / roots/list / sampling）：Go 无应答路径

**原差异（2026-11-25 14:00 审计时）**

- Python MCP SDK 的 `ClientSession` 会回应 server 发起的请求：默认 sampling/elicitation 回调返回 `ErrorData`，roots/list 走默认回调，`ping` 由 BaseSession 处理。
- Go 的 stdio `readLoop`（`internal/tools/mcp_client.go`）当时只把带 id 的帧当**响应**查 pending；没有 pending 的 server request 被丢弃，不回错误。HTTP/SSE 客户端同样是请求-响应模型。

**修复（`bb8cccb1a`，2026-11-25 14:27 +0800）**

- stdio `readLoop` 先按 `method` 字段区分 server request 与 response；有 `method` 的帧进入 `respondToServerRequest`，无 `method` 的仍在 pending 表中匹配响应。
- 应答形状对齐已安装的 `mcp` 1.26.0 `ClientSession._received_request` / `BaseSession` 校验，而不是猜测：
  - `ping` → `result: {}`（EmptyResult）；
  - `roots/list` → `error: {code: -32600, message: "List roots not supported"}`（默认无 `list_roots_callback` 时 SDK 的 ErrorData）；
  - `sampling/createMessage` → `{code: -32600, message: "Sampling not supported"}`；
  - `elicitation/create` → `{code: -32600, message: "Elicitation not supported"}`；
  - 其他方法（不在 `types.ServerRequest` 内）→ `{code: -32602, message: "Invalid request parameters", data: ""}`。
- 回归（`internal/tools/mcp_server_requests_test.go`）：
  - `TestMCPStdioAnswersServerRequests`：直接断言五类应答的 id 回显与 error/result 形状；
  - `TestMCPStdioReadLoopDistinguishesServerRequests`：readLoop 不把 server request 当响应；
  - `TestMCPStdioServerRequestsEndToEnd`：拉起真实子进程 stdio fixture（`os.Args[0]` + `TestMCPStdioServerRequestProcess`），server 在握手阶段发请求（含复用 initialize id 的 ping），随后 `tools/call` 仍成功；
  - `TestMCPStdioServerRequestProcess`：子进程 fixture 本体。

**验证边界**：这里的“真实子进程”是测试二进制拉起的 fake stdio server，不是第三方 MCP server；真实第三方 server 主动发 `ping`/`roots/list` 仍未验证。HTTP/SSE 客户端仍是请求-响应模型，本次只修 stdio。

**复查条件**：真实 server 发出 ping/roots/list；或 MCP 客户端 capability 协商变更。

### R6（已关闭 2026-11-25，w173）— 工具定义路径重写（LLM 边界）

**原差异**：Python 在 `_tool_to_openai_format` 里对 OpenAI function 字典做 `resolve_hive_paths_deep`，把 `~/.hive` 换成实际 `HIVE_HOME`（`core/framework/llm/litellm.py:2257`）；Go 没有等价重写，模型看到的描述保留 `~/.hive` 字面量。

**修复**：

- 新增 `internal/llm/tool_descriptions.go`：`toolDescriptionHiveHome`（测试可注入）+ `resolveHivePathsDeep`。
- `internal/llm/provider.go::ToolToOpenAIFormat` 在返回前调用 `toolDefinitionForRequest`，与 Python 同一位置（OpenAI function 投影）重写 `name` / `description` / 嵌套 `parameters` 的每个字符串值。
- 规则与 `framework/config.py::resolve_hive_paths_in_text` 一致：`strings.ReplaceAll(text, "~/.hive", HIVE_HOME)`；只改字符串**值**、不改 map key，返回新容器（不原地改 registry 的 `ToolDef`）。
- `HIVE_HOME` 解析失败时原样返回，绝不把字面量替换成空串。
- 不在其他边界重复实现：OpenRouter tool-compat prompt 与 Python `_build_openrouter_tool_compat_messages` 一样保留原始 description；Antigravity/Claude Code 等自有 provider 也不重写（Python 只在 litellm 边界做）。

**回归**：`TestToolToOpenAIFormatRewritesHiveHomeInDescriptions`（home 含空格 + 中文）、`...UsesPlainSubstringReplacement`、`...LeavesOtherTextAlone`（负向）、`...SkipsRewriteWhenHomeUnresolved`（负向）、`...DoesNotMutateDefinition`、`TestBuildCompletionKwargsRewritesToolDescriptions`（complete + stream 两条请求路径）、`TestResolveHivePathsDeepLeavesKeysAndNonStringsAlone`。

**验证边界**：只验证 LLM 请求载荷（`buildCompletionKwargs` / `completionRequestKwargs`），不验证第三方 provider 的端到端请求；`internal/cli/tool_contract_parity_test.go` 的契约面没有 `~/.hive` 字符串，不受影响。

### R7 — Go 扩展面（有意保留，已验证）

| 扩展 | Go 实现 | 验证（本轮实跑） | 保留理由 |
|---|---|---|---|
| reload | `POST /api/mcp/reload`，generation 重挂（`internal/server/routes_mcp.go`） | `TestMCPReloadRouteReloadsLoader`、`TestMCPReloadRouteReportsError`、`TestMCPReloadRouteWithoutLoader` | 热重载可用性 |
| timeout 文件 | env > `configuration.json mcp.timeout_seconds` > 240s（`internal/tools/mcp_client.go::MCPCallResultTimeout`） | `TestMCPCallResultTimeoutPrecedence`、`TestSetMCPCallResultTimeoutOverrides` | 运维可配置 |
| status `tool_names` | `GET /api/mcp/servers` 返回 server 侧工具名 | `TestMCPListServersShowsConnectionStatus` | UI 展示 |
| native mcp2cli | `internal/mcpregistry/nativecli` 内置实现 | `go test ./internal/mcpregistry/nativecli`（78 条差分用例） | 无 Python 依赖 |
| 注册表权限 | 目录 0700、文件 0600 + 原子写（`internal/mcpregistry/registry.go::bootstrapIO`） | `TestInitUsesPrivatePermissions`（本轮新增） | 安全加固 |

R7 不是缺口：五项都是 Go 侧主动实现，Python 没有对应物。本轮把此前只有散文说明的“权限”扩展补成可执行断言，其余四项沿用既有测试并在 §5 的命令里实跑。

---

## 4. 共同缺口与 Go 侧超前

- **`resource_link` 丢弃**：两侧只认 `text` 与 `data+mimeType`；合法 `resource_link`（只有 `uri`/`name`/`mimeType`）两侧都丢弃。Go 侧回归：`TestMCPSplitContentResourceLinkItemDropped`、`TestMCPSplitContentResourceLinkExtraFieldsMatchPydantic`（后者钉住 pydantic `extra="allow"` 的字段行为）。
- **MCP 资源/提示（resources/prompts）**：两侧 agent 客户端都只暴露 tools——Python `_discover_tools` 只发 `tools/list`（`core/framework/loader/mcp_client.py:591-663`），Go 客户端也只发 `tools/list`；两边都没有 `resources/list`、`resources/read`、`prompts/list`、`prompts/get` 路径。mcp2cli 原生 CLI 支持 resources/prompts，但不在 agent 工具面。**这是两侧共同缺口，本轮不伪造支持。**
- **sampling/elicitation**：两侧 Hive 都不提供回调；Python SDK 默认返回 “not supported” ErrorData，Go 现在同样回 `-32600` ErrorData（R5），这不等于提供采样/elicitation 能力。
- **`tools/list` 分页（仅剩 Python 侧）**：Go 客户端已跟随 `nextCursor`（R3），Python agent 客户端仍只发一页（mcp SDK 1.26.0 的 `ClientSession.list_tools()` 单页，`_discover_tools` 不循环）。对分页 server，Go 装载更全；若要两侧逐字一致，应改 Python 侧，而不是回退 Go。

---

## 5. 本轮验证

```bash
cd go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/tools -run MCP -count=1
# ok github.com/shark2202/go_hive/go-hive/internal/tools 1.640s

CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/mcpregistry -count=1
# ok github.com/shark2202/go_hive/go-hive/internal/mcpregistry 3.376s

CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/server -run MCP -count=1
# ok github.com/shark2202/go_hive/go-hive/internal/server 0.668s

CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/cli -run MCP -count=1
# ok github.com/shark2202/go_hive/go-hive/internal/cli 0.763s
```

2026-11-25 14:35 CST 复核（基线 `5486a5fab`）补跑：

```bash
cd go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/tools -run 'MCPStdio' -count=1
# ok github.com/shark2202/go_hive/go-hive/internal/tools

cd /Users/mac/hive/go_hive/go-hive && make ci
# RC=0；parity 6,511
```

2026-11-25 21:50 CST（w173 lane，基线 `fb613f42e`）复跑：

```bash
cd go_hive/go-hive
CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off \
  go test ./internal/mcpregistry ./internal/tools ./internal/llm -count=1
# ok .../internal/mcpregistry 3.009s
# ok .../internal/tools 2.974s
# ok .../internal/llm 20.034s

make parity
# check_parity: OK — 6,744 个测试全部带有 parity 注解或豁免说明

gofmt -l .                                     # 无输出，exit 0
go vet ./internal/mcpregistry ./internal/tools ./internal/llm   # exit 0

make compat-check
# pass=13 fail=0 skip=0；C12 273 registered / 228 ported / 27 partial / 8 oos / 10 unported
```

2026-11-26（w185 lane，基线 `b2728f5d9`）复跑：

```bash
cd go_hive/go-hive
CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off \
  go test ./internal/mcpregistry ./internal/tools ./internal/llm -count=1
# ok .../internal/mcpregistry 3.045s
# ok .../internal/tools 2.610s
# ok .../internal/llm 20.752s

make parity
# check_parity: OK — 6,836 个测试全部带有 parity 注解或豁免说明

gofmt -l .                                                    # 无输出，exit 0
go vet ./internal/mcpregistry ./internal/tools ./internal/llm # exit 0
CGO_ENABLED=0 go test ./internal/quality -count=1             # ok（文件 ≤1500 行、函数 ≤200 行）
make parity-targets                                           # 7,930 refs，0 missing
make compat-check                                             # pass=13 fail=0 skip=0
make arch-check                                               # 7 个生成物与 model.yaml + go list 一致
```

C12 登记不需要变更：`core/framework/loader/mcp_client.py`、`tool_registry.py` 与 `core/framework/llm/litellm.py` 已是 `ported`，本轮新增/修改的文件都落在既有 ported 面内；`make compat-check` 复验 273/273 注册、C12 PASS。

既有证据：

- 默认命名 13 用例：`internal/tools/mcp_namespace_parity_test.go`
- R3 键序与分页：`internal/tools/mcp_content_order_test.go`、`internal/tools/mcp_pagination_test.go`
- 真实第三方 server 双跑：`docs/e2e-assets/mcp-real/**`（16/16、3/3 工具；8/8 调用；kill 恢复）
- mcp2cli 差分：`internal/mcpregistry/nativecli` 78 用例
- 工具契约：`docs/tool-contract-diff.md`（48/48 description、schema 归一 48/48）

**未跑**：真实第三方 server 的 ping/roots/list 主动请求（R5 只用 fake/子进程 fixture 验证）；真实第三方**分页** server（R3 分页用 fake HTTP 传输、假 stdio 子进程与本地 fake SSE server 验证）；Windows 实机；91 个第三方集成（无凭据）。全量 `make ci` 已在 `5486a5fab` 跑过（RC=0，parity 6,511）。

---

## 6. 建议

1. **R3（已完成，2026-11-26）**：HTTP 首块非 text 的键序已按 server 顺序复现；`tools/list` 分页已在 Go 客户端（stdio/HTTP/SSE）跟随 `nextCursor`，负向用例钉住重复 cursor 与分页错误。
2. **R4（已完成）**：content 块已与 Python 探针逐字对齐，`mime_type` 别名删除；剩余共同缺口只有 `resource_link` 丢弃（两侧一致）。
3. **R6（已完成）**：Go 在 `ToolToOpenAIFormat` 做同样的 `~/.hive` 深度重写；新测试覆盖含空格/Unicode 的 home、无法解析的 home、非 string 标量与负向回归。
4. **共同缺口（需两侧一起改）**：`resource_link` 与 resources/prompts。`resources/prompts` 若要做，需要同时扩展 agent 工具面与 registry schema，不能只加 Go 客户端。`tools/list` 分页若要两侧逐字一致，应改 Python `_discover_tools` 跟随 cursor（Go 侧已就绪）。
5. **R5 验证边界**：真实第三方 server 主动 `ping`/`roots/list` 仍未验证（只有 fake + 子进程 fixture）。
6. **R7**：五项扩展均已有可执行测试；仍建议把 reload、timeout 文件、`tool_names`、native mcp2cli 写进用户文档，避免被误读为缺失。

---

## 7. 证据索引

| 主题 | Python | Go |
|---|---|---|
| MCP 客户端 | `core/framework/loader/mcp_client.py` | `internal/tools/mcp_client.go`、`mcp_sse.go` |
| 连接池 | `core/framework/loader/mcp_connection_manager.py` | `internal/tools/mcp_connection_manager.go` |
| 注册/命名 | `core/framework/loader/tool_registry.py` | `internal/tools/mcp_registry.go` |
| 配置装载 | `core/framework/loader/mcp_registry.py` | `internal/mcpregistry/*`、`internal/cli/mcp_loader.go`、`mcp_config.go` |
| REST | `core/framework/server/routes_mcp.py` | `internal/server/routes_mcp.go` |
| MCP2CLI | 外部 `mcp2cli` 3.8.0 | `internal/mcpregistry/nativecli` |
| 内容块 | `mcp_client.py:89-140` | `mcp_client.go::SplitMCPContent`；`mcp_namespace_parity_test.go` |
| HTTP 首块键序 | `tool_registry.py::_wrap_result`（`json.dumps` 保留插入序） | `internal/tools/mcp_content_order.go`；`mcp_content_order_test.go`；探针 `docs/e2e-assets/mcp-namespace/python-output.json` |
| tools/list 分页 | SDK `ClientSession.list_tools()`（单页，未跟随） | `internal/tools/mcp_pagination.go`；`mcp_pagination_test.go`；native CLI `internal/mcpregistry/nativecli/jsonrpc.go::listTools` |
| 协议版本 | MCP SDK 1.26.0 | `mcp_client.go:70-100` |
| 命名空间 | 无（裸名） | `mcp_registry.go:590-620` |
| server 主动请求 | SDK `ClientSession`（mcp 1.26.0） | `mcp_client.go` 的 `respondToServerRequest`；`mcp_server_requests_test.go` |
| 路径重写 | `core/framework/llm/litellm.py:2257` | `internal/llm/tool_descriptions.go`；`provider.go::ToolToOpenAIFormat`；`tool_descriptions_test.go` |
