# Hive Cloud 逆向规格（客户端可观察契约）

> 状态：M0 初版（2026-11-22）。所有条目均标注证据（文件:行）或标 `UNKNOWN`。
> 判定线：**只有客户端实际解析/依赖的字段才算契约**；服务端内部（授权实现、计费、迁移）不在此文范围。

## 1. 证据源

| 来源 | 作用 |
| --- | --- |
| `go_hive/docs/cloud-dependencies.md` | 私有云依赖总账（入口索引） |
| `go_hive/go-hive/internal/globaldb/client.go` | Global DB 端点/载荷/错误（Go 客户端，逐字） |
| `go_hive/go-hive/internal/sentinel/notifier.go` / `listeners.go` | Inbox 上报/解决/SSE 回复流 |
| `docs/hive-inbox.html`（go_hive/docs） | Inbox 协议/数据模型/流程（Draft 设计摘要） |
| `go_hive/go-hive/internal/server/routes_colony_workers_global.go` | `/api/global/*`、`/api/crm/grid/*` 代理→云端路径映射 |
| `core/framework/global_db/{client,count_cache,grid_query,cli}.py` | 上游 Python 客户端（字段与错误文案） |
| `core/framework/server/streamtoken_refresh.py` | streamToken 刷新端点/载荷/JWT `exp` 读取 |
| `go_hive/go-hive/internal/llm/{transport,keycheck,image}.go` | LLM 代理调用面（Anthropic Messages、images、Bearer 语义） |
| `core/framework/credentials/aden/client.py` | 凭据代理端点（api.adenhq.com） |
| `go_hive/contracts/api-contract-diff.md` | 已比对过的错误文案（Signed-out 等） |

## 2. 拓扑与边界

```
go_hive / Python runtime ──HIVE_CLOUD_BASE + HIVE_CLOUD_JWT──▶ hive-backend（闭源 Express/TS）
   │                                                             ├─ /v1/inbox/*      人机回复通道（Postgres）
   │                                                             ├─ /v1/global-db/*  团队共享库（schema-per-team）
   │                                                             ├─ /v1/crm/*        CRM 网格/领单
   │                                                             ├─ /v1/gateway/availability
   │                                                             └─ /user/refresh-stream-token(-from-stream)
   ├──Bearer streamToken──▶ llm.open-hive.com（hive-backend/llm，Rust）
   └──ADEN_API_KEY──▶ api.adenhq.com（凭据代理 /v1/credentials*）
桌面客户端（闭源）──Authorization: jwt──▶ 同一 hive-backend；Inbox UI 闭源
```

## 3. 端点清单

### 3.1 hive-backend · Global DB（`HIVE_CLOUD_BASE`）

| 方法/路径 | 请求 | 响应（客户端依赖） | 证据 |
| --- | --- | --- | --- |
| `POST /v1/global-db/sql` | `{"sql": str, "row_cap"?: int}` | 任意 JSON；错误 `{"error": str}` | client.go:36-41 |
| `POST /v1/global-db/query` | 同上（只读） | 同上 | client.go:44-49 |
| `POST /v1/global-db/upsert` | `{"table": str, "row": obj, "source_colony"?: str, "mode"?: str}` | 任意 JSON | client.go:52-60 |
| `POST /v1/global-db/import` | `{"table": str, "rows": [obj], "source_colony"?: str}` | 任意 JSON | client.go:63-68 |
| `GET /v1/global-db/tables` | — | `{"tables": [...]}`（CRM status/reminder 用） | client.go:171；routes_colony_workers.py:1332-1345 |
| `GET /v1/global-db/changes` | `?since=<cursor>` | `{cursor, tables,...}`（客户端原样回传 cursor） | client.go:179；count_cache `record_global_tables` |
| `GET /v1/global-db/tables/{table}/rows` | query params 原样透传 | 行集 JSON | client.go:183 |
| `PATCH /v1/global-db/tables/{table}/rows` | `{"pk": obj, "updates": obj}` | 任意 JSON | client.go:187-190 |

认证：`Authorization: jwt <HIVE_CLOUD_JWT>`（客户端不传 team_id；团队由 JWT 决定）。
未登录错误（逐字，已比对）：`Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).`

### 3.2 hive-backend · Inbox

