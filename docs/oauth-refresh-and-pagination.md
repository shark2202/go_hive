# OAuth2 刷新与通用 REST 分页

状态：已实现（2026-11-21，`oauth-refresh-pagination` 车道）。本文是该能力的契约说明；证据审计见
`docs/integrations-evidence-audit.json` 的 `revision`、`global_gaps[0..1]` 与各模块 `oauth_refresh` / `pagination` 字段。

## 1. 上游事实表（为什么是这两个能力）

上游 Python 侧刷新不在各 tool 里，而在凭据层：

| 事实 | 上游位置 | Go 端口对应 |
| --- | --- | --- |
| 通用 OAuth2 refresh 授权（POST form `grant_type=refresh_token`，client_id/secret/refresh_token，`expires_in` 解析，`invalid_grant` → 重新授权） | `core/framework/credentials/oauth2/base_provider.py`（`refresh_access_token` 214-247、`refresh` 288-324、`_token_request` 385-422） | `internal/tools/integrationkit/oauth.go` |
| 过期前 5 分钟触发刷新（`should_refresh`） | 同上 341-355；`store.py` 518-530 | 同文件 `DefaultRefreshBuffer` |
| 刷新失败不静默（`CredentialRefreshError` / `CredentialExpiredError`，invalid_grant 文案固定） | `store.py` 532-572 | `OAuthRefreshError.ValueError()` |
| Google 系凭据是 OAuth2 access token（gmail/calendar/sheets/docs/email 共用 `google`） | `tools/src/aden_tools/credentials/email.py` | `GOOGLE_*` 环境变量 + `batch5Lane3OAuth` |
| HubSpot 接受 Private App token 或 OAuth2 | `tools/src/aden_tools/credentials/hubspot.py` | `HUBSPOT_*` |
| Zoho CRM 明确用 client_id/client_secret/refresh_token，token endpoint 随区域 | `tools/src/aden_tools/credentials/zoho.py`、`core/framework/credentials/oauth2/zoho_provider.py` | `ZOHO_*` |
| Reddit client-credentials / BigQuery·GA·GSC service-account JWT | 各 tool | 端口已有独立实现，本次不改 |
| 通用 REST 客户端无分页原语；上游列表以 has_more/offset/next_cursor 透传给模型 | 端口审计 `global_gaps`（分页） | `internal/tools/integrationkit/rest.go` |

## 2. 刷新契约

### 2.1 请求与缓存

- `POST <token_endpoint>`，`Content-Type: application/x-www-form-urlencoded`，`Accept: application/json`；
  表单字段：`grant_type=refresh_token`、`client_id`、`client_secret`、`refresh_token`，外加 `ExtraParams`。
- 响应支持 JSON 与表单编码（部分供应商）；解析 `access_token`、`expires_in`（数字或字符串）、`refresh_token`（轮换时更新缓存）。
- 缓存键为 `token_endpoint + client_id + refresh_token` 的哈希；同一凭据的并发调用共享一次授权请求（single-flight）。
- 供应商轮换 refresh_token 时，后续授权使用新值；强制刷新（401 路径）的结果同样写回常规缓存。
- 已知过期时间时，在过期前 **5 分钟**开始刷新；过期时间未知（未设置 `*_EXPIRES_AT`）时静态 token 视为可用，不做乐观刷新。

### 2.2 失败语义（不静默）

刷新失败返回工具值错误（`{"error": ..., "error_code": ..., "help": ...}`），不是空结果：

| 场景 | `error_code` | `error` 文案 |
| --- | --- | --- |
| 供应商返回 `invalid_grant` | `invalid_grant` | `Refresh token for '<module>' is invalid or revoked. Re-authorization required.` |
| 静态 access token 已知过期且无 refresh token | `no_refresh_token` | `Access token for '<module>' from <ORIGINAL_VAR> has expired and cannot be refreshed: no refresh token configured.` |
| 其它供应商错误 | 供应商 `error`（或 `http_error`） | `OAuth refresh for '<module>' failed: <code>: <description>` |
| 网络/解析失败 | `network_error` / `invalid_response` | `OAuth refresh for '<module>' failed: ...` |

`help` 字段指向要设置的变量（例如 `GOOGLE_REFRESH_TOKEN`），静态 token 场景给出原变量名（例如 `GOOGLE_ACCESS_TOKEN`）。

### 2.3 401 重试

模块声明了刷新源（有 refresh token）时：首次请求返回 **401 → 强制刷新一次 → 原请求重试一次**；刷新失败直接返回上表错误，
不再重试。只有静态 token 的模块收到 401 时，若配置了任意 OAuth 变量（过期时间/client_id 等）则返回
`no_refresh_token` 并点名变量；完全未配置 OAuth 变量时保持原有供应商错误文案（避免把 HubSpot Private App token 误判为过期 OAuth）。

## 3. 环境变量

| 模块 | 静态/初始 token | 刷新材料 | 默认 token endpoint |
| --- | --- | --- | --- |
| calendar / gmail / google_docs / google_sheets / email | `GOOGLE_ACCESS_TOKEN` | `GOOGLE_CLIENT_ID`、`GOOGLE_CLIENT_SECRET`、`GOOGLE_REFRESH_TOKEN` | `https://oauth2.googleapis.com/token`（`GOOGLE_TOKEN_URL` 可覆盖） |
| hubspot | `HUBSPOT_ACCESS_TOKEN` | `HUBSPOT_CLIENT_ID`、`HUBSPOT_CLIENT_SECRET`、`HUBSPOT_REFRESH_TOKEN` | `https://api.hubapi.com/oauth/v1/token`（`HUBSPOT_TOKEN_URL` 可覆盖） |
| zoho_crm | `ZOHO_CRM_ACCESS_TOKEN` | `ZOHO_CLIENT_ID`、`ZOHO_CLIENT_SECRET`、`ZOHO_REFRESH_TOKEN` | `ZOHO_TOKEN_URL`，否则由 `ZOHO_ACCOUNTS_DOMAIN`（`<domain>/oauth/v2/token`）或 `ZOHO_REGION`（`in/eu/au/jp/uk/sg/ca` → `accounts.zoho.<code>`，`us/com` → `.com`）推导 |

