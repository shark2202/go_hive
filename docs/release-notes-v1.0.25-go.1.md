# 发布说明：hive 1.0.25-go.1

> 版本：`hive 1.0.25-go.1`；上游 `aden-hive` / `aden-tools` 1.0.14（`c6c2cdcf`）；构建 2026-11-20，`CGO_ENABLED=0`。

1.0.24 之后的版本：补齐 overseer 桥接的可观测性与共享 park。

## 变化

- **Worker 进度镜像**：`runWorker` 回合发布 client text/tool/token 事件到 colony bus；session bridge 以 `relayed_from=<worker-id>` 重标进 queen 会话流，已带标记的不重复镜像，overseer 自身 tool/token 不重复。
- **Overseer 凭据 park**：overseer 的 `tool_call_completed` 命中缺凭据时写 `pending_input(credential_form)` 并发 `client_credential_form_requested`；既有 `POST /api/sessions/{id}/credential-form` 可消费并恢复回合。
- **共享 pending/park**：overseer 的 `client_input_requested` 经 bridge 写入 chat runtime 的 pending 与会话 `cursor.json` 的 `pending_input`；回答经 `Deliver → AnswerOverseer` 双清；cursor 读改写加锁避免并发丢写。
- **overseer 的 ask_user 到客户端**：`overseerToolDefs` 新增 `ask_user`，发 `client_input_requested(stream=overseer)`；回合结束补 `loop_state_changed(awaiting_user)`，快照报 `awaiting_input` + `pending_questions`。
- **overseer 终态报告不再触发 chat 回合**：改为 `recordOnly+external` 写入转录。
- 修 timeline 后台写者竞态：纳入 `WaitGroup`，`Close` 等待。

## 质量

| 项 | 结果 |
| --- | --- |
| 测试 | **4,561**（4561/4561 注解） |
| parity 引用 | **4,840**（3,771 test + 1,069 prod），0 missing |
| 门禁 / QC / 冒烟 | 7/7、8/8、13/13 |

## 已知差异

- Queen 与 overseer 仍是两份 conversation：共享的只有 pending question/park 与 cursor；overseer 回复不写回 chat transcript。
- 纯进程重启后前端 modal 是否重现取决于前端对 events.jsonl 的回放（前端冻结未验证）。
- colony worker 无 `ask_user` 合成工具；escalation 走既有路径。
- MiniMax 仍不流式；Python playbook 不可执行；81 个第三方凭据模块未验证。
