# schema-residuals 车道报告（round 9）

> 车道：worktree `.wt/schema-residuals`，分支 `schema-residuals`，base `f9eb5720`（2026-11-23）。
> 目标：机械收口 `browser_setup` / `task_create` 两条 schema 残差；12 条 lifecycle 工具把契约
> 参考从 Tool Library 层改记到**运行时层**并重取基线；重冻受影响契约 + 全量门禁。
> 结果：**14 条归一化残差 → 0**（会话面 48/48 归一化一致；raw 40/48，余 8 条纯表示差）。

## 1. 结论速览

| 维度 | 修前（round 8，§17.3） | 修后（round 9） |
| --- | --- | --- |
| 归一化残差 | 14（12 lifecycle + `browser_setup` + `task_create`） | **0** |
| 会话面 present | 47/54（含 4 条反射层计入分母） | **48/59**（分母剔除反射层 4 条；未移植 7 条） |
| description 逐字 | 47/47 | **48/48** |
| input_schema raw | 25/47 | **40/48** |
| input_schema 归一化 | 33/47 | **48/48** |

## 2. 14 条逐条处置表

| # | 工具 | 类别 | 处置 | 依据 |
| --- | --- | --- | --- | --- |
| 1 | `browser_setup` | Go 裸定义多 `additionalProperties: false` | **已对齐**：从 Go 定义删除，与上游 raw 逐字（`{type, properties:{}, required:[]}`） | `internal/tools/integrations/browser_setup.go` + 断言测试；`TestToolContractFreeze` 不再列出 |
| 2 | `task_create` | 会话级 pivot overlay（裸 catalogue 2 参数） | **已对齐（确定性镜像）**：Python 运行时抓包与冻结参考逐字节相同；recheck 安装 `enableTaskPivot(DM)` 后比较 4 参数定义 | `compare-python-runtime-vs-go.json` → `task_create`；`tool_contract_freeze_test.go` |
| 3 | `get_playbook_status` | lifecycle 层（参考空对象） | **已对齐**：参考改记运行时层 + Go raw 逐字 | 12/12 pin 测试 |
| 4 | `get_worker_status` | 同上 | **已对齐** | 同上 |
| 5 | `inject_message` | 同上 | **已对齐** | 同上 |
| 6 | `list_playbook_runs` | 同上 + Go 缺请求层 `required: []` | **已对齐**：补 `required: []`（Go 定义 + parity manifest，生成器同步） | `colony/playbook_tools.go`、`gen_tool_contract_parity.py` |
| 7 | `remove_trigger` | 同上 | **已对齐** | 12/12 pin 测试 |
| 8 | `reply_to_worker` | 同上 | **已对齐**（相位事实登记：上游任何相位表都不广告给模型） | `py_runtime_schema_probe.py` 相位输出 |
| 9 | `run_playbook` | 同上 + Go 缺 `required: []` | **已对齐**：补 `required: []` | `colony/playbook_tools.go` |
| 10 | `run_worker` | 同上 + 6 处参数描述为本移植缩写 | **已对齐**：参数描述改 Python 运行时文本逐字 + `required: []` | `colony/queen_tools.go` |
| 11 | `set_trigger` | lifecycle 层 | **已对齐** | 12/12 pin 测试 |
| 12 | `stop_playbook` | 同上 | **已对齐** | 同上 |
| 13 | `stop_worker` | 同上 | **已对齐** | 同上 |
| 14 | `update_worker_profile` | 同上（相位事实登记：任何相位不广告） | **已对齐** | 同上 |

无「待定」项。8 条 raw 表示差（`attach_file` `pdf_read` `terminal_job_logs` `terminal_pty_run`
数值字面拼写；`edit_file` `read_file` `terminal_job_start` `terminal_pty_open` 参考层
`session_cwd`）由既有归一化吸收，语义等价（§18.6/§18.8）。

## 3. 命令与原始输出（可复现）

### 3.1 Python 运行时层抓取（无 LLM）

