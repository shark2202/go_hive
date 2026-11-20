# Default flat tool surface — design

Status: implemented on `tool-flat-surface` (from `main` @ 40945349).
Evidence sources: `docs/tool-contract-diff.md` §11（recheck @ c8cca067）+
`docs/tool-consistency-audit.html`（P0「名称形状：87 分组 vs 857 扁平」）。

## 1. 问题

| 侧 | 默认会话模型可见 `tools[]` | 形状 |
| --- | --- | --- |
| Python 1.0.14（reference） | **24** | 扁平工具名（`read_file`、`terminal_exec`、`task_create`…） |
| go_hive（target，改动前） | **144** | 混合：137 注册表（含 **87** 个分组集成模块 + `action`）+ 10 lifecycle − 4 colony 相位门控 + `ask_user` |

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
| 其余 27 个 | 既有 builtin / task / skills / 集成注册表 |

**未实现**（advertise + 显式 `not_implemented` 信封，不静默）：
`collect_result`、`credentials`、`sentinel_setup`、`suggest_colony`
（Python 侧有实现，Go 只落了工具定义/局部实现；本车道不代实现）。

## 5. 计数（改动后）

| 口径 | 数量 |
| --- | --- |
| catalogue 注册表（内部池，含分组） | 137（+ queen lifecycle 10 当会话为 queen） |
| 默认扁平面 `ModelDefs()`（queen 会话） | **39**（= manifest 三层并集） |
| 默认首轮请求（未绑定 colony） | **35** = 39 − 4 个 colony 相位门控（`run_worker`、`get_worker_status`、`inject_message`、`stop_worker`；上游语义，绑定 colony 后为 39） |
| `HIVE_TOOL_SURFACE=grouped` | 完整注册表（137 + 10 = 147；实测请求 144，与改动前一致） |

### 5.1 线上抓取证据（假网关，同 §11 口径）

抓取脚本/产物：`~/.hermes/profiles/omh-tui/cache/scratch/toolflat/`
（`capture_flat.py` = §11 `capture.py` 指向本 worktree 的副本；`captures/go-llm-tools.flat.json`、
`captures/go-llm-tools.grouped.json`）。

| 会话 | 首轮请求 `tools[]` | 说明 |
| --- | --- | --- |
| 默认（flat） | **35** | name 集合与 `(llm_request ∪ lifecycle ∪ synthetic) − 4 门控` 逐字相同；Python 参考面 24 个 name 全部命中（24/24） |
| `HIVE_TOOL_SURFACE=grouped` | **144** | 与改动前 §11 抓取一致；`airtable`/`slack` 等分组名回归 |

默认抓取上的逐字口径：description 31/35 逐字相同（4 个差异 = `run_playbook` + terminal 3 个，
均在保留现状例外内）；schema 归一化（空 `required`≡缺省、数值等价）24/35，其余 11 个
（terminal 6 + lifecycle 5）全部是 §3 的保留现状例外，无未归因差异。


## 6. 测试

`internal/cli/tool_surface_flat_test.go`：

- `TestFlatSurfaceNamesMatchPythonDefault`：默认 queen 会话 catalogue 的
  `ModelDefs()` name 集 == manifest 三层并集（39），无分组名；
- `TestFlatSurfaceSchemasMatchFrozenContract`：逐工具 name/description/
  input_schema 与冻结契约逐字一致（例外见 §3，逐条列出并计数）；
- `TestFlatSurfaceGroupedSwitch`：`HIVE_TOOL_SURFACE=grouped` 回退为完整池；
- `TestFlatSurfaceExecutionRoutes`：映射工具调用落到既有 action（无 colony →
  可操作的 colony 报错，而非 unknown tool）、未实现工具返回显式信封；
- `TestFlatSurfaceFrozenDataMatchesContract`：重读 `contracts/tool-definitions.json`
  对拍冻结数据。

## 7. 复现 / 再生

```sh
uv run --no-project python go_hive/scripts/gen_tool_surface_flat.py          # 重写冻结数据
uv run --no-project python go_hive/scripts/gen_tool_surface_flat.py --check  # CI 对拍
(cd go_hive/go-hive && make tool-surface-check)                               # 同上，Makefile 入口
(cd go_hive/go-hive && CGO_ENABLED=0 GOPROXY=off go test ./internal/cli -run TestFlatSurface -v)
```

## 8. 影响与已知边界

- **默认请求瘦身**：144 → 35。分组集成模块（87）、harness 目录层（`chart_render`、
  `web_scrape`、`web_search`）、Go-only 扁平工具（`tracker_*`、`skill_view`/`list_skills`、
  `credential_status`、`search_timeline`/`memory_timeline` 等）默认不进模型请求；
  执行路由与 Tool Library/注册表记录不变，`HIVE_TOOL_SURFACE=grouped` 可整体回退。
  这是「name 逐字一致」的代价：Python 默认请求里没有这些名字。
- **colony 相位**：4 个 colony 工具按上游语义在绑定 colony 前不出现在请求里；
  绑定后默认面为 39。tracker 工具（Go 侧 colony 记账）不在 39 内，
  colony 会话若需要它们需 `grouped` 形态——后续可把 `search_tools` 的延迟层
  扩到非 MCP 工具，让隐藏工具可被按名加载（本车道未做）。
- **Python 24 vs Go 35**：Python 首轮请求只有 24 个 always-on 工具，其余
  lifecycle/目录层走 Tool Library/延迟加载；Go 没有等价的延迟层，因此按上游
  「默认可见并集」放全部 39（请求 35）——这是本车道选择的口径，数字逐条可查。
- `HIVE_TOOL_SURFACE` 是新增环境变量；env 契约（`contracts/env-vars.json`，
  report-only 门禁）由 env 车道冻结，本车道未改，合并后可补登记。

