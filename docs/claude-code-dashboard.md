# Claude Code queen 只读 dashboard（/api/claude-code-queens）

上游事实源：`core/framework/server/routes_claude_code_queens.py`、`core/framework/server/claude_code_queens.py`（`conversations` / `transcript` / `_session_file` / `_read_transcript`）、前端 `core/frontend/src/api/claudeCodeQueens.ts`、`core/frontend/src/pages/claude-code-queen.tsx`。
上游测试：`core/framework/server/tests/test_claude_code_queens.py::test_the_dashboard_lists_her_conversations_and_reads_one`。

dashboard 对 Claude Code queen（`queen_mail.claude_code`）只有两个只读视图：列表（每位 queen 的配置 + 她的会话）和一个会话的 transcript。数据全部来自本机：配置、运行器写的 `conversations.json`、以及 Claude Code 自己的会话文件；不发任何模型调用。

Go 实现分布：

| 层 | 文件 |
| --- | --- |
| 配置解析（`queen_mail.claude_code.<id>`） | `internal/queenmail/config.go` |
| 会话记录与收件箱（`conversations.json`） | `internal/claudecode/conversations.go` |
| 会话文件发现与解析（transcript） | `internal/claudecode/claude_transcript.go` |
| 「正在运行」标记 | `internal/claudecode/claude_runner.go::Runner.Running` |
| HTTP 路由（schema、404 文案、py repr） | `internal/server/routes_queens_claude_code.go` |
| deps 构造（配置 + 会话 + transcript + running） | `internal/server/claude_code_queens.go` |
| 接线（每进程的 `queenmail.Mail`，与 `/api/queen-mail/*` 同一个实例） | `internal/server/app.go` |

## 1. 数据来源

| 视图字段 | 来源 |
| --- | --- |
| queen `id` / `name` / `title` / `permission_mode` / `auth` / `model` | `configuration.json` 的 `queen_mail.claude_code.<id>`，`hive serve` 启动时解析一次（默认值与上游 `_claude_code_queen` 一致） |
| conversations `session_id` / `peer` | `HIVE_HOME/claude_code/<id>/conversations.json`（运行器与 Python 版共用同一份记录） |
| conversations `peer_name` | 与 `list_queens` 相同的名字解析：Claude Code queen 取配置，Hive queen 取 `queens/<id>/profile.yaml`，都没有则用 id |
| conversations `running` | 进程内运行器的 worker 表：该会话此刻有 `claude -p` 回合在跑 |
| conversations `letters` / `last_at` | Claude Code 的会话文件（见 §3） |
| transcript `entries` | 同上，逐条转成 dashboard 的条目 |

会话文件的路径与上游 `_session_file` 一致：`<config_dir 或 HIVE_HOME/claude_code/<id>/config>/projects/*/<session id>.jsonl`；多个项目目录下同名时取排序后的第一个。

## 2. `GET /api/claude-code-queens`

```json
{
  "queens": [
    {
      "id": "queen_claude",
      "name": "Claude",
      "title": "Engineer",
      "permission_mode": "default",
      "auth": "api_key",
      "model": null,
      "conversations": [
        {
          "session_id": "cc-session-1",
          "peer": "queen_ava",
          "peer_name": "Ava",
          "running": false,
          "letters": 1,
          "last_at": "2026-10-25T10:00:04Z"
        }
      ]
    }
  ]
}
```

| 字段 | 类型 | 语义 |
| --- | --- | --- |
| `id` | string | `queen_mail.claude_code` 的键 |
| `name`, `title` | string | 其他 queen 在 `list_queens` 里看到的身份；缺省 id / `"Claude Code"` |
| `permission_mode` | string | 原样传给 `--permission-mode` 的值 |
| `auth` | `"api_key"` \| `"subscription"` | 她怎么付费；**从不回传 key 本身** |
| `model` | string \| null | `--model`；未配置为 `null`（上游 `str \| None`） |
| `conversations` | array | 她与每个写信人的会话，`last_at` 倒序（无 transcript 的排最后） |
| `conversations[].session_id` | string | 她的 Claude Code session id，也是 transcript 路由的一段 |
| `conversations[].peer` | string | 写信人的 queen id |
| `conversations[].peer_name` | string | 写信人的显示名 |
| `conversations[].running` | bool | 此刻有回合在跑（dashboard 上的绿点） |
| `conversations[].letters` | int | 会话文件里 `kind == "letter"` 的条目数，即她收到并看到的信；不是 `mail.jsonl` 的条数 |
| `conversations[].last_at` | string \| null | 会话文件最后一个条目的 `at`；没有会话文件时为 `null` |

