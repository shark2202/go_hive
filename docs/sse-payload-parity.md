# SSE payload 对照（Python 参考 vs Go 目标）

> 状态：**已实测**（2026-11-21，双跑探针 + 字段矩阵；api-ws-canonical 车道复跑并冻结帧级 golden）
> 探针：`go_hive/scripts/sse_payload_parity.py`（opt-in，本地 stub LLM，不触外网）
> 证据：`go_hive/docs/e2e-assets/sse-payload-parity/`（capture-*.json / diff.json / summary.txt）
> 帧级 golden：`go_hive/contracts/sse-frames/`（canonicalizer：`go_hive/scripts/sse_frames_canonical.py --check`；
> 口径与断言见 `docs/api-ws-golden.md`）
> 关联：外观矩阵 `sse` 维度、`surface-tracking-matrix.html`、`alignment-dimensions.html`

## 1. 范围与方法

`api-contract-diff`（14 端点）只比较单个响应体的形状；`session_events` 捕获也只覆盖
「订阅即重放」的那几帧。本页回答另一个问题：**同一场景下，两侧客户端实际收到的 SSE
帧序列（类型、顺序、data 字段、replay 语义、judge 行为）是否一致。**

探针驱动同一剧本，两侧各起一个临时 `HIVE_HOME` + 同一个本地 OpenAI 兼容 stub：

1. `POST /api/sessions` 创建 queen 会话，等开场回合（opening turn）稳定；
2. 订阅 `GET /api/sessions/{id}/events`（数据帧无 `event:` 名，与上游一致）；
3. 发一条普通消息 → 文本回合；
4. 发一条 `ASK_ME …` 消息 → stub 返回一次 `ask_user` 工具调用 → 问题挂起；
5. 回一条答案 → 续跑并停在 `turn_done`；
6. 断开后重连一次，捕获 replay 序列；
7. 读 `/events/history` 作为完整事件日志（含开场块，SSE 不会重放它；`judge_verdict`
   也只出现在 history，与参考一致）；**history 在 DELETE 之前抓取**，停机后的日志另存
   `history_after_stop`（仅证据），避免关停尾巴污染契约日志；
8. `DELETE /api/sessions/{id}` 停会话（捕获停止尾巴，不参与逐字段矩阵）。

归一化：`seq` / `timestamp` / `execution_id` / `tool_use_id` / `session_id` /
`correlation_id` 等易变值替换为占位符，**键存在性与类型保留**；`content`、`prompt`、
`model`、`max_iterations` 等语义值保留用于值级对照。`diff.json` 的 `judge` 段把两侧
history 里的 `judge_verdict`（action / feedback / judge_type / iteration）逐条比对，
并记录它是否出现在 live/replay。帧级稳定化（含 `*_at` 时间键、时长、数字字面量保留）
与 golden 断言由 `go_hive/scripts/sse_frames_canonical.py` 复现，见 `docs/api-ws-golden.md`。

## 2. 逐类型字段矩阵（本车道修复后）

完整机器可读版本见 `diff.json` 的 `per_type`；下表的 `missing/extra/type` 均指
**reference → target 方向**。`live/replay/history` 三路都做键集合与类型比较。

