# M5 实现笔记 —— LLM 代理真实透传（Anthropic Messages + images generations）

> 状态：M5（2026-11-23）。范围=README §4 的 M5 行：**LLM 代理（Anthropic Messages 透传 +
> images generations + 路由配置）**。
>
> 口径：只实现 `contracts/observed-contract.json` / 客户端源码能证明的端点与字段。M3 的 Bearer
> 校验边界原样保留；**有效凭据不再返回 `llm_proxy_not_implemented`**。上游 base URL、凭据、鉴权头
> 风格全部显式配置；模型路由、计费、限流、真实图片后端仍是 `UNKNOWN`，没有实现也没有猜。
> 未登记路径保持 404/405，绝不以空 200 冒充。

## 1. 交付物与验证路径

| 层 | 实现 | 验证 |
| --- | --- | --- |
| L1 Messages | `internal/httpapi/llm.go` + `llm_relay.go`：非流式/SSE 透传、4xx/5xx、超时、取消、body 上限 | `internal/httpapi/m5_contract_test.go`（fake upstream，真实 HTTP） |
| L1 Images | `handleLLMImages`：JSON 逐字转发、Bearer 上游凭据、状态/响应透传 | `TestLLMImagesPassThrough` / `TestLLMImagesUpstreamErrorsForwarded` |
| L1 配置 | `internal/config` + `cmd/hive-cloud` flags/env/config.json；URL/auth 启动校验 | `internal/config/config_test.go:TestLLMUpstreamConfig`、`TestLLMProxyConfigValidation` |
| L2 真客户端 | `harness/llm-proxy-smoke`：真实 `go_hive` `llm.NewHTTPProvider` 的 `Complete` / `ACompleteStream` + 真实 `hive-cloud` 二进制 + 本地 fake upstream | `scripts/m5-smoke.sh` → `evidence/m5-smoke.json`，11 项 |
| 回归 | M1/M2/M3 冒烟保留；M3 的 `hive` keycheck 仍把 400 视为有效 | `./scripts/smoke.sh`（四阶段全过） |

`go test ./...` 覆盖 cloud module；harness 是嵌套 module，单独 `go vet/go build`。

## 2. 证据基础（只做有证据的部分）

### 2.1 Messages

| 事实 | 证据 |
| --- | --- |
| 端点 `POST /v1/messages`，Anthropic Messages 请求/响应/SSE 格式 | `go_hive/go-hive/internal/llm/keycheck.go:564-569`（固定 endpoint + `anthropic-version`）；`core/framework/llm/litellm.py`（`hive/` → `anthropic/`，Bearer）；`go_hive/go-hive/internal/llm/stream_anthropic.go`（客户端 SSE 解析） |
| 鉴权 `Authorization: Bearer <streamToken/JWT>` | `keycheck.go` 的 `checkAnthropicCompatible(..., true)`；`litellm.py::rewrite_proxy_model` / `_api_base_needs_bearer_auth`；M3 已实现 |
| 401 `{"error":{"type":"hive_stream_token_invalid", ...}}` | `litellm.py::_is_hive_stream_token_invalid`（只锚定 `type`） |
| 真实客户端会发 `anthropic-version`、`content-type`，可能发 `anthropic-beta` | `stream_anthropic.go::anthropicRequestHeaders`、`keycheck.go` 的请求头 |
| 流式事件是 Anthropic SSE（`message_start` / `content_block_delta` / `message_delta` / `message_stop` 等） | `stream_anthropic.go::parseAnthropicStream` |

**没有证据的**：真实云的模型路由表、计费、限流、上游 provider key、成功响应的额外字段、真实云 SSE
事件子集。因此本实现**不解析、不改写** body/成功响应/SSE；原样转发上游字节。`model` 字段不改写。

### 2.2 Images

| 事实 | 证据 |
| --- | --- |
| 端点 `POST /v1/images/generations` | `go_hive/go-hive/internal/tools/integrations/image.go:36-37`；`tools/src/aden_tools/tools/image_gen_tool/image_gen_tool.py:44-45` |
| 鉴权 `Authorization: Bearer <HIVE_API_KEY>`（与 LLM 调用同一个 per-user proxy token） | `image.go` 的 `req.Header.Set("Authorization", "Bearer "+token)`；Python 模块 docstring |
| 请求 JSON 字段：`model,prompt,size,quality,n,output_format`，可选 `image`（base64 数组，edits） | `image.go` 的 body 构造；Python 的 `_load_reference_image` / payload |
| 响应 `{"data":[{"b64_json":...}], "usage":...}` | `image.go` 的成功解析；Python 的 `_extract_images` |
| 错误体嵌套 `{"error":{"message"/"type"}}`；代理错误外壳 `{"type":"error","error":{...}}` | `image.go::batch3ImageErrorDetail`；Python `_proxy_error` docstring |
| 代理是 JSON-only（不向本服务发 multipart） | Python 模块 docstring：`The proxy is intentionally JSON-only — this tool never sends multipart upstream.` |
| 超时预算 225s | `image.go::batch3ImageRequestTimeout`、Python `REQUEST_TIMEOUT = 225.0` |

