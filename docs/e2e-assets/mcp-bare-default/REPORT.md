# MCP 默认裸名（D3 终选 B′）交付报告

- **日期**：2026-11-23
- **worktree**：`/Users/mac/hive/.wt/mcp-bare-default`（分支 `mcp-bare-default`，基线 `f9eb5720`）
- **决策**：`docs/pending-decisions.md` §D3 —— 用户拍板**终选 B′**：`mcp.namespace` 默认空，默认注册 server 侧裸名；显式非空 namespace 保留旧前缀模式。
- **Python 参考**：`core/framework/loader/tool_registry.py:1053`（`register_mcp_server` 以裸 `mcp_tool.name` 为键）、`:1422`（转换器不改名）；跨 server 同名 first-wins `:993`。

## 1. 模式矩阵

| 输入 | 默认模式（`mcp.namespace` 缺失/空） | 前缀模式（显式 `"mcp.namespace": "mcp"`） |
|---|---|---|
| server 工具 `x-y` | 注册键 `x-y` | `mcp__<server>__x_y` |
| server 工具 `x_y` | 注册键 `x_y`（与 `x-y` 并存） | `mcp__<server>__x_y`（同一键，first-wins） |
| server `a-b` 的 `get-sum` + server `a_b` 的 `get_sum` | 两个裸键并存，各自可调用 | 两个前缀都折叠成 `mcp__a_b__get_sum`；第二个 server `ToolsLoaded=0` |
| 两个 server 同名 `echo_text`（preserve=True） | first-wins：第一个 server 拥有裸键，后者 0 工具 | first-wins：折叠键归第一个 server |
| 同名 + `preserve_existing_tools=False` | 后到覆盖裸键，调用落后者 | 后到覆盖折叠键，调用落后者 |
| 70 字符工具名 | 注册键 70 字符，无截断 | `mcp__srv__` + 70 = 79 字符，无截断 |
| `mcpAliases` | 恒等（空 map，不记录） | `裸名 → 前缀名` 回退别名 |
| `MCPRegistryToolName(ns, srv, tool)` | `ns == ""` → `tool` | `ns != ""` → `NamespacedMCPToolName(ns, srv, tool)` |
| 探活/断连 | 裸键解析（`ProbeForTool` / `KillForTool`） | 前缀键解析 + 裸名回退 |
| provider 边界 | Antigravity `_clean_tool_name` 仅 provider 生效，注册层不动 | 同左 |

实现入口：`internal/tools/mcp_registry.go:MCPRegistryToolName`（默认裸名）与
`NamespacedMCPToolName`（可选前缀）；配置默认值：`internal/cli/mcp_config.go:
readMCPFileSettings`（`Namespace: ""`）。

## 2. 13 条冻结用例实测（默认模式）

命令：

```bash
cd .wt/mcp-bare-default/go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/tools -run \
  'TestMCPBareDefault|TestMCPRegistryToolNameModeMatrix|TestMCPNamespace|TestMCPExecutorLegacy|TestMCPSplitContent' \
  -count=1 -v
```

结果：**PASS（全部 13 条 + 9 条补充钉子）**。

