# 可靠性真机证据：崩溃恢复（kill -9）与并发浸泡

> lane `reliability`，2026-11-22。基线 `main = cb05c2ec`（`cb05c2ecab66e9efb1b3f82e381d76464656f347`），
> 工作树 `.wt/reliability`（分支 `reliability`）。
> 脚本：`go_hive/scripts/reliability_soak_recovery.sh`（可重跑，任一步 FAIL 时退出非零）。
> 证据：`go_hive/docs/e2e-assets/reliability-2026-11-22/`（本文所有数字与文件一一对应）。
> 本 lane 未改产品代码（仅新增脚本/证据/文档），因此 merge_postcheck 不适用；工作树实跑
> `bash go_hive/scripts/merge_postcheck.sh` 退出 0（gofmt/wiring/C12/arch 四关全过）。
> 实测对象：真实模型 `deepseek-flash-1017`（openai 兼容 `https://token.tokenbroad.com/v1`，
> key 由 `~/.hive-go/env` 静默加载）；二进制 `sha256 a93af17d91c676e0…`，由本树离线构建
> （`CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod`）。临时 `HIVE_HOME` 在仓库外，
> 不随证据提交。

## 0. 结论

| 轴 | 结果 | 关键数字 |
|---|---|---|
| A 崩溃恢复（回合中 `kill -9`） | **PASS** | kill 点 = in-flight 工具调用（dangling 1 个）；重启后 7/7 part 字节级一致、`events.jsonl` 29→29 行（append-only）；已完成的 echo 工具结果经模型引用复述（`CRASH_PRE_…`） |
| A2 park 状态可续 | **PASS** | `ask_user` 问题以 `cursor.pending_input` 跨两次重启保留；回答后回合继续、park 清除 |
| B 并发浸泡 | **PASS** | 10 会话 × 5 轮 = **50/50 轮成功**；0 HTTP 错误 / 0 超时 / 0 `execution_failed`；RSS 102→115 MB（+12 MB）；订阅后 53 个 `execution_completed` 全部送达（缺 0）；健康探针 9/9 |
| 脚本可重跑 | **PASS** | 同一脚本连续两次完整跑（10×5）均 27/27 检查通过；本目录为第二次运行产物 |

## 1. 方法

```bash
cd /Users/mac/hive/.wt/reliability
source ~/.hive-go/env   # 真实 key，不打印
RELIABILITY_WORK=<临时目录，仓库外> RELIABILITY_PORT=8803 \
  bash go_hive/scripts/reliability_soak_recovery.sh
```

- 脚本自己去 `~/.hive-go/configuration.json` 复制 LLM 配置到一次性 `HIVE_HOME`，并对
  `api_key_env_var` 做空值前置检查（缺 key 直接失败，不会伪造成功）。
- 全程只用一个真实 `hive serve` 进程树（port 8803），共三次启动：
  boot1（崩溃注入）→ `kill -9` → boot2（冷恢复验证）→ `kill -9` → boot3（park 恢复 + 浸泡），
  浸泡结束后由脚本停掉（`RELIABILITY_KEEP_SERVE=1` 可保留做现场）。
- 长任务回合的触发：让模型顺序调用 `terminal_exec(echo CRASH_PRE_<tok>)`、
  `terminal_exec(sleep 180; echo CRASH_POST_<tok>, auto_background_after_sec=5)`，再
  `terminal_job_logs(wait_until_exit=true)` 等待——脚本轮询对话 part 与 `events.jsonl`，
  在「已有落盘工具结果 **且** 有 in-flight 工具调用（`tool_call_started` > completed+failed 且有
  dangling call id）」时执行 `kill -9 <serve pid>`（实测 16.1s 命中）。
- 冷恢复：重启同一 `HIVE_HOME` → `GET /api/sessions/history` 冷列表 → `POST /api/sessions`
  `{"queen_resume_from": "<sid>"}` 重开同一 id，再对盘上文件做哈希/清单比对。
- 浸泡口径：10 个会话波次创建（每批 `SOAK_CONCURRENCY` 个，等该批最后一个开场回合落定再建下一批）
  先全部 live，再逐会话 5 轮（`SOAK-<i>-R<r>` token 作为完成判定），轮次并发严格 3；同时 1s 采样进程 RSS、
  5s 外部 `/api/health` 探针（死锁/无响应检测）、每会话一条 SSE 订阅（只看边界事件类型）。
  轮次完成判定用「回复中出现本轮 token」，避免把 stall-check 自动回合（上游行为，模型
  “光说不做”时 nudge）误当成本轮完成。

## 2. A. 崩溃恢复（kill -9 回合中）

崩溃会话 `session_20261122_181835_4de3fe1e`（queen_growth / independent）。