**没有证据的**：真实图片后端/模型/格式、OpenAI multipart edits 的内部重建、计费。本实现只把客户端
JSON 逐字转发到显式配置的 OpenAI-compatible images endpoint；上游由部署方提供。

## 3. 已实现

### 3.1 `POST /v1/messages`

1. **M3 凭据边界不变**：Bearer streamToken 或 operator JWT；缺失/过期/错签名/非 stream 的坏 claims
   → `401 {"error":{"type":"hive_stream_token_invalid"}}`（只输出已锚定的 `type`）。
2. **有效凭据 + 未配置上游** → `400`：
   ```json
   {"type":"error","error":{"type":"llm_upstream_not_configured","message":"credential valid; no LLM upstream is configured (set --llm-upstream-url)"}}
   ```
   选 400 不是 503，是为了让真实 `go_hive` keycheck（200/400/429 = valid）继续把 `hive` 凭据判为
   有效；但仍然**不返回 200、不伪造 completion**，M3 的 L2 检查继续有意义。
3. **请求透传**：
   - body 逐字流式转发（不解析），未知字段保留；上限 64MB（本地防护，超出 413
     `llm_request_too_large`）。
   - 转发 `Content-Type`、`Accept`、`anthropic-version`（缺省用 `--llm-anthropic-version`）、
     `anthropic-beta`；其它头不转发。
   - **剥离客户端 `Authorization` 和 `x-api-key`**，按配置写上游凭据：`x-api-key`（默认，Anthropic）
     或 `Authorization: Bearer`（Anthropic-compatible 代理）。
   - 不跟随上游重定向（避免把凭据重放给 Location 目标）；3xx 原样返回调用方。
4. **响应透传**：
   - 状态码原样（2xx/4xx/5xx，含 429）；body 原样；除 hop-by-hop/framing 外的响应头原样
     （`request-id`、`anthropic-ratelimit-*`、`retry-after` 等）。
   - `Content-Type: text/event-stream` → 逐行转发，**每行 flush**；不解析事件名，上游发什么就转什么，
     keepalive 注释也转发。客户端断开/上游结束都正确收尾。
   - 非 SSE → 流式 `io.Copy`。
5. **失败映射**（本地产生，不泄露上游 URL/key）：
   - 连接/传输失败 → `502 {"type":"error","error":{"type":"llm_upstream_unavailable",...}}`
   - 上游超时（`--llm-request-timeout`，默认 10m，覆盖整个流） → `504 ... llm_upstream_timeout`
   - 调用方取消 → 不写响应，取消向上游传播（`r.Context()` → upstream request context）
6. **模型路由**：不实现。`model` 原样透传；没有 model→provider 映射表。

### 3.2 `POST /v1/images/generations`

1. 与 Messages 相同的 Bearer 凭据校验（`HIVE_API_KEY` 就是同一个 per-user token）。
2. 未配置 `--images-upstream-url` → `503 {"type":"error","error":{"type":"llm_upstream_not_configured",...}}`（有效凭据也绝不空 200）。
3. body 逐字转发（64MB 上限），`Content-Type: application/json`；上游凭据固定
   `Authorization: Bearer <--images-upstream-key>`（与两个客户端证据一致）。
4. 上游状态/body/响应头原样返回；4xx（400/402/429）/5xx 不改写。
5. 超时 `--images-request-timeout`（默认 225s，与客户端预算一致）；传输失败 502、超时 504。
6. **不实现**模型路由、图片后端、multipart edits 重建、计费。

### 3.3 配置

| 配置项 | flag | env | config.json | 默认 |
| --- | --- | --- | --- | --- |
| Messages 上游 | `--llm-upstream-url` | `HIVE_CLOUD_LLM_UPSTREAM_URL` | `llm_upstream_url` | 空（未配置） |
| Messages 凭据 | `--llm-upstream-key` | `HIVE_CLOUD_LLM_UPSTREAM_KEY` | `llm_upstream_key` | 空（不发鉴权头） |
| Messages 鉴权头 | `--llm-upstream-auth` | `HIVE_CLOUD_LLM_UPSTREAM_AUTH` | `llm_upstream_auth` | `x-api-key`（可选 `bearer`） |
| anthropic-version 缺省 | `--llm-anthropic-version` | `HIVE_CLOUD_LLM_ANTHROPIC_VERSION` | `llm_anthropic_version` | `2023-06-01` |
| Messages 超时 | `--llm-request-timeout` | `HIVE_CLOUD_LLM_REQUEST_TIMEOUT_MS` | `llm_request_timeout_ms` | `10m` |
| Images 上游 | `--images-upstream-url` | `HIVE_CLOUD_IMAGES_UPSTREAM_URL` | `images_upstream_url` | 空（未配置） |
| Images 凭据 | `--images-upstream-key` | `HIVE_CLOUD_IMAGES_UPSTREAM_KEY` | `images_upstream_key` | 空（不发鉴权头） |
| Images 超时 | `--images-request-timeout` | `HIVE_CLOUD_IMAGES_REQUEST_TIMEOUT_MS` | `images_request_timeout_ms` | `225s` |

