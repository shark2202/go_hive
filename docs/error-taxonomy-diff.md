# 错误体系差异（errors #12；reference=python → target=go_hive）

- 生成时间：2026-11-21T18:03:25+08:00
- reference（基准）：python，HEAD `4af5b3755f9f97c1305fe574055f09533675eb7e`，二进制 `/Users/mac/hive/.venv/bin/hive`
- target（待对齐）：go_hive，HEAD `b79af538dc20b482ad477bef4066548dfb71a708`，二进制 `/Users/mac/hive/.wt/appearance-errors-formats/go_hive/go-hive/dist/hive-errfmt`
- 口径：同一夹具 HIVE_HOME 的逐字节副本（configuration.json + secrets/credential_key + agents/probe-agent + 一个 import upload）；两侧各自起 serve 后对同一请求序列逐一取回，值级比对前对 session id / 时间戳 / 路径做规范化；工具错误文案经各自 serve + 脚本化假模型网关（fake_tool_gateway.py）采集，
  比对的是模型看到的 `role: tool` 文本与循环错误标记（`ERROR: ` 前缀 = is_error）。
- 产物：`go_hive/docs/e2e-assets/errors-formats/errors-raw.json`、`errors-ledger.json`（`--check` 从 raw 重放并比对台账与本报告）。

**HTTP 失败路径矩阵：27/27 对齐（shape_diff 0，value_diff 0，已声明差异 26 条）。**
**工具错误文案：6/6 对齐（different 0，已声明差异 3 条）。**

## 本轮修复

工具错误文案探针实测：edit_file / write_file 的「未读或已过期」拒绝文案与上游逐字相同，
但 Go 通过 `fail()` 把它标成 `is_error=true`（模型看到 `ERROR: ` 前缀），上游 Python 返回纯文本结果（`tools/src/aden_tools/file_ops.py` 的 `_patch_replace` / `write_file` 直接 `return` 字符串）。
修复：`internal/tools/builtin/edit.go`（文件不存在分支 + 未读/过期两条拒绝）与 `internal/tools/builtin/write.go`（未读/过期两条拒绝）改为纯文本 `Result{Content: …}`；Go 测试同步为断言无错误标记（`file_ops_residual_test.go`、`file_ops_test.go`）。
复跑结果：修复前 3/6 场景为 is_error-only 差异、文案逐字相同；修复后 6/6 对齐。
同族残余（未纳入本轮探针，保留现状）：read_file / write_file / edit_file 的其余 `fail()` 分支（参数校验、唯一匹配、写入异常等）仍带错误标记；上游相应路径同样多为纯文本返回，逐分支核对后再收敛。

## HTTP 失败路径矩阵

| 探测 | 端点 | 期望 | reference | target | Content-Type (ref / target) | 判定 | 差异 |
|------|------|------|-----------|--------|---------------------------|------|------|
| sessions_post_nonobject | `POST /api/sessions` | 400 | 400 | 400 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| config_llm_invalid_json | `PUT /api/config/llm` | 400 | 400 | 400 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| credentials_missing_fields | `POST /api/credentials` | 400 | 400 | 400 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| sentinel_telegram_empty | `POST /api/sentinel/telegram/validate` | 400 | 400 | 400 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| skills_patch_enabled_not_bool | `PATCH /api/queen/queen_growth/skills/administrative-behavior-skill` | 400 | 400 | 400 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| agents_delete_missing_path | `DELETE /api/agents` | 400 | 400 | 400 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| import_init_missing_total | `POST /api/colonies/import/init` | 400 | 400 | 400 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| import_chunk_bad_offset_value | `PUT /api/colonies/import/chunk/0123456789abcdef?offset=abc` | 400 | 400 | 400 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| import_chunk_negative_offset | `PUT /api/colonies/import/chunk/0123456789abcdef?offset=-1` | 400 | 400 | 400 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| import_chunk_bad_upload_id | `PUT /api/colonies/import/chunk/NOPE?offset=0` | 400 | 400 | 400 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| colony_scaffold_bad_name | `POST /api/colonies/Bad-Name/scaffold` | 400 | 400 | 400 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| agents_delete_framework | `DELETE /api/agents` | 403 | 403 | 403 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| skill_put_body_framework | `PUT /api/queen/queen_growth/skills/administrative-behavior-skill/body` | 403 | 403 | 403 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| session_missing | `GET /api/sessions/session_YYYYMMDD_HHMMSS_<8hex>` | 404 | 404 | 404 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| session_tasks_missing | `GET /api/sessions/session_YYYYMMDD_HHMMSS_<8hex>/tasks` | 404 | 404 | 404 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| session_triggers_missing | `GET /api/sessions/session_YYYYMMDD_HHMMSS_<8hex>/triggers` | 404 | 404 | 404 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| skill_missing | `GET /api/skills/does-not-exist-probe` | 404 | 404 | 404 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| credentials_missing | `GET /api/credentials/nope_probe` | 404 | 404 | 404 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| queen_missing | `GET /api/queen/queen_nope/overview` | 404 | 404 | 404 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| mcp_health_missing | `POST /api/mcp/servers/nope/health` | 404 | 404 | 404 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| attachment_missing | `GET /api/sessions/session_YYYYMMDD_HHMMSS_<8hex>/attachment/nope.txt` | 404 | 404 | 404 | `text/plain; charset=utf-8` / `text/plain; charset=utf-8` | aligned | — |
| import_chunk_unknown_upload | `PUT /api/colonies/import/chunk/0123456789abcdef?offset=0` | 404 | 404 | 404 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| resolve_missed_no_colony | `POST /api/sessions/session_YYYYMMDD_HHMMSS_<8hex>/colony/resolve_missed` | 409 | 409 | 409 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| import_chunk_offset_mismatch | `PUT /api/colonies/import/chunk/9b21716da5db4e61?offset=5` | 409 | 409 | 409 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| import_finalize_incomplete | `POST /api/colonies/import/finalize/9b21716da5db4e61` | 409 | 409 | 409 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| config_llm_list_body | `PUT /api/config/llm` | 500 | 500 | 500 | `application/json; charset=utf-8` / `application/json` | aligned | — |
| agents_delete_nonstring_path | `DELETE /api/agents` | 500 | 500 | 500 | `application/json; charset=utf-8` / `application/json` | aligned | — |

