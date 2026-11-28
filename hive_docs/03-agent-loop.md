# 03 · 执行内核：AgentLoop

> 文件：`core/framework/agent_loop/agent_loop.py`（7,501 行）
> 这是全系统唯一的执行原语，也是复杂度最高的文件。

## 1. 它在系统中的位置

`AgentLoop` 实现 `AgentProtocol`，负责一个 Agent 的多回合生命周期：

1. 调用 `LLMProvider.stream()` 流式获取模型输出（文本、推理、工具调用、结束原因）。
2. 执行工具并把结果回填对话。
3. 判断是否继续下一回合：judge 验收、显式结束原因、预算、停顿、用户中断。
4. 发布生命周期事件到 `EventBus`。
5. 通过 `ConversationStore` 写穿持久化。
6. 需要等待时把游标写盘并**停泊**。

四个对象构成它的输入：

| 对象 | 作用 | 关键字段 |
|---|---|---|
| `AgentSpec` | 声明“这个 Agent 是什么” | `id/name/description`、`system_prompt`、`tools`、`tool_access_policy`、`output_keys`、`success_criteria`、`skip_judge`、`report_schema` |
| `AgentContext` | 一次执行的全部上下文 | `llm`、`available_tools`、`goal_context`、`input_data`、`session_id/colony_id`、动态提示/工具 provider、`execution_id` |
| `LoopConfig` | 循环策略 | 回合数、工具预算、上下文、超时、看门狗、后台工具、压缩参数、钩子 |
| `AgentResult` | 输出 | 成功/失败、输出键值、迭代数、工具调用数、报告、错误 |

`AgentSpec` 的 `tool_access_policy` 有三种：`all`（注册表全部）、`explicit`（仅 `tools` 列表，默认）、`none`。

## 2. 回合生命周期

```text
execute(ctx)
  └─ _execute_impl(ctx)
       ├─ 恢复/初始化 conversation、cursor、预算、提醒源绑定
       ├─ 若没有可运行输入 → 停泊等待
       └─ _run_turn_loop(ctx)                  ← 一个“回合”，内部可多次 model↔tool
            ┌─────────────────────────────────────────────┐
            │ 1. 构建本轮消息（提醒、记忆、技能、工具清单） │
            │ 2. stream 模型输出                           │
            │ 3. 文本/推理增量 → 事件                      │
            │ 4. 工具调用 → 分区执行（并行/串行）           │
            │ 5. 工具结果 → 截断/落盘 → 回填               │
            │ 6. judge 验收（如声明成功标准）               │
            │ 7. 检查预算/停顿/压缩/中断                    │
            │ 8. 继续 or 结束回合                           │
            └─────────────────────────────────────────────┘
       └─ _finalize_result → AgentResult
```

关键入口（源码符号）：

- `AgentLoop.execute` / `_execute_impl`
- `AgentLoop._run_turn_loop`
- `AgentLoop._await_user_input`
- `AgentLoop._finalize_result`
- `agent_loop/internals/` 下的压缩、游标、判定、工具结果、停顿检测

### 2.1 输入注入不是“塞消息”

外部消息通过 `inject_event` 进入队列；循环在**迭代边界**调用 `_drain_injection_queue` 把它写入对话。这样：

- 不会与正在进行的流式写入竞争；
- 一条在工具执行期间到达的用户消息，会在下一次模型调用前进入上下文（v1.0.15 修复点）；
- 触发器走 `inject_trigger`，同样排队；
- Channel 的 `send` 最终也汇入这一条路径。

`inject_event` 会清除 `user_stopped` 并唤醒循环；`is_client_input` 额外决定：是否重置 idle nudge 计数、通知 Sentinel 本地恢复、记录 pending user text、触发 `CLIENT_INPUT_COMMITTED`。注意：清除停止标记对所有 `wake=True` 的注入生效，不只是真实用户消息——Worker 报告、通道消息等非客户端注入也走这条路径。这是实现与 `mark_user_stopped()` docstring 措辞不完全一致的地方，本报告按源码行为描述。

### 2.2 停止与“不要自动复活我”

用户点 Stop 时：

- 当前回合被取消（`cancel_current_turn`）；
- `mark_user_stopped()` 置位；
- 游标写 `user_stopped=True`；
- 重启后 `restore` 读回该标记，**不会**自动恢复。

