# Soul 每日读聊天反思（daily chat-reading reflection）

上游 `core/framework/agents/queen/soul_reflection.py`（683 行）每天在配置小时之后让 queen 读回自己
聊天里的新消息，按提示词的规则提出对 `soul.json` 的修改。Go 侧此前只有「记忆反思结论沉淀」
（`internal/soul/sediment.go`，见文件头 Parity-exempt 说明），没有读聊天的每日反思：`state.json`
一直是空的，路由把它回落到 sediment.json 的记账值。本文件补上这条链路。

| Go 实现 | 上游 | parity 注解位置 |
| --- | --- | --- |
| `internal/soul/reflection.go` | `soul_reflection.py` 读聊天 / 状态 / 校验 / 单次反思 | 各函数上方 `// Parity: framework/agents/queen/soul_reflection.py::<name>` |
| `internal/soul/reflection_run.go` | `due` / `_note_attempt` / `run_due` / `scheduler` | 同上 |
| `internal/cli/loader_soul_daily.go` | `framework/server` 启动时的 `scheduler` 任务 | Parity-exempt（装配层） |
| `internal/cli/loader_soul_reflect.go` | 手动立即反思（`POST .../soul/reflect`）的 serve 装配：当前模型 + 该 queen 的会话 + soul config | Parity-exempt（装配层） |
| `e2e/soulgw` + `e2e/soul_reflection_e2e_test.go` | 真实 `hive serve` 二进制 + 本地 OpenAI 兼容网关的端到端 | Parity-exempt（Go 侧 E2E） |
| `internal/soul/reflection_test.go` | `core/tests/test_soul_reflection.py`（14 例） | 逐例 `// Parity: core/tests/test_soul_reflection.py::<test>` |

复用（未重写）：`internal/soul` 的 `Load/Save/SaveProposal/LoadProposal/PendingProposal/ApplyEdits/
Decide/SectionChars/GetSoulConfig/SoulQueens`，`internal/queen` 的 `LoadQueenProfile/
FormatQueenIdentityPrompt`，`internal/llm` 的 `Completer/CompletionRequest/ToolDef`，
`internal/cli` 的 `resolveChatProvider` 与 `hiveserver.HiveConfig`。零新依赖。

## 1. 状态机：`queens/<id>/soul/state.json`

```json
{
  "cursors": {"<session_id>": "2026-10-20T15:00:00"},
  "last_run": "2026-10-21",
  "last_result": "2 changes",
  "attempts": {"day": "2026-10-21", "count": 1, "at": "2026-10-21T05:00:00+08:00"},
  "last_error": "no API key configured for model ...",
  "last_error_at": "2026-10-21T05:00:00+08:00"
}
```

* `cursors`：每个 session 已读到的事件时间戳；`ReadChat` 只取 `timestamp > cursor` 的事件，
  并把本次读到的最新时间戳写回（没有新事件时保持不变）。
* `last_run` / `last_result`：当天成功后写入（`nothing new` 或 `N changes`），
  `due` 以 `last_run == today` 判「今天不再跑」。路由 `GET /api/queen/{id}/soul` 读这两个键。
* `attempts`：当天尝试次数与时刻。失败 1 小时后重试，每天最多 3 次。
* `last_error` / `last_error_at`：**Go 附加**字段（上游只有日志）。失败**不写 `last_run`**，
  所以当天仍是 due、重试梯子照常生效；不会把失败冒充成一次成功的反思。
  `last_error` 在下一次成功（有内容或无内容）时清除。

`due` 判定顺序（`ReflectionDue`）：`now.Hour() < hour` → 否；review 模式下有 pending proposal → 否；
`last_run == today` → 否；当天 attempts.count ≥ 3 → 否；距 `attempts.at` 不足 1 小时 → 否；否则是。

## 2. 读聊天

* `SessionsToRead`：只列 `queens/<id>/sessions/*`。配置 `sources.file` 指向一个
  `{"<渠道/线程 key>": "<session_id>"}` 的 JSON，`sources.keys`（正则，缺省 `.`）决定哪些 key 的
  session 允许读；文件缺失或不是对象 → 一个都不读并记日志。随后对 `meta.json.title` 做
  `include_titles`（必须命中一个）与 `exclude_titles`（命中即排除，优先）正则过滤。
