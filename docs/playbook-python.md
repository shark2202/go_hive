# Python Playbook（`.play.py`）支持

> 可视化原语参考：[playbook-primitives.html](playbook-primitives.html)（8 个运行时原语、worker/converge/lane 选项、工具与生命周期面、Go 原生 JSON 子集、语义边界）。

> 适用：`go_hive`（纯 Go 主二进制，`CGO_ENABLED=0`）  
> 上游契约：`core/framework/host/playbook/runner.py`（`PlaybookRun`）、`core/framework/tools/playbook_tools.py`（`run_playbook`）  
> 实现：`internal/playbook/python.go`、内嵌 `internal/playbook/python_bootstrap.py`、`internal/colony/playbook_tools.go`、`internal/colony/playbook_verbatim.go`、`internal/colony/playbook_notify.go`、`internal/colony/worker_slots.go`  
> 运行时选型（是否内嵌 JS/Lua、npm/PyPI 复用）：`docs/playbook-runtime-options.md`
> 记录日期：2026-11-21（本机实测 Python 3.9.6（系统 `python3`）与 3.14.7（`HIVE_PYTHON`）/ Darwin arm64）

## 1. 结论

`go_hive` 现在可以执行上游格式的 Python playbook（`.play.py`），**脚本字节不变**，并且**对外字段与上游逐字一致**：

- 入口：会话工具 `run_playbook` 的 `playbook`（inline）/ `playbook_path` / `playbook_name`（saved，`playbooks/<name>.play.py`）。
- 脚本契约与上游一致：`meta` dict（至少 `name`）+ `async def run(args)`，命名空间注入 `converge / worker / tracker_query / tracker_count / lane / deadletter / log / phase`。
- 返回值与上游 runner 一致：`{meta, result, logs, deadletter, dispatched}`；工具 payload（`status / run_id / concurrency / dispatched / dead_lettered / result / log_db / playbook_name / playbook_path` + 0 派发 `hint`）、`started` 异步应答全文、`[PLAYBOOK_COMPLETE]` 通知文本、`playbook_runs` 行、解析/保存/`playbook_name` 库先例都被逐字段核对过。
- 对齐不是靠人工对照：`go_hive/scripts/playbook_verbatim_compare.py` 用**真实上游工具**（真实 `PlaybookRun` + 真实 `colony_db`）与**真实 Go 工具**跑同一夹具，逐字段 diff；5 个场景 120/120 检查一致（含 `[PLAYBOOK_COMPLETE]` 全文与 `playbook_runs` 行），真 LLM 端到端 19/19 一致。证据见 `docs/e2e-assets/playbook-verbatim/REPORT.md` 与 `REPORT-real-llm.md`。
- 旧 playbook **无损迁移**：Go 历史保存的 `playbooks/<name>.play.json` 与上游 `playbooks/<name>.play.py` 都能按名直接加载运行，文件不改名、不重写、不要求用户改动；写入保持各自原有格式（native 仍写 `.play.json`，Python 仍写 `.play.py`）。见 §8 迁移矩阵。
- run 期间临时抬高 colony 并发上限（上游 `max(original, concurrency)`，结束恢复）已按上游同形实现；`worker(schema=...)` 的 receipt schema 现在会落到 worker spawn spec（`report_to_parent` 的 `data` 参数 + system prompt）。
- 需要宿主机上存在 **Python 3.8+**（`python3` → `python` 顺序查 PATH，或 `HIVE_PYTHON` 指定）。没找到解释器时返回 `status="unsupported"`、`dispatched=0` 并说明原因，**不会假成功**。
- Go 主二进制不链接 libpython、不引入 cgo、不加任何第三方 Go 依赖；Python 只是一个按需启动的外部子进程。

## 2. 运行架构

