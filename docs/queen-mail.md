# Queen 邮件（queen_mail）与会话工具

上游事实源：`core/framework/tools/queen_mail_tools.py`、`core/framework/server/queen_mail.py`、`core/framework/server/channels.py`。
上游测试：`core/framework/server/tests/test_queen_mail.py`、`core/framework/server/tests/test_claude_code_queens.py`。

Go 实现分布：

| 层 | 文件 |
| --- | --- |
| 引擎（信件、队列、日限、深度、审计） | `internal/queenmail/` |
| 服务器适配（投递、线程会话、回复唤醒） | `internal/server/adapters_qmail.go` |
| HTTP 路由（Claude Code queen 的 `list_queens` / 写信入口） | `internal/server/routes_queens_mail.go` |
| 会话工具（`list_queens` / `message_queen`） | `internal/cli/session_tools_queen_mail.go` |
| 接线（一个进程一个服务实例） | `internal/cli/loader.go`、`internal/server/app.go`、`internal/server/session.go` |

## 1. 配置

`configuration.json` 的 `queen_mail` 段：

```json
"queen_mail": {
  "enabled": true,
  "queens": ["queen_ava", "queen_ivy"],
  "daily_limit": 100,
  "max_depth": 8
}
```

| 键 | 默认 | 语义 |
| --- | --- | --- |
| `enabled` | `false` | 是否给 queen 会话注册 `list_queens` / `message_queen`，以及是否接受信件 |
| `queens` | 全部 queen | 参与互写的 queen id 列表；缺省时目录里的每个 queen 都参与（被移除的 queen 除外） |
| `daily_limit` | `100` | 一个 queen 每天最多发送的「信」数；回复不计数 |
| `max_depth` | `8` | 一条信链最多几跳；超过后写信被拒绝，回复被丢弃并记录 |
| `max_parallel_turns` | `2` | 仅 Claude Code queen 使用：同时运行的 `claude -p` 回合数上限；见 `docs/claude-code-queen-runner.md` |

Go 在**启动 `hive serve` 时**读取该段并构造 `qmail.Mail`；修改配置后需要重启服务，新会话才会看到新的开关状态（上游 Python 在每次注册/发送时重读配置）。

## 2. 会话工具面

两个工具只注册在 **queen 聊天会话**的工具表里：

- 会话的 queen 在 `queens` 列表内（或 `queens` 缺省，或会话尚未选 queen）→ 注册；
- `enabled=false`、或会话 queen 不在列表内 → 两个工具都不可见；
- colony worker 的工具池（`colonyWorkerToolSurface`）与 overseer 不注册，与上游 `_QUEEN_INDEPENDENT_TOOLS` / `_QUEEN_COLONY_TOOLS` 的 queen-only 语义一致。

### `list_queens`

无参数。返回：

```json
{"queens": [{"id": "queen_ivy", "name": "Ivy", "title": "Cartographer"}]}
```

只列出**除自己以外**、参与 queen mail 的 queen；未开启时返回 `{"error": "Queen mail isn't running in this server"}`。

### `message_queen`

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `queen_id` | string | 是 | 收信 queen 的 id，来自 `list_queens` |
| `message` | string | 是 | 信件正文；空白消息被拒绝 |

成功返回（与上游同形）：

```json
{"sent": "hop_63a87e1f0a1b", "to": "queen_ivy", "depth": 1}
```

拒绝/失败返回：

```json
{"error": "There's no queen 'queen_zed' to write to; list_queens names them"}
```

已覆盖的错误：自己给自己写、空消息、未知 queen、非参与方、日限用尽、信链超过 `max_depth`、服务未运行。

工具描述在注册时附上当前可写信的 queen 名单（上游 `_roster`），格式为 `Queens you can write to: Ivy (queen_ivy, Cartographer).`。

## 3. 投递与线程会话

一个进程只有一个 `qmail.Mail`，挂在 `SessionManager.QueenMail()` 上：

- `loader.go` 在 `hive serve` 启动时构造 `QueenMailAdapter`，`CreateSession` 指向真实的 `createChatSession`；
- HTTP 路由、会话工具、deliverer 共用这个实例，因此日限计数、Claude Code token、回信地址不会分裂；
- `app.go` 在测试/嵌入式构造时若没有传入实例，会补建一个并记录到 manager。

写信时把**调用方会话 id** 作为 `from_session`。投递到 Hive queen 的线程会话：

1. 查找 `queens/<queen>/sessions/` 下 `meta.json` 满足 `channel="queens"` 且 `peer=<writer>` 的最新会话；
2. 没有 live 会话时通过 `CreateSession(QueenName=收信 queen, QueenResumeFrom=旧会话)` 启动/恢复；
3. 写 `meta.json` 的 `channel/peer/reply_to/queen_id/title`；
4. 通过 `Chat.InjectQueenMail`（有 chat runtime）或 queen node 的 `InjectEvent` 把信件作为一条客户端消息送入会话；
5. 投递失败（无法启动会话、会话被 colony 锁住、没有 live 会话且没有 create hook）返回明确错误，`message_queen` 原样转成 `{"error": ...}`，不会假装成功。

信件文本形如：

```text
(A letter from Ivy, another queen, sent with message_queen. What you say in this conversation goes back to her, so just answer here; you don't need message_queen to reply.)
[Ivy(queen_ivy)]: Can you map the north shore?
```

## 4. 回复与信链

线程会话第一次收到信时，适配器订阅该会话的事件总线。女王在会话里说的话会作为**回复**送回写信人：

