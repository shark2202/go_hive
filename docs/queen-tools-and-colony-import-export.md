# Queen 的工具面 · Colony 的导入导出

> 可复用操作手册（HTML + JSON，含 curl/分片上传/限额/错误）：[colony-import-export.html](colony-import-export.html) · 生成器 `scripts/gen_colony_import_export_doc.py` · `--check` 防漂移。

结论文档。**未改动任何代码。** 每条判定附 `file:line`，并在本机真实运行实例（`127.0.0.1:8787`）上取了一份运行时快照做交叉验证。

对照基线：Python `core/framework` v1.0.15（`contracts/api-routes.json` 记 `reference_id=python@84569fd`，Go 侧 `target_id` 同 commit）。

---

## 第一部分：Queen 在运行时到底有哪些 tool

### 0. 一句话结论

工具面是**三层叠加 + 一道相位闸**：

1. **底座**：进程级的 integration registry（terminal / files / chart / hive_tools / 各 MCP server）；
2. **会话级生命周期表**：task / skill / queen-mail / queen-lifecycle 四组，挂在 `sessionToolCatalogue` 上；
3. **相位清单**：`queenIndependentPhaseTools`（9 个）与 `queenColonyPhaseTools`（23 个），只用来做**裁剪与准入**，不是完整列表；
4. **闸门**：`ChatRuntime.toolsForTurn()` 在**绑定 colony 时**换用 colony 相位，未绑定时剔除 colony-only 的 def。

所以「Queen 有哪些 tool」必须分相位答，DM 和 colony 是两套不同的面。

### 1. 运行时实测（最硬的证据）

```
GET http://127.0.0.1:8787/api/sessions/session_20261124_173727_ad14a20f/live_tools   → 200
```

| 字段 | 值 | 含义 |
|---|---|---|
| `phase` | `independent` | 当前是 DM 相位 |
| `phase_state_ready` | `true` | 相位态可用，`actual_tools` 可信 |
| `actual_tools` | **21** | 本轮**字面可调用**的集合 |
| `expected_tools` | **61** | 已配置/允许的完整面（含懒加载） |
| `framework_added` | `["ask_user"]` | 框架在 dispatch 时补的合成工具 |
| `mcp_tool_count_registered` | `54` | 注册进 registry 的 MCP 名数 |
| `diagnostic.gate_tool_provider_map_size` | `817` | 各凭据提供方的工具映射总量（绝大多数被凭据闸拦掉） |

**`actual_tools` 的 21 个（DM 相位，按 kind 排序）**：

- `lifecycle`(6)：`search_tools`, `task_create`, `task_get`, `task_list`, `task_update`, `write_skill`
- `mcp`(16)：`attach_file`, `browser_setup`, `get_account_info`, `get_current_time`, `image_generate`, `pdf_read`, `search_messages`, `terminal_exec`, `terminal_glob`, `terminal_job_logs`, `terminal_job_manage`, `terminal_output_get`, `terminal_rg`
- `synthetic`(2)：`ask_user`, `suggest_colony`

> 注：`browser_setup` 之外 20 个 browser_* 工具、`edit_file`/`read_file`/`write_file`/`search_files`、`web_scrape`、`chart_render`、`csv_*`/`excel_*`、`terminal_job_start`/`terminal_pty_*` 都在 `expected_tools` 里但不在 `actual_tools`，属于**懒加载档**，由 `search_tools` 搜索结果提升后才可调。

### 2. `actual_tools` 与 `expected_tools` 的分层来源

**`expected_tools` = 61 名的构成**：

- 底座 eager 集 24 名（`flatSurfaceEagerOrder`，`internal/cli/tool_surface_flat.go:257`）：
  始终启用的四个类别 `file_ops` / `terminal_basic` / `context_awareness` / `browser_core`（`internal/tools/tier_defaults.go:109` 的 `AlwaysEnabledCategories`），加上相位注册表工具、`image_generate` 播种、loop 合成工具。
- 懒加载档：`-` 上面列的那批 browser_*/file/pty/web/chart/表格工具。

