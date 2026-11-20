# 模型槽位热切换 live E2E 证据

`go_hive/scripts/e2e_config_hotswap.py` 在真实 `hive serve` 上跑出来的原始输出：**21/21 PASS**。
脚本用两个本地 OpenAI 兼容假网关（old / new）分别扮演「旧 base」和「新 base」，
每个请求的 `model` 都按网关端口分别记录——断言依据是网关记录，而不是模型自述。

复现（约 40s）：

```bash
CGO_ENABLED=0 go build -o go_hive/go-hive/dist/hive ./go_hive/go-hive/cmd/hive   # 或在 go_hive/go-hive 下 make build
uv run --no-project python go_hive/scripts/e2e_config_hotswap.py
```

## 场景与判定

| 槽位 | 动作 | 判定（本目录证据） |
| --- | --- | --- |
| `queen_llm.qa` | 先在新会话聊一轮（旧 base），再 `PUT /api/queen/qa/llm` | PUT 前请求落在 old 网关（`queen-old`）；PUT 返回 `sessions_switched: 1`；下一回合落在 new 网关（`queen-new`）；同一会话两轮都答出（3 个 assistant part）；GET 回读等于写入的 section |
| `vision_fallback` | 会话创建时该槽位还不存在，之后 `PUT /api/config/llm-sections` | 文本主模型 `llama-3.1-8b-instant` 请求 `attach_file`，工具图片的 caption 请求落在 new 网关（`caption-new`，请求体含 1 个 `image_url` 块） |
| `worker_llm` | `PUT` 后新建 colony 会话并让 queen 派活 | PUT 返回 `sessions_swapped: 0`（运行中会话不动，对齐上游 `_write_role_section`）；新会话的 worker 请求落在 new 网关（`worker-new`），queen/overseer 仍是 `queen-new` |

`run.log` 是带 PASS/FAIL 的完整输出；`gateway-requests.md` 是两个假网关按时间顺序记录到的
全部 11 个请求（wire model、是否带图片块、最后一条 user 文本）；`serve.log` 是 serve 侧收尾日志。

环境：临时 `HIVE_HOME`（`configuration.json` 只写 `llm` 与 `queen_llm.qa`），
假 key 一律 `sk-e2e`，网关不校验 key；`GROQ_API_KEY` 仅为了让主槽 provider 构造通过（假值）。
