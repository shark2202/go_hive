# MCP 命名空间折叠 / 长名 / 多 content 块：Python ↔ Go 实测残余

基线 `247c10da`（worktree `.wt/mcp-ns`，分支 `mcp-ns`）。本页是
`go_hive/docs/mcp-crosscheck.md` §3.1 / §8.3 已登记残余的下钻复算：先跑
Python 参考侧的纯函数与真实注册路径（in-process，不启服务），再用同一批
输入跑 Go 侧等价实现，逐项判定 **L1 形状 / L2 值 / L3 字节**。

证据：

| 文件 | 内容 |
|---|---|
| `go_hive/docs/e2e-assets/mcp-namespace/probe_python.py` | Python 探针：假 MCP client 驱动 `ToolRegistry.register_mcp_server`，并直接调用 `_split_mcp_content` / `_clean_tool_name` |
| `go_hive/docs/e2e-assets/mcp-namespace/python-output.json` | 上述探针的实跑输出（本页 Python 列的全部数值） |
| `go_hive/go-hive/internal/tools/mcp_namespace_parity_test.go` | Go 侧等价值/形状的钉子（`// Parity:` / `// Parity-exempt:` 注解逐条给出 Python 锚点） |

## 0. 结论

1. **Python 没有 MCP 工具命名空间/前缀折叠。** 注册键就是 server 侧裸
   `mcp_tool.name`（`core/framework/loader/tool_registry.py:1053`，转换器
   `:1422`）；server 名不进键，跨 server 同名工具 first-wins（`:993`，
   `core/tests/test_mcp_registry_loader.py::test_registry_first_wins_collisions`）。
   唯一的“折叠”在 provider 边界：`core/framework/llm/antigravity.py:248`
   的 `_clean_tool_name`（`[/\s]`→`_`、非字母开头补 `_`、截 64），只服务
   Antigravity 的 function-declaration schema。
2. **Go 的 `mcp__<namespace>__<server>__<tool>` + `[^A-Za-z0-9_]`→`_` 是
   Go 扩展**（`go_hive/go-hive/internal/tools/mcp_registry.go:553`，冻结于
   `go_hive/docs/architecture/contracts.md:378-381`）。折叠不可逆，于是
   `a-b`/`a_b`、`x-y`/`x_y` 在 Go 侧撞成一个注册键并按 first-wins 丢掉一个
   工具；Python 侧两个裸名各自可调用。
3. **多 content 块聚合两侧逐项一致**：stdio/SSE 文本按 `\n` 合并、图片→
   `image_url`、PDF→`file`、HTTP legacy 列表取首块 `text`、空列表→`[]`、
   None→`null`。剩余仅 (a) 首块非 text 的 JSON 键序（L3）与 (b)
   `resource_link` 两侧共同丢弃（已登记于 `mcp-crosscheck.md` §8.3 第 2 条）。
4. **本轮没有代码语义修改**：三组用例里没有“既与 Python 有差异、又属于
   bug、且能在不改工具面 sha 的前提下安全对齐”的项。折叠与长名是 Go 扩展
   自身的边界，按任务要求登记为 R1/R2/R3；工具面 sha 未变化。

## 1. 对照表

### 1.1 组一：`a-b` vs `a_b` 前缀撞车

