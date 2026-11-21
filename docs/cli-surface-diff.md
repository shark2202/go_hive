# CLI 对外面契约对照（维度 7：CLI 子命令 / flags / 退出码）

生成：2026-11-21T16:26:02+08:00 · 工作树基线 `f8aa5fe9` · 生成器 `go_hive/scripts/cli_surface_contract.py` · 契约 `go_hive/contracts/cli-surface.json`

## 方法与范围

- 参考面（reference）：Python `hive`（`core/framework/cli.py`）与 `hive-browser`（`tools/src/gcu/cli.py`）。
- 对齐面（target）：Go `hive`（`go_hive/go-hive/cmd/hive`）与 `hive-browser`（`cmd/hive-browser`）。
- 采集方式：对每个命令路径递归执行 `<cmd> --help`（子命令树、flags、取值占位符、help 文案、help 中声明的默认值、位置参数），再跑一组无副作用退出码探针（`--help`、伪造 flag、缺必填位置参数，外加 browser 的 fail-before-bridge 命令表）。
- 环境固定：`COLUMNS=100`、`HIVE_HOME=<scratch>`、`HIVE_BROWSER_SESSION=cli-surface-contract`、`HIVE_BROWSERBRIDGE_HTTP=http://127.0.0.1:<dead>`、`NO_COLOR=1`。探针不启动服务、不连桥。

## 汇总

| 项 | Python | Go | 共有 | Python 独有 | Go 独有 |
| --- | --- | --- | --- | --- | --- |
| `hive` 命令路径 | 47 | 48 | 47 | 0 | 1 |
| `hive` flag 名 | 44 | 32 | 32 | 12 | 0 |
| `hive-browser` 命令路径 | 27 | 32 | 27 | 0 | 5 |
| `hive-browser` flag 名 | 42 | 49 | 42 | 0 | 7 |
| 退出码探针漂移（总/两侧可探测） | 20 / 1 | | | | |
| 命令行为探针漂移 | 0 | | | | |
| help 文案差异（命令级） | 2 | | | | |
| flag help 文案差异（非阻断） | 68 | | | | |

## `hive`

### 命令树

- 共用命令路径 47 条。
- 仅 Python：无。
- 仅 Go（登记为 Go 扩展或缺口）：`queen-mail-mcp`

| 命令 | Python 子命令 | Go 子命令 |
| --- | --- | --- |
| `<root>` | `serve`, `open`, `queen`, `colony`, `session`, `chat`, `skill`, `debugger`, `mcp`, `janitor` | `serve`, `open`, `queen`, `colony`, `session`, `chat`, `skill`, `debugger`, `mcp`, `janitor`, `queen-mail-mcp` |

### flags

- 全局 flag 名：共有 32（`--cold`, `--colony`, `--debug`, `--defaults`, `--dir`, `--execute`, `--from`, `--help`, `--host`, `--include-tests`, `--input`, `--json`, `--junk`, `--legacy`, `--limit-files`, `--logs-dir`, `--model`, `--name`, `--no-archive`, `--no-open`, `--open`, `--output`, `--pack`, `--port`, `--project-dir`, `--purge-storage`, `--server`, `--session`, `--tiers`, `--verbose`, `--version`, `--yes`）。
- Python 独有：`--args`, `--as-cli`, `--available`, `--command`, `--cwd`, `--description`, `--env`, `--set`, `--set-header`, `--socket-path`, `--transport`, `--url`。
- Go 独有：无。

| 命令 | 项 | 明细 |
| --- | --- | --- |
| `mcp add` | Python 独有 flag | `--args`, `--as-cli`, `--command`, `--cwd`, `--description`, `--env`, `--from`, `--name`, `--socket-path`, `--transport`, `--url` |
| `mcp config` | Python 独有 flag | `--set`, `--set-header` |
| `mcp health` | Python 独有 flag | `--json` |
| `mcp info` | Python 独有 flag | `--json` |
| `mcp install` | Python 独有 flag | `--transport`, `--version` |
| `mcp list` | Python 独有 flag | `--available`, `--json` |
| `mcp search` | Python 独有 flag | `--json` |
| `skill doctor` | 默认值（help 声明）不一致 | `--project-dir`：Python `None` vs Go `current directory` |
| `skill fork` | 默认值（help 声明）不一致 | `--project-dir`：Python `None` vs Go `current directory` |

