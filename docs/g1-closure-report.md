# G1 独立闭包验收报告（字段级逐字一致）

- 验收时间：2026-11-21 14:11–14:45 CST
- 基线：`main 059f4afe`（工作树 `.wt/g1-closure-verify`，分支 `g1-closure-verify`；验收期间不改产品代码）
- 方法：在干净 worktree 上**独立复跑**全部 G1 证据命令（双跑探针 / 契约 `--check` / 目标包测试 / parity / smoke / surface），只读比对既有 CI/QC 日志；发现的可修项当场修复并复验。
- 结论：**G1 八项条件全部满足**（含已登记例外）；复跑中发现 2 个契约产物陈旧、1 个生成页陈旧、1 组文档计数陈旧，均已修复并复验为绿；无未声明差异。

## 0. 结论总表

| # | 条件 | 命令（在 `go_hive/go-hive/` 的用相对根） | 结果 | 证据路径 | 例外 |
| --- | --- | --- | --- | --- | --- |
| 1 | 首轮 LLM 输入逐字 | `uv run --offline python go_hive/scripts/prompt_system_parity.py`；`... prompt_catalog_parity.py` | **PASS** system 21,584/21,584；opening messages 4,344/4,344；catalog 3,985/3,985；manifest 307/307；turn_context 45/45；渲染 golden 10/10 | `go_hive/docs/e2e-assets/prompt-catalog/`（`live-summary.txt`、`diff.json`） | P-06 overseer relay、P-07 credentials 刷新时机、P-08 colony 相位无 live 抓包（`prompt-parity.md` §6，均不影响独立相位首轮） |
| 2 | 工具面（首轮 24 + 延迟 51 + 同名定义） | 重跑 `capture_residual.py`（假网关，本树 `dist/hive`）；`go test ./internal/cli -run 'TestFlatSurface…|TestSameName…|TestQueenLifecycle…'` | **PASS** 首轮 `tools[]`=24 且 sha256 与冻结记录一致；加载后 75=24+51、无多余/缺失；desc 41/41；schema 归一 40/41 | `docs/e2e-assets/tool-contract/g1-recheck.json`（sha 重验）；`docs/tool-contract-recheck.json` rounds[3]；`docs/tool-contract-diff.md` §13 | `run_playbook` schema（折叠参数，§13.3）；`session_cwd` 4 项层差异；`exa_search` 结构不适用（§13.6 清单） |
| 3 | API/SSE 值级 + 路由 | `uv run --offline python go_hive/scripts/api_response_values.py --check`；`... sse_frames_canonical.py --check`；`... export_api_routes.py --check`；`... surface_audit.py --strict` | **PASS** REST 77 端点/8,678 值路径，48 条已声明、**0 未声明**；SSE live 34/34、replay 17/17、history 42/42（stop 18/17 记录），11 条例外全观测；路由 217/220、missing_effective **0**；surface 前端→Go 0、Python→Go 0、卫生 0 | `contracts/api-responses/value-golden.json`、`contracts/sse-frames/diff.json`、`contracts/api-routes.json`、`docs/api-ws-golden.md`、`docs/surface-audit.md` | SPA fallback 200 HTML vs 404（§6.4）；`max_iterations` 999999/50、`system_prefix_sha`、`model` 前缀、context_usage 数值（§5） |
| 4 | env/config 契约 | `... freeze_env_config_contract.py --check`；`... check_env_config_contract.py --strict` | **PASS**（修复后）两个 `--check`/`--strict` 退出 0；env 复算 Python 89 / Go 118 / 共有 83；config 134 键：aligned 125 / go_only 4 / go_partial 4 / unread 1；python_only 0、类型/默认/解析差异 0 | `contracts/env-vars.json`、`contracts/config-keys.json`、`docs/env-config-contract-diff.md`、`docs/configuration.html` | `mcp.*` 4 个 go_only、4 个 go_partial、1 个 unread（`created_at`）；见 §4 |
| 5 | 存储字段 + 凭据双读 | 只读复核 `docs/storage-compat/summary.md` + `credentials-crossread.json` + `contracts/storage-fields.json` | **PASS（快照口径）** v1 20/20、v2 59/59、colony-fork 32/32 文件；JSON 字段差异 0；SQLite schema/行差异 0；凭据双向互读（go_reads_python 2 条、python_reads_go credentials/ids/index） | `docs/storage-compat/`、`contracts/storage-fields.json`、`docs/storage-contract-diff.md` | 值格式级（时间戳渲染/JSON 排版）、probes 组 10 项值/排版差异（§5 登记） |
| 6 | Skill/内置资产 | `... gen_builtin_skills_manifest.py --check`；`--verify-tree go_hive/go-hive/internal/skills` | **PASS** 25 文件 / 229,354 bytes sha256 全一致（两侧同）；catalog 渲染 10/10 identical | `contracts/builtin-skills.json`、`docs/e2e-assets/prompt-catalog/{python,go}` | 无 |
| 7 | 例外清单齐全 | 逐面比对机器清单与文档（下表 §7） | **PASS** 六面（API/SSE/工具/prompt/浏览器/集成）均有清单且与机器产物一致；修正 3 处文档陈旧计数 | 见 §7 | — |
| 8 | 全量 CI + QC（只读日志 + QC 级复跑） | CI 日志 `/tmp/main_ci_wave12.log`；复跑 parity/parity-targets、QC C2/C3/C8、smoke | **PASS（附口径说明）** CI 七门禁绿（5,306 测试）；HEAD 复跑 parity 5,306/5,306、refs 5,709/0 missing；smoke 13/13；QC 报告 8/8（默认项） | `/tmp/main_ci_wave12.log`、`/tmp/qc_wave12.log`、`go_hive/QC_REPORT.md`、`docs/surface-audit.md` | QC「8/8」为默认 8 项（不含 C1/C3）；C1 全量未重跑（门禁约束），以 CI 日志为准；smoke 在本 worktree 因无 UI 资产 404、在 main 树（含 UI）13/13 PASS |