只有一次新的 `wake=True` 注入才会重新开始；聊天页重入本身不再解除停止，用户必须发消息。这是 Hive 对“队列/定时器把已停止的 Agent 又唤醒”这类事故的显式防护。

## 3. 流式与看门狗

### 3.1 流事件

`llm/stream_events.py` 定义统一事件：

- `TextDeltaEvent` / `TextEndEvent`
- `ReasoningStartEvent` / `ReasoningDeltaEvent`（兼容 Qwen/DeepSeek/GLM 的 `reasoning_content`）
- `ToolCallEvent`
- `ToolResultEvent`
- `FinishEvent`（含 usage、cost、finish_reason）
- `StreamErrorEvent`

`AgentLoop` 把文本增量转成 `CLIENT_OUTPUT_DELTA`，推理转成 `CLIENT_REASONING`；前端因此可以分别渲染“说话”和“思考”。

### 3.2 两级流预算

| 预算 | 默认 | 语义 |
|---|---:|---|
| `llm_stream_ttft_timeout_seconds` | 600s | 从发起请求到第一个事件；大上下文本地模型可能合法地慢 |
| `llm_stream_inter_event_idle_seconds` | 120s | 已开始产出后，两个事件之间的静默 |

两者任一触发都会取消流。`llm_stream_inactivity_timeout_seconds` 是历史别名，非默认值时会覆盖 inter-event。

### 3.3 重试与降级

- **有界重试**：`max_stream_retries=5`，指数退避，单次最大 60s。
- **容量错误持续重试**：429/529/overloaded 等按墙钟预算重试，`capacity_retry_max_seconds=600`。
- **上下文过大**：识别 provider 的 context-window 错误，先压缩再重试。
- **继续提示（continue nudge）**：流被看门狗判定“活着但卡住”时，不直接抛错重跑整回合，而是取消流、保留已产出的部分文本/工具调用，追加一条短续写提示；每回合最多 3 次（`continue_nudge_max_per_turn`）。

### 3.4 会话级 idle 看门狗

`session_idle_nudge_seconds=120`：会话活着但长时间没有任何流事件、工具完成或迭代边界时触发。它区分三种停泊：

- 普通（`between_turns` / 慢 TTFT）：120s；
- 无问题的停泊（`TURN_DONE` / `UNKNOWN`）：120s；
- 失败导致的停泊（LLM 错误、doom loop、空回合）：30s，更快恢复。

每会话最多 3 次（`session_idle_nudge_max_per_session`）。这类提醒由 `IdleNudgeSource` 产生，且会在 Sentinel autopilot 开启时让位，避免双重 nudge。

## 4. 工具执行

### 4.1 调度路径

- `_execute_tool`：调度入口，解析超时、判断后台化、包装错误。
- `_execute_tool_inner`：真实调用。
- `_start_background_tool` / `_collect_background_result`：长任务后台化。
- `_wait_for_job` / `_wait_budget`：等待后台任务，不占用模型回合。

### 4.2 并发分区

一个回合内的工具调用按**是否只读安全**分为两批：

- `concurrency_safe=True` 的工具并行执行，信号量上限 **10**；
- 其余（shell、写文件、发消息、浏览器、Tracker 写）按模型给出的顺序串行。

当前 `ToolRegistry.CONCURRENCY_SAFE_TOOLS` 包含：

```text
pdf_read, terminal_rg, terminal_glob, terminal_output_get,
web_scrape, search_papers, search_wikipedia, download_paper
```

> 注意：`terminal_exec` **不**并发安全，因为浏览器 CLI 也走它，且它可能修改进程/文件状态。

### 4.3 超时

| 项 | 默认 |
|---|---:|
| 单工具调用 | 60s |
| `browser_` 前缀覆盖 | 180s |
| MCP 客户端结果上限 | 240s（不变量：所有覆盖值必须低于它） |
| 后台工具超时 | 235s |
| 后台化宽限 | 5s（5s 内完成就原调用返回，不产生 handle） |
| 等待预算 | 600s（`tool_wait_limit_seconds`） |

`background_tools` 默认是 `image_generate` 与 `terminal_exec`：调用先最多等 5s，超时则返回 `collect_result` handle；Agent 可以在后续回合收集，不阻塞回合。