```sh
/Users/mac/hive/.venv/bin/python \
  go_hive/docs/e2e-assets/schema-residuals/py_runtime_schema_probe.py
```

输出（`python-probe.txt` 全文；sha256 见 §4）：

```
registered lifecycle tools: 16
captured 12/12 lifecycle tools + task_create pivot schema
wrote .../schema-residuals/python-runtime-tools.json (55742 bytes)
  get_playbook_status      sha256(parameters)=5e46b50c8112e01b phase={'independent': False, 'colony': True}
  ...
  run_worker               sha256(parameters)=271c66222218e164 phase={'independent': False, 'colony': True}
  ...
  reply_to_worker          sha256(parameters)=dee4c499d85efb5c phase={'independent': False, 'colony': False}
  update_worker_profile    sha256(parameters)=30c1e6bf521dfbee phase={'independent': False, 'colony': False}
```

抓取路径：`register_queen_lifecycle_tools`（录音 registry + mock session）→ 16 个注册 Tool 对象；
每个经真实请求层 `framework/llm/litellm.py::LiteLLMProvider._tool_to_openai_format` 投影
（`type`/`properties`/`required`，`required` 缺省时 materialize 为 `[]`）。
`task_create` 另以 `build_session_tools(pivot_handler=DM new_session)` 构造，pivot 描述文本
从 `queen_orchestrator.py::_NEW_SESSION_DESC` 用 `ast` 原样抽取；抓包与冻结参考
parameters/description 均逐字节相同（`compare-python-runtime-vs-go.json`）。

### 3.2 重冻 + 自洽校验

```sh
source /Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh
/Users/mac/hive/.venv/bin/python go_hive/scripts/refresh_lifecycle_runtime_reference.py
python3 go_hive/scripts/freeze_tool_contract.py check
```

`refresh.txt`（全文）：

```
rebased 12 lifecycle references onto the runtime layer
wrote .../go_hive/contracts/tool-definitions.json
wrote .../go_hive/contracts/tool-types.proto
wrote .../go_hive/docs/tool-contract-recheck.json
live recheck: present 48/59, description 48, raw 40, normalized 48
normalized residual: []
raw residual: ['attach_file', 'edit_file', 'pdf_read', 'read_file', 'terminal_job_logs',
 'terminal_job_start', 'terminal_pty_open', 'terminal_pty_run']
```

`freeze-check.txt`（exit 0）：

```
OK: 59 reference tools, 103 go-only tools, manifest/proto/doc consistent
```

### 3.3 live recheck（C 段，权威口径）

```sh
go test ./internal/cli -run TestToolContractFreeze -count=1 -v
```

`freeze-report.txt` 关键行：

```
A. reference vs live: name 48/59, description 48/59, input_schema 40/59, missing in Go 7, extra in Go 131
A. missing in Go (7): [ask_user collect_result credentials csv_sql search_tools sentinel_setup suggest_colony]
A. reflection-layer entries (not a session surface) (4): [delete_memory_file list_memory_files read_memory_file write_memory_file]
A. input_schema differs (8): [attach_file edit_file pdf_read read_file terminal_job_logs terminal_job_start terminal_pty_open terminal_pty_run]
C. unified metric (authoritative): session reference 48/59 (4 reference entries outside the session surface),
   description 48/48, schema raw 40/48, schema normalized 48/48
C. schema differs after normalization:（无 —— 残差 0）
```

### 3.4 运行时层钉住测试

```sh
go test ./internal/cli -run TestQueenLifecycleRuntimeSchemasMatchFrozenContract -count=1 -v
```

```
--- PASS: TestQueenLifecycleRuntimeSchemasMatchFrozenContract (0.01s)
```

（12 个 `source=python:lifecycle_runtime` 条目逐字比对；少一条即失败。）

## 4. 证据文件与 sha256