URL 规则：Messages 接受 base、`.../v1` 或完整 `.../v1/messages`；Images 接受 base、`.../v1` 或完整
endpoint（非空自定义 path 原样使用）。启动时校验 scheme（http/https）与 host，非法配置直接启动失败。
凭据建议放 0600 的 `config.json` 或 env；**不要写进命令行**（会出现在进程列表）。

### 3.4 配置示例

```bash
# 直连 Anthropic
hive-cloud serve --data-dir ./data \
  --llm-upstream-url https://api.anthropic.com \
  --llm-upstream-key "$ANTHROPIC_API_KEY" \
  --llm-upstream-auth x-api-key

# 自建 Anthropic-compatible 代理（Bearer）+ 自建 images endpoint
hive-cloud serve --data-dir ./data \
  --llm-upstream-url http://127.0.0.1:9000 \
  --llm-upstream-key "$UPSTREAM_KEY" \
  --llm-upstream-auth bearer \
  --images-upstream-url http://127.0.0.1:9001/v1/images/generations \
  --images-upstream-key "$IMAGES_KEY"
```

## 4. 错误面汇总

| 场景 | 状态 | body |
| --- | --- | --- |
| Messages/Images 缺失/非 Bearer/过期/错签名 | 401 | `{"error":{"type":"hive_stream_token_invalid"}}` |
| Messages 有效凭据、未配置上游 | 400 | `{"type":"error","error":{"type":"llm_upstream_not_configured",...}}` |
| Images 有效凭据、未配置上游 | 503 | 同上（`llm_upstream_not_configured`） |
| 上游连接失败 | 502 | `{"type":"error","error":{"type":"llm_upstream_unavailable",...}}` |
| 上游超时 | 504 | `{"type":"error","error":{"type":"llm_upstream_timeout",...}}` |
| body > 64MB | 413 | `{"type":"error","error":{"type":"llm_request_too_large",...}}` |
| 上游 2xx/4xx/5xx | 原状态 | 上游 body 原样（含 Anthropic/OpenAI 错误体） |

`llm_upstream_*` / `llm_request_too_large` 是本模块的本地错误类型，不是云端契约值；外壳采用
Python 图片工具文档里观察到的代理形状 `{"type":"error","error":{...}}`。

## 5. 测试与证据

### L1（`go test ./...`）

`internal/httpapi/m5_contract_test.go` 使用 fake upstream + 真实 `httptest` HTTP：

- 非流式 Messages：body 逐字（含未知字段）、`anthropic-version`/`anthropic-beta` 转发、
  两种上游鉴权头、客户端 token 不泄露、响应状态/body/头透传。
- SSE：上游先发首事件并 flush，代理必须在放行上游之前把首事件交给客户端（证明逐事件 flush）；
  整个字节流对比。
- 上游 400/401/403/429/500/529：状态/body/`retry-after` 原样。
- 超时 504、连接失败 502、客户端取消传播到上游。
- 未配置 Messages 400 / Images 503；GET 405；未登记 `/v1/complete` 404。
- Images：body 逐字（含未知字段）、Bearer 上游凭据、响应/错误透传。
- 配置：非法 URL/auth 启动失败；`TestCapRequestBody*` 覆盖 64MB 上限。

### L2（`./scripts/m5-smoke.sh`）

`harness/llm-proxy-smoke` 用**真实** `go_hive` `llm.NewHTTPProvider`：

1. `Complete` 打自建 `/v1/messages`，fake upstream 返回 Anthropic JSON → 校验 content/tokens/model。
2. `ACompleteStream` 打同一路径 → fake upstream 先发 `message_start`+delta 后暂停；真实客户端收到
   delta 才放行剩余事件，证明代理没有缓冲整条流。