| # | 输入 | Python 实跑 | Go 实跑 | 判定 |
|---|---|---|---|---|
| 1 | 单 server 暴露 `x-y` + `x_y` | 注册键 2 个（`x-y`、`x_y`），都可调用；probe `collision_tool_names_in_one_server` | 折叠成 1 个 `mcp__fold__x_y`；first-wins，`ToolsLoaded=1`，存活执行器调 remote `x-y`（`TestMCPNamespaceFoldCollisionKeepsFirstTool`） | **差异 · L1**（工具数 2 vs 1） |
| 2 | server `a-b` 暴露 `get-sum` + server `a_b` 暴露 `get_sum` | 2 个 server 各 1 工具，裸键 `get-sum`/`get_sum` 并存 | 两个前缀都折叠成 `mcp__a_b__get_sum`；第二个 server `ToolsLoaded=0`（`TestMCPNamespacePrefixFoldCollidesAcrossServers`） | **差异 · L1** |
| 3 | 两个 server 暴露同名工具 `echo_text`（`preserve_existing_tools=True`） | first-wins：`a-b` 拥有 `echo_text`，`a_b` 的 `server_tools` 只剩 `only_b`；probe `collision_cross_server` | first-wins：`mcp__a_b__echo_text` 被 `a-b` 先占后跳过，`a_b` 只剩 `only_b`（`TestMCPNamespaceSameRemoteNameFirstWins`） | **一致**（机制不同：裸名 vs 命名空间键） |
| 4 | 同名工具 + `preserve_existing_tools=False` | 后到覆盖：调用 `echo_text` 落 `a_b:echo_text`；probe `collision_call_echo_text_preserve_false` | 后到覆盖：调用同一折叠名落 `a_b:echo_text`；两个 server 各记 1（`TestMCPNamespaceFoldOverwriteWhenNotPreserving`） | **一致 · L2** |

Go 折叠函数本身（`NamespacedMCPToolName`，Python 无对应物）：

| 输入 `(namespace, server, tool)` | Go 输出 |
|---|---|
| `("mcp", "a-b", "get-sum")` | `mcp__a_b__get_sum` |
| `("mcp", "a_b", "get_sum")` | `mcp__a_b__get_sum` |
| `("mcp", "a-b", "x-y")` / `("mcp", "a-b", "x_y")` | `mcp__a_b__x_y`（同一键） |
| `("", "", "")` | `mcp__server__tool` |
| `("MCP Tools", "a/b", "c:d")` | `MCP_Tools__a_b__c_d` |

### 1.2 组二：名字 ≥64 字符

| # | 输入 | Python 实跑 | Go 实跑 | 判定 |
|---|---|---|---|---|
| 5 | 70 字符工具名（全合法字符） | 注册键原样 70 字符，无截断；probe `long_name.registered_len == 70` | 注册键 `mcp__srv__` + 70 = 79 字符，无截断（`TestMCPNamespaceLongToolNameNotTruncated`） | **差异 · L1**（前缀加长 5 字符） |
| 6 | `_clean_tool_name`（Antigravity provider 边界） | `t*70`→前 64 字符；`"my tool/name"`→`my_tool_name`；`"9lives"`→`_9lives`；`"a-b"`→`a-b`（不折叠 `-`）；`u*128`→前 64 字符 | Go 未移植 Antigravity provider，无对应实现 | **无对应物** |

说明：`_clean_tool_name` 的截断本身也可能撞名（两个前 64 字符相同的长名会
得到同一个 provider 名），属上游自身缺陷；它不在注册层，不改变 `ToolRegistry`
的键。

### 1.3 组三：多 content 块

`_split_mcp_content`（Python，`core/framework/loader/mcp_client.py:89`） vs
`SplitMCPContent`（Go，`go_hive/go-hive/internal/tools/mcp_client.go:108`）：

| # | 输入块 | Python 输出 | Go 输出 | 判定 |
|---|---|---|---|---|
| 7 | 两个 text 块 | `text="hello\nworld"` | 同 | 一致 |
| 8 | text + image | `text="alt"`，`image_url` `data:image/png;base64,AAA` | 同 | 一致 |
| 9 | PDF + `_meta.filename` / 大写 mime | `file` 块，filename 取 `q3.pdf` / 大小写不敏感 | 同 | 一致 |
| 10 | 空 `mimeType`（`""`） | `image_url` `data:;base64,AAA` | 同（`TestMCPSplitContentEmptyMimeTypeStaysImageURL`） | 一致 |
| 11 | 仅 `data`（无 mimeType） | 当作文本原样 `"raw-blob"` | 同（`TestMCPSplitContentDataOnlyItemBecomesText`） | 一致 |
| 12 | `mimeType: null` | image 分支 → `data:None;base64,AAA`（不可转换的 data URL） | 类型断言失败 → 文本 `"AAA"`（`TestMCPSplitContentNullMimeTypeDiverges`） | **差异 · L2**（畸形载荷，见 R3） |
| 13 | `resource_link`（只有 `uri/name/mimeType`） | 丢弃 | 丢弃（`TestMCPSplitContentResourceLinkItemDropped`） | 一致（共同缺口） |
| 14 | 未知对象（无 text/data） | 丢弃 | 丢弃（`TestMCPSplitContentUnknownItemDropped`） | 一致 |
| 15 | 混合 text/image/resource_link/text/pdf | `"a\nb"` + `image_url` + `file` | 同 | 一致 |