| 方法/路径 | 请求 | 响应 | 证据 |
| --- | --- | --- | --- |
| `POST /v1/inbox/messages` | `{runtime_id, session_id, colony_id, kind: blocker\|heartbeat\|progress\|done, title, body, correlation_token, deep_link}` | 2xx + `{"id": ...}`（客户端读 `id` 作 MessageID） | notifier.go:287-331 |
| `POST /v1/inbox/resolve` | `{"session_id": str, "resolved_by": str}` | 2xx（客户端只判 `<300`） | notifier.go:341-360 |
| `GET /v1/inbox/runtime/stream?runtime_id=&kind=` | Bearer JWT；`Accept: text/event-stream` | SSE：`event: reply` + `data: {"correlation_token": str, "text": str}` | listeners.go:436-560 |
| `GET /v1/inbox/stream` | 桌面 SSE（P1 桌面实际用 15s 轮询；字段未使用） | `UNKNOWN` | docs/hive-inbox.html §02 |

认证：HTTP `Authorization: Bearer <JWT>`；桌面用 `Authorization: jwt ***`。SSE 空闲无总超时（客户端不设 Client.Timeout）。

### 3.3 hive-backend · CRM 网格（经 go_hive 代理）

上游桌面路径 `/api/crm/*` 原样映射到云；`_crm_proxy` 是纯透传（投影/FK/custom-field flattening
全在闭源后端）。M4 只实现能恢复请求/响应形状的通用行操作：

| 方法/路径 | 请求（恢复） | 响应（恢复） | M4 |
| --- | --- | --- | --- |
| `GET /v1/crm/grid/schema` | — | `UNKNOWN` | planned |
| `POST /v1/crm/grid/{entity}/query` | `{filter?, search?, order_by?, order_dir?, limit?, offset?}`（`grid_query` 形状） | 通用行页 `{table, columns, primary_key, rows, total, limit, offset}` | implemented |
| `POST /v1/crm/grid/{entity}/group-counts` | `{group_by, filter?, search?}` | `{table, group_by, groups:[{value,count}]}` | implemented |
| `GET /v1/crm/grid/{entity}/{id}` | — | 通用单行页 | implemented |
| `POST /v1/crm/grid/{entity}` | `{fields, identities?, because?}` | 通用单行页 | implemented（identities 仅空值） |
| `PATCH /v1/crm/grid/{entity}/{id}` | `{fields?, identities?, because?}` | 通用单行页 | implemented |
| `DELETE /v1/crm/grid/{entity}/{id}` | body 可选，soft-delete | `{deleted}` | implemented |
| `POST /v1/crm/grid/{entity}/{id}/stage` | `{value, because?}` | 通用单行页；stage 规则 `UNKNOWN` | implemented（不做转移校验） |
| `POST /v1/crm/claim` | `{ids, because?}` | `UNKNOWN`（CLI 只说明 `skipped`） | planned |
| `POST /v1/crm/release` | `{ids, because?}` | `UNKNOWN` | planned |

认证同上（`globaldb` 客户端）。`grid_query.py` 说明：REST 无过滤/搜索/插入/删除，桌面代理用
`/v1/global-db/sql|query` 生成 SQL 补足——**自建实现必须接受这些生成的 Postgres 风格 SQL**；
M4 的 `internal/httpapi/m4_contract_test.go` 用 `test_grid_query.py` 的逐字语句回归。
详细口径与未知项见 `docs/m4-notes.md`。

### 3.4 hive-backend · 其他

| 方法/路径 | 用途 | 证据 |
| --- | --- | --- |
| `GET /v1/gateway/availability?from=<source>` | `hive open` 启动 ping（fire-and-forget，5s 超时，无响应解析） | loader.go:1133；loader/cli.py:965 |
| `POST /user/refresh-stream-token-from-stream` | 请求 `{"streamToken": str}` → `{"streamToken": str}`；用 token 自身签名证明身份，无需 user JWT；24h TTL | streamtoken_refresh.py:154-190 |
| `POST /user/refresh-stream-token` | 桌面用完整 user JWT 换新 streamToken（历史路径） | streamtoken_refresh.py 注释 |

### 3.5 llm 代理（`https://llm.open-hive.com`）

