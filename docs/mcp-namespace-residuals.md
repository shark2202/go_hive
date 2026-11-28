# MCP 命名空间折叠 / 长名 / 多 content 块：Python ↔ Go 实测残余

基线 `247c10da`（worktree `.wt/mcp-ns`，分支 `mcp-ns`）首次完成本页的 Python ↔ Go
对照。**D3 终选 B′（2026-11-23，worktree `.wt/mcp-bare-default`，分支
`mcp-bare-default`）后，默认注册改为 server 侧裸名：R1/R2 在默认模式下不再成立，
只保留为显式 `mcp.namespace` 前缀模式的可选行为。** 本页同时记录两种模式的实测
口径；13 条冻结用例（`mcp_namespace_parity_test.go`）的默认模式断言全部与 Python
一致。

证据：

| 文件 | 内容 |
|---|---|
| `go_hive/docs/e2e-assets/mcp-namespace/probe_python.py` | Python 探针：假 MCP client 驱动 `ToolRegistry.register_mcp_server`，并直接调用 `_split_mcp_content` / `_clean_tool_name` |
| `go_hive/docs/e2e-assets/mcp-namespace/python-output.json` | 上述探针的实跑输出（本页 Python 列的全部数值） |
| `go_hive/go-hive/internal/tools/mcp_namespace_parity_test.go` | Go 侧钉子：默认裸名用例（`TestMCPBareDefault*`）+ 可选前缀模式用例（`TestMCPNamespace*`），逐条带 `// Parity:` / `// Parity-exempt:` |
| `go_hive/docs/e2e-assets/mcp-bare-default/REPORT.md` | D3 B′ 的 13 用例实测、模式矩阵、门禁输出与回滚 |

## 0. 结论

1. **Python 没有 MCP 工具命名空间/前缀折叠。** 注册键就是 server 侧裸
   `mcp_tool.name`（`core/framework/loader/tool_registry.py:1053`，转换器
   `:1422`）；server 名不进键，跨 server 同名工具 first-wins（`:993`，
   `core/tests/test_mcp_registry_loader.py::test_registry_first_wins_collisions`）。
   唯一的“折叠”在 provider 边界：`core/framework/llm/antigravity.py:248`
   的 `_clean_tool_name`（`[/\s]`→`_`、非字母开头补 `_`、截 64），只服务
   Antigravity 的 function-declaration schema。
2. **Go 默认（D3 终选 B′）与 Python 同键**：`mcp.namespace` 默认空，注册名 =
   server 侧原始工具名，**不折叠、不消毒**。`x-y` 与 `x_y` 是两个可调用键，
   `a-b`/`a_b` 两个 server 的 `get-sum`/`get_sum` 并存，70 字符名原样 70 字符。
   13 条冻结用例的默认模式断言全部 PASS（§1.1）。
3. **可选前缀模式是 Go-only 扩展**：仅当 `configuration.json mcp.namespace`
   显式非空时启用 `mcp__<namespace>__<server>__<tool>`，
   `[^A-Za-z0-9_]`→`_`（`go_hive/go-hive/internal/tools/mcp_registry.go:614`
   `NamespacedMCPToolName`，契约见 `go_hive/docs/architecture/contracts.md`
   `tool-schema.mcp-namespace`）。该模式的折叠不可逆（R1）与长名前缀加长（R2）
   是本页保留的残余，只影响主动开启前缀模式的部署；裸名回退别名
   （`mcpAliases`）也只在该模式有意义。
4. **多 content 块聚合两侧逐项一致**：stdio/SSE 文本按 `\n` 合并、图片→
   `image_url`、PDF→`file`、HTTP legacy 列表取首块 `text`、空列表→`[]`、
   None→`null`。R4 关闭后 `mime_type` 别名删除，`data` 类型与
   `mimeType: null` 也按 Python 探针逐字复现；R3 关闭后 HTTP 首块非 text
   的 JSON 键序也按 server 顺序输出。剩余仅 `resource_link` 两侧共同丢弃
   （已登记于 `mcp-crosscheck.md` §8.3 第 2 条）。

## 1. 默认（裸名）模式：13 条冻结用例

### 1.1 命名（5 条，全部与 Python 一致）

