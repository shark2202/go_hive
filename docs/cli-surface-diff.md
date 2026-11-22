# CLI 对外面契约对照（维度 7：CLI 子命令 / flags / 退出码）

生成：2026-11-21T22:42:29+08:00 · 工作树基线 `00eeecc8` · 生成器 `go_hive/scripts/cli_surface_contract.py` · 契约 `go_hive/contracts/cli-surface.json`

## 方法与范围

- 参考面（reference）：Python `hive`（`core/framework/cli.py`）与 `hive-browser`（`tools/src/gcu/cli.py`）。
- 对齐面（target）：Go `hive`（`go_hive/go-hive/cmd/hive`）与 `hive-browser`（`cmd/hive-browser`）。
- 采集方式：对每个命令路径递归执行 `<cmd> --help`（子命令树、flags、取值占位符、help 文案、help 中声明的默认值、位置参数），再跑一组无副作用退出码探针（`--help`、伪造 flag、缺必填位置参数，外加 browser 的 fail-before-bridge 命令表）。
- 环境固定：`COLUMNS=100`、`HIVE_HOME=<scratch>`、`HIVE_BROWSER_SESSION=cli-surface-contract`、`HIVE_BROWSERBRIDGE_HTTP=http://127.0.0.1:<dead>`、`NO_COLOR=1`。探针不启动服务、不连桥。

## 门禁保护（C11：CLI 外观逐 slot）

- 本页的 CLI 外观**逐 slot 受门禁保护**：`make compat-check`（`make ci` 已包含）的 C11 用 `git archive` + 本地 `go build` 把 `meta.baseline_commit` 复现成旧构建、当前树构建成新构建，核对 `hive --help`、`hive-browser --help`、`hive mcp --help`、`hive-cred --help` 的 usage 行，以及本契约冻结的命令路径集合与每路径 flag 集合（旧构建 vs 新构建、新构建 vs 契约基线、新构建 vs Python）；任一漂移即 FAIL（非 0 退出）。
- 合成漂移自检：`make compat-cli-selftest`（`check_compat_contracts.py --self-test`）注入「删一个 flag」「改一个 usage 行」，必须判定 FAIL；基线副本不得误报。
- 改本页任何 slot（usage 行 / 子命令 / flag）都必须重采本契约，否则 C11 直接 FAIL；全量重采（命令树 + 退出码探针 + help 文案）仍由 `cli_surface_contract.py --check` 在发布时执行。

## 汇总

| 项 | Python | Go | 共有 | Python 独有 | Go 独有 |
| --- | --- | --- | --- | --- | --- |
| `hive` 命令路径 | 47 | 47 | 47 | 0 | 0 |
| `hive` flag 名 | 44 | 44 | 44 | 0 | 0 |
| `hive-browser` 命令路径 | 27 | 27 | 27 | 0 | 0 |
| `hive-browser` flag 名 | 42 | 42 | 42 | 0 | 0 |
| 退出码探针漂移（总/两侧可探测） | 0 / 0 | | | | |
| 命令行为探针漂移 | 0 | | | | |
| help 文案差异（命令级） | 0 | | | | |
| flag help 文案差异（非阻断） | 0 | | | | |

## `hive`

### 命令树

- 共用命令路径 47 条。
- 仅 Python：无。
- 仅 Go：无。

### flags

- 全局 flag 名：共有 44（`--args`, `--as-cli`, `--available`, `--cold`, `--colony`, `--command`, `--cwd`, `--debug`, `--defaults`, `--description`, `--dir`, `--env`, `--execute`, `--from`, `--help`, `--host`, `--include-tests`, `--input`, `--json`, `--junk`, `--legacy`, `--limit-files`, `--logs-dir`, `--model`, `--name`, `--no-archive`, `--no-open`, `--open`, `--output`, `--pack`, `--port`, `--project-dir`, `--purge-storage`, `--server`, `--session`, `--set`, `--set-header`, `--socket-path`, `--tiers`, `--transport`, `--url`, `--verbose`, `--version`, `--yes`）。
- Python 独有：无。
- Go 独有：无。

| 命令 | 项 | 明细 |
| --- | --- | --- |

### 退出码

逐命令探针（`--help`、伪造 flag、缺必填位置参数）：

- 无差异。

命令行为探针：

- 无差异。

## `hive-browser`

### 命令树

- 共用命令路径 27 条。
- 仅 Python：无。
- 仅 Go：无。

