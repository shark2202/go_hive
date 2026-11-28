# 发布说明：hive 1.0.26-go.1

> **后续变更（2026-11-25）**：默认的 colony-bound session 已回退为 **chat queen 执行**，overseer 保持 dormant；没有生产环境变量或启动开关会启用本文描述的 live overseer 拓扑。因此“Queen 与 overseer 两份 conversation 存储”只在该 dormant 实现被显式测试启动时适用。

> 版本：`hive 1.0.26-go.1`；上游 `aden-hive` / `aden-tools` 1.0.14（`c6c2cdcf`）；构建 2026-11-20，`CGO_ENABLED=0`。

1.0.25 之后的版本：overseer 回复写入会话转录，附 Orca 浏览器 UI E2E 结果。

## 变化

- **overseer 回复写回会话转录**：桥接按 `execution_id` 收束 `client_output_delta`，仅在 `execution_completed` 落 1 条 assistant 消息（`recordOnly + external`，带 `[overseer]` 标记）；重复完成不重复，失败丢弃未完成文本，工具 preamble 不冒充回复。
- **events.jsonl flush 边界**：`execution_completed/failed` 时冲刷缓冲的 delta 快照，回放刷新可看到回复。
- **归档含 hive-cred**：六平台归档与 SHA256SUMS 现在同时包含 `hive` 与 `hive-cred`；`install.sh` 一并安装，归档缺失时明确报错，worker 脚本可直接调用 `hive-cred token <id> --account <alias>`。
- **归档携带浏览器宿主**：六平台归档现在同时包含 `hive-browser` 与 `browserbridge`（Windows 为 `.exe`），包内 `SHA256SUMS` 覆盖四个二进制。`install.sh` 把它们装进同一目录；解压后直接 `./hive serve`，supervisor 会按「`hive` 同目录」解析并自动拉起 `browserbridge`（14830 `/health` 200），SIGTERM 时桥一并回收。QC C4c 断言每个归档含四个成员、内层 SHA256SUMS 全覆盖，并在本机归档上实跑四个二进制。
- **Orca 浏览器 UI E2E**：7 个用例全部 PASS（首页/版本、中英切换与持久化、凭据页、ask_user 冷加载重现、ask_user 回答闭环、新建会话真实对话、控制台 0 error），记录见 `docs/e2e-orca-ui-1.0.25.md`。

## 质量

| 项 | 结果 |
| --- | --- |
| 测试 | **4,566**（4566/4566 注解） |
| parity 引用 | **4,843**（3,773 test + 1,070 prod），0 missing |
| 门禁 / QC / 冒烟 | 7/7、8/8、13/13 |
| Orca UI E2E | **7/7 PASS**（Orca 1.4.261，真实 LLM） |

## 已知差异

- Queen 与 overseer 仍是两份 conversation 存储；共享 pending/park 与 transcript 可见性已补。
- 首次进入被 onboarding 弹层（Pick a playbook）挡住，需先 Begin；E2E 已记录。
- MiniMax 仍不流式；Python playbook 不可执行；81 个第三方凭据模块未验证。
