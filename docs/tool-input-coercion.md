# Tool input coercion 接线（CoerceToolInput → agent loop 派发前）

状态：**已接线**（lane `coercer-wire`，worktree `.wt/coercer-wire`，基线 `528bdcd6`，提交 `f41657a5` + 本文档提交）。
对应 backlog：`go_hive/docs/delivery-backlog.json` → `unported-tool-input-coercer-wire`
（unported 分诊 P1：Go 实现全量但仅测试调用，未接线）。

## 1. 上游语义（权威参考）

- 实现：`core/framework/agent_loop/internals/tool_input_coercer.py::coerce_tool_input`（:51）。
  schema 驱动（`tool.parameters.properties`），按 `type` 分派；union 类型按顺序取第一个产生变化的强转；
  保守设计：值已符合 schema / 形状不可识别 → 原样返回。
- 调用点：`core/framework/agent_loop/agent_loop.py::_run_turn_loop`（:4802-4805）——
  `_tool_by_name` 查找 schema，逐 tool call 调 `coerce_tool_input(_tool_schema, tc.tool_input)`，
  **原地修改** `tc.tool_input`。
- 顺序（上游）：
  1. assistant 消息先写入 conversation（:4688，`json.dumps(tc.tool_input)` 快照的是**强转前**参数）；
  2. 逐 call 循环开头强转（:4805）；
  3. 之后才是 grace 跳过判断（:4813）/ hard 预算（:4817）/ lifetime 预算（:4834）；
  4. `_publish_tool_started(... tc.tool_input ...)`（:4845）与 dispatch —— 事件、执行器、
     doom-loop 指纹、`logged_tool_calls`/`real_tool_results`（:5783/:5907/:5956）、vision caption intent（:5711）
     看到的都是**强转后**的输入。
- 失败语义：**无异常、无错误文案、无失败事件**。不可识别的形状原样保留，让工具自身的校验去报错；
  仅成功强转的每次写入 `logger.info("coerced tool input tool=%s prop=%s from=%s to=%s")`。

## 2. Go 接线点与顺序

- 接线位置：`internal/agentloop/loop.go::ExecuteTurn`（:651）——
  `coercedInputs := coerceToolCallInputs(tools, toolCalls)` 在整个批次的任何门禁/事件之前
  完成逐 call 的 parse+强转；循环内 `call := ToolCallRequest{... Input: coercedInputs[i]}`（:654）。
  与上游同点位（parse 之后、grace/预算门与 tool_started 之前）。
- 适配层：`internal/agentloop/tool_coercion_wire.go`
  - `indexToolDefsByName`（:12）——上游 `_tool_by_name` 的对应物（每批建一次）；
  - `coerceToolCallInput`（:37）——`llm.ToolDef` 适配为 `tools.Tool` 后调用既有实现
    `internal/tools/coerce.go::CoerceToolInput`（:12）；
  - `coerceToolCallInputs`（:52）——批次入口，供 ExecuteTurn 一行调用（ExecuteTurn 314 行，守 321 行冻结上限）。
- 覆盖范围：模型工具调用全部经由 `AgentLoop.ExecuteTurn`（`internal/colony/runtime.go:1285,1381`、
  `internal/server/chat_runtime.go:977,1076`），单点接线覆盖 server chat / colony worker 全部路径。
- 顺序（Go，与上游一一对应）：
  1. `toolCallDicts`/`AddAssistantMessage` 用**原始** arguments 落盘（loop.go:593,600，对应上游 :4688）；
  2. 批次入口强转（loop.go:651，对应上游 :4805）→ grace 跳过（:655）→ hard 预算（:662）→
     lifetime 预算（:670）→ `toolCallsUsed++`（:677）→ `Observer.ToolCallStarted(call)`（:684）→
     `executeToolBatch`/executor（:689）。
  3. `storeToolBatch(..., coercedInputs)`（loop.go:740 → `tool_batch_store.go:34`）以强转后输入构建
     `ToolCallRecord.ToolInput`（:41），对应上游 `tool_entry["tool_input"]`；vision caption intent
     （`tool_batch_store.go:70` → `vision_caption.go:49`）同样读强转后输入（上游 :5711 `tc.tool_input`）。
- **vision_caption.go 改动的来龙去脉（本 lane 范围）**：上游 caption intent 在 coercion 之后读
  `tc.tool_input`（已 healed）；Go 原实现重新解析原始 arguments。本次把
  `captionToolImages` 改为接收 `records[i].ToolInput`（即强转后输入），是拉直同一条上游链路，
  不是无关改动。传 nil 的旧测试路径行为逐字节不变。
- 未接线路径行为不变（守门条件）：
  - tool 名不在本批 `tools` 列表 → `coerceToolCallInput` 原样返回同一 map；
  - `Parameters` 为 nil / 无 `properties` → `CoerceToolInput` 原样返回同一 map；
  - 值已符合 schema、属性不在 schema → 不触碰；
  - `storeToolBatch` 传 `nil`（两个旧 vision 测试的调用形态）时回落为解析原始 arguments，与接线前完全一致。