**`actual_tools` 比 `expected_tools` 少的 40 个全部是懒加载项**，不是「不存在」。

`live_tools` 路由把两套视图分开渲染，注释在 `internal/server/routes_session_live_tools.go:95-107`：`actual_tools` 取 `phase_state.get_current_tools()`（eager 集），`expected_tools` 是「允许但尚未可调」的面。**这个区分是刻意的**，此处有明确注释说曾经的 fallback 会把 allowed 误报成 callable，已删。

### 3. 相位清单（真正的准入名单）

`internal/cli/colony_pivot_lean.go:548` —— DM 相位 9 名：

```
suggest_colony, task_create, task_update, task_list, task_get,
search_tools, write_skill, list_queens, message_queen
```

`internal/cli/colony_pivot_lean.go:563` —— colony 相位 23 名：

```
get_worker_status, inject_message,
run_playbook, get_playbook_status, stop_playbook, list_playbook_runs,
run_worker, stop_worker, write_skill,
set_trigger, remove_trigger, list_triggers,
tracker_sql, tracker_register_writable, tracker_upsert, tracker_query,
task_create, task_update, task_list, task_get,
search_tools, list_queens, message_queen
```

差集：

- **colony 独占（15 个）**：`get_worker_status`, `inject_message`, `run_playbook`, `get_playbook_status`, `stop_playbook`, `list_playbook_runs`, `run_worker`, `stop_worker`, `set_trigger`, `remove_trigger`, `list_triggers`, `tracker_sql`, `tracker_register_writable`, `tracker_upsert`, `tracker_query`
- **DM 独占（1 个）**：`suggest_colony`

外加 `queenLifecycleExtraTools`（`colony_pivot_lean.go:593`）：`switch_to_independent`, `get_worker_health_summary`，它们不在两张清单里，靠 `colonyBoundQueenTool` 单独闸。

### 4. 闸门在哪一行

`internal/server/phase_tool_surface.go:322` `ChatRuntime.toolsForTurn()`：

```go
runtime := r.boundColonyRuntime()
if runtime != nil {
    return filterToolsForPhase(filterToolsByColonyAllowlist(runtime, r.tools), QueenPhaseColony)
}
out := ... // 未绑 colony：剔除 run_worker 与 colonyBoundQueenTool 命中的 def
```

`internal/server/queen_lifecycle_tools.go:391` `colonyBoundQueenTool()` 只命中四个：`get_worker_status` / `inject_message` / `stop_worker` / `get_worker_health_summary`。

`internal/server/phase_tool_surface.go:346` `filterToolsForPhase()`：colony 相位剔除 `suggest_colony`；非 colony 相位若 `suggest_colony` 缺席则**主动补上**（来自 loop 的合成 builder，所以即使目录没带也能出现）。

这就是为什么 DM 会话的 `actual_tools` 里一定有 `suggest_colony` 而没有 `run_worker`。

### 5. 第四道闸：dispatcher 拦截（不是「没有这个工具」，而是「调了会被改写」）

`internal/agentloop/colony_dispatch.go` 在**任何 executor 执行之前**拦截：

- DM 调 `task_create(new_colony=true)`：返回
  `task_create(new_colony=true) is colony-only (currently 'independent'). In DM, use new_session for unrelated work, or suggest_colony to spawn the first colony.`
- colony 调 `suggest_colony`：被改写成 `run_worker` / `set_trigger` / `write_skill` 的引导。
- worker 调任一个：`task_create(new_colony=true) is queen-only. Workers don't spawn colonies.`

拦在 dispatcher 而不是 executor，原因写在注释里：60 秒的工具超时会杀掉需要等用户弹窗几分钟的调用。

所以「工具存在」和「工具在这里能用」是两件事：`suggest_colony` 在 colony 相位是**看起来被移除**（`filterToolsForPhase` 剔除），而 `task_create(new_colony=true)` 是**仍然在场但被改写**。两者拦截层不同。

### 6. 条件性工具：`list_queens` / `message_queen`

这两名在两张相位清单里都有，但实际是否出现取决于 `queen_mail.enabled`：

