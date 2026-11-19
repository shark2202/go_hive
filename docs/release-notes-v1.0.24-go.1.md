# 发布说明：hive 1.0.24-go.1

> 版本：`hive 1.0.24-go.1`；上游 `aden-hive` / `aden-tools` 1.0.14（`c6c2cdcf`）；构建 2026-11-19，`CGO_ENABLED=0`。

1.0.23 之后的版本：补完最后四项接线，并把 Queen 两阶段与 overseer 桥接接上生产路径。

## 变化

- **credential 提交**：新增 `POST /api/sessions/{id}/credential-form`，消费 `credential_form` park，凭据写入加密 store 并同步环境，清 park 后恢复会话；响应不含密钥。
- **预载会话 SkillsCatalog**：`serve --colony` 预加载会话与标准会话同源注入技能目录。
- **doom-loop park reason**：chat 失败分支保留 `doom_loop`，事件、park 状态与 `pending_input` 一致。
- **judge criteria**：chat Queen 可从 colony goal 继承标准并 `ConfigureJudge`；overseer 无 spec 时继承 goal criteria。
- **Queen 两阶段**：`InitialPhase` 落 `meta.json`，重启可恢复；`GET /api/queen/{id}/tools` 有 phase，PATCH 刷新真正作用于会话工具集。
- **Queen/overseer 桥接**：绑定 colony 且有 live overseer 时，客户端消息、worker report、trigger 只进 overseer；overseer 回复经 session bridge 以 queen stream 呈现；chat Queen 保留为前端。首次中继随消息带 skills 目录。

## 质量

| 项 | 结果 |
| --- | --- |
| 测试 | **4,551**（4551/4551 注解） |
| parity 引用 | **4,798**（3,745 test + 1,053 prod），0 missing |
| 门禁 / QC / 冒烟 | 7/7、8/8、13/13 |

## 已知差异

- 两阶段 phase 在 `meta.json`，但 phase 变更历史未单独审计。
- Queen/overseer 仍有两份 conversation 存储；pending question/park 不共享；chat Queen 自身不再执行 model turn。
- 非 overseer worker 的 client IO 与 tool/token 事件不镜像到会话流。
- MiniMax 仍不流式；Python playbook 不可执行；81 个第三方凭据模块未验证。