```text
Queen / run_playbook tool (Go)
  → RunPlaybook (internal/colony/playbook_tools.go)
      → isPythonPlaybook? (.play.py / 非 JSON 正文)
      → StartPythonPlaybook (internal/playbook/python.go)
          → 解析解释器 (HIVE_PYTHON → python3 → python)
          → 临时目录写内嵌 bootstrap.py
          → exec python3 -u bootstrap.py
          → 发送 init(script)     ──────────────┐
          ← loaded(meta) / load_error          │ 行式 JSON-RPC
          → 校验 meta concurrency（Go 侧策略）  │ stdin/stdout
          → 抬高 colony 并发上限（结束恢复）    │
          → 发送 run(args, concurrency_cap)     │
          ← {"id":N,"method":"query_rows"}      │
          → 查 tracker → {"id":N,"result":rows} │
          ← {"id":N,"method":"dispatch_one"}    │
          → spawn 1 worker + 等报告             │
          → {"id":N,"result":report}            │
          ← result{meta,result,logs,deadletter,dispatched}
      → 落盘 colony.db 的 playbook_runs
      → （超出 grace 的长批次）后台完成 → [PLAYBOOK_COMPLETE] 通知
```

要点：

- **只有两个可调用体跨进程**：`dispatch_one`（派一个 worker 并等终态报告）与 `query_rows`（tracker SELECT）。这与上游 runner 的注入面完全相同；`converge`、重试/退避、lane、`deadletter`、`log`、`phase` 都在 bootstrap 内实现。
- **RPC 并发**：Go 对每个 `dispatch_one` 请求开独立 goroutine，因此脚本侧 `converge` 的 `chunk` / lane 并发是真正并行的；响应按 `id` 关联。
- **stdout 专用**：bootstrap 把脚本的 `sys.stdout` 指向 stderr，脚本里的 `print()` 不会破坏协议。Go 侧遇到非 JSON 行会明确报 `cannot parse interpreter output`，不静默跳过。
- **一次性进程**：每次工具调用 = 一次 `python3` 子进程；脚本的 `load`（编译+执行模块级代码+校验 `meta`/`run`）在 `loaded` 应答前同步完成，所以语法错误、缺 `meta`、缺 `run` 会在任何 worker 派发之前返回真实错误。
- **快速结果 grace 窗口**：与上游一致，工具等待 `HIVE_PLAYBOOK_FAST_GRACE`（默认 1.5s）；窗口内失败/完成直接返回真实结果，长批次返回 `started` 并在后台完成时发 `[PLAYBOOK_COMPLETE]`。

## 3. 解释器解析

| 顺序 | 来源 | 行为 |
| --- | --- | --- |
| 1 | `HIVE_PYTHON` | 显式覆盖；可以是绝对路径，也可以是 PATH 上的命令名。设置了但不可用会直接报错，不静默回退。 |
| 2 | `python3` | `exec.LookPath` |
| 3 | `python` | `exec.LookPath`（兼容只装了 `python` 的环境） |

- 版本要求：bootstrap 启动时检查 `sys.version_info >= (3, 8)`；过旧返回 `PythonVersionError`，工具层按 `unsupported` 处理并给出原因。
- `HIVE_PYTHON` 是 **Go 部署扩展**，上游没有这个变量；无此变量时行为与「系统 python3」一致。
- 找不到解释器时工具返回：

```json
{
  "status": "unsupported",
  "error": "run_playbook: Python playbooks need an external Python 3.8+ interpreter, but none is available. ...",
  "dispatched": 0
}
```

## 4. 脚本契约（与上游一致）

```python
meta = {"name": "fix-ci-failures", "description": "...", "concurrency": 4}

async def run(args):
    await converge(
        pending=lambda: tracker_query("SELECT id, job FROM ci_failures WHERE status='open'"),
        dispatch=lambda row, i: worker(
            task=f"Reproduce and fix CI job {row['job']}",
            data={"id": row["id"]},
            retries=1,
            backoff="exp",
            timeout=600,
        ),
        max_rounds=3,
    )
    return {"remaining": tracker_count("SELECT id FROM ci_failures WHERE status='open'")}
```