| 面 | 细节 | 证据 |
| --- | --- | --- |
| Anthropic Messages | `POST /v1/messages`（Bearer；`hive` 提供商 keycheck 用）；流式 SSE | keycheck.go:564-569；litellm.py 注释 |
| 图片生成 | `POST /v1/images/generations`（Bearer `HIVE_API_KEY`） | image.go:36-37,280 |
| Token | streamToken 24h TTL；过期 → `hive_stream_token_invalid` | streamtoken_refresh.py 头注释 |

`UNKNOWN`：模型路由表、计费、限流、上游提供商密钥管理、SSE 事件细分。

### 3.6 凭据代理（`https://api.adenhq.com`）

| 方法/路径 | 用途 |
| --- | --- |
| `GET /v1/credentials` | 列出集成 |
| `GET /v1/credentials/{integration_id}` | 取 access token（自动刷新） |
| `POST /v1/credentials/{integration_id}/refresh` | 强制刷新 |
| `GET /v1/credentials/{integration_id}/validate` | 校验有效性 |

证据：`core/framework/credentials/aden/client.py:10-20`。`UNKNOWN`：字段细节（Python dataclass 可读）、OAuth 回调流程、token 存储。

## 4. 认证与令牌

| 令牌 | 形式（客户端依赖） | 备注 |
| --- | --- | --- |
| `HIVE_CLOUD_JWT` | JWT；客户端只**不验签**读 `exp`；`team_id`/`user_id` 由服务端从 JWT 解析（客户端不传） | 算法/签发方 `UNKNOWN`；自建可自定义，只要服务端一致 |
| `streamToken` | 不透明字符串；服务端把它当 Bearer 且其签名即身份 | 24h TTL；刷新接口 `{"streamToken": ...}` |
| `HIVE_API_KEY` | 图片生成 Bearer | 与 streamToken 关系 `UNKNOWN`（可能同源） |
| `ADEN_API_KEY` | 凭据代理 | `credentials/keys.go:15` |

## 5. 数据模型（Inbox，来自设计页）

- `inbox_messages`：`id, kind(blocker|heartbeat|progress|done), title, body, correlation_token, deep_link, status(open|resolved), resolved_by, reply_text, created/resolved/updated, runtime_id, session_id, colony_id, user_id, team_id`
- `inbox_message_reads`：每人已读（无行=未读）
- `runtime_connections`：`runtime_id, kind, ...`（SSE 自注册）

Global DB：Postgres schema-per-team；自由 DDL；`grid_query` 约束标识符 `[A-Za-z_][A-Za-z0-9_]*`。
CRM：实体/阶段由 schema 端点描述（`UNKNOWN` 具体实体名，需真实响应）。

## 6. SSE / WS 面

- 仅 Inbox runtime 回复流（SSE，`event: reply`）。
- 桌面 `GET /v1/inbox/stream`：`UNKNOWN`（P1 用轮询）。
- **无 WebSocket** 出现在客户端代码里（除本地 serve 的 WS 会话流，不属于云）。

## 7. 已知未知（必须在自建前决策或标 UNKNOWN）

1. JWT 签发方/算法/claims 全集（team 成员模型、角色）。
2. 登录/设备流（桌面如何取得 JWT）——桌面闭源，自建需自有登录或运维注入 token。
3. `cloud_sync` 对账协议（reconciler 不在仓库；hooks kinds：memory/prompt/tool_allowlist/queen_profile/queen_avatar/skill_override）。
4. Inbox UI（闭源）——自建需自备前端。
5. CRM 实体 schema、阶段流转语义、claim/release 规则。
6. 图片生成后端与计费。
7. 限流/配额/审计格式。
8. 桌面端能否把云基址指到自建（若硬编码，需代理/改 hosts 或桌面提供配置）。

## 8. 自建兼容判定规则

- 客户端**解析**的字段：必须逐字/同型；
- 客户端**只透传**的 JSON（如 global-db 返回体）：形状可自由，但要让 `count_cache`/`grid_query`/UI 能读（`tables`/`rows`/`cursor` 等命名需保持）；
- 状态码语义：401=未登录、4xx=客户端错误（错误体 `{"error": str}`）、5xx=服务端；
- 错误文案：客户端会展示的逐字对齐（已登记一条 Signed-out 文案）；
- 未知项一律在 `contracts/observed-contract.json` 标 `unknown`，实现时不得假装已知。
