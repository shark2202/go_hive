# Go hive vs Python hive — API 行为对比报告

- 生成时间：2026-11-19T10:34:37+08:00
- 仓库：`/Users/mac/hive/.wt/w14-api-parity`
- 分支 / 提交：`w14/api-parity` / `81b509ba`
- Python hive：`hive 1.0.14`，端口 `58222`，HIVE_HOME `/var/folders/fx/_9b3wvr91jq2f28pmwr7p7sc0000gn/T/hive-parity-54509/python-home`
- Go hive：`hive 1.0.14-go.1`，端口 `58223`，HIVE_HOME `/var/folders/fx/_9b3wvr91jq2f28pmwr7p7sc0000gn/T/hive-parity-54509/go-home`
- LLM：provider=`openai` model=`deepseek-flash-1017` api_base=`https://token.tokenbroad.com/v1` key_env=`BROADTOKEN_API_KEY`
- 启动耗时：Python 6.86s / Go 0.26s
- 阶段耗时：静态端点 0.27s → 创建会话 0.96s → 对话流程 40.0s → 会话端点 0.01s → 重启场景 22.71s
- 进程存活：Python 正常 / Go 正常

**端点对比：10/18 PASS，8 FAIL。**
**流程对比：FAIL**（3/5 项通过）

> 对比规则：状态码必须相同；JSON 键集合与类型（递归，数组取前 3 个元素）必须相同；
> 动态值（id、时间戳、端口等）不参与比较。`额外字段` 记入差异但仍判 FAIL，
> 因为本报告服务于后续修复，需要看到完整缺口。

## 结论

- 端点契约 10/18 通过：静态/配置类端点基本对齐（health、config、credentials、queen profiles、models、mcp 全部 PASS）；FAIL 集中在会话运行时与两个目录端点。
- 会话运行时的 4 个 FAIL（创建/列表/详情、history、snapshot、stats）落在同一簇缺口：Go 的 chat 会话没有绑定 queen（`queen_id=null`），`ChatRuntime` 不发布事件、也不写 `events.jsonl`，所以历史、快照、统计只能为空。
- 两个目录缺口：`/api/skills` 在 Go 里根本未注册（404），`/api/credentials/specs` 只有 4 项 vs Python 115 项。
- 对话本身可用：两边 `/chat` 都 200、都落盘 `PARITY_OK`；但 Go 的 SSE 只发 1 帧（1 种 type），Python 发 105 帧（13 种 type）。
- 重试风暴：重启后旧 id 的 `/events` 两边都 404（上游同构触发）；但 `queen_resume_from` 恢复只有 Python 生效（Python 1 次 POST 后恢复，Go 10 次仍 404），且 Go 创建会话快约 42×，所以同一页面只在 Go 上持续高频重试。

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
| 9 | `/api/skills` | GET | 200/404 | **FAIL** | 3 |
| 10 | `/api/mcp/servers` | GET | 200/200 | **PASS** | 0 |
| 11 | `/api/sessions` | POST | 201/201 | **FAIL** | 4 |
| 12 | `/api/sessions` | GET | 200/200 | **FAIL** | 2 |
| 13 | `/api/sessions/{id}` | GET | 200/200 | **FAIL** | 4 |
| 14 | `/api/sessions/{id}/events` | GET | 200/200 | **PASS** | 0 |
| 15 | `/api/sessions/{id}/events/history` | GET | 200/200 | **FAIL** | 1 |
| 16 | `/api/sessions/{id}/snapshot` | GET | 200/200 | **FAIL** | 6 |
| 17 | `/api/sessions/{id}/stats` | GET | 200/200 | **FAIL** | 29 |
| 18 | `/api/sessions/{id}/tasks` | GET | 404/404 | **PASS** | 0 |

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
- ✓ 响应都带 session_id — python='session_20261119_103333_190a547e' go='session_20261119_103334_48de19e6'
- ✗ queen 身份已绑定 (queen_id 非空) — python='queen_growth' go=None
- ✗ queen_phase 相同 — python='independent' go='planning'

<details><summary>响应摘要</summary>

Python：`{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "goal": "", "has_worker": false, "loaded_at": 1795055613.462124, "node_count": 0, "queen_id": "queen_growth", "queen_name": "Steve Jobs", …`

