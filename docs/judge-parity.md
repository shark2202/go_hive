# Judge 面归属与调用链对照（conversation_judge / SubagentJudge / go judge）

基线：`main` @ `00eeecc8`（worktree `.wt/judge-graph-scope`，分支 `judge-graph-scope`）
审计日期：2026-11-21
本页是 `appearance-scope-registry.json` 维度 `judge` 的证据物；所有 `file:line` 都在上述提交上逐条核对过。

方法：先读两侧源码定调用链（谁调用、输入来自哪些字段、在 graph 路径还是 colony 路径），再用三个可重放的探针把结论钉住：

| 探针 | 作用 | 命令 |
| --- | --- | --- |
| 接线门禁 | 「已对齐/生产可达」的断言：判定必须仍在主循环与宿主接线里 | `uv run --no-project python go_hive/scripts/wiring_check.py` |
| 范围探针（静态） | 「无宿主 / 无调用方」的断言：16 条计数，含跨语言 prompt 逐字核对 | `uv run --no-project python go_hive/scripts/judge_scope_probe.py` |
| 范围探针（动态） | 参考侧 Level 2/2b 真的会跑：真实 `AgentLoop` + 脚本化 LLM 三用例 | `PYTHONPATH=<repo>/core uv run python go_hive/scripts/judge_scope_probe.py --dynamic` |

## 1. 结论

| # | 机制 | 归属判定 | 一句话依据 |
| --- | --- | --- | --- |
| J1 | Level 0 短路：`skip_judge` 跳过判定，但仍发 `judge_verdict(implicit)` | **已对齐** | 两侧都有生产接线：Go `internal/server/chat_implicit_judge.go:35,51`、Python `judge_pipeline.py:76` + `agent_loop.py:3343` |
| J2 | Level 1 判定缝（`JudgeProtocol`）+ LLM criteria judge | **已对齐**（Go 侧另有扩展；失败回落语义经 judge-j2 车道实测后对齐，见 §3.2 末条） | Go 默认适配器接线于 colony/chat（3 处 `ConfigureJudge`）；prompt 与 Python `LLMJudge` 逐字一致（golden 钉住）。Python 侧该缝无生产注入，见 §3.2 |
| J3 | Level 2 隐式 output-key 门（缺 key 即 RETRY） | **未对齐（登记，不移植）** | Go 无 output-key accumulator、`set_output` 未接线；参考侧该门的输入也已恒空（§3.3） |
| J4 | Level 2b 会话质量判官 `conversation_judge.evaluate_phase_completion` | **范围外（登记）** | 唯一调用点在 `judge_pipeline.py:130`，触发条件是 graph 节点的 `success_criteria`+`llm`；Go 无 graph 运行时、无 phase/accumulator 宿主 |
| J5 | `SubagentJudge`（含迭代余量 urgency 反馈） | **范围外（登记，不移植）** | 参考侧无生产构造调用（仅 `core/tests/test_subagent.py`）；Go 的迭代预算位置由 grace 收尾占用且已对齐（§3.5） |
| J6 | stall-check judge（`noul`/typesafe 概率判官） | **已对齐**（另一条判定面，仅登记） | Go `internal/server/judge.go:22,65` + `stall_check.go:101`；Python `framework/server/judge.py:39` + `stall_check.py:87` |

## 2. 归属表

