# Cloud surface —— API / SSE / WS 不变量

> 机器可读本体：`contracts/cloud-surface.json`（schema `hive-cloud-surface/v1`）。
> 人类可读页：`docs/cloud-surface.html`（由 JSON 渲染，不手写）。
> 生成器：`scripts/gen_cloud_surface.py`；门禁：`scripts/check_cloud_surface.sh`。

## 1. 为什么存在

`contracts/observed-contract.json` 是**逆向观察记录**，不是全量清单：它有散文、局部端点，senders 只覆盖了
`GET /v1/senders/runtime`，且没有任何机制阻止代码新增一条未登记的路由。`cloud-surface.json` 把
**全部已知端点 + SSE + 未来 WebSocket** 收进一个机器可读文件，并让路由集合、observed 覆盖、SSE 事件名
三者成为门禁：

- 新增/删除路由而不改 surface → `--check` 失败；
- `contract_test.go` 不再硬编码 15 条名单，而是断言 `Routes()` 与 surface 的 `implemented` 集合严格相等；
- 标记为 `planned`/`inferred`/`unknown` 的端点被注册 → 契约测试失败；
- 代码写出新的 SSE event、或出现 WebSocket/Upgrade 代码 → `--check` 失败。

## 2. 状态口径

| status | 含义 | 是否允许有路径 |
| --- | --- | --- |
| `implemented` | 本模块今天在 `server.go::register()` 注册的路由 | 是（必须与路由逐字一致） |
| `planned` | 已观察/已知、属于后续里程碑（M2–M7） | 是 |
| `inferred` | 客户端文档/代码给出路径，但结构化契约未收录（如 `GET /v1/senders/runtime`） | 是 |
| `unknown` | 只有客户端方法名，HTTP 方法/路径不可恢复 | 否，或方法+路径两者都有锚点；**不得猜测** |

`auth` 枚举：`bearer-jwt`、`bearer-stream-token`、`api-key`、`none`。前两者中的
`bearer-stream-token` 在路由层是公开的（凭据在 body 里），`api-key` 目前只出现在未实现服务上。

`milestone`：M1（已落地）、M2（Inbox 契约硬化 + L2b 真客户端闭环，已落地）、M3（streamToken 轮换 + LLM 仅校验面，已落地）、M4（CRM：7 个通用 grid 端点 + `grid_query` SQL 兼容层已落地；schema/claim/release 仍 `planned`）、
**M4.5（senders cloud 兼容）**、M5（LLM 代理 Messages/Images 透传，已落地；模型路由仍 UNKNOWN）、M6（凭据代理）、M7（部署件）。

## 3. 维护动作

### 新增一条路由（例如 M2 的某个端点）

1. 在 `server.go::register()` 里 `s.handle(...)`；
2. 在 `scripts/gen_cloud_surface.py` 的 `CURATED` 表里把对应端点 `status` 改为 `implemented`，
   `auth`/`request`/`response`/`errors`/`source_anchors` 补全；
3. `python3 scripts/gen_cloud_surface.py` 重新生成 JSON/HTML；
4. `go test ./...` 与 `./scripts/check_cloud_surface.sh` 必须过。

**不要**再往 `contract_test.go` 里加白名单。

### 新增 SSE 事件

在 `STREAMS` 表登记 `event`、`payload_fields`、`golden`，并让端点 `sse_events` 与之对应。生成器会扫描
`internal/httpapi/*.go` 里 `writeSSEEvent(...)` 的字面量事件名与 `"event: <name>\n"` 帧；多一个或少一个都会失败。

### 新增 WebSocket

当前 `websockets: []`。任何 `Sec-WebSocket` / `Upgrade` / `websocket.*` 代码出现而列表为空，`--check` 直接失败。
登记项至少要有 `path`、`subprotocol`、`message_schema`、`milestone`、`status`、`source_anchors`。

## 4. 当前统计（生成值快照，以 `cloud-surface.json` 的 `summary` 为准）

| 指标 | 值 |
| --- | --- |
| 端点总数 | 48 |
| implemented | 24 |
| planned | 7（CRM 3：schema/claim/release；凭据 4） |
| inferred | 1（`GET /v1/senders/runtime`） |
| unknown | 16（senders 8 个客户端方法 + Inbox 桌面 UI 6 + `GET /v1/inbox/stream` + `POST /user/refresh-stream-token`） |
| SSE 流 | 1（`/v1/inbox/runtime/stream`，`event: reply`；`/v1/messages` 的 SSE 由上游定义、原样转发，不枚举事件名） |
| WebSocket | 0 |

