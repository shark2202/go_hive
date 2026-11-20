# chart_render（chart-tools 组唯一工具）

上游事实源：`tools/src/chart_tools/tools.py`（工具与路径规则）、`renderer.py`（Playwright 渲染与 HTML 模板）、`theme.py`（OpenHive ECharts 主题）、`static/`（两份浏览器库）。
上游测试：`tools/tests/test_chart_tools_smoke.py`。

Go 实现是「同一引擎、同一资源、同一套页面脚本」，只把 Playwright 换成 Chrome 自带的无头模式：本机 Chrome 打开临时页面（内嵌同版本 ECharts / Mermaid），页面按上游逻辑渲染，`--screenshot` 出 PNG，`--dump-dom` 把页面的成功/失败标记带回 Go。没有 Chrome 时工具返回明确错误，**不会**生成假 PNG。

## 1. 实现分布

| 层 | 文件 |
| --- | --- |
| 工具 schema、envelope、参数校验与重试 | `internal/charts/charts.go` |
| 无头 Chrome 发现、flag 组装、DOM 结果解析 | `internal/charts/chrome.go` |
| HTML 模板与页内渲染脚本（ECharts 消毒逻辑 / Mermaid 初始化） | `internal/charts/html.go` |
| OpenHive 主题（与 `theme.py` 逐字段一致） | `internal/charts/theme.go` |
| 内嵌库（`go:embed`，与上游 `static/` 字节一致） | `internal/charts/assets/echarts.min.js`、`assets/mermaid.min.js` |
| 会话工具接线（defs + `RecordBuiltinToolGroup`） | `internal/cli/session_tools_charts.go`、`internal/cli/session_tools.go` |

## 2. 工具参数（对齐上游签名）

```json
{
  "type": "object",
  "properties": {
    "kind":   {"type": "string", "description": "\"echarts\" for ECharts JSON specs, \"mermaid\" for raw mermaid diagram source."},
    "spec":   {"description": "echarts: dict（或 JSON 字符串，工具会自动解析）；mermaid: 原始源码字符串"},
    "title":  {"type": "string", "default": ""},
    "width":  {"type": "integer", "default": 1600},
    "height": {"type": "integer", "default": 900},
    "dpi":    {"type": "integer", "default": 300},
    "output_path": {"type": "string", "description": "默认 $HIVE_HOME/charts/<UTC-timestamp>-<slug>.png"}
  },
  "required": ["kind", "spec"]
}
```

主题不是参数：与上游一致，从 `HIVE_DESKTOP_THEME` 解析（`dark` → 深色，其余 → `light`）。

## 3. 行为对齐

| 行为 | 上游 Python | Go |
| --- | --- | --- |
| kind 校验 | `kind must be 'echarts' or 'mermaid', got '...'` | 同文案（`quote` 复刻 Python repr） |
| echarts spec 为 JSON 字符串 | 自动 `json.loads`，失败报 `spec was a string but not valid JSON: … Pass the spec as a dict` | 同 |
| mermaid spec | 必须是字符串，否则 `mermaid spec must be a string` | 同 |
| 默认输出路径 | `$HIVE_HOME/charts/<UTC %Y-%m-%dT%H-%M-%S>-<slug>.png` | 同（`resolve` 支持会话 home） |
| slug | 小写、`[^a-z0-9]+` → `-`、去首尾 `-`、空 → `chart` | 同 |
| 像素尺寸 | 逻辑视口 `width×height`，文件 `width*dpi/96 × height*dpi/96`，scale 下限 1 | 同（`--force-device-scale-factor`） |
| 主题 | `HIVE_DESKTOP_THEME=dark` 才深色 | 同 |
| ECharts 主题 | `registerTheme('openhive', JSON.parse(themeJson))` + SVG renderer | 同（主题 JSON 与 `theme.py` 逐字段一致，见 `testdata/theme-*.json`） |
| ECharts 布局消毒 | 关动画；title top=16；grid top=64 / bottom=96（有横向 legend）或 40；legend 缺省 `roundRect 12×12 gap16`、横排 bottom=16、竖排保留元素自身位置 | 同（页内脚本逐行移植） |
| Mermaid | `initialize({startOnLoad:false, theme: dark?'dark':'default', securityLevel:'loose'})`，`render()` 结果插入 `#chart` | 同 |
| 页脚容错 | 等待字体与 `window.__chartReady` 再截图 | 双击 rAF 置 ready；`--virtual-time-budget=10000` 保证截图前完成 |
| 失败重试 | `RendererError` 重试一次（150ms） | 同（第二次失败在 envelope 标 `retried: true`） |
| 成功率下限 | PNG < 200 字节视为失败 | 同 |
| 成功 envelope | kind / spec / file_path / file_url / width / height / dpi / bytes / title / runtime_ms | 同上 **加** `theme`（Go 独有，便于核对环境） |
| 错误 envelope | `{"error", "kind", "spec"}`（重试后加 `retried`） | 同 |

工具层差异（与 FastMCP 参数校验等价的位置）：缺 `spec` 返回 `spec is required`；`width/height/dpi` 非整数或负数返回 `width: must be an integer …` 一类 envelope 错误，不会把非法尺寸交给浏览器。

