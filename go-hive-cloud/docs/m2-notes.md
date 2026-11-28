# M2 实现笔记 —— Inbox 运行时契约硬化与真客户端 L2b 闭环

> 状态：M2（2026-11-23）。范围=README §4 的 M2 行：**Inbox L2（go_hive Sentinel 真实
> escalation → 自建服务 → 回复回注闭环）+ 审计/限流**。
>
> 口径：M2 **不新增猜测的云端点**。`contracts/observed-contract.json` 只记录三条 Inbox 端点
> （`POST /v1/inbox/messages`、`POST /v1/inbox/resolve`、`GET /v1/inbox/runtime/stream`）；这三条在 M1
> 已注册，M2 的职责是把它们从「路由存在」提升为「字段/错误码/auth/SSE 帧可验证」，并用真实客户端跑通闭环。
> 只有文档提及、没有结构化契约的桌面 UI 端点继续登记为 `unknown`，**不实现**（详见 §2）。

## 1. 交付物与验证路径

| 层 | 实现 | 验证 |
| --- | --- | --- |
| L1 契约 | `internal/httpapi/inbox_contract_test.go`：字段落库、错误码、auth、SSE 头/注释帧/事件帧、fan-out、隔离、补投一次、audit、限流 | `go test ./...` 全过 |
| L2b 真客户端 | `harness/inbox-smoke`（独立 module，`replace github.com/shark2202/go_hive/go-hive => ../../../go-hive`）：`sentinel.HTTPNotifier.Send(ChannelHive, …)` 上报 → 内部回复 → `sentinel.HiveInboxListener` 收 `event: reply` 并 `OnInbound` | `scripts/inbox-smoke.sh` → `evidence/inbox-smoke.json`，7/7 |
| 审计 | `_cloud_audit` 记录 `inbox_message / inbox_resolve / inbox_reply`，身份来自 JWT | `TestInboxAuditTrail` |
| 限流 | 可选 per-team 固定窗口（默认关闭） | `TestInboxRateLimitExtension` |

`go test ./...` 覆盖 cloud module 全部包；两个 harness 是嵌套 module，各自 `go vet/go build`。

## 2. 已实现 / 未实现（按证据分级）

### 2.1 已实现（observed-contract 有三条完整记录）

| 端点 | 契约要点 | M2 新增验证 |
| --- | --- | --- |
| `POST /v1/inbox/messages` | `runtime_id/session_id/colony_id/kind/title/body/correlation_token/deep_link`；`kind` 缺省 `blocker`；200 `{"id"}`；400 `invalid JSON body` / `invalid kind (want blocker|heartbeat|progress|done)` / `session_id is required`；401 逐字句子 | 落库字段逐列断言（含 `deep_link` NULL、`user_id/team_id` 来自 JWT、缺省 kind）；四种 kind；尾随 JSON；streamToken 拒绝 |
| `POST /v1/inbox/resolve` | `{session_id, resolved_by}`；客户端只判 `<300` | 同 session 多条一次关闭（`resolved=2`），重复关闭 0；`resolved_by/resolved_at` 落库 |
| `GET /v1/inbox/runtime/stream` | `runtime_id` query 必填；SSE `event: reply` + `{correlation_token,text}`（`colony_id`/`message_id` 可选） | 响应头、`: connected`、`: keepalive`、reply 帧单行 JSON；同 runtime 多订阅者都收到；其它 runtime 收不到；离线回复补投一次；`kind` 只登记连接、不过滤回复 |

### 2.2 未实现（只有文档路径，无请求/响应形状；继续 `unknown`）

来源：`docs/internal/hive-inbox-design.md` §8 与 `go_hive/docs/hive-inbox.html` §02。它们是闭源 Inbox UI 的
设备端接口，**不在 `contracts/observed-contract.json`**，字段/事件/分页/已读模型均不可恢复。

| 路径 | 文档语义 | 本服务行为 |
| --- | --- | --- |
| `GET /v1/inbox/messages` | 列表（own + team-visible，unread 过滤） | **405**（路径被已实现的 POST 占用；不是空 200） |
| `POST /v1/inbox/messages/{id}/reply` | 用户答复 | 404 |
| `POST /v1/inbox/messages/{id}/read` | 单条已读 | 404 |
| `POST /v1/inbox/messages/mark-all-read` | 全部已读 | 404 |
| `GET /v1/inbox/messages/unread-count` | 角标未读数 | 404 |
| `POST /v1/inbox/runtime/heartbeat` | presence 保活（页面注明 runtime 从未调用） | 404 |
| `GET /v1/inbox/stream` | 桌面 SSE（P1 实际用 15s 轮询） | 404 |

这些条目已登记进 `contracts/cloud-surface.json`（status `unknown`，带 source anchor）；`contract_test.go`
的 `TestCloudSurfacePlannedRoutesUnregistered` 断言它们**不被成功服务**（404；唯一 405 是 GET 撞上已实现的
POST 路径），`TestInboxDesktopStreamStaysUnknown` 逐条复核。

**明确没有做的事**：不实现桌面 SSE、不做消息列表/已读/未读/心跳、不猜测 reply 端点形状。自建 UI 仍走
`POST /internal/inbox/reply`（M1 扩展，非契约兼容面）。

## 3. 审计

