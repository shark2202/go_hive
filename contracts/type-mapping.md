# API 契约类型映射与 JSON 线格式规则

- 契约基准（reference）：本仓 Python hive checkout，`git rev-parse HEAD` = `1b9c458a19244379ac65dabd4827addb891045ef`，版本 `1.0.14`；`core` 最后一次源码提交 `a756b1e51765bc75b0a40e362859452a0a54081b`，`tools` 最后一次源码提交 `68b6c230f20e237953dea7a3af7bb583b9fbe0fd`。
- 对齐目标（target）：`go_hive`，版本 `1.0.26-go.1`，HEAD 同上。
- 规范文件（legacy）：`go_hive/contracts/api.proto`（proto3 风格，**不引入 protobuf 运行时**，不生成代码）。
- 当前 canonical 文档：`go_hive/contracts/openapi.json`（HTTP）与 `go_hive/contracts/asyncapi.json`（SSE/WS）；维护方式见 `go_hive/contracts/README.md`。`api.proto` 仍保留为旧字段形状证据，迁移完成前不删除。
- 校验器：`go_hive/scripts/check_api_proto.py`（自写解析器 + 冻结 shape 校验，产出 `api-proto-check.json`）。
- 冻结产物：`go_hive/contracts/api-responses/`（reference 为契约，target 为观测值）；逐端点差异见 `go_hive/docs/api-contract-diff.md`。

## 1. 类型映射表

| Python 实际值 | proto3 类型 | JSON 线格式 | 规则 |
| --- | --- | --- | --- |
| `str` | `string` | string | 逐字；空串与缺失不同 |
| `bool` | `bool` | true/false | 不接受 0/1 |
| `int`（计数、序号、tokens） | `int32` / `int64` | JSON number | 超过 2^53 时 JSON number 不可无损；见 NV-01 |
| `float`（时间戳秒、比率、成本） | `double` | JSON number | `3` 与 `3.0` 在 proto3 JSON 中都可接受；见 NV-02 |
| `None` | 字段标 `optional` | 无输出或 `null` | proto3 不能区分“显式 null”与“未设置”；见 NV-03 |
| `dict`（自由结构） | `google.protobuf.Struct` | object | `events[].data`、`input_schema`、`accounts_by_provider` 的 value |
| `dict`（固定键） | 具名 message | object | 逐键声明；未知键在 proto3 中应被忽略 |
| `dict`（动态键） | `map<string, T>` | object | 键序不参与契约；见 NV-04 |
| `list[T]` | `repeated T` | array | 空数组与缺失不同 |
| ISO 时间字符串 | `google.protobuf.Timestamp` | RFC 3339 字符串 | Hive 实际发本地 naive 时间，按不透明 string 冻结；见 NV-06 |
| `bytes` | `bytes` | base64 string | 本批冻结端点不含 bytes；见 NV-08 |

`null` 的处理：reference 中出现的 `null` 一律映射为 `optional` 字段的“未设置”语义。冻结 shape 保留 `null`（类型 `null`）与键缺失（`absent`）的区别，便于量化；但任何 proto3 JSON 投影都会把两者折叠，见 NV-03。

## 2. 端点 → proto message

| 端点 | 方法 | proto message |
| --- | --- | --- |
| `/api/health` | GET | `HealthResponse` |
| `/api/queen/profiles` | GET | `QueenProfilesResponse` |
| `/api/queen/{queen_id}/tools` | GET | `QueenToolsResponse` |
| `/api/skills` | GET | `SkillsResponse` |
| `/api/config/llm` | GET | `LlmConfigResponse` |
| `/api/models/overview` | GET | `ModelsOverviewResponse` |
| `/api/credentials/specs` | GET | `CredentialSpecsResponse` |
| `/api/sessions` | POST | `SessionView` |
| `/api/sessions` | GET | `SessionsListResponse` |
| `/api/sessions/{id}` | GET | `SessionDetailResponse` |
| `/api/sessions/{id}/events` | GET (SSE) | `SseStream`（抓取表示，非服务端 JSON） |
| `/api/sessions/{id}/events/history` | GET | `EventsHistoryResponse` |
| `/api/sessions/{id}/snapshot` | GET | `SnapshotResponse` |
| `/api/sessions/{id}/stats` | GET | `SessionStatsResponse` |

Go 独有字段在 proto 中以 `// target-extension` 注释标明（`CredentialSpec.tool_modules`、`CredentialSpec.accounts`），它们不属于 reference 契约。

## 3. 冻结时的 canonical 化规则

`freeze_api_responses.py` 在比较前做以下规范化（规则在 `api_contract_lib.canonicalize`）：

1. **对象键排序**：所有 dict 按 key 排序写出，消除 map/对象键序噪声。
2. **易变键置值**：`session_id`、`colony_id`、`execution_id`、`correlation_id`、`loaded_at`、`uptime_seconds`、`snapshot_seq`、`last_event_seq`、`seq`、`*_at`、`port`、`pid`、路径类键等：
   - 数值 → `0.0`（保留“JSON number”类型与存在性）；
   - 字符串 → `<键名>` 占位符；
   - `null` → 保留 `null`。
3. **字符串模式**：session id、UUID、ISO 时间、绝对临时路径替换为占位符；超过 600 字符的文本截断并附 `sha256` 前缀（防止快照体积失控，同时保留可核对摘要）。
4. **数组**：逐元素规范化，保留长度；空数组不产生元素 shape（视为“无数据”，不是字段缺失）。
5. **自由字段**：`events[].data`、`frames[].envelope.data` 视为 `google.protobuf.Struct`，字段级差异不逐键列出，改按事件类型比较 data 键集合（语义检查）。

## 4. 不可逐字（non-verbatim）清单

| 编号 | 主题 | 说明 |
| --- | --- | --- |
| NV-01 | int64 与 2^53 | proto3 JSON 将 64 位整数编码为字符串；Hive 两端都发 JSON number，超过 2^53 不可无损往返。当前载荷的计数/序号远小于 2^53，但契约上需声明上限或改 string。 |
| NV-02 | JSON number 整数性 | Python `json` 区分 `3`（int）与 `3.0`（float），Go `encoding/json` 统一 `float64`，proto3 `double` 二者都接受。`uptime_seconds` 的 int/float 差异无法逐字，只能按 double 规范化。 |
| NV-03 | null vs 缺失 | proto3 JSON 的 `null` 表示“未设置”，不能与“字段存在且值为 null”区分；shape 层保留差异，proto 投影会丢失。 |
| NV-04 | map 顺序 | `map<string, T>` 与 JSON object 的键序都不属于契约；逐字节比较必须排序。 |
| NV-05 | 浮点打印精度 | Python repr 与 Go `encoding/json` 的最短表示不同（`loaded_at` 末位可见），只能按 double 语义比较或屏蔽。 |
| NV-06 | Timestamp 文本 | `google.protobuf.Timestamp` 的规范 JSON 是带时区 RFC 3339；Hive 发本地 naive 时间（`YYYY-MM-DDTHH:MM:SS.ffffff`）。转 Timestamp 前必须约定时区。 |
| NV-07 | NaN / Infinity | proto3 JSON 用 `"NaN"`/`"Infinity"` 字符串；Python json 默认输出裸 `NaN`/`Infinity`（非标准 JSON）。当前冻结载荷未出现。 |
| NV-08 | bytes / base64 | proto3 `bytes` 线上是 base64；当前冻结端点不含二进制字段，附件下载接口不在本批范围。 |
