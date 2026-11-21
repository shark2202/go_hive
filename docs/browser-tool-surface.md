# Browser tool surface — 22 active gcu tools as Go tool definitions

Status: implemented on `browser-tool-surface` (from `main` @ 93b5a3a0).
Evidence labels: **measured** = command output in this repo; **code** = source
reference; **unverified** = not executed here.

## 1. 问题

Python 把浏览器能力做成 gcu 的 FastMCP 工具面：`register_tools()` 注册
22 个 active 工具，模型看到的是每个工具的 `name` / `description` /
`inputSchema`。Go 侧此前只有桥的 27 个 canonical command + `hive-browser`
CLI help，**没有 tool schema 层**（`docs/browser-bridge-surface-audit.html`
§2.2「描述」行）；因此模型在 Go 会话里无法把 `browser_open`、`browser_tabs`
这类工具当作工具调用，22 个定义的名字/描述/参数也无从对照。

本车道做两件事：

1. **定义层**：从 Python FastMCP 注册逐字冻结 22 个定义，装进 Go 会话工具
   注册表的延迟/目录层（`search_tools` 可加载，加载后下一轮进 `tools[]`）。
2. **行为残差**（审计 §2.2「参数」行）：`tab_id` 可省略→active tab、
   `browser_tabs.activeTabId`、`open/navigate` 的 group 摘要、
   `browser_interact` 动作 dict 字段名。

## 2. 定义层

| 项 | 值 |
| --- | --- |
| 定义来源 | `tools/src/gcu/browser/tools/{lifecycle,tabs,navigation,inspection,interact,advanced,script}.py` 的 `register_*_tools()`（live FastMCP ToolManager，不是手抄） |
| 生成器 | `go_hive/scripts/gen_browser_tool_defs.py`（`--check` 做漂移门禁） |
| 冻结产物 | `go_hive/go-hive/internal/tools/integrations/browser_tool_defs.json`（每工具 `name`/`description`/`input_schema` + `sha256`） |
| sha 规则 | `{name, description, input_schema}` 的 sorted-key compact JSON（整数值浮点归一为整数），UTF-8 |
| 定义数 | **22**（lifecycle 3 / tabs 4 / navigation 2 / inspection 5 / interact 2 / advanced 5 / script 1） |
| 逐字校验 | **22/22** name+description+input_schema sha 一致（`TestBrowserToolDefinitionGolden`，measured） |
| 注册 | `integrations.InstallBrowserTools`（`sessionIntegrationInstallers` 的 `browser-tools` 行） |
| 执行 | 每个工具的 executor 向桥 `POST /command` 发 `{profile, type, params}`（与 `hive-browser` CLI 同一 transport） |

例外（逐条，不隐藏）：

- `browser_setup`：Python 默认面 24 名里的 `browser_setup` 来自
  `core/framework/tools/browser_tools.py`（framework discovery 工具），不是
  gcu 的同名副本。Go 保持 framework 版定义（`InstallBrowserSetup`），
  `InstallBrowserTools` 对已注册名跳过；因此 22 个名全覆盖 = 21 个新注册 +
  framework 版 `browser_setup`。**这是有意口径，不是遗漏。**
- `focused_element`：上游 `bridge.click/click_coordinate` 的可选返回字段
  （读 `document.activeElement`）未移植；不影响动作 dict 的必选键集。

## 3. tier：延迟层，不挤进首轮 24

- 首轮 `tools[]` 仍是 Python 参考的 **24** 个 eager 名字（顺序不变）：
  `TestBrowserToolsAreDeferredNotFirstRequest`（measured，日志
  `24 eager + 72 deferred`）与 `internal/server/chat_tool_tiers_lazy_test.go`
  （`first request tools = 24 in Python order`）同时看守。
- 22 个定义里 21 个进 `search_tools` 延迟层；`browser_setup` 本来就在 eager
  24 内。延迟层 51 → **72**（15 lifecycle/colony + 36 扁平目录层 + 21
  browser tools）。
- 与 Python 的 tier 对应关系：Python 侧这些工具挂在 gcu MCP server 上，属于
  Tool Library/延迟层而非默认请求；Go 用同一 eager/searchable 语义承载
  （`core/framework/tools/tool_tiers.py`）。
- 分组模块启发式（schema 带 `action` 视为 module+action）会把
  `browser_dialog_respond`/`browser_interact` 误判成 grouped；
  `flatSurfaceGroupedModule` 对 22 个浏览器名豁免（它们按 Python 形状是
  一工具一动作）。

## 4. 行为残差（审计 §2.2「参数」行）

