# 坐标动作附图（R15）对照 fixtures

本目录是 `docs/browser-bridge-surface-audit.html` R15 修复轮的实测证据：上游
`browser_interact` 的**坐标** click / hover / key 成功后会随手返回一张注解截图
（`tools/src/gcu/browser/tools/interact.py::_build_visual_response`，调用点
871/885/944），Go 之前只回动作 JSON。本目录记录修复前后的返回 key 集合、注解图
本身，以及真桥 / 假扩展 / 真 Chrome 两侧的验证结果。

## 文件

| 文件 | 说明 |
| --- | --- |
| `before-cli-click.json` / `before-cli-hover.json` / `before-cli-key.json` | 修复前 `hive-browser interact --action <a> --coordinate …` 的原始输出（真实 Go 桥 + 假扩展） |
| `after-cli-click.json` / `after-cli-hover.json` / `after-cli-key.json` | 修复后同一条命令的输出：动作 dict + `saved_to` + `_image`，无内联 base64 |
| `before-bridge-click.json` / `after-bridge-click.json` | 桥 `POST /command` 的原始返回（`{ok,result}` 信封）；after 多 `data` + `mimeType` |
| `after-image-click.jpg` | after 里 `data` 解码出的 800×450 JPEG（假扩展的确定性渲染） |
| `real-chrome-cmd-interact_click_coordinate.json` | 真实 Chrome 19 步流程中坐标 click 的 `/command` 原始返回（含注解图 base64） |
| `real-chrome-click-coordinate.jpg` | 上一步 `data` 解码出的真机注解图（1600×893，dpr=2 主机） |
| `before-after.json` | 上述输出的机读摘要（key 集合、图像字节/哈希、真机 marker 扫描） |
| `reproduce.sh` | 复现 before/after 的探针：真桥 + 假扩展，输出 bridge `/command` 与 CLI 两侧 JSON 并保留落盘 JPEG |

复现：

```sh
# after（当前分支）
bash go_hive/docs/fixtures/browser-annotated-actions/reproduce.sh <repo-root> <out-dir>
# before 需要基线二进制；探针用当前 checkout 构建，baseline 输出由同一脚本在修复前采集
# 全链路（含 R15 段）：bash go_hive/scripts/e2e_browser_panel.sh
```

`after-cli-*.json` 里的 `saved_to` 指向探针临时目录（运行后已删除），属测量残留；
落盘内容即 `after-image-click.jpg`。

## 事实表：上游 → Go 前 → Go 后

坐标语义：`coordinate` 是视口分数 0..1，桥用实时 `window.innerWidth/innerHeight`
换算成 CSS px；动作结果的 `x` / `y` 就是 CSS px。

| 层 | 动作 | 上游（Python） | Go 修复前 | Go 修复后（本分支） |
| --- | --- | --- | --- | --- |
| bridge | 坐标 click | `{ok,action:"click_coordinate",x,y}`（+`focused_element`），返回 `[TextContent(dict), ImageContent(jpeg)]` | `{action,button,clickCount,method,ok,tabId,x,y}` | 同左 + `data`（base64 JPEG）+ `mimeType` |
| bridge | 坐标 hover | `{ok,action:"hover_coordinate",x,y}` + `ImageContent` | `{action,ok,tabId,x,y}` | 同左 + `data` + `mimeType` |
| bridge | 坐标 key | `{ok,action:"press_at",x,y,key}` + `ImageContent` | `{action,key,ok,tabId,x,y}` | 同左 + `data` + `mimeType` |
| CLI | 坐标 click | 动作 dict + `saved_to` + `_image` | 动作 dict（无图、无指针） | 动作 dict + `saved_to` + `_image` |
| CLI | 坐标 hover | 同上 | 同上 | 同上 |
| CLI | 坐标 key | 同上 | 同上 | 同上 |
| 不变 | selector click | 动作 dict + `snapshot`（`_attach_snapshot`），无图 | 同上游 | 不变（`data` 不出现） |
| 不变 | selector hover / focused key | 动作 dict，无图 | 同上游 | 不变 |
| 不变 | drag | 动作 dict，无图（`_do_drag` 不经过 visual response） | 同上游 | 不变 |
| 不变 | type / scroll | 动作 dict + `snapshot` | 同上游 | 不变 |

实测 key 集合（`before-after.json` 逐项列出）：

```
bridge click  before: action,button,clickCount,method,ok,tabId,x,y
bridge click  after : + data,mimeType
bridge hover  before: action,ok,tabId,x,y                 after: + data,mimeType
bridge key    before: action,key,ok,tabId,x,y             after: + data,mimeType
cli click     before: action,button,clickCount,method,ok,tabId,x,y
cli click     after : + _image,saved_to
cli hover     after : action,ok,tabId,x,y,_image,saved_to
cli key       after : action,key,ok,tabId,x,y,_image,saved_to
```

**没有新增字段名**：bridge 层只加 `data` + `mimeType`（桥内每个图像 payload 都是
这两个键），CLI 层只加 `saved_to` + `_image`（`_image={path,mime,intent}`）——
即上游 `_blocks_to_cli_dict` 的图像分支。不携带 `imageWidth/cssWidth/annotated/…`
这些截图元数据键（那些属于 `browser_screenshot` 的契约，不属于动作结果）。

## 实现路径（复用，不复制）

