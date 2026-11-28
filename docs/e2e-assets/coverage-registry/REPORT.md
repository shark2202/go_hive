# Coverage Registry 报告

> 2026-11-23 更新：本报告是 lane 交付时的快照。后续实现已把 `gap:colony_pivot_lean_handoff` 与 `gap:dm_fork_seed_compaction` 标为 fixed；另新增 `gap:dm_fork_worker_surface`、`gap:credentials_aden_client`（open）。当前状态以 `docs/coverage-registry.json`（11 gaps：8 fixed / 3 open）为准。

## 1. 目标

现有门禁冻结的是「形状」：routes、工具 desc/schema、config/env 台账。parity 数字多来自自证式注解，没有把「上游可观察行为」变成机器可重新计算的登记册，因此用户不问就发现不了的遗漏反复出现。本 lane 建立第一版机器生成登记册，覆盖四个高价值维度：

1. synthetic / tool dispatch：Python 拦截分支是否有同名 Go 处理；
2. events 双向：Python `EventType` ↔ Go 常量 ↔ Go emitter ↔ 前端 handler；
3. phase-gated behaviours：queen 两阶段工具名单、`task_create` pivot 字段；
4. test mapping：Python `def test_` 按目录 vs Go `func Test` / `// Parity:` 引用计数。

每行强制包含 `id`、`upstream_anchor`、`go_anchor`（可为空）、`status`、`owner`、`notes`。找不到 Go 侧时写 `missing`，不静默跳过。

## 2. 交付物

- `go_hive/scripts/gen_coverage_registry.py`：stdlib 生成器，支持 `--check`。
- `scripts/test_gen_coverage_registry.py`：7 个 unittest 守卫。
- `docs/coverage-registry.json`：机器可读登记册（本报告交付时 141 行 + 9 条 known_gaps（6 fixed / 3 open）；当前为 11 条 / 8 fixed / 3 open，见顶部注记）。
- `docs/coverage-registry.md`：同一数据的人类可读渲染。
- 本报告。

未改动 `go.mod` / `Makefile` / 主 checkout；未 push。

## 3. 生成方法

### 3.1 synthetic / tool dispatch

上游抽取：

- `core/framework/agent_loop/internals/synthetic_tools.py` 的 `def build_*_tool` 与其构造的 `name=`；
- 同文件 `SYNTHETIC_PHASE_TOOL_BUILDERS` 的键；
- `core/framework/agent_loop/agent_loop.py` 中所有 `tc.tool_name == "<name>"` 分支，`task_create` 分支额外记录 `new_colony=true` guard。

Go 侧扫描 `go_hive/go-hive/internal/**/*.go`（排除 `_test.go`），按证据类型分类：

- `dispatch`：`Name/ToolName ==`、`!=`、`case "<name>":`，含常量解析（如 `item.call.Name == AskUserToolName`）；
- `tool_def`：`Tool{Name: ...}` 形式的已注册工具；
- `builder`：`Build*Tool` 内的 `Name:` 或工具名常量；
- `registration`：phase builder / dynamic refresh / live-tools 名单键；
- `prompt`：只出现在提示词里。

状态规则：`dispatch` 或 `tool_def` → `dispatch_port`；只有 `builder` → `builder_only`；否则 `missing`。`conversation.go`（历史摘要）、`runtime_log.go`（日志分类）、`tool_surface_flat.go`（CLI 描述面，含明确写着未移植的 case）不计入 dispatch，避免假阳性。

### 3.2 events 双向

- Python：`class EventType(StrEnum)` 的常量与值。
- Go：`event_type.go` 的 `Event* EventType = "..."` 与 `AllEventTypes`。
- Go emitter：对每个常量标识符扫描非测试 Go 源；只有落在 `Emit*` 函数内或所在函数体含 `Publish(` 的引用才算 emitter。直接 `queue <-` 的生产者不算，保留在 `notes` 里说明。
- 前端：`frontend/src/**/*.ts(x)`（排除 `*.test.*` / `*.spec.*`）中同一事件名字符串的出现。

