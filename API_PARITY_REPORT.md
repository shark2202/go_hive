# Go hive vs Python hive — API 行为对比报告

- 生成时间：2026-11-19T10:07:27+08:00
- 仓库：`/Users/mac/hive/.wt/w14-api-parity`
- 分支 / 提交：`w14/api-parity` / `b7404b97`
- Python hive：`hive 1.0.14`，端口 `58078`，HIVE_HOME `/var/folders/fx/_9b3wvr91jq2f28pmwr7p7sc0000gn/T/hive-parity-45510/python-home`
- Go hive：`hive 1.0.14-go.1`，端口 `58079`，HIVE_HOME `/var/folders/fx/_9b3wvr91jq2f28pmwr7p7sc0000gn/T/hive-parity-45510/go-home`
- LLM：provider=`openai` model=`deepseek-flash-1017` api_base=`https://token.tokenbroad.com/v1` key_env=`BROADTOKEN_API_KEY`
- 启动耗时：Python 2.55s / Go 0.27s

**端点对比：9/17 PASS，8 FAIL。**
**流程对比：FAIL**（3/5 项通过）

> 对比规则：状态码必须相同；JSON 键集合与类型（递归，数组取前 3 个元素）必须相同；
> 动态值（id、时间戳、端口等）不参与比较。`额外字段` 记入差异但仍判 FAIL，
> 因为本报告服务于后续修复，需要看到完整缺口。

## 汇总

| # | 端点 | 方法 | 状态码 (Py/Go) | 结果 | 差异数 |
|---|------|------|----------------|------|--------|
| 1 | `/api/health` | GET | 200/200 | **PASS** | 0 |
| 2 | `/api/config/llm` | GET | 200/200 | **PASS** | 0 |
| 3 | `/api/credentials` | GET | 200/200 | **PASS** | 0 |
| 4 | `/api/credentials/specs` | GET | 200/200 | **FAIL** | 2 |
| 5 | `/api/queen/profiles` | GET | 200/200 | **PASS** | 0 |
| 6 | `/api/models` | GET | 404/404 | **PASS** | 0 |
| 7 | `/api/models/overview` | GET | 200/200 | **PASS** | 0 |
| 8 | `/api/org-chart` | GET | 404/404 | **PASS** | 0 |
| 9 | `/api/skills` | GET | 200/404 | **FAIL** | 2 |
| 10 | `/api/mcp/servers` | GET | 200/200 | **PASS** | 0 |
| 11 | `/api/sessions` | POST | 201/201 | **FAIL** | 4 |
| 12 | `/api/sessions` | GET | 200/200 | **FAIL** | 2 |
| 13 | `/api/sessions/{id}` | GET | 200/200 | **FAIL** | 4 |
| 14 | `/api/sessions/{id}/events/history` | GET | 200/200 | **FAIL** | 18 |
| 15 | `/api/sessions/{id}/snapshot` | GET | 200/200 | **FAIL** | 6 |
| 16 | `/api/sessions/{id}/stats` | GET | 200/200 | **FAIL** | 29 |
| 17 | `/api/sessions/{id}/tasks` | GET | 404/404 | **PASS** | 0 |

## 端点明细

### 1. GET `/api/health` — PASS

- 状态码：Python `200` / Go `200`
- ✓ status == 'ok' — python='ok' go='ok'

### 2. GET `/api/config/llm` — PASS

- 状态码：Python `200` / Go `200`
- ✓ provider 相同 — python='openai' go='openai'
- ✓ model 相同 — python='deepseek-flash-1017' go='deepseek-flash-1017'
- ✓ has_api_key 两边都为 true — python=True go=True

### 3. GET `/api/credentials` — PASS

- 状态码：Python `200` / Go `200`
- ✓ credentials 是数组
- ✓ credential 数量相同 — python=0 go=0

### 4. GET `/api/credentials/specs` — FAIL

- 状态码：Python `200` / Go `200`
- ✗ specs 数量相同 — python=115 go=4
- ✗ credential_id 集合相同 — python 独有 115 个：aden_api_key, airtable_pat, apify, apollo, asana, attio, aws_access_key, aws_secret_key

<details><summary>响应摘要</summary>

Python：`{"accounts_by_provider": {}, "has_aden_key": false, "specs": [{"aden_supported": true, "api_key_instructions": "1. Go to hive.adenhq.com\n2. Open Settings > Developers\n3. Copy your API key", "available": false, "credent…`