- 规模门禁：接线后 `loop.go` 曾到 1518 行（>1500 冻结上限）、`ExecuteTurn` 322 行（>321 冻结上限）。
  按仓库规则**拆函数不上调 waiver**：`storeToolBatch` 整体移至 `internal/agentloop/tool_batch_store.go`，
  coercion 段抽成 `coerceToolCallInputs`。现 `loop.go` 1427 行、`ExecuteTurn` 314 行，
  `internal/quality` 规模门禁全绿。

## 3. 失败语义（Go）

- 与上游一致：不可识别/不可强转的值**原样保留**，不产生错误结果、错误文案或事件；调用照常派发，
  由工具自身校验报错。例：integer schema 收 `"forty-two"` → 保留字符串；`"3.14"`、`"inf"` → 保留字符串；
  boolean schema 收 `"1"` → 保留字符串（只认 `"true"`/`"false"`，大小写与空白不敏感）。
- 空/非法 arguments：`parseToolInput` 返回 `{}`，强转返回同一非 nil map（上游 `None → {}`）。
- 已知诊断差异（非行为差异）：上游每次成功强转打 info 日志；Go 侧 `CoerceToolInput` 不记录逐属性日志，
  事件与输入语义不受影响（如需可后续在适配层加 debug 日志，属独立小项）。

## 4. 测试清单

既有（全部保留，未改动断言）：

- `internal/tools/coerce_test.go` —— 22 条上游镜像单测（`core/tests/test_tool_input_coercer.py`）。

新增接线级 parity 测试：`internal/agentloop/tool_coercion_parity_test.go`（4 条）

| 测试 | 覆盖 | 断言 |
| --- | --- | --- |
| `TestToolInputCoercionHealsDriftBeforeDispatch` | 强转成功：`"42"→int64(42)`、`"3.5"→3.5`、`"true"→true`、`{value}`→string、`[{label}]`→`[string]` | executor、`tool_started`、`tool_completed`、`TurnResult` 记录四者均看到强转后输入 |
| `TestToolInputCoercionFailureLeavesTheCallForTheToolToReject` | 强转失败：`"forty-two"`（integer）、`"1"`（boolean） | 输入原样、调用照常执行且为非错误结果（coercion 不制造错误） |
| `TestToolInputCoercionLeavesMatchingAndUnadvertisedInputsUntouched` | 不需要强转：类型已匹配；未广告的 tool（无 schema） | 两类输入均与未接线时逐一相等 |
| `TestToolInputCoercionRecursesIntoNestedObjectsAndArrays` | 嵌套对象/数组：object 内 `"3"→int64(3)`、`{label}`→string、array<object> 内 options 解包 | executor 与 `TurnResult` 记录递归强转结果 |

注解：每条测试带 `// Parity: core/tests/test_tool_input_coercer.py::test_*`；
适配层函数带 `framework/agent_loop/agent_loop.py::_run_turn_loop` / `tool_input_coercer.py::coerce_tool_input` 参考。

## 5. 门禁与证据（lane 内实跑，go1.26.11 / GOTOOLCHAIN=local / CGO_ENABLED=0 / GOPROXY=off）

| 命令 | 结果 |
| --- | --- |
| `go test ./internal/agentloop/ ./internal/tools/ -count=1` | `ok` ×2（6.7s / 2.8s） |
| `go test ./... -count=1` | **EXIT=0**；64 个包 `ok`，0 `FAIL`，18 个包无测试文件（日志 /tmp/coercer_full_test4.log） |
| `bash go_hive/scripts/check_parity.sh` | OK —— 5726/5726 测试带注解/豁免 |
| `bash go_hive/scripts/check_parity_targets.sh` | OK —— 6255 refs，0 missing |
| `python3 go_hive/scripts/wiring_check.py` | OK —— 21 文件 / 68 项（本 lane 新增 `loop.go` 的 `coerceToolCallInputs?\(` 调用点要求） |
| `bash go_hive/scripts/merge_postcheck.sh` | PASS —— gofmt clean / wiring OK / C12 PASS / arch 一致 |
| `go vet ./internal/agentloop/ ./internal/tools/` | OK |

（`internal/quality` 规模门禁随全量测试一并通过：`ExecuteTurn` 314 行 ≤ 321、`loop.go` 1427 行 ≤ 1500。）

## 6. 后续（不属于本 lane）

- `contracts/module-inventory.json` 的 `core/framework/agent_loop/internals/tool_input_coercer.py`
  条目仍为 `partial/call_chain`（C12 规则仅对 `kind=dead` 强制升级）。接线后已具备 promote→`ported`
  的 caller 证据（`internal/agentloop/tool_coercion_wire.go` 引用 `tools.CoerceToolInput`），
  建议合并后由 CURATED 重建流程升级该条目并同步 unported-triage 计数与 backlog 状态。
