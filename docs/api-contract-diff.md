# API 契约差异（reference=python → target=go_hive）

- 生成时间：2026-11-21T20:10:41+08:00
- 基准标识：`reference=python@982136f85e8fb3a956469944622edbfce1caa2ea`，`target=go_hive@982136f85e8fb3a956469944622edbfce1caa2ea`
- reference（契约基准）：`python` hive 1.0.14，
  HEAD `982136f85e8fb3a956469944622edbfce1caa2ea`，`core` 最后改动 `16291c8d705d169b473c4ec6e1ec44bf736cff26`，`tools` 最后改动 `68b6c230f20e237953dea7a3af7bb583b9fbe0fd`
- target（待对齐）：`go_hive` hive 1.0.26-go.1，HEAD `982136f85e8fb3a956469944622edbfce1caa2ea`
- 探针输入：queen=`queen_growth`；LLM 槽位指向本地 OpenAI 兼容 stub（`http://127.0.0.1:60527/v1`，确定性开场回合，不访问外网），SSE 窗口 3.0s
- 产物：`go_hive/contracts/api-responses/`（canonical JSON + shape + diff.json）

> 口径：契约基准是 Python 当前 checkout 的抓取结果，所有字段名/类型/数量以 reference 为准；
> Go 侧只作为 target 量化差异。易变值（session id、时间戳、端口、临时路径、资源读数）已替换为占位符，
> 但键的存在性（null vs 缺失）保留。

**端点：105/107 PASS，2 DECLARED（已声明差异），0 FAIL。**
**路径：成功 66，失败路径 41（400/404/409 字段对照）；状态码不一致 0。**
**差异计数：missing_in_target=1，extra_in_target=1，type_mismatch=3，count_mismatch=0，semantic_failures=0。**

## 路由面（静态提取）

- reference=217 条，target=220 条；missing_in_target=2（effective=0），extra_in_target=5（api=5），method_mismatch=1。
- 明细：`go_hive/contracts/api-routes.json`（每条 Go 多余路由带 `annotation` 标注是否为扩展/别名）。

## 未知路径 fallback（同形核对）

