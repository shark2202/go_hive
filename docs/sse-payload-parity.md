# SSE payload 对照（Python 参考 vs Go 目标）

> 状态：**已实测**（2026-11-21，双跑探针 + 字段矩阵）
> 探针：`go_hive/scripts/sse_payload_parity.py`（opt-in，本地 stub LLM，不触外网）
> 证据：`go_hive/docs/e2e-assets/sse-payload-parity/`（capture-*.json / diff.json / summary.txt）
> 关联：外观矩阵 `sse` 维度、`surface-tracking-matrix.html`、`alignment-dimensions.html`

## 1. 范围与方法

`api-contract-diff`（14 端点）只比较单个响应体的形状；`session_events` 捕获也只覆盖
「订阅即重放」的那几帧。本页回答另一个问题：**同一场景下，两侧客户端实际收到的 SSE
帧序列（类型、顺序、data 字段、replay 语义）是否一致。**

探针驱动同一剧本，两侧各起一个临时 `HIVE_HOME` + 同一个本地 OpenAI 兼容 stub：

1. `POST /api/sessions` 创建 queen 会话，等开场回合（opening turn）稳定；
2. 订阅 `GET /api/sessions/{id}/events`（数据帧无 `event:` 名，与上游一致）；
3. 发一条普通消息 → 文本回合；
4. 发一条 `ASK_ME …` 消息 → stub 返回一次 `ask_user` 工具调用 → 问题挂起；
5. 回一条答案 → 续跑并停在 `turn_done`；
6. 断开后重连一次，捕获 replay 序列；
7. `DELETE /api/sessions/{id}` 停会话（捕获停止尾巴，不参与逐字段矩阵）；
8. 读 `/events/history` 作为完整事件日志（含开场块，SSE 不会重放它）。

归一化：`seq` / `timestamp` / `execution_id` / `tool_use_id` / `session_id` /
`correlation_id` 等易变值替换为占位符，**键存在性与类型保留**；`content`、`prompt`、
`model`、`max_iterations` 等语义值保留用于值级对照。

## 2. 逐类型字段矩阵（修复后）

完整机器可读版本见 `diff.json` 的 `per_type`；下表的 `missing/extra/type` 均指
**reference → target 方向**。`live/replay/history` 三路都做键集合与类型比较。

| 事件类型 | live 帧数 ref/go | 键缺失 | 键多余 | 类型差 | 结论 |
| --- | --- | --- | --- | --- | --- |
| `session_snapshot` | 1/1 | — | — | — | 一致（14 个键，含 `snapshot_seq`/`activity`/`current_tool_calls`） |
| `client_output_delta` | 3/4 | — | — | — | 键一致（content/snapshot/iteration/inner_turn）；iteration 值见 §4 |
| `llm_turn_complete` | 4/5 | — | — | `cost_usd` float vs int | 键一致；`cost_usd` 为 JSON 数字表示差（见 §4） |
| `client_input_requested` | 4/4 | — | — | — | 键一致（`park_reason`；带问题时为 `questions`） |
| `loop_state_changed` | 6/6 | — | — | — | 键一致（activity/park_reason/interrupt_cause/questions） |
| `reminder_injected` | 0/0（仅 history 1/1） | — | — | — | 键一致（source/detail/meta） |
| `node_loop_started` | 0/3 | — | `max_iterations`（target 独有帧） | — | 见 §4「Go 每回合 envelope」 |
| `node_loop_iteration` | 3/4 | — | — | — | 键一致（iteration+phase，**本轮补齐 phase**） |
| `node_loop_completed` | 0/3 | — | `iterations`（target 独有帧） | — | 见 §4「Go 每回合 envelope」 |
| `execution_started` | 0/3 | — | `input`（target 独有帧） | — | 见 §4「Go 每回合 envelope」 |
| `execution_completed` | 0/3 | — | `output`（target 独有帧） | — | 见 §4「Go 每回合 envelope」 |
| `client_input_committed` | 3/3 | — | — | — | **本轮补齐**；键 `seq`，correlation 与 received 对齐 |
| `context_usage_updated` | 5/4 | — | — | — | 键/类型一致；**本轮补齐每迭代发射**；触发粒度见 §4 |
| `session_title_updated` | 1/1 | — | — | — | **本轮接线**；键 `session_id`/`title` |
| `tool_call_started` / `_completed` | 1/1 | — | — | — | 键一致（含 `tool_index`） |
| `judge_verdict` | 0/0（仅 history 3/0） | `action`/`feedback`/`iteration`/`judge_type` | — | — | 参考独有（隐式 judge）；见 §4 |

修复前（本轮首跑）的差异供对照：

