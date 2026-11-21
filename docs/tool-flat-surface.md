# Default flat tool surface — design

Status: flat surface implemented on `tool-flat-surface` (from `main` @ 40945349);
**lazy layer implemented on `tool-library-lazy-surface` (from `main` @ 93193450)** —
the first request now carries the Python reference's 24 always-on names verbatim.
Evidence sources: `docs/tool-contract-diff.md` §11（recheck @ c8cca067）+
`docs/tool-consistency-audit.html`（P0「名称形状：87 分组 vs 857 扁平」）+
本车道抓包（§5.2）.

## 1. 问题

| 侧 | 默认会话模型可见 `tools[]` | 形状 |
| --- | --- | --- |
| Python 1.0.14（reference） | **24** | 扁平工具名（`read_file`、`terminal_exec`、`task_create`…） |
| go_hive（target，改动前） | **144** | 混合：137 注册表（含 **87** 个分组集成模块 + `action`）+ 10 lifecycle − 4 colony 相位门控 + `ask_user` |
| go_hive（`tool-flat-surface` 后） | **35** | 扁平投影 39（三层并集）− 4 colony 相位门控；无延迟层 |
| go_hive（本车道后，默认） | **24** | 与 Python 首轮逐字同序（§5.2 实测）；其余 51 个可执行工具（15 lifecycle/colony + 36 扁平目录层）经 `search_tools` 延迟加载 |

硬要求：LLM 可见工具面的 `name` 与 Python **逐字一致**。分组模块名（`airtable`、`slack`…）
在 Python 侧不存在，逐工具同名在分组形态下不成立（审计 §03/§07：只有 36 个交集）。
因此默认会话必须暴露**扁平**工具面；分组集成工具降为内部注册表/别名。

## 2. 默认集合的定义（以 manifest 为准）

Python 侧的「默认可见」由 `contracts/tool-definitions.json` 的三层构成：

| 层 | 条目 | 说明 |
| --- | --- | --- |
| `llm_request` | 24 | 默认 queen 会话首轮请求体 `tools[]`（假网关截获） |
| `lifecycle` | 16 | 默认会话的 queen lifecycle 表（Tool Library `lifecycle` 列表） |
| `synthetic` | 1 | `ask_user`（运行时注入） |

去重后（`ask_user` 同时在 `llm_request`+`synthetic`；`write_skill` 同时在
`llm_request`+`lifecycle`）：

```
24 + 16 + 1 − 2 = 39 个唯一 name
```

`mcp_catalog`（26）是 Tool Library/延迟加载层，**不在默认请求内**（Python 侧
`enabled_mcp_tools` 里的条目也并非都进请求），因此排除。

39 个默认 name（字节序）：

```
ask_user attach_file browser_setup collect_result credentials get_account_info
get_current_time get_playbook_status get_worker_status image_generate
inject_message list_browser_profiles list_playbook_runs list_triggers
list_worker_questions pdf_read remove_trigger reply_to_worker run_playbook
run_worker search_messages search_tools sentinel_setup set_trigger stop_playbook
stop_worker suggest_colony task_create task_get task_list task_update
terminal_exec terminal_glob terminal_job_logs terminal_job_manage
terminal_output_get terminal_rg update_worker_profile write_skill
```

## 3. 实现（Go）

- **冻结数据**：`go_hive/go-hive/internal/cli/tool_surface_flat.json`，由
  `go_hive/scripts/gen_tool_surface_flat.py` 从 `contracts/tool-definitions.json`
  的上述三层逐字导出（name/description/input_schema 不改写）。`go:embed` 进
  `internal/cli/tool_surface_flat.go`，另有测试在运行时重读 contracts 文件对拍。
- **模型可见面 = catalogue 的投影**：`sessionToolCatalogue.ModelDefs()`
  - `flat`（默认）：按 39 个 name 的字节序输出；每个 name 取冻结契约；
    三个 chat 装配点（`loader.go`、`loader_preloaded_session.go`、`longmemeval.go`）
    改用它。
  - `grouped`：返回完整注册表（改动前行为，137 + 10）。
  - `sessionToolCatalogue.Defs()` 语义不变：完整注册表。colony worker 的
    declared-tool pool（`colony_worker_definition.go`）继续读 `Defs()`，
    分组工具仍可被 worker 声明选中。
- **注册表不变**：分组集成模块、harness 组、tracker/skill/mail 等工具仍在
  registry / Tool Library / `@server:` 分类里（内部注册表 + 别名）；只是默认
  不进模型请求。显式调用分组名（`airtable` 等）执行路由不变。
