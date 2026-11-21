# Claude Code queen 运行器（queen_mail.claude_code）

上游事实源：`core/framework/server/claude_code_queens.py`、`core/framework/server/queen_mail_mcp.py`、`core/framework/config.py::ClaudeCodeQueenConfig`（`docs/configuration.md` 的 queen_mail.claude_code 段）。
上游测试：`core/framework/server/tests/test_claude_code_queens.py`。

一位 Claude Code queen 就是 Claude Code 本身：其他 queen 用 `list_queens` / `message_queen` 找到她并写信；每封信触发一次 `claude -p` 回合，在她与写信人之间持久化的会话里（首轮 `--session-id`，之后 `--resume`），回合结束的最终文本按 `reply_to` 回写到写信人的会话。

Go 实现分布：

| 层 | 文件 |
| --- | --- |
| 配置解析（完整 entry） | `internal/queenmail/config.go` |
| 运行器（队列、回合、环境、参数、MCP 配置、结果解析） | `internal/claudecode/claude_runner.go`、`internal/claudecode/claude_runner_turn.go` |
| 会话记录与收件箱（conversations.json、inbox） | `internal/claudecode/conversations.go` |
| 进程组创建 / 进程树终止 | `internal/claudecode/procsys_unix.go`、`internal/claudecode/procsys_windows.go` |
| `hive` MCP server（stdio JSON-RPC） | `internal/queenmail/mcp_stdio.go` |
| CLI 子命令 `hive queen-mail-mcp` | `internal/cli/queen_mail_mcp.go` |
| 只读 dashboard 视图（`/api/claude-code-queens`） | `internal/claudecode/claude_transcript.go`、`internal/server/claude_code_queens.go`、`internal/server/routes_queens_claude_code.go`（见 `docs/claude-code-dashboard.md`） |
| 接线（每进程一个运行器；serve 监听后回填 base_url） | `internal/server/adapters_qmail.go`、`internal/cli/loader.go` |

## 1. 配置

`configuration.json` 的 `queen_mail.claude_code.<id>`：

```json
"queen_mail": {
  "enabled": true,
  "queens": ["queen_ava", "queen_ivy"],
  "claude_code": {
    "queen_claude": {"name": "Claude", "api_key": "sk-ant-...", "permission_mode": "acceptEdits"}
  }
}
```

| 键 | 默认 | 语义 |
| --- | --- | --- |
| `name`, `title` | id、`"Claude Code"` | 其他 queen 在 `list_queens` 里看到的名字与头衔 |
| `command` | `"claude"` | Claude Code 程序：字符串，或「程序 + 前置参数」的列表 |
| `auth` | `"api_key"` | `"api_key"`（支持的方式）或 `"subscription"`（她 config_dir 里已登录的 Claude 计划，风险自负） |
| `api_key` | 无 | 她的 Anthropic key。没有它且非 `subscription` 时她不会运行，写信人会收到明确原因 |
| `api_base` | 无 | 她运行时的 `ANTHROPIC_BASE_URL`（API 前的网关） |
| `config_dir` | `HIVE_HOME/claude_code/<id>/config` | 她的 `CLAUDE_CONFIG_DIR`：设置、会话、订阅登录都在这里，与用户自己的 Claude 配置隔离 |
| `cwd` | `HIVE_HOME/claude_code/<id>/workspace` | 她回合的工作目录 |
| `permission_mode` | `"default"` | 原样传给 `--permission-mode`；无人值守时 `"default"` 会拒绝所有要先询问的工具，`"bypassPermissions"` 放开一切 |
| `allowed_tools`, `disallowed_tools`, `add_dirs` | 无 | 传给 `--allowedTools` / `--disallowedTools` / `--add-dir` |
| `model` | 她自己的默认 | 传给 `--model` |
| `timeout_minutes` | `30` | 一个回合最长运行时间；超时后回合及其启动的一切都被终止，写信人收到失败原因 |
| `system_prompt` | 无 | 追加在她的 system prompt（`--append-system-prompt`）里，位于 Hive 关于「信怎么来、回信去哪」的说明之后 |
| `env` | 无 | 她运行的额外环境变量（如 Windows 的 `CLAUDE_CODE_GIT_BASH_PATH`） |

