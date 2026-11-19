# 发布说明：hive 1.0.18-go.1

> 版本：`hive 1.0.18-go.1`；上游 `aden-hive` / `aden-tools` 1.0.14（`c6c2cdcf`）；构建 2026-11-19，`CGO_ENABLED=0`。

1.0.17 之后的补丁版：补上最后一批低风险可验证缺口。

## 变化

- **`search_messages` / `search_timeline`**：会话工具，按关键词/时间检索同 queen 的历史会话 parts；模型只能给 pattern，会话 id 只接受裸 id（含分隔符/`..` 直接拒绝）；搜索引擎只从 queen 自己的 `sessions/` 枚举。
- **stall / doom-loop 检测**：连续 3 次相同工具调用（名称 + 规范化参数）触发一条提醒，附加在该批工具结果尾部；每 streak 只提醒一次、每 turn 重置，不硬中断、不 park。
- **worker task list**：`task_create/update/list/get` 进入 worker 工具面，按 worker 目录隔离，两个 worker 互不可见；overseer 不接。
- **`run_playbook` status/stop**：新增 `action`（run/status/stop）与 `run_id`；status 返回运行中 run 与近 20 条历史，stop 按 run 取消并记 `status=stopped`（非失败）。

## 质量

| 项 | 结果 |
| --- | --- |
| 测试 | **4,422**（4422/4422 注解） |
| parity 引用 | **4,657**（3,682 test + 975 prod），0 missing |
| 门禁 / QC / 冒烟 | 7/7、8/8、13/13 |

## 已知差异

- stall 检测为「无模型」版本：上游 `SubscribeStallCheck` 的 judge 版需 server judge 槽，未做。
- `status.running` 只能看到同会话另一 turn 尚未返回的 run（runner 同步执行）。
- Python playbook 仍不可执行；Anthropic/MiniMax 不流式；第三方集成 81 模块未验证；无在线漏洞库比对。
- 仍缺：并行只读工具、预算软检查点/花费上限、park 落盘 resume、memory timeline、agent credentials、rate limit 执行、soul 进 persona。
