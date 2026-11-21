# API / SSE 帧级 canonical golden

> 状态：**已实测**（2026-11-21，api-ws-canonical 车道；双跑探测 + 帧级 golden + REST 值级账本，全部 PASS）
> 产物：
> - `go_hive/contracts/sse-frames/`（reference/target 帧级 golden + `diff.json` + `index.json` + `summary.txt`）
> - `go_hive/contracts/api-responses/value-golden.json`（14 成功端点 + 3 失败路径的值级差异账本）
> 生成 / 校验：
> ```bash
> uv run python go_hive/scripts/sse_frames_canonical.py            # 写入帧级 golden
> uv run python go_hive/scripts/sse_frames_canonical.py --check    # 重放校验（不起服务、不触网）
> uv run python go_hive/scripts/sse_frames_canonical.py --refresh  # 双跑探针 + 重建（先删旧 Go 二进制）
> uv run python go_hive/scripts/api_response_values.py            # 写入 REST 值级账本
> uv run python go_hive/scripts/api_response_values.py --check     # 重放校验
> ```
> 关联：`docs/sse-payload-parity.md`（探针剧本 + 逐类型字段矩阵）、
> `docs/api-contract-diff.md`（形状 / 语义差）、外观登记册 `sse` 与 `api-routes` 维度。

## 1. 口径与覆盖

此前 API 侧只比较「单个响应体的形状」（14 端点 0 差异），SSE 侧是脚本报告（无帧级 golden）。
本页冻结的是**帧级/值级**契约：同一剧本下，两侧客户端收到的每一帧、以及 14 个端点的
canonical 响应值，逐条比对后写入 golden；差异只有三类——已修、已声明例外、未声明（未声明即 FAIL）。

| 覆盖 | 数量 | 说明 |
| --- | --- | --- |
| SSE canonical 帧 | reference 113 / target 148 | 同一剧本：创建 → 开场回合 → 普通消息 → ask_user → 回答 → 重连 replay → DELETE |
| SSE 比较帧（去文档化变换后） | live 34/34、replay 17/17、history 42/42、stop 18/17 | 变换：丢弃 Go-only 每回合信封 36 帧 + 异步 title 位置不参与顺序 |
| REST 成功端点 | 14/14 PASS | `contracts/api-responses/{reference,target}/` canonical 载荷 |
| REST 失败路径 | 3/3 PASS（400/404/409） | 状态码 + `error` 字段逐字对照（见 §6） |
| REST 值路径 | 4910 条 | 0 未声明差异；37 条已声明差异（§5） |

## 2. SSE canonicalizer（`go_hive/scripts/sse_frames_canonical.py`）

探针 `sse_payload_parity.py` 产出 `docs/e2e-assets/sse-payload-parity/capture-{reference,target}.json`；
canonicalizer 把每一帧稳定化为 golden 帧：

- **占位**：`seq` / `timestamp` / `execution_id` / `correlation_id` / `session_id` / `tool_use_id` /
  `run_id` / 所有 `*_at` 时间键 / 时长（`*_ms`）等易变值 → `<键名>`，**存在性与类型保留**；
- **保留**：事件类型与顺序、字段集合、信封字段、枚举值（`stop_reason`/`park_reason`/`activity`/`trigger`/
  `judge_type`…）、消息内容；数字按字面保留（`0.0` 与 `0` 可区分，防止表示回退）；
- 帧形如：`{"stream","index","event","type","envelope":{…},"data":{…}}`（`sort_keys` 落盘）。

比较前施加**文档化变换**（记录在 `diff.json.transforms` 与 `index.json.transforms`）：

1. **Go-only 每回合 loop 信封**：丢弃 target 侧出现在首个 `client_input_received` 之后的
   `execution_started/_completed/_failed/_paused`、`node_loop_started/_completed` 帧；
   会话开始块（在首个 client input 之前）两侧都保留。这就是把
   「剔除 Go-only execution 信封后一致」固化成 golden 断言的规则。
2. **`session_title_updated` 异步位置**：双侧都是后台任务，位置不属契约；按多重集比较
   （类型 + 载荷），位置仅记录（本次 reference 落在 live#11/history#17，target 落在 live#17/history#23）。
3. **已声明例外**（§5）逐路径掩码为 `<exception>`，掩码后若仍不相等即未声明差异。