- 过期时间：`GOOGLE_ACCESS_TOKEN_EXPIRES_AT`、`HUBSPOT_ACCESS_TOKEN_EXPIRES_AT`、`ZOHO_CRM_ACCESS_TOKEN_EXPIRES_AT`；
  RFC 3339（推荐）或 Unix 秒。未设置 = 未知（不提前刷新）。
- 只配置 refresh token（不配静态 token）也可用：首次调用直接换 token。

### 3.1 凭据库 → 环境桥

`credentials.OAuthRefreshEnvBindings()`（`internal/credentials/oauth_refresh.go`）为 `google` / `hubspot` / `zoho_crm`
三个凭据额外导出 `client_id` / `client_secret` / `refresh_token` 与 `access_token` 的过期时间；`send_email`/serve 启动与
每次凭据写入后都会执行（`SyncCredentialEnv`）。这样在 Dashboard 保存的 OAuth 凭据无需手工导出变量即可被刷新路径读取。
（访问 token 本身仍由原有 `CredentialEnvBindings` 导出，本次未改。）

## 4. 声明式分页

`RestSpec.Pagination`：

```go
Pagination: &PaginationSpec{
    ItemsPath:      "records",                  // 页内条目数组，点号路径
    NextCursorPath: "response_metadata.next_cursor", // 下一页光标，点号路径
    CursorParam:    "cursor",                   // 回传参数：GET/DELETE 走 query，其余走 body
    MaxPages:       10,                         // 0 = DefaultMaxPages(10)
}
```

语义：请求第一页后读取 `NextCursorPath`；光标存在且未重复则带 `CursorParam` 重发，最多 `MaxPages` 页；合并所有
`ItemsPath` 条目写回第一页信封，并删除 `next_cursor` 叶子（避免调用方误以为还有下一页）。光标为空、缺失、重复或达到
页数上限即停止；响应不含 `ItemsPath` 时原样返回该页，不伪造空列表。

已接线：

| 模块 / action | 供应商分页 | 测试 |
| --- | --- | --- |
| `airtable_list_records` | 响应 `offset`，请求 `offset`（每页 ≤100） | `TestAirtableListRecordsFollowsOffsetPages` |
| `slack_list_channels` | `response_metadata.next_cursor`，请求 `cursor` | `TestSlackListChannelsFollowsCursor` |
| `slack_list_users` | 同上 | `TestSlackPaginationStopsAtMaxPages`（页数上限） |

## 5. 测试证据（全部离线）

`internal/tools/integrationkit/oauth_test.go`（2 个）、`internal/tools/integrations/oauth_refresh_test.go`（12 个）、
`internal/credentials/oauth_refresh_test.go`（3 个），共 17 个：

- `TestOAuthRefreshExpiredTokenRefreshesBeforeCall`：过期 token → 一次 refresh 授权（断言 form 四个字段与 Content-Type）→ 供应商收到新 token。
- `TestOAuthRefreshRetriesOnceAfterUnauthorized` / `TestOAuthRefreshHubSpotRetriesAfterUnauthorized`：401 → 刷新 → 重试一次（断言两次请求的 Authorization）。
- `TestOAuthRefreshFailureIsAnExplicitError`：`invalid_grant` → 固定文案 + `error_code` + `help`，供应商零调用。
- `TestOAuthRefreshStaticTokenExpiredNamesVariable` / `TestOAuthRefreshUnauthorizedWithoutRefreshTokenNamesVariable`：点名 `GOOGLE_ACCESS_TOKEN`。
- `TestOAuthRefreshConcurrentCallsShareOneGrant`：6 个并发调用只发 1 次授权请求（`-race` 复跑通过）。
- `TestOAuthRefreshZohoMintsTokenFromRefreshTokenOnly`：仅 refresh token 配置也能取 token，并保持 `Zoho-oauthtoken` 头。
- `TestOAuthRefreshGmailSenderRefreshesBeforeSend`：email（Gmail）模块调用前刷新。
- `TestTokenRefresherUsesRotatedRefreshToken`：供应商轮换 refresh_token 后，下一次授权使用新值；强制刷新写回常规缓存。
- `TestTokenRefresherStaticTokenSemantics`：未知过期时间直通；已知过期 → `no_refresh_token` 且带变量名。
- 分页 3 个测试：见上表。
- 凭据桥 3 个测试：导出变量名、无过期时间不导出、绑定唯一。
- 全部使用本地 fake token endpoint 与 httptest 假 provider，0 外网调用。

## 6. 残余

- 只声明静态 access token 的条目（salesforce / microsoft_graph / powerbi / quickbooks / zoom / google_search_console 等）上游规格没有
  client_id/client_secret/refresh_token，无法在不发明变量的前提下刷新；仍按供应商 401 文案失败。
- `account` 别名的凭据是单 token，不携带刷新材料。
- 刷新后的 token 仅在进程内缓存，不写回凭据库或 `*_ACCESS_TOKEN` 环境变量。
- 分页：github（`Link` 响应头）、stripe（`starting_after` 需上页最后一条 id，而非响应字段）无法用当前 `next_cursor_path` 表达；
  zoom/gmail/google_docs/google_maps/salesforce 仍是参数透传，bigquery 仍是自研 token 循环；这些列为残余而非静默推断。