| # | 冻结用例 | Python 探针（`python-output.json`） | Go 默认实测 | 结果 |
|---|---|---|---|---|
| 1 | 单 server `x-y` + `x_y` | `collision_tool_names_in_one_server`: `x-y_registered=true`, `x_y_registered=true` | `TestMCPBareDefaultKeepsDistinctToolNames`：2 键、`ToolsLoaded=2`、各自调用本方 remote 工具 | PASS |
| 2 | `a-b`/`get-sum` + `a_b`/`get_sum` | `collision_cross_server`：两 server 工具并存 | `TestMCPBareDefaultCrossServerNamesSurvive`：`get-sum`→`a-b:get-sum`、`get_sum`→`a_b:get_sum` | PASS |
| 3 | 两 server 同名 `echo_text`（preserve=True） | `collision_cross_server`：first-wins，`a_b` 只剩 `only_b` | `TestMCPBareDefaultSameRemoteNameFirstWins`：`a_b` 0 工具，调用落 `a-b:echo_text` | PASS |
| 4 | 同名 + preserve=False | `collision_call_echo_text_preserve_false = "a_b:echo_text"` | `TestMCPBareDefaultPreserveFalseOverwrites`：单键 `echo_text`，调用落 `a_b:echo_text` | PASS |
| 5 | 70 字符工具名 | `long_name.registered_len = 70` | `TestMCPBareDefaultLongNameRegisteredVerbatim`：1 个 70 字符键，调用 `srv:<70 chars>` | PASS |
| 6 | 仅 `data` 块 | 文本 `"raw-blob"` | `TestMCPSplitContentDataOnlyItemBecomesText` | PASS |
| 7 | 空 `mimeType` | `image_url` `data:;base64,AAA` | `TestMCPSplitContentEmptyMimeTypeStaysImageURL` | PASS |
| 8 | `mimeType: null` | `data:None;base64,AAA` | `TestMCPSplitContentNullMimeTypeDiverges`（已登记 L2 畸形载荷差异） | PASS |
| 9 | `resource_link` | 丢弃 | `TestMCPSplitContentResourceLinkItemDropped`（两侧共同缺口） | PASS |
| 10 | 未知对象 | 丢弃 | `TestMCPSplitContentUnknownItemDropped` | PASS |
| 11 | HTTP legacy 首块 text | `content="a"` | `TestMCPExecutorLegacyContentListFirstTextBlock` | PASS |
| 12 | HTTP 空列表 / None | `"[]"` / `"null"` | `TestMCPExecutorLegacyContentListEmptyAndNil` | PASS |
| 13 | HTTP 首块非 text | JSON 对象（插入序） | `TestMCPExecutorLegacyContentListFirstNonTextBlock`（已登记 L3 键序差异） | PASS |

补充钉子（不属于 13 条计数）：

| 用例 | 钉住的行为 | 结果 |
|---|---|---|
| `TestMCPRegistryToolNameModeMatrix` | 两种模式映射表（裸名/前缀/自定义 namespace） | PASS |
| `TestMCPBareDefaultAliasesStayIdentity` | 裸名模式 `mcpAliases` 为空，`x_y` 不会借折叠别名打到 `x-y` | PASS |
| `TestMCPBareDefaultProbeAndKillUseBareKeys` | 探活/断连用裸键 | PASS |
| `TestMCPNamespacePrefixModeAliasFallbackStillResolves` | 前缀模式裸名回退仍解析到前缀工具 | PASS |
| `TestMCPNamespace*`（5 条原冻结用例） | 显式 `ToolNamespace: "mcp"` 下旧折叠/first-wins/长名行为不变 | PASS |
| `TestMCPRuntimeLoadsToolsIntoSessionCatalogue` | 默认模式会话目录出现 4 个裸名（`echo/add/x-y/x_y`），无 `mcp__demo__*` | PASS |
| `TestMCPRuntimePrefixModeWhenConfigured` | 显式 `mcp.namespace:"mcp"` 时目录只有 `mcp__demo__echo` | PASS |
| `TestMCPNamespaceDefaultsToBare` | 缺 block / 空 block / 空白值都解析为空；显式值保留 | PASS |
| `TestMCPToolNamesUsesLoaderNamespace` | tier 映射：默认裸名 + 自定义 namespace 前缀并存 | PASS |

## 3. 冻结面重冻与 --check

