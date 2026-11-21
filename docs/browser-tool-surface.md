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
| 注册 | `integrations.InstallBrowserTools`（`newSessionToolCatalogue` 构建会话目录时调用，按 `browserBridgeRequested` 开关：`HIVE_BROWSER_BRIDGE` env → `configuration.json` 的 `gcu_enabled` → 默认开） |
| 执行 | 每个工具的 executor 向桥 `POST /command` 发 `{profile, type, params}`（与 `hive-browser` CLI 同一 transport） |

例外（逐条，不隐藏）：

- **桥开关**：桥关闭（`HIVE_BROWSER_BRIDGE=0` 或 `gcu_enabled: false`）时，22 个定义既不注册也
  不登记进会话工具注册表——executor 只会向桥 POST，没有桥就没有可执行面。这与上游 retired
  bundled gcu-tools server 的口径一致：冻结探针（`HIVE_BROWSER_BRIDGE=0`）的
  `registry_mcp_total` 因此只含 `browser_setup`，不再把 21 个 inactive 定义计成 MCP 宇宙；
  桥开启时延迟层保持 72。回归：`internal/cli/browser_tools_gate_test.go::TestBrowserToolsFollowBridgeSwitch`。

- `browser_setup`：Python 默认面 24 名里的 `browser_setup` 来自
  `core/framework/tools/browser_tools.py`（framework discovery 工具），不是
  gcu 的同名副本。Go 保持 framework 版定义（`InstallBrowserSetup`），
  `InstallBrowserTools` 对已注册名跳过；因此 22 个名全覆盖 = 21 个新注册 +
  framework 版 `browser_setup`。**这是有意口径，不是遗漏。**
- `focused_element`：上游 `bridge.click/click_coordinate` 的可选返回字段
  （读 `document.activeElement`）未移植；不影响动作 dict 的必选键集。
- **dpr>1 注解图宽度（2026-11-22 已修 + 实测）**：`clip.scale` 以 CSS px 计、
  Chrome 位图按 device px 返回（`clip.width × scale × dpr`），Go 之前按
  `max_width/cssWidth` 取值，dpr>1 主机上注解图宽 = 800×dpr（实测 1600/2400），
  上游 PIL 恒为 800 图像像素。现按 `max_width/(cssWidth×dpr)` 取值，三种 dpr
  输出均为 800×450、矩形边与 PIL 偏差 ≤1 px；**残留**是标签字体/alpha/编码字节
  （见 §4.1 与 `docs/fixtures/browser-dpr-probe/README.md`），不声称完全一致。

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

### 4.1 dpr>1 注解图宽度（2026-11-22 实测 + 收口）

审计页 D08 登记的「dpr>1 注解图宽度」差异：固定输入（1280×720 CSS、
`#probe-target` 240,300,640,120、注解文本 `(150,188) #probe-target`）、
`dpr∈{1,2,3}`，真实 Chrome 158 + 真桥双跑，同一 raw PNG 喂上游
`_resize_and_annotate` 对照（fixtures：`docs/fixtures/browser-dpr-probe/`）：

| dpr | Python PIL（参考） | Go 修复前 | Go 修复后 |
| --- | --- | --- | --- |
| 1 | 800×450；rect (150,187,550,262) | 800×450；rect (150,187,549,262) | 800×450；rect (150,187,549,262) |
| 2 | 800×450；rect (149,187,550,262) | **1600×900**；rect (300,375,1099,524) | 800×450；rect (150,187,549,262) |
| 3 | 800×450；rect (150,187,550,262) | **2400×1350**；rect (450,562,1649,787) | 800×450；rect (150,187,549,262) |

- 根因：`Page.captureScreenshot` 的 `clip.scale` 以 CSS px 计，位图按 device px
  返回（`clip.width × scale × dpr`）；Go 原取值 `max_width/cssWidth`，dpr>1 时注解图
  宽 = 800×dpr。修复：取值改为 `max_width/(cssWidth×dpr)`（`cmdBrowserScreenshot`），
  任意 dpr 输出恒为 800 图像像素，与上游 PIL 一致；**工具面 sha 与描述不变**
  （`TestBrowserToolDefinitionGolden` 22/22）。