解析规则与上游 `_claude_code_queen` 一致：字符串 `command` 单独成为程序；列表逐项保留非空字符串；未知 `auth` 回退 `api_key`；`timeout_minutes` 只接受 ≥1 的整数；`env` 只保留字符串到字符串的键值。`ClaudeQueen.String()` 会隐藏 `api_key`（等价上游 `repr=False`）。配置在 **`hive serve` 启动时读取一次**（上游每回合重读）。

## 2. 一个回合的生命周期

1. 写给她的信进入 `qmail.Mail`：按 (queen, 写信人) 找到或创建会话，ref 形如 `claude_code:<queen id>:<session uuid>`；信件落盘到 `HIVE_HOME/claude_code/<id>/inbox/`（`inbox.jsonl` 记顺序），并通知运行器。
2. 运行器为每个会话起一个 worker（同一会话串行，不同会话可并行；全局上限 `queen_mail.max_parallel_turns`）。worker 把当前收件箱里该会话的信一次性取走（批次）；**回合运行期间到达的信留到下一回合，一起进入**。
3. 批次决定：cause = 最新信件的 hop + 批次里最深的深度；route = 批次中最后一封非空 `from_session`（否则会话记录的 `reply_to`）。
4. 组装环境、参数与 MCP 配置（下两节），以 stdin 把 `"[Name(id)]: text"`（信件）/「回复」文本交给 `claude -p --output-format json`，等待其结束；超时则杀掉**整个进程树**。
5. 解析 stdout 的最后一个 JSON 对象（`is_error`、`result`、`num_turns`）。会话记录修正：`--resume` 报 "No conversation found" 时改回首轮重试一次；`--session-id` 报 "already in use" 时改走 `--resume` 重试一次——成功与否都写回 `conversations.json`。
6. 回合文本非空时经 `Mail.DeliverReply` 回到 route：写信人是 Hive queen → 注入她的会话；写信人也是 Claude Code queen → 进入她的收件箱。深度超过 `max_depth`、参与关闭或不可达时写 `dropped` 审计行（`queens/mail.jsonl`）。

会话记录 `HIVE_HOME/claude_code/<id>/conversations.json`（与 Python 版可互读）：

```json
{"conversations": {"<session id>": {"peer": "queen_ava", "reply_to": "session_ava", "started": true}}}
```

`started=true` 表示她的 Claude Code 会话已存在，此后用 `--resume`。

## 3. 环境隔离与付费

她只继承运行所需、能上网的部分（上游同一份白名单）：`PATH`/`PATHEXT`/`SYSTEMROOT`/`WINDIR`/`COMSPEC`/`OS`、`TEMP`/`TMP`/`TMPDIR`、`HOME`/`USERPROFILE`/`APPDATA` 等路径类、`USERNAME`/`USER`/`LOGNAME`/`SHELL`/`TERM`、`LANG`/`LANGUAGE`/`TZ`、`HTTP(S)_PROXY`/`NO_PROXY`/`ALL_PROXY`，以及 `LC_*`、`XDG_*`。**不继承** `HIVE_CREDENTIAL_KEY`、各 provider key、云凭据与任何 token。她始终获得自己的 `CLAUDE_CONFIG_DIR`。

付费：`auth=api_key` 时设置她自己的 `ANTHROPIC_API_KEY`（及可选的 `ANTHROPIC_BASE_URL`）；`auth=subscription` 时不注入任何 key，用 config_dir 里的登录，并在第一次运行时记一条「at your own risk」警告（每进程一次）；两者都无则回合不启动，写信人收到 `(Her reply failed: she has no API key. ...)`。

## 4. `hive` MCP server 注入

每个回合在她自己的目录下写一份 MCP 配置 `HIVE_HOME/claude_code/<id>/mcp/<session id>.json`（**0600**，回合结束删除），内容是 `{"mcpServers": {"hive": {...}}}`：

- `command`: 当前 `hive` 可执行文件（`os.Executable()`；生产中即 `hive serve` 的二进制）
- `args`: `["queen-mail-mcp"]`
- `env`: `HIVE_QUEEN_MAIL_URL`（serve 监听地址，启动后回填）、`HIVE_QUEEN_MAIL_TOKEN`（**只对该会话有效**，HMAC 派生）、`HIVE_QUEEN_MAIL_FROM`（会话 ref）

