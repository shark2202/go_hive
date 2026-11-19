# 发布说明：hive 1.0.22-go.1

> 版本：`hive 1.0.22-go.1`；上游 `aden-hive` / `aden-tools` 1.0.14（`c6c2cdcf`）；构建 2026-11-19，`CGO_ENABLED=0`。

1.0.21 之后的版本：补上 goals_outcome 三条完全未接项，以及四项高影响“部分”。

## 变化

- **加权成功标准真实执行**：worker 完成时按 `Metric` 执行 `output_contains`/`output_equals`/`llm_judge`，`custom`/未知返回 unsupported；结果进 `WorkerResult.GoalEvaluation`、`result.json`、worker 报告与 `goal_progress` 事件。
- **约束/Context 进 prompt**：`Goal.ToPromptContext()` 追加进 worker 系统提示，`[MUST]`/`[SHOULD]` 与 context 模型可见；测试断言实际 `System` 字段。
- **未达标 → 改进信号**：judge RETRY/ESCALATE 与未达标 criterion 写入 `<colony>/improvement/signals.jsonl`，发 `improvement_signal` 事件；空 tracker 表产生 `empty_tracker` 检测。
- **每轮 past-conversation excerpts**：BM25 + 中文切分，最多 6 条带日期摘录，以 `<system-reminder>` 注入 chat turn。
- **memory timeline 写入侧**：启动回填 + 每轮后 `UpdateSessionTimeline`，timeline 不再只读。
- **工具结果 spillover**：超过 30k 字符的结果落盘，消息带 `Full result at:` 路径与 `spillover_path`。
- **worker `search_tools` 与工具分层**：分层 agent 自动暴露 `search_tools`，未加载工具在 dispatch 前被拒绝；MCP gateable 集合非空时 eager 集合收紧。

## 质量

| 项 | 结果 |
| --- | --- |
| 测试 | **4,495**（4495/4495 注解） |
| parity 引用 | **4,713**（3,705 test + 1,008 prod），0 missing |
| 门禁 / QC / 冒烟 | 7/7、8/8、13/13 |

## 已知差异

- 改进信号是可消费记录，尚无自动再派工消费者。
- worker 的 spillover 目录未配置，生产路径目前是 chat Queen。
- `SubscribeTimelineTriggers` 本身仍无调用，改用 turn-completion + 启动回填等价实现。
- MiniMax 仍不流式；Python playbook 不可执行；81 个第三方凭据模块未验证。
