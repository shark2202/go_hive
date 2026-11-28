# Tool output contract — canonical golden (Python reference vs go_hive)

Status: 输出 golden 第二轮（2026-11-21 扩场景）。Tools were compared by
**name / description / input schema** before this lane; their **results**
(success and failure envelopes) were not. This lane adds the missing half: the
same inputs are run on both runtimes and the model-visible tool-result text is
compared after canonicalisation — now **69 场景**（首批 38 + 扩场景 31），
**50 aligned / 1 shape-aligned / 18 recorded / 0 未归因**，`terminal_exec_ok`
例外已在第二轮消除（Go 改为 Python 同形 JSON 信封）。

Evidence: `docs/e2e-assets/tool-output/` (`scenarios.json`, `results.json`,
`diff.json`, `summary.txt`, `raw/`).
Reproduce: `go_hive/scripts/capture_tool_output.py` +
`go_hive/scripts/tool_output_golden.py` (§6).
Guarded by: `internal/toolgolden` (canonical rules),
`internal/tools/integrations/tool_output_golden_test.go`,
`internal/tools/builtin/tool_output_golden_test.go`,
`internal/cli/task_tools_test.go`.

## 1. 口径

- **抓取层 = 模型看到的工具结果**。两侧各自 `hive serve`，前面挂同一个脚本化假
  OpenAI 网关（`go_hive/scripts/fake_tool_gateway.py`）。假网关按顺序返回固定的
  tool_call 序列；每个后续请求的 `messages` 里带着上一步的 `role: tool` 消息，
  那就是契约文本（含循环加的 `ERROR: ` 标记）。会话是默认 `queen_growth`，
  `HIVE_HOME` 是一次性目录，除假网关外不访问网络。
- **同一输入**：`scenarios.json` 的每个场景两侧用完全相同的 tool 名与
  arguments（夹具文件在同一个 workspace 里生成）。两侧的 workspace 路径相同，
  只有 `HIVE_HOME` 不同（已被规则占位化）。
- **两侧 commit**：Python `hive 1.0.14`（`/Users/mac/hive` 参考 checkout）／
  Go 本分支 `go_hive/go-hive`（`make build`，CGO_ENABLED=0）。
- 本文件只报告与冻结；不进入 CI 门禁。`internal/toolgolden` 与
  `go_hive/scripts/tool_output_golden.py` 必须保持规则一致（§2）。

## 2. Canonical 规则

两侧进程各自产生 session id、时钟读数、绝对路径和 pid，逐字节对比无意义。
规则只替换「不可能跨进程相同」的值，不隐藏形状、键集、类型或文案差异。
实现：`go_hive/scripts/tool_output_golden.py`（抓取侧）与
`go_hive/go-hive/internal/toolgolden/canonical.go`（测试侧）。

| 规则 | 处理 |
| --- | --- |
| `is_error` 标记 | 开头的 `ERROR: ` 记录为 `is_error: true` 并从正文剥离；Python 的 `ERROR: ERROR: …` 折叠为一次 |
| 结尾 `<system-reminder>` | 剥离；只记录 `reminder` 标志（循环注入的任务提醒，不属于工具契约，因此**不参与比对**） |
| JSON 正文 | 按 JSON 解析（保序键集无关），键排序后比对；数字保留字面量（`json.Number`） |
| JSON + 尾随文本块 | `attach_file` 形如 `<json>\n[attached …]\n\n<body>`：JSON 部分按上一条比对，尾随块单独比对（`trailer`） |
| 纯文本正文 | 逐字比对，只做下列占位化 |
| 运行时值（按键） | `session_id`/`datetime`/`date`/`time`/`day_of_week`/`unix_timestamp`/`created_at`/`updated_at`/`resolved`/`pid`/`runtime_ms`/`output_handle`/`job_id` → 占位符 |
| 运行时建议文本（按键） | `next_step` / `terminal_hint` → `<ADVISORY>`：Python 指向 pdfplumber/poppler，Go 指向 qpdf；键集与周围载荷才是契约 |
| 文本内模式 | `session_…`、`call_…`、ISO 时间戳、日期、`HH:MM:SS`、10 位 unix 秒、`returned N characters` 的 N |
| 路径 | 抓取布局下的 `/tmp/<scratch>/{python,go}-home` → `<HOME>`、`/tmp/<scratch>/ws` → `<WS>`；macOS 的 `/private/tmp` 归一为 `/tmp`；测试用 `Options.WithPath` 传自己的 workspace |

比对模式（写在 `scenarios.json`）：