参数上体现为 `--mcp-config <path> --strict-mcp-config`；`permission_mode` 不是 `bypassPermissions` 时自动把 `mcp__hive__list_queens`、`mcp__hive__message_queen` 加进 `--allowedTools`。`hive queen-mail-mcp` 是一个 stdio JSON-RPC server：支持 `initialize`、`ping`、`tools/list`、`tools/call`（两个工具）以及 FastMCP 同形的 `resources/list`、`prompts/list` 空面（能力、schema 与错误码的逐字段对拍见 `docs/queen-mail.md` §6），HTTP 回连 `/api/queen-mail/queens` 与 `/api/queen-mail/letters`，超时 180s；HTTP 失败或 Hive 的拒绝都会原样变成工具结果文本（不假装成功）。

## 5. 失败与超时的文案（与上游逐字一致）

| 情况 | 写信人读到的内容 |
| --- | --- |
| 没 API key | `(Her reply failed: she has no API key. Set queen_mail.claude_code.<id>.api_key in configuration.json, or "auth": "subscription" to use a Claude plan signed in at your own risk.)` |
| 超时 | `(Her reply failed: her turn ran out of time after <N> min)` |
| 无法启动 | `(Her reply failed: Claude Code couldn't start: <err>)` |
| JSON 结果里 `is_error` | `(Her reply failed: <result 或 subtype 或 error>)` |
| 退出码非 0 且无结果 | `(Her reply failed: Claude Code ended with <code>: <stderr 尾部 500 字符>)` |
| 配置里已没有她 | 不回答（日志告警）；信留在收件箱 |
| 回复送不出去（深度/关闭/不可达） | 记 `dropped` 审计行；不打断写信人 |

## 6. 测试

| 测试 | 覆盖 |
| --- | --- |
| `TestALetterToHerRunsATurnAndHerAnswerGoesBack` | 首轮 `--session-id`、prompt 形态、回复回写与审计 |
| `TestHerConversationWithAWriterIsResumedNextTime` | 第二封信走 `--resume` 同一 session id |
| `TestLettersThatArriveDuringATurnGoInTogether` | 回合中到达的信合并为下一回合 |
| `TestSheWritesToAHiveQueenAndHerAnswerComesBackToTheConversation` | 她从会话写信、对方的回复回到同一会话、5 跳信链 |
| `TestAFailedTurnTellsTheWriter` / `TestATurnThatRunsOutOfTimeTellsTheWriter` | 失败与超时文案 |
| `TestHerRunsUseHerAPIKeyAndGetTheHiveMCPServer` | 环境、cwd、参数、MCP 配置（0600、token、回合结束删除） |
| `TestWithFullPermissionsTheMCPToolsNeedNoAllowlist` | bypassPermissions 不注入 allowedTools |
| `TestAGatewayInFrontOfTheAPIIsHerBaseURL` / `TestWithoutAnAPIKeySheDoesntRunAndTheWriterIsToldWhy` | 网关与缺 key |
| `TestASubscriptionIsUsedOnlyWhenChosenAndTheRiskIsLoggedOnce` | 订阅只在显式选择时使用，警告一次 |
| `TestHerAPIKeyStaysOutOfHerConfigsRepr` | `String()` 隐藏 key |
| `TestAWrongRecordOfHerSessionIsCorrectedAndTheTurnTriedAgain` | lost/taken 会话记录修正并重试 |
| `TestHerRunsGetNoneOfHivesSecrets` | 环境白名单（HIVE_CREDENTIAL_KEY / provider key 不进） |
| `TestAnAnswerThatHappensToSayAlreadyInUseIsKept` | 成功文本里的 "already in use" 不触发重试 |
| `TestClosingKillsARunningTurn` | Close 杀掉运行中的回合、不产生回复 |
| `TestClaudeRunnerLimitsParallelTurns` | `max_parallel_turns` 上限（fake CLI 并发计数） |
| `TestTurnTimeoutKillsTheWholeProcessTree` | 超时杀整个进程组（含孙进程） |
| `TestTheMCPServerWritesThroughHiveWithTheToken` / `TestTheMCPServerPassesHivesRefusalOn` | MCP server 的 HTTP 契约与拒绝透传 |
| `TestTheMCPServerAnswersTheProtocolEdgeCases` | initialize/ping/未知方法/未知工具 |
| `TestQueenMailMCPCommandServesJSONRPC` | `hive queen-mail-mcp` 子命令真的接通 |
| `TestClaudeCodeQueenRealTurnWhenClaudeIsConfigured` | 真实 `claude -p` 联调（默认 skip，`HIVE_CLAUDE_REAL_TEST=1` 开启，见下） |