## 错误体词法（按 reference 归组）

| 错误体形状 | 探测 |
|------------|------|
| `json: {code, error, fix, what, why}` | 404 mcp_health_missing |
| `json: {declared, error, received}` | 409 import_chunk_offset_mismatch |
| `json: {error, ok}` | 400 sentinel_telegram_empty |
| `json: {error, received, total}` | 409 import_finalize_incomplete |
| `json: {error, session_id, tasks}` | 404 session_tasks_missing |
| `json: {error}` | 400 sessions_post_nonobject, 400 config_llm_invalid_json, 400 credentials_missing_fields, 400 skills_patch_enabled_not_bool, 400 agents_delete_missing_path, 400 import_init_missing_total, 400 import_chunk_bad_offset_value, 400 import_chunk_negative_offset, 400 import_chunk_bad_upload_id, 400 colony_scaffold_bad_name, 403 agents_delete_framework, 403 skill_put_body_framework, 404 session_missing, 404 session_triggers_missing, 404 skill_missing, 404 credentials_missing, 404 queen_missing, 404 import_chunk_unknown_upload, 409 resolve_missed_no_colony, 500 config_llm_list_body, 500 agents_delete_nonstring_path |
| `text` | 404 attachment_missing |

## 工具错误文案（模型可见的 tool 结果文本）

| 场景 | 工具 | 比较口径 | 判定 | 说明 |
|------|------|----------|------|------|
| read_file 不存在 | `read_file` | text+is_error | aligned | — |
| edit_file 不匹配（未读守卫） | `edit_file` | text+is_error | aligned | — |
| edit_file 不匹配（文件不存在） | `edit_file` | text+is_error | aligned | — |
| write_file 覆盖守卫（同类失败路径） | `write_file` | text+is_error | aligned | — |
| terminal 非零退出 | `terminal_exec` | exit_code+stderr | aligned | envelope-form; is_error |
| connect 未授权（凭据未连接） | `image_generate` | error_message+keys | aligned | extra-key |

### 探针原文（reference / target）

| 场景 | python（is_error） | go_hive（is_error） |
|------|--------------------|---------------------|
| read_file_missing | `Error: File not found: /private/tmp/hive-errfmt-probe/ws/missing.txt` (False) | `Error: File not found: /private/tmp/hive-errfmt-probe/ws/missing.txt` (False) |
| edit_file_mismatch | `Refusing to edit '/private/tmp/hive-errfmt-probe/ws/probe.txt': call read_file('/private/tmp/hive-errfmt-probe/ws/probe.txt') first so the harness can track its state before you edit it.` (False) | `Refusing to edit '/private/tmp/hive-errfmt-probe/ws/probe.txt': call read_file('/private/tmp/hive-errfmt-probe/ws/probe.txt') first so the harness can track its state before you edit it.` (False) |
| edit_file_missing | `Error: File not found: /private/tmp/hive-errfmt-probe/ws/missing.txt` (False) | `Error: File not found: /private/tmp/hive-errfmt-probe/ws/missing.txt` (False) |
| write_file_refusal | `Refusing to overwrite '/private/tmp/hive-errfmt-probe/ws/probe.txt': call read_file('/private/tmp/hive-errfmt-probe/ws/probe.txt') first so the harness can track its state before you replace it. If you intend to discard ` (False) | `Refusing to overwrite '/private/tmp/hive-errfmt-probe/ws/probe.txt': call read_file('/private/tmp/hive-errfmt-probe/ws/probe.txt') first so the harness can track its state before you replace it. If you intend to discard ` (False) |
| terminal_exec_nonzero | `{"exit_code": 7, "stdout": "out\n", "stderr": "err\n", "stdout_truncated_bytes": 0, "stderr_truncated_bytes": 0, "runtime_ms": 7, "pid": 89493, "output_handle": null, "timed_out": false, "semantic_status": "error", "sema` (False) | `exit_code: 7<br>auto_shell: true<br>shell_kind: bash<br>stdout:<br>out<br>stderr:<br>err` (True) |
| image_generate_unauthorized | `{"error": "Image generation unavailable: missing Hive proxy token. Sign in to refresh credentials, then retry."}` (False) | `{"error": "Image generation unavailable: missing Hive proxy token. Sign in to refresh credentials, then retry.", "status": 503}` (False) |