| 机制 | Python 调用链（谁调用 → 被调用） | 输入字段 | 路径 | Go 状态 | 依据 |
| --- | --- | --- | --- | --- | --- |
| J1 | `agent_loop.py:3325 _judge_turn` → `6223 judge_turn` → `judge_pipeline.py:76 if ctx.agent_spec.skip_judge → RETRY(feedback=None)`；事件 `agent_loop.py:3343 judge_type="custom" if self._judge else "implicit"` → `7459 _publish_judge_verdict` | `agent_spec.skip_judge` | graph 节点 + colony worker 都走（两条路径都用同一个 `agent_spec` 鸭子类型） | **有**：`chat_implicit_judge.go:35-74`（`noteImplicitJudgeVerdictDue`/`emitPendingImplicitJudgeVerdict`，`judge_type="implicit"`），门控见 `:64` `HasSuccessCriteria` | 探针 `go-implicit-verdict-event`；`wiring_check` judge 组 |
| J2 | `agent_loop.py:6223 judge_turn(judge=self._judge)` → `judge_pipeline.py:81-96`（`JudgeProtocol.evaluate`） | `assistant_text` / `tool_calls` / `output_accumulator` / `accumulator` / `iteration` / `conversation_summary` / `output_keys` / `missing_keys` | 缝存在于两条路径，但两侧生产构造都是 `judge=None`（§3.2） | **有（Go 扩展）**：`agentloop/judge.go:58 Judge` 接口、`:98-109 ConfigureJudge`（默认 `llm_judge.go:57 NewLLMJudgeAdapterFromCompleter`）、失败回落 `:169 evaluateJudgeWithRetry`、调用点 `loop.go:777-782` 与 `worker_stall.go:152-157`、判定应用 `loop_judge.go:17` | `wiring_check` judge 组 66 项（judge-j2 增 2：失败回落契约、瞬态分类）；`go test ./internal/agentloop -run Judge` |
| J3 | `agent_loop.py:3356` 与 `judge_pipeline.py:104-124`（`get_missing_output_keys_fn` 见 `6313-6323`） | `OutputAccumulator`（`internals/types.py:368`） | graph 节点（声明 `output_keys` 的 NodeSpec） | **无**：Go 无 `OutputAccumulator` 类型、无 `missing_keys` 门；`synthetic_tools.go:344/491/534` 的 set_output 家族仅定义无调用方 | 探针 `go-no-output-accumulator`、`go-set-output-unwired`；动态用例 3 |
| J4 | `judge_pipeline.py:126-138`（`if ctx.agent_spec.success_criteria and ctx.llm`）→ `conversation_judge.py:33 evaluate_phase_completion` → `:59 _extract_recent_context` + `:143 _format_outputs` | 最近 10 条消息（含 `set_output` 参数摘要）、`phase_name`/`phase_description`、criteria、accumulator 值；judge 预算 `max(1024, max_context_tokens//5)` | **graph 节点**（`orchestrator/orchestrator.py:764-766` 与 `node_worker.py:592-594` 都以 `judge=None` 建 loop）；触发字段可由 agent 配置抵达（`node.py:168` ← `agent_loader.py:968`） | **无宿主**：`internal/orchestrator` 只有 `buffer.go/safe_eval.go/validator.go`，无 graph/节点/边运行时，且**无人 import**；Go 无 phase 概念 | 探针 `go-no-graph-host`、`py-2b-single-call-site`、`py-criteria-config-reachable`；`internal/orchestrator` 全仓 0 引用 |
| J5 | `judge_pipeline.py:15-39 SubagentJudge.evaluate`（`:30 remaining`、`:33/:35 URGENT/WARNING` 文案）；别名 `agent_loop.py:715` | `context["missing_keys"]` + `iteration` + `max_iterations` | 需要一个把 `SubagentJudge()` 注入 `AgentLoop(judge=…)` 的调用方 —— **不存在**（仅测试） | **无对应物**：无 judge 实现、无 urgency 文案；迭代预算位置是 grace 收尾（`loop.go:58 graceReminderBody`、`:551 announceGrace`），两侧逐字对齐 | 探针 `py-subagent-no-production-caller`、`go-iteration-feedback-is-grace`；`core/tests/test_subagent.py:1-14` 自述为 legacy 遗留面 |
| J6 | `stall_check.py:87 judge.ask` → `judge.py:39 typesafe` | 槽位 `judge.{protocol,api_base,model,api_key,threshold}`、队列状态、固定问题/判据 | chat 会话（queen）旁路守卫 | **有**：`internal/server/judge.go:22 Protocol 表`、`:65 TypesafeJudge`、`stall_check.go:101 AskJudge` | `go test ./internal/server -run Judge`、`-run Stall` |

## 3. 逐机制展开

### 3.1 J1（已对齐）：`skip_judge` 的隐式判定事件

Python 的 queen 是「永远在线」会话：`agents/queen/nodes/__init__.py:772` 与 `server/queen_orchestrator.py:1636` 都设 `skip_judge=True`。判官短路（`judge_pipeline.py:76-77`，`feedback=None` 表示「没有真正评估，不要注入反馈」），但 6i 仍按 `judge_type="implicit"` 发事件（`agent_loop.py:3343-3352`）。
Go 的移植保留了事件、去掉了无意义的每轮评估：`chat_implicit_judge.go` 在下一回合开头补发上一回合的 `judge_verdict`（`judge_type="implicit"`、`action=RETRY`、空 feedback），并在有 criteria judge 时不再重复发（`:64`）。语义差异（何时发）已在文件头注明为移植口径。

