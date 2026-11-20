# 上游模块覆盖清单（收尾版）

> 更新：2026-11-19 02:35。数据来源：`tools/src/aden_tools/tools/*` 的目录名与 `@mcp.tool` 计数，
> 对比 `go_hive/go-hive/internal/tools/integrations` 已注册的 `Tool.ID`。

## 1. 总量

| 口径 | 模块数 | action 数 |
| --- | ---: | ---: |
| 上游 `aden_tools/tools/*` | 104 目录 | 865 |
| 上游可用于对齐的工具模块 | **103** | 865 |
| Go 已注册（23 个安装器，含 batch8） | **102** | **854** |
| 另：`internal/senders`（不经 integrations 包） | 1 | 9 |
| **Go 合计** | **103** | **863** |

上游 104 个目录里 `file_system_toolkits` 只有共享 helper（`@mcp.tool` 为 0），不计入工具模块。

## 2. 仍未移植

**模块：0。** 差额 2 个 action 全部为有意排除，见下节。

## 3. 有意不移植（2 个 action，非遗漏）

| 上游 | 未移植 action | 原因 |
| --- | --- | --- |
| `csv_tool` | `csv_sql` | 上游用 DuckDB（native 依赖）；纯 Go 不做 cgo，`gen_catalog.py` 与 `doc.go` 均已注明排除。 |
| `excel_tool` | `excel_sql` | 同上；`excel.go` 与测试断言该 action 返回 `ErrUnknownAction`。 |

## 4. 已移植但不在 integrations 包里

| 上游目录 | 实现位置 | 说明 |
| --- | --- | --- |
| `senders_tool`（9 action） | `internal/senders` | 由 `hiveserver.RegisterSenderTools` 注册，受 `email_senders` 开关控制，默认关闭。 |

## 5. 各安装器覆盖（23 个入口）

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
| `InstallBatch8Final` | 3 | 3 |
| **合计** | **102** | **854** |

> 注：安装器之间无重名工具（`TestSessionToolCatalogueInstallsEveryBatch` 用「逐个安装后名字集合 ⊆ 会话目录」+「installed 计数等于 registry 大小」双重校验）。
> `port_scan`/`dns_security_scan`/`ssl_tls_scan`/`http_headers_scan`/`subdomain_enumerate`/`tech_stack_detect` 六个网络扫描器与 `risk_score` 和上游一样默认注册进会话目录，无 Go 独有开关。
> 它们会向模型指定的主机发探测流量（TCP/TLS/HTTP/DNS 查询），调用方只能扫描已获授权的目标。

## 6. 已知差异（非模块缺口）

1. **attach_file 的多模态注入只到工具结果层**：`ToolResult.Attachments` 与 `ImageContent` 已实现，但 `serve` 的 chat runtime 只把文本传给模型，图片块与 PDF `file` 块到不了 LLM。结果 JSON 用 `inline` 字段如实标注，不声称模型看到了字节。
2. `account` 别名参数在多个模块保留但不生效（单凭据运行时）。
3. 依赖 DuckDB 的 2 个 SQL action 明确返回未知 action，而不是假装支持。
