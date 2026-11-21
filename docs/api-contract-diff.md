# API 契约差异（reference=python → target=go_hive）

- 生成时间：2026-11-21T12:27:19+08:00
- 基准标识：`reference=python@0763d605d3bba1ee5d943265e87b675be5c0eaee`，`target=go_hive@0763d605d3bba1ee5d943265e87b675be5c0eaee`
- reference（契约基准）：`python` hive 1.0.14，
  HEAD `0763d605d3bba1ee5d943265e87b675be5c0eaee`，`core` 最后改动 `856854659269aa4c9fcfc8ae89ff956eaff75b66`，`tools` 最后改动 `68b6c230f20e237953dea7a3af7bb583b9fbe0fd`
- target（待对齐）：`go_hive` hive 1.0.26-go.1，HEAD `0763d605d3bba1ee5d943265e87b675be5c0eaee`
- 探针输入：queen=`queen_growth`；LLM 槽位指向本地 OpenAI 兼容 stub（`http://127.0.0.1:63937/v1`，确定性开场回合，不访问外网），SSE 窗口 3.0s
- 产物：`go_hive/contracts/api-responses/`（canonical JSON + shape + diff.json）

> 口径：契约基准是 Python 当前 checkout 的抓取结果，所有字段名/类型/数量以 reference 为准；
> Go 侧只作为 target 量化差异。易变值（session id、时间戳、端口、临时路径、资源读数）已替换为占位符，
> 但键的存在性（null vs 缺失）保留。

**端点：74/77 PASS，3 DECLARED（已声明差异），0 FAIL。**
**路径：成功 56，失败路径 21（400/404/409 字段对照）；状态码不一致 0。**
**差异计数：missing_in_target=327，extra_in_target=1，type_mismatch=5，count_mismatch=5，semantic_failures=0。**

## 路由面（静态提取）

- reference=217 条，target=220 条；missing_in_target=2（effective=0），extra_in_target=5（api=5），method_mismatch=1。
- 明细：`go_hive/contracts/api-routes.json`（每条 Go 多余路由带 `annotation` 标注是否为扩展/别名）。

## 未知路径 fallback（待定口径，不改）

