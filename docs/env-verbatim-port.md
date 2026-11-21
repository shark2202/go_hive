# env-verbatim-port 车道：20 个 Python 独有 env 的逐项落点

> 事实表：每个名字按 **Python 读取点 / 解析 / 默认 → Go 读取点 / 解析 / 默认 → 状态与证据** 登记。
> 数据来源：`contracts/env-vars.json`（字面量扫描 + 人工核对默认/解析）与 `contracts/config-support-ledger.json`（支持矩阵，含 anchors/tests）。
> Python 路径相对仓库根；Go 路径相对仓库根（Go module 位于 `go_hive/go-hive/`）。

## 0. 计数口径

| 项 | 本车道后 | 发布审计基线（`docs/env-surface-audit.json` @ 622cc236） |
|---|---|---|
| Python | 89 | 89 |
| Go | 118 | 96 |
| 共有 | 83 | 66 |
| Python 独有 | **6**（全部为 GCU 平台差异） | 23 |
| Go 独有 | 35 | 30 |

- 本车道给 14 个 Python 独有名字补了 Go 读取点（tracker 节奏、历史读取线程池、server 文件日志、压缩调试、LLM 回合日志、playbook 快速宽限、sentinel 日志、agent 名注入、6 个资源采样开关）。
- 剩下 6 个 GCU 名字**不补读取点**：它们调的是 Python GCU 进程的堆采样/截图批处理机制，Go 侧该进程被 Go browser bridge 取代（见 §2），登记为 `platform_diff` 而不是 `python_only` 缺口。
- 机器可读：`contracts/env-vars.json`（`summary` / `inventory` / `verification` 三块都带本车道期望值与复算结果）。

## 1. 逐项事实表（20 行）

