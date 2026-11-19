# 发布说明：hive 1.0.23-go.1

> 版本：`hive 1.0.23-go.1`；上游 `aden-hive` / `aden-tools` 1.0.14（`c6c2cdcf`）；构建 2026-11-19，`CGO_ENABLED=0`。

1.0.22 之后的版本：补齐审计里提醒、持久性、loop 保护三组“部分”。

## 变化

- **提醒供给**：tracker 快照 source（按上游节奏冷却）、真实 skills_catalog、chat searchable tools 分层、REST reminder 与 live chat hub 同源。
- **持久性**：colony scaffold 同时建 `skills/`、`runs/`、`triggers.json`（幂等）；worker 运行态落盘（queued/running/终态），启动自动续跑可恢复记录；`serve --colony` 预载会话按 `queen_resume_from` 复用旧 id；缺凭据时产生 `credential_form` park 与 `client_credential_form_requested` 事件。
- **loop 保护**：stall 检测在 `AddSession` 订阅并发布 `node_stalled`；doom-loop 达阈值真正停车（`ParkReasonDoomLoop` + `node_tool_doom_loop`）；Queen 30 软 / 50 硬检查点；worker 大结果落 `cache/worker-tool-results/`。

## 质量

| 项 | 结果 |
| --- | --- |
| 测试 | **4,527**（4527/4527 注解） |
| parity 引用 | **4,765**（3,719 test + 1,046 prod），0 missing |
| 门禁 / QC / 冒烟 | 7/7、8/8、13/13 |

## 已知差异

- worker 自动续跑只覆盖崩溃时 queued/running，优雅停止的 `stopped` 不自动复活。
- `POST /api/sessions` 带 `colony_id` 的普通创建路径仍未走 `queen_resume_from`（仅预载会话）。
- 无 Go 侧 `credential-form` 提交路由；credential park 目前只有生产者与事件。
- `attachPreloadedChatRuntime` 未注入 `SkillsCatalog`（预载路径）。
- chat 失败分支会把 doom-loop 的 park reason 覆盖为 `unknown`（可观测信号仍在事件里）。
- MiniMax 仍不流式；Python playbook 不可执行；81 个第三方凭据模块未验证。
