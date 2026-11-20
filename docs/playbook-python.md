# Python Playbook（`.play.py`）支持

> 适用：`go_hive`（纯 Go 主二进制，`CGO_ENABLED=0`）  
> 上游契约：`core/framework/host/playbook/runner.py`（`PlaybookRun`）、`core/framework/tools/playbook_tools.py`（`run_playbook`）  
> 实现：`internal/playbook/python.go`、内嵌 `internal/playbook/python_bootstrap.py`、`internal/colony/playbook_tools.go`  
> 记录日期：2026-11-21（本机实测 Python 3.9.6（系统 `python3`）与 3.14.7（`HIVE_PYTHON`）/ Darwin arm64）

## 1. 结论

`go_hive` 现在可以执行上游格式的 Python playbook（`.play.py`），**脚本字节不变**：

- 入口：会话工具 `run_playbook` 的 `playbook`（inline）/ `playbook_path` / `playbook_name`（saved，`playbooks/<name>.play.py`）。
- 脚本契约与上游一致：`meta` dict（至少 `name`）+ `async def run(args)`，命名空间注入 `converge / worker / tracker_query / tracker_count / lane / deadletter / log / phase`。
- 返回值与上游 runner 一致：`{meta, result, logs, deadletter, dispatched}`；工具 payload 也沿用上游快路径形状（`status / run_id / concurrency / dispatched / dead_lettered / result / log_db / playbook_name / playbook_path`）。
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
          → 发送 run(args, concurrency_cap)     │
          ← {"id":N,"method":"query_rows"}      │
          → 查 tracker → {"id":N,"result":rows} │
          ← {"id":N,"method":"dispatch_one"}    │
          → spawn 1 worker + 等报告             │
          → {"id":N,"result":report}            │
          ← result{meta,result,logs,deadletter,dispatched}
      → 落盘 colony.db 的 playbook_runs