状态：const + emitter + frontend 全有 → `ported`；const 存在但缺一 → `partial`；只有 const → `go_const_only`；无 Go 常量 → `missing`。反向检查 Go-only 常量，单列 `event:go_only:*` 行并标注前端是否可处理。

### 3.3 phase-gated behaviours

- 解析 `core/framework/agents/queen/nodes/__init__.py` 的 `_QUEEN_INDEPENDENT_TOOLS`、`_QUEEN_COLONY_TOOLS`，以及条件 `append("crm_summary")`（两阶段都算）。
- 每个名字在 Go 中找同名 dispatch/工具定义/builder/registration/prompt 证据。由于 Go 没有与两个 Python 列表等价的显式 phase 名单，状态用 `port_ungated`（工具有，阶段门不可机器验证）、`builder_only`、`prompt_only`、`missing`，并在 summary 里固定 `explicit_phase_lists_in_go=false`。
- pivot 字段单独成行：`new_session` 查生产 `PivotHandler` 的 `FieldName`；`new_colony` 同样查生产实例，测试里的通用 handler 只作 `test only` anchor。
- 两个 `PhasePrompt` 入口（`IndependentPhasePrompt` / `ColonyPhasePrompt`）各一行，确认 Go 有对应入口。

### 3.4 test mapping

- 扫描 `core/` 与 `tools/` 下含 `test_*.py` 的目录，按目录直接文件统计 `def test_`（不递归子目录，避免重复计数）。
- 每个目录映射到 Go package（`TEST_DIR_MAP`，未知目录按 `framework/<pkg>` 启发式映射），统计该 package 的 `func Test`；`core/tests` 映射到整个 `internal` 作交叉汇总。
- 统计所有 Go 文件里的 `// Parity:` 行，把引用的 Python 路径归一化（`tests/...`、`framework/...`、`core/...`、`tools/...`），按「测试目录覆盖的模块前缀」计数：`core/framework/agent_loop/tests` 算到 `core/framework/agent_loop/`。这样 ref 指向源文件也能被算作对应测试目录的覆盖证据。
- 状态：有 ref → `mapped`；只有 Go 测试无 ref → `go_tests_no_parity_ref`；两者都无 → `missing`。只报数，不做逐条测试映射。

## 4. 当前结果

| 维度 | 行数 | 主要计数 |
|---|---:|---|
| synthetic / tool dispatch | 13 | 9 dispatch_port / 3 builder_only / 1 missing |
| events | 77 | 54 ported / 22 partial / 1 go_const_only / 0 Go-only |
| phase gates | 29 | 25 phase tools（23 port_ungated / 1 builder_only / 1 missing）+ 2 pivot + 2 prompt |
| test mapping | 22 | 6212 Python `def test_` / 5878 Go `func Test` / 6095 `// Parity:` 行 |
| known_gaps | 7 | 4 fixed / 2 in_fix / 1 open |

总计 141 条登记行 + 7 条 known_gaps。

### 4.1 synthetic / tool dispatch

`missing`：`credentials` —— Go 只有 dynamic-refresh / phase surface 的名字登记，没有模型工具定义；CLI 面明确写 `not implemented as a model tool in this build (use the hive-cred CLI)`。

`builder_only`：

- `suggest_colony` —— builder + phase registry + live-tools 名单，无 loop intercept；即本次 7 条之一。
- `collect_result` —— Go 有 builder 和 dynamic-refresh 名单，但没有 Python `agent_loop.py:6550` 对应的后台结果收集 dispatch。
- `set_output` —— Python 分支本身已改为拒绝（"no longer available"），Go 仍保留 builder；登记为 builder_only 以记录两侧行为差异。

`task_create` 有 Go 工具定义，故为 `dispatch_port`，但 `new_colony=true` 的生产 pivot 分支缺失，由 phase 维度和 known_gaps 单列。

### 4.2 events

77 个 Python 事件与 77 个 Go 常量一一对应，没有 Go-only 常量。54 个三层齐全。

