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

`milestone`：M1（已落地）、M2（Inbox L2）、M3（streamToken 轮换）、M4（CRM）、**M4.5（senders cloud 兼容）**、
M5（LLM 代理）、M6（凭据代理）、M7（部署件）。

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
| 端点总数 | 42 |
| implemented | 15 |
| planned | 16（CRM 10、LLM 2、凭据 4） |
| inferred | 1（`GET /v1/senders/runtime`） |
| unknown | 10（senders 8 个客户端方法 + `GET /v1/inbox/stream` + `POST /user/refresh-stream-token`） |
| SSE 流 | 1（`/v1/inbox/runtime/stream`，`event: reply`） |
| WebSocket | 0 |

## 5. 未知项（权威列表见 `cloud-surface.json` 的 `unknowns`；本页为说明）

1. 真实云端 401 body：observed-contract 写 `{"error":"signed_out"}`，客户端文案是逐字句子；本实现按句子。
2. JWT 签发方/算法/claims/登录设备流。
3. `GET /v1/inbox/stream`（桌面 SSE）的事件名与载荷。
4. `POST /user/refresh-stream-token`（桌面 legacy）的请求/响应。
5. CRM 实体 schema、阶段规则、claim/release 语义。
6. **senders send-log/setup 的 8 个客户端方法没有可恢复的 HTTP 方法/路径**：
   `reserve_send`、`complete_send`、`sender_usage_today`、`suppress_recipient`、`list_suppressions`、
   `list_sends`、`create_team_sender`、`list_provider_verified_senders`。开源仓库里
   `core/framework/credentials/aden/client.py` 只实现 `/v1/credentials*`，senders 方法由运行时扩展客户端提供。
7. LLM 代理的模型路由/计费/限流；Anthropic 流式事件子集。
8. 凭据 refresh/validate 的请求体细节。
9. gateway availability 的主机归属：observed-contract 归到 hive-backend，`cloud-dependencies.md` §1.5 指向
   `api.adenhq.com`；路径与 query 透传是确定的部分。
10. `cloud_sync` 对账协议（不在 go-hive-cloud 范围）。

## 6. 门禁命令

```bash
cd go_hive/go-hive-cloud
python3 scripts/gen_cloud_surface.py --check   # 或 ./scripts/check_cloud_surface.sh
gofmt -l .
go vet ./...
go test ./...
./scripts/smoke.sh
```

`--check` 输出示例：

```
cloud-surface: OK — 42 endpoints (implemented 15 / planned 16 / inferred 1 / unknown 10), SSE 1, WS 0;
observed-contract 31 classified; routes 15/15
```

## 7. 与 observed-contract 的关系

`observed-contract.json` 保持不变（它是证据快照）；`cloud-surface.json` 是**超集 + 分类**。
生成器要求 observed-contract 的每一条端点都在 surface 中有同 `method+path` 的记录，因此新增观察结果
不登记会被门禁拦下。senders 的 unknown 条目没有 path，无法与 observed 对齐——它们不在 observed 中，
属于 2026-11 补录的客户端调用面。