### 位置参数

| 命令 | Python 位置参数 | Python 必填 | Go 位置参数 | Go 必填 |
| --- | --- | --- | --- | --- |
| `mcp` | （无） | 必填：（无） | `command` | 必填：`command` |
| `mcp config` | `name` | 必填：`name` | （无） | 必填：（无） |
| `mcp disable` | `name` | 必填：`name` | （无） | 必填：（无） |
| `mcp enable` | `name` | 必填：`name` | （无） | 必填：（无） |
| `mcp health` | `name` | 必填：（无） | （无） | 必填：（无） |
| `mcp info` | `name` | 必填：`name` | （无） | 必填：（无） |
| `mcp install` | `name` | 必填：`name` | （无） | 必填：（无） |
| `mcp mode` | `name` | 必填：`name` | （无） | 必填：（无） |
| `mcp remove` | `name` | 必填：`name` | （无） | 必填：（无） |
| `mcp run` | `name`, `mcp2cli_args` | 必填：`name` | （无） | 必填：（无） |
| `mcp search` | `query` | 必填：`query` | （无） | 必填：（无） |
| `mcp update` | `name` | 必填：（无） | （无） | 必填：（无） |

### 退出码

逐命令探针（`--help`、伪造 flag、缺必填位置参数）：

| 探针 | Python | Go | 备注 |
| --- | --- | --- | --- |
| `missing_required mcp config` | 2 | None | Go 侧未生成该探针：`hive mcp <verb>` 的 help 折叠为组 help，位置参数未声明 |
| `missing_required mcp disable` | 2 | None | Go 侧未生成该探针：`hive mcp <verb>` 的 help 折叠为组 help，位置参数未声明 |
| `missing_required mcp enable` | 2 | None | Go 侧未生成该探针：`hive mcp <verb>` 的 help 折叠为组 help，位置参数未声明 |
| `missing_required mcp info` | 2 | None | Go 侧未生成该探针：`hive mcp <verb>` 的 help 折叠为组 help，位置参数未声明 |
| `missing_required mcp install` | 2 | None | Go 侧未生成该探针：`hive mcp <verb>` 的 help 折叠为组 help，位置参数未声明 |
| `missing_required mcp mode` | 2 | None | Go 侧未生成该探针：`hive mcp <verb>` 的 help 折叠为组 help，位置参数未声明 |
| `missing_required mcp remove` | 2 | None | Go 侧未生成该探针：`hive mcp <verb>` 的 help 折叠为组 help，位置参数未声明 |
| `missing_required mcp run` | 2 | None | Go 侧未生成该探针：`hive mcp <verb>` 的 help 折叠为组 help，位置参数未声明 |
| `missing_required mcp search` | 2 | None | Go 侧未生成该探针：`hive mcp <verb>` 的 help 折叠为组 help，位置参数未声明 |
| `unknown_flag mcp run --no-such-flag-9f3` | 2 | 1 | 退出码不一致 |
| `unknown_flag queen-mail-mcp --no-such-flag-9f3` | None | 2 | Go 扩展命令，Python 侧无对应 |

其中单侧可探测（另一侧 help 未声明该命令的该参数/命令，属探测面差异）10 条；两侧都可探测且退出码不同 1 条。

命令行为探针：

- 无差异。

### help 关键行差异（非阻断，逐项记录）

| 命令 | 字段 | Python | Go |
| --- | --- | --- | --- |
| `mcp run` | text | Run an installed MCP server's tools as commands, through mcp2cli. Examples: hive mcp run NAME --list; hive mcp run NAME TOOL --help; hive m… | Manage MCP servers |

## `hive-browser`

### 命令树

- 共用命令路径 27 条。
- 仅 Python：无。
- 仅 Go（登记为 Go 扩展或缺口）：`click`, `press`, `scroll`, `type`, `type-focused`

