# 交付报告（2026-11-19 02:50）

> 口径：`RELEASE_PLAN.md` 的 G6 商业发布标准（R1–R10）。
> 代码快照：tag `v1.0.14-go.1`（提交 `d1111162`），工作树干净。

## 1. 结论

| 判定 | 结果 |
| --- | --- |
| R1–R9（功能/分发/文档/许可/安全/可靠性/可观测/质量证据/发布流程） | **满足**（R5 以人工审查 + 离线依赖清单达成，在线 CVE 扫描缺口已声明） |
| R10（真实第三方集成验证） | **不适用（用户决定 2026-11-19）**：本阶段不提供第三方工具凭据，仅提供 LLM 凭据；以支持矩阵「未验证」声明代替 |
| 总判定 | **可对外发布的候选版**；「全部集成已真实验证」不成立 |

## 2. 已验证证据（可复现）

| 项 | 结果 | 复现命令 |
| --- | --- | --- |
| 全量门禁 | 退出码 0（fmt/vet/test/cross/parity/parity-targets/wiring-check） | `cd go_hive/go-hive && make ci` |
| 测试 | **4,160**（全部带 parity 注解或豁免） | 同上 |
| parity 引用 | **4,357** 条，0 失效文件 / 0 失效测试；含生产代码 **830** 条 0 失效符号 | `make parity-targets` |
| QC 全量 | **8/8 PASS**（含重跑 make ci 与冒烟） | `uv run --no-project python go_hive/scripts/qc_check.py --full --smoke` |
| 集成冒烟 | **13/13 PASS**（真二进制 + 真 HTTP） | `bash go_hive/scripts/smoke.sh` |
| 发布包 | 6 归档 + SHA256SUMS，`shasum -c` 全 OK | `bash go_hive/scripts/release.sh` |
| 归档可用性 | 解压 darwin-arm64 → `--version` 正确，`/`、`/api/health`、`/assets/*` 全 200 | 见 §5 |
| cgo | `import "C"` 计数 **0** | `grep -rn 'import "C"' --include=*.go .` |
| 版本 | `hive 1.0.14-go.1` | `./dist/hive --version` |

## 3. 覆盖面

| 子系统 | 交付内容 |
| --- | --- |
| 集成模块 | **102 integrations + senders = 103/104**（`file_system_toolkits` 非工具模块），**863/865 action**；23 个安装器全部接入会话工具表，并有「Install\* 必被引用」守护测试 |
| HTTP 路由 | 194 条 method+path；`NewApp` 注册 113 条 pattern |
| Sentinel | 8 模块移植，100 测试 |
| Colony | spawn → 多 worker（并发上限/排队晋升）→ 汇总 → 终态；agent-spec 解析器已接入 |
| 前端 | React 构建产物 embed 进 `hive-embed`，单二进制自带 dashboard（实测 `/`、深链、assets 200） |
| 分发 | 六平台归档 + SHA256SUMS + `install.sh`（篡改拒装已验证） |
| 文档 | quickstart / configuration / support-matrix / CHANGELOG / release-notes / 检查单 |
| 许可 | LICENSE（Apache-2.0 逐字节）+ NOTICE + THIRD_PARTY_NOTICES（随归档分发） |

## 4. 未满足与已声明差异

### 4.1 R10：真实第三方验证（本阶段不适用）
- 863 个 action 只有 `httptest`/假上游证据；**没有任何一次真实厂商调用**。
- 用户已明确本阶段不提供第三方工具凭据，因此该项不再作为交付阻塞；发布以支持矩阵的「未验证」声明为准。
- 无需凭据的 26 个模块（终端/文件/csv/excel/pdf/arxiv/wikipedia/openmeteo 等）可真实验证。

### 4.2 结构性差异（非遗漏，已声明）
1. `attach_file` 的多模态只到工具结果层：`ToolResult.Attachments` 与 `ImageContent` 已实现，但 `serve` 的 chat runtime 只把文本传给模型。
2. `csv_sql` / `excel_sql` 依赖 DuckDB，明确返回未知 action，不做 stub。
3. `account` 别名参数在多个模块保留但不生效（单凭据运行时）。
4. 六个网络扫描器默认关闭，需 `HIVE_NETWORK_SCANNERS=1` 或配置开启。
5. 在线 CVE 扫描未做（`govulncheck` 因无外网不可安装），依赖清单与离线构建证据在 `DEPENDENCIES.md`。

## 5. 发布包与验收

```
go_hive/go-hive/dist/release/
  hive-1.0.14-go.1-darwin-amd64.tar.gz   13.5 MB
  hive-1.0.14-go.1-darwin-arm64.tar.gz   12.9 MB
  hive-1.0.14-go.1-linux-amd64.tar.gz    13.5 MB
  hive-1.0.14-go.1-linux-arm64.tar.gz    12.6 MB
  hive-1.0.14-go.1-windows-amd64.zip     13.6 MB
  hive-1.0.14-go.1-windows-arm64.zip     12.6 MB
  SHA256SUMS
```

归档根目录：`hive[.exe]  LICENSE  NOTICE  THIRD_PARTY_NOTICES.md  README.md  SHA256SUMS`。
独立验收（解压后直接运行）：`--version` = `hive 1.0.14-go.1`；`serve` 后 `/` 200、`/api/health` 200、`/assets/index-*.js` 200。

## 6. 未做（需发布权限或外部条件）

1. **tag 已打但未推送**：`v1.0.14-go.1` 指向 `d1111162`；`git push origin v1.0.14-go.1` 会触发 `release.yml` 上传 Release。
2. 代码签名/公证（macOS notarization、Windows Authenticode）未做，需证书。
3. R10 按决定不验证：以支持矩阵「未验证」声明为准，不写成可用。
