# harness 组登记与别名对齐说明（harness groups / spreadsheet aliases / browser_setup）

上游 `core/framework/tools/harness_tools.py` 把五个进程内工具组（harness
groups）直接服务给会话：`terminal-tools`、`files-tools`、`chart-tools`、
`memory-tools`、`hive_tools`。`tools/tier_defaults.go` 的分类（`charts`、
`spreadsheet_advanced`、`browser_*`、`context_awareness` 等）与
`@server:<组名>` 引用都按这些组名解析。本仓库此前只有 `files-tools` 一组在会话
目录中登记（`internal/cli/session_tools.go` 的 `recordFilesToolGroup`），其余
组在 Tool Library 与分类解析里是空组。本次把五组补全，并顺带对齐三件工具面差异：

1. csv/excel 的上游“每动作一个工具名”别名；
2. `browser_setup` 进程内 discovery 工具；
3. `search_timeline` 严格跟随 `memory_timeline` 开关。

## 1. 五个组的登记

登记入口：`internal/cli/session_tools_harness_groups.go` 的
`recordHarnessGroups()`，在目录构造时（`newSessionToolCatalogue`）调用；每组
数据都通过 `ToolRegistry.RecordBuiltinToolGroup(group, tools)` 写入注册表的
server bookkeeping，Tool Library、`@server:<group>` 分类、per-colony gating
因此都能看到。名字已被真实 MCP server 占用的组不覆盖（与 files-tools 先例一致）。

| 组 | 成员（登记名） | 执行路径 |
| --- | --- | --- |
| `terminal-tools` | `terminal_exec`、`terminal_rg`、`terminal_glob`、`terminal_output_get`、`terminal_job_start`、`terminal_job_logs`、`terminal_job_manage`、`terminal_pty_open`、`terminal_pty_run`、`terminal_pty_close`（10） | builtin 沙箱（`internal/tools/builtin`） |
| `files-tools` | `read_file`、`write_file`、`edit_file`、`search_files`（4） | builtin 沙箱 |
| `chart-tools` | `chart_render`（1） | chart lane 的渲染器（登记时不实现） |
| `memory-tools` | `search_messages`；`search_timeline` 仅在 `memory_timeline` 开关打开时（1–2） | 会话目录内的 memory 引擎 |
| `hive_tools` | `web_scrape`、`pdf_read`、`attach_file`、`get_current_time`、`image_generate`、`get_account_info`、`read_file`、`edit_file`、`csv_read`、`csv_write`、`csv_append`、`csv_info`、`excel_read`、`excel_write`、`excel_append`、`excel_info`、`excel_sheet_list`、`excel_search`（18） | 注册表（集成）+ builtin（`read_file`/`edit_file`） |

成员来源按上游注册顺序；`read_file`/`edit_file` 同时属于 `files-tools` 与
`hive_tools`（上游 `_hive_core` 用 `register_file_tools(tool_names={"read_file",
"edit_file"})` 再次导出，行为一致）。

### chart_render

渲染器由 chart lane 提供。本 lane 只登记名字：

* 若渲染器已进入注册表或 builtin 工具箱，直接采用它的 schema；
* 若尚未进入，catalog 以“以未实现占位”写入上游参数面（`kind`/`spec`/`title`/
  `width`/`height`/`dpi`/`output_path`），执行时返回
  `{"error": "not_implemented", ...}`，而不是含糊的 unknown tool。
* 占位分支只在“注册表、builtin、已宣告的 defs 都没有该工具”时生效，渲染器落地后
  自动让位。

## 2. csv / excel 工具名别名

上游 `hive_tools` 把每个动作暴露为独立工具。Go 端口最初以聚合工具
（`csv` / `excel` + `action` 选择子）发布，导致 `spreadsheet_advanced` 分类与
allowlist 里引用的 `csv_read`、`excel_read` 等名字解析不到工具。

本次新增 `internal/tools/integrations/spreadsheet_aliases.go`：

* `InstallSpreadsheetAliases` 把聚合 catalog 的每个动作注册为独立工具名：
  `csv_read`、`csv_write`、`csv_append`、`csv_info`、
  `excel_read`、`excel_write`、`excel_append`、`excel_info`、
  `excel_sheet_list`、`excel_search`（10 个）。
* schema 直接取该动作的参数表与 required 列表（单动作 schema，不再带 `action`）。
* 执行复用聚合实现：别名 executor 以 `action=<别名>` 调回同一注册表里的
  `csv`/`excel` 工具，沙箱 root、默认值、错误 envelope 与聚合工具完全一致。
* 名字已被真实 MCP server 注册时跳过（真 MCP 优先）。
* 聚合工具 `csv` / `excel` **保留**：现有调用方、`TestSessionToolCatalogueInstallsWaveFourModules`
  与运行器仍引用 `excel`；上游没有聚合名，这里作为 Go 特有便捷面记录在案。