语义：

- `queen_mail.enabled=false`（或配置里没有 `queen_mail`）→ `{"queens": []}`，即使配置了 `claude_code`。
- 没有 queen-mail 服务（`NewClaudeCodeQueenRouteDeps(nil)`）→ `{"queens": []}`，等同上游 `mail is None`。
- 某个 queen 的 `conversations.json` 缺失或损坏 → 她的 `conversations` 为 `[]`。
- 列表顺序：Go 按 queen id 排序（上游保留配置顺序；Go 的配置解析是 map，顺序不可得，这里选择稳定顺序）。
- 永不回传 `api_key`；`ClaudeQueen.String()` 同样遮蔽它。

## 3. `GET /api/claude-code-queens/{queen_id}/conversations/{session_id}`

```json
{
  "entries": [
    {"at": "2026-10-25T10:00:00Z", "kind": "letter", "text": "[Ava(queen_ava)]: Where is the north shore?"},
    {"at": "2026-10-25T10:00:01Z", "kind": "text", "text": "Let me look."},
    {"at": "2026-10-25T10:00:01Z", "kind": "tool", "tool": "Bash", "text": "{\"command\": \"ls maps\"}"},
    {"at": "2026-10-25T10:00:02Z", "kind": "result", "text": "north.txt", "error": false},
    {"at": "2026-10-25T10:00:04Z", "kind": "text", "text": "It is at 61°N."}
  ]
}
```

| 条目 | 字段 | 语义 |
| --- | --- | --- |
| `letter` | `at`, `kind`, `text` | 交给她的一段输入（会话文件里的 user 文本），`[Name(id)]: text` 形态 |
| `text` | `at`, `kind`, `text` | 她说的话 |
| `tool` | `at`, `kind`, `tool`, `text` | 她调用的一次工具：`tool` 是工具名，`text` 是输入的 JSON（键序与分隔符按 Python `json.dumps` 渲染，截断到 600 个字符） |
| `result` | `at`, `kind`, `text`, `error` | 工具返回：`text` 截断到 2000 个字符，`error: true` 表示失败 |

不出现的：她的 thinking、子代理自己的回合（`isSidechain`）、Claude Code 的元数据/附件/记账行（`isMeta`、`attachment` 等）。

语义：

- 会话文件不存在（还没跑过回合，或文件被删）→ `200` + `{"entries": []}`。
- queen 未在 `queen_mail.claude_code` 配置、或该 session 不在她的 `conversations.json` 里 → `404` + `{"error": "No conversation '<session_id>' of '<queen_id>'"}`（Python `repr` 引号规则一致）。
- `queen_mail.enabled=false` 不影响这个路由：上游按 `mail is None` 判断，配置了的 queen 的会话仍可读。
- 没有 queen-mail 服务 → `404`（同上文案）。
- API 路径里的 `..` 段由全局中间件拦成 `400` + `{"error": "invalid path"}`，不会重定向出 API。

## 4. 测试

