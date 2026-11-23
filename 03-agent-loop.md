# 03 · AgentLoop 深入

> **本文描述上游 Hive（Python 参考实现）。**
> go_hive 的对应实现：`internal/agentloop`（循环主干与 internals）、`internal/server`（`chat_runtime*.go` 会话装配）。
> 状态：部分（循环主干已移植；judge J3/J4 等残差）；差异登记见 `docs/alignment-differences.md`。

> 文件：`core/framework/agent_loop/agent_loop.py`（约 7,500 行；2026-11-23 实测 7,501）
> 这是全系统唯一执行原语，也是复杂度最高的文件。

## 1. 职责

`AgentLoop` 实现 `AgentProtocol`，负责一个 Agent 的多回合生命周期：

1. 调用 `LLMProvider.stream()` 流式获取模型输出（文本、推理、工具调用、结束原因）。
2. 执行工具并把结果回填到对话。
3. 决定是否继续下一回合：judge 验收、显式结束原因、预算、停顿、用户中断。
4. 发布生命周期事件到 `EventBus`。
5. 通过 `ConversationStore` 写穿持久化。
6. 在需要等待时把游标写入磁盘并**停泊**。

## 2. 回合（turn）生命周期

```
execute(ctx)
  └─ _execute_impl(ctx)
       ├─ 恢复/初始化 conversation、cursor、预算
       ├─ _await_user_input?  → 若无可运行输入则 park
       └─ _run_turn_loop(ctx)
            ┌─────────────────────────────────────────────┐
            │ 1. 构建本轮消息（提醒、记忆、技能、工具清单） │
            │ 2. stream 模型输出                           │
            │ 3. 文本增量 → SSE 推送                       │
            │ 4. 工具调用 → _execute_tool（并发/串行）      │
            │ 5. 工具结果 → 截断/落盘 → 回填               │
            │ 6. judge 验收（如声明成功标准）               │
            │ 7. 检查预算/停顿/压缩/中断                    │
            │ 8. 继续 or 结束回合                           │
            └─────────────────────────────────────────────┘
       └─ _finalize_result → AgentResult
```

关键入口：

- `execute()`：`agent_loop.py:1299`
- `_execute_impl()`：`agent_loop.py:1365`（主体，约 2,100 行）
- `_run_turn_loop()`：`agent_loop.py:3934`（单回合，约 2,100 行）
- `_await_user_input()`：`agent_loop.py:3736`
- `_finalize_result()`：`agent_loop.py:1271`

> 说明：这两个方法体量极大，是后续重构的重点候选。阅读时建议按「消息构建 → 流处理 → 工具执行 → 决策」分段看，不要顺序通读。

## 3. 工具执行

### 3.1 执行路径

- `_execute_tool()`（`agent_loop.py:6534`）是调度入口。
- `_execute_tool_inner()`（`:6617`）负责超时与真实调用。
- 并发安全工具走 `_start_background_tool()` / `_collect_background_result()`（`:6655`、`:6741`）。
- 长任务通过 `_wait_for_job()` 与 `_wait_budget()` 控制等待预算，避免一次工具调用吃掉整个回合。

### 3.2 结果处理

`internals/tool_result_handler.py` 提供：

- `truncate_tool_result()`：按 `get_max_tool_result_chars()`（默认 30,000）截断。
- `build_json_preview()` / `extract_json_metadata()`：为结构化结果生成预览。
- `is_transient_error()`：识别可重试的瞬时错误。
- `restore_spill_counter()`：恢复落盘文件编号，保证重启后文件名不冲突。

超过阈值时，完整结果写成 `<tool>_<n>.txt`，对话里只保留预览、文件路径和「用终端工具搜索它」的提示。这是控制上下文膨胀的核心手段。

### 3.3 工具预算

- 每回合软上限：Queen 30（开 Colony 后 50），Worker 30。
- 软上限触发**检查点提醒**（`_render_tool_budget_checkpoint`），不是停止。
- 硬上限：Worker 每回合 90、生命周期 200；Queen 生命周期预算可被 `apply_lifetime_budget_cap()` 调整。
- 检查点出现时提醒 Agent「如果确实在做长任务就继续；如果在重复/失效就换方法或升级」。

### 3.4 工具超时

`_resolve_tool_timeout()` 按工具名解析不同超时；长任务（后台 job、浏览器操作）有专门预算。超时后可被标记为 transient 并重试。

## 4. Judge 验收

文件：`internals/judge_pipeline.py`

- 当 Agent 声明了成功标准（`success_criteria`）时，每回合结束由 judge 模型验收。
- 返回 `RETRY` 时，judge 的反馈以 `[Judge feedback]` 消息注入，Agent 在下一回合看到并修正。
- **Queen 跳过 judge**：用户就是她的 judge。
- **Worker 不靠 judge 结束**：Worker 通过 `report_to_parent` 结束。

SDK 用户可以实现 `JudgeProtocol` 自定义验收逻辑。

## 5. 上下文管理

### 5.1 压缩（compaction）

文件：`internals/compaction.py`

两阶段策略：

1. **清理旧工具结果**：把大结果替换为指向落盘文件的引用。
2. **LLM 摘要最旧回合**：只压最旧的一部分，最近回合原样保留；必要时把两半并行摘要（1.0.14 的修复）。

触发阈值由 `get_max_context_tokens()` 与 `_compact_char_limit()` 决定。压缩前后都会注入提醒，避免 Agent「失忆」而不自知。

### 5.2 提醒注入（Reminder Hub）

文件：`agent_loop/reminders.py` 及同目录的 `*_reminder.py`：