Go：`{"accounts_by_provider": {}, "has_aden_key": false, "specs": [{"accounts": [], "aden_supported": false, "api_key_instructions": "", "available": false, "credential_group": "", "credential_id": "openai", "credential_key":…`

</details>

### 5. GET `/api/queen/profiles` — PASS

- 状态码：Python `200` / Go `200`
- ✓ queen 数量相同 — python=13 go=13
- ✓ queen id 集合相同

### 6. GET `/api/models` — PASS

- 状态码：Python `404` / Go `404`
- 备注：两边都没有 /api/models，真实端点名为 /api/models/overview
- ✓ 两边都未实现 /api/models — python=404 go=404

### 7. GET `/api/models/overview` — PASS

- 状态码：Python `200` / Go `200`
- ✓ llm 槽位 provider/model 相同 — python={'source': 'catalog', 'provider': 'openai', 'model': 'deepseek-flash-1017', 'endpoint': None, 'subscription': None, 'api_base': 'https://token.tokenbroad.com/v1', 'max_context_tokens': None, 'has_extra_body': False} go={'api_base': 'https://token.tokenbroad.com/v1', 'endpoint': None, 'has_extra_body': False, 'max_context_tokens': None, 'model': 'deepseek-flash-1017', 'provider': 'openai', 'source': 'catalog', 'subscription': None}

### 8. GET `/api/org-chart` — PASS

- 状态码：Python `404` / Go `404`
- ✓ 两边都未实现 /api/org-chart — python=404 go=404

### 9. GET `/api/skills` — FAIL

- 状态码：Python `200` / Go `404`
- 备注：go body: non-JSON body — '404 page not found\n'
- ✗ skill 数量相同 — python=195 go=0

<details><summary>响应摘要</summary>

Python：`{"colonies": [], "queens": [], "skills": [{"deletable": false, "description": "管理行为（赫伯特·西蒙《Administrative Behavior》，诺贝尔奖作品）。当用户要理解\"有限理性\"与\"满意准则\"（管理人 vs 经济人）、要把决策拆成\"事实要素与价值要素\"（手段-目的链）、要理解决策的层级系统、要用\"诱因-贡献\"分析组织均衡、要理解…`

Go：`<non-JSON or empty>`

</details>

### 10. GET `/api/mcp/servers` — PASS

- 状态码：Python `200` / Go `200`
- ✓ server 名称集合相同 — python=5 go=5

### 11. POST `/api/sessions` — FAIL

- 状态码：Python `201` / Go `201`
- 类型不一致（2）：queen_id: python=string go=null; queen_name: python=string go=null
- ✓ 两边都创建成功 (201) — python=201 go=201
- ✓ 响应都带 session_id — python='session_20261119_100634_92b6e343' go='session_20261119_100635_9bb6fb4f'
- ✗ queen 身份已绑定 (queen_id 非空) — python='queen_growth' go=None
- ✗ queen_phase 相同 — python='independent' go='planning'

<details><summary>响应摘要</summary>

Python：`{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "goal": "", "has_worker": false, "loaded_at": 1795053994.058719, "node_count": 0, "queen_id": "queen_growth", "queen_name": "Steve Jobs", …`

Go：`{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "goal": "", "has_worker": false, "loaded_at": 1795053995.147253, "node_count": 0, "queen_id": null, "queen_name": null, "queen_phase": "pl…`

</details>

### 12. GET `/api/sessions` — FAIL

- 状态码：Python `200` / Go `200`
- 类型不一致（2）：sessions[].queen_id: python=string go=null; sessions[].queen_name: python=string go=null
- ✓ 两边列表都包含刚建的会话 — python=1 go=1

<details><summary>响应摘要</summary>

Python：`{"sessions": [{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "goal": "", "has_worker": false, "loaded_at": 1795053994.058719, "node_count": 0, "queen_id": "queen_growth", "queen_name": …`

Go：`{"sessions": [{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "goal": "", "has_worker": false, "loaded_at": 1795053995.147253, "node_count": 0, "queen_id": null, "queen_name": null, "que…`

</details>

### 13. GET `/api/sessions/{id}` — FAIL

- 状态码：Python `200` / Go `200`
- 类型不一致（2）：queen_id: python=string go=null; queen_name: python=string go=null
- ✓ session_id 回显正确 — python='session_20261119_100634_92b6e343' go='session_20261119_100635_9bb6fb4f'
- ✗ queen_phase 相同 — python='independent' go='planning'
- ✗ queen_id 相同 — python='queen_growth' go=None