| 变量 | Python：读取点 · 解析 · 默认 | Go：读取点 · 解析 · 默认 | 状态与证据 |
|---|---|---|---|
| `HIVE_TRACKER_WORKER_TOOL_CALLS` | `core/framework/agent_loop/tracker_snapshot_reminder.py:81` · `_env_int(name, 20)`＝`int(os.environ.get(name, "20"))`，`ValueError` 回 20 · 20 | `go_hive/go-hive/internal/agentloop/tracker_snapshot_reminder.go:42` · `envInt`（`pyenv.Int`：空白/正负号/单下划线同 `int()`）· 20 | **wired** · 支持矩阵 `HIVE_TRACKER_WORKER_TOOL_CALLS`<br>anchors：`tracker_snapshot_reminder.go` `TrackerWorkerCadenceToolCalls`、`NewWorkerTrackerSnapshotReminderSourceWithCadence`<br>tests：`TestTrackerWorkerToolCallsDefaultAndParse`、`TestWorkerTrackerSnapshotReachesARealWorkerTurn` |
| `HIVE_HISTORY_READ_WORKERS` | `core/framework/server/routes_sessions.py:1845` · `max(4, int(os.environ.get(name, "8")))`；裸 `int()` 无 `except`，垃圾值让 handler 抛 500 · 8 | `go_hive/go-hive/internal/server/history_read_pool.go:46` · `envIntOrError(name, 8)` + `max(4, …)`；垃圾值返回 error → 路由 500（与上游 raise 对齐）· 8 | **wired** · 支持矩阵 `HIVE_HISTORY_READ_WORKERS`<br>anchors：`history_read_pool.go` `withHistoryReadSlot`、`routes_session_history.go`（会话历史走该池）、`routes_events_history.go`（events/history 走同一池）<br>tests：`TestHistoryReadWorkersResolvesLikeUpstream`、`TestHistoryReadWorkersGarbageIsAnError`、`TestHistoryReadPoolBoundsConcurrentDiskReads`、`TestSessionHistoryRouteReadsColdSessionsThroughThePool` |
| `HIVE_NO_FILE_LOG` | `core/framework/observability/logging.py:289` · `if not os.getenv(name)`：**只看非空**（`"0"` 也算开） · 未设＝写文件 | `go_hive/go-hive/internal/logging/logging.go:40` · `envNonEmpty(name)`（非空即关；`"0"` 同上游）· 未设＝写 `HIVE_HOME/logs/hive-server.log`（20MB × 3） | **wired** · 支持矩阵 `HIVE_NO_FILE_LOG`<br>anchors：`logging.go` `fileLogDisabled`、`cli/loader_soul_probe.go` `hivelogging.OpenServerLog(home.Root)`<br>tests：`TestNoFileLogDisablesTheFileForAnyNonEmptyValue`、`TestServerLogWritesUnderHiveHomeLogs`、`TestInstallRuntimeLogBufferWritesHiveServerLog`、`TestInstallRuntimeLogBufferHonoursNoFileLog` |
| `HIVE_COMPACTION_DEBUG` | `core/framework/agent_loop/internals/compaction.py:1007` · `if os.environ.get(name)` 非空即写 · 未设＝不写 | `go_hive/go-hive/internal/agentloop/compaction_debug.go:32` · `envNonEmpty(name)` · 未设＝不写 | **wired** · 支持矩阵 `HIVE_COMPACTION_DEBUG`<br>anchors：`compaction_debug.go` `compactionDebugEnabled`/`WriteCompactionDebugLog`、`agentloop/loop.go`（实时压缩路径调用 `writeCompactionReport`）<br>tests：`TestCompactionDebugLogWritesInventoryOnlyWhenEnabled`、`TestCompactionDebugLogRunsOnTheLiveCompactionPath`、`TestCompactionLevelLabelsMatchUpstream` |
| `HIVE_ENABLE_LLM_LOGS` | `core/framework/tracker/llm_debug_logger.py:118` · `.strip().lower() not in ("1","true")` 即关 · 未设＝关 | `go_hive/go-hive/internal/agentloop/llm_debug_log.go:48` · `envOptIn(name)`（`strip+lower` 后仅 `"1"`/`"true"` 开）· 未设＝关 | **wired** · 支持矩阵 `HIVE_ENABLE_LLM_LOGS`<br>anchors：`llm_debug_log.go` `llmLogsEnabled`/`LogLLMTurn`、`agentloop/loop.go`（每回合写 `HIVE_HOME/llm_logs` JSONL）<br>tests：`TestLLMTurnLogJSONLThroughARealTurn`、`TestLLMTurnLogIsSilentWithoutTheOptIn` |
| `HIVE_SENTINEL_LOG` | `core/framework/sentinel/manager.py:62` · `if not os.environ.get(name)` 非空即装文件 handler（`propagate=False`）· 未设＝只在主日志 | `go_hive/go-hive/internal/sentinel/logging.go:46` · `envNonEmpty(name)` · 未设＝不重定向；设了＝`HIVE_HOME/logs/sentinel.log` | **wired** · 支持矩阵 `HIVE_SENTINEL_LOG`<br>anchors：`sentinel/logging.go` `ConfigureLogging`、`server/sentinel_runtime.go` `sentinel.ConfigureLogging(homeRoot)`<br>tests：`TestSentinelLogDivertsToItsFile`、`TestSentinelLogOffByDefault`、`TestSentinelLogPresenceGateAcceptsZero` |
| `HIVE_PLAYBOOK_FAST_GRACE` | `core/framework/tools/playbook_tools.py:184` · `max(0.0, float(os.environ.get(name, "1.5")))`，`ValueError` 回 1.5 · 1.5s | `go_hive/go-hive/internal/playbook/grace.go:34` · `envFloat(name, 1.5)` + `max(0, …)`（垃圾值回 1.5）· 1.5s | **wired** · 支持矩阵 `HIVE_PLAYBOOK_FAST_GRACE`<br>anchors：`playbook/grace.go` `FastGraceSeconds`、`colony/playbook_tools.go`（run 超窗即返回 `started`）、`colony/playbook_notify.go`（后台完成发 `[PLAYBOOK_COMPLETE]`）<br>tests：`TestFastGraceSecondsMatchesUpstream`、`TestPlaybookFastGraceZeroReturnsStartedAndRecordsInBackground`、`TestPlaybookDefaultGraceWaitsOutShortRuns`、`TestNotifyPlaybookCompletePublishesOnTheColonyBus` |
| `HIVE_AGENT_NAME` | **注入，不是读取**：`core/framework/loader/agent_loader.py:1247` `set_mcp_extra_env({"HIVE_AGENT_NAME": agent_path.name, …})`（写进 MCP/工具子进程 env） | **注入，不是读取**：`go_hive/go-hive/internal/cli/session_tools.go:62,70` `SetAgentName` → `registry.SetMCPExtraEnv`；`cli/colony_worker_definition.go:101` `catalogue.SetAgentName(name)` | **wired** · 支持矩阵 `HIVE_AGENT_NAME`<br>anchors：`session_tools.go` `SetAgentName`/`SetMCPExtraEnv(map["HIVE_AGENT_NAME"])`、`colony_worker_definition.go` `catalogue.SetAgentName(name)`<br>tests：`TestColonyWorkerCatalogueInjectsTheAgentName`、`TestColonyWorkerToolSurfaceRunsTerminalWithTheAgentName`（真实 terminal 子进程读回） |
| `HIVE_RESOURCE_INTERVAL_S` | `core/framework/host/runtime_resources.py:55` · `_env_num(name, 15.0, float)`（垃圾值回默认 + `warning`）· 15.0s | `go_hive/go-hive/internal/resources/resources.go:65` · `envFloat(name, 15.0)`；`<= 0` 时回 15.0（Go 侧护栏：0s ticker 会空转）· 15.0s | **wired** · 支持矩阵 `HIVE_RESOURCE_*`（通配条目，含 6 个 anchor）<br>anchors：`resources/resources.go` 6 个 `env<Kind>` 读取 + `cli/loader.go` `resourceMonitor.Start(...)`、`server/app.go`（`/api/health/resources` 挂载）<br>tests：`TestResourceTunableDefaults`、`TestResourceTunableOverrides`、`TestResourceTunableGarbageFallsBackLikeEnvNum`、`TestClassifyUsesEnvThresholds`、`TestMonitorSnapshotResolvesThresholdsAndCapsHistory`、`TestMonitorHistoryIsBoundedByTheEnvAtFirstUse` |
| `HIVE_RESOURCE_HISTORY` | `core/framework/host/runtime_resources.py:57` · `_env_num(name, 480, int)`（垃圾值回默认 + `warning`）· 480 | `go_hive/go-hive/internal/resources/resources.go:76` · `envInt(name, 480)`；`< 1` 时取 1 · 480 | **wired** · 支持矩阵 `HIVE_RESOURCE_*`（同上） |
| `HIVE_RESOURCE_WARN_AVAIL_FRAC` | `core/framework/host/runtime_resources.py:71` · `_env_num(name, 0.25, float)` · 0.25 | `go_hive/go-hive/internal/resources/resources.go:94` · `envFloat(name, 0.25)` · 0.25 | **wired** · 支持矩阵 `HIVE_RESOURCE_*`（同上） |
| `HIVE_RESOURCE_CRIT_AVAIL_FRAC` | `core/framework/host/runtime_resources.py:72` · `_env_num(name, 0.15, float)` · 0.15 | `go_hive/go-hive/internal/resources/resources.go:95` · `envFloat(name, 0.15)` · 0.15 | **wired** · 支持矩阵 `HIVE_RESOURCE_*`（同上） |
| `HIVE_RESOURCE_WARN_RENDERERS` | `core/framework/host/runtime_resources.py:74` · `_env_num(name, 45, int)` · 45 | `go_hive/go-hive/internal/resources/resources.go:96` · `envInt(name, 45)` · 45 | **wired** · 支持矩阵 `HIVE_RESOURCE_*`（同上） |
| `HIVE_RESOURCE_CRIT_RENDERERS` | `core/framework/host/runtime_resources.py:75` · `_env_num(name, 60, int)` · 60 | `go_hive/go-hive/internal/resources/resources.go:97` · `envInt(name, 60)` · 60 | **wired** · 支持矩阵 `HIVE_RESOURCE_*`（同上） |
| `HIVE_GCU_MEMTRACE` | `tools/src/gcu/memtrace.py:112` · `_truthy(os.environ.get(name, ""))` 非空且非假值才启动追踪 · 关 | **无读取点**（设计如此）· — | **platform_diff** · 替代 anchor：`cli/browser_supervisor.go` `startBrowserBridge` |
| `HIVE_GCU_MEMTRACE_FILE` | `tools/src/gcu/bridge_host.py:472`（未设时写默认值 `${GCU_LOG_FILE}.bridge_host`）、`tools/src/gcu/memtrace.py:122` · `.strip()` 输出路径 · 未设＝仅 stderr 摘要 | **无读取点**（设计如此）· — | **platform_diff** · 替代 anchor：`cli/browser_cli.go`（Go 桥取代 GCU 进程） |
| `HIVE_GCU_MEMTRACE_FRAMES` | `tools/src/gcu/memtrace.py:114` · `_env_int(name, 12, 1)`（下限 1）· 12 | **无读取点**（设计如此）· — | **platform_diff** · 替代 anchor：`cli/browser_supervisor.go` `startBrowserBridge` |
| `HIVE_GCU_MEMTRACE_INTERVAL_S` | `tools/src/gcu/memtrace.py:115` · `float(_env_int(name, 60, 10))`（下限 10）· 60 | **无读取点**（设计如此）· — | **platform_diff** · 替代 anchor：`cli/browser_supervisor.go` `startBrowserBridge` |
| `HIVE_GCU_MEMTRACE_TOP` | `tools/src/gcu/memtrace.py:116` · `_env_int(name, 8, 1)`（下限 1）· 8 | **无读取点**（设计如此）· — | **platform_diff** · 替代 anchor：`cli/browser_supervisor.go` `startBrowserBridge` |
| `HIVE_GCU_SCREENSHOT_CONCURRENCY` | `tools/src/gcu/browser/tools/inspection.py:94` · `max(1, int(os.environ.get(name, "2") or "2"))` · 2 | **无读取点**（设计如此）· — | **platform_diff** · 替代 anchor：`browserbridge/bridge.go`（`windowLocks` 串行化）、`browserbridge/commands_page.go` `cmdScreenshot` |