### 4.4 结果截断与落盘

`max_tool_result_chars=30_000`。超过时：

1. 完整结果写入 `spillover_dir/<tool>_<n>.txt`；
2. 上下文只保留预览、绝对路径与“用终端搜索它”的提示；
3. 文件名编号会从已有文件恢复，重启后不冲突。

结构化结果还会生成 JSON 预览与元数据提取（`_build_json_preview` / `_extract_json_metadata`），避免模型只看到无意义的巨大 JSON。

### 4.5 图片

- 支持视觉的模型：截图/附件直接进入工具结果或下一轮用户消息。
- 纯文本模型：由视觉回退模型生成 caption（`internals/vision_fallback.py`）。
- 历史图片只保留最近 **2** 张（`max_retained_screenshots`）；更早的 `image_content` 被剥离，但保留 URL/尺寸等文字元数据，避免 Gemini 等把 base64 当文本计数导致上下文爆掉。

### 4.6 重放与死循环

| 检测 | 默认 | 行为 |
|---|---|---|
| 工具重放检测 | 最近 3 回合，同 name+canonical args | 仍执行，但在结果前加一句“你重复了”提示 |
| 工具死循环 | 连续 3 回合完全相同指纹 | 触发 tool breaker，停止并把控制权交回 |
| 文本停顿 | 连续 3 次回复 n-gram 相似度 ≥ 0.85 | 判定 stalled |
| Worker 纯文本回合 | 连续 2 次无工具/无输出/无 ask_user | 合成 `report_to_parent(failed)` 并结束（并行 Worker） |
| 静默工具流 | 连续 5 个只有工具、没有用户可见文本的回合 | 注入“说点什么”的 nudge |

`replay_exempt_tools` 明确豁免 `ask_user`、`escalate`、`collect_result`、`terminal_job_*`、`search_tools`、`search_messages`、`get_*` 等“本来就该重复”的工具。

## 5. 预算与宽限收尾

Hive 的预算不是一个数字，而是多层：

```text
回合数 max_iterations
  ├─ 每回合工具软预算 tool_call_budget（跨过倍数 → 检查点提醒）
  ├─ 每回合工具硬停 tool_call_budget × tool_call_hard_multiple（推迟剩余调用）
  ├─ 生命周期工具预算 tool_call_lifetime_budget（跨回合累计）
  ├─ 上下文预算 max_context_tokens（压缩触发）
  └─ 时间/流预算（看门狗、工具超时、等待预算）
```

### 5.1 软检查点

每跨过一个 `tool_call_budget` 倍数，注入一次递增语气的提醒：

- 不是错误，不停止；
- 提醒 Agent 重新评估：真在长任务就继续，重复/失效就换方法或升级。

### 5.2 硬停不是报错

达到硬停后，剩余工具调用**不执行**，但返回给模型的是**非错误**的中性结果：

> “本次调用未执行——已达到工具调用预算。这不是错误，也不是调用失败。你已用完本回合工作预算，下一回合将进入收尾。”

这样模型不会把“预算”误判为工具故障而反复重试。

### 5.3 Grace（宽限回合）

`grace_iterations`：

- Worker 默认 **1**：最后一次收尾回合，只允许 `report_to_parent`、`tracker_upsert`、`task_update`；
- Queen 默认 **0**：她不需要 `report_to_parent`，回合结束即停泊。

宽限回合的意义：Worker 耗尽预算时仍然能“汇报”，而不是静默消失。触发宽限的两条路：

1. 迭代数达到 `max_iterations`；
2. 生命周期工具调用数达到 `tool_call_lifetime_budget`（可以更早进入宽限）。

### 5.4 预算被“群体”调整

Colony 有一个**自适应 Worker 预算**（`colony_runtime.py:160`）：

- 成功 Worker 的实际工具消耗构成“群体常态”；
- 统计估计（中位数 × 2，带 p90/离散度保护）会**收缩**新 Worker 的生命周期预算，让大概率失败的 Worker 更早收尾；
- 只允许在“未截断的成功样本”上**上调**（max-guard 1.25×），防止死亡螺旋；
- 下限 30，至少 3 个成功样本，窗口 50；
- 可通过 `HIVE_ADAPTIVE_TOOL_BUDGET=0` 或 Colony 元数据关闭。

