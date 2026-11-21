# Browser 注解截图 dpr 对照 fixtures（browser-dpr lane，2026-11-22）

固定输入、两侧实测：同一份 **真实 Chrome** 原始 PNG 同时喂给

* **Python 参考**：上游 `tools/src/gcu/browser/tools/inspection.py::_resize_and_annotate`
  （PIL LANCZOS 缩到 800px + PIL 矩形/标签），与
* **Go**：`hive-browser`/`browserbridge` 的 `browser_screenshot`
  （`format=jpeg, quality=75, max_width=800, annotate=true`；注解由页面 overlay 合成）。

判定只看几何/视觉区域（矩形四条边、区域内/外像素均值、标签带 bbox），
不比 JPEG/PNG 编码字节（Skia vs PIL 已于审计页登记为已接受例外）。

## 输入

| 项 | 值 |
| --- | --- |
| 视口 | 1280×720 CSS（`--window-size=1280,900`，`window.innerWidth/Height` 实测逐次 1280×720） |
| dpr | 1 / 2 / 3（Chrome `--force-device-scale-factor=<dpr>`；`window.devicePixelRatio` 实测一致） |
| 注解目标 | `#probe-target`，`position:fixed; left:240px; top:300px; width:640px; height:120px`（`getBoundingClientRect` 实测 240,300,640,120） |
| 注解文本 | `(150,188) #probe-target`（两侧同一公式：`round(css/1.6)`；CSS→图像 = 800/1280） |
| 截图 | 每次先取 raw PNG（无 annotation），再取 annotated JPEG（q75, max_width=800） |

复现：`bash go_hive/scripts/browser_dpr_probe.sh <evidence-dir>`
（分析半场：`uv run --offline --project tools python go_hive/scripts/browser_dpr_probe_analyze.py <evidence-dir>`）。

## 结果（真实 Chrome 158.0.8127.37，macOS 26.7.1）

注解矩形用「overlay 蓝」区域（fill+outline+label 底色）在矩形中部行/列扫描测得；
`rect_edges = (left,top,right,bottom)`，图像像素坐标。

| dpr | raw PNG | Python PIL | Go 修复前（7a14adf0） | Go 修复后 |
| --- | --- | --- | --- | --- |
| 1 | 1280×720 | 800×450，rect (150,187,550,262) | 800×450，rect (150,187,549,262) | 800×450，rect (150,187,549,262) |
| 2 | 2560×1440 | 800×450，rect (149,187,550,262) | **1600×900**，rect (300,375,1099,524) | 800×450，rect (150,187,549,262) |
| 3 | 3840×2160 | 800×450，rect (150,187,550,262) | **2400×1350**，rect (450,562,1649,787) | 800×450，rect (150,187,549,262) |

* 修复前：Go 注解图宽 = 800 CSS px × dpr（1600/2400），矩形随之 ×dpr；
  上游 PIL 恒为 800 图像像素 → 即审计页登记的「dpr>1 注解宽度」差异。
* 修复后：三种 dpr 输出均为 800×450；矩形边与 PIL 的最大偏差 = **1 px**
  （左边 150 vs 149–150、右边 549 vs 550；来自 DOM 边框 2 CSS px≈1.25 图像像素 + JPEG 抗锯齿）。
  矩形宽 399 vs 400–401 px（<1%）。
* 区域均值（矩形中心 40×20）：Go `(62,81,124)` vs PIL `(55,84,128)`（同一 0.27 alpha 蓝
  叠在 #334155 上，alpha/取整差）；矩形外 40×20：两侧均 `(255,255,255)`。
* 标签带 bbox（800px 图内）：Go `(151,177,232,184)` = 81×7 px；PIL `(147,169,263,184)` = 116×15 px。
  标签文本相同，差异是字体：页面 `11px sans-serif` × 0.625 = ~6.9 图像像素，PIL 固定 11 图像像素
  （本机 macOS 无上游硬编码的 `/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf`，
  Pillow 走 `load_default()`；Linux 生产机为 DejaVuSans-Bold 11）。**未修，登记为字体度量例外。**

## 文件

| 文件 | 说明 |
| --- | --- |
| `report-prefix.json` | 修复前（base `7a14adf0` 二进制）三档 dpr 的机读测量 |
| `report-postfix.json` | 修复后（本 lane）同输入的机读测量 |

`path` 字段（本机 scratch 绝对路径）已剔除；其余为实测原值。
