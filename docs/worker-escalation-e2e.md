# Worker escalation 真实端到端（真实 `hive serve` + 脚本化模型）

本文件记录 worker escalation 生产链路（`escalate` 工具 → `ESCALATION_REQUESTED` → 会话级路由 →
`list_worker_questions` → `reply_to_worker` → worker 继续）对**真实二进制**跑通的端到端证据。

不是单测对表：真实 `hive serve` 进程、临时 `HIVE_HOME`、真实 HTTP、真实 colony 目录与 worker
子进程内运行；唯一被脚本化的是模型（`e2e/escalationgw`：确定性本地 OpenAI 兼容网关，按请求里
的工具面与消息内容给出脚本化回合）。

- 日期：2026-11-21
- 被测：`.wt/worker-escalation-channel/go_hive/go-hive`（module `github.com/aden-hive/hive-go`，分支 `worker-escalation-channel`，基线 `46939ab5`）
- 网关：`go_hive/go-hive/e2e/escalationgw`（本 lane 新增，测试基础设施）
- 证据目录：`go_hive/docs/e2e-assets/worker-escalation-e2e/escalation-chain/`

## 结论

用例 `TestWorkerEscalationEndToEnd` PASS。整条链路在真实运行期成立：

| 步骤 | 断言 | 实测（模型请求序号） |
| --- | --- | --- |
| 会话绑定 colony，queen 派工 | 会话 `201`；colony 侧 `run_worker` 派出的 worker 真实启动 | seq 1 overseer 调 `run_worker` |
| worker 工具面含 `escalate` | worker 请求的 `tools` 里真实出现 `escalate`（25 个工具） | seq 3 worker 调 `escalate` |
| 升级事件 → 会话路由 | queen 会话收到 `[WORKER_ESCALATION]` 注入，带 `request_id`/`worker_id`/`reason`/`context` | seq 5 queen 请求携带该文本 |
| `list_worker_questions` 读真实收件箱 | 工具返回 `{"count":1,"pending":[{...}]}`，与事件里的 `request_id` 一致 | seq 5 queen 调 `list_worker_questions`；事件日志 seq 27 为工具结果 |
| `reply_to_worker` 回写 | 工具返回 `{"status":"delivered","worker_id":"worker_1",...}`；worker 收到 `[QUEEN_REPLY]` | seq 6 queen 调 `reply_to_worker`；事件日志 seq 31 为工具结果 |
| worker 继续 | 恢复后的 worker 回合携带 `[QUEEN_REPLY] request_id=…`，并调用 `report_to_parent` 终结 | seq 7 worker 调 `report_to_parent` |
| 落盘可查 | queen 转录含 `[WORKER_ESCALATION]` 记录；worker 转录含 `[QUEEN_REPLY] request_id=…` | 证据 `queen-transcript.json` / `worker-transcript.json` |

一次真实运行的链路（`summary.txt` 原样摘录）：

```text
chain (model request seq):
  3 worker  escalate tool call
  5 queen   [WORKER_ESCALATION] handoff
  5 queen   list_worker_questions
  6 queen   reply_to_worker
  7 worker  [QUEEN_REPLY] -> report_to_parent

model requests: 2 queen, 3 overseer, 2 worker, 0 other
```

会话事件日志（`session-events.jsonl`）里同一条链的工具记录：

```text
16 tool_call_started   escalate               {"reason":"Database credentials missing","context":"DATABASE_URL is unset ..."}
18 tool_call_completed escalate               Escalation requested to queen; waiting for guidance.
26 tool_call_started   list_worker_questions
27 tool_call_completed list_worker_questions {"count":1,"pending":[{"colony_id":"escalation_e2e", ...}]}
30 tool_call_started   reply_to_worker       {"reply":"Use the staging database.","request_id":"35d919b3..."}
31 tool_call_completed reply_to_worker       {"request_id":"35d919b3...","status":"delivered","worker_id":"worker_1"}
```