| 产物 | 命令 | 结果 |
|---|---|---|
| `contracts/env-vars.json`、`contracts/config-keys.json`、`docs/env-config-contract-diff.md` | `python3 go_hive/scripts/freeze_env_config_contract.py` | 写入 3 个产物；env audit python=90/go=125/common=84；config 137 键 |
| 同上 `--check` | `python3 go_hive/scripts/freeze_env_config_contract.py --check` | `OK: env-vars.json, config-keys.json and env-config-contract-diff.md are up to date`（exit 0） |
| `docs/architecture/contracts.md` 等 7 个 arch 产物 | `python3 go_hive/scripts/gen_arch.py` | 写入 7 个产物（`tool-schema.mcp-namespace` 改为“默认裸名 + 可选前缀模式”） |
| arch `--check` | `python3 go_hive/scripts/gen_arch.py --check` | `一致：7 个生成物与 model.yaml + go list 完全一致（commit f9eb572042c0）`（exit 0） |
| `contracts/tool-definitions.json` | `make tool-contract-check` | `OK: 59 reference tools, 103 go-only tools, manifest/proto/doc consistent`（exit 0） |
| `internal/cli/tool_surface_flat.json` | `make tool-surface-check` | `is up to date ({'llm_request': 24, 'lifecycle': 16, 'synthetic': 1, 'unique': 39})`（exit 0） |
| `docs/appearance-master-matrix.html`（附带一致性） | `python3 go_hive/scripts/gen_appearance_master_matrix.py --check` | `ok: ... matches appearance-scope-registry.json`（exit 0） |
| `docs/configuration.html` | `python3 go_hive/scripts/gen_configuration_html.py` 然后 `--check` | `wrote 100423 bytes`；`ok: ... matches contracts/config-keys.json`（exit 0） |
| env/config 严格门禁 | `python3 go_hive/scripts/check_env_config_contract.py --strict` | `RESULT: OK — manifests match the tree and the published env audit`（exit 0） |

说明：`tool-definitions.json` 与 `tool_surface_flat.json` **不含动态 MCP 工具名**
（前者只有 Python 侧 `mcp_catalog` 层，后者只提取默认三层 `llm_request/lifecycle/
synthetic`，`mcp_catalog` 明确排除），因此不需要为命名模式改动它们；`--check`
证明零漂移。`mcp.namespace` 的默认值变更落在 `contracts/config-keys.json`
（`default: ""`）与 `docs/env-config-contract-diff.md`。

## 4. 门禁

| 检查 | 命令 | 结果 |
|---|---|---|
| gofmt | `gofmt -l .`（`go-hive/`） | 输出空，exit 0 |
| vet | `go vet ./...` | exit 0 |
| 四包测试 | `go test ./internal/tools ./internal/server ./internal/cli ./internal/colony -count=1` | 4 × `ok`（2.8s / 22.6s / 20.8s / 17.9s），exit 0 |
| 工具面 | `make tool-surface-check` | exit 0（unique 39） |
| parity | `make parity` | `5859/5859`，missing `0`，exit 0 |
| 合并收尾 | `bash go_hive/scripts/merge_postcheck.sh` | exit 0（gofmt clean；wiring 21 文件 68 项 PASS；C12 PASS；arch 7 产物一致） |
| 附加：arch | `make arch-check` | exit 0 |
| 附加：契约 | `make compat-check` | `pass=12 fail=0 skip=0 verdict=PASS`，exit 0 |
| 附加：全模块 | `go test ./... -count=1` | 69 包 `ok`、0 `FAIL`、18 包无测试；`FULLTEST_EXIT=0` |

## 5. 回滚

1. **配置级（推荐，无需改代码）**：`configuration.json` 写
   `"mcp": {"namespace": "mcp"}` → 立即回到前缀 + 折叠 + 裸名回退别名的旧行为。
2. **代码级**：`git revert <本分支提交>`；无数据迁移、无磁盘格式变化。

## 6. 改动文件

- 代码：`internal/tools/mcp_registry.go`、`internal/tools/mcp_bootstrap.go`、
  `internal/cli/mcp_config.go`、`internal/cli/mcp_loader.go`、
  `internal/server/chat_tool_tiers.go`
- 测试：`internal/tools/mcp_namespace_parity_test.go`、
  `internal/cli/mcp_loader_test.go`、`internal/server/mcp_tool_names_test.go`
- 契约/生成物：`contracts/env-vars.json`、`contracts/config-keys.json`、
  `docs/env-config-contract-diff.md`、`architecture/model.yaml`、
  `architecture/graph.json`、`docs/architecture/*`（7 件）、
  `scripts/env_config_contract_data.py`、`docs/appearance-scope-registry.json`、
  `docs/appearance-master-matrix.html`、`docs/configuration.html`
- 文档：`docs/pending-decisions.md`、`docs/alignment-differences.md`、
  `docs/mcp-namespace-residuals.md`、`docs/mcp-alias-prep.md`、
  `docs/mcp-crosscheck.md`、`docs/mcp-registry/README.md`、
  `docs/delivery-backlog.json`、本报告
