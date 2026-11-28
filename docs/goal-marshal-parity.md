# goal 序列化对齐（goal-marshal-parity）

记录时间：2026-11-21 · 工作树基线 `00eeecc8`（main）· 车道 backlog id `goal-marshal-parity`

范围：`core/framework/schemas/goal.py`、`core/framework/orchestrator/goal.py`、`core/framework/loader/agent_loader.py`、
`core/framework/server/session_manager.py`、`core/framework/host/outcome_aggregator.py` ↔
`go_hive/go-hive/internal/schemas/goal.go`（新增 `ordered.go`）、`internal/agents/agent_spec.go`、`internal/agentloop/goal_eval.go`、`internal/colony/goal_outcome.go`。

双跑命令（两侧都是真实代码，不是转述）：

```bash
# Python 侧（pydantic 2.12.5）
cd core && /Users/mac/hive/.venv/bin/python <证据脚本>   # AgentLoader.load / Goal.model_dump_json / to_prompt_context
# Go 侧
cd go_hive/go-hive && go test ./internal/schemas/ ./internal/agents/ -run 'Weight|IDOmitted|PromptContext|ColonyPath' -v
```

## 1. weight=0 不再被归一为 1.0

`framework/schemas/goal.py::SuccessCriterion.weight` 的语义是「缺省 1.0，显式值原样保留」；
Go 旧 `MarshalJSON` 无条件 `if p.Weight == 0 { p.Weight = 1.0 }`，把显式 0 也改写掉了。

修法：`UnmarshalJSON` 记录 `"weight"` 键是否出现（`weightSet`）；`MarshalJSON` 走新增的 `EffectiveWeight()`
（未提供且为 0 → 1.0；其余原样）；Go 侧构造用 `SetWeight(v)` 表达显式值（含 `SetWeight(0)`）。
`IsSuccess()` 与 `agentloop` 的加权分也改用 `EffectiveWeight()`，避免「JSON 说 1.0、算术按 0 算」的自我矛盾。

同一输入两侧输出（criterion 其余字段相同）：

| 输入 | Python `model_dump_json()` | Go `json.Marshal` |
| --- | --- | --- |
| 无 `weight` 键 | `…"weight":1.0,"met":false}` | `…"weight":1,"met":false}` |
| `"weight":0` | `…"weight":0.0,"met":false}` | `…"weight":0,"met":false}` |
| `"weight":0.5` | `…"weight":0.5,"met":false}` | `…"weight":0.5,"met":false}` |

解析后的数值三例两侧相等（1 / 0 / 0.5）；数字文本 `1` vs `1.0` 是 Go `encoding/json` 的 float 格式，见残差 1。

测试：`internal/schemas/goal_test.go::TestSuccessCriterionWeightKeepsExplicitZero`、
`TestSuccessCriterionProgrammaticZeroWeightIsExplicit`。

## 2. id：空 id 不再伪造，内部键不变

先实测（基线 `00eeecc8`），得到的结论与车道描述里的前提不同：

- `framework/schemas/goal.py` 与 `framework/orchestrator/goal.py` 的 `SuccessCriterion/Constraint/Goal`
  **都有 `id: str` 且都是必填**（`model_fields` 实测 required=`['id','description','metric','target']` 等；
  `id` 自查 4be61ebf 建文件起就存在）。缺 `id` 构造直接 `ValidationError: missing`，不会输出「无 id」的 JSON。
- 真实 colony 装载路径 `AgentLoader.load`（worker.json，2 criteria + 1 constraint）输出：
  `"id":"colony_fixture-goal"`、criteria `sc-0`/`sc-1`、constraint `c-0`、`"weight":1.0`。
- 会话级 colony goal（`session_manager.py:1741`）输出 `"id":"colony_goal_sess1"`。
- `goal_progress.criteria_status` 的 Python 生产者现在是 stub（`outcome_aggregator.py:18`）：
  `{"progress": 0.0, "criteria_status": {}}`；历史实现（`3c9911c2^:framework/runtime/outcome_aggregator.py:254`）
  以 `criterion.id` 为键，值为 `{description, met, progress, evidence}`。

策略（2026-11-25 修订）：三个 `id` 字段**不加** `omitempty` —— Python 侧 `id` 是必填（缺 id 直接
`ValidationError: missing`，显式空 id 才输出 `"id":""`），所以 Go 必须始终输出 `id` 键：零值输出
`"id":""`（Python 接受的值），缺失/null id 在解析时按必填拒绝。此前「删掉空 id」的写法会让 Go 产物在
Python 侧被当成缺必填字段，已回退。id 有值时输出与 Python 一致，内部键
（`criterionStatusMap` 以 `criterion.ID` 为键）不受影响。

同场景 JSON 对拍（同一 worker.json，Python loader 与 Go `LoadAgentSpec`）：

