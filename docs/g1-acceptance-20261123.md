# G1 独立验收报告（HEAD `42f2b932`，2026-11-23）

- 验收时间：2026-11-23 22:45–22:58 CST
- 工作树：`/Users/mac/hive/.wt/w91-g1-acceptance`，分支 `w91/g1-acceptance`
- 冻结 HEAD：`42f2b93215edddcd3f7f9ef5ca246162753e6d7b`（= 当前 `main`；起始工作树干净）
- 参照：`go_hive/docs/g1-closure-report.md`（旧基线 `059f4afe`，2026-11-21）
- 方法：在当前 HEAD **独立重跑**全部可选且可复现的条件；快照类条件只读复核；不跑全量 `make ci`，读取最近全量 CI 代码点 `cea04d9e` 的归档日志。
- 结论：**G1 八项条件在当前 HEAD 全部 PASS**（含已登记例外），`accepted: true`。验收过程中条件 4 在 HEAD 上为红（三份 env/config 产物陈旧），已按正规生成器最小刷新并复验为绿；另修正 1 处文档陈旧计数。无未声明差异，无产品代码改动。

机器可读结论：`go_hive/docs/e2e-assets/g1-acceptance-20261123/summary.json`。

## 0. 结论总表

| # | 条件 | 结果 | 关键数字（本次实测） |
| --- | --- | --- | --- |
| 1 | 首轮 LLM 输入逐字 | **PASS** | system 21,584/21,584；opening 4,344/4,344；catalog 3,985/3,985；manifest 307/307；turn_context 正文一致；渲染 golden 10/10 |
| 2 | 工具面（首轮 24 + 延迟 51 + 同名定义） | **PASS** | 首轮 24，sha256 `0658b308…` 与冻结记录逐字节相同；加载后 75 = 24+51；desc 48/48；schema 归一 48/48（raw 40/48，8 条纯表示差） |
| 3 | API/SSE 值级 + 路由 | **PASS** | REST 107 端点 / 8,716 值路径 / 46 条已声明 / **0 未声明** / 0 端点级；SSE 113/148 帧、9 条例外全观测；路由 222/225、missing_effective 0；surface 0/0、卫生 0 |
| 4 | env/config 契约 | **PASS（修复后）** | env python 90 / go 125 / common 84；config 137 键：aligned 131 / go_only 4 / go_partial 1 / unread 1；python_only 0；类型/默认/解析差异 0 |
| 5 | 存储字段 + 凭据双读 | **PASS（快照口径）** | v1 20/20、v2 59/59、colony-fork 32/32；JSON 字段差 0；SQLite schema/行差 0；凭据双向互读完整 |
| 6 | Skill/内置资产 | **PASS** | 25 文件 / 229,354 bytes sha256 两侧一致；catalog 渲染 10/10 |
| 7 | 例外清单齐全 | **PASS** | 六面清单与机器产物一致；修正 API 文档 1 处陈旧汇总计数（2 端点级 → 0） |
| 8 | 全量 CI + QC 证据 | **PASS（附口径）** | 最近全量 CI：`cea04d9e` 5,979 测试、6,689 refs/0 missing、compat 12/12、CI_RC=0；HEAD 本地 parity 5,979/5,979、refs 6,725/0 missing、smoke 13/13 |

完整命令与退出码：`go_hive/docs/e2e-assets/g1-acceptance-20261123/command-logs/final-verification.txt`（18 条复跑命令全部 `exit=0`），另有 smoke `exit=0`。

## 1. 冻结与代码点

```text
git rev-parse HEAD                  = 42f2b93215edddcd3f7f9ef5ca246162753e6d7b
git status --porcelain（起始）      = <empty>
git merge-base --is-ancestor cea04d9e HEAD = true
git diff --name-status cea04d9e..HEAD
  M go_hive/docs/reports/INDEX.md
  A go_hive/docs/reports/slot-20261123-22.md
```

- **current HEAD**：`42f2b932`（2026-11-23 22:02）。
- **last full CI code point**：`cea04d9e`（2026-11-23 21:40，5,979 tests，CI_RC=0；日志 `command-logs/ci-cea04d9e-full.txt`）。
- 两者之间**只有 2 个 docs/reports 提交**，产品代码、测试、契约与生成产物均无差异；因此 last full CI 的证据可直接覆盖 HEAD 的产品代码面。本 lane 按门禁不重跑 `go test ./...`。

工具链：go1.26.11 darwin/arm64、uv 0.12.18、Python 3.11.16、macOS 26.7.1 arm64（`environment.txt`）。

## 2. 八项明细

### 条件 1 — 首轮 LLM 输入逐字（PASS）

```bash
uv run --offline python go_hive/scripts/prompt_system_parity.py   # exit 0
uv run --offline python go_hive/scripts/prompt_catalog_parity.py  # exit 0
```

