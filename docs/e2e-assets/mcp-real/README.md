# 真实第三方 MCP server 双跑证据

这是 `go_hive/docs/mcp-crosscheck.md` §8 的机器证据目录：Python hive 与 Go hive 对**同一批真实
第三方 MCP server** 双跑的结果，不含自建夹具 server。

| 文件 | 内容 |
|---|---|
| `servers.json` | 两侧共同输入的 server 条目（stdio / sse / http 各一条或多条） |
| `call-specs.json` | 两侧共同执行的调用矩阵（label / tool / arguments） |
| `raw/<entry>.json` | 本证据集自己抓的 server 原始 JSON-RPC：`tools/list`、每次 `tools/call` 的原始响应、SSE/streamable-HTTP 传输元数据 |
| `python-side.json` | Python 侧 dump：register→resolve→load 的工具面、schema sha、执行器调用、裸 `MCPClient` 结果形状、disable/enable、崩溃重连 |
| `go-side.json` | Go 侧同结构 dump（工具名为 `mcp__<ns>__<server>__<tool>`，另存命名空间前缀） |
| `diff.json` | 逐维度判定；`exceptions` = 已解释的明确例外，`unexpected` = 未知差异（本次 0） |
| `summary.md` | 人读差异表 |
| `commands.md` | 本次运行的全部命令与输出（含两侧 CLI 模式 `hive mcp run`） |
| `server-versions.json` | 被测 server 的包与版本、node/uv 版本 |

重跑：

```bash
bash go_hive/scripts/mcp_real_crosscheck_run.sh    # 需要 node/npx、uvx 与网络
```

被测 server：`@modelcontextprotocol/server-everything@2.3.1`（npm）、`mcp-server-time==2026.11.18`
（PyPI，stdio 与原生 streamable-HTTP）、`mcp-proxy==0.15.0`（PyPI，stdio→HTTP+SSE / streamable-HTTP 桥）。