未知路径 fallback 两侧同形：本环境 reference/target 都是 404 text/plain; charset=utf-8。两侧都只在存在前端 dist 时注册 SPA catch-all，未注册 GET 一律返回 index.html（含未注册的 /api/*）。

| 路径 | reference 状态 | reference Content-Type | target 状态 | target Content-Type |
|------|----------------|------------------------|-------------|---------------------|
| `/api/__contract_probe_unknown__` | 404 | `text/plain; charset=utf-8` | 404 | `text/plain; charset=utf-8` |
| `/api/models` | 404 | `text/plain; charset=utf-8` | 404 | `text/plain; charset=utf-8` |
| `/api/org-chart` | 404 | `text/plain; charset=utf-8` | 404 | `text/plain; charset=utf-8` |

原始证据：`go_hive/contracts/api-responses/fallback-probe.json`；口径讨论见 `docs/api-ws-golden.md`。

## 显式跳过的端点（值级不比较）

| 方法 | 端点 | 类 | 原因 |
|------|------|----|------|
| GET | `/api/events/global` | sse-stream | Long-lived SSE multiplexer; the stream contract is frozen frame-by-frame in contracts/sse-frames (session_events). |
| GET | `/api/sessions/live` | sse-stream | Long-lived SSE session feed; frame-level coverage lives in contracts/sse-frames. |
| GET | `/api/sessions/{session}/workers/stream` | sse-stream | Long-lived SSE worker feed; needs a live worker to stream, which the throwaway home never has. |
| GET | `/api/browser/status/stream` | sse-stream | Long-lived SSE status feed; /api/browser/status already freezes the snapshot contract. |
| GET | `/api/health/resources` | documented-functional-gap | Python serves its renderer-sampler shape ({available,history,thresholds,verdict}); Go serves the resource-monitor rollup ({available,chrome,components,context,...}). Different feature surfaces, recorded in docs/api-ws-golden.md §8; /api/health already freezes the contract fields. |
| GET | `/api/startup-status` | runtime-internal | Boot-stage history and wall-clock timestamps are per-process samples (Python reports its own boot stages, Go its own); not an HTTP contract value. |
| GET | `/api/runtime-logs` | runtime-internal | Ring buffer of each process's own stdout/stderr; text differs by construction. |
| GET | `/api/sessions/{session}/colony/tools` | runtime-state-gap | Python's REST session binds an eager ColonyRuntime and answers the full tool catalog; the Go REST session has no colony runtime (same root cause as config_llm_put.sessions_swapped). Add the probe once the colony-binding lane lands. |
| GET | `/api/sessions/{session}/colony/skills` | runtime-state-gap | Same colony-runtime binding gap as /colony/tools: Python returns the bound colony's skills, Go returns an empty list. |
| GET | `/api/sessions/{session}/workers/{worker_id}/reap-timeline` | runtime-state-gap | Requires a bound colony to answer: Python resolves the (missing) worker and answers 404, Go answers 503 'No active colony on this session' — a derivative of the colony-binding gap. |
| GET | `/{path:.*}` | covered-elsewhere | SPA fallback; unknown-path behaviour is captured as evidence only in contracts/api-responses/fallback-probe.json (docs/api-ws-golden.md §6.4). |

机器可读清单：`go_hive/contracts/api-responses/index.json` 的 `skipped` 字段。

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
| 19 | `/api/sessions/{session}/live_tools` | GET | 200/200 | **PASS** | 0 |
| 20 | `/api/sessions/{session}/triggers` | GET | 200/200 | **PASS** | 0 |
| 21 | `/api/sessions/{session}/workers` | GET | 200/200 | **PASS** | 0 |
| 22 | `/api/sessions/{session}/skill-installs` | GET | 200/200 | **PASS** | 0 |
| 23 | `/api/sessions/{session}/workers/probe-missing` | GET | 404/404 | **PASS** | 0 |
| 24 | `/api/sessions/{session}/workers/probe-missing/conversation` | GET | 404/404 | **PASS** | 0 |
| 25 | `/api/claude-code-queens/queen_growth/conversations/probe-missing` | GET | 404/404 | **PASS** | 0 |
| 26 | `/api/sessions/{session}/tasks` | GET | 200/200 | **PASS** | 0 |
| 27 | `/api/config/features` | GET | 200/200 | **PASS** | 0 |
| 28 | `/api/config/models` | GET | 200/200 | **DECLARED** | 3 |
| 29 | `/api/config/profile` | GET | 200/200 | **PASS** | 0 |
| 30 | `/api/config/sentinel` | GET | 200/200 | **PASS** | 0 |
| 31 | `/api/config/rate-limits` | GET | 200/200 | **PASS** | 0 |
| 32 | `/api/config/llm-sections` | GET | 200/200 | **PASS** | 0 |
| 33 | `/api/config/external-skills` | GET | 200/200 | **PASS** | 0 |
| 34 | `/api/config/provider-library` | GET | 200/200 | **PASS** | 0 |
| 35 | `/api/credentials/oauth-status` | GET | 200/200 | **PASS** | 0 |
| 36 | `/api/sentinel/credentials` | GET | 200/200 | **PASS** | 0 |
| 37 | `/api/credentials` | POST | 201/201 | **PASS** | 0 |
| 38 | `/api/credentials` | GET | 200/200 | **PASS** | 0 |
| 39 | `/api/credentials/airtable_pat` | GET | 200/200 | **PASS** | 0 |
| 40 | `/api/skills/scopes` | GET | 200/200 | **PASS** | 0 |
| 41 | `/api/skills/administrative-behavior-skill` | GET | 200/200 | **PASS** | 0 |
| 42 | `/api/queen/queen_growth/skills` | GET | 200/200 | **PASS** | 0 |
| 43 | `/api/mcp/servers` | GET | 200/200 | **PASS** | 0 |
| 44 | `/api/browser/status` | GET | 200/200 | **PASS** | 0 |
| 45 | `/api/browser/contexts` | GET | 200/200 | **DECLARED** | 2 |
| 46 | `/api/browser/tabs` | GET | 200/200 | **PASS** | 0 |
| 47 | `/api/colonies/tools-index` | GET | 200/200 | **PASS** | 0 |
| 48 | `/api/queen/removed` | GET | 200/200 | **PASS** | 0 |
| 49 | `/api/queen/queen_growth/overview` | GET | 200/200 | **PASS** | 0 |
| 50 | `/api/queen/queen_growth/profile` | GET | 200/200 | **PASS** | 0 |
| 51 | `/api/queen/queen_growth/llm` | GET | 200/200 | **PASS** | 0 |
| 52 | `/api/queen/queen_growth/persona` | GET | 200/200 | **PASS** | 0 |
| 53 | `/api/queen/queen_growth/soul` | GET | 200/200 | **PASS** | 0 |
| 54 | `/api/maintenance/janitor/report` | GET | 200/200 | **PASS** | 0 |
| 55 | `/api/prompts` | GET | 200/200 | **PASS** | 0 |
| 56 | `/api/memories` | GET | 200/200 | **PASS** | 0 |
| 57 | `/api/memories/file?path=probe-missing.md` | GET | 404/404 | **PASS** | 0 |
| 58 | `/api/channels` | GET | 200/200 | **PASS** | 0 |
| 59 | `/api/claude-code-queens` | GET | 200/200 | **PASS** | 0 |
| 60 | `/api/discover` | GET | 200/200 | **PASS** | 0 |
| 61 | `/api/queen-mail/queens` | GET | 401/401 | **PASS** | 0 |
| 62 | `/api/config/profile/avatar` | GET | 404/404 | **PASS** | 0 |
| 63 | `/api/queen/queen_growth/avatar` | GET | 404/404 | **PASS** | 0 |
| 64 | `/api/models/endpoints/probe-missing/models` | GET | 404/404 | **PASS** | 0 |
| 65 | `/api/queen/queen_growth/skills/administrative-behavior-skill/body` | GET | 200/200 | **PASS** | 0 |
| 66 | `/api/global/crm/status` | GET | 501/501 | **PASS** | 0 |
| 67 | `/api/global/data/tables` | GET | 401/401 | **PASS** | 0 |
| 68 | `/api/global/data/changes` | GET | 401/401 | **PASS** | 0 |
| 69 | `/api/global/data/foreign-keys` | GET | 401/401 | **PASS** | 0 |
| 70 | `/api/global/data/tables/probe_missing/rows` | GET | 401/401 | **PASS** | 0 |
| 71 | `/api/crm/grid/schema` | GET | 401/401 | **PASS** | 0 |
| 72 | `/api/crm/grid/account/probe-missing` | GET | 401/401 | **PASS** | 0 |
| 73 | `/api/colony/queen_growth/notifications` | GET | 404/404 | **PASS** | 0 |
| 74 | `/api/colony/queen_growth/tools` | GET | 404/404 | **PASS** | 0 |
| 75 | `/api/colonies/queen_growth/sessions` | GET | 200/200 | **PASS** | 0 |
| 76 | `/api/colonies/queen_growth/active-session` | GET | 200/200 | **PASS** | 0 |
| 77 | `/api/colonies/queen_growth/worker_profiles` | GET | 404/404 | **PASS** | 0 |
| 78 | `/api/colonies/queen_growth/data/tables` | GET | 200/200 | **PASS** | 0 |
| 79 | `/api/colonies/queen_growth/data/changes?since=0` | GET | 200/200 | **PASS** | 0 |
| 80 | `/api/colonies/queen_growth/data/tables/probe_missing/rows` | GET | 404/404 | **PASS** | 0 |
| 81 | `/api/colonies/import/status/probe-missing` | GET | 400/400 | **PASS** | 0 |
| 82 | `/api/sessions/{session}/upload-attachment` | POST | 200/200 | **PASS** | 0 |
| 83 | `/api/sessions/{session}/attachment/probe-note.txt` | GET | 200/200 | **PASS** | 0 |
| 84 | `/api/config/llm` | PUT | 200/200 | **PASS** | 0 |
| 85 | `/api/sessions` | POST | 201/201 | **PASS** | 0 |
| 86 | `/api/sessions/{session2}/title` | PATCH | 200/200 | **PASS** | 0 |
| 87 | `/api/sessions/{session2}/chat` | POST | 200/200 | **PASS** | 0 |
| 88 | `/api/sessions/{session2}` | DELETE | 200/200 | **PASS** | 0 |
| 89 | `/api/sessions/{session2}` | GET | 200/200 | **PASS** | 0 |
| 90 | `/api/sessions/session_20990101_000000_deadbeef/live_tools` | GET | 404/404 | **PASS** | 0 |
| 91 | `/api/sessions/session_20990101_000000_deadbeef/tasks` | GET | 404/404 | **PASS** | 0 |
| 92 | `/api/sessions/session_20990101_000000_deadbeef/triggers` | GET | 404/404 | **PASS** | 0 |
| 93 | `/api/sessions/session_20990101_000000_deadbeef/workers` | GET | 404/404 | **PASS** | 0 |
| 94 | `/api/sessions/session_20990101_000000_deadbeef/skill-installs` | GET | 404/404 | **PASS** | 0 |
| 95 | `/api/sessions/{session}/attachment/nope.txt` | GET | 404/404 | **PASS** | 0 |
| 96 | `/api/sessions/session_20990101_000000_deadbeef/upload-attachment` | POST | 404/404 | **PASS** | 0 |
| 97 | `/api/sessions/session_20990101_000000_deadbeef/chat` | POST | 404/404 | **PASS** | 0 |
| 98 | `/api/sessions/session_20990101_000000_deadbeef/title` | PATCH | 404/404 | **PASS** | 0 |
| 99 | `/api/sessions/session_20990101_000000_deadbeef` | DELETE | 404/404 | **PASS** | 0 |
| 100 | `/api/credentials/nope_probe` | GET | 404/404 | **PASS** | 0 |
| 101 | `/api/skills/does-not-exist-probe` | GET | 404/404 | **PASS** | 0 |
| 102 | `/api/queen/queen_nope/overview` | GET | 404/404 | **PASS** | 0 |
| 103 | `/api/mcp/servers/nope/health` | POST | 404/404 | **PASS** | 0 |
| 104 | `/api/sentinel/telegram/validate` | POST | 400/400 | **PASS** | 0 |
| 105 | `/api/credentials` | POST | 400/400 | **PASS** | 0 |
| 106 | `/api/config/llm` | PUT | 400/400 | **PASS** | 0 |
| 107 | `/api/config/llm` | PUT | 500/500 | **PASS** | 0 |

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

### 19. GET `/api/sessions/{session}/live_tools` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

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

### 23. GET `/api/sessions/{session}/workers/probe-missing` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='unknown worker: probe-missing' target='unknown worker: probe-missing'

### 24. GET `/api/sessions/{session}/workers/probe-missing/conversation` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='unknown worker: probe-missing' target='unknown worker: probe-missing'

### 25. GET `/api/claude-code-queens/queen_growth/conversations/probe-missing` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="No conversation 'probe-missing' of 'queen_growth'" target="No conversation 'probe-missing' of 'queen_growth'"

### 26. GET `/api/sessions/{session}/tasks` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 27. GET `/api/config/features` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 28. GET `/api/config/models` — DECLARED

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

### 29. GET `/api/config/profile` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 30. GET `/api/config/sentinel` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 31. GET `/api/config/rate-limits` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 32. GET `/api/config/llm-sections` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 33. GET `/api/config/external-skills` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 34. GET `/api/config/provider-library` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 35. GET `/api/credentials/oauth-status` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 36. GET `/api/sentinel/credentials` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 37. POST `/api/credentials` — PASS

- 状态码：reference `201` / target `201`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=201 target=201

### 38. GET `/api/credentials` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 39. GET `/api/credentials/airtable_pat` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 40. GET `/api/skills/scopes` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 41. GET `/api/skills/administrative-behavior-skill` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 42. GET `/api/queen/queen_growth/skills` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 43. GET `/api/mcp/servers` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 44. GET `/api/browser/status` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 45. GET `/api/browser/contexts` — DECLARED

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 口径备注：已声明差异（empty-state-divergence）：Python reports its in-process gcu lifecycle registry (available:true, count:0) while Go proxies the bridge's /contexts and reports the unavailable envelope (available:false, reason) when no bridge/extension is present. Both shapes are UI-handled; pinned here as a known empty-state difference.

| 字段 | proto 类型 | reference 实际 | target 实际 | JSON 可规范化 | 说明 |
|------|-----------|----------------|-------------|----------------|------|
| `count` | `undeclared (no message mapped for endpoint browser_contexts)` | `number(int)` | `absent` | yes (presence) | reference emits a field the target omits; proto optional captures absence, not a value |
| `reason` | `undeclared (no message mapped for endpoint browser_contexts)` | `absent` | `string` | yes | target extension: additive field; not part of the reference contract |

语义检查：
- ✓ status equal — reference=200 target=200

### 46. GET `/api/browser/tabs` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 47. GET `/api/colonies/tools-index` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 48. GET `/api/queen/removed` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 49. GET `/api/queen/queen_growth/overview` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 50. GET `/api/queen/queen_growth/profile` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 51. GET `/api/queen/queen_growth/llm` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 52. GET `/api/queen/queen_growth/persona` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 53. GET `/api/queen/queen_growth/soul` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 54. GET `/api/maintenance/janitor/report` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 55. GET `/api/prompts` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 56. GET `/api/memories` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 57. GET `/api/memories/file?path=probe-missing.md` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='Memory not found' target='Memory not found'

### 58. GET `/api/channels` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 59. GET `/api/claude-code-queens` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 60. GET `/api/discover` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 61. GET `/api/queen-mail/queens` — PASS

- 状态码：reference `401` / target `401`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=401 target=401
- ✓ expected failure status — expect=401 reference=401 target=401
- ✓ error message equal — reference='unauthorized' target='unauthorized'

### 62. GET `/api/config/profile/avatar` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='No avatar found' target='No avatar found'

### 63. GET `/api/queen/queen_growth/avatar` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='No avatar found' target='No avatar found'

### 64. GET `/api/models/endpoints/probe-missing/models` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="There's no endpoint 'probe-missing'" target="There's no endpoint 'probe-missing'"

### 65. GET `/api/queen/queen_growth/skills/administrative-behavior-skill/body` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 66. GET `/api/global/crm/status` — PASS

- 状态码：reference `501` / target `501`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=501 target=501
- ✓ expected failure status — expect=501 reference=501 target=501
- ✓ error message equal — reference='CRM is not available in this build' target='CRM is not available in this build'

### 67. GET `/api/global/data/tables` — PASS

- 状态码：reference `401` / target `401`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=401 target=401
- ✓ expected failure status — expect=401 reference=401 target=401
- ✓ error message equal — reference='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).' target='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).'

### 68. GET `/api/global/data/changes` — PASS

- 状态码：reference `401` / target `401`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=401 target=401
- ✓ expected failure status — expect=401 reference=401 target=401
- ✓ error message equal — reference='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).' target='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).'

### 69. GET `/api/global/data/foreign-keys` — PASS

- 状态码：reference `401` / target `401`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=401 target=401
- ✓ expected failure status — expect=401 reference=401 target=401
- ✓ error message equal — reference='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).' target='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).'

### 70. GET `/api/global/data/tables/probe_missing/rows` — PASS

- 状态码：reference `401` / target `401`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=401 target=401
- ✓ expected failure status — expect=401 reference=401 target=401
- ✓ error message equal — reference='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).' target='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).'

### 71. GET `/api/crm/grid/schema` — PASS

- 状态码：reference `401` / target `401`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=401 target=401
- ✓ expected failure status — expect=401 reference=401 target=401
- ✓ error message equal — reference='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).' target='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).'

### 72. GET `/api/crm/grid/account/probe-missing` — PASS

- 状态码：reference `401` / target `401`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=401 target=401
- ✓ expected failure status — expect=401 reference=401 target=401
- ✓ error message equal — reference='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).' target='Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).'

### 73. GET `/api/colony/queen_growth/notifications` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Colony 'queen_growth' not found" target="Colony 'queen_growth' not found"

### 74. GET `/api/colony/queen_growth/tools` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Colony 'queen_growth' not found" target="Colony 'queen_growth' not found"

### 75. GET `/api/colonies/queen_growth/sessions` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 76. GET `/api/colonies/queen_growth/active-session` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 77. GET `/api/colonies/queen_growth/worker_profiles` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="colony 'queen_growth' not found" target="colony 'queen_growth' not found"

### 78. GET `/api/colonies/queen_growth/data/tables` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 79. GET `/api/colonies/queen_growth/data/changes?since=0` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 80. GET `/api/colonies/queen_growth/data/tables/probe_missing/rows` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='no tracker.db' target='no tracker.db'

### 81. GET `/api/colonies/import/status/probe-missing` — PASS

- 状态码：reference `400` / target `400`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=400 target=400
- ✓ expected failure status — expect=400 reference=400 target=400
- ✓ error message equal — reference='invalid upload_id' target='invalid upload_id'

### 82. POST `/api/sessions/{session}/upload-attachment` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 83. GET `/api/sessions/{session}/attachment/probe-note.txt` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `text/plain` / target `text/plain`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 84. PUT `/api/config/llm` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 85. POST `/api/sessions` — PASS

- 状态码：reference `201` / target `201`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=201 target=201

### 86. PATCH `/api/sessions/{session2}/title` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 87. POST `/api/sessions/{session2}/chat` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 88. DELETE `/api/sessions/{session2}` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 89. GET `/api/sessions/{session2}` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=200 target=200

### 90. GET `/api/sessions/session_20990101_000000_deadbeef/live_tools` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="session '<session_id>' not found" target="session '<session_id>' not found"

### 91. GET `/api/sessions/session_20990101_000000_deadbeef/tasks` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Task list for session '<session_id>' not found" target="Task list for session '<session_id>' not found"

### 92. GET `/api/sessions/session_20990101_000000_deadbeef/triggers` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Session '<session_id>' not found" target="Session '<session_id>' not found"

### 93. GET `/api/sessions/session_20990101_000000_deadbeef/workers` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Session '<session_id>' not found" target="Session '<session_id>' not found"

### 94. GET `/api/sessions/session_20990101_000000_deadbeef/skill-installs` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='session not found' target='session not found'

### 95. GET `/api/sessions/{session}/attachment/nope.txt` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `text/plain; charset=utf-8` / target `text/plain; charset=utf-8`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='Attachment not found' target='Attachment not found'

### 96. POST `/api/sessions/session_20990101_000000_deadbeef/upload-attachment` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='session not found' target='session not found'

### 97. POST `/api/sessions/session_20990101_000000_deadbeef/chat` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Session '<session_id>' not found" target="Session '<session_id>' not found"

### 98. PATCH `/api/sessions/session_20990101_000000_deadbeef/title` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference='No such queen conversation' target='No such queen conversation'

### 99. DELETE `/api/sessions/session_20990101_000000_deadbeef` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Session '<session_id>' not found" target="Session '<session_id>' not found"

### 100. GET `/api/credentials/nope_probe` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Credential 'nope_probe' not found" target="Credential 'nope_probe' not found"

### 101. GET `/api/skills/does-not-exist-probe` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="skill 'does-not-exist-probe' not found" target="skill 'does-not-exist-probe' not found"

### 102. GET `/api/queen/queen_nope/overview` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Queen 'queen_nope' not found" target="Queen 'queen_nope' not found"

### 103. POST `/api/mcp/servers/nope/health` — PASS

- 状态码：reference `404` / target `404`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=404 target=404
- ✓ expected failure status — expect=404 reference=404 target=404
- ✓ error message equal — reference="Cannot health-check server 'nope'" target="Cannot health-check server 'nope'"

### 104. POST `/api/sentinel/telegram/validate` — PASS

- 状态码：reference `400` / target `400`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=400 target=400
- ✓ expected failure status — expect=400 reference=400 target=400
- ✓ error message equal — reference='no token provided or saved' target='no token provided or saved'

### 105. POST `/api/credentials` — PASS

- 状态码：reference `400` / target `400`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=400 target=400
- ✓ expected failure status — expect=400 reference=400 target=400
- ✓ error message equal — reference='credential_id and keys are required' target='credential_id and keys are required'

### 106. PUT `/api/config/llm` — PASS

- 状态码：reference `400` / target `400`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status equal — reference=400 target=400
- ✓ expected failure status — expect=400 reference=400 target=400
- ✓ error message equal — reference='Invalid JSON body' target='Invalid JSON body'

### 107. PUT `/api/config/llm` — PASS

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