| 命令 | Python 子命令 | Go 子命令 |
| --- | --- | --- |
| `<root>` | `help`, `setup`, `status`, `stop`, `open`, `navigate`, `reload`, `interact`, `select`, `upload`, `screenshot`, `evaluate`, `script`, `tab`, `page`, `dialog` | `help`, `setup`, `status`, `stop`, `open`, `navigate`, `reload`, `interact`, `select`, `upload`, `screenshot`, `evaluate`, `script`, `click`, `type`, `type-focused`, `press`, `scroll`, `tab`, `page`, `dialog` |

### flags

- 全局 flag 名：共有 42（`--action`, `--args`, `--auto-snapshot-mode`, `--background`, `--browser-profile`, `--coordinate`, `--duration`, `--file`, `--full-page`, `--head`, `--height`, `--help`, `--intent`, `--js`, `--json`, `--level`, `--mode`, `--modifiers`, `--no-annotate`, `--no-clear-first`, `--no-insert-text`, `--prompt-text`, `--region`, `--repeat`, `--script`, `--scroll-amount`, `--scroll-direction`, `--selector`, `--skill`, `--start-coordinate`, `--start-selector`, `--tab`, `--text`, `--timeout-ms`, `--timeout-s`, `--trigger-selector`, `--value`, `--wait-after-ms`, `--wait-for-selector`, `--wait-for-text`, `--wait-until`, `--width`）。
- Python 独有：无。
- Go 独有：`--amount`, `--bridge`, `--button`, `--delay-ms`, `--direction`, `--double`, `--profile`。

| 命令 | 项 | 明细 |
| --- | --- | --- |
| `<root>` | Go 独有 flag | `--profile` |
| `dialog` | Python 独有 flag | `--json` |
| `dialog respond` | Go 独有 flag | `--bridge`, `--profile` |
| `dialog respond` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `evaluate` | Go 独有 flag | `--bridge`, `--profile` |
| `evaluate` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `help` | Go 独有 flag | `--bridge`, `--profile` |
| `interact` | Go 独有 flag | `--bridge`, `--profile` |
| `interact` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `navigate` | Go 独有 flag | `--bridge`, `--profile` |
| `navigate` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `open` | Go 独有 flag | `--bridge`, `--profile` |
| `page` | Python 独有 flag | `--json` |
| `page console` | Go 独有 flag | `--bridge`, `--profile` |
| `page console` | 可选值集合不一致 | `--level`：Python `None` vs Go `['error', 'warn', 'log', 'all']` |
| `page console` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `page html` | Go 独有 flag | `--bridge`, `--profile` |
| `page html` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `page resize` | Go 独有 flag | `--bridge`, `--profile` |
| `page resize` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `page shadow-query` | Go 独有 flag | `--bridge`, `--profile` |
| `page shadow-query` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `page snapshot` | Go 独有 flag | `--bridge`, `--profile` |
| `page snapshot` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `page text` | Go 独有 flag | `--bridge`, `--profile` |
| `page text` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `reload` | Go 独有 flag | `--bridge`, `--profile` |
| `reload` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `screenshot` | Go 独有 flag | `--bridge`, `--profile` |
| `screenshot` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `script` | Go 独有 flag | `--bridge`, `--profile` |
| `script` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `select` | Go 独有 flag | `--bridge`, `--profile` |
| `select` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `setup` | Go 独有 flag | `--bridge`, `--profile` |
| `status` | Go 独有 flag | `--bridge`, `--profile` |
| `status` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |
| `stop` | Go 独有 flag | `--bridge`, `--profile` |
| `tab` | Python 独有 flag | `--json` |
| `tab activate` | Go 独有 flag | `--bridge`, `--profile`, `--tab` |
| `tab close` | Go 独有 flag | `--bridge`, `--profile`, `--tab` |
| `tab list` | Go 独有 flag | `--bridge`, `--profile`, `--tab` |
| `upload` | Go 独有 flag | `--bridge`, `--profile` |
| `upload` | 默认值（help 声明）不一致 | `--tab`：Python `None` vs Go `this profile's active tab` |