- target live 类型集合 **缺** `client_input_committed`、`context_usage_updated`、`session_title_updated`；
- target live 每回合 **多** `execution_started`/`execution_completed`/`node_loop_started`/`node_loop_completed`；
- `node_loop_iteration` 每回合帧缺 `phase`；`context_usage_updated` 只在开场块出现一次；
- replay 集合因此同样缺三类可见聊天帧、多 execution 信封帧。

## 3. 本轮修复（低风险、证据明确）

| 问题 | 证据 | 修复 | 代码 |
| --- | --- | --- | --- |
| 每回合 `node_loop_iteration` 缺 `phase` | 参考 live/history 每个 iteration 都带 `phase`；Go 仅开场块带 | emitter 在每次迭代写入 `phase`（`SessionPhaseLabel`，independent/colony） | `internal/server/chat_turn_emitter.go`、`session_start_events.go`、`cli/loader*.go` |
| 缺 `client_input_committed` | 参考在每条用户消息落地时发一次 `{seq}`，replay 子集也包含它；Go 只定义了 emitter 从未调用 | 用户消息写入会话后发 committed，`seq` 取会话消息 seq，correlation 与 `client_input_received` 相同 | `internal/server/chat_runtime.go`、`chat_routes.go` |
| 每迭代缺 `context_usage_updated` | 参考在每次 iteration start 发一次；Go 只在开场块发一次 | `IterationStarted` 之后按同一 payload 形状发一次（开场首迭代仍由开场块独占） | `chat_turn_emitter.go`、`chat_runtime.go`、`session_start_events.go` |
| 缺 `session_title_updated` | 参考首回合后自动命名并发事件；Go 的 `TitleTrigger` 有实现有测试但**从未接线** | 会话创建/预加载两条路径订阅 title trigger，停止时释放 | `internal/cli/loader.go`、`loader_preloaded_session.go`、`session.go`、`session_titles.go` |
| live 默认事件过滤缺失 | 参考 `handle_events` 无 `?types=` 时回退 `DEFAULT_EVENT_TYPES`（49 类）；Go 无过滤直接推所有事件；且参考 replay 与订阅过滤集求交（`_REPLAY_TYPES & event_type_values`），Go replay 不看过滤 | Go 订阅按上游 49 类白名单过滤；replay 同样与订阅过滤求交；未知类型名被丢弃，全无效时回退默认集（对齐 `_parse_event_types`） | `internal/server/chat_routes.go` |
| replay 集合多 `execution_failed` | 参考 `_REPLAY_TYPES` 只含 started/completed | 从 Go replay 集合移除 `execution_failed` | `internal/server/chat_routes.go` |

以上均有回归测试钉住（`internal/server/sse_payload_parity_test.go`，`// Parity:` 注解指向
`routes_events.py::DEFAULT_EVENT_TYPES`、`_REPLAY_TYPES`、`agent_loop.py::_publish_iteration`、
`event_publishing.py::publish_context_usage`、`agent_loop.py::_drain_injection_queue`）。
两个既有顺序断言测试按上游顺序更新（committed 在 node_loop_started 之后、context usage 在
iteration 之后）。

## 4. 保留差异（有意 / 未硬改）

### 4.1 Go 每回合 execution / node-loop 信封（有意）

Go 的聊天回合是「每回合一个 bounded loop」，因此每回合发
`execution_started → loop_state_changed(executing) → node_loop_started … node_loop_completed →
client_input_requested → loop_state_changed(awaiting_user) → execution_completed`。
Python 的 queen 是**永久 loop**：整个会话只在开场写一次 node_loop_started/iteration，
用户回合不发 execution 信封。

证据：target live 49 帧 vs reference 35 帧 = +12 帧信封（execution_started/completed ×3、
node_loop_started/completed ×3）+3 帧（4.4 的多一次迭代：node_loop_iteration、
client_output_delta、llm_turn_complete 各 +1）−1 帧（4.5 的 context usage 触发粒度）。
`diff.json` 中 `execution_started`/`execution_completed`/`node_loop_completed` 的 extra
键只出现在 target-only 帧上。**结论：作为有意差异保留，不硬改**；快照（is_executing）与
前端 spinner 依赖这组信封，拆掉会破坏 Go 的运行时可观测性。

### 4.2 `max_iterations` 值：999999 vs 50（有意）

参考永久 loop 报 `999999`，Go 报每回合 loop 的实际上限（默认 50）。字段存在性与类型一致，
值不同是 4.1 的直接推论。

### 4.3 `judge_verdict`（参考独有）

Python 的 queen loop 每个回合跑一次「隐式 judge」（`judge_type: implicit`，本场景恒
`RETRY` + 空 feedback），Go 的 judge 只在配置了 success criteria 时武装（chat 会话默认无
criteria）。这是 judge 机制的差异，不是 SSE 层缺失；作为已知差异保留。

### 4.4 `ask_user` 回合：Go 多一次模型调用（已发现，未硬改）