- system prompt `IDENTICAL (21,584/21,584)`；opening messages `IDENTICAL (4,344/4,344)`；skills catalog `IDENTICAL (3,985/3,985)`；searchable manifest `IDENTICAL (307/307)`；turn_context 两侧都注入且正文一致。
- catalog 渲染 10/10 逐字（small/edge 三相位、caps full/compact、turn-context、reminder）。
- 与旧报告数字完全一致；canonical 产物无变化。复跑产生的 `python-raw/meta.*.json` `raw_chars` 环境噪声已还原，不入提交。

### 条件 2 — 工具面（PASS）

- 用脚本化假网关 + 本树 `CGO_ENABLED=0` 构建的 `dist/hive` 做 live 抓包（scratch harness，复用旧口径：首轮 `tools[]` 原样落盘、`search_tools` load 全部 51 个延迟工具）：
  - 首轮 `tools[]` = **24**，顺序与 Python eager 清单逐字相同；
  - `first-tools.json` sha256 = **`0658b3081e61328717d543fa4539d6201b50b5828ec22b004959218c62040f41`**，与 `docs/tool-contract-recheck.json` rounds[3] 冻结值**逐字节相同**；
  - 加载后 `tools[]` = **75 = 24 + 51**，`last_missing=[]`、无重复、`loading_complete=true`。
- 定义级 `go test ./internal/cli`（FlatSurface/SameName/QueenLifecycle/RunPlaybook/ToolContractFreeze/ToolContractParity）全部 `--- PASS`：
  - description 逐字 **48/48**；schema 归一 **48/48**；raw **40/48**，8 条为纯表示差（数值字面拼写 4 + 参考层 `session_cwd` 4），由既有归一化吸收（`tool-contract-diff.md` §18.6）；
  - `run_playbook` 折叠参数例外在 round 9 已收口，不再登记。

### 条件 3 — API / SSE / 路由（PASS）

```bash
uv run --no-project python go_hive/scripts/api_response_values.py --check   # exit 0
uv run --no-project python go_hive/scripts/sse_frames_canonical.py --check  # exit 0
uv run --no-project python go_hive/scripts/export_api_routes.py --check     # exit 0
uv run --no-project python go_hive/scripts/surface_audit.py --strict        # exit 0
```

- REST：**107 端点 / 8,716 值路径 / 46 条已声明差异 / 0 未声明 / 0 端点级折叠**，`verdict=PASS`。
- SSE：reference 113 / target 148 帧；live 34/34、replay 17/17、history 42/42 `EQUAL`，stop 18/17 为停机尾巴（informational）；**9 条例外全部在 golden 观测到**，`verdict=PASS`。
- 路由：python 222 / go 225；`missing_in_target_effective=0`（2 条由 alias 覆盖）、`extra_in_target=5`（带 annotation 的扩展/别名）、`method_mismatch=1`。
- surface：前端→Go 缺失 0、Python→Go 缺失 0、卫生 0，`PASS`；`Go 多余 11` 为信息项，不构成缺口。
- 说明：首次 surface 审计曾因本 lane 新建的空 `command-logs/` 目录报卫生 FAIL；写入日志后复跑 PASS。这是验收工作树产物，不是产品缺口。

### 条件 4 — env/config（as-found FAIL → 修复后 PASS）

**as-found（HEAD 原状，exit 1）**：

```text
freeze_env_config_contract.py --check  → STALE: env-vars.json, config-keys.json, env-config-contract-diff.md
check_env_config_contract.py --strict  → RESULT: REPORT — ... staleness
```

漂移内容（`git diff` 逐项核对）：`chat_memory_wiring.go` 行号 325/326/327 → 313/314/315；`HIVE_PAST_RECALL_TIMEOUT` 读取点从 `memory/past_conversations.go`（envFloat/float）迁到 `agentloop/past_conversations_reminder.go`（string 解析），status 变为 `default_and_parse_differ`；若干 mentions/行号漂移。均为代码演进后的产物陈旧，不是产品缺陷。

**最小修复**（未改产品代码）：

```bash
uv run --no-project python go_hive/scripts/freeze_env_config_contract.py        # exit 0（3 份产物）
uv run --no-project python go_hive/scripts/gen_configuration_html.py             # exit 0（config-keys sha 变化）
```

**复验**：

```text
freeze_env_config_contract.py --check  → OK: ... are up to date            (exit 0)
check_env_config_contract.py --strict  → RESULT: OK — manifests match ...  (exit 0)
gen_configuration_html.py --check      → ok: configuration.html matches ...(exit 0)
```

