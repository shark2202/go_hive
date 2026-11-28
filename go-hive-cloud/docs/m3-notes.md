# M3 实现笔记 —— streamToken 轮换 L2 与 `hive` LLM 凭据仅校验面

> 状态：M3（2026-11-23）。范围=README §4 的 M3 行：**streamToken 轮换 L2 + `hive` LLM 凭据兼容
> （仅 token 校验面）**。
>
> **M5 更新（2026-11-23）**：`/v1/messages` 已升级为真实透传；有效凭据不再返回
> `llm_proxy_not_implemented`。未配置上游时返回 400 `llm_upstream_not_configured`（仍保持
> `go_hive` keycheck 的 200/400/429 = valid 语义），配置上游后按上游 2xx/4xx/5xx/SSE 原样转发。
> 本文件中 §2.3/§3/§6 关于“固定 400 placeholder / 透传是 M5”的描述是 M3 当时的历史口径，
> 现以 `docs/m5-notes.md` 为准。
>
> 口径：只实现 `contracts/observed-contract.json` 有证据支撑的校验/轮换语义。轮换接口、请求字段、
> 响应字段、24h TTL、401 `hive_stream_token_invalid` 均沿用 M1 已登记的观察契约；LLM 代理只实现
> `POST /v1/messages` 的 Bearer 校验边界，**不接上游、不返回 200**，Anthropic Messages 透传仍是 M5。
> 没有证据的吊销账本、sid blacklist、真实云 401 message、LLM 成功响应/流式事件全部登记为 `UNKNOWN`。

## 1. 交付物与验证路径

| 层 | 实现 | 验证 |
| --- | --- | --- |
| L1 契约 | `internal/httpapi/m3_contract_test.go`：轮换窗口、旧 token 行为、claims 硬化、刷新 body 错误、LLM Bearer 校验 | `go test ./...` 全过 |
| L1 auth | `internal/auth.VerifyStreamToken`（签名 + `typ:stream` + user/team 可用性） | `internal/auth/jwt_test.go:TestVerifyStreamToken` |
| 配置 | 上一把 stream secret：`--stream-secret-previous` / `HIVE_CLOUD_STREAM_SECRET_PREVIOUS` / `config.json:stream_secret_previous` / `<data-dir>/stream_secret_previous` | `internal/config/config_test.go` |
| L2 轮换 | `harness/streamtoken-smoke`：真实 Python `framework.server.streamtoken_refresh.post_refresh` / `_refresh_loop` / `try_refresh_now` | `scripts/m3-smoke.sh` → `evidence/m3-smoke.json`，18 项 |
| L2 LLM 凭据 | `harness/llm-credential-smoke`：真实 `llm.ValidateProviderKey("hive", …)` + `credentials.CredentialStore` 的 `hive`/`api_key` 形状 | 同上，7 项 |
| 合计 | 两个 harness 合并 | **25 项全过** |

`go test ./...` 覆盖 cloud module 全部包；三个 harness 是嵌套 module，各自 `go vet/go build`。

## 2. 已实现（有观察契约支撑）

### 2.1 streamToken 轮换窗口

`POST /user/refresh-stream-token-from-stream`：

- 请求 `{"streamToken": "<old>"}`；响应 200 `{"streamToken": "<new>"}`。
- 旧 token 用自己的 HMAC 签名证明身份，route-level auth 仍是 none（契约行为，M1 起）。
- 新 token 用 `StreamSecrets[0]` 签发，TTL 24h（`StreamTTL` 可配置，默认 24h）。
- M3 新增**双钥轮换窗口**：配置了上一把 secret 时，上一把 secret 签发的 token 仍可通过校验并换新；
  移除上一把 secret 后，旧 token 立即 401。这是 `streamtoken_refresh.py` 头部注释明确提到的
  “master-secret rotation”吊销手段，不是猜测的端点。
- 轮换**不会**吊销尚未过期的旧 token：本服务没有观察契约支撑的吊销账本；旧 token 在 exp 前仍可
  刷新/通过 LLM 校验。真实云是否有 sid blacklist 属 `UNKNOWN`（§6）。
- claims 硬化：必须 `typ == "stream"`、`user_id != ""`、`team_id` 通过 `[A-Za-z0-9._-]{1,64}` 校验；
  否则 401。避免用畸形 token 换出空 team 的新 token。

配置入口：