### 3.2 J2（已对齐）：判定缝与 criteria judge

* Python 侧：`judge_pipeline.py:81-96` 是完整的 `JudgeProtocol` 缝，`agent_loop.py:6223` 传 `self._judge`。但生产构造点全部传 `judge=None`：`orchestrator/orchestrator.py:766`、`orchestrator/node_worker.py:594`、`host/colony_runtime.py:1477`（worker）与 `:2322`（overseer）、`server/queen_orchestrator.py:1611` 均未注入；`agent_loop.py:715` 的 `SubagentJudge` 别名也只被测试使用（§3.5）。即：**缝是生产可达的，注入方不是**。
* Go 侧：同一缝（`Judge` 接口）+ 生产默认实现（`LLMJudgeAdapter` → `internal/llmjudge`），并把 criteria 的来源补齐：worker/overseer 在自己的 spec 无 criteria 时回落到 colony goal 的 criteria（`colony/runtime.go:1195-1200`、`:1290-1296`，`GoalCriteriaText`），chat 会话绑定 colony 时继承同一批 criteria（`chat_runtime.go:1316-1328`，已标 `Parity-exempt: Go-only inheritance seam`）。
* prompt 逐字一致：`internal/llmjudge/judge.go:94-108` ↔ `core/framework/testing/llm_judge.py` 的 `LLMJudge.evaluate` 模板（205 字符）。本车道把它冻成 golden：
  `go_hive/go-hive/internal/llmjudge/testdata/prompt-parity/judge.prompt.txt`，由 `internal/llmjudge/prompt_parity_golden_test.go` 与 `go_hive/scripts/judge_prompt_golden.py` 两侧核对。
* 已知语义差异（judge-j2 车道：已实测并修复）：judge 调用失败时，Python 的 `_judge_turn` 先重试瞬态错误、预算耗尽后回落到 ACCEPT（`agent_loop.py:6202-6270`），本车道修复前 Go 把错误归一成 RETRY + `"Judge evaluation failed: …"`（原 `judge.go:148-153`）。两侧都先用真实纯函数实跑过同一矩阵（`go_hive/scripts/judge_j2_probe.py` ↔ `internal/agentloop/judge_j2_parity_test.go`），修复后 Go 对齐 Python 的缝契约：

  | 输入（judge.evaluate） | Python（实跑） | Go 修复前（基线实跑） | Go 修复后（测试） | 性质 |
  | --- | --- | --- | --- | --- |
  | 返回失败判定（RETRY），无异常 | RETRY，1 次调用，不重试 | RETRY，1 次 | RETRY，1 次 | 一致：判定失败不重试 |
  | 第 1 次抛瞬态异常，第 2 次 ACCEPT | **ACCEPT**（用第 2 次判定），2 次 | RETRY `"Judge evaluation failed: …"`，1 次 | ACCEPT，2 次 | **J2 差异（已修）**：瞬态重试 |
  | 第 1 次抛瞬态异常，重试返回 RETRY | RETRY，2 次（不再试第 3 次） | RETRY，1 次 | RETRY，2 次 | 动作同、机制不同（已修） |
  | 连续瞬态异常耗尽预算（默认 5 次） | **ACCEPT** + `"[judge unavailable after 5 attempts: TimeoutError; accepting to avoid stalling the loop]"`，5 次 | RETRY，1 次 | ACCEPT 回退，5 次 | **J2 差异（已修）**：fail-open 回落 |
  | 非瞬态异常（ValueError） | **上抛**，调用方拿到异常 | RETRY `"Judge evaluation failed: bad judge wiring"`，1 次 | 上抛（turn 带错结束），1 次 | **J2 差异（已修）**：不再伪装成 RETRY |
  | 返回 ESCALATE | ESCALATE，1 次 | ESCALATE，1 次 | ESCALATE，1 次 | 一致 |

* 修复落点（`internal/agentloop`）：`evaluateJudge` 现返回 `(JudgeVerdict, error)`，新 `evaluateJudgeWithRetry` 实现 `_judge_turn` 契约（`max(1, MaxStreamRetries)` 次尝试、`min(base*2**attempt, max_delay)` 退避、耗尽回退 ACCEPT、非瞬态上抛）；`isTransientJudgeError` 移植 `is_transient_error` 的类/关键字规则；`normalizeJudgeVerdict` 去掉 error 归一臂并把 RETRY 空反馈文案对齐为 `"Custom judge returned RETRY."`（`judge_pipeline.py:95`）；`LoopConfig` 补 `MaxStreamRetries`/`StreamRetryBackoffBase`/`StreamRetryMaxDelay`（上游 `types.py:173-175` 的镜像，默认 5/2.0/60.0）；`loop.go`/`worker_stall.go` 两处调用点把非瞬态错误作为 turn 错误返回。
* 上游自身的边界与矛盾（重试预算借用 stream 旋钮、`OSError` 家族的过宽分类、fail-open 语义与"参考侧不可达"）逐条登记在 §6.2，不猜测。