口径数字：env 复算 `python 90 / go 125 / common 84 / python_only 6 / go_only 41`（MATCH）；config **137 键**：`aligned 131 / go_only 4（mcp.*）/ go_partial 1 / unread 1（created_at）`，`python_only=0`、类型/默认/解析差异 0。

### 条件 5 — 存储与凭据（PASS，快照口径）

- `docs/storage-compat/summary.md`（锚定基线 `93193450`）：v1 20/20（18 identical + 2 opaque）、v2 59/59（56 + 3）、colony-fork 32/32（30 + 2）；三组 JSON 字段差 **0**、SQLite schema/行差 **0**。
- `contracts/storage-fields.json`：observed 10 类 / code 9 类；`meta.baseline_commit=93193450`。
- 凭据双读：`go_reads_python` 2 条、`python_reads_go` credentials/ids/index 齐全（固定 Fernet key）。
- probes 组 10 个 changed 文件为会话 id 导致的目录/值级设计差异（`docs/storage-contract-diff.md` §5 已登记）。
- 未重跑 `storage_compat_run.sh`（会重写 tracked 证据）；HEAD 代码面由 CI `cea04d9e` 的 C4（storage contract self-consistent）与 C5（DB schema versions agree）佐证。

### 条件 6 — Skill/内置资产（PASS）

```bash
uv run --no-project python go_hive/scripts/gen_builtin_skills_manifest.py --check          # exit 0
uv run --no-project python go_hive/scripts/gen_builtin_skills_manifest.py --verify-tree …  # exit 0
```

- 25 文件 / 229,354 bytes，两侧 sha256 全一致（manifest sha256 `c0ae86d4…`）；catalog 渲染 10/10 identical。

### 条件 7 — 例外清单（PASS）

| 面 | 机器产物 | 本次复核 |
| --- | --- | --- |
| API（REST） | `value-golden.json`：10 条规则 / 46 条已声明 / 0 端点级 | 与 `api-ws-golden.md` §5/§6.3 一致；修正 2 行陈旧汇总（“105 + 2 端点级”→“107/107 全 PASS、46 值级、0 端点级”） |
| SSE | `sse-frames/diff.json`：9 条 | 与 `sse-payload-parity.md` §4 一致 |
| 工具定义 | `tool-contract-diff.md` §18.6/§18.8（desc 48/48、归一 48/48、raw 8 条表示差；§13.6 为历史轮次） | live 测试数字与 §18.6 一致 |
| 工具输出 | `e2e-assets/tool-output/diff.json`：18 条、0 未归因 | 与 `tool-output-contract.md` §5 的 18 条一致（§3/§4 的 38 场景表为“首批”历史口径，顶部当前汇总为 69/50/1/18） |
| prompt | `skills-parity-report.json` structural_exceptions = P-06/07/08 | 与 `prompt-parity.md` §6 一致 |
| 浏览器 | `browser-bridge-surface-audit.html` R01–R20 | 同页即机器产物 |
| 集成 | `integrations-evidence-audit.json` A12/B91/C0/D1/E0 | 与同名 .md/.html 一致 |

“未声明差异 = FAIL”由 REST/SSE/工具输出三个 `--check` 强制执行，本次均 PASS。

### 条件 8 — 全量 CI + QC（PASS，附口径）

- **last full CI code point `cea04d9e`（只读日志）**：`gofmt OK`、`go vet` 无输出、`go test ./...` 全包 ok、`check_parity 5,979/5,979`、`parity refs 6,689 / 0 missing`、`compat-check 12/12 PASS`、`CI_RC=0`。
- **current HEAD 本地复跑**：
  - `check_parity.sh` → `5,979/5,979`（exit 0）；
  - `check_parity_targets.sh` → `6,725 refs / 0 missing`（exit 0；refs 记账随提交数增长，测试总数与 CI 一致，非回归）；
  - `gofmt -l .` 空；`go vet` 目标包 0；`go test ./internal/cli ./internal/skills ./internal/agentloop ./internal/envcontract` 全 `ok`；
  - `smoke.sh` **13/13 PASS**（在主树、同 HEAD、含 UI 资产；构建输出到临时目录，只读不改主树）。本 worktree 无 `uiembed/dist`，裸 worktree 的 `/` 与 SPA 深链必 404，属环境而非产品缺口。
- QC 的“8/8”为默认 8 项（不含 C1/C3）；C1 全量以 CI 日志为准。

## 3. 与旧报告（`059f4afe`）的差异