### 位置参数

| 命令 | Python 位置参数 | Python 必填 | Go 位置参数 | Go 必填 |
| --- | --- | --- | --- | --- |
| `dialog respond` | （无） | 必填：（无） | `action` | 必填：`action` |

### 退出码

逐命令探针（`--help`、伪造 flag、缺必填位置参数）：

| 探针 | Python | Go | 备注 |
| --- | --- | --- | --- |
| `missing_required click` | None | 2 | Go 扩展命令，Python 侧无对应 |
| `missing_required dialog respond` | None | 2 | Python 侧未生成该探针（argparse 把 choices 渲染成无名位置参数） |
| `missing_required press` | None | 2 | Go 扩展命令，Python 侧无对应 |
| `missing_required type` | None | 2 | Go 扩展命令，Python 侧无对应 |
| `unknown_flag click --no-such-flag-9f3` | None | 2 | Go 扩展命令，Python 侧无对应 |
| `unknown_flag press --no-such-flag-9f3` | None | 2 | Go 扩展命令，Python 侧无对应 |
| `unknown_flag scroll --no-such-flag-9f3` | None | 2 | Go 扩展命令，Python 侧无对应 |
| `unknown_flag type --no-such-flag-9f3` | None | 2 | Go 扩展命令，Python 侧无对应 |
| `unknown_flag type-focused --no-such-flag-9f3` | None | 2 | Go 扩展命令，Python 侧无对应 |

其中单侧可探测（另一侧 help 未声明该命令的该参数/命令，属探测面差异）9 条；两侧都可探测且退出码不同 0 条。

命令行为探针：

- 无差异。

### help 关键行差异（非阻断，逐项记录）

| 命令 | 字段 | Python | Go |
| --- | --- | --- | --- |
| `<root>` | text | Drive the Hive browser (Beeline extension) from the terminal. Replaces the browser_* MCP tools; agents invoke it via terminal_exec with --j… | Drive the Hive browser (Hive Browser Bridge extension) from the terminal. Replaces the retired browser_* MCP tools; agents invoke it with -… |

## 默认值核对

- 方法：regex scan of argSpec{Default:} (Go CLI) vs add_argument(default=) (Python CLI)；只比较两侧都声明为字面量的 flag（符号/常量另有标注）。
- 两侧都声明且为字面量：4 个 flag，其中值不同 0 个；一侧为符号常量/表达式（不参与等值比较）：1 个。

已核对且一致：

| flag | 默认值（两侧相同） |
| --- | --- |
| `--host` | `127.0.0.1` |
| `--mode` | `default` |
| `--server` | `http://127.0.0.1:8787` |
| `--wait-until` | `load` |

## 已修 / 保留