## 6. 上下文管理

### 6.1 压缩管线（从便宜到贵）

`agent_loop/internals/compaction.py`：

```text
0. Microcompaction      按数量清理最旧的可重取工具结果（保留最近 6 个）
1. Prune                按 token 预算清理更旧结果
2. Structure-preserving 把结果替换为落盘引用
3. LLM summary          把最老回合总结成一条消息（可递归切分）
4. Emergency summary    不依赖 LLM 的确定性兜底
```

触发不是固定 80%，而是**混合缓冲**：

```text
触发点 = max_context_tokens
        - (compaction_buffer_tokens + compaction_buffer_ratio × max_context_tokens)
```

默认 `8_000 + 0.4 × window`：

| 窗口 | 触发点 | 占比 |
|---:|---:|---:|
| 32k | ≈ 20.8k | 65% |
| 180k | ≈ 100k | 56% |
| 1M | ≈ 408k | 59% |

另外还有：

- **发送前保护**：若即将发送时已接近预算，先压缩再发；
- **压缩警告**：提前一个缓冲量发出 `CONTEXT_USAGE_UPDATED` 警告；
- **失败熔断**：连续 3 次压缩失败后停止自动压缩，避免死循环；
- **超长用户消息**：超过 100k 字符的消息截断头尾并插入说明，防止历史脏数据卡死压缩；
- **群聊保护**：LLM 摘要会逐字保留 `is_client_input` 消息，但群聊每条都是 client input，因此最多保留 40 条，其余进摘要。

### 6.2 压缩后的“再宣告”

压缩会丢掉模型看不见的东西：延迟加载的工具清单、技能目录、当前计划。`POST_COMPACT` 提醒点让这些源重新注入，避免压缩后 Agent “忘了自己有什么工具”。

### 6.3 微压缩的可恢复性不变量

只有“有可引用落盘路径”的工具结果才会被清理；否则模型无法重新取得该信息。当前可压缩工具集合包括 `pdf_read`、`terminal_*`、`web_scrape`、论文/维基检索等。

### 6.4 输出溢写

`set_output` 的单个值超过 `max_output_value_chars=2000` 时，自动写到 `output_<key>.json/txt`，游标里保存引用字符串。这样大结构化输出不会撑爆上下文。

## 7. Judge：回合验收

`internals/judge_pipeline.py` 分三级：

```text
Level 0 短路
  - mark_complete_flag → ACCEPT
  - skip_judge        → RETRY（feedback=None，不记录）
  - 有真实工具调用     → RETRY（继续工作）

Level 1 自定义 Judge（JudgeProtocol）
  - 传入对话摘要、工具调用、输出、缺失键
  - 返回 ACCEPT / RETRY / ESCALATE

Level 2 隐式 Judge
  - 缺 output_keys → RETRY + 反馈
  - 全 nullable 且一个都没设 → RETRY
  - 有 success_criteria → LLM 质量检查
  - 否则 ACCEPT
```

`RETRY` 的反馈会以 `[Judge feedback]` 形式进入下一轮上下文，形成**reflexion**（试一次、评估、带批评再试）。

角色差异：

- **Queen**：`skip_judge=True`，用户就是她的 judge；她不会因为“文本回合”被判定完成而退出。
- **Worker**：通常没有 `output_keys` 与 `success_criteria`，它的终点是 `report_to_parent`，不是 judge。
- **独立 SDK Agent**：可用 `output_keys` / `success_criteria` / 自定义 Judge 获得完整验收。

## 8. 停泊、恢复与冷启动

### 8.1 三种顶层状态

`LoopActivity`：

- `EXECUTING`：流、工具、judge、压缩中；
- `AWAITING_USER`：Agent 主动结束回合或等待外部方；
- `INTERRUPTED`：异常停泊、流停顿、崩溃/陈旧。

### 8.2 11 种停泊原因

`ParkReason`：

