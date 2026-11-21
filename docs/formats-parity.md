# 时间 / ID / 分页格式差异（formats #14；reference=python → target=go_hive）

- 生成时间：2026-11-21T18:00:27+08:00
- reference（基准）：python，HEAD `4af5b3755f9f97c1305fe574055f09533675eb7e`，二进制 `/Users/mac/hive/.venv/bin/hive`
- target（待对齐）：go_hive，HEAD `4af5b3755f9f97c1305fe574055f09533675eb7e`，二进制 `/Users/mac/hive/.wt/appearance-errors-formats/go_hive/go-hive/dist/hive-errfmt`
- 口径：两侧各起真实 serve（同一夹具 HIVE_HOME 的逐字节副本），脚本化 LLM 完成一轮对话后逐端点采集；
  时间字段按「表示法类别」比较（ISO naive / ISO 带时区 / float epoch / int epoch / date-only），不比较运行期数值；
  ID 按「形状」比较（正则/长度/字符集），分页按「参数 + 元数据 + 游标不变量」比较。
- 事件落盘总量（settle 时点）：python `20`，go_hive `24`；随后异步 title 帧追平到 21 / 25（同一剧本、帧集不同，语义差异见 api-contract-diff.md 与 SSE 审计）。
- 产物：`go_hive/docs/e2e-assets/errors-formats/formats-raw.json`、`formats-ledger.json`（`--check` 从 raw 重放并比对台账与本报告）。raw 中 >80 字符的字符串值以 `<str:sha256=… len=…>` 记录，只压缩文本体量，键/类型/时间/顺序不变。

**时间字段：11 个，表示法差异 0 个。**
**ID 格式：6 项，差异 1 项。**
**分页契约：9 项，差异 0 项。**
**排序稳定性：6 个端点，稳定性差异 0 个，两侧顺序差异 1 个。**

## 时间字段表示法

| 端点 | 字段 | python | go_hive | 示例（reference） | 判定 |
|------|------|--------|---------|-------------------|------|
| sessions_list | `sessions[].loaded_at` | epoch-seconds | epoch-seconds | `1795255097.307787` | aligned |
| sessions_history | `sessions[].created_at` | epoch-seconds | epoch-seconds | `1795255098.9029582` | aligned |
| sessions_history | `sessions[].last_active_at` | epoch-seconds | epoch-seconds | `1795255099.0734172` | aligned |
| session_detail | `entry_points[].next_fire_at` | epoch-millis | epoch-millis | `1795258702899` | aligned |
| session_detail | `loaded_at` | epoch-seconds | epoch-seconds | `1795255097.307787` | aligned |
| session_snapshot | `last_event_at` | iso8601-naive micros=6 | iso8601-naive micros=6 | `2026-11-21T17:58:22.899479` | aligned |
| session_snapshot | `last_queen_event_at` | iso8601-naive micros=6 | iso8601-naive micros=6 | `2026-11-21T17:58:22.899479` | aligned |
| session_triggers | `triggers[].trigger_config.next_fire_at` | epoch-millis | epoch-millis | `1795258702899` | aligned |
| session_events_history | `events[].timestamp` | iso8601-naive micros=6 | iso8601-naive micros=6 | `2026-11-21T17:58:17.814027` | aligned |
| queen_overview | `sessions.first` | date-only | date-only | `2026-11-21` | aligned |
| colonies_tools_index | `colonies[].created_at` | epoch-seconds | epoch-seconds | `1795255102.895762` | aligned |

口径归纳：
- `created_at` / `last_active_at` / `loaded_at` / colony `created_at`：JSON number（float epoch 秒，两侧同）；
- 事件与 snapshot 时间：本地时间 naive ISO-8601，微秒恒为 6 位（`2026-11-21T17:49:33.246443`），无时区后缀；
- trigger 的 `next_fire_at`：整数 epoch **毫秒**（`1795258178237`），同时提供 `next_fire_in` 浮点秒；
- `queen_overview.sessions[].first`：date-only（`YYYY-MM-DD`）。

## ID 格式清单

| 种类 | python | go_hive | 判定 |
|------|--------|---------|------|
| session_id | `{"sample": "session_20261121_175817_3bda34fd", "pattern": "session_YYYYMMDD_HHMMSS_<8hex>"}` | `{"sample": "session_20261121_175819_a610d5f8", "pattern": "session_YYYYMMDD_HHMMSS_<8hex>"}` | aligned |
| colony_id | `{"status": 200, "payload": {"colony_id": "probe_colony", "scaffolded": true}}` | `{"status": 200, "payload": {"colony_id": "probe_colony", "scaffolded": true}}` | aligned |
| trigger | `{"status": 200, "sample": {"trigger_id": "user-probe-trigger"}}` | `{"status": 200, "sample": {"trigger_id": "user-probe-trigger"}}` | aligned |
| event execution_id 形状（按事件类型） | `{"client_input_committed": {"session-shaped": 1}, "client_input_received": {"session-shaped": 1}, "client_input_requested": {"session-shaped": 2}, "client_output_delta": {"session-shaped": 2}, "context_usage_updated": {"none": 2}, "judge_verdict": {"session-sh` | `{"client_input_committed": {"hex32": 1}, "client_input_received": {"session-shaped": 1}, "client_input_requested": {"session-shaped": 1, "hex32": 1}, "client_output_delta": {"session-shaped": 1, "hex32": 1}, "context_usage_updated": {"none": 2}, "judge_verdict` | diff |
| event correlation_id | `{"hex32": 2}` | `{"hex32": 2}` | aligned |
| event seq | `{"count": 21, "contiguous_from_1": true, "type": "int"}` | `{"count": 25, "contiguous_from_1": true, "type": "int"}` | aligned |