## 1. 首轮 LLM 输入逐字（条件 1）

命令与结果（本次复跑，全部 exit 0）：

```bash
uv run --offline python go_hive/scripts/prompt_system_parity.py
  system prompt:        IDENTICAL (python=21584 chars, go=21584 chars)
  opening messages:     IDENTICAL (python=4344 chars, go=4344 chars)
  skills catalog block: IDENTICAL (python=3985 chars, go=3985 chars)
  searchable manifest:  IDENTICAL (python=307 chars, go=307 chars)
  turn_context:         python_injected=True go_injected=True body=IDENTICAL

uv run --offline python go_hive/scripts/prompt_catalog_parity.py
  10/10 cases IDENTICAL（small/edge ×3 相位、caps full/compact、reminder、turn-context）
```

- 证据落盘：`go_hive/docs/e2e-assets/prompt-catalog/`（本次复跑后 canonical 产物无变化；`python-raw/meta.*.json` 的 raw_chars 环境噪声已还原）。
- 例外：`prompt-parity.md` §6 的 P-06（overseer relay）、P-07（credentials 段刷新时机）、P-08（colony 相位无 live 抓包），均不影响独立相位首轮文本逐字一致的口径。

## 2. 工具面（条件 2）

- **首轮 `tools[]` 24 逐字（sha）**：在 HEAD 上重跑冻结抓取脚本（`capture_residual.py`，脚本化假网关 → 本树 `CGO_ENABLED=0` 构建的 `dist/hive`）：
  - 首轮请求 24 名、顺序与 Python eager 清单逐字相同；
  - 抓取文件 sha256 = `0658b3081e61328717d543fa4539d6201b50b5828ec22b004959218c62040f41`，与 `docs/tool-contract-recheck.json` rounds[3] 记录的 `go-lazy-first-tools.json` **逐字节相同**；
  - 复验记录：`go_hive/docs/e2e-assets/tool-contract/g1-recheck.json`（含两个 sha 与 24/75/51 计数）。
- **deferred 51 可加载**：同一次抓取把 51 个延迟工具全部 `search_tools(select:…)` 加载，下一次请求 `tools[]` = **75 = 24 + 51**，`loading_complete=true`、`unexpected_extra=[]`（`g1-recheck-summary.json`）。
- **同名定义**：`go test ./internal/cli -run 'TestSameNameToolDescriptionsMatchFrozenContract|TestToolContractParity…|TestQueenLifecycleDefinitionsMatchPythonSource|TestRunPlaybookDescriptionMatchesFrozenContract|TestFlatSurface…'` 全部 `--- PASS`：
  - desc 逐字 **41/41**；schema 归一（源码参考口径）**40/41**，唯一例外 `run_playbook`（Python 侧把 `get_playbook_status`/`stop_playbook` 折叠为独立工具，Go 为 `action`/`run_id`，共享参数与描述逐字一致）；
  - `session_cwd` 4 项为层差异（请求层两侧同样剥除）；`exa_search` 结构不适用；例外清单见 `tool-contract-diff.md` §13.6。

