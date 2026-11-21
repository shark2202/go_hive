# `hive mcp` help / 退出码取证（cli-mcp-help lane）

基线 `main @ 982136f8`，分支 `cli-mcp-help`。本目录是「`hive mcp [<verb>] --help`
逐字对齐 + `mcp run --unknown-flag` 退出码对齐」的原始取证，全部可重放。

## 结论

| 项 | 结果 |
| --- | --- |
| `hive mcp --help` + 14 个 verb `--help` | **15/15 页与 Python argparse 逐字节一致**（stdout，含 usage 换行、两列排版、help 文案、trailing newline） |
| help 退出码 | 15 页两侧均 0 |
| 解析错误 / 透传退出码电池 | 34 例，33 例两侧一致；1 例为已登记 Go 扩展（见下） |
| `hive mcp run --no-such-flag-9f3` | Python 2 / Go **2**（修复前 Go 1） |
| `hive mcp <verb> --help` help 面（契约） | 12 个 Python-only flag 归零；mcp 全部 15 个路径 usage/positionals/flags/help 文案差异 0 |
| 契约探针漂移 | 11 → **1**（仅剩 `hive-browser dialog respond` 单侧，见 `docs/cli-surface-diff.md`） |

**已登记例外**：`hive mcp help` 是 Go 专属别名（上游 argparse 把 `help` 当未知子命令 →
exit 2，Go 打印组 help → 0）；该扩展在 `docs/cli-surface-alignment.json` 的 `kept` 段。

## 重放

```bash
# 从仓库根目录（需要 .venv 里的 Python 与 Go 工具链）
uv run --no-project python go_hive/docs/e2e-assets/cli-mcp-help/capture.py \
    --repo . --scratch "$TMPDIR/cli-mcp-help-evidence"

# 契约（递归 --help + 探针）与页面决定论
cd go_hive/go-hive && make cli-surface-check
cd ../.. && uv run --no-project python go_hive/scripts/gen_cli_surface_html.py --check
```

`capture.py` 固定与契约相同的环境（`COLUMNS=100`、scratch `HIVE_HOME`、
`NO_COLOR=1`、`TERM=dumb`），因此两侧输出可直接逐字节比较。

## 文件

| 文件 | 内容 |
| --- | --- |
| `capture.py` | 取证脚本（构建 Go 二进制、双跑、写本目录） |
| `help-python/<page>.txt`、`help-go/<page>.txt` | 15 页原始 stdout（`group` + 14 verbs） |
| `help-diff.txt` | 逐页比对结果（本次 15/15 identical） |
| `exit-codes.json` | 34 例 argv 的 Python/Go 退出码、stderr 头、例外标注 |
| `summary.json` | 汇总（pages/battery/pass） |
| `cli-surface-check.txt` | `make cli-surface-check` 输出（OK） |
| `html-determinism.txt` | `docs/cli-surface.html` 两次独立生成 sha256 + `--check` 输出 |

## 本轮修复点

- `internal/mcpregistry/mcp_help.go`：按 argparse.HelpFormatter 复刻 help 渲染
  （usage 换行、`max_help_position=24`、COLUMNS 驱动宽度），14 个 verb 的
  usage/options/positionals 全部逐字对齐；`hive mcp --help` 组页同源。
- `internal/mcpregistry/cli.go`：verb 级 `-h/--help` 打印该 verb 页面；
  `parseRun` 在必填 `name`/REMAINDER 尚未消费时拒绝未知 flag（exit 2），
  `run NAME --help` 仍按 REMAINDER 透传给 mcp2cli；裸 `hive mcp` 的 usage +
  `required: mcp_command` 与 Python 逐字一致。
- golden：`internal/mcpregistry/testdata/mcp-help/*.txt`（15 页），回归
  `TestMCPHelpMatchesPythonArgparse` / `TestMCPRunUnknownFlagExitsTwo` /
  `TestMCPBareErrorMatchesArgparseText`。