Go：`{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "goal": "", "has_worker": false, "loaded_at": 1795055614.412668, "node_count": 0, "queen_id": null, "queen_name": null, "queen_phase": "pl…`

</details>

### 12. GET `/api/sessions` — FAIL

- 状态码：Python `200` / Go `200`
- 类型不一致（2）：sessions[].queen_id: python=string go=null; sessions[].queen_name: python=string go=null
- ✓ 两边列表都包含刚建的会话 — python=1 go=1

<details><summary>响应摘要</summary>

Python：`{"sessions": [{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "goal": "", "has_worker": false, "loaded_at": 1795055613.462124, "node_count": 0, "queen_id": "queen_growth", "queen_name": …`

Go：`{"sessions": [{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "goal": "", "has_worker": false, "loaded_at": 1795055614.412668, "node_count": 0, "queen_id": null, "queen_name": null, "que…`

</details>

### 13. GET `/api/sessions/{id}` — FAIL

- 状态码：Python `200` / Go `200`
- 类型不一致（2）：queen_id: python=string go=null; queen_name: python=string go=null
- ✓ session_id 回显正确 — python='session_20261119_103333_190a547e' go='session_20261119_103334_48de19e6'
- ✗ queen_phase 相同 — python='independent' go='planning'
- ✗ queen_id 相同 — python='queen_growth' go=None

<details><summary>响应摘要</summary>

Python：`{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "entry_points": [], "goal": "", "has_worker": false, "loaded_at": 1795055613.462124, "node_count": 0, "queen_id": "queen_growth", "queen_n…`

Go：`{"agent_path": "", "colony_id": null, "colony_spawned": false, "description": "", "entry_points": [], "goal": "", "has_worker": false, "loaded_at": 1795055614.412668, "node_count": 0, "queen_id": null, "queen_name": null…`

</details>

### 14. GET `/api/sessions/{id}/events` — PASS

- 状态码：Python `200` / Go `200`
- ✓ 都返回 200 — python=200 go=200
- ✓ Content-Type 都声明 text/event-stream — python='text/event-stream' go='text/event-stream'

### 15. GET `/api/sessions/{id}/events/history` — FAIL

- 状态码：Python `200` / Go `200`
- ✗ 历史事件非空（turn 之后） — python total=19 go total=0
- ✓ events 都是数组

<details><summary>响应摘要</summary>

Python：`{"events": [{"colony_id": null, "correlation_id": null, "data": {"name": "Steve Jobs", "queen_id": "queen_growth", "title": "Head of Growth"}, "execution_id": null, "node_id": null, "seq": 1, "stream_id": "queen", "times…`

Go：`{"events": [], "has_more_older": false, "limit": 500, "returned": 0, "session_id": "session_20261119_103334_48de19e6", "start_index": 0, "start_offset": 0, "total": 0, "truncated": false}`

</details>

### 16. GET `/api/sessions/{id}/snapshot` — FAIL

- 状态码：Python `200` / Go `200`
- 类型不一致（4）：activity: python=string go=null; last_event_seq: python=number go=null; park_reason: python=string go=null; queen_busy_reason: python=string go=null
- ✗ activity 相同 — python='awaiting_user' go=None
- ✗ awaiting_input 相同 — python=True go=False

<details><summary>响应摘要</summary>

Python：`{"activity": "awaiting_user", "awaiting_input": true, "current_execution_id": null, "current_tool_calls": [], "interrupt_cause": null, "interrupted": false, "is_executing": false, "last_event_at": "2026-11-19T10:33:40.60…`

Go：`{"activity": null, "awaiting_input": false, "current_execution_id": null, "current_tool_calls": [], "interrupt_cause": null, "interrupted": false, "is_executing": false, "last_event_at": "2026-11-19T10:33:35.429025", "la…`

</details>

### 17. GET `/api/sessions/{id}/stats` — FAIL

