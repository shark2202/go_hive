# 快速开始

> 版本：`hive 1.0.26-go.1`（Go 模块 `github.com/shark2202/go_hive/go-hive`）
> 验证环境：macOS 26.7.1（darwin/arm64）、Go 1.26.5、`CGO_ENABLED=0`。
> 本页每一条命令都在一个临时 `HIVE_HOME` 下实际执行过；输出为实测摘录，不是示例值。

## 0. 前置条件

| 项 | 要求 |
| --- | --- |
| Go | 1.26.5（`go.mod` 的 `go` 指令） |
| cgo | 不需要，构建固定 `CGO_ENABLED=0` |
| 网络 | 首次构建需要联网拉一次 module cache；之后 `GOPROXY=off` 可离线构建 |
| Node.js | 只有构建 dashboard 的单二进制（`make embed-ui`）才需要 |
| 操作系统 | 本机构建/运行验证于 darwin/arm64；linux、windows 只做了交叉编译 |

首次在新机器上构建前，先联网预热模块缓存（只需一次）：

```bash
cd go_hive/go-hive
go mod download
```

之后所有 `make` 目标都在 `CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod` 下运行。

## 1. 构建并确认版本

```bash
cd go_hive/go-hive
make build
```

实测输出（末两行）：

```text
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build ./...
building ./cmd/...
```

产物是 `dist/hive`：

```bash
./dist/hive --version
```

```text
hive 1.0.26-go.1
```

同目录下的 `dist/hive-cred` 是独立凭据入口（本文不展开，见 `configuration.md`）：

```bash
go build -o dist/hive-cred ./cmd/hive-cred
```

## 2. 指定 HIVE_HOME

`HIVE_HOME` 决定会话、凭据、日志等状态写到哪里；不设置时默认 `~/.hive`。本文用一个独立目录，避免污染已有数据：

```bash
export HIVE_HOME="$HOME/.hive-quickstart"
```

目录不需要预先创建：服务在第一次真正写文件时才会建出需要的子目录。成功跑完一轮对话后，实测布局是：

```text
$HIVE_HOME/
├── credentials/
│   ├── credentials/          # 加密后的凭据文件
│   └── metadata/index.json   # 凭据索引（名称、键名、过期时间，不含明文）
└── queens/default/sessions/<session_id>/conversations/
    ├── meta.json
    ├── cursor.json
    └── parts/0000000000.json …
```

## 3. 启动服务

```bash
./dist/hive serve
```

实测输出（省略后续访问日志）：

```text
Hive API server running on http://127.0.0.1:8787
Health:     http://127.0.0.1:8787/api/health
Sessions:   0 colony, 0 queen-only

Press Ctrl+C to stop
```

默认监听 `127.0.0.1:8787`。常用参数：

```bash
./dist/hive serve --host 0.0.0.0 --port 8899
```

另一终端做健康检查：

```bash
curl -sS http://127.0.0.1:8787/api/health
```

实测（HTTP 200，响应做了换行整理）：

```json
{
  "agents_loaded": 0,
  "request_executor": {"max_workers": 32, "active": 0, "queued": 0},
  "resources": {"verdict": "unknown"},
  "sessions": 0,
  "status": "ok"
}
```

## 4. 创建会话

```bash
curl -sS -X POST http://127.0.0.1:8787/api/sessions \
  -H 'Content-Type: application/json' -d '{}'
```

实测响应（HTTP 201）：

```json
{
  "agent_path": "",
  "colony_id": null,
  "colony_spawned": false,
  "description": "",
  "goal": "",
  "has_worker": false,
  "loaded_at": 1795020229.836253,
  "node_count": 0,
  "queen_id": null,
  "queen_name": null,
  "queen_phase": "planning",
  "queen_supports_images": true,
  "session_id": "session_20261119_005445_bc12c65a",
  "spawned_colony_id": null,
  "uptime_seconds": 0
}
```

`session_id` 形如 `session_YYYYMMDD_HHMMSS_<8 位 hex>`。后续命令用实际返回的 id 替换 `<session_id>`。

请求体支持的可选键：`model`、`initial_prompt`、`colony_id`、`session_id`、`queen_name`、`queen_resume_from`、`initial_phase`（`colony` / `independent`）。