* `csv_sql` / `excel_sql` **保持不支持**：上游 `gen_catalog.py` 本身就排除
  `csv_sql`，`excel_sql` 依赖 DuckDB（本端口不引入原生依赖），两者都不进
  catalog、不做 stub，调用即为 unknown tool/unknown action。

## 3. browser_setup

`internal/tools/integrations/browser_setup.go` 按
`core/framework/tools/browser_tools.py` 移植进程内 discovery 工具：

* 名称 `browser_setup`，无参数（`additionalProperties: false`），
  `concurrency_safe=true`，queen 与 worker 目录都通过注册表拿到；
* 返回上游同一 envelope：`ok`、`cli=hive-browser`、`how`（`terminal_exec` 跑
  `hive-browser <command> ... --json`）、`first_step`（`hive-browser setup
  --json` 检查扩展连接并列出 Chrome profile）、`commands`（14 条命令面）、
  `coordinates`（点击/悬停/按键坐标是 0..1 **fraction**，不是像素）、
  `screenshots`（截图落盘 `saved_to`，框架自动附到下一轮，不要重复
  attach_file）、`exit_codes`（`0 ok · 2 not_connected · 3 not_started ·
  4 not_found · 6 bad_args · 7 pending_dialog · 8 rate_limited`）；
* 执行不读环境、不起子进程、不调网络，纯静态表；
* 登记 `browser` 组（`RecordBuiltinTools`，等价上游
  `record_builtin_tools("browser", ...)`，成员合并、harness 记录落表），因此
  `browser_basic`/`browser_core` 等分类与 worker keep-set 能看到它；该记录
  在凭据 resync（`dropMCPServers`）后仍保留。

## 4. search_timeline 开关

`search_timeline` 与上游 `register_search_timeline` 一样跟随
`get_memory_timeline_enabled()`（`internal/server/config_features.go::
MemoryTimelineEnabled`，env `HIVE_MEMORY_TIMELINE` 优先，其次
`hive.config.json` 的 `memory_timeline`）：

* 开关关：不进 `memory-tools` catalog，也不进模型可见 defs；
* 开关开：catalog 与 defs 同时出现；
* 显式调用被关掉的 `search_timeline` 返回 `feature_disabled`（已有行为，
  未改动）。

Go 特有的 `memory_timeline`（自研 browse-by-day 工具）不进 `memory-tools` 组：
上游该组只有 `search_messages`（+ 条件项 `search_timeline`）。

## 5. 测试

新增测试（全部带 `// Parity:` 或 `// Parity-exempt:` 注解）：

* `internal/cli/session_tools_harness_groups_test.go`
  * 五组登记 + 每组名字集合 + `input_schema` 形状（`test_every_group_builds`）；
  * `memory_timeline` 开关控制 catalog 与 defs（关/开两态）；
  * `chart_render` 占位执行返回 `not_implemented`（渲染器落地后 skip）；
  * csv/excel 别名与 `browser_setup` 出现在会话 defs，SQL 名不出现。
* `internal/tools/integrations/spreadsheet_aliases_test.go`
  * 10 个别名的存在性、单动作 schema、required 列表（对齐上游签名）；
  * csv、excel 各走一遍 write → read → append → info（+ sheet_list/search）
    的别名行为；
  * 错误 envelope 透传；`csv_sql`/`excel_sql` 不可达；已占用名不被覆盖。
* `internal/tools/integrations/browser_setup_test.go`
  * envelope 全字段 + 14 条命令 + fraction 坐标 + exit codes + 静态可重复；
  * `browser` 组登记与 `input_schema`；重复安装幂等。

## 6. 未对齐项

* `csv_sql` / `excel_sql`：上游有（需要 DuckDB/本地 SQL），本端口不提供，见 §2。
* `hive_tools` 的条件成员 `senders`（`HIVE_EMAIL_SENDERS` 打开时上游会加 9 个
  sender 工具）：Go 端口是单个聚合 `senders` 工具，本次未进组；开启开关后它
  仍以注册表工具单独可达。
* excel 的可用性门（上游 `importlib.util.find_spec("openpyxl")`）在 Go 端不存在：
  实现是纯标准库 OOXML 读写，excel_\* 恒定可用，因此 `hive_tools` 恒有 6 个
  excel 别名（上游则是“装了 openpyxl 才有”）。
* `chart_render` 的实现随 chart lane 合并；本 lane 只保证 catalog 名字、分类解析
  与未实现时的明确报错。
* 描述文本：别名的 model-facing description 取 catalog 的动作摘要（上游为函数
  docstring 全文），参数说明沿用 catalog 生成器的简表。