| 事件类型 | live 帧数 ref/go | 键缺失 | 键多余 | 类型差 | 结论 |
| --- | --- | --- | --- | --- | --- |
| `session_snapshot` | 1/1 | — | — | — | 一致（14 个键，含 `snapshot_seq`/`activity`/`current_tool_calls`） |
| `client_output_delta` | 3/3 | — | — | — | 键一致（content/snapshot/iteration/inner_turn）；iteration 值见 §4.6 |
| `llm_turn_complete` | 4/4 | — | — | — | 键一致；`cost_usd` 数字表示已修（两侧 `0.0`），其余值级差异见 §4.6 |
| `client_input_requested` | 4/4 | — | — | — | 键一致（`park_reason`；带问题时为 `questions`）；发射时机已对齐（§3） |
| `loop_state_changed` | 6/6 | — | — | — | 键一致（activity/park_reason/interrupt_cause/questions） |
| `reminder_injected` | 0/0（仅 history 1/1） | — | — | — | 键一致（source/detail/meta） |
| `node_loop_started` | 0/3 | — | `max_iterations`（target 独有帧） | — | 见 §4.1、§4.6 |
| `node_loop_iteration` | 3/3 | — | — | — | 键一致（iteration+phase） |
| `node_loop_completed` | 0/3 | — | `iterations`（target 独有帧） | — | 见 §4.1、§4.6 |
| `execution_started` | 0/3 | — | `input`（target 独有帧） | — | 见 §4.1 |
| `execution_completed` | 0/3 | — | `output`（target 独有帧） | — | 见 §4.1 |
| `client_input_committed` | 3/3 | — | — | — | 键 `seq`，correlation 与 received 对齐 |
| `context_usage_updated` | 5/5 | — | — | — | 键/类型一致；**post_tool_call/post_tool_results 已补齐**（本车道，见 §3/§4.5） |
| `session_title_updated` | 1/1 | — | — | — | 键 `session_id`/`title`；发射为异步任务，位置非契约（§4.6） |
| `tool_call_started` / `_completed` | 1/1 | — | — | — | 键一致（含 `tool_index`） |
| `judge_verdict` | 0/0（history 3/3） | — | — | — | **已对齐**；history-only，两侧 live/replay 均不可见；见 §4.3 |

修复历史（前两轮车道）供对照：target live 类型集合缺 `client_input_committed`、
`context_usage_updated`、`session_title_updated`；target live 每回合多
`execution_started`/`execution_completed`/`node_loop_started`/`node_loop_completed`；
`node_loop_iteration` 每回合帧缺 `phase`；`context_usage_updated` 只在开场块出现一次；
`ask_user` 回合多一次模型调用；replay 集合缺三类可见聊天帧、多 execution 信封帧。
`judge_verdict` 参考独有与 `llm_turn_complete`/`client_input_requested` 发射时机是本
车道（sse-timing-judge）修的残余。

## 3. 本车道修复（发射时机 + 隐式 judge）

| 问题 | 证据 | 修复 | 代码 |
| --- | --- | --- | --- |
| `client_input_requested` 由工具处理器提前发射（夹在 tool_started 与 tool_completed 之间） | 参考在 `_await_user_input` 里、工具批与 `llm_turn_complete` 之后发；目标在 ask_user 工具执行时发 | 工具处理器只记 `pendingQuestions` 并落盘；停车边界（`executionCompleted`）统一一次性发出，ask_user park 带上原 `questions` | `chat_runtime.go`（`executeAskUser` 去掉提前发射）、`chat_turn_emitter.go`（`executionCompleted(..., questions)`） |
| `llm_turn_complete` 非流式「完成即发」，落在工具帧之前 | 参考在 `_run_turn_loop` 返回后（工具批之后、park 之前）发 | 文本回合在 `settleTextOnlyTurn` 开头发；工具回合在 `observeToolBatch` 里、`tool_call_completed` 之后发 | `loop.go`、`worker_stall.go` |
| 同一改动会把 `message_count` 变成「回合结束后的会话大小」（+assistant/+tool 消息） | 复跑探针发现 target `2/4/7/9` vs 参考 `1/3/5/8` | loop 新增 `RequestMessageRecorder` 可选 seam：请求发出前记录 `len(messages)`，emitter 用它填充诊断字段 | `loop.go::requestCompletion`、`types.go`、`chat_turn_emitter.go` |
| `judge_verdict` 参考独有（隐式 judge，history 3/0） | 参考 queen 的永久 loop 在 park 解除后跑 6i judge；`skip_judge=True` 短路仍发布 RETRY/implicit 事件 | 会话级隐式 verdict 账本：回合结束时记账，下一回合开始（`client_input_received`+EXECUTING 之后、`client_input_committed` 之前）发布上一回合的 `judge_verdict{RETRY,"",implicit,iteration}`；带 criteria 的会话跳过（其 loop judge 自己发 verdict） | `chat_implicit_judge.go`、`chat_runtime.go` |
| `context_usage_updated` 只有每迭代 `iteration_start`（参考另有 `post_tool_call` / `post_tool_results`） | 参考工具批内每个结果落库后发一次 `post_tool_call`、批尾发 `post_tool_results`；本复跑 live 5 vs 3、history 5 vs 3 | `storeToolBatch` 每次 `AddToolResult` 后按调用序发 `tool_call_completed → context_usage_updated(post_tool_call)`，批尾发 `post_tool_results`，再发 `llm_turn_complete`；`ToolBatchContextObserver` 可选 seam | `agentloop/loop.go`、`types.go`、`chat_turn_emitter.go` |
| `cost_usd` 数字表示 `0` vs `0.0`（数值等价、字面不同） | 复跑类型差只剩这一项 | `eventbus.EmitLLMTurnComplete` 用 `pythonFloat` 按 Python 口径渲染 float（整数补 `.0`），`credits` 同理；回归测试断言 JSON 字节 | `internal/eventbus/emit.go`、`event_bus_test.go` |