## 3. API / SSE / 路由（条件 3）

```bash
uv run --offline python go_hive/scripts/api_response_values.py --check
  value golden --check OK: 77 endpoints, 8678 value paths, 48 declared differences, verdict=PASS
uv run --offline python go_hive/scripts/sse_frames_canonical.py --check
  SSE frame golden --check OK: 113 reference / 148 target frames, 11 declared exceptions, verdict=PASS
uv run --offline python go_hive/scripts/export_api_routes.py --check
  [routes] manifest is current（修复后）
uv run --offline python go_hive/scripts/surface_audit.py --strict
  前端→Go 缺失 0；Python→Go 缺失 0；卫生 0；结论 PASS
```

- SSE 帧级：`live 34/34`、`replay 17/17`、`history 42/42` EQUAL（`stop` 为停机尾巴，`18/17` 逐条登记）；11 条例外全部在 golden 观测到（陈旧例外会 FAIL）。
- REST：`api-contract-diff.md` 汇总 75 PASS + 2 DECLARED（`config_models`、`browser_contexts`），0 FAIL；`api-ws-golden.md` §6.3 登记 13 条值级/形状例外规则（0 端点级折叠）。
- 路由：静态提取 217/220，`missing_in_target_effective=0`（2 条缺失由 alias 覆盖）、`extra_in_target=5`（均为扩展/别名，带 annotation）、`method_mismatch=1`（`/api/sessions/{}/colony` DELETE 由 alias 形状接管）。
- 未知路径 fallback：Python 200 HTML vs Go 404，口径未定、`未改行为`（§6.4，仅证据）。

## 4. env / config 契约（条件 4）

- **发现（修复前）**：`freeze_env_config_contract.py --check` 与 `check_env_config_contract.py --strict` 在 059f4afe 上均为 **红**——三份产物（`env-vars.json`、`config-keys.json`、`env-config-contract-diff.md`）落后于 wave11/wave12 合并后的代码（行号漂移 + mentions/read 计数变化，如 `HIVE_HOME` mentions 96→145、`HIVE_DESKTOP_PARENT_PID` loader.go 1026→1067）。
- **修复**：重跑 `freeze_env_config_contract.py` → 提交新产物；复验：
  - `--check` → `OK: env-vars.json, config-keys.json and env-config-contract-diff.md are up to date`
  - `--strict` → `RESULT: OK — manifests match the tree and the published env audit`
- **口径数字**：env 复算 `python 89 / go 118 / common 83 / python_only 6 / go_only 35`（与审计自检 MATCH）；config **134 键**：`aligned 125 / go_only 4（mcp.enabled|namespace|timeout_seconds|servers）/ go_partial 4 / unread 1（created_at）`，`missing(python_only)=0`、类型/默认/解析差异 0。
- 关联刷新：`gen_configuration_html.py` 重生成（其检查对 `config-keys.json` 的 sha 敏感），`configuration.html` 复验 `ok`。

## 5. 存储字段与凭据双读（条件 5）

- `docs/storage-compat/summary.md`（storage-verbatim 车道，基线 `93193450`）：v1 20/20、v2 59/59、colony-fork 32/32 文件；JSON 字段差异 0、SQLite schema/行差异 0；probes 组 14 文件（会话 id 不同导致的目录/值级差异为设计内）。
- `contracts/storage-fields.json`：字段级契约（observed 10 类 + code 8 类；presence 口径 both/python_only/go_only）。
- 凭据双读：`docs/storage-compat/credentials-crossread.json` 两个方向都有完整值记录（固定 Fernet key；`go_reads_python` 2 条、`python_reads_go` credentials/ids/index）。
- 口径：条件 5 为**快照复核**（未重跑 storage_compat_run.sh——它会重写 tracked 证据文件）；如需重出，命令见 `docs/storage-compat/README.md`。

## 6. Skill / 内置资产（条件 6）