- 修复后几何：矩形的 fill/outline 区域边与 PIL 最大偏差 **1 px**（左边 150 vs 149–150、
  右边 549 vs 550；DOM 边框 2 CSS px≈1.25 图像像素 + JPEG 抗锯齿）；矩形宽 399 vs 400–401；
  矩形内外区域均值两侧一致口径（内 (62,81,124) vs (55,84,128)，外均 (255,255,255)）。
- **仍不对齐（登记，不声称完全一致）**：标签字体度量——Go 是页面 `11px sans-serif`
  （CSS px，800px 输出下 ≈6.9 图像像素，标签盒 81×7），PIL 固定 11 图像像素
  （本机回退 `load_default()`，Linux 生产为 DejaVuSans-Bold；标签盒 116×15）；
  alpha 混合与 JPEG 编码字节（Skia vs PIL）仍属已接受例外。回归：
  `TestScreenshotMaxWidthIsDprIndependent`（dpr 1/2/3）与
  `TestScreenshotMaxWidthJpegsAt800`（dpr 2 假页 = 真实 Chrome 语义：
  `clip.width × scale × dpr`）。

## 5. 测试与复跑

```bash
# 定义 golden（22/22 sha）
cd go_hive/go-hive && go test ./internal/tools/integrations/ -run TestBrowserTool -v

# tier：首轮 24 + 延迟层含 22 个浏览器工具 + executor 走桥
cd go_hive/go-hive && go test ./internal/cli/ -run TestBrowserTool -v
cd go_hive/go-hive && go test ./internal/server/ -run TestChatToolTier -v   # 首轮 24 逐字

# 行为残差
cd go_hive/browserbridge && go test ./ -run 'TestBrowserTabless|TestBrowserTabs|TestBrowserOpenAndNavigate|TestBrowserInteractReturnKeySets|TestBrowserSelectReturnKeySet' -v

# dpr 注解宽度（新增：假页 dpr 1/2/3 + 真实 Chrome 双跑探针）
cd go_hive/browserbridge && go test ./ -run 'TestScreenshotMaxWidth' -v
bash go_hive/scripts/browser_dpr_probe.sh <evidence-dir>   # 真实 Chrome + 真桥，dpr 1/2/3
uv run --offline --project tools python go_hive/scripts/browser_dpr_probe_analyze.py <evidence-dir>

# 定义漂移门禁（重新从 Python 抽取并逐字比对）
uv run python go_hive/scripts/gen_browser_tool_defs.py --check
```

本次实测（2026-11-21，本机）：

- `TestBrowserToolDefinitionGolden` → `22/22 name+description+schema sha matches`
- `TestBrowserToolsAreDeferredNotFirstRequest` → `24 eager (first request) + 72 deferred (incl. 22 browser tools)`
- `internal/server` → `first request tools = 24 in Python order`（既有用例）
- browserbridge 行为用例全绿；`go_hive/browserbridge` 全包 `go test ./...` 绿。
- `browser_dpr_probe.sh`（真实 Chrome 158）→ dpr 1/2/3 注解图均 800×450、矩形边与
  PIL 最大偏差 1 px；修复前为 800/1600/2400 宽（2026-11-22，见 §4.1）。

## 6. 已知不做（not-doing）

- 不做 gcu MCP server 的 Go 进程：定义 + 执行直接落在桥的 `/command`，不新增
  MCP 传输面。
- 不为 22 个工具改 `HIVE_TOOL_SURFACE=grouped` 的完整注册表语义：它们仍作为
  flat 名字存在（grouped 模式下同样可见），不是 module+action。
- 不移植 `focused_element`、可见高亮 overlay、`has_public_listeners`（与
  `PARITY.md` §6 的既有 not-available 清单一致）。
