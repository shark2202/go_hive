# 限额（并发 / 预算 / 轮次）两侧对照与实测

基线：`main` @ `4af5b375`（worktree `appearance-limits`）· Go 1.0.26-go.1 · Python hive 1.0.14（core 树）
被测代码提交：`8bec824e`（证据在同一棵树的 `dist/hive` 上采集；后续只改文档）
实测日期：2026-11-21 · 模型：`deepseek-flash-1017`（真实厂商，经 `scripts/real_llm_proxy.py` 记录型直通代理，无假响应）
本页是 `appearance-scope-registry.json` 维度 #20 `limits` 的 `measured_now` 指向物。

口径：每条给「两侧默认值 / 读取点 / 越界行为」，并在 §2 给本次真实 serve 的「设了限额 → 观测到效果」证据。
`↔` 表示两侧同一表达式；行末状态只标本条的证据等级（实测 / 单测 / 未验）。

## 1. 限额清单

### 1.1 并发

| # | 限额 | Python 默认 / 读取点 | Go 默认 / 读取点 | 越界行为 | 状态 |
| --- | --- | --- | --- | --- | --- |
| C1 | playbook `meta.concurrency` 默认 | `8`（`HIVE_DEFAULT_PLAYBOOK_CONCURRENCY`，模块导入时读取）`tools/playbook_tools.py:207` → `_resolve_concurrency` `:210-226` | ↔ `8`（同 env，包初始化读取）`internal/playbook/rows.go:250` → `ResolveConcurrency` `:253-271` | 非整数：`{"status":"failed",…,"error":"meta['concurrency'] must be an integer, got …"}`；`<1` 夹到 `1` | 单测＋逐字 payload（`TestPlaybookVerbatimConcurrencyFailurePayloadMatchesUpstream`） |
| C2 | playbook 并发硬顶 | `32`（`HIVE_MAX_PLAYBOOK_CONCURRENCY`）`tools/playbook_tools.py:206` | ↔ `32` `internal/playbook/rows.go:249` | `>32` → `{"status":"failed",…,"error":"meta['concurrency']=N exceeds the maximum 32. Lower it, split the batch, or raise HIVE_MAX_PLAYBOOK_CONCURRENCY."}`（两侧逐字） | 逐字 payload 单测（同上） |
| C3 | colony worker 并发 | `4`（`HIVE_MAX_CONCURRENT_WORKERS`）`core/framework/host/colony_runtime.py:141` → `ColonyConfig.max_concurrent_workers` `:231` | ↔ `4` `internal/colony/scheduler.go:15`；`RuntimeOptions.MaxConcurrentWorkers` `internal/colony/runtime.go:61` | 超出部分进队列等待（不拒绝）：`spawner` 排队并回报 `queued` | 单测（`colony` 包）＋ live 抬高实验（C4） |
| C4 | playbook run 期间抬高 colony 上限 | `colony_config.max_concurrent_workers = max(原值, concurrency)`，run 结束恢复 `tools/playbook_tools.py:421-435` | ↔ `internal/colony/playbook_tools.go:354-375`（`colony_config` 语义同名；可调 gate `worker_slots.go`） | run 结束（快速路径/后台 finally）恢复 `max(原值, 0)…`（原值 ≤ 声明值时写回 `concurrency`） | 单测：`TestPlaybookVerbatimConcurrencyCapRaiseAndRestore`、`TestPlaybookCapRaiseAdmitsDeclaredConcurrency`（本页 §2.4 复跑） |
| C5 | 只读并发工具批上限 | `10`（内联 `asyncio.Semaphore(_PARALLEL_CAP = 10)`，仅 `concurrency_safe` 工具批）`agent_loop/agent_loop.py:5548` | `10`（本车道接线：`parallelReadOnlyCap`，`internal/agentloop/loop.go:905`） | 两侧都只对「显式标记安全」的调用放并发；未标记整批串行 | 单测：`TestParallelReadOnlyBatchCapsInflightAtTen`（新增）、`…RunsConcurrentlyAndKeepsOrder` |
| C6 | 事件总线并发处理器 | `10`（`hive.event_bus` 默认）`core/framework/host/event_bus.py:422` | ↔ `10` `internal/eventbus/bus.go:99` | 超过排队（信号量），不丢事件 | 单测 |

C5 备注：上游把工具批拆成「并发安全批 + 串行批」两段；Go 保持整批（全部 safe 才并发）并共享同一 10 上限——这是两侧**有意**的调度差异（Go 侧 `executeToolBatch` 顶部 `Parity-exempt` 说明），本车道把上限值补齐。