## 4. 渲染后端：本机 Chrome 无头模式

浏览器解析顺序：`CHROME_PATH`（设置了就必须可执行）→ `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome` → `/Applications/Chromium.app/Contents/MacOS/Chromium` → `PATH` 上的 `google-chrome` / `google-chrome-stable` / `chromium` / `chromium-browser` / `chrome`。全部找不到时返回：

```text
no Chrome/Chromium found: set CHROME_PATH or install Google Chrome (looked for …)
```

一次渲染的实际命令（默认 1600×900@300，本机 Chrome 158.0.8127.37 实测）：

```text
/Applications/Google Chrome.app/Contents/MacOS/Google Chrome \
  --headless=new --disable-gpu --no-sandbox --disable-dev-shm-usage \
  --hide-scrollbars --mute-audio --no-first-run --no-default-browser-check \
  --disable-extensions --disable-background-networking \
  --user-data-dir=/var/folders/…/hive-chart-…/chrome-profile \
  --window-size=1600,900 --force-device-scale-factor=3.125 \
  --virtual-time-budget=10000 \
  --dump-dom --screenshot=/var/folders/…/hive-chart-…/chart.png \
  file:///var/folders/…/hive-chart-…/chart.html
```

各 flag 的职责：

- `--headless=new` + `--screenshot`：无窗口截图；`--window-size` 等于逻辑视口，`--force-device-scale-factor=dpi/96` 得到 `width*dpi/96` 的实际像素（与 Playwright 的 `device_scale_factor` 同义）。
- `--virtual-time-budget=10000`：让定时器/rAF 在虚拟时间上立即推进，页面完成布局与异步 Mermaid 渲染后才截图（避免截到动画中间帧）。
- `--dump-dom`：输出渲染完成后的 DOM。页面在 `<html>` 上写入 `data-chart-status="ok|error"` 和 `data-chart-error="<浏览器异常文本>"`；Go 只解析开头 32KB。渲染失败时 Chrome 仍会写一张空白截图，这个标记保证错误不会被当成成功。
- `--user-data-dir`：每次渲染独立 profile，不影响（也不依赖）用户正在使用的 Chrome。
- `--no-sandbox --disable-dev-shm-usage`：与上游 Playwright 启动参数一致，容器/root 下可用。

临时目录结构：`chart.html`、`<echarts|mermaid>.min.js`（从 embed 写出）、`chrome-profile/`、`chart.png`；结束后整目录删除，成功时才把 PNG 写到目标路径。

## 5. 会话接线

- `internal/cli/session_tools_charts.go`：`registerChartTool()` 把 `chart_render` 加入会话 defs，并 `RecordBuiltinToolGroup("chart-tools", …)` 记录 catalog（Tool Library / `@server:chart-tools` 读取该 catalog）。
- `internal/cli/session_tools.go`：建 catalogue 时调用一次 `registerChartTool()`；`Execute` 在 builtin 分支之后把 `chart_render` 交给 `executeChartTool`。
- 分层配置 `internal/tools/tier_defaults.go` 的 `"charts": {"@server:chart-tools"}` 现在能解析出 `chart_render`（测试断言）。

## 6. 测试

```bash
cd go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off go test ./internal/charts/ ./internal/cli/ -run 'Chart|Theme|Slug|OutputPath|PixelSize|BuildPage|Embedded|NoChrome|PageError|ExecuteTool|DPIScale|Mermaid' -v
```

| 测试 | 覆盖 | 需要 Chrome |
| --- | --- | --- |
| `TestRenderEChartsBarChart` | 上游 smoke 主用例：600×400@96，envelope、落盘路径、PNG 尺寸、品牌色像素 | 是（无则 skip 并打印原因） |
| `TestRenderEChartsAcceptsStringSpec` | JSON 字符串 spec 被解析后回显为 dict | 是 |
| `TestRenderMermaidFlowchart` | mermaid 流程图 PNG | 是 |
| `TestRenderMermaidDarkTheme` | `HIVE_DESKTOP_THEME=dark` → envelope theme=dark、背景 #0e0e0d | 是 |
| `TestRenderHonorsDPIScale` | 300×200@192 → PNG 600×400 | 是 |
| `TestRenderMermaidInvalidSourceReturnsError` | 浏览器异常文本透传成 `Mermaid render failed: …` + `retried` | 是 |
| `TestThemeMatchesUpstreamJSON` | `theme.py` 输出的 light/dark 主题 JSON 深度相等 | 否 |
| `TestResolveThemeNameFollowsDesktopEnv` | 主题 env 解析 | 否 |
| `TestSlugifyMirrorsUpstream` / `TestOutputPathDefaultsUnderChartsDir` | slug 与默认路径规则 | 否 |
| `TestRenderEChartsInvalidKindReturnsError` | 上游错误 envelope | 否 |
| `TestRenderEChartsStringSpecInvalidJSONReturnsError` | 字符串 spec 解析失败文案 | 否 |
| `TestRenderRejectsBadDimensions` | 参数强转错误 | 否 |
| `TestNoChromeReturnsExplicitError` | 无 Chrome 明确报错且不落文件 | 否（注入假 lookPath） |
| `TestPageErrorSurfacesInEnvelope` | `data-chart-error` → `ECharts render failed: …` | 否（stub Chrome） |
| `TestChromeArgsCarryViewportScaleAndBudget` | flag 契约（window-size / device-scale / budget / dump-dom / screenshot / file URL） | 否（stub） |
| `TestExecuteToolSerializesEnvelope` | 工具 JSON 通道与全字段 | 否（stub） |
| `TestBuildPageMatchesRendererShell` / `TestEmbeddedLibrariesPresent` | HTML 模板、主题背景、库名、embed 资源 | 否 |
| `TestSessionCatalogueExposesChartToolGroup`（internal/cli） | 会话可见、catalog schema、charts 分类解析、错误分派 | 否 |