ID 口径：
- session id：`session_YYYYMMDD_HHMMSS_<8hex>`（两侧生成器同形，样例逐侧不同属运行期）；
- colony id：**客户端提供**（路径参数/目录名），约束 `[a-z0-9_]{1,64}`，两侧 scaffold 均回显 `probe_colony`；
- trigger id：按 name slug 化并加 `user-` 前缀（`probe_trigger` → `user-probe-trigger`，两侧同）；
- event seq：整数、从 1 连续（两侧都通过 `contiguous_from_1`）；`correlation_id`：32 位小写 hex；
- execution id：**保留差异**——上游把执行域事件（`llm_turn_complete`/`client_output_delta`/`client_input_committed` 等）的 `execution_id` 复用 session id，Go 为执行域分配独立的 32 位 hex（`execution_started` → `execution_completed`），前置帧（`reminder_injected`/首个 `node_loop_started`）两侧同为 session id。该差异与 Go 每回合额外的 `execution_*`/`node_loop_*` 信封同源，属 SSE/事件 lane 的已登记口径，不在 formats 层单独改。

## 分页契约（/api/sessions/{id}/events/history）

参数：`limit`（默认 500，上限 10000，`0`/非数字回落默认，负数夹到 1）、`before_offset`、`before_index`（游标分页）；
响应键：`events, session_id, total, returned, truncated, limit, start_index, start_offset, has_more_older`（两侧逐键一致）。

| 检查 | 判定 | 差异 | 说明 |
|------|------|------|------|
| page1 | aligned | — | event totals differ (reference 21 vs target 25); page metadata values follow the on-disk event count, not the paging contract |
| page2 | aligned | — | — |
| limit_absent | aligned | — | event totals differ (reference 21 vs target 25); page metadata values follow the on-disk event count, not the paging contract |
| limit_bad | aligned | — | event totals differ (reference 21 vs target 25); page metadata values follow the on-disk event count, not the paging contract |
| limit_zero | aligned | — | event totals differ (reference 21 vs target 25); page metadata values follow the on-disk event count, not the paging contract |
| limit_negative | aligned | — | event totals differ (reference 21 vs target 25); page metadata values follow the on-disk event count, not the paging contract |
| limit_huge | aligned | — | event totals differ (reference 21 vs target 25); page metadata values follow the on-disk event count, not the paging contract |
| before_offset_zero | aligned | — | — |
| page_stitching | aligned | — | — |

不变量（两侧逐项通过）：`returned == min(limit, total)`（尾页）、`len(events) == returned`、`has_more_older == (start_offset > 0)`；游标页 `total == -1`，且 `page2.start_index + page2.returned == page1.start_index`，旧页 `seq` 连续衔接。事件总量两侧不同（python 20 / go 24，同一剧本），因此 `total`/`start_offset` 等运行期数值不做逐值比较，只作为 declared 记录。

## 排序稳定性

| 端点 | 身份键 | 顺序依据（python / go） | python 稳定 | go_hive 稳定 | 两侧顺序一致 | 元素数 |
|------|--------|--------------------------|-------------|--------------|--------------|--------|
| sessions_list | session_id | loaded_at asc / loaded_at asc | True | True | True | 3/3 |
| sessions_history | session_id | session_id desc / session_id desc | True | True | True | 3/3 |
| skills | name | — | True | True | True | 195/195 |
| queen_profiles | id | — | True | True | True | 13/13 |
| credentials_specs | credential_id | — | True | True | False | 115/115 |
| colonies_tools_index | name | — | True | True | True | 1/1 |

结论：6 个列表端点两侧连续两次读取顺序完全一致（0 个稳定性差异）。
`/api/sessions` 两侧同为 `loaded_at` 升序（创建/载入序）；`/api/sessions/history` 两侧同为 `session_id` 降序（最新在前）；`/api/skills`、`/api/queen/profiles`、`/api/colonies/tools-index` 两侧顺序逐项相同。

## 保留差异（declared，逐条量化）

| 编号 | 主题 | 实测 | 处理 |
|------|------|------|------|
| F-01 | `/api/credentials/specs` 数组顺序 | 两侧元素集合相同（115 条）且各自稳定；reference 为 Python 注册序，target 按 `credential_id` 字典序 | 保留：Go 的生成表（`internal/credentials/specs_gen.go`）按 id 排序产出，属凭据目录冻结契约；要对齐需重生成并改 `CatalogNamedSpecs` 排序，影响 `hive-cred browse` 顺序，另派凭据 lane |
| F-02 | 执行域 `execution_id` 取值 | 见「ID 格式清单」：上游复用 session id，Go 用 32 hex | 保留：与 Go 额外 `execution_*` 信封同源（SSE lane 已登记），改 ID 语义会波及事件流契约 |
| F-03 | 事件总量 / 分页运行期数值 | 同一剧本 settle 时 python 20 帧 vs go 24 帧（采集 21/25）；`total`/`start_offset` 随之不同 | 保留：分页契约（参数/键/不变量/游标算术）已逐项对齐，帧数差异归 SSE/事件 lane |

## 复现

```bash
cd go_hive/go-hive && CGO_ENABLED=0 go build -o dist/hive-errfmt ./cmd/hive
uv run --no-project python go_hive/scripts/errors_formats_probe.py formats          # 双跑 + 采集
uv run --no-project python go_hive/scripts/errors_formats_probe.py formats --check  # 从 raw 重放台账与报告
```

夹具：同一 HIVE_HOME 模板的逐字节副本；两侧各起真实 serve，`POST /api/sessions` 建 3 个会话（间隔 0.4s 以固定排序样本），首个会话跑一轮脚本化 LLM 对话并等 `is_executing=false` + 事件总数稳定；随后 scaffold 一个 colony、建一个 timer trigger，再逐端点采集。