| Hook | 语义 | 跨进程 |
| --- | --- | --- |
| `converge(pending=, dispatch=, max_rounds=1, circuit_breaker=None, chunk=None)` | 每轮查 pending → 并发派发 → 再查；未解决行进入 deadletter | 否（bootstrap 内） |
| `worker(task, *, profile, lane, skill, retries, backoff, timeout, schema, phase, data, goal)` | 派一个 worker 并等报告；仅对生命周期失败重试；每次尝试计入 `dispatched`；`schema` 落到 worker spawn spec | **是**（`dispatch_one`） |
| `tracker_query(sql)` / `tracker_count(sql)` | 同步查询，返回行 dict 列表 / 行数 | **是**（`query_rows`） |
| `lane(name, *, concurrency=1, rate_per_min=None)` | 同时并发上限 + 启动速率门（`>= 60/rate_per_min` 秒一个） | 否 |
| `deadletter` | `.add(item)` / `.list()` / `.size` / `len()` | 否 |
| `log(message)` / `phase(title)` | 运行日志与阶段 | 否 |

结果信封：

```json
{
  "meta": {"name": "...", "..." : "..."},
  "result": "<run() 的返回值>",
  "logs": ["phase: ...", "round 1: ...", "..."],
  "deadletter": [{"row": {...}, "reason": "unresolved after 1 round(s)"}],
  "dispatched": 3
}
```

## 5. 工具 payload（上游逐字）

所有 payload 的键名、键序无关的键集合与文本都由 `internal/colony/playbook_verbatim.go` 按上游源码逐字构建：

| 场景 | payload |
| --- | --- |
| 快速路径完成 | `{status:"completed", run_id, concurrency, dispatched, dead_lettered, result, log_db, playbook_name, playbook_path}`，`dispatched==0` 时追加上游原文 `hint` |
| 加载失败 | `{status:"failed", run_id, error:"playbook did not start — <concise>", traceback}` |
| 并发超限 | `{status:"failed", run_id, error:"meta['concurrency']=N exceeds the maximum M. Lower it, split the batch, or raise HIVE_MAX_PLAYBOOK_CONCURRENCY."}` |
| 运行失败（快速路径） | `{status:"failed", run_id, error:"playbook failed — <concise>", traceback, log_db}` |
| 长批次 | `{status:"started", run_id, concurrency, log_db, playbook_name, playbook_path, message}`，`message` 为上游原文（含 `get_playbook_status(run_id='…')` / `stop_playbook(run_id='…')`；这两个名字在本移植里由扁平工具面路由到 `run_playbook` 的 `action`） |
| `[PLAYBOOK_COMPLETE]` | 上游 `_notify_complete` 文本：`<run_id>/<status>/<log_db>/<dispatched>/<dead_lettered>/<result>/<hint?>/<log_tail>`；失败/停止为 `<status>error</status>` + `<error>concise</error>`。Go 端发布到 colony bus，queen 报告订阅对 playbook 通知**原样透传**（不再套 `[WORKER_REPORT]`） |
| 解析/装载错误 | 上游 `_resolve_script` 原文，含 `Pass exactly ONE of …`、`no saved playbook named 'x'. Saved playbooks in this colony: [...]`（Python 列表 repr；Go 会同时列出 `.play.json` 旧库） |

**并发上限**：run 开始（load → persist → concurrency 校验之后）把 colony 上限写成 `max(原值, meta['concurrency'])`，run 结束（快速路径返回前 / 后台完成 finally）恢复原值。数值、时机、甚至「concurrency ≤ 原值时也写一次 no-op」都与上游一致；Go 的 admission gate 改为可调容量（`worker_slots.go`），所以声明的并发真的会被放行。

## 6. 解析与保存规则

| 输入 | 判定 |
| --- | --- |
| `playbook_path` 以 `.play.py` 结尾 | Python（即使正文以 `{` 开头） |
| `playbook_path` 以 `.play.json` 结尾 | 原生 JSON（即使正文不是合法 JSON，也报 JSON 错误） |
| inline `playbook` 正文是 JSON object | 原生 JSON |
| inline `playbook` 其它正文 | Python |
| `playbook_name` | 先找 `playbooks/<sanitized>.play.json`，再找 `.play.py`（见 §8 迁移矩阵） |