## 5. 没有模型 key 时的行为

这是最容易误判的一步：**会话能建、能列，但投递消息会明确失败，不会伪造成功。**

```bash
curl -i -X POST "http://127.0.0.1:8787/api/sessions/<session_id>/chat" \
  -H 'Content-Type: application/json' -d '{"message":"hello"}'
```

实测：

```text
HTTP/1.1 503 Service Unavailable
Content-Type: application/json

{"error":"No LLM configured for this session"}
```

此时会话仍然可以被列出：

```bash
./dist/hive session list
```

```text
SESSION                                   COLONY                PHASE         WORKER
------------------------------------------------------------------------------------------
session_20261119_005445_bc12c65a                                planning      no
```

其他已实测的错误分支：

| 请求 | 状态码 | 响应体 |
| --- | --- | --- |
| 未知会话 chat | 404 | `{"error":"session not found"}` |
| 空消息（`{"message":"  "}`） | 400 | `{"error":"message is required"}` |
| 停止不存在的会话 | 404 | `{"error":"Session 'no_such_session' not found"}` |

## 6. 接模型

服务进程读取 key 与环境变量；**改完环境变量要重启 `hive serve`**。

### 6.1 OpenAI 兼容接口

```bash
export OPENAI_API_KEY="sk-..."          # 必填
export OPENAI_BASE_URL="https://api.openai.com/v1"   # 可选；默认就是这个地址

./dist/hive serve
```

创建会话时显式指定模型（也可以省略，默认 `gpt-4o-mini`）：

```bash
curl -sS -X POST http://127.0.0.1:8787/api/sessions \
  -H 'Content-Type: application/json' -d '{"model":"gpt-4o-mini"}'
```

投递消息：

```bash
curl -i -X POST "http://127.0.0.1:8787/api/sessions/<session_id>/chat" \
  -H 'Content-Type: application/json' -d '{"message":"ping"}'
```

实测：

```text
HTTP/1.1 200 OK

{"delivered":true,"status":"queen"}
```

请求带 `Authorization` 头（`Bearer` + API key），发往 `POST <base>/chat/completions`；基址没有 `/v1` 时会自动补上。

### 6.2 Anthropic Messages 接口

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
export ANTHROPIC_BASE_URL="https://api.anthropic.com"   # 可选

./dist/hive serve
```

```bash
curl -sS -X POST http://127.0.0.1:8787/api/sessions \
  -H 'Content-Type: application/json' -d '{"model":"claude-haiku-4-5-20251001"}'
```

实测上游请求形态（本地假上游记录）：

```json
{"path": "/v1/messages", "x_api_key": "<key>", "anthropic_version": "2023-06-01",
 "model": "claude-haiku-4-5-20251001", "max_tokens": 8192}