绝大多数测试用 `internal/claudecode/claude_runner_test.go` 里的 fake `claude` 脚本（记录 args/stdin/cwd/env/MCP 配置、按序输出预置结果、可 sleep、可产出 lost/taken/error）；Parity 注解指向 `core/framework/server/tests/test_claude_code_queens.py` 的同名用例，Go 特有测试带 `Parity-exempt` 理由。

## 7. 真实 Claude Code 联调（已验证，2026-11-20）

`TestClaudeCodeQueenRealTurnWhenClaudeIsConfigured` 使用**临时 HIVE_HOME**、真实配置解析、真实 `claude -p` 进程：`hive` 的写信人（fake deliverer 记录回信）→ 信件 → 运行器回合 → 回复回写 + `conversations.json started=true`。**该用例默认 skip**（避免 `make test` 依赖外网与机器登录态）；要真实跑需显式开启：

```bash
HIVE_CLAUDE_REAL_TEST=1 go test ./internal/claudecode/ -run TestClaudeCodeQueenRealTurnWhenClaudeIsConfigured -v
```

本机结果（2026-11-20，`HIVE_CLAUDE_REAL_TEST=1` 时）：

- exit code `0`，`num_turns=1`，回复文本 `OK`；
- stderr 警告：`[claude-code:unrecognized_model] {"model":"deepseek-flash-1017[1m]","query_source":"sdk"}`（CLI 不认识 config_dir 的 settings 里配置的网关模型别名，但回合照常完成）；
- `total_cost_usd=0.077165`（一次小回合；测试每次运行都会真实计费）。

登录方式：她按 `config_dir` 隔离。联调默认使用 `~/.claude`（含登录/网关设置），可用 `HIVE_CLAUDE_TEST_CONFIG_DIR` 指向别处；没有可用的 Claude Code 时该测试 **skip**（带原因），不会失败。测试输出中的 stderr 与 cost 经 `scrubSecrets` 处理，凭据不会出现在日志里。订阅方式在生产中会记录一条「风险自负」警告（与上游一致）。

## 8. 已知差异 / 未对齐

- **配置热重载**：Go 在 serve 启动时读一次配置；上游每回合重读。
- **MCP 协议面**：只实现 Claude Code 用到的 `initialize` / `ping` / `tools/list` / `tools/call`；未实现 resources/prompts/通知回调。
- **Windows 进程树**：Unix 用进程组 SIGKILL；Windows 仅 `Process.Kill()`（无 `taskkill /T` 等价物），子进程可能存活。
- **收件箱持久化**：等待中的信在磁盘（跨重启保留），强于上游「停服即丢」的内存队列；回合被取走时从收件箱移除。
- **真实联调范围**：只跑了一个回合（回复回写已验证）；「她在真实模型回合里主动调用 MCP `message_queen`」未做真实模型验证（由 fake CLI 的 MCP 配置断言 + MCP server 的 httptest 契约覆盖）。
- **`docs/claude-code-queen.html`** 已刷新为运行器与只读 dashboard 落地状态；`docs/claude-code-dashboard.md` 是 dashboard 视图的细节文档。

## 9. 复现

```bash
cd go_hive/go-hive
go test ./internal/claudecode/ -run Claude -v -count=1     # fake CLI 全套
go test ./internal/claudecode/ -run RealTurn -v -count=1    # 真实 claude -p（计费一次小回合）
bash ../scripts/check_parity.sh . && bash ../scripts/check_parity_targets.sh . ../../core
make ci
```