```json
// Python AgentLoader.load（去掉运行期 created_at/updated_at）
{"id":"colony_fixture-goal","name":"ship it","description":"ship it","status":"draft","success_criteria":[{"id":"sc-0","description":"a","metric":"llm_judge","type":"success_rate","target":"","weight":1.0,"met":false},{"id":"sc-1","description":"b","metric":"llm_judge","type":"success_rate","target":"","weight":1.0,"met":false}],"constraints":[{"id":"c-0","description":"c","constraint_type":"hard","category":"general","check":""}],"context":{},"required_capabilities":[],"version":"1.0.0","parent_version":null,"evolution_reason":null}
// Go LoadAgentSpec（created_at/updated_at 为 null，见残差 2）
{"id":"colony_fixture-goal","name":"ship it","description":"ship it","status":"draft","success_criteria":[{"id":"sc-0","description":"a","metric":"llm_judge","type":"success_rate","target":"","weight":1,"met":false},{"id":"sc-1","description":"b","metric":"llm_judge","type":"success_rate","target":"","weight":1,"met":false}],"constraints":[{"id":"c-0","description":"c","constraint_type":"hard","category":"general","check":""}],"context":{},"required_capabilities":[],"version":"1.0.0","parent_version":null,"evolution_reason":null,"created_at":null,"updated_at":null}
```

差异只有 `weight` 数字文本与时间戳两项（残差 1、2），键集合、键序、取值逐项一致（测试做 map 深比较）。

Go-only 边界样例：

| 输入 | Go 输出 |
| --- | --- |
| criterion 零值（程序化构造） | `{"id":"","description":"d","metric":"llm_judge","type":"success_rate","target":"","weight":1,"met":false}`（与 Python `id=""` 同值；不再省略键） |
| criterion `"id":""` | `{"id":"","description":"d",…}`（保留空 id，与 Python `id=""` 同值） |
| criterion `"id":"sc-0"` | `{"id":"sc-0","description":"d",…}`（与 Python 逐字节同序） |
| criterion 输入缺 `id` / `"id":null` | 解析错误（Python 同为 `ValidationError`） |

测试：`internal/agents/goal_marshal_parity_test.go::TestLoadAgentSpecGoalJSONMatchesPythonColonyPath`、
`internal/schemas/goal_test.go::TestGoalIDsAreAlwaysSerialized`、`TestGoalIDsAreRequiredOnRead`。

## 3. context 键序保序

`Goal.to_prompt_context()` 按 dict 插入序遍历 `self.context.items()`；Go 旧实现 `sort.Strings(keys)`，
多 key 时 prompt 行序与 Python 相反（`zone/audience/deadline` 被排成 `audience/deadline/zone`）。

实现：新增 `internal/schemas/ordered.go::OrderedMap`（`json.Decoder` 逐 token 解析保序、
`MarshalJSON` 按插入序输出、零值 marshal 为 `{}`），`Goal.Context` 由 `map[string]any` 改为 `OrderedMap`，
`ToPromptContext()` 用 `Context.Keys()` 原序 —— 调用方（JSON 作者 / `Set()` 调用者）无需排序。

3 key 逐字节证据（同一文档 `{"context":{"zone":"us-east","audience":"CFO","deadline":"Friday"}}`）：

```
# Goal: Context goal
Ship the report

## Success Criteria:

## Context:
- zone: us-east
- audience: CFO
- deadline: Friday
```

Python：122 bytes，sha256 `e211734a982cb41b220d37a9d208915cdaf7de64b9e9d1627eb089cd67239ef2`；
Go：122 bytes，sha256 同上（逐字节一致）。Go 侧 marshal 的 `context` 也为
`{"zone":"us-east","audience":"CFO","deadline":"Friday"}`（旧实现会输出 `audience/deadline/zone`）。

测试：`internal/schemas/goal_test.go::TestGoalPromptContextPreservesContextKeyOrder`。

## 残差（未改，均已实测）

1. **float 数字文本**：Go 写 `1`/`0`，Python 写 `1.0`/`0.0`。`encoding/json` 的全局行为，非 goal 专有
   （`score`、`progress` 同理）；本车道只保证取值一致，不做逐字节 JSON 对齐。
2. **时间戳**：Go 侧 `buildWorkerSpec` 构造的 goal 不带时间戳 → `"created_at":null`；Python 在构造时
   `datetime.now()`，故是两个「非空但每次不同」的值。任何对拍都必须按运行期字段归一。
3. **criteria_status**：Python 当前是 stub（恒 `{}`），Go 的 `{"sc-0":{…}}` 是 Go 侧执行层产出，
   `goal_outcome.go` 已标 `Parity-exempt`；键仍来自 `criterion.ID`。
4. **Extra 透传会重排键**（既有行为）：带未知字段的 goal/criterion 经 `mergeExtras` 走 map 再序列化，
   键序变字母序。`id` 现在是有 tag 的声明字段，不再走这条路径（显式 `"id":""` 的往返保持声明顺序）。
5. **context 值渲染**：`fmt %v` vs Python `str()`，布尔/None/嵌套 dict 的文本不同（如 `map[a:1]` vs `{'a': 1}`）；
   本车道只对齐顶层键序，字符串值逐字节一致。

## 门禁

```
go test ./internal/schemas/ ./internal/agents/ ./internal/colony/ ./internal/agentloop/   # 全绿
gofmt -l internal/                                  # 空
go vet ./...                                        # 空
make parity          # TOTAL 5449 5449 0 → OK
make parity-targets  # parity refs checked: 5871  missing: 0 → OK
```

> 目标模型与「目标驱动」全貌（可复用资料）：`docs/goal-design.html`（生成器 `scripts/gen_goal_doc.py`，数字现场重算）。
