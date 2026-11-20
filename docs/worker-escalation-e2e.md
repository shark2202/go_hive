# Worker escalation 真实端到端（真实 `hive serve` + 脚本化模型）

本文件记录 worker escalation 生产链路在**真实二进制**上的端到端证据。当前链路是上游为
parallel worker 保留的那一条：

```text
worker 连续重复同一工具调用 ×3
  → doom-loop 检测（agentloop）→ ESCALATION_REQUESTED（colony 戳 + request_id）
  → 会话级 colony 作用域路由 → queen 收件箱 + [WORKER_ESCALATION] 注入
  → list_worker_questions → reply_to_worker → worker 收到 [QUEEN_REPLY] → 继续/终止
```

不是单测对表：真实 `hive serve` 进程、临时 `HIVE_HOME`、真实 HTTP、真实 colony 目录与 worker
子进程内运行；唯一被脚本化的是模型（`e2e/escalationgw`：确定性本地 OpenAI 兼容网关，按请求里
的工具面与消息内容给出脚本化回合）。

- 日期：2026-11-21
- 被测：`.wt/escalation-doomloop/go_hive/go-hive`（module `github.com/aden-hive/hive-go`，分支 `escalation-doomloop`，基线 `db5c4c1b`）
- 网关：`go_hive/go-hive/e2e/escalationgw`（本 lane 改写为 doom-loop 剧本）
- 证据目录：`go_hive/docs/e2e-assets/worker-escalation-e2e/escalation-chain/`

## 1. 对齐决定：parallel worker 不接 escalate，失败即终态

上游 1.0.14 的精确行为（逐处核实）：

| 位置 | 行为 |
| --- | --- |
| `core/framework/agent_loop/agent_loop.py::_execute_impl`（工具面） | `is_parallel_worker = self._reports_to_parent(stream_id)`（`worker:<id>` 或带 `on_report`）；只有 `stream_id not in ("queen","judge","overseer") and not is_parallel_worker` 才追加 `escalate`。parallel worker 得到的是 `report_to_parent`。 |
| 同上（6h worker stall） | parallel worker 在 text-only 轮次超过 grace 后**合成** `report_to_parent(status='failed')` 并终止：不发 `ESCALATION_REQUESTED`、不同步等待。legacy primary worker（`stream_id="worker"`，无冒号）才走 escalate + 等待。 |
| 同上（6f' tool doom loop） | 检测到连续相同工具调用后，对 `not supports_direct_user_io and not event_triggered and stream_id not in ("queen","judge") and event_bus` 的流（**包含 parallel worker**）发 `ESCALATION_REQUESTED`（`reason="Tool doom loop detected"`），追加 `[SYSTEM] Escalated tool doom loop to queen for intervention.`，清空指纹后**继续**（不停车、不等待）。 |
| `core/tests/test_run_worker_tool.py::test_parallel_worker_stall_auto_fails_with_synthetic_report` | 钉住 BRD 不变式：parallel worker **不得**发 `ESCALATION_REQUESTED`（stall 路径），只能 fail-fast report。 |
| `docs/key_concepts/worker_agent.md` | “**No escalation** — it has no live audience. It can't ask the Queen or the user a question mid-run, so it never blocks waiting for an answer.” |

上游 `ColonyRuntime` 的 `run_worker` worker 就是 `worker:<id>`（`host/colony_runtime.py`：
`stream_id=explicit_stream_id or f"worker:{worker_id}"`），因此上游的 `run_worker` worker
**本来就没有** `escalate` 工具；`host/worker.py` 也没有升级处理（只有报告/终止）。

**本 lane 的决定：对齐。** `colony.workerTools` 不再向 colony worker 广告 `escalate`，并在分发处
同时拒绝 `worker:<id>` 的 `escalate` 调用（防模型幻觉把 fail-fast worker 永久停泊）。worker 的
失败即终态（`report_to_parent(status='failed')` / 循环错误），queen 读 `[WORKER_REPORT]` 后重派
或接管。

影响：

- 上一 lane 的“worker 调 escalate → 停车 → 女王回复 → 继续”链路不再是生产形态；`escalate`
  工具与 `settleQueenInputPark` 仍保留给 legacy `worker` 流（本 port 尚无该入口），循环级测试
  继续覆盖其语义。
- `list_worker_questions` / `reply_to_worker` 的生产写入方改为**doom-loop 自动升级**（上游为
  parallel worker 保留的唯一升级生产者），收件箱不再依赖非上游工具面扩展。
- 失败语义与上游一致：doom-loop 的确定性停车（5 次）是 port 的额外终态边界，见 §5。

