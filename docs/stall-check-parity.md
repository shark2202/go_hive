# stall check / judge 双实现实测（真实 startlux judge）

本文件记录 Python（`core/framework/server/stall_check.py` + `judge.py`）与 Go
（`internal/server/stall_check.go` + `judge.go`）在**真实 judge 服务**下的对照实测：
同一个脚本化女王回合、同一个 judge 端点，逐场景观察两侧发出的 judge 请求体、返回概率、
阈值判定、提醒注入、第二轮补做与降级行为。

- 日期：2026-11-21
- 工作树：`.wt/judge-stall-real`，基线 `main c4eb266d`
- judge：本机 `startlux-decide`，`http://127.0.0.1:18099`（TypeSafe `POST /v1/systemone`，
  模型 `0.8B-Instant-MLX-4bit`）；健康检查 `{"status":"ok", "model":"0.8B-Instant-MLX-4bit", …}`
- 女王模型：脚本化确定性网关 `go_hive/go-hive/e2e/cmd/stallgw`（同一回答同时喂给两侧）——
  本 lane 验证**判定/注入管线**；真实模型的命中率不在结论内
- judge 记录：录制代理转发到真实服务，逐字记录请求体/响应体/耗时
- 证据：`go_hive/docs/e2e-assets/judge-stall-real/`
- 复现：

```bash
# 先确认真实 judge 在跑（curl http://127.0.0.1:18099/v1/health）
cd .wt/judge-stall-real
uv run --offline python go_hive/scripts/judge_stall_real.py \
    --out go_hive/docs/e2e-assets/judge-stall-real
```

## 1. 场景结果（16/16 符合预期）

`p` 是真实 judge 对 `questions.answer`（`type=noul`）返回的概率；阈值为 judge 槽的
`threshold`。两侧收到的是**同一份 state**（脚本化同一回复/同一工具）。

| 场景 | threshold | Python: judge 请求 / p / 提醒 / 第二轮 | Go: judge 请求 / p / 提醒 / 第二轮 |
| --- | ---: | --- | --- |
| 说而不做（stall） | 0.5 | 1 / 0.936458 / 是 / 是 | 1 / 0.936458 / 是 / 是 |
| 说而不做（stall） | 0.7 | 1 / 0.936458 / 是 / 是 | 1 / 0.936458 / 是 / 是 |
| 阈值间回复（borderline） | 0.5 | 1 / 0.672394 / 是 / 是 | 1 / 0.672394 / 是 / 是 |
| 阈值间回复（borderline） | 0.7 | 1 / 0.672394 / 否 / 否 | 1 / 0.672394 / 否 / 否 |
| 有工具调用（get_current_time） | 0.5 | 1 / 0.020912 / 否 / 否 | 1 / 0.020912 / 否 / 否 |
| 开关关闭（judge 槽仍配置） | 0.5 | 0 / — / 否 / 否 | 0 / — / 否 / 否 |
| judge 不可达（`127.0.0.1:9`） | 0.5 | 0（连接失败）/ — / 否 / 否 | 0（连接失败）/ — / 否 / 否 |
| 无用户消息（外部事件回合） | 0.5 | 0 / — / 否 / 否 | 0 / — / 否 / 否 |

- “提醒”指转录中出现 `[External event] <system-reminder>…` 且该消息在下一轮模型请求里逐字可见；
- “第二轮”指提醒后的补做回合（脚本网关回 `整理好了。`）；
- 逐场景证据：`cases/py-<case>/`、`cases/go-<case>/`（`summary.txt`、`judge-requests.jsonl`、
  `queen-requests.jsonl`、`transcript.json`、`session-events.jsonl`、`sse-reminder-events.jsonl`、`server.log`）。

## 2. judge 请求面：两侧逐字段一致

两侧 `POST {api_base}/systemone` 的请求体都含且仅含：

```json
{"state": {"previous_turn": {"assistant_reply": "...", "tools_called": []},
           "user_message": "...", "assistant_reasoning_tail": "",
           "assistant_reply": "...", "tools_called_this_turn": []},
 "model": "0.8B-Instant-MLX-4bit",
 "questions": {"answer": {"type": "noul", "instructions": "<stall_check._QUESTION>",
                          "criteria": {"true": "...", "false": "..."}}}}
```