```

要点：

- **只有两个可调用体跨进程**：`dispatch_one`（派一个 worker 并等终态报告）与 `query_rows`（tracker SELECT）。这与上游 runner 的注入面完全相同；`converge`、重试/退避、lane、`deadletter`、`log`、`phase` 都在 bootstrap 内实现。
- **RPC 并发**：Go 对每个 `dispatch_one` 请求开独立 goroutine，因此脚本侧 `converge` 的 `chunk` / lane 并发是真正并行的；响应按 `id` 关联。
- **stdout 专用**：bootstrap 把脚本的 `sys.stdout` 指向 stderr，脚本里的 `print()` 不会破坏协议。Go 侧遇到非 JSON 行会明确报 `cannot parse interpreter output`，不静默跳过。
- **一次性进程**：每次工具调用 = 一次 `python3` 子进程；脚本的 `load`（编译+执行模块级代码+校验 `meta`/`run`）在 `loaded` 应答前同步完成，所以语法错误、缺 `meta`、缺 `run` 会在任何 worker 派发之前返回真实错误。

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
| `worker(task, *, profile, lane, skill, retries, backoff, timeout, schema, phase, data, goal)` | 派一个 worker 并等报告；仅对生命周期失败重试；每次尝试计入 `dispatched` | **是**（`dispatch_one`） |
| `tracker_query(sql)` / `tracker_count(sql)` | 同步查询，返回行 dict 列表 / 行数 | **是**（`query_rows`） |
| `lane(name, *, concurrency=1, rate_per_min=None)` | 同时并发上限 + 启动速率门（`>= 60/rate_per_min` 秒一个） | 否 |
| `deadletter` | `.add(item)` / `.list()` / `.size` / `len()` | 否 |
| `log(message)` / `phase(title)` | 运行日志与阶段 | 否 |

结果信封：

```json
{
  "meta": {"name": "...", "...": "..."},
  "result": "<run() 的返回值>",
  "logs": ["phase: ...", "round 1: ...", "..."],
  "deadletter": [{"row": {...}, "reason": "unresolved after 1 round(s)"}],
  "dispatched": 3
}
```

## 5. 解析与保存规则

| 输入 | 判定 |
| --- | --- |
| `playbook_path` 以 `.play.py` 结尾 | Python（即使正文以 `{` 开头） |
| `playbook_path` 以 `.play.json` 结尾 | 原生 JSON（即使正文不是合法 JSON，也报 JSON 错误） |
| inline `playbook` 正文是 JSON object | 原生 JSON |
| inline `playbook` 其它正文 | Python |
| `playbook_name` | 先找 `playbooks/<sanitized>.play.json`，再找 `.play.py` |

- inline Python 在 `load` 成功后按 `meta["name"]` 保存为 `<colony>/playbooks/<sanitized>.play.py`（与上游 `_persist_script` 一致），因此下一次 `run_playbook(playbook_name=…)` 就是 resume：`done` 行不在 pending 查询里。
- 原生 JSON playbook 仍保存为 `.play.json`，两条路径互不影响。

## 6. 生命周期、取消与安全

- **进程组**：`python3` 以独立进程组启动；run 结束/取消时先对进程组发 `SIGTERM`，`2s` 后 `SIGKILL`，并 `Wait` 回收。脚本自己再 fork 的子进程也在这个组里。
- **临时目录**：内嵌 bootstrap 写入 `os.MkdirTemp("", "hive-playbook-*")`，`Close` 时删除；`PYTHONDONTWRITEBYTECODE=1` 避免 `__pycache__`。
- **最小环境**：子进程只拿 `PATH`、`HOME`、locale、`TMPDIR` 等基础变量与解释器变量（`PYTHONUNBUFFERED/PYTHONDONTWRITEBYTECODE/PYTHONIOENCODING`）；不继承 `HIVE_*` 配置开关、provider API key 或凭据。
- **加载超时**：`load` 阶段最长 120s（模块级死循环的兜底）；`run` 阶段没有人为总时限，靠工具 `action="stop"` / 会话取消终止。
- **信任模型**：与上游一致——playbook 是 Queen（可信作者）写的进程内代码，`exec`/子进程都不是安全沙箱；不要执行不可信来源的脚本。
- **协议失败**：非 JSON 输出、意外退出、无结果退出都返回 `PythonBridgeError`，附带退出码与 stderr 尾部，不当作成功。

## 7. 与上游 runner 的逐项对表

| 行为 | 上游 `runner.py` | 本实现 | 证据 |
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

## 8. 已知差异（有意）

1. **同步工具调用**：本移植在工具调用内跑完整个收敛循环并直接返回结果/失败；上游是「先返回 `started` + `run_id`，后台完成再注入 `[PLAYBOOK_COMPLETE]`」。长批次会占住一轮工具调用；`action="status"` 可看历史、`action="stop"` 可按 `run_id` 取消（取消后已 done 行保留，重跑即 resume）。
2. **colony 并发上限不临时抬高**：上游 tool 在 run 期间把 colony 的 `max_concurrent_workers` 抬到 `max(原值, meta concurrency)`，结束后恢复。Go 的 admission 通道在 runtime 构造时固定，本次未改；`meta["concurrency"]` 仍决定 bootstrap 的 `chunk` 默认值与 lane 警告，但实际同时运行的 worker 数仍受 colony `max_concurrent_workers` 约束（超出部分排队）。这是与原生 JSON playbook 相同的现有边界。
3. **`schema` 未落到 worker spawn spec**：`BatchTask` 没有 `report_schema` 字段；worker 仍拿到原生 `report_to_parent` 面。上游会把 schema 注入 worker 的 prompt/工具。
4. **结果值 JSON 化**：非 JSON 类型（`datetime`、`set` 等）在传输时用 `str()` 转换；上游快路径会因 `json.dumps` 失败而报错。这里选择让 run 成功返回字符串化结果。
5. **`print()` 去 stderr**：防止破坏 RPC；上游的 `print()` 直接进宿主 stdout，不进 `logs`。

## 9. 未验证项

- **Windows 无 Python**：`python3`/`python` 查找与 `HIVE_PYTHON` 逻辑已写好，但未在无 Python 的 Windows 上实跑；Windows 的进程清理只用 `Process.Kill()`（无进程组语义），脚本自己 fork 的子进程可能残留。这是本次明确的未验证边界。
- **任意第三方 import**：bootstrap 不做包枚举，脚本继承宿主 `python3` 的 `sys.path` / site-packages；没有在本机验证「某个特定第三方包」可用性（取决于宿主环境）。
- **其他模型/provider**：真实模型 E2E 只跑了本机配置的 `deepseek-flash-1017`（OpenAI 兼容网关）；其他 provider 的工具调用格式未逐一验证。
- **超大结果 / 超长单行**：协议单行上限 32 MiB，未做压力测试。
- **Python 3.8/3.10/3.12 等全版本矩阵**：本机实测 3.9.6（系统）与 3.14.7（`HIVE_PYTHON`，桥测试与工具层测试均通过）；bootstrap 只断言 `>= 3.8`，没有覆盖所有版本组合。

### 真实 LLM 端到端（已跑通）

`internal/cli/playbook_real_llm_e2e_test.go` 用真实模型跑完整链路：`RunPlaybook → Python bridge → dispatch_one → colony worker → 真实 LLM → report_to_parent → bridge result`。

- 本机配置：`HIVE_HOME=~/.hive-go`，模型 `deepseek-flash-1017`（OpenAI 兼容网关）。
- 结果（2026-11-21）：**PASS**；真实模型返回 `tool_calls=[report_to_parent {"status":"success","summary":"REAL_LLM_E2E_OK","data":{"marker":"REAL_LLM_E2E_OK"}}]`，bridge 信封 `status=completed, dispatched=1, receipt={"marker":"REAL_LLM_E2E_OK"}`。
- 该测试是 opt-in，默认 skip，不拖垮 CI：

```bash
HIVE_HOME=~/.hive-go HIVE_REAL_LLM_E2E=1 \
  go test ./internal/cli -run TestRealLLMPlaybookPythonWorkerE2E -v -count=1 -timeout 12m
```

- 同一模型也已独立验证多模态输入：发送 512px 字母图，模型返回「H，绿色」；说明 worker 的 LLM 通道支持图像块，但 playbook 目前没有自动把图像传给 worker 的入口。
- 进程内多模态链路也已用真实模型跑通（`hive serve` 8788，2026-11-21）：会话 `session_20261121_012728_ea41d806`，模型调用 `attach_file({"paths":["…/shot.png"]})`，工具结果 `inline=image_block`，下一轮模型输出「图片里是一个绿色的字母 **H**」。

## 10. 复现与排障

```bash
cd go_hive/go-hive

# 真实 python3 的桥测试（19 个用例）
go test ./internal/playbook -run 'TestPython|TestResolvePython' -v -count=1

# 工具层：inline / saved / load error / 无解释器（4 个新用例 + 原生回归）
go test ./internal/colony -run 'TestRunPlaybook' -v -count=1

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

## 11. 相关文档

- `docs/support-matrix.md` — 支持矩阵中的「Python `.play.py` playbook」行
- `docs/queen-lifecycle-tools.md` — `run_playbook` 控制动作
- 仓库根 `docs/playbook-spec.md` — 上游 playbook 契约（Python 版）