模型可见的 `ToolResult`（经 `ToolRegistry.get_executor()` 真实路径）：

| # | 输入结果形状 | Python 输出 | Go 输出 | 判定 |
|---|---|---|---|---|
| 16 | stdio 文本 `"a\nb"` | `content="a\nb"` | 同 | 一致 |
| 17 | stdio 图片 dict `{"_text","_images"}` | `content="caption"` + `image_content=[image_url]` | 同 | 一致 |
| 18 | HTTP legacy 列表 `[text a, text b]` | `content="a"`（首块 text） | 同（`TestMCPExecutorLegacyContentListFirstTextBlock`） | 一致 |
| 19 | HTTP 列表 `[text caption, image]` | `content="caption"` | 同 | 一致 |
| 20 | HTTP 空列表 / `None` | `"[]"` / `"null"` | 同（`TestMCPExecutorLegacyContentListEmptyAndNil`） | 一致 |
| 21 | HTTP 首块非 text（image dict） | `{"type": "image", "data": "AAA", "mimeType": "image/png"}` | `{"data": "AAA", "mimeType": "image/png", "type": "image"}`（`TestMCPExecutorLegacyContentListFirstNonTextBlock`） | **差异 · L3**（键序；L1/L2 一致） |

## 2. 残余登记

### R1 · 命名空间折叠不可逆（`a-b` vs `a_b`）

- **事实**：`NamespacedMCPToolName` 把 server/tool 中所有 `[^A-Za-z0-9_]+`
  折叠为 `_`、合并连续非法字符、并 `Trim(_, "_")`。等价类因此包括
  `a-b`/`a_b`、`-a-`/`a`/`_a_`、`a--b`/`a-b`/`a_b`。同一 server 内或跨
  server 的不同 remote 工具一旦落进同一等价类，就只剩一个注册键：
  `preserve_existing_tools=True` 时后到者被跳过（`ToolsLoaded` 少计），
  `False` 时后到者覆盖执行器（先到者不可达）。
- **与 Python 的差异**：Python 键是裸名，`x-y` 与 `x_y` 是两个可调用工具；
  只有裸名相同才 first-wins。所以这是 Go 扩展引入的**工具丢失**，不是两侧
  同一语义的实现差异（probe #1/#2）。
- **不修原因**：
  1. 命名空间与折叠字符集是冻结契约（`docs/architecture/contracts.md:378-381`），
     改折叠集（例如允许 `-`）会改写所有含 `-` 的模型可见名与工具面 sha；
  2. 无 Python 参考语义可“对齐”（Python 根本没有前缀）；
  3. 若只在冲突时加后缀，`chat_tool_tiers.mcpToolNames()` /
     `mcpLoadedToolNames()` 依赖
     `NamespacedMCPToolName(namespace, server, remote)` 纯函数反查
     `MCPServerStatus.ToolNames`（存 remote 名），顺序相关的后缀会让反查
     失效，反而制造新的“工具未提升”缺陷。
