# 发布说明：hive 1.0.21-go.1

> 版本：`hive 1.0.21-go.1`；上游 `aden-hive` / `aden-tools` 1.0.14（`c6c2cdcf`）；构建 2026-11-19，`CGO_ENABLED=0`。

1.0.20 之后的补丁版：补齐 DELIVERY 里最后三项行为缺口。

## 变化

- **Anthropic 流式**：Messages SSE 解析，`content_block_delta/text_delta` 实时转 `TextDeltaEvent`；`tool_use` 由 `input_json_delta` 聚合；支持 usage/cache token、429 与空流重试；代理忽略 `stream:true` 时整段单 delta 回退。`AnthropicProvider` 不再返回 `ErrStreamingUnsupported`。
- **park 落盘 resume**：`ask_user`/`turn_done` 的 park 状态即时写 `conversations/cursor.json` 的 `pending_input`；启动恢复未答问题与 park reason，回答后清盘，重启不复活已答问题。
- **soul 进 persona**：queen soul 追加进 `IdentityPrompt`（静态 system prompt），实测发给模型的 `System` 含 `<soul>` 条目。

## 质量

| 项 | 结果 |
| --- | --- |
| 测试 | **4,468**（4468/4468 注解） |
| parity 引用 | **4,681**（3,691 test + 990 prod），0 missing |
| 门禁 / QC / 冒烟 | 7/7、8/8、13/13 |

## 已知差异

- **MiniMax 仍不流式**：其 chunk 会破坏解析，保持 `ErrStreamingUnsupported` 非流式回退，由测试锁定；不伪造。
- Python playbook 不可执行；81 个第三方凭据模块未验证；无在线漏洞库比对。