## 集成工具 401 文案（静态对照）

上游 `tools/src/aden_tools/tools/` 与 Go `internal/tools/integrations/`（生产文件）中的 `Unauthorized…` 字面量：共有 **23** 条、仅上游 0 条、仅 Go 0 条。运行时只有凭据门控的 harness 工具（image_generate）可离线实测；集成模块需真实厂商调用，本表单列静态口径。

## CLI 退出码（tracker / hive-cred）

两侧同源逐值一致：`core/framework/trackerdb/errors.py`（`EXIT_OK..EXIT_BUSY`）与 `go_hive/go-hive/internal/tracker/errors.go`（`ExitOK..ExitBusy`）；错误体字段同为 `{code, message, details}`，`run_cli` 以 `e.exit_code` 退出并打印 envelope。

| 退出码 | 常量 | 语义 |
|--------|------|------|
| 0 | EXIT_OK | 成功 |
| 1 | EXIT_DOMAIN | sqlite 错误或未分类域失败 |
| 2 | EXIT_NO_COLONY | 不在 colony 上下文 |
| 3 | EXIT_PERMISSION | worker 调用 queen-only 命令 |
| 4 | EXIT_NOT_FOUND | 表/列不存在 |
| 5 | EXIT_NOT_REGISTERED | 表存在但未被白名单放行写 |
| 6 | EXIT_VALIDATION | 参数/JSON/CSV 非法 |
| 7 | EXIT_DENIED | SQL 被 tracker 拒绝清单拦下 |
| 9 | EXIT_PARTIAL | 部分行写入、部分被拒（看 `failed`） |
| 10 | EXIT_BUSY | 其他写者持有 DB，重试 |

browser CLI 的退出码分类另有 `TestBrowserCLIExitCodes` 固定（跨维度引用，不重复展开）。

## 保留差异（declared，逐条量化）

| 编号 | 主题 | 实测 | 处理 |
|------|------|------|------|
| E-01 | JSON Content-Type 的 charset 参数 | reference `application/json; charset=utf-8` vs target `application/json`（26 条探测均同） | 保留：HTTP 规范允许两种；改 Go 侧 `writeJSON` 会触及全部路由与已冻结契约 |
| E-02 | terminal 非零退出的信封与错误标记 | reference：JSON envelope（`semantic_status=error`，无 `ERROR: ` 前缀，is_error=false）；target：文本报告 + `ERROR: `（is_error=true）；`exit_code` 两侧同为 7 | 保留：terminal 输出形态是 terminal lane 的冻结契约（tool-output golden 已记录 envelope 差异），本页只保证 exit_code 语义相等 |
| E-03 | `image_generate` 凭据缺失错误的额外键 | 两侧 error 文案逐字相同；target 多 `status: 503` | 保留：Go 附加字段（additive），不影响上游键集 |
| E-04 | 框架 Agent 保护的符号链接路径 | HIVE_HOME 经符号链接（`/tmp`→`/private/tmp`）时：reference 解析后与未解析的 `$HIVE_HOME/agents` 比较，保护失效（实测 200 并删除）；target 解析两侧后 403 | 保留：Go 的有意加固（见 `internal/server/routes_agents.go` 注释）；非符号链接夹具下两侧同为 403 |
| E-05 | 文件工具其余 `fail()` 分支的 is_error | 静态对照：上游同族返回纯文本；本页 5 个分支已修，其余未纳入探针 | 保留待逐分支核对（见「本轮修复」末段） |

## 复现

```bash
cd go_hive/go-hive && CGO_ENABLED=0 go build -o dist/hive-errfmt ./cmd/hive
uv run --no-project python go_hive/scripts/errors_formats_probe.py errors          # 双跑 + 采集
uv run --no-project python go_hive/scripts/errors_formats_probe.py errors --check  # 从 raw 重放台账与报告
```

夹具：同一 HIVE_HOME 模板的逐字节副本（`configuration.json` + `secrets/credential_key` + `agents/probe-agent`）；
两侧各自起真实 serve（python 用 `/Users/mac/hive/.venv/bin/hive`，target 用本树构建的二进制），
工具文案改由 `fake_tool_gateway.py` 脚本化驱动（read_file 缺失 / edit_file 未读拒绝 / edit_file 文件缺失 / write_file 覆盖拒绝 / terminal 非零退出 / image_generate 缺凭据）。