| 模式 | 含义 | 数量 |
| --- | --- | --- |
| `exact` | 规范化后逐字一致 | 14 |
| `masked` | 有占位化仍逐字一致 | 11 |
| `shape` | 只比信封形状（form / is_error / 键集 / 类型），值由各自工具库决定 | 1 |
| `none` | 记录两侧信封但不比对（已复核的例外，§4） | 12 |

## 3. 覆盖（首批 38 场景；扩场景后总计 69，见 §5 与 §9）

场景选择：默认工具面里能在临时 `HIVE_HOME` 离线执行的工具（`search_tools`、`task_*`、
`get_current_time`、`read_file`、`attach_file`、`pdf_read`、`search_messages`、
`terminal_exec`、`skill_view`），加上两侧都能构造的失败路径（缺文件、参数错、
路径越界、未加载工具、未知 action、未移植工具）。

`docs/e2e-assets/tool-output/results.json` 是机器可读版，`summary.txt` 是人读版。
结论：**25 逐字一致 + 1 形状一致 + 12 记录例外，0 未归因差异**。

按形状覆盖：

| 形状 | 场景 |
| --- | --- |
| JSON 成功信封（普通） | `search_tools_load` / `_keyword` / `_already_loaded`、`get_account_info` 同类 `get_current_time_*`、`task_create_*`、`task_list_*`、`task_get_*`、`task_update_completed`、`attach_file_*`、`pdf_read_ok*`、`search_messages_none` |
| JSON 错误信封（参数/业务错误，非 is_error） | `get_current_time_bad_tz`、`pdf_read_missing`、`pdf_read_both_args`、`pdf_read_bad_pages`、`task_get_missing_numeric`、`task_create_missing_goal` |
| JSON 错误信封（is_error） | `task_update_bad_status`、`task_get_bad_id`、`unloaded_tool_call`、`skill_view_missing`、四个 `not_implemented` |
| 纯文本成功（带行号） | `read_file_ok` |
| 纯文本成功（目录列表） | `read_file_dir` |
| 纯文本拒绝（is_error=false） | `read_file_missing`、`read_file_escape` |
| JSON + 尾随文本块 | `attach_file_ok` |
| 富 JSON（页/元数据/建议） | `pdf_read_ok`、`pdf_read_ok_multi` |
| 目录/清单信封 | `search_tools_inventory`（形状）、`search_tools_load_read_file` |
| 超大结果溢出信封 | `search_messages_spill` |
| 文本报告信封 | `terminal_exec_ok` |

## 4. 本轮修掉的差异

全部为 Go 侧改动，Python 是基准；每项都有测试或抓取证据。

| # | 差异 | 处理 | 位置 |
| --- | --- | --- | --- |
| 1 | `get_current_time.datetime` Go 输出 `2026-11-21T03:15:18Z`（`time.RFC3339`，无小数秒），Python `isoformat()` 输出 `…T03:15:18.717088+00:00` | Go 改为 isoformat 形状（微秒非零时保留小数、UTC 用 `+00:00` 而非 `Z`） | `internal/tools/integrations/local_time_csv.go` |
| 2 | `PythonJSONDumps` 用 Go 的 `'g'` 格式化 float：`created_at` 输出 `1.795230196937622e+09`，Python `json.dumps` 输出 `1795230842.954646` | 新增 `tools.FormatPyFloat`（Python `repr(float)` 语义），`PythonJSONDumps` 与 `nativecli` 共用一份实现 | `internal/tools/pyjson.go`、`mcp.go`、`internal/mcpregistry/nativecli/pyjson.go` |
| 3 | `read_file` 文件系统失败被标成 is_error 且文案自带前缀：`ERROR: read_file: file not found: X`；目录被当错误拒绝 | 对齐 `file_ops.py`：`Error: File not found: X` / `Error: <policy>` 为**普通文本**（is_error=false）；新增目录列表分支 `Directory: X (N entries)`（≤200 条、目录带 `/`） | `internal/tools/builtin/read.go`、`builtin.go` |
| 4 | `attach_file` 文本条目多出 Go 专有键 `inline` / `inline_preview`（且成功路径上还有 `content_blocks` / `note`） | 与上游同形：条目只保留 `path/resolved/kind/mime/bytes/filename`，文本预览改为 JSON 之后的尾随文本块 `[attached <name> (<mime>)]\n\n<body>`；`hive_attachment_url` 仍由发布器注入；发布器与 chip 采集器改为解析「首个 JSON 文档 + 保留其余」；仅超限（未作为内容块发送）时才保留 Go 专有 `inline_error` / `note` | `internal/tools/integrations/attach_file.go`、`internal/agentloop/conversation.go` |
| 5 | `task_update` 接受任意 status（`bogus` 直接写进任务） | 按 Python 的 StrEnum 校验，文案逐字复用：`'bogus' is not a valid TaskStatus`，并以 is_error 信封返回 | `internal/cli/task_tools.go`、`internal/tasks/models.go` |
| 6 | `task_get` / `task_update` 把非整数 id 静默强制成 0（回一句 “Task #0 not found.”） | 改为参数错误信封 `{"error": "invalid task id: 'no-such-task' (expected an integer)"}` + is_error（文案差异见 §5） | `internal/cli/task_tools.go` |

