# Webhook 语义实测：重复头 / 重复 query / body 上限

- Lane：`webhook-semantics`（worktree `/Users/mac/hive/.wt/webhook-semantics`，基线 `9bd4bd44`）
- 上游参照：`core/framework/host/webhook_server.py`（真实 aiohttp **3.13.5**，Python 3.11.16，`/Users/mac/hive/.venv`）
- Go 侧：`go_hive/go-hive/internal/server/webhook_server.go`
- 结论一句话：**上游 `dict(request.headers)` / `dict(request.query)` 均取首值（不是末值）；body 上限为 aiohttp 默认 `client_max_size = 1 MiB`，且累积体 ≥ 1 MiB 即拒绝（恰好 1 MiB 也拒），响应为 `400 {"error": "Failed to read request body"}`——该路径不产生 413。Go 已按此对齐，另有 3 条无法/不应在本 lane 修的微差登记于文末。**

## 1. 测量方法

脚本：`probe_aiohttp.py`（本目录）。要点：

1. 直接启动**真实** `framework.host.webhook_server.WebhookServer`（`webhook_server.py` 原文，未改动），监听 `127.0.0.1:0`，`WebhookServerConfig` 默认不设 `client_max_size`。
2. 订阅真实 `EventBus` 的 `WEBHOOK_RECEIVED`，捕获 `event.data`——即 `_handle_request` 里 `dict(request.headers)` / `dict(request.query)` 的**实际产物**。
3. 用 **raw socket** 发 HTTP/1.1 请求（首行 + `Host` + 自定行 + `Content-Length` + `Connection: close`），完整控制重复头字节与 body 精确大小；响应按原始字节解析（状态行/头/体）。
4. C 题用恰好大小的合法 JSON 体：`{"pad":"xxx…"}`，尺寸 = 1048575 / 1048576 / 1048577 / 1572864。
5. 辅助组：同一 aiohttp 版本另起最小 echo app，直接打印 `getall()` 与 `dict()` 的对照（证明多值字典里两个值都在，`dict()` 只是扁平化取首值）。
6. 踩坑记录：服务端与客户端若共用同一 asyncio event loop，客户端阻塞会饿死服务端（第一次运行全部 `status=None`）。最终脚本把 raw 客户端放进 `asyncio.to_thread` 执行，服务端在 loop 内正常响应。

复现命令：

```bash
cd /Users/mac/hive/.wt/webhook-semantics
VIRTUAL_ENV=/Users/mac/hive/.venv uv run --no-project python \
  go_hive/docs/e2e-assets/webhook-semantics/probe_aiohttp.py
```

原始产物（本目录）：

| 文件 | 内容 |
| --- | --- |
| `probe_aiohttp.py` | 上游实测脚本 |
| `probe-results.json` | 结构化原始结果（机器可读） |
| `probe-aiohttp-stdout.txt` | 同次运行的 stdout（含逐探针进度行 + 完整 JSON） |
| `go-current-state.txt` | **修正前** Go 侧同三题探针输出（临时测试，已删除代码） |
| `go-after-state.txt` | **修正后** 新增 Go 表驱动测试 `-v` 输出 |

## 2. 原始结果（逐题引用）

### A) 重复请求头 —— 上游取**首值**

请求（`probe-results.json` → `probes.A_duplicate_headers.request.header_lines`）发送了两组同名头：`X-Test: alpha` + `X-Test: beta`；`X-Mix-Case: FirstCase` + `x-mix-case: secondcase`；`X-Hub-Signature-256: sha256=aaaa` + `sha256=bbbb`。真实 handler 捕获到的 `dict(request.headers)`：

```json
{
  "Host": "127.0.0.1:51720",
  "Content-Type": "application/json",
  "X-Test": "alpha",
  "X-Mix-Case": "FirstCase",
  "x-mix-case": "FirstCase",
  "X-Hub-Signature-256": "sha256=aaaa",
  "Content-Length": "2",
  "Connection": "close"
}
```

- `X-Test` → `"alpha"`（**首值**，不是 `"beta"`）；`X-Hub-Signature-256` → `"sha256=aaaa"`（首值）。
- 辅助 echo app（`probes.aux_multidict_structure`）证明多值字典原样保存两值、`dict()` 只取首值：`getall_xtest = ["alpha","beta"]`，`get_xtest = "alpha"`。
- 大小写变体（`X-Mix-Case` + `x-mix-case`）在上游 dict 里**两种拼写各占一个 key，值都取首值 `FirstCase`**（MultiDict 的 `keys()` 按插入拼写产出、`__getitem__` 大小写不敏感取首值）。`getall_xmixcase = ["FirstCase","secondcase"]`。
- `Host` 作为普通请求头出现在 dict 中（`"127.0.0.1:51720"`，含端口）。