| # | 输入 | Python 实跑 | Go 默认（裸名）实跑 | 判定 |
|---|---|---|---|---|
| 1 | 单 server 暴露 `x-y` + `x_y` | 注册键 2 个（`x-y`、`x_y`），都可调用；probe `collision_tool_names_in_one_server` | 注册键 2 个，`ToolsLoaded=2`，`x-y`→`fold:x-y`、`x_y`→`fold:x_y`（`TestMCPBareDefaultKeepsDistinctToolNames`） | **一致** |
| 2 | server `a-b` 暴露 `get-sum` + server `a_b` 暴露 `get_sum` | 2 个 server 各 1 工具，裸键 `get-sum`/`get_sum` 并存；probe `collision_cross_server` | 2 个裸键并存，调用分别落 `a-b:get-sum` / `a_b:get_sum`（`TestMCPBareDefaultCrossServerNamesSurvive`） | **一致** |
| 3 | 两个 server 暴露同名工具 `echo_text`（`preserve_existing_tools=True`） | first-wins：`a-b` 拥有 `echo_text`，`a_b` 的 `server_tools` 只剩 `only_b`；probe `collision_cross_server` | first-wins：`a_b` 的 `GetServerToolNames` 为 0，调用落 `a-b:echo_text`（`TestMCPBareDefaultSameRemoteNameFirstWins`） | **一致**（同为裸键 first-wins） |
| 4 | 同名工具 + `preserve_existing_tools=False` | 后到覆盖：调用 `echo_text` 落 `a_b:echo_text`；probe `collision_call_echo_text_preserve_false` | 后到覆盖：注册键只剩 1 个 `echo_text`，调用落 `a_b:echo_text`（`TestMCPBareDefaultPreserveFalseOverwrites`） | **一致 · L2** |
| 5 | 70 字符工具名（全合法字符） | 注册键原样 70 字符，无截断；probe `long_name.registered_key` / `registered_len == 70` | 注册键原样 70 字符，无前缀、无截断（`TestMCPBareDefaultLongNameRegisteredVerbatim`） | **一致** |

补充钉子（不属于 13 用例计数）：

| 用例 | 钉住的行为 |
|---|---|
| `TestMCPRegistryToolNameModeMatrix` | 两种模式的映射表：`("",…,x-y)`→`x-y`、`("mcp",a-b,x-y)`→`mcp__a_b__x_y`、`("remote",custom,ping)`→`remote__custom__ping` |
| `TestMCPBareDefaultAliasesStayIdentity` | 裸名模式 `mcpAliases` 为空（恒等），`x_y` 不会借折叠别名打到 `x-y` |
| `TestMCPBareDefaultProbeAndKillUseBareKeys` | 探活/断连用裸键解析，`ProbeForTool("echo_text")=="alive"`、`KillForTool` 断到同一 client |

### 1.2 多 content 块（5 条 split + 3 条 executor，全部一致；与 D3 无关）

`_split_mcp_content`（Python，`core/framework/loader/mcp_client.py:89`） vs
`SplitMCPContent`（Go，`go_hive/go-hive/internal/tools/mcp_client.go::SplitMCPContent`）：

| # | 输入块 | Python 输出 | Go 输出 | 判定 |
|---|---|---|---|---|
| 6 | 仅 `data`（无 mimeType） | 当作文本原样 `"raw-blob"` | 同（`TestMCPSplitContentDataOnlyItemBecomesText`） | 一致 |
| 7 | 空 `mimeType`（`""`） | `image_url` `data:;base64,AAA` | 同（`TestMCPSplitContentEmptyMimeTypeStaysImageURL`） | 一致 |
| 8 | `mimeType: null` | image 分支 → `data:None;base64,AAA`（不可转换的 data URL） | 同（`TestMCPSplitContentNullMimeTypeMatchesPython`） | 一致（R4 关闭） |
| 9 | `resource_link`（只有 `uri/name/mimeType`） | 丢弃 | 丢弃（`TestMCPSplitContentResourceLinkItemDropped`） | 一致（共同缺口） |
| 10 | 未知对象（无 text/data） | 丢弃 | 丢弃（`TestMCPSplitContentUnknownItemDropped`） | 一致 |
| 11 | HTTP legacy 列表 `[text a, text b]` | `content="a"`（首块 text） | 同（`TestMCPExecutorLegacyContentListFirstTextBlock`） | 一致 |
| 12 | HTTP 空列表 / `None` | `"[]"` / `"null"` | 同（`TestMCPExecutorLegacyContentListEmptyAndNil`） | 一致 |
| 13 | HTTP 首块非 text（image dict） | `{"type": "image", "data": "AAA", "mimeType": "image/png"}` | 同（`TestMCPHTTPFirstNonTextContentBlockPreservesKeyOrder`；executor 形状由 `TestMCPExecutorLegacyContentListFirstNonTextBlock` 钉住） | 一致（R3 关闭） |