| 测试 | 覆盖 |
| --- | --- |
| `TestTheDashboardListsTheConfiguredQueensAndReadsOne` | 真实配置解析 + `conversations.json` + 会话文件：列表字段、`model: null`、`peer_name`、letters/last_at、5 条 transcript、未知 session/queen 的 404 与文案、API key 不出现 |
| `TestTheDashboardIsEmptyWithQueenMailOffYetReadsAConfiguredQueensTranscript` | `enabled=false`：列表空、transcript 仍读（上游 `mail is None` 语义） |
| `TestTheDashboardWithoutQueenMailRuntimeIsEmpty` | 无 queen-mail 服务：列表空、transcript 404 |
| `TestNewAppServesTheConfiguredClaudeCodeQueens` | 装配守卫：`NewApp` 真的把 qmail 服务接进这两个路由（不再硬编码空 deps）；`..` 路径为 400 |
| `TestTheDashboardListsHerConversationsAndReadsOne`（`routes_queens_claude_code_test.go`） | 路由层 schema 与 404（用注入的 deps） |
| `TestReadTranscriptShowsLettersSpeechToolsAndResults` | `_read_transcript` 逐条对齐上游 fixture：letter/text/tool/result，跳过 thinking、isMeta、attachment、isSidechain |
| `TestSessionFileFindsHerProjectSession` | `projects/*/<session>.jsonl` 发现、排序取第一个、缺省 config_dir 落在 `HIVE_HOME/claude_code/<id>/config` |
| `TestReadTranscriptClipsLongTextAndMarksFailedTools` | 工具输入 600 / 工具结果 2000 字符截断（按 code point）与 `error` 标记 |
| `TestReadTranscriptOfMissingOrBrokenFileIsEmpty` | 缺失/损坏文件读成空 |
| `TestRunningMarksAConversationWithATurnInFlight` | 「正在运行」标记：回合在跑为 true，跑完落回 false（fake `claude` 驱动真实运行器） |

## 5. 实测（2026-11-20）

临时 `HIVE_HOME`（一份 `queen_mail` 配置、一位 `queen_claude`、一份 `conversations.json` 与一个会话文件），`hive serve --port 8899`：

```console
$ curl -s http://127.0.0.1:8899/api/claude-code-queens
{"queens":[{"auth":"api_key","conversations":[{"last_at":"2026-10-25T10:00:04Z","letters":1,"peer":"queen_ava","peer_name":"Ava","running":false,"session_id":"cc-session-1"}],"id":"queen_claude","model":null,"name":"Claude","permission_mode":"default","title":"Engineer"}]}

$ curl -s http://127.0.0.1:8899/api/claude-code-queens/queen_claude/conversations/cc-session-1
{"entries":[{"at":"2026-10-25T10:00:00Z","kind":"letter","text":"[Ava(queen_ava)]: Where is the north shore?"},{"at":"2026-10-25T10:00:01Z","kind":"text","text":"Let me look."},{"at":"2026-10-25T10:00:01Z","kind":"tool","text":"{\"command\": \"ls maps\"}","tool":"Bash"},{"at":"2026-10-25T10:00:02Z","error":false,"kind":"result","text":"north.txt"},{"at":"2026-10-25T10:00:04Z","kind":"text","text":"It is at 61°N."}]}

$ curl -s -w ' status=%{http_code}\n' http://127.0.0.1:8899/api/claude-code-queens/queen_claude/conversations/no-such-session
{"error":"No conversation 'no-such-session' of 'queen_claude'"} status=404

$ curl -s --path-as-is -w ' status=%{http_code}\n' http://127.0.0.1:8899/api/claude-code-queens/../queens
{"error":"invalid path"} status=400
```

没有 `queen_mail` 的 `HIVE_HOME`（另一个实例，端口 8901）：

```console
$ curl -s -w ' status=%{http_code}\n' http://127.0.0.1:8901/api/claude-code-queens
{"queens":[]} status=200

$ curl -s -w ' status=%{http_code}\n' http://127.0.0.1:8901/api/claude-code-queens/queen_claude/conversations/cc-session-1
{"error":"No conversation 'cc-session-1' of 'queen_claude'"} status=404
```

## 6. 已知差异 / 未对齐

- **queen 顺序**：Go 列表按 queen id 排序；上游按 `configuration.json` 的书写顺序。配置解析成 map 后顺序不可得，这里选确定性优先。
- **每次请求重读会话文件**：与上游相同（上游也在 `asyncio.to_thread` 里逐个会话读），未加缓存；会话文件很大时列表请求的代价随之增长。
- **transcript 的 `at`**：原样取会话文件的 `timestamp` 字段（通常 ISO 8601 UTC 字符串）；未做时区归一化，与上游一致。
- **只读**：没有写操作（发信、改配置）——与上游这两个路由一致；写入口仍是 `message_queen` 工具与 `/api/queen-mail/*`。