- inline Python 在 `load` 成功后按 `meta["name"]` 保存为 `<colony>/playbooks/<sanitized>.play.py`（与上游 `_persist_script` 一致），因此下一次 `run_playbook(playbook_name=…)` 就是 resume：`done` 行不在 pending 查询里。
- 原生 JSON playbook 仍保存为 `.play.json`，两条路径互不影响。

## 7. 生命周期、取消与安全

- **进程组**：`python3` 以独立进程组启动；run 结束/取消时先对进程组发 `SIGTERM`，`2s` 后 `SIGKILL`，并 `Wait` 回收。脚本自己再 fork 的子进程也在这个组里。
- **临时目录**：内嵌 bootstrap 写入 `os.MkdirTemp("", "hive-playbook-*")`，`Close` 时删除；`PYTHONDONTWRITEBYTECODE=1` 避免 `__pycache__`。
- **最小环境**：子进程只拿 `PATH`、`HOME`、locale、`TMPDIR` 等基础变量与解释器变量（`PYTHONUNBUFFERED/PYTHONDONTWRITEBYTECODE/PYTHONIOENCODING`）；不继承 `HIVE_*` 配置开关、provider API key 或凭据。
- **加载超时**：`load` 阶段最长 120s（模块级死循环的兜底）；`run` 阶段有**父侧静默守护**（默认 = worker 默认 timeout + 300s，桥保持静默超过该预算即杀进程组并返回可诊断错误；任何一行桥输出都会重置预算，见 §7.1）；工具 `action="stop"` / 会话取消仍然有效。
- **信任模型**：与上游一致——playbook 是 Queen（可信作者）写的进程内代码，`exec`/子进程都不是安全沙箱；不要执行不可信来源的脚本。
- **协议失败**：非 JSON 输出、意外退出、无结果退出都返回 `PythonBridgeError`，附带退出码与 stderr 尾部，不当作成功。

### 7.1 超时与故障处置（父侧静默守护）

`Execute` 有**父侧静默守护**（不是整段 run 的总时限——上游 `run()` 本身无总时限）：预算默认 = worker 默认 timeout（`PythonOptions.DefaultTimeout`，未设置时 600s，同一数值也写进 init 交给 bootstrap 作为「脚本未指定 timeout 的 worker」默认值）**+ 300s 静默宽限**，即默认 900s；可用 `PythonOptions.StallTimeout` 覆盖（不设环境变量：Go 侧不引入上游没有的 env 面）。桥**每产生一行输出都会重置预算**（`touchActivity`），所以一个合法但很长的批次只要还在推进就不会被切；连续静默超过整个预算（典型：回复被丢、reader 线程卡死、脚本自己的钩子死锁）才判定停摆：

1. 对子进程**进程组**发 `SIGTERM`；2s（`pythonKillGrace`）后仍存活则 `SIGKILL`；
2. 等在 `cmd.Wait` 上回收子进程，并等 `readLoop` / `drainStderr` 读到 EOF 退出（都带界，不无限等待）；
3. 返回 `PythonBridgeError`，文案（可直接读）形如：

```text
python playbook: no bridge activity for 900s (bridge stalled); the interpreter did not answer and was
terminated; bridge requests served=7; in-flight=[8:dispatch_one]; stderr: <子进程 stderr 最后 20 行>
```