`--check` 从已提交 capture 在内存中重放整个 canonicalize + 比较流程：逐字节比对
`reference/target/*.json`、`diff.json`、`index.json`（忽略 `generated_at`），并重跑全部断言；
**不需要起服务、不触外网**。`--refresh` 先删除 `dist/hive-sse-parity` 再跑探针，避免复用旧二进制。

## 3. golden 断言（本次结果）

| 断言 | 结果 | 证据 |
| --- | --- | --- |
| `live-core-equal` | PASS | 34/34 帧逐帧一致（类型 + 信封 + 数据） |
| `replay-core-equal` | PASS | 17/17 帧逐帧一致 |
| `history-core-equal` | PASS | 42/42 帧逐帧一致（history 在 DELETE 之前抓取，见 §4.3） |
| `replay-first-frame-snapshot` | PASS | 两侧首帧均为合成 `session_snapshot` |
| `replay-subsequence-of-history-{side}` | PASS | replay 是 history 的有序子序列（允许被抑制项跳过） |
| `judge-verdicts-equal` | PASS | 两侧 history 各 3 条 `judge_verdict{RETRY,"",implicit,0/1/2}`，live/replay 均不可见 |
| `async-title-multiset-equal` | PASS | `session_title_updated` 载荷/数量一致，位置非契约 |
| `exceptions-observed` | PASS | 11 条 SSE 例外全部在 golden 观测到（陈旧例外会 FAIL） |
| `stop-tail-informational` | 记录 | DELETE 停机尾巴：reference 永久 loop 可能多发一帧 `loop_state_changed`，Go 的每回合 loop 已收口；帧已存 golden，顺序不作断言 |

## 4. 本车道修复（可修项 → 已修）

### 4.1 `context_usage_updated` 的工具批触发点（补齐 2 帧/回合）

参考在工具批里每个工具结果落库后发 `trigger=post_tool_call`，批尾再发一次
`trigger=post_tool_results`（`agent_loop.py::_execute_impl`）；Go 此前只有 `iteration_start`。
现在 Go 的 `storeToolBatch` 在每次 `AddToolResult` 后按调用序发 `tool_call_completed →
context_usage_updated(post_tool_call)`，批尾发 `context_usage_updated(post_tool_results)`，
再发该次迭代的 `llm_turn_complete`。观测值随之对齐：`message_count` 由 5 → 7 → 7 →
（下一迭代）8，与参考相同；剩余差异只有开场读数（参考有 1 条种子消息，Go 为 0，见 §5）。
回归测试：`internal/server/sse_payload_parity_test.go::TestToolBatchPublishesPostToolContextReadouts`
（`// Parity:` 指向 `_execute_impl` 与 `publish_context_usage`）。

### 4.2 `cost_usd` 数字表示（`0` vs `0.0`）——已修

`eventbus.EmitLLMTurnComplete` 现在用 `pythonFloat` 把 float 按 Python 的口径渲染
（整数浮点补 `.0`，必要时回退定点表示），`credits` 同理；`0` 与 `0.0` 的最后一条类型差消失。
回归测试：`internal/eventbus/event_bus_test.go::TestEmitLLMTurnComplete` 改为断言
JSON 字节（`Marshal(cost_usd) == "0.0042"`）。

### 4.3 history 抓取点前移（探针）

`DELETE /api/sessions/{id}` 的停机尾巴此前会写进 history（reference 多一帧
`loop_state_changed`），污染契约日志。探针现在**先抓 history 再停会话**，停机后的日志另存
`history_after_stop`（仅证据，不参与比较），`stop` 流单独记录为信息性差异。

### 4.4 REST 失败路径与状态码对照

`freeze_api_responses.py` 新增 3 条失败探针（§6），并在 `diff_endpoint` 里把
「状态码相等」「期望失败码」「error 文案相等」加入语义检查；当前 17/17 PASS、状态码不一致 0。

## 5. 例外清单（结构化 + 值级；不进对齐目标）

SSE（`contracts/sse-frames/diff.json.exceptions`，11 条）与 REST
（`contracts/api-responses/value-golden.json.exceptions`，8 条规则）的关系：