```

协议按模型名选择：`anthropic/*`、`claude-*`、`kimi/*`、`hive/*` 走 Anthropic Messages；`openrouter/*` 以及其余模型走 OpenAI-compatible。详见 `configuration.md`。

> 说明：本次文档验证没有可用的真实厂商 key，模型路径是用本地假上游（实现了同样的 `POST /v1/chat/completions` 与 `POST /v1/messages`）跑通的，因此请求形状、鉴权头、会话落盘都被验证；厂商侧的额度/权限仍需你自己的 key。

### 6.3 用配置文件接模型（重启后生效）

```json
{
  "llm": {
    "model": "gpt-4o-mini",
    "provider": "openai",
    "api_base": "http://127.0.0.1:8900/v1",
    "api_key_env_var": "MY_TEST_KEY"
  }
}
```

实测：只设置 `MY_TEST_KEY`、不设置 `OPENAI_API_KEY`，新建会话不传 `model`，请求仍以 `Bearer <MY_TEST_KEY>` 发往配置里的 `api_base`，模型为 `gpt-4o-mini`。

> 上面的 `api_base` 是文档验证用的本地假上游地址。实际使用时填你的网关地址，或者删掉 `api_base` 让它走 provider 默认值。完整优先级见 `configuration.md`。

## 7. 用 CLI 发消息

```bash
./dist/hive chat <session_id> "hello" --server http://127.0.0.1:8787
```

实测输出：

```text
Sent. Tail the SSE stream at http://127.0.0.1:8787/api/sessions/<session_id>/events
```

`--server` 默认就是 `http://127.0.0.1:8787`。服务未启动或端口不对时：

```text
Could not reach server at http://127.0.0.1:8787: ... connection refused
Tip: pass --cold to read on-disk sessions, or start 'hive serve' first.
```

## 8. 看回复与落盘位置

```bash
find "$HIVE_HOME/queens/default/sessions/<session_id>/conversations" -type f | sort
```

实测：

```text
.../conversations/cursor.json
.../conversations/meta.json
.../conversations/parts/0000000000.json
.../conversations/parts/0000000001.json
```

```bash
cat "$HIVE_HOME/queens/default/sessions/<session_id>/conversations/parts/0000000001.json"
```

```json
{"content":"pong from fake openai","role":"assistant","seq":1}
```

用户消息在同一目录的 `0000000000.json`，带 `is_client_input: true`。同样的 `session_id` 再次创建会话时，会从该目录恢复上下文（E2E 覆盖；本文未复跑）。

> 上面这条回复来自文档验证用的本地假上游；接真实 key 时这里就是模型的实际回复文本。

## 9. 订阅会话事件（SSE）

另开一个终端，先订阅：

```bash
curl -N "http://127.0.0.1:8787/api/sessions/<session_id>/events?types=client_input_received"
```

再在服务端投递一条消息。实测收到：

```text
event: client_input_received
data: {"colony_id":null,"correlation_id":"…","data":{"content":"third turn","image_count":0},
       "execution_id":"<session_id>","node_id":"queen","seq":3,"stream_id":"queen",
       "timestamp":"2026-11-19T00:45:44.604866","type":"client_input_received"}
```

说明：

- 事件名是小写；`?types=` 按事件名精确过滤，写 `CLIENT_INPUT_RECEIVED` 过滤不到。
- 每 20 秒发一行注释心跳 `: heartbeat`。
- 这里只有会话事件总线事件（如 `client_input_received`），**没有**逐 token 的模型流。

## 10. 停止会话与服务

```bash
./dist/hive session list
./dist/hive session stop <session_id>
```

实测：

```text
Stopped session session_20261119_005445_f9b1fc20
```

服务端按 `Ctrl+C` 优雅退出（最多等 6 秒）；再按一次强制退出。

## 11. 可选：加载一个 colony

```bash
mkdir -p "$HIVE_HOME/colonies/docs_probe"
cat > "$HIVE_HOME/colonies/docs_probe/metadata.json" <<'JSON'
{"queen_name":"queen_operations","queen_session_id":"session_docs_probe","created_at":"2026-11-19T00:00:00Z"}
JSON
cat > "$HIVE_HOME/colonies/docs_probe/worker.json" <<'JSON'
{
  "name": "Research Worker",
  "description": "Documentation probe worker",
  "system_prompt": "You are a worker in a colony.",
  "agent_type": "event_loop",
  "tools": ["read_file", "write_file", "terminal_exec"],
  "goal": {"description": "Collect facts for the docs", "success_criteria": ["facts recorded"], "constraints": []}
}
JSON

./dist/hive colony list
./dist/hive serve --port 8896 --colony docs_probe
```

实测启动输出：

```text
Loaded colony: docs_probe → session session_20261119_005452_b01a10a6

Hive API server running on http://127.0.0.1:8896
...
Sessions:   1 colony, 0 queen-only
```

`serve --colony` 需要一个可解析的模型 provider；没有 key 时会打印 `Warning: colony runtime unavailable ...`（代码路径），会话仍以冷记录存在。colony 目录与 worker 定义格式见 `configuration.md`。

## 12. 下一步

| 想知道 | 看 |
| --- | --- |
| `HIVE_HOME` 每个目录是什么、模型/凭据怎么解析 | `configuration.md` |
| 哪些集成已实现、哪些没移植、平台/离线限制 | `support-matrix.md` |
| 后续交付如何保证旧用户无损升级、什么算破坏性变更 | `compatibility-policy.md` |
| 本版本包含什么、已知缺口 | `CHANGELOG.md` |
| 构建与质量门禁 | `../README.md`、`../14-quality-assurance.md` |