Go 修正前（`go-current-state.txt`）：

```
A captured headers={"Connection":"close","Content-Length":"2","Content-Type":"application/json","Host":"127.0.0.1:51824","X-Hub-Signature-256":"sha256=bbbb","X-Mix-Case":"secondcase","X-Test":"beta"}
```

即 Go 取**末值**（`beta` / `secondcase` / `bbbb`）——与上游相反；`Host` 已额外加入（与上游一致）。

**结论 A：语义需对齐为「同名头取首值」；Go 已改 `values[len(values)-1]` → `values[0]`。**

### B) 重复 query 参数 —— 上游取**首值**

```json
// GET /hook?a=1&a=2  → event.data.query_params
{"a": "1"}                       // 首值；不是 "2"
// GET /hook?a=1&a=2&b=3&empty=&a=4
{"a": "1", "b": "3", "empty": ""}
```

辅助 echo app：`raw_query_string = "a=1&a=2&b=3&empty=&a=4"`，`getall_a = ["1","2","4"]`，`get_a = "1"`。空值保持 `""`。

Go 修正前（`go-current-state.txt`）：

```
B target="/hook?a=1&a=2" ... query_params={"a":"2"}
B target="/hook?a=1&a=2&b=3&empty=&a=4" ... query_params={"a":"4","b":"3","empty":""}
```

即 Go 取**末值**（`2` / `4`）。

**结论 B：语义需对齐为「重复参数取首值」；Go 已改 `values[len(values)-1]` → `values[0]`。**

### C) body 上限 —— 上游 aiohttp 默认 **1 MiB**，≥1 MiB 即 400（无 413）

`WebhookServerConfig` 未设 `client_max_size`，`web.Application` 默认 `1024**2`。aiohttp 3.13.5 源码 `web_request.py:665-684` 在 `read()` 里边读边累积、`body_size >= client_max_size` 即 `raise HTTPRequestEntityTooLarge`；`webhook_server.py:111-117` 的裸 `except Exception` 把它转成 `400 {"error": "Failed to read request body"}`（该异常是 `Exception` 子类，不会冒出 413）。

实测（`probe-results.json` → `probes.C_body_limit.by_size`，请求均完整送达）：

| body 字节 | 状态 | 响应体（原始字节） | 捕获事件数 |
| --- | --- | --- | --- |
| 1048575（1 MiB−1） | `202` | `{"status": "accepted"}` | 1 |
| 1048576（恰好 1 MiB） | **`400`** | `{"error": "Failed to read request body"}` | 0 |
| 1048577（1 MiB+1） | `400` | `{"error": "Failed to read request body"}` | 0 |
| 1572864（1.5 MiB） | `400` | `{"error": "Failed to read request body"}` | 0 |

即：**接受 ≤ 1 MiB−1（1048575）；≥ 1 MiB（含恰好 1048576）拒绝，400 + 上述文案；此路径不存在 413。**

Go 修正前（`go-current-state.txt`）：1048575 / 1048576 / 1048577 / 1572864 / 10485760 全部 `202` 并入事件；仅 10485761 返回 `413 {"error":"Failed to read request body"}`。即上限 10 MiB 且超限 413。

**结论 C：Go 上限改为 1 MiB、按 `>= 1 MiB` 拒绝、状态码 400、文案保持 `Failed to read request body`。已改。**

## 3. Go 代码改动（仅 `internal/server/webhook_server.go` 及其测试）

| 位置 | 改动 |
| --- | --- |
| 文件头常量 | 新增 `const webhookBodyLimitBytes int64 = 1 << 20`（注释记录 aiohttp 默认 `client_max_size`、`>=` 语义、实测三边界与证据路径） |
| `handleRequest` | `readLimitedBody(r, maxWebhookBodyBytes)` → `readWebhookBody(r, webhookBodyLimitBytes)`；任意读错误统一 `400 {"error": "Failed to read request body"}`（删除 413 分支，同步删 `errors` import） |
| 新增 `readWebhookBody` | `io.ReadAll(io.LimitReader(r.Body, limit))` 后按 `len >= limit` 拒绝——`LimitReader` 上限即 `limit`，恰好 `limit` 与超限都判 `errBodyTooLarge`，与 aiohttp `>=` 判定一致 |
| `webhookHeaders` | 同名头取 `values[0]`（首值）；保留 `Host` 回填（上游 dict 含 Host） |
| `webhookQueryParams` | 重复参数取 `values[0]`（首值）；空值保持 `""` |