无 Chrome 的机器上真实渲染用例输出 `--- SKIP`，原因写明（例如 `real Chrome render skipped: no Chrome/Chromium found: …`）；其余用例照常覆盖参数/路径/主题/错误通道。

## 7. 实测证据（本机 Chrome 158.0.8127.37，macOS）

| 用例 | 请求 | PNG | 字节 | 视觉核对 |
| --- | --- | --- | --- | --- |
| ECharts 柱+线，默认尺寸 | 1600×900@300 | 5000×2813 | 130,351 | 标题 “Revenue by Region” + 副标题、x 轴 NA/EMEA/APAC/LATAM、y 轴 0–50 与 “$M”、橙色柱（42/31/28/12）、蓝色 Target 折线、底部居中图例；861 种颜色，4,117,449 个非背景像素，其中 3,989,778 像素为品牌主色 `#db6f02` |
| Mermaid 流程图 | 1200×600@150 | 1875×938 | 39,074 | `Chart request → kind?`，echarts/mermaid 两条分支汇到 `Headless Chrome`，末端数据库形 `PNG file`；666 种颜色，124,107 个非背景像素 |
| ECharts 深色主题 | 1200×600@150 | 1875×938 | 46,965 | envelope `theme:"dark"`；近黑背景、白色标题、琥珀 p50 / 钢蓝 p99、虚线网格、底部图例；1,020 种颜色 |

附件：`docs/e2e-assets/chart-render-echarts-bar-line.png`、`chart-render-mermaid-flowchart.png`、`chart-render-dark-theme.png`。

耗时（同一台机器，`go test` 实测）：600×400@96 热启动约 0.58–0.70s/次；默认 1600×900@300 约 4.4s（5000×2813 的 Chrome 首次冷启动）。

复现（生成一条真实 PNG）：

```bash
cd go_hive/go-hive
HIVE_HOME=$(mktemp -d) CGO_ENABLED=0 go test ./internal/charts/ -run TestRenderEChartsBarChart -v -count=1
```

## 8. 未对齐项（已知差异）

1. **进程模型**：上游 Playwright 复用浏览器进程（首次 ~1.5s，之后 200–300ms/张）；Go 每次调用起一个独立 Chrome（600×400 热启动 ~0.6s，默认尺寸 ~4s）。输出一致，成本结构不同。
2. **无 CDN 回退**：上游在 `static/` 缺失时才回退 jsdelivr；Go 只使用 `go:embed` 的两份上游字节（离线、确定性）。因此库文件缺失在 Go 侧是构建期问题，不是运行期回退路径。
3. **截图范围**：上游截 `#chart` locator；Go 截整个视口。模板里 `html/body` margin 为 0 且 `#chart` 等于视口，所以两者尺寸一致；若未来给模板加外层 padding，需要改回按元素裁剪（CDP）才能完全等价。
4. **字体等待**：上游显式等待 `document.fonts.ready`；Go 依赖 `--virtual-time-budget=10000` 覆盖字体/布局完成。实测文本未被裁切，但没有逐字体断言的等价保护。
5. **Chromium 版本**：上游锁定 Playwright 自带 Chromium；Go 用本机 Chrome（本机 158）。字体渲染与抗锯齿可能存在亚像素差异，像素级完全一致只在同版本 Chromium 下成立。
6. **envelope 多一个 `theme` 字段**：上游不返回主题；这是 Go 侧附加字段（其余字段与顺序契约一致）。
7. **错误文案来源**：上游从 `page.evaluate` 的返回值读异常；Go 从 `--dump-dom` 里的 `data-chart-error` 读。两者都渲染成 `<ECharts|Mermaid> render failed: <浏览器异常>`。
8. **参数校验位置**：FastMCP 在进入工具前拒绝缺参/类型错误；Go 在工具内返回 `{"error", "kind", "spec"}` envelope（错误类别等价，文案是 Go 侧措辞）。
9. **`width/height/dpi <= 0`**：Go 明确报错（0 表示“用默认值”）；上游会把 0 交给 Playwright，行为未定义。
10. **并发**：上游用进程内锁串行化渲染；Go 未加锁，但每次渲染使用独立临时目录与 profile，天然可并行（工具本身标记为非并发安全，交给运行时排序）。
