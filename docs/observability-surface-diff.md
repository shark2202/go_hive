# 日志与可观测面双跑对照（外观维度 16：observability）

- 基线：`main` f8aa5fe9（工作树 `.wt/appearance-logs`）；Python hive 1.0.14 / Go 1.0.26-go.1。
- 日期：2026-11-21（CST）。
- 方法：同一台机器、同一份临空 `HIVE_HOME`，两侧各自 `serve` 冷启动后访问 `/api/health`、`/api/runtime-logs`，再检查落盘；另用进程内探针验证轮转与 LLM 调试日志；看门狗用夹具仓库做故障注入。原始样例在 `go_hive/docs/e2e-assets/observability/`。
- 范围：日志文件路径/轮转/行格式/开关（`HIVE_NO_FILE_LOG`、`HIVE_SENTINEL_LOG`、`HIVE_ENABLE_LLM_LOGS`）、`/api/runtime-logs`、看门狗主动告警。成本/延迟/队列指标不在本轮。

## 0. 结论先行

| 项 | 结果 |
|---|---|
| 同名落盘文件 | **3/3 同名**：`logs/hive-server.log`、`logs/sentinel.log`、`llm_logs/<ts>.jsonl` |
| 轮转参数 | **2/2 一致**：server 20 MiB × 3；sentinel 5,000,000 B × 3（Python `RotatingFileHandler`；Go 重实现并以测试钉住） |
| 行首时间戳 | **2/2 已对齐**：`YYYY-MM-DD HH:MM:SS`（本轮把 Go 的 `YYYY/MM/DD` 改成同一形状） |
| 开关 | 4 个实测：`HIVE_NO_FILE_LOG` / `HIVE_SENTINEL_LOG` / `HIVE_ENABLE_LLM_LOGS` 两侧行为一致；`LOG_FORMAT=json` / `ENV=production` 为 Python-only |
| `/api/runtime-logs` | 同路径、同 `boot`/`after` 语义；ring 容量同为 500。差异：级别列、stdout/stderr 分流、Go 额外有行/响应上限 |
| 看门狗 | **7/8 告警类实测触发**（health/serve/dirty/ledger/release/governance/stall），`gate` 未注入；osascript 调用有记录；故障注入发现并修掉 2 个真实缺陷 |
| 仍未对齐 | 行内级别/logger 元数据、JSON 模式、`--debug` 级别开关、ring 的 level/stream、Python 三级运行日志（`tool_logs.jsonl` 等） |

本轮共做 **15 次 serve 冷启动**（Python 6 / Go 9，其中 4 次是改动前 Go 基线）与 **6 轮看门狗注入**。

## 1. 落盘文件面（实测）

| 文件 | Python | Go | 证据/备注 |
|---|---|---|---|
| `$HIVE_HOME/logs/hive-server.log` | ✅ 默认写；RotatingFileHandler | ✅ 默认写；`RotatingFile` | 两侧默认冷启动均生成；`e2e-assets/observability/{python,go}-hive-server-default.txt` |
| `$HIVE_HOME/logs/sentinel.log` | ✅ `HIVE_SENTINEL_LOG` 非空即建；`propagate=False` | ✅ 同名同门控；文件安装后不再进主日志 | 两侧冷启动均生成；`{python,go}-sentinel-log.txt` |
| `$HIVE_HOME/llm_logs/<ts>.jsonl` | ✅ `HIVE_ENABLE_LLM_LOGS=1/true`，首个 LLM 回合打开 `<YYYYmmdd_HHMMSS>.jsonl` | ✅ 同名门控、同一命名、同一记录种类（`session_header` + `turn`） | Python 进程内探针实际写盘；Go 由 `TestLLMTurnLogJSONLThroughARealTurn` 跑真实回合 |
| `$HIVE_HOME/sessions/<sid>/logs/{summary.json,details.jsonl,tool_logs.jsonl}` | ✅ `RuntimeLogStore` 三级运行日志 | ❌ 未移植 | 静态核对：`core/framework/tracker/runtime_log_store.py`；Go `internal/` 无对应写入点 |
| `$HIVE_HOME/browserbridge.log` | — | ✅ serve 拉起 `browserbridge` sidecar 时捕获 stdout/stderr | 现存 live home 有该文件；Python 的浏览器日志是另一子系统：`~/.hive/browser-logs/browser-YYYY-MM-DD.jsonl`（工具 telemetry） |
| `~/.hive/browser-logs/browser-YYYY-MM-DD.jsonl` | ✅ 浏览器工具 telemetry（JSONL） | — | Python-only 子系统日志，不与 Go sidecar 日志同名对应 |