### 1.2 轮次

| # | 限额 | Python 默认 / 读取点 | Go 默认 / 读取点 | 越界行为 | 状态 |
| --- | --- | --- | --- | --- | --- |
| T1 | 裸 `LoopConfig.max_iterations` | `50` `agent_loop/internals/types.py:52` | ↔ `50` `internal/agentloop/loop_config.go:96` | 用尽即结束：Python `exit_reason="max_iterations"`＋`Max iterations (N) reached without acceptance`（`agent_loop.py:3518`）；Go `exceeded N inner turns`（`internal/agentloop/loop.go:824`） | 单测；live 见 T2 |
| T2 | queen 会话轮次上限 | `999_999`（`core/framework/agents/queen/agent.py:21,32`；colony 相位同值） | `50`（`DefaultLoopConfig`，`chatLoopConfig` 未改则保持） | 见 §3 差异 D1/D2：两侧默认值有意不同；`HIVE_MAX_ITER` 的效力也两侧不同 | **实测**（§2.3） |
| T3 | `HIVE_MAX_ITER` | 读取点 `core/framework/server/queen_orchestrator.py:1589-1597`：`int(raw) if raw.isdigit() else 999_999`，**但被 profile 的 `max_iterations` 键遮蔽、实际不生效**（§2.3） | 生效：正数（trim 后 `>0`）覆盖 chat loop 上限 `internal/server/chat_memory_wiring.go:63-65`；否则保持 50 | Python：0/负数/带空白都回落到 999_999 且不改变行为；Go：非正/非数字视为未设置 | **实测**（§2.3）：`HIVE_MAX_ITER=1` 时 Python 仍报 `999999`；Go 报 `1` 并 `execution_failed` |
| T4 | worker 轮次 | `3` + grace `1` `core/framework/agents/queen/worker_definition.py:70-77` | ↔ `internal/colony/worker_definition.go:9-16`；`BuildWorkerLoopConfig` `internal/colony/worker_loop_config.go:51-56` | 用尽后进入 grace（仅 `report_to_parent` 等终局工具可执行），grace 用完终止 | 单测（`worker_loop_config_test.go`、`test_loop_config_overrides.py`） |
| T5 | worker 每回合工具调用预算 | `30`，硬停 = `30×3 = 90` `worker_definition.py:73-75` | ↔ `30` / hard multiple `3`（`ToolCallHardLimit=0` → 走倍数规则） | 软检查点每 30 次提醒；越过硬停值后本回合剩余调用被延后 | 单测（`soft_budget_test.go`、`worker_loop_config_test.go`） |
| T6 | queen 每回合工具调用预算 | `30`，硬停 `30×5 = 150`；colony 相位 `50×10 = 500`（`agent.py:20-36`，`queen_orchestrator.py:1598-1599`） | `30`，硬停固定 `50`（`loop_config.go:128-129`，审计口径「Queen 30/50」） | 越过后剩余调用延后到下一回合 | 单测；差异 D3（有意） |
| T7 | worker 生命周期工具预算 | `200` `worker_definition.py:75` | ↔ `200` `worker_definition.go:14` | 达到即提前进入 grace 收尾 | 单测 |
| T8 | worker `loop_config_overrides` 边界 | `max_iterations` 1–1000、`grace_iterations` 0–3、`tool_call_budget` 0–200、`tool_call_lifetime_budget` 0–2000、`max_context_tokens` 1000–1_000_000，越界报错 `loop_config override 'k'=v out of range [lo, hi]` `core/framework/host/colony_runtime.py:68-82` | ↔ 同表 `internal/colony/worker_loop_config.go:27-47` | 越界 → `ValueError`/`LoopOverrideError`（拒绝 spawn） | 单测 |

### 1.3 token / 预算 / 截断