### 3.3 J3（未对齐，登记）：Level 2 output-key 门

参考侧的门本身是活的（`judge_pipeline.py:104-124`，`agent_loop.py:3356` 在 ACCEPT 后再查一次），但**输入恒空**：

* `OutputAccumulator.set`（`internals/types.py:377`）全仓 0 个调用点；
* 活循环把 `set_output` 直接拒掉：`agent_loop.py:4856-4860` 返回 `set_output is no longer available. Report your results via conversation instead.`；
* 连 `outputs_set_this_turn` 也 0 个写入点（`agent_loop.py:4003` 声明后无人 append）。

动态证据（探针用例 3）：声明 `output_keys=["findings"]` 的节点，两轮都收到 `Task incomplete. Required outputs not yet produced: ['findings']`，循环以失败结束——即该门在当前 runtime 下只能把回合推到 `max_iterations`。
Go 侧连宿主都没有（无 accumulator、`synthetic_tools.go:344 BuildSetOutputTool` 与 `:534 SyntheticPhaseToolBuilders` 仅定义无调用方）。**移植它等于移植一个两边都不成立的契约**，故只登记：等上游给 output key 接上写入点（或 Go 决定实现 output-key 面）时重开。

### 3.4 J4（范围外）：会话质量判官 `conversation_judge.evaluate_phase_completion`

* 调用链只有一条：`judge_pipeline.py:126-138`（隐式分支里，`judge=None` 时）→ `conversation_judge.py:33`。
* 触发条件只有一处：`ctx.agent_spec.success_criteria and ctx.llm`（`judge_pipeline.py:127`）。`success_criteria` 是 graph `NodeSpec` 字段（`orchestrator/node.py:168-174`），由 agent 配置映射进来（`loader/agent_loader.py:968`），因此**参考侧生产可达**（动态探针用例 1：判官被调用 2 次、RETRY 反馈进了对话、第二次 ACCEPT 结束回合）。仓库里没有随附配置设置 **node 级** `success_criteria`（`appearance-scope-registry.json` 等命中都是 goal 级 `success_criteria[]` 或架构文档，不是本触发字段），所以它是「可开未开」的机制，不是死代码。
* Go 侧没有宿主：`internal/orchestrator` 既无 graph 运行时（无 `GraphSpec`/`EntryNode`/`EdgeSpec`/节点执行器）也无人 import（只有 `internal/quality/size_test.go` 里作为字符串登记规模）；Go 的会话对象也没有 phase/accumulator 概念（`conversation.go:1406,1455` 只从消息里回显 `OUTPUTS SET: <key>`，没有值）。Go 的 criteria judge（J2）承担了「判定 criteria」的意图，但输入是 `NodeConversation.ExportSummary()` + 最终文本，而不是 phase + accumulator 值。
* 结论：**范围外**。若未来 Go 引入 graph 运行时，或 `conversation_judge` 的输入面出现 Go 等价物，重开本项。

### 3.5 J5（范围外，不移植）：`SubagentJudge` 与迭代余量 urgency

* 参考侧：`judge_pipeline.py:15-39`，它是一个 `JudgeProtocol` 实现——「output key 没填满就 RETRY，并按剩余迭代数升级文案」。但**没有生产调用方**：`SubagentJudge(` 在 `core/framework` 外只出现在 `core/tests/test_subagent.py:28,45,64,75`，测试文件自述「the old … mechanism is gone. … what survives is SubagentJudge」（`:1-14`）。
* 它的输入 `missing_keys` 来自 §3.3 那个恒空的 accumulator，因此文案在参考侧也不可达。
* 「迭代余量 urgency 在 Go worker 循环有没有等价位置」：**位置有，但已被另一条已对齐机制占用，且语义不同**——Go 的迭代预算反馈是 grace 收尾：`loop.go:520 limit := l.maxIterations()`、`:551 announceGrace`、文案 `:56 graceSkipMessage`/`:58 graceReminderBody`，与 Python `agent_loop.py:217/224` 逐字一致，并在 grace 里只放行 `report_to_parent/tracker_upsert/task_update`。SubagentJudge 的 urgency 是**限额前**（剩余 ≤3 / ≤max/2）的判官反馈，两侧活路径都没有这个触发点。
* 结论：**不倒代码**。若照它的文案在 Go 里新增「剩余 N 轮」提醒，移植的只能是字符串，触发语义（基于 missing_keys 的判官 RETRY）无处落地，等于发明新行为；且参考侧没有可对照的活路径。重启条件：上游把 `SubagentJudge` 接进任一活循环（或 Go 出现 output-key accumulator），届时按「judge 反馈 + 迭代余量」一起移植。