```bash
hive-cloud serve --stream-secret "$CURRENT" --stream-secret-previous "$OLD"
# 或
HIVE_CLOUD_STREAM_SECRET_PREVIOUS="$OLD" hive-cloud serve
# 或 config.json: {"stream_secret_previous": "..."}
# 或 <data-dir>/stream_secret_previous（0600，存在即启用；不会自动生成）
```

### 2.2 错误面（逐字）

| 场景 | 状态 | body |
| --- | --- | --- |
| 刷新 body 非 JSON / 尾随数据 | 400 | `{"error":"invalid JSON body"}` |
| 刷新 token 为空/过期/错签名/非 stream/坏 claims | 401 | `{"error":"hive_stream_token_invalid"}` |
| LLM `POST /v1/messages` 缺失/非 Bearer/过期/错签名 | 401 | `{"error":{"type":"hive_stream_token_invalid"}}` |

两个 401 形状不同是**按客户端证据**分开的：

- 刷新接口的 flat `{"error":"hive_stream_token_invalid"}` 来自 `streamtoken_refresh.py` 的返回处理。
- LLM 代理的 nested `{"error":{"type":"hive_stream_token_invalid", ...}}` 来自
  `core/framework/llm/litellm.py::_is_hive_stream_token_invalid` 的注释与匹配逻辑。注释里 `...`
  的部分不可恢复，因此只输出已锚定的 `type`，不编造 message。
- 两者都只匹配/返回错误标记；真实云的完整错误体字段仍 `UNKNOWN`。

### 2.3 `hive` LLM 凭据仅校验面

`POST /v1/messages`（llm-proxy 服务）：

- 接受 `Authorization: Bearer <streamToken>`（`hive` credential 的 `api_key` 值）以及
  `Authorization: Bearer <operator JWT>`；契约 surface 记录的是 “Bearer streamToken/JWT”。
- 有效凭据：**400** `{"error":{"type":"llm_proxy_not_implemented","message":"credential valid; Anthropic Messages pass-through is not implemented (M5)"}}`。
  - 不接真实上游，不伪造 completion，不返回空 200。
  - 400 是真实 `go_hive` keycheck 判定“key 有效”的状态之一（`keycheck.go` 对 Hive/Anthropic
    compatible 的 200/400/429 都判 valid）；因此 `hive` 凭据校验面与真实客户端兼容。
  - `llm_proxy_not_implemented` 是本模块的占位错误类型，**不是**云端契约；M5 会用真实透传替换。
- 无效/过期/错签名/缺失：401 nested `hive_stream_token_invalid`（见 §2.2）。
- 请求体（Anthropic Messages）在 M3 不解析、不校验、不转发；body 字段/流式事件留给 M5。

### 2.4 真实客户端 L2 闭环

`scripts/m3-smoke.sh` 启两个本地 server（不访问外部云）：

1. server A：current + previous secret（轮换窗口）；server B：仅 current（窗口关闭）。
2. Python harness 直接 import 运行时模块，用真实 `post_refresh` 轮换旧 token，验证：
   新 token 由 current secret 签名、`typ:stream`、身份保留、TTL≈24h；旧 token 轮换后仍可再刷新；
   在 server B 上旧 token 401；过期/错签名/user JWT/garbage 401；非法 JSON 400。
3. Python harness 用真实 `_refresh_loop`（force event 触发立即刷新）把新 token 写回
   `id="hive", key="api_key"` 的 credential store；并用 `try_refresh_now` 验证 LLM 调用点的强制刷新路径。
4. Go harness 用真实 `llm.ValidateProviderKey("hive", …)` 校验旧/current/user-JWT 为 valid、过期/
   错签名/garbage 为 invalid；并用真实 `credentials.CredentialStore` 保存/读取/校验
   `{id:"hive", keys:{api_key: <streamToken>}}`。
5. 证据合并：`evidence/m3-smoke.json`。

## 3. 未实现 / 明确不做

