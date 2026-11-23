# 私有云依赖总账（Hive Cloud / Aden 服务）

回答两个具体问题 + 全量清单 + 替代方案。核对时间：2026-11-22 深夜（wave-73 基线）。
所有条目均逐条核对自源码（附行号为证）；Go 侧为 go_hive/go-hive，Python 侧为上游 core/。

## 0. 直接回答

**`internal/senders` 不依赖私有云 API。** 它是纯本地发送器引擎（SMTP 投递、日志、抑制、轮换、`hive://` setup 深链）。
上游 `tools/src/aden_tools/senders/registry.py` 的发送器池来自 Aden cloud，Go 版本改为本地池（`sender.go:4` 注释）；
OAuth/ESP 投递在上游经 "Aden cloud connector"，Go 未实现、直接给出配置指引错误（`engine.go:192/197/470`）——这是"连不上的引导"，不是运行时依赖。

**`docs/hive-inbox.html` 描述的"Hive Inbox"正是私有云服务**：后端 hive-backend（Express/TS，**闭源**，GitHub 不可见），
消息存 Hive 云端 Postgres；开源侧只有协议 + 运行时客户端（Python `core/framework/sentinel/*`、Go `internal/sentinel/*`）。
该页自述：自建兼容后端可行但要自造账号体系、`HIVE_CLOUD_BASE` 可指向自建服务。

## 1. 依赖清单（按服务分组）

### 1.1 hive-backend —— Inbox（Sentinel `hive` 渠道）

| 端点 | 调用方 | 证据 |
| --- | --- | --- |
| `POST {HIVE_CLOUD_BASE}/v1/inbox/messages` | escalation 上报（blocker/heartbeat/progress/done） | `internal/sentinel/notifier.go:314` |
| `POST {HIVE_CLOUD_BASE}/v1/inbox/resolve` | 回复/解决回写 | `notifier.go:341` |
| `GET {HIVE_CLOUD_BASE}/v1/inbox/runtime/stream`（SSE，Bearer） | 运行时收回复流 | `internal/sentinel/listeners.go:444` |

- 渠道常量：`ChannelHive = "hive"`（`notifier.go:32`）；**默认渠道**：无 `notifications.json` 的 colony 解析为 `channel=hive` + 开启（`store.go:39` `DefaultChannel="hive"`；与 `hive-inbox.html` §00 一致）。
- 凭据：`HIVE_CLOUD_BASE` + `HIVE_CLOUD_JWT`（账号级，桌面端登录态注入；`notifier.go:31`）。
- 无云时行为：发送 → `NotifierResult{Error:"hive not connected (no cloud JWT/base)"}`；监听器 `BaseURL==""` 直接不连接（`listeners.go:441`）。本地 park/resume 不受影响，仅云端通知失效。

### 1.2 hive-backend —— 团队 Global DB（`/v1/global-db/*`）

| 端点 | 用途 |
| --- | --- |
| `POST /v1/global-db/sql`·`query`·`upsert`·`import` | 全局表读写/导入 |
| `GET /v1/global-db/tables`·`changes`·`tables/{t}/rows`；`PATCH …/rows` | 表目录/增量/行读写 |

- Go 客户端：`internal/globaldb/client.go`（env `HIVE_CLOUD_JWT`/`HIVE_CLOUD_BASE`，`:18-19`；端点 `:140-188`）。
- 使用面：`/api/colony/...` 全局 DB 路由 + **tracker 工具 `scope='global'`**（`internal/tracker/tools.go:758`）。
- 无云时：`NotSignedInError` → HTTP 401 `{"signed_out": true}`（`server/global_data.go:132`）；tracker 文案 `scope='global' requires a signed-in cloud session...`（`tools.go:834`）。

### 1.3 hive-backend —— CRM 只读网格（`/v1/crm/*`）

- Go 代理：`/api/crm/grid/schema`、`POST /api/crm/grid/{entity}/query`、`/api/global/crm/status` → hive-backend `/v1/crm/grid/*`（`server/routes_colony_workers_global.go:722-757`）。
- 同一云会话（`globaldb` client）；无云时与其他 global 路由同样 401。

### 1.4 hive-backend/llm —— LLM 代理（`llm.open-hive.com`，Rust 服务）

- `'hive'` 托管模型提供商：`HIVE_CLOUD_BASE` 未设置时**从模型目录隐藏**（`internal/llm/catalog.go:525-529`）；keycheck 端点 `HiveLLMEndpoint`（`keycheck.go:19`）。
- **image_gen_tool 默认代理**：`batch3ImageProxyBase = "https://llm.open-hive.com"`（`tools/integrations/image.go:36`）；base 覆盖优先级 `HIVE_LLM_BASE_URL` env > `modules.image_gen_tool.base_url` 配置 > 默认；token = `HIVE_API_KEY`（`:280`，无则报 "Sign in to refresh credentials"）。
- Bearer 认证启发（仅影响指向该主机的请求头选择）：`internal/llm/transport.go:253`（镜像 Python `llm/litellm.py::_api_base_needs_bearer_auth`）。
- **streamToken 24h 刷新**：Python `core/framework/server/streamtoken_refresh.py`（`POST {base}/user/refresh-stream-token-from-stream`）；**Go 未移植（C12 `out_of_scope`）**——用 hive 代理的长会话需桌面端或外部刷新。

