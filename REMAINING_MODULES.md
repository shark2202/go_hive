# 剩余上游模块清单

> 生成日期：2026-11-19。数据来源：`tools/src/aden_tools/tools/*` 的目录名与 `@mcp.tool` 计数，
> 对比 `go_hive/go-hive/internal/tools/integrations` 已注册的 `Tool.ID`。
> 复现：`grep -rhoE 'Source: +"[a-z0-9_]+/' internal/tools/integrations/*.go` 只能看到逐 action
> 的 Source，对本仓库的表格驱动 catalog 会少算；准确口径是运行一次 `Registry.Tools()` 打印
> `Tool.ID` 与 `len(Tool.Actions)`（本次为一次性探针，未入库）。

## 1. 总量

| 口径 | 模块数 | action 数 |
| --- | ---: | ---: |
| 上游 `aden_tools/tools/*` | 104 | 865 |
| Go 已注册（22 个 `Install*`/`Batch*` 安装器） | 99 | 851 |
| 其中 `internal/senders`（不经 integrations 包） | 1 | 9 |

对上表口径的说明：上游 104 个目录里有 1 个不是工具模块（`file_system_toolkits`，只有
`security.py` 与 `__init__.py`，`@mcp.tool` 数为 0），因此 103 个工具模块 / 865 action 才是
可对齐的分母。Go 侧 99 个 integrations 模块 + `senders` 1 个 = 100 个模块，
851 + 9 = 860 个 action，与 865 的差额由下面两节解释。

## 2. 仍未移植（3 个模块 / 3 个 action）

| 上游目录 | action 数 | action 名 | 说明 |
| --- | ---: | --- | --- |
| `attach_file_tool` | 1 | `attach_file` | 把磁盘上的 PDF/图片重新注入下一轮上下文。`internal/tools/tier_defaults.go` 的 `file_ops` 分类已引用 `attach_file`，但没有任何 `Register` 注册它——分类表存在悬空项，需要在 `internal/tools/integrations` 或 `internal/senders` 同级新增实现。 |
| `subdomain_enumerator` | 1 | `subdomain_enumerate` | 子域枚举，纯网络探测。与四个 scanner 同属默认关闭一族，落地时应复用 `HIVE_NETWORK_SCANNERS` 开关。 |
| `tech_stack_detector` | 1 | `tech_stack_detect` | 技术栈指纹识别，同样是默认关闭的网络探测。 |

这三个模块的 action 合计 3 个，是 865 与 860 之间 5 个 action 差额的 3 个。

## 3. 有意不移植（2 个 action，非遗漏）

| 上游 | 未移植 action | 原因 |
| --- | --- | --- |
| `csv_tool` | `csv_sql` | 上游用 DuckDB（native 依赖）；纯 Go 移植不引入 cgo，`gen_catalog.py` 与本仓库 `doc.go` 均已注明排除。 |
| `excel_tool` | `excel_sql` | 同上，`batch3_excel.go` 顶部注释与 `batch3_excel_test.go` 都断言该 action 返回 `ErrUnknownAction`。 |

## 4. 已移植但不在 integrations 包里

| 上游目录 | 实现位置 | 说明 |
| --- | --- | --- |
| `senders_tool`（9 action） | `internal/senders` | 由 `hiveserver.RegisterSenderTools` 注册，受 `email_senders` 特性开关控制，默认关闭。 |

## 5. 各安装器覆盖（22 个入口）

| 安装器 | 模块数 | action 数 |
| --- | ---: | ---: |
| `integrations.New(...).Install`（基础 catalog） | 10 | 165 |
| `InstallBatch2` | 10 | 19 |
| `InstallBatch3Doc` | 2 | 2 |
| `InstallBatch3Excel` | 1 | 6 |
| `InstallBatch3Media` | 2 | 2 |
| `InstallBatch4` | 2 | 34 |
| `InstallBatch5Lane1` | 3 | 26 |
| `InstallBatch5Lane2` | 4 | 32 |
| `InstallBatch5Lane3` | 4 | 39 |
| `InstallBatch5Lane4` | 2 | 21 |
| `InstallBatch6Lane1` | 4 | 43 |
| `InstallBatch6Lane2` | 4 | 45 |
| `InstallBatch6Lane3` | 4 | 29 |
| `InstallBatch6Lane4` | 7 | 33 |
| `InstallBatch7Lane1` | 4 | 31 |
| `InstallBatch7Lane2` | 1 | 54 |
| `InstallBatch7Lane3` | 6 | 32 |
| `InstallBatch7Lane4` | 5 | 39 |
| `Batch7Lane5Install` | 5 | 36 |
| `InstallBatch7Lane6` | 3 | 42 |
| `InstallBatch7Lane7` | 5 | 36 |
| `InstallBatch7Lane8` | 5 | 40 |
| `InstallBatch7Lane9` | 6 | 45 |
| **合计** | **99** | **851** |

> 注：22 个 batch 安装器之间没有重名工具（`TestSessionToolCatalogueInstallsEveryBatch`
> 用「安装器逐个安装后的名字集合 ⊆ 会话目录」+「installed 计数等于 registry 大小」双重校验）。
> `port_scan`、`dns_security_scan`、`ssl_tls_scan`、`http_headers_scan` 四个 scanner 会注册后
> 被 `HIVE_NETWORK_SCANNERS`（或 `hive.config.json` 的 `network_scanners`）过滤，默认不在会话里。
