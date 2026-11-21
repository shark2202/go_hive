# MCP registry 契约（本目录）

本目录是 MCP registry 三个文件的事实规范与样例：

| 文件 | 作用 |
|---|---|
| `index.schema.json` | 远端索引 `registry_index.json`（只 GET，10s 超时，无认证 header、无分页） |
| `config.schema.json` | 本地 `$HIVE_HOME/mcp_registry/config.json`（`index_url` / `refresh_interval_hours`） |
| `installed.schema.json` | 本地 `$HIVE_HOME/mcp_registry/installed.json`（`servers.<name>`：`source`/`manifest`/`transport`/`mode`/`enabled`/`overrides`/health） |
| `examples/` | 三个文件的可用样例 |
| `validate_mcp_registry.py`（`go_hive/scripts/`） | 按 schema 校验，exit 0 才通过 |

## 运行时语义（本契约约束的行为）

`installed.json` 只描述「装了什么」；真正决定模型能看到什么的是装载路径。以下行为已在
`docs/mcp-crosscheck.md` 的 Python ↔ Go 双跑中实测：自建夹具（基线 `85595e30`，§1–§7）与
**真实第三方 server**（基线 `c4eb266d`，§8；`server-everything` 2.3.1 / `mcp-server-time`
2026.11.18，stdio + HTTP+SSE + streamable-HTTP 三条传输）。

- **enabled**：`enabled:false` 的 registry 条目不装载（两侧都遵守）。Go 额外允许
  `configuration.json` 的 `mcp.servers[]` 条目写 `enabled:false`，用来覆盖/剔除同名 registry 条目
  （操作者的显式声明优先于 UI 状态）。
- **mode**：`mode:"cli"` 的 server 不进工具目录，只能通过 `hive mcp run` 从终端调用（两侧一致）。
- **工具命名**：Go 把每个 server 工具注册为 `mcp__<namespace>__<server>__<tool>`，server/tool 中
  `[^A-Za-z0-9_]` 折叠为 `_`（默认 namespace `mcp`，可用 `configuration.json mcp.namespace` 改）。
  `installed.json` 的 `manifest.tools[].name` 仍写 **server 侧原名**；Python 不前缀，直接用原名，
  因此跨实现迁移时不要把 Python 侧工具名写成带 `mcp__` 前缀的形态。
- **reload**：Python 没有 reload 路由，新 server 在下一个 queen 会话生效；Go 有
  `POST /api/mcp/reload`，重连连接池并 bump generation。
- **超时**：Go 解析顺序 `HIVE_MCP_CALL_RESULT_TIMEOUT`（env）> `configuration.json mcp.timeout_seconds`
  > 240s；Python 只有 env（import 时读一次），没有 `configuration.json` 的 MCP 块。
- **transport**：两侧共有 `stdio` / `http` / `unix` / `sse`；`harness` 是 Python 的进程内组表示，
  Go 侧由 `HarnessGroupNames` 按 server 名分派，不写进 `transport` 字段。
  **`http` 指的是 Hive 自家 `/mcp/v1` JSON-RPC 桥，不是 MCP 的 streamable HTTP**：两侧 tools 路径
  对标准 `POST /mcp` 端点都会失败（实测 404/406，两侧一致）。要连标准 streamable-HTTP server 用
  CLI 模式（`hive mcp run`，两侧同一份 mcp2cli 语义，实测各拿到 16 个工具）。

## 校验

```bash
python3 go_hive/scripts/validate_mcp_registry.py index     go_hive/docs/mcp-registry/examples/index.json
python3 go_hive/scripts/validate_mcp_registry.py config    go_hive/docs/mcp-registry/examples/config.json
python3 go_hive/scripts/validate_mcp_registry.py installed go_hive/docs/mcp-registry/examples/installed.json
```

行为差异与双跑证据：`docs/mcp-crosscheck.md`、`docs/e2e-assets/mcp-crosscheck/evidence.json`。