结论：**主日志三件套同名同门控**；Python 另有三级运行日志（不同名、未移植），Go 另有 sidecar 日志（不同子系统）。

## 2. 行格式（实测样例）

### 2.1 serve 主日志

Python 默认（human；级别列宽 8；**ANSI 颜色会写进文件本身**）：

```text
2026-11-21 15:28:04 ^[[32m[INFO    ]^[[0m Generated and persisted HIVE_CREDENTIAL_KEY to ~/.hive/secrets/credential_key
```

Python `LOG_FORMAT=json`（`ENV=production` 同形）：

```json
{"timestamp": "2026-11-21T07:28:10.731122+00:00", "level": "info", "logger": "framework.server.app", "message": "Generated and persisted HIVE_CREDENTIAL_KEY to ~/.hive/secrets/credential_key", "asctime": "2026-11-21 15:28:10"}
```

Go（本轮对齐后，默认；见 §7）：

```text
2026-11-21 15:34:18 [sentinel] manager starting (0 open escalations)
2026-11-21 15:34:18 [access] GET /api/health -> 200 (0ms)
```

对齐口径：Go 用 `NewTimestampWriter` 给每条记录加 `2006-01-02 15:04:05 ` 前缀，并 `log.SetFlags(0)` 去掉标准库默认的 `2006/01/02` 前缀。**级别列与 logger 名未对齐**：Go 的进程日志是标准库单流，`log.Printf` 不携带级别/模块元数据；强行补 `[INFO]` 会把 warning/error 行伪造成 info（理由见 §9）。

### 2.2 sentinel.log

Python（`%(asctime)s %(levelname)-7s %(name)s | %(message)s`）：

```text
2026-11-21 15:28:23,096 INFO    framework.sentinel.manager | [sentinel] manager starting (0 open escalations)
```

Go（本轮对齐后；只有时间戳，无级别/模块列）：

```text
2026-11-21 15:34:18 [sentinel] manager starting (0 open escalations)
```

### 2.3 LLM 调试 JSONL

- 记录种类与字段两侧一致：`_kind=session_header|turn`、`timestamp`、`execution_id`、`node_id`、`stream_id`、`header_hash`、`message_hashes`、`new_messages` 等。
- 本轮把 Go 时间戳改为 **UTC**（`time.Now().UTC().Format(time.RFC3339)`），与 Python 的 `datetime.now(UTC).isoformat()` 同时间轴；表示差异保留：Python 微秒 + `+00:00`，Go 秒级 + `Z`。
- 哈希仅用于文件内的增量去重：Python 用 `json.dumps(..., sort_keys=True, default=str)`（带空格），Go 用紧凑 `json.Marshal`；**同一 payload 的 hash 不可跨运行时比对**，各自文件内部自洽。

## 3. 开关矩阵（实测行为）