| # | 限额 | Python 默认 / 读取点 | Go 默认 / 读取点 | 越界行为 | 状态 |
| --- | --- | --- | --- | --- | --- |
| B1 | 辅助 LLM 输出预算 `llm.aux_max_tokens` | `min(8192, llm.max_tokens)` `core/framework/config.py:690-705` | ↔ `internal/server/config_slots.go:461-471`，进程级安装 `llm.SetAuxMaxTokens`（`internal/cli/loader.go:297`），落点 `internal/llm/complete.go:44-80` | 配置值原样生效（file 值不夹取）；未配置回落 8192/主模型上限 | **实测**（§2.2）：两侧 title 请求 `max_tokens=512`；单测 `TestConfiguredAuxMaxTokensReachesTheJudge`、`TestTitleBudgetFollowsConfiguredAuxMaxTokens`(新增) |
| B2 | 工具结果溢出阈值 `loop.max_tool_result_chars` | `30_000` `core/framework/config.py:708-717`；queen/worker 都走 `get_max_tool_result_chars`（`queen_orchestrator.py:1604`、`worker_definition.py:285`） | ↔ `30000`（`loop_config.go:152`）；`hiveconf.MaxToolResultChars` 应用在 queen chat（`chat_memory_wiring.go:52`）与每个 worker loop（`worker_loop_config.go:74`） | `len>limit` 且配置了 spillover 目录：落盘全文，消息换成「头部 + 5000 切片预览」；`limit<=0` 不截断 | **实测**（§2.1） |
| B3 | 截断信封形状 | 头部两行（`Tool … returned N characters (too large for context). Full result at: <abs>` + ``grep -nE "<pattern>" "<abs>"` via terminal_exec`）+ 抽样警告 + `Preview (truncated):` | ↔ 同形（`internal/agentloop/tool_result_spillover.go:104-110`；本车道补齐 grep 提示行） | JSON 结果另有 Python 独有的 `Data structure:` 结构摘要与数组感知预览；Go 是纯文本切片（差异 D5） | **实测**（§2.1，四段标记两侧齐备） |
| B4 | 上下文窗口 `max_context_tokens` | 默认 `180_000`（`types.py:77`）；配置链 `llm.max_context_tokens`/queen section/模型目录 `config.py:592-687` | ↔ `180_000`（`loop_config.go:97`）；`QueenMaxContextTokens`/`WorkerMaxContextTokens`（`chat_memory_wiring.go:54`、`worker_loop_config.go:76`） | 窗口越小压缩越早触发（见 B5） | 单测 |
| B5 | 压缩触发（上下文阈值） | 混合缓冲：`budget = max_context − (8_000 + 0.4×max_context)`，另 12_000 提前告警（`types.py:85,107,110`；`conversation.py:1258-1286`） | ↔ 本车道接线：`LoopConfig.CompactionBuffer{Tokens,Ratio,WarningBufferTokens}` 默认 `8_000/0.4/12_000`，每次 `ExecuteTurn` 推给会话（`loop.go:342-371,543`） | 达到预算即先压缩再发请求；`Both=0` 时退回旧 0.8 乘法阈值 | 单测：`TestExecuteTurnAppliesLoopContextLimits`(新增)＋既有 `NeedsCompaction*` 系列 |
| B6 | 会话花费上限 | 无此旋钮 | Go 独有：`HIVE_SESSION_COST_LIMIT_USD`（默认 `0`=关闭）`internal/agentloop/session_cost.go:26-38` | 超过上限后下一次 `ExecuteTurn` 直接返回 `*SessionCostLimitError`（不发模型请求） | 单测（`session_cost_test.go`）；差异 D4（Go 扩展） |
| B7 | 终端输出内联上限 `max_output_kb` | 默认 `256`（`tools/src/terminal_tools/exec.py:66`）；`max(1024, kb×1024)` 字节/流 `common/truncation.py:47,63` | ↔ `256`（`internal/tools/builtin/builtin.go:339`）；`max(1024, kb*1024)` `exec.go:217-224` | 溢出流全文进 `output_handle`，可用 `terminal_output_get` 取回；非整数参数报 `terminal_exec: max_output_kb must be an integer` | 单测两侧（本页 §2.4 复跑）：`TestTerminalExecTruncatesOutput` / `test_truncation_via_handle` |

### 1.4 其他（类目/行数上限）

| # | 限额 | Python 默认 / 读取点 | Go 默认 / 读取点 | 越界行为 | 状态 |
| --- | --- | --- | --- | --- | --- |
| O1 | `worker_tools.always_enabled_categories`（类目 keep-set 覆盖，非数值上限） | 列表（全字符串）时**替换**硬编码 keep-set；缺省/混合列表回落默认 `core/framework/agents/queen/queen_tools_defaults.py:452-467` | ↔ `internal/server/config_slots.go:483-528`（空列表=关闭 worker 分层） | 非法形状忽略（回落默认），不报错 | 单测 |
| O2 | `queen_tools.always_enabled_categories` | 同上，作用于默认工具目录 `queen_tools_defaults.py:384-403` | ↔ `internal/server/routes_queens_tools.go:513-524` | 同上 | 单测 |
| O3 | playbook 单次待查行上限 | `_PENDING_ROW_CAP = 100_000` `tools/playbook_tools.py:193`（超过时 `query_rows` 告警） | 同语义查询路径（playbook runner） | 不静默截断：超过只告警 | 单测 |