同一批探针里另有两侧一致的扩展行（保留作证据，不重复计数）：两个 text 块
`hello\nworld`；text+image → `image_url`；PDF + `_meta.filename` / 大写 mime →
`file` 块；混合 text/image/resource_link/text/pdf → `"a\nb"` + `image_url` +
`file`；HTTP `[text caption, image]` → `caption`。

## 2. 可选前缀模式（显式 `mcp.namespace` 非空）：原 R1/R2

只有显式配置时才走这组行为，与旧版逐字一致（`go_hive/docs/mcp-alias-prep.md` 的
别名回退也在该模式生效）。

| # | 输入 | Python 实跑 | Go 前缀模式实跑 | 判定 |
|---|---|---|---|---|
| P1 | 单 server 暴露 `x-y` + `x_y` | 2 个裸键 | 折叠成 1 个 `mcp__fold__x_y`；first-wins，`ToolsLoaded=1`，存活执行器调 remote `x-y`（`TestMCPNamespaceFoldCollisionKeepsFirstTool`） | 差异 · L1（仅前缀模式） |
| P2 | server `a-b`/`get-sum` + `a_b`/`get_sum` | 2 个裸键 | 两个前缀都折叠成 `mcp__a_b__get_sum`；第二个 server `ToolsLoaded=0`（`TestMCPNamespacePrefixFoldCollidesAcrossServers`） | 差异 · L1（仅前缀模式） |
| P3 | 两个 server 同名 `echo_text`（preserve=True） | first-wins | first-wins：`mcp__a_b__echo_text` 被 `a-b` 先占后跳过（`TestMCPNamespaceSameRemoteNameFirstWins`） | 一致（机制不同：裸名 vs 命名空间键） |
| P4 | 同名 + preserve=False | 后到覆盖 → `a_b:echo_text` | 后到覆盖 → `a_b:echo_text`（`TestMCPNamespaceFoldOverwriteWhenNotPreserving`） | 一致 · L2 |
| P5 | 70 字符工具名 | 70 字符 | 注册键 `mcp__srv__` + 70 = 79 字符，无截断（`TestMCPNamespaceLongToolNameNotTruncated`） | 差异 · L1（前缀加长） |

前缀模式折叠函数本身（`NamespacedMCPToolName`，Python 无对应物）：

| 输入 `(namespace, server, tool)` | Go 输出 |
|---|---|
| `("mcp", "a-b", "get-sum")` | `mcp__a_b__get_sum` |
| `("mcp", "a_b", "get_sum")` | `mcp__a_b__get_sum` |
| `("mcp", "a-b", "x-y")` / `("mcp", "a-b", "x_y")` | `mcp__a_b__x_y`（同一键） |
| `("", "", "")` | `mcp__server__tool` |
| `("MCP Tools", "a/b", "c:d")` | `MCP_Tools__a_b__c_d` |

别名回退（前缀模式）：`TestMCPNamespacePrefixModeAliasFallbackStillResolves` 钉住
裸名 `echo_text` 仍可解析到 `mcp__srv__echo_text`；裸名模式该映射恒等、不记录
（`TestMCPBareDefaultAliasesStayIdentity`）。

## 3. 残余登记

### R1 · 前缀模式的命名空间折叠不可逆（`a-b` vs `a_b`）—— 仅可选模式

- **状态（2026-11-23）**：**默认模式已一致，不再是残余**。只有显式
  `mcp.namespace` 非空时，`NamespacedMCPToolName` 才把 server/tool 中所有
  `[^A-Za-z0-9_]+` 折叠为 `_`、合并连续非法字符、并 `Trim(_, "_")`。等价类包括
  `a-b`/`a_b`、`-a-`/`a`/`_a_`、`a--b`/`a-b`/`a_b`；同一 server 内或跨 server
  的不同 remote 工具一旦落进同一等价类，就只剩一个注册键（preserve=True 时后到
  者被跳过，False 时后到者覆盖执行器）。
- **与 Python 的差异**：Python 键是裸名，`x-y` 与 `x_y` 是两个可调用工具；只有
  裸名相同才 first-wins。该差异只在操作者主动开启前缀模式时出现（P1/P2）。
- **不修原因**：折叠字符集是可选 Go 扩展的既定契约
  （`docs/architecture/contracts.md` `tool-schema.mcp-namespace`）；默认路径已
  与 Python 同键，无需再改。若未来要求前缀模式无损，需要一次**破坏性改名**：
  对非法字符做可逆转义（如 `-`→`_2d_`）并同步重编码 `_` 以保持单射，同时更新
  `contracts.md`、工具面 digest、`mcpToolNames` 反查与所有钉住名字的测试。