### 3.6 J6（已对齐）：stall-check judge

独立判定面：Python `framework/server/judge.py`（`typesafe` 协议，`/systemone` 的 noul 概率）+ `stall_check.py:87`；Go `internal/server/judge.go` + `stall_check.go:101`，含重定向、状态键序、失败回落等测试。本车道只做归属登记，不改动。

## 4. 本车道改了什么（可移植部分的判定结果）

**没有新增任何 Go 生产代码。** 判定：三个机制里（J3/J4/J5）没有一条能在 Go 找到「有触发语义的活位置」，强行移植只会产生无调用方代码或发明行为。可移植的部分是**可核对性**：

| 产物 | 类型 | 说明 |
| --- | --- | --- |
| `go_hive/scripts/judge_scope_probe.py` | 静态探针（16 条断言） | 把「无宿主 / 无调用方 / 已对齐」逐条变成计数；注释里的符号不算，canary 验证过会失败 |
| `go_hive/scripts/judge_python_probe.py` | 动态探针（3 用例） | 真实 `AgentLoop` 驱动 Level 2/2b 的可达性与 output-key 门现状 |
| `go_hive/scripts/judge_prompt_golden.py` + `internal/llmjudge/testdata/prompt-parity/judge.prompt.txt` | 跨语言 golden | judge prompt 逐字冻结（Python 侧提取模板、Go 侧 `buildPrompt("%s"×4)` 比对） |
| `internal/llmjudge/prompt_parity_golden_test.go` | 单测 | golden 漂移即失败（已用 1 字符漂移验证会红） |
| `go_hive/scripts/wiring_check.py`（新增 6 个接线点 / 17 项要求；judge-j2 车道再增 2 项，现 19 接线文件 / 66 要求） | 接线门禁 | 把「判定仍在主循环、宿主仍接线」纳入既有门禁 |
| `go_hive/docs/appearance-scope-registry.json`（新维度 `judge`） | 登记 | 6 个机制的状态与依据；J3 未对齐与 J4/J5 范围外的结论都在这里 |

## 5. 试验证据

Go（`CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod`，模块根 `go_hive/go-hive`）：

```
go test ./internal/llmjudge/ ./internal/agentloop/ ./internal/colony/ ./internal/server/ -count=1
  ok llmjudge / ok agentloop / ok colony / ok server      # 见 §6 的 flake 说明
go test ./internal/llmjudge/ -run JudgePrompt -v
  --- PASS: TestJudgePromptMatchesPythonGolden
  --- PASS: TestJudgePromptKeepsTheContractSections
```

Python（`PYTHONPATH=<repo>/core`，仓库 venv）：

```
python go_hive/scripts/judge_scope_probe.py --dynamic
  Level 2b: judge_calls=2 model_turns=2 accept=True
  no criteria: judge_calls=0 model_turns=1 accept=True
  output-key gate: model_turns=2 accept=False feedback="Task incomplete. Required outputs not yet produced: ['findings']"
```

门禁与探针：

```
python go_hive/scripts/wiring_check.py --root go_hive/go-hive
  wiring_check: OK —— 19 个接线文件、66 项要求全部命中（judge-j2 后）
python go_hive/scripts/wiring_check.py --self-test
  wiring_check self-test: OK —— 19 个接线文件、66 项要求，缺失会被报出
python go_hive/scripts/judge_scope_probe.py
  judge_scope_probe: OK —— 16 条静态断言全部与 docs/judge-parity.md 一致
python go_hive/scripts/judge_prompt_golden.py
  judge_prompt_golden: OK —— 205 chars match …
```

J2 失败回落（judge-j2 车道；纯函数级，不启服务、不调模型）：