<details><summary>响应摘要</summary>

Python：`{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "entry_points": [], "goal": "", "has_worker": false, "loaded_at": 1795053994.058719, "node_count": 0, "queen_id": "queen_growth", "queen_n…`

Go：`{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "entry_points": [], "goal": "", "has_worker": false, "loaded_at": 1795053995.147253, "node_count": 0, "queen_id": null, "queen_name": null…`

</details>

### 14. GET `/api/sessions/{id}/events/history` — FAIL

- 状态码：Python `200` / Go `200`
- 仅 Python 有的字段（17）：`events[].colony_id (null)`, `events[].correlation_id (null)`, `events[].data (object)`, `events[].data.detail (string)`, `events[].data.max_iterations (number)`, `events[].data.meta (object)`, `events[].data.meta.point (string)`, `events[].data.name (string)`, `events[].data.queen_id (string)`, `events[].data.source (string)`, `events[].data.title (string)`, `events[].execution_id (string)` … 共 17 个
- ✗ 历史事件非空（turn 之后） — python total=18 go total=0
- ✓ events 都是数组

<details><summary>响应摘要</summary>

Python：`{"events": [{"colony_id": null, "correlation_id": null, "data": {"name": "Steve Jobs", "queen_id": "queen_growth", "title": "Head of Growth"}, "execution_id": null, "node_id": null, "seq": 1, "stream_id": "queen", "times…`

Go：`{"events": [], "has_more_older": false, "limit": 500, "returned": 0, "session_id": "session_20261119_100635_9bb6fb4f", "start_index": 0, "start_offset": 0, "total": 0, "truncated": false}`

</details>

### 15. GET `/api/sessions/{id}/snapshot` — FAIL

- 状态码：Python `200` / Go `200`
- 类型不一致（4）：activity: python=string go=null; last_event_seq: python=number go=null; park_reason: python=string go=null; queen_busy_reason: python=string go=null
- ✗ activity 相同 — python='awaiting_user' go=None
- ✗ awaiting_input 相同 — python=True go=False

<details><summary>响应摘要</summary>

Python：`{"activity": "awaiting_user", "awaiting_input": true, "current_execution_id": null, "current_tool_calls": [], "interrupt_cause": null, "interrupted": false, "is_executing": false, "last_event_at": "2026-11-19T10:06:41.02…`

Go：`{"activity": null, "awaiting_input": false, "current_execution_id": null, "current_tool_calls": [], "interrupt_cause": null, "interrupted": false, "is_executing": false, "last_event_at": "2026-11-19T10:06:36.165437", "la…`

</details>

### 16. GET `/api/sessions/{id}/stats` — FAIL

- 状态码：Python `200` / Go `200`
- 仅 Python 有的字段（28）：`active_workers (number)`, `adaptive_tool_budget (object)`, `adaptive_tool_budget.enabled (bool)`, `adaptive_tool_budget.nominal (null)`, `adaptive_tool_budget.samples (number)`, `colony_id (string)`, `event_bus (object)`, `event_bus.events_by_type (object)`, `event_bus.events_by_type.client_input_committed (number)`, `event_bus.events_by_type.client_input_received (number)`, `event_bus.events_by_type.client_input_requested (number)`, `event_bus.events_by_type.client_output_delta (number)` … 共 28 个
- ✗ stats 都非空 — python keys=['active_workers', 'adaptive_tool_budget', 'colony_id', 'event_bus', 'running', 'total_workers'] go keys=[]

<details><summary>响应摘要</summary>

Python：`{"active_workers": 0, "adaptive_tool_budget": {"enabled": true, "nominal": null, "samples": 0}, "colony_id": "session_20261119_100634_92b6e343", "event_bus": {"events_by_type": {"client_input_committed": 1, "client_input…`

Go：`{}`

</details>

### 17. GET `/api/sessions/{id}/tasks` — PASS

- 状态码：Python `404` / Go `404`
- 备注：两边都返回 404（该会话尚无任务列表），属预期一致
- ✓ tasks 都是数组

## 流程对比（创建会话 → /chat → 落盘 → SSE）

- 提示词：`Reply with exactly: PARITY_OK`
- POST `/api/sessions/{id}/chat`：Python `200` / Go `200`
- assistant 落盘 part 数：Python 2 / Go 1
- 回复文本：Python `PARITY_OK` / Go `PARITY_OK`
- assistant part 字段：Python `['content', 'reasoning_content', 'role', 'seq']` / Go `['content', 'role', 'seq']`

