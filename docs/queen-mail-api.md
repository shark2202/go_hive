# Queen 邮件 HTTP 端点契约（`/api/queen-mail/*`）

Claude Code queen 的每次 `claude -p` 回合通过 stdio MCP server（`hive` server，见 `docs/queen-mail.md` §6.1）
回调 `hive serve` 的这两个端点；它们只服务这一条通路（Hive 内 queen 不经此面）。

上游事实源：`core/framework/server/queen_mail.py`（`handle_queens`:506 / `handle_letter`:518 / `register_routes`:537）。
Go 实现：`go_hive/go-hive/internal/server/routes_queens_mail.go`（`handleQueenMailQueens`:46 / `handleQueenMailLetter`:65 / `RegisterQueenMailRoutes`:96；mux 注册行 :97/:100）+ `internal/server/adapters_qmail.go`（backend）。
路由注册表：`contracts/api-routes.json` 两条目（reference=Python handler，target=Go 文件:行）。
状态码契约由路由层持有（Go 源码注释：*the routes own the 401/404/400 contract*）。

## 1. 端点总表

| 方法 + 路径 | 谁调用 | 用途 |
| --- | --- | --- |
| `GET /api/queen-mail/queens?from=<conversation ref>` | MCP `list_queens` | 该会话的 queen 可以写信的对象 |
| `POST /api/queen-mail/letters` | MCP `message_queen` | 从该会话发出一封信 |

## 2. 认证（两端点相同）

- 请求头 `X-Queen-Mail-Token: <token>`；token = `HMAC-SHA256(进程内 secret, from)`（`Mail.TokenFor`），**只对该会话有效**、不落盘、随 serve 进程轮换。
- `from`（GET 用 query、POST 用请求体）必须给出；token 与 `from` 不匹配、`from` 为空、queen mail 未运行 → **401** `{"error": "unauthorized"}`。
- 两侧实现同语义：Python `_mail_for` / Go `mailFor`。

## 3. `GET /api/queen-mail/queens`

请求：query `from`。

| 状态 | 响应体 | 条件 |
| --- | --- | --- |
| 200 | `{"queens": [{"id", "name", "title"}, ...]}` | 除自己以外的全部参与 queen：Hive 内 queen（`enabled` 且在该 queen 的 `queens` 列表内）+ Claude Code queen；`name`/`title` 缺省时回退（name→id、title→空） |
| 401 | `{"error": "unauthorized"}` | token 缺失/无效 |
| 404 | `{"error": "This conversation isn't known to Hive"}` | token 有效但 `from` 不是该 queen 的会话 |

Go 侧空列表归一为 `[]`（不返回 `null`）。

## 4. `POST /api/queen-mail/letters`

请求体：JSON object `{"from": "<conversation ref>", "to": "<queen id>", "message": "<正文>"}`。

成功 **200**：`{"sent": "hop_<12hex>", "to": "<to>", "depth": <n>}`（`depth` = 这条信链的第几跳）。

失败一律 `{"error": "<文案>"}`；**400 的文案就是模型可见文本，逐字对齐**：

| 状态 | 文案（逐字） | 条件 |
| --- | --- | --- |
| 401 | `unauthorized` | 见 §2 |
| 400 | `The body isn't JSON` | 请求体不是 JSON |
| 400 | `The body must be a JSON object` | JSON 但不是 object |
| 400 | `This conversation isn't known to Hive` | token 对但 ref 未知 |
| 400 | `You don't take part in queen mail (queen_mail in configuration.json)` | 写信 queen 不在参与列表 |
| 400 | `The message is empty` | `message` 去空白后为空 |
| 400 | `That's your own id` | `to` 是自己 |
| 400 | `There's no queen '<to>' to write to; list_queens names them` | `to` 不存在/不参与（`'<to>'` 为 Python repr 形式，Go 用 `pyReprString` 复刻） |
| 400 | `This chain of letters already has <N> hops, the most queen_mail.max_depth allows. It starts again when someone else writes to you.` | 信链超过 `max_depth` |
| 400 | `You've sent <N> letters today, the most queen_mail.daily_limit allows` | 日限用尽（回复不计数） |
| 400 | `<to> can't take letters now` / `<to> can't take letters now: <e>` | 收信方当前不可投递（运行器/投递器不可用） |

## 5. 设计要点

- 这条回调面是 **Claude Code queen 运行器**（`docs/claude-code-queen-runner.md`）的专用通道：stdio MCP server 从 env 取 `HIVE_QUEEN_MAIL_URL/TOKEN/FROM`，唯一消费者就是它。
- 发信立即返回 hop id；**回复**异步走会话事件总线回到写信人（排队、日限、深度、审计都在 serve 进程内，见 `docs/queen-mail.md` §3/§4）。
- 无附件参数（上游 `message_queen` 同形）；正文不落日志（只存会话）。
- 超时：MCP 侧 180s（防止"报失败但信到了"重复投递）。

## 6. 实现落点与证据

| 面 | 位置 |
| --- | --- |
| Python handlers | `core/framework/server/queen_mail.py:506`（queens）/ `:518`（letters） |
| Go 路由 | `internal/server/routes_queens_mail.go:46` / `:65`（`RegisterQueenMailRoutes:96`） |
| Go adapter | `internal/server/adapters_qmail.go`（`Authorizes`/`QueenOf`/`Correspondents`/`SendFromConversation`） |
| 路由注册表 | `contracts/api-routes.json`（两条目，python+go 源位置） |
| C12 登记 | `queen_mail.py` → `internal/server/adapters_qmail.go`（ported） |
| 两侧互映测试 | Python `test_the_routes_want_the_token_and_one_of_her_conversations`（`test_claude_code_queens.py:272`：401×3、200、响应体逐字） ↔ Go `TestQueenMailRoutesWantTheTokenAndOneOfHerConversations` / `TestQueenMailQueensUnknownConversationIs404` / `TestQueenMailLetterValidatesBodyAndRefusals` |
| MCP 协议面证据 | `docs/e2e-assets/queen-mail-mcp/`（probe.py + handshake.json，字段级双跑） |

待补（登记）：端点级 HTTP 双跑探针（当前为两侧路由测试互映 + 2026-11-21 双跑"401/404/400 文案一致"结论；尚无独立落盘的 HTTP 对拍脚本）。

## 7. 相关

- `docs/queen-mail.md` —— 引擎语义（配置/工具面/投递/回复/审计/差异/双 queen E2E）
- `docs/claude-code-queen-runner.md` —— Claude Code queen 运行器（MCP 注入、回合、超时）
- `docs/claude-code-dashboard.md` —— 只读 dashboard 视图