3. 上游 429/`retry-after` 透传；garbage token 401；operator JWT 200。
4. 调用方取消 → fake upstream 观察到 `r.Context().Done()`。
5. 未配置 server：Messages 400 / Images 503，均带 `llm_upstream_not_configured`。
6. Images：真实 JSON body 逐字、Bearer 上游 key、`data[].b64_json` 响应；上游 402 透传。
7. 上游凭据隔离：客户端 streamToken 不出现在上游任何 header；上游看到的是配置的 key。
8. 证据 `evidence/m5-smoke.json`：11 项全过；只含状态、计数、body、key 指纹，不含 token/key 明文。

`./scripts/smoke.sh` 顺序跑 M1 globaldb、M2 inbox、M3 streamToken/LLM 凭据、M5 LLM 代理；四阶段全过。

**顺带修复（M2 queued replay parity）**：全量冒烟暴露了一个时序相关的 M2 缺陷——回复在 runtime 尚未
连上 SSE 时排队，重放 payload 丢了 `colony_id`，真实 `HiveInboxListener` 因此得到空 `source`。
`store.PendingReplies` 现在 join `inbox_messages` 取回 `colony_id`，重放 payload 与实时广播一致；
`TestInboxStreamQueuedReplay` 增加了 `colony_id` 断言。这不是 M5 的新契约，只是让 M2 的队列重放
和实时路径同形，消除冒烟抖动。

**并发复核修复（delivery race）**：`-race` 下发现客户端读完 SSE 帧后立即断开时，handler 的
`MarkReplyDelivered(r.Context())` 可能因请求上下文取消而失败，快速重连会再次收到同一回复。
`inbox.go` 的排队重放与实时广播两处都改用 `context.WithoutCancel(r.Context())` 标记投递，既保留
客户端断开事实，也保证已写出的回复不会因断连被重放；`go test -race ./internal/httpapi` 现已通过。

## 6. 未实现 / 明确不做

| 项 | 状态 | 本服务行为 |
| --- | --- | --- |
| 模型路由（model → provider/上游 key） | `UNKNOWN` | 不实现；`model` 原样透传 |
| 计费/配额/限流 | `UNKNOWN` | 不实现；按私有部署信任域处理 |
| 真实云 SSE 事件子集/额外事件字段 | `UNKNOWN` | 不枚举；原样转发上游 SSE 字节 |
| 真实云成功响应额外字段 | `UNKNOWN` | 不改写；原样转发 |
| 真实云 401 完整 body 的 message/其它字段 | `UNKNOWN` | 只输出锚定的 `error.type` |
| 图片后端/模型/格式、multipart edits 重建 | `UNKNOWN` | 只把客户端 JSON 转发到显式配置的 OpenAI-compatible endpoint |
| `x-api-key` 作为**客户端**凭据头 | `UNKNOWN`（客户端用 Bearer） | 只接受 Bearer；客户端 `x-api-key` 会被剥离 |
| 上游重定向跟随 | 不做 | 3xx 原样返回，避免凭据重放 |
| legacy `/user/refresh-stream-token` | `UNKNOWN` | 404 |

## 7. 安全/运维注意

- 上游 key 只存在配置/内存；访问日志只记录路径/状态/耗时，不记录 header/body。
- 客户端凭据不会转发到上游：Messages 显式设置上游 key，覆盖任何客户端 `Authorization`/`x-api-key`。
- 64MB body 上限是本地防护，不是云端契约；超限返回 413，不联系上游。
- 流式请求的 `--llm-request-timeout` 覆盖整个流（默认 10m）；长任务部署方应按需调大。
- 未配置上游时，Messages 用 400 保持 `go_hive` keycheck 的“有效 key”语义；这不是 completion，
  也不是 200。配置上游后才会有真实 200。

## 8. 门禁命令

```bash
source /Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh
cd go_hive/go-hive-cloud
gofmt -l .
go vet ./...
go test ./... -count=1
python3 scripts/gen_cloud_surface.py --check
./scripts/m5-smoke.sh       # L2 M5 → evidence/m5-smoke.json
./scripts/smoke.sh          # M1 + M2 + M3 + M5
```

harness 单独构建：

```bash
cd harness/llm-proxy-smoke && go vet ./... && go build ./...
```

## 9. 证据

- `evidence/m5-smoke.json`：`scripts/m5-smoke.sh` 生成，11 项真客户端/E2E 检查全过。
- L1：`internal/httpapi/m5_contract_test.go`、`internal/httpapi/llm_relay_test.go`、
  `internal/config/config_test.go:TestLLMUpstreamConfig`。
- surface：`contracts/cloud-surface.json` + `docs/cloud-surface.html`；`llm-messages` 与
  `llm-images-generations` 均为 implemented，`llm-messages` 的成功面按“上游原样透传”登记。
- M3 的历史说明见 `docs/m3-notes.md`；其中“有效凭据固定 400 placeholder”已被本文件 §3.1 取代。