发射时机修复前/后（探针剧本的 ask_user 回合，Go live）：

```
修复前 Go:  llm_turn_complete(iter=1, stop=tool_calls) → tool_call_started(ask_user)
            → client_input_requested{park_reason=ask_user, questions} → tool_call_completed(ask_user)
            → node_loop_completed → loop_state awaiting_user → execution_completed
修复后 Go:  tool_call_started(ask_user) → tool_call_completed(ask_user)
            → context_usage_updated(post_tool_call) → context_usage_updated(post_tool_results)
            → llm_turn_complete(iter=1, stop=tool_calls)
            → node_loop_completed → client_input_requested{ask_user, questions}
            → loop_state awaiting_user → execution_completed
参考（去 envelope）: tool_call_started → tool_call_completed
            → context_usage_updated(post_tool_call) → context_usage_updated(post_tool_results)
            → llm_turn_complete → client_input_requested{ask_user, questions} → loop_state awaiting_user
```

隐式 judge（探针剧本的 history，两侧现在逐条一致）：

```
参考 & 目标: 回合 1 开始: client_input_received → loop_state executing → judge_verdict{RETRY,"",implicit,0}
                            → client_input_committed → node_loop_iteration(1) …
             回合 2 开始: … judge_verdict{…,1} …；回合 3 开始: … judge_verdict{…,2} …
             末回合（回答回合，停止前无下一条消息）不发 verdict —— 两侧一致
修复前 Go:  history 中 0 条 judge_verdict（同一位置缺帧）
```

以上均有回归测试钉住：`internal/server/sse_payload_parity_test.go`
（`TestAskUserRequestIsAnnouncedAtTheTurnBoundary`、
`TestImplicitJudgeVerdictFollowsThePreviousTurn`，后者同时钉住
`llm_turn_complete.message_count` 为请求时刻的大小；`TestToolBatchPublishesPostToolContextReadouts`
钉住 post_tool_call/post_tool_results 的触发点与 message_count 7），`// Parity:` 注解指向
`agent_loop.py::_run_turn_loop`、`_await_user_input`、`execute`（6i）、
`judge_pipeline.py::judge_turn`、`queen_orchestrator.py`（skip_judge spec）、
`event_publishing.py::publish_context_usage`。

## 4. 差异清单（保留项与已修项）

### 4.1 Go 每回合 execution / node-loop 信封（有意）

Go 的聊天回合是「每回合一个 bounded loop」，因此每回合发
`execution_started → loop_state_changed(executing) → node_loop_started … node_loop_completed →
client_input_requested → loop_state_changed(awaiting_user) → execution_completed`。
Python 的 queen 是**永久 loop**：整个会话只在开场写一次 node_loop_started/iteration，
用户回合不发 execution 信封。

证据：target live 47 帧 vs reference 35 帧 = 每回合 +12 帧信封（execution_started/completed ×3、
node_loop_started/completed ×3）在剔除后两侧逐帧一致（帧级 golden 断言 `live-core-equal`，
`diff.json`：34/34）。`diff.json` 中 `execution_started`/`execution_completed`/`node_loop_completed`
的 extra 键（`input`/`output`/`iterations`）只出现在 target-only 帧上。**结论：作为有意差异保留，
不硬改**；快照（is_executing）与前端 spinner 依赖这组信封，拆掉会破坏 Go 的运行时可观测性。

### 4.2 `max_iterations` 值：999999 vs 50（有意）