## 2. doom-loop 自动升级生产者（本 lane 新增）

- 检测点：`internal/agentloop/loop.go::observeToolBatch` → `DoomLoopDetector.Observe`
  首次跨过阈值（默认 3 次同名同参）时调用 `l.escalateDoomLoop(agentCtx, tool, streak)`。
- 生产者：`internal/agentloop/escalate.go::escalateDoomLoop` → `AgentContext.EscalationSink`
  → `internal/colony/worker_escalation.go::requestWorkerEscalation`（colony_id +
  `request_id`），与手动 escalate 走**同一个**事件形状与会话路由。
- 事件字段：`reason="Tool doom loop detected"`、`context="`tool` called N times in a row with
  identical arguments and the same result"`、`stream_id=worker:<id>`、`node_id`、
  `execution_id`、`colony_id`、`request_id`。
- 不等待：升级是信息性的（上游同语义），worker 继续；能继续工作的 worker 会在
  `InjectInput` 后收到 `[QUEEN_REPLY]`，已经停车的 worker 由 `reply_to_worker` 返回
  `worker_not_active`。

### 阈值与去重（防重复/风暴）

| 机制 | 值/位置 | 作用 |
| --- | --- | --- |
| 警告/升级阈值 | `DefaultDoomLoopThreshold = 3`（`doom_loop.go`） | 第一次检测即升级，与上游 `tool_doom_loop_threshold=3` 同一量级 |
| 停车阈值 | `DefaultDoomLoopParkThreshold = 5`（port 增加，Parity-exempt） | 重复不收敛时以 `DoomLoopParkedError` + `ParkReasonDoomLoop` 终止，不无限消耗预算 |
| 每 streak 一次 | `DoomLoopDetector.Observe` 每个 streak 只返回一次警告；升级挂在该返回值上 | 同一次重复只产生 1 个 `ESCALATION_REQUESTED` |
| 每 turn-loop 一次 | `AgentLoop.doomLoopEscalated` 锁存（`ExecuteTurn` 重置） | 一个 worker 回合内即使 thrash 出多个 streak 也只升级一次，避免收件箱风暴 |
| 与手动 escalate 不重复 | parallel worker 工具面不含 `escalate`，分发层也拒绝 `worker:<id>` | 不存在第二个生产者可重复；legacy `worker` 流仍保留手动语义 |
| 发布失败 | sink 返回 error 时释放锁存 | 瞬态总线错误后下一次 streak 可重试，而不是永久沉默 |

## 3. 结论（本次真实运行）

用例 `TestWorkerEscalationEndToEnd` PASS（2026-11-21，1.7s）：

| 步骤 | 断言 | 实测（模型请求序号） |
| --- | --- | --- |
| 会话绑定 colony，overseer 派工 | 会话 `201`；`run_worker` 真实启动 worker（带 `max_iterations=60` 脚本参数，见下） | seq 1 overseer 调 `run_worker` |
| worker 工具面无 `escalate` | worker 请求的 `tools` 不含 `escalate`，含 `report_to_parent` | seq 3 worker |
| doom-loop 触发一次升级 | 前 3 次为同一 `task_list {}`；第 3 次后产生 1 个 `ESCALATION_REQUESTED`（reason=`Tool doom loop detected`） | seq 3/5/6 worker |
| 升级 → 会话路由 | queen 会话收到 `[WORKER_ESCALATION]`，带 `request_id`/`worker_id`/`reason`/`context` | seq 7 queen |
| `list_worker_questions` 读真实收件箱 | 工具返回 `count=1`，与事件 `request_id` 一致 | seq 7 queen |
| `reply_to_worker` 回写 | 工具返回 `delivered`；worker 收到 `[QUEEN_REPLY] request_id=…` | seq 8 queen；seq 11 worker |
| worker 继续并终止 | 恢复后的 worker 回合携带 `[QUEEN_REPLY]` 并调用 `report_to_parent` | seq 11 worker |
| 不重复/不风暴 | queen 转录中 `[WORKER_ESCALATION]` 记录恰好 1 条；worker 转录中 `[SYSTEM] Escalated tool doom loop…` 恰好 1 条 | 证据 `queen-transcript.json` / `worker-transcript.json` |
| 重复后不触发停车 | 升级后 worker 改用不同参数（`task_list {"status":…}` 交替），未达到 5 次停车阈值 | `model-requests.jsonl`：3 次 `{}` + 2 次不同参数 |

一次真实运行的链路（`summary.txt` 原样摘录）：