- **开关**：环境变量 `HIVE_TOOL_SURFACE=grouped` 回退到分组形态；缺省/`flat`
  为扁平。未知值按 `flat` 处理（默认不得暴露分组）。
- **延迟层（Python Tool Library 同形）**：`sessionToolCatalogue.LazySurfaceDeps()` 产出
  `(alwaysOn, deferred)`，经 `ChatRuntimeDeps.AlwaysOnToolNames/DeferredTools` 交给
  `server.chatToolTier`，复刻上游 `QueenPhaseState` / `ToolTierState` 的 eager/searchable 拆分：
  - `alwaysOn` = 冻结 manifest `llm_request` 层的 24 个 name，**顺序按上游抓包**
    （`flatSurfaceEagerOrder`：phase 注册表工具 → always-enabled 类别 → 合成工具）；
  - `deferred` = 默认面其余 15 个（lifecycle/colony 层）+ 36 个扁平目录层工具
    （`chart_render`、`web_scrape`、`web_search`、`read_file`/`write_file`/`edit_file`/
    `search_files`、`tracker_*`、`skill_view`/`list_skills`、`credential_status`、
    `terminal_job_start`/`terminal_pty_*`、`csv_*`/`excel_*` 别名、网络扫描器等）；
  - 首轮请求只含 24 个 eager 全量 schema；51 个 deferred 以
    `<searchable_tools>` 提醒按 name 一行宣布（上游 `SearchableToolsReminderSource`
    同文案），**不在 `tools[]` 内**；
  - `search_tools(query="select:name_a,name_b")` 或关键词搜索 → `promote_searched_tools`
    → 下一轮 `tools[]` 追加已加载名称（eager 前缀顺序不变），会话内持久化到
    `<session>/tool_tiers.json`；
  - 加载前直接调用 deferred 工具由 loop 拦下并返回 `not loaded yet` 提示（不是 unknown tool）；
  - 绑定 colony 后进入 colony 相位：`toolsForTurn()` 的 39 个默认面重新 eager（保持
    改动前 colony 行为），目录层仍为 searchable；
  - 分组集成模块（`airtable` + `action` 形状）**不进**延迟层：它们不是扁平 name，
    继续留在注册表与 `grouped` 形态。
- **schema 例外（保留现状，不改写）**：
  1. P1 对齐表（`tool_contract_parity.json`）覆盖的 12 个默认工具
     （`attach_file`、`get_account_info`、`get_current_time`、`image_generate`、
     `pdf_read`、`search_messages`、terminal 6 个）——它们已由 P1/terminal 车道
     冻结，逐字 schema 以现场定义为准；
  2. lifecycle 层在 Python Tool Library 里只渲染 name+description（无
     input_schema，manifest 里是空对象），Go 侧的真实 schema 信息量更大且是
     执行契约（`run_playbook`/`run_worker`/10 个 lifecycle、`write_skill` 除
     外——它是 `llm_request`+`lifecycle`，有真实 schema，按 manifest 应用）。
     这是层间不可比（§11.4 已记录），不是缺陷。

## 4. 执行路由（不改变既有实现）

扁平工具调用落到既有实现，唯一新增的是把「分组 action」映射回扁平名：

| 扁平工具 | 落点 |
| --- | --- |
| `get_playbook_status` / `list_playbook_runs` | `colony.RunPlaybook`（既有 `run_playbook(action="status")`） |
| `stop_playbook` | `colony.RunPlaybook(action="stop")` |
| `run_playbook` / `run_worker` | 既有实现（colony） |
| 10 个 lifecycle（`get_worker_status`…`update_worker_profile`） | 既有 `QueenLifecycleBinding`（queen 会话启用；4 个 colony 相位工具按上游语义门控） |
| `search_tools` | agent loop 的 tier 拦截（有 tier 时）；无 tier 时返回 inventory 信封 |
| 延迟层 51 个（含 15 lifecycle + 36 目录层） | 加载后走各自既有实现（builtin / task / skills / queen lifecycle / tracker / colony / 集成 registry）；未加载时 loop 拒绝并提示 `select:<name>` |
| 其余 27 个 | 既有 builtin / task / skills / 集成注册表 |

**未实现**（advertise + 显式 `not_implemented` 信封，不静默）：
`collect_result`、`credentials`、`sentinel_setup`、`suggest_colony`
（Python 侧有实现，Go 只落了工具定义/局部实现；本车道不代实现）。

## 5. 计数（本车道后）