21 个事件没有匹配到 `Emit*` / `Publish` emitter（const 与前端 handler 存在），例如：`colony_created`、`context_compaction_started`、`credential_provider_connected/disconnected`、`credentials_required`、`crm_changed`、`goal_achieved`、`missed_triggers`、`skill_install_requested`、`task_list_reset`、`trigger_*`、`worker_colony_loaded`。其中 `session_snapshot` 的生产者是 SSE 首次订阅时直接 `queue <-`，不经过 `Publish`，`notes` 已标注为 “non-Publish reference(s) — may be a direct queue”，状态仍按严格定义记为 partial。

前端 handler 缺失 3 个：`payment_required`、`tool_call_replay_detected`、`execution_resurrected`。`execution_resurrected` 同时也没有 emitter，是唯一 `go_const_only`。

### 4.3 phase gates

- 25 个 phase 工具名中 23 个在 Go 有同名工具（`port_ungated`），1 个只有 builder 面（`suggest_colony`），1 个 `missing`（`crm_summary`；Go 的 `CRMInThisBuild=false`，与 Python 的可选 CRM 构建一致，属于条件功能）。
- `phase_pivot:new_session` → ported（CLI session catalogue）。
- `phase_pivot:new_colony` → missing；Go 的通用 `PivotHandler` 在 `tasks/tools_test.go` 里覆盖了 `new_colony`，但 HEAD 没有生产实例。
- `IndependentPhasePrompt` / `ColonyPhasePrompt` 两个入口均存在。

### 4.4 test mapping

`go_tests_no_parity_ref` 12 个目录，主要集中在：

- `core/framework/credentials/aden/tests`、`core/framework/runtime/tests`、`core/tests/live`；
- 7 个 `tools/src/aden_tools/tools/*/tests`；
- `tools/tests/credentials`、`tools/tests/integrations`。

这些目录上游有测试，但 Go 侧没有指向对应模块的 `// Parity:` 引用；计数只用于提示复查，不等于 Go 没有等价测试。`core/tests` 是跨模块汇总，与各 package 行存在重叠，报告中已注明。

## 5. 本次 7 条 known_gaps

| ID | 状态 | 链接 / 证据 |
|---|---|---|
| `gap:suggest_colony_dispatch` | **in_fix** | lane `colony-suggestion-dispatch`（`.wt/colony-suggestion-dispatch`，HEAD `c5fc9557` 时未提交）。上游 `agent_loop.py:5076`；Go 目前只有 `synthetic_tools.go` 的 builder/registry。 |
| `gap:task_create_new_colony_pivot` | **in_fix** | 同一 lane 的未提交 diff 增加 `enableColonyTaskPivot` 和 agentloop 拦截。上游 `queen_orchestrator.py:929`；Go 生产侧只有 `new_session`。 |
| `gap:worker_skills_scope` | **fixed** | `f18d9e23`（merge `22170ab2`）；测试 `TestWorkerCatalogCoversFrameworkUserAndColonyVisibility`。 |
| `gap:queen_preset_defaults` | **fixed** | `f18d9e23`（merge `22170ab2`）；测试 `TestWorkerCatalogAppliesQueenProfileDefaultPresetSkills`、`TestPresetSkillsFromProfileReadsCustomList`。 |
| `gap:compaction_long_reflection` | **fixed** | `61acecb9`（merge `76c0835b`）；测试 `TestContextCompactedTriggersLongReflectionOnce`、`TestCompactionPublishesContextCompactedOnce`。 |
| `gap:memory_cloud_sync_schedule_push` | **open** | 上游 `routes_memories.py:42` 的 `schedule_push("memory", rel)`；Go `routes_memories_extra.go` 的 `MemoryRouteDeps` 只有 `Home`，没有 SchedulePush 面，也没有 cloud-sync 实现。无 lane/commit/test。 |
| `gap:env_ledger_anchor` | **fixed** | `abd646b7`（merge `bb1bf6b1`）；`config-support-ledger.json` 的 `HIVE_COMPACTION_DEBUG` 锚点已指向 `writeCompactionReport(...)`。 |

## 6. 登记册额外暴露、但不在 7 条内的差异

以下仅为机器登记结果，供后续复查，不在本 lane 修复：