未知路径 fallback（待定口径，不改）：Python 的 SPA fallback 对未注册的 /api/* GET 返回 200 text/html + index.html；Go 路由返回 404 JSON。

| 路径 | reference 状态 | reference Content-Type | target 状态 | target Content-Type |
|------|----------------|------------------------|-------------|---------------------|
| `/api/__contract_probe_unknown__` | 200 | `text/html` | 404 | `text/plain; charset=utf-8` |
| `/api/models` | 200 | `text/html` | 404 | `text/plain; charset=utf-8` |
| `/api/org-chart` | 200 | `text/html` | 404 | `text/plain; charset=utf-8` |

原始证据：`go_hive/contracts/api-responses/fallback-probe.json`；口径讨论见 `docs/api-ws-golden.md`。

## 端点汇总

| # | 端点 | 方法 | 状态码 (reference/target) | 判定 | 差异数 |
|---|------|------|---------------------------|------|--------|
| 1 | `/api/health` | GET | 200/200 | **PASS** | 0 |
| 2 | `/api/queen/profiles` | GET | 200/200 | **PASS** | 0 |
| 3 | `/api/queen/queen_growth/tools` | GET | 200/200 | **PASS** | 0 |
| 4 | `/api/skills` | GET | 200/200 | **PASS** | 0 |
| 5 | `/api/config/llm` | GET | 200/200 | **PASS** | 0 |
| 6 | `/api/models/overview` | GET | 200/200 | **PASS** | 0 |
| 7 | `/api/credentials/specs` | GET | 200/200 | **PASS** | 0 |
| 8 | `/api/sessions` | POST | 201/201 | **PASS** | 0 |
| 9 | `/api/sessions` | GET | 200/200 | **PASS** | 0 |
| 10 | `/api/sessions/{session}` | GET | 200/200 | **PASS** | 0 |
| 11 | `/api/sessions/{session}/events` | GET | 200/200 | **PASS** | 0 |
| 12 | `/api/sessions/{session}/events/history` | GET | 200/200 | **PASS** | 0 |
| 13 | `/api/sessions/{session}/snapshot` | GET | 200/200 | **PASS** | 0 |
| 14 | `/api/sessions/{session}/stats` | GET | 200/200 | **PASS** | 0 |
| 15 | `/api/sessions/session_20990101_000000_deadbeef` | GET | 404/404 | **PASS** | 0 |
| 16 | `/api/sessions` | POST | 400/400 | **PASS** | 0 |
| 17 | `/api/sessions/{session}/colony/resolve_missed` | POST | 409/409 | **PASS** | 0 |
| 18 | `/api/sessions/history` | GET | 200/200 | **PASS** | 0 |
| 19 | `/api/sessions/{session}/live_tools` | GET | 200/200 | **DECLARED** | 333 |
| 20 | `/api/sessions/{session}/triggers` | GET | 200/200 | **PASS** | 0 |
| 21 | `/api/sessions/{session}/workers` | GET | 200/200 | **PASS** | 0 |
| 22 | `/api/sessions/{session}/skill-installs` | GET | 200/200 | **PASS** | 0 |
| 23 | `/api/sessions/{session}/tasks` | GET | 200/200 | **PASS** | 0 |
| 24 | `/api/config/features` | GET | 200/200 | **PASS** | 0 |
| 25 | `/api/config/models` | GET | 200/200 | **DECLARED** | 3 |
| 26 | `/api/config/profile` | GET | 200/200 | **PASS** | 0 |
| 27 | `/api/config/sentinel` | GET | 200/200 | **PASS** | 0 |
| 28 | `/api/config/rate-limits` | GET | 200/200 | **PASS** | 0 |
| 29 | `/api/config/llm-sections` | GET | 200/200 | **PASS** | 0 |
| 30 | `/api/config/external-skills` | GET | 200/200 | **PASS** | 0 |
| 31 | `/api/config/provider-library` | GET | 200/200 | **PASS** | 0 |
| 32 | `/api/credentials/oauth-status` | GET | 200/200 | **PASS** | 0 |
| 33 | `/api/sentinel/credentials` | GET | 200/200 | **PASS** | 0 |
| 34 | `/api/credentials` | POST | 201/201 | **PASS** | 0 |
| 35 | `/api/credentials` | GET | 200/200 | **PASS** | 0 |
| 36 | `/api/credentials/airtable_pat` | GET | 200/200 | **PASS** | 0 |
| 37 | `/api/skills/scopes` | GET | 200/200 | **PASS** | 0 |
| 38 | `/api/skills/administrative-behavior-skill` | GET | 200/200 | **PASS** | 0 |
| 39 | `/api/queen/queen_growth/skills` | GET | 200/200 | **PASS** | 0 |
| 40 | `/api/mcp/servers` | GET | 200/200 | **PASS** | 0 |
| 41 | `/api/browser/status` | GET | 200/200 | **PASS** | 0 |
| 42 | `/api/browser/contexts` | GET | 200/200 | **DECLARED** | 2 |
| 43 | `/api/browser/tabs` | GET | 200/200 | **PASS** | 0 |
| 44 | `/api/colonies/tools-index` | GET | 200/200 | **PASS** | 0 |
| 45 | `/api/queen/removed` | GET | 200/200 | **PASS** | 0 |
| 46 | `/api/queen/queen_growth/overview` | GET | 200/200 | **PASS** | 0 |
| 47 | `/api/queen/queen_growth/profile` | GET | 200/200 | **PASS** | 0 |
| 48 | `/api/queen/queen_growth/llm` | GET | 200/200 | **PASS** | 0 |
| 49 | `/api/queen/queen_growth/persona` | GET | 200/200 | **PASS** | 0 |
| 50 | `/api/queen/queen_growth/soul` | GET | 200/200 | **PASS** | 0 |
| 51 | `/api/maintenance/janitor/report` | GET | 200/200 | **PASS** | 0 |
| 52 | `/api/sessions/{session}/upload-attachment` | POST | 200/200 | **PASS** | 0 |
| 53 | `/api/sessions/{session}/attachment/probe-note.txt` | GET | 200/200 | **PASS** | 0 |
| 54 | `/api/config/llm` | PUT | 200/200 | **PASS** | 0 |
| 55 | `/api/sessions` | POST | 201/201 | **PASS** | 0 |
| 56 | `/api/sessions/{session2}/title` | PATCH | 200/200 | **PASS** | 0 |
| 57 | `/api/sessions/{session2}/chat` | POST | 200/200 | **PASS** | 0 |
| 58 | `/api/sessions/{session2}` | DELETE | 200/200 | **PASS** | 0 |
| 59 | `/api/sessions/{session2}` | GET | 200/200 | **PASS** | 0 |
| 60 | `/api/sessions/session_20990101_000000_deadbeef/live_tools` | GET | 404/404 | **PASS** | 0 |
| 61 | `/api/sessions/session_20990101_000000_deadbeef/tasks` | GET | 404/404 | **PASS** | 0 |
| 62 | `/api/sessions/session_20990101_000000_deadbeef/triggers` | GET | 404/404 | **PASS** | 0 |
| 63 | `/api/sessions/session_20990101_000000_deadbeef/workers` | GET | 404/404 | **PASS** | 0 |
| 64 | `/api/sessions/session_20990101_000000_deadbeef/skill-installs` | GET | 404/404 | **PASS** | 0 |
| 65 | `/api/sessions/{session}/attachment/nope.txt` | GET | 404/404 | **PASS** | 0 |
| 66 | `/api/sessions/session_20990101_000000_deadbeef/upload-attachment` | POST | 404/404 | **PASS** | 0 |
| 67 | `/api/sessions/session_20990101_000000_deadbeef/chat` | POST | 404/404 | **PASS** | 0 |
| 68 | `/api/sessions/session_20990101_000000_deadbeef/title` | PATCH | 404/404 | **PASS** | 0 |
| 69 | `/api/sessions/session_20990101_000000_deadbeef` | DELETE | 404/404 | **PASS** | 0 |
| 70 | `/api/credentials/nope_probe` | GET | 404/404 | **PASS** | 0 |
| 71 | `/api/skills/does-not-exist-probe` | GET | 404/404 | **PASS** | 0 |
| 72 | `/api/queen/queen_nope/overview` | GET | 404/404 | **PASS** | 0 |
| 73 | `/api/mcp/servers/nope/health` | POST | 404/404 | **PASS** | 0 |
| 74 | `/api/sentinel/telegram/validate` | POST | 400/400 | **PASS** | 0 |
| 75 | `/api/credentials` | POST | 400/400 | **PASS** | 0 |
| 76 | `/api/config/llm` | PUT | 400/400 | **PASS** | 0 |
| 77 | `/api/config/llm` | PUT | 500/500 | **PASS** | 0 |

## 逐端点明细

### 1. GET `/api/health` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 口径备注：比较时忽略 resources.：这些是进程环境读数（内存采样/渲染器数），不是 API 契约字段；reference 触发采样时才会出现 avail_mb/renderers。
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ status == ok — reference='ok' target='ok'

### 2. GET `/api/queen/profiles` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ queen id set equal — reference=13 target=13 missing=[]

### 3. GET `/api/queen/queen_growth/tools` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ queen_id equal — reference='queen_growth' target='queen_growth'
- ✓ mcp server set equal — reference=['chart-tools', 'files-tools', 'hive_tools', 'terminal-tools'] target=['chart-tools', 'files-tools', 'hive_tools', 'terminal-tools']

### 4. GET `/api/skills` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ skill name set equal — reference=195 target=195 missing=[] extra=[]

### 5. GET `/api/config/llm` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ provider equal — reference='openai' target='openai'
- ✓ model equal — reference='contract-probe-model' target='contract-probe-model'
- ✓ has_api_key equal — reference=True target=True

### 6. GET `/api/models/overview` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ slots.llm.provider equal — reference='openai' target='openai'
- ✓ slots.llm.model equal — reference='contract-probe-model' target='contract-probe-model'

### 7. GET `/api/credentials/specs` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ credential_id set equal — reference=115 target=115 missing=[] extra=[]

### 8. POST `/api/sessions` — PASS

- 状态码：reference `201` / target `201`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=201 target=201
- ✓ queen_id equal — reference='queen_growth' target='queen_growth'
- ✓ queen_name equal — reference='Steve Jobs' target='Steve Jobs'
- ✓ queen_phase equal — reference='independent' target='independent'

### 9. GET `/api/sessions` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ session count equal — reference=1 target=1

### 10. GET `/api/sessions/{session}` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ queen_id equal — reference='queen_growth' target='queen_growth'

### 11. GET `/api/sessions/{session}/events` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `text/event-stream` / target `text/event-stream`
- 口径备注：events[].data 与 frames[].envelope.data 是 google.protobuf.Struct 自由字段：按事件类型比较 data 键集合（见语义检查），不逐字段列出。
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ content type is event-stream — reference='text/event-stream' target='text/event-stream'
- ✓ payload type set equal — reference=['client_input_requested', 'client_output_delta', 'llm_turn_complete', 'session_snapshot'] target=['client_input_requested', 'client_output_delta', 'llm_turn_complete', 'session_snapshot']
- ✓ SSE event: name presence equal — reference=[] target=[]
- ✓ data keys equal for common frame types — differences=[]

### 12. GET `/api/sessions/{session}/events/history` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 口径备注：events[].data 与 frames[].envelope.data 是 google.protobuf.Struct 自由字段：按事件类型比较 data 键集合（见语义检查），不逐字段列出。
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ event type sequence equal — reference=['queen_identity_selected', 'reminder_injected', 'node_loop_started', 'node_loop_iteration', 'context_usage_updated', 'client_output_delta', 'llm_turn_complete', 'client_input_requested', 'loop_state_changed'] target=['queen_identity_selected', 'reminder_injected', 'node_loop_started', 'node_loop_iteration', 'context_usage_updated', 'client_output_delta', 'llm_turn_complete', 'client_input_requested', 'loop_state_changed']
- ✓ total equal — reference=9 target=9
- ✓ data keys equal for common event types — differences=[]

### 13. GET `/api/sessions/{session}/snapshot` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ snapshot keys equal — reference=['activity', 'awaiting_input', 'current_execution_id', 'current_tool_calls', 'interrupt_cause', 'interrupted', 'is_executing', 'last_event_at', 'last_event_seq', 'last_queen_event_at', 'park_reason', 'pending_questions', 'queen_busy_reason', 'snapshot_seq'] target=['activity', 'awaiting_input', 'current_execution_id', 'current_tool_calls', 'interrupt_cause', 'interrupted', 'is_executing', 'last_event_at', 'last_event_seq', 'last_queen_event_at', 'park_reason', 'pending_questions', 'queen_busy_reason', 'snapshot_seq']

### 14. GET `/api/sessions/{session}/stats` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200
- ✓ stats keys equal — reference=['active_workers', 'adaptive_tool_budget', 'colony_id', 'event_bus', 'running', 'total_workers', 'triggers'] target=['active_workers', 'adaptive_tool_budget', 'colony_id', 'event_bus', 'running', 'total_workers', 'triggers']
- ✓ running equal — reference=True target=True
- ✓ event_bus.subscriptions is runtime-internal (not compared) — reference=9 target=3

### 15. GET `/api/sessions/session_20990101_000000_deadbeef` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Session '<session_id>' not found" target="Session '<session_id>' not found"

### 16. POST `/api/sessions` — PASS

- 状态码：reference `400` / target `400`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=400 target=400
- ✓ expected failure status — expect=400 reference=400 target=400
- ✓ error message equal — reference='Request body must be a JSON object' target='Request body must be a JSON object'

### 17. POST `/api/sessions/{session}/colony/resolve_missed` — PASS

- 状态码：reference `409` / target `409`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=409 target=409
- ✓ expected failure status — expect=409 reference=409 target=409
- ✓ error message equal — reference='Colony not loaded' target='Colony not loaded'

### 18. GET `/api/sessions/history` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 19. GET `/api/sessions/{session}/live_tools` — DECLARED

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 口径备注：已声明差异（runtime-state-gap）：REST-created Go sessions carry no PhaseState (only CLI / preloaded / resumed sessions attach one), so actual_tools / expected_tools / framework_added and the registry diagnostic are empty while the Python reference attaches a queen phase state at creation. The key/type shape is pinned; the values are a known gap.

| 字段 | proto 类型 | reference 实际 | target 实际 | JSON 可规范化 | 说明 |
|------|-----------|----------------|-------------|----------------|------|
| `diagnostic.admitted_by_provider` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.airtable_pat` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.airtable_pat.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.airtable_pat.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.airtable_pat.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.apify` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.apify.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.apify.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.apify.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.apollo` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.apollo.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.apollo.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.apollo.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.asana` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.asana.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.asana.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.asana.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.attio` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.attio.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.attio.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.attio.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.aws_secret_key` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.aws_secret_key.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.aws_secret_key.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.aws_secret_key.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.azure_subscription_id` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.azure_subscription_id.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.azure_subscription_id.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.azure_subscription_id.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.bigquery_project` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.bigquery_project.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.bigquery_project.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.bigquery_project.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.brave_search` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.brave_search.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.brave_search.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.brave_search.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.brevo` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.brevo.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.brevo.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.brevo.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.calcom` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.calcom.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.calcom.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.calcom.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.calendly_pat` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.calendly_pat.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.calendly_pat.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.calendly_pat.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.cloudflare` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.cloudflare.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.cloudflare.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.cloudflare.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.cloudinary_secret` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.cloudinary_secret.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.cloudinary_secret.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.cloudinary_secret.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.confluence_token` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.confluence_token.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.confluence_token.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.confluence_token.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.databricks` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.databricks.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.databricks.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.databricks.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.discord` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.discord.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.discord.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.discord.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.docker_hub` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.docker_hub.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.docker_hub.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.docker_hub.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.exa_search` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.exa_search.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.exa_search.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.exa_search.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.finlight` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.finlight.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.finlight.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.finlight.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.freshdesk_domain` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.freshdesk_domain.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.freshdesk_domain.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.freshdesk_domain.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.github` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.github.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.github.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.github.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.gitlab_token` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.gitlab_token.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.gitlab_token.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.gitlab_token.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_analytics` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_analytics.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_analytics.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_analytics.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_cse` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_cse.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_cse.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_cse.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_maps` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_maps.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_maps.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_maps.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_search_console` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_search_console.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_search_console.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_search_console.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_vision` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_vision.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_vision.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.google_vision.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.greenhouse_token` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.greenhouse_token.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.greenhouse_token.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.greenhouse_token.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.hubspot` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.hubspot.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.hubspot.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.hubspot.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.huggingface` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.huggingface.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.huggingface.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.huggingface.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.intercom` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.intercom.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.intercom.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.intercom.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.jira_token` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.jira_token.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.jira_token.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.jira_token.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.kafka_cluster_id` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.kafka_cluster_id.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.kafka_cluster_id.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.kafka_cluster_id.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.langfuse_secret_key` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.langfuse_secret_key.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.langfuse_secret_key.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.langfuse_secret_key.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.linear` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.linear.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.linear.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.linear.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.lusha_api_key` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.lusha_api_key.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.lusha_api_key.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.lusha_api_key.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.mattermost_url` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.mattermost_url.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.mattermost_url.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.mattermost_url.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.microsoft_graph` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.microsoft_graph.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.microsoft_graph.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.microsoft_graph.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.mongodb_data_source` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.mongodb_data_source.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.mongodb_data_source.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.mongodb_data_source.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.n8n_base_url` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.n8n_base_url.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.n8n_base_url.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.n8n_base_url.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.newsdata` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.newsdata.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.newsdata.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.newsdata.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.notion` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.notion.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.notion.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.notion.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.obsidian` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.obsidian.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.obsidian.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.obsidian.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pagerduty_api_key` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pagerduty_api_key.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pagerduty_api_key.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pagerduty_api_key.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pagerduty_from_email` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pagerduty_from_email.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pagerduty_from_email.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pagerduty_from_email.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pinecone` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pinecone.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pinecone.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pinecone.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pipedrive` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pipedrive.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pipedrive.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pipedrive.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.plaid_secret` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.plaid_secret.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.plaid_secret.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.plaid_secret.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.postgres` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.postgres.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.postgres.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.postgres.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.powerbi_token` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.powerbi_token.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.powerbi_token.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.powerbi_token.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.prometheus` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.prometheus.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.prometheus.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.prometheus.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pushover` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pushover.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pushover.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.pushover.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.quickbooks_realm_id` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.quickbooks_realm_id.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.quickbooks_realm_id.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.quickbooks_realm_id.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.razorpay_secret` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.razorpay_secret.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.razorpay_secret.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.razorpay_secret.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.reddit_secret` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.reddit_secret.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.reddit_secret.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.reddit_secret.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.redis` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.redis.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.redis.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.redis.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.redshift_secret_key` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.redshift_secret_key.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.redshift_secret_key.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.redshift_secret_key.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.salesforce_instance_url` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.salesforce_instance_url.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.salesforce_instance_url.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.salesforce_instance_url.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.sap_password` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.sap_password.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.sap_password.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.sap_password.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.serpapi` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.serpapi.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.serpapi.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.serpapi.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.shopify_store_name` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.shopify_store_name.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.shopify_store_name.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.shopify_store_name.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.similarweb` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.similarweb.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.similarweb.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.similarweb.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.slack` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.slack.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.slack.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.slack.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.snowflake_token` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.snowflake_token.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.snowflake_token.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.snowflake_token.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.stripe` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.stripe.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.stripe.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.stripe.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.supabase` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.supabase.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.supabase.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.supabase.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.telegram` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.telegram.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.telegram.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.telegram.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.tfc_token` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.tfc_token.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.tfc_token.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.tfc_token.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.tines_api_key` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.tines_api_key.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.tines_api_key.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.tines_api_key.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.trello_token` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.trello_token.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.trello_token.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.trello_token.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.twilio_token` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.twilio_token.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.twilio_token.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.twilio_token.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.vercel` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.vercel.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.vercel.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.vercel.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.wandb_api_key` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.wandb_api_key.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.wandb_api_key.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.wandb_api_key.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.x_bearer_token` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.x_bearer_token.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.x_bearer_token.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.x_bearer_token.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.youtube` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.youtube.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.youtube.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.youtube.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zendesk_token` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zendesk_token.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zendesk_token.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zendesk_token.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zoho_crm` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zoho_crm.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zoho_crm.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zoho_crm.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zoom` | `undeclared (no message mapped for endpoint session_live_tools)` | `object` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zoom.count` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zoom.sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.dropped_by_provider.zoom.sample[]` | `undeclared (no message mapped for endpoint session_live_tools)` | `string` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.gate_live_providers_now` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.gate_tool_provider_map_size` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.google_admitted_sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.registry_mcp_total` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `diagnostic.phase_state.allowlist_total` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `null` | no | null vs value: proto3 JSON folds null and unset together; the distinction is lost |
| `diagnostic.phase_state.google_in_allowlist` | `undeclared (no message mapped for endpoint session_live_tools)` | `number(int)` | `null` | no | null vs value: proto3 JSON folds null and unset together; the distinction is lost |
| `actual_tools` | `undeclared (no message mapped for endpoint session_live_tools)` | `array[21]` | `array[1]` | no | element count differs; only the element type is proto-verifiable |
| `diagnostic.gate_live_providers_now` | `undeclared (no message mapped for endpoint session_live_tools)` | `array[0]` | `array[None]` | no | element count differs; only the element type is proto-verifiable |
| `diagnostic.google_admitted_sample` | `undeclared (no message mapped for endpoint session_live_tools)` | `array[0]` | `array[None]` | no | element count differs; only the element type is proto-verifiable |
| `expected_tools` | `undeclared (no message mapped for endpoint session_live_tools)` | `array[32]` | `array[0]` | no | element count differs; only the element type is proto-verifiable |
| `tools` | `undeclared (no message mapped for endpoint session_live_tools)` | `array[21]` | `array[1]` | no | element count differs; only the element type is proto-verifiable |

语义检查：
- ✓ status equal — reference=200 target=200

### 20. GET `/api/sessions/{session}/triggers` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 21. GET `/api/sessions/{session}/workers` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 22. GET `/api/sessions/{session}/skill-installs` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 23. GET `/api/sessions/{session}/tasks` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 24. GET `/api/config/features` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 25. GET `/api/config/models` — DECLARED

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 口径备注：已声明差异（non-verbatim-numeric）：Three integer-valued prices render as JSON 5.0 (Python) vs 5 (Go); both are JSON numbers and proto double accepts either (documented NV-02). Values are otherwise identical.

| 字段 | proto 类型 | reference 实际 | target 实际 | JSON 可规范化 | 说明 |
|------|-----------|----------------|-------------|----------------|------|
| `models.openai[].pricing_usd_per_mtok.input` | `undeclared (no message mapped for endpoint config_models)` | `number(float)` | `number(int)` | yes (numeric canonicalization) | both are JSON numbers; proto double accepts 3 and 3.0 |
| `models.openai[].pricing_usd_per_mtok.output` | `undeclared (no message mapped for endpoint config_models)` | `number(float)` | `number(int)` | yes (numeric canonicalization) | both are JSON numbers; proto double accepts 3 and 3.0 |
| `models.openrouter[].pricing_usd_per_mtok.cache_creation` | `undeclared (no message mapped for endpoint config_models)` | `number(float)` | `number(int)` | yes (numeric canonicalization) | both are JSON numbers; proto double accepts 3 and 3.0 |

语义检查：
- ✓ status equal — reference=200 target=200

### 26. GET `/api/config/profile` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 27. GET `/api/config/sentinel` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 28. GET `/api/config/rate-limits` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 29. GET `/api/config/llm-sections` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 30. GET `/api/config/external-skills` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 31. GET `/api/config/provider-library` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 32. GET `/api/credentials/oauth-status` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 33. GET `/api/sentinel/credentials` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 34. POST `/api/credentials` — PASS

- 状态码：reference `201` / target `201`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=201 target=201

### 35. GET `/api/credentials` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 36. GET `/api/credentials/airtable_pat` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 37. GET `/api/skills/scopes` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 38. GET `/api/skills/administrative-behavior-skill` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 39. GET `/api/queen/queen_growth/skills` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 40. GET `/api/mcp/servers` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 41. GET `/api/browser/status` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 42. GET `/api/browser/contexts` — DECLARED

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 口径备注：已声明差异（empty-state-divergence）：Python reports its in-process gcu lifecycle registry (available:true, count:0) while Go proxies the bridge's /contexts and reports the unavailable envelope (available:false, reason) when no bridge/extension is present. Both shapes are UI-handled; pinned here as a known empty-state difference.

| 字段 | proto 类型 | reference 实际 | target 实际 | JSON 可规范化 | 说明 |
|------|-----------|----------------|-------------|----------------|------|
| `count` | `undeclared (no message mapped for endpoint browser_contexts)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `reason` | `undeclared (no message mapped for endpoint browser_contexts)` | `absent` | `string` | yes | target extension: additive field; not part of the reference contract |

语义检查：
- ✓ status equal — reference=200 target=200

### 43. GET `/api/browser/tabs` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 44. GET `/api/colonies/tools-index` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 45. GET `/api/queen/removed` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 46. GET `/api/queen/queen_growth/overview` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 47. GET `/api/queen/queen_growth/profile` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 48. GET `/api/queen/queen_growth/llm` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 49. GET `/api/queen/queen_growth/persona` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 50. GET `/api/queen/queen_growth/soul` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 51. GET `/api/maintenance/janitor/report` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 52. POST `/api/sessions/{session}/upload-attachment` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 53. GET `/api/sessions/{session}/attachment/probe-note.txt` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `text/plain` / target `text/plain`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 54. PUT `/api/config/llm` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 55. POST `/api/sessions` — PASS

- 状态码：reference `201` / target `201`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=201 target=201

### 56. PATCH `/api/sessions/{session2}/title` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 57. POST `/api/sessions/{session2}/chat` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 58. DELETE `/api/sessions/{session2}` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 59. GET `/api/sessions/{session2}` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 60. GET `/api/sessions/session_20990101_000000_deadbeef/live_tools` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="session '<session_id>' not found" target="session '<session_id>' not found"

### 61. GET `/api/sessions/session_20990101_000000_deadbeef/tasks` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Task list for session '<session_id>' not found" target="Task list for session '<session_id>' not found"

### 62. GET `/api/sessions/session_20990101_000000_deadbeef/triggers` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Session '<session_id>' not found" target="Session '<session_id>' not found"

### 63. GET `/api/sessions/session_20990101_000000_deadbeef/workers` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Session '<session_id>' not found" target="Session '<session_id>' not found"

### 64. GET `/api/sessions/session_20990101_000000_deadbeef/skill-installs` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='session not found' target='session not found'

### 65. GET `/api/sessions/{session}/attachment/nope.txt` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `text/plain; charset=utf-8` / target `text/plain; charset=utf-8`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='Attachment not found' target='Attachment not found'

### 66. POST `/api/sessions/session_20990101_000000_deadbeef/upload-attachment` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='session not found' target='session not found'

### 67. POST `/api/sessions/session_20990101_000000_deadbeef/chat` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Session '<session_id>' not found" target="Session '<session_id>' not found"

### 68. PATCH `/api/sessions/session_20990101_000000_deadbeef/title` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='No such queen conversation' target='No such queen conversation'

### 69. DELETE `/api/sessions/session_20990101_000000_deadbeef` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Session '<session_id>' not found" target="Session '<session_id>' not found"

### 70. GET `/api/credentials/nope_probe` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Credential 'nope_probe' not found" target="Credential 'nope_probe' not found"

### 71. GET `/api/skills/does-not-exist-probe` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="skill 'does-not-exist-probe' not found" target="skill 'does-not-exist-probe' not found"

### 72. GET `/api/queen/queen_nope/overview` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Queen 'queen_nope' not found" target="Queen 'queen_nope' not found"

### 73. POST `/api/mcp/servers/nope/health` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Cannot health-check server 'nope'" target="Cannot health-check server 'nope'"

### 74. POST `/api/sentinel/telegram/validate` — PASS

- 状态码：reference `400` / target `400`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=400 target=400
- ✓ expected failure status — expect=400 reference=400 target=400
- ✓ error message equal — reference='no token provided or saved' target='no token provided or saved'

### 75. POST `/api/credentials` — PASS

- 状态码：reference `400` / target `400`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=400 target=400
- ✓ expected failure status — expect=400 reference=400 target=400
- ✓ error message equal — reference='credential_id and keys are required' target='credential_id and keys are required'

### 76. PUT `/api/config/llm` — PASS

- 状态码：reference `400` / target `400`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=400 target=400
- ✓ expected failure status — expect=400 reference=400 target=400
- ✓ error message equal — reference='Invalid JSON body' target='Invalid JSON body'

### 77. PUT `/api/config/llm` — PASS

- 状态码：reference `500` / target `500`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=500 target=500
- ✓ expected failure status — expect=500 reference=500 target=500
- ✓ error message equal — reference='Internal server error' target='Internal server error'

## 不可逐字清单

| 编号 | 主题 | 说明 |
|------|------|------|
| NV-01 | int64/uint64 与 2^53 | proto3 JSON 把 64 位整数编码为字符串；Hive 两端都发 JSON number。> 2^53 的整数在 JSON number 中不可无损往返，需按 string 传或在契约中声明上限。 |
| NV-02 | JSON number 整数性 | Python json 区分 3（int）与 3.0（float），Go encoding/json 统一成 float64，proto3 double 两种字面量都接受。`uptime_seconds` 的 int/float 差异无法逐字，只能按 double 规范化。 |
| NV-03 | null vs 缺失 | proto3 JSON 中 null 表示字段未设置；它不能与“字段存在且值为 null”区分。契约冻结保留 null 与缺失的差异（shape 里 null 与 absent 是两种类型），但任何 proto 化投影都会丢掉这一层。 |
| NV-04 | map 顺序 | map<string, T> 与 JSON object 的键序都不属于契约；逐字节比较必须排序后比较。 |
| NV-05 | 浮点打印精度 | Python repr 与 Go encoding/json 对 double 的最短表示不同（如 loaded_at 的末位），值级不能逐字比较，只能按 double 语义比较或屏蔽。 |
| NV-06 | Timestamp 文本格式 | google.protobuf.Timestamp 的规范 JSON 是带时区的 RFC 3339；Hive 两端都发本地 naive 时间（YYYY-MM-DDTHH:MM:SS.ffffff）。冻结按不透明 string 处理，转 Timestamp 时必须先约定时区。 |
| NV-07 | NaN / Infinity | proto3 JSON 用 "NaN"/"Infinity" 字符串；Python json 默认发 NaN/Infinity 裸字面量（非标准 JSON）。当前冻结载荷未出现。 |
| NV-08 | bytes/base64 | proto3 bytes 线上是 base64；当前冻结端点不含二进制字段，附件接口未纳入本批冻结。 |

## 类型映射

Python → proto3 → JSON 线格式规则见 `go_hive/contracts/type-mapping.md`；
proto 定义见 `go_hive/contracts/api.proto`（规范文件，不引入 protobuf 运行时）。

## 复现

```bash
uv run python go_hive/scripts/export_api_routes.py
uv run python go_hive/scripts/freeze_api_responses.py
uv run python go_hive/scripts/check_api_proto.py
```