未动 `http_limits.go`（该文件属 limits lane；`maxWebhookBodyBytes = 10 MiB` 自此在 webhook 路径**不再被引用**，见 §5 残留项）。

## 4. 测试

新增 `internal/server/webhook_semantics_parity_test.go`（全部带 `// Parity: framework/host/webhook_server.py::WebhookServer._handle_request` 注解，raw socket 发同名头以保真）：

- `TestWebhookDuplicateHeadersKeepFirstValueParity`：两条同名头 / 三条同名头 / 大小写变体，断言首值 + `Host` + `Content-Length`。
- `TestWebhookDuplicateQueryParamsKeepFirstValueParity`：`?a=1&a=2`、`?a=1&a=2&b=3&empty=&a=4`，整 map 断言首值与空值。
- `TestWebhookBodyLimitAiohttpDefaultParity`：`1 MiB−1` → 202 且发事件；`恰好 1 MiB` → 400 `{"error":"Failed to read request body"}` 且不发事件；`1 MiB+1` → 400 同上（任务要求的 `1MiB−1`/`1MiB+1` 之外补测恰好 1 MiB）。
- 同步修正既有 `routes_session_triggers_test.go::TestWebhookServerResponses` 中固化旧语义的断言（`?x=1&x=2` 由 `want the last value` 改为首值 `1`，附 Parity 注释）。

`go-after-state.txt` 为上述测试 `-v` 原始输出（全 PASS）。

## 5. 门禁

| 检查 | 结果 |
| --- | --- |
| `gofmt -l .`（go-hive 全模块） | 空 |
| `go vet ./...` | exit 0 |
| `go test ./internal/server -count=1` | `ok github.com/shark2202/go_hive/go-hive/internal/server 16.387s` |
| `bash go_hive/scripts/merge_postcheck.sh` | exit 0（gofmt clean；wiring_check 21 文件 68 项全命中；module inventory PASS；arch 生成物 7 个一致） |

## 6. 残留不确定项 / 登记（本 lane 未修，不猜测）

1. **头名大小写拼写**：上游保留 wire 原始拼写（如 `x-mix-case`），Go `net/http` 解析时统一 canonical（`X-Mix-Case`）；且上游 CI 变体的重复头会在 `dict()` 里保留两种拼写各一个 key（值同为首值），Go 只有 canonical 一个 key。**值语义已对齐（首值），拼写层差异无法在 `net/http` 之上修复**，登记。
2. **JSON 序列化形态**：上游 `json_response` 用 `json.dumps` 默认分隔符（`{"error": "Failed to read request body"}`，40 字节、无尾换行、`ensure_ascii=True`）；Go `writeJSON` 为紧凑 JSON + 尾换行（`{"error":"Failed to read request body"}\n`）。这是**全端点既有约定**（202 同样 `{"status":"accepted"}\n` vs `{"status": "accepted"}`），非本 lane 引入；消息文本逐字一致，登记（与 `docs/triggers-webhook.html` 第 126 行已登记的编码差异同族）。
3. **连接收尾**：超限响应后 aiohttp 走 lingering-close（`web_protocol.py:624-651`，最多 `lingering_time` 内继续读掉剩余体）；Go 在 handler 返回后最多 drain 256 KiB（`net/http` 内建）即关闭。状态码/响应体不受影响，客户端若在收到 400 后仍狂写可能更早感知连接关闭；未列为需对齐项，登记备查。
4. **`http_limits.go` 的 `maxWebhookBodyBytes`（10 MiB）成为死常量**：webhook 路径已改用 `webhook_server.go` 内的 `webhookBodyLimitBytes`。按本 lane 范围未动该文件，建议 limits lane 后续删除/改名。
5. **文档仍描述旧 Go 行为，需后续更新**（本 lane 仅改代码+测试+本目录）：`docs/triggers-webhook.html` 第 64/65 行（重复头/query 取末值）、第 66/125 行（Go 10 MiB）、响应表 **413 行**（上游该路径永不返回 413，应删除该行并并入 400）、`docs/alignment-differences.md` 第 64 行 ③、`docs/webhook-guide.html` 第 119 行（413 / 10 MiB）。
6. **版本依赖**：实测基于仓库锁定的 aiohttp 3.13.5（`uv.lock` 约束 `>=3.9.0`）。`client_max_size` 默认 1 MiB 与 `>=` 判定在 3.9+ 代码路径一致，但若上游未来升级版本，建议重跑本脚本复核（脚本可直接复用）。