### flags

- 全局 flag 名：共有 42（`--action`, `--args`, `--auto-snapshot-mode`, `--background`, `--browser-profile`, `--coordinate`, `--duration`, `--file`, `--full-page`, `--head`, `--height`, `--help`, `--intent`, `--js`, `--json`, `--level`, `--mode`, `--modifiers`, `--no-annotate`, `--no-clear-first`, `--no-insert-text`, `--prompt-text`, `--region`, `--repeat`, `--script`, `--scroll-amount`, `--scroll-direction`, `--selector`, `--skill`, `--start-coordinate`, `--start-selector`, `--tab`, `--text`, `--timeout-ms`, `--timeout-s`, `--trigger-selector`, `--value`, `--wait-after-ms`, `--wait-for-selector`, `--wait-for-text`, `--wait-until`, `--width`）。
- Python 独有：无。
- Go 独有：无。

| 命令 | 项 | 明细 |
| --- | --- | --- |

### 退出码

逐命令探针（`--help`、伪造 flag、缺必填位置参数）：

- 无差异。

命令行为探针：

- 无差异。

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

- 已修：内部扩展从 help 面隐藏（`internal/cli/args.go` 新增 `commandSpec.Hidden` / `argSpec.Hidden`）：隐藏项仍在 usage/commands/options 之外的解析、执行、报错路径上完全可用，只是不渲染进 help。据此隐藏 `hive queen-mail-mcp`、`hive-browser` 的 5 个 legacy 动词（click/type/type-focused/press/scroll，及其 --amount/--button/--double/--delay-ms/--direction）、`--bridge`、`--profile`（上游 argparse.SUPPRESS 的对应物）与 `tab list/close/activate` 的 Go 扩展 `--tab`。重采结果：`hive` 命令路径 47/47、`hive-browser` 27/27，`hive-browser` flag 名 42/42 全局相等，两侧 go_only 全为 0。回归 `internal/cli/args_hidden_test.go`（TestHiddenCommandStaysOutOfHelpButRuns / TestHiddenFlagStaysOutOfHelpButParses / TestHiveRootHelpHidesInternalExtensions / TestBrowserHelpHidesLegacyVerbsAndInternalFlags）。
- 已修：`hive-browser` 的「桥不可达 + 无本地 tab」退出码：upload/reload/navigate/select/page text/screenshot 此前在 requireTab 的本地预检处直接返回 3（Browser not started），上游先查连通性、返回 2（not connected）。本次在 requireTab 前加 `GET /health` 连通性探测（`internal/cli/browser_cli.go::bridgeConnected`），无桥时 2/2 对齐；桥在且 extension 已连、只是没有 tab 时仍为 3/3。回归 `TestBrowserCLINotConnectedBeforeNotStarted`（R07 家族收口）。
- 已修：`hive mcp` 裸调用退出码 0 → 2：上游 argparse 视子命令为必填（usage error，exit 2，stderr 带 usage + required: command）；Go 此前把空 argv 当 help 打到 stdout 并 exit 0。改为 `missing-command` 分支（`internal/mcpregistry/cli.go`），`hive mcp --help` / `hive mcp help` 仍为 0。回归 `TestMCPBareInvocationIsUsageError`。
- 已修：help 渲染 `padRight` 分隔符：label 达到/超过 22 列时 help 文本被直接粘在 label 后（`--limit-files LIMIT_FILESMaximum ...`、`--coordinate COORDINATE[x,y] ...`、`--project-dir PROJECT_DIRProject ...`），改为至少保留 1 个空格（`internal/cli/args.go`）。影响 debugger、page coordinate/start-coordinate、skill doctor/fork/list/info 等命令的可读性。回归 `TestHelpKeepsLabelSeparator`。
- 已修：help 文案逐字对齐（本车道 cli-help-text）：68 条 flag 级差异清零——Go 给上游留空的 flag（`--json`/`--server`/`--tab`/interact 全部参数等）改回留空，措辞不同的 6 条（`evaluate --js`、`page html --head`、`page snapshot --head`、`screenshot --full-page/--timeout-ms`、`script --script/--args`、`open --browser-profile`）改成上游原句，choices 类参数改用 Choices 渲染（`--action {left_click,...}`、`--wait-until {commit,...}`、`--mode`、`--auto-snapshot-mode`、`--scroll-direction`、dialog 的 `{accept,dismiss}` 位置参数），并把 `--json` 等 common flag 排到 leaf flag 之前（上游 parent parser 的声明顺序）。`skill doctor/fork --project-dir` 的 help 声明默认值差异随之消失。回归 `internal/cli/help_layout_test.go`（TestHelpRendersArgparseLayout / TestHelpRepeatsMetavarPerAlias / TestDialogRespondRendersChoicePositional）。
- 已修：argparse 帮助版面移植（新包 `internal/arghelp`，`internal/cli/args.go::helpText` 改为其唯一调用方）：usage 按宽度折行并沿用 argparse 的悬挂缩进；label 列改成 `min(action_max_length + 2, 24)` 动态列宽；位置参数与子命令改用 `positional arguments:` 段（子命令按 argparse 渲染成 `{a,b,c}` + 每条缩进一行），不再输出 Go 私有的 `commands:` 段；help 文本按 `textwrap` 规则折行、续行对齐 help 列；宽度取 `COLUMNS`（缺省 80，终端 tty 时取窗口宽，均减 2 与 argparse 一致）；RawDescription 页面（hive-browser evaluate、hive-cred token/browse/inspect）的 epilog 保持原样。`internal/cli/help_layout_test.go` 以 80 列 golden 固定版式；对照脚本 `go_hive/scripts/cli_help_layout_compare.py` 给出逐命令行级相似度（对齐前后数字见 docs/cli-surface-diff.md 与提交说明）。
- 已修：`hive mcp <verb> --help` per-verb help 落地（原 deferred 结构缺口）：`internal/mcpregistry/help.go` 用 arghelp 渲染 14 个动词各自的 usage/positionals/options 页（含 `add` 的 11 个 flag、`list` 的 `--available`、`config` 的 `--set/--set-header`、`install` 的 `--version/--transport`、`run` 的 mcp2cli 说明），`hive mcp --help` 组页也改成 argparse 的 `positional arguments:` + choice 集。命令级 help 文案差异 2 → 0；mcp 动词的 Python-only flag 差集清零，9 条缺参探针（mcp config/enable/disable/info/install/mode/remove/run/search）与 dialog respond 探针行随之对齐；探针漂移仅剩 `mcp run --unknown-flag` 一条退出码差异（见待办）。回归 `internal/mcpregistry/help_test.go`、`internal/cli/help_layout_test.go::TestMCPVerbHelpRendersOwnPage`。
- 已修：`HIVE_CRED_LOG` 行格式对齐 Python logging：新增 `internal/credentials/log.go`，凭据包的四条日志统一经 sink 输出为 `%(asctime)s %(levelname)-7s [%(name)s] %(message)s`（datefmt `%H:%M:%S`，levelname 左对齐 7 列），logger 名沿用上游的 `framework.credentials.storage` / `framework.credentials.key_storage` / `framework.credentials.envsync`；`hive-cred` 的 `setupCredLogging` 安装该 sink，未设 `HIVE_CRED_LOG` 时仍丢弃（上游 NullHandler 的对应物）。实测同一场景（fresh HIVE_HOME + browse --json）：Python `20:18:42 WARNING [framework.credentials.storage] Generated new encryption key. ...` / Go 同形同列。回归 `internal/credentials/log_test.go`、`internal/cli/cred_log_test.go`。
- 已修：非 tracker 异常呈现对齐：`internal/tracker/errors.go::Run` 此前把非 `*CLIError` 的错误洗成 `domain_error`（文本单行 / `--json` 出 envelope），而上游 `framework/trackerdb/errors.py::run` 只捕获 TrackerError，其余异常以未捕获 traceback + exit 1 结束、`--json` 也不出 envelope。Go 改为：非 `*CLIError` 时向 stderr 打印 Python 形状的 `Traceback (most recent call last):` + 真实 Go 帧（`File "…", line N, in func`，外层在前）+ `<类型>: <消息>`，exit 1，stdout 为空。另外三个入口（`cmd/hive`、`cmd/hive-browser`、`cmd/hive-cred`）加 `cli.GuardMain`：panic 原先由 Go runtime 以 `panic: …` + 栈 + exit 2 结束，现保留真实栈但改为同一 traceback 形状 + exit 1（Python 未捕获异常的退出码）。实测：panic 前 exit 2 / 后 exit 1；未分类错误前 `error: …`+`domain_error` envelope / 后 traceback+exit 1+空 stdout。回归 `internal/tracker/errors_test.go::TestUnclassifiedErrorCrashesLikePython`、`internal/cli/crash_test.go`。
- 已修：CLI usage 错误形状对齐（本车道 cli-usage-error-shape）：把 `hive` / `hive-cred` / `hive-browser` 的解析失败输出改成 argparse 的两行形状——先打「责任页」的 usage 块（按宽度折行），再打 `prog: error: <消息>`（`hive-cred` 为 `error: <prog>: <消息>`，上游自写的 `_Parser.error`），exit 2/6 不变。为此在 `internal/cli/args.go` 里移植 argparse 的三条机制：(1) 未知选项/多余位置参数不再就地报错，而是作为 argparse 的「extras」收集，整棵树解析完后由最外层 parse_args 报 `unrecognized arguments`，因此**总是打根页**（叶子上的坏 flag 也打 `hive` 根 usage）；(2) 缺必填（位置参数或子命令）由声明它的解析器在 extras 之前报错，所以「未知 flag + 缺子命令」时报缺子命令、打该组自己的页；(3) `allow_abbrev` 默认开启的长选项唯一前缀解析与 ambiguous option 报错、负数字面量按位置参数处理、`--flag` 后跟选项样式的 token 时报 `expected one argument`、`--` 分隔符语义（含「`--` 后被喂了子命令时 choice 变成 `--` 本身」的边界）。另外把 invalid choice / ignored explicit argument 的 `%q` 改成 argparse 的 `'…'` 单引号、错误信息用缩写解析后的完整选项名。`hive-browser` 的 `interact` 缺必填 `--action` 改为 argparse 形状（此前是自写文案），`evaluate` 缺 `--js` 的文案对齐上游 `error: evaluate requires JavaScript via --js (inline, @file, or -)`。回归 `internal/cli/usage_error_shape_test.go`（9 类形状逐字节 + `--bogus --version` 优先级 + `--vers` 前缀解析 + `hive mcp list --bogus` 根页归属）、`internal/cli/debugger_test.go::TestDebuggerInvalidPortIsUsageError`（改为断言完整 usage+前缀）。
- 已修：`hive mcp` 解析错误形状对齐（`internal/mcpregistry/cli.go`）：`hive mcp` 的 argparse 树此前只打一行裸消息（`invalid choice: …` 无前缀无 usage）。本次给 mcp 引擎加 `UsageError{Prog,Msg,Root}` + `mcpParse` extras 收集器，与 `internal/cli` 同一套语义：组级未知 flag 与缺 `mcp_command` 的顺序、动词页（install/info/mode/run/search/config…）的缺必填页、动词上未知 flag 与多余位置参数回**根页**（调用方经 `CLI.RootUsage/RootProg` 注入 `hive` 根 usage，`NewRunner` 里接线）。`mode a` 只列缺的 `mode`（argparse 只列真正缺的 action），`mcp run --bogus`（无 name）从「透传 → exit 1」改为上游的 usage error（exit 2），原 deferred 项随之关闭；`list --` / `init --` 这类「没有位置参数可填」的 `--` 归入 extras，与原实现一致。回归 `internal/mcpregistry/usage_shape_test.go`（7 类形状 + run 缺参）与 `internal/cli/usage_error_shape_test.go::TestMCPVerbUnknownFlagPrintsRootPage`。
- 已核对：usage 错误面双跑（本车道，COLUMNS=100，同一 pinned 环境，stdout/stderr/exit 三面逐字节）：`hive` 91 例、`hive-cred` 29 例、`hive-browser` 14 例，合计 134 例；126 例三面完全相同，8 例差异均不属五类解析错误（`--version` 版本号载荷 1、桥未连接信封 2、Go 日志时间戳前缀与 JSON 分隔符 3、`chat` 传输错误文案 1、`help <unknown>` 未捕获 traceback 1）。五类覆盖：根级未知参数、leaf 级未知参数、leaf 缺必填（位置参数/必填 flag/子命令）、非法 choice、多余位置参数；边界另含 `allow_abbrev` 前缀/歧义、`--` 分隔符、负数字面量、`expected one argument`（值位是选项样式 token）、help/version 优先级。
- 已核对：`--timeout-ms` 逐命令默认值（人工核对源码 + help）：navigate 30000、upload 30000、screenshot 5000、page text 5000、interact 无默认（Python None / Go 0，均交给 bridge 侧语义），两侧一致。
- 已核对：`--host` 127.0.0.1、`--server` http://127.0.0.1:8787、`--mode` default、`--wait-until` load：源码字面量默认值两侧逐值相同（契约 defaults.literal_compared，0 处不一致）。
- 已核对：`--port` 8787、`--limit-files` 200、`--tiers` 1,2,3、`--logs-dir` ~/.hive/llm_logs：两侧 help 文本声明的默认值逐字相同（见 contracts/cli-surface.json 各命令 flags[].default_in_help）。
- 已核对：help 版面实测（COLUMNS=80/100，`go_hive/scripts/cli_help_layout_compare.py`）：`hive` 33 条与 `hive-browser` 27 条命令的 `--help` 与 Python 逐字节相同；`hive-cred` 4 条同。
- 保留：已隐藏的内部扩展（不出现在 help，仍可调用）：`hive-browser` 的 5 个 legacy 动词 click/type/type-focused/press/scroll（含 --amount/--button/--double/--delay-ms/--direction 等 5 个 flag）——包装旧 bridge 命令的兼容入口，上游无对应路径；隐藏后 `hive-browser --help` 与 27 条上游路径逐条对齐。
- 保留：已隐藏的内部扩展（不出现在 help，仍可调用）：`--bridge`（直连 bridge HTTP 的 Go 实现参数，上游是进程内 bridge，无此 flag）。
- 保留：已隐藏的参数（不出现在 help，仍可调用）：`--profile`——上游同款行为是 argparse.SUPPRESS（`tools/src/gcu/cli.py:104`），Go 用 Hidden 对齐，两侧 help 都不渲染。
- 保留：已隐藏的内部扩展（不出现在 help，仍可调用）：`hive queen-mail-mcp` 子命令（Python 侧只有 `framework.server.queen_mail_mcp` 模块，无 CLI 子命令；Claude Code queen 的 runner 依赖该入口）。
- 保留：已隐藏的内部扩展（不出现在 help，仍可调用）：`tab list/close/activate` 上的 `--tab`（上游 tab 子解析器不声明该 flag；Go 保留按 tab id 过滤/定位的扩展行为）。
- 保留：Go 扩展：`hive mcp help` 作为 `--help` 的别名（上游把 help 当未知子命令 → 2；`mcp` 在根 help 里仍是单条透传命令，不受影响）。
- 保留：日志内容的安全偏差（保留）：Python 的 key 生成警告把 `HIVE_CREDENTIAL_KEY=<值>` 写进日志，Go 只写变量名、不落密钥值；行格式（时间/级别/logger 名）已逐字节对齐，仅消息尾部少该值。
- 保留：`hive-browser` 的 usage/label 文案与上游一致，但 Go 在宽终端（cobra 场景外）由 `COLUMNS`/tty 决定折行；无 tty 且无 `COLUMNS` 时回落 80，与 argparse 的 shutil 回退一致。