- 已修：`hive-browser` 的「桥不可达 + 无本地 tab」退出码：upload/reload/navigate/select/page text/screenshot 此前在 requireTab 的本地预检处直接返回 3（Browser not started），上游先查连通性、返回 2（not connected）。本次在 requireTab 前加 `GET /health` 连通性探测（`internal/cli/browser_cli.go::bridgeConnected`），无桥时 2/2 对齐；桥在且 extension 已连、只是没有 tab 时仍为 3/3。回归 `TestBrowserCLINotConnectedBeforeNotStarted`（R07 家族收口）。
- 已修：`hive mcp` 裸调用退出码 0 → 2：上游 argparse 视子命令为必填（usage error，exit 2，stderr 带 usage + required: command）；Go 此前把空 argv 当 help 打到 stdout 并 exit 0。改为 `missing-command` 分支（`internal/mcpregistry/cli.go`），`hive mcp --help` / `hive mcp help` 仍为 0。回归 `TestMCPBareInvocationIsUsageError`。
- 已修：help 渲染 `padRight` 分隔符：label 达到/超过 22 列时 help 文本被直接粘在 label 后（`--limit-files LIMIT_FILESMaximum ...`、`--coordinate COORDINATE[x,y] ...`、`--project-dir PROJECT_DIRProject ...`），改为至少保留 1 个空格（`internal/cli/args.go`）。影响 debugger、page coordinate/start-coordinate、skill doctor/fork/list/info 等命令的可读性。回归 `TestHelpKeepsLabelSeparator`。
- 已核对：`--timeout-ms` 逐命令默认值（人工核对源码 + help）：navigate 30000、upload 30000、screenshot 5000、page text 5000、interact 无默认（Python None / Go 0，均交给 bridge 侧语义），两侧一致。
- 已核对：`--host` 127.0.0.1、`--server` http://127.0.0.1:8787、`--mode` default、`--wait-until` load：源码字面量默认值两侧逐值相同（契约 defaults.literal_compared，0 处不一致）。
- 已核对：`--port` 8787、`--limit-files` 200、`--tiers` 1,2,3、`--logs-dir` ~/.hive/llm_logs：两侧 help 文本声明的默认值逐字相同（见 contracts/cli-surface.json 各命令 flags[].default_in_help）。
- 保留：Go 扩展（登记保留，不视为缺口）：`hive-browser` 的 5 个 legacy 动词 click/type/type-focused/press/scroll（含 --amount/--button/--double/--delay-ms/--direction 等 5 个 Go-only flag），上游无对应路径。
- 保留：Go 扩展：`--bridge`（直连 bridge HTTP 的 Go 实现参数，上游是进程内 bridge，无此 flag）。
- 保留：Go 扩展：根级与叶级共享的 `--profile`（上游用 HIVE_BROWSER_SESSION 环境变量选择 profile；Go 同时支持 flag）。
- 保留：Go 扩展：`hive queen-mail-mcp` 子命令（Python 侧只有 `framework.server.queen_mail_mcp` 模块，无 CLI 子命令）。
- 保留：Go 扩展：`hive mcp help` 作为 `--help` 的别名（上游把 help 当未知子命令 → 2）。
- 保留：help 文案：Go 给上游留空的 flag 补了说明（如 `--json`、`--server`、`--project-dir`、interact 的全部参数），并给 `--tab` 标注默认值；信息更多、非行为差异，保留并在差异表中逐条列出。

## 高风险待办（未改，附估算）

- 【中风险/结构缺口】`hive mcp <verb> --help` 在 Go 侧折叠为组 help（`MCPMain` 透传的 14 个动词没有 per-command help）。后果：上游每个动词的 usage/flags 在 Go help 面不可见（`mcp list --available/--json`、`mcp install --version/--transport`、`mcp add` 的 11 个 flag、`mcp config --set/--set-header` 等 12 个 Python-only flag 全来自这里），且 `hive mcp <verb>` 缺必填位置参数时的 2 无法被探针覆盖。估算：14 个动词 × help 表 ≈ 120–160 行 + 8–10 条回归 ≈ 0.5–1 人日；风险中（help 输出整体替换，需逐字对齐上游文案）。
- 【低中风险/错误路径】`hive mcp run --unknown-flag`（未给 name）Go 把 `--unknown-flag` 当 mcp2cli 参数透传 → exit 1，上游 argparse → 2；同类还有 install/add/info/config/search/update 等把 flag 当 name 的分支。修法：解析阶段对「必填 name 以 `-` 开头」统一返回 usage error。估算：10 个解析函数各 3–5 行 + 1–2 条回归 ≈ 2–3 小时。
- 【低风险/批量】help 文案逐字对齐：interact/script/screenshot/navigate 等 30+ 条 help 文本与上游措辞不同（部分上游为空）。逐条改写收益低、易引入回归，建议单独车道按命令分批。估算 ≈ 1 人日。
- 【既定口径】`hive --version`：Python 1.0.14 / Go 1.0.26-go.1，两侧版本号独立演进，不进契约、不阻断。
- 【渲染差异，非缺口】`hive-browser dialog respond` 的位置参数：argparse 用 choices 渲染成无名的 `{accept,dismiss}`，Go 命名为 `action`；同一槽位、行为一致（缺参均 exit 2），契约中按渲染差异记录，不改代码。