- `state` 截断规则：`user_message` 尾 2000 字符、`assistant_reasoning_tail`/`assistant_reply`
  尾 1500、`previous_turn.assistant_reply` 尾 600；两侧相同。
- `body-comparison.json`：5 个会问 judge 的场景，两侧请求体**语义相等**（解析后 deep-equal），
  返回的 `p` 也逐位相等。Authorization 均为 `Bearer <槽里的 api_key>`。
- Python 侧 `judge.py::PROTOCOLS` 的 `typesafe` 支持**实测存在且可用**（上表全部 p 均由真实
  `/v1/systemone` 返回），不存在“Python 缺 typesafe”的待修项。

### 2.1 修复的差异：Go 的 state 键序

真实 judge 按 **state 键的到达顺序**渲染提示词，同一语义状态会给出不同概率。Go 原实现把
`state map[string]any` 交给 `encoding/json`（键按字典序输出），Python 按 `stall_check.check`
的插入序输出，实测同一回合：

| 侧 | state 键序 | p |
| --- | --- | ---: |
| Python | `previous_turn, user_message, assistant_reasoning_tail, assistant_reply, tools_called_this_turn` | 0.936458 |
| Go（修复前） | 字典序：`assistant_reasoning_tail, assistant_reply, previous_turn, tools_called_this_turn, user_message` | 0.923282 |

同一 body 重复发送，p 完全稳定（不是采样噪声）；只改 state 键序即可复现两个值。行为上两者都
≥0.5/0.7，但阈值附近会翻转，属于真实差异。

修复：`internal/server/judge.go` 新增 `marshalJudgeState`，按上游插入序输出已知键（其他 judge
调用方的未知键仍按字典序追加），并加单测
`internal/server/judge_test.go::TestTheTypesafeJudgeKeepsTheUpstreamStateKeyOrder` 钉住字节序。
修复后两侧 p 完全一致（0.936458 / 0.672394 / 0.020912）。

> 顶层 `state/model/questions` 顺序与 `criteria` 的 `true/false` 顺序对 p 无影响（实测），
> 只有 `state` 内部键序影响；因此只修 state。

## 3. 同一请求重复 3 次：p 稳定

`judge-stability.json`：把两侧实际捕获的 stall / borderline 请求体各重放 3 次到真实端点。

| 请求 | 3 次 p |
| --- | --- |
| Python stall | 0.936458, 0.936458, 0.936458 |
| Go stall | 0.936458, 0.936458, 0.936458 |
| Python borderline | 0.672394, 0.672394, 0.672394 |
| Go borderline | 0.672394, 0.672394, 0.672394 |

真实 judge 对同一请求是确定性的；修复键序后两侧对同一回合得到同一概率。

## 4. 阈值行为（0.5 vs 0.7）

同一 borderline 回复（“收到，我先看看。”）在真实 judge 下 p=0.672394：

- threshold=0.5：`p >= 0.5` → 注入提醒 → 第二轮补做（两侧一致）；
- threshold=0.7：`p < 0.7` → 不提醒，回合原样结束（两侧一致）。

stall 回复 p=0.936458 在两个阈值下都触发（0.7 也挡不住强承诺）。这验证了文档里“误报多就调高
阈值”的建议在真实服务上成立。

## 5. 负路径与降级

- **有工具调用**：网关先发 `get_current_time` 工具调用、执行后回最终文本；judge 看到
  `tools_called_this_turn:["get_current_time"]`，返回 p=0.020912 → 不提醒（两侧一致，转录含 1 个
  tool 片段）。
- **开关关闭**：`stall_check=false` 时 `judge_slot()` 直接返回 None，judge 0 次调用；女王照常回复。
- **judge 不可达**：把槽的 `api_base` 指向死端口（`http://127.0.0.1:9/v1`）。回合正常完成、无提醒、
  服务保持健康，日志各一行：
  - Python：`judge: unavailable (ConnectError: All connection attempts failed)`
  - Go：`judge: unavailable (Post "http://127.0.0.1:9/v1/systemone": dial tcp 127.0.0.1:9: connect: connection refused)`