## 2. 实测：设了限额 → 观测到效果

驱动脚本：`go_hive/scripts/limits_real_e2e.py`（`HIVE_REAL_LLM_E2E=1`，真实 serve + 真实模型经记录代理）。
产物：`go_hive/docs/e2e-assets/limits/limits-real-e2e.json`、`go-truncated-tool-result.txt`、`python-truncated-tool-result.txt`。
复现：

```sh
cd go_hive/go-hive && CGO_ENABLED=0 GOPROXY=off go build -o dist/hive ./cmd/hive
cd .. && set +x && source ~/.hive-go/env   # BROADTOKEN_API_KEY
HIVE_REAL_LLM_E2E=1 uv run --no-project python scripts/limits_real_e2e.py --sides go,python
```

### 2.1 `loop.max_tool_result_chars=200` + 长输出工具（B2/B3）

剧本：两个临时 HIVE_HOME 都设 `loop.max_tool_result_chars=200`、`llm.aux_max_tokens=512`；让 queen 用 `terminal_exec` 跑 `python3 -c "print('Z'*12000)"`。

| 观测 | Go | Python |
| --- | --- | --- |
| 工具结果原始字符数 | 12,064 | 12,333 |
| LLM 可见内容字符数 | 5,608 | 6,013 |
| 首行 | ``Tool `terminal_exec` returned 12,064 characters (too large for context). Full result at: <abs>`` | ``Tool `terminal_exec` returned 12,333 characters (too large for context). Full result at: <abs>`` |
| 恢复提示 / 抽样警告 / 预览标记 | ✓ / ✓ / ✓ | ✓ / ✓ / ✓ |
| 落盘全文 | 12,064 bytes（原样） | 12,367 bytes（JSON 美化） |

结论：两侧同一阈值下都在同一点溢出、同形信封；`Preview (truncated):` 后的切片都停在 5000 字符档。可见内容差 ~400 字符来自 Python 独有的 `Data structure:` 结构块（差异 D5）。

### 2.2 `llm.aux_max_tokens=512`（B1）

同一轮真实回合结束后，代理记录到两侧都发起 title namer 请求：

- Go：`max_tokens=512`、HTTP 200，`meta.json` 落标题 `"Run Python command and count characters"`（`title_source=auto`）。`internal/server/session_titles.go` 本车道改为读 `llm.AuxMaxTokens()`；此前固定 8192 占位。
- Python：`max_tokens=512`、HTTP 200，且 `meta.json` 落标题 `"Counting characters from terminal command output"`（`title_source=auto`）。

`aux_max_tokens` 是 recall/judge/sentinel/skill-test/title 共用的单一旋钮：本次以 title 为消费者证明「配置值 → 出网请求上限」在两侧都成立（Go 另有 `TestConfiguredAuxMaxTokensReachesTheJudge` 覆盖 judge）。

### 2.3 `HIVE_MAX_ITER=1`（T2/T3）

两侧同法重启 serve（`HIVE_MAX_ITER=1`），让 queen「先跑 `echo one`，再跑 `echo two`，然后汇报」：

| 观测 | Go | Python |
| --- | --- | --- |
| `node_loop_started.data.max_iterations` | `1` | `999999`（无视 env） |
| 实际执行的工具 | 只跑了 `echo one`（1 个工具部分） | `echo one` + `echo two`（2 个） |
| 回合结局 | `execution_failed`：`agent loop: exceeded 1 inner turns` | 正常完成，assistant 汇报两个输出 |
| env 是否生效 | 是（正数覆盖默认 50） | 否：`queen_loop_config["max_iterations"]=999_999` 遮蔽了 `lc.get(..., _default_max_iter)` 的兜底，读取点实际不可达 |

结论：Go 的 `HIVE_MAX_ITER` 按文档语义生效；Python 侧是「有读取点、无效果」的参考实现缺陷（上游注释与代码不一致）。登记为差异 D2，不改 Python（参考侧行为改动属上游变更）。

### 2.4 复跑的门禁/单测（无模型依赖）