```
# 参考侧：真实 AgentLoop._judge_turn + judge_pipeline.judge_turn + is_transient_error
PYTHONPATH=$PWD/core /Users/mac/hive/.venv/bin/python go_hive/scripts/judge_j2_probe.py
  {"case": "first-eval-fails(RETRY)", "action": "RETRY", "judge_calls": 1, ...}
  {"case": "transient-then-accept", "action": "ACCEPT", "judge_calls": 2, ...}
  {"case": "transient-then-eval-fails", "action": "RETRY", "judge_calls": 2, ...}
  {"case": "transient-exhausted(cap=2)", "action": "ACCEPT", "judge_calls": 2,
   "feedback": "[judge unavailable after 2 attempts: TimeoutError; accepting to avoid stalling the loop]", ...}
  {"case": "transient-exhausted(default cap=5)", "action": "ACCEPT", "judge_calls": 5, ...}
  {"case": "non-transient-raises", "raised": "ValueError: bad judge wiring", ...}
  {"case": "verdict-escalate", "action": "ESCALATE", "judge_calls": 1, ...}
  is_transient_error: TimeoutError/ConnectionError/OSError/FileNotFoundError/RuntimeError("429 …")=true,
    ValueError=false
  check: OK

# Go 侧（同一矩阵 + 分类表 + turn 级集成）
go test ./internal/agentloop/ -run 'JudgeFailureMatrix|JudgeTransient|JudgeNonTransient|JudgeRetryWithoutFeedback|JudgeRetryBackoff' -count=2 -v
  --- PASS: TestJudgeFailureMatrixMatchesPython/first-eval-fails(RETRY)
  --- PASS: TestJudgeFailureMatrixMatchesPython/transient-then-accept
  --- PASS: TestJudgeFailureMatrixMatchesPython/transient-then-eval-fails
  --- PASS: TestJudgeFailureMatrixMatchesPython/transient-exhausted(cap=2)
  --- PASS: TestJudgeFailureMatrixMatchesPython/transient-exhausted(default cap=5)
  --- PASS: TestJudgeFailureMatrixMatchesPython/non-transient-raises
  --- PASS: TestJudgeFailureMatrixMatchesPython/verdict-escalate
  --- PASS: TestJudgeTransientClassification (6/6 子用例)
  --- PASS: TestJudgeTransientFallbackKeepsTheLoopMoving
  --- PASS: TestJudgeNonTransientFailureEndsTurn
  --- PASS: TestJudgeRetryWithoutFeedbackUsesUpstreamSentence
  --- PASS: TestJudgeRetryBackoffFollowsUpstreamFormula

# 修复前的 Go 基线（同一矩阵在 7a14adf0 的 scratch 提取上实跑）
BASELINE transient-then-accept            action=RETRY  feedback="Judge evaluation failed: context deadline exceeded" calls=1
BASELINE transient-exhausted(default cap=5) action=RETRY feedback="Judge evaluation failed: context deadline exceeded" calls=1
BASELINE non-transient-raises             action=RETRY  feedback="Judge evaluation failed: bad judge wiring" calls=1
```

探针的可证伪性：放入两个 canary（Go 侧 `"Only 3 iterations left…"`、Python 侧 `SubagentJudge(`）后，`go-iteration-feedback-is-grace` 与 `py-subagent-no-production-caller` 立刻 FAIL；删除后恢复 OK。golden 侧把 `CONSTRAINT: ` 改成 `CONSTRAINT:`（删一个空格）即 FAIL，恢复后 PASS。

## 6. 登记：既有 flake（未修改）与 J2 上游语义边界

### 6.1 与 judge 无关的既有 flake（单列，未修改）

本车道跑宿主测试时命中 5 个（4 类）与 judge 无关的间歇失败。**这些测试与判定链不相干，本车道未改动它们**：触发的那几次运行里，`internal/server`、`internal/colony`、`internal/memory` 的生产文件与本车道提交前的基线逐字节相同（本车道唯一的 Go 改动是 `internal/llmjudge` 的 golden 测试 + `testdata`）。

观测条件三档：**顺序** = 本车道 4 包命令 `go test ./internal/llmjudge/ ./internal/agentloop/ ./internal/colony/ ./internal/server/ -count=1`（14 次）；**负载** = 同一命令 + 6×`yes`（10 次）；**隔离** = 单测 `-count=30`（server 两例、无负载）或单测 `-count=10`（colony 两例、6×`yes`）或 colony 包单独 `-count=1` + 6×`yes`（10 次）。