| 项 | 旧报告 | 本次 HEAD | 说明 |
| --- | --- | --- | --- |
| HEAD / CI | 059f4afe / 5,306 tests | 42f2b932 / 5,979 tests（CI 点 cea04d9e） | 代码增长，非回归 |
| 条件 1 | 21,584 / 4,344 / 3,985 / 307；10/10 | 同左 | 未漂移 |
| 条件 2 | 首轮 24 sha 相同；desc 41/41；schema 40/41；run_playbook 例外 | 首轮 24 sha 相同；desc **48/48**；归一 **48/48**；run_playbook 例外退役 | 同名集从 41 扩到 48（round 7–9） |
| 条件 3 | 77 端点 / 8,678 路径 / 48 declared / 75+2；SSE 11 例外；路由 217/220 | **107 / 8,716 / 46 / 0 端点级**；SSE **9** 例外；路由 **222/225** | 值级面扩 30 探针；例外收敛退役 |
| 条件 4 | 在 059f4afe as-found 红，重生成后绿；89/118/83；134 键 | 在 42f2b932 **再次 as-found 红**，重生成后绿；90/125/84；137 键 | 代码继续演进导致产物再次陈旧；已复验 |
| 条件 5 | v1 20/20、v2 59/59、colony 32/32；0 差异 | 同左 | 证据仍锚定 93193450 |
| 条件 6 | 25 文件 / 229,354 bytes | 同左 | 未漂移 |
| 条件 7 | 修正 3 处陈旧计数 | 修正 1 处（API 端点级汇总） | 其余面已同步 |
| 条件 8 | CI 5,306；refs 5,687；smoke 13/13 | CI 5,979；CI refs 6,689 / 本地 6,725；smoke 13/13 | refs 记账差异同旧报告性质 |

## 4. 本次改动（全部非产品代码）

| 文件 | 原因 | 处置 |
| --- | --- | --- |
| `go_hive/contracts/env-vars.json` | 条件 4 as-found 红（读取点/计数/行号漂移） | 正规生成器重生成 |
| `go_hive/contracts/config-keys.json` | 行号漂移 325/326/327 → 313/314/315 | 正规生成器重生成 |
| `go_hive/docs/env-config-contract-diff.md` | 同上 | 正规生成器重生成 |
| `go_hive/docs/configuration.html` | config-keys sha 变化 | 正规生成器重生成，`--check` 复验 `ok` |
| `go_hive/docs/api-ws-golden.md` | 条件 7 陈旧汇总（2 端点级 vs 机器 0） | 修正 2 行 |
| `go_hive/docs/e2e-assets/g1-acceptance-20261123/` | 本次验收证据 | 新增 |
| `go_hive/docs/g1-acceptance-20261123.md` | 本报告 | 新增 |

未改产品代码、测试、proto、路由表或其他生成产物；未改 `delivery-governance.html` / `GOVERNANCE_LEDGER.csv`。

## 5. 例外与遗留

**已登记例外（不阻塞 G1）**：P-06 / P-07 / P-08；REST 10 条值级规则；SSE 9 条；工具输出 18 条；工具定义 8 条 raw 表示差 + `exa_search` 结构差 + `get_worker_health_summary` Go-only；存储值格式级差异与 probes 会话 id 差异。

**遗留/说明**：

1. `make ci` 未在本 lane 重跑（门禁）；HEAD 与 `cea04d9e` 产品代码逐字相同，以该 CI 日志为准。
2. `storage_compat_run.sh` 未重跑（会重写 tracked 证据）；条件 5 为快照只读复核，代码面由 CI C4/C5 佐证。
3. 裸 worktree 无 UI 资产；smoke 必须用主树或先构建 UI（本次用主树、同 HEAD、只读）。
4. `tool-output-contract.md` §3/§4 的 38 场景表为“首批”历史口径；当前汇总与 §5 已是 69 场景 / 18 例外，不构成清单缺失。
5. `tool-contract-diff.md` §13.6 是历史轮次清单；当前数字与残余以 §18.6/§18.8 为准。
6. 旧报告 §10 的 architecture `model.yaml` 映射债仍属 backlog，不属 G1 门禁。

## 6. 证据路径

```text
go_hive/docs/e2e-assets/g1-acceptance-20261123/
  summary.json                          # accepted/head/conditions/evidence
  environment.txt                       # HEAD、分支、工具链、代码点差异
  condition2-live-capture.json          # 24/75/51 + sha 复验
  condition2-first-tools.json           # 首轮 tools[] 原样
  condition5-storage-readonly.json      # 存储只读复核
  condition7-exceptions-crosscheck.json # 六面机器↔文档核对
  command-logs/final-verification.txt           # 18 条复跑命令与真实退出码
  command-logs/ci-cea04d9e-full.txt             # 最近全量 CI 归档
  command-logs/cond4-*-as-found/after/...       # 条件 4 红→绿全过程
  command-logs/cond8-smoke.txt                  # 13/13
```

复现入口：`command-logs/final-verification.txt` 中每条命令均在干净工作树上可直接重跑；条件 2 的 live 抓包脚本为 scratch harness（假网关 + 本树 `dist/hive`），未入库。