### 1.5 api.adenhq.com —— 网关 ping 与凭据代理

- **gateway availability ping**：`hive open` 时 fire-and-forget `GET https://api.adenhq.com/v1/gateway/availability?from=hive-open`（Go `cli/loader.go:1133`、`cli.go:147`；Python `loader/cli.py:965`；5s 超时、失败静默、**无开关**）。
- **凭据代理**：Python `core/framework/credentials/aden/client.py`（base `https://api.adenhq.com`，`/v1/credentials*` 拉取/刷新/校验集成凭据）——**Go C12 `unported`**（Go 用本地 credential store/provider；`credentials/agent_validation.go:66` 注明 Aden 预同步为 no-op）。
- 相关 env：`ADEN_API_KEY`（`credentials/keys.go:15`）。

### 1.6 hive.adenhq.com —— 托管网页（仅文案/用户跳转）

- 凭据 help_url / API-key 指引（Go `server/routes_credentials.go:584`、`routes_credentials_check.go:98`）、Gmail/Email 工具 "Re-authorize via hive.adenhq.com" 提示（`integrations/gmail.go:34` 等）。
- 非运行时调用：只有用户被引导去网页完成 OAuth/取 key。

### 1.7 cloud_sync —— 本地写入推云

- Python hooks：`core/framework/cloud_sync_hooks.py::schedule_push`（kinds：memory / prompt / tool_allowlist / queen_profile / queen_avatar / skill_override）。**reconciler `framework.cloud_sync` 不在本仓库**（hook 捕获异常后静默 no-op）。
- Go：镜像为可选 deps 钩子（`routes_queens.go:52`、`routes_queens_tools.go:50`），**全库无生产代码装配 → no-op**；C12 登记 `partial`。

### 1.8 Aden cloud connector（发送器池、send-log、OAuth/ESP）

- 上游发送器池从 Aden cloud 拉取、OAuth/ESP 投递走云端连接器；Go `internal/senders` 为**纯本地实现**（SMTP 投递），OAuth/ESP 路径返回引导错误（见 §0）。属"未实现的云能力"，不是隐藏依赖。
- 上游 `tools/src/aden_tools/senders/` 通过同一个 `ADEN_API_KEY` 客户端访问 Aden cloud；base 取 `ADEN_API_URL`，缺省 `https://app.open-hive.com`（`registry.py:24`）。

| 端点 / 客户端方法 | 用途 | 证据 |
| --- | --- | --- |
| `GET /v1/senders/runtime`（`list_team_senders()`） | 拉取团队发送器池（60s 缓存；OAuth 行只带 `integration_id`，token 按需走 `/v1/credentials/{id}`） | `tools/src/aden_tools/senders/registry.py:3,135` |
| `reserve_send(payload)` | 发送前预留配额（有 daily_limit 时 fail-closed） | `senders/sendlog.py:96-114` |
| `complete_send(send_id, outcome)` | 回报 provider 结果（status/provider_message_id/error） | `senders/sendlog.py:130-148` |
| `sender_usage_today()` | 当日每发送器用量（`{sender_id: {sent_today}}`） | `senders/sendlog.py:153-168` |
| `suppress_recipient(email, reason, note)` | 加入团队 do-not-contact 名单 | `senders/sendlog.py:171-179` |
| `list_suppressions()` | 读取抑制名单 | `senders/sendlog.py:182-191` |
| `list_sends(**filters)` | 发送审计（sender_id/to_email/colony_id/limit） | `senders/sendlog.py:194-203` |
| `create_team_sender(payload)` | 创建设置好的发送器 | `senders/setup.py:347` |
| `list_provider_verified_senders(provider, secret)` | provider 已验证地址探测（SendGrid 等） | `senders/setup.py:166` |

- **除 `GET /v1/senders/runtime` 外，其余方法的 HTTP method/path 在开源仓库中不可恢复**：`core/framework/credentials/aden/client.py` 只实现 `/v1/credentials*`，senders 方法来自运行时扩展客户端。不得按猜测实现；机器可读登记见 `go_hive/go-hive-cloud/contracts/cloud-surface.json`（`status` 分别为 `inferred` 与 `unknown`）。
- 私有化替代：SMTP relay（Go `internal/senders` 已支持）；若要兼容上游 agent 工具链，需要 M4.5 按真实云端点补 send-log/setup（见 `go-hive-cloud/README.md` §4）。


### 1.9 不是私有云（常见误报，澄清）