- ✓ chat 都返回 200 — python=200 go=200
- ✓ 两边都落盘了 assistant part — python=True go=True
- ✓ assistant 回复包含 PARITY_OK — python='PARITY_OK' go='PARITY_OK'
- ✗ SSE 事件类型集合相同 — python=['client_input_committed', 'client_input_received', 'client_input_requested', 'client_output_delta', 'client_reasoning', 'context_usage_updated', 'llm_reasoning_delta', 'llm_turn_complete', 'loop_state_changed', 'node_loop_iteration', 'node_loop_started', 'session_snapshot', 'session_title_updated'] go=['client_input_received']
- ✗ SSE 帧都带 event: 名称（或都不带） — python event 名称 0 个，go 1 个

### SSE（各订阅 10 秒）

**Python 事件 type 序列（去重保序）：** `session_snapshot → node_loop_started → node_loop_iteration → context_usage_updated → client_input_received → llm_reasoning_delta → client_reasoning → client_output_delta → llm_turn_complete → client_input_committed → session_title_updated → client_input_requested → loop_state_changed`

**Go 事件 type 序列（去重保序）：** `client_input_received`

- 帧数：Python 50 / Go 1
- SSE `event:` 名称：Python `[]` / Go `['client_input_received']`
- 仅 Python 有的事件类型：`['client_input_committed', 'client_input_requested', 'client_output_delta', 'client_reasoning', 'context_usage_updated', 'llm_reasoning_delta', 'llm_turn_complete', 'loop_state_changed', 'node_loop_iteration', 'node_loop_started', 'session_snapshot', 'session_title_updated']`
- 仅 Go 有的事件类型：`[]`

## 场景对比：重启后打开已不在内存的会话

模拟前端：先用返回的 session id 打开会话页，然后重启 serve（同端口、同 HIVE_HOME），再用旧 id 订阅 `/events`；若 `/events` 非 200，就像页面那样再 POST 一次 `/api/sessions`，持续 10 秒（POST 上限 30 次）。

| 指标 | Python | Go |
|------|--------|----|
| 重启耗时 | 2.28s | 0.26s |
| GET `/api/sessions/{旧 id}` | 200 | 200 |
| 旧会话出现在 `/api/sessions` 列表 | False | False |
| `/events` 首次状态码 | 404 | 404 |
| `/events` 状态码集合 | [404] | [404] |
| 10 秒内 POST `/api/sessions` 次数 | ≥30（达上限） | ≥30（达上限） |
| POST 状态码集合 | [201] | [201] |
| POST 耗时 min/中位/max | 110.7/117.7/833.2ms | 0.9/1.2/49.7ms |
| 按中位耗时折算的创建速率 | ≈8/s | ≈833/s |

> POST 计数上限 30 次：达到上限的一侧记 `≥30`，真实速率看折算值。

**结论：** 两边一致：重启后旧 session id 的 `/events` 都返回 404（会话详情 `GET /api/sessions/{id}` 两边都能从磁盘回退返回 200/200），因此前端重试风暴不是 Go 独有，上游同样触发。差别在放大器：POST /api/sessions 中位耗时 Python 117.7ms（110.7–833.2ms）、Go 1.2ms（0.9–49.7ms），约 98×，同样的重试循环在 Go 上 10 秒内至少产生 30 次创建请求（达到测量上限）。

## 最重要的 5 个差异

1. GET /api/skills 状态码不一致：Python 200 / Go 404
2. SSE 事件序列：Go 少发 client_input_committed, client_input_requested, client_output_delta, client_reasoning, context_usage_updated, llm_reasoning_delta, llm_turn_complete, loop_state_changed 等 12 种事件类型
3. GET /api/sessions/{id}/stats — stats 都非空：python keys=['active_workers', 'adaptive_tool_budget', 'colony_id', 'event_bus', 'running', 'total_workers'] go keys=[]
4. GET /api/sessions/{id}/events/history — 历史事件非空（turn 之后）：python total=18 go total=0
5. GET /api/credentials/specs — specs 数量相同：python=115 go=4

## 复现

```bash
uv run python go_hive/scripts/api_parity.py
```

脚本每次自行挑选空闲端口、创建临时 HIVE_HOME、拉起两侧 serve，结束后清理（`--keep` 可保留）。