| 开关 | Python | Go | 判定 |
|---|---|---|---|
| `HIVE_NO_FILE_LOG`（非空即关） | 设 `1`：不建 `logs/`、不写 `hive-server.log`；控制台/ring 正常 | 同规则；设 `1` 不建 `logs/`；stderr/ring 正常 | ✅ 对齐（两侧实测） |
| `HIVE_SENTINEL_LOG`（非空即开） | 装 `logs/sentinel.log`（5 MB × 3），sentinel 行不再进主日志 | 同门控、同文件、同“移出主日志”语义；“diverting” 提示行留在主日志（Python 写在 sentinel 文件内） | ✅ 行为对齐；行内容差异见 §2.2 |
| `HIVE_ENABLE_LLM_LOGS`（仅 `1`/`true`） | 首个 LLM 回合建 `llm_logs/<ts>.jsonl`；`PYTEST_CURRENT_TEST` 下跳过 | 同门控、同路径/命名；无 pytest 守卫 | ✅ 对齐（Python 进程内探针 / Go 真实回合测试） |
| `LOG_FORMAT=json`、`ENV=production` | 主日志/控制台切 JSON（`StructuredFormatter`） | 未读取；实测设了 `LOG_FORMAT=json` 仍是 human 行 | ❌ Python-only（见 §9） |
| `--debug`（CLI） | `configure_logging(level="DEBUG")`，root 级别降为 DEBUG | 接受 flag，但无级别系统/无消费点（`--verbose` 两侧都无效果） | ❌ 未对齐（见 §9） |
| `LITELLM_LOG` | 控制 LiteLLM logger 级别 | 无 LiteLLM 依赖 | 不适用 |

## 4. `/api/runtime-logs`

同路径（`GET /api/runtime-logs`）、同响应外形 `{"boot", "entries":[{seq,stream,text}]}`、同 `boot` 失效回放语义；ring 容量都是 500。

| 维度 | Python | Go |
|---|---|---|
| text 格式 | `%(asctime)s [%(levelname)s] %(message)s`（无 logger 名、级别不补空格） | 进程日志原行（现为 `YYYY-MM-DD HH:MM:SS` + 消息） |
| level | 行内 `[INFO]/[WARNING]` | 无（标准库单流） |
| `stream` | `WARNING+` → `stderr`，其余 `stdout` | 一律 `stdout`（`WriteStderr` 有实现但宿主未分流） |
| 上限 | 无显式上限（deque 500 条） | 单行 4000 runes、整个响应 512 KiB，超限从旧端丢 |
| 轮询抑制 | `/api/runtime-logs` 自身访问不记 access 日志（除 4xx/slow） | 同（`quiet` + `status<400` + 非 slow 不记） |

样例见 `e2e-assets/observability/runtime-logs-ring.txt`。

## 5. 轮转

| 文件 | Python | Go | 实测 |
|---|---|---|---|
| `hive-server.log` | `maxBytes=20*1024*1024`, `backupCount=3` | 同值（`serverLogMaxBytes`/`serverLogBackups`） | Python：21 MB 探针后 `hive-server.log.1=20,970,945 B`、base 942,262 B；Go：`TestRotatingFileShiftsBackups` + `TestServerLogSizesMatchUpstreamRotation` |
| `sentinel.log` | `maxBytes=5_000_000`, `backupCount=3` | 同值 | Go `TestSentinelLogSizesMatchUpstreamRotation`；Python 用 stdlib handler 同参数 |

## 6. 看门狗主动告警（故障注入）

脚本：`go_hive/scripts/watchdog_report.sh`。夹具仓库 + 隔离状态/报告文件，注入：

- serve 端口无监听 → `health`、`serve`；
- `go_hive/` 下 3 个 tracked 修改 → `dirty`；
- `TIME_EVENTS.csv` 最后一行 26+ 分钟前 → `ledger`；
- 无 `cadence.json` → `release`；`governance.json` mtime 回拨 7h → `governance`；状态里预置 50 分钟前的 `last_head_at` → `stall`。

观测（完整记录：`e2e-assets/observability/watchdog-fault-injection.txt`）：