- 状态码：Python `200` / Go `200`
- 仅 Python 有的字段（28）：`active_workers (number)`, `adaptive_tool_budget (object)`, `adaptive_tool_budget.enabled (bool)`, `adaptive_tool_budget.nominal (null)`, `adaptive_tool_budget.samples (number)`, `colony_id (string)`, `event_bus (object)`, `event_bus.events_by_type (object)`, `event_bus.events_by_type.client_input_committed (number)`, `event_bus.events_by_type.client_input_received (number)`, `event_bus.events_by_type.client_input_requested (number)`, `event_bus.events_by_type.client_output_delta (number)` … 共 28 个
- ✗ stats 都非空 — python keys=['active_workers', 'adaptive_tool_budget', 'colony_id', 'event_bus', 'running', 'total_workers'] go keys=[]

<details><summary>响应摘要</summary>

Python：`{"active_workers": 0, "adaptive_tool_budget": {"enabled": true, "nominal": null, "samples": 0}, "colony_id": "session_20261119_103333_190a547e", "event_bus": {"events_by_type": {"client_input_committed": 1, "client_input…`

Go：`{}`

</details>

### 18. GET `/api/sessions/{id}/tasks` — PASS

- 状态码：Python `404` / Go `404`
- 备注：状态码 Python 404 / Go 404（该会话尚无任务列表，两边同为 404，属预期一致）
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

> 注：Python 额外落盘的 assistant part 是会话创建时的开场白；`event:` 名称差异属于线格式（Python 只发 `data:`，Go 额外带 `event:` 名），当前前端 `api/client.ts::subscribeSse` 会解析但 `onEvent` 不消费事件名，所以不影响现有 UI。

### SSE（各订阅 10 秒）

**Python 事件 type 序列（去重保序）：** `session_snapshot → node_loop_started → node_loop_iteration → context_usage_updated → client_input_received → llm_reasoning_delta → client_reasoning → client_output_delta → llm_turn_complete → client_input_committed → session_title_updated → client_input_requested → loop_state_changed`

**Go 事件 type 序列（去重保序）：** `client_input_received`

- 帧数：Python 105 / Go 1
- 完整事件 type 序列（按到达顺序，最多 80 帧）：Python `session_snapshot → node_loop_started → node_loop_iteration → context_usage_updated → client_input_received → llm_reasoning_delta → llm_reasoning_delta → client_reasoning → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → client_output_delta → …（共 105 帧）`
- 完整事件 type 序列（按到达顺序，最多 80 帧）：Go `client_input_received`
- SSE `event:` 名称：Python `[]` / Go `['client_input_received']`
- 仅 Python 有的事件类型：`['client_input_committed', 'client_input_requested', 'client_output_delta', 'client_reasoning', 'context_usage_updated', 'llm_reasoning_delta', 'llm_turn_complete', 'loop_state_changed', 'node_loop_iteration', 'node_loop_started', 'session_snapshot', 'session_title_updated']`
- 仅 Go 有的事件类型：`[]`

## 场景对比：重启后打开已不在内存的会话

模拟前端：先用返回的 session id 打开会话页，然后重启 serve（同端口、同 HIVE_HOME），再用旧 id 订阅 `/events`；若 `/events` 非 200，就像页面那样再 POST 一次 `/api/sessions`。两种请求体各测 10 秒：`blank` = `{}`（新建一个无关会话），`resume` = `{"queen_resume_from": 旧 id}`（`pages/queen-dm.tsx` 的 `onSessionGone` 实际发的恢复请求）。

| 指标 | Python | Go |
|------|--------|----|
| 重启耗时 | 13.23s | 0.26s |
| GET `/api/sessions/{旧 id}` | 200 | 200 |
| 旧会话出现在 `/api/sessions` 列表 | False | False |
| `/events` 首次状态码 | 404 | 404 |
| `/events` 状态码集合（blank 循环） | [404] | [404] |
| blank：POST 次数 | ≥30（达上限） | ≥30（达上限） |
| blank：POST 状态码集合 | [201] | [201] |
| blank：POST 耗时 min/中位/max | 115.2/133.9/924.1ms | 1.0/3.2/47.9ms |
| blank：按中位耗时折算的创建速率 | ≈7/s | ≈312/s |
| resume：POST 次数 | 1 | ≥10（达上限） |
| resume：POST 后旧 id 的 `/events` | 200 | 404 |
| resume：旧 id 是否恢复可订阅 | True | False |
| resume：POST 返回的 session id 是否复用旧 id | True | False |
| resume：产生的不同 session id 数 | 1 | 10 |
| resume：POST 耗时 min/中位/max | 172.9/172.9/172.9ms | 1.1/1.8/3.8ms |