| 提醒源 | 作用 |
| --- | --- |
| `turn_context_reminder.py` | 每回合的外部上下文 |
| `tracker_snapshot_reminder.py` | 当前 Tracker 状态 |
| `active_workers_reminder.py` | 正在跑的 Worker，避免重复派活 |
| `colony_worker_snapshot_reminder.py` | Colony Worker 舰队快照 |
| `colony_parallel_nudge.py` | 提醒可以并行化 |
| `past_conversations_reminder.py` | 相关历史对话 |
| `tool_skill_reminders.py` | 可用工具与技能 |
| `idle_nudge.py` / `stream_stall.py` | 空闲与流停顿 |

注入点是固定枚举 `ReminderPoint`：会话开始、每条用户消息、工具调用后、预算检查点、压缩前后、停泊空闲 tick、流停顿。

### 5.3 记忆召回

在每回合开始前，`agents/queen/recall_selector.py` 选择相关记忆与历史对话片段注入。详见 [07-memory-and-skills.md](07-memory-and-skills.md)。

## 6. 停泊与恢复

这是「崩溃后精确续跑」的核心。

- `_write_cursor()`（`agent_loop.py:7039`）把迭代计数、累加输出、停顿状态、待处理输入写入 `cursor.json`。
- `_restore()`（`:7023`）在启动时恢复。
- `_check_pause()`（`:7114`）检查是否需要停泊。
- `_drain_injection_queue()` / `_drain_trigger_queue()` 处理停泊期间到达的用户消息与触发器。
- `ParkReason` 枚举区分等待原因：问题、凭证、建群确认、停止、错误等。

停泊不是「休眠进程」：会话对象仍在内存，SSE 连接可断开重连；重启后由 `SessionManager` 从磁盘重建。

## 7. 停顿与死循环检测

文件：`internals/stall_detector.py`

| 检测 | 方法 |
| --- | --- |
| 流停顿 | 看门狗监控流是否持续产出 token |
| 文本重复 | n-gram 相似度（`_ngram_similarity`，默认 n=2） |
| 工具死循环 | 工具调用指纹（`fingerprint_tool_calls`）连续重复 |
| 工具断路器 | `_trip_tool_breaker()` 在重复工具模式时打断 |

还有可选的 **stall check**（`server/stall_check.py`，Settings → Developer，默认关闭）：judge 模型读取「以等待用户结束」的回合，如果 Queen 声称要做某事却没做，就给她一次提醒。1.0.14 扩展为也能捕捉「同意了修改但没执行」。

## 8. 视觉回退

文件：`internals/vision_fallback.py`

- 支持视觉的模型直接看截图/附件。
- API 无法在工具结果里带图时，图片被移到下一条用户消息。
- 纯文本模型由配置的视觉模型生成 caption，并做 crop 重映射（`remap_caption_for_crop`）。

## 9. 子 Agent 合成工具

`internals/synthetic_tools.py` 为每个 Agent 动态合成：

| 工具 | 作用 |
| --- | --- |
| `ask_user` | 提问并停泊等待 |
| `escalate` | Worker 向 Queen 升级 |
| `report_to_parent` | Worker 结束并回报（success/partial/failed） |
| `collect_result` | 收集结构化结果 |

`report_to_parent` 的宽限回合设计很重要：Worker 即使耗尽预算，也仍能体面地汇报，而不是「消失」。

## 10. 事件发布

`internals/event_publishing.py` 把内部状态转成 `AgentEvent`：

- 循环开始/结束、迭代、文本增量、工具开始/结束、上下文用量、judge 判决、停顿、输出键设置。
- 前端通过 SSE 消费 `CLIENT_*` 事件；Queen 通过 `SUBAGENT_REPORT` 消费 Worker 报告。

事件发布是「运行时观测」与「UI 实时性」的共同基础。

## 11. 配置面（LoopConfig）

`agent_loop/internals/types.py` 的 `LoopConfig` 控制：

- `max_iterations` / `grace_iterations`
- `tool_call_budget` / `tool_call_lifetime_budget` / `tool_call_hard_multiple`
- `max_context_tokens`
- judge 节奏、停顿阈值、压缩缓冲
- Worker 允许的覆盖字段被白名单限制在 `_ALLOWED_WORKER_LOOP_OVERRIDES`（`host/colony_runtime.py`）：`max_iterations`、`grace_iterations`、`tool_call_budget`、`tool_call_lifetime_budget`、`max_context_tokens`，并带边界校验。

## 12. 评价

**强项**

- 把可靠性能力集中在一个原语，Queen/Worker/SDK Agent 自动继承。
- 停泊-恢复、结果落盘、压缩、停顿检测的组合在开源 Agent 项目里属于较完整的实现。
- 事件模型清晰，UI 与运行时可观测性共用一套总线。

**风险**

- 单文件约 7,500 行（2026-11-23 实测 7,501）、两个方法各约 2,000 行，认知负担和回归风险都很高。
- 大量行为由隐式约定（提醒文案、预算常量、白名单字段）决定，缺少显式的状态机文档。
- 异步并发 + 文件写穿 + 事件总线三者交织，调试需要较强的心智模型。

**建议**

- 优先把 `_execute_impl` 与 `_run_turn_loop` 拆成显式的阶段函数（构建 → 流 → 工具 → 决策 → 持久化），并为每个阶段补单元测试。
- 把预算、压缩、停顿的阈值集中到一个可审计的配置对象，而不是散落在常量与函数里。
- 为 `ParkReason` 与 `ReminderPoint` 画一张状态迁移图，作为维护者的入口文档。