| 用例 | 位置 | 现象 | 复现率（观测） | 根因线索 |
| --- | --- | --- | --- | --- |
| `TestChatRuntimeRelaysClientMessageToOverseer` | `internal/server/chat_overseer_relay_test.go:129-133` | `Close()` 后断言「最后一条消息是 client user turn」，实际拿到 overseer 回执（`Role:"assistant", Content:"[overseer] the north field is clear"`） | 顺序 **2/14**；负载 **2/10**；隔离 `-count=30` **0/30** | 回执镜像走队列：`internal/server/chat_overseer_transcript.go:138 observeOverseerTranscriptEvent` → `:146 enqueueTurn(recordOnly)` → `:157-166 recordExternalAssistant` 追加 assistant 轮；镜像在 `Close()` 前落盘时「最后一条=用户轮」就不成立 |
| `TestChatRuntimeTurnDoneReflectsIntoQueenMemory` | `internal/server/chat_runtime_reflection_test.go:128-129` | `long memory = ""`：文件存在但内容为空 | 顺序 **1/14**；负载 **0/10**；隔离 `-count=30` **0/30** | 写方非原子：`internal/memory/reflection.go:303 os.WriteFile`（先截断后写）；读方 `chat_runtime_reflection_test.go:77-89 waitForPath` 一旦 `ReadFile` 成功就返回，可能读到 0 字节。仓库已有原子写助手：`internal/memory/timeline.go:185 atomicfs.WriteFile` |
| `TestPlaybookVerbatimStartedPayloadAndMessage` / `TestPlaybookFastGraceZeroReturnsStartedAndRecordsInBackground`（同一 helper，算一类） | `internal/colony/playbook_verbatim_test.go`、`internal/colony/playbook_grace_test.go:81`（等待 `waitForPlaybookRun`，helper `playbook_grace_test.go:34-49`） | 15s 内后台 run 行未落库：`background playbook run was never recorded (last error: <nil>)` | 顺序 **0/14**；负载 **2/10**（1+1，分别命中两例）；隔离：两例各 `-count=10` 负载 **0/10**、colony 包单独负载 10 次 **0/10** | 后台 playbook 写入与 15s 上限竞争；负载/包级并发下调度延迟超过上限即失败（失败耗时都是 15.1s，正好卡在 helper 的 deadline） |
| `TestPlaybookRunLogErrorRowHasNullName` | `internal/colony/playbook_verbatim_test.go:196` | `created_at = "2026-11-21T13:56:09.998789+00:00", want a Python isoformat UTC stamp` | 顺序 **0/14**；负载 **1/10**；隔离 `-count=10` 负载 **0/10**、colony 包单独负载 10 次 **0/10** | 断言用「断言时刻的 `time.Now()` 秒前缀」比对行时间戳：行时间戳若落在秒边界前 1-2ms（观测值正是 `.998789`），前缀天然不匹配——秒边界竞态 |

合计：顺序 14 次里 3 次失败（都在 server 两例）；负载 10 次里 5 次失败（server 2、colony 3）；隔离运行全部 0 失败。没有数据竞争报告、没有网络依赖，按「既有 flake」处理：未改动主线外代码；若要修，方向分别是「断言改为包含用户文本而非 last-message」「写侧换 `atomicfs.WriteFile`」「等待上限随负载放宽 / 行时间戳改用区间断言」。

### 6.2 J2 上游语义边界与矛盾（judge-j2 车道逐条登记）

以下每条都来自对参考侧真实函数的实跑或逐行核对（`go_hive/scripts/judge_j2_probe.py`、`agent_loop.py:6202-6270`、`tool_result_handler.py:72-122`）；登记的是上游口径本身，不是 Go 的猜测：