### 1.1 阅读要点

- 「wired」= 运行时真的读了该值，且有 Go 测试证明（支持矩阵逐条给 anchor + 测试名；`TestSupportLedgerWiredEntriesResolve` 会校验 anchor 字符串与测试名仍存在）。
- Python 的 `_env_num` 是 **module 导入期**解析（`SAMPLE_INTERVAL_S` 等常量），Go 是 **每次调用**解析；值域一致，差异只在 Go 能对长驻进程生效（`HIVE_RESOURCE_HISTORY` 的「首次使用时定界」测试固定了这一点）。
- Go 侧两处显式护栏（`INTERVAL_S <= 0 → 15.0`、`HISTORY < 1 → 1`）是防空转/防负深度的口径固定，不是默认值分歧；上游同输入不会更宽松。
- `HIVE_AGENT_NAME` 两侧都只写不读，所以扫描器把它标为 `literal_only`；它的证据是「子进程 env 里真的出现这个键」（见 §1 表中两个测试）。

## 2. 平台差异：6 个 GCU 名字的理由与 Go 替代 anchor

Python 侧 GCU（browser tools）是一个独立进程：`tools/src/gcu/` 自带 memtrace 堆采样与截图批处理，这 6 个 env 只对它生效。Go 端口把「Python GCU 进程」整体换成了 **Go browser bridge**（`go_hive/browserbridge/` + `internal/cli/browser_supervisor.go` 监督的 `hive-browser` CLI），因此**没有可调的对象**——不是"还没接"，而是"对象不存在"。登记为 `platform_diff`（理由 + 替代 anchor 齐备）而不是 `unsupported`：