- `queen_mail`（本地引擎）、Browser Bridge、所有第三方集成（GitHub/Google/…）、Telegram/Slack 通知（用户自己的 bot token）、LLM（用户自配 provider；除非选了 `hive` 托管提供商）、`token.tokenbroad.com` 等第三方网关——均与 Hive 私有云无关。

## 2. 依赖矩阵（Go 侧）

| 模块 | 云服务 | 触发条件 | 未配置时 | C12 |
| --- | --- | --- | --- | --- |
| `internal/sentinel`（notifier/listeners） | Inbox | 渠道=hive（默认）+ env | 本地照常，云通知静默失败 | ported |
| `internal/globaldb` + 全局路由 | Global DB | 调用全局表 API/工具 | 401 `signed_out` | ported |
| CRM 代理（routes_colony_workers_global.go） | CRM grid | 调 `/api/crm/*` | 401 `signed_out` | ported |
| `internal/llm`（hive 提供商/keycheck） | LLM 代理 | 选择 `hive` 提供商 | 提供商隐藏，用自配端点 | ported |
| image_gen_tool | LLM 代理（图片） | 调 image_gen | 缺 `HIVE_API_KEY` 报错；可改 base | ported |
| streamToken 刷新 | LLM 代理（鉴权） | hive 代理长会话 | **无 Go 实现** | out_of_scope |
| `credentials/aden/client` | 凭据代理 | 拉 Aden 托管凭据 | Go 本地 store | unported |
| `cli` gateway ping | api.adenhq.com | `hive open` | 静默失败 | ported（parity） |
| cloud_sync hooks | cloud_sync | 本地写入后推云 | no-op | partial |

## 3. 替代方案

| 服务 | 替代方案（按推荐序） |
| --- | --- |
| Inbox（云通知） | ① **Telegram / Slack 渠道**（两侧已实装，配 token 即可，opt-in）；② 本地-only：清空/关闭 channel——escalation 的 park/resume 本来就在本地；③ 自建兼容后端（按 `hive-inbox.html` 端点+数据模型；需自造账号体系，`HIVE_CLOUD_BASE` 指过去）；④ 走官方桌面登录态（原生路径） |
| Global DB | ① 本地 tracker（`scope='colony'`，无云要求）；② 文件交换（导出/导入 SQLite）；③ 自建 `/v1/global-db/*` 兼容服务（语义见 `globaldb/client.go`） |
| CRM grid | 无 OSS 替代（云只读代理）；用 tracker 自建表或外部 CRM 集成工具 |
| LLM 代理 | ① 任意 OpenAI/Anthropic 兼容端点（`configuration.json` llm 段；本地 LM Studio/vLLM/MLX 或第三方 API）；② image_gen：`HIVE_LLM_BASE_URL` 或 `modules.image_gen_tool.base_url` 指向自建；③ 长会话鉴权改用长效 API key（绕开 24h streamToken 刷新） |
| gateway ping | 目前无开关；仅一个 GET（无业务数据），离线静默失败可接受；如需完全本地化可加环境开关（未实现，可登记） |
| 凭据代理（Aden） | ① 本地 credential store + 各 provider OAuth/API key（go_hive 默认路径，`hive-cred` 按需拉取）；② 自建 broker |
| cloud_sync | 无移植（Go no-op）；替代=手动同步 `HIVE_HOME`（git/rsync/iCloud）或等待 backlog |
| senders OAuth/ESP | ① **SMTP 邮箱**（已全本地支持）；② provider 直连（Gmail 等集成工具用自己的凭据）；③ ESP 经 SMTP relay |
| hive.adenhq.com | 仅文案/跳转；可换成自托管指引（文档层面替换，不影响运行时） |

## 4. 关闭/切换开关速查

- 云整体：不设 `HIVE_CLOUD_BASE`/`HIVE_CLOUD_JWT`（或退出登录）→ inbox 不发、global 路由 401、hive 提供商隐藏。
- Inbox 渠道：colony 的 `notifications.json` 写 `channel:"telegram"|"slack"`（或 `sentinel_enabled:false`）。
- 图片/LLM：`HIVE_LLM_BASE_URL`、`modules.image_gen_tool.base_url`、`configuration.json` 的 `llm` 段。
- tracker：`scope='colony'`（本地）vs `scope='global'`（云）。

## 5. 未验证 / 待办

- 真实 Hive 云端 e2e（inbox 收回复、global-db 真读写）**无登录态未验证**（`hive-inbox.html` footer 同注）。
- gateway ping 无关闭开关（登记）。
- streamToken 刷新 Go 未实现（C12 out_of_scope）；aden 凭据代理 Go 未移植（unported）。
- senders 的 8 个 send-log/setup 客户端方法（reserve/complete/usage/suppress/list/create/verified）HTTP 方法/路径未恢复，已在 `go-hive-cloud/contracts/cloud-surface.json` 登记为 `unknown`；`GET /v1/senders/runtime` 为 `inferred`。
- 本页为总账；`hive-inbox.html` 是 Inbox 单服务深页（协议/数据模型/流程）。