* `ReadChat`：逐行扫 `events.jsonl`，只看 `client_input_received` / `client_output_delta`；
  `stream_id` 非空且不是 `"queen"` 的事件跳过（worker 流不读）。时间戳必须 `> cursor`。
* 发言人与正文：去掉 `<system-reminder>…</system-reminder>` 后取**最后一个** `[nick(id)]: ` 前缀；
  没有前缀时，渠道会话记为 `(chat)`，面板会话记为 `owner`。
* 她自己的回答：`client_output_delta` 的 snapshot 是累积的，按 `(execution_id, iteration,
  inner_turn)` 分组，每组取最后一个 snapshot，组内时间取第一次出现的时间。
* 裁剪：空白折叠成单行，超过 `message_chars` 时保留头 `min(80, limit/4)` 与尾部，中间 ` … `；
  单条消息为空则丢弃；最后从**最新往旧**保留到 `session_chars` 预算（每条按 `len(text)+20` 计）。
* 行时间显示为 `MM-DD HH:MM`（取事件时间戳第 5–16 字符，`T` 换空格），与上游一致。

## 3. 证据与编辑校验（代码把关，不靠提示词自觉）

* 工具与上游一致：`add_item` / `update_item` / `remove_item` / `finish`，schema 逐字移植。
* **quote 逐字**：证据里的每条 quote 去掉所有空白后必须**逐字**出现在本次读到的材料里
  （正文、以及 bootstrap 时的 `memory <文件>`）；少于 4 个字符（去空白后）直接拒绝。
  失败回给模型的正是上游文案：`ERROR: the quote '…' is not in what you read; copy it exactly`。
* **不同来源**：section 的 `evidence: N` 要求 N 条 quote 来自**不同的人或会话**（`(会话标题, 发言人)`
  去重）。不足时报 `the section <key> needs quotes from N different people or chats; you gave M`。
* **`max_edits`**：每次反思最多 `max_edits` 个修改（bootstrap 用 `bootstrap_edits`）；
  超预算返回 `you have made all N changes this reflection allows; call finish`。
  过期的 item 自动移除算「自己的修改」，不占她的预算（`max_edits + len(expiring)`）。
* **section 上限**：改动后的 section 字符数超过 `limit` 时拒绝，并报差值
  （`would hold 81/80 characters; merge or drop an item first, or say it shorter`）。
* **重复**：section 已有同一条（去空白比较）→ `the section <key> already says that`。
* **同一 item 不能改两次**：被本次改过、或被「过期移除」覆盖的 item → `already being changed`。
* **过期移除**：`max_age_days` 的 section 里，日期早于 `today - max_age_days` 的 item 生成
  `remove` 编辑（`expired=true`），在提示词里标 `[expiring today: re-add it elsewhere if it still matters]`。
* **日期窗口**：item 的日期必须是合法 `YYYY-MM-DD` 且落在 `[today-90d, today]`，否则写今天。

## 4. review / auto / bootstrap

* review（默认）：提议存 `queens/<id>/soul/proposals/<id>.json`（`status=pending`），等
  `POST /api/queen/{id}/soul/proposals/{proposal_id}/decide` 由人决定；有 pending 时当天不再反思。
* auto：保存提议后立即以作者 `her` 落地（`soul.json` + `history/v<N>.json` + `log.jsonl`）。
* bootstrap（首轮）：额外读 `bootstrap_memories` 列出的记忆文件（路径必须仍在
  `memories/agents/queens/<id>/` 内），用 `bootstrap_edits` 预算，最多 16 轮对话；
  **即使 auto 也要人审**（上游 `and not bootstrap`）。
* 有内容但模型一个编辑都没给 → 存 state（`0 changes`）、不落 proposal，返回 proposal 对象。
* 完全没内容（无新消息、无记忆、无过期 item）→ 不调模型，state 记 `nothing new`，返回 `nil`。

## 5. serve 调度点

### 5.1 每日调度（自动）

```
hive serve（internal/cli/loader.go defaultServe）
  └─ r.startSoulDailyReflection(home)            # 新增文件 loader_soul_daily.go
       ├─ runner.Run(stop, 10*time.Minute, 0)    # 独立 goroutine，不阻塞主对话
       │    ├─ 启动即跑一次（进程停机期间错过的当天反思在 boot 补跑）
       │    └─ 之后每 10 分钟一次（上游 scheduler 的 every_s=600）
       └─ 返回 stop()：serve 退出时关闭（幂等，可重复调用）
```