- `go test ./internal/colony -run 'Concurrency|CapRaise' -v`：`TestPlaybookVerbatimConcurrencyFailurePayloadMatchesUpstream`、`TestPlaybookVerbatimConcurrencyCapRaiseAndRestore`、`TestPlaybookCapRaiseAdmitsDeclaredConcurrency` 全 PASS（C2/C4）。
- `go test ./internal/playbook -run 'Concurrency' -v`：`TestResolveConcurrencyQueenDeclared`、`TestLaneLimitsConcurrency`、`TestRateGateComposesWithConcurrency`、`TestPythonBridgeLaneLimitsConcurrency` 全 PASS（C1/C2）。
- `go test ./internal/tools/builtin -run TestTerminalExecTruncatesOutput` PASS；`cd tools && uv run --offline python -m pytest tests/test_terminal_tools_exec.py -k "truncat or max_output or handle"` → 2 passed（B7）。
- 本车道新增/扩展：`TestExecuteTurnAppliesLoopContextLimits`、`TestParallelReadOnlyBatchCapsInflightAtTen`、`TestTitleBudgetFollowsConfiguredAuxMaxTokens`、截断信封 grep 提示断言（B5/C5/B1/B3）。

## 3. 差异清单

### 已修（本车道）

| # | 差异 | 修法 |
| --- | --- | --- |
| F1 | 会话的窗口与压缩缓冲从不接收 `LoopConfig`（新建会话恒 180k + 0.8 乘法阈值；小窗口 queen 会在 provider 报错前不压缩） | `LoopConfig` 增 `CompactionBufferTokens/Ratio/WarningBufferTokens`（8000/0.4/12000），`ExecuteTurn` 每次把窗口与混合缓冲推给会话（`loop.go`） |
| F2 | title namer 固定 8192，忽略 `llm.aux_max_tokens`（上游 `generate_title` 用 `get_aux_max_tokens`） | `TitleTokenBudget` 改读 `llm.AuxMaxTokens()` |
| F3 | 只读并发批无上限（上游 10） | `parallelReadOnlyCap=10` 信号量 |
| F4 | 截断信封缺上游 grep 恢复提示行 | 补 `For targeted lookup, run …` + 空行 |

### 保留（有意 / 未修，均带理由）

| # | 差异 | 理由 |
| --- | --- | --- |
| D1 | queen 轮次默认 `999_999`（Python，含 colony 相位）vs Go `50` | 已在 `sse-payload-parity.md` §4.2 登记为有意：Python 是「永久 loop」报数，Go 是每回合 loop 上限；Go 的 50 同时是跑飞保护 |
| D2 | `HIVE_MAX_ITER`：Python 读取点被 profile 键遮蔽、不生效；Go 生效 | 本车道实测（§2.3）。Go 保留「生效」语义（等于上游注释意图）；改 Python 属参考侧行为变更，不属本 port 车道 |
| D3 | queen 每回合硬停：Python 150（colony 500）vs Go 固定 50 | `soft_budget_test.go` 明确「审计口径 30 soft / 50 hard」；Go 取更紧的审计值，软检查点 30 两侧一致 |
| D4 | `HIVE_SESSION_COST_LIMIT_USD` 为 Go 独有 | Python 无对应实现；Go 默认关闭，不改默认行为（additive） |
| D5 | JSON 工具结果的预览整形：Python `Data structure:` 结构摘要 + 数组感知预览（`tool-result_handler.py:125-235`）vs Go 纯文本切片 | 已登记在 `tool-output-contract.md` §5 `search_messages_spill`（本车道把该行更新为「grep 提示已两侧同形，剩余为结构摘要/预览整形」）；移植 preview 整形属独立工作 |
| D6 | 小结果落盘：Python 把每个非错误结果都写 spillover 目录（LLM 可见内容不变），Go 只写超限结果 | 磁盘审计痕迹差异，模型可见内容一致；`tool_result_spillover_test.go` 钉的是 Go 现状 |
| D7 | 无 spillover 目录时：Python 原地截断（另一套头部文案），Go 原样返回不截断 | 生产路径（queen chat / worker）都配置了目录；未配置属测试/嵌入式场景 |
| D8 | `llm.aux_max_tokens` 配 `0`/负值：Python 原样下发，Go 回落默认 8192（`SetAuxMaxTokens(<=0)` 清覆盖） | 0 不是有效输出预算；Go 取保守回落 |

## 4. 复现与产物

| 产物 | 路径 |
| --- | --- |
| 实测记录（两 side 全量字段） | `go_hive/docs/e2e-assets/limits/limits-real-e2e.json` |
| 截断信封原文 | `go_hive/docs/e2e-assets/limits/{go,python}-truncated-tool-result.txt` |
| 驱动脚本 | `go_hive/scripts/limits_real_e2e.py`（真实 serve + 记录代理；`--sides go,python` 可合并两侧结果） |
| 逐键契约（config 面） | `go_hive/docs/env-config-contract-diff.md`（B1/B2/O1/O2 的键级对照） |
| 工具输出契约（截断例外行） | `go_hive/docs/tool-output-contract.md` §5 |