kill 瞬间盘上（`crash-recovery/pre-kill-parts.json`、`crash-recovery/crash-point.json`）：

- 7 个 part：system-reminder / 开场回复 / 长任务 user / assistant(tool_calls ×2) / tool(echo 结果) /
  tool(sleep 已转后台) / assistant(tool_calls=[`terminal_job_logs`]，**悬空**）；
- `execution_started=1, execution_completed=0`；`tool_call_started=3, tool_call_completed=2`，
  dangling = `call_00_g4136qvyar275vfqmh7wfzft`；
- `cursor.pending_input = null`（回合开始即清空上一个 park）——正是「中途崩溃、无持久化等待」形态。

重启与恢复（boot2）：

| 检查 | 证据 | 结果 |
|---|---|---|
| 冷列表仍在（盘上） | `crash-recovery/cold-listing.json`：`cold=true, has_messages=true, message_count=3` | PASS |
| `queen_resume_from` 重开同一 id | `crash-recovery/resume.json` | PASS |
| 崩溃前 7 个 part 字节级保留 | `pre-kill-parts.json`（逐文件 sha256）→ `parts-verify.json`（missing=0 unchanged=0） | PASS |
| 已完成的工具结果可恢复 | echo 结果 `CRASH_PRE_RELCRASH…` 仍在（`post-resume-parts.json`） | PASS |
| `events.jsonl` 追加不重写 | 29→29 行、首行字节一致（`pre-kill-events.jsonl` 与运行后比对） | PASS |
| 恢复的结果**真正进入模型上下文** | 续跑提问「刚才 echo 的原样输出是什么？」→ 回复只含 `CRASH_PRE_RELCRASH179534271532429`（`crash-recovery/continue-stats.json`） | PASS |
| 日志有恢复痕迹 | `serve-boot2.out` 第二次 boot banner；`hive-server.log` 的第二个 boot 块（skill_startup）、resume 的 `POST /api/sessions -> 201`、以及该会话的 `/chat` access 行（`cr_log_traces`） | PASS |

说明：`kill -9` 落在 `terminal_job_logs` 等待期间，该 in-flight 工具结果必然丢失（未完成）；
其调用在恢复后由 agentloop 的悬空修复补为 `ERROR: Tool execution was interrupted.`，因此会话
可以继续而不会给上游模型发送非法消息序列——本项在续跑回合实际走通。

## 3. A2. park 可续（ask_user → kill -9 → resume → 回答）

park 会话 `session_20261122_181856_1e1eaa7b`：模型调用 `ask_user` 提问
「What color should the widget be?」（id `widget_color`），`cursor.pending_input.reason=ask_user`
（`park/park-cursor.json`）→ `kill -9` → boot3 → resume 同 id：

- 恢复后 `cursor.pending_input` 仍是 `ask_user` 且问题文本完整（`park/resume-cursor.json`）；
- 回答 `blue` 后新回合完成，回复非空（“Blue it is. …”），park 清除为 `turn_done`
  （`park/answer-cursor.json`、`park/metrics.tsv`）。

## 4. B. 并发浸泡（10 会话 × 5 轮，真实模型，并发 3）

| 指标 | 值 | 出处 |
|---|---|---|
| 会话 × 轮 / 完成 / 失败 / 超时 | 10 × 5 = 50 / **50** / 0 / 0 | `soak/metrics.tsv`、`soak/turns.tsv` |
| HTTP 非 200 / `execution_failed` | 0 / 0 | `soak/turns.tsv`、各会话 events |
| 单轮延迟 min / p50 / max | 1.108 s / 2.137 s / 4.666 s | `soak/latency.json` |
| 浸泡时长 | 40 s（18:19:19→18:20:05，不含建会话/开场回合） | `soak/metrics.tsv` |
| RSS 起始 / 结束 / 峰值 | 102 MB → 115 MB（峰值 115 MB，+12 MB，1s 采样 43 点） | `soak/rss.csv`、`rss.json` |
| 边界事件：发出(订阅后落盘) / 收到 / 缺失 | 53 / **53** / **0**（`execution_completed` 按 execution_id 去重） | `soak/sse-compare-*.json`、`per-session.tsv` |
| 每轮回复 token 落盘 | 50/50 | `checks.tsv`（`soak_tokens`） |
| 外部健康探针 | 9/9 OK，最差 34 ms | `soak/health.csv` |
| 进程存活 | 浸泡后仍健康 | `serve-boot3.out`、`soak_alive` |

- 53 > 50 的原因：服务端 stall-check 自动 nudge 回合也发 `execution_completed`（本轮 3 个）；
  这类回合不被计为轮次（完成判定用 token），但同样进入「不丢帧」比对——两个方向都对齐才算过。
- 每会话 5/5：`soak/per-session.tsv`（10 行，sent/received/missing 全为 5/5/0）。

## 5. 失败分析

**产品质量结论：在本轴定义的可测属性上未发现缺陷**——无数据丢失（part 哈希逐一比对）、
无死锁/卡死（外部探针 9/9、50/50 回合按 token 收敛）、无错误（0 HTTP/0 超时/0
`execution_failed`）、无边界事件丢帧（0 missing）、RSS 有界（+12 MB，末段平台）。

**测量陷阱（harness，已修）**：首轮冒烟（2×2）用「`execution_completed` 计数 +1」判定本轮完成，
被 stall-check 自动回合提前满足，导致在最后一轮真正结束前就撤掉 SSE 订阅，出现假“丢帧 2”。
修正为：① 以本轮回复 token 为完成边界；② 撤订阅前先快照 `events.jsonl` 作为“已发出”基线；
③ “发出”只统计订阅时刻之后的执行（`sse_since`）。修正后两次完整跑均 0 丢帧，冒烟与完整跑
数字见本目录 `summary.md` 与 `soak/sse-compare-*.json`。

**观察到的上游 parity 缺口（非阻断，本次未改代码，建议另行 port）**：

1. **cold-resume mid-turn guard 未移植**。判据：崩溃瞬间 `cursor.pending_input=null`
   （`crash-recovery/pre-kill-cursor.json`），resume 后 Go 写入的是 `turn_done`
   （`post-resume-cursor.json`）。上游 `core/framework/agent_loop/agent_loop.py`（§6b1½）在此形态下
   会注入 “[System] The session was interrupted …” 并把 `pending_input.reason` 置为
   `cold_interrupted`；Go 侧 `internal/agentloop/park_reason.go` 已定义 `ParkReasonColdInterrupted`
   但全仓无生产者（`grep ColdInterrupted` 仅枚举/判定）。
2. **resume 后 park 可见性**。重启后新总线无事件，`GET /api/sessions/{id}/snapshot` 与 SSE 首帧
   `session_snapshot` 都是空态（`park_reason=null, awaiting_input=false`），即使 cursor 已是
   `ask_user`（`park/resume-cursor.json` vs `park/resume-snapshot.json`；崩溃会话同）。
   `events.jsonl` 里已有历史问题事件，重新订阅 `/events/history` 仍可回放；差异在“实时等待态”
   要等下一次事件才恢复。上游恢复路径会在重启后重新进入等待并重发
   `client_input_requested`（重发时 `emit_client_request=false` 避免重复弹问）。

影响评估：两项都不影响本轴验收属性——`kill -9` 后不会自动续跑在途工作（resume 不自动起回合）、
回答 park 问题可正常续跑、无数据丢失；缺口集中在恢复语义与前端可见性。改动会触及
agentloop/server 行为，按本 lane「可靠性证据、非新功能」边界仅记录，未实施。

## 6. 复跑与证据清单

```bash
# 快速冒烟（约 1.5–2 分钟）
cd <repo>/go_hive && SOAK_SESSIONS=2 SOAK_ROUNDS=2 SOAK_CONCURRENCY=2 \
  RELIABILITY_PORT=8801 scripts/reliability_soak_recovery.sh
# 完整（约 1.5–2 分钟；本目录为完整跑产物）
scripts/reliability_soak_recovery.sh
```

- 脚本只清理自己的一次性 `HIVE_HOME`（`$RELIABILITY_WORK/hive-home`），证据目录中的文本逐次覆盖；
  同一 `RELIABILITY_WORK` 可安全重跑。
- 退出码：任一步 FAIL → 非零；`checks.tsv`/`results.json` 为逐项即机器可读结果。

证据目录 `docs/e2e-assets/reliability-2026-11-22/`：

```
summary.md                  运行汇总（结论/三轴明细/全部检查表）
checks.tsv results.json     27 项检查逐条 PASS/FAIL 与计数
build.out config.sha256     本树离线构建日志；所用 LLM 配置哈希（不含 key）
serve-boot{1,2,3}.{out,err} 三次启动 stdout/stderr（含 access log）
crash-recovery/             kill 点、pre/post part 哈希、cursor、冷列表、resume、
                            events 行数、续跑回复、resume 后 snapshot/cursor 观察
park/                       ask_user park cursor、resume/answer cursor、回复、resume 后 snapshot
soak/                       turns.tsv（逐轮延迟/状态）、per-session.tsv、sse-*.sse.jsonl（边界帧，data: 行）、
                            sse-compare-*.json（发出/收到/缺失）、rss.csv、health.csv、metrics.tsv
```