| 支持矩阵条目 | 覆盖名字 | 理由（`reason` 字段，原文） | 替代 anchor |
|---|---|---|---|
| `HIVE_GCU_MEMTRACE*`（`matches_wildcard`） | `HIVE_GCU_MEMTRACE`、`HIVE_GCU_MEMTRACE_FILE`、`HIVE_GCU_MEMTRACE_FRAMES`、`HIVE_GCU_MEMTRACE_INTERVAL_S`、`HIVE_GCU_MEMTRACE_TOP` | Platform difference: the GCU memory tracer (tools/src/gcu/memtrace.py, 5 vars) belongs to the Python GCU process. The Go port replaces that process with the Go browser bridge + hive-browser CLI (browserbridge module, supervised by startBrowserBridge) and samples no GCU heap, so there is no code path a reader could tune. The substitute mechanism is the Go bridge's process supervision and its own runtime health surface; nothing is silently ignored — the names are registered here and in docs/environment-variables.html. | `go_hive/go-hive/internal/cli/browser_cli.go`（`the CLI that replaces`）<br>`go_hive/go-hive/internal/cli/browser_supervisor.go`（`func startBrowserBridge(...)`） |
| `HIVE_GCU_SCREENSHOT_CONCURRENCY` | 同名 | Platform difference: the Python GCU batches screenshots with a tunable worker count; the Go browser bridge serializes per-window operations with its own locks (windowLocks) and has no batch concurrency knob to size. Registering it as a difference keeps the absence explicit. | `go_hive/browserbridge/commands_page.go`（`func cmdScreenshot(...)`）<br>`go_hive/browserbridge/bridge.go`（`windowLocks map[string]*sync.Mutex`） |

不变量：`envcontract` 的 `TestSupportLedgerCoversEveryGap` 要求每个 Python 独有名字都有矩阵条目；`TestSupportLedgerWiredEntriesResolve` 要求 `platform_diff` 必须带 `reason` **和** anchor（anchor 指向替代机制），否则门禁失败——差异不允许沉默。

## 3. 门禁与复现

```bash
cd go_hive/go-hive
make fmt-check vet test cross parity parity-targets wiring-check   # 或 make ci
```

- `parity` / `parity-targets`：本车道新增测试逐条带 `// Parity:`（指向真实 Python 测试/符号）或带具体理由的 `// Parity-exempt:`，不用文件级豁免覆盖多个测试。
- 契约自洽（支持矩阵、计数、逐名字清单）：

```bash
cd go_hive
uv run --no-project python scripts/check_env_config_contract.py --strict
```

- 计数复算与逐名单比对结果写在 `contracts/env-vars.json` 的 `verification` 块里（`baseline` / `expected` / `reproduced` / `counts_match`）。