| 文件 | 说明 | sha256 |
| --- | --- | --- |
| `py_runtime_schema_probe.py` | 抓取探针（注册路径 + 请求层投影 + 相位 + task_create pivot） | — |
| `python-runtime-tools.json` | 抓取产物（16 个注册工具、12 条重点、task_create 两形态） | `77ddce1f…`（见下） |
| `go-live-surface.json` | Go live 会话面 dump（177 条，DM pivot 已安装） | `a6b037b9…`（见下） |
| `compare-python-runtime-vs-go.json` | 修前/修后逐工具比对（12/12 aligned；含 sha256） | — |
| `python-probe.txt` / `refresh.txt` / `freeze-check.txt` | 命令原始输出 | — |

完整 sha256：
- `python-runtime-tools.json` = `77ddce1fdb1596ef2482eb10c3f01132b1392b1cedd8dbde0add37b65774ec09`
- `go-live-surface.json` = `a6b037b90f6714672c6749e79d583c2b31ad79d3105f22ab26e3b563342b3201`

（`go-live-surface.json` 由临时探针生成后复制至此；探针本身未入库，复现方式：安装 DM pivot 的
`newSessionToolCatalogue` + `QueenLifecycleToolDefs` dump，等价于 `TestToolContractFreeze` 的
live 装配。）

## 5. 门禁（全部 exit 0）

| 门禁 | 命令 | 结果 |
| --- | --- | --- |
| gofmt | `gofmt -l internal/ cmd/`（go-hive） | 空 |
| vet | `go vet ./...` | 0 |
| 目标包测试 | `go test ./internal/cli ./internal/tools ./internal/server -count=1` | 0 |
| 改动包测试 | `go test ./internal/colony ./internal/tools/integrations -count=1` | 0 |
| 全量测试 | `go test ./... -count=1` | 0 |
| 契约自洽 | `make tool-contract-check` | 0 |
| 契约对拍报告 | `make tool-contract-report` | 0 |
| 扁平工具面 | `make tool-surface-check` | 0 |
| 浏览器工具定义 | `make browser-tool-check` | 0 |
| parity 注解 | `make parity` | 0（5849/5849） |
| 收尾检查 | `bash go_hive/scripts/merge_postcheck.sh` | 0（gofmt/wiring/C12/arch 全 PASS） |

## 6. 口径变更说明（参考层 → 运行时层）

- 12 条 lifecycle 的参考从 Tool Library 层（name+description、空 schema）改为运行时层：
  注册 `Tool` 对象经 `_tool_to_openai_format` 投影。语义上这才是「模型实际收到的 schema」；
  Tool Library 层仍作为 `layers` 记录维度保留（`python_layers.lifecycle` 计数不变）。
- 契约落点：`contracts/tool-definitions.json` 12 条 `source=python:lifecycle_runtime`、
  `reference_round: 9`、`layer_detail.lifecycle` 记录投影路径/相位/parameters sha256；
  `statistics.python_params` 169→189；`statistics.live_recheck` 与
  `docs/tool-contract-recheck.json` round 9 同步。
- C 段分母修正：反射侧 4 条不再计入 present 分母（旧报法 48/52 → 新报法 48/48）。

## 7. 残留风险

- raw 8 条表示差长期存在（数值字面拼写、参考层 `session_cwd`）；若上游 JSON Schema 结构化
  改造或要求 raw 逐字，需要新一轮（C 段已单列清单，随时可查）。
- `reply_to_worker` / `update_worker_profile` 上游注册但任何相位都不广告（模型收不到）；
  Go 侧保留目录条目。若上游相位表变化，需要重新取相位基线。
- 本抓取以仓库当前 venv 运行（core 已到 1.0.15 合并提交 `265d93d1`）；与冻结参考相关的
  `queen_lifecycle_tools.py` / `playbook_tools.py` / `session_tools.py` / `litellm.py`
  自 `a756b1e5` 未变（`python-runtime-tools.json` `repo.note` 记录注意点），因此基线可比。
- `go-live-surface.json` 是本车道一次性 dump；长期门禁是
  `TestQueenLifecycleRuntimeSchemasMatchFrozenContract`（每次 CI 重算）。