- `internal/cli/loader_queen_mail.go:42` `enableQueenMailTools()` —— `manager.QueenMail() == nil` 时直接返回，不注册。
- 本机 `.hive_go/configuration.json:26` 是 `"enabled": true`，所以理论上可用；但本次实测的 `actual_tools` 里**没有** `list_queens`/`message_queen`。

**这是一个已确认的不一致，不下结论**：可能是我这个会话创建时 mail adapter 尚未就绪（`SetQueenMail` 在 serve 启动路径里晚于会话注册），也可能是 live_tools 的 eager 集不走 mail binding。要确定得抓一次带 mail 的会话的 `live_tools` 或直接调用试。**这是唯一一个我没能用运行时证据闭环的点**，明确标出来。

### 7. Prompt 层声明的工具（第三层）

`internal/queen/prompt_sections.go` 在 prompt 文本里直接点名：`ask_user`（:25）、`suggest_colony`（:25/:47/:55/:62）、`write_skill`、`tracker_sql`、`run_playbook`、`run_worker`（:47）、`tracker_query`（:98）、`converge`/`worker`（playbook 脚本内 API，不是工具）。

prompt 里那句「once the user has confirmed, you'll have `tracker_sql`, `run_playbook` (and `run_worker`) and the rest of the colony toolkit on the other side」正好对应上表的 15 个 colony 独占项 —— **prompt 声明与代码清单一致**，这一层是通的。

### 8. 一句话给结论

Queen 的 tool 面 = **61 名允许面 / 21 名 DM 常驻可调 / colony 侧另加 15 名**，由 `toolsForTurn` + `filterToolsForPhase` + `colonyBoundQueenTool` + dispatcher 四道闸切分。DM 与 colony 不是「同一套工具的开与关」，是**两张清单加一次运行时替换**。

---

## 第二部分：Colony 支持导入导出吗

### 0. 一句话结论

**导入：完整支持，服务端 6 条路由，能力相当硬（2GB 分片上传、tar/gz/bz2 解包、路径穿越防护、多根布局、覆盖开关），但前端零调用，等于只能靠 API 手动打。**

**导出：不存在。整仓没有任何 export 路由、没有 CLI 子命令、没有前端入口。**

所以现状是**单向的**：能进不能出。

### 1. 导入 —— 服务端（4 层里的 route 层，完整）

`internal/server/routes_colonies.go:42-58` 注册了 6 条：

| 方法 | 路径 | handler |
|---|---|---|
| POST | `/api/colonies/import` | `handleImportColony`（一次性 multipart） |
| POST | `/api/colonies/import/init` | `handleInitUpload` |
| PUT | `/api/colonies/import/chunk/{upload_id}` | `handleChunkUpload` |
| GET | `/api/colonies/import/status/{upload_id}` | `handleUploadStatus` |
| POST | `/api/colonies/import/finalize/{upload_id}` | `handleFinalizeUpload` |
| DELETE | `/api/colonies/import/{upload_id}` | `handleCancelUpload` |

契约层也对得上：`contracts/api-routes.json` 里六条全在（`/api/colonies/import`、`/import/{upload_id}`、`/import/chunk/{upload_id}`、`/import/finalize/{upload_id}`、`/import/status/{upload_id}`、`/import/init`）。

实现细节（`internal/server/routes_colonies_import.go`）：

- 上限：单包 2 GiB、单片 128 MiB、并发上传 32、TTL 24 小时（`:35-38`）
- 格式：`tar` / `tar.gz` / `tar.bz2`，`gzip.NewReader`（:742）、`bzip2.NewReader`（:750）
- 安全：`safeExtractArchive` + `_safe_extract_tar` 对齐（文件头 `:9` 有 Parity 注释），路径穿越被拦
- 两种布局：`importLegacySingleRoot`（单根，`routes_colonies_import.go:1222`）与 `importMultiRoot`（多根，:1266），由 `extractUploadedArchive` 在 :1197 分派
- 覆盖语义：目标已存在且 `replace_existing != true` → **409** + `"hint": "set replace_existing=true to overwrite"`（:1240-1243）；覆盖时先 `os.RemoveAll`
- 解包失败会 `os.RemoveAll(target)` 回滚，不留半个目录（:1254）
- 名字校验走 `validateColonyName`（非法 slug → 400），支持 `name_override`

