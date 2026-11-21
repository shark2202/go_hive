# 真实第三方 MCP server 双跑差异表

## everything-http (http)

- 两侧 tools 路径都连不上（both sides failed to connect）；raw 直连拿到 16 个工具
  - Python: Failed to list tools via HTTP: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
  - Go: Failed to list tools via HTTP: HTTP 406: {"jsonrpc":"2.0","id":"server-error","error":{"code":-32600,"message":"Not Acceptable: Client must accept application/json"}}

## everything-sse (sse)

- 工具清单：raw 16 / Python 16 / Go 16；名字与 server 一致：Python=True，Go(去命名空间)=True
- schema 透传：16 个工具两侧都等于 `{type,properties,required}` 归一化后的 server schema；与 raw 不一致：Python=0，Go=0；两侧互不一致：0
- 调用：8 个用例，逐字段一致 8，不一致 0
- 生命周期：disable 后两侧 0 个工具=True；enable 后计数相等=True（Python 16 / Go 16）
- 传输：连接 Python=True Go=True；工具数 Python=16 Go=16

## everything-stdio (stdio)

- 工具清单：raw 16 / Python 16 / Go 16；名字与 server 一致：Python=True，Go(去命名空间)=True
- schema 透传：16 个工具两侧都等于 `{type,properties,required}` 归一化后的 server schema；与 raw 不一致：Python=0，Go=0；两侧互不一致：0
- 调用：8 个用例，逐字段一致 8，不一致 0
- 生命周期：disable 后两侧 0 个工具=True；enable 后计数相等=True（Python 16 / Go 16）
- 崩溃恢复：杀掉 server 进程 Python 4 个 / Go 4 个；杀后调用成功 Python=True Go=True
- 传输：连接 Python=True Go=True；工具数 Python=16 Go=16

## time-http (http)

- 两侧 tools 路径都连不上（both sides failed to connect）；raw 直连拿到 3 个工具
  - Python: Failed to list tools via HTTP: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
  - Go: Failed to list tools via HTTP: HTTP 404: Not Found

## time-stdio (stdio)

- 工具清单：raw 3 / Python 3 / Go 3；名字与 server 一致：Python=True，Go(去命名空间)=True
- schema 透传：3 个工具两侧都等于 `{type,properties,required}` 归一化后的 server schema；与 raw 不一致：Python=0，Go=0；两侧互不一致：0
- 调用：2 个用例，逐字段一致 2，不一致 0
- 生命周期：disable 后两侧 0 个工具=True；enable 后计数相等=True（Python 3 / Go 3）
- 崩溃恢复：杀掉 server 进程 Python 4 个 / Go 4 个；杀后调用成功 Python=True Go=True
- 传输：连接 Python=True Go=True；工具数 Python=3 Go=3

## 明确例外（非未知差异）

- **everything-stdio / tool_name_sanitization**：Go's NamespacedMCPToolName collapses every run of characters outside [A-Za-z0-9_] to '_' in the tool name too, so the model sees <server tool with '_'>; Python registers the server's own name. Execution still targets the server's real tool name in both implementations. Collisions are possible in Go when a server advertises names that differ only in those characters.
- **everything-stdio / unknown_tool_message**：Go reports the namespaced tool name it looked up and a did-you-mean candidate; Python reports the remote name. The remote names differ by design (Go registers mcp__<server>__<tool>).
- **time-stdio / unknown_tool_message**：Go reports the namespaced tool name it looked up and a did-you-mean candidate; Python reports the remote name. The remote names differ by design (Go registers mcp__<server>__<tool>).
- **everything-sse / tool_name_sanitization**：Go's NamespacedMCPToolName collapses every run of characters outside [A-Za-z0-9_] to '_' in the tool name too, so the model sees <server tool with '_'>; Python registers the server's own name. Execution still targets the server's real tool name in both implementations. Collisions are possible in Go when a server advertises names that differ only in those characters.
- **everything-sse / unknown_tool_message**：Go reports the namespaced tool name it looked up and a did-you-mean candidate; Python reports the remote name. The remote names differ by design (Go registers mcp__<server>__<tool>).

未知差异：0