| 类别 | 原因 | 含义 |
|---|---|---|
| 健康 | `ASK_USER` | 调用了 `ask_user` |
| 健康 | `CREDENTIAL_FORM` | 等待安全凭据表单 |
| 健康 | `COLONY_SUGGESTION` | 等待用户确认建群 |
| 健康 | `TURN_DONE` | 干净结束回合 |
| 健康 | `AWAITING_QUEEN` | Worker 升级后等待 Queen |
| 健康 | `USER_STOPPED` | 用户点 Stop |
| 健康 | `COLD_INTERRUPTED` | 运行中进程死亡，重载后被停泊 |
| 故障 | `LLM_ERROR` | LLM 调用失败 |
| 故障 | `EMPTY_RESPONSES` | 连续空回合 |
| 故障 | `DOOM_LOOP` | 工具死循环 |
| 未知 | `UNKNOWN` | 未记录原因（视为 bug） |

视图属性：

- `is_external_wait`：等待另一方输入（问题/凭据/建群/Worker 等 Queen），idle nudge 不打扰；
- `is_silent_park`：干净停止点（`TURN_DONE` / `USER_STOPPED` / `COLD_INTERRUPTED`），不自动唤醒；
- `is_broken`：故障停泊，更快恢复。

### 8.3 游标里到底有什么

`cursor.json`（`internals/cursor_persistence.py`）：

```json
{
  "iteration": 12,
  "outputs": {"key": "value 或落盘引用"},
  "recent_responses": ["..."],
  "recent_tool_fingerprints": [["tool", "args-json"]],
  "pending_input": {"...": "等待中的问题/表单/建群建议"},
  "user_stopped": false,
  "tool_calls_used": 87,
  "next_seq": 143
}
```

恢复时：

1. 读 parts 重建对话；
2. 读 cursor 恢复迭代号、输出、停顿窗口、待输入、停止标记、累计工具调用；
3. 若有 `pending_input`，直接回到对应停泊状态；
4. 若没有可运行输入，进入等待。

### 8.4 崩溃修复

- **悬空工具调用**：对话里 assistant 发了 tool_use 但没有 tool_result，恢复时会补齐“中断”结果，避免 provider 拒绝。
- **冷中断**：Queen 在回合中被杀，重载后标记 `COLD_INTERRUPTED` 并停泊，不会盲目重放。
- **用户停止跨重启**：`user_stopped` 持久化，防止“重启后自动继续”。

## 9. 视觉回退

`llm/capabilities.py` + `internals/vision_fallback.py`：

- 模型目录里 `supports_vision` 为 false 的模型，图片不会直接塞进工具结果；
- 由 `vision_fallback` 槽位的模型生成 caption，作为文本附在结果旁；
- 未知模型默认“支持视觉”，避免误伤新模型；
- 工具定义层面也会过滤“只产出图片”的工具（名称匹配 `screenshot|capture|render_image|...`），避免纯文本模型反复调用必然失败的截图工具。

## 10. 钩子与可观测性

- **生命周期钩子**：`LoopConfig.hooks`，事件如 `before_turn` / `after_turn` 等；钩子可修改系统提示或注入文本。
- **事件**：`NODE_LOOP_STARTED/ITERATION/COMPLETED`、`LLM_TURN_COMPLETE`、`TOOL_CALL_STARTED/COMPLETED`、`JUDGE_VERDICT`、`REMINDER_INJECTED`、`CONTEXT_USAGE_UPDATED`、`STREAM_*`、`LOOP_STATE_CHANGED`。
- **诊断**：每回合计算 token 使用、工具调用、耗时、是否被压缩、是否触发预算，通过 `llm_turn_complete` 事件暴露给 UI。

## 11. 这一层的设计评价

**优点**

- 把可靠性（恢复、压缩、预算、停顿、视觉、判定）收敛到一个实现，所有角色共享。
- 软检查点 + 硬停中性结果 + grace 收尾，是比“超时直接杀”更细的控制。
- 停泊原因建模细，idle/ Sentinel/恢复都能基于原因做不同决策。

**代价与风险**

- 7,501 行的单文件，`_execute_impl` 与 `_run_turn_loop` 各自约 2,000 行；分支密度高，修改需要极强测试覆盖。
- 大量行为由提示词驱动（宽限提示、预算提示、Worker 规则、Queen 教条），模型换代可能改变行为。
- 工具超时、MCP 客户端超时、后台工具超时、等待预算四套时间轴并存，配置漂移容易踩坑。
- 压缩是有损的；虽然保留落盘引用，但模型可能不再主动读取，导致“压缩后失忆”。