| 口径 | 数量 |
| --- | --- |
| catalogue 注册表（内部池，含分组） | 137（+ queen lifecycle 10 当会话为 queen） |
| 默认扁平面 `ModelDefs()`（queen 会话） | **39**（= manifest 三层并集） |
| 默认首轮请求（未绑定 colony，本车道后） | **24** = manifest `llm_request` 层，name 与顺序逐字同 Python（§5.2 实测） |
| 延迟层（searchable，首轮不出现） | **51** = 默认面其余 15（lifecycle/colony）+ 36 扁平目录层 |
| colony 绑定后 eager | **39** = `toolsForTurn()` 的全量默认面（保持改动前 colony 行为） |
| `HIVE_TOOL_SURFACE=grouped` | 完整池（本基线上实测 **150**：143 注册表 + 10 queen lifecycle + `ask_user` − 4 门控；`tool-flat-surface` 基线 @40945349 实测 144，差异来自 main 新增工具，非本车道） |

### 5.1 线上抓取证据（`tool-flat-surface` 车道，改动前口径）

抓取脚本/产物：`~/.hermes/profiles/omh-tui/cache/scratch/toolflat/`
（`capture_flat.py` = §11 `capture.py` 指向本 worktree 的副本；`captures/go-llm-tools.flat.json`、
`captures/go-llm-tools.grouped.json`）。

| 会话 | 首轮请求 `tools[]` | 说明 |
| --- | --- | --- |
| 默认（flat，本车道前） | **35** | name 集合与 `(llm_request ∪ lifecycle ∪ synthetic) − 4 门控` 逐字相同；Python 参考面 24 个 name 全部命中（24/24） |
| `HIVE_TOOL_SURFACE=grouped` | **144** | 与改动前 §11 抓取一致；`airtable`/`slack` 等分组名回归 |

默认抓取上的逐字口径：description 31/35 逐字相同（4 个差异 = `run_playbook` + terminal 3 个，
均在保留现状例外内）；schema 归一化（空 `required`≡缺省、数值等价）24/35，其余 11 个
（terminal 6 + lifecycle 5）全部是 §3 的保留现状例外，无未归因差异。

### 5.2 延迟层抓取证据（本车道，假网关）

抓取脚本/产物：`~/.hermes/profiles/omh-tui/cache/scratch/toolflat-lazy/`
（`capture_lazy.py` + `fake_gateway_scripted.py`；产物 `captures/go-lazy-first-tools.json`、
`captures/go-lazy-after-search-tools.json`、`captures/go-lazy-requests.jsonl`、`out/summary.json`）。
方法：临时 `HIVE_HOME` + 脚本化假网关；POST `/api/queen/queen_growth/session`；POST chat；
假网关第 1 步返回 `search_tools(select:list_triggers,read_file)`，第 2 步返回
`read_file(<probe>)`，逐步记录每个请求体。

| 请求 | `tools[]` | 断言 |
| --- | --- | --- |
| #0 首轮 | **24**，name 与顺序与 Python 抓包（`toolresidual/out/py-gateway.jsonl` row 0）**逐字相同**（`first_matches_python_order=true`） | 24 vs 35：本车道修复的直接对照；deferred 名不在其中 |
| #1 search_tools 之后 | **26** = 24 + `list_triggers` + `read_file`（`second_added` 实测） | 加载后下一轮出现 |
| #2 read_file 之后 | 继承 #1；messages 携带工具结果 `1  lazy probe file content`（`read_file_result_seen=true`） | 已加载工具**真实执行**并回灌模型 |
| 首轮 messages | 含 searchable-tools 提醒，逐行列出 51 个 deferred name（`searchable_refusal_seen=true`） | 未加载面可发现；调用未加载工具由 loop 拒绝并提示 `select:<name>` |
| `HIVE_TOOL_SURFACE=grouped`（`capture_grouped.py`） | **150**（本基线完整池；`airtable`/`slack`/`stripe` 等分组名回归） | grouped 回退不受延迟层影响 |

## 6. 测试

`internal/cli/tool_surface_flat_test.go`：

- `TestFlatSurfaceDefaultMatchesPythonContract`：默认 queen 会话 catalogue 的
  `ModelDefs()` name 集 == manifest 三层并集（39），无分组名；
- `TestFlatSurfaceEagerOrderMatchesUpstreamCapture`（本车道）：`EagerModelDefNames()` ==
  24 个上游抓包顺序，且集合 == manifest `llm_request` 层；
- `TestFlatSurfaceLazyLayerPartition`（本车道）：deferred == 15 默认面其余 + 扁平目录层，
  与 eager 不相交、无重复、无分组模块，且每个 deferred name 都有执行路由；
- `TestFlatSurfaceDeferredToolsExecute`（本车道）：`read_file`（目录层）真实读到文件内容，
  `run_playbook`（lifecycle）落到既有 colony 实现的可操作错误，非 unknown tool；