- `served` / `in-flight` 是**父侧视角**的桥请求计数：`served` = 已写回回答的 `query_rows` / `dispatch_one` 请求数；`in-flight` = 已收到、还没写回答的请求（`id:method`）。子进程卡在某个 hook 上时 `in-flight` 会点名它；`in-flight=[]` 而 `served>0` 则说明父侧该做的都做了（典型是子进程内部等待/死锁）。老实现里这类停摆表现为**父侧 `Execute` 永远等事件**（工具调用或后台完成 goroutine 一直挂着），现在最多等一个 run 时限。
- **返回前子进程必已回收**：超时路径先杀进程组再返回（`cmd.ProcessState != nil`），不留孤儿；脚本自己 fork 的子进程也在同一进程组内（Windows 例外见 §10/§11）。
- **写入也有限**：写 `run`（或 init）时若子进程不再读 stdin、管道写满，父侧最多等到同一个静默预算，然后 `SIGTERM` 并返回 `cannot start the run: bridge write did not complete within …`，不会卡在管道写里。
- **与 `ctx` 取消的关系**：`ctx` 取消仍立即 `terminate` 并按 `ctx.Err()` 返回（调用方 `Close` 负责回收）；父侧时限只兜「没人取消、桥又不回应」这一态。
- **行为边界（与上游一致）**：worker 的 timeout 语义不变（默认 600s/个，见 §8 表）。静默守护只在「桥连续 N 秒没有任何输出」时触发，合法长批次（worker 在跑、完成会回一行）不受影响；确实是脚本内部静默死循环的场景会被如实终止并报错。要改变静默预算用 `PythonOptions.StallTimeout`，**不要**为了这个去放大 `DefaultTimeout`（那会改变 worker 超时口径）。
- **工具层表现**：快速 grace 窗口内，该错误按上游格式返回成 `status="failed"`（`playbook failed — <concise>` + traceback）；超出窗口的后台 run 用同一错误写 `playbook_runs` 行并发 `[PLAYBOOK_COMPLETE]`（`<status>error</status>` + `<error>`），不再存在「永远 running」的 run。

**根因（已修，2026-11-21）**：子进程在**发送请求之前**没有登记等待槽——`_rpc_sync` / `_rpc_async` 先 `_emit` 请求、后写 `_pending[id]`。宿主侧回答 `query_rows` / `dispatch_one` 只需几微秒（本地假 colony 尤其快）；机器负载高时，回复可能在子进程完成登记前就被 reader 线程读到，找不到槽只能丢弃，请求方于是永久阻塞（父侧表现就是 `Execute` 等事件、子进程活着但不说话）。实测证据：一次 40s 超时转储中，同批 8 次跑有 3~5 次挂；对挂住的子进程做多线程 Python 栈转储，主线程停在 `_rpc_sync → threading.Event.wait`，reader 线程正常停在 stdin read；诊断日志记录到 `response for unknown id=1/3/4/5 arrived before registration (DROPPED)`。修复：**先登记、后发送**（`python_bootstrap.py`），并在 reader 收到「无主回复」时向 stderr 打一条诊断（老行为是静默丢弃）。

**测试夹具无责**：`bridgeFakeColony` 行为正确，问题在桥的请求/响应顺序，与夹具无关。

| 现象 | 判定 | 文案/行为 |
| --- | --- | --- |
| 子进程退出、未给结果 | 桥退出 | `python playbook exited before producing a result (exit code N)` + stderr 尾部 |
| stdout 出现非 JSON / 协议错 | 协议失败 | `cannot parse interpreter output …` + 选中行 |
| 连续静默超过预算 | 桥停摆（父侧杀进程组） | `no bridge activity for Ns (bridge stalled)` + 请求计数 + stderr 尾部 |

回归测试：`TestPythonBridgePendingRequestRegisteredBeforeSend`（确定性探针：回复与登记同时发生时旧实现必挂、新实现必过）、`TestPythonBridgeRunDeadlineStopsStalledChild`（故意不回包的 stub 子进程）、`TestPythonBridgeRunDeadlineCutsSilentLiveBridge`（真实 bootstrap + 正常但不出结果的脚本）、`TestPythonPlaybookCloseIsIdempotent`（并发重复 `Close`）。

## 8. 与上游 runner 的逐项对表