- `collect_result`：Python 有后台工具结果收集 dispatch，Go 只有 builder/名单。
- `credentials`：Go 无模型工具定义。
- 21 个事件缺 `Emit*`/`Publish` 生产者；3 个事件前端无 handler。
- `crm_summary` 在 Go CRM 关闭构建下不存在。
- 12 个测试目录没有 `// Parity:` 引用。

## 7. 复现与验证

```bash
# 生成
python3 go_hive/scripts/gen_coverage_registry.py

# CI 检查
python3 go_hive/scripts/gen_coverage_registry.py --check

# 单元测试
python3 scripts/test_gen_coverage_registry.py -v

# 语法
python3 -m py_compile go_hive/scripts/gen_coverage_registry.py scripts/test_gen_coverage_registry.py

# 合并收尾
bash go_hive/scripts/merge_postcheck.sh
```

实测结果：

- 连续两次生成，JSON sha256 `4f8ce4c9d543035451f67828832df68c03b697bf7243d7115b9bfac9b6b13e58`，MD sha256 `b19304552d48d1feeeb7d809590d43eb1f16fb8fbaecc322b1353ab86ab6219d`，完全一致（无时间戳抖动）。
- 干净树 `--check`：`coverage-registry: OK (141 rows, 7 known gaps)`，exit 0。
- 反证：在 `agent_loop.py` 注入 `tc.tool_name == "fake_probe_tool"` 后 `--check` exit 1，输出包含 `synthetic_dispatch: added synthetic:fake_probe_tool`；恢复后 exit 0。
- `py_compile`：OK。
- unittest：7 tests OK。
- `merge_postcheck.sh`：exit 0（gofmt clean / wiring_check PASS / 模块清单一致）。
- `ruff check` 与 `ruff format --check`：通过。

## 8. CI 接入建议

本 lane 不直接改 `Makefile`，建议增加独立目标并挂到现有 `check`：

```diff
--- a/Makefile
+++ b/Makefile
@@ -1,4 +1,4 @@
-.PHONY: lint format check test test-tools test-live test-all install-hooks help frontend-install frontend-dev frontend-build
+.PHONY: lint format check coverage-check test test-tools test-live test-all install-hooks help frontend-install frontend-dev frontend-build
@@ -24,8 +24,11 @@ check: ## Run all checks without modifying files (CI-safe)
 	cd core && uv run ruff check .
 	cd tools && uv run ruff check .
 	cd core && uv run ruff format --check .
 	cd tools && uv run ruff format --check .
+
+coverage-check: ## Verify upstream→Go behaviour registry is current
+	python3 go_hive/scripts/gen_coverage_registry.py --check
```

如果希望 `make check` 直接覆盖它，可把第 24 行改为：

```diff
-check: ## Run all checks without modifying files (CI-safe)
+check: coverage-check ## Run all checks without modifying files (CI-safe)
```

`--check` 的失败输出会列出新增/删除/变化的 row id，并提示重新生成并提交 JSON + MD。任何上游或 Go 源文件（含行号锚点）变化都会触发漂移，这是有意设计：登记册必须跟着代码更新。

## 9. 限制与后续

- 事件 emitter 采用严格定义（`Emit*` 或 `Publish` 调用点）；直接入队、由调用方发布的间接生产者会被标为 partial，需要看 `notes` 判断。
- 前端 handler 用事件名字符串匹配；`custom` 这类通用值可能误命中非事件字符串。
- Go 没有显式 phase 工具名单，phase 维度只能证明工具存在，不能证明阶段门；`explicit_phase_lists_in_go=false` 明确记录这一点。
- 测试映射只报目录级计数，且 `core/tests` 是跨模块汇总，会与 package 行重叠；不用于证明逐条测试等价。
- 行号锚点会随代码移动而漂移；`--check` 失败不是脚本 bug，而是要求重新生成并 review。
- 后续可扩展：tool tier / searchable manifest、config key 读写点、env 台账逐项锚点、prompt section 组成、REST route 行为字段。登记册 schema 预留了按 section 追加 row 的空间。