| 类别 | 例外 | 归因 |
| --- | --- | --- |
| 有意结构（应当保留） | `node_loop_started.max_iterations` 999999 vs 50；`node_loop_iteration/llm_turn_complete/client_output_delta.iteration` 会话累计 vs 每回合；`node_loop_completed.iterations`、`execution_*` 的 `input`/`output`（随信封整体剔除） | Python 是永久 loop、Go 是每回合 bounded loop（`sse-payload-parity.md` §4.1/§4.2 既有结论）。快照 `is_executing` 与前端 spinner 依赖该信封，**不硬改**；若要统一 loop 语义需单独立项确认 |
| 值级（提示词 / 语料车道） | `llm_turn_complete.model`（litellm 前缀 `openai/…`）；`system_prefix_sha`；`context_usage_updated` 的 `usage_ratio/usage_pct/estimated_tokens/message_count/breakdown.*`；REST 侧同路径 + `queen_tools` 24 条工具描述 | 系统提示词与工具描述文本仍在对齐（prompt / tool-description 车道）。字段集合、类型、发射点、枚举值不受影响 |
| 值级（开场结构） | `context_usage_updated.message_count` 开场 1 vs 0 | 开场块消息结构与提示词车道同源；后续读数已一致（§4.1 钉住 7/7/8） |
| 运行期内部 | REST `session_stats.event_bus.subscriptions`（9 vs 1）；`health.resources.*`（比较时忽略） | 进程内读数，不属 HTTP 契约 |
| 非契约 | `session_title_updated` 位置；`stop` 停机尾巴 | 异步任务 / 关停竞态，两侧都可能漂移 |

> 未声明差异 = FAIL：`--check`（SSE / REST）都会在出现新差异或旧例外失效时退出 1。

## 6. REST 值级 golden（`api_response_values.py`）

- **输入**：`contracts/api-responses/index.json` + `{reference,target}/<endpoint>.json`（canonical 载荷，已由
  `freeze_api_responses.py` 冻结；值级脚本只读，不抓取）。
- **条件采样字段**：`health.resources.avail_mb` / `resources.renderers` 只在宿主采样恰好在冻结时触发才出现，
  冻结时从 shape 中剔除（`shape_ignore`），canonical 载荷仍保留作为证据，避免快照门禁要求每次运行都出现。
- **展开规则**：递归展开为叶子路径；数组带稳定身份键（`credential_id`/`skill_id`/`tool_name`/`id`/`name`）
  时按该键排序后逐项比较（map 迭代序不算契约变化）；纯标量数组按多重集；其余按位置。
- **失败路径对照**（新增探针）：

| 端点 | 方法 | 状态 | error 文案 |
| --- | --- | --- | --- |
| `/api/sessions/session_…deadbeef` | GET | 404/404 | `Session '<session_id>' not found`（id 已掩码） |
| `/api/sessions`（body=`[]`） | POST | 400/400 | `Request body must be a JSON object` |
| `/api/sessions/{session}/colony/resolve_missed` | POST | 409/409 | `Colony not loaded` |

- **结果**：17/17 PASS（14 成功 + 3 失败），4910 条值路径，37 条已声明差异（24 工具描述 +
  2 SSE 帧 model/sha + 10 history 数值/结构 + 1 运行期内部），0 未声明。
- `pythonFloat` 修复后，`session_events` / `session_events_history` 的 `cost_usd` 已从差异清单消失。

## 7. 复现与门禁

```bash
# 刷新双跑证据 + 帧级 golden（先删旧二进制；脚本会自动删）
uv run python go_hive/scripts/sse_frames_canonical.py --refresh
# 刷新 REST 形状/语义冻结（含 3 条失败路径）
rm -f go_hive/go-hive/dist/hive-contract && uv run python go_hive/scripts/freeze_api_responses.py
# 写/验值级账本与帧级 golden
uv run python go_hive/scripts/api_response_values.py
uv run python go_hive/scripts/api_response_values.py --check
uv run python go_hive/scripts/sse_frames_canonical.py --check
```

探针与 golden 均只监听 127.0.0.1、只用本地 stub，不访问外部服务；**不纳入 `make ci`**，
按需手动跑。本车道门禁只跑受影响范围（gofmt / vet / 目标包测试 / parity / 外观矩阵 --check），
不跑全量 `make ci`。

## 8. 未覆盖 / 后续

- 真实 provider 冒烟：当前剧本与 stub 固定内容，值级差异在提示词车道收敛后需再复跑。
- 多工具批的金句序：单工具批已被 golden + 单测钉住；多工具批的 `tool_call_completed →
  post_tool_call` 交替顺序由同一段代码保证，但未纳入本剧本。
- `system_prefix_sha` / `model` / 工具描述等提示词语料的追平（prompt / tool-description 车道）。