| 行为 | 上游 `runner.py` / `playbook_tools.py` | 本实现 | 证据 |
| --- | --- | --- | --- |
| `meta` / `run` 校验 | `load()` 同步 | bootstrap `load()` 同步，错误经 `load_error` 回传 | `TestPythonBridgeMissingMeta/MissingRun/SyntaxError` |
| converge 收敛 / 再查 | `converge()` | bootstrap 同实现 | `TestPythonBridgeConvergesAllRows` |
| 重试 + 退避 | `worker()` 内 `retries+1` 次 | 同 | `TestPythonBridgeRetryThenSucceed` |
| 永久失败只死信一次 | `converge` 末尾统一 deadletter | 同 | `TestPythonBridgePermanentFailureDeadLetteredOnce` |
| lane 并发 / 速率门 | `_RateGate` + `asyncio.Semaphore` | 同（bootstrap 移植） | `TestPythonBridgeLaneLimitsConcurrency`、`RateGateSpacesDispatches` |
| chunk 默认 = cap | `converge` 内 | 同 | `TestPythonBridgeChunkDefaultsToCap` |
| cap 警告文案 | `note: ... bounds this run` | 同 | `TestPythonBridgeWarnsWhenChunkExceedsCap` |
| 返回值信封 | `run_loaded()` | 同 | 各测试断言 `meta/result/logs/deadletter/dispatched` |
| resume（重跑只做 gap） | tracker 为事实源 | 同 | `TestRunPlaybookSavedPythonNameResumes`、`TestPythonBridgeRerunIsResume` |
| 根因 + playbook 行号 | `_error_detail` | bootstrap 同实现（`<playbook>` 行号） | `TestPythonBridgeRuntimeErrorSurfaces` |
| `goal` 透传 | 设置才转发 | 同（未设置不出现） | `TestPythonBridgeGoalPassesThrough` |
| 快速路径 payload | 见 §5 表 | 逐字 | `TestPlaybookVerbatimCompletedPayloadFields`、双侧 harness `converge`/`load_error`/`run_error` |
| 0 派发 `hint` | `_zero_dispatch_hint` | 逐字 | `TestPlaybookVerbatimZeroDispatchHintMatchesUpstream` |
| `started` 异步应答 | 原文 message | 逐字 | `TestPlaybookVerbatimStartedPayloadAndMessage`、harness `started` |
| `[PLAYBOOK_COMPLETE]` | `_notify_complete` 文本 | 逐字（bus 透传） | `TestNotifyPlaybookCompletePublishesOnTheColonyBus`、harness `started` + 真 LLM 重放 |
| run 期间抬高 colony 并发上限 | `max(original, concurrency)`，结束恢复 | 同（可调 gate） | `TestPlaybookVerbatimConcurrencyCapRaiseAndRestore`、`TestPlaybookCapRaiseAdmitsDeclaredConcurrency`、harness `colony.cap_moves` |
| `schema` 落到 spawn spec | `entry["report_schema"]` | `BatchTask.ReportSchema` → `report_to_parent.data` + prompt 指令 | `TestPlaybookVerbatimSchemaRidesSpawnSpec`、`TestPlaybookSchemaSpecializesWorkerSurface`、harness `spawn.report_schema` |
| `playbook_runs` 行 | `record_playbook_run` | 同 schema/字段；错误行 `name=NULL`、`created_at` 用 Python isoformat（`+00:00`，微秒为 0 时省略） | `TestPlaybookRunLogErrorRowHasNullName`、harness `runs[*]` |
| 按名解析 / 保存 | `_resolve_script` / `_persist_script` | 同字符串、同保存位置；按名先 `.play.json` 后 `.play.py` | §8 迁移矩阵 + `TestPlaybookMigration*` |

## 9. 迁移矩阵（旧 playbook → 新二进制）

判定：**0 改名、0 语义改动、0 用户动作**。两代 playbook 都是原文件直接跑。