- **建议**：保持现状并把“前缀模式可能丢工具”写进配置说明（本页 + R1）；若要在
  前缀模式内观测同 server 折叠冲突，可在 `LogCollisions` 打开时补一条显式
  warning（只加日志，不动名字）。

### R2 · 前缀模式的长名可能超过 provider 的 64 字符上限 —— 仅可选模式

- **状态（2026-11-23）**：**默认模式已一致**——裸名 70 字符两侧都原样注册、
  无截断（用例 5）。只有前缀模式在名字前加
  `mcp__<namespace>__<server>__`（默认 5 + server 折叠名长度），70 字符工具名
  变成 79 字符，可能超过 OpenAI 风格 function name 的 64 字符上限。
- **上游不一致**：Python 只在 Antigravity 边界截断到 64，且截断会制造新的同名
  （前 64 字符相同的两个长名）；Go 没有 Antigravity provider，因此没有可对照的
  截断实现。
- **不修原因**：注册层截断会改工具面键与 sha；在 provider 层截断则要按模型能力
  区分，两侧都无现成语义可抄。
- **建议**：前缀模式部署时对 `len(prefix)+len(tool) > 64` 给出显式 warning；
  provider 侧截断/哈希是独立决策，两侧一起改并更新 digest。

### R3 · 多 content 块：L3 键序与 `tools/list` 分页（已关闭 2026-11-26）

- **聚合语义已一致**（§1.2）：文本 join、图片/PDF 块、HTTP legacy 列表首块、
  空列表/None 全部对齐。
- **L3 键序（#13，已关闭）**：原差异是 HTTP 路径的 `content` 数组在 Go 侧是
  `map[string]any`（解码丢序），`PythonJSONDumps` 又按字典序输出；Python
  `json.dumps` 保留 server 返回的插入序。现在
  `internal/tools/mcp_content_order.go` 用 `json.Decoder` token 流把首块非
  text 对象重解为 `mcpOrderedObject`，`PythonJSONDumps` 按插入序渲染
  （`, ` / `: `），与探针 `model_visible.http-image-first` 逐字一致。文本首块
  / 标量首块 / 畸形 body 保持原路径；普通 map 仍按字典序（负向回归）。
- **`tools/list` 分页（Go 侧已关闭）**：`internal/tools/mcp_pagination.go` 的
  `mcpListToolsPaged` 被 stdio / HTTP / SSE 共用：首块不带 cursor、后续页
  原样回传 `nextCursor`、空页继续、重复 cursor 或 100 页上限停止；任一分页
  失败则整次 `Connect` 失败。Python agent 客户端仍是单页
  （mcp SDK 1.26.0 `ClientSession.list_tools()`），属于 Go 侧超前。
- **`mimeType: null`（#8，已关闭）**：Python 的 `hasattr(item, "mimeType")`
  让 `{data, mimeType: null}` 进 image 分支并生成 `data:None;base64,…`；Go
  现在同样按字段存在性判断。注意 mcp 1.26.0 的 `ImageContent.mimeType` 是
  `str`，真实 SDK 会在解析阶段拒绝 null，所以该分支只在直接调用分块函数
  （或 Go 的非类型化解码路径）可见。
- **`resource_link`（#9，共同缺口）**：两侧都丢弃，链接信息丢失；已在
  `mcp-crosscheck.md` §8.3 第 2 条登记，单侧修改会造成新的两侧不一致。Go 侧
  另钉住 pydantic `extra="allow"` 的字段行为
  （`TestMCPSplitContentResourceLinkExtraFieldsMatchPydantic`）。

## 4. 复现

```bash
# Python 侧（输出即 e2e-assets/mcp-namespace/python-output.json）
cd .wt/mcp-ns/core
uv run python ../go_hive/docs/e2e-assets/mcp-namespace/probe_python.py \
  > ../go_hive/docs/e2e-assets/mcp-namespace/python-output.json

# Go 侧：默认裸名（13 用例）+ 可选前缀模式
cd .wt/mcp-bare-default/go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/tools -run \
  'TestMCPBareDefault|TestMCPRegistryToolNameModeMatrix|TestMCPNamespace|TestMCPExecutorLegacy|TestMCPSplitContent' \
  -count=1 -v
```

Python 探针不启动任何 MCP 服务：它用一个假的 `MCPClient` 驱动
`ToolRegistry.register_mcp_server` 的真实注册/执行路径，并直接调用
`_split_mcp_content`、`_clean_tool_name` 两个纯函数。
