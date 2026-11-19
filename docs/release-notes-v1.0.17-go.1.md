# 发布说明：hive 1.0.17-go.1

> 版本：`hive 1.0.17-go.1`；上游 `aden-hive` / `aden-tools` 1.0.14（`c6c2cdcf`）；构建 2026-11-19，`CGO_ENABLED=0`。

1.0.16 之后的补丁版：收口终审列出的五个「半截项」，并修一处测试竞态。

## 变化

- **colony 事件桥 UI**：worker 进入运行时发布 `execution_started`，与 `execution_completed/failed`、`subagent_report` 一起进入会话 EventBus，SSE 可见；同一 runtime 幂等。
- **overseer 输入源**：已绑定 colony 的会话把 worker 报告与 framework trigger 转发给 persistent overseer（跳过它自己的报告避免回声）。
- **MCP 白名单生效**：host 启动解析进程 MCP 工具名与 colony `tools.json`，worker 池与会话目录均过滤；未列出的 MCP 工具不可见且调用被拒。
- **worker reminder hub**：worker loop 注册 `tasks` + `idle_nudge`，启动 ticker 并回注 parked reminder，结束停止。
- **REST reminders 镜像**：`GET /api/sessions/{id}/reminders` 只读镜像 loop hub（pending/active/sources），无 hub 返回空 200；`DELETE` 清队列。
- 修复 `TestWorkerTranscriptPersistedToDisk` 的终端状态竞态。

## 质量

| 项 | 结果 |
| --- | --- |
| 测试 | **4,406**（4406/4406 注解） |
| parity 引用 | **4,647**（3,674 test + 973 prod），0 missing |
| 门禁 / QC / 冒烟 | 7/7、8/8、13/13 |

## 已知差异

- Python playbook 不可执行（Go runner 原生，明确 unsupported）。
- Anthropic/MiniMax 不流式（OpenAI-compatible 逐帧）。
- 第三方集成 81 个模块未验证；无在线漏洞库比对。
- 仍缺：`search_messages`/`search_timeline`、`stall_check`、并行只读工具、预算执行、park 落盘 resume、memory timeline、worker task list、agent credentials、rate limit 执行、soul 进 persona。