| 旧来源 | 盘上形态 | 新二进制行为 | 写回 | 证据 |
| --- | --- | --- | --- | --- |
| Go 历史（native JSON） | `colonies/<id>/playbooks/<name>.play.json`（历史任何版本写出的字段集合；`nativePlaybookSpec` 自引入以来未变） | `run_playbook(playbook_name="<name>")` 直接加载；`playbook_path` 指向该文件；converge/重试/退避/死信/resume 语义不变 | **同名同格式**：inline native 仍写 `.play.json`，字节等于传入正文；run 不重写已存在的文件 | `TestPlaybookMigrationLegacyNativeJsonRunsUnchanged`、`TestPlaybookMigrationNativeWritesKeepFormat` |
| 上游 Python | `colonies/<id>/playbooks/<name>.play.py`（上游 `_persist_script` 的字节） | 直接加载执行（外部解释器），`playbook_name`/`playbook_path` 字段指向原文件；lane/deadletter 等上游钩子可用 | 不产生 `.play.json` 影子文件、不改写 | `TestPlaybookMigrationUpstreamPlayPyRunsUnchanged`（夹具 `internal/colony/testdata/playbook-migration/upstream-lanes.play.py`） |
| 两边同名共存 | 同名 `.play.json` + `.play.py` | 按名解析优先级：`.play.json`（旧 native）→ `.play.py`；另一个仍可用 `playbook_path` 直接跑。库列表诊断两种都列出 | 不改动任何一个 | `TestPlaybookMigrationBothFormatsResolveDeterministically` |
| `colony.db` 历史行 | `playbook_runs` 旧行 | 只读展示（`action="status"`）；不迁移、不回填 | 不动 | 表 schema 与上游一致（`colony_db.go`） |

注：`.play.py` 与 `.play.json` 同名共存时按名优先旧 native 文件，是为了让历史入口不被静默改变；要切到 Python 版，删掉 `.play.json` 或用 `playbook_path` 指定即可（两者都不需要改文件内容）。

## 10. 已知例外（有意，非字段缺失）

1. **无解释器 → `status="unsupported"`**：上游进程内执行，没有「没有 Python」这一态；Go 侧如实报告能力缺失、`dispatched=0`，绝不假成功。
2. **`action` / `run_id` 折叠参数**：上游把 `get_playbook_status` / `stop_playbook` / `list_playbook_runs` 作为独立工具；本移植是一个工具 + 两个折叠参数，扁平工具面把这三个名字路由到对应 action。工具 schema 的这一处例外记录在 `docs/tool-contract-diff.md` §13；`status`/`stop` 自己的 payload 属于折叠面。
3. **无 colony / 无 binding 的错误文案**：Go 的绑定模型与 `session.colony` 不同，文案如实描述本移植的接线错误。
4. **JSON 对象键序**：`result_json` / 通知里的 `<result>` 由 Go 排序键输出，上游按插入序；解析后字段等价（同上，无插入序可用）。
5. **`traceback` 栈帧**：其余内容（root cause、`(playbook line N)`）一致；栈帧本身必然一侧是 `runner.py`、一侧是 `bootstrap.py`。
6. **Windows**：进程清理只有 `Process.Kill()`（无进程组语义），脚本自己 fork 的子进程可能残留（见 §11）。

## 11. 未验证项

- **Windows 无 Python**：`python3`/`python` 查找与 `HIVE_PYTHON` 逻辑已写好，但未在无 Python 的 Windows 上实跑；Windows 的进程清理只用 `Process.Kill()`（无进程组语义），脚本自己 fork 的子进程可能残留。这是本次明确的未验证边界。
- **任意第三方 import**：bootstrap 不做包枚举，脚本继承宿主 `python3` 的 `sys.path` / site-packages；没有在本机验证「某个特定第三方包」可用性（取决于宿主环境）。
- **其他模型/provider**：真实模型 E2E 只跑了本机配置的 `deepseek-flash-1017`（OpenAI 兼容网关）；其他 provider 的工具调用格式未逐一验证。
- **超大结果 / 超长单行**：协议单行上限 32 MiB，未做压力测试。
- **Python 3.8/3.10/3.12 等全版本矩阵**：本机实测 3.9.6（系统）与 3.14.7（`HIVE_PYTHON`）；bootstrap 只断言 `>= 3.8`，没有覆盖所有版本组合。

### 真实 LLM 端到端（已跑通）

`internal/cli/playbook_real_llm_e2e_test.go` 与 `playbook_verbatim_e2e_test.go` 用真实模型跑完整链路：`RunPlaybook → Python bridge → dispatch_one → colony worker → 真实 LLM → report_to_parent → bridge result`。