* `commands_interact.go::attachInteractImage`：坐标 click / hover / key 成功后调用
  既有 `cmdScreenshot {format:jpeg, quality:75, max_width:800, annotate:true}`；
  该路径用 `b.tabHighlight(tab)`（动作刚由 `cmdClick`/`hoverPoint`/`pressKeyAt`
  写入的 last-interaction 记录）注入页面 overlay → 捕获 → 移除 overlay，把
  `data`/`mimeType` 合入动作结果。任何失败都原样返回动作结果（上游 text-only
  fallback）。
* `browser_cli_commands.go`（interact post）+ `browser_cli_artifacts.go::spillActionImage`：
  非 screenshot/zoom 的 interact 结果若有 `data`，落盘
  `browser_interact_<ms>_tab<id>.jpg` 并只加 `saved_to` + `_image`。
* 高亮语义与上游逐项对应：click → `label="click (x,y)"`、`kind="point"`；
  hover → `"hover (x,y)"`；key → `"<key> (x,y)"`。截图注解的几何/标签公式与上游
  `_resize_and_annotate` 相同（CSS→图像坐标、`(x,y) label` 位置），只是渲染方式不同
  （页面 overlay vs PIL 绘制）。

## 验证

单测（假扩展，`go_hive/browserbridge`）：

* `TestInteractCoordinateClickAttachesAnnotatedImage`：动作 dict 保留、`data`
  解码为 800×500 JPEG、overlay 注入→捕获→移除的顺序、标签
  `"(400,250) click (800.0,500.0)"`。
* `TestInteractCoordinateHoverAndKeyAttachAnnotatedImage`：hover / key 同样附图，
  标签分别为 `hover (400.0,500.0)` / `Enter (800.0,500.0)`。
* `TestInteractCoordinateImageFailureDegradesToActionResult`：捕获失败时动作仍
  `ok:true` 且不带 `data`。
* `TestInteractClickBySelectorAttachesSnapshot`：selector click 仍只带 `snapshot`，
  不带图。

CLI（`go_hive/go-hive/internal/cli`）：

* `TestBrowserCLIInteractCoordinateSpillsImagePointer`：`interact --coordinate`
  的结果 key 集合 = 动作 dict + `saved_to` + `_image`，JPEG 字节落盘一致。

真桥 + 假扩展 e2e（`bash go_hive/scripts/e2e_browser_panel.sh`，R15 段）：

```
PASS: hive-browser interact coordinate click/hover/key -> interaction dict + saved_to/_image (800x450 JPEG)
R15 key sets + pointers ok: click/hover/key
R15 image decode ok: 800x450 JPEG x3
```

真实 Chrome（本机 Google Chrome 158.0.8127.37，扩展 1.7.4）：

```
BB_E2E_WS_PORT=19849 BB_E2E_HTTP_PORT=19850 BB_E2E_CONTROL_PORT=19851 \
BB_E2E_LEGACY_PORT=19852 BB_E2E_FIXTURE_PORT=19899 \
BB_E2E_EXT_DIR=<extension copy with BRIDGE_PORTS=[19849] only> \
  bash go_hive/browserbridge/scripts/real_chrome_e2e.sh <evidence-dir>
# 19 步流程 + 坐标 click 探针
ok  : interact_click_coordinate -> {"action":"click",…,"mimeType":"image/jpeg",…,"x":1152,"y":642.6}
ok  : click marker 200 red px, bbox=(1424,788)-(1455,816) in 1600x893
PASS: all steps succeeded against real Chrome (Google Chrome 158.0.8127.37)
```

说明：本机 14829/9229 已被在跑的 Hive 会话桥占用，而扩展把
`BRIDGE_PORTS = [14829, 9229]` 写死；因此真机验证使用扩展副本，仅把该常量改为
`[19849]`（`diff -rq` 只报 `offscreen.js` 这一行），其余逐字节一致。坐标 click 的
注解图在真机上被逐像素扫描到红色落点标记（200 px，bbox 中心 1439.7,802.4 ≈
CSS (1152,642.6) × dpr 1.25），证明返回的是**注解图**而不只是截图。

## 仍未对齐（本目录测量范围内）

* **dpr>1 主机的图像宽度**：上游 PIL 恒输出 800 设备像素；Go 的
  `clip.scale=800/cssWidth` 以 CSS px 计，Chrome 再乘 dpr，所以 dpr=2 时注解图是
  1600×893（= 800 CSS px × dpr）。这是 R01 已量化的编码/重采样近似的既有表现，
  本次沿用同一 screenshot 路径，未改公式；假扩展按 dpr=1 建模，故 e2e 断言仍是
  800×450。
* **动作 dict 字段集**：Go 坐标 click 仍沿用 legacy `browser_click` 形状
  （`action:"click"` + `tabId/button/clickCount/method`），hover/key 多 `tabId`；
  上游坐标方法返回 `{ok,action,x,y(,key)}`（click 另可带 `focused_element`）。
  R15 只补图，未改字段名；CLI 落盘名因此用真实 tab id（上游 `_blocks_to_cli_dict`
  在坐标 dict 里取不到 `tabId`，会落成 `…_tab0.jpg`）。
* **数值文本**：Go JSON 把整数值的 float 输出为 `640`，Python 为 `640.0`
  （既有差异，与本次改动无关）。
* **注解像素级**：页面 overlay 由浏览器合成（字体/alpha 与 PIL 不同），几何与标签
  文本同公式；真机 marker 扫描验证落点存在，不做逐像素等价承诺。
* 假扩展的截图是确定性渲染（click/hover/key 三张字节相同），overlay 是否注入由
  Go 单测的 inject→capture→remove 顺序断言；真机由 marker 扫描补证。