- `TestFlatSurfaceSchemasMatchFrozenContract`：逐工具 name/description/
  input_schema 与冻结契约逐字一致（例外见 §3，逐条列出并计数）；
- `TestFlatSurfaceGroupedSwitch`：`HIVE_TOOL_SURFACE=grouped` 回退为完整池；
- `TestFlatSurfaceExecutionRoutes`：映射工具调用落到既有 action（无 colony →
  可操作的 colony 报错，而非 unknown tool）、未实现工具返回显式信封；
- `TestFlatSurfaceFrozenDataMatchesContract`：重读 `contracts/tool-definitions.json`
  对拍冻结数据。

`internal/server/chat_tool_tiers_lazy_test.go`（本车道，运行时级）：

- `TestLazyChatFirstRequestAdvertisesPythonEagerOrder`：真实 `ChatRuntime` 的首个
  completion 请求 `tools[]` == 24 个上游顺序；deferred 名不在其中；首轮 messages
  携带 searchable-tools 提醒；
- `TestLazyChatSearchLoadsDeferredToolForNextStep`：脚本化模型先调
  `search_tools(select:chart_render)`，下一请求出现 `chart_render`，再调用时执行器
  真实收到该调用；`search_tools` 结果在后续请求的 transcript 中；
- `TestChatToolTierColonyBindingRestoresEagerSurface`：绑定 colony 后 eager 恢复为
  `toolsForTurn()` 全量，目录层仍 searchable。

## 7. 复现 / 再生

```sh
uv run --no-project python go_hive/scripts/gen_tool_surface_flat.py          # 重写冻结数据
uv run --no-project python go_hive/scripts/gen_tool_surface_flat.py --check  # CI 对拍
(cd go_hive/go-hive && make tool-surface-check)                               # 同上，Makefile 入口
(cd go_hive/go-hive && CGO_ENABLED=0 GOPROXY=off go test ./internal/cli -run TestFlatSurface -v)
(cd go_hive/go-hive && CGO_ENABLED=0 GOPROXY=off go test ./internal/server -run 'TestLazyChat|TestChatToolTierColony' -v)
# 首轮 24 + search→load→execute 的真实抓包（假网关）：
uv run --no-project python ~/.hermes/profiles/omh-tui/cache/scratch/toolflat-lazy/capture_lazy.py
```

## 8. 影响与已知边界

- **默认请求瘦身**：144 → 35（`tool-flat-surface`）→ **24（本车道）**。分组集成模块
  （87）与扁平目录层（`chart_render`、`web_scrape`、`web_search`、`tracker_*`、
  `skill_view`/`list_skills`、`credential_status`、`search_timeline`/`memory_timeline` 等）
  默认不进模型请求；区别是本车道把 **51 个可执行工具**放进 `search_tools` 延迟层，
  按名加载后立即可调用——执行路由与 Tool Library/注册表记录不变。
- **延迟层覆盖面**：15 个默认面其余（lifecycle/colony，含 4 个 colony 相位门控）+
  36 个扁平目录层工具（文件、图表、搜索、tracker、skill、terminal advanced、
  csv/excel 别名、扫描器）。分组集成模块（`airtable` + `action` 形状）不进延迟层：
  它们不是扁平 name，继续留在注册表与 grouped 形态；需要时用
  `HIVE_TOOL_SURFACE=grouped` 整体回退。
- **colony 相位**：4 个 colony 工具按上游语义在绑定 colony 前不进 `tools[]`（但可在
  延迟层按名加载，调用返回可操作的 colony 提示）；绑定后 `toolsForTurn()` 的 39 个
  默认面重新 eager，保持改动前 colony 行为；tracker 工具仍在延迟层，可加载。
- **持久化**：已加载名称写入 `<session>/tool_tiers.json`，resume 后恢复（heal-on-read：
  丢弃不再注册/不再允许的 name）。
- **搜索文案/形状**与上游一致：`inventory`、`select:a,b`、关键词三种查询；
  返回 `{loaded, already_loaded, tools:[{name,description}], note}`；重复搜索已加载工具
  返回 `already_loaded` 提示。
- `HIVE_TOOL_SURFACE` 是 `tool-flat-surface` 车道新增的环境变量；env 契约
  （`contracts/env-vars.json`，report-only 门禁）由 env 车道冻结，本车道未改。
- **模型侧代价**：首轮 messages 的 searchable-tools 提醒从 35-name 面收敛为 51 行
  name（无 schema），`tools[]` 从 35 个全量 schema 降到 24，prompt 体积明显下降；
  被加载工具在后续请求追加 schema，只为其实际使用付费。

