# 发布说明：hive 1.0.20-go.1

> 版本：`hive 1.0.20-go.1`；上游 `aden-hive` / `aden-tools` 1.0.14（`c6c2cdcf`）；构建 2026-11-19，`CGO_ENABLED=0`。

1.0.19 之后的补丁版：把限速、花费上限与记忆时间线接成生产路径；同时修一处测试竞态。

## 变化

- **rate limit 执行**：新增滑动窗口 + 最小间隔限速（`llm.requests_per_minute`、`llm.min_interval_s`、`tools.*`、`calls.max_wait_s`）；超限在预算内等待，超出返回可重试 `CallLimitError`（带 RetryAfter）。接入 LLM（非流式/Anthropic 与流式每次尝试）与每次工具执行；未配置时零行为变化。
- **会话花费硬上限**：`SessionCostLimitUSD`（或 `HIVE_SESSION_COST_LIMIT_USD`）达到后，下一轮在发请求前返回 `SessionCostLimitError`；默认 0=关闭。
- **`memory_timeline` 工具**：按天浏览记忆，返回计数与摘要，`since/until/kind/max_days/per_day` 可调；输出做密钥脱敏与截断。
- 修复 `TestChatRuntimeSedimentsReflectionIntoQueenSoul` 的竞态：等待 `sediment.json` 而不只是 `soul.json`。

## 质量

| 项 | 结果 |
| --- | --- |
| 测试 | **4,460**（4460/4460 注解） |
| parity 引用 | **4,664**（3,685 test + 979 prod），0 missing |
| 门禁 / QC / 冒烟 | 7/7、8/8、13/13 |

## 已知差异

- 花费上限仅环境变量开关，未接 `configuration.json`/UI；无定价模型 `CostUSD=0`，不受硬上限约束。
- 限速配置进程内只读一次，无热更新；`memory_timeline` 未进 worker 工具分档。
- 仍缺：Anthropic/MiniMax 流式、park 落盘 resume、soul 进 persona；Python playbook 不可执行；第三方 81 模块未验证。
