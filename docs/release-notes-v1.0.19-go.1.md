# 发布说明：hive 1.0.19-go.1

> 版本：`hive 1.0.19-go.1`；上游 `aden-hive` / `aden-tools` 1.0.14（`c6c2cdcf`）；构建 2026-11-19，`CGO_ENABLED=0`。

1.0.18 之后的补丁版：补上并行只读工具、预算软提醒与只读凭据查询。

## 变化

- **并行只读工具**：loop 消费 `ToolDef.ConcurrencySafe`；同一批调用全部显式标记为安全时并发执行，混合批次保持串行；结果按原索引回填，`tool_call_started/completed` 仍按调用顺序发布。
- **预算软提醒**：每达到 `ToolCallBudget` 倍数、在硬停之前注入一条 `Tool-call checkpoint`（随工具结果尾部），不中断；`ToolCallBudget=0` 关闭。
- **只读 `credential_status`**：会话工具返回已配置凭据的 `name/credential_id/env_var/sources/tool_modules/required`，不含密钥、别名或身份。

## 质量

| 项 | 结果 |
| --- | --- |
| 测试 | **4,429**（4429/4429 注解） |
| parity 引用 | **4,661**（3,683 test + 978 prod），0 missing |
| 门禁 / QC / 冒烟 | 7/7、8/8、13/13 |

## 已知差异

- 并行只读仅对显式标记工具生效；混合批次仍串行（保守策略）。
- 仍缺：预算硬执行的会话花费上限、Anthropic/MiniMax 流式、park 落盘 resume、memory timeline、rate limit 执行、soul 进 persona；Python playbook 不可执行。