- **建议**：
  - 保持 first-wins 并在文档中写明（本页 + `mcp-crosscheck.md` §3.1）；
  - 至少让“同一 server 内两个不同 remote 名折叠成同一键”可观测：当前
    `registerMCPServerInner` 只在 `origin != serverName` 时告警，同 server
    折叠冲突完全静默；可在 `LogCollisions` 打开时补一条显式 warning
    （只加日志，不动名字）；
  - 若未来要求无损，需要一次**破坏性改名**：对非法字符做可逆转义（如
    `-`→`_2d_`）并同步重编码 `_` 以保持单射，同时更新
    `contracts.md`、工具面 digest、`mcpToolNames` 反查与所有钉住名字的
    测试；不建议用不透明数字后缀。

### R2 · 长名可能超过 provider 的 64 字符上限

- **事实**：两侧注册层都没有长度上限。Python 裸名 70 字符原样注册；Go
  在名字前加 `mcp__<namespace>__<server>__`（默认 5 + server 折叠名长度），
  70 字符工具名变成 79 字符。OpenAI 风格 function name 上限为 64 字符，
  超限可能被 provider 拒绝或截断。
- **上游不一致**：Python 只在 Antigravity 边界截断到 64，且截断会制造新的
  同名（前 64 字符相同的两个长名）；Go 没有 Antigravity provider，因此
  没有可对照的截断实现。
- **不修原因**：注册层截断会改工具面键与 sha；在 provider 层截断则要按
  模型能力区分，两侧都无现成语义可抄。
- **建议**：在 provider 请求构建处按模型能力做“校验 + 截断/哈希”时，两侧
  一起改并更新 digest；在那之前，装载时对 `len(prefix)+len(tool) > 64` 的
  名字给出显式 warning（提示调用方缩短 server/tool 名）。

### R3 · 多 content 块：仅剩序列化/畸形载荷差异

- **聚合语义已一致**（上表 #7–#11、#13–#20）：文本 join、图片/PDF 块、
  HTTP legacy 列表首块、空列表/None 全部对齐。
- **L3 键序（#21）**：HTTP 路径的 `content` 数组在 Go 侧是
  `map[string]any`（解码丢序），`PythonJSONDumps` 又按字典序输出；Python
  `json.dumps` 保留 server 返回的插入序。同一 JSON 对象、不同字节。只在
  “首块非 text”时可见（stdio/SSE 经 `SplitMCPContent` 已是字符串，不受影响）。
- **`mimeType: null`（#12）**：Python 的 `hasattr(item, "mimeType")` 让
  `{data, mimeType: null}` 进 image 分支并生成 `data:None;base64,…`（不可
  转换）；Go 的类型断言失败后回退为文本。畸形载荷，Go 的行为更可用，不建
  议为“对齐”去复刻坏 URL。
- **`resource_link`（#13）**：两侧都丢弃，链接信息丢失；已在
  `mcp-crosscheck.md` §8.3 第 2 条登记，单侧修改会造成新的两侧不一致。
- **建议**：值级 golden 对“首块非 text”的 HTTP 载荷按形状比较（现有
  `api_response_values.py` 口径即可）；若要求字节级一致，需要在 HTTP MCP
  客户端保留原始 JSON 顺序（`json.RawMessage` 或有序解码），影响面大，暂不做。

## 3. 复现

```bash
# Python 侧（输出即 e2e-assets/mcp-namespace/python-output.json）
cd .wt/mcp-ns/core
uv run python ../go_hive/docs/e2e-assets/mcp-namespace/probe_python.py \
  > ../go_hive/docs/e2e-assets/mcp-namespace/python-output.json

# Go 侧（本页的 Go 列全部由这些测试钉住）
cd .wt/mcp-ns/go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/tools -run 'TestMCPNamespace|TestMCPExecutorLegacy|TestMCPSplitContent' -count=1 -v
```

Python 探针不启动任何 MCP 服务：它用一个假的 `MCPClient` 驱动
`ToolRegistry.register_mcp_server` 的真实注册/执行路径，并直接调用
`_split_mcp_content`、`_clean_tool_name` 两个纯函数。