> blank 循环 POST 计数上限 30 次：达到上限的一侧记 `≥30`，真实速率看折算值。

**结论：** 前端真实的自愈路径（POST `queen_resume_from=<旧 id>`）只有 Python 能恢复：Python 第 1 次 POST 后旧 id 的 `/events` 回到 200，循环自行停止；Go 在 10s 窗口内试了 10 次（上限 10），旧 id 始终 404，因此同一个页面在 Go 上会持续重试。空创建（POST `{}`）两边都陷入循环：Python 30 次 / Go 30 次（10s 窗口，上限 30），中位耗时 Python 133.9ms / Go 3.2ms，约 42×。结论：404 本身是上游同构行为，但「恢复后能否继续流」是 Go 独有缺口，这才是重试风暴在 Go 上放大的根因；Go 每次 POST 又快 42×，把同一个循环推得更高频。

## 磁盘证据（空响应的根因）

| 证据 | Python | Go |
|------|--------|----|
| `events.jsonl` 文件数 | 31 | 0 |
| `events.jsonl` 总行数（前 50 个文件） | 256 | 0 |
| `queens/` 下的身份目录 | `['queen_growth']` | `['default']` |
| 会话目录数 | 31 | 1 |

Go 的 HIVE_HOME 里没有任何 `events.jsonl`，`queens/` 只有一个 `default`，因此 `events/history` 只能返回空、`snapshot` 只能给出 null、`stats` 只能给出 `{}` —— 不是序列化差异，是这些数据从未被写入。

### 代码定位

- **GET /api/skills → 404** — Go 没有注册 `/api/skills` 路由（`internal/server` 全目录 grep 无命中）；Python `routes_skills.py` 返回 195 条。
- **创建会话 `queen_id=null`、`queen_phase=planning`** — `internal/server/routes_sessions.go::sessionToLiveDict` 的 queen 字段来自 `SessionPhaseState`；chat 会话没有绑定 profile，Python 则落到 `queens/queen_growth`。
- **`events/history` total=0、SSE 只有 1 帧、`snapshot` 全空** — `internal/server/chat_runtime.go` 的 `ChatRuntime` 完全不引用 `EventBus`；`eventbus.SetSessionLog` 在 `internal/` 与 `cmd/` 均无调用点，所以磁盘上不会出现 `events.jsonl`，history 路由只读该文件。
- **`stats` 恒为 `{}`** — `internal/server/routes_session_detail.go::handleSessionStats` 只在 `session.Colony != nil` 时返回统计，queen-only 会话直接写空对象。
- **`credentials/specs` 4 vs 115** — `internal/server/app.go::credentialSpecsFromCatalog` 由模型目录派生，只覆盖 4 个带 API-key 环境变量的 provider；Python 的 spec 目录有 115 项（含 aden/tools/instructions）。
- **`queen_resume_from` 不能恢复旧 id** — Python：`core/framework/server/session_manager.py:745` `resolved_session_id = queen_resume_from or session_id`；Go：`internal/cli/loader.go:461-467` 只认 `params.SessionID`，其余情况新铸 id，`QueenResumeFrom` 只决定存储目录。

## 最重要的 5 个差异

1. GET /api/skills 状态码不一致：Python 200 / Go 404
2. 重启后 `queen_resume_from` 自愈：Python 恢复 / Go 未恢复（旧 id 的 /events 最终 Python 200 / Go 404）
3. POST /api/sessions — queen 身份已绑定 (queen_id 非空)：python='queen_growth' go=None
4. SSE 事件序列：Go 少发 client_input_committed, client_input_requested, client_output_delta, client_reasoning, context_usage_updated, llm_reasoning_delta, llm_turn_complete, loop_state_changed 等 12 种事件类型
5. GET /api/sessions/{id}/stats — stats 都非空：python keys=['active_workers', 'adaptive_tool_budget', 'colony_id', 'event_bus', 'running', 'total_workers'] go keys=[]

## 复现

```bash
uv run python go_hive/scripts/api_parity.py
```

脚本每次自行挑选空闲端口、创建临时 HIVE_HOME、拉起两侧 serve，结束后清理（`--keep` 可保留）。