参考永久 loop 报 `999999`，Go 报每回合 loop 的实际上限（默认 50）。字段存在性与类型一致，
值不同是 4.1 的直接推论。

### 4.3 `judge_verdict`（本车道已对齐）

参考 queen（`queen_orchestrator.py` 的 `skip_judge=True` spec）每个回合在 park 解除后跑
一次 6i judge；短路 verdict 是恒定的 `RETRY` + 空 feedback，但仍发布
`judge_verdict{judge_type: implicit}`，且只在 history（默认 live 过滤与 replay 集合都不含
它）。Go 现在用会话级账本在**下一回合开始**发布上一回合的 verdict，payload 与位置和参考
逐条一致（`diff.json.judge.same_verdicts=true`；两侧 live/replay 均不可见）。

保留的粒度边界（探针剧本之外才可见，属实现结构差异）：
- 参考每**loop iteration**（多 inner-turn 回合 = 多次 LLM 调用）各发一次；Go 每**回合**
  发一次（per-turn loop 的迭代号也按回合计）。
- 带 `success_criteria` 的会话走 Go 的 criteria judge（`judge_type=custom`，由 loop 发布），
  本隐式账本跳过，避免重复帧；探针剧本（无 criteria）不受影响。
- 进程重启后账本重置：重启会话的第一条新消息不会再补发重启前那一回合的 verdict。

### 4.4 `ask_user` 回合的提前返回与发射时机（已修）

参考在 `ask_user` 工具返回后**立即结束当前回合**（`user_input_requested` 提前 return），
并在 `_await_user_input` 里发 `client_input_requested{questions}`。Go 的回合提前返回在
前一轮车道修完（`batchStopsTurn`）；本轮把 `client_input_requested` 从工具处理器移到
停车边界（§3 的第一条），两侧去 envelope 后的顺序现在一致。

### 4.5 `context_usage_updated` 触发粒度（本车道已对齐）

参考观察到的 3 种 trigger：`iteration_start`、`post_tool_call`（每个工具结果后）、
`post_tool_results`（工具批结束后）；Go 此前只有 `iteration_start`。本车道补齐两类工具批
触发点（§3 第 5 行、§4.1 附注），逐帧顺序与参考一致（帧级 golden `live-core-equal` /
`history-core-equal`）；剩余差异只有读数数值本身（提示词/工具定义字符数）与开场
`message_count` 的 1 vs 0，均已归类为值级例外（§4.6）。

### 4.6 值级/有意差异（不追平，明确归类）

以下差异是**值级（value-level）或实现结构性的有意差异**，探针逐项复测但不作为对齐目标；
本车道不为凑数硬改。机器可读清单见 `go_hive/contracts/sse-frames/diff.json` 的 `exceptions`
与 `go_hive/contracts/api-responses/value-golden.json`（REST 侧同源路径）：

| 路径 | 参考 | Go | 归类 |
| --- | --- | --- | --- |
| `llm_turn_complete.system_prefix_sha` | `692b774c98c7` | `885285cb1ffe` | **值级**：两侧系统提示词文本不同（prompt parity 车道），hash 只是文本指纹 |
| `node_loop_started.max_iterations` | `999999` | `50` | **有意**：永久 loop vs 每回合 loop（§4.2） |
| `node_loop_completed.iterations` | （参考无此帧） | `1` | **有意**：Go-only 每回合信封的 `iterations` 字段（§4.1） |
| `llm_turn_complete.cost_usd` | `0.0`（float） | `0.0`（float，已修） | **已修**：本车道按 Python 口径渲染 float（§3 最后一行），字面差异消失 |
| `llm_turn_complete.model` | `openai/sse-parity-probe-model` | `sse-parity-probe-model` | **值级**：provider 前缀由 litellm 归一化；配置层差异 |
| `llm_turn_complete.iteration` / `node_loop_iteration.iteration` / `client_output_delta.iteration` | 会话累计 0,1,2,3 | 每回合 0,1 | **有意**：永久 loop vs 每回合 loop（§4.1） |
| `context_usage_updated.*` 值 | 不同字符数/条数 | 不同 | **值级**：系统提示词与消息顺序不同（prompt parity）；开场 `message_count` 1 vs 0 同源，触发点已对齐（§4.5） |
| `session_title_updated` 位置 | 异步任务，落在 llm_turn_complete 之后 | 同（异步） | **非契约**：两侧都由后台任务发标题，位置不确定；帧级 golden 按多重集比较、位置仅记录 |