- 本机配置：`HIVE_HOME=~/.hive-go`，模型 `deepseek-flash-1017`（OpenAI 兼容网关）。
- `TestRealLLMPlaybookPythonWorkerE2E`（2026-11-21）：**PASS**；真实模型返回 `tool_calls=[report_to_parent {"status":"success","summary":"REAL_LLM_E2E_OK",...}]`。
- `TestRealLLMPlaybookVerbatimConvergeDeadletterE2E`（2026-11-21）：**PASS**；2 行 converge 各派一个真实模型 worker（`dispatched=2`），无人标记 done → `dead_lettered=2`；工具返回上游原文 `started`，后台完成发出与上游逐字的 `[PLAYBOOK_COMPLETE]`（`<dispatched>2</dispatched>`、`<dead_lettered>2</dead_lettered>`、`<log_tail>` 含 `converge finished`）；cap 恢复为 1。
  - 记录的真实 prompt/报告被 `playbook_verbatim_compare.py --real-llm` 重放进真实上游工具，19/19 字段一致（见 `docs/e2e-assets/playbook-verbatim/REPORT-real-llm.md`）。
- 该测试是 opt-in，默认 skip，不拖垮 CI：

```bash
HIVE_HOME=~/.hive-go HIVE_REAL_LLM_E2E=1 \
  go test ./internal/cli -run TestRealLLMPlaybook -v -count=1 -timeout 12m
```

## 12. 复现与排障

```bash
cd go_hive/go-hive

# 真实 python3 的桥测试
go test ./internal/playbook -run 'TestPython|TestResolvePython' -v -count=1

# 工具层：payload/迁移/并发上限/schema/spawn spec
go test ./internal/colony -run 'TestRunPlaybook|TestPlaybookVerbatim|TestPlaybookMigration|TestPlaybookCapRaise|TestWorkerSlotGate|TestNotifyPlaybookComplete' -v -count=1

# 字段级双侧对照（真实上游工具 vs 真实 Go 工具）
cd ../..
PYTHONPATH=core uv run --project core python go_hive/scripts/playbook_verbatim_compare.py
# 真 LLM 记录重放（先跑上面的 opt-in E2E）
PYTHONPATH=core uv run --project core python go_hive/scripts/playbook_verbatim_compare.py --real-llm

# 无解释器行为（应 status=unsupported、dispatched=0）
PATH= HIVE_PYTHON= go test ./internal/colony -run TestRunPlaybookPythonWithoutInterpreterIsUnsupported -v
```

排障对照：

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `status="unsupported"`，`error` 含 no interpreter | PATH 与 `HIVE_PYTHON` 都找不到 Python | 安装 Python 3.8+ 或设置 `HIVE_PYTHON` |
| `playbook did not start — SyntaxError: ... (playbook line N)` | 脚本语法错误 | 修脚本第 N 行 |
| `playbook did not start — PlaybookScriptError: playbook must define a meta dict ...` | 缺 `meta` | 补 `meta = {"name": ...}` |
| `playbook failed — NameError: ... (playbook line N)` | `run()` 内运行时错误 | 修脚本；`traceback` 字段有完整栈 |
| `Python 3.8+ required` / `PythonVersionError` | 解释器过旧 | 换 3.8+ 或设 `HIVE_PYTHON` |
| `cannot parse interpreter output ...` | 解释器/脚本往 stdout 写了非协议内容 | 不要直接写 fd 1；用 `log()` |
| `no bridge activity for 900s (bridge stalled); ... served=N; in-flight=[...]; stderr: ...` | 桥连续静默超过守护预算（回复被丢、reader 卡死、脚本钩子死锁） | 先看 `in-flight` 点名的是哪个 hook、再看 stderr 尾巴；需要更长静默预算用 `PythonOptions.StallTimeout`（见 §7.1） |

## 13. 相关文档

- `docs/support-matrix.md` — 支持矩阵中的「Python `.play.py` playbook」行
- `docs/queen-lifecycle-tools.md` — `run_playbook` 控制动作
- `docs/tool-contract-diff.md` §13 — `run_playbook` 的 schema 例外（折叠参数）
- `docs/e2e-assets/playbook-verbatim/` — 双侧字段对照报告、真 LLM 记录与夹具
- 仓库根 `docs/playbook-spec.md` — 上游 playbook 契约（Python 版）