| 项 | 状态 | 本服务行为 |
| --- | --- | --- |
| `POST /user/refresh-stream-token`（桌面 legacy，用 user JWT 换 streamToken） | `unknown`（只有路径） | **404**（不猜请求/响应；`m3_contract_test` 与 smoke 都断言） |
| 真实云 sid blacklist / token 吊销账本 | `UNKNOWN` | 无吊销表；只能靠 exp 或移除 signing secret（master rotation） |
| `/v1/messages` Anthropic Messages 透传、流式事件、model routing、计费、限流 | M5 / `UNKNOWN` | 有效凭据固定 400 `llm_proxy_not_implemented`，绝不 200 |
| `x-api-key` 作为 LLM 凭据头 | `UNKNOWN`（客户端源码用 Bearer） | 只接受 Bearer；`x-api-key` 未实现 |
| 真实云 401 body 的 message/其它字段 | `UNKNOWN` | 只输出锚定的 `error.type` |
| 真实云端 streamToken TTL/claims/issuer、刷新限流 | `UNKNOWN` | 24h TTL + HS256 自签（M1 口径） |
| 轮换是否在真实云吊销旧 token | `UNKNOWN` | 本服务不吊销；旧 token 在 exp 前继续有效 |

**没有做的事**：不实现 LLM 上游、不实现 image generations（M5）、不实现 legacy refresh、不添加
revocation store、不猜桌面/真实云的额外字段。未注册路径保持 404/405，不以空 200 冒充。

## 4. 配置/兼容说明

- `StreamSecrets` 顺序固定：`[current, previous]`；只有 `[0]` 签发新 token，全部 secret 参与校验。
- 上一把 secret 只来自显式配置/文件；不存在时保持空，不会自动生成——这让“是否仍接受旧 token”
  成为显式运维动作。
- 轮换步骤：先加 `--stream-secret-previous=<old>` 并把 `--stream-secret` 换成 `<new>`，等待一个
  刷新周期（≤24h）；确认旧 token 不再需要后移除 previous。
- 同一监听端口同时提供 hive-backend 的刷新接口与 llm-proxy 的 `/v1/messages` 校验面；私有部署可把
  `HIVE_LLM_BASE_URL` 指向同一 base。真实云是两个 host，但本模块的契约面按路径区分，不新增 host 假设。

## 5. 门禁命令

```bash
source /Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh   # go1.26.11 + 模块缓存
cd go_hive/go-hive-cloud
gofmt -l .                                  # 期望无输出
go vet ./...
go test ./... -count=1                      # L1：含 M3 轮换/LLM 校验契约
python3 scripts/gen_cloud_surface.py --check
./scripts/smoke.sh                          # L2a 14/14 + L2b 7/7 + M3 25/25
```

harness 单独构建：

```bash
cd harness/globaldb-smoke && go vet ./... && go build ./...
cd harness/inbox-smoke && go vet ./... && go build ./...
cd harness/llm-credential-smoke && go vet ./... && go build ./...
# Python harness 走仓库 uv 环境：
cd <repo-root> && uv run python go_hive/go-hive-cloud/harness/streamtoken-smoke/main.py
```

## 6. 已知未知 / 后续

| 项 | 状态 |
| --- | --- |
| sid blacklist / 吊销账本 | `UNKNOWN`；无实现 |
| 真实云 401 完整 body（LLM 与后端） | `UNKNOWN`；只固定已锚定标记 |
| 真实云端 TTL/claims/issuer/刷新限流 | `UNKNOWN`；24h 为客户端观察值 |
| 轮换是否在真实云使旧 token 失效 | `UNKNOWN`；本服务不失效 |
| `/v1/messages` 成功/流式/路由/计费 | M5；M3 仅校验 |
| `x-api-key`、图片生成 | M5 / `UNKNOWN` |
| legacy `/user/refresh-stream-token` | `UNKNOWN`；404 |
| 桌面端能否改 `HIVE_LLM_BASE_URL` | `UNKNOWN`（部署侧可通过配置/环境覆盖） |

## 7. 证据

- `evidence/m3-smoke.json`：`scripts/m3-smoke.sh` 生成，25 项真客户端检查全过（含 base URL、claims
  指纹、credential store 往返；不含 token 明文/secret）。
- L1：`internal/httpapi/m3_contract_test.go`、`internal/httpapi/testdata/llm-token-invalid.json`、
  `internal/httpapi/testdata/llm-not-implemented.json`。
- surface：`contracts/cloud-surface.json` + `docs/cloud-surface.html`（`llm-messages` 从 planned 升为
  implemented，明确仅校验面；unknowns 增加吊销/LLM 成功面）。
- 本文件 §3、§6 是 M3 的诚实边界：没有证据的端点/字段/吊销语义没有实现，也没有以 200 冒充。