测试：`internal/server/routes_colonies_import_test.go`，**45 个 Test 函数**。这一层是真实有覆盖的。

### 2. 导入 —— 前端（第 3 层，缺口在这一点）

```
grep -rn "colonies/import" frontend/src   →  0 命中
```

`frontend/src/api/colonies.ts`（83 行）里没有导入方法，只有 `list` / `scaffold` / `getTools` / `updateTools` / `rename` / 删除。

也就是说：**后端能力齐全，UI 完全没有入口**。想导入只能自己 `curl` 走 6 条路由。

这与上一份审计 `docs/colony-fork-ui-entry-audit.md` 里那条规律一致（route 有、wrapper 无、UI 无）。

### 3. 导出 —— 四层全空，是不存在而非未接线

逐层查过，全空：

| 层 | 检查方式 | 结果 |
|---|---|---|
| route | `grep 'HandleFunc("' internal/server/ \| grep -i export` | **0 命中** |
| 全部 colony 路由枚举 | `grep 'api/colonies' \| grep HandleFunc` | 21 条，**无一条含 export / download** |
| 契约 | `contracts/api-routes.json` 里 colony 相关路径 | 无 export |
| CLI | `contracts/cli-surface.json` 的 `hive colony` | 只有 `list` / `info` / `delete` |
| 前端 | `grep -riE "export.{0,20}colony\|colony.{0,20}export"` | 只有 TS 的 `export interface` 等，语义无关 |

现存最接近「导出」的能力只有两条，都不是导出：

1. `POST /api/colonies/{colony_id}/reveal`（`routes_colonies.go:65`）—— 在 Finder 里打开目录，然后人自己压缩。这是**本地路径导向**的操作，服务端不产生任何可下载字节。
2. 无。仅此一条。

对比导入测试有 45 个函数，导出方向**连一个 handler 文件都没有**。

### 4. 为什么导出缺失可能是刻意的（与上一轮 fork 结论呼应）

上一轮审计已经确认仓里**没有 `POST /api/colonies`**（服务端不给 colony 做整目录复制），原因写在 `docs/colony-fork-ui-entry-audit.md`：整目录复制会把 `worker.json` 里的**绝对路径绑定**带到新 colony，副本的 worker 会写回源 colony 的 tracker。

导出**不受这个约束**（导出是离开本机，不在本机再生成一个活 colony），所以缺导出**不能用同一条理由解释**。它看起来就是没做，不是一个被论证掉的设计选择。

**这一条我标成「事实缺失，原因未记录」**，不编造理由。要确认只能查 Python 基线 `framework/server/routes_colonies.py` 有没有 `handle_export_colony`：Go 侧该文件的 Parity 头（`routes_colonies_import.go:3-10`）列了 8 个 Python 函数，**全部是 import 侧**，没有 export。这至少说明 Go 的移植清单里从来就没出现过导出。

---

## 附：两个问题的交叉点

- Queen 的 61 名工具面里，**没有任何一个工具能操作 colony 的导入导出**。`run_worker` / `tracker_*` / `playbook` 全是 colony 内部编排，`suggest_colony` 是建新 colony。所以「让 Queen 帮我导入一个 colony」这条路**不通**，Queen 只能 `terminal_exec` 手打 curl。
- 反过来说，`POST /api/colonies/import` 是纯 HTTP + multipart，**用 `terminal_exec` 起个 curl 完全可行**，这是唯一可用的自动化路径。

---

## 待确认（明确标出，不含推测）

1. `list_queens`/`message_queen` 在 `queen_mail.enabled=true` 的配置下为何未出现在 `actual_tools` —— 需要抓一个 mail 已就绪的会话的 `live_tools`，或直接调用试。
2. 导出缺失是「未实现」还是「被有意排除」—— 仓内无记录，Python 基线也未在 Go 的 Parity 头里出现 export，倾向于未实现。