* 每个 tick 遍历 `SoulQueens(configuration.json)`，对 `ReflectionDue` 为真的 queen 依次执行；
  一次只跑一个 queen 的反思，pass 内串行、pass 之间不重叠（`DailyRunner.mu`），
  同一 queen 的反思用 per-queen gate 串行（`IsReflecting` 可查）。
* 模型：`resolveChatProvider(home, "")`（配置里的主模型）。上游 `llm_for` 在 `model: worker` 时优先
  取 worker 模型；Go 侧没有进程级 worker LLM（colony host 各自持有），`worker` 与 `queen` 都落到
  配置模型，见「未对齐项」。
* `max_tokens = max(llm.max_tokens, 8192)`，与上游 `get_aux_max_tokens()` 的默认一致。
* 失败路径：模型构建失败或反思报错 → `log.Printf`（进运行时日志环 `GET /api/runtime-logs`）+
  `state.last_error`；attempt 已计数，1 小时后重试，当天 3 次后不再跑。**不会被记成一次成功**。
* tick 内部 `recover()`，一个 queen 的 panic 不会带走 server。

### 5.2 手动立即反思（HTTP，`POST /api/queen/{id}/soul/reflect`）

```
hive serve（internal/cli/loader.go defaultServe）
  └─ AppDeps.SoulReflect = newSoulReflectFor(home, args.str("model"))   # loader_soul_reflect.go
       └─ NewApp: RegisterSoulRoutes(..., Reflect: deps.SoulReflect)   # app.go
            └─ POST /api/queen/{id}/soul/reflect → 202 {"started": true}
                 └─ 后台 goroutine（routes_credentials_soul.go::handleReflectSoul）
                      resolveChatProvider(home, --model 或配置模型)     # 与新建会话同一取模口径
                      soulstore.Reflect{HomeRoot, QueensDir, QueenID, cfg, Completer,
                                        Bootstrap, MaxTokens=max(llm.max_tokens, 8192),
                                        Context: 10 分钟上限}
```

* 路由语义（上游 `routes_soul.py::handle_reflect`）：queen 不存在 404；无 soul 配置 409
  （`has no soul`）；等在审的 proposal 409（`Decide on the waiting proposal first`）；
  同一 queen 已在反思 409（`She is reflecting already`）；模型未装配 503；
  其余 202，实际反思在后台跑，结果看 `GET .../soul` 的 `proposal` / `last_run` / `last_result`。
* review 写 pending proposal；auto 保存后立即以 `her` 落地（bootstrap 永远留给人审）。
  两条入口共用同一个 `soulstore.Reflect`，证据校验、预算、日期窗口与每日反思完全一致。
* **无会话**：`queens/<id>/sessions` 下没有任何可读 session（含 `sources` / `include_titles` 过滤后为空）
  且不是 bootstrap 时，适配层直接返回 `soul: queen "..." has no chat sessions to read`，
  不写 `last_run`、不落 proposal、不调模型。无 soul 配置、模型构建失败、模型调用失败同样返回明确错误。
  错误经路由的 `log.Printf` 进 `GET /api/runtime-logs`，**不会**被记成一次成功。
  （每日调度路径保持上游口径：这种情况记 `nothing new`，见「未对齐项」。）
* 并发：路由的 `reflecting` 槽用一次加锁的 `beginReflecting` 原子认领（8 个并发请求只有 1 个 202、
  其余 409），并额外检查反射层的 per-queen gate，避免手动请求撞上正在跑的每日反思。
* 模型解析在每次调用时执行（与 soul probe、新建会话相同），配置改模型后无需重启。

## 6. 配置示例

```json
{
  "soul": {
    "queens": {
      "queen_x": {
        "enabled": true,
        "mode": "review",
        "hour": 4,
        "max_edits": 5,
        "bootstrap_edits": 30,
        "sections": [
          {"key": "now", "title": "Now", "limit": 300, "max_age_days": 14},
          {"key": "people", "title": "Me and them", "limit": 800, "evidence": 2}
        ],
        "sources": {"file": "channels/x/sessions.json", "keys": ["/group/"]},
        "include_titles": ["Night"],
        "exclude_titles": ["Planning"],
        "bootstrap_memories": ["me.md"]
      }
    }
  }
}
```

