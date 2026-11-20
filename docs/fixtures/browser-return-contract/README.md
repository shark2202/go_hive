# Browser 返回契约对照 fixtures（R01/R02/R07）

本目录是 `docs/browser-bridge-surface-audit.html` D08 修复轮的实测证据：同一条输入
（1280×720 视口、一个 `Docs` 链接、一个 `Go` 按钮）在

* **Python 参考**（`tools/src/gcu` 工具层，本仓库源码）与
* **Go**（`hive-browser` + 真实 `browserbridge` 二进制 + 假扩展，`go_hive/scripts/e2e_browser_panel.sh`）

两侧的返回形状对照。

## 文件

| 文件 | 说明 |
| --- | --- |
| `python-reference.json` | 上游工具层实测：`render_screenshot(spill=True)`、`BeelineBridge._format_ax_tree`、`classify_result`（由 `python_reference.py` 生成，不启动桥进程） |
| `before-screenshot.json` / `before-snapshot.json` / `before-upload.json` | 修复前（main `db5c4c1b` 构建的二进制）的 `hive-browser --json` 原始输出 |
| `after-screenshot.json` / `after-snapshot.json` / `after-upload.json` | 修复后（分支 `browser-return-contract`）同一条命令的原始输出 |
| `after-snapshot-tree.txt` | 修复后 `page snapshot` 落盘的 AX 树 artifact |
| `before-after.json` | 上述输出的机读摘要（key 集合、rc、树文本、错误文本） |

复现：`go_hive/scripts/e2e_browser_panel.sh`（R01/R02/R07 三个 PASS 段）；修复前输出用
main 检出构建的同名二进制 + 同一个假扩展测得。`saved_to` 内的绝对路径是本机
scratch 目录，属测量残留。

## R01 截图

| 维度 | Python（实测） | Go 修复前 | Go 修复后 |
| --- | --- | --- | --- |
| CLI JSON keys | `ok,tabId,url,imageType,size,imageWidth,cssWidth,cssHeight,capturedWidth,capturedHeight,fullPage,crop_box,devicePixelRatio,physicalScale,annotated,scaleHint,saved_to,_image` | `ok,tabId,data,format,mimeType` | 与 Python **逐 key 相同** |
| 图像 | 800×450 JPEG（解码实测） | 1280×720 PNG base64 内联（8460 chars） | 800×450 JPEG 落盘（解码实测） |
| `saved_to`/`_image` | 有 | 无 | 有（`_image.mime=image/jpeg`，`intent:null`） |
| `imageWidth`/`physicalScale`/`capturedWidth` | 800 / 1.6 / 1280 | 无 | 800 / 1.6 / 1280 |
| `crop_box` | `null`（整视口） | 无 | `null` |
| `annotated` | `false`（无高亮） | 无 | `false`（无高亮；点击后为 `true`，单测 + 截图流程覆盖） |

**编码近似量化**：两侧输出都是 800×450 的 JPEG q75，尺寸与物理比例逐值相同；
字节级不等价——Python 用 Pillow(libjpeg) 对原始 PNG 做 LANCZOS 重采样后编码，
Go 让 Chrome 以 `clip.scale=800/cssWidth` 原生渲染同一区域（Skia 编码器 + 浏览器
合成器重采样），e2e 里则由假扩展按同一 clip 渲染。两者重采样器与 JPEG 编码器不同，
无法给出逐字节相等承诺；可核验的是：输出宽高、纵横比、格式、质量与元数据字段逐值一致。
注解：Python 用 Pillow 画蓝色框/红点 + `(x,y) label` 文本，Go 在捕获前向页面注入同
几何的 overlay（同一 CSS→图像坐标公式，`round` 后同一标签文本），颜色/字体族不同。

## R02 快照

| 维度 | Python（实测） | Go 修复前 | Go 修复后 |
| --- | --- | --- | --- |
| CLI JSON keys | `ok,tabId,url,length,saved_to`（`--head` 时多 `head`） | `ok,tabId,url,tree,nodes` | 与 Python 相同 |
| 树文本 | `- RootWebArea [ref=e1] "Doc"\n  - button [ref=e2] "Go"\n  - link [ref=e3] "Docs" [href="https://example.com/docs"]`（112 chars） | `- RootWebArea "Doc"\n  - button "Go"\n  - link "Docs"`（51 chars，无 refs/href） | 与 Python **逐字节相同**（112 chars） |
| artifact | `browser_snapshot_<ms>_tab<id>.txt` | 无 | 有 |
| `--head` | （Go 自有开关）从 artifact 读前 N 字符 | no-op（无 `saved_to`） | 读 artifact 前 40 字符，与落盘内容一致 |
| DOM fallback / dialog 前置检查 | 有 | 无 | 有（单测覆盖；fallback 触发条件与上游同值） |

## R07 upload 文件不存在

| | Python（实测 classify） | Go 修复前 | Go 修复后 |
| --- | --- | --- | --- |
| 已连桥 + 有 tab | `File not found: …` → **4** | 本地预检 → **6** | 桥返回 `File not found: …` → **4** |
| 无 context/tab | `Browser not started` → **3** | **6**（先本地预检） | 无 tab 时 CLI 本地 → **3**；桥侧无 tab 同上游路径 |

## 仍未对齐（本目录测量范围内）

* 逐字节图像/JPEG 等价性：见上「编码近似量化」。
* 注解像素级：overlay 由页面合成（字体族、alpha 混合与 PIL 不同），几何与标签文本一致。
* 快照树未实现 `_clean_inline_text_boxes` 之外的上游全部微格式（InlineTextBox 去重已
  移植；其余树形差异以单测中逐字节断言代表样本为准）。
* Python 侧的进程级 e2e 未在本机重跑（14829/14831 被在跑的 Go 桥占用），fixture 的
  Python 列是工具层函数实测 + 源码路径，非全链路。