| 事件 | 行为 |
| --- | --- |
| `client_output_delta` | 记住最新 `snapshot` |
| `tool_call_started` | 除 `ask_user` 外，把已说出的文本作为一条回复发出 |
| `tool_call_completed` | `attach_file` 成功时发送 `(She attached: <路径>)` |
| `llm_turn_complete` | 发出剩余文本 |
| `client_input_requested` | `ask_user` 发送 `(She asks:) ...`（带选项）；否则发出剩余文本 |
| `execution_failed` | 发送 `(Her reply failed: <错误>)` |

回复目标按上游 `_reply` 解析：先看该会话最新一封信来自哪个会话（`reply_to`），否则用 `(收信 queen, 写信 queen)` 线程最近的回信地址，再退回 `meta.json` 的 `reply_to`。写信人会话已停止时按上游行为恢复它；恢复失败会写一条 `dropped="unavailable"` 的审计记录并打日志，不静默丢失。

**信链深度**：一封信或回复比它回应的那封信深一跳。`message_queen` 发送时读取调用方会话当前的 cause；当人、定时器或 worker 报告在会话里说话时，`ChatRuntime.OnTurnStart` 调用 `Mail.ForgetCause` 重新开始信链（上游 `_watch` 的 else 分支）。这样两条 queen 互相回复最多 `max_depth` 次，而不是永远续链。

**审计**：每次投递/丢弃写一行 `queens/mail.jsonl`，字段与上游一致：`id`、`at`、`kind`（`message`/`reply`）、`from`、`to`、`from_session`、`to_session`、`caused_by`、`depth`、`chars`，丢弃时加 `dropped`。正文不写日志，只存在会话里。

## 5. 测试

| 测试 | 覆盖 |
| --- | --- |
| `TestQueenMailToolsAreOffUntilQueenMailIsOn` | `enabled=false` 时工具不可见 |
| `TestQueenMailToolsAreTheQueensAlone` | 非参与 queen 不可见；worker 工具池没有；参与 queen 有 |
| `TestQueenMailToolsListTheQueensAndSendALetter` | `list_queens` 内容、发送成功、返回 `{sent,to,depth}`、未知 queen 错误、回信地址是调用方会话 |
| `TestQueenMailToolsSaySoWhenQueenMailIsntRunning` | 没有 `Mail` 实例时的明确错误 |
| `TestQueenMailToolsRefuseOwnIDEmptyMessageAndNonParticipants` | 自写、空消息、非参与方 |
| `TestQueenMailToolsRespectTheDailyLimit` | 日限 |
| `TestQueenMailToolsRespectMaxDepth` | `max_depth` 拒绝与 `ForgetCause` 重开信链 |
| `TestQueenMailToolDescriptionNamesTheCorrespondents` | 工具描述附带 queen 名单 |
| `TestQueenMailRepliesGoBackToTheWriterAndContinueTheChain` | 回复唤醒写信人、回复文本、深度继续为 3 |
| `TestQueenMailAdapterWakesAStoppedWritersSession` | 停止的写信人会话被恢复并收到回复 |

Parity 注解指向 `core/framework/server/tests/test_queen_mail.py` 与 `test_claude_code_queens.py` 的同名测试；`make ci` 的 `parity` / `parity-targets` 门禁会校验引用可解析。

## 6. 已知差异 / 未验证

- **信件附件**：上游 `message_queen` 只有 `queen_id`、`message` 两个参数，没有附件参数；本实现保持同形。回复方向的 `attach_file` 附件会以 `(She attached: ...)` 文本回传（上游 `_reply_text` 的 `files` 分支）。
- **Claude Code queen 运行器**：已实现（`internal/claudecode/claude_runner*.go` + `hive queen-mail-mcp`），配置、回合、超时、回复回写、MCP 注入与测试见 `docs/claude-code-queen-runner.md`；只读 dashboard 路由（`GET /api/claude-code-queens` 与 transcript）读真实配置、会话记录与 Claude Code 会话文件，见 `docs/claude-code-dashboard.md`。
- **配置生效时机**：Go 在 `hive serve` 启动时构造服务；开关变化需要重启。上游在每次注册/发送时重读配置。
- **fork / superseded 会话**：上游 `_newest_successor` 会跟随 `superseded_by` 把回复送到新会话；Go 适配器按会话 id 恢复，未跟随 fork 后的新 id。

## 7. 2026-11-20 真实双 queen 端到端（已验证）

环境：独立 `HIVE_HOME`（临时目录，已删除）+ 真实 provider（`configuration.json` 的 llm），`queen_mail.enabled=true`、`max_depth=4`，`hive serve` 监听 8794。

流程与结果：

1. 通过 `POST /api/queen/queen_growth/session` 建立真实会话，再 `POST /api/sessions/{id}/chat` 要求 `queen_growth` 用 `message_queen` 给 `queen_content_creation` 发信。
2. 会话事件显示 `tool_call_started/completed message_queen`，返回 `{"sent":"hop_f5b724ee47a1","to":"queen_content_creation","depth":1}`。
3. `queens/mail.jsonl` 记录：
   - `message` depth 1：`queen_growth` → `queen_content_creation`，243 chars
   - `reply` depth 2：`queen_content_creation` → `queen_growth`，90 chars
   - `message` depth 3：`queen_growth` → `queen_content_creation`，1002 chars
   - `reply` depth 4：`queen_content_creation` → `queen_growth`，1982 chars
4. 两条回复都回到 `queen_growth` 的原会话（`to_session` 与发信会话一致），证明回复唤醒与信链深度生效。
5. 该测试覆盖**普通 queen 之间**；Claude Code queen 的独立运行器与真实 `claude -p` 回合已另行验证，见 `docs/claude-code-queen-runner.md`。