### 4.1 2026-11-21 扩场景波次新增修复

| # | 差异 | 处理 |
| --- | --- | --- |
| 7 | `terminal_exec` 输出信封：Go 纯文本报告 vs Python JSON | Go 改为 Python `build_exec_envelope` 同形 JSON（16 键；失败路径 17 键含 `error`），`terminal_exec_golden_test.go` 回放 8 场景 |
| 8 | 非零退出/timeout 曾带 `"ERROR: "` 前缀 | 去除，与 Python 一致（语义看 `exit_code`/`semantic_status`） |
| 9 | `terminal_job_*`/`terminal_pty_*` body 错误曾标 `is_error` | 改为上游语义：job/pty 结果体的错误走普通 Result |
| 10 | `write_file` 连续写入新鲜度（macOS `/var` 规范化致误判 stale） | `TestWriteFileChainedWritesStayFresh` 钉住；`read_file` 目录列表测试改为夹具驱动 |

## 5. 例外清单（18 条，全部已归因）

记录在 `scenarios.json` 的 `exception` 字段与 `diff.json` 的 `exceptions`。

| 场景 | 类别 | 原因 |
| --- | --- | --- |
| `search_tools_inventory` | 工具集 | the inventory lists each runtime's own tool library (Python's allowlist disables the file ops and ships senders; Go defers 51 flat names), so only the envelope keys/types are contract. |
| `search_tools_load_read_file` | 工具集 | Python reports "Nothing to load" (read_file/skill_view are outside its allowlist), Go promotes both. Recorded, not compared — the branches differ by tool library. |
| `task_get_bad_id` | 例外 | Python converts the id with int() and lets the CPython ValueError escape ("invalid literal for int()"), Go answers with a typed argument error. Both are is_error JSON envelopes with an error key. |
| `read_file_escape` | 例外 | Python resolves the path against its session dir and reports a misleading "File not found"; Go refuses the escape explicitly. Same outcome (no bytes, plain-text refusal, is_error=false). |
| `search_messages_spill` | 例外 | an oversized result spills on both sides, but Python emits a structural digest + grep hint and Go emits a bounded preview with a sampling warning. Reviewed and kept: different spillover designs, each self-consistent. |
| `skill_view_missing` | 工具集 | skill_view exists only on Go (Python answers "Unknown tool"). Both are is_error JSON envelopes. |
| `credentials_list` | 未移植 | credentials is a Python-only model tool; Go answers with the explicit not_implemented envelope. |
| `credentials_usage` | 未移植 | see credentials_list. |
| `sentinel_setup` | 未移植 → 已移植（记录为抓取时状态） | 抓取时点 Go 无执行器，返回 not_implemented 信封（见 `e2e-assets/tool-output/`，未重抓）。2026-11-22 sentinel-setup lane 已实装（`internal/sentinel/sentinel_tool.go` + `internal/cli/sentinel_setup_tool.go`），help 文案与 Python 记录逐字一致（`SentinelSetupHelpMatchesRecordedPythonPayload` 钉住）。 |
| `collect_result` | 已移植（2026-11-25） | The agent loop runs `LoopConfig.BackgroundTools` asynchronously and answers `collect_result` from its `bg_*` handle registry (waits for the work, `{"status":"pending"}` on a new message). The catalogue-level envelope is only reachable without a loop in the path and says so. |
| `suggest_colony` | 未移植 | suggest_colony drives the colony popup; Go answers with the explicit not_implemented envelope. |
| `unloaded_tool_call` | 工具集 | excel_read does not exist in Python ("Unknown tool ... Did you mean 'csv_read'?"); on Go it is a deferred name and the loop refuses it with the select: prompt. Both are actionable is_error envelopes. |
| `write_file_escape` | 例外 | the Python file tools resolve any path the process can reach (their only ceiling is a credential deny list), while the Go builtin toolbox is rooted at the session workspace and refuses escapes. Both answer plain text (is_error=false); the semantics differ by design. |
| `pty_run_ok` | Python harness 注入口径 | the live loop injects its execution-context session_id over any tool argument named session_id (framework/loader/tool_registry.py::_inject_context), so terminal_pty_run always sees the hive session id and answers 'unknown session_id'. The Go port has no session_id context param, so it runs the command ({"output":"pty-hello\r\n",...}). |
| `pty_close` | Python harness 注入口径 | same session_id injection as pty_run_ok — terminal_pty_close cannot address the opened session and answers 'unknown session_id'; Go closes it and returns exit_code/final_output. |
| `pty_run_unknown` | Python harness 注入口径 | the injected hive session_id replaces the bogus literal, so the error cites session_<ID> instead of pty_doesnotexist. Go echoes the argument it received. |
| `http_headers_ok` | Python harness 注入口径 | http_headers_scan is not registered in the queen chat surface (the capture session answers 'Unknown tool: http_headers_scan'); the tool module itself is compared against this Go result by raw/network-direct.json (direct invocation over the same fixture server). |
| `http_headers_bare` | Python harness 注入口径 | http_headers_scan is not registered in the queen chat surface (the capture session answers 'Unknown tool: http_headers_scan'); the tool module itself is compared against this Go result by raw/network-direct.json (direct invocation over the same fixture server). |