- 每次 `POST /v1/inbox/messages` → `_cloud_audit(action='inbox_message', detail='id=<id> kind=<kind> session=<sid>')`。
- 每次 `POST /v1/inbox/resolve` → `inbox_resolve`（`session=<sid> resolved=<n>`）。
- 每次 `POST /internal/inbox/reply` → `inbox_reply`（`message=<id> delivered=<n>`）。
- `user_id/team_id` 来自 operator JWT（客户端不传）；审计失败只记日志，不影响主响应（M1 语义，M2 未改）。
- `TestInboxAuditTrail` 直接读 team SQLite 的 `_cloud_audit` 断言 action 计数与身份。
- 真实云的审计格式/保留策略 `UNKNOWN`；这里只保证本地可追溯。

## 4. 限流（私有部署扩展，默认关闭）

观测契约没有任何 429/`Retry-After` 语义，参考客户端也不处理 429。因此：

- 默认 `--inbox-rate-limit 0` = 关闭，行为与 M1 完全一致。
- 开启后：**per-team 固定窗口**，`--inbox-rate-window`（默认 1m）；窗口内第 `limit+1` 次 inbox 写返回
  `429 {"error":"inbox rate limit exceeded"}` + `Retry-After: <秒>`（向上取整，最小 1）。
- 计入写操作：`POST /v1/inbox/messages`、`POST /v1/inbox/resolve`、`POST /internal/inbox/reply`（共享同一
  team 桶）。SSE 连接与读操作不计。
- 配置入口：`--inbox-rate-limit/--inbox-rate-window`、`HIVE_CLOUD_INBOX_RATE_LIMIT` /
  `HIVE_CLOUD_INBOX_RATE_WINDOW_MS`、`config.json` 的 `inbox_rate_limit` / `inbox_rate_window_ms`。
- 这是运维控制，不是云端契约；`contracts/cloud-surface.json` 在三个写端点的 errors 里以 `note` 标注
  “private-deploy extension only; default disabled”。

## 5. L2b 闭环（真实 go_hive Sentinel 客户端）

`harness/inbox-smoke` 直接 import `github.com/shark2202/go_hive/go-hive/internal/sentinel`，不是重写客户端：

1. `sentinel.HTTPNotifier.Send(ctx, sentinel.ChannelHive, …)` → `POST /v1/inbox/messages`（生产代码路径），
   校验 `NotifierResult.OK` + `message_id`。
2. `POST /internal/inbox/reply` 注入答复（真实 Inbox UI 端点未知，故用扩展）。
3. `sentinel.HiveInboxListener.Run` 订阅 `GET /v1/inbox/runtime/stream`，解析 `event: reply` 后调用
   `OnInbound("hive","hive","(ref: <token>) <text>", nil, colony_id)`；harness 断言 channel/sender/source/text
   与 `sentinel.FormatRef` 一致。
4. `sentinel.HTTPNotifier.ResolveHiveSession` → `POST /v1/inbox/resolve`，校验 2xx。
5. 负向：streamToken 打 inbox 写端点 → 401 逐字句子；`GET /v1/inbox/stream` → 404。

证据：`evidence/inbox-smoke.json`（7 项全过，含 base_url、message/reply id、监听器收到的文本）。
脚本：`scripts/inbox-smoke.sh`；`scripts/smoke.sh` 现在先跑 L2a global-db（14/14）再跑 L2b inbox（7/7）。

## 6. 门禁命令

```bash
source /Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh   # go1.26.11 + 模块缓存
cd go_hive/go-hive-cloud
gofmt -l .                                  # 期望无输出
go vet ./...
go test ./...                               # L1：含 inbox 契约/SSE/审计/限流
python3 scripts/gen_cloud_surface.py --check
./scripts/smoke.sh                          # L2a 14/14 + L2b 7/7
```

harness 单独构建：`cd harness/globaldb-smoke && go vet ./... && go build ./...`；
`cd harness/inbox-smoke && go vet ./... && go build ./...`。

## 7. 已知未知 / 后续

| 项 | 状态 |
| --- | --- |
| 桌面 Inbox UI 列表/回复/已读/未读/心跳端点 | `UNKNOWN`（§2.2；只有路径，无形状；未实现） |
| `GET /v1/inbox/stream`（桌面 SSE） | `UNKNOWN`（事件名/载荷不可恢复；404） |
| `kind` 对 runtime stream 的过滤语义 | `UNKNOWN`；本实现只做连接登记，不过滤回复 |
| 消息列表分页、未读计数、read-state 模型 | `UNKNOWN`；`inbox_message_reads` 表存在但无端点 |
| 真实云审计格式、限流/配额 | `UNKNOWN`；本实现为本地扩展 |
| 回复投递的 at-least-once 语义 | 本实现补投一次并标记 delivered；真实云 `UNKNOWN` |
| streamToken 轮换/吊销 | M3 已落地双钥轮换与旧 token 边界；sid blacklist/吊销账本仍 `UNKNOWN`（docs/m3-notes.md） |
| 另一种 SSE 负载（桌面/网页） | 未实现；M2 只保证运行时 reply 流 |

## 8. 指纹/证据

- `evidence/inbox-smoke.json`：`scripts/inbox-smoke.sh` 生成，7 项真客户端检查全过。
- `evidence/globaldb-smoke.json`：随 `scripts/smoke.sh` 重跑（14/14），时间戳/base 会更新。
- L1：`internal/httpapi/inbox_contract_test.go`；surface：`contracts/cloud-surface.json` + `docs/cloud-surface.html`。
- 本文件 §2.2、§4、§7 是 M2 的诚实边界：没有契约支撑的端点不实现，扩展行为明确标注为非契约。