```text
[watchdog 2026-11-21 15:37:47] health=000 HEAD=b03c41e dirty_hive=3 ... serve=0 ledger_age=1720s release="none" gov_age=34667s gate="no-gate-log"
⚠️ ALERT [health] health=000（期望 200，端口 18998）
⚠️ ALERT [serve] serve 进程（hive-nonexistent-wd-probe）为 0
⚠️ ALERT [release] 2 小时 slot 版未按时更新（none）；...
⚠️ ALERT [governance] 治理重算超过 6h40m 未更新（34667s）；...
⚠️ ALERT [dirty] go_hive 区有 3 个未提交改动（total=3）...
⚠️ ALERT [ledger] 时间台账超过 28 分钟未更新...
⚠️ ALERT [stall] HEAD 已 50 分钟未变（worktrees=1），可能停滞
```

- **7/8 告警类实测触发**（health / serve / dirty / ledger / release / governance / stall），每类都调用 `osascript -e 'display notification ...'`（stub 记录 7 次调用；用真实 `/usr/bin/osascript` 重跑一轮，4 条告警、退出码 0）。
- 同状态文件立即重跑 → **0 条 ALERT**，15 分钟去抖成立。
- **`gate` 未注入**：它读共享的 `/tmp/[knmprwstx]*_targets.log`，生产看门狗实例同时消费该 glob；放一个 FAIL 文件会向真实监控发假告警。留作唯一未验证告警类。
- 注入过程中发现并修复的**真实缺陷**：
  1. `release` 告警行 `$release_line（` 中的全角括号被并进变量名，`set -u` 下 `unbound variable` 直接杀掉看门狗（release 告警永远发不出，且循环终止）。改为 `${release_line}`。
  2. `health=$(curl ... || echo 000)`：连接失败时 curl 仍打印 `000`，再追加一次 → `health=000000`。改为单次取值 + 空值兜底。
- 为可测试性新增 `WATCHDOG_REPO / WATCHDOG_REPORT / WATCHDOG_STATE / WATCHDOG_PORT / WATCHDOG_SERVE_PATTERN / WATCHDOG_ONCE` 注入开关，**默认值即生产口径**。

## 7. Go 实现变更（本轮）

| 文件 | 变更 |
|---|---|
| `internal/logging/logging.go` | 新增 `NewTimestampWriter`：每条 log 记录前加 `2006-01-02 15:04:05 `（对齐 Python human 行首）；包注释写明与 Python 的差异边界 |
| `internal/cli/loader_soul_probe.go` | `installRuntimeLogBuffer` 改为 `log.SetFlags(0)` + `NewTimestampWriter(io.MultiWriter(stderr, ring, file))`，时间戳只打一次 |
| `internal/cli/loader.go` | 把 `installRuntimeLogBuffer` 前移到 storage 迁移之前（对齐 Python 在 `create_app` 前 `configure_logging`），迁移期日志也进文件/ring |
| `internal/sentinel/logging.go` | `sentinel.log` 的 logger 改用同一 `NewTimestampWriter`（`log.New(w, "", 0)`） |
| `internal/agentloop/llm_debug_log.go` | JSONL `timestamp` 改 UTC RFC3339，与 Python `datetime.now(UTC)` 同时间轴 |
| `internal/logging/logging_test.go` | 新增 `TestTimestampWriterUsesUpstreamDateShape`、`TestServerLogSizesMatchUpstreamRotation` |
| `internal/sentinel/logging_test.go` | 新增 `TestSentinelLogSizesMatchUpstreamRotation`；`TestSentinelLogDivertsToItsFile` 加时间戳形状断言 |
| `internal/cli/env_verbatim_test.go` | `TestInstallRuntimeLogBufferWritesHiveServerLog` 加 `YYYY-MM-DD HH:MM:SS` 行形状断言；两个用例恢复 `log` flags 防串扰 |
| `internal/agentloop/env_verbatim_test.go` | `TestLLMTurnLogJSONLThroughARealTurn` 断言 header `timestamp` 以 `Z` 结尾（UTC） |
| `go_hive/scripts/watchdog_report.sh` | 注入开关 + `WATCHDOG_ONCE`；修 §6 两个缺陷 |

## 8. 复现