`llm_turn_complete.message_count` 曾因发射时机后移而漂移（`2/4/7/9` vs `1/3/5/8`），
前序车道用请求时刻快照修回 `1/3/5/8`，本车道复跑保持并在帧级 golden 里逐帧比对（§3/§5）。

## 5. Replay / history 语义

- **首帧**：两侧都是合成的 `session_snapshot`（不落盘），随后才重放缓冲事件。
- **顺序**：两侧 replay 序列都是 history 的子序列（逐帧 type+data 匹配、允许跳过被抑制项），
  即「按时间正序重放」。`diff.json`：
  `reference_replay_order_matches_history=true`、`target_replay_order_matches_history=true`。
- **集合**：剔除 Go-only execution 信封后，replay 多重集**完全一致**：`diff.json` 的
  `same_replayed_multiset_without_go_envelope=true`、`multiset_delta_without_envelope` 为空。
- **已解决问题抑制**：两侧都跳过已被 `client_input_received` 回答的
  `client_input_requested`（参考 `collect_resolved_request_seqs` / Go
  `CollectResolvedRequestSeqs`），本次场景验证到 replay 只重放最后一条未回答问题。
- **history**：包含开场 5 条块（identity/reminder/node_loop_started/iteration/context usage），
  两侧开场块逐字段一致（`session_start_events.go` 既有实现；`max_iterations` 值见 4.2）。
  本复跑 target history 55 条 vs reference 43 条（顺序在 DELETE 之前抓取），差额全部是
  Go-only 每回合信封（+12）；剔除后两侧 42/42 帧逐帧一致（帧级 golden `history-core-equal`）。
  停机后的日志另存 `history_after_stop`（reference 44 / target 55），不参与比较。

## 6. 复现

```bash
# 默认写证据到 go_hive/docs/e2e-assets/sse-payload-parity/
uv run python go_hive/scripts/sse_payload_parity.py
# 复用已构建的 Go 二进制 / 保留临时 HOME / 指定输出
uv run python go_hive/scripts/sse_payload_parity.py --no-build --keep --workdir /tmp/sse-parity
# 帧级 golden：重建（自动删旧二进制再双跑）与重放校验（不起服务）
uv run python go_hive/scripts/sse_frames_canonical.py --refresh
uv run python go_hive/scripts/sse_frames_canonical.py --check
```

探针只监听 127.0.0.1、只调用本地 stub，不访问任何外部服务；**不纳入 `make ci`**，按需手动跑。
Go 侧二进制构建到 `go_hive/go-hive/dist/hive-sse-parity`（git 忽略）。注意：脚本复用已存在的
二进制，改动 Go 代码后先删 `dist/hive-sse-parity` 再跑，否则对照的是旧版本。

## 7. 门禁

本车道按任务范围只跑受影响门禁（**不跑全量 `make ci`**），全部退出码 0：

- `gofmt -l internal/` 空、`go vet ./internal/{server,agentloop,eventbus}` 0、`go build ./...` 通过；
- `go test ./internal/server ./internal/agentloop ./internal/eventbus` 全绿（新增
  `TestToolBatchPublishesPostToolContextReadouts`、`TestPythonFloatKeepsPythonSpelling`）；
- parity 注解 **5261/5261**（基线 5259 + 本车道新增 2）；parity 引用 **5637/5637**
  （test refs 4188 + prod refs 1449，missing 0）；
- 外观矩阵 `gen_appearance_master_matrix.py --check` 通过（sse 维度升为 measured_aligned +
  frozen_contract，api-routes 值级账本入证）；
- `sse_frames_canonical.py --check`、`api_response_values.py --check`、
  `check_api_proto.py`（42 消息 / 2 枚举 / 1322 字段，errors=0 warnings=0）全部 PASS。

双跑探针与 golden 不在 `make ci` 内（opt-in、本地 stub、不触外网）；帧级 golden 的断言与
例外清单见 `docs/api-ws-golden.md`。