## 5. 未知项（权威列表见 `cloud-surface.json` 的 `unknowns`；本页为说明）

1. 真实云端 401 body：observed-contract 写 `{"error":"signed_out"}`，客户端文案是逐字句子；本实现按句子。
2. JWT 签发方/算法/claims/登录设备流。
3. `GET /v1/inbox/stream`（桌面 SSE）的事件名与载荷。
4. `POST /user/refresh-stream-token`（桌面 legacy）的请求/响应。
5. CRM：`GET /v1/crm/grid/schema` 的实体/列/阶段选项响应形状、`claim`/`release` 的响应键与租约语义、
   投影字段/FK 扁平化/`identities` 去重规则均 `UNKNOWN`。M4 只实现通用 grid 的 query/group-counts/
   get/create/patch/delete/stage（参数化 SQL、标识符白名单），schema/claim/release 保持 `planned`。
6. **senders send-log/setup 的 8 个客户端方法没有可恢复的 HTTP 方法/路径**：
   `reserve_send`、`complete_send`、`sender_usage_today`、`suppress_recipient`、`list_suppressions`、
   `list_sends`、`create_team_sender`、`list_provider_verified_senders`。开源仓库里
   `core/framework/credentials/aden/client.py` 只实现 `/v1/credentials*`，senders 方法由运行时扩展客户端提供。
7. LLM 代理内部语义：模型路由/计费/限流/上游 provider key、真实云 Anthropic SSE 事件子集、真实图片后端
   与 multipart edits 重建均为 `UNKNOWN`。M5 只把 body/model 与上游状态/响应/SSE 原样转发到显式配置的
   upstream；没有路由表。
8. 凭据 refresh/validate 的请求体细节。
9. gateway availability 的主机归属：observed-contract 归到 hive-backend，`cloud-dependencies.md` §1.5 指向
   `api.adenhq.com`；路径与 query 透传是确定的部分。
10. `cloud_sync` 对账协议（不在 go-hive-cloud 范围）。
11. Inbox 桌面 UI 的 `GET /v1/inbox/messages`（列表）、`POST /v1/inbox/messages/{id}/reply`、`{id}/read`、
    `mark-all-read`、`GET .../unread-count`：`docs/internal/hive-inbox-design.md` §8 只有路径与用途，
    `observed-contract.json` 无请求/响应形状——登记 `unknown`，不实现（`GET /v1/inbox/messages` 因 POST
    已注册而返回 405，其余 404；绝不空 200）。
12. `POST /v1/inbox/runtime/heartbeat`：页面注明端点存在但 runtime 从未调用；请求/响应 `UNKNOWN`，未实现。
13. Inbox runtime stream 的 `kind` 过滤语义、消息列表分页、未读计数/`inbox_message_reads` 模型：`UNKNOWN`；
    本实现只把 `kind` 记为连接登记，不过滤回复。

## 6. 门禁命令

```bash
cd go_hive/go-hive-cloud
python3 scripts/gen_cloud_surface.py --check   # 或 ./scripts/check_cloud_surface.sh
gofmt -l .
go vet ./...
go test ./...
./scripts/smoke.sh          # M1 + M2 + M3 + M5 + M4
./scripts/inbox-smoke.sh    # 仅 L2b
./scripts/m5-smoke.sh       # 仅 M5（Messages/Images 透传）
./scripts/m4-smoke.sh       # 仅 M4（grid_query SQL + CRM grid 子集）
```

`--check` 输出示例：

```
cloud-surface: OK — 48 endpoints (implemented 17 / planned 14 / inferred 1 / unknown 16), SSE 1, WS 0;
observed-contract 31 classified; routes 17/17
```

## 7. 与 observed-contract 的关系

`observed-contract.json` 保持不变（它是证据快照）；`cloud-surface.json` 是**超集 + 分类**。
生成器要求 observed-contract 的每一条端点都在 surface 中有同 `method+path` 的记录，因此新增观察结果
不登记会被门禁拦下。senders 的 unknown 条目没有 path，无法与 observed 对齐——它们不在 observed 中，
属于 2026-11 补录的客户端调用面。