- **无用户消息**：向 parked 会话 `POST /api/sessions/{id}/queen-context`（`is_client_input=false`），
  女王产生一个外部事件回合（转录 `[External event] …`、`is_client_input` 缺省=false），回合结束时
  `turn.user == ""` → 两侧都不问 judge（0 次请求）。会话启动回合（skills 块）同样不计入。

## 6. 提醒的观测面（SSE / 转录）

- 两侧与判定相关的 part 一致：用户消息 `is_client_input=true`；提醒是 user part
  `[External event] <system-reminder>…</system-reminder>`，`is_client_input` 缺省（=false）；
  随后是补做的 assistant part。`transcript.json` 可直接比对（两侧的会话起始工具清单块与
  `get_current_time` 返回 JSON 的键序/时间戳不同，属工具目录/工具实现，不进 judge state）。
- 提醒文本在下一次女王模型请求里逐字可见（`queen-requests.jsonl` 的 `has_reminder=true`）。
- **事件面差异（未修，记录）**：Go 的会话 SSE 在提醒回合发出
  `execution_started`（`data.input.message` = 提醒全文，`sse-reminder-events.jsonl` 1 条）；
  Python 会话 SSE 没有承载提醒文本的事件（0 条），UI 靠转录/session snapshot 得到它。
  两者判定与注入结果相同，只是观测路径不同；属于 chat emitter 的事件面，不在 stall_check 内。

## 7. 字段面：`/api/config/features`

两侧返回同形 JSON：

```json
{"features": {"adaptive_tool_budget": true, "email_senders": false,
              "memory_timeline": false, "stall_check": true}}
```

开关关闭时为 `"stall_check": false`；其余字段两侧一致（`cases/*/summary.json` 的
`features`）。Go 的 `config_features.go` 与 Python `routes_config.py::_FEATURE_KEYS` 同键同义。

## 8. 关于真实 judge 的观察（非缺陷，供调参）

用捕获的真实 state 探不同回复（同一 user、同一 previous_turn，各 3 次稳定）：

| 回复 | p |
| --- | ---: |
| “好的，我这就去整理，整理完马上告诉你。” | 0.9365 |
| “收到，我先看看。” | 0.6724 |
| “好的。” | 0.6609 |
| “抱歉，我现在没法帮你整理提醒事项里的清单。” | 0.5190 |
| “你想先整理工作待办还是生活待办？” | 0.9104 |
| 工具已调用（final “现在是下午 4 点 05 分。”） | 0.0209 |

0.8B 判定模型对“没有完成证据”的回复普遍给高概率（连澄清式提问也 0.91），只有本轮确有工具调用
才压到 0.02。默认阈值 0.5 会经常提醒；0.7 明显更保守。这与 `stall-check.html` 的调参建议一致，
但说明它更适合“抓承诺”，不适合当质量门禁。

## 9. 残余差异清单

| 项 | 状态 |
| --- | --- |
| Go state 键序导致同回合 p 不同（0.9233 vs 0.9365） | **已修**（`judge.go::marshalJudgeState` + 单测） |
| Go SSE `execution_started` 携带提醒文本，Python 无对应事件 | 记录（事件面差异，不影响判定/注入） |
| 顶层 JSON 键序、`criteria` 键序 | 无行为影响（实测 p 相同），不修 |
| 女王模型为脚本化网关 | 本 lane 的边界；真实模型命中率需另测 |
| Python judge 槽 typesafe 支持 | 实测存在且可用，无缺项 |

## 10. 证据索引

- `go_hive/docs/e2e-assets/judge-stall-real/summary.md` / `summary.json`：16 个用例汇总与 PASS 判定
- `body-comparison.json`：5 个 judge 场景的两侧请求体 deep-equal 与 p
- `judge-stability.json`：3 次重放
- `judge-requests.jsonl`：所有真实 judge 调用的请求/响应/耗时（按 case 打 tag）
- `cases/<side>-<case>/`：转录、会话事件、SSE 提醒事件、女王模型调用摘要、服务日志、配置
- 复现脚本：`go_hive/scripts/judge_stall_real.py`（脚本化女王 + 录制代理 + 真实 judge）