## 6. 复现

```sh
# 1) 抓取（两侧各起一次 serve；约 1 分钟）
uv run --no-project python go_hive/scripts/capture_tool_output.py --side both \
    --scenarios go_hive/docs/e2e-assets/tool-output/scenarios.json \
    --out go_hive/docs/e2e-assets/tool-output/raw \
    --scratch /tmp/tool-output-capture \
    --python-root /Users/mac/hive --python-bin /Users/mac/hive/.venv/bin/hive

# 2) 规范化 + 对比（写 results.json / diff.json / summary.txt）
uv run --no-project python go_hive/scripts/tool_output_golden.py report

# 3) 校验产物没过期（读已提交的 raw/，不改文件）
uv run --no-project python go_hive/scripts/tool_output_golden.py report --check

# 4) Go 侧守门测试（无网络）
(cd go_hive/go-hive && CGO_ENABLED=0 GOPROXY=off go test ./internal/toolgolden ./internal/tools/builtin ./internal/tools/integrations ./internal/cli -run 'Golden|Canonical|Validate' -v)
```

抓取会把完整请求转写写在 `--scratch`（含系统提示，不进仓库）；仓库里只保留
抽取出的 `raw/<side>-results.json`、场景脚本与 server 日志。

## 7. Go 侧测试

| 测试 | 覆盖 |
| --- | --- |
| `internal/toolgolden/canonical_test.go` | 规范化规则：is_error 标记（含 Python 双前缀折叠）、JSON/文本形状、尾随块、reminder 记录、按键遮罩、文本遮罩、路径遮罩、golden payload 可重建 |
| `internal/tools/integrations/tool_output_golden_test.go` | 8 个抓取场景重放：`get_current_time`（UTC/Kolkata/坏时区）、`pdf_read`（缺文件/双参数/坏 pages）、`attach_file`（缺文件/成功含发布） |
| `internal/tools/builtin/tool_output_golden_test.go` | `read_file` 成功/缺文件/目录三种形状 + 目录列表格式 |
| `internal/cli/task_tools_test.go` | 参数校验（坏 status/非整数 id/小数 id）与两个错误信封的 golden 对照 |

## 8. 已知边界

- 只覆盖**默认 queen 会话能离线执行**的工具；集成模块（需要凭据/网络）、
  browser/vision/image 工具未覆盖。
- Python 侧首个请求之后的 `search_tools` 装载差异使「未装载工具」这条路不可比
  （§5：两侧工具库不同）。
- **auto-shell 检测**：Go 把「贴附在词上的」元字符（如 `echo x >&2`）也路由到
  bash，Python 只识别独立 token 元字符；场景改用两侧同为 direct exec 的输入。
- **pty / http_headers**：Python harness 会把 execution-context `session_id`
  注入覆盖同名工具参数、且 `http_headers_scan` 不在 queen 会话工具面，导致
  这几条只能单侧记录（Go 侧另有 network_probe.py 直调对照）。
- **`web_search`**：需 provider key，未验证。