| 项 | Python | Go（本车道后） | 证据 |
| --- | --- | --- | --- |
| `browser_close(tab_id=None)` | `tab_id or ctx["activeTabId"]`（`tabs.py:311`） | 省略时 `resolveTabTarget` 取上下文 active tab；关闭的是 active tab 时按 group 首 tab 修复 | `TestBrowserTablessCommandsResolveActiveTab` |
| `browser_navigate(tab_id=None)` | `navigation.py:151` | 同上；先做 `_ensure_context` 等价懒创建（seed tab 复用） | 同上 |
| `browser_reload(tab_id=None)` | `navigation.py:266` | 同上 | 同上 |
| 无 active tab | `{"ok": false, "error": "No tab to close"/"No active tab"}` | `invalid_state: no active tab is known for this profile — pass tab_id …`（CLI 仍按 not_started 分类） | `TestBrowserTablessCommandsFailWithoutActiveTab` |
| `browser_tabs` | `{ok, tabsHint?, total, activeTabId, tabs}`（`tabs.py:159`） | 有上下文时 `activeTabId` 为上下文 active tab、按上下文 group 列 tab；无上下文保持 `null` + profile 列表 | `TestBrowserTabsReportsActiveTabID`、`TestBrowserTabsWithoutContextKeepsNullActiveTabID` |
| `browser_open/navigate` group 摘要 | `summarize_group_tabs`: `groupTabCount`, `groupTabs[{id,url,title,active}]`, >3 时 `tabsHint` | 同键集 | `TestBrowserOpenAndNavigateAppendGroupSummary` |
| `browser_interact` 动作 dict | click `{ok,action,selector,x,y,method}`；坐标 click `{ok,action:"click_coordinate",x,y}`；hover `{ok,action,selector,x,y}`；type `{ok,action,selector,length}`；key `{ok,action,key,modifiers}`；scroll `{ok,action,direction,amount,method,container,steps}`；drag `{ok,action,fromCoords,toCoords}`；select `{ok,action,selector,selected}`；wait `{ok,action,condition,…}` | 逐键对齐；移除 Go legacy `tabId`/`button`/`clickCount`/`chars`/`deltaX`/`deltaY`；R15 的 `data`+`mimeType` 图像载体与 `snapshot` 额外键为已记录设计 | `TestBrowserInteractReturnKeySets`、`TestBrowserSelectReturnKeySet`、`TestBrowserInteractSnapshotPayloadStillAttachedByDefault` |

补充（有意口径）：

- **懒创建上下文**是 best-effort：`browser_open/navigate` 在无上下文时按上游
  `_ensure_context` 创建 tab group + seed tab；扩展不支持/桥报错时退回旧行为
  （tab 级命令需显式 `tab_id`），不会让 open 直接失败。调用方显式给
  `group_id` 时不做懒创建。
- **scroll 的 `method/container/steps`**：Go 用一次 CDP wheel event，故报
  `method:"cdp"`、`steps:1`；`container` 在无 selector 时沿用上游的
  `"unknown"` 兜底（不伪造 DOM tag）。
- **CLI artifact 命名**：动作 dict 不再带 `tabId`，`hive-browser interact`
  的坐标动作图按上游同款 tab0 兜底命名（zoom/screenshot 元数据仍带 tabId）。

## 5. 测试与复跑

```bash
# 定义 golden（22/22 sha）
cd go_hive/go-hive && go test ./internal/tools/integrations/ -run TestBrowserTool -v

# tier：首轮 24 + 延迟层含 22 个浏览器工具 + executor 走桥
cd go_hive/go-hive && go test ./internal/cli/ -run TestBrowserTool -v
cd go_hive/go-hive && go test ./internal/server/ -run TestChatToolTier -v   # 首轮 24 逐字

# 行为残差
cd go_hive/browserbridge && go test ./ -run 'TestBrowserTabless|TestBrowserTabs|TestBrowserOpenAndNavigate|TestBrowserInteractReturnKeySets|TestBrowserSelectReturnKeySet' -v

# 定义漂移门禁（重新从 Python 抽取并逐字比对）
uv run python go_hive/scripts/gen_browser_tool_defs.py --check
```

本次实测（2026-11-21，本机）：

- `TestBrowserToolDefinitionGolden` → `22/22 name+description+schema sha matches`
- `TestBrowserToolsAreDeferredNotFirstRequest` → `24 eager (first request) + 72 deferred (incl. 22 browser tools)`
- `internal/server` → `first request tools = 24 in Python order`（既有用例）
- browserbridge 行为用例全绿；`go_hive/browserbridge` 全包 `go test ./...` 绿。

## 6. 已知不做（not-doing）

- 不做 gcu MCP server 的 Go 进程：定义 + 执行直接落在桥的 `/command`，不新增
  MCP 传输面。
- 不为 22 个工具改 `HIVE_TOOL_SURFACE=grouped` 的完整注册表语义：它们仍作为
  flat 名字存在（grouped 模式下同样可见），不是 module+action。
- 不移植 `focused_element`、可见高亮 overlay、`has_public_listeners`（与
  `PARITY.md` §6 的既有 not-available 清单一致）。