```bash
# Python（临时 HIVE_HOME；冷启动）
cd core && HIVE_HOME=<tmp> uv run --offline hive serve --port 18787
curl -s 127.0.0.1:18787/api/health; curl -s 127.0.0.1:18787/api/runtime-logs
ls -l <tmp>/logs; head -3 <tmp>/logs/hive-server.log | cat -v

# Python JSON 模式
LOG_FORMAT=json HIVE_HOME=<tmp> uv run --offline hive serve --port 18789

# Go
cd go_hive/go-hive && CGO_ENABLED=0 go build -o /tmp/hive-go ./cmd/hive
HIVE_HOME=<tmp> /tmp/hive-go serve --port 18788
ls -l <tmp>/logs; head -3 <tmp>/logs/hive-server.log

# 开关
HIVE_NO_FILE_LOG=1 ... ; HIVE_SENTINEL_LOG=1 ... ; HIVE_ENABLE_LLM_LOGS=1 ...

# 看门狗故障注入（夹具仓库见 e2e-assets/observability/watchdog-fault-injection.txt）
WATCHDOG_REPO=<fixture> WATCHDOG_PORT=<free> WATCHDOG_STATE=<tmp> WATCHDOG_REPORT=<tmp> \
WATCHDOG_SERVE_PATTERN=hive-nonexistent-wd-probe WATCHDOG_ONCE=1 \
  bash go_hive/scripts/watchdog_report.sh 2
```

## 9. 仍未对齐（差异与理由）

| 差异 | 理由 | 影响/下一步 |
|---|---|---|
| Go 行内无 `[LEVEL]` / logger 名 | Go 走标准库单流 `log`，没有级别/模块元数据；补假级别会让 WARNING/ERROR 伪装成 INFO | 需要结构化 logger 层（级别调用点改造），列为后续项 |
| `LOG_FORMAT=json` / `ENV=production` 未支持 | 同上：JSON 模式要写真实 `level`/`logger`，不能伪造 | 与上一条同批做；否则明确标注 Python-only |
| `--debug` 在 Go 无效果 | 无级别系统可调；flag 只为 CLI 兼容保留 | 文档/help 应写“no-op until structured levels” |
| sentinel 行缺 `LEVEL` / `framework.sentinel.*` 模块名 | Go sentinel 是单一 `sentinelLogf` 门面（74 个调用点），未逐点分级 | 可先给少数 warning 调用点加 `sentinelWarnf` |
| access 日志的级别/`stream` 分流 | 同“无级别” | 4xx/slow 目前只体现在响应码与 ` SLOW` 后缀 |
| ring 的 4000 runes / 512 KiB 上限 | Go 侧防单条巨大记录撑爆 JSON 响应；Python 无 | 有意差异；解析方需容忍截断 |
| `sessions/<sid>/logs/*` 三级运行日志（含 `tool_logs.jsonl`） | Python `RuntimeLogStore` 未移植，属独立功能面（非“格式对齐”） | 单列 backlog：summary/details/tool_logs 三级写入 |
| LLM JSONL 时间戳精度与 hash 字节 | Python 微秒+`+00:00`、Go 秒级+`Z`；hash 生成器空白/fallback 不同 | 各自文件内自洽；跨运行时只比对字段语义，不比对 hash |
| Python human 文件含 ANSI 颜色 | Python 的 `HumanReadableFormatter` 同时用于控制台与文件（strip 只发生在 ring/JSON） | Python 侧 quirk；需要无颜色文件时用 `LOG_FORMAT=json` |
| `gate` 告警类 | 注入需写共享 `/tmp` glob，会误触生产看门狗 | 保持未验证；后续可用 `WATCHDOG_GATE_GLOB` 注入点补齐 |

## 10. 相关文档

- `go_hive/docs/appearance-scope-registry.json`（维度 `observability`，`measured_now` 指向本页）
- `go_hive/docs/surface-tracking-matrix.html`（“日志 / 可观测”行）
- `go_hive/docs/environment-variables.html`（三个日志开关的环境变量契约）
- `go_hive/docs/e2e-assets/observability/`（原始样例与故障注入记录）