```bash
uv run --no-project python go_hive/scripts/gen_builtin_skills_manifest.py --check
  OK — contracts/builtin-skills.json is up to date (25 files, 229354 bytes)
uv run --no-project python go_hive/scripts/gen_builtin_skills_manifest.py \
    --verify-tree go_hive/go-hive/internal/skills
  go_hive/go-hive/internal/skills matches (25 files, 229354 bytes)
```

- catalog 渲染 golden 10/10 identical（条件 1 同源证据）；20 skill / 25 文件（含 `pdf/REFERENCE.md`、`references/*.md`）sha256 两侧一致。

## 7. 例外清单（条件 7）

| 面 | 机器产物 | 文档清单 | 复核结论 |
| --- | --- | --- | --- |
| API（REST 值级） | `contracts/api-responses/value-golden.json`（13 条规则） | `docs/api-ws-golden.md` §5/§6.3 | 一致（本次修正 209→48、74+3→75+2、14→13） |
| SSE（帧级） | `contracts/sse-frames/diff.json`（11 条） | `docs/sse-payload-parity.md` §4、`api-ws-golden.md` §5 | 一致 |
| 工具（定义 + 输出） | `docs/tool-contract-recheck.json` rounds[3]、`docs/e2e-assets/tool-output/diff.json`（13 条） | `docs/tool-contract-diff.md` §13.6、`tool-output-contract.md` §5 | 一致（0 未归因差异） |
| prompt | `skills-parity-report.json` structural_exceptions | `docs/prompt-parity.md` §6（P-01…P-08） | 一致 |
| 浏览器 | `docs/browser-bridge-surface-audit.html`（R01–R15 状态） | 同页 + `browser-bridge.html` | 一致：已修项标注，剩余为引擎近似（图像编码字节/dpr） |
| 集成 | `docs/integrations-evidence-audit.json`（A12/B91/C0/D1/E0） | 同名 .md/.html | 一致：B 类=假服务、真实第三方未验证如实标注 |

"未声明差异 = FAIL" 由三个 `--check`（REST/SSE/工具输出）强制执行；本次复跑均 PASS。

## 8. 全量 CI + QC（条件 8）

- **CI**：只读确认 `/tmp/main_ci_wave12.log`（14:08，wave12 合并后）：`gofmt OK`、`go vet` 无输出、`go test ./...` 全包 ok、`cross` 六平台、`parity` 5,306/5,306、`parity-targets` 5,687 refs / 0 missing、`wiring_check` 12 文件 47 项。
- **HEAD 级复跑（本 worktree 059f4afe）**：
  - `bash go_hive/scripts/check_parity.sh` → `check_parity: OK — 5306 个测试全部带有 parity 注解或豁免说明`（exit 0）；
  - `bash go_hive/scripts/check_parity_targets.sh` → `5,709 refs, 0 missing`（exit 0）；与 CI 日志的 5,687 差异为 refs 记账口径（test 总数一致），非回归；
  - `gofmt -l .` 空、`go vet` 目标包 0、`go test -count=1 ./internal/cli ./internal/skills ./internal/agentloop ./internal/envcontract` 全 ok；
  - QC 级复跑：C2 parity PASS、C8 surface strict PASS、C3 `smoke.sh` **13/13 PASS**（main 树、空闲端口 62721；本 worktree 因 UI 资产未构建，`/` 与 SPA 深链 404——属工作树环境，非产品缺口）。
- **QC 报告**：`go_hive/QC_REPORT.md` **8/8 PASS**（05c5152b，HEAD 的直接父提交，其后仅 docs 提交）；注意其"8 项"为默认集合，不含 C1/C3；C1 全量 CI 未在本次重跑（任务门禁：不跑全量 ci），以 `/tmp/main_ci_wave12.log` 为准。

## 9. 本次修复（工作树内，随本报告一并提交）

| 文件 | 问题 | 处置 |
| --- | --- | --- |
| `contracts/env-vars.json`、`contracts/config-keys.json`、`docs/env-config-contract-diff.md` | `freeze --check` 红：行号/计数漂移 | 重生成 |
| `contracts/api-routes.json` | `--check` 红：行号漂移（路由集合不变，missing_effective 0） | 重生成 |
| `docs/configuration.html` | config-keys sha 变化后陈旧；另：已提交 HTML 含 `.card .d` 折行 CSS 而生成器缺该行（HEAD 上 `--check` 本就红） | 重生成；同步生成器与该 CSS 一行，`--check` 复验 `ok` |
| `docs/release-cadence.html` | 门槛日期 11-24/11-27 落后于 delivery-targets（11-22/11-22） | 重生成 |
| `architecture/graph.json` + `docs/architecture/*` | `gen_arch --check` 红：20 个包未映射、路由 198→220 等 | 重生成（见 §10 遗留） |
| `docs/api-ws-golden.md` | 陈旧计数：209 条已声明差异、74+3 端点、14 条规则、165 条声明族、`live-tools-mcp-universe` 3 条 | 按 ledger 复算修正为 48、75+2、13、4、4 条 |
| `docs/e2e-assets/tool-contract/`（新增） | —— | 首个请求 24 工具 sha 重验记录 |

