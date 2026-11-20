# Stall check 真实端到端（真实 `hive serve` + 脚本化模型与 judge）

本文件记录 stall check（`internal/server/stall_check.go`）对**真实二进制**跑通的端到端证据。
不是单测对表：真实 `hive serve` 进程、临时 `HIVE_HOME`、真实 HTTP；唯一被脚本化的是模型与 judge
（`e2e/stallgw`：确定性本地 OpenAI 兼容网关 + TypeSafe `/systemone`）。

- 日期：2026-11-20
- 被测：`.wt/stall-check/go_hive/go-hive`（module `github.com/aden-hive/hive-go`，分支 `stall-check`，基线 `853a15e9`）
- 网关：`go_hive/go-hive/e2e/stallgw`（本 lane 新增，测试基础设施）
- 证据目录：`go_hive/docs/e2e-assets/stall-check-e2e/`

## 结论

| 用例 | 结果 | 断言 |
| --- | --- | --- |
| judge 说 true（p=0.95，threshold=0.5） | PASS | judge 槽收到真实请求（`Bearer judge-key`、`noul`、question/criteria/state 逐字段相等）；提醒进入**下一轮**模型请求与会话转录；该用户消息只判一次 |
| judge 说 false（p=0.05） | PASS | judge 被问一次；任何模型请求与转录都不含提醒；没有第二轮 |
| 开关关闭（judge 槽照常配置） | PASS | judge 0 次调用；无提醒 |

**这是管线验证，不是模型质量结论**：女王模型是一段脚本（第一轮「宣称要做却无工具调用地停手」，
其后答 `Done.`），judge 按固定概率作答。本 E2E 证明的是判定、注入、开关与转录链路的接线，
不代表任何真实模型的命中率。

## 复现

```bash
cd .wt/stall-check/go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off \
HIVE_STALL_E2E_OUT=/tmp/stall-check-e2e \
  go test -tags e2e ./e2e -run TestStallCheckEndToEnd -v -count=1
```

每个用例写进临时 `HIVE_HOME` 的 `configuration.json`：

```json
{"llm":{"provider":"custom","model":"ollama/stall-e2e-model","api_base":"<gateway>/v1"},
 "stall_check": true,
 "judge":{"protocol":"typesafe","model":"judge-e2e","api_base":"<gateway>/judge","api_key":"judge-key","threshold":0.5}}
```

`-tags e2e` 的测试不进入 `make ci` 的常规测试集。

## 证据文件（`go_hive/docs/e2e-assets/stall-check-e2e/`）

每个用例一个目录（`judge-true/`、`judge-false/`、`switch-off/`）：

- `summary.txt` — 请求序列（seq / kind / model / messages / reminder）与计数
- `judge-requests.jsonl` — judge 槽收到的原始请求体（state、question、criteria、model）
- `queen-requests.jsonl` — 每次女王模型请求的摘要（model、消息数、是否携带提醒、最后一条 user 内容）
- `transcript.json` — 会话转录 parts（每条内容截断 4000 字符）
- `events.jsonl` — 会话事件日志
- `server-output.txt` — `hive serve` 输出

## 关键断言的实际值（judge-true）

- judge 请求（逐字）：`state = {"user_message":"draw me a fox","assistant_reply":"On it — drawing the fox now.",
  "assistant_reasoning_tail":"","tools_called_this_turn":[]}`；`model = judge-e2e`；
  `questions.answer.type = noul`，`instructions` = `stall_check.py::_QUESTION`，`criteria` = `_CRITERIA`；
  `Authorization: Bearer judge-key`
- 转录：`user(skills 提醒块)` → `user([stamp] draw me a fox)` → `assistant(On it — drawing the fox now.)`
  → `user([stamp] [External event] <system-reminder>…Don't mention this reminder.</system-reminder>)`
  → `assistant(Done.)`
- 提醒在下一次女王模型请求（seq 3，messages=4）的消息里逐字可见；judge 请求总数 = 1。

## 与上游的关系

- 上游 `framework/server/stall_check.py` 判定后只注入提醒（`inject_event`），**不发事件**；本移植本轮
  去掉了此前额外的 `node_stalled` 发布，事件面与上游对齐（`node_stalled` 属 doom-loop）。
- 事件触发面保留一处**有注释的移植适配**：上游以 `client_input_requested(turn_done)` 为回合结束信号，
  本移植的 chat emitter 以 `loop_state_changed(awaiting_user, park_reason=turn_done)` 报告同一停车；
  订阅同时接受两种形状，`park_reason` 过滤与上游一致。
- threshold 语义按上游 `float(slot.get("threshold", 0.5))` 对齐：缺省 0.5；数字字符串按 `float()` 同义；
  `float()` 拒绝的值（垃圾串、null、列表、对象）不提醒。单测钉住（`internal/server/stall_check_test.go`）。
  已知残余差异（极端字符串形态，未复现于配置/UI）：Go 的 `strconv.ParseFloat` 还接受十六进制浮点字面量
  （如 `"0x1p-1"`），Python `float()` 会抛异常（→ 不提醒）；其余已核对一致（前后空白、下划线、`nan`/`inf`）。
