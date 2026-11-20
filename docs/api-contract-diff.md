# API 契约差异（reference=python → target=go_hive）

- 生成时间：2026-11-21T00:58:55+08:00
- 基准标识：`reference=python@445494a3033c3036eaea044505b838d101257b56`，`target=go_hive@445494a3033c3036eaea044505b838d101257b56`
- reference（契约基准）：`python` hive 1.0.14，
  HEAD `445494a3033c3036eaea044505b838d101257b56`，`core` 最后改动 `a756b1e51765bc75b0a40e362859452a0a54081b`，`tools` 最后改动 `68b6c230f20e237953dea7a3af7bb583b9fbe0fd`
- target（待对齐）：`go_hive` hive 1.0.26-go.1，HEAD `445494a3033c3036eaea044505b838d101257b56`
- 探针输入：queen=`queen_growth`；LLM 槽位指向本地 OpenAI 兼容 stub（`http://127.0.0.1:59422/v1`，确定性开场回合，不访问外网），SSE 窗口 3.0s
- 产物：`go_hive/contracts/api-responses/`（canonical JSON + shape + diff.json）

> 口径：契约基准是 Python 当前 checkout 的抓取结果，所有字段名/类型/数量以 reference 为准；
> Go 侧只作为 target 量化差异。易变值（session id、时间戳、端口、临时路径、资源读数）已替换为占位符，
> 但键的存在性（null vs 缺失）保留。

**端点：14/14 PASS，0 FAIL。**
**差异计数：missing_in_target=0，extra_in_target=0，type_mismatch=0，count_mismatch=0，semantic_failures=0。**

## 路由面（静态提取）

- reference=217 条，target=220 条；missing_in_target=2（effective=0），extra_in_target=5（api=5），method_mismatch=1。
- 明细：`go_hive/contracts/api-routes.json`（每条 Go 多余路由带 `annotation` 标注是否为扩展/别名）。

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

## 逐端点明细

### 1. GET `/api/health` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 口径备注：比较时忽略 resources.：这些是进程环境读数（内存采样/渲染器数），不是 API 契约字段；reference 触发采样时才会出现 avail_mb/renderers。
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ status == ok — reference='ok' target='ok'

### 2. GET `/api/queen/profiles` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ queen id set equal — reference=13 target=13 missing=[]

### 3. GET `/api/queen/queen_growth/tools` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ queen_id equal — reference='queen_growth' target='queen_growth'
- ✓ mcp server set equal — reference=['chart-tools', 'files-tools', 'hive_tools', 'terminal-tools'] target=['chart-tools', 'files-tools', 'hive_tools', 'terminal-tools']

### 4. GET `/api/skills` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ skill name set equal — reference=195 target=195 missing=[] extra=[]

### 5. GET `/api/config/llm` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ provider equal — reference='openai' target='openai'
- ✓ model equal — reference='contract-probe-model' target='contract-probe-model'
- ✓ has_api_key equal — reference=True target=True

### 6. GET `/api/models/overview` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ slots.llm.provider equal — reference='openai' target='openai'
- ✓ slots.llm.model equal — reference='contract-probe-model' target='contract-probe-model'

### 7. GET `/api/credentials/specs` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ credential_id set equal — reference=115 target=115 missing=[] extra=[]

### 8. POST `/api/sessions` — PASS

- 状态码：reference `201` / target `201`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ queen_id equal — reference='queen_growth' target='queen_growth'
- ✓ queen_name equal — reference='Steve Jobs' target='Steve Jobs'
- ✓ queen_phase equal — reference='independent' target='independent'

### 9. GET `/api/sessions` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ session count equal — reference=1 target=1

### 10. GET `/api/sessions/{session}` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ queen_id equal — reference='queen_growth' target='queen_growth'

### 11. GET `/api/sessions/{session}/events` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `text/event-stream` / target `text/event-stream`
- 口径备注：events[].data 与 frames[].envelope.data 是 google.protobuf.Struct 自由字段：按事件类型比较 data 键集合（见语义检查），不逐字段列出。
- 字段形状：一致（按 canonical shape 比较）

语义检查：
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
- ✓ event type sequence equal — reference=['queen_identity_selected', 'reminder_injected', 'node_loop_started', 'node_loop_iteration', 'context_usage_updated', 'client_output_delta', 'llm_turn_complete', 'client_input_requested', 'loop_state_changed'] target=['queen_identity_selected', 'reminder_injected', 'node_loop_started', 'node_loop_iteration', 'context_usage_updated', 'client_output_delta', 'llm_turn_complete', 'client_input_requested', 'loop_state_changed']
- ✓ total equal — reference=9 target=9
- ✓ data keys equal for common event types — differences=[]

### 13. GET `/api/sessions/{session}/snapshot` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ snapshot keys equal — reference=['activity', 'awaiting_input', 'current_execution_id', 'current_tool_calls', 'interrupt_cause', 'interrupted', 'is_executing', 'last_event_at', 'last_event_seq', 'last_queen_event_at', 'park_reason', 'pending_questions', 'queen_busy_reason', 'snapshot_seq'] target=['activity', 'awaiting_input', 'current_execution_id', 'current_tool_calls', 'interrupt_cause', 'interrupted', 'is_executing', 'last_event_at', 'last_event_seq', 'last_queen_event_at', 'park_reason', 'pending_questions', 'queen_busy_reason', 'snapshot_seq']

### 14. GET `/api/sessions/{session}/stats` — PASS

- 状态码：reference `200` / target `200`
- Content-Type：reference `application/json; charset=utf-8` / target `application/json`
- 字段形状：一致（按 canonical shape 比较）

语义检查：
- ✓ stats keys equal — reference=['active_workers', 'adaptive_tool_budget', 'colony_id', 'event_bus', 'running', 'total_workers', 'triggers'] target=['active_workers', 'adaptive_tool_budget', 'colony_id', 'event_bus', 'running', 'total_workers', 'triggers']
- ✓ running equal — reference=True target=True
- ✓ event_bus.subscriptions is runtime-internal (not compared) — reference=9 target=1

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