## 10. 发现的新缺口 / 遗留

1. **architecture/model.yaml 映射债**：重生成后 `gen_arch` 报告 64 包 / 20 个"未映射层"包 / 50 条 warnings；`--check` 已绿，但"未映射层"需要补齐 `model.yaml` 的 `packages`/`capabilities[].modules` 才能消失。不属 G1 门禁，列入 backlog。
2. **`gen_configuration_html.py` 与已提交 HTML 不一致（HEAD 既有）**：HEAD 的 `configuration.html` 含 `.card .d{…overflow-wrap:anywhere;word-break:break-word}`，而 HEAD 的生成器没有这一行，`--check` 在 HEAD 上就会失败。本 lane 把该行补进生成器并重生成（与 main 工作树里未提交的同名修改逐字一致），`--check` 现为 `ok`。后续合并该未提交修改时不会产生冲突块。
3. **QC 口径**：治理板"QC 8 项"列表含 C1/C3，但最近的 QC 运行是默认 8 项（不含二者）。本报告已按实复跑 C3；建议治理板措辞改为"默认 8 项；C1/C3 需 `--full --smoke`"（本 lane 已改）。
4. **CI 日志 refs 数**：`/tmp/main_ci_wave12.log` 记 5,687 refs，HEAD 复跑 5,709（test 总数同为 5,306）。差异为记账时点，非回归；引用时应写口径。
5. **本 worktree 无 UI 资产**：`frontend/dist` 与 `internal/server/uiembed/dist` 均未入库，`smoke.sh` 的 `/`/SPA 断言在裸 worktree 必红——判冒烟必须用 main 树或先构建 UI。
6. **storage 快照时效**：条件 5 证据仍锚定 `93193450`；本轮只读复核（字段/凭据结构未再变化）。如需追新，重跑 `storage_compat_run.sh` 并按 diff 决定是否刷新产物。

## 11. 复现

```bash
cd /Users/mac/hive/.wt/g1-closure-verify          # 或任意 main 检出
uv run --offline python go_hive/scripts/prompt_system_parity.py
uv run --offline python go_hive/scripts/prompt_catalog_parity.py
uv run --no-project python go_hive/scripts/gen_builtin_skills_manifest.py --check \
  && uv run --no-project python go_hive/scripts/gen_builtin_skills_manifest.py --verify-tree go_hive/go-hive/internal/skills
uv run --no-project python go_hive/scripts/freeze_env_config_contract.py --check
uv run --no-project python go_hive/scripts/check_env_config_contract.py --strict
uv run --no-project python go_hive/scripts/export_api_routes.py --check
uv run --no-project python go_hive/scripts/api_response_values.py --check
uv run --no-project python go_hive/scripts/sse_frames_canonical.py --check
uv run --no-project python go_hive/scripts/surface_audit.py --strict
cd go_hive/go-hive && bash ../scripts/check_parity.sh && bash ../scripts/check_parity_targets.sh
CGO_ENABLED=0 GOPROXY=off go build -o dist/hive ./cmd/hive
cd ../.. && bash go_hive/scripts/smoke.sh "$(uv run --no-project python -c "import socket;s=socket.socket();s.bind(('127.0.0.1',0));print(s.getsockname()[1]);s.close()")"
```

> 所有 `--check`/`--strict` 均以**显式参数**逐条运行，并记录进程真实退出码（本次复跑 14 项全部 `exit=0`；不以管道尾部命令的退出码判绿）。

> 首轮工具面 sha 重验脚本（scratch，不入库）：`capture_residual.py`（GO_ROOT 指向本树、`dist/hive`）——14:26 复跑产出与冻结记录逐字节相同，记录见 `go_hive/docs/e2e-assets/tool-contract/g1-recheck.json`。