```text
chain (model request seq):
  3 worker  identical task_list x3 -> doom-loop escalation
  7 queen   [WORKER_ESCALATION] handoff
  7 queen   list_worker_questions
  8 queen   reply_to_worker
  11 worker  [QUEEN_REPLY] -> report_to_parent

model requests: 3 queen, 3 overseer, 5 worker, 0 other
```

**这是管线验证，不是模型质量结论**：worker 的重复调用、queen 的列表/回复都是脚本；本 E2E
证明的是工具面、检测阈值、事件生产、会话路由、收件箱读写与等待/继续语义的接线，不代表任何
真实模型的升级行为。

## 4. 复现

```bash
cd .wt/escalation-doomloop/go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off \
HIVE_ESCALATION_E2E_OUT=/tmp/escalation-e2e \
  go test -tags e2e ./e2e -run TestWorkerEscalationEndToEnd -v -count=1
```

`-tags e2e` 的测试不进入 `make ci` 的常规测试集。测试自己构建 `./cmd/hive`、在临时
`HIVE_HOME` 写 `configuration.json`（模型指向网关）、物化一个最小 colony（`metadata.json` +
`worker.json`），再走真实 HTTP：`POST /api/sessions{colony_id}` → `POST /api/sessions/{id}/chat`。
脚本化 worker 在 3 次重复后阻塞到 queen 的 `reply_to_worker` 被记录（网关自检测），从而让
“仍在运行 → 收到回复 → 继续”这一分支可稳定复现；生产默认 3+1 次迭代对这条测试链太短，所以
`run_worker` 参数带 `max_iterations=60`（合法 override，不是产品默认变更）。

## 5. 行为矩阵（parallel worker，`worker:<id>`）

| 场景 | 本 port 行为 | 上游 1.0.14 | 判定 |
| --- | --- | --- | --- |
| 工具面 | 有 `report_to_parent`，无 `escalate` | 同（`is_parallel_worker` 门控） | 已对齐 |
| 模型幻觉调用 `escalate` | 分发层拒绝，返回 `ERROR: escalate is not available to colony workers…`，不产生事件、不停车 | 分发层只拒绝 queen/judge（会发事件并停泊） | 本 port 更严格（BRD 不变式；防 fail-fast worker 被永久停泊），已记录 |
| 连续 3 次同名同参工具调用 | 1 个 `ESCALATION_REQUESTED` + 提醒 + `[SYSTEM] Escalated…` 通知；worker 继续 | 同（reason/通知一致；上游清空指纹后继续） | 已对齐（去重更强） |
| 连续 5 次同名同参 | 停车：`ParkReasonDoomLoop` + `DoomLoopParkedError`，worker failed | 无确定性停车，靠预算/迭代上限 | port 增强（Parity-exempt），见 §6 |
| 未重复（参数持续变化） | 不产生升级事件 | 同 | 已对齐 |
| 女王回复时 worker 仍存活 | `[QUEEN_REPLY]` 入会话，worker 继续 | `inject_input` 队列，worker 下一回合读取 | 已对齐（本 lane E2E 覆盖） |
| 女王回复时 worker 已终止 | `reply_to_worker` 返回 `worker_not_active`，条目出队 | 注入失败即丢弃（同上） | 已对齐（server 测试覆盖） |
| text-only 轮次耗尽 grace | 未实现上游的 `worker_escalation_grace_turns` 自动失败；worker 由迭代上限 + grace 报告收束 | 合成 `report_to_parent(failed)`，不升级 | **残余差异**（与 escalation 无关，见 §6） |

## 6. 残余与已知差异

1. **确定性停车是 port 增强**：上游 doom-loop 只提醒 + 升级后继续；本 port 在 5 次时停车，
   作为不收敛 worker 的终态边界。停车后 `reply_to_worker` 只能得到 `worker_not_active`。
2. **legacy primary worker 入口不存在**：上游 `stream_id="worker"` 的手动 escalate 路径在本
   port 没有生产者形态；`escalate` 工具构建器、分发与停车/唤醒语义保留并由 loop 级测试覆盖。
3. **`node_tool_doom_loop` 事件仍只覆盖 chat**：上游在检测点对所有流发该事件；本 port 的
   colony worker 目前不发（会话事件日志里不会出现），只有 `ESCALATION_REQUESTED`。属可观测性
   残余，不影响本链路。
4. **text-only stall 自动失败未移植**：见 §5 最后一行；这是 worker 行为矩阵中唯一与 escalation
   无关的已知差异。
5. **收件箱持久化口径**：与上游一致为内存字典；durable 的一半是 queen 转录里的
   `[WORKER_ESCALATION]` 记录（本 E2E 已断言恰好 1 条）。