worker 转录（`worker-transcript.json`）证明「等待 → 继续」语义：`escalate` 调用后工具结果为
`Escalation requested to queen; waiting for guidance.`；随后一条 user 消息是
`[QUEEN_REPLY] request_id=…`（`is_client_input: true`），worker 从该回合继续并上报。

**这是管线验证，不是模型质量结论**：女王与 worker 的回复都是脚本；本 E2E 证明的是工具面、
事件生产、会话路由、收件箱读写与等待/继续语义的接线，不代表任何真实模型的升级行为。

## 复现

```bash
cd .wt/worker-escalation-channel/go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off \
HIVE_ESCALATION_E2E_OUT=/tmp/escalation-e2e \
  go test -tags e2e ./e2e -run TestWorkerEscalationEndToEnd -v -count=1
```

`-tags e2e` 的测试不进入 `make ci` 的常规测试集。测试自己构建 `./cmd/hive`、在临时
`HIVE_HOME` 写 `configuration.json`（模型指向网关）、物化一个最小 colony（`metadata.json` +
`worker.json`），再走真实 HTTP：`POST /api/sessions{colony_id}` → `POST /api/sessions/{id}/chat`。

## 证据文件（`go_hive/docs/e2e-assets/worker-escalation-e2e/escalation-chain/`）

- `summary.txt` — 链路各步的模型请求序号与计数
- `model-requests.jsonl` — 每次模型请求的 kind（queen / overseer / worker / other）、工具数、应答与工具调用
- `queen-transcript.json` — 会话转录（含注入的 `[WORKER_ESCALATION]`，每条截断 4000 字符）
- `worker-transcript.json` — worker 转录（含 `escalate` 调用与 `[QUEEN_REPLY]` 回合）
- `session-events.jsonl` — 会话事件日志（`escalate` / `list_worker_questions` / `reply_to_worker` 的 started+completed）
- `server-output.txt` — `hive serve` 输出

## 与上游的关系

- 上游 `framework/server/queen_orchestrator.py::install_worker_escalation_routing` 在 queen 会话的
  colony 阶段生产安装；本移植的对应安装点是 `server.Session.BindChatColony`（会话拿到 colony 时
  幂等安装；卸载 colony / 停止会话时从**订阅所在的总线**卸载，见 `session_chat_colony.go`）。
- 事件形状逐字对齐：`ESCALATION_REQUESTED` 带 `colony_id`（colony 作用域订阅的过滤依据）、
  `request_id`（16 字节随机，上游 `uuid4().hex`）、`reason`、`context`、`node_id`、`execution_id`。
- 上游 `session.pending_escalations` 是**内存字典**，不落盘；本移植相同——收件箱随会话进程存活，
  重启后为空的语义与上游一致（不虚构持久化）。durable 的一半是 queen 转录里的
  `[WORKER_ESCALATION]` 记录（本 E2E 已断言），重启后可重建「用户看到过这个问题」。
- 上游 worker 的等待是 loop 内 `_await_user_input(reason=AWAITING_QUEEN)`；本移植在同一位置
  （工具批次之后、judge 之前）阻塞，`inject_input` 唤醒后把女王回复作为 client user 回合写入
  会话再继续，行为与上游 6h'' 段一致。
- 上游在「等待期间被收线」时会再发一条 `Blocked waiting for queen guidance - no input received`
  的升级（问题不随 worker 消失）；本移植在运行被取消时发同一条事件（单测钉住）。

## 已知差异

- 上游 1.0.14 的 `escalate` 工具只发给「legacy primary worker」（`stream_id="worker"`，无冒号），
  parallel worker（`worker:{uuid}`）按 BRD 走 fail-fast `report_to_parent`。本移植的 `run_worker`
  worker 全是 `worker:{id}`，且这是它唯一的 colony worker 形态；为了让 queen 收件箱真实可用
  （审计 GAP-02），本移植把同一通道发给 colony worker。等待/继续语义与上游完全一致，工具面是
  本移植的有意扩展（详见 `queen-lifecycle-tools.md` §6）。