## 高风险待办（未改，附估算）

- 【低风险/错误路径，非本车道】`hive-browser` 的「桥未连接」错误面仍是两条路：Python 走 `framework/…` 的 BrowserError → 无 `--json` 时 `ok: False`/`error: …` 打到 **stdout**（exit 2），Go 打 `hive-browser: the Hive browser extension isn't connected …` 到 **stderr**（exit 2）。属浏览器错误信封车道，解析形状已对齐；修法：把 not_connected 统一到 gcu 的 `errors.run` 输出（文本走 stderr 前缀 `error:`、`--json` 走 envelope）≈ 2–4 小时。
- 【既定口径】`hive --version`：Python 1.0.15 / Go 1.0.26-go.1，两侧版本号独立演进，不进契约、不阻断。
- 【既有文案，非本车道】`hive-browser help <topic>` 的未知主题错误信息保留 Go 措辞（`hive-browser: no such command … — run hive-browser help for the full list`）；上游是未捕获 `BrowserError` traceback（exit 1），退出码一致、形状不同，文案差异不在 help 面。
- 【机制差异，无法逐字】argparse 的折行宽度取 `shutil.get_terminal_size()`（`COLUMNS` → tty → 80，均减 2），Go 的 `internal/arghelp` 已按同一规则实现并在 COLUMNS=80/100 下逐字节一致；无 tty 且无 `COLUMNS` 时两侧同样回落 80，故此差异只在「有 tty 且窗口宽不同」的交互场景出现，不属可冻结契约。