参考在 `ask_user` 工具返回后**立即结束当前回合**（tool_started → tool_completed →
post_tool_call/post_tool_results context usage → llm_turn_complete → client_input_requested
+ questions → loop_state awaiting_user）。Go 在该回合继续跑一次 LLM 调用，然后才挂起：

```
参考: tool_call_completed → (无输出) → llm_turn_complete → client_input_requested{questions}
Go:   tool_call_completed → node_loop_iteration → client_output_delta → llm_turn_complete
      → node_loop_completed → client_input_requested{questions}
```

影响：Go 多一帧 `client_output_delta` + 一帧 `llm_turn_complete`（replay 计数 3/4、4/5 的
唯一来源），并在问题挂起时可能多出一条助手文本气泡。修复需要改 loop 的 ask_user 提前返回
语义（Python 的 `user_input_requested` 提前 return 是 loop 级逻辑，不是 payload 字段），
超出「字段/顺序补齐」的低风险范围，**记录为下一车道事项**。

### 4.5 `context_usage_updated` 的触发粒度

参考有 4 种 trigger：`iteration_start`、`post_tool_call`（每个工具结果后）、
`post_tool_results`（工具批结束后）、`post_compaction`。Go 目前只有 `iteration_start`
（每迭代一次）。键/类型已一致，缺的是事件发射点；补齐需要 loop 级工具批钩子，随 4.4
一并处理。

### 4.6 值级差异（非形状）

| 路径 | 参考 | Go | 说明 |
| --- | --- | --- | --- |
| `llm_turn_complete.model` | `openai/sse-parity-probe-model` | `sse-parity-probe-model` | provider 前缀由 litellm 归一化；配置层差异 |
| `llm_turn_complete.cost_usd` | `0.0`（float） | `0`（int） | JSON 数字表示差，数值等价；`encoding/json` 对 float64(0) 打 `0` |
| `llm_turn_complete.system_prefix_sha` | `692b774c98c7` | `885285cb1ffe` | 两侧系统提示词文本不同（prompt parity 车道） |
| `node_loop_iteration.iteration` | 会话累计 0,1,2,3 | 每回合 0,1,2 | 永久 loop vs 每回合 loop（4.1 推论） |
| `client_output_delta.iteration` | 同上 | 同上 | 同上 |
| `context_usage_updated.*` | conv/system/tool 字符数 | 不同 | 系统提示词与消息顺序不同（prompt parity） |
| `llm_turn_complete.message_count` | 1,3,5,8 | 1,3,5,7,9 | ask_user 额外迭代 + 会话消息形状（4.4） |

## 5. Replay / history 语义

- **首帧**：两侧都是合成的 `session_snapshot`（不落盘），随后才重放缓冲事件。
- **顺序**：两侧 replay 序列都是 history 的子序列（逐帧 type+data 匹配、允许跳过被抑制项），
  即「按时间正序重放」。`diff.json`：
  `reference_replay_order_matches_history=true`、`target_replay_order_matches_history=true`。
- **集合**：在剔除 Go-only execution 信封后，唯一计数差是 4.4 的
  `client_output_delta` 3→4、`llm_turn_complete` 4→5。
- **已解决问题抑制**：两侧都跳过已被 `client_input_received` 回答的
  `client_input_requested`（参考 `collect_resolved_request_seqs` / Go
  `CollectResolvedRequestSeqs`），本次场景验证到 replay 只重放最后一条未回答问题。
- **history**：包含开场 5 条块（identity/reminder/node_loop_started/iteration/context usage），
  两侧开场块逐字段一致（`session_start_events.go` 既有实现；`max_iterations` 值见 4.2）。

## 6. 复现

```bash
# 默认写证据到 go_hive/docs/e2e-assets/sse-payload-parity/
uv run python go_hive/scripts/sse_payload_parity.py
# 复用已构建的 Go 二进制 / 保留临时 HOME / 指定输出
uv run python go_hive/scripts/sse_payload_parity.py --no-build --keep --workdir /tmp/sse-parity
```

探针只监听 127.0.0.1、只调用本地 stub，不访问任何外部服务；**不纳入 `make ci`**，按需手动跑。
Go 侧二进制构建到 `go_hive/go-hive/dist/hive-sse-parity`（git 忽略）。

## 7. 门禁

本车道改动后 `cd go_hive/go-hive && make ci`（fmt-check / vet / test / cross / parity /
parity-targets / wiring-check）**七项全部退出码 0**：测试 5127（基线 5123 + 本车道新增 4）、
parity 注解 5127/5127、parity 引用 5465/5465、wiring 12 文件 47 项全命中。
新增/更新的测试均带 `// Parity:` 注解；双跑探针不在 `make ci` 内（opt-in、本地 stub、不触外网）。