## 7. 测试

`internal/soul/reflection_test.go`（20 例）+ `internal/soul/reflection_transport_test.go`（1 例，
真实 HTTP 传输）+ `internal/cli/loader_soul_daily_test.go`（2 例）+ `internal/cli/loader_soul_reflect_test.go`
（4 例）+ `internal/server/routes_credentials_soul_test.go` 新增 2 例 + `internal/server/app_soul_reflect_test.go`
（2 例），共 31 例（普通测试），另有 1 例真实二进制 E2E：

| 覆盖 | 用例 |
| --- | --- |
| 上游 14 例对等 | 读聊天（发言人/快照去重/worker 流）、长聊天保留最新行、source file 过滤、证据逐字、两来源、max_edits + section 上限、重复、过期自动移除、auto 落地、无新内容不调模型、bootstrap 读记忆、due 判定、pending 阻止下一次 |
| 本 port 补充（Parity-exempt） | 标题 include/exclude、cursor 只读新消息、失败重试梯子与「失败不算 run」、启动补跑 + 定时 tick + 停止、`IsReflecting`、未配置 soul 的 queen 不被触碰、serve 接线与 max_tokens |
| 真实传输 | `TestReflectionThroughTheHTTPProvider`：httptest 的 OpenAI 兼容端点 → 走 `llm.NewHTTPProvider` 真实 HTTP 传输，校验请求里带 4 个工具与 soul 系统提示词、tool_call 回传被解析、auto 落地成 soul |
| 手动路由接线 | `TestAppSoulReflectHandsTheDepThrough`（旧 503 路径现在 202 + 拿到 queen 自己的 review config）、`TestAppSoulReflectWithoutDepAnswers503`、`TestSoulRoutesReflectWithoutModelAnswers503`、`TestSoulRoutesReflectConcurrentCallsReturn409`（8 并发只有 1 个 202） |
| 手动适配层（`e2e/soulgw` 当模型，真实 HTTP） | review 落 pending proposal + state（cursor/`1 changes`）、auto 落 soul + proposal `applied`、无会话报错、模型 5xx 报错且不写 `last_run`/proposal |
| 真实二进制 E2E（`-tags e2e`） | `TestSoulReflectionEndToEnd`：临时 HIVE_HOME + 会话事件 + `e2e/soulgw` 网关；review/auto/空会话/无 soul 四种 queen 一起跑，断言 202/409、proposal、`state.json` cursor、日志里的明确错误 |

真实模型联调仍未执行：本机没有可用的 LLM provider 配置/API key，也没有 ollama 等本地权重服务
（`~/.hive` 无 `configuration.json`，环境无 provider key）。E2E 的"模型"是确定性的本地
OpenAI 兼容网关（`e2e/soulgw`，按提示词回 tool_call），其余全是真的：真实 `hive serve` 二进制、
真实 HTTP 传输（请求里带 4 个反思工具与聊天正文）、真实 `events.jsonl`、真实 proposal/state 落盘。
局限照实标注：网关不是开放权重模型，模型质量类行为（乱编、拒答、超长输出）未被真实模型覆盖。

## 8. 未对齐项（已知差异）

1. `model: worker` 不启用独立的 worker 模型（Go 无进程级 worker LLM 构建入口），回落配置主模型；
   对结果无影响，只是与上游「优先 worker」的取模口径不同。
2. 手动反思的「无会话」报错：上游 `handle_reflect` 在无会话时由 `reflect` 记 `nothing new` 并返回
   `None`（HTTP 仍是 202、无错误可见）；Go 的手动适配层先检查可读 session，为空则返回明确错误
   （路由写日志），不写 `last_run`。每日调度路径保持上游口径。这是为了让「立即反思」的失败可见，
   不是假成功；见 §5.2。
3. 触发时机只有「启动补跑 + 10 分钟 tick」；未接「每会话结束检查」，与上游 scheduler 的粒度一致。
4. 上游 `scheduler(first_after_s=120)` 是启动后延迟 2 分钟首跑；Go 版 boot 后立即跑一次（要求
   「启动时补跑」），之后每 10 分钟。