| # | 上游口径 | 证据 | 本车道处置 |
| --- | --- | --- | --- |
| 1 | **judge 重试预算借自 stream 旋钮**：`max_attempts = max(1, self._config.max_stream_retries)`，退避用 `stream_retry_backoff_base`（2.0）/`stream_retry_max_delay`（60.0）。stream 重试参数同时决定 judge 尝试次数；取 0 时 judge 只有 1 次机会。 | `agent_loop.py:6220`；探针 cap=2/5 两行（调用次数 2/5） | Go 镜像同一组 `LoopConfig` 字段（默认 5/2.0/60.0），公式 `max(1,·)` 与 `min(base*2**attempt, max)` 逐条照抄；`TestJudgeRetryBackoffFollowsUpstreamFormula` 钉住 |
| 2 | **"瞬态"分类宽于字面**：`is_transient_error` 把整个 `OSError` 家族判为瞬态，包括 `FileNotFoundError`/`PermissionError`；这样一个永久性接线/路径错误会被重试 5 次（约 30s 退避）后自动 ACCEPT，掩盖缺陷。 | 探针分类表：`FileNotFoundError` → `transient=true`；`tool_result_handler.py:93-96` | Go 只保留 类→类 映射（`context.DeadlineExceeded`、`net.Error`、连接类 syscall errno、关键字表），**刻意不复刻 `PathError` 分支**；这是本车道唯一有意保留的语义差异，登记于此（探针/测试枚举的分类行两侧一致） |
| 3 | **回退是"隐式跳过判定"**：预算耗尽后的 ACCEPT 不携带任何评估结论；fallback feedback 只进 judge_verdict 事件与运行日志，**不注入对话**。与"ACCEPT 表示 criteria 通过"的语义冲突，上游注释自述为 "skipping judgment and accepting the turn to keep moving"。 | `agent_loop.py:6238-6251`；调用点在 ACCEPT 分支只写 `verdict_feedback`（`:3401`） | Go 原样移植（fail-open，feedback 事件同步、对话不注入），未改上游口径 |
| 4 | **该路径在参考侧不可达**：生产构造点全部 `judge=None`；唯一 `JudgeProtocol` 实现 `SubagentJudge` 从不抛异常；`LLMJudge` 不是 `JudgeProtocol`（`evaluate(constraint,…)` 签名不匹配）且把自身异常吞成 `passes=False`。因此"重试→ACCEPT"是**缝契约**，不是参考侧活路径。Go 的默认适配器 `LLMJudgeAdapter` 同样不返回 error（评估失败→失败判定），故本次对齐在两侧都是契约级语义。 | `judge_pipeline.py:15-39`、`llm_judge.py:124-125`；§3.2 的生产构造清单 | 语义按契约移植（这正是 J2 差异的对象）；是否给适配器加"评估失败→error"的分类另立 lane，不在本车道猜 |
| 5 | **非瞬态失败即结束整个 turn**：上游只兜瞬态；非瞬态 `raise` 由调用方处理（`execute` 里没有包住 `_judge_turn` 的 try，异常直接冒出去）。 | `agent_loop.py:6236-6253`（无 try 包裹，`raise` 直出）；探针 `non-transient-raises` 行 | Go 落为 `ExecuteTurn` 返回该错误（`loop.go`/`worker_stall.go` 两处），不再把错误伪装成 RETRY 反馈 |

## 7. 复现

```bash
# 1. 静态范围断言（离线，仅标准库）
uv run --no-project python go_hive/scripts/judge_scope_probe.py --verbose

# 2. 动态（参考侧真实循环）
PYTHONPATH=$PWD/core /Users/mac/hive/.venv/bin/python go_hive/scripts/judge_scope_probe.py --dynamic

# 3. prompt golden
uv run --no-project python go_hive/scripts/judge_prompt_golden.py            # check
uv run --no-project python go_hive/scripts/judge_prompt_golden.py --write    # regenerate

# 4. 接线门禁 + Go 判定测试
uv run --no-project python go_hive/scripts/wiring_check.py --root go_hive/go-hive
cd go_hive/go-hive && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/agentloop/ ./internal/colony/ ./internal/llmjudge/ ./internal/server/ -run 'Judge|Stall' -count=1

# 5. J2 失败回落矩阵（纯函数级：不启服务、不调模型）
PYTHONPATH=$PWD/core /Users/mac/hive/.venv/bin/python go_hive/scripts/judge_j2_probe.py
cd go_hive/go-hive && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/agentloop/ -run 'JudgeFailureMatrix|JudgeTransient|JudgeNonTransient|JudgeRetryWithoutFeedback|JudgeRetryBackoff' -count=2 -v
```

重启条件（写进登记册 `next`）：① Go 出现 graph 运行时或 phase/accumulator 宿主 → 重开 J3/J4；② 上游把 `SubagentJudge` 接进活循环 → 重开 J5；③ `judge_prompt_golden.py` 报漂移 → 重开 J2 的 prompt 对齐；④ judge 失败回落语义已由 judge-j2 车道按 `_judge_turn` 契约对齐（§3.2 末条），若上游改 `_judge_turn` 的预算/分类/回退文案，`judge_j2_probe.py` 与 `judge_j2_parity_test.go` 会红并重开本项。
